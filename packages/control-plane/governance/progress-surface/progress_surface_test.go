package progresssurface

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/budget"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// --- Helpers de teste: spans `chat` com custo/tokens conhecidos --------------------

// fixedTrace é um trace_id determinista (16 bytes) para os testes.
var fixedTrace = [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}

func fixedTraceHex() string { return hex.EncodeToString(fixedTrace[:]) }

// chatSpan constrói um SpanData `chat` com os tokens/custo dados, no fixedTrace.
func chatSpan(inTok, outTok, costMicroUSD int64) otelgenai.SpanData {
	return otelgenai.SpanData{
		Name:        otelgenai.OpChat,
		SpanContext: otelgenai.SpanContext{TraceID: fixedTrace},
		Attributes: []otelgenai.KeyValue{
			{Key: otelgenai.AttrOperationName, Value: otelgenai.OpChat},
			{Key: otelgenai.AttrInputTokens, Value: inTok},
			{Key: otelgenai.AttrOutputTokens, Value: outTok},
			{Key: otelgenai.AttrCostMicroUSD, Value: costMicroUSD},
		},
	}
}

// toolSpan é um execute_tool (sem custo de modelo) — a agregação deve ignorá-lo.
func toolSpan() otelgenai.SpanData {
	return otelgenai.SpanData{
		Name:        otelgenai.OpExecuteTool,
		SpanContext: otelgenai.SpanContext{TraceID: fixedTrace},
		Attributes: []otelgenai.KeyValue{
			{Key: otelgenai.AttrOperationName, Value: otelgenai.OpExecuteTool},
			{Key: otelgenai.AttrCostMicroUSD, Value: int64(999_999)},
		},
	}
}

// --- Spies das portas --------------------------------------------------------------

// spyBudgetReader devolve um Limit/Available fixos e CONTA as leituras — nunca é mutado
// pela superfície (a superfície só LÊ). Regista se algum método de escrita foi chamado
// (não existe nenhum — a interface é só de leitura, o que já prova o desacoplamento).
type spyBudgetReader struct {
	limit                  budget.Amount
	available              budget.Amount
	limitCalls, availCalls int
}

func (s *spyBudgetReader) Limit(_ context.Context, _ string) (budget.Amount, error) {
	s.limitCalls++
	return s.limit, nil
}
func (s *spyBudgetReader) Available(_ context.Context, _ string) (budget.Amount, error) {
	s.availCalls++
	return s.available, nil
}

// spyExtender prova que OptionExtend delega — regista a chamada e o pedido recebido.
type spyExtender struct {
	called  int
	lastReq ExtensionRequest
	outcome ExtensionOutcome
	err     error
}

func (s *spyExtender) RequestExtension(_ context.Context, req ExtensionRequest) (ExtensionOutcome, error) {
	s.called++
	s.lastReq = req
	return s.outcome, s.err
}

// spyDegrader prova que o timeout degrada — regista a razão.
type spyDegrader struct {
	called     int
	lastReason string
	err        error
}

func (s *spyDegrader) Degrade(_ context.Context, reason string) error {
	s.called++
	s.lastReason = reason
	return s.err
}

// stubReflector devolve um progresso fixo.
type stubReflector struct{ snap ProgressSnapshot }

func (s stubReflector) Snapshot() ProgressSnapshot { return s.snap }

// --- (a) Burn-down: sem recontabilizar (AC4) ---------------------------------------

