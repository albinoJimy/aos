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
# `aos-orq` não serve `/metrics` (é um processo curto). Quem sabe é o disco: o NHI em /opt/aos/nhi,
# o mandato em /opt/aos/orq e, desde o AOS-443, o ficheiro de métricas que cada drenagem deixa em
# /opt/aos/logs. Lê-se ao lado de quem os produz, e o aviso sai para o telemóvel do operador — o
# molde do alerta-ancora.sh, com o mesmo tópico.
#
# ─── O QUE DISPARA ──────────────────────────────────────────────────────────────────────────
#   NHI ausente ou ilegível               a cunhagem nunca correu, ou a pasta mudou de dono
#   NHI a menos de 20 min do fim          a cunhagem parou (é recunhado a cada 15 com 45 de vida)
#   mandato ausente                       o emissor não tem o que cunhar
#   mandato a menos de 7 dias do fim      o humano tem de assinar outro — o mint recusará depois
#   aos-cunhar-nhi / aos-drenar-planos    a última execução falhou (`systemctl is-failed`)
#   um dos dois timers não está activo    desligado, nunca instalado — fica `inactive`, não `failed`
#   nenhuma drenagem bem-sucedida há 5 h  o defeito do AOS-430 (a fila parada) visto pelo efeito
#   3 desfechos de plano seguidos falhados a drenagem corre, mas os planos não acabam bem: nem
#                                         terminal/0 nem à espera de humano (AOS-443; lido de
#                                         aos_orq_consume_falhas_consecutivas). Sem o ficheiro não
#                                         dispara: quem o produz FALHA a drenagem se não o escrever,
#                                         e isso já dispara acima
#
# Só avisa ao fim de 2 leituras seguidas em falha (30 min com o cron de 15); relembra de 24 h em
# 24 h enquanto durar; avisa quando passa. Um aviso que não sai NÃO conta: tenta de novo.
# Todos os cheques correm, e o estado guarda o CONJUNTO de causas avisado: quando ele muda — uma
# causa nova, ou uma que passou — avisa de novo, logo (AOS-443). O título diz o que está em alerta:
# «planos da fila» quando é só isso, «cunhagem/drenagem sem operador» nos outros casos.
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
DRENAGEM_MAX_S=18000
METRICAS_ORQ="${AOS_ALERTA_METRICAS_ORQ:-${AOS_DIR}/logs/aos-orq-consume.prom}"
FALHAS_PLANO_MAX="${AOS_ALERTA_FALHAS_PLANO_MAX:-3}"
[[ "${FALHAS_PLANO_MAX}" =~ ^[1-9][0-9]*$ ]] || FALHAS_PLANO_MAX=3   # um valor inválido não desliga o aviso
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

# causa <chave> <motivo> — regista UMA causa. A chave é curta, sem espaços, e é o que o estado
# guarda para saber se o conjunto de causas mudou.
causa() { CAUSAS+=("$1"); MOTIVOS+=("$2"); }

