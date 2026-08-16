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
printf 'aos-backup\nstamp=%s\nhost=%s\nimage=%s\n' \
  "${STAMP}" "$(hostname)" "$(grep -oE 'sha256:[a-f0-9]{12}' "${AOS_DIR}/image.env" 2>/dev/null || echo '?')" \
  > "${WORK}/MANIFEST"
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
