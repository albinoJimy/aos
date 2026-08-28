# Harness de replay/idempotência (AOS-024)

Harness **reutilizável** que, dado um **run gravado** (uma trajectória no Event
Store), verifica automaticamente — de forma repetível e determinística — as
propriedades não-negociáveis do Agent Runtime (`specs/01` §4, **gate 8**;
ADR-001, ADR-010):

| Verificação | O que faz | Falha quando |
|---|---|---|
| **(a) Replay determinístico** | Corre o motor de replay de **AOS-016** ([`replay.ReplayEngine`]) sobre a trajectória; suporta `resume-from-step`. | Algum passo diverge (prompt_hash, modelo/seed, versão do assembler ou sequência de step_id). |
| **(b) Idempotência por passo** | Reexecuta cada passo com efeito sob um calendário *at-least-once* com **crash intercalado** (ledger reconstruído do log), via o **step-ledger de AOS-014** ([`durable.StepLedger`]). | Um efeito observável corre mais do que uma vez (ex.: idempotency key não-determinística). |
| **(c) Fault-injection** | Retoma a partir de pontos de crash configuráveis ([`FaultPoint`]) e compara o estado reconstruído. | A retoma não reproduz o mesmo estado/desfecho que o replay completo. |
| **(d) Âncora de desfecho** | Compara o desfecho reconstruído contra um esperado vindo de **fora do log** ([`Case`].`ExpectedFinalText` / `ExpectedFinalStateHash`). Opcional: vazio ⇒ sem verificação. | O desfecho gravado não bate com a âncora. |

> **Reutiliza, não reimplementa.** O harness **orquestra e afere** peças já
> Done: o replay (`agent-runtime/replay`, AOS-016) e o ledger
> (`agent-runtime/durable`, AOS-014). Não contém lógica de replay nem de ledger.

## Relatório de fidelidade

Cada verificação emite um [`FidelityReport`] (por run) / [`AggregateReport`] (por
golden set) com serialização **JSON estável** (structs de ordem fixa, sem mapas —
os mesmos inputs dão sempre os mesmos bytes), consumível pelas métricas do backlog
(`replay-fidelity`, efeitos duplicados; `specs/01` §9):

```json
{"name":"echo-3turns","run_id":"golden_echo_3turns","turns":3,"replay_fidelity":1,
 "diverged":false,"effects_verified":2,"duplicated_effects":0,
 "resume_points_verified":2,"resume_mismatches":0,"pass":true}
```

`FidelityReport.Err()` converte um relatório falhado num erro *fail-closed* — é o
que o gate de CI e os testes usam para transformar uma quebra de fidelidade/
idempotência/retoma num `exit != 0`.

## Golden trajectories / fixtures

Trajectórias de referência **determinísticas e versionadas**, construídas por
builders Go que correm o loop real de AOS-013 sobre um Event Store em memória com
**relógios injectados** (nunca `time.Now`) e um **modelo guionado** (nunca
random) — logo **reprodutíveis entre execuções** e reutilizáveis por outros epics:

- [`BuildEchoGolden`] — `echo-3turns`: 3 turnos, 2 com tool call despachada via
  Reference Monitor real, o 3º final. Dois passos de efeito + dois pontos de crash.
- [`BuildImmediateFinalGolden`] — `immediate-final`: 1 turno, resposta directa.
- [`GoldenSet`] / [`GoldenReport`] — o conjunto completo + o relatório agregado.

## Uso local

```go
import "github.com/aos-ref/kernel/agent-runtime/harness"

// Verificar o golden set completo:
agg, closer, err := harness.GoldenReport(ctx)
defer closer()
if err := agg.Err(); err != nil { /* fail-closed */ }

// Verificar uma trajectória própria:
rep, err := harness.Verify(ctx, harness.Case{
    RunID:  "meu_run",
    Reader: meuEventStore,          // só-leitura (replay.EventReader)
    Spec:   spec,                   // inputs determinísticos re-fornecidos
    LedgerStore: meuStoreLedger,    // Append+Read (durable.EventStore)
    Effects: []harness.Effect{ /* passos com efeito a exercitar */ },
    Faults:  []harness.FaultPoint{{AtStepID: "step-000002"}},
})
```

Correr os testes localmente (mesmo ambiente do gate):

```bash
cd packages/kernel/agent-runtime
go test ./harness/... -race -count=1 -covermode=atomic -coverprofile=cover.out
go tool cover -func=cover.out | tail -1
```

## Gate de CI (gate 8, fail-closed)

`scripts/ci/replay.sh` corre o harness sobre as golden trajectories via `go test`
(usando `require_tests` de `lib.sh` para **não passar vazio**), emite o relatório
de fidelidade e é **fail-closed**: uma trajectória divergente ou um efeito
duplicado torna o gate **vermelho**. Ligado a `scripts/ci/run.sh` (`ALL_GATES`),
ao `Makefile` (`make ci-replay`) e ao `.github/workflows/ci.yml` (job `replay`,
agregado em `gates`). O `scripts/ci/selftest.sh` (secção D) prova que uma
trajectória adulterada bloqueia o gate.

## Meta-testes (a prova de que o harness FUNCIONA)

- `TestHarnessDetectsTamperedTrajectory` — o harness **apanha** deriva do CÓDIGO
  (system/objectivo/tools/seed/assembly) e reprova, com a divergência localizada no
  passo exacto. O nome diz «trajectória adulterada», mas o que estes cinco casos
  mutam é a **spec** — o lado do código —, nunca o log. A distinção importa porque
  são ameaças diferentes: deriva de código, que isto cobre, e adulteração do
  registo, que só é apanhada quando o troço adulterado alimenta o prompt de um turno
  seguinte (ver a âncora de desfecho, para o caso em que não alimenta).
- `TestHarnessDetectsDuplicatedEffect` — o harness **apanha** um efeito duplicado
  injectado (idempotency key não-determinística) e reprova.
- `TestFixturesReproducible` — as fixtures produzem relatórios **byte-idênticos**
  entre execuções (`-count` alto).
- `TestFaultInjectionResume` — pontos de crash retomam no **estado correcto**.
- `TestFidelityReportEmitted` — o relatório é emitido, estável e consumível.
- `TestAncoraDeDesfecho_TextoFinalAdulteradoEhApanhado` — o harness apanha um run cujo
  **texto final** foi adulterado no log. Nasceu vermelho: (a), (b) e (c) não o viam.

## Alinhamento com EPIC-11

AOS-024 entrega a **fundação transversal** (harness + fixtures + gate 8). As
suites AOS-111 (replay) e AOS-112 (idempotência) de EPIC-11 **consomem** este
harness e as suas fixtures — não reimplementam a mecânica. O **eval harness** de
comportamento (AOS-114, golden-sets curados + `gen_ai.evaluation.result`) é um
aparelho **separado e complementar**: foco distinto (replay/idempotência vs
golden-set de comportamento). Este pacote não lhe toca.

[`replay.ReplayEngine`]: ../replay
[`durable.StepLedger`]: ../durable
[`FidelityReport`]: ./replay_idempotency.go
[`AggregateReport`]: ./replay_idempotency.go
[`FaultPoint`]: ./replay_idempotency.go
[`Case`]: ./replay_idempotency.go
[`BuildEchoGolden`]: ./fixtures.go
[`BuildImmediateFinalGolden`]: ./fixtures.go
[`GoldenSet`]: ./fixtures.go
[`GoldenReport`]: ./fixtures.go
