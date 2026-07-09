# Ambiente STAGING — versionado, SEM SEGREDOS (ver specs/01 §3, ADR-006).
# Uso: tofu apply -var-file=env/staging.tfvars

environment = "staging"
project     = "aos"

# Rede: interna (egress default-deny) — endurecimento face a dev (ADR-004 / Princípio 7).
network_internal = true
network_subnet   = "172.29.0.0/16"

# Event Store: cluster replicado, sem SPOF (ADR-007).
eventstore_image        = "nats:2.10-alpine"
eventstore_cluster_size = 3
eventstore_client_port  = 4222

# Segredos: Vault server persistente. Arranca SELADO — init/unseal fora-de-banda.
secrets_image    = "hashicorp/vault:1.16"
secrets_dev_mode = false
secrets_port     = 8200

extra_labels = {
  "aos.tier" = "staging"
}
