variable "name" {
  description = "Prefixo de nomeação (ex.: aos-dev)."
  type        = string
}

variable "subnet" {
  description = "CIDR da sub-rede."
  type        = string
}

variable "internal" {
  description = "true = rede interna (sem egress para a Internet); default-deny (ADR-004)."
  type        = bool
  default     = false
}

variable "labels" {
  description = "Etiquetas comuns."
  type        = map(string)
  default     = {}
}
