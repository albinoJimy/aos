# control-plane/scheduler — Escalonador (SCH)

Esqueleto contratual do **Escalonador** do AOS (ticket **AOS-012**). O SCH é o
consumidor de referência do plano de controlo: subscreve os eventos `task.ready`
no barramento (AOS-009), transita a tarefa `ready → running`, e despacha a tool
call **SEMPRE via Reference Monitor (AOS-003)**. Conforme a `Decision` e o
resultado, emite `task.complete` ou `task.failed`, correlacionados por `run_id`.

Importa o contrato partilhado de `control-plane/orchestrator/contract`.

## Fluxo

```
task.ready ─▶ ready→running ─▶ (emite task.running)
                                    │
                                    ▼
                           rm.Mediate(Call)      ◀── ÚNICA via de execução
                                    │
              permit + tool ok ─────┼───── deny/escalate ─┐
                     │              │        tool error ──┤
                     ▼              │                     ▼
              task.complete         │                task.failed
```

O estado transita **como eventos** no Event Store (via barramento). Num run
bem-sucedido o stream `run_id` fica com:
`run.created → task.ready → task.running → tool.call.mediated → task.complete`.
O `tool.call.mediated` (gravado pelo RM) entre `running` e `complete` é a prova de
que o despacho passou pelo gate.

## No-bypass (garantia estrutural, reutilizada do AOS-003)

O SCH detém apenas um `*referencemonitor.Monitor` — **nunca** a `ToolFunc`. A tool
é registada no RM pelo compositor; o SCH **não tem qualquer via** de a invocar
fora de `rm.Mediate`. Um RM que **nega** leva o fluxo a `task.failed` **sem efeito**
(a tool não executa). O teste `TestSchedulerOnlyExecutesViaRM` prova-o: com uma
cadeia de hooks que nega, o contador de efeito da tool fica a `0` e não há evento
`tool.call.mediated` no stream.

`scheduler.New` recusa um RM `nil` — não existe caminho de execução sem RM.

```go
rm := referencemonitor.New(
    referencemonitor.WithHooks(referencemonitor.DefaultHooks()...),
    referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
)
_ = rm.Register("tool:echo", meuToolFunc)   // tool vive NO RM, não no SCH
sch, _ := scheduler.New(bus, rm, es)         // es = StreamReader (guard de idempotência)
_ = sch.Start(ctx)                           // subscreve ANTES do Submit
```

## O que é STUB / NÃO-PRODUTIVO (AOS-012)

Marcado como stub no código; **fica para EPIC-03**:

- **Sem leases/fencing/heartbeat** e sem detecção de zombies — o despacho é
  sequencial e direto; um worker morto não é detectado.
- **Sem prioridade** nem **backpressure avançada** (o barramento tem a sua própria
  política de overflow, mas o SCH não a modula).
- **Durabilidade PARCIAL do consumo**: a subscrição (cursor+ACK) re-entrega
  at-least-once eventos **já observados** mas não confirmados; **não** faz
  catch-up multi-stream. A subscrição filtra por `Type` sem `Streams`, pelo que só
  recebe entregas **live** — um `task.ready` publicado enquanto o SCH está em baixo
  (ou **antes** de `Start`) **não** é re-entregue no reinício e o run fica sem
  terminal. Por isso o SCH deve `Start` **antes** do `Submit`.
- **Sem saga de compensação**: `failed` é terminal simples (não entra em
  `compensating`).

## Pontos de extensão (EPIC-03)

- Lease/fencing token na transição `running`. NOTA: o `step_id` é âncora de
  idempotência dos **EVENTOS** (dedup do Event Store), **não** do efeito da tool.
  A idempotência de EFEITO é hoje garantida por um guard em `dispatch`, que lê o
  stream do run e recusa re-despachar uma tarefa com `task.running`/terminal já
  emitido (evita double-exec sob re-entrega at-least-once). Um lease/fencing token
  dá exactly-once forte sob workers **concorrentes** (fora do esqueleto de 1
  consumidor sequencial).
- Fila de prioridade e admissão sobre o consumo do barramento.
- Estados de suspensão (`waiting_on_tool`, `waiting_on_human`) — acrescentados no
  `contract` e tratados aqui sem alterar `Start`/`Stop`.

## Dependências e build

Zero dependências externas. `orchestrator/contract`, RM (AOS-003), bus (AOS-009) e
eventstore (AOS-002) integrados por `replace` local. **Não** altera nenhum módulo
existente — consome-os pelas APIs públicas.

```
go mod tidy && go vet ./... && go test ./... -race -count=1 -covermode=atomic -coverprofile=cover.out
```
