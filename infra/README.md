# `infra/` — IaC dev/staging

Infraestrutura **declarativa** (OpenTofu / Terraform) para os ambientes **dev** e
**staging** do AOS. Provisiona a fundação que os componentes consomem — **rede**,
**Event Store** e **cofre de segredos** — com **estado remoto + locking**, `apply`
idempotente e `destroy` limpo. Não provisiona lógica de negócio.

## Layout

```
infra/
  versions.tf          # Terraform >=1.11 / OpenTofu >=1.10 + providers pinados
  backend.tf           # backend S3 com locking nativo (use_lockfile)
  main.tf              # liga os 3 módulos
  variables.tf         # knobs (sem segredos)
  outputs.tf           # endpoints do ambiente
  locals.tf            # nomeação + etiquetas comuns
  backend-dev.hcl      # config de estado remoto (dev)
  backend-staging.hcl  # config de estado remoto (staging)
  env/
    dev.tfvars         # parâmetros dev  (versionado, sem segredos)
    staging.tfvars     # parâmetros staging (versionado, sem segredos)
  modules/
    network/           # rede + egress default-deny (ADR-004)
    eventstore/        # NATS JetStream, append-only, replicado (ADR-007)
    secrets/           # Vault / Credential Broker, tokens JIT (ADR-006)
  bootstrap/           # MinIO local para o store de estado remoto (offline)
```

## Ciclo de vida

```bash
# 0) Estado remoto local (uma vez) — ver bootstrap/README.md
docker compose -f bootstrap/docker-compose.yml up -d
export AWS_ACCESS_KEY_ID=aos-dev-minio AWS_SECRET_ACCESS_KEY=aos-dev-minio-pass

# 1) Init do backend do ambiente
tofu init -backend-config=backend-dev.hcl

# 2) Plan / Apply (idempotente)
tofu plan  -var-file=env/dev.tfvars
tofu apply -var-file=env/dev.tfvars

# 3) Destroy (limpo)
tofu destroy -var-file=env/dev.tfvars
```

Para **staging**, troca `-backend-config=backend-staging.hcl` (com `-reconfigure`) e
`-var-file=env/staging.tfvars`. O `Makefile` na raiz encapsula estes passos
(`make apply ENV=staging`).

## Garantias

| Requisito | Como |
|---|---|
| **Declarativo** | 100% OpenTofu/Terraform HCL; nenhum passo imperativo. |
| **Estado remoto** | backend S3 (`backend.tf` + `backend-<env>.hcl`). |
| **Locking** | `use_lockfile = true` (S3-native, sem DynamoDB). |
| **`apply` idempotente** | Recursos convergentes; reexecutar não recria (verificável: 2.º `plan` = *no changes*). |
| **`destroy` limpo** | Volumes/containers/rede geridos; sem recursos órfãos. |
| **dev vs staging por var-file** | `env/dev.tfvars` / `env/staging.tfvars`, versionados, **sem segredos**. |
| **Sem segredos** | Var-files só topologia; credenciais de estado por env vars; root token de dev gerado em runtime (ADR-006). |

## ADRs aplicáveis

ADR-004 (isolamento/egress), ADR-006 (broker/vault), ADR-007 (Event Store replicado).
