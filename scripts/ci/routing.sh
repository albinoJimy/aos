#!/usr/bin/env bash
# routing.sh — GATE de ROTEAMENTO/FAILOVER adversarial (AOS-063), o ÚLTIMO do EPIC-06.
#
# Corre a SUITE reutilizável de AOS-063 (packages/platform/model-gateway/routingtests)
# que ORQUESTRA os controlos REAIS do Model Gateway (AOS-058/059 — router cost/load-
# aware, tiering, degradação, guarda de soberania, allowlist regional, keypool) e prova,
# por adversário, que CADA cenário de risco de tecnica/06 §5/§6/§9 é REPRODUZIDO e
# tratado correctamente:
#   1. SATURAÇÃO         — selecção least-loaded/token-aware + SEM colapso agregado do
#                          rate limit partilhado (ADR-008): o excedente é ADIADO;
#   2. TIERING            — o tier mais barato que SATISFAZ a capacidade (nunca um
#                          incapaz); interactivo favorece latência, batch tolera barato;
#   3. DEGRADAÇÃO         — shed→defer→degradar→rejeitar sob pressão de orçamento/rate-
#                          limit; exaustão graciosa a ~80% oferece degradar (nunca
#                          hard-stop cego); esgotado sem cheaper sinaliza, não pára;
#   4. FAILOVER INTRA      — primário indisponível + alternativo intra-fronteira →
#                          failover intra; sem intra → REJEIÇÃO (nunca cross-border);
#   5. CROSS-BORDER        — failover cross-border BLOQUEADO fail-closed, com DENY
#                          registado e ATRIBUÍVEL a principal + board (audit WORM AOS-011).
# Cada cenário tem um META-TESTE que, com o controlo CONTORNADO, deixa o ataque PASSAR
# (prova de detecção não-vácua — não green-vazio).
#
# É o análogo, para o ROTEAMENTO/FAILOVER, dos gates supplychain (AOS-054), memory
# (AOS-044) e replay (AOS-024): fail-closed e NÃO-VAZIO. Usa require_tests (lib.sh) para
# exigir que CADA teste obrigatório — os 5 CENÁRIOS + os 8 META-TESTES (detecção) + o
# relatório — tenha EFECTIVAMENTE corrido (não basta o exit 0; um -run que não casasse
# nada passaria vazio). O self-test (scripts/ci/selftest.sh, secção G) prova que um
# cenário DESBLOQUEADO torna este gate VERMELHO.
#
# Fail-closed: um cenário não-tratado OU um meta-teste que deixe de detectar faz o gate
# ficar VERMELHO (exit != 0). A cobertura do módulo do GW não pode regredir abaixo do
# limiar (§4 do Engineering Standards).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

GW_MOD="packages/platform/model-gateway"
SUITE_PKG="./routingtests/..."

# Cobertura mínima do módulo do GW (não regride). Igual ao limiar do kernel/memória.
ROUTING_COVERAGE_MIN="${ROUTING_COVERAGE_MIN:-80}"

# Testes OBRIGATÓRIOS: os 5 cenários + os 8 meta-testes (detecção) + o relatório.
# require_tests exige que TODOS tenham corrido (fail-closed contra vacuous pass).
REQUIRED=(
  TestScenario1_Saturation_LeastLoaded_NoAggregateCollapse
  TestScenario2_Tiering_CheapestCapable_InteractiveVsBatch
  TestScenario3_Degradation_ShedDeferDegradeReject
  TestScenario4_Failover_IntraBoundary_And_Reject
  TestScenario5_CrossBorder_Blocked_DenyAttributable
  TestMetaDetects_AggregateCollapse
  TestMetaDetects_BlindUnderSaturation
  TestMetaDetects_IncapableTierWhenCapabilityIgnored
  TestMetaDetects_DegradationBelowCapability
  TestMetaDetects_DispatchWhenAdmissionOffUnderRateLimit
  TestMetaDetects_AdmitWhenAdmissionOffCostExceedsCeiling
  TestMetaDetects_FailoverCrossBorderWhenBoundaryCollapsed
  TestMetaDetects_CrossBorderRoutedWhenSovereigntyBypassed
  TestSuiteReportEmitted
)
# Regex ancorado (^Test…$) por nome, unido por '|': casa EXACTAMENTE os obrigatórios e
# NUNCA o teste-veneno do self-test (TestSelftestRoutingBypassReddensGate). Evita
# apanhar bónus por substring.
RE="^($(IFS='|'; echo "${REQUIRED[*]}"))\$"

