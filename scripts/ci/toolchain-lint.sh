#!/usr/bin/env bash
# toolchain-lint.sh — os gates correm com a MESMA toolchain que constroi producao.
#
# O DEFEITO QUE FECHA. O gate de SCA, corrido a mao numa maquina com Go 1.26.5, acusava
# CINCO vulnerabilidades de stdlib que a CI (1.25.13) nunca via:
#
#   Found in: encoding/asn1@go1.26.5   Fixed in: encoding/asn1@go1.26.6
#
# Nao era o repositorio a ter vulnerabilidades: era a toolchain do posto de trabalho a estar
# atras dos patches. Mas quem corresse o gate lia o contrario — e um gate que falha por uma
# razao que nao e a que anuncia treina as pessoas a ignora-lo.
#
# PORQUE NAO SE VERIFICA A DIRECTIVA `toolchain` DOS go.mod: ela e um PISO, nao um pino.
# `toolchain go1.25.13` pede "pelo menos 1.25.13", e um 1.26.5 instalado satisfa-la — o Go
# nunca DESCE. Medido: com a directiva em todos os modulos, o govulncheck continuava a
# reportar `encoding/asn1@go1.26.5`. So `GOTOOLCHAIN` forca a versao exacta.
#
# O QUE SE VERIFICA E A CABLAGEM, e nao a intencao: corre-se o `setup_env` REAL e pergunta-se
# ao `go` com que versao ele ficou. Um gate que so lesse a linha do `lib.sh` passaria com a
# variavel a ser depois ignorada — o padrao que ja custou dezasseis achados a este repositorio.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

log_gate "toolchain · os gates correm com a toolchain que constroi producao"
rc=0

dockerfile="$REPO_ROOT/deploy/node/Dockerfile"
if [ ! -f "$dockerfile" ]; then
  log_fail "Dockerfile de producao ausente ($dockerfile) — sem autoridade o gate seria vacuo"
  exit 1
fi
versao="$(grep -oE '^FROM golang:[0-9]+\.[0-9]+\.[0-9]+' "$dockerfile" | head -1 | sed 's/^FROM golang://' || true)"
if [ -z "$versao" ]; then
  log_fail "nao consegui ler a versao do Go de $dockerfile — bloqueia em vez de afirmar alinhamento"
  exit 1
fi
esperado="go${versao}"
log_step "autoridade: a imagem de producao constroi com $esperado"

# (a) O setup_env FIXOU a variavel?
setup_env
if [ "${GOTOOLCHAIN:-}" != "$esperado" ]; then
  log_fail "setup_env deixou GOTOOLCHAIN=${GOTOOLCHAIN:-<vazia>} mas producao constroi com $esperado"
  rc=1
fi

# (b) E o `go` OBEDECEU? E esta a asercao que importa: a variavel podia estar certa e o
#     comando correr com outra coisa (GOTOOLCHAIN=local no ambiente, toolchain por
#     descarregar, GOFLAGS a interferir). Pergunta-se ao proprio binario.
efectiva="$(go version | grep -oE 'go[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"
if [ -z "$efectiva" ]; then
  log_fail "nao consegui ler a versao efectiva de \`go version\` — o gate nao pode afirmar alinhamento"
  rc=1
elif [ "$efectiva" != "$esperado" ]; then
  log_fail "os gates correriam com $efectiva mas producao constroi com $esperado — resultados de SCA/lint/test nao transferem para a CI"
  rc=1
else
  log_ok "toolchain efectiva: $efectiva (igual a da imagem de producao)"
fi

[ "$rc" -eq 0 ] || exit 1
