# Agent Runtime (RT) — loop base · AOS-013

O **Agent Runtime** é o batimento cardíaco do AOS (ver `tecnica/02_Agent_Runtime_Execucao_Duravel.md`). Cada iteração — *turno* — percorre quatro fases:

```
montar (prompt cache-estável) → chamar (Model Gateway) → despachar (tool calls via Reference Monitor) → verificar
```

até uma resposta final ou o esgotamento do tecto de turnos. Este pacote entrega **apenas o esqueleto do loop** (estimativa M): o loop, o prompt cache-estável, o *turn recorder*, o despacho via RM e os spans. A durabilidade ao nível do passo é acrescentada pelos tickets seguintes.

## Contrato do loop

`Runtime.Run(ctx, Goal)` recebe um objectivo com `RunID`, `Principal` (NHI + cadeia de delegação) e escopo, e devolve um `Result` (resposta final ou paragem por `MaxTurns`). Por cada turno:

1. **Montar** — o `PromptAssembler` remonta o prompt preservando um **prefixo IMUTÁVEL** (`system` + tool set congelado no run) e um **tail append-only** (`memory_context`, objectivo, histórico, resultados). O prefixo é **byte-idêntico** entre turnos com o mesmo tool set (ADR-009) — nunca é reordenado. O prompt materializado é hasheado (`prompt_hash = sha256:…`) por turno.
2. **Chamar** — a `ModelClient` (porta do Model Gateway) é invocada com a `PromptView` do turno, sob um span `chat`.
3. **Despachar** — cada tool call **pretendida** pelo modelo é traduzida num `referencemonitor.Call` e submetida a `Monitor.Mediate`. **Nenhuma** tool executa fora do RM.
4. **Verificar** — o resultado de cada tool volta ao loop **marcado untrusted** (`Tainted`, ADR-005) e é injectado no tail append-only. A terminação é um *stub* simples (a máquina de estados durável é AOS-017).

Cada turno grava um evento `turn.recorded` no Event Store com o **manifesto por trajectória** (ADR-010): `prompt_hash`, `system_hash`, `model{model_id,params,seed}`, `assembly_version` e as `tools`/`skills` pinadas. O `step_id` é distinto por turno (evita a deduplicação por `idempotency_key` do Event Store).

## Garantia estrutural de no-bypass (ADR-002)

O `Runtime` detém um `*referencemonitor.Monitor`, **nunca** uma `referencemonitor.ToolFunc`. Não existe qualquer via na API pública que execute uma tool sem passar por `Monitor.Mediate` — o dispatcher que executa tools é não-exportado no RM e o *permit* é não-forjável. A prova vive nos testes em duas camadas:

- **estrutural** (`nobypass_test.go`): reflexão confirma que nenhum campo/método do `Runtime` é (ou expõe) uma `ToolFunc`, e que detém um `*Monitor`;
- **sintáctica** (defesa-em-profundidade): reutiliza o `archlint` do RM (AOS-003) sobre o código-fonte do RT — zero invocações directas de tool / dispatchers reservados.

## Decisões de porta (o que é *stub* e porquê)

| Porta | Neste ticket | Real em |
|---|---|---|
| **Model Gateway** (`ModelClient`) | interface mínima, mockada nos testes | **EPIC-06** (routing, rate-limit, cache no provider) |
| **Observabilidade** (`Tracer`) | porta leve com atributos da **semconv OTel GenAI**; `NoopTracer` default + `RecordingTracer` de teste; **zero-dep** | **EPIC-08** (SDK OTel real — adaptador fino, os atributos já têm os nomes canónicos) |

Os atributos emitidos por span usam os nomes exactos da semconv (`gen_ai.operation.name`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`/`output_tokens`) mais o custo por span (`gen_ai.usage.cost_usd`, derivado do micro-USD inteiro). Operações: `invoke_agent` (run), `chat` (chamada ao modelo), `execute_tool` (mediação de tool).

## Hooks para tickets futuros (expostos, não implementados)

- `StepIdentity` — ponto de ligação de **AOS-014** (idempotency key = f(run_id, step_id)). Default: `step-000001`, `step-000002`, … O contrato determinístico ancorado na posição no log substitui o default.
- `Checkpointer` — ponto de ligação de **AOS-015** (checkpoint intra-iteração). Default no-op; as fases (`assembled`, `model_called`, `turn_recorded`, `dispatched`, `verified`) são os pontos de checkpoint.

Fora de âmbito e **não** implementados aqui: idempotência de passo (AOS-014), checkpoint durável (AOS-015), máquina de estados durável (AOS-017), activities (AOS-021).

## Layout

| Ficheiro | Responsabilidade |
|---|---|
| `doc.go` | contrato e escopo do pacote |
| `prompt.go` | `PromptAssembler` cache-estável, `PromptView`, `prompt_hash` |
| `model.go` | porta `ModelClient`, `ModelResponse`, `Usage`, `ToolInvocation` |
| `tracing.go` | porta `Tracer`, semconv GenAI, `NoopTracer`, `RecordingTracer` |
| `turn.go` | `TurnRecorder` + `Manifest` (evento `turn.recorded` no Event Store) |
| `taint.go` | `Tainted` / níveis de taint (ADR-005) |
| `hooks.go` | `StepIdentity` (AOS-014) e `Checkpointer` (AOS-015) — hooks |
| `loop.go` | `Runtime`, `Goal`, `Result`, `Run` (o loop) |
| `errors.go` | sentinelas |
| `*_test.go`, `bench_test.go` | testes table-driven determinísticos + benchmarks |

## Dependências

Zero dependências externas. Integração por `replace` local do Reference Monitor (AOS-003) e do Event Store (AOS-002) — build offline. Os pacotes referenciados **não** são alterados.

## Testes

```sh
export PATH="$HOME/scoop/apps/mingw/current/bin:$HOME/scoop/shims:$PATH"
export CGO_ENABLED=1   # -race exige o gcc do mingw
go vet ./...
go test ./... -race -count=1 -covermode=atomic -coverprofile=cover.out
go tool cover -func=cover.out | tail -1
```

Cobre os Testes Requeridos de AOS-013: prefixo byte-idêntico entre turnos (regressão de cache); no-bypass estrutural (chamada directa impossível); percurso e2e (montar→chamar→despachar→verificar) gravando N eventos de turno no Event Store real; spans `invoke_agent`/`chat`/`execute_tool` com `gen_ai.usage.*` e custo USD; resultados de tool marcados untrusted; manifesto por turno completo.
