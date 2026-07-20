# `testkit` — fixtures e mocks de referência do AOS (AOS-109)

Módulo **opt-in** que consolida as *fixtures* deterministas e os *mocks/stubs* de
referência dos cinco componentes canónicos — **RM, PDP, ES, GW, BRK** — alinhados
ao catálogo do `_BRIEF` §2. Escrever um teste de domínio passa a ser **compor**
*fixtures*, em vez de reinventar *fakes* dispersos.

Convenções gerais (estrutura unit/integração, nomes, gate de cobertura):
[`docs/testing/README.md`](../../docs/testing/README.md).

## Adoptar (opt-in)

Nenhum dos 23 módulos de produção depende do `testkit`. Para o reutilizar, o
**teu** módulo de teste adiciona o `require` + `replace` path-local:

```go
// go.mod do teu módulo
require github.com/aos-ref/testkit v0.0.0
replace github.com/aos-ref/testkit => ../../testkit
```

```go
import tk "github.com/aos-ref/testkit"
```

O `testkit` mantém o `go.mod` **leve** e **zero-dep externo**: importa apenas os
contratos folha reais (`substrate/eventstore`, `kernel/reference-monitor`,
`kernel/agent-runtime`). Para **PDP/GW/BRK** define **interfaces alinhadas** ao
contrato + *fakes* deterministas — evitando o motor Cedar (externo) e a cadeia de
`replace` do Model Gateway. São *mocks alinhados ao contrato*, não os componentes
reais.

## Catálogo

| Componente | Fixtures / mocks |
|---|---|
| **Determinismo** | `FixedClock()`, `NewManualClock()`, `CanonicalInstant`; `NewSeqIDGen()` |
| **run_id/step_id** | `FixtureRunID`, `FixtureStepID(turn)`, `FixtureKey(turn)`, `IdempotencyKey`, `NewStepSequencer` (compõem `durable.*`) |
| **ES** | `NewEventStore()`, `MustEventStore(t)` (o `substrate/eventstore` **real** in-memory) |
| **RM** | `FakeEventSink`, `SpyHook` + `AllowHook`/`DenyHook`/`EscalateHook`, `HookRecorder`, `ToolSpy`, `BaseCall()`, `NewMonitor()` |
| **PDP** | `FakePDP` + interface `PolicyDecisionPoint` (`Decide`), tipos `PolicyInput`/`PolicyDecision`/`PolicyEffect` |
| **GW** | `FakeGateway` + interface `Gateway` (`Chat`/`ChatStream`/`Embeddings`) |
| **BRK** | `FakeBroker` + interface `CredentialBroker` (`Issue`/`Revoke`) |

## Como escrever um teste de domínio

Os exemplos completos e executáveis estão em
[`domain_example_test.go`](./domain_example_test.go) (parte da suite canário).

### Idempotência

O mesmo passo lógico (mesma `run_id`/`step_id` ⇒ mesma *idempotency key*) escrito
duas vezes **deduplica**. As *fixtures* fornecem a chave canónica e o ES real:

```go
es := tk.MustEventStore(t)
in := eventstore.EventInput{Type: "turn.recorded", RunID: tk.FixtureRunID, StepID: tk.FixtureStepID(1)}
r1, _ := es.Append(ctx, tk.FixtureRunID, in)
r2, _ := es.Append(ctx, tk.FixtureRunID, in) // retry do MESMO passo
// r2.Status == StatusDuplicate && r2.Seq == r1.Seq
```

### Replay

O sequenciador de step_ids é **puro** — reexecutar a sequência produz as **mesmas**
chaves, a base do replay determinista:

```go
k1, _ := tk.FixtureKey(1) // "run-testkit-0001:step-000001"
k1b, _ := tk.FixtureKey(1)
// k1 == k1b, sempre (execução, retry, replay)
```

### Política

Compõe o **RM real** com um `DenyHook` (o duplo do PDP a negar) e prova que a
decisão **bloqueia o efeito** — sem o motor Cedar. Trocar por `AllowHook` exercita
o caminho *permit*; o `FakePDP` serve o mesmo papel num teste do PDP isolado:

```go
m, sink := tk.NewMonitor(
    tk.AllowHook("identity"),
    tk.DenyHook("policy", "capability fora da allowlist"),
    tk.AllowHook("audit"),
)
tool := tk.NewToolSpy([]byte("efeito"), nil)
_ = m.Register("tool.echo", tool.Func())
d, _ := m.Mediate(ctx, tk.BaseCall())
// d.Effect == EffectDeny && !tool.Called() && sink.Count() == 1
```

## Determinismo (obrigatório)

Isola as três fontes de *flakiness*:

- **Tempo** — injecta `tk.FixedClock()` (parado) ou `tk.NewManualClock()` (avanço
  explícito) via as opções `WithClock` dos módulos. Nunca `time.Now()`.
- **Aleatoriedade** — `tk.NewSeqIDGen("h")` produz `h-1`, `h-2`, … deterministas.
  Nunca UUID/`rand` num caminho asserido.
- **I/O** — `tk.MustEventStore(t)` é in-memory. Sem rede, sem disco.

A suite canário corre `-race -count=2` estável. Cobre cada tipo de *fixture*
(clock, ids, ES, RM, PDP, GW, BRK) e o conversor de cobertura.
