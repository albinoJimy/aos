#!/usr/bin/env bash
# avisar-planos.sh — avisa o OPERADOR, por push (ntfy), de cada plano da fila que TERMINA (AOS-445).
#
#   cron do `aos`:   * * * * * /bin/bash /opt/aos/avisar-planos.sh >/dev/null 2>&1
#   à mão:           bash /opt/aos/avisar-planos.sh                     (envia o que estiver pendente)
#                    bash /opt/aos/avisar-planos.sh --teste             (aviso de TESTE; não mexe no estado)
#                    bash /opt/aos/avisar-planos.sh --pseudonimo <run>  (o pseudónimo de um run, para cruzar)
#
# ─── DE ONDE VÊM OS AVISOS ──────────────────────────────────────────────────────────────────
# O `aos-orq consume` imprime `aviso: run=<id> geracao=<g> classe=terminal codigo=<n>` DEPOIS de o
# nó ter registado o desfecho terminal de um plano; o drenar-planos.sh recolhe essas linhas e
# acrescenta-as ao OUTBOX ${AVISOS_DIR}/pendentes (600). Este script consome o outbox. Um envio que
# falha FICA no outbox e tenta-se na execução seguinte; um envio feito regista-se em
# ${AVISOS_DIR}/enviados. Duas peças e não uma porque a drenagem não pode esperar pela rede do ntfy
# nem falhar por ela: os pedidos já foram drenados.
#
# ─── O QUE SAI DO SERVIDOR ──────────────────────────────────────────────────────────────────
# SÓ o id PSEUDONIMIZADO do plano — os 12 primeiros hex de HMAC-SHA256(chave, run_id) —, a classe e
# o código. Nunca o run_id (escolhe-o quem submete, e pode trazer o que quiser), nunca o objectivo, o
# resultado ou o tipo do erro.
#
# PORQUÊ HMAC E NÃO sha256(run_id): um run_id é previsível (`plan-e2e-<epoch>`, o nome de um
# cliente…), e um sha256 sem chave inverte-se por dicionário — e é o prefixo do nome do documento
# do plano no volume (`planos/<sha256>.plan.json`, AOS-442). Com a chave ${CHAVE_FILE} (64 hex, só
# no servidor), o pseudónimo não se inverte sem ela. SEM CHAVE O SCRIPT NÃO ENVIA (fail-closed): os
# avisos ficam no outbox. O HMAC é calculado aqui, em bash com o sha256sum — a chave nunca vai
# para a linha de comando de outro processo.
#
# O operador cruza o pseudónimo com o log: ${LOG_FILE} tem uma linha por aviso com o run_id ao lado;
# e `--pseudonimo <run>` dá o pseudónimo de um run conhecido (README §Aviso do resultado de um plano).
#
#   código 0        «ok»         prioridade default
#   código 7        «recusado»   prioridade high   (houve decisão e foi NÃO — ou um pendente fora do prazo)
#   outro código    «falhou»     prioridade high
#
# O TÓPICO É OUTRO: ${TOPICO_FILE}, separado do dos alertas de infraestrutura (secrets/ntfy-topico),
# para que o fim de um plano não se confunda com um alarme — e o script RECUSA enviar se os dois
# forem o mesmo.
#
# ─── NO MÁXIMO UM AVISO POR PLANO ───────────────────────────────────────────────────────────
# O registo de enviados é por RUN, e não por (run, geração). O `consume` só imprime a linha depois
# de o nó aceitar o desfecho, e um desfecho terminal fecha o pedido; mas um reporte cuja resposta se
# perdeu (o nó registou, o `consume` viu erro) seguido de outra geração daria dois fins para o mesmo
# plano. O segundo NÃO é avisado: diz-se neste log, com a geração e o código de cada um (resíduo
# declarado no AOS-445).
#
# O outbox pode trazer também `desencontro: terminais=<n> avisos=<m> em=<epoch>` — a drenagem contou
# desfechos terminais nas métricas que não vieram com a linha `aviso:`. Vai como aviso próprio, só
# com as contagens, e também só uma vez (o `em=` é o início da drenagem que o viu).
set -uo pipefail
# O `[^[:space:]]` da regex depende do locale: em C.UTF-8, um run_id com U+3000 (que o
# ValidarStreamID aceita) não casaria, e o aviso desse plano desaparecia. Em C, cada byte ≥ 0x80
# conta como não-espaço — a mesma leitura do drenar-planos.sh, seja qual for o locale do cron.
export LC_ALL=C
AOS_DIR="${AOS_DIR:-/opt/aos}"
AVISOS_DIR="${AOS_AVISO_PLANOS_DIR:-${AOS_DIR}/.avisos-planos}"
TOPICO_FILE="${AOS_AVISO_PLANOS_TOPICO_FILE:-${AOS_DIR}/secrets/ntfy-topico-planos}"
TOPICO_INFRA_FILE="${AOS_ALERTA_TOPICO_FILE:-${AOS_DIR}/secrets/ntfy-topico}"
CHAVE_FILE="${AOS_AVISO_PLANOS_CHAVE_FILE:-${AOS_DIR}/secrets/aviso-planos-hmac.key}"
NTFY_BASE="${AOS_ALERTA_NTFY_BASE:-https://ntfy.sh}"
LOG_DIR="${AOS_AVISO_PLANOS_LOG_DIR:-${AOS_DIR}/logs}"
LOG_FILE="${LOG_DIR}/avisar-planos.log"
LOG_MAX_BYTES=1048576
# Quantos avisos por execução: com o cron de minuto a minuto, um outbox acumulado (ntfy em baixo
# durante horas) sai aos poucos e não num jorro que o limite do ntfy.sh cortaria a meio.
MAX_POR_EXECUCAO="${AOS_AVISO_PLANOS_MAX:-20}"
[[ "${MAX_POR_EXECUCAO}" =~ ^[1-9][0-9]{0,2}$ ]] || MAX_POR_EXECUCAO=20
# Quanto tempo se guarda um enviado. A duplicação que o registo trava vive dentro do TTL da
# reclamação (30 min); 30 dias é folga larga, e o ficheiro não cresce sem fim.
RETER_S=2592000
# A forma EXACTA da linha — a mesma do drenar-planos.sh; o TestAOS445ContratoDaLinhaDoAvisoComOsScripts
# fixa as duas contra o `linhaDoAviso` do Go.
AVISO_RE='^aviso: run=[^[:space:]]+ geracao=[0-9]{1,9} classe=terminal codigo=[0-9]{1,3}$'
DESENCONTRO_RE='^desencontro: terminais=([0-9]{1,9}) avisos=([0-9]{1,9}) em=([0-9]{1,12})$'

umask 077
mkdir -p "${LOG_DIR}" 2>/dev/null || true

log() {
  logger -t aos-avisar-planos "$1" 2>/dev/null || true
  printf '[avisar-planos] %s\n' "$1"
  printf '%s [avisar-planos] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" >> "${LOG_FILE}" 2>/dev/null || true
}

# ler_chave — põe em CHAVE os 64 hex de ${CHAVE_FILE}; != 0 se não houver chave válida.
ler_chave() {
  CHAVE="$(tr -d ' \r\n' 2>/dev/null < "${CHAVE_FILE}" || true)"
  [[ "${CHAVE}" =~ ^[0-9a-fA-F]{64}$ ]]
}

# hmac_sha256 <chave hex, 32 bytes> <mensagem> — HMAC-SHA256 em hex (RFC 2104), com o sha256sum e os
# builtins do bash: a chave não sai para o argv de nenhum processo. O cenário do AOS-445 confere-o
# contra o vector do RFC 4231.
hmac_sha256() {
  local k="$1" ipad="" opad="" i b interno
  while (( ${#k} < 128 )); do k+="0"; done   # a chave é preenchida com zeros até ao bloco de 64 bytes
  for (( i = 0; i < 128; i += 2 )); do
    b=$(( 16#${k:i:2} ))
    ipad+="$(printf '\\x%02x' $(( b ^ 0x36 )))"
    opad+="$(printf '\\x%02x' $(( b ^ 0x5c )))"
  done
  # shellcheck disable=SC2059
  interno="$( { printf "${ipad}"; printf '%s' "$2"; } | sha256sum | cut -d' ' -f1)"
  # shellcheck disable=SC2059
  { printf "${opad}"; printf "$(sed 's/../\\x&/g' <<<"${interno}")"; } | sha256sum | cut -d' ' -f1
}

# notificar <título ASCII> <prioridade> <tags> <mensagem> — devolve != 0 se o aviso não saiu. O molde
# do alerta-nhi.sh, com o tópico dos PLANOS.
notificar() {
  local topico infra
  # `2>/dev/null` ANTES do `<`: sem o ficheiro, o erro do redireccionamento sairia para o cron.
  topico="$(tr -d ' \r\n' 2>/dev/null < "${TOPICO_FILE}" || true)"
  if [[ ! "${topico}" =~ ^[A-Za-z0-9_-]{16,64}$ ]]; then
    log "sem tópico ntfy válido em ${TOPICO_FILE} — o aviso NÃO saiu e fica pendente: $1"
    return 1
  fi
  infra="$(tr -d ' \r\n' 2>/dev/null < "${TOPICO_INFRA_FILE}" || true)"
  if [[ "${topico}" == "${infra}" ]]; then
    log "o tópico dos planos (${TOPICO_FILE}) é o MESMO dos alertas de infraestrutura (${TOPICO_INFRA_FILE}) — recusado; o aviso fica pendente: $1"
    return 1
  fi
  if ! curl -fsS --max-time 20 -H "Title: $1" -H "Priority: $2" -H "Tags: $3" \
      --data-binary "$4" "${NTFY_BASE}/${topico}" >/dev/null 2>&1; then
    log "o envio para o ntfy FALHOU — fica pendente para a execução seguinte: $1"
    return 1
  fi
}

# rodar_log — uma geração só: este log tem uma linha por aviso.
rodar_log() {
  local tam
  [[ -f "${LOG_FILE}" ]] || return 0
  tam="$(wc -c < "${LOG_FILE}" 2>/dev/null || echo 0)"
  (( tam >= LOG_MAX_BYTES )) && mv -f "${LOG_FILE}" "${LOG_FILE}.1" 2>/dev/null
  return 0
}

case "${1:-}" in
  --teste)
    ler_chave || { log "sem chave HMAC válida em ${CHAVE_FILE} (64 hex) — os avisos não sairiam; crie-a (README §Aviso do resultado de um plano)"; exit 1; }
    if notificar "AOS: teste do aviso de planos" default "white_check_mark" \
        "Teste do aviso do resultado de um plano. Assim chega um plano: 'plano 0123456789ab: terminal, codigo 0 (ok)'."; then
      log "aviso de TESTE enviado para o tópico dos planos"
      exit 0
    fi
    exit 1 ;;
  --pseudonimo)
    [[ -n "${2:-}" ]] || { log "uso: avisar-planos.sh --pseudonimo <run_id>"; exit 2; }
    ler_chave || { log "sem chave HMAC válida em ${CHAVE_FILE}"; exit 1; }
    h="$(hmac_sha256 "${CHAVE}" "$2")"; printf '%s\n' "${h:0:12}"
    exit 0 ;;
  "") ;;
  *) log "argumento desconhecido: $1 (uso: avisar-planos.sh [--teste | --pseudonimo <run>])"; exit 2 ;;
esac

[[ -d "${AVISOS_DIR}" ]] || exit 0   # nenhuma drenagem entregou avisos ainda
exec 7>"${AVISOS_DIR}/lock-avisar" || { log "impossível abrir ${AVISOS_DIR}/lock-avisar"; exit 1; }
flock -n 7 || { log "outra execução em curso — esta termina"; exit 0; }
rodar_log

PENDENTES="${AVISOS_DIR}/pendentes"
ENVIADOS="${AVISOS_DIR}/enviados"
[[ -s "${PENDENTES}" ]] || exit 0
# FAIL-CLOSED: sem a chave, nada sai — nem com um pseudónimo mais fraco.
if ! ler_chave; then
  log "sem chave HMAC válida em ${CHAVE_FILE} (64 hex) — NENHUM aviso sai; ficam no outbox ($(wc -l < "${PENDENTES}" | tr -d ' ') linha(s))"
  exit 1
fi

# O outbox lê-se e reescreve-se debaixo do lock que a drenagem usa para acrescentar; o ENVIO não, para
# a drenagem não esperar pela rede. A drenagem só acrescenta — as K primeiras linhas lidas são, no
# fim, as K primeiras do ficheiro.
exec 8>>"${AVISOS_DIR}/lock" || { log "impossível abrir ${AVISOS_DIR}/lock"; exit 1; }
flock -w 30 8 || { log "o outbox está preso há 30 s (uma drenagem a escrever?) — tenta-se no minuto seguinte"; exit 0; }
mapfile -t LINHAS < <(head -n "${MAX_POR_EXECUCAO}" "${PENDENTES}")
flock -u 8
K="${#LINHAS[@]}"
(( K > 0 )) || exit 0

# ja_enviado <chave> — imprime o que o registo diz desse envio (vazio se nunca foi enviado).
ja_enviado() { awk -v h="$1" '$2 == h { print "geracao " $3 ", codigo " $4; exit }' "${ENVIADOS}" 2>/dev/null || true; }
registar() {
  printf '%s %s %s %s\n' "${agora}" "$1" "$2" "$3" >> "${ENVIADOS}" \
    || log "enviado mas NÃO registado em ${ENVIADOS} — seria avisado outra vez"
}

agora="$(date +%s)"
FICAM=()
n_env=0; n_dup=0; n_inv=0; n_fic=0
for (( i = 0; i < K; i++ )); do
  linha="${LINHAS[i]}"
  enviou=1
  if [[ "${linha}" =~ ${AVISO_RE} ]]; then
    resto="${linha#aviso: run=}"
    run="${resto%% geracao=*}"
    resto="${resto#* geracao=}"; geracao="${resto%% *}"
    codigo="${linha##*codigo=}"
    h="$(hmac_sha256 "${CHAVE}" "${run}")"; p="${h:0:12}"
    anterior="$(ja_enviado "${h}")"
    if [[ -n "${anterior}" ]]; then
      log "plano ${p} (run=${run}) TERMINOU OUTRA VEZ (geracao ${geracao}, codigo ${codigo}) — NÃO avisado: já foi avisado (${anterior}); no máximo um aviso por plano"
      n_dup=$(( n_dup + 1 ))
      continue
    fi
    case "${codigo}" in
      0) rotulo="ok";       prio=default; tags="white_check_mark"; titulo="AOS: plano terminado (ok)" ;;
      7) rotulo="recusado"; prio=high;    tags="no_entry";         titulo="AOS: plano recusado (codigo 7)" ;;
      *) rotulo="falhou";   prio=high;    tags="x";                titulo="AOS: plano falhou (codigo ${codigo})" ;;
    esac
    if notificar "${titulo}" "${prio}" "${tags}" "Plano ${p}: terminal, código ${codigo} (${rotulo})."; then
      registar "${h}" "${geracao}" "${codigo}"
      log "enviado: plano ${p} run=${run} geracao=${geracao} codigo=${codigo} (${rotulo})"
      n_env=$(( n_env + 1 ))
    else
      enviou=0
    fi
  elif [[ "${linha}" =~ ${DESENCONTRO_RE} ]]; then
    terminais="${BASH_REMATCH[1]}"; avisos="${BASH_REMATCH[2]}"
    # Também só uma vez: a chave é a própria linha (o `em=` distingue duas drenagens).
    h="$(printf '%s' "${linha}" | sha256sum | cut -d' ' -f1)"
    if [[ -n "$(ja_enviado "${h}")" ]]; then
      log "desencontro já avisado (${linha}) — não se repete"
      n_dup=$(( n_dup + 1 ))
      continue
    fi
    if notificar "AOS: planos terminados SEM aviso" high "warning" \
        "Uma drenagem contou ${terminais} desfecho(s) terminal(is) e só ${avisos} aviso(s): há planos que terminaram sem aviso. Ver /opt/aos/logs/drenar-planos.log (linhas AVISO)."; then
      registar "${h}" "-" "desencontro"
      log "enviado: desencontro (terminais=${terminais} avisos=${avisos})"
      n_env=$(( n_env + 1 ))
    else
      enviou=0
    fi
  else
    # Uma linha que não tem a forma não se envia nem se guarda: ficava no outbox para sempre. O que ela
    # dizia está no drenar-planos.log.
    log "linha do outbox com forma desconhecida DESCARTADA ($(printf '%s' "${linha}" | head -c 80 | tr -cd '[:alnum:] :=._~-'))"
    n_inv=$(( n_inv + 1 ))
  fi
  if (( ! enviou )); then
    # PÁRA NA PRIMEIRA FALHA: com o ntfy em baixo, cada tentativa custa até 20 s, e as seguintes
    # falhariam igual. Esta e as que faltam ficam no outbox, pela mesma ordem.
    FICAM+=("${linha}"); n_fic=$(( K - i ))
    K=$(( i + 1 ))
    break
  fi
done

# Reescrita: os que ficam, à frente do que falta e do que a drenagem acrescentou entretanto (a ordem
# de chegada mantém-se).
if ! flock -w 30 8; then
  log "o outbox está preso há 30 s — os ${K} avisos processados NÃO foram tirados dele; os enviados não se repetem (registo de enviados)"
  exit 1
fi
if { (( ${#FICAM[@]} == 0 )) || printf '%s\n' "${FICAM[@]}"; } > "${PENDENTES}.novo" \
    && tail -n "+$(( K + 1 ))" "${PENDENTES}" >> "${PENDENTES}.novo" \
    && chmod 600 "${PENDENTES}.novo" && mv -f "${PENDENTES}.novo" "${PENDENTES}"; then
  :
else
  rm -f "${PENDENTES}.novo"
  log "o outbox NÃO foi reescrito — os ${K} avisos processados continuam lá; os enviados não se repetem (registo de enviados)"
fi
# Os enviados com mais de RETER_S saem (debaixo do mesmo lock: só este script lhes toca).
if [[ -s "${ENVIADOS}" ]]; then
  awk -v lim="$(( agora - RETER_S ))" '$1 >= lim' "${ENVIADOS}" > "${ENVIADOS}.novo" \
    && mv -f "${ENVIADOS}.novo" "${ENVIADOS}" || rm -f "${ENVIADOS}.novo"
fi
flock -u 8
log "outbox: ${n_env} enviado(s), ${n_fic} pendente(s) por falha de envio (parou na primeira), ${n_dup} repetido(s) não avisado(s), ${n_inv} descartado(s)"
(( n_fic == 0 ))
