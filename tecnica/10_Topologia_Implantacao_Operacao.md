# Topologia, Implantação e Operação — AOS

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Operação — Topologia, Implantação e Operação |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/00_Arquitectura_Solucao.md`, `tecnica/03_Orquestracao_Escalonamento.md`, `tecnica/08_Observabilidade_Evals.md`, `specs/EPIC-10_Topologia_Operacao_DR.md` |

---

## 1. Introdução

### 1.1 Propósito

Este documento define a **topologia de implantação de referência** do AOS — Agentic OS de Referência — e o modelo operacional que a sustenta em produção. Estabelece a separação física entre plano de controlo e plano de dados, as opções de implantação (self-hosted, on-prem, nuvem), a estratégia de **escala horizontal** com degradação graciosa, o plano de **recuperação de desastre (DR)** ancorado no backup do Event Store e no replay determinístico, a observação operacional a partir dos SLIs, e um catálogo de **runbooks** para os modos de falha canónicos.

O princípio orientador é o mesmo que enforma toda a arquitectura: a topologia deve tornar as falhas operacionais *recuperáveis por desenho*. Workers *stateless* sobre um Event Store replicado (ADR-007), orçamento global com reserva de headroom (ADR-008) e trajectória completa em OTel com audit WORM (ADR-010) são as três alavancas que transformam incidentes de produção em operações rotineiras de recuperação, e não em perda de dados ou de atribuição.

### 1.2 Âmbito

Abrange a topologia de implantação, os modelos de implantação, a escala horizontal e a degradação graciosa, o DR e a recuperação por replay, a observação operacional (dashboards, SLIs, alertas) e os runbooks operacionais. Está fora do âmbito o detalhe interno de cada subsistema — remetido para os documentos especializados — e o desenho do harness de testes e de carga, tratado em `specs/EPIC-11`.

### 1.3 Audiência

Equipas de DevOps/SRE, arquitectos de plataforma, engenheiros de observabilidade e responsáveis de operação que implantam, escalam e operam o AOS. Serve também de base ao backlog executável `specs/EPIC-10_Topologia_Operacao_DR.md`.

### 1.4 Definições e termos

- **Plano de controlo:** o conjunto de componentes que *decidem* — admission control, escalonamento, PDP — sem processar directamente efeitos externos.
- **Plano de dados:** o conjunto que *executa e regista* — workers, Event Store, audit WORM.
- **RPO (Recovery Point Objective):** perda máxima de dados tolerável, medida em tempo.
- **RTO (Recovery Time Objective):** tempo máximo tolerável até restabelecer o serviço.
- **SLI/SLO:** indicador e objectivo de nível de serviço, base de alertas e de decisões de degradação.
- **Runbook:** procedimento operacional documentado para diagnóstico e mitigação de um modo de falha específico.

---

## 2. ADRs aplicáveis

| ADR | Decisão | Relevância operacional |
|---|---|---|
| ADR-007 | Event Store replicado | Fonte de verdade replicada, append-only, transporte push; elimina o SPOF do single-writer e é a base do backup e do replay como recuperação. |
| ADR-010 | Observabilidade OTel GenAI + audit WORM | Trajectória completa como árvore de spans e audit hash-chain + WORM; alimenta dashboards, SLIs, alertas e a fidelidade do replay em DR. |
| ADR-008 | Admission control global em tokens/$ | Token-bucket distribuído com reserva de headroom; é o mecanismo de degradação graciosa e a defesa contra o colapso agregado de rate limit. |

Aplicam-se ainda, de forma transversal, ADR-001 (durabilidade que torna o replay possível), ADR-002 (mediação total, cuja disponibilidade condiciona a operação) e ADR-011 (PDP, cuja falha exige runbook próprio).

---

## 3. Topologia de implantação de referência

A topologia separa fisicamente o **plano de controlo** (que decide) do **plano de dados** (que executa e regista), permitindo escalar cada um de forma independente. Os workers são *stateless*: todo o estado durável vive no Event Store replicado e no estado particionado por *run*, nunca no processo. Assim, qualquer worker pode morrer e ser substituído sem perda, e o número de réplicas ajusta-se à carga sem coordenação intra-processo.

```mermaid
flowchart TB
    subgraph EDGE["Fronteira de entrada"]
        LB["Balanceador / Ingress: identidade e escopo"]
    end
    subgraph CP["Plano de controlo (decide)"]
        ORQ["Orquestrador: grafo de tarefas aciclico"]
        SCH["Escalonador: leases, fencing, prioridade"]
        ADM["Admission control global: token-bucket distribuido"]
        PDP["Policy Decision Point: Rego/Cedar versionado"]
    end
    subgraph DP["Plano de dados (executa e regista)"]
        W1["Worker stateless 1"]
        W2["Worker stateless N"]
        POOL["Pool de microVMs pre-aquecidas (snapshot)"]
        ES["Event Store replicado (append-only, quorum)"]
        AUDIT["Audit WORM hash-chained"]
    end
    subgraph OBSV["Observabilidade (envolve tudo)"]
        OTEL["Colector OTel: spans, metricas, custo"]
        DASH["Dashboards e alertas por SLI"]
    end
    LB --> ORQ
    ORQ --> SCH
    ADM -->|reserva headroom| SCH
    SCH -->|push event-driven| W1
    SCH -->|push event-driven| W2
    PDP -.decisao por tool call.-> W1
    PDP -.decisao por tool call.-> W2
    W1 --> POOL
    W2 --> POOL
    W1 --> ES
    W2 --> ES
    ES --> AUDIT
    W1 --> OTEL
    W2 --> OTEL
    ES --> OTEL
    OTEL --> DASH
```

**Elementos da topologia.** O plano de controlo aloja o Orquestrador (ORQ) e o Escalonador (SCH), o admission control global (ADM) e o Policy Decision Point (PDP) — componentes de baixo volume de dados e alta criticidade de decisão. O plano de dados aloja os workers *stateless*, o **pool de microVMs pré-aquecidas** (snapshot/restore em 5–30 ms, cold-start < 125 ms, ADR-004), o **Event Store replicado** por quorum e o audit WORM encadeado. A observabilidade envolve ambos os planos: cada worker e o próprio Event Store emitem spans e métricas para o colector OTel, que alimenta os dashboards e as regras de alerta. O detalhe do escalonamento e das leases está em `tecnica/03_Orquestracao_Escalonamento.md`; o dos spans e do audit em `tecnica/08_Observabilidade_Evals.md`.

**Porquê esta separação.** Manter o PDP e o admission control fora do caminho de dados significa que a decisão de política e de orçamento não escala com o volume de execução — escala com o número de tool calls, avaliada em memória com política compilada (overhead de mediação p95 < 15 ms). O Event Store replicado remove o único ponto de falha do single-writer do plano-base e passa a suportar leituras de replay e escritas de eventos de múltiplos workers em paralelo.

**Materialização por IaC (AOS-098).** A separação de planos não é apenas lógica: o IaC em `infra/` instancia o módulo `network` **uma vez por plano** (rede de controlo e rede de dados, ambas *default-deny* com egress allowlist explícita — ADR-004) e dois módulos de *scaffold*, `control-plane` (ORQ/SCH/ADM/PDP) e `data-plane` (workers + pool de microVMs), cada um com a **sua contagem de réplicas** (`control_plane_replicas`, `data_plane_worker_replicas`, `microvm_pool_size`) — donde a escala independente de cada plano. Os componentes ficam como *placeholders* mínimos; a lógica interna é entregue por AOS-099 (workers *stateless* + estado particionado), AOS-100 (replicação do Event Store) e AOS-103 (pool de microVMs). O Event Store (módulo `eventstore`) e o audit WORM vivem no plano de dados; o Credential Broker/Vault no plano de controlo.

---

## 4. Opções de implantação

O AOS é um blueprint neutro quanto ao provedor. A mesma topologia lógica materializa-se em três modelos, que diferem sobretudo no substrato do Event Store, no isolamento das microVMs e nas fronteiras de soberania.

| Modelo | Substrato típico | Isolamento | Soberania e notas |
|---|---|---|---|
| **Self-hosted (cluster próprio)** | Postgres/NATS replicado ou Kafka; orquestração Kubernetes | Firecracker/Kata em nós bare-metal ou nested-virt | Controlo total; failover restrito ao cluster; ideal para requisitos de soberania estritos |
| **On-prem (data center regulado)** | Event Store replicado on-prem; vault local | microVM em hardware dedicado | Dados nunca saem da fronteira; allowlist regional de modelos via Model Gateway |
| **Nuvem (managed)** | Serviços geridos (log replicado, filas push); KMS gerido | gVisor ou microVM em instâncias com virtualização aninhada | Elasticidade rápida; **failover proibido de cruzar fronteira regional** (ADR-011) |

Em qualquer modelo mantêm-se invariantes: rede **default-deny** com egress allowlist no substrato (ADR-004), Credential Broker + Vault server-side (ADR-006), e a allowlist regional de modelos no Model Gateway. A soberania por *board* impõe que o failover nunca atravesse uma fronteira regional — restrição que se aplica igualmente à réplica do Event Store e aos backups de DR.

**Parametrização e guardrails por IaC (AOS-098).** O IaC parametriza os três modelos via `deployment_model ∈ {self_hosted, on_prem, cloud}` (validação fail-closed que rejeita qualquer outro valor). A fronteira de soberania é declarada por `region` + `sovereignty_board` + `sovereignty_regions`, e um **guardrail fail-closed** (validação de variável, disparada em *input-time* antes de qualquer ligação ao provider) **falha o `plan`/`validate`** se `backup_region` ou `replica_region` cruzarem a fronteira regional — igual a `region` ou dentro do *board*, nunca fora (ADR-011). No modelo cloud, o estado remoto é cifrado (`encrypt = true` em `backend-<env>.hcl`). Estes guardrails são verificáveis **offline** pelos testes nativos `infra/tests/*.tftest.hcl` (`tofu test` com `mock_provider`), sem daemon Docker.

---

## 5. Escala horizontal e degradação graciosa

A escala horizontal assenta em três propriedades: workers *stateless* (adicionar réplicas não exige coordenação), estado **particionado** por *run* (sharding natural do trabalho) e admission control **global** (o crescimento do plano de dados nunca ultrapassa o headroom real do provider). O Escalonador faz *push* event-driven de trabalho apenas quando há débito reservado no token-bucket distribuído; `max_spawn` é derivado dinamicamente do headroom, não uma constante (ADR-008).

```mermaid
flowchart TB
    METR["SLIs: profundidade de fila, latencia p95, headroom de tokens"] --> DEC{"Decisao de escala / degradacao"}
    DEC -->|"fila cresce, ha headroom"| UP["Escalar workers horizontalmente"]
    DEC -->|"headroom esgotado"| SHED["Degradacao graciosa"]
    UP --> POOL["Aumentar pool de microVMs pre-aquecidas"]
    POOL --> ABSORVE["Absorve carga sem cold-start"]
    subgraph LADDER["Escada de degradacao graciosa"]
        S1["1. Shed: rejeita trabalho nao-essencial"]
        S2["2. Defer: adia para fila de baixa prioridade"]
        S3["3. Degradar: encaminha para modelo mais barato"]
        S4["4. Rejeitar: fail-closed com sinal ao utilizador"]
        S1 --> S2 --> S3 --> S4
    end
    SHED --> S1
```

**Escalar para cima.** Quando a profundidade de fila e a latência p95 sobem mas há headroom no token-bucket, adicionam-se réplicas de worker e amplia-se o pool de microVMs pré-aquecidas, absorvendo a carga sem pagar cold-start. Como o estado é particionado, novas réplicas assumem partições sem *rebalancing* disruptivo.

**Degradar com graça.** Quando o headroom se esgota, entra a **escada de degradação graciosa** com política declarativa: *shed* (rejeitar trabalho não-essencial) → *defer* (adiar para fila de baixa prioridade) → *degradar* (encaminhar para modelo mais barato via roteamento cost-aware) → *rejeitar* (fail-closed com sinal explícito). Isto substitui a acumulação ilimitada de fila e a cascata de timeouts por uma resposta previsível e observável. A mecânica de backpressure e de prioridade/aging detalha-se em `tecnica/03_Orquestracao_Escalonamento.md`.

---

## 6. DR e recuperação por replay

A estratégia de DR do AOS distingue-se de um sistema convencional: como o Event Store é a **fonte de verdade append-only** e a execução é durável ao nível do passo (ADR-001), a recuperação primária é o **replay determinístico** a partir do log, e não a restauração de snapshots de estado mutável. Restaura-se o log; o estado reconstrói-se *resume-from-step*.

```mermaid
sequenceDiagram
    participant OP as Operador SRE
    participant BK as Backup do Event Store
    participant ES as Event Store restaurado
    participant RT as Agent Runtime
    participant AUD as Audit WORM
    OP->>BK: Detecta desastre e inicia DR
    BK->>ES: Restaura log replicado ate ao ultimo evento integro
    ES->>AUD: Verifica hash-chain do audit (tamper-evidence)
    ES->>RT: Fornece eventos por run e por step
    RT->>RT: Replay deterministico resume-from-step
    Note over RT: Reutiliza inputs nao-deterministicos capturados (model-id, seed, hash do prompt)
    RT->>ES: Retoma execucao a partir do ultimo step durado
    RT->>OP: Servico restabelecido dentro do RTO
```

**Backup do Event Store.** O log é replicado por quorum em contínuo (RPO tende a zero dentro da região) e adicionalmente exportado para backup imutável, com verificação periódica da hash-chain do audit WORM (ADR-010). O backup respeita a soberania: réplicas e cópias nunca cruzam a fronteira regional (ADR-011).

**RPO/RTO de referência.** *(proposta)* Alvos operacionais: **RPO ≤ 1 minuto** dentro de região (replicação síncrona por quorum) e **RTO ≤ 30 minutos** para restauro do log e retoma por replay. Estes valores derivam da disponibilidade-alvo do plano de controlo de 99,9% e devem ser validados por exercícios de DR periódicos (*game days*).

**Replay como recuperação.** A fidelidade do replay exige que todos os inputs não-determinísticos tenham sido capturados por trajectória — model-id, params, seed e hash do prompt materializado (o manifesto de dependências). Com essa captura, 100% dos passos são reproduzíveis: a recuperação não "adivinha" o estado, reexecuta-o de forma idêntica a partir do último passo durado. Efeitos externos, sendo idempotentes por chave = f(run_id, step_id), não são duplicados na retoma. O fundamento de replay e captura está em `tecnica/08_Observabilidade_Evals.md`.

---

## 7. Observação operacional, SLIs e alertas

A observação operacional deriva directamente da camada de observabilidade (ADR-010): os mesmos spans OTel GenAI e métricas que suportam debug e eval alimentam os dashboards e as regras de alerta. O padrão é *wide events* — capturar tudo, filtrar no query-time — de modo a que os alertas se construam sobre SLIs e não sobre filtragem no emit-time que esconde padrões sistémicos.

| SLI | Alvo (SLO) | Alerta quando | Runbook associado |
|---|---|---|---|
| Disponibilidade do plano de controlo | 99,9% | Erro > 0,1% em janela de 5 min | RB-04 (falha de PDP), geral |
| Overhead de mediação (RM) p95 | < 15 ms | p95 > 15 ms sustentado | RB-04 |
| Cold-start de sandbox | < 125 ms | pool esgotado ou p95 > 125 ms | escala (secção 5) |
| Cache-hit-rate de prompt | > 80% | queda abrupta (cache thrash) | observação de custo |
| Headroom de tokens/$ | > 0 reservável | headroom < limiar de reserva | RB-01 (rate limit), RB-03 (orçamento) |
| Fidelidade de replay | 100% dos passos | falha de reprodução em amostra | RB-05, DR |
| Integridade do audit WORM | hash-chain íntegra | quebra de encadeamento | DR, escalar segurança |

Os dashboards organizam-se por plano (controlo vs dados) e por *run*, com *drill-down* da árvore de spans até à tool call individual, incluindo custo em USD por span. Alertas ligam-se sempre a um runbook accionável; alertas sem runbook são considerados ruído e removidos.

---

## 8. Catálogo de runbooks operacionais

Cada runbook segue a estrutura: **sinal** (o que dispara), **diagnóstico** (como confirmar) e **mitigação** (passos de recuperação). Todos assumem acesso aos dashboards de SLI e à trajectória OTel.

**RB-01 — Colapso de rate limit (agregado).**
Sinal: headroom de tokens perto de zero, múltiplos boards a saturar colectivamente o rate limit partilhado. Diagnóstico: verificar o token-bucket distribuído e a taxa de *admit* recusado; confirmar que nenhum board excede individualmente o `max_spawn`. Mitigação: o admission control global deve estar já a recusar spawns sem headroom (ADR-008); accionar a escada de degradação (shed → defer → degradar → rejeitar); reduzir a reserva de headroom por tenant se um tenant monopoliza; validar que `max_spawn` está derivado do headroom e não fixo.

**RB-02 — Zumbi cross-host (falso-positivo/positivo).**
Sinal: worker aparenta estar `running` mas sem progresso, ou detecção de execução duplicada entre hosts. Diagnóstico: **nunca usar PID**; inspeccionar lease/heartbeat com TTL e o fencing token — um worker com fencing token obsoleto está morto. Distinguir de `waiting_on_human`, que é estado durável legítimo e não zumbi. Mitigação: deixar o lease expirar e reatribuir a partição com novo fencing token; escritas do worker obsoleto são invalidadas pela monotonicidade do token, garantindo que a tarefa não executa duas vezes. Ver máquina de estados em `tecnica/03`.

**RB-03 — Esgotamento de orçamento (tokens/$).**
Sinal: árvore de execução a aproximar-se do orçamento em tokens/custo; circuit breaker prestes a disparar. Diagnóstico: consultar o burn-down por *run* e o custo por span. Mitigação: a ~80% do orçamento, apresentar prompt de exaustão graciosa (estender / resumir e parar / abortar); se o circuit breaker disparar, a execução pára fail-closed sem efeitos parciais não compensados; o orçamento é medido em tokens/$ e não em iterações (ADR-008).

**RB-04 — Falha de PDP (Policy Decision Point).**
Sinal: latência de decisão de política a subir ou PDP indisponível. Diagnóstico: verificar a saúde do PDP e a versão de política carregada. Mitigação: o Reference Monitor deve **fail-closed** — sem decisão de política, a tool call não executa (a segurança nunca degrada para *fail-open*); encaminhar tráfego para réplica de PDP com a mesma política assinada; se a política corrompeu, reverter para a versão anterior assinada a partir do git versionado (ADR-011). A indisponibilidade do PDP degrada disponibilidade, nunca segurança.

**RB-05 — Rollback de auto-modificação.**
Sinal: regressão comportamental detectada após promoção de skill/prompt/schema auto-escrito (misevolution/drift). Diagnóstico: comparar success-rate e unsafe-action rate contra a baseline (trace-diffing); identificar a versão SemVer promovida. Mitigação: accionar o **rollback atómico** para a versão anterior; reencaminhar o artefacto para staging → eval-gate (golden-set) → canary antes de nova tentativa (ADR-012); registar o incidente no audit WORM. Nenhuma auto-modificação regressa a produção sem repassar o eval-gate.

---

## 9. Vista de qualidade

### 9.1 Escalabilidade
Plano de dados horizontalmente escalável com workers *stateless* e estado particionado; pool de microVMs pré-aquecidas para absorver picos sem cold-start; admission control global com reserva de headroom e degradação graciosa em escada (shed → defer → degradar → rejeitar). A escala do plano de controlo é desacoplada da do plano de dados. Ver ADR-008; detalhe em `tecnica/03_Orquestracao_Escalonamento.md`.

### 9.2 Fiabilidade
Event Store replicado por quorum sem SPOF; recuperação por replay determinístico *resume-from-step* com idempotência por passo; RPO ≤ 1 min e RTO ≤ 30 min *(proposta)* validados por *game days*; failover restrito à fronteira de soberania; PDP e Reference Monitor com comportamento fail-closed. Ver ADR-007, ADR-001, ADR-011.

### 9.3 Observabilidade
Trajectória completa em OTel GenAI semconv e audit hash-chain + WORM como base de dashboards, SLIs e alertas; padrão *wide events* (filtrar no query-time); cada alerta ligado a um runbook accionável; custo em USD por span. Ver ADR-010; detalhe em `tecnica/08_Observabilidade_Evals.md`.

---

## 10. Riscos e mitigações

| Risco | Impacto | Mitigação |
|---|---|---|
| Colapso agregado de rate limit | Board autodestrói-se | Admission control global com reserva de headroom; escada de degradação (ADR-008) — RB-01 |
| Falso-positivo de zumbi cross-host | Tarefa executada duas vezes | Lease/heartbeat + fencing token, nunca PID (ADR-001) — RB-02 |
| Esgotamento de orçamento silencioso | Explosão de custo | Orçamento em tokens/$ com circuit breaker e exaustão graciosa a 80% (ADR-008) — RB-03 |
| Falha de PDP degradar para fail-open | Tool call sem política | Reference Monitor fail-closed; réplica de PDP; reversão de política assinada (ADR-011) — RB-04 |
| Auto-modificação regressiva em prod | Regressão comportamental | Rollback atómico + repasse por eval-gate/canary (ADR-012) — RB-05 |
| Perda do Event Store | Perda da fonte de verdade | Replicação por quorum + backup imutável; replay como recuperação (ADR-007) |
| Replay infiel após restauro | RCA e retoma inválidos | Manifesto de dependências por trajectória + hash do prompt (ADR-010) |
| Failover cruza fronteira de soberania | Transferência ilegal de PII | Failover e backup restritos à região; allowlist regional (ADR-011) |
| Alerta sem runbook | Fadiga de alerta, ruído | Todo o alerta liga a runbook accionável; SLIs sobre wide events (ADR-010) |
| Cold-start em pico de carga | Latência excessiva | Pool de microVMs pré-aquecidas dimensionado ao headroom (ADR-004) |

---

## 11. Glossário técnico

- **Plano de controlo / plano de dados:** separação entre os componentes que decidem (admission, escalonamento, PDP) e os que executam e registam (workers, Event Store, audit).
- **Event Store replicado:** log append-only, replicado por quorum, fonte de verdade e base do replay; substitui o single-writer (ADR-007).
- **Pool de microVMs:** conjunto de sandboxes pré-aquecidas com snapshot/restore, que absorve picos sem pagar cold-start.
- **Degradação graciosa:** política declarativa em escada — shed, defer, degradar, rejeitar — accionada quando o headroom se esgota.
- **RPO/RTO:** perda máxima de dados tolerável / tempo máximo até restabelecer o serviço.
- **Replay como recuperação:** reconstrução do estado por reexecução determinística *resume-from-step* a partir do log, em vez de restauro de estado mutável.
- **SLI/SLO:** indicador e objectivo de nível de serviço; base de alertas e de decisões de degradação.
- **Runbook:** procedimento documentado (sinal, diagnóstico, mitigação) para um modo de falha.
- **Game day:** exercício periódico de DR que valida RPO/RTO e a fidelidade do replay.

---

## 12. Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

---

## 13. Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Emissão inicial | Equipa AOS |
