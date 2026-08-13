package main

// Liga o MODEL GATEWAY REAL (EPIC-06, packages/platform/model-gateway) à porta [Config.Model] do
// nó, apontado a um endpoint OpenAI-compatível (OmniRoute/OpenRouter/…). NÃO duplica o cliente
// OpenAI: reutiliza o adaptador interno do gateway (internal/adapters/openai_http) e o
// [modelgateway.NewModelClient] canónico (RT→GW). O gateway traz a sua allowlist regional ASSINADA
// (go:embed, trust anchor pinado), keypool, routing de failover, metering/pricing e o endurecimento
// SSRF — tudo o que um cliente à parte não teria.
//
// EGRESS EM DEV: injecta-se um [http.Client] simples, o que faz o gateway DELEGAR a validação de
// BaseURL nesse transporte (o mesmo seam que os testes de integração usam para apontar a um
// httptest). Assim o nó fala com o OmniRoute em http na rede interna sem o bloqueio https+allowlist
// do caminho de egress real. Em produção remove-se o HTTPClient e usa-se BaseURL https +
// AllowedEgressHosts (o SSRF fail-closed de AOS-223 volta a valer).
//
// ZERO-DEP preservado: o model-gateway já está no grafo do nó (replace local) e não traz nenhuma
// dependência EXTERNA além do que o nó já tem.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/identity"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/pipeline/authn"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
)

// ErrBadModelAllowlist — o bundle de allowlist EXTERNO está pedido (AOS_MODEL_ALLOWLIST_BUNDLE_DIR)
// mas mal configurado: sem AOS_MODEL_ALLOWLIST_TRUST_ANCHOR, ou o bundle não verifica contra o
// anchor. Fail-closed: quem monta uma allowlist externa obtém-na verificada ou o nó recusa arrancar.
var ErrBadModelAllowlist = errors.New("aos: allowlist externa do model gateway mal configurada — AOS_MODEL_ALLOWLIST_BUNDLE_DIR exige AOS_MODEL_ALLOWLIST_TRUST_ANCHOR (base64 da pubkey ed25519 do assinante, out-of-band); bundle ausente/adulterado/assinado por outra chave e RECUSADO fail-closed")

// loadModelAllowlistFromEnv carrega a policy de allowlist EXTERNA (bundle assinado montado) quando
// AOS_MODEL_ALLOWLIST_BUNDLE_DIR está definido — a via gémea do bundle PDP (AOS-220) que remove o
// acoplamento "modelos fixos no código". Vazio ⇒ nil (o gateway usa a allowlist EMBEBIDA, trust
// anchor pinado; comportamento inalterado). Fail-closed: dir sem anchor, ou bundle não-verificável.
func loadModelAllowlistFromEnv() (*allowlist.Policy, error) {
	dir := strings.TrimSpace(os.Getenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR"))
	if dir == "" {
		return nil, nil
	}
	anchor := strings.TrimSpace(os.Getenv("AOS_MODEL_ALLOWLIST_TRUST_ANCHOR"))
	if anchor == "" {
		return nil, ErrBadModelAllowlist
	}
	pol, err := allowlist.LoadSignedPolicyFromDir(dir, anchor)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadModelAllowlist, err)
	}
	return pol, nil
}

// defaultModelGatewayRegion / defaultModelGatewayBoard casam com a allowlist regional ASSINADA
// EMBEBIDA (regra `board-eu` → {gpt-4o, gpt-4o-mini, text-embedding-3-large} em {eu, eu-west}).
// São os defaults quando NÃO se monta uma allowlist externa. Com um bundle externo
// (AOS_MODEL_ALLOWLIST_BUNDLE_DIR), o operador escolhe o board/região/modelos que quiser
// (AOS_MODEL_BOARD/REGION) — o nome pedido deixa de ter de ser um alias fixo do código.
const (
	defaultModelGatewayRegion = "eu"
	defaultModelGatewayBoard  = "board-eu"
)

