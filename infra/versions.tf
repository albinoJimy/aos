# Versões e providers. IaC declarativa — Terraform >= 1.11 ou OpenTofu >= 1.10
# (necessário para locking nativo de estado S3 via `use_lockfile`; ver backend.tf).
terraform {
  required_version = ">= 1.11.0, < 2.0.0"

  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}
