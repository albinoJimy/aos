package trajectorysurface

import (
	"context"
	"reflect"
	"testing"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
	"github.com/aos-ref/substrate/redaction"
)

// --- helpers de construção determinista de SpanData ---------------------------

func traceID(b byte) [16]byte {
	var t [16]byte
	t[0] = b
	return t
}

func spanID(b byte) [8]byte {
	var s [8]byte
	s[0] = b
	return s
}

func kv(pairs ...any) []otelgenai.KeyValue {
	out := make([]otelgenai.KeyValue, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, otelgenai.KeyValue{Key: pairs[i].(string), Value: pairs[i+1]})
	}
	return out
}

// mkSpan cria uma SpanData no trace tr, com span sp e parent pa (0 = raiz).
func mkSpan(tr, sp, pa byte, attrs []otelgenai.KeyValue) otelgenai.SpanData {
	return otelgenai.SpanData{
		Name:         attrOpName(attrs),
		SpanContext:  otelgenai.SpanContext{TraceID: traceID(tr), SpanID: spanID(sp)},
		ParentSpanID: spanID(pa),
		Attributes:   attrs,
	}
}

func attrOpName(attrs []otelgenai.KeyValue) string {
	for _, a := range attrs {
		if a.Key == otelgenai.AttrOperationName {
			if s, ok := a.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

// trajectory devolve uma trajectória-modelo: um invoke_agent raiz com um chat directo e
// um sub-agente aninhado (invoke_agent -> execute_tool -> chat). Trace 0xAA.
//
//	01 invoke_agent (raiz)
//	├─ 02 chat            (in 100 / out 50 / 1000 µUSD)
//	└─ 03 invoke_agent    (sub-agente)
//	   └─ 04 execute_tool (tool=search, result_taint=untrusted)
//	      └─ 05 chat      (in 200 / out 80 / 2000 µUSD)
func trajectory() []otelgenai.SpanData {
	return []otelgenai.SpanData{
		mkSpan(0xAA, 0x01, 0x00, kv(
			otelgenai.AttrOperationName, otelgenai.OpInvokeAgent,
			otelgenai.AttrRunID, "run-xyz",
		)),
		mkSpan(0xAA, 0x02, 0x01, kv(
			otelgenai.AttrOperationName, otelgenai.OpChat,
			otelgenai.AttrRequestModel, "claude-x",
			otelgenai.AttrPrincipalNHI, "nhi-1",
			otelgenai.AttrInputTokens, int64(100),
			otelgenai.AttrOutputTokens, int64(50),
			otelgenai.AttrCostMicroUSD, int64(1000),
		)),
		mkSpan(0xAA, 0x03, 0x01, kv(
			otelgenai.AttrOperationName, otelgenai.OpInvokeAgent,
		)),
		mkSpan(0xAA, 0x04, 0x03, kv(
			otelgenai.AttrOperationName, otelgenai.OpExecuteTool,
			otelgenai.AttrToolName, "search",
			otelgenai.AttrToolCallHash, "deadbeef",
			otelgenai.AttrResultTaint, "untrusted",
		)),
		mkSpan(0xAA, 0x05, 0x04, kv(
			otelgenai.AttrOperationName, otelgenai.OpChat,
			otelgenai.AttrRequestModel, "claude-x",
			otelgenai.AttrPrincipalNHI, "nhi-2",
			otelgenai.AttrInputTokens, int64(200),
			otelgenai.AttrOutputTokens, int64(80),
			otelgenai.AttrCostMicroUSD, int64(2000),
		)),
	}
}

func newEngine() *redaction.Engine { return redaction.NewEngine(nil) }

func newSurface(t *testing.T, opts ...Option) *TrajectorySurface {
	t.Helper()
	s, err := New(newEngine(), opts...)
	if err != nil {
		t.Fatalf("New: erro inesperado: %v", err)
	}
	return s
}

// findChild devolve o filho de node cujo span_id (byte 0) é b.
func findChild(node *SpanNode, b byte) *SpanNode {
	for _, c := range node.Children {
		if c.Span.SpanContext.SpanID == spanID(b) {
			return c
		}
	}
	return nil
}

// ============================================================================
// TESTE (a) — ÁRVORE: hierarquia invoke_agent -> execute_tool -> chat (AC1)
// ============================================================================

func TestBuildTree_HierarchyAndOrder(t *testing.T) {
	roots := BuildTree(trajectory())

	if len(roots) != 1 {
		t.Fatalf("esperava 1 raiz, obtive %d", len(roots))
	}
	root := roots[0]
	if root.Span.SpanContext.SpanID != spanID(0x01) {
		t.Fatalf("raiz errada: %x", root.Span.SpanContext.SpanID)
	}
	if root.Kind != KindInvokeAgent {
		t.Fatalf("kind da raiz: %q", root.Kind)
	}

	// A raiz tem 2 filhos DIRECTOS, na ORDEM de aparição: chat(02), invoke_agent(03).
	if len(root.Children) != 2 {
		t.Fatalf("raiz devia ter 2 filhos, tem %d", len(root.Children))
	}
	if root.Children[0].Span.SpanContext.SpanID != spanID(0x02) ||
		root.Children[0].Kind != KindChat {
		t.Fatalf("primeiro filho devia ser chat(02): %x %q",
			root.Children[0].Span.SpanContext.SpanID, root.Children[0].Kind)
	}
	sub := root.Children[1]
	if sub.Span.SpanContext.SpanID != spanID(0x03) || sub.Kind != KindInvokeAgent {
		t.Fatalf("segundo filho devia ser invoke_agent(03): %x %q",
			sub.Span.SpanContext.SpanID, sub.Kind)
	}

	// invoke_agent(03) -> execute_tool(04) -> chat(05).
	if len(sub.Children) != 1 || sub.Children[0].Kind != KindExecuteTool {
		t.Fatalf("sub-agente devia ter 1 execute_tool: %+v", sub.Children)
	}
	tool := sub.Children[0]
	if tool.Span.SpanContext.SpanID != spanID(0x04) {
		t.Fatalf("execute_tool errado: %x", tool.Span.SpanContext.SpanID)
	}
	if len(tool.Children) != 1 || tool.Children[0].Kind != KindChat ||
		tool.Children[0].Span.SpanContext.SpanID != spanID(0x05) {
		t.Fatalf("execute_tool devia ter 1 chat(05): %+v", tool.Children)
	}
}

func TestBuildTree_ForestAndOrphanParent(t *testing.T) {
	// Dois spans-raiz (parent nulo) + um cujo parent NÃO está no conjunto => raiz.
	spans := []otelgenai.SpanData{
		mkSpan(0xB1, 0x01, 0x00, kv(otelgenai.AttrOperationName, otelgenai.OpInvokeAgent)),
		mkSpan(0xB2, 0x02, 0x00, kv(otelgenai.AttrOperationName, otelgenai.OpInvokeAgent)),
		mkSpan(0xB2, 0x09, 0x7F, kv(otelgenai.AttrOperationName, otelgenai.OpChat)), // parent 0x7F ausente
	}
	roots := BuildTree(spans)
	if len(roots) != 3 {
		t.Fatalf("esperava 3 raízes (floresta + órfão), obtive %d", len(roots))
	}
	// A ordem das raízes é a de aparição.
	if roots[0].Span.SpanContext.SpanID != spanID(0x01) ||
		roots[1].Span.SpanContext.SpanID != spanID(0x02) ||
		roots[2].Span.SpanContext.SpanID != spanID(0x09) {
		t.Fatalf("ordem/identidade das raízes errada")
	}
}

func TestBuildTree_DuplicateSpanIDKeepsFirst(t *testing.T) {
	spans := []otelgenai.SpanData{
		mkSpan(0xC1, 0x01, 0x00, kv(otelgenai.AttrOperationName, otelgenai.OpInvokeAgent)),
		mkSpan(0xC1, 0x01, 0x00, kv(otelgenai.AttrOperationName, otelgenai.OpChat)), // dup id
	}
	roots := BuildTree(spans)
	if len(roots) != 1 || roots[0].Kind != KindInvokeAgent {
		t.Fatalf("dup span_id: devia manter a primeira ocorrência, obtive %+v", roots)
	}
}

func TestKindOf_FallbackToName(t *testing.T) {
	// Sem AttrOperationName, o kind cai no Name do span.
	sd := otelgenai.SpanData{
		Name:        "custom_op",
		SpanContext: otelgenai.SpanContext{TraceID: traceID(0xD1), SpanID: spanID(0x01)},
	}
	roots := BuildTree([]otelgenai.SpanData{sd})
	if roots[0].Kind != "custom_op" {
		t.Fatalf("kind fallback: %q", roots[0].Kind)
	}
}

// ============================================================================
// TESTE (b) — DRILL-DOWN: tokens/custo/resultado/taint + rollup sub-árvore (AC2)
// ============================================================================

func TestInspect_ToolCallTokensCostTaint(t *testing.T) {
	rd := &Redaction{Engine: newEngine(), Subject: "s", Policy: redaction.RemoveAllPolicy("v")}
	roots := BuildTree(trajectory())
	root := roots[0]
	sub := findChild(root, 0x03)
	tool := findChild(sub, 0x04)

	// execute_tool: ToolName + ResultTaint; sem tokens de modelo.
	dt := Inspect(tool, rd)
	if dt.Kind != KindExecuteTool || dt.ToolName != "search" {
		t.Fatalf("execute_tool inspect: kind=%q tool=%q", dt.Kind, dt.ToolName)
	}
	if dt.ResultTaint != "untrusted" {
		t.Fatalf("result_taint esperado untrusted, obtive %q", dt.ResultTaint)
	}
	if dt.InputTokens != 0 || dt.OutputTokens != 0 {
		t.Fatalf("execute_tool não devia ter tokens de modelo: %+v", dt)
	}

	// chat(05): tokens + custo próprios do turno.
	chat := findChild(tool, 0x05)
	dc := Inspect(chat, rd)
	if dc.InputTokens != 200 || dc.OutputTokens != 80 {
		t.Fatalf("chat tokens: %+v", dc)
	}
	if dc.CostMicroUSD != 2000 || dc.CostUSD != 0.002 {
		t.Fatalf("chat custo: micro=%d usd=%v", dc.CostMicroUSD, dc.CostUSD)
	}
}

func TestInspect_InvokeAgentSubtreeRollup(t *testing.T) {
	rd := &Redaction{Engine: newEngine(), Subject: "s", Policy: redaction.RemoveAllPolicy("v")}
	roots := BuildTree(trajectory())
	root := roots[0]

	// Sub-árvore do invoke_agent RAIZ = chats 02 (1000) + 05 (2000) = 3000 µUSD;
	// tokens in 300 / out 130. COMPOSTO por RollupByTrace.
	dr := Inspect(root, rd)
	if dr.SubtreeCostMicroUSD != 3000 {
		t.Fatalf("subtree custo raiz esperado 3000, obtive %d", dr.SubtreeCostMicroUSD)
	}
	if dr.SubtreeInputTokens != 300 || dr.SubtreeOutputTokens != 130 {
		t.Fatalf("subtree tokens raiz: in=%d out=%d", dr.SubtreeInputTokens, dr.SubtreeOutputTokens)
	}
	if dr.SubtreeCostUSD != 0.003 {
		t.Fatalf("subtree USD raiz: %v", dr.SubtreeCostUSD)
	}

	// Sub-árvore do sub-agente(03) = só o chat 05 = 2000 µUSD.
	sub := findChild(root, 0x03)
	ds := Inspect(sub, rd)
	if ds.SubtreeCostMicroUSD != 2000 || ds.SubtreeInputTokens != 200 {
		t.Fatalf("subtree sub-agente: %+v", ds)
	}
}

func TestInspect_CostUSDFallback(t *testing.T) {
	// Um chat que só traz o USD float (sem o micro-USD inteiro): converte round-half.
	rd := &Redaction{Engine: newEngine(), Subject: "s", Policy: redaction.RemoveAllPolicy("v")}
	sd := mkSpan(0xE1, 0x01, 0x00, kv(
		otelgenai.AttrOperationName, otelgenai.OpChat,
		otelgenai.AttrCostUSD, float64(0.0025),
	))
	node := BuildTree([]otelgenai.SpanData{sd})[0]
	d := Inspect(node, rd)
	if d.CostMicroUSD != 2500 {
		t.Fatalf("fallback USD->micro: esperado 2500, obtive %d", d.CostMicroUSD)
	}
}

// ============================================================================
// TESTE (c) — CONSUMO: leitura pura, sem mutar nem re-emitir spans (AC3)
// ============================================================================

func TestPureConsumption_InputSpansUnchanged(t *testing.T) {
	in := trajectory()
	// Cópia profunda de referência ANTES.
	before := deepCopySpans(in)

	rd := &Redaction{Engine: newEngine(), Subject: "s", Policy: redaction.RemoveAllPolicy("v")}
	roots := BuildTree(in)
	for _, r := range roots {
		walk(r, func(n *SpanNode) { _ = Inspect(n, rd) })
	}

	if !reflect.DeepEqual(in, before) {
		t.Fatalf("os SpanData de entrada foram MUTADOS (leitura não-pura)")
	}
}

func TestTreeView_DoesNotReEmitTrajectorySpans(t *testing.T) {
	// O tracer da superfície só deve capturar os SEUS spans de interacção
	// (aos.trajectory.surface), NUNCA cópias dos spans da trajectória consumida.
	rec := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	s := newSurface(t, WithTracer(rec), WithRunID("run-xyz"))

	roots := s.TreeView(context.Background(), trajectory())
	_ = s.DrillDown(context.Background(), roots[0])

	for _, rs := range rec.Spans() {
		if rs.Operation != OpTrajectorySurface {
			t.Fatalf("a superfície re-emitiu um span não-interacção: %q", rs.Operation)
		}
	}
	// Exactamente 2 spans de interacção: 1 tree_view + 1 drill_down.
	if got := len(rec.Spans()); got != 2 {
		t.Fatalf("esperava 2 spans de interacção, obtive %d", got)
	}
}

// ============================================================================
// TESTE (d) — TAINT/PII: valores redigidos + untrusted marcado como DADO (AC4)
// ============================================================================

func TestInspect_RedactsPIIAndMarksUntrusted(t *testing.T) {
	rd := &Redaction{Engine: newEngine(), Subject: "titular", Policy: redaction.RemoveAllPolicy("v")}

	// Um execute_tool com um atributo de CONTEÚDO (data-plane) que carrega PII
	// (email) e o resultado marcado untrusted.
	sd := mkSpan(0xF1, 0x01, 0x00, kv(
		otelgenai.AttrOperationName, otelgenai.OpExecuteTool,
		otelgenai.AttrToolName, "reader",
		otelgenai.AttrToolCallHash, "abc123",
		otelgenai.AttrResultTaint, "untrusted",
		"gen_ai.tool.output", "contacta joao@example.com por favor",
	))
	node := BuildTree([]otelgenai.SpanData{sd})[0]
	d := Inspect(node, rd)

	eng := newEngine()
	var contentView, toolNameView *AttrView
	for i := range d.Attributes {
		switch d.Attributes[i].Key {
		case "gen_ai.tool.output":
			contentView = &d.Attributes[i]
		case otelgenai.AttrToolName:
			toolNameView = &d.Attributes[i]
		}
		// GATE AC4: NENHUM valor apresentado contém PII em claro.
		if fs := eng.ScanText(d.Attributes[i].Value); len(fs) != 0 {
			t.Fatalf("PII em claro no valor apresentado da chave %q: %q",
				d.Attributes[i].Key, d.Attributes[i].Value)
		}
	}
	if contentView == nil {
		t.Fatal("falta a AttrView do conteúdo")
	}
	// O email foi redigido (substituído pelo marcador), não aparece em claro.
	if contentView.Value == "contacta joao@example.com por favor" {
		t.Fatalf("o email NÃO foi redigido: %q", contentView.Value)
	}
	// Conteúdo de data-plane num span untrusted => marcado como DADO.
	if !contentView.Untrusted {
		t.Fatalf("o conteúdo untrusted devia estar marcado como DADO (Untrusted=true)")
	}
	// Um rótulo de control-plane (tool.name) NÃO é marcado untrusted.
	if toolNameView == nil || toolNameView.Untrusted {
		t.Fatalf("gen_ai.tool.name (control-plane) não devia estar marcado untrusted: %+v", toolNameView)
	}
}

func TestInspect_TrustedResultNotMarked(t *testing.T) {
	// Sem result_taint=untrusted, nenhuma AttrView é marcada como DADO.
	rd := &Redaction{Engine: newEngine(), Subject: "s", Policy: redaction.RemoveAllPolicy("v")}
	sd := mkSpan(0xF2, 0x01, 0x00, kv(
		otelgenai.AttrOperationName, otelgenai.OpChat,
		"gen_ai.tool.output", "texto qualquer",
	))
	node := BuildTree([]otelgenai.SpanData{sd})[0]
	d := Inspect(node, rd)
	for _, a := range d.Attributes {
		if a.Untrusted {
			t.Fatalf("nenhuma AttrView devia estar marcada untrusted: %+v", a)
		}
	}
}

func TestPresent_FailClosedOnTokenizeWithoutKeys(t *testing.T) {
	// Uma política que TOKENIZA sem KeySource faz RedactText falhar-fechar =>
	// present devolve o marcador duro, nunca o valor em claro.
	tokPolicy, err := redaction.NewPolicy("tok", map[redaction.Class]redaction.Action{
		redaction.ClassEmail:      redaction.ActionTokenize,
		redaction.ClassPhone:      redaction.ActionTokenize,
		redaction.ClassCreditCard: redaction.ActionTokenize,
		redaction.ClassIBAN:       redaction.ActionTokenize,
		redaction.ClassIPv4:       redaction.ActionTokenize,
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	rd := &Redaction{Engine: redaction.NewEngine(nil), Subject: "s", Policy: tokPolicy}
	if got := rd.present("joao@example.com"); got != "[REDACTED]" {
		t.Fatalf("fail-closed esperava [REDACTED], obtive %q", got)
	}
}

func TestPresent_NilEngineFailsClosed(t *testing.T) {
	var rd *Redaction
	if got := rd.present("qualquer"); got != "[REDACTED]" {
		t.Fatalf("nil Redaction devia fail-closed: %q", got)
	}
	rd2 := &Redaction{}
	if got := rd2.present("qualquer"); got != "[REDACTED]" {
		t.Fatalf("nil Engine devia fail-closed: %q", got)
	}
}

// ============================================================================
// AC5 — LIGAÇÃO A EVAL/REPLAY (navegação, sem recalcular)
// ============================================================================

type fakeEvalSource struct {
	byTrace map[string]otelgenai.EvaluationResult
}

func (f fakeEvalSource) EvalFor(traceIDHex string) (otelgenai.EvaluationResult, bool) {
	r, ok := f.byTrace[traceIDHex]
	return r, ok
}

type fakeReplaySource struct{ byTrace map[string]string }

func (f fakeReplaySource) ReplayFor(traceIDHex string) (string, bool) {
	r, ok := f.byTrace[traceIDHex]
	return r, ok
}

func TestLinkEvalReplay_AvailableAndAbsent(t *testing.T) {
	roots := BuildTree(trajectory())
	root := roots[0]
	traceHex := root.Span.SpanContext.TraceIDHex()

	evalSrc := fakeEvalSource{byTrace: map[string]otelgenai.EvaluationResult{
		traceHex: {EvalID: "ev-1", Suite: "golden-suite", Verdict: otelgenai.EvalPass, Score: 0.97, Dataset: otelgenai.EvalDatasetGolden},
	}}
	replaySrc := fakeReplaySource{byTrace: map[string]string{traceHex: "replay://run-xyz"}}

	s := newSurface(t, WithEvalSource(evalSrc), WithReplaySource(replaySrc))

	el := s.LinkEval(root)
	if !el.Available || el.EvalID != "ev-1" || el.Verdict != "pass" || el.Score != 0.97 ||
		el.Dataset != "golden" || el.Suite != "golden-suite" {
		t.Fatalf("EvalLink inesperada: %+v", el)
	}
	rl := s.LinkReplay(root)
	if !rl.Available || rl.Ref != "replay://run-xyz" {
		t.Fatalf("ReplayLink inesperada: %+v", rl)
	}

	// Sem porta => Available=false (não inventa).
	bare := newSurface(t)
	if bare.LinkEval(root).Available || bare.LinkReplay(root).Available {
		t.Fatalf("sem portas, as ligações deviam estar indisponíveis")
	}
	// Nó de outro trace sem eval registada => indisponível.
	other := &SpanNode{Span: mkSpan(0x11, 0x01, 0x00, nil)}
	if s.LinkEval(other).Available || s.LinkReplay(other).Available {
		t.Fatalf("trace sem eval/replay devia ficar indisponível")
	}
	// Portas diretas com nó nil => indisponível.
	if LinkEval(nil, evalSrc).Available || LinkReplay(nil, replaySrc).Available {
		t.Fatalf("nó nil devia ficar indisponível")
	}
}

// ============================================================================
// SUPERFÍCIE — construção fail-closed + spans de interacção (DoD)
// ============================================================================

func TestNew_FailClosedOnNilEngine(t *testing.T) {
	if _, err := New(nil); err != ErrNilEngine {
		t.Fatalf("New(nil engine) devia devolver ErrNilEngine, obtive %v", err)
	}
}

func TestSurfaceSpans_InteractionKindsAndNoSecrets(t *testing.T) {
	rec := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	s := newSurface(t, WithTracer(rec), WithRunID("run-xyz"))

	roots := s.TreeView(context.Background(), trajectory())
	_ = s.DrillDown(context.Background(), findChild(roots[0], 0x03))

	spans := rec.Spans()
	if len(spans) != 2 {
		t.Fatalf("esperava 2 spans de interacção, obtive %d", len(spans))
	}
	tv, dd := spans[0], spans[1]
	if tv.Attributes[AttrTrajectorySurfaceKind] != SurfaceKindTreeView {
		t.Fatalf("tree_view kind: %v", tv.Attributes[AttrTrajectorySurfaceKind])
	}
	if tv.Attributes[AttrTrajectorySurfaceRoots] != int64(1) ||
		tv.Attributes[AttrTrajectorySurfaceSpans] != int64(5) {
		t.Fatalf("tree_view contagens: %+v", tv.Attributes)
	}
	if tv.Attributes[otelgenai.AttrRunID] != "run-xyz" {
		t.Fatalf("tree_view run_id em falta: %+v", tv.Attributes)
	}
	if dd.Attributes[AttrTrajectorySurfaceKind] != SurfaceKindDrillDown ||
		dd.Attributes[AttrTrajectorySurfaceNodeKind] != KindInvokeAgent {
		t.Fatalf("drill_down attrs: %+v", dd.Attributes)
	}
	// SEM SEGREDOS: os spans de interacção só carregam kind/contagens/run_id — nunca
	// valores de conteúdo, nomes de tool, tokens/custo ou taint da trajectória.
	forbidden := []string{
		otelgenai.AttrToolName, otelgenai.AttrResultTaint, otelgenai.AttrInputTokens,
		otelgenai.AttrOutputTokens, otelgenai.AttrCostMicroUSD, otelgenai.AttrToolCallHash,
		otelgenai.AttrPrincipalNHI, "gen_ai.tool.output",
	}
	for _, sp := range spans {
		for _, k := range forbidden {
			if _, present := sp.Attributes[k]; present {
				t.Fatalf("span de interacção vazou o atributo proibido %q: %+v", k, sp.Attributes)
			}
		}
	}
}

func TestDrillDown_NilNode(t *testing.T) {
	rec := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	s := newSurface(t, WithTracer(rec))
	d := s.DrillDown(context.Background(), nil)
	if d.Kind != "" || d.Name != "" {
		t.Fatalf("DrillDown(nil) devia devolver SpanDetail vazio: %+v", d)
	}
	if len(rec.Spans()) != 1 { // ainda emite o span de interacção
		t.Fatalf("DrillDown(nil) devia emitir 1 span, obtive %d", len(rec.Spans()))
	}
}

func TestOptions_TracerNilIgnoredAndSubjectPolicy(t *testing.T) {
	// WithTracer(nil) é ignorado (mantém Noop); WithPolicy/WithRedactionSubject aplicam.
	s := newSurface(t, WithTracer(nil), WithRedactionSubject("titular-x"),
		WithPolicy(redaction.RemoveAllPolicy("custom")))
	if s.red.Subject != "titular-x" {
		t.Fatalf("subject: %q", s.red.Subject)
	}
	if s.red.Policy.Version != "custom" {
		t.Fatalf("policy version: %q", s.red.Policy.Version)
	}
	// Noop tracer não regista nada, mas TreeView continua a funcionar.
	if got := s.TreeView(context.Background(), trajectory()); len(got) != 1 {
		t.Fatalf("TreeView com Noop: %d raízes", len(got))
	}
}

// --- helpers de teste ---------------------------------------------------------

func walk(n *SpanNode, fn func(*SpanNode)) {
	fn(n)
	for _, c := range n.Children {
		walk(c, fn)
	}
}

func deepCopySpans(in []otelgenai.SpanData) []otelgenai.SpanData {
	out := make([]otelgenai.SpanData, len(in))
	for i, sd := range in {
		cp := sd
		cp.Attributes = make([]otelgenai.KeyValue, len(sd.Attributes))
		copy(cp.Attributes, sd.Attributes)
		out[i] = cp
	}
	return out
}

// asInt64/asFloat64/costMicroUSDOf cobertos por helpers directos.
func TestHelperNumberConversions(t *testing.T) {
	if asInt64(int32(7)) != 7 || asInt64(int(9)) != 9 || asInt64(uint64(3)) != 3 {
		t.Fatal("asInt64 conversões inteiras")
	}
	if asInt64(uint64(1)<<63) != 0 { // overflow => 0
		t.Fatal("asInt64 overflow devia dar 0")
	}
	if asInt64("nan") != 0 {
		t.Fatal("asInt64 tipo não-numérico devia dar 0")
	}
	if f, ok := asFloat64(float32(1.5)); !ok || f != 1.5 {
		t.Fatal("asFloat64 float32")
	}
	if _, ok := asFloat64("x"); ok {
		t.Fatal("asFloat64 não-float devia dar ok=false")
	}
}
