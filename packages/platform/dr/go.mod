module github.com/aos-ref/platform/dr

go 1.24

// AOS-102 — DR por replay determinístico (RPO/RTO definidos). O módulo COMPÕE as
// peças já Done, um nível acima de engine.NewOwnContractEngine, SEM reimplementar
// restauro/replay/resume/verify/idempotência/soberania:
//   - platform/backup (AOS-101) para o RESTAURO+PITR do log e a medição de RPO;
//   - platform/audit (AOS-072/083) para a VERIFICAÇÃO hash-chain do WORM pós-restauro;
//   - kernel/agent-runtime (replay/worker/durable) para PROVAR fidelidade e RETOMAR
//     resume-from-step com efeitos idempotentes;
//   - substrate/eventstore para o Event Store de DR LIMPO na fronteira de soberania.
//
// LAYERING (crítico): platform/dr NÃO importa control-plane/governance/* (seria um
// up-import ilegal platform→control-plane). A resolução board→região é INJECTADA
// pelo chamador (BoundaryResolver) e a soberania é REFORÇADA pelo próprio guard do
// eventstore (WithSovereigntyBoard recusa cross-border por construção) mais uma
// ASSERÇÃO no orquestrador de que a região do Store restaurado == região-alvo.
//
// Os replace NÃO são transitivos: re-declaram-se aqui TODAS as arestas do fecho
// (backup, audit, agent-runtime, eventstore + as indirectas reference-monitor e
// otel-genai) para o build fechar OFFLINE.
require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/platform/backup v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
)

replace github.com/aos-ref/platform/backup => ../backup

replace github.com/aos-ref/platform/audit => ../audit

replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai
