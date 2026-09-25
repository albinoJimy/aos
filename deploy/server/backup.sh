#!/usr/bin/env bash
# backup.sh — cópia CIFRADA do estado durável do nó. Corre por cron, sem root.
#
#   bash /opt/aos/backup.sh
#
# ─── PORQUE O BACKUP TEM DE LEVAR TUDO JUNTO ────────────────────────────────────────────────
# Copiar o `events.wal` sozinho produz um ficheiro INÚTIL: o conteúdo dos runs está cifrado por
# KEK-por-titular, e as KEKs vivem no Vault. Sem `vault-data` o backup restaura metadados e
# ciphertext indecifrável. E sem `secrets/vault-init.json` nem se consegue destravar o Vault
# restaurado. As três peças só valem juntas.
#
# ─── E PORQUE ISSO OBRIGA A CIFRAR ──────────────────────────────────────────────────────────
# Juntas, valem exactamente o mesmo que a máquina: quem tiver o backup tem o conteúdo, as chaves
# que o decifram e o material que destrava o Vault. Um backup em claro anula a cifra em repouso
# que o Vault existe para dar — e é precisamente ao SAIR do host que ele fica exposto.
#
# Por isso cifra-se para um CERTIFICADO cuja chave privada NUNCA esteve neste servidor (vive em
# secrets-local/backup-key/, ao lado da issuer.key). Consequências, ambas deliberadas:
#   - um atacante com root aqui NÃO consegue ler os backups que esta máquina produz;
#   - PERDER A CHAVE PRIVADA É PERDER OS BACKUPS. Não há recuperação. Trate-a como a issuer.key.
#
# ─── O QUE ISTO PROTEGE, E O QUE NÃO ────────────────────────────────────────────────────────
# Ficheiros no MESMO disco protegem contra apagamento acidental do volume, corrupção da aplicação
# e um deploy mau. NÃO protegem contra perda do host nem falha de disco. Para isso é preciso
# levar os ficheiros para outro sítio — ver README §"Backup" para o comando de recolha. O facto
# de estarem cifrados é o que torna essa cópia segura.
#
# ─── E PORQUE O SUBSTRATO TEM DE SER VERIFICADO, E NÃO PRESUMIDO ────────────────────────────
# Este script copia um VOLUME. Isso só é um backup do Event Store enquanto o Event Store viver
# nesse volume — e desde o AOS-100 pode não viver. Com `AOS_EVENTSTORE_NATS` preenchido, o log
# passa a viver num cluster NATS JetStream e PRECEDE o WAL local (a precedência está em
# packages/cmd/aos/bootstrap.go); o `events.wal` do volume fica obsoleto, ou vazio.
#
# Sem guarda, nada disto se via daqui: o `tar` do volume corria, o envelope PKCS#7 verificava, e
# o cron saía VERDE sobre um artefacto SEM O LOG DOS RUNS. Um backup verde e vazio é pior do que
# backup nenhum — não falha o suficiente para alguém ir ver, e ocupa o lugar do alarme.
#
# Daí duas verificações, e são perguntas diferentes:
#   passo 0  — que substrato está CONFIGURADO? (contentor em execução + .env)
#   passo 2b — que ficheiros é que o tar TROUXE MESMO?
# A primeira apanha o log que se mudou; a segunda apanha o volume que se esvaziou. Nenhuma das
# duas substitui a outra, e as duas são fail-closed.
#
# ─── E O QUE SAI DO BUNDLE, EM CLARO: O REGISTO DE APAGAMENTOS (AOS-436) ────────────────────
# Este bundle leva o Vault TAL COMO ESTÁ — com as KEKs vivas nesse instante. Restaurá-lo depois de
# um apagamento DSAR traz a KEK do titular de volta, e o WORM do mesmo bundle nem sabe que o
# apagamento aconteceu. Nada DENTRO do bundle pode cobrir isto: é o bundle inteiro que recua.
#
# Por isso o registo de apagamentos do nó (`aos/apagamentos-dsar.txt`) é copiado TAMBÉM para FORA
# do bundle, em claro, como `backups/apagamentos-<stamp>.txt`. Cada linha é `<id> <instante> <mac>`,
# com id e mac HMAC sob a chave do nó (`aos/apagamentos-dsar.txt.chave`) — que fica SÓ dentro do
# bundle cifrado e NUNCA sai daqui em claro: sem ela, o registo não diz quem foi apagado nem se
# deixa forjar. A recolha leva o mais recente; ao restaurar um bundle MAIS ANTIGO, importa-se antes
# de arrancar o nó, e o nó destrói de novo o que ele diz destruído.
#
# O QUE ISTO NÃO COBRE, dito sem arredondar: esta cópia é tirada do MESMO tar e no MESMO instante que
# o bundle. Para «perdi o host, restauro o último bundle» não acrescenta nada — o último bundle já
# sabe o mesmo. Só ajuda quem restaura um bundle ANTERIOR ao último (o último está estragado, ou quer
# voltar a um ponto antes de um deploy mau). Os apagamentos feitos depois do último backup não estão
# em cópia nenhuma.

set -Eeuo pipefail

AOS_DIR="${AOS_DIR:-/opt/aos}"
DEST="${AOS_DIR}/backups"
CERT="${AOS_DIR}/backup-recipient.crt"
KEEP="${BACKUP_KEEP:-14}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

log()  { printf '[backup] %s\n' "$*"; }
fail() { printf '[backup] ERRO: %s\n' "$*" >&2; exit 1; }

[[ -s "${CERT}" ]] || fail "${CERT} em falta — sem destinatário não se cifra, e sem cifrar não se copia"
command -v openssl >/dev/null || fail "openssl em falta"
mkdir -p "${DEST}"; chmod 700 "${DEST}"

# --- 0. O log de eventos está mesmo NESTE volume? ---------------------------------------------
# Fail-closed ANTES de qualquer trabalho: se o log não está no volume, nada do que vem a seguir
# produz um backup — produz a APARÊNCIA de um, que é o modo de falha caro.
#
# Duas fontes, e a distinção importa no diagnóstico: o CONTENTOR diz o que o nó corre AGORA, e é
# isso que decide onde o log está a ser escrito HOJE; o `.env` diz o que o próximo `deploy.sh`
# vai aplicar, e é isso que decide onde estará AMANHÃ. Qualquer uma a apontar para um cluster é
# motivo de recusa — a primeira porque este backup já não tem o log, a segunda porque o seguinte
# deixa de ter e ninguém estaria a olhar nessa noite.
#
# As buscas capturam para variável ANTES de procurar, e não usam `| grep -q` nem `| head`: com
# `set -o pipefail`, um consumidor que fecha o pipe cedo manda SIGPIPE a montante e o pipeline
# falha APESAR de ter encontrado. É o defeito que já custou duas iterações no restore-drill.sh.
env_do_no() {
  local todas
  todas="$(docker inspect aos-aos-1 --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null || true)"
  sed -n "s/^$1=//p" <<<"${todas}"
}
env_do_ficheiro() {
  [[ -r "${AOS_DIR}/.env" ]] || return 0
  # `tail -n 1` e NAO `${v##*$'\n'}`: a substituicao de comando come as linhas vazias do FIM,
  # pelo que uma redefinicao para VAZIO desaparecia e o valor antigo e que vencia. O sentido do
  # erro era seguro (recusava a mais), mas nao inofensivo: um .env com uma linha obsoleta acima
  # de uma vazia faria o cron recusar TODAS as noites, e um alarme que toca sempre e o mesmo
  # silencio que esta guarda existe para evitar. Medido nos cinco casos (valor->vazio,
  # vazio->valor, dois valores, aspas, comentado).
  #
  # `tail` le a entrada TODA, pelo que nao ha aqui o SIGPIPE que proibe o `| head` acima.
  local v; v="$(sed -n "s/^[[:space:]]*$1=//p" "${AOS_DIR}/.env" | tail -n 1)"
  tr -d "\"'\r" <<<"${v}"
}

log "0/4 substrato do Event Store"
NATS_NO="$(env_do_no AOS_EVENTSTORE_NATS)"
NATS_ENV="$(env_do_ficheiro AOS_EVENTSTORE_NATS)"
EXTERNO="${BACKUP_EVENTSTORE_EXTERNO:-}"
LOG_FORA_DO_VOLUME=0

if [[ -n "${NATS_NO}${NATS_ENV}" ]]; then
  FONTE=""
  if [[ -n "${NATS_NO}"  ]]; then FONTE="contentor aos-aos-1 (a correr): AOS_EVENTSTORE_NATS=${NATS_NO}"; fi
  if [[ -n "${NATS_ENV}" ]]; then FONTE="${FONTE:+${FONTE}; }${AOS_DIR}/.env (próximo deploy): AOS_EVENTSTORE_NATS=${NATS_ENV}"; fi
  if [[ -z "${EXTERNO}" ]]; then
    fail "SUBSTRATO REPLICADO, BACKUP LOCAL — recusado, e NÃO se produziu artefacto nenhum.

  ${FONTE}

  Com AOS_EVENTSTORE_NATS preenchido, o Event Store REPLICADO (AOS-100, ADR-007) PRECEDE o WAL
  local: o log dos runs vive no cluster JetStream e o events.wal do volume aos_aos-data fica
  obsoleto ou vazio. Este script copia o VOLUME — e só o volume.

  O que sairia daqui: Vault, IdP e configuração, SEM o log dos runs, e VERDE na mesma, porque o
  tar corre e o envelope verifica. Um backup verde e vazio não falha o suficiente para alguém ir
  ver: ocupa o lugar do alarme. É por isso que isto recusa em vez de avisar.

  Para desbloquear é preciso DIZER como o log replicado é copiado — e depois fazê-lo mesmo:
    BACKUP_EVENTSTORE_EXTERNO='<onde e como>' bash $0
  O texto fica gravado no MANIFEST do artefacto, e é o que o restore-drill.sh lê para recusar um
  ensaio que não poderia provar nada. NÃO copia coisa nenhuma: é uma declaração, não um mecanismo."
  fi
  LOG_FORA_DO_VOLUME=1
  log "  ⚠️  REPLICADO — ${FONTE}"
  log "  ⚠️  este artefacto NÃO leva o log dos runs. Declarado: ${EXTERNO}"
else
  log "  local — WAL no volume aos_aos-data, que é o que o passo 2 copia"
fi

# --- 1. Postgres do IdP: pg_dump, NÃO cópia de ficheiros -------------------------------------
# Um tar do PGDATA em execução apanha páginas a meio de escrita e produz um dump que restaura
# corrompido — silenciosamente, o que é pior do que falhar. O pg_dump dá um snapshot coerente.
log "1/4 pg_dump do IdP"
docker exec aos-idp-db-1 pg_dump -U keycloak -d keycloak --clean --if-exists \
  > "${WORK}/idp-db.sql" 2>/dev/null || fail "pg_dump falhou"
[[ -s "${WORK}/idp-db.sql" ]] || fail "pg_dump devolveu vazio"
log "  $(wc -c < "${WORK}/idp-db.sql") bytes"

# --- 2. Volumes de estado ---------------------------------------------------------------------
# Os WAL são append-only com fsync: um tar de um ficheiro vivo dá um PREFIXO, e um prefixo de
# hash-chain é uma cadeia válida (truncada) — restaura e verifica. O storage `file` do Vault é
# escrito em ficheiros pequenos; a janela de inconsistência existe mas é estreita. Não se para o
# nó para copiar 700 KB.
log "2/4 volumes"
# O volume do orquestrador `aos-orq` (AOS-403) guarda os WAL dos runs multi-nó e o WORM de
# governação do gateway do planeador. Só existe depois da primeira corrida (`--profile orq`): um
# `-v` a um volume inexistente CRIÁ-LO-IA fora do compose, que depois avisa que não é seu. Entra
# no tar quando existe, e a ausência fica escrita no log.
ORQ_MOUNT=()
ORQ_DIR=()
if docker volume inspect aos_aos-orq-data >/dev/null 2>&1; then
  ORQ_MOUNT=(-v aos_aos-orq-data:/aos-orq:ro)
  ORQ_DIR=(aos-orq)
