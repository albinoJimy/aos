#!/usr/bin/env bash
# security.sh — GATE de SEGURANÇA ADVERSARIAL (AOS-075), o ÚLTIMO do EPIC-07.
#
# Corre a SUITE reutilizável de AOS-075 (packages/security-tests) que ORQUESTRA os
# controlos REAIS da fronteira de segurança (AOS-066..070) e prova, por adversário, que
# CADA vector prioritário de tecnica/07 §9 é REPRODUZIDO e BLOQUEADO:
#   1. PROMPT INJECTION (AOS-069) — injecção em tool result / web / memória (conteúdo
#                        untrusted) NÃO origina acção privilegiada: o TaintGate NEGA a
#                        tool call privilegiada autorizada por untrusted (ADR-005).
#                        Bateria do corpus versionado → 100% bloqueado;
#   2. EXFILTRAÇÃO (AOS-067/068) — egress fora da allowlist (EgressFilter DENY), DNS
#                        tunneling / domínio fora da allowlist (DNSFilter DENY) e tool
#                        "benigna" com recurso mislabelado (EgressHook DENY fail-closed) —
#                        todos negados E (onde nativo) selados no audit WORM (AOS-072);
#   3. SEGREDOS (AOS-070) — o segredo downstream NUNCA observável no output/portadores/
#                        spans/Event Store (scan de sentinela);
#   4. ISOLAMENTO (AOS-066) — overlay não persiste (N+1 não vê N), seccomp bloqueia
#                        syscall fora da allowlist, e a fronteira sem-socket-do-host é
#                        imposta fail-closed.
# Cada cenário tem um META-TESTE que, com o controlo CONTORNADO, deixa o ataque PASSAR
# (prova de detecção não-vácua — não green-vazio).
#
# É o análogo, para a FRONTEIRA DE SEGURANÇA, dos gates supplychain (AOS-054) e routing
# (AOS-063): fail-closed e NÃO-VAZIO. Usa require_tests (lib.sh) para exigir que CADA teste
# obrigatório — os 4 cenários (12 testes) + os 8 META-TESTES (detecção) + o corpus + o
# relatório — tenha EFECTIVAMENTE corrido (não basta o exit 0; um -run que não casasse nada
# passaria vazio). O self-test (scripts/ci/selftest.sh, secção H) prova que um controlo
# DESLIGADO torna este gate VERMELHO.
#
# Fail-closed: um vector não-bloqueado OU um meta-teste que deixe de detectar faz o gate
# ficar VERMELHO (exit != 0). O módulo é SÓ-DE-TESTES (não tem código de produção próprio),
# pelo que NÃO há piso de cobertura — a suite exercita a cobertura dos módulos AOS-066..070
# em runtime (cross-package), não a sua própria.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

SEC_MOD="packages/security-tests"
SUITE_PKG="./..."

# Testes OBRIGATÓRIOS: os 4 cenários (12 testes) + os 8 meta-testes (detecção) + o corpus
# versionado + o relatório. require_tests exige que TODOS tenham corrido (fail-closed
# contra vacuous pass).
REQUIRED=(
  TestPromptInjection_ToolResult_Blocked
  TestPromptInjection_Web_Blocked
  TestPromptInjection_Memory_Blocked
  TestPromptInjection_CorpusBattery_AllBlocked
  TestPromptInjection_ProvenanceLaunderingResisted
  TestExfiltration_EgressOutsideAllowlist_BlockedAndAudited
  TestExfiltration_DNSTunneling_BlockedAndAudited
  TestExfiltration_BenignToolMislabeled_Blocked
  TestSecrets_NeverObservableDownstream
  TestIsolation_OverlayDoesNotPersist
  TestIsolation_SeccompBlocksOutsideAllowlist
  TestIsolation_NoHostSocket
  TestMetaDetects_PromptInjection_WhenTaintGateBypassed
  TestMetaDetects_EgressExfiltration_WhenAllowlistOpen
  TestMetaDetects_MislabeledEgress_WhenDestinationDerivable
  TestMetaDetects_DNSTunneling_WhenFilterBypassed
  TestMetaDetects_SecretLeak_WhenScanned
  TestMetaDetects_OverlayPersistence_WhenReused
  TestMetaDetects_SeccompBypass_WhenProfileOpen
  TestMetaDetects_HostSocket_WhenBoundaryWeakened
  TestCorpusVersionedAndExtensible
  TestSuiteReportEmitted
)
# Regex ancorado (^Test…$) por nome, unido por '|': casa EXACTAMENTE os obrigatórios e
# NUNCA o teste-veneno do self-test (TestSelftestSecurityBypassReddensGate). Evita apanhar
# bónus por substring.
RE="^($(IFS='|'; echo "${REQUIRED[*]}"))\$"

log_gate "security (AOS-075) · 4 cenários adversariais (prompt injection · exfiltração · segredos · isolamento) fail-closed"

# (1) require_tests: os testes obrigatórios (incl. meta-testes) CORRERAM e passaram
# (não-vazio, fail-closed). É o coração do gate — a prova de que a suite não é vacuous.
require_tests "$REPO_ROOT/$SEC_MOD" "$SUITE_PKG" "$RE" "${REQUIRED[@]}" || exit 1

# (2) -race na suite completa (determinismo sob concorrência).
log_step "go test -race $SUITE_PKG"
if ! ( cd "$REPO_ROOT/$SEC_MOD" && go test "$SUITE_PKG" -race -count=1 ); then
  log_fail "suite de segurança vermelha (-race)"
  exit 1
fi

# (3) RELATÓRIO da suite (linha marcada AOS_SECURITY_REPORT) + fail-closed sobre o
# veredicto agregado. À imagem do AOS_SUPPLYCHAIN_REPORT (AOS-054) e AOS_ROUTING_REPORT
# (AOS-063): o campo "pass" agregado é o ÚLTIMO do objecto (…,"pass":true}), pelo que
# ancorar ao fim da linha (}$) faz a verificação reflectir o veredicto AGREGADO.
log_gate "security · relatório da suite"
report="$( cd "$REPO_ROOT/$SEC_MOD" && go test "$SUITE_PKG" -run '^TestSuiteReportEmitted$' -v -count=1 2>/dev/null \
  | grep 'AOS_SECURITY_REPORT' | sed 's/.*AOS_SECURITY_REPORT //' | head -1 )"
if [ -z "$report" ]; then
  log_fail "relatório da suite não emitido"
  exit 1
fi
printf '   %s\n' "$report"
if ! printf '%s' "$report" | grep -Eq '"pass":true[[:space:]]*}[[:space:]]*$'; then
  log_fail "relatório indica cenário não-bloqueado (pass agregado != true)"
  exit 1
fi

log_ok "security: verde (prompt injection + exfiltração + segredos + isolamento bloqueados + meta-testes de detecção + audit WORM)"
