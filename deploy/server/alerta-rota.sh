#!/usr/bin/env bash
# alerta-rota.sh — avisa por push (ntfy) quando o modelo que o proxy declara ter servido deixa de
# ser o do perfil da rota (AOS-505): a regra de alerta sobre `diferente` maior do que zero.
#
#   cron do `aos`:   */15 * * * * /bin/bash /opt/aos/alerta-rota.sh >/dev/null 2>&1
#   à mão:           bash /opt/aos/alerta-rota.sh            (avalia e mostra o estado)
#                    bash /opt/aos/alerta-rota.sh --teste    (envia um aviso de TESTE, não mexe no estado)
#
# ─── O QUE LÊ ───────────────────────────────────────────────────────────────────────────────
# A família `aos_model_route_checks_total{result=…,served=…}` do `/metrics` do nó, que só existe
# com `AOS_MODEL_ROUTE_GOVERNANCE` em `observe` ou `enforce`. Soma as amostras por resultado.
#
# ─── O QUE DISPARA ──────────────────────────────────────────────────────────────────────────
#   soma de result="diferente" > 0     o proxy declarou outro modelo ou outro endpoint, ou o nome
#                                      pedido não tem perfil: uma troca de CONFIGURAÇÃO no proxy
#
# O QUE NÃO DISPARA, e porquê:
#   família ausente                    a governação da rota está desligada — é o estado por omissão
#   result="nao_reportado" > 0         fica no estado mostrado e não avisa: a regra pedida é sobre
#                                      `diferente`; quem quiser segui-lo lê o contador
#   métricas sem resposta              nó ou collector em baixo — é o alerta-ancora.sh que avisa
#
# O contador é por processo e só sobe: uma vez acima de zero, fica até o nó reiniciar. Avisa à
# primeira leitura, relembra de 24 h em 24 h enquanto durar, e avisa quando volta a zero (o nó
# reiniciou; se a troca continuar, volta a disparar no turno seguinte).
#
# O QUE NÃO DETECTA: uma troca feita pelo provider por trás do mesmo nome e do mesmo endpoint. O
# que o nó compara é o que o proxy DECLARA nos cabeçalhos, e isso só muda com a configuração dele.
#
# O QUE SAI DO SERVIDOR: título, as três somas e o nome do host. Nenhum nome de modelo, nenhum
# endereço. O tópico é o segredo (o mesmo do alerta-ancora.sh).
set -uo pipefail

AOS_DIR="${AOS_DIR:-/opt/aos}"
ESTADO_DIR="${AOS_ALERTA_ROTA_ESTADO_DIR:-${AOS_DIR}/.alerta-rota}"
TOPICO_FILE="${AOS_ALERTA_TOPICO_FILE:-${AOS_DIR}/secrets/ntfy-topico}"
NTFY_BASE="${AOS_ALERTA_NTFY_BASE:-https://ntfy.sh}"
# Só para ENSAIO: um URL de métricas directo. Em produção lê-se pela rede interna do compose.
METRICAS_URL="${AOS_ALERTA_METRICAS_URL:-}"
ALPINE="alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
LEMBRETE_S=86400
HOST_ID="$(hostname 2>/dev/null || echo servidor)"

log() {
  logger -t aos-alerta-rota "$1" 2>/dev/null || true
  printf '[alerta-rota] %s\n' "$1"
}

metricas() {
  # O ramo de PRODUÇÃO vem primeiro, pela razão escrita no alerta-ancora.sh (o scan de segredos).
  if [[ -z "${METRICAS_URL}" ]]; then
    docker run --rm --pull=never --log-driver none --network aos_default --read-only \
      --cap-drop ALL --security-opt no-new-privileges --user 65534:65534 \
      "${ALPINE}" wget -q -T 20 -O - http://otel:9464/metrics
    return
  fi
  curl -fsS --max-time 20 "${METRICAS_URL}"
}

# soma <resultado> — soma das amostras de aos_model_route_checks_total com esse `result`, em
# $TXT. Vazio se a família não tem nenhuma amostra desse resultado.
soma() {
  awk -v r="result=\"$1\"" '
    $0 !~ /^#/ && index($1, "aos_model_route_checks_total{") == 1 && index($0, r) > 0 { s += $NF; n++ }
    END { if (n > 0) printf "%d", s }' <<<"${TXT}"
}

