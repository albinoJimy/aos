module github.com/aos-ref/control-plane/governance/authoring-surface

go 1.24

// AOS-126 — LOOP DE AUTORIA DE SKILLS: a SUPERFÍCIE que torna o loop de autoria de
// uma skill candidata LEGÍVEL e ACCIONÁVEL — DRY-RUN (executar em modo simulado, ver
// o efeito SEM o cometer), ATRIBUIÇÃO visível (autor-agente/humano, versão SemVer,
// proveniência) e ENCAMINHAMENTO ao gate de ratificação de AOS-096, mostrando o
// resultado do eval/canary ANTES da decisão. É uma camada FINA de APRESENTAÇÃO
// (padrão porta+adaptador, molde autonomy-surface): COMPÕE portas e NÃO reimplementa
// o sandbox/registry/eval-gate/ratificação; NÃO comete efeitos (dry-run isolado,
// egress default-deny, efeitos capturados untrusted, Committed=false) nem RATIFICA
// (só apresenta+submete; não há caminho de Ratify na superfície).
//
// COMPÕE só o LEVE:
//   - agent-runtime (AOS-076): a porta Tracer e o vocabulário aos.* (AttrRunID) —
//     os spans do loop (dry_run/attribution_view/submit) ligados à trajectória.
//   - otel-genai (AOS-084): o EvaluationResult/EvalVerdict (folha zero-dep) para a
//     porta de eval — a superfície LÊ o veredicto/score e apresenta-o.
// Os adaptadores CONCRETOS (sandbox/registry/hitl) ficam no WIRING atrás das portas.
// Build offline; integração por path local (replace, NÃO herdados). ZERO deps externas.
require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

replace github.com/aos-ref/kernel/agent-runtime => ../../../kernel/agent-runtime

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai

// Transitivos: agent-runtime arrasta reference-monitor + eventstore + otel-genai. Os
// replace NÃO são herdados — este módulo resolve-os localmente para fechar o build
// offline (molde autonomy-surface/go.mod).
require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
)

replace github.com/aos-ref/kernel/reference-monitor => ../../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../../substrate/eventstore
