# control-plane/orchestrator — Orquestrador (ORQ) + contrato do plano de controlo

Esqueleto contratual do **Orquestrador** do AOS (ticket **AOS-012**). O ORQ é o
produtor de referência do plano de controlo: recebe um objectivo, constrói um
grafo de tarefas acíclico e publica os eventos de coordenação no barramento
(AOS-009). O Escalonador (módulo `control-plane/scheduler`) consome-os.

Este módulo exporta **dois** artefactos:

1. `contract/` — o **contrato partilhado** do plano de controlo (zero dependências
   fora da stdlib): tipos de evento canónicos, identificadores de correlação
   (`run_id`/`task_id`/`step_id`), a máquina de estados mínima e as portas
   estáveis `Orchestrator`/`Scheduler`. É importado tanto pelo ORQ como pelo SCH.
2. o pacote raiz — a implementação `Orchestrator` com `Submit`.

## Contrato

### Máquina de estados mínima (`contract/state.go`)

```
ready ──▶ running ──▶ complete
                └────▶ failed
```

Transições válidas são impostas por `contract.Transition`; uma transição inválida
devolve `contract.ErrInvalidTransition` e **não** altera o estado. É o subconjunto
mínimo da máquina durável completa de `tecnica/02 §5`.

### Eventos canónicos (`contract/events.go`)

`run.created`, `task.ready`, `task.running`, `task.complete`, `task.failed` —
todos correlacionados por `run_id` (o `stream_id` no Event Store) e, quando
aplicável, `task_id`/`step_id`. Cada fase usa um `step_id` distinto (`contract.Step*`)
para que a `idempotency_key = run_id:step_id` seja única no stream e nenhum evento
seja deduplicado/perdido pelo Event Store — incluindo o evento de mediação que o
RM grava no mesmo stream.

### Portas estáveis (`contract/ports.go`)

`Orchestrator.Submit(ctx, goal) (RunID, error)` e `Scheduler.Start/Stop`. São o
contrato que a **EPIC-03** estende **sem quebrar**.

## Orquestrador

`Orchestrator.Submit`:
1. gera um `run_id` único;
2. constrói o grafo **mínimo** (1 nó) a partir do `Goal`;
3. publica `run.created` e depois `task.ready` no stream `run_id`;
4. devolve o `run_id`.

```go
es, _ := eventstore.New()
b, _ := bus.New(es)
orch, _ := orchestrator.New(b)
runID, _ := orch.Submit(ctx, contract.Goal{
    Objective: "eco",
    Task: contract.TaskSpec{ToolID: "tool:echo", Capability: "cap:echo", Input: []byte("olá")},
})
```

## O que é STUB / NÃO-PRODUTIVO (AOS-012)

Marcado como stub no código; **fica para EPIC-03**:

- **Decomposição real**: o grafo é sempre **1 nó** derivado directamente do `Goal`
  (`contract.NewMinimalGraph`) — sem planeamento, sem multi-nó, sem arestas/DAG.
- **`run_id`**: `defaultRunID` só é único **dentro de uma instância** do ORQ
  (contador monotónico); produção usa ULID/UUID global.
- **Sem re-planeamento, prioridade, ou dependências entre tarefas.**
- **Sem compensação de fan-out parcial**: `run.created` é publicado antes dos
  `task.ready`. Se um `task.ready` falhar depois de `run.created` ser durável,
  `Submit` devolve o erro mas o `run.created` FICA no stream (run sem tarefa
  observável), sem rollback. Benigno no grafo de 1 nó; EPIC-03 (multi-nó) deve
  emitir um `run.failed` terminal ou compensar os `task.ready` já publicados.

## Pontos de extensão (EPIC-03)

- `contract/state.go` → acrescentar `waiting_on_tool`, `waiting_on_human`,
  `paused`, `compensating`, `timed_out`, `killed` e as respectivas arestas.
- `contract/graph.go` → arestas do DAG real e decomposição de `Goal` multi-nó.
- `contract/events.go` → novos tipos de evento (sem renomear os existentes).

## Dependências e build

Zero dependências externas. Barramento (AOS-009) e Event Store (AOS-002)
integrados por `replace` local. **Não** altera nenhum módulo existente.

```
go mod tidy && go vet ./... && go test ./... -race -count=1
```
