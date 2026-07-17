module github.com/aos-ref/control-plane/scheduler

go 1.24

require (
	github.com/aos-ref/control-plane/orchestrator v0.0.0
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/bus v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

// Indirectas: puxadas pelo pacote orchestrator (AOS-025/026 — delegation.go usa
// budget + identity). Os replace NÃO são transitivos, por isso re-declaram-se
// aqui para o build do módulo scheduler resolver a cadeia por path local.
require (
	github.com/aos-ref/control-plane/budget v0.0.0
	github.com/aos-ref/platform/identity v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

// Todas as dependências integradas por path local (zero deps externas, build
// offline). Os replace NÃO são transitivos, por isso todos os módulos da cadeia
// (orchestrator/contract, RM, bus, eventstore) são re-declarados a partir da
// raiz de packages/. O Escalonador consome cada um apenas pelas APIs públicas;
// NÃO alterar os pacotes referenciados.
replace github.com/aos-ref/control-plane/orchestrator => ../orchestrator

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/substrate/bus => ../../substrate/bus

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/control-plane/budget => ../budget

replace github.com/aos-ref/platform/identity => ../../platform/identity

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai
