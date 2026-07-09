# Estado remoto DEV — backend S3 com locking nativo (use_lockfile em backend.tf).
# SEM SEGREDOS: as credenciais do store vêm de AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY.
# Por omissão aponta para o MinIO local do bootstrap (infra/bootstrap). Para um store
# S3/GCS real, troca bucket/region/endpoints.
bucket  = "aos-tfstate"
key     = "dev/terraform.tfstate"
region  = "us-east-1"
encrypt = false # MinIO local não tem KMS; SSE ligada dá 501. Em S3 real, pôr true.

# Endpoint S3-compatível (MinIO local). Remover para AWS S3 real.
endpoints = {
  s3 = "http://localhost:9000"
}

use_path_style              = true
skip_credentials_validation = true
skip_metadata_api_check     = true
skip_requesting_account_id  = true
skip_region_validation      = true