log_gate "routing (AOS-063) · 5 cenários adversariais de roteamento/failover fail-closed"

# (1) require_tests: os testes obrigatórios (incl. meta-testes) CORRERAM e passaram
# (não-vazio, fail-closed). É o coração do gate — a prova de que a suite não é vacuous.
require_tests "$REPO_ROOT/$GW_MOD" "$SUITE_PKG" "$RE" "${REQUIRED[@]}" || exit 1

# (2) -race na suite completa (determinismo sob concorrência).
log_step "go test -race $SUITE_PKG"
if ! ( cd "$REPO_ROOT/$GW_MOD" && go test "$SUITE_PKG" -race -count=1 ); then
  log_fail "suite de roteamento/failover vermelha (-race)"
  exit 1
fi

# (3) Cobertura do MÓDULO do GW não regride (>= ROUTING_COVERAGE_MIN%). Sem -coverpkg,
# cada pacote conta SÓ a sua própria cobertura: mede-se que a cobertura dos testes
# UNITÁRIOS do módulo não regride (a suite adversarial, sendo quase só ficheiros
# _test.go, contribui ~0 e NÃO conduz esta métrica), não a cobertura que a orquestração
# cross-package produziria em runtime (que exigiria -coverpkg=./... para ser contada).
log_gate "routing · piso anti-regressão de cobertura do módulo GW (>= ${ROUTING_COVERAGE_MIN}%; NÃO é a cobertura da suite AOS-063)"
cover_prof="$(mktemp)"
trap 'rm -f "$cover_prof"' EXIT
if ! ( cd "$REPO_ROOT/$GW_MOD" && go test ./... -covermode=atomic -coverprofile="$cover_prof" >/dev/null ); then
  log_fail "cobertura do módulo do GW não mensurável (testes vermelhos)"
  exit 1
fi
pct="$( cd "$REPO_ROOT/$GW_MOD" && go tool cover -func="$cover_prof" | awk '/^total:/{print $NF}' )"
num="${pct%\%}"
if [ -z "$num" ] || ! awk "BEGIN{exit !($num >= $ROUTING_COVERAGE_MIN)}"; then
  log_fail "cobertura do módulo do GW ${pct} < ${ROUTING_COVERAGE_MIN}%"
  exit 1
fi
log_ok "piso anti-regressão do módulo do GW ${pct} >= ${ROUTING_COVERAGE_MIN}% (cobertura dos testes UNITÁRIOS de AOS-055..062; a suite adversarial AOS-063 contribui ~0)"

# (4) RELATÓRIO da suite (linha marcada AOS_ROUTING_REPORT) + fail-closed sobre o
# veredicto agregado. À imagem do AOS_SUPPLYCHAIN_REPORT (AOS-054) e AOS_MEMORY_REPORT
# (AOS-044): o campo "pass" agregado é o ÚLTIMO do objecto (…,"pass":true}), pelo que
# ancorar ao fim da linha (}$) faz a verificação reflectir o veredicto AGREGADO.
log_gate "routing · relatório da suite"
report="$( cd "$REPO_ROOT/$GW_MOD" && go test "$SUITE_PKG" -run '^TestSuiteReportEmitted$' -v -count=1 2>/dev/null \
  | grep 'AOS_ROUTING_REPORT' | sed 's/.*AOS_ROUTING_REPORT //' | head -1 )"
if [ -z "$report" ]; then
  log_fail "relatório da suite não emitido"
  exit 1
fi
printf '   %s\n' "$report"
if ! printf '%s' "$report" | grep -Eq '"pass":true[[:space:]]*}[[:space:]]*$'; then
  log_fail "relatório indica cenário não-tratado (pass agregado != true)"
  exit 1
fi

log_ok "routing: verde (5 cenários de roteamento/failover + deny cross-border atribuível + meta-testes de detecção)"
