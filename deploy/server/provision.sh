#!/usr/bin/env bash
# =============================================================================
# provision.sh — material NÃO-SECRETO do servidor + TLS do edge. Corre como `aos`, UMA vez.
#
#   ssh aos@37.60.241.150 'bash /opt/aos/provision.sh'
#
# IDEMPOTENTE: reutiliza o que já existe. O certificado só é regerado com FORCE_TLS=1.
#
# ─── O que este script NÃO faz, e é o ponto ──────────────────────────────────────────────────
# NÃO gera chaves de issuer, operador, ratificador ou aprovador. Nenhuma delas pode ser gerada
# aqui: se a chave de assinatura do issuer nascesse no servidor, o servidor passaria a poder
# mintar a sua própria identidade e o "trust-anchor-only" do nó seria uma ficção. Essas chaves
# nascem e ficam na máquina do operador (deploy/server/gen-identity.sh); o servidor recebe SÓ
# as PUBKEYS, por /opt/aos/.env.
#
# A ÚNICA chave privada que este script cria é a do TLS do edge — porque é o edge que a usa,
# e ela não autoriza nada: cifra transporte, não autentica sujeitos.
# =============================================================================
set -euo pipefail

APP_DIR="${APP_DIR:-/opt/aos}"
ENV_FILE="${APP_DIR}/.env"
SECRETS="${APP_DIR}/secrets"

log()  { printf '\033[36m[provision]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[provision] FAIL:\033[0m %s\n' "$*" >&2; exit 1; }

command -v docker  >/dev/null 2>&1 || fail "docker ausente — corre bootstrap.sh como root primeiro"
command -v openssl >/dev/null 2>&1 || fail "openssl ausente (apt-get install openssl)"

mkdir -p "${SECRETS}/tls" "${APP_DIR}/policies"
chmod 700 "${SECRETS}"

# --- 1. .env: existe e tem TODAS as variáveis obrigatórias? -----------------------------------
log "1/4 a validar ${ENV_FILE} ..."
[ -s "${ENV_FILE}" ] || fail "${ENV_FILE} não existe. Copia .env.example para .env e preenche-o com o output de gen-identity.sh."
chmod 600 "${ENV_FILE}"

# Fail-closed e RUIDOSO: uma variável em falta é abortada aqui, com o nome, e não seis meses
# depois num boot que aborta com um erro do runtime. Lista alinhada com os `${VAR:?}` do compose.
missing=()
for var in AOS_ISSUER_ID AOS_ISSUER_PUBKEY AOS_OPERATORS AOS_RATIFIERS \
           AOS_POLICY_TRUST_ANCHOR AOS_BOARD_REGIONS AOS_EDGE_HOST; do
  val="$( grep -E "^${var}=" "${ENV_FILE}" | tail -1 | cut -d= -f2- | tr -d '"'"'"' \r' || true )"
  [ -n "${val}" ] || missing+=("${var}")
done
[ "${#missing[@]}" -eq 0 ] || fail "variáveis por preencher em ${ENV_FILE}: ${missing[*]}"

# shellcheck disable=SC1090
set -a; . "${ENV_FILE}"; set +a

# As duas âncoras são ed25519 em hex: 64 caracteres. Um valor truncado faz o nó recusar TODAS
# as credenciais legítimas — falha silenciosa do lado da config. Apanha-se aqui.
[ "${#AOS_ISSUER_PUBKEY}" -eq 64 ]        || fail "AOS_ISSUER_PUBKEY tem ${#AOS_ISSUER_PUBKEY} chars (esperado 64 hex)"
[ "${#AOS_POLICY_TRUST_ANCHOR}" -eq 64 ]  || fail "AOS_POLICY_TRUST_ANCHOR tem ${#AOS_POLICY_TRUST_ANCHOR} chars (esperado 64 hex)"

# --- 2. Rosters públicos (four-eyes + directório de autoridade) --------------------------------
log "2/4 rosters públicos ..."
for f in approvers.json authority.json; do
  if [ ! -s "${SECRETS}/${f}" ]; then
    [ -s "${APP_DIR}/templates/${f}.example" ] \
      || fail "${SECRETS}/${f} ausente e sem template em ${APP_DIR}/templates/${f}.example"
    cp "${APP_DIR}/templates/${f}.example" "${SECRETS}/${f}"
    log "     ${f} criado a partir do template — ⚠️ EDITA-O com as pubkeys reais antes de confiar no four-eyes"
  fi
  # UID 65532 (non-root da imagem distroless) tem de LER estes mounts.
  chmod 644 "${SECRETS}/${f}"
done

# Um roster de aprovadores com as pubkeys do template é pior do que nenhum: cria a expectativa
# de supervisão humana que ninguém consegue exercer (não há quem detenha a chave privada).
if grep -q 'SUBSTITUI-ME' "${SECRETS}/approvers.json" 2>/dev/null; then
  log "     ⚠️ approvers.json ainda tem marcadores SUBSTITUI-ME — o four-eyes NÃO destrava nada assim."
fi

# --- 3. TLS do edge ----------------------------------------------------------------------------
log "3/4 TLS do edge (${AOS_EDGE_HOST}) ..."
if [ -s "${SECRETS}/tls/edge.crt" ] && [ "${FORCE_TLS:-0}" != "1" ]; then
  not_after="$( openssl x509 -in "${SECRETS}/tls/edge.crt" -noout -enddate | cut -d= -f2 )"
  log "     certificado já existe (válido até ${not_after}) — FORCE_TLS=1 para regerar"
else
  # SAN correcto: um cert por IP precisa de IP: no subjectAltName, senão todo o cliente moderno
  # o recusa mesmo com o CN certo (o CN foi deprecado para validação de nome desde há muito).
  if printf '%s' "${AOS_EDGE_HOST}" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'; then
    san="IP:${AOS_EDGE_HOST}"
  else
    san="DNS:${AOS_EDGE_HOST}"
  fi
  openssl req -x509 -nodes -newkey rsa:2048 -days 397 \
    -keyout "${SECRETS}/tls/edge.key" -out "${SECRETS}/tls/edge.crt" \
    -subj "/CN=${AOS_EDGE_HOST}" -addext "subjectAltName=${san}" >/dev/null 2>&1 \
    || fail "openssl falhou a gerar o certificado do edge"
  log "     certificado self-signed gerado (SAN=${san}, 397 dias)"
  log "     ⚠️ self-signed ⇒ os clientes precisam de --cacert/-k. Com domínio, vê README §TLS real."
fi
chmod 644 "${SECRETS}/tls/edge.crt"
chmod 640 "${SECRETS}/tls/edge.key"

# --- 4. Bundle PDP presente? --------------------------------------------------------------------
log "4/4 bundle PDP ..."
if [ -s "${APP_DIR}/policies/aos_authz.cedar" ] && [ -s "${APP_DIR}/policies/aos_authz.sig" ]; then
  log "     bundle presente (${APP_DIR}/policies)"
else
  log "     ⚠️ bundle AUSENTE — o deploy sincroniza-o a partir do repositório. Até lá, o nó"
  log "        ABORTA o arranque (AOS_POLICY_BUNDLE_DIR aponta para um bundle que não carrega)."
fi

log "✅ servidor provisionado. Falta só o primeiro deploy:"
echo "     bash ${APP_DIR}/deploy.sh ghcr.io/albinojimy/aos-node@sha256:<digest>"
