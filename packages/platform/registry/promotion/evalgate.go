package promotion

import (
	"context"

	"github.com/aos-ref/platform/registry/domain"
)

// EvalRequest é o pedido ao eval-gate: avalia a (skill, versão) candidata contra o
// golden-set e por trace-diffing vs a versão BASELINE (a active corrente da linha
// de versões; zero se for a primeira promoção). É a fronteira que reutiliza o
// harness de EPIC-11 sem o reimplementar.
type EvalRequest struct {
	// ID e Version identificam a skill candidata a promoção.
	ID      string
	Version domain.Version
	// Baseline é a versão active corrente (a prod actual) contra a qual se faz o
	// trace-diffing. IsZero() indica que não há baseline (primeira promoção).
	Baseline domain.Version
	// Digest é o digest do conteúdo canonicalizado (âncora de integridade do
	// artefacto avaliado; liga a avaliação aos bytes exactos).
	Digest string
}

// EvalResult é o veredicto do eval-gate. Passed=true EXIGE golden-set acima do
// limiar E regressões de trace-diffing dentro do tolerado (ADR-012). É o que é
// emitido no atributo de span gen_ai.evaluation.result.
type EvalResult struct {
	// Passed é o veredicto binário: só uma skill com Passed=true pode ir a active.
	Passed bool
	// GoldenSetScore é a pontuação sobre o golden-set curado.
	GoldenSetScore float64
	// TraceDiffRegressions é o nº de regressões detectadas vs a baseline.
	TraceDiffRegressions int
	// Baseline é a versão contra a qual se avaliou (ecoada para o audit/span).
	Baseline domain.Version
	// Detail é uma razão legível (diagnóstico; nunca contém segredos).
	Detail string
}

// EvalGate é a PORTA do eval-gate (harness de EPIC-11). A implementação real
// (golden-set curado, trace-diffing sobre trajectórias) pertence a EPIC-11; aqui
// vive apenas o contrato e uma implementação de referência determinista. Um
// Evaluate que devolva erro é tratado como falha de avaliação (fail-closed).
type EvalGate interface {
	Evaluate(ctx context.Context, req EvalRequest) (EvalResult, error)
}

// ThresholdEvalGate é a implementação de REFERÊNCIA de [EvalGate]: aplica limiares
// determinísticos a métricas INJECTADAS (sem I/O, sem rand, sem relógio). O campo
// Metrics devolve o golden-set score e o nº de regressões de trace-diffing da
// (skill, versão) — em produção viria do harness de EPIC-11; em testes é uma função
// pura. Fail-closed: sem Metrics injectado, RECUSA sempre.
type ThresholdEvalGate struct {
	// MinGoldenSetScore é o limiar mínimo de golden-set para passar.
	MinGoldenSetScore float64
	// MaxTraceDiffRegressions é o nº máximo tolerado de regressões de trace-diffing.
	MaxTraceDiffRegressions int
	// Metrics devolve (goldenScore, traceDiffRegressions) da (id, versão) avaliada.
	Metrics func(id string, v domain.Version) (goldenScore float64, traceDiffRegressions int)
}

// Evaluate implementa [EvalGate]. Determinista e puro sobre as métricas injectadas.
// Fail-closed: sem Metrics, devolve Passed=false com detalhe explícito (nunca um
// falso verde por omissão de wiring).
func (g ThresholdEvalGate) Evaluate(_ context.Context, req EvalRequest) (EvalResult, error) {
	if g.Metrics == nil {
		return EvalResult{Passed: false, Baseline: req.Baseline, Detail: "sem metrics: fail-closed"}, nil
	}
	score, regressions := g.Metrics(req.ID, req.Version)
	passed := score >= g.MinGoldenSetScore && regressions <= g.MaxTraceDiffRegressions
	detail := "aprovado"
	if !passed {
		detail = "abaixo do limiar (golden-set/trace-diff)"
	}
	return EvalResult{
		Passed:               passed,
		GoldenSetScore:       score,
		TraceDiffRegressions: regressions,
		Baseline:             req.Baseline,
		Detail:               detail,
	}, nil
}
