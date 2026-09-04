package broker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"sync/atomic"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/broker/internal/vault"
	"github.com/aos-ref/substrate/eventstore"
)

// handleEntropyBytes é a entropia (em bytes) do handle opaco. 128 bits tornam o
// handle NÃO-ADIVINHÁVEL/ENUMERÁVEL.
const handleEntropyBytes = 16

// newHandle gera um Handle OPACO de ALTA ENTROPIA (128 bits, base64url).
//
// A injecção é autorizada por POSSE do handle (a porta [sandbox.CredentialInjector]
// não transporta run/principal para binding), pelo que o handle É a credencial de
// capacidade: TEM de ser não-adivinhável. Um id sequencial/derivado de campos
// não-secretos (provider/region/contador) seria trivialmente enumerável e permitiria
// usar uma lease de outra run/utilizador dentro do TTL — contornando o escopo
// utilizador ∩ classe na camada de injecção. Falha fail-closed se faltar entropia
// (sem handle não-adivinhável, não se emite handle).
func newHandle() (Handle, error) {
	var buf [handleEntropyBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return Handle("h-" + base64.RawURLEncoding.EncodeToString(buf[:])), nil
}

// DefaultExchangeToolID é o id sob o qual a troca de credenciais é registada no
// Reference Monitor. O [ScopeGate] tem de ser construído com o MESMO id.
const DefaultExchangeToolID = "broker.credential.exchange"

// exchangeEventType é o tipo do evento de troca selado no Event Store (sem valor).
const exchangeEventType = "credential.exchange.issued"

// taintTrusted marca a troca como acção do plano de controlo (não conteúdo
// untrusted). O gate de taint do RM recusa que untrusted autorize privilégio.
const taintTrusted = "trusted"

// Downstream descreve a credencial downstream pedida. Todos os campos são
// NÃO-SECRETOS (o valor obtém-se server-side no Vault por esta chave).
type Downstream struct {
	Provider      string // ex.: "stripe"
	Region        string // ex.: "eu"
	Capability    string // capability trocada (ex.: "cap:payments.charge")
	ResourceType  string // ex.: "url" (contrato C1 do RM)
	ResourceValue string // ex.: "https://api.stripe.com/charges" (não-secreto)
}

// ExchangeRequest é o pedido de troca do token scoped por uma credencial
// downstream. NÃO transporta segredos: o token NHI é um bearer efémero (não o
// segredo downstream) e a credencial resolve-se server-side.
type ExchangeRequest struct {
	RunID      string
	StepID     string
	Principal  referencemonitor.Principal // quem (NHI, classe, autoridade do utilizador)
	Credential string                     // token NHI apresentado ao RM (não é o segredo)
	Downstream Downstream                 // para quê
}

// exchangeInput é o envelope NÃO-SECRETO passado à ToolFunc despachada pelo RM.
type exchangeInput struct {
	RunID         string   `json:"run_id"`
	StepID        string   `json:"step_id"`
	PrincipalNHI  string   `json:"principal_nhi"`
	AgentClass    string   `json:"agent_class"`
	UserAuthority []string `json:"user_authority"`
	Provider      string   `json:"provider"`
	Region        string   `json:"region"`
	Capability    string   `json:"capability"`
	ResourceValue string   `json:"resource_value"`
}

// Broker troca tokens scoped por credenciais downstream, mediado pelo Reference
// Monitor e apoiado num Vault. Construir com [New]. Concorrente-seguro.
type Broker struct {
	rm             *referencemonitor.Monitor
	vault          vault.Client
	es             eventstore.EventStore
	store          *leaseStore
	clock          func() time.Time
	ttl            time.Duration
	toolID         string
	classScopes    map[string][]string
	classProviders map[string][]string // AOS-324; nil ⇒ ProviderPostureUnset

	seq atomic.Uint64 // contador determinista de ids de lease
}

// Option configura o [Broker].
type Option func(*Broker)

// WithClock injecta o relógio (TTL determinista nos testes). Por omissão time.Now.
func WithClock(f func() time.Time) Option { return func(b *Broker) { b.clock = f } }

// WithTTL define o TTL CURTO das credenciais downstream. Por omissão 5 minutos.
func WithTTL(d time.Duration) Option { return func(b *Broker) { b.ttl = d } }

// WithToolID sobrepõe o id de tool da troca (default [DefaultExchangeToolID]).
func WithToolID(id string) Option { return func(b *Broker) { b.toolID = id } }

// WithClassScopes regista o escopo-máximo por classe de agente (AOS-057),
// consultado na verificação defensiva de escopo do lado server-side.
func WithClassScopes(m map[string][]string) Option {
	return func(b *Broker) {
		cp := make(map[string][]string, len(m))
		for k, v := range m {
			cp[k] = append([]string(nil), v...)
		}
		b.classScopes = cp
	}
}

// WithClassProviders declara a política do eixo PROVIDER (AOS-324): o mapa
// AgentClass → provedores autorizados, consultado tanto pelo [ScopeGate] que
// [Broker.ScopeGate] produz como pela verificação defensiva server-side de dispatch.
//
// Declará-la é o INTERRUPTOR da imposição: o broker passa a
// [ProviderPostureEnforced] e um pedido para um provedor fora da autoridade
// efectiva é NEGADO ([ErrProviderOutOfScope]). Sem esta opção o broker fica em
// [ProviderPostureUnset] — estado DECLARADO, devolvido por [Broker.ProviderPosture]
// e SELADO em cada troca (`provider_policy` no evento). É pré-condição do wiring
// (DEF-218) que o nó a declare. Use [ProviderAny] para uma classe sem restrição.
func WithClassProviders(m map[string][]string) Option {
	return func(b *Broker) { b.classProviders = copyProviderPolicy(m) }
}

// New constrói o Broker e REGISTA a troca como ToolFunc no Reference Monitor: a
// partir daí a única via de troca é [referencemonitor.Monitor.Mediate]. Falha
// fail-closed se faltar RM/Vault/Event Store ou se o toolID já estiver registado.
func New(rm *referencemonitor.Monitor, vlt vault.Client, es eventstore.EventStore, opts ...Option) (*Broker, error) {
	if rm == nil {
		return nil, ErrNilMonitor
	}
	if vlt == nil {
		return nil, ErrNilVault
	}
	if es == nil {
		return nil, ErrNilEventStore
	}
	b := &Broker{
		rm:     rm,
		vault:  vlt,
		es:     es,
		store:  newLeaseStore(),
		clock:  time.Now,
		ttl:    5 * time.Minute,
		toolID: DefaultExchangeToolID,
	}
	for _, o := range opts {
		o(b)
	}
	if b.clock == nil {
		b.clock = time.Now
	}
	if b.ttl <= 0 {
		b.ttl = 5 * time.Minute
	}
	if b.toolID == "" {
		return nil, ErrEmptyToolID
	}
	if err := rm.Register(b.toolID, b.dispatch); err != nil {
		return nil, err
	}
	return b, nil
}

// ToolID devolve o id sob o qual a troca está registada no RM.
func (b *Broker) ToolID() string { return b.toolID }

// ScopeGate devolve o hook de escopo a inserir na cadeia do Reference Monitor para
// NEGAR trocas fora do escopo já na mediação, nos DOIS eixos que o broker impõe:
// capability (utilizador ∩ classe, AOS-057) e provider (AOS-324). Propaga a
// política registada em [WithClassProviders] — é a via RECOMENDADA de composição,
// porque garante que o gate e a defesa server-side partilham a MESMA política.
func (b *Broker) ScopeGate() ScopeGate {
	return NewScopeGate(b.toolID, b.classScopes, WithGateClassProviders(b.classProviders))
}

// ProviderPosture devolve a postura DECLARADA do eixo provider deste broker
// ([ProviderPostureEnforced] se [WithClassProviders] foi declarado,
// [ProviderPostureUnset] caso contrário). É o valor selado em cada troca no campo
// `provider_policy` do evento, e o que o wiring (DEF-218) deve assertar.
func (b *Broker) ProviderPosture() ProviderPosture { return providerPosture(b.classProviders) }

// Exchange troca o token scoped por uma credencial downstream, MEDIADA pelo
// Reference Monitor. Devolve um [Handle] OPACO (nunca o segredo). Uma decisão que
// não seja permit (ex.: fora do escopo) devolve [*DeniedError] fail-closed; um
// erro de resolução no Vault (fail-closed) é devolvido tal como.
//
// INVARIANTE: o valor da credencial NUNCA regressa por esta função — só o handle.
func (b *Broker) Exchange(ctx context.Context, req ExchangeRequest) (Handle, error) {
	in := exchangeInput{
		RunID:         req.RunID,
		StepID:        req.StepID,
		PrincipalNHI:  req.Principal.NHIID,
		AgentClass:    req.Principal.AgentClass,
		UserAuthority: req.Principal.Authority,
		Provider:      req.Downstream.Provider,
		Region:        req.Downstream.Region,
		Capability:    req.Downstream.Capability,
		ResourceValue: req.Downstream.ResourceValue,
	}
	input, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	call := referencemonitor.Call{
		RunID:      req.RunID,
		StepID:     req.StepID,
		ToolID:     b.toolID,
		Capability: req.Downstream.Capability,
		Resource: referencemonitor.Resource{
			Type:   req.Downstream.ResourceType,
			Value:  req.Downstream.ResourceValue,
			Region: req.Downstream.Region,
		},
		Principal:  req.Principal,
		Credential: req.Credential,
		Context:    referencemonitor.CallContext{Taint: taintTrusted},
		Input:      input,
	}
	dec, err := b.rm.Mediate(ctx, call)
	if err != nil {
		return "", err // cancelamento de contexto
	}
	if dec.Effect != referencemonitor.EffectPermit {
		return "", &DeniedError{Effect: string(dec.Effect), Code: dec.Code, Reason: dec.Reason, DeniedBy: dec.DeniedBy}
	}
	if dec.ToolErr != nil {
		return "", dec.ToolErr // ex.: material ausente no vault, escopo inconsistente
	}
	return Handle(dec.Output), nil
}

// dispatch é a [referencemonitor.ToolFunc] registada no RM. NÃO-EXPORTADA: só o
// dispatcher interno do RM a invoca, e só sob um permit válido. Corre server-side:
// resolve o segredo no Vault, cria a lease (TTL/revogável), sela o registo da
// troca SEM o valor e devolve APENAS o handle opaco. O segredo NUNCA entra no
// output (que poderia chegar ao agente): fica encapsulado na lease server-side.
func (b *Broker) dispatch(ctx context.Context, input []byte) ([]byte, error) {
	var in exchangeInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}

	// Verificação DEFENSIVA de escopo server-side (belt-and-suspenders face ao
	// ScopeGate na mediação): só se troca por credenciais consistentes com a
	// autoridade efectiva utilizador ∩ classe (AOS-057). Fail-closed.
	if b.classScopes != nil {
		classScope := b.classScopes[in.AgentClass]
		if !permitsCapability(in.UserAuthority, classScope, in.Capability) {
			return nil, ErrOutOfScope
		}
	}

	// Verificação DEFENSIVA do eixo PROVIDER server-side (AOS-324), no mesmo molde:
	// a chave do Vault é montada a partir do PEDIDO, pelo que o provedor tem de ser
	// autorizado ANTES de a chave existir. Corre SEMPRE — sob
	// [ProviderPostureUnset] só nega o provedor vazio; sob
	// [ProviderPostureEnforced] impõe a autoridade efectiva de provedor. A negação é
	// ATRIBUÍVEL ([ErrProviderOutOfScope]/[ErrProviderUndetermined]) e NUNCA se
	// confunde com [ErrNoMaterial].
	if err := authorizeProvider(b.classProviders, in.AgentClass, in.UserAuthority, in.Provider); err != nil {
		return nil, err
	}

	key := vault.Key{Provider: in.Provider, Region: in.Region, Capability: in.Capability}
	secret, err := b.vault.Fetch(ctx, key)
	if err != nil {
		return nil, err // fail-closed (ErrNoMaterial)
	}

	now := b.clock()
	// leaseID é o id de GESTÃO/AUDITORIA (não-secreto, determinista): identifica a
	// lease em revogação e no Event Store. NUNCA autoriza a injecção — quem autoriza
	// é o handle (opaco, alta entropia). Manter o leaseID enumerável é seguro; o
	// handle é que TEM de ser não-adivinhável (ver newHandle).
	n := b.seq.Add(1)
	leaseID := "lease-" + in.Provider + "-" + in.Region + "-" + strconv.FormatUint(n, 10)
	handle, err := newHandle()
	if err != nil {
		return nil, err // fail-closed: sem handle não-adivinhável, sem emissão
	}
	lease := &Lease{
		ID:           leaseID,
		Handle:       handle,
		RunID:        in.RunID,
		PrincipalNHI: in.PrincipalNHI,
		Resource:     in.ResourceValue,
		Provider:     in.Provider,
		Region:       in.Region,
		Capability:   in.Capability,
		IssuedAt:     now,
		ExpiresAt:    now.Add(b.ttl),
		secret:       secret,
	}
	b.store.put(lease)

	// Sela o registo da troca no Event Store SEM o valor (audit-before-effect da
	// injecção): quem/para quê/quando + lease-id/handle NÃO-SECRETOS (ADR-006/010).
	if err := b.recordExchange(ctx, lease); err != nil {
		return nil, err // fail-closed: uma troca não-auditável não emite handle
	}
	return []byte(handle), nil
}

