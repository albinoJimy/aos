package promotion

import (
	"context"
	"errors"
	"testing"

	eval "github.com/aos-ref/platform/eval"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
)

// evalResolverForTest mapeia ids de skills para candidatos conhecidos-bom/regredidos e
// os golden-sets embebidos da classe skill. Usado para cablar o eval-gate real ao harness
// via [NewEvalGateFromHarness].
func evalResolverForTest(t *testing.T) func(string) (eval.Candidate, []eval.GoldenSet, bool) {
	t.Helper()
	sets, err := eval.EmbeddedSuitesFor(eval.ArtifactSkill)
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
		case "skill.good":
			return eval.GoodSkillCandidate(), sets, true
		case "skill.regressed":
			return regressed, sets, true
		default:
			return nil, nil, false
		}
	}
}

// evalAdapterHarness é uma variante do harness de teste que constrói o eval-gate via
// [NewEvalGateFromHarness] (ligando o eval harness real ao pipeline de promoção).
func evalAdapterHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	gate := NewEvalGateFromHarness(eval.NewHarness(eval.DefaultMinScore), evalResolverForTest(t))
	pipe, err := NewPipeline(h.reg, h.integrity, h.ledger, h.auditStore,
		WithClock(fixedClock()),
		WithEvalGate(gate),
		WithRatifiers(h.ratifiers),
	)
	if err != nil {
		t.Fatalf("NewPipeline com eval-gate cablado: %v", err)
	}
	h.pipe = pipe
	return h
}

// TestPromote_EvalAdapter_ApprovesImprovementAndBlocksRegression prova o cablagem do
// eval-gate via [NewEvalGateFromHarness] (AOS-189): uma skill conhecida-bom é promovida
// (golden-set score 1.0 >= 0.90), mas uma skill regredida (acção unsafe) é bloqueada pelo
// eval-gate e nunca chega a active.
func TestPromote_EvalAdapter_ApprovesImprovementAndBlocksRegression(t *testing.T) {
	t.Parallel()
	c := contract(domain.EgressInternal)
	dig := digest.SHA256Digester{}.Digest(domain.KindSkill, c)

	// (a) Skill conhecida-bom: passa o eval-gate e, com ratificação, promove a active.
	t.Run("melhoria aprovada", func(t *testing.T) {
		t.Parallel()
		h := evalAdapterHarness(t)
		h.mustPublish(h.skillReq("skill.good", ver(1, 0, 0), c))
		res, err := h.pipe.Promote(context.Background(), PromoteRequest{
			ID: "skill.good", Version: ver(1, 0, 0), Ratification: h.ratify("skill.good", ver(1, 0, 0), dig),
		})
		if err != nil {
			t.Fatalf("Promote: %v", err)
		}
		if !res.Eval.Passed || res.Eval.GoldenSetScore != 1.0 {
			t.Fatalf("eval-gate devia passar com score 1.0: %+v", res)
		}
		if !h.isAdmissible("skill.good", ver(1, 0, 0)) {
			t.Fatal("skill aprovada no eval-gate devia ficar active")
		}
		if !h.hasStage(stageEvalPassed) || !h.hasStage(stageRatified) || !h.hasStage(stagePromoted) {
			t.Fatal("todas as transições de governação deviam estar seladas")
		}
	})

	// (b) Skill regredida: reprova no eval-gate (acção unsafe) e NÃO chega a active,
	// mesmo com ratificação válida.
	t.Run("regressao bloqueada", func(t *testing.T) {
		t.Parallel()
		h := evalAdapterHarness(t)
		h.mustPublish(h.skillReq("skill.regressed", ver(1, 0, 0), c))
		_, err := h.pipe.Promote(context.Background(), PromoteRequest{
			ID: "skill.regressed", Version: ver(1, 0, 0), Ratification: h.ratify("skill.regressed", ver(1, 0, 0), dig),
		})
		if !errors.Is(err, ErrEvalGateRejected) {
			t.Fatalf("Promote err = %v, quer ErrEvalGateRejected", err)
		}
		if h.isAdmissible("skill.regressed", ver(1, 0, 0)) {
			t.Fatal("skill regredida NÃO deve chegar a active")
		}
		if !h.hasStage(stageEvalRejected) {
			t.Fatal("rejeição do eval-gate devia estar selada no audit")
		}
		if h.hasStage(stageRatified) || h.hasStage(stagePromoted) {
			t.Fatal("skill rejeitada no eval-gate NÃO deve passar por ratificação/promoção")
		}
	})
}
