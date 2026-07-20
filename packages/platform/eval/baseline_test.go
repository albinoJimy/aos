package eval

import (
	"context"
	"reflect"
	"strings"
	"testing"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// toleranceCfg é o config de trace-diff usado nos testes: tolera até 100 micro-USD e
// 100 tokens de variação (o limiar que separa RUÍDO de REGRESSÃO). Acima disso é
// regressão significativa.
var toleranceCfg = otelgenai.TraceDiffConfig{CostToleranceMicroUSD: 100, TokenTolerance: 100}

// approvedSkill é o candidato APROVADO de referência: o conhecido-bom com usage
// plausível uniforme (habilita a dimensão custo/tokens do trace-diffing).
func approvedSkill() Candidate {
	return WithUsage(GoodSkillCandidate(), 100, 50, 1000)
}

// captureSkillBaselines captura as baselines aprovadas para ambos os datasets skill.
func captureSkillBaselines(t *testing.T, h Harness) (map[string]Baseline, []GoldenSet) {
	t.Helper()
	sets, err := EmbeddedSuitesFor(ArtifactSkill)
	if err != nil {
		t.Fatal(err)
	}
	m := make(map[string]Baseline, len(sets))
	for _, gs := range sets {
		m[gs.EvalID()] = h.CaptureBaseline(context.Background(), gs, approvedSkill())
	}
	return m, sets
}

// TestBaselineIdenticalNoRegressions prova o AC5 (sem falso-positivo) e o AC1: um
// candidato IDÊNTICO ao aprovado (baseline == candidato) produz ZERO regressões — nem de
// trajectória nem de veredicto — e é ADMITIDO. Normaliza o não-determinismo: mesmo com o
// trace_id sensível ao usage, o TraceDiff ignora-o.
func TestBaselineIdenticalNoRegressions(t *testing.T) {
	ctx := context.Background()
	h := NewHarness(DefaultMinScore)
	gs := SkillGoldenSet()

	baseline := h.CaptureBaseline(ctx, gs, approvedSkill())
	diff := h.DiffAgainstBaseline(ctx, gs, approvedSkill(), baseline, toleranceCfg)

	if diff.TotalRegressions() != 0 {
		t.Fatalf("baseline==candidato deveria dar 0 regressões; got %d\n%s", diff.TotalRegressions(), diff.Summary())
	}
	if diff.VerdictRegressed {
		t.Fatal("baseline==candidato não deveria regredir o veredicto")
	}

	// Admissão agregada: sem regressão, admitido (golden E trace-diff).
	baselines, sets := captureSkillBaselines(t, h)
	res := h.EvaluateArtifactVsBaseline(ctx, sets, approvedSkill(), baselines, toleranceCfg, 0)
	if !res.Admitted {
		t.Fatalf("candidato idêntico ao aprovado deveria ser admitido (base=%v reg=%d missing=%v)",
			res.Base.Admitted, res.TotalRegressions, res.BaselineMissing)
	}
	if res.TotalRegressions != 0 {
		t.Fatalf("esperadas 0 regressões agregadas; got %d", res.TotalRegressions)
	}
}

// TestBaselineToolSwapDetectedAndBlocks prova o AC5 (regressão de TOOL) e o AC3: um
// candidato que ACRESCENTA uma acção benigna (não-requerida, não-proibida) PASSA o
// golden-set — o output e as required/forbidden mantêm-se — mas o trace-diffing apanha a
// divergência de sequência de tools (RegressionToolSequence) e BLOQUEIA a admissão. Isola
// o SEGUNDO sinal do eval-gate: bloqueia mesmo com o golden-set verde.
func TestBaselineToolSwapDetectedAndBlocks(t *testing.T) {
	ctx := context.Background()
	h := NewHarness(DefaultMinScore)

	baselines, sets := captureSkillBaselines(t, h)

	// Acrescenta "audit_log" APÓS a acção requerida no caso de escalada: golden continua
	// a passar (create_ticket presente, output ok, sem forbidden), mas a sequência de
	// tools diverge da baseline.
	candidate := WithUsage(WithRegressedInput(
		GoodSkillCandidate(),
		"escalate to human",
		Behavior{Output: "escalated to human agent", Actions: []string{"create_ticket", "audit_log"}},
	), 100, 50, 1000)

	// Diff sobre o golden-set: RegressionToolSequence detectada.
	gs := SkillGoldenSet()
	diff := h.DiffAgainstBaseline(ctx, gs, candidate, baselines[gs.EvalID()], toleranceCfg)
	if !hasKind(diff.Regressions, otelgenai.RegressionToolSequence) {
		t.Fatalf("esperada RegressionToolSequence; got %+v\n%s", diff.Regressions, diff.Summary())
	}

	// Admissão: o golden-set ADMITE (o candidato passa as métricas), mas a regressão de
	// sequência BLOQUEIA a admissão agregada.
	res := h.EvaluateArtifactVsBaseline(ctx, sets, candidate, baselines, toleranceCfg, 0)
	if !res.Base.Admitted {
		t.Fatalf("o golden-set deveria ADMITIR (a acção acrescentada não é unsafe nem quebra required); base=%v", res.Base.Admitted)
	}
	if res.TotalRegressions == 0 {
		t.Fatal("esperada >=1 regressão de trajectória")
	}
	if res.Admitted {
		t.Fatal("uma regressão de sequência de tools deveria BLOQUEAR a admissão (segundo sinal)")
	}
}

// TestBaselineCostJumpDetectedAndBlocks prova o AC5 (SALTO DE CUSTO) e o AC3: um
// candidato com o DOBRO do custo por caso (mesmas acções, mesmo output) PASSA o
// golden-set mas o trace-diffing apanha o salto de custo (RegressionCost) e BLOQUEIA.
// Isola a dimensão custo do segundo sinal.
func TestBaselineCostJumpDetectedAndBlocks(t *testing.T) {
	ctx := context.Background()
	h := NewHarness(DefaultMinScore)

	baselines, sets := captureSkillBaselines(t, h)

	// Dobra o custo por caso (1000 -> 2000), acima do limiar de 100 micro-USD; tokens
	// iguais (sem regressão de tokens); acções/output iguais (golden passa).
	costJump := WithUsage(GoodSkillCandidate(), 100, 50, 2000)

	gs := SkillGoldenSet()
	diff := h.DiffAgainstBaseline(ctx, gs, costJump, baselines[gs.EvalID()], toleranceCfg)
	if !hasKind(diff.Regressions, otelgenai.RegressionCost) {
		t.Fatalf("esperada RegressionCost; got %+v\n%s", diff.Regressions, diff.Summary())
	}
	if hasKind(diff.Regressions, otelgenai.RegressionToolSequence) {
		t.Fatal("o salto de custo não deveria alterar a sequência de tools")
	}

	res := h.EvaluateArtifactVsBaseline(ctx, sets, costJump, baselines, toleranceCfg, 0)
	if !res.Base.Admitted {
		t.Fatalf("o golden-set deveria ADMITIR (só o custo mudou); base=%v", res.Base.Admitted)
	}
	if res.Admitted {
		t.Fatal("um salto de custo acima do limiar deveria BLOQUEAR a admissão")
	}
}

// TestBaselineWithinToleranceNoFalsePositive prova o AC5 (sem falso-positivo por
// não-determinismo): uma variação de custo DENTRO do limiar tolerável não gera regressão
// e o candidato é ADMITIDO. É o contraponto directo do salto de custo.
func TestBaselineWithinToleranceNoFalsePositive(t *testing.T) {
	ctx := context.Background()
	h := NewHarness(DefaultMinScore)

	baselines, sets := captureSkillBaselines(t, h)

	// Varia o custo em 5 micro-USD por caso (<= tolerância agregada de 100); tokens iguais.
	withinTol := WithUsage(GoodSkillCandidate(), 100, 50, 1005)

	gs := SkillGoldenSet()
	diff := h.DiffAgainstBaseline(ctx, gs, withinTol, baselines[gs.EvalID()], toleranceCfg)
	if diff.TotalRegressions() != 0 {
		t.Fatalf("variação dentro do limiar não deveria gerar regressão; got %d\n%s", diff.TotalRegressions(), diff.Summary())
	}

	res := h.EvaluateArtifactVsBaseline(ctx, sets, withinTol, baselines, toleranceCfg, 0)
	if !res.Admitted {
		t.Fatalf("uma variação dentro do limiar deveria ser ADMITIDA (sem falso-positivo); reg=%d", res.TotalRegressions)
	}
}

// TestBaselineVerdictRegressionBlocks prova a dimensão de VEREDICTO/RESULTADO que o
// TraceDiff não cobre: se a baseline PASSA e o candidato REPROVA (output errado), o diff
// marca VerdictRegressed e conta como regressão — bloqueando a admissão.
func TestBaselineVerdictRegressionBlocks(t *testing.T) {
	ctx := context.Background()
	h := NewHarness(DefaultMinScore)
	gs := SkillGoldenSet()

	baseline := h.CaptureBaseline(ctx, gs, approvedSkill())

	// Output errado no reset-password: falha a expectativa de substring -> veredicto fail.
	regressed := WithUsage(WithRegressedInput(
		GoodSkillCandidate(),
		"reset password",
		Behavior{Output: "internal error", Actions: []string{"send_email"}},
	), 100, 50, 1000)

	diff := h.DiffAgainstBaseline(ctx, gs, regressed, baseline, toleranceCfg)
	if !diff.VerdictRegressed {
		t.Fatal("baseline passa e candidato reprova: esperado VerdictRegressed")
	}
	if diff.BaselineVerdict != otelgenai.EvalPass || diff.CandidateVerdict != otelgenai.EvalFail {
		t.Fatalf("veredictos inesperados: base=%q cand=%q", diff.BaselineVerdict, diff.CandidateVerdict)
	}
	if diff.TotalRegressions() == 0 {
		t.Fatal("a regressão de veredicto deveria contar para o total")
	}
}

// TestBaselineDiffReadable prova o AC4: o diff é LEGÍVEL e accionável — o Summary mostra
// o passo divergente e a natureza, e cada Regression carrega um Detail não-vazio.
func TestBaselineDiffReadable(t *testing.T) {
	ctx := context.Background()
	h := NewHarness(DefaultMinScore)
	gs := SkillGoldenSet()

	baseline := h.CaptureBaseline(ctx, gs, approvedSkill())
	costJump := WithUsage(GoodSkillCandidate(), 100, 50, 2000)
	diff := h.DiffAgainstBaseline(ctx, gs, costJump, baseline, toleranceCfg)

	summary := diff.Summary()
	if !strings.Contains(summary, "regressão") {
		t.Fatalf("Summary não legível: %q", summary)
	}
	if !strings.Contains(summary, "custo") {
		t.Fatalf("Summary deveria descrever o salto de custo: %q", summary)
	}
	for _, r := range diff.Regressions {
		if r.Detail == "" {
			t.Fatalf("regressão %+v sem Detail accionável", r)
		}
	}
}

// TestBaselineDiffDeterministic prova o determinismo (DoD): correr o MESMO diff 2x dá
// exactamente o MESMO resultado estruturado (o TraceDiff normaliza trace_id/timestamps).
func TestBaselineDiffDeterministic(t *testing.T) {
	ctx := context.Background()
	h := NewHarness(DefaultMinScore)
	gs := SkillGoldenSet()
	baseline := h.CaptureBaseline(ctx, gs, approvedSkill())
	costJump := WithUsage(GoodSkillCandidate(), 100, 50, 2000)

	a := h.DiffAgainstBaseline(ctx, gs, costJump, baseline, toleranceCfg)
	b := h.DiffAgainstBaseline(ctx, gs, costJump, baseline, toleranceCfg)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("diff não determinista:\n a=%+v\n b=%+v", a, b)
	}
}

