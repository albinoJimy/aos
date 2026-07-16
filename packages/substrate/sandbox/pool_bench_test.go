package sandbox

import (
	"context"
	"testing"
)

// benchSnap constrói um snapshot base determinista para os benchmarks (equivalente ao
// newSnap dos testes, mas para *testing.B).
func benchSnap(b *testing.B) *Snapshot {
	b.Helper()
	s, err := NewSnapshot("img-v1", baseImage())
	if err != nil {
		b.Fatalf("NewSnapshot: %v", err)
	}
	return s
}

// BenchmarkPool_WarmReserve mede o custo Go REAL de uma reserva+libertação servida
// pelo caminho quente (warm hit) com reposição síncrona. O timing MODELADO de microVM
// (restore 5–30 ms) é intencionalmente excluído deste caminho — este benchmark isola o
// overhead da máquina de pool (reserva atómica + contabilidade + reposição). É a
// "medição anexada" reprodutível do DoD (finding dod-medicoes): correr com
//
//	go test -run=^$ -bench=BenchmarkPool_WarmReserve -benchmem ./...
func BenchmarkPool_WarmReserve(b *testing.B) {
	s := benchSnap(b)
	p, err := NewPool(s, 16, WithSynchronousReplenish())
	if err != nil {
		b.Fatalf("NewPool: %v", err)
	}
	defer p.Close()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l, err := p.Reserve(ctx)
		if err != nil {
			b.Fatalf("Reserve: %v", err)
		}
		l.Release()
	}
}

// BenchmarkPool_ColdExpand mede o custo de uma reserva que EXPANDE (pool a frio, sem
// pré-aquecimento): cada reserva paga o restore modelado (5–30 ms) no caminho crítico.
// Complementa o warm hit — mostra a diferença entre servir pré-aquecido e restaurar a
// pedido.
func BenchmarkPool_ColdExpand(b *testing.B) {
	s := benchSnap(b)
	p, err := NewPool(s, 0, WithPolicy(PolicyExpand), WithMaxSize(1<<20), WithSynchronousReplenish())
	if err != nil {
		b.Fatalf("NewPool: %v", err)
	}
	defer p.Close()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l, err := p.Reserve(ctx)
		if err != nil {
			b.Fatalf("Reserve: %v", err)
		}
		l.Release()
	}
}

// BenchmarkPool_ColdStartSLI corre uma carga MISTA (warm hits + expansões) sob um
// [ColdStartRecorder] e ANEXA as medições do SLI como métricas do benchmark
// (p95_ms, max_ms, warm_restores) via b.ReportMetric — a "medição anexada" exigida
// pelo DoD, reprodutível e determinista. Não substitui as asserções não-vácuas dos
// testes (p95<125ms, restore∈[5,30]ms); torna os números um artefacto legível.
func BenchmarkPool_ColdStartSLI(b *testing.B) {
	s := benchSnap(b)
	rec := NewColdStartRecorder()
	warm, err := NewPool(s, 8, WithSynchronousReplenish(), WithColdStartRecorder(rec))
	if err != nil {
		b.Fatalf("NewPool warm: %v", err)
	}
	defer warm.Close()
	cold, err := NewPool(s, 0, WithPolicy(PolicyExpand), WithMaxSize(1<<20), WithSynchronousReplenish(), WithColdStartRecorder(rec))
	if err != nil {
		b.Fatalf("NewPool cold: %v", err)
	}
	defer cold.Close()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 1 warm hit + 2 expansões por iteração — mistura estável para a cauda.
		lw, err := warm.Reserve(ctx)
		if err != nil {
			b.Fatalf("warm.Reserve: %v", err)
		}
		lw.Release()
		for j := 0; j < 2; j++ {
			lc, err := cold.Reserve(ctx)
			if err != nil {
				b.Fatalf("cold.Reserve: %v", err)
			}
			lc.Release()
		}
	}
	b.StopTimer()

	agg, ok := rec.SnapshotAgg(warm.key())
	if !ok {
		b.Fatal("sem agregado de SLI")
	}
	b.ReportMetric(float64(agg.P95.Microseconds())/1000, "p95_ms")
	b.ReportMetric(float64(agg.Max.Microseconds())/1000, "max_ms")
	b.ReportMetric(float64(agg.WarmRestores), "warm_restores")
}
