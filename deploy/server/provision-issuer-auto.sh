#!/usr/bin/env bash
# provision-issuer-auto.sh — prepara o EMISSOR AUTOMÁTICO do NHI no servidor (AOS-437, ADR-033).
#
#   bash /opt/aos/provision-issuer-auto.sh          (como `aos`, depois de provision-identity.sh)
#
# Idempotente: correr de novo verifica em vez de refazer. Faz quatro coisas, e cada uma é
# CONTROLADA depois de feita — emitir e não verificar foi exactamente como o defeito do token do
# nó de 2026-08-19 passou (ver provision-identity.sh §5):
#
#   1. a chave `aos-issuer-auto` no Vault transit: ed25519, NÃO exportável;
#   2. a política `aos-issuer-auto`: SÓ assinar com essa chave (e auto-consulta/renovação);
#   3. um token periódico com essa política, em secrets/vault-issuer-token;
#   4. a pasta /opt/aos/nhi, dona do uid 65532 (o do contentor) e 0700, onde o NHI é escrito.
#
# No fim IMPRIME a pubkey do emissor — o valor de AOS_MANDATED_ISSUER_PUBKEY no .env. Imprime-a o
# PRÓPRIO `aos-issuer`, pela mesma via que assina os tokens: a pubkey que o nó pina vem da mesma
# fonte que o signer, e não de uma conversão feita à parte.
#
# ─── O QUE ISTO NÃO PROTEGE, DITO COM AS MESMAS LETRAS DO ADR-033 ───────────────────────────
# O Vault corre NESTE servidor e destrava-se sozinho. Quem tiver o token, o contentor ou a chave
# transit pede assinaturas — e o nó só as aceita DENTRO do mandato que um humano assinou com a SUA
# chave, que nunca esteve aqui. Esse é o limite. Root neste host muda o .env do nó e não é coberto.

set -Eeuo pipefail

AOS_DIR="${AOS_DIR:-/opt/aos}"
SECRETS="${AOS_DIR}/secrets"
INIT_FILE="${SECRETS}/vault-init.json"
TOKEN_FILE="${SECRETS}/vault-issuer-token"
NHI_DIR="${AOS_DIR}/nhi"
CHAVE="aos-issuer-auto"
ALPINE="alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"

log()  { printf '[issuer-auto] %s\n' "$*"; }
fail() { printf '[issuer-auto] ERRO: %s\n' "$*" >&2; exit 1; }

[[ -s "${INIT_FILE}" ]] || fail "${INIT_FILE} em falta — corra primeiro provision-identity.sh (é ele que inicializa o Vault)"
docker inspect aos-vault-1 >/dev/null 2>&1 || fail "o contentor aos-vault-1 não está a correr"

# O root token passa pelo AMBIENTE do docker (-e SEM valor herda-o), nunca pela linha de comando:
# um argumento aparece no `ps` de qualquer utilizador do host.
ROOT_TOKEN="$(grep -o '"root_token":"[^"]*"' "${INIT_FILE}" | cut -d'"' -f4)"
[[ -n "${ROOT_TOKEN}" ]] || fail "root token ilegível em ${INIT_FILE}"
vaultx() { VAULT_TOKEN="${ROOT_TOKEN}" docker exec -i -e VAULT_TOKEN -e VAULT_ADDR=https://127.0.0.1:8200 \
             -e VAULT_CACERT=/vault/tls/ca.crt aos-vault-1 "$@"; }

# O `-format=json` do CLI é INDENTADO (`"sealed": false`, com espaço); tiram-se os espaços antes de
# comparar, como nas outras leituras deste script. A primeira versão procurava `"sealed":false` e
# recusava um Vault destravado — medido na primeira corrida em produção (2026-09-25).
# Captura para variável e compara em bash, sem `| grep -q`: com pipefail, um consumidor que fecha o
# pipe cedo faz o pipeline falhar APESAR de ter encontrado (o defeito que o backup.sh já documenta).
ESTADO_VAULT="$(vaultx vault status -format=json 2>/dev/null | tr -d ' \n' || true)"
[[ "${ESTADO_VAULT}" == *'"sealed":false'* ]] \
  || fail "o Vault está selado ou não responde — destrave-o antes (vault-unseal)"

# --- 1. a chave -------------------------------------------------------------------------------
log "1/4 chave ${CHAVE} no transit"
if ! vaultx vault read -format=json "transit/keys/${CHAVE}" >/dev/null 2>&1; then
  # exportable=false e allow_plaintext_backup=false são os defaults; ficam escritos para quem ler.
  vaultx vault write -f "transit/keys/${CHAVE}" type=ed25519 exportable=false allow_plaintext_backup=false >/dev/null
  log "  criada"
fi
META="$(vaultx vault read -format=json "transit/keys/${CHAVE}")"
grep -q '"type": "ed25519"' <<<"${META}" || fail "transit/keys/${CHAVE} existe mas não é ed25519 — não se reutiliza uma chave de outro tipo"
grep -q '"exportable": false' <<<"${META}" || fail "transit/keys/${CHAVE} é EXPORTÁVEL — a chave podia sair do Vault; recrie-a"
grep -q '"allow_plaintext_backup": false' <<<"${META}" || fail "transit/keys/${CHAVE} permite backup em claro — a chave podia sair do Vault; recrie-a"
log "  ed25519, não exportável, sem backup em claro"

# --- 2. a política ----------------------------------------------------------------------------
log "2/4 política ${CHAVE}"
vaultx vault policy write "${CHAVE}" - <<POL >/dev/null
# SÓ assinar com a chave do emissor automático, e ler a sua pubkey. Nada de encrypt/decrypt, nada
# das KEKs dos titulares, nada de outras chaves: o que este token faz é o que o mandato limita.
path "transit/sign/${CHAVE}"  { capabilities = ["update"] }
path "transit/keys/${CHAVE}"  { capabilities = ["read"] }
# Auto-consulta e renovação: sem elas o token periódico morre no fim do período, sem aviso
# (a lição do token do nó, 2026-08-19).
path "auth/token/lookup-self" { capabilities = ["read"] }
path "auth/token/renew-self"  { capabilities = ["update"] }
POL
vaultx vault policy read "${CHAVE}" >/dev/null || fail "a política ${CHAVE} não ficou escrita"

# --- 3. o token -------------------------------------------------------------------------------
log "3/4 token em ${TOKEN_FILE}"
if [[ ! -s "${TOKEN_FILE}" ]]; then
  TOK="$(vaultx vault token create -format=json -policy="${CHAVE}" -no-default-policy \
           -period=720h -display-name="${CHAVE}" | grep -o '"client_token": "[^"]*"' | cut -d'"' -f4)"
  [[ -n "${TOK}" ]] || fail "não consegui emitir o token"
  # 0644 DENTRO de secrets/ (0700 do aos): o contentor corre como 65532 e lê-o pelo bind-mount; mais
  # ninguém no host atravessa secrets/. O mesmo precedente do vault-token do nó.
  (umask 022; printf '%s' "${TOK}" > "${TOKEN_FILE}")
  unset TOK
  log "  emitido (periódico, 720h, renovado a cada cunhagem)"
else
  log "  já existia (mantido)"
fi

# CONTROLO — e vale também para um token que JÁ EXISTIA (um ficheiro copiado por engano, ou um
# token com mais políticas do que esta, passaria todos os testes positivos).
#
# O QUE NÃO SE PODE FAZER é testar o negativo com uma operação: uma chave que não existe responde
# 404 com QUALQUER política, e um `encrypt` numa chave inexistente é um *create* (upsert) — com uma
# política larga, o «controlo» criava uma KEK espúria. A primeira versão deste script fazia isso e
# não provava nada (revisão adversarial do AOS-437, achado A1). Pergunta-se à ACL, que responde
# pelo caminho e não pela existência: `sys/capabilities`, com o token lido do STDIN (`token=-`),
# para nunca aparecer na linha de comando.
ISS_TOKEN="$(cat "${TOKEN_FILE}")"
issx() { VAULT_TOKEN="${ISS_TOKEN}" docker exec -i -e VAULT_TOKEN -e VAULT_ADDR=https://127.0.0.1:8200 \
           -e VAULT_CACERT=/vault/tls/ca.crt aos-vault-1 "$@"; }
caps() { printf '%s' "${ISS_TOKEN}" | vaultx vault write -format=json sys/capabilities token=- path="$1" 2>/dev/null \
           | tr -d ' \n' | sed -n 's/.*"capabilities":\[\([^]]*\)\].*/\1/p'; }

# (a) as políticas do token são EXACTAMENTE a deste emissor.
POLS="$(printf '%s' "${ISS_TOKEN}" | vaultx vault write -format=json auth/token/lookup token=- 2>/dev/null \
         | tr -d ' \n' | sed -n 's/.*"policies":\[\([^]]*\)\].*/\1/p')"
[[ "${POLS}" == "\"${CHAVE}\"" ]] || fail "o token em ${TOKEN_FILE} tem as políticas [${POLS}] — quer EXACTAMENTE [\"${CHAVE}\"]; apague o ficheiro e corra de novo (revogue o token antigo com o root)"

# (b) o que precisa, pela ACL e a funcionar.
[[ "$(caps "transit/sign/${CHAVE}")" == *'"update"'* ]] || fail "a ACL não dá update em transit/sign/${CHAVE}"
issx vault token lookup >/dev/null 2>&1 || fail "o token do emissor NÃO consegue lookup-self"
issx vault token renew  >/dev/null 2>&1 || fail "o token do emissor NÃO consegue renew-self — morreria no fim do período"
issx vault write -format=json "transit/sign/${CHAVE}" input=YW9zLTQzNw== >/dev/null 2>&1 \
  || fail "o token do emissor NÃO consegue assinar com ${CHAVE}"

# (c) o que NÃO pode — pela ACL, que não depende de a chave existir.
for proibido in "transit/encrypt/aos-kek-x" "transit/decrypt/aos-kek-x" "transit/keys/aos-kek-x" \
                "transit/keys/${CHAVE}/rotate" "transit/keys/${CHAVE}/config" "transit/export/signing-key/${CHAVE}" \
                "auth/token/create" "sys/policy/aos-node"; do
  c="$(caps "${proibido}")"
  [[ "${c}" == '"deny"' ]] || fail "a ACL do token do emissor dá [${c}] em ${proibido} — quer só deny; a política está larga demais"
done
unset ISS_TOKEN ROOT_TOKEN
log "  controlo pela ACL: políticas exactamente [${CHAVE}], assina e renova-se, e deny nas KEKs, na rotação, na exportação e na emissão de tokens"

# --- 4. a pasta do NHI ------------------------------------------------------------------------
log "4/4 ${NHI_DIR} (uid 65532, 0700)"
mkdir -p "${NHI_DIR}"
docker run --rm --pull=never -v "${NHI_DIR}":/n "${ALPINE}" sh -c 'chown 65532:65532 /n && chmod 0700 /n' \
  || fail "não consegui entregar ${NHI_DIR} ao uid 65532"

# --- a pubkey, pelo próprio emissor -----------------------------------------------------------
# SÓ com o mandato no sítio: o serviço monta ./orq/mandato.json, e um bind de um ficheiro que não
# existe faz o docker CRIAR uma DIRECTORIA de root com esse nome — o scp do mandato falharia a
# seguir e o `-s` dos scripts daria verdadeiro sobre uma directoria (revisão, achado B1).
if [[ -d "${AOS_DIR}/orq/mandato.json" ]]; then
  fail "${AOS_DIR}/orq/mandato.json é uma DIRECTORIA (bind de uma corrida anterior sem o mandato) — remova-a (como root) e copie o mandato"
fi
if [[ ! -f "${AOS_DIR}/orq/mandato.json" ]]; then
  log "FEITO, mas a pubkey NÃO foi impressa: falta ${AOS_DIR}/orq/mandato.json (passo 1 do runbook)."
  log "  Copie o mandato e corra este script outra vez — só então se imprimem as linhas do .env."
  exit 0
fi
if [[ -r "${AOS_DIR}/image.env" ]] && docker compose -f "${AOS_DIR}/docker-compose.prod.yml" \
     --env-file "${AOS_DIR}/.env" --env-file "${AOS_DIR}/image.env" --profile issuer \
     run --rm -T aos-issuer pubkey --vault-addr https://vault:8200 --vault-key "${CHAVE}" \
     --vault-token-path /run/aos-issuer/vault-token --vault-ca /etc/aos/internal-ca.crt \
     </dev/null > "${AOS_DIR}/.issuer-auto.pub" 2>/dev/null; then
  PUB="$(tr -d ' \r\n' < "${AOS_DIR}/.issuer-auto.pub")"
  [[ "${PUB}" =~ ^[0-9a-f]{64}$ ]] || fail "o aos-issuer devolveu uma pubkey malformada: ${PUB}"
  log "FEITO. Acrescente ao ${AOS_DIR}/.env (e reinicie o nó):"
  printf '\n  AOS_MANDATED_ISSUER_ID=iss:aos-issuer-auto\n  AOS_MANDATED_ISSUER_PUBKEY=%s\n  AOS_MANDATE_SIGNERS=<user_id>=<pubkey do humano>\n\n' "${PUB}"
else
  log "FEITO, mas a pubkey NÃO foi impressa: a imagem em ${AOS_DIR}/image.env ainda não traz o aos-issuer"
  log "  (chega com a release do AOS-437). Depois do deploy, corra este script outra vez."
fi
