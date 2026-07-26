#!/usr/bin/env bash
# =============================================================================
# AOS-193 — HARNESS DO PLANO DE CONTROLO NO CONTENTOR (achados ORF-02 + STR-04)
#
# Prova, com o CONTENTOR REAL (imagem distroless de AOS-168, deploy/node/Dockerfile), que o
# plano de controlo do nó é OPERÁVEL a partir do binário entregue — sem forkar nem recompilar:
#
#   PROVA NEGATIVA (1): o nó com AOS_API_ADDR=0.0.0.0:8080 e ZERO operadores RECUSA arrancar
#     (`ErrRefuseNonLoopbackBind`, exit != 0). Antes de AOS-193 este arranque era PERMITIDO —
#     o bind-guardrail exigia só (SteerAuth != nil ∧ identidade real), duas condições que o
#     Bootstrap satisfaz SEMPRE, pelo que o predicado nunca recusava nada.
#
#   PROVA POSITIVA (2): o MESMO contentor, com `-e AOS_OPERATORS="<id>=<hexpubkey>"`, arranca
#     no MESMO bind e ACEITA um `aos steer` assinado por esse operador.
#
#   PROVA NEGATIVA (3): no MESMO nó, um steer assinado por um emissor NÃO registado é RECUSADO
#     (403) — o caminho continua fail-closed, não se abriu uma porta.
#
#   ROSTER DE APROVADORES (4): o MESMO contentor monta um `approvers.json` read-only e recebe
#     `-e AOS_APPROVERS_FILE=/etc/aos/approvers.json`; o banner declara o gate COMPOSTO e
#     `POST /runs/{id}/approve` deixa de responder 501 (passa a JULGAR: 403 sem pernas válidas).
#     A prova POSITIVA do 200 é in-process (TestApproversFileAuthorizesDualControlEndToEnd) —
#     assinar duas pernas ed25519 aqui exigiria uma ferramenta de assinatura que a CLI do nó
#     não expõe (a assinatura do aprovador é do dispositivo do humano, ADR-016/AOS-162).
#
#   ROSTER INVÁLIDO (5): o mesmo ficheiro com a MESMA pubkey em dois principals — o roster que
#     anularia o dual-control com UMA chave privada — faz o contentor RECUSAR arrancar.
#
# CUSTÓDIA DA CHAVE. A chave privada do operador é gerada e mantida NA MÁQUINA DO OPERADOR (aqui,
# um ficheiro temporário do host, apagado no fim) e usada por um `aos` compilado no host, que é o
# papel real da CLI: cliente que assina. Ao CONTENTOR só entra a PUBKEY, por variável de ambiente.
# Nenhum material privado entra na imagem, no ambiente do nó ou nos logs.
#
# Uso:   deploy/node/aos193-control-plane-harness.sh
# Saída: "AOS-193 PLANO DE CONTROLO: PASS" (rc 0) ou "... FAIL: <motivo>" (rc != 0).
# Requer: docker; curl; go (para compilar a CLI do lado do operador). Idempotente.
# =============================================================================
set -euo pipefail

# Ver a nota de MSYS_NO_PATHCONV no aos169-durability-harness.sh: os caminhos DO CONTENTOR não
# podem ser convertidos pelo Git Bash. Há UM bind-mount de host — o `approvers.json`, que contém
# exclusivamente material PÚBLICO (ver `hostpath` para o lado do host); nenhuma chave privada
# entra em contentor algum.
dockerrun() { MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*' docker run "$@"; }

IMG="aos-node:aos193-control-plane"
CT="aos193-control-plane"
PORT="18193"
BASE="http://127.0.0.1:${PORT}"
RUN_ID="run-aos193-control"
EMITTER="ops:harness"
ROGUE_EMITTER="ops:rogue"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

EXE=""
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) EXE=".exe" ;; esac
WORKDIR="$(mktemp -d)"
AOSBIN="${WORKDIR}/aos-cli${EXE}"
KEYFILE="${WORKDIR}/operator.seed"
ROGUEKEY="${WORKDIR}/rogue.seed"
APPROVER_A_KEY="${WORKDIR}/approver-a.seed"
APPROVER_B_KEY="${WORKDIR}/approver-b.seed"
APPROVERS_JSON="${WORKDIR}/approvers.json"        # SÓ material público (pubkeys)
APPROVERS_BAD_JSON="${WORKDIR}/approvers-bad.json"

fail() { echo "AOS-193 PLANO DE CONTROLO: FAIL: $*" >&2; exit 1; }

cleanup() {
  docker rm -f "${CT}" >/dev/null 2>&1 || true
  docker rm -f "${CT}-bad" >/dev/null 2>&1 || true
  rm -rf "${WORKDIR}" 2>/dev/null || true   # a chave privada do operador NÃO fica para trás
}
trap cleanup EXIT

wait_healthz() {
  local tries=60
  while (( tries-- > 0 )); do
    if curl -fsS "${BASE}/healthz" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

# gen_seed escreve 32 bytes CSPRNG em hex (o formato de seed que `aos steer` lê) no ficheiro dado.
gen_seed() {
  local out="$1"
  head -c 32 /dev/urandom | od -An -v -tx1 | tr -d ' \n' > "${out}"
  local n; n=$(tr -d '\n' < "${out}" | wc -c | tr -d ' ')
  [[ "${n}" == "64" ]] || fail "seed gerada com ${n} chars (esperado 64 hex = 32 bytes)"
  chmod 600 "${out}" 2>/dev/null || true
}

# hostpath converte um caminho do shell para a forma que o daemon Docker aceita como origem de
# bind-mount. Em Git Bash/MSYS o `-v /tmp/...` NÃO é compreendido pelo Docker Desktop (o /tmp é
# do MSYS, não do Windows); `cygpath -w` dá o caminho Windows real. Noutros SO é a identidade.
hostpath() {
  case "$(uname -s)" in
    MINGW* | MSYS* | CYGWIN*) cygpath -w "$1" ;;
    *) printf '%s' "$1" ;;
  esac
}

# pubkey_of imprime SÓ a pubkey hex derivada de uma seed (o `aos operator-pubkey` imprime
# "<id>=<hex>"; aqui queremos o valor para o campo "pubkey" do roster de aprovadores).
pubkey_of() {
  local entry
  entry="$("${AOSBIN}" operator-pubkey --key "$1" --emitter tmp)" || fail "operator-pubkey falhou"
  echo "${entry}" | tr -d '\r\n' | sed 's/^tmp=//'
}

# --- 0. limpeza defensiva ----------------------------------------------------
docker rm -f "${CT}" >/dev/null 2>&1 || true
docker rm -f "${CT}-bad" >/dev/null 2>&1 || true

# --- 1. build da imagem (contexto = raiz do repo) ---------------------------
echo "[harness] build da imagem ${IMG} (contexto=${REPO_ROOT}) ..."
docker build -f "${REPO_ROOT}/deploy/node/Dockerfile" -t "${IMG}" "${REPO_ROOT}" \
  || fail "docker build falhou"

# --- 2. CLI do lado do OPERADOR + chaves (nunca entram no contentor) --------
echo "[harness] compila a CLI do operador e gera as chaves (host) ..."
( cd "${REPO_ROOT}/packages/cmd/aos" && go build -o "${AOSBIN}" ./ ) || fail "go build da CLI falhou"
gen_seed "${KEYFILE}"
gen_seed "${ROGUEKEY}"

OP_ENTRY="$("${AOSBIN}" operator-pubkey --key "${KEYFILE}" --emitter "${EMITTER}")" \
  || fail "aos operator-pubkey falhou"
OP_ENTRY="$(echo "${OP_ENTRY}" | tr -d '\r\n')"
[[ "${OP_ENTRY}" == "${EMITTER}="* ]] || fail "operator-pubkey devia imprimir '${EMITTER}=<hex>', veio '${OP_ENTRY}'"
echo "[harness] entrada de operador (SO material publico): ${OP_ENTRY}"

