package otelgenai

import (
	"context"
	"testing"
	"time"
)

// fixedClock devolve um relógio que avança 1ms por leitura, para timestamps
// deterministas e monotónicos.
func fixedClock() func() time.Time {
	base := time.Unix(1_700_000_000, 0).UTC()
	n := int64(0)
	return func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Millisecond)
	}
}

// TestSpanTreeWellFormed é o teste de INTEGRAÇÃO da árvore: simula
// invoke_agent → chat → execute_tool e prova trace_id comum, parent_span_id
// correcto e exportação bem-formada (CA3/CA4 de AOS-076).
func TestSpanTreeWellFormed(t *testing.T) {
	exp := &RecordingExporter{}
	tr := NewTracer(exp, WithIDGenerator(&SequentialIDGenerator{}), WithClock(fixedClock()))

	// invoke_agent (raiz).
	ctx, agent := tr.StartSpan(context.Background(), OpInvokeAgent)
	agent.SetAttribute(AttrOperationName, OpInvokeAgent)
	agent.SetAttribute(AttrRunID, "run-1")

	// chat (filho de invoke_agent).
	chatCtx, chat := tr.StartSpan(ctx, OpChat)
	chat.SetAttribute(AttrOperationName, OpChat)
	chat.SetAttribute(AttrRequestModel, "claude-opus-4-8")
	chat.SetAttribute(AttrPrincipalNHI, "nhi:agent-1")
	_ = chatCtx
	chat.End()

	// execute_tool (filho de invoke_agent — o chat já fechou; a mediação nasce do
	// ctx do invoke_agent, como no loop real).
	_, tool := tr.StartSpan(ctx, OpExecuteTool)
	tool.SetAttribute(AttrOperationName, OpExecuteTool)
	tool.SetAttribute(AttrToolName, "search")
	tool.SetAttribute(AttrToolCallHash, "deadbeef")
	tool.SetAttribute(AttrTaint, "untrusted")
	tool.SetAttribute(AttrResultTaint, "untrusted")
	tool.End()

	agent.End()

	spans := exp.Spans()
	if len(spans) != 3 {
		t.Fatalf("esperava 3 spans exportados, obtive %d", len(spans))
	}
	agentSpan := exp.SpansByName(OpInvokeAgent)
	chatSpan := exp.SpansByName(OpChat)
	toolSpan := exp.SpansByName(OpExecuteTool)
	if len(agentSpan) != 1 || len(chatSpan) != 1 || len(toolSpan) != 1 {
		t.Fatalf("contagem por operação errada: agent=%d chat=%d tool=%d", len(agentSpan), len(chatSpan), len(toolSpan))
	}

	root := agentSpan[0]
	// A raiz não tem pai.
	if root.ParentSpanID != ([8]byte{}) {
		t.Errorf("invoke_agent (raiz) não devia ter parent_span_id")
	}
	if !root.SpanContext.IsValid() {
		t.Errorf("invoke_agent devia ter SpanContext válido")
	}

	trace := root.SpanContext.TraceID
	for _, child := range []SpanData{chatSpan[0], toolSpan[0]} {
		// (CA4) trace_id comum.
		if child.SpanContext.TraceID != trace {
			t.Errorf("filho %q não partilha o trace_id da raiz", child.Name)
		}
		// (CA4) parent_span_id = span_id do invoke_agent.
		if child.ParentSpanID != root.SpanContext.SpanID {
			t.Errorf("filho %q parent_span_id != span_id do invoke_agent", child.Name)
		}
		// span_id próprio distinto do pai.
		if child.SpanContext.SpanID == root.SpanContext.SpanID {
			t.Errorf("filho %q reutilizou o span_id do pai", child.Name)
		}
	}

	// Todos conformes com a semconv.
	for _, s := range spans {
		if err := ValidateSpanData(s); err != nil {
			t.Errorf("span não conforme: %v", err)
		}
	}
	// Status derivado: sem error.type ⇒ OK.
	if toolSpan[0].Status.Code != StatusOK {
		t.Errorf("execute_tool sem erro devia ter Status OK, tem %v", toolSpan[0].Status.Code)
	}
	// Timestamps preenchidos e ordenados.
	if root.StartUnixNano == 0 || root.EndUnixNano <= root.StartUnixNano {
		t.Errorf("timestamps do invoke_agent mal formados: start=%d end=%d", root.StartUnixNano, root.EndUnixNano)
	}
}

// TestErrorTypeDrivesStatus prova que um span com error.type sai com Status Error.
func TestErrorTypeDrivesStatus(t *testing.T) {
	exp := &RecordingExporter{}
	tr := NewTracer(exp, WithIDGenerator(&SequentialIDGenerator{}), WithClock(fixedClock()))
	_, s := tr.StartSpan(context.Background(), OpExecuteTool)
	s.SetAttribute(AttrErrorType, "boom")
	s.End()
	got := exp.SpansByName(OpExecuteTool)
	if len(got) != 1 || got[0].Status.Code != StatusError {
		t.Fatalf("esperava Status Error com error.type, obtive %+v", got)
	}
}

// TestDoubleEndExportsOnce prova que End é idempotente (não duplica exportações).
func TestDoubleEndExportsOnce(t *testing.T) {
	exp := &RecordingExporter{}
	tr := NewTracer(exp, WithIDGenerator(&SequentialIDGenerator{}), WithClock(fixedClock()))
	_, s := tr.StartSpan(context.Background(), OpChat)
	s.End()
	s.End()
	if got := len(exp.Spans()); got != 1 {
		t.Fatalf("End duplo exportou %d spans, esperava 1", got)
	}
}

// TestSpanContextExposedBeforeEnd prova que o SpanContext é legível no span aberto
// (indispensável para propagação/correlação em runtime).
func TestSpanContextExposedBeforeEnd(t *testing.T) {
	tr := NewTracer(&RecordingExporter{}, WithIDGenerator(&SequentialIDGenerator{}))
	ctx, s := tr.StartSpan(context.Background(), OpInvokeAgent)
	if !s.SpanContext().IsValid() {
		t.Fatal("SpanContext do span aberto devia ser válido")
	}
	// O ctx devolvido carrega o mesmo SpanContext (propagação).
	prop, ok := SpanContextFromContext(ctx)
	if !ok || prop != s.SpanContext() {
		t.Fatal("ctx devolvido não propaga o SpanContext do span")
	}
	s.End()
}

// TestNoopTracerInert prova que o NoopTracer não propaga nem entra em pânico.
func TestNoopTracerInert(t *testing.T) {
	ctx, s := NoopTracer{}.StartSpan(context.Background(), OpChat)
	if _, ok := SpanContextFromContext(ctx); ok {
		t.Fatal("NoopTracer não devia propagar SpanContext")
	}
	if s.SpanContext().IsValid() {
		t.Fatal("noopSpan não devia ter SpanContext válido")
	}
	s.SetAttribute("k", "v")
	s.End()
}
