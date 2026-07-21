module github.com/aos-ref/control-plane/governance/autonomy-surface

go 1.24

// AOS-125 — UX DA AUTONOMIA PROGRESSIVA: a SUPERFÍCIE que torna o nível L0–L5
// corrente, os critérios/progresso rumo à próxima promoção e as transições
// (promoção/demoção com o seu motivo) LEGÍVEIS e ACCIONÁVEIS por (agente, domínio)
// — adaptada à maturidade do utilizador (EPIC-12, UX/DX). É uma camada FINA de
// APRESENTAÇÃO (padrão porta+adaptador, molde control-surface/progress-surface):
// LÊ o registo de níveis e DELEGA a decisão ao Controller — NUNCA decide o nível.
// COMPÕE só o LEVE:
//   - autonomy (AOS-089/090): os tipos de domínio (Level/LevelChange/Reliability) e
//     as portas de consulta (LevelRegistry LÊ o nível/histórico; ReliabilitySource
//     o sinal de progresso; a DECISÃO fica atrás da porta LevelReviewer que o wiring
//     adapta ao Controller.Evaluate). NÃO reimplementa taxonomia/promoção/demoção.
//   - agent-runtime (AOS-076): a porta Tracer e o vocabulário aos.autonomy.*.
// Build offline; integração por path local (replace, NÃO herdados). ZERO deps externas.
require (
	github.com/aos-ref/control-plane/governance/autonomy v0.0.0
	github.com/aos-ref/kernel/agent-runtime v0.0.0
)

replace github.com/aos-ref/control-plane/governance/autonomy => ../autonomy

replace github.com/aos-ref/kernel/agent-runtime => ../../../kernel/agent-runtime

// Transitivos: autonomy arrasta reference-monitor + audit + otel-genai + eventstore;
// agent-runtime arrasta reference-monitor + eventstore + otel-genai. Os replace NÃO
// são herdados — este módulo resolve-os localmente para fechar o build offline.
require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/platform/audit v0.0.0 // indirect
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
)

replace github.com/aos-ref/kernel/reference-monitor => ../../../kernel/reference-monitor

replace github.com/aos-ref/platform/audit => ../../../platform/audit

replace github.com/aos-ref/substrate/eventstore => ../../../substrate/eventstore

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai
