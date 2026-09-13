#!/usr/bin/env bash
# =============================================================================
# gen-litellm-tls.sh — CA dedicada + certificado TLS do serviço `litellm`.
# Corre na MÁQUINA DO OPERADOR, nunca no servidor.
#
#   bash deploy/server/gen-litellm-tls.sh
#
# PORQUE EXISTE. Sob AOS_MODE=production o nó exige AOS_MODEL_ENDPOINT em https (AOS-366), e a
# LiteLLM do compose serve http. Este script produz o material para a pôr em TLS
# (LITELLM_TLS_ARGS no .env — ver docker-compose.prod.yml, serviço `litellm`).
#
# PORQUE UMA CA DEDICADA, e não a CA interna que assinou `idp` e `vault`: a chave privada dessa CA
# não está disponível. Um bundle de confiança aceita várias CAs, pelo que se ACRESCENTA esta ao
# bundle e os certificados existentes continuam a validar pela CA antiga.
#
# NAME CONSTRAINTS (permitted DNS:litellm). O nó confia no bundle para todo o TLS que faz
# (SSL_CERT_FILE é o trust store do processo inteiro), não só para o modelo. Sem restrição de nome,
# quem obtivesse esta chave de CA podia emitir um certificado para `idp` ou `vault` e personificá-los
# perante o nó. Com a restrição, o pior que esta CA consegue é personificar a própria LiteLLM.
#
# CUSTÓDIA. ca.key fica em secrets-local/ (git-ignored), ao lado das outras chaves de autoridade.
# Só litellm.crt, litellm.key e ca.crt viajam para o servidor.
#
# Idempotente quanto à CA: se ca.key já existir, é reutilizada e só a folha é re-emitida (é o
# caminho de renovação, 397 dias).
# =============================================================================
set -euo pipefail

OUT="${OUT:-$(cd "$(dirname "$0")" && pwd)/secrets-local/litellm-ca}"
CA_DAYS="${CA_DAYS:-1825}"
LEAF_DAYS="${LEAF_DAYS:-397}"
SAN_DNS="${SAN_DNS:-litellm}"

log()  { printf '\033[36m[litellm-tls]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[litellm-tls] FAIL:\033[0m %s\n' "$*" >&2; exit 1; }

# run corre um comando e, se falhar, mostra o stderr REAL antes de devolver erro. O openssl escreve
# o progresso da geração de chaves (linhas só de `.`, `+` e `*`) no stderr, que se filtra; tudo o
# resto passa. Um palpite sobre a causa em vez do erro real já custou um diagnóstico errado aqui.
run() {
  local err
  if ! err="$("$@" 2>&1 >/dev/null)"; then
    printf '%s\n' "${err}" | grep -vE '^[.+*]+$' >&2 || true
    return 1
  fi
}

command -v openssl >/dev/null || fail "openssl não encontrado"

# GIT BASH (MSYS) E O OPENSSL NATIVO. O openssl do Git para Windows é um binário MinGW que só
# entende caminhos Windows. O MSYS converte argumentos que começam por "/" — o que partia o
# `-subj "/CN=..."` (chegava como "C:/Program Files/Git/CN=...") — e por isso a conversão
# desliga-se. Mas desligá-la vale para TODOS os argumentos: um OUT em forma MSYS (/c/Users/...)
# chegava literal ao openssl, que não o encontra. Resolve-se passando OUT para a forma mista
# (C:/Users/...), que o bash e o openssl nativo aceitam os dois. Em Linux/macOS não há cygpath e
# nada disto corre.
if command -v cygpath >/dev/null 2>&1; then
  export MSYS_NO_PATHCONV=1
  OUT="$(cygpath -m "${OUT}")"
fi

umask 077
mkdir -p "${OUT}"

if [ -s "${OUT}/ca.key" ] && [ -s "${OUT}/ca.crt" ]; then
  log "CA existente reutilizada: ${OUT}/ca.crt"
else
  log "a gerar CA dedicada (${CA_DAYS} dias, restrita a DNS:${SAN_DNS}) ..."
  run openssl req -x509 -new -nodes -newkey rsa:3072 -sha256 -days "${CA_DAYS}" \
    -keyout "${OUT}/ca.key" -out "${OUT}/ca.crt" \
    -subj "/CN=AOS litellm CA" \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -addext "nameConstraints=critical,permitted;DNS:${SAN_DNS}" \
    || fail "geração da CA falhou (erro do openssl acima)"
fi

log "a emitir a folha para DNS:${SAN_DNS} (${LEAF_DAYS} dias) ..."
run openssl req -new -nodes -newkey rsa:2048 -sha256 \
  -keyout "${OUT}/litellm.key" -out "${OUT}/litellm.csr" -subj "/CN=${SAN_DNS}" \
  || fail "CSR falhou (erro do openssl acima)"

cat > "${OUT}/litellm.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:${SAN_DNS}
EOF

run openssl x509 -req -in "${OUT}/litellm.csr" -CA "${OUT}/ca.crt" -CAkey "${OUT}/ca.key" \
  -CAcreateserial -days "${LEAF_DAYS}" -sha256 -extfile "${OUT}/litellm.ext" \
  -out "${OUT}/litellm.crt" \
  || fail "assinatura da folha falhou (erro do openssl acima)"
rm -f "${OUT}/litellm.csr" "${OUT}/litellm.ext"

# A verificação usa a MESMA cadeia que o nó vai usar, e falha em voz alta: um certificado que não
# valida aqui dava um nó que recusa arrancar lá, com uma mensagem de TLS e não de configuração.
run openssl verify -CAfile "${OUT}/ca.crt" "${OUT}/litellm.crt" \
  || fail "a folha NÃO valida contra a CA — não enviar"

log "✅ material pronto em ${OUT}"
log "   validade da folha: $(openssl x509 -noout -enddate -in "${OUT}/litellm.crt" | cut -d= -f2)"
cat <<EOF

Próximos passos (servidor, como root ou aos):
  1. mkdir -p /opt/aos/tls-internal/litellm
  2. copiar ${OUT}/{litellm.crt,litellm.key} para /opt/aos/tls-internal/litellm/
  3. ACRESCENTAR ${OUT}/ca.crt ao ficheiro apontado por AOS_INTERNAL_CA_BUNDLE
     (acrescentar com >>, nunca substituir: o bundle leva as raízes públicas e a CA do idp/vault)
  4. no /opt/aos/.env:
       LITELLM_TLS_ARGS=--ssl_certfile_path /app/tls/litellm.crt --ssl_keyfile_path /app/tls/litellm.key
       AOS_MODEL_ENDPOINT=https://litellm:4000/v1
  NÃO copiar ca.key para lado nenhum.
EOF
