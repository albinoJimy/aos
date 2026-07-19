# Teste: EGRESS DEFAULT-DENY + ALLOWLIST (ADR-004, AC3) — fail-closed.
# Entradas permissivas (0.0.0.0/0, ::/0) FALHAM o plan na validação de input (OFFLINE).
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

run "egress_data_permissivo_ipv4_falha" {
  command = plan

  variables {
    egress_allowlist_data = ["0.0.0.0/0"]
  }

  expect_failures = [var.egress_allowlist_data]
}

run "egress_control_permissivo_ipv6_falha" {
  command = plan

  variables {
    egress_allowlist_control = ["::/0"]
  }

  expect_failures = [var.egress_allowlist_control]
}

# Anti-contorno: rota-default partida em duas metades /1 cobre todo o IPv4 sem ser literal /0.
# Tem de FALHAR na validação de masklen larga (ADR-004, fail-closed).
run "egress_data_rota_default_partida_falha" {
  command = plan

  variables {
    egress_allowlist_data = ["0.0.0.0/1", "128.0.0.0/1"]
  }

  expect_failures = [var.egress_allowlist_data]
}

# Anti-contorno IPv6: prefixo demasiado largo (/16) equivale a egress permissivo.
run "egress_control_prefixo_ipv6_largo_falha" {
  command = plan

  variables {
    egress_allowlist_control = ["2000::/16"]
  }

  expect_failures = [var.egress_allowlist_control]
}

run "egress_allowlist_explicita_passa" {
  command = plan

  variables {
    egress_allowlist_data = ["10.10.0.0/24"]
  }

  assert {
    condition     = output.topology.data_plane.egress_posture == "allowlist"
    error_message = "Uma allowlist não-vazia e não-permissiva deve dar postura 'allowlist'."
  }
}

run "egress_vazio_e_deny_all" {
  command = plan

  assert {
    condition     = output.topology.control_plane.egress_posture == "deny-all"
    error_message = "Allowlist vazia = deny-all (fail-closed, ADR-004)."
  }
}
