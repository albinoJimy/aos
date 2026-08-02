// Package capabilitygap entrega a GOVERNAÇÃO de um nó `capability_gap` do plano
// (AOS-240, EPIC-19) — NÃO um executor de skills.
//
// Quando o planeamento produz um nó cuja skill NÃO existe no snapshot de
// capabilities pinado, esse nó abre um GAP DE CAPACIDADE. O nó BLOQUEIA em estado
// `waiting_on_capability` e NÃO despacha até ser RATIFICADO: uma skill candidata
// tem de ser sintetizada por um agente-autor GOVERNADO e sobreviver a um pipeline
// fail-closed de admissão antes de qualquer efeito. Este pacote é a máquina de
// estados dessa admissão — não corre a skill.
//
// # O que este pacote É
//
//   - A máquina de estados por-nó (waiting → authoring → dry_run → eval_gate →
//     canary → resolved, com os terminais não-despacháveis blocked/rejected),
//     SEM bypass: cada método de avanço exige o estado predecessor exacto
//     ([ErrStageOutOfOrder]); não há via que salte uma etapa nem que ponha
//     `resolved` directamente — a transição canary → resolved SÓ ocorre via
//     [GapNode.Ratify] com ratificação assinada.
//   - O agente-autor GOVERNADO ([Coordinator.authorSkill]): NHI própria emitida
//     via identity (on-behalf-of, autoridade = allowlist RESTRITA), orçamento
//     reservado/consolidado via budget, e a spec do gap tratada como INPUT
//     UNTRUSTED. O artefacto gerado nasce com TAINT ([OriginSelfAuthored]) que o
//     acompanha por todo o pipeline — o eval-gate vê-o taintado e a ratificação
//     assinada é o ÚNICO ponto que autoriza produção (o taint nunca é "limpo" pela
//     máquina).
//   - O gate de estado que faz o nó recusar despacho até `resolved`
//     ([GapNode.CanDispatch]).
//   - O teto de gaps por plano ([Config.MaxGapsPerPlan]) — bloqueia um plano
//     adversarial que tente abrir gaps sem fim ([ErrGapCeilingExceeded]).
//   - A emissão dos factos `plan.capability_gap_opened` / `plan.capability_gap_resolved`
//     (constantes de plannerevents — REUTILIZADAS, nunca redeclaradas): `opened`
//     na abertura, `resolved` SÓ após ratificação assinada e verificada.
//
// # Portas (o wiring liga; nada acrescentado ao go.mod)
//
// As etapas do pipeline são PORTAS ([DryRunner] AOS-126, [EvalGate] AOS-114/115/189,
// [CanaryRunner], [Ratifier] AOS-096/206). A geração da skill é a porta [SkillAuthor].
// A emissão de eventos é a porta [GapRecorder] (satisfeita por *plannerevents.Recorder).
// A identidade ([ChildIssuer]) e o orçamento ([Reserver]) usam os tipos reais de
// platform/identity e control-plane/budget (já no módulo). Nenhuma porta executa
// a skill.
//
// # Fronteira honesta (§5, ca_em_falta)
//
// O EXECUTOR DE SKILLS — o runtime que carrega e CORRE a skill ratificada como uma
// capability nova (registo no REG, resolução de tool, dispatch mediado pelo
// Reference Monitor) — é um DESENHO SEPARADO e NÃO faz parte de AOS-240. Este
// pacote governa a ADMISSÃO do artefacto (quem o escreve, sob que orçamento/NHI,
// que etapas tem de passar, quem o ratifica) e emite `capability_gap_resolved` com
// a RatificationID; a activação/execução da capability admitida fica a jusante,
// noutro ticket. O conteúdo da skill ([CandidateSkill.Content]) é bytes opacos
// aqui: nunca é interpretado nem corrido — só hashed, taintado e passado às portas.
package capabilitygap
