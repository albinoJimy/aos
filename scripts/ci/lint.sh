#!/usr/bin/env bash
# lint.sh — GATE 2 (Lint/format + arch-lint AOS-003). Fail-closed.
#   - gofmt -l   : falha se houver ficheiros por formatar (em todo o packages/).
#   - go vet     : por módulo.
#   - staticcheck: por módulo, comparado com baseline (falha em descobertas NOVAS;
#                  a dívida pré-existente de outros tickets está listada em
#                  baseline/staticcheck.txt — o gate é fail-closed para código novo).
#   - arch-lint  : regra AOS-003 (proibição de despacho directo de tools) — corre o
#                  teste do subpacote archlint que assevera que o RM de produção não
#                  faz bypass.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env
ensure_tool staticcheck "$STATICCHECK_PIN"

rc=0

# --- 2a. gofmt ----------------------------------------------------------------
log_gate "lint · gofmt -l"
unformatted="$( cd "$REPO_ROOT" && gofmt -l packages )"
if [ -n "$unformatted" ]; then
  log_fail "ficheiros por formatar (corre 'gofmt -w'):"
  printf '%s\n' "$unformatted" | sed 's/^/       /' >&2
  rc=1
else
  log_ok "gofmt: tudo formatado"
fi

# --- 2b. go vet ---------------------------------------------------------------
log_gate "lint · go vet"
while IFS= read -r mod; do
  log_step "vet $mod"
  if ( cd "$REPO_ROOT/$mod" && go vet ./... ); then
    log_ok "$mod vet limpo"
  else
    log_fail "$mod vet com erros"
    rc=1
  fi
done < <(discover_modules)

# --- 2c. staticcheck (com baseline) ------------------------------------------
log_gate "lint · staticcheck (fail-closed p/ descobertas novas)"
cur="$(mktemp)"; : > "$cur"
while IFS= read -r mod; do
  # staticcheck emite 'file:line:col: msg (CODE)'. Normaliza: prefixa o módulo,
  # remove :line:col (drift), converte '\' -> '/'. Captura o exit sem mascarar.
  sc=0
  out="$( cd "$REPO_ROOT/$mod" && staticcheck ./... 2>&1 )" || sc=$?
  # FAIL-CLOSED: staticcheck sai 0 (limpo) ou 1 (achados, comparados com baseline).
  # Outro código ou erro de compilação/carregamento = não correu -> bloqueia.
  if tool_exec_failed "$sc" "$out" "0,1" 'could not load|compile|no such|internal error|failed to'; then
    log_fail "staticcheck falhou a executar (exit $sc) em $mod — fail-closed:"
    printf '%s\n' "$out" | tail -6 | sed 's/^/       /' >&2
    rc=1
    continue
  fi
  if [ -n "$out" ]; then
    printf '%s\n' "$out" \
      | sed -e "s#^#$mod/#" -e 's/:[0-9]\+:[0-9]\+:/:/' -e 's#\\#/#g' \
      >> "$cur"
  fi
done < <(discover_modules)

if new="$(baseline_diff "$cur" "$BASELINE_DIR/staticcheck.txt")"; then
  log_ok "staticcheck: sem descobertas novas (baseline respeitada)"
else
  log_fail "staticcheck: descobertas NOVAS fora da baseline:"
  printf '%s\n' "$new" | sed 's/^/       /' >&2
  rc=1
fi
rm -f "$cur"

# --- 2d. arch-lint AOS-003 ----------------------------------------------------
log_gate "lint · arch-lint AOS-003 (proibição de despacho directo)"
# require_tests: fail-closed contra "vacuous pass" — exige que ESTES testes tenham
# mesmo corrido (não basta 'go test -run' sair 0 sem casar nada).
if require_tests "$REPO_ROOT/packages/kernel/reference-monitor" "./archlint/" \
     'TestArchLint_RMNaoTemBypass|TestAnalyze_CasoBom|TestAnalyze_CasoMau' \
     TestArchLint_RMNaoTemBypass TestAnalyze_CasoBom TestAnalyze_CasoMau; then
  log_ok "arch-lint: regra de despacho directo verde"
else
  log_fail "arch-lint: violação da regra AOS-003 (ou teste crítico não correu)"
  rc=1
fi

[ "$rc" -eq 0 ] && log_ok "lint: verde" || log_fail "lint: vermelho"
exit "$rc"
