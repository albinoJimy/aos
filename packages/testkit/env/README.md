# `testkit/env` — ambiente efémero de teste (AOS-110)

Harness que **provisiona dependências efémeras por execução** — Event Store,
transporte push (`Bus`), PDP e `FakeVault` — com um **lifecycle determinista**
(_Provision → Seed → uso → Teardown_) e **teardown garantido mesmo em falha**.
É a fundação reutilizável pelas suites de integração de domínio **AOS-111..118**.

Compõe (não reimplementa) os _mocks_/_fixtures_ de **AOS-109** (o pacote
[`testkit`](../) pai): `testkit.NewEventStore` (o `*eventstore.Store` **real**),
`testkit.NewFakePDP`, `testkit.NewFakeBroker` e as _fixtures_ de `run_id`/`step_id`.

## Uso

```go
import "github.com/aos-ref/testkit/env"

func TestReplay(t *testing.T) {
    // 1. DECLARA as deps efémeras (AC1) — provisionadas + destruídas
    //    automaticamente via t.Cleanup.
    e := env.New(t, env.WithEventStore(), env.WithBus(), env.WithPDP(), env.WithVault())

    // 2. SEED de uma trajectória CONHECIDA (AC5) para replay/idempotência.
    steps := e.SeedTrajectory("run-42", 3) // 3 turnos × {turn.recorded, replay.captured}

    // 3. Usa e.EventStore / e.Bus / e.PDP / e.Vault ... (estado LIMPO, AC2)

    // Teardown corre no fim — inclusive se o teste falhar (AC3). Sem defer manual.
}
```

`env.New(t)` **sem opções** provisiona o conjunto _core_ (ES + Bus + PDP +
Vault). Cada dependência é **opt-in** por `With…`; `WithBus()` implica o Event
Store (o bus é servido pelas subscrições do `Store`). `WithBroker()` adiciona um
`testkit.FakeBroker`.

## Critérios de aceitação → onde são provados

| AC | O quê | Prova |
|----|-------|-------|
| AC1 | Deps declaradas e provisionadas/destruídas automaticamente | `env_test.go` (`TestNew_*`) |
| AC2 | Estado limpo por execução; sem contaminação | `isolation_test.go` (mesma suite **duas vezes** → resultado idêntico) |
| AC3 | Teardown garantido em falha; sem órfãos; idempotente | `teardown_test.go` (falha a meio + contagem de goroutines com `-race`) |
| AC4 | Corre local == CI, sem config manual | in-process Go (nada a configurar) |
| AC5 | Seed de trajectória conhecida | `seed_test.go` (`SeedTrajectory`, replay deduplica) |

Verificação local (a mesma do CI):

```sh
CGO_ENABLED=1 go test -race -count=2 -covermode=atomic ./env/
```

O `-race` é **crítico**: prova ausência de _data race_ e de _goroutine leak_ no
transporte push e no teardown. `-count=2` reforça o isolamento (a suite corre
duas vezes de raiz).

---

## Variante de PRODUÇÃO (Testcontainers, imagens pinadas por hash)

A implementação de referência é **in-process, offline, determinista** — coerente
com todo o repo, onde os cinco componentes canónicos **são** modelos in-process
zero-dep. É este o _"equivalente"_ a Testcontainers para o AOS.

Para **testes de aceitação end-to-end contra os componentes reais**, a mesma API
de fixture (`env.New` + `With…` + `SeedTrajectory`) fica atrás de um provisionador
que arranca contentores **efémeros** com **imagens pinadas por digest `sha256`**
(nunca `:latest`), coerente com a supply-chain do `_BRIEF §2` (reprodutibilidade,
sem _drift_ de imagem). Mapeamento de referência:

| Dep efémera | Impl. de referência (este pacote) | Variante de produção (pinada por hash) |
|-------------|-----------------------------------|-----------------------------------------|
| Event Store | `eventstore.Store` in-memory replicado | Event store durável real (imagem `@sha256:…`), cluster replicado |
| Transporte push (`Bus`) | subscrições `Store.Subscribe` | NATS JetStream `nats:2.10.x@sha256:…` |
| PDP | `testkit.FakePDP` (alinhado ao contrato C1) | PDP Cedar real atrás da porta `pdp.PolicyCheck` |
| Vault | `env.FakeVault` (segredo encapsulado) | HashiCorp Vault / KMS `vault:1.x@sha256:…` |
| Relógios | `testkit.FixedClock` / `ManualClock` | relógio real (os testes e2e toleram tempo de parede) |

Requisitos da variante de produção (DoD AOS-110):

- **Imagens pinadas por versão + `@sha256:<digest>`** — documentadas e verificadas
  no arranque; nunca tags móveis.
- **Sem segredos** no código ou nas imagens — o vault é semeado em runtime por
  material efémero de teste.
- **Teardown garantido** — `testcontainers.CleanupContainer` / `t.Cleanup`
  destrói cada contentor no fim (mesmo em falha), espelhando o contrato deste
  harness.
- **Isolamento** — um _namespace_/rede por execução; nenhuma execução observa
  efeitos de outra.

> A troca entre as duas variantes é a **mesma superfície de fixture** — um teste
> de domínio escrito contra `env.New(...)` não muda; só o provisionador por trás
> é que difere (in-process vs. contentores pinados). A referência **não** introduz
> Docker/Testcontainers nem qualquer dep externa: mantém o `go.mod` leve e o
> _build_ offline.
