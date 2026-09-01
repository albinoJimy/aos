# Teste: `server_tags` DE REGIÃO NOS NÓS DO EVENT STORE (ADR-011, AC5 do AOS-100).
#
# O defeito que este teste guarda: o módulo `eventstore` provisionava nós NATS SEM tag
# nenhuma. A cadeia, medida contra um cluster real:
#   1. deploy/server/docker-compose.prod.yml declara AOS_BOARD_REGIONS como OBRIGATÓRIA;
#   2. a guarda (1c) de packages/cmd/aos/bootstrap.go EXIGE AOS_EVENTSTORE_NATS_REGION;
#   3. packages/substrate/eventstore/jetstream/soberania.go cria o stream com `placement`
#      restrita a `region:<regiao>` e LÊ a colocação de volta;
#   4. sem pares que anunciem a tag, o servidor recusa («no suitable peers», 10005) e o nó
#      NÃO ARRANCA.
#
# Este teste falha se a correcção for revertida ou mutada: sem o prefixo `region:`, sem o
# `-c` que injecta o fragmento, ou com a região presa a var.region em vez da região das
# réplicas.
#
# OFFLINE: mock_provider substitui o daemon Docker => corre sem daemon.
#   tofu init -backend=false && tofu test

mock_provider "docker" {}
mock_provider "random" {}

variables {
  environment            = "dev"
  region                 = "eu-west-1"
  sovereignty_board      = "eu"
  sovereignty_regions    = ["eu-west-1", "eu-central-1"]
  network_subnet_control = "172.28.0.0/24"
  network_subnet_data    = "172.28.1.0/24"
}

run "cada_no_anuncia_a_tag_de_regiao" {
  command = plan

  variables {
    eventstore_cluster_size = 3
  }

  # A convenção é a constante TagDeRegiao ("region:") de soberania.go. Um prefixo diferente
  # é uma tag que a `placement` nunca pede.
  assert {
    condition     = module.eventstore.server_tags == ["region:eu-west-1"]
    error_message = "Os nós do Event Store têm de anunciar server_tags = [\"region:<regiao>\"] (ADR-011): sem isso a placement do stream não tem pares elegíveis e o nó não arranca."
  }

  # Ligação output→recurso: o nats-server não tem flag para `server_tags`, logo a tag só
  # chega ao processo se o fragmento de configuração for injectado com `-c`. Se o `-c` cair
  # do comando, o ficheiro fica no container e o servidor NUNCA o lê — falha silenciosa.
  assert {
    condition = alltrue([
      for c in module.eventstore.node_commands : contains(c, "-c") && contains(c, "/etc/nats/aos-soberania.conf")
    ])
    error_message = "TODOS os nós têm de arrancar com `-c /etc/nats/aos-soberania.conf`: é a única via pela qual as server_tags chegam ao nats-server."
  }

  assert {
    condition     = length(module.eventstore.node_commands) == 3
    error_message = "A tag tem de ir a todos os nós do cluster, não só ao nó 0."
  }

  # O guardrail de soberania e as tags têm de contar a MESMA história no output.
  assert {
    condition     = output.sovereignty.eventstore_server_tags == ["region:${output.sovereignty.effective_replica}"]
    error_message = "As server_tags do Event Store têm de derivar da região EFECTIVA das réplicas, senão o guardrail e o cluster divergem."
  }
}

run "a_tag_segue_a_regiao_das_replicas_nao_a_do_ambiente" {
  command = plan

  # replica_region dentro do board mas DIFERENTE de var.region: os nós do ES são onde as
  # réplicas ficam, logo é a região das réplicas que tem de ser anunciada. Prender a tag a
  # var.region passaria este plan com a região errada — e só um cluster real o revelaria.
  variables {
    replica_region = "eu-central-1"
  }

  assert {
    condition     = module.eventstore.server_tags == ["region:eu-central-1"]
    error_message = "Com replica_region = eu-central-1, os nós têm de anunciar region:eu-central-1 (e não a região do ambiente)."
  }

  assert {
    condition     = module.eventstore.region == "eu-central-1"
    error_message = "A região efectiva do módulo tem de ser a das réplicas, normalizada como em soberania.go."
  }
}

run "single_node_de_dev_tambem_e_taggado" {
  command = plan

  # Um cluster_size = 1 sem tag é o caso mais fácil de esquecer e o mais fácil de deixar
  # passar: em dev ninguém liga um nó com fronteira declarada... até ligar.
  variables {
    eventstore_cluster_size = 1
  }

  assert {
    condition     = module.eventstore.server_tags == ["region:eu-west-1"]
    error_message = "O single-node de dev também tem de anunciar a tag: um par sem tag não é elegível para a placement."
  }

  assert {
    condition = alltrue([
      for c in module.eventstore.node_commands : contains(c, "/etc/nats/aos-soberania.conf")
    ])
    error_message = "O single-node também tem de receber o fragmento de configuração das tags."
  }
}
