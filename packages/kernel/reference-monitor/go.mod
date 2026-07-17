module github.com/aos-ref/kernel/reference-monitor

go 1.24

require (
	github.com/aos-ref/substrate/eventstore v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

// O Event Store (AOS-002) é integrado por path local: zero dependências
// externas, build offline. NÃO alterar o pacote referenciado.
replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

// A camada de instrumentação OTel GenAI (AOS-076) é um módulo folha do substrato
// (zero-dep), integrado por path local para manter o build offline. O RM abre o
// span execute_tool aqui — o ponto único de mediação (ADR-002).
replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai
