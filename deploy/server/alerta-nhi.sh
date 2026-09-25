#!/usr/bin/env bash
# alerta-nhi.sh — avisa por push (ntfy) ANTES de a cunhagem sem operador deixar o caminho do plano
# sem credencial (AOS-437; o sensor que o ADR-032 §2.3 torna condição da renovação).
#
#   cron do `aos`:   */15 * * * * /bin/bash /opt/aos/alerta-nhi.sh >/dev/null 2>&1
#   à mão:           bash /opt/aos/alerta-nhi.sh            (avalia e mostra o estado)
#                    bash /opt/aos/alerta-nhi.sh --teste    (envia um aviso de TESTE, não mexe no estado)
#
# ─── PORQUÊ NO SERVIDOR, E NÃO NO NÓ ────────────────────────────────────────────────────────
# O critério é «visível ANTES de o run falhar». O nó só sabe da credencial quando ela lhe chega, e o
# `aos-orq` não expõe métricas. Quem sabe é o disco: o NHI em /opt/aos/nhi e o mandato em
# /opt/aos/orq. Lê-se ao lado de quem os produz, e o aviso sai para o telemóvel do operador — o
# molde do alerta-ancora.sh, com o mesmo tópico.
#
# ─── O QUE DISPARA ──────────────────────────────────────────────────────────────────────────
#   NHI ausente ou ilegível               a cunhagem nunca correu, ou a pasta mudou de dono
#   NHI a menos de 20 min do fim          a cunhagem parou (é recunhado a cada 15 com 45 de vida)
#   mandato ausente                       o emissor não tem o que cunhar
#   mandato a menos de 7 dias do fim      o humano tem de assinar outro — o mint recusará depois
#   aos-cunhar-nhi / aos-drenar-planos    a última execução falhou (`systemctl is-failed`)
#
# Só avisa ao fim de 2 leituras seguidas em falha (30 min com o cron de 15); relembra de 24 h em
# 24 h enquanto durar; avisa quando passa. Um aviso que não sai NÃO conta: tenta de novo.
#
# O QUE SAI DO SERVIDOR: título, motivo (prazos em horas, nomes de unidades) e o nome do host.
# Nunca o token, nunca o mandato, nunca o humano que o assinou.
set -uo pipefail
AOS_DIR="${AOS_DIR:-/opt/aos}"
ESTADO_DIR="${AOS_ALERTA_NHI_ESTADO_DIR:-${AOS_DIR}/.alerta-nhi}"
TOPICO_FILE="${AOS_ALERTA_TOPICO_FILE:-${AOS_DIR}/secrets/ntfy-topico}"
NTFY_BASE="${AOS_ALERTA_NTFY_BASE:-https://ntfy.sh}"
ALPINE="alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
NHI_MIN_S=1200
MANDATO_MIN_S=604800
LEITURAS=2
LEMBRETE_S=86400
HOST_ID="$(hostname 2>/dev/null || echo servidor)"

log() {
  logger -t aos-alerta-nhi "$1" 2>/dev/null || true
  printf '[alerta-nhi] %s\n' "$1"
}

horas() { awk -v s="$1" 'BEGIN { printf "%.1f", s / 3600 }'; }

# nhi_exp — o `exp` de TOPO do NHI, lido como o uid 65532 (dono da pasta, 0700), sem rede. Ancora
# em `"exp":N,"jti"`: o mandato embebido também tem `exp`, e só o de topo é seguido de `jti` — forma
# fixada por TestAOS437OExpDeTopoESeguidoDoJti (packages/cmd/aos-issuer).
nhi_exp() {
  docker run --rm --pull=never --log-driver none --network none --read-only --cap-drop ALL \
    --security-opt no-new-privileges --user 65532:65532 -v "${AOS_DIR}/nhi":/n:ro "${ALPINE}" sh -c '
      [ -s /n/nhi-run.jwt ] || exit 3
      p=$(cut -d. -f2 /n/nhi-run.jwt | tr "_-" "/+")
      case $(( ${#p} % 4 )) in 2) p="$p==";; 3) p="$p=";; esac
      printf %s "$p" | base64 -d 2>/dev/null | sed -n "s/.*\"exp\":\([0-9]*\),\"jti\".*/\1/p"'
}

# mandato_exp — o `exp` do mandato (JSON indentado do mandate-sign, com um só `exp`). Não é segredo.
mandato_exp() {
  sed -n 's/.*"exp": *\([0-9][0-9]*\).*/\1/p' "${AOS_DIR}/orq/mandato.json" 2>/dev/null | head -n 1
}

avaliar() {
  CLASSE=mau
  local agora exp mexp u
  agora="$(date +%s)"
  if [[ ! -s "${AOS_DIR}/orq/mandato.json" ]]; then
    MOTIVO="não há mandato em ${AOS_DIR}/orq/mandato.json — o emissor automático não tem o que cunhar"
    return
  fi
  mexp="$(mandato_exp)"
  if [[ ! "${mexp}" =~ ^[0-9]+$ ]]; then
    MOTIVO="o mandato em ${AOS_DIR}/orq/mandato.json não tem prazo legível"
    return
  fi
  if (( mexp - agora < MANDATO_MIN_S )); then
    MOTIVO="o mandato caduca em $(horas $(( mexp - agora ))) h — o humano tem de assinar outro (aos-issuer mandate-sign); depois disso a cunhagem recusa"
    return
  fi
  exp="$(nhi_exp 2>/dev/null || true)"
  if [[ ! "${exp}" =~ ^[0-9]+$ ]]; then
    MOTIVO="não há NHI legível em ${AOS_DIR}/nhi/nhi-run.jwt — a cunhagem nunca correu ou a pasta mudou de dono"
    return
  fi
  if (( exp - agora < NHI_MIN_S )); then
    MOTIVO="o NHI caduca em $(( (exp - agora) / 60 )) min — a cunhagem (aos-cunhar-nhi) parou; a drenagem vai recusar reclamar planos"
    return
  fi
  for u in aos-cunhar-nhi.service aos-drenar-planos.service; do
    if systemctl is-failed --quiet "${u}" 2>/dev/null; then
      MOTIVO="a última execução de ${u} FALHOU — journalctl -u ${u}"
      return
    fi
  done
  CLASSE=ok
  MOTIVO="NHI com $(( (exp - agora) / 60 )) min de vida; mandato com $(horas $(( mexp - agora ))) h"
}

# notificar <título ASCII> <prioridade> <tags> <mensagem> — devolve != 0 se o aviso não saiu.
notificar() {
  local topico
  topico="$(tr -d ' \r\n' < "${TOPICO_FILE}" 2>/dev/null || true)"
  if [[ ! "${topico}" =~ ^[A-Za-z0-9_-]{16,64}$ ]]; then
    log "sem tópico ntfy válido em ${TOPICO_FILE} — o aviso NÃO saiu: $1"
    return 1
  fi
  if ! curl -fsS --max-time 20 -H "Title: $1" -H "Priority: $2" -H "Tags: $3" \
      --data-binary "$4" "${NTFY_BASE}/${topico}" >/dev/null 2>&1; then
    log "o envio para o ntfy FALHOU — tenta-se na execução seguinte: $1"
    return 1
  fi
}

avaliar

if [[ "${1:-}" == "--teste" ]]; then
  if notificar "AOS: teste do alerta do NHI" default "white_check_mark" \
      "Teste do alerta da cunhagem sem operador em ${HOST_ID}. Estado actual: ${CLASSE} — ${MOTIVO}."; then
    log "aviso de TESTE enviado (estado actual: ${CLASSE} — ${MOTIVO})"
    exit 0
  fi
  exit 1
fi

mkdir -p "${ESTADO_DIR}" && chmod 700 "${ESTADO_DIR}"
exec 9>"${ESTADO_DIR}/lock"
flock -n 9 || { log "outra execução em curso — esta termina"; exit 0; }

consecutivos=0; disparado=0; ultimo_aviso=0
if [[ -f "${ESTADO_DIR}/estado" ]]; then
  read -r consecutivos disparado ultimo_aviso < "${ESTADO_DIR}/estado" || true
fi
[[ "${consecutivos}" =~ ^[0-9]+$ ]] || consecutivos=0
[[ "${disparado}" =~ ^[01]$ ]] || disparado=0
[[ "${ultimo_aviso}" =~ ^[0-9]+$ ]] || ultimo_aviso=0
agora="$(date +%s)"

if [[ "${CLASSE}" == mau ]]; then
  consecutivos=$((consecutivos + 1))
else
  consecutivos=0
fi

if [[ "${CLASSE}" == mau && "${consecutivos}" -ge "${LEITURAS}" ]]; then
  if [[ "${disparado}" == 0 ]]; then
    if notificar "AOS: cunhagem sem operador em ALERTA" high "rotating_light" \
        "${MOTIVO}. Host: ${HOST_ID}. Ver deploy/server/README.md §Cunhagem sem operador."; then
      disparado=1; ultimo_aviso="${agora}"
      log "ALERTA enviado: ${MOTIVO}"
    fi
  elif (( agora - ultimo_aviso >= LEMBRETE_S )); then
    if notificar "AOS: cunhagem sem operador continua em ALERTA" high "rotating_light" \
        "Há mais de 24 h: ${MOTIVO}. Host: ${HOST_ID}."; then
      ultimo_aviso="${agora}"
      log "LEMBRETE enviado: ${MOTIVO}"
    fi
  else
    log "em alerta (já avisado): ${MOTIVO}"
  fi
elif [[ "${CLASSE}" == ok && "${disparado}" == 1 ]]; then
  if notificar "AOS: cunhagem sem operador recuperada" default "white_check_mark" \
      "${MOTIVO}. Host: ${HOST_ID}."; then
    disparado=0; ultimo_aviso="${agora}"
    log "RECUPERADO: ${MOTIVO}"
  fi
else
  log "${CLASSE} (${consecutivos}/${LEITURAS}): ${MOTIVO}"
fi

printf '%s %s %s\n' "${consecutivos}" "${disparado}" "${ultimo_aviso}" > "${ESTADO_DIR}/estado.novo" \
  && mv -f "${ESTADO_DIR}/estado.novo" "${ESTADO_DIR}/estado"
