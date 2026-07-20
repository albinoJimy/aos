package controlsurface_test

import (
	"context"
	"testing"
	"time"

	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// TestConsistency_TwoChannelsSeeSameState — AC4: dois canais subscritos ao MESMO run
// (dois callbacks de reflexão no mesmo StateProjector) recebem o MESMO estado após uma
// transição durável, da mesma fonte e na mesma ordem. É a consistência transversal
// "o mesmo run mostra o mesmo estado em desktop e em chatbot".
func TestConsistency_TwoChannelsSeeSameState(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	proj, err := controlsurface.NewStateProjector(ctx, st, testRunID)
	if err != nil {
		t.Fatalf("NewStateProjector: %v", err)
	}
	t.Cleanup(proj.Close)

	// Dois "canais": cada um regista um callback que empurra o estado recebido para o
	// seu próprio channel Go (espera DETERMINÍSTICA pela entrega real, sem sleeps).
	desktop := make(chan state.State, 8)
	chatbot := make(chan state.State, 8)
	proj.Observe(func(s state.State) { desktop <- s })
	proj.Observe(func(s state.State) { chatbot <- s })

	// Uma transição durável real (claim ready→running) via a máquina de AOS-017.
	m, err := state.NewMachine(st, testRunID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("Transition running: %v", err)
	}

	got1 := recv(t, desktop)
	got2 := recv(t, chatbot)
	if got1 != state.Running || got2 != state.Running {
		t.Fatalf("canais divergem: desktop=%s chatbot=%s, quero ambos running", got1, got2)
	}

	// Uma segunda transição (running→paused) é vista IGUAL pelos dois canais.
	if err := m.Transition(ctx, state.Paused, state.TransitionEvent{}); err != nil {
		t.Fatalf("Transition paused: %v", err)
	}
	got1 = recv(t, desktop)
	got2 = recv(t, chatbot)
	if got1 != state.Paused || got2 != state.Paused {
		t.Fatalf("2ª transição diverge: desktop=%s chatbot=%s, quero ambos paused", got1, got2)
	}

	// A projecção convergiu para o mesmo To que a máquina (fonte única).
	if proj.Current() != state.Paused || proj.Current() != m.Current() {
		t.Fatalf("Current()=%s, máquina=%s, quero paused", proj.Current(), m.Current())
	}
}

// TestConsistency_TwoProjectorsConverge — AC4 (variante): dois projectores
// INDEPENDENTES sobre o mesmo store (o molde de dois processos/superfícies distintas)
// convergem para o mesmo estado corrente a partir do mesmo log.
func TestConsistency_TwoProjectorsConverge(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	pA, err := controlsurface.NewStateProjector(ctx, st, testRunID)
	if err != nil {
		t.Fatalf("NewStateProjector A: %v", err)
	}
	t.Cleanup(pA.Close)
	pB, err := controlsurface.NewStateProjector(ctx, st, testRunID)
	if err != nil {
		t.Fatalf("NewStateProjector B: %v", err)
	}
	t.Cleanup(pB.Close)

	sawA := make(chan state.State, 8)
	sawB := make(chan state.State, 8)
	pA.Observe(func(s state.State) { sawA <- s })
	pB.Observe(func(s state.State) { sawB <- s })

	m, err := state.NewMachine(st, testRunID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("Transition running: %v", err)
	}
	if a, b := recv(t, sawA), recv(t, sawB); a != state.Running || b != state.Running {
		t.Fatalf("projectores divergem: A=%s B=%s", a, b)
	}
	if pA.Current() != pB.Current() {
		t.Fatalf("Current diverge: A=%s B=%s", pA.Current(), pB.Current())
	}
}

// recv espera por uma entrega real com deadline (determinístico — falha em vez de
// pendurar se a subscrição não entregar).
func recv(t *testing.T, ch <-chan state.State) state.State {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("timeout à espera da reflexão de estado (nenhuma entrega)")
		return ""
	}
}
