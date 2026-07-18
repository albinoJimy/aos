module github.com/aos-ref/control-plane/governance/hitl

go 1.24

// AOS-095 — Gate HITL concreto (Art. 14): o ConfirmationChannel REAL (não o
// DenyChannel base) que escala acções danger/irreversíveis para um APROVADOR
// AUTORIZADO, com aprovação ASSINADA (não-repúdio), timeout FAIL-CLOSED e
// override-rate exposto. É um módulo GOV que COMPÕE por porta — não reimplementa:
//   - risk (AOS-074): os tipos ConfirmationRequest/Response/Class/tiers; o gate de
//     kernel aceita este Channel como a sua porta ConfirmationChannel.
//   - messaging (AOS-073): a porta [messaging.Signer] (chave privada do aprovador
//     via broker/Vault, server-side — NUNCA neste módulo) é o molde de assinatura.
//   - audit (AOS-072): a cadeia WORM tamper-evident onde a decisão assinada é SELADA.
// Build offline; integração por path local (replace) como no resto do monorepo.
require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/platform/messaging v0.0.0
	// AOS-096 — otel-genai passa a dependência DIRECTA: o RatificationGate compõe a
	// porta EvalGate + EvaluationResult (AOS-084, a pré-condição de eval) como parte do
	// gate de ratificação de auto-modificação. Continua um módulo folha (sem ciclos).
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

// agent-runtime é dependência SÓ-DE-TESTE: o teste de integração comprova que a
// escalada danger↔waiting_on_human e o timeout↔killed são transições válidas da
// máquina durável (EPIC-01/02), ligando o gate HITL ao estado durável exigido pela AC1/AC3.
replace github.com/aos-ref/kernel/agent-runtime => ../../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../../kernel/reference-monitor

replace github.com/aos-ref/platform/audit => ../../../platform/audit

replace github.com/aos-ref/platform/messaging => ../../../platform/messaging

// Transitivos: messaging depende de audit+rm+eventstore+otel-genai; audit depende
// do rm que depende do eventstore/otel-genai. Os replace NÃO são herdados — este
// módulo resolve-os localmente para fechar o build offline.
require github.com/aos-ref/substrate/eventstore v0.0.0 // indirect

replace github.com/aos-ref/substrate/eventstore => ../../../substrate/eventstore

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai
