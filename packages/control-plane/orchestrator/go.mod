module github.com/aos-ref/control-plane/orchestrator

go 1.24

require (
	github.com/aos-ref/control-plane/budget v0.0.0
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/platform/identity v0.0.0
	github.com/aos-ref/substrate/bus v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

// Barramento (AOS-009) e Event Store (AOS-002) integrados por path local: zero
// dependências externas, build offline. Os replace NÃO são transitivos, por isso
// o eventstore é re-declarado a partir da raiz de packages/. NÃO alterar os
// pacotes referenciados — o Orquestrador consome-os apenas pelas APIs públicas.
replace github.com/aos-ref/substrate/bus => ../../substrate/bus

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

// Máquina de estados durável do run (AOS-017), subpacote state do Agent Runtime:
// a autoridade da tabela declarativa de transições (ready→running→complete/failed)
// que o Orquestrador reutiliza por-nó no DAG.
replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

// AOS-026 — delegação a sub-agentes com orçamento herdado COMPÕE três fundações
// (não reimplementa nenhuma): orçamento hierárquico com reserva CAS (AOS-008),
// identidade NHI filha (AOS-005/006) e mediação do Reference Monitor (AOS-003).
replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/control-plane/budget => ../budget

replace github.com/aos-ref/platform/identity => ../../platform/identity
