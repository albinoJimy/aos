package referencemonitor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento de mediação gravados no Event Store.
const (
	// EventTypeMediated — tool call permitida e despachada.
	EventTypeMediated = "tool.call.mediated"
	// EventTypeDenied — tool call negada (fail-closed).
	EventTypeDenied = "tool.call.denied"
	// EventTypeEscalated — tool call escalada a gate humano.
	EventTypeEscalated = "tool.call.escalated"
	// EventTypeOutcome — DESFECHO pós-efeito de uma tool call permitida (ADR-025): ok/erro de
	// EXECUÇÃO, o que os selos de decisão (mediated/denied/escalated) não capturam. Alimenta a
	// fiabilidade medida da promoção de autonomia (AOS-090).
	EventTypeOutcome = "tool.call.outcome"
)

// Prefixos do `step_id` do ENVELOPE dos eventos de mediação e de desfecho (AOS-481).
//
// # Porque uma chave por tipo
//
// O Event Store deduplica pela chave `run_id:step_id` DENTRO do stream, QUALQUER que seja o tipo
// do evento, e devolve `StatusDuplicate` SEM erro. Com o `step_id` da call em todos, o primeiro
// evento de um passo engolia os seguintes: o `tool.call.outcome` colidia com o
// `tool.call.mediated` do mesmo passo (gravado antes, audit-before-effect); a decisão que segue
// uma aprovação (permit ou deny) colidia com o `tool.call.escalated`; e o permit que segue uma
// recusa no mesmo passo (AOS-485) colidia com o `tool.call.denied`. O WORM tinha tudo; o log do
// run não, e a fiabilidade da promoção de autonomia (AOS-090, ADR-025) nunca recebia desfechos.
//
// A chave passa a ser f(`step_id` da call, tipo do evento): cada TIPO de decisão de um passo, e o
// desfecho, tem a sua. O `tool.call.mediated` fica com o `step_id` nu, como sempre: é o selo
// pré-efeito, o tipo dominante em todos os logs gravados, e quem já o lia pela chave do passo
// continua a lê-lo. Os outros três ganham o nome do tipo como prefixo.
//
// # Porque não há sufixo de tentativa
//
// A idempotência continua a ser por (`run_id`, `step_id`, tipo): a re-execução determinista da
// mesma decisão no mesmo passo — uma retoma que volta a pedir a mesma call e recebe o mesmo
// veredicto — deduplica, que é a propriedade que a chave existe para dar. Um número de tentativa
// estável sob re-execução teria de vir de fora do RM (o RM não sabe que tentativa é esta, e
// contá-la no stream não é determinista em replay). O único caso conhecido de duas decisões do
// MESMO tipo, e com significado distinto, no mesmo passo é uma segunda escalada depois de uma
// aprovação inválida ou expirada; a segunda fica no WORM e no `approval.pending` próprio, e não
// num segundo `tool.call.escalated` (residual declarado no AOS-481).
//
// Os prefixos não colidem com nenhum domínio de dedup do stream do run: os `step_id` do runtime
// (`step-NNNNNN-tool-N`), `ledger-`, `ckpt-`, `cap-`, `custo-modelo-`, `consumo-`, `ctrl-`,
// `state-`, `comp-` e `lease-`. Nos três tipos com prefixo o `step_id` da call vai também no
// payload (`step_id`), para a correlação não ter de desfazer o prefixo.
const (
	StepPrefixDenied    = "denied-"
	StepPrefixEscalated = "escalated-"
	StepPrefixOutcome   = "outcome-"
)

// EventStepID devolve o `step_id` do envelope de um evento `tool.call.*` do tipo dado para a
// call do passo `callStepID` (AOS-481). `tool.call.mediated` (e qualquer tipo fora da família)
// fica com o passo nu. Um passo vazio fica vazio: um prefixo sobre o vazio daria a todas as
// calls sem passo de um run a MESMA chave fixa, e o comportamento de hoje para elas não muda.
func EventStepID(eventType, callStepID string) string {
	if callStepID == "" {
		return ""
	}
	switch eventType {
	case EventTypeDenied:
		return StepPrefixDenied + callStepID
	case EventTypeEscalated:
		return StepPrefixEscalated + callStepID
	case EventTypeOutcome:
		return StepPrefixOutcome + callStepID
	default:
		return callStepID
	}
}

