package integration

import (
	"context"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-069 — a via DURÁVEL preserva o taint da autorização (ADR-034).
//
// Falha medida em produção a 2026-09-26 (fase 1, AOS_PRIVILEGED_CAPS com cap:fs.read): o loop
// cunhava trusted para a call do turno 1 de um nó só com o objectivo, e o RM recebia
// untrusted. O [DurableDispatcher] traduzia o Call numa activity.Activity sem o taint, e o
// `Activity.toCall` fixava-o em untrusted. A via directa (o default do kernel, a de todos os
// testes do AOS-069 no kernel) não tinha o defeito — por isso os testes do kernel passavam.
//
// A propriedade fixada aqui é a PARIDADE: as duas implementações da MESMA porta
// [agentruntime.ActivityDispatcher] entregam ao RM o MESMO rótulo, para os dois lados do
// reticulado, e com as duas janelas (a inline e a do MEM).

// taintRecorder é um hook do RM que regista o taint da autorização de cada Call e permite.
type taintRecorder struct {
	mu     sync.Mutex
	taints []string
}

func (h *taintRecorder) Name() string { return "taint-rec" }
func (h *taintRecorder) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	h.mu.Lock()
	h.taints = append(h.taints, call.Context.Taint)
	h.mu.Unlock()
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// taintQueChegaAoRM corre o goal e devolve o taint que o RM viu na (única) tool call.
func taintQueChegaAoRM(t *testing.T, g agentruntime.Goal, duravel bool, opts ...agentruntime.Option) string {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	rec := &taintRecorder{}
	rm := referencemonitor.New(referencemonitor.WithHooks(rec), referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if duravel {
		opts = append(opts, agentruntime.WithActivityDispatcher(newDurableDispatcher(t, store, rm)))
	}
	rt := agentruntime.New(portModel(nil), rm, agentruntime.NewTurnRecorder(store), opts...)
	if _, err := rt.Run(context.Background(), g); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.taints) != 1 {
		t.Fatalf("esperava exactamente 1 mediacao, vieram %d (%v)", len(rec.taints), rec.taints)
	}
	return rec.taints[0]
}

func TestAOS069_ViaDuravelPreservaOTaintDaAutorizacao(t *testing.T) {
	comPlanInput := portTestGoal()
	comPlanInput.Inputs = []agentruntime.PlanInput{{From: "n0", Output: "document_content", Digest: "sha256:00", Content: []byte("dados")}}

	wf, err := NewWindowManagerFactory(100_000)
	if err != nil {
		t.Fatalf("NewWindowManagerFactory: %v", err)
	}
	janelas := map[string][]agentruntime.Option{
		"janela-inline": nil,
		"janela-MEM":    {agentruntime.WithWindowFactory(wf)},
	}
	casos := []struct {
		nome string
		goal agentruntime.Goal
		quer string
	}{
		{"so-objectivo", portTestGoal(), "trusted"},
		{"com-plan_input", comPlanInput, "untrusted"},
	}
	for jn, jopts := range janelas {
		for _, c := range casos {
			t.Run(jn+"/"+c.nome, func(t *testing.T) {
				directo := taintQueChegaAoRM(t, c.goal, false, jopts...)
				duravel := taintQueChegaAoRM(t, c.goal, true, jopts...)
				if directo != c.quer {
					t.Fatalf("via directa: o RM devia ver %q, viu %q", c.quer, directo)
				}
				if duravel != directo {
					t.Fatalf("via duravel: o RM viu %q e a directa %q — a porta durável perde o rotulo cunhado pelo loop", duravel, directo)
				}
			})
		}
	}
}

// TestAOS069_ViaDuravelTaintDesconhecidoResolveUntrusted fixa o fail-closed da tradução: um
// rótulo vazio ou fora da forma canónica chega ao RM como untrusted.
func TestAOS069_ViaDuravelTaintDesconhecidoResolveUntrusted(t *testing.T) {
	for _, bruto := range []string{"", "TRUSTED", "trusted ", "confiavel"} {
		store, err := eventstore.New()
		if err != nil {
			t.Fatalf("eventstore.New: %v", err)
		}
		rec := &taintRecorder{}
		rm := referencemonitor.New(referencemonitor.WithHooks(rec), referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
		if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
			t.Fatalf("Register: %v", err)
		}
		dd := newDurableDispatcher(t, store, rm)
		g := portTestGoal()
		if _, err := dd.Dispatch(context.Background(), referencemonitor.Call{
			RunID: g.RunID, StepID: "s-" + bruto + "-1", ToolID: "echo", Capability: "cap:echo",
			Principal: g.Principal, Context: referencemonitor.CallContext{Taint: bruto},
		}); err != nil {
			t.Fatalf("Dispatch(%q): %v", bruto, err)
		}
		if len(rec.taints) != 1 || rec.taints[0] != "untrusted" {
			t.Fatalf("taint %q devia chegar ao RM como untrusted, chegou %v", bruto, rec.taints)
		}
		store.Close()
	}
}
