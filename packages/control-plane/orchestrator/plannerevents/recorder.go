package plannerevents

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aos-ref/substrate/eventstore"
)

// DefaultPlannerNHI é a identidade não-humana (NHI) por omissão do emissor destes
// eventos. Em produção resolve-se da identidade real do serviço (AOS-005). NÃO é
// PII — é um rótulo de serviço.
const DefaultPlannerNHI = "nhi:control-plane/orchestrator/planner"

// step ids estáveis por evento (idempotency_key = plan_id:step_id no Event
// Store). Deliberadamente com prefixo `planstep:` e separador `:` — NÃO com a
// forma dotted `familia.segmento` — para não colidir com nenhum tipo do catálogo
// nem ser apanhado como literal de emissão pelo gate `event-catalog`. Os eventos
// repetíveis (validation_failed por tentativa; capability_gap por nó) compõem o
// step id com um discriminador, para coexistirem no mesmo stream sem dedupe.
const (
	stepIntakeClassified = "planstep:intake_classified"
	stepPlannerAdmitted  = "planstep:planner_admitted"
	stepProposed         = "planstep:proposed"
	stepValidationFailed = "planstep:validation_failed"
	stepValidated        = "planstep:validated"
	stepDecision         = "planstep:decision"
	stepMaterialized     = "planstep:materialized"
	stepBranchDecided    = "planstep:branch_decided"
	stepCapabilityGap    = "planstep:capability_gap"
	stepReplan           = "planstep:replan"
)

// Appender é a superfície MÍNIMA do Event Store de que o [Recorder] depende:
// apenas Append. *eventstore.Store satisfá-la. Torna o caminho de escrita
// explícito e testável sem puxar o resto do store.
type Appender interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
}

// Proposer é o passo de decomposição APOIADO EM LLM. É o ÚNICO componente deste
// domínio que consulta um modelo, e vive EXCLUSIVAMENTE no caminho de ESCRITA
// ([Recorder.RecordProposedFrom]). O plano proposto é DADOS UNTRUSTED (ADR-005):
// o Proposer devolve só o hash e a proveniência (planner_meta), nunca o conteúdo
// cru. A reconstrução por replay ([Reconstruct]) NÃO tem referência ao Proposer —
// é essa a fronteira que garante que nenhum evento re-chama o LLM (§3.4/§6.1).
type Proposer interface {
	Propose(ctx context.Context) (PlanHash string, Meta PlannerMeta, err error)
}

// Recorder emite os eventos do domínio `aos.planner.v1` para o Event Store, num
// stream por plan_id. Cada método apensa exactamente um facto imutável. Construir
// com [NewRecorder].
type Recorder struct {
	store    Appender
	producer eventstore.Producer
}

// RecorderOption configura o [Recorder].
type RecorderOption func(*Recorder)

// WithProducer injecta a identidade emissora (NHI) dos eventos.
func WithProducer(p eventstore.Producer) RecorderOption {
	return func(r *Recorder) { r.producer = p }
}

// NewRecorder constrói um Recorder sobre store. store é obrigatório.
func NewRecorder(store Appender, opts ...RecorderOption) (*Recorder, error) {
	if store == nil {
		return nil, fmt.Errorf("plannerevents: Appender nil")
	}
	r := &Recorder{
		store:    store,
		producer: eventstore.Producer{NHIID: DefaultPlannerNHI},
	}
	for _, o := range opts {
		o(r)
	}
	return r, nil
}

// emit apensa um evento ao stream do plano. evType é SEMPRE uma constante do
// catálogo (os chamadores typed passam-na); este helper recebe-a como parâmetro,
// pelo que o gate `event-catalog` não a resolve aqui — a disciplina «sem literais»
// é cumprida pelos chamadores. stepID compõe a idempotency_key plan_id:step_id.
func (r *Recorder) emit(ctx context.Context, planID, evType, stepID string, payload any) (uint64, error) {
	if planID == "" {
		return 0, fmt.Errorf("plannerevents: plan_id vazio")
	}
	raw, err := marshal(payload)
	if err != nil {
		return 0, err
	}
	res, err := r.store.Append(ctx, planID, eventstore.EventInput{
		Type:          evType,
		Payload:       raw,
		SchemaVersion: DomainVersion,
		RunID:         planID,
		StepID:        stepID,
		Producer:      r.producer,
	})
	if err != nil {
		return 0, err
	}
	return res.Seq, nil
}

