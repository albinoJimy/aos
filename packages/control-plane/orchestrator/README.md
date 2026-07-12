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

## Grafo de tarefas acíclico + deadlock (AOS-025)

O pacote raiz acrescenta o **DAG de tarefas** e a **detecção de deadlock**, sobre
o Event Store (ADR-007) e a máquina de estados durável de AOS-017.

### DAG e aciclicidade fail-closed (`graph.go`)

- `DAG` é o grafo **puro** (sem I/O). Uma aresta `From→To` exprime que `To`
  **depende de** `From` (`From` precede `To`).
- **Aciclicidade INCREMENTAL na admissão**: `DAG.AddEdge` recusa
  (`ErrEdgeClosesCycle`) qualquer aresta cujo destino já alcance a origem — o
  fecho transitivo não pode conter o nó de origem. **Fail-closed**: a aresta NÃO
  é adicionada, nunca "aceita e corrige depois".
- **Ordenação topológica reprodutível**: `DAG.TopoOrder` é Kahn com desempate por
  `task_id` **lexicográfico** (nunca a ordem de um mapa Go). Para os mesmos
  nós/arestas o plano é **idêntico**, independentemente da ordem de admissão.
- `GraphBuilder` persiste cada admissão como evento append-only no stream=`run_id`:
  `task.node.created`, `task.edge.added` e — nas rejeições — `task.edge.rejected_cycle`
  (com razão explícita). A idempotência é por passo (`step_id` = `node:<id>` /
  `edge:<from>><to>`), pelo que reemitir é deduplicado (ADR-001).
- **Replay determinístico**: `RebuildDAG` reconstrói o DAG relendo os eventos do
  Event Store; a `TopoOrder` reconstruída é **idêntica** à original (ADR-010).
- Cada nó carrega uma `AgentIdentity` (NHI + cadeia de delegação on-behalf-of,
  ADR-003) e uma `Priority`.

### Detecção e resolução de deadlock (`deadlock.go`)

- `WaitForGraph` é o **grafo de espera**: uma aresta `Waiter→Holder` significa que
  `Waiter` está bloqueado num recurso detido por `Holder`. `FindCycle` procura
  espera circular (DFS a três cores) e devolve o **conjunto de tarefas** do ciclo,
  ordenado (determinístico).
- `ResourceLedger` regista quem detém/espera cada recurso (leases, filas,
  orçamento); o wait-for graph é derivado dele.
- `DeadlockDetector.DetectAndResolve` emite `deadlock.detected` (com o conjunto de
  tarefas), aplica a **política determinística** `abort_lowest_priority_victim` e
  emite `deadlock.resolved`. Critério de escolha da vítima, por ordem de desempate
  **estável**: (1) **menor prioridade**; (2) empate → **mais recente** (maior ordem
  de admissão); (3) empate → `task_id` lexicograficamente maior (defensivo — a
  ordem de admissão já é única).
- **Sem efeitos duplicados** (ADR-001): a libertação dos recursos da vítima e a
  transição do nó (`running→failed`, validada pela tabela de AOS-017) só são
  aplicadas se o `deadlock.resolved` for **committed**. Num duplicado (replay do
  mesmo deadlock) o Event Store deduplica e os efeitos **não** são reaplicados.

### Integração com a máquina de estados (AOS-017)

As transições por-nó (`ready→running`, `running→failed`) são validadas pela tabela
declarativa de `kernel/agent-runtime/state` (`state.IsValidTransition`) — a mesma
autoridade da máquina durável do run. O Orquestrador **não** detém o fencing token
do claim (isso é do Escalonador/AOS-018): ao nível do grafo regista-se a transição
de estado, não o enforcement do lease.

## Delegação a sub-agentes com orçamento herdado (AOS-026)

`delegation.go` acrescenta o **spawn de sub-agentes** com **orçamento hierárquico
herdado**. O `Delegator` **compõe** três fundações (não reimplementa nenhuma):

- **Orçamento CAS** (`control-plane/budget`, AOS-008) — a **reserva atómica** por
  compare-and-swap sobre a árvore de orçamento. É o budget (nunca um contador
  partilhado com corrida) que impõe *soma-das-fatias-dos-filhos ≤ orçamento do pai*
  (**0 overshoot**) e *sub-orçamento herdado ≤ remanescente*, em **tokens e USD**
  (nunca iterações).
- **Identidade NHI filha** (`platform/identity`, AOS-005/006) — cada sub-agente é
  uma identidade **não-humana única**, emitida on-behalf-of o pai por
  `Issuer.IssueChild`, estendendo a cadeia de delegação hash-linked (autoridade =
  pedido ∩ classe, ⊆ pai; raiz humana preservada, ADR-003).
