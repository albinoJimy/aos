package eval

import (
	"context"
	"fmt"
	"strings"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Trace-diffing vs baseline (AOS-115, EPIC-11): o SEGUNDO sinal do eval-gate.
//
// Um artefacto candidato pode passar as métricas AGREGADAS do golden-set (success-
// rate/zero-unsafe, AOS-114) e ainda assim mudar o comportamento PASSO-A-PASSO — uma
// tool trocada, uma ordem diferente, um salto de custo/tokens. Essa regressão
// SILENCIOSA só o diff da árvore de spans do candidato contra uma BASELINE aprovada
// (o mesmo input) revela. Este ficheiro COMPÕE o [otelgenai.TraceDiff] robusto do
// módulo folha (NÃO o reimplementa) e acrescenta a dimensão VEREDICTO/RESULTADO ao
// NÍVEL do eval — que o TraceDiff, focado na trajectória, não cobre.
//
// # Limiares (RUÍDO vs REGRESSÃO — AC2, sem falso-positivo por não-determinismo)
//
// A classificação de uma diferença como ruído tolerável vs regressão significativa é
// a de [otelgenai.TraceDiffConfig]: um |Δcusto| <= CostToleranceMicroUSD e um |Δtokens|
// <= TokenTolerance são RUÍDO (não sinalizados); uma troca/reordenação de tool é
// SEMPRE significativa. O TraceDiff NORMALIZA trace_id/span_id/timestamps (ignora-os),
// pelo que só divergências de COMPORTAMENTO afloram — a base do "sem falso-positivo por
// não-determinismo". O valor-zero de config é ESTRITO (qualquer Δ > 0 conta); alargue os
// limiares para tolerar a variação esperada. baseline == candidato ⇒ zero regressões.

// Baseline é a trajectória APROVADA (o [otelgenai.EvalTarget] de spans) de um golden-set,
// capturada de um candidato aprovado/conhecido-bom, MAIS o seu veredicto de eval. É o
// referencial contra o qual um candidato-sob-teste é comparado: as suas acções/custo/
// tokens (via TraceDiff) e o seu veredicto (a dimensão de resultado). Determinista: a
// mesma captura (mesmo golden-set + mesmo candidato aprovado) dá sempre a mesma baseline.
type Baseline struct {
	// EvalID identifica o golden-set (suite+versão+dataset) que a baseline cobre — a
	// chave que casa uma baseline com o set avaliado.
	EvalID string
	// Target é a trajectória de spans aprovada (o lado esquerdo do TraceDiff).
	Target otelgenai.EvalTarget
	// Result é o veredicto de eval da baseline aprovada (a dimensão de resultado do diff).
	Result otelgenai.EvaluationResult
}

// CaptureBaseline conduz o candidato APROVADO sobre o golden-set e captura a sua
// trajectória de spans e veredicto como [Baseline] (reutiliza [Harness.buildTarget] via
// [Harness.EvaluateSet] — não há caminho de captura paralelo). Determinista: a baseline
// é reprodutível. No modelo de referência, o candidato aprovado é o conhecido-bom e o
// candidato-sob-teste é uma variante possivelmente regressida.
func (h Harness) CaptureBaseline(ctx context.Context, gs GoldenSet, approved Candidate) Baseline {
	se := h.EvaluateSet(ctx, gs, approved)
	return Baseline{EvalID: gs.EvalID(), Target: se.Target, Result: se.Result}
}

// BaselineDiff é o relatório ESTRUTURADO (AC1/AC4) do diff de um candidato contra uma
// baseline sobre o mesmo golden-set: as regressões de trajectória do [otelgenai.TraceDiff]
// (acções/sequência/custo/tokens, já NORMALIZADAS) MAIS a dimensão de VEREDICTO ao nível
// do eval. Legível e accionável: cada [otelgenai.Regression] traz o passo divergente e a
// natureza da divergência (Detail).
type BaselineDiff struct {
	// Suite e EvalID ecoam a origem avaliada (a classe de artefacto e o golden-set).
	Suite  string
	EvalID string
	// BaselineTraceID e CandidateTraceID são os traces comparados (o candidato liga a
	// evidência à SUA execução). Normalizados pelo TraceDiff — aqui só para referência.
	BaselineTraceID  [16]byte
	CandidateTraceID [16]byte
	// Regressions são as regressões de TRAJECTÓRIA (tool_sequence/cost/tokens) do
	// otelgenai.TraceDiff. Vazio ⇒ trajectória sem regressão.
	Regressions []otelgenai.Regression
	// BaselineVerdict e CandidateVerdict são os veredictos de eval de cada lado.
	BaselineVerdict  otelgenai.EvalVerdict
	CandidateVerdict otelgenai.EvalVerdict
	// VerdictRegressed é a dimensão de RESULTADO que o TraceDiff não cobre (documentada
	// honestamente): true sse a baseline PASSA e o candidato REPROVA. É uma regressão de
	// resultado — o candidato deixou de cumprir uma expectativa que a baseline cumpria.
	VerdictRegressed bool
}

// TotalRegressions é a contagem AGREGADA de regressões significativas: as de trajectória
// (TraceDiff) MAIS a de veredicto (se houver). É o número REAL que alimenta o eval-gate
// (o placeholder "0" que AOS-114 devolvia) — o [otelgenai.EvalGate]/ThresholdEvalGate
// reprova quando excede o máximo tolerado.
func (d BaselineDiff) TotalRegressions() int {
	n := len(d.Regressions)
	if d.VerdictRegressed {
		n++
	}
	return n
}

// HasRegression reporta se há QUALQUER regressão significativa (trajectória ou veredicto).
func (d BaselineDiff) HasRegression() bool { return d.TotalRegressions() > 0 }

// CandidateTraceIDHex devolve o trace_id da trajectória candidata em hex de 32 dígitos,
// ou "" se all-zero (não-ligado). É a referência que liga a evidência do diff à execução.
func (d BaselineDiff) CandidateTraceIDHex() string { return traceIDHex(d.CandidateTraceID) }

// Summary é a descrição LEGÍVEL e accionável do diff (AC4), útil para eval-driven
// development: enumera cada regressão com o passo divergente e a sua natureza. Vazio de
// regressões ⇒ "sem regressões". Determinista (ordem fixa: trajectória depois veredicto).
func (d BaselineDiff) Summary() string {
	if !d.HasRegression() {
		return fmt.Sprintf("%s: sem regressões vs baseline", d.EvalID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d regressão(ões) vs baseline\n", d.EvalID, d.TotalRegressions())
	for _, r := range d.Regressions {
		if r.Step >= 0 {
			fmt.Fprintf(&b, "  - [%s passo %d] %s\n", r.Kind, r.Step, r.Detail)
		} else {
			fmt.Fprintf(&b, "  - [%s agregado] %s\n", r.Kind, r.Detail)
		}
	}
	if d.VerdictRegressed {
		fmt.Fprintf(&b, "  - [veredicto] baseline %q → candidato %q (regressão de resultado)\n",
			d.BaselineVerdict, d.CandidateVerdict)
	}
	return b.String()
}

// diffSet compõe o diff estruturado de uma [SetEvaluation] já computada contra a
// baseline: COMPÕE otelgenai.TraceDiff sobre os spans (normalizado) e acrescenta a
// dimensão de veredicto. É o núcleo partilhado por [Harness.DiffAgainstBaseline] e
// [Harness.EvaluateArtifactVsBaseline] — sem re-conduzir o candidato.
func (h Harness) diffSet(se SetEvaluation, baseline Baseline, cfg otelgenai.TraceDiffConfig) BaselineDiff {
	regs := otelgenai.TraceDiff(baseline.Target.Spans, se.Target.Spans, cfg)
	return BaselineDiff{
		Suite:            se.Result.Suite,
		EvalID:           se.Result.EvalID,
		BaselineTraceID:  baseline.Target.TraceID,
		CandidateTraceID: se.Target.TraceID,
		Regressions:      regs,
		BaselineVerdict:  baseline.Result.Verdict,
		CandidateVerdict: se.Result.Verdict,
		VerdictRegressed: baseline.Result.Passed() && !se.Result.Passed(),
	}
}

// DiffAgainstBaseline conduz o candidato sobre o golden-set e diffa-o contra a baseline
// aprovada (AC1/AC2/AC4). COMPÕE [otelgenai.TraceDiff] (acções/sequência/custo/tokens,
// NORMALIZADO) e acrescenta a dimensão de veredicto. Determinista: correr 2x dá o mesmo
// diff (o TraceDiff ignora trace_id/timestamps; o resto é função pura dos spans).
func (h Harness) DiffAgainstBaseline(ctx context.Context, gs GoldenSet, candidate Candidate, baseline Baseline, cfg otelgenai.TraceDiffConfig) BaselineDiff {
	se := h.EvaluateSet(ctx, gs, candidate)
	return h.diffSet(se, baseline, cfg)
}

// BaselineArtifactEvaluation é o resultado de avaliar um candidato sobre TODOS os seus
// golden-sets COM trace-diffing vs baseline (AC3): a avaliação de golden-set de AOS-114
// (Base), os diffs por set e a contagem agregada de regressões. Admitted é fail-closed:
// exige o golden-set (Base.Admitted: success-rate/zero-unsafe) E regressões agregadas
// <= MaxRegressions E uma baseline presente para CADA set.
type BaselineArtifactEvaluation struct {
	// Base é a avaliação de golden-set (AOS-114): success-rate/unsafe/admissão de golden.
	Base ArtifactEvaluation
	// Diffs são os diffs vs baseline por set (na ordem dada).
	Diffs []BaselineDiff
	// TotalRegressions é a soma das regressões significativas sobre todos os sets — o
	// número REAL que o gateadapter alimenta ao ThresholdEvalGate.
	TotalRegressions int
	// BaselineMissing é true se ALGUM set não tinha baseline: fail-closed, não se pode
	// certificar ausência de regressão, logo bloqueia a admissão.
	BaselineMissing bool
	// MaxRegressions é o limiar de regressões tolerado (parâmetro da avaliação).
	MaxRegressions int
	// Admitted é o veredicto AGREGADO fail-closed (golden E regressões <= max E baseline
	// presente para cada set). Uma regressão SIGNIFICATIVA acima do máximo BLOQUEIA.
	Admitted bool
}

// EvaluateArtifactVsBaseline corre AMBOS os sinais do eval-gate (AC3): o golden-set de
// AOS-114 E o trace-diffing vs baseline deste ticket. Admitted exige as métricas do
// golden-set (via [Harness.EvaluateArtifact]) E que as regressões agregadas vs baseline
// não excedam maxRegressions E uma baseline para cada set (fail-closed). baselines é
// indexado por [GoldenSet.EvalID]. Determinista.
func (h Harness) EvaluateArtifactVsBaseline(ctx context.Context, sets []GoldenSet, candidate Candidate, baselines map[string]Baseline, cfg otelgenai.TraceDiffConfig, maxRegressions int) BaselineArtifactEvaluation {
	gate := h.Gate()
	out := BaselineArtifactEvaluation{
		Base:           ArtifactEvaluation{Sets: make([]SetEvaluation, 0, len(sets)), Admitted: len(sets) > 0},
		Diffs:          make([]BaselineDiff, 0, len(sets)),
		MaxRegressions: maxRegressions,
	}
	for _, gs := range sets {
		se := h.EvaluateSet(ctx, gs, candidate)
		out.Base.Sets = append(out.Base.Sets, se)
		out.Base.TotalCases += se.Report.Total
		out.Base.TotalSuccesses += se.Report.Successes
		out.Base.TotalUnsafe += se.Report.UnsafeCount
		if !gate.Admit(se.Result) {
			out.Base.Admitted = false
		}
		bl, ok := baselines[gs.EvalID()]
		if !ok {
			// Fail-closed: sem baseline não se pode certificar ausência de regressão.
			out.BaselineMissing = true
			continue
		}
		diff := h.diffSet(se, bl, cfg)
		out.Diffs = append(out.Diffs, diff)
		out.TotalRegressions += diff.TotalRegressions()
	}
	out.Admitted = out.Base.Admitted && !out.BaselineMissing && out.TotalRegressions <= maxRegressions
	return out
}

// ---------------------------------------------------------------------------
// Evidência do diff LIGADA ao trace (AC/DoD)
// ---------------------------------------------------------------------------

// Atributos PRÓPRIOS deste harness para a evidência de trace-diff (não poluem a semconv
// do módulo folha otel-genai). Transportam a contagem/natureza das regressões e a
// referência à baseline. Não são segredos — são contadores/rótulos de avaliação.
const (
	// spanTraceDiff é o Name do span de evidência de trace-diff (nome próprio; NÃO um
	// gen_ai.operation.name novo — não se altera o vocabulário do módulo folha).
	spanTraceDiff = "aos.eval.trace_diff"
	// attrEvalRegressionCount — aos.eval.regression_count: o nº de regressões significativas.
	attrEvalRegressionCount = "aos.eval.regression_count"
	// attrEvalRegressionKinds — aos.eval.regression_kinds: as naturezas (kinds) das
	// regressões, por ordem, separadas por vírgula (vazio se nenhuma).
	attrEvalRegressionKinds = "aos.eval.regression_kinds"
	// attrEvalVerdictRegressed — aos.eval.verdict_regressed: a dimensão de resultado
	// (true sse baseline passa e candidato reprova).
	attrEvalVerdictRegressed = "aos.eval.verdict_regressed"
	// attrEvalBaselineTraceID — aos.eval.baseline_trace_id: o trace_id (hex 32) da
	// trajectória BASELINE contra a qual se diffou.
	attrEvalBaselineTraceID = "aos.eval.baseline_trace_id"
)

// EmitBaselineDiff EMITE o [BaselineDiff] como um span de evidência LIGADO ao trace da
// trajectória CANDIDATA (via otelgenai.AttrEvalTargetTraceID — o mesmo atributo de
// ligação eval→trajectória), carregando a contagem/natureza das regressões como
// atributos aos.eval.*. Devolve o [otelgenai.SpanContext] do span. Fail-closed de
// ligação (no molde de [otelgenai.RecordEvaluation]): se o diff não tiver
// CandidateTraceID E ctx não propagar um SpanContext válido, RECUSA e devolve um
// SpanContext inválido — nunca uma evidência órfã/enganosa. Compõe a porta
// [otelgenai.Tracer] — não escreve OTLP à mão.
func (h Harness) EmitBaselineDiff(ctx context.Context, tr otelgenai.Tracer, diff BaselineDiff) otelgenai.SpanContext {
	parent, hasParent := otelgenai.SpanContextFromContext(ctx)
	candHex := traceIDHex(diff.CandidateTraceID)
	linked := (hasParent && parent.IsValid()) || candHex != ""
	if !linked {
		return otelgenai.SpanContext{}
	}
	_, span := tr.StartSpan(ctx, spanTraceDiff)
	if diff.Suite != "" {
		span.SetAttribute(otelgenai.AttrEvalSuite, diff.Suite)
	}
	if diff.EvalID != "" {
		span.SetAttribute(otelgenai.AttrEvalID, diff.EvalID)
	}
	if candHex != "" {
		span.SetAttribute(otelgenai.AttrEvalTargetTraceID, candHex)
	}
	if b := traceIDHex(diff.BaselineTraceID); b != "" {
		span.SetAttribute(attrEvalBaselineTraceID, b)
	}
	span.SetAttribute(attrEvalRegressionCount, int64(diff.TotalRegressions()))
	span.SetAttribute(attrEvalRegressionKinds, regressionKinds(diff))
	span.SetAttribute(attrEvalVerdictRegressed, diff.VerdictRegressed)
	sc := span.SpanContext()
	span.End()
	return sc
}

// regressionKinds devolve os kinds das regressões (trajectória + veredicto) por ordem,
// separados por vírgula — a natureza legível carregada na evidência.
func regressionKinds(diff BaselineDiff) string {
	kinds := make([]string, 0, diff.TotalRegressions())
	for _, r := range diff.Regressions {
		kinds = append(kinds, string(r.Kind))
	}
	if diff.VerdictRegressed {
		kinds = append(kinds, "verdict")
	}
	return strings.Join(kinds, ",")
}

// traceIDHex devolve o trace_id em hex minúsculo de 32 dígitos, ou "" se all-zero
// (não-ligado). Espelha a convenção de [otelgenai.EvaluationResult.TargetTraceIDHex].
func traceIDHex(id [16]byte) string {
	if id == ([16]byte{}) {
		return ""
	}
	return otelgenai.SpanContext{TraceID: id}.TraceIDHex()
}
