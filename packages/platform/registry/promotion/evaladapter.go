package promotion

import (
	"github.com/aos-ref/platform/eval"
	"github.com/aos-ref/platform/eval/gateadapter"
)

// NewEvalGateFromHarness constrói um [EvalGate] concreto ligado ao eval harness
// (AOS-114) e a um resolver de candidatos/golden-sets. É o ponto de cablagem
// canónico para que o [Pipeline] aplique a admissão comportamental a skills
// auto-escritas: golden-set score >= 0.90 e zero regressões de trace-diffing.
//
// O resolver mapeia o id da skill (usado no [Registry]) para o [eval.Candidate] sob
// teste e os respectivos golden-sets. Um id desconhecido ou um artefacto não admitido
// reprova incondicionalmente (fail-closed via gateadapter.rejectScore /
// rejectRegressions).
func NewEvalGateFromHarness(h eval.Harness, resolve gateadapter.CandidateResolver) EvalGate {
	return ThresholdEvalGate{
		MinGoldenSetScore:       0.90,
		MaxTraceDiffRegressions: 0,
		Metrics:                 gateadapter.PromotionMetrics(h, resolve),
	}
}
