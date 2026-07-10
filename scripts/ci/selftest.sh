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
#      afetante fora da baseline faz o comparador do SCA falhar fechado.
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
printf '\n%s============ RESUMO DOS SELF-TESTS ============%s\n' "$C_BLD" "$C_RST"
if [ "$fails" -eq 0 ]; then
  printf '%s  TODOS OS SELF-TESTS OK — falhas são bloqueadas pelos gates%s\n' "$C_GRN$C_BLD" "$C_RST"
else
  printf '%s  SELF-TESTS VERMELHOS — um gate não bloqueou como devia%s\n' "$C_RED$C_BLD" "$C_RST"
fi
exit "$fails"
