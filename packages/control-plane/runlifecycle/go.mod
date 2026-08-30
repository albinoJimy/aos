module github.com/aos-ref/control-plane/runlifecycle

go 1.24

require (
	github.com/aos-ref/control-plane/budget v0.0.0
	github.com/aos-ref/control-plane/orchestrator v0.0.0
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/platform/identity v0.0.0 // indirect
	github.com/aos-ref/substrate/bus v0.0.0 // indirect
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
)

// AOS-281 / ADR-023 — O TERCEIRO SÍTIO.
//
// Este módulo é a composição ORQ/SCH↔posse que nem o nó nem o despachante podem
// conter, e a razão de cada exclusão está registada no ADR-023 §2.6:
//
//   - `packages/cmd/aos` não pode, por `TestBoundary_NodeDoesNotImport…` (ADR-018 §5);
//   - `plandispatch` não pode, por `TestBoundary_ProductionImportsAreAllowlisted`;
//   - `packages/integration` não pode, por estar DENTRO do grafo de build do nó — o
//     guarda transitivo do ADR-018 dispararia.
//
// A DIRECÇÃO das dependências é o que mantém as duas fronteiras verdes e INALTERADAS:
// este módulo importa o orquestrador e o despachante; nenhum deles o importa, e o nó
// não o requer (não entra no seu `go list -deps`).
replace github.com/aos-ref/control-plane/orchestrator => ../orchestrator

// Lease/fencing durável (AOS-018, `durable`) e máquina de estados do run (AOS-017,
// `state`) — o mecanismo de arbitragem NÃO é reinventado aqui, é composto.
replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

// Os replace NÃO são transitivos: os módulos que o orquestrador arrasta são
// re-declarados a partir da raiz de packages/. Build offline, zero deps externas.
replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/substrate/bus => ../../substrate/bus

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/control-plane/budget => ../budget

replace github.com/aos-ref/platform/identity => ../../platform/identity
