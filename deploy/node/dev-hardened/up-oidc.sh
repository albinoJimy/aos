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
  # As extensões de CA (basicConstraints CA:TRUE + keyUsage keyCertSign) são OBRIGATÓRIAS: o Go (nó)
  # e o Java (Keycloak) toleram a sua ausência, mas o openssl/Python (httpx do LiteLLM, usado no SSO
  # backend) RECUSA uma CA sem elas ("CA cert does not include key usage extension").
  openssl req -x509 -nodes -newkey rsa:2048 -days 3650 \
    -keyout "${SECRETS}/ca.key" -out "${SECRETS}/ca.crt" -subj "//CN=aos-dev-ca" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1 \
    || fail "openssl CA falhou"
fi
if [[ ! -s "${SECRETS}/idp-tls/idp.crt" ]]; then
  echo "[oidc] 1/5 a gerar cert do IdP (SAN: idp, localhost, 127.0.0.1) ..."
  EXT="${SECRETS}/idp-tls/idp.ext"
  printf 'subjectAltName=DNS:idp,DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' > "${EXT}"
  openssl req -nodes -newkey rsa:2048 \
    -keyout "${SECRETS}/idp-tls/idp.key" -out "${SECRETS}/idp-tls/idp.csr" -subj "//CN=idp" >/dev/null 2>&1 \
    || fail "openssl CSR do IdP falhou"
  openssl x509 -req -in "${SECRETS}/idp-tls/idp.csr" -CA "${SECRETS}/ca.crt" -CAkey "${SECRETS}/ca.key" \
    -CAcreateserial -out "${SECRETS}/idp-tls/idp.crt" -days 825 -extfile "${EXT}" >/dev/null 2>&1 \
    || fail "openssl assinatura do cert do IdP falhou"
  chmod 644 "${SECRETS}/idp-tls/idp.crt" "${SECRETS}/idp-tls/idp.key" "${SECRETS}/ca.crt"
fi

# Cert do COMPONENTE DE ATTESTATION (SAN: attestation) assinado pela MESMA CA de dev — o nó recusa
# http em claro para a attestation (veredito manipulável em trânsito), pelo que fala https; confia
# na CA via SSL_CERT_FILE. Mesmo molde do cert do IdP.
if [[ ! -s "${SECRETS}/attestation-tls/tls.crt" ]]; then
  echo "[oidc] 1b/5 a gerar cert do attestation (SAN: attestation, localhost) ..."
  mkdir -p "${SECRETS}/attestation-tls"
  AEXT="${SECRETS}/attestation-tls/tls.ext"
  printf 'subjectAltName=DNS:attestation,DNS:localhost\nextendedKeyUsage=serverAuth\n' > "${AEXT}"
  openssl req -nodes -newkey rsa:2048 \
    -keyout "${SECRETS}/attestation-tls/tls.key" -out "${SECRETS}/attestation-tls/tls.csr" -subj "//CN=attestation" >/dev/null 2>&1 \
    || fail "openssl CSR do attestation falhou"
  openssl x509 -req -in "${SECRETS}/attestation-tls/tls.csr" -CA "${SECRETS}/ca.crt" -CAkey "${SECRETS}/ca.key" \
    -CAcreateserial -out "${SECRETS}/attestation-tls/tls.crt" -days 825 -extfile "${AEXT}" >/dev/null 2>&1 \
    || fail "openssl assinatura do cert do attestation falhou"
  chmod 644 "${SECRETS}/attestation-tls/tls.crt" "${SECRETS}/attestation-tls/tls.key"
fi

# Token do Vault: o ficheiro tem de EXISTIR antes do `up` (o nó lê-o no arranque, 1x). Aqui é só
# um PLACEHOLDER — a secção 2b substitui-o pelo root token REAL do init do Vault persistente e
# recria o nó. Material de DEV; produção usaria AppRole/k8s-auth de curta duração.
if [[ ! -s "${SECRETS}/vault-token" ]]; then
  printf 'placeholder-ate-init' > "${SECRETS}/vault-token"
  chmod 644 "${SECRETS}/vault-token"   # UID 65532 non-root tem de LER o mount.
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

# --- 2a. Model gateway (LiteLLM): ficheiro de segredos das keys de provider --------------------
# As keys dos providers (Moonshot/Kimi, etc.) vivem AQUI (externo, git-ignored), não no nó nem no
# YAML. Cria um placeholder se não existir; o LiteLLM lê-o por env_file. Enche MOONSHOT_API_KEY
# com a tua key do Kimi para os runs completarem.
if [[ ! -f "${SECRETS}/model.env" ]]; then
  cat > "${SECRETS}/model.env" <<'EOF'
# Keys dos providers do LiteLLM (litellm/config.yaml lê-as via os.environ/<VAR>).
# Enche a(s) que usas; sem key o turno do modelo devolve erro do provider.
MOONSHOT_API_KEY=
# OPENAI_API_KEY=
# ANTHROPIC_API_KEY=

# --- LiteLLM UI/admin (login em http://localhost:4000/ui) ---
LITELLM_MASTER_KEY=sk-aos-litellm-master
UI_USERNAME=admin
UI_PASSWORD=aos-litellm-2026
DATABASE_URL=postgresql://keycloak:keycloak-dev-pass@postgres:5432/litellm
STORE_MODEL_IN_DB=True

# --- SSO OIDC via Keycloak (login humano da UI; "key para máquinas, SSO para humanos") ---
GENERIC_CLIENT_ID=litellm
GENERIC_CLIENT_SECRET=litellm-dev-secret
GENERIC_AUTHORIZATION_ENDPOINT=https://localhost:9443/realms/aos/protocol/openid-connect/auth
GENERIC_TOKEN_ENDPOINT=https://idp:8443/realms/aos/protocol/openid-connect/token
GENERIC_USERINFO_ENDPOINT=https://idp:8443/realms/aos/protocol/openid-connect/userinfo
GENERIC_SCOPE=openid email profile
PROXY_BASE_URL=http://localhost:4000
EOF
  echo "[oidc] criado ${SECRETS}/model.env — PÕE a tua MOONSHOT_API_KEY lá para o Kimi completar."
fi
# A master key que o NÓ apresenta ao LiteLLM (bearer). Deriva-a do model.env (idempotente).
grep -E '^LITELLM_MASTER_KEY=' "${SECRETS}/model.env" | head -1 | sed 's/^LITELLM_MASTER_KEY=//' | tr -d '\r\n' > "${SECRETS}/litellm-key"
chmod 644 "${SECRETS}/litellm-key"
# Bundle de CA COMBINADO para o LiteLLM (Python): CAs públicas do certifi (egress real p/ os
# providers) + a nossa CA de dev (SSO backend ao Keycloak em https://idp:8443). Só a CA de dev
# quebraria o TLS público.
if [[ ! -s "${SECRETS}/ca-bundle.crt" ]]; then
  docker run --rm --entrypoint python ghcr.io/berriai/litellm:main-stable \
    -c 'import certifi,sys; sys.stdout.write(open(certifi.where()).read())' > "${SECRETS}/ca-bundle.crt" 2>/dev/null || true
  cat "${SECRETS}/ca.crt" >> "${SECRETS}/ca-bundle.crt"
  chmod 644 "${SECRETS}/ca-bundle.crt"
fi

# --- 2b. Vault: init/unseal PERSISTENTE + motor Transit (idempotente) --------
# Storage `file` (NÃO -dev): o Transit e as KEKs SOBREVIVEM a restart. Custo: init/unseal reais.
# DEV usa 1 share / threshold 1 e guarda o material em secrets/vault-init.json (git-ignored);
# produção usaria AUTO-UNSEAL (KMS/HSM) + cerimónia Shamir multi-custodiante. A cada arranque:
# init-se-preciso -> unseal-se-selado -> enable-transit-idempotente -> sincroniza o token do nó.
echo "[oidc] a aguardar o Vault e a garantir init/unseal + Transit ..."
VADDR="http://localhost:8200"
INIT_FILE="${SECRETS}/vault-init.json"
seal_status() { curl -s "${VADDR}/v1/sys/seal-status"; }
tries=60
until [[ "$(curl -s -o /dev/null -w '%{http_code}' "${VADDR}/v1/sys/seal-status" 2>/dev/null)" == "200" ]]; do
  (( tries-- > 0 )) || fail "Vault não respondeu a tempo (docker compose logs vault)"
  sleep 1
