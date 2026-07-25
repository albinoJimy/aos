package procedural

import (
	"github.com/aos-ref/platform/eval"
	"github.com/aos-ref/platform/eval/gateadapter"
)

// NewEvalGateFromHarness constrói um [EvalGate] concreto ligado ao eval harness
// (AOS-114) e a um resolver de candidatos/golden-sets. É o ponto de cablagem
// canónico para que o [SkillMemory] aplique a admissão comportamental a skills
// auto-escritas: golden-set score >= 0.90 e zero regressões de trace-diffing.
//
// O resolver mapeia o nome da skill (SkillName) para o [eval.Candidate] sob teste
// e os respectivos golden-sets. Um nome desconhecido ou um artefacto não admitido
// reprova incondicionalmente (fail-closed via gateadapter.rejectScore /
// rejectRegressions).
func NewEvalGateFromHarness(h eval.Harness, resolve gateadapter.CandidateResolver) EvalGate {
	return ThresholdEvalGate{
		MinGoldenSetScore:       0.90,
		MaxTraceDiffRegressions: 0,
		Metrics:                 gateadapter.ProceduralMetrics(h, resolve),
	}
}
