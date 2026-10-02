package worker_test

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/worker"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// TestAOS478_WorkerProducerPorEvento (AOS-478): sem [worker.WithProducer], o marcador
// `worker.step.dispatched` leva a identidade de componente do worker, nunca um envelope
// vazio; e o `step.ledger.applied` de cada passo leva o principal que o RM resolveu — o
// mesmo do `tool.call.mediated` do passo, com a cadeia.
func TestAOS478_WorkerProducerPorEvento(t *testing.T) {
	store := newStore(t)
	p := newProc(t, store, newManualClock(), &effectCounter{}, "w-478")
	w := p.worker(t, nil, otelgenai.NoopTracer{})
	const runID = "run-aos478-worker"
	if _, err := w.Run(context.Background(), plan(runID, 2)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	evs, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var marcadores, ledger int
	for _, ev := range evs {
		switch ev.Type {
		case worker.EventTypeWorkerStep:
			marcadores++
			if ev.Producer.NHIID != worker.DefaultProducerNHI {
				t.Errorf("%s producer=%+v, quer %q", ev.Type, ev.Producer, worker.DefaultProducerNHI)
			}
		case durable.EventTypeLedgerApplied:
			ledger++
			if ev.Producer.NHIID != "nhi:agent-1" || len(ev.Producer.DelegationChain) != 1 || ev.Producer.DelegationChain[0].Sub != "human:alice" {
				t.Errorf("%s producer=%+v, quer o principal da call com a cadeia", ev.Type, ev.Producer)
			}
		}
	}
	if marcadores == 0 || ledger != 2 {
		t.Fatalf("marcadores=%d ledger=%d — o cenário não exercitou os dois emissores", marcadores, ledger)
	}
}
