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
#   AOS_ALLOW_PARTIAL_DELIVERY (default 0) — 1 aceita a saída 0 num VERDE PARCIAL. Mesmo
#     modelo do escape hatch dos pisos: visível no output e RECUSADO em CI (a CI não
#     publica entrega por verificar).
#
# CÓDIGOS DE SAÍDA: 0 = verde (nada saltado) · 1 = vermelho (etapa falhou) ·
#   2 = configuração inválida (interruptor mal escrito) · 3 = VERDE PARCIAL (nenhuma etapa
#   falhou mas alguma NÃO correu). O 3 existe porque registar não é impedir: este script é
#   um step de CI cujo único sinal consumido a jusante é o código de saída, e o passo
#   seguinte publica o artefacto. Um verde parcial indistinguível de um verde publicaria o
#   ponto 2 do ADR-017 por verificar. O mesmo sinal fica em deploy/node/build/SKIPPED.txt
#   (presente sse houve skips), para quem condiciona a publicação por ficheiro.
set -uo pipefail
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$CI_DIR/lib.sh"
setup_env

IMAGE_TAG="${IMAGE_TAG:-aos-node:local}"
export IMAGE_TAG   # sbom.sh extrai o SUBJECT desta imagem (o binário que SHIPA).
DOCKERFILE="$REPO_ROOT/deploy/node/Dockerfile"
overall=0

# Validação da CONFIGURAÇÃO do interruptor (AOS-199 / ORF-06). Antes, qualquer valor
# diferente de "1" (ex.: SKIP_DOCKER=true, SKIP_DOCKER=yes) era tratado como 0 em
# SILÊNCIO — o operador julgava ter saltado e não tinha, ou vice-versa. Só 0|1.
SKIP_DOCKER="${SKIP_DOCKER:-0}"
case "$SKIP_DOCKER" in
  0|1) ;;
  *)
    log_fail "CONFIGURAÇÃO INVÁLIDA: SKIP_DOCKER='$SKIP_DOCKER' — valores aceites: 0 ou 1."
    log_fail "  Isto NÃO é uma etapa vermelha: é o INTERRUPTOR que está mal configurado."
    exit 2 ;;
esac

# Interruptor de aceitação do VERDE PARCIAL — mesmo modelo do escape hatch dos pisos:
# visível, declarado e RECUSADO em CI. Um valor mal escrito é configuração inválida.
ALLOW_PARTIAL="${AOS_ALLOW_PARTIAL_DELIVERY:-0}"
case "$ALLOW_PARTIAL" in
  0|1) ;;
  *)
    log_fail "CONFIGURAÇÃO INVÁLIDA: AOS_ALLOW_PARTIAL_DELIVERY='$ALLOW_PARTIAL' — valores aceites: 0 ou 1."
    exit 2 ;;
esac

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
if [ "$SKIP_DOCKER" = "1" ]; then
  gate_skip "docker build (imagem endurecida)" \
            "SKIP_DOCKER=1 (interruptor explícito do operador)" \
            "ADR-017 ponto 2 — USER non-root da imagem NÃO foi verificado"
  gate_skip "SBOM subject bindado à imagem" \
            "sem imagem construída, sbom.sh cai para rebuild do host" \
            "ADR-017 ponto 3 — a proveniência NÃO fica ligada ao binário que shipa"
elif ! command -v docker >/dev/null 2>&1; then
  gate_skip "docker build (imagem endurecida)" \
            "docker indisponível no ambiente (Dockerfile entregue)" \
            "ADR-017 ponto 2 — USER non-root da imagem NÃO foi verificado; repetir noutro ambiente"
  gate_skip "SBOM subject bindado à imagem" \
            "sem imagem construída, sbom.sh cai para rebuild do host" \
            "ADR-017 ponto 3 — a proveniência NÃO fica ligada ao binário que shipa"
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
# REDECLARAÇÃO das etapas saltadas (AOS-199): um WARN a meio de 300 linhas não é
# registo — o veredicto final tem de dizer o que NÃO foi verificado. `none` quando
# nada saltou, para que a ausência de skips seja ela própria uma afirmação falsificável.
gate_skip_report || true
# ... e o MESMO registo ao lado do artefacto, legível por máquina: este script é um step
# de CI cujo passo seguinte faz upload do que está em deploy/node/build. Quem publica passa
# a poder testar `[ -e deploy/node/build/SKIPPED.txt ]` em vez de ler o log.
gate_skip_file "$REPO_ROOT/deploy/node/build/SKIPPED.txt" || true
if [ "$overall" -eq 0 ]; then
  if [ "${#GATE_SKIPPED[@]}" -eq 0 ]; then
    log_ok "package: VERDE — pontos 1/2/4 impostos, ponto 3 mínimo declarado (ponto 5 respeitado: sem chave na imagem)"
  else
    log_warn "package: VERDE PARCIAL — pontos 1/4 impostos e ponto 5 respeitado, mas as ${#GATE_SKIPPED[@]} etapa(s) acima NÃO correram."
    log_warn "         Este verde NÃO é prova do ponto 2 (imagem endurecida) do ADR-017. Não o cite como entrega verificada."
    # REGISTAR NÃO É IMPEDIR. O único sinal que a automação a jusante consome é o código
    # de saída; um verde parcial indistinguível de um verde publicaria o artefacto com o
    # ponto 2 do ADR-017 por verificar. Saída DEDICADA (3): não é vermelho (nada falhou)
    # nem verde (nem tudo correu).
    overall=3
    if [ "$ALLOW_PARTIAL" = "1" ]; then
      if [ -n "${CI:-}${GITHUB_ACTIONS:-}" ]; then
        log_fail "AOS_ALLOW_PARTIAL_DELIVERY=1 NÃO é honrado em CI — a CI não publica entrega por verificar."
      else
        log_warn "AOS_ALLOW_PARTIAL_DELIVERY=1 — saída forçada a 0 apesar de ${#GATE_SKIPPED[@]} etapa(s) por verificar."
        log_warn "         Esta execução NÃO é prova de entrega verificada; não a cite como evidência."
        printf 'AOS_PARTIAL_ACCEPTED package: VERDE PARCIAL aceite por AOS_ALLOW_PARTIAL_DELIVERY=1 — NÃO é prova de entrega verificada\n'
        overall=0
      fi
    fi
  fi
else
  log_fail "package: VERMELHO — a entrega do artefacto está bloqueada"
fi
exit "$overall"
