package state

import (
	"context"
	"reflect"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-494 — O DESFECHO DURÁVEL LÊ-SE COM A RAZÃO. [Machine.RebuildOutcome] devolve o estado e o
// veredicto da MESMA transição: é o que deixa o ramo durável do `GET /runs/{id}` do nó responder
// o que o ramo em memória responde.
func TestAOS494_RebuildOutcome_EstadoEVeredictoDaUltimaTransicao(t *testing.T) {
	ctx := context.Background()
	negativo := &agentruntime.Verdict{
		Mode: agentruntime.CompletionEnforce, Reason: agentruntime.OutcomeContractNoCall,
		Tools: []agentruntime.ToolEvidence{{Tool: "doc_read"}},
	}
	observado := &agentruntime.Verdict{Mode: agentruntime.CompletionObserve, Reason: agentruntime.OutcomeEmptyOutput}

	for _, c := range []struct {
		nome string
		to   State
		ev   TransitionEvent
		quer *agentruntime.Verdict
	}{
		{"failed por veredicto imposto", Failed, TransitionEvent{Reason: "objective_unfulfilled", Verdict: negativo}, negativo},
		{"complete com veredicto observado", Complete, TransitionEvent{Reason: "run_complete", Verdict: observado}, observado},
		{"failed sem veredicto", Failed, TransitionEvent{Reason: "run_failed"}, nil},
	} {
		t.Run(c.nome, func(t *testing.T) {
			st := newStore(t)
			m := mustMachine(t, st, "run-494-outcome")
			if err := m.Transition(ctx, Running, tok); err != nil {
				t.Fatal(err)
			}
			// A meio do run não há veredicto para ler.
			if estado, v, err := mustMachine(t, st, "run-494-outcome").RebuildOutcome(ctx); err != nil || estado != Running || v != nil {
				t.Fatalf("a meio do run: estado=%s veredicto=%+v err=%v; quero running, nil, nil", estado, v, err)
			}
			if err := m.Transition(ctx, c.to, c.ev); err != nil {
				t.Fatalf("selo terminal: %v", err)
			}
			// Outra máquina sobre o mesmo log: é a leitura de um processo que não viu o run.
			outra := mustMachine(t, st, "run-494-outcome")
			estado, v, err := outra.RebuildOutcome(ctx)
			if err != nil || estado != c.to {
				t.Fatalf("RebuildOutcome: estado=%s err=%v; quero %s", estado, err, c.to)
			}
			if !reflect.DeepEqual(v, c.quer) {
				t.Fatalf("veredicto lido do log = %+v, quero %+v", v, c.quer)
			}
			// E é o mesmo estado que o Rebuild de sempre devolve.
			if so, err := mustMachine(t, st, "run-494-outcome").Rebuild(ctx); err != nil || so != estado {
				t.Fatalf("Rebuild=%s err=%v; RebuildOutcome disse %s", so, err, estado)
			}
		})
	}

	// Um run que nunca existiu é `ready`, sem veredicto e sem erro — como no Rebuild.
	if estado, v, err := mustMachine(t, newStore(t), "run-494-inexistente").RebuildOutcome(ctx); err != nil || estado != Ready || v != nil {
		t.Fatalf("stream inexistente: estado=%s veredicto=%+v err=%v", estado, v, err)
	}
}
