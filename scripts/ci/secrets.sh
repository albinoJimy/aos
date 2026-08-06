#!/usr/bin/env bash
# secrets.sh — GATE de segredos. Um scan limpo é PRÉ-CONDIÇÃO de merge
# (specs/01 §4, cruza com a DoD). Zero-dep: grep sobre ficheiros RASTREADOS pelo
# git (não toca em .git/caches/ficheiros ignorados). Fail-closed: qualquer
# material sensível bloqueia. Reproduz localmente o invariante que a CI também
# cobre (gitleaks), para que 'make ci' seja a fonte de verdade completa.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$REPO_ROOT"

log_gate "secrets · material sensível em ficheiros rastreados"
rc=0

# 1) Blocos de chave privada PEM (alta confiança). Inclui _test/testdata: uma
#    chave privada real committada é sempre proibida (os testes do AOS usam pares
#    EFÉMEROS gerados em runtime, nunca material committado).
pem="$( git ls-files -z | xargs -0 grep -lE 'BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY' 2>/dev/null || true )"
if [ -n "$pem" ]; then
  log_fail "bloco de chave privada PEM em ficheiro(s) rastreado(s):"
  printf '%s\n' "$pem" | norm_path | sed 's/^/       /' >&2
  rc=1
fi

# 2) Ficheiros de chave/segredo rastreados por extensão (nunca devem ser
#    committados — o .gitignore já os ignora; isto apanha um 'git add -f').
#    NOTA: '*.pem' NÃO está na lista de propósito — .pem serve também para certs/
#    cadeias PÚBLICAS (ex.: raízes FIDO em fido-roots.pem), que são legítimas. Um
#    .pem com CHAVE PRIVADA é sempre apanhado pela verificação de conteúdo (1) acima
#    ('BEGIN ... PRIVATE KEY'), pelo que nenhuma chave privada escapa por esta omissão.
keyfiles="$( git ls-files -- '*.key' '*.p12' '*.pfx' '*.pgp' '.env' '**/.env' 2>/dev/null || true )"
if [ -n "$keyfiles" ]; then
  log_fail "ficheiro de chave/segredo rastreado (não deve ser committado):"
  printf '%s\n' "$keyfiles" | sed 's/^/       /' >&2
  rc=1
fi

[ "$rc" -eq 0 ] && log_ok "secrets: verde (sem material sensível rastreado)" || log_fail "secrets: vermelho"
exit "$rc"
