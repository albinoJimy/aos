package controlsurface_test

import (
	"context"
	"testing"

	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
	"github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// TestGracefulPause_InterruptFromChannelPausesAtTurnEnd — AC2: um interrupt de um
// canal provoca a PAUSA GRACIOSA no fim do turno via o mecanismo REAL de AOS-023 (a
// superfície NÃO reimplementa a transição — delega).
//
// Sequência: máquina em running → interrupt via a superfície → o sinal é ACEITE já
// (PendingPause=true) mas a máquina CONTINUA running (nada transitou a meio do turno)
// → no fim do turno o loop chama GracefulPause do canal REAL → running→paused.
func TestGracefulPause_InterruptFromChannelPausesAtTurnEnd(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	auth := authWith(t)
	ch := newChannel(t, st, auth, nil)
	surface := newSurface(t, ch, nil)
	m, binding := runningMachine(t, st, testRunID)

	// Interrupt de um canal (desktop). Assinatura autêntica sobre o sinal "pause".
	sig := sign(t, auth, testRunID, control.SignalPause, nil, testEmitter)
	msg := controlsurface.NewInterrupt(testRunID, controlsurface.ChannelDesktop, testEmitter, sig)

	ack, err := surface.Dispatch(ctx, msg, binding)
	if err != nil {
		t.Fatalf("Dispatch(interrupt): %v", err)
	}

	// AC2: o sinal foi ACEITE (garantia 1) mas a transição é DIFERIDA — a máquina
	// permanece running (a superfície não transitou nada a meio do turno).
	if !ack.PendingPause {
		t.Fatalf("ack.PendingPause=false, quero true (o interrupt tem de ser aceite já)")
	}
	if got := m.Current(); got != state.Running {
		t.Fatalf("estado após interrupt=%s, quero running (a pausa é graciosa, não imediata)", got)
	}
	if ack.State != state.Running {
		t.Fatalf("ack.State=%s, quero running (reflecte o estado durável, ainda não pausado)", ack.State)
	}

	// FIM DO TURNO: o loop do runtime chama o mecanismo REAL de AOS-023. A superfície
	// não o reimplementa — delega no SteerChannel.GracefulPause + MachineGate.
	paused, err := ch.GracefulPause(ctx, testRunID, binding.Gate)
	if err != nil {
		t.Fatalf("GracefulPause: %v", err)
	}
	if !paused {
		t.Fatalf("GracefulPause=false, quero true (havia pausa pendente)")
	}
	if got := m.Current(); got != state.Paused {
		t.Fatalf("estado após GracefulPause=%s, quero paused", got)
	}

	// A transição durável foi materializada pela máquina REAL (evento append-only).
	final, err := m.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if final != state.Paused {
		t.Fatalf("estado reconstruído do log=%s, quero paused (facto durável)", final)
	}
}

// TestGracefulPause_ResumeMaterializesViaMachine — AC2 (retoma): resume via a
// superfície materializa paused→running via o MachineGate real de AOS-023.
func TestGracefulPause_ResumeMaterializesViaMachine(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	auth := authWith(t)
	ch := newChannel(t, st, auth, nil)
	surface := newSurface(t, ch, nil)
	m, binding := runningMachine(t, st, testRunID)

	// Pausa o run (interrupt + graceful pause) para o pôr em paused.
	pauseSig := sign(t, auth, testRunID, control.SignalPause, nil, testEmitter)
	if _, err := surface.Dispatch(ctx, controlsurface.NewInterrupt(testRunID, controlsurface.ChannelDesktop, testEmitter, pauseSig), binding); err != nil {
		t.Fatalf("Dispatch(interrupt): %v", err)
	}
	if _, err := ch.GracefulPause(ctx, testRunID, binding.Gate); err != nil {
		t.Fatalf("GracefulPause: %v", err)
	}
	if m.Current() != state.Paused {
		t.Fatalf("pré-condição: máquina não está paused (%s)", m.Current())
	}

	// Resume via a superfície (de um canal diferente — chatbot). Delega em Resume+Gate.
	resumeSig := sign(t, auth, testRunID, control.SignalResume, nil, testEmitter)
	ack, err := surface.Dispatch(ctx, controlsurface.NewResume(testRunID, controlsurface.ChannelChatbot, testEmitter, resumeSig), binding)
	if err != nil {
		t.Fatalf("Dispatch(resume): %v", err)
	}
	if ack.State != state.Running {
		t.Fatalf("ack.State após resume=%s, quero running", ack.State)
	}
	if got := m.Current(); got != state.Running {
		t.Fatalf("máquina após resume=%s, quero running", got)
	}
}

// TestGracefulPause_ResumeWithoutBindingRejected — a superfície recusa um resume sem
// gate (não há como delegar a transição no runtime). Fail-closed.
func TestGracefulPause_ResumeWithoutBindingRejected(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	auth := authWith(t)
	ch := newChannel(t, st, auth, nil)
	surface := newSurface(t, ch, nil)

	resumeSig := sign(t, auth, testRunID, control.SignalResume, nil, testEmitter)
	msg := controlsurface.NewResume(testRunID, controlsurface.ChannelAPI, testEmitter, resumeSig)
	_, err := surface.Dispatch(ctx, msg, controlsurface.RunBinding{ /* Gate nil */ })
	if err == nil {
		t.Fatalf("Dispatch(resume) sem gate devia falhar")
	}
	if err != controlsurface.ErrNilBinding {
		t.Fatalf("err=%v, quero ErrNilBinding", err)
	}
}
