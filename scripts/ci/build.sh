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

# O go.work da raiz (AOS-387). Os builds acima correm com GOWORK=off (lib.sh), como a CI
# sempre correu; estes dois passos são a única coisa que impede o workspace de apodrecer em
# silêncio: (1) o conjunto `use` é exactamente o dos go.mod de packages/ — um módulo novo sem
# `use` avermelha aqui —, e (2) o workspace compila inteiro, offline. Corre DEPOIS do ciclo
# por-módulo, que aquece o cache de cada módulo; o grafo do workspace aquece-o o próprio
# `compilar` quando o run tem rede, ou o cache-prime quando não tem.
if bash "$CI_DIR/gowork.sh" verificar; then
  bash "$CI_DIR/gowork.sh" compilar || rc=1
else
  rc=1
fi

[ "$rc" -eq 0 ] && log_ok "build: todos os módulos compilam" || log_fail "build: houve módulos a falhar"
exit "$rc"
