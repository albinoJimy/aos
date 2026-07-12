package replay

import (
	"context"
	"testing"
)

// BenchmarkReplayFull mede o custo de reconstruir uma trajectória completa a
// partir do Event Store (leitura + re-materialização + comparação de hash).
func BenchmarkReplayFull(b *testing.B) {
	or := runOriginalB(b, "run_bench_full")
	e, err := NewEngine(or.store)
	if err != nil {
		b.Fatalf("NewEngine: %v", err)
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := e.Replay(ctx, or.goal.RunID, Options{Spec: or.spec})
		if err != nil {
			b.Fatalf("Replay: %v", err)
		}
		if res.Fidelity != 1.0 {
			b.Fatalf("fidelidade = %v", res.Fidelity)
		}
	}
}

// BenchmarkReplayResume mede o custo do resume-from-step (dobra os turnos
// anteriores + verifica a partir do ponto de retoma).
func BenchmarkReplayResume(b *testing.B) {
	or := runOriginalB(b, "run_bench_resume")
	e, _ := NewEngine(or.store)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Replay(ctx, or.goal.RunID, Options{Spec: or.spec, FromStepID: "step-000002"}); err != nil {
			b.Fatalf("Replay resume: %v", err)
		}
	}
}
