# Ambiente STAGING — versionado, SEM SEGREDOS (ver specs/01 §3, ADR-006).
# Uso: tofu apply -var-file=env/staging.tfvars

environment = "staging"
project     = "aos"

# --- Topologia de planos: modelo de implantação e soberania (ADR-011) ---
deployment_model    = "on_prem"
region              = "eu-west-1"
sovereignty_board   = "eu"
sovereignty_regions = ["eu-west-1", "eu-central-1"]
# backup_region/replica_region dentro da região (dados nunca saem da fronteira).
backup_region  = "eu-west-1"
replica_region = "eu-west-1"

# --- Escala independente por plano (plano de controlo e de dados escalam separadamente) ---
control_plane_replicas     = 2
data_plane_worker_replicas = 3
microvm_pool_size          = 3

# --- Rede por plano: default-deny ESTRITO (egress allowlist VAZIA = deny-all, ADR-004) ---
network_subnet_control   = "172.29.0.0/24"
network_subnet_data      = "172.29.1.0/24"
egress_allowlist_control = []
egress_allowlist_data    = []

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
