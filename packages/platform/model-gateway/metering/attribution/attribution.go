// Package attribution produz o REGISTO DE ATRIBUIÇÃO por chamada do Model Gateway
// (AOS-057, tecnica/06 §4, ADR-011/ADR-010): principal (utilizador, agente),
// modelo e região — SEMPRE, seja qual for a chave de infra usada, NUNCA "o pool".
//
// # Os dois eixos, ligados aqui
//
// O eixo da IDENTIDADE (principal, cadeia on-behalf-of até um humano) vem do
// estágio de authn; o eixo da CHAVE DE INFRA (KeyID pooled por throughput) vem do
// keypool, DESACOPLADO da identidade. Este pacote junta-os num [Record] e:
//
//   - ANOTA o span OTel GenAI com principal/modelo/região + o KeyID NÃO-SECRETO
//     (nunca o segredo — ADR-006);
//   - SELA o registo no audit WORM hash-chain tamper-evident (AOS-011), ligando a
//     model call à cadeia de responsabilização (ADR-010).
//
// A resposta à pergunta "quem autorizou esta chamada ao modelo?" é sempre o
// principal — a prova de que o desacoplamento identidade/chave não destrói a
// atribuição.
//
// # Segredos (ADR-006)
//
// O [Record] transporta APENAS o KeyID NÃO-SECRETO da chave (ex.: "acct-eu-1").
// O segredo da credencial NUNCA entra aqui, nem no span, nem no WORM, nem em logs.
package attribution

import (
	"context"
	"errors"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
)

// ErrNoPrincipal — o registo de atribuição não tem principal resolvido
// (UserID/AgentID vazios). Fail-closed (ADR-010/ADR-011): a invariante central de
// AOS-057 é que toda a model call é atribuída a um PRINCIPAL, nunca "o pool" nem
// "ninguém". Uma config que ligue a atribuição sem um estágio de authn real (que
// popula o principal) NUNCA pode selar uma chamada anónima — esta guarda é a
// defesa-em-profundidade que o impede, mesmo que o chamador se esqueça.
var ErrNoPrincipal = errors.New("attribution: registo sem principal resolvido (user/agent vazios; fail-closed)")

// Atributos de span (semconv-aligned) do registo de atribuição. O principal e o
// KeyID NÃO-SECRETO tornam a model call imputável e correlacionável SEM revelar
// segredos.
const (
	// AttrPrincipalUser — utilizador responsável (raiz humana da autoridade).
	AttrPrincipalUser = "aos.principal.user_id"
	// AttrPrincipalAgent — agente (NHI) que actuou.
	AttrPrincipalAgent = "aos.principal.agent_id"
	// AttrPrincipalClass — classe do agente.
	AttrPrincipalClass = "aos.principal.agent_class"
	// AttrPrincipalHuman — humano responsável na raiz da cadeia on-behalf-of.
	AttrPrincipalHuman = "aos.principal.human_root"
	// AttrRegion — região efectiva da chamada (soberania).
	AttrRegion = "aos.region"
	// AttrKeyID — identificador NÃO-SECRETO da chave de infra pooled usada. Prova
	// que a atribuição regista uma CHAVE concreta (nunca "o pool") — nunca o segredo.
	AttrKeyID = "aos.infra.key_id"
)

// capabilityModelInvoke é a capability selada no registo WORM: a model call é o
// exercício do direito de invocar um modelo.
const capabilityModelInvoke = "model:invoke"

// Hop é um elo (sub → act_as) da cadeia on-behalf-of, em forma primitiva.
type Hop struct {
	Sub   string
	ActAs string
}

// Record é o registo de atribuição de UMA model call. Junta o eixo da identidade
// (principal/cadeia) ao eixo da chave (KeyID não-secreto) + modelo + região.
type Record struct {
	// Principal (utilizador, agente) — SEMPRE presente numa chamada atribuída.
	UserID     string
	AgentID    string
	AgentClass string
	// HumanRoot é o humano responsável na raiz da cadeia (ADR-003).
	HumanRoot string
	// DelegationChain é a cadeia on-behalf-of verificada (raiz humana → agente).
	DelegationChain []Hop
	// Model e Region identificam o modelo efectivo e a região da chamada.
	Model  string
	Region string
	// KeyID é a chave de infra NÃO-SECRETA usada (pooled por throughput). Nunca o
	// segredo; nunca "o pool".
	KeyID string
	// Operation é a operação GenAI ("chat"|"embeddings").
	Operation string
	// PolicyVersion é a versão da policy-as-code de validação de token em vigor.
	PolicyVersion string
	// RunID/StepID correlacionam com a trajectória do agente (opcional).
	RunID  string
	StepID string
	// Timestamp é observacional (a ordem no WORM é o AuditSeq).
	Timestamp time.Time
}

// PrincipalID devolve o identificador NÃO-SECRETO e estável do principal
// (utilizador, agente) — a resposta a "quem autorizou". Nunca "o pool".
func (r Record) PrincipalID() string {
	return "user:" + r.UserID + ";agent:" + r.AgentID
}

// Sink recebe, opcionalmente, cada [Record] emitido (introspecção/observabilidade
// em memória; os testes de atribuição cruzada inspeccionam-no). A selagem WORM é
// separada (ver [Recorder]).
type Sink interface {
	Emit(ctx context.Context, rec Record)
}

// SinkFunc adapta uma função a [Sink].
type SinkFunc func(ctx context.Context, rec Record)

// Emit implementa [Sink].
func (f SinkFunc) Emit(ctx context.Context, rec Record) { f(ctx, rec) }

// Recorder produz o registo de atribuição por chamada: anota o span e sela no
// audit WORM. Construir com [NewRecorder].
type Recorder struct {
	store       audit.Store
	sink        Sink
	partitionFn func(Record) string
}

// Option configura o [Recorder].
type Option func(*Recorder)

// WithSink liga um [Sink] em memória que recebe cada registo (além da selagem
// WORM). Útil para observabilidade e para os testes de atribuição cruzada.
func WithSink(s Sink) Option { return func(r *Recorder) { r.sink = s } }

// WithPartition define a partição da hash-chain de audit para um registo. Default:
// "modelgw:<board-ou-humano>" — aqui, a raiz humana (agrupa a responsabilização
// por humano). Uma partição estável mantém a cadeia tamper-evident coesa.
func WithPartition(fn func(Record) string) Option {
	return func(r *Recorder) {
		if fn != nil {
			r.partitionFn = fn
		}
	}
}

// NewRecorder constrói o recorder sobre um audit [audit.Store] (WORM). Sem store,
// a selagem é no-op (só a anotação de span corre) — produção liga um store real.
func NewRecorder(store audit.Store, opts ...Option) *Recorder {
	r := &Recorder{
		store:       store,
		partitionFn: defaultPartition,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// defaultPartition agrupa a cadeia de audit pelo humano responsável (raiz da
// atribuição). Cai para o agente se não houver humano.
func defaultPartition(rec Record) string {
	root := rec.HumanRoot
	if root == "" {
		root = rec.AgentID
	}
	return "modelgw:" + root
}

// Annotate anota o span com o registo de atribuição (principal/modelo/região +
// KeyID não-secreto). NUNCA emite segredos. É seguro com span nil.
func (r *Recorder) Annotate(span agentruntime.Span, rec Record) {
	if span == nil {
		return
	}
	span.SetAttribute(AttrPrincipalUser, rec.UserID)
	span.SetAttribute(AttrPrincipalAgent, rec.AgentID)
	span.SetAttribute(AttrPrincipalClass, rec.AgentClass)
	span.SetAttribute(AttrPrincipalHuman, rec.HumanRoot)
	span.SetAttribute(agentruntime.AttrRequestModel, rec.Model)
	span.SetAttribute(AttrRegion, rec.Region)
	span.SetAttribute(AttrKeyID, rec.KeyID)
}

// Seal sela o registo de atribuição no audit WORM hash-chain (ADR-010/AOS-011) e
// devolve o registo selado. Sem store configurado é no-op (devolve zero-value,
// nil). O registo sela o PRINCIPAL e a cadeia on-behalf-of, o modelo e a região —
// nunca o segredo nem "o pool".
func (r *Recorder) Seal(ctx context.Context, rec Record) (audit.AuditRecord, error) {
	if r.store == nil {
		return audit.AuditRecord{}, nil
	}
	// Defesa-em-profundidade: nunca selar um registo sem principal (user/agent)
	// resolvido — selar "ninguém" no WORM violaria a invariante de atribuição.
	if rec.UserID == "" || rec.AgentID == "" {
		return audit.AuditRecord{}, ErrNoPrincipal
	}
	ar := audit.AuditRecord{
		Partition:     r.partitionFn(rec),
		Timestamp:     rec.Timestamp,
		Decision:      audit.DecisionAllow,
		Principal:     audit.Principal{NHIID: rec.AgentID, DelegationChain: toAuditHops(rec.DelegationChain)},
		Capability:    capabilityModelInvoke,
		PolicyVersion: rec.PolicyVersion,
		RunID:         rec.RunID,
		StepID:        rec.StepID,
		ToolID:        "model." + rec.Model,
		Resource:      audit.Resource{Type: "model", Value: rec.Model, Region: rec.Region},
	}
	return r.store.Append(ctx, ar)
}

// Record ANOTA o span e SELA no WORM, e emite para o [Sink] se configurado. É o
// ponto único que o Gateway chama por chamada. Devolve erro se a selagem falhar —
// uma model call não-auditável é fail-closed a montante (uma chamada sem rasto de
// atribuição é inaceitável, ADR-010).
func (r *Recorder) Record(ctx context.Context, span agentruntime.Span, rec Record) error {
	// Fail-closed ANTES de qualquer efeito observável (anotação de span, emissão para
	// o sink ou selagem): um registo sem principal resolvido é recusado. Assim uma
	// chamada "atribuída a ninguém" nunca chega a anotar o span nem a alimentar o WORM.
	if rec.UserID == "" || rec.AgentID == "" {
		return ErrNoPrincipal
	}
	r.Annotate(span, rec)
	if r.sink != nil {
		r.sink.Emit(ctx, rec)
	}
	if _, err := r.Seal(ctx, rec); err != nil {
		return err
	}
	return nil
}

// toAuditHops projecta os hops de atribuição para o modelo do audit.
func toAuditHops(hops []Hop) []audit.DelegationHop {
	if len(hops) == 0 {
		return nil
	}
	out := make([]audit.DelegationHop, len(hops))
	for i, h := range hops {
		out[i] = audit.DelegationHop{Sub: h.Sub, ActAs: h.ActAs}
	}
	return out
}
