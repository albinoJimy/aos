#!/usr/bin/env bash
# restore-drill.sh — prova que um backup LEVANTA O SISTEMA, e não apenas que decifra.
#
# São perguntas diferentes, e só a segunda interessa no dia mau:
#
#   o artefacto decifra e não está truncado?  ← o pull-backups.ps1 já responde
#   o que lá está levantaria o sistema?       ← só se sabe RESTAURANDO
#
# Este ensaio responde à segunda. Levanta um AOS COMPLETO — Vault, Postgres, Keycloak e nó — a
# partir do conteúdo de um backup, numa rede isolada, e faz uma leitura autenticada de ponta a
# ponta. Não toca no que está a correr.
#
#   USO (no servidor, com o bundle JÁ DECIFRADO):
#     bash /opt/aos/restore-drill.sh /tmp/bundle.tar.gz
#
#   O bundle decifra-se na MÁQUINA DO OPERADOR, onde vive a chave privada:
#     openssl smime -decrypt -binary -inform DER -in aos-<stamp>.tar.gz.enc \
#       -inkey secrets-local/backup-key/backup.key -out bundle.tar.gz
#     scp bundle.tar.gz aos@<host>:/tmp/
#
#   A chave privada NUNCA vai para o servidor. É o que torna seguro guardar os backups no host.
#
# ─── Três coisas que este ensaio aprendeu por falhar primeiro ─────────────────────────────────
#
#  1. O RESTAURO TEM DE REPRODUZIR OS NOMES. Os certificados internos têm SAN para `idp` e
#     `vault`. Um ensaio que chame aos contentores `drill-idp`/`drill-vault` vê o nó recusar TUDO
#     com 404 uniforme — a verificação TLS do JWKS falha e o read-path nega fail-closed. Correcto,
#     e indistinguível de "não existe". Daí a rede própria com --alias.
#
#  2. O vault-init.json deste sistema tem "keys", não "unseal_keys_b64" (que é o que o
#     `vault operator init -format=json` produz). Presumir o formato de um ficheiro que está ali
#     para ser lido custou uma iteração.
#
#  3. `start --optimized` falha num Keycloak que nunca fez `build`. O comando certo é o que o
#     contentor de produção usa — este script lê-o de lá em vez de o adivinhar.
#
# ─── O que faz este ensaio VALER ──────────────────────────────────────────────────────────────
#
# Um 200 sozinho provaria apenas que o nó responde. O ensaio só passa se os CONTROLOS também
# valerem no sistema restaurado: sem credencial 404, header forjado 404, e o MESMO token recusado
# à segunda. É a diferença entre "arrancou" e "as decisões continuam a ser tomadas".
set -Eeuo pipefail

BUNDLE="${1:-}"
NET=drill-net
PREFIX=drill
RID_PADRAO="${RESTORE_DRILL_RUN_ID:-}"

log()  { printf '[ensaio] %s\n' "$*"; }
fail() { printf '[ensaio] ERRO: %s\n' "$*" >&2; exit 1; }

[[ -n "${BUNDLE}" && -f "${BUNDLE}" ]] || fail "uso: $0 <bundle.tar.gz decifrado>"
command -v docker >/dev/null || fail "docker em falta"

D="$(mktemp -d /tmp/restore-drill.XXXXXX)"

# LIMPEZA EM TRAP, e com shred. Enquanto o ensaio corre, ${D} tem lá dentro o .env, os secrets/ e
# o material TLS interno em CLARO. Sair a meio sem isto deixaria segredos no disco do host.
limpar() {
  local st=$?
  log "a limpar ..."
  docker rm -f "${PREFIX}-aos" "${PREFIX}-idp" "${PREFIX}-vault" "${PREFIX}-pg" >/dev/null 2>&1 || true
  docker network rm "${NET}" >/dev/null 2>&1 || true
  find "${D}" -type f -exec shred -u {} + 2>/dev/null || true
  rm -rf "${D}"
  exit "${st}"
}
trap limpar EXIT INT TERM

