module github.com/aos-ref/control-plane/scheduler

go 1.24

require (
	github.com/aos-ref/control-plane/orchestrator v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/bus v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

// Todas as dependências integradas por path local (zero deps externas, build
// offline). Os replace NÃO são transitivos, por isso todos os módulos da cadeia
// (orchestrator/contract, RM, bus, eventstore) são re-declarados a partir da
// raiz de packages/. O Escalonador consome cada um apenas pelas APIs públicas;
// NÃO alterar os pacotes referenciados.
replace github.com/aos-ref/control-plane/orchestrator => ../orchestrator

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/bus => ../../substrate/bus

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore
