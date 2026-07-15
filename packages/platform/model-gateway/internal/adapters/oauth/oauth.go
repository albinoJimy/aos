// Package oauth é a camada OAuth multi-provedor do Model Gateway (AOS-056,
// tecnica/06 §4, tecnica/07 §7.1, ADR-006). Traduz o mecanismo de autenticação
// ESPECÍFICO de cada provedor (API key, OAuth de serviço, identidade federada)
// para a credencial de infra que o adaptador de provider apresenta a jusante.
//
// # Fronteira e segredos (ADR-006)
//
// A aquisição corre SEMPRE server-side (dentro do GW); o agente nunca participa
// no fluxo OAuth nem vê o material. O material bruto entra por [Material] (obtido
// do broker/vault) e o token resultante sai encapsulado numa
// [adapters.Credential] — cujo segredo é NÃO-EXPORTADO e redigido. Nenhum tipo
// deste pacote imprime o segredo: [Material.String] e [Token.String] redigem-no,
// e [Material.MarshalJSON]/[Token.MarshalJSON] serializam a FORMA REDIGIDA (o
// segredo é imposto pelo tipo, não apenas omitido por ser não-exportado), de
// modo que um log/span acidental — texto OU JSON — nunca o revela.
//
// Este pacote vive sob model-gateway/internal/ (não importável de fora do GW) —
// a garantia estrutural de que a camada OAuth é detalhe interno do gate.
package oauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
)

// Mecanismos de autenticação por provedor. Modelam a assimetria real: OpenAI usa
// uma API key portadora directa; Anthropic/Claude usa OAuth de serviço
// (client-credentials → access token); Gemini/Google usa identidade federada
// (workload identity → access token com audiência regional).
type Mechanism string

const (
	// MechanismAPIKey — a chave de API É o token portador (pass-through). OpenAI.
	MechanismAPIKey Mechanism = "api_key"
	// MechanismServiceOAuth — OAuth de serviço: troca de client-credentials por um
	// access token de vida curta. Anthropic/Claude.
	MechanismServiceOAuth Mechanism = "service_oauth"
	// MechanismFederated — identidade federada: troca de uma asserção de workload
	// por um access token com audiência regional. Gemini/Google.
	MechanismFederated Mechanism = "federated"
)

// ErrNoMaterial — o material de entrada não tem segredo. Fail-closed: a troca
// NUNCA produz um token vazio (que geraria um pedido não-autenticado a jusante).
var ErrNoMaterial = errors.New("oauth: material sem segredo (fail-closed)")

// Material é o material bruto que o broker/vault entrega ao fluxo OAuth
// server-side (a API key, o client-secret de serviço ou a asserção federada). O
// segredo é NÃO-EXPORTADO e redigido — só os provedores deste pacote o lêem.
type Material struct {
	Provider  string
	Region    string
	ExpiresAt time.Time
	secret    string
}

// NewMaterial constrói [Material] a partir do segredo bruto do vault. O segredo
// fica encapsulado (sem getter exportado).
func NewMaterial(provider, region, secret string, expiresAt time.Time) Material {
	return Material{Provider: provider, Region: region, secret: secret, ExpiresAt: expiresAt}
}

// String redige o segredo (ADR-006).
func (m Material) String() string {
	return "oauth.Material{provider=" + m.Provider + ",region=" + m.Region + ",secret=REDACTED}"
}

// MarshalJSON IMPÕE a redação também em JSON (ADR-006): serializa a forma
// redigida em vez da estrutura, para que um encoding/json acidental (log/span
// estruturado) nunca exponha o segredo. A garantia deixa de depender de o campo
// continuar não-exportado — passa a ser imposta pelo tipo.
func (m Material) MarshalJSON() ([]byte, error) { return json.Marshal(m.String()) }

// Token é o resultado da troca OAuth: o token de infra a apresentar ao provedor,
// com o seu instante de expiração. O segredo é NÃO-EXPORTADO e redigido.
type Token struct {
	ExpiresAt time.Time
	secret    string
}

// String redige o segredo (ADR-006).
func (t Token) String() string { return "oauth.Token{secret=REDACTED}" }

// MarshalJSON IMPÕE a redação também em JSON (ver [Material.MarshalJSON]).
func (t Token) MarshalJSON() ([]byte, error) { return json.Marshal(t.String()) }

// Provider é a porta de um provedor OAuth: sabe o seu mecanismo e troca material
// bruto por uma [adapters.Credential] pronta a injectar server-side.
type Provider interface {
	// Name é o identificador estável do provedor ("openai"|"anthropic"|"google").
	Name() string
	// Mechanism é o mecanismo de autenticação do provedor.
	Mechanism() Mechanism
	// Exchange troca o material bruto pela credencial de infra final e devolve o
	// instante de expiração REAL do token trocado. Fail-closed: material sem
	// segredo devolve [ErrNoMaterial]. NUNCA regista o segredo. A expiração
	// devolvida permite à origem limitar o TTL do cache pela expiração efectiva do
	// token (nunca servir um token já expirado no provedor).
	Exchange(ctx context.Context, m Material) (adapters.Credential, time.Time, error)
}

// exchangeToken aplica o mecanismo a um material e devolve o [Token]. É a lógica
// partilhada, determinista (sem rede, sem rand): OpenAI passa a chave adiante;
// os restantes derivam um access token estável do material via HMAC-SHA256 (um
// stand-in DETERMINISTA da troca real no token endpoint — a integração com o
// endpoint real é infra/EPIC-07). A derivação é unidireccional; o material bruto
// nunca é recuperável do token.
func exchangeToken(mech Mechanism, m Material, ttl time.Duration, clock func() time.Time) (Token, error) {
	if m.secret == "" {
		return Token{}, ErrNoMaterial
	}
	switch mech {
	case MechanismAPIKey:
		// Pass-through: a API key É o portador. A expiração é a do lease do vault.
		return Token{secret: m.secret, ExpiresAt: m.ExpiresAt}, nil
	case MechanismServiceOAuth:
		// client-credentials → access token de vida curta.
		tok := deriveToken("service_oauth:"+m.Provider, m.secret)
		return Token{secret: tok, ExpiresAt: clock().Add(ttl)}, nil
	case MechanismFederated:
		// asserção federada → access token com audiência regional (a região entra
		// na derivação: o token de uma região não é válido noutra).
		tok := deriveToken("federated:"+m.Provider+":"+m.Region, m.secret)
		return Token{secret: tok, ExpiresAt: clock().Add(ttl)}, nil
	default:
		return Token{}, errors.New("oauth: mecanismo desconhecido")
	}
}

// deriveToken produz um access token DETERMINISTA a partir do material via
// HMAC-SHA256(label, material). Não é uma chave real nem é reversível; modela a
// troca no token endpoint sem rede nem aleatoriedade (determinismo dos testes).
func deriveToken(label, material string) string {
	mac := hmac.New(sha256.New, []byte(material))
	mac.Write([]byte(label))
	return label + "." + hex.EncodeToString(mac.Sum(nil))
}

// secretOf devolve o segredo de um Token (uso interno do pacote, ao construir a
// [adapters.Credential]).
func (t Token) secretOf() string { return t.secret }

// baseProvider implementa [Provider] para um dado nome+mecanismo, partilhando a
// troca determinista. O TTL do token e o relógio são injectados (determinismo).
type baseProvider struct {
	name  string
	mech  Mechanism
	ttl   time.Duration
	clock func() time.Time
}

func (p *baseProvider) Name() string         { return p.name }
func (p *baseProvider) Mechanism() Mechanism { return p.mech }

func (p *baseProvider) Exchange(_ context.Context, m Material) (adapters.Credential, time.Time, error) {
	tok, err := exchangeToken(p.mech, m, p.ttl, p.clock)
	if err != nil {
		return adapters.Credential{}, time.Time{}, err
	}
	// O segredo passa directamente para a Credential (segredo não-exportado,
	// redigido). Fora daqui, o token nunca existe como string solta. A expiração
	// REAL do token sobe à origem para limitar o TTL do cache.
	return adapters.NewCredential(m.Provider, m.Region, tok.secretOf()), tok.ExpiresAt, nil
}

// Options configura um provedor OAuth.
type Options struct {
	// TokenTTL é a vida do access token trocado (mecanismos OAuth/federado). Curta
	// por desenho. Ignorada no pass-through (usa o TTL do lease do vault).
	TokenTTL time.Duration
	// Clock é o relógio injectável (default time.Now).
	Clock func() time.Time
}

func (o Options) ttl() time.Duration {
	if o.TokenTTL > 0 {
		return o.TokenTTL
	}
	return 5 * time.Minute
}

func (o Options) clock() func() time.Time {
	if o.Clock != nil {
		return o.Clock
	}
	return time.Now
}

// NewOpenAI constrói o provedor OpenAI (mecanismo API key, pass-through).
func NewOpenAI(opts Options) Provider {
	return &baseProvider{name: "openai", mech: MechanismAPIKey, ttl: opts.ttl(), clock: opts.clock()}
}

// NewAnthropic constrói o provedor Claude/Anthropic (OAuth de serviço).
func NewAnthropic(opts Options) Provider {
	return &baseProvider{name: "anthropic", mech: MechanismServiceOAuth, ttl: opts.ttl(), clock: opts.clock()}
}

// NewGoogle constrói o provedor Gemini/Google (identidade federada).
func NewGoogle(opts Options) Provider {
	return &baseProvider{name: "google", mech: MechanismFederated, ttl: opts.ttl(), clock: opts.clock()}
}

// Registry mapeia nome de provedor → [Provider]. É imutável após construção
// (concorrente-seguro para leitura sob os Fetch paralelos do gateway).
type Registry struct {
	byName map[string]Provider
}

// NewRegistry constrói um registo a partir dos provedores dados.
func NewRegistry(providers ...Provider) *Registry {
	m := make(map[string]Provider, len(providers))
	for _, p := range providers {
		m[p.Name()] = p
	}
	return &Registry{byName: m}
}

// DefaultRegistry regista os três provedores canónicos (OpenAI, Anthropic,
// Google) com as mesmas Options.
func DefaultRegistry(opts Options) *Registry {
	return NewRegistry(NewOpenAI(opts), NewAnthropic(opts), NewGoogle(opts))
}

// Get devolve o provedor pelo nome, ou (nil,false) se não registado.
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.byName[name]
	return p, ok
}
