# Convenções de teste do AOS (AOS-109)

Este documento é a **fonte de convenções** do framework de testes de referência do
AOS (EPIC-11). Estabelece a organização unit/integração, a nomenclatura, a
localização das *fixtures* e o gate de cobertura. É a fundação exigível desde a
Fase 0 e reutilizada por AOS-110 … AOS-118.

Liga-se a `specs/01_Engineering_Standards_e_Handoff.md` §3 (Definition of Done:
testes verdes + cobertura que **não regride**) e §4 (Gate 3 = Unit; cobertura
>= limiar).

## 1. Comando único e cobertura máquina-legível (AC1)

```sh
make cover        # corre a suite unit (gate 3, -race por módulo) e emite LCOV
make test-unit    # alias de `make cover`
COVERAGE_MIN=90 make cover   # tune do limiar por env var
```

- Corre `go test ./... -race -covermode=atomic -coverprofile` em **cada** módulo
  (via `discover_modules`), reporta a percentagem de todos e emite um relatório
  **máquina-legível** em `coverage/lcov.info` (formato **LCOV**).
- O LCOV é produzido pelo conversor `packages/testkit/cmd/cov2lcov` — **Go stdlib
  puro, ZERO dependências externas, determinista** (lê os coverprofiles Go e
  agrega por linha o *count* máximo). Corre offline.
- `make cover` é **distinto** de `make ci` (pipeline completo com todos os gates).

## 2. Estrutura: `unit/` vs `integração/`

O AOS usa o *test layout* idiomático do Go (testes ao lado do código, sufixo
`_test.go`) com a distinção unit/integração feita por **convenção explícita**:

| Dimensão | Unit | Integração |
|---|---|---|
| Escopo | um pacote/tipo isolado | vários módulos compostos pela costura real |
| Dependências | *fakes*/*stubs* do `testkit`; sem I/O, rede ou relógio de parede | Event Store real in-memory, cadeia de mediação real, ambiente efémero (AOS-110) |
| Localização | `foo_test.go` no mesmo pacote, ou `package foo_test` (teste de caixa-preta) | `*_integration_test.go` no mesmo módulo, ou um **módulo agregador top-level** (o molde é `packages/security-tests`, com `replace` dos módulos do grafo) |
| Determinismo | tempo/rand/IO **isolados** (obrigatório; sem *flakiness*) | mesmo princípio; o ambiente é efémero e reposto por *teardown* idempotente |
| Gate | Gate 3 (`test.sh`) | Gate 3 + gates de domínio (memory/routing/supplychain/security) |

Regras:

- **Nome do teste**: `Test<Unidade>_<Comportamento>` (ex.: `TestMediate_PermitDespachaERegista`). Subtestes com `t.Run("cenário", …)`.
- **Teste de integração cross-package**: sufixo `_integration_test.go` e, quando
  compõe vários módulos, vive num **módulo só-de-testes** que re-declara os
  `replace` do grafo (os `replace` **não** são transitivos). Ver
  `packages/security-tests/go.mod` como precedente.
- **Paralelismo**: `t.Parallel()` sempre que o teste não partilhar estado mutável
  global — as *fixtures* do `testkit` são seguras para concorrência (`-race`).

## 3. Localização e convenção das *fixtures*

- As *fixtures* e *mocks* **partilhados** vivem no módulo **`packages/testkit`**
  (importado *opt-in*: só quem os reutiliza adiciona o `replace`). Ver
  `packages/testkit/README.md`.
- *Fixtures* **específicas de um pacote** (corpus, *golden files*) vivem em
  `testdata/` do próprio pacote e são embebidas com `//go:embed` (precedente:
  `packages/security-tests/testdata/corpus.json`).
- **run_id/step_id**: derivados das *fixtures* do `testkit` (`FixtureRunID`,
  `FixtureStepID`, `FixtureKey`) — que **compõem** `durable.IdempotencyKey` /
  `durable.StepSequencer`, a **mesma** derivação pura da produção. Nunca
  *hardcode* `"run-1"`/`"step-1"` num teste novo.
- **Event Store in-memory**: `testkit.MustEventStore(t)` devolve o `substrate/eventstore`
  **real** (append-only, dedup, quórum), com *teardown* registado.
- **Tempo/aleatoriedade**: `testkit.FixedClock()` / `testkit.NewManualClock()` e
  `testkit.NewSeqIDGen()` — injectados via as opções `WithClock`/`WithIDGen` dos
  módulos. Nunca `time.Now()` nem `rand` num caminho asserido.

## 4. Mocks de referência dos componentes canónicos (AC3)

Alinhados ao catálogo do `_BRIEF` §2. Ver `packages/testkit/README.md` para a
tabela contrato ⇄ mock e exemplos.

| Componente | Mock do `testkit` | Contrato |
|---|---|---|
| RM (Reference Monitor) | `FakeEventSink`, `SpyHook`/`AllowHook`/`DenyHook`/`EscalateHook`, `ToolSpy`, `BaseCall`, `NewMonitor` | importa o RM real (`Hook`/`EventSink`/`Call`/`Decision`) |
| PDP (Policy Decision Point) | `FakePDP` + `PolicyDecisionPoint` | **interface alinhada** (evita o motor Cedar externo) |
| ES (Event Store) | `NewEventStore`/`MustEventStore` | importa o ES real (folha, zero-dep) |
| GW (Model Gateway) | `FakeGateway` + `Gateway` | **interface alinhada** (evita a cadeia de 12 `replace`) |
| BRK (Credential Broker) | `FakeBroker` + `CredentialBroker` | **interface alinhada** (Issue devolve handle opaco, nunca o segredo) |

## 5. Gate de cobertura fail-closed com limiar configurável (AC4)

- O piso do kernel (AOS-010) foi **generalizado** para um limiar **configurável**
  (`COVERAGE_MIN`, default `80`) aplicado aos `COVERAGE_GATED_MODULES` (kernel +
  testkit) no **Gate 3** (`scripts/ci/test.sh`).
- Uma descida abaixo do limiar num módulo *gated* — ou uma cobertura
  não-mensurável (`FALHOU`/`n/a`) — faz o gate sair `!= 0` e **bloqueia o merge**
  (fail-closed). O predicado é `coverage_meets_min` em `scripts/ci/lib.sh`.
- **Teste do próprio gate**: `scripts/ci/selftest.sh` (caso I) prova que
  `coverage_meets_min` rejeita `50% < 99%` e uma cobertura não-mensurável, e
  aceita `95.3% >= 80%` — de forma determinista e offline. Corre com
  `make ci-selftest`.
- Os gates de domínio mantêm os seus próprios pisos (`MEMORY_/ROUTING_/REGISTRY_COVERAGE_MIN`),
  inalterados por esta generalização.

## 6. Sem *flakiness*

A suite canário do `testkit` corre `-race -count=2` estável: tempo
([FixedClock]/[ManualClock]), aleatoriedade ([SeqIDGen] sequencial) e I/O
(Event Store in-memory) estão isolados. Um teste novo que introduza *flakiness*
(relógio de parede, `rand`, rede, ordenação de `map` asserida) viola estas
convenções.
