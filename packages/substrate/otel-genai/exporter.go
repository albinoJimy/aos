package otelgenai

import "sync"

// KeyValue é um atributo de span (chave semconv + valor). O valor é any porque a
// semconv mistura string/int/float/bool; a serialização OTLP mapeia cada tipo
// para o AnyValue correspondente (ver otlp.go).
type KeyValue struct {
	Key   string
	Value any
}

// StatusCode é o código de estado de um span (subconjunto do OTLP Status).
type StatusCode int

const (
	// StatusUnset — sem estado explícito (default).
	StatusUnset StatusCode = iota
	// StatusOK — o span concluiu com sucesso.
	StatusOK
	// StatusError — o span concluiu em erro.
	StatusError
)

// Status é o estado final de um span (código + descrição legível).
type Status struct {
	Code        StatusCode
	Description string
}

// SpanData é a forma exportável e imutável de um span fechado — a estrutura que o
// [Exporter] recebe. Os timestamps são unix-nano (via um Clock injectável, nunca
// time.Now() directo em código testável). Os ids vivem em SpanContext/ParentSpanID
// e serializam-se em HEX no wire OTLP (ver otlp.go).
type SpanData struct {
	Name          string
	SpanContext   SpanContext
	ParentSpanID  [8]byte
	StartUnixNano int64
	EndUnixNano   int64
	Attributes    []KeyValue
	Status        Status
}

// Attribute devolve o valor do atributo key e se ele existe. Conveniência para
// validadores/testes lerem a SpanData sem varrer o slice.
func (sd SpanData) Attribute(key string) (any, bool) {
	for _, kv := range sd.Attributes {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return nil, false
}

// Exporter é o sink de spans fechados. A implementação de produção (adapter OTLP-
// gRPC/HTTP sobre o SDK go.opentelemetry.io) é um adapter de deployment DIFERIDO
// (ver doc.go); aqui fornece-se o [RecordingExporter] in-memory para testes e
// integração, e a serialização OTLP/JSON zero-dep (ver otlp.go).
type Exporter interface {
	Export(spans []SpanData) error
}

// RecordingExporter acumula em memória os spans exportados. É seguro para
// concorrência (o loop pode, no futuro, exportar de goroutines).
type RecordingExporter struct {
	mu    sync.Mutex
	spans []SpanData
}

// Export implementa [Exporter].
func (e *RecordingExporter) Export(spans []SpanData) error {
	e.mu.Lock()
	e.spans = append(e.spans, spans...)
	e.mu.Unlock()
	return nil
}

// Spans devolve uma cópia dos spans acumulados, na ordem de exportação.
func (e *RecordingExporter) Spans() []SpanData {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]SpanData, len(e.spans))
	copy(out, e.spans)
	return out
}

// SpansByName devolve os spans cujo Name (operação) é o dado.
func (e *RecordingExporter) SpansByName(name string) []SpanData {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []SpanData
	for _, s := range e.spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}
