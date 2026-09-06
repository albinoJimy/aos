#!/usr/bin/env bash
# isolation-live.sh — GATE OPCIONAL de ISOLAMENTO CONTRA O EXECUTOR REAL (AOS-358).
#
# ─── O QUE ESTE GATE PROVA ───────────────────────────────────────────────────────────────────
# Conduz a cadeia canónica do nó
#     MediatedLauncher -> RM(permit) -> GVisorDriver -> GuestExecutor(HTTP) -> componente -> runsc
# contra o EXECUTOR REAL (deploy/server/gvisor/component, que monta um bundle OCI efémero por
# chamada e corre o guest num sandbox gVisor), e mede, na FRONTEIRA e não no contrato:
#   P1 EXECUÇÃO REAL      — a tool call chega mesmo ao sandbox: devolve conteúdo que SÓ existe
#                           na raiz semeada read-only, e o resultado é untrusted POR TIPO;
#   P2 FRONTEIRA FECHADA  — um caminho que EXISTE na imagem do componente e NÃO existe dentro do
#                           sandbox é inalcançável, por travessia relativa e em forma absoluta;
#   P3 DETECÇÃO NÃO-VÁCUA — o MESMO shape de executor, SEM sandbox, ALCANÇA o caminho. Sem esta
#                           contraprova, «recusado» seria indistinguível de «nunca existiu».
#
# ─── O QUE ESTE GATE NÃO PROVA (e é preciso lê-lo) ───────────────────────────────────────────
#   N1 NÃO é a fronteira do Firecracker. O gVisor interpõe syscalls em USER-SPACE (systrap); a
#      microVM tem virtualização de HARDWARE (ADR-004) e é mais forte. O que se ganha é que o
#      gVisor NÃO exige /dev/kvm — por isso é exercitável num runner partilhado. A prova sobre
#      microVM continua MANUAL e documentada em deploy/node/dev-hardened/firecracker/README.md
#      (`go test -tags fclive -run TestFCLive_RealMicroVM`, com FC_ORCH_URL e /dev/kvm).
#   N2 NÃO substitui o gate `security` (AOS-075). Aquele prova o CONTRATO da fronteira — overlay
#      não persiste, seccomp default-deny, sem socket do host — sobre `sandbox.NewFakeDriver()`,
#      um jail IN-PROCESS cuja fronteira é o processo do nó. Prova-o com fidelidade demonstrada
#      (treze meta-testes `TestMetaDetects_*` + selftest §H). CONTRATO != FRONTEIRA: são
#      complementares, e nenhum dos dois torna o outro dispensável.
#   N3 Destes três, o que aqui NÃO é medido contra o executor real: a não-persistência do
#      overlay (o guest do componente só expõe o verbo `read`, não há como escrever), a
#      allowlist seccomp e a ausência de socket do host. Ficam nomeados no campo `not_proved`
#      do relatório AOS_ISOLATION_LIVE_REPORT, para que a lacuna esteja no log e não na memória.
#
# ─── PORQUE É OPCIONAL, E PORQUE NÃO É INERTE ────────────────────────────────────────────────
# O componente precisa de docker com `--privileged` (o runsc cria namespaces e monta o bundle) e
# de um host Linux; um runner partilhado pode não o ter. Por isso NÃO está nos required checks:
# corre-se por `bash scripts/ci/isolation-live.sh` (ou `make ci-isolation-live`).
# MAS o caminho de salto NÃO é vazio — sem componente o gate continua a:
#   (a) COMPILAR a suite `-tags gvlive` e exigir que CADA cenário obrigatório exista por NOME
#       (uma suite dormente que deixasse de compilar, ou um cenário renomeado, ficam VERMELHOS);
#   (b) CORRER o meta-teste P3, que não precisa do componente;
#   (c) DECLARAR o salto de forma RUIDOSA (gate_skip/gate_skip_report/gate_skip_file, AOS-199),
#       nomeando a garantia que ficou POR VERIFICAR.
# Um salto silencioso é o defeito que este ticket fecha: `AOS_ISOLATION_LIVE_REQUIRED=1` torna o
# salto VERMELHO, para quem quiser exigir a prova real (staging, host com docker privilegiado).
#
# ─── ACTIVAÇÃO ───────────────────────────────────────────────────────────────────────────────
#   AOS_SANDBOX_GVISOR_URL=http://127.0.0.1:9101/exec bash scripts/ci/isolation-live.sh
#       liga-se a um componente JÁ a correr (a MESMA env var que o nó lê — o caminho é o de
#       produção, não um paralelo);
#   AOS_ISOLATION_LIVE=1 bash scripts/ci/isolation-live.sh
#       levanta o componente por docker (build de deploy/server/gvisor/Dockerfile, run
#       --privileged com a raiz semeada em bind read-only) e derruba-o no fim;
#   sem nenhuma das duas ⇒ salto RUIDOSO + (a) + (b).
#
# Knobs: AOS_ISOLATION_LIVE_REQUIRED=1 (salto = vermelho), AOS_ISOLATION_LIVE_PORT (9101),
#        AOS_ISOLATION_LIVE_TIMEOUT (segundos à espera do /healthz, 120).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
setup_env

