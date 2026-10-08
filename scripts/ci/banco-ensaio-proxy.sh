#!/usr/bin/env bash
# banco-ensaio-proxy.sh — GATE OPCIONAL: a BATERIA do banco de ensaio atrás do PROXY REAL (AOS-512).
#
# ─── O QUE ESTE GATE PROVA ───────────────────────────────────────────────────────────────────
# Corre o modo `proxy` do banco de ensaio (`packages/qa/banco-ensaio`): a MESMA bateria e o
# MESMO roteiro do provider falso que correm em cada PR, com a imagem de produção do proxy
# (LiteLLM, fixada pelo digest) no meio — nó de ensaio → proxy → provider falso num contentor.
# Fica vermelho se os desfechos atrás do proxy não forem os do modo falso, se a chave da rota
# não chegar ao fornecedor (o provider falso exige-a) ou se um segredo ou um texto de resposta
# aparecer numa saída.
#
# ─── O QUE NÃO PROVA ─────────────────────────────────────────────────────────────────────────
#   N1 Nada sobre um modelo real: o fornecedor é o provider falso. Este gate NÃO corre, nem
#      pode correr, o modo com modelo real do banco — esse recusa em CI e só o dono o corre.
#   N2 Nada sobre uma rota que não seja `openai/…` (a de produção).
#
# ─── PORQUE É OPCIONAL, E O QUE CORRE SEMPRE ─────────────────────────────────────────────────
# Precisa de Docker e da imagem já descarregada (não a descarrega). Por isso NÃO está nos
# required checks nem no `run.sh`. Sem Docker ou sem a imagem, o salto é RUIDOSO (gate_skip,
# AOS-199) e nomeia o que ficou por verificar. O equivalente offline — a mesma bateria contra o
# provider falso, sem proxy, com taxas exactas — corre SEMPRE, aqui e no gate `test`.
#
# Knobs: AOS_BANCO_PROXY_REQUIRED=1 torna o salto VERMELHO.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

MOD="packages/qa/banco-ensaio"
TESTE_REAL="TestAOS512_ProxyReal_ABateriaAtrasDoProxy"
TESTE_OFFLINE="TestAOS512_Falso_BateriaInteira_TaxasExactas"
FONTE="$REPO_ROOT/$MOD/proxy.go"

log_gate "banco-ensaio-proxy (AOS-512) · a bateria do banco atrás da imagem de produção do proxy"

# (0) Corre sempre: os dois cenários existem por nome, e o offline passa.
log_step "go test -list (os dois cenários existem por nome)"
listed="$( cd "$REPO_ROOT/$MOD" && go test -list '^TestAOS512_' . )"
for t in "$TESTE_REAL" "$TESTE_OFFLINE"; do
  if ! printf '%s\n' "$listed" | grep -qx "$t"; then
    log_fail "cenário ausente (renomeado/removido?): $t"
    exit 1
  fi
done
log_step "equivalente offline ($TESTE_OFFLINE)"
require_tests "$REPO_ROOT/$MOD" "." "^${TESTE_OFFLINE}\$" "$TESTE_OFFLINE" || exit 1

# (1) Há Docker e a imagem? (a imagem é a do AOS-505 e do AOS-508: a mesma, pelo mesmo digest)
IMAGEM="$( sed -n 's/^const ImagemDoProxy = "\(.*\)"$/\1/p' "$FONTE" )"
if [ -z "$IMAGEM" ]; then
  log_fail "não consegui ler a imagem do proxy de $FONTE"
  exit 1
fi
motivo=""
if ! command -v docker >/dev/null 2>&1; then
  motivo="docker ausente do PATH"
elif ! docker info >/dev/null 2>&1; then
  motivo="daemon docker inacessível"
elif ! docker image inspect "$IMAGEM" >/dev/null 2>&1; then
  motivo="imagem do proxy não descarregada (docker pull $IMAGEM)"
fi
if [ -n "$motivo" ]; then
  gate_skip "banco-ensaio-proxy · a bateria atrás do proxy real" "$motivo" \
    "que o pedido montado pelo nó de ensaio atravessa a imagem de PRODUÇÃO do proxy com os mesmos desfechos (o cenário offline corre a bateria sem proxy)"
  log_gate "banco-ensaio-proxy · veredicto"
  gate_skip_report || true
  if [ "${AOS_BANCO_PROXY_REQUIRED:-0}" = "1" ]; then
    log_fail "banco-ensaio-proxy: VERMELHO — AOS_BANCO_PROXY_REQUIRED=1 e a corrida atrás do proxy real NÃO foi produzida"
    exit 1
  fi
  log_warn "banco-ensaio-proxy: SALTADO (opcional). O cenário offline passou; o proxy real não foi exercitado nesta execução."
  exit 0
fi

# (2) O provider falso corre DENTRO de um contentor: precisa de um binário Linux do banco, para
#     a arquitectura do daemon. É estático (CGO desligado), sem dependências externas.
ARQ="$( docker version --format '{{.Server.Arch}}' 2>/dev/null || true )"
case "$ARQ" in
  amd64|arm64) ;;
  *) log_fail "arquitectura do daemon docker não suportada pelo gate: '$ARQ'"; exit 1 ;;
esac
TMP="$( mktemp -d )"
trap 'rm -rf "$TMP"' EXIT
log_step "go build do provider falso para linux/$ARQ"
( cd "$REPO_ROOT/$MOD" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARQ" go build -trimpath -o "$TMP/aos-ensaio-linux" ./cmd/aos-ensaio ) || {
  log_fail "banco-ensaio-proxy: o binário Linux do banco não compilou"
  exit 1
}
BIN="$TMP/aos-ensaio-linux"
if command -v cygpath >/dev/null 2>&1; then
  BIN="$( cygpath -w "$BIN" )"   # o `docker cp` e o Go do Windows querem o caminho nativo
fi

# (3) O cenário real. Exige `--- PASS` por nome e o relatório com o veredicto agregado.
log_step "AOS_BANCO_PROXY=1 go test -run $TESTE_REAL (arranca o proxy uma vez; cerca de 1 a 3 min)"
saida="$( cd "$REPO_ROOT/$MOD" && AOS_BANCO_PROXY=1 AOS_BANCO_FALSO_BIN="$BIN" go test -run "^${TESTE_REAL}\$" -v -count=1 -timeout 20m . 2>&1 )" || {
  printf '%s\n' "$saida" | tail -40 | sed 's/^/       /' >&2
  log_fail "banco-ensaio-proxy: o cenário contra o proxy real falhou"
  exit 1
}
if ! printf '%s\n' "$saida" | grep -q -- "--- PASS: $TESTE_REAL"; then
  printf '%s\n' "$saida" | tail -20 | sed 's/^/       /' >&2
  log_fail "banco-ensaio-proxy: sem '--- PASS: $TESTE_REAL' (um salto não conta)"
  exit 1
fi
relatorio="$( printf '%s\n' "$saida" | grep 'AOS_BANCO_PROXY_REPORT' | sed 's/.*AOS_BANCO_PROXY_REPORT //' | head -1 )"
printf '   %s\n' "$relatorio"
if ! printf '%s' "$relatorio" | grep -q '"pass":true'; then
  log_fail "banco-ensaio-proxy: o relatório não declara pass=true"
  exit 1
fi
gate_skip_report || true
log_ok "banco-ensaio-proxy: verde contra o proxy real (a bateria deu os desfechos do modo falso)"
log_warn "LEMBRETE: o fornecedor é um provider falso. Nada aqui diz o que um modelo real faz."
