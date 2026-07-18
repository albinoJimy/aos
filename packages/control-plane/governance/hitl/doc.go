// Package hitl é o gate HITL CONCRETO de AOS-095 (Art. 14 do EU AI Act): a
// implementação real da porta [risk.ConfirmationChannel] (o molde de kernel é o
// [risk.DenyChannel], que nega tudo — o fail-closed base). Onde o gate de risco
// (AOS-074) apenas ESCALA uma acção danger/irreversível, este módulo materializa a
// confirmação humana EFECTIVA com as quatro propriedades do Art. 14:
//
//   - APROVADOR AUTORIZADO — a aprovação vem de um principal com AUTORIDADE para a
//     classe (autoridade resolvida do [ApproverRegistry]; "utilizador ∩ classe").
//     Uma aprovação de principal sem autoridade é RECUSADA (fail-closed, AC2).
//   - TIMEOUT FAIL-CLOSED — para acções irreversíveis o silêncio NEGA, nunca
//     permite: o [Channel] respeita o ctx (o gate impõe-lhe a deadline
//     não-desactivável) e uma expiração conta em Timeouts e devolve deny (AC3).
//   - APROVAÇÃO ASSINADA — cada decisão (aprovação OU recusa) é assinada ed25519
//     pelo aprovador via [messaging.Signer] (chave privada no broker/Vault,
//     server-side — NUNCA neste módulo), sobre a serialização canónica de
//     (request-id + decisão + aprovador + nonce + timestamp); é VERIFICÁVEL contra
//     a chave pública PINADA e SELADA no audit tamper-evident. Uma assinatura
//     forjada/inválida é RECUSADA (não-repúdio, AC4).
//   - OVERRIDE-RATE — a fracção de prompts que resultam em aprovação (Overrides/
//     Prompted, molde AOS-074) é MEDIDA e EXPOSTA como sinal OTel; um valor
//     cronicamente alto dispara um sinal anti-rubber-stamping (alimenta AOS-090, AC5).
//
// O tiering SA-ROC (safe corre, gray agrupa em lote, danger confirma individual +
// dual-control 4-eyes) é expresso como POLICY-AS-CODE versionada e fail-closed
// ([TieringPolicy]) — AC6.
//
// COMPOSIÇÃO (ADR-013/ADR-011): o [Channel] não importa nada do kernel além dos
// TIPOS de [risk]; é uma porta que o gate de risco aceita — o wiring concreto
// liga-se no composition root (diferido). Depende, por porta, de: [ApproverRegistry]
// (aprovadores + chaves públicas pinadas + autoridade), [ApprovalSource] (o canal
// out-of-band que devolve a decisão assinada ou bloqueia até ao timeout), um relógio,
// um [audit.Store] (selar a decisão assinada) e um [Tracer]/[MetricSink]
// (override-rate). A chave PRIVADA nunca entra aqui — só a pública pinada e, em
// teste, seeds efémeras.
package hitl
