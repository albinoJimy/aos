module github.com/aos-ref/cmd/aos-orq

go 1.24

require (
	github.com/aos-ref/control-plane/budget v0.0.0
	github.com/aos-ref/control-plane/orchestrator v0.0.0
	github.com/aos-ref/control-plane/runlifecycle v0.0.0
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/platform/identity v0.0.0
	github.com/aos-ref/platform/model-gateway v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

require (
	github.com/aos-ref/control-plane/scheduler v0.0.0 // indirect
	github.com/aos-ref/platform/eval v0.0.0 // indirect
	github.com/aos-ref/platform/memory v0.0.0 // indirect
	github.com/aos-ref/platform/registry v0.0.0 // indirect
	github.com/aos-ref/substrate/bus v0.0.0 // indirect
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
)

replace github.com/aos-ref/platform/model-gateway => ../../platform/model-gateway

replace github.com/aos-ref/control-plane/scheduler => ../../control-plane/scheduler

replace github.com/aos-ref/platform/audit => ../../platform/audit

replace github.com/aos-ref/platform/registry => ../../platform/registry

replace github.com/aos-ref/platform/memory => ../../platform/memory

replace github.com/aos-ref/platform/eval => ../../platform/eval

replace github.com/aos-ref/control-plane/runlifecycle => ../../control-plane/runlifecycle

replace github.com/aos-ref/control-plane/orchestrator => ../../control-plane/orchestrator

replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/substrate/bus => ../../substrate/bus

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/control-plane/budget => ../../control-plane/budget

replace github.com/aos-ref/platform/identity => ../../platform/identity
