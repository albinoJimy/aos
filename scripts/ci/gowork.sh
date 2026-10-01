#!/usr/bin/env bash
# gowork.sh — gera e verifica o workspace `go.work` da raiz (AOS-387).
#
# O QUE É O go.work AQUI. Um workspace com UMA directiva `use` por módulo de `packages/` — o
# conjunto exacto de `find packages -name go.mod -printf '%h\n'`. Dá ao programador (e ao gopls)
# a resolução inter-módulo local sem depender das `replace`, um `go build ./packages/...` que
# atravessa os 49 módulos de uma vez, e reduz a integração de um módulo novo a `go work use`.
#
# O QUE NÃO É. Não é o modo em que os gates correm: `setup_env` (lib.sh) exporta `GOWORK=off`,
# pelo que a CI resolve como antes — pelas `replace` committadas de cada `go.mod`. A razão e as
# alternativas medidas estão em tecnica/11 §8.1. Por isso esta verificação é a
# ÚNICA coisa que impede o go.work de apodrecer em silêncio: se a CI não o usa, só um teste
# explícito diz que ele ainda cobre a árvore.
#
# Uso:
#   bash scripts/ci/gowork.sh gerar        # (re)escreve go.work a partir da árvore
#   bash scripts/ci/gowork.sh verificar    # default: falha se go.work divergir da árvore
#   bash scripts/ci/gowork.sh compilar     # build dos 49 módulos em modo workspace, offline
#   bash scripts/ci/gowork.sh verificar --root <árvore>   # só para o self-test (árvore sintética)
#
# O build.sh (gate 1) corre `verificar` e `compilar`: um módulo novo em packages/ sem `use`
# avermelha o gate build.
#
# A VERIFICAÇÃO É SEMÂNTICA, não byte-a-byte: lê o ficheiro com o próprio parser do Go
# (`go work edit -json`), para que um `go work use ./packages/x` à mão — que formata à sua
# maneira — seja aceite se o conjunto ficar certo. Exige:
#   (1) o conjunto `use` == conjunto de directórios com go.mod sob packages/ (nem a mais, nem a menos);
#   (2) a directiva `go` == a maior directiva `go` dos módulos (o Go recusa um workspace abaixo
#       de um módulo; acima seria pedir uma linguagem que nenhum módulo declara);
#   (3) a directiva `toolchain` == a toolchain da imagem de produção (`FROM golang:` do
#       Dockerfile, a mesma autoridade de `setup_env` e do toolchain-lint). Sem ela, um `go`
#       local mais antigo com GOTOOLCHAIN=auto tentaria descarregar «a última 1.25.x» — rede.
#   (4) nenhum `replace` no go.work: as `replace` vivem nos go.mod (decisão (a) de tecnica/11);
#       um `replace` no workspace mudaria a resolução só para quem o usa, em silêncio.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

modo="${1:-verificar}"
ROOT="$REPO_ROOT"
if [ "${2:-}" = "--root" ]; then
  ROOT="${3:-}"
  [ -d "$ROOT/packages" ] || { log_fail "gowork: --root inválido (sem packages/): $ROOT"; exit 2; }
fi
WORK="$ROOT/go.work"

# modulos_esperados — os directórios de módulo sob packages/, como ./caminho, ordenados (C).
modulos_esperados() {
  ( cd "$ROOT" && find packages -name go.mod -printf './%h\n' | LC_ALL=C sort )
}

# go_esperado — a maior directiva `go` dos módulos de packages/ (ordem de versões, não lexical).
go_esperado() {
  ( cd "$ROOT" && find packages -name go.mod -exec awk '$1=="go"{print $2; exit}' {} \; ) \
    | sort -V | tail -1
}

# toolchain_esperada — a toolchain da imagem de produção (mesma leitura de setup_env).
toolchain_esperada() {
  local v
  v="$(grep -oE '^FROM golang:[0-9]+\.[0-9]+\.[0-9]+' "$REPO_ROOT/deploy/node/Dockerfile" 2>/dev/null \
       | head -1 | sed 's/^FROM golang://' || true)"
  [ -n "$v" ] && printf 'go%s\n' "$v"
}

gerar() {
  local gov tc
  gov="$(go_esperado)"; tc="$(toolchain_esperada)"
  if [ -z "$gov" ] || [ -z "$tc" ]; then
    log_fail "gowork: não consegui determinar a directiva go ('$gov') ou a toolchain ('$tc') — não escrevo um go.work adivinhado"
    exit 1
  fi
  {
    printf '// go.work — GERADO por scripts/ci/gowork.sh (AOS-387). Não editar à mão a lista `use`:\n'
    printf '// `bash scripts/ci/gowork.sh gerar` (ou `go work use ./packages/<novo>`). Os gates correm\n'
    printf '// com GOWORK=off (lib.sh setup_env); ver tecnica/11 §8.1.\n\n'
    printf 'go %s\n\ntoolchain %s\n\nuse (\n' "$gov" "$tc"
    modulos_esperados | sed 's/^/\t/'
    printf ')\n'
  } > "$WORK"
  log_ok "gowork: $WORK escrito ($(modulos_esperados | wc -l | tr -d ' ') módulos, go $gov, toolchain $tc)"
}

