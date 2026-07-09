package budget

import (
	"context"
	"testing"
)

// BenchmarkReserve mede o custo do caminho atómico Reserve (sem log durável, o
// caminho rápido in-memory). O orçamento do RM (p95) tem de aguentar este
// overhead por tool call. Reserva-e-liberta para não esgotar o headroom.
func BenchmarkReserve(b *testing.B) {
	ctx := context.Background()
	bud, err := New("bench", Amount{Tokens: 1 << 62, CostMicroUSD: 1 << 62})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	amount := Amount{Tokens: 1, CostMicroUSD: 1}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := bud.Reserve(ctx, "bench", amount)
		if err != nil {
			b.Fatalf("Reserve: %v", err)
		}
		if err := bud.Release(ctx, r); err != nil {
			b.Fatalf("Release: %v", err)
		}
	}
}

// BenchmarkReserveParallel mede o Reserve sob contenção (várias goroutines no
// mesmo nó) — o cenário real de admission control concorrente.
func BenchmarkReserveParallel(b *testing.B) {
	ctx := context.Background()
	bud, _ := New("bench", Amount{Tokens: 1 << 62, CostMicroUSD: 1 << 62})
	amount := Amount{Tokens: 1, CostMicroUSD: 1}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r, err := bud.Reserve(ctx, "bench", amount)
			if err != nil {
				b.Fatalf("Reserve: %v", err)
			}
			_ = bud.Release(ctx, r)
		}
	})
}

// BenchmarkReserveHierarchy mede o Reserve numa cadeia profunda (débito em todos
// os ancestrais) — o custo cresce com a profundidade da árvore.
func BenchmarkReserveHierarchy(b *testing.B) {
	ctx := context.Background()
	bud, _ := New("root", Amount{Tokens: 1 << 62, CostMicroUSD: 1 << 62})
	const depth = 8
	parent := "root"
	for i := 0; i < depth; i++ {
		child := parent + "-c"
		if err := bud.AddNode(child, parent, Amount{Tokens: 1 << 62, CostMicroUSD: 1 << 62}); err != nil {
			b.Fatalf("AddNode: %v", err)
		}
		parent = child
	}
	amount := Amount{Tokens: 1, CostMicroUSD: 1}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := bud.Reserve(ctx, parent, amount)
		if err != nil {
			b.Fatalf("Reserve: %v", err)
		}
		_ = bud.Release(ctx, r)
	}
}
