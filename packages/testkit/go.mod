module github.com/aos-ref/testkit

go 1.24

require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

require github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect

// Módulo de testes de referência (AOS-109, EPIC-11). É OPT-IN: nenhum dos 23
// módulos de produção depende do testkit — só quem quiser reutilizar as fixtures/
// mocks adiciona o replace. ZERO dependências externas, build offline.
//
// LAYERING deliberado (go.mod o mais LEVE possível): o testkit importa apenas os
// contratos LEVES/FOLHA reais e mantém a cadeia zero-dep:
//   - substrate/eventstore  — o Event Store in-memory de referência (FOLHA, zero-dep);
//   - kernel/reference-monitor — {Hook, EventSink, Call, Decision, ...} (arrasta só
//     eventstore + otel-genai, ambos folha zero-dep);
//   - kernel/agent-runtime  — {durable.IdempotencyKey, durable.StepSequencer} para as
//     fixtures de run_id/step_id (mesma cadeia zero-dep do RM).
//
// Os replace NÃO são transitivos: re-declaram-se aqui a partir da raiz de packages/.
// Para PDP/GW/BRK o testkit NÃO importa os contratos reais (PDP arrasta cedar-go
// EXTERNO; GW/BRK vivem em model-gateway/internal e arrastam 12 replace): define
// INTERFACES ALINHADAS ao _BRIEF §2 + fakes deterministas (ver pdp.go/gateway.go/
// broker.go). NÃO alterar os pacotes referenciados (são código de produção Done).
replace github.com/aos-ref/kernel/reference-monitor => ../kernel/reference-monitor

replace github.com/aos-ref/kernel/agent-runtime => ../kernel/agent-runtime

replace github.com/aos-ref/substrate/eventstore => ../substrate/eventstore

replace github.com/aos-ref/substrate/otel-genai => ../substrate/otel-genai
