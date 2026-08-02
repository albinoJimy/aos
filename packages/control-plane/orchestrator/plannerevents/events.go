// Package plannerevents define o domínio de eventos append-only `aos.planner.v1`
// (tecnica/18 §6.1) e a sua reconstrução por replay (ADR-010), assente no Event
// Store (AOS-013) e no motor de replay read-only (AOS-016) EXISTENTES.
//
// AOS-235. O ciclo de vida de um meta-run — intake → planeamento → validação →
// gate → materialização, com desvios de capability-gap e re-planeamento — é
// gravado como uma sequência de factos imutáveis num único stream (o plan_id). O
// documento APROVADO no log é o input do restante run; por isso a reconstrução é
// read-only e byte-a-byte, SEM re-chamar o modelo (§3.4/§6.1).
//
// Três invariantes deste pacote:
//
//  1. Constantes junto do emissor (disciplina do event-catalog, tecnica/13 §3.3):
//     os tipos são CONSTANTES declaradas aqui, nunca literais no caminho de
//     emissão. A família `plan.*` é nova — carece de entrada na taxonomia de
//     tecnica/13 §3.3; ver o report de AOS-235.
//
//  2. Ordem idêntica na reconstrução (ADR-010): [Reconstruct] devolve os factos
//     na MESMA ordem em que foram apensos (a ordem total por-stream do Event
//     Store), preservando os bytes do payload. Nada de re-ordenar por tipo nem de
//     projecção por mapa.
//
//  3. Sem eco de conteúdo sensível/PII em `plan.validation_failed` (§6.1, CA(c)):
//     o payload do evento carrega SÓ metadados classificados (regra violada,
//     código de diagnóstico limitado, tentativa n/N, hash). O detalhe cru do
//     validador — que PODE referenciar o conteúdo untrusted do PlanDocument
//     (ADR-005) — NUNCA entra no evento; é descartado por [NewValidationFailed].
package plannerevents

import (
	"encoding/json"
	"errors"
	"fmt"
)

// DomainVersion é a versão do domínio de eventos deste pacote. É o `plan_version`
// de schema (tecnica/18 §3.6): as capturas ficam carimbadas e a reconstrução
// rejeita fail-closed um envelope de versão desconhecida (ver [Reconstruct]).
const DomainVersion = "aos.planner.v1"

// Tipos de evento append-only do domínio `aos.planner.v1` (tecnica/18 §6.1).
//
// São a FONTE DE VERDADE do catálogo desta família — declarados como constantes
// junto do emissor ([Recorder]), nunca como literais no caminho de emissão
// (tecnica/13 §3.3; o gate `event-catalog` verifica-o). A ordem de declaração
// espelha a ordem canónica do ciclo de vida (§3.2), que NÃO é alfabética — de
// propósito: uma reconstrução que ordenasse por nome de tipo produziria uma
// sequência diferente e os testes de ordem apanham-na.
const (
	// EventIntakeClassified — o intake classificou o Goal (meta-nível vs. tarefa
	// simples) por heurística declarativa, não por LLM (§3.5).
	EventIntakeClassified = "plan.intake_classified"
	// EventPlannerAdmitted — reserva de planeamento admitida para `agent:planner`.
	EventPlannerAdmitted = "plan.planner_admitted"
	// EventProposed — o PLN propôs um PlanDocument (dados untrusted, ADR-005): só
	// o hash e o planner_meta entram no log, nunca o conteúdo cru.
	EventProposed = "plan.proposed"
	// EventValidationFailed — a validação estrutural fail-closed rejeitou a
	// proposta. SEM eco de conteúdo sensível (§6.1, CA(c)).
	EventValidationFailed = "plan.validation_failed"
	// EventValidated — a proposta passou a validação pura e determinística.
	EventValidated = "plan.validated"
	// EventApproved — o gate (AOS-121) aprovou o plano com decisão assinada.
	EventApproved = "plan.approved"
	// EventRejected — o gate rejeitou o plano.
	EventRejected = "plan.rejected"
	// EventEdited — o cidadão editou o organigrama; segue nova validação.
	EventEdited = "plan.edited"
	// EventMaterialized — o plano APROVADO virou eventos do DAG / spawns delegados.
	EventMaterialized = "plan.materialized"
	// EventCapabilityGapOpened — um nó abriu um gap de capacidade (skill em falta).
	EventCapabilityGapOpened = "plan.capability_gap_opened"
	// EventCapabilityGapResolved — o gap foi ratificado e resolvido (AOS-096).
	EventCapabilityGapResolved = "plan.capability_gap_resolved"
	// EventReplanRequested — pedido de re-planeamento de um subgrafo (§4.2).
	EventReplanRequested = "plan.replan_requested"
	// EventReplanApplied — o re-plano foi aplicado ao subgrafo com novo hash.
	EventReplanApplied = "plan.replan_applied"
)

