package durable

import (
	"context"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

func BenchmarkIdempotencyKey(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := IdempotencyKey("run-benchmark", "step-000123"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStepID(b *testing.B) {
	s := NewStepSequencer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.StepID("run", i)
	}
}

// BenchmarkApplyAlreadyApplied mede o caminho quente de dedup (already-applied),
// que precede qualquer efeito — deve ser barato (só lookup em mapa sob lock).
func BenchmarkApplyAlreadyApplied(b *testing.B) {
	ctx := context.Background()
	st, err := eventstore.New()
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ledger, _ := NewStepLedger(st)
	key, _ := IdempotencyKey("run-b", "step-000001")
	if _, _, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("x")}, nil
	}); err != nil {
		b.Fatal(err)
	}
	effect := func(context.Context) (Result, error) { return Result{}, nil }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, applied, _ := ledger.Apply(ctx, key, effect); applied {
			b.Fatal("não devia aplicar")
		}
	}
}
