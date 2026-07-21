module github.com/aos-ref/control-plane/governance/confidence-calibration

go 1.24

// AOS-124 — SUPERFÍCIE DE CALIBRAÇÃO DE CONFIANÇA (EPIC-12, UX/DX). Apresenta
// LINGUAGEM DE INCERTEZA de forma SELECTIVA (só quando há sinal de baixa confiança/
// ambiguidade — nunca um disclaimer universal) e expõe o HISTÓRICO DE CORRECÇÕES
// relevante para a acção/contexto corrente. É uma camada de APRESENTAÇÃO/DX que
// COMPÕE e CONSOME os sinais que JÁ existem — NÃO inventa métricas de confiança, NÃO
// recalcula o comportamento do modelo, NÃO expõe PII.
//
// COMPÕE só o LEVE (padrão porta+adaptador, molde: progress-surface/AOS-123):
//   - otel-genai (EPIC-08, AOS-084): o EvaluationResult (Score/Verdict) é o SINAL de
//     incerteza — CONSUMIDO, nunca recalculado. Também a porta Tracer + AttrRunID.
//   - redaction (AOS-091): o Engine (RedactText/ScanText) garante que o histórico
//     apresentado NÃO tem PII em claro (belt-and-suspenders sobre o texto da correcção).
// A FONTE das correcções (os eventos control.steer de AOS-119) fica como PORTA
// (CorrectionSource): o adaptador que os lê do Event Store vive no WIRING — o core não
// arrasta o agent-runtime/control. Build offline; integração por path local (replace).
// otel-genai e redaction são módulos FOLHA (zero deps): sem transitivos a re-declarar.
require (
	github.com/aos-ref/substrate/otel-genai v0.0.0
	github.com/aos-ref/substrate/redaction v0.0.0
)

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai

replace github.com/aos-ref/substrate/redaction => ../../../substrate/redaction