func TestComputeBurndown_MatchesAggregateByTrace_NoReaccounting(t *testing.T) {
	spans := []otelgenai.SpanData{
		chatSpan(100, 50, 1_200_000),
		chatSpan(200, 80, 3_400_000),
		toolSpan(), // ignorado pela agregação (sem custo de modelo)
	}
	traceID := fixedTraceHex()
	limit := budget.Amount{Tokens: 10_000, CostMicroUSD: 10_000_000}

	bd := ComputeBurndown(spans, traceID, limit)

	// PROVA DIRECTA de AC4: o custo do burn-down == a soma pública dos spans, byte-a-byte.
	want := otelgenai.AggregateByTrace(spans)[traceID]
	if bd.Consumed.CostMicroUSD != want.CostMicroUSD {
		t.Fatalf("custo do burn-down recontabilizado: got %d, AggregateByTrace %d", bd.Consumed.CostMicroUSD, want.CostMicroUSD)
	}
	if bd.Consumed.Tokens != want.TotalTokens() {
		t.Fatalf("tokens do burn-down recontabilizados: got %d, AggregateByTrace %d", bd.Consumed.Tokens, want.TotalTokens())
	}
	// Sanidade: 1.2M+3.4M = 4.6M micro-USD; 430 tokens.
	if want.CostMicroUSD != 4_600_000 {
		t.Fatalf("custo agregado inesperado: %d", want.CostMicroUSD)
	}
	if bd.Consumed.Tokens != 430 {
		t.Fatalf("tokens agregados inesperados: %d", bd.Consumed.Tokens)
	}
	// Fracção = max(4.6M/10M, 430/10000) = 0.46.
	if bd.Fraction < 0.4599 || bd.Fraction > 0.4601 {
		t.Fatalf("fracção inesperada: %v", bd.Fraction)
	}
}

func TestComputeBurndown_ZeroLimit_FailSafeFraction(t *testing.T) {
	spans := []otelgenai.SpanData{chatSpan(100, 100, 5_000_000)}
	bd := ComputeBurndown(spans, fixedTraceHex(), budget.Amount{})
	if bd.Fraction != 0 {
		t.Fatalf("limite nulo devia dar fracção 0 (fail-safe), got %v", bd.Fraction)
	}
}

// --- (b) Limiar: a ~80% dispara o prompt com 3 opções (AC2) ------------------------

func TestEvaluate_Threshold_FiresPromptWithThreeOptions(t *testing.T) {
	reader := &spyBudgetReader{limit: budget.Amount{CostMicroUSD: 10_000_000}}
	tr := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	s := New(reader, &spyExtender{}, &spyDegrader{}, stubReflector{}, tr, WithRunID("run-1"))

	// 8.5M de 10M = 0.85 >= 0.80 -> dispara.
	spans := []otelgenai.SpanData{chatSpan(0, 0, 8_500_000)}
	ev, err := s.Evaluate(context.Background(), spans, fixedTraceHex(), "tree-1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if ev.Prompt == nil {
		t.Fatalf("a ~85%% do orçamento o prompt devia disparar")
	}
	if ev.State != PromptPrompting {
		t.Fatalf("estado do prompt inesperado: %v", ev.State)
	}
	opts := ev.Prompt.Options
	if len(opts) != 3 {
		t.Fatalf("esperadas 3 opções, got %d", len(opts))
	}
	got := map[ExhaustionOption]bool{}
	for _, o := range opts {
		got[o] = true
	}
	if !got[OptionExtend] || !got[OptionSummarizeStop] || !got[OptionAbort] {
		t.Fatalf("as 3 opções (extend/summarize_stop/abort) não estão todas presentes: %v", opts)
	}
	// Span do prompt emitido, ligado ao run, sem segredos.
	promptSpans := tr.SpansByOperation(OpExhaustionPrompt)
	if len(promptSpans) != 1 {
		t.Fatalf("esperado 1 span de prompt, got %d", len(promptSpans))
	}
	if promptSpans[0].Attributes[otelgenai.AttrRunID] != "run-1" {
		t.Fatalf("span do prompt sem run_id correcto: %v", promptSpans[0].Attributes[otelgenai.AttrRunID])
	}
}

func TestEvaluate_BelowThreshold_NoPrompt(t *testing.T) {
	reader := &spyBudgetReader{limit: budget.Amount{CostMicroUSD: 10_000_000}}
	s := New(reader, &spyExtender{}, &spyDegrader{}, stubReflector{}, nil)

	spans := []otelgenai.SpanData{chatSpan(0, 0, 5_000_000)} // 0.5 < 0.8
	ev, err := s.Evaluate(context.Background(), spans, fixedTraceHex(), "tree-1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if ev.Prompt != nil {
		t.Fatalf("abaixo do limiar o prompt NÃO devia disparar")
	}
	if ev.State != PromptIdle {
		t.Fatalf("estado inesperado abaixo do limiar: %v", ev.State)
	}
}

