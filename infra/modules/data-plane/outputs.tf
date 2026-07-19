output "worker_replicas" {
  description = "Réplicas de worker stateless do plano de dados."
  value       = var.worker_replicas
}

output "microvm_pool_size" {
  description = "Nós do pool de microVMs (scaffold; aquecimento em AOS-103)."
  value       = var.microvm_pool_size
}

output "worker_names" {
  description = "Nomes dos containers placeholder de worker."
  value       = [for c in docker_container.worker : c.name]
}

output "microvm_names" {
  description = "Nomes dos containers placeholder do pool de microVMs."
  value       = [for c in docker_container.microvm_pool : c.name]
}