avaliar() {
  CLASSE=ok
  if ! TXT="$(metricas 2>/dev/null)" || [[ -z "${TXT}" ]]; then
    CLASSE=sem_leitura
    MOTIVO="as métricas não responderam — sem leitura (o alerta-ancora.sh avisa de nó ou collector em baixo)"
    return
  fi
  local iguais diferentes por_reportar
  iguais="$(soma igual)"; diferentes="$(soma diferente)"; por_reportar="$(soma nao_reportado)"
  if [[ -z "${iguais}" && -z "${diferentes}" && -z "${por_reportar}" ]]; then
    MOTIVO="a família aos_model_route_checks_total não existe — governação da rota desligada (AOS_MODEL_ROUTE_GOVERNANCE=off)"
    return
  fi
  iguais="${iguais:-0}"; diferentes="${diferentes:-0}"; por_reportar="${por_reportar:-0}"
  MOTIVO="turnos comparados desde o arranque do nó: ${iguais} iguais, ${diferentes} diferentes, ${por_reportar} não reportados"
  if (( diferentes > 0 )); then
    CLASSE=mau
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
  if notificar "AOS: teste do alerta da rota do modelo" default "white_check_mark" \
      "Teste do alerta da rota do modelo em ${HOST_ID}. Estado actual: ${CLASSE} — ${MOTIVO}."; then
    log "aviso de TESTE enviado (estado actual: ${CLASSE} — ${MOTIVO})"
    exit 0
  fi
  exit 1
fi

# Sem leitura não se mexe no estado: nem dispara, nem dá por recuperado.
if [[ "${CLASSE}" == sem_leitura ]]; then
  log "${MOTIVO}"
  exit 0
fi

mkdir -p "${ESTADO_DIR}" && chmod 700 "${ESTADO_DIR}"
exec 9>"${ESTADO_DIR}/lock"
flock -n 9 || { log "outra execução em curso — esta termina"; exit 0; }

disparado=0; ultimo_aviso=0
if [[ -f "${ESTADO_DIR}/estado" ]]; then
  read -r disparado ultimo_aviso < "${ESTADO_DIR}/estado" || true
fi
[[ "${disparado}" =~ ^[01]$ ]] || disparado=0
[[ "${ultimo_aviso}" =~ ^[0-9]+$ ]] || ultimo_aviso=0
agora="$(date +%s)"

if [[ "${CLASSE}" == mau ]]; then
  if [[ "${disparado}" == 0 ]]; then
    if notificar "AOS: rota do modelo em ALERTA" high "rotating_light" \
        "O proxy declarou uma rota que não é a do perfil — ${MOTIVO}. Host: ${HOST_ID}. Ver deploy/server/README.md, secção «Rota do modelo sob governação»."; then
      disparado=1; ultimo_aviso="${agora}"
      log "ALERTA enviado: ${MOTIVO}"
    fi
  elif (( agora - ultimo_aviso >= LEMBRETE_S )); then
    if notificar "AOS: rota do modelo continua em ALERTA" high "rotating_light" \
        "Há mais de 24 h: ${MOTIVO}. Host: ${HOST_ID}."; then
      ultimo_aviso="${agora}"
      log "LEMBRETE enviado: ${MOTIVO}"
    fi
  else
    log "em alerta (já avisado): ${MOTIVO}"
  fi
elif [[ "${disparado}" == 1 ]]; then
  if notificar "AOS: rota do modelo sem diferencas" default "white_check_mark" \
      "O contador de rotas diferentes voltou a zero (o nó reiniciou) — ${MOTIVO}. Host: ${HOST_ID}."; then
    disparado=0; ultimo_aviso="${agora}"
    log "RECUPERADO: ${MOTIVO}"
  fi
else
  log "ok: ${MOTIVO}"
fi

printf '%s %s\n' "${disparado}" "${ultimo_aviso}" > "${ESTADO_DIR}/estado.novo" \
  && mv -f "${ESTADO_DIR}/estado.novo" "${ESTADO_DIR}/estado"
