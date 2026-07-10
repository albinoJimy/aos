#!/usr/bin/env bash
# sast.sh — GATE 5 (SAST). gosec por módulo; falha em findings HIGH/CRITICAL.
# Comparado com baseline (baseline/gosec.txt): a dívida HIGH pré-existente de
# outros tickets está listada; o gate é fail-closed para descobertas NOVAS.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env
ensure_tool gosec "$GOSEC_PIN"

log_gate "sast · gosec (severidade HIGH/CRITICAL)"
cur="$(mktemp)"; : > "$cur"
rc=0

while IFS= read -r mod; do
  log_step "gosec $mod"
  # -severity=high => só HIGH e CRITICAL. Linhas de finding:
  #   [<abs-path>:<line>] - <CODE> (CWE-x): <details> (Confidence:.., Severity: HIGH)
  # Normaliza para: <relpath>|<CODE>  (multiconjunto => nova ocorrência é caçada).
  gs=0
  out="$( cd "$REPO_ROOT/$mod" && gosec -quiet -fmt=text -severity=high ./... 2>&1 )" || gs=$?
  # FAIL-CLOSED: gosec sai 0 (limpo) ou 1 (encontrou issues, parseadas abaixo).
  # Código > 1, ou um erro de análise de pacotes (não compila / não carrega),
  # significa que o scanner NÃO correu — bloqueia em vez de reportar "limpo".
  if tool_exec_failed "$gs" "$out" "0,1" 'could not (load|import|resolve)|no packages|failed to (load|compile|analyze)|build constraints exclude all|expected .* found'; then
    log_fail "gosec falhou a executar (exit $gs) em $mod — fail-closed:"
    printf '%s\n' "$out" | tail -6 | sed 's/^/       /' >&2
    rc=1
    continue
  fi
  printf '%s\n' "$out" \
    | strip_ansi \
    | grep -E '^\[.*\] - G[0-9]+ ' \
    | sed -E 's#^\[(.*):[0-9]+\] - (G[0-9]+) .*#\1|\2#' \
    | norm_path \
    >> "$cur" || true
done < <(discover_modules)

if new="$(baseline_diff "$cur" "$BASELINE_DIR/gosec.txt")"; then
  log_ok "sast: sem findings HIGH/CRITICAL novos (baseline respeitada)"
else
  log_fail "sast: findings HIGH/CRITICAL NOVOS fora da baseline:"
  printf '%s\n' "$new" | sed 's/^/       /' >&2
  rc=1
fi
rm -f "$cur"

[ "$rc" -eq 0 ] && log_ok "sast: verde" || log_fail "sast: vermelho"
exit "$rc"
