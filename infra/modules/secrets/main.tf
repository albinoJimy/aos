# Credential Broker/Vault (BRK) — cofre de segredos com tokens JIT (ADR-006).
# O agente nunca vê o segredo downstream; aqui provisiona-se apenas o cofre.
#
# IMPORTANTE (ADR-006 / DoD "Sem segredos"):
#   - dev  : Vault em modo -dev (in-memory). O root token é GERADO em runtime
#            (random_password), nunca fica num var-file. É descartável e não-produtivo.
#   - stag : Vault server persistente que ARRANCA SELADO. Nenhum material secreto é
#            colocado pela IaC — `vault operator init/unseal` é operação fora-de-banda.

resource "random_password" "dev_root_token" {
  count   = var.dev_mode ? 1 : 0
  length  = 24
  special = false
}

locals {
  # Splat + join nunca indexa fora de alcance quando count=0 (staging).
  dev_root_token = join("", random_password.dev_root_token[*].result)
}

resource "docker_image" "vault" {
  name         = var.image
  keep_locally = true
}

# Volume de dados só no modo persistente (staging).
resource "docker_volume" "data" {
  count = var.dev_mode ? 0 : 1
  name  = "${var.name}-vault-data"

  dynamic "labels" {
    for_each = var.labels
    content {
      label = labels.key
      value = labels.value
    }
  }
}

resource "docker_container" "vault" {
  name    = "${var.name}-vault"
  image   = docker_image.vault.image_id
  restart = "unless-stopped"

  # Modo dev arranca desbloqueado; modo server usa a config injectada abaixo.
  command = var.dev_mode ? ["server", "-dev", "-dev-listen-address=0.0.0.0:8200"] : ["server"]

  env = var.dev_mode ? [
    "VAULT_DEV_ROOT_TOKEN_ID=${local.dev_root_token}",
    "VAULT_ADDR=http://0.0.0.0:8200",
    ] : [
    "VAULT_ADDR=http://0.0.0.0:8200",
  ]

  # Sem `capabilities`: mlock está desativado (dev -dev desativa-o; staging usa
  # disable_mlock=true no vault.hcl), logo IPC_LOCK é desnecessário. Além disso o
  # bloco `capabilities` do provider Docker gera diff fantasma => replace a cada
  # plan (quebra idempotência). Omiti-lo mantém o apply idempotente.
  networks_advanced {
    name    = var.network_name
    aliases = ["${var.name}-vault"]
  }

  ports {
    internal = 8200
    external = var.api_port
  }

  # Config HCL injectada apenas no modo server (staging). Sem segredos — só topologia.
  dynamic "upload" {
    for_each = var.dev_mode ? [] : [1]
    content {
      file    = "/vault/config/vault.hcl"
      content = <<-EOT
        ui            = true
        disable_mlock = true
        api_addr      = "http://0.0.0.0:8200"

        storage "file" {
          path = "/vault/data"
        }

        listener "tcp" {
          address     = "0.0.0.0:8200"
          tls_disable = true
        }
      EOT
    }
  }

  dynamic "volumes" {
    for_each = var.dev_mode ? [] : [1]
    content {
      volume_name    = docker_volume.data[0].name
      container_path = "/vault/data"
    }
  }

  dynamic "labels" {
    for_each = var.labels
    content {
      label = labels.key
      value = labels.value
    }
  }
}
