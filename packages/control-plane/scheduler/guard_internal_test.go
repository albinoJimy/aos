package scheduler

// Testes white-box do guard de idempotência de EFEITO em dispatch (finding
// AOS-012 / correctness-idempotency). Provam que, sob re-entrega at-least-once,
// a tool NÃO volta a ser executada: a dedup do Event Store por step_id protege
// só os eventos, não o efeito, por isso dispatch consulta o stream do run antes
// de mediar. São white-box (package scheduler) porque exercitam dispatch e
// priorDispatchState directamente, de forma determinística e sem sleeps.

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/contract"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/bus"
	"github.com/aos-ref/substrate/eventstore"
)

const guardTool = "tool:guard-echo"

// newGuardFixture monta es+bus+rm(+tool contador)+scheduler. O contador da tool
// é o efeito observável: o guard é correcto sse ficar a 0 nas re-entregas.
func newGuardFixture(t *testing.T) (*Scheduler, *eventstore.Store, *bus.Bus, *atomic.Int64) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	b, err := bus.New(es)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	rm := referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooks()...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)
	var calls atomic.Int64
	if err := rm.Register(guardTool, func(_ context.Context, in []byte) ([]byte, error) {
		calls.Add(1)
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	s, err := New(b, rm, es)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close(); _ = es.Close() })
	return s, es, b, &calls
}

// seedState publica directamente um evento de estado no stream do run, para
// simular um despacho anterior (task.running/terminal) sem passar pelo SCH.
func seedState(t *testing.T, b *bus.Bus, runID, evType, stepID, state string) {
	t.Helper()
	raw, _ := json.Marshal(contract.TaskPayload{
		RunID:  runID,
		TaskID: contract.MinimalTaskID,
		StepID: stepID,
		State:  state,
	})
	if _, err := b.Publish(context.Background(), runID, eventstore.EventInput{
		Type:    evType,
		Payload: raw,
		RunID:   runID,
		StepID:  stepID,
	}); err != nil {
		t.Fatalf("seed %s: %v", evType, err)
	}
}

func guardReady(runID string) contract.TaskPayload {
	return contract.TaskPayload{
		RunID:  runID,
		TaskID: contract.MinimalTaskID,
		StepID: contract.StepReady(contract.MinimalTaskID),
		State:  string(contract.StateReady),
		ToolID: guardTool,
	}
}

// TestGuardShortCircuitsWhenTerminal: re-entrega de task.ready num run já
// terminal é um no-op — a tool NÃO volta a correr e nenhum evento novo é escrito.
func TestGuardShortCircuitsWhenTerminal(t *testing.T) {
	t.Parallel()
	s, es, b, calls := newGuardFixture(t)
	const runID = "run-terminal"
	seedState(t, b, runID, contract.EventTaskComplete, contract.StepComplete(contract.MinimalTaskID), string(contract.StateComplete))

	before, err := es.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read antes: %v", err)
	}
	if err := s.dispatch(context.Background(), guardReady(runID)); err != nil {
		t.Fatalf("dispatch devia ser no-op (nil), deu %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("tool correu %d vezes num run já terminal, quero 0 (double-exec!)", got)
	}
	after, err := es.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read depois: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("dispatch escreveu %d evento(s) num run terminal, quero 0", len(after)-len(before))
	}
}

// TestGuardFailsClosedWhenInFlight: re-entrega cujo run tem task.running sem
// terminal (despacho anterior não fechou) fail-closed com terminal indeterminado,
// SEM re-executar a tool.
func TestGuardFailsClosedWhenInFlight(t *testing.T) {
	t.Parallel()
	s, es, b, calls := newGuardFixture(t)
	const runID = "run-inflight"
	seedState(t, b, runID, contract.EventTaskRunning, contract.StepRunning(contract.MinimalTaskID), string(contract.StateRunning))

	if err := s.dispatch(context.Background(), guardReady(runID)); err != nil {
		t.Fatalf("dispatch (in-flight) devia devolver nil após emitir failed, deu %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("tool correu %d vezes numa re-entrega, quero 0 (double-exec!)", got)
	}
	evs, err := es.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var sawFailed bool
	for _, ev := range evs {
		if ev.StepID == contract.StepFailed(contract.MinimalTaskID) {
			sawFailed = true
			var tp contract.TaskPayload
			if err := json.Unmarshal(ev.Payload, &tp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if tp.Code != "E_REDELIVERY_INDETERMINATE" {
				t.Fatalf("code=%q, quero E_REDELIVERY_INDETERMINATE", tp.Code)
			}
		}
	}
	if !sawFailed {
		t.Fatal("guard in-flight não emitiu task.failed terminal")
	}
}

// TestGuardFreshWhenStreamAbsent: sem stream prévio (Read → ErrStreamNotFound), o
// guard classifica fresh e o despacho corre normalmente — a tool executa UMA vez
// e o run chega a task.complete.
func TestGuardFreshWhenStreamAbsent(t *testing.T) {
	t.Parallel()
	s, es, _, calls := newGuardFixture(t)
	const runID = "run-fresh"
	if err := s.dispatch(context.Background(), guardReady(runID)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("tool correu %d vezes, quero 1", got)
	}
	evs, err := es.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var sawComplete bool
	for _, ev := range evs {
		if ev.StepID == contract.StepComplete(contract.MinimalTaskID) {
			sawComplete = true
		}
	}
	if !sawComplete {
		t.Fatal("dispatch fresh não chegou a task.complete")
	}
}
