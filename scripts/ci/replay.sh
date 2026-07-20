#!/usr/bin/env bash
# replay.sh — GATE 8 (Replay determinístico + idempotência por passo).
#
# Corre o HARNESS reutilizável de AOS-024 (packages/kernel/agent-runtime/harness)
# sobre as golden trajectories versionadas e verifica, de forma repetível:
#   (a) REPLAY DETERMINÍSTICO — cada trajectória reproduz-se resume-from-step com
#       os hashes a coincidir (replay-fidelity 100%);
#   (b) IDEMPOTÊNCIA POR PASSO — reexecutar um passo com a mesma idempotency key
#       produz ZERO efeitos observáveis duplicados (via step-ledger de AOS-014);
#   (c) FAULT-INJECTION — pontos de crash retomam no estado correcto.
#
# Emite um RELATÓRIO de fidelidade de replay (specs/01 §9, driver replay-fidelity).
#
# Fail-closed: uma trajectória divergente OU um efeito duplicado faz o gate ficar
# VERMELHO (exit != 0). Usa require_tests (lib.sh) para NÃO passar vazio — os
# meta-testes que provam que o harness DETECTA falhas têm de correr efectivamente.
# O self-test (scripts/ci/selftest.sh, secção D) prova que uma trajectória
# adulterada bloqueia este gate.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

HARNESS_MOD="packages/kernel/agent-runtime"
HARNESS_PKG="./harness/..."

# Meta-testes OBRIGATÓRIOS: provam que o harness (a) passa nas golden, (b) apanha
# trajectória adulterada, (c) apanha efeito duplicado, (d) é reprodutível, (e)
# retoma correctamente sob crash, e (f) emite o relatório de fidelidade. Os três
# seguintes são a suite de replay determinístico de EPIC-11/AOS-111 sobre a
# trajectória MULTI-PASSO COM SUB-AGENTE: (g) replay positivo resume-from-step de
# domínio, (h) replay negativo (prefixo do prompt / seed) detectado em vermelho, e
# (i) o driver "% de trajectórias 100% reproduzíveis". Os três ÚLTIMOS são a suite de
# IDEMPOTÊNCIA POR PASSO de EPIC-11/AOS-112 sobre uma activity de domínio com efeito
# externo: (j) exactamente-uma-vez sob retry+crash com a mesma f(run_id,step_id), (k)
# fencing rejeita a escrita de worker obsoleto sem corromper estado, e (l) a saga de
# compensação (failed→compensating→ready) permite retry idempotente. Renomear/remover
# qualquer um avermelha o gate (require_tests, fail-closed).
REQUIRED=(
  TestGoldenReplayIdempotency
  TestHarnessDetectsTamperedTrajectory
  TestHarnessDetectsDuplicatedEffect
  TestFixturesReproducible
  TestFaultInjectionResume
  TestFidelityReportEmitted
  TestReplayResumeFromStepDomainSuite
  TestReplayDomainNegativeDetected
  TestPerfectFractionReported
  TestDomainEffectExactlyOnceUnderRetry
  TestDomainFencingRejectsStaleWrite
  TestDomainSagaCompensatesAndRetriesIdempotent
)
RE='TestGoldenReplayIdempotency|TestHarnessDetectsTamperedTrajectory|TestHarnessDetectsDuplicatedEffect|TestFixturesReproducible|TestFaultInjectionResume|TestFidelityReportEmitted|TestReplayResumeFromStepDomainSuite|TestReplayDomainNegativeDetected|TestPerfectFractionReported|TestDomainEffectExactlyOnceUnderRetry|TestDomainFencingRejectsStaleWrite|TestDomainSagaCompensatesAndRetriesIdempotent'

log_gate "replay (gate 8) · harness replay/idempotência fail-closed"

# (1) require_tests: os meta-testes CORRERAM e passaram (não-vazio, fail-closed).
require_tests "$REPO_ROOT/$HARNESS_MOD" "$HARNESS_PKG" "$RE" "${REQUIRED[@]}" || exit 1

# (2) -race no pacote do harness (determinismo sob concorrência).
log_step "go test -race $HARNESS_PKG"
if ! ( cd "$REPO_ROOT/$HARNESS_MOD" && go test "$HARNESS_PKG" -race -count=1 ); then
  log_fail "harness de replay vermelho (-race)"
  exit 1
fi

# (3) Emitir o RELATÓRIO de fidelidade de replay (linha marcada AOS_REPLAY_REPORT).
log_gate "replay · relatório de fidelidade"
report="$( cd "$REPO_ROOT/$HARNESS_MOD" && go test "$HARNESS_PKG" -run TestFidelityReportEmitted -v -count=1 2>/dev/null \
  | grep 'AOS_REPLAY_REPORT' | sed 's/.*AOS_REPLAY_REPORT //' | head -1 )"
if [ -z "$report" ]; then
  log_fail "relatório de fidelidade não emitido pelo harness"
  exit 1
fi
printf '   %s\n' "$report"

# Fail-closed sobre o conteúdo do relatório: fidelidade 100% e zero duplicados.
# ANCORADO AO VEREDICTO AGREGADO: o AOS_REPLAY_REPORT é o AggregateReport.CompactJSON,
# cujo campo agregado "pass" é o ÚLTIMO do objecto de topo (…,"pass":<bool>}). Um grep
# solto por '"pass":true' casaria também o sub-objecto "pass" de um caso individual que
# passasse (falso-verde se um caso passasse e o agregado fosse false). Ancorar ao fim da
# linha (}$) faz a verificação reflectir o veredicto AGREGADO, não um sub-objecto de caso.
if ! printf '%s' "$report" | grep -Eq '"pass":true[[:space:]]*}[[:space:]]*$'; then
  log_fail "relatório indica falha de fidelidade/idempotência (pass agregado != true)"
  exit 1
fi
if ! printf '%s' "$report" | grep -q '"total_duplicated_effects":0'; then
  log_fail "relatório indica efeitos duplicados"
  exit 1
fi

log_ok "replay: verde (fidelidade 100%, 0 efeitos duplicados)"
