# shellcheck shell=bash
# lib.sh — biblioteca partilhada do gate runner local do AOS (AOS-010).
#
# Fonte de verdade dos gates de CI: estes scripts. A CI (.github/workflows/ci.yml)
# apenas INVOCA scripts/ci/*.sh — não duplica lógica de gates. O runner é
# fail-closed: qualquer gate vermelho aborta com exit != 0. NÃO há '|| true',
# 'continue-on-error' nem 'set +e' a mascarar falhas. Onde capturamos o código de
# saída de uma ferramenta (padrão 'cmd || rc=$?') é SEMPRE para o avaliar e falhar
# fechado a seguir — nunca para o ignorar.
#
# Uso: cada gate faz 'source "$(dirname "$0")/lib.sh"' e chama as helpers.

set -euo pipefail

# --- Localização canónica -----------------------------------------------------
# CI_DIR = scripts/ci ; REPO_ROOT = raiz do monorepo.
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$CI_DIR/../.." && pwd)"
BASELINE_DIR="$CI_DIR/baseline"

# --- Versões pinadas das ferramentas (SCA/SAST/lint) --------------------------
# Pinadas para builds reprodutíveis (ADR-008: supply-chain mínima e determinista).
STATICCHECK_PIN="honnef.co/go/tools/cmd/staticcheck@v0.7.0"
GOVULNCHECK_PIN="golang.org/x/vuln/cmd/govulncheck@v1.6.0"
GOSEC_PIN="github.com/securego/gosec/v2/cmd/gosec@v2.21.4"

# --- Gate de cobertura --------------------------------------------------------
# Cobertura mínima de linhas do kernel (Reference Monitor). AOS-010 AC.
KERNEL_COVERAGE_MIN="${KERNEL_COVERAGE_MIN:-80}"
# Módulo(s) do kernel sujeitos ao gate de cobertura (rel. a REPO_ROOT).
KERNEL_MODULES=("packages/kernel/reference-monitor")

# --- Cores (desligadas se não houver TTY ou se NO_COLOR) ----------------------
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YEL=$'\033[33m'
  C_CYN=$'\033[36m'; C_BLD=$'\033[1m'; C_RST=$'\033[0m'
else
  C_RED=""; C_GRN=""; C_YEL=""; C_CYN=""; C_BLD=""; C_RST=""
fi

log()      { printf '%s\n' "$*"; }
log_gate() { printf '\n%s== GATE: %s ==%s\n' "$C_BLD$C_CYN" "$*" "$C_RST"; }
log_step() { printf '%s--> %s%s\n'  "$C_CYN" "$*" "$C_RST"; }
log_ok()   { printf '%sOK%s   %s\n' "$C_GRN" "$C_RST" "$*"; }
log_warn() { printf '%sWARN%s %s\n' "$C_YEL" "$C_RST" "$*"; }
log_fail() { printf '%sFAIL%s %s\n' "$C_RED" "$C_RST" "$*" >&2; }

# --- Ambiente (Windows/scoop-mingw + Linux) -----------------------------------
# Garante gcc (para -race via CGO) e o bin do GOPATH no PATH; força CGO_ENABLED=1.
# Idempotente e portátil: em Linux o gcc é do sistema; em Windows usa o mingw do
# scoop se existir. NÃO falha se um caminho não existir — apenas o acrescenta.
setup_env() {
  export CGO_ENABLED=1
  local gobin; gobin="$(go env GOPATH)/bin"
  case ":$PATH:" in *":$gobin:"*) ;; *) PATH="$gobin:$PATH";; esac
  # Windows + scoop mingw (gcc para o -race). Silencioso se ausente.
  if [ -n "${HOME:-}" ] && [ -d "$HOME/scoop/apps/mingw/current/bin" ]; then
    case ":$PATH:" in *":$HOME/scoop/apps/mingw/current/bin:"*) ;;
      *) PATH="$HOME/scoop/apps/mingw/current/bin:$PATH";; esac
  fi
  if [ -n "${HOME:-}" ] && [ -d "$HOME/scoop/shims" ]; then
    case ":$PATH:" in *":$HOME/scoop/shims:"*) ;;
      *) PATH="$HOME/scoop/shims:$PATH";; esac
  fi
  export PATH
}

# --- Descoberta de módulos Go -------------------------------------------------
# Ecoa os directórios de módulo (dir que contém go.mod), rel. a REPO_ROOT,
# ordenados. Descoberto dinamicamente — SEM hardcode frágil.
discover_modules() {
  ( cd "$REPO_ROOT" && find packages -name go.mod -print | sed 's#/go.mod$##' | sort )
}

# --- Instalação idempotente de ferramentas (go install pinado) ----------------
# ensure_tool <binário> <pin>. Só instala se o binário não estiver no PATH.
# NÃO committa binários — instala em $(go env GOPATH)/bin.
ensure_tool() {
  local bin="$1" pin="$2"
  if command -v "$bin" >/dev/null 2>&1; then
    log_step "ferramenta presente: $bin"
    return 0
  fi
  log_step "instalar $bin ($pin) ..."
  GOFLAGS="" go install "$pin"
  command -v "$bin" >/dev/null 2>&1 || { log_fail "instalação de $bin falhou"; return 1; }
}

