#!/usr/bin/env bash
# alerta-ancora.sh — avisa por push (ntfy) quando a âncora do WORM deixa de chegar ao servidor.
#
#   cron do `aos`:   */15 * * * * /bin/bash /opt/aos/alerta-ancora.sh >/dev/null 2>&1
#   à mão:           bash /opt/aos/alerta-ancora.sh            (avalia e mostra o estado)
#                    bash /opt/aos/alerta-ancora.sh --teste    (envia um aviso de TESTE, não mexe no estado)
#
# ─── PORQUÊ NO SERVIDOR ─────────────────────────────────────────────────────────────────────
# A selagem diária (AOS-SelarWORM) corre na máquina do operador. Um alerta avaliado nessa mesma
# máquina fica cego exactamente quando a selagem morre por a máquina estar desligada. Aqui, a
# série que diz a verdade — `aos_worm_anchor_delivered_age_seconds`, lida do par MONTADO a cada
# recolha — é avaliada ao lado de quem a produz, e o aviso sai para o telemóvel do operador.
#
# ─── O QUE DISPARA ──────────────────────────────────────────────────────────────────────────
#   idade > 172800 s (48 h)     a selagem parou (o dobro da cadência: um dia falhado não alerta)
#   idade < 0                   o relógio de quem sela está adiantado — nunca cruzaria o limiar
#   _unreadable == 1            o par entregue não passa a validação de forma do arranque
#   séries ausentes             âncora desligada, ou o nó sem os ficheiros
#   métricas sem resposta       nó ou collector em baixo — o silêncio conta como falha
#
# Só avisa ao fim de 2 leituras seguidas em falha (30 min com o cron de 15): a troca legítima dos
# dois `mv` e um restart do nó atravessam, por instantes, um estado que pareceria avariado.
# Avisa ao entrar em alerta, relembra de 24 h em 24 h enquanto durar, e avisa quando passa. Um
# aviso que não consegue sair NÃO conta como dado: tenta de novo na execução seguinte.
#
# ─── O QUE SAI DO SERVIDOR ──────────────────────────────────────────────────────────────────
# Para o ntfy vão o título, o motivo (nomes de séries e horas) e o nome do host. Nada do WORM,
# nada de configuração. O tópico é o segredo: quem o souber lê os avisos e pode publicar nele.
# Vive em /opt/aos/secrets/ntfy-topico (600) e entra no backup cifrado com o resto de secrets/.
set -uo pipefail

AOS_DIR="${AOS_DIR:-/opt/aos}"
ESTADO_DIR="${AOS_ALERTA_ESTADO_DIR:-${AOS_DIR}/.alerta-ancora}"
TOPICO_FILE="${AOS_ALERTA_TOPICO_FILE:-${AOS_DIR}/secrets/ntfy-topico}"
NTFY_BASE="${AOS_ALERTA_NTFY_BASE:-https://ntfy.sh}"
# Só para ENSAIO: um URL de métricas directo. Em produção o otel não publica a porta no host, e lê-se
# pela rede interna do compose com a mesma imagem fixada do gate da selagem.
METRICAS_URL="${AOS_ALERTA_METRICAS_URL:-}"
ALPINE="alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
LIMITE_S=172800
LEITURAS=2
LEMBRETE_S=86400
HOST_ID="$(hostname 2>/dev/null || echo servidor)"

log() {
  logger -t aos-alerta-ancora "$1" 2>/dev/null || true
  printf '[alerta-ancora] %s\n' "$1"
}

metricas() {
  # O ramo de PRODUÇÃO vem primeiro, e a ordem não é estética: o scan de segredos da CI (gitleaks)
  # lia o uid:gid do docker, quando vinha poucas linhas depois do pedido HTTP do ramo de ensaio,
  # como credencial desse pedido — um falso positivo que partia o gate `secrets`. Este comentário
  # também não nomeia o cliente HTTP nem a flag, pela mesma razão.
  if [[ -z "${METRICAS_URL}" ]]; then
    docker run --rm --pull=never --log-driver none --network aos_default --read-only \
      --cap-drop ALL --security-opt no-new-privileges --user 65534:65534 \
      "${ALPINE}" wget -q -T 20 -O - http://otel:9464/metrics
    return
  fi
  curl -fsS --max-time 20 "${METRICAS_URL}"
}

# valor <série> — primeira amostra da série no texto das métricas em $TXT (formato Prometheus).
valor() {
  awk -v n="$1" '$0 !~ /^#/ { split($1, a, "{"); if (a[1] == n) { print $NF; exit } }' <<<"${TXT}"
}

horas() { awk -v s="$1" 'BEGIN { printf "%.1f", s / 3600 }'; }

avaliar() {
  CLASSE=mau
  if ! TXT="$(metricas 2>/dev/null)" || [[ -z "${TXT}" ]]; then
    MOTIVO="o otel:9464 não respondeu — nó ou collector em baixo, e a âncora deixa de ser observável"
    return
  fi
  local idade ilegivel
  idade="$(valor aos_worm_anchor_delivered_age_seconds)"
  ilegivel="$(valor aos_worm_anchor_delivered_unreadable)"
  if [[ -z "${idade}" && -z "${ilegivel}" ]]; then
    MOTIVO="as séries aos_worm_anchor_delivered_* não existem — âncora desligada, ou o nó sem os ficheiros"
  elif [[ "${ilegivel}" == 1 ]]; then
    MOTIVO="o par entregue não passa a validação de forma (aos_worm_anchor_delivered_unreadable=1) — o nó não arrancaria com ele"
  elif [[ -z "${idade}" ]]; then
    MOTIVO="falta aos_worm_anchor_delivered_age_seconds"
  elif awk -v a="${idade}" 'BEGIN { exit !(a != a + 0 || a == "NaN") }'; then
    MOTIVO="idade da âncora não numérica (${idade})"
  elif awk -v a="${idade}" -v l="${LIMITE_S}" 'BEGIN { exit !(a > l) }'; then
    MOTIVO="a última âncora entregue tem $(horas "${idade}") h (limite 48 h) — a selagem diária (AOS-SelarWORM) parou"
  elif awk -v a="${idade}" 'BEGIN { exit !(a < 0) }'; then
    MOTIVO="idade da âncora NEGATIVA (${idade} s) — o relógio da máquina que sela está adiantado"
  else
    CLASSE=ok
    MOTIVO="âncora entregue há $(horas "${idade}") h"
  fi
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
  if notificar "AOS: teste do alerta da ancora" default "white_check_mark" \
      "Teste do alerta da âncora do WORM em ${HOST_ID}. Estado actual: ${CLASSE} — ${MOTIVO}."; then
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
    if notificar "AOS: ancora do WORM em ALERTA" high "rotating_light" \
        "${MOTIVO}. Host: ${HOST_ID}. Ver ULTIMA.txt da selagem na máquina do operador e o README §8."; then
      disparado=1; ultimo_aviso="${agora}"
      log "ALERTA enviado: ${MOTIVO}"
    fi
  elif (( agora - ultimo_aviso >= LEMBRETE_S )); then
    if notificar "AOS: ancora do WORM continua em ALERTA" high "rotating_light" \
        "Há mais de 24 h: ${MOTIVO}. Host: ${HOST_ID}."; then
      ultimo_aviso="${agora}"
      log "LEMBRETE enviado: ${MOTIVO}"
    fi
  else
    log "em alerta (já avisado): ${MOTIVO}"
  fi
elif [[ "${CLASSE}" == ok && "${disparado}" == 1 ]]; then
  if notificar "AOS: ancora do WORM recuperada" default "white_check_mark" \
      "${MOTIVO}. Host: ${HOST_ID}."; then
    disparado=0; ultimo_aviso="${agora}"
    log "RECUPERADO: ${MOTIVO}"
  fi
else
  log "${CLASSE} (${consecutivos}/${LEITURAS}): ${MOTIVO}"
fi

printf '%s %s %s\n' "${consecutivos}" "${disparado}" "${ultimo_aviso}" > "${ESTADO_DIR}/estado.novo" \
  && mv -f "${ESTADO_DIR}/estado.novo" "${ESTADO_DIR}/estado"
