module github.com/aos-ref/platform/registry

go 1.24

require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/platform/memory v0.0.0
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

// AOS-046: a integração MCP marca os schemas/descrições devolvidos por servidores
// MCP como UNTRUSTED reutilizando a maquinaria de taint de AOS-042
// (platform/memory/provenance) — a barreira estrutural control/data-plane que
// impede conteúdo untrusted de comandar o planeador (ADR-005). NÃO se reimplementa
// a barreira; depende-se dela por path local. O audit é dependência TRANSITIVA do
// pacote provenance (promoção auditável untrusted→trusted); redeclarado aqui porque
// os replace não são transitivos. NÃO alterar os pacotes referenciados (AOS-042/011).
replace github.com/aos-ref/platform/memory => ../memory

replace github.com/aos-ref/platform/audit => ../audit
