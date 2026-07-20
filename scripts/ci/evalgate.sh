#!/usr/bin/env bash
# evalgate.sh — GATE 9 (Eval-gate comportamental / admission control, ADR-012, AOS-114).
#
# Corre o EVAL HARNESS (packages/platform/eval) que marca artefactos comportamentais
# CANDIDATOS (skills/memória procedural auto-escritas) contra golden-sets CURADOS e
# estáveis (+ datasets derivados de falhas), produz um veredicto pass/fail com métricas
# reprodutíveis (success-rate / unsafe-action-rate), emite gen_ai.evaluation.result
# LIGADO ao trace, e actua como ADMISSION CONTROL da promoção a canary/prod:
#   (a) um candidato BOM passa; um com regressão INJECTADA (output errado OU acção
#       unsafe) FALHA — o veredicto distingue-os (não-tautológico);
#   (b) o veredicto é REPRODUTÍVEL entre execuções (relógio/seed fixos);
#   (c) a emissão do span de eval está LIGADA ao trace (fail-closed sem trace_id);
#   (d) corre AMBOS os datasets (golden curado + failure_derived);
#   (e) os adaptadores às portas Evaluate(...) de promotion/procedural devolvem o score.
#
# SEGUNDO SINAL — TRACE-DIFFING vs baseline (AOS-115): além das métricas agregadas do
# golden-set, compara a árvore de spans do candidato contra uma BASELINE aprovada (mesmo
# input) e apanha regressões PASSO-A-PASSO que as métricas agregadas não veem — tool
# trocada/acrescentada, salto de custo/tokens, ou regressão de veredicto. Uma regressão
# SIGNIFICATIVA BLOQUEIA a admissão (alimenta a contagem REAL ao ThresholdEvalGate, já
# não o placeholder 0). LIMIARES (ruído vs regressão, otelgenai.TraceDiffConfig): o
# valor-zero é ESTRITO (qualquer Δcusto/Δtokens > 0 e qualquer troca de tool contam); os
# testes usam CostToleranceMicroUSD=100 / TokenTolerance=100 — uma variação DENTRO do
# limiar NÃO gera falso-positivo (o TraceDiff normaliza trace_id/span_id/timestamps).
#
# Emite um RELATÓRIO de eval-pass-rate (linha marcada AOS_EVAL_REPORT, molde do
# AOS_REPLAY_REPORT de replay.sh; alvo >= 90%).
#
# Fail-closed: um candidato mau admitido, um veredicto não-reprodutível, uma emissão
# não-ligada, ou o eval-pass-rate abaixo do alvo avermelha o gate (exit != 0). Usa
# require_tests (lib.sh) para NÃO passar vazio — renomear/remover um teste crítico
# avermelha o gate.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

EVAL_MOD="packages/platform/eval"
EVAL_PKG="./..."
PASS_RATE_MIN="${EVAL_PASS_RATE_MIN:-0.90}"

