# shellcheck shell=bash
# lib.sh — biblioteca partilhada do gate runner local do AOS (AOS-010).
#
# Fonte de verdade dos gates de CI: estes scripts. A CI (.github/workflows/ci.yml)
# apenas INVOCA scripts/ci/*.sh — não duplica lógica de gates. O runner é
# fail-closed: qualquer gate vermelho aborta com exit != 0. NÃO há '|| true',
# 'continue-on-error' nem 'set +e' a mascarar falhas. Onde capturamos o código de
# saída de uma ferramenta (padrão 'cmd || rc=$?') é SEMPRE para o avaliar e falhar
# fechado a seguir — nunca para o ignorar.
#
# Uso: cada gate faz 'source "$(dirname "$0")/lib.sh"' e chama as helpers.

set -euo pipefail

# --- Localização canónica -----------------------------------------------------
# CI_DIR = scripts/ci ; REPO_ROOT = raiz do monorepo.
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$CI_DIR/../.." && pwd)"
BASELINE_DIR="$CI_DIR/baseline"

# --- Cores (desligadas se não houver TTY ou se NO_COLOR) ----------------------
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YEL=$'\033[33m'
  C_CYN=$'\033[36m'; C_BLD=$'\033[1m'; C_RST=$'\033[0m'
else
  C_RED=""; C_GRN=""; C_YEL=""; C_CYN=""; C_BLD=""; C_RST=""
fi

log()      { printf '%s\n' "$*"; }
log_gate() { printf '\n%s== GATE: %s ==%s\n' "$C_BLD$C_CYN" "$*" "$C_RST"; }
log_step() { printf '%s--> %s%s\n'  "$C_CYN" "$*" "$C_RST"; }
log_ok()   { printf '%sOK%s   %s\n' "$C_GRN" "$C_RST" "$*"; }
log_warn() { printf '%sWARN%s %s\n' "$C_YEL" "$C_RST" "$*"; }
log_fail() { printf '%sFAIL%s %s\n' "$C_RED" "$C_RST" "$*" >&2; }

# --- PISOS dos limiares de gate (AOS-199 / ORF-06) ----------------------------
# PROBLEMA que isto fecha: TODO o limiar abaixo era sobreponível por ambiente SEM
# piso nem registo. `EVAL_PASS_RATE_MIN=0` ou `KERNEL_COVERAGE_MIN=0` produziam
# «gates verdes» sem exercitar nada — o verde deixava de ser prova reproduzível.
# Um gate cujo limiar se pode zerar em silêncio não é um gate.
#
# MODELO: estes knobs passam a ser RATCHETS — só APERTAM. O piso é o compromisso
# JÁ DOCUMENTADO (specs/01 §4, ADR-012); descer abaixo dele não é afinar um gate,
# é renegociar uma spec em silêncio a partir do ambiente.
#
# DOIS DIAGNÓSTICOS DISTINTOS (é a razão de haver duas mensagens diferentes):
#   VIOLAÇÃO DE PISO   -> a CONFIGURAÇÃO é inválida; nenhum código foi avaliado.
#                         Quem lê a CI corrige o ambiente/pipeline, não o código.
#   LIMIAR NÃO ATINGIDO -> a configuração é válida e o CÓDIGO ficou abaixo dela.
#                         Quem lê a CI corrige o código.
#
# ESCAPE HATCH (uso legítimo: bisect/experimentação LOCAL):
#   AOS_GATE_FLOOR_OVERRIDE="<justificação legível>"
#   (a) exige justificação com >= 8 caracteres — um "1" não serve;
#   (b) é RECUSADO em CI (CI= / GITHUB_ACTIONS=) — a CI nunca desce um piso;
#   (c) exige SINAL POSITIVO de sessão local — terminal interactivo ([ -t 1 ]) ou
#       AOS_LOCAL_SESSION=1 declarado. A AUSÊNCIA de CI=/GITHUB_ACTIONS= NÃO é prova de
#       sessão local: um Jenkins, um runner self-hosted com ambiente limpo ou um
#       `env -u CI -u GITHUB_ACTIONS` não os definem, e antes disso o hatch era honrado
#       nesses ambientes. A recusa é o comportamento por OMISSÃO; só um sinal positivo
#       a levanta;
#   (d) imprime um banner AOS_FLOOR_BREACH que declara, no próprio output, que a
#       execução NÃO constitui prova de gates verdes. Uma linha do banner vai também
#       para STDOUT: o veredicto agregado ("PIPELINE VERDE") viaja em stdout, e uma
#       ressalva que só existisse em stderr desapareceria de qualquer captura que
#       separe os canais (`bash run.sh > evidencia.log`) — precisamente o artefacto
#       que a ressalva diz não poder ser citado como prova.
FLOOR_KERNEL_COVERAGE_MIN=80    # specs/01 §4 + CONTRIBUTING: «cobertura do kernel >= 80%».
FLOOR_COVERAGE_MIN=80           # gate generalizado (AOS-109) herda a barra do kernel.
FLOOR_MODULE_COVERAGE_MIN=80    # apex/memory/routing/registry: «igual ao limiar do kernel».
FLOOR_EVAL_PASS_RATE_MIN=0.90   # ADR-012 / AOS-114: alvo de eval-pass-rate >= 90%.

# _is_number <s> — verdadeiro sse `s` for um decimal não-negativo bem formado.
# Rejeita '', '.', '8.0.0' e qualquer não-dígito ANTES de o valor chegar ao awk: um
# valor malformado provocaria erro de sintaxe no awk e seria diagnosticado como o
# problema errado ("fora do domínio" em vez de "não é numérico"). Fail-closed em ambos
# os casos, mas a mensagem tem de apontar a causa certa.
_is_number() {
  case "${1:-}" in
    ''|.|*[!0-9.]*|*.*.*) return 1 ;;
    *) return 0 ;;
  esac
}

# enforce_threshold_floor <NOME> <valor> <piso> <max> <unidade> <origem>
#   Valida a CONFIGURAÇÃO de um limiar. Devolve 0 se o limiar é utilizável; != 0
#   se a configuração é inválida (não-numérica, fora do domínio, ou abaixo do piso
#   sem override declarado). NUNCA emite a mensagem de «limiar não atingido» — esse
#   é o outro diagnóstico, e pertence ao gate que mede.
#   <echo> = "always" (default) imprime sempre a linha de registo; "on-override"
#   imprime-a só quando o valor veio do ambiente; "never" cala-a e delega o registo a
#   gate_threshold_report, chamado pelo gate que CONSOME o limiar. Os dois limiares
#   globais de lib.sh usam "never": lib.sh é sourced pelos ~22 gates e repetir o valor
#   em cada um seria ruído (o gate ux-dx/AOS-128 mede fadiga de output), mas o marcador
#   AOS_GATE_THRESHOLD tem de ser um INVENTÁRIO — se um recolector que lhe faz grep só
#   visse os limiares quando houvesse override, veria menos do que o que está em vigor.
#   Registo no ponto de CONSUMO: aparece SEMPRE (default incluído), uma só vez.
enforce_threshold_floor() {
  local name="$1" val="$2" floor="$3" max="$4" unit="${5:-}" origin="${6:-default}" echo_pol="${7:-always}"

  if ! _is_number "$val"; then
    log_fail "VIOLAÇÃO DE PISO (configuração inválida): ${name}='${val}' não é numérico."
    log_fail "  Nenhum código foi avaliado — corrija a CONFIGURAÇÃO do pipeline, não o código."
    return 1
  fi
  if ! awk "BEGIN{exit !($val <= $max)}"; then
    log_fail "VIOLAÇÃO DE PISO (configuração inválida): ${name}=${val} excede o domínio (máx ${max}${unit})."
    log_fail "  Provável confusão de unidade (percentagem vs fracção). Nenhum código foi avaliado."
    return 1
  fi
  if awk "BEGIN{exit !($val >= $floor)}"; then
    if [ "$echo_pol" = "always" ] || { [ "$echo_pol" = "on-override" ] && [ "$origin" != "default" ]; }; then
      printf '   AOS_GATE_THRESHOLD %s=%s%s piso=%s%s origem=%s\n' \
        "$name" "$val" "$unit" "$floor" "$unit" "$origin"
    fi
    return 0
  fi

  # Abaixo do piso. É SEMPRE violação de piso — nunca «limiar não atingido».
  local reason="${AOS_GATE_FLOOR_OVERRIDE:-}"
  if [ -n "$reason" ]; then
    if [ -n "${CI:-}${GITHUB_ACTIONS:-}" ]; then
      log_fail "VIOLAÇÃO DE PISO: ${name}=${val}${unit} < piso ${floor}${unit} (origem ${origin})."
      log_fail "  AOS_GATE_FLOOR_OVERRIDE está definido mas NÃO é honrado em CI — a CI não desce pisos."
      return 1
    fi
    if [ "${#reason}" -lt 8 ]; then
      log_fail "VIOLAÇÃO DE PISO: ${name}=${val}${unit} < piso ${floor}${unit} (origem ${origin})."
      log_fail "  AOS_GATE_FLOOR_OVERRIDE='${reason}' não é justificação (mínimo 8 caracteres legíveis)."
      return 1
    fi
    # SINAL POSITIVO de sessão local. A ausência de CI=/GITHUB_ACTIONS= não serve: num
    # runner que não os defina (Jenkins, self-hosted, `env -u CI`) o hatch passaria a
    # descer o piso com exit 0 — defesa-em-profundidade a funcionar num sentido só.
    if [ ! -t 1 ] && [ "${AOS_LOCAL_SESSION:-}" != "1" ]; then
      log_fail "VIOLAÇÃO DE PISO: ${name}=${val}${unit} < piso ${floor}${unit} (origem ${origin})."
      log_fail "  AOS_GATE_FLOOR_OVERRIDE só é honrado com SINAL POSITIVO de sessão local:"
      log_fail "    terminal interactivo (stdout é um TTY) ou AOS_LOCAL_SESSION=1 declarado."
      log_fail "  Não haver CI=/GITHUB_ACTIONS= NÃO é prova de sessão local — a recusa é o default."
      return 1
    fi
    printf '%s\n' "${C_YEL}${C_BLD}AOS_FLOOR_BREACH ============================================${C_RST}" >&2
    printf '%s\n' "${C_YEL}AOS_FLOOR_BREACH ${name}=${val}${unit} ABAIXO do piso ${floor}${unit} (origem ${origin})${C_RST}" >&2
    printf '%s\n' "${C_YEL}AOS_FLOOR_BREACH justificação: ${reason}${C_RST}" >&2
    printf '%s\n' "${C_YEL}AOS_FLOOR_BREACH esta execução NÃO é prova de gates verdes; não a cite como evidência.${C_RST}" >&2
    printf '%s\n' "${C_YEL}${C_BLD}AOS_FLOOR_BREACH ============================================${C_RST}" >&2
    # A MESMA ressalva em STDOUT, o canal onde viaja o veredicto agregado: um log que
    # capture só stdout ficaria «verde» sem ela.
    printf 'AOS_FLOOR_BREACH %s=%s%s abaixo do piso %s%s — esta corrida NÃO é prova de gates verdes\n' \
      "$name" "$val" "$unit" "$floor" "$unit"
    return 0
  fi
  log_fail "VIOLAÇÃO DE PISO: ${name}=${val}${unit} < piso ${floor}${unit} (origem ${origin})."
  log_fail "  Isto NÃO é «limiar não atingido»: a CONFIGURAÇÃO é inválida e NENHUM código foi avaliado."
  log_fail "  O piso é o compromisso documentado (CONTRIBUTING.md §Limiares); ${name} só pode APERTAR."
  log_fail "  Bisect local legítimo: AOS_GATE_FLOOR_OVERRIDE=\"<justificação>\" (recusado em CI)."
  return 1
}

# gate_threshold <NOME> <default> <piso> <max> [unidade]
#   Resolve o limiar (env > default), VALIDA-O contra o piso e publica-o na
#   variável <NOME>. Ponto ÚNICO de resolução: quem acrescentar um limiar novo
#   passa por aqui e herda piso + registo no output. Devolve != 0 se a
#   configuração for inválida (o chamador faz `|| exit 1` — fail-closed).
GATE_THRESHOLDS=()

gate_threshold() {
  local name="$1" def="$2" floor="$3" max="$4" unit="${5:-}" echo_pol="${6:-always}"
  local cur="${!name:-}" origin val
  if [ -n "$cur" ]; then origin="env(override)"; val="$cur"; else origin="default"; val="$def"; fi
  enforce_threshold_floor "$name" "$val" "$floor" "$max" "$unit" "$origin" "$echo_pol" || return 1
  GATE_THRESHOLDS+=("$name|$val|$floor|$unit|$origin")
  printf -v "$name" '%s' "$val"
}

# gate_threshold_report — INVENTÁRIO dos limiares em vigor NESTE processo, no ponto de
#   consumo. Chamado pelos gates cujos limiares foram resolvidos em silêncio no `source`
#   (echo="never"), para que um recolector que faça grep a AOS_GATE_THRESHOLD veja o valor
#   EFECTIVO em toda a corrida — e não só quando alguém sobrepôs. Não duplica: os limiares
#   com echo="always" pertencem a gates que não chamam esta função.
gate_threshold_report() {
  if [ "${#GATE_THRESHOLDS[@]}" -eq 0 ]; then
    printf '   AOS_GATE_THRESHOLD none\n'
    return 0
  fi
  local e n v f u o
  for e in "${GATE_THRESHOLDS[@]}"; do
    IFS='|' read -r n v f u o <<< "$e"
    printf '   AOS_GATE_THRESHOLD %s=%s%s piso=%s%s origem=%s\n' "$n" "$v" "$u" "$f" "$u" "$o"
  done
}

# --- Knobs de CAMINHO (raízes e baselines) — simétrico de gate_threshold ------------
# MESMA classe de defeito que ORF-06, por knob de CAMINHO em vez de knob numérico: um
# gate cuja RAIZ de varrimento ou cuja BASELINE se pode desviar por ambiente sai VERDE
# VACUOSO sem exercitar nada (uma árvore vazia não tem violações; uma baseline que
# contenha tudo não tem descobertas novas). Reprodução do buraco, hoje ainda aberto:
#   mkdir -p /tmp/fakeroot/packages
#   LAYER_LINT_ROOT=/tmp/fakeroot bash scripts/ci/layer-lint.sh   # -> exit 0, "OK"
#
# gate_path <NOME> <default> — resolve o caminho (env > default), RECUSA o desvio em CI
#   e regista sempre a origem. Os consumidores (layer-lint.sh, ref-lint.py, deferrals.py,
#   event-catalog.py, integration.py) estão FORA da pista de escrita do AOS-199: o
#   mecanismo fica pronto e a adopção é PENDÊNCIA nomeada (ver CONTRIBUTING §Knobs de
#   caminho). Enquanto não for adoptado, esses gates continuam desviáveis.
gate_path() {
  local name="$1" def="$2"
  local cur="${!name:-}" origin val
  if [ -n "$cur" ]; then origin="env(override)"; val="$cur"; else origin="default"; val="$def"; fi
  if [ "$origin" != "default" ] && [ -n "${CI:-}${GITHUB_ACTIONS:-}" ]; then
    log_fail "DESVIO DE RAIZ RECUSADO: ${name}='${val}' != default '${def}' — a CI não desvia a raiz/baseline de um gate."
    log_fail "  Um gate apontado a uma árvore vazia (ou a uma baseline que contém tudo) sai VERDE sem varrer nada."
    return 1
  fi
  printf '   AOS_GATE_ROOT %s=%s origem=%s\n' "$name" "$val" "$origin"
  printf -v "$name" '%s' "$val"
}

# --- Auto-teste do PRÓPRIO mecanismo de pisos (AOS-199, anti-recorrência) ------------
# A violação de piso é uma propriedade NÃO-REGRESSIVA e, sem guard, o mecanismo que
# impede a deriva está ele próprio desprotegido: repor `APEX_COVERAGE_MIN="${APEX_COVERAGE_MIN:-80}"`
# ou apagar o `|| exit 1` de uma chamada a gate_threshold reabriria ORF-06 em SILÊNCIO,
# passando em todos os gates. Este auto-teste é barato (aritmética + grep, sem I/O de
# rede nem toolchain) e corre dentro de um gate que a CI já invoca. Assere a MENSAGEM,
# não só o código de saída: um `exit 1` por outra razão qualquer não o satisfaz.
gate_floor_selftest() {
  local rc=0 out spec name floor max fname fmin fval offenders bad
  log_step "auto-teste do mecanismo de pisos (AOS-199): a violação de piso TEM de bloquear"

  # (1) DINÂMICO: injectar 0 em cada piso e exigir recusa COM a mensagem certa.
  #     Corre em subshell (command substitution) com o hatch e os marcadores de CI
  #     limpos — não contamina o processo do gate nem o inventário de limiares.
  while IFS= read -r spec; do
    [ -n "$spec" ] || continue
    read -r name floor max <<< "$spec"
    if out="$( AOS_GATE_FLOOR_OVERRIDE=""; CI=""; GITHUB_ACTIONS=""; \
               enforce_threshold_floor "$name" 0 "$floor" "$max" "" "auto-teste" 2>&1 )"; then
      log_fail "AUTO-TESTE FALHOU: ${name}=0 NÃO foi recusado — o mecanismo de pisos está NEUTRALIZADO."
      rc=1
    else
      case "$out" in
        *"VIOLAÇÃO DE PISO"*) ;;
        *) log_fail "AUTO-TESTE FALHOU: ${name}=0 foi recusado mas SEM a mensagem «VIOLAÇÃO DE PISO» (diagnóstico errado)."
           rc=1 ;;
      esac
    fi
  done <<EOF
