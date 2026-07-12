package activity_test

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/activity"
)

// newBenchHarness constrói o harness de bench (permit, sem erro de tool).
func newBenchHarness(b *testing.B) *harness { return newHarness(b, false, nil) }

// BenchmarkDispatch_NormalDedup mede o custo do caminho quente: a primeira chamada
// aplica o efeito; as seguintes batem no ledger (already-applied) sem re-executar.
func BenchmarkDispatch_NormalDedup(b *testing.B) {
	h := newBenchHarness(b)
	d, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		b.Fatalf("NewDispatcher: %v", err)
	}
	act := baseActivity()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.Dispatch(ctx, act); err != nil {
			b.Fatalf("Dispatch: %v", err)
		}
	}
}

// BenchmarkDispatch_Replay mede o caminho de replay (devolução do registado, zero efeito).
func BenchmarkDispatch_Replay(b *testing.B) {
	h := newBenchHarness(b)
	normal, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		b.Fatalf("NewDispatcher: %v", err)
	}
	ctx := context.Background()
	if _, err := normal.Dispatch(ctx, baseActivity()); err != nil {
		b.Fatalf("seed: %v", err)
	}
	replay, err := activity.NewReplayDispatcher(h.ledger)
	if err != nil {
		b.Fatalf("NewReplayDispatcher: %v", err)
	}
	act := baseActivity()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := replay.Dispatch(ctx, act); err != nil {
			b.Fatalf("Dispatch replay: %v", err)
		}
	}
}
