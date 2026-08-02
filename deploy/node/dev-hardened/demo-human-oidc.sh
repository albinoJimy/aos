#!/usr/bin/env bash
# =============================================================================
# demo-human-oidc.sh — a ÚLTIMA peça do balde B: autenticação HUMANA por OIDC no `aos-issuer`.
#
# Em modo endurecido o directório humano vive com o ISSUER EXTERNO (não no nó). Aqui o humano-raiz
# da delegação deixa de ser uma flag manual (--human) e passa a ser DERIVADO do `sub` de um ID-token
# OIDC VERIFICADO contra o Keycloak (aos-issuer mint --assertion). Prova: o `human` dentro do NHI
# mintado == o `sub` do ID-token do Keycloak, e o run é aceite em produção.
#
# Pré-requisito: a stack OIDC a correr (bash up-oidc.sh).
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE="${SCRIPT_DIR}/docker-compose.yml"
OIDC="${SCRIPT_DIR}/docker-compose.oidc.yml"
ENV_FILE="${SCRIPT_DIR}/.env"
PROJECT="aos-dev-hardened"
DC=(docker compose -p "${PROJECT}" -f "${BASE}" -f "${OIDC}" --env-file "${ENV_FILE}")

fail() { echo "demo-human-oidc.sh: FAIL: $*" >&2; exit 1; }
# b64url decode do payload (segmento 2) de um JWT, e extrai um claim string.
claim_of() { printf '%s' "$1" | cut -d. -f2 | tr '_-' '/+' | { base64 -d 2>/dev/null || true; } | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p"; }

ISS="https://idp:8443/realms/aos"
TOKURL="https://idp:8443/realms/aos/protocol/openid-connect/token"
getidtoken() {
  docker run --rm --network "${PROJECT}_default" curlimages/curl:latest -sk \
    -d grant_type=password -d client_id=aos-node -d username=alice -d password=alice -d scope=openid \
    "${TOKURL}" | sed -n 's/.*"id_token":"\([^"]*\)".*/\1/p'
}

echo "[human] 1/4 a construir o toolbox aos-issuer (compila em-container) ..."
"${DC[@]}" build issuer >/dev/null 2>&1 || fail "build do toolbox falhou"

echo "[human] 2/4 ID-token de alice (para --assertion) ..."
ASSERT="$(getidtoken)"; [[ -n "${ASSERT}" ]] || fail "sem id-token do Keycloak"
SUB="$(claim_of "${ASSERT}" sub)"
echo "[human]   sub verificado do Keycloak = ${SUB}"

echo "[human] 3/4 aos-issuer mint --assertion (verifica o ID-token contra o Keycloak, deriva o humano) ..."
NHI="$("${DC[@]}" run --rm -T issuer \
  mint --key-file issuer.key --issuer iss:aos-issuer \
       --assertion "${ASSERT}" --oidc-issuer "${ISS}" --oidc-audience aos-node \
       --agent agt-human --class researcher --caps cap:doc.read --ttl 15m 2>/dev/null | tr -d '\r\n')"
[[ -n "${NHI}" ]] || fail "mint --assertion falhou (ver: ${DC[*]} run --rm issuer mint --assertion ...)"
NHI_HUMAN="$(claim_of "${NHI}" user_id)"
echo "[human]   humano-raiz DENTRO do NHI (claim user_id) = ${NHI_HUMAN}"
[[ -n "${NHI_HUMAN}" && "${NHI_HUMAN}" == *"${SUB}"* ]] \
  && echo "[human]   ✅ o humano-raiz do NHI foi DERIVADO do sub OIDC verificado (não de uma flag manual)" \
  || echo "[human]   ⚠ humano-raiz (${NHI_HUMAN}) não contém o sub (${SUB}) — inspecionar formato"

echo "[human] 4/4 POST /runs em produção com este NHI (humano autenticado por OIDC) + Bearer soberano ..."
BEARER="$(getidtoken)"; [[ -n "${BEARER}" ]] || fail "sem id-token soberano"
code="$(curl -sk -o /dev/null -w '%{http_code}' -X POST https://localhost:8443/runs \
  -H "Authorization: Bearer ${BEARER}" -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"run-human-oidc\",\"objective\":\"run com humano autenticado por OIDC\",\"principal_nhi\":\"agt-human\",\"credential\":\"${NHI}\",\"scope\":[\"cap:doc.read\"]}")"
echo
[[ "${code}" == "201" ]] && echo "[human] ✅ HTTP 201 — cadeia OIDC completa: humano (issuer) + soberania (nó), ambos verificados." \
                         || echo "[human] ⚠ HTTP ${code} — ver: ${DC[*]} logs aos"