KERNEL_COVERAGE_MIN $FLOOR_KERNEL_COVERAGE_MIN 100
COVERAGE_MIN $FLOOR_COVERAGE_MIN 100
MODULE_COVERAGE_MIN $FLOOR_MODULE_COVERAGE_MIN 100
EVAL_PASS_RATE_MIN $FLOOR_EVAL_PASS_RATE_MIN 1
EOF

  # (2) RATCHET DOS PRÓPRIOS PISOS: só sobem. Os valores esperados são o compromisso
  #     documentado (specs/01 §4, ADR-012); a comparação é >=, pelo que APERTAR um piso
  #     não exige tocar aqui — só BAIXÁ-LO avermelha.
  while IFS= read -r spec; do
    [ -n "$spec" ] || continue
    read -r fname fmin <<< "$spec"
    fval="${!fname:-}"
    if ! _is_number "$fval" || ! awk "BEGIN{exit !($fval >= $fmin)}"; then
      log_fail "AUTO-TESTE FALHOU: ${fname}=${fval:-<vazio>} desceu abaixo do compromisso documentado (${fmin}) — os pisos só sobem."
      rc=1
    fi
  done <<EOF
FLOOR_KERNEL_COVERAGE_MIN 80
FLOOR_COVERAGE_MIN 80
FLOOR_MODULE_COVERAGE_MIN 80
FLOOR_EVAL_PASS_RATE_MIN 0.90
EOF

  # (3) ESTÁTICO: nenhum limiar volta a resolver-se FORA de gate_threshold. É o padrão
  #     exacto que ORF-06 descreve (`VAR="${VAR:-80}"`): sobreponível por ambiente, sem
  #     piso e sem registo.
  offenders="$( grep -nE '^[[:space:]]*(export[[:space:]]+)?[A-Z_]*(_MIN|_MAX|_THRESHOLD|_RATE|_PCT|_BUDGET|_LIMIT|_TARGET)="?\$\{[A-Za-z_]+:-' "$CI_DIR"/*.sh 2>/dev/null || true )"
  if [ -n "$offenders" ]; then
    log_fail "AUTO-TESTE FALHOU: limiar resolvido por \${VAR:-default} FORA de gate_threshold (bypass de piso — ORF-06):"
    printf '%s\n' "$offenders" | sed 's/^/       /' >&2
    log_fail "  Encaminhe-o por gate_threshold <NOME> <default> <piso> <max> [unidade] || exit 1."
    rc=1
  fi

  # (4) ESTÁTICO: toda a invocação de gate_threshold é FAIL-CLOSED. Apagar o `|| exit 1`
  #     deixaria o gate prosseguir com um limiar recusado.
  bad="$( grep -nE '^[[:space:]]*gate_threshold[[:space:]]' "$CI_DIR"/*.sh 2>/dev/null \
          | grep -vE '\|\|[[:space:]]*(exit|return)[[:space:]]+1' || true )"
  if [ -n "$bad" ]; then
    log_fail "AUTO-TESTE FALHOU: invocação de gate_threshold sem '|| exit 1' (a recusa do limiar não bloquearia):"
    printf '%s\n' "$bad" | sed 's/^/       /' >&2
    rc=1
  fi

  # (5) O simétrico para CAMINHOS: gate_path tem de RECUSAR o desvio de raiz/baseline em CI.
  #     O mecanismo está pronto mas os consumidores vivem noutras pistas (ver CONTRIBUTING
  #     §Knobs de CAMINHO); sem este guard apodrecia antes de ser adoptado.
  if out="$( CI=1 AOS_SELFTEST_PATH_PROBE=/tmp/nao-existe gate_path AOS_SELFTEST_PATH_PROBE /raiz-default 2>&1 )"; then
    log_fail "AUTO-TESTE FALHOU: gate_path NÃO recusou um desvio de raiz em CI — um gate apontado a uma árvore vazia sairia verde."
    rc=1
  else
    case "$out" in
      *"DESVIO DE RAIZ RECUSADO"*) ;;
      *) log_fail "AUTO-TESTE FALHOU: gate_path recusou o desvio mas SEM a mensagem «DESVIO DE RAIZ RECUSADO»."
         rc=1 ;;
    esac
  fi

  [ "$rc" -eq 0 ] && log_ok "auto-teste dos pisos: mecanismo intacto (4 pisos recusam 0; nenhum limiar fora de gate_threshold; gate_path recusa desvio em CI)"
  return "$rc"
}

# --- Registo de etapas SALTADAS (AOS-199) -------------------------------------
# Uma etapa saltada que não aparece no output é um FALSO-VERDE. Toda a etapa que
# não corra regista-se aqui e é REDECLARADA no veredicto final do gate, com a
# garantia que ficou POR VERIFICAR — nunca só um WARN a meio de 300 linhas.
GATE_SKIPPED=()

# gate_skip <etapa> <motivo> <garantia por verificar>
gate_skip() {
  GATE_SKIPPED+=("$1|$2|$3")
  log_warn "SALTADO: $1 — $2"
  log_warn "         garantia POR VERIFICAR: $3"
}

# gate_skip_report — redeclara, no fim, tudo o que foi saltado (0 se nada saltou).
gate_skip_report() {
  if [ "${#GATE_SKIPPED[@]}" -eq 0 ]; then
    printf '   AOS_SKIPPED_STEPS none\n'
    return 0
  fi
  local e
  printf '   %sAOS_SKIPPED_STEPS %s etapa(s) NÃO verificada(s) nesta execução:%s\n' \
    "$C_YEL" "${#GATE_SKIPPED[@]}" "$C_RST"
  for e in "${GATE_SKIPPED[@]}"; do
    printf '   %sAOS_SKIPPED_STEP  %s (motivo: %s) -> POR VERIFICAR: %s%s\n' \
      "$C_YEL" "${e%%|*}" "$(printf '%s' "$e" | cut -d'|' -f2)" "${e##*|}" "$C_RST"
  done
  return 1
}

# gate_skip_file <caminho> — grava o registo de skips ao LADO do artefacto, para que a
#   decisão de publicar seja condicionável POR MÁQUINA e não só legível por um humano
#   que leia o log. Um consumidor a jusante (job de publicação, promoção de release)
#   testa `[ -e <caminho> ]`.
#   REMOVE o ficheiro quando nada foi saltado: um marcador obsoleto de uma corrida
#   anterior seria um falso-positivo tão mau como a ausência dele é um falso-verde.
gate_skip_file() {
  local out="$1" e
  if [ "${#GATE_SKIPPED[@]}" -eq 0 ]; then
    rm -f "$out"
    return 0
  fi
  mkdir -p "$(dirname "$out")"
  {
    printf '# AOS_SKIPPED_STEPS %s — etapas NÃO verificadas nesta execução (AOS-199).\n' "${#GATE_SKIPPED[@]}"
    printf '# A presença deste ficheiro significa: o artefacto ao lado NÃO está integralmente verificado.\n'
    for e in "${GATE_SKIPPED[@]}"; do
      printf 'AOS_SKIPPED_STEP\t%s\tmotivo=%s\tpor_verificar=%s\n' \
        "${e%%|*}" "$(printf '%s' "$e" | cut -d'|' -f2)" "${e##*|}"
    done
  } > "$out"
  printf '   AOS_SKIPPED_FILE %s (%s etapa(s)) — sinal MÁQUINA-legível para quem decide publicar\n' \
    "$out" "${#GATE_SKIPPED[@]}"
  return 1
}

# --- Versões pinadas das ferramentas (SCA/SAST/lint) --------------------------
# Pinadas para builds reprodutíveis (ADR-008: supply-chain mínima e determinista).
STATICCHECK_PIN="honnef.co/go/tools/cmd/staticcheck@v0.7.0"
GOVULNCHECK_PIN="golang.org/x/vuln/cmd/govulncheck@v1.6.0"
# gosec: v2.21.4 arrastava golang.org/x/tools@v0.25.0, que NÃO COMPILA com Go 1.25
# ("invalid array length -delta * delta" em internal/tokeninternal). Não é achado de
# segurança — é a ferramenta que deixou de construir quando o runner subiu de toolchain.
# v2.28.0 é a primeira linha que constrói na 1.25. Ao subir a toolchain, as ferramentas
# pinadas têm de subir com ela: um pin antigo não é reprodutibilidade, é um gate que não corre.
GOSEC_PIN="github.com/securego/gosec/v2/cmd/gosec@v2.28.0"

# --- Gate de cobertura --------------------------------------------------------
# Cobertura mínima de linhas do kernel (Reference Monitor). AOS-010 AC.
# Sobreponível por ambiente APENAS PARA APERTAR: piso FLOOR_KERNEL_COVERAGE_MIN (AOS-199).
# echo="never": o registo destes dois é feito por gate_threshold_report no gate que os
# CONSOME (test.sh) — inventário completo em toda a corrida, sem repetir a linha nos ~22
# gates que apenas fazem `source lib.sh` e nunca lêem o valor.
gate_threshold KERNEL_COVERAGE_MIN 80 "$FLOOR_KERNEL_COVERAGE_MIN" 100 "%" never || exit 1
# Módulo(s) do kernel sujeitos ao gate de cobertura (rel. a REPO_ROOT).
KERNEL_MODULES=("packages/kernel/reference-monitor")

# --- Gate de cobertura GENERALIZADO (AOS-109) ---------------------------------
# Limiar CONFIGURÁVEL aplicado além do kernel: o piso do kernel (AOS-010) é agora
# um caso particular de um gate parametrizável por env var. Uma descida abaixo do
# limiar num módulo gated BLOQUEIA o merge (fail-closed; ver test.sh). O default
# HERDA de KERNEL_COVERAGE_MIN (retro-compat: o knob histórico continua a governar o
# piso — apertar KERNEL_COVERAGE_MIN aperta o gate generalizado), pelo que o
# comportamento herdado não muda e o knob antigo não fica inerte.
# Piso próprio (AOS-199): herdar de KERNEL_COVERAGE_MIN deixaria o gate generalizado
# descer se o knob histórico descesse; ambos têm agora o mesmo piso, independente.
gate_threshold COVERAGE_MIN "$KERNEL_COVERAGE_MIN" "$FLOOR_COVERAGE_MIN" 100 "%" never || exit 1
# Módulos sujeitos ao limiar generalizado (rel. a REPO_ROOT). Inclui o kernel
# (retro-compat com KERNEL_MODULES) e o próprio testkit (AOS-109) — dogfooding: o
# framework de testes de referência está ele próprio sob o piso que impõe.
#
# O `agent-runtime` FALTAVA, e é METADE DO KERNEL. Achado da verificação de completude de
# 2026-08-23: cinco documentos — README §gates, CONTRIBUTING (duas vezes), specs/EPIC-01 e
# docs/testing — declaram «cobertura do KERNEL >= 80%», e o README define kernel como
# «RM (Reference Monitor) + RT (Agent Runtime)». O RT nunca esteve nesta lista.
#
# O módulo ERA descoberto e medido (`discover_modules` apanha todos os `go.mod` sob
# `packages/`); só não era COMPARADO com o limiar. Estava a 93,5% quando isto foi escrito — o
# que é exactamente o ponto: esse verde saía igual com o gate desligado, e o RT podia cair para
# 40% com os 24 gates verdes.
#
# É onde vivem a execução durável, o disjuntor, a saga, o replay e a máquina de estados.
COVERAGE_GATED_MODULES=("packages/kernel/reference-monitor" "packages/kernel/agent-runtime" "packages/testkit" "packages/control-plane/governance/approval-card" "packages/control-plane/governance/plan-approval" "packages/control-plane/governance/surface-adapter" "packages/control-plane/governance/progress-surface" "packages/control-plane/governance/confidence-calibration" "packages/control-plane/governance/autonomy-surface" "packages/control-plane/governance/authoring-surface" "packages/control-plane/governance/trajectory-surface")
# Directório do testkit (conversor de cobertura cov2lcov, Go stdlib puro).
TESTKIT_DIR="$REPO_ROOT/packages/testkit"
# Artefacto de cobertura MÁQUINA-LEGÍVEL emitido pelo gate 3 (LCOV). Ignorado pelo
# git (.gitignore: coverage/). AOS-109 AC1.
COVERAGE_LCOV_OUT="${COVERAGE_LCOV_OUT:-$REPO_ROOT/coverage/lcov.info}"

# --- Ambiente (Windows/scoop-mingw + Linux) -----------------------------------
# Garante gcc (para -race via CGO) e o bin do GOPATH no PATH; força CGO_ENABLED=1.
# Idempotente e portátil: em Linux o gcc é do sistema; em Windows usa o mingw do
# scoop se existir. NÃO falha se um caminho não existir — apenas o acrescenta.
setup_env() {
  export CGO_ENABLED=1
  local gobin; gobin="$(go env GOPATH)/bin"
  case ":$PATH:" in *":$gobin:"*) ;; *) PATH="$gobin:$PATH";; esac
  # Windows + scoop mingw (gcc para o -race). Silencioso se ausente.
  if [ -n "${HOME:-}" ] && [ -d "$HOME/scoop/apps/mingw/current/bin" ]; then
    case ":$PATH:" in *":$HOME/scoop/apps/mingw/current/bin:"*) ;;
      *) PATH="$HOME/scoop/apps/mingw/current/bin:$PATH";; esac
  fi
  if [ -n "${HOME:-}" ] && [ -d "$HOME/scoop/shims" ]; then
    case ":$PATH:" in *":$HOME/scoop/shims:"*) ;;
      *) PATH="$HOME/scoop/shims:$PATH";; esac
  fi
  export PATH
}

# --- Descoberta de módulos Go -------------------------------------------------
# Ecoa os directórios de módulo (dir que contém go.mod), rel. a REPO_ROOT,
# ordenados. Descoberto dinamicamente — SEM hardcode frágil.
#
# AOS_EXTRA_GATE_MODULES (opt-in, separado por espaços): módulos que SHIPAM mas vivem FORA de
# packages/ (ex.: deploy/node/healthprobe, empacotado na imagem — AOS-168). A cadeia de entrega
# (scripts/ci/package.sh) define-o para estender a fronteira do gate a tudo o que shipa; a CI
# global NÃO o define, pelo que o varrimento por-módulo default fica inalterado.
discover_modules() {
  {
    ( cd "$REPO_ROOT" && find packages -name go.mod -print | sed 's#/go.mod$##' )
    if [ -n "${AOS_EXTRA_GATE_MODULES:-}" ]; then
      printf '%s\n' ${AOS_EXTRA_GATE_MODULES}
    fi
# O VERIFICADOR DA PRÓPRIA CADEIA ENTRA SEMPRE, e não pelo knob opt-in acima.
#
# `scripts/ci/attest` é o binário que RECUSA um envelope DSSE inválido — a única peça da entrega
# cuja função é dizer «não». Vive fora de `packages/`, pelo que a descoberta nunca o via, e os
# únicos usos dele em toda a cadeia são `go build` (sign.sh, verify-attestation.sh), nunca
# `go test`.
#
# CONSEQUÊNCIA MEDIDA a 2026-08-21: neutralizar a verificação da assinatura (a) É apanhado pelos
# 12 controlos negativos do módulo — não são vacuosos; (b) COMPILA, que é tudo o que a cadeia
# fazia; (c) o binário mutado aceita uma falsificação total contra o roster REAL de produção, com
# assinatura de 64 bytes de zeros. Ou seja: uma alteração que transformava o verificador de
# release num carimbo passava os 26 gates.
#
# Não entra por `AOS_EXTRA_GATE_MODULES` de propósito: esse knob é para módulos que SHIPAM e a CI
# global deliberadamente não o define. Este não shipa — guarda o que shipa —, e tem de correr
# SEMPRE, incluindo na CI global.
    if [ -f "$REPO_ROOT/scripts/ci/attest/go.mod" ]; then
      printf '%s\n' "scripts/ci/attest"
    fi
  } | sort
}

