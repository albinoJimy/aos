module github.com/aos-ref/qa/banco-ensaio

go 1.24

// AOS-512 — Banco de ensaio mínimo da fronteira runtime↔modelo (fase A2).
//
// Módulo-FOLHA de medição: NINGUÉM o importa, e não faz parte do nó `aos` nem da imagem de
// produção. Por isso pode compor o kernel (loop + Reference Monitor) e o Model Gateway de
// produção sem violar o layering — é a imagem do qa/dr-e2e e do qa/ux-dx. NÃO reimplementa a
// montagem do pedido: o pedido que sai é o que o loop e a projecção do gateway produzem.
//
// Os replace NÃO são transitivos: re-declaram-se aqui TODAS as arestas do fecho para o build
// fechar OFFLINE, sem dependências externas. Molde: cmd/aos-orq/go.mod.
require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/platform/identity v0.0.0
	github.com/aos-ref/platform/model-gateway v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

require github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect

replace github.com/aos-ref/platform/model-gateway => ../../platform/model-gateway

replace github.com/aos-ref/control-plane/scheduler => ../../control-plane/scheduler

replace github.com/aos-ref/platform/audit => ../../platform/audit

replace github.com/aos-ref/platform/registry => ../../platform/registry

replace github.com/aos-ref/platform/memory => ../../platform/memory

replace github.com/aos-ref/control-plane/orchestrator => ../../control-plane/orchestrator

replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/substrate/bus => ../../substrate/bus

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/control-plane/budget => ../../control-plane/budget

replace github.com/aos-ref/platform/identity => ../../platform/identity
