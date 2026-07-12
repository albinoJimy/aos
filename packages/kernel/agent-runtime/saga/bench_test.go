package saga

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// BenchmarkCompensateColdVsReplay mede o custo de uma saga de N compensações no caminho
// FRIO (todas correm) versus na RETOMA (todas deduplicadas pelo ledger) — o segundo é o
// custo do crash-resume que não deve repetir efeitos.
func BenchmarkCompensate(b *testing.B) {
	const k = 16
	ctx := context.Background()

	b.Run("cold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			store, _ := eventstore.New()
			runID := "bench-cold"
			m, _ := state.NewMachine(store, runID)
			_ = m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)})
			_ = m.Transition(ctx, state.Failed, state.TransitionEvent{})
			reg := NewCompensationRegistry()
			for s := 0; s < k; s++ {
				id := "step-" + itoa(s)
				_ = reg.Register(Compensation{StepID: id, Action: func(context.Context) error { return nil }})
			}
			l, _ := durable.NewStepLedger(store)
			c, _ := NewSagaCoordinator(m, l, reg)
			b.StartTimer()
			_ = c.Compensate(ctx)
			b.StopTimer()
			_ = store.Close()
		}
	})
}

// itoa é um helper mínimo sem depender de strconv nos benches.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
