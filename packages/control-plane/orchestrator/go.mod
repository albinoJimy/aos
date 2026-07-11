module github.com/aos-ref/control-plane/orchestrator

go 1.24

require (
	github.com/aos-ref/substrate/bus v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

// Barramento (AOS-009) e Event Store (AOS-002) integrados por path local: zero
// dependências externas, build offline. Os replace NÃO são transitivos, por isso
// o eventstore é re-declarado a partir da raiz de packages/. NÃO alterar os
// pacotes referenciados — o Orquestrador consome-os apenas pelas APIs públicas.
replace github.com/aos-ref/substrate/bus => ../../substrate/bus

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore
