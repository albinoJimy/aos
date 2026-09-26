#!/usr/bin/env bash
# drenar-planos.sh — drena a fila de pedidos de plano SEM OPERADOR (AOS-437).
#
#   corre-o o timer aos-drenar-planos (systemd/), a cada 5 min
#   à mão:  bash /opt/aos/drenar-planos.sh
#
# O AOS-430 mediu que NADA drenava a fila em produção: o serviço `aos-orq` é `restart: "no"` e o
# `consume` drena uma vez e termina. Este é o «uma vez», repetido. Nunca há duas drenagens em
# simultâneo: o systemd não volta a arrancar um serviço oneshot que ainda está activo, e o WAL do
# `consume` (/var/lib/aos-orq/consume.wal) pede posse sequencial.
#
# RECUSA RECLAMAR SEM CREDENCIAL. Reclamar um pedido e falhar a seguir gasta uma geração por nada
# e fecha o pedido como falhado — por isso, com o NHI ausente ou a menos de NHI_MIN_S do fim, não
# se reclama coisa nenhuma, e a falha fica em `systemctl --failed` (que o sensor lê).
#
# ─── O QUE FICA LEGÍVEL SEM ROOT (AOS-443) ──────────────────────────────────────────────────
# O stdout do timer vai para o journal do SISTEMA, que o `aos` não lê: para ver o que aconteceu
# num plano era preciso segurar o lock desta drenagem e correr o `consume` à mão. Agora, em
# ${LOG_DIR} (do `aos`, 0750):
#
#   drenar-planos.log         tudo o que esta drenagem e o `consume` escrevem, com carimbo UTC.
#                             Roda-o ESTE script (o logrotate exigiria root): quando passa de
#                             DRENAR_LOG_MAX_BYTES, .log → .log.1 → … → .log.DRENAR_LOG_GERACOES.
#   aos-orq-consume.prom      as métricas do `consume` (formato de texto Prometheus, contadores
#                             acumulados — ver packages/cmd/aos-orq/metricas_do_consumo.go),
#                             copiadas do volume do `aos-orq` no fim de cada drenagem. É o que o
#                             alerta-nhi.sh lê para avisar de desfechos falhados seguidos.
#
# O LOG NÃO LEVA O OBJECTIVO DO PEDIDO. É dado do titular, e um ficheiro em claro não é alcançado
# pelo apagamento DSAR. Não se filtra aqui: tirou-se da ORIGEM (o `consume` imprime
# `objectivo_bytes=N`), o que fecha também o journal — um filtro neste script seria uma lista
# negra sobre texto livre, que deixa passar a próxima linha que alguém acrescente. O que o log leva
# são ids (run, nós, planos), contagens, códigos, hashes e durações.

set -Eeuo pipefail

AOS_DIR="${AOS_DIR:-/opt/aos}"
MAX="${DRENAR_MAX:-3}"
NHI_MIN_S="${DRENAR_NHI_MIN_S:-600}"
LOG_DIR="${DRENAR_LOG_DIR:-${AOS_DIR}/logs}"
LOG_FILE="${LOG_DIR}/drenar-planos.log"
LOG_MAX_BYTES="${DRENAR_LOG_MAX_BYTES:-5242880}"
LOG_GERACOES="${DRENAR_LOG_GERACOES:-5}"
# O volume do `aos-orq` pelo nome que o compose lhe dá (projecto `aos`) — o mesmo do backup.sh.
ORQ_VOLUME="${DRENAR_ORQ_VOLUME:-aos_aos-orq-data}"
# O `consume` escreve as métricas ao lado do WAL (/var/lib/aos-orq/), pelo caminho POR OMISSÃO. A
# flag que o escolhe NÃO se passa daqui, de propósito: o deploy sincroniza os scripts antes de
# trocar a imagem, e o rollback repõe a imagem sem repor os scripts — um script novo com um binário
# anterior ao AOS-443 recusaria a flag desconhecida, e a fila parava. Sem ela, esse par drena na
# mesma; só a verificação de frescura das métricas, abaixo, falha (ruidosa, depois de drenar).
METRICAS_NO_VOLUME="aos-orq-consume.prom"
METRICAS="${LOG_DIR}/aos-orq-consume.prom"
ALPINE="alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
COMPOSE=(docker compose -f "${AOS_DIR}/docker-compose.prod.yml" --env-file "${AOS_DIR}/.env"
         --env-file "${AOS_DIR}/image.env")
INICIO="$(date +%s)"

umask 027
# Antes do log() existir: sem a pasta não há para onde escrever, e o erro tem de o dizer.
if ! mkdir -p "${LOG_DIR}" || ! chmod 750 "${LOG_DIR}"; then
  logger -t aos-drenar-planos "ERRO: pasta de log ${LOG_DIR} impossível de criar ou de restringir" 2>/dev/null || true
  printf '[drenar-planos] ERRO: pasta de log %s impossível de criar ou de restringir (dono? disco?)\n' "${LOG_DIR}" >&2
  exit 1
fi

carimbo() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log()  {
  logger -t aos-drenar-planos "$1" 2>/dev/null || true
  printf '[drenar-planos] %s\n' "$1"
  printf '%s [drenar-planos] %s\n' "$(carimbo)" "$1" >> "${LOG_FILE}" 2>/dev/null || true
}
fail() { log "ERRO: $1"; exit 1; }

[[ "${LOG_MAX_BYTES}" =~ ^[0-9]+$ ]] && (( LOG_MAX_BYTES >= 4096 )) \
  || fail "DRENAR_LOG_MAX_BYTES='${LOG_MAX_BYTES}' inválido (inteiro ≥ 4096)"
[[ "${LOG_GERACOES}" =~ ^[0-9]+$ ]] && (( LOG_GERACOES >= 1 && LOG_GERACOES <= 50 )) \
  || fail "DRENAR_LOG_GERACOES='${LOG_GERACOES}' inválido (1..50)"

# UMA drenagem de cada vez, também contra uma corrida À MÃO: o systemd só impede duas pelo timer.
ESTADO_DIR="${AOS_DIR}/.drenagem"
mkdir -p "${ESTADO_DIR}" && chmod 700 "${ESTADO_DIR}"
exec 9>"${ESTADO_DIR}/lock"
flock -n 9 || fail "outra drenagem em curso (${ESTADO_DIR}/lock) — esta não reclama nada"

# rodar_log — só DEBAIXO DO LOCK: duas drenagens a rodar ao mesmo tempo perdiam uma geração. Roda
# no início de cada drenagem, pelo que um ficheiro passa do tecto no máximo pelo output de uma.
rodar_log() {
  local tam i
  [[ -f "${LOG_FILE}" ]] || return 0
  tam="$(wc -c < "${LOG_FILE}")"
  (( tam >= LOG_MAX_BYTES )) || return 0
  rm -f "${LOG_FILE}.${LOG_GERACOES}"
  for (( i = LOG_GERACOES - 1; i >= 1; i-- )); do
    if [[ -f "${LOG_FILE}.${i}" ]]; then
      mv -f "${LOG_FILE}.${i}" "${LOG_FILE}.$(( i + 1 ))"
    fi
  done
  mv -f "${LOG_FILE}" "${LOG_FILE}.1"
  return 0
}
rodar_log

# carimbar — cada linha do `consume` para o stdout (journal) e, com carimbo, para o log.
carimbar() {
  local linha
  while IFS= read -r linha || [[ -n "${linha}" ]]; do
    printf '%s\n' "${linha}"
    printf '%s | %s\n' "$(carimbo)" "${linha}" >> "${LOG_FILE}"
  done
}

# copiar_metricas — o ficheiro vive no volume do `aos-orq` (escreve-o o uid 65532, que é quem lá
# escreve); lê-se como 65532, sem rede, e substitui-se a cópia do `aos` de forma atómica.
# O stderr do docker vai para o ficheiro de log (é a razão de uma cópia falhada), e um `.novo` a
# meio não fica.
copiar_metricas() {
  if docker run --rm --pull=never --log-driver none --network none --read-only --cap-drop ALL \
       --security-opt no-new-privileges --user 65532:65532 -v "${ORQ_VOLUME}":/d:ro "${ALPINE}" \
       cat "/d/${METRICAS_NO_VOLUME}" > "${METRICAS}.novo" 2>> "${LOG_FILE}" \
     && [[ -s "${METRICAS}.novo" ]] && chmod 640 "${METRICAS}.novo" && mv -f "${METRICAS}.novo" "${METRICAS}"; then
    return 0
  fi
  rm -f "${METRICAS}.novo"
  return 1
}

# nhi_exp — o `exp` de TOPO do NHI, lido como o uid 65532 (a pasta é dele e 0700), sem rede.
# Ancora em `"exp":N,"jti"` porque o mandato embebido também tem `exp` e só o de topo é seguido de
# `jti`; a forma está fixada por TestAOS437OExpDeTopoESeguidoDoJti (packages/cmd/aos-issuer).
nhi_exp() {
  docker run --rm --pull=never --log-driver none --network none --read-only --cap-drop ALL \
    --security-opt no-new-privileges --user 65532:65532 -v "${AOS_DIR}/nhi":/n:ro "${ALPINE}" sh -c '
      [ -s /n/nhi-run.jwt ] || exit 3
      p=$(cut -d. -f2 /n/nhi-run.jwt | tr "_-" "/+")
      case $(( ${#p} % 4 )) in 2) p="$p==";; 3) p="$p=";; esac
      printf %s "$p" | base64 -d 2>/dev/null | sed -n "s/.*\"exp\":\([0-9]*\),\"jti\".*/\1/p"'
}

[[ -s "${AOS_DIR}/orq/snapshot.json" ]] || fail "sem ${AOS_DIR}/orq/snapshot.json — o planeador precisa do instantâneo de validação"

EXP="$(nhi_exp || true)"
[[ "${EXP}" =~ ^[0-9]+$ ]] || fail "sem NHI legível em ${AOS_DIR}/nhi/nhi-run.jwt — a cunhagem (aos-cunhar-nhi) não correu ou falhou; NÃO se reclama nenhum plano"
RESTA=$(( EXP - $(date +%s) ))
(( RESTA >= NHI_MIN_S )) || fail "o NHI caduca em ${RESTA}s (mínimo ${NHI_MIN_S}s) — a cunhagem parou; NÃO se reclama nenhum plano"
# Um NHI não vive mais de 1h (tecto da biblioteca); um prazo maior é um ficheiro que não saiu do
# emissor — e um número gigante dá a volta na aritmética do bash sem aviso.
(( RESTA <= 3900 )) || fail "o NHI diz que vive ${RESTA}s — acima do tecto de 1h; não é um NHI do emissor, NÃO se reclama nenhum plano"

log "drenagem a começar (máximo ${MAX} pedidos; NHI com ${RESTA}s de vida)"
# -T e </dev/null: o `compose run` come o stdin de quem o chama (lição do AOS-403). O stderr junta-se
# ao stdout para chegar também ao log; o código de saída é o do `compose`, não o do `carimbar`.
set +e
"${COMPOSE[@]}" --profile orq run --rm -T \
  -e AOS_ORQ_NODE_CREDENTIAL_FILE=/run/aos-nhi/nhi-run.jwt \
  aos-orq consume --snapshot /etc/aos-orq/snapshot.json --wal /var/lib/aos-orq/consume.wal \
  --max "${MAX}" </dev/null 2>&1 | carimbar
ESTADOS=("${PIPESTATUS[@]}")
set -e
RC="${ESTADOS[0]}"

if (( RC != 0 )); then
  # As métricas desta drenagem falhada também contam (o `consume` escreve-as mesmo a falhar); sem
  # elas é só menos informação, e o erro que importa é o de cima.
  copiar_metricas || log "métricas do consume NÃO copiadas de ${ORQ_VOLUME}"
  fail "o consume saiu com ${RC} — ver acima ou ${LOG_FILE} (AOS_ORQ_NODE_URL / AOS_ORQ_OIDC_* no .env? o nó responde?)"
fi

# MÉTRICAS DESTA DRENAGEM, OU FALHA. O sensor lê a cópia; uma cópia parada diria «tudo bem» sobre
# desfechos que ninguém contou. Prova-se que é DESTA drenagem pelo carimbo que o `consume` lá põe.
copiar_metricas || fail "métricas do consume NÃO copiadas de ${ORQ_VOLUME}:/${METRICAS_NO_VOLUME} para ${METRICAS}"
FIM_METRICAS="$(awk '$1 == "aos_orq_consume_ultima_drenagem_timestamp_seconds" { print $2 }' "${METRICAS}")"
[[ "${FIM_METRICAS}" =~ ^[0-9]+$ ]] && (( FIM_METRICAS >= INICIO )) \
  || fail "as métricas em ${METRICAS} não são desta drenagem (fim=${FIM_METRICAS:-?}, início=${INICIO}) — o consume não as escreveu (imagem anterior ao AOS-443, depois de um rollback? os pedidos foram drenados na mesma)"

# CARIMBO DE SUCESSO, para o sensor: um timer parado ou nunca instalado fica `inactive` e não
# `failed`, e só a idade deste carimbo o denuncia (revisão do AOS-437, achado M2).
date +%s > "${ESTADO_DIR}/ultima-ok.novo" && mv -f "${ESTADO_DIR}/ultima-ok.novo" "${ESTADO_DIR}/ultima-ok"
log "drenagem terminada (máximo ${MAX} pedidos; NHI com ${RESTA}s de vida; métricas em ${METRICAS})"
