package state

import (
	"bytes"
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-493 (revisão M5) — O VEREDICTO SÓ ENTRA NO FIM DO RUN, E NO VOCABULÁRIO.
//
// [TransitionEvent.Verdict] vira `outcome_reason` e `verdict` no evento da transição. A máquina
// aceitava-o em qualquer transição e com qualquer razão; quem o preenche hoje é só o selo
// terminal do nó, mas a regra era do chamador e não da máquina. Passa a ser dela: fora de uma
// transição que termina o run, ou com uma razão fora do vocabulário fechado, a transição é
// rejeitada sem tocar no log nem no estado.

func aos493Transicoes(t *testing.T, st EventStore, runID string) [][]byte {
	t.Helper()
	evs, err := st.Read(context.Background(), runID, 1)
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		return nil // o run ainda não gravou transição nenhuma
	}
	if err != nil {
		t.Fatalf("Read(%s): %v", runID, err)
	}
	var out [][]byte
	for _, ev := range evs {
		if ev.Type == EventTypeTransition {
			out = append(out, ev.Payload)
		}
	}
	return out
}

func TestAOS493_Veredicto_SoNumaTransicaoQueTerminaORun(t *testing.T) {
	ctx := context.Background()
	positivo := &agentruntime.Verdict{Mode: agentruntime.CompletionObserve, Fulfilled: true}
	negativo := &agentruntime.Verdict{Mode: agentruntime.CompletionEnforce, Reason: agentruntime.OutcomeTruncated}

	// (1) RECUSADO fora do fim do run: no claim e em cada suspensão. Nada fica no log.
	for _, c := range []struct {
		nome string
		// antes leva a máquina ao estado de partida; to é o destino pedido com veredicto.
		antes []State
		to    State
	}{
		{"claim ready→running", nil, Running},
		{"running→paused", []State{Running}, Paused},
		{"running→waiting_on_human", []State{Running}, WaitingOnHuman},
		{"running→waiting_on_tool", []State{Running}, WaitingOnTool},
		{"paused→running", []State{Running, Paused}, Running},
		{"failed→compensating", []State{Running, Failed}, Compensating},
	} {
		t.Run("recusado/"+c.nome, func(t *testing.T) {
			st := newStore(t)
			obs := &countingObserver{}
			m := mustMachine(t, st, "run-493-m5", WithObserver(obs))
			for _, s := range c.antes {
				if err := m.Transition(ctx, s, tok); err != nil {
					t.Fatalf("preparar %s: %v", s, err)
				}
			}
			de, gravadas := m.Current(), len(aos493Transicoes(t, st, "run-493-m5"))
			ev := tok
			ev.Verdict = negativo
			if err := m.Transition(ctx, c.to, ev); !errors.Is(err, ErrVerdictOutsideTerminal) {
				t.Fatalf("%s com veredicto tinha de ser recusada com ErrVerdictOutsideTerminal; veio %v", c.nome, err)
			}
			if m.Current() != de {
				t.Fatalf("a transicao recusada avancou o estado: %s → %s", de, m.Current())
			}
			if n := len(aos493Transicoes(t, st, "run-493-m5")); n != gravadas {
				t.Fatalf("a transicao recusada deixou rasto no log: %d → %d transicoes", gravadas, n)
			}
			if obs.rejected != 1 || !errors.Is(obs.lastRejectErr, ErrVerdictOutsideTerminal) {
				t.Fatalf("o observador nao viu a recusa: rejected=%d err=%v", obs.rejected, obs.lastRejectErr)
			}
			// A MESMA transição, sem veredicto, passa: a recusa é do veredicto e de mais nada.
			if err := m.Transition(ctx, c.to, tok); err != nil {
				t.Fatalf("%s sem veredicto tinha de passar: %v", c.nome, err)
			}
		})
	}

	// (2) ACEITE em cada estado em que um run acaba, com a razão e o vector no evento.
	for _, c := range []struct {
		to State
		v  *agentruntime.Verdict
	}{
		{Complete, positivo},
		{Complete, &agentruntime.Verdict{Mode: agentruntime.CompletionObserve, Reason: agentruntime.OutcomeContractNoCall}},
		{Failed, negativo},
		{TimedOut, positivo},
	} {
		t.Run("aceite/"+string(c.to)+"/"+string(c.v.Reason), func(t *testing.T) {
			st := newStore(t)
			m := mustMachine(t, st, "run-493-m5-ok")
			if err := m.Transition(ctx, Running, tok); err != nil {
				t.Fatal(err)
			}
			if err := m.Transition(ctx, c.to, TransitionEvent{Reason: "fim", Verdict: c.v}); err != nil {
				t.Fatalf("running→%s com veredicto tinha de passar: %v", c.to, err)
			}
			trs := aos493Transicoes(t, st, "run-493-m5-ok")
			ultima := trs[len(trs)-1]
			if !bytes.Contains(ultima, []byte(`"verdict":{`)) {
				t.Fatalf("o evento nao leva o veredicto: %s", ultima)
			}
			if c.v.Reason != "" && !bytes.Contains(ultima, []byte(`"outcome_reason":"`+string(c.v.Reason)+`"`)) {
				t.Fatalf("o evento nao leva a razao: %s", ultima)
			}
			if c.v.Reason == "" && bytes.Contains(ultima, []byte("outcome_reason")) {
				t.Fatalf("um veredicto positivo nao grava outcome_reason: %s", ultima)
			}
		})
	}
}

