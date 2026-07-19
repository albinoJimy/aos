# Estado remoto STAGING — backend S3 com locking nativo (use_lockfile em backend.tf).
# SEM SEGREDOS: credenciais via AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (ou role).
# Em staging real, aponta para um bucket S3/GCS gerido (com versioning ativado);
# o exemplo abaixo mantém o MinIO local para paridade de arranque.
# SOBERANIA (ADR-011): o tfstate guarda outputs sensíveis e topologia sob o board de
# soberania — o bucket de estado TEM de residir DENTRO do board (region alinhada com
# var.region / sovereignty_regions). Por isso a region fica eu-west-1, não us-east-1.
bucket  = "aos-tfstate"
key     = "staging/terraform.tfstate"
region  = "eu-west-1"
encrypt = true # staging real usa S3/GCS com SSE. Se apontar ao MinIO local, pôr false.

endpoints = {
  s3 = "http://localhost:9000"
}

use_path_style              = true
skip_credentials_validation = true
skip_metadata_api_check     = true
skip_requesting_account_id  = true
skip_region_validation      = true