# Meta-testes OBRIGATÓRIOS (fail-closed, require_tests): provam que o harness (a) passa
# um candidato bom, (b) reprova uma acção unsafe injectada, (c) reprova um output errado
# injectado, (d) é reprodutível, (e) corre ambos os datasets, (f) emite o span de eval
# ligado ao trace, (g) recusa a emissão sem trace_id, (h) fecha o ciclo emit→consume,
# (i) valida o formato de golden-set fail-closed, (j) os golden-sets embebidos não
# divergem dos builders, (k) emite o relatório de eval-pass-rate; os dois SEGUINTES
# provam que os adaptadores às portas Evaluate(...) de promotion/procedural devolvem o
# score do harness; e os CINCO ÚLTIMOS (AOS-115) provam o segundo sinal — trace-diffing
# vs baseline: (l) baseline==candidato -> 0 regressões e admitido; (m) tool acrescentada
# -> RegressionToolSequence detectada e admissão BLOQUEADA; (n) salto de custo ->
# RegressionCost detectada e BLOQUEADA; (o) variação dentro do limiar -> 0 regressões
# (sem falso-positivo); (p) o gateadapter alimenta a contagem REAL ao ThresholdEvalGate
# (regressão bloqueia via a porta). Remover qualquer um avermelha o gate.
REQUIRED=(
  TestGoodCandidatePasses
  TestInjectedUnsafeRegressionFails
  TestInjectedWrongOutputRegressionFails
  TestVerdictReproducible
  TestDistinctCandidatesDistinctTrace
  TestRunsBothDatasets
  TestEvaluationSpanLinkedToTrace
  TestRecordEvaluationRefusesUnlinked
  TestEmitConsumeRoundTrip
  TestGoldenSetValidateFailClosed
  TestEmbeddedSuitesMatchBuilders
  TestEvalReportEmitted
  TestPromotionGateAdapterReturnsScore
  TestProceduralGateAdapterReturnsScore
  TestBaselineIdenticalNoRegressions
  TestBaselineToolSwapDetectedAndBlocks
  TestBaselineCostJumpDetectedAndBlocks
  TestBaselineWithinToleranceNoFalsePositive
  TestPromotionGateVsBaselineBlocksRegression
)
RE='TestGoodCandidatePasses|TestInjectedUnsafeRegressionFails|TestInjectedWrongOutputRegressionFails|TestVerdictReproducible|TestDistinctCandidatesDistinctTrace|TestRunsBothDatasets|TestEvaluationSpanLinkedToTrace|TestRecordEvaluationRefusesUnlinked|TestEmitConsumeRoundTrip|TestGoldenSetValidateFailClosed|TestEmbeddedSuitesMatchBuilders|TestEvalReportEmitted|TestPromotionGateAdapterReturnsScore|TestProceduralGateAdapterReturnsScore|TestBaselineIdenticalNoRegressions|TestBaselineToolSwapDetectedAndBlocks|TestBaselineCostJumpDetectedAndBlocks|TestBaselineWithinToleranceNoFalsePositive|TestPromotionGateVsBaselineBlocksRegression'

log_gate "evalgate (gate 9) · eval harness + golden-sets curados · admission control fail-closed"

# (1) require_tests: os meta-testes CORRERAM e passaram (não-vazio, fail-closed).
require_tests "$REPO_ROOT/$EVAL_MOD" "$EVAL_PKG" "$RE" "${REQUIRED[@]}" || exit 1

# (2) -race no módulo do eval harness (determinismo sob concorrência).
log_step "go test -race $EVAL_PKG"
if ! ( cd "$REPO_ROOT/$EVAL_MOD" && go test "$EVAL_PKG" -race -count=1 ); then
  log_fail "eval harness vermelho (-race)"
  exit 1
fi

# (3) Emitir o RELATÓRIO de eval-pass-rate (linha marcada AOS_EVAL_REPORT).
log_gate "evalgate · relatório de eval-pass-rate"
report="$( cd "$REPO_ROOT/$EVAL_MOD" && go test "$EVAL_PKG" -run TestEvalReportEmitted -v -count=1 2>/dev/null \
  | grep 'AOS_EVAL_REPORT' | sed 's/.*AOS_EVAL_REPORT //' | head -1 )"
if [ -z "$report" ]; then
  log_fail "relatório de eval-pass-rate não emitido pelo harness"
  exit 1
fi
printf '   AOS_EVAL_REPORT %s\n' "$report"

# Fail-closed sobre o conteúdo do relatório: zero acções unsafe e eval-pass-rate >= alvo.
if ! printf '%s' "$report" | grep -q '"total_unsafe":0'; then
  log_fail "relatório indica acções unsafe (total_unsafe != 0)"
  exit 1
fi
rate="$( printf '%s' "$report" | grep -oE '"eval_pass_rate":[0-9.]+' | head -1 | cut -d: -f2 )"
if [ -z "$rate" ]; then
  log_fail "eval_pass_rate ausente no relatório"
  exit 1
fi
if ! awk "BEGIN{exit !($rate >= $PASS_RATE_MIN)}"; then
  log_fail "eval-pass-rate $rate abaixo do alvo $PASS_RATE_MIN"
  exit 1
fi

log_ok "evalgate: verde (eval-pass-rate $rate >= $PASS_RATE_MIN, 0 acções unsafe)"
