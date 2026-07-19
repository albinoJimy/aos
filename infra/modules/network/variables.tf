variable "name" {
  description = "Prefixo de nomeação (ex.: aos-dev)."
  type        = string
}

variable "subnet" {
  description = "CIDR da sub-rede."
  type        = string
}

variable "egress_allowlist" {
  description = <<-EOT
    Allowlist EXPLÍCITA de destinos CIDR autorizados a egress (ADR-004 / Princípio 7).
    Postura DEFAULT-DENY: lista VAZIA = deny-all (rede sem gateway, sem egress). Uma lista
    não-vazia mantém a rede fechada por omissão e materializa cada CIDR como etiqueta
    `aos.egress.allow.<i>` para o egress-proxy / Model Gateway aplicar a jusante.
    NUNCA aceita rotas permissivas: 0.0.0.0/0 e ::/0 (ou qualquer prefixo /0) são rejeitados.
  EOT
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for c in var.egress_allowlist : can(cidrhost(c, 0))])
    error_message = "egress_allowlist só aceita CIDRs válidos (ex.: 10.100.0.0/24)."
  }

  validation {
    condition = (
      !contains(var.egress_allowlist, "0.0.0.0/0") &&
      !contains(var.egress_allowlist, "::/0")
    )
    error_message = "egress_allowlist não pode conter rotas permissivas 0.0.0.0/0 nem ::/0 (default-deny, ADR-004)."
  }

  validation {
    condition     = alltrue([for c in var.egress_allowlist : !endswith(trimspace(c), "/0")])
    error_message = "egress_allowlist rejeita prefixos /0 (permissivos por omissão). Usa CIDRs específicos (ADR-004)."
  }

  # Anti-contorno: um prefixo demasiado largo (ou uma rota-default partida em 0.0.0.0/1 +
  # 128.0.0.0/1) cobre TODO o espaço de endereços sem nunca ser literalmente /0. Rejeita
  # masklen larga por família — IPv4 exige >= /8, IPv6 exige >= /32 (default-deny, ADR-004).
  validation {
    condition = alltrue([
      for c in var.egress_allowlist : (
        can(cidrhost(c, 0)) && (
          strcontains(c, ":")
          ? tonumber(split("/", trimspace(c))[1]) >= 32
          : tonumber(split("/", trimspace(c))[1]) >= 8
        )
      )
    ])
    error_message = "egress_allowlist rejeita prefixos demasiado largos (IPv4 < /8 ou IPv6 < /32): cobrem o espaço inteiro e equivalem a egress permissivo (ADR-004)."
  }
}

variable "labels" {
  description = "Etiquetas comuns."
  type        = map(string)
  default     = {}
}
