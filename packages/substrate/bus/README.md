# `substrate/bus` — Barramento de eventos push + subscrições (AOS-009)

Camada de **distribuição fiável** sobre o Event Store (AOS-002, ADR-007). O
barramento **envolve** um `eventstore.EventStore` e acrescenta subscrições
nomeadas por filtro, cursores duráveis com ACK (entrega *at-least-once*), replay
por `seq`, *backpressure* com política declarada e *dead-letter*. **Não**
reimplementa o Event Store — consome-o.

Módulo: `github.com/aos-ref/substrate/bus` · Go 1.24 · **zero dependências
externas** · `replace` local para `../eventstore`.

## Porquê

O plano de controlo escala com **push event-driven**: o Escalonador empurra
trabalho a *workers* stateless (`tecnica/03`). O barramento desacopla produtores
de consumidores e **evita polling** — os eventos *committed* são empurrados ao
`Handler` de cada subscritor.

## API essencial

```go
b, _ := bus.New(es) // es é um eventstore.EventStore

// Produzir (pass-through fino para es.Append).
b.Publish(ctx, "run-42", eventstore.EventInput{Type: "tool.call.dispatched", /* ... */})

// Subscrever por filtro, com cursor durável nomeado.
sub, _ := b.Subscribe(ctx, bus.SubConfig{
    Name:     "escalonador",                       // cursor durável por (Name, stream)
    Filter:   bus.Filter{
        Types:     []string{"tool.call.dispatched"},
        Streams:   []string{"run-42"},
        Producers: []string{"nhi:orq-1"},
    },
    Handler:  func(d *bus.Delivery) {
        processa(d.Event)
        d.Ack()                                    // confirma → cursor avança durável
    },
    Retry:    3,                                    // re-entregas antes de dead-letter
    Buffer:   1024,                                 // profundidade do buffer live
    Overflow: bus.Block,                            // política de degradação declarada
})
defer sub.Unsubscribe()
```

## Filtro (estende o do Event Store)

O `Filter` do barramento combina por AND três dimensões: `Types`, `Streams` e
`Producers` (por `Producer.NHIID`). O Event Store só conhece `Streams`/`Types`;
a dimensão `Producers` é aplicada no barramento a cada entrega. Campo vazio não
filtra nessa dimensão.

## Entrega push e a costura *catch-up → live* (sem saltos nem buracos)

Ao subscrever, o barramento:

1. Liga **primeiro** a subscrição *live* ao Event Store (que passa a bufferizar).
2. Lê a **história** a partir do arranque (`cursor+1`, ou `FromSeq` no replay)
   até à cabeça.
3. Entrega a história e depois **drena o buffer live**, **deduplicando** por
   `(stream_id, seq)` na fronteira (marca de água por *stream*).

Como o *live* é registado **antes** da leitura histórica, qualquer evento
*committed* durante a transição está no buffer; a sobreposição é deduplicada.
Resultado: **0 skips** e **ordem de `seq` monotónica por *stream***.

