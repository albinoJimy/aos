package otelgenai

import (
	"context"
	"sort"
	"sync"
	"time"
)

// SpanTracer é o Tracer concreto: gera SpanContexts com propagação (herda o
// trace do pai em ctx, regista o parent_span_id) e, ao End() de cada span,
// entrega-o a um [Exporter] em forma de [SpanData]. Zero-dep — o exportador
// OTLP-gRPC/HTTP real é um adapter de deployment DIFERIDO (ver doc.go).
type SpanTracer struct {
	exporter Exporter
	idgen    IDGenerator
	clock    func() time.Time
	scope    string
}

// TracerOption configura o [SpanTracer] na construção.
type TracerOption func(*SpanTracer)

// WithIDGenerator injecta o gerador de ids (default [CryptoIDGenerator] sobre
// crypto/rand). Testes injectam [SequentialIDGenerator] para determinismo.
func WithIDGenerator(g IDGenerator) TracerOption {
	return func(t *SpanTracer) { t.idgen = g }
}

// WithClock injecta o relógio (default time.Now). Nunca chamar time.Now()
// directamente no código testável — segue o padrão de clock injectável do repo.
func WithClock(clock func() time.Time) TracerOption {
	return func(t *SpanTracer) { t.clock = clock }
}

// WithScope define o nome do instrumentation scope emitido no OTLP.
func WithScope(scope string) TracerOption {
	return func(t *SpanTracer) { t.scope = scope }
}

// NewTracer constrói um [SpanTracer] que exporta para exporter. Defaults:
// [CryptoIDGenerator], time.Now, scope "github.com/aos-ref/substrate/otel-genai".
func NewTracer(exporter Exporter, opts ...TracerOption) *SpanTracer {
	t := &SpanTracer{
		exporter: exporter,
		idgen:    NewCryptoIDGenerator(nil),
		clock:    time.Now,
		scope:    ScopeName,
	}
	for _, o := range opts {
		o(t)
	}
	if t.idgen == nil {
		t.idgen = NewCryptoIDGenerator(nil)
	}
	if t.clock == nil {
		t.clock = time.Now
	}
	if t.scope == "" {
		t.scope = ScopeName
	}
	return t
}

// Scope devolve o nome do instrumentation scope deste tracer (para a serialização
// OTLP).
func (t *SpanTracer) Scope() string { return t.scope }

// StartSpan implementa [Tracer]: herda o trace do pai (ou gera novo se raiz),
// gera um span novo, injecta-o no ctx devolvido e regista o parent_span_id.
func (t *SpanTracer) StartSpan(ctx context.Context, operation string) (context.Context, Span) {
	sc, parentSpanID := deriveSpanContext(ctx, t.idgen)
	s := &exportingSpan{
		tracer: t,
		attrs:  make(map[string]any),
	}
	s.data = SpanData{
		Name:          operation,
		SpanContext:   sc,
		ParentSpanID:  parentSpanID,
		StartUnixNano: t.clock().UnixNano(),
	}
	ctx = ContextWithSpanContext(ctx, sc)
	return ctx, s
}

// deriveSpanContext calcula o SpanContext de um span novo a partir do pai
// propagado em ctx (se válido, herda o trace e aponta ao pai; senão, raiz com
// trace novo) usando o gerador dado. É partilhada por [SpanTracer] e
// [RecordingTracer] para que a topologia de propagação seja idêntica.
func deriveSpanContext(ctx context.Context, idgen IDGenerator) (sc SpanContext, parentSpanID [8]byte) {
	if parent, ok := SpanContextFromContext(ctx); ok && parent.IsValid() {
		sc.TraceID = parent.TraceID
		parentSpanID = parent.SpanID
	} else {
		sc.TraceID = idgen.NewTraceID()
	}
	sc.SpanID = idgen.NewSpanID()
	return sc, parentSpanID
}

type exportingSpan struct {
	tracer *SpanTracer

	mu    sync.Mutex
	attrs map[string]any
	data  SpanData
	ended bool
}

func (s *exportingSpan) SetAttribute(key string, value any) {
	s.mu.Lock()
	s.attrs[key] = value
	s.mu.Unlock()
}

func (s *exportingSpan) SpanContext() SpanContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.SpanContext
}

// End fecha o span (uma só vez), materializa os atributos por ordem estável de
// chave e exporta a SpanData. A ordenação torna a saída OTLP determinista.
func (s *exportingSpan) End() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.data.EndUnixNano = s.tracer.clock().UnixNano()
	s.data.Attributes = sortedAttributes(s.attrs)
	if s.data.Status.Code == StatusUnset {
		if _, isErr := s.attrs[AttrErrorType]; isErr {
			s.data.Status = Status{Code: StatusError}
		} else {
			s.data.Status = Status{Code: StatusOK}
		}
	}
	data := s.data
	s.mu.Unlock()
	_ = s.tracer.exporter.Export([]SpanData{data})
}

// sortedAttributes projecta o mapa de atributos num slice ordenado por chave.
func sortedAttributes(attrs map[string]any) []KeyValue {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]KeyValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, KeyValue{Key: k, Value: attrs[k]})
	}
	return out
}