else
  log "  aos_aos-orq-data não existe (o aos-orq nunca correu neste servidor) — fora do tar"
fi
docker run --rm -v aos_aos-data:/aos:ro -v aos_vault-data:/vault:ro "${ORQ_MOUNT[@]}" -v "${WORK}":/out alpine:3.20 \
  tar czf /out/volumes.tar.gz -C / aos vault "${ORQ_DIR[@]}" 2>/dev/null || fail "tar dos volumes falhou"
log "  $(wc -c < "${WORK}/volumes.tar.gz") bytes"

# --- 2b. E o tar trouxe mesmo o que existe para trazer? ---------------------------------------
# Até aqui o script só sabia que o `tar` SAIU BEM. Um tar de um volume vazio também sai bem, e um
# tar de um volume onde o WAL deixou de estar também. Esta verificação lê o índice do artefacto
# que acabou de produzir e exige lá dentro os ficheiros pelos quais ele existe.
MEMBROS="$(tar tzf "${WORK}/volumes.tar.gz")"
tem_membro() { grep -Fxq "$1" <<<"${MEMBROS}"; }
dim_membro() { tar xzOf "${WORK}/volumes.tar.gz" "$1" 2>/dev/null | wc -c || true; }

# A guarda procura caminhos FIXOS dentro do tar (`aos/…`, porque o volume entra montado em /aos),
# e esse mapa vem do docker-compose.prod.yml. Se o nó passar a correr com outros caminhos, o mapa
# cala-se e a guarda passaria a verificar com sucesso o ficheiro ERRADO — a mesma doença noutra
# casa. Por isso confirma-se o mapa contra o contentor, e recusa-se quando ele deixar de valer.
WAL_NO="$(env_do_no AOS_EVENTSTORE_PATH)"
WORM_NO="$(env_do_no AOS_WORM_PATH)"
if [[ -n "${WAL_NO}" && "${WAL_NO}" != "/var/lib/aos/events.wal" ]]; then
  fail "o nó corre com AOS_EVENTSTORE_PATH=${WAL_NO}, mas esta guarda verifica 'aos/events.wal' dentro do tar (mapa de docker-compose.prod.yml: aos_aos-data em /var/lib/aos). O mapa deixou de valer — actualize a guarda antes de voltar a confiar no artefacto"
fi
if [[ -n "${WORM_NO}" && "${WORM_NO}" != "/var/lib/aos/worm.wal" ]]; then
  fail "o nó corre com AOS_WORM_PATH=${WORM_NO}, mas esta guarda verifica 'aos/worm.wal' dentro do tar. O mapa deixou de valer — actualize a guarda"
fi

# O WORM é SEMPRE um ficheiro local: não tem substrato replicado e por isso não tem excepção. Dois
# escritores forkam-lhe a hash-chain (AOS-284), e é essa a razão de ele continuar local mesmo com
# o Event Store no cluster.
tem_membro "aos/worm.wal" \
  || fail "o tar dos volumes NÃO contém aos/worm.wal — o trilho de decisões não está no backup. O volume aos_aos-data está vazio, ou não é o que o nó escreve"

# O REGISTO DE APAGAMENTOS (AOS-436), no mesmo molde: o caminho verificado vem do mapa do
# docker-compose.prod.yml, e confirma-se contra o contentor. Se o nó o escrever noutro sítio, esta
# guarda copiaria para fora um registo que não é o dele — e o restauro importaria um registo
# parado no tempo, que é exactamente a falha que ele existe para impedir.
REG_NO="$(env_do_no AOS_DSAR_ERASURE_REGISTER)"
if [[ -n "${REG_NO}" && "${REG_NO}" != "/var/lib/aos/apagamentos-dsar.txt" ]]; then
  fail "o nó corre com AOS_DSAR_ERASURE_REGISTER=${REG_NO}, mas esta guarda copia 'aos/apagamentos-dsar.txt' do tar. O mapa deixou de valer — actualize a guarda"