// RecordIntakeClassified apensa `plan.intake_classified`. Fail-closed: recusa uma
// classificação desconhecida.
func (r *Recorder) RecordIntakeClassified(ctx context.Context, p IntakeClassifiedPayload) (uint64, error) {
	if !p.Classification.valid() {
		return 0, fmt.Errorf("plannerevents: classificação inválida %q", p.Classification)
	}
	return r.emit(ctx, p.PlanID, EventIntakeClassified, stepIntakeClassified, p)
}

// RecordPlannerAdmitted apensa `plan.planner_admitted`.
func (r *Recorder) RecordPlannerAdmitted(ctx context.Context, p PlannerAdmittedPayload) (uint64, error) {
	return r.emit(ctx, p.PlanID, EventPlannerAdmitted, stepPlannerAdmitted, p)
}

// RecordProposedFrom consulta o Proposer (o LLM) UMA vez, monta o payload a partir
// do hash e da proveniência devolvidos, e apensa `plan.proposed`. É o único ponto
// deste pacote que toca num modelo — e está no caminho de ESCRITA. O replay não o
// re-executa: reconstrói `plan.proposed` a partir do log (ADR-010).
func (r *Recorder) RecordProposedFrom(ctx context.Context, planID string, attempt int, proposer Proposer) (uint64, error) {
	if proposer == nil {
		return 0, fmt.Errorf("plannerevents: Proposer nil")
	}
	hash, meta, err := proposer.Propose(ctx)
	if err != nil {
		return 0, err
	}
	return r.RecordProposed(ctx, ProposedPayload{PlanID: planID, PlanHash: hash, Meta: meta, Attempt: attempt})
}

// RecordProposed apensa `plan.proposed` a partir de um payload já montado (sem
// consultar o modelo — útil quando a proposta já foi obtida noutro passo).
func (r *Recorder) RecordProposed(ctx context.Context, p ProposedPayload) (uint64, error) {
	return r.emit(ctx, p.PlanID, EventProposed, stepProposed, p)
}

// RecordValidationFailed apensa `plan.validation_failed` a partir de um
// [ValidationOutcome], DESCARTANDO o RawDetail (§6.1, CA(c)). Fail-closed: uma
// regra desconhecida não emite evento ([ErrUnknownRule]). O step id inclui a
// tentativa para que várias falhas coexistam no stream.
func (r *Recorder) RecordValidationFailed(ctx context.Context, o ValidationOutcome) (uint64, error) {
	payload, err := NewValidationFailed(o)
	if err != nil {
		return 0, err
	}
	stepID := stepValidationFailed + ":" + strconv.Itoa(o.Attempt)
	return r.emit(ctx, payload.PlanID, EventValidationFailed, stepID, payload)
}

// RecordValidated apensa `plan.validated`.
func (r *Recorder) RecordValidated(ctx context.Context, p ValidatedPayload) (uint64, error) {
	return r.emit(ctx, p.PlanID, EventValidated, stepValidated, p)
}

// RecordDecision apensa `plan.approved`/`rejected`/`edited` conforme a decisão do
// gate. Fail-closed: uma decisão desconhecida é recusada.
func (r *Recorder) RecordDecision(ctx context.Context, p DecisionPayload) (uint64, error) {
	if !p.Decision.valid() {
		return 0, fmt.Errorf("plannerevents: decisão inválida %q", p.Decision)
	}
	evType, ok := eventForDecision(p.Decision)
	if !ok {
		return 0, fmt.Errorf("plannerevents: decisão inválida %q", p.Decision)
	}
	// Um step id por decisão distinta: aprovações/edições/rejeições sucessivas do
	// mesmo plano (edição → revalidação → nova decisão) coexistem no stream.
	stepID := stepDecision + ":" + string(p.Decision)
	return r.emit(ctx, p.PlanID, evType, stepID, p)
}

