#!/bin/sh
# entrypoint.sh — prepara os artefactos (kernel + rootfs) e arranca o orchestrator.
# Corre no container PRIVILEGIADO com /dev/kvm. A build do rootfs é em runtime porque a semente
# (FC_SEED_DIR) é montada pelo compose.
set -e

: "${FC_KERNEL:=/art/vmlinux}"
: "${FC_ROOTFS:=/art/rootfs.ext4}"
: "${FC_SEED_DIR:=/seed}"
: "${FC_KERNEL_URL:=https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.10/x86_64/vmlinux-5.10.223}"

mkdir -p "$(dirname "$FC_KERNEL")"

if [ ! -f "$FC_KERNEL" ]; then
  echo "[entrypoint] a baixar kernel do guest: $FC_KERNEL_URL"
  curl -fsSL -o "$FC_KERNEL" "$FC_KERNEL_URL"
fi

echo "[entrypoint] a construir rootfs ($FC_ROOTFS) a partir do guest-agent + $FC_SEED_DIR"
/build-rootfs.sh /guest-agent "$FC_SEED_DIR" "$FC_ROOTFS"

echo "[entrypoint] /dev/kvm: $(ls -l /dev/kvm 2>&1 || echo AUSENTE)"
echo "[entrypoint] firecracker: $(/usr/local/bin/firecracker --version | head -1)"
echo "[entrypoint] a arrancar orchestrator"
exec /orchestrator
