package otelgenai

import (
	"encoding/json"
	"strconv"
)

// ScopeName é o nome do instrumentation scope emitido no OTLP (a "biblioteca" que
// produziu os spans).
const ScopeName = "github.com/aos-ref/substrate/otel-genai"

// Este ficheiro serializa [SpanData] no wire format OTLP/JSON (ResourceSpans →
// ScopeSpans → Span) com traceId/spanId/parentSpanId em HEX minúsculo, para ser
// consumível por qualquer backend compatível com OTel. Só encoding/json stdlib —
// o exportador OTLP-gRPC/HTTP real é um adapter de deployment DIFERIDO (doc.go).

// otlpResourceSpans é o topo do documento OTLP/JSON de traces.
type otlpResourceSpans struct {
	ResourceSpans []otlpResource `json:"resourceSpans"`
}

type otlpResource struct {
	Resource   otlpResourceBody `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResourceBody struct {
	Attributes []otlpKeyValue `json:"attributes,omitempty"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId,omitempty"`
	Name              string         `json:"name"`
	Kind              int            `json:"kind"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	EndTimeUnixNano   string         `json:"endTimeUnixNano"`
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
	Status            otlpStatus     `json:"status"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

// otlpAnyValue é o AnyValue do OTLP: exactamente um campo preenchido. int64
// serializa-se como string (convenção OTLP/JSON para proto3 int64).
type otlpAnyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
}

// MarshalOTLP serializa spans no documento OTLP/JSON de traces, com um único
// ResourceSpans (recurso vazio) e um ScopeSpans do scope dado. Os ids saem em
// HEX. Determinista para a mesma entrada.
func MarshalOTLP(spans []SpanData, scope string) ([]byte, error) {
	if scope == "" {
		scope = ScopeName
	}
	out := make([]otlpSpan, 0, len(spans))
	for _, s := range spans {
		out = append(out, toOTLPSpan(s))
	}
	doc := otlpResourceSpans{
		ResourceSpans: []otlpResource{{
			ScopeSpans: []otlpScopeSpans{{
				Scope: otlpScope{Name: scope},
				Spans: out,
			}},
		}},
	}
	return json.Marshal(doc)
}

// toOTLPSpan converte uma [SpanData] no span OTLP correspondente.
func toOTLPSpan(s SpanData) otlpSpan {
	var parent string
	if s.ParentSpanID != ([8]byte{}) {
		parent = spanIDHex(s.ParentSpanID)
	}
	attrs := make([]otlpKeyValue, 0, len(s.Attributes))
	for _, kv := range s.Attributes {
		attrs = append(attrs, otlpKeyValue{Key: kv.Key, Value: toAnyValue(kv.Value)})
	}
	return otlpSpan{
		TraceID:           s.SpanContext.TraceIDHex(),
		SpanID:            s.SpanContext.SpanIDHex(),
		ParentSpanID:      parent,
		Name:              s.Name,
		Kind:              1, // SPAN_KIND_INTERNAL
		StartTimeUnixNano: strconv.FormatInt(s.StartUnixNano, 10),
		EndTimeUnixNano:   strconv.FormatInt(s.EndUnixNano, 10),
		Attributes:        attrs,
		Status:            otlpStatus{Code: int(s.Status.Code), Message: s.Status.Description},
	}
}

// toAnyValue mapeia um valor de atributo Go para o AnyValue OTLP.
func toAnyValue(v any) otlpAnyValue {
	switch x := v.(type) {
	case string:
		return otlpAnyValue{StringValue: &x}
	case bool:
		return otlpAnyValue{BoolValue: &x}
	case int:
		s := strconv.FormatInt(int64(x), 10)
		return otlpAnyValue{IntValue: &s}
	case int32:
		s := strconv.FormatInt(int64(x), 10)
		return otlpAnyValue{IntValue: &s}
	case int64:
		s := strconv.FormatInt(x, 10)
		return otlpAnyValue{IntValue: &s}
	case uint64:
		s := strconv.FormatUint(x, 10)
		return otlpAnyValue{IntValue: &s}
	case float32:
		f := float64(x)
		return otlpAnyValue{DoubleValue: &f}
	case float64:
		return otlpAnyValue{DoubleValue: &x}
	default:
		// Fallback conservador: representação textual estável, sem perder o atributo.
		s := stringify(v)
		return otlpAnyValue{StringValue: &s}
	}
}
