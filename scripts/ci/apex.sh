#!/usr/bin/env bash
# apex.sh — GATE do COMPOSITION-ROOT (ápice de integração, AOS-146/147/148, PR-0.a).
#
# O módulo packages/integration é o ÚNICO que depende dos pilares concretos em
# simultâneo e os COMPÕE por porta (idioma AOS-060) através das costuras públicas
# NewProduction (RM e Model Gateway, AOS-147). É onde o enforcement de produção é
# MONTADO — logo a sua regressão silenciosa desmontaria as garantias que os pilares
# impõem. Este gate prova, fail-closed e NÃO-VAZIO, que o wiring do ápice continua a:
#   - MEDIAR totalmente (uma tool que sofreu drift do snapshot congelado NÃO executa;
#     um run não-congelado é NEGADO);
#   - falhar FECHADO sem audit durável (NewProduction recusa por construção);
#   - impor a ALLOWLIST regional (allow chega ao provider; deny NUNCA o alcança);
#   - BLOQUEAR failover cross-border no data-plane vivo (fica preso à fronteira);
#   - congelar o RunToolSet por-run (imutável) e revalidar fail-closed.
#
# É o análogo, para o ÁPICE, dos gates routing (AOS-063), supplychain (AOS-054) e
# memory (AOS-044): usa require_tests (lib.sh) para exigir que CADA invariante de
# wiring tenha EFECTIVAMENTE corrido (não basta o exit 0; um -run que não casasse
# nada passaria vazio). Fail-closed: uma invariante não-provada OU a cobertura do
# módulo abaixo do piso torna o gate VERMELHO. O módulo é descoberto por
# discover_modules (find packages -name go.mod), pelo que build/test genéricos já o
# cobrem; este gate acrescenta a prova NÃO-VÁCUA do enforcement composto.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

APEX_MOD="packages/integration"
APEX_PKG="./..."

# Piso anti-regressão de cobertura do módulo do ápice (igual ao kernel/GW: 80%).
# Sobreponível por ambiente APENAS PARA APERTAR: piso FLOOR_MODULE_COVERAGE_MIN (AOS-199).
gate_threshold APEX_COVERAGE_MIN 80 "$FLOOR_MODULE_COVERAGE_MIN" 100 "%" || exit 1

# Invariantes de wiring OBRIGATÓRIAS: os e2e do Model Gateway (AOS-058) + os do
# SecuredRuntime/freeze/revalidação (AOS-050/051) + as unidades de composição.
# require_tests exige que TODAS tenham corrido (fail-closed contra vacuous pass).
REQUIRED=(
  TestApex_FailClosedWithoutAudit
  TestApex_AllowlistEnforced_AllowPath
  TestApex_AllowlistEnforced_DenyNeverReachesProvider
  TestApex_CrossBorderFailover_BlockedInLiveDataPlane
  TestApex_FailoverIntraBorder_StaysInJurisdiction
  TestTotalMediation_DriftedToolCannotExecute
  TestTotalMediation_UnfrozenRunDenied
  TestSecuredRuntime_RealChain_FreezeAndFailClosed
  TestSecuredRuntime_RealHookChain_SingleWORM
  TestApexEnforcement_FiveDenials
  TestFreeze_PerRunImmutable
  TestNewRevalidationHook_FailClosed
  TestNewSecuredRuntime_FailClosed
  TestRunToolSets_Lifecycle
  TestApplyFrozenToGoal
  TestCatalogResolver_Current
  TestProvenanceQuarantiner_Admits
  TestWindowManagerFactory_ByteIdenticalToInline
  TestDurableDispatcher_PreservesCredential
  # TestCompactionTriggerAdapter_ObserveGating saiu em AOS-298 com o adaptador que testava: a
  # porta de sinal de janela (WindowSignal/CompactionTrigger) foi REMOVIDA por ter zero
  # chamadores de produção em toda a cadeia — sinal, eviction, sink e drenagem da fila. Este
  # gate apanhou a remoção, que é o que ele existe para fazer; a entrada sai porque o
  # adaptador saiu, e não porque o teste se tornou inconveniente.
)
# Regex ancorado (^Test…$) unido por '|': casa EXACTAMENTE os obrigatórios e nunca
# um teste-veneno por substring.
RE="^($(IFS='|'; echo "${REQUIRED[*]}"))\$"

log_gate "apex (AOS-146/147/148) · composition-root: enforcement composto fail-closed e não-vazio"

# (1) require_tests: as invariantes de wiring CORRERAM e passaram (não-vazio,
# fail-closed). É o coração do gate — a prova de que a suite do ápice não é vacuous.
require_tests "$REPO_ROOT/$APEX_MOD" "$APEX_PKG" "$RE" "${REQUIRED[@]}" || exit 1

# (2) -race na suite completa do ápice (determinismo sob concorrência).
log_step "go test -race $APEX_PKG"
if ! ( cd "$REPO_ROOT/$APEX_MOD" && go test "$APEX_PKG" -race -count=1 ); then
  log_fail "suite do ápice vermelha (-race)"
  exit 1
fi

# (3) Cobertura do MÓDULO do ápice não regride (>= APEX_COVERAGE_MIN%).
log_gate "apex · piso anti-regressão de cobertura do módulo (>= ${APEX_COVERAGE_MIN}%)"
cover_prof="$(mktemp)"
trap 'rm -f "$cover_prof"' EXIT
if ! ( cd "$REPO_ROOT/$APEX_MOD" && go test "$APEX_PKG" -covermode=atomic -coverprofile="$cover_prof" >/dev/null ); then
  log_fail "cobertura do ápice não mensurável (testes vermelhos)"
  exit 1
fi
pct="$( cd "$REPO_ROOT/$APEX_MOD" && go tool cover -func="$cover_prof" | awk '/^total:/{print $NF}' )"
if ! coverage_meets_min "$pct" "$APEX_COVERAGE_MIN"; then
  log_fail "LIMIAR NÃO ATINGIDO: cobertura do ápice ${pct:-n/a} < ${APEX_COVERAGE_MIN}% (ou não-mensurável) — configuração válida, foi o CÓDIGO que ficou abaixo"
  exit 1
fi
log_ok "piso anti-regressão do ápice ${pct} >= ${APEX_COVERAGE_MIN}%"

log_ok "apex: verde (mediação total + fail-closed sem audit + allowlist regional + bloqueio cross-border + freeze por-run)"
