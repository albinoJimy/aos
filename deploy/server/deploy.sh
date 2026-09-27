#!/usr/bin/env bash
# =============================================================================
# deploy.sh — troca o nó `aos` para uma imagem nova. Corre como `aos`, no servidor.
#
#   bash /opt/aos/deploy.sh ghcr.io/albinojimy/aos-node@sha256:<digest>
#
# Um deploy é, por construção, a troca de UM digest. Isso dá três propriedades que um
# `docker compose pull && up` à mão não dá:
#   · REPRODUTÍVEL — a referência é imutável (digest, não tag móvel: `:latest` pode apontar
#     para outra coisa amanhã e o mesmo comando entregaria outro binário);
#   · REVERSÍVEL   — o digest anterior fica em image.env.prev, e rollback.sh volta a ele;
#   · VERIFICÁVEL  — a saída só é verde depois de o nó ficar `healthy` E de o edge responder
#     em TLS. Um `up -d` que devolve 0 prova apenas que o docker aceitou o pedido.
#
# FAIL-CLOSED com REVERSÃO AUTOMÁTICA: se o `compose up` falhar, se o nó não ficar saudável ou se
# o smoke falhar, este script repõe o digest anterior e sai != 0. Nunca deixa o servidor num estado
# que ninguém escolheu.
#
# Variáveis opcionais:
#   GHCR_USER / GHCR_TOKEN   login efémero no registry (o token é revogado ao fim do job de CD)
#   HEALTH_TIMEOUT           segundos a esperar pelo healthy (default 180)
#   NO_ROLLBACK=1            desliga a reversão automática (para depurar um arranque falhado)
#   DEPLOY_ESPERA_DRENAGEM_S tecto da espera por uma drenagem da fila em curso (default 2700)
#   DEPLOY_AO_DESISTIR_DA_DRENAGEM  abortar (default) | avancar — o que fazer quando esse tecto passa
#
#   bash -s -- --anunciar < deploy.sh   modo ANÚNCIO (AOS-450): o CD corre-o ANTES do rsync dos
#                                       scripts; ver «A DRENAGEM DA FILA» abaixo.
# =============================================================================
set -euo pipefail

APP_DIR="${APP_DIR:-/opt/aos}"
COMPOSE_FILE="${APP_DIR}/docker-compose.prod.yml"
ENV_FILE="${APP_DIR}/.env"
IMAGE_ENV="${APP_DIR}/image.env"
IMAGE_ENV_PREV="${APP_DIR}/image.env.prev"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-180}"
# Tecto da espera por PRONTIDÃO (passo 6), separado do de LIVENESS (passo 5) porque medem coisas
# diferentes: o 5 espera que o processo responda, o 6 espera que ele aceite servir. Um Vault
# recriado sobe SELADO e o watchdog destrava-o em até 30s — a espera tem de absorver essa janela.
READY_TIMEOUT="${READY_TIMEOUT:-90}"

IMAGE_REF="${1:-}"

log()  { printf '\033[36m[deploy]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[deploy] FAIL:\033[0m %s\n' "$*" >&2; exit 1; }

dc() { docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" --env-file "${IMAGE_ENV}" "$@"; }

# --- A DRENAGEM DA FILA DE PLANOS: o deploy segura-a (AOS-450) ---------------------------------
# O timer `aos-drenar-planos` corre o drenar-planos.sh 1 min depois da drenagem anterior (AOS-447),
# e o CD sincroniza os scripts (rsync) ANTES de este script trocar a imagem. Medido no deploy da
# v0.1.35: uma drenagem calhou entre os dois e correu o script NOVO com o binário ANTIGO — falhou
# a verificação das métricas do AOS-443 e a unidade ficou `failed`. E uma drenagem a meio do
# `compose up` vê o nó reiniciar e os pedidos que tinha a meio falharem.
#
# POR QUE UM MARCADOR E NÃO SÓ O LOCK. O lock da drenagem é um `flock`, e um `flock` só vive
# enquanto um processo o detém — mas o rsync e este script são DUAS ligações SSH do runner. Nenhum
# processo atravessa as duas. Por isso há duas peças:
#   · o MARCADOR ${MARCADOR_DEPLOY} — «um deploy anunciou a troca»; o drenar-planos.sh, ao vê-lo
#     válido, sai 0 com «drenagem adiada» sem reclamar nada. Linha: `<epoch> <pid> <validade_s> <origem>`;
#   · o LOCK — a espera por uma drenagem que JÁ corria (o marcador não a pára) e, neste script, a
#     posse até ao fim: fecha a janela do `compose up` também contra uma drenagem à mão.
# O CD escreve o marcador com o modo --anunciar ANTES do rsync (o script vai por stdin, do checkout
# — o que está no servidor ainda é o da release anterior). Este script, ao arrancar, assume-o com o
# SEU pid; e apaga-o à saída, seja qual for o desfecho.
#
# PORQUE NÃO SÓ MUDAR A ORDEM (rsync dos scripts depois da troca): trocava a janela «script novo,
# binário antigo» pela inversa, e não protegia nada durante o reinício do nó.
#
# O QUE ISTO NÃO FECHA: um deploy que FALHE depois do rsync (passos 0/0b, pull, desistência no 0c,
# reversão automática) deixa scripts novos com o binário antigo e larga o marcador — a drenagem
# seguinte corre esse par (o `failed` falso da v0.1.35). O par misturado só deixa de acontecer num
# deploy BEM-SUCEDIDO. Fechá-lo também nos falhados pede o rsync para uma pasta de preparação e a
# instalação dos scripts por este script, debaixo do lock, depois do nó saudável (AOS-450, Estado).
#
# UM DEPLOY QUE MORRE não pára a fila: o marcador de um pid morto, ou de pid 0 (o anúncio) com o
# prazo passado, é ÓRFÃO para o drenar-planos.sh, que o ignora e apaga; e o flock morre com o
# processo — e com os filhos que o herdaram (um `docker` a meio de um deploy morto por SIGKILL
# segura-o até acabar: a drenagem desse intervalo falha como «outra drenagem», transitório).
DRENAGEM_DIR="${APP_DIR}/.drenagem"
DRENAGEM_LOCK="${DRENAGEM_DIR}/lock"
MARCADOR_DEPLOY="${DRENAGEM_DIR}/deploy-em-curso"
# Um plano pode durar até 40 min (--plan-timeout); 45 min cobre UM plano a meio, que é o que uma
# drenagem leva desde o AOS-447 (DRENAR_MAX=1 na unidade). Com mais, uma drenagem de vários planos
# compridos pode passar disto — é o caso de desistir.
ESPERA_DRENAGEM_S="${DEPLOY_ESPERA_DRENAGEM_S:-2700}"
AO_DESISTIR="${DEPLOY_AO_DESISTIR_DA_DRENAGEM:-abortar}"
# Quanto vale o anúncio depois de tomado o lock: o rsync e o arranque deste script. Se o deploy
# não chegar a correr (rsync falhado, job cancelado), as drenagens retomam sozinhas ao fim disto.
# ENQUANTO ESPERA pela drenagem em curso, o anúncio vale a espera MAIS isto (1 h por omissão). Um
# job cancelado a meio da espera pode deixá-lo assim: sem pty o processo remoto não é sinalizado de
# forma fiável, e se morrer (ou não renovar) o marcador fica com essa validade — a fila adia no
# máximo esse tempo, muito abaixo das 5 h do alerta.
ANUNCIO_VALIDADE_S=900
# Quanto vale o marcador deste script depois de tomado o lock: o pull, o up, o healthy e o smoke. É
# um TECTO contra a reutilização do pid — enquanto vale, o drenar-planos.sh exige ainda que o pid
# seja um deploy.sh vivo.
DEPLOY_VALIDADE_S=3600

# Sem zeros à esquerda: o bash lê `0900` como octal e a aritmética rebenta.
[[ "${ESPERA_DRENAGEM_S}" =~ ^(0|[1-9][0-9]{0,5})$ ]] \
  || fail "DEPLOY_ESPERA_DRENAGEM_S='${ESPERA_DRENAGEM_S}' inválido (segundos, inteiro)"
case "${AO_DESISTIR}" in
  abortar|avancar) : ;;
  *) fail "DEPLOY_AO_DESISTIR_DA_DRENAGEM='${AO_DESISTIR}' inválido (abortar | avancar)" ;;
esac

# marcar_deploy <pid> <validade_s> <origem> — escreve o marcador de forma atómica.
marcar_deploy() {
  mkdir -p "${DRENAGEM_DIR}" && chmod 700 "${DRENAGEM_DIR}" \
    && printf '%s %s %s %s\n' "$(date +%s)" "$1" "$2" "$3" > "${MARCADOR_DEPLOY}.novo" \
    && mv -f "${MARCADOR_DEPLOY}.novo" "${MARCADOR_DEPLOY}"
}

# esperar_drenagem — toma o lock da drenagem no fd 8, esperando até ESPERA_DRENAGEM_S por uma que
# esteja em curso. 0 = tomado (e fica tomado até este processo sair).
esperar_drenagem() {
  local t0
  exec 8>"${DRENAGEM_LOCK}" || return 1
  if flock -n 8; then
    log "     nenhuma drenagem em curso — lock tomado"
    return 0
  fi
  log "     uma drenagem da fila está EM CURSO — à espera que termine (tecto ${ESPERA_DRENAGEM_S}s) ..."
  t0="${SECONDS}"
  if flock -w "${ESPERA_DRENAGEM_S}" 8 </dev/null; then
    log "     a drenagem terminou ao fim de $(( SECONDS - t0 ))s — lock tomado"
    return 0
  fi
  return 1
}

# libertar_drenagem — à saída: apaga o marcador se ainda for DESTE processo, e larga o lock (um filho
# que tivesse herdado o fd 8 e sobrevivesse seguraria o lock por nós).
libertar_drenagem() {
  local pid=""
  read -r _ pid _ < "${MARCADOR_DEPLOY}" 2>/dev/null || true
  if [ "${pid}" = "$$" ]; then rm -f "${MARCADOR_DEPLOY}"; fi
  flock -u 8 2>/dev/null || true
  return 0
}

# MODO ANÚNCIO — o CD corre-o antes do rsync: `ssh aos@host 'bash -s -- --anunciar' < deploy.sh`.
# Nada do que vem depois deste bloco corre neste modo, e ele não lê o stdin (que é o próprio script).
if [ "${IMAGE_REF}" = "--anunciar" ]; then
  log "anúncio (AOS-450): as drenagens da fila de planos ficam ADIADAS até o deploy.sh terminar"
  marcar_deploy 0 "$(( ESPERA_DRENAGEM_S + ANUNCIO_VALIDADE_S ))" anuncio \
    || fail "impossível escrever ${MARCADOR_DEPLOY} — NADA foi sincronizado nem trocado"
  if ! esperar_drenagem; then
    rm -f "${MARCADOR_DEPLOY}"
    fail "desisti de esperar pela drenagem em curso ao fim de ${ESPERA_DRENAGEM_S}s — NADA foi sincronizado nem trocado, e as drenagens retomam. Repita o deploy (o mesmo digest) quando ela acabar: ${APP_DIR}/logs/drenar-planos.log"
  fi
  # O prazo conta a partir de AGORA: a espera acima não pode ter gasto o tempo do rsync.
  marcar_deploy 0 "${ANUNCIO_VALIDADE_S}" anuncio \
    || fail "impossível renovar ${MARCADOR_DEPLOY} — NADA foi sincronizado nem trocado"
  log "anúncio feito: sem drenagens até o deploy.sh assumir (ou ${ANUNCIO_VALIDADE_S}s, se ele não chegar a correr)"
  exit 0
fi

[ -n "${IMAGE_REF}" ] || fail "uso: deploy.sh <image-ref>  (ex.: ghcr.io/albinojimy/aos-node@sha256:...)"
[ -s "${COMPOSE_FILE}" ] || fail "${COMPOSE_FILE} ausente — sincroniza deploy/server/ para ${APP_DIR}"
[ -s "${ENV_FILE}" ]     || fail "${ENV_FILE} ausente — corre provision.sh primeiro"

# A porta do edge vem do .env: com AOS_EDGE_PORT alterado, um smoke fixo em 8443 testaria uma
# porta que ninguém publicou e daria vermelho num deploy verde (ou pior, o contrário).
# shellcheck disable=SC1090
set -a; . "${ENV_FILE}"; set +a
EDGE_PORT="${AOS_EDGE_PORT:-8444}"

# Uma tag móvel entregue como se fosse uma versão é a forma mais comum de um rollback "bem
# sucedido" repor exactamente o binário partido. Avisa, não bloqueia (dev pode querê-lo).
case "${IMAGE_REF}" in
  *@sha256:*) : ;;
  *) log "⚠️ ${IMAGE_REF} não é um digest — o rollback deixa de ser garantido (tags são móveis)." ;;
esac

# --- 0. Bundle PDP presente? ------------------------------------------------------------------
# Sem bundle o nó ABORTA no arranque. Melhor dizê-lo agora do que depois de derrubar o que corre.
[ -s "${APP_DIR}/policies/aos_authz.cedar" ] && [ -s "${APP_DIR}/policies/aos_authz.sig" ] \
  || fail "bundle PDP ausente em ${APP_DIR}/policies — o nó abortaria o arranque (AOS_POLICY_BUNDLE_DIR)"

# --- 0b. Âncora do WORM coerente? --------------------------------------------------------------
# A verificação ancorada é FAIL-CLOSED POR PARTIÇÃO: uma âncora que não bata com o WORM impede o
# nó de SERVIR. Se as três variáveis estão ligadas, os ficheiros TÊM de estar cá — e é melhor
# dizê-lo agora do que descobrir depois de derrubar o que estava a correr.
#
# Os directórios criam-se sempre, ligada ou não a verificação: o compose monta-os, e um bind-mount
# de origem ausente deixa o Docker criar o que lhe apetecer.
#
# As variáveis vêm da shell e NÃO de um `grep` ao ficheiro: o `.env` já foi carregado acima
# (`set -a; . "${ENV_FILE}"`). Grepar um ficheiro já lido seria frágil por nada — um padrão
# errado nunca casaria, o guarda nunca dispararia, e diria «DESLIGADA» sobre um nó com a âncora
# ligada. Um guarda que não pode falhar em voz alta é pior do que não ter guarda.
mkdir -p "${APP_DIR}/ancoras" "${APP_DIR}/pisos"
# `orq/` é a pasta de entradas do serviço `aos-orq` (AOS-403: snapshot e plan-doc). Pela mesma
# razão: sem ela, o primeiro `--profile orq run` deixava o Docker criá-la como root.
mkdir -p "${APP_DIR}/orq"
if [ -n "${AOS_WORM_TRUST_ANCHOR:-}" ]; then
  [ -s "${APP_DIR}/ancoras/checkpoints.json" ] \
    || fail "AOS_WORM_TRUST_ANCHOR está ligada mas ${APP_DIR}/ancoras/checkpoints.json não existe (ou está vazio) — o nó abortaria no arranque. Corra a selagem e entregue o ficheiro ANTES de ligar a âncora"
  [ -s "${APP_DIR}/pisos/heads.json" ] \
    || fail "AOS_WORM_TRUST_ANCHOR está ligada mas ${APP_DIR}/pisos/heads.json não existe (ou está vazio) — o piso de frescura é obrigatório: sem ele a âncora seria aceite SEM frescura, que é o no-op que a verificação existe para impedir"
  # As TRÊS ou NENHUMA — a mesma regra que o nó impõe, dita aqui antes de custar uma paragem.
  [ -n "${AOS_WORM_CHECKPOINT_FILE:-}" ] \
    || fail "AOS_WORM_TRUST_ANCHOR ligada sem AOS_WORM_CHECKPOINT_FILE — algumas das três aborta o arranque (ErrWormAnchorIncomplete)"
  [ -n "${AOS_WORM_EXPECTED_HEADS_FILE:-}" ] \
    || fail "AOS_WORM_TRUST_ANCHOR ligada sem AOS_WORM_EXPECTED_HEADS_FILE — algumas das três aborta o arranque (ErrWormAnchorIncomplete)"
  log "0b/6 âncora do WORM ligada: checkpoints e pisos presentes."
else
  log "0b/6 âncora do WORM DESLIGADA (AOS_WORM_TRUST_ANCHOR vazia) — só re-encadeamento, sem truncatura do tail nem reescrita da génese."
fi

# --- 0c. Segurar a drenagem da fila (AOS-450) ------------------------------------------------------
# Antes de tocar em qualquer coisa, e até ao fim: o marcador (com o pid deste processo) adia as
# drenagens do timer, e o lock espera pela que estiver a correr. O deploy do CD já o anunciou antes
# do rsync e já esperou; o rollback.sh e um deploy à mão chegam aqui sem anúncio.
#
# AO DESISTIR DE ESPERAR, o deploy ABORTA (fail-closed) e o rollback AVANÇA:
#   · um deploy não é urgente — adiá-lo não perde nada, e interromper um plano a meio fecha o
#     pedido como falhado (gasta uma geração) e o utilizador vê-o;
#   · um rollback é a saída de emergência de um nó partido AGORA, e a drenagem que corre contra ele
#     provavelmente já está a falhar; esperar 45 min para a proteger deixava o nó partido a servir.
#     O rollback.sh pede-o com DEPLOY_AO_DESISTIR_DA_DRENAGEM=avancar e uma espera mais curta.
log "0c/6 a segurar a drenagem da fila de planos (${DRENAGEM_LOCK}) ..."
marcar_deploy "$$" "$(( ESPERA_DRENAGEM_S + DEPLOY_VALIDADE_S ))" deploy \
  || fail "impossível escrever ${MARCADOR_DEPLOY} — o stack NÃO foi tocado"
trap libertar_drenagem EXIT
if esperar_drenagem; then
  marcar_deploy "$$" "${DEPLOY_VALIDADE_S}" deploy \
    || fail "impossível renovar ${MARCADOR_DEPLOY} — o stack NÃO foi tocado"
  log "     as drenagens do timer ficam ADIADAS até este deploy terminar"
elif [ "${AO_DESISTIR}" = "avancar" ]; then
  log "⚠️ desisti de esperar pela drenagem em curso ao fim de ${ESPERA_DRENAGEM_S}s — AVANÇO (DEPLOY_AO_DESISTIR_DA_DRENAGEM=avancar): os pedidos que ela tem a meio podem falhar quando o nó reiniciar. As drenagens seguintes continuam adiadas."
else
  fail "desisti de esperar pela drenagem em curso ao fim de ${ESPERA_DRENAGEM_S}s — o stack NÃO foi tocado (DEPLOY_AO_DESISTIR_DA_DRENAGEM=abortar). Repita o deploy quando ela acabar: ${APP_DIR}/logs/drenar-planos.log"
fi

# --- 1. Login efémero no registry (se fornecido) ------------------------------------------------
LOGGED_IN=0
if [ -n "${GHCR_TOKEN:-}" ]; then
  log "1/6 login em ghcr.io como ${GHCR_USER:-x-access-token} ..."
  printf '%s' "${GHCR_TOKEN}" | docker login ghcr.io -u "${GHCR_USER:-x-access-token}" --password-stdin >/dev/null \
    || fail "docker login em ghcr.io falhou"
  LOGGED_IN=1
else
  log "1/6 sem GHCR_TOKEN — assumo imagem pública ou credencial já no docker config"
fi
# O logout corre SEMPRE, incluindo em falha: uma credencial de CD não fica a residir no
# ~/.docker/config.json de um servidor entre deploys.
cleanup_login() { [ "${LOGGED_IN}" -eq 1 ] && docker logout ghcr.io >/dev/null 2>&1 || true; }
# Substitui o trap do passo 0c: a drenagem também se liberta em QUALQUER saída (AOS-450).
trap 'cleanup_login; libertar_drenagem' EXIT

# --- 2. Pull ANTES de tocar no que corre ---------------------------------------------------------
log "2/6 pull ${IMAGE_REF} ..."
docker pull "${IMAGE_REF}" >/dev/null || fail "pull falhou — o stack em execução NÃO foi tocado"

# --- 3. Guarda o digest anterior (base do rollback) ----------------------------------------------
log "3/6 a registar o estado anterior ..."
if [ -s "${IMAGE_ENV}" ]; then
  cp "${IMAGE_ENV}" "${IMAGE_ENV_PREV}"
  PREV_REF="$( grep -E '^AOS_IMAGE=' "${IMAGE_ENV_PREV}" | tail -1 | cut -d= -f2- )"
  log "     anterior: ${PREV_REF}"
else
  PREV_REF=""
  log "     primeiro deploy (sem anterior — não haverá reversão automática)"
fi

RESOLVED="$( docker image inspect --format '{{index .RepoDigests 0}}' "${IMAGE_REF}" 2>/dev/null || echo "${IMAGE_REF}" )"
{
  echo "# Escrito por deploy.sh — NÃO editar à mão."
  echo "AOS_IMAGE=${RESOLVED}"
} > "${IMAGE_ENV}"

# --- 3b. LINHA DE BASE DE PRONTIDÃO --------------------------------------------------------------
# O gate do passo 6 passou a exigir `/readyz`, e o `/readyz` tem uma condição que NÃO depende da
# imagem: um crypto-shred por confirmar é RE-HIDRATADO da cadeia a cada arranque e mantém o nó
# não-pronto INDEFINIDAMENTE. Sem esta medição, uma entrega falharia — e reverteria — por um estado
# durável que a imagem anterior tinha exactamente igual, e a reversão NÃO o resolveria: o nó antigo
# subiria igualmente não-pronto.
#
# Mede-se ANTES de tocar em nada. Verde antes e vermelho depois é regressão DESTA entrega. Vermelho
# antes e vermelho depois é uma avaria que já lá estava, e travar a entrega por ela seria pior do
# que deixá-la passar: bloquearia justamente a correcção que a resolveria.
#
# 000 (edge em baixo, primeiro deploy) NÃO é linha de base verde — na dúvida não se atribui culpa.
ready_antes="$( curl -sk -o /dev/null -w '%{http_code}' --max-time 10 \
                "https://127.0.0.1:${EDGE_PORT}/readyz" 2>/dev/null || echo 000 )"
log "3b/6 prontidão ANTES da entrega: HTTP ${ready_antes}"

# --- reverter() -----------------------------------------------------------------------------------
# Repõe o digest anterior e SAI != 0 — nunca devolve. Definida AQUI, depois do passo 3, porque só a
# partir dele existem o PREV_REF e o image.env.prev para onde voltar.
#
# UMA FUNÇÃO CHAMADA DE DOIS SÍTIOS, e a razão é o incidente de 2026-09-13. A reversão vivia só no
# fim do script, e o passo 4 saía com `fail` quando o `compose up` falhava — ANTES de lá chegar. Mas
# quando o `up` falha o stack JÁ FOI TOCADO: o image.env aponta ao digest novo e o contentor antigo
# já foi substituído. Foi assim que a v0.1.11 deixou a produção em 502: o nó novo abortava no
# arranque, o compose desistia com «dependency failed to start: ... is unhealthy», e o script saía
# sem repor nada. A recuperação foi um rollback à mão — e com o digest explícito, porque uma
# tentativa seguinte já tinha reescrito o image.env.prev com o próprio digest partido.
#
# O `motivo` entra nas mensagens, para o log dizer QUE falha disparou a reversão.
reverter() {
  local motivo="$1"
  if [ "${NO_ROLLBACK:-0}" = "1" ]; then
    fail "${motivo} e NO_ROLLBACK=1 — o stack fica como está, para inspecção."
  fi
  if [ -z "${PREV_REF}" ]; then
    fail "${motivo} no PRIMEIRO deploy — não há digest anterior para onde reverter. Corrige a config (ver log acima) e repete."
  fi
  log "⏪ ${motivo} — a reverter para ${PREV_REF} ..."
  cp "${IMAGE_ENV_PREV}" "${IMAGE_ENV}"
  dc up -d --remove-orphans || fail "REVERSÃO FALHOU — servidor precisa de intervenção manual. Estado: docker compose -f ${COMPOSE_FILE} ps"
  fail "deploy revertido para ${PREV_REF}. A versão nova NÃO está a servir."
}

# --- 4. Sobe ---------------------------------------------------------------------------------------
log "4/6 docker compose up -d ..."
if ! dc up -d --remove-orphans; then
  # O log do nó ANTES de reverter: é onde aparece o motivo do aborto (uma porta de produção por
  # satisfazer, config inválida). Depois da reversão o contentor novo deixa de existir, e com ele o
  # log — no incidente, o motivo só se leu por SSH à mão, e só por ter sido antes do rollback.
  log "     compose up falhou. Últimas linhas do nó:"
  dc logs --tail 40 aos 2>&1 | sed 's/^/       /' || true
  reverter "compose up falhou"
fi


# --- 4b. CONFIG MONTADA MAIS NOVA QUE O PROCESSO ----------------------------------------------
# `docker compose up -d` recria um serviço quando a DEFINIÇÃO muda — imagem, env, montagens. NÃO
# o recria quando muda apenas o CONTEÚDO de um ficheiro montado, porque a definição é a mesma. E
# um bind-mount de FICHEIRO fica agarrado ao INODE: o `rsync` escreve um ficheiro novo e renomeia,
# o inode muda, e o processo continua a ler o antigo — que já não está em caminho nenhum.
#
# O nó escapa por acidente: a imagem muda a cada deploy, é recriado, relê tudo. Os OUTROS
# serviços — litellm, otel, edge, idp, vault — ficam com o inode antigo INDEFINIDAMENTE, e a
# divergência não produz sintoma até alguém reiniciar.
#
# Foi assim que o LiteLLM serviu dois dias a partir de um inode órfão: o host tinha `model_list:`
# vazio, o processo tinha o encaminhamento real, e um restart de rotina teria deixado o nó sem
# modelo — com a configuração a existir só na vista de um processo.
#
# COMPARAR HASHES NÃO SERVE, e vale a pena dizer porquê: um contentor auxiliar com
# `--volumes-from` RE-RESOLVE o caminho de origem, monta o ficheiro de novo, e vê sempre o do
# host. Concorda sempre. Verificado — o detector que o fazia passou o caso de deriva sem o notar.
# Ler `/proc/<pid>/root/...` do host mostraria a vista real, mas exige root, que este deploy
# deliberadamente não tem.
#
# O sinal que resta é sólido e conservador: se o ficheiro no host foi TOCADO depois de o processo
# arrancar, o processo ou já não o lê (substituído) ou leu-o antes (escrito por cima e não
# relido). Nos dois casos recriar realinha. O custo é um restart a mais quando o ficheiro foi
# escrito por cima — barato, e do lado certo do erro. Nos serviços SEM LEITOR esse restart a mais
# acontecia em TODOS os deploys, e é isso que o registo do que cada contentor carregou (abaixo) fecha.
# hash_no_contentor devolve o md5 do ficheiro TAL COMO O PROCESSO O VÊ, ou vazio.
#
# A VALIDAÇÃO DA FORMA NÃO É ZELO. Uma imagem distroless não tem shell nem `md5sum`, e o
# `docker exec` devolve o erro do OCI — `OCI runtime exec failed: ...`. Esse texto entra numa
# variável tão bem como um hash entraria, e comparado com o do host DIFERE SEMPRE. Já produziu
# três falsos "*** DERIVA ***" neste projecto, e o pior deles apontava para o WORM do nó.
#
# Um hash tem 32 hex. Tudo o resto é "não sei", e "não sei" nunca pode ser "diferente".
hash_no_contentor() {
  local cid="$1" dst="$2" h
  h="$( docker exec "${cid}" md5sum "${dst}" 2>/dev/null | cut -d' ' -f1 )"
  case "${h}" in
    [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) printf '%s' "${h}" ;;
    *) printf '' ;;
  esac
}

