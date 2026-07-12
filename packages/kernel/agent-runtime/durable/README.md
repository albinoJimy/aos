# `durable` — Contrato de execução durável (AOS-014)

Subpacote de `agent-runtime` que implementa a **cláusula de idempotência por passo**
do ADR-001 (`tecnica/02` §4). É a fundação consumida por **AOS-015** (checkpoint),
**AOS-016** (replay), **AOS-020** (sagas), **AOS-021/022** (activities de efeito
externo).

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

## Contrato para consumidores

| Ticket | Como consome |
|---|---|
| **AOS-015** (checkpoint) | `step_id` estável de `StepSequencer`; `Applied(key)` para inspeccionar estado sem efeito. |
| **AOS-016** (replay) | `Rebuild` reconstrói do log; `step_id` idêntico entre execução e replay (lê inputs do log, não regenera). |
| **AOS-020** (sagas) | Cada compensação é um `Apply` idempotente; regista a acção inversa a par do resultado. |
| **AOS-021** (activities) | Toda a activity corre dentro de `Apply`, derivando a key de `IdempotencyKey`/`SubKey` e propagando-a ao downstream. |
| **AOS-022** | Contadores `apply`/`dedup` via `Observer`; reconciliação a partir do ledger reconstruído. |

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
- corrida concorrente na mesma key (alvo do `-race`).
