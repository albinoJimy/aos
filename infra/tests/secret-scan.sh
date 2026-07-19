#!/usr/bin/env bash
# =============================================================================
# SCAN DE SEGREDOS ao código IaC e ao estado (Testes Requeridos AOS-098, ADR-006).
# =============================================================================
# Gate AUTOMATIZADO e OFFLINE (só grep — não precisa de Docker nem de provider).
# Falha (exit 1) se encontrar material de segredo em texto claro no código versionado
# de infra/ (ficheiros .tf/.hcl/.tfvars) ou num tfstate local acidentalmente commitado.
#
# Princípio (ADR-006): nenhum segredo em texto claro no código nem no estado. As
# credenciais do store de estado vêm de env vars; o root token do Vault -dev é gerado
# em runtime e exposto só como output sensível.
#
# Uso:
#   bash infra/tests/secret-scan.sh
# =============================================================================
set -euo pipefail

INFRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$INFRA_DIR"

echo ">> Scan de segredos em: $INFRA_DIR"

# Ficheiros a inspeccionar: HCL/IaC versionado + qualquer tfstate que NÃO devia existir aqui.
mapfile -t FILES < <(git -C "$INFRA_DIR" ls-files -- '*.tf' '*.tfvars' '*.hcl' '*.tfstate' '*.tfstate.*' 2>/dev/null || true)
if [ "${#FILES[@]}" -eq 0 ]; then
  # Fallback sem git: varre a árvore de infra/.
  mapfile -t FILES < <(find . -type f \( -name '*.tf' -o -name '*.tfvars' -o -name '*.hcl' -o -name '*.tfstate' -o -name '*.tfstate.*' \))
fi

FAIL=0
report() { echo "!! SEGREDO POTENCIAL — $1" >&2; FAIL=1; }

# 1) tfstate versionado é proibido (contém outputs sensíveis desserializados).
for f in "${FILES[@]}"; do
  case "$f" in
    *.tfstate|*.tfstate.*) report "ficheiro de estado versionado: $f" ;;
  esac
done

# 2) Padrões de alto sinal de material de segredo em texto claro.
#    Usamos ERE; as descrições de variáveis que só MENCIONAM 'segredo/token/password'
#    não fazem match (exigimos atribuição a um literal não-vazio).
PATTERNS=(
  'AKIA[0-9A-Z]{16}'                                   # AWS Access Key ID
  '-----BEGIN [A-Z ]*PRIVATE KEY-----'                 # chave privada PEM
  '(aws_secret_access_key|secret_key|secret_access_key)[[:space:]]*=[[:space:]]*"[^"$][^"]+"'
  '(password|passwd|passphrase)[[:space:]]*=[[:space:]]*"[^"$][^"]+"'
  '(api[_-]?key|access[_-]?token|auth[_-]?token|bearer)[[:space:]]*=[[:space:]]*"[^"$][^"]+"'
  'xox[baprs]-[0-9A-Za-z-]{10,}'                       # token Slack
  'gh[pousr]_[0-9A-Za-z]{20,}'                          # token GitHub
)

for f in "${FILES[@]}"; do
  [ -f "$f" ] || continue
  for p in "${PATTERNS[@]}"; do
    if grep -nEI "$p" "$f" >/dev/null 2>&1; then
      report "padrão '$p' em $f"
      grep -nEI "$p" "$f" >&2 || true
    fi
  done
done

if [ "$FAIL" -ne 0 ]; then
  echo "!! SCAN DE SEGREDOS FALHOU: remove os segredos do código/estado (ADR-006)." >&2
  exit 1
fi

echo ">> OK: nenhum segredo em texto claro no código IaC nem estado versionado (ADR-006)."
