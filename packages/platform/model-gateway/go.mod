module github.com/aos-ref/platform/model-gateway

go 1.24

require (
	github.com/aos-ref/control-plane/scheduler v0.0.0
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/platform/identity v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

require (
	github.com/aos-ref/control-plane/budget v0.0.0 // indirect
	github.com/aos-ref/control-plane/orchestrator v0.0.0 // indirect
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/substrate/bus v0.0.0 // indirect
)

// Integração por path local: ZERO dependências externas, build offline. O GW
// consome a porta agentruntime.Tracer (zero-dep) e alinha o contrato ModelClient
// (AOS-013). Os replace de um módulo dependente NÃO são transitivos, pelo que o
// reference-monitor e o eventstore (dependências transitivas do agent-runtime)
// são re-declarados aqui a partir da raiz de packages/.
//
// AOS-057 compõe (não reimplementa) a IDENTIDADE (AOS-005/006, platform/identity)
// e o audit WORM (AOS-011, platform/audit): o estágio de authn reutiliza o
// Verifier/Principal do token NHI e a atribuição sela no hash-chain tamper-evident.
// Ambos são módulos zero-dep (stdlib) integrados por path local.
replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/platform/identity => ../identity

replace github.com/aos-ref/platform/audit => ../audit

// AOS-059 (router cost/load-aware): o adaptador de fronteira routing/tieradapter
// SATISFAZ scheduler.ModelTierRouter (a porta de tiering do Escalonador, AOS-031) e
// COORDENA com o admission control global (ADR-008). É o ÚNICO ponto do GW que
// importa control-plane — o núcleo do roteamento é zero-dep de control-plane. Os
// replace NÃO são transitivos, por isso a cadeia do scheduler (orchestrator,
// budget, bus) é re-declarada aqui a partir da raiz de packages/.
replace github.com/aos-ref/control-plane/scheduler => ../../control-plane/scheduler

replace github.com/aos-ref/control-plane/orchestrator => ../../control-plane/orchestrator

replace github.com/aos-ref/control-plane/budget => ../../control-plane/budget

replace github.com/aos-ref/substrate/bus => ../../substrate/bus
