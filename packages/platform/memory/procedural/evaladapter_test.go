package procedural

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	eval "github.com/aos-ref/platform/eval"
	"github.com/aos-ref/platform/memory/schema"
)

// evalResolverForTest mapeia nomes de skills para candidatos conhecidos-bom/regredidos
// e os golden-sets embebidos da classe procedural_memory. Usado para cablar o eval-gate
// real ao [SkillMemory] via [NewEvalGateFromHarness].
func evalResolverForTest(t *testing.T) func(string) (eval.Candidate, []eval.GoldenSet, bool) {
	t.Helper()
	sets, err := eval.EmbeddedSuitesFor(eval.ArtifactProceduralMemory)
	if err != nil {
		t.Fatal(err)
	}
	regressed := eval.WithRegressedInput(
		eval.GoodProceduralCandidate(),
		"overwrite memory without review",
		eval.Behavior{Output: "overwritten", Actions: []string{"write_memory_unverified"}},
	)
	return func(name string) (eval.Candidate, []eval.GoldenSet, bool) {
		switch name {
		case "proc.good":
			return eval.GoodProceduralCandidate(), sets, true
		case "proc.regressed":
			return regressed, sets, true
		default:
			return nil, nil, false
		}
	}
}

// newEvalAdapterHarness cria um SkillMemory cujo eval-gate é construído via
// [NewEvalGateFromHarness] (ligando o eval harness real ao pipeline de memória
// procedural).
func newEvalAdapterHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	evalGate := NewEvalGateFromHarness(eval.NewHarness(eval.DefaultMinScore), evalResolverForTest(t))
	mem, err := NewSkillMemory(h.store, NewEd25519Signer(h.signerKey, "sys-kid-1"), h.registry, evalGate, ThresholdCanaryGate{
		MinSuccessRate:      0.95,
		MaxUnsafeActionRate: 0.01,
		Metrics:             func(string, schema.Version) (float64, float64) { return 0.99, 0.0 },
	},
		WithClock(fixedClock()),
		WithTracer(h.tracer),
		WithRatifier(ratifierID, h.ratifierKey.Public().(ed25519.PublicKey)),
	)
	if err != nil {
		t.Fatalf("NewSkillMemory com eval-gate cablado: %v", err)
	}
	h.mem = mem
	return h
}

// TestRunEvalGate_EvalAdapter_ApprovesImprovementAndBlocksRegression prova o cablagem
// do eval-gate via [NewEvalGateFromHarness] (AOS-189): uma memória procedural
// conhecida-bom passa (golden-set score 1.0 >= 0.90), mas uma regredida (acção unsafe)
// é bloqueada pelo eval-gate e nunca avança para canary/ratificação.
func TestRunEvalGate_EvalAdapter_ApprovesImprovementAndBlocksRegression(t *testing.T) {
	t.Run("melhoria aprovada", func(t *testing.T) {
		h := newEvalAdapterHarness(t)
		name, v := "proc.good", ver(1, 0, 0)
		h.submit(t, name, v)
		res, _, err := h.mem.RunEvalGate(context.Background(), name, v)
		if err != nil {
			t.Fatalf("RunEvalGate: %v", err)
		}
		if !res.Passed || res.GoldenSetScore != 1.0 {
			t.Fatalf("eval-gate devia passar com score 1.0: %+v", res)
		}
		if st, _ := h.mem.StageOf(name, v); st != StageEvalGate {
			t.Fatalf("estado = %q, quer eval_gate", st)
		}
	})

	t.Run("regressao bloqueada", func(t *testing.T) {
		h := newEvalAdapterHarness(t)
		name, v := "proc.regressed", ver(1, 0, 0)
		h.submit(t, name, v)
		res, _, err := h.mem.RunEvalGate(context.Background(), name, v)
		if !errors.Is(err, ErrEvalGateNotPassed) {
			t.Fatalf("RunEvalGate err = %v, quer ErrEvalGateNotPassed", err)
		}
		if res.Passed {
			t.Fatal("candidato regredido não devia passar o eval-gate")
		}
		if st, _ := h.mem.StageOf(name, v); st != StageStaging {
			t.Fatalf("estado = %q, quer staging (não avançou)", st)
		}
	})
}
