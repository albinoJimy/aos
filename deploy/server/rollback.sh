#!/usr/bin/env bash
# =============================================================================
# rollback.sh — reversão MANUAL para o digest anterior (ou para um explícito).
#
#   bash /opt/aos/rollback.sh                                     # volta ao digest anterior
#   bash /opt/aos/rollback.sh ghcr.io/albinojimy/aos-node@sha256:...  # volta a um digest à escolha
#
# Existe separado de deploy.sh porque a reversão automática deste só cobre o deploy que a
# despoletou. Uma regressão descoberta HORAS depois (um bug que só aparece com carga real) já
# não tem esse contexto — e é aí que a reversão tem de ser um comando, não uma arqueologia.
#
# A reversão é um deploy: passa pelos mesmos gates de saúde e smoke. Um rollback que não se
# verifica é só uma segunda avaria.
# =============================================================================
set -euo pipefail

APP_DIR="${APP_DIR:-/opt/aos}"
IMAGE_ENV_PREV="${APP_DIR}/image.env.prev"

log()  { printf '\033[36m[rollback]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[rollback] FAIL:\033[0m %s\n' "$*" >&2; exit 1; }

TARGET="${1:-}"
if [ -z "${TARGET}" ]; then
  [ -s "${IMAGE_ENV_PREV}" ] || fail "sem ${IMAGE_ENV_PREV} — não há anterior conhecido. Passa o digest à mão: rollback.sh <image-ref>"
  TARGET="$( grep -E '^AOS_IMAGE=' "${IMAGE_ENV_PREV}" | tail -1 | cut -d= -f2- )"
  [ -n "${TARGET}" ] || fail "${IMAGE_ENV_PREV} não tem AOS_IMAGE"
  log "alvo (anterior registado): ${TARGET}"
else
  log "alvo (explícito): ${TARGET}"
fi

# NO_ROLLBACK=1: se a reversão em si falhar, NÃO se auto-reverte para a versão partida de onde
# viemos — isso seria um ciclo. Fica como está, para o operador ver.
NO_ROLLBACK=1 exec bash "${APP_DIR}/deploy.sh" "${TARGET}"