done
if [[ "$(seal_status | grep -o '"initialized":[a-z]*' | cut -d: -f2)" != "true" ]]; then
  echo "[oidc]   Vault não inicializado — operator init (1 share / threshold 1) ..."
  curl -s -X POST "${VADDR}/v1/sys/init" -d '{"secret_shares":1,"secret_threshold":1}' > "${INIT_FILE}"
  chmod 600 "${INIT_FILE}"
fi
UNSEAL_KEY="$(grep -o '"keys_base64":\["[^"]*"' "${INIT_FILE}" 2>/dev/null | sed 's/.*\["//;s/"$//')"
ROOT_TOKEN="$(grep -o '"root_token":"[^"]*"' "${INIT_FILE}" 2>/dev/null | cut -d'"' -f4)"
[[ -n "${UNSEAL_KEY}" && -n "${ROOT_TOKEN}" ]] || fail "material de init do Vault ausente/ilegível (${INIT_FILE})"
if [[ "$(seal_status | grep -o '"sealed":[a-z]*' | cut -d: -f2)" == "true" ]]; then
  echo "[oidc]   Vault selado — a fazer unseal ..."
  curl -s -X POST "${VADDR}/v1/sys/unseal" -d "{\"key\":\"${UNSEAL_KEY}\"}" > /dev/null
fi
# 204 = criado; 400 "path is already in use" = já habilitado ⇒ ambos OK.
tcode="$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "X-Vault-Token: ${ROOT_TOKEN}" \
  "${VADDR}/v1/sys/mounts/transit" -d '{"type":"transit"}')"
echo "[oidc]   enable transit -> HTTP ${tcode} (204=novo, 400=já existia)"
# Sincroniza o token do nó com o root token REAL; recria o nó se mudou (lê-o no boot, 1x).
if [[ "$(cat "${SECRETS}/vault-token" 2>/dev/null)" != "${ROOT_TOKEN}" ]]; then
  printf '%s' "${ROOT_TOKEN}" > "${SECRETS}/vault-token"; chmod 644 "${SECRETS}/vault-token"
  echo "[oidc]   token do nó actualizado — a recriar o nó para o reler ..."
  docker compose -p "${PROJECT}" -f "${BASE}" -f "${OIDC}" --env-file "${ENV_FILE}" up -d --force-recreate aos >/dev/null 2>&1
fi

echo "[oidc] a aguardar o Keycloak (discovery) ..."
DISCO="https://localhost:9443/realms/aos/.well-known/openid-configuration"
tries=120
until curl -sk "${DISCO}" | grep -q '"issuer"'; do
  (( tries-- > 0 )) || fail "Keycloak não respondeu ao discovery a tempo (docker compose logs idp)"
  sleep 2
done
echo "[oidc]   discovery OK: $(curl -sk "${DISCO}" | sed -n 's/.*"issuer":"\([^"]*\)".*/\1/p')"

# Cliente 'litellm' do SSO no Keycloak. Em imports frescos vem do realm-aos.json; num realm já
# importado, cria-o idempotentemente via admin API (201=novo, 409=já existia — ambos OK).
ADMTOK="$(curl -sk -d client_id=admin-cli -d username=admin -d password=admin -d grant_type=password \
  https://localhost:9443/realms/master/protocol/openid-connect/token | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
if [[ -n "${ADMTOK}" ]]; then
  sc="$(curl -sk -o /dev/null -w '%{http_code}' -X POST https://localhost:9443/admin/realms/aos/clients \
    -H "Authorization: Bearer ${ADMTOK}" -H 'Content-Type: application/json' \
    -d '{"clientId":"litellm","secret":"litellm-dev-secret","publicClient":false,"standardFlowEnabled":true,"directAccessGrantsEnabled":false,"redirectUris":["http://localhost:4000/sso/callback","http://localhost:4000/*"],"webOrigins":["http://localhost:4000"]}')"
  echo "[oidc]   cliente SSO litellm -> HTTP ${sc} (201=novo, 409=existia)"
fi

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
