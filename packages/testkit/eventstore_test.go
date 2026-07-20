package testkit_test

import (
	"context"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	tk "github.com/aos-ref/testkit"
)

// TestEventStore_AppendRead: o ES in-memory de referência aceita um append e lê-o.
func TestEventStore_AppendRead(t *testing.T) {
	t.Parallel()
	es := tk.MustEventStore(t)
	ctx := context.Background()

	res, err := es.Append(ctx, tk.FixtureRunID, eventstore.EventInput{
		Type:   "test.event",
		RunID:  tk.FixtureRunID,
		StepID: tk.FixtureStepID(1),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if res.Status != eventstore.StatusCommitted || res.Seq != 1 {
		t.Fatalf("Append: status=%v seq=%d", res.Status, res.Seq)
	}

	events, err := es.Read(ctx, tk.FixtureRunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 1 || events[0].Type != "test.event" {
		t.Fatalf("Read: %+v", events)
	}
}

// TestEventStore_Idempotencia: um segundo append com a mesma (run_id, step_id)
// deduplica — a fixture liga-se à dedup REAL do Event Store.
func TestEventStore_Idempotencia(t *testing.T) {
	t.Parallel()
	es := tk.MustEventStore(t)
	ctx := context.Background()
	in := eventstore.EventInput{Type: "t", RunID: tk.FixtureRunID, StepID: tk.FixtureStepID(1)}

	first, err := es.Append(ctx, tk.FixtureRunID, in)
	if err != nil {
		t.Fatalf("Append#1: %v", err)
	}
	second, err := es.Append(ctx, tk.FixtureRunID, in)
	if err != nil {
		t.Fatalf("Append#2: %v", err)
	}
	if second.Status != eventstore.StatusDuplicate {
		t.Fatalf("segundo append: status=%v, esperava duplicate", second.Status)
	}
	if second.Seq != first.Seq {
		t.Fatalf("dedup devolveu seq diferente: %d != %d", second.Seq, first.Seq)
	}
}

// TestEventStore_NewErro: NewEventStore propaga um erro de configuração inválida.
func TestEventStore_NewErro(t *testing.T) {
	t.Parallel()
	if _, err := tk.NewEventStore(eventstore.WithReplicas(0)); err == nil {
		t.Fatal("esperava erro para WithReplicas(0)")
	}
}
