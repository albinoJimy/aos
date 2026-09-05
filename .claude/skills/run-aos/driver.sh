#!/usr/bin/env bash
# driver.sh — arranca e CONDUZ o nó `aos` (packages/cmd/aos) sem Docker e sem IaC.
#
# Porque existe: `aos serve` sem ambiente arranca DEGRADADO — sem operadores o canal de
# controlo recusa tudo, sem bundle o PDP nega toda a tool call, sem four-eyes o /approve
# responde 501. Compor o nó à mão são ~10 variáveis, duas delas DERIVADAS (a pubkey do
# operador vem de uma seed; o trust anchor da política é o base64 do repo convertido a hex).
# Este script fecha essa distância: `up` levanta um nó COMPOSTO, `smoke` prova-o ponta-a-ponta.
#
# Requer: Go >= 1.24, bash (Git Bash em Windows), curl. NÃO requer Docker, make, node nem jq.
# Uso: bash .claude/skills/run-aos/driver.sh <comando> [args]
set -uo pipefail

# --- localização -----------------------------------------------------------------------------
SKILL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SKILL_DIR/../../.." && pwd)"
HOME_DIR="${AOS_DRIVER_HOME:-${TMPDIR:-/tmp}/aos-driver}"
BIN_DIR="$HOME_DIR/bin"
STATE_DIR="$HOME_DIR/state"
PORT="${AOS_DRIVER_PORT:-18080}"
ADDR="http://127.0.0.1:$PORT"
LOG="$HOME_DIR/serve.log"
PIDFILE="$HOME_DIR/serve.pid"

# Credencial de leitura DEMO-GRADE do read-path soberano (AOS-172): o nó aceita
# X-Aos-Reader/X-Aos-Board só FORA de produção. "board:aos-demo" é o board por omissão
# que o binário semeia quando AOS_BOARD_REGIONS não está definida (ver main.go).
READER="${AOS_DRIVER_READER:-nhi:demo}"
BOARD="${AOS_DRIVER_BOARD:-board:aos-demo}"
POLICY_DIR="$REPO_ROOT/packages/control-plane/pdp/policies"

say()  { printf '\033[36m[driver]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[driver] FALHOU:\033[0m %s\n' "$*" >&2; exit 1; }
ok()   { printf '\033[32m[driver] OK\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[driver] AVISO:\033[0m %s\n' "$*" >&2; }

# hexdeseed — imprime a pubkey ed25519 (hex) de um ficheiro de seed.
hexdeseed() { "$BIN_DIR/aos" operator-pubkey --key "$1"; }

# --- build -----------------------------------------------------------------------------------
cmd_build() {
  mkdir -p "$BIN_DIR"
  local m
  for m in aos aos-issuer aos-demo aos-attestation aos-orq; do
    say "build $m"
    ( cd "$REPO_ROOT/packages/cmd/$m" && go build -o "$BIN_DIR/$m" . ) || fail "build de $m"
  done
  ok "binarios em $BIN_DIR"
}

# --- frescura dos binarios ---------------------------------------------------------------------
# O cmd_build escreve SEMPRE "$BIN_DIR/<nome>" (sem .exe), por isso e esse o unico caminho que
# conta: um aos.exe deixado por um `go build -o aos.exe .` a mao nao e o binario que o driver
# corre, e tolera-lo como "binario presente" era metade do buraco que se fecha aqui.
BINARIES="aos aos-issuer aos-demo aos-attestation aos-orq"

# newer_source <ref> — primeiro ficheiro de packages/ mais recente que <ref> (vazio = nenhum).
# Todos os modulos cmd/* usam `replace` para caminhos locais, logo uma mudanca em QUALQUER
# subarvore de packages/ entra nos binarios — a varredura nao pode limitar-se a packages/cmd.
# Exclui *_test.go e testdata: nao entram no binario, e forcar um relink de ~25s por um teste
# editado so ensinaria a gente a passar AOS_DRIVER_NO_BUILD=1, que e o buraco de volta.
newer_source() {
  find "$REPO_ROOT/packages" \
    \( -name vendor -o -name testdata -o -name node_modules \) -prune -o \
    \( \( -name '*.go' ! -name '*_test.go' \) -o -name go.mod -o -name go.sum \) \
    -newer "$1" -print -quit 2>/dev/null
}

# stale_reason — porque e que os binarios NAO servem de prova do codigo actual (vazio = servem).
# Compara com o binario MAIS ANTIGO: basta um dos cinco estar atrasado para o conjunto nao valer.
stale_reason() {
  local m oldest="" src
  for m in $BINARIES; do
    if [ ! -x "$BIN_DIR/$m" ]; then printf 'binario em falta: %s' "$m"; return 0; fi
    if [ -z "$oldest" ] || [ "$BIN_DIR/$m" -ot "$oldest" ]; then oldest="$BIN_DIR/$m"; fi
  done
  src="$(newer_source "$oldest")"
  [ -n "$src" ] && printf 'fonte mais recente que os binarios: %s' "${src#"$REPO_ROOT"/}"
  return 0
}

# bin_age — idade do `aos` em forma legivel. E o numero que denuncia um verde sobre codigo velho.
bin_age() {
  local mt now s
  mt="$(stat -c %Y "$BIN_DIR/aos" 2>/dev/null)" || { printf 'desconhecida'; return 0; }
  now="$(date +%s)"; s=$(( now - mt ))
  if   [ "$s" -lt 60 ];   then printf '%ds' "$s"
  elif [ "$s" -lt 3600 ]; then printf '%dm' "$(( s / 60 ))"
  else                         printf '%dh%02dm' "$(( s / 3600 ))" "$(( s % 3600 / 60 ))"; fi
}

# ensure_build — garante que os binarios em $BIN_DIR correspondem ao codigo em packages/.
#
# Ate aqui o `up` fazia `[ -x "$BIN_DIR/aos" ] || cmd_build`: construia so quando o binario NAO
# existia. Da segunda corrida em diante, `smoke` levantava o binario da corrida anterior e dava
# 9/9 VERDE sem exercitar uma linha do codigo alterado. MEDIDO a 2026-09-05 na validacao da
# EPIC-23: smoke verde, binario com sete horas (de antes do epic inteiro) e nenhum dos banners
# de postura novos (AOS-322, AOS-326) no serve.log; com `build` forcado, o mesmo smoke deu 9/9
# E os tres banners apareceram. E grave porque a skill agentic-engineering §7 nomeia o smoke
# como a unica evidencia que conta para mudancas no wiring, nos banners de arranque e na
# superficie HTTP — exactamente as que os testes unitarios nao apanham, e por isso exactamente
# aquelas em que um binario obsoleto passa despercebido com tudo verde por cima de codigo velho.
#
# A varredura de frescura custa ~3s e o relink dos cinco binarios ~25s, por isso so se
# reconstroi quando ha fonte mais recente — mas nunca se reutiliza em SILENCIO.
ensure_build() {
  if [ "${AOS_DRIVER_NO_BUILD:-0}" = "1" ]; then
    warn "AOS_DRIVER_NO_BUILD=1 — binarios NAO verificados (idade do aos: $(bin_age))."
    warn "um verde a partir daqui NAO e prova sobre o codigo actual."
    return 0
  fi
  local why
  why="$(stale_reason)"
  if [ -n "$why" ] || [ "${AOS_DRIVER_ALWAYS_BUILD:-0}" = "1" ]; then
    say "a reconstruir — ${why:-AOS_DRIVER_ALWAYS_BUILD=1}"
    cmd_build
    # Pos-condicao: se ficou fonte mais recente, ou o build nao escreveu o que devia, ou ha um
    # ficheiro com mtime no futuro. Em qualquer dos casos o verde seguinte nao valeria nada.
    why="$(stale_reason)"
    [ -z "$why" ] && return 0
    fail "binarios ainda obsoletos apos o build ($why) — o build nao escreveu, ou ha fonte com mtime no futuro"
  fi
  say "binarios frescos (aos com $(bin_age); nada mais recente em packages/)"
}

cmd_freshness() {
  local m why
  for m in $BINARIES; do
    if [ -x "$BIN_DIR/$m" ]; then
      printf '  %-16s %s\n' "$m" "$(stat -c %y "$BIN_DIR/$m" 2>/dev/null || echo 'mtime desconhecido')"
    else
      printf '  %-16s AUSENTE\n' "$m"
    fi
  done
  why="$(stale_reason)"
  [ -n "$why" ] && fail "OBSOLETOS: $why"
  ok "frescos (aos com $(bin_age))"
}

# cmd_freshness_selftest — a assercao que impede a regressao de voltar em silencio.
# Envelhece o PROPRIO binario (mtime a 2000-01-01) em vez de tocar no repo, corre ensure_build e
# exige que ele tenha reconstruido. Com o antigo `[ -x "$BIN_DIR/aos" ] || cmd_build` este teste
# falha no penultimo passo — que e precisamente o ponto.
cmd_freshness_selftest() {
  [ "${AOS_DRIVER_NO_BUILD:-0}" = "1" ] && fail "AOS_DRIVER_NO_BUILD=1 desliga exactamente o que este auto-teste verifica"
  [ -x "$BIN_DIR/aos" ] || cmd_build
  local before after
  touch -d '2000-01-01 00:00:00' "$BIN_DIR/aos" || fail "nao consegui envelhecer $BIN_DIR/aos"
  before="$(stat -c %Y "$BIN_DIR/aos" 2>/dev/null)"
  # Sem um stat GNU nao ha comparacao de mtime e este auto-teste daria um verde vazio.
  case "$before" in ''|*[!0-9]*) fail "stat -c %Y indisponivel — este driver assume coreutils/findutils GNU (Git Bash, Linux)" ;; esac
  [ -n "$(stale_reason)" ] || fail "stale_reason nao viu um binario datado de 2000-01-01 — a deteccao esta partida"
  say "binario envelhecido para 2000-01-01; ensure_build tem de reconstruir"
  ensure_build
  after="$(stat -c %Y "$BIN_DIR/aos")"
  [ "$after" -gt "$before" ] || fail "ensure_build REUTILIZOU o binario obsoleto (mtime inalterado) — a regressao voltou"
  [ -z "$(stale_reason)" ] || fail "apos ensure_build os binarios continuam obsoletos"
  ok "frescura garantida: um binario obsoleto forca reconstrucao"
}

# --- chaves ----------------------------------------------------------------------------------
# As seeds são 32 bytes em HEX, texto simples UTF-8 SEM BOM. O ">" do PowerShell escreve BOM
# ou UTF-16 e o binário recusa com "nao e hex" / ErrSeedUTF16 — por isso escrevem-se em bash.
cmd_keys() {
  mkdir -p "$HOME_DIR"
  local n op op2 p1 p2
  # DOIS operadores (AOS-305): mudar a autonomia PARA L4/L5 exige duas assinaturas de emissores
  # distintos com autonomy:set. O smoke promove agt-1:fs a L5, logo precisa dos dois.
  for n in operador operador2 ap1 ap2; do
    [ -s "$HOME_DIR/$n.seed" ] && continue
    head -c 32 /dev/urandom | xxd -p | tr -d '\n' > "$HOME_DIR/$n.seed"
  done
  op="$(hexdeseed "$HOME_DIR/operador.seed")" || fail "derivar pubkey do operador (corre build primeiro)"
  op2="$(hexdeseed "$HOME_DIR/operador2.seed")"
  p1="$(hexdeseed "$HOME_DIR/ap1.seed")"
  p2="$(hexdeseed "$HOME_DIR/ap2.seed")"
  cat > "$HOME_DIR/approvers.json" <<EOF
{"approvers":[
 {"principal":"human:ana","pubkey":"$p1","authority":["approve:safe","approve:gray","approve:danger"]},
 {"principal":"human:bruno","pubkey":"$p2","authority":["approve:safe","approve:gray","approve:danger"]}
]}
EOF
  printf 'op:jimy=%s,op:maria=%s\n' "$op" "$op2" > "$HOME_DIR/operators.env"
  ok "seeds + approvers.json em $HOME_DIR (operadores op:jimy=$op op:maria=$op2)"
}

# trust anchor da política: o repo guarda a pubkey em BASE64 (trust_anchor.pub) e o nó
# exige-a em HEX (AOS_POLICY_TRUST_ANCHOR, 64 chars). A conversão é aqui, não à mão.
anchor_hex() { tr -d '\r\n' < "$POLICY_DIR/trust_anchor.pub" | base64 -d | xxd -p | tr -d '\n'; }

# --- up / down -------------------------------------------------------------------------------
cmd_up() {
  ensure_build
  # Regenera tambem quando o roster de operadores mudou de forma (AOS-305 passou a exigir DOIS
  # operadores): um operators.env em cache com um so id faz o no abortar com ErrBadAutonomySetters,
  # e o sintoma — "o no nao ficou pronto" — nao aponta para o ficheiro velho.
  { [ -s "$HOME_DIR/approvers.json" ] && grep -q 'op:maria' "$HOME_DIR/operators.env" 2>/dev/null; } || cmd_keys
  cmd_down >/dev/null 2>&1
  mkdir -p "$STATE_DIR"
  local anchor i
  anchor="$(anchor_hex)"
  [ "${#anchor}" -eq 64 ] || fail "trust anchor com ${#anchor} chars (esperado 64)"

  AOS_API_ADDR="127.0.0.1:$PORT" \
  AOS_OPERATORS="$(cat "$HOME_DIR/operators.env")" \
  AOS_AUTONOMY_SETTERS="op:jimy,op:maria" \
  AOS_APPROVERS_FILE="$HOME_DIR/approvers.json" \
  AOS_DURABLE_EXECUTION=1 \
  AOS_EVENTSTORE_PATH="$STATE_DIR/es.wal" \
  AOS_WORM_PATH="$STATE_DIR/worm.log" \
  AOS_POLICY_BUNDLE_DIR="$POLICY_DIR" \
  AOS_POLICY_TRUST_ANCHOR="$anchor" \
  AOS_AUTONOMY_LEVELS="${AOS_DRIVER_AUTONOMY:-agt-1:fs=L4,agt-1:http=L2}" \
  AOS_AUTONOMY_DEFAULT="L1" \
  "$BIN_DIR/aos" serve > "$LOG" 2>&1 &
  echo $! > "$PIDFILE"

  for i in $(seq 1 40); do
    if [ "$(curl -s -m 2 -o /dev/null -w '%{http_code}' "$ADDR/readyz" 2>/dev/null)" = "200" ]; then
      ok "no pronto em $ADDR (log: $LOG, estado: $STATE_DIR)"
      return 0
    fi
    sleep 0.5
  done
  tail -25 "$LOG" >&2
  fail "o no nao ficou pronto em 20s"
}

cmd_down() {
  if [ -f "$PIDFILE" ]; then kill "$(cat "$PIDFILE")" 2>/dev/null; rm -f "$PIDFILE"; fi
  # Fallback Windows: o pid do job de bash nem sempre mata o processo nativo. O nome da
  # imagem e "aos" (sem .exe) porque o build usa `go build -o .../aos` — mata-se os dois,
  # porque quem compilou a mao com `go build -o aos.exe .` tem a outra.
  if command -v taskkill >/dev/null 2>&1; then
    taskkill //F //IM aos >/dev/null 2>&1
    taskkill //F //IM aos.exe >/dev/null 2>&1
  fi
  pkill -f "aos serve" 2>/dev/null
  say "no parado"
  return 0
}

cmd_logs() {
  [ -f "$LOG" ] || fail "sem log ($LOG)"
  if [ "${1:-}" = "-f" ]; then tail -f "$LOG"; else cat "$LOG"; fi
}

cmd_banner() {
  grep -E 'four-eyes|mediacao de politica|autonomia / escalate|soberania de leitura|execucao duravel|ingresso / admission' "$LOG"
}

# --- API -------------------------------------------------------------------------------------
# Toda a leitura atravessa o read-path soberano: sem os dois headers e 403 "nao autorizado".
api() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -m 15 -X "$method" -H 'Content-Type: application/json' \
      -H "X-Aos-Reader: $READER" -H "X-Aos-Board: $BOARD" --data-binary "$body" "$ADDR$path"
  else
    curl -s -m 15 -X "$method" -H "X-Aos-Reader: $READER" -H "X-Aos-Board: $BOARD" "$ADDR$path"
  fi
}
cmd_api() { api "$@"; echo; }

cmd_status() {
  printf 'healthz: %s\n' "$(curl -s -m 5 "$ADDR/healthz")"
  printf 'readyz : %s\n' "$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$ADDR/readyz")"
  curl -s -m 5 "$ADDR/metrics" | grep -E '^aos_(up|ready|draining|eventstore_healthy|goroutines) '
}

# --- CLI do no -------------------------------------------------------------------------------
# --run-id e EXIGIDO (sem ele o submit e 400); --reader/--board sao exigidos pela soberania.
cmd_run()     { "$BIN_DIR/aos" run --addr "$ADDR" --run-id "$1" --objective "${2:-objectivo de teste}" --reader "$READER" --board "$BOARD"; }
cmd_observe() { "$BIN_DIR/aos" observe --addr "$ADDR" --run-id "$1" --reader "$READER" --board "$BOARD"; }
cmd_pause()   { "$BIN_DIR/aos" pause --addr "$ADDR" --run-id "$1" --emitter op:jimy --key "$HOME_DIR/operador.seed"; }
cmd_steer()   { "$BIN_DIR/aos" steer --addr "$ADDR" --run-id "$1" --emitter op:jimy --key "$HOME_DIR/operador.seed" --correction "${2:-corrige o rumo}"; }

# SSE da trajectoria: o stream NUNCA fecha do lado do no, por isso corre com prazo e devolve
# as primeiras N linhas. O curl sai SEMPRE 28 (timeout) ou 23 (SIGPIPE do head) — e a
# terminacao NORMAL aqui, e sem o "|| true" o pipefail do script transformava-a num falso erro.
cmd_trajectory() {
  { curl -s -N -m "${2:-4}" -H "X-Aos-Reader: $READER" -H "X-Aos-Board: $BOARD" \
      "$ADDR/runs/$1/trajectory" || true; } | head -"${3:-20}"
}

# --- autonomia (assinada + selada) -----------------------------------------------------------
# O corpo e produzido FORA do no pelo aos-issuer: a chave privada nunca entra no processo.
# L4/L5 levam a SEGUNDA assinatura (op:maria) — AOS-305: remover a supervisao e de duas pessoas.
cmd_autonomy_set() {
  local body co=()
  case "$3" in L4|L5|l4|l5) co=(--co-emitter op:maria --co-key-file "$HOME_DIR/operador2.seed") ;; esac
  body="$("$BIN_DIR/aos-issuer" autonomy-sign --emitter op:jimy --key-file "$HOME_DIR/operador.seed" \
            --agent "$1" --domain "$2" --level "$3" --reason "${4:-driver}" "${co[@]}")" || fail "autonomy-sign"
  api POST /autonomy "$body"
  echo
}
cmd_autonomy_get() { api GET /autonomy; echo; }

# --- diagnostico read-only sobre os ficheiros ------------------------------------------------
cmd_wal()      { "$BIN_DIR/aos" wal-summary --path "$STATE_DIR/es.wal"; }
cmd_walcount() { "$BIN_DIR/aos" wal-count --path "$STATE_DIR/es.wal" --run "$1" --turns; }
# ATENCAO: "audit-trail --run" recebe uma PARTICAO do WORM, nao um run id. As particoes
# por-run chamam-se gov.read/<run>, gov.residency/<run>, ingestion:<run>; as de no
# chamam-se governance.control, autonomy, gov.sovereignty.authority.
cmd_worm()       { "$BIN_DIR/aos" audit-trail --path "$STATE_DIR/worm.log" --run "$@"; }
cmd_partitions() { strings "$STATE_DIR/worm.log" | grep -o '"Partition":"[^"]*"' | sed 's/.*:"//; s/"$//' | sort -u; }

# --- outros binarios -------------------------------------------------------------------------
cmd_demo()     { "$BIN_DIR/aos-demo"; }
cmd_selftest() { "$BIN_DIR/aos-attestation" selftest; }

# --- testes ----------------------------------------------------------------------------------
# Invocacao DIRECTA do codigo interno, que e onde a maioria dos PRs deste repo mexe.
cmd_test() {
  local mod="${1:-packages/cmd/aos}"
  ( cd "$REPO_ROOT/$mod" && go test -race -count=1 "${2:-./...}" )
}

# --- smoke ponta-a-ponta ---------------------------------------------------------------------
# NOTA de implementacao: as assercoes NUNCA sao "cmd | grep -q". Com "set -o pipefail" o
# grep -q fecha o pipe assim que casa, o produtor apanha SIGPIPE (141) e o pipeline devolve
# 141 mesmo com o teste a passar — falso vermelho intermitente. Captura-se para variavel.
tem() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }

cmd_smoke() {
  local rid="smoke-$$" r why
  cmd_up || exit 1

  # Pre-voo: cmd_up so pode devolver controlo com binarios que correspondem ao codigo. Se esta
  # assercao falhar, alguem voltou a por o driver a reutilizar binarios e os nove passos abaixo
  # estariam a exercitar codigo velho — um verde que nao prova nada.
  why="$(stale_reason)"
  [ -z "$why" ] || fail "pre-voo: o no subiu com binarios obsoletos ($why) — se puseste AOS_DRIVER_NO_BUILD=1, tira-o: um verde assim nao e prova"
  say "pre-voo: binarios correspondem ao codigo (aos com $(bin_age))"

  say "1/9 submeter run $rid"
  r="$(cmd_run "$rid" "auditar o pipeline")"
  tem 'status=accepted' "$r" || fail "submit: $r"

  say "2/9 observar ate completar"
  local out="" i
  for i in $(seq 1 20); do
    out="$(cmd_observe "$rid")"
    case "$out" in *"status=completed"*) break ;; esac
    sleep 0.5
  done
  case "$out" in
    *"status=completed"*) ok "$out" ;;
    *) fail "run nao completou: $out" ;;
  esac

  say "3/9 read-path soberano NEGA sem credencial (leitura 404, escrita 403)"
  local code
  # A leitura nega com 404 "not found" — ANTI-ENUMERACAO: um 403 revelaria que o run existe.
  code="$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$ADDR/runs/$rid")"
  [ "$code" = "404" ] || fail "GET sem headers: esperado 404, veio $code"
  # A escrita nega com 403 "nao autorizado" — nao ha nada a esconder num id ainda inexistente.
  code="$(curl -s -m 5 -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
            --data-binary '{"run_id":"x","objective":"x"}' "$ADDR/runs")"
  [ "$code" = "403" ] || fail "POST sem headers: esperado 403, veio $code"
  ok "404 na leitura, 403 na escrita"

  say "4/9 canal de controlo assinado (pause + steer)"
  r="$(cmd_pause "$rid")";                tem 'pause enviado' "$r" || fail "pause: $r"
  r="$(cmd_steer "$rid" "muda de rumo")"; tem 'steer enviado' "$r" || fail "steer: $r"
  ok "pause + steer aceites"

  say "5/9 canal de controlo RECUSA emissor nao pinado"
  if "$BIN_DIR/aos" pause --addr "$ADDR" --run-id "$rid" --emitter op:intruso \
       --key "$HOME_DIR/ap1.seed" >/dev/null 2>&1; then
    fail "um emissor nao pinado foi aceite"
  fi
  ok "emissor nao pinado recusado"

  say "6/9 autonomia assinada (POST /autonomy) e leitura"
  r="$(cmd_autonomy_set agt-1 fs L5 smoke)"; tem '"status":"applied"' "$r" || fail "autonomy set: $r"
  r="$(cmd_autonomy_get)";                   tem '"level":"L5"' "$r"       || fail "autonomy get: $r"
  ok "agt-1:fs L4 -> L5 aplicado e selado"

  say "7/9 SSE da trajectoria"
  r="$(cmd_trajectory "$rid" 4 6)"; tem 'run.state.transition' "$r" || fail "SSE sem eventos"
  ok "trajectoria a emitir"

  say "8/9 substrato duravel (WAL) e atribuicao selada (WORM)"
  r="$(cmd_wal)";                     tem 'turn.recorded' "$r" || fail "WAL sem turn.recorded: $r"
  r="$(cmd_worm governance.control)"; tem 'control:pause' "$r"  || fail "WORM sem selo de pause: $r"
  ok "WAL e WORM coerentes"

  say "9/9 metricas Prometheus"
  r="$(curl -s -m 5 "$ADDR/metrics")"; tem 'aos_ready 1' "$r" || fail "metricas"
  ok "aos_ready 1"

  cmd_down
  ok "SMOKE VERDE (run=$rid)"
}