// exchangePayload é o payload NÃO-SECRETO do evento de troca. NÃO contém o valor.
type exchangePayload struct {
	LeaseID      string `json:"lease_id"`
	Handle       string `json:"handle"`
	PrincipalNHI string `json:"principal_nhi"`
	Resource     string `json:"resource"`
	Provider     string `json:"provider,omitempty"`
	Region       string `json:"region,omitempty"`
	Capability   string `json:"capability"`
	IssuedAt     string `json:"issued_at"`
	ExpiresAt    string `json:"expires_at"`
	// ProviderPolicy sela a POSTURA do eixo provider em vigor NESTA troca
	// ("enforced"/"unset", ver [ProviderPosture]). É o que torna o estado por
	// omissão LEGÍVEL no audit em vez de silencioso: uma troca emitida sem política
	// de provedores declarada fica marcada como tal, greppável no Event Store
	// (AOS-324).
	ProviderPolicy string `json:"provider_policy"`
}

func (b *Broker) recordExchange(ctx context.Context, l *Lease) error {
	payload, err := json.Marshal(exchangePayload{
		LeaseID:        l.ID,
		Handle:         string(l.Handle),
		PrincipalNHI:   l.PrincipalNHI,
		Resource:       l.Resource,
		Provider:       l.Provider,
		Region:         l.Region,
		Capability:     l.Capability,
		IssuedAt:       l.IssuedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:      l.ExpiresAt.UTC().Format(time.RFC3339Nano),
		ProviderPolicy: string(b.ProviderPosture()),
	})
	if err != nil {
		return err
	}
	_, err = b.es.Append(ctx, l.RunID, eventstore.EventInput{
		Type:    exchangeEventType,
		Payload: payload,
		RunID:   l.RunID,
		StepID:  "exchange:" + l.ID, // idempotency_key estável por lease (não-secreto)
		Producer: eventstore.Producer{
			NHIID: l.PrincipalNHI,
			Scope: []string{l.Capability},
		},
	})
	return err
}
