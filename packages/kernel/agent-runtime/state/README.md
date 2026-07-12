# `state` — Máquina de estados durável do run (AOS-017)

Subpacote do Agent Runtime (`packages/kernel/agent-runtime`) que implementa a
**máquina de estados durável** do run: os dez estados canónicos, a tabela
declarativa de transições, a persistência append-only no Event Store, a
reconstrução por replay, a pré-condição de fencing token no claim e os timeouts
fail-closed. É a espinha dorsal de estados sobre a qual assentam a liveness por
lease/fencing (AOS-018), o Escalonador (EPIC-03) e o canal de steer (AOS-023).

Fonte de verdade: `specs/EPIC-02_…` §AOS-017; `tecnica/02` §5 (stateDiagram e
tabela); ADR-001 (durável), ADR-013 (gates de risco, timeout fail-closed).

Zero dependências externas. Assenta em `substrate/eventstore` (AOS-002) e reusa a
porta `Tracer` do Agent Runtime (AOS-013) para observabilidade.

## Os dez estados

| Família | Estados |
|---|---|
| **Activos** | `ready`, `running` |
| **Suspensos** (legítimos, retomáveis) | `waiting_on_tool`, `waiting_on_human`, `paused` |
| **Terminais / recuperação** | `complete`, `failed`, `compensating`, `killed`, `timed_out` |

Terminais **absorventes** (sem saída): `complete`, `killed`, `timed_out`. `failed`
é falha **recuperável** — a única saída é `→ compensating` (saga).

## Tabela declarativa de transições (13 pares)

A máquina é **dados** (`transitions.go` → `validTransitions`), não `if/switch`.
`IsValidTransition(from, to)` é a única fonte de verdade da validação.

```
ready            → running          (EXIGE fencing token válido — o claim)
running          → waiting_on_tool
waiting_on_tool  → running
running          → waiting_on_human
waiting_on_human → running
waiting_on_human → killed           (timeout fail-closed — ADR-013)
running          → paused
paused           → running
running          → complete | failed | timed_out
failed           → compensating
compensating     → ready
```

Os restantes 87 dos 100 pares da matriz 10×10 são inválidos e rejeitados com
`ErrInvalidTransition` **sem** tocar no estado persistido.

## API essencial

```go
m, _ := state.NewMachine(store, runID,
    state.WithClock(clk),                       // relógio injectável (timeouts determinísticos)
    state.WithHumanApprovalTTL(30*time.Second), // fail-closed do gate humano
    state.WithRunWallClock(10*time.Minute),     // timed_out do running
    state.WithTracer(tracer),                   // spans por transição (AOS-013)
    state.WithObserver(obs),                    // contadores
)

// Claim: EXIGE fencing token válido.
_ = m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)})
_ = m.Transition(ctx, state.WaitingOnTool, state.TransitionEvent{Reason: "http.get"})
_ = m.Transition(ctx, state.Running, state.TransitionEvent{}) // retoma: sem token
_ = m.Transition(ctx, state.Complete, state.TransitionEvent{})

m.Current()            // estado corrente
s, _ := m.Rebuild(ctx) // reconstrói do log (após crash / novo worker)

// Eventos expostos (accionados por AOS-023 steer / AOS-018 lease).
_ = m.Pause(ctx, ev)   // running → paused
_ = m.Resume(ctx, ev)  // paused → running
_ = m.Kill(ctx, ev)    // waiting_on_human → killed

// Timeouts fail-closed (chamado periodicamente pelo lease/Escalonador).
st, fired, _ := m.CheckDeadlines(ctx)
```

## Garantias

- **Durabilidade (ADR-001).** Cada transição válida é um evento append-only
  `run.state.transition` no Event Store replicado, com `step_id` namespaced
  `state-N` (domínio de dedup distinto de turno/ledger/checkpoint).
- **Reconstrução por replay.** `Rebuild(run_id)` adopta o `to` do evento de seq
  mais alto — **sobrevive a crash**. Stream vazio → `ready`. Estado desconhecido no
  log → `ErrUnknownState` (fail-closed).
- **Não-corrupção.** O estado in-memory só avança **após** o commit durável: uma
  transição inválida ou uma falha do Event Store deixa o estado (persistido e
  in-memory) intacto.
- **Fencing token (contrato AOS-018).** Só `ready → running` o exige. AOS-017
  define o contrato (`FencingToken`/`Uint64Token`) e verifica presença/validade; a
  origem monotónica durável, o heartbeat e a rejeição de token inferior são AOS-018.
- **Timeout fail-closed (ADR-013).** `waiting_on_human` há ≥ TTL → `killed` (nunca
  `running`); `running` há ≥ wall-clock → `timed_out`. Relógio injectável (`Clock`)
  → testes determinísticos sem sleeps.
- **Observabilidade.** Um span por transição (porta `Tracer` de AOS-013) +
  `TransitionObserver` (contadores). Sem segredos nos atributos.

## Âmbito (o que NÃO está aqui)

- Lease/heartbeat e a origem monotónica do fencing token → **AOS-018**.
- Canal de steer (a decisão de pausar/retomar) → **AOS-023**.
- Execução da saga de compensação (reprodução inversa do log) → **AOS-020**. O
  estado `compensating` existe; a saga em si é do ticket respectivo.

## Testes

- `transitions_test.go` — matriz **10×10** completa (cada par válido aceite, cada
  inválido rejeitado; oráculo independente da tabela), classificação de famílias,
  fencing só no claim.
- `machine_test.go` — sequência realista persistida + reconstruída
  (`ready → running → waiting_on_tool → running → complete`); fail-closed
  `waiting_on_human → killed`; `timed_out` por wall-clock; recuperação após crash;
  transição inválida e falha de Append não corrompem; pause/resume/kill;
  concorrência `-race`.
- `bench_test.go` — benchmarks (`IsValidTransition`, `Transition`) + cobertura de
  opções/acessores/tracer.

```
go test ./state/ -race -count=1 -covermode=atomic -coverprofile=cover.out
```

Cobertura do pacote: **92.1%** dos statements; `-race` limpo; `go vet` limpo.
