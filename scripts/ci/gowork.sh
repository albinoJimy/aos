#!/usr/bin/env bash
# gowork.sh — gera e verifica o workspace `go.work` da raiz (AOS-387).
#
# O QUE É O go.work AQUI. Um workspace com UMA directiva `use` por módulo de `packages/` — o
# conjunto exacto de `find packages -name go.mod -printf '%h\n'`. Dá ao programador (e ao gopls)
# a resolução inter-módulo local sem depender das `replace`, um `go build` que atravessa os 49
# módulos de uma vez, e reduz a integração de um módulo novo a `go work use`.
#
# O QUE NÃO É. Não é o modo em que os gates correm: lib.sh exporta `GOWORK=off` ao ser
# carregado, pelo que a CI resolve como antes — pelas `replace` committadas de cada `go.mod`. A
# razão e as alternativas medidas estão em tecnica/11 §8.1. Por isso esta verificação é a ÚNICA
# coisa que impede o go.work de apodrecer em silêncio: se a CI não o usa, só um teste explícito
# diz que ele ainda cobre a árvore.
#
# Uso:
#   bash scripts/ci/gowork.sh gerar        # (re)escreve go.work a partir da árvore
#   bash scripts/ci/gowork.sh verificar    # default: falha se go.work divergir da árvore
#   bash scripts/ci/gowork.sh compilar     # build dos módulos em modo workspace, offline
#   bash scripts/ci/gowork.sh <modo> --root <árvore>   # só para o self-test (árvore sintética)
#
# O build.sh (gate 1) corre `verificar` e `compilar`: um módulo novo em packages/ sem `use`
# avermelha o gate build.
#
# A VERIFICAÇÃO É SEMÂNTICA, não byte-a-byte: lê o ficheiro com o próprio parser do Go
# (`go work edit`), para que um `go work use ./packages/x` à mão — que formata à sua maneira —
# seja aceite se o conjunto ficar certo. Exige:
#   (1) o conjunto `use` == conjunto de directórios com go.mod sob packages/, a QUALQUER
#       profundidade (nem a mais, nem a menos). Inclui um go.mod sob testdata/ ou vendor/, se
#       algum dia existir: é o mesmo critério do `discover_modules` (lib.sh), que os builda e
#       testa — o workspace cobre o que os gates cobrem;
#   (2) a directiva `go` == a MAIOR directiva `go` dos módulos. O Go recusa um workspace abaixo
#       de um módulo (`module ... requires go >= 1.25`); acima seria pedir uma linguagem que
#       nenhum módulo declara;
#   (3) a directiva `toolchain` == a MAIOR directiva `toolchain` dos módulos (ausente se nenhum a
#       tiver). Em modo workspace o Go IGNORA as `toolchain` dos go.mod e só lê a do go.work: sem
#       esta linha, os módulos que pedem `toolchain go1.26.9` (os cmd/*) perdiam essa exigência
#       para quem usasse o workspace, e um Go local 1.24 com GOTOOLCHAIN=auto mudaria para
#       go1.25.0 — uma toolchain que não é a de produção, também descarregada da rede. Com ela, o
#       workspace pede exactamente o que o módulo mais exigente já pede em modo clássico, nem
#       mais; num cache aquecido pelo cache-prime (que já instala essa toolchain) não há rede;
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

# _maior_directiva <palavra> — a maior versão da directiva <palavra> (`go`/`toolchain`) nos
# go.mod de packages/, por ordem de versões (não lexical). Vazio se nenhum a tiver.
_maior_directiva() {
  ( cd "$ROOT" && find packages -name go.mod -exec awk -v d="$1" '$1==d{print $2; exit}' {} \; ) \
    | sort -V | tail -1
}
go_esperado()        { _maior_directiva go; }
toolchain_esperada() { _maior_directiva toolchain; }