log "1/7 a desembrulhar o artefacto"
mkdir -p "${D}/x"
tar xzf "${BUNDLE}" -C "${D}/x"
for f in MANIFEST volumes.tar.gz config.tar.gz idp-db.sql; do
  [[ -s "${D}/x/${f}" ]] || fail "o bundle não tem ${f} — não é um backup deste sistema"
done
# SEPARADOS de propósito: o config.tar.gz traz `vault/` (a CONFIGURAÇÃO de /opt/aos/vault) e o
# volumes.tar.gz traz `vault/` (os DADOS do volume). Extraídos para o mesmo sítio colidem, e o
# sintoma é o Vault a não desselar — que se lê como "o backup está mau" e não como "o ensaio
# está mal montado". Custou uma iteração.
mkdir -p "${D}/cfg" "${D}/vol"
tar xzf "${D}/x/config.tar.gz"  -C "${D}/cfg"
tar xzf "${D}/x/volumes.tar.gz" -C "${D}/vol"
chmod -R a+rwX "${D}/vol"
log "  $(sed -n 's/^stamp=//p' "${D}/x/MANIFEST"), $(du -sh "${D}/vol/aos" | cut -f1) de dados do nó"

# Rede PRÓPRIA com os nomes de produção como aliases — ver a nota 1 do cabeçalho.
docker network create "${NET}" >/dev/null 2>&1 || true

log "2/7 Vault restaurado"
UK="$(sed -n 's/.*"keys":\[\?"\([^"]*\)".*/\1/p' "${D}/cfg/secrets/vault-init.json")"
[[ -n "${UK}" ]] || fail "sem chave de unseal no backup — um Vault restaurado ficaria selado para sempre"
docker run -d --name "${PREFIX}-vault" --network "${NET}" --network-alias vault --cpus 1 --cap-add IPC_LOCK \
  -v "${D}/vol/vault:/vault/file" -v /opt/aos/tls-internal/vault:/vault/tls:ro \
  -v /opt/aos/vault/config.hcl:/vault/config/config.hcl:ro \
  hashicorp/vault:1.17 vault server -config=/vault/config/config.hcl >/dev/null
sleep 6
# SEM `| grep -q`: com `set -o pipefail`, o `grep -q` fecha o pipe mal encontra o padrão, o
# comando a montante leva SIGPIPE e sai não-zero, e o PIPELINE INTEIRO falha — apesar de o padrão
# TER SIDO ENCONTRADO. Custou duas iterações a diagnosticar: o Vault desselava sempre, era a
# verificação que mentia. Todas as buscas deste script capturam para variável antes de procurar.
SAIDA="$(docker exec -e VAULT_ADDR=https://127.0.0.1:8200 -e VAULT_SKIP_VERIFY=1 "${PREFIX}-vault" \
  vault operator unseal "${UK}" 2>&1 || true)"
grep -qE 'Sealed +false' <<<"${SAIDA}" || fail "o Vault restaurado NAO desselou: $(tail -2 <<<"${SAIDA}")"
log "  desselado com a chave que veio de dentro do backup"

log "3/7 Postgres + dump do IdP"
PW="$(grep -E '^IDP_DB_PASSWORD=' "${D}/cfg/.env" | cut -d= -f2-)"
docker run -d --name "${PREFIX}-pg" --network "${NET}" --cpus 1 \
  -e POSTGRES_DB=keycloak -e POSTGRES_USER=keycloak -e POSTGRES_PASSWORD="${PW}" \
  postgres:16-alpine >/dev/null
# ESPERA EXPLÍCITA, e falha se não ficar pronto. Sem isto o script seguia em frente, o psql
# rebentava contra um Postgres ainda a arrancar, e o sintoma era "o dump restaurou 0 tabelas" —
# que se lê como "o backup está mau". Aconteceu uma vez e passou na tentativa seguinte, que é o
# pior comportamento possível: um ensaio intermitente ensina a ignorá-lo.
pronto_pg=0
for _ in $(seq 1 60); do
  if docker exec "${PREFIX}-pg" pg_isready -U keycloak >/dev/null 2>&1; then pronto_pg=1; break; fi
  sleep 2
done
[[ "${pronto_pg}" = 1 ]] || fail "o Postgres do ensaio não ficou pronto em 120s (host sob carga?)"
PSQLOUT="$(docker exec -i "${PREFIX}-pg" psql -q -U keycloak -d keycloak < "${D}/x/idp-db.sql" 2>&1 || true)"
TAB="$(docker exec "${PREFIX}-pg" psql -tA -U keycloak -d keycloak \
        -c "select count(*) from information_schema.tables where table_schema='public'" | tr -d ' ')"
[[ "${TAB}" -gt 50 ]] || fail "o dump do IdP restaurou so ${TAB} tabelas. psql disse: $(grep -m3 -iE "error|fatal" <<<"${PSQLOUT}" | tr "
" " ")"
log "  ${TAB} tabelas, realms: $(docker exec "${PREFIX}-pg" psql -tA -U keycloak -d keycloak -c 'select name from realm order by 1' | tr '\n' ' ')"

log "4/7 Keycloak sobre a base restaurada (a JVM demora)"
IDP_IMG="$(docker inspect aos-idp-1 --format '{{.Config.Image}}')"
IDP_CMD="$(docker inspect aos-idp-1 --format '{{join .Config.Cmd " "}}')"   # nunca adivinhado — ver nota 3
IDP_MNT="$(docker inspect aos-idp-1 --format '{{range .Mounts}}-v {{.Source}}:{{.Destination}}:ro {{end}}')"
docker inspect aos-idp-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -E '^(KC_|KEYCLOAK_|JAVA)' > "${D}/env-idp"
sed -i "s#^KC_DB_URL=.*#KC_DB_URL=jdbc:postgresql://${PREFIX}-pg:5432/keycloak#" "${D}/env-idp"
# shellcheck disable=SC2086
docker run -d --name "${PREFIX}-idp" --network "${NET}" --network-alias idp --cpus 2 \
  --env-file "${D}/env-idp" ${IDP_MNT} "${IDP_IMG}" ${IDP_CMD} >/dev/null
pronto=0
for i in $(seq 1 36); do
  sleep 10
  LOGIDP="$(docker logs "${PREFIX}-idp" 2>&1 || true)"
  grep -q "Listening on" <<<"${LOGIDP}" && { pronto=1; log "  arrancou em ~$((i*10))s"; break; }
  ESTIDP="$(docker ps -a --filter "name=${PREFIX}-idp" --format "{{.Status}}" || true)"
  grep -q Exited <<<"${ESTIDP}" && {
    docker logs "${PREFIX}-idp" 2>&1 | tail -5; fail "o Keycloak restaurado SAIU"; }
done
[[ "${pronto}" = 1 ]] || fail "o Keycloak restaurado não ficou pronto"

log "5/7 nó restaurado"
AOS_IMG="$(docker inspect aos-aos-1 --format '{{.Image}}')"
docker inspect aos-aos-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -E '^[A-Z]' > "${D}/env-aos"
sed -i 's#^AOS_DSAR_VAULT_ADDR=.*#AOS_DSAR_VAULT_ADDR=https://vault:8200#' "${D}/env-aos"
sed -i 's#^AOS_SOVEREIGN_OIDC_JWKS_URI=.*#AOS_SOVEREIGN_OIDC_JWKS_URI=https://idp:8443/realms/aos/protocol/openid-connect/certs#' "${D}/env-aos"
docker run -d --name "${PREFIX}-aos" --network "${NET}" --cpus 2 --env-file "${D}/env-aos" \
  -v "${D}/vol/aos:/var/lib/aos" \
  -v "${D}/cfg/model-tools/tools.json:/etc/aos/model-tools.json:ro" \
  -v "${D}/cfg/tls-internal/ca-bundle.crt:/etc/aos/internal-ca.crt:ro" \
  -v "${D}/cfg/secrets/vault-token:/etc/aos/vault-token:ro" \
  -v "${D}/cfg/secrets/model-api.key:/etc/aos/model-api.key:ro" \
  -v "${D}/cfg/secrets/approvers.json:/etc/aos/approvers.json:ro" \
  -v "${D}/cfg/secrets/authority.json:/etc/aos/authority.json:ro" \
  -v "${D}/cfg/policies:/etc/aos/policies:ro" "${AOS_IMG}" >/dev/null
sleep 28
LOGAOS="$(docker logs "${PREFIX}-aos" 2>&1 || true)"
grep -q "bootstrap concluido" <<<"${LOGAOS}" || {
  docker logs "${PREFIX}-aos" 2>&1 | tail -6; fail "o nó restaurado NÃO arrancou"; }
PART="$(docker logs "${PREFIX}-aos" 2>&1 | sed -n 's/.*verificada no arranque (\([0-9]*\) particao.*/\1/p' | head -1)"
log "  arrancou; hash-chain re-encadeada em ${PART} partição(ões)"

log "6/7 leitura autenticada, do IdP restaurado para o nó restaurado"
RID="${RID_PADRAO}"
if [[ -z "${RID}" ]]; then
  RID="$(strings -n 8 "${D}/vol/aos/worm.wal" | sed -n 's#.*"Partition":"gov.read/\([^"]*\)".*#\1#p' | tail -1)"
fi
[[ -n "${RID}" ]] || fail "não encontrei nenhum run no WORM restaurado para ler"
SEC="$(tr -d '\r\n' < "${D}/cfg/secrets/reader-client-secret")"
C()   { docker run --rm --network "${NET}" curlimages/curl:latest -sk "$@"; }
tok() { C -X POST https://idp:8443/realms/aos/protocol/openid-connect/token \
          -d grant_type=client_credentials -d client_id=aos-reader --data-urlencode "client_secret=${SEC}" \
          | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'; }
cod() { C -o /dev/null -w '%{http_code}' "$@" "http://${PREFIX}-aos:8080/runs/${RID}"; }

log "7/7 controlos — é o que distingue 'arrancou' de 'ainda decide'"
TT="$(tok)"; [[ -n "${TT}" ]] || fail "o IdP restaurado não emitiu token com o segredo do backup"
c_ok="$(cod -H "Authorization: Bearer ${TT}")"
c_replay="$(cod -H "Authorization: Bearer ${TT}")"
c_nada="$(cod)"
c_forjado="$(cod -H 'X-Aos-Board: board:prod' -H 'X-Aos-Reader: alguem')"
printf '[ensaio]   run lido ................. %s\n'                 "${RID}"
printf '[ensaio]   token valido ............. HTTP %s  (quero 200)\n' "${c_ok}"
printf '[ensaio]   MESMO token outra vez .... HTTP %s  (quero 404)\n' "${c_replay}"
printf '[ensaio]   sem credencial ........... HTTP %s  (quero 404)\n' "${c_nada}"
printf '[ensaio]   header forjado ........... HTTP %s  (quero 404)\n' "${c_forjado}"

falhas=0
[[ "${c_ok}"      = 200 ]] || { log "  FALHA: a leitura autenticada não passou"; falhas=1; }
[[ "${c_replay}"  = 404 ]] || { log "  FALHA: o anti-replay do jti NÃO voltou"; falhas=1; }
[[ "${c_nada}"    = 404 ]] || { log "  FALHA: leitura SEM credencial passou"; falhas=1; }
[[ "${c_forjado}" = 404 ]] || { log "  FALHA: o header auto-declarado AUTORIZOU"; falhas=1; }
[[ "${falhas}" = 0 ]] || fail "o sistema restaurado arrancou mas NÃO decide como devia"

log "ENSAIO PASSOU — o artefacto levanta o sistema, e as decisões continuam a ser tomadas."
