# activity — Isolamento de efeitos externos (AOS-021)

Pacote `activity` do módulo `agent-runtime`. Define o **contrato de activity** do
Agent Runtime do AOS: a unidade que **isola** todo o efeito externo (tool call / I/O /
rede) do batimento determinístico do loop.

> "O loop é o batimento cardíaco, mas cada efeito externo é uma *activity* durável,
> isolada, idempotente e mediada." — tecnica/02 §4, _FONTE_ (Fluxo de execução).

A activity separa a lógica **reproduzível** do loop do efeito **não-determinístico**
sobre o mundo externo — que tem de ser **mediado**, **registado** e **não re-executado**
no replay. Este ticket obriga a que todo o efeito externo passe pelo contrato — e,
através dele, pelo Reference Monitor.

## Não reimplementa — compõe

O `Dispatcher` é o **ponto de composição**; não reinventa nenhuma das fundações:

| Fundação | Reutilização |
|---|---|
| **Reference Monitor** (AOS-003, `../../reference-monitor`) | Mediação: o efeito é despachado por `rm.Mediate` **antes** de executar (identidade/política/orçamento/egress/audit). Sem caminho directo (ADR-002). |
| **Step-ledger + idempotency** (AOS-014, `../durable`) | Idempotência: `step_id` + chave `run_id:step_id`; `already-applied` **precede** o efeito; resultado memorizado (append-only no Event Store). Reexecutar **não duplica**. |
| **Replay** (AOS-016, `../replay`) | Em `ModeReplay` a activity devolve o resultado **registado** (`ReplaySource`), zero efeito. |
| **Taint** (ADR-005, `../taint.go`) | O resultado volta ao loop **sempre** marcado `untrusted`. |
| **Saga** (AOS-020, `../saga`) | Se a activity tiver compensação, é registada no `CompensationRegistry` pelo `step_id`, no momento do permit. |

## Fluxo (`ModeNormal`)

```
Dispatch(ctx, activity):
  1. key = run_id:step_id                         (AOS-014)
  2. ledger.Apply(key, effect):                   (already-applied ANTES do efeito)
       effect:
         3. dec = rm.Mediate(call)                (ADR-002 — única via de despacho)
            └─ deny/escalate → ErrMediationDenied  (ZERO efeito, nada memorizado)
         4. regista compensação (se houver)        (AOS-020)
         5. Result{Status, Payload: dec.Output}    (append-only no Event Store)
  6. return Untrusted(payload)                     (ADR-005)
```

Uma segunda chamada com a mesma key devolve o resultado memorizado com
`Result.Deduplicated=true`, **sem re-executar** o efeito.

## No-bypass ESTRUTURAL (não é convenção — é impossibilidade)

O efeito externo é uma **tool registada no RM**. A única via de a executar é
`rm.Mediate`, cujo dispatcher interno exige um *permit não-forjável* (campo
não-exportado, uso único — AOS-003). O `Dispatcher`:

- **nunca** detém uma função de efeito directamente invocável — só uma *descrição*
  (`Activity{ToolID, Input, …}`) que traduz num `referencemonitor.Call`;
- em `ModeReplay` sequer detém um `Mediator` (`rm == nil`): devolver o registo **não
  pode**, por construção, disparar um efeito.

O teste `TestDispatch_DenyNaoExecutaEfeito` prova-o: com um RM que nega, o contador de
execuções da tool fica em **0**. `TestDispatch_ReplayZeroEfeito` prova o zero-efeito em
replay.

## Separação (lint) — `activity/separation`

Analisador AST (stdlib `go/ast`, zero-dep) que **detecta um efeito externo directo**
(`http.Get`, `os.Open`, `exec.Command`, `net.Dial`, …) escrito na lógica do loop, **fora
de uma activity**. `testdata/good` (roteia tudo por `Dispatch`) → 0 violações;
`testdata/bad` (I/O directo) → sinalizado. O teste corre-o também sobre o próprio loop
determinístico (AOS-013) e exige **0** violações. É defesa-em-profundidade (segunda
camada); a garantia forte é estrutural (acima).

## Agnóstico ao engine (AOS-022)

As peças que o `Dispatcher` consome são **interfaces** — `Mediator`, `Ledger`,
`ReplaySource`, `CompensationRegistrar`. O adaptador de AOS-022 (Temporal / Restate /
DBOS **ou** o contrato próprio) satisfaz `Ledger`/`ReplaySource` sobre o seu backend
**sem alterar esta API**:

| Contrato de activity | Engine externo |
|---|---|
| `Activity` | activity/step (durável, input + step_id estável) |
| `Dispatcher` | o worker que executa a activity |
| `Ledger.Apply` | memoização exactly-once-observável por key |
| `ReplaySource` | event history / journal relido no replay |

Este pacote **não** implementa o engine externo — só o contrato e a composição sobre o
contrato próprio (AOS-014/016).

## API

```go
// Modo normal: medeia + memoriza.
d, _ := activity.NewDispatcher(rm, ledger,
    activity.WithCompensationRegistry(reg),
    activity.WithTracer(tracer),
    activity.WithObserver(obs))
res, err := d.Dispatch(ctx, activity.Activity{
    RunID: "run-1", StepID: "step-000001-tool-1",
    ToolID: "http.fetch", Capability: "cap:http.get",
    Input: payload,
    Compensation: &activity.Compensation{Action: undo},
})
// res.Output é SEMPRE untrusted.

// Modo replay: devolve o registado, zero efeito, sem RM.
r, _ := activity.NewReplayDispatcher(ledgerRebuilt)
got, _ := r.Dispatch(ctx, act) // got.Replayed == true
```

## Testes (todos `-race` limpos)

- **Mediação/no-bypass** — permit executa 1×; deny → `ErrMediationDenied`, contador 0.
- **Idempotência** — reexecução não duplica (dedup); concorrência colapsa em 1 efeito.
- **Replay** — devolve o registado, contador inalterado; `replay-miss` → `ErrReplayMiss`.
- **Separação** — lint apanha o caso mau, não o bom, e o loop real está limpo.
- **Observabilidade** — custo por span (`gen_ai.usage.cost_usd`), contadores, key opaca.
- **Compensação** — registada pelo `step_id`; sem registry → `ErrNoRegistry`.

Cobertura do pacote: **94%**.