fi
# O registo é produzido SEMPRE — vazio quando o nó nunca destruiu nada (o ficheiro só nasce na
# primeira destruição confirmada). Assim «o mais recente» existe sempre para ser recolhido, e a
# ausência de linhas é uma afirmação («nenhum apagamento até aqui»), não um silêncio.
if tem_membro "aos/apagamentos-dsar.txt"; then
  tar xzOf "${WORK}/volumes.tar.gz" "aos/apagamentos-dsar.txt" > "${WORK}/apagamentos-bruto.txt" \
    || fail "não consegui extrair aos/apagamentos-dsar.txt do tar dos volumes"
  # O tar de um ficheiro vivo pode apanhar uma linha a meio de ser escrita. Só as linhas COMPLETAS
  # saem (`wc -l` conta os '\n'): o fragmento final é o que o próprio nó trata como escrita
  # interrompida, e deixá-lo sair faria a recolha rejeitar o registo todos os dias.
  head -n "$(wc -l < "${WORK}/apagamentos-bruto.txt")" "${WORK}/apagamentos-bruto.txt" > "${WORK}/apagamentos.txt"
  REG_ESTADO="volume"
else
  printf '# aos — registo de apagamentos (AOS-436): o volume nao tinha registo neste instante\n' > "${WORK}/apagamentos.txt"
  REG_ESTADO="ausente-no-volume"
fi
REG_N="$(grep -cvE '^(#|[[:space:]]*$)' "${WORK}/apagamentos.txt" || true)"
if [[ -z "${REG_NO}" ]]; then
  log "  ⚠️  o nó corre SEM AOS_DSAR_ERASURE_REGISTER — os apagamentos novos NÃO ficam registados, e um restauro de TUDO antigo ressuscita-os sem ninguém saber"
fi
log "  registo de apagamentos: ${REG_N:-0} entrada(s) (${REG_ESTADO})"

if [[ "${LOG_FORA_DO_VOLUME}" = 0 ]]; then
  tem_membro "aos/events.wal" \
    || fail "o tar dos volumes NÃO contém aos/events.wal, e o passo 0 não viu substrato replicado configurado. Não há log dos runs neste artefacto nem explicação para a falta — o backup seria uma cópia de metadados"
  SZ_WAL="$(dim_membro "aos/events.wal")"; SZ_WAL="${SZ_WAL:-0}"
  # PRESENTE-mas-VAZIO não é recusa: é o estado legítimo de um nó que ainda não escreveu nada. É
  # também o sintoma exacto de um log que se mudou de casa sem ninguém dizer, e por isso grita.
  if [[ "${SZ_WAL}" -eq 0 ]]; then
    log "  ⚠️  aos/events.wal está PRESENTE mas VAZIO — normal num nó que nunca escreveu; se este nó já correu runs, o log mudou de sítio e este backup não os tem"
  else
    log "  aos/events.wal ${SZ_WAL} bytes, aos/worm.wal $(dim_membro "aos/worm.wal") bytes"
  fi
else
  log "  aos/worm.wal presente ($(dim_membro "aos/worm.wal") bytes); events.wal NÃO exigido — o log está no cluster"
fi

