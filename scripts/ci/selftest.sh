#!/usr/bin/env bash
# selftest.sh — Auto-testes dos gates do CI (AOS-010, "Testes Requeridos").
#
# Prova, de forma ISOLADA e REVERSÍVEL (sem deixar rasto no repo), que os gates
# BLOQUEIAM falhas (exit != 0). Cada subteste injecta temporariamente uma falha,
# corre o gate real, assevera que fica VERMELHO, e limpa.
#
#   A) lint/test bloqueiam — módulo mau injectado (gofmt sujo + teste que falha);
#   B) política não-assinada/adulterada falha o policy-test — bundle committado é
#      adulterado (backup+restore), o gate tem de ficar vermelho;
#   C) CVE crítico bloqueia o SCA — prova determinista (offline) de que uma vuln
#      afetante fora da baseline faz o comparador do SCA falhar fechado;
#   D) trajectória adulterada bloqueia o gate 8 (replay/idempotência);
#   E) violação de invariante de memória bloqueia o gate memory (AOS-044);
#   F) vector de supply-chain desbloqueado bloqueia o gate supplychain (AOS-054);
#   G) cenário de roteamento/failover desbloqueado bloqueia o gate routing (AOS-063);
#   H) controlo de segurança desligado bloqueia o gate security (AOS-075).
#
# NOTA: um self-test PASSA quando o gate correspondente FALHA como esperado.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

fails=0
pass() { printf '%sSELFTEST OK%s   %s\n' "$C_GRN" "$C_RST" "$*"; }
bad()  { printf '%sSELFTEST FAIL%s %s\n' "$C_RED" "$C_RST" "$*" >&2; fails=1; }

BAD_MOD="$REPO_ROOT/packages/_selftest_bad"
SIG="$REPO_ROOT/packages/control-plane/pdp/policies/aos_authz.sig"
SIG_BAK="$(mktemp)"
cp "$SIG" "$SIG_BAK"

cleanup() {
  rm -rf "$BAD_MOD"
  # Restaura sempre a assinatura committada byte-a-byte (sem rasto).
  if [ -f "$SIG_BAK" ]; then cp "$SIG_BAK" "$SIG"; rm -f "$SIG_BAK"; fi
}
trap cleanup EXIT INT TERM

# ============================================================================
# A) lint e test bloqueiam um "PR mau" (módulo isolado, injectado e removido)
# ============================================================================
log_gate "self-test A · lint/test bloqueiam módulo mau"
mkdir -p "$BAD_MOD"
cat > "$BAD_MOD/go.mod" <<'EOF'
module aos-selftest/bad

go 1.24
EOF
# Ficheiro gofmt-SUJO (espaçamento inválido) — o gate de lint (gofmt -l) TEM de o apanhar.
printf 'package bad\n\nfunc  Bad( )  {}\n' > "$BAD_MOD/bad.go"
# Teste que FALHA — o gate de test TEM de ficar vermelho.
cat > "$BAD_MOD/bad_test.go" <<'EOF'
package bad

import "testing"

func TestSelftestFalhaProposital(t *testing.T) {
	t.Fatal("falha proposital do self-test (deve bloquear o gate)")
}
EOF

# A1: o gate de LINT (real) tem de falhar (gofmt sujo).
if bash "$CI_DIR/lint.sh" >/dev/null 2>&1; then
  bad "A1: lint.sh passou com um ficheiro gofmt-sujo — gate NÃO bloqueou"
else
  pass "A1: lint.sh bloqueou (exit!=0) o ficheiro gofmt-sujo"
fi

# A2: o gate de TEST (mesmos flags do gate) tem de falhar no módulo mau.
if ( cd "$BAD_MOD" && go test ./... -race -covermode=atomic -coverprofile="$(mktemp)" ) >/dev/null 2>&1; then
  bad "A2: go test passou com um teste que falha — gate NÃO bloqueou"
else
  pass "A2: o gate de test bloqueou (exit!=0) o teste que falha"
fi
rm -rf "$BAD_MOD"

# ============================================================================
# B) política não-assinada/adulterada falha o policy-test
# ============================================================================
log_gate "self-test B · bundle adulterado falha o policy-test"
# Adultera a assinatura committada do bundle (base64 -> lixo). O bundle passa a
# NÃO verificável: o policy-test (TestReferenceBundle_Assinado abre o bundle real)
# TEM de ficar vermelho.
printf 'Zm9yamFkYS1hc3NpbmF0dXJhLWludmFsaWRh\n' > "$SIG"
if bash "$CI_DIR/policy-test.sh" >/dev/null 2>&1; then
  bad "B: policy-test passou com assinatura adulterada — gate NÃO bloqueou"
else
  pass "B: policy-test bloqueou (exit!=0) o bundle adulterado/não-verificável"
fi
cp "$SIG_BAK" "$SIG"   # restaura já (o trap volta a garantir)
# Verifica restauro sem rasto.
if [ "$(sha256sum "$SIG" | awk '{print $1}')" = "$(sha256sum "$SIG_BAK" | awk '{print $1}')" ]; then
  pass "B: assinatura committada restaurada byte-a-byte (sem rasto)"
else
  bad "B: restauro da assinatura divergente — POSSÍVEL RASTO no repo"
fi

# ============================================================================
# C) CVE crítico bloqueia o SCA (prova determinista, offline)
# ============================================================================
log_gate "self-test C · vuln afetante fora da baseline bloqueia o SCA"
# Injecta um achado sintético "afetante" ausente da baseline e corre o MESMO
# comparador (sca_decide) que o gate de SCA usa. Determinista e sem rede volátil
# (conforme permitido pelo desenho do ticket).
syn_cur="$(mktemp)"; syn_base="$(mktemp)"
printf 'packages/control-plane/pdp|GO-9999-9999\n' > "$syn_cur"   # CVE crítico simulado
: > "$syn_base"                                                    # baseline sem essa vuln
if sca_decide "$syn_cur" "$syn_base" >/dev/null; then
  bad "C: sca_decide aceitou uma vuln afetante fora da baseline — SCA NÃO bloquearia"
else
  pass "C: sca_decide bloqueou (exit!=0) a vuln afetante fora da baseline"
fi
# Controlo negativo: a MESMA vuln, se estiver na baseline, é aceite (não bloqueia).
printf 'packages/control-plane/pdp|GO-9999-9999\n' > "$syn_base"
if sca_decide "$syn_cur" "$syn_base" >/dev/null; then
  pass "C: controlo — vuln já triada na baseline não bloqueia"
else
  bad "C: controlo falhou — baseline não suprime uma vuln já triada"
fi
rm -f "$syn_cur" "$syn_base"

# ============================================================================
# D) trajectória adulterada torna o gate 8 (replay/idempotência) vermelho
# ============================================================================
log_gate "self-test D · trajectória adulterada bloqueia o gate 8 (replay)"
# O harness de AOS-024 tem um teste-veneno (TestSelftestTamperReddensGate) que só
# corre com AOS_REPLAY_SELFTEST=1: adultera uma golden trajectory e ASSEVERA
# (falsamente) que ela é fiel — a asserção FALHA de propósito, provando que uma
# trajectória divergente faz o gate ficar vermelho (fail-closed). Determinista,
# offline e sem rasto no repo (nenhum ficheiro é alterado).
if ( cd "$REPO_ROOT/packages/kernel/agent-runtime" && \
     AOS_REPLAY_SELFTEST=1 go test ./harness/ -run TestSelftestTamperReddensGate -count=1 ) >/dev/null 2>&1; then
  bad "D: o harness aceitou uma trajectória adulterada — gate 8 NÃO bloquearia"
else
  pass "D: o harness bloqueou (exit!=0) a trajectória adulterada (gate 8 fail-closed)"
fi

# ============================================================================
# E) violação de invariante de memória torna o gate memory (AOS-044) vermelho
# ============================================================================
log_gate "self-test E · violação de integridade bloqueia o gate memory (AOS-044)"
# A suite de AOS-044 tem um teste-veneno (TestSelftestMemoryViolationReddensGate) que só
# corre com AOS_MEMORY_SELFTEST=1: injecta um sink que NÃO preserva o registo evictado e
# ASSEVERA (falsamente) que ele foi preservado — a asserção FALHA de propósito, provando
# que uma violação de invariante de memória torna o gate VERMELHO (fail-closed).
# Determinista, offline e sem rasto no repo (nenhum ficheiro é alterado).
if ( cd "$REPO_ROOT/packages/platform/memory" && \
     AOS_MEMORY_SELFTEST=1 go test ./integritytests/ -run TestSelftestMemoryViolationReddensGate -count=1 ) >/dev/null 2>&1; then
  bad "E: a suite aceitou um registo apagado — gate memory NÃO bloquearia"
else
  pass "E: a suite bloqueou (exit!=0) a violação de integridade injectada (gate memory fail-closed)"
fi

# ============================================================================
# F) vector de supply-chain desbloqueado torna o gate supplychain (AOS-054) vermelho
# ============================================================================
log_gate "self-test F · vector desbloqueado bloqueia o gate supplychain (AOS-054)"
# A suite de AOS-054 tem um teste-veneno (TestSelftestSupplychainBypassReddensGate) que
# só corre com AOS_SUPPLYCHAIN_SELFTEST=1: reproduz o rug-pull com o controlo CONTORNADO
# (a chave do atacante adicionada ao trust store) — pelo que a promoção é ADMITIDA — e
# ASSEVERA (falsamente) que foi bloqueada; a asserção FALHA de propósito, provando que um
# vector desbloqueado torna o gate VERMELHO (fail-closed). Determinista, offline e sem
# rasto no repo (nenhum ficheiro é alterado).
if ( cd "$REPO_ROOT/packages/platform/registry" && \
     AOS_SUPPLYCHAIN_SELFTEST=1 go test ./supplychaintests/ -run TestSelftestSupplychainBypassReddensGate -count=1 ) >/dev/null 2>&1; then
  bad "F: a suite aceitou um rug-pull com o controlo desligado — gate supplychain NÃO bloquearia"
else
  pass "F: a suite bloqueou (exit!=0) o vector desbloqueado injectado (gate supplychain fail-closed)"
fi

# ============================================================================
# G) cenário de roteamento desbloqueado torna o gate routing (AOS-063) vermelho
# ============================================================================
log_gate "self-test G · cenário desbloqueado bloqueia o gate routing (AOS-063)"
# A suite de AOS-063 tem um teste-veneno (TestSelftestRoutingBypassReddensGate) que só
# corre com AOS_ROUTING_SELFTEST=1: reproduz o failover CROSS-BORDER com a soberania
# CONTORNADA (guarda com fronteiras colapsadas + allowlist fail-open) — pelo que a rota
# resolve para us-east — e ASSEVERA (falsamente) que foi bloqueada; a asserção FALHA de
# propósito, provando que um cenário desbloqueado torna o gate VERMELHO (fail-closed).
# Determinista, offline e sem rasto no repo (nenhum ficheiro é alterado).
if ( cd "$REPO_ROOT/packages/platform/model-gateway" && \
     AOS_ROUTING_SELFTEST=1 go test ./routingtests/ -run TestSelftestRoutingBypassReddensGate -count=1 ) >/dev/null 2>&1; then
  bad "G: a suite aceitou um failover cross-border com a soberania desligada — gate routing NÃO bloquearia"
else
  pass "G: a suite bloqueou (exit!=0) o cenário desbloqueado injectado (gate routing fail-closed)"
fi

# ============================================================================
# H) controlo de segurança desligado torna o gate security (AOS-075) vermelho
# ============================================================================
log_gate "self-test H · controlo desligado bloqueia o gate security (AOS-075)"
# A suite de AOS-075 tem um teste-veneno (TestSelftestSecurityBypassReddensGate) que só
# corre com AOS_SECURITY_SELFTEST=1: reproduz a PROMPT INJECTION com o controlo CONTORNADO
# (o TaintGate REMOVIDO da cadeia do RM) — pelo que a acção privilegiada autorizada por
# untrusted é ADMITIDA — e ASSEVERA (falsamente) que foi bloqueada; a asserção FALHA de
# propósito, provando que um controlo desligado torna o gate VERMELHO (fail-closed).
# Determinista, offline e sem rasto no repo (nenhum ficheiro é alterado).
if ( cd "$REPO_ROOT/packages/security-tests" && \
     AOS_SECURITY_SELFTEST=1 go test ./ -run TestSelftestSecurityBypassReddensGate -count=1 ) >/dev/null 2>&1; then
  bad "H: a suite aceitou uma injecção com o TaintGate desligado — gate security NÃO bloquearia"
else
  pass "H: a suite bloqueou (exit!=0) o controlo desligado injectado (gate security fail-closed)"
fi

# ============================================================================
# I) cobertura abaixo do limiar bloqueia o gate 3 (AOS-109) — teste do próprio gate
# ============================================================================
log_gate "self-test I · cobertura < limiar bloqueia o gate de cobertura (AOS-109)"
# Exercita DIRECTAMENTE o predicado fail-closed que o gate 3 usa (coverage_meets_min),
# à imagem do self-test C (que corre o comparador sca_decide directamente). Prova, de
# forma determinista e offline (sem rebuild da suite), que uma cobertura ABAIXO do
# limiar NÃO satisfaz o piso — logo o gate sai != 0 e bloqueia o merge.
if coverage_meets_min "50.0%" 99; then
  bad "I: coverage_meets_min aceitou 50% < 99% — o gate de cobertura NÃO bloquearia"
else
  pass "I: coverage_meets_min rejeitou (exit!=0) 50% < 99% (gate de cobertura fail-closed)"
fi
# Controlo positivo: uma cobertura >= limiar é aceite (o gate não bloqueia à toa).
if coverage_meets_min "95.3%" 80; then
  pass "I: controlo — cobertura 95.3% >= 80% é aceite (não bloqueia falsamente)"
else
  bad "I: controlo falhou — 95.3% >= 80% devia ser aceite"
fi
# Fail-closed em cobertura NÃO-MENSURÁVEL: um módulo sem cobertura (FALHOU/n/a/vazio)
# nunca satisfaz o piso (uma acção não-mensurável não é promovida).
if coverage_meets_min "FALHOU" 0 || coverage_meets_min "n/a" 0 || coverage_meets_min "" 0; then
  bad "I: cobertura não-mensurável foi aceite — gate NÃO seria fail-closed"
else
  pass "I: cobertura não-mensurável (FALHOU/n/a/vazio) rejeitada (fail-closed)"
fi

# ============================================================================
printf '\n%s============ RESUMO DOS SELF-TESTS ============%s\n' "$C_BLD" "$C_RST"
if [ "$fails" -eq 0 ]; then
  printf '%s  TODOS OS SELF-TESTS OK — falhas são bloqueadas pelos gates%s\n' "$C_GRN$C_BLD" "$C_RST"
else
  printf '%s  SELF-TESTS VERMELHOS — um gate não bloqueou como devia%s\n' "$C_RED$C_BLD" "$C_RST"
fi
exit "$fails"
