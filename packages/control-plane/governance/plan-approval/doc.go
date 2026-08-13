// Package planapproval é o GATE DE APROVAÇÃO-DE-PLANO ANTES DO SPAWN (AOS-121,
// EPIC-12): a superfície que apresenta o GRAFO DE TAREFAS proposto pelo orquestrador
// ao humano ANTES de o orquestrador queimar tokens no spawn, permitindo APROVAR,
// EDITAR (podar/reordenar/anotar) ou REJEITAR o plano, e só libertando o spawn após a
// aprovação.
//
// É DISTINTO e ANTERIOR aos gates de ACÇÃO (AOS-120): opera sobre o GRAFO MULTI-NÓ
// (o "plano" reproduzível), não sobre uma tool call individual. Aprovar acção-a-acção
// depois de o plano já correr é tarde e caro; aprovar o plano à cabeça é barato e
// alinha expectativas (estilo AgentScope).
//
// É uma camada de APRESENTAÇÃO/GATE que COMPÕE — não reimplementa:
//   - o EFEITO CONCRETO por nó pertence a AOS-120: cada [PlanNode] vira uma
//     [approvalcard.ApprovalCard] via [approvalcard.BuildCard] (já redigida). O
//     [PlanCard] embrulha esses cards + a topologia (ordem, arestas, contagem, classe
//     agregada, custo agregado).
//   - o NÍVEL de autonomia pertence a AOS-089: o gate CONSOME o par
//     [autonomy.Oracle.LevelFor] e [autonomy.Oversight].Runs para auto-aprovar a níveis
//     altos — NÃO decide nem promove o nível.
//   - a assinatura/autoridade/anti-replay/4-eyes/audit pertencem a AOS-095: a decisão
//     binária é DEVOLVIDA ao [risk.ConfirmationChannel] (o [hitl.Channel]), que assina e
//     sela. O gate não assina.
//
// AS EXTENSÕES DE ADR-022 SÃO VISÍVEIS NO CARTÃO (invariante §2.4(5), DEF-274): o
// [PlanNode] transporta o PAPEL do nó ([RoleVerifier] distingue quem JULGA de quem
// PRODUZ), as ARESTAS CONDICIONAIS que governam a sua entrada e os contratos de dados
// (`outputs`/`consumes`) — todos em FORMA CANÓNICA (símbolos de charset fechado,
// inteiros, referências a nós do próprio plano), e o [PlanCard] projecta-os em
// [PlanCard.NodeExtensions] + [PlanCard.VerificationView] («quem verifica quem, sob que
// condição»). A regra de ouro do cartão vale aqui com força de estrutura, não de
// convenção, e nas DUAS portas: uma extensão fora da forma canónica RECUSA o plano
// ([ErrNonCanonicalExtension]) tanto na CONSTRUÇÃO ([Plan.Validate]/[BuildPlanCard])
// como no WIRE ([PlanCard.UnmarshalJSON], que re-parseia a mesma gramática) — em vez de
// deixar conteúdo do run entrar na superfície de aprovação por qualquer uma delas. O
// cartão expõe também as arestas induzidas por condição em [PlanCard.ConditionalEdges],
// para que o grafo que ORDENA e o grafo que se MOSTRA sejam o mesmo. Ver extensions.go.
//
// O GRAFO e o SPAWN são PORTAS locais: [Plan]/[PlanNode] é a representação do grafo
// sobre a qual o gate opera (o [orchestrator.DAG] mapeia para ela no wiring); [Spawner]
// é a fronteira PRE-SPAWN (o [scheduler.SubtreeSpawner] adapta-se a ela no wiring). O
// [SpawnGuard] prova ESTRUTURALMENTE que nenhum sub-agente é lançado antes da aprovação
// (defesa-em-profundidade fail-closed). Tudo determinista, offline, sem segredos.
package planapproval
