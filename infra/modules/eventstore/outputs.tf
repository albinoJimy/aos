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

# --- Soberania regional (ADR-011) ---
output "server_tags" {
  description = "Tags de elegibilidade anunciadas por CADA nó. Têm de casar com a `placement` que o adaptador JetStream exige (`region:<regiao>`), senão o stream é recusado e o nó não arranca."
  value       = local.server_tags
}

output "region" {
  description = "Região de soberania efectiva dos nós, já normalizada como em soberania.go (minúsculas, sem espaços)."
  value       = local.regiao_normalizada
}

output "node_commands" {
  description = "Comando efectivo de cada nó. Inclui `-c <fragmento de tags>` — é por aí que as `server_tags` entram (o nats-server não tem flag para elas)."
  value       = docker_container.nats[*].command
}

output "node_names" {
  description = "Nomes dos containers dos nós."
  value       = docker_container.nats[*].name
}
