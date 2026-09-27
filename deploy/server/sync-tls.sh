#!/usr/bin/env bash
# =============================================================================
# sync-tls.sh — traz o certificado de `aos.elysiumii.site` do cert-manager para o edge do nó.
#
# Corre como ROOT, por systemd timer, a partir de /usr/local/sbin/aos-sync-tls:
#   systemctl start aos-tls-sync.service     # uma passagem
#   systemctl status aos-tls-sync.timer      # estado do agendamento
#
# ─── Onde vive, e porque NÃO é em /opt/aos (AOS-446, ADR-033 §2.1) ──────────────────────────
# Até ao AOS-446 este script vivia em /opt/aos/sync-tls.sh, do `aos` (0755), reescrito pelo CD a
# cada deploy, e o serviço corria-o como root com o admin.conf do Kubernetes. Quem escrevesse
# naquele ficheiro — o `aos`, a chave de deploy, ou quem fizesse merge de uma alteração a ele —
# ganhava root no host e cluster-admin no cluster na passagem diária seguinte, sem ninguém tocar
# no servidor. Agora:
#   - a cópia que o root executa é instalada por ROOT, root:root 0755, fora do /opt/aos e fora do
#     rsync do deploy (deploy/server/README.md §TLS, «Instalar como root»);
#   - o kubeconfig é o da ServiceAccount `aos-tls-sync` (tls-sync-rbac.yaml), que só pode `get`
#     no secret default/aos-node-tls — o admin.conf é recusado;
#   - tudo o que se lê ou escreve na pasta do edge, que é do `aos`, faz-se COMO o `aos`
#     (runuser). O root nunca segue um caminho que o `aos` controla: um symlink plantado em
#     /opt/aos/secrets/tls levaria um `chown` ou um `install` do root para onde o `aos` quisesse.
#
# ─── Porque é que este script existe ─────────────────────────────────────────────────────────
# O cert-manager renova o Certificate DENTRO do cluster. O edge do nó é um contentor Docker
# FORA do cluster, que lê os ficheiros de /opt/aos/secrets/tls. Sem esta ponte, o certificado
# renovava no Kubernetes e o nó continuava a servir o antigo até expirar — pior do que
# self-signed, porque expira em silêncio e ninguém está à espera disso.
#
# IDEMPOTENTE: compara os fingerprints e não toca em nada se forem iguais (não recarrega o
# nginx à toa). Só escreve — e só recarrega — quando o material muda de facto.
#
# FAIL-LOUD: sai != 0 se o certificado EM VIGOR no edge expirar dentro de ALERT_DAYS. Sob
# systemd isso deixa a unidade em `failed`, visível em `systemctl --failed`, em vez de um
# aviso enterrado num log que ninguém lê.
# =============================================================================
set -uo pipefail

NS="${NS:-default}"
SECRET="${SECRET:-aos-node-tls}"
TLS_DIR="${TLS_DIR:-/opt/aos/secrets/tls}"
TLS_OWNER="${TLS_OWNER:-aos}"
EDGE_CONTAINER="${EDGE_CONTAINER:-aos-edge-1}"
ALERT_DAYS="${ALERT_DAYS:-15}"
KUBECONFIG_MINIMO="/etc/aos/kube/aos-tls-sync.kubeconfig"

log()  { printf '[sync-tls] %s\n' "$*"; }
fail() { printf '[sync-tls] FALHA: %s\n' "$*" >&2; exit 1; }

command -v kubectl >/dev/null 2>&1 || fail "kubectl ausente — este script corre no control-plane"

EU_ROOT=0
[ "$(id -u)" -eq 0 ] && EU_ROOT=1

# como_dono CMD... — corre CMD como o dono da pasta do edge, nunca como root. Fora de root (uma
# passagem à mão, como o próprio `aos`) corre-o directamente.
como_dono() {
  if [ "${EU_ROOT}" -eq 1 ]; then
    runuser -u "${TLS_OWNER}" -- "$@"
  else
    "$@"
  fi
}

