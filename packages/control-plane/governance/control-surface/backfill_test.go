package controlsurface_test

import (
	"context"
	"testing"
	"time"

	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// TestStateProjector_BackfillColdStart é o AC central de AOS-150: um cliente que liga
// (constrói o projector) a um run que JÁ transitou — sem projector anterior a
// observar — vê o estado corrente A FRIO, não [state.Ready]. Prova o backfill do
// backlog (Read + fold) sob watermark.
func TestStateProjector_BackfillColdStart(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	defer st.Close()

	// Conduz o run ready → running → paused ANTES de qualquer projector existir. As
	// transições ficam só no log; nenhum read-model as observou ao vivo.
	m, err := state.NewMachine(st, testRunID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("ready→running: %v", err)
	}
	if err := m.Transition(ctx, state.Paused, state.TransitionEvent{}); err != nil {
		t.Fatalf("running→paused: %v", err)
	}

	// Só AGORA liga o cliente. Sem backfill veria "ready"; com backfill vê "paused".
	proj, err := controlsurface.NewStateProjector(ctx, st, testRunID)
	if err != nil {
		t.Fatalf("NewStateProjector: %v", err)
	}
	defer proj.Close()

	if got := proj.Current(); got != state.Paused {
		t.Fatalf("cold-start: Current()=%q, quero %q (backfill não dobrou o backlog)", got, state.Paused)
	}
	if got := proj.Current(); got != m.Current() {
		t.Fatalf("cold-start: projector %q != máquina %q", got, m.Current())
	}
	// O cursor de seq está exposto e é > 0 (duas transições dobradas).
	if wm := proj.Watermark(); wm == 0 {
		t.Fatalf("watermark=0 após backfill de 2 transições (cursor não exposto)")
	}
}

// TestStateProjector_BackfillThenLive_NoLossNoDup prova que, após o backfill a frio,
// as transições VIVAS continuam a ser reflectidas EXACTAMENTE uma vez (o dedup por
// seq da sobreposição não engole eventos genuinamente novos), e que a watermark
// avança monotonicamente — a base do reconnect sem perder nem duplicar.
func TestStateProjector_BackfillThenLive_NoLossNoDup(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	defer st.Close()

	m, err := state.NewMachine(st, testRunID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	// Backlog: ready → running.
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("ready→running: %v", err)
	}

	proj, err := controlsurface.NewStateProjector(ctx, st, testRunID)
	if err != nil {
		t.Fatalf("NewStateProjector: %v", err)
	}
	defer proj.Close()
	if proj.Current() != state.Running {
		t.Fatalf("backfill: Current()=%q, quero running", proj.Current())
	}
	wmAfterBackfill := proj.Watermark()

	// Observa as transições VIVAS a partir daqui.
	live := make(chan state.State, 8)
	cancel := proj.Observe(func(s state.State) { live <- s })
	defer cancel()

	// Transição viva: running → paused. Deve chegar UMA vez (não a de backfill de novo).
	if err := m.Transition(ctx, state.Paused, state.TransitionEvent{}); err != nil {
		t.Fatalf("running→paused: %v", err)
	}
	select {
	case got := <-live:
		if got != state.Paused {
			t.Fatalf("transição viva reflectida como %q, quero paused", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("transição viva não reflectida (backfill terá engolido o vivo?)")
	}
	// Nenhuma reflexão espúria da transição já dobrada no backfill (dedup): o canal
	// não deve ter recebido um segundo evento imediato.
	select {
	case extra := <-live:
		t.Fatalf("reflexão duplicada/espúria após o vivo: %q", extra)
	case <-time.After(150 * time.Millisecond):
		// ok — sem duplicados.
	}
	if proj.Current() != state.Paused {
		t.Fatalf("Current()=%q após o vivo, quero paused", proj.Current())
	}
	if proj.Watermark() <= wmAfterBackfill {
		t.Fatalf("watermark não avançou com o vivo (%d <= %d)", proj.Watermark(), wmAfterBackfill)
	}
}

// TestStateProjector_ReconnectConsistent prova que um SEGUNDO projector (reconnect)
// construído sobre o mesmo run vê o MESMO estado a frio — dois clientes independentes
// convergem, a base da consistência AC4 através de reconexões.
func TestStateProjector_ReconnectConsistent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	defer st.Close()

	m, err := state.NewMachine(st, testRunID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("ready→running: %v", err)
	}

	pA, err := controlsurface.NewStateProjector(ctx, st, testRunID)
	if err != nil {
		t.Fatalf("projector A: %v", err)
	}
	defer pA.Close()
	// "Reconnect": um segundo projector, construído mais tarde, backfilla do mesmo log.
	pB, err := controlsurface.NewStateProjector(ctx, st, testRunID)
	if err != nil {
		t.Fatalf("projector B (reconnect): %v", err)
	}
	defer pB.Close()

	if pA.Current() != pB.Current() || pA.Current() != state.Running {
		t.Fatalf("clientes divergem a frio: A=%q B=%q", pA.Current(), pB.Current())
	}
	if pA.Watermark() != pB.Watermark() {
		t.Fatalf("watermarks divergem: A=%d B=%d", pA.Watermark(), pB.Watermark())
	}
}
