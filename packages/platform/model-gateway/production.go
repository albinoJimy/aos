package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/routing/failover"
	"github.com/aos-ref/platform/model-gateway/routing/keypool"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
)

// production.go é a COSTURA DE PRODUÇÃO do Model Gateway — o construtor público que
// um composition root (packages/integration) usa para montar um GW FAIL-CLOSED POR
// CONSTRUÇÃO. Existe porque os adaptadores de provider e a aquisição de credenciais
// vivem sob internal/ (a garantia estrutural de no-bypass de AOS-055: nenhum módulo
// externo importa um provider directamente) — logo [New] não é chamável de fora do
// módulo. [NewProduction] fecha essa lacuna sem quebrar o no-bypass: constrói o
// adaptador internamente e expõe apenas seams públicos (audit.Store, provider de
// credenciais, inventário de contas de infra).
//
// # Fail-closed por construção (resposta ao ponto 3 de AOS-058)
//
// Ao contrário de [New] (o construtor de baixo nível, que mantém o default opt-in
// pass-through de AOS-055 para não partir o baseline verde dos tickets anteriores),
// [NewProduction] LIGA SEMPRE os estágios reais: a allowlist regional default-deny
// ([allowlist.LoadAndActivate], que verifica o trust anchor pinado + assinatura e
// SELA a versão no changelog WORM na activação) e o router de failover que resolve a
// região SÓ via a guarda de soberania (routing/failover). NÃO existe caminho de
// produção que caia no passthrough allow-by-default. Por isso o default global da
// biblioteca (pipeline.PassthroughAllowlist) NÃO é flipado — a segurança é garantida
// estruturalmente AQUI, no ápice, e não por um default que partiria AOS-055/056/057.

// Erros fail-closed da montagem de produção.
var (
	// ErrNoAuditStore — falta o audit.Store. Fail-closed: uma decisão de soberania
	// (allow/deny por chamada + changelog de activação) TEM de ser selável no WORM;
	// sem store, o GW recusa arrancar (uma governação não-auditável é inaceitável).
	ErrNoAuditStore = errors.New("modelgateway: NewProduction exige um audit.Store (fail-closed)")
	// ErrNoCredentialProvider — falta o provedor de credenciais. Fail-closed: sem uma
	// fonte de credenciais de infra o GW não pode autenticar chamadas (nunca fail-open).
	ErrNoCredentialProvider = errors.New("modelgateway: NewProduction exige um CredentialProvider (fail-closed)")
	// ErrNoAuthnStage — falta o estágio de identidade (AOS-057). Fail-closed: cada
	// decisão de governação (allow/deny da allowlist, deny de failover cross-border) TEM
	// de ser atribuível a um principal + board — o recorder recusa selar uma decisão
	// anónima (ADR-010). O principal é resolvido pelo estágio de authn; sem ele, nenhuma
	// chamada seria selável e o GW não deve arrancar em modo fail-open.
	ErrNoAuthnStage = errors.New("modelgateway: NewProduction exige o estagio de authn/identidade (AOS-057) para atribuibilidade (fail-closed)")
	// ErrUnknownProvider — o provider pedido não tem adaptador conhecido.
	ErrUnknownProvider = errors.New("modelgateway: provider desconhecido (sem adaptador)")
	// ErrNoBaseURL — o provider HTTP exige um baseURL (endpoint da API).
	ErrNoBaseURL = errors.New("modelgateway: provider HTTP exige baseURL (fail-closed)")
)

// CredentialProvider é o SEAM PÚBLICO de aquisição de credenciais de infra para o
// composition root. Devolve o segredo em claro para o par (provider, região); o
// gateway encapsula-o imediatamente numa credencial de segredo NÃO-EXPORTADO
// ([adapters.Credential]) — o agente e os planos consumidores NUNCA o vêem (ADR-006),
// só o adaptador dentro do módulo. É o composition root (camada de infra de confiança
// que liga o vault/broker de EPIC-07) quem fornece esta fonte, nunca conteúdo de
// tráfego. Fail-closed: um par sem credencial devolve erro (nunca cai para outra conta).
type CredentialProvider interface {
	Fetch(ctx context.Context, provider, region string) (secret string, err error)
}

// InfraAccount descreve uma conta de infra pooled: o KeyID NÃO-SECRETO (ADR-006), o
// provider e a região onde vive, e os limites de throughput. É a FONTE DE VERDADE
// ÚNICA da topologia de infra: [NewProduction] constrói dela TANTO o keypool (escolha
// da chave por throughput, AOS-057) COMO o inventário de endpoints do router de
// failover (candidatos de soberania, AOS-058) — mantendo os dois coerentes.
type InfraAccount struct {
	KeyID    string
	Provider string
	Region   string
	LimitRPM int
	LimitTPM int
}

