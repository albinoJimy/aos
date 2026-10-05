package state

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-497 — A ÂNCORA DA SAÍDA NA TRANSIÇÃO TERMINAL.
//
// [TransitionEvent.OutputSource] vira `output_source` no evento, ao lado do veredicto. Segue a
// regra dele: só numa transição que termina o run, e só na forma que o kernel produz. Lê-se de
// volta por [Machine.RebuildOutcome], do mesmo evento que o estado e o veredicto.

const aos497Digest = "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func aos497Designada() *agentruntime.OutputSource {
	return &agentruntime.OutputSource{
		Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceDesignated,
		StepID: "passo-1-tool-1", Digest: aos497Digest, Bytes: 4,
	}
}

func TestAOS497_Ancora_SeladaAoLadoDoVeredictoELidaDeVolta(t *testing.T) {
	ctx := context.Background()
	emFalta := &agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceMeasure, State: agentruntime.OutputSourceMissing}
	ambigua := &agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceAmbiguous}
	positivo := &agentruntime.Verdict{Mode: agentruntime.CompletionEnforce, Fulfilled: true}
	negativo := &agentruntime.Verdict{Mode: agentruntime.CompletionEnforce, Reason: agentruntime.OutcomeOutputSourceMissing}
	for _, c := range []struct {
		nome   string
		to     State
		v      *agentruntime.Verdict
		ancora *agentruntime.OutputSource
		quer   string
	}{
		{"complete, designada", Complete, positivo, aos497Designada(),
			`"output_source":{"tool":"doc_read","binding":"binding","state":"designated","step_id":"passo-1-tool-1","digest":"` + aos497Digest + `","bytes":4}`},
		{"complete, so medicao em falta", Complete, positivo, emFalta,
			`"output_source":{"tool":"doc_read","binding":"measure","state":"missing"}`},
		{"failed, vinculada em falta", Failed, negativo, &agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceMissing},
			`"output_source":{"tool":"doc_read","binding":"binding","state":"missing"}`},
		{"failed, ambigua", Failed, &agentruntime.Verdict{Mode: agentruntime.CompletionEnforce, Reason: agentruntime.OutcomeOutputSourceAmbiguous}, ambigua,
			`"output_source":{"tool":"doc_read","binding":"binding","state":"ambiguous"}`},
		{"timed_out, sem veredicto", TimedOut, nil, emFalta, `"output_source":{"tool":"doc_read","binding":"measure","state":"missing"}`},
	} {
		t.Run(c.nome, func(t *testing.T) {
			st := newStore(t)
			m := mustMachine(t, st, "run-497-ancora")
			if err := m.Transition(ctx, Running, tok); err != nil {
				t.Fatal(err)
			}
			if err := m.Transition(ctx, c.to, TransitionEvent{Reason: "fim", Verdict: c.v, OutputSource: c.ancora}); err != nil {
				t.Fatalf("running→%s com ancora tinha de passar: %v", c.to, err)
			}
			trs := aos493Transicoes(t, st, "run-497-ancora")
			ultima := trs[len(trs)-1]
			if !bytes.Contains(ultima, []byte(c.quer)) {
				t.Fatalf("o evento nao leva a ancora na forma esperada:\n  %s\nquero\n  %s", ultima, c.quer)
			}
			// A âncora vem DEPOIS do veredicto, no fim do registo: os campos anteriores ficam onde
			// estavam.
			if c.v != nil && !strings.HasSuffix(string(ultima), c.quer+"}") {
				t.Fatalf("a ancora tinha de ser o ultimo campo do registo: %s", ultima)
			}
			estado, oc, err := mustMachine(t, st, "run-497-ancora").RebuildOutcome(ctx)
			if err != nil || estado != c.to {
				t.Fatalf("RebuildOutcome: estado=%s err=%v", estado, err)
			}
			if !reflect.DeepEqual(oc.OutputSource, c.ancora) || !reflect.DeepEqual(oc.Verdict, c.v) {
				t.Fatalf("lido do log: ancora=%+v veredicto=%+v; quero %+v e %+v", oc.OutputSource, oc.Verdict, c.ancora, c.v)
			}
		})
	}
}

// SEM ÂNCORA, o evento não ganha o campo.
func TestAOS497_SemAncora_OEventoNaoGanhaOCampo(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	m := mustMachine(t, st, "run-497-sem")
	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	v := &agentruntime.Verdict{Mode: agentruntime.CompletionObserve, Fulfilled: true}
	if err := m.Transition(ctx, Complete, TransitionEvent{Reason: "run_complete", Verdict: v}); err != nil {
		t.Fatal(err)
	}
	for _, tr := range aos493Transicoes(t, st, "run-497-sem") {
		if bytes.Contains(tr, []byte("output_source")) {
			t.Fatalf("uma transicao sem ancora ganhou o campo: %s", tr)
		}
	}
}

