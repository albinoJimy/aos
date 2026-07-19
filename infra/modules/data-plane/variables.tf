variable "name" {
  description = "Prefixo de nomeação (ex.: aos-dev)."
  type        = string
}

variable "network_name" {
  description = "Rede Docker do PLANO DE DADOS onde workers e pool de microVMs correm."
  type        = string
}

variable "image" {
  description = "Imagem placeholder do scaffold, pinada por tag imutável. Inerte (sem lógica de negócio)."
  type        = string
}

variable "worker_replicas" {
  description = <<-EOT
    Réplicas de worker stateless do plano de dados. Materializa a ESCALA INDEPENDENTE do plano
    de dados (contagem parametrizada por plano). A lógica stateless + estado particionado por
    run é de AOS-099 — aqui apenas um placeholder por worker.
  EOT
  type        = number
  default     = 1
  validation {
    condition     = var.worker_replicas >= 1 && var.worker_replicas <= 50
    error_message = "worker_replicas tem de estar entre 1 e 50."
  }
}

variable "microvm_pool_size" {
  description = <<-EOT
    Tamanho do pool de microVMs pré-aquecidas. SCAFFOLD: o aquecimento/snapshot/restore e o
    dimensionamento por headroom são de AOS-103 — aqui apenas placeholders que representam
    nós do pool. 0 = sem pool (default fail-closed: nada é provisionado sem indicação explícita).
  EOT
  type        = number
  default     = 0
  validation {
    condition     = var.microvm_pool_size >= 0 && var.microvm_pool_size <= 50
    error_message = "microvm_pool_size tem de estar entre 0 e 50."
  }
}

variable "labels" {
  description = "Etiquetas comuns (inclui aos.plane=data)."
  type        = map(string)
  default     = {}
}
