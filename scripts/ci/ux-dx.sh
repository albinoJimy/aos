#!/usr/bin/env bash
# ux-dx.sh — GATE de TESTES DE UX/DX dos gates de governação (AOS-128, EPIC-12 — o que
# FECHA o epic).
#
# Corre a BATERIA (packages/qa/ux-dx) que COMPÕE as superfícies de governação já Done
# (AOS-120..125) e CONSOME o override-rate de AOS-095, provando de forma repetível e
# DETERMINISTA:
#   AC1 USABILIDADE DOS GATES — o preview é COMPLETO, as opções INEQUÍVOCAS e a decisão
#                               FAIL-CLOSED é respeitada em approval-card (AOS-120),
#                               aprovação-de-plano (AOS-121), exaustão graciosa (AOS-123)
#                               e autonomia progressiva (AOS-125);
#   AC2 ANTI-FADIGA           — o override-rate (AOS-095) é EXPOSTO em cada decisão e um
#                               limiar cronicamente alto (>0.40) é SINALIZADO como problema
#                               de superfície (rubber-stamping); abaixo do limiar NÃO
#                               dispara (não-tautológico). CONSOME a métrica, sem enforcement;
#   AC3 PARIDADE              — a MESMA ApprovalCard rende cards EQUIVALENTES nas 3
#                               plataformas (AOS-122) e degrada FAIL-CLOSED nos canais sem
#                               dual-control (Telegram+irreversível);
#   AC4 ACESSIBILIDADE        — as superfícies expõem rótulos NÃO-VAZIOS e acções NOMEADAS.
# O RELATÓRIO (linha marcada AOS_UXDX_REPORT) reporta a contagem de gates validados e o
# limiar anti-fadiga consumido de AOS-095.
#
# À imagem de dr-e2e.sh (AOS-118) e scale.sh (AOS-116): fail-closed e NÃO-VAZIO. Usa
# require_tests (lib.sh) para exigir que CADA teste obrigatório tenha EFECTIVAMENTE
# corrido (um -run que não casasse nada passaria vazio). Fail-closed: um gate de
# usabilidade não coberto OU o sinal anti-fadiga a deixar de disparar torna o gate
# VERMELHO (exit != 0).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

UXDX_MOD="packages/qa/ux-dx"
SUITE_PKG="./..."

# Testes OBRIGATÓRIOS: usabilidade por gate (4) + anti-fadiga (2, incl. contraprova) +
# paridade (2) + acessibilidade (2) + relatório (1). require_tests exige que TODOS tenham
# corrido (fail-closed contra vacuous pass).
REQUIRED=(
  TestUsability_ApprovalCard_PreviewCompleteAndFailClosedDualControl
  TestUsability_PlanApproval_CompletePlanUnambiguousVerdictsFailClosedSpawn
  TestUsability_ProgressSurface_ThreeUnambiguousOptionsTimeoutDegrades
  TestUsability_AutonomySurface_LevelReadableTransitionsReasonedDemotion
  TestAntiFatigue_OverrideRateExposedAndChronicallyHighSignaled
  TestAntiFatigue_BelowThresholdNotSignaled
  TestParity_EquivalentCardsAcrossPlatforms
  TestParity_IrreversibleDegradesFailClosedOnTelegram
  TestAccessibility_SurfacesExposeLabelsAndNamedActions
  TestAccessibility_ExhaustionOptionsAreNamed
  TestUXDX_Report
)
# Regex ancorado (^Test…$) por nome, unido por '|': casa EXACTAMENTE os obrigatórios.
RE="^($(IFS='|'; echo "${REQUIRED[*]}"))\$"

log_gate "ux-dx (AOS-128) · usabilidade dos gates + anti-fadiga (override-rate AOS-095) + paridade + acessibilidade"

# (1) require_tests: os testes obrigatórios CORRERAM e passaram (não-vazio, fail-closed).
# É o coração do gate — a prova de que a bateria não é vacuous.
require_tests "$REPO_ROOT/$UXDX_MOD" "$SUITE_PKG" "$RE" "${REQUIRED[@]}" || exit 1

# (2) -race na suite completa (determinismo sob concorrência). Tudo é in-process e
# determinista (relógios/seeds injectados), mas -race blinda os primitivos compostos.
log_step "go test $SUITE_PKG -race -count=2 (determinismo: 2x o MESMO veredicto)"
if ! ( cd "$REPO_ROOT/$UXDX_MOD" && go test "$SUITE_PKG" -race -count=2 ); then
  log_fail "bateria de UX/DX vermelha (-race)"
  exit 1
fi

# (3) RELATÓRIO (linha marcada AOS_UXDX_REPORT) + fail-closed sobre o conteúdo. À imagem
# do AOS_DR_REPORT/AOS_SCALE_REPORT: o veredicto "pass" é o ÚLTIMO campo, ancorado ao fim.
log_gate "ux-dx · relatório (gates de usabilidade validados · limiar anti-fadiga de AOS-095)"
report="$( cd "$REPO_ROOT/$UXDX_MOD" && go test "$SUITE_PKG" -run '^TestUXDX_Report$' -v -count=1 2>/dev/null \
  | grep 'AOS_UXDX_REPORT' | sed 's/.*AOS_UXDX_REPORT //' | head -1 )"
if [ -z "$report" ]; then
  log_fail "relatório de UX/DX não emitido (AOS_UXDX_REPORT ausente)"
  exit 1
fi
printf '   %s\n' "$report"

# Fail-closed sobre as invariantes centrais: os 4 gates de usabilidade cobertos, o limiar
# anti-fadiga CONSUMIDO de AOS-095 (0.40), e o veredicto agregado (pass) verdadeiro.
if ! printf '%s' "$report" | grep -q '"usability_gates":4'; then
  log_fail "relatório indica cobertura de usabilidade incompleta (usability_gates != 4)"
  exit 1
fi
if ! printf '%s' "$report" | grep -q '"override_rate_threshold":0.40'; then
  log_fail "relatório não consome o limiar anti-fadiga de AOS-095 (override_rate_threshold != 0.40)"
  exit 1
fi
if ! printf '%s' "$report" | grep -Eq '"pass":true[[:space:]]*}[[:space:]]*$'; then
  log_fail "relatório indica veredicto de UX/DX falhado (pass != true)"
  exit 1
fi

log_ok "ux-dx: verde (4 gates de usabilidade · anti-fadiga override-rate exposto+sinalizado · paridade 3 plataformas · acessibilidade)"
