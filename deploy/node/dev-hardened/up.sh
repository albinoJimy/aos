#!/usr/bin/env bash
# =============================================================================
# up.sh — gera o material de dev (chaves, roster, TLS, .env) e levanta o stack ENDURECIDO.
#
# Idempotente: reutiliza as chaves já geradas em ./secrets se existirem (pubkeys estáveis entre
# reinícios => o volume durável continua válido). Apaga ./secrets para regenerar do zero.
#
# Requisitos: docker, openssl, e o binário do issuer/CLI do repo. No Windows usa os .exe
# pré-compilados (packages/cmd/**/*.exe); noutros SO tenta `go build` (exige Go >= 1.25).
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
SECRETS="${SCRIPT_DIR}/secrets"
ENV_FILE="${SCRIPT_DIR}/.env"
COMPOSE="${SCRIPT_DIR}/docker-compose.yml"

EXE=""
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) EXE=".exe" ;; esac

fail() { echo "up.sh: FAIL: $*" >&2; exit 1; }

# --- resolve os binários geradores de chave (issuer + CLI do operador) -----------------------
# Prefere os .exe pré-compilados do repo (host Windows sem Go 1.25); senão compila.
resolve_bin() {
  local pkg="$1" prebuilt="$2" out="$3"
  if [[ -x "${prebuilt}" ]]; then echo "${prebuilt}"; return; fi
  ( cd "${REPO_ROOT}/${pkg}" && go build -o "${out}" ./ ) >/dev/null 2>&1 \
    || fail "não encontrei ${prebuilt} nem consegui compilar ${pkg} (precisa de Go >= 1.25)"
  echo "${out}"
}
ISSUERBIN="$(resolve_bin packages/cmd/aos-issuer "${REPO_ROOT}/packages/cmd/aos-issuer/aos-issuer${EXE}" "${SECRETS}/aos-issuer${EXE}")"
AOSBIN="$(resolve_bin packages/cmd/aos "${REPO_ROOT}/packages/cmd/aos/aos${EXE}" "${SECRETS}/aos${EXE}")"

mkdir -p "${SECRETS}/tls"
chmod 700 "${SECRETS}" 2>/dev/null || true

# gen_seed escreve 32 bytes CSPRNG em hex (o formato de seed do operador/ratificador/aprovador).
gen_seed() { [[ -s "$1" ]] && return; head -c 32 /dev/urandom | od -An -v -tx1 | tr -d ' \n' > "$1"; chmod 600 "$1" 2>/dev/null || true; }
# entry_of imprime "<id>=<hex>" derivando a pubkey da seed (formato de AOS_OPERATORS/AOS_RATIFIERS).
entry_of() { "${AOSBIN}" operator-pubkey --key "$1" --emitter "$2" | tr -d '\r\n'; }
# pub_of imprime só a pubkey hex (para o campo "pubkey" do roster de aprovadores).
pub_of() { entry_of "$1" tmp | sed 's/^tmp=//'; }

echo "[up] 1/6 identidade endurecida — pubkey do issuer EXTERNO (aos-issuer) ..."
ISSUER_KEY="${SECRETS}/issuer.key"
ISSUER_PUBKEY="$("${ISSUERBIN}" pubkey --key-file "${ISSUER_KEY}" | tr -d '\r\n')"
[[ ${#ISSUER_PUBKEY} == 64 ]] || fail "pubkey do issuer com tamanho errado (${#ISSUER_PUBKEY})"

echo "[up] 2/6 plano de controlo — operador + ratificador ..."
gen_seed "${SECRETS}/operator.seed"
gen_seed "${SECRETS}/ratifier.seed"
AOS_OPERATORS="$(entry_of "${SECRETS}/operator.seed" ops:demo)"
AOS_RATIFIERS="$(entry_of "${SECRETS}/ratifier.seed" release:demo)"

echo "[up] 3/6 four-eyes — roster de 2 aprovadores (só pubkeys, material público) ..."
gen_seed "${SECRETS}/approver-a.seed"
gen_seed "${SECRETS}/approver-b.seed"
AP_A="$(pub_of "${SECRETS}/approver-a.seed")"
AP_B="$(pub_of "${SECRETS}/approver-b.seed")"
[[ "${AP_A}" != "${AP_B}" ]] || fail "as pubkeys de aprovador têm de ser DISTINTAS"
cat > "${SECRETS}/approvers.json" <<EOF
{"approvers":[
  {"principal":"human:alice","pubkey":"${AP_A}","authority":["approve:danger","approve:gray"]},
  {"principal":"human:bob","pubkey":"${AP_B}","authority":["approve:danger"]}
]}
EOF
chmod 644 "${SECRETS}/approvers.json"   # UID 65532 non-root tem de LER o mount.

# AOS-071 — directório de autoridade externo. Sem ele o ScopeGate acaba a re-verificar o
# que a identidade já impôs e NÃO há revogação: um token válido vale até expirar. Os
# sujeitos são os TRÊS que o gate dobra (raiz humana, agente, e o eixo "agent:<classe>").
# Para REVOGAR um sujeito, deixe "capabilities": [] — remover a linha NÃO revoga, devolve-o
# à autoridade plena do seu token.
cat > "${SECRETS}/authority.json" <<'JSON'
{
  "revision": 1,
  "subjects": [
    { "subject": "human:alice",        "capabilities": ["cap:fs.read", "cap:http.post"] },
    { "subject": "agt-aprov",          "capabilities": ["cap:fs.read", "cap:http.post"] },
    { "subject": "agent:agent-worker", "capabilities": ["cap:fs.read", "cap:http.post"] }
  ]
}
JSON
chmod 644 "${SECRETS}/authority.json"   # UID 65532 non-root tem de LER o mount.

echo "[up] 4/6 trust anchor do PDP — base64 do bundle -> hex (formato de AOS_POLICY_TRUST_ANCHOR) ..."
ANCHOR_B64="$(tr -d ' \r\n' < "${REPO_ROOT}/packages/control-plane/pdp/policies/trust_anchor.pub")"
AOS_POLICY_TRUST_ANCHOR="$(printf '%s' "${ANCHOR_B64}" | base64 -d | od -An -v -tx1 | tr -d ' \n')"
[[ ${#AOS_POLICY_TRUST_ANCHOR} == 64 ]] || fail "trust anchor hex com tamanho errado (${#AOS_POLICY_TRUST_ANCHOR})"

echo "[up] 5/6 TLS do edge — cert self-signed (localhost) ..."
if [[ ! -s "${SECRETS}/tls/edge.crt" ]]; then
  # Paths absolutos precisam da conversão MSYS (/c/ -> C:/); só o -subj é protegido do mangling
  # com o prefixo "//" (o MSYS colapsa "//CN=..." para "/CN=..."). NÃO usar MSYS_NO_PATHCONV aqui:
  # desligaria a conversão dos caminhos dos ficheiros e o openssl (binário Windows) receberia /c/...
  openssl req -x509 -nodes -newkey rsa:2048 -days 365 \
    -keyout "${SECRETS}/tls/edge.key" -out "${SECRETS}/tls/edge.crt" \
    -subj "//CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1 \
    || fail "openssl falhou a gerar o cert do edge"
  chmod 644 "${SECRETS}/tls/edge.crt" "${SECRETS}/tls/edge.key"
fi

# --- .env consumido pelo docker-compose ------------------------------------------------------
cat > "${ENV_FILE}" <<EOF
AOS_ISSUER_PUBKEY=${ISSUER_PUBKEY}
AOS_OPERATORS=${AOS_OPERATORS}
AOS_RATIFIERS=${AOS_RATIFIERS}
AOS_POLICY_TRUST_ANCHOR=${AOS_POLICY_TRUST_ANCHOR}
EOF
echo "[up]   issuer=${ISSUER_PUBKEY:0:12}...  operator=${AOS_OPERATORS%%=*}  ratifier=${AOS_RATIFIERS%%=*}  anchor=${AOS_POLICY_TRUST_ANCHOR:0:12}..."

echo "[up] 6/6 docker compose up ..."
docker compose -f "${COMPOSE}" --env-file "${ENV_FILE}" up -d

echo "[up] a aguardar o nó ficar healthy ..."
CID="$(docker compose -f "${COMPOSE}" ps -q aos)"
until [[ "$(docker inspect -f '{{.State.Health.Status}}' "${CID}" 2>/dev/null)" == "healthy" ]]; do
  st="$(docker inspect -f '{{.State.Status}}' "${CID}" 2>/dev/null || echo '?')"
  [[ "${st}" == "running" || "${st}" == "created" ]] || fail "container do nó em estado '${st}' — ver: docker compose -f ${COMPOSE} logs aos"
  sleep 2
done

echo
echo "[up] ✅ nó healthy. TLS via edge:"
curl -sk -o /dev/null -w "     https://localhost:8443/healthz -> HTTP %{http_code}\n" https://localhost:8443/healthz || true
echo "[up] banner do nó (subsistemas ligados):"
docker compose -f "${COMPOSE}" logs aos 2>&1 | grep -iE 'endurecid|operador\(es\) registado|four-eyes gate .*composto|ratificador|BUNDLE CARREGADO|durav|WORM|soberania|OTLP' || true
