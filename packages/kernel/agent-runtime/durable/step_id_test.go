package durable

import (
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// TestStepSequencerFormat verifica o formato default e as sobreposições.
func TestStepSequencerFormat(t *testing.T) {
	t.Parallel()
	s := NewStepSequencer()
	if got := s.StepID("run", 1); got != "step-000001" {
		t.Fatalf("StepID(1) = %q, quero step-000001", got)
	}
	if got := s.StepID("run", 42); got != "step-000042" {
		t.Fatalf("StepID(42) = %q, quero step-000042", got)
	}
	custom := NewStepSequencer(WithPrefix("s"), WithWidth(3))
	if got := custom.StepID("run", 7); got != "s007" {
		t.Fatalf("custom StepID(7) = %q, quero s007", got)
	}
	// width <= 0 cai no default.
	if NewStepSequencer(WithWidth(0)).StepID("r", 1) != "step-000001" {
		t.Fatalf("WithWidth(0) devia cair no default")
	}
}

// TestStepSequencerImplementsHook garante a ligação ADITIVA ao hook de AOS-013:
// o StepSequencer é injectável via agentruntime.WithStepIdentity e produz o mesmo
// formato que o default sequencial (não quebra os testes de AOS-013).
func TestStepSequencerImplementsHook(t *testing.T) {
	t.Parallel()
	var hook agentruntime.StepIdentity = NewStepSequencer()
	// Paridade com o default sequencial de AOS-013 ("step-000001"…).
	for turn := 1; turn <= 5; turn++ {
		if got := hook.StepID("run", turn); got == "" {
			t.Fatalf("hook.StepID(%d) vazio", turn)
		}
	}
	if hook.StepID("run", 1) != "step-000001" {
		t.Fatalf("paridade de formato com AOS-013 quebrada")
	}
}

// TestStepIDStableAcrossReplay é o critério "step_id estável por passo lógico
// entre execução e retry/replay": a derivação é pura na posição (turno), pelo que
// re-derivar o mesmo passo lógico — em execução, retry ou replay — dá o MESMO
// step_id e a MESMA idempotency key. Nunca reatribuído.
func TestStepIDStableAcrossReplay(t *testing.T) {
	t.Parallel()
	s := NewStepSequencer()
	const runID = "run-replay"

	// "Execução": deriva os step_ids/keys dos turnos 1..3.
	type step struct{ id, key string }
	exec := make([]step, 0, 3)
	for turn := 1; turn <= 3; turn++ {
		id := s.StepID(runID, turn)
		key, err := s.Key(runID, turn)
		if err != nil {
			t.Fatal(err)
		}
		exec = append(exec, step{id, key})
	}

	// "Replay/retry": re-deriva com um sequenciador NOVO (estado zero) — simula um
	// worker reiniciado a reprocessar o log. Tem de coincidir passo-a-passo.
	s2 := NewStepSequencer()
	for turn := 1; turn <= 3; turn++ {
		id := s2.StepID(runID, turn)
		key, _ := s2.Key(runID, turn)
		if id != exec[turn-1].id {
			t.Fatalf("turno %d: step_id reatribuído no replay: %q != %q", turn, id, exec[turn-1].id)
		}
		if key != exec[turn-1].key {
			t.Fatalf("turno %d: key instável no replay: %q != %q", turn, key, exec[turn-1].key)
		}
	}

	// Sub-passos (activities dentro do turno) também estáveis e distintos.
	subA := s.SubStepID(runID, 2, 1)
	subB := s.SubStepID(runID, 2, 2)
	if subA == subB {
		t.Fatalf("sub-passos não distintos: %q", subA)
	}
	if subA != s2.SubStepID(runID, 2, 1) {
		t.Fatalf("sub-passo instável no replay")
	}
	if k, err := s.SubKey(runID, 2, 1); err != nil || k != runID+":"+subA {
		t.Fatalf("SubKey inconsistente: %q err=%v", k, err)
	}
}

// TestStepIDMonotonic verifica a ordenação lexicográfica monotónica (graças ao
// zero-padding) para turnos crescentes.
func TestStepIDMonotonic(t *testing.T) {
	t.Parallel()
	s := NewStepSequencer()
	prev := ""
	for turn := 1; turn <= 20; turn++ {
		id := s.StepID("r", turn)
		if prev != "" && id <= prev {
			t.Fatalf("não-monotónico: turno %d dá %q <= %q", turn, id, prev)
		}
		prev = id
	}
}