// MediationRecord é o registo de auditoria de uma mediação. O RM constrói-o
// para permit E para deny/escalate e submete-o ao [EventSink].
type MediationRecord struct {
	RequestID    string
	RunID        string
	StepID       string
	ParentStepID string
	Effect       Effect
	Code         string
	DeniedBy     string
	Reason       string
	ToolID       string
	Capability   string
	Resource     Resource
	Context      CallContext
	Principal    Principal
	Latency      time.Duration
	Obligations  []Obligation
	// PolicyVersion é a versão de política em vigor na decisão (preenchida pelo
	// PDP via [HookResult.PolicyVersion]). Fica no evento de mediação (AOS-004).
	PolicyVersion string
	// HumanApproved diz que o permit foi destravado por uma aprovação humana VERIFICADA
	// ([ApprovalGate], [Call.HumanApproval]) — não-forjável, só este pacote o põe na call. É o
	// sinal, dentro do próprio selo, de que a call precisou de humano, mesmo quando a escalada
	// que a precedeu não está no log que o leitor vê (escalada antes de um reinício, ou cujo
	// registo best-effort falhou). AOS-481, emenda de 2026-10-18 ao ADR-025.
	HumanApproved bool
	// Metadata são os metadados estruturados do hook que TERMINOU a mediação
	// (AOS-340), vindos de [HookResult.Metadata]. Registo, nunca enforcement: não
	// passam por `enforceObligations` e não podem negar nada.
	Metadata map[string]string
}

// EventSink é a porta mínima de que o RM precisa para gravar cada mediação de
// forma durável. É a fronteira estável entre o RM e o Event Store (AOS-002):
// os testes usam um fake; produção usa [NewEventStoreSink].
//
// Semântica fail-closed: se RecordMediation devolver erro num caminho de
// permit, o RM degrada a decisão para Deny (ver Monitor.Mediate).
type EventSink interface {
	// RecordMediation grava o registo e devolve o seq atribuído no log. Um seq 0
	// com err nil indica um sink que não materializa seq (ex.: descarte).
	RecordMediation(ctx context.Context, rec MediationRecord) (seq uint64, err error)
}

// eventTypeFor mapeia o Effect para o tipo de evento canónico.
func eventTypeFor(e Effect) string {
	switch e {
	case EffectPermit:
		return EventTypeMediated
	case EffectEscalate:
		return EventTypeEscalated
	default:
		return EventTypeDenied
	}
}

// mediationPayload é o corpo JSON do evento de mediação. Não contém segredos
// (o Input da tool NÃO é gravado; só metadados de decisão). Inclui o recurso
// alvo e o contexto de decisão para que o evento seja explicável/auditável sem
// depender de estado externo (contrato C1, tecnica/12 §4).
type mediationPayload struct {
	PortVersion string `json:"port_version"`
	// StepID é o `step_id` da call (AOS-481), só quando o do envelope NÃO é ele: em `denied` e
	// `escalated` o envelope leva o prefixo do tipo ([EventStepID]), e é por este campo que a
	// decisão se correlaciona com o `tool.call.mediated` e o desfecho da mesma call. No
	// `mediated` fica omitido: o envelope já é o passo da call, e o payload do selo fica
	// byte-a-byte o de antes (o guarda do AOS-497 compara-o com a base).
	StepID        string       `json:"step_id,omitempty"`
	PolicyVersion string       `json:"policy_version,omitempty"`
	RequestID     string       `json:"request_id,omitempty"`
	Decision      string       `json:"decision"`
	Code          string       `json:"code,omitempty"`
	Reason        string       `json:"reason,omitempty"`
	DeniedBy      string       `json:"denied_by,omitempty"`
	ToolID        string       `json:"tool_id"`
	Capability    string       `json:"capability,omitempty"`
	Resource      resourceDTO  `json:"resource,omitempty"`
	Context       contextDTO   `json:"context,omitempty"`
	LatencyNanos  int64        `json:"latency_ns"`
	Principal     principalDTO `json:"principal"`
	Obligations   []Obligation `json:"obligations,omitempty"`
	// Metadata é o canal estruturado do hook que terminou a mediação (AOS-340).
	// `omitempty` mantém o payload de quem nada acrescenta byte-a-byte igual ao de
	// antes — acrescentar um campo OPCIONAL é MINOR no `port_version` (tecnica/12 §4).
	Metadata map[string]string `json:"metadata,omitempty"`
	// HumanApproved: o permit veio de uma aprovação humana verificada ([MediationRecord.HumanApproved]).
	// `omitempty`: um permit directo fica byte-a-byte o de antes.
	HumanApproved bool `json:"human_approved,omitempty"`
}

type resourceDTO struct {
	Type   string `json:"type,omitempty"`
	Value  string `json:"value,omitempty"`
	Region string `json:"region,omitempty"`
}

type contextDTO struct {
	Taint         string `json:"taint,omitempty"`
	Reversibility string `json:"reversibility,omitempty"`
	Sensitivity   string `json:"sensitivity,omitempty"`
	// RiskClass é a classe de risco SA-ROC selada no audit (AOS-074, ADR-013).
	RiskClass string `json:"risk_class,omitempty"`
	// RiskApprover é a identidade que autorizou um override gray/danger (SAROC-05).
	RiskApprover string `json:"risk_approver,omitempty"`
	// RiskDecisionMode é a natureza da decisão HITL: auto/batch/human/timeout/denied
	// (SAROC-05) — torna atribuível "quem aprovou e por que via".
	RiskDecisionMode string `json:"risk_decision_mode,omitempty"`
}

type principalDTO struct {
	NHIID      string   `json:"nhi_id,omitempty"`
	AgentID    string   `json:"agent_id,omitempty"`
	AgentClass string   `json:"agent_class,omitempty"`
	Authority  []string `json:"authority,omitempty"`
	// DelegationChain é a cadeia on-behalf-of completa (raiz humana → agente
	// actual) também no payload da mediação, para reconstruir "quem autorizou"
	// directamente do payload sem depender do envelope Producer (AOS-006).
	DelegationChain []delegationHopDTO `json:"delegation_chain,omitempty"`
	// MandateID é o mandato verificado sob o qual o token foi cunhado (AOS-439, resíduo 3 do
	// ADR-033). RequestedBy é o submissor do run, derivado pelo nó da reclamação do plano
	// (AOS-439). Os dois são OPCIONAIS (`omitempty`): o payload de um run sem mandato nem
	// submissor fica byte-a-byte igual ao de antes — MINOR no `port_version` (tecnica/12 §4).
	MandateID   string `json:"mandate_id,omitempty"`
	RequestedBy string `json:"requested_by,omitempty"`
	// MandateSigner é a impressão digital do pino que verificou o mandato (AOS-446 fase 1).
	// OPCIONAL pela mesma razão dos dois acima: sem mandato o payload fica byte-a-byte igual.
	MandateSigner string `json:"mandate_signer,omitempty"`
}

// delegationHopDTO é um elo (sub/act_as) da cadeia serializado no payload.
type delegationHopDTO struct {
	Sub   string `json:"sub"`
	ActAs string `json:"act_as"`
}

// appender é o subconjunto do Event Store de que o adaptador depende. Manter
// mínimo facilita testes e desacopla o RM da superfície completa do store.
type appender interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
}

// eventStoreSink adapta o Event Store (AOS-002) à porta [EventSink].
type eventStoreSink struct {
	store appender
}

// NewEventStoreSink constrói um [EventSink] que grava as mediações no Event
// Store dado. O stream_id é o run_id; o tipo é derivado do Effect.
func NewEventStoreSink(store eventstore.EventStore) EventSink {
	return &eventStoreSink{store: store}
}

func (s *eventStoreSink) RecordMediation(ctx context.Context, rec MediationRecord) (uint64, error) {
	typ := eventTypeFor(rec.Effect)
	stepID := EventStepID(typ, rec.StepID)
	payload := mediationPayload{
		PortVersion:   PortVersion,
		PolicyVersion: rec.PolicyVersion,
		RequestID:     rec.RequestID,
		Decision:      string(rec.Effect),
		Code:          rec.Code,
		Reason:        rec.Reason,
		DeniedBy:      rec.DeniedBy,
		ToolID:        rec.ToolID,
		Capability:    rec.Capability,
		Resource: resourceDTO{
			Type:   rec.Resource.Type,
			Value:  rec.Resource.Value,
			Region: rec.Resource.Region,
		},
		Context: contextDTO{
			Taint:            rec.Context.Taint,
			Reversibility:    rec.Context.Reversibility,
			Sensitivity:      rec.Context.Sensitivity,
			RiskClass:        rec.Context.RiskClass,
			RiskApprover:     rec.Context.RiskApprover,
			RiskDecisionMode: rec.Context.RiskDecisionMode,
		},
		LatencyNanos: rec.Latency.Nanoseconds(),
		Principal: principalDTO{
			NHIID:           rec.Principal.NHIID,
			AgentID:         rec.Principal.AgentID,
			AgentClass:      rec.Principal.AgentClass,
			Authority:       rec.Principal.Authority,
			DelegationChain: toHopDTOs(rec.Principal.DelegationChain),
			MandateID:       rec.Principal.MandateID,
			RequestedBy:     rec.Principal.RequestedBy,
			MandateSigner:   rec.Principal.MandateSigner,
		},
		Obligations:   rec.Obligations,
		Metadata:      rec.Metadata,
		HumanApproved: rec.HumanApproved,
	}
	if stepID != rec.StepID {
		payload.StepID = rec.StepID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	in := eventstore.EventInput{
		Type:    typ,
		Payload: raw,
		RunID:   rec.RunID,
		// Chave própria por tipo (AOS-481): sem ela a decisão que segue uma escalada ou uma
		// recusa no mesmo passo era engolida como duplicado. Ver [EventStepID].
		StepID:       stepID,
		ParentStepID: rec.ParentStepID,
		// A projecção é partilhada com os factos que a call causa a jusante (AOS-478,
		// [Principal.EventProducer]): o mesmo passo identifica o mesmo principal.
		Producer: rec.Principal.EventProducer(),
	}
	res, err := s.store.Append(ctx, rec.RunID, in)
	if err != nil {
		return 0, err
	}
	return res.Seq, nil
}

// toHopDTOs projecta a cadeia de delegação do RM para os elos do payload.
func toHopDTOs(chain []DelegationHop) []delegationHopDTO {
	if len(chain) == 0 {
		return nil
	}
	out := make([]delegationHopDTO, len(chain))
	for i, h := range chain {
		// Conversão directa (campos idênticos; as tags JSON do DTO não afectam a
		// convertibilidade). Equivale a delegationHopDTO{Sub: h.Sub, ActAs: h.ActAs}.
		out[i] = delegationHopDTO(h)
	}
	return out
}

// toStoreChain converte a cadeia de delegação do RM para o modelo do Event Store.
func toStoreChain(chain []DelegationHop) []eventstore.DelegationHop {
	if len(chain) == 0 {
		return nil
	}
	out := make([]eventstore.DelegationHop, len(chain))
	for i, h := range chain {
		out[i] = eventstore.DelegationHop{Sub: h.Sub, ActAs: h.ActAs}
	}
	return out
}

// discardSink é o EventSink por omissão: não materializa nada e devolve seq 0.
// Destina-se a testes/benchmark; produção DEVE injectar um sink durável, sob
// pena de o fail-closed de auditoria nunca disparar (a acção seria permitida
// sem rasto). Ver New / WithEventSink.
type discardSink struct{}

func (discardSink) RecordMediation(context.Context, MediationRecord) (uint64, error) {
	return 0, nil
}

// outcomePayload é o corpo JSON do evento tool.call.outcome (ADR-025). Sem segredos: só o
// desfecho, a classe/domínio de onde a fiabilidade agrega, e metadados de correlação.
type outcomePayload struct {
	PortVersion string `json:"port_version"`
	// StepID é o `step_id` da call (AOS-481): o do envelope é `outcome-<step_id>`. É por este
	// que o desfecho se correlaciona com o `tool.call.mediated` da mesma call.
	StepID       string      `json:"step_id,omitempty"`
	Outcome      string      `json:"outcome"`
	ErrorKind    string      `json:"error_kind,omitempty"`
	ToolID       string      `json:"tool_id,omitempty"`
	Capability   string      `json:"capability,omitempty"`
	Resource     resourceDTO `json:"resource,omitempty"`
	AgentClass   string      `json:"agent_class,omitempty"`
	LatencyNanos int64       `json:"latency_ns"`
}

// eventStoreOutcomeSink adapta o Event Store à porta [OutcomeSink]: grava o desfecho
// pós-efeito no stream do run (stream_id == run_id), no tipo [EventTypeOutcome].
type eventStoreOutcomeSink struct {
	store appender
}

// NewEventStoreOutcomeSink constrói um [OutcomeSink] que grava os desfechos no Event Store
// dado (ADR-025). Composto no nó a par do [NewEventStoreSink]; a promoção de autonomia
// (AOS-090) lê estes eventos para medir fiabilidade.
func NewEventStoreOutcomeSink(store eventstore.EventStore) OutcomeSink {
	return &eventStoreOutcomeSink{store: store}
}

func (s *eventStoreOutcomeSink) RecordOutcome(ctx context.Context, rec OutcomeRecord) error {
	payload := outcomePayload{
		PortVersion:  PortVersion,
		StepID:       rec.StepID,
		Outcome:      rec.Outcome,
		ErrorKind:    rec.ErrorKind,
		ToolID:       rec.ToolID,
		Capability:   rec.Capability,
		Resource:     resourceDTO{Type: rec.Resource.Type, Value: rec.Resource.Value, Region: rec.Resource.Region},
		AgentClass:   rec.AgentClass,
		LatencyNanos: rec.Latency.Nanoseconds(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.store.Append(ctx, rec.RunID, eventstore.EventInput{
		Type:    EventTypeOutcome,
		Payload: raw,
		RunID:   rec.RunID,
		// AOS-481: `outcome-<step_id>`. Com o `step_id` nu, o desfecho colidia com o selo
		// `tool.call.mediated` do mesmo passo, gravado antes, e o Event Store engolia-o.
		StepID: EventStepID(EventTypeOutcome, rec.StepID),
		// AOS-478: o desfecho é da MESMA tool call que o selo `tool.call.mediated` do passo,
		// e identifica o mesmo principal.
		Producer: rec.Principal.EventProducer(),
	})
	return err
}
