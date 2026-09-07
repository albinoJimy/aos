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

// SpanKind é a ESPÉCIE de um span (subconjunto do OTLP SpanKind). O VALOR-ZERO é
// [SpanKindInternal]: um produtor que não declare espécie mantém-se INTERNAL, pelo
// que acrescentar o campo Kind a [SpanData] é ADITIVO e retro-compatível — os
// produtores existentes (todos KEYED) continuam a serializar INTERNAL sem alteração.
// O mapeamento para o inteiro do wire OTLP/JSON (INTERNAL→1, CLIENT→3) vive na
// serialização (ver otlp.go); aqui só se nomeia a espécie de forma type-safe.
type SpanKind int

const (
	// SpanKindInternal — trabalho INTERNO do processo (default, valor-zero). É a
	// espécie de todo o span que não seja uma chamada de saída.
	SpanKindInternal SpanKind = iota
	// SpanKindClient — chamada de SAÍDA a um serviço remoto. A convenção GenAI trata
	// a chamada ao modelo assim; é o que a distingue de trabalho interno no backend.
	SpanKindClient
)

// KindForOperation mapeia uma operação semconv GenAI para a [SpanKind]. A chamada ao
// modelo ([OpChat]) é uma chamada de SAÍDA (CLIENT); tudo o resto fica INTERNAL
// (valor-zero). É a FONTE ÚNICA desta correspondência — partilhada por [StartSpan] e
// pelos produtores de spans sintéticos (ex.: platform/eval) — para não divergirem.
func KindForOperation(operation string) SpanKind {
	// As chamadas de SAÍDA a um serviço remoto (o modelo) são CLIENT: o `chat` e o
	// `embeddings`. Tudo o resto — invoke_agent, execute_tool, activity — é trabalho
	// INTERNO do processo. Acrescentar operações CLIENT aqui é o único ponto a tocar.
	if operation == OpChat || operation == OpEmbeddings {
		return SpanKindClient
	}
	return SpanKindInternal
}

// SpanData é a forma exportável e imutável de um span fechado — a estrutura que o
// [Exporter] recebe. Os timestamps são unix-nano (via um Clock injectável, nunca
// time.Now() directo em código testável). Os ids vivem em SpanContext/ParentSpanID
// e serializam-se em HEX no wire OTLP (ver otlp.go).
type SpanData struct {
	Name          string
	Kind          SpanKind
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