SEC_MOD="packages/security-tests"
SUITE_PKG="./..."
TAG="gvlive"

# Cenários que exigem o componente (o meta-teste NÃO, de propósito).
REQUIRED_LIVE=(
  TestIsolationLive_GVisorExecutaNoSandboxReal
  TestIsolationLive_GVisorFronteiraRecusaForaDaRaizSemeada
  TestIsolationLive_Report
)
# Cenário que mede o CONTRAFACTUAL e corre sempre, com ou sem componente.
REQUIRED_ALWAYS=(
  TestMetaDetectsLive_FugaAlcancavelSemSandbox
)
REQUIRED_ALL=("${REQUIRED_LIVE[@]}" "${REQUIRED_ALWAYS[@]}")

PORT="${AOS_ISOLATION_LIVE_PORT:-9101}"
WAIT_S="${AOS_ISOLATION_LIVE_TIMEOUT:-120}"
IMAGE="aos-gvisor:ci"
CONTAINER="aos-gvisor-ci-$$"
SKIP_FILE="$REPO_ROOT/coverage/isolation-live.skipped"

log_gate "isolation-live (AOS-358) · isolamento contra o EXECUTOR REAL (gVisor/runsc) — FRONTEIRA, não contrato"

# =============================================================================================
# (0) ANTI-APODRECIMENTO — corre SEMPRE, haja componente ou não.
#     Uma suite atrás de build tag é código que ninguém compila. Este passo compila-a e exige
#     que cada cenário exista POR NOME: renomear/remover um deles fica VERMELHO aqui, mesmo num
#     runner sem docker. É o que impede a dormência de virar apodrecimento.
# =============================================================================================
log_step "go vet -tags $TAG $SUITE_PKG (a suite dormente TEM de compilar)"
if ! ( cd "$REPO_ROOT/$SEC_MOD" && go vet -tags "$TAG" "$SUITE_PKG" ); then
  log_fail "a suite -tags $TAG não compila — dormência virou apodrecimento"
  exit 1
fi

log_step "go test -tags $TAG -list (cada cenário obrigatório existe por nome)"
listed="$( cd "$REPO_ROOT/$SEC_MOD" && go test -tags "$TAG" -list '^Test' "$SUITE_PKG" )"
faltam=0
for t in "${REQUIRED_ALL[@]}"; do
  if ! printf '%s\n' "$listed" | grep -qx "$t"; then
    log_fail "cenário OBRIGATÓRIO ausente da suite -tags $TAG (renomeado/removido?): $t"
    faltam=1
  fi
done
[ "$faltam" -eq 0 ] || exit 1
log_ok "suite -tags $TAG compila e declara os ${#REQUIRED_ALL[@]} cenários obrigatórios"

# =============================================================================================
# (1) RESOLUÇÃO DO COMPONENTE — anexar a um já a correr, levantar por docker, ou saltar RUIDOSO.
# =============================================================================================
GV_URL="${AOS_SANDBOX_GVISOR_URL:-}"
LEVANTADO_POR_NOS=0

derrubar() {
  if [ "$LEVANTADO_POR_NOS" -eq 1 ]; then
    log_step "a derrubar o componente gVisor ($CONTAINER)"
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  fi
}
trap derrubar EXIT

if [ -n "$GV_URL" ]; then
  log_ok "componente gVisor ANEXADO em $GV_URL (AOS_SANDBOX_GVISOR_URL)"
elif [ "${AOS_ISOLATION_LIVE:-0}" = "1" ]; then
  # Levantar exige Linux + docker + privileged. Recusamos cedo e por escrito em vez de falhar
  # a meio de um `docker build` de 200 MB.
  motivo=""
  [ "$(uname -s)" = "Linux" ] || motivo="host não-Linux ($(uname -s)): o runsc só corre em Linux"
  if [ -z "$motivo" ] && ! command -v docker >/dev/null 2>&1; then
    motivo="docker ausente do PATH"
  fi
  if [ -z "$motivo" ] && ! docker info >/dev/null 2>&1; then
    motivo="daemon docker inacessível"
  fi
  if [ -n "$motivo" ]; then
    log_fail "AOS_ISOLATION_LIVE=1 foi PEDIDO e o componente NÃO pode ser levantado: $motivo"
    log_fail "pediste a prova real; não a mascaramos com um salto. Usa AOS_SANDBOX_GVISOR_URL para anexar a um componente remoto, ou não peças AOS_ISOLATION_LIVE=1."
    exit 1
  fi

  log_step "docker build -f deploy/server/gvisor/Dockerfile -t $IMAGE (contexto: raiz do repositório)"
  if ! ( cd "$REPO_ROOT" && docker build -f deploy/server/gvisor/Dockerfile -t "$IMAGE" . ); then
    log_fail "build da imagem do componente gVisor falhou"
    exit 1
  fi

  # --privileged: o runsc cria namespaces (mount/pid/user) e monta o bundle. É a MESMA postura
  # do serviço `gvisor` de deploy/server/docker-compose.prod.yml, e a razão de o componente ser
  # um processo SEPARADO do nó — o nó nunca corre privilegiado.
  log_step "docker run --privileged (porta $PORT, raiz semeada em bind read-only)"
  if ! docker run -d --name "$CONTAINER" --privileged \
        -p "127.0.0.1:$PORT:9101" \
        -v "$REPO_ROOT/deploy/server/gvisor/seed:/seed:ro" \
        "$IMAGE" >/dev/null; then
    log_fail "arranque do componente gVisor falhou"
    exit 1
  fi
  LEVANTADO_POR_NOS=1

  log_step "à espera do /healthz (até ${WAIT_S}s)"
  pronto=0
  for _ in $(seq 1 "$WAIT_S"); do
    if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then pronto=1; break; fi
    if [ -z "$(docker ps -q -f "name=^${CONTAINER}\$")" ]; then
      log_fail "o componente morreu durante o arranque; logs:"
      docker logs "$CONTAINER" 2>&1 | tail -30 | sed 's/^/       /' >&2
      exit 1
    fi
    sleep 1
  done
  if [ "$pronto" -ne 1 ]; then
    log_fail "componente gVisor não ficou saudável em ${WAIT_S}s; logs:"
    docker logs "$CONTAINER" 2>&1 | tail -30 | sed 's/^/       /' >&2
    exit 1
  fi
  GV_URL="http://127.0.0.1:$PORT/exec"
  log_ok "componente gVisor LEVANTADO em $GV_URL"
fi

