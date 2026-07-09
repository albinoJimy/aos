# Rede do ambiente. Fronteira de isolamento entre componentes AOS.
# `internal = true` remove o gateway default => sem egress para a Internet
# (materializa o egress default-deny do ADR-004 / Princípio 7 ao nível da rede).
resource "docker_network" "this" {
  name       = "${var.name}-net"
  driver     = "bridge"
  internal   = var.internal
  attachable = true

  ipam_config {
    subnet = var.subnet
  }

  dynamic "labels" {
    for_each = var.labels
    content {
      label = labels.key
      value = labels.value
    }
  }
}
