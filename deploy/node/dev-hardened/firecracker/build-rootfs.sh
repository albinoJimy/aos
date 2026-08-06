#!/bin/sh
# build-rootfs.sh — constrói um rootfs ext4 MÍNIMO para a microVM: o guest-agent como /init +
# os documentos semeados em /seed. Precisa de privilégio (loop mount, mknod) — corre no
# entrypoint do container do orchestrator, não na build da imagem (a semente é montada em runtime).
set -e

AGENT="$1"     # binário estático do guest-agent
SEED_DIR="$2"  # directório com os documentos a semear (pode não existir)
OUT="$3"       # ficheiro rootfs.ext4 a produzir
SIZE_MB="${ROOTFS_SIZE_MB:-64}"

rm -f "$OUT"
dd if=/dev/zero of="$OUT" bs=1M count="$SIZE_MB" status=none
mkfs.ext4 -q -F "$OUT"

MNT="$(mktemp -d)"
mount -o loop "$OUT" "$MNT"
trap 'umount "$MNT" 2>/dev/null || true; rmdir "$MNT" 2>/dev/null || true' EXIT

mkdir -p "$MNT/seed" "$MNT/proc" "$MNT/dev"
cp "$AGENT" "$MNT/init"
chmod +x "$MNT/init"
# Nós de dispositivo mínimos: o kernel abre /dev/console para a stdio do init ANTES de este
# correr; /dev/vsock vem depois via devtmpfs (montado pelo próprio agent). /dev/null por higiene.
mknod -m 622 "$MNT/dev/console" c 5 1 2>/dev/null || true
mknod -m 666 "$MNT/dev/null" c 1 3 2>/dev/null || true

if [ -d "$SEED_DIR" ]; then
  cp -a "$SEED_DIR/." "$MNT/seed/" 2>/dev/null || true
fi

sync
umount "$MNT"
rmdir "$MNT"
trap - EXIT
echo "[build-rootfs] $OUT pronto ($(du -h "$OUT" | cut -f1); seed=$( [ -d "$SEED_DIR" ] && ls "$SEED_DIR" | wc -l || echo 0 ) ficheiros)"