# --- O QUE CADA CONTENTOR CARREGOU ---------------------------------------------------------------
# A data sozinha recriava SEMPRE os serviços sem leitor (distroless: o `otel`, o próprio nó) — e
# não por deriva nenhuma. O `rsync -a` do CD reescreve TODOS os ficheiros que sincroniza: o checkout
# do runner é novo, a data difere, e o rsync escreve um temporário e renomeia-o mesmo com o conteúdo
# idêntico. O ficheiro fica sempre "mais novo que o processo", o contentor não tem `md5sum` para o
# desmentir, e o `otel` era recriado em todos os deploys, incluindo num redeploy do mesmo digest.
#
# O que falta a um serviço sem leitor é saber O QUE o processo leu. Isso sabe-se do lado do host, no
# único momento em que é certo: um contentor que arrancou DEPOIS da última alteração ao ficheiro viu
# o conteúdo que lá está. Regista-se aí o md5, por contentor (o id muda a cada recriação, e o registo
# com ele) e por destino. Num deploy seguinte, um ficheiro mais novo com o MESMO md5 que o registado
# é o conteúdo que o processo já tem, só com um inode novo — nada a realinhar. Um md5 diferente é
# deriva de conteúdo, dita como tal. Sem registo (o primeiro deploy com este passo, ou um contentor
# que arrancou antes de o ficheiro mudar) fica a data, conservadora como sempre foi.
#
# A data é a de ALTERAÇÃO DO INODE (ctime, `%Z`), não a de modificação (`%Y`). O `rsync -a` e o
# `touch -d` escrevem a mtime que quiserem: uma cópia com a mtime da origem pode ser mais antiga que
# o processo e ter conteúdo novo. A ctime não se escreve à mão — um inode novo ou alterado tem a ctime
# de agora. Para a suspeita é mais conservadora; para o registo é o que o torna seguro.
#
# O REGISTO É DE UM ARRANQUE, NÃO DE UM CONTENTOR. O id só muda quando o contentor é RECRIADO; um
# REINÍCIO (crash, reboot, `restart: unless-stopped`, `docker compose restart`) mantém o id e volta a
# montar o ficheiro pelo caminho — o processo relê o que lá está. Um registo por id sobreviveria a isso
# e afirmaria um conteúdo que o processo já não tem: o host muda sem deploy (um deploy que falhou
# depois do rsync e antes do passo 4), o contentor reinicia e lê o novo, um commit repõe o antigo — e o
# antigo, igual ao registo, passaria por alinhado. Por isso cada linha guarda o `StartedAt` exacto, e
# só vale enquanto for o do arranque actual.
REGISTO_DIR="${APP_DIR}/.config-carregada"

e_md5() { [[ "$1" =~ ^[0-9a-f]{32}$ ]]; }

# md5 do ficheiro no host, ou vazio. A mesma regra do hash_no_contentor: só 32 hex é um hash.
md5_no_host() {
  local h
  h="$( md5sum "$1" 2>/dev/null | cut -d' ' -f1 )" || true
  if e_md5 "${h}"; then printf '%s' "${h}"; fi
}

# md5 que o contentor <cid> carregou em <destino> NO ARRANQUE <inicio>, ou vazio.
registo_ler() {
  local h
  h="$( awk -F'\t' -v d="$2" -v i="$3" '$1 == d && $3 == i { h = $2 } END { print h }' "${REGISTO_DIR}/$1" 2>/dev/null )" || true
  if e_md5 "${h}"; then printf '%s' "${h}"; fi
}

