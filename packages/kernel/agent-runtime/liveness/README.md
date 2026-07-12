# `liveness` — Espera legítima sem colidir com detecção de zombi (AOS-019)

Pacote `github.com/aos-ref/kernel/agent-runtime/liveness`. Subpacote do módulo
`agent-runtime` (sem `go.mod` próprio). **Zero dependências externas.**

Resolve a colisão do plano-base em que um gate humano (`waiting_on_human`) parado
horas parecia um worker `running` **pendurado** e a detecção de zombi o matava. Este
pacote garante que os **estados de espera nunca são classificados como zumbi**,
**preservando** o timeout fail-closed do gate humano (ADR-013).

## O cerne: DOIS relógios distintos

| Relógio | Origem | Governa | Nos estados de espera |
|---|---|---|---|
| **Trabalho activo** | heartbeat/lease de AOS-018 (`durable`) | liveness de `running` | **PAUSA** — não é renovado, mas **não** conta como expirado-para-zumbi |
| **Espera** | `WaitingGate` (este pacote) | TTL de aprovação do gate humano | corre; excedido → `killed` fail-closed |

O plano-base confundia os dois num só relógio de parede, e por isso um gate legítimo
disparava a detecção de zombi. AOS-019 separa-os.

## Componentes

### `ZombieClassifier`

Puro e determinístico. `Classify(ctx, RunLiveness{State, WorkLeaseExpired, GateDeadlineExceeded})`
devolve uma `Classification`:

| Estado | Condição | Classificação | Acção |
|---|---|---|---|
| `waiting_on_human` / `waiting_on_tool` / `paused` | — (mesmo com lease de trabalho **expirado**) | `WaitingLegitimate` | nenhuma — suspensão legítima |
| `waiting_on_human` | gate TTL **excedido** | `GateExpired` | **matar** o run (fail-closed, ADR-013) |
| `running` | lease de trabalho **expirado** | `Zombie` | **reatribuir** (fencing/novo claim) — o run continua vivo |
| `running` | lease vivo | `Alive` | nenhuma |
| `complete` / `killed` / `timed_out` | — | `Terminal` | nenhuma |
| `ready` / `failed` / `compensating` | — | `Alive` | nenhuma |

**Invariante não-negociável:** um estado de espera legítima é **NUNCA** `Zombie` (mesmo
com o lease de trabalho expirado); um worker realmente preso em `running` com o lease
expirado **É** `Zombie` (não-regressão). `Zombie` ≠ matar o run: é **reatribuição**
(o run segue com outro worker). Só `GateExpired` mata o run.

### `WaitingGate` — relógio de espera fail-closed

TTL PRÓPRIO do gate humano, **distinto** do heartbeat de trabalho. `Exceeded(enteredAt)`
usa a **mesma fronteira inclusiva** (`now >= enteredAt + TTL`) que
`state.Machine.CheckDeadlines` de AOS-017 — a decisão do gate e a transição durável
`waiting_on_human → killed` **concordam**. Relógio injectável (`WithGateClock`).
Construção fail-closed: `NewWaitingGate(ttl<=0)` → `ErrInvalidGateTTL` (um gate sem TTL
não fecha).

### `WorkClock` — contrato de exclusão para o circuit breaker (EPIC-08)

Acumula **só** o tempo em `running`; o tempo em espera **não conta**. O breaker
multi-sinal de EPIC-08 consome `ActiveWork()` como o seu sinal de wall-clock/ausência-
de-progresso de **trabalho**, garantindo que uma espera humana longa nunca é lida como
"sem progresso". Predicados: `CountsAsActiveWork(s)` (só `running`) e `IsWorkPaused(s)`
(os estados de suspensão). **Só a exclusão** vive aqui; o breaker completo é EPIC-08.

### `RunLivenessFrom` — integração aditiva

Compõe a `RunLiveness` a partir da Machine (AOS-017), do lease (AOS-018) e do gate
deste ticket, **sem os acoplar nem os quebrar**. `GateDeadlineExceeded` só é derivado
para `waiting_on_human` (o gate humano é específico desse estado).

## Uso

```go
clk := myClock // injectável
gate, _ := liveness.NewWaitingGate(1*time.Hour, liveness.WithGateClock(clk))
classifier := liveness.NewZombieClassifier()

// Periodicamente, por run:
rl := liveness.RunLivenessFrom(m.Current(), workLeaseExpired, gate, m.EnteredAt())
switch classifier.Classify(ctx, rl) {
case liveness.Zombie:            // worker preso → reatribuir (novo claim, fencing)
case liveness.GateExpired:       // gate excedido → m.CheckDeadlines(ctx) mata fail-closed
case liveness.WaitingLegitimate: // espera legítima → nada a fazer
}

// Sinal de exclusão para o breaker (EPIC-08):
wc.Observe(m.Current())          // a cada transição
active := wc.ActiveWork()        // exclui todo o tempo de espera
```

## Critérios de aceitação (AOS-019) → testes

| Critério | Teste |
|---|---|
| Espera legítima não é zombi (mesmo com lease expirado) | `TestClassify`, `TestClassifyExhaustiveSuspendedNeverZombie` |
| Breaker exclui o tempo de espera de "sem progresso" | `TestWorkClockExcludesWaitingTime`, `TestWorkClockSignalPredicates` |
| TTL do gate → `killed` fail-closed | `TestWaitingGateExceededBoundary`, `TestCombinedWaitLongThenGateKill` |
| **Combinado (crit. 4):** 100% do TTL não é zombi MAS excede o gate → killed | `TestCombinedWaitLongThenGateKill`, `TestWaitFullTTLNeverZombieWhileGateEnforced` |
| Não-regressão: `running` preso continua zombi | `TestClassifyRunningStuckIsZombie` |
| `waiting_on_tool` e `paused` cobertos | `TestClassify`, `TestClassifyExhaustiveSuspendedNeverZombie` |

## Fronteiras (fora de âmbito)

- A transição durável `waiting_on_human → killed` é de `state.Machine.CheckDeadlines`
  (AOS-017); este pacote **decide**, a Machine **materializa**.
- A origem monotónica do lease/fencing e a expiração do lease de trabalho são de
  AOS-018 (`durable`).
- O circuit breaker multi-sinal completo (avaliação, *trip*, alerta) é **EPIC-08**;
  aqui só a **exclusão** dos estados de espera.

Ver `tecnica/02` §6 (Liveness e fencing) e `tecnica/08` §6 (Circuit breaker multi-sinal).
