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
smoke_ok=0
if [ "${healthy}" -eq 1 ]; then
  log "6/6 smoke: GET https://127.0.0.1:${EDGE_PORT}/healthz ..."
  for _ in 1 2 3 4 5; do
    code="$( curl -sk -o /dev/null -w '%{http_code}' --max-time 10 \
             "https://127.0.0.1:${EDGE_PORT}/healthz" || echo 000 )"
    if [ "${code}" = "200" ]; then smoke_ok=1; log "     HTTP ${code}"; break; fi
    sleep 3
  done
  [ "${smoke_ok}" -eq 1 ] || log "     smoke vermelho (último código: ${code:-000})"
fi

if [ "${healthy}" -eq 1 ] && [ "${smoke_ok}" -eq 1 ]; then
  log "✅ deploy verde — ${RESOLVED}"
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
