# `engine` — Porta de execução durável agnóstica ao backend (AOS-022)

Subpacote de `agent-runtime` que entrega a **fase feature** de **AOS-022** sob o
**ADR-015 ratificado**: consolidar o **contrato próprio** de durable execution e
expô-lo por uma **porta estável** que mantém o Agent Runtime **agnóstico ao backend**
(Princípio 8 / anti lock-in).

> **Decisão (ADR-015).** AOS-022 não decide *se* há durable execution — o ADR-001
> fixa-a como primitivo — mas *que substrato* a implementa. O spike concluiu, e o
> ADR-015 ratificou, **consolidar o contrato próprio** (AOS-014/015/016/021). Um
> engine externo (Temporal/Restate/DBOS) fica como **backend plugável** opcional,
> subordinado ao Event Store como fonte de verdade (ADR-007).

## O que este pacote contém

| Símbolo | Papel |
|---|---|
| `Engine` (interface) | A **porta**: `Dispatch` / `Checkpoint` / `Resume` / `Replay` / `Mode`, agnóstica ao backend. As assinaturas seguem as APIs de AOS-014/015/016/021. |
| `OwnContractEngine` | **Adaptador de referência**: satisfaz `Engine` **compondo** as peças já Done — não reimplementa nada. |
| `NewOwnContractEngine(store, rm, opts…)` | Cabla o adaptador sobre o Event Store replicado e o Reference Monitor. |

### Composição (o adaptador não reimplementa nada)

```
Engine.Dispatch    → *activity.Dispatcher            (AOS-021: idempotência AOS-014 +
                                                      mediação RM AOS-003 + taint +
                                                      registo p/ replay AOS-016)
Engine.Checkpoint  → *durable.EventStoreCheckpointer (AOS-015: cursor intra-iteração)
Engine.Resume      → *durable.Resumer                (AOS-015: retoma resume-from-step)
Engine.Replay      → *replay.ReplayEngine            (AOS-016: replay determinístico)
```

Todas as peças assentam no **mesmo Event Store replicado** (ADR-007, fonte de verdade
única). É precisamente isto que distingue o contrato próprio dos engines externos,
que trariam um **segundo** log de durabilidade e a consequente reconciliação de duas
fontes de verdade (ver ADR-015 §2).

## Porquê um subpacote (e não `durable`)

A porta refere tipos de `activity` (AOS-021) e `replay` (AOS-016), e ambos importam
`durable`. Colocar a porta em `durable` criaria um **ciclo de importação**. O
subpacote `engine` importa `durable` + `activity` + `replay` (nenhum importa
`engine`) — acíclico por construção.

## Uso

```go
store, _ := eventstore.New()
rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
// … rm.Register("tool", …)

eng, err := engine.NewOwnContractEngine(store, rm,
    engine.WithProducer(eventstore.Producer{NHIID: "nhi:agent-1"}),
)

// O RT usa APENAS a interface Engine — nunca vê o backend:
res, err := eng.Dispatch(ctx, act)            // idempotente + mediado + registado
_ = eng.Checkpoint(ctx, cp)                    // cursor intra-iteração
rp, _ := eng.Resume(ctx, runID)                // retoma resume-from-step
rr, _ := eng.Replay(ctx, runID, replay.Options{Spec: spec}) // replay determinístico
```

**Crash / failover.** Um worker novo reconstrói o ledger do log e injecta-o:

```go
lb, _ := durable.NewStepLedger(store); _ = lb.Rebuild(ctx, runID)
engB, _ := engine.NewOwnContractEngine(store, rm, engine.WithLedger(lb))
```

## Como um backend externo implementaria a MESMA porta (mapeamento; não implementado)

| Operação | Temporal | Restate | DBOS |
|---|---|---|---|
| `Dispatch` | Activity idempotente (`activity-id = run_id:step_id`) | Handler com idempotency key (journal + dedup) | `@step` transaccional (Postgres) |
| `Checkpoint` | Event history (implícito) | Journal da invocation | Estado do workflow |
| `Resume` | Replay do workflow | Recuperação do journal | Recovery do workflow |
| `Replay` | Replayer do SDK | Re-execução do journal | Time-travel do estado |

Em todos os casos o RT continuaria a chamar só os métodos de `Engine`; o adaptador
externo subordinaria o seu log ao Event Store (ADR-007).

## Prova de contrato (Princípio 8)

`engine_contract_test.go` corre o **cenário de referência** (run multi-passo com
crash e retoma) sobre a porta e prova:

- **Idempotência (AOS-014):** re-despacho após crash **com worker novo** (ledger
  reconstruído do log) → **0 efeitos duplicados**.
- **Replay (AOS-016):** `Replay` reconstrói com **fidelidade 100 %** e **zero efeitos
  externos**; mutar o `Spec` (evolução de código) → **divergência localizada**.
- **Isolamento:** o **mesmo** driver de RT (escrito só contra `Engine`) corre com
  asserções idênticas sobre o `OwnContractEngine` **e** sobre um stub/fake — trocar o
  backend **não altera** a API nem o uso do RT.

## Estado

- Go 1.24, **zero dependências externas**, sem novo `go.mod`.
- `go vet` limpo · `go test -race` verde · cobertura do adaptador **≥ 80 %**.
- **Aditivo:** não altera AOS-013..021 (nenhuma mudança de API a montante).

## Fronteiras (abertas; herdadas do ADR-015)

- Enforcement de fencing **por-escrita** exige o ES fencing-aware (AOS-018, aberto).
- Adopção do `Dispatcher`/`Engine` **pelo loop** é wiring diferido (como em AOS-021).
- HA de produção depende do ES replicado real (NATS/JetStream), validado em staging.