// staticModelCredential implementa [modelgateway.CredentialProvider]: devolve o segredo de infra
// (o bearer para o gateway upstream) por (provider, região). Em produção o composition root liga
// aqui o vault/broker (EPIC-07); aqui é o valor lido de AOS_MODEL_API_KEY_PATH. OmniRoute em dev
// pode não exigir bearer — devolve-se um valor não-vazio para o keypool não falhar; o upstream
// ignora-o se não o exigir.
//
// O FALLBACK DE DEV NÃO SE GUARDA AQUI (AOS-247, achado F5). Este `Fetch` corre POR-PEDIDO e não
// tem por onde abortar o arranque: devolver erro daria um nó vivo que anuncia gateway e falha cada
// chamada em runtime, o que é pior diagnóstico do que uma recusa de boot. As DUAS metades da
// remediação vivem por isso na fronteira que lê o ambiente:
//
//   - AOS_MODE=production sem AOS_MODEL_API_KEY_PATH ⇒ [ErrProductionNeedsModelCredential] em
//     [parseModelFromEnv] (o nó não chega a construir-se, quanto mais a apresentar isto);
//   - fora de produção ⇒ [devModelCredentialBanner] declara, em cada arranque, que é o bearer de
//     DEV que está em uso — o estado deixou de ser indistinguível de uma credencial real.
//
// Por construção, então, este ramo só é alcançável num nó de REFERÊNCIA que já o declarou.
type staticModelCredential struct{ secret string }

func (c staticModelCredential) Fetch(_ context.Context, _, _ string) (string, error) {
	if c.secret == "" {
		return "aos-dev-omniroute", nil
	}
	return c.secret, nil
}

// ErrModelIdentityNotComposed — o caminho do modelo foi INVOCADO mas o verifier REAL do nó
// ainda não foi ligado ao estágio authn do GW (AOS-278, CUTOVER DURO). Estruturalmente
// inalcançável no arranque normal: o [Bootstrap] compõe o verifier em AMBOS os modos e
// liga-o (Config.ModelIdentityBinder) ANTES de servir. É a guarda fail-closed do estado
// intermédio (gateway construído na fronteira de ambiente, antes de a identidade existir):
// enquanto não-ligado, o estágio authn NEGA toda a chamada em vez de aceitar sem verificar.
var ErrModelIdentityNotComposed = errors.New("aos: identidade do model gateway nao composta (AOS-278) — o verifier real do no ainda nao foi ligado ao estagio authn do GW; nenhum turno de modelo corre sem identidade real (fail-closed)")

// lateBoundModelVerifier é o verifier do estágio authn do GW ligado TARDIAMENTE (AOS-278).
// O Model Gateway REAL é construído na fronteira de ambiente ([parseModelFromEnv]) ANTES de
// a IDENTIDADE estar composta — o verifier só nasce no [Bootstrap] (bootstrap.go, o MESMO
// integration.NewVerifierFromAuthority que as tool calls usam). Este holder arranca VAZIO e
// NEGA fail-closed ([ErrModelIdentityNotComposed]); o Bootstrap chama [bind] com o verifier
// REAL, fechando o cutover. O ponteiro é atómico: o bind (uma vez, no arranque de um único
// goroutine) e as leituras (por-chamada, nos goroutines dos runs) não corrompem nem correm.
type lateBoundModelVerifier struct {
	v atomic.Pointer[identity.Verifier]
}

// bind liga o verifier REAL. Chamado UMA vez pelo Bootstrap depois de compor a identidade.
func (l *lateBoundModelVerifier) bind(v *identity.Verifier) { l.v.Store(v) }

// Verify implementa [authn.Verifier]: delega no verifier REAL ligado; se ainda não ligado,
// NEGA fail-closed — o nó não verifica tokens de modelo sem a raiz de confiança real.
func (l *lateBoundModelVerifier) Verify(ctx context.Context, compact string) (identity.Principal, error) {
	v := l.v.Load()
	if v == nil {
		return identity.Principal{}, ErrModelIdentityNotComposed
	}
	return v.Verify(ctx, compact)
}

// modelInvokeCapability é a capability que o token do run tem de SELAR no escopo para
// invocar um modelo (casa com token_policy.json embebido do estágio authn: require
// model:invoke para as operações chat/embeddings).
const modelInvokeCapability = "model:invoke"

// nodeModelAuthority é o [authn.AuthorityResolver] do estágio authn do GW no nó (AOS-278). O
// nó CONCEDE a capability de invocação de modelo a QUALQUER principal VERIFICADO — a
// autoridade REAL não vem daqui: vem do ESCOPO SELADO no token NHI do run, com que o estágio
// authn RECONCILIA esta concessão (authn.Stage: effective = utilizador ∩ classe ∩
// token.Scope, menor privilégio). Espelha a descoberta de AOS-071/AOS-156 — a autoridade é
// derivada do TOKEN verificado, não de um directório estático — e vale em AMBOS os modos
// (referência e endurecido, onde NÃO há autoridade in-process, só o verifier). Um token cujo
// escopo NÃO sela model:invoke é negado ATRIBUÍVELMENTE (authn.ErrScopeExceedsSeal / falta a
// capability obrigatória): o nó não alarga o que o token concedeu.
type nodeModelAuthority struct{}

