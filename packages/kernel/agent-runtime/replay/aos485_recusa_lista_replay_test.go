package replay

// AOS-485 — a recusa pela lista-branca do run passou a gravar `tool.call.denied` no stream do
// run. O replay reconstrói o estado de `turn.recorded` + `replay.captured` (a negação vai na
// captura do turno), pelo que o evento novo não pode mudar o que se reconstrói — nem a sua
// falta, num log gravado antes do AOS-485.

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// aos485Original corre dois turnos com a lista-branca VAZIA: o turno 1 pede `echo`, que o RM
// nega; o turno 2 conclui.
func aos485Original(t *testing.T, runID string) originalRun {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	hits := 0
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		hits++
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	capturer, err := NewCapturer(store, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	turno := 0
	model := agentruntime.ModelClientFunc(func(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		turno++
		if turno == 1 {
			return agentruntime.ModelResponse{
				Text:      "vou ler",
				ToolCalls: []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}},
				Usage:     agentruntime.Usage{InputTokens: 3, OutputTokens: 1},
			}, nil
		}
		return agentruntime.ModelResponse{Text: "não tenho a tool", Final: true, Usage: agentruntime.Usage{InputTokens: 4, OutputTokens: 1}}, nil
	})
	rt := agentruntime.New(model, rm, agentruntime.NewTurnRecorder(store), agentruntime.WithCapturer(capturer))

	goal := sampleGoal(runID)
	goal.AllowedTools = []string{}
	if _, err := rt.Run(context.Background(), goal); err != nil {
		t.Fatalf("Run original: %v", err)
	}
	return originalRun{store: store, goal: goal, spec: specFromGoal(goal), toolHits: &hits}
}

// semRecusaDaLista lê o stream como ele era ANTES do AOS-485: sem o `tool.call.denied`. Conta
// os eventos que tirou, para o teste não passar sobre um log que nunca os teve.
type semRecusaDaLista struct {
	inner    EventReader
	tirados  int
	restante int
}

func (r *semRecusaDaLista) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	events, err := r.inner.Read(ctx, streamID, fromSeq)
	if err != nil {
		return nil, err
	}
	out := make([]eventstore.Event, 0, len(events))
	r.tirados = 0
	for _, ev := range events {
		if ev.Type == referencemonitor.EventTypeDenied {
			r.tirados++
			continue
		}
		out = append(out, ev)
	}
	r.restante = len(out)
	return out, nil
}

func TestAOS485_ReplayDeUmRunComRecusaPelaListaEFielEDeterminista(t *testing.T) {
	or := aos485Original(t, "run_replay_485")
	if *or.toolHits != 0 {
		t.Fatalf("preparação: a tool não podia ter executado (%d)", *or.toolHits)
	}
	// VerifyAuthority: o `tool.call.denied` novo entra na âncora de autoridade (taint selado
	// por turno), e tem de bater com a autoridade re-dobrada.
	opts := Options{Spec: or.spec, VerifyAuthority: true}
	primeiro, err := mustEngine(t, or).Replay(context.Background(), or.goal.RunID, opts)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if primeiro.Divergence != nil || primeiro.Fidelity != 1.0 || len(primeiro.Steps) != 2 || !primeiro.Terminated {
		t.Fatalf("o run com a recusa tinha de replayar fiel em 2 turnos: fidelidade=%v steps=%d divergencia=%+v",
			primeiro.Fidelity, len(primeiro.Steps), primeiro.Divergence)
	}
	segundo, err := mustEngine(t, or).Replay(context.Background(), or.goal.RunID, opts)
	if err != nil {
		t.Fatalf("Replay (2.º): %v", err)
	}
	if segundo.FinalStateHash != primeiro.FinalStateHash || segundo.Steps[1].PromptHash != primeiro.Steps[1].PromptHash {
		t.Fatalf("o replay não é determinista: %s vs %s", primeiro.FinalStateHash, segundo.FinalStateHash)
	}
	if *or.toolHits != 0 {
		t.Fatalf("o replay executou a tool (%d)", *or.toolHits)
	}
}

// Um log ANTIGO — o mesmo run sem o `tool.call.denied`, que é o que o nó gravava antes do
// AOS-485 — reconstrói exactamente o mesmo estado.
func TestAOS485_LogAntigoSemOEventoReconstroiOMesmoEstado(t *testing.T) {
	or := aos485Original(t, "run_replay_485_antigo")
	novo, err := mustEngine(t, or).Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("Replay (log novo): %v", err)
	}

	leitor := &semRecusaDaLista{inner: or.store}
	antigo, err := mustEngineOn(t, leitor).Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec, VerifyAuthority: true})
	if err != nil {
		t.Fatalf("Replay (log antigo): %v", err)
	}
	if leitor.tirados != 1 || leitor.restante == 0 {
		t.Fatalf("preparação: o log novo tinha de ter exactamente 1 tool.call.denied para tirar (tirados=%d, restantes=%d)", leitor.tirados, leitor.restante)
	}
	if antigo.Divergence != nil || antigo.Fidelity != 1.0 {
		t.Fatalf("o log antigo tinha de replayar fiel: fidelidade=%v divergencia=%+v", antigo.Fidelity, antigo.Divergence)
	}
	if antigo.FinalStateHash != novo.FinalStateHash || antigo.FinalText != novo.FinalText || len(antigo.Steps) != len(novo.Steps) {
		t.Fatalf("o log antigo reconstrói outro estado: %s (%d turnos) vs %s (%d turnos)",
			antigo.FinalStateHash, len(antigo.Steps), novo.FinalStateHash, len(novo.Steps))
	}
	for i := range novo.Steps {
		if antigo.Steps[i].PromptHash != novo.Steps[i].PromptHash {
			t.Fatalf("turno %d: prompt_hash do log antigo %s, do novo %s", novo.Steps[i].Turn, antigo.Steps[i].PromptHash, novo.Steps[i].PromptHash)
		}
	}
}
