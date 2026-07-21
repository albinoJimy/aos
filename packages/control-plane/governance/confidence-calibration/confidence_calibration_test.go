package confcalib

import (
	"context"
	"errors"
	"testing"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
	"github.com/aos-ref/substrate/redaction"
)

// staticSource é uma fonte de correcções de teste: devolve TODOS os registos guardados
// (de vários contextos), para provar que é o CORE que filtra por contexto semelhante — e
// não a fonte. Ignora o contextKey de propósito.
type staticSource struct {
	records []CorrectionRecord
	calls   int
	err     error
}

func (s *staticSource) Corrections(_ context.Context, _ string) ([]CorrectionRecord, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.records, nil
}

func newScanner() *redaction.Engine { return redaction.NewEngine(nil) }

// ---------------------------------------------------------------------------
// TESTE (a) — SELECTIVIDADE (AC1): incerteza mostrada SÓ acima do sinal; AUSENTE
// quando a confiança é alta. Não-tautológico: cobre os dois lados da fronteira.
// ---------------------------------------------------------------------------

func TestUncertaintySelectivity(t *testing.T) {
	p := NewUncertaintyPolicy(0.7)

	cases := []struct {
		name       string
		result     otelgenai.EvaluationResult
		wantShow   bool
		wantReason UncertaintyReason
	}{
		{
			name:     "confianca alta: pass + score acima do limiar -> SEM nota",
			result:   otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.95},
			wantShow: false,
		},
		{
			name:     "confianca alta: pass + score exactamente no limiar -> SEM nota",
			result:   otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.70},
			wantShow: false,
		},
		{
			name:       "score baixo: pass mas abaixo do limiar -> nota low_score",
			result:     otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.55},
			wantShow:   true,
			wantReason: ReasonLowScore,
		},
		{
			name:       "veredicto fail: independentemente do score -> nota verdict_fail",
			result:     otelgenai.EvaluationResult{Verdict: otelgenai.EvalFail, Score: 0.99},
			wantShow:   true,
			wantReason: ReasonVerdictFail,
		},
		{
			name:       "veredicto vazio (desconhecido): fail-closed -> nota verdict_fail",
			result:     otelgenai.EvaluationResult{Verdict: "", Score: 0.99},
			wantShow:   true,
			wantReason: ReasonVerdictFail,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotShow := p.ShouldSurface(tc.result)
			if gotShow != tc.wantShow {
				t.Fatalf("ShouldSurface = %v, quer %v", gotShow, tc.wantShow)
			}
			note := p.NoteFor(tc.result)
			if tc.wantShow {
				if note == nil {
					t.Fatalf("NoteFor devolveu nil mas esperava-se nota (%s)", tc.wantReason)
				}
				if note.Reason != tc.wantReason {
					t.Fatalf("Reason = %q, quer %q", note.Reason, tc.wantReason)
				}
				// CONSUMO: o Score da nota é EXACTAMENTE o do result (não recalculado).
				if note.Score != tc.result.Score {
					t.Fatalf("note.Score = %v, quer %v (deve ser o Score consumido)", note.Score, tc.result.Score)
				}
				if note.Message == "" {
					t.Fatalf("nota sem Message legível")
				}
			} else if note != nil {
				t.Fatalf("confiança alta devia dar nota nil (selectivo), obteve %+v", note)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TESTE (b) — HISTÓRICO (AC2): SummaryFor agrupa por contexto semelhante e devolve a
// contagem/tipo correctos; correcções de OUTRO contexto NÃO aparecem.
// ---------------------------------------------------------------------------

func TestCorrectionHistoryGroupsByContext(t *testing.T) {
	const ctxA = "agent-x/capability-plan/domain-finance"
	const ctxB = "agent-y/capability-exec/domain-ops"
	src := &staticSource{records: []CorrectionRecord{
		{Context: ctxA, Kind: "scope_narrowing", RedactedText: "reduz o âmbito"},
		{Context: ctxA, Kind: "factual", RedactedText: "corrige a data"},
		{Context: ctxA, Kind: "scope_narrowing", RedactedText: "não toques em prod"},
		{Context: ctxB, Kind: "factual", RedactedText: "outro contexto"}, // deve ser excluído
	}}
	h := NewCorrectionHistory(src)

	sum, err := h.SummaryFor(context.Background(), ctxA)
	if err != nil {
		t.Fatalf("SummaryFor: %v", err)
	}
	if sum.Count != 3 {
		t.Fatalf("Count = %d, quer 3 (correcções de ctxB excluídas)", sum.Count)
	}
	if sum.ByKind["scope_narrowing"] != 2 || sum.ByKind["factual"] != 1 {
		t.Fatalf("ByKind = %v, quer {scope_narrowing:2, factual:1}", sum.ByKind)
	}
	if len(sum.Records) != 3 {
		t.Fatalf("Records = %d, quer 3", len(sum.Records))
	}
	for _, r := range sum.Records {
		if r.Context != ctxA {
			t.Fatalf("registo apresentado de contexto errado: %q", r.Context)
		}
	}

	// Contexto sem correcções -> sumário vazio (não o de outro contexto).
	empty, err := h.SummaryFor(context.Background(), "agent-z/none/none")
	if err != nil {
		t.Fatalf("SummaryFor vazio: %v", err)
	}
	if empty.Count != 0 || len(empty.Records) != 0 {
		t.Fatalf("contexto sem correcções devia dar sumário vazio, obteve %+v", empty)
	}
}

func TestCorrectionHistorySourceError(t *testing.T) {
	sentinel := errors.New("boom")
	h := NewCorrectionHistory(&staticSource{err: sentinel})
	if _, err := h.SummaryFor(context.Background(), "ctx"); !errors.Is(err, sentinel) {
		t.Fatalf("erro da fonte devia propagar, obteve %v", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE (c) — CONSUMO (AC3): a superfície usa result.Score/Verdict directamente, sem
// recálculo. Prova que Calibrate é função PURA do EvaluationResult dado.
// ---------------------------------------------------------------------------

func TestCalibrateConsumesSignalNoRecompute(t *testing.T) {
	tr := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	h := NewCorrectionHistory(&staticSource{})
	cal := New(NewUncertaintyPolicy(0.7), h, tr, WithRunID("run-1"))

	// Determinismo/pureza: a MESMA entrada dá SEMPRE a mesma saída de incerteza, e a
	// saída é exactamente a política aplicada ao Score/Verdict dado — sem re-avaliação.
	low := otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.4}
	high := otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.9}

	c1, err := cal.Calibrate(context.Background(), low, "ctx")
	if err != nil {
		t.Fatalf("Calibrate low: %v", err)
	}
	c2, err := cal.Calibrate(context.Background(), low, "ctx")
	if err != nil {
		t.Fatalf("Calibrate low#2: %v", err)
	}
	if !c1.UncertaintyShown() || !c2.UncertaintyShown() {
		t.Fatalf("Score 0.4 < 0.7 devia mostrar incerteza")
	}
	if c1.Uncertainty.Score != low.Score || c2.Uncertainty.Score != low.Score {
		t.Fatalf("nota não reflecte o Score consumido: %v/%v", c1.Uncertainty.Score, c2.Uncertainty.Score)
	}
	// Pureza: duas chamadas com a mesma entrada -> mesma decisão de selectividade.
	if c1.UncertaintyShown() != c2.UncertaintyShown() {
		t.Fatalf("Calibrate não é determinista/pura")
	}

	cHigh, err := cal.Calibrate(context.Background(), high, "ctx")
	if err != nil {
		t.Fatalf("Calibrate high: %v", err)
	}
	if cHigh.UncertaintyShown() {
		t.Fatalf("Score 0.9 >= 0.7 e pass -> confiança alta, sem incerteza")
	}

	// Prova de CONSUMO directo: alterar SÓ o Score do result vira a decisão, sem qualquer
	// outra fonte de sinal (não há runner/replay a interferir).
	boundaryBelow := otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.699}
	boundaryAt := otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.7}
	if got, _ := cal.Calibrate(context.Background(), boundaryBelow, "ctx"); !got.UncertaintyShown() {
		t.Fatalf("0.699 < 0.7 devia mostrar incerteza")
	}
	if got, _ := cal.Calibrate(context.Background(), boundaryAt, "ctx"); got.UncertaintyShown() {
		t.Fatalf("0.7 >= 0.7 não devia mostrar incerteza")
	}
}

// ---------------------------------------------------------------------------
// TESTE (d) — AUSÊNCIA DE PII (AC5): um CorrectionRecord com PII no texto é REDIGIDO
// (Engine.ScanText(histórico apresentado) == []) e os spans NUNCA contêm o texto.
// ---------------------------------------------------------------------------

func TestNoPIIInHistoryOrSpans(t *testing.T) {
	const ctx = "agent-x/cap/dom"
	// Texto de correcção com PII em claro (email, cartão de crédito, IBAN) — como se um
	// adaptador tivesse falhado a redigir. O core TEM de o minimizar.
	piiEmail := "contacta john.doe@example.com sobre o caso"
	piiCard := "cartão 4111 1111 1111 1111 recusado"
	src := &staticSource{records: []CorrectionRecord{
		{Context: ctx, Kind: "factual", RedactedText: piiEmail},
		{Context: ctx, Kind: "factual", RedactedText: piiCard},
	}}
	h := NewCorrectionHistory(src)
	scanner := newScanner()

	sum, err := h.SummaryFor(context.Background(), ctx)
	if err != nil {
		t.Fatalf("SummaryFor: %v", err)
	}
	// Sanidade: o scanner DETECTA a PII no texto original (senão o teste seria vacuoso).
	if len(scanner.ScanText(piiEmail)) == 0 {
		t.Fatalf("teste vacuoso: o scanner não detecta a PII do texto original")
	}
	// AC5: o histórico APRESENTADO passa ScanText == [] em CADA CAMPO de cada registo
	// (não só o texto livre RedactedText, mas também os rótulos estruturados Context/Kind
	// — defesa-em-profundidade: nenhum campo apresentado pode conter PII em claro).
	for _, r := range sum.Records {
		presented := r.Context + " " + r.Kind + " " + r.RedactedText
		if findings := scanner.ScanText(presented); len(findings) != 0 {
			t.Fatalf("PII em claro no histórico apresentado: %q -> %v", presented, findings)
		}
	}

	// Spans: emitir a calibração e provar que NENHUM atributo contém o texto da correcção.
	tr := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	cal := New(NewUncertaintyPolicy(0.7), h, tr, WithRunID("run-1"))
	result := otelgenai.EvaluationResult{Verdict: otelgenai.EvalFail, Score: 0.2}
	c, err := cal.Calibrate(context.Background(), result, ctx)
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	spans := tr.SpansByOperation(OpCalibrationInteraction)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span de calibração, obteve %d", len(spans))
	}
	s := spans[0]
	if !s.Ended {
		t.Fatalf("span não foi fechado")
	}
	// A contagem/selectividade estão no span; o TEXTO não.
	if s.Attributes[AttrCorrectionCount] != 2 {
		t.Fatalf("correction_count = %v, quer 2", s.Attributes[AttrCorrectionCount])
	}
	if s.Attributes[AttrUncertaintyShown] != true {
		t.Fatalf("uncertainty_shown devia ser true (verdict fail)")
	}
	for k, v := range s.Attributes {
		str, ok := v.(string)
		if !ok {
			continue
		}
		// Nenhum atributo pode conter PII nem fragmentos do texto da correcção.
		if len(scanner.ScanText(str)) != 0 {
			t.Fatalf("atributo de span %q contém PII: %q", k, str)
		}
		if containsAny(str, "john.doe", "4111", "example.com") {
			t.Fatalf("atributo de span %q contém texto da correcção: %q", k, str)
		}
	}
	// E o objecto de calibração também não expõe PII no que apresenta.
	for _, r := range c.Corrections.Records {
		if containsAny(r.RedactedText, "john.doe", "4111", "example.com") {
			t.Fatalf("Calibration expõe texto de correcção com PII: %q", r.RedactedText)
		}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if idx := indexOf(s, sub); idx >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	n, m := len(s), len(sub)
	if m == 0 {
		return 0
	}
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Extras de cobertura: defaults, opções de política inválidas, tracer/history nil.
// ---------------------------------------------------------------------------

func TestPolicyDefaultsAndBounds(t *testing.T) {
	if NewUncertaintyPolicy(0).Threshold != DefaultUncertaintyThreshold {
		t.Fatalf("limiar 0 devia cair no default")
	}
	if NewUncertaintyPolicy(1.5).Threshold != DefaultUncertaintyThreshold {
		t.Fatalf("limiar >1 devia cair no default")
	}
	if NewUncertaintyPolicy(0.9).Threshold != 0.9 {
		t.Fatalf("limiar válido devia ser preservado")
	}
	// UncertaintyPolicy{} zero (limiar 0) cai no default via effectiveThreshold.
	var zero UncertaintyPolicy
	if zero.ShouldSurface(otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.8}) {
		t.Fatalf("com o default (0.7), score 0.8 pass não deveria sinalizar")
	}
}

func TestCalibrateNilTracerAndNilHistory(t *testing.T) {
	// tracer nil -> NoopTracer (sem panics); history nil -> sumário vazio.
	cal := New(NewUncertaintyPolicy(0.7), nil, nil)
	c, err := cal.Calibrate(context.Background(), otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.5}, "ctx")
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	if !c.UncertaintyShown() {
		t.Fatalf("score 0.5 devia mostrar incerteza")
	}
	if c.Corrections.Count != 0 || c.Corrections.ByKind == nil {
		t.Fatalf("history nil devia dar sumário vazio inicializado, obteve %+v", c.Corrections)
	}
}

