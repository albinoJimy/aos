# Event Store (ES) — NATS com JetStream: log append-only, transporte push (ADR-007).
# dev: single-node. staging: cluster replicado (cluster_size >= 3) via rotas full-mesh.
# Persistência de JetStream em volume Docker por nó => `apply` idempotente e
# `destroy` limpo (volumes removidos com os containers).

locals {
  # Rotas full-mesh entre nós (DNS do Docker resolve o nome do container na rede).
  routes = join(",", [
    for i in range(var.cluster_size) : "nats://${var.name}-es-${i}:6222"
  ])

  base_command = [
    "-js",
    "-sd", var.store_dir,
    "-p", "4222",
    "-m", "8222",
  ]

  cluster_command = var.cluster_size > 1 ? [
    "--cluster_name", "${var.name}-es",
    "--cluster", "nats://0.0.0.0:6222",
    "--routes", local.routes,
  ] : []
}

resource "docker_image" "nats" {
  name         = var.image
  keep_locally = true
}

resource "docker_volume" "jetstream" {
  count = var.cluster_size
  name  = "${var.name}-es-${count.index}-data"

  dynamic "labels" {
    for_each = var.labels
    content {
      label = labels.key
      value = labels.value
    }
  }
}

resource "docker_container" "nats" {
  count = var.cluster_size

  name    = "${var.name}-es-${count.index}"
  image   = docker_image.nats.image_id
  restart = "unless-stopped"

  command = concat(
    local.base_command,
    ["-n", "${var.name}-es-${count.index}"],
    local.cluster_command,
  )

  networks_advanced {
    name = var.network_name
    aliases = concat(
      ["${var.name}-es-${count.index}"],
      count.index == 0 ? ["${var.name}-es"] : [],
    )
  }

  # Só o nó 0 é exposto no host para arranque simples; restantes ficam na rede interna.
  dynamic "ports" {
    for_each = count.index == 0 ? [1] : []
    content {
      internal = 4222
      external = var.client_port
    }
  }

  dynamic "ports" {
    for_each = count.index == 0 ? [1] : []
    content {
      internal = 8222
      external = var.monitoring_port
    }
  }

  volumes {
    volume_name    = docker_volume.jetstream[count.index].name
    container_path = var.store_dir
  }

  healthcheck {
    test     = ["CMD", "wget", "--spider", "-q", "http://localhost:8222/healthz"]
    interval = "10s"
    timeout  = "3s"
    retries  = 5
  }

  dynamic "labels" {
    for_each = var.labels
    content {
      label = labels.key
      value = labels.value
    }
  }
}
