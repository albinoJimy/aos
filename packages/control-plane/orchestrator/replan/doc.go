// Package replan implementa o RE-PLANEAMENTO DE SUBGRAFO com orçamento residual
// (AOS-239, EPIC-18/tecnica/18 §4.2). É a GOVERNANÇA do re-plano — não o
// planeamento em si (a decomposição LLM que produz o novo subgrafo é untrusted e
// vive a montante, ADR-005). Este pacote impõe, fail-closed, os quatro
// invariantes que tornam um re-plano seguro:
//
//  1. DÉBITO NA ÁRVORE. O custo residual do re-plano é debitado do orçamento da
//     ÁRVORE partilhada (AOS-008) — nunca de um contador novo. O débito atravessa
//     a porta [ResidualBudget]: reserva CAS antes de aplicar, commit no applied,
//     release em qualquer recusa/falha ATÉ ao commit (sem leak). Após o commit o
//     débito é definitivo (o re-plano foi aprovado); uma falha posterior compensa o
//     DAG mas não reverte o débito. Se a árvore não tiver headroom residual, o
//     re-plano é RECUSADO ([ErrNoResidualBudget]) — o orçamento residual é
//     respeitado por construção, não por convenção.
//
//  2. MESMO GATE, AUTONOMIA NÃO-CRESCENTE. O re-plano atravessa o MESMO gate
//     (AOS-121, porta [Gate]) que o plano original, ao nível L0–L5 do plano
//     ORIGINAL. A autonomia pedida para o re-plano NUNCA excede a do original
//     ([ErrAutonomyExceedsOriginal]): um re-plano só pode reduzir autonomia,
//     jamais escalá-la para contornar supervisão. O nível original é FIXADO na
//     árvore na primeira invocação: um re-plano ANINHADO não pode re-declarar um
//     original superior ao fixado — a escalada por aninhamento é recusada.
//
//  3. HISTÓRICO INTOCÁVEL. Nós CONCLUÍDOS são imutáveis. O re-plano só opera sobre
//     o FUTURO — o subgrafo pendente. Qualquer tentativa de incluir um nó
//     concluído (no subgrafo a substituir OU no novo subgrafo a despachar) é
//     recusada fail-closed ([ErrCompletedNodeImmutable]). É este guard que impede
//     o re-despacho do histórico.
//
//  4. TECTO POR ÁRVORE + REVISÃO HUMANA FORÇADA. Há um tecto de re-planos por
//     ÁRVORE. Re-planos ANINHADOS contam para o MESMO tecto (o contador é
//     por-tree_id, não por-invocação) — é isto que impede um loop de re-plano
//     permanente (DoD). Quando o tecto é esgotado, OU quando o custo acumulado dos
//     re-planos excede uma fracção do orçamento da árvore, a revisão humana é
//     FORÇADA no gate (o nível de autonomia deixa de bastar).
//
// FRONTEIRA ADR-018. O re-plano coordena o ciclo-de-vida via as portas [Gate],
// [ResidualBudget], [SubgraphScheduler] e [ReplanRecorder]; NÃO despacha nós nem é
// autoridade concorrente do SCH. Emite `plan.replan_requested`/`applied`
// (constantes de plannerevents, reutilizadas — nunca literais) e o SCH suspende o
// subgrafo no requested e retoma no applied (AOS-238, via [SubgraphScheduler]).
package replan
