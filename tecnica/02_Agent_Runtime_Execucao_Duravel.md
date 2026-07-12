# Agent Runtime e Execução Durável — AOS

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Técnica — Agent Runtime e Execução Durável |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/00_Arquitectura_Solucao.md`, `tecnica/01_Reference_Monitor_Plano_Controlo.md`, `tecnica/03_Orquestracao_Escalonamento.md`, `tecnica/08_Observabilidade_Evals.md`, `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md` |

---

## 1. Introdução

### 1.1 Propósito

Este documento especifica o **Agent Runtime (RT)** — o componente do plano de execução que corre o *loop* do agente — e o primitivo de **execução durável** (*durable execution*) sobre o qual esse loop assenta. O RT é o batimento cardíaco do AOS: monta o prompt, chama o modelo, despacha as *tool calls* através do Reference Monitor e verifica o resultado, uma iteração de cada vez. A tese é que este loop, para ser digno de produção, tem de ser *durável ao nível do passo* — não *durável ao nível da tarefa*. Um crash a meio de um `POST` não-idempotente não pode re-executar o efeito no retry; um worker declarado zombie por engano não pode duplicar trabalho; um replay após evolução de código não pode divergir da trajectória original. Tornar estas falhas *arquitecturalmente impossíveis* é o objectivo.

### 1.2 Âmbito

Abrange: a anatomia do loop do agente e o seu contrato de turno; o contrato de execução durável (idempotência por passo, checkpoint intra-iteração, replay *resume-from-step*, isolamento de efeitos externos em *activities*); a máquina de estados durável do run; a *liveness* por lease/heartbeat com *fencing tokens*; e a saga de compensação. Fora de âmbito, remetidos para os documentos indicados: a mediação da tool call em si (`tecnica/01`), o grafo de tarefas e o *admission control* de orçamento global (`tecnica/03`), e a captura de spans e replay-como-observabilidade (`tecnica/08`).

### 1.3 Audiência

Engenheiros de runtime, arquitectos de plataforma, engenheiros de observabilidade e equipas de SRE responsáveis por operar e recuperar runs de agentes.

### 1.4 Definições e termos

- **Loop do agente:** ciclo iterativo montar prompt → chamar modelo → despachar tools → verificar, até *complete* ou paragem.
- **Durable execution (execução durável):** modelo em que cada passo é idempotente, checkpointado e reproduzível a partir de um log — *resume-from-step*, não *resume-from-task*.
- **Activity:** unidade de efeito externo (uma tool call, uma escrita) isolada, idempotente e mediada, cuja entrada e saída são gravadas como eventos.
- **Idempotency key:** chave determinística `key = f(run_id, step_id)` que garante que um efeito externo se aplica no máximo uma vez, mesmo com retries.
- **Fencing token:** contador monotónico que invalida escritas de um worker obsoleto.
- **Saga de compensação:** sequência de acções inversas que desfaz efeitos parciais de um passo falhado.

---

## 2. ADRs aplicáveis

Este documento concretiza dois ADRs canónicos e apoia-se em vários outros do catálogo (ver `tecnica/00_Arquitectura_Solucao.md`).

- **ADR-001 — Execução durável como primitivo (central).** Idempotência por passo (`key = f(run_id, step_id)`), checkpoint intra-iteração, replay *resume-from-step*, efeitos externos isolados em *activities* (integrar Temporal/Restate/DBOS ou implementar explicitamente o contrato). É o eixo estruturante de todo o RT.
- **ADR-008 — Admission control global em tokens/$ (central para orçamento).** Orçamento por árvore em tokens e custo (não iterações), token-bucket distribuído sobre TPM/RPM real, circuit breaker, e **reserva de headroom atómica** (compare-and-swap) antes de cada spawn. O RT consome e reporta débito; a reserva é imposta pelo Escalonador (`tecnica/03`).
- **ADR-002 — Reference Monitor mandatório.** Toda a tool call despachada pelo loop atravessa o RM antes de executar; o RT nunca chama tools directamente (`tecnica/01`).
- **ADR-009 — Layout de prompt cache-estável.** O RT remonta o prompt a cada turno preservando um prefixo imutável + tail append-only, e hasheia o prompt materializado no evento de turno.
- **ADR-010 — Observabilidade OTel GenAI + audit WORM.** Cada turno e cada activity emitem spans e ficam no event store, base do replay fiel (`tecnica/08`).
- **ADR-013 — Gates de risco e controlo bidireccional.** Os estados `waiting_on_human` e `paused` do RT concretizam o steer/interrupt e o timeout fail-closed.

---

## 3. Anatomia do loop do agente

O loop é conceptualmente simples e operacionalmente exigente. Cada iteração (*turno*) executa quatro fases: **(1) montar** o prompt a partir do estado durável; **(2) chamar** o modelo via Model Gateway; **(3) despachar** as tool calls propostas, cada uma como uma *activity* mediada; **(4) verificar** o resultado e decidir se continua, pausa ou termina. O ponto não-negociável é que o loop não guarda estado próprio na memória do processo: o estado autoritativo vive no event store. O processo do worker é *stateless* e descartável; qualquer worker pode retomar qualquer run a partir do log.

Duas propriedades tornam isto duro. Primeiro, o system prompt é **remontado a cada turno** — para preservar a estabilidade de cache (ADR-009) — **e** o prompt materializado é hasheado e gravado no evento de turno, junto com model-id, params/seed e versões de skills/tools. Assim ganha-se replay fiel sem sacrificar cache-hit-rate. Segundo, a fase de despacho não executa efeitos directamente: emite pedidos ao Reference Monitor, que aplica RBAC, policy-as-code, orçamento e egress antes de qualquer execução em sandbox.

```mermaid
sequenceDiagram
    participant U as Utilizador
    participant RT as Agent Runtime
    participant GW as Model Gateway
    participant REF as Reference Monitor
    participant SBX as Sandbox microVM
    participant BUS as Event Store
    U->>RT: Objectivo com identidade e escopo
    RT->>BUS: Grava turno (hash do prompt, model-id, versoes)
    RT->>GW: Monta prompt fresco e chama o modelo
    GW-->>RT: Resposta com tool calls propostas
    RT->>REF: Pedido de tool call (step_id, taint verificado)
    REF->>REF: Avalia RBAC, policy, orcamento, egress
    alt Accao danger ou irreversivel
        REF->>U: Escala gate (preview do efeito concreto)
        U-->>REF: Aprovacao assinada ou recusa
    end
    REF->>SBX: Executa activity (idempotency key f(run_id, step_id))
    SBX-->>BUS: Grava resultado como evento append-only
    SBX-->>RT: Devolve resultado (marcado untrusted)
    RT->>RT: Verifica e decide continuar, pausar ou terminar
    RT->>U: Resposta com progresso e burn-down de custo
```

### 3.1 Contrato do loop entregue (AOS-013)

O loop base é implementado no pacote `packages/kernel/agent-runtime` (`agentruntime`). Esta subsecção fixa o **contrato concreto** entregue por AOS-013 (o esqueleto do loop; a durabilidade ao nível do passo é AOS-014/015/017).

**Superfície pública.** `Runtime.Run(ctx, Goal) (Result, error)` percorre o ciclo montar→chamar→despachar→verificar. `Goal` traz `RunID` (stream_id), `Principal` (NHI + cadeia de delegação), escopo, `ModelConfig` (model_id/params/seed), o `System` prompt e o tool set **congelado** do run. `Result` devolve a resposta final (ou paragem por `MaxTurns`), o uso/custo agregado, os `ToolResults` (todos untrusted) e os `TurnSeqs` gravados.

**Evento canónico `turn.recorded`.** Cada turno grava um evento `turn.recorded` no Event Store (AOS-002) com o **manifesto por trajectória** (ADR-010): `prompt_hash` (sha256 do prompt materializado do turno), `system_hash`, `assembly_version`, `model{model_id,params,seed}` e as `tools`/`skills` **pinadas** (nome+versão+digest+servidor MCP). O `step_id` é distinto por turno (evita a deduplicação por `idempotency_key` do Event Store). O manifesto é gravado **inline** em cada evento — a indirecção canónica `dependency_manifest_ref`/`frozen_at_seq:1` (tecnica/13 §6) fica como dívida técnica ligada a AOS-016, porque o envelope do Event Store ainda não expõe o campo.

**Interfaces (portas) públicas.** `PromptAssembler` (prompt cache-estável: prefixo imutável byte-idêntico entre turnos + tail append-only; expõe `PrefixHash` como SLI de estabilidade, emitido no span `chat` via `aos.prefix_hash`); `ModelClient` (porta do Model Gateway — real em EPIC-06); `TurnRecorder` (materialização durável do `turn.recorded`); `Tracer` (spans OTel GenAI `invoke_agent`/`chat`/`execute_tool` com a semconv — real em EPIC-08).

**Garantia estrutural de no-bypass (ADR-002).** O `Runtime` detém um `*referencemonitor.Monitor`, **nunca** uma `ToolFunc`: o único caminho de execução de tools é `Monitor.Mediate`. A prova é estrutural (reflexão) + sintáctica (`archlint`). Cada resultado de tool volta ao loop **marcado untrusted** (ADR-005); um erro de tool permitida (`dec.ToolErr`) é propagado ao span (`error.type`) e ao tail, sem ser silenciosamente descartado.

**Pontos de ligação (hooks, default no-op).** `StepIdentity` — derivação do `step_id` (AOS-014, idempotência por passo). `Checkpointer` — checkpoint intra-iteração por fase `assembled`/`model_called`/`turn_recorded`/`dispatched`/`verified` (AOS-015). A máquina de estados durável rica (`waiting_on_human`/`paused`) é AOS-017.

**Fidelidade de replay.** O `prompt_hash` por turno permite **detectar** divergência no replay; a **reconstrução** do tail exige o journaling durável dos inputs não-determinísticos, **implementado** em AOS-016 (§4.3): o `EventStoreCapturer` persiste a resposta do modelo e os outputs das tools por turno, e o `ReplayEngine` reconstrói a trajectória a partir do log. Ver `packages/kernel/agent-runtime/replay`.

---

## 4. Contrato de execução durável

A execução durável é o primitivo (ADR-001). O AOS admite duas realizações equivalentes: **integrar** um motor de durabilidade (Temporal, Restate, DBOS) ou **implementar explicitamente** o mesmo contrato sobre o event store replicado. O contrato tem quatro cláusulas.

**Idempotência por passo.** Cada efeito externo tem uma `idempotency key = f(run_id, step_id)` determinística. A activity verifica, antes de aplicar, se essa chave já produziu resultado gravado; se sim, devolve o resultado memorizado em vez de re-executar. Isto garante *zero efeitos duplicados no retry* — o driver não-funcional canónico. O `step_id` é atribuído deterministicamente pela posição no log, não por relógio nem por UUID aleatório.

**Checkpoint intra-iteração.** O estado do loop é persistido *dentro* da iteração, não apenas nas suas fronteiras. Cada evento — turno gravado, tool call emitida, resultado recebido — é um ponto de checkpoint. Um crash entre o despacho de uma tool call e a recepção do seu resultado é recuperável: ao retomar, o RT reconstrói o estado até ao último evento e sabe que a activity está pendente.

**Replay resume-from-step.** A recuperação não recomeça a tarefa do zero (*resume-from-task*), mas retoma a partir do último passo confirmado (*resume-from-step*). O RT reprocessa o log de eventos aplicando as decisões já registadas — os inputs não-determinísticos (respostas do modelo, resultados de tools, relógio) são *lidos do log*, não regenerados. O que resulta é replay determinístico: os mesmos eventos produzem o mesmo estado.

**Efeitos externos isolados em activities.** Nenhum efeito externo acontece no corpo do loop; todos vivem em activities. A distinção é a linha entre o que é *determinístico e reproduzível* (a lógica do loop) e o que é *não-determinístico e efémero* (chamar o mundo). Só assim o replay pode reexecutar a lógica sem reexecutar os efeitos.

```mermaid
flowchart LR
    CRASH["Worker cai a meio do run"] --> CLAIM["Novo worker reclama o run com fencing token"]
    CLAIM --> LOAD["Le event log do run ate ao ultimo evento"]
    LOAD --> REPLAY["Reaplica decisoes lendo inputs nao-deterministicos do log"]
    REPLAY --> CHECK{"Passo pendente com efeito?"}
    CHECK -->|"idempotency key ja resolvida"| SKIP["Devolve resultado memorizado, nao re-executa"]
    CHECK -->|"key por resolver"| EXEC["Executa activity uma vez"]
    SKIP --> RESUME["Retoma o loop no passo seguinte"]
    EXEC --> RESUME
    RESUME --> DONE["Run continua ate complete"]
