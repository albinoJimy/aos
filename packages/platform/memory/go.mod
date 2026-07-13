module github.com/aos-ref/platform/memory

go 1.24

require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

require github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect

// Integração por path local, ZERO dependências externas, build offline.
//
// O Event Store (AOS-002, ADR-007) é a fonte de verdade da memória; o adaptador
// de ES escreve eventos append-only e RECONSTRÓI a leitura do log.
//
// O Agent Runtime (AOS-013) fornece a PORTA Tracer/Span zero-dep para os spans
// OTel gen_ai.* emitidos por cada operação de porta. Como os replace de um módulo
// dependente NÃO são transitivos, o reference-monitor (dependência transitiva do
// agent-runtime) e o eventstore são re-declarados aqui a partir da raiz de
// packages/. NÃO alterar os pacotes referenciados (AOS-002/003/013 são Done).
replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor
