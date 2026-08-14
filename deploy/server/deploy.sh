#!/usr/bin/env bash
# =============================================================================
# deploy.sh — troca o nó `aos` para uma imagem nova. Corre como `aos`, no servidor.
#
#   bash /opt/aos/deploy.sh ghcr.io/albinojimy/aos-node@sha256:<digest>
#
# Um deploy é, por construção, a troca de UM digest. Isso dá três propriedades que um
# `docker compose pull && up` à mão não dá:
#   · REPRODUTÍVEL — a referência é imutável (digest, não tag móvel: `:latest` pode apontar
#     para outra coisa amanhã e o mesmo comando entregaria outro binário);
#   · REVERSÍVEL   — o digest anterior fica em image.env.prev, e rollback.sh volta a ele;
#   · VERIFICÁVEL  — a saída só é verde depois de o nó ficar `healthy` E de o edge responder
#     em TLS. Um `up -d` que devolve 0 prova apenas que o docker aceitou o pedido.
#
# FAIL-CLOSED com REVERSÃO AUTOMÁTICA: se o nó não ficar saudável ou o smoke falhar, este
# script repõe o digest anterior e sai != 0. Nunca deixa o servidor num estado que ninguém
# escolheu.
#
# Variáveis opcionais:
#   GHCR_USER / GHCR_TOKEN   login efémero no registry (o token é revogado ao fim do job de CD)
#   HEALTH_TIMEOUT           segundos a esperar pelo healthy (default 180)
#   NO_ROLLBACK=1            desliga a reversão automática (para depurar um arranque falhado)
# =============================================================================
set -euo pipefail

APP_DIR="${APP_DIR:-/opt/aos}"
COMPOSE_FILE="${APP_DIR}/docker-compose.prod.yml"
ENV_FILE="${APP_DIR}/.env"
IMAGE_ENV="${APP_DIR}/image.env"
IMAGE_ENV_PREV="${APP_DIR}/image.env.prev"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-180}"

IMAGE_REF="${1:-}"

log()  { printf '\033[36m[deploy]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[deploy] FAIL:\033[0m %s\n' "$*" >&2; exit 1; }

dc() { docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" --env-file "${IMAGE_ENV}" "$@"; }

[ -n "${IMAGE_REF}" ] || fail "uso: deploy.sh <image-ref>  (ex.: ghcr.io/albinojimy/aos-node@sha256:...)"
[ -s "${COMPOSE_FILE}" ] || fail "${COMPOSE_FILE} ausente — sincroniza deploy/server/ para ${APP_DIR}"
[ -s "${ENV_FILE}" ]     || fail "${ENV_FILE} ausente — corre provision.sh primeiro"

# A porta do edge vem do .env: com AOS_EDGE_PORT alterado, um smoke fixo em 8443 testaria uma
# porta que ninguém publicou e daria vermelho num deploy verde (ou pior, o contrário).
# shellcheck disable=SC1090
set -a; . "${ENV_FILE}"; set +a
EDGE_PORT="${AOS_EDGE_PORT:-8444}"

# Uma tag móvel entregue como se fosse uma versão é a forma mais comum de um rollback "bem
# sucedido" repor exactamente o binário partido. Avisa, não bloqueia (dev pode querê-lo).
case "${IMAGE_REF}" in
  *@sha256:*) : ;;
  *) log "⚠️ ${IMAGE_REF} não é um digest — o rollback deixa de ser garantido (tags são móveis)." ;;
esac

# --- 0. Bundle PDP presente? ------------------------------------------------------------------
# Sem bundle o nó ABORTA no arranque. Melhor dizê-lo agora do que depois de derrubar o que corre.
[ -s "${APP_DIR}/policies/aos_authz.cedar" ] && [ -s "${APP_DIR}/policies/aos_authz.sig" ] \
  || fail "bundle PDP ausente em ${APP_DIR}/policies — o nó abortaria o arranque (AOS_POLICY_BUNDLE_DIR)"

# --- 1. Login efémero no registry (se fornecido) ------------------------------------------------
LOGGED_IN=0
if [ -n "${GHCR_TOKEN:-}" ]; then
  log "1/6 login em ghcr.io como ${GHCR_USER:-x-access-token} ..."
  printf '%s' "${GHCR_TOKEN}" | docker login ghcr.io -u "${GHCR_USER:-x-access-token}" --password-stdin >/dev/null \
    || fail "docker login em ghcr.io falhou"
  LOGGED_IN=1
else
  log "1/6 sem GHCR_TOKEN — assumo imagem pública ou credencial já no docker config"
fi
# O logout corre SEMPRE, incluindo em falha: uma credencial de CD não fica a residir no
# ~/.docker/config.json de um servidor entre deploys.
cleanup_login() { [ "${LOGGED_IN}" -eq 1 ] && docker logout ghcr.io >/dev/null 2>&1 || true; }
trap cleanup_login EXIT

# --- 2. Pull ANTES de tocar no que corre ---------------------------------------------------------
log "2/6 pull ${IMAGE_REF} ..."
docker pull "${IMAGE_REF}" >/dev/null || fail "pull falhou — o stack em execução NÃO foi tocado"

# --- 3. Guarda o digest anterior (base do rollback) ----------------------------------------------
log "3/6 a registar o estado anterior ..."
if [ -s "${IMAGE_ENV}" ]; then
  cp "${IMAGE_ENV}" "${IMAGE_ENV_PREV}"
  PREV_REF="$( grep -E '^AOS_IMAGE=' "${IMAGE_ENV_PREV}" | tail -1 | cut -d= -f2- )"
  log "     anterior: ${PREV_REF}"
else
  PREV_REF=""
  log "     primeiro deploy (sem anterior — não haverá reversão automática)"
fi

RESOLVED="$( docker image inspect --format '{{index .RepoDigests 0}}' "${IMAGE_REF}" 2>/dev/null || echo "${IMAGE_REF}" )"
{
  echo "# Escrito por deploy.sh — NÃO editar à mão."
  echo "AOS_IMAGE=${RESOLVED}"
} > "${IMAGE_ENV}"

# --- 4. Sobe ---------------------------------------------------------------------------------------
log "4/6 docker compose up -d ..."
dc up -d --remove-orphans || fail "compose up falhou"

# --- 5. Espera pelo healthy do NÓ (não do edge: o edge só arranca depois) -------------------------
log "5/6 a aguardar o nó healthy (tecto ${HEALTH_TIMEOUT}s) ..."
CID="$( dc ps -q aos )"
[ -n "${CID}" ] || fail "container do nó não existe após o up"

deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
healthy=0
while [ "$(date +%s)" -lt "${deadline}" ]; do
  hs="$( docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}nohealth{{end}}' "${CID}" 2>/dev/null || echo '?' )"
  st="$( docker inspect -f '{{.State.Status}}' "${CID}" 2>/dev/null || echo '?' )"
  if [ "${hs}" = "healthy" ]; then healthy=1; break; fi
  # Um container que saiu não vai ficar healthy — falhar já poupa o tecto inteiro e, mais
  # importante, mostra o log do ABORT do nó (config inválida) em vez de um timeout mudo.
  if [ "${st}" = "exited" ] || [ "${st}" = "dead" ]; then
    log "     nó em estado '${st}'. Últimas linhas:"
    dc logs --tail 40 aos 2>&1 | sed 's/^/       /'
    break
  fi
  sleep 3
done

# --- 6. Smoke em TLS pelo edge (o caminho REAL do cliente) ----------------------------------------
smoke_ok=0
if [ "${healthy}" -eq 1 ]; then
  log "6/6 smoke: GET https://127.0.0.1:${EDGE_PORT}/healthz ..."
  for _ in 1 2 3 4 5; do
    code="$( curl -sk -o /dev/null -w '%{http_code}' --max-time 10 \
             "https://127.0.0.1:${EDGE_PORT}/healthz" || echo 000 )"
    if [ "${code}" = "200" ]; then smoke_ok=1; log "     HTTP ${code}"; break; fi
    sleep 3
  done
  [ "${smoke_ok}" -eq 1 ] || log "     smoke vermelho (último código: ${code:-000})"
fi

if [ "${healthy}" -eq 1 ] && [ "${smoke_ok}" -eq 1 ]; then
  log "✅ deploy verde — ${RESOLVED}"
  dc logs --tail 25 aos 2>&1 | grep -iE 'endurecid|operador|four-eyes|ratificador|BUNDLE CARREGADO|durav|WORM|soberania|OTLP|AVISO' | sed 's/^/       /' || true
  exit 0
fi

# --- Reversão -------------------------------------------------------------------------------------
if [ "${NO_ROLLBACK:-0}" = "1" ]; then
  fail "deploy vermelho e NO_ROLLBACK=1 — o stack fica como está, para inspecção."
fi
if [ -z "${PREV_REF}" ]; then
  fail "deploy vermelho no PRIMEIRO deploy — não há digest anterior para onde reverter. Corrige a config (ver log acima) e repete."
fi

log "⏪ deploy vermelho — a reverter para ${PREV_REF} ..."
cp "${IMAGE_ENV_PREV}" "${IMAGE_ENV}"
dc up -d --remove-orphans || fail "REVERSÃO FALHOU — servidor precisa de intervenção manual. Estado: docker compose -f ${COMPOSE_FILE} ps"
fail "deploy revertido para ${PREV_REF}. A versão nova NÃO está a servir."
