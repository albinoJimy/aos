output "roles" {
  description = "Componentes de decisão do plano de controlo (ORQ/SCH/ADM/PDP)."
  value       = local.roles
}

output "replicas" {
  description = "Réplicas por componente do plano de controlo."
  value       = var.replicas
}

output "component_names" {
  description = "Nomes dos containers placeholder do plano de controlo."
  value       = [for c in docker_container.component : c.name]
}
