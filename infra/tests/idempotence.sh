#!/usr/bin/env bash
# =============================================================================
# Teste de IDEMPOTÊNCIA (AC1/AC2) — APPLY-TIME (requer Docker daemon).
# =============================================================================
# Ao contrário dos testes nativos `*.tftest.hcl` (que correm OFFLINE com
# mock_provider e verificam guardrails de INPUT: soberania, egress, modelo),
# este script exige um daemon Docker e um store de estado (MinIO local do
# bootstrap). Destina-se ao CI-com-docker, NÃO à verificação offline.
#
# Prova:
#   AC1 — ambiente limpo levanta-se de zero SEM passos manuais (apply #1).
#   AC2 — reaplicar sobre ambiente existente = no-op (plan #2 = "No changes").
#
# Uso:
#   ENV=dev bash infra/tests/idempotence.sh
#   ENV=staging bash infra/tests/idempotence.sh
# =============================================================================
set -euo pipefail

ENV="${ENV:-dev}"
INFRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$INFRA_DIR"

TOFU="$(command -v tofu || command -v terraform)"
echo ">> A usar: $TOFU  (ENV=$ENV)"

# 0) Credenciais do store de estado (MinIO local do bootstrap) — SEM SEGREDOS no código.
: "${AWS_ACCESS_KEY_ID:?exporta AWS_ACCESS_KEY_ID (ver infra/bootstrap/README.md)}"
: "${AWS_SECRET_ACCESS_KEY:?exporta AWS_SECRET_ACCESS_KEY}"

# 1) Init do backend do ambiente.
"$TOFU" init -backend-config="backend-${ENV}.hcl" -reconfigure

# 2) APPLY #1 — de zero, sem intervenção manual (AC1).
"$TOFU" apply -auto-approve -var-file="env/${ENV}.tfvars"

# 3) PLAN #2 — tem de ser no-op (AC2). `-detailed-exitcode`: 0=sem mudanças, 2=mudanças.
set +e
"$TOFU" plan -detailed-exitcode -var-file="env/${ENV}.tfvars"
CODE=$?
set -e

if [ "$CODE" -eq 0 ]; then
  echo ">> IDEMPOTENTE: 2.º plan = 'No changes'. (AC2 OK)"
elif [ "$CODE" -eq 2 ]; then
  echo "!! NÃO idempotente: o 2.º plan mostra alterações. (AC2 FALHOU)" >&2
  exit 1
else
  echo "!! Erro no plan (exit $CODE)." >&2
  exit "$CODE"
fi
