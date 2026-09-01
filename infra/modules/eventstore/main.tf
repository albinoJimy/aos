# Event Store (ES) — NATS com JetStream: log append-only, transporte push (ADR-007).
# dev: single-node. staging: cluster replicado (cluster_size >= 3) via rotas full-mesh.
# Persistência de JetStream em volume Docker por nó => `apply` idempotente e
# `destroy` limpo (volumes removidos com os containers).

locals {
  # Rotas full-mesh entre nós (DNS do Docker resolve o nome do container na rede).
  routes = join(",", [
    for i in range(var.cluster_size) : "nats://${var.name}-es-${i}:6222"
  ])

  # --- Soberania regional: `server_tags` por nó (ADR-011, AC5 do AOS-100) ----------------
  #
  # DEFEITO QUE ISTO FECHA: o módulo provisionava nós NATS SEM tag nenhuma, e um nó do AOS
  # com fronteira declarada não arrancava contra este cluster. A cadeia, medida contra um
  # cluster real durante o AOS-100:
  #   1. deploy/server/docker-compose.prod.yml declara AOS_BOARD_REGIONS como OBRIGATÓRIA;
  #   2. com AOS_BOARD_REGIONS + AOS_EVENTSTORE_NATS, a guarda (1c) de
  #      packages/cmd/aos/bootstrap.go EXIGE AOS_EVENTSTORE_NATS_REGION;
  #   3. packages/substrate/eventstore/jetstream/soberania.go cria o stream com `placement`
  #      restrita a `region:<regiao>` e LÊ a colocação de volta da config armazenada;
  #   4. sem pares que anunciem essa tag, o servidor RECUSA criar o stream
  #      («no suitable peers», err_code=10005) — e o nó NÃO ARRANCA.
  # O fail-closed é do servidor, e é isso que o torna confiável. Estas tags são o outro
  # lado desse contrato: sem elas a fronteira não é impossível de violar, é impossível de
  # satisfazer.
  #
  # `region:` é a constante TagDeRegiao de soberania.go. Está duplicada aqui porque o
  # Terraform não lê Go — se a constante mudar lá, TEM de mudar aqui, ou a colocação passa
  # a pedir uma tag que nenhum nó anuncia.
  tag_de_regiao_prefixo = "region:"

  # A MESMA normalização de normalizarRegiao() em soberania.go (minúsculas, sem espaços em
  # redor). Duas normalizações diferentes fariam "EU-West-1" e "eu-west-1" ser a mesma
  # região de um lado e regiões distintas do outro — e o desencontro só apareceria contra
  # um cluster real, na criação do stream.
  regiao_normalizada = lower(trimspace(var.region))
  server_tags        = ["${local.tag_de_regiao_prefixo}${local.regiao_normalizada}"]

  # O nats-server NÃO tem flag de linha de comando para `server_tags` — só configuração.
  # Por isso vai num fragmento próprio, injectado por nó. Ficheiro DISTINTO do
  # nats-server.conf que a imagem traz, para não lhe tocar; as flags continuam a mandar em
  # tudo o resto (o servidor lê o `-c` e depois aplica as flags por cima).
  ficheiro_de_tags = "/etc/nats/aos-soberania.conf"
  conteudo_de_tags = <<-EOT
    # Gerado pelo módulo Terraform `eventstore` — NÃO editar no container.
    # Tags de elegibilidade para a `placement` dos streams (ADR-011). Sem isto, um nó do
    # AOS com fronteira regional declarada não arranca contra este cluster.
    server_tags: [${join(", ", [for t in local.server_tags : jsonencode(t)])}]
  EOT

  base_command = [
    "-c", local.ficheiro_de_tags,
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

  # Tags de soberania deste nó (ADR-011). Vai em TODOS os nós, incluindo o single-node de
  # dev: um cluster em que só alguns pares anunciam a região tem menos pares elegíveis do
  # que réplicas pedidas, e a criação do stream é recusada na mesma. Sem segredos — só a
  # região, que já é etiqueta pública nos labels.
  upload {
    file    = local.ficheiro_de_tags
    content = local.conteudo_de_tags
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
