package sandbox

import (
	"context"
	"encoding/json"

	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento do ciclo de vida da sandbox gravados no Event Store (AOS-002).
const (
	// EventInstanceCreated — microVM criada (antes do efeito de exec).
	EventInstanceCreated = "sandbox.instance.created"
	// EventExecCompleted — a tool call correu na microVM (resultado untrusted).
	EventExecCompleted = "sandbox.exec.completed"
	// EventInstanceDestroyed — microVM destruída (garantido; sem órfãs).
	EventInstanceDestroyed = "sandbox.instance.destroyed"
)

// LifecyclePhase é a fase do ciclo de vida a que um evento corresponde.
type LifecyclePhase string

const (
	PhaseCreated   LifecyclePhase = "created"
	PhaseExec      LifecyclePhase = "exec"
	PhaseDestroyed LifecyclePhase = "destroyed"
)

// LifecycleEvent é o registo de uma transição do ciclo de vida. NÃO contém
// segredos: o credentials_handle é opaco e o segredo nunca entra aqui (ADR-006).
type LifecycleEvent struct {
	RunID        string
	StepID       string
	Phase        LifecyclePhase
	InstanceID   string
	Driver       DriverKind
	Isolation    Isolation
	ExitCode     int
	CostMicroUSD int64
	// CredentialsHandle é o id OPACO (não-secreto) presente na execução, para
	// correlação de audit. Vazio quando não há credencial.
	CredentialsHandle string
}

// EventSink é a porta mínima de que o [Launcher] precisa para selar cada
// transição do ciclo de vida de forma durável. Os testes usam a impl de
// referência do Event Store; produção injecta [NewEventStoreSink].
type EventSink interface {
	// RecordLifecycle grava a transição e devolve o seq atribuído. Fail-closed no
	// create: se o create não puder ser auditado, o exec não corre.
	RecordLifecycle(ctx context.Context, ev LifecycleEvent) (seq uint64, err error)
}

// eventTypeFor mapeia a fase para o tipo de evento canónico.
func eventTypeFor(p LifecyclePhase) string {
	switch p {
	case PhaseCreated:
		return EventInstanceCreated
	case PhaseExec:
		return EventExecCompleted
	default:
		return EventInstanceDestroyed
	}
}

// stepIDFor deriva um step_id distinto por fase a partir do step_id canónico. É
// necessário porque a idempotency_key do Event Store é run_id:step_id: três
// eventos com o MESMO (run_id, step_id) seriam deduplicados. O step_id canónico
// vai no payload; o envelope usa o sufixo por fase para manter as três transições
// distintas e append-only.
func stepIDFor(stepID string, p LifecyclePhase) string {
	return stepID + "-sbx-" + string(p)
}

// lifecyclePayload é o corpo JSON do evento (sem segredos).
type lifecyclePayload struct {
	Phase             string       `json:"phase"`
	RunID             string       `json:"run_id"`
	StepID            string       `json:"step_id"`
	InstanceID        string       `json:"instance_id,omitempty"`
	Driver            string       `json:"driver"`
	Isolation         isolationDTO `json:"isolation"`
	ExitCode          int          `json:"exit_code"`
	CostMicroUSD      int64        `json:"cost_micro_usd,omitempty"`
	Taint             string       `json:"taint"`
	CredentialsHandle string       `json:"credentials_handle,omitempty"`
}

type isolationDTO struct {
	NoHostSocket   bool `json:"no_host_socket"`
	NoSharedNetNS  bool `json:"no_shared_net_ns"`
	NoSharedPIDNS  bool `json:"no_shared_pid_ns"`
	RootFSReadOnly bool `json:"rootfs_read_only"`
}

// appender é o subconjunto do Event Store de que o adaptador depende.
type appender interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
}

// eventStoreSink adapta o Event Store (AOS-002) à porta [EventSink].
type eventStoreSink struct {
	store appender
}

// NewEventStoreSink constrói um [EventSink] que sela o ciclo de vida no Event
// Store dado. O stream_id é o run_id; o step_id do envelope é distinto por fase
// (ver [stepIDFor]); o tipo deriva da fase.
func NewEventStoreSink(store eventstore.EventStore) EventSink {
	return &eventStoreSink{store: store}
}

func (s *eventStoreSink) RecordLifecycle(ctx context.Context, ev LifecycleEvent) (uint64, error) {
	payload := lifecyclePayload{
		Phase:      string(ev.Phase),
		RunID:      ev.RunID,
		StepID:     ev.StepID,
		InstanceID: ev.InstanceID,
		Driver:     string(ev.Driver),
		Isolation: isolationDTO{
			NoHostSocket:   ev.Isolation.NoHostSocket,
			NoSharedNetNS:  ev.Isolation.NoSharedNetNS,
			NoSharedPIDNS:  ev.Isolation.NoSharedPIDNS,
			RootFSReadOnly: ev.Isolation.RootFSReadOnly,
		},
		ExitCode:          ev.ExitCode,
		CostMicroUSD:      ev.CostMicroUSD,
		Taint:             string(TaintUntrusted),
		CredentialsHandle: ev.CredentialsHandle,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	in := eventstore.EventInput{
		Type:         eventTypeFor(ev.Phase),
		Payload:      raw,
		RunID:        ev.RunID,
		StepID:       stepIDFor(ev.StepID, ev.Phase),
		ParentStepID: ev.StepID,
	}
	res, err := s.store.Append(ctx, ev.RunID, in)
	if err != nil {
		return 0, err
	}
	return res.Seq, nil
}

// discardSink é o EventSink por omissão: não materializa nada. Destina-se a
// benchmarks; produção DEVE injectar um sink durável ([NewEventStoreSink]).
type discardSink struct{}

func (discardSink) RecordLifecycle(context.Context, LifecycleEvent) (uint64, error) {
	return 0, nil
}