# --- despacho --------------------------------------------------------------------------------
case "${1:-help}" in
  build)              cmd_build ;;
  freshness)          cmd_freshness ;;
  freshness-selftest) cmd_freshness_selftest ;;
  keys)         cmd_keys ;;
  up)           cmd_up ;;
  down)         cmd_down ;;
  logs)         shift; cmd_logs "$@" ;;
  banner)       cmd_banner ;;
  status)       cmd_status ;;
  api)          shift; cmd_api "$@" ;;
  run)          shift; cmd_run "$@" ;;
  observe)      shift; cmd_observe "$@" ;;
  pause)        shift; cmd_pause "$@" ;;
  steer)        shift; cmd_steer "$@" ;;
  trajectory)   shift; cmd_trajectory "$@" ;;
  autonomy-set) shift; cmd_autonomy_set "$@" ;;
  autonomy-get) cmd_autonomy_get ;;
  wal)          cmd_wal ;;
  wal-count)    shift; cmd_walcount "$@" ;;
  worm)         shift; cmd_worm "$@" ;;
  partitions)   cmd_partitions ;;
  demo)         cmd_demo ;;
  selftest)     cmd_selftest ;;
  test)         shift; cmd_test "$@" ;;
  smoke)        cmd_smoke ;;
  home)         echo "$HOME_DIR" ;;
  help|*)
    cat <<'EOF'
driver.sh — arranca e conduz o no `aos`

  build                       compila aos, aos-issuer, aos-demo, aos-attestation, aos-orq
  freshness                   mtime dos binarios; sai 1 se algum ficou atras de packages/
  freshness-selftest          prova que um binario obsoleto forca reconstrucao (anti-regressao)
  keys                        gera seeds (2 operadores + 2 aprovadores) e approvers.json
  up                          levanta o no COMPOSTO (PDP assinado + four-eyes + autonomia + WAL/WORM)
  down                        para o no
  status                      healthz / readyz / metricas-chave
  banner                      as linhas de postura do arranque (o que ficou composto)
  logs [-f]                   log do no

  run <run-id> [objectivo]    submete um run (POST /runs)
  observe <run-id>            estado do run (GET /runs/{id})
  pause <run-id>              pause assinado (canal de controlo)
  steer <run-id> [correccao]  steer assinado
  trajectory <run-id> [s] [n] SSE dos eventos da trajectoria
  autonomy-set <ag> <dom> <L> muda o nivel de autonomia (assinado pelo aos-issuer, selado no WORM)
  autonomy-get                niveis em vigor
  api <METODO> <rota> [corpo] curl cru com os headers de leitura soberana

  wal                         wal-summary do Event Store duravel
  wal-count <run-id>          turnos duraveis de um run
  partitions                  particoes existentes no WORM
  worm <particao> [--denied-only]   trilha selada de uma PARTICAO (nao e um run id)

  demo                        aos-demo: apice minimo single-process, zero-rede
  selftest                    aos-attestation selftest
  test [modulo] [pkgs]        go test -race (default: packages/cmd/aos ./...)
  smoke                       ponta-a-ponta com assercoes (up -> 9 passos -> down)
  home                        directorio de trabalho do driver

`up` (e portanto `smoke`) reconstroi sempre que ha fonte em packages/ mais recente que os
binarios, e nunca reutiliza um binario sem dizer a idade dele — um verde nao corre em silencio
sobre codigo velho. AOS_DRIVER_ALWAYS_BUILD=1 forca o build; AOS_DRIVER_NO_BUILD=1 salta a
verificacao com aviso (e faz o `smoke` recusar-se a dar verde).
EOF
    ;;
esac
