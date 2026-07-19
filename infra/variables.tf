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

# --- Topologia de planos: modelo de implantação e soberania (AOS-098 / EPIC-10) ---
variable "deployment_model" {
  description = "Modelo de implantação (tecnica/10 §4): 'self_hosted', 'on_prem' ou 'cloud'. Determina substrato/isolamento/fronteira de soberania."
  type        = string
  default     = "self_hosted"
  validation {
    condition     = contains(["self_hosted", "on_prem", "cloud"], var.deployment_model)
    error_message = "deployment_model tem de ser 'self_hosted', 'on_prem' ou 'cloud'."
  }
}

variable "region" {
  description = "Região de soberania onde o ambiente corre (ADR-011). Fronteira que réplicas/backups NUNCA cruzam."
  type        = string
  validation {
    condition     = trimspace(var.region) != ""
    error_message = "region é obrigatória (ADR-011): sem região não há fronteira de soberania definível (fail-closed)."
  }
  validation {
    condition     = length(var.sovereignty_regions) == 0 || contains(var.sovereignty_regions, var.region)
    error_message = "GUARDRAIL DE SOBERANIA (ADR-011): region tem de pertencer ao board (sovereignty_regions), quando este é declarado."
  }
}

variable "sovereignty_board" {
  description = "Identificador do board de soberania a que a região pertence (ADR-011). Só metadado — sem segredos."
  type        = string
}

variable "sovereignty_regions" {
  description = "Conjunto de regiões DENTRO do board de soberania. Vazio => board de região única (só a própria region é admissível, fail-closed). Réplicas/backups só podem ficar dentro deste conjunto."
  type        = list(string)
  default     = []
}

variable "backup_region" {
  description = "Região dos backups do Event Store. Vazio = mesma que region. NUNCA pode cruzar a fronteira de soberania (ADR-011)."
  type        = string
  default     = ""
  validation {
    condition = (
      var.backup_region == "" ||
      var.backup_region == var.region ||
      contains(var.sovereignty_regions, var.backup_region)
    )
    error_message = "GUARDRAIL DE SOBERANIA (ADR-011): backup_region cruza a fronteira regional. Tem de ser igual a region ou pertencer a sovereignty_regions (fail-closed)."
  }
}

variable "replica_region" {
  description = "Região das réplicas do Event Store/planos. Vazio = mesma que region. NUNCA cruza a fronteira de soberania (ADR-011)."
  type        = string
  default     = ""
  validation {
    condition = (
      var.replica_region == "" ||
      var.replica_region == var.region ||
      contains(var.sovereignty_regions, var.replica_region)
    )
    error_message = "GUARDRAIL DE SOBERANIA (ADR-011): replica_region cruza a fronteira regional. Tem de ser igual a region ou pertencer a sovereignty_regions (fail-closed)."
  }
}

# --- Escala independente por plano (AOS-098 / EPIC-10 §3/§5) ---
variable "control_plane_replicas" {
  description = "Réplicas por componente do plano de controlo (ORQ/SCH/ADM/PDP). Escala independente do plano de dados."
  type        = number
  default     = 1
  validation {
    condition     = var.control_plane_replicas >= 1 && var.control_plane_replicas <= 10
    error_message = "control_plane_replicas tem de estar entre 1 e 10."
  }
}

variable "data_plane_worker_replicas" {
  description = "Réplicas de worker stateless do plano de dados. Escala independente. Lógica stateless em AOS-099."
  type        = number
  default     = 1
  validation {
    condition     = var.data_plane_worker_replicas >= 1 && var.data_plane_worker_replicas <= 50
    error_message = "data_plane_worker_replicas tem de estar entre 1 e 50."
  }
}

variable "microvm_pool_size" {
  description = "Nós do pool de microVMs pré-aquecidas (scaffold; aquecimento/snapshot em AOS-103). 0 = sem pool."
  type        = number
  default     = 0
  validation {
    condition     = var.microvm_pool_size >= 0 && var.microvm_pool_size <= 50
    error_message = "microvm_pool_size tem de estar entre 0 e 50."
  }
}

