package control_test

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// TestLoopSteer_AdaptsSteerChannel prova que o adaptador [control.LoopSteer] liga o
// SteerChannel REAL à porta do loop (AOS-158): uma pausa pendente, consultada via o
// adaptador na fronteira de fim-de-turno, MATERIALIZA a transição durável running→paused
// através do StateGate do run.
func TestLoopSteer_AdaptsSteerChannel(t *testing.T) {
	ctx := context.Background()
	const runID = "run-158"

	st, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer st.Close()

	a := control.NewHMACAuthenticator()
	a.Register(testEmitter, []byte(testSecret))
	ch := newChannel(t, st, a)

	// O run tem de estar RUNNING para a pausa graciosa (running→paused).
	m, err := state.NewMachine(st, runID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("ready→running: %v", err)
	}
	gate := control.NewMachineGate(m)
	adapter := control.NewLoopSteer(ch, func(string) control.StateGate { return gate })

	// nil-guards fail-safe: um adaptador sem canal/gates não pausa nem corrige.
	nilAdapter := control.NewLoopSteer(nil, nil)
	if p, err := nilAdapter.GracefulPause(ctx, runID); p || err != nil {
		t.Fatalf("nil adapter GracefulPause=(%v,%v), quero (false,nil)", p, err)
	}
	if c, ok := nilAdapter.PendingCorrection(runID); c != nil || ok {
		t.Fatalf("nil adapter PendingCorrection=(%v,%v), quero (nil,false)", c, ok)
	}

	// Sem pausa pendente: o loop continua.
	if p, err := adapter.GracefulPause(ctx, runID); err != nil || p {
		t.Fatalf("sem pausa pendente: (%v,%v), quero (false,nil)", p, err)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado=%q, quero running (sem pausa)", m.Current())
	}

	// Pedir pausa out-of-band; o adaptador materializa-a na fronteira.
	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	paused, err := adapter.GracefulPause(ctx, runID)
	if err != nil {
		t.Fatalf("GracefulPause: %v", err)
	}
	if !paused {
		t.Fatal("pausa pendente devia materializar (paused=true)")
	}
	if m.Current() != state.Paused {
		t.Fatalf("estado=%q, quero paused (pausa durável materializada)", m.Current())
	}
}