# Regista, por arranque de cada contentor, o md5 dos ficheiros que ele DE CERTEZA carregou. Corre
# depois das recriações, para os contentores novos entrarem já com registo. Devolve != 0 quando não
# consegue escrever — quem a chama avisa, e o pior que isso custa é o próximo deploy decidir pela data.
registar_config_carregada() {
  local svc cid inicio ini_epoch tmp f vivos=" "
  mkdir -p "${REGISTO_DIR}" || return 1
  for svc in $( dc ps --services 2>/dev/null ); do
    cid="$( dc ps -q "${svc}" 2>/dev/null )" || continue
    [[ "${cid}" =~ ^[0-9a-f]{12,64}$ ]] || continue
    vivos="${vivos}${cid} "
    inicio="$( docker inspect -f '{{.State.StartedAt}}' "${cid}" 2>/dev/null )" || continue
    ini_epoch="$( date -d "${inicio}" +%s 2>/dev/null )" || continue
    tmp="${REGISTO_DIR}/${cid}.tmp"
    { docker inspect "${cid}" --format '{{range .Mounts}}{{if eq .Type "bind"}}{{.Source}}|{{.Destination}}{{println}}{{end}}{{end}}' 2>/dev/null || true; } \
    | while IFS='|' read -r src dst; do
        [ -n "${src}" ] && [ -f "${src}" ] || continue
        c_epoch="$( stat -c %Z "${src}" 2>/dev/null )" || continue
        h="$( md5_no_host "${src}" )"
        if [ -n "${h}" ] && [ "${c_epoch}" -lt "${ini_epoch}" ]; then
          printf '%s\t%s\t%s\n' "${dst}" "${h}" "${inicio}"          # arrancou depois: vê este conteúdo
        else
          h="$( registo_ler "${cid}" "${dst}" "${inicio}" )"          # senão, só o que já se sabia DESTE arranque
          [ -z "${h}" ] || printf '%s\t%s\t%s\n' "${dst}" "${h}" "${inicio}"
        fi
      done > "${tmp}"
    [ -f "${tmp}" ] || return 1
    mv -f "${tmp}" "${REGISTO_DIR}/${cid}" || return 1
  done
  # Registos (e temporários) de contentores que já não existem não servem a ninguém.
  for f in "${REGISTO_DIR}"/*; do
    [ -f "${f}" ] || continue
    case "${vivos}" in *" $( basename "${f}" ) "*) ;; *) rm -f "${f}" ;; esac
  done
}

log "4b/6 a verificar config montada mais nova que o processo ..."
DET="$( mktemp )"
IGUAIS="$( mktemp )"
for svc in $( dc ps --services 2>/dev/null ); do
  cid="$( dc ps -q "${svc}" 2>/dev/null )"
  [ -n "${cid}" ] || continue
  inicio="$( docker inspect -f '{{.State.StartedAt}}' "${cid}" 2>/dev/null )" || continue
  ini_epoch="$( date -d "${inicio}" +%s 2>/dev/null )" || continue
  docker inspect "${cid}" --format '{{range .Mounts}}{{if eq .Type "bind"}}{{.Source}}|{{.Destination}}{{println}}{{end}}{{end}}' 2>/dev/null \
  | while IFS='|' read -r src dst; do
      [ -n "${src}" ] && [ -f "${src}" ] || continue
      f_epoch="$( stat -c %Z "${src}" 2>/dev/null )" || continue
      [ "${f_epoch}" -gt "${ini_epoch}" ] || continue

      # O ficheiro é mais novo que o processo. Isso SUSPEITA de deriva; não a prova. Quando dá
      # para ler de dentro, o CONTEÚDO decide — e poupa um restart a quem só levou uma data nova
      # do rsync. Reiniciar o `edge` por causa de uma data é uma interrupção pública sem motivo.
      hh="$( md5_no_host "${src}" )"
      hc="$( hash_no_contentor "${cid}" "${dst}" )"
      if [ -n "${hc}" ]; then
        if [ -n "${hh}" ] && [ "${hh}" = "${hc}" ]; then      # mais novo, mas IGUAL ⇒ nada a fazer
          printf '%s\t%s\n' "${svc}" "$( basename "${src}" )" >> "${IGUAIS}"
          continue
        fi
        printf '%s\t%s\t%s\n' "${svc}" "$( basename "${src}" )" "conteudo" >> "${DET}"
        continue
      fi

      # Sem leitor lá dentro (distroless). Decide o registo do que o processo carregou; sem registo
      # fica a data, que é conservadora: no pior caso recria-se um serviço que já estava alinhado.
      hr="$( registo_ler "${cid}" "${dst}" "${inicio}" )"
      if [ -z "${hr}" ]; then
        printf '%s\t%s\t%s\n' "${svc}" "$( basename "${src}" )" "data (sem leitor nem registo)" >> "${DET}"
      elif [ -n "${hh}" ] && [ "${hh}" = "${hr}" ]; then
        printf '%s\t%s\n' "${svc}" "$( basename "${src}" )" >> "${IGUAIS}"
      else
        printf '%s\t%s\t%s\n' "${svc}" "$( basename "${src}" )" "conteudo (registo do arranque)" >> "${DET}"
      fi
    done
done

if [ -s "${IGUAIS}" ]; then
  log "     data nova mas o MESMO conteúdo que o processo tem (nada a recriar): $( awk -F'\t' '{ printf "%s%s:%s", (NR > 1 ? " " : ""), $1, $2 }' "${IGUAIS}" )"
fi

if [ -s "${DET}" ]; then
  while IFS="$(printf '\t')" read -r s f m; do
    log "     ${s}: ${f} diverge por ${m}"
  done < "${DET}"

  # GUARDA: só passam nomes que o compose RECONHECE. Se algo além de um nome de serviço chegar
  # aqui outra vez, pára com uma mensagem que o diz — em vez de o entregar ao docker.
  SERVICOS="$( dc ps --services 2>/dev/null | tr '\n' ' ' )"
  ALVOS=""
  for s in $( cut -f1 "${DET}" | sort -u ); do
    case " ${SERVICOS} " in
      *" ${s} "*) ALVOS="${ALVOS} ${s}" ;;
      *) fail "deriva: ${s} nao e um servico do compose — a lista foi contaminada" ;;
    esac
  done
  log "     a recriar:${ALVOS}"
  # shellcheck disable=SC2086
  dc up -d --force-recreate ${ALVOS} || fail "recriacao por config nova falhou"
else
  log "     nenhuma config divergente"
fi
rm -f "${DET}" "${IGUAIS}"
registar_config_carregada || log "     aviso: não foi possível registar a config carregada — o próximo deploy decide pela data"

# --- 5. Espera pelo healthy do NÓ (não do edge: o edge só arranca depois) -------------------------
log "5/6 a aguardar o nó healthy (tecto ${HEALTH_TIMEOUT}s) ..."
CID="$( dc ps -q aos )"
[ -n "${CID}" ] || fail "container do nó não existe após o up"

deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
healthy=0
while [ "$(date +%s)" -lt "${deadline}" ]; do
  hs="$( docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}nohealth{{end}}' "${CID}" 2>/dev/null || echo '?' )"
  st="$( docker inspect -f '{{.State.Status}}' "${CID}" 2>/dev/null || echo '?' )"
  if [ "${hs}" = "healthy" ]; then healthy=1; break; fi
  # Um container que saiu não vai ficar healthy — falhar já poupa o tecto inteiro e, mais
  # importante, mostra o log do ABORT do nó (config inválida) em vez de um timeout mudo.
  if [ "${st}" = "exited" ] || [ "${st}" = "dead" ]; then
    log "     nó em estado '${st}'. Últimas linhas:"
    dc logs --tail 40 aos 2>&1 | sed 's/^/       /'
    break
  fi
  sleep 3
done

# --- 6. Smoke em TLS pelo edge (o caminho REAL do cliente) ----------------------------------------
# SONDA-SE O `/readyz`, NÃO O `/healthz`. O `handleHealthz` devolve 200 INCONDICIONALMENTE — é
# liveness, "o processo responde" — pelo que um nó que recusa 100% dos pedidos passava este gate.
# E este gate é a ÚNICA entrada automática da reversão: as duas variáveis que a decidiam (`healthy`
# e `smoke_ok`) vinham as DUAS do `/healthz`.
#
# O `/healthz` continua a ser o certo no passo 5 (HEALTHCHECK do contentor) e no `depends_on` do
# edge: aí a acção é REINICIAR, e reiniciar um nó que arrancou bem mas não está pronto não ajuda.
smoke_ok=0
if [ "${healthy}" -eq 1 ]; then
  log "6/6 smoke: GET https://127.0.0.1:${EDGE_PORT}/readyz (tecto ${READY_TIMEOUT}s) ..."
  # ESPERA-ATÉ-PRONTO, e não 5 confirmações: o edge só arranca depois do `service_healthy` do nó, e
  # um Vault recriado pelo passo 4b sobe SELADO com o watchdog a destravá-lo em até 30s. Cinco
  # tentativas de 3s transformariam essa janela normal num deploy vermelho.
  fim=$(( SECONDS + READY_TIMEOUT ))
  while [ "${SECONDS}" -lt "${fim}" ]; do
    code="$( curl -sk -o /dev/null -w '%{http_code}' --max-time 10 \
             "https://127.0.0.1:${EDGE_PORT}/readyz" || echo 000 )"
    if [ "${code}" = "200" ]; then smoke_ok=1; log "     HTTP ${code}"; break; fi
    sleep 3
  done
  [ "${smoke_ok}" -eq 1 ] || log "     prontidão vermelha (último código: ${code:-000})"
fi

# A ATRIBUIÇÃO. `smoke_ok` diz se o nó está pronto; `gate_ok` diz se ESTA entrega é a culpada.
# São coisas distintas e só a segunda decide reverter.
gate_ok=0
if [ "${smoke_ok}" -eq 1 ]; then
  gate_ok=1
elif [ "${ready_antes}" = "200" ]; then
  log "     ANTES da entrega estava 200 — a regressão é DESTA entrega"
else
  log "     mas ANTES da entrega já estava ${ready_antes} — a causa é ANTERIOR e a reversão não a resolve"
  log "     o nó continua NÃO-PRONTO: ver GET /readyz e o log do nó. A entrega NÃO é revertida por isto."
  gate_ok=1
fi

if [ "${healthy}" -eq 1 ] && [ "${gate_ok}" -eq 1 ]; then
  # A LINHA NÃO PODE DIZER "verde" SOBRE UM NÓ NÃO-PRONTO. `gate_ok` sem `smoke_ok` significa
  # "a entrega não é a culpada", que não é a mesma coisa que "está tudo bem" — e escrever a
  # segunda quando só a primeira é verdade seria a mesma mentira de anúncio que este gate veio
  # corrigir. Duas linhas distintas para dois desfechos distintos.
  if [ "${smoke_ok}" -eq 1 ]; then
    log "✅ deploy verde — ${RESOLVED}"
  else
    log "⚠️  deploy ENTREGUE mas o nó NÃO está pronto (causa anterior a esta entrega) — ${RESOLVED}"
  fi
  dc logs --tail 25 aos 2>&1 | grep -iE 'endurecid|operador|four-eyes|ratificador|BUNDLE CARREGADO|durav|WORM|soberania|OTLP|AVISO' | sed 's/^/       /' || true
  exit 0
fi

# --- Reversão: nó não saudável, ou regressão de prontidão desta entrega ---------------------------
reverter "deploy vermelho"