// --- (c) Delegação: OptionExtend pede; a superfície não muta o budget (AC3) --------

func TestResolvePrompt_Extend_DelegatesAndDoesNotMutateBudget(t *testing.T) {
	reader := &spyBudgetReader{
		limit:     budget.Amount{Tokens: 1000, CostMicroUSD: 10_000_000},
		available: budget.Amount{Tokens: 200, CostMicroUSD: 2_000_000},
	}
	ext := &spyExtender{outcome: ExtensionOutcome{Granted: true, Detail: "ok"}}
	tr := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	s := New(reader, ext, &spyDegrader{}, stubReflector{}, tr)

	limitBefore := reader.limit
	availBefore := reader.available

	req := ExtensionRequest{TreeID: "tree-1", RunID: "run-1", Additional: budget.Amount{CostMicroUSD: 5_000_000}, Reason: "user_requested"}
	res, err := s.ResolvePrompt(context.Background(), OptionExtend, req)
	if err != nil {
		t.Fatalf("ResolvePrompt: %v", err)
	}
	if ext.called != 1 {
		t.Fatalf("OptionExtend devia delegar exactamente 1 vez, got %d", ext.called)
	}
	if ext.lastReq.Additional.CostMicroUSD != 5_000_000 {
		t.Fatalf("pedido de extensão não propagado: %+v", ext.lastReq)
	}
	if !res.Extension.Granted {
		t.Fatalf("outcome do controlo não devolvido tal-qual: %+v", res.Extension)
	}
	// A superfície NÃO mutou o orçamento — o reader mantém-se intacto (só LÊ).
	if reader.limit != limitBefore || reader.available != availBefore {
		t.Fatalf("a superfície mutou o orçamento: limit %v->%v, avail %v->%v", limitBefore, reader.limit, availBefore, reader.available)
	}
	// Span da decisão emitido com a opção e o resultado da extensão.
	decSpans := tr.SpansByOperation(OpExhaustionDecision)
	if len(decSpans) != 1 {
		t.Fatalf("esperado 1 span de decisão, got %d", len(decSpans))
	}
	if decSpans[0].Attributes[AttrExhaustionOption] != "extend" {
		t.Fatalf("span da decisão sem a opção extend: %v", decSpans[0].Attributes[AttrExhaustionOption])
	}
	if decSpans[0].Attributes[AttrExtensionGranted] != true {
		t.Fatalf("span da decisão sem extension_granted=true")
	}
}

func TestResolvePrompt_SummarizeStopAndAbort_ReturnToOrchestrator(t *testing.T) {
	ext := &spyExtender{}
	s := New(&spyBudgetReader{}, ext, &spyDegrader{}, stubReflector{}, nil)
	for _, opt := range []ExhaustionOption{OptionSummarizeStop, OptionAbort} {
		res, err := s.ResolvePrompt(context.Background(), opt, ExtensionRequest{RunID: "run-1"})
		if err != nil {
			t.Fatalf("ResolvePrompt(%v): %v", opt, err)
		}
		if res.Option != opt || res.State != PromptResolved {
			t.Fatalf("resolução inesperada: %+v", res)
		}
	}
	if ext.called != 0 {
		t.Fatalf("summarize_stop/abort NÃO deviam pedir extensão, got %d", ext.called)
	}
}

func TestResolvePrompt_UnknownOption_FailClosed(t *testing.T) {
	s := New(&spyBudgetReader{}, &spyExtender{}, &spyDegrader{}, stubReflector{}, nil)
	if _, err := s.ResolvePrompt(context.Background(), OptionUnset, ExtensionRequest{}); !errors.Is(err, ErrUnknownOption) {
		t.Fatalf("opção desconhecida devia ser fail-closed, got %v", err)
	}
}

// --- (d) Progresso: a ProgressSnapshot reflecte o estado/passo corrente (AC1) ------