# --- 3. Configuração e segredos ---------------------------------------------------------------
# Inclui secrets/vault-init.json — sem a chave de unseal, um Vault restaurado fica selado para
# sempre e o backup do event store não vale nada. Exclui os próprios backups (recursão) e os
# .bak-* acumulados.
#
# E A ÂNCORA DO WORM (AOS-268/AOS-072). Com AOS_WORM_TRUST_ANCHOR ligada, o nó só arranca com
# `ancoras/checkpoints.json` e `pisos/heads.json` presentes — é fail-closed por partição. Esses dois
# ficheiros NÃO vêm do deploy nem do git (nomeiam partições `gov.read/<run-id>`): chegam pela selagem
# diária, e as únicas cópias vivem neste disco. Faltavam aqui. Um host restaurado deste backup subia
# com o env da âncora ligada e SEM os ficheiros, e o nó abortava no arranque: o backup verificava, e
# não levantava o sistema. Por isso entram, e com âncora ligada a falta deles é recusa, não aviso.
log "3/4 configuração e segredos"
ANCORA_NO="$(env_do_no AOS_WORM_TRUST_ANCHOR)"
ANCORA_ENV="$(env_do_ficheiro AOS_WORM_TRUST_ANCHOR)"
if [[ -n "${ANCORA_NO}${ANCORA_ENV}" ]]; then
  # O mesmo cuidado da guarda 2b: os caminhos verificados vêm do mapa do docker-compose.prod.yml
  # (./ancoras → /etc/aos/ancoras, ./pisos → /etc/aos/pisos). Se o nó os ler de outro sítio, esta
  # guarda verificaria com sucesso os ficheiros ERRADOS.
  CK_NO="$(env_do_no AOS_WORM_CHECKPOINT_FILE)"
  HD_NO="$(env_do_no AOS_WORM_EXPECTED_HEADS_FILE)"
  if [[ -n "${CK_NO}" && "${CK_NO}" != "/etc/aos/ancoras/checkpoints.json" ]]; then
    fail "o nó lê a âncora de AOS_WORM_CHECKPOINT_FILE=${CK_NO}, mas esta guarda copia ${AOS_DIR}/ancoras/checkpoints.json (mapa do docker-compose.prod.yml). O mapa deixou de valer — actualize a guarda"
  fi
  if [[ -n "${HD_NO}" && "${HD_NO}" != "/etc/aos/pisos/heads.json" ]]; then
    fail "o nó lê os pisos de AOS_WORM_EXPECTED_HEADS_FILE=${HD_NO}, mas esta guarda copia ${AOS_DIR}/pisos/heads.json. O mapa deixou de valer — actualize a guarda"
  fi
  [[ -s "${AOS_DIR}/ancoras/checkpoints.json" && -s "${AOS_DIR}/pisos/heads.json" ]] \
    || fail "a âncora do WORM está LIGADA mas ${AOS_DIR}/ancoras/checkpoints.json ou ${AOS_DIR}/pisos/heads.json falta (ou está vazio). Um host restaurado deste backup não arrancaria o nó — recusado, e NÃO se produziu artefacto"
  ANCORA="ligada"
else
  ANCORA="desligada"
fi
CONFIG=(.env secrets policies keycloak vault litellm model-tools tls-internal docker-compose.prod.yml image.env)
for d in ancoras pisos orq; do [[ -d "${AOS_DIR}/${d}" ]] && CONFIG+=("${d}"); done
tar czf "${WORK}/config.tar.gz" -C "${AOS_DIR}" \
  --exclude=backups --exclude='*.bak-*' --exclude='.env.bak*' \
  "${CONFIG[@]}" 2>/dev/null || fail "tar da configuração falhou"
log "  $(wc -c < "${WORK}/config.tar.gz") bytes; âncora do WORM ${ANCORA}"

# --- 4. Selar num só artefacto CIFRADO --------------------------------------------------------
log "4/4 cifrar"
# DE ONDE VEIO O LOG — o restore-drill.sh lê esta linha para saber se o artefacto pode sequer
# levantar o sistema. Sem ela, um bundle sem log só se distingue de um bundle bom quando o ensaio
# falha três minutos depois, com um sintoma ("não encontrei nenhum run") que parece outra coisa.
if [[ "${LOG_FORA_DO_VOLUME}" = 1 ]]; then
  MANIFEST_ES="eventstore=externo
eventstore-nats=${NATS_NO:-${NATS_ENV}}
eventstore-externo=${EXTERNO}"
else
  MANIFEST_ES="eventstore=volume"
