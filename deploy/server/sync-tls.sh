#!/usr/bin/env bash
# =============================================================================
# sync-tls.sh — traz o certificado de `aos.elysiumii.site` do cert-manager para o edge do nó.
#
# Corre como ROOT (precisa do kubeconfig do control-plane), idealmente por systemd timer:
#   systemctl start aos-tls-sync.service     # uma passagem
#   systemctl status aos-tls-sync.timer      # estado do agendamento
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
EDGE_CONTAINER="${EDGE_CONTAINER:-aos-edge-1}"
ALERT_DAYS="${ALERT_DAYS:-15}"

log()  { printf '[sync-tls] %s\n' "$*"; }
fail() { printf '[sync-tls] FALHA: %s\n' "$*" >&2; exit 1; }

command -v kubectl >/dev/null 2>&1 || fail "kubectl ausente — este script corre no control-plane"
[ -d "${TLS_DIR}" ] || fail "${TLS_DIR} nao existe"

# KUBECONFIG EXPLÍCITO. Sob systemd o serviço não herda o ambiente do root e o kubectl não
# encontra ~/.kube/config — a leitura do secret falha em silêncio. Apanhado no primeiro teste
# via `systemctl start`: à mão funcionava, pelo timer não. Nunca se confie no $HOME aqui.
if [ -z "${KUBECONFIG:-}" ]; then
  for c in /etc/kubernetes/admin.conf /root/.kube/config; do
    [ -r "$c" ] && { export KUBECONFIG="$c"; break; }
  done
fi
[ -n "${KUBECONFIG:-}" ] || fail "sem kubeconfig legivel (/etc/kubernetes/admin.conf ou /root/.kube/config)"

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

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
  new_fp="$( openssl x509 -noout -fingerprint -sha256 -in "${TMP}/new.crt" 2>/dev/null )"
  cur_fp="$( openssl x509 -noout -fingerprint -sha256 -in "${TLS_DIR}/edge.crt" 2>/dev/null || echo 'nenhum' )"

  if [ "${new_fp}" = "${cur_fp}" ]; then
    log "sem alteracoes (fingerprint igual)"
  else
    log "certificado NOVO detectado — a instalar"
    # Escrita atómica: o nginx nunca vê um ficheiro meio-escrito.
    install -m 644 "${TMP}/new.crt" "${TLS_DIR}/edge.crt.new" && mv -f "${TLS_DIR}/edge.crt.new" "${TLS_DIR}/edge.crt"
    install -m 640 "${TMP}/new.key" "${TLS_DIR}/edge.key.new" && mv -f "${TLS_DIR}/edge.key.new" "${TLS_DIR}/edge.key"
    chown aos:aos "${TLS_DIR}/edge.crt" "${TLS_DIR}/edge.key" 2>/dev/null || true

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
[ -s "${TLS_DIR}/edge.crt" ] || fail "nao ha certificado em ${TLS_DIR}/edge.crt"
end="$( openssl x509 -noout -enddate -in "${TLS_DIR}/edge.crt" | cut -d= -f2 )"
end_s="$( date -d "${end}" +%s 2>/dev/null || echo 0 )"
days=$(( (end_s - $(date +%s)) / 86400 ))
subj="$( openssl x509 -noout -subject -in "${TLS_DIR}/edge.crt" | sed 's/^subject= *//' )"
log "em vigor: ${subj} — expira ${end} (${days} dias)"

if [ "${days}" -lt "${ALERT_DAYS}" ]; then
  fail "o certificado do edge expira em ${days} dias (< ${ALERT_DAYS}) e a sincronizacao nao o renovou. Verifica o cert-manager: kubectl get certificate -n ${NS} ${SECRET}"
fi
if [ "${CLUSTER_OK}" -eq 0 ]; then
  fail "o certificado em vigor ainda serve (${days} dias), mas NAO foi possivel ler ${NS}/${SECRET}. A ponte para o cert-manager esta partida: quando a renovacao acontecer, ela nao chega ao edge. Verifica: KUBECONFIG=${KUBECONFIG} kubectl get certificate -n ${NS} ${SECRET}"
fi
log "OK"
