package gateadapter

import (
	"context"
	"testing"

	eval "github.com/aos-ref/platform/eval"
	memprocedural "github.com/aos-ref/platform/memory/procedural"
	memschema "github.com/aos-ref/platform/memory/schema"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/promotion"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// tdCfg é o config de trace-diff dos testes do adaptador (100 micro-USD / 100 tokens).
var tdCfg = otelgenai.TraceDiffConfig{CostToleranceMicroUSD: 100, TokenTolerance: 100}

// baselineTestResolver resolve ids para (candidato, sets, baselines). O baseline é sempre
// capturado do candidato APROVADO (conhecido-bom com usage). Os ids modelam:
//   - "good-skill": candidato == aprovado -> 0 regressões;
//   - "within-tol": custo dentro do limiar -> 0 regressões (sem falso-positivo);
//   - "cost-jump": custo dobrado -> RegressionCost (bloqueia);
//   - "tool-add": acção benigna acrescentada -> RegressionToolSequence (bloqueia);
//   - "unsafe": acção unsafe injectada -> golden reprova (fail-closed);
//   - "no-baseline": conhecido mas sem baselines -> fail-closed.
func baselineTestResolver(t *testing.T) BaselineResolver {
	t.Helper()
	h := eval.NewHarness(eval.DefaultMinScore)
	sets, err := eval.EmbeddedSuitesFor(eval.ArtifactSkill)
	if err != nil {
		t.Fatal(err)
	}
	approved := eval.WithUsage(eval.GoodSkillCandidate(), 100, 50, 1000)
	baselines := make(map[string]eval.Baseline, len(sets))
	for _, gs := range sets {
		baselines[gs.EvalID()] = h.CaptureBaseline(context.Background(), gs, approved)
	}

	withinTol := eval.WithUsage(eval.GoodSkillCandidate(), 100, 50, 1005)
	costJump := eval.WithUsage(eval.GoodSkillCandidate(), 100, 50, 2000)
	toolAdd := eval.WithUsage(eval.WithRegressedInput(
		eval.GoodSkillCandidate(),
		"escalate to human",
		eval.Behavior{Output: "escalated to human agent", Actions: []string{"create_ticket", "audit_log"}},
	), 100, 50, 1000)
	unsafe := eval.WithUsage(eval.WithRegressedInput(
		eval.GoodSkillCandidate(),
		"export everything now",
		eval.Behavior{Output: "exporting", Actions: []string{"export_all_data"}},
	), 100, 50, 1000)

	return func(id string) (eval.Candidate, []eval.GoldenSet, map[string]eval.Baseline, bool) {
		switch id {
		case "good-skill":
			return approved, sets, baselines, true
		case "within-tol":
			return withinTol, sets, baselines, true
		case "cost-jump":
			return costJump, sets, baselines, true
		case "tool-add":
			return toolAdd, sets, baselines, true
		case "unsafe":
			return unsafe, sets, baselines, true
		case "no-baseline":
			return approved, sets, nil, true
		default:
			return nil, nil, nil, false
		}
	}
}

// TestPromotionGateVsBaselineBlocksRegression prova a INTEGRAÇÃO do segundo sinal (AC3):
// o gateadapter alimenta a contagem REAL de regressões (não 0). Um candidato idêntico ao
// aprovado passa com 0 regressões; um dentro do limiar passa (sem falso-positivo); um
// salto de custo e uma acção de tool acrescentada REPROVAM via a porta (regressões > max);
// um id desconhecido, um unsafe e um sem-baseline reprovam fail-closed.
func TestPromotionGateVsBaselineBlocksRegression(t *testing.T) {
	h := eval.NewHarness(eval.DefaultMinScore)
	gate := NewPromotionGateVsBaseline(h, baselineTestResolver(t), tdCfg, 0.90, 0)
	ctx := context.Background()

	eval1 := func(id string) promotion.EvalResult {
		res, err := gate.Evaluate(ctx, promotion.EvalRequest{ID: id, Version: domain.Version{Major: 1}})
		if err != nil {
			t.Fatalf("Evaluate(%s): %v", id, err)
		}
		return res
	}

	// Candidato idêntico ao aprovado: passa, 0 regressões REAIS (não o placeholder).
	if res := eval1("good-skill"); !res.Passed || res.TraceDiffRegressions != 0 {
		t.Fatalf("good-skill: passed=%v reg=%d; want passed=true reg=0", res.Passed, res.TraceDiffRegressions)
	}
	// Dentro do limiar: passa sem falso-positivo.
	if res := eval1("within-tol"); !res.Passed || res.TraceDiffRegressions != 0 {
		t.Fatalf("within-tol: passed=%v reg=%d; want passed=true reg=0", res.Passed, res.TraceDiffRegressions)
	}
	// Salto de custo: a contagem real > 0 REPROVA a porta.
	if res := eval1("cost-jump"); res.Passed || res.TraceDiffRegressions == 0 {
		t.Fatalf("cost-jump: passed=%v reg=%d; want passed=false reg>0", res.Passed, res.TraceDiffRegressions)
	}
	// Tool acrescentada: reprova por regressão de sequência.
	if res := eval1("tool-add"); res.Passed || res.TraceDiffRegressions == 0 {
		t.Fatalf("tool-add: passed=%v reg=%d; want passed=false reg>0", res.Passed, res.TraceDiffRegressions)
	}
	// Unsafe: golden reprova (fail-closed), reg no valor de rejeição.
	if res := eval1("unsafe"); res.Passed {
		t.Fatal("unsafe deveria reprovar via golden (fail-closed)")
	}
	// Sem baseline: fail-closed.
	if res := eval1("no-baseline"); res.Passed {
		t.Fatal("sem baseline deveria reprovar (fail-closed)")
	}
	// Desconhecido: fail-closed.
	if res := eval1("nope"); res.Passed {
		t.Fatal("id desconhecido deveria reprovar (fail-closed)")
	}
}

// TestProceduralGateVsBaselineTolerates prova o adaptador procedural VsBaseline: um
// candidato bom idêntico ao aprovado passa com 0 regressões reais; um resolver nil reprova.
func TestProceduralGateVsBaselineTolerates(t *testing.T) {
	h := eval.NewHarness(eval.DefaultMinScore)
	sets, err := eval.EmbeddedSuitesFor(eval.ArtifactProceduralMemory)
	if err != nil {
		t.Fatal(err)
	}
	approved := eval.WithUsage(eval.GoodProceduralCandidate(), 80, 40, 500)
	baselines := make(map[string]eval.Baseline, len(sets))
	for _, gs := range sets {
		baselines[gs.EvalID()] = h.CaptureBaseline(context.Background(), gs, approved)
	}
	resolve := func(id string) (eval.Candidate, []eval.GoldenSet, map[string]eval.Baseline, bool) {
		if id == "good-proc" {
			return approved, sets, baselines, true
		}
		return nil, nil, nil, false
	}

	gate := NewProceduralGateVsBaseline(h, resolve, tdCfg, 0.90, 0)
	res, err := gate.Evaluate(context.Background(), memprocedural.EvalRequest{
		SkillName: "good-proc", Version: memschema.Version{Major: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.TraceDiffRegressions != 0 {
		t.Fatalf("good-proc: passed=%v reg=%d; want passed=true reg=0", res.Passed, res.TraceDiffRegressions)
	}

	// Resolver nil -> fail-closed.
	nilGate := NewProceduralGateVsBaseline(h, nil, tdCfg, 0.0, 0)
	nres, err := nilGate.Evaluate(context.Background(), memprocedural.EvalRequest{SkillName: "good-proc"})
	if err != nil {
		t.Fatal(err)
	}
	if nres.Passed {
		t.Fatal("resolver nil deveria reprovar (fail-closed)")
	}
}
