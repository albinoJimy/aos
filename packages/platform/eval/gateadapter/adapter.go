// Package gateadapter fornece adaptadores FINOS entre o eval harness (AOS-114,
// github.com/aos-ref/platform/eval) e as portas Evaluate(...) dos consumidores do
// eval-gate:
//
//   - [github.com/aos-ref/platform/registry/promotion].ThresholdEvalGate (promoção de
//     skills no REG);
//   - [github.com/aos-ref/platform/memory/procedural].ThresholdEvalGate (promoção de
//     memória procedural).
//
// Ambos os consumidores expõem uma [ThresholdEvalGate] cujo ponto de injecção é uma
// função Metrics func(id, v) (goldenScore float64, traceDiffRegressions int) — o godoc
// desses tipos diz que "em produção viria do harness de EPIC-11". É EXACTAMENTE esse
// ponto que este pacote liga: o harness fornece o GoldenSetScore (o success-rate
// agregado sobre os golden-sets do candidato) e a contagem de regressões de
// trace-diffing. Há DOIS caminhos: as funções base [PromotionMetrics]/
// [ProceduralMetrics] devolvem 0 regressões (só o sinal golden-set, AOS-114); as
// variantes [PromotionMetricsVsBaseline]/[ProceduralMetricsVsBaseline] (AOS-115)
// alimentam a contagem REAL de regressões vs uma baseline aprovada — o segundo sinal.
//
// Os construtores concretos ([NewPromotionGate]/[NewProceduralGate]/
// [NewPromotionGateVsBaseline]/[NewProceduralGateVsBaseline]) vivem nos próprios
// pacotes consumidores para evitar dependências cíclicas: estes consumidores importam
// o gateadapter pelas funções Metrics e montam o [ThresholdEvalGate] local.
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
	memschema "github.com/aos-ref/platform/memory/schema"
	"github.com/aos-ref/platform/registry/domain"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
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
	// (este caminho base só carrega o sinal golden-set — o segundo sinal vem do caminho
	// VsBaseline, ver [metricsVsBaseline]).
	return res.AggregateSuccessRate(), 0
}

// PromotionMetrics devolve a função Metrics para o [ThresholdEvalGate] de promoção do
// REG, ligada ao harness. A versão de domínio é ignorada (o candidato é resolvido por
// id; o versionamento é responsabilidade do resolver).
func PromotionMetrics(h eval.Harness, resolve CandidateResolver) func(id string, v domain.Version) (float64, int) {
	return func(id string, _ domain.Version) (float64, int) {
		return metrics(h, resolve, id)
	}
}

// ProceduralMetrics devolve a função Metrics para o [ThresholdEvalGate] de memória
// procedural, ligada ao harness. O nome e a versão de schema são ignorados (o candidato
// é resolvido por nome; o versionamento é responsabilidade do resolver).
func ProceduralMetrics(h eval.Harness, resolve CandidateResolver) func(name string, v memschema.Version) (float64, int) {
	return func(name string, _ memschema.Version) (float64, int) {
		return metrics(h, resolve, name)
	}
}

// ---------------------------------------------------------------------------
// Trace-diffing vs baseline (AOS-115): a contagem REAL de regressões
// ---------------------------------------------------------------------------
//
// As funções base acima devolvem 0 regressões (o placeholder de AOS-114). As variantes
// VsBaseline abaixo alimentam a contagem REAL de regressões de trace-diffing vs uma
// baseline aprovada — o SEGUNDO sinal do eval-gate. São ADITIVAS: não alteram a
// assinatura de [CandidateResolver] nem as funções Metrics existentes; acrescentam um
// [BaselineResolver] (resolve também as baselines por golden-set) e uma
// [otelgenai.TraceDiffConfig] configurável (os limiares ruído-vs-regressão, AC2). O
// ThresholdEvalGate já reprova quando as regressões excedem MaxTraceDiffRegressions —
// aqui só se lhe passa o número real.

// BaselineResolver resolve o id de um artefacto candidato para o [eval.Candidate] sob
// teste, os seus golden-sets E as baselines APROVADAS por golden-set (indexadas por
// [eval.GoldenSet].EvalID). ok=false ⇒ desconhecido ⇒ fail-closed. É a variante ciente
// de baseline do [CandidateResolver] — o wiring liga-a ao registo de artefactos
// aprovados/candidatos; os testes passam um resolver determinista.
type BaselineResolver func(id string) (candidate eval.Candidate, sets []eval.GoldenSet, baselines map[string]eval.Baseline, ok bool)

// metricsVsBaseline corre o harness COM trace-diffing vs baseline e projecta o resultado
// no par (goldenScore, traceDiffRegressions) — devolvendo agora a contagem REAL de
// regressões (não 0). Fail-closed em toda a fronteira: resolver nil/desconhecido, golden
// não-admitido, ou baseline em falta reprovam incondicionalmente. maxRegressions é o
// limiar do gate, propagado para consistência interna; o ThresholdEvalGate reaplica-o
// sobre o número real devolvido.
func metricsVsBaseline(h eval.Harness, resolve BaselineResolver, cfg otelgenai.TraceDiffConfig, maxRegressions int, id string) (float64, int) {
	if resolve == nil {
		return rejectScore, rejectRegressions
	}
	candidate, sets, baselines, ok := resolve(id)
	if !ok || candidate == nil || len(sets) == 0 || baselines == nil {
		return rejectScore, rejectRegressions
	}
	res := h.EvaluateArtifactVsBaseline(context.Background(), sets, candidate, baselines, cfg, maxRegressions)
	if !res.Base.Admitted || res.BaselineMissing {
		// Golden reprovou (unsafe/abaixo do limiar) ou faltou baseline: fail-closed.
		return rejectScore, rejectRegressions
	}
	// Admitido no golden E com baseline para cada set: devolve o success-rate agregado e
	// a contagem REAL de regressões vs baseline (o segundo sinal). Uma regressão
	// significativa acima do máximo do gate reprova o ThresholdEvalGate.
	return res.Base.AggregateSuccessRate(), res.TotalRegressions
}

// PromotionMetricsVsBaseline devolve a função Metrics para o [ThresholdEvalGate] de
// promoção do REG com a contagem REAL de regressões de trace-diffing vs baseline.
func PromotionMetricsVsBaseline(h eval.Harness, resolve BaselineResolver, cfg otelgenai.TraceDiffConfig, maxRegressions int) func(id string, v domain.Version) (float64, int) {
	return func(id string, _ domain.Version) (float64, int) {
		return metricsVsBaseline(h, resolve, cfg, maxRegressions, id)
	}
}

// ProceduralMetricsVsBaseline devolve a função Metrics para o [ThresholdEvalGate] de
// memória procedural com a contagem REAL de regressões de trace-diffing vs baseline.
func ProceduralMetricsVsBaseline(h eval.Harness, resolve BaselineResolver, cfg otelgenai.TraceDiffConfig, maxRegressions int) func(name string, v memschema.Version) (float64, int) {
	return func(name string, _ memschema.Version) (float64, int) {
		return metricsVsBaseline(h, resolve, cfg, maxRegressions, name)
	}
}
