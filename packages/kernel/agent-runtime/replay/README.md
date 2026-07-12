# replay — Replay determinístico resume-from-step (AOS-016)

Motor de **replay determinístico** do Agent Runtime. Reconstrói uma trajectória a
partir do Event Store reproduzindo exactamente as mesmas transições — **sem**
re-chamar o modelo, **sem** despachar tools ao vivo, **sem** escrever no log — e
valida por **hash de prompt por turno**, localizando o passo exacto de qualquer
divergência. É a base do RCA e do *eval-driven development* (Dimensão 4).

Subpacote do módulo `agent-runtime` (sem `go.mod` próprio). Zero dependências
externas.

## As duas metades

### 1. Captura de não-determinismo (`nondeterminism_capture.go`)

O loop base (AOS-013) grava `turn.recorded` com o **manifesto** (`prompt_hash`,
`model{id,params,seed}`, versões pinadas) mas **não** os inputs crus. Sem eles o
replay detecta divergência mas não reconstrói. `EventStoreCapturer` fecha o gap:
implementa o ponto de ligação `agentruntime.Capturer` e persiste, por turno, um
evento **`replay.captured`** com:

- a **resposta do modelo completa** — texto + tool calls pretendidas + uso + custo + `final`;
- o **resultado de cada tool call** — output *untrusted* + eventual erro de execução;
- o **relógio de captura** (`observed_at`, carimbo do capturer).

O **seed** não é duplicado: vem do manifesto por trajectória (`turn.recorded →
model.seed`). Liga-se ao loop de forma **aditiva**:

```go
capturer, _ := replay.NewCapturer(store)                       // ou WithClock / WithSensitiveResults
rt := agentruntime.New(model, rm, recorder,
    agentruntime.WithCapturer(capturer))                       // sem isto, AOS-013 é byte-idêntico
```

O envelope usa o step_id namespaced `cap-<step_id>` — o **quarto** domínio de dedup
por passo, distinto de `turn.recorded` (`run_id:step_id`), do ledger
(`run_id:ledger-…`, AOS-014) e do checkpoint (`run_id:ckpt-…`, AOS-015). A escrita
é idempotente (uma re-captura dá `StatusDuplicate`).

**Segredos.** `WithSensitiveResults()` persiste apenas uma **referência**
(`sha256`), nunca a PII em claro — análogo a `durable.WithSensitiveResults` de
AOS-014. A guarda cobre **todo** o não-determinismo PII-portador do turno, não só
os outputs: o **output** de cada tool, o **texto** da resposta do modelo (que ecoa
dados) e o **input**/`resource_value` de cada tool call (ex.: corpo/destinatário de
um `send_email`). `resource_type`/`region` mantêm-se (estruturais). O replay de um
turno sensível reconstrói marcadores de referência, não o valor original — o modo
sensível troca fidelidade byte-a-byte por confidencialidade, por desenho.

### 2. Motor de replay (`engine.go`)

`ReplayEngine.Replay(ctx, run_id, opts)`:

1. **Lê** o stream do run do Event Store (`turn.recorded` + `replay.captured`).
2. **Re-materializa** o prompt de cada turno com o **mesmo** `PromptAssembler` e a
   **mesma** construção de tail do loop (funções exportadas `TailFromModelText` /
   `TailFromToolResult`), semeando o tail com `System`/`Tools`/`Objective`/
   `MemoryContext` da `TrajectorySpec`.
3. **Compara** o `prompt_hash` re-materializado com o gravado no manifesto.
4. **"Chama"** o modelo via um cliente de replay que devolve a resposta **registada**
   (nunca ao vivo) e **"despacha"** cada tool via um dispatcher que devolve o
   resultado **registado** (nunca executa efeitos).

```go
engine, _ := replay.NewEngine(store)                           // store: só é usado o Read
res, _ := engine.Replay(ctx, runID, replay.Options{
    Spec:       replay.TrajectorySpec{System: sys, Tools: tools, Objective: obj, MemoryContext: mem},
    FromStepID: "step-000002",                                 // opcional: resume-from-step
})
// res.Fidelity == 1.0, res.Divergence == nil  ⇒ replay fiel
```

## Fonte dos inputs (nunca ao vivo)

| Input não-determinístico | Fonte no replay |
|---|---|
| Resposta do modelo (texto + tool calls) | evento `replay.captured` |
| Resultado de cada tool call | evento `replay.captured` |
| Seed | manifesto (`turn.recorded → model.seed`) |
| Relógio | carimbo de captura (`replay.captured → observed_at`) |

Os inputs **determinísticos** (system prompt, tool set congelado, objectivo,
memory_context) são re-fornecidos via `TrajectorySpec` — são código/config,
re-materializados pelo assembler. **Alterá-los simula a evolução de código**: o
`prompt_hash` re-materializado diverge do gravado e o motor **localiza o passo**.

## Detecção de divergência

Se o `prompt_hash` re-materializado ≠ o gravado (ou, com `Options.StepIdentity`, se
a sequência de step_ids divergir), `Replay` devolve `ReplayResult.Divergence` com
`{ StepID, Turn, ExpectedHash, ActualHash, Reason }` — o **passo exacto** — e pára
aí. `Fidelity < 1.0` sinaliza replay infiel.

## Resume-from-step

`Options.FromStepID` arranca de qualquer step_id: o motor **dobra** os turnos
anteriores a partir do log (zero efeitos) para reconstruir o estado (o tail) e
começa a verificar/emitir a partir do ponto de retoma. `FinalStateHash` é
**idêntico** entre um replay completo e um resume do mesmo run — a prova de que o
resume produz o mesmo estado. Alinha-se com o cursor de AOS-015: o
`ResumePoint.NextStepID` de um `durable.Resumer` pode ser passado directamente como
`FromStepID`.

## Zero efeitos externos (garantia estrutural)

O `ReplayEngine` detém **apenas** um `EventReader` (só `Read`) e um `Tracer`. **Não
tem** `ModelClient`, **não tem** Reference Monitor, **não tem** registo de tools,
**não tem** `Append`. Não existe, por construção, caminho para um efeito externo em
modo replay. Provado por teste comportamental (o contador de execuções de tool não
mexe no replay; o nº de eventos do stream não cresce) e por teste estrutural (o
struct não detém campos de efeito ao vivo).

## Observabilidade (ADR-010)

`WithTracer` faz o motor emitir um span **`replay`** com `aos.replay.fidelity`,
`aos.replay.from_step`, `aos.replay.diverged` e `gen_ai.evaluation.result`
(`pass`/`fail`), ligado ao trace original por `aos.run_id` — base do eval e do RCA
(`tecnica/08`). Sem SDK OTel real (porta leve; o adaptador é EPIC-08).

## Testes

`go test ./... -race -covermode=atomic` (o `-race` exige `CGO_ENABLED=1` + gcc).
Cobre: replay 100% (mesma sequência de step_ids, hashes coincidem); captura
completa (modelo/tools/relógio/seed lidos do log); zero efeitos externos
(comportamental + estrutural); divergência injectada localizada no passo exacto;
resume de step_id intermédio com o mesmo estado. Cobertura do pacote ≥ 90%.
