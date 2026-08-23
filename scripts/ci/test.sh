#!/usr/bin/env bash
# test.sh — GATE 3 (Unit/Integração). 'go test ./... -race -covermode=atomic
# -coverprofile' em CADA módulo (CGO+gcc). Reporta cobertura de todos, emite o
# relatório MÁQUINA-LEGÍVEL (LCOV, AOS-109) e aplica o GATE de cobertura
# GENERALIZADO (>= COVERAGE_MIN % nos COVERAGE_GATED_MODULES). Fail-closed.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

COVER_DIR="$(mktemp -d)"
trap 'rm -rf "$COVER_DIR"' EXIT

rc=0
declare -A COVER

log_gate "test (-race -covermode=atomic) por módulo"
while IFS= read -r mod; do
  log_step "test $mod"
  prof="$COVER_DIR/$(echo "$mod" | tr '/' '_').out"
  if ( cd "$REPO_ROOT/$mod" && go test ./... -race -covermode=atomic -coverprofile="$prof" ); then
    if [ -f "$prof" ]; then
      pct="$( cd "$REPO_ROOT/$mod" && go tool cover -func="$prof" | awk '/^total:/{print $NF}' )"
    else
      pct="n/a"
    fi
    COVER["$mod"]="$pct"
    log_ok "$mod verde (cobertura ${pct})"
  else
    COVER["$mod"]="FALHOU"
    log_fail "$mod testes vermelhos"
    rc=1
  fi
done < <(discover_modules)

# --- Relatório de cobertura ---------------------------------------------------
log_gate "test · relatório de cobertura"
for mod in $(discover_modules); do
  printf '   %-40s %s\n' "$mod" "${COVER[$mod]:-n/a}"
done

# --- Relatório MÁQUINA-LEGÍVEL (LCOV) — AOS-109 AC1 --------------------------
# Emite coverage/lcov.info a partir dos coverprofiles já gerados, via o conversor
# cov2lcov (Go stdlib puro, determinista). Fail-closed: se o artefacto não puder
# ser produzido, o gate fica vermelho (o AC exige cobertura máquina-legível).
log_gate "test · relatório de cobertura máquina-legível (LCOV)"
if ! emit_lcov "$COVER_DIR" "$COVERAGE_LCOV_OUT"; then
  rc=1
fi

# --- GATE de cobertura GENERALIZADO (>= COVERAGE_MIN) — AOS-109 AC4 -----------
# Generaliza o piso do kernel (AOS-010) para um limiar CONFIGURÁVEL aplicado a um
# conjunto de módulos. Usa o MESMO predicado (coverage_meets_min) que o self-test
# exercita. Uma descida abaixo do limiar sai != 0 (bloqueia o merge).
log_gate "test · gate de cobertura generalizado (>= ${COVERAGE_MIN}%)"
# INVENTÁRIO dos limiares em vigor (AOS-199): a linha AOS_GATE_THRESHOLD é emitida aqui —
# no gate que CONSOME KERNEL_COVERAGE_MIN/COVERAGE_MIN — e não no `source lib.sh`. Emitida
# no source só quando houvesse override, o marcador prometia inventário e não o dava: numa
# corrida normal um recolector que lhe fizesse grep não via nenhum dos limiares globais.
gate_threshold_report
for gmod in "${COVERAGE_GATED_MODULES[@]}"; do
  pct="${COVER[$gmod]:-}"
  if coverage_meets_min "$pct" "$COVERAGE_MIN"; then
    log_ok "$gmod cobertura ${pct} >= ${COVERAGE_MIN}%"
  else
    log_fail "LIMIAR NÃO ATINGIDO: $gmod cobertura ${pct:-n/a} < ${COVERAGE_MIN}% (ou não-mensurável) — configuração válida, foi o CÓDIGO que ficou abaixo"
    rc=1
  fi
done


# --- TODO O MÓDULO DO KERNEL TEM DE ESTAR SOB O LIMIAR ---------------------------------------
#
# O DEFEITO QUE FECHA: o `packages/kernel/agent-runtime` — metade do kernel — nunca esteve em
# COVERAGE_GATED_MODULES, apesar de CINCO documentos declararem «cobertura do kernel >= 80%». Era
# descoberto e medido, e simplesmente não era comparado. Estava a 93,5%, o que é o ponto: esse
# verde saía igual com o gate desligado.
#
# A disciplina de «acrescentar em dois sítios» — criar o módulo e lembrar de o listar aqui — já
# falhou uma vez. Não é uma disciplina que se possa confiar à memória de quem vier a seguir.
log_gate "test · cobertura: todo o kernel esta sob o limiar"
kfaltam=0
while IFS= read -r kmod; do
  [ -n "$kmod" ] || continue
  encontrado=0
  for gmod in "${COVERAGE_GATED_MODULES[@]}"; do
    [ "$gmod" = "$kmod" ] && encontrado=1 && break
  done
  if [ "$encontrado" -eq 0 ]; then
    log_fail "$kmod e um modulo do KERNEL e NAO esta em COVERAGE_GATED_MODULES — os documentos prometem «cobertura do kernel >= 80%» e este modulo nao e comparado com limiar nenhum"
    rc=1
    kfaltam=$((kfaltam + 1))
  fi
done < <(cd "$REPO_ROOT" && find packages/kernel -name go.mod -printf '%h\n' 2>/dev/null | sort)
# CONTROLO ANTI-VACUIDADE: se o `find` não encontrar módulo nenhum, este bloco passa por não ter
# feito nada. Um gate que não encontra o que verifica tem de gritar.
kencontrados="$( cd "$REPO_ROOT" && find packages/kernel -name go.mod 2>/dev/null | wc -l )"
if [ "$kencontrados" -eq 0 ]; then
  log_fail "nenhum go.mod encontrado sob packages/kernel — a verificacao nao correu"
  rc=1
elif [ "$kfaltam" -eq 0 ]; then
  log_ok "cobertura: os $kencontrados modulo(s) do kernel estao todos sob o limiar"
fi
[ "$rc" -eq 0 ] && log_ok "test: verde" || log_fail "test: vermelho"
exit "$rc"
