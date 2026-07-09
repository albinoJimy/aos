output "network_name" {
  description = "Nome da rede Docker criada."
  value       = docker_network.this.name
}

output "network_id" {
  description = "ID da rede Docker criada."
  value       = docker_network.this.id
}

output "internal" {
  description = "Se a rede é interna (sem egress)."
  value       = docker_network.this.internal
}