verificar() {
  log_gate "go.work · o workspace cobre exactamente os módulos de packages/ (AOS-387)"
  if [ ! -f "$WORK" ]; then
    log_fail "gowork: $WORK não existe — corra: bash scripts/ci/gowork.sh gerar"
    return 1
  fi
  local json rc=0
  # O parser é o do Go: um go.work malformado falha aqui, e não num grep tolerante.
  if ! json="$(GOWORK=off go work edit -json "$WORK" 2>&1)"; then
    log_fail "gowork: o Go não consegue ler $WORK: $json"
    return 1
  fi
  local tmp; tmp="$(mktemp -d)"
  printf '%s' "$json" | python3 -c '
import json, sys
w = json.load(sys.stdin)
d = sys.argv[1]
open(d + "/use", "w").write("".join(u["DiskPath"] + "\n" for u in (w.get("Use") or [])))
open(d + "/go", "w").write((w.get("Go") or "") + "\n")
open(d + "/replace", "w").write("".join("%s\n" % r["Old"]["Path"] for r in (w.get("Replace") or [])))
' "$tmp"
  # O `-json` do go1.25 não exporta a directiva toolchain; o `-print` devolve o ficheiro na forma
  # canónica do parser do Go, de onde ela se lê sem ambiguidade (comentários já não contam).
  GOWORK=off go work edit -print "$WORK" | awk '$1=="toolchain"{print $2}' > "$tmp/toolchain"
  LC_ALL=C sort -o "$tmp/use" "$tmp/use"
  modulos_esperados > "$tmp/esperado"

  local falta sobra
  falta="$(LC_ALL=C comm -23 "$tmp/esperado" "$tmp/use")"
  sobra="$(LC_ALL=C comm -13 "$tmp/esperado" "$tmp/use")"
  if [ -n "$falta" ]; then
    log_fail "gowork: módulo(s) com go.mod em packages/ SEM \`use\` no go.work:"
    printf '       %s\n' $falta >&2
    log_fail "  corrija com: go work use <dir>   (ou: bash scripts/ci/gowork.sh gerar)"
    rc=1
  fi
  if [ -n "$sobra" ]; then
    log_fail "gowork: \`use\` no go.work que NÃO é um módulo de packages/ (removido, movido ou fora do âmbito):"
    printf '       %s\n' $sobra >&2
    rc=1
  fi
  local gov tc gow tcw
  gov="$(go_esperado)"; tc="$(toolchain_esperada)"
  gow="$(cat "$tmp/go")"; tcw="$(cat "$tmp/toolchain")"
  if [ "$gow" != "$gov" ]; then
    log_fail "gowork: directiva go do go.work é '$gow'; a maior dos módulos é '$gov'"
    rc=1
  fi
  if [ -z "$tc" ] || [ "$tcw" != "$tc" ]; then
    log_fail "gowork: directiva toolchain do go.work é '$tcw'; a da imagem de produção é '${tc:-<ilegível>}'"
    rc=1
  fi
  if [ -s "$tmp/replace" ]; then
    log_fail "gowork: o go.work tem \`replace\` — as replace vivem nos go.mod (tecnica/11 §8.1, decisão (a)):"
    sed 's/^/       /' "$tmp/replace" >&2
    rc=1
  fi
  rm -rf "$tmp"
  if [ "$rc" -eq 0 ]; then
    log_ok "gowork: go.work cobre os $(modulos_esperados | wc -l | tr -d ' ') módulos de packages/ (go $gov, toolchain $tc, sem replace)"
  fi
  return "$rc"
}

# compilar — UM `go build` sobre os 49 módulos, em modo workspace e OFFLINE.
#
# Prova o que o conjunto `use` sozinho não prova: que o workspace RESOLVE. Corre com
# `GOPROXY=off` e `-mod=readonly` de propósito: um checksum que só existisse no go.work.sum (e
# não nos go.sum dos módulos) seria pedido à rede, e aqui isso é vermelho — é o sinal de que o
# go.work.sum passou a ser necessário e tem de ser committado (tecnica/11 §8.1, decisão (b)). Corre
# depois do build por-módulo, que é quem aquece o cache num runner frio com rede.
compilar() {
  log_gate "go.work · um build que atravessa os módulos todos, em modo workspace e offline (AOS-387)"
  local pats rc=0 out
  if ! pats="$(cd "$ROOT" && GOWORK="$WORK" GOPROXY=off GOFLAGS=-mod=readonly go list -m 2>&1)"; then
    log_fail "gowork: o Go não carrega o workspace: $pats"
    return 1
  fi
  # shellcheck disable=SC2046 # um padrão `<módulo>/...` por linha, sem espaços
  if out="$(cd "$ROOT" && GOWORK="$WORK" GOPROXY=off GOFLAGS=-mod=readonly \
              go build $(printf '%s\n' "$pats" | sed 's#$#/...#') 2>&1)"; then
    log_ok "gowork: os $(printf '%s\n' "$pats" | wc -l | tr -d ' ') módulos do workspace compilam juntos, offline"
  else
    printf '%s\n' "$out" >&2
    log_fail "gowork: o workspace não compila offline (GOPROXY=off, -mod=readonly)"
    rc=1
  fi
  if [ -e "$ROOT/go.work.sum" ] && ! git -C "$ROOT" ls-files --error-unmatch go.work.sum >/dev/null 2>&1; then
    log_fail "gowork: apareceu um go.work.sum NÃO versionado — o workspace precisa de checksums que os go.sum não têm; versione-o (tecnica/11 §8.1, decisão (b))"
    rc=1
  fi
  return "$rc"
}

case "$modo" in
  gerar)     gerar ;;
  verificar) verificar ;;
  compilar)  compilar ;;
  *) log_fail "gowork: modo desconhecido '$modo' (gerar|verificar|compilar)"; exit 2 ;;
esac