func TestCorrectionSourceFuncAndNilSource(t *testing.T) {
	called := false
	src := CorrectionSourceFunc(func(_ context.Context, _ string) ([]CorrectionRecord, error) {
		called = true
		return []CorrectionRecord{{Context: "c", Kind: "k", RedactedText: "ok"}}, nil
	})
	h := NewCorrectionHistory(src)
	sum, err := h.SummaryFor(context.Background(), "c")
	if err != nil || !called || sum.Count != 1 {
		t.Fatalf("CorrectionSourceFunc não foi exercida correctamente: called=%v sum=%+v err=%v", called, sum, err)
	}

	// source nil -> sumário vazio, sem panic.
	hNil := NewCorrectionHistory(nil)
	if s, err := hNil.SummaryFor(context.Background(), "c"); err != nil || s.Count != 0 {
		t.Fatalf("source nil devia dar sumário vazio, obteve %+v err=%v", s, err)
	}
}

func TestCustomEngineInjection(t *testing.T) {
	// Injecção de engine/política à medida: RemoveAllPolicy explícita.
	eng := redaction.NewEngine(nil)
	pol := redaction.RemoveAllPolicy("custom-v1")
	h := NewCorrectionHistoryWithEngine(&staticSource{records: []CorrectionRecord{
		{Context: "c", Kind: "k", RedactedText: "email a@b.com aqui"},
	}}, eng, pol)
	sum, err := h.SummaryFor(context.Background(), "c")
	if err != nil {
		t.Fatalf("SummaryFor: %v", err)
	}
	if len(newScanner().ScanText(sum.Records[0].RedactedText)) != 0 {
		t.Fatalf("engine à medida não redigiu: %q", sum.Records[0].RedactedText)
	}
	// engine nil no construtor with-engine -> cai no default.
	hDef := NewCorrectionHistoryWithEngine(&staticSource{}, nil, redaction.Policy{})
	if hDef == nil {
		t.Fatalf("construtor devia tolerar engine nil")
	}
}