func TestAOS497_Ancora_SoNumaTransicaoQueTerminaORun(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		nome  string
		antes []State
		to    State
	}{
		{"claim ready→running", nil, Running},
		{"running→paused", []State{Running}, Paused},
		{"running→waiting_on_human", []State{Running}, WaitingOnHuman},
		{"running→waiting_on_tool", []State{Running}, WaitingOnTool},
		{"failed→compensating", []State{Running, Failed}, Compensating},
	} {
		t.Run(c.nome, func(t *testing.T) {
			st := newStore(t)
			obs := &countingObserver{}
			m := mustMachine(t, st, "run-497-fora", WithObserver(obs))
			for _, s := range c.antes {
				if err := m.Transition(ctx, s, tok); err != nil {
					t.Fatalf("preparar %s: %v", s, err)
				}
			}
			de, gravadas := m.Current(), len(aos493Transicoes(t, st, "run-497-fora"))
			ev := tok
			ev.OutputSource = aos497Designada()
			if err := m.Transition(ctx, c.to, ev); !errors.Is(err, ErrOutputSourceOutsideTerminal) {
				t.Fatalf("%s com ancora tinha de ser recusada com ErrOutputSourceOutsideTerminal; veio %v", c.nome, err)
			}
			if m.Current() != de || len(aos493Transicoes(t, st, "run-497-fora")) != gravadas {
				t.Fatalf("a transicao recusada deixou efeitos: estado %s → %s", de, m.Current())
			}
			if obs.rejected != 1 || !errors.Is(obs.lastRejectErr, ErrOutputSourceOutsideTerminal) {
				t.Fatalf("o observador nao viu a recusa: rejected=%d err=%v", obs.rejected, obs.lastRejectErr)
			}
			if err := m.Transition(ctx, c.to, tok); err != nil {
				t.Fatalf("%s sem ancora tinha de passar: %v", c.nome, err)
			}
		})
	}
}

func TestAOS497_Ancora_MalFormadaERecusada(t *testing.T) {
	ctx := context.Background()
	const hostil = "MARCA-HOSTIL\naos_up 0"
	for nome, muda := range map[string]func(*agentruntime.OutputSource){
		"estado fora do vocabulario":  func(s *agentruntime.OutputSource) { s.State = hostil },
		"vinculo fora do vocabulario": func(s *agentruntime.OutputSource) { s.Binding = hostil },
		"tool com quebra de linha":    func(s *agentruntime.OutputSource) { s.Tool = hostil },
		"digest com texto":            func(s *agentruntime.OutputSource) { s.Digest = "sha256:" + hostil },
		"passo com texto":             func(s *agentruntime.OutputSource) { s.StepID = hostil },
		"em falta com digest":         func(s *agentruntime.OutputSource) { s.State = agentruntime.OutputSourceMissing },
	} {
		t.Run(nome, func(t *testing.T) {
			st := newStore(t)
			obs := &countingObserver{}
			m := mustMachine(t, st, "run-497-forma", WithObserver(obs))
			if err := m.Transition(ctx, Running, tok); err != nil {
				t.Fatal(err)
			}
			ancora := aos497Designada()
			muda(ancora)
			err := m.Transition(ctx, Complete, TransitionEvent{Reason: "run_complete", OutputSource: ancora})
			if !errors.Is(err, ErrOutputSourceMalformed) {
				t.Fatalf("a ancora tinha de ser recusada com ErrOutputSourceMalformed; veio %v", err)
			}
			if bytes.Contains([]byte(err.Error()), []byte("MARCA-HOSTIL")) {
				t.Fatalf("o erro repete o conteudo recusado: %q", err)
			}
			if m.Current() != Running || len(aos493Transicoes(t, st, "run-497-forma")) != 1 || obs.rejected != 1 {
				t.Fatalf("a transicao recusada deixou efeitos: estado=%s rejected=%d", m.Current(), obs.rejected)
			}
			// O run não fica preso: o selo sem âncora continua a passar.
			if err := m.Transition(ctx, Complete, TransitionEvent{Reason: "run_complete"}); err != nil {
				t.Fatalf("running→complete sem ancora tinha de passar: %v", err)
			}
		})
	}
}
