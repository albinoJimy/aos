// Package trajectorysurface é a VISUALIZAÇÃO/DRILL-DOWN DA TRAJECTÓRIA DO SUB-AGENTE
// (AOS-127, EPIC-12, UX/DX): a SUPERFÍCIE que torna NAVEGÁVEL a árvore de spans
// completa de um run e dos seus sub-agentes — a hierarquia invoke_agent ->
// execute_tool -> chat — e INSPECCIONÁVEL cada span (atributos, tokens, custo em USD,
// resultado, taint), com ligação opcional a eval/replay.
//
// # CONSOME — não captura, muta nem re-emite (ADR-010, AC3)
//
// Esta camada é APRESENTAÇÃO/DX pura (padrão porta+adaptador, molde autonomy-surface/
// authoring-surface). CONSOME os spans OTel de AOS-077 (EPIC-08) que já estão SEMPRE
// no backend — o Princípio 4 (contexto != registo) garante que, embora ao pai só vá um
// resumo higienizado, a ÁRVORE DE SPANS COMPLETA do sub-agente é persistida — e NÃO:
//   - captura, muta nem re-emite spans: [BuildTree]/[Inspect] LÊEM os [otelgenai.SpanData]
//     de entrada, que ficam byte-a-byte intactos. Os únicos spans emitidos são os da
//     PRÓPRIA interacção (ver span.go), nunca cópias dos spans da trajectória.
//   - reimplementa o backend de observabilidade, o custo, o eval ou o replay: COMPÕE
//     [otelgenai.RollupByTrace]/[otelgenai.AggregateByTrace] (custo por sub-árvore sem
//     dupla-contagem) e [otelgenai.EvaluationResult] (ligação a eval); o replay/eval são
//     LOCALIZADOS por porta (navegação), nunca recalculados.
//
// # Taint e redação na apresentação (AC4)
//
// Cada valor de atributo apresentado é REDIGIDO por [redaction.Engine] ANTES de entrar
// numa [AttrView], com um gate fail-closed que confirma [redaction.Engine.ScanText]==[]
// (sem PII em claro). Um resultado untrusted (aos.tool.result_taint=untrusted) marca o
// conteúdo de data-plane como DADO ([AttrView.Untrusted]) — nunca apresentado como
// instrução (separação control/data-plane, ADR-005).
//
// # Mapa dos critérios de aceitação
//
//   - AC1: [BuildTree] / [TrajectorySurface.TreeView] — a árvore de spans completa,
//     hierarquia invoke_agent -> execute_tool -> chat por ParentSpanID->SpanID.
//   - AC2: [Inspect] / [TrajectorySurface.DrillDown] — tokens/custo/resultado/taint de
//     um span e o custo da sub-árvore de um invoke_agent (RollupByTrace).
//   - AC3: leitura pura — os SpanData de entrada ficam intactos; nenhum span re-emitido.
//   - AC4: [AttrView] redigida + [AttrView.Untrusted] (taint marcado como DADO).
//   - AC5: [TrajectorySurface.LinkEval]/[TrajectorySurface.LinkReplay] — navegação para
//     a eval (gen_ai.evaluation.result) e o replay, quando disponíveis, sem recalcular.
//
// Ver specs/EPIC-12 (## AOS-127), specs/EPIC-08 (AOS-077), tecnica/15 §10 e ADR-010.
package trajectorysurface
