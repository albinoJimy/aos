# Ambiente DEV — versionado, SEM SEGREDOS (ver specs/01 §3, ADR-006).
# Uso: tofu apply -var-file=env/dev.tfvars

environment = "dev"
project     = "aos"

# Rede: egress permitido para conveniência de desenvolvimento.
network_internal = false
network_subnet   = "172.28.0.0/16"

# Event Store: single-node (arranque rápido).
eventstore_image        = "nats:2.10-alpine"
eventstore_cluster_size = 1
eventstore_client_port  = 4222

# Segredos: Vault em modo -dev (in-memory, unseal automático). Nunca em produção.
secrets_image    = "hashicorp/vault:1.16"
secrets_dev_mode = true
secrets_port     = 8200

extra_labels = {
  "aos.tier" = "dev"
}
