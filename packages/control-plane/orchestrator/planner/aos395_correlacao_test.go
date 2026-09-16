package planner_test

// AOS-395 — cada TENTATIVA de decomposição leva no ctx o run e um passo estável, para que quem
// chama o modelo (no aos-orq, o Model Gateway) possa selar a chamada ligada ao run e à tentativa.
// É o mecanismo do AOS-394 aplicado ao planeador.

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planner"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// ctxCapturingDecomposer regista, por tentativa, o par (run, passo) que o ctx traz — e falha as
// primeiras `failFirst` para forçar mais do que uma tentativa.
type ctxCapturingDecomposer struct {
	failFirst int
	vistos    []string // "run|passo|ok"
}

func (d *ctxCapturingDecomposer) Decompose(ctx context.Context, _ planner.DecomposeInput) (plan.PlanDocument, error) {
	run, step, ok := agentruntime.ModelCallFromContext(ctx)
	marca := run + "|" + step
	if !ok {
		marca = "SEM-ANEXO"
	}
	d.vistos = append(d.vistos, marca)
	if len(d.vistos) <= d.failFirst {
		return plan.PlanDocument{}, errFakeDecompose
	}
	return validDoc(), nil
}

func TestAOS395_RunAttempt_AnexaRunEPassoPorTentativa(t *testing.T) {
	iss := newIssuer(t)
	b := newBudget(t, runID, amt(100_000, 200_000))
	dec := &ctxCapturingDecomposer{failFirst: 1} // a 1.ª tentativa falha ⇒ há uma 2.ª
	p, err := planner.NewPlanner(b, permittingRM(t), iss, dec)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}

	res, err := p.Decompose(context.Background(), planner.DecomposeRequest{
		RunID: runID, PlanID: runID, ParentBudgetNode: parentNode, PlannerBudgetNode: plannerNode,
		ParentToken: runToken(t, iss).Compact, Child: plannerChildReq(),
		Context: planner.PlanningContext{Goal: "g", ContextUnits: 4, CapabilitiesHash: "sha256:cap"},
	})
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if res.Attempts < 2 {
		t.Fatalf("pre-condicao do teste: esperava 2 tentativas, veio %d", res.Attempts)
	}

	// Uma marca por tentativa, com o passo a discriminar a tentativa — e nunca "SEM-ANEXO".
	quer := []string{runID + "|planstep:decompose:1", runID + "|planstep:decompose:2"}
	if len(dec.vistos) != len(quer) {
		t.Fatalf("tentativas observadas = %v, esperava %v", dec.vistos, quer)
	}
	for i, q := range quer {
		if dec.vistos[i] != q {
			t.Fatalf("tentativa %d viu %q, esperava %q", i+1, dec.vistos[i], q)
		}
	}
}
