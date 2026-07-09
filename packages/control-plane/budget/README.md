# budget — Orçamento hierárquico com reserva atómica (AOS-008, ADR-008)

`packages/control-plane/budget` implementa o **admission control** do AOS: o
orçamento por **árvore de execução**, denominado em **tokens E custo ($)**, com
**reserva atómica (compare-and-swap)** de débito **antes** de cada spawn/tool
call. Substitui o cap de delegação fixo e o contador partilhado com corrida
(over-spawn além do orçamento) por uma invariante estrutural: **a soma das
reservas dos filhos nunca excede o pai**, e **dois spawns concorrentes nunca
ultrapassam o limite** (0 overshoot).

Módulo: `github.com/aos-ref/control-plane/budget` · Go 1.24 · **zero dependências
externas** (só `reference-monitor` (AOS-003) e `eventstore` (AOS-002) por path
local).

## Modelo

- **Árvore.** Um nó raiz (`tree_id`) e sub-árvores (`parent_id`). Cada nó tem um
  `Limit` e dois contadores: `Reserved` e `Committed`.
  `Available = Limit − Reserved − Committed`.
- **Duas dimensões.** [`Amount`](amount.go) é `{Tokens, CostMicroUSD}` em
  **inteiros** (micro-dólares), **nunca float** — comparação exacta, sem
  corrida/arredondamento. Só há headroom se a reserva couber em **tokens E em
  custo**; nenhuma dimensão domina a outra. Orçamento em iterações é um proxy
  péssimo e não é usado.
- **Admissão hierárquica.** Uma reserva numa sub-árvore consome headroom em
  **todos os ancestrais até à raiz**. O pai é o tecto real, mesmo que o limite
  do filho seja mais generoso.

## API

```go
Reserve(ctx, nodeID, Amount) (Reservation, error) // débito CAS na cadeia
Commit(ctx, Reservation) error                    // Reserved → Committed
Release(ctx, Reservation) error                   // Reserved → Available (rollback)
```

- **`Reserve`** — sobe a cadeia de ancestrais debitando `Reserved` em cada nível
  **só se houver headroom nas duas dimensões**. O check-and-débito de cada nó é
  **indivisível** (mutex por nó): é o compare-and-swap real que garante 0
  overshoot. Se **qualquer** nível falhar, os níveis já reservados são
  **revertidos** (rollback parcial) e devolve-se `ErrNoHeadroom` (deny). Em
  sucesso emite `budget.reserved`; se o log durável falhar, a reserva é revertida
  por inteiro (fail-closed).
- **`Commit`** — converte `Reserved→Committed` (débito final) em todos os níveis.
  **Idempotente** por `reservation.ID`.
- **`Release`** — devolve `Reserved` a `Available` em todos os níveis.
  **Idempotente**. Uma reserva é **commit OU release exactamente uma vez**;
  `commit-após-release` → `ErrCommitAfterRelease`, `release-após-commit` →
  `ErrReleaseAfterCommit`. A transição é feita por CAS na máquina de estados
  (`pending → committed | released`): sob commit/release concorrentes, exactamente
  um vence e o débito aplica-se uma só vez. Reserva não consumida em
  falha/cancelamento é libertada **sem leak**.

## Integração com o Reference Monitor (AOS-003)

O adaptador [`BudgetCheck`](rmadapter.go) implementa o hook `budget` do RM (o
antigo `BudgetStub`). Por cada `Call`:

1. **estima** o custo (`CostFunc`; `DefaultEstimator` é um placeholder honesto —
   produção injecta a contagem de tokens do prompt materializado e a tarifa do
   provider);
2. aplica um **circuit breaker leve** (trip por custo/token acima de um limiar —
   `WithCircuitBreaker`; detalhe completo é EPIC-08);
3. **reserva** headroom no nó (`WithNodeSelector`, por omissão o `RunID`).

**Sem headroom → `HookDeny` (fail-closed)** e o RM **audita** a negação no Event
Store. A reserva feita em `Evaluate` fica **pendente**; o consumidor, consoante a
`rm.Decision` final, **confirma** (`Settle`/`Commit`, em permit) ou **liberta**
(`Settle`/`Release`, em deny/escalate/falha) — garantindo que reserva não
consumida não faz leak. Ver `rmadapter_test.go` (commit-em-permit,
release-em-deny-a-jusante, deny-por-falta-de-headroom com audit).

## Eventos e reconstrução (consistência com AOS-002)

Cada mutação autoritativa emite um evento no Event Store, com `tree_id`,
`reservation_id`, a cadeia de nós afectados e a `amount`:

| Evento | Efeito nos contadores |
|---|---|
| `budget.reserved`  | `Reserved += amount` em cada nó da cadeia |
| `budget.committed` | `Reserved −= amount`, `Committed += amount` |
| `budget.released`  | `Reserved −= amount` |

`stream_id = tree_id`; `idempotency_key = tree_id:<reservation>:<tipo>` (um
re-emit dedup no store). [`Rebuild(events)`](events.go) reconstrói os contadores
por nó **só a partir do log** (não precisa da topologia — os nós afectados vêm no
payload). O teste `TestRebuild_FromEventStore` prova que
`Rebuild == Budget.Snapshot()` sobre o Event Store real.

## In-memory vs distribuído (honesto)

Esta é a implementação de **referência in-memory** com **CAS real**, que torna o
**0-overshoot determinístico e testável sob `-race`**. É o **caminho rápido**; os
eventos são o **log durável e autoritativo**. Em **produção**, o backend é um
**token-bucket distribuído** (Redis/consenso) sobre o TPM/RPM real do provider
(ADR-008) — o seam [`Reserver`](budget.go) é o ponto de troca, **sem tocar no
RM**. O `Budget` in-memory é uma implementação de `Reserver` entre outras.

## Âmbito

**Dentro (AOS-008):** orçamento hierárquico + reserve/commit/release CAS +
`BudgetCheck` do RM + reconstrução por eventos + circuit breaker leve.
**Fora:** scheduler e backpressure completos (AOS-025+), token-bucket distribuído
real, e o detalhe do circuit breaker (EPIC-08).

## Testes e gates

```sh
export PATH="$HOME/scoop/apps/mingw/current/bin:$HOME/scoop/shims:$PATH"
export CGO_ENABLED=1   # -race exige o gcc do mingw
go vet ./...
go test ./... -race -count=1 -covermode=atomic -coverprofile=cover.out
go tool cover -func=cover.out | tail -1
```

- **`TestReserve_ConcurrentCAS_ZeroOvershoot`** — 200 goroutines contra o mesmo
  limite; admite **exactamente** o que cabe, `Reserved` nunca excede o limite em
  nenhuma dimensão (0 overshoot), sob `-race`.
- **`TestReserve_HierarchyConsumesAncestors`** / **`_RollbackOnAncestorFailure`**
  — reserva em sub-árvore consome ancestrais; falha num ancestral faz rollback
  sem débito residual.
- **`TestRelease_NoLeakOnCancellation`**, **`TestReservation_Idempotency`**,
  **`TestSettle_ConcurrentCommitRelease`** — sem leak; idempotência; erros de
  transição oposta; exclusão mútua sob concorrência.
- **`TestBudgetCheck_*`** — deny por falta de headroom (fail-closed + audit no
  RM), commit-em-permit, release-em-deny, circuit breaker, backend nil.
- **`TestRebuild_FromEventStore`** — reconstrução do estado a partir do Event
  Store real.
- **`BenchmarkReserve`** (+ paralelo e hierárquico) — overhead do caminho
  atómico.
