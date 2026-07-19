package eventstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// AOS-100 — testes específicos da eliminação do single-writer (paralelismo
// por-stream) e da soberania regional (ADR-011).

// --- soberania regional (ADR-011), fail-closed -----------------------------

func TestSovereignty_ReplicaOutOfRegionRejected(t *testing.T) {
	// Uma réplica fora da fronteira do board é rejeitada na construção (fail-closed).
	_, err := New(WithReplicas(3), WithQuorum(2),
		WithRegion("eu"),
		WithReplicaRegions("eu", "eu", "us"), // a última cruza a fronteira
	)
	if !errors.Is(err, ErrSovereigntyViolation) {
		t.Fatalf("err = %v, quero ErrSovereigntyViolation (réplica cross-border rejeitada)", err)
	}
}

func TestSovereignty_EmptyRegionRejected(t *testing.T) {
	// Região ausente/desconhecida ⇒ deny (fail-closed).
	if _, err := New(WithRegion("")); !errors.Is(err, ErrSovereigntyViolation) {
		t.Fatalf("região vazia: err = %v, quero ErrSovereigntyViolation", err)
	}
	if _, err := New(WithRegion("   ")); !errors.Is(err, ErrSovereigntyViolation) {
		t.Fatalf("região só espaços: err = %v, quero ErrSovereigntyViolation", err)
	}
	// Réplica com região vazia sob fronteira activa ⇒ deny.
	_, err := New(WithReplicas(3), WithQuorum(2), WithRegion("eu"),
		WithReplicaRegions("eu", "", "eu"))
	if !errors.Is(err, ErrSovereigntyViolation) {
		t.Fatalf("réplica sem região: err = %v, quero ErrSovereigntyViolation", err)
	}
}

