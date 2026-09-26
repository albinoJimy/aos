#!/usr/bin/env bash
# aos445_avisos_planos.sh — corre o drenar-planos.sh e o avisar-planos.sh REAIS contra stubs
# (docker, curl, logger) num directório temporário, e prova o desenho do AOS-445:
#
#   1. a drenagem recolhe as linhas `aviso:` do consume para o outbox (600), e falhar a escrevê-lo
#      nunca a faz falhar; um consume que falha entrega na mesma o que já reportou;
#   2. a drenagem confere o delta de terminais nas métricas com as linhas lidas, e um desencontro
#      (binário antigo, reporte falhado) vai para o outbox;
#   3. o aviso sai pseudonimizado (HMAC-SHA256(chave, run_id)[:12], com o vector do RFC 4231), com
#      classe e código e nada mais; prioridade default para 0 e high para os outros; o 7 é «recusado»;
#      sem chave não sai nada;
#   4. no máximo um aviso por run, e um por desencontro; um envio falhado fica no outbox e a volta
#      pára nele; o tópico dos planos é outro e igual ao da infraestrutura é recusado; --teste não
#      mexe no estado;
#   5. um run_id com espaços Unicode (U+3000, U+2028) casa em QUALQUER locale (LC_ALL=C nos scripts);
#   6. as sobras de uma drenagem interrompida entram no outbox na drenagem seguinte.
#
# Sem rede, sem docker, sem root. Corre-o o TestAOS445OutboxEAvisos (Linux: flock(1)).
#
#   bash aos445_avisos_planos.sh <raiz-do-repositório>
set -uo pipefail

RAIZ="$(cd "${1:?uso: aos445_avisos_planos.sh <raiz-do-repositório>}" && pwd)"
SRV="${RAIZ}/deploy/server"
for f in drenar-planos.sh avisar-planos.sh; do
  [[ -s "${SRV}/${f}" ]] || { echo "FALTA ${SRV}/${f}"; exit 2; }
done
command -v flock >/dev/null || { echo "sem flock(1) — o cenário não prova nada sem ele"; exit 2; }
command -v sha256sum >/dev/null || { echo "sem sha256sum"; exit 2; }
LINUX=0; [[ "$(uname -s)" == Linux ]] && LINUX=1

T="$(mktemp -d)"
trap 'rm -rf "${T}"' EXIT

FALHAS=0
N_OK=0
ok()  { printf 'OK    %s\n' "$1"; N_OK=$(( N_OK + 1 )); }
mau() { printf 'FALHA %s\n' "$1"; FALHAS=$(( FALHAS + 1 )); return 1; }
exige() { local d="$1"; shift; if "$@"; then ok "${d}"; else mau "${d}"; fi; }
contem()     { grep -qF -- "$2" "$1" 2>/dev/null; }
nao_contem() { ! grep -qF -- "$2" "$1" 2>/dev/null; }
linhas()     { if [[ -f "$1" ]]; then wc -l < "$1" | tr -d ' '; else echo 0; fi; }
mostrar()    { sed 's/^/        | /' "$1" 2>/dev/null; }
# O HMAC do script, tirado dele próprio: o pseudónimo esperado calcula-se com o mesmo código, e o
# código confere-se contra o vector do RFC 4231 logo abaixo.
# shellcheck disable=SC1090
source <(sed -n '/^hmac_sha256() {/,/^}/p' "${SRV}/avisar-planos.sh")
CHAVE="000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
pseudo()     { local h; h="$(LC_ALL=C hmac_sha256 "${CHAVE}" "$1")"; printf '%s' "${h:0:12}"; }

# ── stubs ─────────────────────────────────────────────────────────────────────────────────────
mkdir -p "${T}/bin" "${T}/stub"
cat > "${T}/bin/docker" <<'STUB'
#!/usr/bin/env bash
# docker falso: o consume imprime ${STUB_DIR}/consume.out e sai ${STUB_DIR}/consume.rc; as métricas
# são ${STUB_DIR}/metricas.prom com o carimbo DESTA drenagem.
case "$*" in
  "compose "*" --profile orq run "*)
    cat "${STUB_DIR}/consume.out" 2>/dev/null
    exit "$(cat "${STUB_DIR}/consume.rc" 2>/dev/null || echo 0)" ;;
  *nhi-run.jwt*)                   echo $(( $(date +%s) + 2000 )) ;;
  *"cat /d/aos-orq-consume.prom"*)
    cat "${STUB_DIR}/metricas.prom" 2>/dev/null
    echo "aos_orq_consume_ultima_drenagem_timestamp_seconds $(date +%s)" ;;
esac
exit 0
STUB
cat > "${T}/bin/curl" <<'STUB'
#!/usr/bin/env bash
# curl falso (ntfy): guarda título, prioridade, tags, corpo e URL de cada envio; sai ${STUB_DIR}/curl.rc.
rc="$(cat "${STUB_DIR}/curl.rc" 2>/dev/null || echo 0)"
{
  printf '=== envio (rc=%s)\n' "${rc}"
  while (( $# > 0 )); do
    case "$1" in
      -H) printf 'H %s\n' "$2"; shift ;;
      --data-binary) printf 'CORPO %s\n' "$2"; shift ;;
      http*) printf 'URL %s\n' "$1" ;;
    esac
    shift
  done
} >> "${STUB_DIR}/ntfy.log"
exit "${rc}"
STUB
printf '#!/usr/bin/env bash\nexit 0\n' > "${T}/bin/logger"
chmod +x "${T}/bin/docker" "${T}/bin/curl" "${T}/bin/logger"

# ── o servidor falso ─────────────────────────────────────────────────────────────────────────────
AOS="${T}/aos"
mkdir -p "${AOS}/orq" "${AOS}/secrets"
echo '{}' > "${AOS}/orq/snapshot.json"
TOPICO_PLANOS="topico-dos-planos-0123456789"
printf '%s\n' "${TOPICO_PLANOS}" > "${AOS}/secrets/ntfy-topico-planos"
printf '%s\n' "topico-da-infra-9876543210" > "${AOS}/secrets/ntfy-topico"
printf '%s\n' "${CHAVE}" > "${AOS}/secrets/aviso-planos-hmac.key"
cp "${SRV}/drenar-planos.sh" "${SRV}/avisar-planos.sh" "${AOS}/"
OUTBOX="${AOS}/.avisos-planos/pendentes"
ENVIADOS="${AOS}/.avisos-planos/enviados"
ALOG="${AOS}/logs/avisar-planos.log"
export PATH="${T}/bin:${PATH}" STUB_DIR="${T}/stub" AOS_DIR="${AOS}"
unset DRENAR_MAX DRENAR_AVISOS_DIR AOS_AVISO_PLANOS_DIR AOS_AVISO_PLANOS_TOPICO_FILE AOS_ALERTA_TOPICO_FILE \
      AOS_AVISO_PLANOS_LOG_DIR AOS_AVISO_PLANOS_MAX AOS_ALERTA_NTFY_BASE

# consume <rc> <linhas...> — o que o consume falso imprime nesta drenagem.
consume() { local rc="$1"; shift; printf '%s\n' "$@" > "${STUB_DIR}/consume.out"; echo "${rc}" > "${STUB_DIR}/consume.rc"; }
# metricas <terminais_0> <terminais_7> <nao_reportados> — as séries acumuladas que o consume «escreve».
metricas() {
  {
    echo "# HELP aos_orq_consume_desfechos_total desfechos"
    echo "aos_orq_consume_desfechos_total{classe=\"terminal\",codigo=\"0\"} $1"
    echo "aos_orq_consume_desfechos_total{classe=\"terminal\",codigo=\"7\"} $2"
    echo "aos_orq_consume_desfechos_total{classe=\"transitorio\",codigo=\"8\"} 4"
    echo "aos_orq_consume_desfechos_nao_reportados_total $3"
  } > "${STUB_DIR}/metricas.prom"
}
drenar() { timeout 60 bash "${AOS}/drenar-planos.sh" > "${T}/dout" 2>&1; }
avisar() { rm -f "${STUB_DIR}/ntfy.log"; timeout 60 bash "${AOS}/avisar-planos.sh" "$@" > "${T}/aout" 2>&1; }
echo 0 > "${STUB_DIR}/curl.rc"

RA="plan-e2e-445-a"; RB="cliente/pedido~b"; RC="plan-e2e-445-c"

echo "── 3. o HMAC do script é o do RFC 4231"
exige "HMAC-SHA256 do RFC 4231, caso 2 (chave «Jefe»)" \
  test "$(LC_ALL=C hmac_sha256 4a656665 'what do ya want for nothing?')" = 5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843

echo "── 1. a drenagem recolhe as linhas aviso: para o outbox"
consume 0 "reclamado: run=${RA} geracao=1 objectivo_bytes=40" \
          "desfecho: run=${RA} codigo=0 classe=terminal origem=decomposicao geracao=1" \
          "aviso: run=${RA} geracao=1 classe=terminal codigo=0" \
          "desfecho: run=${RB} codigo=7 classe=terminal origem=documento geracao=2" \
          "aviso: run=${RB} geracao=2 classe=terminal codigo=7" \
          "aviso: run=${RC} geracao=1 classe=transitorio codigo=8" \
          "drenagem terminada: 2 pedido(s) consumido(s)"
metricas 1 1 0
drenar; rc=$?
exige "drenagem com dois terminais sai 0" test "${rc}" -eq 0 || mostrar "${T}/dout"
exige "  … o outbox tem as DUAS linhas aviso: terminais (e não a transitória)" test "$(linhas "${OUTBOX}")" -eq 2
exige "  … tal e qual" contem "${OUTBOX}" "aviso: run=${RB} geracao=2 classe=terminal codigo=7"
exige "  … e diz quantas entregou" contem "${T}/dout" "2 aviso(s) de plano terminado no outbox"
exige "  … sem contagem anterior, não confere e di-lo" contem "${T}/dout" "contagem por conferir nesta drenagem"
exige "  … e guarda a contagem desta para a seguinte" bash -c '[[ "$(cat "$1")" == "2 0" ]]' _ "${AOS}/.drenagem/avisos-contagem"
exige "  … sem deixar o ficheiro de trabalho" test ! -e "${AOS}/.drenagem/avisos-desta-drenagem"
if (( LINUX )); then
  exige "  … outbox em 600 e a pasta em 700" bash -c '[[ "$(stat -c %a "$1")" == 600 && "$(stat -c %a "$2")" == 700 ]]' _ "${OUTBOX}" "${AOS}/.avisos-planos"
fi

echo "── 3. o aviso sai pseudonimizado, com classe e código e nada mais"
avisar; rc=$?
exige "avisar-planos com dois pendentes sai 0" test "${rc}" -eq 0 || mostrar "${T}/aout"
exige "  … dois envios" test "$(grep -c '^=== envio' "${STUB_DIR}/ntfy.log")" -eq 2
exige "  … para o tópico dos PLANOS" contem "${STUB_DIR}/ntfy.log" "URL https://ntfy.sh/${TOPICO_PLANOS}"
exige "  … o 0: título ok, prioridade default" bash -c 'grep -A3 "^=== envio" "$1" | grep -qF "H Title: AOS: plano terminado (ok)" && grep -qF "H Priority: default" "$1"' _ "${STUB_DIR}/ntfy.log"
exige "  … com o pseudónimo sha256(run)[:12] do primeiro" contem "${STUB_DIR}/ntfy.log" "CORPO Plano $(pseudo "${RA}"): terminal, código 0 (ok)."
exige "  … o 7: «recusado», prioridade high" bash -c 'grep -qF "CORPO Plano $2: terminal, código 7 (recusado)." "$1" && grep -qF "H Priority: high" "$1"' _ "${STUB_DIR}/ntfy.log" "$(pseudo "${RB}")"
exige "  … e NENHUM run_id sai do servidor" bash -c '! grep -qF -e "$2" -e "$3" "$1"' _ "${STUB_DIR}/ntfy.log" "${RA}" "pedido~b"
exige "  … o outbox fica vazio" test "$(linhas "${OUTBOX}")" -eq 0
exige "  … e os dois ficam registados como enviados (sem o run_id)" bash -c '[[ $(wc -l < "$1") -eq 2 ]] && ! grep -qF "$2" "$1"' _ "${ENVIADOS}" "${RA}"
exige "  … o log local cruza pseudónimo e run" contem "${ALOG}" "enviado: plano $(pseudo "${RA}") run=${RA} geracao=1 codigo=0 (ok)"

echo "── 4. no máximo um aviso por run"
consume 0 "aviso: run=${RA} geracao=3 classe=terminal codigo=0"
metricas 2 1 0
drenar; rc=$?
exige "a drenagem com a contagem certa confere sem queixa" bash -c '[[ $1 == 0 ]] && ! grep -qF "AVISO:" "$2"' _ "${rc}" "${T}/dout"
avisar; rc=$?
exige "um segundo fim do MESMO run não é avisado" test ! -e "${STUB_DIR}/ntfy.log"
exige "  … e di-lo, com a geração e o código de cada um" contem "${T}/aout" "TERMINOU OUTRA VEZ (geracao 3, codigo 0) — NÃO avisado: já foi avisado (geracao 1, codigo 0)"
exige "  … e sai do outbox" test "$(linhas "${OUTBOX}")" -eq 0

echo "── 4. um envio falhado fica no outbox"
consume 0 "aviso: run=${RC} geracao=2 classe=terminal codigo=9"
metricas 3 1 0
drenar
echo 22 > "${STUB_DIR}/curl.rc"
avisar; rc=$?
exige "ntfy em baixo: o avisar-planos sai != 0" test "${rc}" -ne 0
exige "  … e a linha continua no outbox" contem "${OUTBOX}" "aviso: run=${RC} geracao=2 classe=terminal codigo=9"
exige "  … sem ser registada como enviada" bash -c '[[ $(wc -l < "$1") -eq 2 ]]' _ "${ENVIADOS}"
echo 0 > "${STUB_DIR}/curl.rc"
avisar; rc=$?
exige "ntfy de volta: sai na execução seguinte" bash -c '[[ $1 == 0 ]] && grep -qF "H Title: AOS: plano falhou (codigo 9)" "$2" && grep -qF "H Priority: high" "$2"' _ "${rc}" "${STUB_DIR}/ntfy.log"
exige "  … e o outbox esvazia" test "$(linhas "${OUTBOX}")" -eq 0

echo "── 2. o desencontro: terminais nas métricas sem linha aviso:"
consume 0 "desfecho: run=plan-antigo codigo=0 classe=terminal"
metricas 4 1 0
drenar; rc=$?
exige "binário sem a linha: a drenagem sai 0 na mesma" test "${rc}" -eq 0
exige "  … e di-lo (anterior ao AOS-445?)" contem "${T}/dout" "1 desfecho(s) terminal(is) nas métricas e 0 linha(s) «aviso:»"
exige "  … e põe o desencontro no outbox, com o início da drenagem" bash -c 'grep -qE "^desencontro: terminais=1 avisos=0 em=[0-9]+$" "$1"' _ "${OUTBOX}"
DESENC="$(grep '^desencontro:' "${OUTBOX}")"
avisar
exige "  … que chega ao operador como aviso próprio, high" bash -c 'grep -qF "H Title: AOS: planos terminados SEM aviso" "$1" && grep -qF "H Priority: high" "$1"' _ "${STUB_DIR}/ntfy.log"
printf '%s\n' "${DESENC}" >> "${OUTBOX}"
avisar
exige "  … e só uma vez: a mesma linha outra vez (reescrita falhada) não se reenvia" bash -c '[[ ! -e "$1" ]] && grep -qF "desencontro já avisado" "$2"' _ "${STUB_DIR}/ntfy.log" "${T}/aout"

consume 0 "aos-orq: desfecho de plan-x NAO reportado (503)"
metricas 5 1 1
drenar
exige "reporte falhado: o desencontro é atribuído ao reporte" contem "${T}/dout" "o reporte de 1 falhou"
exige "  … e vai também para o outbox" contem "${OUTBOX}" "desencontro: terminais=1 avisos=0 em="
avisar

echo "── 1. o consume que falha entrega o que já reportou"
consume 1 "aviso: run=plan-e2e-445-d geracao=1 classe=terminal codigo=0" "aos-orq: reclamar pedido: 503"
metricas 6 1 1
drenar; rc=$?
exige "consume a falhar: a drenagem falha (como antes)" test "${rc}" -eq 1
exige "  … mas o aviso do pedido já reportado está no outbox" contem "${OUTBOX}" "aviso: run=plan-e2e-445-d geracao=1"
exige "  … e confere a contagem na mesma (métricas desta drenagem)" nao_contem "${T}/dout" "AVISO:"
avisar

echo "── 1. o outbox impossível não faz falhar a drenagem"
rm -rf "${AOS}/.avisos-planos"
printf 'isto não é uma pasta\n' > "${AOS}/.avisos-planos"
consume 0 "aviso: run=plan-e2e-445-e geracao=1 classe=terminal codigo=0"
metricas 7 1 1
drenar; rc=$?
exige "outbox impossível: a drenagem sai 0" test "${rc}" -eq 0 || mostrar "${T}/dout"
exige "  … e escreve o carimbo de sucesso" test -s "${AOS}/.drenagem/ultima-ok"
exige "  … e di-lo em voz alta" contem "${T}/dout" "NÃO entraram no outbox"
rm -f "${AOS}/.avisos-planos"

echo "── 2. sem métricas desta drenagem, a seguinte não confere contra um «antes» velho"
consume 0 "aviso: run=plan-e2e-445-f geracao=1 classe=terminal codigo=0"
metricas 8 1 1
mv "${T}/bin/docker" "${T}/docker.bom"
sed 's|cat "${STUB_DIR}/metricas.prom" 2>/dev/null|exit 1|' "${T}/docker.bom" > "${T}/bin/docker"; chmod +x "${T}/bin/docker"
drenar; rc=$?
exige "cópia das métricas falhada: a drenagem falha (como antes)" test "${rc}" -eq 1
exige "  … o aviso entra no outbox na mesma" contem "${OUTBOX}" "aviso: run=plan-e2e-445-f"
exige "  … e a contagem fica por conferir" bash -c 'grep -qF "contagem por conferir — sem as métricas desta drenagem" "$1" && [[ ! -e "$2" ]]' _ "${T}/dout" "${AOS}/.drenagem/avisos-contagem"
mv -f "${T}/docker.bom" "${T}/bin/docker"
consume 0 "fila vazia: nada a consumir"
metricas 9 1 1
drenar
exige "  … a drenagem seguinte não acusa um desencontro falso" bash -c '! grep -qF "AVISO:" "$1" && grep -qF "contagem por conferir nesta drenagem" "$1"' _ "${T}/dout"
avisar

echo "── 4. a volta pára no PRIMEIRO envio falhado"
printf 'aviso: run=plan-p%s geracao=1 classe=terminal codigo=0\n' 1 2 3 > "${OUTBOX}"
echo 22 > "${STUB_DIR}/curl.rc"
avisar
exige "ntfy em baixo com 3 pendentes: UMA tentativa, não três" test "$(grep -c '^=== envio' "${STUB_DIR}/ntfy.log")" -eq 1
exige "  … e os 3 ficam, pela mesma ordem" bash -c '[[ "$(sed -n 1p "$1")" == *plan-p1* && "$(sed -n 3p "$1")" == *plan-p3* && $(wc -l < "$1") -eq 3 ]]' _ "${OUTBOX}"
echo 0 > "${STUB_DIR}/curl.rc"
avisar
exige "  … e saem os 3 quando o ntfy volta" bash -c '[[ $(grep -c "^=== envio" "$1") -eq 3 && ! -s "$2" ]]' _ "${STUB_DIR}/ntfy.log" "${OUTBOX}"

echo "── 3. sem chave HMAC não sai nada (fail-closed)"
printf 'aviso: run=plan-sem-chave geracao=1 classe=terminal codigo=0\n' > "${OUTBOX}"
mv "${AOS}/secrets/aviso-planos-hmac.key" "${T}/chave.guardada"
avisar; rc=$?
exige "sem chave: sai != 0, sem envio, e o aviso fica" bash -c '[[ $1 != 0 && ! -e "$2" ]] && grep -qF "plan-sem-chave" "$3" && grep -qF "NENHUM aviso sai" "$4"' _ "${rc}" "${STUB_DIR}/ntfy.log" "${OUTBOX}" "${T}/aout"
avisar --teste; rc=$?
exige "  … e o --teste também recusa (a instalação não está completa)" bash -c '[[ $1 != 0 && ! -e "$2" ]]' _ "${rc}" "${STUB_DIR}/ntfy.log"
mv "${T}/chave.guardada" "${AOS}/secrets/aviso-planos-hmac.key"
avisar
exige "  … com a chave, sai" contem "${STUB_DIR}/ntfy.log" "CORPO Plano $(pseudo plan-sem-chave): terminal, código 0 (ok)."
exige "--pseudonimo dá o mesmo pseudónimo que o aviso" bash -c '[[ "$(AOS_DIR="$1" bash "$1/avisar-planos.sh" --pseudonimo plan-sem-chave | tail -1)" == "$2" ]]' _ "${AOS}" "$(pseudo plan-sem-chave)"

echo "── 5. espaços Unicode no run_id casam em qualquer locale"
RU=$'plan-u\xe3\x80\x80x\xe2\x80\xa8y'   # U+3000 e U+2028, que o ValidarStreamID aceita
consume 0 "aviso: run=${RU} geracao=1 classe=terminal codigo=0"
metricas 10 1 1
LC_ALL=C.UTF-8 timeout 60 bash "${AOS}/drenar-planos.sh" > "${T}/dout" 2>&1
exige "drenagem em C.UTF-8: o aviso do run com U+3000/U+2028 entra no outbox" bash -c 'grep -qF "$2" "$1"' _ "${OUTBOX}" "aviso: run=${RU} "
rm -f "${STUB_DIR}/ntfy.log"
LC_ALL=C.UTF-8 timeout 60 bash "${AOS}/avisar-planos.sh" > "${T}/aout" 2>&1
exige "  … e o avisar em C.UTF-8 envia-o (não o descarta)" bash -c 'grep -qF "CORPO Plano $2:" "$1" && ! grep -qF "DESCARTADA" "$3"' _ "${STUB_DIR}/ntfy.log" "$(pseudo "${RU}")" "${T}/aout"

echo "── 6. as sobras de uma drenagem interrompida"
printf 'aviso: run=plan-orfao geracao=2 classe=terminal codigo=9\n' > "${AOS}/.drenagem/avisos-desta-drenagem"
consume 0 "fila vazia: nada a consumir"
metricas 10 1 1
drenar; rc=$?
exige "a drenagem seguinte entrega as sobras ao outbox" bash -c '[[ $1 == 0 ]] && grep -qF "aviso: run=plan-orfao geracao=2" "$2" && grep -qF "drenagem INTERROMPIDA" "$3"' _ "${rc}" "${OUTBOX}" "${T}/dout"
exige "  … e não as deixa para trás" test ! -e "${AOS}/.drenagem/avisos-desta-drenagem"
avisar

echo "── 4. o tópico, o --teste e o outbox estranho"
: > "${OUTBOX}"
# O lixo vem PRIMEIRO: a volta pára no primeiro envio falhado, e o que vem depois dele fica por ler.
printf 'lixo que alguém escreveu\naviso: run=plan-g geracao=1 classe=terminal codigo=0\n' >> "${OUTBOX}"
cp "${AOS}/secrets/ntfy-topico" "${AOS}/secrets/ntfy-topico-planos"
avisar
exige "tópico dos planos IGUAL ao da infra: recusado" bash -c '[[ ! -e "$1" ]] && grep -qF "é o MESMO dos alertas de infraestrutura" "$2"' _ "${STUB_DIR}/ntfy.log" "${T}/aout"
exige "  … e o aviso fica pendente" contem "${OUTBOX}" "aviso: run=plan-g"
exige "  … a linha de forma desconhecida sai (e di-lo)" bash -c '! grep -qF "lixo" "$1" && grep -qF "forma desconhecida DESCARTADA" "$2"' _ "${OUTBOX}" "${T}/aout"
rm -f "${AOS}/secrets/ntfy-topico-planos"
avisar
exige "sem tópico dos planos: não envia e fica pendente" bash -c '[[ ! -e "$1" ]] && grep -qF "aviso: run=plan-g" "$2"' _ "${STUB_DIR}/ntfy.log" "${OUTBOX}"
printf '%s\n' "${TOPICO_PLANOS}" > "${AOS}/secrets/ntfy-topico-planos"
antes="$(cat "${OUTBOX}")"
avisar --teste; rc=$?
exige "--teste envia UM aviso de teste ao tópico dos planos" bash -c '[[ $1 == 0 ]] && grep -qF "H Title: AOS: teste do aviso de planos" "$2" && [[ $(grep -c "^=== envio" "$2") == 1 ]]' _ "${rc}" "${STUB_DIR}/ntfy.log"
exige "  … sem mexer no outbox" bash -c '[[ "$(cat "$1")" == "$2" ]]' _ "${OUTBOX}" "${antes}"
avisar
exige "e o pendente sai quando o tópico volta" bash -c 'grep -qF "CORPO Plano $2: terminal, código 0 (ok)." "$1"' _ "${STUB_DIR}/ntfy.log" "$(pseudo plan-g)"

rm -rf "${AOS}/.avisos-planos"
avisar; rc=$?
exige "sem pasta de avisos (nenhuma drenagem entregou nada): sai 0 calado" bash -c '[[ $1 == 0 && ! -e "$2" ]]' _ "${rc}" "${STUB_DIR}/ntfy.log"

echo
echo "verificações: ${N_OK} ok, ${FALHAS} falhas"
if (( FALHAS > 0 || N_OK < 62 )); then
  echo "falhas: ${FALHAS}"
  exit 1
fi
echo "falhas: 0"
