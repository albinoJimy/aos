variable "environment" {
  description = "Ambiente-alvo. Determina o var-file usado (env/<environment>.tfvars)."
  type        = string
  validation {
    condition     = contains(["dev", "staging"], var.environment)
    error_message = "environment tem de ser 'dev' ou 'staging'."
  }
}

variable "project" {
  description = "Prefixo de nomeação e etiquetagem de todos os recursos."
  type        = string
  default     = "aos"
}

variable "docker_host" {
  description = "Endpoint do Docker daemon. Vazio = auto-detecta (DOCKER_HOST ou socket local)."
  type        = string
  default     = ""
}

# --- Rede (módulo network) ---
variable "network_internal" {
  description = "Se true, a rede é interna (sem egress para a Internet) — default-deny (ADR-004/Princípio 7)."
  type        = bool
  default     = false
}

variable "network_subnet" {
  description = "Sub-rede CIDR da rede do ambiente."
  type        = string
  default     = "172.28.0.0/16"
}

# --- Event Store (módulo eventstore) ---
variable "eventstore_image" {
  description = "Imagem do Event Store (NATS JetStream). Pinada por tag imutável."
  type        = string
  default     = "nats:2.10-alpine"
}

variable "eventstore_cluster_size" {
  description = "Nº de nós do Event Store. dev=1 (single-node); staging>=3 (replicado, ADR-007)."
  type        = number
  default     = 1
  validation {
    condition     = var.eventstore_cluster_size >= 1 && var.eventstore_cluster_size <= 5
    error_message = "eventstore_cluster_size tem de estar entre 1 e 5."
  }
}

variable "eventstore_client_port" {
  description = "Porta de cliente do nó 0 exposta no host (restantes nós usam base+índice)."
  type        = number
  default     = 4222
}

variable "eventstore_store_dir" {
  description = "Diretório de JetStream dentro do container (persistido em volume)."
  type        = string
  default     = "/data/jetstream"
}

# --- Segredos (módulo secrets) ---
variable "secrets_image" {
  description = "Imagem do Vault. Pinada por tag imutável."
  type        = string
  default     = "hashicorp/vault:1.16"
}

variable "secrets_dev_mode" {
  description = "Vault em -dev (in-memory, unseal automático). true SÓ em dev; staging usa Vault externo/persistente."
  type        = bool
  default     = true
}

variable "secrets_port" {
  description = "Porta da API do Vault exposta no host."
  type        = number
  default     = 8200
}

variable "extra_labels" {
  description = "Etiquetas adicionais aplicadas a todos os recursos."
  type        = map(string)
  default     = {}
}
