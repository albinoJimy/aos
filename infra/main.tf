# Raiz da IaC dev/staging. Materializa a TOPOLOGIA de referência (tecnica/10 §3–4):
# separação física entre PLANO DE CONTROLO (decide: ORQ/SCH/ADM/PDP) e PLANO DE DADOS
# (executa e regista: workers stateless, pool de microVMs, Event Store, audit WORM),
# cada um com a sua rede default-deny e a sua contagem de réplicas (escala independente).
# Não provisiona lógica de negócio — apenas a infra e os guardrails que os componentes consomem.

provider "docker" {
  host = var.docker_host != "" ? var.docker_host : null
}

# Guardrail de soberania — 2.ª camada, legível no output (ADR-011).
# A aplicação fail-closed já vive nas `validation` de backup_region/replica_region (falham o
# plan/validate ANTES do provider, offline). Este `check` torna a violação explícita e nomeada.
check "soberania_regional" {
  assert {
    condition = (
      contains(local.sovereignty_allowed_regions, local.effective_backup_region) &&
      contains(local.sovereignty_allowed_regions, local.effective_replica_region)
    )
    error_message = "Backup/réplica fora do board de soberania (ADR-011): efectivo backup=${local.effective_backup_region}, réplica=${local.effective_replica_region}."
  }
}

# =========================================================================================
# PLANO DE CONTROLO (decide) — rede default-deny + scaffold ORQ/SCH/ADM/PDP + Credential Broker
# =========================================================================================

# Rede do plano de controlo (default-deny; egress só via allowlist explícita — ADR-004).
module "network_control" {
  source = "./modules/network"

  name             = "${local.name_prefix}-cp"
  subnet           = var.network_subnet_control
  egress_allowlist = var.egress_allowlist_control
  labels           = merge(local.common_labels, { "aos.plane" = "control" })
}

# Componentes de decisão (scaffold): ORQ, SCH, ADM, PDP. Escala por control_plane_replicas.
module "control_plane" {
  source = "./modules/control-plane"

  name         = local.name_prefix
  network_name = module.network_control.network_name
  image        = var.plane_placeholder_image
  replicas     = var.control_plane_replicas
  labels       = merge(local.common_labels, { "aos.plane" = "control" })
}

# Credential Broker/Vault (BRK) — cofre de segredos, tokens JIT (ADR-006). Identidade/decisão
# => plano de controlo.
module "secrets" {
  source = "./modules/secrets"

  name         = local.name_prefix
  network_name = module.network_control.network_name
  image        = var.secrets_image
  dev_mode     = var.secrets_dev_mode
  api_port     = var.secrets_port
  labels       = merge(local.common_labels, { "aos.plane" = "control" })
}

# =========================================================================================
# PLANO DE DADOS (executa e regista) — rede default-deny + scaffold workers/microVM + Event Store
# =========================================================================================

# Rede do plano de dados (default-deny; egress só via allowlist explícita — ADR-004).
module "network_data" {
  source = "./modules/network"

  name             = "${local.name_prefix}-dp"
  subnet           = var.network_subnet_data
  egress_allowlist = var.egress_allowlist_data
  labels           = merge(local.common_labels, { "aos.plane" = "data" })
}

# Workers stateless + pool de microVMs (scaffold). Escala por data_plane_worker_replicas /
# microvm_pool_size. Lógica stateless => AOS-099; aquecimento do pool => AOS-103.
module "data_plane" {
  source = "./modules/data-plane"

  name              = local.name_prefix
  network_name      = module.network_data.network_name
  image             = var.plane_placeholder_image
  worker_replicas   = var.data_plane_worker_replicas
  microvm_pool_size = var.microvm_pool_size
  labels            = merge(local.common_labels, { "aos.plane" = "data" })
}

# Event Store (ES) — NATS JetStream, append-only, transporte push (ADR-007). Plano de dados.
module "eventstore" {
  source = "./modules/eventstore"

  name         = local.name_prefix
  network_name = module.network_data.network_name
  image        = var.eventstore_image
  # Região das RÉPLICAS, não a do ambiente: os nós do Event Store SÃO onde as réplicas do
  # stream ficam, e é essa colocação que a fronteira de soberania governa (ADR-011). Usar
  # `var.region` aqui divergiria de `replica_region` sempre que o board tem mais de uma
  # região — e a divergência só apareceria contra um cluster real.
  # `effective_replica_region` já passou pelas duas camadas de guarda deste ficheiro: a
  # `validation` de replica_region (falha o plan, offline) e o `check "soberania_regional"`
  # acima. As `server_tags` são a TERCEIRA camada, e a única que o SERVIDOR impõe.
  region       = local.effective_replica_region
  cluster_size = var.eventstore_cluster_size
  client_port  = var.eventstore_client_port
  store_dir    = var.eventstore_store_dir
  labels       = merge(local.common_labels, { "aos.plane" = "data" })
}
