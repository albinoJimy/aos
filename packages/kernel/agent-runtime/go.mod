module github.com/aos-ref/kernel/agent-runtime

go 1.24

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

// Integração por path local: zero dependências externas, build offline.
// NÃO alterar os pacotes referenciados (AOS-003 / AOS-002 são Done).
replace github.com/aos-ref/kernel/reference-monitor => ../reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore
