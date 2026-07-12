package control_test

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// BenchmarkAuthenticate mede o caminho quente da fronteira de segurança (recálculo do
// HMAC + comparação em tempo constante) — corre em CADA sinal antes de qualquer escrita.
func BenchmarkAuthenticate(b *testing.B) {
	ctx := context.Background()
	a := control.NewHMACAuthenticator()
	a.Register(testEmitter, []byte(testSecret))
	payload := []byte("corrige o rumo")
	em, _ := a.Sign("run-bench", control.SignalSteer, payload, testEmitter)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := a.Authenticate(ctx, "run-bench", control.SignalSteer, payload, em); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPauseSteerResumeCycle mede um ciclo durável completo pause→graceful pause→
// steer→resume (autenticação + Append replicado + transições de estado de AOS-017).
func BenchmarkPauseSteerResumeCycle(b *testing.B) {
	ctx := context.Background()
	a := control.NewHMACAuthenticator()
	a.Register(testEmitter, []byte(testSecret))
	correction := []byte("segue o plano B")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		st, err := eventstore.New()
		if err != nil {
			b.Fatal(err)
		}
		runID := "bench-run-" + itoa(i)
		ch, _ := control.NewChannel(st, a, control.WithClock(fixedClock()))
		m, _ := state.NewMachine(st, runID)
		_ = m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)})
		gate := control.NewMachineGate(m)
		pause, _ := a.Sign(runID, control.SignalPause, nil, testEmitter)
		steer, _ := a.Sign(runID, control.SignalSteer, correction, testEmitter)
		resume, _ := a.Sign(runID, control.SignalResume, nil, testEmitter)
		b.StartTimer()

		_ = ch.Pause(ctx, runID, pause)
		_, _ = ch.GracefulPause(ctx, runID, gate)
		_ = ch.Steer(ctx, runID, correction, steer)
		_, _ = ch.Resume(ctx, runID, resume, gate)

		b.StopTimer()
		_ = st.Close()
		b.StartTimer()
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
