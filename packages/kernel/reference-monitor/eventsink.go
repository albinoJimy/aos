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
)

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
	PortVersion   string       `json:"port_version"`
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
}

type principalDTO struct {
	NHIID     string   `json:"nhi_id,omitempty"`
	AgentID   string   `json:"agent_id,omitempty"`
	Authority []string `json:"authority,omitempty"`
	// DelegationChain é a cadeia on-behalf-of completa (raiz humana → agente
	// actual) também no payload da mediação, para reconstruir "quem autorizou"
	// directamente do payload sem depender do envelope Producer (AOS-006).
	DelegationChain []delegationHopDTO `json:"delegation_chain,omitempty"`
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
			Taint:         rec.Context.Taint,
			Reversibility: rec.Context.Reversibility,
			Sensitivity:   rec.Context.Sensitivity,
		},
		LatencyNanos: rec.Latency.Nanoseconds(),
		Principal: principalDTO{
			NHIID:           rec.Principal.NHIID,
			AgentID:         rec.Principal.AgentID,
			Authority:       rec.Principal.Authority,
			DelegationChain: toHopDTOs(rec.Principal.DelegationChain),
		},
		Obligations: rec.Obligations,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	in := eventstore.EventInput{
		Type:         eventTypeFor(rec.Effect),
		Payload:      raw,
		RunID:        rec.RunID,
		StepID:       rec.StepID,
		ParentStepID: rec.ParentStepID,
		Producer: eventstore.Producer{
			NHIID:           rec.Principal.NHIID,
			DelegationChain: toStoreChain(rec.Principal.DelegationChain),
			Scope:           rec.Principal.Authority,
		},
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
		out[i] = delegationHopDTO{Sub: h.Sub, ActAs: h.ActAs}
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
