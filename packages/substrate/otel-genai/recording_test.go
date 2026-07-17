package otelgenai

import (
	"context"
	"testing"
)

// TestRecordingTracerCapturesAttributes prova a captura de operação/atributos/End
// (a superfície que o RT/RM usam nos testes).
func TestRecordingTracerCapturesAttributes(t *testing.T) {
	tr := &RecordingTracer{} // valor-zero utilizável
	_, s := tr.StartSpan(context.Background(), OpChat)
	s.SetAttribute(AttrOperationName, OpChat)
	s.SetAttribute(AttrInputTokens, int64(10))
	if got := tr.SpansByOperation(OpChat); len(got) != 1 {
		t.Fatalf("esperava 1 span chat, obtive %d", len(got))
	}
	rec := tr.SpansByOperation(OpChat)[0]
	if rec.Attributes[AttrInputTokens] != int64(10) {
		t.Errorf("atributo não capturado: %v", rec.Attributes[AttrInputTokens])
	}
	if rec.Ended {
		t.Error("span não devia estar fechado antes de End")
	}
	s.End()
	if !tr.SpansByOperation(OpChat)[0].Ended {
		t.Error("End não marcou o span")
	}
	if len(tr.Spans()) != 1 {
		t.Errorf("Spans() = %d, esperava 1", len(tr.Spans()))
	}
}

// TestRecordingTracerPropagatesTopology prova que o RecordingTracer também
// reconstitui a árvore (trace comum, parent_span_id) — o que permite ao teste de
// integração do RT/RM asserir a topologia sem exportador.
func TestRecordingTracerPropagatesTopology(t *testing.T) {
	tr := NewRecordingTracer(&SequentialIDGenerator{})
	ctx, root := tr.StartSpan(context.Background(), OpInvokeAgent)
	_, child := tr.StartSpan(ctx, OpExecuteTool)
	child.End()
	root.End()

	r := tr.SpansByOperation(OpInvokeAgent)[0]
	c := tr.SpansByOperation(OpExecuteTool)[0]
	if r.ParentSpanID != ([8]byte{}) {
		t.Error("raiz não devia ter pai")
	}
	if c.SpanContext.TraceID != r.SpanContext.TraceID {
		t.Error("filho não partilha o trace_id da raiz")
	}
	if c.ParentSpanID != r.SpanContext.SpanID {
		t.Error("parent_span_id do filho != span_id da raiz")
	}
	// SpanContext() do span vivo reflecte o registado.
	if child.SpanContext() != c.SpanContext {
		t.Error("SpanContext() do span diverge do capturado")
	}
}

// TestRecordedSpanToSpanData liga a captura ao validador de contrato.
func TestRecordedSpanToSpanData(t *testing.T) {
	tr := NewRecordingTracer(&SequentialIDGenerator{})
	_, s := tr.StartSpan(context.Background(), OpExecuteTool)
	s.SetAttribute(AttrOperationName, OpExecuteTool)
	s.SetAttribute(AttrToolName, "search")
	s.SetAttribute(AttrToolCallHash, "abc")
	s.SetAttribute(AttrTaint, "untrusted")
	s.SetAttribute(AttrResultTaint, "untrusted")
	s.End()
	sd := tr.SpansByOperation(OpExecuteTool)[0].ToSpanData()
	if err := ValidateSpanData(sd); err != nil {
		t.Errorf("SpanData projectada devia ser conforme: %v", err)
	}
}