func TestAOS493_Veredicto_RazaoForaDoVocabularioERecusada(t *testing.T) {
	ctx := context.Background()
	const hostil = "MARCA-HOSTIL\naos_up 0"
	for _, razao := range []agentruntime.OutcomeReason{"objective_unfulfilled", "Truncated", " truncated", "run_failed", hostil} {
		st := newStore(t)
		obs := &countingObserver{}
		m := mustMachine(t, st, "run-493-m5-razao", WithObserver(obs))
		if err := m.Transition(ctx, Running, tok); err != nil {
			t.Fatal(err)
		}
		v := &agentruntime.Verdict{Mode: agentruntime.CompletionEnforce, Reason: razao}
		err := m.Transition(ctx, Failed, TransitionEvent{Reason: "objective_unfulfilled", Verdict: v})
		if !errors.Is(err, ErrVerdictReasonUnknown) {
			t.Fatalf("a razao %q tinha de ser recusada com ErrVerdictReasonUnknown; veio %v", razao, err)
		}
		// A razão recusada não é repetida no erro: é o texto que não se quer propagado.
		if bytes.Contains([]byte(err.Error()), []byte("MARCA-HOSTIL")) {
			t.Fatalf("o erro repete a razao recusada: %q", err)
		}
		if m.Current() != Running || len(aos493Transicoes(t, st, "run-493-m5-razao")) != 1 {
			t.Fatalf("a transicao recusada deixou efeitos: estado=%s transicoes=%d", m.Current(), len(aos493Transicoes(t, st, "run-493-m5-razao")))
		}
		if obs.rejected != 1 {
			t.Fatalf("o observador nao viu a recusa (rejected=%d)", obs.rejected)
		}
		// O run não fica preso: o selo sem veredicto continua a passar.
		if err := m.Transition(ctx, Failed, TransitionEvent{Reason: "run_failed"}); err != nil {
			t.Fatalf("running→failed sem veredicto tinha de passar: %v", err)
		}
	}
	// Todo o vocabulário é aceite.
	for _, razao := range append([]agentruntime.OutcomeReason{agentruntime.OutcomeFulfilled}, agentruntime.OutcomeReasons()...) {
		st := newStore(t)
		m := mustMachine(t, st, "run-493-m5-vocab")
		if err := m.Transition(ctx, Running, tok); err != nil {
			t.Fatal(err)
		}
		v := &agentruntime.Verdict{Mode: agentruntime.CompletionObserve, Fulfilled: razao == "", Reason: razao}
		if err := m.Transition(ctx, Complete, TransitionEvent{Verdict: v}); err != nil {
			t.Fatalf("a razao %q e do vocabulario e foi recusada: %v", razao, err)
		}
	}
}
