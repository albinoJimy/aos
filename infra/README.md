# `infra/` — IaC dev/staging

Infraestrutura **declarativa** (OpenTofu / Terraform) para os ambientes **dev** e
**staging** do AOS. Materializa a **topologia de referência** (tecnica/10 §3–4): separação
física entre **plano de controlo** (decide) e **plano de dados** (executa e regista), cada um
com a sua rede default-deny e a sua escala independente. Provisiona a fundação que os
componentes consomem — **redes por plano**, **Event Store**, **cofre de segredos** e os
**scaffolds de plano** — com **estado remoto + locking**, `apply` idempotente e `destroy`
limpo. Não provisiona lógica de negócio.

## Topologia de planos (AOS-098)

```
                 ┌───────────────── PLANO DE CONTROLO (decide) ─────────────────┐
   region /      │  network_control (default-deny + egress allowlist, ADR-004)  │
   soberania     │  control_plane: ORQ · SCH · ADM · PDP  (× control_plane_replicas) │
   (ADR-011)     │  secrets (Credential Broker/Vault, ADR-006)                  │
                 └──────────────────────────────────────────────────────────────┘
                 ┌───────────────── PLANO DE DADOS (executa/regista) ───────────┐
                 │  network_data (default-deny + egress allowlist, ADR-004)     │
                 │  data_plane: workers (× data_plane_worker_replicas)          │
                 │             microvm-pool (× microvm_pool_size)               │
                 │  eventstore (NATS JetStream, append-only, ADR-007)           │
                 └──────────────────────────────────────────────────────────────┘
```

Cada plano tem **rede e contagem de réplicas próprias** => escala de forma independente. Os
componentes ORQ/SCH/ADM/PDP e os workers/pool são **scaffolds** (placeholders): a lógica
interna é de AOS-099 (workers), AOS-100 (replicação ES) e AOS-103 (pool de microVMs).

## Guardrails (invioláveis, fail-closed)

| Guardrail | Como | AC |
|---|---|---|
| **Egress default-deny** | `egress_allowlist_*` vazia = deny-all; validação rejeita `0.0.0.0/0`, `::/0`, `/0` **e prefixos demasiado largos** (IPv4 < `/8`, IPv6 < `/32`), fechando o contorno da rota-default partida em `0.0.0.0/1`+`128.0.0.0/1` (ADR-004). | AC3 |
| **Separação de planos** | Duas redes + módulos `control-plane`/`data-plane`, réplicas por plano. | AC4 |
| **3 modelos de implantação** | `deployment_model ∈ {self_hosted, on_prem, cloud}` (validação). | AC5 |
| **Soberania regional** | `backup_region`/`replica_region` têm de == `region` ou pertencer a `sovereignty_regions`; caso contrário o **plan falha** (ADR-011). | AC5 |
| **Estado cifrado + locking** | backend S3 `use_lockfile`; `encrypt` on no modelo cloud; **bucket de estado dentro do board de soberania** (`region` do backend alinhada com `var.region`, ADR-011); zero segredos. | AC6 |

Todos os guardrails de input disparam **offline** (validação de variável, antes do provider).

> **Nota de âmbito (AC3, camada de aplicação).** Uma allowlist **não-vazia** materializa
> cada CIDR como etiqueta `aos.egress.allow.<i>` para o **egress-proxy / Model Gateway**
> aplicarem a jusante; ao nível do Docker, uma rede com allowlist deixa de ser `internal`
> (ganha gateway), pelo que a restrição per-CIDR é **advisory (por etiqueta), não um egress
> firewall efectivo nesta camada**. O que ESTE IaC garante fail-closed é o **deny-all por
> omissão** (allowlist vazia = rede `internal`, sem gateway) e a **rejeição de entradas
> permissivas**. O enforcement per-destino é responsabilidade da camada de egress a jusante.

## Layout

