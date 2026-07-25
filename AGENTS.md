# AOS — Agentic OS de Referência

> Guia para agentes de IA que vão trabalhar neste repositório. Lê este ficheiro antes de tocar em código, testes ou infraestrutura. O projecto escreve-se em **português europeu (PT-PT)**; mantém o idioma nas alterações que fizeres.

## 1. O que é este projecto

O **AOS** é um *Agentic OS de Referência*. Para a **v1**, a forma do produto é um **runtime de referência deployável**: o binário/serviço `aos` que se instala e corre, hospeda *runs* de agentes sob a cadeia de governança REAL e expõe uma interface externa mínima (CLI + API `net/http` stdlib). A visão de longo prazo de *blueprint/platforma standalone* vive no `_FONTE_agentic-os-ideal.md` e só muda por emenda da Carta. É um monorepo que contém a fundação (código, infraestrutura declarativa dev/staging, especificações e desenho técnico). A lógica de negócio de cada componente é entregue pelos tickets `AOS-NNN` dos dezassete epics do backlog (EPIC-01..EPIC-17).

A tese central: tornar as falhas **arquitecturalmente impossíveis**, não apenas desencorajadas. Para isso assenta em três fundações não-negociáveis:

- **Reference Monitor (RM)** mandatório — toda a *tool call* passa por ele.
- **Identidade não-humana por agente (NHI)** — tokens scoped/time-bound numa cadeia de delegação `on-behalf-of` que termina num humano.
- **Execução durável ao nível do passo** — idempotência por passo, checkpoint, replay `resume-from-step`, efeitos externos isolados em *activities*.

As fontes de verdade canónicas são: [`specs/00_AOS_Carta.md`](specs/00_AOS_Carta.md), [`_BRIEF.md`](_BRIEF.md), [`specs/01_Engineering_Standards_e_Handoff.md`](specs/01_Engineering_Standards_e_Handoff.md) e [`specs/00_System_Spec.md`](specs/00_System_Spec.md).

## 2. Stack tecnológica

| Camada | Tecnologia de referência |
|---|---|
| Linguagem | **Go 1.24** (`go 1.24` nos `go.mod`) |
| Orquestração de dependências | Módulos Go locais (`replace` por path) — 46 módulos em `packages/` (incluindo `platform/attestation`, adicionado no âmbito de EPIC-16/AOS-177) |
| Infraestrutura | **OpenTofu** ≥ 1.10 (ou Terraform ≥ 1.11) + provider Docker |
| Estado remoto | S3-compatível (MinIO local em dev) com `use_lockfile` nativo |
| Event Store / transporte | **NATS JetStream** (dev/staging via Docker); alternativas: Redis/Postgres |
| Cofre de segredos | **HashiCorp Vault** (`-dev` em dev; selado em staging) |
| Motor de políticas | **Cedar** (`cedar-go` v1.8.0) — policy-as-code versionado e assinado |
| Observabilidade | OpenTelemetry GenAI semantic conventions (spans `invoke_agent`/`execute_tool`/`chat`) |
| Isolamento | microVM Firecracker/Kata ou gVisor; seccomp; jails secundários |
| Container runtime | Docker; imagem de produção `deploy/node/Dockerfile` distroless/static/non-root/read-only |
| CI local | `bash` + `make` + `gofmt`/`go vet`/`staticcheck`/`gosec`/`govulncheck` |

## 3. Estrutura do repositório

```
.
├── packages/               # Código Go organizado por camada canónica
│   ├── cmd/                #   Binários: aos (nó de produção), aos-demo
│   ├── kernel/             #   Plano de execução: RM (Reference Monitor), RT (Agent Runtime)
│   ├── control-plane/      #   Plano de controlo: ORQ, SCH, PDP, budget, governance/*
│   ├── platform/           #   Serviços de plataforma: MEM, REG, GW, BRK, identity, audit, …
│   ├── substrate/          #   Log & substrato: ES (Event Store), bus, sandbox, otel-genai, redaction
│   ├── integration/        #   Composition-root / wiring / ápice de enforcement composto
│   ├── testkit/            #   Fixtures, mocks deterministas e conversor cov2lcov (AOS-109)
│   ├── qa/                 #   Testes de qualidade (dr-e2e, ux-dx)
│   └── security-tests/     #   Cenários adversariais de segurança
├── infra/                  # IaC dev/staging (OpenTofu/Terraform)
│   ├── modules/            #   network, control-plane, data-plane, eventstore, secrets
│   ├── env/                #   dev.tfvars, staging.tfvars (versionados, sem segredos)
│   ├── tests/              #   Testes nativos *.tftest.hcl + secret-scan.sh + idempotence.sh
│   └── bootstrap/          #   MinIO local para estado remoto
├── specs/                  # Backlog executável: System Spec, Standards, EPIC-01..EPIC-17
├── tecnica/                # Desenho de solução, contratos, ADRs, matrizes de rastreabilidade
├── docs/                   # ADRs materializados, runbooks, hipercare, relatórios
├── scripts/ci/             # Fonte de verdade dos gates de CI (fail-closed)
├── deploy/node/            # Dockerfile, healthprobe, scripts de package/SBOM
├── analises/               # Relatórios de auditoria e scripts de remediação
├── coverage/               # Relatório LCOV gerado pelos testes (ignorado pelo git)
└── Makefile                # Wrappers para IaC e gates de CI
```

### Camadas canónicas (catálogo de componentes)

| Código | Componente | Onde vive | Epic principal |
|---|---|---|---|
| RM | Reference Monitor (PEP) | `packages/kernel/reference-monitor` | EPIC-01 |
| RT | Agent Runtime | `packages/kernel/agent-runtime` | EPIC-02 |
| ORQ | Orquestrador | `packages/control-plane/orchestrator` | EPIC-03 |
| SCH | Escalonador | `packages/control-plane/scheduler` | EPIC-03 |
| PDP | Policy Decision Point | `packages/control-plane/pdp` | EPIC-09 |
| MEM | Memory Service | `packages/platform/memory` | EPIC-04 |
| REG | Skill/Tool Registry | `packages/platform/registry` | EPIC-05 |
| GW | Model Gateway | `packages/platform/model-gateway` | EPIC-06 |
| BRK | Credential Broker + Vault | `packages/platform/broker` + `infra/modules/secrets` | EPIC-06/07 |
| ES | Event Store | `packages/substrate/eventstore` + `infra/modules/eventstore` | EPIC-01 |
| SBX | Sandbox Substrate | `packages/substrate/sandbox` | EPIC-07 |
| OBS | Observabilidade & Evals | `packages/substrate/otel-genai`, spans em todo o código | EPIC-08 |
| GOV | Governação & Learning | `packages/control-plane/governance/*`, `packages/platform/identity` | EPIC-09/16 |

### Invariantes de fronteira

- **RM é o único caminho para tool calls.** Nenhum pacote chama tools directamente; `platform/` e `substrate/` são alcançados através do `kernel/RM`.
- **Sentido de dependências permitido:** `control-plane` → `kernel` → `platform`/`substrate`. O substrato não conhece camadas acima.
- **Excepções escopadas e justificadas:** adaptadores de fronteira finos, tipos de domínio partilhados entre o kernel e a plataforma, e consumo de serviços de plataforma/substrato pelo plano de controlo estão formalizados em **ADR-019** e reflectidos na baseline do gate `layer-lint`.
- **Sem dependências circulares.** Os `go.mod` usam `replace` path-local para montar o grafo offline.

## 4. Build e testes — comandos essenciais

### Arranque rápido do ambiente dev (≤ 30 min)

Requer: Docker, OpenTofu (ou Terraform), `make` (opcional).

```bash
# 1. Estado remoto local (MinIO)
make bootstrap
export AWS_ACCESS_KEY_ID=aos-dev-minio
export AWS_SECRET_ACCESS_KEY=aos-dev-minio-pass

# 2. Init · Plan · Apply
make init  ENV=dev
make plan  ENV=dev
make apply ENV=dev
make output ENV=dev

# 3. Verificar idempotência
make plan ENV=dev      # deve reportar "No changes"

# 4. Teardown limpo
make destroy ENV=dev
make bootstrap-down
```

Endpoints esperados em dev:

| Serviço | Endpoint |
|---|---|
| Event Store (NATS) | `nats://localhost:4222` |
| Monitorização NATS | `http://localhost:8222` |
| Vault (BRK) | `http://localhost:8200` |

Para staging: `make init ENV=staging` (reconfigura backend), `make apply ENV=staging`. O Vault de staging arranca selado — `vault operator init` + `unseal` fora-de-banda.

### Comandos de CI local (fail-closed)

A fonte de verdade dos gates é `scripts/ci/*.sh`; `make` é apenas wrapper. Reproduz tudo com um comando:

```bash
make ci                 # todos os gates locais
make ci-selftest        # prova que falhas de lint/test/política/CVE bloqueiam
make ci-all             # gates + self-tests
```

Gates individuais:

```bash
make ci-secrets         # scan de segredos
make ci-build           # go build ./... por módulo
make ci-lint            # gofmt, go vet, staticcheck + arch-lint AOS-003
make ci-test            # go test -race + cobertura + relatório LCOV
make ci-replay          # replay determinístico + idempotência por passo
make ci-memory          # integridade/migração/proveniência de memória
make ci-supplychain     # vectores adversariais de supply-chain
make ci-routing         # roteamento/failover do GW
make ci-apex            # composition-root / enforcement composto
make ci-security        # cenários adversariais de segurança
make ci-evalgate        # eval harness + golden-sets
make ci-scale           # carga/escala
make ci-dr-e2e          # DR / node loss / failover / resume
make ci-ux-dx           # usabilidade / anti-fadiga
make ci-sast            # gosec (HIGH/CRITICAL falham)
make ci-sca             # govulncheck (vulns afetantes falham)
make ci-policy          # teste de política do PDP (allow/deny + assinatura)
```

Cobertura apenas unit + LCOV:

```bash
make cover              # ou make test-unit — emite coverage/lcov.info
COVERAGE_MIN=90 make cover
```

### Formatação e validação da IaC

```bash
make fmt
make validate
```

### Testes nativos da IaC (offline, sem Docker)

```bash
cd infra && tofu init -backend=false && tofu test
bash infra/tests/secret-scan.sh
```

### Build da imagem de produção

```bash
docker build -f deploy/node/Dockerfile -t aos-node:local .
```

## 5. Convenções de código e desenvolvimento

### Idioma

- **Documentação e comentários:** PT-PT. Termos técnicos consagrados podem ficar em inglês (`reference monitor`, `taint`, `durable execution`, `eval-gate`, `replay`).
- **Código Go:** nomes, pacotes e interfaces em inglês, seguindo as convenções oficiais de Go.

### Versionamento e commits

- **Conventional Commits** com ID de ticket: `tipo(AOS-NNN): descrição no imperativo`.
  - Exemplos: `feat(AOS-013): idempotency key por passo no runtime`, `fix(AOS-072): fecha egress default-deny em DNS`.
  - Tipos: `feat`, `fix`, `chore`, `refactor`, `test`, `docs`, `spike`.
- **Branches:** `feature/AOS-NNN-<slug>` ou `fix/AOS-NNN-<slug>`.
- **Artefactos comportamentais:** SemVer (`MAJOR.MINOR.PATCH`) — skills, módulos de prompt, schemas de tool, schema de memória. Major = quebra de contrato; Minor = capacidade retro-compatível; Patch = correcção sem alteração de contrato.

### Estrutura de um ticket / entrega

Antes de implementar, lê o ticket `AOS-NNN` no epic correspondente em `specs/EPIC-XX_*.md`, os ADRs citados e o documento técnico de referência em `tecnica/`. A implementação deve ser o mínimo suficiente para os Critérios de Aceitação; não expandas o escopo. Bugs fora de escopo abrem novo ticket.

### ADRs