// ProductionConfig configura a montagem fail-closed de um GW de produção.
type ProductionConfig struct {
	// Provider é o identificador do provedor default ("openai"). Selecciona o adaptador
	// interno (mantido sob internal/ — no-bypass).
	Provider string
	// BaseURL é a raiz da API compatível OpenAI do provedor (ex.: "https://host/v1").
	// Injectável para apontar a um httptest em testes de integração.
	BaseURL string
	// HTTPClient é opcional (nil ⇒ http.DefaultClient); permite injectar o client de
	// um httptest.Server nos testes de integração.
	HTTPClient *http.Client
	// DefaultRegion é a região usada quando o pedido não a especifica.
	DefaultRegion string
	// Authn é o estágio de IDENTIDADE (AOS-057) que valida o token do principal e
	// resolve (utilizador, agente, raiz humana, cadeia) no Exchange. OBRIGATÓRIO
	// (fail-closed): a governação da soberania sela decisões atribuíveis a principal +
	// board, e o principal vem daqui. É o seam onde o estágio real de AOS-057 se liga
	// (o composition root constrói-o com o identity.Verifier + policy-as-code); o seu
	// wiring completo é a dívida de integração de AOS-057, ortogonal a este ticket.
	Authn pipeline.Stage
	// Audit é o audit.Store WORM onde a governação sela (changelog de activação da
	// allowlist + decisões allow/deny por chamada + deny de failover cross-border).
	// OBRIGATÓRIO (fail-closed).
	Audit audit.Store
	// Credentials é a fonte de credenciais de infra. OBRIGATÓRIO (fail-closed) — não há
	// fonte estática por omissão em produção.
	Credentials CredentialProvider
	// Accounts é a topologia de infra (keypool + inventário de failover).
	Accounts []InfraAccount
	// Health reporta se um endpoint (por KeyID + região) está saudável — a saúde do
	// primário para o router de failover. Nil ⇒ todos tratados como saudáveis (a saúde
	// é liveness; a fronteira de soberania é imposta INDEPENDENTEMENTE dela).
	Health func(keyID, region string) bool
	// ActivatedAt é o instante selado no changelog WORM na activação da allowlist.
	// Zero ⇒ usa Clock (ou time.Now).
	ActivatedAt time.Time
	// Clock, Tracer, Variance são opcionais (defaults: time.Now, Noop, descarta).
	Clock    func() time.Time
	Tracer   agentruntime.Tracer
	Variance VarianceSink
}

