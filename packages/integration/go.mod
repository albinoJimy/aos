module github.com/aos-ref/integration

go 1.24

require (
	github.com/aos-ref/control-plane/pdp v0.0.0
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/platform/identity v0.0.0
	github.com/aos-ref/platform/memory v0.0.0
	github.com/aos-ref/platform/model-gateway v0.0.0
	github.com/aos-ref/platform/registry v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
	github.com/aos-ref/substrate/sandbox v0.0.0
)

require (
	github.com/aos-ref/control-plane/governance/autonomy v0.0.0 // indirect
	github.com/aos-ref/control-plane/governance/sovereignty v0.0.0 // indirect
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
	github.com/cedar-policy/cedar-go v1.8.0 // indirect
	golang.org/x/exp v0.0.0-20220921023135-46d9e7742f1e // indirect
)

replace github.com/aos-ref/control-plane/budget => ../control-plane/budget

replace github.com/aos-ref/control-plane/governance/approval-card => ../control-plane/governance/approval-card

replace github.com/aos-ref/control-plane/governance/authoring-surface => ../control-plane/governance/authoring-surface

replace github.com/aos-ref/control-plane/governance/autonomy => ../control-plane/governance/autonomy

replace github.com/aos-ref/control-plane/governance/autonomy-surface => ../control-plane/governance/autonomy-surface

replace github.com/aos-ref/control-plane/governance/compliance => ../control-plane/governance/compliance

replace github.com/aos-ref/control-plane/governance/confidence-calibration => ../control-plane/governance/confidence-calibration

replace github.com/aos-ref/control-plane/governance/control-surface => ../control-plane/governance/control-surface

replace github.com/aos-ref/control-plane/governance/dsar => ../control-plane/governance/dsar

replace github.com/aos-ref/control-plane/governance/hitl => ../control-plane/governance/hitl

replace github.com/aos-ref/control-plane/governance/plan-approval => ../control-plane/governance/plan-approval

replace github.com/aos-ref/control-plane/governance/progress-surface => ../control-plane/governance/progress-surface

replace github.com/aos-ref/control-plane/governance/sovereignty => ../control-plane/governance/sovereignty

replace github.com/aos-ref/control-plane/governance/surface-adapter => ../control-plane/governance/surface-adapter

replace github.com/aos-ref/control-plane/governance/trajectory-surface => ../control-plane/governance/trajectory-surface

replace github.com/aos-ref/control-plane/orchestrator => ../control-plane/orchestrator

replace github.com/aos-ref/control-plane/pdp => ../control-plane/pdp

replace github.com/aos-ref/control-plane/scheduler => ../control-plane/scheduler

replace github.com/aos-ref/kernel/agent-runtime => ../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../kernel/reference-monitor

replace github.com/aos-ref/platform/audit => ../platform/audit

replace github.com/aos-ref/platform/backup => ../platform/backup

replace github.com/aos-ref/platform/broker => ../platform/broker

replace github.com/aos-ref/platform/dr => ../platform/dr

replace github.com/aos-ref/platform/eval => ../platform/eval

replace github.com/aos-ref/platform/hipercare => ../platform/hipercare

replace github.com/aos-ref/platform/identity => ../platform/identity

replace github.com/aos-ref/platform/memory => ../platform/memory

replace github.com/aos-ref/platform/messaging => ../platform/messaging

replace github.com/aos-ref/platform/model-gateway => ../platform/model-gateway

replace github.com/aos-ref/platform/registry => ../platform/registry

replace github.com/aos-ref/platform/runbooks => ../platform/runbooks

replace github.com/aos-ref/qa/dr-e2e => ../qa/dr-e2e

replace github.com/aos-ref/qa/ux-dx => ../qa/ux-dx

replace github.com/aos-ref/security-tests => ../security-tests

replace github.com/aos-ref/substrate/bus => ../substrate/bus

replace github.com/aos-ref/substrate/eventstore => ../substrate/eventstore

replace github.com/aos-ref/substrate/otel-genai => ../substrate/otel-genai

replace github.com/aos-ref/substrate/redaction => ../substrate/redaction

replace github.com/aos-ref/substrate/sandbox => ../substrate/sandbox

replace github.com/aos-ref/testkit => ../testkit
