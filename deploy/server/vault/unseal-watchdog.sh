#!/bin/sh
# Watchdog de unseal do Vault. Corre como serviço do compose, não como unidade systemd — e isso
# é uma vantagem, não uma cedência: não exige root para instalar, sobe com a stack, e destrava o
# Vault sempre que ele estiver selado, não só depois de um reboot.
#
# ⚠️ O TRADE-OFF, declarado. Com storage `file` o Vault sobe SELADO. Para o nó conseguir decifrar
# o conteúdo dos runs sem intervenção humana, a chave de unseal tem de estar acessível À MÁQUINA.
# Consequência: o selo protege contra roubo do VOLUME (as KEKs em repouso são inúteis sem a
# chave), mas NÃO contra compromisso desta MÁQUINA — quem tiver root aqui destrava o Vault.
#
# A alternativa séria é auto-unseal por KMS/HSM externo (o seal fica fora do host), que este
# servidor não tem. A outra alternativa é unseal MANUAL, que troca esta exposição por
# indisponibilidade: um reboot não vigiado deixaria o nó incapaz de decifrar até alguém agir.
# Escolheu-se a disponibilidade, e fica escrito porquê.

set -eu
ADDR="${VAULT_ADDR:-https://vault:8200}"
INIT_FILE=/vault/init/vault-init.json
INTERVAL="${UNSEAL_INTERVAL:-30}"

[ -s "$INIT_FILE" ] || { echo "[unseal] sem material de unseal em $INIT_FILE — a sair"; exit 1; }
KEY=$(sed 's/.*"keys":\["\([^"]*\)".*/\1/' "$INIT_FILE")
[ -n "$KEY" ] || { echo "[unseal] material de unseal ilegivel"; exit 1; }

echo "[unseal] watchdog activo (${ADDR}, a cada ${INTERVAL}s)"
while :; do
  # `|| true`: o Vault pode ainda não estar a responder (arranque, restart). Isso NÃO é erro —
  # é a condição normal que este watchdog existe para atravessar.
  S=$(wget -q --no-check-certificate -O- "${ADDR}/v1/sys/seal-status" 2>/dev/null || true)
  case "$S" in
    *'"sealed":true'*)
      echo "[unseal] Vault SELADO — a destravar"
      wget -q --no-check-certificate -O- --header='X-Vault-Request: true' \
        --post-data="{\"key\":\"${KEY}\"}" "${ADDR}/v1/sys/unseal" >/dev/null 2>&1 \
        && echo "[unseal] destravado" || echo "[unseal] FALHOU o unseal"
      ;;
    *'"sealed":false'*) : ;;                    # normal, silencioso
    *) echo "[unseal] Vault sem resposta — a aguardar" ;;
  esac
  sleep "$INTERVAL"
done
