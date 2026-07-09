variable "name" {
  description = "Prefixo de nomeação (ex.: aos-dev)."
  type        = string
}

variable "network_name" {
  description = "Rede Docker onde o Vault corre."
  type        = string
}

variable "image" {
  description = "Imagem do Vault, pinada por tag imutável."
  type        = string
}

variable "dev_mode" {
  description = "true = Vault -dev (in-memory, unseal automático, SÓ dev). false = server persistente (staging), arranca selado."
  type        = bool
  default     = true
}

variable "api_port" {
  description = "Porta da API do Vault exposta no host."
  type        = number
  default     = 8200
}

variable "labels" {
  description = "Etiquetas comuns."
  type        = map(string)
  default     = {}
}
