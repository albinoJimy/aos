package eval

import (
	"context"
	"testing"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// TestEvaluationSpanLinkedToTrace prova o AC3: o resultado é emitido como span
// gen_ai.evaluation.result (OpEvaluation) LIGADO ao trace da trajectória avaliada (via
// o atributo explícito aos.eval.target_trace_id), com veredicto/score/dataset gravados.
func TestEvaluationSpanLinkedToTrace(t *testing.T) {
	ctx := context.Background()
	h := NewHarness(DefaultMinScore)
	gs := SkillGoldenSet()
	se := h.EvaluateSet(ctx, gs, GoodSkillCandidate())

	tr := otelgenai.NewRecordingTracer(nil)
	sc := h.Emit(ctx, tr, se.Result)
	if !sc.IsValid() {
		t.Fatal("Emit devolveu SpanContext inválido para um resultado ligado ao trace")
	}

	evals := tr.SpansByOperation(otelgenai.OpEvaluation)
	if len(evals) != 1 {
		t.Fatalf("esperado 1 span %s, obtido %d", otelgenai.OpEvaluation, len(evals))
	}
	sd := evals[0].ToSpanData()
	if got := attrStr(sd, otelgenai.AttrEvalTargetTraceID); got != se.Result.TargetTraceIDHex() {
		t.Fatalf("target_trace_id no span = %q; want %q", got, se.Result.TargetTraceIDHex())
	}
	if got := attrStr(sd, otelgenai.AttrEvalVerdict); got != string(se.Result.Verdict) {
		t.Fatalf("verdict no span = %q; want %q", got, se.Result.Verdict)
	}
	if got := attrStr(sd, otelgenai.AttrEvalDataset); got != string(se.Result.Dataset) {
		t.Fatalf("dataset no span = %q; want %q", got, se.Result.Dataset)
	}
}

// TestRecordEvaluationRefusesUnlinked prova o fail-closed de ligação (AC3): sem
// trace_id (TargetTraceID all-zero E ctx sem SpanContext propagado), a emissão é
// RECUSADA e devolve um SpanContext inválido — nunca um span de eval auto-referente
// enganoso.
func TestRecordEvaluationRefusesUnlinked(t *testing.T) {
	h := NewHarness(DefaultMinScore)
	tr := otelgenai.NewRecordingTracer(nil)
	unlinked := otelgenai.EvaluationResult{
		Suite:   "skill",
		EvalID:  "x",
		Dataset: otelgenai.EvalDatasetGolden,
		Verdict: otelgenai.EvalPass,
		Score:   1.0,
		// TargetTraceID deixado a zero — sem ligação.
	}
	sc := h.Emit(context.Background(), tr, unlinked)
	if sc.IsValid() {
		t.Fatal("Emit deveria RECUSAR (SpanContext inválido) um resultado sem trace-alvo")
	}
	if len(tr.SpansByOperation(otelgenai.OpEvaluation)) != 0 {
		t.Fatal("nenhum span de eval deveria ter sido emitido sem ligação")
	}
}

// TestEmitConsumeRoundTrip fecha o ciclo emit→consume: o span emitido reconstrói o
// EvaluationResult via EvaluationResultFromSpanData, idêntico no que respeita à
// identidade da avaliação (suite/eval-id/dataset/veredicto/score/trace-alvo).
func TestEmitConsumeRoundTrip(t *testing.T) {
	ctx := context.Background()
	h := NewHarness(DefaultMinScore)
	gs := ProceduralGoldenSet()
	se := h.EvaluateSet(ctx, gs, GoodProceduralCandidate())

	tr := otelgenai.NewRecordingTracer(nil)
	if sc := h.Emit(ctx, tr, se.Result); !sc.IsValid() {
		t.Fatal("emissão recusada inesperadamente")
	}
	sd := tr.SpansByOperation(otelgenai.OpEvaluation)[0].ToSpanData()

	got, ok := otelgenai.EvaluationResultFromSpanData(sd)
	if !ok {
		t.Fatal("EvaluationResultFromSpanData: não reconheceu um span de eval")
	}
	want := se.Result
	if got.Verdict != want.Verdict || got.Dataset != want.Dataset || got.Suite != want.Suite ||
		got.EvalID != want.EvalID || got.Score != want.Score || got.TargetTraceID != want.TargetTraceID {
		t.Fatalf("round-trip emit→consume divergente:\n got=%+v\n want=%+v", got, want)
	}
}

// TestEmitSameTraceLinkage cobre a via de ligação por MESMO trace: sem TargetTraceID
// explícito mas com o SpanContext da trajectória propagado no ctx, RecordEvaluation
// aceita (herda o trace) — o outro ramo de linked em RecordEvaluation.
func TestEmitSameTraceLinkage(t *testing.T) {
	h := NewHarness(DefaultMinScore)
	tr := otelgenai.NewRecordingTracer(nil)

	// Propaga um SpanContext de trajectória válido no ctx.
	traj := otelgenai.SpanContext{TraceID: [16]byte{1, 2, 3, 4}, SpanID: [8]byte{9}}
	ctx := otelgenai.ContextWithSpanContext(context.Background(), traj)

	res := otelgenai.EvaluationResult{
		Suite: "skill", EvalID: "y", Dataset: otelgenai.EvalDatasetGolden,
		Verdict: otelgenai.EvalPass, Score: 1.0, // sem TargetTraceID explícito
	}
	sc := h.Emit(ctx, tr, res)
	if !sc.IsValid() {
		t.Fatal("Emit deveria aceitar via ligação por mesmo-trace (ctx propaga SpanContext)")
	}
}
