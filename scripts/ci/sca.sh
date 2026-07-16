#!/usr/bin/env bash
# sca.sh — GATE 6 (SCA). govulncheck por módulo; falha em vulnerabilidade que
# AFECTA o código (secção "Symbol Results"). Comparado com baseline
# (baseline/govulncheck.txt): vulns afetantes já triadas (ex.: CVE de stdlib cuja
# remediação é bump de toolchain, fora do âmbito deste ticket de código) estão
# listadas. O gate é fail-closed para QUALQUER vuln afetante nova.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env
ensure_tool govulncheck "$GOVULNCHECK_PIN"

log_gate "sca · govulncheck (vulns que afectam o código)"
cur="$(mktemp)"; : > "$cur"
rc=0

while IFS= read -r mod; do
  log_step "govulncheck $mod"
  gv=0
  out="$( cd "$REPO_ROOT/$mod" && govulncheck ./... 2>&1 )" || gv=$?
  # FAIL-CLOSED: govulncheck sai 0 (sem vulns) ou 3 (vulns encontradas, extraídas
  # abaixo). QUALQUER outro código = falha de execução (rede/DB de vulns/toolchain
  # indisponível) — não podemos afirmar "sem vulnerabilidades" se o scanner não
  # correu. Bloqueia em vez de passar em falso.
  # Nota: os padrões de erro têm de ser ESPECÍFICOS de falha de execução. `connection`
  # e `timeout` "nus" davam FALSO POSITIVO ao casarem com prosa de descrição de
  # vulnerabilidade (ex.: "persistent connection", "excessive ... timeout") quando o
  # govulncheck sai 3 com vulns reais — marcando erradamente uma falha de execução e
  # impedindo a extração dos IDs para comparação com a baseline. Usam-se as formas
  # concretas de erro de rede do Go (connection refused / i/o timeout / dial tcp).
  if tool_exec_failed "$gv" "$out" "0,3" 'govulncheck:|loading|no such|failed to|connection refused|i/o timeout|dial tcp'; then
    log_fail "govulncheck falhou a executar (exit $gv) em $mod — fail-closed:"
    printf '%s\n' "$out" | tail -6 | sed 's/^/       /' >&2
    rc=1
    continue
  fi
  # IDs GO-xxxx-xxxx que aparecem na secção "=== Symbol Results ===" (código
  # afetado). Vulns só "Informational" (importadas mas não chamadas) NÃO contam.
  ids="$( printf '%s\n' "$out" | awk '
      /=== Symbol Results ===/ {sym=1; next}
      /=== .* Results ===/     {sym=0}
      /=== Informational ===/  {sym=0}
      sym && match($0, /GO-[0-9]+-[0-9]+/) {print substr($0, RSTART, RLENGTH)}
    ' | sort -u )"
  if [ -n "$ids" ]; then
    while IFS= read -r id; do printf '%s|%s\n' "$mod" "$id"; done <<< "$ids" >> "$cur"
  fi
done < <(discover_modules)

if new="$(sca_decide "$cur" "$BASELINE_DIR/govulncheck.txt")"; then
  log_ok "sca: sem vulnerabilidades afetantes novas (baseline respeitada)"
else
  log_fail "sca: vulnerabilidades afetantes NOVAS fora da baseline:"
  printf '%s\n' "$new" | sed 's/^/       /' >&2
  rc=1
fi
rm -f "$cur"

[ "$rc" -eq 0 ] && log_ok "sca: verde" || log_fail "sca: vermelho"
exit "$rc"
