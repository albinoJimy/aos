package otelgenai

import (
	"encoding/json"
	"testing"
)

func TestMarshalOTLPShape(t *testing.T) {
	span := SpanData{
		Name: OpExecuteTool,
		SpanContext: SpanContext{
			TraceID: [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
			SpanID:  [8]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22},
		},
		ParentSpanID:  [8]byte{0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0x00},
		StartUnixNano: 111,
		EndUnixNano:   222,
		Attributes: []KeyValue{
			{Key: AttrToolName, Value: "search"},
			{Key: AttrInputTokens, Value: int64(42)},
			{Key: AttrCostUSD, Value: 0.5},
			{Key: "aos.flag", Value: true},
		},
		Status: Status{Code: StatusError, Description: "boom"},
	}
	raw, err := MarshalOTLP([]SpanData{span}, "")
	if err != nil {
		t.Fatalf("MarshalOTLP: %v", err)
	}

	var doc otlpResourceSpans
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("re-unmarshal OTLP: %v", err)
	}
	if len(doc.ResourceSpans) != 1 || len(doc.ResourceSpans[0].ScopeSpans) != 1 {
		t.Fatal("estrutura ResourceSpans→ScopeSpans mal formada")
	}
	ss := doc.ResourceSpans[0].ScopeSpans[0]
	if ss.Scope.Name != ScopeName {
		t.Errorf("scope = %q, esperava %q", ss.Scope.Name, ScopeName)
	}
	if len(ss.Spans) != 1 {
		t.Fatalf("esperava 1 span, obtive %d", len(ss.Spans))
	}
	s := ss.Spans[0]

	// (CA4) ids em HEX.
	if s.TraceID != "0102030405060708090a0b0c0d0e0f10" {
		t.Errorf("traceId hex errado: %q", s.TraceID)
	}
	if s.SpanID != "aabbccddeeff1122" {
		t.Errorf("spanId hex errado: %q", s.SpanID)
	}
	if s.ParentSpanID != "3344556677889900" {
		t.Errorf("parentSpanId hex errado: %q", s.ParentSpanID)
	}
	if s.StartTimeUnixNano != "111" || s.EndTimeUnixNano != "222" {
		t.Errorf("timestamps errados: %q/%q", s.StartTimeUnixNano, s.EndTimeUnixNano)
	}
	if s.Status.Code != int(StatusError) || s.Status.Message != "boom" {
		t.Errorf("status errado: %+v", s.Status)
	}

	// AnyValue por tipo.
	byKey := map[string]otlpAnyValue{}
	for _, kv := range s.Attributes {
		byKey[kv.Key] = kv.Value
	}
	if v := byKey[AttrToolName]; v.StringValue == nil || *v.StringValue != "search" {
		t.Errorf("stringValue errado: %+v", v)
	}
	if v := byKey[AttrInputTokens]; v.IntValue == nil || *v.IntValue != "42" {
		t.Errorf("intValue devia ser string \"42\": %+v", v)
	}
	if v := byKey[AttrCostUSD]; v.DoubleValue == nil || *v.DoubleValue != 0.5 {
		t.Errorf("doubleValue errado: %+v", v)
	}
	if v := byKey["aos.flag"]; v.BoolValue == nil || *v.BoolValue != true {
		t.Errorf("boolValue errado: %+v", v)
	}
}

func TestMarshalOTLPRootHasNoParent(t *testing.T) {
	root := SpanData{
		Name:        OpInvokeAgent,
		SpanContext: SpanContext{TraceID: [16]byte{1}, SpanID: [8]byte{2}},
	}
	raw, err := MarshalOTLP([]SpanData{root}, "custom-scope")
	if err != nil {
		t.Fatalf("MarshalOTLP: %v", err)
	}
	var doc otlpResourceSpans
	_ = json.Unmarshal(raw, &doc)
	s := doc.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if s.ParentSpanID != "" {
		t.Errorf("span raiz não devia emitir parentSpanId, tem %q", s.ParentSpanID)
	}
	if doc.ResourceSpans[0].ScopeSpans[0].Scope.Name != "custom-scope" {
		t.Errorf("scope custom não aplicado")
	}
}

func TestToAnyValueFallback(t *testing.T) {
	// Tipos não previstos caem no stringValue textual, sem perder o atributo.
	v := toAnyValue([]int{1, 2})
	if v.StringValue == nil || *v.StringValue == "" {
		t.Errorf("fallback devia produzir stringValue não-vazio: %+v", v)
	}
	// uint64 e int mapeiam para intValue.
	if iv := toAnyValue(uint64(7)); iv.IntValue == nil || *iv.IntValue != "7" {
		t.Errorf("uint64 devia mapear intValue \"7\": %+v", iv)
	}
	if iv := toAnyValue(int(5)); iv.IntValue == nil || *iv.IntValue != "5" {
		t.Errorf("int devia mapear intValue \"5\": %+v", iv)
	}
}
