module github.com/aos-ref/platform/model-gateway

go 1.24

require github.com/aos-ref/kernel/agent-runtime v0.0.0

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
)

// Integração por path local: ZERO dependências externas, build offline. O GW
// consome a porta agentruntime.Tracer (zero-dep) e alinha o contrato ModelClient
// (AOS-013). Os replace de um módulo dependente NÃO são transitivos, pelo que o
// reference-monitor e o eventstore (dependências transitivas do agent-runtime)
// são re-declarados aqui a partir da raiz de packages/.
replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore
