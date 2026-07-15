package allowlist

import (
	"context"
	"errors"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
)

// O REGISTO DE GOVERNAÇÃO por chamada da allowlist regional (AOS-058, ADR-010/
// ADR-011): a decisão de allowlist (allow/deny) e a rota (modelo, região) são
// registadas POR CHAMADA, atribuíveis ao PRINCIPAL E ao BOARD — nunca um deny
// anónimo. É o mesmo eixo de atribuição de AOS-057 (metering/attribution), aqui
// aplicado à decisão de soberania: um deny cross-border ou fora-de-allowlist sela
// no audit WORM tamper-evident quem o motivou (principal) e sob que fronteira
// (board), respondendo a "quem foi recusado e porquê" sem revelar segredos.

// Erros fail-closed da selagem de governação.
var (
	// ErrNoBoard — a decisão não tem board resolvido. Fail-closed: um registo de
	// soberania sem board não é atribuível à fronteira e é recusado (a soberania é
	// por board — selar "sem fronteira" violaria a invariante de AOS-058).
	ErrNoBoard = errors.New("allowlist: decisao de governacao sem board (fail-closed)")
	// ErrNoAttribution — a decisão não tem principal (nem utilizador nem agente).
	// Fail-closed: um deny/allow anónimo é inaceitável (ADR-010).
	ErrNoAttribution = errors.New("allowlist: decisao de governacao sem principal (fail-closed)")
)

// Atributos de span (semconv-aligned) do registo de governação da allowlist.
const (
	// AttrAllowlistResult — resultado da decisão de allowlist ("allow"|"deny").
	AttrAllowlistResult = "aos.allowlist.result"
	// AttrAllowlistReason — razão estável da decisão (motivo do deny/allow).
	AttrAllowlistReason = "aos.allowlist.reason"
	// AttrAllowlistPolicyVersion — versão tamper-evident da allowlist em vigor.
	AttrAllowlistPolicyVersion = "aos.allowlist.policy_version"
	// AttrBoard — board (fronteira de soberania) da decisão.
	AttrBoard = "aos.board"
	// AttrRegion — região efectiva/pedida da decisão.
	AttrRegion = "aos.region"
	// AttrModel — modelo alvo da decisão.
	AttrModel = "aos.model"
)

// capabilityModelInvoke é a capability escopada exercida/negada: invocar um modelo.
const capabilityModelInvoke = "model:invoke"

// obligationGovernance é o tipo da obligation que sela board/razão/utilizador na
// cadeia (tamper-evident), tornando a decisão atribuível a partir de um único
// registo (não só pela partição).
const obligationGovernance = "governance"

// Hop é um elo (sub → act_as) da cadeia on-behalf-of, em forma primitiva.
type Hop struct {
	Sub   string
	ActAs string
}

// GovRecord é a decisão de governação da allowlist de UMA chamada: o board, o
// principal (utilizador, agente) e a cadeia, o modelo e a região, o veredicto e a
// razão. Junta o eixo da soberania (board) ao eixo da identidade (principal).
type GovRecord struct {
	// Board é a fronteira de soberania da decisão (SEMPRE presente).
	Board string
	// Principal (utilizador, agente) responsável.
	PrincipalUser  string
	PrincipalAgent string
	AgentClass     string
	// HumanRoot é o humano responsável na raiz da cadeia on-behalf-of (ADR-003).
	HumanRoot string
	// DelegationChain é a cadeia on-behalf-of verificada.
	DelegationChain []Hop
	// Model e Region identificam a rota da decisão.
	Model  string
	Region string
	// Decision é o veredicto (allow|deny).
	Decision audit.Decision
	// Reason é a razão estável da decisão (ex.: "modelo fora da allowlist").
	Reason string
	// PolicyVersion é a versão tamper-evident da allowlist em vigor.
	PolicyVersion string
	// Operation é a operação GenAI ("chat"|"embeddings").
	Operation string
	// RunID/StepID correlacionam com a trajectória (opcional).
	RunID  string
	StepID string
	// Timestamp é observacional (a ordem no WORM é o AuditSeq).
	Timestamp time.Time
}

// principalID devolve o identificador NÃO-SECRETO do principal (a resposta a
// "quem"). Prefere o agente (NHI); cai para o utilizador.
func (r GovRecord) principalID() string {
	if r.PrincipalAgent != "" {
		return r.PrincipalAgent
	}
	return r.PrincipalUser
}

// Sink recebe, opcionalmente, cada [GovRecord] emitido (introspecção em memória; os
// testes de governação inspeccionam-no). A selagem WORM é separada.
type Sink interface {
	Emit(ctx context.Context, rec GovRecord)
}

// SinkFunc adapta uma função a [Sink].
type SinkFunc func(ctx context.Context, rec GovRecord)

// Emit implementa [Sink].
func (f SinkFunc) Emit(ctx context.Context, rec GovRecord) { f(ctx, rec) }

// Recorder produz o registo de governação por chamada: anota o span e sela no
// audit WORM (partição POR BOARD — a cadeia tamper-evident da soberania). Construir
// com [NewRecorder].
type Recorder struct {
	store audit.Store
	sink  Sink
}

// Option configura o [Recorder].
type Option func(*Recorder)

// WithSink liga um [Sink] em memória que recebe cada registo (além da selagem WORM).
func WithSink(s Sink) Option { return func(r *Recorder) { r.sink = s } }

