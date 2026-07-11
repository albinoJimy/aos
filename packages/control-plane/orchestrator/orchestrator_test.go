package orchestrator_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/contract"
	"github.com/aos-ref/substrate/bus"
	"github.com/aos-ref/substrate/eventstore"
)

// harness monta Event Store + barramento + Orquestrador para os testes do ORQ.
type harness struct {
	es   *eventstore.Store
	bus  *bus.Bus
	orch *orchestrator.Orchestrator
	ctx  context.Context
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	b, err := bus.New(es)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	orch, err := orchestrator.New(b)
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	t.Cleanup(func() {
		_ = b.Close()
		_ = es.Close()
	})
	return &harness{es: es, bus: b, orch: orch, ctx: context.Background()}
}

// collect subscreve um coletor por tipo e devolve um canal com os eventos brutos.
// Deve ser chamado ANTES de Submit (entrega live).
func (h *harness) collect(t *testing.T, evType string) <-chan eventstore.Event {
	t.Helper()
	ch := make(chan eventstore.Event, 8)
	sub, err := h.bus.Subscribe(h.ctx, bus.SubConfig{
		Name:   "coletor:" + evType,
		Filter: bus.Filter{Types: []string{evType}},
		Handler: func(d *bus.Delivery) {
			d.Ack()
			ch <- d.Event
		},
	})
	if err != nil {
		t.Fatalf("subscribe %s: %v", evType, err)
	}
	t.Cleanup(sub.Unsubscribe)
	return ch
}

func waitEvent(t *testing.T, ch <-chan eventstore.Event) eventstore.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timeout à espera de evento")
		return eventstore.Event{}
	}
}

// TestSubmitFanOutAndCorrelation prova que Submit emite run.created e task.ready,
// que o barramento faz fan-out por filtro (cada coletor recebe só o seu tipo) e
// que ambos os eventos estão correlacionados pelo mesmo run_id.
func TestSubmitFanOutAndCorrelation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	createdCh := h.collect(t, contract.EventRunCreated)
	readyCh := h.collect(t, contract.EventTaskReady)

	runID, err := h.orch.Submit(h.ctx, contract.Goal{
		Objective: "objetivo de brinquedo",
		Task:      contract.TaskSpec{ToolID: "tool:echo", Capability: "cap:echo", Input: []byte("oi")},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if runID == "" {
		t.Fatal("run_id vazio")
	}

	createdEv := waitEvent(t, createdCh)
	if createdEv.Type != contract.EventRunCreated {
		t.Fatalf("fan-out falhou: coletor run.created recebeu %s", createdEv.Type)
	}
	if createdEv.StreamID != string(runID) {
		t.Fatalf("run.created no stream %q, quero %q", createdEv.StreamID, runID)
	}
	var created contract.RunCreatedPayload
	if err := json.Unmarshal(createdEv.Payload, &created); err != nil {
		t.Fatalf("unmarshal run.created: %v", err)
	}
	if created.RunID != string(runID) {
		t.Fatalf("run.created.run_id=%q, quero %q", created.RunID, runID)
	}
	if created.TaskCount != 1 {
		t.Fatalf("grafo mínimo: TaskCount=%d, quero 1", created.TaskCount)
	}

	readyEv := waitEvent(t, readyCh)
	if readyEv.Type != contract.EventTaskReady {
		t.Fatalf("fan-out falhou: coletor task.ready recebeu %s", readyEv.Type)
	}
	var ready contract.TaskPayload
	if err := json.Unmarshal(readyEv.Payload, &ready); err != nil {
		t.Fatalf("unmarshal task.ready: %v", err)
	}
	// Correlação: ambos os eventos partilham o run_id.
	if ready.RunID != created.RunID {
		t.Fatalf("correlação falhou: task.ready.run_id=%q != run.created.run_id=%q", ready.RunID, created.RunID)
	}
	if ready.State != string(contract.StateReady) {
		t.Fatalf("task.ready.state=%q, quero ready", ready.State)
	}
	if ready.ToolID != "tool:echo" {
		t.Fatalf("task.ready não propagou a spec: %+v", ready)
	}
}

// TestSubmitPersistsAsOrderedEvents prova que o estado é persistido como eventos
// no Event Store, no stream = run_id, na ordem run.created → task.ready.
func TestSubmitPersistsAsOrderedEvents(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	runID, err := h.orch.Submit(h.ctx, contract.Goal{Objective: "x", Task: contract.TaskSpec{ToolID: "tool:echo"}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	evs, err := h.es.Read(h.ctx, string(runID), 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	wantTypes := []string{contract.EventRunCreated, contract.EventTaskReady}
	if len(evs) != len(wantTypes) {
		t.Fatalf("stream tem %d eventos, quero %d", len(evs), len(wantTypes))
	}
	for i, want := range wantTypes {
		if evs[i].Type != want {
			t.Fatalf("evento %d é %s, quero %s", i, evs[i].Type, want)
		}
		if evs[i].Seq != uint64(i+1) {
			t.Fatalf("evento %d tem seq %d, quero %d", i, evs[i].Seq, i+1)
		}
	}
}

func TestSubmitUniqueRunIDs(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seen := make(map[contract.RunID]bool)
	for range 5 {
		id, err := h.orch.Submit(h.ctx, contract.Goal{Objective: "x", Task: contract.TaskSpec{ToolID: "t"}})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if seen[id] {
			t.Fatalf("run_id repetido: %q", id)
		}
		seen[id] = true
	}
}

func TestSubmitContextCancelled(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.orch.Submit(ctx, contract.Goal{Objective: "x"}); err == nil {
		t.Fatal("Submit com contexto cancelado devia falhar")
	}
}

func TestNewNilBus(t *testing.T) {
	t.Parallel()
	if _, err := orchestrator.New(nil); err == nil {
		t.Fatal("New(nil) devia falhar")
	}
}

func TestWithRunIDFunc(t *testing.T) {
	t.Parallel()
	es, _ := eventstore.New()
	b, _ := bus.New(es)
	t.Cleanup(func() { _ = b.Close(); _ = es.Close() })
	orch, err := orchestrator.New(b, orchestrator.WithRunIDFunc(func(n uint64) contract.RunID {
		return contract.RunID("fixo")
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id, err := orch.Submit(context.Background(), contract.Goal{Objective: "x", Task: contract.TaskSpec{ToolID: "t"}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if id != "fixo" {
		t.Fatalf("run_id=%q, quero 'fixo'", id)
	}
}

// _ garante em compile-time que o tipo concreto implementa a porta estável.
var _ contract.Orchestrator = (*orchestrator.Orchestrator)(nil)
