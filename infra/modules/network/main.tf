# Rede do ambiente. Fronteira de isolamento entre componentes AOS.
# POSTURA DEFAULT-DENY (ADR-004 / Princípio 7): substitui o antigo binário
# `internal=true/false` por uma allowlist de egress EXPLÍCITA.
#   - allowlist VAZIA  => `internal = true`  => rede sem gateway => deny-all (sem egress).
#   - allowlist NÃO-vazia => rede continua fechada por omissão; os CIDRs autorizados ficam
#     registados como etiquetas para o egress-proxy / Model Gateway aplicar a jusante.
# NUNCA há egress permissivo por omissão: omitir configuração = NEGAR (fail-closed).
locals {
  # Deny-all quando não há um único destino explicitamente autorizado.
  deny_all = length(var.egress_allowlist) == 0

  # Cada destino autorizado vira uma etiqueta consumível pela camada de enforcement.
  egress_labels = {
    for idx, cidr in var.egress_allowlist : "aos.egress.allow.${idx}" => cidr
  }

  all_labels = merge(
    var.labels,
    { "aos.egress.posture" = local.deny_all ? "deny-all" : "allowlist" },
    local.egress_labels,
  )
}

resource "docker_network" "this" {
  name       = "${var.name}-net"
  driver     = "bridge"
  internal   = local.deny_all
  attachable = true

  ipam_config {
    subnet = var.subnet
  }

  dynamic "labels" {
    for_each = local.all_labels
    content {
      label = labels.key
      value = labels.value
    }
  }
}
