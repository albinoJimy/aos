module github.com/aos-ref/control-plane/governance/control-surface

go 1.24

// AOS-119 — Contrato unificado da superfície de controlo HITL out-of-band (EPIC-12,
// ADR-013). Este módulo é uma CAMADA FINA de APRESENTAÇÃO/PROTOCOLO: define um
// contrato de mensagens VERSIONADO (SemVer) e traduz acções de utilizador de QUALQUER
// canal (desktop/chatbot/API) para os sinais que AOS-023 JÁ consome — NÃO reimplementa
// a máquina de estados, o estado `paused` nem o graceful pause (isso é o pacote
// kernel/agent-runtime/control + kernel/agent-runtime/state, EPIC-02, Done). COMPÕE:
//   - control (AOS-023): SteerChannel (Pause/Steer/Resume), MachineGate, StateGate.
//   - state (AOS-017): a máquina durável — só LIDA (Current) para reflexão, nunca
//     accionada directamente (vai sempre via SteerChannel para preservar
//     durabilidade + não-repúdio + aceitação-garantida).
//   - eventstore (AOS-002): Subscribe para o read-model de reflexão do estado.
//   - otel-genai / agent-runtime (AOS-076): a porta Tracer e o vocabulário aos.control.*.
//   - reference-monitor/taint (AOS-069): a prova de que a correcção out-of-band é
//     control-plane (trusted).
// Build offline; integração por path local (replace) como no resto do monorepo
// (molde: control-plane/governance/hitl/go.mod). ZERO dependências externas.
require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

replace github.com/aos-ref/kernel/agent-runtime => ../../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../../substrate/eventstore

// Transitivo: agent-runtime deriva a superfície de tracing de otel-genai (módulo
// folha zero-dep). Os replace NÃO são herdados — este módulo resolve-o localmente
// para fechar o build offline.
require github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai
