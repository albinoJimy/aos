// Package eval é o EVAL HARNESS + golden-sets curados do AOS (AOS-114, EPIC-11,
// ADR-012): o ADMISSION CONTROL comportamental — o "gate 9" — do pipeline de
// auto-modificação staging → EVAL-GATE → canary → ratificação → prod.
//
// # O que este módulo entrega
//
// O esqueleto de eval JÁ existe como PORTAS + impls de referência no módulo FOLHA
// otel-genai (AOS-084), explicitamente DIFERIDO para EPIC-11. Este pacote COMPÕE
// essas portas num harness CONCRETO — não reimplementa nenhum tipo de veredicto:
//
//   - [GoldenSet]/[GoldenCase] — o FORMATO de golden-set curado, VERSIONADO (SemVer)
//     e revisável (JSON round-trip byte-estável + [GoldenSet.Validate] fail-closed),
//     distinguindo o golden curado (regressões NOVAS) do failure-derived (regressões
//     CONHECIDAS) via [otelgenai.EvalDataset]. Um set inicial não-trivial por classe
//     de artefacto comportamental (skill, procedural_memory) vive embebido no repo
//     (ver [EmbeddedSuites]).
//   - [Candidate]/[Behavior] — a superfície do artefacto comportamental sob teste: uma
//     função DETERMINISTA input → (output final + acções/tool-calls). Sem I/O, sem
//     relógio, sem aleatoriedade — o veredicto é reprodutível.
//   - [Runner] — o runner CONCRETO que satisfaz a porta [otelgenai.EvalRunner]: marca o
//     comportamento produzido (transportado no [otelgenai.EvalTarget] como spans)
//     contra as expectativas do golden-set, agrega success-rate/unsafe-action-rate e
//     produz um [otelgenai.EvaluationResult] fail-closed (qualquer acção unsafe reprova).
//   - [Harness] — conduz o candidato sobre os casos, codifica o comportamento em spans,
//     corre AMBOS os datasets, emite o gen_ai.evaluation.result LIGADO ao trace via
//     [otelgenai.RecordEvaluation], e expõe o veredicto de admissão via
//     [otelgenai.FailClosedGate].
//
// # Fail-closed
//
// O default é REJEITAR. Um artefacto só fica elegível a canary se o golden-set curado
// E o failure-derived ficarem acima do limiar de success-rate E com ZERO acções
// unsafe. Qualquer falha — output errado, acção proibida, dataset vazio, versão
// mal-formada — reprova sem ir a produção.
//
// # Fronteira de composição
//
// Este pacote (o core) importa APENAS o módulo folha otel-genai. Os adaptadores às
// portas Evaluate(...) dos consumidores (promotion/procedural) vivem no subpacote
// gateadapter, para manter o core enxuto. NÃO se tocam hitl/ratification.go,
// procedural/skill_memory.go nem promotion/pipeline.go — o canary e a ratificação
// (EPIC-09) já existem e só se compõem.
package eval
