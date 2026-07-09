# Estado remoto STAGING — backend S3 com locking nativo (use_lockfile em backend.tf).
# SEM SEGREDOS: credenciais via AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (ou role).
# Em staging real, aponta para um bucket S3/GCS gerido (com versioning ativado);
# o exemplo abaixo mantém o MinIO local para paridade de arranque.
bucket  = "aos-tfstate"
key     = "staging/terraform.tfstate"
region  = "us-east-1"
encrypt = true # staging real usa S3/GCS com SSE. Se apontar ao MinIO local, pôr false.

endpoints = {
  s3 = "http://localhost:9000"
}

use_path_style              = true
skip_credentials_validation = true
skip_metadata_api_check     = true
skip_requesting_account_id  = true
skip_region_validation      = true
