# Reference Monitor (RM / PEP) — AOS-003

Implementação do **Reference Monitor** do AOS: o *Policy Enforcement Point*
(PEP) e a primeira fundação não-negociável (ADR-002). Nenhum caminho de código
chama uma tool directamente — **toda** a tool call atravessa
[`Monitor.Mediate`](./monitor.go), o único ponto por onde uma acção externa é
autorizada e despachada.

Módulo: `github.com/aos-ref/kernel/reference-monitor` (Go 1.24, sem dependências
externas). O Event Store (AOS-002) é integrado por *path* local via `replace` no
`go.mod` — build offline.

## Superfície única

```go
d, err := rm.Mediate(ctx, call)   // autoriza E despacha; nada executa fora daqui
```

`Mediate(ctx, call) (Decision, error)` executa a cadeia de hooks pela ordem
determinística **identity → policy → budget → egress → audit** e, só se todos
permitirem, grava o evento de mediação e despacha a tool pelo *dispatcher*
interno. As negações de política são comunicadas em `Decision.Effect`
(`permit` / `deny` / `escalate`), não via `error` (o `error` fica reservado a
cancelamento de contexto).

## As três propriedades clássicas

| Propriedade | Como é garantida aqui |
|---|---|
| **Sempre invocado** | O Agent Runtime não tem via directa para o substrato; `Mediate` é a única saída. Uma tool não registada é negada (default-deny). |
| **Inviolável** | A execução exige um [`Permit`](./decision.go) não-forjável — `struct` com campo não-exportado (`permitToken`), ligado ao *fingerprint* do call e de **uso único**, que só este pacote consegue mintar dentro de `Mediate`. Código externo não constrói um `Permit` válido nem alcança o *dispatcher*. |
| **Verificável** | Superfície pequena; política externalizada em hooks plugáveis; cada decisão (permit **e** deny/escalate) é registada no Event Store. |

## Fail-closed (ADR-002, contrato C1 — `tecnica/12` §4)

A ausência de um permit explícito é negação. Fazem `Mediate` devolver `deny`
(ou `escalate`), registar o evento e **não** despachar a tool:

- qualquer hook que devolva `deny`/`escalate`, um **erro** ou entre em **panic**;
- tool não registada (default-deny);
- **falha de auditoria no caminho de permit**: se o registo no Event Store
  falhar antes do efeito, a decisão degrada para `deny` — uma acção
  não-auditável não é permitida (ADR-002/010). O evento é gravado **antes** do
  despacho (*audit-before-effect*).

Em `deny`/`escalate` o registo é *best-effort* (o efeito já está bloqueado).

## Cadeia de hooks (contratos + stubs neutros)

`Hook` é um ponto de decisão plugável. No AOS-003 os cinco hooks são **stubs
neutros** com contratos estáveis; as implementações reais chegam noutros
tickets:

| Ordem | Hook | Stub | Implementação real |
|---|---|---|---|
| 1 | `IdentityStub` | resolve/valida o principal (neutro) | AOS-005 |
| 2 | `PolicyStub` | `permit` (placeholder documentado) | AOS-004 (PDP, policy-as-code) |
| 3 | `BudgetStub` | reserva orçamento (allow) | AOS-008 |
| 4 | `EgressStub` | allowlist egress (allow) | — |
| 5 | `AuditStub` | audit tamper-evident (no-op) | AOS-011 |

O `call` é partilhado por ponteiro para que a identidade resolva o principal e o
contexto propague aos hooks seguintes. As obrigações (`Obligation`) acumulam-se
e só são impostas se a decisão final for `permit`.

## No-bypass (duas camadas)

1. **Estrutural** — o `Permit` não-forjável + *dispatcher* não-exportado
   (ver acima). Um `Permit{}` zero (ou qualquer um construído fora do pacote)
   tem token `nil` e é rejeitado com `ErrInvalidPermit`.
2. **Lint de arquitectura** — o subpacote [`archlint`](./archlint) detecta, em
   `go/ast` puro, despacho directo de tools fora do RM (invocação directa de um
   valor `ToolFunc` ou chamada a um *dispatcher* directo). Corre como **teste
   Go** (`archlint_test.go`): falha o `go test` se houver violação. Inclui
   `testdata` com um caso **BOM** (usa `Mediate`) e um **MAU** (contorna o RM);
   o teste assevera que o caso mau é sinalizado.

   O `archlint` é uma **heurística de defesa-em-profundidade** (por nome/tipo
   textual), *não* uma prova: é contornável por renomeação. A garantia **forte**
   de no-bypass é a **estrutural** (camada 1). Para robustez real seria preciso
   análise com *type-info* (`go/types`) — fora do escopo AOS-003.

   > **CI:** o mecanismo (`go test`) está pronto e falha em violação, mas o
   > repositório ainda **não tem pipeline** que o invoque (não há
   > `.github/workflows` nem alvo `test`/`lint` no `Makefile` raiz, que só cobre
   > IaC). Fechar o *checkbox* «lint activa no CI» do DoD depende de um ticket de
   > **infra transversal** que corra `go test ./...` neste módulo — dependência
   > registada, fora do escopo de código do RM.

## Registo no Event Store (AOS-002)

Cada mediação grava um evento via a porta mínima [`EventSink`](./eventsink.go),
com adaptador `NewEventStoreSink` para `packages/substrate/eventstore`:

- `stream_id = run_id`; `idempotency_key = run_id:step_id`;
- tipo: `tool.call.mediated` (permit) / `tool.call.denied` (deny) /
  `tool.call.escalated` (escalate);
- *payload* com decisão, `tool_id`, `capability`, latência e principal — **sem
  segredos** (o `Input` da tool não é gravado).

## Overhead

Alvo: **p95 < 15 ms** para a avaliação de política/mediação **em memória**
(NFR-01; `specs/01` §4 — o overhead total composto por tool call, com admissão,
broker, egress e append, é outro orçamento). Medido: `BenchmarkMediate`
≈ **0,65 µs/op** (3 allocs/op); `TestMediate_P95Overhead` reporta p95-efectivo
≈ **0,7 µs**, ~4 ordens de grandeza abaixo do alvo. A medição autoritativa é o
benchmark (ns/op); o teste é um *smoke* barato.

## Observabilidade

Ganchos leves de contagem (`Metrics`: permits/denials/escalations) sem SDK OTel
(isso é EPIC-08).

## Correr localmente

```bash
# -race exige um compilador C (mingw)
export PATH="$HOME/scoop/apps/mingw/current/bin:$HOME/scoop/shims:$PATH"
export CGO_ENABLED=1

go vet ./...
go test ./... -race -count=1 -covermode=atomic -coverprofile=cover.out
go tool cover -func=cover.out | tail -1
go test -run '^$' -bench BenchmarkMediate -benchtime=200000x
```

## Ficheiros

| Ficheiro | Conteúdo |
|---|---|
| `doc.go` | Documentação do pacote (visão geral, garantias). |
| `call.go` | `Call`, `Principal`, `Resource`, `CallContext`, `fingerprint`. |
| `decision.go` | `Effect`, `Decision`, `Obligation`, `Permit` (token não-forjável). |
| `hooks.go` | Contrato `Hook` + stubs neutros + `DefaultHooks`. |
| `monitor.go` | `Monitor`, `Mediate`, `Register`, *dispatcher* interno, métricas. |
| `eventsink.go` | Porta `EventSink` + adaptador ao Event Store + `discardSink`. |
| `errors.go` | Sentinelas (`ErrInvalidPermit`, `ErrToolNotRegistered`, …). |
| `archlint/` | Analisador de no-bypass (`go/ast`) + `testdata` bom/mau. |

## Escopo (AOS-003)

Só o RM: contratos de hooks, stubs neutros, no-bypass (estrutural + lint),
registo no Event Store e benchmark. **Não** implementa PDP real, Vault,
orçamento distribuído nem egress real — esses chegam em AOS-004/005/008/011.