- Formato MADR; um ficheiro por decisão: `docs/adr/ADR-NNN-slug-curto.md`.
- Não contradigas um ADR canónico sem um ADR de supersessão explícito.
- ADRs em vigor: catálogo em `specs/00_System_Spec.md` §11 e materializados em `docs/adr/README.md`.

## 6. Testes

### Estratégia

O pipeline é **fail-closed**: qualquer gate vermelho bloqueia merge. A CI (`.github/workflows/ci.yml`) apenas invoca `scripts/ci/*.sh`; a lógica dos gates vive localmente.

### Tipos de teste

- **Unitários:** `go test ./... -race -covermode=atomic` por módulo.
- **Integração:** testes cross-package (ex.: `identity_gate_integration_test.go` no PDP, wiring em `packages/integration`).
- **Domínio AOS:** replay determinístico, idempotência por passo, testes de política (allow/deny), eval-gate, supply-chain, segurança adversarial, carga/escala, DR/E2E.
- **IaC:** testes `tftest.hcl` offline + `idempotence.sh` apply-time.

### Cobertura

- Piso histórico: cobertura do `kernel/reference-monitor` ≥ 80%.
- Generalizado (AOS-109): `COVERAGE_MIN` aplicado a módulos listados em `COVERAGE_GATED_MODULES` em `scripts/ci/lib.sh` (kernel, testkit, várias superfícies de governação).
- O gate emite `coverage/lcov.info` via conversor `cov2lcov` em `packages/testkit`.

### Baselines

`lint`, `sast` e `sca` comparam descobertas com baselines (`scripts/ci/baseline/*.txt`). Dívida pré-existente documentada é tolerada; **descobertas novas bloqueiam**. Cada entrada da baseline tem dono e remediação documentados.

### Auto-testes dos gates (`ci-selftest`)

Provam de forma determinista e sem rasto no repo que:

- módulo mau (gofmt sujo + teste que falha) bloqueia `lint`/`test`;
- assinatura do bundle PDP adulterada bloqueia `policy-test`;
- CVE afetante fora da baseline bloqueia `sca`;
- golden trajectory adulterada bloqueia o harness de replay.

## 7. Segurança e governação

### Princípios não-negociáveis

1. **Toda a tool call mediada pelo RM.** Não escrevas código que chame uma tool directamente fora do Reference Monitor.
2. **Identidade antes de autoridade.** Cada agente/sub-agente tem NHI scoped/time-bound; toda a acção resolve numa cadeia de delegação até um humano.
3. **Execução durável.** Idempotency key = `f(run_id, step_id)`; efeitos externos isolados em *activities*; replay `resume-from-step`.
4. **Untrusted não comanda.** Resultados de tools, web, memória e schemas MCP são dados e têm *taint*; não autorizam acções privilegiadas.
5. **Segredos só via Broker/Vault.** Nenhum segredo em código, logs ou spans. Credenciais downstream são injectadas server-side, JIT, com TTL curto.
6. **Egress default-deny.** Rede negada por omissão; allowlist explícita e validada (rejeita `0.0.0.0/0` e prefixos largos).
7. **Política default-deny.** Decisões de autorização expressas em policy-as-code (Cedar/Rego), versionada, assinada e testada.
8. **Fail-closed por omissão.** Timeouts, allowlists e gates ambíguos resolvem pelo lado seguro.
9. **Auto-modificação com rede.** Nenhum artefacto auto-escrito chega a produção sem eval-gate, canary e ratificação humana assinada.
10. **Soberania regional.** Dados não cruzam fronteiras sem configuração explícita; o plano da IaC falha se `backup/replica_region` violarem o board de soberania.

### Scan de segredos

- `make ci-secrets` é pré-condição de merge.
- Também corre `gitleaks` na CI.
- `infra/tests/secret-scan.sh` verifica a IaC e `tfstate`.

### Container de produção