// NewRecorder constrói o recorder sobre um audit [audit.Store] (WORM). Sem store, a
// selagem é no-op (só a anotação de span/sink corre) — produção liga um store real.
func NewRecorder(store audit.Store, opts ...Option) *Recorder {
	r := &Recorder{store: store}
	for _, o := range opts {
		o(r)
	}
	return r
}

// partitionOf agrupa a cadeia de audit POR BOARD (fronteira de soberania): a
// responsabilização de soberania é contígua por board.
func partitionOf(board string) string { return "modelgw-gov:" + board }

// Annotate anota o span com a decisão de governação (resultado, board, modelo,
// região, versão da política). NUNCA emite segredos. Seguro com span nil.
func (r *Recorder) Annotate(span agentruntime.Span, rec GovRecord) {
	if span == nil {
		return
	}
	span.SetAttribute(AttrAllowlistResult, string(rec.Decision))
	span.SetAttribute(AttrAllowlistReason, rec.Reason)
	span.SetAttribute(AttrAllowlistPolicyVersion, rec.PolicyVersion)
	span.SetAttribute(AttrBoard, rec.Board)
	span.SetAttribute(AttrModel, rec.Model)
	span.SetAttribute(AttrRegion, rec.Region)
}

// Seal sela a decisão no audit WORM hash-chain (ADR-010/AOS-011) e devolve o
// registo selado. Sem store é no-op. Fail-closed: um registo sem board ou sem
// principal é RECUSADO (a decisão de soberania tem de ser atribuível a ambos).
func (r *Recorder) Seal(ctx context.Context, rec GovRecord) (audit.AuditRecord, error) {
	if err := rec.validate(); err != nil {
		return audit.AuditRecord{}, err
	}
	if r.store == nil {
		return audit.AuditRecord{}, nil
	}
	ar := audit.AuditRecord{
		Partition:     partitionOf(rec.Board),
		Timestamp:     rec.Timestamp,
		Decision:      rec.Decision,
		Principal:     audit.Principal{NHIID: rec.principalID(), DelegationChain: toAuditHops(rec.DelegationChain)},
		Capability:    capabilityModelInvoke,
		PolicyVersion: rec.PolicyVersion,
		RunID:         rec.RunID,
		StepID:        rec.StepID,
		ToolID:        "model." + rec.Model,
		Resource:      audit.Resource{Type: "model", Value: rec.Model, Region: rec.Region},
		// A obligation sela BOARD + razão + utilizador na cadeia (tamper-evident):
		// a decisão é atribuível a partir de UM registo, não só pela partição.
		Obligations: []audit.Obligation{{
			Type: obligationGovernance,
			Params: map[string]string{
				"board":          rec.Board,
				"reason":         rec.Reason,
				"principal_user": rec.PrincipalUser,
			},
		}},
	}
	return r.store.Append(ctx, ar)
}

// changelogPartition é a cadeia dedicada ao CHANGELOG da allowlist (ADR-011): cada
// activação de uma versão da policy sela aqui, tornando a evolução da allowlist
// tamper-evident e auditável independentemente das decisões por chamada.
const changelogPartition = "modelgw-gov:allowlist-changelog"

// SealChangelog sela no audit WORM a ACTIVAÇÃO de uma versão da allowlist
// (policy-as-code versionada + assinada) — o CHANGELOG exigido pelo ADR-011. Regista
// a versão tamper-evident (`Policy.Version()`) numa cadeia dedicada. Chamado no
// arranque/rotação da policy. Sem store é no-op. A versão vazia é recusada.
func (r *Recorder) SealChangelog(ctx context.Context, policyVersion string, at time.Time) (audit.AuditRecord, error) {
	if policyVersion == "" {
		return audit.AuditRecord{}, ErrPolicyMalformed
	}
	if r.store == nil {
		return audit.AuditRecord{}, nil
	}
	ar := audit.AuditRecord{
		Partition:     changelogPartition,
		Timestamp:     at,
		Decision:      audit.DecisionAllow,
		Principal:     audit.Principal{NHIID: "allowlist-policy"},
		Capability:    "policy:activate",
		PolicyVersion: policyVersion,
		ToolID:        "allowlist.regional",
		Resource:      audit.Resource{Type: "policy", Value: "allowlist-regional"},
		Obligations: []audit.Obligation{{
			Type:   obligationGovernance,
			Params: map[string]string{"changelog": "activate", "policy_version": policyVersion},
		}},
	}
	return r.store.Append(ctx, ar)
}

// Record ANOTA o span, emite para o [Sink] (se configurado) e SELA no WORM. É o
// ponto único que o estágio de allowlist chama por chamada. Fail-closed ANTES de
// qualquer efeito observável: uma decisão sem board/principal nunca chega a anotar
// nem a selar.
func (r *Recorder) Record(ctx context.Context, span agentruntime.Span, rec GovRecord) error {
	if err := rec.validate(); err != nil {
		return err
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

// validate impõe as invariantes de atribuição (fail-closed): board presente e
// pelo menos um dos identificadores do principal.
func (r GovRecord) validate() error {
	if r.Board == "" {
		return ErrNoBoard
	}
	if r.PrincipalUser == "" && r.PrincipalAgent == "" {
		return ErrNoAttribution
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
