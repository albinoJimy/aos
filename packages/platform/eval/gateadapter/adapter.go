// Package gateadapter fornece adaptadores FINOS entre o eval harness (AOS-114,
// github.com/aos-ref/platform/eval) e as portas Evaluate(...) dos consumidores JÁ
// existentes do eval-gate:
//
//   - [github.com/aos-ref/platform/registry/promotion].EvalGate (promoção de skills no REG);
//   - [github.com/aos-ref/platform/memory/procedural].EvalGate (promoção de memória procedural).
//
// Ambos os consumidores expõem uma [ThresholdEvalGate] cujo ponto de injecção é uma
// função Metrics func(id, v) (goldenScore float64, traceDiffRegressions int) — o godoc
// desses tipos diz que "em produção viria do harness de EPIC-11". É EXACTAMENTE esse
// ponto que este pacote liga: o harness fornece o GoldenSetScore (o success-rate
// agregado sobre os golden-sets do candidato) e, por agora, 0 regressões de
// trace-diffing (AOS-115, fora do âmbito de AOS-114).
//
// NÃO se reimplementa a ratificação, o canary nem os gates dos consumidores — só se
// injecta a métrica. Este pacote é isolado do core do harness (que importa apenas o
// módulo folha otel-genai) precisamente para manter o core enxuto: só os adaptadores
// puxam as dependências de registry/memory.
//
// # Fail-closed
//
// Um id que o resolver não conheça, ou um artefacto que o harness NÃO admita (acção
// unsafe ou success-rate abaixo do limiar), produz uma métrica que REPROVA o
// ThresholdEvalGate incondicionalmente (score abaixo de qualquer limiar >= 0 E
// regressões acima de qualquer tolerância). Assim, a garantia de zero-unsafe do harness
// — que o ThresholdEvalGate não modela por si — é preservada através da fronteira.
package gateadapter

import (
	"context"

	eval "github.com/aos-ref/platform/eval"
	memprocedural "github.com/aos-ref/platform/memory/procedural"
	memschema "github.com/aos-ref/platform/memory/schema"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/promotion"
)

// rejectScore é um score que reprova qualquer limiar de golden-set >= 0 (o
// ThresholdEvalGate exige score >= MinGoldenSetScore). Fail-closed para artefactos
// desconhecidos ou não-admitidos pelo harness.
const rejectScore = -1.0

// rejectRegressions é um nº de regressões que excede qualquer tolerância razoável
// (defesa-em-profundidade: mesmo com MinGoldenSetScore 0, a métrica reprova).
const rejectRegressions = 1 << 20

// CandidateResolver resolve o id de um artefacto candidato para o [eval.Candidate] sob
// teste e os seus golden-sets (ambos os datasets). ok=false ⇒ desconhecido ⇒
// fail-closed. É a fronteira injectável: o wiring (EPIC-08) liga-o ao registo de
// artefactos candidatos; os testes passam um resolver determinista.
type CandidateResolver func(id string) (candidate eval.Candidate, sets []eval.GoldenSet, ok bool)

// metrics corre o harness sobre o candidato resolvido e projecta o resultado no par
// (goldenScore, traceDiffRegressions) esperado pelas portas Metrics. É o núcleo
// partilhado pelos dois adaptadores. Fail-closed em toda a fronteira.
func metrics(h eval.Harness, resolve CandidateResolver, id string) (float64, int) {
	if resolve == nil {
		return rejectScore, rejectRegressions
	}
	candidate, sets, ok := resolve(id)
	if !ok || candidate == nil || len(sets) == 0 {
		return rejectScore, rejectRegressions
	}
	res := h.EvaluateArtifact(context.Background(), sets, candidate)
	if !res.Admitted {
		// Não admitido (acção unsafe ou abaixo do limiar): reprova incondicionalmente,
		// preservando a garantia de zero-unsafe do harness através da porta.
		return rejectScore, rejectRegressions
	}
	// Admitido: o GoldenSetScore é o success-rate agregado; 0 regressões de trace-diff
	// (AOS-115 fora do âmbito — o segundo sinal liga-se depois via otelgenai.TraceDiff).
	return res.AggregateSuccessRate(), 0
}

// PromotionMetrics devolve a função Metrics para
// [github.com/aos-ref/platform/registry/promotion].ThresholdEvalGate, ligada ao harness.
// A versão de domínio é ignorada (o candidato é resolvido por id; o versionamento é
// responsabilidade do resolver).
func PromotionMetrics(h eval.Harness, resolve CandidateResolver) func(id string, v domain.Version) (float64, int) {
	return func(id string, _ domain.Version) (float64, int) {
		return metrics(h, resolve, id)
	}
}

// NewPromotionGate constrói uma [promotion.ThresholdEvalGate] já ligada ao harness — o
// eval-gate concreto de promoção de skills do REG, com o GoldenSetScore vindo do
// harness. minGoldenSetScore/maxTraceDiffRegressions são os limiares do gate.
func NewPromotionGate(h eval.Harness, resolve CandidateResolver, minGoldenSetScore float64, maxTraceDiffRegressions int) promotion.ThresholdEvalGate {
	return promotion.ThresholdEvalGate{
		MinGoldenSetScore:       minGoldenSetScore,
		MaxTraceDiffRegressions: maxTraceDiffRegressions,
		Metrics:                 PromotionMetrics(h, resolve),
	}
}

// ProceduralMetrics devolve a função Metrics para
// [github.com/aos-ref/platform/memory/procedural].ThresholdEvalGate, ligada ao harness.
func ProceduralMetrics(h eval.Harness, resolve CandidateResolver) func(name string, v memschema.Version) (float64, int) {
	return func(name string, _ memschema.Version) (float64, int) {
		return metrics(h, resolve, name)
	}
}

// NewProceduralGate constrói uma [memprocedural.ThresholdEvalGate] já ligada ao harness
// — o eval-gate concreto de promoção de memória procedural, com o GoldenSetScore vindo
// do harness.
func NewProceduralGate(h eval.Harness, resolve CandidateResolver, minGoldenSetScore float64, maxTraceDiffRegressions int) memprocedural.ThresholdEvalGate {
	return memprocedural.ThresholdEvalGate{
		MinGoldenSetScore:       minGoldenSetScore,
		MaxTraceDiffRegressions: maxTraceDiffRegressions,
		Metrics:                 ProceduralMetrics(h, resolve),
	}
}
