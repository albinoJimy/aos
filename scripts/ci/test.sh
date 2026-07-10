#!/usr/bin/env bash
# test.sh — GATE 3 (Unit/Integração). 'go test ./... -race -covermode=atomic
# -coverprofile' em CADA módulo (CGO+gcc). Reporta cobertura de todos e aplica o
# GATE de cobertura do kernel (>= KERNEL_COVERAGE_MIN %). Fail-closed.
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

# --- GATE de cobertura do kernel ---------------------------------------------
log_gate "test · gate de cobertura do kernel (>= ${KERNEL_COVERAGE_MIN}%)"
for kmod in "${KERNEL_MODULES[@]}"; do
  pct="${COVER[$kmod]:-}"
  num="${pct%\%}"
  if [ -z "$num" ] || [ "$pct" = "FALHOU" ] || [ "$pct" = "n/a" ]; then
    log_fail "kernel $kmod sem cobertura mensurável ($pct)"
    rc=1
    continue
  fi
  if awk "BEGIN{exit !($num >= $KERNEL_COVERAGE_MIN)}"; then
    log_ok "kernel $kmod cobertura ${pct} >= ${KERNEL_COVERAGE_MIN}%"
  else
    log_fail "kernel $kmod cobertura ${pct} < ${KERNEL_COVERAGE_MIN}%"
    rc=1
  fi
done

[ "$rc" -eq 0 ] && log_ok "test: verde" || log_fail "test: vermelho"
exit "$rc"
