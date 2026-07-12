package liveness

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/state"
)

// BenchmarkClassify mede o custo da classificação pura (caminho quente do detector de
// zombi, invocado periodicamente por run).
func BenchmarkClassify(b *testing.B) {
	c := NewZombieClassifier()
	ctx := context.Background()
	run := RunLiveness{State: state.WaitingOnHuman, WorkLeaseExpired: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if c.Classify(ctx, run) != WaitingLegitimate {
			b.Fatal("classificacao inesperada")
		}
	}
}

// BenchmarkGateExceeded mede o custo da avaliação do gate de espera.
func BenchmarkGateExceeded(b *testing.B) {
	clk := newClock()
	entered := clk.Now()
	gate, err := NewWaitingGate(time.Hour, WithGateClock(clk))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = gate.Exceeded(entered)
	}
}

// BenchmarkWorkClockObserve mede o custo de registar uma transição no relógio de
// trabalho activo (chamado a cada transição da Machine).
func BenchmarkWorkClockObserve(b *testing.B) {
	clk := newClock()
	wc := NewWorkClock(WithWorkClockClock(clk))
	states := []state.State{state.Running, state.WaitingOnHuman, state.Running, state.Paused}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wc.Observe(states[i%len(states)])
		clk.Advance(time.Millisecond)
	}
}
