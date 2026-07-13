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

## Admission control global (AOS-027 — token-bucket distribuído)

Extensão do SCH que resolve o modo de falha *agregado* do plano-base (ADR-008):
15 boards, cada um dentro do seu `max_spawn` local, saturam colectivamente o
rate limit partilhado do provider. A admissão de trabalho passa a ser uma decisão
**global** denominada em **tokens**, não a soma de decisões locais cegas.

Ficheiros: `quota.go` (porta `QuotaProvider`/`TenantQuotaProvider` + impl de
referência) e `admission.go` (token-bucket distribuído + `Admit`/`Release`/`Replay`).

```go
qp := scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: 1000, RPM: 300, Window: time.Minute})
adm, _ := scheduler.NewAdmission(es, qp,                       // es = Event Store (AOS-002)
    scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 100}),
    scheduler.WithTracer(tracer),                             // agentruntime.Tracer zero-dep
)
res, _ := adm.Admit(ctx, scheduler.AdmitRequest{Key: key, Tenant: "board-7"})
if !res.Granted {
    // sem headroom: ADIA (nunca descarta) — reagenda após res.RetryAfter
}
```

Garantias:

- **TPM/RPM real via porta.** Os limites derivam de `QuotaProvider` por
  `provider:model:region` — o Model Gateway (EPIC-06) implementá-la-á; a impl de
  referência (`StaticQuotaProvider`) fecha o contrato entretanto. **Nunca**
  constantes locais como fonte.
- **Reserva atómica sem SPOF (ADR-007).** O estado do bucket vive no Event Store
  replicado (stream `admission/bucket/<key>`); a reserva é um **Append CAS**
  (`WithExpectedSeq`). Workers *stateless* relêem o estado, calculam o headroom e
  reservam; o perdedor de corrida re-tenta ou adia. Sem *single-writer*, sem
  contador em memória partilhado.
- **`admit()`/defer.** `Admit(cost) → {granted, retry_after}`: reserva se há
  headroom; sem headroom **ADIA** com `retry_after` derivado do refill, **nunca
  descarta silenciosamente**.
- **Rejeição PERMANENTE de pedido oversized.** Se o custo estimado excede o
  próprio tecto `TPM`/`RPM` da chave (ou do tenant), nenhum refill futuro o torna
  admissível. Em vez de aconselhar um `retry_after` que geraria *poll* eterno
  (livelock), `Admit` devolve `Rejected=true` com `retry_after=0` e marca o
  `admit_deferred` como `unsatisfiable` (span `aos.admission.unsatisfiable`). O
  chamador distingue assim "tenta mais tarde" de "nunca admissível".
- **`Release` = reconciliação PARCIAL.** `Release(id, tokens, requests)` SUBTRAI
  o montante indicado do débito activo da reserva (clamp em 0), **não** liberta a
  reserva inteira. Devolver 100 de uma reserva de 1000 abre 100 de headroom (não
  1000); os 900 remanescentes continuam a contar contra o TPM enquanto in-flight.
  Libertar o custo total remove a reserva. O clamp garante que sobre-libertar
  nunca abre headroom além do reservado (nunca oversubscreve). A idempotência de
  `Admit` é sensível ao custo: reusar um `RequestID` activo com custo diferente é
  rejeitado (`ErrIdempotencyConflict`), não concedido cegamente.
- **Refill temporizado, relógio injectável.** Uma reserva expira ao fim de
  `Window` (janela deslizante por reserva). Sem `time.Now` na decisão. A soma das
  reservas activas **nunca excede** o TPM/RPM — provado sob carga concorrente com
  `-race` (`TestAdmit_ConcurrentNoOversubscription`).
- **Quotas multidimensionais por tenant.** `TenantQuotaProvider` particiona o
  bucket global; o **tecto global domina sempre** (um tenant nunca excede o
  global, mesmo com folga na sua partição).
- **Eventos append-only + replay.** `admit_requested`/`admit_granted`/
  `admit_deferred`/`quota_released`. Grants/releases no stream de reserva (fonte
  de verdade, com CAS); auditoria à parte. `Replay`/`ReplayAudit` reconstroem a
  sequência de forma determinística (ADR-001).

### Limites conhecidos (semântica declarada)

- **Distribuição cross-process exige um `EventLog` verdadeiramente partilhado.** A
  admissão é genuinamente *stateless* (sem contador em memória): toda a
  contabilidade passa pela porta `EventLog` (`foldBucket` lê, `Admit` faz Append
  CAS). Dois workers que **partilhem o mesmo** Event Store serializam grants e não
  oversubscrevem — provado sob `-race`. **Porém**, o `*eventstore.Store` de
  referência é um cluster de réplicas **in-process**; "workers stateless" só se
  vêem uns aos outros **dentro do mesmo processo OS**. A não-oversubscription
  entre processos distintos requer um `EventLog` real partilhado/replicado
  (fornecido pelo deploy / Model Gateway — porta EPIC-06), **não** o `Store`
  in-process de referência.
- **Janela deslizante POR-reserva vs minuto corrido do provider.** O refill expira
  cada reserva a `ts+Window` (janela deslizante individual). A invariante
  declarada — "a soma das reservas **activas** nunca excede `TPM`" — mantém-se em
  **qualquer instante**. Mas contra um provider cujo limite real é "`TPM` por
  minuto corrido", reservas perto do fim da sua janela somadas a um novo lote no
  início da sua podem atingir até ~2×`TPM` dentro de uma janela deslizante real —
  é inerente a esquemas de janela e não viola a invariante declarada. Se o
  provider penalizar *bursts* no reset, considerar um bucket de janela fixa
  alinhada ou suavização (*leaky-bucket*) — fica para trabalho futuro.

**Fora do âmbito de AOS-027:** `max_spawn` derivado do headroom (AOS-028),
circuit breaker (AOS-029), backpressure/filas (AOS-030), degradação (AOS-031),
scheduling priority-aware (AOS-032), roteamento (AOS-033) e o Model Gateway
(EPIC-06).

## `max_spawn` derivado do headroom (AOS-028 — reserva no admit)

O plano-base fazia *spawn* com um `max_spawn` **constante**, cego ao estado
agregado do provider. O `SpawnCoordinator` (`spawn_admission.go`) substitui-o por
um valor **derivado dinamicamente do headroom**, ligando a delegação hierárquica
(AOS-026, `orchestrator.Delegator` — sub-orçamento por árvore) ao admission
control global (AOS-027, `Admission` — token-bucket distribuído). **Compõe**, não
reimplementa: os dois primitivos ficam intactos.

- **Derivação dinâmica.** `deriveMaxSpawn(headroom_tokens, headroom_requests, custo)`
  = `min(headroom_tokens/custo, headroom_requests)`, **reavaliada a cada pedido** —
  nunca uma constante. É **monótona** (mais headroom ⇒ ≥ *spawns*) e **0** sob
  headroom nulo. `Coordinator.MaxSpawn(...)` expõe o valor corrente (leitura pura,
  via `Admission.Headroom`, sem reservar); `Admission.Headroom` reusa o `foldBucket`
  do AOS-027 — o **mesmo** estado que o `Admit` vê.
- **Reserva no admit ANTES do spawn.** `RequestSpawn` chama `Admission.Admit(custo)`
  **antes** de criar o sub-agente. Sem headroom ⇒ **ADIA** (`spawn_deferred_no_headroom`,
  com `retry_after`), devolve `Deferred=true` e **não** cria o sub-agente nem toca no
  sub-orçamento (nunca *oversubscription*). Custo > tecto ⇒ rejeição **permanente**
  (`ErrSpawnUnsatisfiable`), distinta do *defer* transitório.
- **Ambos os limites.** Só após reservar headroom se delega ao `Delegator.Spawn`
  (sub-orçamento da árvore). **Ambos** têm de conceder: se o global concede mas o
  sub-orçamento **nega**, o headroom já reservado é **LIBERTADO** e o *spawn* é
  recusado (`ErrSubtreeBudgetDenied`) — **sem fuga de duas-fases** (o risco central).
- **Libertação idempotente.** `Finish(ticket, success)` consolida o sub-orçamento
  (`Delegator.Finish` — *Commit* em sucesso, *Release* em falha/timeout) e liberta o
  headroom (`Admission.Release`). Um segundo `Finish` é **no-op** (guard atómico no
  ticket + dedup por `step_id`): terminar/falhar/timeout com retries **não** deixa
  fuga de reservas. A contagem de reservas activas volta sempre a 0 (provado sob
  `-race`, incl. `Finish` concorrente do mesmo ticket).
- **Eventos append-only** (`spawn.headroom_reserved` / `spawn.headroom_released` /
  `spawn.spawn_deferred_no_headroom`, com o headroom reservado por *spawn*) tornam
  cada decisão auditável; `ReplaySpawnAdmission` reconstrói a sequência. Determinismo:
  relógio/IDs injectáveis, serialização estável (structs). Span OTel `spawn_admission`
  com o headroom reservado por *spawn*, reutilizando a porta `agentruntime.Tracer`
  zero-dep.

**Fora do âmbito de AOS-028:** circuit breaker (AOS-029), backpressure/filas
(AOS-030), degradação (AOS-031), scheduling priority-aware (AOS-032), roteamento
(AOS-033), métricas de saturação (AOS-034) e o Model Gateway (EPIC-06).

## Dependências e build

Zero dependências externas. `orchestrator/contract`, RM (AOS-003), bus (AOS-009) e
eventstore (AOS-002) integrados por `replace` local. **Não** altera nenhum módulo
existente — consome-os pelas APIs públicas.

```
go mod tidy && go vet ./... && go test ./... -race -count=1 -covermode=atomic -coverprofile=cover.out
```
