#!/usr/bin/env bash
# policy-test.sh — GATE 7 (Teste de política / PDP, AOS-004). Fail-closed.
# Valida o bundle de políticas do PDP:
#   - tabela-verdade golden allow/deny (default-deny) — TestDecide_GoldenTruthTable
#     e a allowlist de capabilities;
#   - verificação de assinatura do bundle COMMITTADO — TestReferenceBundle_Assinado;
#   - rejeição fail-closed de bundle NÃO-ASSINADO / ADULTERADO —
#     TestVerify_FailClosed e TestOpen_TamperedOnDisk (conteúdo, versão, assinatura
#     e anchor adulterados têm de dar ErrSignatureInvalid).
# Estes testes de assinatura do PDP são a fonte de verdade reutilizada; se a
# verificação regredir para fail-open, ficam vermelhos e o gate bloqueia.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

PDP_DIR="$REPO_ROOT/packages/control-plane/pdp"
rc=0

log_gate "policy-test · golden allow/deny (default-deny)"
# require_tests: fail-closed contra "vacuous pass" — exige que CADA teste crítico
# tenha mesmo corrido (um -run que não casa nada sai 0 sem testar).
if require_tests "$PDP_DIR" "./..." \
     'TestDecide_GoldenTruthTable|TestDecide_PolicyVersionRegistada|TestDecide_DefaultDeny_CapabilityAusente|TestDecide_ToolNovaFalhaFechada|TestDecide_AllowExplicitoPorPoliticaAssinada' \
     TestDecide_GoldenTruthTable TestDecide_PolicyVersionRegistada TestDecide_DefaultDeny_CapabilityAusente TestDecide_ToolNovaFalhaFechada TestDecide_AllowExplicitoPorPoliticaAssinada; then
  log_ok "golden allow/deny verde"
else
  log_fail "golden allow/deny vermelho (ou teste crítico não correu)"
  rc=1
fi

log_gate "policy-test · assinatura do bundle (assinado / não-assinado / adulterado)"
if require_tests "$PDP_DIR" "./..." \
     'TestReferenceBundle_Assinado|TestVerify_FailClosed|TestOpen_TamperedOnDisk|TestOpen_SignBundleRoundTrip|TestReload' \
     TestReferenceBundle_Assinado TestVerify_FailClosed TestOpen_TamperedOnDisk; then
  log_ok "verificação de assinatura verde (bundle não-assinado/adulterado é rejeitado)"
else
  log_fail "verificação de assinatura vermelha (ou teste crítico não correu)"
  rc=1
fi

[ "$rc" -eq 0 ] && log_ok "policy-test: verde" || log_fail "policy-test: vermelho"
exit "$rc"
