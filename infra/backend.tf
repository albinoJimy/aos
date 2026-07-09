# Estado remoto com LOCKING (ADR-007 aplica a mesma filosofia ao próprio estado da IaC:
# fonte de verdade única, sem single-writer local). Configuração PARCIAL — os valores
# concretos por ambiente vêm de backend-<env>.hcl:
#
#   tofu init -backend-config=backend-dev.hcl
#   tofu init -backend-config=backend-staging.hcl -reconfigure
#
# Locking: `use_lockfile = true` usa o lockfile nativo do backend S3 (S3-native
# locking, sem DynamoDB) — GA em OpenTofu >= 1.10 / Terraform >= 1.11.
# NENHUM segredo vive aqui nem em backend-*.hcl: as credenciais do store de estado
# são fornecidas por variáveis de ambiente (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY).
terraform {
  backend "s3" {
    use_lockfile = true
    # `encrypt` é parametrizado por ambiente (backend-<env>.hcl): off no MinIO local
    # (sem KMS), on em S3/GCS real (staging). Evita SSE não suportada localmente.
  }
}
