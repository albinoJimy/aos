package otelgenai

import (
	"context"
	"encoding/hex"
)

// SpanContext é a identidade W3C/OTLP de um span: o trace_id partilhado por toda
// a árvore e o span_id único deste span. Os formatos hex minúsculo de 32/16
// dígitos são os do wire format OTLP, consumíveis por qualquer backend OTel.
type SpanContext struct {
	// TraceID é o identificador de 16 bytes da trajectória inteira (comum a todos
	// os spans da árvore de delegação on-behalf-of).
	TraceID [16]byte
	// SpanID é o identificador de 8 bytes deste span (único na árvore).
	SpanID [8]byte
}

// IsValid indica se o SpanContext está preenchido (trace e span não-nulos). Um
// SpanContext zero (ex.: o de [NoopTracer]) é inválido.
func (sc SpanContext) IsValid() bool {
	return sc.TraceID != [16]byte{} && sc.SpanID != [8]byte{}
}

// TraceIDHex devolve o trace_id em hex minúsculo de 32 dígitos (formato OTLP).
func (sc SpanContext) TraceIDHex() string { return hex.EncodeToString(sc.TraceID[:]) }

// SpanIDHex devolve o span_id em hex minúsculo de 16 dígitos (formato OTLP).
func (sc SpanContext) SpanIDHex() string { return hex.EncodeToString(sc.SpanID[:]) }

// spanIDHex é o hex de um span_id de 8 bytes (usado também para o parent_span_id).
func spanIDHex(id [8]byte) string { return hex.EncodeToString(id[:]) }

// contextKey é a chave privada de propagação do SpanContext no context.Context.
// Sendo de tipo não-exportado, nenhum pacote externo colide com ela.
type contextKey struct{}

// ContextWithSpanContext devolve um ctx derivado que carrega sc. Os StartSpan
// dos filhos lêem-no via [SpanContextFromContext] para herdar o trace_id e
// registar o parent_span_id — é assim que chat/execute_tool se ligam ao
// invoke_agent partilhando o trace e apontando ao pai.
func ContextWithSpanContext(ctx context.Context, sc SpanContext) context.Context {
	return context.WithValue(ctx, contextKey{}, sc)
}

// SpanContextFromContext extrai o SpanContext do pai propagado em ctx. O segundo
// valor é false se não houver nenhum (span raiz).
func SpanContextFromContext(ctx context.Context) (SpanContext, bool) {
	sc, ok := ctx.Value(contextKey{}).(SpanContext)
	return sc, ok
}
