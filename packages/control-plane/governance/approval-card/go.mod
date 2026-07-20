module github.com/aos-ref/control-plane/governance/approval-card

go 1.24

// AOS-120 — Modelo canónico do APPROVAL-CARD (EPIC-12, UX/HITL). Camada de
// APRESENTAÇÃO que COMPÕE — não reimplementa — os gates existentes:
//   - risk (AOS-074): LÊ Class/Irreversible/Preview/Capability/Resource da
//     [risk.ConfirmationRequest] e devolve a decisão à porta
//     [risk.ConfirmationChannel]. NÃO reclassifica.
//   - redaction (AOS-091): aplica o [redaction.Engine] ao preview/args ANTES de os
//     por no card e prova a ausência de PII em claro ([Engine.Scan] == []).
//   - agent-runtime (AOS-076): a porta [Tracer] para o span de apresentação.
// O ÚNICO enforcement NOVO é a regra dual-control approver_1 != approver_2 para
// acções irreversíveis — tudo o resto (assinatura, autoridade, anti-replay, 4-eyes,
// audit) é do [hitl.Channel] de AOS-095, a que o card DEVOLVE a decisão.
// Build offline; integração por path local (replace) como no resto do monorepo
// (molde: control-plane/governance/hitl/go.mod). ZERO dependências externas.
require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/redaction v0.0.0
)

replace github.com/aos-ref/kernel/agent-runtime => ../../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../../kernel/reference-monitor

replace github.com/aos-ref/substrate/redaction => ../../../substrate/redaction

replace github.com/aos-ref/control-plane/governance/hitl => ../hitl

replace github.com/aos-ref/platform/audit => ../../../platform/audit

replace github.com/aos-ref/platform/messaging => ../../../platform/messaging

// Transitivos: agent-runtime/reference-monitor derivam de otel-genai (folha
// zero-dep); reference-monitor/audit/messaging/hitl arrastam eventstore. Os replace
// NÃO são herdados — este módulo resolve-os localmente para fechar o build offline.
require (
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
)

require (
	github.com/aos-ref/control-plane/governance/hitl v0.0.0-00010101000000-000000000000
	github.com/aos-ref/platform/audit v0.0.0
)

require github.com/aos-ref/platform/messaging v0.0.0 // indirect

replace github.com/aos-ref/substrate/eventstore => ../../../substrate/eventstore

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai
