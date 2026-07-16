module github.com/aos-ref/platform/broker

go 1.24

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
	github.com/aos-ref/substrate/sandbox v0.0.0
)

// Integração por path local: ZERO dependências externas, build offline (padrão
// de infra do repo). O broker (BRK, AOS-070/ADR-006) COMPÕE — não reimplementa —
// o Reference Monitor (AOS-003, medeia a troca), o Event Store (AOS-002, sela o
// registo da troca sem o valor) e o SBX (AOS-064, cujo credentials_handle opaco é
// resolvido/injectado server-side por este módulo). Nenhum destes importa o
// broker, logo NÃO há ciclo. NÃO alterar os pacotes referenciados.
replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/substrate/sandbox => ../../substrate/sandbox

// O SBX depende transitivamente do audit (AOS-011); resolve-se o path local para
// que o build feche sem rede. Os replace de um módulo dependente NÃO são
// transitivos, pelo que audit/reference-monitor/eventstore são re-declarados aqui
// a partir da raiz de packages/.
replace github.com/aos-ref/platform/audit => ../audit