# --- 2b. ROSTER DE APROVADORES (só pubkeys; as privadas ficam no host) ------
gen_seed "${APPROVER_A_KEY}"
gen_seed "${APPROVER_B_KEY}"
AP_A_PUB="$(pubkey_of "${APPROVER_A_KEY}")"
AP_B_PUB="$(pubkey_of "${APPROVER_B_KEY}")"
[[ ${#AP_A_PUB} == 64 && ${#AP_B_PUB} == 64 ]] || fail "pubkeys de aprovador com tamanho errado"
[[ "${AP_A_PUB}" != "${AP_B_PUB}" ]] || fail "as duas pubkeys de aprovador tinham de ser DISTINTAS"
cat > "${APPROVERS_JSON}" <<EOF
{"approvers":[
  {"principal":"human:alice","pubkey":"${AP_A_PUB}","authority":["approve:danger"]},
  {"principal":"human:bob","pubkey":"${AP_B_PUB}","authority":["approve:danger","approve:gray"]}
]}
EOF
# 0644: o contentor corre como UID 65532 (non-root) e tem de conseguir LER o ficheiro montado.
# É material exclusivamente PÚBLICO — nenhuma seed entra neste ficheiro.
chmod 644 "${APPROVERS_JSON}"
if grep -qi "private\|seed\|secret" "${APPROVERS_JSON}"; then
  fail "o roster de aprovadores NAO pode conter material privado"
fi
# roster INVÁLIDO: a MESMA pubkey em dois principals (o bypass do 4-eyes por copy-paste).
cat > "${APPROVERS_BAD_JSON}" <<EOF
{"approvers":[
  {"principal":"human:alice","pubkey":"${AP_A_PUB}","authority":["approve:danger"]},
  {"principal":"human:bob","pubkey":"${AP_A_PUB}","authority":["approve:danger"]}
]}
EOF
chmod 644 "${APPROVERS_BAD_JSON}"
echo "[harness] roster de aprovadores escrito (2 pubkeys distintas, SO material publico)"

# --- 3. PROVA NEGATIVA: 0.0.0.0 com ZERO operadores RECUSA arrancar ---------
echo "[harness] PROVA NEGATIVA: arranque em 0.0.0.0 SEM operadores ..."
set +e
neg_out=$(dockerrun --rm --name "${CT}-neg" \
  --read-only \
  -e AOS_API_ADDR=0.0.0.0:8080 \
  -e AOS_BOARD_REGIONS= \
  "${IMG}" serve 2>&1)
neg_rc=$?
set -e
echo "${neg_out}" | sed 's/^/[neg] /'
(( neg_rc != 0 )) || fail "o no com ZERO operadores NAO devia conseguir servir em 0.0.0.0 (exit ${neg_rc})"
echo "${neg_out}" | grep -q "RECUSADO" \
  || fail "a recusa devia nomear o bind-guardrail (ErrRefuseNonLoopbackBind), veio: ${neg_out}"
echo "${neg_out}" | grep -q "SEM OPERADORES" \
  || fail "o banner devia declarar o canal de controlo SEM OPERADORES, veio: ${neg_out}"

# --- 4. PROVA POSITIVA: o MESMO bind, agora com um operador registado -------
echo "[harness] PROVA POSITIVA: arranque em 0.0.0.0 COM o operador registado ..."
dockerrun -d --name "${CT}" \
  --read-only \
  -e AOS_API_ADDR=0.0.0.0:8080 \
  -e AOS_OPERATORS="${OP_ENTRY}" \
  -e AOS_APPROVERS_FILE=/etc/aos/approvers.json \
  -e AOS_BOARD_REGIONS= \
  -v "$(hostpath "${APPROVERS_JSON}"):/etc/aos/approvers.json:ro" \
  -p "${PORT}:8080" \
  "${IMG}" serve >/dev/null || fail "docker run falhou"

wait_healthz || fail "o contentor nao ficou saudavel (/healthz) — o bind 0.0.0.0 devia ser PERMITIDO com um operador"
docker logs "${CT}" 2>&1 | grep -q "1 operador(es) registado(s) via AOS_OPERATORS" \
  || fail "o banner nao declarou o operador registado a partir de AOS_OPERATORS"
docker logs "${CT}" 2>&1 | grep -q "four-eyes gate (AOS-162) composto: 2 aprovador(es) pinado(s) via AOS_APPROVERS_FILE" \
  || fail "o banner nao declarou o four-eyes composto a partir do ficheiro montado"

# 4a. submete um run (plano de DADOS, nao-autenticado por ADR-016).
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}/runs" \
  -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"${RUN_ID}\",\"objective\":\"plano de controlo AOS-193\",\"principal_nhi\":\"nhi:${RUN_ID}\"}") \
  || fail "POST /runs falhou (transporte)"
[[ "${code}" == "201" ]] || fail "POST /runs devia dar 201, veio ${code}"

# 4b. STEER ASSINADO pelo operador registado ⇒ ACEITE pelo contentor.
echo "[harness] aos steer assinado pelo operador registado ..."
"${AOSBIN}" steer --addr "${BASE}" --run-id "${RUN_ID}" \
  --emitter "${EMITTER}" --key "${KEYFILE}" --correction "aperta o ambito ao ticket" \
  || fail "o steer assinado pelo operador REGISTADO devia ser ACEITE pelo contentor"

# --- 5. PROVA NEGATIVA (2): emissor NAO registado continua RECUSADO --------
echo "[harness] steer de um emissor NAO registado (tem de ser recusado) ..."
set +e
rogue_out=$("${AOSBIN}" steer --addr "${BASE}" --run-id "${RUN_ID}" \
  --emitter "${ROGUE_EMITTER}" --key "${ROGUEKEY}" --correction "correccao maliciosa" 2>&1)
rogue_rc=$?
set -e
(( rogue_rc != 0 )) || fail "um emissor NAO registado NAO podia ser aceite (o canal deixou de ser fail-closed)"
echo "${rogue_out}" | grep -q "403" || fail "a recusa devia ser 403, veio: ${rogue_out}"

# --- 6. /approve: composto pelo ficheiro montado (deixou de ser 501) --------
echo "[harness] POST /approve com o roster montado (tem de JULGAR, nao 501) ..."
ap_code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}/runs/${RUN_ID}/approve" \
  -H 'Content-Type: application/json' \
  -d '{"request":{"request_id":"req-aos193","preview":"ZWZlaXRv","risk_class":0,"dual_control_required":true},"legs":[]}') \
  || fail "POST /approve falhou (transporte)"
