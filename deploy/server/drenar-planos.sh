#!/usr/bin/env bash
# drenar-planos.sh — drena a fila de pedidos de plano SEM OPERADOR (AOS-437).
#
#   corre-o o timer aos-drenar-planos (systemd/), a cada 5 min
#   à mão:  bash /opt/aos/drenar-planos.sh
#
# O AOS-430 mediu que NADA drenava a fila em produção: o serviço `aos-orq` é `restart: "no"` e o
# `consume` drena uma vez e termina. Este é o «uma vez», repetido. Nunca há duas drenagens em
# simultâneo: o systemd não volta a arrancar um serviço oneshot que ainda está activo, e o WAL do
# `consume` (/var/lib/aos-orq/consume.wal) pede posse sequencial.
#
# RECUSA RECLAMAR SEM CREDENCIAL. Reclamar um pedido e falhar a seguir gasta uma geração por nada
# e fecha o pedido como falhado — por isso, com o NHI ausente ou a menos de NHI_MIN_S do fim, não
# se reclama coisa nenhuma, e a falha fica em `systemctl --failed` (que o sensor lê).

set -Eeuo pipefail

AOS_DIR="${AOS_DIR:-/opt/aos}"
MAX="${DRENAR_MAX:-4}"
NHI_MIN_S="${DRENAR_NHI_MIN_S:-600}"
ALPINE="alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
COMPOSE=(docker compose -f "${AOS_DIR}/docker-compose.prod.yml" --env-file "${AOS_DIR}/.env"
         --env-file "${AOS_DIR}/image.env")

log()  { logger -t aos-drenar-planos "$1" 2>/dev/null || true; printf '[drenar-planos] %s\n' "$1"; }
fail() { log "ERRO: $1"; exit 1; }

# nhi_exp — o `exp` de TOPO do NHI, lido como o uid 65532 (a pasta é dele e 0700), sem rede.
# Ancora em `"exp":N,"jti"` porque o mandato embebido também tem `exp` e só o de topo é seguido de
# `jti`; a forma está fixada por TestAOS437OExpDeTopoESeguidoDoJti (packages/cmd/aos-issuer).
nhi_exp() {
  docker run --rm --pull=never --log-driver none --network none --read-only --cap-drop ALL \
    --security-opt no-new-privileges --user 65532:65532 -v "${AOS_DIR}/nhi":/n:ro "${ALPINE}" sh -c '
      [ -s /n/nhi-run.jwt ] || exit 3
      p=$(cut -d. -f2 /n/nhi-run.jwt | tr "_-" "/+")
      case $(( ${#p} % 4 )) in 2) p="$p==";; 3) p="$p=";; esac
      printf %s "$p" | base64 -d 2>/dev/null | sed -n "s/.*\"exp\":\([0-9]*\),\"jti\".*/\1/p"'
}

[[ -s "${AOS_DIR}/orq/snapshot.json" ]] || fail "sem ${AOS_DIR}/orq/snapshot.json — o planeador precisa do instantâneo de validação"

EXP="$(nhi_exp || true)"
[[ "${EXP}" =~ ^[0-9]+$ ]] || fail "sem NHI legível em ${AOS_DIR}/nhi/nhi-run.jwt — a cunhagem (aos-cunhar-nhi) não correu ou falhou; NÃO se reclama nenhum plano"
RESTA=$(( EXP - $(date +%s) ))
(( RESTA >= NHI_MIN_S )) || fail "o NHI caduca em ${RESTA}s (mínimo ${NHI_MIN_S}s) — a cunhagem parou; NÃO se reclama nenhum plano"

# -T e </dev/null: o `compose run` come o stdin de quem o chama (lição do AOS-403).
"${COMPOSE[@]}" --profile orq run --rm -T \
  -e AOS_ORQ_NODE_CREDENTIAL_FILE=/run/aos-nhi/nhi-run.jwt \
  aos-orq consume --snapshot /etc/aos-orq/snapshot.json --wal /var/lib/aos-orq/consume.wal \
  --max "${MAX}" </dev/null \
  || fail "o consume saiu com erro — ver acima (AOS_ORQ_NODE_URL / AOS_ORQ_OIDC_* no .env? o nó responde?)"

log "drenagem terminada (máximo ${MAX} pedidos; NHI com ${RESTA}s de vida)"
