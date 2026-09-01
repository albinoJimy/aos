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
docker run --rm -v aos_aos-data:/aos:ro -v aos_vault-data:/vault:ro -v "${WORK}":/out alpine:3.20 \
  tar czf /out/volumes.tar.gz -C / aos vault 2>/dev/null || fail "tar dos volumes falhou"
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
log "3/4 configuração e segredos"
tar czf "${WORK}/config.tar.gz" -C "${AOS_DIR}" \
  --exclude=backups --exclude='*.bak-*' --exclude='.env.bak*' \
  .env secrets policies keycloak vault litellm model-tools tls-internal \
  docker-compose.prod.yml image.env 2>/dev/null || fail "tar da configuração falhou"
log "  $(wc -c < "${WORK}/config.tar.gz") bytes"

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

# --- Rotação ----------------------------------------------------------------------------------
N=$(ls -1 "${DEST}"/aos-*.tar.gz.enc 2>/dev/null | wc -l)
if (( N > KEEP )); then
  ls -1t "${DEST}"/aos-*.tar.gz.enc | tail -n +$((KEEP+1)) | while read -r f; do rm -f "$f"; done
  log "rotação: ${N} -> ${KEEP}"
fi
log "FEITO — ${N} cópia(s), $(du -sh "${DEST}" | cut -f1) no total"
log "⚠️  no MESMO disco. Perda do host = perda destas cópias. Ver README §Backup."