[[ "${ap_code}" != "501" ]] || fail "com AOS_APPROVERS_FILE o /approve NAO pode continuar a responder 501 (endpoint inatingivel)"
[[ "${ap_code}" == "403" ]] || fail "/approve sem pernas validas devia dar 403 (fail-closed), veio ${ap_code}"

# --- 7. ROSTER INVÁLIDO: mesma pubkey em dois principals => NAO arranca -----
# É o roster que anularia o dual-control (UMA chave privada a assinar as DUAS pernas). Tem de
# abortar o ARRANQUE, não ser aceite com o banner a declarar "2 aprovador(es) pinados".
echo "[harness] roster com a MESMA pubkey em dois principals (tem de RECUSAR o arranque) ..."
# DESTACADO com espera limitada, e não um `run` bloqueante: se a guarda REGREDIR, o nó arranca
# e serve para sempre — o harness tem de FALHAR, não de pendurar.
dockerrun -d --name "${CT}-bad" \
  --read-only \
  -e AOS_API_ADDR=127.0.0.1:8080 \
  -e AOS_OPERATORS="${OP_ENTRY}" \
  -e AOS_APPROVERS_FILE=/etc/aos/approvers.json \
  -e AOS_BOARD_REGIONS= \
  -v "$(hostpath "${APPROVERS_BAD_JSON}"):/etc/aos/approvers.json:ro" \
  "${IMG}" serve >/dev/null || fail "docker run (roster invalido) falhou"
bad_status="running"
tries=30
while (( tries-- > 0 )); do
  bad_status=$(docker inspect -f '{{.State.Status}}' "${CT}-bad" 2>/dev/null || echo unknown)
  [[ "${bad_status}" == "exited" ]] && break
  sleep 1
done
bad_out=$(docker logs "${CT}-bad" 2>&1 || true)
bad_rc=$(docker inspect -f '{{.State.ExitCode}}' "${CT}-bad" 2>/dev/null || echo 0)
docker rm -f "${CT}-bad" >/dev/null 2>&1 || true
echo "${bad_out}" | sed 's/^/[bad] /'
[[ "${bad_status}" == "exited" ]] || fail "o no com roster invalido continuou a CORRER (devia ter abortado o arranque)"
(( bad_rc != 0 )) || fail "um roster com a MESMA pubkey em dois principals NAO podia arrancar (dual-control anulado)"
echo "${bad_out}" | grep -q "partilham a MESMA pubkey" \
  || fail "a recusa devia nomear a colisao de pubkey, veio: ${bad_out}"

echo "AOS-193 PLANO DE CONTROLO: PASS (0 operadores => bind 0.0.0.0 RECUSADO; 1 operador => bind permitido e steer assinado ACEITE; emissor nao registado => 403; roster montado => four-eyes composto e /approve a julgar (403, nao 501); roster com pubkey partilhada => arranque RECUSADO)"
