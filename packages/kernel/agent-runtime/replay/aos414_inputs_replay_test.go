package replay

// AOS-414 — um run que consumiu payloads do plano tem de replayar byte-idêntico. O tail semeado
// inclui-os, na mesma ordem e com a mesma construção do loop; sem eles o run divergia no turno 1
// e a fidelidade dava zero — um replay que "passa" sobre outro prompt não prova nada.

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// aos414Original corre UM turno final com um payload do plano no tail.
func aos414Original(t *testing.T, runID string) originalRun {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	capturer, err := NewCapturer(store, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	model := agentruntime.ModelClientFunc(func(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		return agentruntime.ModelResponse{Text: "concluído", Final: true, Usage: agentruntime.Usage{InputTokens: 3, OutputTokens: 1}}, nil
	})
	rt := agentruntime.New(model, rm, agentruntime.NewTurnRecorder(store), agentruntime.WithCapturer(capturer))

	goal := sampleGoal(runID)
	goal.Inputs = []agentruntime.PlanInput{{
		From: "read_notes", Output: "notes_document", Digest: "sha256:abc", Content: []byte("o documento lido"),
	}}
	if _, err := rt.Run(context.Background(), goal); err != nil {
		t.Fatalf("Run original: %v", err)
	}
	spec := specFromGoal(goal)
	spec.Inputs = goal.Inputs
	return originalRun{store: store, goal: goal, spec: spec}
}

func TestAOS414_ReplayComPayloadsEFiel(t *testing.T) {
	or := aos414Original(t, "run_replay_414")
	res, err := mustEngine(t, or).Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Divergence != nil || res.Fidelity != 1.0 {
		t.Fatalf("um run com payloads tinha de replayar fiel: fidelidade=%v divergencia=%+v", res.Fidelity, res.Divergence)
	}
}

// E o contrário, que é o que prova que o teste acima não passa por acaso: sem os payloads na
// spec, o prompt re-materializado é outro e o replay DIVERGE.
func TestAOS414_ReplaySemPayloadsDiverge(t *testing.T) {
	or := aos414Original(t, "run_replay_414_sem")
	spec := or.spec
	spec.Inputs = nil
	res, err := mustEngine(t, or).Replay(context.Background(), or.goal.RunID, Options{Spec: spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Divergence == nil {
		t.Fatal("sem os payloads o replay tinha de divergir — o prompt não é o mesmo")
	}
}
