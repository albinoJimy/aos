module github.com/aos-ref/qa/dr-e2e

go 1.24

// AOS-118 — Teste de fogo de DR/replay end-to-end (EPIC-11, o que FECHA o epic).
//
// Módulo-FOLHA de teste: NINGUÉM o importa. Por isso pode importar de QUALQUER
// camada (substrate, kernel, testkit) sem violar o layering de produção — é a
// imagem do testkit. NÃO reimplementa nenhum primitivo: COMPÕE o cluster de
// réplicas do Event Store (eventstore.Store.Kill/electLeader — failover por
// promoção de réplica, não restore-from-backup), o harness de replay/idempotência
// (kernel/agent-runtime/harness), o worker durável resume-from-step
// (kernel/agent-runtime/worker), o fencing/lease (kernel/agent-runtime/durable) e o
// ambiente efémero (testkit/env) numa única história de desastre.
//
// Os replace NÃO são transitivos: re-declaram-se aqui TODAS as arestas do fecho
// (eventstore, agent-runtime, reference-monitor, otel-genai, testkit) para o build
// fechar OFFLINE, sem dependências externas. Molde: platform/dr/go.mod.
require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
	github.com/aos-ref/testkit v0.0.0
)

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai

replace github.com/aos-ref/testkit => ../../testkit