func TestSovereignty_InRegionAccepted(t *testing.T) {
	// Cluster inteiramente dentro da fronteira é aceite; a região é case-insensitive.
	s, err := New(WithReplicas(3), WithQuorum(2), WithRegion("EU"),
		WithReplicaRegions("eu", " eu ", "Eu"))
	if err != nil {
		t.Fatalf("cluster in-region rejeitado: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if s.Region() != "eu" {
		t.Fatalf("região do board = %q, quero \"eu\" (normalizada)", s.Region())
	}
	for _, id := range s.Replicas() {
		if s.ReplicaRegion(id) != "eu" {
			t.Fatalf("réplica %d região = %q, quero \"eu\"", id, s.ReplicaRegion(id))
		}
	}
	// Escreve e lê normalmente dentro da fronteira.
	ctx := context.Background()
	if _, err := s.Append(ctx, "run-A", input("run-A", "s1", "t", `{}`)); err != nil {
		t.Fatalf("append in-region: %v", err)
	}
	if evs, err := s.Read(ctx, "run-A", 1); err != nil || len(evs) != 1 {
		t.Fatalf("read in-region: n=%d err=%v", len(evs), err)
	}
}

func TestSovereignty_BoardDefaultsColocated(t *testing.T) {
	// Com fronteira e sem WithReplicaRegions, todas as réplicas assumem a região do board.
	s, err := New(WithReplicas(3), WithQuorum(2), WithSovereigntyBoard("board:acme", "eu"))
	if err != nil {
		t.Fatalf("board co-localizado rejeitado: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, id := range s.Replicas() {
		if s.ReplicaRegion(id) != "eu" {
			t.Fatalf("réplica %d região = %q, quero \"eu\"", id, s.ReplicaRegion(id))
		}
	}
	// O id do board é retido como rótulo de observabilidade (não participa na fronteira).
	if s.SovereigntyBoard() != "board:acme" {
		t.Fatalf("board = %q, quero \"board:acme\"", s.SovereigntyBoard())
	}
	// Uma fronteira declarada por WithRegion (sem board) não retém board.
	s2, err := New(WithReplicas(1), WithQuorum(1), WithRegion("eu"))
	if err != nil {
		t.Fatalf("WithRegion rejeitado: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if s2.SovereigntyBoard() != "" {
		t.Fatalf("board = %q, quero \"\" (WithRegion não tem board)", s2.SovereigntyBoard())
	}
}

func TestSovereignty_ReplicaRegionsRequireBoundary(t *testing.T) {
	// WithReplicaRegions sem fronteira declarada é configuração inválida.
	if _, err := New(WithReplicas(2), WithQuorum(1), WithReplicaRegions("eu", "eu")); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, quero ErrConfig (regiões sem fronteira)", err)
	}
	// Comprimento diferente do número de réplicas é inválido.
	if _, err := New(WithReplicas(3), WithQuorum(2), WithRegion("eu"), WithReplicaRegions("eu", "eu")); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, quero ErrConfig (len(regiões) != réplicas)", err)
	}
}

func TestSovereignty_FailoverStaysInRegion(t *testing.T) {
	// Sob fronteira, o failover elege sempre uma réplica in-region e não perde dados.
	s, err := New(WithReplicas(3), WithQuorum(2), WithRegion("eu"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	for i := 1; i <= 4; i++ {
		if _, err := s.Append(ctx, "run-A", input("run-A", fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	leader := s.Leader()
	if err := s.Kill(leader); err != nil {
		t.Fatalf("kill: %v", err)
	}
	newLeader := s.Leader()
	if newLeader == -1 || newLeader == leader {
		t.Fatalf("failover falhou: líder = %d", newLeader)
	}
	if s.ReplicaRegion(newLeader) != "eu" {
		t.Fatalf("novo líder fora da região: %q", s.ReplicaRegion(newLeader))
	}
	evs, err := s.Read(ctx, "run-A", 1)
	if err != nil || len(evs) != 4 {
		t.Fatalf("perda pós-failover in-region: n=%d err=%v", len(evs), err)
	}
}

// --- paralelismo por-stream (AC3/AC4): sem contenção de single-writer -------

// TestParallelMultiWriterStreams demonstra múltiplos "workers" a escrever em
// streams DIFERENTES em paralelo, cada stream com seq gapless e ordem própria —
// sem serializador global. Corre sob -race para provar a ausência de data race na
// remoção do mutex global.
func TestParallelMultiWriterStreams(t *testing.T) {
	s := mustNew(t, WithReplicas(3), WithQuorum(2))
	ctx := context.Background()
	const (
		workers       = 16
		eventsPerWkr  = 100
		streamsPerWkr = 1 // cada worker escreve no seu próprio stream
	)
	_ = streamsPerWkr

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			stream := fmt.Sprintf("run-%d", w)
			for i := 1; i <= eventsPerWkr; i++ {
				res, err := s.Append(ctx, stream, input(stream, fmt.Sprintf("s%d", i), "t", `{"k":"v"}`))
				if err != nil {
					t.Errorf("worker %d append %d: %v", w, i, err)
					return
				}
				if res.Seq != uint64(i) {
					t.Errorf("worker %d: seq=%d, quero %d (gapless por stream)", w, res.Seq, i)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// Cada stream tem exactamente eventsPerWkr eventos, em ordem 1..N.
	for w := 0; w < workers; w++ {
		stream := fmt.Sprintf("run-%d", w)
		evs, err := s.Read(ctx, stream, 1)
		if err != nil {
			t.Fatalf("read %s: %v", stream, err)
		}
		if len(evs) != eventsPerWkr {
			t.Fatalf("%s: %d eventos, quero %d", stream, len(evs), eventsPerWkr)
		}
		for i, ev := range evs {
			if ev.Seq != uint64(i+1) {
				t.Fatalf("%s[%d].Seq = %d, quero %d", stream, i, ev.Seq, i+1)
			}
		}
	}
}

// TestParallelReadWhileWriting exercita replay (Read) concorrente com escrita —
// leituras de replay e escritas em paralelo, sem contenção de single-writer.
func TestParallelReadWhileWriting(t *testing.T) {
	s := mustNew(t, WithReplicas(3), WithQuorum(2))
	ctx := context.Background()
	const streams = 8
	const perStream = 200

	var wg sync.WaitGroup
	// Escritores.
	wg.Add(streams)
	for w := 0; w < streams; w++ {
		go func(w int) {
			defer wg.Done()
			stream := fmt.Sprintf("run-%d", w)
			for i := 1; i <= perStream; i++ {
				if _, err := s.Append(ctx, stream, input(stream, fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
					t.Errorf("append %s#%d: %v", stream, i, err)
					return
				}
			}
		}(w)
	}
	// Leitores concorrentes (replay parcial) — a ordem tem de ser sempre gapless.
	var readErr atomic.Int64
	wg.Add(streams)
	for r := 0; r < streams; r++ {
		go func(r int) {
			defer wg.Done()
			stream := fmt.Sprintf("run-%d", r)
			for iter := 0; iter < 50; iter++ {
				evs, err := s.Read(ctx, stream, 1)
				if err != nil && !errors.Is(err, ErrStreamNotFound) {
					readErr.Add(1)
					return
				}
				for i, ev := range evs {
					if ev.Seq != uint64(i+1) {
						readErr.Add(1)
						return
					}
				}
			}
		}(r)
	}
	wg.Wait()
	if readErr.Load() != 0 {
		t.Fatalf("leituras concorrentes viram log inconsistente (%d falhas)", readErr.Load())
	}
}

// TestNodeFailureContinuityParallel: sob escrita multi-stream concorrente, matar
// uma réplica (não-SPOF) não interrompe as escritas nem perde dados confirmados.
func TestNodeFailureContinuityParallel(t *testing.T) {
	s := mustNew(t, WithReplicas(3), WithQuorum(2))
	ctx := context.Background()
	const streams = 8

	var wg sync.WaitGroup
	var failed atomic.Int64
	wg.Add(streams)
	for w := 0; w < streams; w++ {
		go func(w int) {
			defer wg.Done()
			stream := fmt.Sprintf("run-%d", w)
			for i := 1; i <= 150; i++ {
				if _, err := s.Append(ctx, stream, input(stream, fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
					failed.Add(1)
					return
				}
			}
		}(w)
	}
	// A meio da escrita, mata uma réplica não-líder — o quórum 2 sobrevive.
	victim := -1
	for _, id := range s.Replicas() {
		if id != s.Leader() {
			victim = id
			break
		}
	}
	if err := s.Kill(victim); err != nil {
		t.Fatalf("kill: %v", err)
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("escritas interrompidas por falha de nó dentro do quórum: %d streams falharam", failed.Load())
	}
	// Zero perda: cada stream tem os 150 eventos.
	for w := 0; w < streams; w++ {
		stream := fmt.Sprintf("run-%d", w)
		evs, err := s.Read(ctx, stream, 1)
		if err != nil || len(evs) != 150 {
			t.Fatalf("%s: n=%d err=%v, quero 150 (zero perda dentro do quórum)", stream, len(evs), err)
		}
	}
}

// --- benchmark: paralelismo por-stream vs. baseline serial -----------------

// BenchmarkAppendParallelStreams mede o throughput de escrita paralela a streams
// distintos (o ganho da eliminação do single-writer). Comparar com BenchmarkAppend
// (serial, um stream) para observar o paralelismo por-stream.
func BenchmarkAppendParallelStreams(b *testing.B) {
	s, err := New(WithReplicas(3), WithQuorum(2))
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	var ctr atomic.Uint64
	b.RunParallel(func(pb *testing.PB) {
		// Cada goroutine escreve no seu próprio stream: sem contenção de single-writer.
		stream := fmt.Sprintf("run-%d", ctr.Add(1))
		i := 0
		for pb.Next() {
			i++
			if _, err := s.Append(ctx, stream, input(stream, fmt.Sprintf("s%d", i), "t", `{"k":"v"}`)); err != nil {
				b.Fatalf("append: %v", err)
			}
		}
	})
}
