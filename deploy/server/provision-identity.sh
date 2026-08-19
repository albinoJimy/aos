#!/usr/bin/env bash
# provision-identity.sh — arma as DUAS portas que faltam ao AOS_MODE=production: a credencial
# forte da soberania de leitura (Keycloak) e a custódia durável da KEK (Vault). Fecha ainda a
# credencial do modelo (bearer do LiteLLM por ficheiro).
#
# IDEMPOTENTE. Corre-se as vezes que forem precisas: o que já existe não é regenerado, e nada
# aqui destrói material. Os segredos NASCEM NESTE HOST com `openssl rand` — não vêm do
# repositório, não passam pelo git e não são transportados por ninguém.
#
#   bash /opt/aos/provision-identity.sh
#
# O que NÃO faz, e é deliberado: não cria a sua identidade de leitura no Keycloak. Isso envolve
# escolher uma password, e essa escolha é sua. Ver keycloak/README.md §"Criar a sua identidade".

set -Eeuo pipefail

AOS_DIR="${AOS_DIR:-/opt/aos}"
ENV_FILE="${AOS_DIR}/.env"
SECRETS="${AOS_DIR}/secrets"
COMPOSE="${AOS_DIR}/docker-compose.prod.yml"
IMAGE_ENV="${AOS_DIR}/image.env"

log()  { printf '[identity] %s\n' "$*"; }
fail() { printf '[identity] ERRO: %s\n' "$*" >&2; exit 1; }

dc() { docker compose -f "${COMPOSE}" --env-file "${ENV_FILE}" --env-file "${IMAGE_ENV}" "$@"; }

[[ -f "${ENV_FILE}"  ]] || fail "${ENV_FILE} não existe — corre provision.sh primeiro"
[[ -f "${COMPOSE}"   ]] || fail "${COMPOSE} não existe"
command -v openssl >/dev/null || fail "openssl em falta"

# ------------------------------------------------------------------------------------------
# 1. Segredos gerados NESTE host. `set_env` só escreve se a chave ainda não existir — correr o
#    script outra vez NÃO roda passwords (rodar uma password do Postgres sem migrar os dados
#    partiria o Keycloak, e um script de provisionamento não deve poder fazer isso por acidente).
# ------------------------------------------------------------------------------------------
set_env() {
  local key="$1" val="$2"
  if grep -qE "^${key}=" "${ENV_FILE}" 2>/dev/null; then
    log "  ${key}: já definida (mantida)"
  else
    printf '%s=%s\n' "${key}" "${val}" >> "${ENV_FILE}"
    log "  ${key}: gerada"
  fi
}

log "1/6 segredos locais"
umask 077
set_env IDP_DB_PASSWORD    "$(openssl rand -base64 30 | tr -d '/+=' | head -c 32)"
set_env IDP_ADMIN_USER     "admin"
set_env IDP_ADMIN_PASSWORD "$(openssl rand -base64 30 | tr -d '/+=' | head -c 32)"
set_env IDP_PORT           "9443"
set_env IDP_BIND           "0.0.0.0"
# O `iss` que os tokens vão levar. TEM de ser o URL por onde o OPERADOR alcança o IdP — é o que
# o cliente vê. O nó não o usa para lá chegar (ver AOS_SOVEREIGN_OIDC_JWKS_URI no compose).
set_env IDP_PUBLIC_URL     "https://$(grep -E '^AOS_EDGE_HOST=' "${ENV_FILE}" | cut -d= -f2-):9443"
chmod 600 "${ENV_FILE}"

# NOTA sobre o que este script deliberadamente NÃO liga: AOS_SOVEREIGN_OIDC_* e
# AOS_DSAR_VAULT_*. Definir as primeiras faz o read-path EXIGIR ID-token JÁ — e as leituras por
# header, que hoje funcionam, passariam a ser recusadas antes de existir uma única identidade no
# Keycloak. A passagem é um corte deliberado, feito de uma vez, e não um efeito lateral de
# provisionar infraestrutura.

# O operador precisa de LER as credenciais de administração para criar a identidade de leitura.
# Ficam num ficheiro próprio, 0400, em vez de o obrigar a esgravatar o .env.
if [[ ! -s "${SECRETS}/keycloak-admin.env" ]]; then
  { grep -E '^IDP_ADMIN_(USER|PASSWORD)=' "${ENV_FILE}"
    printf 'IDP_CONSOLE_URL=%s\n' "$(grep -E '^IDP_PUBLIC_URL=' "${ENV_FILE}" | cut -d= -f2-)"
  } > "${SECRETS}/keycloak-admin.env"
  chmod 400 "${SECRETS}/keycloak-admin.env"
  log "  credenciais de admin em ${SECRETS}/keycloak-admin.env (0400)"
fi

# ------------------------------------------------------------------------------------------
# 2. Credencial do modelo. Sem ela, sob produção, o nó apresentaria ao LiteLLM um bearer de DEV
#    EMBEBIDO NO BINÁRIO — idêntico em todos os nós, legível por quem tenha o artefacto e não
#    revogável. O mesmo valor tem de estar dos dois lados: no LiteLLM (que o exige) e num
#    ficheiro que o nó lê.
# ------------------------------------------------------------------------------------------
log "2/6 credencial do modelo"
if [[ ! -s "${SECRETS}/model-api.key" ]]; then
  MK="sk-$(openssl rand -hex 24)"
  printf '%s' "${MK}" > "${SECRETS}/model-api.key"
  chmod 644 "${SECRETS}/model-api.key"      # uid 65532 (non-root) tem de o LER
  touch "${SECRETS}/model.env"; chmod 600 "${SECRETS}/model.env"
  grep -qE '^LITELLM_MASTER_KEY=' "${SECRETS}/model.env" \
    || printf 'LITELLM_MASTER_KEY=%s\n' "${MK}" >> "${SECRETS}/model.env"
  unset MK
  log "  master key gerada e espelhada nos dois lados"
else
  log "  já existia (mantida)"
fi
# Placeholder do token do Vault: o compose monta este ficheiro SEMPRE, e um bind em falta impede
# o arranque. A secção 5 substitui-o pelo token real.
[[ -s "${SECRETS}/vault-token" ]] || { printf 'placeholder-ate-init' > "${SECRETS}/vault-token"; chmod 644 "${SECRETS}/vault-token"; }

# ------------------------------------------------------------------------------------------
# 3. Pré-condições de TLS. Sem estes ficheiros o Keycloak e o Vault não sobem, e o nó não teria
#    como confiar em nenhum dos dois.
# ------------------------------------------------------------------------------------------
log "3/6 material TLS interno"
for f in tls-internal/ca-bundle.crt tls-internal/idp/idp.crt tls-internal/idp/idp.key \
         tls-internal/vault/vault.crt tls-internal/vault/vault.key tls-internal/vault/ca.crt; do
  [[ -s "${AOS_DIR}/${f}" ]] || fail "${f} em falta — gera a CA interna na máquina do operador e envia o material (ver README §\"CA interna\")"
done
log "  presente"

# ------------------------------------------------------------------------------------------
# 4. Subir os serviços de identidade e custódia. O nó NÃO é tocado aqui: continua a servir com a
#    configuração que tem. A passagem a produção é um passo separado e deliberado.
# ------------------------------------------------------------------------------------------
log "4/6 a subir idp-db, idp e vault"
dc up -d --no-build idp-db idp vault
log "  a aguardar o Keycloak (o arranque da JVM neste host demora ~1-2 min) ..."
IDP_URL="$(grep -E '^IDP_PUBLIC_URL=' "${ENV_FILE}" | cut -d= -f2-)"
IDP_LOCAL="https://127.0.0.1:$(grep -E '^IDP_PORT=' "${ENV_FILE}" | cut -d= -f2-)"
for _ in $(seq 1 40); do
  [[ "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 8 "${IDP_LOCAL}/realms/aos" 2>/dev/null)" == "200" ]] && break
  sleep 6
done

# ------------------------------------------------------------------------------------------
# 4b. DECLARAR `board` NO USER PROFILE. Sem isto o Keycloak 24+ DESCARTA-O EM SILÊNCIO: o
#     realm só declara username/email/firstName/lastName e atributos não-declarados são
#     rejeitados por omissão. O sintoma seria cruel — cria-se o leitor, define-se o board, o
#     Keycloak aceita o pedido, e depois TODA a leitura é negada com "id-token verificado sem
#     claim board", que parece um bug do nó e não é.
#     Fica aqui e não no realm-aos.json porque a importação é SALTADA quando o realm já existe
#     (Strategy: IGNORE_EXISTING) — um realm provisionado antes desta correcção nunca a
#     receberia. Aqui é idempotente e alcança ambos os casos.
#     A validação por padrão faz o IdP RECUSAR um board malformado à cabeça, e `required`
#     impede que se crie um leitor sem fronteira nenhuma.
# ------------------------------------------------------------------------------------------
log "4b/6 a declarar o atributo `board` no user profile do realm"
ADMTOK="$(curl -sk --max-time 20 -X POST "${IDP_LOCAL}/realms/master/protocol/openid-connect/token" \
  -d grant_type=password -d client_id=admin-cli \
  -d "username=$(grep -E '^IDP_ADMIN_USER=' "${ENV_FILE}" | cut -d= -f2-)" \
  --data-urlencode "password=$(grep -E '^IDP_ADMIN_PASSWORD=' "${ENV_FILE}" | cut -d= -f2-)" \
  | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)"
if [[ -n "${ADMTOK}" ]]; then
  curl -sk --max-time 20 "${IDP_LOCAL}/admin/realms/aos/users/profile" \
    -H "Authorization: Bearer ${ADMTOK}" > /tmp/aos-profile.json
  python3 - <<'PY' > /tmp/aos-profile-new.json
import json
p = json.load(open('/tmp/aos-profile.json'))
if 'board' not in [a['name'] for a in p.get('attributes', [])]:
    p.setdefault('attributes', []).append({
        "name": "board",
        "displayName": "Board de soberania (AOS)",
        "multivalued": False,
        "permissions": {"view": ["admin"], "edit": ["admin"]},
        "required": {"roles": ["user"]},
        "validations": {"pattern": {
            "pattern": "^board:[a-z0-9][a-z0-9-]*$",
            "error-message": "tem de ser board:<nome>, e constar de AOS_BOARD_REGIONS no no"}},
        "annotations": {"inputType": "text"},
        "group": "user-metadata",
    })
print(json.dumps(p))
PY
  RC="$(curl -sk --max-time 20 -o /dev/null -w '%{http_code}' -X PUT \
    "${IDP_LOCAL}/admin/realms/aos/users/profile" -H "Authorization: Bearer ${ADMTOK}" \
    -H 'Content-Type: application/json' --data-binary @/tmp/aos-profile-new.json)"
  rm -f /tmp/aos-profile.json /tmp/aos-profile-new.json
  [[ "${RC}" == "200" ]] && log "  declarado (HTTP ${RC})" || log "  ATENÇÃO: HTTP ${RC} — verifique manualmente"
  unset ADMTOK
else
  log "  ATENÇÃO: não obtive token de admin — o atributo board NÃO foi declarado."
  log "           Sem ele o Keycloak descarta o board em silêncio e nenhuma leitura passa."
fi

log "  a aguardar o Vault ..."
for _ in $(seq 1 30); do
  docker exec aos-vault-1 wget -q --no-check-certificate -O- \
    'https://127.0.0.1:8200/v1/sys/health?sealedcode=200&uninitcode=200' >/dev/null 2>&1 && break
  sleep 3
done

# ------------------------------------------------------------------------------------------
# 4c. CLIENTE `aos-reader` — a identidade de MÁQUINA que chama a API.
#
#     Existe porque, sob AOS_MODE=production, TODA a chamada precisa de um token verificado —
#     leituras E submissões. Sem uma identidade provisionada, o nó não aceita nada: não é
#     degradação parcial, é a API fechada.
#
#     O `board` vem do ATRIBUTO do utilizador de service account, não de um
#     oidc-hardcoded-claim-mapper. A diferença não é cosmética: com o mapper fixo, a fronteira
#     de soberania passa a ser uma constante na configuração do CLIENTE, e duas identidades do
#     mesmo cliente nunca poderiam ter boards diferentes — que é exactamente o defeito do realm
#     de dev, onde `board:demo` está cravado.
#
#     O segredo é GERADO pelo Keycloak e escrito num ficheiro 0400. Ninguém o escolhe.
# ------------------------------------------------------------------------------------------
log "4c/6 cliente aos-reader (service account)"
if [[ -n "${ADMTOK:-}" ]] || ADMTOK="$(curl -sk --max-time 20 -X POST "${IDP_LOCAL}/realms/master/protocol/openid-connect/token" \
      -d grant_type=password -d client_id=admin-cli \
      -d "username=$(grep -E '^IDP_ADMIN_USER=' "${ENV_FILE}" | cut -d= -f2-)" \
      --data-urlencode "password=$(grep -E '^IDP_ADMIN_PASSWORD=' "${ENV_FILE}" | cut -d= -f2-)" \
      | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)"; then :; fi

kc() { curl -sk --max-time 20 -H "Authorization: Bearer ${ADMTOK}" "$@"; }
READER_CID="$(kc "${IDP_LOCAL}/admin/realms/aos/clients?clientId=aos-reader" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)"
if [[ -z "${READER_CID}" ]]; then
  # `aud` TEM de ser aos-node e de VALOR ÚNICO: com múltiplas audiences o verificador do nó
  # passa a exigir que `azp` seja o client configurado — e o azp aqui é aos-reader, não
  # aos-node. Daí defaultClientScopes/optionalClientScopes vazios e fullScopeAllowed false.
  kc -o /dev/null -X POST "${IDP_LOCAL}/admin/realms/aos/clients" -H 'Content-Type: application/json' -d '{
    "clientId":"aos-reader","name":"AOS — leitor de servico (client credentials)",
    "enabled":true,"publicClient":false,"protocol":"openid-connect",
    "standardFlowEnabled":false,"implicitFlowEnabled":false,"directAccessGrantsEnabled":false,
    "serviceAccountsEnabled":true,"fullScopeAllowed":false,
    "defaultClientScopes":[],"optionalClientScopes":[],
    "attributes":{"access.token.lifespan":"300"},
    "protocolMappers":[
      {"name":"board-claim","protocol":"openid-connect","protocolMapper":"oidc-usermodel-attribute-mapper",
       "config":{"user.attribute":"board","claim.name":"board","jsonType.label":"String",
                 "id.token.claim":"true","access.token.claim":"true","multivalued":"false"}},
      {"name":"aud-aos-node","protocol":"openid-connect","protocolMapper":"oidc-audience-mapper",
       "config":{"included.client.audience":"aos-node","access.token.claim":"true"}}]}'
  READER_CID="$(kc "${IDP_LOCAL}/admin/realms/aos/clients?clientId=aos-reader" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)"
  [[ -n "${READER_CID}" ]] || fail "nao consegui criar o cliente aos-reader"
  log "  criado"
else
  log "  ja existia (mantido)"
fi

# O board vive no utilizador de service account. Sem ele o token sai SEM claim `board` e toda a
# chamada e negada com "id-token verificado sem claim board" — que parece um bug do no.
SA_ID="$(kc "${IDP_LOCAL}/admin/realms/aos/clients/${READER_CID}/service-account-user" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)"
READER_BOARD="$(grep -E '^AOS_BOARD_REGIONS=' "${ENV_FILE}" | cut -d= -f2- | cut -d= -f1 | cut -d, -f1)"
kc -o /dev/null -X PUT "${IDP_LOCAL}/admin/realms/aos/users/${SA_ID}" -H 'Content-Type: application/json' \
  -d "{\"attributes\":{\"board\":[\"${READER_BOARD}\"]}}"
log "  board do leitor = ${READER_BOARD} (atributo da identidade, nao constante do cliente)"

if [[ ! -s "${SECRETS}/reader-client-secret" ]]; then
  kc "${IDP_LOCAL}/admin/realms/aos/clients/${READER_CID}/client-secret" \
    | grep -o '"value":"[^"]*"' | cut -d'"' -f4 > "${SECRETS}/reader-client-secret"
  chmod 400 "${SECRETS}/reader-client-secret"
  [[ -s "${SECRETS}/reader-client-secret" ]] || fail "nao consegui obter o segredo do aos-reader"
  log "  segredo em ${SECRETS}/reader-client-secret (0400)"
fi
unset ADMTOK

# ------------------------------------------------------------------------------------------
# 5. Vault: init -> unseal -> Transit -> token scoped. Tudo idempotente.
# ------------------------------------------------------------------------------------------
log "5/6 Vault: init/unseal/Transit"
INIT_FILE="${SECRETS}/vault-init.json"
v() { docker exec aos-vault-1 wget -q --no-check-certificate -O- "$@" 2>/dev/null; }
vpost() { docker exec -i aos-vault-1 wget -q --no-check-certificate -O- --header="$2" --post-data="$3" "$1" 2>/dev/null; }

STATUS="$(v 'https://127.0.0.1:8200/v1/sys/seal-status' || true)"
if ! grep -q '"initialized":true' <<<"${STATUS}"; then
  log "  não inicializado — operator init"
  vpost 'https://127.0.0.1:8200/v1/sys/init' 'X-Vault-Request: true' \
    '{"secret_shares":1,"secret_threshold":1}' > "${INIT_FILE}"
  chmod 400 "${INIT_FILE}"
  grep -q '"root_token"' "${INIT_FILE}" || fail "init do Vault falhou — ver ${INIT_FILE}"
fi
[[ -s "${INIT_FILE}" ]] || fail "Vault já inicializado mas ${INIT_FILE} não existe — sem o material de unseal não há como o destravar"

UNSEAL_KEY="$(grep -o '"keys":\["[^"]*"' "${INIT_FILE}" | cut -d'"' -f4)"
ROOT_TOKEN="$(grep -o '"root_token":"[^"]*"' "${INIT_FILE}" | cut -d'"' -f4)"
[[ -n "${UNSEAL_KEY}" && -n "${ROOT_TOKEN}" ]] || fail "material de init ilegível (${INIT_FILE})"

if grep -q '"sealed":true' <<<"$(v 'https://127.0.0.1:8200/v1/sys/seal-status')"; then
  log "  selado — unseal"
  vpost 'https://127.0.0.1:8200/v1/sys/unseal' 'X-Vault-Request: true' "{\"key\":\"${UNSEAL_KEY}\"}" >/dev/null
fi
grep -q '"sealed":false' <<<"$(v 'https://127.0.0.1:8200/v1/sys/seal-status')" || fail "o Vault continua selado"

# Motor Transit (204 = criado, 400 = já existia — ambos aceitáveis).
vpost 'https://127.0.0.1:8200/v1/sys/mounts/transit' "X-Vault-Token: ${ROOT_TOKEN}" '{"type":"transit"}' >/dev/null || true

# Política least-privilege: SÓ as operações Transit que o nó precisa. O root token nunca chega
# ao nó — fica em vault-init.json, para operações de administração.
#
# Escrita pelo CLI `vault` e NÃO por wget: o BusyBox wget desta imagem não suporta `--method`
# nem `--body-data`, pelo que o PUT falhava em silêncio. O sintoma era cruel — o token era
# emitido com uma política INEXISTENTE, o Vault devolvia 403 a tudo, e o run terminava com
# "custódia da KEK no Vault falhou: criar chave: status 403", que parece problema de permissões
# mal escritas e é uma política que nunca chegou a existir. Daí também o `set -e` valer aqui:
# esta chamada NÃO leva `|| true`.
vaultx() { docker exec -i -e VAULT_TOKEN="${ROOT_TOKEN}" -e VAULT_ADDR=https://127.0.0.1:8200 \
             -e VAULT_CACERT=/vault/tls/ca.crt aos-vault-1 "$@"; }
vaultx vault policy write aos-node - <<'POL' >/dev/null
# AUTO-CONSULTA E RENOVAÇÃO. Sem estes dois caminhos o token é emitido com `no_default_policy` e
# fica SEM eles — é a política `default` que normalmente os concede. Duas consequências, e a
# segunda é silenciosa:
#
#   · `lookup-self` negado ⇒ a sonda de saúde do nó recebe 403 e, até 2026-08-19, lia-o como
#     «token expirado/revogado»: /readyz VERMELHO sobre um token perfeitamente bom;
#   · `renew-self` negado ⇒ o token PERIÓDICO nunca é renovado e MORRE no fim do período, sem
#     que nada avise antes.
#
# Foi observado em produção: o token de 2026-08-15 (período 720h) nunca pôde ser renovado.
path "auth/token/lookup-self" { capabilities = ["read"] }
path "auth/token/renew-self"  { capabilities = ["update"] }
path "transit/keys" { capabilities = ["list"] }
path "transit/keys/aos-kek-*" { capabilities = ["create","read","update","delete"] }
path "transit/keys/aos-kek-*/config" { capabilities = ["update"] }
path "transit/encrypt/aos-kek-*" { capabilities = ["update"] }
path "transit/decrypt/aos-kek-*" { capabilities = ["update"] }
POL
vaultx vault policy read aos-node >/dev/null || fail "a politica aos-node nao ficou escrita — o token do no ficaria sem permissao nenhuma"
log "  politica aos-node escrita e confirmada"

if [[ "$(cat "${SECRETS}/vault-token" 2>/dev/null)" == "placeholder-ate-init" ]]; then
  TOK="$(vpost 'https://127.0.0.1:8200/v1/auth/token/create' "X-Vault-Token: ${ROOT_TOKEN}" \
    '{"policies":["aos-node"],"no_default_policy":true,"period":"720h","display_name":"aos-node"}' \
    | grep -o '"client_token":"[^"]*"' | cut -d'"' -f4)"
  [[ -n "${TOK}" ]] || fail "não consegui emitir o token scoped"
  printf '%s' "${TOK}" > "${SECRETS}/vault-token"; chmod 644 "${SECRETS}/vault-token"
  unset TOK
  log "  token NÃO-ROOT least-privilege emitido"
else
  log "  token do nó já existia (mantido)"
fi

# CONTROLO — com o token DO NÓ, não com a raiz. Emitir um token e não verificar o que ele
# consegue fazer foi exactamente como o defeito de 2026-08-19 passou: a política estava escrita,
# o token estava emitido, e faltavam-lhe os dois caminhos de que o nó depende para se manter vivo.
#
# Fail-closed: se o token não se consegue auto-consultar, a custódia da KEK arranca já a caminho
# de uma morte silenciosa, e é melhor sabê-lo aqui do que daqui a 30 dias.
NODE_TOK="$(cat "${SECRETS}/vault-token")"
nodex() { docker exec -i -e VAULT_TOKEN="${NODE_TOK}" -e VAULT_ADDR=https://127.0.0.1:8200 \
            -e VAULT_CACERT=/vault/tls/ca.crt aos-vault-1 "$@"; }
nodex vault token lookup >/dev/null 2>&1 \
  || fail "o token do no NAO consegue lookup-self — a sonda de saude lera 403 e o /readyz ficara VERMELHO sobre um token bom; confirme os paths auth/token/* na politica aos-node"
nodex vault token renew >/dev/null 2>&1 \
  || fail "o token do no NAO consegue renew-self — um token periodico que nunca e renovado MORRE no fim do periodo, sem aviso"
nodex vault list transit/keys >/dev/null 2>&1 \
  || fail "o token do no NAO consegue listar transit/keys — a prova de capacidade da sonda falharia"
unset NODE_TOK
log "  token do no verificado: lookup-self, renew-self e list transit/keys PASSAM"
unset ROOT_TOKEN UNSEAL_KEY

# ------------------------------------------------------------------------------------------
# 6. Unseal automático ao arranque. DECISÃO DECLARADA, não escondida: com storage `file` o Vault
#    sobe SELADO após cada restart do host. Sem auto-unseal, um reboot não vigiado deixa o nó
#    incapaz de decifrar o conteúdo dos runs até alguém agir manualmente. Com auto-unseal, o
#    material de unseal vive NESTE host — quem tiver root aqui destrava o Vault, pelo que o selo
#    protege contra roubo do VOLUME, não contra compromisso da MÁQUINA.
#    A alternativa séria é auto-unseal por KMS/HSM externo, que este servidor não tem.
# ------------------------------------------------------------------------------------------
log "6/6 unseal automático ao arranque"
if [[ ! -s "${AOS_DIR}/vault-unseal.sh" ]]; then
  cat > "${AOS_DIR}/vault-unseal.sh" <<'UNSEAL'
#!/usr/bin/env bash
# Destrava o Vault após um restart. Ver provision-identity.sh §6 para o trade-off.
set -euo pipefail
INIT_FILE=/opt/aos/secrets/vault-init.json
[[ -s "${INIT_FILE}" ]] || { echo "sem material de unseal"; exit 1; }
for _ in $(seq 1 40); do
  docker exec aos-vault-1 wget -q --no-check-certificate -O- \
    'https://127.0.0.1:8200/v1/sys/health?sealedcode=200&uninitcode=200' >/dev/null 2>&1 && break
  sleep 3
done
S="$(docker exec aos-vault-1 wget -q --no-check-certificate -O- https://127.0.0.1:8200/v1/sys/seal-status 2>/dev/null || true)"
grep -q '"sealed":false' <<<"${S}" && { echo "já destravado"; exit 0; }
K="$(grep -o '"keys":\["[^"]*"' "${INIT_FILE}" | cut -d'"' -f4)"
docker exec -i aos-vault-1 wget -q --no-check-certificate -O- \
  --header='X-Vault-Request: true' --post-data="{\"key\":\"${K}\"}" \
  https://127.0.0.1:8200/v1/sys/unseal >/dev/null
echo "destravado"
UNSEAL
  chmod 700 "${AOS_DIR}/vault-unseal.sh"
  log "  ${AOS_DIR}/vault-unseal.sh criado"
else
  log "  já existia"
fi

log ""
log "FEITO. O nó continua na configuração actual — passar a produção é o passo seguinte."
log "Antes disso, e SEM ISTO ninguém lê runs: crie a identidade de leitura no Keycloak"
log "(realm aos, atributo board=board:prod). Ver keycloak/README.md."
