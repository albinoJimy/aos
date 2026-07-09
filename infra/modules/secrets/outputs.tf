output "address" {
  description = "Endereço da API do Vault (host) — consumido por platform/BRK."
  value       = "http://localhost:${var.api_port}"
}

output "dev_mode" {
  description = "Se o Vault está em modo -dev (in-memory, unseal automático)."
  value       = var.dev_mode
}

output "dev_root_token" {
  description = "Root token descartável do modo -dev. Vazio em staging. NÃO usar em produção."
  value       = join("", random_password.dev_root_token[*].result)
  sensitive   = true
}
