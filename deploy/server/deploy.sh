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
# FAIL-CLOSED com REVERSÃO AUTOMÁTICA: se o nó não ficar saudável ou o smoke falhar, este
# script repõe o digest anterior e sai != 0. Nunca deixa o servidor num estado que ninguém
# escolheu.
#
# Variáveis opcionais:
#   GHCR_USER / GHCR_TOKEN   login efémero no registry (o token é revogado ao fim do job de CD)
#   HEALTH_TIMEOUT           segundos a esperar pelo healthy (default 180)
#   NO_ROLLBACK=1            desliga a reversão automática (para depurar um arranque falhado)
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
trap cleanup_login EXIT

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

# --- 4. Sobe ---------------------------------------------------------------------------------------
log "4/6 docker compose up -d ..."
dc up -d --remove-orphans || fail "compose up falhou"


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
# escrito por cima — barato, e do lado certo do erro.
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

log "4b/6 a verificar config montada mais nova que o processo ..."
DET=/tmp/aos-deriva-detalhe.txt
: > "${DET}"
for svc in $( dc ps --services 2>/dev/null ); do
  cid="$( dc ps -q "${svc}" 2>/dev/null )"
  [ -n "${cid}" ] || continue
  inicio="$( docker inspect -f '{{.State.StartedAt}}' "${cid}" 2>/dev/null )" || continue
  ini_epoch="$( date -d "${inicio}" +%s 2>/dev/null )" || continue
  docker inspect "${cid}" --format '{{range .Mounts}}{{if eq .Type "bind"}}{{.Source}}|{{.Destination}}{{println}}{{end}}{{end}}' 2>/dev/null \
  | while IFS='|' read -r src dst; do
      [ -n "${src}" ] && [ -f "${src}" ] || continue
      f_epoch="$( stat -c %Y "${src}" 2>/dev/null )" || continue
      [ "${f_epoch}" -gt "${ini_epoch}" ] || continue

      # O ficheiro é mais novo que o processo. Isso SUSPEITA de deriva; não a prova. Quando dá
      # para ler de dentro, o CONTEÚDO decide — e poupa um restart a quem só levou uma data nova
      # do rsync. Reiniciar o `edge` por causa de um mtime é uma interrupção pública sem motivo.
      hc="$( hash_no_contentor "${cid}" "${dst}" )"
      if [ -n "${hc}" ]; then
        hh="$( md5sum "${src}" | cut -d' ' -f1 )"
        [ "${hh}" = "${hc}" ] && continue          # mais novo, mas IGUAL ⇒ nada a fazer
        printf '%s\t%s\t%s\n' "${svc}" "$( basename "${src}" )" "conteudo" >> "${DET}"
      else
        # Sem leitor lá dentro (distroless). Fica a data, que é conservadora: no pior caso
        # recria-se um serviço que já estava alinhado.
        printf '%s\t%s\t%s\n' "${svc}" "$( basename "${src}" )" "data (sem leitor)" >> "${DET}"
      fi
    done
done

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
rm -f "${DET}"

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

# --- Reversão -------------------------------------------------------------------------------------
if [ "${NO_ROLLBACK:-0}" = "1" ]; then
  fail "deploy vermelho e NO_ROLLBACK=1 — o stack fica como está, para inspecção."
fi
if [ -z "${PREV_REF}" ]; then
  fail "deploy vermelho no PRIMEIRO deploy — não há digest anterior para onde reverter. Corrige a config (ver log acima) e repete."
fi

log "⏪ deploy vermelho — a reverter para ${PREV_REF} ..."
cp "${IMAGE_ENV_PREV}" "${IMAGE_ENV}"
dc up -d --remove-orphans || fail "REVERSÃO FALHOU — servidor precisa de intervenção manual. Estado: docker compose -f ${COMPOSE_FILE} ps"
fail "deploy revertido para ${PREV_REF}. A versão nova NÃO está a servir."
