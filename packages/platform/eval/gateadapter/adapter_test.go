package gateadapter

import (
	"testing"

	eval "github.com/aos-ref/platform/eval"
	memschema "github.com/aos-ref/platform/memory/schema"
	"github.com/aos-ref/platform/registry/domain"
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

// TestPromotionMetricsReturnsScore prova que a projecção de métricas de promoção
// compila e devolve o score do harness: um candidato bom passa com
// GoldenSetScore=1.0 e 0 regressões; um id desconhecido e um candidato regredido
// REPROVAM (fail-closed) via score < 0 / regressões no valor de rejeição.
func TestPromotionMetricsReturnsScore(t *testing.T) {
	h := eval.NewHarness(eval.DefaultMinScore)
	m := PromotionMetrics(h, testResolver(t))

	score, reg := m("good-skill", domain.Version{Major: 1})
	if score != 1.0 || reg != 0 {
		t.Fatalf("good-skill = (%.3f, %d); want (1.0, 0)", score, reg)
	}

	// Desconhecido -> fail-closed.
	score, reg = m("nope", domain.Version{Major: 1})
	if score >= 0 || reg != rejectRegressions {
		t.Fatalf("desconhecido = (%.3f, %d); deveria reprovar (score<0, reg=rejeição)", score, reg)
	}

	// Regredido (acção unsafe) -> fail-closed.
	score, reg = m("bad-skill", domain.Version{Major: 1})
	if score >= 0 || reg != rejectRegressions {
		t.Fatalf("bad-skill = (%.3f, %d); deveria reprovar via a porta", score, reg)
	}
}

// TestProceduralMetricsReturnsScore prova a projecção de métricas de memória procedural.
func TestProceduralMetricsReturnsScore(t *testing.T) {
	h := eval.NewHarness(eval.DefaultMinScore)
	m := ProceduralMetrics(h, testResolver(t))

	score, reg := m("good-proc", memschema.Version{Major: 1})
	if score != 1.0 || reg != 0 {
		t.Fatalf("good-proc = (%.3f, %d); want (1.0, 0)", score, reg)
	}
}

// TestNilResolverFailClosed prova que um resolver nil reprova (nunca falso-verde).
func TestNilResolverFailClosed(t *testing.T) {
	h := eval.NewHarness(eval.DefaultMinScore)
	m := PromotionMetrics(h, nil)
	score, reg := m("good-skill", domain.Version{})
	if score >= 0 || reg != rejectRegressions {
		t.Fatalf("resolver nil = (%.3f, %d); deveria reprovar mesmo com limiar 0", score, reg)
	}
}
