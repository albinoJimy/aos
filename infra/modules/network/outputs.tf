output "network_name" {
  description = "Nome da rede Docker criada."
  value       = docker_network.this.name
}

output "network_id" {
  description = "ID da rede Docker criada."
  value       = docker_network.this.id
}

output "internal" {
  description = "Se a rede é interna (sem egress). true quando a allowlist está vazia (deny-all)."
  value       = docker_network.this.internal
}

output "egress_posture" {
  description = "Postura de egress: 'deny-all' (allowlist vazia) ou 'allowlist' (CIDRs explícitos)."
  value       = local.deny_all ? "deny-all" : "allowlist"
}

output "egress_allowlist" {
  description = "Destinos CIDR explicitamente autorizados a egress (ADR-004)."
  value       = var.egress_allowlist
}
