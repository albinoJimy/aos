module github.com/aos-ref/control-plane/governance/trajectory-surface

go 1.24

// AOS-127 — VISUALIZAÇÃO/DRILL-DOWN DA TRAJECTÓRIA DO SUB-AGENTE (EPIC-12, UX/DX).
// A SUPERFÍCIE que torna NAVEGÁVEL a árvore de spans completa de um run e dos seus
// sub-agentes (hierarquia invoke_agent -> execute_tool -> chat) lida de AOS-077, com
// drill-down por span (atributos, tokens, custo, resultado, taint) e ligação a
// eval/replay quando disponíveis. É LEITURA PURA (padrão porta+adaptador, molde
// autonomy-surface/authoring-surface): CONSOME os spans OTel já emitidos e NÃO
// captura, muta nem re-emite spans; NÃO reimplementa o backend de observabilidade, o
// custo, o eval nem o replay. COMPÕE só o LEVE:
//   - otel-genai (AOS-076/077/078/084 — módulo FOLHA zero-dep): SpanData, a topologia
//     por ParentSpanID->SpanID, RollupByTrace/AggregateByTrace (custo por sub-árvore
//     sem dupla-contagem), EvaluationResult (ligação a eval) e a porta Tracer.
//   - redaction (AOS-091 — módulo FOLHA zero-dep): Engine.RedactText/ScanText para
//     apresentar cada valor de atributo REDIGIDO (sem PII em claro) e marcar o
//     conteúdo untrusted como DADO (separação control/data-plane).
// Build offline; integração por path local (replace, NÃO herdados). ZERO deps externas.
require (
	github.com/aos-ref/substrate/otel-genai v0.0.0
	github.com/aos-ref/substrate/redaction v0.0.0
)

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai

replace github.com/aos-ref/substrate/redaction => ../../../substrate/redaction
