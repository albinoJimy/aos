package gateadapter

import (
	"context"
	"testing"

	eval "github.com/aos-ref/platform/eval"
	memschema "github.com/aos-ref/platform/memory/schema"
	"github.com/aos-ref/platform/registry/domain"
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

// TestPromotionMetricsVsBaselineBlocksRegression prova a INTEGRAÇÃO do segundo sinal
// (AC3): o gateadapter alimenta a contagem REAL de regressões (não 0). Um candidato
// idêntico ao aprovado passa com 0 regressões; um dentro do limiar passa (sem
// falso-positivo); um salto de custo e uma acção de tool acrescentada REPROVAM via a
// porta (regressões > max); um id desconhecido, um unsafe e um sem-baseline reprovam
// fail-closed.
func TestPromotionMetricsVsBaselineBlocksRegression(t *testing.T) {
	h := eval.NewHarness(eval.DefaultMinScore)
	m := PromotionMetricsVsBaseline(h, baselineTestResolver(t), tdCfg, 0)

	score, reg := m("good-skill", domain.Version{Major: 1})
	if score != 1.0 || reg != 0 {
		t.Fatalf("good-skill = (%.3f, %d); want (1.0, 0)", score, reg)
	}

	score, reg = m("within-tol", domain.Version{Major: 1})
	if score != 1.0 || reg != 0 {
		t.Fatalf("within-tol = (%.3f, %d); want (1.0, 0)", score, reg)
	}

	// Salto de custo: a contagem real > 0 REPROVA a porta.
	score, reg = m("cost-jump", domain.Version{Major: 1})
	if score != 1.0 || reg == 0 {
		t.Fatalf("cost-jump = (%.3f, %d); want score=1.0 reg>0", score, reg)
	}

	// Tool acrescentada: reprova por regressão de sequência.
	score, reg = m("tool-add", domain.Version{Major: 1})
	if score != 1.0 || reg == 0 {
		t.Fatalf("tool-add = (%.3f, %d); want score=1.0 reg>0", score, reg)
	}

	// Unsafe: golden reprova (fail-closed), reg no valor de rejeição.
	score, reg = m("unsafe", domain.Version{Major: 1})
	if score >= 0 || reg != rejectRegressions {
		t.Fatalf("unsafe = (%.3f, %d); deveria reprovar (fail-closed)", score, reg)
	}

	// Sem baseline: fail-closed.
	score, reg = m("no-baseline", domain.Version{Major: 1})
	if score >= 0 || reg != rejectRegressions {
		t.Fatalf("no-baseline = (%.3f, %d); deveria reprovar (fail-closed)", score, reg)
	}

	// Desconhecido: fail-closed.
	score, reg = m("nope", domain.Version{Major: 1})
	if score >= 0 || reg != rejectRegressions {
		t.Fatalf("nope = (%.3f, %d); deveria reprovar (fail-closed)", score, reg)
	}
}

// TestProceduralMetricsVsBaselineTolerates prova a projecção procedural VsBaseline: um
// candidato bom idêntico ao aprovado passa com 0 regressões reais; um resolver nil reprova.
func TestProceduralMetricsVsBaselineTolerates(t *testing.T) {
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

	m := ProceduralMetricsVsBaseline(h, resolve, tdCfg, 0)
	score, reg := m("good-proc", memschema.Version{Major: 1})
	if score != 1.0 || reg != 0 {
		t.Fatalf("good-proc = (%.3f, %d); want (1.0, 0)", score, reg)
	}

	// Resolver nil -> fail-closed.
	mnil := ProceduralMetricsVsBaseline(h, nil, tdCfg, 0)
	score, reg = mnil("good-proc", memschema.Version{Major: 1})
	if score >= 0 || reg != rejectRegressions {
		t.Fatalf("resolver nil = (%.3f, %d); deveria reprovar (fail-closed)", score, reg)
	}
}