```
infra/
  versions.tf          # Terraform >=1.11 / OpenTofu >=1.10 + providers pinados
  backend.tf           # backend S3 com locking nativo (use_lockfile)
  main.tf              # liga redes/planos/ES/secrets + check de soberania
  variables.tf         # knobs (sem segredos) + validações fail-closed
  outputs.tf           # topologia, soberania e endpoints
  locals.tf            # nomeação + etiquetas comuns + regiões admissíveis
  backend-dev.hcl      # config de estado remoto (dev)
  backend-staging.hcl  # config de estado remoto (staging)
  env/
    dev.tfvars         # parâmetros dev  (versionado, sem segredos)
    staging.tfvars     # parâmetros staging (versionado, sem segredos)
  modules/
    network/           # rede por plano + egress default-deny/allowlist (ADR-004)
    control-plane/     # scaffold ORQ/SCH/ADM/PDP (AOS-098)
    data-plane/        # scaffold workers + microvm-pool (AOS-098; lógica em AOS-099/103)
    eventstore/        # NATS JetStream, append-only, replicado (ADR-007)
    secrets/           # Vault / Credential Broker, tokens JIT (ADR-006)
  tests/
    *.tftest.hcl       # testes nativos OFFLINE (soberania, egress, modelo, defaults)
    secret-scan.sh     # gate OFFLINE de scan de segredos ao código/estado (ADR-006)
    idempotence.sh     # teste de idempotência APPLY-TIME (requer daemon Docker)
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
`-var-file=env/staging.tfvars`.

## Testes

### Offline (sem daemon Docker) — guardrails de input

Os testes nativos (`tests/*.tftest.hcl`) usam `mock_provider` e correm **sem daemon**. Provam
os guardrails de soberania (ADR-011), egress default-deny (ADR-004), modelo de implantação e a
topologia de planos por defeito:

```bash
tofu init -backend=false
tofu test
```

### Offline (sem daemon Docker) — scan de segredos (AC6, ADR-006)

Gate automatizado que falha se houver material de segredo em texto claro no código IaC ou
num `tfstate` acidentalmente versionado. Só usa `grep` — corre no CI sem Docker:

```bash
bash tests/secret-scan.sh
```

### Apply-time (requer daemon Docker) — idempotência

```bash
docker compose -f bootstrap/docker-compose.yml up -d
export AWS_ACCESS_KEY_ID=aos-dev-minio AWS_SECRET_ACCESS_KEY=aos-dev-minio-pass
ENV=dev bash tests/idempotence.sh    # apply #1 (AC1) + plan #2 = "No changes" (AC2)
```

## Garantias

| Requisito | Como |
|---|---|
| **Declarativo** | 100% OpenTofu/Terraform HCL; nenhum passo imperativo. |
| **Estado remoto** | backend S3 (`backend.tf` + `backend-<env>.hcl`). |
| **Locking** | `use_lockfile = true` (S3-native, sem DynamoDB). |
| **Cifra do estado** | `encrypt` parametrizado por `backend-<env>.hcl`; **on** em S3/GCS real (cloud). |
| **`apply` idempotente** | Recursos convergentes; reexecutar não recria (verificável: 2.º `plan` = *no changes*). |
| **`destroy` limpo** | Volumes/containers/rede geridos; sem recursos órfãos. |
| **Planos separados** | 2 redes + módulos `control-plane`/`data-plane`; escala independente por plano. |
| **Egress default-deny** | Allowlist vazia = deny-all; sem `0.0.0.0/0`/`::/0` (ADR-004). |
| **Soberania fail-closed** | `backup/replica_region` não cruzam a fronteira; senão o plan falha (ADR-011). |
| **dev vs staging por var-file** | `env/dev.tfvars` / `env/staging.tfvars`, versionados, **sem segredos**. |
| **Sem segredos** | Var-files só topologia; credenciais de estado por env vars; root token de dev gerado em runtime (ADR-006). |

## ADRs aplicáveis

ADR-004 (isolamento/egress), ADR-006 (broker/vault), ADR-007 (Event Store replicado),
ADR-011 (soberania regional).
