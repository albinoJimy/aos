#!/usr/bin/env bash
# aos450_deploy_drenagem.sh — corre o deploy.sh, o rollback.sh e o drenar-planos.sh REAIS contra
# stubs (docker, curl, logger) num directório temporário, e prova os critérios do AOS-450:
#
#   1. o deploy (e o rollback) seguram a drenagem desde o anúncio (antes do rsync) até o nó estar
#      saudável — uma drenagem no momento do `compose up` é ADIADA;
#   2. uma drenagem que encontra o deploy sai 0 com «drenagem ADIADA», sem docker e sem carimbo;
#      «outra drenagem em curso» continua a falhar;
#   3. o deploy espera, com tecto, por uma drenagem em curso; ao desistir o deploy ABORTA sem tocar
#      no stack e o rollback AVANÇA — e ambos o dizem;
#   4. um marcador de um deploy morto (pid morto, prazo passado, validade gigante) não pára a fila.
#
# Sem rede, sem docker, sem root. Corre-o o TestAOS450DeployEDrenagem (Linux: flock(1) e /proc).
#
#   bash aos450_deploy_drenagem.sh <raiz-do-repositório>
set -uo pipefail

RAIZ="$(cd "${1:?uso: aos450_deploy_drenagem.sh <raiz-do-repositório>}" && pwd)"
SRV="${RAIZ}/deploy/server"
for f in deploy.sh rollback.sh drenar-planos.sh; do
  [[ -s "${SRV}/${f}" ]] || { echo "FALTA ${SRV}/${f}"; exit 2; }
done
command -v flock >/dev/null || { echo "sem flock(1) — o cenário não prova nada sem ele"; exit 2; }
[[ -r "/proc/$$/cmdline" ]] || { echo "sem /proc — o cenário não prova nada sem ele"; exit 2; }

T="$(mktemp -d)"
limpar() {
  touch "${T}/solta" "${T}/fim-deploy" "${T}/stub/solta-pull" 2>/dev/null
  # shellcheck disable=SC2046
  kill $(jobs -p) 2>/dev/null
  wait 2>/dev/null
  rm -rf "${T}"
}
trap limpar EXIT

FALHAS=0
N_OK=0
ok()  { printf 'OK    %s\n' "$1"; N_OK=$(( N_OK + 1 )); }
mau() { printf 'FALHA %s\n' "$1"; FALHAS=$(( FALHAS + 1 )); return 1; }
# exige <descrição> <comando...>
exige() { local d="$1"; shift; if "$@"; then ok "${d}"; else mau "${d}"; fi; }
contem()    { grep -qF -- "$2" "$1"; }
nao_contem() { ! grep -qF -- "$2" "$1" 2>/dev/null; }
mostrar()   { sed 's/^/        | /' "$1"; }

# ── stubs ─────────────────────────────────────────────────────────────────────────────────────
mkdir -p "${T}/bin"
cat > "${T}/bin/docker" <<'STUB'
#!/usr/bin/env bash
# docker falso: regista cada chamada e responde o minimo que os scripts leem.
printf '%s\n' "$*" >> "${STUB_DIR}/docker.log"
case "$*" in
  "compose "*" --profile orq run "*)  echo "consume: 0 pedidos" ;;
  *nhi-run.jwt*)                      echo $(( $(date +%s) + 2000 )) ;;
  *"cat /d/aos-orq-consume.prom"*)    echo "aos_orq_consume_ultima_drenagem_timestamp_seconds $(date +%s)" ;;
  "pull "*)
    touch "${STUB_DIR}/em-pull"
    if [[ -n "${STUB_PULL_PRESO:-}" ]]; then
      while [[ ! -e "${STUB_DIR}/solta-pull" ]]; do sleep 0.05; done
    fi ;;
  "compose "*" up -d --remove-orphans")
    # O MOMENTO DA TROCA DA IMAGEM: que faz uma drenagem que arranque agora?
    if [[ -n "${STUB_DRENAR_NO_UP:-}" ]]; then
      bash "${STUB_DRENAR_NO_UP}" > "${STUB_DIR}/drenar-no-up.out" 2>&1
      echo $? > "${STUB_DIR}/drenar-no-up.rc"
    fi ;;
  "compose "*" ps --services")        echo aos ;;
  "compose "*" ps -q "*)              echo 0123456789ab ;;
  *"{{.State.StartedAt}}"*)           echo 2026-01-01T00:00:00Z ;;
  *"{{if .State.Health}}"*)           echo healthy ;;
  *"{{.State.Status}}"*)              echo running ;;
  *RepoDigests*)                      echo "${@: -1}" ;;
