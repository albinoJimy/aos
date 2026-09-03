package control_test

import (
	"bytes"
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
	if c, ok := nilAdapter.PendingCorrection(ctx, runID); c != nil || ok {
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

// TestLoopSteer_CorrectionAppliedOncePerRun (AOS-218, achado da semântica activada) — pina a
// semântica UMA-VEZ do live-steer: o loop consulta PendingCorrection em TODA a fronteira de
// fim-de-turno, mas a correcção durável do canal fica pendente até uma resume; o adaptador
// devolve cada correcção DISTINTA uma ÚNICA vez, para não a re-injectar no tail turno-a-turno
// (crescimento ilimitado). Prova: (1) a mesma correcção não se repete; (2) uma NOVA correcção
// aplica-se; (3) após resume (canal limpo) o marcador reinicia e um steer futuro volta a aplicar.
func TestLoopSteer_CorrectionAppliedOncePerRun(t *testing.T) {
	ctx := context.Background()
	const runID = "run-218-once"

	st, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer st.Close()

	a := control.NewHMACAuthenticator()
	a.Register(testEmitter, []byte(testSecret))
	ch := newChannel(t, st, a)

	// O run em running (a pausa graciosa precisa de running→paused).
	m, err := state.NewMachine(st, runID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("ready→running: %v", err)
	}
	gate := control.NewMachineGate(m)
	adapter := control.NewLoopSteer(ch, func(string) control.StateGate { return gate })

	corrA := []byte("prioriza a superficie desktop")

	// (1) Steer "A": injecta UMA vez; a segunda consulta (turno seguinte, sem resume) NÃO repete.
	if err := ch.Steer(ctx, runID, corrA, signed(t, a, runID, control.SignalSteer, corrA)); err != nil {
		t.Fatalf("Steer A: %v", err)
	}
	if got, ok := adapter.PendingCorrection(ctx, runID); !ok || !bytes.Equal(got, corrA) {
		t.Fatalf("1ª consulta: (%q,%v), quero (%q,true)", got, ok, corrA)
	}
	if got, ok := adapter.PendingCorrection(ctx, runID); ok || got != nil {
		t.Fatalf("2ª consulta da MESMA correcção devia ser (nil,false) — semântica uma-vez; deu (%q,%v)", got, ok)
	}

	// (2) NOVA correcção "B" (bytes diferentes): aplica-se uma vez.
	corrB := []byte("aperta o ambito ao ticket")
	if err := ch.Steer(ctx, runID, corrB, signed(t, a, runID, control.SignalSteer, corrB)); err != nil {
		t.Fatalf("Steer B: %v", err)
	}
	if got, ok := adapter.PendingCorrection(ctx, runID); !ok || !bytes.Equal(got, corrB) {
		t.Fatalf("nova correcção B devia aplicar-se: (%q,%v), quero (%q,true)", got, ok, corrB)
	}
	if _, ok := adapter.PendingCorrection(ctx, runID); ok {
		t.Fatal("B não devia repetir na consulta seguinte")
	}

	// (3) RESET no resume: pausa graciosa → resume limpa a correcção do canal; a consulta
	// seguinte (canal vazio) reinicia o marcador do adaptador, e um steer futuro de conteúdo
	// idêntico a um anterior ("A") volta a aplicar-se uma vez.
	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if paused, err := adapter.GracefulPause(ctx, runID); err != nil || !paused {
		t.Fatalf("GracefulPause: (%v,%v), quero (true,nil)", paused, err)
	}
	if _, err := ch.Resume(ctx, runID, signed(t, a, runID, control.SignalResume, nil), gate); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// Canal sem correcção pendente ⇒ (nil,false) e o marcador do adaptador é limpo.
	if got, ok := adapter.PendingCorrection(ctx, runID); ok || got != nil {
		t.Fatalf("após resume o canal está limpo: (%q,%v), quero (nil,false)", got, ok)
	}
	// Re-steer "A" (mesmo conteúdo do passo 1) aplica-se OUTRA VEZ — o reset funcionou.
	if err := ch.Steer(ctx, runID, corrA, signed(t, a, runID, control.SignalSteer, corrA)); err != nil {
		t.Fatalf("Steer A (2): %v", err)
	}
	if got, ok := adapter.PendingCorrection(ctx, runID); !ok || !bytes.Equal(got, corrA) {
		t.Fatalf("após reset, re-steer A devia aplicar-se: (%q,%v), quero (%q,true)", got, ok, corrA)
	}
}
