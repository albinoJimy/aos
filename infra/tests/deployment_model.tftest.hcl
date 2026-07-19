# Teste: MODELO DE IMPLANTAÇÃO (AC5) — só self_hosted|on_prem|cloud.
# Um modelo fora do conjunto FALHA o plan na validação de input (OFFLINE, fail-closed).
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

run "modelo_invalido_falha" {
  command = plan

  variables {
    deployment_model = "kubernetes"
  }

  expect_failures = [var.deployment_model]
}

run "modelo_cloud_valido_passa" {
  command = plan

  variables {
    deployment_model = "cloud"
  }

  assert {
    condition     = output.topology.deployment_model == "cloud"
    error_message = "O modelo 'cloud' é válido e tem de passar."
  }
}

run "modelo_on_prem_valido_passa" {
  command = plan

  variables {
    deployment_model = "on_prem"
  }

  assert {
    condition     = output.topology.deployment_model == "on_prem"
    error_message = "O modelo 'on_prem' é válido e tem de passar."
  }
}
