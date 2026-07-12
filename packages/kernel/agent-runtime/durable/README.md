# `durable` — Execução durável: idempotência (AOS-014) + checkpoint intra-iteração (AOS-015) + liveness lease/fencing (AOS-018)

Subpacote de `agent-runtime` que implementa a **cláusula de idempotência por passo**
do ADR-001 (`tecnica/02` §4) e o **checkpoint intra-iteração** *resume-from-step*
sobre o Event Store replicado (ADR-007). É a fundação consumida por **AOS-016**
(replay), **AOS-020** (sagas), **AOS-021/022** (activities de efeito externo).

Zero dependências externas (só a stdlib + `eventstore` e `agent-runtime` por path).

---

## Modelo de garantia (honesto)

> **Exactly-once verdadeiro do efeito externo é impossível sem cooperação
> downstream.** O contrato é **at-least-once + idempotência downstream honrando a
> key = 0 efeitos OBSERVÁVEIS duplicados.**

O ledger **não** afirma exactly-once do efeito. Afirma que, se o downstream honrar
a `idempotency key` (determinística e idêntica entre tentativas), o efeito é
registado **uma vez observável** — mesmo que o `effect` corra mais do que uma vez
(crash entre o efeito e o commit do ledger). O `effect` pode ser chamado
`>= 1` vez; a deduplicação dos efeitos externos é responsabilidade do downstream,
pela chave que o `effect` lhe propaga.

> **Alcance da precedência "already-applied" — dois âmbitos de dedup.** A verificação
> in-memory **mais** o single-flight por-key cobrem apenas o **mesmo processo**. Um
> worker **novo** (estado in-memory vazio) que **não** chame `Rebuild` volta a correr
> o `effect`; a garantia de *0 duplicados observáveis* nesse caminho **não** vem da
> verificação in-memory, mas da **dedup durável no commit do ES** (`StatusDuplicate`,
> chave `run_id:ledger-<step_id>`) **+ idempotência downstream**. A verificação
> in-memory é um **atalho** (fast-path) e um single-flight intra-processo, **não** um
> single-flight durável nem um read-through ao ES. **Não** salte a dedup downstream em
> passos "baratos" confiando só na precedência in-memory; chame `Rebuild` antes do
> primeiro `Apply` de um run para eliminar a re-execução após restart no mesmo processo.

---

## As três peças

### 1. `IdempotencyKey(runID, stepID) (string, error)` — pura, determinística, injectiva

```
key = run_id + ":" + step_id
```

- **Determinística e pura**: mesma entrada ⇒ mesma chave, sem efeitos colaterais.
- **Casa com o Event Store**: é byte-a-byte a forma que o ES (AOS-002) usa para
  deduplicar o `turn.recorded` (`eventstore/event.go`), e é a chave que a activity
  propaga ao downstream. **"Espaço único" é conceptual, não literal**: por passo há
  até **três** chaves de ES — `run_id:step_id` (turno + header downstream),
  `run_id:step_id-tool-n` (sub-passo/tool call) e `run_id:ledger-<step_id>` (o
  **registo durável do ledger**, um domínio de dedup **separado**). Commitar o ledger
  **não** deduplica na mesma chave que o downstream vê.
- **Injectiva**: rejeita `run_id`/`step_id` vazios ou que contenham `:`
  (`ErrDelimiterInInput`). Isto fecha a colisão de deslocamento do delimitador
  (`("a","bc")` vs `("ab","c")`): com o `:` proibido nos inputs, cada chave tem
  **exactamente uma** decomposição. `SplitKey` é a inversa exacta.
- **Forma opaca para logs**: `OpaqueKey` / `HashKey` devolvem o SHA-256 hex, para
  logs/spans/contadores sem expor identificadores em claro. Nunca é a chave de
  dedup (essa é sempre a canónica).

### 2. `StepSequencer` — step_id monotónico e estável

Atribui `step_id`s (`step-000001`, …) **puros na posição** (número do turno = a sua
posição no log). Consequência: o **mesmo passo lógico recebe sempre o mesmo
`step_id`** em execução, retry e replay — nunca reatribuído. É isto que permite que
`key = f(run_id, step_id)` identifique o mesmo efeito entre tentativas.

Implementa o hook `agentruntime.StepIdentity` de AOS-013 — ligação **aditiva** via
`agentruntime.WithStepIdentity(durable.NewStepSequencer())`, sem alterar a forma do
loop (mantém os testes de AOS-013 verdes; mesmo formato que o default sequencial).

```go
seq := durable.NewStepSequencer()
rt  := agentruntime.New(model, rm, recorder, agentruntime.WithStepIdentity(seq))

seq.StepID("run", 1)        // "step-000001"   (turno)
seq.SubStepID("run", 1, 2)  // "step-000001-tool-2" (activity dentro do turno)
seq.Key("run", 1)           // "run:step-000001", nil
```

### 3. `StepLedger` — ledger de resultado sobre o Event Store

```go
res, wasApplied, err := ledger.Apply(ctx, key, func(ctx) (durable.Result, error) {
    // efeito externo: PROPAGA `key` ao downstream (header de idempotência do alvo)
    return durable.Result{Status: "ok", Payload: out}, nil
})
```

Semântica (ADR-001):

1. **Verifica already-applied ANTES de qualquer efeito.** Se `key` já tem resultado
   registado (in-memory, reconstruído do ES), devolve o memorizado com
   `wasApplied=false` e **não corre** o `effect`. A verificação in-memory é reforçada
   por um **single-flight por-key** (mapa `inflight`): Applies concorrentes da mesma
   key **dentro do processo** colapsam num só — o `effect` corre no máximo uma vez por
   processo por key em voo. O `step_id` da `key` **não pode** começar por `ledger-`
   (namespace reservado do ledger) — `Apply` recusa-o com `ErrReservedStepID`.
2. Caso contrário corre o `effect`, **regista `{key → status, resultado,
   hash(resultado)}`** de forma durável no Event Store, e devolve com
   `wasApplied=true`.
3. `effect` com erro ⇒ **nada é registado** (passo não aplicado); o retry volta a
   correr o `effect` e a idempotência downstream converge sobre a mesma key.
4. **Corrida entre workers**: se dois workers correrem o `effect` em paralelo, o ES
   deduplica o registo (`StatusDuplicate`) e `Apply` devolve o resultado **canónico**
   do vencedor com `wasApplied=false` — resultado idêntico independentemente de quem
   ganhou.

**Reconstrutível** — sobrevive ao reinício do worker:

```go
ledger2, _ := durable.NewStepLedger(store) // novo processo, estado zero
ledger2.Rebuild(ctx, runID)                // relê o stream, reindexa key→resultado
```

O evento durável é `step.ledger.applied`, com `idempotency_key` namespaced no
envelope (`run_id:ledger-<step_id>`) para não colidir com o `turn.recorded`
homónimo (`run_id:step_id`).

---

## Checkpoint intra-iteração + resume (AOS-015)

O checkpoint materializa o progresso **dentro** de uma iteração no Event Store
replicado (ADR-007, fonte de verdade), para que o RT retome no **próximo passo não
confirmado** sem repetir os já confirmados nem perder os pendentes
(*resume-from-step*, não *resume-from-task*). O checkpoint dá **eficiência** (salta
o trabalho); o ledger (AOS-014) é a **rede de segurança** de idempotência se um
passo for na mesma re-tentado.

### `EventStoreCheckpointer` — o `Checkpointer` real de AOS-013

```go
cpr, _ := durable.NewCheckpointer(store) // implementa agentruntime.Checkpointer
rt := agentruntime.New(model, rm, recorder,
    agentruntime.WithStepIdentity(seq),
    agentruntime.WithCheckpointer(cpr), // ligação ADITIVA — sem alterar o loop
)
```

- **Evento append-only** `step.checkpoint` por **fase confirmada** do turno
  (`assembled`/`model_called`/`turn_recorded`/`dispatched`*/`verified`). O payload é
  o **cursor de progresso** `{ run_id, confirmed_step_id, turn, phase, step_index,
  pending_activities }` — **referencia**, não copia: a resposta do modelo vive no
  `turn.recorded`, o resultado da activity no `step.ledger.applied`.
- **`idempotency_key` namespaced** `run_id:ckpt-<phase>-<step_id>` — o **terceiro**
  domínio de dedup por passo, **distinto** do turno (`run_id:step_id`) e do ledger
  (`run_id:ledger-<step_id>`). Re-escrever o mesmo checkpoint num retry dá
  `StatusDuplicate` (a escrita de checkpoint é, ela própria, **idempotente**).
- **Consistente com o ledger**: `confirmed_step_id` é **exactamente** o `step_id`
  que o ledger usa para o mesmo passo lógico (para uma activity,
  `seq.SubStepID(run, turn, n)` = `step-000001-tool-n`).
- **Não muta o prefixo cache-estável** (ADR-009): o checkpointer nunca toca no
  assembler — só **cresce** o registo append-only.

### `Resumer` — cursor de retoma resume-from-step

```go
resumer, _ := durable.NewResumer(store, durable.WithStepIdentity(seq))
rp, _ := resumer.Resume(ctx, runID) // lê os checkpoints do stream do run
```

`Resume` encontra a **fronteira** (o último checkpoint por `seq`) e devolve o
`ResumePoint`:

| Fronteira | Retoma |
|---|---|
| `verified` do turno *T* | turno *T+1*, `NextStepID = StepID(T+1)` |
| `dispatched` com pendentes | turno *T*, `NextStepID` = 1.ª activity pendente, `PendingActivities` = restantes |
| `dispatched` sem pendentes | turno *T* (re-verifica; ledger dedup) |
| `assembled`/`model_called`/`turn_recorded` | turno *T* (re-entra; modelo re-chamado) |
| **sem checkpoints** | `FromScratch`, turno 1 |

O `ResumePoint` é **serializável e estável** — preparado para o replay determinístico
(AOS-016) consumir o cursor (o `next` é uma função pura da fase + pendentes). Como os
checkpoints vivem no ES replicado, um worker **novo** reconstrói o cursor após
**failover** (análogo de leitura do `StepLedger.Rebuild`).

---

## Observabilidade e segredos

- `Observer` recebe contadores `Applied`/`Deduplicated` com a forma **opaca** (hash)
  da chave — **nunca** a chave em claro nem o resultado. Default `NopObserver`.
- O `Result.Payload` é persistido **em claro** no Event Store (fonte de verdade, não é
  "log"; o cifrado por-titular do ES é dívida de EPIC-13). Para resultados
  **sensíveis**, passe uma **referência** (hash/URI) em vez dos bytes em claro — por
  defeito o ledger persiste o `Payload` tal como o recebe (redacção a cargo do chamador).
- **Guarda opt-in** `WithSensitiveResults()`: quando activa, `Apply` **recusa**
  (`ErrClearResultInSensitiveMode`) memorizar um `Payload` não-vazio que não esteja
  marcado como `Result.Reference` — impondo, ao nível do módulo, que resultados
  sensíveis passem por referência. Consumidores AOS-021 com resultados de tool calls
  sensíveis devem activá-la.

---

## Liveness por lease/heartbeat + fencing tokens (AOS-018)

A liveness distribuída **não** é decidida por PID (falha silenciosamente cross-host):
é decidida por **lease com TTL renovável por heartbeat** sobre um **relógio injectável**
(`Clock`), com **fencing tokens** monotónicos que invalidam escritas de um worker
obsoleto. Ficheiros: `lease.go`, `fencing.go`.

### `LeaseManager` — a autoridade

| Método | Semântica |
|---|---|
| `Claim(ctx, runID) (Lease, error)` | Reclama um run **livre** (nunca reclamado ou com o lease expirado) e minta um `Lease{Token, TTL, ExpiresAt}` com token **estritamente monotónico**. Lease vivo detido → `ErrLeaseHeld`. |
| `Heartbeat(ctx, lease) (Lease, error)` | Renova o TTL **se** o lease for o corrente e não tiver expirado. Superado por novo claim → `ErrLeaseSuperseded`; TTL esgotado → `ErrLeaseExpired`. Não minta novo token. |
| `CurrentToken(ctx, runID) (FencingToken, error)` | Token corrente do run (0 se nunca reclamado). Serve de `TokenSource` ao enforcement. |

**Origem e durabilidade do contador.** O token vive no **stream de lease do run**
(`lease:<run_id>`) do Event Store. Cada claim faz `Append(lease.claimed{token})` com
`WithExpectedSeq` — a **concorrência optimista** de AOS-002. Dois claims concorrentes
competem no mesmo slot: um vence, o outro é rejeitado (`ErrSeqConflict`/
`ErrAppendOnlyViolation`), **relê** e obtém um token **estritamente maior**. Não há
contador só em memória — é reconstruível por replay do log replicado.

### `FencedAppender` — o enforcement **opt-in**

`Append(ctx, runID, token, in, opts...)` escreve ao stream do run **apenas** se
`token >= corrente`; um token inferior (worker superado por novo claim) ou ausente/0 é
**rejeitado** com `ErrStaleFencingToken` **sem tocar no Event Store**. Com uma
`LeaseExpiryAuthority` (que o `LeaseManager` satisfaz) também rejeita a escrita de um
detentor cujo **lease expirou** por ausência de heartbeat (janela expirado-mas-não-superado).

> **Alcance honesto.** O fencing é um **guard opt-in**: só protege as escritas
> **efectivamente encaminhadas** pelo `FencedAppender` (padrão `Claim → token →
> Append`). Não está ligado aos caminhos internos do módulo — `StepLedger`,
> `EventStoreCheckpointer` e a máquina de estados (AOS-017) persistem **directo** no
> Event Store. Nesses caminhos, o que impede duplicados **hoje** é a **dedup por
> `idempotency_key`** do ES (`StatusDuplicate`) **mais** a idempotência **downstream** —
> não o fencing. São camadas **complementares**.
>
> **Limite conhecido (TOCTOU).** O token é consultado externamente e **não** é dobrado
> no CAS do evento de negócio. AOS-018 fecha, provado, o caso token-**estritamente-
> inferior**; o boundary token-**igual** sob concorrência real fica delegado ao CAS
> durável do Event Store de produção (`expected_seq`). Ver
> `TestFencingTOCTOUWindowBoundary`.
>
> **Durabilidade.** A persistência real e a monotonicidade **cross-host** herdam do
> backend do Event Store (o reference impl é in-memory): os testes de restart/failover
> provam **reconstrução-a-partir-do-log**, não persistência através da morte do processo.

