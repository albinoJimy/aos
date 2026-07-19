# PLANO DE DADOS (executa e regista) — SCAFFOLD de AOS-098.
#
# Materializa TOPOLOGICAMENTE a separação plano-controlo/plano-dados e a escala independente
# (var.worker_replicas e var.microvm_pool_size, parametrizadas por plano). Aloja (tecnica/10 §3):
#   worker  — worker stateless que executa os passos
#   microvm — nó do pool de microVMs pré-aquecidas (isolamento primário, ADR-004)
# O Event Store replicado e o audit WORM (também plano de dados) vivem nos módulos `eventstore`
# e na cadeia de audit já existentes; aqui trata-se só do compute stateless e do pool.
#
# ÂMBITO: AOS-098 entrega apenas a TOPOLOGIA/SCAFFOLDING. Fica FORA deste ticket:
#   - Workers stateless + estado particionado por run  => AOS-099
#   - Replicação interna do Event Store para além do módulo eventstore => AOS-100
#   - Aquecimento/snapshot/restore do pool de microVMs => AOS-103
# Os containers são placeholders inertes (sem portas, sem egress) que provam a separação de
# planos, a etiquetagem de soberania e a escala parametrizada sem antecipar a implementação.

resource "docker_image" "placeholder" {
  name         = var.image
  keep_locally = true
}

# Workers stateless (placeholder). Lógica de particionamento por run => AOS-099.
resource "docker_container" "worker" {
  count = var.worker_replicas

  name    = "${var.name}-dp-worker-${count.index}"
  image   = docker_image.placeholder.image_id
  restart = "unless-stopped"

  command = ["sh", "-c", "while true; do sleep 3600; done"]

  networks_advanced {
    name    = var.network_name
    aliases = ["${var.name}-worker-${count.index}"]
  }

  dynamic "labels" {
    for_each = merge(var.labels, {
      "aos.role"     = "worker"
      "aos.replica"  = tostring(count.index)
      "aos.scaffold" = "AOS-099"
    })
    content {
      label = labels.key
      value = labels.value
    }
  }
}

# Pool de microVMs pré-aquecidas (placeholder). Aquecimento/snapshot => AOS-103.
resource "docker_container" "microvm_pool" {
  count = var.microvm_pool_size

  name    = "${var.name}-dp-microvm-${count.index}"
  image   = docker_image.placeholder.image_id
  restart = "unless-stopped"

  command = ["sh", "-c", "while true; do sleep 3600; done"]

  networks_advanced {
    name    = var.network_name
    aliases = ["${var.name}-microvm-${count.index}"]
  }

  dynamic "labels" {
    for_each = merge(var.labels, {
      "aos.role"     = "microvm"
      "aos.replica"  = tostring(count.index)
      "aos.scaffold" = "AOS-103"
    })
    content {
      label = labels.key
      value = labels.value
    }
  }
}
