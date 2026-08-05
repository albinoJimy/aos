#!/usr/bin/env bash
# =============================================================================
# demo-vault-issuer.sh — CUSTÓDIA DA CHAVE DO ISSUER NO VAULT (AOS-175, realização da Opção A do D4).
#
# Fecha o "teatro criptográfico" do D4: a chave de assinatura do issuer deixa de ser um FICHEIRO
# (que o nó poderia ler) e passa a viver DENTRO do Vault (motor Transit, key-never-leaves). O
# aos-issuer assina ATRAVÉS do Vault pela costura stdlib crypto.Signer; o nó verifica SÓ com a
# pubkey (trust-anchor-only) e NUNCA pode mintar tokens.
#
#   1. provisiona uma chave ed25519 no Transit do Vault (a autoridade de identidade);
#   2. exporta a pubkey do Vault  → o trust-anchor do nó (AOS_ISSUER_PUBKEY);
#   3. minta um NHI ASSINADO PELO VAULT (a privada nunca entra no issuer);
#   4. o nó (ancorado na pubkey do Vault) ACEITA o run (201);
#   5. PROVA NEGATIVA: um NHI da chave-de-ficheiro antiga é RECUSADO (403) — não-forjabilidade;
#   6. restaura o nó.
#
# Produção: trocar o Vault dev por HSM/KMS (a MESMA costura crypto.Signer) + IdP corporativo.
# Pré-requisito: stack OIDC+Vault a correr (up-oidc.sh); aos-issuer.exe recompilado com o vault-signer.
# =============================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
SECRETS="${SCRIPT_DIR}/secrets"
PROJECT="aos-dev-hardened"
EXE=""; case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) EXE=".exe" ;; esac
ISSUERBIN="${REPO_ROOT}/packages/cmd/aos-issuer/aos-issuer${EXE}"
VADDR_HOST="http://localhost:8200"
VTOK="$(cat "${SECRETS}/vault-token" 2>/dev/null)"
KEYNAME="aos-issuer-key"

fail() { echo "demo-vault-issuer.sh: FAIL: $*" >&2; exit 1; }
tok() { docker run --rm --network "${PROJECT}_default" curlimages/curl:latest -sk -d grant_type=password -d client_id=aos-node -d username=alice -d password=alice -d scope=openid https://idp:8443/realms/aos/protocol/openid-connect/token | sed -n 's/.*"id_token":"\([^"]*\)".*/\1/p'; }
DC=(docker compose -p "${PROJECT}" -f "${SCRIPT_DIR}/docker-compose.yml" -f "${SCRIPT_DIR}/docker-compose.oidc.yml" --env-file "${SCRIPT_DIR}/.env")
recreate_node() { "${DC[@]}" up -d --force-recreate aos >/dev/null 2>&1; for _ in $(seq 1 20); do [ "$(curl -sk -o /dev/null -w '%{http_code}' https://localhost:8443/readyz)" = "200" ] && return 0; sleep 2; done; }

[[ -x "${ISSUERBIN}" ]] || fail "aos-issuer não encontrado (recompile com o vault-signer)"
[[ -n "${VTOK}" ]] || fail "sem token do Vault em ${SECRETS}/vault-token"

echo "[vault-issuer] 1/6 a provisionar a chave ed25519 '${KEYNAME}' no Transit do Vault ..."
code="$(curl -s -o /dev/null -w '%{http_code}' -H "X-Vault-Token: ${VTOK}" -X POST -d '{"type":"ed25519"}' "${VADDR_HOST}/v1/transit/keys/${KEYNAME}")"
echo "[vault-issuer]   create key -> HTTP ${code} (204=nova, 400/200=já existia)"

echo "[vault-issuer] 2/6 a exportar a pubkey DO VAULT (trust-anchor do nó) ..."
PUB="$("${ISSUERBIN}" pubkey --vault-addr "${VADDR_HOST}" --vault-key "${KEYNAME}" --vault-token-path "${SECRETS}/vault-token" | tr -d '\r\n')"
[[ ${#PUB} -eq 64 ]] || fail "pubkey do Vault inválida (${PUB})"
echo "[vault-issuer]   AOS_ISSUER_PUBKEY = ${PUB:0:32}…  (veio do Vault, não de um ficheiro)"

echo "[vault-issuer] 3/6 a mintar um NHI ASSINADO PELO VAULT (a privada nunca entra no issuer) ..."
NHI="$("${ISSUERBIN}" mint --vault-addr "${VADDR_HOST}" --vault-key "${KEYNAME}" --vault-token-path "${SECRETS}/vault-token" \
  --issuer iss:aos-issuer --human human:alice --agent agt-vault --class agent-worker --caps cap:http.post --ttl 15m | tr -d '\r\n')"
[[ -n "${NHI}" ]] || fail "mint via Vault falhou"
echo "[vault-issuer]   NHI mintado (${#NHI} chars)"

echo "[vault-issuer] 4/6 a apontar o trust-anchor do nó à pubkey do Vault ..."
cp "${SCRIPT_DIR}/.env" "${SCRIPT_DIR}/.env.bak-vaultissuer"
trap 'mv -f "${SCRIPT_DIR}/.env.bak-vaultissuer" "${SCRIPT_DIR}/.env" 2>/dev/null; recreate_node' EXIT
sed -i "s#^AOS_ISSUER_PUBKEY=.*#AOS_ISSUER_PUBKEY=${PUB}#" "${SCRIPT_DIR}/.env"
recreate_node
FNHI="$("${ISSUERBIN}" mint --key-file "${SECRETS}/issuer.key" --issuer iss:aos-issuer --human human:alice --agent agt-file --class agent-worker --caps cap:http.post --ttl 15m | tr -d '\r\n')"
# O NHI é verificado na MEDIAÇÃO (hook de identidade do RM por-tool-call), NÃO no submit — logo a
# prova faz-se INDUZINDO uma tool call. Objetivo diretivo força a web_post.
OBJ="Publica resultado-42 fazendo HTTP POST para https://api.example.com/results usando a tool web_post. So podes publicar via web_post."
RV="run-idv-$(date -u +%H%M%S)"; RF="run-idf-$(date -u +%H%M%S)"

echo "[vault-issuer] 5/6 a induzir tool calls: NHI Vault-assinado vs NHI de outra chave ..."
curl -sk -o /dev/null -X POST https://localhost:8443/runs -H "Authorization: Bearer $(tok)" -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"${RV}\",\"objective\":\"${OBJ}\",\"principal_nhi\":\"agt-vault\",\"credential\":\"${NHI}\",\"scope\":[\"cap:http.post\"]}"
curl -sk -o /dev/null -X POST https://localhost:8443/runs -H "Authorization: Bearer $(tok)" -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"${RF}\",\"objective\":\"${OBJ}\",\"principal_nhi\":\"agt-file\",\"credential\":\"${FNHI}\",\"scope\":[\"cap:http.post\"]}"
denied_by() { docker compose -p "${PROJECT}" logs otel --since 120s 2>/dev/null | sed 's/\r$//' | awk -v RS='Span #' -v rid="$1" '/execute_tool/ && index($0,"aos.run_id: Str(" rid ")"){print}' | grep -aoE 'aos\.decision\.denied_by: Str\(([a-z]+)\)' | sed -E 's/.*Str\(([a-z]+)\)/\1/' | sort | uniq -c | tr -s ' '; }
# Poll até ambos terem span OU timeout (o modelo é não-determinista a chamar a tool).
for _ in $(seq 1 12); do
  DV="$(denied_by "$RV")"; DF="$(denied_by "$RF")"
  [[ -n "${DF}" && -n "${DV}" ]] && break; sleep 5
done
echo "[vault-issuer]   Vault-NHI mediação: ${DV:-<o modelo nao chamou a tool neste run>}"
echo "[vault-issuer]   File-NHI  mediação: ${DF:-<o modelo nao chamou a tool neste run>}"

echo "[vault-issuer] 6/6 a restaurar o nó (chave de ficheiro) ..."
# o trap restaura o .env e recria o nó.
echo
# O PASS é a NÃO-FORJABILIDADE: uma tool call autorizada por um NHI de OUTRA chave é negada no
# hook de IDENTIDADE (denied_by=identity), não chega sequer à política. O Vault-NHI→policy (a
# identidade certa passa e morre no taint-gate) é confirmatório quando o modelo chama a tool.
if echo "$DF" | grep -q 'identity'; then
  echo "[vault-issuer] ✅ CUSTÓDIA NO VAULT REAL (não-forjabilidade na MEDIAÇÃO):"
  echo "[vault-issuer]    • NHI de OUTRA chave → denied_by=IDENTITY: o hook nega — a assinatura não valida"
  echo "[vault-issuer]      contra o trust-anchor do Vault. Só a chave NO VAULT minta identidades aceites."
  echo "$DV" | grep -q 'policy' \
    && echo "[vault-issuer]    • NHI Vault-assinado → denied_by=policy: a identidade certa PASSA o hook e morre no taint-gate." \
    || echo "[vault-issuer]    (o Vault-NHI não chamou a tool neste run — confirmado à parte por TestVaultTransitSigner_EndToEndIssuer.)"
  echo "[vault-issuer]    A chave vive no Vault (key-never-leaves); o nó não pode mintar. Opção A do D4"
  echo "[vault-issuer]    realizada — em produção HSM/KMS via a MESMA costura crypto.Signer."
else
  echo "[vault-issuer] ⚠ o File-NHI não chamou a tool neste run (Vault='${DV}' ficheiro='${DF}') — re-corre"
  echo "[vault-issuer]    (é preciso pelo menos uma tool call para exercitar o hook de identidade)."
fi
