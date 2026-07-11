package contract

import (
	"errors"
	"testing"
)

func TestTransitions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		from   State
		to     State
		wantOK bool
	}{
		{"ready->running", StateReady, StateRunning, true},
		{"running->complete", StateRunning, StateComplete, true},
		{"running->failed", StateRunning, StateFailed, true},
		// inválidas
		{"ready->complete (salta running)", StateReady, StateComplete, false},
		{"ready->failed (salta running)", StateReady, StateFailed, false},
		{"running->ready (retrocede)", StateRunning, StateReady, false},
		{"complete->running (terminal)", StateComplete, StateRunning, false},
		{"failed->running (terminal)", StateFailed, StateRunning, false},
		{"complete->failed (terminal)", StateComplete, StateFailed, false},
		{"desconhecido->running", State("bogus"), StateRunning, false},
		{"ready->desconhecido", StateReady, State("bogus"), false},
		{"self-loop ready", StateReady, StateReady, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := CanTransition(c.from, c.to); got != c.wantOK {
				t.Fatalf("CanTransition(%s,%s)=%v, quero %v", c.from, c.to, got, c.wantOK)
			}
			got, err := Transition(c.from, c.to)
			if c.wantOK {
				if err != nil {
					t.Fatalf("Transition(%s,%s) erro inesperado: %v", c.from, c.to, err)
				}
				if got != c.to {
					t.Fatalf("Transition devolveu %s, quero %s", got, c.to)
				}
				return
			}
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("Transition(%s,%s) quero ErrInvalidTransition, obtive %v", c.from, c.to, err)
			}
			// Numa transição inválida o estado de origem fica inalterado.
			if got != c.from {
				t.Fatalf("transição inválida mudou o estado: %s (quero %s)", got, c.from)
			}
		})
	}
}

func TestStateKnownTerminal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s        State
		known    bool
		terminal bool
	}{
		{StateReady, true, false},
		{StateRunning, true, false},
		{StateComplete, true, true},
		{StateFailed, true, true},
		{State("bogus"), false, false},
	}
	for _, c := range cases {
		if c.s.Known() != c.known {
			t.Errorf("%s.Known()=%v, quero %v", c.s, c.s.Known(), c.known)
		}
		if c.s.Terminal() != c.terminal {
			t.Errorf("%s.Terminal()=%v, quero %v", c.s, c.s.Terminal(), c.terminal)
		}
	}
}

// TestStepIDsDistinct garante a invariante que sustenta a persistência de estado
// como eventos: cada fase de uma tarefa usa um step_id distinto (e distinto do
// step de despacho do RM), para que a idempotency_key run_id:step_id seja única
// no stream e nenhum evento seja deduplicado/perdido pelo Event Store.
func TestStepIDsDistinct(t *testing.T) {
	t.Parallel()
	const task = MinimalTaskID
	steps := []string{
		StepRunCreated(),
		StepReady(task),
		StepRunning(task),
		StepDispatch(task),
		StepComplete(task),
		StepFailed(task),
	}
	seen := make(map[string]bool, len(steps))
	for _, s := range steps {
		if s == "" {
			t.Fatal("step_id vazio")
		}
		if seen[s] {
			t.Fatalf("step_id duplicado: %q", s)
		}
		seen[s] = true
	}
}

func TestNewMinimalGraph(t *testing.T) {
	t.Parallel()
	goal := Goal{
		Objective: "objetivo de brinquedo",
		Task:      TaskSpec{ToolID: "tool:echo", Capability: "cap:echo", Input: []byte("oi")},
	}
	g := NewMinimalGraph("run-1", goal)
	if g.RunID != "run-1" {
		t.Fatalf("RunID=%q", g.RunID)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("grafo mínimo tem de ter 1 nó, tem %d", len(g.Nodes))
	}
	n := g.Nodes[0]
	if n.TaskID != MinimalTaskID {
		t.Fatalf("TaskID=%q, quero %q", n.TaskID, MinimalTaskID)
	}
	if n.State != StateReady {
		t.Fatalf("estado inicial=%s, quero ready", n.State)
	}
	if len(n.Deps) != 0 {
		t.Fatalf("grafo trivial não deve ter dependências, tem %d", len(n.Deps))
	}
	if n.Spec.ToolID != "tool:echo" {
		t.Fatalf("spec não propagada do goal: %+v", n.Spec)
	}
}
