module github.com/aos-ref/control-plane/governance/plan-approval

go 1.24

// AOS-121 — Gate de APROVAÇÃO-DE-PLANO ANTES DO SPAWN (EPIC-12, UX/HITL). É a
// superfície que apresenta o GRAFO DE TAREFAS proposto pelo orquestrador ao humano
// ANTES de se queimar tokens no spawn (estilo AgentScope), distinta e ANTERIOR aos
// gates de acção (AOS-120). É um módulo GOV (irmão de approval-card/autonomy/hitl)
// que COMPÕE por porta — não reimplementa:
//   - approval-card (AOS-120): COMPÕE []approvalcard.ApprovalCard (um card por nó via
//     approvalcard.BuildCard) — o efeito CONCRETO por nó, já redigido.
//   - autonomy (AOS-089): CONSOME autonomy.Oracle.LevelFor + Oversight().Runs() para a
//     auto-aprovação a níveis altos — não decide/promove o nível.
//   - hitl (AOS-095): DEVOLVE a decisão binária assinada ao risk.ConfirmationChannel
//     (o hitl.Channel: assinatura ed25519 + autoridade + anti-replay + 4-eyes + audit).
//   - agent-runtime (AOS-076): a porta Tracer para o span plan_approval.
// O GRAFO e o SPAWN são PORTAS locais (Plan/Spawner) que o orchestrator.DAG e o
// scheduler.SubtreeSpawner mapeiam no WIRING (documentado; a cadeia Submit->DAG->spawn
// de EPIC-03 está em construção). Build offline; integração por path local (replace)
// como no resto do monorepo (molde: approval-card/go.mod). ZERO dependências externas.
require (
	github.com/aos-ref/control-plane/governance/approval-card v0.0.0
	github.com/aos-ref/control-plane/governance/autonomy v0.0.0
	github.com/aos-ref/control-plane/governance/hitl v0.0.0
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
)

replace github.com/aos-ref/control-plane/governance/approval-card => ../approval-card

replace github.com/aos-ref/control-plane/governance/autonomy => ../autonomy

replace github.com/aos-ref/control-plane/governance/hitl => ../hitl

replace github.com/aos-ref/kernel/agent-runtime => ../../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../../kernel/reference-monitor

replace github.com/aos-ref/platform/audit => ../../../platform/audit

replace github.com/aos-ref/platform/messaging => ../../../platform/messaging

// Transitivos: approval-card arrasta redaction; approval-card/hitl arrastam messaging;
// autonomy/approval-card/hitl arrastam otel-genai; audit/rm/messaging/hitl arrastam
// eventstore. Os replace NÃO são herdados — este módulo resolve-os localmente para
// fechar o build offline.
require (
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
	github.com/aos-ref/substrate/redaction v0.0.0 // indirect
)

require github.com/aos-ref/platform/messaging v0.0.0 // indirect

replace github.com/aos-ref/substrate/eventstore => ../../../substrate/eventstore

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai

replace github.com/aos-ref/substrate/redaction => ../../../substrate/redaction
