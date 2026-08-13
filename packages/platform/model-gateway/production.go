package modelgateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
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
	// ErrInsecureBaseURL — o BaseURL (ou um alvo de redirect) do provider NÃO usa
	// https. Fail-closed (SSRF, AOS-223): um esquema não-https (http, file, gopher,
	// …) no caminho de egress real é recusado ANTES de qualquer chamada.
	ErrInsecureBaseURL = errors.New("modelgateway: BaseURL de egress tem de ser https (fail-closed SSRF)")
	// ErrHostNotAllowed — o host do BaseURL (ou de um redirect) NÃO está na allowlist
	// de egress. Fail-closed (SSRF, AOS-223): um host fora da allowlist — incluindo
	// metadados de nuvem (169.254.169.254), loopback ou qualquer alvo interno — é
	// recusado. Uma allowlist vazia nega tudo.
	ErrHostNotAllowed = errors.New("modelgateway: host de egress fora da allowlist (fail-closed SSRF)")
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
	// HTTPClient é opcional. Se nil, o gateway constrói um cliente ENDURECIDO para o
	// egress REAL (timeout + TLS 1.2 + limite de redirect + re-validação de cada salto
	// de redirect contra AllowedEgressHosts) e VALIDA o BaseURL (https + allowlist)
	// antes de arrancar. Se um client for injectado (o seam de httptest/integração), é
	// esse transporte de confiança que governa o egress e a validação de BaseURL é
	// delegada nele — é como os testes apontam a um httptest.Server (http).
	HTTPClient *http.Client
	// AllowedEgressHosts é a ALLOWLIST de hosts (SSRF, AOS-223) a que o gateway pode
	// fazer egress no caminho real (HTTPClient nil). Consultada na validação do BaseURL
	// e re-validada em cada salto de redirect. Fail-closed: no caminho de egress real,
	// uma allowlist VAZIA nega tudo. Ignorada quando um HTTPClient é injectado (o
	// transporte injectado é a fronteira de confiança nesse caso).
	AllowedEgressHosts []string
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
	// Cost é a CONTABILIDADE DE CUSTO (AOS-062) — o agregador que deriva o custo em
	// micro-USD de cada chamada pela tabela de preços versionada, agrega por run/árvore e
	// alimenta o burn-down. Nil ⇒ SEM contabilidade: o gateway serve na mesma e o canal de
	// custo de AOS-259 transporta ZERO até ao runtime (ausência de dados declarada, nunca
	// custo nulo — ver [port.Usage.CostMicroUSD]).
	//
	// É opcional e não obrigatório porque o custo é FAIL-CLOSED por (modelo, região): com
	// um recorder ligado, uma chamada a um par SEM preço na tabela é RECUSADA (nunca
	// facturada a zero). Compor um recorder cujo tabela não cobre o modelo do nó tornaria
	// TODAS as chamadas impossíveis — por isso quem compõe decide, com a tabela em mão, se
	// o par está coberto. Ver o wiring do nó (model_pricing_env.go).
	Cost *cost.Recorder
	// Allowlist é a policy regional EXTERNA (bundle assinado montado, carregada via
	// [allowlist.LoadSignedPolicyFromDir] com o trust anchor OUT-OF-BAND). Presente ⇒ substitui a
	// policy EMBEBIDA (fim do acoplamento "modelos fixos no código"; o operador curadoria/assina o
	// catálogo). Nil ⇒ usa a embebida (trust anchor pinado) — comportamento inalterado. A
	// governança (default-deny + assinatura + selagem WORM na activação) é IDÊNTICA nos dois casos.
	Allowlist *allowlist.Policy
	// Routing é o REFINO de roteamento (AOS-059 + ADR-021) encadeado a jusante da
	// guarda de soberania: carga real, tier mais barato CAPAZ, degradação graciosa por
	// orçamento, admissão global e ranking ponderado com pesos assinados. Zero-valor ⇒
	// o slot de roteamento fica só com o failover (comportamento anterior a AOS-280,
	// sem regressão). Declarar [RoutingConfig.Tiers] é o que ARMA a cadeia — ver
	// production_routing.go.
	Routing RoutingConfig
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
//  4. constrói o keypool (AOS-057) das mesmas contas (escolha da chave por throughput)
//     e, se o deployment declarou a escada de tiers, ENCADEIA o refino cost/load-aware
//     (AOS-280: failover → routingstage+router com o scoring assinado armado);
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
	var alStage *allowlist.Stage
	var err error
	pol := cfg.Allowlist
	if pol != nil {
		// Policy EXTERNA (bundle assinado montado): activa e sela IGUAL à embebida; só a
		// proveniência muda (o operador assinou-a com a sua chave, verificada out-of-band já no
		// carregamento). Remove o acoplamento "modelos fixos no código".
		alStage, err = allowlist.ActivateWith(ctx, govRec, at, pol)
	} else {
		// Policy EMBEBIDA (trust anchor pinado) — comportamento inalterado, retro-compat.
		alStage, pol, err = allowlist.LoadAndActivate(ctx, govRec, at)
	}
	if err != nil {
		return nil, fmt.Errorf("modelgateway: allowlist nao activada (fail-closed): %w", err)
	}

	// (3) Router de failover: fronteira de soberania derivada da MESMA policy; inventário
	// de endpoints das contas de infra; deny cross-border selado pelo mesmo recorder.
	inv := inventoryFromAccounts(cfg.Accounts)
	failStage := failover.NewStage(pol, inv,
		failover.WithHealth(adaptHealth(cfg.Health)),
		failover.WithRecorder(govRec),
	)

	// (4) Keypool: escolha da chave por throughput, DESACOPLADA da identidade (AOS-057).
	kp := keypoolFromAccounts(cfg.Accounts)

	// (4-bis) REFINO de roteamento (AOS-280): o slot "roteamento" passa a ser a CADEIA
	// failover → routingstage+router (carga, tier capaz mais barato, degradação por
	// orçamento e ranking ponderado assinado) quando o deployment declara a escada de
	// tiers, mais o elo que SELA no WORM a troca de modelo que o refino decida (o
	// mesmo recorder de governação da allowlist). Sem escada declarada, fica o failover
	// sozinho — inalterado. Ver production_routing.go para o desenho e as armadilhas.
	routeStage, err := composeRoutingStage(failStage, cfg, pol, inv, kp, govRec)
	if err != nil {
		return nil, err
	}

	// (5) Adaptador de provider interno (no-bypass) + ponte de credenciais. No caminho
	// de egress REAL (HTTPClient nil) o BaseURL é validado (https + allowlist) e um
	// cliente endurecido/allowlist-aware é construído ANTES de servir tráfego (SSRF,
	// AOS-223) — fail-closed: um BaseURL malicioso não produz gateway.
	adapter, err := newProviderAdapter(cfg.Provider, cfg.BaseURL, cfg.HTTPClient, cfg.AllowedEgressHosts)
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
	if cfg.Cost != nil {
		opts = append(opts, WithCost(cfg.Cost))
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
//
// SSRF (AOS-223): no caminho de egress REAL (client == nil) o BaseURL é VALIDADO
// (esquema https obrigatório + host na allowlist) e um cliente endurecido
// allowlist-aware é construído — um BaseURL malicioso (http, host interno,
// não-allowlisted) é recusado fail-closed ANTES de qualquer chamada. Quando um
// client é injectado (httptest/integração), esse transporte de confiança governa e
// a validação é delegada nele.
func newProviderAdapter(provider, baseURL string, client *http.Client, allowedHosts []string) (adapters.Adapter, error) {
	switch provider {
	case "openai", "anthropic", "google":
		if baseURL == "" {
			return nil, ErrNoBaseURL
		}
		if client == nil {
			allow := newHostAllowlist(allowedHosts)
			if err := validateEgressURL(baseURL, allow); err != nil {
				return nil, err
			}
			client = newHardenedEgressClient(allow)
		}
		return adapters.NewOpenAIHTTPAdapter(provider, baseURL, client), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
	}
}

// egressTimeout e egressMaxRedirects endurecem o cliente HTTP de egress REAL do
// gateway (defeito (a) de AOS-223): timeout explícito e limite de redirect, contra
// o http.DefaultClient nu (sem timeout/TLS/limite).
const (
	egressTimeout      = 30 * time.Second
	egressMaxRedirects = 5
)

// hostAllowlist é o conjunto de destinos (host, ou host:porta) a que o gateway pode
// fazer egress. Vazio ⇒ nega tudo (fail-closed, SSRF). A porta faz parte da chave: uma
// entrada NUA ("host") permite só a porta https default (443); um destino numa porta
// não-default TEM de ser allowlisted explicitamente como "host:porta" — fail-closed,
// não se assume que um host allowlisted é alcançável em qualquer porta (defesa contra
// desviar o egress para uma porta interna — SSH, admin — de um host allowlisted, ex.:
// via 3xx para https://host-allowlisted:22/).
type hostAllowlist map[string]struct{}

// newHostAllowlist normaliza a lista de destinos numa allowlist (lowercase, sem
// espaços; entradas vazias ignoradas). Uma entrada "host:443" colapsa para o host nu
// (443 é a porta https default e é o que uma entrada nua já permite); uma entrada
// "host:porta" com porta não-default é mantida como par exacto.
func newHostAllowlist(hosts []string) hostAllowlist {
	set := make(hostAllowlist, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if host, port, err := net.SplitHostPort(h); err == nil {
			if port == "443" {
				set[host] = struct{}{} // porta https default: colapsa para o host nu
			} else {
				set[host+":"+port] = struct{}{}
			}
			continue
		}
		set[h] = struct{}{} // entrada só-host (sem porta)
	}
	return set
}

// permits reporta se o destino (host bare + porta do URL, "" quando ausente) está na
// allowlist. A porta https default (443) ou ausente é satisfeita por uma entrada NUA
// "host"; qualquer outra porta exige uma entrada exacta "host:porta". Uma allowlist
// vazia nunca permite (fail-closed).
func (s hostAllowlist) permits(host, port string) bool {
	host = strings.ToLower(host)
	if port == "" || port == "443" {
		if _, ok := s[host]; ok {
			return true
		}
	}
	if port != "" {
		if _, ok := s[host+":"+port]; ok {
			return true
		}
	}
	return false
}

// validateEgressURL valida um URL de egress fail-closed (SSRF, AOS-223): esquema
// https OBRIGATÓRIO + par (host, porta) na allowlist. Qualquer desvio — URL inválido,
// esquema não-https, host vazio, host fora da allowlist, OU porta não-default de um
// host cuja entrada é nua — é RECUSADO. É a mesma política aplicada ao BaseURL inicial
// E a cada salto de redirect (fecha o SSRF-via-redirect, incluindo o desvio para uma
// porta interna de um host allowlisted).
func validateEgressURL(raw string, allow hostAllowlist) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%w: URL invalido: %v", ErrInsecureBaseURL, err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("%w: esquema %q", ErrInsecureBaseURL, u.Scheme)
	}
	host := u.Hostname() // host sem porta
	if host == "" {
		return fmt.Errorf("%w: sem host", ErrHostNotAllowed)
	}
	if !allow.permits(host, u.Port()) {
		return fmt.Errorf("%w: %q", ErrHostNotAllowed, u.Host)
	}
	return nil
}

// newHardenedEgressClient constrói o cliente de egress REAL do gateway: timeout
// explícito, política de TLS mínima (TLS 1.2) e uma política de redirect que (i)
// limita o número de saltos e (ii) RE-VALIDA cada alvo de redirect contra a mesma
// allowlist https (fecha o SSRF-via-redirect — um 3xx para http ou para um host
// interno é recusado a meio do fluxo). Fecha os defeitos (a) e (b) de AOS-223 no
// caminho de egress real.
func newHardenedEgressClient(allow hostAllowlist) *http.Client {
	return &http.Client{
		Timeout: egressTimeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= egressMaxRedirects {
				return fmt.Errorf("modelgateway: demasiados redirects no egress (%d, max %d)", len(via), egressMaxRedirects)
			}
			return validateEgressURL(req.URL.String(), allow)
		},
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
