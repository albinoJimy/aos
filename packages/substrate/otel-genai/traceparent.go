package otelgenai

import (
	"encoding/hex"
	"errors"
	"fmt"
)

// Carrier cross-fronteira do SpanContext (AOS-077). A propagação EM-ctx
// ([ContextWithSpanContext]/[SpanContextFromContext]) só liga spans dentro do
// MESMO context.Context; não atravessa a fronteira de serialização de uma
// delegação (RT-pai → Orquestrador.Spawn → RT-filho), onde o sub-agente corre com
// o SEU ctx-raiz novo. Para a sub-árvore do filho herdar o trace_id do pai e
// apontar ao span_id certo é preciso TRANSPORTAR o SpanContext explicitamente como
// string. O veículo é o formato W3C traceparent, para que a ligação seja a nativa
// OTel (trace_id/parent_span_id), não uma reconstrução por atributos NHI.
//
// Formato: "00-<trace_id hex 32>-<span_id hex 16>-<flags hex 2>" (55 chars). Só a
// versão "00" é emitida e aceite (carrier interno, fail-closed). O acréscimo ao
// módulo-folha é puro encoding (encoding/hex + fmt/strings da stdlib) — mantém o
// zero-dep.
const (
	// traceParentVersion é a única versão suportada do W3C traceparent.
	traceParentVersion = "00"
	// traceFlagsSampled — flags "sampled" (bit 0 a 1). O AOS regista SEMPRE a árvore
	// completa no backend (Princípio 4), logo o span propagado é sempre recorded.
	traceFlagsSampled = "01"
	// traceParentLen é o comprimento exacto de "00-<32>-<16>-<2>".
	traceParentLen = 2 + 1 + 32 + 1 + 16 + 1 + 2 // 55
)

// ErrInvalidTraceParent — a string não é decodificável para um SpanContext válido
// (versão/tamanho/hex inválidos, separadores fora do sítio, ou ids all-zero). É a
// sentinela fail-closed de [ParseTraceParent], comparável por errors.Is.
var ErrInvalidTraceParent = errors.New("otelgenai: traceparent inválido")

// FormatTraceParent serializa sc no formato W3C traceparent
// "00-<trace_id hex 32>-<span_id hex 16>-01". É o inverso de [ParseTraceParent].
// O chamador é responsável por só formatar SpanContexts válidos (um sc zero produz
// ids all-zero que [ParseTraceParent] rejeita fail-closed no re-parse).
func FormatTraceParent(sc SpanContext) string {
	return traceParentVersion + "-" + sc.TraceIDHex() + "-" + sc.SpanIDHex() + "-" + traceFlagsSampled
}

// ParseTraceParent decodifica um traceparent num [SpanContext], FAIL-CLOSED:
// rejeita comprimento errado, separadores fora do sítio, versão não suportada, hex
// inválido em qualquer campo, e ids all-zero (que dariam um SpanContext inválido).
// Devolve [ErrInvalidTraceParent] (via %w) em qualquer desses casos — nunca um
// SpanContext parcialmente preenchido. É o ponto de re-injecção do trace do pai no
// ctx-raiz do filho: o resultado alimenta [ContextWithSpanContext].
func ParseTraceParent(s string) (SpanContext, error) {
	if len(s) != traceParentLen {
		return SpanContext{}, fmt.Errorf("%w: comprimento %d (esperado %d)", ErrInvalidTraceParent, len(s), traceParentLen)
	}
	if s[2] != '-' || s[35] != '-' || s[52] != '-' {
		return SpanContext{}, fmt.Errorf("%w: separadores em posição errada", ErrInvalidTraceParent)
	}
	version, traceHex, spanHex, flagsHex := s[0:2], s[3:35], s[36:52], s[53:55]
	if version != traceParentVersion {
		return SpanContext{}, fmt.Errorf("%w: versão %q não suportada", ErrInvalidTraceParent, version)
	}
	// A W3C exige hex MINÚSCULO; hex.DecodeString aceitaria [A-F]. Rejeitamos
	// maiúsculas para o carrier ser estritamente conforme e fail-closed (um produtor
	// que emita hex maiúsculo é recusado em vez de aceite silenciosamente).
	if !isLowerHex(traceHex) || !isLowerHex(spanHex) || !isLowerHex(flagsHex) {
		return SpanContext{}, fmt.Errorf("%w: hex maiúsculo não conforme (W3C exige minúsculo)", ErrInvalidTraceParent)
	}
	// flags não participa na ligação, mas rejeitamos lixo não-hex (fail-closed).
	if _, err := hex.DecodeString(flagsHex); err != nil {
		return SpanContext{}, fmt.Errorf("%w: flags %q não-hex", ErrInvalidTraceParent, flagsHex)
	}
	traceRaw, err := hex.DecodeString(traceHex)
	if err != nil {
		return SpanContext{}, fmt.Errorf("%w: trace_id não-hex", ErrInvalidTraceParent)
	}
	spanRaw, err := hex.DecodeString(spanHex)
	if err != nil {
		return SpanContext{}, fmt.Errorf("%w: span_id não-hex", ErrInvalidTraceParent)
	}
	var sc SpanContext
	copy(sc.TraceID[:], traceRaw)
	copy(sc.SpanID[:], spanRaw)
	if !sc.IsValid() {
		return SpanContext{}, fmt.Errorf("%w: ids all-zero", ErrInvalidTraceParent)
	}
	return sc, nil
}

// isLowerHex reporta se s contém apenas [0-9a-f] (hex minúsculo estrito, como a
// W3C traceparent exige). hex.DecodeString sozinho aceitaria também [A-F].
func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
