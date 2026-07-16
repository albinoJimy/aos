module github.com/aos-ref/substrate/sandbox

go 1.24

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

// Integração por path local: ZERO dependências externas, build offline (padrão
// de infra do repo). O SBX compõe (não reimplementa) o Reference Monitor (AOS-003,
// o ÚNICO ponto de invocação da sandbox — ADR-002) e o Event Store (AOS-002, o
// ciclo de vida create/exec/destroy sela aqui). Ambos são módulos zero-dep
// (reference-monitor → eventstore; sem ciclo). NÃO alterar os pacotes referenciados.
replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore
