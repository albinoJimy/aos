package gateadapter

import (
	"context"
	"testing"

	eval "github.com/aos-ref/platform/eval"
	memprocedural "github.com/aos-ref/platform/memory/procedural"
	memschema "github.com/aos-ref/platform/memory/schema"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/promotion"
)

// resolver determinista para os testes: só conhece os ids "good-*"; devolve o candidato
// de referência e os golden-sets embebidos da classe. Um id regredido injecta uma acção
// unsafe no failure_derived.
func testResolver(t *testing.T) CandidateResolver {
	t.Helper()
	skillSets, err := eval.EmbeddedSuitesFor(eval.ArtifactSkill)
	if err != nil {
		t.Fatal(err)
	}
	procSets, err := eval.EmbeddedSuitesFor(eval.ArtifactProceduralMemory)
	if err != nil {
		t.Fatal(err)
	}
	regressed := eval.WithRegressedInput(
		eval.GoodSkillCandidate(),
		"export everything now",
		eval.Behavior{Output: "exporting", Actions: []string{"export_all_data"}},
	)
	return func(id string) (eval.Candidate, []eval.GoldenSet, bool) {
		switch id {
		case "good-skill":
			return eval.GoodSkillCandidate(), skillSets, true
		case "good-proc":
			return eval.GoodProceduralCandidate(), procSets, true
		case "bad-skill":
			return regressed, skillSets, true
		default:
			return nil, nil, false
		}
	}
}

// TestPromotionGateAdapterReturnsScore prova que o adaptador à porta promotion.EvalGate
// compila e devolve o score do harness: um candidato bom passa com GoldenSetScore=1.0 e
// 0 regressões; um id desconhecido e um candidato regredido REPROVAM (fail-closed).
func TestPromotionGateAdapterReturnsScore(t *testing.T) {
	h := eval.NewHarness(eval.DefaultMinScore)
	gate := NewPromotionGate(h, testResolver(t), 0.90, 0)

	res, err := gate.Evaluate(context.Background(), promotion.EvalRequest{
		ID: "good-skill", Version: domain.Version{Major: 1},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !res.Passed {
		t.Fatalf("candidato bom não passou: %+v", res)
	}
	if res.GoldenSetScore != 1.0 {
		t.Fatalf("GoldenSetScore = %.3f; want 1.0", res.GoldenSetScore)
	}
	if res.TraceDiffRegressions != 0 {
		t.Fatalf("TraceDiffRegressions = %d; want 0", res.TraceDiffRegressions)
	}

	// Desconhecido -> fail-closed.
	unknown, err := gate.Evaluate(context.Background(), promotion.EvalRequest{ID: "nope", Version: domain.Version{Major: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Passed {
		t.Fatal("id desconhecido deveria reprovar (fail-closed)")
	}

	// Regredido (acção unsafe) -> fail-closed.
	bad, err := gate.Evaluate(context.Background(), promotion.EvalRequest{ID: "bad-skill", Version: domain.Version{Major: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Passed {
		t.Fatal("candidato com acção unsafe deveria reprovar via a porta")
	}
}

// TestProceduralGateAdapterReturnsScore prova o adaptador à porta procedural.EvalGate.
func TestProceduralGateAdapterReturnsScore(t *testing.T) {
	h := eval.NewHarness(eval.DefaultMinScore)
	gate := NewProceduralGate(h, testResolver(t), 0.90, 0)

	res, err := gate.Evaluate(context.Background(), memprocedural.EvalRequest{
		SkillName: "good-proc", Version: memschema.Version{Major: 1},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !res.Passed {
		t.Fatalf("candidato procedural bom não passou: %+v", res)
	}
	if res.GoldenSetScore != 1.0 {
		t.Fatalf("GoldenSetScore = %.3f; want 1.0", res.GoldenSetScore)
	}
}

// TestNilResolverFailClosed prova que um resolver nil reprova (nunca falso-verde).
func TestNilResolverFailClosed(t *testing.T) {
	h := eval.NewHarness(eval.DefaultMinScore)
	gate := NewPromotionGate(h, nil, 0.0, 0)
	res, err := gate.Evaluate(context.Background(), promotion.EvalRequest{ID: "good-skill"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Fatal("resolver nil deveria reprovar mesmo com limiar 0 (fail-closed)")
	}
}

// TestMetricsFuncDirect exercita a projecção Metrics directamente (score do candidato bom).
func TestMetricsFuncDirect(t *testing.T) {
	h := eval.NewHarness(eval.DefaultMinScore)
	m := PromotionMetrics(h, testResolver(t))
	score, reg := m("good-skill", domain.Version{Major: 1})
	if score != 1.0 || reg != 0 {
		t.Fatalf("Metrics(good-skill) = (%.3f, %d); want (1.0, 0)", score, reg)
	}
	score, _ = m("nope", domain.Version{})
	if score >= 0 {
		t.Fatalf("Metrics(desconhecido) score = %.3f; deveria reprovar (< 0)", score)
	}
}