variable "plane_placeholder_image" {
  description = "Imagem placeholder dos scaffolds de plano (inerte, sem lógica). Pinada por tag imutável."
  type        = string
  default     = "busybox:1.36"
}

# --- Rede por plano: postura default-deny + egress allowlist (ADR-004) ---
variable "network_subnet_control" {
  description = "Sub-rede CIDR do plano de controlo. Não pode sobrepor-se à do plano de dados."
  type        = string
  default     = "172.28.0.0/24"
}

variable "network_subnet_data" {
  description = "Sub-rede CIDR do plano de dados. Não pode sobrepor-se à do plano de controlo."
  type        = string
  default     = "172.28.1.0/24"
}

variable "egress_allowlist_control" {
  description = "Allowlist EXPLÍCITA de egress do plano de controlo (ADR-004). Vazia = deny-all. Rejeita 0.0.0.0/0 e ::/0."
  type        = list(string)
  default     = []
  validation {
    condition     = alltrue([for c in var.egress_allowlist_control : can(cidrhost(c, 0))])
    error_message = "egress_allowlist_control só aceita CIDRs válidos."
  }
  validation {
    condition = (
      !contains(var.egress_allowlist_control, "0.0.0.0/0") &&
      !contains(var.egress_allowlist_control, "::/0") &&
      alltrue([for c in var.egress_allowlist_control : !endswith(trimspace(c), "/0")])
    )
    error_message = "egress_allowlist_control não pode conter rotas permissivas (0.0.0.0/0, ::/0 ou /0) — default-deny (ADR-004)."
  }
  # Anti-contorno: um prefixo demasiado largo (ou uma rota-default partida em /1+/1) cobre todo
  # o espaço de endereços sem nunca ser literalmente /0. Rejeita masklen larga por família:
  # IPv4 exige >= /8, IPv6 exige >= /32 (ADR-004, fail-closed).
  validation {
    condition = alltrue([
      for c in var.egress_allowlist_control : (
        can(cidrhost(c, 0)) && (
          strcontains(c, ":")
          ? tonumber(split("/", trimspace(c))[1]) >= 32
          : tonumber(split("/", trimspace(c))[1]) >= 8
        )
      )
    ])
    error_message = "egress_allowlist_control rejeita prefixos demasiado largos (IPv4 < /8 ou IPv6 < /32) — cobrem o espaço inteiro e equivalem a egress permissivo (ADR-004)."
  }
}

variable "egress_allowlist_data" {
  description = "Allowlist EXPLÍCITA de egress do plano de dados (ADR-004). Vazia = deny-all. Rejeita 0.0.0.0/0 e ::/0."
  type        = list(string)
  default     = []
  validation {
    condition     = alltrue([for c in var.egress_allowlist_data : can(cidrhost(c, 0))])
    error_message = "egress_allowlist_data só aceita CIDRs válidos."
  }
  validation {
    condition = (
      !contains(var.egress_allowlist_data, "0.0.0.0/0") &&
      !contains(var.egress_allowlist_data, "::/0") &&
      alltrue([for c in var.egress_allowlist_data : !endswith(trimspace(c), "/0")])
    )
    error_message = "egress_allowlist_data não pode conter rotas permissivas (0.0.0.0/0, ::/0 ou /0) — default-deny (ADR-004)."
  }
  # Anti-contorno: um prefixo demasiado largo (ou uma rota-default partida em /1+/1) cobre todo
  # o espaço de endereços sem nunca ser literalmente /0. Rejeita masklen larga por família:
  # IPv4 exige >= /8, IPv6 exige >= /32 (ADR-004, fail-closed).
  validation {
    condition = alltrue([
      for c in var.egress_allowlist_data : (
        can(cidrhost(c, 0)) && (
          strcontains(c, ":")
          ? tonumber(split("/", trimspace(c))[1]) >= 32
          : tonumber(split("/", trimspace(c))[1]) >= 8
        )
      )
    ])
    error_message = "egress_allowlist_data rejeita prefixos demasiado largos (IPv4 < /8 ou IPv6 < /32) — cobrem o espaço inteiro e equivalem a egress permissivo (ADR-004)."
  }
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