```

### 4.1. Contrato de idempotência por passo — especificação (AOS-014)

A cláusula de idempotência por passo está **implementada** no subpacote
`packages/kernel/agent-runtime/durable`. O contrato publicado, consumível por
AOS-015/016/020/021/022, tem três peças e um modelo de garantia honesto.

**Função de chave — pura, determinística, injectiva.**
`IdempotencyKey(run_id, step_id) = run_id + ":" + step_id`. É byte-a-byte a forma
que o Event Store (AOS-002) já usa para deduplicar — um único espaço de nomes de
idempotência de ponta a ponta: a chave que a activity propaga ao downstream é a
mesma por que o ES deduplica. A função **rejeita** `run_id`/`step_id` vazios ou que
contenham `:`, fechando a colisão de deslocamento do delimitador (`("a","bc")` vs
`("ab","c")`); com o `:` proibido nos inputs, cada chave tem exactamente uma
decomposição (a função é injectiva; `SplitKey` é a inversa). Uma forma opaca
(SHA-256 hex) está disponível para logs/spans sem expor a chave em claro.

**`step_id` monotónico e estável.** O `StepSequencer` atribui `step_id`s puros na
posição do passo no log (número do turno), não em relógio nem UUID. O mesmo passo
lógico recebe sempre o mesmo `step_id` em execução, retry e replay — nunca
reatribuído. Implementa o hook `StepIdentity` de AOS-013 de forma aditiva
(`WithStepIdentity`), coordenando o significado de `step_id` com o checkpoint
(AOS-015) e o replay (AOS-016).

**Step-ledger de resultado.** `StepLedger.Apply(ctx, key, effect)` verifica
*already-applied* **antes** de qualquer efeito: se a chave já tem resultado
registado (in-memory, reconstruído do ES), devolve o memorizado sem correr o
`effect`; caso contrário corre o `effect`, regista `{key → status, resultado,
hash}` como evento durável `step.ledger.applied` no Event Store, e devolve o
resultado. O ledger é reconstruível do log (`Rebuild`) — sobrevive ao reinício do
worker. O envelope do evento de ledger usa uma `idempotency_key` namespaced
(`run_id:ledger-<step_id>`) para não colidir com o `turn.recorded` homónimo; esse
namespace é **reservado** — `Apply` recusa (`ErrReservedStepID`) um step_id de
negócio começado por `ledger-`, fechando estruturalmente a colisão na dedup global
do ES. A precedência *already-applied* opera em **dois âmbitos**: a verificação
in-memory reforçada por um **single-flight por-key** cobre o mesmo processo (colapsa
Applies concorrentes/repetidos no máximo a uma execução do `effect`); a garantia após
**restart-sem-`Rebuild`** ou entre workers distintos vem da **dedup durável no commit
do ES** (`StatusDuplicate`) **+ idempotência downstream**, não da verificação
in-memory — que é um atalho, não um single-flight durável.

**Observabilidade e segredos do ledger (delegações explícitas).** A vertente
**"eventos observáveis"** do DoD de AOS-014 está satisfeita pelo evento durável
`step.ledger.applied` no Event Store (fonte de verdade WORM, base do replay/ADR-010);
o `Observer` expõe contadores `apply`/`dedup` apenas na forma **opaca** (hash) da
chave. O **wiring OTel de spans/métricas** do `Observer` (default `NopObserver`, sem
emissão de span pelo próprio ledger) é **delegado a AOS-021** (activities), quando os
efeitos passarem pelo ledger — não é requisito de AOS-014. Quanto a **segredos**: o
`Result.Payload` é persistido **em claro** no ES (o cifrado por-titular é dívida de
EPIC-13); AOS-014 oferece uma guarda **opt-in** `WithSensitiveResults()` que recusa
memorizar Payload em claro não marcado como referência, mas a imposição por defeito
de resultados sensíveis por **referência/hash** (idealmente via helper ou validação no
contrato de activity) é **requisito explícito de AOS-021**.

**Modelo de garantia (honesto).** Exactly-once verdadeiro do efeito externo é
impossível sem cooperação downstream. O contrato é **at-least-once + idempotência
downstream honrando a key = 0 efeitos OBSERVÁVEIS duplicados**. O `effect` pode
correr mais de uma vez (crash entre o efeito e o commit do ledger); a deduplicação
dos efeitos externos é do downstream, pela chave determinística que o `effect` lhe
propaga. O teste de fault-injection modela um downstream que honra a key e prova
que, com crash antes ou depois do commit, o efeito é registado uma vez observável.

### 4.2. Checkpoint intra-iteração e resume-from-step — especificação (AOS-015)

O checkpoint intra-iteração está **implementado** no mesmo subpacote
`packages/kernel/agent-runtime/durable` (`checkpoint.go`, `resume.go`), sobre o
Event Store replicado (ADR-007) como fonte de verdade. Materializa a cláusula de
*checkpoint* do contrato: o RT retoma no **último passo confirmado dentro da
iteração**, sem repetir passos aplicados nem perder passos pendentes.

**Checkpointer — evento append-only por fase confirmada.** O `EventStoreCheckpointer`
é o `Checkpointer` REAL do hook de AOS-013 (ligação aditiva via `WithCheckpointer`,
sem alterar a forma do loop). Por cada fase confirmada de um turno
(`assembled`/`model_called`/`turn_recorded`/`dispatched`*/`verified`) escreve um
evento `step.checkpoint` cujo payload é o **cursor de progresso**
`{ run_id, confirmed_step_id, turn, phase, step_index, pending_activities }`. O
cursor **referencia, não copia**: a resposta do modelo permanece no `turn.recorded`
(AOS-013) e o resultado da activity no `step.ledger.applied` (AOS-014) — o checkpoint
é só um marcador barato de progresso. A `idempotency_key` do envelope é **namespaced**
(`run_id:ckpt-<phase>-<step_id>`), um **terceiro** domínio de dedup, distinto do
turno (`run_id:step_id`) e do ledger (`run_id:ledger-<step_id>`); re-escrever o mesmo
checkpoint num retry dá `StatusDuplicate` (a escrita é ela própria idempotente).

**Consistência checkpoint↔ledger.** O `confirmed_step_id` gravado é **exactamente** o
`step_id` que o ledger de AOS-014 usa para o mesmo passo lógico — para uma activity,
`SubStepID(run, turn, n) = step-NNNNNN-tool-n`. O mesmo passo lógico tem, pois, o
mesmo identificador no ledger E no checkpoint (provado por teste).

**Resumer — cursor de retoma.** `Resumer.Resume(ctx, run_id)` relê os checkpoints do
stream e devolve o `ResumePoint`: a **fronteira** é o último checkpoint por `seq`
(escritos pela ordem de execução). A tradução para o próximo passo é **pura**: fase
`verified` do turno *T* ⇒ retoma no turno *T+1*; fase `dispatched` com pendentes ⇒
retoma na 1.ª activity pendente do turno *T* (salta as confirmadas); sem checkpoints
⇒ retoma do início (turno 1). O checkpoint dá **eficiência** (salta o trabalho
confirmado); o step-ledger é a **rede de segurança** — se um passo for na mesma
re-executado, o downstream deduplica pela chave determinística. O `ResumePoint` é
**serializável e estável**, preparado para o replay determinístico (AOS-016) consumir
o cursor e arrancar de qualquer `step_id`.

**Durabilidade sob failover e cache de prompt.** Como os checkpoints vivem no Event
Store replicado por quórum, o cursor **sobrevive à morte do worker** que os escreveu:
um worker novo constrói um `Resumer` sobre o mesmo cluster e recupera a fronteira
(análogo de leitura do `Rebuild` do ledger). O teste de integração exercita o **loop
real** + **failover** (ES de 3 réplicas, `Kill` do líder + eleição da follower, e
`Revive`/resync) e confirma que o cursor de retoma se mantém idêntico. A escrita de
checkpoint **não muta o prefixo cache-estável** do prompt (ADR-009) — o checkpointer
nunca toca no assembler, só **cresce** o registo append-only; um teste compara o
prefixo de um run com checkpointer real contra um run no-op e exige igualdade
byte-a-byte. A recuperação é provada com crash em **seis pontos distintos** de uma
iteração multi-passo.

### 4.3. Replay determinístico resume-from-step (AOS-016)

O replay determinístico está **implementado** no subpacote
`packages/kernel/agent-runtime/replay` (`nondeterminism_capture.go`, `engine.go`),
em duas metades complementares que fecham a cláusula de *replay* do contrato.

**Captura de não-determinismo — o gap-filler.** O `turn.recorded` de AOS-013 grava
o manifesto (hashes, model-id/params/seed, versões pinadas) mas **não** os inputs
crus — sem eles o replay detecta divergência mas não reconstrói. O
`EventStoreCapturer` implementa o hook aditivo `Capturer` do loop (ligação via
`WithCapturer`, sem alterar a forma de AOS-013: default no-op ⇒ byte-idêntico) e
persiste, por turno, um evento **`replay.captured`** com (a) a **resposta do modelo
completa** (texto + tool calls + uso + custo + `final`), (b) o **output de cada tool
call** (untrusted + eventual erro) e (c) o **relógio** (`observed_at`). O **seed**
não é duplicado — vem do manifesto (`model.seed`). O envelope usa a `idempotency_key`
namespaced `run_id:cap-<step_id>` — o **quarto** domínio de dedup por passo,
distinto do turno, do ledger e do checkpoint. Resultados **sensíveis** são gravados
só como referência (`payload_ref = sha256`), nunca PII em claro (`WithSensitiveResults`,
análogo a AOS-014). Serialização canónica/estável (structs, sem mapas).

**Motor de replay.** `ReplayEngine.Replay(ctx, run_id, opts)` relê o stream do run e,
por turno a partir do `FromStepID` (ou do início): re-materializa o prompt com o
**mesmo `PromptAssembler`** e a **mesma construção de tail** do loop (funções
exportadas `TailFromModelText`/`TailFromToolResult` — reuso, não réplica), **compara**
o `prompt_hash` re-materializado com o gravado, e obtém a resposta do modelo de um
**cliente de replay** (devolve o registado, nunca ao vivo) e o resultado de cada tool
de um **dispatcher de replay** (devolve o registado, nunca executa). Os inputs
determinísticos (system, tool set congelado, objectivo, memory_context) são
re-fornecidos via `TrajectorySpec` — são código/config; **alterá-los simula a
evolução de código** e o motor **localiza o passo** onde o hash diverge
(`ReplayDivergence{ StepID, Turn, ExpectedHash, ActualHash }`), parando aí.

**Resume-from-step.** `FromStepID` arranca de qualquer `step_id`: os turnos
anteriores são **dobrados** a partir do log (zero efeitos) para reconstruir o estado
(o tail) e a verificação começa no ponto de retoma. O `FinalStateHash` é **idêntico**
entre um replay completo e um resume do mesmo run — a prova de que o resume produz o
mesmo estado. Consome o cursor de AOS-015: o `ResumePoint.NextStepID` de um `Resumer`
é passável directamente como `FromStepID`.

**Zero efeitos externos — garantia estrutural.** O `ReplayEngine` detém **apenas**
um `EventReader` (só `Read`) e um `Tracer`: **não tem** `ModelClient`, Reference
Monitor, registo de tools nem `Append`. Não existe, por construção, caminho para um
efeito ao vivo. Provado por teste comportamental (o contador de execuções de tool
não mexe; o stream não cresce) e estrutural (reflexão sobre os campos do struct). A
cobertura do subpacote é ≥ 90 % com `-race` limpo; a fidelidade de replay é 100 %
nos testes (mesma sequência de `step_id`, `prompt_hash` por turno coincide).

---

## 5. Máquina de estados durável

A máquina de estados grosseira dos frameworks comuns (`ready → running → complete + blocked`) é insuficiente: confunde um gate humano legítimo com um worker pendurado, e não tem estados de suspensão de primeira classe. O AOS adopta estados duráveis distintos — cada transição é um evento no log, e os estados de suspensão (`waiting_on_tool`, `waiting_on_human`, `paused`) não colidem com a detecção de zombies.

```mermaid
stateDiagram-v2
    [*] --> ready
    ready --> running: claim com fencing token
    running --> waiting_on_tool: activity externa
    waiting_on_tool --> running: resultado
    running --> waiting_on_human: gate de risco
    waiting_on_human --> running: aprovação assinada
    waiting_on_human --> killed: timeout fail-closed
    running --> paused: sinal de steer
    paused --> running: resume com correcção
    running --> complete: sucesso
    running --> failed: erro recuperável
    running --> timed_out: excede wall-clock
    failed --> compensating: saga rollback
    compensating --> ready: retry idempotente
    complete --> [*]
    killed --> [*]
    timed_out --> [*]
