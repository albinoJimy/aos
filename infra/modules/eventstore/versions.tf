terraform {
  required_version = ">= 1.11.0, < 2.0.0"
  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.0"
    }
  }
}