### Ligação ao claim `ready → running` (AOS-017)

`FencingToken` é um `uint64` com `Valid()`/`Value()`, satisfazendo **estruturalmente**
o contrato `state.FencingToken`. O token mintado por `Claim` alimenta directamente
`Machine.Transition(ready → running)` — **sem** o pacote `durable` importar `state`.

Por omissão a máquina valida **só a presença/validade** do token no claim (contrato
mínimo de AOS-017). Para recusar também um token **obsoleto** (worker superado) na
própria transição, ligue uma autoridade de staleness com `state.WithFencingAuthority`
(o `LeaseManager` adapta-se a ela — ver `TestMachineRejectsStaleClaimWithAuthority`).

### Contrato partilhável com o Escalonador (EPIC-03)

`LeaseAuthority` (`Claim`/`Heartbeat`/`CurrentToken`) e `TokenSource` (`CurrentToken`)
são as interfaces reutilizáveis: o SCH (AOS-025..034) partilha o **mesmo** token
monotónico e a **mesma** autoridade de expiração, sem duplicar o mecanismo.

---

## Contrato para consumidores

| Ticket | Como consome |
|---|---|
| **AOS-016** (replay) | `Rebuild` reconstrói do log; `ResumePoint` (cursor estável) arranca de qualquer `step_id`; `step_id` idêntico entre execução e replay (lê inputs do log, não regenera). |
| **AOS-020** (sagas) | Cada compensação é um `Apply` idempotente; regista a acção inversa a par do resultado. |
| **AOS-021** (activities) | Toda a activity corre dentro de `Apply`, derivando a key de `IdempotencyKey`/`SubKey` e propagando-a ao downstream. |
| **AOS-022** | Contadores `apply`/`dedup` via `Observer`; reconciliação a partir do ledger reconstruído. |
| **EPIC-03** (Escalonador, AOS-025..034) | Reutiliza `LeaseAuthority`/`TokenSource` (AOS-018): mesmo token monotónico e mesma autoridade de expiração, sem duplicar o mecanismo. |

---

## Testes

`go test ./durable/... -race -count=1 -covermode=atomic` cobre:

- determinismo **e** ausência de colisão da chave (incl. adversarial `a:bc`/`ab:c` →
  rejeitado por delimitador);
- idempotência: reexecução ⇒ 0 efeitos duplicados, resultado idêntico, `effect` não
  corre 2.ª vez;
- fault-injection: crash **antes** e **depois** do commit; downstream que honra a
  key regista 1× observável;
- integração: ledger persiste no Event Store real e sobrevive a reinício (`Rebuild`);
- `step_id` estável por passo lógico entre execução e retry/replay;
- corrida concorrente na mesma key (alvo do `-race`);
- **checkpoint/resume (AOS-015)**: recuperação com crash em **6 pontos distintos** de
  uma iteração multi-passo (retoma sem repetir os confirmados nem perder os
  pendentes); consistência `confirmed_step_id` ↔ `step_id` do ledger; a escrita de
  checkpoint **não muta** o prefixo cache-estável (comparação com run no-op);
  integração com o **loop real** + **failover** de worker (ES 3 réplicas, `Kill`/eleição
  + `Revive`/resync) preservando o cursor de retoma.
- **lease/fencing (AOS-018)**: ciclo claim → heartbeat → renovação → expiração (relógio
  injectável, sem sleeps); fencing rejeita escrita de token obsoleto (`ErrStaleFencingToken`)
  sem duplicação; **zero execução dupla** sob reatribuição com efeito de `step_id`
  **distinto** (isola o fencing da dedup do ES — só o fencing barra o efeito
  não-idempotente de A); rejeição de **detentor com lease expirado** (fail-open de
  liveness fechado); **boundary TOCTOU** com supersessão-durante-append forçada por store
  wrapper (prova o caso `<` e documenta o boundary `==`); staleness da **própria
  transição** ready→running via `state.WithFencingAuthority`; **observabilidade** dos
  eventos `lease.claimed`/`lease.renewed` lidos do stream de lease; concorrência (N
  workers competem → 1 vencedor) e retry de CAS que produz token **estritamente maior**;
  integração claim → token → `Transition(ready → running)` de AOS-017 → escrita fenced;
  auditoria de **ausência de decisão de liveness por PID**.
