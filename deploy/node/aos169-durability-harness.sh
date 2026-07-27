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
#   6. INSPECÇÃO pós-kill (contentor PARADO, sem escritor concorrente): conta os TURNOS DURÁVEIS
#      do run no WAL do volume (N) — prova que o turno committed SOBREVIVEU ao kill abrupto (N>=1)
#   7. `docker start` do MESMO contentor/volume — REINÍCIO; espera /healthz + banner durável
#   8. RE-SUBMETE o MESMO run_id (⇒ 201 uniforme: idempotente-ou-fresco, indistinguível por
#      construção); dá tempo a uma eventual re-execução para gravar
#   9. `docker kill` + INSPECÇÃO final: conta de novo os turnos duráveis (M) e prova M==N — a
#      re-submissão NÃO acrescentou um turno, logo NÃO houve DUPLA-EXECUÇÃO observável no substrato
#      durável (dedup por (RunID,StepID) do WAL + fencing do lease monotónico)
#
# A INSPECÇÃO (passos 6/9) usa um contentor EFÉMERO da MESMA imagem (`aos wal-count`, subcomando
# READ-ONLY) — sem imagem externa. Corre só com o contentor principal PARADO, para o WAL não ter
# escritor concorrente. A prova byte-a-byte da monotonicidade do fencing (um token residual é
# rejeitado com ErrLeaseSuperseded) permanece no teste Go do nó sobre o MESMO substrato durável:
#   packages/cmd/aos: TestServiceShutdownDurable_NoLossNoDupNoDoubleExecAfterRestart
# Este harness ENCENA no ARTEFACTO EMPACOTADO (o contentor) a persistência + a não-duplicação
# OBSERVÁVEL (cardinalidade de turnos estável), elevando-as ao nível do nó empacotado.
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

# wal_turns conta os TURNOS DURÁVEIS (turn.recorded) do stream RUN_ID no WAL do volume, a partir
# de um contentor EFÉMERO da MESMA imagem (subcomando `aos wal-count`, read-only). NÃO usa imagem
# externa. Só é seguro com o contentor principal PARADO (sem escritor concorrente do WAL). Imprime
# o inteiro (última linha da saída do contentor efémero).
wal_turns() {
  dockerrun --rm -v "${VOL}:/var/lib/aos" "${IMG}" \
    wal-count --path /var/lib/aos/events.wal --run "${RUN_ID}" --turns 2>/dev/null | tail -n1 | tr -d '[:space:]'
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
# O bind 0.0.0.0 exige o canal de SINAIS (steer/pause) AUTENTICADO **E OPERÁVEL**: identidade real (modo de
# referência, que o Bootstrap compõe) **E pelo menos um operador com pubkey registada**
# (AOS_OPERATORS). Este harness NÃO exercita o plano de controlo — mas registar um operador é
# agora PRÉ-CONDIÇÃO do bind não-loopback, pelo que a variável tem de estar aqui.
#
# MUDANÇA DE COMPORTAMENTO (AOS-193). Até AOS-193 este `docker run` arrancava SEM AOS_OPERATORS:
# o guardrail exigia só (SteerAuth != nil ∧ identidade real), duas condições que o Bootstrap
# satisfaz SEMPRE, logo o predicado era identicamente verdadeiro e nunca recusava nada — o nó
# expunha à rede um plano de controlo que, com zero operadores, recusava TODOS os sinais. Este
# harness era, ele próprio, a prova de que a condição não discriminava. Agora discrimina.
#
# O valor de operador é gerado por CSPRNG A CADA EXECUÇÃO e NÃO é commitado: nada no repositório
# fica a parecer material de chave, e como este harness nunca ASSINA um sinal, basta-lhe uma
# entrada bem-formada (32 bytes) para satisfazer a pré-condição do bind. A prova fim-a-fim do
# plano de controlo (steer assinado e ACEITE) vive no harness irmão
# deploy/node/aos193-control-plane-harness.sh, que gera um par real no host do operador.
# O plano de DADOS (POST /runs) continua não-autenticado por ADR-016. `--read-only` prova que o
# estado só escreve no volume montado.
HARNESS_OPERATOR="ops:aos169-harness=$(head -c 32 /dev/urandom | od -An -v -tx1 | tr -d ' \n')"
echo "[harness] run do contentor (read-only + volume durAvel) ..."
# AOS_BOARD_REGIONS vazio ⇒ read-path LEGADO deliberado: a durabilidade (§13.3) NÃO depende da
# soberania de leitura (§13.7/D7); assim GET /runs reflecte o desfecho sem exigir os headers
# X-Aos-Reader/X-Aos-Board (que a soberania de leitura, quando ligada, passaria a exigir).
# AOS-209: o bind 0.0.0.0 exige agora TRANSPORTE cifrado (quarta conjuncao do bind-guardrail).
# Este harness exercita a DURABILIDADE, nao o TLS; declara a terminacao a montante
# (AOS_TLS_EXTERNAL_TERMINATION=1) para isolar o seu eixo, servindo em claro por decisao
# declarada. A prova de TLS propriamente dito e in-process (packages/cmd/aos/tls_test.go).
dockerrun -d --name "${CT}" \
  --read-only \
  -e AOS_API_ADDR=0.0.0.0:8080 \
  -e AOS_TLS_EXTERNAL_TERMINATION=1 \
  -e AOS_EVENTSTORE_PATH=/var/lib/aos/events.wal \
  -e AOS_WORM_PATH=/var/lib/aos/worm.wal \
  -e AOS_ISSUER_KEY_PATH=/var/lib/aos/issuer.seed \
  -e AOS_OPERATORS="${HARNESS_OPERATOR}" \
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

# --- 6. INSPECÇÃO pós-kill: o turno DURÁVEL sobreviveu (cardinalidade N) ----
# Contentor PARADO ⇒ sem escritor concorrente do WAL. Conta os turnos duráveis do run: >=1 prova
# que o turno committed antes do kill PERSISTIU no volume (durabilidade real, nAo sO /healthz).
turns_before=$(wal_turns) || fail "inspecçAo do WAL (pOs-kill) falhou"
echo "[harness] turnos durAveis no WAL apOs o kill: ${turns_before}"
[[ "${turns_before}" =~ ^[0-9]+$ ]] || fail "contagem de turnos invAlida (pOs-kill): '${turns_before}'"
(( turns_before >= 1 )) || fail "o turno durAvel NAO sobreviveu ao kill (turnos=${turns_before}) — perda de trabalho committed"

# --- 7. REINÍCIO do MESMO contentor/volume ---------------------------------
echo "[harness] REINICIA o contentor do MESMO volume ..."
docker start "${CT}" >/dev/null || fail "docker start falhou"
wait_healthz || fail "o contentor nAo recuperou (/healthz) apOs o reinIcio do MESMO volume"

# O reinIcio tem de reabrir o substrato DURÁVEL (replay do WAL), nAo um in-memory novo.
if ! docker logs "${CT}" 2>&1 | grep -q "duravel em disco (AOS-170)"; then
  fail "apOs o reinIcio o banner nAo declarou o substrato durAvel — o volume nAo foi reutilizado"
fi

# --- 8. RE-SUBMISSÃO do MESMO run_id (201 uniforme) ------------------------
# Um run_id jA conhecido devolve o MESMO 201 "accepted" que uma submissAo fresca — o status é
# NÃO-ENUMERÁVEL por construçAo (ADR-016), logo o 201 NÃO distingue idempotEncia de re-hospedagem.
# É por isso que a prova de nAo-duplicaçAo NÃO se apoia no cOdigo 201, mas na cardinalidade de
# turnos do WAL (passo 9). Dá-se tempo a uma EVENTUAL re-execuçAo para gravar o seu turno (se
# houvesse um bug de duplicaçAo, o turno apareceria e o passo 9 falharia).
echo "[harness] re-submete o MESMO run_id apOs o reinIcio ..."
code2=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}/runs" \
  -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"${RUN_ID}\",\"objective\":\"durabilidade AOS-169\",\"principal_nhi\":\"nhi:${RUN_ID}\"}") \
  || fail "re-POST /runs falhou (transporte)"
[[ "${code2}" == "201" ]] || fail "re-submissAo do mesmo run_id devia dar 201 uniforme, veio ${code2}"
sleep 3  # janela para uma hipotetica re-execuçAo materializar um turno duplicado no WAL

# --- 9. INSPECÇÃO final: SEM DUPLA-EXECUÇÃO (cardinalidade M == N) ----------
echo "[harness] KILL + inspecçAo final (nAo-duplicaçAo) ..."
docker kill "${CT}" >/dev/null || fail "docker kill (final) falhou"
turns_after=$(wal_turns) || fail "inspecçAo do WAL (final) falhou"
echo "[harness] turnos durAveis no WAL apOs a re-submissAo: ${turns_after}"
[[ "${turns_after}" =~ ^[0-9]+$ ]] || fail "contagem de turnos invAlida (final): '${turns_after}'"
[[ "${turns_after}" == "${turns_before}" ]] || \
  fail "DUPLA-EXECUÇÃO detectada: turnos ${turns_before}->${turns_after} (a re-submissAo acrescentou trabalho durAvel)"

echo "AOS-169 DURABILIDADE CONTENTOR: PASS (persistencia turnos=${turns_before}; sem duplicacao apos reinicio)"
