#!/usr/bin/env bash
# =============================================================================
# up-oidc.sh — promove a stack a OIDC REAL (Keycloak) + AOS_MODE=production.
#
# Sobre a base (up.sh): gera uma CA de dev + cert do IdP, sobe o Keycloak, e liga a credencial
# forte de soberania no nó. Depois PROVA a postura: obtém um ID-token REAL do Keycloak e submete
# um run em produção (Authorization: Bearer <id-token verificado>, board vindo das claims).
#
# Requisitos: docker, openssl, curl e o issuer/CLI do repo (idem up.sh).
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
SECRETS="${SCRIPT_DIR}/secrets"
ENV_FILE="${SCRIPT_DIR}/.env"
BASE="${SCRIPT_DIR}/docker-compose.yml"
OIDC="${SCRIPT_DIR}/docker-compose.oidc.yml"
PROJECT="aos-dev-hardened"

EXE=""
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) EXE=".exe" ;; esac
ISSUERBIN="${REPO_ROOT}/packages/cmd/aos-issuer/aos-issuer${EXE}"

fail() { echo "up-oidc.sh: FAIL: $*" >&2; exit 1; }
hostpath() { case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) cygpath -w "$1" ;; *) printf '%s' "$1" ;; esac; }

# --- 0. garantir a base (chaves/roster/.env) ---------------------------------
[[ -s "${ENV_FILE}" && -s "${SECRETS}/issuer.key" ]] || { echo "[oidc] base ausente — a correr up.sh ..."; bash "${SCRIPT_DIR}/up.sh" >/dev/null; }

# --- 1. CA de dev + cert do IdP (SAN idp/localhost) --------------------------
mkdir -p "${SECRETS}/idp-tls"
if [[ ! -s "${SECRETS}/ca.crt" ]]; then
  echo "[oidc] 1/5 a gerar CA de dev ..."
  openssl req -x509 -nodes -newkey rsa:2048 -days 3650 \
    -keyout "${SECRETS}/ca.key" -out "${SECRETS}/ca.crt" -subj "//CN=aos-dev-ca" >/dev/null 2>&1 \
    || fail "openssl CA falhou"
fi
if [[ ! -s "${SECRETS}/idp-tls/idp.crt" ]]; then
  echo "[oidc] 1/5 a gerar cert do IdP (SAN: idp, localhost, 127.0.0.1) ..."
  EXT="${SECRETS}/idp-tls/idp.ext"
  printf 'subjectAltName=DNS:idp,DNS:localhost,IP:127.0.0.1\n' > "${EXT}"
  openssl req -nodes -newkey rsa:2048 \
    -keyout "${SECRETS}/idp-tls/idp.key" -out "${SECRETS}/idp-tls/idp.csr" -subj "//CN=idp" >/dev/null 2>&1 \
    || fail "openssl CSR do IdP falhou"
  openssl x509 -req -in "${SECRETS}/idp-tls/idp.csr" -CA "${SECRETS}/ca.crt" -CAkey "${SECRETS}/ca.key" \
    -CAcreateserial -out "${SECRETS}/idp-tls/idp.crt" -days 825 -extfile "${EXT}" >/dev/null 2>&1 \
    || fail "openssl assinatura do cert do IdP falhou"
  chmod 644 "${SECRETS}/idp-tls/idp.crt" "${SECRETS}/idp-tls/idp.key" "${SECRETS}/ca.crt"
fi

# --- 2. sobe base + override (Keycloak + nó em produção) ---------------------
echo "[oidc] 2/5 docker compose up (base + oidc) ..."
docker compose -p "${PROJECT}" -f "${BASE}" -f "${OIDC}" --env-file "${ENV_FILE}" up -d

echo "[oidc] a aguardar o nó ficar healthy ..."
CID="$(docker compose -p "${PROJECT}" -f "${BASE}" -f "${OIDC}" ps -q aos)"
until [[ "$(docker inspect -f '{{.State.Health.Status}}' "${CID}" 2>/dev/null)" == "healthy" ]]; do
  st="$(docker inspect -f '{{.State.Status}}' "${CID}" 2>/dev/null || echo '?')"
  [[ "${st}" == "running" || "${st}" == "created" || "${st}" == "restarting" ]] \
    || fail "nó em '${st}' — ver: docker compose -p ${PROJECT} -f ${BASE} -f ${OIDC} logs aos"
  sleep 2
