package replay

// AOS-506 — um run que levou o aviso de nova tentativa reproduz-se byte a byte. A semente do
// replay leva o aviso no mesmo sítio e com a mesma construção do loop; sem ele na spec o prompt
// do turno 1 é outro e o replay diverge — e com ele na spec de um run que não o levou, também.

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// aos506Original corre UM turno final de um run com entradas e, se pedido, com o aviso.
func aos506Original(t *testing.T, runID string, aviso agentruntime.RetryNotice) originalRun {
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
	goal.RetryNotice = aviso
	if _, err := rt.Run(context.Background(), goal); err != nil {
		t.Fatalf("Run original: %v", err)
	}
	spec := specFromGoal(goal)
	spec.Inputs = goal.Inputs
	spec.RetryNotice = goal.RetryNotice
	return originalRun{store: store, goal: goal, spec: spec}
}

func TestAOS506_ReplayDeUmaTentativaComAvisoEFiel(t *testing.T) {
	or := aos506Original(t, "plano~read_notes~2", agentruntime.RetryNoticeNoFunctionCall)
	res, err := mustEngine(t, or).Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Divergence != nil || res.Fidelity != 1.0 {
		t.Fatalf("uma tentativa com aviso tinha de reproduzir-se fiel: fidelidade=%v divergencia=%+v", res.Fidelity, res.Divergence)
	}
}

// O contrário, nos dois sentidos — é o que prova que o teste acima não passa por acaso.
func TestAOS506_ReplayComASementeErradaDiverge(t *testing.T) {
	com := aos506Original(t, "plano~read_notes~2", agentruntime.RetryNoticeNoFunctionCall)
	spec := com.spec
	spec.RetryNotice = agentruntime.RetryNoticeNone
	if res, err := mustEngine(t, com).Replay(context.Background(), com.goal.RunID, Options{Spec: spec}); err != nil || res.Divergence == nil {
		t.Fatalf("sem o aviso na spec, o replay de um run que o levou tinha de divergir: err=%v res=%+v", err, res)
	}
	sem := aos506Original(t, "plano~read_notes", agentruntime.RetryNoticeNone)
	spec = sem.spec
	spec.RetryNotice = agentruntime.RetryNoticeNoFunctionCall
	if res, err := mustEngine(t, sem).Replay(context.Background(), sem.goal.RunID, Options{Spec: spec}); err != nil || res.Divergence == nil {
		t.Fatalf("com o aviso na spec, o replay de um run que nao o levou tinha de divergir: err=%v res=%+v", err, res)
	}
	// E o run sem aviso reproduz-se com a spec de sempre: a semente dele não mudou.
	if res, err := mustEngine(t, sem).Replay(context.Background(), sem.goal.RunID, Options{Spec: sem.spec}); err != nil || res.Divergence != nil || res.Fidelity != 1.0 {
		t.Fatalf("um run sem aviso reproduz-se como sempre: err=%v res=%+v", err, res)
	}
}

// UM AVISO QUE O KERNEL NÃO CONHECE É RECUSADO PELO REPLAY, COMO PELO LOOP (revisão, M-6). Antes,
// o motor tratava-o como «sem aviso» e reproduzia o run com outra semente.
func TestAOS506_ReplayComAvisoDesconhecidoERecusado(t *testing.T) {
	sem := aos506Original(t, "plano~read_notes", agentruntime.RetryNoticeNone)
	spec := sem.spec
	spec.RetryNotice = "um_aviso_que_nao_existe"
	res, err := mustEngine(t, sem).Replay(context.Background(), sem.goal.RunID, Options{Spec: spec})
	if !errors.Is(err, agentruntime.ErrUnknownRetryNotice) {
		t.Fatalf("um aviso fora do vocabulario tinha de recusar o replay com ErrUnknownRetryNotice; veio err=%v res=%+v", err, res)
	}
	// Controlo: o loop recusa o mesmo valor, com o mesmo erro.
	goal := sem.goal
	goal.RunID, goal.RetryNotice = "plano~read_notes~9", "um_aviso_que_nao_existe"
	if _, lerr := agentruntime.SeedTail(agentruntime.AssemblyVersion, goal); !errors.Is(lerr, agentruntime.ErrUnknownRetryNotice) {
		t.Fatalf("controlo: o kernel recusa o mesmo valor; veio %v", lerr)
	}
	// E um aviso CONHECIDO num layout sem `notice` (a 1.3.0) é recusado pela mesma regra.
	if _, lerr := seedTail(TrajectorySpec{RetryNotice: agentruntime.RetryNoticeNoFunctionCall}, agentruntime.AssemblyVersion130); !errors.Is(lerr, agentruntime.ErrUnknownRetryNotice) {
		t.Fatalf("um aviso num layout sem notice e recusado; veio %v", lerr)
	}
	if segs, lerr := seedTail(TrajectorySpec{Objective: "x"}, agentruntime.AssemblyVersion130); lerr != nil || len(segs) != 1 {
		t.Fatalf("sem aviso a semente e a de sempre, em qualquer layout: %v %d", lerr, len(segs))
	}
}
