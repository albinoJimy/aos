locals {
  name_prefix = "${var.project}-${var.environment}"

  # Etiquetas comuns a todos os recursos. Sem segredos — apenas metadados.
  common_labels = merge(
    {
      "aos.project"     = var.project
      "aos.environment" = var.environment
      "aos.managed-by"  = "opentofu"
    },
    var.extra_labels,
  )
}
