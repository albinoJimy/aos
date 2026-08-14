#!/usr/bin/env bash
# =============================================================================
# gen-identity.sh — gera o material de identidade do deployment. Corre na máquina do OPERADOR,
# a partir da raiz do repositório. NUNCA no servidor.
#
#   bash deploy/server/gen-identity.sh
#
# Produz DUAS coisas com destinos opostos:
#
#   PRIVADO  → deploy/server/secrets-local/   (ignorado pelo git; NÃO sai desta máquina)
#              issuer.key, operator.seed, ratifier.seed, approver-a.seed, approver-b.seed
#   PÚBLICO  → deploy/server/secrets-local/server.env  +  approvers.json
#              pubkeys hex. É isto — e só isto — que vai para /opt/aos no servidor.
#
# ─── Porque é que a chave do issuer NÃO vai para o servidor ─────────────────────────────────
# O nó corre em modo trust-anchor-only: recebe a PUBKEY do issuer e verifica credenciais contra
# ela; nenhuma chave de assinatura entra no processo. Se a privada vivesse no servidor, quem
# comprometesse o servidor mintaria a sua própria identidade com a autoridade do issuer — e a
# separação de trust-domains do ADR-006/ADR-017 seria decorativa. Mintar tokens faz-se aqui:
#
#   packages/cmd/aos-issuer/aos-issuer mint --key-file deploy/server/secrets-local/issuer.key ...
#
# IDEMPOTENTE: reutiliza seeds já geradas (as pubkeys têm de ser estáveis — mudá-las invalida o
# estado durável e todas as credenciais em circulação). Apaga secrets-local/ para recomeçar.
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
OUT="${SCRIPT_DIR}/secrets-local"

EXE=""
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) EXE=".exe" ;; esac

log()  { printf '\033[36m[gen-identity]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[gen-identity] FAIL:\033[0m %s\n' "$*" >&2; exit 1; }

# --- binários geradores (mesma resolução de deploy/node/dev-hardened/up.sh) --------------------
resolve_bin() {
  local pkg="$1" prebuilt="$2" out="$3"
  if [ -x "${prebuilt}" ]; then echo "${prebuilt}"; return; fi
  ( cd "${REPO_ROOT}/${pkg}" && go build -o "${out}" ./ ) >/dev/null 2>&1 \
    || fail "não encontrei ${prebuilt} nem consegui compilar ${pkg} (precisa de Go >= 1.25)"
  echo "${out}"
}

mkdir -p "${OUT}"
chmod 700 "${OUT}" 2>/dev/null || true

ISSUERBIN="$(resolve_bin packages/cmd/aos-issuer "${REPO_ROOT}/packages/cmd/aos-issuer/aos-issuer${EXE}" "${OUT}/aos-issuer${EXE}")"
AOSBIN="$(resolve_bin packages/cmd/aos "${REPO_ROOT}/packages/cmd/aos/aos${EXE}" "${OUT}/aos${EXE}")"

gen_seed() { [ -s "$1" ] && return 0; head -c 32 /dev/urandom | od -An -v -tx1 | tr -d ' \n' > "$1"; chmod 600 "$1" 2>/dev/null || true; }
entry_of() { "${AOSBIN}" operator-pubkey --key "$1" --emitter "$2" | tr -d '\r\n'; }
pub_of()   { entry_of "$1" tmp | sed 's/^tmp=//'; }

log "1/5 issuer externo (pubkey trust-anchor) ..."
ISSUER_PUBKEY="$("${ISSUERBIN}" pubkey --key-file "${OUT}/issuer.key" | tr -d '\r\n')"
[ "${#ISSUER_PUBKEY}" -eq 64 ] || fail "pubkey do issuer com tamanho errado (${#ISSUER_PUBKEY})"

log "2/5 plano de controlo — operador + ratificador ..."
gen_seed "${OUT}/operator.seed"
gen_seed "${OUT}/ratifier.seed"
AOS_OPERATORS="$(entry_of "${OUT}/operator.seed" ops:prod)"
AOS_RATIFIERS="$(entry_of "${OUT}/ratifier.seed" release:prod)"

log "3/5 four-eyes — roster de 2 aprovadores DISTINTOS ..."
gen_seed "${OUT}/approver-a.seed"
gen_seed "${OUT}/approver-b.seed"
AP_A="$(pub_of "${OUT}/approver-a.seed")"
AP_B="$(pub_of "${OUT}/approver-b.seed")"
[ "${AP_A}" != "${AP_B}" ] || fail "as pubkeys de aprovador têm de ser DISTINTAS (o dual-control recusa self-approval)"
cat > "${OUT}/approvers.json" <<EOF
{"approvers":[
  {"principal":"human:alice","pubkey":"${AP_A}","authority":["approve:danger","approve:gray"]},
  {"principal":"human:bob","pubkey":"${AP_B}","authority":["approve:danger"]}
]}
EOF

log "4/5 trust anchor do PDP (base64 do bundle -> hex) ..."
ANCHOR_B64="$(tr -d ' \r\n' < "${REPO_ROOT}/packages/control-plane/pdp/policies/trust_anchor.pub")"
AOS_POLICY_TRUST_ANCHOR="$(printf '%s' "${ANCHOR_B64}" | base64 -d | od -An -v -tx1 | tr -d ' \n')"
[ "${#AOS_POLICY_TRUST_ANCHOR}" -eq 64 ] || fail "trust anchor hex com tamanho errado (${#AOS_POLICY_TRUST_ANCHOR})"

log "5/5 server.env (SÓ material público) ..."
EDGE_HOST="${AOS_EDGE_HOST:-37.60.241.150}"
cat > "${OUT}/server.env" <<EOF
# Gerado por deploy/server/gen-identity.sh — SÓ material PÚBLICO (pubkeys, ids, regras).
# Destino: /opt/aos/.env no servidor, com permissões 600.
AOS_EDGE_HOST=${EDGE_HOST}
# 8443 está ocupada pelo ingress-nginx do Kubernetes deste host; 8444 verificada livre.
AOS_EDGE_PORT=${EDGE_PORT:-8444}
AOS_EDGE_BIND=0.0.0.0

AOS_MODE=
AOS_ISSUER_ID=iss:aos-issuer
AOS_ISSUER_PUBKEY=${ISSUER_PUBKEY}
AOS_OPERATORS=${AOS_OPERATORS}
AOS_RATIFIERS=${AOS_RATIFIERS}
AOS_POLICY_TRUST_ANCHOR=${AOS_POLICY_TRUST_ANCHOR}
AOS_BOARD_REGIONS=board:prod=eu-west

AOS_RETENTION_VERSION=1.0.0
AOS_RETENTION_PERIODS=pii_operational=720h,trajectory=2160h,diagnostic=168h
AOS_RETENTION_SWEEP_INTERVAL=1h

AOS_INGRESS_RATE=64
AOS_INGRESS_BURST=128
AOS_INGRESS_MAX_INFLIGHT=512
AOS_MEM_LIMIT=1g
EOF

echo
log "✅ material gerado em ${OUT}"
echo "   PRIVADO (fica aqui, nunca sai):  issuer.key operator.seed ratifier.seed approver-*.seed"
echo "   PÚBLICO (vai para o servidor):   server.env approvers.json"
echo
echo "   issuer=${ISSUER_PUBKEY:0:12}…  operador=${AOS_OPERATORS%%=*}  ratificador=${AOS_RATIFIERS%%=*}  anchor=${AOS_POLICY_TRUST_ANCHOR:0:12}…"
echo
echo "   Copiar para o servidor:"
echo "     scp ${OUT}/server.env      aos@${EDGE_HOST}:/opt/aos/.env"
echo "     scp ${OUT}/approvers.json  aos@${EDGE_HOST}:/opt/aos/secrets/approvers.json"
echo "     ssh aos@${EDGE_HOST} 'chmod 600 /opt/aos/.env && chmod 644 /opt/aos/secrets/approvers.json'"
