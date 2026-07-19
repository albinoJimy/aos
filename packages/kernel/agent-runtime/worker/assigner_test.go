package worker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/worker"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ---------------------------------------------------------------------------
// AC3/AC5: ciclo de vida da posse de partição — TryAcquire/Owns/Owned/Release/
// Requeue sobre os leases existentes, sem rebalancing disruptivo.
// ---------------------------------------------------------------------------

func TestAssigner_OwnershipLifecycle(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}
	p := newProc(t, store, clock, ctr, "replica")

	asg, err := worker.NewAssigner(p.lm)
	if err != nil {
		t.Fatalf("NewAssigner: %v", err)
	}

	// run_id vazio é rejeitado.
	if _, _, err := asg.TryAcquire(context.Background(), ""); !errors.Is(err, worker.ErrEmptyRunID) {
		t.Fatalf("esperava ErrEmptyRunID, obtive %v", err)
	}

	// Sharding natural: adquire duas partições (dois runs) distintas — sem mapa de
	// hash fixo, cada run é a sua partição.
	for _, runID := range []string{"run_a", "run_b"} {
		lease, ok, err := asg.TryAcquire(context.Background(), runID)
		if err != nil || !ok {
			t.Fatalf("TryAcquire(%s): ok=%v err=%v", runID, ok, err)
		}
		if lease.RunID != runID {
			t.Fatalf("lease do run errado: %+v", lease)
		}
		if !asg.Owns(runID) {
			t.Fatalf("Owns(%s) devia ser true", runID)
		}
	}
	if owned := asg.Owned(); len(owned) != 2 || owned[0] != "run_a" || owned[1] != "run_b" {
		t.Fatalf("Owned inesperado (ordem estável esperada): %v", owned)
	}

	// Re-adquirir uma partição já detida por ESTA réplica: o lease ainda é vivo, logo
	// o Claim recusa (ErrLeaseHeld) e TryAcquire devolve false — sem duplicar posse.
	if _, ok, err := asg.TryAcquire(context.Background(), "run_a"); err != nil || ok {
		t.Fatalf("re-adquirir partição viva devia devolver false: ok=%v err=%v", ok, err)
	}

	// Release larga a posse em-processo (idempotente).
	asg.Release("run_a")
	asg.Release("run_a")
	if asg.Owns("run_a") {
		t.Fatalf("após Release, Owns(run_a) devia ser false")
	}

	// Requeue é o sinónimo semântico de largar após perda de lease.
	asg.Requeue("run_b")
	if asg.Owns("run_b") {
		t.Fatalf("após Requeue, Owns(run_b) devia ser false")
	}
	if owned := asg.Owned(); len(owned) != 0 {
		t.Fatalf("Owned devia estar vazio, obtive %v", owned)
	}
}

func TestNewAssigner_NilLeaseManager(t *testing.T) {
	t.Parallel()
	if _, err := worker.NewAssigner(nil); !errors.Is(err, worker.ErrNilLeaseManager) {
		t.Fatalf("esperava ErrNilLeaseManager, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Adopt: o lease tem de corresponder ao run do plano (fail-closed).
// ---------------------------------------------------------------------------

func TestWorker_AdoptLeaseRunMismatch(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}
	p := newProc(t, store, clock, ctr, "w")

	lease, err := p.lm.Claim(context.Background(), "run_x")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// Plano de outro run: Adopt recusa.
	if _, err := p.worker(t, nil, otelgenai.NoopTracer{}).Adopt(context.Background(), plan("run_y", 1), lease); err == nil {
		t.Fatalf("Adopt com lease de outro run devia falhar")
	}
	// Plano nil / run vazio.
	w := p.worker(t, nil, otelgenai.NoopTracer{})
	if _, err := w.Adopt(context.Background(), nil, lease); !errors.Is(err, worker.ErrNilPlan) {
		t.Fatalf("esperava ErrNilPlan, obtive %v", err)
	}
	if _, err := w.Run(context.Background(), &worker.RunPlan{RunID: ""}); !errors.Is(err, worker.ErrEmptyRunID) {
		t.Fatalf("esperava ErrEmptyRunID, obtive %v", err)
	}
	if _, err := w.Run(context.Background(), nil); !errors.Is(err, worker.ErrNilPlan) {
		t.Fatalf("esperava ErrNilPlan, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Heartbeat: enquanto a posse é detida, o heartbeat RENOVA o lease (evento
// lease.renewed no log). Exercita o caminho de sucesso do heartbeat periódico.
// ---------------------------------------------------------------------------

func TestWorker_HeartbeatRenewsLease(t *testing.T) {
	t.Parallel()
	const runID = "run_hb_renew"
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}
	p := newProc(t, store, clock, ctr, "worker-A")

	// Tool bloqueante no turno 1: mantém o worker a deter a partição enquanto
	// disparamos o heartbeat.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	rm := referencemonitorNew(store)
	registerBlocking(t, rm, ctr, entered, release)

	tickCh := make(chan time.Time)
	factory := func(time.Duration) (<-chan time.Time, func()) { return tickCh, func() {} }

	w, err := worker.NewWorker(p.lm, p.fenced, p.ledger, p.resume, p.cpr, rm,
		worker.WithStepSequencer(p.seq),
		worker.WithHeartbeatInterval(time.Hour),
		worker.WithTickerFactory(factory),
		worker.WithWorkerID("worker-A"),
		worker.WithProducer(eventstore.Producer{NHIID: "nhi:agent-1"}),
	)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}

	done := make(chan error, 1)
	go func() { _, e := w.Run(context.Background(), plan(runID, 2)); done <- e }()

	<-entered // worker detém o lease (token 1), preso no turno 1

	// Dispara o heartbeat (lease ainda válido — relógio não avançou): renova o TTL.
	tickCh <- clock.Now()

	// Espera DETERMINÍSTICA pela renovação no log durável (sem sleep fixo).
	waitForCount(t, store, leaseStreamID(runID), durable.EventTypeLeaseRenewed, 1)

	close(release) // deixa o worker completar
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("Run devia completar após heartbeat bem-sucedido, obtive %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker não completou")
	}
}

// leaseStreamID replica a convenção interna do stream de lease ("lease:"+run_id)
// para o teste poder observar os eventos de renovação sem API interna.
func leaseStreamID(runID string) string { return "lease:" + runID }

func waitForCount(t *testing.T, store *eventstore.Store, streamID, typ string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs, err := store.Read(context.Background(), streamID, 1)
		if err == nil {
			n := 0
			for _, e := range evs {
				if e.Type == typ {
					n++
				}
			}
			if n >= want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout à espera de %d eventos %q no stream %q", want, typ, streamID)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
