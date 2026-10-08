#!/usr/bin/env bash
# wire-live.sh — GATE OPCIONAL: os PROVIDERS FALSOS DE WIRE atrás do PROXY REAL (AOS-508).
#
# ─── O QUE ESTE GATE PROVA ───────────────────────────────────────────────────────────────────
# Levanta a MESMA imagem do LiteLLM que corre em produção (fixada pelo digest) à frente de um
# provider falso que devolve cada caso do wirefake (`packages/platform/model-gateway/internal/
# wirefake/casos`), e regista, para cada caso, o que o proxy ENTREGA ao gateway: o status, as
# chaves que acrescenta ou retira, se reescreve `content`, que nome dá ao raciocínio, e o que o
# gateway faz ao corpo entregue. É medição: fica vermelho se o ensaio não se montar ou se um
# caso ficar por medir, e não por o proxy fazer isto ou aquilo a uma forma.
#
# ─── O QUE NÃO PROVA ─────────────────────────────────────────────────────────────────────────
#   N1 Nada sobre o provider real: não é contactado. O que ele devolve fica por confirmar.
#   N2 Nada sobre uma rota que não seja `openai/…` (a de produção): outro adaptador do proxy
#      traduz de outra maneira.
#   N3 Nada sobre taxas de comportamento do modelo: os casos são corpos fixos, escritos à mão.
#
# ─── PORQUE É OPCIONAL, E O QUE CORRE SEMPRE ─────────────────────────────────────────────────
# Precisa de Docker e da imagem já descarregada (não a descarrega). Por isso NÃO está nos
# required checks nem no `run.sh`. Sem Docker ou sem a imagem, o salto é RUIDOSO (gate_skip,
# AOS-199) e nomeia o que ficou por verificar. O equivalente offline — os mesmos casos contra o
# gateway, sem proxy — corre SEMPRE, aqui e no gate `test` (TestAOS508_OQueOGatewayFazACadaCaso).
#
# Knobs: AOS_WIRE_LIVE_REQUIRED=1 torna o salto VERMELHO.
#        AOS_WIRE_LIVE_OUT=<ficheiro> grava as medidas de cada caso em JSON (para o relatório).
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

MOD="packages/platform/model-gateway"
TESTE_REAL="TestAOS508_ProxyReal_OQueOProxyEntregaACadaCaso"
TESTE_OFFLINE="TestAOS508_OQueOGatewayFazACadaCaso"
FONTE="$REPO_ROOT/$MOD/route_aos505_proxyreal_test.go"

log_gate "wire-live (AOS-508) · providers falsos de wire atrás da imagem de produção do proxy"

# (0) Corre sempre: os dois cenários existem por nome, e o offline passa.
log_step "go test -list (os dois cenários existem por nome)"
listed="$( cd "$REPO_ROOT/$MOD" && go test -list '^TestAOS508_' . )"
for t in "$TESTE_REAL" "$TESTE_OFFLINE"; do
  if ! printf '%s\n' "$listed" | grep -qx "$t"; then
    log_fail "cenário ausente (renomeado/removido?): $t"
    exit 1
  fi
done
log_step "equivalente offline ($TESTE_OFFLINE)"
require_tests "$REPO_ROOT/$MOD" "." "^${TESTE_OFFLINE}\$" "$TESTE_OFFLINE" || exit 1

# (1) Há Docker e a imagem? (a imagem é a do AOS-505: a mesma, pelo mesmo digest)
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
  gate_skip "wire-live · casos de wire atrás do proxy real" "$motivo" \
    "o que a imagem de PRODUÇÃO do proxy entrega ao gateway para cada forma de resposta (o cenário offline põe os casos directamente à frente do gateway, sem proxy)"
  log_gate "wire-live · veredicto"
  gate_skip_report || true
  if [ "${AOS_WIRE_LIVE_REQUIRED:-0}" = "1" ]; then
    log_fail "wire-live: VERMELHO — AOS_WIRE_LIVE_REQUIRED=1 e a medição contra o proxy real NÃO foi produzida"
    exit 1
  fi
  log_warn "wire-live: SALTADO (opcional). O cenário offline passou; o proxy real não foi exercitado nesta execução."
  exit 0
fi

# (2) O cenário real. Exige `--- PASS` por nome e o relatório com o veredicto agregado.
log_step "AOS_WIRE_LIVE=1 go test -run $TESTE_REAL (arranca o proxy uma vez; cerca de 1 a 3 min)"
saida="$( cd "$REPO_ROOT/$MOD" && AOS_WIRE_LIVE=1 go test -run "^${TESTE_REAL}\$" -v -count=1 -timeout 20m . 2>&1 )" || {
  printf '%s\n' "$saida" | tail -40 | sed 's/^/       /' >&2
  log_fail "wire-live: o cenário contra o proxy real falhou"
  exit 1
}
if ! printf '%s\n' "$saida" | grep -q -- "--- PASS: $TESTE_REAL"; then
  printf '%s\n' "$saida" | tail -20 | sed 's/^/       /' >&2
  log_fail "wire-live: sem '--- PASS: $TESTE_REAL' (um salto não conta)"
  exit 1
fi
relatorio="$( printf '%s\n' "$saida" | grep 'AOS_WIRE_LIVE_REPORT' | sed 's/.*AOS_WIRE_LIVE_REPORT //' | head -1 )"
printf '   %s\n' "$relatorio"
if ! printf '%s' "$relatorio" | grep -q '"pass":true'; then
  log_fail "wire-live: o relatório não declara pass=true"
  exit 1
fi

# (3) AOS-515/AOS-513 — o que o GATEWAY ENVIA atravessa o proxy? Dois turnos por rota (openai/… e
# anthropic/…) com um perfil que declara parâmetros e `devolver: obrigatorio`; o falso grava o
# que recebe. A rota openai/… tem de completar os dois turnos; a anthropic/… é medição.
TESTE_515="TestAOS515_ProxyReal_OQueChegaAoProviderNoSegundoTurno"
log_step "AOS_WIRE_LIVE=1 go test -run $TESTE_515 (arranca o proxy outra vez; cerca de 1 a 3 min)"
saida515="$( cd "$REPO_ROOT/$MOD" && AOS_WIRE_LIVE=1 go test -run "^${TESTE_515}\$" -v -count=1 -timeout 20m . 2>&1 )" || {
  printf '%s
' "$saida515" | tail -40 | sed 's/^/       /' >&2
  log_fail "wire-live: o cenário da devolução do estado contra o proxy real falhou"
  exit 1
}
if ! printf '%s
' "$saida515" | grep -q -- "--- PASS: $TESTE_515"; then
  log_fail "wire-live: sem '--- PASS: $TESTE_515' (um salto não conta)"
  exit 1
fi
printf '%s
' "$saida515" | grep 'AOS515_MEDIDA' | sed 's/.*AOS515_MEDIDA /   /'
gate_skip_report || true
log_ok "wire-live: verde contra o proxy real (todos os casos medidos)"
log_warn "LEMBRETE: isto mede o que o PROXY faz a corpos fixos de um provider falso. O que o provider real devolve fica por confirmar."
