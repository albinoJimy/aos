package otelgenai

import (
	"context"
	"testing"
)

// evalToolSpan constrói um span execute_tool com o nome de tool dado (a acção sobre
// a qual o trace-diffing compara sequências).
func evalToolSpan(name string) SpanData {
	return SpanData{
		Name: OpExecuteTool,
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpExecuteTool},
			{Key: AttrToolName, Value: name},
			{Key: AttrToolCallHash, Value: "h-" + name},
			{Key: AttrResultTaint, Value: "untrusted"},
		},
	}
}

// evalChatSpan constrói um span chat com tokens/custo (a unidade-verdade agregada).
func evalChatSpan(in, out, micro int64) SpanData {
	return SpanData{
		Name: OpChat,
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpChat},
			{Key: AttrRequestModel, Value: "claude-opus-4-8"},
			{Key: AttrPrincipalNHI, Value: "nhi:agent-1"},
			{Key: AttrInputTokens, Value: in},
			{Key: AttrOutputTokens, Value: out},
			{Key: AttrCostMicroUSD, Value: micro},
		},
	}
}

// --- (1) Integração: eval sobre um trace existente → span ligado por trace_id ---

func TestRecordEvaluationLinkedBySameTraceID(t *testing.T) {
	exp := &RecordingExporter{}
	tr := NewTracer(exp, WithIDGenerator(&SequentialIDGenerator{}))
	ctx := context.Background()

	// Trajectória avaliada: invoke_agent (raiz) + um turno chat, partilhando o trace.
	ctx, agent := tr.StartSpan(ctx, OpInvokeAgent)
	agent.SetAttribute(AttrOperationName, OpInvokeAgent)
	target := agent.SpanContext()
	_, chat := tr.StartSpan(ctx, OpChat)
	chat.SetAttribute(AttrOperationName, OpChat)
	chat.SetAttribute(AttrRequestModel, "claude-opus-4-8")
	chat.SetAttribute(AttrPrincipalNHI, "nhi:agent-1")
	chat.End()
	agent.End()

	// A eval é emitida NO MESMO trace (ctx propaga o SpanContext da trajectória).
	sc := RecordEvaluation(ctx, tr, EvaluationResult{
		Suite:   "skill.summarize",
		EvalID:  "eval-1",
		Dataset: EvalDatasetGolden,
		Verdict: EvalPass,
		Score:   0.97,
	}.WithTarget(target))

	if !sc.IsValid() {
		t.Fatal("o span de eval devia ter um SpanContext válido")
	}
	if sc.TraceIDHex() != target.TraceIDHex() {
		t.Fatalf("eval span trace %s != trajectória %s", sc.TraceIDHex(), target.TraceIDHex())
	}

	evals := exp.SpansByName(OpEvaluation)
	if len(evals) != 1 {
		t.Fatalf("esperava 1 span %s exportado, obtive %d", OpEvaluation, len(evals))
	}
	ev := evals[0]
	// Recuperável por um exportador OTel-compatível: partilha o trace_id da trajectória.
	if ev.SpanContext.TraceIDHex() != target.TraceIDHex() {
		t.Errorf("span de eval exportado com trace %s, esperava %s", ev.SpanContext.TraceIDHex(), target.TraceIDHex())
	}
	// E aponta o parent_span_id ao span da trajectória (ligação nativa OTel).
	if parentHexOf(ev) != target.SpanIDHex() {
		t.Errorf("parent do span de eval %s, esperava %s", parentHexOf(ev), target.SpanIDHex())
	}
	// A via explícita concorda: aos.eval.target_trace_id = trace-alvo.
	if got := attrString(ev, AttrEvalTargetTraceID); got != target.TraceIDHex() {
		t.Errorf("%s = %q, esperava %q", AttrEvalTargetTraceID, got, target.TraceIDHex())
	}
	if got := attrString(ev, AttrEvalVerdict); got != string(EvalPass) {
		t.Errorf("veredicto = %q, esperava %q", got, EvalPass)
	}
	if got := attrString(ev, AttrEvalDataset); got != string(EvalDatasetGolden) {
		t.Errorf("dataset = %q, esperava %q", got, EvalDatasetGolden)
	}
	// O span de eval é conforme ao contrato semconv (AOS-076 tabela, agora com eval).
	if err := ValidateSpanData(ev); err != nil {
		t.Errorf("span de eval não conforme: %v", err)
	}
}

// A eval pode também ser emitida num trace PRÓPRIO e ligar-se só pela via explícita.
func TestRecordEvaluationLinkedByExplicitTargetAttr(t *testing.T) {
	exp := &RecordingExporter{}
	tr := NewTracer(exp, WithIDGenerator(&SequentialIDGenerator{}))

	target := SpanContext{TraceID: traceID(0xAB), SpanID: spanID(0x01)}
	// ctx SEM o SpanContext da trajectória: o span de eval nasce num trace novo.
	sc := RecordEvaluation(context.Background(), tr, EvaluationResult{
		Dataset:       EvalDatasetFailureDerived,
		Verdict:       EvalFail,
		TargetTraceID: target.TraceID,
	})
	if sc.TraceIDHex() == target.TraceIDHex() {
		t.Fatal("sem propagação em ctx, o span de eval devia ter um trace PRÓPRIO")
	}
	ev := exp.SpansByName(OpEvaluation)[0]
	if got := attrString(ev, AttrEvalTargetTraceID); got != target.TraceIDHex() {
		t.Errorf("ligação explícita %s = %q, esperava %q", AttrEvalTargetTraceID, got, target.TraceIDHex())
	}
	// EvaluationResultFromSpanData recupera o trace-alvo explícito.
	r, ok := EvaluationResultFromSpanData(ev)
	if !ok || r.TargetTraceIDHex() != target.TraceIDHex() {
		t.Errorf("reconstrução do alvo = %q (ok=%v), esperava %q", r.TargetTraceIDHex(), ok, target.TraceIDHex())
	}
}

func TestRecordEvaluationNoopTracer(t *testing.T) {
	sc := RecordEvaluation(context.Background(), NoopTracer{}, EvaluationResult{Verdict: EvalPass})
	if sc.IsValid() {
		t.Error("NoopTracer não devia produzir um SpanContext válido")
	}
}

// --- (2) Integração: trace-diffing apanha uma regressão NOVA de alteração de skill ---

func TestTraceDiffCatchesNewSkillRegression(t *testing.T) {
	// Baseline aprovada: a skill faz search → summarize.
	baseline := []SpanData{
		evalChatSpan(100, 50, 1000),
		evalToolSpan("web.search"),
		evalChatSpan(80, 40, 800),
		evalToolSpan("doc.summarize"),
	}
	// Candidato após alteração de skill: a segunda acção mudou para uma tool DIFERENTE
	// (doc.delete) — uma regressão NOVA que nenhum dataset de falha passada teria.
	candidate := []SpanData{
		evalChatSpan(100, 50, 1000),
		evalToolSpan("web.search"),
		evalChatSpan(80, 40, 800),
		evalToolSpan("doc.delete"),
	}

	regs := TraceDiff(baseline, candidate, TraceDiffConfig{})
	if len(regs) == 0 {
		t.Fatal("o trace-diffing devia apanhar a regressão nova (tool trocada)")
	}
	var found bool
	for _, r := range regs {
		if r.Kind == RegressionToolSequence && r.Baseline == "doc.summarize" && r.Candidate == "doc.delete" {
			found = true
			if r.Step != 1 {
				t.Errorf("acção divergente no step %d, esperava 1 (2ª tool)", r.Step)
			}
		}
	}
	if !found {
		t.Errorf("esperava uma regressão tool_sequence doc.summarize→doc.delete, obtive %+v", regs)
	}
}

func TestTraceDiffIdenticalZeroRegressions(t *testing.T) {
	trj := []SpanData{
		evalChatSpan(100, 50, 1000),
		evalToolSpan("web.search"),
	}
	// Baseline == candidato (mesma estrutura semântica) ⇒ zero regressões, mesmo com
	// trace/span ids e timestamps diferentes (normalizados/ignorados).
	if regs := TraceDiff(trj, trj, TraceDiffConfig{}); len(regs) != 0 {
		t.Errorf("baseline idêntica devia dar 0 regressões, obtive %+v", regs)
	}
}

func TestTraceDiffCostRegressionAndTolerance(t *testing.T) {
	baseline := []SpanData{evalChatSpan(100, 50, 1_000)}
	candidate := []SpanData{evalChatSpan(100, 50, 5_000)} // salto de custo +4000 micro-USD

	// Sem tolerância: apanha o salto de custo.
	regs := TraceDiff(baseline, candidate, TraceDiffConfig{})
	if !hasKind(regs, RegressionCost) {
		t.Errorf("esperava uma regressão de custo, obtive %+v", regs)
	}
	// Com tolerância acima do delta: NÃO gera falso-positivo.
	regs = TraceDiff(baseline, candidate, TraceDiffConfig{CostToleranceMicroUSD: 10_000})
	if hasKind(regs, RegressionCost) {
		t.Errorf("dentro da tolerância não devia sinalizar custo, obtive %+v", regs)
	}

	// Um custo MENOR que a baseline além do limiar também é uma divergência (delta
	// absoluto): o candidato barato demais pode sinalizar uma acção saltada.
	cheaper := TraceDiff([]SpanData{evalChatSpan(100, 50, 9_000)}, []SpanData{evalChatSpan(100, 50, 1_000)}, TraceDiffConfig{})
	if !hasKind(cheaper, RegressionCost) {
		t.Errorf("uma queda de custo além do limiar devia sinalizar, obtive %+v", cheaper)
	}
}

func TestTraceDiffTokenRegression(t *testing.T) {
	baseline := []SpanData{evalChatSpan(100, 50, 1_000)}  // 150 tokens
	candidate := []SpanData{evalChatSpan(300, 50, 1_000)} // 350 tokens (+200)

	if regs := TraceDiff(baseline, candidate, TraceDiffConfig{}); !hasKind(regs, RegressionTokens) {
		t.Errorf("esperava uma regressão de tokens, obtive %+v", regs)
	}
	// Dentro da tolerância de tokens: sem falso-positivo.
	regs := TraceDiff(baseline, candidate, TraceDiffConfig{TokenTolerance: 500})
	if hasKind(regs, RegressionTokens) {
		t.Errorf("dentro da tolerância não devia sinalizar tokens, obtive %+v", regs)
	}
}

func TestTraceDiffAddedAndRemovedActions(t *testing.T) {
	base := []SpanData{evalToolSpan("a"), evalToolSpan("b")}
	// Candidato acrescenta uma acção.
	added := TraceDiff(base, []SpanData{evalToolSpan("a"), evalToolSpan("b"), evalToolSpan("c")}, TraceDiffConfig{})
	if !hasStep(added, 2, "", "c") {
		t.Errorf("esperava acção acrescentada c no step 2, obtive %+v", added)
	}
	// Candidato remove uma acção.
	removed := TraceDiff(base, []SpanData{evalToolSpan("a")}, TraceDiffConfig{})
	if !hasStep(removed, 1, "b", "") {
		t.Errorf("esperava acção b em falta no step 1, obtive %+v", removed)
	}
}

// --- (3) Correlação: dado o trace avaliado, junta-se eval + custo + decisão ---

func TestEvalCorrelatesWithCostAndDecisionByTraceID(t *testing.T) {
	exp := &RecordingExporter{}
	tr := NewTracer(exp, WithIDGenerator(&SequentialIDGenerator{}))

	// Trajectória (trace comum): um turno chat com custo + uma tool call com decisão.
	ctx, agent := tr.StartSpan(context.Background(), OpInvokeAgent)
	agent.SetAttribute(AttrOperationName, OpInvokeAgent)
	target := agent.SpanContext()

	_, chat := tr.StartSpan(ctx, OpChat)
	for _, kv := range evalChatSpan(200, 100, 4_242).Attributes {
		chat.SetAttribute(kv.Key, kv.Value)
	}
	chat.End()

	_, tool := tr.StartSpan(ctx, OpExecuteTool)
	tool.SetAttribute(AttrOperationName, OpExecuteTool)
	tool.SetAttribute(AttrToolName, "web.search")
	tool.SetAttribute(AttrToolCallHash, "hh")
	tool.SetAttribute(AttrResultTaint, "untrusted")
	tool.SetAttribute(AttrDecision, "permit")
	tool.End()
	agent.End()

	// A eval no mesmo trace.
	RecordEvaluation(ctx, tr, EvaluationResult{
		Dataset: EvalDatasetGolden,
		Verdict: EvalPass,
		Score:   0.9,
	}.WithTarget(target))

	all := exp.Spans()
	traceHex := target.TraceIDHex()

	// (a) custo pelo trace_id (agregação por trajectória, sem dupla-contagem).
	cost := AggregateByTrace(all)[traceHex]
	if cost.CostMicroUSD != 4_242 {
		t.Errorf("custo por trace = %d, esperava 4242", cost.CostMicroUSD)
	}
	if cost.TotalTokens() != 300 {
		t.Errorf("tokens por trace = %d, esperava 300", cost.TotalTokens())
	}

	// (b) decisão de política pelo mesmo trace_id.
	var decision string
	var verdict EvalVerdict
	for _, sd := range all {
		if sd.SpanContext.TraceIDHex() != traceHex {
			continue
		}
		if operationOf(sd) == OpExecuteTool {
			decision = attrString(sd, AttrDecision)
		}
		if r, ok := EvaluationResultFromSpanData(sd); ok {
			verdict = r.Verdict
		}
	}
	if decision != "permit" {
		t.Errorf("decisão pelo trace = %q, esperava permit", decision)
	}
	// (c) veredicto da eval pelo mesmo trace_id — os três juntam-se pelo trace.
	if verdict != EvalPass {
		t.Errorf("veredicto da eval pelo trace = %q, esperava pass", verdict)
	}
}

// --- Portas: EvalRunner + EvalGate (fail-closed) ---

func TestStaticEvalRunnerLinksTarget(t *testing.T) {
	runner := StaticEvalRunner{Result: EvaluationResult{Verdict: EvalPass, Score: 1}}
	target := EvalTarget{TraceID: traceID(0x22), Spans: []SpanData{evalChatSpan(1, 1, 1)}}
	got := runner.Run(context.Background(), target)
	if got.TargetTraceID != target.TraceID {
		t.Error("StaticEvalRunner devia ligar o resultado ao TraceID do target")
	}
	if !got.Passed() {
		t.Error("resultado devia ser pass")
	}
}

func TestEvalRunnerFuncAdapter(t *testing.T) {
	var runner EvalRunner = EvalRunnerFunc(func(_ context.Context, target EvalTarget) EvaluationResult {
		return EvaluationResult{Verdict: EvalFail, TargetTraceID: target.TraceID}
	})
	r := runner.Run(context.Background(), EvalTarget{TraceID: traceID(0x33)})
	if r.Passed() || r.TargetTraceID != traceID(0x33) {
		t.Errorf("EvalRunnerFunc não encaminhou correctamente: %+v", r)
	}
}

func TestFailClosedGate(t *testing.T) {
	gate := FailClosedGate{}
	cases := []struct {
		name string
		res  EvaluationResult
		want bool
	}{
		{"pass admite", EvaluationResult{Verdict: EvalPass}, true},
		{"fail não admite", EvaluationResult{Verdict: EvalFail}, false},
		{"veredicto vazio não admite (fail-closed)", EvaluationResult{}, false},
		{"veredicto desconhecido não admite", EvaluationResult{Verdict: "maybe"}, false},
	}
	for _, tc := range cases {
		if got := gate.Admit(tc.res); got != tc.want {
			t.Errorf("%s: Admit = %v, esperava %v", tc.name, got, tc.want)
		}
	}
}

func TestFailClosedGateMinScore(t *testing.T) {
	gate := FailClosedGate{MinScore: 0.8}
	if gate.Admit(EvaluationResult{Verdict: EvalPass, Score: 0.79}) {
		t.Error("pass abaixo do MinScore não devia admitir")
	}
	if !gate.Admit(EvaluationResult{Verdict: EvalPass, Score: 0.8}) {
		t.Error("pass no MinScore devia admitir")
	}
}

func TestEvalGateFuncAdapter(t *testing.T) {
	var gate EvalGate = EvalGateFunc(func(r EvaluationResult) bool { return r.Score > 0.5 })
	if !gate.Admit(EvaluationResult{Score: 0.6}) || gate.Admit(EvaluationResult{Score: 0.4}) {
		t.Error("EvalGateFunc não delegou correctamente")
	}
}

// Fecho do ciclo: runner produz resultado → recorder emite span → gate lê o span
// exportado e decide (fail-closed). Prova a consumibilidade pelo eval-gate (ADR-012).
func TestRunnerRecordGateEndToEnd(t *testing.T) {
	exp := &RecordingExporter{}
	tr := NewTracer(exp, WithIDGenerator(&SequentialIDGenerator{}))
	target := EvalTarget{TraceID: traceID(0x44), Spans: []SpanData{evalChatSpan(1, 1, 1)}}

	runner := StaticEvalRunner{Result: EvaluationResult{Dataset: EvalDatasetGolden, Verdict: EvalFail, Score: 0.2}}
	result := runner.Run(context.Background(), target)
	RecordEvaluation(context.Background(), tr, result)

	// O gate consome o resultado reconstruído do span exportado — fail-closed.
	ev := exp.SpansByName(OpEvaluation)[0]
	consumed, ok := EvaluationResultFromSpanData(ev)
	if !ok {
		t.Fatal("devia reconstruir o EvaluationResult do span")
	}
	if (FailClosedGate{}).Admit(consumed) {
		t.Error("um veredicto fail NÃO devia admitir (fail-closed)")
	}
}

// --- Leitura/reconstrução ---

