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
#   policy-taint(cada permit da política assinada exige context.taint != untrusted, AOS-376)
#
# Fail-closed: corre TODOS os gates para dar visibilidade completa, mas termina
# com exit != 0 se QUALQUER um falhar. SEM '|| true' / 'set +e' / 'continue-on-error'
# a mascarar — cada gate é um processo cujo código de saída é avaliado e agregado.
#
# VERDE PARCIAL (AOS-474). Um gate que SALTA uma etapa (`gate_skip`, lib.sh) sai 0 e declara-o
# no próprio output — e o veredicto daqui dizia «TODOS OS GATES VERDES», com o
# AOS_SKIPPED_STEP perdido a meio. Cada gate corre agora com AOS_RUN_SKIP_LEDGER a apontar para
# um ficheiro seu; o veredicto REDECLARA todas as etapas saltadas, com o gate, o motivo e a
# garantia por verificar, e nunca diz «todos verdes» se alguma saltou. Saídas, as do
# `package.sh` (CONTRIBUTING §«Registar não é impedir»):
#   0  verde: nenhum gate falhou e nenhuma etapa foi saltada;
#   1  vermelho: pelo menos um gate falhou (ganha a qualquer salto);
#   3  VERDE PARCIAL: nada falhou, mas alguma etapa NÃO correu.
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

# `dormencia` entra na lista e `isolation-live` NÃO, e a assimetria é deliberada. O primeiro
# corre em qualquer runner (só compila suites e conta testes saltados) e é o que impede a
# dormência de virar apodrecimento — um gate que nunca corre não impede nada, que foi o
# achado da revisão adversarial de AOS-358. O segundo precisa de Linux com docker
# privilegiado, salta ruidosamente onde não o há, e invoca-se por `make ci-isolation-live`.
ALL_GATES=(secrets build lint ref-lint deferrals estado-citado rtm layer-lint test integration event-catalog stream-names replay memory supplychain routing apex security evalgate scale dr-e2e ux-dx nats dormencia sast sca policy-test policy-taint)
GATES=("$@")
[ "${#GATES[@]}" -eq 0 ] && GATES=("${ALL_GATES[@]}")

declare -A RESULT
overall=0
start_all=$(date +%s)

# Um ficheiro de saltos por gate, FORA do repo; o `trap` limpa-o. O nome do gate é a chave.
SKIP_DIR="$(mktemp -d)"
trap 'rm -rf "$SKIP_DIR"' EXIT

for gate in "${GATES[@]}"; do
  script="$CI_DIR/$gate.sh"
  if [ ! -f "$script" ]; then
    log_fail "gate desconhecido: $gate"
    RESULT["$gate"]="DESCONHECIDO"
    overall=1
    continue
  fi
  t0=$(date +%s)
  ledger="$SKIP_DIR/$gate.tsv"
  : > "$ledger"
  if AOS_RUN_SKIP_LEDGER="$ledger" bash "$script"; then
    if [ -s "$ledger" ]; then
      RESULT["$gate"]="PARCIAL"
    else
      RESULT["$gate"]="PASS"
    fi
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
  elif [ "$r" = "PARCIAL" ]; then
    printf '  %sPARCIAL%s %-12s %s (saltou etapas — ver abaixo)\n' "$C_YEL" "$C_RST" "$gate" "$dt"
  else
    printf '  %sFAIL%s  %-14s %s\n' "$C_RED" "$C_RST" "$gate" "$dt"
  fi
done
printf '  %s-----------------------------------------------%s\n' "$C_BLD" "$C_RST"
printf '  tempo total: %ss\n' "$(( end_all - start_all ))"

# REDECLARAÇÃO DOS SALTOS, de TODOS os gates corridos — também dos que falharam: um vermelho não
# torna verificado o que não correu. A mesma etapa declarada duas vezes (um neto que a regista e
# o filho que a reabsorve, como o package.sh faz ao sbom.sh) conta uma vez.
saltos="$(for gate in "${GATES[@]}"; do
  [ -f "$SKIP_DIR/$gate.tsv" ] || continue
  awk -F'\t' -v g="$gate" '$1 != "" {print g "\t" $0}' "$SKIP_DIR/$gate.tsv"
done | awk '!visto[$0]++')"
n_saltos=0
[ -n "$saltos" ] && n_saltos="$(printf '%s\n' "$saltos" | wc -l | tr -d ' ')"
if [ "$n_saltos" -eq 0 ]; then
  printf '   AOS_SKIPPED_STEPS none\n'
else
  printf '   %sAOS_SKIPPED_STEPS %s etapa(s) NÃO verificada(s) nesta execução:%s\n' "$C_YEL" "$n_saltos" "$C_RST"
  while IFS=$'\t' read -r g etapa motivo garantia; do
    printf '   %sAOS_SKIPPED_STEP  [%s] %s (motivo: %s) -> POR VERIFICAR: %s%s\n' \
      "$C_YEL" "$g" "$etapa" "$motivo" "$garantia" "$C_RST"
  done <<< "$saltos"
fi

if [ "$overall" -ne 0 ]; then
  printf '%s  RESULTADO: PIPELINE VERMELHO (fail-closed)%s\n' "$C_RED$C_BLD" "$C_RST"
elif [ "$n_saltos" -gt 0 ]; then
  printf '%s  RESULTADO: VERDE PARCIAL — nenhum gate falhou, mas %s etapa(s) NÃO correram (acima).%s\n' "$C_YEL$C_BLD" "$n_saltos" "$C_RST"
  printf '%s           Isto NÃO é «todos os gates verdes»; não o cite como prova de pipeline verde.%s\n' "$C_YEL" "$C_RST"
  overall=3
else
  printf '%s  RESULTADO: TODOS OS GATES VERDES%s\n' "$C_GRN$C_BLD" "$C_RST"
fi
exit "$overall"
