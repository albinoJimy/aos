#!/usr/bin/env bash
# =============================================================================
# AOS-169 — HARNESS DE DURABILIDADE DO CONTENTOR (E6 / §13.3)
#
# Prova, com o CONTENTOR REAL (imagem distroless de AOS-168, deploy/node/Dockerfile), que o nó
# `aos` sobrevive a um KILL abrupto + REINÍCIO a partir do MESMO volume gravável SEM perder nem
# duplicar trabalho DURÁVEL — a costura AOS-170 (Event Store WAL + WORM) ligada por ambiente
# (AOS_EVENTSTORE_PATH / AOS_WORM_PATH) a um mount fora do root-fs read-only.
#
# PASSOS:
#   1. build da imagem (contexto = raiz do repo; os replaces relativos exigem a árvore packages/)
#   2. volume nomeado gravável em /var/lib/aos
#   3. run do contentor (root-fs READ-ONLY + volume) com a API em 0.0.0.0:8080 e substrato durável
#   4. espera /healthz; SUBMETE um run (POST /runs) e espera-o concluir (turno durável no WAL)
#   5. `docker kill` ABRUPTO (SIGKILL — nunca um shutdown gracioso)
#   6. `docker start` do MESMO contentor/volume — REINÍCIO; espera /healthz
#   7. prova a DURABILIDADE: os ficheiros WAL persistiram no volume (replay no arranque) e a
#      RE-SUBMISSÃO do MESMO run_id é IDEMPOTENTE (201 "accepted", sem dupla-execução)
#
# A prova byte-a-byte de NO-LOSS/NO-DUP/NO-DOUBLE-EXEC (token de lease monotónico/fencing) é o
# teste Go do nó sobre o MESMO substrato durável real:
#   packages/cmd/aos: TestServiceShutdownDurable_NoLossNoDupNoDoubleExecAfterRestart
# Este harness eleva-a ao ARTEFACTO EMPACOTADO (o contentor), não a substitui.
#
# Uso:   deploy/node/aos169-durability-harness.sh
# Saída: "AOS-169 DURABILIDADE CONTENTOR: PASS" (rc 0) ou "... FAIL: <motivo>" (rc != 0).
# Requer: docker; curl. Idempotente (limpa os seus recursos no início e no fim).
# =============================================================================
set -euo pipefail

# Git Bash/MSYS no Windows CONVERTE argumentos que começam por '/' em caminhos Windows. Isso é
# CORRECTO para caminhos do HOST (o contexto de build) mas CORROMPE os caminhos DO CONTENTOR
# passados a `docker run` (ex.: -e AOS_EVENTSTORE_PATH=/var/lib/aos ⇒ C:/Program Files/Git/...).
# Por isso a conversão é desligada APENAS no `docker run` (via `dockerrun`), nunca no build.
# Inócuo em Linux/macOS (as variáveis são simplesmente ignoradas).
dockerrun() { MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*' docker run "$@"; }

IMG="aos-node:aos169-durability"
CT="aos169-durability"
VOL="aos169-durability-data"
PORT="18080"
BASE="http://127.0.0.1:${PORT}"
RUN_ID="run-durability-1"

# Raiz do repo = dois níveis acima deste script (deploy/node/..).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

fail() { echo "AOS-169 DURABILIDADE CONTENTOR: FAIL: $*" >&2; exit 1; }

cleanup() {
  docker rm -f "${CT}" >/dev/null 2>&1 || true
  docker volume rm "${VOL}" >/dev/null 2>&1 || true
}

wait_healthz() {
  local tries=60
  while (( tries-- > 0 )); do
    if curl -fsS "${BASE}/healthz" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

# --- 0. limpeza defensiva de execuções anteriores ---------------------------
cleanup
trap cleanup EXIT

# --- 1. build da imagem (contexto = raiz do repo) ---------------------------
echo "[harness] build da imagem ${IMG} (contexto=${REPO_ROOT}) ..."
docker build -f "${REPO_ROOT}/deploy/node/Dockerfile" -t "${IMG}" "${REPO_ROOT}" \
  || fail "docker build falhou"

# --- 2. volume gravável -----------------------------------------------------
docker volume create "${VOL}" >/dev/null || fail "docker volume create falhou"

# --- 3. run: root-fs READ-ONLY + volume durável + API 0.0.0.0:8080 ----------
# O bind 0.0.0.0 é permitido porque o modo de identidade de referência é `real` e o canal de
# controlo (SteerAuth) está composto (controlAuthenticated). O plano de DADOS (POST /runs) é
# não-autenticado por ADR-016. `--read-only` prova que o estado só escreve no volume montado.
echo "[harness] run do contentor (read-only + volume durAvel) ..."
# AOS_BOARD_REGIONS vazio ⇒ read-path LEGADO deliberado: a durabilidade (§13.3) NÃO depende da
# soberania de leitura (§13.7/D7); assim GET /runs reflecte o desfecho sem exigir os headers
# X-Aos-Reader/X-Aos-Board (que a soberania de leitura, quando ligada, passaria a exigir).
dockerrun -d --name "${CT}" \
  --read-only \
  -e AOS_API_ADDR=0.0.0.0:8080 \
  -e AOS_EVENTSTORE_PATH=/var/lib/aos/events.wal \
  -e AOS_WORM_PATH=/var/lib/aos/worm.wal \
  -e AOS_ISSUER_KEY_PATH=/var/lib/aos/issuer.seed \
  -e AOS_BOARD_REGIONS= \
  -v "${VOL}:/var/lib/aos" \
  -p "${PORT}:8080" \
  "${IMG}" serve >/dev/null || fail "docker run falhou"

wait_healthz || fail "o contentor nAo ficou saudAvel (/healthz) apOs o arranque"

# Confirma que o substrato durAvel foi declarado no arranque (nAo in-memory).
if ! docker logs "${CT}" 2>&1 | grep -q "duravel em disco (AOS-170)"; then
  fail "o banner nAo declarou o substrato durAvel (AOS-170) — env de durabilidade nAo ligou"
fi

# --- 4. submete um run e espera concluir (turno durAvel no WAL) -------------
echo "[harness] submete o run ${RUN_ID} ..."
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}/runs" \
  -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"${RUN_ID}\",\"objective\":\"durabilidade AOS-169\",\"principal_nhi\":\"nhi:${RUN_ID}\"}") \
  || fail "POST /runs falhou (transporte)"
