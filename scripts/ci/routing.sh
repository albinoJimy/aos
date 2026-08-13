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
#   6. SCORING/GUARDAS     — (AOS-269, ADR-021) nenhum peso, por mais extremo, elege um
#                          candidato cross-border ou um modelo fora da allowlist: as
#                          guardas correm ANTES do ranking e NÃO são factores;
#   7. SCORING/DETERMINISMO — a decisão pontuada é FUNÇÃO PURA dos inputs (repetição
#                          byte-a-byte) e o caminho de decisão não tem floats, rand nem
#                          relógio (provado por análise AST do código, com self-check
#                          de não-vacuidade sobre um pacote que usa floats);
#   8. SCORING/FAIL-CLOSED — com o scoring COMPOSTO mas sem tabela de pesos válida e
#                          ASSINADA, o router RECUSA (nunca pesos implícitos); sem o
#                          scoring composto, o comportamento AOS-059 fica INALTERADO
#                          (postura de compatibilidade opt-in declarada);
#   9. SCORING/SATURAÇÃO   — (remediação) uma região SATURADA, ou um erro transitório
#                          de leitura de carga, é PRESSÃO (factor 0) e não uma quarta
#                          guarda: a disposição é a MESMA com e sem scoring, nunca uma
#                          rejeição permanente atribuída à allowlist;
#  10. SCORING/COERÊNCIA   — (remediação) depois de uma degradação por orçamento, o
#                          score e os factores registados descrevem o modelo
#                          DESPACHADO (a calibração offline não aprende do errado);
#  11. SCORING/INTENÇÃO    — (remediação) o perfil de pesos é seleccionável POR PEDIDO
#                          e um perfil desconhecido é recusa fail-closed; a semântica
#                          de CLASSE de AOS-059 (batch não paga o bónus `Fast`) não se
#                          perde ao armar o scoring.
# Cada cenário tem um META-TESTE que, com o controlo CONTORNADO, deixa o ataque PASSAR
# (prova de detecção não-vácua — não green-vazio).
#
# É o análogo, para o ROTEAMENTO/FAILOVER, dos gates supplychain (AOS-054), memory
# (AOS-044) e replay (AOS-024): fail-closed e NÃO-VAZIO. Usa require_tests (lib.sh) para
# exigir que CADA teste obrigatório — os 11 CENÁRIOS + os 10 META-TESTES (detecção) + o
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
# Sobreponível por ambiente APENAS PARA APERTAR: piso FLOOR_MODULE_COVERAGE_MIN (AOS-199).
gate_threshold ROUTING_COVERAGE_MIN 80 "$FLOOR_MODULE_COVERAGE_MIN" 100 "%" || exit 1

# Testes OBRIGATÓRIOS: os 11 cenários (5 de AOS-063 + 3 de scoring AOS-269 + 3 de
# remediação da auditoria adversarial) + os 10 meta-testes (detecção) + o relatório.
# require_tests exige que TODOS tenham corrido (fail-closed contra vacuous pass).
REQUIRED=(
  TestScenario1_Saturation_LeastLoaded_NoAggregateCollapse
  TestScenario2_Tiering_CheapestCapable_InteractiveVsBatch
  TestScenario3_Degradation_ShedDeferDegradeReject
  TestScenario4_Failover_IntraBoundary_And_Reject
  TestScenario5_CrossBorder_Blocked_DenyAttributable
  TestScenario6_Scoring_GuardsFirstNeverElectsCrossBorderOrNonAllowlisted
  TestScenario7_Scoring_PureFunctionDeterministicNoFloats
  TestScenario8_Scoring_FailClosedWithoutSignedWeights
  TestScenario9_Scoring_SaturationIsPressureNotGuard
  TestScenario10_Scoring_DegradationRescoresChosenTier
  TestScenario11_Scoring_ProfilePerRequestAndClass
  TestMetaDetects_AggregateCollapse
  TestMetaDetects_BlindUnderSaturation
  TestMetaDetects_IncapableTierWhenCapabilityIgnored
  TestMetaDetects_DegradationBelowCapability
  TestMetaDetects_DispatchWhenAdmissionOffUnderRateLimit
  TestMetaDetects_AdmitWhenAdmissionOffCostExceedsCeiling
  TestMetaDetects_FailoverCrossBorderWhenBoundaryCollapsed
  TestMetaDetects_CrossBorderRoutedWhenSovereigntyBypassed
  TestMetaDetects_ScoringElectsCrossBorderWhenGuardsBypassed
  TestMetaDetects_TamperedWeightsFlipDecisionWhenSignatureIgnored
  TestSuiteReportEmitted
)
# Regex ancorado (^Test…$) por nome, unido por '|': casa EXACTAMENTE os obrigatórios e
# NUNCA o teste-veneno do self-test (TestSelftestRoutingBypassReddensGate). Evita
# apanhar bónus por substring.
RE="^($(IFS='|'; echo "${REQUIRED[*]}"))\$"

log_gate "routing (AOS-063 + AOS-269) · 11 cenários adversariais de roteamento/failover/scoring fail-closed"

# (0) ARTEFACTO COMPORTAMENTAL — a tabela de pesos do scoring entra no ciclo ADR-012.
# ADR-021 §5 exige «alteração de pesos = bump de versão + passagem no eval-gate ANTES
# de produção». A assinatura ed25519 prova AUTENTICIDADE (quem assinou), não
# GOVERNANÇA (que a mudança foi versionada e avaliada): quem tem a chave offline pode
# reassinar pesos novos mantendo o mesmo `semver`, e nada ficaria vermelho. Este passo
# fecha isso do lado do CI: se weights_table.json diverge do baseline commitado, o
# campo `semver` TEM de subir estritamente. (O admission control do eval-gate sobre a
# tabela é a segunda metade e está declarada como PROCEDIMENTAL em tecnica/06 §6.1 —
# ver DEF-269-EVALGATE em docs/governance/REGISTO-Deferimentos.md.)
WEIGHTS_JSON="$GW_MOD/policy/weights/weights_table.json"
semver_of() { printf '%s' "$1" | tr -d ' \n' | sed -n 's/.*"semver":"\([0-9][0-9.]*\)".*/\1/p'; }
semver_num() { printf '%s' "$1" | awk -F. '{printf "%d%03d%03d", $1, $2, $3}'; }
if git -C "$REPO_ROOT" rev-parse --verify -q HEAD >/dev/null 2>&1 \
   && git -C "$REPO_ROOT" cat-file -e "HEAD:$WEIGHTS_JSON" 2>/dev/null; then
  base_raw="$(git -C "$REPO_ROOT" show "HEAD:$WEIGHTS_JSON")"
  head_raw="$(cat "$REPO_ROOT/$WEIGHTS_JSON")"
  if [ "$base_raw" != "$head_raw" ]; then
    base_sv="$(semver_of "$base_raw")"; head_sv="$(semver_of "$head_raw")"
    if [ -z "$base_sv" ] || [ -z "$head_sv" ]; then
      log_fail "tabela de pesos sem campo semver legivel (ADR-012)"
      exit 1
    fi
    if [ "$(semver_num "$head_sv")" -le "$(semver_num "$base_sv")" ]; then
      log_fail "tabela de pesos ALTERADA sem bump de SemVer (${base_sv} -> ${head_sv}): ADR-021 §5 exige bump + eval-gate ANTES de producao"
      exit 1
    fi
    log_ok "tabela de pesos alterada COM bump de SemVer (${base_sv} -> ${head_sv})"
  else
    log_ok "tabela de pesos inalterada face ao baseline commitado (semver $(semver_of "$head_raw"))"
  fi
fi

# (1) require_tests: os testes obrigatórios (incl. meta-testes) CORRERAM e passaram
# (não-vazio, fail-closed). É o coração do gate — a prova de que a suite não é vacuous.
require_tests "$REPO_ROOT/$GW_MOD" "$SUITE_PKG" "$RE" "${REQUIRED[@]}" || exit 1

# (1-bis) CADEIA REAL DE ROTEAMENTO (AOS-280, fecha DEF-271). A suite acima exercita
# os controlos de roteamento pelo router ISOLADO; estes provam-nos no GATEWAY COMPOSTO
# (modelgateway.NewProduction + Chat), que é onde o estágio `failover` →
# `routingstage`+`router` (com o scoring assinado armado) passou a decidir o caminho
# quente de TODAS as chamadas de modelo. Vivem no pacote RAIZ do módulo (é lá que está
# o composition root e o harness de produção), pelo que precisam do seu próprio
# require_tests — sem ele, apagar o ficheiro deixaria o gate verde.
#
# A REMEDIAÇÃO adversarial de AOS-280 acrescentou quatro: a troca de modelo decidida
# pelo refino SELADA no audit WORM (o par EFECTIVO, não só o pedido); a recusa de
# ARRANQUE de uma RoutingConfig preenchida SEM escada (que desligava em silêncio a
# admissão global do ADR-008); a recusa de ARRANQUE quando a escada declara um modelo
# sem preço numa região alcançável (que só falharia DEPOIS de facturar); e a
# ATRIBUIBILIDADE do erro de perfil desconhecido (que acusava o artefacto de pesos).
REQUIRED_CHAIN=(
  TestAOS280_Chain_CrossBorderDenyStillSealedInWORM
  TestAOS280_Chain_AdmissionSaturationDefersBeforeProvider
  TestAOS280_Chain_BudgetDegradesToCheaperCapableTier
  TestAOS280_Chain_ScoringOrdersSurvivorsByProfile
  TestAOS280_Chain_HealthFailoverSurvivesRefinement
  TestAOS280_NoTiersDeclared_KeepsFailoverOnly
  TestAOS280_ModelOutsideLadder_KeepsSovereignRouteWithoutRefinement
  TestAOS280_UnknownWeightProfile_FailsClosedAtBoot
  TestAOS280_EmptyLadder_FailsClosedAtBoot
  TestAOS280_Chain_ModelSwapSealedInWORM
  TestAOS280_PartialRoutingConfigWithoutTiers_FailsClosedAtBoot
  TestAOS280_UnpricedLadderTier_FailsClosedAtBoot
  TestAOS280_UnknownDefaultProfile_BlamesProfileNotWeightsArtefact
)
RE_CHAIN="^($(IFS='|'; echo "${REQUIRED_CHAIN[*]}"))\$"
log_gate "routing · cadeia real (AOS-280) · soberania+saturação+orçamento+scoring+failover por saúde no GW COMPOSTO"
require_tests "$REPO_ROOT/$GW_MOD" "." "$RE_CHAIN" "${REQUIRED_CHAIN[@]}" || exit 1

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
  log_fail "LIMIAR NÃO ATINGIDO: cobertura do módulo do GW ${pct} < ${ROUTING_COVERAGE_MIN}% (configuração válida; foi o CÓDIGO que ficou abaixo)"
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

log_ok "routing: verde (11 cenários de roteamento/failover/scoring + deny cross-border atribuível + guardas antes do score + meta-testes de detecção)"
