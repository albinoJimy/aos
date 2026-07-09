# AOS — Agentic OS de Referência

Monorepo do **AOS**, o *blueprint* de plataforma para correr, coordenar e governar
agentes de IA. Este repositório contém a **fundação** — esqueleto de código e a
infraestrutura declarativa de **dev/staging**. A lógica de negócio de cada
componente é entregue pelos tickets `AOS-NNN` dos epics.

> **Fonte de convenções:** [`_BRIEF.md`](_BRIEF.md) e
> [`specs/01_Engineering_Standards_e_Handoff.md`](specs/01_Engineering_Standards_e_Handoff.md).
> Idioma do projeto: **PT-PT**.

---

## Estrutura

```
.
├── packages/            # Código, por camada canónica (só esqueleto)
│   ├── kernel/          #   RM (Reference Monitor) + RT (Agent Runtime)
│   ├── control-plane/   #   ORQ + SCH + PDP
│   ├── platform/        #   MEM + REG + GW + BRK
│   └── substrate/       #   ES (Event Store) + SBX (Sandbox)
├── infra/               # IaC dev/staging (OpenTofu/Terraform)
│   ├── modules/         #   network · eventstore · secrets
│   ├── env/             #   dev.tfvars · staging.tfvars (sem segredos)
│   └── bootstrap/       #   estado remoto local (MinIO)
├── specs/               # Backlog executável (System Spec, Standards, EPIC-01..12)
└── tecnica/             # Desenho de solução (arquitectura, contratos, ADRs)
```

Cada pasta tem o seu `README.md`. O mapa camadas↔componentes está em
[`packages/README.md`](packages/README.md); a IaC em [`infra/README.md`](infra/README.md).

---

## Arranque rápido (≤ 30 min)

Sobe um ambiente **dev** completo (rede + Event Store + Vault) com estado remoto e locking.

### 1. Pré-requisitos (~5 min)

| Ferramenta | Versão | Verificar |
|---|---|---|
| **Docker** (Desktop/Engine) | a correr | `docker info` |
| **OpenTofu** ≥ 1.10 *(ou Terraform ≥ 1.11)* | locking S3 nativo | `tofu version` |
| **make** *(opcional)* | qualquer | `make --version` |

> Sem `make`? Corre os comandos `tofu` diretamente — ver [`infra/README.md`](infra/README.md).
> Em Windows, `make` vem com Git Bash/`choco install make`; os comandos `tofu` funcionam em PowerShell.

### 2. Estado remoto local (~3 min)

```bash
make bootstrap        # sobe MinIO (S3-compatível) e cria o bucket aos-tfstate
```

Exporta as credenciais do store (dev-local, **não são segredos de produção**):

```bash
# bash / Git Bash
export AWS_ACCESS_KEY_ID=aos-dev-minio
export AWS_SECRET_ACCESS_KEY=aos-dev-minio-pass
```

```powershell
# PowerShell
$env:AWS_ACCESS_KEY_ID = "aos-dev-minio"
$env:AWS_SECRET_ACCESS_KEY = "aos-dev-minio-pass"
```

### 3. Init · Plan · Apply (~10 min)

```bash
make init  ENV=dev      # tofu init -backend-config=backend-dev.hcl
make plan  ENV=dev      # revê o que vai ser criado
make apply ENV=dev      # provisiona (idempotente)
make output ENV=dev     # mostra os endpoints
```

Esperado após `apply`:

| Serviço | Endpoint (dev) |
|---|---|
| Event Store (NATS) | `nats://localhost:4222` |
| Monitorização NATS | <http://localhost:8222> |
| Vault (BRK) | <http://localhost:8200> |

Root token descartável do Vault `-dev` (gerado em runtime, **só dev**):

```bash
cd infra && tofu output -raw vault_dev_root_token
```

### 4. Verificar idempotência (~2 min)

```bash
make plan ENV=dev       # deve reportar "No changes" — apply é idempotente
```

### 5. Teardown limpo

```bash
make destroy ENV=dev            # remove containers, volumes e rede
make bootstrap-down             # desliga o MinIO (usa -v no compose para apagar o estado)
```

---

## Staging

Mesmo fluxo, com endurecimento (rede interna/egress default-deny, Event Store
replicado com 3 nós, Vault persistente selado):

```bash
make init ENV=staging           # usa backend-staging.hcl (-reconfigure)
make apply ENV=staging          # usa env/staging.tfvars
```

> O Vault de staging **arranca selado**: `vault operator init` + `unseal` são
> operação fora-de-banda. Nenhum segredo é colocado pela IaC (ADR-006).
> Para um store de estado real, aponta `backend-staging.hcl` para um bucket S3/GCS gerido.

Diferenças dev↔staging (todas por var-file versionado, sem segredos):

| Parâmetro | dev | staging |
|---|---|---|
| Rede | egress permitido | interna (default-deny) |
| Event Store | 1 nó | 3 nós (replicado) |
| Vault | `-dev` (in-memory) | server persistente (selado) |

---

## Onde ler a seguir

- **Padrões de engenharia / DoR / DoD / gates:** [`specs/01_Engineering_Standards_e_Handoff.md`](specs/01_Engineering_Standards_e_Handoff.md)
- **Visão do sistema e roadmap:** [`specs/00_System_Spec.md`](specs/00_System_Spec.md)
- **Backlog por epic:** [`specs/`](specs/) · **Desenho técnico:** [`tecnica/`](tecnica/)
- **Catálogo de componentes e ADRs:** [`_BRIEF.md`](_BRIEF.md)

## Convenções

Conventional Commits com ID de ticket (`tipo(AOS-NNN): descrição`), branches
`feature/AOS-NNN-<slug>`, SemVer para artefactos comportamentais. Detalhe em
[`specs/01_Engineering_Standards_e_Handoff.md`](specs/01_Engineering_Standards_e_Handoff.md) §5.
