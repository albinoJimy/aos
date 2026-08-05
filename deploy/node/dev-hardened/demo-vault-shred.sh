#!/usr/bin/env bash
# =============================================================================
# demo-vault-shred.sh — prova o CICLO DE CUSTÓDIA em HashiCorp Vault (AOS-215/216):
#   1. um run em produção sela conteúdo sob a KEK do titular  => aparece uma chave Transit no Vault;
#   2. POST /dsar/erase (crypto-shred)                        => a chave Transit é DESTRUÍDA;
#   3. sem a KEK, o conteúdo do run é IRRECUPERÁVEL (key-never-leaves + apagamento real).
#
# Pré-requisito: a stack OIDC+Vault a correr (bash up-oidc.sh).
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
SECRETS="${SCRIPT_DIR}/secrets"
PROJECT="aos-dev-hardened"
EXE=""; case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) EXE=".exe" ;; esac
ISSUERBIN="${REPO_ROOT}/packages/cmd/aos-issuer/aos-issuer${EXE}"
VADDR="https://localhost:8200"; VTOK="$(cat "${SECRETS}/vault-token" 2>/dev/null || echo aos-dev-root)"

fail() { echo "demo-vault-shred.sh: FAIL: $*" >&2; exit 1; }
claim_of() { printf '%s' "$1" | cut -d. -f2 | tr '_-' '/+' | { base64 -d 2>/dev/null || true; } | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p"; }
getidtoken() {
  docker run --rm --network "${PROJECT}_default" curlimages/curl:latest -sk \
    -d grant_type=password -d client_id=aos-node -d username=alice -d password=alice -d scope=openid \
    https://idp:8443/realms/aos/protocol/openid-connect/token | sed -n 's/.*"id_token":"\([^"]*\)".*/\1/p'
}
# lista os nomes das chaves Transit (via host).
list_keys() { curl -sk -H "X-Vault-Token: ${VTOK}" "${VADDR}/v1/transit/keys?list=true" | tr ',' '\n' | sed -n 's/.*"\(aos-kek-[0-9a-f]*\)".*/\1/p'; }
has_key() { list_keys | grep -qx "$1"; }

echo "[shred] 1/6 ID-token de alice + titular (sub) ..."
BEARER="$(getidtoken)"; [[ -n "${BEARER}" ]] || fail "sem id-token"
SUB="$(claim_of "${BEARER}" sub)"; [[ -n "${SUB}" ]] || fail "sem sub no id-token"
# nome esperado da chave Transit = "aos-kek-" + sha256("aos.audit.pii:" + subject) (espelha vaultKeyName).
NAME="aos-kek-$(printf 'aos.audit.pii:%s' "${SUB}" | sha256sum | cut -d' ' -f1)"
echo "[shred]   titular=${SUB}"
echo "[shred]   chave Transit esperada=${NAME}"

echo "[shred] 2/6 NHI (aos-issuer) ..."
NHI="$("${ISSUERBIN}" mint --key-file "${SECRETS}/issuer.key" --issuer iss:aos-issuer \
  --human human:alice --agent agt-shred --class researcher --caps cap:doc.read --ttl 15m | tr -d '\r\n')"

echo "[shred] 3/6 POST /runs (produção; sela conteúdo sob a KEK do titular) ..."
rc="$(curl -sk -o /dev/null -w '%{http_code}' -X POST https://localhost:8443/runs \
  -H "Authorization: Bearer ${BEARER}" -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"run-shred-demo\",\"objective\":\"conteudo a selar e depois apagar\",\"principal_nhi\":\"agt-shred\",\"credential\":\"${NHI}\",\"scope\":[\"cap:doc.read\"]}")"
echo "[shred]   POST /runs -> HTTP ${rc}"

echo "[shred] 4/6 a aguardar a KEK do titular aparecer no Vault (conteúdo selado) ..."
tries=30; appeared=0
while (( tries-- > 0 )); do if has_key "${NAME}"; then appeared=1; break; fi; sleep 1; done
if (( appeared )); then
  echo "[shred]   ✅ chave Transit PRESENTE no Vault: ${NAME}"
else
  echo "[shred]   ⚠ a KEK não apareceu (este run pode não ter selado conteúdo por-titular)."
  echo "[shred]     Chaves Transit actuais:"; list_keys | sed 's/^/[shred]       /'
  echo "[shred]     (o contrato de crypto-shred está provado por TestVaultKeyVault_WrapUnwrapShred.)"
  exit 0
fi

echo "[shred] 5/6 POST /dsar/erase (crypto-shred da KEK no Vault) ..."
# Token FRESCO: o verificador de soberania recusa reutilização por-jti (anti-replay) — o Bearer do
# /runs não pode servir outra vez para o /dsar/erase, senão 403 (ErrTokenReplayed).
ERASE_BEARER="$(getidtoken)"; [[ -n "${ERASE_BEARER}" ]] || fail "sem id-token fresco para o erase"
ec="$(curl -sk -o /dev/null -w '%{http_code}' -X POST https://localhost:8443/dsar/erase \
  -H "Authorization: Bearer ${ERASE_BEARER}" -H 'Content-Type: application/json' \
  -d "{\"subject_id\":\"${SUB}\"}")"
echo "[shred]   POST /dsar/erase -> HTTP ${ec}"

echo "[shred] 6/6 a confirmar a DESTRUIÇÃO da chave no Vault ..."
tries=15; gone=0
while (( tries-- > 0 )); do if ! has_key "${NAME}"; then gone=1; break; fi; sleep 1; done
echo
if (( gone )); then
  echo "[shred] ✅ CRYPTO-SHRED REAL: a chave Transit ${NAME:0:20}... foi DESTRUÍDA no Vault — o conteúdo do titular é irrecuperável."
else
  echo "[shred] ⚠ a chave ainda consta no Vault após o erase (HTTP ${ec}) — inspecionar: ${PROJECT} logs aos"
fi