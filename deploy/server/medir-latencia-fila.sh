#!/usr/bin/env bash
# medir-latencia-fila.sh — FASE 0 do AOS-447: mede, em produção, quanto um pedido de plano espera
# para COMEÇAR e quanto a drenagem VAZA — antes e depois de mudar a forma do trabalhador.
#
#   como aos, no servidor:
#     bash /opt/aos/medir-latencia-fila.sh               submete UM pedido de prova, mede o arranque e lê os logs
#     bash /opt/aos/medir-latencia-fila.sh --ate-ao-fim  idem, e espera também pelo fim do plano
#     bash /opt/aos/medir-latencia-fila.sh --so-logs     não submete nada: só lê o log da drenagem e as métricas
#
# ─── O PEDIDO DE PROVA ──────────────────────────────────────────────────────────────────────
# run_id `plan-e2e-447-<epoch>` e um objectivo inócuo — ler o documento 'notes' com a tool doc_read,
# o mesmo da prova do AOS-437. Passa pela fila REAL e por nada mais: custa uma decomposição do modelo
# e um run do nó, como qualquer plano, e fica no `GET /plans/<id>` como qualquer outro. Mede o
# sistema tal como está: um pedido atrás de outros espera também por eles.
#
# ─── A CREDENCIAL ───────────────────────────────────────────────────────────────────────────
# Um token client_credentials do `aos-reader` POR CHAMADA (o nó recusa um `jti` repetido), pedido
# de DENTRO de um contentor na rede `aos_default` — o nó e o IdP só respondem por ela. O segredo
# lê-se de ${AOS_DIR}/secrets/reader-client-secret, montado só-leitura; nunca vai para a linha de
# comando do host nem para o output. O IdP valida-se pela CA interna (sem -k). O Bearer também não vai
# no argv do curl: escreve-se num ficheiro do tmpfs do contentor e passa por `-H @ficheiro`.
#
# A IMAGEM É FIXADA POR DIGEST e corre com --pull=never, como o alpine dos outros scripts: recebe
# montado o segredo do `aos-reader` — a credencial do aos-orq para o nó —, e uma etiqueta mutável de
# terceiros puxada em produção entregava esse segredo ao que a etiqueta apontasse nesse dia. O dono
# puxa-a UMA vez pelo digest (`docker pull ${CURL_IMG}`; README §A forma do trabalhador).
#
# ─── O QUE A MEDIÇÃO SUJA ───────────────────────────────────────────────────────────────────
# Submete com a identidade do `aos-reader` (o service account do caminho do plano), e o pedido de
# prova é um plano verdadeiro: conta nas métricas do consume, no log da drenagem, nos avisos do
# AOS-445 (chega um aviso ao operador) e no histórico do `GET /plans`; e cada sondagem pede um token
# novo ao IdP (~1 por segundo durante a espera). Não o corra em ciclo.
#
# ─── O QUE MEDE ─────────────────────────────────────────────────────────────────────────────
#   arranque   t(o nó deixa de dizer `pending`) − t(201 do POST /plans), sondando o GET /plans/<id>
#              de ~1 s em ~1 s (1 s de resolução, mais o tempo de pedir cada token). É a latência que
#              o timer impõe: até OnUnitInactiveSec + a passagem em curso.
#   S          a duração de uma passagem VAZIA da drenagem, do «drenagem a começar» ao «drenagem
#              terminada» no drenar-planos.log (não conta o arranque da unidade nem a leitura do NHI,
#              que vêm antes da primeira linha). É o custo fixo de cada tique do timer.
#   intervalo  do fim de uma passagem ao começo da seguinte — o OnUnitInactiveSec, visto de fora.
#   vazão      desfechos por hora na janela que o log cobre, e a duração de cada plano (duracao_s),
#              e os contadores acumulados de ${AOS_DIR}/logs/aos-orq-consume.prom.
#
# Não escreve nada fora do stdout; não mexe no timer, na fila nem no nó além do pedido de prova.
set -uo pipefail

AOS_DIR="${AOS_DIR:-/opt/aos}"
LOG_DIR="${DRENAR_LOG_DIR:-${AOS_DIR}/logs}"
DRENAR_LOG="${LOG_DIR}/drenar-planos.log"
METRICAS="${LOG_DIR}/aos-orq-consume.prom"
REDE="${MEDIR_REDE:-aos_default}"
# curlimages/curl 8.11.1, índice multi-arquitectura (docker buildx imagetools inspect, 2026-09-26).
CURL_IMG="${MEDIR_CURL_IMG:-curlimages/curl@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69}"
SEGREDO="${AOS_DIR}/secrets/reader-client-secret"
CA="${AOS_DIR}/tls-internal/ca-bundle.crt"
ESPERA_S="${MEDIR_ESPERA_S:-900}"
ESPERA_FIM_S="${MEDIR_ESPERA_FIM_S:-3600}"
OBJECTIVO="Le o documento 'notes' com a tool doc_read."

log()  { printf '[medir-fila] %s\n' "$1"; }
fail() { log "ERRO: $1"; exit 1; }

MODO="submeter"
case "${1:-}" in
  "")            ;;
  --ate-ao-fim)  MODO="ate-ao-fim" ;;
  --so-logs)     MODO="so-logs" ;;
  *)             fail "argumento desconhecido: $1 (uso: medir-latencia-fila.sh [--ate-ao-fim | --so-logs])" ;;
esac
[[ "${ESPERA_S}" =~ ^[1-9][0-9]{0,4}$ && "${ESPERA_FIM_S}" =~ ^[1-9][0-9]{0,4}$ ]] \
  || fail "MEDIR_ESPERA_S / MEDIR_ESPERA_FIM_S inválidos (segundos, inteiro)"

# valor_env <NOME> — o valor no .env do servidor (a última linha que o define), sem aspas.
valor_env() {
  sed -n "s/^$1=//p" "${AOS_DIR}/.env" 2>/dev/null | tail -n 1 | tr -d "\"'\r"
}

# ─── 1. o pedido de prova ───────────────────────────────────────────────────────────────────
submeter_e_medir() {
  local no idp run
  no="${MEDIR_NODE_URL:-$(valor_env AOS_ORQ_NODE_URL)}"; no="${no:-http://aos:8080}"
  idp="${MEDIR_IDP_TOKEN_URL:-$(valor_env AOS_ORQ_OIDC_TOKEN_URL)}"
  idp="${idp:-https://idp:8443/realms/aos/protocol/openid-connect/token}"
  [[ "${no}" =~ ^https?://[A-Za-z0-9._:-]+$ ]] || fail "URL do nó inválido: ${no}"
  [[ "${idp}" =~ ^https://[A-Za-z0-9._:/-]+$ ]] || fail "URL do token do IdP inválido: ${idp}"
  [[ -s "${SEGREDO}" ]] || fail "sem ${SEGREDO} — o segredo do aos-reader (provision-identity.sh)"
  [[ -s "${CA}" ]] || fail "sem ${CA} — a CA interna com que se valida o IdP"
  run="plan-e2e-447-$(date +%s)"
  log "pedido de prova ${run} → ${no}/plans (token de ${idp}); espera até ${ESPERA_S}s pelo arranque"

  # O laço corre DENTRO do contentor: um `docker run` por sondagem custaria mais do que a resolução
  # que se quer medir. `sh` do busybox; os tempos são de `date +%s` do mesmo relógio.
  # shellcheck disable=SC2016
  docker image inspect "${CURL_IMG}" >/dev/null 2>&1 \
    || fail "a imagem ${CURL_IMG} não está no servidor — puxe-a uma vez pelo digest: docker pull ${CURL_IMG}"
  docker run --rm --pull=never --network "${REDE}" --read-only --tmpfs /tmp --cap-drop ALL \
    --security-opt no-new-privileges --user 65534:65534 \
    -v "${SEGREDO}:/run/segredo:ro" -v "${CA}:/run/ca.crt:ro" \
    -e RUN="${run}" -e OBJ="${OBJECTIVO}" -e NO="${no}" -e IDP="${idp}" \
    -e ESPERA="${ESPERA_S}" -e ESPERA_FIM="${ESPERA_FIM_S}" -e ATE_AO_FIM="$([[ "${MODO}" == ate-ao-fim ]] && echo 1 || echo 0)" \
    --entrypoint sh "${CURL_IMG}" -c '
      set -u
      tr -d "\r\n" < /run/segredo > /tmp/s 2>/dev/null && [ -s /tmp/s ] || { echo "ERRO: segredo do aos-reader ilegivel no contentor"; exit 3; }
      # tok — um token NOVO, direito para o cabecalho em /tmp/h: nunca numa variavel nem no argv.
      tok() {
        curl -fsS --max-time 20 --cacert /run/ca.crt -X POST "$IDP" \
          -d grant_type=client_credentials -d client_id=aos-reader --data-urlencode client_secret@/tmp/s \
          | sed -n "s/.*\"access_token\":\"\([^\"]*\)\".*/Authorization: Bearer \1/p" > /tmp/h
        [ -s /tmp/h ]
      }
      tok || { echo "ERRO: o IdP nao emitiu token ao aos-reader"; exit 3; }
      printf "{\"run_id\":\"%s\",\"objective\":\"%s\"}" "$RUN" "$OBJ" > /tmp/corpo
      cod=$(curl -sS -o /tmp/r -w "%{http_code}" --max-time 20 -X POST "$NO/plans" \
              -H @/tmp/h -H "Content-Type: application/json" --data-binary @/tmp/corpo)
      t201=$(date +%s)
      echo "submetido: run=$RUN http=$cod t=$t201"
      case "$cod" in 201) ;; *) echo "ERRO: POST /plans respondeu $cod: $(head -c 300 /tmp/r)"; exit 4 ;; esac
      anterior=""; tsaida=""
      while :; do
        agora=$(date +%s)
        if [ -z "$tsaida" ] && [ $((agora - t201)) -gt "$ESPERA" ]; then
          echo "PRAZO: ao fim de ${ESPERA}s o pedido continua pending — a drenagem nao o reclamou (timer parado? fila atras?)"; exit 5
        fi
        if [ -n "$tsaida" ] && [ $((agora - tsaida)) -gt "$ESPERA_FIM" ]; then
          echo "PRAZO: ao fim de ${ESPERA_FIM}s desde o arranque o plano ainda nao terminou"; exit 5
        fi
        tok || true
        curl -sS -o /tmp/e --max-time 10 "$NO/plans/$RUN" -H @/tmp/h 2>/dev/null
        st=$(sed -n "s/.*\"status\":\"\([^\"]*\)\".*/\1/p" /tmp/e)
        if [ "$st" != "$anterior" ]; then
          echo "estado: ${st:-?} t=$agora (+$((agora - t201))s)"; anterior=$st
        fi
        if [ -z "$tsaida" ] && [ -n "$st" ] && [ "$st" != pending ]; then
          tsaida=$agora
          echo "arranque: $((tsaida - t201))s (201 -> $st)"
          [ "$ATE_AO_FIM" = 1 ] || exit 0
        fi
        if [ -n "$tsaida" ]; then
          case "$st" in
            terminal|aguarda_humano)
              echo "fim: $st exit_code=$(sed -n "s/.*\"exit_code\":\([0-9]*\).*/\1/p" /tmp/e) duracao_desde_o_arranque=$((agora - tsaida))s"; exit 0 ;;
          esac
          sleep 5
        else
          sleep 1
        fi
      done'
  return $?
}

# ─── 2. o log da drenagem e as métricas ─────────────────────────────────────────────────────
# resumo <rótulo> — lê números do stdin: n, mediana, p90 e máximo.
resumo() {
  sort -n | awk -v r="$1" '
    { v[NR] = $1 }
    END {
      if (NR == 0) { printf "  %-34s sem amostras\n", r; exit }
      m = v[int((NR + 1) / 2)]; p = v[int(NR * 0.9 + 0.999999)]; if (p == "") p = v[NR]
      printf "  %-34s n=%d  mediana=%s  p90=%s  max=%s\n", r, NR, m, p, v[NR]
    }'
}

ler_logs() {
  local fontes=() i amostras
  for (( i = 5; i >= 1; i-- )); do
    [[ -f "${DRENAR_LOG}.${i}" ]] && fontes+=("${DRENAR_LOG}.${i}")
  done
  [[ -f "${DRENAR_LOG}" ]] && fontes+=("${DRENAR_LOG}")
  if (( ${#fontes[@]} == 0 )); then
    log "sem ${DRENAR_LOG} — nada a ler"
    return 0
  fi
  log "log da drenagem: ${fontes[*]}"
  # Uma linha por facto: `vazia <S>`, `cheia <S> <planos>`, `intervalo <s>`, `plano <duracao_s>`,
  # `desfecho <epoch>`, `janela <ini> <fim>`, `adiada`, `falhada`. Os carimbos são UTC ISO-8601 e
  # convertem-se à mão (dias civis) — o awk do Debian (mawk) não tem mktime.
  amostras="$(cat "${fontes[@]}" | awk '
    function ep(ts,   y, m, d, yy, era, yoe, mp, doy, doe) {
      y = substr(ts, 1, 4) + 0; m = substr(ts, 6, 2) + 0; d = substr(ts, 9, 2) + 0
      yy = y - (m <= 2 ? 1 : 0)
      era = int((yy >= 0 ? yy : yy - 399) / 400)
      yoe = yy - era * 400
      mp = (m + 9) % 12
      doy = int((153 * mp + 2) / 5) + d - 1
      doe = yoe * 365 + int(yoe / 4) - int(yoe / 100) + doy
      return (era * 146097 + doe - 719468) * 86400 + substr(ts, 12, 2) * 3600 + substr(ts, 15, 2) * 60 + substr(ts, 18, 2)
    }
    # Sem `{n}`: o mawk antigo não conhece intervalos numa regex.
    $1 !~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z$/ { next }
    { t = ep($1); if (ini == "") ini = t; fim = t }
    /\[drenar-planos\] drenagem a começar/ {
      if (fimant != "") print "intervalo", t - fimant
      emcurso = 1; comeco = t; planos = 0; vazia = 0; next
    }
    emcurso && /\| fila vazia: nada a consumir/ { vazia = 1 }
    emcurso && /\| desfecho: run=/ {
      planos++; print "desfecho", t
      for (i = 1; i <= NF; i++) if ($i ~ /^duracao_s=/) { sub(/^duracao_s=/, "", $i); print "plano", $i }
    }
    emcurso && /\[drenar-planos\] drenagem terminada/ {
      if (vazia && planos == 0) print "vazia", t - comeco; else print "cheia", t - comeco, planos
      emcurso = 0; fimant = t; next
    }
    emcurso && /\[drenar-planos\] ERRO:/ { print "falhada"; emcurso = 0; fimant = t; next }
    /drenagem ADIADA/ { print "adiada" }
    END { if (ini != "") print "janela", ini, fim }
  ')"

  local ini fim horas n_desf
  read -r _ ini fim <<<"$(grep '^janela ' <<<"${amostras}" || true)"
  if [[ -z "${ini:-}" ]]; then
    log "o log não tem linhas com carimbo — nada a medir"
    return 0
  fi
  horas="$(awk -v a="${ini}" -v b="${fim}" 'BEGIN { printf "%.2f", (b - a) / 3600 }')"
  n_desf="$(grep -c '^desfecho ' <<<"${amostras}" || true)"
  echo
  echo "== drenagem (${horas} h de log, $(date -u -d "@${ini}" +%FT%TZ 2>/dev/null || echo "${ini}") → $(date -u -d "@${fim}" +%FT%TZ 2>/dev/null || echo "${fim}"))"
  grep '^vazia '     <<<"${amostras}" | awk '{ print $2 }' | resumo "S — passagem vazia (s)"
  grep '^cheia '     <<<"${amostras}" | awk '{ print $2 }' | resumo "passagem com planos (s)"
  grep '^cheia '     <<<"${amostras}" | awk '{ print $3 }' | resumo "planos por passagem com planos"
  grep '^intervalo ' <<<"${amostras}" | awk '{ print $2 }' | resumo "intervalo entre passagens (s)"
  grep '^plano '     <<<"${amostras}" | awk '{ print $2 }' | resumo "duração de cada plano (duracao_s)"
  printf '  %-34s %s (adiadas pelo deploy: %s, falhadas: %s)\n' "passagens" \
    "$(grep -cE '^(vazia|cheia) ' <<<"${amostras}" || true)" \
    "$(grep -c '^adiada' <<<"${amostras}" || true)" "$(grep -c '^falhada' <<<"${amostras}" || true)"
  printf '  %-34s %s desfecho(s); %s por hora\n' "vazão" "${n_desf}" \
    "$(awk -v n="${n_desf}" -v h="${horas}" 'BEGIN { if (h > 0) printf "%.2f", n / h; else print "?" }')"

  echo
  if [[ -s "${METRICAS}" ]]; then
    echo "== métricas acumuladas (${METRICAS})"
    grep -E '^aos_orq_consume_(drenagens_total|pedidos_reclamados_total|retomas_total|desfechos_total|desfechos_nao_reportados_total|ultima_drenagem_pedidos)' "${METRICAS}" | sed 's/^/  /'
    awk '
      index($1, "aos_orq_consume_plano_duracao_segundos_sum{") == 1   { c = $1; sub(/.*classe="/, "", c); sub(/".*/, "", c); s[c] = $2 }
      index($1, "aos_orq_consume_plano_duracao_segundos_count{") == 1 { c = $1; sub(/.*classe="/, "", c); sub(/".*/, "", c); n[c] = $2 }
      END { for (c in n) if (n[c] > 0) printf "  duração média por plano, classe %s: %.1f s (n=%d)\n", c, s[c] / n[c], n[c] }
    ' "${METRICAS}"
  else
    log "sem ${METRICAS} — a drenagem ainda não copiou métricas"
  fi
}

rc=0
if [[ "${MODO}" != so-logs ]]; then
  submeter_e_medir; rc=$?
  (( rc == 0 )) || log "a medição do arranque falhou (saída ${rc}) — os números dos logs abaixo continuam a valer"
fi
ler_logs
exit "${rc}"