# --- Versão REAL de um binário Go ---------------------------------------------
# tool_module_version <caminho-do-binário> — devolve a versão do MÓDULO principal
# gravada no binário por `go install` (linha `mod` de `go version -m`).
#
# PORQUE NÃO `<bin> --version`: um binário construído por `go install` não leva
# ldflags de release, pelo que várias ferramentas mentem — o gosec@v2.28.0 responde
# literalmente "Version: dev". Os metadados de módulo, ao contrário, são gravados
# sempre e são a versão VERDADEIRA. Vazio ⇒ não determinável (binário não-Go, ou
# construído de outra forma).
tool_module_version() {
  go version -m "$1" 2>/dev/null | awk '$1=="mod"{print $3; exit}'
}

# --- Instalação idempotente de ferramentas (go install pinado) ----------------
# ensure_tool <binário> <pin>. Instala em $(go env GOPATH)/bin; não committa binários.
#
# VERIFICA A VERSÃO CONTRA O PIN — e é essa a razão de ser desta função hoje. A versão
# anterior devolvia 0 assim que `command -v` encontrasse QUALQUER binário com o nome
# certo, sem nunca comparar com o pin. Consequência real (observada nesta base de código
# em 2026-08-11): uma máquina com um `gosec`/`staticcheck` pré-instalado corria a versão
# ERRADA em silêncio, via VERDE localmente, e a CI — que instalava o pin de raiz — via
# VERMELHO com achados que o local nem detectava. Um pin que não é verificado não é um
# pin: é uma sugestão. Perde-se mais tempo a perseguir a divergência do que a reinstalar.
#
# FAIL-CLOSED em três pontos: versão diferente ⇒ reinstala; instalação falhada ⇒ erro;
# e — o caso que mais engana — se DEPOIS de instalar o binário resolvido pelo PATH
# continuar na versão errada, ABORTA em vez de correr o gate com a ferramenta errada
# (é o sintoma de outro binário com o mesmo nome à frente no PATH).
ensure_tool() {
  local bin="$1" pin="$2"
  local want="${pin##*@}"
  if command -v "$bin" >/dev/null 2>&1; then
    local got; got="$(tool_module_version "$(command -v "$bin")")"
    if [ "$got" = "$want" ]; then
      log_step "ferramenta presente: $bin ($got, casa o pin)"
      return 0
    fi
    log_step "ferramenta $bin está em ${got:-versão indeterminada} mas o pin é $want — reinstalar"
  fi
  log_step "instalar $bin ($pin) ..."
  GOFLAGS="" go install "$pin"
  command -v "$bin" >/dev/null 2>&1 || { log_fail "instalação de $bin falhou"; return 1; }
  # PÓS-CHECK: o que o PATH resolve TEM de ser o que se instalou. Se não for, há outro
  # binário com o mesmo nome à frente de $(go env GOPATH)/bin — e o gate correria com a
  # ferramenta errada, exactamente o silêncio que esta função existe para eliminar.
  local now; now="$(tool_module_version "$(command -v "$bin")")"
  if [ "$now" != "$want" ]; then
    log_fail "$bin: instalou-se $pin mas o PATH resolve $(command -v "$bin") em ${now:-versão indeterminada} — outro binário com o mesmo nome está à frente de \$(go env GOPATH)/bin. Corrija o PATH: correr o gate com outra versão dá resultados diferentes dos da CI."
    return 1
  fi
}

# --- Normalização de caminhos -------------------------------------------------
# Torna um caminho relativo à raiz do repo com '/' e sem prefixo de drive.
# Mantém apenas de 'packages/' em diante (todos os módulos vivem sob packages/).
norm_path() {
  # stdin -> stdout ; converte '\' -> '/' e recorta a partir de packages/
  sed -e 's#\\#/#g' -e 's#.*/packages/#packages/#'
}

# --- Remoção de códigos de cor ANSI ------------------------------------------
# Algumas ferramentas (gosec) colorizam mesmo em -fmt=text. Remove as sequências
# de escape para uma normalização estável. stdin -> stdout.
strip_ansi() { sed 's/\x1b\[[0-9;]*m//g'; }

