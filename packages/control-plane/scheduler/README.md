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

## Circuit breaker de orçamento (AOS-029 — tokens/$ por árvore)

`breaker.go` implementa o **circuit breaker de orçamento** por **árvore de run**: o par
de *continuação* do admission control (AOS-027 governa a **entrada**, o breaker governa a
**continuação**). Interrompe de forma segura uma árvore que queima orçamento a uma
velocidade anómala **ou** que esgota o orçamento atribuído, antes da explosão de custo.
**Compõe** — não reimplementa — o orçamento hierárquico (AOS-026/008, `budget.Budget`
pela porta `TreeBudgetReader`) e a máquina de estados durável das tarefas (AOS-017,
`state.Machine` pela porta `TaskParker` / adaptador `MachineParker`).

- **Dois sinais de trip.** `Observe(sample)` contabiliza o consumo (tokens/$) numa
  **janela deslizante** com **relógio injectável** (à imagem do refill do AOS-027) e
  reavalia: **(a) VELOCIDADE** — tokens/$ por janela acima do limiar; **(b) ESGOTAMENTO**
  — remanescente da árvore (`budget.Available`) `<=` margem. O esgotamento tem
  precedência (condição mais grave).
- **Máquina PRÓPRIA declarativa** (`closed → open → half_open → closed|open`), tabela de
  4 pares — **não** confundir com os dez estados das TAREFAS (AOS-017). Half-open permite
  a **retoma controlada** após o cooldown (relógio injectável).
- **Trip fail-closed para o consumo.** `Allow()` **nega** a continuação enquanto `open`
  (na dúvida, pára); um erro de decisão degrada para negação. Ao disparar, as tarefas em
  curso transitam `running → paused` (estado durável seguro) via a `Machine` do AOS-017,
  de forma **idempotente** — uma tarefa já parada/terminal é no-op, **sem duplicar
  efeitos** (ADR-001).
- **Retoma NÃO re-executa concluídos.** O breaker só **liberta** a continuação
  (`Allow → true` em half-open); a não-reexecução de passos é do **ledger/replay**
  determinístico (AOS-014/017) — o breaker nunca reexecuta.
- **Aviso ~80%.** Antes do hard-trip, `budget.warning_80pct` sinaliza a aproximação do
  limite (uma vez por ciclo `closed`), integrando a exaustão graciosa (UX).
- **Limiares por classe/tenant** (`Thresholds` + `ThresholdProvider` /
  `StaticThresholdProvider`, resolução por especificidade) — **opções**, nunca constantes.
- **Eventos append-only** (`budget.breaker_tripped` / `budget.breaker_half_open` /
  `budget.breaker_closed` / `budget.warning_80pct`), cada transição com o **motivo**
  (velocidade vs esgotamento) e o **estado de orçamento no momento**. `Rebuild` reconstrói
  o estado do breaker por replay (sobrevive a crash: o `open` durável mantém o fail-closed);
  `Replay` devolve a sequência. Determinismo: relógio/IDs injectáveis, serialização estável.
  Span OTel `budget_breaker` com os sinais do breaker + **custo por span**, via a porta
  `agentruntime.Tracer` zero-dep.

**Fora do âmbito de AOS-029:** backpressure/filas (AOS-030), degradação
shed→defer→downgrade→reject (AOS-031), scheduling priority-aware (AOS-032), roteamento
(AOS-033), métricas de saturação (AOS-034). **Não** reimplementa budget/state/admission.

## Backpressure: filas limitadas + política declarativa (AOS-030)

`queue.go` (filas limitadas) e `policy.go` (motor de política declarativa) implementam o
**backpressure real**: em vez de acumular trabalho de forma ilimitada (cascatas de
timeouts), as filas têm **limite explícito** por partição `tenant:priority` e o enchimento
é **detectado e sinalizado a montante**, nunca silenciosamente absorvido. Aqui só se
**SELECCIONA** a acção de degradação; a **execução** (shed→defer→downgrade→reject) é o
AOS-031.

- **Filas limitadas por partição.** `PartitionedQueues` mantém uma fila bounded por
  `tenant:priority`, com **limite explícito de comprimento** (`MaxLen`, tecto duro) **e/ou
  idade** (`MaxAge` do item mais antigo). Ao atingir o limite, aplica-se a **política** em
  vez de crescer — **sem acumulação ilimitada** (provado: a profundidade nunca excede
  `MaxLen`, `TestQueue_BoundedNoUnboundedAccumulation`).
- **Watermarks com histerese.** Cada partição satura ao cruzar o `HighWatermark` e só
  **sai** de saturado ao descer **até/abaixo** do `LowWatermark` — o estado latched entre
  os dois evita *flapping* (`TestQueue_WatermarksHysteresis_NoFlapping`).
- **Motor de política DECLARATIVA versionado.** `PolicyEngine` carrega um artefacto JSON
  (stdlib, **zero deps** — não YAML/Rego) que mapeia condições de saturação
  (`priority` + `min_fill_ratio` + `min_age_ms`) → acção nominal. A selecção é
  **determinística** (primeira regra que casa, por ordem de declaração; senão
  `default_action`). Emite `degradation_policy_selected`.
- **Hot-reload atómico versionado (SemVer).** `Reload` troca o motor de decisão via
  `atomic.Pointer` — as filas em curso **não** são tocadas (**nenhum trabalho se perde**,
  `TestPolicy_HotReloadPreservesInFlightWork`). A nova versão tem de ser **estritamente
  mais recente** (SemVer monótono); cada troca regista o **changelog** (versão-antiga→nova)
  no **audit trail** (evento append-only). Validação **fail-closed**: JSON malformado,
  acção desconhecida, SemVer inválido ou versão não-monótona são **rejeitados** e mantém-se
  a política anterior (`TestPolicy_ReloadFailClosed_KeepsPrevious`).
- **Acoplamento ADITIVO ao admit (AOS-027).** `PartitionedQueues` implementa
  `BackpressureSource`; injectada via `Admission` + **`WithBackpressure(source)`**, faz o
  admit **ADIAR (defer)** mais agressivamente para o tenant saturado, mesmo havendo
  headroom — o sinal propaga-se a montante. **Sem** a opção, o admit é bit-a-bit o do
  AOS-027 (os seus testes não mudam). Prova concreta: fila saturada ⇒ mais defers
  (`TestBackpressure_PropagatesToAdmit_MoreDefers`).
- **Eventos append-only observáveis.** `queue_saturated`, `backpressure_signalled`,
  `backpressure_cleared`, `degradation_policy_selected` e o changelog `policy_reloaded`.
  `ReplayQueue` / `ReplayVersions` reconstroem o estado de fila e a linhagem de versões por
  replay (determinismo: relógio/IDs injectáveis, iteração ordenada, serialização estável).
  Spans OTel `backpressure_enqueue` / `backpressure_policy_select` (profundidade/idade de
  fila) via a porta `agentruntime.Tracer` zero-dep.

**Fora do âmbito de AOS-030:** a **execução** das acções shed/defer/downgrade/reject
(AOS-031), a degradação graciosa (AOS-031), o scheduling priority-aware (AOS-032), o
roteamento (AOS-033) e as métricas de saturação (AOS-034). **Não** reescreve o admission
(só acoplamento aditivo).

## Degradação graciosa: executor shed→defer→downgrade→reject (AOS-031)

`degradation.go` implementa o **executor** das quatro acções de degradação. A política de
AOS-030 (`policy.go`) **SELECCIONA** a acção quando uma fila satura; o `Degrader`
**EXECUTA-a**, na ordem de preferência canónica da fonte (ADR-008)
**shed → defer → downgrade → reject**. **Compõe** — não reimplementa — a política (AOS-030)
nem o admission (AOS-027); o downgrade encaminha para um tier mais barato via a **porta**
`ModelTierRouter` (o Model Gateway de EPIC-06 implementá-la-á; aqui só a porta + impl de
referência), à imagem do `QuotaProvider` de AOS-027.

```go
router := scheduler.NewStaticModelTierRouter( // impl de referência (NÃO é o Model Gateway)
    scheduler.ModelTier{Tier: "premium", Model: "claude-opus", CostRank: 30},
    scheduler.ModelTier{Tier: "standard", Model: "claude-sonnet", CostRank: 20},
    scheduler.ModelTier{Tier: "economy", Model: "claude-haiku", CostRank: 10},
)
deg, _ := scheduler.NewDegrader(router,
    scheduler.WithDegradationLog(es),                 // eventos append-only (AOS-002)
    scheduler.WithDeferSink(queues),                  // defer preserva o trabalho (AOS-030)
    scheduler.WithDegradationTracer(tracer),          // agentruntime.Tracer zero-dep
)
// A política (AOS-030) escolhe a acção; o executor executa-a.
action, ver, _ := policy.Select(ctx, cond)
res, err := deg.Execute(ctx, action, item, scheduler.TriggerFromCondition(cond, ver, "saturação"))
```

- **Shed.** `Shed` descarta trabalho **opcional/baixa prioridade** com **razão** e evento
  `work_shed`. **GUARDS FAIL-CLOSED** (nenhum emite evento nem descarta nada; o chamador
  escala): sem **razão** no gatilho ⇒ `ErrMissingReason`; trabalho **crítico**
  (`DegradationItem.Critical`) ⇒ `ErrCannotShedCritical`; trabalho **irreversível**
  (`DegradationItem.Irreversible`) ⇒ `ErrCannotShedIrreversible`; trabalho **não marcado
  opcional** (`DegradationItem.Optional`) ⇒ `ErrCannotShedNonOptional`. O default é
  **proteger** — só se descarta trabalho *provadamente* opcional (a mera ausência de
  `Critical` NÃO autoriza o descarte). Cobertura: `TestShed_CriticalNeverDiscardedSilently`,
  `TestShed_NonOptionalFailsClosed`, `TestShed_IrreversibleNeverDiscarded`.
- **Defer.** `Defer` adia trabalho admissível com `retry_after`, **preservando-o** pela porta
  `DeferSink` (integra AOS-027/030 — não perde o trabalho). Um erro do sink é **propagado**
  (fail-closed: não afirma um defer que não preservou). Evento `work_deferred`.
- **Downgrade.** `Downgrade` encaminha para o tier **mais barato** via `ModelTierRouter.Cheaper`,
  registando o swap como **VARIÂNCIA EXPLÍCITA** (`tier_antigo→tier_novo`, evento
  `model_downgraded`) — **NUNCA silencioso**. A variância entra no log para o **replay ser
  fiel** (ADR-010/AOS-016) e o downgrade fica **reversível**. Sem tier mais barato **ou**
  trabalho **irreversível** ⇒ `Applied=false` (nenhuma variância — o irreversível não é
  degradado em silêncio; escala para o reject). `Execute` traduz `Applied=false` em
  `ErrDegradationNotApplied` para o chamador **escalar** (a pressão nunca fica sem alívio
  silenciosamente); `ExecuteChain` faz esse fallback embutido.
- **Reject.** `Reject` recusa como **último recurso** com um erro **claro e accionável**
  (`ErrWorkRejected`) e evento `work_rejected`. **FAIL-CLOSED para irreversíveis**
  (`DegradationItem.Irreversible`): a rejeição é terminal — o efeito irreversível NÃO ocorre e
  `Normalize` nunca ressuscita trabalho rejeitado.
- **Ordem de preferência configurável.** `ExecuteChain` percorre a ordem (por omissão
  `DefaultPreferenceOrder`, **configurável** por classe/tenant) aplicando o **primeiro degrau
  aplicável**: um crítico **salta** o shed e é adiado; um item não-diferível sem tier mais
  barato termina em reject (`TestExecuteChain_*`). A **escalada por pressão crescente** é
  conduzida pela política de AOS-030 e executada com `Execute`
  (`TestChain_IncreasingPressureFollowsPreferenceOrder`).
- **Reversibilidade.** `Normalize(reason)` reverte os downgrades **reversíveis** ao normalizar
  a carga (ex.: `backpressure_cleared` de AOS-030): restaura o tier (evento `tier_restored`,
  `tier_novo→tier_antigo`) por ordem de ID (determinismo) e é **idempotente** (2ª passagem é
  no-op). Um downgrade em **cascata** do mesmo item restaura o tier **original**. Shed/reject
  **não** são reversíveis (documentado). A variância `model_downgraded` **permanece** no log
  mesmo após a reversão (`TestNormalize_*`). **Erro parcial do store:** cada item só sai do
  registo `active` **depois** de o seu `tier_restored` ser persistido — um erro a meio deixa
  os restantes activos para a próxima `Normalize` (`TestNormalize_PartialFailureKeepsUnrestored`).
  **Durabilidade:** `RehydrateActive` reconstrói o registo de downgrades activos do log no
  arranque (`model_downgraded` menos `tier_restored`), para a reversão sobreviver a restarts
  (`TestRehydrateActive_RebuildsFromLogAfterRestart`).
- **Eventos append-only + replay.** `work_shed`, `work_deferred`, `model_downgraded`,
  `work_rejected` e `tier_restored`, cada um com **gatilho + acção + efeito**.
  `ReplayDegradation` reconstrói a sequência (incl. a variância do downgrade) por ordem de seq.
  Determinismo: relógio/IDs injectáveis, iteração ordenada, serialização estável (structs).
  Span OTel `degradation_execute` **por acção**, via a porta `agentruntime.Tracer` zero-dep.

**Fora do âmbito de AOS-031:** o **Model Gateway** (EPIC-06 — o `ModelTierRouter` é a porta),
o scheduling priority-aware (AOS-032), o roteamento least-loaded (AOS-033) e as métricas de
saturação (AOS-034). **Não** reimplementa a política (AOS-030) nem o admission (AOS-027).

## Scheduling priority-aware + aging (AOS-032)

`priority.go` implementa o `Dispatcher`: despacho por **classe de prioridade** sobre as filas
particionadas (AOS-030), com **aging** anti-starvation, decisão **latency-aware** (idade + SLO)
e ordem **determinística/replayável**. **Compõe** — não reimplementa — as filas (AOS-030) nem o
admission (AOS-027): ordena **sobre** as filas e só despacha o que o `Admit` **admitiu**.

```go
d, _ := scheduler.NewDispatcher(
    scheduler.AgingParams{Base: 0, AgingStep: 1, AgingInterval: time.Second}, // omissão
    scheduler.WithClassAging("P0", scheduler.AgingParams{Base: 300, AgingStep: 10, AgingInterval: time.Second}),
    scheduler.WithClassAging("P1", scheduler.AgingParams{Base: 200, AgingStep: 10, AgingInterval: time.Second}),
    scheduler.WithClassAging("P2", scheduler.AgingParams{Base: 100, AgingStep: 10, AgingInterval: time.Second}),
    scheduler.WithTenantClassAging("vip", "P2", scheduler.AgingParams{Base: 5000}), // override por tenant
    scheduler.WithAdmission(adm),        // AOS-027: só despacha trabalho ADMITIDO
    scheduler.WithQueues(queues),        // AOS-030: bounding + backpressure (opcional)
    scheduler.WithDefaultKey(providerKey),
    scheduler.WithDispatchClock(clk.now),// relógio INJECTÁVEL (sem time.Now na decisão)
    scheduler.WithDispatchLog(es),       // eventos append-only (AOS-002)
)
d.Submit(ctx, scheduler.Task{ID: "t1", Tenant: "acme", Class: "P2", Cost: 100, SLO: 30 * time.Second})
res, _ := d.Dispatch(ctx) // serve a MAIOR prioridade efectiva ADMISSÍVEL
```

- **Prioridade efectiva = f(base, idade, SLO), MONÓTONA na idade** (aritmética inteira, sem
  float — replay byte-a-byte): `eff = Base + AgingStep·(idade/AgingInterval) + SLOWeight·(idade/SLO)`.
  Como o termo de aging cresce **sem tecto**, qualquer classe baixa acaba por ultrapassar trabalho
  **novo** de classe alta — a garantia **ZERO starvation** (`TestDispatch_NoStarvationAdversarial`,
  fluxo P0 contínuo, a vítima P2 é despachada). `TestDispatch_AgingPromotesOldLowOverNewHigh`.
- **Latency-aware.** O SLO entra na decisão além da prioridade nominal: a igual classe e idade, a
  tarefa de SLO **mais apertado** é servida primeiro (`TestDispatch_LatencyAwareSLO`).
- **Só despacha ADMITIDO (AOS-027).** Cada candidato passa por `AdmissionGate.Admit`; sem headroom
  **ADIA** (`Dispatched=false` com `retry_after`), **nunca descarta**; rejeição permanente é saltada.
  Serve a maior prioridade **admissível** (`TestDispatch_OnlyAdmittedDispatched`: TPM 300/custo 100 ⇒
  3 despachos, 2 adiados preservados).
- **Ordem determinística/replay.** A selecção ordena um **slice** (nunca ordem de mapa Go) com
  tie-break **estável**: prioridade efectiva desc → timestamp de entrada asc → `task_id` asc (total
  order, `task_id` único). Mesmos inputs ⇒ **mesmos bytes** de evento na mesma ordem
  (`TestDispatch_ReplayByteForByte`).
- **Aging por classe/tenant.** `AgingParams` configuráveis por omissão, por classe e por
  (tenant, classe) — resolução por especificidade (`TestDispatch_AgingParamsPerClassTenant`).
- **Eventos append-only + replay.** `task_scheduled` (prioridade efectiva/idade) por despacho e
  `priority_aged` na **promoção** por aging; `ReplaySchedule` reconstrói a ordem
  (`TestDispatch_ReplayScheduleReconstructs`). Span OTel `priority_dispatch` (tempo de espera por
  classe) via a porta `agentruntime.Tracer` zero-dep. Seguro para concorrência
  (`TestDispatch_ConcurrentRaceFree`, `-race`).

**Fora do âmbito de AOS-032:** o roteamento least-loaded/token-aware (AOS-033) e as métricas de
saturação/headroom (AOS-034). **Não** reimplementa as filas (AOS-030) nem o admission (AOS-027).

## Dependências e build

Zero dependências externas. `orchestrator/contract`, RM (AOS-003), bus (AOS-009) e
eventstore (AOS-002) integrados por `replace` local. **Não** altera nenhum módulo
existente — consome-os pelas APIs públicas.

```
go mod tidy && go vet ./... && go test ./... -race -count=1 -covermode=atomic -coverprofile=cover.out
```
