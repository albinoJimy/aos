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
     'TestArchLint_RMNaoTemBypass|TestArchLint_NenhumBypassNoRepositorio|TestArchLint_InvocacaoDirectaDeToolFuncEZero|TestAnalyze_CasoBom|TestAnalyze_CasoMau' \
     TestArchLint_RMNaoTemBypass TestArchLint_NenhumBypassNoRepositorio \
     TestArchLint_InvocacaoDirectaDeToolFuncEZero TestAnalyze_CasoBom TestAnalyze_CasoMau; then
  log_ok "arch-lint: regra de despacho directo verde"
else
  log_fail "arch-lint: violação da regra AOS-003 (ou teste crítico não correu)"
  rc=1
fi


# --- 2e. entrega do servidor: modo de execucao e cobertura do rsync -----------
#
# O DEFEITO QUE FECHA, encontrado em producao a 2026-08-23: o `aos-tls-sync.service`
# falhava ha dias com `203/EXEC` porque o `/opt/aos/sync-tls.sh` chegava ao servidor
# SEM bit de execucao. O deploy entregava-o — o `rsync -az` preserva o modo, e o modo
# no git era `100644`. O systemd nao consegue executar um ficheiro sem `+x`, e falhava
# em silencio: o timer disparava todos os dias, o servico falhava, e nada alertava.
#
# DUAS VERIFICACOES, e sao independentes:
#   (a) todo o ficheiro que um `ExecStart=` nomeia TEM de ter modo 755 no git;
#   (b) todo o ficheiro que um `ExecStart=` nomeia TEM de estar na lista do `rsync`
#       do deploy — um bit de execucao correcto num ficheiro que nunca chega ao
#       servidor nao serve de nada.
#
# NAO se exige `+x` a TODOS os `.sh`: o `deploy.sh`, o `rollback.sh` e o `backup.sh`
# sao invocados via `bash /opt/aos/...` e nao precisam. Exigi-lo seria ceremonia sem
# propriedade, e a proxima pessoa desligaria o gate em vez de o obedecer.
log_gate "lint · entrega do servidor (ExecStart)"
unidades="$REPO_ROOT/deploy/server/systemd"
if [ -d "$unidades" ]; then
  n_exec=0
  while IFS= read -r alvo; do
    [ -n "$alvo" ] || continue
    n_exec=$((n_exec + 1))
    ficheiro="deploy/server/$(basename "$alvo")"
    if [ ! -f "$REPO_ROOT/$ficheiro" ]; then
      log_fail "ExecStart nomeia $alvo e $ficheiro NAO existe no repositorio"
      rc=1
      continue
    fi
    modo="$( cd "$REPO_ROOT" && git ls-files -s "$ficheiro" | awk '{print $1}' )"
    if [ "$modo" != "100755" ]; then
      log_fail "$ficheiro e executado por systemd (ExecStart=$alvo) e tem modo $modo — o systemd responde 203/EXEC. Corrige com: git update-index --chmod=+x $ficheiro"
      rc=1
    else
      log_ok "$ficheiro: modo 755 (executavel pelo systemd)"
    fi
    if ! grep -q "$ficheiro" "$REPO_ROOT/.github/workflows/deploy.yml"; then
      log_fail "$ficheiro e executado por systemd e NAO consta do rsync do deploy — nunca chega ao servidor"
      rc=1
    fi
  done < <(grep -rhoE '^ExecStart=[^ ]+' "$unidades" 2>/dev/null | sed 's/^ExecStart=//')
  # CONTROLO ANTI-VACUIDADE: se nenhum ExecStart for encontrado, este bloco passa por
  # nao ter feito nada. Um gate que nao encontra o que verifica tem de gritar.
  if [ "$n_exec" -eq 0 ]; then
    log_fail "nenhum ExecStart encontrado em $unidades — o gate nao verificou nada"
    rc=1
  else
    log_ok "entrega do servidor: $n_exec ExecStart verificado(s)"
  fi
fi
[ "$rc" -eq 0 ] && log_ok "lint: verde" || log_fail "lint: vermelho"
exit "$rc"
