#!/usr/bin/env bash
# dr-e2e.sh — GATE de DR/replay end-to-end (AOS-118, EPIC-11 — o que FECHA o epic).
#
# Corre o TESTE DE FOGO (packages/qa/dr-e2e, dr_replay_e2e_test.go) que COMPÕE os
# primitivos já Done numa história de desastre REALISTA e prova, de forma repetível
# e DETERMINISTA:
#   AC1 ZERO EVENTOS PERDIDOS  — mata o LÍDER do Event Store a meio de escritas
#                                concorrentes; o failover para a réplica sobrevivente
#                                (quórum) é automático; a RECONCILIAÇÃO do log mostra
#                                que nenhum committed desaparece;
#   AC2 RESUME-FROM-STEP FIEL  — após o failover, um worker durável FRESCO retoma o
#                                run do log restaurado (Skipped>0 && Completed) e a
#                                golden de delegação replaya com fidelidade 1.0;
#   AC3 ZERO EFEITOS DUPLICADOS— o Kill intercalado entre a aplicação e a reconstrução
#                                do worker seguinte NÃO duplica o efeito (dedup por
#                                f(run_id,step_id), reutilizando AOS-112);
#   AC4 SOBERANIA + FENCING     — o novo líder fica IN-REGION, um cluster cross-border
#                                é recusado por construção, e a escrita de um worker
#                                obsoleto é fenced-out (ErrStaleFencingToken);
#   AC5 MTTR + REPLAY-FIDELITY  — medidos e REPORTADOS na linha marcada AOS_DR_REPORT
#                                (MTTR <= alvo de disponibilidade do plano de controlo).
# O META-TESTE prova detecção NÃO-VÁCUA: sem quórum sobrevivente o Store recusa
# escritas fail-closed (ErrNoQuorum), e o filtro de soberania recusa por construção um
# cluster cross-border — os controlos são load-bearing, não tautológicos.
#
# À imagem de replay.sh (AOS-024) e scale.sh (AOS-116): fail-closed e NÃO-VAZIO. Usa
# require_tests (lib.sh) para exigir que CADA teste obrigatório — os 5 cenários (AC1..
# AC5) + os 2 meta-testes — tenha EFECTIVAMENTE corrido (um -run que não casasse nada
# passaria vazio). Fail-closed: um cenário não-tratado OU um meta-teste que deixe de
# detectar torna o gate VERMELHO (exit != 0).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

DRE2E_MOD="packages/qa/dr-e2e"
SUITE_PKG="./..."

# Testes OBRIGATÓRIOS: os 5 cenários (AC1..AC5) + os 2 meta-testes (detecção não-vácua).
# require_tests exige que TODOS tenham corrido (fail-closed contra vacuous pass).
REQUIRED=(
  TestDR_NodeLoss_ZeroEventsConfirmedLost
  TestDR_Failover_ResumeFromStepFidelity
  TestDR_Failover_ZeroDuplicatedEffects
  TestDR_Failover_StaysInRegion
  TestDR_ReportsMTTRAndFidelity
  TestMetaDetects_DRLossWithoutQuorum
  TestMetaDetects_SovereigntyBlocksCrossBorderPromotion
)
# Regex ancorado (^Test…$) por nome, unido por '|': casa EXACTAMENTE os obrigatórios.
RE="^($(IFS='|'; echo "${REQUIRED[*]}"))\$"

log_gate "dr-e2e (AOS-118) · teste de fogo DR/replay: node loss → failover → resume-from-step"

# (1) require_tests: os testes obrigatórios (incl. meta-testes) CORRERAM e passaram
# (não-vazio, fail-closed). É o coração do gate — a prova de que a suite não é vacuous.
require_tests "$REPO_ROOT/$DRE2E_MOD" "$SUITE_PKG" "$RE" "${REQUIRED[@]}" || exit 1

# (2) -race na suite completa (determinismo sob concorrência). A carga de escrita corre
# concorrente, mas as asserções são sobre o estado committed — 2 execuções, mesmo
# veredicto.
log_step "go test $SUITE_PKG -race -count=2 (determinismo: 2x o MESMO veredicto)"
if ! ( cd "$REPO_ROOT/$DRE2E_MOD" && go test "$SUITE_PKG" -race -count=2 ); then
  log_fail "suite de DR/replay end-to-end vermelha (-race)"
  exit 1
fi

# (3) RELATÓRIO MTTR + Replay-fidelity (linha marcada AOS_DR_REPORT) + fail-closed sobre
# o conteúdo. À imagem do AOS_REPLAY_REPORT (AOS-024): o veredicto "pass" é o ÚLTIMO
# campo do objecto, ancorado ao fim da linha ("pass":true}$).
log_gate "dr-e2e · relatório MTTR + Replay-fidelity"
report="$( cd "$REPO_ROOT/$DRE2E_MOD" && go test "$SUITE_PKG" -run '^TestDR_ReportsMTTRAndFidelity$' -v -count=1 2>/dev/null \
  | grep 'AOS_DR_REPORT' | sed 's/.*AOS_DR_REPORT //' | head -1 )"
if [ -z "$report" ]; then
  log_fail "relatório de DR não emitido (AOS_DR_REPORT ausente)"
  exit 1
fi
printf '   %s\n' "$report"

# Fail-closed sobre as invariantes centrais do teste de fogo: zero perda, zero efeitos
# duplicados, sem cruzar a fronteira, e o veredicto agregado (pass) verdadeiro.
if ! printf '%s' "$report" | grep -q '"events_lost":0'; then
  log_fail "relatório indica PERDA de eventos confirmados (events_lost != 0)"
  exit 1
fi
if ! printf '%s' "$report" | grep -q '"duplicated_effects":0'; then
  log_fail "relatório indica efeitos DUPLICADOS (duplicated_effects != 0)"
  exit 1
fi
if ! printf '%s' "$report" | grep -q '"crossed_boundary":false'; then
  log_fail "relatório indica que o failover CRUZOU a fronteira de soberania"
  exit 1
fi
if ! printf '%s' "$report" | grep -Eq '"pass":true[[:space:]]*}[[:space:]]*$'; then
  log_fail "relatório indica veredicto de DR falhado (pass != true)"
  exit 1
fi

log_ok "dr-e2e: verde (5 cenários AC1..AC5 + 2 meta-testes · zero-loss · zero-dup · in-region · MTTR<=alvo)"
