module github.com/aos-ref/control-plane/governance/surface-adapter

go 1.24

// AOS-122 — PARIDADE DE SUPERFÍCIE: adaptador de plataforma que renderiza o MODELO
// CANÓNICO do approval-card (AOS-120) em Slack/Telegram/desktop com PARIDADE
// FUNCIONAL. É PURA TRADUÇÃO DE APRESENTAÇÃO — COMPÕE, não reimplementa:
//   - approval-card (AOS-120): a FONTE ÚNICA de verdade. Os renderers derivam da
//     MESMA [approvalcard.ApprovalCard]; a decisão é DEVOLVIDA ao gate via o
//     [approvalcard.DualControlCollector]. O adaptador NÃO decide/assina/reclassifica.
//   - control-surface (AOS-119): o contrato versionado [controlsurface.ChannelID]
//     (slack/telegram → chatbot, desktop → desktop), consumido sem acoplamento à impl.
//   - reference-monitor/risk (AOS-074): os tipos de risco LIDOS do card (Class).
//   - agent-runtime (AOS-076): a porta [Tracer] para o span de interacção por canal.
// MODELO OFFLINE (reference model): os "blocos Slack"/"teclados Telegram"/"componentes
// desktop" são ESTRUTURAS DE DADOS deterministas, NÃO chamadas a APIs reais.
// Build offline; integração por path local (replace) como no resto do monorepo
// (molde: control-plane/governance/approval-card/go.mod). ZERO dependências externas.
require (
	github.com/aos-ref/control-plane/governance/approval-card v0.0.0
	github.com/aos-ref/control-plane/governance/control-surface v0.0.0
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
)

replace github.com/aos-ref/control-plane/governance/approval-card => ../approval-card

replace github.com/aos-ref/control-plane/governance/control-surface => ../control-surface

replace github.com/aos-ref/kernel/agent-runtime => ../../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../../kernel/reference-monitor

// Transitivos re-declarados: os replace NÃO são herdados dos módulos requeridos, pelo
// que este módulo resolve-os localmente para fechar o build offline. approval-card
// arrasta redaction/hitl/audit/messaging; approval-card + control-surface derivam a
// superfície de tracing de otel-genai (folha zero-dep); reference-monitor/hitl/audit/
// messaging arrastam eventstore.
require (
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
	github.com/aos-ref/substrate/redaction v0.0.0 // indirect
)

require (
	github.com/aos-ref/control-plane/governance/hitl v0.0.0-00010101000000-000000000000
	github.com/aos-ref/platform/audit v0.0.0
)

require github.com/aos-ref/platform/messaging v0.0.0 // indirect

replace github.com/aos-ref/control-plane/governance/hitl => ../hitl

replace github.com/aos-ref/platform/audit => ../../../platform/audit

replace github.com/aos-ref/platform/messaging => ../../../platform/messaging

replace github.com/aos-ref/substrate/eventstore => ../../../substrate/eventstore

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai

replace github.com/aos-ref/substrate/redaction => ../../../substrate/redaction
