#!/usr/bin/env bash
# build.sh — GATE 1 (Build). 'go build ./...' em CADA módulo Go. Fail-closed.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

log_gate "build (go build ./... por módulo)"
rc=0
while IFS= read -r mod; do
  log_step "build $mod"
  if ( cd "$REPO_ROOT/$mod" && go build ./... ); then
    log_ok "$mod compila"
  else
    log_fail "$mod NÃO compila"
    rc=1
  fi
done < <(discover_modules)

[ "$rc" -eq 0 ] && log_ok "build: todos os módulos compilam" || log_fail "build: houve módulos a falhar"
exit "$rc"
