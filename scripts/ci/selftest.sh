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
#   H) controlo de segurança desligado bloqueia o gate security (AOS-075);
#   I) cobertura abaixo do limiar bloqueia o gate de cobertura (AOS-109);
#   J) invariante vacuosa bloqueia o AC4 do ápice (AOS-151);
#   K) egress desbloqueado bloqueia o enforcement do ápice (AOS-161);
#   L) inversão de camada sintética bloqueia o layer-lint (AOS-178);
#   M) as três listas de required checks coincidem entre si (AOS-190) — ver nota;
#   N) divergência de contrato de porta bloqueia o gate 4 «Integração» (AOS-198);
#   O) literal/concatenação de tipo de evento bloqueia o gate event-catalog (AOS-198);
#   P) título citado de OUTRO ticket é recusado pelo ref-lint (AOS-198, residual STR-01).
#   R) linha da §6 da RTM que atribui um ticket a um epic que não o contém
#      bloqueia o gate rtm (meta-achado de `analises/10` §5).
#   S) citação inexistente na §7 da RTM bloqueia o gate rtm, e a RTM deixa de
#      estar fora do ref-lint (AOS-313).
#   T) a suite recusa correr concorrente consigo própria, e recusa arrancar
#      sobre resíduo de um run morto sem trap (AOS-316).
#
# ESTA SUITE MUTA A ÁRVORE DE TRABALHO. Injecta cada falha nos ficheiros reais e
# restaura-os no `trap`. Não a corra concorrente com edições nem consigo própria:
# desde AOS-316 há exclusão mútua (exit 3) e guarda de resíduo (exit 4), mas o
# que ela não pode impedir é que edite ficheiros por baixo dela enquanto corre.
#
# Os subtestes N4/N5, O3/O4 e P2 existem porque a primeira versão destes três
# gates passava os seus próprios self-testes sendo, ainda assim, contornável: o
# §N só testava o caso fácil da baseline (entrada cujo código EXISTE), o §O só
# exercitava E4 através do corpus real, e o §P era um teste unitário do predicado
# — nenhum deles provava que o gate BLOQUEIA a evasão. Cada um dos novos injecta
# a evasão concreta que foi medida e exige VERMELHO.
#
# NOTA: um self-test PASSA quando o gate correspondente FALHA como esperado.
# Excepção: §M não injecta falha — é uma verificação de COERÊNCIA de configuração.
# Existe porque as listas de required checks são a única mitigação humana caso o
# agregador `gates` seja alguma vez desendurecido; divergirem em silêncio já
# aconteceu (auditoria v4) e não havia nada a detectá-lo.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

fails=0
pass() { printf '%sSELFTEST OK%s   %s\n' "$C_GRN" "$C_RST" "$*"; }
bad()  { printf '%sSELFTEST FAIL%s %s\n' "$C_RED" "$C_RST" "$*" >&2; fails=1; }

# ============================================================================
# Exclusão mútua e guarda de árvore limpa (AOS-316)
# ============================================================================
# Esta suite injecta cada falha NA ÁRVORE REAL e restaura-a a seguir. Dois runs
# sobrepostos partilham esses ficheiros com backups tirados em instantes
# DIFERENTES, e o restauro de um escreve por cima do trabalho do outro. Foi assim
# que um commit saiu com a mensagem de uma mudança e o conteúdo de outra, sem
# nenhum gate dar por isso.
#
# `flock` não existe no Git Bash de Windows; `mkdir` é a primitiva atómica
# portável. O lock vive no *gitdir* — nunca aparece em `git status`, e é por
# worktree, que é o âmbito certo: as mutações são da árvore de trabalho.
#
# Códigos de saída próprios, para que quem chama distinga a RECUSA do vermelho
# de um gate:
#   3 — outro run em curso;
#   4 — a árvore já tinha mutações desta suite à entrada.
_gitdir() { git -C "$REPO_ROOT" rev-parse --absolute-git-dir 2>/dev/null || printf '%s' "$REPO_ROOT"; }
LOCK_DIR="${AOS_SELFTEST_LOCK:-$(_gitdir)/aos-selftest.lock}"
LOCK_ADQUIRIDO=0
LOCK_ORFAO=""

adquirir_lock() {
  local dono
  if mkdir "$LOCK_DIR" 2>/dev/null; then
    printf '%s' "$$" > "$LOCK_DIR/pid"; LOCK_ADQUIRIDO=1; return 0
  fi
  dono="$(cat "$LOCK_DIR/pid" 2>/dev/null || printf '')"
  if [ -n "$dono" ] && kill -0 "$dono" 2>/dev/null; then
    printf '%sERRO%s outro selftest.sh está a correr (PID %s).\n' "$C_RED" "$C_RST" "$dono" >&2
    printf '  A suite muta ficheiros da árvore de trabalho e restaura-os; dois runs\n' >&2
    printf '  sobrepostos corrompem-se um ao outro (AOS-316). Espere que termine.\n' >&2
    printf '  Lock: %s\n' "$LOCK_DIR" >&2
    return 1
  fi
  # Lock órfão: o dono já não existe. O `trap` cobre EXIT INT TERM e NÃO KILL,
  # pelo que esse run pode ter deixado a árvore mutada — toma-se o lock (senão a
  # suite ficava inarrancável para sempre) e a guarda a seguir é que decide.
  LOCK_ORFAO="${dono:-desconhecido}"
  rm -rf "$LOCK_DIR"
  if mkdir "$LOCK_DIR" 2>/dev/null; then
    printf '%s' "$$" > "$LOCK_DIR/pid"; LOCK_ADQUIRIDO=1; return 0
  fi
  printf '%sERRO%s não foi possível tomar o lock %s\n' "$C_RED" "$C_RST" "$LOCK_DIR" >&2
  return 1
}

libertar_lock() { [ "$LOCK_ADQUIRIDO" = 1 ] && rm -rf "$LOCK_DIR"; LOCK_ADQUIRIDO=0; }

# `packages/_selftest_bad/` está em `.gitignore` (`:56`), pelo que `git status` NÃO
# o vê: para os módulos sintéticos a prova de resíduo é EXISTIREM. Para o ficheiro
# rastreado que §B muta, pergunta-se ao git.
verificar_superficie_limpa() {
  local sujos=() d
  if [ -n "$(git -C "$REPO_ROOT" status --porcelain -- "packages/control-plane/pdp/policies/aos_authz.sig" 2>/dev/null)" ]; then
    sujos+=("packages/control-plane/pdp/policies/aos_authz.sig (modificado)")
  fi
  for d in packages/_selftest_bad packages/_selftest_eventcat; do
    [ -e "$REPO_ROOT/$d" ] && sujos+=("$d (resíduo de um run anterior)")
  done
  [ "${#sujos[@]}" -eq 0 ] && return 0
  printf '%sERRO%s a árvore já tem mutações desta suite à entrada:\n' "$C_RED" "$C_RST" >&2
  for d in "${sujos[@]}"; do printf '  - %s\n' "$d" >&2; done
  printf '  Um run anterior terminou sem correr o trap (AOS-316). Sem isto, o backup\n' >&2
  printf '  de arranque seria tirado de uma árvore já corrompida e a suite propagava-a.\n' >&2
  printf '  Restaure antes de repetir:\n' >&2
  printf '    git checkout -- packages/control-plane/pdp/policies/aos_authz.sig\n' >&2
  printf '    rm -rf packages/_selftest_bad packages/_selftest_eventcat\n' >&2
  return 1
}

adquirir_lock || exit 3
if ! verificar_superficie_limpa; then libertar_lock; exit 4; fi
if [ -n "$LOCK_ORFAO" ]; then
  printf '%sAVISO%s tomei um lock órfão (PID %s, já morto); a árvore estava limpa.\n' "$C_YEL" "$C_RST" "$LOCK_ORFAO"
fi

# Seam de teste (§T): provar a exclusão sem pagar a suite inteira. O job de CI não
# define a variável — mesmo molde de `AOS_REFLINT_ROOT` em `ref-lint.py:90`.
if [ -n "${AOS_SELFTEST_LOCK_PROBE:-}" ]; then
  printf 'LOCK OK (pid %s)\n' "$$"
  libertar_lock
  exit 0
fi

BAD_MOD="$REPO_ROOT/packages/_selftest_bad"
SIG="$REPO_ROOT/packages/control-plane/pdp/policies/aos_authz.sig"
SIG_BAK="$(mktemp)"
cp "$SIG" "$SIG_BAK"

LAYER_TMP=""
# Sonda de evento injectada na árvore real (§O3/§O4). Prefixo `_` para que o
# toolchain Go a ignore mesmo se algo correr em paralelo. Removida pelo trap.
EVENT_PROBE="$REPO_ROOT/packages/_selftest_eventcat"
REFLINT_TMP=""
# §R e §S mutam o gerador da RTM numa CÓPIA, desde AOS-316: o gerador aceita
# `AOS_RTM_ROOT`, pelo que a árvore real deixou de fazer parte da superfície
# mutada — R3/S3 apenas a LÊEM. A sandbox é criada em §R e apagada pelo trap.
RTM_SANDBOX=""
RTM_GEN=""
RTM_GEN_BAK=""
# Impressão digital do gerador à ENTRADA. §T5 compara com ela para provar que a
# suite não lhe tocou. Comparar com o git seria outra coisa — daria vermelho a
# quem tivesse o ficheiro por commitar, que é o caso normal de quem o edita.
RTM_GEN_SHA_INICIO="$(git -C "$REPO_ROOT" hash-object "$CI_DIR/rtm-regenerate.py")"
cleanup() {
  rm -rf "$BAD_MOD"
  # Restaura sempre a assinatura committada byte-a-byte (sem rasto).
  if [ -f "$SIG_BAK" ]; then cp "$SIG_BAK" "$SIG"; rm -f "$SIG_BAK"; fi
  rm -rf "$LAYER_TMP"
  rm -rf "$EVENT_PROBE"
  rm -rf "$REFLINT_TMP"
  rm -rf "$RTM_SANDBOX"
  libertar_lock
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
# J) invariante vacuosa avermelha o AC4 do ápice mínimo (AOS-151, PR-0.b)
# ============================================================================
log_gate "self-test J · invariante vacuosa bloqueia o AC4 do ápice (AOS-151)"
# O ápice mínimo (packages/cmd/aos-demo) tem um teste-veneno (TestSelftestApexSufficiencyReddensGate)
# que só corre com AOS_APEX_SELFTEST=1: injecta uma invariante NÃO CLASSIFICADA (nem
# provada nem diferida-com-seam) — exactamente o *vacuous pass* que o AC4 proíbe — e
# assevera (falsamente) que a classificação a aceitou; como o gate a DETECTA, a asserção
# FALHA de propósito, provando que uma invariante vacuosa torna o AC4 VERMELHO. Prova
# que "o ápice mínimo chega" nunca passa por omissão. Determinista, offline, sem rasto.
if ( cd "$REPO_ROOT/packages/cmd/aos-demo" && \
     AOS_APEX_SELFTEST=1 go test ./ -run TestSelftestApexSufficiencyReddensGate -count=1 ) >/dev/null 2>&1; then
  bad "J: o AC4 aceitou uma invariante não-classificada — vacuous pass NÃO bloquearia"
else
  pass "J: o AC4 bloqueou (exit!=0) a invariante vacuosa injectada (fail-closed, não-vacuoso)"
fi

# ============================================================================
# K) egress desbloqueado torna o gate de enforcement do ápice (AOS-161) vermelho
# ============================================================================
log_gate "self-test K · egress desbloqueado bloqueia o enforcement do ápice (AOS-161)"
# O guard-test do ápice (packages/integration) tem um teste-veneno
# (TestSelftestApexEnforcementBypassReddensGate) que só corre com AOS_APEX_SELFTEST=1:
# reproduz o cenário (d) — egress a um destino fora da allowlist — mas com o hook de egress
# real substituído pelo EgressStub neutro (default-deny AOS-067 CONTORNADO, via a via crua que
# a costura estrita NewProductionSecure rejeitaria) e ASSEVERA (falsamente) que a acção foi
# negada por egress; como o stub a admite, a asserção FALHA de propósito, provando que um
# egress desbloqueado torna o enforcement do ápice VERMELHO (fail-closed). Determinista,
# offline e sem rasto no repo (nenhum ficheiro é alterado).
if ( cd "$REPO_ROOT/packages/integration" && \
     AOS_APEX_SELFTEST=1 go test ./ -run TestSelftestApexEnforcementBypassReddensGate -count=1 ) >/dev/null 2>&1; then
  bad "K: o ápice admitiu egress a um destino fora da allowlist — o default-deny AOS-067 NÃO bloquearia"
else
  pass "K: o ápice bloqueou (exit!=0) o egress desbloqueado injectado (enforcement fail-closed)"
fi

# ============================================================================
# L) inversão de fronteira canónica bloqueia o gate layer-lint (AOS-178)
# ============================================================================
log_gate "self-test L · inversão de camada bloqueia o layer-lint"
# Cria uma árvore temporária fora do repo com uma inversão sintética:
# platform/fake importa kernel/fake, o que viola control-plane → kernel → platform/substrate.
LAYER_TMP="$(mktemp -d)"
mkdir -p "$LAYER_TMP/packages/kernel/fake" "$LAYER_TMP/packages/platform/fake"
cat > "$LAYER_TMP/packages/kernel/fake/go.mod" <<'EOF'
module github.com/aos-ref/kernel/fake

go 1.24
EOF
cat > "$LAYER_TMP/packages/kernel/fake/fake.go" <<'EOF'
package fake
EOF
cat > "$LAYER_TMP/packages/platform/fake/go.mod" <<'EOF'
module github.com/aos-ref/platform/fake

go 1.24

replace github.com/aos-ref/kernel/fake => ../kernel/fake
EOF
cat > "$LAYER_TMP/packages/platform/fake/fake.go" <<'EOF'
package fake

import _ "github.com/aos-ref/kernel/fake"
EOF

if bash "$CI_DIR/layer-lint.sh" --root "$LAYER_TMP" >/dev/null 2>&1; then
  bad "L: layer-lint passou com inversão sintética platform -> kernel — gate NÃO bloqueou"
else
  pass "L: layer-lint bloqueou (exit!=0) a inversão sintética platform -> kernel"
fi
rm -rf "$LAYER_TMP"
LAYER_TMP=""

# ============================================================================
# M) Coerência das listas de required checks (AOS-190)
#
# A lista de checks obrigatórios existe em três sítios e TEM de ser a mesma:
#   1. `needs:` do job agregador `gates` em .github/workflows/ci.yml (a real);
#   2. o comentário `REQUIRED-CHECKS:` do cabeçalho do mesmo ficheiro;
#   3. a linha `REQUIRED-CHECKS:` de CONTRIBUTING.md.
# Se divergirem, um mantenedor configura menos checks do que os que existem e
# gates que falham deixam de bloquear. Comparação por sequência exacta (a ordem
# também importa: é ela que torna a revisão humana viável).
# ============================================================================
log_gate "self-test M · listas de required checks coerentes com o needs: do agregador"

CI_YML="$REPO_ROOT/.github/workflows/ci.yml"
CONTRIB="$REPO_ROOT/CONTRIBUTING.md"

# 1. needs: do agregador -> "a b c"
needs_list="$(grep -m1 -E '^[[:space:]]*needs:[[:space:]]*\[secrets,' "$CI_YML" \
  | sed -E 's/^[^[]*\[//; s/\].*$//; s/,/ /g' | tr -s ' ' | sed -E 's/^ +| +$//g')"
# 2/3. linhas REQUIRED-CHECKS: (separadas por " · ") -> "a b c"
# O marcador TEM de estar no início da linha (opcionalmente após "# " em YAML):
# sem a âncora, uma menção em prosa a `REQUIRED-CHECKS:` no texto explicativo é
# apanhada primeiro pelo `-m1` e a comparação passa a ser feita contra prosa.
extract_required() {
  grep -m1 -E '^(# )?REQUIRED-CHECKS:' "$1" \
    | sed -E 's/^(# )?REQUIRED-CHECKS:[[:space:]]*//; s/`//g' \
    | sed -E 's/ · / /g' | tr -s ' ' | sed -E 's/^ +| +$//g'
}
yml_list="$(extract_required "$CI_YML")"
doc_list="$(extract_required "$CONTRIB")"

if [ -z "$needs_list" ]; then
  bad "M: não encontrei o \`needs:\` do agregador \`gates\` em .github/workflows/ci.yml"
elif [ -z "$yml_list" ]; then
  bad "M: não encontrei a linha REQUIRED-CHECKS: em .github/workflows/ci.yml"
elif [ -z "$doc_list" ]; then
  bad "M: não encontrei a linha REQUIRED-CHECKS: em CONTRIBUTING.md"
elif [ "$needs_list" != "$yml_list" ]; then
  bad "M: REQUIRED-CHECKS de ci.yml diverge do needs: do agregador
       needs: $needs_list
       ci.yml: $yml_list"
elif [ "$needs_list" != "$doc_list" ]; then
  bad "M: REQUIRED-CHECKS de CONTRIBUTING.md diverge do needs: do agregador
       needs:        $needs_list
       CONTRIBUTING: $doc_list"
else
  n_checks="$(printf '%s' "$needs_list" | wc -w | tr -d ' ')"
  pass "M: as 3 listas de required checks coincidem ($n_checks checks, mesma ordem)"
fi

# O agregador só é substituto legítimo da lista completa se for fail-closed:
# sem `if: always()` seria SALTADO (conclusão `skipped` = passagem para a branch
# protection) em vez de vermelho, e os gates deixariam de bloquear de facto.
if grep -A2 -E '^  gates:' "$CI_YML" | grep -q 'if: always()'; then
  pass "M: agregador \`gates\` tem \`if: always()\` (não pode ficar \`skipped\` em vez de vermelho)"
else
  bad "M: agregador \`gates\` sem \`if: always()\` — ficaria \`skipped\` (= verde) quando um gate falha"
fi
if grep -q "needs.\*.result" "$CI_YML"; then
  pass "M: agregador \`gates\` avalia \`needs.*.result\` (falha se algum gate não ficar success)"
else
  bad "M: agregador \`gates\` não avalia \`needs.*.result\` — passaria verde com gates vermelhos"
fi

# ============================================================================
# N) Divergência de contrato de porta bloqueia o gate 4 «Integração» (AOS-198)
#
# Ao contrário do §L, estes subtestes correm contra o CORPUS REAL, e cada um
# ataca uma via distinta de falso-verde:
#   N1 baseline VAZIA        — as divergências C3/C4/C5 são mesmo detectadas;
#   N2 baseline OBSOLETA     — a dívida fechada tem de ser removida;
#   N3 baseline SEM `owner=` — a regra de honestidade é executável;
#   N4 parágrafo RENOMEADO   — o gate não se desliga editando o documento;
#   N5 baseline ÓRFÃ         — uma entrada nunca visitada não se torna permanente.
# N4 e N5 são as duas vias pelas quais o gate ficava VERDE sobre menos contratos
# do que dizia verificar (medido pela auditoria de AOS-198).
# ============================================================================
log_gate "self-test N · divergência de contrato bloqueia o gate 4 (AOS-198)"
N_EMPTY="$(mktemp)"
: > "$N_EMPTY"
if AOS_CONTRACT_BASELINE="$N_EMPTY" bash "$CI_DIR/integration.sh" >/dev/null 2>&1; then
  bad "N1: gate 4 passou com baseline VAZIA — as divergências C3/C4/C5 não estão a ser detectadas"
else
  pass "N1: gate 4 bloqueou (exit!=0) com baseline vazia — detecta as divergências reais de contrato"
fi
# N2: entrada de baseline para um código que EXISTE (C1) tem de falhar como obsoleta.
N_STALE="$(mktemp)"
printf 'C1|E_POLICY_UNAVAILABLE|packages/control-plane/pdp # owner=selftest; entrada propositadamente obsoleta\n' > "$N_STALE"
cat "$CI_DIR/baseline/contract-codes.txt" >> "$N_STALE"
if AOS_CONTRACT_BASELINE="$N_STALE" bash "$CI_DIR/integration.sh" >/dev/null 2>&1; then
  bad "N2: gate 4 passou com entrada de baseline OBSOLETA — a baseline pode crescer sem custo"
else
  pass "N2: gate 4 bloqueou (exit!=0) uma entrada de baseline obsoleta — a baseline só encolhe"
fi
# N3: baseline sem dono declarado tem de falhar (a regra de honestidade é executável).
N_NOOWNER="$(mktemp)"
printf 'C3|E_NO_DECISION|packages/platform/broker # sem dono declarado\n' > "$N_NOOWNER"
if AOS_CONTRACT_BASELINE="$N_NOOWNER" bash "$CI_DIR/integration.sh" >/dev/null 2>&1; then
  bad "N3: gate 4 aceitou uma entrada de baseline sem \`owner=\`"
else
  pass "N3: gate 4 recusou (exit!=0) uma entrada de baseline sem \`owner=\`"
fi

# N4: RENOMEAR o marcador do parágrafo «Semântica de erro» de um contrato NÃO pode
# desligar o gate para esse contrato. Antes de AOS-198 (revisão da auditoria) o
# gate ficava VERDE, a dívida reconhecida caía de 10 para 6 entradas, e a string
# do contrato desaparecia por completo da saída — sem um único aviso.
N_DOC="$(mktemp)"
# O interpretador e uma DEPENDENCIA do gate: sem ele o vermelho seria por falta de
# ferramenta e nao por defeito. Ver [ensure_python] em lib.sh.
ensure_python || exit 1
python3 - "$REPO_ROOT/tecnica/12_Contratos_de_Interface.md" "$N_DOC" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
# Renomeia SÓ o último parágrafo (contrato C5), como na prova da auditoria.
marker = "**Semântica de erro.**"
i = text.rfind(marker)
assert i != -1, "marcador «Semântica de erro» não encontrado — self-test inválido"
open(dst, "w", encoding="utf-8").write(text[:i] + "**Erros da porta.**" + text[i + len(marker):])
PY
if AOS_CONTRACTS_DOC="$N_DOC" bash "$CI_DIR/integration.sh" >/dev/null 2>&1; then
  bad "N4: gate 4 passou com o parágrafo «Semântica de erro» de um contrato RENOMEADO — o gate desliga-se em silêncio editando o documento que guarda"
else
  pass "N4: gate 4 bloqueou (exit!=0) um contrato sem códigos extraíveis (parse fail-closed)"
fi

# N5: entrada de baseline ÓRFÃ — um par contrato|código que não existe no
# documento. Nunca seria visitada pelo laço de verificação, logo passaria
# despercebida para sempre; é o caminho pelo qual a baseline deixaria de «só
# encolher» sem que nada o dissesse.
N_ORPHAN="$(mktemp)"
cat "$CI_DIR/baseline/contract-codes.txt" > "$N_ORPHAN"
printf 'C9|E_FANTASMA|packages/nao/existe # owner=selftest; contrato inexistente, entrada propositadamente orfa\n' >> "$N_ORPHAN"
if AOS_CONTRACT_BASELINE="$N_ORPHAN" bash "$CI_DIR/integration.sh" >/dev/null 2>&1; then
  bad "N5: gate 4 aceitou uma entrada de baseline ÓRFÃ (contrato/código inexistente) — a baseline pode crescer sem custo"
else
  pass "N5: gate 4 bloqueou (exit!=0) uma entrada de baseline órfã"
fi
rm -f "$N_EMPTY" "$N_STALE" "$N_NOOWNER" "$N_DOC" "$N_ORPHAN"

# ============================================================================
# O) Literal/concatenação de tipo de evento bloqueia o gate event-catalog (AOS-198)
# ============================================================================
log_gate "self-test O · literal de tipo de evento bloqueia o gate event-catalog (AOS-198)"
O_EMPTY="$(mktemp)"
: > "$O_EMPTY"
if AOS_EVENT_BASELINE="$O_EMPTY" bash "$CI_DIR/event-catalog.sh" >/dev/null 2>&1; then
  bad "O1: event-catalog passou com baseline VAZIA — os literais/concatenações reais não são detectados"
else
  pass "O1: event-catalog bloqueou (exit!=0) com baseline vazia — detecta os literais/concatenações reais"
fi
O_STALE="$(mktemp)"
printf 'literal-catalog|packages/nao/existe.go|turn.recorded # owner=selftest; entrada propositadamente obsoleta\n' > "$O_STALE"
cat "$CI_DIR/baseline/event-catalog.txt" >> "$O_STALE"
if AOS_EVENT_BASELINE="$O_STALE" bash "$CI_DIR/event-catalog.sh" >/dev/null 2>&1; then
  bad "O2: event-catalog passou com entrada de baseline OBSOLETA"
else
  pass "O2: event-catalog bloqueou (exit!=0) uma entrada de baseline obsoleta"
fi
rm -f "$O_EMPTY" "$O_STALE"

# O3 — E3 (a promessa de título do gate: «zero literais em EventInput.Type»).
# Injecta a evasão TRIVIAL medida pela auditoria: literal composto de UMA linha
# com `Type` a NÃO ser o primeiro campo. A versão anterior do parser exigia
# `Type:` no início de linha e ficava VERDE. Corre contra a baseline REAL: a
# única violação nova é a injectada.
rm -rf "$EVENT_PROBE"; mkdir -p "$EVENT_PROBE"
cat > "$EVENT_PROBE/probe_literal.go" <<'EOF'
package selftesteventcat

// Sonda do self-test §O3 (AOS-198). Struct homónimo de propósito: o gate procura
// literais compostos `EventInput{…}` por texto, sem resolver tipos.
type EventInput struct {
	StreamID string
	Type     string
}

func sondaLiteralNaoPrimeiroCampo() EventInput {
	return EventInput{StreamID: "s", Type: "turn.selftest_injectado"}
}
EOF
if bash "$CI_DIR/event-catalog.sh" >/dev/null 2>&1; then
  bad "O3: event-catalog passou com um literal em \`EventInput.Type\` numa só linha (Type não-primeiro) — a promessa «zero literais» é evadível por reformatação"
else
  pass "O3: event-catalog bloqueou (exit!=0) o literal em \`EventInput.Type\` com Type não-primeiro numa só linha"
fi
rm -rf "$EVENT_PROBE"

# O4 — E2 (família fora da taxonomia de tecnica/13 §3.3). É o mecanismo
# anti-reincidência da deriva de 80 entradas e não tinha prova negativa nenhuma:
# o corpus real só exercitava E4.
rm -rf "$EVENT_PROBE"; mkdir -p "$EVENT_PROBE"
cat > "$EVENT_PROBE/probe_familia.go" <<'EOF'
package selftesteventcat

// Sonda do self-test §O4 (AOS-198): constante de tipo de evento cuja FAMÍLIA não
// consta das tabelas (a)/(b) de tecnica/13 §3.3.
const EventSondaSelftest = "familianaodeclarada.sonda"
EOF
if bash "$CI_DIR/event-catalog.sh" >/dev/null 2>&1; then
  bad "O4: event-catalog passou com uma família de evento FORA da taxonomia de tecnica/13 §3.3 — a deriva doc↔código voltaria a ser invisível"
else
  pass "O4: event-catalog bloqueou (exit!=0) uma família de evento fora da taxonomia documentada"
fi
rm -rf "$EVENT_PROBE"

# O5 — controlo negativo: sem sondas, o gate volta ao verde contra a baseline
# committada. Sem isto, um O3/O4 «sempre vermelho» (p.ex. por a árvore ter ficado
# suja) passaria por prova válida.
if bash "$CI_DIR/event-catalog.sh" >/dev/null 2>&1; then
  pass "O5: controlo — sem sondas, o event-catalog volta ao verde (as sondas foram removidas sem rasto)"
else
  bad "O5: controlo falhou — o event-catalog ficou vermelho SEM sondas injectadas (rasto no repo ou violação nova)"
fi

# ============================================================================
# P) Título citado que pertence a OUTRO ticket é recusado (AOS-198, residual STR-01)
#
# Exercita DIRECTAMENTE o predicado que o ref-lint usa — o mesmo padrão do §I com
# `coverage_meets_min`. Os pares são os do STR-01 real: o título do controlo de
# taint pertence a AOS-069 (não a AOS-067), e o do audit tamper-evident pertence a
# AOS-072 (não a AOS-071). O subteste é NÃO-TAUTOLÓGICO: exige que o par CORRECTO
# case e que o par ERRADO não case.
# ============================================================================
log_gate "self-test P · título de outro ticket é recusado pelo ref-lint (AOS-198)"
if python3 - <<'PY'
import importlib.util, sys
spec = importlib.util.spec_from_file_location("reflint", "scripts/ci/ref-lint.py")
rl = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rl)
ref = rl.reference_title_tokens(rl.extract_backlog())
casos = [
    ("AOS-069", "Separacao control/data-plane + taint tracking (CaMeL)", True),
    ("AOS-067", "Separacao control/data-plane + taint tracking (CaMeL)", False),
    ("AOS-072", "Audit trail hash-chained tamper-evident", True),
    ("AOS-071", "Audit trail hash-chained tamper-evident", False),
]
erros = 0
for aos, titulo, esperado in casos:
    if aos not in ref or not ref[aos]:
        print(f"P: ticket {aos} sem titulo extraivel do backlog", file=sys.stderr)
        erros += 1
        continue
    obtido = rl.titles_agree(titulo, ref[aos])
    if obtido != esperado:
        print(f"P: {aos} esperado={esperado} obtido={obtido}", file=sys.stderr)
        erros += 1
sys.exit(1 if erros else 0)
PY
then
  pass "P1: o predicado título↔ticket aceita o par correcto e RECUSA o desvio do STR-01"
else
  bad "P1: o predicado título↔ticket não discrimina o desvio do STR-01 (falso-verde)"
fi

# P2 — prova PONTA-A-PONTA de que o GATE (não só o predicado) fica vermelho.
# O §P1 é um teste unitário: exercita `titles_agree()` com pares escritos à mão e
# não prova que existe input capaz de avermelhar o gate. Mediu-se que não existia:
# as 395 declarações verificadas vinham TODAS de `specs/EPIC-*.md`, que é também de
# onde os títulos de referência eram extraídos — a comparação era do texto consigo
# mesmo e nenhuma troca de título podia falhar. Este subteste copia o corpus,
# injecta o desvio EXACTO do STR-01 (o título de AOS-069 no cabeçalho de AOS-067)
# e exige VERMELHO. A árvore real NÃO é tocada — a mutação vive só na cópia.
log_gate "self-test P2 · troca de título dentro da própria EPIC avermelha o ref-lint (leave-one-out)"
REFLINT_TMP="$(mktemp -d)"
mkdir -p "$REFLINT_TMP/docs"
cp -r "$REPO_ROOT/specs" "$REFLINT_TMP/specs"
cp -r "$REPO_ROOT/tecnica" "$REFLINT_TMP/tecnica"
cp -r "$REPO_ROOT/docs/adr" "$REFLINT_TMP/docs/adr"

# Controlo positivo: a cópia intacta tem de ficar VERDE (senão o vermelho de
# baixo não prova nada — provaria só que a cópia está partida).
if AOS_REFLINT_ROOT="$REFLINT_TMP" python3 "$CI_DIR/ref-lint.py" >/dev/null 2>&1; then
  pass "P2: controlo — a cópia intacta do corpus fica verde"
else
  bad "P2: controlo falhou — a cópia intacta do corpus já está vermelha (o subteste não provaria nada)"
fi

if python3 - "$REFLINT_TMP" <<'PY'
import sys, glob, re
root = sys.argv[1]
alvo = glob.glob(root + "/specs/EPIC-07*.md")
if not alvo:
    print("P2: EPIC-07 nao encontrada na copia", file=sys.stderr); sys.exit(2)
path = alvo[0]
text = open(path, encoding="utf-8").read()
# Desvio exacto do STR-01: o titulo de AOS-069 colado no cabecalho de AOS-067.
novo, n = re.subn(
    r"(?m)^## AOS-067\s*[-–—].*$",
    "## AOS-067 — Separacao control/data-plane + taint tracking (CaMeL)",
    text,
)
if n != 1:
    print(f"P2: esperava 1 cabecalho de AOS-067, encontrei {n}", file=sys.stderr); sys.exit(2)
open(path, "w", encoding="utf-8").write(novo)
PY
then
  if AOS_REFLINT_ROOT="$REFLINT_TMP" python3 "$CI_DIR/ref-lint.py" >/dev/null 2>&1; then
    bad "P2: ref-lint passou com o título de AOS-069 no cabeçalho de AOS-067 — a verificação título↔ticket é tautológica"
  else
    pass "P2: ref-lint bloqueou (exit!=0) o desvio do STR-01 injectado na cópia do corpus"
  fi
else
  bad "P2: não foi possível injectar o desvio do STR-01 na cópia do corpus"
fi
rm -rf "$REFLINT_TMP"
REFLINT_TMP=""

# P3 — a árvore real não foi tocada por §P2.
if python3 "$CI_DIR/ref-lint.py" >/dev/null 2>&1; then
  pass "P3: controlo — o ref-lint continua verde contra a árvore REAL (sem rasto)"
else
  bad "P3: o ref-lint ficou vermelho contra a árvore real — POSSÍVEL RASTO no repo"
fi
# ============================================================================
# Q) o gate de ENTREGA bloqueia um smoke apontado a liveness (2026-08-23)
# ============================================================================
# O defeito real: `handleHealthz` devolve 200 INCONDICIONALMENTE, e as duas variaveis
# que decidiam a reversao automatica do deploy vinham as duas do /healthz. Um no que
# recusasse 100% dos pedidos era entregue VERDE. Aqui prova-se que o gate que fecha
# isso REJEITA a regressao, em vez de a afirmar por comentario.
log_gate "self-test Q · o gate de entrega bloqueia um smoke em /healthz"
DG_TMP="$(mktemp -d)"
mkdir -p "$DG_TMP/deploy/server" "$DG_TMP/.github/workflows"
cp "$REPO_ROOT/deploy/server/deploy.sh"        "$DG_TMP/deploy/server/deploy.sh"
cp "$REPO_ROOT/.github/workflows/deploy.yml"   "$DG_TMP/.github/workflows/deploy.yml"

# Q1 — a REGRESSAO: o smoke volta a sondar liveness.
sed -i 's|EDGE_PORT}/readyz"|EDGE_PORT}/healthz"|g' "$DG_TMP/deploy/server/deploy.sh"
if bash "$CI_DIR/deploy-gate-lint.sh" --root "$DG_TMP" >/dev/null 2>&1; then
  bad "Q1: o gate passou com o smoke do deploy.sh apontado a /healthz — NAO bloqueou a regressao"
else
  pass "Q1: o gate bloqueou o smoke do deploy.sh apontado a /healthz"
fi

# Q2 — a ATRIBUICAO removida: fica so o smoke, sem a linha de base. Sem ela uma avaria
# ANTERIOR reverte uma entrega que nao a causou, e a reversao nao a resolve.
cp "$REPO_ROOT/deploy/server/deploy.sh" "$DG_TMP/deploy/server/deploy.sh"
perl -0pi -e 's/EDGE_PORT\}\/readyz"/EDGE_PORT}\/healthzz"/' "$DG_TMP/deploy/server/deploy.sh"
if bash "$CI_DIR/deploy-gate-lint.sh" --root "$DG_TMP" >/dev/null 2>&1; then
  bad "Q2: o gate passou com UMA so sonda /readyz no deploy.sh — a linha de base pode desaparecer sem ninguem dar por isso"
else
  pass "Q2: o gate bloqueou o deploy.sh com uma so sonda /readyz (linha de base em falta)"
fi

# Q3 — o cheque de ALCANCE do workflow revertido.
cp "$REPO_ROOT/deploy/server/deploy.sh"      "$DG_TMP/deploy/server/deploy.sh"
sed -i 's|//\$HOST:\$PORT/readyz|//$HOST:$PORT/healthz|g' "$DG_TMP/.github/workflows/deploy.yml"
if bash "$CI_DIR/deploy-gate-lint.sh" --root "$DG_TMP" >/dev/null 2>&1; then
  bad "Q3: o gate passou com o cheque de ALCANCE do workflow em /healthz"
else
  pass "Q3: o gate bloqueou o cheque de ALCANCE do workflow em /healthz"
fi
rm -rf "$DG_TMP"

# Q4 — CONTROLO POSITIVO (o molde do P3): depois de tres mutacoes, a arvore REAL tem de
# continuar verde. Sem esta linha, um gate que rejeitasse TUDO passaria Q1..Q3 e o
# selftest estaria a medir "o gate diz sempre que nao" em vez de "o gate distingue".
if bash "$CI_DIR/deploy-gate-lint.sh" >/dev/null 2>&1; then
  pass "Q4: controlo — o gate de entrega continua verde contra a arvore REAL (distingue, nao rejeita tudo)"
else
  bad "Q4: o gate de entrega ficou vermelho contra a arvore real — POSSIVEL RASTO no repo"
fi


# ============================================================================
# R) o gate rtm bloqueia uma §6 que atribui um ticket ao epic errado
# ============================================================================
# O defeito real: `rtm-regenerate.py` escrevia «o último epic do backlog» onde a
# linha afirma o epic de um ticket CONCRETO. A linha do STRIDE dizia EPIC-21, e
# passou a dizer EPIC-22 ao ser regenerada — e AOS-194 sempre viveu na EPIC-18.
# Nada comparava o que a máquina escrevia com a fonte: é o meta-achado de
# `analises/10` §5 agravado por o autor ser automático. Aqui prova-se que a
# asserção `validate_section6` RECUSA a atribuição falsa, em vez de a afirmar
# por comentário.
log_gate "self-test R · o gate rtm bloqueia atribuição ticket→epic falsa na §6"

# Sandbox de §R/§S: corpus copiado + cópia do gerador. Nada aqui toca na árvore
# real; o gerador é apontado à cópia por `AOS_RTM_ROOT` (AOS-316).
RTM_SANDBOX="$(mktemp -d)"
mkdir -p "$RTM_SANDBOX/root/docs"
cp -r "$REPO_ROOT/specs"    "$RTM_SANDBOX/root/specs"
cp -r "$REPO_ROOT/tecnica"  "$RTM_SANDBOX/root/tecnica"
cp -r "$REPO_ROOT/docs/adr" "$RTM_SANDBOX/root/docs/adr"
cp    "$REPO_ROOT/_BRIEF.md" "$RTM_SANDBOX/root/_BRIEF.md"
RTM_GEN="$RTM_SANDBOX/gen.py"
RTM_GEN_BAK="$RTM_SANDBOX/gen.py.bak"
cp "$CI_DIR/rtm-regenerate.py" "$RTM_GEN"
cp "$RTM_GEN" "$RTM_GEN_BAK"

# Vermelho PELO motivo certo: `--check` também falha por simples divergência de
# texto, e isso provaria outra coisa. Exige-se a mensagem da asserção.
# `set -o pipefail` está activo: encadear directamente em `grep` devolveria o
# exit!=0 do gerador e a prova diria sempre «bloqueou». A saída é capturada
# primeiro e só depois inspeccionada.
rtm_bloqueou_pela_asercao() {
  local out rc
  out="$(AOS_RTM_ROOT="$RTM_SANDBOX/root" python3 "$RTM_GEN" --check 2>&1)" && rc=0 || rc=$?
  [ "$rc" -ne 0 ] || return 1
  printf '%s' "$out" | grep -q 'atribui tickets a epics'
}

# R1 — o par explícito `EPIC-NN/AOS-194` apontado ao epic errado.
cp "$RTM_GEN_BAK" "$RTM_GEN"
perl -0pi -e 's/stride_epic = epic_of\(tickets, "AOS-194"\)/stride_epic = "EPIC-01"/' "$RTM_GEN"
if rtm_bloqueou_pela_asercao; then
  pass "R1: o gate bloqueou a §6 que atribuía AOS-194 a um epic que não o contém"
else
  bad "R1: o gate não bloqueou (ou falhou por outro motivo) com AOS-194 atribuído à EPIC-01"
fi

# R2 — a REGRESSÃO literal: voltar a usar «o último epic» para a gama de remediação.
cp "$RTM_GEN_BAK" "$RTM_GEN"
perl -0pi -e 's/rem_epics = epics_covering\(tickets, rem_low, stats\["max_aos"\]\)/rem_epics = [last_epic]/' "$RTM_GEN"
if rtm_bloqueou_pela_asercao; then
  pass "R2: o gate bloqueou o regresso de last_epic como epic de tickets concretos"
else
  bad "R2: o gate passou com a gama AOS-190→ atribuída só ao último epic — a classe de defeito volta com a próxima epic"
fi

# R3 — CONTROLO POSITIVO (o molde do P3/Q4): restaurada a árvore, o gate volta a
# verde. Sem isto, um validador que rejeitasse TUDO passaria R1/R2 e estaríamos a
# medir «diz sempre que não» em vez de «distingue».
if python3 "$CI_DIR/rtm-regenerate.py" --check >/dev/null 2>&1; then
  pass "R3: controlo — o gate rtm continua verde contra a árvore REAL (sem rasto)"
else
  bad "R3: o gate rtm ficou vermelho contra a árvore real — POSSÍVEL RASTO no repo"
fi


# ============================================================================
# S) o gate rtm bloqueia uma §7 que cita o que não existe
# ============================================================================
# O defeito real: a §7 fechava com «20/20 ADRs e 12/12 NFRs» enquanto as §§4 e 5,
# geradas, tinham 19 e 10 linhas e declaravam 19/19 e 10/10 — no mesmo ficheiro, a
# setenta linhas. Era a unica seccao da RTM fora da regeneracao E fora do ref-lint:
# ninguem a lia. Agora e gerada, e `validate_section7` confronta com o corpus tudo o
# que ela cite. Aqui prova-se que RECUSA uma citacao inventada.
log_gate "self-test S · o gate rtm bloqueia uma citação inexistente na §7"

# O padrão tem de ser ASCII PURO. O Python escreve stderr na codificação da
# consola (cp1252 em Windows), e um 'não' vindo deste ficheiro UTF-8 nunca casa.
rtm_bloqueou_pela_asercao7() {
  local out rc
  out="$(AOS_RTM_ROOT="$RTM_SANDBOX/root" python3 "$RTM_GEN" --check 2>&1)" && rc=0 || rc=$?
  [ "$rc" -ne 0 ] || return 1
  printf '%s' "$out" | grep -q 'cita entidades que'
}

# S1 — um ticket que nao existe, citado por uma lacuna.
cp "$RTM_GEN_BAK" "$RTM_GEN"
perl -0pi -e 's/GAP04_STEER = \[/GAP04_STEER = ["AOS-999", /' "$RTM_GEN"
if rtm_bloqueou_pela_asercao7; then
  pass "S1: o gate bloqueou a §7 que citava um ticket inexistente"
else
  bad "S1: o gate não bloqueou (ou falhou por outro motivo) com AOS-999 citado na §7"
fi

# S2 — um NFR fora de NFR_SPECS. E o molde exacto do defeito historico: a §7
# afirmava 12/12 NFRs quando NFR_SPECS tem dez, e NFR-11/NFR-12 nao existem para o
# gerador. Se alguem voltar a nomea-los na §7 sem os por na fonte, fica vermelho.
cp "$RTM_GEN_BAK" "$RTM_GEN"
perl -0pi -e 's/"evidencia": "§3, §5",/"evidencia": "§3, §5 (NFR-11)",/' "$RTM_GEN"
if rtm_bloqueou_pela_asercao7; then
  pass "S2: o gate bloqueou a §7 que nomeava NFR-11, ausente de NFR_SPECS"
else
  bad "S2: o gate passou com NFR-11 na §7 — o defeito histórico de 12/12 podia voltar"
fi

# S3 — CONTROLO POSITIVO: restaurada a arvore, o gate volta a verde.
if python3 "$CI_DIR/rtm-regenerate.py" --check >/dev/null 2>&1; then
  pass "S3: controlo — o gate rtm continua verde contra a árvore REAL (sem rasto)"
else
  bad "S3: o gate rtm ficou vermelho contra a árvore real — POSSÍVEL RASTO no repo"
fi

# S4 — a RTM deixou de estar fora do ref-lint. Uma referencia partida DENTRO da RTM
# tem de avermelhar o gate; antes de AOS-313 era a unica do corpus que ninguem lia.
RTM_TMP="$(mktemp -d)"
mkdir -p "$RTM_TMP/docs"
cp -r "$REPO_ROOT/specs"    "$RTM_TMP/specs"
cp -r "$REPO_ROOT/tecnica"  "$RTM_TMP/tecnica"
cp -r "$REPO_ROOT/docs/adr" "$RTM_TMP/docs/adr"
perl -0pi -e 's/AOS-001 – AOS-/AOS-998 – AOS-/' "$RTM_TMP/tecnica/16_Rastreabilidade_RTM.md"
if AOS_REFLINT_ROOT="$RTM_TMP" python3 "$CI_DIR/ref-lint.py" >/dev/null 2>&1; then
  bad "S4: o ref-lint passou com AOS-998 citado na RTM — a RTM continua fora do gate"
else
  pass "S4: o ref-lint bloqueou uma referência partida DENTRO da RTM (fim do skip)"
fi
rm -rf "$RTM_TMP"

# S5 — controlo positivo do §S4, no molde do P3.
if python3 "$CI_DIR/ref-lint.py" >/dev/null 2>&1; then
  pass "S5: controlo — o ref-lint continua verde contra a árvore REAL (sem rasto)"
else
  bad "S5: o ref-lint ficou vermelho contra a árvore real — POSSÍVEL RASTO no repo"
fi


# ============================================================================
# T) a suite recusa correr concorrente consigo própria (AOS-316)
# ============================================================================
# O defeito real: três runs sobrepostos, cada um com o seu backup tirado num
# instante diferente. Um deles tinha o backup de `rtm-regenerate.py` de um commit
# anterior e, ao chegar a §R, repô-lo por cima de edições em curso — saiu um
# commit cuja mensagem descrevia uma mudança e cujo conteúdo revertia outra, e
# nenhum gate deu por isso. Aqui prova-se que a exclusão RECUSA, que um lock
# órfão não deixa a suite inarrancável, e que §R/§S deixaram de tocar no original.
log_gate "self-test T · exclusão mútua e guarda de árvore limpa"
# ATENÇÃO ao errexit: `lib.sh:13` faz `set -euo pipefail`, pelo que uma captura
# simples — `x="$(cmd)"; rc=$?` — de um comando que sai != 0 MATA a suite com o
# código dele, e nunca chega ao `rc`. Aqui isso dava um run que morria em §T com
# exit 3 e sem uma única linha de diagnóstico. Daí o `|| t_rc=$?`, que torna a
# atribuição um comando composto e desarma o errexit.
T_TMP="$(mktemp -d)"

# T1 — lock de um processo VIVO (este). A segunda invocação tem de recusar, com
# código próprio e mensagem que nomeie a concorrência — nunca «POSSÍVEL RASTO no
# repo», que mandava investigar o sítio errado.
mkdir -p "$T_TMP/vivo"; printf '%s' "$$" > "$T_TMP/vivo/pid"
t_rc=0
t_out="$(AOS_SELFTEST_LOCK="$T_TMP/vivo" AOS_SELFTEST_LOCK_PROBE=1 bash "$CI_DIR/selftest.sh" 2>&1)" || t_rc=$?
if [ "$t_rc" -eq 3 ] && printf '%s' "$t_out" | grep -q 'outro selftest.sh'; then
  pass "T1: recusou arrancar com outro run vivo a deter o lock (exit 3)"
else
  bad "T1: esperava exit 3 e mensagem de concorrência; veio exit $t_rc"
fi

# T2 — lock ÓRFÃO. O `trap` não cobre KILL; um lock eterno tornaria a suite
# inarrancável, o que trocaria um defeito por outro. O PID usado é o de um
# processo que JÁ TERMINOU, não um número inventado que pudesse estar vivo.
( : ) & t_morto=$!; wait "$t_morto" 2>/dev/null || true
mkdir -p "$T_TMP/orfao"; printf '%s' "$t_morto" > "$T_TMP/orfao/pid"
t_rc=0
t_out="$(AOS_SELFTEST_LOCK="$T_TMP/orfao" AOS_SELFTEST_LOCK_PROBE=1 bash "$CI_DIR/selftest.sh" 2>&1)" || t_rc=$?
if [ "$t_rc" -eq 0 ] && printf '%s' "$t_out" | grep -q 'LOCK OK'; then
  pass "T2: tomou o lock órfão de um processo morto (não fica inarrancável)"
else
  bad "T2: esperava tomar o lock órfão; veio exit $t_rc"
fi

# T3 — a guarda de árvore limpa. Resíduo de um run morto sem trap tem de FAZER
# RECUSAR: sem isto o backup de arranque sairia de uma árvore já corrompida.
mkdir -p "$REPO_ROOT/packages/_selftest_bad"
t_rc=0
t_out="$(AOS_SELFTEST_LOCK="$T_TMP/guarda" AOS_SELFTEST_LOCK_PROBE=1 bash "$CI_DIR/selftest.sh" 2>&1)" || t_rc=$?
rm -rf "$REPO_ROOT/packages/_selftest_bad"
if [ "$t_rc" -eq 4 ] && printf '%s' "$t_out" | grep -q '_selftest_bad'; then
  pass "T3: recusou arrancar com resíduo de um run anterior, nomeando-o (exit 4)"
else
  bad "T3: esperava exit 4 por resíduo; veio exit $t_rc"
fi

# T4 — CONTROLO POSITIVO (molde de §P3/§Q4/§R3): sem lock e sem resíduo, arranca.
# Sem esta linha, uma guarda que recusasse SEMPRE passaria T1..T3.
t_rc=0
t_out="$(AOS_SELFTEST_LOCK="$T_TMP/livre" AOS_SELFTEST_LOCK_PROBE=1 bash "$CI_DIR/selftest.sh" 2>&1)" || t_rc=$?
if [ "$t_rc" -eq 0 ]; then
  pass "T4: controlo — sem lock nem resíduo a suite arranca (distingue, não recusa tudo)"
else
  bad "T4: a suite recusou arrancar com a árvore limpa e sem lock (exit $t_rc)"
fi

# T5 — a superfície encolheu: §R e §S correram ACIMA e o gerador da RTM não pode
# ter ficado modificado. É a prova de que passaram a mutar uma cópia via
# `AOS_RTM_ROOT`, e o teste que fica de guarda caso alguém reverta isso.
if [ "$(git -C "$REPO_ROOT" hash-object "$CI_DIR/rtm-regenerate.py")" = "$RTM_GEN_SHA_INICIO" ]; then
  pass "T5: §R e §S correram sem tocar em scripts/ci/rtm-regenerate.py (mutação em cópia)"
else
  bad "T5: o gerador da RTM mudou durante o run — §R/§S voltaram a mutar a árvore real"
fi

rm -rf "$T_TMP"


# ============================================================================
printf '\n%s============ RESUMO DOS SELF-TESTS ============%s\n' "$C_BLD" "$C_RST"
if [ "$fails" -eq 0 ]; then
  printf '%s  TODOS OS SELF-TESTS OK — falhas são bloqueadas pelos gates%s\n' "$C_GRN$C_BLD" "$C_RST"
else
  printf '%s  SELF-TESTS VERMELHOS — um gate não bloqueou como devia%s\n' "$C_RED$C_BLD" "$C_RST"
fi
exit "$fails"
