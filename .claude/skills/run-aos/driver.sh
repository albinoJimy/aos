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

# --- chaves ----------------------------------------------------------------------------------
# As seeds são 32 bytes em HEX, texto simples UTF-8 SEM BOM. O ">" do PowerShell escreve BOM
# ou UTF-16 e o binário recusa com "nao e hex" / ErrSeedUTF16 — por isso escrevem-se em bash.
cmd_keys() {
  mkdir -p "$HOME_DIR"
  local n op p1 p2
  for n in operador ap1 ap2; do
    [ -s "$HOME_DIR/$n.seed" ] && continue
    head -c 32 /dev/urandom | xxd -p | tr -d '\n' > "$HOME_DIR/$n.seed"
  done
  op="$(hexdeseed "$HOME_DIR/operador.seed")" || fail "derivar pubkey do operador (corre build primeiro)"
  p1="$(hexdeseed "$HOME_DIR/ap1.seed")"
  p2="$(hexdeseed "$HOME_DIR/ap2.seed")"
  cat > "$HOME_DIR/approvers.json" <<EOF
{"approvers":[
 {"principal":"human:ana","pubkey":"$p1","authority":["approve:safe","approve:gray","approve:danger"]},
 {"principal":"human:bruno","pubkey":"$p2","authority":["approve:safe","approve:gray","approve:danger"]}
]}
EOF
  printf 'op:jimy=%s\n' "$op" > "$HOME_DIR/operators.env"
  ok "seeds + approvers.json em $HOME_DIR (operador op:jimy=$op)"
}

# trust anchor da política: o repo guarda a pubkey em BASE64 (trust_anchor.pub) e o nó
# exige-a em HEX (AOS_POLICY_TRUST_ANCHOR, 64 chars). A conversão é aqui, não à mão.
anchor_hex() { tr -d '\r\n' < "$POLICY_DIR/trust_anchor.pub" | base64 -d | xxd -p | tr -d '\n'; }

# --- up / down -------------------------------------------------------------------------------
cmd_up() {
  [ -x "$BIN_DIR/aos" ] || [ -x "$BIN_DIR/aos.exe" ] || cmd_build
  [ -s "$HOME_DIR/approvers.json" ] || cmd_keys
  cmd_down >/dev/null 2>&1
  mkdir -p "$STATE_DIR"
  local anchor i
  anchor="$(anchor_hex)"
  [ "${#anchor}" -eq 64 ] || fail "trust anchor com ${#anchor} chars (esperado 64)"

  AOS_API_ADDR="127.0.0.1:$PORT" \
  AOS_OPERATORS="$(cat "$HOME_DIR/operators.env")" \
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
cmd_autonomy_set() {
  local body
  body="$("$BIN_DIR/aos-issuer" autonomy-sign --emitter op:jimy --key-file "$HOME_DIR/operador.seed" \
            --agent "$1" --domain "$2" --level "$3" --reason "${4:-driver}")" || fail "autonomy-sign"
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
  local rid="smoke-$$" r
  cmd_up || exit 1

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
  build)        cmd_build ;;
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
  keys                        gera seeds (operador + 2 aprovadores) e approvers.json
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
EOF
    ;;
esac