- **Reference Monitor** (`kernel/reference-monitor`, AOS-003) — a criação da
  identidade filha e o débito são **mediados**: só ocorrem sob um Permit
  não-forjável de `Monitor.Mediate` (ADR-002). Uma negação **liberta** a reserva
  (sem leak) e recusa o spawn fail-closed.

### Modelo de contabilidade (sem dupla contagem)

A árvore de orçamento **espelha** a árvore de delegação: cada agente tem um nó cujo
**limite** é o orçamento do seu subárvore. No spawn reserva-se a **fatia de consumo
próprio** do filho **no nó do filho**; como `budget.Reserve` debita em **cascata**
por toda a linhagem (filho→pai→raiz), a reserva é atomicamente validada contra o
limite do pai (e da raiz) — é isto a *reserva CAS antes do spawn*. A fatia própria
de cada agente é contada **uma vez** e sobe a cascata uma vez: a soma na raiz é o
consumo real total, sem dupla contagem. O limite do nó do filho (≥ fatia própria)
deixa headroom para o filho **delegar recursivamente** (map-reduce).

### Ciclo de vida e idempotência (ADR-001)

`Spawn` → reserva CAS + mediação RM + emissão da NHI filha (+ admissão opcional no
DAG). `Finish(success)` consolida o consumo real (`budget.Commit`); `Finish(fail)`
liberta a fatia (`budget.Release`). Ambos são **idempotentes** por `reservation.ID`
(um retry é no-op nos contadores) e os eventos deduplicam por `(run_id, step_id)` —
**0 efeitos duplicados** no retry. Um `Commit` após `Release` (ou vice-versa) é
rejeitado: a reserva consome-se exactamente uma vez.

### Eventos append-only

`subagent.budget_reserved`, `subagent.spawned`, `subagent.budget_consumed`,
`subagent.budget_released`, `subagent.spawn_denied_no_budget` (fail-closed sem
orçamento) e `subagent.spawn_denied` (profundidade/fan-out/mediação). Permitem
reconstruir a **árvore de delegação** e correlacionar com o **burn-down** — cuja
contabilidade autoritativa e durável é a do próprio budget (`budget.Rebuild` sobre
`budget.reserved/committed/released`). Profundidade e fan-out são **configuráveis**
(`WithMaxDepth`/`WithMaxFanOut`) e adicionalmente limitados pelo orçamento.

```go
del, _ := orchestrator.NewDelegator(bud /* *budget.Budget */, mon /* *rm.Monitor */, iss /* *identity.Issuer */,
    orchestrator.WithMaxDepth(8), orchestrator.WithMaxFanOut(16),
    orchestrator.WithDelegationStore(es, producer), orchestrator.WithDelegationGraph(gb))
h, err := del.Spawn(ctx, orchestrator.SpawnRequest{
    RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nA",
    InheritedBudget: budget.Amount{Tokens: 400, CostMicroUSD: 400_000},
    SpawnReserve:    budget.Amount{Tokens: 50, CostMicroUSD: 50_000},
    Depth: 1, ParentToken: parentTok.Compact, Child: childReq,
})
// ... sub-agente executa ...
_ = del.Finish(ctx, h, true) // consolida o consumo real (idempotente)
```

O token-bucket global TPM/RPM (AOS-027/028), o circuit breaker (AOS-029) e o
scheduling (AOS-032) assentam **sobre** esta delegação, **fora** do âmbito de AOS-026.

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

- **Decomposição real**: `Submit` continua a construir o grafo mínimo (1 nó); a
  decomposição de linguagem natural em DAG multi-nó liga-se ao `GraphBuilder`.
- Delegação a sub-agentes com orçamento herdado (AOS-026) — **implementada** em
  `delegation.go` (ver secção acima), reutiliza a `AgentIdentity` dos nós; o
  `admission control` global (AOS-027) e o scheduling priority-aware com aging
  (AOS-032) consomem a `TopoOrder` — **fora** do escopo de AOS-025/026.
- O fencing token do claim `ready→running` e a expiração de lease/heartbeat são do
  Escalonador/AOS-018 (o Orquestrador só regista a transição de estado por-nó).

## Dependências e build

Zero dependências externas. Integrados por `replace` local: Barramento (AOS-009),
Event Store (AOS-002), a **máquina de estados durável** (AOS-017, subpacote `state`
do Agent Runtime) e — para a delegação de AOS-026 — o **orçamento CAS** (AOS-008,
`control-plane/budget`), a **identidade NHI** (AOS-005/006, `platform/identity`) e o
**Reference Monitor** (AOS-003, `kernel/reference-monitor`). **Não** altera nenhum
módulo existente; consome-os só pelas APIs públicas.

```
go mod tidy && go vet ./... && go test ./... -race -count=1
```
