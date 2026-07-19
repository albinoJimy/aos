locals {
  name_prefix = "${var.project}-${var.environment}"

  # --- Soberania (ADR-011) ---
  # Conjunto EFECTIVO de regiões admissíveis dentro do board. Fail-closed: board vazio =>
  # só a própria region é admissível para réplicas/backups.
  sovereignty_allowed_regions = length(var.sovereignty_regions) > 0 ? var.sovereignty_regions : [var.region]

  # Regiões efectivas de backup/réplica (vazio => mesma que region).
  effective_backup_region  = var.backup_region == "" ? var.region : var.backup_region
  effective_replica_region = var.replica_region == "" ? var.region : var.replica_region

  # Etiquetas comuns a todos os recursos. Sem segredos — apenas metadados.
  common_labels = merge(
    {
      "aos.project"           = var.project
      "aos.environment"       = var.environment
      "aos.managed-by"        = "opentofu"
      "aos.region"            = var.region
      "aos.sovereignty-board" = var.sovereignty_board
      "aos.deployment-model"  = var.deployment_model
    },
    var.extra_labels,
  )
}
