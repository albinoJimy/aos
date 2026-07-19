# PLANO DE CONTROLO (decide) — SCAFFOLD de AOS-098.
#
# Materializa TOPOLOGICAMENTE a separação plano-controlo/plano-dados e a escala independente
# (var.replicas por componente). Aloja os quatro componentes de decisão (tecnica/10 §3):
#   ORQ — Orquestrador (grafo de tarefas acíclico)
#   SCH — Escalonador (leases, fencing, prioridade)
#   ADM — Admission control global (token-bucket distribuído)
#   PDP — Policy Decision Point (Rego/Cedar versionado)
#
# ÂMBITO: AOS-098 entrega apenas a TOPOLOGIA/SCAFFOLDING. A LÓGICA interna de cada componente
# NÃO é deste ticket — os containers são placeholders inertes (sem portas, sem egress). Detalhe:
# ORQ/SCH/ADM em specs/EPIC-03; PDP em ADR-011. Os placeholders provam a separação de planos,
# a etiquetagem de soberania e a escala parametrizada sem antecipar a implementação.

locals {
  # Componentes de decisão do plano de controlo.
  roles = ["orq", "sch", "adm", "pdp"]

  # Uma instância por (componente, índice-de-réplica) => escala independente do plano.
  instances = merge([
    for role in local.roles : {
      for i in range(var.replicas) : "${role}-${i}" => {
        role    = role
        replica = i
      }
    }
  ]...)
}

resource "docker_image" "placeholder" {
  name         = var.image
  keep_locally = true
}

resource "docker_container" "component" {
  for_each = local.instances

  name    = "${var.name}-cp-${each.key}"
  image   = docker_image.placeholder.image_id
  restart = "unless-stopped"

  # Placeholder inerte: mantém-se vivo sem produzir efeitos. Substituído pela lógica real
  # do componente noutro ticket. Sem portas expostas e sem egress (default-deny da rede).
  command = ["sh", "-c", "while true; do sleep 3600; done"]

  networks_advanced {
    name    = var.network_name
    aliases = ["${var.name}-${each.value.role}-${each.value.replica}"]
  }

  dynamic "labels" {
    for_each = merge(var.labels, {
      "aos.role"     = each.value.role
      "aos.replica"  = tostring(each.value.replica)
      "aos.scaffold" = "AOS-098"
    })
    content {
      label = labels.key
      value = labels.value
    }
  }
}
