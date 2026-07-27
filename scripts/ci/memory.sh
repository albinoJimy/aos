#!/usr/bin/env bash
# memory.sh — GATE de INTEGRIDADE/MIGRAÇÃO/PROVENIÊNCIA de memória (AOS-044).
#
# Corre a SUITE reutilizável de AOS-044 (packages/platform/memory/integritytests) que
# COMPÕE os subpacotes reais do Memory Service e afere, de forma repetível, as três
# propriedades não-negociáveis da camada (EPIC-04) mais a conformidade e a cache:
#   (a) INTEGRIDADE (Princípio 4, AOS-036) — projecção/eviction/compressão NUNCA
#       apagam do REGISTO o que o audit trail exige;
#   (b) MIGRAÇÃO (AOS-041) — round-trip expand→migrate→contract sem perda, rollback de
#       migração falhada, idempotência;
#   (c) PROVENIÊNCIA/SEGURANÇA (AOS-042) — quarentena NÃO autoriza acções, taint
#       transitivo;
#   (d) CRYPTO-SHREDDING/TTL (AOS-038, ADR-011) — irrecuperável SEM partir a hash-chain;
#   (e) ESTABILIDADE DE CACHE (AOS-043, ADR-009) — prefixo imutável sob compressão.
#
# É o análogo, para a MEMÓRIA, do gate replay de AOS-024 (scripts/ci/replay.sh):
# fail-closed e NÃO-VAZIO. Usa require_tests (lib.sh) para exigir que CADA teste
# obrigatório — incluindo os META-TESTES que provam que a suite DETECTA violações —
# tenha EFECTIVAMENTE corrido (não basta o exit 0; um -run que não casasse nada passaria
# vazio). O self-test (scripts/ci/selftest.sh, secção E) prova que uma violação injectada
# torna este gate VERMELHO.
#
# Fail-closed: uma invariante violada OU um meta-teste que deixe de detectar faz o gate
# ficar VERMELHO (exit != 0). A cobertura do módulo de memória não pode regredir abaixo
# do limiar (§4 do Engineering Standards).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

MEM_MOD="packages/platform/memory"
SUITE_PKG="./integritytests/..."

# Cobertura mínima do módulo de memória (não regride). Igual ao limiar do kernel.
# Sobreponível por ambiente APENAS PARA APERTAR: piso FLOOR_MODULE_COVERAGE_MIN (AOS-199).
gate_threshold MEMORY_COVERAGE_MIN 80 "$FLOOR_MODULE_COVERAGE_MIN" 100 "%" || exit 1

# Testes OBRIGATÓRIOS: as três suites + shredding/TTL + cache + os META-TESTES (prova de
# detecção, não green-vazio) + o relatório. require_tests exige que TODOS tenham corrido.
REQUIRED=(
  TestIntegrityProjectionPreservesRecord
  TestIntegrityEvictionPreservesRecord
  TestIntegrityCompressionPreservesRecord
  TestMigrationRoundTripNoLoss
  TestMigrationFailedRollbackIdentical
  TestMigrationIdempotentReapply
  TestProvenanceQuarantineCannotAuthorize
  TestProvenanceTaintTransitive
  TestShreddingIrrecoverableChainIntact
  TestTTLSweepShredsExpired
  TestCachePrefixImmutableUnderCompression
  TestMetaSuiteDetectsRecordErasure
  TestMetaSuiteDetectsQuarantineBreach
  TestMetaSuiteDetectsLossyMigration
  TestMetaSuiteDetectsBrokenHashChain
  TestMetaSuiteDetectsMutatedPrefix
  TestMetaSuiteDetectsRawLeak
  TestMetaSuiteDetectsRegisterIncomplete
  TestSuiteReportEmitted
)
# Regex ancorado (^Test…$) por nome, unido por '|': casa EXACTAMENTE os obrigatórios e
# NUNCA o teste-veneno do self-test (TestSelftint…). Evita apanhar bónus por substring.
RE="^($(IFS='|'; echo "${REQUIRED[*]}"))\$"

log_gate "memory (AOS-044) · integridade/migração/proveniência fail-closed"

# (1) require_tests: os testes obrigatórios (incl. meta-testes) CORRERAM e passaram
# (não-vazio, fail-closed). É o coração do gate — a prova de que a suite não é vacuous.
require_tests "$REPO_ROOT/$MEM_MOD" "$SUITE_PKG" "$RE" "${REQUIRED[@]}" || exit 1

# (2) -race na suite completa (determinismo sob concorrência; corre também os bónus).
log_step "go test -race $SUITE_PKG"
if ! ( cd "$REPO_ROOT/$MEM_MOD" && go test "$SUITE_PKG" -race -count=1 ); then
  log_fail "suite de memória vermelha (-race)"
  exit 1
fi

# (3) Cobertura do MÓDULO de memória não regride (>= MEMORY_COVERAGE_MIN%). Mede-se o
# módulo inteiro (as classes que a suite orquestra), não só o pacote da suite.
log_gate "memory · cobertura do módulo (>= ${MEMORY_COVERAGE_MIN}%)"
cover_prof="$(mktemp)"
trap 'rm -f "$cover_prof"' EXIT
if ! ( cd "$REPO_ROOT/$MEM_MOD" && go test ./... -covermode=atomic -coverprofile="$cover_prof" >/dev/null ); then
  log_fail "cobertura do módulo de memória não mensurável (testes vermelhos)"
  exit 1
fi
pct="$( cd "$REPO_ROOT/$MEM_MOD" && go tool cover -func="$cover_prof" | awk '/^total:/{print $NF}' )"
num="${pct%\%}"
if [ -z "$num" ] || ! awk "BEGIN{exit !($num >= $MEMORY_COVERAGE_MIN)}"; then
  log_fail "LIMIAR NÃO ATINGIDO: cobertura do módulo de memória ${pct} < ${MEMORY_COVERAGE_MIN}% (configuração válida; foi o CÓDIGO que ficou abaixo)"
  exit 1
fi
log_ok "cobertura do módulo de memória ${pct} >= ${MEMORY_COVERAGE_MIN}%"

# (4) RELATÓRIO da suite (linha marcada AOS_MEMORY_REPORT) + fail-closed sobre o
# veredicto agregado. À imagem do AOS_REPLAY_REPORT de AOS-024: o campo "pass" agregado
# é o ÚLTIMO do objecto (…,"pass":true}), pelo que ancorar ao fim da linha (}$) faz a
# verificação reflectir o veredicto AGREGADO, não um sub-campo individual.
log_gate "memory · relatório da suite"
report="$( cd "$REPO_ROOT/$MEM_MOD" && go test "$SUITE_PKG" -run '^TestSuiteReportEmitted$' -v -count=1 2>/dev/null \
  | grep 'AOS_MEMORY_REPORT' | sed 's/.*AOS_MEMORY_REPORT //' | head -1 )"
if [ -z "$report" ]; then
  log_fail "relatório da suite não emitido"
  exit 1
fi
printf '   %s\n' "$report"
if ! printf '%s' "$report" | grep -Eq '"pass":true[[:space:]]*}[[:space:]]*$'; then
  log_fail "relatório indica falha de invariante (pass agregado != true)"
  exit 1
fi

log_ok "memory: verde (integridade/migração/proveniência/shredding/cache + meta-testes)"