esac
exit 0
STUB
printf '#!/usr/bin/env bash\necho 200\n' > "${T}/bin/curl"
printf '#!/usr/bin/env bash\nexit 0\n' > "${T}/bin/logger"
chmod +x "${T}/bin/docker" "${T}/bin/curl" "${T}/bin/logger"

# ── o servidor falso ─────────────────────────────────────────────────────────────────────────────
AOS="${T}/aos"
mkdir -p "${AOS}/orq" "${AOS}/policies" "${AOS}/.drenagem"
echo '{}' > "${AOS}/orq/snapshot.json"
echo 'services: {}' > "${AOS}/docker-compose.prod.yml"
printf 'AOS_EDGE_PORT=8444\n' > "${AOS}/.env"
echo x > "${AOS}/policies/aos_authz.cedar"
echo x > "${AOS}/policies/aos_authz.sig"
cp "${SRV}/deploy.sh" "${SRV}/rollback.sh" "${SRV}/drenar-planos.sh" "${AOS}/"
IMG_ANTIGA="ghcr.io/x/aos-node@sha256:$(printf '%064d' 1)"
IMG_NOVA="ghcr.io/x/aos-node@sha256:$(printf '%064d' 2)"
MARCADOR="${AOS}/.drenagem/deploy-em-curso"
LOCK="${AOS}/.drenagem/lock"
export PATH="${T}/bin:${PATH}" STUB_DIR="${T}/stub" AOS_DIR="${AOS}" APP_DIR="${AOS}"
unset STUB_DRENAR_NO_UP STUB_PULL_PRESO DEPLOY_ESPERA_DRENAGEM_S DEPLOY_AO_DESISTIR_DA_DRENAGEM \
      DRENAR_DEPLOY_MAX_S NO_ROLLBACK GHCR_TOKEN

repor() {
  rm -f "${MARCADOR}" "${AOS}/.drenagem/ultima-ok" "${T}/solta" "${T}/preso" "${T}/fim-deploy"
  rm -rf "${STUB_DIR}" "${AOS}/logs"
  mkdir -p "${STUB_DIR}"
  printf 'AOS_IMAGE=%s\n' "${IMG_ANTIGA}" > "${AOS}/image.env"
  printf 'AOS_IMAGE=%s\n' "${IMG_ANTIGA}" > "${AOS}/image.env.prev"
}
marcar() { printf '%s %s %s %s\n' "$1" "$2" "$3" "$4" > "${MARCADOR}"; }
esperar_ficheiro() {  # <ficheiro> — até 10 s
  local i
  for (( i = 0; i < 200; i++ )); do [[ -e "$1" ]] && return 0; sleep 0.05; done
  return 1
}
# Outra drenagem (ou o deploy) a segurar o lock, num processo à parte, até libertar_lock.
segurar_lock() {
  rm -f "${T}/solta" "${T}/preso"
  flock "${LOCK}" bash -c 'touch "$1"; while [[ ! -e "$2" ]]; do sleep 0.05; done' _ "${T}/preso" "${T}/solta" &
  SEGURADOR=$!
  esperar_ficheiro "${T}/preso" || { echo "o segurador do lock não arrancou"; exit 2; }
}
libertar_lock() { touch "${T}/solta"; wait "${SEGURADOR}" 2>/dev/null; }
lock_livre() { flock -n "${LOCK}" true; }
drenar() { timeout 60 bash "${AOS}/drenar-planos.sh" > "${T}/out" 2>&1; }
agora() { date +%s; }

# Um deploy.sh VIVO (é o nome no cmdline que o drenar-planos.sh confere) e um pid MORTO.
printf 'while [[ ! -e "$1" ]]; do sleep 0.05; done\n' > "${T}/deploy.sh"
bash "${T}/deploy.sh" "${T}/fim-deploy" &
DEPLOY_VIVO=$!
bash -c 'exit 0' &
PID_MORTO=$!
wait "${PID_MORTO}"
# Um pid VIVO que não é um deploy.sh — independente do nome deste ficheiro de teste.
sleep 600 &
PID_ALHEIO=$!

echo "── 2. a drenagem perante o deploy"
repor
marcar "$(agora)" 0 900 anuncio
drenar; rc=$?
exige "anúncio do CD válido, lock livre: a drenagem sai 0" test "${rc}" -eq 0
exige "  … e diz «drenagem ADIADA»" contem "${T}/out" "drenagem ADIADA"
exige "  … sem chamar o docker (não reclama nada)" test ! -e "${STUB_DIR}/docker.log"
exige "  … sem escrever o carimbo ultima-ok" test ! -e "${AOS}/.drenagem/ultima-ok"
exige "  … e o marcador fica" test -e "${MARCADOR}"

repor
segurar_lock
marcar "$(agora)" "${DEPLOY_VIVO}" 3600 deploy
drenar; rc=$?
exige "lock ocupado por um deploy.sh vivo: sai 0 ADIADA" test "${rc}" -eq 0
exige "  … e nomeia o deploy.sh" contem "${T}/out" "deploy.sh pid ${DEPLOY_VIVO}"
libertar_lock

repor
segurar_lock
drenar; rc=$?
exige "lock ocupado sem marcador (outra drenagem): continua a FALHAR" test "${rc}" -eq 1
exige "  … com «outra drenagem em curso»" contem "${T}/out" "outra drenagem em curso"
exige "  … e não diz ADIADA" nao_contem "${T}/out" "ADIADA"
marcar "$(agora)" "${PID_MORTO}" 3600 deploy
drenar; rc=$?
exige "lock ocupado com marcador de pid morto: FALHA como outra drenagem" test "${rc}" -eq 1
exige "  … e, sem o lock, não apaga o marcador" test -e "${MARCADOR}"
libertar_lock

echo "── 4. um deploy morto não pára a fila"
# nome|marcador|motivo que o log tem de dar
for caso in "pid morto|$(agora) ${PID_MORTO} 3600 deploy|MORREU" \
            "anúncio com o prazo passado|$(( $(agora) - 1000 )) 0 900 anuncio|EXPIROU" \
            "deploy.sh VIVO mas expirado|$(( $(agora) - 100 )) ${DEPLOY_VIVO} 60 deploy|EXPIROU" \
            "validade gigante limitada a DRENAR_DEPLOY_MAX_S|$(( $(agora) - 20000 )) 0 999999 anuncio|EXPIROU" \
            "pid vivo que não é um deploy.sh|$(agora) ${PID_ALHEIO} 3600 deploy|já não é um deploy.sh" \
            "validade com zero à esquerda (octal)|$(agora) 0 0900 anuncio|ilegível" \
            "ilegível|lixo|ilegível"; do
  nome="${caso%%|*}"; resto="${caso#*|}"; linha="${resto%%|*}"; motivo="${resto#*|}"
  repor
  printf '%s\n' "${linha}" > "${MARCADOR}"
  drenar; rc=$?
  exige "marcador órfão (${nome}): a drenagem corre e sai 0" test "${rc}" -eq 0
  exige "  … drena de facto (escreve ultima-ok)" test -s "${AOS}/.drenagem/ultima-ok"
  exige "  … diz ÓRFÃO e apaga o marcador" bash -c '[[ ! -e "$1" ]] && grep -qF "ÓRFÃO" "$2"' _ "${MARCADOR}" "${T}/out"
  exige "  … com o motivo «${motivo}»" contem "${T}/out" "${motivo}"
done

echo "── 3. o anúncio do CD espera pela drenagem em curso"
repor
timeout 60 bash -s -- --anunciar < "${SRV}/deploy.sh" > "${T}/anuncio" 2>&1; rc=$?
exige "anúncio com o lock livre: sai 0" test "${rc}" -eq 0
exige "  … e deixa o marcador «<epoch> 0 900 anuncio»" bash -c 'read -r e p v o < "$1" && [[ "$e" =~ ^[0-9]+$ && "$p" == 0 && "$v" == 900 && "$o" == anuncio ]]' _ "${MARCADOR}"
exige "  … e larga o lock (o rsync não o segura)" lock_livre

repor
segurar_lock
DEPLOY_ESPERA_DRENAGEM_S=10 timeout 60 bash -s -- --anunciar < "${SRV}/deploy.sh" > "${T}/anuncio" 2>&1 &
ANUNCIO=$!
esperar_ficheiro "${MARCADOR}"
exige "anúncio à espera: o marcador já existe ANTES de o lock vagar" bash -c 'read -r e p v o < "$1" && [[ "$p" == 0 && "$v" == 910 ]]' _ "${MARCADOR}"
libertar_lock
wait "${ANUNCIO}"; rc=$?
exige "  … a drenagem acaba e o anúncio sai 0" test "${rc}" -eq 0
exige "  … e diz quanto esperou" contem "${T}/anuncio" "a drenagem terminou ao fim de"

repor
segurar_lock
DEPLOY_ESPERA_DRENAGEM_S=1 timeout 60 bash -s -- --anunciar < "${SRV}/deploy.sh" > "${T}/anuncio" 2>&1; rc=$?
exige "anúncio que desiste: FALHA (o CD pára antes do rsync)" test "${rc}" -ne 0
exige "  … diz que desistiu e que nada foi tocado" contem "${T}/anuncio" "desisti de esperar pela drenagem em curso ao fim de 1s — NADA foi sincronizado"
exige "  … e apaga o marcador (as drenagens retomam)" test ! -e "${MARCADOR}"
libertar_lock

echo "── 1. o caminho do CD: anúncio → rsync → deploy.sh, e uma drenagem no «compose up»"
repor
timeout 60 bash -s -- --anunciar < "${SRV}/deploy.sh" > "${T}/anuncio" 2>&1
# (o rsync dos scripts corria aqui; uma drenagem agora é adiada pelo anúncio)
drenar; rc=$?
exige "entre o anúncio e o deploy.sh (a janela do rsync): ADIADA, sai 0" bash -c '[[ $1 == 0 ]] && grep -qF "drenagem ADIADA" "$2"' _ "${rc}" "${T}/out"
STUB_DRENAR_NO_UP="${AOS}/drenar-planos.sh" timeout 120 bash "${AOS}/deploy.sh" "${IMG_NOVA}" > "${T}/deploy" 2>&1; rc=$?
exige "deploy verde" test "${rc}" -eq 0 || mostrar "${T}/deploy"
exige "  … a drenagem que arrancou no «compose up» saiu 0" bash -c '[[ "$(cat "$1")" == 0 ]]' _ "${STUB_DIR}/drenar-no-up.rc"
exige "  … ADIADA pelo deploy.sh (lock + marcador com o pid dele)" contem "${STUB_DIR}/drenar-no-up.out" "drenagem ADIADA, nada reclamado (deploy.sh pid"
exige "  … o deploy disse que segurou a drenagem" contem "${T}/deploy" "as drenagens do timer ficam ADIADAS até este deploy terminar"
exige "  … no fim o marcador foi apagado" test ! -e "${MARCADOR}"
exige "  … e o lock largado" lock_livre
drenar; rc=$?
exige "depois do deploy a drenagem volta a correr (sai 0, ultima-ok)" bash -c '[[ $1 == 0 && -s "$2" ]]' _ "${rc}" "${AOS}/.drenagem/ultima-ok"

echo "── 3. o deploy e o rollback perante uma drenagem que não acaba"
repor
segurar_lock
DEPLOY_ESPERA_DRENAGEM_S=1 timeout 60 bash "${AOS}/deploy.sh" "${IMG_NOVA}" > "${T}/deploy" 2>&1; rc=$?
exige "deploy que desiste: ABORTA" test "${rc}" -ne 0
exige "  … e diz que desistiu sem tocar no stack" contem "${T}/deploy" "desisti de esperar pela drenagem em curso ao fim de 1s — o stack NÃO foi tocado"
exige "  … sem pull nem compose up" nao_contem "${STUB_DIR}/docker.log" "up -d"
exige "  … com o image.env intacto" contem "${AOS}/image.env" "${IMG_ANTIGA}"
exige "  … e sem deixar o marcador" test ! -e "${MARCADOR}"
libertar_lock

repor
segurar_lock
STUB_DRENAR_NO_UP="${AOS}/drenar-planos.sh" DEPLOY_ESPERA_DRENAGEM_S=1 timeout 120 bash "${AOS}/rollback.sh" > "${T}/rollback" 2>&1; rc=$?
exige "rollback que desiste: AVANÇA e fica verde" test "${rc}" -eq 0 || mostrar "${T}/rollback"
exige "  … e di-lo" contem "${T}/rollback" "AVANÇO (DEPLOY_AO_DESISTIR_DA_DRENAGEM=avancar)"
exige "  … trocou mesmo a imagem" contem "${STUB_DIR}/docker.log" "up -d --remove-orphans"
exige "  … uma drenagem nova durante a troca continua ADIADA" contem "${STUB_DIR}/drenar-no-up.out" "drenagem ADIADA"
exige "  … e o marcador sai com ele" test ! -e "${MARCADOR}"
libertar_lock

echo "── 3. as variáveis que o CD passa são validadas no script"
for par in "DEPLOY_ESPERA_DRENAGEM_S=0900" "DEPLOY_ESPERA_DRENAGEM_S=1;true" "DEPLOY_AO_DESISTIR_DA_DRENAGEM=sim"; do
  repor
  env "${par}" timeout 60 bash -s -- --anunciar < "${SRV}/deploy.sh" > "${T}/anuncio" 2>&1; rc=$?
  exige "anúncio com ${par}: recusa" bash -c '[[ $1 != 0 ]] && grep -qF "inválido" "$2"' _ "${rc}" "${T}/anuncio"
  exige "  … sem escrever o marcador" test ! -e "${MARCADOR}"
done

echo "── 4. um deploy.sh morto por SIGKILL a meio"
repor
STUB_PULL_PRESO=1 bash "${AOS}/deploy.sh" "${IMG_NOVA}" > "${T}/deploy" 2>&1 &
MORTO=$!
esperar_ficheiro "${STUB_DIR}/em-pull"
exige "deploy a meio (no pull): o marcador tem o pid dele" bash -c 'read -r e p v o < "$1" && [[ "$p" == "$2" ]]' _ "${MARCADOR}" "${MORTO}"
kill -9 "${MORTO}"; wait "${MORTO}" 2>/dev/null
touch "${STUB_DIR}/solta-pull"
for (( i = 0; i < 200; i++ )); do lock_livre && break; sleep 0.05; done
exige "  … morto, deixa o marcador para trás (sem trap)" test -e "${MARCADOR}"
drenar; rc=$?
exige "  … e a drenagem seguinte corre na mesma (sai 0, ultima-ok)" bash -c '[[ $1 == 0 && -s "$2" ]]' _ "${rc}" "${AOS}/.drenagem/ultima-ok"
exige "  … apagando o marcador órfão" test ! -e "${MARCADOR}"

touch "${T}/fim-deploy"
echo
echo "verificações: ${N_OK} ok, ${FALHAS} falhas"
if (( FALHAS > 0 || N_OK < 70 )); then
  echo "falhas: ${FALHAS}"
  exit 1
fi
echo "falhas: 0"
