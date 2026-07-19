# Teste: DEFAULTS VÁLIDOS => plan assertivo (planos separados, escala independente, deny-all).
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

run "planos_topologicamente_separados" {
  command = plan

  assert {
    condition     = module.network_control.network_name != module.network_data.network_name
    error_message = "Os planos de controlo e de dados têm de ter redes distintas (separação topológica)."
  }

  assert {
    condition     = output.topology.deployment_model == "self_hosted"
    error_message = "deployment_model por omissão deve ser self_hosted."
  }
}

run "egress_nasce_deny_all_por_omissao" {
  command = plan

  assert {
    condition     = output.topology.control_plane.egress_posture == "deny-all"
    error_message = "Plano de controlo tem de nascer deny-all quando a allowlist é vazia (ADR-004)."
  }

  assert {
    condition     = output.topology.data_plane.egress_posture == "deny-all"
    error_message = "Plano de dados tem de nascer deny-all quando a allowlist é vazia (ADR-004)."
  }
}

run "escala_independente_por_plano" {
  command = plan

  variables {
    control_plane_replicas     = 2
    data_plane_worker_replicas = 4
  }

  assert {
    condition     = output.topology.control_plane.replicas == 2
    error_message = "O plano de controlo tem de escalar de forma independente (control_plane_replicas)."
  }

  assert {
    condition     = output.topology.data_plane.worker_replicas == 4
    error_message = "O plano de dados tem de escalar de forma independente (data_plane_worker_replicas)."
  }
}

run "soberania_efectiva_dentro_do_board" {
  command = plan

  assert {
    condition     = output.sovereignty.effective_backup == "eu-west-1"
    error_message = "Sem backup_region explícito, o backup fica na própria região (fail-closed)."
  }
}
