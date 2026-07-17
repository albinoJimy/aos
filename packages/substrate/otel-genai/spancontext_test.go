package otelgenai

import (
	"context"
	"testing"
)

func TestSpanContextValidityAndHex(t *testing.T) {
	var zero SpanContext
	if zero.IsValid() {
		t.Fatal("SpanContext zero não devia ser válido")
	}
	// Só trace preenchido → inválido (falta span).
	onlyTrace := SpanContext{TraceID: [16]byte{1}}
	if onlyTrace.IsValid() {
		t.Fatal("SpanContext sem span_id não devia ser válido")
	}
	sc := SpanContext{
		TraceID: [16]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef},
		SpanID:  [8]byte{0xfe, 0xdc, 0xba, 0x98},
	}
	if !sc.IsValid() {
		t.Fatal("SpanContext preenchido devia ser válido")
	}
	if got := sc.TraceIDHex(); got != "0123456789abcdef0000000000000000" {
		t.Errorf("TraceIDHex = %q", got)
	}
	if len(sc.TraceIDHex()) != 32 || len(sc.SpanIDHex()) != 16 {
		t.Errorf("hex de comprimento errado: trace=%d span=%d", len(sc.TraceIDHex()), len(sc.SpanIDHex()))
	}
	if got := sc.SpanIDHex(); got != "fedcba9800000000" {
		t.Errorf("SpanIDHex = %q", got)
	}
}

func TestContextPropagationRoundTrip(t *testing.T) {
	ctx := context.Background()
	if _, ok := SpanContextFromContext(ctx); ok {
		t.Fatal("ctx vazio não devia trazer SpanContext")
	}
	sc := SpanContext{TraceID: [16]byte{9}, SpanID: [8]byte{7}}
	ctx = ContextWithSpanContext(ctx, sc)
	got, ok := SpanContextFromContext(ctx)
	if !ok {
		t.Fatal("SpanContext não propagado")
	}
	if got != sc {
		t.Errorf("propagado = %+v, esperava %+v", got, sc)
	}
}
