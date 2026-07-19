# Teste: GUARDRAIL DE SOBERANIA (ADR-011, AC5) — fail-closed.
# backup/réplica que cruzam a fronteira regional FALHAM o plan na validação de input (OFFLINE),
# antes de qualquer ligação ao provider.
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

run "backup_cruza_fronteira_falha" {
  command = plan

  variables {
    backup_region = "us-east-1"
  }

  expect_failures = [var.backup_region]
}

run "replica_cruza_fronteira_falha" {
  command = plan

  variables {
    replica_region = "us-east-1"
  }

  expect_failures = [var.replica_region]
}

run "region_fora_do_board_falha" {
  command = plan

  variables {
    region = "us-east-1"
  }

  expect_failures = [var.region]
}

run "backup_dentro_do_board_passa" {
  command = plan

  variables {
    backup_region = "eu-central-1"
  }

  assert {
    condition     = output.sovereignty.effective_backup == "eu-central-1"
    error_message = "Backup dentro do board (eu-central-1) tem de ser admissível."
  }
}
