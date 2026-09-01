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

variable "region" {
  description = "Região de soberania dos nós (ADR-011). Anunciada em `server_tags` como `region:<regiao>` — é o que torna satisfazível a `placement` que o adaptador JetStream exige. Região NUA (ex.: eu-west-1), sem o prefixo."
  type        = string

  # Sem região não há tag; sem tag não há par elegível; sem par elegível o stream é
  # recusado e o nó não arranca. Uma região ausente é uma região DESCONHECIDA, e uma região
  # desconhecida nega — a mesma leitura de validarFronteira() em soberania.go.
  validation {
    condition     = trimspace(var.region) != ""
    error_message = "region é obrigatória (ADR-011): um cluster sem `server_tags` de região recusa a colocação do stream e o nó com fronteira declarada NÃO ARRANCA (fail-closed)."
  }

  # Anti-duplicação do prefixo: o módulo é que acrescenta `region:` (a constante TagDeRegiao
  # de soberania.go). Passar já a tag produziria `region:region:eu-west-1`, que nenhum par
  # anuncia — e a falha só apareceria na criação do stream, contra um cluster real.
  validation {
    condition     = !startswith(lower(trimspace(var.region)), "region:")
    error_message = "region é a região nua (ex.: eu-west-1), NÃO a tag: o prefixo `region:` é acrescentado pelo módulo. Prefixá-la aqui geraria `region:region:...`, que nenhum par anuncia."
  }

  # O JetStream só compara STRINGS de tags. Espaços, vírgulas ou aspas partiriam a tag do
  # lado do servidor e a comparação falharia em silêncio na configuração, ruidosamente na
  # criação do stream.
  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?$", lower(trimspace(var.region))))
    error_message = "region só aceita [a-z0-9-] começando e acabando em alfanumérico (ex.: eu-west-1): a tag `region:<regiao>` é comparada como string literal pelo servidor."
  }
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
