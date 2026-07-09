variable "name" {
  description = "Prefixo de nomeação (ex.: aos-dev)."
  type        = string
}

variable "network_name" {
  description = "Rede Docker onde os nós do Event Store correm."
  type        = string
}

variable "image" {
  description = "Imagem NATS JetStream, pinada por tag imutável."
  type        = string
}

variable "cluster_size" {
  description = "Nº de nós. 1 = single-node (dev); >=3 = cluster replicado (staging, ADR-007)."
  type        = number
  default     = 1
}

variable "client_port" {
  description = "Porta de cliente do nó 0 no host. Nó i usa client_port + i."
  type        = number
  default     = 4222
}

variable "monitoring_port" {
  description = "Porta de monitorização HTTP do nó 0 no host. Nó i usa monitoring_port + i."
  type        = number
  default     = 8222
}

variable "store_dir" {
  description = "Diretório de JetStream dentro do container (montado em volume)."
  type        = string
  default     = "/data/jetstream"
}

variable "labels" {
  description = "Etiquetas comuns."
  type        = map(string)
  default     = {}
}
