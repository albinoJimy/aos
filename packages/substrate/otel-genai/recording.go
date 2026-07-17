package otelgenai

import (
	"context"
	"sync"
)

// RecordedSpan é um span capturado pelo [RecordingTracer]: a operação, os
// atributos acumulados, se foi fechado, e a sua identidade/parentesco de trace
// (para asserir a topologia da árvore sem um exportador). Os campos de contexto
// são aditivos — código que só lê Operation/Attributes/Ended mantém-se válido.
type RecordedSpan struct {
	Operation    string
	Attributes   map[string]any
	Ended        bool
	SpanContext  SpanContext
	ParentSpanID [8]byte
}

// RecordingTracer capta todos os spans abertos, os seus atributos e a sua
// topologia de propagação (trace_id herdado do pai, parent_span_id registado). É
// o Tracer de teste do RT/RM: leve, sem exportador, seguro para concorrência. O
// valor-zero (`&RecordingTracer{}`) é utilizável — o gerador de ids é criado
// preguiçosamente na primeira abertura de span.
type RecordingTracer struct {
	mu    sync.Mutex
	idgen IDGenerator
	spans []*RecordedSpan
}

// NewRecordingTracer constrói um RecordingTracer com um gerador de ids explícito
// (ex.: [SequentialIDGenerator] para topologia determinista). Passe nil para o
// default CSPRNG.
func NewRecordingTracer(idgen IDGenerator) *RecordingTracer {
	return &RecordingTracer{idgen: idgen}
}

// StartSpan implementa [Tracer]: propaga o trace do pai (ou gera novo se raiz),
// regista o parent_span_id e injecta o novo SpanContext no ctx devolvido.
func (t *RecordingTracer) StartSpan(ctx context.Context, operation string) (context.Context, Span) {
	t.mu.Lock()
	if t.idgen == nil {
		t.idgen = NewCryptoIDGenerator(nil)
	}
	sc, parentSpanID := deriveSpanContext(ctx, t.idgen)
	rs := &RecordedSpan{
		Operation:    operation,
		Attributes:   make(map[string]any),
		SpanContext:  sc,
		ParentSpanID: parentSpanID,
	}
	t.spans = append(t.spans, rs)
	t.mu.Unlock()
	ctx = ContextWithSpanContext(ctx, sc)
	return ctx, &recordingSpan{tracer: t, rec: rs}
}

// Spans devolve uma cópia (shallow) da lista de spans capturados, por ordem de
// abertura.
func (t *RecordingTracer) Spans() []*RecordedSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*RecordedSpan, len(t.spans))
	copy(out, t.spans)
	return out
}

// SpansByOperation devolve os spans cuja operação é a dada.
func (t *RecordingTracer) SpansByOperation(op string) []*RecordedSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*RecordedSpan
	for _, s := range t.spans {
		if s.Operation == op {
			out = append(out, s)
		}
	}
	return out
}

type recordingSpan struct {
	tracer *RecordingTracer
	rec    *RecordedSpan
}

func (s *recordingSpan) SetAttribute(key string, value any) {
	s.tracer.mu.Lock()
	s.rec.Attributes[key] = value
	s.tracer.mu.Unlock()
}

func (s *recordingSpan) SpanContext() SpanContext {
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	return s.rec.SpanContext
}

func (s *recordingSpan) End() {
	s.tracer.mu.Lock()
	s.rec.Ended = true
	s.tracer.mu.Unlock()
}

// ToSpanData projecta um RecordedSpan em [SpanData] (ex.: para o alimentar ao
// validador de contrato [ValidateSpanData] ou à serialização OTLP). Os
// timestamps ficam a zero — o RecordingTracer não modela relógio.
func (rs *RecordedSpan) ToSpanData() SpanData {
	return SpanData{
		Name:         rs.Operation,
		SpanContext:  rs.SpanContext,
		ParentSpanID: rs.ParentSpanID,
		Attributes:   sortedAttributes(rs.Attributes),
	}
}
