# Event Store — `eventstore` (AOS-002)

Event Store **append-only replicado** com esquema de eventos versionado e
**transporte push**. É a **fonte de verdade única e ordenada por stream** do AOS
(ADR-007): turnos, tool calls, resultados, decisões de política e audit derivam
deste log. Sem este substrato durável não há M1 (Recuperável) nem replay
determinístico.

> Módulo Go self-contained, **zero dependências externas** (só stdlib). O ULID é
> implementado inline (`crypto/rand` + Crockford base32).

## Âmbito

Este pacote implementa **apenas** o Event Store (componente **ES**). RM, PDP e o
registo de audit são outros tickets; aqui existem no máximo como interfaces ou
ganchos. O modelo de replicação é in-process e de **referência** — determinístico
e testável (ver secção *Replicação*). Em produção o backend é NATS JetStream
(R3/R5, replicação Raft).

## API pública

```go
type EventStore interface {
    Append(ctx, streamID string, in EventInput, opts ...AppendOption) (AppendResult, error)
    Read(ctx, streamID string, fromSeq uint64) ([]Event, error) // committed, seq asc, fromSeq inclusivo
    Subscribe(ctx, filter Filter, h Handler) (Subscription, error) // push
    Close() error
}
```

A superfície é deliberadamente **append-only estrito**: não há `Update`,
`Delete`, `Set` nem `Truncate`. Correcções são **novos eventos**.

### Exemplo

```go
s, _ := eventstore.New(
    eventstore.WithReplicas(3),
    eventstore.WithQuorum(2),
    // Soberania regional (ADR-011), opcional e fail-closed. Se declarada, todas as
    // réplicas TÊM de estar na região do board (senão New devolve ErrSovereigntyViolation):
    //   eventstore.WithRegion("eu"),
    //   eventstore.WithReplicaRegions("eu", "eu", "eu"),
)
defer s.Close()

res, err := s.Append(ctx, "run-91c2", eventstore.EventInput{
    Type:   "tool.result.received",
    RunID:  "run-91c2",
    StepID: "step-014",
    Payload: json.RawMessage(`{"ok":true}`),
    Producer: eventstore.Producer{NHIID: "nhi:agent:planner@v2.3.1"},
})
// res.Seq == 1, res.Status == Committed

evs, _ := s.Read(ctx, "run-91c2", 1) // cópias, ordenadas por seq

sub, _ := s.Subscribe(ctx, eventstore.Filter{Streams: []string{"run-91c2"}}, func(ev eventstore.Event) {
    // entregue por push, em ordem de seq
})
defer sub.Unsubscribe()
```

## Envelope de evento

O envelope canónico (ver `tecnica/13 §3` e `schemas/event-envelope-1.0.json`):

| Campo | Papel |
|---|---|
| `event_id` | ULID globalmente único. Identificador, **nunca** fonte de ordem. |
| `stream_id` | Stream (ex.: um run). A ordem total é por `(stream_id, seq)`. |
| `seq` | Monotónico, **gapless**, por stream, começa em 1. Atribuído pelo store. |
| `type` | Nome canónico do facto (ex.: `tool.call.dispatched`). |
| `ts` | RFC3339. **Observacional**, nunca fonte de ordenação. |
| `producer` | NHI emissora + cadeia de delegação on-behalf-of + scope. |
| `payload` | Conteúdo (inline neste reference impl). |
| `schema_version` | Versão do schema no registo (`1.0`). |
| `run_id`, `step_id`, `parent_step_id` | Correlação e causalidade. |
| `idempotency_key` | `f(run_id, step_id) = run_id + ":" + step_id`. Calculada pelo store. |

O `schema_version` está registado em `schemas/event-envelope-1.0.json`. A
evolução é compatível (expand/contract): novos `type` são *MINOR*; remover um
campo ou mudar a semântica de `expected_seq` é *MAJOR* (`tecnica/12 §5`).

> **Fidelidade ao envelope canónico.** Esta struct cumpre os campos **mínimos**
> de AOS-002. Os metadados de determinismo/proveniência do envelope de
> `tecnica/13 §3` e do evento C2 — `prompt_hash`, `model{model_id,params,seed}`,
> `dependency_manifest_ref`, `taint`, `payload_ref{uri,content_hash,encryption}` —
> **não** estão materializados nesta reference impl (o `payload` é inline). São
> extensões previstas, a introduzir por *expand* compatível (novos campos
> opcionais), sem quebrar o schema `1.0`.

## Semântica (contrato C2 — RT ↔ ES)

- **Append-only estrito.** O log nunca é sobrescrito. Uma tentativa de escrever
  numa posição já ocupada devolve `ErrAppendOnlyViolation`. `Read` devolve
  **cópias** — o chamador não pode mutar o estado guardado.
- **Ordem total por `(stream_id, seq)`.** `seq` monotónico e gapless por stream,
  serializado **por stream** (não globalmente — ver *Concorrência*). Escritas
  concorrentes ao mesmo stream produzem seqs únicos e contíguos, sem perda;
  escritas a streams distintos correm em paralelo.
- **Concorrência optimista (`WithExpectedSeq`).** `n` é o último seq committed que
  o chamador afirma ser o corrente; o evento novo ficaria em `n+1`:
  - `n == último` → procede;
  - `n < último`  → a posição pretendida já está ocupada por história committed →
    `ErrAppendOnlyViolation` (escrita no passado);
  - `n > último`  → o chamador está adiantado face à realidade → `ErrSeqConflict`.

  **Retry de CAS concorrente:** quando dois `Append` com o mesmo
  `WithExpectedSeq(n)` correm em paralelo, o vencedor materializa `n+1` e o
  perdedor passa a ver `last = n+1 > n`, recebendo `ErrAppendOnlyViolation` (e não
  `ErrSeqConflict`). Um chamador de concorrência optimista deve tratar **tanto
  `ErrSeqConflict` como `ErrAppendOnlyViolation`** como sinal para reler e
  re-tentar — nenhum é fatal neste contexto. Coberto por
  `TestConcurrentCASLoserError`.
- **Idempotência.** Um segundo `Append` com a mesma `idempotency_key` devolve
  `Status=Duplicate` e o **seq committed original**, sem duplicar. A dedup é **por
  stream** (a `idempotency_key = run_id:step_id` vive no stream do run;
  `stream_id == run_id`), o que a torna serializada pelo stripe do stream — sem
  contentor de dedup partilhado entre streams. Sobrevive a failover (o índice de
  dedup é mantido em cada réplica, reconstrutível a partir do log committed).
- **`Read` de stream inexistente** → `ErrStreamNotFound`.

**Ordem de verificação num `Append`:** idempotência primeiro (o duplicado ganha),
depois `expected_seq`, depois quórum, e só então a escrita.

Todos os erros são sentinelas comparáveis com `errors.Is` e carregam o código
canónico do contrato (`E_APPEND_ONLY_VIOLATION`, `E_SEQ_CONFLICT`,
`E_STREAM_NOT_FOUND`, `E_NO_QUORUM`).

> **Envelope de porta wire (`port_version`, `op`).** Este pacote implementa o
> contrato C2 ao nível **semântico** (Go-native: `Append`/`Read`,
> `StatusCommitted`/`StatusDuplicate`, `AppendResult.Seq`). O **envelope wire** de
> `tecnica/12 §5` (`port_version`, `op`, `committed_seq`, `status`) e o
> versionamento SemVer da porta são responsabilidade do **adaptador RT** (outro
> ticket), não do ES. Se a propriedade da porta C2 for reatribuída ao ES, expor
> aqui uma constante `PortVersion` e um (des)serializador é *expand* compatível.

## Replicação (modelo de referência)

- Réplicas **in-process**, replicação **síncrona em lockstep** ao *in-sync
  replica set* (todas as réplicas vivas têm log idêntico). Quórum = maioria.
- Uma entrada só é **committed** quando `>=` quórum de réplicas vivas a armazena —
  **antes** do ACK ao produtor e **antes** do push. Se `#vivas < quórum` a
  escrita é rejeitada (`ErrNoQuorum`) e **não deixa rasto** (fail-closed).
- **Failover:** a eleição escolhe a réplica viva mais actualizada (maior commit
  index; desempate pelo menor id). Eventos **confirmados nunca se perdem**
  enquanto sobreviver um quórum.
- **Perda de quórum (durabilidade):** o store persiste o **commit index
  confirmado por quórum** mais alto (`committed`). A eleição **recusa** promover
  a líder qualquer réplica cujo commit index seja inferior a esse piso. Se o
  quórum se perde e só sobrevive uma réplica desactualizada, o store fica
  **indisponível** (`Leader() == -1`, `Read`/`Append` devolvem `ErrNoQuorum`) em
  vez de servir um **log truncado como autoritativo e completo**. Quando uma
  réplica suficientemente actualizada regressa, o store recupera automaticamente.
  Coberto por `TestReviveStaleAfterQuorumLoss_NoSilentTruncation`.

Controlo de teste do cluster: `New(WithReplicas(n), WithQuorum(q))`, `Kill(id)`
(dispara eleição se for o líder), `Revive(id)` (ressincroniza do líder),
`Leader()`, `AliveCount()`, `Replicas()`, `IsAlive(id)`.

Este modelo torna as invariantes determinísticas e testáveis; **não** é um Raft
completo.

## Concorrência — sem single-writer (AOS-100, ADR-007)

A ordem total é **por stream**, nunca global — por isso **não há escritor único**.
A serialização é **por-stream** (locks listrados, `sharding.go`):

- Appends ao **mesmo stream** serializam-se (seq gapless, CAS, dedup e ordem de
  push preservados); appends a **streams diferentes** correm **em paralelo**, sem
  contenção global. Múltiplos workers escrevem e leem para replay em paralelo.
- **SPOF eliminado.** O antigo mutex global (um único líder a serializar TODAS as
  escritas) desapareceu: o `mu` do `Store` protege apenas a **membership** do
  cluster (líder, *alive set*) e os appends detêm-no em `RLock`. O log e a dedup
  são **por stream**; o commit index é atómico. A falha de um nó (`Kill`) não
  interrompe as escritas nem perde dados confirmados dentro do quórum.
- Coberto por `TestParallelMultiWriterStreams`, `TestParallelReadWhileWriting`,
  `TestNodeFailureContinuityParallel` (todos sob `-race`) e pelo benchmark
  `BenchmarkAppendParallelStreams` (ganho vs. `BenchmarkAppend` serial).

## Soberania regional (ADR-011)

Um *board* tem uma **fronteira regional de soberania**: os seus dados só podem
residir nessa região. Quando configurada, o enforcement é **fail-closed**:

- `WithRegion(region)` ou `WithSovereigntyBoard(board, region)` declaram a fronteira;
  `WithReplicaRegions(...)` dá a região de cada réplica (uma por réplica).
- **TODAS** as réplicas têm de estar na região do board. Uma réplica **fora da
  fronteira** — ou com **região ausente/desconhecida** — é **rejeitada na construção**
  com `ErrSovereigntyViolation` (`E_SOVEREIGNTY_VIOLATION`). Região desconhecida ⇒
  *deny*. Sem `WithReplicaRegions`, todas as réplicas assumem a região do board
  (cluster co-localizado — o caso comum).
- O quórum é computado **dentro da região** e a eleição de líder **nunca** promove
  liderança *cross-border*. Réplicas e backups **NUNCA** cruzam a fronteira.
- Sem fronteira configurada a soberania fica **dormente** (retro-compatível).
- `Region()` e `ReplicaRegion(id)` expõem a topologia regional. Coberto por
  `TestSovereignty_*`.

## Transporte push (fan-out)

Cada subscritor tem a sua própria goroutine e uma fila FIFO ilimitada. A entrega
é **em ordem de seq**, com filtro por `Streams` e/ou `Types` (combinados por AND).
Um subscritor **lento não bloqueia** o produtor nem os outros subscritores (o
enqueue é O(1) e nunca bloqueia). `Unsubscribe`/`Close` libertam a goroutine e a
fila sem fugas. Latência de fan-out intra-cluster **< 250 ms p95**.

- **Ciclo de vida pelo ctx.** O `ctx` passado a `Subscribe` **governa** a
  subscrição: cancelá-lo desregista-a e liberta a goroutine e a fila
  automaticamente (equivalente a `Unsubscribe`). Um `ctx` sem cancelamento
  (`context.Background()`) mantém-na activa até `Unsubscribe`/`Close`. Coberto por
  `TestSubscribeCtxCancelUnsubscribes`.
- **Bloqueio no encerramento.** `Unsubscribe`/`Close` bloqueiam até `run()`
  drenar a fila corrente, invocando o handler por cada evento pendente — **não há
  deadline**. Um handler que bloqueie indefinidamente bloqueia o encerramento; o
  contrato é que os handlers retornam (trabalho demorado/cancelável é do handler,
  que deve honrar o seu próprio ctx).
- **Fila ilimitada (tradeoff da reference impl).** A fila não tem tecto nem
  política de descarte: um subscritor permanentemente lento acumula memória sem
  limite. Backpressure, limite de profundidade e política de drop são do backend
  de produção (NATS JetStream), não deste modelo determinístico.

## Observabilidade

Gancho **leve** e opcional (`Observer`, via `WithObserver`) para contagem e
latência de `Append`/`Publish`. **Não** puxa o SDK OTel (isso é EPIC-08). Sem
segredos em código ou logs.

**Auditoria de rejeições (critério de aceitação C2).** Toda a tentativa
rejeitada — incluindo a violação append-only (`ErrAppendOnlyViolation`) — é
sinalizada via `Observer.AppendRejected`. A parte *"devolve erro"* é intrínseca
(sentinela `E_APPEND_ONLY_VIOLATION`); a parte *"é auditada"* de forma **durável**
é responsabilidade do consumidor, que deve injectar um `Observer` (via
`WithObserver`) que persista o registo. O `Observer` por omissão é **no-op**, pelo
que sem essa configuração a rejeição não é auditada de forma durável por este
pacote.

## Testes

Table-driven, determinísticos e `-race` limpos. Cobrem todos os *Testes
Requeridos* do ticket:

| Teste | Ficheiro |
|---|---|
| Append-only (overwrite rejeitado, sem API de mutação, `Read` devolve cópias) | `TestAppendOnly_*`, `TestReadReturnsCopies` |
| Ordenação + monotonicidade de `seq` sob concorrência | `TestSeqMonotonicUnderConcurrency` |
| `expected_seq` CAS → `ErrSeqConflict` / `ErrAppendOnlyViolation` | `TestExpectedSeqCAS` |
| Idempotência por `idempotency_key` (2.ª escrita = Duplicate, log inalterado) | `TestIdempotency` |
| Idempotência sobrevive a failover | `TestIdempotencySurvivesFailover` |
| Failover (3 nós, quórum 2: mata líder → 0 eventos confirmados perdidos) | `TestFailover_NoConfirmedLoss` |
| Escrita sub-quórum não fica confirmada nem aparece após failover | `TestSubQuorumRejected_NoTrace` |
| Perda de quórum + revive de réplica stale → indisponível (sem log truncado); recuperação e eleição da mais actualizada | `TestReviveStaleAfterQuorumLoss_NoSilentTruncation` |
| Erro re-tentável ao perdedor de CAS concorrente | `TestConcurrentCASLoserError` |
| `ctx` cancelado desregista a subscrição (sem fuga, sem entrega posterior) | `TestSubscribeCtxCancelUnsubscribes` |
| Latência de fan-out push (p95 < 250 ms) + ordem preservada | `TestFanoutLatencyAndOrder` |
| **Escrita multi-worker paralela** por stream (seq gapless, sem single-writer) | `TestParallelMultiWriterStreams` |
| **Replay (Read) concorrente** com escrita, log sempre gapless | `TestParallelReadWhileWriting` |
| **Falha de nó sob carga paralela** → continuidade, zero perda no quórum | `TestNodeFailureContinuityParallel` |
| **Soberania (ADR-011):** réplica *cross-border* / região ausente rejeitada (fail-closed) | `TestSovereignty_*` |
| Failover preso à fronteira regional (líder in-region, zero perda) | `TestSovereignty_FailoverStaysInRegion` |

