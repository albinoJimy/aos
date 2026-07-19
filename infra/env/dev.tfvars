# Ambiente DEV — versionado, SEM SEGREDOS (ver specs/01 §3, ADR-006).
# Uso: tofu apply -var-file=env/dev.tfvars

environment = "dev"
project     = "aos"

# --- Topologia de planos: modelo de implantação e soberania (ADR-011) ---
deployment_model    = "self_hosted"
region              = "eu-west-1"
sovereignty_board   = "eu"
sovereignty_regions = ["eu-west-1", "eu-central-1"]
# backup_region/replica_region omitidos => mesma região (fail-closed dentro do board).

# --- Escala independente por plano ---
control_plane_replicas     = 1
data_plane_worker_replicas = 1
microvm_pool_size          = 1

# --- Rede por plano: default-deny + egress allowlist (ADR-004) ---
# Sub-redes distintas por plano (não sobrepostas).
network_subnet_control = "172.28.0.0/24"
network_subnet_data    = "172.28.1.0/24"
# Dev pode ter egress mais permissiva por conveniência, mas NUNCA 0.0.0.0/0:
# aqui autoriza-se apenas o CIDR do proxy/registry de desenvolvimento (allowlist explícita).
egress_allowlist_control = ["172.28.0.0/24"]
egress_allowlist_data    = ["172.28.1.0/24"]

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
