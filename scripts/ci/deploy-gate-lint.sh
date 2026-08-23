#!/usr/bin/env bash
# deploy-gate-lint.sh — O GATE DE ENTREGA TEM DE SONDAR PRONTIDAO, NAO LIVENESS.
#
# O DEFEITO QUE FECHA, encontrado na verificacao de funcionamento de 2026-08-23:
# `handleHealthz` devolve 200 INCONDICIONALMENTE (packages/cmd/aos/api.go) — e liveness,
# "o processo responde". As DUAS variaveis que decidiam a reversao automatica do
# `deploy.sh` (`healthy` e `smoke_ok`) vinham AS DUAS do /healthz. Um no que recusasse
# 100% dos pedidos — mount remontado :ro, WORM a recusar escritas, Vault selado — era
# entregue VERDE e a reversao automatica nunca disparava.
#
# AMARRA-SE A CADEIA EXECUTAVEL, NAO A PROSA. Um `grep -q /readyz` passaria com o curl
# revertido para /healthz, porque os comentarios destes ficheiros nomeiam /readyz varias
# vezes para explicar a regra. Conta-se a URL que o curl constroi, ancorada na aspa de
# fecho — a linha de `log` que anuncia a sonda nao a tem.
#
# RAIZ PARAMETRIZAVEL (o padrao de layer-lint.sh --root): o selftest muta uma arvore
# sintetica e prova que este gate morde. Um gate sem essa prova e uma afirmacao.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

ROOT="$REPO_ROOT"
if [ "${1:-}" = "--root" ] && [ -n "${2:-}" ]; then
  ROOT="$2"
elif [ -n "${AOS_DEPLOY_GATE_ROOT:-}" ]; then
  ROOT="$AOS_DEPLOY_GATE_ROOT"
fi

rc=0
n_sondas=0

log_gate "deploy-gate · o gate de entrega sonda /readyz"

sh_gate="$ROOT/deploy/server/deploy.sh"
if [ -f "$sh_gate" ]; then
  n_ready="$( grep -c 'EDGE_PORT}/readyz"' "$sh_gate" || true )"
  n_live="$(  grep -c 'EDGE_PORT}/healthz"' "$sh_gate" || true )"
  n_sondas=$(( n_sondas + n_ready ))
  if [ "$n_live" -ne 0 ]; then
    log_fail "deploy/server/deploy.sh sonda EDGE_PORT/healthz $n_live vez(es) — o /healthz e 200 incondicional, logo um no nao-pronto seria entregue VERDE e a reversao nunca dispararia"
    rc=1
  fi
  # DUAS sondas, e as duas sao necessarias:
  #   - a LINHA DE BASE, antes de tocar em nada, sem a qual uma avaria ANTERIOR reverteria
  #     uma entrega que nao a causou (e a reversao nao a resolveria: o estado e duravel);
  #   - o SMOKE do passo 6, que e o gate propriamente dito.
  # Exigir as duas impede que alguem remova a atribuicao e deixe so o smoke.
  if [ "$n_ready" -lt 2 ]; then
    log_fail "deploy/server/deploy.sh tem $n_ready sonda(s) EDGE_PORT/readyz — esperava 2 (linha de base + smoke do passo 6)"
    rc=1
  else
    log_ok "deploy/server/deploy.sh: $n_ready sondas /readyz (linha de base + smoke)"
  fi
else
  log_fail "deploy/server/deploy.sh nao existe em $ROOT — o gate nao verificou nada"
  rc=1
fi

yml_gate="$ROOT/.github/workflows/deploy.yml"
if [ -f "$yml_gate" ]; then
  # SO o cheque de ALCANCE (por $HOST). O de CONFIANCA usa $PUBLIC_HOST e fica
  # DELIBERADAMENTE no /healthz: a pergunta dele e sobre o CERTIFICADO, e o /healthz e o
  # unico endpoint que responde 200 em qualquer estado da aplicacao — o que faz dele o
  # melhor canario de TLS possivel (um vermelho la so pode ser da cadeia). Ancora-se em
  # `//$HOST:` para nao casar com `//$PUBLIC_HOST:`, que e substring.
  a_ready="$( grep -c '//\$HOST:\$PORT/readyz' "$yml_gate" || true )"
  a_live="$(  grep -c '//\$HOST:\$PORT/healthz' "$yml_gate" || true )"
  n_sondas=$(( n_sondas + a_ready ))
  if [ "$a_live" -ne 0 ]; then
    log_fail ".github/workflows/deploy.yml: o cheque de ALCANCE sonda /healthz — nunca pergunta se a aplicacao serve"
    rc=1
  fi
  if [ "$a_ready" -lt 1 ]; then
    log_fail ".github/workflows/deploy.yml: o cheque de ALCANCE nao sonda //\$HOST:\$PORT/readyz"
    rc=1
  else
    log_ok ".github/workflows/deploy.yml: o cheque de alcance sonda /readyz"
  fi
else
  log_fail ".github/workflows/deploy.yml nao existe em $ROOT — o gate nao verificou nada"
  rc=1
fi

# CONTROLO ANTI-VACUIDADE (molde do §2e do lint.sh): se nenhuma sonda for encontrada, este
# gate passaria por nao ter feito nada. Um gate que nao encontra o que verifica tem de gritar.
if [ "$n_sondas" -eq 0 ]; then
  log_fail "nenhuma sonda /readyz encontrada nos ficheiros de entrega — o gate nao verificou nada"
  rc=1
else
  log_ok "gate de entrega: $n_sondas sonda(s) /readyz verificada(s)"
fi

exit "$rc"
