module github.com/aos-ref/kernel/reference-monitor

go 1.24

require github.com/aos-ref/substrate/eventstore v0.0.0

// O Event Store (AOS-002) é integrado por path local: zero dependências
// externas, build offline. NÃO alterar o pacote referenciado.
replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore
