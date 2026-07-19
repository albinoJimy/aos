variable "name" {
  description = "Prefixo de nomeação (ex.: aos-dev)."
  type        = string
}

variable "network_name" {
  description = "Rede Docker do PLANO DE CONTROLO onde os componentes de decisão correm."
  type        = string
}

variable "image" {
  description = "Imagem placeholder do scaffold, pinada por tag imutável. Inerte (sem lógica de negócio)."
  type        = string
}

variable "replicas" {
  description = <<-EOT
    Réplicas por componente do plano de controlo. Materializa a ESCALA INDEPENDENTE do plano
    de controlo face ao plano de dados (contagem parametrizada por plano, EPIC-10 §3/§5).
  EOT
  type        = number
  default     = 1
  validation {
    condition     = var.replicas >= 1 && var.replicas <= 10
    error_message = "replicas do plano de controlo tem de estar entre 1 e 10."
  }
}

variable "labels" {
  description = "Etiquetas comuns (inclui aos.plane=control)."
  type        = map(string)
  default     = {}
}