fi
{ printf 'aos-backup\nstamp=%s\nhost=%s\nimage=%s\n' \
    "${STAMP}" "$(hostname)" "$(grep -oE 'sha256:[a-f0-9]{12}' "${AOS_DIR}/image.env" 2>/dev/null || echo '?')"
  printf '%s\n' "${MANIFEST_ES}"
  printf 'worm-ancora=%s\n' "${ANCORA}"
  # AOS-403: diz se o volume do aos-orq entrou no tar, para que a ausência não se confunda com perda.
  printf 'aos-orq-data=%s\n' "$( [[ ${#ORQ_DIR[@]} -gt 0 ]] && echo volume || echo ausente )"
  # AOS-436: o registo de apagamentos que saiu em claro ao lado deste bundle.
  printf 'apagamentos=%s\napagamentos-entradas=%s\n' "${REG_ESTADO}" "${REG_N:-0}"
  # A chave que autentica o registo viaja SÓ aqui dentro. Um bundle sem ela (anterior ao AOS-436)
  # não consegue autenticar um registo importado — o restauro tem de a trazer do bundle mais recente.
  printf 'apagamentos-chave=%s\n' "$(tem_membro "aos/apagamentos-dsar.txt.chave" && echo volume || echo ausente)"
} > "${WORK}/MANIFEST"
tar czf "${WORK}/bundle.tar.gz" -C "${WORK}" MANIFEST idp-db.sql volumes.tar.gz config.tar.gz
OUT="${DEST}/aos-${STAMP}.tar.gz.enc"
openssl smime -encrypt -aes256 -binary -outform DER \
  -in "${WORK}/bundle.tar.gz" -out "${OUT}" "${CERT}" || fail "cifra falhou"
chmod 600 "${OUT}"

# O plaintext morre com o trap, mas um `shred` explícito fecha a janela em que ele existiu.
shred -u "${WORK}/bundle.tar.gz" "${WORK}/config.tar.gz" "${WORK}/idp-db.sql" 2>/dev/null || true

# VERIFICAÇÃO. Um backup que ninguém abriu é uma suposição. Não se consegue DECIFRAR aqui (a
# chave privada não está neste host, e ainda bem), mas confirma-se que o resultado é um envelope
# PKCS#7 íntegro E do tipo `envelopedData` — ou seja, que traz mesmo conteúdo CIFRADO, e não um
# PKCS#7 qualquer nem um ficheiro truncado, que são os modos de falha reais.
openssl pkcs7 -inform DER -in "${OUT}" -noout 2>/dev/null \
  || fail "o artefacto não é um PKCS#7 íntegro (truncado?)"
openssl asn1parse -inform DER -in "${OUT}" 2>/dev/null | head -3 | grep -q 'pkcs7-envelopedData' \
  || fail "o artefacto é PKCS#7 mas NÃO é envelopedData — o conteúdo pode não estar cifrado"
log "  ${OUT} ($(wc -c < "${OUT}") bytes, envelope verificado)"

# O registo de apagamentos sai SÓ depois de o bundle estar verificado: um registo sem o bundle
# correspondente seria recolhido como «o mais recente» de um backup que não existe.
REG_OUT="${DEST}/apagamentos-${STAMP}.txt"
install -m 600 "${WORK}/apagamentos.txt" "${REG_OUT}" || fail "não consegui escrever ${REG_OUT}"
log "  ${REG_OUT} (${REG_N:-0} entrada(s), EM CLARO — ids HMAC sob a chave do nó, que fica só no bundle)"

# --- Rotação ----------------------------------------------------------------------------------
N=$(ls -1 "${DEST}"/aos-*.tar.gz.enc 2>/dev/null | wc -l)
if (( N > KEEP )); then
  ls -1t "${DEST}"/aos-*.tar.gz.enc | tail -n +$((KEEP+1)) | while read -r f; do rm -f "$f"; done
  log "rotação: ${N} -> ${KEEP}"
fi
# Os registos rodam com a mesma conta. Perder os antigos não custa nada: o mais recente é
# superconjunto de todos eles.
NR=$(ls -1 "${DEST}"/apagamentos-*.txt 2>/dev/null | wc -l)
if (( NR > KEEP )); then
  ls -1t "${DEST}"/apagamentos-*.txt | tail -n +$((KEEP+1)) | while read -r f; do rm -f "$f"; done
fi
log "FEITO — ${N} cópia(s), $(du -sh "${DEST}" | cut -f1) no total"
log "⚠️  no MESMO disco. Perda do host = perda destas cópias. Ver README §Backup."
