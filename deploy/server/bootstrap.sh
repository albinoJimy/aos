#!/usr/bin/env bash
# =============================================================================
# bootstrap.sh — prepara um servidor VIRGEM para receber o nó `aos`. Corre UMA vez, como root.
#
#   scp deploy/server/bootstrap.sh root@37.60.241.150:/tmp/
#   ssh root@37.60.241.150 'bash /tmp/bootstrap.sh "<ssh-pubkey-do-deploy>"'
#
# IDEMPOTENTE: correr duas vezes não duplica nada nem sobrepõe material já provisionado.
#
# O que faz, e porquê cada peça:
#   1. Docker Engine + compose plugin do repositório OFICIAL (não o do apt da distro, que
#      atrasa versões e não traz o plugin compose v2).
#   2. Utilizador `aos` SEM sudo, no grupo docker — é a identidade do CD. Um deploy comprometido
#      não escala a root por esta via (o que o grupo docker dá é equivalente a root no daemon;
#      é a fronteira aceite e está declarada, não escondida).
#   3. Preflight da porta do edge + árvore /opt/aos. O nó em claro NUNCA é publicado — vive só
#      na rede interna do compose, e é essa a contenção (não a firewall).
#   4. Rotação de logs no daemon do docker — SÓ se ainda não houver daemon.json. Sobrepor a
#      configuração do daemon num host com outros containers é mexer no que não é nosso.
#   5. Firewall: DIAGNÓSTICO apenas. Ver o bloco do passo 5 para a razão — não é timidez.
#   6. Actualizações de segurança automáticas.
#
# NÃO faz: gerar chaves, escrever .env, arrancar o stack. Isso é provision.sh (como `aos`).
# =============================================================================
set -euo pipefail

DEPLOY_USER="${DEPLOY_USER:-aos}"
APP_DIR="${APP_DIR:-/opt/aos}"
# 8443 está ocupada por ingress-nginx neste host (ver README §"O servidor real"). 8444 foi
# verificada livre. Trocar aqui exige trocar também AOS_EDGE_PORT no .env do servidor.
EDGE_PORT="${EDGE_PORT:-8444}"
SSH_PUBKEY="${1:-}"

log()  { printf '\033[36m[bootstrap]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[bootstrap] FAIL:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "corre como root (sudo bash bootstrap.sh ...)"
command -v apt-get >/dev/null 2>&1 || fail "este script assume uma base Debian/Ubuntu (apt-get). Noutra distro, replica os 5 passos à mão."

export DEBIAN_FRONTEND=noninteractive

# --- 0. Utilitários que o CD PRESSUPÕE no servidor --------------------------------------------
# rsync: o deploy.yml sincroniza por rsync — sem ele o job falha no primeiro `Sincronizar`.
# curl:  o smoke interno de deploy.sh. openssl: o TLS do edge em provision.sh.
log "0/6 utilitários (rsync, curl, openssl, ca-certificates) ..."
apt-get update -qq
apt-get install -y -qq rsync curl openssl ca-certificates

# --- 1. Docker Engine + compose plugin (repositório oficial) ---------------------------------
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  log "1/6 docker já presente ($(docker --version | head -1)) — salto a instalação"
else
  log "1/6 a instalar Docker Engine + compose plugin ..."
  apt-get install -y -qq gnupg
  install -m 0755 -d /etc/apt/keyrings
  if [ ! -s /etc/apt/keyrings/docker.asc ]; then
    curl -fsSL https://download.docker.com/linux/"$(. /etc/os-release && echo "$ID")"/gpg \
      -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
  fi
  cat > /etc/apt/sources.list.d/docker.list <<EOF
deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$(. /etc/os-release && echo "$ID") $(. /etc/os-release && echo "${VERSION_CODENAME}") stable
EOF
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
fi

# --- 2. Utilizador de deploy (sem sudo, no grupo docker) -------------------------------------
log "2/6 utilizador '${DEPLOY_USER}' ..."
if ! id -u "${DEPLOY_USER}" >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash "${DEPLOY_USER}"
fi
usermod -aG docker "${DEPLOY_USER}"

if [ -n "${SSH_PUBKEY}" ]; then
  install -d -m 700 -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" "/home/${DEPLOY_USER}/.ssh"
  touch "/home/${DEPLOY_USER}/.ssh/authorized_keys"
  # Idempotente: só acrescenta se a chave ainda não estiver lá.
  grep -qxF "${SSH_PUBKEY}" "/home/${DEPLOY_USER}/.ssh/authorized_keys" \
    || printf '%s\n' "${SSH_PUBKEY}" >> "/home/${DEPLOY_USER}/.ssh/authorized_keys"
  chmod 600 "/home/${DEPLOY_USER}/.ssh/authorized_keys"
  chown "${DEPLOY_USER}:${DEPLOY_USER}" "/home/${DEPLOY_USER}/.ssh/authorized_keys"
  log "     chave de deploy instalada em /home/${DEPLOY_USER}/.ssh/authorized_keys"
else
  log "     AVISO: sem pubkey no argumento 1 — instala-a à mão antes de o CD conseguir entrar."
fi

# --- 3. Árvore da aplicação + preflight de colisão de porta ------------------------------------
# Num host povoado, descobrir que a porta está ocupada só quando o `docker compose up` falha é
# tarde: já se mexeu no estado. Verifica-se aqui, antes de qualquer coisa ficar meio-feita.
if ( ss -tlnH 2>/dev/null || netstat -tln 2>/dev/null ) | grep -qE "[:.]${EDGE_PORT}[[:space:]]"; then
  fail "porta ${EDGE_PORT}/tcp JÁ OCUPADA neste host. Escolhe outra: EDGE_PORT=<livre> bash bootstrap.sh ... (e põe o mesmo valor em AOS_EDGE_PORT no .env)"
fi
log "3/6 porta ${EDGE_PORT}/tcp livre · árvore em ${APP_DIR} ..."
install -d -m 750 -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" "${APP_DIR}"
install -d -m 700 -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" "${APP_DIR}/secrets"
install -d -m 700 -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" "${APP_DIR}/secrets/tls"
install -d -m 750 -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" "${APP_DIR}/policies"
install -d -m 750 -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" "${APP_DIR}/releases"

# --- 4. Rotação de logs do docker + live-restore ---------------------------------------------
log "4/6 daemon.json (rotação de logs, live-restore) ..."
install -d -m 755 /etc/docker
if [ ! -s /etc/docker/daemon.json ]; then
  cat > /etc/docker/daemon.json <<'JSON'
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "50m", "max-file": "5" },
  "live-restore": true
}
JSON
  systemctl restart docker
