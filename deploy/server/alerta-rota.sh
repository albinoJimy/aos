#!/usr/bin/env bash
# alerta-rota.sh — avisa por push (ntfy) quando o modelo que o proxy declara ter servido deixa de
# ser o do perfil da rota, ou quando o proxy deixa de o declarar (AOS-505): duas regras, sobre
# `diferente` e sobre `nao_reportado` maiores do que zero, cada uma com o seu aviso.
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
# Duas regras INDEPENDENTES, cada uma com o seu aviso, o seu lembrete e a sua recuperação:
#   soma de result="diferente" > 0     o proxy declarou outro modelo ou outro endpoint, ou o nome
#                                      pedido não tem perfil: uma troca de CONFIGURAÇÃO no proxy
#   soma de result="nao_reportado" > 0 o proxy NÃO declarou o modelo ou o endpoint: outro proxy
#                                      à frente do provider, uma versão que renomeou ou desligou
#                                      os cabeçalhos — a comparação deixou de ter com que comparar.
#                                      Sem este aviso, um proxy trocado por outro que não emita
#                                      os cabeçalhos passava calado em `observe`
#
# O QUE NÃO DISPARA, e porquê:
#   família ausente                    a governação da rota está desligada — é o estado por omissão
#   métricas sem resposta              nó ou collector em baixo — é o alerta-ancora.sh que avisa
#
# Os contadores são por processo e só sobem: uma vez acima de zero, ficam até o nó reiniciar. Cada
# regra avisa à primeira leitura, relembra de 24 h em 24 h enquanto durar, e avisa quando volta a
# zero (o nó reiniciou; se a causa continuar, volta a disparar no turno seguinte). Uma regra em
# alerta não cala nem repete a outra: os estados são dois ficheiros.
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
  CLASSE=ok; MAU_DIFERENTE=0; MAU_NAO_REPORTADO=0
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
    MAU_DIFERENTE=1; CLASSE=diferente
  fi
  if (( por_reportar > 0 )); then
    MAU_NAO_REPORTADO=1
    if [[ "${CLASSE}" == diferente ]]; then CLASSE=diferente+nao_reportado; else CLASSE=nao_reportado; fi
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

agora="$(date +%s)"

# seguir <ficheiro de estado> <em alerta: 0|1> <rótulo do log> <título do alerta> <mensagem do
#        alerta> <título do lembrete> <título da recuperação> <mensagem da recuperação>
# Uma regra: avisa à primeira leitura em alerta, relembra de 24 h em 24 h, avisa quando recupera.
# Um aviso que não saiu não muda o estado: tenta-se na execução seguinte.
seguir() {
  local ficheiro="${ESTADO_DIR}/$1" mau="$2" rotulo="$3"
  local disparado=0 ultimo_aviso=0
  if [[ -f "${ficheiro}" ]]; then
    read -r disparado ultimo_aviso < "${ficheiro}" || true
  fi
  [[ "${disparado}" =~ ^[01]$ ]] || disparado=0
  [[ "${ultimo_aviso}" =~ ^[0-9]+$ ]] || ultimo_aviso=0

  if [[ "${mau}" == 1 ]]; then
    if [[ "${disparado}" == 0 ]]; then
      if notificar "$4" high "rotating_light" "$5"; then
        disparado=1; ultimo_aviso="${agora}"
        log "ALERTA enviado (${rotulo}): ${MOTIVO}"
      fi
    elif (( agora - ultimo_aviso >= LEMBRETE_S )); then
      if notificar "$6" high "rotating_light" "Há mais de 24 h: ${MOTIVO}. Host: ${HOST_ID}."; then
        ultimo_aviso="${agora}"
        log "LEMBRETE enviado (${rotulo}): ${MOTIVO}"
      fi
    else
      log "em alerta (${rotulo}, já avisado): ${MOTIVO}"
    fi
  elif [[ "${disparado}" == 1 ]]; then
    if notificar "$7" default "white_check_mark" "$8"; then
      disparado=0; ultimo_aviso="${agora}"
      log "RECUPERADO (${rotulo}): ${MOTIVO}"
    fi
  fi

  printf '%s %s\n' "${disparado}" "${ultimo_aviso}" > "${ficheiro}.novo" \
    && mv -f "${ficheiro}.novo" "${ficheiro}"
}

# O ficheiro `estado` é o da regra sobre `diferente` desde a primeira versão: mantém o nome.
seguir estado "${MAU_DIFERENTE}" "diferente" \
  "AOS: rota do modelo em ALERTA" \
  "O proxy declarou uma rota que não é a do perfil — ${MOTIVO}. Host: ${HOST_ID}. Ver deploy/server/README.md, secção «Rota do modelo sob governação»." \
  "AOS: rota do modelo continua em ALERTA" \
  "AOS: rota do modelo sem diferencas" \
  "O contador de rotas diferentes voltou a zero (o nó reiniciou) — ${MOTIVO}. Host: ${HOST_ID}."

seguir estado-nao-reportado "${MAU_NAO_REPORTADO}" "nao_reportado" \
  "AOS: rota do modelo NAO REPORTADA" \
  "O proxy não declarou o modelo ou o endpoint que serviu — a rota deixou de ser comparada. ${MOTIVO}. Host: ${HOST_ID}. Causas a ver: outro proxy à frente do provider, ou uma versão do LiteLLM que não emite os cabeçalhos. Não é o aviso de rota diferente. Ver deploy/server/README.md, secção «Rota do modelo sob governação»." \
  "AOS: rota do modelo continua NAO REPORTADA" \
  "AOS: rota do modelo voltou a ser reportada" \
  "O contador de rotas não reportadas voltou a zero (o nó reiniciou) — ${MOTIVO}. Host: ${HOST_ID}."

if [[ "${CLASSE}" == ok ]]; then
  log "ok: ${MOTIVO}"
fi