- `deploy/node/Dockerfile`: imagem distroless `static-debian12:nonroot`, UID numérico `65532:65532`, root-fs read-only, sem shell, sem segredos.
- O binário compila com `CGO_ENABLED=0`, `GOPROXY=off`, `-trimpath`, `-ldflags="-s -w -buildid="`.
- Estado durável escreve só num volume explícito (`-v aos-data:/var/lib/aos`); não se declara `VOLUME` de propósito para evitar volumes anónimos órfãos.
- Arranque em produção exige `AOS_MODE=production` + `AOS_ISSUER_PUBKEY` (trust-anchor-only) + `AOS_BOARD_REGIONS` não-vazio; caso contrário aborta fail-closed.

## 8. Deployment e operações

### Topologia de planos

A infraestrutura separa fisicamente:

- **Plano de controlo** (decide): ORQ, SCH, ADM, PDP, secrets (Vault).
- **Plano de dados** (executa/regista): workers stateless, microVM pool, Event Store NATS JetStream.

Cada plano tem a sua rede com egress default-deny/allowlist e escala independente.

### Modelos de implantação

`deployment_model ∈ {self_hosted, on_prem, cloud}`. A variável é validada pela IaC; staging usa `on_prem`.

### Estado e locking

- Backend S3 (`infra/backend.tf`) com `use_lockfile = true` (locking nativo, sem DynamoDB).
- Estado cifrado (`encrypt`) no modelo cloud; em dev o MinIO local não cifra.
- Bucket de estado dentro do board de soberania (`region` alinhada com `var.region`).

### Runbooks

Consulta `docs/runbooks/` para procedimentos operacionais (escala, incidentes, etc.).

## 9. O que fazer quando entras num ticket

1. Lê o ticket `AOS-NNN` no ficheiro `specs/EPIC-XX_*.md` correspondente.
2. Lê os ADRs citados e o documento `tecnica/` de referência.
3. Confirma que as dependências estão `Done` ou disponíveis; se bloquearem, pára e reporta.
4. Implementa o mínimo suficiente para os Critérios de Aceitação.
5. Escreve/actualiza testes: unit, integração e os de domínio aplicáveis.
6. Corres os gates locais relevantes (`make ci-build`, `make ci-lint`, `make ci-test`, etc.).
7. Garante scan de segredos limpo e cobertura sem regressão.
8. Commits em Conventional Commits; branch `feature/AOS-NNN-<slug>`; abre PR com o template de `specs/01_Engineering_Standards_e_Handoff.md` §7.

## 10. Ficheiros-chave para consulta rápida

| Para… | Lê… |
|---|---|
| Forma do produto + registo de decisões congeladas | `specs/00_AOS_Carta.md` |
| Visão do sistema e roadmap | `specs/00_System_Spec.md` |
| Padrões de engenharia / DoR / DoD / PR | `specs/01_Engineering_Standards_e_Handoff.md` |
| Convenções e catálogo canónico | `_BRIEF.md` |
| Mapa de componentes do código | `packages/README.md` |
| IaC e arranque dev/staging | `infra/README.md` |
| Gates de CI e como correr localmente | `CONTRIBUTING.md` |
| ADRs materializados | `docs/adr/README.md` |
| Empacotamento do nó de produção | `deploy/node/README.md` |
| Contratos de interface | `tecnica/12_Contratos_de_Interface.md` |
| Testes e fixtures | `packages/testkit/README.md`, `docs/testing/README.md` |

## 11. Notas para o agente executor

- Não inventes estruturas fora das camadas canónicas. Se um componente novo parecer necessário, verifica se já existe um código/catálogo correspondente.
- Não ignores um ADR citado. Se algo parecer contraditório, pára e reporta ambiguidade.
- Não deixes TODOs órfãos nem código morto.
- Não committes binários, ficheiros de cobertura ou artefactos de build.
- Não introduzas segredos em texto claro. Qualquer credencial deve vir de env var, vault ou ficheiro montado em runtime.
- Mantém o idioma PT-PT em documentação e comentários novos.
- A cobertura não pode regride abaixo do piso configurado. Se o teu ticket não justificar uma descida, reforça os testes.
- Preferes `go test` determinista: usa fixtures do `testkit` (relógios manuais, geradores sequenciais, ES in-memory) em vez de `time.Now()`/`rand`/UUID em asserções.