```

Os estados repartem-se em três famílias. **Activos:** `ready` (elegível para claim), `running` (a executar sob um fencing token). **Suspensos:** `waiting_on_tool` (bloqueado numa activity externa), `waiting_on_human` (num gate HITL, com timeout fail-closed que transita para `killed`), `paused` (steer/interrupt aceite, retomável com correcção). **Terminais e de recuperação:** `complete`, `failed` (erro recuperável, que entra em `compensating` para a saga e regressa a `ready` com retry idempotente), `timed_out` (excedeu wall-clock) e `killed` (terminado por política ou timeout). A separação de `waiting_on_human` de `running` é o que impede que um gate humano pareça um worker morto — e vice-versa.

---

## 6. Liveness e fencing

Detectar um worker morto por **PID** falha silenciosamente em substratos distribuídos: o PID pode estar saudável num host enquanto o run está pendurado, ou o processo pode ter migrado. O AOS substitui isto por **lease/heartbeat com TTL**. Um worker só executa um run enquanto detém um *lease* válido, renovado por heartbeat periódico. Se o heartbeat cessa e o TTL expira, o run volta a `ready` e pode ser reclamado por outro worker.

O perigo desta abordagem é o **falso-positivo de zombie**: o worker original não morreu, apenas ficou lento, e agora dois workers julgam-se donos do mesmo run. A defesa é o **fencing token** — um contador monotónico incrementado a cada claim. Toda a escrita ao event store carrega o token do worker; o store rejeita escritas com token inferior ao último aceite. O worker obsoleto, ao tentar gravar, é fenced-out: a sua escrita é recusada e ele aborta. Assim garante-se *no máximo um escritor efectivo* por run, sem depender de relógios sincronizados. Este mecanismo é partilhado com o Escalonador (`tecnica/03`), que atribui os leases e resolve prioridade e backpressure.

O orçamento hierárquico integra-se aqui: antes de o RT fazer spawn de um sub-agente, o Escalonador executa uma **reserva atómica** (compare-and-swap) de débito no token-bucket global (ADR-008). Sem headroom reservado, não há spawn — o que elimina o colapso agregado de rate limit em que muitos runs, cada um dentro do seu limite local, saturam colectivamente o provider.

---

## 7. Sagas e compensação

Nem todo o efeito externo é reversível por retry idempotente. Quando um passo falha *após* já ter produzido efeitos parciais no mundo — um recurso criado, uma reserva feita, uma mensagem enviada — a idempotência sozinha não basta: é preciso **desfazer**. O AOS modela isto como uma **saga de compensação**. Cada activity com efeito reversível regista, junto com o seu resultado, a acção inversa correspondente (a *compensação*). No estado `compensating`, o RT reproduz o log em sentido inverso, executando as compensações registadas dos passos já aplicados, cada uma também idempotente. Concluída a compensação, o run regressa a `ready` para retry limpo, ou termina em `failed` se o retry estiver esgotado.

Isto fecha o gap que os gates deixavam aberto: os gates *previnem* efeitos indesejados, mas nada faziam quando um efeito legítimo ficava a meio. A saga adiciona *recuperação* onde antes só havia prevenção. Efeitos irreversíveis (que não admitem compensação) são precisamente os que exigem gate `danger` com dual-control a montante (ADR-013) — a irreversibilidade empurra o controlo para antes da execução.

---

## 8. Vista de qualidade

**Arquitectura.** O RT concretiza a fronteira de durabilidade ao nível do passo — uma das três fundações não-negociáveis do AOS. A separação entre lógica determinística (loop) e efeitos não-determinísticos (activities) é o que torna o replay possível. O estado autoritativo no event store, com workers stateless, mantém o RT alinhado com a separação control/data-plane e evita qualquer SPOF de estado em memória.

**Escalabilidade.** Porque o worker é descartável e o estado vive no log, o plano de dados escala horizontalmente: adicionar workers aumenta o throughput sem coordenação de estado partilhado. A reserva de headroom atómica (ADR-008) impede que esse escalar se traduza em colapso agregado de rate limit. O checkpoint intra-iteração mantém o custo de recuperação proporcional ao trabalho perdido (um passo), não à tarefa inteira.

**Observabilidade.** Cada turno e cada activity emitem spans OTel GenAI (`invoke_agent`, `execute_tool`, `chat`) com `gen_ai.usage.*` e custo em USD, e ficam no event store append-only (ADR-010). O hash do prompt materializado por turno é a âncora do replay fiel: reproduz-se qualquer decisão a partir do manifesto de versões. O detalhe da captura de trajectória e do replay-como-observabilidade está em `tecnica/08`.

---

## 9. Riscos e mitigações

| Risco | Impacto | Mitigação |
|---|---|---|
| Double-execution de efeito externo no retry | Corrupção do mundo externo | Idempotency key = f(run_id, step_id); resultado memorizado por chave |
| Falso-positivo de zombie cross-host | Tarefa executada duas vezes | Lease/heartbeat + fencing token, nunca PID |
| Efeito parcial após falha a meio do passo | Estado externo inconsistente | Saga de compensação registada por activity; estado `compensating` |
| Colapso agregado de rate limit no spawn | Árvore de agentes autodestrói-se | Reserva atómica de headroom no token-bucket global (ADR-008) |
| Replay infiel após evolução de código | RCA e eval inválidos | Hash do prompt + model-id/params/seed + versões por turno (ADR-010) |
| Gate humano confundido com worker morto | Kill indevido de run legítimo | `waiting_on_human` como estado durável distinto de `running` |
| Cache thrash por remontagem de prompt | Explosão de custo silenciosa | Prefixo imutável + tail append-only; cache-hit-rate como SLI (ADR-009) |
| Timeout de aprovação deixa run pendurado | Recurso preso, custo acumulado | Timeout fail-closed: `waiting_on_human → killed` (ADR-013) |

---

## 10. Glossário

- **Loop do agente:** ciclo montar prompt → chamar modelo → despachar tools → verificar, iterado até paragem.
- **Durable execution:** modelo em que cada passo é idempotente, checkpointado e reproduzível — resume-from-step, não resume-from-task.
- **Activity:** unidade de efeito externo isolada, idempotente e mediada, cuja entrada e saída são eventos no log.
- **Idempotency key:** chave determinística f(run_id, step_id) que limita um efeito a aplicar-se no máximo uma vez.
- **Checkpoint intra-iteração:** persistência do estado do loop dentro do turno, não só nas suas fronteiras.
- **Resume-from-step:** retoma a partir do último passo confirmado, lendo inputs não-determinísticos do log.
- **Lease/heartbeat:** posse temporária de um run renovada periodicamente; substitui a detecção de liveness por PID.
- **Fencing token:** contador monotónico que invalida escritas de um worker obsoleto, garantindo um só escritor efectivo.
- **Saga de compensação:** sequência de acções inversas idempotentes que desfaz efeitos parciais de um passo falhado.
- **Reserva atómica de headroom:** compare-and-swap de débito no token-bucket global antes de um spawn (ADR-008).

---

## 11. Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

---

## 12. Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Emissão inicial | Equipa AOS |