# --- Comparação com baseline (semântica de multiconjunto) ---------------------
# baseline_normalize <ficheiro_baseline>
#   Produz em stdout o ficheiro baseline limpo, ignorando linhas em branco e
#   comentários (linhas que começam por '#' ou ';', e parte após '#' numa linha
#   de dados). Permite documentar cada entrada com dono/remediação sem alterar
#   a semântica de comparação.
baseline_normalize() {
  local base="$1"
  if [ ! -f "$base" ]; then
    return
  fi
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      ''|\#*|\;*) continue ;;
    esac
    # Remover comentário inline (primeiro '#' não escapado) e espaços.
    key="${line%%#*}"
    key="${key%;*}"
    key="${key#"${key%%[![:space:]]*}"}"   # ltrim
    key="${key%"${key##*[![:space:]]}"}"   # rtrim
    [ -n "$key" ] || continue
    printf '%s\n' "$key"
  done < "$base"
}

# baseline_diff <ficheiro_actual> <ficheiro_baseline>
#   Imprime em stdout as descobertas NOVAS (presentes no actual e não cobertas
#   pela baseline, contando duplicados). Devolve 1 se houver novas, 0 caso
#   contrário. Descobertas obsoletas (na baseline e já ausentes) só geram WARN.
baseline_diff() {
  local cur="$1" base="$2"
  # Baseline ausente == toda a descoberta é NOVA (compara contra vazio).
  local base_eff; base_eff="$(mktemp)"
  baseline_normalize "$base" > "$base_eff"
  if [ ! -s "$base_eff" ]; then
    rm -f "$base_eff"
    base_eff="/dev/null"
  fi

  local sc sb; sc="$(mktemp)"; sb="$(mktemp)"
  sort "$cur" > "$sc"
  sort "$base_eff" > "$sb"

  local new stale
  new="$(comm -23 "$sc" "$sb" || true)"
  stale="$(comm -13 "$sc" "$sb" || true)"
  rm -f "$sc" "$sb" "$base_eff"

  if [ -n "$stale" ]; then
    log_warn "entradas de baseline obsoletas (já não ocorrem) em $(basename "$base"):"
    printf '%s\n' "$stale" | sed 's/^/       /' >&2
  fi
  if [ -n "$new" ]; then
    printf '%s\n' "$new"
    return 1
  fi
  return 0
}

# sca_decide <ficheiro_ids_afetantes> <ficheiro_baseline>
#   Igual a baseline_diff mas com mensagem específica de SCA. Reutilizado pelo
#   self-test determinista do SCA (injecta um CVE fictício não-baseline).
sca_decide() { baseline_diff "$1" "$2"; }

# require_tests <module_dir> <pkg_pattern> <run_regex> <test1> [test2 ...]
#   Corre 'go test <pkg> -run <regex> -v -count=1' e exige (a) que a suite passe e
#   (b) que CADA <testN> tenha EFECTIVAMENTE corrido (linha '--- PASS: testN').
#   Fecha o buraco de "vacuous pass" apanhado no audit: um -run que não casa
#   nenhum teste sai 0 sem correr nada, pelo que um gate baseado só no exit
#   passaria vazio se um teste crítico fosse renomeado/removido. Fail-closed.
require_tests() {
  local dir="$1" pkg="$2" re="$3"; shift 3
  local out rcx=0
  out="$( cd "$dir" && go test "$pkg" -run "$re" -v -count=1 2>&1 )" || rcx=$?
  if [ "$rcx" -ne 0 ]; then
    log_fail "testes vermelhos em $dir ($pkg -run '$re'):"
    printf '%s\n' "$out" | grep -E '^--- FAIL|^FAIL|panic:|no test files' | head -20 | sed 's/^/       /' >&2
    return 1
  fi
  local t missing=0
  for t in "$@"; do
    if ! printf '%s\n' "$out" | grep -qE "^--- PASS: ${t} "; then
      log_fail "teste OBRIGATÓRIO não correu (renomeado/removido/filtro vazio?): ${t}"
      missing=1
    fi
  done
  [ "$missing" -eq 0 ] && log_ok "$# testes obrigatórios correram e passaram em $(basename "$dir")"
  return "$missing"
}

# coverage_meets_min <pct> <min>
#   Predicado FAIL-CLOSED do gate de cobertura generalizado (AOS-109). Devolve 0
#   (verdadeiro) sse a percentagem `pct` (com ou sem '%') for numérica E >= `min`;
#   1 caso contrário — incluindo pct vazio, "FALHOU" ou "n/a" (uma cobertura
#   não-mensurável NÃO satisfaz o piso). É a MESMA função que o gate 3 usa e que o
#   self-test exercita directamente (prova de que uma descida abaixo do limiar
#   bloqueia). Determinista e offline.
coverage_meets_min() {
  local pct="$1" min="$2"
  local num="${pct%\%}"
  case "$pct" in ""|FALHOU|n/a) return 1 ;; esac
  # num TEM de ser numérico (inteiro ou decimal); qualquer outra coisa é fail-closed.
  case "$num" in
    ''|*[!0-9.]*) return 1 ;;
  esac
  awk "BEGIN{exit !($num >= $min)}"
}

# emit_lcov <cover_dir> <out_file>
#   Emite o relatório de cobertura MÁQUINA-LEGÍVEL (LCOV) de AOS-109 AC1 a partir
#   dos coverprofiles Go já gerados em <cover_dir> (*.out), via o conversor
#   cov2lcov (Go stdlib puro, ZERO deps, determinista). Escreve <out_file> criando
#   o directório. Fail-closed: devolve != 0 se não houver perfis ou se a conversão
#   falhar (um artefacto de cobertura ausente não satisfaz o AC).
emit_lcov() {
  local cover_dir="$1" out="$2"
  local profiles=("$cover_dir"/*.out)
  if [ ! -e "${profiles[0]}" ]; then
    log_fail "emit_lcov: nenhum coverprofile em $cover_dir"
    return 1
  fi
  mkdir -p "$(dirname "$out")"
  if ( cd "$TESTKIT_DIR" && go run ./cmd/cov2lcov "${profiles[@]}" ) > "$out"; then
    # Anti-vacuidade: um LCOV sem nenhum registo SF: (ex.: coverprofiles só com a
    # linha "mode:") é um artefacto VAZIO e não satisfaz o AC1 — fail-closed.
    if grep -q '^SF:' "$out"; then
      log_ok "cobertura máquina-legível (LCOV) emitida: $out"
      return 0
    fi
    log_fail "emit_lcov: LCOV emitido está vazio (nenhum registo SF:) — artefacto vacuoso"
    return 1
  fi
  log_fail "emit_lcov: conversão cov2lcov falhou"
  return 1
}

# tool_exec_failed <exit_code> <output> <ok_codes_csv> [error_regex]
#   Distingue "a ferramenta correu" de "falhou a executar", para scanners
#   (gosec/govulncheck/staticcheck) cujo exit != 0 significa TANTO "encontrou algo"
#   (legítimo, parseado à parte) COMO "não conseguiu correr" (rede/DB/toolchain).
#   Devolve 0 (verdadeiro) se a ferramenta FALHOU a executar => o chamador faz
#   fail-closed. ok_codes_csv = códigos que significam "correu" (ex.: "0,3"
#   govulncheck; "0,1" gosec/staticcheck).
tool_exec_failed() {
  local code="$1" out="$2" ok_csv="$3" err_re="${4:-}"
  case ",$ok_csv," in *",$code,"*) : ;; *) return 0 ;; esac
  [ -n "$err_re" ] && printf '%s\n' "$out" | grep -qiE "$err_re" && return 0
  return 1
}