func TestEvaluate_Progress_ReflectsCurrentStateStep(t *testing.T) {
	reader := &spyBudgetReader{limit: budget.Amount{CostMicroUSD: 10_000_000}}
	refl := stubReflector{snap: ProgressSnapshot{State: "waiting_on_tool", Step: "tool:search"}}
	s := New(reader, &spyExtender{}, &spyDegrader{}, refl, nil)

	ev, err := s.Evaluate(context.Background(), []otelgenai.SpanData{chatSpan(0, 0, 1_000_000)}, fixedTraceHex(), "tree-1")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if ev.Progress.State != "waiting_on_tool" || ev.Progress.Step != "tool:search" {
		t.Fatalf("progresso não reflecte o reflector: %+v", ev.Progress)
	}
}

// --- (e) Configurabilidade do limiar (AC5) -----------------------------------------

func TestWithThreshold_RespectsConfiguredThreshold(t *testing.T) {
	reader := &spyBudgetReader{limit: budget.Amount{CostMicroUSD: 10_000_000}}
	// 6M de 10M = 0.6.
	spans := []otelgenai.SpanData{chatSpan(0, 0, 6_000_000)}

	// Limiar 0.5 -> 0.6 dispara.
	s50 := New(reader, &spyExtender{}, &spyDegrader{}, stubReflector{}, nil, WithThreshold(0.5))
	ev, _ := s50.Evaluate(context.Background(), spans, fixedTraceHex(), "tree-1")
	if ev.Prompt == nil {
		t.Fatalf("com limiar 0.5, 0.6 devia disparar")
	}
	// Limiar 0.9 -> 0.6 não dispara.
	s90 := New(reader, &spyExtender{}, &spyDegrader{}, stubReflector{}, nil, WithThreshold(0.9))
	ev, _ = s90.Evaluate(context.Background(), spans, fixedTraceHex(), "tree-1")
	if ev.Prompt != nil {
		t.Fatalf("com limiar 0.9, 0.6 NÃO devia disparar")
	}
}

func TestWithThreshold_InvalidFallsBackToDefault(t *testing.T) {
	for _, bad := range []float64{0, -0.1, 1, 1.5} {
		s := New(&spyBudgetReader{}, &spyExtender{}, &spyDegrader{}, stubReflector{}, nil, WithThreshold(bad))
		if s.Threshold() != DefaultThreshold {
			t.Fatalf("limiar inválido %v devia cair no default %v, got %v", bad, DefaultThreshold, s.Threshold())
		}
	}
}

// --- Timeout sem resposta -> Degrader (AC5): nunca morre em silêncio ----------------

func TestOnPromptTimeout_Degrades(t *testing.T) {
	deg := &spyDegrader{}
	tr := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	s := New(&spyBudgetReader{}, &spyExtender{}, deg, stubReflector{}, tr, WithRunID("run-1"))

	if err := s.OnPromptTimeout(context.Background()); err != nil {
		t.Fatalf("OnPromptTimeout: %v", err)
	}
	if deg.called != 1 {
		t.Fatalf("timeout devia degradar exactamente 1 vez, got %d", deg.called)
	}
	if deg.lastReason != ReasonExhaustionPromptTimeout {
		t.Fatalf("razão de degradação inesperada: %q", deg.lastReason)
	}
	decSpans := tr.SpansByOperation(OpExhaustionDecision)
	if len(decSpans) != 1 || decSpans[0].Attributes[AttrDegradeReason] != ReasonExhaustionPromptTimeout {
		t.Fatalf("span da decisão de timeout em falta ou sem razão: %+v", decSpans)
	}
}

func TestOnPromptTimeout_NilDegrader_FailClosed(t *testing.T) {
	s := New(&spyBudgetReader{}, &spyExtender{}, nil, stubReflector{}, nil)
	if err := s.OnPromptTimeout(context.Background()); !errors.Is(err, ErrNilDegrader) {
		t.Fatalf("sem Degrader devia ser fail-closed, got %v", err)
	}
}

func TestEvaluate_NilReader_FailClosed(t *testing.T) {
	s := New(nil, &spyExtender{}, &spyDegrader{}, stubReflector{}, nil)
	if _, err := s.Evaluate(context.Background(), nil, fixedTraceHex(), "tree-1"); !errors.Is(err, ErrNilBudgetReader) {
		t.Fatalf("sem BudgetReader devia ser fail-closed, got %v", err)
	}
}