# --- 0. A fronteira do host (AOS-446) -----------------------------------------------------------
# Um ficheiro executado por root que um não-root possa reescrever é root para esse não-root. Estas
# verificações não protegem contra quem já reescreveu o script (esse reescreve-as também); apanham
# a instalação ERRADA — a unidade reposta a apontar para /opt/aos, o ficheiro copiado sem
# `-o root`, o admin.conf de volta por conveniência — e fazem-na falhar em vez de correr.
if [ "${EU_ROOT}" -eq 1 ]; then
  command -v runuser >/dev/null 2>&1 || fail "runuser ausente (util-linux) — recuso escrever na pasta do ${TLS_OWNER} como root"
  id -u "${TLS_OWNER}" >/dev/null 2>&1 || fail "utilizador ${TLS_OWNER} nao existe"
  eu="$(readlink -f "$0")"
  case "${eu}" in
    /opt/aos/*) fail "corro como root a partir de ${eu}, que o ${TLS_OWNER} e o CD reescrevem. Instala a copia root-owned: deploy/server/README.md §TLS" ;;
  esac
  dono_modo="$(stat -c '%u %a' "${eu}" 2>/dev/null || echo '? ?')"
  case "${dono_modo}" in
    "0 755"|"0 750"|"0 700"|"0 555"|"0 550"|"0 500") ;;
    *) fail "${eu} tem dono/modo '${dono_modo}' — tem de ser root e sem escrita de grupo nem de outros" ;;
  esac
fi

# KUBECONFIG EXPLÍCITO e MÍNIMO. Sob systemd o serviço não herda o ambiente do root; a unidade
# fixa-o. Sem ele usa-se o da ServiceAccount — e nunca se recua para o admin.conf, que era o que
# este script fazia até ao AOS-446: cluster-admin para ler UM secret. ALLOWLIST EXACTA, e não
# lista de proibidos: o kubeadm deixa no host outras credenciais largas (controller-manager.conf,
# scheduler.conf) que uma lista de proibidos deixava passar.
export KUBECONFIG="${KUBECONFIG:-${KUBECONFIG_MINIMO}}"
[ "${KUBECONFIG}" = "${KUBECONFIG_MINIMO}" ] \
  || fail "KUBECONFIG=${KUBECONFIG} recusado: o unico aceite e ${KUBECONFIG_MINIMO} (tls-sync-rbac.yaml), que so faz 'get' em ${NS}/${SECRET}"
[ -r "${KUBECONFIG}" ] || fail "sem kubeconfig legivel em ${KUBECONFIG} — gera-o: deploy/server/README.md §TLS"
if [ "${EU_ROOT}" -eq 1 ]; then
  # Um kubeconfig que o `aos` escreva deixa-o apontar o `server:` para onde quiser e escolher o
  # certificado que o root instala. Tem de ser do root, sem acesso de grupo nem de outros.
  kc_modo="$(stat -L -c '%u %a' "${KUBECONFIG}" 2>/dev/null || echo '? ?')"
  case "${kc_modo}" in
    "0 600"|"0 400") ;;
    *) fail "${KUBECONFIG} tem dono/modo '${kc_modo}' — tem de ser root:root 0600" ;;
  esac
fi
# O ficheiro certo pode ter a credencial errada (um token de outra conta colado lá dentro). Se
# ela consegue LISTAR secrets, é maior do que a ponte — recusa-se usá-la. Um cluster inalcançável
# responde erro, não «yes», e cai na falha de leitura mais abaixo.
if kubectl auth can-i list secrets -n "${NS}" >/dev/null 2>&1; then
  fail "a credencial de ${KUBECONFIG} consegue listar secrets em ${NS} — e maior do que esta ponte precisa (tls-sync-rbac.yaml). Recuso usa-la"
fi

como_dono test -d "${TLS_DIR}" || fail "${TLS_DIR} nao existe (ou o ${TLS_OWNER} nao a alcanca)"

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

# O certificado em vigor, lido COMO o dono para a pasta privada do root. Só a cópia é analisada,
# e também COMO o dono: o PEM é do `aos`, e o parser do openssl não corre como root sobre ele.
# (Um `edge.crt` trocado por uma FIFO bloqueia o `cat`; o TimeoutStartSec da unidade corta-o.)
ler_em_vigor() {
  : > "${TMP}/cur.crt"
  como_dono cat "${TLS_DIR}/edge.crt" > "${TMP}/cur.crt" 2>/dev/null || true
}
x509_em_vigor() {
  como_dono openssl x509 -noout "$@" < "${TMP}/cur.crt" 2>/dev/null
}

# instalar_como_dono ORIGEM DESTINO MODO — escrita atómica, feita pelo dono da pasta: o nginx
# nunca vê um ficheiro meio-escrito, e o root não abre nenhum caminho dentro dela.
instalar_como_dono() {
  como_dono sh -c 'rm -f "$1.new" && (umask 077 && cat > "$1.new") && chmod "$2" "$1.new" && mv -f "$1.new" "$1"' \
    _ "$2" "$3" < "$1"
}

# --- 1. Extrair do cluster ---------------------------------------------------------------------
CLUSTER_OK=1
if ! kubectl get secret -n "${NS}" "${SECRET}" -o jsonpath='{.data.tls\.crt}' 2>/dev/null | base64 -d > "${TMP}/new.crt" || [ ! -s "${TMP}/new.crt" ]; then
  log "AVISO: nao consegui ler ${NS}/${SECRET} do cluster."
  CLUSTER_OK=0
  # Não se aborta AQUI para que o relatório de expiração ainda saia — mas isto É falha, e no
  # fim sai != 0. Uma incapacidade de ler o cluster que se repita todos os dias é exactamente
  # o que precisa de ser visto; esperar pelos 15 dias finais seria descobrir tarde de mais.
else
  kubectl get secret -n "${NS}" "${SECRET}" -o jsonpath='{.data.tls\.key}' 2>/dev/null | base64 -d > "${TMP}/new.key"
  [ -s "${TMP}/new.key" ] || fail "o secret tem tls.crt mas nao tls.key — recuso escrever um par incompleto"

  # O par bate certo? Um cert e uma chave desirmanados deixariam o nginx sem arrancar.
  c_mod="$( openssl x509 -noout -pubkey -in "${TMP}/new.crt" 2>/dev/null | openssl md5 )"
  k_mod="$( openssl pkey -pubout -in "${TMP}/new.key" 2>/dev/null | openssl md5 )"
  [ -n "${c_mod}" ] && [ "${c_mod}" = "${k_mod}" ] || fail "cert e chave do secret NAO correspondem — nada foi escrito"

  # --- 2. Mudou? -------------------------------------------------------------------------------
  ler_em_vigor
  new_fp="$( openssl x509 -noout -fingerprint -sha256 -in "${TMP}/new.crt" 2>/dev/null )"
  cur_fp="$( x509_em_vigor -fingerprint -sha256 || echo 'nenhum' )"

  if [ "${new_fp}" = "${cur_fp}" ]; then
    log "sem alteracoes (fingerprint igual)"
  else
    log "certificado NOVO detectado — a instalar"
    instalar_como_dono "${TMP}/new.crt" "${TLS_DIR}/edge.crt" 644 || fail "nao consegui escrever ${TLS_DIR}/edge.crt"
    instalar_como_dono "${TMP}/new.key" "${TLS_DIR}/edge.key" 640 || fail "nao consegui escrever ${TLS_DIR}/edge.key"

    if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "${EDGE_CONTAINER}"; then
      if docker exec "${EDGE_CONTAINER}" nginx -s reload >/dev/null 2>&1; then
        log "nginx recarregado (sem quebrar ligacoes em curso)"
      else
        log "AVISO: reload falhou — a reiniciar o edge"
        docker restart "${EDGE_CONTAINER}" >/dev/null 2>&1 || log "AVISO: restart do edge tambem falhou"
      fi
    else
      log "AVISO: ${EDGE_CONTAINER} nao esta a correr — os ficheiros ficam prontos para o proximo arranque"
    fi
  fi
fi

# --- 3. O que está EM VIGOR ainda serve? --------------------------------------------------------
# Esta é a pergunta que importa, e é feita ao ficheiro que o edge usa — não ao que o cluster tem.
ler_em_vigor
[ -s "${TMP}/cur.crt" ] || fail "nao ha certificado em ${TLS_DIR}/edge.crt"
end="$( x509_em_vigor -enddate | cut -d= -f2 )"
end_s="$( date -d "${end}" +%s 2>/dev/null || echo 0 )"
days=$(( (end_s - $(date +%s)) / 86400 ))
subj="$( x509_em_vigor -subject | sed 's/^subject= *//' )"
log "em vigor: ${subj} — expira ${end} (${days} dias)"

if [ "${days}" -lt "${ALERT_DAYS}" ]; then
  fail "o certificado do edge expira em ${days} dias (< ${ALERT_DAYS}) e a sincronizacao nao o renovou. Verifica o cert-manager: kubectl get certificate -n ${NS} ${SECRET}"
fi
if [ "${CLUSTER_OK}" -eq 0 ]; then
  fail "o certificado em vigor ainda serve (${days} dias), mas NAO foi possivel ler ${NS}/${SECRET}. A ponte para o cert-manager esta partida: quando a renovacao acontecer, ela nao chega ao edge. Verifica: KUBECONFIG=${KUBECONFIG} kubectl get secret -n ${NS} ${SECRET}"
fi
log "OK"
