#!/usr/bin/env bash
# alerta-disjuntor.sh — avisa por push (ntfy) quando o disjuntor do modelo candidato ABRE, e, com o
# nó em observação, quando TERIA ABERTO (AOS-521, ADR-041 §2.9). No molde do alerta-rota.sh.
#
#   cron do `aos`:   */15 * * * * /bin/bash /opt/aos/alerta-disjuntor.sh >/dev/null 2>&1
#   à mão:           bash /opt/aos/alerta-disjuntor.sh            (avalia e mostra o estado)
#                    bash /opt/aos/alerta-disjuntor.sh --teste    (envia um aviso de TESTE, não mexe no estado)
#
# ─── O QUE LÊ ───────────────────────────────────────────────────────────────────────────────
# As famílias `aos_model_breaker_*` do `/metrics` do nó, que só existem com `AOS_MODEL_BREAKER` em
# `observe` ou `enforce` E um candidato a servir. Os valores são os da última leitura do livro
# durável por essa réplica (o arranque, cada run novo e cada run do candidato que acaba).
#
# ─── O QUE DISPARA ──────────────────────────────────────────────────────────────────────────
# Três regras INDEPENDENTES, cada uma com o seu aviso, o seu lembrete e a sua recuperação:
#   aos_model_breaker_open = 1          o disjuntor ABRIU: o candidato deixou de receber runs novos.
#                                       Não volta sozinho — reabrir é assinar um registo novo
#   aos_model_breaker_would_open = 1    com aos_model_breaker_open = 0: o nó está em observação e a
#     e aos_model_breaker_open = 0      regra disparou — é o que `enforce` teria feito. A fatia não
#                                       mudou; o candidato continua a receber
#   aos_model_breaker_unrecorded_total  o nó NÃO conseguiu gravar no livro uma observação ou a marca
#     > 0                               de aberto. É a via pela qual falhas reais não abrem o
#                                       disjuntor, e pela qual um corte feito em `enforce` deixa de
#                                       valer ao mudar para `off` ou `observe`. O contador é por
#                                       processo: volta a zero quando o nó reinicia
#
# O QUE NÃO DISPARA, e porquê:
#   famílias ausentes                   o disjuntor está desligado, ou o nó não serve candidato — é o
#                                       estado por omissão. NÃO se dá por recuperado: um disjuntor
#                                       aberto não fecha por se desligar a variável, e a marca de
#                                       aberto continua a valer (o arranque do nó avisa). ATENÇÃO:
#                                       com `off` este script NÃO VÊ NADA — nem o disjuntor aberto,
#                                       nem escritas por gravar. Confirmar a marca antes de desligar
#   métricas sem resposta               nó ou collector em baixo — é o alerta-ancora.sh que avisa
#
# Cada regra avisa à primeira leitura, relembra de 24 h em 24 h enquanto durar, e avisa quando volta
# a zero — o que, nas duas primeiras, só acontece com um registo de entrada novo em vigor. Os
# estados são três ficheiros.
#
# O QUE SAI DO SERVIDOR: título, as contagens (falhas e unidades da janela, e os sete sinais) e o
# nome do host. Nenhum nome de modelo, nenhum digest, nenhum endereço. O tópico é o segredo (o mesmo
# do alerta-ancora.sh).
set -uo pipefail

AOS_DIR="${AOS_DIR:-/opt/aos}"
ESTADO_DIR="${AOS_ALERTA_DISJUNTOR_ESTADO_DIR:-${AOS_DIR}/.alerta-disjuntor}"
TOPICO_FILE="${AOS_ALERTA_TOPICO_FILE:-${AOS_DIR}/secrets/ntfy-topico}"
NTFY_BASE="${AOS_ALERTA_NTFY_BASE:-https://ntfy.sh}"
# Só para ENSAIO: um URL de métricas directo. Em produção lê-se pela rede interna do compose.
METRICAS_URL="${AOS_ALERTA_METRICAS_URL:-}"
ALPINE="alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
LEMBRETE_S=86400
HOST_ID="$(hostname 2>/dev/null || echo servidor)"

log() {
  logger -t aos-alerta-disjuntor "$1" 2>/dev/null || true
  printf '[alerta-disjuntor] %s\n' "$1"
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

# valor <família sem rótulos> — o valor da amostra, em $TXT. Com mais de uma réplica no collector
# fica o MAIOR (basta uma ver o disjuntor aberto). Vazio se a família não existe.
valor() {
  awk -v f="$1" '
    $0 !~ /^#/ && ($1 == f || index($1, f "{") == 1) { v = $NF + 0; if (n == 0 || v > m) m = v; n++ }
    END { if (n > 0) printf "%d", m }' <<<"${TXT}"
}

# sinal <nome> — o valor de aos_model_breaker_signals para esse sinal (o maior entre réplicas).
sinal() {
  awk -v r="signal=\"$1\"" '
    $0 !~ /^#/ && index($1, "aos_model_breaker_signals{") == 1 && index($0, r) > 0 { v = $NF + 0; if (n == 0 || v > m) m = v; n++ }
    END { if (n > 0) printf "%d", m; else printf "0" }' <<<"${TXT}"
}

avaliar() {
  CLASSE=ok; MAU_ABERTO=0; MAU_TERIA=0; MAU_POR_GRAVAR=0
  if ! TXT="$(metricas 2>/dev/null)" || [[ -z "${TXT}" ]]; then
    CLASSE=sem_leitura
    MOTIVO="as métricas não responderam — sem leitura (o alerta-ancora.sh avisa de nó ou collector em baixo)"
    return
  fi
  local aberto teria falhas unidades por_gravar
  aberto="$(valor aos_model_breaker_open)"; teria="$(valor aos_model_breaker_would_open)"
  if [[ -z "${aberto}" && -z "${teria}" ]]; then
    CLASSE=ausente
    MOTIVO="as famílias aos_model_breaker_* não existem — disjuntor desligado (AOS_MODEL_BREAKER=off) ou nó sem candidato a servir"
    return
  fi
  aberto="${aberto:-0}"; teria="${teria:-0}"
  falhas="$(valor aos_model_breaker_window_failures)"; unidades="$(valor aos_model_breaker_window_units)"
  por_gravar="$(valor aos_model_breaker_unrecorded_total)"; por_gravar="${por_gravar:-0}"
  MOTIVO="janela: ${falhas:-0} falha(s) em ${unidades:-0} unidade(s); sinais: nao_cumprido $(sinal nao_cumprido), resposta_vazia $(sinal resposta_vazia), resposta_rejeitada $(sinal resposta_rejeitada), erro_do_provider $(sinal erro_do_provider), modelo_servido_diferente $(sinal modelo_servido_diferente), nao_convergiu $(sinal nao_convergiu), falha_nao_classificada $(sinal falha_nao_classificada); escritas por gravar: ${por_gravar}"
  if (( aberto > 0 )); then
    MAU_ABERTO=1; CLASSE=aberto
  elif (( teria > 0 )); then
    MAU_TERIA=1; CLASSE=teria_aberto
  fi
  if (( por_gravar > 0 )); then
    MAU_POR_GRAVAR=1
    if [[ "${CLASSE}" == ok ]]; then CLASSE=por_gravar; else CLASSE="${CLASSE}+por_gravar"; fi
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
  if notificar "AOS: teste do alerta do disjuntor do candidato" default "white_check_mark" \
      "Teste do alerta do disjuntor do modelo candidato em ${HOST_ID}. Estado actual: ${CLASSE} — ${MOTIVO}."; then
    log "aviso de TESTE enviado (estado actual: ${CLASSE} — ${MOTIVO})"
    exit 0
  fi
  exit 1
fi

# Sem leitura, ou com as famílias ausentes, não se mexe no estado: nem dispara, nem dá por recuperado.
if [[ "${CLASSE}" == sem_leitura || "${CLASSE}" == ausente ]]; then
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

seguir aberto "${MAU_ABERTO}" "aberto" \
  "AOS: disjuntor do candidato ABERTO" \
  "O disjuntor do modelo candidato abriu: o candidato deixou de receber runs novos — ${MOTIVO}. Host: ${HOST_ID}. Não volta sozinho. Ver deploy/server/README.md, secção «O disjuntor do candidato»." \
  "AOS: disjuntor do candidato continua ABERTO" \
  "AOS: disjuntor do candidato fechado" \
  "O disjuntor do modelo candidato está fechado para o registo em vigor (um registo de entrada novo) — ${MOTIVO}. Host: ${HOST_ID}."

seguir teria-aberto "${MAU_TERIA}" "teria_aberto" \
  "AOS: disjuntor do candidato TERIA ABERTO" \
  "Em observação: a regra do disjuntor do modelo candidato disparou — ${MOTIVO}. Host: ${HOST_ID}. A fatia NÃO mudou; com AOS_MODEL_BREAKER=enforce o candidato deixava de receber runs novos. Ver deploy/server/README.md, secção «O disjuntor do candidato»." \
  "AOS: disjuntor do candidato continua em TERIA ABERTO" \
  "AOS: disjuntor do candidato sem disparo em observacao" \
  "A regra do disjuntor do modelo candidato já não está disparada em observação (um registo novo em vigor, ou o disjuntor abriu em enforce) — ${MOTIVO}. Host: ${HOST_ID}."

seguir por-gravar "${MAU_POR_GRAVAR}" "por_gravar" \
  "AOS: disjuntor do candidato com escritas POR GRAVAR" \
  "O nó não conseguiu gravar no livro uma observação do candidato ou a marca do disjuntor — ${MOTIVO}. Host: ${HOST_ID}. Falhas reais podem não estar a contar, e um corte feito em enforce pode não ter a marca: NÃO mudar AOS_MODEL_BREAKER para off ou observe sem confirmar a marca no livro. Ver deploy/server/README.md, secção «O disjuntor do candidato»." \
  "AOS: disjuntor do candidato continua com escritas POR GRAVAR" \
  "AOS: disjuntor do candidato sem escritas por gravar" \
  "O contador de escritas por gravar do disjuntor voltou a zero (o nó reiniciou) — ${MOTIVO}. Host: ${HOST_ID}. Confirmar no arranque do nó o estado do disjuntor."

if [[ "${CLASSE}" == ok ]]; then
  log "ok: ${MOTIVO}"
fi