func (nodeModelAuthority) UserAuthority(context.Context, string) ([]string, error) {
	return []string{modelInvokeCapability}, nil
}

func (nodeModelAuthority) ClassAuthority(context.Context, string) ([]string, error) {
	return []string{modelInvokeCapability}, nil
}

// newGatewayModelClient compõe o [modelgateway.Gateway] de produção apontado ao endpoint dado e
// devolve-o já adaptado à porta [agentruntime.ModelClient] via [modelgateway.NewModelClient].
// region/board são a fronteira de soberania que o nó declara ao gateway; pol, se != nil, é a
// allowlist EXTERNA (bundle assinado montado) que substitui a embebida. tools, se não-vazio, é o
// tool set OFERECIDO ao modelo (WithTools → `tools` do request OpenAI) a partir do registry
// (AOS_MODEL_TOOLS); sem ele o modelo não emite tool_calls. Ver modeltools.go.
// gwAudit é o [audit.Store] de governação do gateway (AOS-265). nil ⇒ [audit.NewMemStore]
// de referência (governação VOLÁTIL — o comportamento até AOS-265); != nil ⇒ o WORM
// DURÁVEL resolvido de AOS_MODEL_AUDIT_PATH (ver model_audit_env.go), onde a activação
// da allowlist e as decisões por chamada sobrevivem ao restart.
//
// verifier (AOS-278, CUTOVER DURO) é o verificador de identidade que o estágio authn REAL do
// GW usa em CADA chamada: valida EdDSA + janela + revogação + raiz humana (ADR-003) do token
// NHI do run. Tipicamente é o [lateBoundModelVerifier] (ligado pelo Bootstrap ao verifier
// real do nó); nil ⇒ ErrModelIdentityNotComposed (o gateway não se compõe sem seam de
// identidade — não há fallback para um stub allow-all).
//
// costRec (AOS-259) é a contabilidade de custo do gateway, resolvida por
// [parseModelPricingFromEnv]: != nil quando a tabela de preços em vigor cobre o par
// (model, region) deste nó, e é o que faz o canal de custo transportar um número derivado
// em vez de zero. nil ⇒ sem contabilidade (zero DECLARADO no banner, nunca um preço
// inventado) — ver model_pricing_env.go.
func newGatewayModelClient(verifier authn.Verifier, baseURL, model, apiKeyPath, region, board string, pol *allowlist.Policy, tools []port.Tool, gwAudit audit.Store, costRec *cost.Recorder) (agentruntime.ModelClient, error) {
	// CUTOVER DURO: sem seam de identidade não há gateway. O estágio authn REAL substitui o
	// antigo stub (nodeModelAuthn) que forjava o principal e devolvia allow incondicional.
	if verifier == nil {
		return nil, ErrModelIdentityNotComposed
	}
	// ESTÁGIO AUTHN REAL (AOS-057/AOS-278): verifier real + autoridade do nó (concede
	// model:invoke, reconciliada com o escopo selado no token) + policy-as-code embebida
	// (token_policy.json, default-deny: exige model:invoke para chat/embeddings). Qualquer um
	// nil torna o estágio fail-closed por construção (authn.New).
	authnPolicy, err := authn.LoadPolicy()
	if err != nil {
		return nil, fmt.Errorf("%w: policy de validacao de token do GW (AOS-278): %v", ErrBadModelConfig, err)
	}
	authnStage := authn.New(verifier, nodeModelAuthority{}, authnPolicy)
	secret := ""
	if apiKeyPath != "" {
		raw, err := os.ReadFile(apiKeyPath)
		if err != nil {
			return nil, fmt.Errorf("%w: ler API key: %v", ErrBadModelConfig, err)
		}
		secret = strings.TrimSpace(string(raw))
		if secret == "" {
			return nil, fmt.Errorf("%w: API key vazia em %q", ErrBadModelConfig, apiKeyPath)
		}
	}
	// AUDIT DE GOVERNAÇÃO (AOS-265). Durável quando AOS_MODEL_AUDIT_PATH resolveu um WORM
	// ([parseModelAuditFromEnv]); caso contrário, o MemStore de referência (volátil). O
	// gateway SELA aqui a activação da allowlist e cada decisão de soberania — durabilizá-lo
	// é o que faz o trilho sobreviver a um restart, em vez de re-nascer vazio a cada arranque.
	govAudit := gwAudit
	if govAudit == nil {
		govAudit = audit.NewMemStore()
	}
	// BaseURL = raiz OpenAI-compatível (termina em /v1); o adaptador acrescenta /chat/completions.
	base := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	gw, err := modelgateway.NewProduction(context.Background(), modelgateway.ProductionConfig{
		Provider:      "openai",
		BaseURL:       base,
		HTTPClient:    &http.Client{Timeout: 60 * time.Second}, // seam de dev: delega validação de egress
		DefaultRegion: region,
		Audit:         govAudit, // audit de governação do GW (activação da allowlist + decisões) — durável via AOS_MODEL_AUDIT_PATH (AOS-265)
		Credentials:   staticModelCredential{secret: secret},
		// Uma ÚNICA conta de infra — não há pool por onde distribuir carga. LIMITES A ZERO
		// (⇒ ILIMITADO, contrato keypool.Account: "<=0 = ilimitado") de PROPÓSITO (F1/AOS-203):
		// o keypool é um SELECTOR de chave por throughput, NÃO um rate-limiter — não tem janela
		// temporal e o rpm só SOBE a cada Select (keypool.Select). Um limite finito numa conta
		// única não faz backpressure: vira um FUSÍVEL permanente — à (limite+1).ª chamada ao
		// modelo da vida do processo, g.credential() falharia fail-closed (ErrNoCapacity) para
		// SEMPRE até reiniciar o nó, um brownout silencioso indistinguível de avaria do provider.
		// O tecto de throughput REAL é do gateway EXTERNO (o LiteLLM do deployment endurecido, que
		// tem janela e backpressure a sério). Ver deploy/node/README.md e, como guarda,
		// TestModelGateway_NoThroughputFuse.
		//
		// A CONTA ÚNICA É TAMBÉM O QUE SUSTENTA A GARANTIA DE PREÇO DO ARRANQUE (AOS-259): a
		// cobertura é verificada para o par PEDIDO, mas `Gateway.recordCost` calcula sobre o par
		// RESOLVIDO pelo roteamento de failover. Com um só inventário na região pedida, resolvido
		// == pedido sempre. Quem acrescentar aqui uma segunda conta ou região TEM de estender
		// [resolveModelPricing] a verificar todos os pares alcançáveis — senão um failover resolve
		// um par sem preço, `pricing.ErrNoPrice` recusa a chamada, e o mecanismo de resiliência
		// passa a ser a causa de uma interrupção total.
		Accounts: []modelgateway.InfraAccount{{
			KeyID: "model-upstream", Provider: "openai", Region: region, LimitRPM: 0, LimitTPM: 0,
		}},
		Authn:     authnStage, // estágio authn REAL (AOS-278) — verifica o token NHI do run; sem stub
		Allowlist: pol,        // nil ⇒ allowlist EMBEBIDA (retro-compat); != nil ⇒ bundle externo montado
		// CONTABILIDADE DE CUSTO (AOS-062) ligada ao CANAL de AOS-259: com recorder, cada
		// resposta leva o custo derivado em port.Usage.CostMicroUSD e o adaptador RT→GW
		// projecta-o no turno (span + evento durável que o burn-down lê). nil ⇒ o canal
		// existe e transporta zero — ausência de preço para este par, declarada no banner.
		Cost: costRec,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadModelConfig, err)
	}
	// IDENTIDADE POR-RUN (AOS-278, CUTOVER DURO). WithPrincipalFromContext SOURCE o token
	// NHI do RUN do ctx por-chamada (Goal.Credential, anexado ao runCtx em service.go) —
	// o MESMO token que cada tool call mediada verifica. O adaptador é construído UMA VEZ
	// ao nível do nó, mas a identidade a apresentar é a do run, que só o ctx conhece: a
	// porta correcta é o ctx que flui Run→Call (a mesma mecânica do plano de replay). Sob o
	// cutover duro NÃO se fixa WithPrincipal: sem credencial no ctx o ex.Principal chega
	// VAZIO e o estágio authn nega ATRIBUÍVELMENTE — nunca se forja um principal (o antigo
	// nodeReferencePrincipal "aos-node/aos-agent", que não era um token verificável, foi
	// removido com o stub).
	//
	// WithRun NÃO se liga aqui de propósito: este adaptador é construído UMA VEZ, ao
	// nível do nó, pelo que um runID de construção seria CONSTANTE e agregaria todos os
	// runs no mesmo balde de SLI/atribuição — a "auditoria que não distingue runs" que o
	// A3 §V descreve. A amarra POR-RUN é a porta de aquisição com contexto de AOS-265.
	opts := []modelgateway.RuntimeAdapterOption{
		modelgateway.WithRegionBoard(region, board),
		modelgateway.WithPrincipalFromContext(modelCredentialFromContext),
	}
	if len(tools) > 0 {
		opts = append(opts, modelgateway.WithTools(tools))
	}
	return modelgateway.NewModelClient(gw, model, opts...), nil
}
