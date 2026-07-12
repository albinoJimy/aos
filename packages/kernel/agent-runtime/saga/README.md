# saga — Sagas de compensação (AOS-020)

Pacote `saga` do módulo `agent-runtime`. Implementa a **saga de compensação** do
Agent Runtime do AOS: quando um passo falha *depois* de já ter produzido efeitos
parciais no mundo externo, a saga **desfaz** esses efeitos por **ordem inversa** e de
forma **idempotente**, devolvendo o sistema a um estado consistente antes do retry.

Fecha o gap que os *gates* deixavam aberto: os gates **previnem** efeitos indesejados,
mas nada faziam quando um efeito legítimo ficava a meio. A saga adiciona **recuperação**
onde antes só havia prevenção (tecnica/02 §7, ADR-001).

## Não reimplementa — compõe

A saga **reutiliza** duas fundações já *Done*, sem as reimplementar:

| Fundação | Reutilização |
|---|---|
| **Step-ledger** (AOS-014, `../durable`) | Idempotência das compensações: cada reversão corre dentro de `StepLedger.Apply` com uma chave de compensação **distinta**. `already-applied` precede o efeito ⇒ **0 reversões duplicadas**. `Rebuild` dá o conjunto de compensações já commitadas (crash-resume). |
| **Máquina de estados** (AOS-017, `../state`) | Transições duráveis `failed → compensating → ready`, válidas e reconstruíveis por replay. |

## Peças

- **`CompensationRegistry`** (`registry.go`) — mapeia `step_id → Compensation`
  **preservando a ordem de aplicação**. Cada activity com efeito reversível regista a
  sua acção inversa no momento em que aplica o efeito. `Register` é **idempotente por
  `step_id`** (re-registar não duplica nem desloca a posição) — o que torna o registo
  **reproduzível após crash**.
- **`SagaCoordinator`** (`compensation.go`) — `Compensate(ctx)` aciona
  `failed → compensating`, executa as compensações por **ordem inversa** (LIFO) via o
  ledger, e — em sucesso — transita `compensating → ready` para retry limpo.

## Chave de idempotência da compensação

```
comp_key = f(run_id, "comp-" + step_id)      # CompensationKey
```

É **distinta** da chave do passo original (`run_id:step_id`) e das demais famílias de
dedup (turno, ledger, checkpoint, transição de estado). "Aplicar o passo" e "compensar
o passo" são dois domínios de dedup separados. No envelope do Event Store o ledger
volta a prefixar a sua namespace (`ledger-`), pelo que o `step_id` efectivo do evento
é `ledger-comp-<step_id>`.

## Fluxo de `Compensate`

1. **Entrada/retoma.** `failed` ⇒ aciona `failed → compensating`. `compensating` ⇒
   retoma (crash-resume). Outro estado ⇒ `ErrNotCompensating` sem tocar no run.
2. **Reconstrução.** `StepLedger.Rebuild(run_id)` — base durável do `already-applied`.
3. **LIFO idempotente.** Percorre as compensações por ordem inversa; cada uma corre
   dentro de `StepLedger.Apply(comp_key, …)`. Já aplicada ⇒ **deduplicada** (não corre);
   pendente ⇒ corre exactamente uma vez observável. O evento `step.ledger.applied` de
   cada compensação é o seu **registo append-only** no Event Store.
4. **Política.** Compensado tudo ⇒ `compensating → ready` (retry limpo).

Respeita o cancelamento do `ctx` **entre** compensações (shutdown limpo; a retoma
posterior deduplica as já feitas).

## Crash-resume

Um crash **durante** a compensação retoma sem repetir as já aplicadas nem saltar as
pendentes. Um worker novo reconstrói o estado durável:

- `Machine.Rebuild` ⇒ estado (`compensating`);
- `StepLedger.Rebuild` ⇒ conjunto de compensações **já commitadas**.

O coordenador reitera a mesma sequência LIFO; o ledger deduplica as já feitas e corre
só as que faltam. Modela-se o **crash-before-commit** autêntico (o efeito correu mas o
registo durável não): na retoma o efeito volta a correr (*at-least-once*), mas o registo
durável fica **uma** vez — exactamente a garantia honesta de AOS-014.

## Compensação que falha — semântica honesta

- **Retry idempotente.** Uma compensação que falha é re-tentada (`WithMaxRetries`,
  default 2 ⇒ 3 tentativas). Como uma tentativa falhada **nada commita** no ledger, o
  retry repete sem duplicar.
- **Escalada, não fingimento.** Esgotada a política de retry, a saga **não finge
  sucesso**: **não** transita para `ready`, deixa o run **preso** em `compensating`
  (estado honesto de "exige intervenção") e **escala por alerta**
  (`Observer.Escalated`, `ErrCompensationExhausted`, que envolve a causa-raiz).

> **Nota sobre `killed`.** A tabela de AOS-017 **não** tem aresta
> `compensating → killed`. A escalada é, por isso, por **alerta + paragem**, nunca por
> uma transição de estado forjada. A saga respeita a máquina; não a contorna.

## Política pós-compensação

A única aresta de saída de `compensating` na tabela de AOS-017 é `compensating → ready`.
"Retry limpo" é, portanto, a política modelada; uma desistência permanente exprime-se
como a **escalada** acima (preso + alerta), não como um terminal fictício.

## Observabilidade e segredos

Os eventos de compensação são observáveis via `Observer`; as chaves entram sempre na
forma **opaca** (hash SHA-256, `durable.HashKey`) — nunca a chave em claro nem o payload
da compensação (vazio por convenção). Honra "sem segredos em logs" (DoD).

## Testes

`go test ./saga/... -race -covermode=atomic` (mingw + `CGO_ENABLED=1` para `-race`):

- **Saga feliz** — falha após K de N passos ⇒ compensa os K por **ordem inversa**;
  estado consistente; `→ ready`.
- **Idempotência** — reexecutar a saga **não duplica** reversões (2.ª passagem toda
  deduplicada).
- **Crash-resume** — crash-before-commit a compensar `step-4`; a retoma deduplica as
  já commitadas (`step-6`, `step-5`) e corre as pendentes; **0 registos de compensação
  duplicados** no log.
- **Transições coerentes e reconstruíveis** — `failed → compensating → ready` válidas
  em AOS-017 e reconstruídas por `Machine.Rebuild` (senão `ErrCorruptChain`).
- **Compensação que falha** — retry converge sem duplicar; irrecuperável ⇒
  `ErrCompensationExhausted` + run preso em `compensating` (não finge sucesso); e
  retoma após correcção da causa.

Cobertura do pacote **≥ 92 %**; `-race` limpo.

## Fora de âmbito (delegado)

Durabilidade do ledger e dedup do Event Store (AOS-014/AOS-002); tabela de transições e
`Rebuild` da máquina (AOS-017); fencing/lease do claim (AOS-018). A saga **compõe** estas
peças — não as reimplementa.