# avaliar — corre TODOS os cheques e junta as causas (AOS-443). Até aqui parava na primeira, e um
# alerta já disparado calava as seguintes: com os planos a falhar há dias, um NHI a caducar não
# voltava a avisar. Agora o estado guarda o CONJUNTO de causas, e um conjunto diferente avisa.
avaliar() {
  CAUSAS=(); MOTIVOS=()
  local agora exp="" mexp="" u ult falhas
  agora="$(date +%s)"
  if [[ ! -f "${AOS_DIR}/orq/mandato.json" || ! -s "${AOS_DIR}/orq/mandato.json" ]]; then
    causa mandato_ausente "não há mandato em ${AOS_DIR}/orq/mandato.json — o emissor automático não tem o que cunhar"
  else
    mexp="$(mandato_exp)"
    if [[ ! "${mexp}" =~ ^[0-9]+$ ]]; then
      causa mandato_ilegivel "o mandato em ${AOS_DIR}/orq/mandato.json não tem prazo legível"
      mexp=""
    elif (( mexp - agora < MANDATO_MIN_S )); then
      causa mandato_a_caducar "o mandato caduca em $(horas $(( mexp - agora ))) h — o humano tem de assinar outro (aos-issuer mandate-sign); depois disso a cunhagem recusa"
    fi
  fi
  exp="$(nhi_exp 2>/dev/null || true)"
  if [[ ! "${exp}" =~ ^[0-9]+$ ]]; then
    causa nhi_ausente "não há NHI legível em ${AOS_DIR}/nhi/nhi-run.jwt — a cunhagem nunca correu ou a pasta mudou de dono"
    exp=""
  elif (( exp - agora < NHI_MIN_S )); then
    causa nhi_a_caducar "o NHI caduca em $(( (exp - agora) / 60 )) min — a cunhagem (aos-cunhar-nhi) parou; a drenagem vai recusar reclamar planos"
  fi
  for u in aos-cunhar-nhi.service aos-drenar-planos.service; do
    if systemctl is-failed --quiet "${u}" 2>/dev/null; then
      causa "falhou:${u}" "a última execução de ${u} FALHOU — journalctl -u ${u} (a drenagem, sem root: ${AOS_DIR}/logs/drenar-planos.log)"
    fi
  done
  # `is-active` e não `is-failed`: um timer desligado ou nunca instalado NÃO está falhado — está
  # parado, e parado era exactamente o estado da fila que o AOS-430 mediu. Uma consulta ao systemd
  # que falhe conta como parado (fail-closed).
  for u in aos-cunhar-nhi.timer aos-drenar-planos.timer; do
    if ! systemctl is-active --quiet "${u}" 2>/dev/null; then
      causa "parado:${u}" "o ${u} NÃO está activo — a cunhagem ou a drenagem não correm (systemctl enable --now ${u})"
    fi
  done
  ult="$(cat "${AOS_DIR}/.drenagem/ultima-ok" 2>/dev/null || true)"
  if [[ ! "${ult}" =~ ^[0-9]+$ ]] || (( agora - ult > DRENAGEM_MAX_S )); then
    causa drenagem_parada "nenhuma drenagem bem-sucedida há mais de $(horas "${DRENAGEM_MAX_S}") h — a fila de planos está parada (${AOS_DIR}/logs/drenar-planos.log)"
  fi
  # AOS-443: a drenagem corre, mas os planos acabam mal. Um valor ilegível não dispara (o ficheiro é
  # escrito de forma atómica; ilegível seria outro defeito, que o log da drenagem mostra).
  falhas="$(awk '$1 == "aos_orq_consume_falhas_consecutivas" { print $2 }' "${METRICAS_ORQ}" 2>/dev/null || true)"
  if [[ "${falhas}" =~ ^[0-9]+$ ]] && (( falhas >= FALHAS_PLANO_MAX )); then
    causa planos_a_falhar "os últimos ${falhas} desfechos de plano não foram terminal/0 nem à espera de humano — a drenagem corre mas os planos falham (${AOS_DIR}/logs/drenar-planos.log, linhas «desfecho:»)"
  fi

  if (( ${#CAUSAS[@]} == 0 )); then
    CLASSE=ok; CHAVE=""
    MOTIVO="NHI com $(( (exp - agora) / 60 )) min de vida; mandato com $(horas $(( mexp - agora ))) h"
    return
  fi
  CLASSE=mau
  CHAVE="$(IFS=,; printf '%s' "${CAUSAS[*]}")"   # pela ordem fixa dos cheques: estável
  MOTIVO="$(printf '%s; ' "${MOTIVOS[@]}")"; MOTIVO="${MOTIVO%; }"
}

# titulo <prefixo> <chave> — o título diz O QUE está em alerta. Os planos a falhar sozinhos não são
# a cunhagem, e um título que o dissesse mandaria o operador procurar no sítio errado.
titulo() {
  if [[ "$2" == planos_a_falhar ]]; then
    printf 'AOS: planos da fila %s' "$1"
  else
    printf 'AOS: cunhagem/drenagem sem operador %s' "$1"
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

# O estado: leituras más seguidas, se há alerta disparado, quando foi o último aviso, e o CONJUNTO
# de causas avisado (AOS-443). Um estado antigo, de três campos, lê-se com o conjunto vazio: o
# primeiro alerta depois da actualização volta a sair, uma vez.
consecutivos=0; disparado=0; ultimo_aviso=0; avisado=""
if [[ -f "${ESTADO_DIR}/estado" ]]; then
  read -r consecutivos disparado ultimo_aviso avisado < "${ESTADO_DIR}/estado" || true
fi
[[ "${consecutivos}" =~ ^[0-9]+$ ]] || consecutivos=0
[[ "${disparado}" =~ ^[01]$ ]] || disparado=0
[[ "${ultimo_aviso}" =~ ^[0-9]+$ ]] || ultimo_aviso=0
[[ "${avisado}" =~ ^[A-Za-z0-9_.:,-]*$ ]] || avisado=""
agora="$(date +%s)"

if [[ "${CLASSE}" == mau ]]; then
  consecutivos=$((consecutivos + 1))
else
  consecutivos=0
fi

# O debounce (LEITURAS seguidas) é de «haver alguma causa»; uma causa NOVA num alerta já disparado
# avisa logo — é o caso de uma causa mais urgente a juntar-se a uma que já se conhecia. O lembrete de
# 24 h é para o MESMO conjunto.
if [[ "${CLASSE}" == mau && "${consecutivos}" -ge "${LEITURAS}" ]]; then
  if [[ "${disparado}" == 0 || "${CHAVE}" != "${avisado}" ]]; then
    prefixo="em ALERTA"; texto="${MOTIVO}"
    if [[ "${disparado}" == 1 ]]; then
      prefixo="em ALERTA (causas mudaram)"; texto="Agora: ${MOTIVO}"
    fi
    if notificar "$(titulo "${prefixo}" "${CHAVE}")" high "rotating_light" \
        "${texto}. Host: ${HOST_ID}. Ver deploy/server/README.md §Cunhagem sem operador."; then
      disparado=1; ultimo_aviso="${agora}"; avisado="${CHAVE}"
      log "ALERTA enviado [${CHAVE}]: ${MOTIVO}"
    fi
  elif (( agora - ultimo_aviso >= LEMBRETE_S )); then
    if notificar "$(titulo "continua em ALERTA" "${CHAVE}")" high "rotating_light" \
        "Há mais de 24 h: ${MOTIVO}. Host: ${HOST_ID}."; then
      ultimo_aviso="${agora}"
      log "LEMBRETE enviado [${CHAVE}]: ${MOTIVO}"
    fi
  else
    log "em alerta (já avisado) [${CHAVE}]: ${MOTIVO}"
  fi
elif [[ "${CLASSE}" == ok && "${disparado}" == 1 ]]; then
  if notificar "$(titulo "OK de novo" "${avisado}")" default "white_check_mark" \
      "${MOTIVO}. Host: ${HOST_ID}."; then
    disparado=0; ultimo_aviso="${agora}"; avisado=""
    log "RECUPERADO: ${MOTIVO}"
  fi
else
  log "${CLASSE} (${consecutivos}/${LEITURAS}): ${MOTIVO}"
fi

printf '%s %s %s %s\n' "${consecutivos}" "${disparado}" "${ultimo_aviso}" "${avisado}" > "${ESTADO_DIR}/estado.novo" \
  && mv -f "${ESTADO_DIR}/estado.novo" "${ESTADO_DIR}/estado"
