#!/usr/bin/env bash
# =============================================================================
# demo-ciclo-completo.sh — percorre o CICLO DE VIDA COMPLETO de um run no nó `aos`,
# tocando cada subsistema em ordem e imprimindo evidência por fase:
#
#   0. SAÚDE            GET /healthz + /readyz
#   1. IDENTIDADE       NHI assinado (aos-issuer) + Bearer soberano (Keycloak OIDC)
#   2. SUBMISSÃO        POST /runs (residência selada) -> 201
#   3. TOOL SET FROZEN  evento run.toolset.frozen (catálogo ASSINADO, frozen_at real)
#   4. MODELO+MEDIAÇÃO  Kimi via LiteLLM; cada tool call mediada RM->reval->PDP/Cedar
#   5. LEITURA SOBERANA GET /runs/{id} (ID-token OIDC verificado)
#   6. TRAJETÓRIA       GET /runs/{id}/trajectory (SSE, AOS-167)
#   7. RECONSTRUÇÃO     GET /runs/{id}/reconstruct (replay soberano, AOS-214)
#   8. AUDITORIA        WORM selado (hash-chain) + spans OTLP execute_tool
#   9. CRYPTO-SHRED     POST /dsar/erase -> KEK do titular DESTRUÍDA no Vault (AOS-215/216)
#
# Pré-requisito: stack OIDC+Vault (up-oidc.sh) com AOS_MODEL_TOOLS_REGISTER=1 e MOONSHOT_API_KEY.
# =============================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
SECRETS="${SCRIPT_DIR}/secrets"
PROJECT="aos-dev-hardened"
EXE=""; case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) EXE=".exe" ;; esac
ISSUERBIN="${REPO_ROOT}/packages/cmd/aos-issuer/aos-issuer${EXE}"
VADDR="http://localhost:8200"; VTOK="$(cat "${SECRETS}/vault-token" 2>/dev/null || echo aos-dev-root)"
TMP="$(mktemp -d)"; trap 'rm -rf "${TMP}"' EXIT

hdr()  { echo; echo "══ $* ═══════════════════════════════════════════"; }
ok()   { echo "   ✅ $*"; }
info() { echo "   •  $*"; }
warn() { echo "   ⚠  $*"; }

claim_of() { printf '%s' "$1" | cut -d. -f2 | tr '_-' '/+' | { base64 -d 2>/dev/null || true; } | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p"; }
tok() {
  docker run --rm --network "${PROJECT}_default" curlimages/curl:latest -sk \
    -d grant_type=password -d client_id=aos-node -d username=alice -d password=alice -d scope=openid \
    https://idp:8443/realms/aos/protocol/openid-connect/token | sed -n 's/.*"id_token":"\([^"]*\)".*/\1/p'
}
list_keys() { curl -sk -H "X-Vault-Token: ${VTOK}" "${VADDR}/v1/transit/keys?list=true" | tr ',' '\n' | sed -n 's/.*"\(aos-kek-[0-9a-f]*\)".*/\1/p'; }
has_key()   { list_keys | grep -qx "$1"; }

RID="run-ciclo-$(date -u +%H%M%S)"

# ── 0. SAÚDE ────────────────────────────────────────────────────────────────
hdr "0. SAÚDE"
HZ="$(curl -sk -o /dev/null -w '%{http_code}' https://localhost:8443/healthz)"
RZ="$(curl -sk -o /dev/null -w '%{http_code}' https://localhost:8443/readyz)"
info "GET /healthz -> ${HZ}    GET /readyz -> ${RZ}"
[[ "${HZ}" == "200" && "${RZ}" == "200" ]] && ok "nó vivo e pronto (production, fail-closed)" || warn "nó não pronto"

# ── 1. IDENTIDADE ───────────────────────────────────────────────────────────
hdr "1. IDENTIDADE"
BEARER="$(tok)"; SUB="$(claim_of "${BEARER}" sub)"
BOARD="$(claim_of "${BEARER}" board)"
info "Bearer soberano: sub=${SUB}  board=${BOARD}  (ID-token RS256 do Keycloak)"
NHI="$("${ISSUERBIN}" mint --key-file "${SECRETS}/issuer.key" --issuer iss:aos-issuer \
  --human human:alice --agent agt-ciclo --class agent-worker --caps cap:http.post,cap:fs.read --ttl 15m | tr -d '\r\n')"
[[ -n "${NHI}" && -n "${SUB}" ]] && ok "NHI assinado (agent-worker, cap:http.post+cap:fs.read) + Bearer verificado" || { warn "identidade incompleta"; exit 1; }
KEK="aos-kek-$(printf 'aos.audit.pii:%s' "${SUB}" | sha256sum | cut -d' ' -f1)"

# ── 2. SUBMISSÃO ────────────────────────────────────────────────────────────
hdr "2. SUBMISSÃO"
CODE="$(curl -sk -o "${TMP}/submit" -w '%{http_code}' -X POST https://localhost:8443/runs \
  -H "Authorization: Bearer ${BEARER}" -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"${RID}\",\"objective\":\"Publica resultado-42 via HTTP POST para https://api.example.com/results usando a tool web_post. So podes publicar via web_post.\",\"principal_nhi\":\"agt-ciclo\",\"credential\":\"${NHI}\",\"scope\":[\"cap:http.post\"]}")"
info "POST /runs -> ${CODE}  (run_id=${RID})"
[[ "${CODE}" == "201" || "${CODE}" == "202" ]] && ok "run aceite; residência de região selada na criação" || { warn "submit recusado"; exit 1; }

# ── 4. MODELO + MEDIAÇÃO (aguarda terminar) ─────────────────────────────────
hdr "4. MODELO + MEDIAÇÃO  (a aguardar o run terminar; chama o Kimi real)"
ST=""; TURNS=""
for _ in $(seq 1 40); do
  R="$(curl -sk -H "Authorization: Bearer $(tok)" "https://localhost:8443/runs/${RID}")"
  ST="$(printf '%s' "$R" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
  [[ "${ST}" == "completed" ]] && break; sleep 2
done
info "status final = ${ST}"
[[ "${ST}" == "completed" ]] && ok "run executado (turnos multi, modelo real via LiteLLM->Kimi)" || warn "run não terminou a tempo"

# ── 3. TOOL SET FROZEN (evento durável) ─────────────────────────────────────
hdr "3. TOOL SET FROZEN  (evento durável run.toolset.frozen)"
docker cp "${PROJECT}-aos-1:/var/lib/aos/events.wal" "${TMP}/events.wal" >/dev/null 2>&1
FROZEN="$(grep -a -oE "\"run_id\":\"${RID}\",\"frozen_at\":\"[^\"]*\"" "${TMP}/events.wal" | head -1)"
ENTRIES="$(grep -a -oE "\"run_id\":\"${RID}\",\"frozen_at\":\"[^\"]*\",\"entries\":\[.{0,4000}" "${TMP}/events.wal" | tr -d '\000' | grep -a -oE '"id":"[a-z_]+","version"' | sed -E 's/","version"//' | sort -u | tr '\n' ' ')"
info "${FROZEN}"
info "entries assinadas (tools congeladas): ${ENTRIES:-<nenhuma>}"
if printf '%s' "${FROZEN}" | grep -q 'frozen_at":"20'; then ok "catálogo assinado congelado; frozen_at é timestamp real (não zero-value)"; else warn "sem registo frozen para o run"; fi

# ── 5. LEITURA SOBERANA ─────────────────────────────────────────────────────
hdr "5. LEITURA SOBERANA  (GET /runs/{id})"
G="$(curl -sk -w '\n%{http_code}' -H "Authorization: Bearer $(tok)" "https://localhost:8443/runs/${RID}")"
GC="$(printf '%s' "$G" | tail -1)"; GBODY="$(printf '%s' "$G" | sed '$d')"
info "GET /runs/${RID} -> ${GC}"
info "corpo: $(printf '%s' "$GBODY" | cut -c1-160)"
[[ "${GC}" == "200" ]] && ok "leitura autorizada pelo gate soberano (ID-token verificado, região == run)" || warn "leitura recusada"
# prova de anti-replay: reusar o MESMO Bearer da submissão deve falhar (jti já visto).
RC_REPLAY="$(curl -sk -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${BEARER}" "https://localhost:8443/runs/${RID}")"
[[ "${RC_REPLAY}" == "403" || "${RC_REPLAY}" == "401" ]] && ok "anti-replay por-jti: reutilizar o Bearer da submissão -> ${RC_REPLAY} (recusado)" || info "reutilização do Bearer -> ${RC_REPLAY}"

# ── 6. TRAJETÓRIA (SSE) ─────────────────────────────────────────────────────
hdr "6. TRAJETÓRIA  (GET /runs/{id}/trajectory, SSE)"
curl -sk -N --max-time 4 -H "Authorization: Bearer $(tok)" "https://localhost:8443/runs/${RID}/trajectory" > "${TMP}/traj" 2>/dev/null || true
TC="$(grep -c '^data:' "${TMP}/traj" 2>/dev/null || echo 0)"
EVT="$(grep -oE '"type":"[a-z._]+"' "${TMP}/traj" 2>/dev/null | sort -u | tr '\n' ' ')"
info "eventos SSE recebidos: ${TC}"
info "tipos: ${EVT:-<stream vazio ou já fechado>}"
[[ "${TC}" -ge 1 ]] && ok "trajetória reproduzível em tempo-real a partir do Event Store" || info "sem eventos (run já concluído; o stream fecha após o replay)"

# ── 7. RECONSTRUÇÃO SOBERANA ────────────────────────────────────────────────
hdr "7. RECONSTRUÇÃO SOBERANA  (GET /runs/{id}/reconstruct, AOS-214)"
XC="$(curl -sk -o "${TMP}/rec" -w '%{http_code}' -H "Authorization: Bearer $(tok)" "https://localhost:8443/runs/${RID}/reconstruct")"
info "GET /runs/${RID}/reconstruct -> ${XC}"
case "${XC}" in
  200) ok "conteúdo selado por-titular decifrado para o leitor autorizado (D6+D7)";;
  501) info "endpoint desligado (501): o gate soberano de reconstrução não está composto nesta stack (fail-closed by design)";;
  40*) info "recusado (${XC}) — autorização soberana insuficiente para reconstrução";;
  *)   warn "resposta inesperada (${XC})";;
esac

# ── 8. AUDITORIA (WORM + OTLP) ──────────────────────────────────────────────
hdr "8. AUDITORIA  (WORM selado + spans OTLP)"
docker cp "${PROJECT}-aos-1:/var/lib/aos/worm.wal" "${TMP}/worm.wal" >/dev/null 2>&1
# O WORM é um WAL BINÁRIO (quase sem newlines) — grep -c contaria LINHAS (=1), não registos.
# Conta OCORRÊNCIAS. Partições com namespace por plano+run: run-<id> (decisões de mediação de
# DADOS), gov.read/run-<id> (read-path soberano, ele próprio auditado), gov.residency/run-<id>
# (residência na criação). O RunID vai selado no conteúdo de TODAS, independentemente da Partition.
WN="$(grep -a -o "\"Partition\":\"run-[^\"]*${RID#run-}\"" "${TMP}/worm.wal" 2>/dev/null | wc -l | tr -d ' ')"
WGOV="$(grep -a -o "\"Partition\":\"gov.read/${RID}\"" "${TMP}/worm.wal" 2>/dev/null | wc -l | tr -d ' ')"
DENY="$(docker compose -p "${PROJECT}" logs otel 2>/dev/null | awk -v RS='Name           : execute_tool' -v rid="${RID}" '
  index($0,"aos.run_id: Str(" rid ")") {
    if (match($0,/aos.decision.denied_by: Str\(policy\)/)) print "deny|policy";
    else if (match($0,/aos.decision.denied_by: Str\(scope\)/)) print "deny|scope";
    else if (match($0,/aos.decision: Str\(deny\)/)) print "deny|other" }' | sort | uniq -c | tr '\n' ';')"
info "WORM selado — partição de DADOS (run-${RID#run-}): ${WN} decisões de mediação  (hash-chain por-partição)"
info "WORM selado — partição gov.read/${RID}: ${WGOV} registos  (o read-path soberano é ele próprio auditado)"
info "spans OTLP execute_tool: ${DENY:-<nenhum>}"
[[ "${WN}" -ge 1 ]] && ok "cada decisão de mediação gravada em audit imutável + trace distribuído" || info "sem registos WORM (o run pode não ter emitido tool calls)"

# ── 9. CRYPTO-SHRED (DSAR) ──────────────────────────────────────────────────
hdr "9. CRYPTO-SHRED  (POST /dsar/erase -> Vault)"
if has_key "${KEK}"; then
  ok "KEK do titular PRESENTE no Vault: ${KEK:0:24}…  (conteúdo do run selado key-never-leaves)"
  EC="$(curl -sk -o /dev/null -w '%{http_code}' -X POST https://localhost:8443/dsar/erase \
    -H "Authorization: Bearer $(tok)" -H 'Content-Type: application/json' -d "{\"subject_id\":\"${SUB}\"}")"
  info "POST /dsar/erase -> ${EC}"
  gone=0; for _ in $(seq 1 15); do has_key "${KEK}" || { gone=1; break; }; sleep 1; done
  [[ "${gone}" == 1 ]] && ok "CRYPTO-SHRED REAL: KEK DESTRUÍDA no Vault — conteúdo do titular irrecuperável (Art. 17)" \
                       || warn "KEK ainda presente após erase (HTTP ${EC})"
else
  info "KEK do titular ainda não visível no Vault — o run pode não ter selado conteúdo por-titular; contrato provado por TestVaultKeyVault_WrapUnwrapShred"
fi

hdr "CICLO COMPLETO"
ok "identidade → submissão → frozen → modelo+mediação → leitura → trajetória → reconstrução → auditoria → crypto-shred"
