module github.com/aos-ref/platform/registry

go 1.24

require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

require github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect

// Integração por path local, ZERO dependências externas, build offline.
//
// O Event Store (AOS-002, ADR-007) é a FONTE DE VERDADE do catálogo: o REG grava
// eventos append-only e RECONSTRÓI o estado por replay do log — nunca um estado
// autoritativo em RAM nem um single-writer SQLite.
//
// O Agent Runtime (AOS-013) fornece a PORTA Tracer/Span zero-dep para os spans
// OTel gen_ai.* emitidos pelas operações de consulta do REG. Como os replace de um
// módulo dependente NÃO são transitivos, o reference-monitor (dependência
// transitiva do agent-runtime) é re-declarado aqui a partir da raiz de packages/.
replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore
