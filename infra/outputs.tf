output "environment" {
  description = "Ambiente provisionado."
  value       = var.environment
}

output "network_name" {
  description = "Nome da rede Docker do ambiente."
  value       = module.network.network_name
}

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
    event_store = module.eventstore.client_url
    monitoring  = module.eventstore.monitoring_url
    vault       = module.secrets.address
    network     = module.network.network_name
  }
}
