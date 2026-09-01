output "environment" {
  description = "Ambiente provisionado."
  value       = var.environment
}

# --- Topologia de planos (AOS-098) ---
output "topology" {
  description = "Resumo da topologia: modelo de implantação, planos e escala independente por plano."
  value = {
    deployment_model = var.deployment_model
    control_plane = {
      network         = module.network_control.network_name
      egress_posture  = module.network_control.egress_posture
      roles           = module.control_plane.roles
      replicas        = module.control_plane.replicas
      component_count = length(module.control_plane.component_names)
    }
    data_plane = {
      network           = module.network_data.network_name
      egress_posture    = module.network_data.egress_posture
      worker_replicas   = module.data_plane.worker_replicas
      microvm_pool_size = module.data_plane.microvm_pool_size
      event_store_nodes = module.eventstore.cluster_size
    }
  }
}

# --- Guardrail de soberania (ADR-011) ---
output "sovereignty" {
  description = "Fronteira de soberania efectiva: board, regiões admissíveis e regiões efectivas de backup/réplica."
  value = {
    board             = var.sovereignty_board
    region            = var.region
    allowed_regions   = local.sovereignty_allowed_regions
    effective_backup  = local.effective_backup_region
    effective_replica = local.effective_replica_region

    # As tags que os nós do Event Store ANUNCIAM. É o que torna a fronteira imposta pelo
    # SERVIDOR e não só declarada por nós: sem elas a `placement` do stream não tem pares
    # elegíveis e o nó com fronteira declarada não arranca (ADR-011, AC5 do AOS-100).
    eventstore_server_tags = module.eventstore.server_tags
    eventstore_region      = module.eventstore.region
  }
}

# --- Endpoints ---
output "eventstore_client_url" {
  description = "URL de cliente do Event Store (NATS) — consumido por substrate/ES."
  value       = module.eventstore.client_url
}

output "eventstore_monitoring_url" {
  description = "URL de monitorização do Event Store (NATS)."
  value       = module.eventstore.monitoring_url
}

output "secrets_address" {
  description = "Endereço da API do Vault — consumido por platform/BRK."
  value       = module.secrets.address
}

output "vault_dev_root_token" {
  description = "Root token descartável do Vault -dev (só dev; vazio em staging). Ler com: tofu output -raw vault_dev_root_token. NÃO usar em produção."
  value       = module.secrets.dev_root_token
  sensitive   = true
}

output "endpoints" {
  description = "Resumo de endpoints do ambiente para arranque rápido."
  value = {
    event_store     = module.eventstore.client_url
    monitoring      = module.eventstore.monitoring_url
    vault           = module.secrets.address
    network_control = module.network_control.network_name
    network_data    = module.network_data.network_name
  }
}
