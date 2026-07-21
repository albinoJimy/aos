module github.com/aos-ref/qa/ux-dx

go 1.24

// AOS-128 — Bateria de TESTES DE UX/DX dos gates de governação (EPIC-12, o que
// FECHA o epic).
//
// Módulo-FOLHA de teste: NINGUÉM o importa. Por isso pode importar as SUPERFÍCIES
// de governação (approval-card/plan-approval/surface-adapter/progress-surface/
// autonomy-surface) e o gate HITL (hitl) sem violar o layering de produção — é a
// imagem do dr-e2e/scale de EPIC-11. NÃO reimplementa nada: COMPÕE as superfícies
// que AOS-120..125 construíram e CONSOME o override-rate de AOS-095 como sinal
// anti-fadiga. É VALIDAÇÃO (usabilidade/paridade/acessibilidade/anti-fadiga), sem
// enforcement próprio.
//
// Os replace NÃO são transitivos: re-declaram-se aqui TODAS as arestas do fecho das
// superfícies validadas para o build fechar OFFLINE, sem dependências externas
// (o MAIOR risco de integração — molde: qa/dr-e2e/go.mod + os go.mod das superfícies).
require (
	github.com/aos-ref/control-plane/governance/approval-card v0.0.0
	github.com/aos-ref/control-plane/governance/autonomy v0.0.0
	github.com/aos-ref/control-plane/governance/autonomy-surface v0.0.0
	github.com/aos-ref/control-plane/governance/hitl v0.0.0
	github.com/aos-ref/control-plane/governance/plan-approval v0.0.0
	github.com/aos-ref/control-plane/governance/progress-surface v0.0.0
	github.com/aos-ref/control-plane/governance/surface-adapter v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
)

// Transitivos re-declarados (os replace NÃO são herdados): control-surface e budget
// (superfícies compostas indirectamente), agent-runtime (Tracer), messaging (Signer
// do vault de aprovação), eventstore/otel-genai/redaction (folhas do substrato).
require (
	github.com/aos-ref/control-plane/budget v0.0.0 // indirect
	github.com/aos-ref/control-plane/governance/control-surface v0.0.0 // indirect
	github.com/aos-ref/kernel/agent-runtime v0.0.0 // indirect
	github.com/aos-ref/platform/messaging v0.0.0 // indirect
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
	github.com/aos-ref/substrate/redaction v0.0.0 // indirect
)

replace github.com/aos-ref/control-plane/governance/approval-card => ../../control-plane/governance/approval-card

replace github.com/aos-ref/control-plane/governance/plan-approval => ../../control-plane/governance/plan-approval

replace github.com/aos-ref/control-plane/governance/surface-adapter => ../../control-plane/governance/surface-adapter

replace github.com/aos-ref/control-plane/governance/progress-surface => ../../control-plane/governance/progress-surface

replace github.com/aos-ref/control-plane/governance/autonomy-surface => ../../control-plane/governance/autonomy-surface

replace github.com/aos-ref/control-plane/governance/autonomy => ../../control-plane/governance/autonomy

replace github.com/aos-ref/control-plane/governance/control-surface => ../../control-plane/governance/control-surface

replace github.com/aos-ref/control-plane/governance/hitl => ../../control-plane/governance/hitl

replace github.com/aos-ref/control-plane/budget => ../../control-plane/budget

replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/platform/audit => ../../platform/audit

replace github.com/aos-ref/platform/messaging => ../../platform/messaging

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai

replace github.com/aos-ref/substrate/redaction => ../../substrate/redaction
