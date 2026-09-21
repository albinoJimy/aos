# EPIC-03 — Orquestração e Escalonamento

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Orquestração e Escalonamento |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/03_Orquestracao_Escalonamento.md`, `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md`, `specs/EPIC-06_Model_Gateway_Custos.md`, `specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md` |

---

## 1. Visão do Epic

Este epic constrói o **plano de controlo** do AOS: o **Orquestrador (ORQ)**, que decompõe objectivos num **grafo de tarefas acíclico** e delega a sub-agentes, e o **Escalonador (SCH)**, que impõe **admission control global**, **backpressure** e **scheduling** sobre um substrato horizontalmente escalável. É aqui que se resolve o modo de falha mais insidioso do plano-base: o colapso *agregado*, em que cada board vive individualmente dentro do seu limite mas, somados, saturam colectivamente o rate limit partilhado do provider e destroem-se mutuamente.

A tese do epic é que **orçamento e admissão são propriedades globais, não locais**. Não basta cada agente respeitar o seu `max_spawn`; o sistema tem de reservar *headroom* real no TPM/RPM antes de qualquer spawn, contabilizar orçamento por árvore em **tokens e custo (não iterações)**, e degradar graciosamente sob pressão em vez de acumular filas ilimitadas e cascatear timeouts. O contador de delegação partilhado, sujeito a corrida, é substituído por **reserva atómica** (compare-and-swap antes do spawn); o `max_spawn` constante torna-se **derivado dinamicamente do headroom**; e o roteamento round-robin cego dá lugar a **least-loaded/token-aware** com *cost-aware model tiering*.

O epic materializa a **Dimensão 1 (Arquitectura)** na parte de grafo acíclico + detecção de deadlock + orçamento hierárquico com reserva atómica, e a **Dimensão 3 (Escalabilidade e desempenho)** por inteiro. Assenta no **ADR-008** (admission control global em tokens/\$) como decisão estruturante, e cruza com o Agent Runtime (EPIC-02) para a delegação e a máquina de estados, e com o Model Gateway (EPIC-06) para o roteamento e a leitura do TPM/RPM real. Corresponde predominantemente à **Fase 3 — Escala e controlo** do roadmap, com as fundações de grafo e delegação hierárquica a ancorarem-se na **Fase 0**.

**Componentes-alvo (catálogo canónico):** ORQ (Orquestrador), SCH (Escalonador), com dependências de RT (Agent Runtime), GW (Model Gateway), ES (Event Store) e OBS (Observabilidade).

---

## 2. Critérios de Saída do Epic

- [ ] O Orquestrador decompõe objectivos num **grafo de tarefas acíclico (DAG)**, com **detecção de ciclos na admissão** e **detecção de deadlock** em execução (espera circular sobre recursos/leases).
- [ ] A delegação a sub-agentes propaga um **orçamento herdado** por árvore (tokens e \$), com **reserva atómica** (compare-and-swap) antes do spawn — sem corrida no contador partilhado.
- [ ] Existe **admission control global** por **token-bucket distribuído** sobre o **TPM/RPM real** do provider, e nenhum spawn ocorre sem débito reservado (ADR-008).
- [ ] O `max_spawn` é **derivado do headroom disponível**, não uma constante; a reserva é feita no *admit* e libertada no fim ou em falha.
- [ ] Existe **circuit breaker de orçamento** que interrompe (trip) por velocidade de tokens/\$ e por esgotamento do orçamento da árvore.
- [ ] Há **backpressure real**: filas limitadas por partição, com **política declarativa** de degradação.
- [ ] A **degradação graciosa** segue a cadeia **shed → defer → downgrade → reject**, configurável por classe e tenant.
- [ ] O **scheduling é priority-aware com aging** (anti-starvation) e o **roteamento é least-loaded/token-aware** com *cost-aware model tiering*.
- [ ] Existem **métricas de saturação e reserva de headroom** exportadas em OTel, com SLIs/SLOs e alertas.
- [ ] Todas as decisões de admissão, reserva, degradação e trip são **eventos append-only** no Event Store, replay-fiéis e mediadas de acordo com as fundações (ADR-001/002/010).

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-025 | Grafo de tarefas acíclico + detecção de deadlock | feature | L | P0 | AOS-013 |
| AOS-026 | Delegação a sub-agentes com orçamento herdado | feature | L | P0 | AOS-025, AOS-002 |
| AOS-027 | Admission control global (token-bucket distribuído sobre TPM/RPM real) | feature | L | P0 | AOS-008, AOS-026 |
| AOS-028 | `max_spawn` derivado do headroom (reserva no admit) | feature | M | P0 | AOS-027 |
| AOS-029 | Circuit breaker de orçamento (tokens/\$) | feature | M | P1 | AOS-026, AOS-027 |
| AOS-030 | Backpressure: filas limitadas + política declarativa de degradação | feature | L | P1 | AOS-027 |
| AOS-031 | Degradação graciosa (shed→defer→downgrade→reject) | feature | M | P1 | AOS-030 |
| AOS-032 | Scheduling priority-aware + aging | feature | M | P1 | AOS-027 |
| AOS-033 | Roteamento least-loaded/token-aware | feature | M | P1 | AOS-027 |
| AOS-034 | Métricas de saturação e reserva de headroom | feature | S | P1 | AOS-027, AOS-028 |
| AOS-420 | Caminho quente do escalonador: lock em processo sobre CAS durável + releitura integral do bucket | fix | M | P1 | AOS-027, AOS-032 |

---

## AOS-025 — Grafo de tarefas acíclico + detecção de deadlock

| Campo | Valor |
|---|---|
| Epic | EPIC-03 — Orquestração e Escalonamento |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-013 (loop do Agent Runtime / máquina de estados durável) |
| Bloqueia | AOS-026, AOS-032 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/03_Orquestracao_Escalonamento.md`, `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md`, ADR-001 |

### Contexto

O plano-base decompunha objectivos sem estrutura formal, permitindo dependências circulares e esperas cruzadas que se manifestavam como *zombies* aparentemente `running`. A fonte (Dimensão 1) exige um **grafo de tarefas acíclico com detecção de deadlock** como requisito inegociável. O Orquestrador tem de representar a decomposição como um **DAG** explícito, persistido no Event Store, e recusar na admissão qualquer aresta que feche um ciclo. Em execução, esperas circulares sobre recursos partilhados (leases, filas, orçamento) têm de ser **detectadas e quebradas**, não deixadas a expirar por timeout cego.

### Objectivo

Implementar no ORQ a construção, validação e persistência de um grafo de tarefas acíclico, com detecção de ciclos na admissão de arestas e detecção de deadlock (espera circular) em runtime, integrada com a máquina de estados durável do EPIC-02.

### Critérios de Aceitação

- [ ] O Orquestrador representa a decomposição de um objectivo como **DAG** de nós-tarefa, cada nó com `task_id`, dependências e estado, **persistido como eventos** no Event Store.
- [ ] A adição de uma aresta que **fecharia um ciclo é rejeitada na admissão** (fail-closed), com evento de rejeição registado e razão explícita.
- [ ] Um detector de deadlock identifica **espera circular** sobre recursos/leases e emite um evento `deadlock_detected` com o conjunto de tarefas envolvidas.
- [ ] Ao detectar deadlock, o sistema aplica uma **política de resolução determinística** (ex.: abortar a tarefa de menor prioridade / mais recente) e liberta os recursos, transitando as tarefas afectadas para estado terminal ou de retry idempotente.
- [ ] A ordenação topológica do DAG produz um plano de execução estável e reproduzível em **replay** (mesma ordem para os mesmos inputs).
- [ ] Nós e arestas do grafo referenciam identidades de agente/sub-agente coerentes com a cadeia de delegação (ADR-003), sem quebrar a `idempotency key` por passo (ADR-001).

### Detalhes Técnicos

- Componentes: **ORQ** (construtor/validador de grafo), **SCH** (consumo da ordenação topológica), **ES** (persistência de nós/arestas como eventos).
- Estruturas: representação de DAG com verificação incremental de aciclicidade (DFS com marcação, ou union-find sobre arestas); detector de deadlock por grafo de espera de recursos (*wait-for graph*) com procura de ciclos.
- Ficheiros/módulos: `orchestrator/graph.*`, `orchestrator/deadlock.*`, contratos de evento `task_node_created`, `task_edge_added`, `edge_rejected_cycle`, `deadlock_detected`, `deadlock_resolved`.
- Integração com a máquina de estados durável (EPIC-02): transições `ready → running → complete/failed` por nó; recursos e leases geridos pelo Escalonador.

### Testes Requeridos

- Unit: aciclicidade — tentativa de adicionar aresta que fecha ciclo é rejeitada; ordenação topológica correcta.
- Unit: detector de deadlock sobre *wait-for graph* sintético (com e sem ciclo).
- Integração: DAG persistido reconstrói-se por **replay resume-from-step** com ordem idêntica.
- Integração: cenário de espera circular real dispara `deadlock_detected` e a política de resolução liberta recursos sem duplicar efeitos.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Eventos de grafo/deadlock append-only no Event Store; **replay determinístico testado** (ADR-010).
- [ ] Sem chamada directa a tools; qualquer efeito externo mediado pelo RM (ADR-002).
- [ ] Spans OTel GenAI (`invoke_agent` para nós) com atributos de decomposição; custo por span.
- [ ] Cobertura não regride; revisão por 2 revisores (artefacto P0).
- [ ] Documentação e `tecnica/03` cruzados; `CHANGELOG` por Conventional Commits.

### Handoff para Claude Code

```text
Implementa AOS-025 (EPIC-03): grafo de tarefas acíclico + detecção de deadlock no Orquestrador.
- Representa a decomposição como DAG persistido em eventos no Event Store (ADR-001/007).
- Rejeita na admissão qualquer aresta que feche ciclo (fail-closed) com evento de razão.
- Implementa detector de deadlock por wait-for graph e política de resolução determinística.
- Garante ordenação topológica reproduzível em replay resume-from-step.
- Testes: aciclicidade, deadlock sintético, replay, resolução sem efeitos duplicados.
Não expandas escopo (admission control global é AOS-027). Abre PR com template §7 dos Standards.
```

---

## AOS-026 — Delegação a sub-agentes com orçamento herdado

| Campo | Valor |
|---|---|
| Epic | EPIC-03 — Orquestração e Escalonamento |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-025 (grafo de tarefas), AOS-003 (Reference Monitor mandatório) |
| Bloqueia | AOS-027, AOS-029 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/03_Orquestracao_Escalonamento.md`, `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md`, ADR-003, ADR-008 |

### Contexto

O plano-base fixava um cap de delegação de 2 e usava um contador partilhado sujeito a corrida. A fonte substitui isto por **orçamento hierárquico configurável com reserva atómica** (compare-and-swap antes do spawn), eliminando a corrida e permitindo map-reduce recursivo legítimo. Cada sub-agente é uma **identidade não-humana** distinta (ADR-003) que herda uma fatia do orçamento da árvore, expressa em **tokens e custo (não iterações)** — porque uma iteração pode arrastar 200K tokens e é um proxy péssimo de consumo.

### Objectivo

Implementar delegação a sub-agentes em que o pai reserva atomicamente uma fatia do orçamento por árvore (tokens/\$) antes do spawn, o filho herda esse sub-orçamento com identidade própria, e a libertação/consolidação do consumo é durável e idempotente.

### Critérios de Aceitação

- [ ] O orçamento é **hierárquico por árvore de run**, denominado em **tokens e USD**, e cada spawn só ocorre após **reserva atómica (CAS)** de uma fatia — nunca por incremento não-atómico de um contador partilhado.
- [ ] Cada sub-agente recebe uma **identidade não-humana única** e um **sub-orçamento herdado** que não pode exceder o remanescente do pai (autoridade = utilizador ∩ classe, imposta pelo kernel).
- [ ] A reserva é **libertada de forma idempotente** no fim do sub-agente (sucesso ou falha), consolidando o consumo real no orçamento da árvore.
- [ ] Uma tentativa de spawn **sem orçamento remanescente** é recusada (fail-closed) com evento explícito, sem *deadlock* nem espera indefinida.
- [ ] A profundidade e o *fan-out* de delegação são **configuráveis** (não constantes fixas) e limitados pelo orçamento, não por um número mágico.
- [ ] Toda a delegação é registada como cadeia de eventos append-only, permitindo reconstruir a árvore e o burn-down por replay.

### Detalhes Técnicos

- Componentes: **ORQ** (spawn e reserva), **RT** (loop do sub-agente), **RM** (mediação da criação de identidade e do débito), **ES** (eventos de reserva/consumo).
- Primitiva de reserva atómica sobre o Event Store / store de orçamento (CAS ou transacção condicional); contadores de tokens/\$ por nó e por árvore.
- Contratos de evento: `budget_reserved`, `subagent_spawned`, `budget_consumed`, `budget_released`, `spawn_denied_no_budget`.
- Nota: a reserva de *headroom* global no token-bucket (TPM/RPM) é responsabilidade de AOS-027/028; aqui trata-se do orçamento **lógico por árvore**.

### Testes Requeridos

- Unit: CAS de reserva sob concorrência simulada — nunca excede o orçamento do pai (sem corrida).
- Unit: herança de sub-orçamento e recusa quando remanescente = 0.
- Integração: árvore de delegação recursiva (map-reduce) consolida consumo real por replay idempotente.
- Integração: falha a meio do sub-agente liberta a reserva sem duplicar efeitos (ADR-001).

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] **Idempotência por passo verificada** na reserva/libertação (0 efeitos duplicados no retry).
- [ ] Criação de sub-agente e débito **mediados pelo RM**; identidade por agente aplicada (ADR-002/003).
- [ ] Spans OTel GenAI (`invoke_agent` do filho ligado ao pai) + custo USD por span.
- [ ] Sem segredos; cobertura não regride; revisão por 2 revisores (P0).

### Handoff para Claude Code

```text
Implementa AOS-026 (EPIC-03): delegação a sub-agentes com orçamento herdado.
- Orçamento hierárquico por árvore em tokens e USD (nunca iterações).
- Reserva ATÓMICA (compare-and-swap) antes de cada spawn; sem contador partilhado com corrida.
- Cada sub-agente = identidade não-humana única com sub-orçamento <= remanescente do pai (ADR-003).
- Libertação idempotente no fim; recusa fail-closed sem orçamento.
- Testes: CAS sob concorrência, herança, consolidação por replay, falha sem efeitos duplicados.
Não implementes aqui o token-bucket global (é AOS-027). Abre PR com o template dos Standards.
```

---

## AOS-027 — Admission control global (token-bucket distribuído sobre TPM/RPM real)

| Campo | Valor |
|---|---|
| Epic | EPIC-03 — Orquestração e Escalonamento |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-002 (Event Store replicado / transporte push), AOS-026 (orçamento herdado) |
| Bloqueia | AOS-028, AOS-029, AOS-030, AOS-032, AOS-033, AOS-034 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/03_Orquestracao_Escalonamento.md`, `specs/EPIC-06_Model_Gateway_Custos.md`, ADR-008 |

### Contexto

Este é o ticket estruturante do epic e a concretização directa do **ADR-008**. O modo de falha central do plano-base era "individualmente ok, agregadamente colapsa": 15 boards, cada um dentro do seu `max_spawn`, saturam colectivamente o rate limit partilhado. A fonte impõe **admission control global denominado em tokens** — um **token-bucket distribuído sobre o TPM/RPM real** do provider (lido via Model Gateway, EPIC-06) — de modo que a admissão de trabalho seja uma decisão global, não a soma de decisões locais cegas umas às outras.

### Objectivo

Implementar um admission control global no Escalonador, baseado num token-bucket distribuído parametrizado pelo TPM/RPM real do provider, que autoriza ou adia a admissão de trabalho com reserva/débito atómico e consistente entre workers.

### Critérios de Aceitação

- [ ] Existe um **token-bucket distribuído** cujos limites derivam do **TPM/RPM real** por provider/modelo/região (fornecido pelo Model Gateway), não de constantes locais.
- [ ] A admissão de qualquer trabalho que consuma quota do provider passa por **admit()** que **reserva débito atomicamente**; sem *headroom* suficiente, o trabalho é **adiado** (não silenciosamente descartado).
- [ ] O bucket é **consistente entre workers stateless** (estado no store replicado / transporte push), sem single-writer nem SPOF (ADR-007/008).
- [ ] A reposição (*refill*) segue a janela real do provider; a soma das reservas activas **nunca excede** o TPM/RPM efectivo (prova sob carga concorrente).
- [ ] Cada decisão de admissão (`admit`/`defer`) é um **evento append-only** com o consumo previsto e o headroom no momento.
- [ ] Suporta **quotas multidimensionais por tenant** (partição por tenant/board sobre o bucket global), preservando o tecto global.

### Detalhes Técnicos

- Componentes: **SCH** (admission control), **GW** (leitura de TPM/RPM e consumo real), **ES** (estado do bucket e eventos).
- Algoritmo: token-bucket distribuído com reserva atómica (lease de quota) e refill temporizado; chaves por `provider:model:region` e sub-chaves por tenant.
- Contratos: `admit_requested`, `admit_granted`, `admit_deferred`, `quota_released`; API `admit(cost_estimate) -> {granted, retry_after}`.
- Estimativa de custo por trabalho a partir do histórico/heurística de tokens (ligação a EPIC-06 e à contabilidade de custo do EPIC-08).

### Testes Requeridos

- Unit: reserva/refill do bucket; nunca excede o limite configurado.
- Integração distribuída: N workers concorrentes admitem trabalho; soma das reservas ≤ TPM/RPM (sem *oversubscription*).
- Integração: cenário "15 boards" — agregado não ultrapassa o rate limit partilhado; excesso é adiado, não rejeitado cegamente.
- Replay: sequência de admissões reconstrói-se de forma fiel a partir dos eventos.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos; prova de não-*oversubscription* sob carga.
- [ ] Estado no Event Store replicado; **sem SPOF** (ADR-007); replay determinístico testado.
- [ ] Decisões mediadas e auditáveis; spans OTel com headroom e custo por span.
- [ ] Cobertura não regride; revisão por 2 revisores (P0); scan de segredos limpo.

### Handoff para Claude Code

```text
Implementa AOS-027 (EPIC-03): admission control global (ADR-008).
- Token-bucket DISTRIBUÍDO sobre TPM/RPM REAL por provider/modelo/região (lido do Model Gateway, EPIC-06).
- admit() reserva débito atomicamente; sem headroom => defer (retry_after), nunca descarte silencioso.
- Estado no Event Store replicado (ADR-007), consistente entre workers stateless; sem SPOF.
- Quotas multidimensionais por tenant sobre o bucket global.
- Testes: refill, concorrência sem oversubscription, cenário 15 boards, replay.
Abre PR com o template dos Standards e evidência de não-oversubscription sob carga.
```

---

## AOS-028 — `max_spawn` derivado do headroom (reserva no admit)

| Campo | Valor |
|---|---|
| Epic | EPIC-03 — Orquestração e Escalonamento |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-027 (admission control global) |
| Bloqueia | AOS-034 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/03_Orquestracao_Escalonamento.md`, ADR-008 |

### Contexto

A fonte é explícita: "o escalonador não faz spawn sem débito reservado no token-bucket global; `max_spawn` passa a ser derivado dinamicamente do headroom, não uma constante". Este ticket liga a delegação lógica (AOS-026) ao admission control global (AOS-027): antes de qualquer spawn, o Escalonador **reserva headroom no admit**; o número máximo de sub-agentes activos é uma **função do headroom disponível**, não um valor fixo que ignora o estado agregado do sistema.

### Objectivo

Substituir o `max_spawn` constante por um valor derivado dinamicamente do headroom global, integrando a reserva de headroom no caminho de admissão do spawn, com libertação garantida no fim ou em falha.

### Critérios de Aceitação

- [ ] Nenhum spawn ocorre sem **reserva de headroom bem-sucedida** no token-bucket global (AOS-027) — a reserva é feita **no admit**, antes de criar o sub-agente.
- [ ] O `max_spawn` efectivo é **calculado a partir do headroom disponível** (e do custo estimado por sub-agente), variando dinamicamente com a carga agregada.
- [ ] A reserva de headroom é **libertada** quando o sub-agente termina (sucesso, falha ou timeout), de forma **idempotente**.
- [ ] Sob headroom nulo, o spawn é **adiado** (backpressure) e não força *oversubscription*; a decisão é observável.
- [ ] O comportamento é coerente com o orçamento hierárquico por árvore (AOS-026): headroom global **e** sub-orçamento têm ambos de permitir o spawn.

### Detalhes Técnicos

- Componentes: **SCH** (derivação de `max_spawn` e reserva no admit), **ORQ** (pedido de spawn), **ES** (eventos de reserva/libertação).
- Fórmula de derivação: `max_spawn = f(headroom_disponível, custo_estimado_por_subagente)`, reavaliada a cada pedido; sem constante hard-coded.
- Contratos: `headroom_reserved`, `headroom_released`, `spawn_deferred_no_headroom`.

### Testes Requeridos

- Unit: derivação de `max_spawn` para vários níveis de headroom; monotonia (mais headroom ⇒ ≥ spawns).
- Integração: reserva no admit + libertação idempotente ao terminar; sem fuga de reservas.
- Integração: headroom nulo ⇒ spawn adiado, nunca oversubscription; ambos os limites (árvore + global) respeitados.

### Definition of Done

- [ ] Critérios satisfeitos; `max_spawn` provadamente dinâmico (sem constante).
- [ ] Reserva/libertação idempotentes; replay determinístico testado.
- [ ] Spans OTel com headroom reservado por spawn; revisão por 2 revisores (P0).
- [ ] Cobertura não regride; `tecnica/03` cruzado.

### Handoff para Claude Code

```text
Implementa AOS-028 (EPIC-03): max_spawn derivado do headroom, reserva no admit.
- Sem spawn sem reserva de headroom no token-bucket global (AOS-027), feita no admit.
- max_spawn = f(headroom, custo_estimado_por_subagente); nunca constante hard-coded.
- Libertação idempotente ao terminar/falhar/timeout; headroom nulo => defer, nunca oversubscription.
- Respeita AMBOS os limites: sub-orçamento da árvore (AOS-026) e headroom global.
- Testes: derivação, reserva+libertação sem fuga, defer sob headroom nulo.
Abre PR com o template dos Standards.
```

---

## AOS-029 — Circuit breaker de orçamento (tokens/\$)

| Campo | Valor |
|---|---|
| Epic | EPIC-03 — Orquestração e Escalonamento |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-026 (orçamento herdado), AOS-027 (admission control) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/03_Orquestracao_Escalonamento.md`, `specs/EPIC-06_Model_Gateway_Custos.md`, ADR-008 |

### Contexto

A fonte troca orçamento em iterações (proxy péssimo) por **tokens/\$ com circuit breaker**. O objectivo é interromper de forma segura uma árvore que queima orçamento a uma velocidade anómala ou que esgota o orçamento atribuído, antes que provoque explosão de custo. É o par de admissão do *burn-down*: enquanto AOS-027 controla a entrada, o circuit breaker controla a **continuação**.

### Objectivo

Implementar um circuit breaker de orçamento por árvore de run que dispara (trip) por velocidade de tokens/\$ e por esgotamento do orçamento, transitando as tarefas afectadas para um estado durável e controlado, com meia-abertura para retoma segura.

### Critérios de Aceitação

- [ ] O breaker dispara por **velocidade de consumo** (tokens/\$ por unidade de tempo acima de limiar) **e** por **esgotamento** do orçamento da árvore.
- [ ] Ao disparar, as tarefas em curso transitam para estado durável seguro (ex.: `paused`/`waiting_on_human` ou terminal controlado), **sem duplicar efeitos** (ADR-001).
- [ ] O breaker tem estados **closed → open → half-open**, permitindo retoma controlada após reavaliação/reabastecimento de orçamento.
- [ ] Limiares são **configuráveis por classe/tenant** e o trip é **fail-closed** para o consumo (pára o gasto por omissão).
- [ ] Cada transição de breaker é um **evento append-only** com o motivo (velocidade vs. esgotamento) e o estado de orçamento no momento.
- [ ] Integra-se com o prompt de exaustão graciosa a ~80% (UX): antes do hard-trip, sinaliza aproximação do limite.

### Detalhes Técnicos

- Componentes: **SCH** (breaker), **RT** (transição de estado das tarefas), **GW/OBS** (consumo real de tokens/\$), **ES** (eventos).
- Sinais: token velocity, cost velocity, orçamento remanescente da árvore; janela deslizante.
- Contratos: `budget_breaker_tripped`, `budget_breaker_half_open`, `budget_breaker_closed`, `budget_warning_80pct`.

### Testes Requeridos

- Unit: trip por velocidade e por esgotamento; transições closed/open/half-open.
- Integração: trip pausa a árvore sem efeitos duplicados; retoma em half-open não re-executa passos concluídos (replay).
- Integração: aviso a ~80% precede o trip; limiares por classe/tenant respeitados.

### Definition of Done

- [ ] Critérios satisfeitos; trip é fail-closed para consumo.
- [ ] Idempotência e replay testados na pausa/retoma.
- [ ] Spans OTel com sinais do breaker e custo por span; revisão (P1, ≥1 revisor).
- [ ] Cobertura não regride.

### Handoff para Claude Code

```text
Implementa AOS-029 (EPIC-03): circuit breaker de orçamento (tokens/$).
- Trip por velocidade (tokens/$ por tempo) E por esgotamento do orçamento da árvore.
- Estados closed/open/half-open; trip fail-closed para o consumo; retoma controlada.
- Ao disparar, tarefas -> estado durável seguro sem efeitos duplicados (ADR-001).
- Aviso de exaustão graciosa a ~80% antes do hard-trip; limiares por classe/tenant.
- Testes: trip por ambos os sinais, pausa/retoma sem re-execução, aviso 80%.
Abre PR com o template dos Standards.
```

---

## AOS-030 — Backpressure: filas limitadas + política declarativa de degradação

| Campo | Valor |
|---|---|
| Epic | EPIC-03 — Orquestração e Escalonamento |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-027 (admission control global) |
| Bloqueia | AOS-031 |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `tecnica/03_Orquestracao_Escalonamento.md`, ADR-007, ADR-008 |

### Contexto

O plano-base acumulava trabalho de forma ilimitada, produzindo cascatas de timeouts. A fonte exige **backpressure real: filas limitadas com política declarativa de degradação graciosa**. Este ticket cria as filas limitadas por partição e o mecanismo de **política declarativa** que decide o que fazer quando uma fila enche — a *execução* das acções de degradação (shed/defer/downgrade/reject) é o ticket seguinte (AOS-031).

### Objectivo

Implementar filas de trabalho limitadas por partição (tenant/prioridade), com deteção de saturação e um motor de política declarativa que selecciona a acção de degradação a aplicar, emitindo sinais de backpressure a montante.

### Critérios de Aceitação

- [ ] As filas têm **limite explícito** (comprimento/idade) por partição; enchimento é **detectado** e sinalizado a montante (backpressure), não silenciosamente absorvido.
- [ ] Existe uma **política declarativa** (configuração versionada) que mapeia condições de saturação → acção de degradação (referenciando as acções de AOS-031).
- [ ] O sinal de backpressure propaga-se ao **admission control** (AOS-027): sob saturação, `admit` passa a `defer` mais agressivamente.
- [ ] Não há **acumulação ilimitada**: ao atingir o limite, aplica-se a política em vez de crescer a fila indefinidamente.
- [ ] A política é **hot-reloadable** e versionada (SemVer de artefacto de configuração), com o changelog no audit trail.
- [ ] Estados de fila e decisões de política são **eventos observáveis**.

### Detalhes Técnicos

- Componentes: **SCH** (filas e motor de política), **ES** (eventos de estado de fila), **OBS** (métricas de profundidade/idade).
- Filas limitadas por partição `tenant:priority`; watermarks (high/low) para histerese; política declarativa em ficheiro versionado (ex.: YAML/Rego para condições).
- Contratos: `queue_saturated`, `backpressure_signalled`, `degradation_policy_selected`.

### Testes Requeridos

- Unit: limites de fila e watermarks; deteção de saturação com histerese.
- Integração: saturação propaga backpressure ao admit (mais defers).
- Integração: política declarativa selecciona a acção correcta por condição; hot-reload sem perder trabalho em curso.

### Definition of Done

- [ ] Critérios satisfeitos; sem acumulação ilimitada demonstrada sob carga.
- [ ] Política versionada (SemVer) com changelog no audit trail; replay testado.
- [ ] Spans/métricas OTel de profundidade e idade de fila; revisão (P1).
- [ ] Cobertura não regride.

### Handoff para Claude Code

```text
Implementa AOS-030 (EPIC-03): backpressure com filas limitadas + política declarativa.
- Filas com limite explícito por partição tenant:priority; watermarks com histerese.
- Deteção de saturação sinaliza backpressure ao admission control (AOS-027) -> mais defers.
- Política DECLARATIVA versionada (SemVer) mapeia saturação -> acção de degradação (executada em AOS-031).
- Sem acumulação ilimitada; política hot-reloadable com changelog no audit trail.
- Testes: limites+watermarks, propagação de backpressure, selecção de política, hot-reload.
Não implementes aqui a execução shed/defer/downgrade/reject (é AOS-031). Abre PR com o template.
```

---

## AOS-031 — Degradação graciosa (shed→defer→downgrade→reject)

| Campo | Valor |
|---|---|
| Epic | EPIC-03 — Orquestração e Escalonamento |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-030 (backpressure + política declarativa) |
| Bloqueia | — |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `tecnica/03_Orquestracao_Escalonamento.md`, `specs/EPIC-06_Model_Gateway_Custos.md`, ADR-008 |

### Contexto

A fonte define a cadeia exacta: "shed → defer → degradar para modelo mais barato → rejeitar". Este ticket **executa** as acções que a política de AOS-030 selecciona, transformando pressão em degradação controlada em vez de falha catastrófica. O *downgrade* liga-se ao Model Gateway (EPIC-06) para roteamento para um tier de modelo mais barato.

### Objectivo

Implementar as quatro acções de degradação graciosa — shed, defer, downgrade e reject — accionadas pela política de backpressure, na ordem de preferência definida, cada uma durável, observável e reversível quando aplicável.

### Critérios de Aceitação

- [ ] **Shed**: descarta trabalho de baixa prioridade/opcional de forma controlada, com evento e razão (nunca descarte silencioso de trabalho crítico).
- [ ] **Defer**: adia trabalho admissível para quando houver headroom, preservando-o na fila com `retry_after` (integra AOS-027/030).
- [ ] **Downgrade**: encaminha para **modelo/tier mais barato** via Model Gateway (EPIC-06), registando o *swap* como evento de variância explícito (nunca silencioso).
- [ ] **Reject**: rejeita como último recurso, devolvendo erro claro e accionável ao chamador; **fail-closed** para acções irreversíveis.
- [ ] A **ordem de preferência** (shed → defer → downgrade → reject) é respeitada e configurável por classe/tenant através da política de AOS-030.
- [ ] Cada acção é um **evento append-only** com o gatilho, a acção e o efeito; degradações reversíveis podem ser **revertidas** ao normalizar a carga.

### Detalhes Técnicos

- Componentes: **SCH** (executor de degradação), **GW** (downgrade/roteamento de tier), **ORQ/RT** (efeito nas tarefas), **ES** (eventos).
- Contratos: `work_shed`, `work_deferred`, `model_downgraded`, `work_rejected`; ligação ao *cost-aware model tiering* (AOS-033).
- Reversibilidade: ao descer o sinal de saturação, restaurar tier/normalizar; downgrade regista variância para replay fiel.

### Testes Requeridos

- Unit: cada acção isolada (shed/defer/downgrade/reject) com o seu evento.
- Integração: cadeia completa sob pressão crescente segue a ordem de preferência.
- Integração: downgrade encaminha para tier barato via GW e regista variância; reject é fail-closed para irreversíveis.

### Definition of Done

- [ ] Critérios satisfeitos; cadeia shed→defer→downgrade→reject demonstrada.
- [ ] Downgrade regista variância de modelo (não silencioso); replay fiel testado.
- [ ] Spans/eventos OTel por acção; sem segredos; revisão (P1).
- [ ] Cobertura não regride; `tecnica/03` e EPIC-06 cruzados.

### Handoff para Claude Code

```text
Implementa AOS-031 (EPIC-03): degradação graciosa shed->defer->downgrade->reject.
- Executa as acções seleccionadas pela política de AOS-030, nesta ordem de preferência.
- shed: descarta opcional com razão; defer: adia com retry_after; downgrade: tier mais barato via
  Model Gateway (EPIC-06) registado como variância explícita; reject: último recurso, fail-closed.
- Cada acção é evento append-only; degradações reversíveis revertem ao normalizar a carga.
- Testes: cada acção, cadeia completa sob pressão, downgrade+variância, reject fail-closed.
Abre PR com o template dos Standards.
```

---

## AOS-032 — Scheduling priority-aware + aging

| Campo | Valor |
|---|---|
| Epic | EPIC-03 — Orquestração e Escalonamento |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-027 (admission control global) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/03_Orquestracao_Escalonamento.md`, ADR-008 |

### Contexto

A fonte exige **scheduling latency/priority-aware** e, no diagrama do plano de controlo, "Escalonador: prioridade, aging, detecção de deadlock". Prioridade sem *aging* provoca *starvation* de trabalho de baixa prioridade; este ticket adiciona ambos: despacho por prioridade **e** envelhecimento que promove trabalho antigo para evitar inanição.

### Objectivo

Implementar no Escalonador o despacho priority-aware sobre as filas partitionadas, com aging que aumenta a prioridade efectiva do trabalho em espera prolongada, garantindo ausência de starvation.

### Critérios de Aceitação

- [ ] O despacho respeita **classes de prioridade** (ex.: P0/P1/P2 mapeadas a filas), servindo primeiro a maior prioridade admissível.
- [ ] O **aging** aumenta a prioridade efectiva do trabalho conforme o tempo de espera, garantindo que **nenhum trabalho fica em starvation** indefinidamente.
- [ ] O scheduling é **latency-aware**: decisões consideram a idade e o SLO da tarefa, não só a prioridade nominal.
- [ ] A ordem de despacho é **determinística e reproduzível em replay** para os mesmos inputs (tie-breaking estável).
- [ ] Integra-se com o admission control (AOS-027): só despacha trabalho **admitido** (com headroom reservado).
- [ ] Parâmetros de aging são **configuráveis** por classe/tenant.

### Detalhes Técnicos

- Componentes: **SCH** (política de scheduling), **ES** (eventos de despacho).
- Estruturas: fila de prioridade com prioridade efectiva = `f(prioridade_base, idade)`; tie-break estável por `task_id`/timestamp.
- Contratos: `task_scheduled`, `priority_aged`; métricas de tempo de espera por classe.

### Testes Requeridos

- Unit: ordenação por prioridade; aging promove trabalho antigo além de trabalho novo de menor prioridade.
- Unit: ausência de starvation em cenário adversarial (fluxo contínuo de alta prioridade).
- Integração: despacho apenas de trabalho admitido; ordem reproduzível em replay.

### Definition of Done

- [ ] Critérios satisfeitos; ausência de starvation demonstrada.
- [ ] Ordem de despacho determinística; replay testado.
- [ ] Spans/métricas de tempo de espera por classe; revisão (P1).
- [ ] Cobertura não regride.

### Handoff para Claude Code

```text
Implementa AOS-032 (EPIC-03): scheduling priority-aware + aging.
- Despacho por classe de prioridade sobre filas partitionadas; serve a maior prioridade admissível.
- Aging: prioridade_efectiva = f(prioridade_base, idade); garante ZERO starvation.
- Latency-aware (considera idade/SLO); ordem determinística e reproduzível em replay (tie-break estável).
- Só despacha trabalho admitido (AOS-027). Parâmetros de aging configuráveis por classe/tenant.
- Testes: ordenação, aging, ausência de starvation adversarial, replay.
Abre PR com o template dos Standards.
```

---

## AOS-033 — Roteamento least-loaded/token-aware

| Campo | Valor |
|---|---|
| Epic | EPIC-03 — Orquestração e Escalonamento |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-027 (admission control global) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/03_Orquestracao_Escalonamento.md`, `specs/EPIC-06_Model_Gateway_Custos.md`, ADR-008 |

### Contexto

A fonte substitui o "roteamento round-robin cego" por **least-loaded/token-aware com cost-aware model tiering**. O round-robin ignora a carga real e o consumo de tokens, agravando a saturação; este ticket encaminha trabalho para o worker/tier **menos carregado** em função de tokens/carga real, em coordenação com o Model Gateway (EPIC-06).

### Objectivo

Implementar roteamento de trabalho least-loaded e token-aware no plano de controlo, integrado com o cost-aware model tiering do Model Gateway, substituindo o round-robin cego.

### Critérios de Aceitação

- [ ] O roteamento selecciona o destino (worker/tier) **menos carregado** com base em **carga real e consumo de tokens**, não em rotação cega.
- [ ] É **token-aware**: considera o custo/consumo estimado do trabalho e o headroom por destino (integra AOS-027).
- [ ] Suporta **cost-aware model tiering**: para trabalho elegível, prefere o tier adequado ao custo/qualidade, coerente com a política de downgrade (AOS-031) e o Model Gateway (EPIC-06).
- [ ] As decisões de roteamento são **observáveis** (destino escolhido, carga no momento) e reproduzíveis em replay.
- [ ] Evita *hotspots*: prova de melhor distribuição de carga vs. round-robin sob carga heterogénea.

### Detalhes Técnicos

- Componentes: **SCH** (router), **GW** (estado de carga/tier e consumo real), **OBS** (sinais de carga).
- Sinais de carga: filas por worker, tokens em voo, latência recente; função de custo por destino.
- Contratos: `work_routed`, com atributos de carga/destino; ligação ao tiering do EPIC-06.

### Testes Requeridos

- Unit: selecção least-loaded sobre estados de carga sintéticos; token-awareness.
- Integração: distribuição melhor que round-robin sob carga heterogénea (menos hotspots).
- Integração: tiering encaminha para tier adequado coerente com AOS-031/EPIC-06; decisões reproduzíveis em replay.

### Definition of Done

- [ ] Critérios satisfeitos; melhoria vs. round-robin demonstrada.
- [ ] Decisões observáveis e reproduzíveis; replay testado.
- [ ] Spans OTel com destino/carga; revisão (P1); sem segredos.
- [ ] Cobertura não regride; EPIC-06 cruzado.

### Handoff para Claude Code

```text
Implementa AOS-033 (EPIC-03): roteamento least-loaded/token-aware.
- Selecciona destino (worker/tier) MENOS carregado por carga e tokens reais; substitui round-robin cego.
- Token-aware: considera custo estimado do trabalho e headroom por destino (AOS-027).
- Cost-aware model tiering coerente com AOS-031 e o Model Gateway (EPIC-06).
- Decisões observáveis e reproduzíveis em replay; evita hotspots.
- Testes: least-loaded sintético, distribuição > round-robin, tiering, replay.
Abre PR com o template dos Standards.
```

---

## AOS-034 — Métricas de saturação e reserva de headroom

| Campo | Valor |
|---|---|
| Epic | EPIC-03 — Orquestração e Escalonamento |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-027 (admission control global), AOS-028 (max_spawn/headroom) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/03_Orquestracao_Escalonamento.md`, `specs/EPIC-08_Observabilidade_Evals.md`, ADR-008, ADR-010 |

### Contexto

Sem visibilidade, o admission control e a reserva de headroom são caixas negras. O diagrama do plano de controlo da fonte mostra a aresta "admissão → reserva headroom → escalonador". Este ticket expõe as **métricas de saturação e reserva de headroom** em OTel, com SLIs/SLOs e alertas, fechando o ciclo de controlo do epic e ligando-se ao pilar de métricas do EPIC-08.

### Objectivo

Instrumentar o plano de controlo com métricas de saturação (profundidade/idade de fila, defer-rate, backpressure) e de reserva de headroom (headroom livre, reservas activas, utilização do TPM/RPM), expostas em OTel com SLIs/SLOs e alertas.

### Critérios de Aceitação

- [ ] Exporta métricas de **saturação**: profundidade e idade de fila por partição, `defer-rate`, taxa de degradação (shed/downgrade/reject), sinais de backpressure activos.
- [ ] Exporta métricas de **headroom**: headroom livre por `provider:model:region`, reservas activas, **utilização do TPM/RPM real**, spawns adiados por falta de headroom.
- [ ] Métricas seguem **OTel** (pilar de métricas), com nomes/atributos estáveis e custo em USD onde aplicável (ADR-010).
- [ ] Estão definidos **SLIs/SLOs** (ex.: utilização-alvo de headroom, defer-rate máximo) e **alertas** para saturação sustentada e headroom criticamente baixo.
- [ ] Um **dashboard** mínimo permite ver o estado agregado (evita o modo de falha "individualmente ok, agregadamente colapsa").
- [ ] As métricas são **query-time filterable** (padrão *wide events*), sem filtragem destrutiva no emit-time.

### Detalhes Técnicos

- Componentes: **OBS** (métricas/alertas), **SCH** (fonte dos sinais), **GW** (TPM/RPM real).
- Instrumentação: métricas OTel (counters/gauges/histograms) para fila, defer, degradação, headroom; SLOs em config versionada.
- Ligação a `specs/EPIC-08_Observabilidade_Evals.md` para o pilar de métricas e alertas.

### Testes Requeridos

- Unit: emissão de cada métrica com atributos correctos.
- Integração: sob carga sintética, métricas reflectem saturação/headroom reais; alerta dispara em headroom crítico.
- Integração: filtragem query-time preserva o sinal (nada perdido no emit-time).

### Definition of Done

- [ ] Critérios satisfeitos; métricas visíveis em dashboard mínimo.
- [ ] SLIs/SLOs e alertas definidos e testados; nomes OTel estáveis.
- [ ] Sem segredos em métricas/spans; revisão (P1).
- [ ] Cobertura não regride; EPIC-08 cruzado.

### Handoff para Claude Code

```text
Implementa AOS-034 (EPIC-03): métricas de saturação e reserva de headroom.
- Métricas OTel de saturação (profundidade/idade de fila, defer-rate, taxa de degradação, backpressure)
  e de headroom (livre por provider:model:region, reservas activas, utilização TPM/RPM, spawns adiados).
- SLIs/SLOs + alertas para saturação sustentada e headroom crítico; dashboard mínimo agregado.
- Padrão wide events: capturar tudo, filtrar em query-time (nada perdido no emit-time).
- Testes: emissão por métrica, reflexo sob carga, alerta em headroom crítico, filtragem query-time.
Liga ao pilar de métricas do EPIC-08. Abre PR com o template dos Standards.
```

---

## AOS-420 — Caminho quente do escalonador: um lock em processo por cima de um CAS durável, e a releitura integral do bucket a cada decisão

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa ADR nenhum: corrige o caminho quente de AOS-027 e AOS-032, que
     são as realizações do ADR-007 (sem SPOF) e do ADR-008 (reserva atómica). As citações a
     esses ADRs são menções — quem os implementa continua a ser AOS-027/AOS-032. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-03 — Orquestração e Escalonamento |
| Fase | 3 — Escala e controlo |
| Tipo | fix |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-027 (admission control global), AOS-032 (scheduling priority-aware) |
| Bloqueia | Composição do escalonador em qualquer processo |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/control-plane/scheduler/priority.go`, `packages/control-plane/scheduler/admission.go`, `docs/governance/REGISTO-Deferimentos.md` (DEF-910, DEF-911), `analises/10_Auditoria_ORQ_SCH_PDP_Adversarial.md` |

### Contexto

A auditoria adversarial ORQ/SCH/PDP mediu **dois** defeitos no mesmo caminho quente — o par
`Dispatcher.Dispatch` → `Admission.Admit` —, registados como **DEF-910** e **DEF-911**. Ambos
citavam como eixo tickets **já entregues** (AOS-032 e AOS-027), pelo que nenhum deles tinha
destino executável: é o padrão que o §1 do registo de deferimentos existe para impedir. Este
ticket é o eixo novo, no precedente de DEF-274/275 → AOS-281.

**DEF-910 — o lock do dispatcher é mantido através do CAS durável.** `Dispatch` fazia
`d.mu.Lock(); defer d.mu.Unlock()` à entrada e só o largava no fim, laço de candidatos incluído
— e esse laço chama `Admit`, que lê o stream do bucket e faz `Append` com `WithExpectedSeq` em
retry. Uma secção crítica em processo por cima de uma operação durável. Medido sobre o substrato
real: `Submit` bloqueado **25,4 ms a N=30** e **83,7 ms a N=100**, linear em N. O sinal sai
**invertido**: quanto mais saturado o bucket (mais re-tentativas de CAS), menos trabalho novo
consegue ENTRAR — que é o oposto do que um controlo de admissão deve fazer.

**DEF-911 — cada admissão relê o stream do bucket desde a seq 1.** `foldBucket` fazia
`a.log.Read(ctx, bucketID, 1)` **dentro** do laço de CAS, sem fronteira avançada: O(N) por
decisão e O(N²) acumulado, no stream mais quente do sistema (partilhado por todos os tenants de
um `provider:model:region`). Medido: **99,5 eventos lidos por admissão nas primeiras 200 e 699,5
nas 601–800**. A `Window` limitava quais as reservas que **contam**, não quais os eventos que são
**lidos**.

Os dois estão no mesmo módulo e no mesmo caminho: não são separáveis em dois trabalhos, porque
tirar o `Admit` da secção crítica aumenta a concorrência sobre exactamente o `foldBucket` que o
segundo torna barato.

### Objectivo

O escalonador deixa de pagar, no caminho quente, dois custos que não são do problema que resolve:
(a) a fila de submissão deixa de serializar atrás de um CAS durável; (b) o custo de uma decisão de
admissão passa a depender da **janela**, não do **histórico**.

### Critérios de aceitação

- [ ] `Dispatch` **não** detém `d.mu` durante `Admit`: o lock passa a cobrir só o índice de
      candidatos (snapshot antes, materialização depois).
- [ ] Uma submissão concorrente **progride** enquanto uma admissão durável está em curso, provado
      por teste com um gate que pára dentro do `Admit`.
- [ ] Soltar o lock **não** abre despacho duplicado nem perda de trabalho: a materialização
      reconfirma sob o lock que a tarefa continua pendente e, se não estiver, salta para o
      candidato seguinte. Provado com N despachantes concorrentes sob `-race`.
- [ ] Uma reserva concedida a um candidato que perdeu a corrida **não é devolvida**: o `RequestID`
      deriva do `task_id`, pelo que a reserva do vencedor é a MESMA (idempotência por `step_id`) e
      libertá-la abriria headroom em uso — oversubscrição.
- [ ] O número de **eventos lidos por admissão** deixa de crescer com o histórico do bucket. A
      medida é a contagem de eventos, **não** o tempo de relógio.
- [ ] A fronteira de leitura **nunca** altera um veredicto: é derivada da fronteira da janela e das
      reconciliações, é descartável (perdê-la só faz reler tudo), e recua para leitura integral se
      o relógio andar para trás.
- [ ] `Replay`/`ReplayAudit` continuam a ler o stream **inteiro**: isto não é compactação nem
      snapshot, e o stream não é tocado.
- [ ] Toda a bateria do módulo verde sob `-race`.

### Detalhes técnicos

- **Fronteira do lock (DEF-910).** `Dispatch` = snapshot ordenado sob o lock (candidatos copiados
  **por valor**, para que nada partilhado seja lido fora dele) → `Admit` fora do lock →
  materialização sob o lock com reconfirmação de pendência.
- **Fronteira de leitura (DEF-911).** Por bucket, memoriza-se o par `(fromSeq, atNano)`: o menor
  `seq` entre as reservas que ainda contam e o instante em que foi calculado. A invariante é que
  tudo antes de `fromSeq` pertence a uma reserva que, em `atNano`, já estava fora da janela ou
  integralmente reconciliada — e nenhuma das duas condições se desfaz com o avanço do relógio. Sem
  reservas activas, a fronteira é a **cauda** (que é a âncora do CAS), pelo que um bucket ocioso
  colapsa para leitura constante.
- **Não é snapshot nem compactação.** Foi decidido em triagem: avançar a `fromSeq` é *bounded* e
  não toca no stream; um snapshot por bucket não o seria e fica fora de âmbito.

### Testes requeridos

- Progresso: um `Submit` concorrente completa enquanto o `Admit` está parado lá dentro (falha por
  bloqueio verdadeiro antes da correcção, não por lentidão).
- Concorrência (controlo negativo, sob `-race`): N despachantes sobre M tarefas ⇒ M despachos
  distintos, zero duplicados, zero perdidos.
- Contagem de leituras, janela deslizante: com o relógio a avançar um passo por admissão, os
  eventos lidos na admissão tardia não excedem o que cabe na janela.
- Contagem de leituras, reservas reconciliadas: com débito activo zero, a admissão lê um número
  constante de eventos.
- `Replay` devolve todos os eventos do stream depois de qualquer dos dois cenários.

### Residuais declarados

- A reserva de um candidato que perde a corrida de materialização só é segura de **não** libertar
  porque o `AdmissionGate` honra a idempotência por `RequestID` — que é o contrato documentado de
  `AdmitRequest.RequestID`. Um gate de terceiros que o ignore fica com uma reserva órfã por
  despacho perdido. Não há gate assim no repositório.
- `releaseReservation` devolve `max(Task.Cost, 1)` tokens, que só coincide com o reservado quando
  o custo é fixado na tarefa; com o `CostEstimator` a decidir, uma libertação de erro pode devolver
  menos do que reservou. É anterior a este ticket e não foi tocado.
- O módulo continua fora do grafo de build de qualquer binário (ADR-018/ADR-023): os dois
  deferimentos eram **latentes** e esta correcção é preventiva, medida em teste e não em produção.

### Definition of Done

- [ ] DEF-910 e DEF-911 reapontados para este ticket no registo, com o estado certo.
- [ ] Testes que falham antes e passam depois, para cada um dos dois eixos, com a contagem de
      eventos (não o tempo) a medir o segundo.
- [ ] `go test -race` verde em todo o módulo; `layer-lint`, `rtm`, `ref-lint`, `deferrals` e
      `estado-citado` verdes.
- [ ] `CHANGELOG.md` e a secção do módulo no `README.md` actualizados.

### Estado

**IMPLEMENTADO** (2026-09-21), **sem alcance em produção** — e a revisão adversarial encontrou
duas regressões que esta correcção tinha introduzido.

**As duas regressões, e o que as fechou:**

| Regressão | Como se manifestava | O que a fecha |
|---|---|---|
| **Oversubscrição.** A fronteira memorizava `(fromSeq, atNano)` mas **não a janela com que foi calculada**. O raciocínio era «a janela só desliza para a frente» — mas ela também **CRESCE**: o `QuotaProvider` lê limites reais do provider e o `Window` muda em runtime | Medido: TPM=100 com **90 tokens activos escondidos**, e um pedido de 30 concedido com `headroom=40` — 120 de 100 | `bucketCursor` guarda o `windowNano` e a fronteira é esquecida quando a janela do fold corrente é maior (`TestAOS420_JanelaQueCresceNaoEscondeReservasActivas`) |
| **ABA no índice.** Soltar o lock abriu uma janela: o vencedor materializa e remove o id, alguém re-submete o MESMO `task_id` com outro tenant, e o perdedor encontrava a chave e despachava com o SNAPSHOT ANTIGO | Dois despachos contra uma só reserva, e o `Dequeue` a drenar a partição do snapshot velho | A reconfirmação compara a **geração** da submissão (`TestAOS420_ReSubmissaoNaoEDespachadaComOSnapshotAntigo`) |

**A primeira tentativa de fechar o ABA estava errada, e foi o teste que a apanhou.** Usava o
`enqueue` como discriminador — e sob relógio fixo, que os testes usam e que um relógio real de
baixa resolução imita, as duas submissões têm o mesmo instante. Só uma **geração monotónica**,
independente do relógio, distingue uma re-submissão. O teste falhou com a correcção aplicada, que
é exactamente o que um teste tem de fazer quando a correcção não corrige.

**As redes de segurança passaram a ter sensor.** A revisão mediu que removê-las deixava a bateria
inteira verde — o critério «recua para leitura integral se o relógio andar para trás» estava
escrito e não era medido por nada. Há agora um controlo que compara o veredicto de uma instância
com a fronteira quente contra outra nascida sem fronteira nenhuma, sobre o MESMO stream: uma
optimização de leitura que mude um veredicto não é uma optimização.

**Alcance, dito sem arredondar.** O escalonador **não está no grafo de build de binário nenhum**:
o único importador não-teste é o `tieradapter` do Model Gateway, e o `NewAdmissionAdapter` não tem
chamadores fora de testes — há inclusive um teste de fronteira que **proíbe** o import em
`cmd/aos`. Os dois deferimentos eram latentes, e esta correcção está medida **em teste, não em
produção**.

**Resíduo declarado:** o ganho do DEF-911 está medido em regime SEQUENCIAL. A revisão mostrou que
com admissões concorrentes o `nowNano` lido uma vez por `Admit` faz o guarda do relógio disparar e
apagar a fronteira — 2,5× mais eventos por fold e 4,3× mais re-tentativas de CAS. O ganho existe,
é menor do que os números sequenciais sugerem, e medi-lo em concorrência é trabalho próprio.


## Vista de qualidade

Este epic responde primariamente às dimensões **Arquitectura** (grafo acíclico, deadlock, orçamento hierárquico com reserva atómica), **Escalabilidade** (admission control global, headroom, backpressure, scheduling, roteamento) e **Observabilidade** (métricas de saturação/headroom, SLIs/SLOs). Toca **Governação** na atribuição de identidade por sub-agente (delegação, ADR-003) e **Segurança** na mediação de toda a criação de trabalho pelo Reference Monitor (ADR-002). A tese transversal — *orçamento e admissão são globais* — é o antídoto directo ao colapso agregado.

## Riscos e mitigações

| Risco | Impacto | Mitigação |
|---|---|---|
| Colapso agregado de rate limit (15 boards) | Board autodestrói-se | Admission control global com reserva de headroom (AOS-027/028) |
| Corrida no contador de delegação | Over-spawn / orçamento excedido | Reserva atómica CAS antes do spawn (AOS-026) |
| Espera circular (deadlock) | Tarefas presas, zombies | DAG acíclico + wait-for graph + resolução determinística (AOS-025) |
| Acumulação ilimitada de filas | Cascata de timeouts | Filas limitadas + degradação shed→defer→downgrade→reject (AOS-030/031) |
| Starvation de baixa prioridade | Trabalho nunca corre | Aging no scheduling (AOS-032) |
| Hotspots por round-robin cego | Saturação desigual | Roteamento least-loaded/token-aware (AOS-033) |
| Explosão de custo silenciosa | Burn descontrolado | Circuit breaker de orçamento tokens/\$ + aviso a 80% (AOS-029) |
| Controlo sem visibilidade | Falha invisível até tarde | Métricas de saturação/headroom + alertas (AOS-034) |

## Glossário

- **Admission control global:** token-bucket distribuído que só permite spawn com headroom reservado no TPM/RPM real do provider (ADR-008).
- **Headroom:** margem livre entre o consumo reservado/activo e o tecto real (TPM/RPM) do provider.
- **Reserva atómica (CAS):** compare-and-swap que debita orçamento/headroom antes do spawn, eliminando corridas no contador partilhado.
- **Backpressure:** propagação de sinal de saturação a montante para travar a admissão em vez de acumular filas.
- **Degradação graciosa:** cadeia de acções shed → defer → downgrade → reject sob pressão, em vez de falha catastrófica.
- **Aging:** aumento da prioridade efectiva com o tempo de espera, para evitar starvation.
- **Wait-for graph:** grafo de espera de recursos usado para detectar deadlock (ciclos de espera).

## Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

## Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Emissão inicial | Equipa AOS |
| 1.1 | 2026-09-20 | +AOS-420 (caminho quente do escalonador): os deferimentos DEF-910 e DEF-911 citavam como eixo dois tickets já ENTREGUES (AOS-032 e AOS-027), logo não tinham destino executável. O primeiro mede um lock em processo mantido através do CAS durável da admissão (`Submit` bloqueado 25,4 ms a N=30 e 83,7 ms a N=100, linear em N — o sinal do escalonador sai invertido); o segundo, a releitura do stream do bucket desde a seq 1 a cada decisão (99,5 eventos lidos por admissão nas primeiras 200, 699,5 nas 601–800). Vivem no mesmo caminho e não são separáveis. | Equipa AOS |