else
  log "     /etc/docker/daemon.json já existe — não sobreponho (verifica log-opts à mão)"
fi

# --- 5. Firewall — DIAGNÓSTICO, nunca alteração automática -----------------------------------
#
# ⚠️ ESTE PASSO NÃO LIGA NADA, E ISSO É DELIBERADO.
#
# A versão anterior fazia `ufw --force enable` com política default-deny. Num servidor vazio
# isso seria correcto. Neste servidor seria uma avaria grave: o host corre um control-plane
# Kubernetes cujos componentes escutam em portas de HOST (apiserver 6443, kubelet 10250,
# etcd 2379/2380 no IP público, flannel VXLAN 8472/udp) — o ufw FILTRA essas, ao contrário
# das portas publicadas por containers. Um default-deny com 22 e ${EDGE_PORT} abertos cortaria
# o plano de controlo do cluster e a ligação aos nós worker, sem aviso.
#
# A decisão de fechar este servidor é legítima e provavelmente urgente, mas é uma operação
# própria, com a lista de portas a preservar levantada primeiro — não um efeito colateral de
# instalar um nó.
log "5/6 firewall — só DIAGNÓSTICO (este script nunca altera regras) ..."
if command -v ufw >/dev/null 2>&1; then
  ufw status 2>/dev/null | head -1 | sed 's/^/     ufw: /'
else
  log "     ufw: não instalado"
fi
log "     portas de host à escuta em 0.0.0.0/*:"
( ss -tulpnH 2>/dev/null || netstat -tulpn 2>/dev/null ) \
  | awk '{print $5, $7}' | grep -E '^(0\.0\.0\.0|\*|\[::\])' | sort -u | sed 's/^/       /' | head -20
log "     ⚠️ para expor o nó, abre APENAS ${EDGE_PORT}/tcp — e revê a lista acima antes de"
log "        activar qualquer política default-deny neste host."

# --- 6. Actualizações de segurança automáticas — OPT-IN ---------------------------------------
#
# Num host dedicado, ligar isto é obviamente certo. Num host partilhado com um control-plane
# Kubernetes e uptime de meses, é uma decisão de operação com consequências: as actualizações
# reiniciam serviços em momento não escolhido, e num cluster já frágil isso pode transformar-se
# num incidente. Instalar um nó não é altura de mudar a política de patching de outra gente.
if [ "${ENABLE_AUTO_UPDATES:-0}" = "1" ]; then
  log "6/6 unattended-upgrades (pedido explicitamente) ..."
  apt-get install -y -qq unattended-upgrades >/dev/null
  dpkg-reconfigure -f noninteractive unattended-upgrades >/dev/null 2>&1 || true
else
  log "6/6 unattended-upgrades: NÃO alterado (ENABLE_AUTO_UPDATES=1 para o activar)"
  pend="$( apt-get -s upgrade 2>/dev/null | grep -c '^Inst' || true )"
  log "     nota: ${pend} pacotes por actualizar neste host — decisão do operador, não deste script"
fi

log "✅ servidor preparado."
echo
echo "  Próximo passo, como '${DEPLOY_USER}':"
echo "    ssh ${DEPLOY_USER}@\$(hostname -I | awk '{print \$1}')"
echo "    # copia deploy/server/* para ${APP_DIR}, preenche ${APP_DIR}/.env e corre:"
echo "    bash ${APP_DIR}/provision.sh"