// NewProduction monta um GW de produção FAIL-CLOSED por construção a partir de seams
// públicos. Passos (todos fail-closed):
//
//  1. valida a config (audit + credenciais + provider obrigatórios);
//  2. ACTIVA a allowlist regional embebida ([allowlist.LoadAndActivate]): verifica o
//     trust anchor pinado + a assinatura e SELA a activação no changelog WORM ANTES de
//     servir tráfego — se qualquer passo falhar, NÃO devolve gateway;
//  3. constrói o router de failover (routing/failover) sobre a mesma policy e o
//     inventário de contas — a região resolve SÓ via a guarda de soberania;
//  4. constrói o keypool (AOS-057) das mesmas contas (escolha da chave por throughput);
//  5. constrói o adaptador de provider interno + a ponte de credenciais;
//  6. compõe o [Gateway] com a allowlist e o router reais ligados (WithAllowlistStage
//     + WithRoutingStage + WithKeyPool) — nenhum estágio pass-through no caminho de
//     produção.
func NewProduction(ctx context.Context, cfg ProductionConfig) (*Gateway, error) {
	if cfg.Audit == nil {
		return nil, ErrNoAuditStore
	}
	if cfg.Credentials == nil {
		return nil, ErrNoCredentialProvider
	}
	if cfg.Authn == nil {
		return nil, ErrNoAuthnStage
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	at := cfg.ActivatedAt
	if at.IsZero() {
		at = clock()
	}

	// (2) Allowlist regional default-deny: carrega+verifica (trust anchor + assinatura)
	// e sela a activação no changelog WORM. Fail-closed: sem policy activada, sem GW.
	govRec := allowlist.NewRecorder(cfg.Audit)
	alStage, pol, err := allowlist.LoadAndActivate(ctx, govRec, at)
	if err != nil {
		return nil, fmt.Errorf("modelgateway: allowlist nao activada (fail-closed): %w", err)
	}

	// (3) Router de failover: fronteira de soberania derivada da MESMA policy; inventário
	// de endpoints das contas de infra; deny cross-border selado pelo mesmo recorder.
	inv := inventoryFromAccounts(cfg.Accounts)
	routeStage := failover.NewStage(pol, inv,
		failover.WithHealth(adaptHealth(cfg.Health)),
		failover.WithRecorder(govRec),
	)

	// (4) Keypool: escolha da chave por throughput, DESACOPLADA da identidade (AOS-057).
	kp := keypoolFromAccounts(cfg.Accounts)

	// (5) Adaptador de provider interno (no-bypass) + ponte de credenciais.
	adapter, err := newProviderAdapter(cfg.Provider, cfg.BaseURL, cfg.HTTPClient)
	if err != nil {
		return nil, err
	}
	creds := &credProviderSource{p: cfg.Credentials}

	// (6) Composição fail-closed por construção.
	opts := []Option{
		WithAuthnStage(cfg.Authn),
		WithAllowlistStage(alStage),
		WithRoutingStage(routeStage),
		WithKeyPool(kp),
		WithCredentialSource(creds),
		WithClock(clock),
	}
	if cfg.DefaultRegion != "" {
		opts = append(opts, WithDefaultRegion(cfg.DefaultRegion))
	}
	if cfg.Tracer != nil {
		opts = append(opts, WithTracer(cfg.Tracer))
	}
	if cfg.Variance != nil {
		opts = append(opts, WithVarianceSink(cfg.Variance))
	}
	return New(adapter, opts...), nil
}

// inventoryFromAccounts projecta as contas de infra no inventário de endpoints do
// router de failover (provider → endpoints KeyID+região). Contas sem KeyID/região são
// ignoradas (não são endpoints roteáveis).
func inventoryFromAccounts(accounts []InfraAccount) failover.StaticInventory {
	inv := failover.StaticInventory{}
	for _, a := range accounts {
		if a.KeyID == "" || a.Provider == "" || a.Region == "" {
			continue
		}
		inv[a.Provider] = append(inv[a.Provider], sovereignty.Endpoint{KeyID: a.KeyID, Region: a.Region})
	}
	return inv
}

// keypoolFromAccounts constrói o [keypool.Registry] das mesmas contas: um pool por
// par (provider, região) com as contas dessa região e os seus limites de throughput.
func keypoolFromAccounts(accounts []InfraAccount) *keypool.Registry {
	type key struct{ provider, region string }
	grouped := map[key][]keypool.Account{}
	for _, a := range accounts {
		if a.KeyID == "" || a.Provider == "" || a.Region == "" {
			continue
		}
		k := key{a.Provider, a.Region}
		grouped[k] = append(grouped[k], keypool.Account{KeyID: a.KeyID, LimitRPM: a.LimitRPM, LimitTPM: a.LimitTPM})
	}
	reg := keypool.NewRegistry()
	for k, accs := range grouped {
		reg.Register(k.provider, k.region, keypool.NewPool(accs...))
	}
	return reg
}

// adaptHealth adapta a função de saúde pública (keyID, região) à
// [sovereignty.HealthFunc]. Nil ⇒ todos saudáveis (a saúde é liveness; o controlo de
// soberania — o descarte cross-border — é imposto independentemente dela).
func adaptHealth(h func(keyID, region string) bool) sovereignty.HealthFunc {
	if h == nil {
		return func(sovereignty.Endpoint) bool { return true }
	}
	return func(e sovereignty.Endpoint) bool { return h(e.KeyID, e.Region) }
}

// newProviderAdapter constrói o adaptador de provider INTERNO (mantido sob internal/,
// no-bypass). Hoje só o wire OpenAI-compatible (AOS-055/056); outros provedores
// entram por aqui sem alterar o composition root.
func newProviderAdapter(provider, baseURL string, client *http.Client) (adapters.Adapter, error) {
	switch provider {
	case "openai", "anthropic", "google":
		if baseURL == "" {
			return nil, ErrNoBaseURL
		}
		return adapters.NewOpenAIHTTPAdapter(provider, baseURL, client), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
	}
}

// credProviderSource faz a ponte entre o [CredentialProvider] público (segredo em
// claro fornecido pelo composition root) e a porta interna
// [adapters.CredentialSource]: o segredo é encapsulado numa [adapters.Credential] de
// campo NÃO-EXPORTADO no acto (ADR-006 — a redacção imposta pelo tipo garante que
// nunca aparece em logs/spans a jusante). Fail-closed: um segredo vazio é ausência de
// credencial ([adapters.ErrNoCredential]), nunca um pedido não-autenticado.
type credProviderSource struct{ p CredentialProvider }

// Fetch implementa [adapters.CredentialSource].
func (c *credProviderSource) Fetch(ctx context.Context, provider, region string) (adapters.Credential, error) {
	secret, err := c.p.Fetch(ctx, provider, region)
	if err != nil {
		return adapters.Credential{}, err
	}
	if secret == "" {
		return adapters.Credential{}, adapters.ErrNoCredential
	}
	return adapters.NewCredential(provider, region, secret), nil
}

// Compile-time: a ponte satisfaz a porta interna de credenciais.
var _ adapters.CredentialSource = (*credProviderSource)(nil)

// Compile-time: os construtores de estágio satisfazem [pipeline.Stage] (defesa contra
// drift de assinatura ao refactorizar os estágios reais).
var (
	_ pipeline.Stage = (*failover.Stage)(nil)
	_ pipeline.Stage = (*allowlist.Stage)(nil)
)
