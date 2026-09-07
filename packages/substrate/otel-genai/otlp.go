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

// otlpResourceBody carrega os atributos de RECURSO (ex.: service.name). Deixou de
// ficar sempre no valor-zero (AOS-368): [MarshalOTLP] povoa-o a partir das opções.
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

// ServiceNameAttr é a chave semconv do atributo de recurso que dá IDENTIDADE ao
// produtor no backend OTel. Sem ela, um trace chega atribuído a `unknown_service`
// (AOS-368). Vive aqui, na serialização, porque é aqui que o recurso é montado.
const ServiceNameAttr = "service.name"

// MarshalOption configura [MarshalOTLP]. É VARIÁDICA e retro-compatível: sem
// opções, o documento sai como antes (recurso vazio) — os chamadores existentes não
// mudam de forma.
type MarshalOption func(*marshalConfig)

type marshalConfig struct {
	resource []otlpKeyValue
}

// WithServiceName injecta o atributo de recurso [ServiceNameAttr] (service.name) no
// documento OTLP — a identidade que o backend usa para atribuir o trace. Vazio ⇒ não
// injecta (mantém o comportamento anterior, recurso vazio). Repetir a opção com o
// mesmo valor acumula atributos, mas o exporter passa-a uma só vez.
func WithServiceName(name string) MarshalOption {
	return func(c *marshalConfig) {
		if name == "" {
			return
		}
		v := name
		c.resource = append(c.resource, otlpKeyValue{
			Key:   ServiceNameAttr,
			Value: otlpAnyValue{StringValue: &v},
		})
	}
}

// MarshalOTLP serializa spans no documento OTLP/JSON de traces, com um único
// ResourceSpans e um ScopeSpans do scope dado. Os atributos de RECURSO (ex.:
// service.name via [WithServiceName]) povoam `resourceSpans[0].resource.attributes`;
// sem opções o recurso fica vazio (retro-compatível). Os ids saem em HEX.
// Determinista para a mesma entrada.
func MarshalOTLP(spans []SpanData, scope string, opts ...MarshalOption) ([]byte, error) {
	if scope == "" {
		scope = ScopeName
	}
	var cfg marshalConfig
	for _, o := range opts {
		o(&cfg)
	}
	out := make([]otlpSpan, 0, len(spans))
	for _, s := range spans {
		out = append(out, toOTLPSpan(s))
	}
	doc := otlpResourceSpans{
		ResourceSpans: []otlpResource{{
			Resource: otlpResourceBody{Attributes: cfg.resource},
			ScopeSpans: []otlpScopeSpans{{
				Scope: otlpScope{Name: scope},
				Spans: out,
			}},
		}},
	}
	return json.Marshal(doc)
}

// otlpSpanKind mapeia a [SpanKind] interna para o inteiro do wire OTLP/JSON. Só
// INTERNAL (1) e CLIENT (3) são necessários hoje; SERVER=2, PRODUCER=4 e CONSUMER=5
// existem na spec OTLP mas nenhum produtor os declara. Uma espécie desconhecida cai
// no valor-zero seguro (INTERNAL→1), preservando a compatibilidade dos produtores.
func otlpSpanKind(k SpanKind) int {
	switch k {
	case SpanKindClient:
		return 3 // SPAN_KIND_CLIENT
	default:
		return 1 // SPAN_KIND_INTERNAL
	}
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
		Kind:              otlpSpanKind(s.Kind), // AOS-368: espécie declarada (INTERNAL→1, CLIENT→3), já não fixa
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
