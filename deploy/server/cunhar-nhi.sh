#!/usr/bin/env bash
# cunhar-nhi.sh — cunha o NHI do run SEM OPERADOR, dentro do mandato (AOS-437, ADR-033).
#
#   corre-o o timer aos-cunhar-nhi (systemd/), a cada 15 min
#   à mão:  bash /opt/aos/cunhar-nhi.sh
#
# O NHI dura 45 min (o máximo do mandato, ≤ 1h do tecto da biblioteca) e é recunhado a cada 15:
# o ficheiro tem sempre entre 30 e 45 min de vida, e o `aos-orq consume` relê-o a cada submissão.
# Uma cunhagem falhada NÃO invalida o token anterior — há 30 min de folga até o sensor
# (alerta-nhi.sh) avisar e a drenagem (drenar-planos.sh) se recusar a reclamar planos.
#
# Sem Restart no systemd: uma falha fica em `systemctl --failed`, que o sensor lê.

set -Eeuo pipefail

AOS_DIR="${AOS_DIR:-/opt/aos}"
COMPOSE=(docker compose -f "${AOS_DIR}/docker-compose.prod.yml" --env-file "${AOS_DIR}/.env"
         --env-file "${AOS_DIR}/image.env")

log()  { logger -t aos-cunhar-nhi "$1" 2>/dev/null || true; printf '[cunhar-nhi] %s\n' "$1"; }
fail() { log "ERRO: $1"; exit 1; }

[[ -f "${AOS_DIR}/orq/mandato.json" && -s "${AOS_DIR}/orq/mandato.json" ]] || fail "sem mandato em ${AOS_DIR}/orq/mandato.json — o humano assina-o na sua máquina (aos-issuer mandate-sign) e copia-o para aqui"
[[ -s "${AOS_DIR}/secrets/vault-issuer-token" ]] || fail "sem token do emissor — corra provision-issuer-auto.sh"
[[ -d "${AOS_DIR}/nhi" ]] || fail "sem ${AOS_DIR}/nhi — corra provision-issuer-auto.sh"

# 1. RENOVAR o token periódico do emissor. Sem isto morre ao fim de 720h. Uma falha aqui não
#    impede esta cunhagem (o token pode ainda estar válido) — mas fica no journal e o sensor vê a
#    cunhagem falhar quando o token morrer, com semanas de avanço.
if ! VAULT_TOKEN="$(cat "${AOS_DIR}/secrets/vault-issuer-token")" docker exec -i -e VAULT_TOKEN \
     -e VAULT_ADDR=https://127.0.0.1:8200 -e VAULT_CACERT=/vault/tls/ca.crt aos-vault-1 \
     vault token renew >/dev/null 2>&1; then
  log "AVISO: renew-self do token do emissor falhou — a cunhagem segue, mas o token morre no fim do período se isto persistir"
fi

# 2. CUNHAR. -T e </dev/null: o `compose run` come o stdin de quem o chama (lição do AOS-403).
"${COMPOSE[@]}" --profile issuer run --rm -T aos-issuer </dev/null \
  || fail "o mint-mandated falhou — ver a linha acima (mandato fora do prazo? chave do humano que não verifica? Vault?)"

log "NHI cunhado em ${AOS_DIR}/nhi/nhi-run.jwt"
