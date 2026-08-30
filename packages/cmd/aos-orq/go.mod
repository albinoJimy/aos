module github.com/aos-ref/cmd/aos-orq

go 1.24

require (
	github.com/aos-ref/control-plane/orchestrator v0.0.0
	github.com/aos-ref/control-plane/runlifecycle v0.0.0
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

require (
	github.com/aos-ref/control-plane/budget v0.0.0 // indirect
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/platform/identity v0.0.0 // indirect
	github.com/aos-ref/substrate/bus v0.0.0 // indirect
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
)

replace github.com/aos-ref/control-plane/runlifecycle => ../../control-plane/runlifecycle

replace github.com/aos-ref/control-plane/orchestrator => ../../control-plane/orchestrator

replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/substrate/bus => ../../substrate/bus

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/control-plane/budget => ../../control-plane/budget

replace github.com/aos-ref/platform/identity => ../../platform/identity
