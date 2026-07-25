package rm

import (
	"context"
	"time"
)

// ToolFunc é a assinatura de uma tool despachável. Recebe o input opaco do call
// e devolve o resultado (marcado untrusted a jusante) ou um erro de execução.
//
// IMPORTANTE (no-bypass): um valor ToolFunc NUNCA deve ser invocado
// directamente fora do RM. A única via legítima de execução é [Monitor.Mediate],
// que só despacha após permit.
type ToolFunc func(ctx context.Context, input []byte) ([]byte, error)

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
// os testes usam um fake; produção usa um adaptador do Event Store.
//
// Semântica fail-closed: se RecordMediation devolver erro num caminho de
// permit, o RM degrada a decisão para Deny (ver Monitor.Mediate).
type EventSink interface {
	// RecordMediation grava o registo e devolve o seq atribuído no log. Um seq 0
	// com err nil indica um sink que não materializa seq (ex.: descarte).
	RecordMediation(ctx context.Context, rec MediationRecord) (seq uint64, err error)
}
