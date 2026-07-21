#!/usr/bin/env bash
# cache-prime.sh — PRIME determinista do módulo-cache para builds OFFLINE (AOS-148).
#
# Contexto (medido no build-spike AOS-145): os gates correm offline (`GOPROXY=off`),
# mas isso só passa com o GOMODCACHE já quente — um runner FRIO falha. O projecto NÃO
# vendoriza (`vendor/` está no .gitignore por opção: supply-chain mínima via cache
# pinado, não árvores vendored duplicadas por módulo). Este script fecha esse buraco:
# popula o cache uma vez, de forma DETERMINISTA, a partir dos `go.sum` pinados, para que
# os gates offline sejam reproduzíveis num runner limpo.
#
# NÃO é um gate (não entra em ALL_GATES): é um passo de PREPARAÇÃO. Uso típico:
#   bash scripts/ci/cache-prime.sh          # uma vez (com rede)
#   GOPROXY=off bash scripts/ci/run.sh      # depois: pipeline offline reproduzível
#
# Determinismo: `go mod download` resolve EXACTAMENTE as versões pinadas em go.mod e
# VERIFICA os hashes contra go.sum (falha se divergirem). A única dependência externa do
# monorepo é `cedar-policy/cedar-go` (+ `golang.org/x/exp` indirect); tudo o mais é
# stdlib + módulos locais (replace por path). `go mod verify` confirma a integridade do
# que ficou em cache. Fail-closed: um download/verify que falhe aborta com exit != 0.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

log_gate "cache-prime · popular o módulo-cache a partir dos go.sum pinados (para GOPROXY=off reproduzível)"

rc=0
while IFS= read -r mod; do
  log_step "go mod download + verify · $mod"
  if ! ( cd "$REPO_ROOT/$mod" && go mod download && go mod verify >/dev/null ); then
    log_fail "$mod: download/verify falhou (go.sum divergente ou rede indisponível)"
    rc=1
  fi
done < <(discover_modules)

if [ "$rc" -eq 0 ]; then
  log_ok "cache-prime: cache populado e verificado; os gates podem agora correr com GOPROXY=off num runner frio"
else
  log_fail "cache-prime: houve módulos a falhar o download/verify"
fi
exit "$rc"