# --- Normalização de caminhos -------------------------------------------------
# Torna um caminho relativo à raiz do repo com '/' e sem prefixo de drive.
# Mantém apenas de 'packages/' em diante (todos os módulos vivem sob packages/).
norm_path() {
  # stdin -> stdout ; converte '\' -> '/' e recorta a partir de packages/
  sed -e 's#\\#/#g' -e 's#.*/packages/#packages/#'
}

# --- Remoção de códigos de cor ANSI ------------------------------------------
# Algumas ferramentas (gosec) colorizam mesmo em -fmt=text. Remove as sequências
# de escape para uma normalização estável. stdin -> stdout.
strip_ansi() { sed 's/\x1b\[[0-9;]*m//g'; }

# --- Comparação com baseline (semântica de multiconjunto) ---------------------
# baseline_diff <ficheiro_actual> <ficheiro_baseline>
#   Imprime em stdout as descobertas NOVAS (presentes no actual e não cobertas
#   pela baseline, contando duplicados). Devolve 1 se houver novas, 0 caso
#   contrário. Descobertas obsoletas (na baseline e já ausentes) só geram WARN.
baseline_diff() {
  local cur="$1" base="$2"
  # Baseline ausente == toda a descoberta é NOVA (compara contra vazio).
  local base_eff="$base"
  [ -f "$base" ] || base_eff="/dev/null"

  local sc sb; sc="$(mktemp)"; sb="$(mktemp)"
  sort "$cur" > "$sc"
  sort "$base_eff" > "$sb"

  local new stale
  new="$(comm -23 "$sc" "$sb" || true)"
  stale="$(comm -13 "$sc" "$sb" || true)"
  rm -f "$sc" "$sb"

  if [ -n "$stale" ]; then
    log_warn "entradas de baseline obsoletas (já não ocorrem) em $(basename "$base"):"
    printf '%s\n' "$stale" | sed 's/^/       /' >&2
  fi
  if [ -n "$new" ]; then
    printf '%s\n' "$new"
    return 1
  fi
  return 0
}

# sca_decide <ficheiro_ids_afetantes> <ficheiro_baseline>
#   Igual a baseline_diff mas com mensagem específica de SCA. Reutilizado pelo
#   self-test determinista do SCA (injecta um CVE fictício não-baseline).
sca_decide() { baseline_diff "$1" "$2"; }

# require_tests <module_dir> <pkg_pattern> <run_regex> <test1> [test2 ...]
#   Corre 'go test <pkg> -run <regex> -v -count=1' e exige (a) que a suite passe e
#   (b) que CADA <testN> tenha EFECTIVAMENTE corrido (linha '--- PASS: testN').
#   Fecha o buraco de "vacuous pass" apanhado no audit: um -run que não casa
#   nenhum teste sai 0 sem correr nada, pelo que um gate baseado só no exit
#   passaria vazio se um teste crítico fosse renomeado/removido. Fail-closed.
require_tests() {
  local dir="$1" pkg="$2" re="$3"; shift 3
  local out rcx=0
  out="$( cd "$dir" && go test "$pkg" -run "$re" -v -count=1 2>&1 )" || rcx=$?
  if [ "$rcx" -ne 0 ]; then
    log_fail "testes vermelhos em $dir ($pkg -run '$re'):"
    printf '%s\n' "$out" | grep -E '^--- FAIL|^FAIL|panic:|no test files' | head -20 | sed 's/^/       /' >&2
    return 1
  fi
  local t missing=0
  for t in "$@"; do
    if ! printf '%s\n' "$out" | grep -qE "^--- PASS: ${t} "; then
      log_fail "teste OBRIGATÓRIO não correu (renomeado/removido/filtro vazio?): ${t}"
      missing=1
    fi
  done
  [ "$missing" -eq 0 ] && log_ok "$# testes obrigatórios correram e passaram em $(basename "$dir")"
  return "$missing"
}

# tool_exec_failed <exit_code> <output> <ok_codes_csv> [error_regex]
#   Distingue "a ferramenta correu" de "falhou a executar", para scanners
#   (gosec/govulncheck/staticcheck) cujo exit != 0 significa TANTO "encontrou algo"
#   (legítimo, parseado à parte) COMO "não conseguiu correr" (rede/DB/toolchain).
#   Devolve 0 (verdadeiro) se a ferramenta FALHOU a executar => o chamador faz
#   fail-closed. ok_codes_csv = códigos que significam "correu" (ex.: "0,3"
#   govulncheck; "0,1" gosec/staticcheck).
tool_exec_failed() {
  local code="$1" out="$2" ok_csv="$3" err_re="${4:-}"
  case ",$ok_csv," in *",$code,"*) : ;; *) return 0 ;; esac
  [ -n "$err_re" ] && printf '%s\n' "$out" | grep -qiE "$err_re" && return 0
  return 1
}