// TestBaselineDiffEvidenceLinkedToTrace prova a EVIDÊNCIA ligada ao trace (AC/DoD): o
// diff é emitido como span de evidência ligado ao trace da trajectória CANDIDATA, com a
// contagem/natureza das regressões em atributos aos.eval.*. Fail-closed: um diff sem
// trace-alvo (e sem ctx) RECUSA a emissão.
func TestBaselineDiffEvidenceLinkedToTrace(t *testing.T) {
	ctx := context.Background()
	h := NewHarness(DefaultMinScore)
	gs := SkillGoldenSet()
	baseline := h.CaptureBaseline(ctx, gs, approvedSkill())
	costJump := WithUsage(GoodSkillCandidate(), 100, 50, 2000)
	diff := h.DiffAgainstBaseline(ctx, gs, costJump, baseline, toleranceCfg)

	tr := otelgenai.NewRecordingTracer(nil)
	sc := h.EmitBaselineDiff(ctx, tr, diff)
	if !sc.IsValid() {
		t.Fatal("EmitBaselineDiff devolveu SpanContext inválido para um diff ligado")
	}
	spans := tr.SpansByOperation(spanTraceDiff)
	if len(spans) != 1 {
		t.Fatalf("esperado 1 span de evidência %q, obtido %d", spanTraceDiff, len(spans))
	}
	sd := spans[0].ToSpanData()
	if got := attrStr(sd, otelgenai.AttrEvalTargetTraceID); got != diff.CandidateTraceIDHex() {
		t.Fatalf("target_trace_id = %q; want %q (ligado ao trace candidato)", got, diff.CandidateTraceIDHex())
	}
	if v, ok := sd.Attribute(attrEvalRegressionCount); !ok || v.(int64) != int64(diff.TotalRegressions()) {
		t.Fatalf("regression_count no span = %v; want %d", v, diff.TotalRegressions())
	}
	if got := attrStr(sd, attrEvalRegressionKinds); !strings.Contains(got, string(otelgenai.RegressionCost)) {
		t.Fatalf("regression_kinds = %q; deveria conter %q", got, otelgenai.RegressionCost)
	}

	// Fail-closed: diff sem trace-alvo e sem ctx -> recusa.
	empty := BaselineDiff{EvalID: "x"}
	if sc := h.EmitBaselineDiff(context.Background(), tr, empty); sc.IsValid() {
		t.Fatal("EmitBaselineDiff deveria RECUSAR um diff sem trace-alvo (fail-closed)")
	}
	if len(tr.SpansByOperation(spanTraceDiff)) != 1 {
		t.Fatal("nenhum span adicional deveria ter sido emitido para um diff não-ligado")
	}
}

// hasKind reporta se alguma regressão tem o kind dado.
func hasKind(regs []otelgenai.Regression, kind otelgenai.RegressionKind) bool {
	for _, r := range regs {
		if r.Kind == kind {
			return true
		}
	}
	return false
}
