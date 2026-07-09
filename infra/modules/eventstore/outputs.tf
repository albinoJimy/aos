output "client_url" {
  description = "URL de cliente NATS do nó 0 (host)."
  value       = "nats://localhost:${var.client_port}"
}

output "monitoring_url" {
  description = "URL de monitorização HTTP do nó 0 (host)."
  value       = "http://localhost:${var.monitoring_port}"
}

output "cluster_size" {
  description = "Nº de nós do Event Store provisionados."
  value       = var.cluster_size
}

output "internal_client_urls" {
  description = "URLs de cliente internos (rede Docker) de todos os nós."
  value       = [for i in range(var.cluster_size) : "nats://${var.name}-es-${i}:4222"]
}

output "node_names" {
  description = "Nomes dos containers dos nós."
  value       = docker_container.nats[*].name
}