> A leitura histórica do Event Store é **por *stream*** (`Read(stream, fromSeq)`)
> e o `seq` é **por *stream*** (não há índice global nem enumeração de *streams*).
> Por isso o *catch-up*, o **cursor durável** (retoma) e o *replay* aplicam-se
> **apenas** a subscrições *stream-scoped* (`Streams` não vazio) — limitação
> intencional do modelo de referência. Um filtro **só por *type*/*producer*** é
> *fan-out* válido mas recebe **apenas *live*** a partir da subscrição (sem
> *catch-up* nem retoma por cursor). Para não prometer o que não cumpre,
> `Subscribe` **falha-rápido** com `ErrConfig` se for pedido *replay* (`FromSeq`)
> **sem `Streams`**.

## Cursor durável + ACK — contrato **at-least-once**

Cada subscrição **nomeada** (`SubConfig.Name`) tem um cursor durável **por
*stream***: o último `seq` **confirmado**. O `Handler` confirma explicitamente:

| Chamada | Efeito |
|---|---|
| `d.Ack()` | processamento concluído → o cursor avança de forma durável |
| `d.Nack(err)` | falha → re-entrega (até `Retry` vezes) e depois *dead-letter* |
| *nem Ack nem Nack* | **não confirmado** → cursor não avança; **re-entregue** no reinício |

- **Retoma:** no reinício, a subscrição recomeça em `cursor+1` — **0 eventos
  saltados**.
- **At-least-once:** um evento entregue mas não confirmado (queda do consumidor,
  ou `Handler` que não confirmou) é **re-entregue** após reinício. **O consumidor
  DEVE ser idempotente** (deduplicar por `(stream_id, seq)` ou pela
  `idempotency_key` do envelope).
- **Avanço contíguo:** o cursor só ultrapassa `seq = N` quando **todos** os
  `seq ≤ N` estão confirmados. Um buraco de ACK nunca é silenciosamente saltado.

### `CursorStore` (plugável)

- `MemoryCursorStore` — referência in-memory. Sobrevive à *morte de uma
  subscrição* (é externo a ela: é isso que permite a retoma), mas não à morte do
  processo.
- `SnapshotCursorStore` — variante durável: mantém estado em memória e **espelha
  cada escrita para um *sink*** (`persist`), *fail-closed* (se o *sink* falha, o
  cursor não avança). Em produção o *sink* escreve para disco/KV/DB (ex.:
  *consumer* durável de NATS JetStream).

## Replay

`SubConfig.FromSeq` (arbitrário) reprocessa a partir desse `seq`: história do
Event Store desse ponto + *live*, com a mesma costura. Ignora o cursor guardado
para efeitos de posição de arranque.

## Backpressure — política **declarada** (`OverflowPolicy`)

O buffer *live* é **limitado** por subscritor (`Buffer`). Quando enche, a política
declarada decide. **Em nenhuma política** um consumidor lento bloqueia o produtor
ou os outros subscritores (a *intake* de cada subscritor corre na sua própria
*goroutine* de subscrição *live* do Event Store; o `Append` do produtor devolve
após enfileirar, O(1)).

| Política | Comportamento em overflow |
|---|---|
| `Block` (omissão) | a *intake* **deste** subscritor espera por espaço. Preserva ordem e at-least-once. |
| `DropOldest` | descarta o mais antigo do buffer (sheds load). O descartado é **perda deliberada**: fica marcado como buraco conhecido e o cursor **avança** para além dele (não é relido no reinício). **Tradeoff de durabilidade** (o evento perde-se), não prende o cursor. |
| `DeadLetter` | encaminha o evento em excesso para a *dead-letter queue*, sem bloquear. O cursor **avança** para além do evento (fica capturado na fila). |

## Dead-letter

Um `Handler` que falha repetidamente (`Nack` mais do que `Retry` vezes) manda o
evento para a *dead-letter queue* (`Bus.DeadLetter()`, inspecionável) e o cursor
**avança** para além dele — a subscrição **não fica presa** no evento venenoso.
A política de overflow `DeadLetter` alimenta a mesma fila com `Reason:
"overflow"`.

## Métricas

`Bus.Metrics()` expõe contadores agregados (`Delivered`, `Acked`, `Nacked`,
`DeadLettered`, `Dropped`), latência de entrega (`AvgLatency`, `MaxLatency`) e
**percentis** (`P50Latency`, `P95Latency`, `P99Latency`) — medidos do *commit*
`Event.Ts` até à invocação do `Handler`, sobre uma janela recente de amostras
(reservatório circular limitado). Assim o **p95 fica observável em produção** via
`Bus.Metrics()`, não só dentro de um teste. `WithObserver` injecta um gancho de
eventos (`Delivered`/`Acked`/`DeadLettered`/`Dropped`). Sem SDK OTel (EPIC-08).

**Latência de entrega push:** alvo p95 < 250 ms (coerente com AOS-002); exposto
em `MetricsSnapshot.P95Latency` e exercitado sob carga concorrente + *catch-up*
em `TestLatenciaEntregaPushP95Metrica`, além de `TestLatenciaEntregaPushP95`
(tipicamente sub-milissegundo no modelo in-process) e `BenchmarkPublishDeliver`.

## Testes (mapeamento aos Testes Requeridos do AOS-009)

| Teste | Ficheiro | Critério |
|---|---|---|
| `TestFanoutPorFiltro` | `bus_test.go` | fan-out por type/stream/producer |
| `TestRetomaPorCursorZeroSkips` | `cursor_test.go` | retoma por cursor, **0 skips** + re-entrega at-least-once |
| `TestRetomaCobreEventosNovosAposReinicio` | `cursor_test.go` | retoma cobre eventos novos entre reinícios |
| `TestReplayPorFromSeq` | `cursor_test.go` | replay a partir de `seq` arbitrário |
| `TestCosturaCatchUpLiveSemSaltos` | `cursor_test.go` | costura catch-up→live sem buracos, ordem monotónica |
| `TestBackpressureBlockNaoBloqueiaProdutorNemOutros` | `backpressure_test.go` | consumidor lento não bloqueia produtor/outros |
| `TestBackpressureDropOldest` / `...DeadLetterOverflow` | `backpressure_test.go` | políticas de degradação declaradas |
| `TestDeadLetterHandlerFalhaRepetida` | `deadletter_test.go` | dead-letter para handler que falha > Retry |
| `TestLatenciaEntregaPushP95` | `latency_test.go` | latência de entrega push p95 < 250 ms |

### Correr

```sh
export PATH="$HOME/scoop/apps/mingw/current/bin:$HOME/scoop/shims:$PATH"
export CGO_ENABLED=1   # -race exige o gcc do mingw
go vet ./...
go test ./... -race -count=1 -covermode=atomic -coverprofile=cover.out
go tool cover -func=cover.out | tail -1
```

Estado: `go vet` limpo, `go test -race` verde, cobertura ~85%.

## Limites do modelo de referência

Determinístico e *in-process*. Em produção o barramento assenta sobre o Event
Store de produção e o cursor vive num store persistente. A *dead-letter queue* de
referência é in-memory; em produção é um *stream* dedicado.
```
