package otelgenai

import "context"

// Span é um span aberto. SetAttribute acumula atributos (semconv gen_ai.*/aos.*);
// SpanContext expõe a identidade W3C/OTLP para propagação/asserção; End fecha-o
// (e, no [SpanTracer], exporta-o). É deliberadamente mínimo — o adaptador OTel
// real (ver doc.go) implementa esta porta sobre trace.Span.
type Span interface {
	SetAttribute(key string, value any)
	SpanContext() SpanContext
	End()
}

// Tracer é a PORTA de observabilidade. StartSpan abre um span nomeado pela
// operação GenAI, herda o trace_id do pai propagado em ctx (ou gera um novo se
// for raiz), regista o parent_span_id, injecta o novo SpanContext no ctx
// devolvido (para os filhos se ligarem) e devolve o span.
type Tracer interface {
	StartSpan(ctx context.Context, operation string) (context.Context, Span)
}

// ---------------------------------------------------------------------------
// NoopTracer — default (sem observabilidade).
// ---------------------------------------------------------------------------

// NoopTracer descarta todos os spans. É o default do Runtime e do Reference
// Monitor: com ele, o comportamento é idêntico ao de antes da instrumentação.
type NoopTracer struct{}

// StartSpan implementa [Tracer]: não propaga nada e devolve um span inerte.
func (NoopTracer) StartSpan(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) SetAttribute(string, any) {}
func (noopSpan) SpanContext() SpanContext { return SpanContext{} }
func (noopSpan) End()                     {}
