module github.com/aos-ref/control-plane/budget

go 1.24

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

// Reference Monitor (AOS-003) integrado por path local para o adaptador
// BudgetCheck (o antigo BudgetStub passa a ser o orçamento real). Traz o Event
// Store (AOS-002) transitivamente, mas os replace NÃO são transitivos: o
// eventstore é re-declarado aqui a partir da raiz de packages/. Build offline,
// ZERO dependências externas (ADR-008 — supply-chain mínima).
replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai
