#!/usr/bin/env bash
# drenar-planos.sh — drena a fila de pedidos de plano SEM OPERADOR (AOS-437).
#
#   corre-o o timer aos-drenar-planos (systemd/), 1 min depois de a drenagem anterior acabar (AOS-447)
#   à mão:  bash /opt/aos/drenar-planos.sh
#   durante um deploy sai 0 SEM drenar («drenagem ADIADA») — ver «O DEPLOY SEGURA A DRENAGEM» (AOS-450)
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
#
# ─── UM PLANO POR DRENAGEM, DE MINUTO A MINUTO (AOS-447) ────────────────────────────────────
# A forma do trabalhador decidida pelo dono: UM trabalhador (este timer), que volta 1 min depois de
# a drenagem anterior acabar, e cada drenagem consome UM pedido (`Environment=DRENAR_MAX=1` na
# unidade aos-drenar-planos.service). Até aqui eram 3 de 5 em 5 min: um pedido esperava até 5 min para começar, e um que chegasse atrás de dois planos
# longos esperava também por eles. Com 1, um pedido espera no máximo o plano em curso mais ~1 min, e
# o aviso do resultado (abaixo) sai quando ESTE plano acaba, não quando acabam os três. As
# re-verificações de planos à espera de humano não contam para o máximo (AOS-442).
#
# O 1 vive na UNIDADE e não aqui, de propósito: este script chega pelo rsync em cada deploy, mas o
# timer só muda quando o root reinstala as unidades. Com o 1 aqui, entre um e outro a fila drenava 1
# pedido de 5 em 5 min. Na unidade, o máximo e o intervalo mudam juntos; à mão, sem a variável,
# continua a ser 3.
#
# ─── O AVISO DO RESULTADO DE UM PLANO (AOS-445) ─────────────────────────────────────────────
# O `consume` imprime `aviso: run=<id> geracao=<g> classe=terminal codigo=<n>` DEPOIS de o nó ter
# registado um desfecho terminal. Esta drenagem recolhe essas linhas e, debaixo do lock dela,
# acrescenta-as ao OUTBOX ${AVISOS_DIR}/pendentes (600), que o avisar-planos.sh (cron do `aos`)
# consome e envia por ntfy ao operador, pseudonimizadas. Falhar a escrever o outbox NUNCA faz
# falhar a drenagem — os pedidos já foram drenados e reportados; perde-se o aviso, e di-lo o log,
# onde as linhas `aviso:` ficam.
#
# E CONFERE-SE a contagem: o delta de aos_orq_consume_desfechos_total{classe="terminal"} nas
# métricas desta drenagem tem de bater com as linhas `aviso:` lidas. Não bate quando o binário não
# as imprime (uma imagem anterior ao AOS-445, depois de um rollback — o par misturado do AOS-450)
# ou quando o reporte de um terminal falhou; nos dois casos há planos que terminaram sem aviso, e
# isso vai também para o outbox (`desencontro:`), para chegar ao operador pelo mesmo canal.

set -Eeuo pipefail
# O `[^[:space:]]` da AVISO_RE depende do locale: em C.UTF-8, um run_id com U+3000 (que o
# ValidarStreamID aceita) não casaria, e o submissor fazia desaparecer o aviso do seu plano. Em C,
# cada byte ≥ 0x80 conta como não-espaço — a mesma leitura do avisar-planos.sh (AOS-445).
export LC_ALL=C

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
# AOS-445: o outbox dos avisos, e a forma EXACTA da linha que o `consume` imprime — a mesma regex
# está no avisar-planos.sh, e o TestAOS445ContratoDaLinhaDoAvisoComOsScripts fixa as duas contra o
# `linhaDoAviso` do Go. O run_id vai com `+` e não com um tecto `{1,N}`: acima de 255 o regcomp de
# algumas libc recusa a regex, e o bash trata isso como «não casa» — em silêncio.
AVISOS_DIR="${DRENAR_AVISOS_DIR:-${AOS_DIR}/.avisos-planos}"
AVISO_RE='^aviso: run=[^[:space:]]+ geracao=[0-9]{1,9} classe=terminal codigo=[0-9]{1,3}$'
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

# ─── O DEPLOY SEGURA A DRENAGEM (AOS-450) ───────────────────────────────────────────────────
# O deploy.sh (e o rollback.sh, que corre por baixo dele) escreve ${DEPLOY_MARCADOR} ANTES de o CD
# sincronizar os scripts, e apaga-o depois de o nó estar saudável com a imagem nova — o desenho está
# no deploy.sh, «A DRENAGEM DA FILA». Enquanto ele for válido, esta drenagem sai 0 SEM reclamar
# nada: um par script/binário misturado, ou um nó a reiniciar a meio de um plano, não são falhas da
# fila, e um `failed` por eles era um alerta falso a cada release (medido na v0.1.35).
#
# O ADIAMENTO NÃO ESCREVE O CARIMBO ultima-ok: não drenou nada, e dizê-lo seria mentir ao sensor.
# Não é preciso: o alerta-nhi.sh só se queixa ao fim de 5 h sem sucesso, e um deploy adia a fila
# por minutos (até ~1 h, se esperar por uma drenagem longa).
#
# Válido é: `<epoch> <pid> <validade_s> <origem>` legível, com a idade dentro da validade (limitada
# a DRENAR_DEPLOY_MAX_S) e — com pid > 0 — esse pid vivo e a correr um deploy.sh. pid 0 é o anúncio
# do CD, que vale só pelo prazo. Tudo o resto é ÓRFÃO (um deploy que morreu sem trap): ignora-se, e
# apaga-se quando esta drenagem tem o lock — a fila nunca pára para sempre por um deploy morto.
# PRESSUPOSTO: o deploy e o timer correm como o mesmo `aos`. Com /proc montado com hidepid e um
# DEPLOY_USER diferente, o pid do deploy não se vê e o marcador passa por órfão.
DEPLOY_MARCADOR="${ESTADO_DIR}/deploy-em-curso"
DEPLOY_MAX_S="${DRENAR_DEPLOY_MAX_S:-14400}"
[[ "${DEPLOY_MAX_S}" =~ ^[1-9][0-9]{0,5}$ ]] || fail "DRENAR_DEPLOY_MAX_S='${DEPLOY_MAX_S}' inválido (segundos, inteiro)"

# deploy_em_curso <tenho_o_lock: 0|1> — 0 se um deploy válido está em curso (e põe-no em DEPLOY_DESC).
deploy_em_curso() {
  local ini="" pid="" validade="" origem="" idade cmd="" motivo
  # Sem zeros à esquerda: o bash leria `0900` como octal (e `09` rebenta a aritmética).
  local num='^(0|[1-9][0-9]*)$'
  DEPLOY_DESC=""
  [[ -e "${DEPLOY_MARCADOR}" ]] || return 1
  read -r ini pid validade origem < "${DEPLOY_MARCADOR}" 2>/dev/null || true
  if [[ "${ini}" =~ ${num} && ${#ini} -le 12 && "${pid}" =~ ${num} && ${#pid} -le 10 \
        && "${validade}" =~ ${num} && ${#validade} -le 6 ]]; then
    (( validade <= DEPLOY_MAX_S )) || validade="${DEPLOY_MAX_S}"
    idade=$(( $(date +%s) - ini ))
    # -300: um relógio acertado para trás logo depois de escrever não torna o marcador órfão.
    if (( idade < -300 )); then
      motivo="escrito ${idade#-}s no FUTURO — relógio ou marcador forjado"
    elif (( idade > validade )); then
      motivo="EXPIROU: tem ${idade}s e valia ${validade}s (pid ${pid})"
    elif (( pid == 0 )); then
      DEPLOY_DESC="anunciado pelo CD há ${idade}s, vale ${validade}s"
      return 0
    else
      cmd="$(tr '\0' ' ' 2>/dev/null < "/proc/${pid}/cmdline" || true)"
      if [[ "${cmd}" == *deploy.sh* ]]; then
        DEPLOY_DESC="deploy.sh pid ${pid} (${origem:-?}) há ${idade}s"
        return 0
      fi
      if [[ -z "${cmd}" ]]; then
        motivo="o deploy que o escreveu MORREU (pid ${pid} não existe)"
      else
        motivo="o pid ${pid} está vivo mas já não é um deploy.sh (o deploy morreu e o pid foi reutilizado)"
      fi
    fi
  else
    motivo="ilegível"
  fi
  log "marcador de deploy ÓRFÃO ignorado ($(head -c 80 "${DEPLOY_MARCADOR}" 2>/dev/null | tr -cd '[:alnum:] ')): ${motivo}"
  if [[ "$1" == 1 ]]; then rm -f "${DEPLOY_MARCADOR}"; fi
  return 1
}

adiar() {
  log "deploy em curso — drenagem ADIADA, nada reclamado (${DEPLOY_DESC}); o timer volta a tentar"
  exit 0
}

exec 9>"${ESTADO_DIR}/lock"
if ! flock -n 9; then
  # O lock ocupado é o deploy (que o segura até ao fim) ou outra drenagem. A segunda continua a
  # FALHAR: o systemd nunca arranca dois oneshot, por isso é uma corrida à mão ou um deploy que
  # perdeu o marcador — os dois querem-se visíveis.
  if deploy_em_curso 0; then adiar; fi
  fail "outra drenagem em curso (${ESTADO_DIR}/lock) — esta não reclama nada"
fi
# COM o lock na mão: o anúncio do CD não segura o lock (o rsync e o deploy.sh são ligações SSH
# distintas), e o deploy.sh que avançou sem ele (rollback) também não.
if deploy_em_curso 1; then adiar; fi

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

# carimbar — cada linha do `consume` para o stdout (journal) e, com carimbo, para o log. As linhas
# `aviso:` (AOS-445) vão também, tal e qual, para ${AVISOS_DESTA} — e uma falha aí não pára nada.
AVISOS_DESTA="${ESTADO_DIR}/avisos-desta-drenagem"
AVISOS_CONTAGEM="${ESTADO_DIR}/avisos-contagem"
carimbar() {
  local linha
  while IFS= read -r linha || [[ -n "${linha}" ]]; do
    printf '%s\n' "${linha}"
    printf '%s | %s\n' "$(carimbo)" "${linha}" >> "${LOG_FILE}"
    if [[ "${linha}" =~ ${AVISO_RE} ]]; then
      printf '%s\n' "${linha}" >> "${AVISOS_DESTA}" 2>/dev/null || true
    fi
  done
}

# ─── os avisos desta drenagem (AOS-445) ─────────────────────────────────────────────────────
# terminais_em <ficheiro .prom> — a soma de aos_orq_consume_desfechos_total{classe="terminal",…}
# (todos os códigos). Vazio se o ficheiro não existir: sem «antes» não há delta a conferir.
terminais_em() {
  [[ -s "$1" ]] || return 0
  awk 'index($1, "aos_orq_consume_desfechos_total{classe=\"terminal\",") == 1 { s += $2 } END { printf "%d\n", s }' "$1" 2>/dev/null || true
}
# nao_reportados_em <ficheiro .prom> — aos_orq_consume_desfechos_nao_reportados_total (0 se ausente).
nao_reportados_em() {
  [[ -s "$1" ]] || return 0
  awk '$1 == "aos_orq_consume_desfechos_nao_reportados_total" { s = $2 } END { printf "%d\n", s }' "$1" 2>/dev/null || true
}

# para_o_outbox <ficheiro> — acrescenta as linhas ao outbox, debaixo do lock do outbox (o
# avisar-planos.sh reescreve-o debaixo do mesmo). Devolve != 0 se não conseguiu; NUNCA sai.
para_o_outbox() {
  (
    umask 077
    mkdir -p "${AVISOS_DIR}" && chmod 700 "${AVISOS_DIR}" || exit 1
    exec 8>>"${AVISOS_DIR}/lock" || exit 1
    flock -w 30 8 || exit 1
    cat "$1" >> "${AVISOS_DIR}/pendentes" && chmod 600 "${AVISOS_DIR}/pendentes"
  ) 2>> "${LOG_FILE}"
}

# entregar_avisos <métricas desta drenagem: 0|1> — as linhas `aviso:` desta drenagem para o outbox,
# e a conferência com o delta de terminais nas métricas. Nada aqui faz falhar a drenagem.
entregar_avisos() {
  local n=0 depois nr_depois delta nr_delta
  if [[ -s "${AVISOS_DESTA}" ]]; then
    n="$(wc -l < "${AVISOS_DESTA}" 2>/dev/null | tr -d ' ' || true)"
    [[ "${n}" =~ ^[0-9]+$ ]] || n=0
  fi
  if (( n > 0 )); then
    if para_o_outbox "${AVISOS_DESTA}"; then
      log "${n} aviso(s) de plano terminado no outbox ${AVISOS_DIR}/pendentes (o avisar-planos.sh envia-os)"
    else
      log "AVISO: ${n} aviso(s) de plano terminado NÃO entraram no outbox ${AVISOS_DIR}/pendentes — ninguém será avisado destes planos; as linhas «aviso:» estão neste log"
    fi
  fi

  # A CONFERÊNCIA. Só com as métricas DESTA drenagem e um «antes» conferido; sem isso diz-se porquê.
  if [[ "$1" != 1 ]]; then
    log "avisos: contagem por conferir — sem as métricas desta drenagem (a seguinte também não confere)"
    rm -f "${AVISOS_DESTA}" 2>/dev/null || true
    return 0
  fi
  depois="$(terminais_em "${METRICAS}")"; nr_depois="$(nao_reportados_em "${METRICAS}")"
  printf '%s %s\n' "${depois:-0}" "${nr_depois:-0}" > "${AVISOS_CONTAGEM}" 2>/dev/null \
    || log "avisos: contagem NÃO guardada em ${AVISOS_CONTAGEM} — a drenagem seguinte não confere"
  if [[ -z "${TERMINAIS_ANTES}" ]]; then
    log "avisos: contagem por conferir nesta drenagem — sem contagem anterior (primeira com o AOS-445, ou a anterior não leu métricas suas)"
  else
    delta=$(( ${depois:-0} - TERMINAIS_ANTES )); nr_delta=$(( ${nr_depois:-0} - NAO_REPORTADOS_ANTES ))
    if (( delta < 0 || nr_delta < 0 )); then
      log "avisos: contagem por conferir — os contadores do consume recomeçaram (métricas ilegíveis?)"
    elif (( n != delta )); then
      # n < delta com nr_delta que o explique: terminais cujo reporte falhou. n < delta sem isso: um
      # binário que não imprime a linha, ou um `consume` corrido fora desta drenagem (conta no
      # volume e não passou por aqui). n > delta não tem causa conhecida — diz-se na mesma.
      if (( n < delta && delta - n <= nr_delta )); then
        log "AVISO: ${delta} desfecho(s) terminal(is) e só ${n} aviso(s) — o reporte de $(( delta - n )) falhou («NAO reportado» acima): se o nó o registou na mesma, o pedido FECHOU sem aviso; se não, volta à fila e avisa na geração seguinte"
      elif (( n < delta )); then
        log "AVISO: ${delta} desfecho(s) terminal(is) nas métricas e ${n} linha(s) «aviso:» — o aos-orq desta imagem não as imprime (anterior ao AOS-445, depois de um rollback?), ou um consume correu à mão desde a drenagem anterior; há planos que terminaram SEM aviso"
      else
        log "AVISO: ${n} linha(s) «aviso:» e só ${delta} desfecho(s) terminal(is) nas métricas — a contagem não bate, e não há causa conhecida para isto"
      fi
      # `em=` (o início desta drenagem) torna a linha única: o avisar-planos.sh envia cada uma só uma vez.
      printf 'desencontro: terminais=%d avisos=%d em=%d\n' "${delta}" "${n}" "${INICIO}" > "${AVISOS_DESTA}.desencontro" 2>/dev/null \
        && para_o_outbox "${AVISOS_DESTA}.desencontro" \
        || log "AVISO: o desencontro também NÃO entrou no outbox"
      rm -f "${AVISOS_DESTA}.desencontro" 2>/dev/null || true
    fi
  fi
  rm -f "${AVISOS_DESTA}" 2>/dev/null || true
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

# AS SOBRAS DE UMA DRENAGEM INTERROMPIDA (AOS-445). Uma drenagem morta entre a linha `aviso:` e o
# entregar_avisos (SIGKILL, reinício do host) deixa ${AVISOS_DESTA} para trás. Entrega-se agora,
# antes de qualquer outra verificação — apagá-lo perdia esses avisos sem desencontro nenhum (a
# drenagem morta também não deixou contagem). Um aviso que já tivesse entrado no outbox não se
# repete: o avisar-planos.sh regista os enviados por run.
if [[ -s "${AVISOS_DESTA}" ]]; then
  if para_o_outbox "${AVISOS_DESTA}"; then
    log "$(wc -l < "${AVISOS_DESTA}" | tr -d ' ') aviso(s) de uma drenagem INTERROMPIDA entregue(s) ao outbox"
    rm -f "${AVISOS_DESTA}"
  else
    log "AVISO: os avisos de uma drenagem interrompida (${AVISOS_DESTA}) NÃO entraram no outbox — ficam para a próxima"
  fi
fi

[[ -s "${AOS_DIR}/orq/snapshot.json" ]] || fail "sem ${AOS_DIR}/orq/snapshot.json — o planeador precisa do instantâneo de validação"

EXP="$(nhi_exp || true)"
[[ "${EXP}" =~ ^[0-9]+$ ]] || fail "sem NHI legível em ${AOS_DIR}/nhi/nhi-run.jwt — a cunhagem (aos-cunhar-nhi) não correu ou falhou; NÃO se reclama nenhum plano"
RESTA=$(( EXP - $(date +%s) ))
(( RESTA >= NHI_MIN_S )) || fail "o NHI caduca em ${RESTA}s (mínimo ${NHI_MIN_S}s) — a cunhagem parou; NÃO se reclama nenhum plano"
# Um NHI não vive mais de 1h (tecto da biblioteca); um prazo maior é um ficheiro que não saiu do
# emissor — e um número gigante dá a volta na aritmética do bash sem aviso.
(( RESTA <= 3900 )) || fail "o NHI diz que vive ${RESTA}s — acima do tecto de 1h; não é um NHI do emissor, NÃO se reclama nenhum plano"

log "drenagem a começar (máximo ${MAX} pedidos; NHI com ${RESTA}s de vida)"
# AOS-445: o «antes» da conferência dos avisos é o que a última drenagem CONFERIDA deixou. Apaga-se
# já: se esta drenagem não chegar a ler métricas suas, a seguinte não confere contra um «antes»
# velho (daria um desencontro falso com os avisos que esta entregou).
TERMINAIS_ANTES=""; NAO_REPORTADOS_ANTES=""
read -r TERMINAIS_ANTES NAO_REPORTADOS_ANTES 2>/dev/null < "${AVISOS_CONTAGEM}" || true
[[ "${TERMINAIS_ANTES}" =~ ^[0-9]{1,12}$ && "${NAO_REPORTADOS_ANTES}" =~ ^[0-9]{1,12}$ ]] \
  || { TERMINAIS_ANTES=""; NAO_REPORTADOS_ANTES=""; }
rm -f "${AVISOS_CONTAGEM}" 2>/dev/null || true
# O que ainda lá estiver (as sobras acima não entraram no outbox) junta-se aos desta drenagem.
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

# metricas_desta_drenagem — 0 se a cópia em ${METRICAS} traz o carimbo desta drenagem.
metricas_desta_drenagem() {
  FIM_METRICAS="$(awk '$1 == "aos_orq_consume_ultima_drenagem_timestamp_seconds" { print $2 }' "${METRICAS}" 2>/dev/null || true)"
  [[ "${FIM_METRICAS}" =~ ^[0-9]+$ ]] && (( FIM_METRICAS >= INICIO ))
}

if (( RC != 0 )); then
  # As métricas desta drenagem falhada também contam (o `consume` escreve-as mesmo a falhar); sem
  # elas é só menos informação, e o erro que importa é o de cima. Os avisos TAMBÉM se entregam: um
  # `consume` que falha no 2.º pedido já reportou o 1.º.
  frescas=0
  if copiar_metricas; then
    metricas_desta_drenagem && frescas=1
  else
    log "métricas do consume NÃO copiadas de ${ORQ_VOLUME}"
  fi
  entregar_avisos "${frescas}"
  fail "o consume saiu com ${RC} — ver acima ou ${LOG_FILE} (AOS_ORQ_NODE_URL / AOS_ORQ_OIDC_* no .env? o nó responde?)"
fi

# MÉTRICAS DESTA DRENAGEM, OU FALHA. O sensor lê a cópia; uma cópia parada diria «tudo bem» sobre
# desfechos que ninguém contou. Prova-se que é DESTA drenagem pelo carimbo que o `consume` lá põe.
# Os avisos entregam-se ANTES de falhar: os pedidos foram drenados e reportados na mesma.
if ! copiar_metricas; then
  entregar_avisos 0
  fail "métricas do consume NÃO copiadas de ${ORQ_VOLUME}:/${METRICAS_NO_VOLUME} para ${METRICAS}"
fi
if ! metricas_desta_drenagem; then
  entregar_avisos 0
  fail "as métricas em ${METRICAS} não são desta drenagem (fim=${FIM_METRICAS:-?}, início=${INICIO}) — o consume não as escreveu (imagem anterior ao AOS-443, depois de um rollback? os pedidos foram drenados na mesma)"
fi
entregar_avisos 1

# CARIMBO DE SUCESSO, para o sensor: um timer parado ou nunca instalado fica `inactive` e não
# `failed`, e só a idade deste carimbo o denuncia (revisão do AOS-437, achado M2).
date +%s > "${ESTADO_DIR}/ultima-ok.novo" && mv -f "${ESTADO_DIR}/ultima-ok.novo" "${ESTADO_DIR}/ultima-ok"
log "drenagem terminada (máximo ${MAX} pedidos; NHI com ${RESTA}s de vida; métricas em ${METRICAS})"