```sh
go vet ./...
# -race exige toolchain C (cgo). Ex. no Windows com MinGW:
CGO_ENABLED=1 go test ./... -race -count=1 -covermode=atomic -coverprofile=cover.out
go tool cover -func=cover.out | tail -1
```

> **Verificação `-race`.** O detector de corridas requer `CGO_ENABLED=1` e um
> compilador C. Validado limpo (0 data races) com `go 1.24.5` + MinGW gcc
> (`windows/amd64`), `-race -count=2`; cobertura **97.4%** dos statements. A remoção
> do mutex global (AOS-100) é o ponto de maior risco de corrida — daí o `-race` ser
> crítico e a suite incluir escrita multi-worker e replay concorrentes.

### Benchmark (throughput e latência) — DoD

`go test -run '^$' -bench 'BenchmarkAppend|BenchmarkFanout' -benchmem` (in-process,
13th Gen i7-1355U, `go 1.24.5 windows/amd64`):

| Benchmark | Latência | Throughput | Alloc |
|---|---|---|---|
| `BenchmarkAppend` (serial, 1 stream: commit + replicação a 3 réplicas) | ~2.7 µs/op | ~370 000 ops/s | 5234 B/op, 25 allocs/op |
| `BenchmarkAppendParallelStreams` (workers em streams distintos, 12 CPU) | ~0.98 µs/op | **~1 020 000 ops/s** | 4938 B/op, 25 allocs/op |
| `BenchmarkFanout` (append + entrega a 50 subscritores, ponta-a-ponta) | ~35 µs/op | ~28 000 appends/s | 16684 B/op, 75 allocs/op |

O **ganho do paralelismo por-stream** (AOS-100): a escrita paralela a streams
distintos atinge ~1.9–2.9× o throughput da escrita serial num único stream
(depende do número de CPUs e da carga da máquina — medições próprias variaram entre
~1.9× e ~2.9× em execuções distintas), porque não há serializador único —
exactamente o SPOF de escrita que o baseline single-writer tinha. O invariante
demonstrado pelo benchmark, e não o número absoluto, é o que importa para o DoD:
escrever a streams distintos em paralelo bate consistentemente a escrita serial a
um só stream. Números in-process de referência (não substituem a validação no
backend real); os valores absolutos da tabela acima são ilustrativos e dependentes
do hardware.

> **DoD *"Replicação e failover validados em staging"*.** Fica **pendente**: este
> pacote é o modelo de replicação in-process de referência; a validação em
> *staging* com o backend distribuído real (NATS JetStream / Raft) é âmbito de
> *deployment* e é rastreada em aberto no ticket **AOS-002**, fora do código deste
> pacote.

## Estrutura

```
eventstore/
  doc.go            # visão geral do pacote (PT-PT)
  event.go          # envelope Event, EventInput, Producer, Filter, opções
  errors.go         # sentinelas E_* (errors.Is)
  ulid.go           # ULID inline (crypto/rand + Crockford base32)
  store.go          # EventStore, Store, Append/Read/Close, Observer, opções (região)
  replicated.go     # réplicas (log+dedup por stream), quórum, eleição, soberania
  sharding.go       # locks listrados por-stream (sem single-writer) + normalização de região
  subscribe.go      # transporte push / fan-out
  store_test.go     # testes table-driven (-race)
  replication_aos100_test.go  # paralelismo por-stream + soberania (AOS-100)
  schemas/
    event-envelope-1.0.json  # schema JSON do envelope + registo de versão
```

Referência: `specs/EPIC-01_Fundacoes_Plano_Controlo.md` (AOS-002),
`specs/EPIC-10_Topologia_Operacao_DR.md` (AOS-100),
`tecnica/10_Topologia_Implantacao_Operacao.md` (§3, §6, ADR-007/ADR-011),
`tecnica/12_Contratos_de_Interface.md` (§5, contrato C2),
`tecnica/13_Modelo_Dados_Eventos.md` (§3–§4).
