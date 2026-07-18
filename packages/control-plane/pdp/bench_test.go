package pdp

import (
	"context"
	"sort"
	"testing"
	"time"

	rm "github.com/aos-ref/kernel/reference-monitor"
)

// BenchmarkDecide mede a latência da avaliação de política em memória (PDP puro,
// sem RM nem I/O). É a medição autoritativa do overhead do PDP (ns/op).
func BenchmarkDecide(b *testing.B) {
	p := mustOpen(b)
	in := httpPost()
	in.Context.Sensitivity = "confidential" // exercita também a derivação de obligations
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d, err := p.Decide(ctx, in)
		if err != nil || d.Effect != Permit {
			b.Fatalf("Decide: effect=%q err=%v", d.Effect, err)
		}
	}
}

// buildRMDiscard constrói um RM com a cadeia SANCIONADA (o PDP real no hook de
// política, via [DefaultHooksWithPDP]) e sink de descarte (mede o caminho de compute
// RM+PDP sem I/O do Event Store). O [permitCall] usado nos benchmarks tem
// sensitivity=confidential + Input JSON, pelo que o caminho medido INCLUI o
// enforcement da obrigação redact_pii ANTES do dispatch (AOS-087, AC5).
func buildRMDiscard(tb testing.TB) *rm.Monitor {
	tb.Helper()
	p := mustOpen(tb)
	m := rm.New(rm.WithHooks(DefaultHooksWithPDP(p)...))
	if err := m.Register("tool.http", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		tb.Fatalf("Register: %v", err)
	}
	return m
}

// BenchmarkMediate_RM_PDP mede o caminho combinado RM+PDP (mediação completa com o
// PDP real no hook de política E o enforcement de obrigações do PEP).
func BenchmarkMediate_RM_PDP(b *testing.B) {
	m := buildRMDiscard(b)
	call := permitCall()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d, err := m.Mediate(ctx, call)
		if err != nil || d.Effect != rm.EffectPermit {
			b.Fatalf("Mediate: effect=%q err=%v", d.Effect, err)
		}
	}
}

// p95 devolve o percentil 95 de uma amostra de durações.
func p95(samples []time.Duration) time.Duration {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[int(float64(len(samples))*0.95)]
}

// Parâmetros do tripwire p95. A medição é POR LOTE (batch): cada amostra
// cronometra `batchSize` operações e divide pela contagem, obtendo uma latência
// média-por-op com resolução muito acima do tick do relógio (o relógio do
// Windows tem granularidade ~100ns–1ms; medir uma única op por iteração
// colapsava para 0s e tornava a asseveração <15ms trivial/de baixo sinal). O
// percentil é calculado sobre os lotes, preservando sensibilidade a caudas.
const (
	p95Batches   = 200 // nº de amostras (lotes) sobre as quais se tira o p95
	p95BatchSize = 200 // ops por lote (amortiza a granularidade do relógio)
)

// batchedPerOp cronometra p95Batches lotes de p95BatchSize chamadas a op e
// devolve a latência média-por-op de cada lote — amostras com resolução real
// para um p95 significativo.
func batchedPerOp(op func()) []time.Duration {
	for i := 0; i < 500; i++ { // aquecimento
		op()
	}
	samples := make([]time.Duration, p95Batches)
	for b := 0; b < p95Batches; b++ {
		start := time.Now()
		for i := 0; i < p95BatchSize; i++ {
			op()
		}
		samples[b] = time.Since(start) / p95BatchSize
	}
	return samples
}

// TestDecide_P95Overhead é um tripwire do overhead do PDP puro contra o alvo NFR
// (p95 < 15 ms). Mede por lote para ter resolução real (ver [batchedPerOp]); a
// medição autoritativa continua a ser BenchmarkDecide.
func TestDecide_P95Overhead(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	in := httpPost()
	ctx := context.Background()

	got := p95(batchedPerOp(func() { _, _ = p.Decide(ctx, in) }))
	t.Logf("PDP.Decide p95=%v (por-op, %d lotes x %d)", got, p95Batches, p95BatchSize)
	if got >= 15*time.Millisecond {
		t.Fatalf("p95 do PDP=%v excede o alvo de 15 ms", got)
	}
}

// TestMediate_RM_PDP_P95Overhead assevera que o caminho combinado RM+PDP também
// cumpre o alvo p95 < 15 ms (medição por lote, ver [batchedPerOp]).
func TestMediate_RM_PDP_P95Overhead(t *testing.T) {
	t.Parallel()
	m := buildRMDiscard(t)
	call := permitCall()
	ctx := context.Background()

	got := p95(batchedPerOp(func() { _, _ = m.Mediate(ctx, call) }))
	t.Logf("RM+PDP Mediate p95=%v (por-op, %d lotes x %d)", got, p95Batches, p95BatchSize)
	if got >= 15*time.Millisecond {
		t.Fatalf("p95 combinado RM+PDP=%v excede o alvo de 15 ms", got)
	}
}