# =============================================================================================
# (2) CAMINHO DE SALTO — ruidoso, nomeado, e ainda assim NÃO vazio.
# =============================================================================================
if [ -z "$GV_URL" ]; then
  gate_skip "isolation-live · cenários P1/P2 contra o executor real (gVisor/runsc)" \
    "componente gVisor ausente (nem AOS_SANDBOX_GVISOR_URL nem AOS_ISOLATION_LIVE=1)" \
    "ADR-004/ADR-007 na FRONTEIRA: que o sandbox real execute a tool call e que a raiz semeada seja o ÚNICO conteúdo alcançável. O gate 'security' prova o CONTRATO sobre FakeDriver — NÃO isto."

  # O contrafactual corre à mesma: sem ele, nem o caminho de salto teria conteúdo. require_tests
  # exige `--- PASS` por nome, pelo que um t.Skip aqui também seria vermelho.
  log_step "meta-teste do contrafactual (não precisa do componente)"
  RE_ALWAYS="^($(IFS='|'; echo "${REQUIRED_ALWAYS[*]}"))\$"
  ( export GOFLAGS="${GOFLAGS:+$GOFLAGS }-tags=$TAG"
    require_tests "$REPO_ROOT/$SEC_MOD" "$SUITE_PKG" "$RE_ALWAYS" "${REQUIRED_ALWAYS[@]}" ) || exit 1
  log_ok "contrafactual verde (sem sandbox o caminho É alcançável — o cenário P2 não é tautológico)"

  log_gate "isolation-live · veredicto"
  gate_skip_report || true
  gate_skip_file "$SKIP_FILE" || true
  if [ "${AOS_ISOLATION_LIVE_REQUIRED:-0}" = "1" ]; then
    log_fail "isolation-live: VERMELHO — AOS_ISOLATION_LIVE_REQUIRED=1 e a prova na fronteira NÃO foi produzida"
    exit 1
  fi
  log_warn "isolation-live: SALTADO (opcional). A suite compila, os cenários existem por nome e o contrafactual passou — mas a FRONTEIRA não foi medida nesta execução."
  log_warn "Para a medir: AOS_ISOLATION_LIVE=1 bash scripts/ci/isolation-live.sh (Linux + docker privilegiado)."
  exit 0
fi

# =============================================================================================
# (3) CAMINHO REAL — require_tests exige `--- PASS` POR NOME. Um t.Skip não produz `--- PASS`,
#     pelo que um componente que responda mal (ou uma env var perdida a meio) fica VERMELHO em
#     vez de verde vazio. É o mesmo mecanismo de security.sh/dr-e2e.sh.
# =============================================================================================
export AOS_SANDBOX_GVISOR_URL="$GV_URL"
export GOFLAGS="${GOFLAGS:+$GOFLAGS }-tags=$TAG"

RE_ALL="^($(IFS='|'; echo "${REQUIRED_ALL[*]}"))\$"
log_gate "isolation-live · cenários contra o executor real ($GV_URL)"
require_tests "$REPO_ROOT/$SEC_MOD" "$SUITE_PKG" "$RE_ALL" "${REQUIRED_ALL[@]}" || exit 1

# =============================================================================================
# (4) RELATÓRIO (linha marcada AOS_ISOLATION_LIVE_REPORT) + fail-closed sobre o veredicto.
#     À imagem de AOS_SECURITY_REPORT/AOS_DR_REPORT: "pass" é o ÚLTIMO campo do objecto, pelo
#     que ancorar ao fim da linha faz a verificação reflectir o veredicto AGREGADO.
# =============================================================================================
log_gate "isolation-live · relatório (fronteira exercitada · lacunas nomeadas)"
report="$( cd "$REPO_ROOT/$SEC_MOD" && go test "$SUITE_PKG" -run '^TestIsolationLive_Report$' -v -count=1 2>/dev/null \
  | grep 'AOS_ISOLATION_LIVE_REPORT' | sed 's/.*AOS_ISOLATION_LIVE_REPORT //' | head -1 )"
if [ -z "$report" ]; then
  log_fail "relatório não emitido (AOS_ISOLATION_LIVE_REPORT ausente)"
  exit 1
fi
printf '   %s\n' "$report"
if ! printf '%s' "$report" | grep -q '"executor":"real (componente HTTP externo)"'; then
  log_fail "o relatório não declara execução contra o executor REAL"
  exit 1
fi
if ! printf '%s' "$report" | grep -Eq '"pass":true[[:space:]]*}[[:space:]]*$'; then
  log_fail "relatório indica veredicto falhado (pass agregado != true)"
  exit 1
fi

# Nada saltou neste caminho — remove um marcador obsoleto de uma corrida anterior.
gate_skip_file "$SKIP_FILE" || true

log_ok "isolation-live: verde na FRONTEIRA (execução real em gVisor/runsc · fora da raiz semeada recusado · contrafactual não-vácuo)"
log_warn "LEMBRETE: isto NÃO é a fronteira do Firecracker (virtualização de hardware, ADR-004) — essa prova exige /dev/kvm e continua MANUAL: deploy/node/dev-hardened/firecracker/README.md"