// RecordMaterialized apensa `plan.materialized`. Fail-closed: recusa um nó com
// spawn kind desconhecido.
func (r *Recorder) RecordMaterialized(ctx context.Context, p MaterializedPayload) (uint64, error) {
	for _, n := range p.Nodes {
		if !n.Kind.valid() {
			return 0, fmt.Errorf("plannerevents: spawn kind inválido %q (node %q)", n.Kind, n.NodeID)
		}
	}
	return r.emit(ctx, p.PlanID, EventMaterialized, stepMaterialized, p)
}

// RecordBranchDecided apensa `plan.branch_decided` (ADR-022 §2.1, AOS-270): a
// decisão de ramo de UM nó, avaliada pelo despachante sobre o resultado registado.
//
// O step id é `planstep:branch_decided:<node_id>` — UM por nó, sem discriminador de
// tentativa, de propósito: a idempotency_key `plan_id:step_id` do Event Store torna
// a decisão de ramo de um nó um facto ÚNICO e imutável no stream. Uma segunda
// passagem do despachante sobre o mesmo nó não pode produzir um segundo facto
// (nem, portanto, um ramo diferente) — é a metade durável da garantia «o replay
// reproduz o ramo SEM re-avaliação»; a outra metade é o despachante ler a decisão
// registada ANTES de avaliar seja o que for.
//
// Fail-closed: recusa node_id vazio ou digest ausente — sem o carimbo da expressão
// não há como distinguir, no replay, um ramo do mesmo nó num documento editado.
func (r *Recorder) RecordBranchDecided(ctx context.Context, p BranchDecidedPayload) (uint64, error) {
	if p.NodeID == "" {
		return 0, fmt.Errorf("plannerevents: node_id vazio na decisão de ramo")
	}
	if p.ConditionDigest == "" {
		return 0, fmt.Errorf("plannerevents: condition_digest vazio na decisão de ramo (nó %q)", p.NodeID)
	}
	stepID := stepBranchDecided + ":" + p.NodeID
	return r.emit(ctx, p.PlanID, EventBranchDecided, stepID, p)
}

// RecordCapabilityGap apensa `plan.capability_gap_opened`/`resolved`. Fail-closed:
// recusa um estado desconhecido. O step id inclui node_id + estado.
func (r *Recorder) RecordCapabilityGap(ctx context.Context, p CapabilityGapPayload) (uint64, error) {
	if !p.State.valid() {
		return 0, fmt.Errorf("plannerevents: estado de gap inválido %q", p.State)
	}
	evType, ok := eventForGap(p.State)
	if !ok {
		return 0, fmt.Errorf("plannerevents: estado de gap inválido %q", p.State)
	}
	stepID := stepCapabilityGap + ":" + p.NodeID + ":" + string(p.State)
	return r.emit(ctx, p.PlanID, evType, stepID, p)
}

// RecordReplan apensa `plan.replan_requested`/`applied`. Fail-closed: recusa uma
// fase desconhecida. O step id inclui a fase e o novo hash para que replans
// aninhados coexistam no stream.
func (r *Recorder) RecordReplan(ctx context.Context, p ReplanPayload) (uint64, error) {
	if !p.Phase.valid() {
		return 0, fmt.Errorf("plannerevents: fase de replan inválida %q", p.Phase)
	}
	evType, ok := eventForReplan(p.Phase)
	if !ok {
		return 0, fmt.Errorf("plannerevents: fase de replan inválida %q", p.Phase)
	}
	stepID := stepReplan + ":" + string(p.Phase) + ":" + p.NewPlanHash
	return r.emit(ctx, p.PlanID, evType, stepID, p)
}
