#!/usr/bin/env bash
# package.sh — GATE de ENTREGA do empacotamento do nó `aos` (AOS-168 / ADR-017 ponto 4).
#
# Orquestra a cadeia de entrega FAIL-CLOSED do artefacto deployável, REUTILIZANDO os gates
# já existentes (não duplica lógica de baseline):
#
#   1. secrets.sh  — nenhum material sensível rastreado (a chave do issuer NUNCA na imagem);
#   2. sast.sh     — gosec, baseline MULTISET (nunca sort -u); descoberta NOVA avermelha;
#   3. sca.sh      — govulncheck, baseline multiset; vuln afetante nova avermelha;
#   4. sbom.sh     — SBOM + proveniência mínima do binário (ADR-017 ponto 3, assinatura DEFERIDA);
#   5. docker build (se o Docker estiver disponível) — a imagem endurecida (ADR-017 ponto 2).
#
# Fail-closed: QUALQUER etapa vermelha aborta a entrega (exit != 0). O docker build é a única
# etapa que pode faltar por AMBIENTE (Docker/rede indisponível) — nesse caso é reportada como
# SKIP explícito (não um falso verde), coerente com a honestidade de ADR-017 ("declarado, não
# fingido"). As restantes etapas são SEMPRE obrigatórias.
#
# Uso:  bash scripts/ci/package.sh
#   IMAGE_TAG   (default aos-node:local)  — tag da imagem construída.
#   SKIP_DOCKER (default 0)               — 1 força saltar o docker build (só verificação de gates).
set -uo pipefail
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$CI_DIR/lib.sh"
setup_env

IMAGE_TAG="${IMAGE_TAG:-aos-node:local}"
export IMAGE_TAG   # sbom.sh extrai o SUBJECT desta imagem (o binário que SHIPA).
DOCKERFILE="$REPO_ROOT/deploy/node/Dockerfile"
overall=0

# COBERTURA DO GATE = tudo o que SHIPA. O healthprobe (deploy/node/healthprobe) entra na imagem
# mas vive FORA de packages/, logo discover_modules não o varreria. Este knob (só desta cadeia de
# entrega — a CI global não o define) fá-lo entrar no varrimento de sast.sh/sca.sh SEM duplicar a
# lógica de baseline: sendo stdlib-only não tem dívida, pelo que qualquer finding é NOVO => vermelho.
export AOS_EXTRA_GATE_MODULES="deploy/node/healthprobe"

run_gate() {
  local name="$1"; shift
  log_gate "package · $name"
  if bash "$@"; then
    log_ok "package · $name: verde"
  else
    log_fail "package · $name: VERMELHO — entrega bloqueada (fail-closed)"
    overall=1
  fi
}

# (1..3) Gates de segurança REUTILIZADOS (baseline multiset intacta; healthprobe incluído).
run_gate "secrets" "$CI_DIR/secrets.sh"
run_gate "sast"    "$CI_DIR/sast.sh"
run_gate "sca"     "$CI_DIR/sca.sh"

# (4) docker build — imagem endurecida. Corre ANTES do SBOM para que o sbom.sh extraia o SUBJECT
# do binário que a imagem REALMENTE carrega (proveniência bindada ao que shipa, não a um rebuild
# do host). Pode faltar por ambiente (Docker/rede): SKIP honesto, não falso-verde.
log_gate "package · docker build (imagem endurecida distroless non-root)"
if [ "${SKIP_DOCKER:-0}" = "1" ]; then
  log_warn "docker build SALTADO por SKIP_DOCKER=1 (só verificação de gates)"
elif ! command -v docker >/dev/null 2>&1; then
  log_warn "docker indisponível — build da imagem SALTADO (Dockerfile entregue; verificar noutro ambiente)"
else
  log_step "docker build -f deploy/node/Dockerfile -t $IMAGE_TAG (contexto = raiz do repo)"
  if ( cd "$REPO_ROOT" && docker build -f "$DOCKERFILE" -t "$IMAGE_TAG" . ); then
    log_ok "imagem construída: $IMAGE_TAG"
    # Prova mínima de endurecimento: USER non-root numérico (ADR-017 ponto 2).
    user="$( docker image inspect --format '{{.Config.User}}' "$IMAGE_TAG" 2>/dev/null || true )"
    if [ "$user" = "65532:65532" ] || [ "$user" = "65532" ]; then
      log_ok "imagem corre como NON-ROOT (User=$user)"
    else
      log_fail "imagem NÃO declara USER non-root numérico (User=${user:-<vazio>}) — fail-closed"
      overall=1
    fi
  else
    log_fail "docker build falhou (ver output; se foi o PULL da base por rede, o Dockerfile fica entregue)"
    overall=1
  fi
fi

# (5) SBOM + proveniência (ADR-017 ponto 3, forma mínima). Depois do build: extrai o subject da
# imagem $IMAGE_TAG quando presente; senão cai para rebuild do host (declarado, reproducible=false).
run_gate "sbom" "$CI_DIR/sbom.sh"

printf '\n%s========== ENTREGA (AOS-168 / ADR-017) ==========%s\n' "$C_BLD" "$C_RST"
if [ "$overall" -eq 0 ]; then
  log_ok "package: VERDE — pontos 1/2/4 impostos, ponto 3 mínimo declarado (ponto 5 respeitado: sem chave na imagem)"
else
  log_fail "package: VERMELHO — a entrega do artefacto está bloqueada"
fi
exit "$overall"