func TestEvaluationResultFromSpanDataNonEval(t *testing.T) {
	if _, ok := EvaluationResultFromSpanData(evalChatSpan(1, 1, 1)); ok {
		t.Error("um span chat não é uma eval")
	}
}

func TestEvaluationResultFromSpanDataFallsBackToOwnTrace(t *testing.T) {
	// Span de eval SEM aos.eval.target_trace_id mas FILHO na árvore da trajectória
	// (parent_span_id não-nulo): é o caso "mesmo trace" legítimo — o span vive no
	// trace da trajectória avaliada, logo o alvo cai no próprio trace_id.
	sd := SpanData{
		Name:         OpEvaluation,
		SpanContext:  SpanContext{TraceID: traceID(0x55), SpanID: spanID(0x02)},
		ParentSpanID: spanID(0x01), // pai = span da trajectória (ligação nativa OTel)
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpEvaluation},
			{Key: AttrEvalVerdict, Value: string(EvalPass)},
			{Key: AttrEvalDataset, Value: string(EvalDatasetGolden)},
			{Key: AttrEvalScore, Value: 0.5},
		},
	}
	r, ok := EvaluationResultFromSpanData(sd)
	if !ok {
		t.Fatal("devia reconstruir")
	}
	if r.TargetTraceIDHex() != sd.SpanContext.TraceIDHex() {
		t.Errorf("fallback do alvo = %q, esperava o próprio trace %q", r.TargetTraceIDHex(), sd.SpanContext.TraceIDHex())
	}
	if r.Score != 0.5 {
		t.Errorf("score = %v, esperava 0.5", r.Score)
	}
}

// Um span de eval RAIZ (sem pai) e sem aos.eval.target_trace_id NÃO partilha nenhuma
// trajectória: reconstruir NÃO pode reportar o seu próprio trace-raiz como alvo (seria
// um join falso). O alvo fica por preencher (unlinked), distinguível pelo consumidor.
func TestEvaluationResultFromSpanDataRootSpanNotSelfTargeting(t *testing.T) {
	sd := SpanData{
		Name:        OpEvaluation,
		SpanContext: SpanContext{TraceID: traceID(0x99), SpanID: spanID(0x03)},
		// ParentSpanID all-zero ⇒ span RAIZ: o trace_id é um trace novo, não um alvo.
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpEvaluation},
			{Key: AttrEvalVerdict, Value: string(EvalFail)},
			{Key: AttrEvalDataset, Value: string(EvalDatasetGolden)},
		},
	}
	r, ok := EvaluationResultFromSpanData(sd)
	if !ok {
		t.Fatal("devia reconstruir")
	}
	if r.TargetTraceIDHex() != "" {
		t.Errorf("um eval raiz sem alvo explícito NÃO devia auto-referir-se: alvo = %q, esperava vazio", r.TargetTraceIDHex())
	}
}

// RecordEvaluation impõe a invariante de ligação (fail-closed): sem propagação em ctx
// E sem TargetTraceID, RECUSA a emissão e devolve um SpanContext inválido, em vez de
// emitir um span-raiz sem alvo que um leitor tomaria por auto-referente.
func TestRecordEvaluationRefusesUnlinked(t *testing.T) {
	exp := &RecordingExporter{}
	tr := NewTracer(exp, WithIDGenerator(&SequentialIDGenerator{}))

	// ctx.Background() (sem SpanContext propagado) E result sem TargetTraceID.
	sc := RecordEvaluation(context.Background(), tr, EvaluationResult{
		Dataset: EvalDatasetGolden,
		Verdict: EvalPass,
		Score:   0.9,
	})
	if sc.IsValid() {
		t.Error("uma eval sem ligação (nem ctx nem alvo) NÃO devia produzir um SpanContext válido")
	}
	if got := len(exp.SpansByName(OpEvaluation)); got != 0 {
		t.Errorf("uma eval sem ligação NÃO devia emitir span, emitiu %d", got)
	}
}

func TestEvaluationResultTargetHexEmpty(t *testing.T) {
	if (EvaluationResult{}).TargetTraceIDHex() != "" {
		t.Error("TargetTraceIDHex de um alvo all-zero devia ser vazio")
	}
}

// helpers de asserção
func hasKind(regs []Regression, k RegressionKind) bool {
	for _, r := range regs {
		if r.Kind == k {
			return true
		}
	}
	return false
}

func hasStep(regs []Regression, step int, base, cand string) bool {
	for _, r := range regs {
		if r.Kind == RegressionToolSequence && r.Step == step && r.Baseline == base && r.Candidate == cand {
			return true
		}
	}
	return false
}
