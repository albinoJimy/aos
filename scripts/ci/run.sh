#!/usr/bin/env bash
# run.sh — AGREGADOR do gate runner local do AOS (AOS-010).
#
# Corre, por ordem canónica (specs/01 §4), os gates:
#   1) build  2) lint(+arch-lint)  2b) ref-lint  2b') deferrals  2c) rtm  2d) layer-lint
#   deferrals(eixo verificável de cada deferimento declarado no código, AOS-196)
#   3) test(+cobertura)  4) integration(contratos de porta C1–C5, AOS-198)
#   event-catalog(catálogo de tipos de evento, AOS-198/AOS-201)
#   8) replay(harness AOS-024)
#   memory(integridade/migração AOS-044)  supplychain(7 vectores AOS-054)
#   routing(5 cenários de roteamento/failover AOS-063)
#   security(4 cenários adversariais de segurança AOS-075)
#   9) evalgate(eval harness + golden-sets curados / admission control AOS-114)
#   scale(carga/escala: admission global + backpressure + degradação AOS-116)
#   dr-e2e(teste de fogo DR/replay: node loss → failover → resume-from-step AOS-118)
#   ux-dx(usabilidade dos gates + anti-fadiga/override-rate + paridade AOS-128)
#   4) sast  5) sca  6) policy-test
#
# Fail-closed: corre TODOS os gates para dar visibilidade completa, mas termina
# com exit != 0 se QUALQUER um falhar. SEM '|| true' / 'set +e' / 'continue-on-error'
# a mascarar — cada gate é um processo cujo código de saída é avaliado e agregado.
#
# Uso:
#   scripts/ci/run.sh              # todos os gates
#   scripts/ci/run.sh build lint   # apenas os gates indicados
#
# Reprodução por um único comando: 'make ci' (ver Makefile / CONTRIBUTING.md).
set -uo pipefail
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$CI_DIR/lib.sh"

ALL_GATES=(secrets build lint ref-lint deferrals rtm layer-lint test integration event-catalog replay memory supplychain routing apex security evalgate scale dr-e2e ux-dx sast sca policy-test)
GATES=("$@")
[ "${#GATES[@]}" -eq 0 ] && GATES=("${ALL_GATES[@]}")

declare -A RESULT
overall=0
start_all=$(date +%s)

for gate in "${GATES[@]}"; do
  script="$CI_DIR/$gate.sh"
  if [ ! -f "$script" ]; then
    log_fail "gate desconhecido: $gate"
    RESULT["$gate"]="DESCONHECIDO"
    overall=1
    continue
  fi
  t0=$(date +%s)
  if bash "$script"; then
    RESULT["$gate"]="PASS"
  else
    RESULT["$gate"]="FAIL"
    overall=1
  fi
  t1=$(date +%s)
  RESULT["$gate.t"]="$(( t1 - t0 ))s"
done

end_all=$(date +%s)

printf '\n%s================ RESUMO DOS GATES ================%s\n' "$C_BLD" "$C_RST"
for gate in "${GATES[@]}"; do
  r="${RESULT[$gate]:-?}"; dt="${RESULT[$gate.t]:-}"
  if [ "$r" = "PASS" ]; then
    printf '  %sPASS%s  %-14s %s\n' "$C_GRN" "$C_RST" "$gate" "$dt"
  else
    printf '  %sFAIL%s  %-14s %s\n' "$C_RED" "$C_RST" "$gate" "$dt"
  fi
done
printf '  %s-----------------------------------------------%s\n' "$C_BLD" "$C_RST"
printf '  tempo total: %ss\n' "$(( end_all - start_all ))"

if [ "$overall" -eq 0 ]; then
  printf '%s  RESULTADO: TODOS OS GATES VERDES%s\n' "$C_GRN$C_BLD" "$C_RST"
else
  printf '%s  RESULTADO: PIPELINE VERMELHO (fail-closed)%s\n' "$C_RED$C_BLD" "$C_RST"
fi
exit "$overall"
