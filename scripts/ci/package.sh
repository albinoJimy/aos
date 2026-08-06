#!/usr/bin/env bash
# package.sh — GATE de ENTREGA do empacotamento do nó `aos` (AOS-168 / ADR-017 ponto 4).
#
# Orquestra a cadeia de entrega FAIL-CLOSED do artefacto deployável, REUTILIZANDO os gates
# já existentes (não duplica lógica de baseline):
#
#   1. secrets.sh  — nenhum material sensível rastreado (a chave do issuer NUNCA na imagem);
#   2. sast.sh     — gosec, baseline MULTISET (nunca sort -u); descoberta NOVA avermelha;
#   3. sca.sh      — govulncheck, baseline multiset; vuln afetante nova avermelha;
#   4. docker build (se o Docker estiver disponível) — a imagem endurecida (ADR-017 ponto 2),
#      provada no artefacto CONSTRUÍDO: USER non-root numérico + os labels de proveniência
#      (OCI + org.aos.*) que têm de VIAJAR com a imagem, não apenas constar do Dockerfile;
#   5. sbom.sh     — SBOM + proveniência do binário que a imagem carrega (ADR-017 ponto 3);
#   6. sign.sh     — ATESTAÇÃO ASSINADA (DSSE/ed25519) do conjunto entregue (AOS-207);
#   7. verify-attestation.sh — a entrega RECUSA o que não valida (AOS-207).
#
# AOS-207 fechou o ponto 3 do ADR-017: a atestação deixou de ser «gerada, por assinar». Os
# passos 6/7 são o que torna isso verificável — assinar sem verificar não recusa nada, e por
# isso a verificação corre SEMPRE, mesmo acabada de assinar nesta corrida. Sem chave de
# release (o esperado em PR/local) o passo 6 declara `NAO-ASSINADA` e o 7 devolve 4, que
# aqui vira VERDE PARCIAL (saída 3): a entrega não é publicável, mas nada mentiu. O mesmo
# vale quando se assina SEM imagem construída (`ASSINADA-SEM-IMAGEM`) ou quando a imagem
# atestada não está presente para o digest ser recomparado: a garantia central do ticket é a
# da IMAGEM, e um verde que não a tenha provado seria um falso-verde.
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
#   falhou mas alguma NÃO correu — inclui a entrega NÃO-ASSINADA, que não é publicável).
#   O 3 existe porque registar não é impedir: este script é
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

# --- SKIPS DOS PROCESSOS FILHOS (fronteira de processo) -----------------------
# `GATE_SKIPPED` é um array de SHELL, por processo. Este script invoca os gates com
# `bash "$CI_DIR/..."`, ou seja em processos FILHOS: um skip declarado dentro do sbom.sh, do
# sign.sh ou do verify-attestation.sh («a atestação NÃO ficou bindada à imagem») morria com o
# filho e o veredicto FINAL daqui dizia «AOS_SKIPPED_STEPS none» — falso-verde por fronteira de
# processo, exactamente o defeito que AOS-199 fechou dentro de cada script. O sink é o canal por
# ficheiro: os filhos ANEXAM (skip_declared), este script REABSORVE-OS para o seu próprio array,
# e por isso o relatório final e o SKIPPED.txt passam a ser a UNIÃO — nunca uma truncatura.
AOS_SKIP_SINK="$REPO_ROOT/deploy/node/build/SKIPPED.child.tsv"
export AOS_SKIP_SINK
mkdir -p "$( dirname "$AOS_SKIP_SINK" )"
: > "$AOS_SKIP_SINK"   # de uma corrida anterior seria um skip fantasma.

# absorb_child_skips — lê o sink e regista cada linha no array DESTE processo. Devolve o número
# absorvido (usado para não duplicar um registo genérico quando o filho já foi específico).
absorb_child_skips() {
  local n=0 etapa motivo garantia
  [ -f "$AOS_SKIP_SINK" ] || return 0
  while IFS=$'\t' read -r etapa motivo garantia; do
    [ -n "${etapa:-}" ] || continue
    gate_skip "$etapa" "$motivo" "$garantia"
    n=$(( n + 1 ))
  done < "$AOS_SKIP_SINK"
  : > "$AOS_SKIP_SINK"
  return "$n"
}

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

    # Prova mínima de PROVENIÊNCIA QUE VIAJA: os labels do Dockerfile chegaram ao artefacto.
    #
    # PORQUÊ um gate e não «está no Dockerfile, logo está na imagem»: o Dockerfile é a INTENÇÃO,
    # o `Config.Labels` da imagem construída é o FACTO — e já divergiram. Ficaram digests deste
    # nó (untagged, Agosto 3-5) com `Config.Labels: null`, construídos pelo builder clássico a
    # partir de um estado da árvore que não é o de hoje. Nenhum gate reparou, porque nenhum
    # OLHAVA: o passo 4 provava o USER e mais nada. Quem inspeccionasse esse artefacto não
    # encontraria nem o ADR nem os caminhos de verificação da cadeia — exactamente o contrário
    # do que os labels existem para dizer. Um label que não viaja é um label que não existe.
    #
    # A lista é o CONTRATO (os 4 OCI + os 7 org.aos.* de ADR-017), deliberadamente escrita aqui
    # e não derivada por grep ao Dockerfile: derivá-la faria o gate concordar com qualquer
    # apagamento — apagar o LABEL apagaria também a expectativa, e o gate ficaria verde sobre
    # uma imagem sem proveniência. Acrescentar um label ao Dockerfile NÃO obriga a tocar aqui;
    # remover um dos obrigatórios é que tem de ser uma emenda CONSCIENTE a esta lista e ao ADR.
    log_step "labels de proveniência OCI/ADR-017 presentes na imagem construída"
    labels_json="$( docker image inspect --format '{{json .Config.Labels}}' "$IMAGE_TAG" 2>/dev/null || true )"
    missing_labels=""
    for lbl in \
      org.opencontainers.image.title \
      org.opencontainers.image.description \
      org.opencontainers.image.source \
      org.opencontainers.image.licenses \
      org.aos.adr \
      org.aos.supplychain.sbom \
      org.aos.supplychain.attestation \
      org.aos.supplychain.attestation.artifact \
      org.aos.supplychain.attestation.verify \
      org.aos.supplychain.attestation.trustroot \
      org.aos.supplychain.custody ; do
      # Ausente OU presente-mas-vazio contam como EM FALTA: um label vazio não diz nada a quem
      # inspecciona o artefacto, e um verde sobre ele seria o mesmo falso-verde. Com
      # `Config.Labels: null` o inspect devolve a string `null` e nenhum padrão casa => todos
      # em falta, que é precisamente o caso que fica vermelho.
      case "$labels_json" in
        *"\"$lbl\":\"\""*) missing_labels="$missing_labels $lbl(vazio)" ;;
        *"\"$lbl\":\""*)   ;;
        *)                 missing_labels="$missing_labels $lbl" ;;
      esac
    done
    if [ -z "$missing_labels" ]; then
      log_ok "imagem carrega os 11 labels de proveniência (OCI + org.aos.* / ADR-017)"
    else
      log_fail "imagem SEM label(s) de proveniência obrigatórios:$missing_labels — fail-closed"
      log_fail "  A imagem construída não carrega a proveniência que o Dockerfile declara."
      log_fail "  Ver o bloco LABEL em deploy/node/Dockerfile e ADR-017 (docs/adr/ADR-017-supply-chain-node.md)."
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

# (6) ATESTAÇÃO ASSINADA (AOS-207 / ADR-017 ponto 3). Finaliza o bloco `signature` da
# proveniência, escreve o manifesto de entrega e — havendo AOS_RELEASE_KEY_FILE — emite o
# envelope DSSE. Sem chave NÃO falha: escreve `NAO-ASSINADA` e regista um SKIP declarado
# (assinar é um passo de RELEASE; um PR não tem, nem deve ter, a chave de release).
# Vermelho aqui é configuração inválida ou chave em que o registo não confia — não ausência.
run_gate "sign (atestação assinada)" "$CI_DIR/sign.sh"

# (7) A ENTREGA RECUSA O QUE NÃO VALIDA (AOS-207). Corre SEMPRE, mesmo que o passo 6 acabe
# de assinar: é a verificação — não a assinatura — que impede publicar um artefacto cujo
# digest não bate com o que está atestado. É este passo que a PROVA NEGATIVA do CA exercita
# (trocar o digest da imagem no manifesto ⇒ vermelho).
#
# Precisa de tratamento próprio porque `run_gate` colapsa todo o não-zero em VERMELHO, e a
# saída 4 (POR VERIFICAR, declarada) não é uma falha. Cobre TRÊS casos, todos «nada mentiu, mas
# nem tudo foi provado»: (a) build sem chave de release; (b) assinou-se sem imagem construída,
# logo a atestação não cobre imagem nenhuma; (c) a imagem atestada não está presente para o
# digest ser recomparado com a realidade. Tratá-la como vermelha tornaria todo o PR vermelho;
# tratá-la como verde publicaria uma entrega por verificar — era o falso-verde que (b) e (c)
# produziam antes. Vira SKIP ⇒ VERDE PARCIAL (saída 3) ⇒ não publicável.
log_gate "package · verify-attestation (a entrega recusa o que não valida)"
# `|| va_rc=$?`: `lib.sh` impõe `set -euo pipefail`. Escrito como `bash ...; va_rc=$?`, uma saída
# 4 do filho abortava ESTE script imediatamente — o `case` abaixo, o veredicto, o registo dos
# skips e o SKIPPED.txt eram código morto, e o package.sh terminava com 4 (um código que a sua
# própria documentação não define) em vez de 3. Capturar em condição mantém o errexit e o
# vocabulário de saída documentado.
va_rc=0
bash "$CI_DIR/verify-attestation.sh" || va_rc=$?

# REABSORÇÃO dos skips dos três filhos (sbom, sign, verify). Tem de acontecer ANTES do veredicto:
# é o que faz o relatório final e o SKIPPED.txt dizerem a verdade sobre o que não correu.
# (mesma razão para o `|| absorbed=$?`: a função devolve a CONTAGEM, não um estado de erro.)
absorbed=0
absorb_child_skips || absorbed=$?
if [ "$absorbed" -gt 0 ]; then
  log_step "reabsorvidos $absorbed skip(s) declarados nos gates filhos (sbom/sign/verify)"
fi

case "$va_rc" in
  0)
    log_ok "package · verify-attestation: verde — atestação assinada, a cobrir a imagem, e coerente com o artefacto" ;;
  4)
    # Saída 4 = POR VERIFICAR (não-assinada, ou assinada sem cobrir a imagem, ou imagem
    # indisponível para recomparação). O filho já declarou o motivo EXACTO no sink, acabado de
    # reabsorver; só se acrescenta um registo genérico se, por alguma razão, nada tiver vindo —
    # nunca se deixa uma saída 4 sem skip registado, senão o veredicto final voltaria a verde.
    if [ "$absorbed" -eq 0 ]; then
      gate_skip "verificação da atestação assinada" \
                "verify-attestation devolveu 4 (POR VERIFICAR) sem detalhe no sink" \
                "ADR-017 ponto 3 — a atestação NÃO foi integralmente verificada; entrega não publicável"
    fi
    log_warn "package · verify-attestation: POR VERIFICAR (saída 4) — entrega NÃO publicável" ;;
  *)
    log_fail "package · verify-attestation: VERMELHO — entrega bloqueada (atestação não valida)"
    overall=1 ;;
esac

printf '\n%s========== ENTREGA (AOS-168 / ADR-017) ==========%s\n' "$C_BLD" "$C_RST"
# REDECLARAÇÃO das etapas saltadas (AOS-199): um WARN a meio de 300 linhas não é
# registo — o veredicto final tem de dizer o que NÃO foi verificado. `none` quando
# nada saltou, para que a ausência de skips seja ela própria uma afirmação falsificável.
gate_skip_report || true
# ... e o MESMO registo ao lado do artefacto, legível por máquina: este script é um step
# de CI cujo passo seguinte faz upload do que está em deploy/node/build. Quem publica passa
# a poder testar `[ -e deploy/node/build/SKIPPED.txt ]` em vez de ler o log.
#
# A lista deste processo JÁ INCLUI os skips dos filhos (absorb_child_skips, acima), pelo que
# esta escrita é a UNIÃO e não uma truncatura: antes, o `rm -f` interno apagava o SKIPPED.txt
# que o sbom.sh tinha escrito sempre que o pai não tivesse skips PRÓPRIOS — o sinal máquina-
# legível desaparecia justamente para quem decide publicar por ficheiro.
gate_skip_file "$REPO_ROOT/deploy/node/build/SKIPPED.txt" || true
rm -f "$AOS_SKIP_SINK"   # canal interno; não é artefacto de entrega.
if [ "$overall" -eq 0 ]; then
  if [ "${#GATE_SKIPPED[@]}" -eq 0 ]; then
    log_ok "package: VERDE — pontos 1/2/3/4 impostos (ponto 3 ASSINADO e VERIFICADO, AOS-207; ponto 5 respeitado: sem chave na imagem)"
  else
    log_warn "package: VERDE PARCIAL — pontos 1/4 impostos e ponto 5 respeitado, mas as ${#GATE_SKIPPED[@]} etapa(s) acima NÃO correram."
    log_warn "         Este verde NÃO é prova do ponto 2 (imagem endurecida) nem do ponto 3 (atestação assinada) do ADR-017."
    log_warn "         Não o cite como entrega verificada."
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