done

echo "[oidc] a aguardar o Keycloak (discovery) ..."
DISCO="https://localhost:9443/realms/aos/.well-known/openid-configuration"
tries=120
until curl -sk "${DISCO}" | grep -q '"issuer"'; do
  (( tries-- > 0 )) || fail "Keycloak não respondeu ao discovery a tempo (docker compose logs idp)"
  sleep 2
done
echo "[oidc]   discovery OK: $(curl -sk "${DISCO}" | sed -n 's/.*"issuer":"\([^"]*\)".*/\1/p')"

# --- 3. ID-token REAL do Keycloak (in-network; iss = https://idp:8443/...) ---
echo "[oidc] 3/5 a obter ID-token do Keycloak (grant password, user alice) ..."
# -k ao OBTER o token: a integridade do id-token vem da sua ASSINATURA JWS (que o NÓ verifica
# contra o JWKS do Keycloak), não do TLS deste fetch — logo dispensa montar a CA neste helper.
TOKJSON="$(docker run --rm --network "${PROJECT}_default" curlimages/curl:latest \
  -sk \
  -d grant_type=password -d client_id=aos-node -d username=alice -d password=alice -d scope=openid \
  https://idp:8443/realms/aos/protocol/openid-connect/token)"
ID_TOKEN="$(printf '%s' "${TOKJSON}" | sed -n 's/.*"id_token":"\([^"]*\)".*/\1/p')"
[[ -n "${ID_TOKEN}" ]] || fail "sem id_token na resposta do Keycloak: ${TOKJSON:0:200}"
echo "[oidc]   id_token obtido (${#ID_TOKEN} chars); board no payload: $(printf '%s' "${ID_TOKEN}" | cut -d. -f2 | tr '_-' '/+' | { base64 -d 2>/dev/null || true; } | sed -n 's/.*\("board":"[^"]*"\).*/\1/p')"

# --- 4. NHI do issuer externo (delegação manual do humano) -------------------
echo "[oidc] 4/5 a mintar NHI (aos-issuer) ..."
NHI="$("${ISSUERBIN}" mint --key-file "${SECRETS}/issuer.key" --issuer iss:aos-issuer \
  --human human:alice --agent agt-oidc --class researcher --caps cap:doc.read --ttl 15m | tr -d '\r\n')"
[[ -n "${NHI}" ]] || fail "mint do NHI falhou"

# --- 5. PROVA: submeter run em PRODUÇÃO com Bearer OIDC verificado -----------
echo "[oidc] 5/5 POST /runs (produção; Authorization: Bearer <id-token OIDC>) via edge TLS ..."
code="$(curl -sk -o /dev/null -w '%{http_code}' -X POST https://localhost:8443/runs \
  -H "Authorization: Bearer ${ID_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"run-oidc-demo\",\"objective\":\"ler um documento (via OIDC)\",\"principal_nhi\":\"agt-oidc\",\"credential\":\"${NHI}\",\"scope\":[\"cap:doc.read\"]}")"
echo "[oidc]   POST /runs -> HTTP ${code}"

# Prova NEGATIVA: sem Bearer, o read-path soberano em produção RECUSA (403).
neg="$(curl -sk -o /dev/null -w '%{http_code}' -X POST https://localhost:8443/runs \
  -H 'Content-Type: application/json' -H 'X-Aos-Board: board:demo' \
  -d "{\"run_id\":\"run-oidc-neg\",\"objective\":\"x\",\"principal_nhi\":\"agt-oidc\",\"credential\":\"${NHI}\"}")"
echo "[oidc]   PROVA NEGATIVA (X-Aos-Board forjado, sem Bearer) -> HTTP ${neg} (esperado 403)"

echo
[[ "${code}" == "201" ]] && echo "[oidc] ✅ produção + OIDC real: run aceite com credencial forte verificada." \
                          || echo "[oidc] ⚠ run não aceite (HTTP ${code}) — ver: docker compose -p ${PROJECT} -f ${BASE} -f ${OIDC} logs aos"
echo "[oidc] banner (postura de produção + soberania forte):"
docker compose -p "${PROJECT}" -f "${BASE}" -f "${OIDC}" logs aos 2>&1 | grep -iE 'producao|production|ENDURECID|soberania|OIDC|credencial forte|MODO' | tail -8
