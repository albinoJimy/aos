# Raiz da IaC dev/staging. Liga os três módulos de fundação.
# Não provisiona lógica de negócio — apenas a infra que os componentes AOS consomem.

provider "docker" {
  host = var.docker_host != "" ? var.docker_host : null
}

# Rede do ambiente (isolamento + egress default-deny quando internal=true).
module "network" {
  source = "./modules/network"

  name     = local.name_prefix
  subnet   = var.network_subnet
  internal = var.network_internal
  labels   = local.common_labels
}

# Event Store (ES) — NATS JetStream, append-only, transporte push (ADR-007).
module "eventstore" {
  source = "./modules/eventstore"

  name         = local.name_prefix
  network_name = module.network.network_name
  image        = var.eventstore_image
  cluster_size = var.eventstore_cluster_size
  client_port  = var.eventstore_client_port
  store_dir    = var.eventstore_store_dir
  labels       = local.common_labels
}

# Credential Broker/Vault (BRK) — cofre de segredos, tokens JIT (ADR-006).
module "secrets" {
  source = "./modules/secrets"

  name         = local.name_prefix
  network_name = module.network.network_name
  image        = var.secrets_image
  dev_mode     = var.secrets_dev_mode
  api_port     = var.secrets_port
  labels       = local.common_labels
}
