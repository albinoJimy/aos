#!/usr/bin/env bash
# layer-lint.sh — Gate de lint de fronteiras de camadas (AOS-178).
#
# Valida que os módulos Go em packages/ respeitam as fronteiras canónicas:
#   control-plane só importa control-plane e kernel;
#   kernel só importa kernel, platform e substrate;
#   platform só importa platform e substrate;
#   substrate só importa substrate.
# Módulos de composição/teste (cmd/*, integration, testkit, qa/*, security-tests)
# são isentos como importadores (podem importar qualquer camada), mas não podem
# ser importados por camadas de produção. Inversões conhecidas (baseline) são
# toleradas até AOS-179 as resolver.
#
# Uso:
#   scripts/ci/layer-lint.sh
#   scripts/ci/layer-lint.sh --root /caminho/para/arvore/temporaria
#   LAYER_LINT_ROOT=/caminho/para/arvore scripts/ci/layer-lint.sh
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# --- Argumentos ----------------------------------------------------------------
ROOT="${REPO_ROOT}"
if [ "${1:-}" = "--root" ]; then
  ROOT="${2:-}"
  shift 2
elif [ -n "${LAYER_LINT_ROOT:-}" ]; then
  ROOT="${LAYER_LINT_ROOT}"
fi

if [ ! -d "$ROOT/packages" ]; then
  log_fail "root inválido: não existe $ROOT/packages"
  exit 1
fi

# --- Grafo canónico ------------------------------------------------------------
# Para cada camada produtiva, lista as camadas de destino permitidas.
declare -A LAYER_ALLOWED=(
  [substrate]="substrate"
  [platform]="platform substrate"
  [kernel]="kernel platform substrate"
  [control-plane]="control-plane kernel"
)

# Camadas isentas da regra de importação. Podem importar qualquer camada, mas
# não podem ser importadas por camadas de produção.
is_exempt_layer() {
  case "$1" in
    cmd|integration|testkit|qa|security-tests) return 0 ;;
    *) return 1 ;;
  esac
}

# --- Helpers -------------------------------------------------------------------
# Verifica se uma camada destino é permitida para uma camada origem.
layer_allowed() {
  local from="$1" to="$2"
  if [[ "$from" = "$to" ]]; then
    return 0
  fi
  case " ${LAYER_ALLOWED[$from]:-} " in
    *" $to "*) return 0 ;;
  esac
  return 1
}

# Extrai a camada canónica de um import path github.com/aos-ref/<camada>/...
layer_of() {
  local path="$1"
  local layer
  layer="${path#github.com/aos-ref/}"
  layer="${layer%%/*}"
  printf '%s' "$layer"
}

# Verifica se um par pacote|importação consta da baseline.
# A baseline pode conter padrões glob (*), que são expandidos em bash case.
in_baseline() {
  key="$1"
  base="$2"
  [ -f "$base" ] || return 1
  local pattern
  while IFS= read -r pattern; do
    [ -n "$pattern" ] || continue
    case "$pattern" in
      *'*'*)
        case "$key" in
          $pattern) return 0 ;;
        esac
        ;;
      *)
        [ "$pattern" = "$key" ] && return 0
        ;;
    esac
  done < "$base"
  return 1
}

# --- Carregar baseline ---------------------------------------------------------
BASELINE_FILE="${BASELINE_DIR}/layer-lint-exceptions.txt"
BASELINE_KEYS="$(mktemp)"
trap 'rm -f "$BASELINE_KEYS" "$TMP_ALL" "$TMP_ERR" "$TMP_VIOL"' EXIT INT TERM

if [ -f "$BASELINE_FILE" ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    # ignorar linhas em branco e comentários puros
    case "$line" in ''|'#'*) continue ;; esac
    # extrair parte antes do primeiro '#' e remover espaços nos extremos
    key="${line%%#*}"
    key="${key%"${key##*[![:space:]]}"}"  # rtrim
    key="${key#"${key%%[![:space:]]*}"}"  # ltrim
    [ -n "$key" ] || continue
    printf '%s\n' "$key"
  done < "$BASELINE_FILE" > "$BASELINE_KEYS"
fi

# --- Varrimento ---------------------------------------------------------------
TMP_ALL="$(mktemp)"
TMP_ERR="$(mktemp)"
TMP_VIOL="$(mktemp)"

log_gate "layer-lint — fronteiras de camadas (AOS-178)"
log_step "root: $ROOT"

while IFS= read -r moddir; do
  [ -n "$moddir" ] || continue
  log_step "analisar $moddir"
  if ! ( cd "$ROOT/$moddir" && go list -f '{{.ImportPath}} {{join .Imports " "}}' ./... ) > "$TMP_ALL" 2> "$TMP_ERR"; then
    log_fail "go list falhou em $moddir:"
    sed 's/^/       /' "$TMP_ERR" >&2 || true
    exit 1
  fi

  while IFS= read -r line; do
    [ -n "$line" ] || continue
    set -- $line
    from_pkg="$1"
    shift

    from_layer="$(layer_of "$from_pkg")"
    # Pacotes de camada isenta não são avaliados como importadores.
    if is_exempt_layer "$from_layer"; then
      continue
    fi

    if [[ ! -v LAYER_ALLOWED[$from_layer] ]]; then
      printf '%s|%s # camada desconhecida do pacote: %s\n' "$from_pkg" "$from_pkg" "$from_layer" >> "$TMP_VIOL"
      continue
    fi

    for to_pkg in "$@"; do
      # Só nos interessam imports internos (github.com/aos-ref/*).
      case "$to_pkg" in github.com/aos-ref/*) ;; *) continue ;; esac

      to_layer="$(layer_of "$to_pkg")"

      # Camada isenta importada por camada de produção é violação.
      if is_exempt_layer "$to_layer"; then
        key="$from_pkg|$to_pkg"
        if ! in_baseline "$key" "$BASELINE_KEYS"; then
          printf '%s # importação de módulo isento por camada de produção\n' "$key" >> "$TMP_VIOL"
        fi
        continue
      fi

      if [[ ! -v LAYER_ALLOWED[$to_layer] ]]; then
        key="$from_pkg|$to_pkg"
        if ! in_baseline "$key" "$BASELINE_KEYS"; then
          printf '%s # camada desconhecida do import: %s\n' "$key" "$to_layer" >> "$TMP_VIOL"
        fi
        continue
      fi

      # Fronteira canónica: mesmo camada ou destino na lista permitida.
      if ! layer_allowed "$from_layer" "$to_layer"; then
        key="$from_pkg|$to_pkg"
        if ! in_baseline "$key" "$BASELINE_KEYS"; then
          printf '%s # violação de fronteira: %s -> %s\n' "$key" "$from_layer" "$to_layer" >> "$TMP_VIOL"
        fi
      fi
    done
  done < "$TMP_ALL"
done < <( ( cd "$ROOT" && find packages -name go.mod -print | sed 's#/go.mod$##' | sort ) )

# --- Resultado ----------------------------------------------------------------
if [ ! -s "$TMP_VIOL" ]; then
  log_ok "nenhuma violação de fronteira fora da baseline"
  exit 0
fi

log_fail "violações de fronteira detectadas fora da baseline:"
sed 's/^/       /' "$TMP_VIOL" >&2
exit 1
