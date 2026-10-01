#!/usr/bin/env bash
# nats-levantar.sh — subir o cluster do gate `nats`, e dizer PORQUE não subiu (AOS-471).
#
# Biblioteca: faz-se `source` dela depois do `lib.sh`, não se corre. Vive à parte do `nats.sh`
# pela razão do `gotest-pacotes.sh`: para que o `selftest.sh` (§NX) exercite ESTE código com um
# `docker` de brincar no PATH, sem cluster e sem daemon.
#
# ─── O DEFEITO QUE FECHA ───────────────────────────────────────────────────────────────────
#
# O `nats.sh` fazia `if ! eval "$(bash nats-cluster.sh up)"`. O código de saída da
# substituição de comando PERDE-SE dentro do argumento do `eval`: o `eval` de uma string vazia
# sai 0, e o ramo «o cluster não subiu» era inalcançável. Medido a 2026-10-01 numa máquina com
# o CLI `docker` e SEM daemon:
#
#   failed to connect to the docker API at unix:///var/run/docker.sock; …
#   scripts/ci/nats.sh: line 138: AOS_NATS_URL: unbound variable
#
# Fail-closed por acaso (`set -u`), com o diagnóstico a apontar para uma variável e não para o
# cluster. E o «sem docker» só olhava para o CLI (`command -v docker`): um CLI sem daemon
# passava a porta do salto declarado e caía neste vermelho enganador.
#
# ─── DUAS AVARIAS, DOIS DESTINOS ───────────────────────────────────────────────────────────
#
#   · DOCKER INUTILIZÁVEL (CLI ausente OU daemon inacessível): o ambiente não tem com que
#     medir. É uma propriedade do posto, não do código — `nats_docker_utilizavel` diz qual das
#     duas e o `nats.sh` decide (salto declarado localmente, vermelho em CI).
#   · O CLUSTER NÃO SOBE COM DOCKER UTILIZÁVEL: é avaria a sério (imagem, portas, Raft, nkey) e
#     é SEMPRE vermelho — `nats_levantar` nomeia o `nats-cluster.sh` e o código com que saiu.

# nats_docker_utilizavel — 0 se o CLI existe E o daemon responde. Caso contrário devolve 1 e
#   deixa em NATS_DOCKER_MOTIVO qual das duas falhou, com a linha de erro do `docker info`.
#   `docker info` porque não responde sem falar com o daemon; é a mesma sonda do
#   `isolation-live.sh` («daemon docker inacessível»).
nats_docker_utilizavel() {
  NATS_DOCKER_MOTIVO=""
  if ! command -v docker >/dev/null 2>&1; then
    NATS_DOCKER_MOTIVO="docker ausente do PATH"
    return 1
  fi
  local erro
  if ! erro="$(docker info 2>&1 >/dev/null)"; then
    erro="$(printf '%s\n' "$erro" | grep -v '^[[:space:]]*$' | tail -1 || true)"
    erro="${erro//|//}" # `|` é o separador dos campos do `gate_skip` (lib.sh)
    NATS_DOCKER_MOTIVO="daemon docker inacessível (docker info: ${erro:-saiu ≠ 0 sem mensagem})"
    return 1
  fi
  return 0
}

# nats_levantar <nats-cluster.sh> — sobe o cluster e exporta o env dele NESTA shell.
#   0: cluster de pé e AOS_NATS_URL exportado. 1: não subiu, com o diagnóstico já escrito.
#   O código de saída e a saída do `up` verificam-se ANTES do `eval`. E depois dele exige-se
#   AOS_NATS_URL: um `up` que saia 0 sem o imprimir é um cluster que ninguém sabe onde está.
nats_levantar() {
  local cluster="$1" env_cluster="" rc_up=0
  env_cluster="$(bash "$cluster" up)" || rc_up=$?
  if [ "$rc_up" -ne 0 ]; then
    log_fail "nats: o cluster NÃO subiu — \`$(basename "$cluster") up\` saiu $rc_up com o docker utilizável (o motivo está nas linhas acima)"
    return 1
  fi
  if ! eval "$env_cluster"; then
    log_fail "nats: \`$(basename "$cluster") up\` saiu 0 mas o env que imprimiu não é shell válido"
    return 1
  fi
  if [ -z "${AOS_NATS_URL:-}" ]; then
    log_fail "nats: \`$(basename "$cluster") up\` saiu 0 sem exportar AOS_NATS_URL — o cluster não diz onde está"
    return 1
  fi
  return 0
}
