package runlifecycle_test

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planbudget"
	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/substrate/eventstore"
)

// derivacao_condicional_test.go — A PODA DE `branch_not_taken` COM PORTAS DE PRODUÇÃO
// (AOS-390, ADR-024, ADR-022 §2.1).
//
// Prova a peça central da mudança de ADR-024: o efeito de despacho é do
// plandispatch.Dispatcher, e a avaliação da condição (ADR-022 §2.1) corre sobre o
// RESULTADO REGISTADO, derivado do log de um run possuído — não sobre um double. As
// portas de RAMOS são as de produção:
//   - `terminal_state` ← plandispatch.LifecycleResults sobre o LifecycleReader real;
//   - o registo append-only da decisão ← runlifecycle.PlanRecorder.BranchJournal();
//   - o débito da avaliação ← planbudget.TreeBudgetMeter sobre o orçamento real (ADR-008).
//
// Gate/headroom/cartões/sink são duplos triviais de propósito (não são o que está sob
// prova — a mesma escolha de derivacao_test.go).
//
// Cenário: `a` termina COMPLETE. Dois ramos observam-no:
//   - `recuperacao` só despacha se `a` FALHAR  (terminal_state == failed) ⇒ PODADO;
//   - `seguimento`  só despacha se `a` COMPLETAR (terminal_state == complete) ⇒ DESPACHADO.
//
// É a prova simétrica de ADR-022 §2.1 (o ramo feliz e o de recuperação são duas arestas
// sobre o mesmo produtor): um é tomado, o outro é `branch_not_taken`.
func TestAOS390_Derivacao_CondicionalPodaOuDespacha(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	lm := replica(t, store, newClock(), "proc-orq")
	const runID = "run-cond-deriv"
	const planID = "plan-cond-deriv"

	ten, err := runlifecycle.Claim(ctx, store, lm, runID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	rec, err := runlifecycle.NewPlanRecorder(ten, planID, eventstore.Producer{NHIID: "nhi:orq"})
	if err != nil {
		t.Fatalf("NewPlanRecorder: %v", err)
	}

	// Topologia admitida pelo DONO (sob posse): os três nós, `a` conduzido a complete.
	g, err := ten.Graph(ctx, eventstore.Producer{})
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	for _, id := range []string{"a", "recuperacao", "seguimento"} {
		if err := g.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode(%s): %v", id, err)
		}
	}
	if err := g.MarkRunning(ctx, "a"); err != nil {
		t.Fatalf("MarkRunning(a): %v", err)
	}
	if err := marcarCompleto(ctx, ten, runID, "a"); err != nil {
		t.Fatalf("a→complete: %v", err)
	}

	// Orçamento real: raiz + um nó por condicional (o meter reserva a avaliação CONTRA o
	// nó que a declara, e a reserva sobe a ancestralidade até à raiz — ADR-008).
	bud, err := budget.New(runID, budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	for _, id := range []string{"recuperacao", "seguimento"} {
		if err := bud.AddNode(id, runID, budget.Amount{Tokens: 1000, CostMicroUSD: 1000}); err != nil {
			t.Fatalf("budget.AddNode(%s): %v", id, err)
		}
	}
	meter, err := planbudget.NewTreeBudgetMeter(bud, budget.Amount{Tokens: 1, CostMicroUSD: 1})
	if err != nil {
		t.Fatalf("NewTreeBudgetMeter: %v", err)
	}

	// Portas de LEITURA de produção, derivadas do log (retrato por passagem).
	lr, err := runlifecycle.NewLifecycleReader(store, planID, runID)
	if err != nil {
		t.Fatalf("NewLifecycleReader: %v", err)
	}
	vista, err := lr.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	results, err := plandispatch.NewLifecycleResults(vista)
	if err != nil {
		t.Fatalf("NewLifecycleResults: %v", err)
	}

	sink := &sinkRegistador{}
	d, err := plandispatch.NewDispatcher(gateAberto{}, vista, semTecto{}, cartoesLimpos{}, sink,
		plandispatch.WithConditionalBranches(results, rec.BranchJournal(), meter))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	falha := plan.ConditionalEdge{From: "a", When: []plan.Predicate{{Subject: plan.SubjectTerminalState, Op: plan.OpEq, Enum: plan.EnumFailed}}}
	completa := plan.ConditionalEdge{From: "a", When: []plan.Predicate{{Subject: plan.SubjectTerminalState, Op: plan.OpEq, Enum: plan.EnumComplete}}}

	res, err := d.Dispatch(ctx, plandispatch.Plan{
		PlanID: planID,
		Nodes: []plandispatch.Node{
			{NodeID: "a"},
			{NodeID: "recuperacao", ConditionalOn: []plan.ConditionalEdge{falha}},
			{NodeID: "seguimento", ConditionalOn: []plan.ConditionalEdge{completa}},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// `seguimento` (condição satisfeita) DESPACHADO; `recuperacao` (condição falsa) PODADO.
	got := sink.lista()
	if len(got) != 1 || got[0] != "seguimento" {
		t.Fatalf("despachados = %v, quer [seguimento] (só o ramo tomado)", got)
	}
	if res.NotTaken < 1 {
		t.Fatalf("NotTaken = %d, quer ≥1 (`recuperacao` devia ser podado por branch_not_taken)\nresultados: %+v", res.NotTaken, res.Results)
	}
	for _, r := range res.Results {
		if r.NodeID == "recuperacao" && r.Outcome != plandispatch.OutcomeBranchNotTaken {
			t.Errorf("`recuperacao` outcome = %q, quer branch_not_taken", r.Outcome)
		}
	}
	// Não-vacuidade: as decisões foram AVALIADAS e REGISTADAS nesta passagem (não lidas
	// de um registo pré-existente) — o que debita orçamento e apensa `plan.branch_decided`.
	if res.BranchesEvaluated < 2 {
		t.Errorf("BranchesEvaluated = %d, quer ≥2 (as duas condições decididas e registadas)", res.BranchesEvaluated)
	}
}
