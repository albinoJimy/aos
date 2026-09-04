#!/usr/bin/env bash
# supplychain.sh — GATE de SUPPLY-CHAIN adversarial (AOS-054), o ÚLTIMO do EPIC-05.
#
# Corre a SUITE reutilizável de AOS-054 (packages/platform/registry/supplychaintests)
# que ORQUESTRA os controlos REAIS do REG (AOS-045..053) e prova, por adversário, que
# CADA vector da tabela de riscos de tecnica/05 §9 é REPRODUZIDO e BLOQUEADO:
#   1. RUG-PULL           — conteúdo re-hasheado sem assinatura legítima → recusa de
#                           admissão (AOS-045 gate + AOS-048 assinatura);
#   2. SCHEMA DRIFT        — servidor MCP muta o schema após pinned → changed (AOS-049);
#   3. RUG-PULL A MEIO DO RUN — definição diverge do congelado → revalidação bloqueia +
#                           quarentena (AOS-050 + AOS-051);
#   4. TOOL POISONING      — descrição MCP injectada permanece untrusted, não comanda o
#                           planeador (ADR-005; barreira AOS-042 reutilizada por AOS-046);
#   5. RESOLUÇÃO POR LATEST — referência flutuante rejeitada (AOS-047/045);
#   6. CAPACIDADE FORA DO CATÁLOGO — recusada por default-deny (ADR-002; AOS-045);
#   7. REPLAY INFIEL       — o manifesto de dependências reproduz o passado apesar da
#                           evolução de tool (ADR-012; AOS-052).
# Cada bloqueio é atestado na hash-chain WORM tamper-evident (AOS-011) e re-verificado
# com audit.Verify + os campos do registo.
#
# É o análogo, para a SUPPLY-CHAIN, dos gates replay (AOS-024) e memory (AOS-044):
# fail-closed e NÃO-VAZIO. Usa require_tests (lib.sh) para exigir que CADA teste
# obrigatório — os 7 VECTORES + os 7 META-TESTES (prova de detecção, não green-vazio) +
# o relatório — tenha EFECTIVAMENTE corrido (não basta o exit 0; um -run que não casasse
# nada passaria vazio). O self-test (scripts/ci/selftest.sh, secção F) prova que um
# vector DESBLOQUEADO torna este gate VERMELHO.
#
# Fail-closed: um vector não-bloqueado OU um meta-teste que deixe de detectar faz o gate
# ficar VERMELHO (exit != 0). A cobertura do módulo do REG não pode regredir abaixo do
# limiar (§4 do Engineering Standards).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

REG_MOD="packages/platform/registry"
SUITE_PKG="./supplychaintests/..."

# Cobertura mínima do módulo do REG (não regride). Igual ao limiar do kernel/memória.
# Sobreponível por ambiente APENAS PARA APERTAR: piso FLOOR_MODULE_COVERAGE_MIN (AOS-199).
gate_threshold REGISTRY_COVERAGE_MIN 80 "$FLOOR_MODULE_COVERAGE_MIN" 100 "%" || exit 1

# Testes OBRIGATÓRIOS: os 7 vectores + os 7 meta-testes (detecção) + o relatório.
# require_tests exige que TODOS tenham corrido (fail-closed contra vacuous pass).
REQUIRED=(
  TestVector1_RugPull_Blocked
  TestVector2_SchemaDrift_Blocked
  TestVector3_RugPullMidRun_BlockedAndQuarantined
  TestVector4_ToolPoisoning_RemainsUntrusted
  TestVector5_FloatingResolution_Rejected
  TestVector6_OutOfCatalog_DefaultDeny
  TestVector7_FaithfulReplay_ViaManifest
  TestVector8_MCPServerRugPull_Blocked
  TestMetaDetects_RugPull
  TestMetaDetects_SchemaDrift
  TestMetaDetects_RugPullMidRun
  TestMetaDetects_ToolPoisoning
  TestMetaDetects_FloatingResolution
  TestMetaDetects_OutOfCatalog
  TestMetaDetects_UnfaithfulReplay
  TestMetaDetects_MCPServerRugPull
  TestSuiteReportEmitted
)
# Regex ancorado (^Test…$) por nome, unido por '|': casa EXACTAMENTE os obrigatórios e
# NUNCA o teste-veneno do self-test (TestSelftestSupplychainBypassReddensGate). Evita
# apanhar bónus por substring.
RE="^($(IFS='|'; echo "${REQUIRED[*]}"))\$"

log_gate "supplychain (AOS-054) · 7 vectores adversariais + audit WORM fail-closed"

# (1) require_tests: os testes obrigatórios (incl. meta-testes) CORRERAM e passaram
# (não-vazio, fail-closed). É o coração do gate — a prova de que a suite não é vacuous.
require_tests "$REPO_ROOT/$REG_MOD" "$SUITE_PKG" "$RE" "${REQUIRED[@]}" || exit 1

# (2) -race na suite completa (determinismo sob concorrência).
log_step "go test -race $SUITE_PKG"
if ! ( cd "$REPO_ROOT/$REG_MOD" && go test "$SUITE_PKG" -race -count=1 ); then
  log_fail "suite de supply-chain vermelha (-race)"
  exit 1
fi

# (3) Cobertura do MÓDULO do REG não regride (>= REGISTRY_COVERAGE_MIN%). Sem -coverpkg,
# cada pacote conta SÓ a sua própria cobertura de testes: mede-se que a cobertura dos
# testes UNITÁRIOS do módulo não regride (a suite adversarial, sendo quase só ficheiros
# _test.go, contribui ~0 e NÃO conduz esta métrica), não a cobertura que a orquestração
# cross-package produziria em runtime (que exigiria -coverpkg=./... para ser contada).
log_gate "supplychain · cobertura do módulo REG (>= ${REGISTRY_COVERAGE_MIN}%)"
cover_prof="$(mktemp)"
trap 'rm -f "$cover_prof"' EXIT
if ! ( cd "$REPO_ROOT/$REG_MOD" && go test ./... -covermode=atomic -coverprofile="$cover_prof" >/dev/null ); then
  log_fail "cobertura do módulo do REG não mensurável (testes vermelhos)"
  exit 1
fi
pct="$( cd "$REPO_ROOT/$REG_MOD" && go tool cover -func="$cover_prof" | awk '/^total:/{print $NF}' )"
num="${pct%\%}"
if [ -z "$num" ] || ! awk "BEGIN{exit !($num >= $REGISTRY_COVERAGE_MIN)}"; then
  log_fail "LIMIAR NÃO ATINGIDO: cobertura do módulo do REG ${pct} < ${REGISTRY_COVERAGE_MIN}% (configuração válida; foi o CÓDIGO que ficou abaixo)"
  exit 1
fi
log_ok "cobertura do módulo do REG ${pct} >= ${REGISTRY_COVERAGE_MIN}%"

# (4) RELATÓRIO da suite (linha marcada AOS_SUPPLYCHAIN_REPORT) + fail-closed sobre o
# veredicto agregado. À imagem do AOS_REPLAY_REPORT (AOS-024) e AOS_MEMORY_REPORT
# (AOS-044): o campo "pass" agregado é o ÚLTIMO do objecto (…,"pass":true}), pelo que
# ancorar ao fim da linha (}$) faz a verificação reflectir o veredicto AGREGADO.
log_gate "supplychain · relatório da suite"
report="$( cd "$REPO_ROOT/$REG_MOD" && go test "$SUITE_PKG" -run '^TestSuiteReportEmitted$' -v -count=1 2>/dev/null \
  | grep 'AOS_SUPPLYCHAIN_REPORT' | sed 's/.*AOS_SUPPLYCHAIN_REPORT //' | head -1 )"
if [ -z "$report" ]; then
  log_fail "relatório da suite não emitido"
  exit 1
fi
printf '   %s\n' "$report"
if ! printf '%s' "$report" | grep -Eq '"pass":true[[:space:]]*}[[:space:]]*$'; then
  log_fail "relatório indica vector não-bloqueado (pass agregado != true)"
  exit 1
fi

log_ok "supplychain: verde (7 vectores bloqueados + audit WORM + meta-testes de detecção)"
