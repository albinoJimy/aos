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
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/pipeline"
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

// nodeModelAuthn implementa [pipeline.Stage] — o seam de IDENTIDADE (AOS-057) que torna as decisões
// de soberania do gateway atribuíveis a um principal. Em produção liga-se aqui o estágio real
// (identity.Verifier + policy); no nó de referência resolve um principal determinista quando o
// pedido não traz um. É o gémeo do estágio de teste do model-gateway.
type nodeModelAuthn struct{}

func (nodeModelAuthn) Name() string { return "auth-principal" }

func (nodeModelAuthn) Process(_ context.Context, ex *pipeline.Exchange) error {
	if strings.TrimSpace(ex.Principal) == "" {
		ex.PrincipalUser = "aos-node"
		ex.PrincipalAgent = "aos-agent"
		ex.AgentClass = "reader"
		ex.HumanRoot = "aos-node"
		ex.Principal = "aos-node/aos-agent"
	}
	ex.Record("auth-principal", "allow", "identidade de referencia do no (seam AOS-057)")
	return nil
}

// newGatewayModelClient compõe o [modelgateway.Gateway] de produção apontado ao endpoint dado e
// devolve-o já adaptado à porta [agentruntime.ModelClient] via [modelgateway.NewModelClient].
// region/board são a fronteira de soberania que o nó declara ao gateway; pol, se != nil, é a
// allowlist EXTERNA (bundle assinado montado) que substitui a embebida. tools, se não-vazio, é o
// tool set OFERECIDO ao modelo (WithTools → `tools` do request OpenAI) a partir do registry
// (AOS_MODEL_TOOLS); sem ele o modelo não emite tool_calls. Ver modeltools.go.
func newGatewayModelClient(baseURL, model, apiKeyPath, region, board string, pol *allowlist.Policy, tools []port.Tool) (agentruntime.ModelClient, error) {
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
		Audit:         audit.NewMemStore(), // audit de governação do GW (activação da allowlist + decisões)
		Credentials:   staticModelCredential{secret: secret},
		Accounts: []modelgateway.InfraAccount{{
			KeyID: "model-upstream", Provider: "openai", Region: region, LimitRPM: 120, LimitTPM: 200_000,
		}},
		Authn:     nodeModelAuthn{},
		Allowlist: pol, // nil ⇒ allowlist EMBEBIDA (retro-compat); != nil ⇒ bundle externo montado
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadModelConfig, err)
	}
	opts := []modelgateway.RuntimeAdapterOption{modelgateway.WithRegionBoard(region, board)}
	if len(tools) > 0 {
		opts = append(opts, modelgateway.WithTools(tools))
	}
	return modelgateway.NewModelClient(gw, model, opts...), nil
}
