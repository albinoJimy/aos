#!/usr/bin/env bash
# rota-live.sh — GATE OPCIONAL: a ROTA SOB GOVERNAÇÃO contra o PROXY REAL (AOS-505).
#
# ─── O QUE ESTE GATE PROVA ───────────────────────────────────────────────────────────────────
# Levanta a MESMA imagem do LiteLLM que corre em produção (fixada pelo digest) à frente de dois
# providers falsos, compõe o gateway de produção (NewProduction) contra ela, e troca a
# configuração do proxy POR BAIXO do nome pedido a meio de um run — primeiro o modelo, depois o
# endpoint. Exige que o turno seguinte registe a variância em observação (resultado, selo de
# governação, contador) e que FALHE com causa em vocabulário fechado em imposição; e que nem o
# identificador do deployment que o proxy emite, nem o endpoint, nem as chaves cheguem ao registo.
#
# ─── O QUE NÃO PROVA ─────────────────────────────────────────────────────────────────────────
#   N1 Nada sobre o provider real: não é contactado. O que ele devolve fica por confirmar.
#   N2 Nada sobre uma troca feita pelo provider por trás do mesmo nome e do mesmo endpoint: essa
#      não muda nenhum cabeçalho do proxy, e a governação da rota não a detecta.
#   N3 Os cabeçalhos do proxy não são atestação; valem enquanto o canal nó–proxy for de confiança.
#
# ─── PORQUE É OPCIONAL, E O QUE CORRE SEMPRE ─────────────────────────────────────────────────
# Precisa de Docker e da imagem já descarregada (não a descarrega: é um download de centenas de
# MB que um gate não faz por conta própria). Por isso NÃO está nos required checks. Sem Docker ou
# sem a imagem, o salto é RUIDOSO (gate_skip, AOS-199) e nomeia o que ficou por verificar. O
# equivalente offline — o mesmo cenário com httptest a emitir os cabeçalhos medidos — corre
# SEMPRE, aqui e no gate `test` (TestAOS505_TrocaPorBaixoAMeioDoRun).
#
# Knobs: AOS_ROTA_LIVE_REQUIRED=1 torna o salto VERMELHO.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

MOD="packages/platform/model-gateway"
TESTE_REAL="TestAOS505_ProxyReal_TrocaPorBaixoDetectada"
TESTE_OFFLINE="TestAOS505_TrocaPorBaixoAMeioDoRun"
FONTE="$REPO_ROOT/$MOD/route_aos505_proxyreal_test.go"

log_gate "rota-live (AOS-505) · troca de modelo por baixo, contra a imagem de produção do proxy"

# (0) Corre sempre: os dois cenários existem por nome, e o offline passa.
log_step "go test -list (os dois cenários existem por nome)"
listed="$( cd "$REPO_ROOT/$MOD" && go test -list '^TestAOS505_' . )"
for t in "$TESTE_REAL" "$TESTE_OFFLINE"; do
  if ! printf '%s\n' "$listed" | grep -qx "$t"; then
    log_fail "cenário ausente (renomeado/removido?): $t"
    exit 1
  fi
done
log_step "equivalente offline ($TESTE_OFFLINE)"
require_tests "$REPO_ROOT/$MOD" "." "^${TESTE_OFFLINE}\$" "$TESTE_OFFLINE" || exit 1

# (1) Há Docker e a imagem?
IMAGEM="$( sed -n 's/^const aos505ImagemDoProxy = "\(.*\)"$/\1/p' "$FONTE" )"
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
  gate_skip "rota-live · troca por baixo contra o proxy real" "$motivo" \
    "que os cabeçalhos que a imagem de PRODUÇÃO do proxy emite mudam com a troca de configuração e que o gateway a detecta (o cenário offline usa cabeçalhos medidos uma vez, em 2026-10-06)"
  log_gate "rota-live · veredicto"
  gate_skip_report || true
  if [ "${AOS_ROTA_LIVE_REQUIRED:-0}" = "1" ]; then
    log_fail "rota-live: VERMELHO — AOS_ROTA_LIVE_REQUIRED=1 e a prova contra o proxy real NÃO foi produzida"
    exit 1
  fi
  log_warn "rota-live: SALTADO (opcional). O cenário offline passou; o proxy real não foi exercitado nesta execução."
  exit 0
fi

# (2) O cenário real. Exige `--- PASS` por nome e o relatório com o veredicto agregado.
log_step "AOS_ROTA_LIVE=1 go test -run $TESTE_REAL (arranca o proxy três vezes; cerca de 1 a 3 min)"
saida="$( cd "$REPO_ROOT/$MOD" && AOS_ROTA_LIVE=1 go test -run "^${TESTE_REAL}\$" -v -count=1 -timeout 20m . 2>&1 )" || {
  printf '%s\n' "$saida" | tail -40 | sed 's/^/       /' >&2
  log_fail "rota-live: o cenário contra o proxy real falhou"
  exit 1
}
if ! printf '%s\n' "$saida" | grep -q -- "--- PASS: $TESTE_REAL"; then
  printf '%s\n' "$saida" | tail -20 | sed 's/^/       /' >&2
  log_fail "rota-live: sem '--- PASS: $TESTE_REAL' (um salto não conta)"
  exit 1
fi
relatorio="$( printf '%s\n' "$saida" | grep 'AOS_ROTA_LIVE_REPORT' | sed 's/.*AOS_ROTA_LIVE_REPORT //' | head -1 )"
printf '   %s\n' "$relatorio"
if ! printf '%s' "$relatorio" | grep -q '"pass":true'; then
  log_fail "rota-live: o relatório não declara pass=true"
  exit 1
fi
gate_skip_report || true
log_ok "rota-live: verde contra o proxy real (troca de modelo e de endpoint detectadas; observação segue, imposição falha)"
log_warn "LEMBRETE: isto prova a detecção de uma troca de CONFIGURAÇÃO no proxy. Uma troca feita pelo provider por trás do mesmo nome e endpoint não é detectada."
