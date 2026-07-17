module github.com/aos-ref/kernel/agent-runtime

go 1.24

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

// Integração por path local: zero dependências externas, build offline.
// NÃO alterar os pacotes referenciados (AOS-003 / AOS-002 são Done).
// O primitivo de taint (AOS-069) é o subpacote reference-monitor/taint — folha
// zero-dep partilhada RT↔RM — resolvido por este mesmo replace (sem novo módulo).
replace github.com/aos-ref/kernel/reference-monitor => ../reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

// A camada de instrumentação OTel GenAI (AOS-076) é um módulo folha do substrato
// (zero-dep). O RT deriva a sua superfície pública de tracing daqui (aliases em
// tracing.go) e partilha o Tracer com o RM (mesma árvore de spans).
replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai
