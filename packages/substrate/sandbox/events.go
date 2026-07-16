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
	// ImageVersion é a versão da imagem base read-only da microVM (AOS-066),
	// gravada no manifesto por trajectória. Vazia se não configurada.
	ImageVersion string
	// SeccompProfileHash é o HASH (sha256) do perfil seccomp aplicado (AOS-066): o
	// hash do manifesto da execução. NÃO é segredo (ADR-006).
	SeccompProfileHash string
	// SeccompProfileVersion é a versão tamper-evident ("tag#digest12") do perfil
	// seccomp aplicado (AOS-066).
	SeccompProfileVersion string
	// RootFSBaseDigest é o digest do snapshot base read-only EFETIVAMENTE montado
	// (AOS-066). Prova, no manifesto, que o rootfs foi montado (não só declarado) e
	// liga a execução à imagem base imutável exacta. Vazio quando não há snapshot
	// configurado (a raiz read-only é então só uma declaração de config).
	RootFSBaseDigest string
	// OverlayID é o id do overlay efémero desta execução (AOS-065/066). Único por
	// restore; prova por trajectória de que cada execução tem o seu overlay (nunca
	// reciclado). Vazio quando não há snapshot configurado.
	OverlayID string
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
	// Manifesto de segurança AOS-066 (rootfs read-only + overlay efémero + seccomp).
	// O hash do perfil seccomp liga a trajectória à versão EXACTA do perfil em vigor.
	ImageVersion          string `json:"image_version,omitempty"`
	SeccompProfileHash    string `json:"seccomp_profile_hash,omitempty"`
	SeccompProfileVersion string `json:"seccomp_profile_version,omitempty"`
	// Prova do rootfs EFETIVAMENTE montado (AOS-066): só presentes quando o overlay
	// read-only é montado (WithSnapshot), distinguindo imposição de mera declaração.
	RootFSBaseDigest string `json:"rootfs_base_digest,omitempty"`
	OverlayID        string `json:"overlay_id,omitempty"`
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
		ExitCode:              ev.ExitCode,
		CostMicroUSD:          ev.CostMicroUSD,
		Taint:                 string(TaintUntrusted),
		CredentialsHandle:     ev.CredentialsHandle,
		ImageVersion:          ev.ImageVersion,
		SeccompProfileHash:    ev.SeccompProfileHash,
		SeccompProfileVersion: ev.SeccompProfileVersion,
		RootFSBaseDigest:      ev.RootFSBaseDigest,
		OverlayID:             ev.OverlayID,
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