// canonicalLifecycle é a ordem canónica dos tipos do ciclo de vida (§3.2), usada
// só para a auto-verificação de não-vacuidade dos testes e como documentação
// executável. NÃO é imposta à reconstrução — a ordem que [Reconstruct] devolve é
// a ordem REAL de append do stream, não esta.
var canonicalLifecycle = []string{
	EventIntakeClassified,
	EventPlannerAdmitted,
	EventProposed,
	EventValidationFailed,
	EventValidated,
	EventApproved,
	EventRejected,
	EventEdited,
	EventMaterialized,
	EventCapabilityGapOpened,
	EventCapabilityGapResolved,
	EventReplanRequested,
	EventReplanApplied,
}

// knownType indica se t é um tipo do domínio (fail-closed: a reconstrução recusa
// um envelope com um tipo `plan.*` desconhecido em vez de o aceitar em silêncio).
func knownType(t string) bool {
	for _, k := range canonicalLifecycle {
		if k == t {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Payloads — todos SEM PII: só ids opacos, hashes, versões e enums classificados.
// ---------------------------------------------------------------------------

// Classification é o resultado do intake: rota do run (§3.5). Fail-safe na
// direcção da supervisão — em ambiguidade classifica-se `meta`, nunca `simple`.
type Classification string

const (
	// ClassificationMeta — rota de planeamento (PlanDocument + gate).
	ClassificationMeta Classification = "meta"
	// ClassificationSimple — rota directa (tarefa de 1 nó, sem planeamento).
	ClassificationSimple Classification = "simple"
)

func (c Classification) valid() bool {
	return c == ClassificationMeta || c == ClassificationSimple
}

// IntakeClassifiedPayload — corpo de `plan.intake_classified`. `heuristic` é o id
// da heurística declarativa aplicada (§3.5), não texto livre do Goal.
type IntakeClassifiedPayload struct {
	PlanID         string         `json:"plan_id"`
	GoalID         string         `json:"goal_id"`
	Classification Classification `json:"classification"`
	Heuristic      string         `json:"heuristic"`
}

// PlannerAdmittedPayload — corpo de `plan.planner_admitted`: a reserva de
// planeamento admitida para o `agent:planner` (contexto, tabela de preços, factor
// de retry). Sem conteúdo do pedido — só metadados da reserva.
type PlannerAdmittedPayload struct {
	PlanID              string `json:"plan_id"`
	PlannerNHI          string `json:"planner_nhi"`
	PricingTableVersion string `json:"pricing_table_version"`
	RetryFactor         int    `json:"retry_factor"`
	MaxAttempts         int    `json:"max_attempts"`
}

// PlannerMeta pina a proveniência da proposta para reprodutibilidade (§3.6): os
// três carimbos (modelo, versão do prompt, hash das capabilities). São
// identificadores/hashes — nunca o prompt nem a resposta crua.
type PlannerMeta struct {
	Model            string `json:"model"`
	PromptVersion    string `json:"prompt_version"`
	CapabilitiesHash string `json:"capabilities_hash"`
}

// ProposedPayload — corpo de `plan.proposed`. Só o HASH do PlanDocument (dados
// untrusted, ADR-005) e o planner_meta; o documento cru vive fora do log.
type ProposedPayload struct {
	PlanID   string      `json:"plan_id"`
	PlanHash string      `json:"plan_hash"`
	Meta     PlannerMeta `json:"planner_meta"`
	Attempt  int         `json:"attempt"`
}

// Rule é a regra de validação estrutural violada (§3.3, regras 1–6). Enum
// classificado e fail-closed — NUNCA texto livre que pudesse arrastar conteúdo.
type Rule string

const (
	RuleSchema            Rule = "schema"             // campos desconhecidos / tipos
	RuleAcyclicity        Rule = "acyclicity"         // proposta cíclica
	RuleToolResolution    Rule = "tool_resolution"    // tool inexistente/deprecada/fora da allowlist
	RuleStructuralCeiling Rule = "structural_ceiling" // depth/fanout/cardinalidade
	RuleBudget            Rule = "budget"             // budget_total / teto por-ramo
	RuleRisk              Rule = "risk"               // risk_class abaixo do piso derivado
)

func (r Rule) valid() bool {
	switch r {
	case RuleSchema, RuleAcyclicity, RuleToolResolution, RuleStructuralCeiling, RuleBudget, RuleRisk:
		return true
	default:
		return false
	}
}

// ValidationOutcome é o resultado INTERNO do validador. PODE referenciar o
// conteúdo untrusted do PlanDocument (ADR-005) — o literal ofensor, texto cru do
// modelo — em [ValidationOutcome.RawDetail], que serve APENAS logs locais sob o
// gate soberano do nó. NÃO é um evento: nunca chega ao Event Store. A projecção
// para o evento ([NewValidationFailed]) DESCARTA RawDetail.
type ValidationOutcome struct {
	PlanID      string
	PlanHash    string
	Rule        Rule
	Attempt     int
	MaxAttempts int
	// RawDetail é o detalhe cru do validador — POSSIVELMENTE SENSÍVEL/PII. Existe
	// para diagnóstico local; é PROIBIDO ecoá-lo no evento (§6.1, CA(c)).
	RawDetail string
}

// ValidationFailedPayload — corpo de `plan.validation_failed`. Só metadados
// classificados: a regra violada, um código de diagnóstico LIMITADO (derivado da
// regra, content-free), e a tentativa n/N. Sem eco de conteúdo sensível — o campo
// que poderia carregá-lo (RawDetail) não existe aqui, por construção.
type ValidationFailedPayload struct {
	PlanID      string `json:"plan_id"`
	PlanHash    string `json:"plan_hash"`
	Rule        Rule   `json:"rule"`
	Diagnostic  string `json:"diagnostic"`
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts"`
}

// ErrUnknownRule é devolvido quando a regra de validação não é classificada —
// fail-closed: sem um diagnóstico limitado conhecido, não se emite o evento (não
// se inventa um código, nem se cai para texto livre que pudesse vazar conteúdo).
var ErrUnknownRule = errors.New("plannerevents: regra de validação desconhecida")

// diagnosticFor mapeia uma regra para um código de diagnóstico LIMITADO e
// content-free. É a única forma do «diagnóstico» de §6.1 — um rótulo estável, não
// uma mensagem. Fail-closed para regras desconhecidas.
func diagnosticFor(r Rule) (string, bool) {
	switch r {
	case RuleSchema:
		return "schema_violation", true
	case RuleAcyclicity:
		return "cycle_detected", true
	case RuleToolResolution:
		return "tool_unresolved", true
	case RuleStructuralCeiling:
		return "ceiling_exceeded", true
	case RuleBudget:
		return "budget_exceeded", true
	case RuleRisk:
		return "risk_floor_violated", true
	default:
		return "", false
	}
}

// NewValidationFailed projecta um [ValidationOutcome] no payload do evento,
// DESCARTANDO RawDetail — só metadados classificados sobrevivem (§6.1, CA(c)).
// Fail-closed: uma regra desconhecida devolve [ErrUnknownRule] e o evento não é
// emitido. Esta é a fronteira que impede o vazamento de PII em
// `plan.validation_failed`; alterá-la para copiar RawDetail parte os testes de
// não-vazamento.
func NewValidationFailed(o ValidationOutcome) (ValidationFailedPayload, error) {
	if !o.Rule.valid() {
		return ValidationFailedPayload{}, fmt.Errorf("%w: %q", ErrUnknownRule, o.Rule)
	}
	diag, ok := diagnosticFor(o.Rule)
	if !ok {
		return ValidationFailedPayload{}, fmt.Errorf("%w: %q", ErrUnknownRule, o.Rule)
	}
	return ValidationFailedPayload{
		PlanID:      o.PlanID,
		PlanHash:    o.PlanHash,
		Rule:        o.Rule,
		Diagnostic:  diag,
		Attempt:     o.Attempt,
		MaxAttempts: o.MaxAttempts,
	}, nil
}

// ValidatedPayload — corpo de `plan.validated`: hash, nº de nós, budget_total e os
// tectos estruturais aplicados (§3.3, regra 4).
type ValidatedPayload struct {
	PlanID      string `json:"plan_id"`
	PlanHash    string `json:"plan_hash"`
	NodeCount   int    `json:"node_count"`
	BudgetTotal int64  `json:"budget_total"`
	MaxDepth    int    `json:"max_depth"`
	MaxFanout   int    `json:"max_fanout"`
	MaxNodes    int    `json:"max_nodes"`
}

// Decision é a decisão do gate de aprovação-de-plano (AOS-121).
type Decision string

const (
	DecisionApproved Decision = "approved"
	DecisionRejected Decision = "rejected"
	DecisionEdited   Decision = "edited"
)

func (d Decision) valid() bool {
	return d == DecisionApproved || d == DecisionRejected || d == DecisionEdited
}

// eventForDecision devolve o tipo de evento correspondente à decisão. Fail-closed.
func eventForDecision(d Decision) (string, bool) {
	switch d {
	case DecisionApproved:
		return EventApproved, true
	case DecisionRejected:
		return EventRejected, true
	case DecisionEdited:
		return EventEdited, true
	default:
		return "", false
	}
}

// DecisionPayload — corpo de `plan.approved`/`rejected`/`edited`. Carrega o hash
// final, a REFERÊNCIA à decisão assinada (hitl.Channel, AOS-121) — não a
// assinatura crua nem PII do decisor — e o hash do diff estrutural da edição.
type DecisionPayload struct {
	PlanID       string   `json:"plan_id"`
	PlanHash     string   `json:"plan_hash"`
	Decision     Decision `json:"decision"`
	DecisionRef  string   `json:"decision_ref"`
	StructDiffID string   `json:"struct_diff_id,omitempty"`
}

// SpawnKind distingue como um nó materializa (§4.1/§6.1).
type SpawnKind string

const (
	// SpawnLeaf — nó-folha: `task.node.created` (AOS-025).
	SpawnLeaf SpawnKind = "leaf"
	// SpawnRole — papel-que-expande: `Delegator.Spawn` (AOS-026).
	SpawnRole SpawnKind = "role"
)

func (k SpawnKind) valid() bool { return k == SpawnLeaf || k == SpawnRole }

// MaterializedNode é o mapeamento de um nó do plano para o DAG. `tools[]` vincula
// o `Authority[]` da NHI filha (issuer_child) — são ids de tool, não credenciais.
type MaterializedNode struct {
	NodeID string    `json:"node_id"`
	Kind   SpawnKind `json:"kind"`
	Tools  []string  `json:"tools,omitempty"`
}

// MaterializedPayload — corpo de `plan.materialized`: o hash aprovado e o mapa
// node_id → materialização. Determinístico a partir do documento gravado (§3.6).
type MaterializedPayload struct {
	PlanID   string             `json:"plan_id"`
	PlanHash string             `json:"plan_hash"`
	Nodes    []MaterializedNode `json:"nodes"`
}

// GapState distingue a fase do gap de capacidade.
type GapState string

const (
	GapOpened   GapState = "opened"
	GapResolved GapState = "resolved"
)

func (g GapState) valid() bool { return g == GapOpened || g == GapResolved }

func eventForGap(g GapState) (string, bool) {
	switch g {
	case GapOpened:
		return EventCapabilityGapOpened, true
	case GapResolved:
		return EventCapabilityGapResolved, true
	default:
		return "", false
	}
}

// CapabilityGapPayload — corpo de `plan.capability_gap_opened`/`resolved`:
// node_id, skill candidata (id) e RatificationID (AOS-096). Ids opacos, sem PII.
type CapabilityGapPayload struct {
	PlanID         string   `json:"plan_id"`
	NodeID         string   `json:"node_id"`
	State          GapState `json:"state"`
	CandidateSkill string   `json:"candidate_skill"`
	RatificationID string   `json:"ratification_id,omitempty"`
}

// ReplanPhase distingue o pedido da aplicação do re-plano.
type ReplanPhase string

const (
	ReplanRequested ReplanPhase = "requested"
	ReplanApplied   ReplanPhase = "applied"
)

func (p ReplanPhase) valid() bool { return p == ReplanRequested || p == ReplanApplied }

func eventForReplan(p ReplanPhase) (string, bool) {
	switch p {
	case ReplanRequested:
		return EventReplanRequested, true
	case ReplanApplied:
		return EventReplanApplied, true
	default:
		return "", false
	}
}

// ReplanPayload — corpo de `plan.replan_requested`/`applied`: o subgrafo afectado
// (ids de nós), o orçamento residual e o novo hash (§4.2).
type ReplanPayload struct {
	PlanID         string      `json:"plan_id"`
	Phase          ReplanPhase `json:"phase"`
	Subgraph       []string    `json:"subgraph"`
	ResidualBudget int64       `json:"residual_budget"`
	NewPlanHash    string      `json:"new_plan_hash"`
}

// marshal serializa um payload de domínio de forma determinística (encoding/json
// é estável para structs: ordem dos campos). Erro raro (só tipos não
// serializáveis, que não existem aqui) propaga fail-closed.
func marshal(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}
