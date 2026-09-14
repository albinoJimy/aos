#!/usr/bin/env bash
# backup-pull-gate.sh — o ÚNICO comando que a chave de recolha dos backups pode correr no servidor.
#
# Instalado como comando FORÇADO no authorized_keys do `aos` (ver README §Backup):
#   restrict,command="bash /opt/aos/backup-pull-gate.sh" ssh-ed25519 AAAA… aos-backup-pull
#
# ─── PORQUÊ ─────────────────────────────────────────────────────────────────────────────────
# A recolha (pull-backups.ps1) corre sozinha, todos os dias, numa máquina do operador — a chave
# não pode ter passphrase. E o `aos` está no grupo docker: uma shell com ele é root no servidor.
# Sem este gate, quem levasse a chave levava o host. Com ele, leva o que a tarefa precisa e mais
# nada: a lista dos backups e os próprios backups, que estão CIFRADOS para uma chave privada que
# nunca esteve neste servidor.
#
# `restrict` fecha pty, forwarding, agente e X11. O comando forçado substitui QUALQUER pedido,
# incluindo o subsistema sftp — e isso importa: o `scp` moderno fala SFTP por omissão, e por SFTP
# a chave leria tudo o que o `aos` lê (.env, secrets/). Por isso a recolha usa `scp -O` (protocolo
# clássico), que chega aqui como `scp -f <caminho>`: um caminho que se pode validar.
#
# O que não passa na validação é recusado e registado no syslog (`aos-backup-pull`). Um pedido
# recusado com esta chave é, por definição, alguém que não é a tarefa.
set -euo pipefail

DEST=/opt/aos/backups
PEDIDO="${SSH_ORIGINAL_COMMAND:-}"

recusa() {
  logger -t aos-backup-pull "recusado: ${PEDIDO:0:200}" 2>/dev/null || true
  printf 'backup-pull-gate: recusado — esta chave só lista e recolhe backups\n' >&2
  exit 1
}

case "${PEDIDO}" in
  listar)
    ls -1 "${DEST}"/aos-*.tar.gz.enc 2>/dev/null || true
    ;;
  recente)
    # Sem backups não há data: sai != 0 e o pull-backups.ps1 alerta, que é o certo.
    f="$(ls -1t "${DEST}"/aos-*.tar.gz.enc 2>/dev/null | head -1 || true)"
    [[ -n "${f}" ]] || exit 1
    stat -c %Y "${f}"
    ;;
  "scp -f "*)
    # Só um nome de artefacto do backup.sh, por caminho absoluto e sem nada à volta: sem `..`,
    # sem globs, sem opções extra (-r, -p, -d), sem um segundo caminho.
    alvo="${PEDIDO#scp -f }"
    [[ "${alvo}" =~ ^/opt/aos/backups/aos-[0-9]{8}T[0-9]{6}Z\.tar\.gz\.enc$ ]] || recusa
    [[ -f "${alvo}" ]] || recusa
    exec scp -f "${alvo}"
    ;;
  *)
    recusa
    ;;
esac
