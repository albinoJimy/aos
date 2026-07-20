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
for gmod in "${COVERAGE_GATED_MODULES[@]}"; do
  pct="${COVER[$gmod]:-}"
  if coverage_meets_min "$pct" "$COVERAGE_MIN"; then
    log_ok "$gmod cobertura ${pct} >= ${COVERAGE_MIN}%"
  else
    log_fail "$gmod cobertura ${pct:-n/a} < ${COVERAGE_MIN}% (ou não-mensurável)"
    rc=1
  fi
done

[ "$rc" -eq 0 ] && log_ok "test: verde" || log_fail "test: vermelho"
exit "$rc"