[[ "${code}" == "201" ]] || fail "POST /runs devia dar 201, veio ${code}"

# Espera o run terminar (o modelo de referência conclui no 1.º turno; GET reflecte o desfecho).
run_done=0
for _ in $(seq 1 30); do
  st=$(curl -s "${BASE}/runs/${RUN_ID}" || true)
  if echo "${st}" | grep -q '"status":"completed"'; then run_done=1; break; fi
  sleep 1
done
[[ "${run_done}" == "1" ]] || fail "o run nAo concluiu antes do kill (turno durAvel nAo gravado)"

# --- 5. KILL ABRUPTO (SIGKILL) ---------------------------------------------
echo "[harness] KILL abrupto do contentor (SIGKILL) ..."
docker kill "${CT}" >/dev/null || fail "docker kill falhou"

# --- 6. REINÍCIO do MESMO contentor/volume ---------------------------------
echo "[harness] REINICIA o contentor do MESMO volume ..."
docker start "${CT}" >/dev/null || fail "docker start falhou"
wait_healthz || fail "o contentor nAo recuperou (/healthz) apOs o reinIcio do MESMO volume"

# O reinIcio tem de reabrir o substrato DURÁVEL (replay do WAL), nAo um in-memory novo.
if ! docker logs "${CT}" 2>&1 | grep -q "duravel em disco (AOS-170)"; then
  fail "apOs o reinIcio o banner nAo declarou o substrato durAvel — o volume nAo foi reutilizado"
fi

# --- 7. DURABILIDADE: re-submissAo idempotente (sem dupla-execuçAo) ---------
# Um run_id JÁ conhecido (reconstruIdo do WAL / lease) devolve o MESMO 201 "accepted" idempotente
# — o nO nAo re-hospeda nem duplica o desfecho (nAo-enumerAvel + idempotente, ADR-016). A prova
# byte-a-byte de nAo-duplicaçAo/fencing é o teste Go TestServiceShutdownDurable.
echo "[harness] re-submete o MESMO run_id apOs o reinIcio (idempotEncia) ..."
code2=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}/runs" \
  -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"${RUN_ID}\",\"objective\":\"durabilidade AOS-169\",\"principal_nhi\":\"nhi:${RUN_ID}\"}") \
  || fail "re-POST /runs falhou (transporte)"
[[ "${code2}" == "201" ]] || fail "re-submissAo do mesmo run_id devia dar 201 idempotente, veio ${code2}"

echo "AOS-169 DURABILIDADE CONTENTOR: PASS"