gerar() {
  local gov tc
  gov="$(go_esperado)"; tc="$(toolchain_esperada)"
  if [ -z "$gov" ]; then
    log_fail "gowork: não consegui determinar a directiva go dos módulos — não escrevo um go.work adivinhado"
    exit 1
  fi
  {
    printf '// go.work — GERADO por scripts/ci/gowork.sh (AOS-387). Não editar à mão a lista `use`:\n'
    printf '// `bash scripts/ci/gowork.sh gerar` (ou `go work use ./packages/<novo>`). Os gates correm\n'
    printf '// com GOWORK=off (lib.sh); ver tecnica/11 §8.1.\n\n'
    printf 'go %s\n\n' "$gov"
    [ -n "$tc" ] && printf 'toolchain %s\n\n' "$tc"
    printf 'use (\n'
    modulos_esperados | sed 's/^/\t/'
    printf ')\n'
  } > "$WORK"
  log_ok "gowork: $WORK escrito ($(modulos_esperados | wc -l | tr -d ' ') módulos, go $gov, toolchain ${tc:-<nenhuma>})"
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
  # O `python3` tem de ser o PROVISIONADO (AOS-480): num checkout limpo em Windows o `python3` do
  # PATH é o atalho da Microsoft Store, que sai 49 com uma mensagem que nada diz sobre o go.work.
  ensure_python || return 1
  local tmp; tmp="$(mktemp -d)"
  # `newline="\n"` em TODAS as escritas (AOS-480). Em Windows o modo texto do Python troca `\n`
  # por `\r\n`, e estes ficheiros são comparados a seguir com a saída de `find | sort`, que é LF:
  # cada linha deixava de casar e o gate dava os 49 módulos como em falta E a mais, com o go.work
  # certo. O self-test GW14 prende a forma.
  printf '%s' "$json" | python3 -c '
import json, sys
w = json.load(sys.stdin)
d = sys.argv[1]
def escreve(nome, texto):
    with open(d + "/" + nome, "w", newline="\n") as f:
        f.write(texto)
escreve("use", "".join(u["DiskPath"] + "\n" for u in (w.get("Use") or [])))
escreve("go", (w.get("Go") or "") + "\n")
escreve("replace", "".join("%s\n" % r["Old"]["Path"] for r in (w.get("Replace") or [])))
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
  if [ "$tcw" != "$tc" ]; then
    log_fail "gowork: directiva toolchain do go.work é '${tcw:-<nenhuma>}'; a maior dos módulos é '${tc:-<nenhuma>}'"
    rc=1
  fi
  if [ -s "$tmp/replace" ]; then
    log_fail "gowork: o go.work tem \`replace\` — as replace vivem nos go.mod (tecnica/11 §8.1, decisão (a)):"
    sed 's/^/       /' "$tmp/replace" >&2
    rc=1
  fi
  rm -rf "$tmp"
  if [ "$rc" -eq 0 ]; then
    log_ok "gowork: go.work cobre os $(modulos_esperados | wc -l | tr -d ' ') módulos de packages/ (go $gov, toolchain ${tc:-<nenhuma>}, sem replace)"
  fi
  return "$rc"
}

# compilar — UM `go build` sobre os módulos do workspace, em modo workspace e OFFLINE.
#
# Prova o que o conjunto `use` sozinho não prova: que o workspace RESOLVE e compila só com o
# que está versionado e com o cache que o cache-prime aquece. Corre com `GOPROXY=off` e
# `-mod=readonly`, depois do build por-módulo.
#
# O go.work.sum NÃO é prova de nada (AOS-387, revisão). Comandos de rotina em modo workspace
# criam-no — `go list -m all`, `go mod download`, até um `go build` — a partir do próprio cache,
# mesmo offline (medido: um módulo sem go.sum nem go.work.sum compila e o Go escreve o
# go.work.sum sozinho). A sua PRESENÇA não diz que seja preciso; só a falta dele pode dizer. Por
# isso: um go.work.sum NÃO versionado é posto de lado durante o build. Se o workspace compila sem
# ele, não é preciso (aviso, e o ficheiro é reposto tal como estava). Se não compila, o vermelho
# diz o que o build disse — e nunca «versione-o» por defeito. Um go.work.sum VERSIONADO é parte
# da árvore e fica no sítio.
SOMA_VERSIONADA=0
SOMA_APARTE=""
SOMA_POR_REPOR=0
# _repor_soma — apaga o go.work.sum que a própria sonda tenha criado e repõe o que estava.
# Idempotente: corre explicitamente no fim e de novo no trap EXIT, se o gate for interrompido.
_repor_soma() {
  [ "$SOMA_POR_REPOR" -eq 1 ] || return 0
  SOMA_POR_REPOR=0
  if [ "$SOMA_VERSIONADA" -eq 0 ]; then rm -f "$ROOT/go.work.sum"; fi
  if [ -n "$SOMA_APARTE" ]; then mv "$SOMA_APARTE" "$ROOT/go.work.sum"; fi
}

compilar() {
  log_gate "go.work · um build que atravessa os módulos todos, em modo workspace e offline (AOS-387)"
  local pats rc=0 out havia_local=0
  # COM REDE NESTE RUN (a CI do GitHub não corre o cache-prime): aquece o grafo do workspace,
  # como o ciclo por-módulo do build.sh aqueceu o de cada módulo. Sem isto, um módulo a fixar
  # outra versão externa deixava o build offline abaixo sem go.mod que só o workspace pede
  # (medido: `go-cmp v0.7.0`). Não é fatal: quem decide é o build offline. Sem rede
  # (GOPROXY=off), o cache tem de vir do cache-prime, que faz o mesmo.
  if [ "$(go env GOPROXY)" != "off" ]; then
    log_step "go mod download · grafo do workspace (há rede neste run; o build abaixo é offline)"
    ( cd "$ROOT" && GOWORK="$WORK" go mod download ) \
      || log_warn "gowork: o download do grafo do workspace falhou; o build offline abaixo decide"
  fi
  if [ -e "$ROOT/go.work.sum" ] && git -C "$ROOT" ls-files --error-unmatch go.work.sum >/dev/null 2>&1; then
    SOMA_VERSIONADA=1
  fi
  SOMA_POR_REPOR=1
  trap _repor_soma EXIT
  if [ -e "$ROOT/go.work.sum" ] && [ "$SOMA_VERSIONADA" -eq 0 ]; then
    SOMA_APARTE="$(mktemp)"
    mv "$ROOT/go.work.sum" "$SOMA_APARTE"
    havia_local=1
  fi

  if ! pats="$(cd "$ROOT" && GOWORK="$WORK" GOPROXY=off GOFLAGS=-mod=readonly go list -m 2>&1)"; then
    _repor_soma
    log_fail "gowork: o Go não carrega o workspace offline: $pats"
    log_fail "  num runner frio, corra antes: bash scripts/ci/cache-prime.sh (aquece também o grafo do workspace)"
    return 1
  fi
  # shellcheck disable=SC2046 # um padrão `<módulo>/...` por linha, sem espaços
  if out="$(cd "$ROOT" && GOWORK="$WORK" GOPROXY=off GOFLAGS=-mod=readonly \
              go build $(printf '%s\n' "$pats" | sed 's#$#/...#') 2>&1)"; then
    log_ok "gowork: os $(printf '%s\n' "$pats" | wc -l | tr -d ' ') módulos do workspace compilam juntos, offline"
    if [ "$havia_local" -eq 1 ]; then
      log_warn "gowork: há um go.work.sum local por versionar; o workspace compila sem ele, logo não é preciso (fica como estava; está no .gitignore)"
    fi
  else
    printf '%s\n' "$out" >&2
    if [ "$havia_local" -eq 1 ]; then
      log_fail "gowork: o workspace não compila offline (GOPROXY=off, -mod=readonly, sem o go.work.sum local)."
    else
      log_fail "gowork: o workspace não compila offline (GOPROXY=off, -mod=readonly)."
    fi
    log_fail "  Se o erro é de módulo/checksum em falta: corra bash scripts/ci/cache-prime.sh (com rede), que aquece o grafo do workspace."
    rc=1
  fi
  _repor_soma
  return "$rc"
}

case "$modo" in
  gerar)     gerar ;;
  verificar) verificar ;;
  compilar)  compilar ;;
  *) log_fail "gowork: modo desconhecido '$modo' (gerar|verificar|compilar)"; exit 2 ;;
esac
