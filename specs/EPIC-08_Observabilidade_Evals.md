# EPIC-08 — Observabilidade e Evals

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — EPIC-08 Observabilidade e Evals |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/08_Observabilidade_Evals.md`, `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md`, `specs/EPIC-11_Testes_Qualidade.md`, `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `tecnica/09_Governacao_Conformidade.md`, `tecnica/13_Modelo_Dados_Eventos.md` |

---

## 1. Visão do Epic

Este epic concretiza a camada transversal de **Observabilidade & Evals (OBS)** do AOS, materializando o **ADR-010** (Observabilidade OTel GenAI + audit WORM). A observabilidade no AOS não é telemetria pendurada no fim: é uma fronteira de primeira classe que envolve todos os subsistemas e torna cada acção *auditável, reproduzível e avaliável*.

A tese subjacente é o Princípio 4 do blueprint — **contexto ≠ registo**: descartar da injecção no modelo é legítimo (higiene, cache, economia de tokens); descartar do audit trail nunca é. O epic entrega, por camadas: (1) a instrumentação em **OpenTelemetry GenAI semantic conventions (semconv)** como *wire format* neutro; (2) a **árvore de spans completa** de cada agente e sub-agente, reconstruindo a cadeia de delegação *on-behalf-of*; (3) a **contabilidade de tokens/custo em USD por span**; (4) o **replay determinístico** por captura de inputs não-determinísticos; (5) o **circuit breaker multi-sinal** que apanha o agente *vivo* em loop — o gap que a detecção de zumbis por lease/PID nunca cobria; (6) a **detecção de loop semântico** por *action-dedup* via `hash(tool+args)`; (7) o padrão **wide events** (capturar tudo, filtrar no query-time); (8) o **audit hash-chain + WORM** *tamper-evident*, separado dos diagnósticos efémeros; (9) o **eval harness ligado ao trace** (`gen_ai.evaluation.result`); e (10) **dashboards, SLIs/SLOs e alertas** operacionais.

O epic encerra dois cenários de falha do plano-base: *The Audit Log Lied* (o trail que respondia "o pool" a *quem autorizou*) e o **loop invisível** (agente saudável para o PID mas preso num ciclo semântico, com explosão de custo silenciosa). O detalhe de solução vive em `tecnica/08_Observabilidade_Evals.md`; a execução durável e o manifesto de versões em `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md`; o eval-gate e os golden-sets em `specs/EPIC-11_Testes_Qualidade.md`.

**Fase de roadmap:** predominantemente **Fase 2 — Governação e observabilidade (P1)**; os dashboards, SLIs/SLOs e alertas alinham com a **Fase 3 — Escala e controlo (P1/P2)**.

---

## 2. Critérios de Saída do Epic

- [ ] Toda a tool call, turno de modelo e delegação emitem spans OTel GenAI semconv (`execute_tool`/`chat`/`invoke_agent`) com `trace_id`/`span_id` correlacionáveis.
- [ ] A árvore de spans reconstrói a cadeia de delegação *on-behalf-of* completa de um run com sub-agentes, sem lacunas no *handoff*.
- [ ] Cada span de modelo e tool carrega `gen_ai.usage.input_tokens`/`output_tokens` e custo derivado em USD, agregável por trajectória.
- [ ] O replay determinístico reproduz **100% dos passos** de um run capturado, a partir do manifesto de versões e dos inputs não-determinísticos.
- [ ] O circuit breaker multi-sinal faz *trip* sobre cost/token velocity, wall-clock, action-dedup e ausência de progresso, transitando o run para estado durável sem o matar cegamente.
- [ ] A detecção de loop semântico por `hash(tool+args)` sinaliza repetição de acção sem efeito acima de um limiar configurável.
- [ ] A telemetria segue o padrão *wide events* — captura de alta cardinalidade filtrável no query-time, sem decisão de descarte no emit-time.
- [ ] O audit trail é hash-chained + WORM assinado, com verificação de integridade *tamper-evident* e separado dos diagnósticos efémeros.
- [ ] Cada avaliação é registada como span `gen_ai.evaluation.result` ligado ao trace avaliado, alimentando o eval-gate de auto-modificação.
- [ ] Existem dashboards com SLIs/SLOs (cache-hit-rate, overhead de mediação p95, custo por trajectória, override-rate) e alertas accionáveis a partir dos SLIs.
- [ ] Todos os tickets AOS-076 a AOS-086 cumprem a DoD; os gates de CI/CD e o scan de segredos estão limpos.

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-076 | Instrumentação OpenTelemetry GenAI semconv | feature | M | P1 | EPIC-01 (Reference Monitor), EPIC-02 (Runtime) |
| AOS-077 | Árvore de spans completa de sub-agentes | feature | M | P1 | AOS-076 |
| AOS-078 | Contabilidade de tokens/custo por span | feature | S | P1 | AOS-076 |
| AOS-079 | Replay determinístico (captura de inputs não-determinísticos) | feature | L | P1 | AOS-076, EPIC-02 |
| AOS-080 | Circuit breaker multi-sinal (agente vivo em loop) | feature | L | P1 | AOS-076, AOS-078 |
| AOS-081 | Detecção de loop semântico (action-dedup por hash) | feature | S | P1 | AOS-076, AOS-080 |
| AOS-082 | Wide events (capturar tudo, filtrar no query-time) | feature | M | P1 | AOS-076 |
| AOS-083 | Audit hash-chain + WORM (pipeline) | feature | L | P1 | AOS-076, EPIC-01 |
| AOS-084 | Eval harness ligado ao trace | feature | M | P1 | AOS-077, EPIC-11 |
| AOS-085 | Dashboards + SLIs/SLOs | feature | M | P1 | AOS-076, AOS-078, AOS-082 |
| AOS-086 | Alertas a partir dos SLIs | feature | S | P2 | AOS-085 |
| AOS-398 | O SLI de overhead de mediação mede a execução da tool, não a decisão | fix | M | P0 | AOS-085, AOS-086, AOS-274 |
| AOS-401 | O SLI de overhead de mediação ainda conta a escrita do selo e continua a violar o SLO em produção | fix | S | P0 | AOS-398 |
| AOS-402 | A escrita do selo de mediação fica legível no `/metrics` do nó | fix | S | P2 | AOS-401 |
| AOS-404 | Os ~31 ms de overhead de mediação da v0.1.15 ficam explicados pelos dados de produção | spike | S | P2 | AOS-402 |
| AOS-405 | A janela da política de mediação fica partida por hook no span e no `/metrics` do nó | fix | S | P1 | AOS-404 |

---

## AOS-076 — Instrumentação OpenTelemetry GenAI semconv

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | EPIC-01 (Reference Monitor), EPIC-02 (Agent Runtime) |
| Bloqueia | AOS-077, AOS-078, AOS-079, AOS-080, AOS-082, AOS-083, AOS-085 |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md` §3, ADR-010, ADR-002 |

### Contexto
A trajectória completa de cada agente e sub-agente tem de ser persistida como árvore de spans, adoptando OpenTelemetry GenAI semconv como *wire format*. A escolha evita lock-in ao dashboard interno: qualquer backend compatível com OTel consome os mesmos dados. Este ticket é a fundação de instrumentação sobre a qual todos os restantes do epic assentam.

### Objectivo
Instrumentar o Agent Runtime e o Reference Monitor para emitir spans OTel GenAI semconv normalizados — `invoke_agent` por nível de delegação, `chat` por turno de modelo, `execute_tool` por tool call mediada — com os atributos `gen_ai.*` obrigatórios e propagação de contexto correcta.

### Critérios de Aceitação
- [ ] Cada turno de modelo emite um span `chat` com `gen_ai.request.model` e o identificador da NHI do principal que executa.
- [ ] Cada tool call mediada pelo Reference Monitor emite um span `execute_tool` com o nome da tool, o `hash(tool+args)` e o resultado marcado *untrusted* (taint).
- [ ] Cada nível de delegação abre um span `invoke_agent` que envolve os spans-filho do respectivo sub-objectivo.
- [ ] Os spans propagam `trace_id` comum e `span_id` do pai, reconstituíveis por um exportador OTel-compatível.
- [ ] Nenhum caminho de código executa uma tool sem produzir o span correspondente (mediação total, ADR-002).

### Detalhes Técnicos
- Componentes: Agent Runtime (RT), Reference Monitor (RM), biblioteca de instrumentação OBS partilhada.
- Adoptar OTel SDK com semconv GenAI; definir nomes de span e mapa de atributos numa camada `otel_genai` reutilizável.
- O span `execute_tool` é aberto no RM (ponto único de mediação), garantindo cobertura de 100% das tool calls.
- Exportador configurável (OTLP) para backend externo; content-capture apenas por referência nesta fase (payloads em AOS-079).

### Testes Requeridos
- Unit: mapeamento de atributos `gen_ai.*` por tipo de span.
- Integração: um run com uma tool call produz árvore `invoke_agent`→`chat`→`execute_tool` bem formada.
- Contrato: validação do esquema de span contra a semconv GenAI.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e verificados por teste.
- [ ] Cobertura de instrumentação sem regressão; sem tool call não instrumentada.
- [ ] Gates de CI/CD (build, lint, unit, integração, SAST/SCA) verdes; scan de segredos limpo.
- [ ] Documentação da camada `otel_genai` e cross-ref a `tecnica/08` actualizada.

### Handoff para Claude Code
```text
És o executor do ticket AOS-076 do Agentic OS de Referência (AOS).
Lê specs/EPIC-08_Observabilidade_Evals.md (AOS-076) e tecnica/08_Observabilidade_Evals.md §3.
Implementa a camada de instrumentação OTel GenAI semconv: spans invoke_agent/chat/execute_tool
com atributos gen_ai.* e propagação trace_id/span_id. O span execute_tool abre no Reference
Monitor para garantir cobertura de 100% das tool calls (ADR-002). Exportador OTLP configurável.
Escreve testes unit (mapa de atributos), integração (árvore bem formada) e de contrato (esquema
semconv). Não expandas escopo: custo/tokens é AOS-078, payloads/replay é AOS-079. Corre gates
locais e scan de segredos antes do PR.
```

---

## AOS-077 — Árvore de spans completa de sub-agentes

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-076 |
| Bloqueia | AOS-084 |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md` §3–§4, ADR-010, Princípio 4 |

### Contexto
A contradição mais aguda do plano-base era *"avaliamos trajectórias, não saídas"* contra *"o filho só devolve o resumo ao pai"*. Resolve-se desacoplando os dois eixos do Princípio 4: o sub-agente devolve ao contexto do pai apenas um resumo de 1–2k tokens (higiene, menos custo); em paralelo, emite a árvore de spans completa para o backend de observabilidade. Descartar do contexto injectado é legítimo; descartar do backend nunca é.

### Objectivo
Garantir que a trajectória completa de cada sub-agente é sempre persistida no backend como sub-árvore de spans ligada ao trace do pai, independentemente do resumo higienizado devolvido ao contexto do orquestrador.

### Critérios de Aceitação
- [ ] Um sub-agente delegado produz uma sub-árvore `invoke_agent` completa no backend, mesmo quando devolve ao pai apenas um resumo de 1–2k tokens.
- [ ] A sub-árvore liga-se ao span do pai por `span_id`, reconstruindo a cadeia de delegação *on-behalf-of* de N níveis.
- [ ] O resumo devolvido ao contexto do pai e a trajectória persistida no backend são artefactos distintos (contexto ≠ registo).
- [ ] Nenhuma parte da trajectória do sub-agente se perde no *handoff*.

### Detalhes Técnicos
- Componentes: Agent Runtime (RT) do sub-agente, Orquestrador (ORQ), backend de observabilidade (OBS).
- Separar o canal de *resumo-ao-pai* do canal de *árvore-ao-backend*; ambos derivam da mesma execução mas seguem destinos distintos.
- Testar recursão (map-reduce): delegação de sub-agentes que por sua vez delegam, com árvore coerente.

### Testes Requeridos
- Integração: run pai→filho→neto produz árvore de 3 níveis no backend; contexto do pai contém só o resumo.
- Propriedade: para qualquer profundidade de delegação, a árvore é acíclica e completamente ligada.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e verificados por teste.
- [ ] Verificado que o eval-driven development (AOS-084) consegue reconstruir sub-trajectórias.
- [ ] Gates de CI/CD verdes; scan de segredos limpo.
- [ ] Cross-ref a `tecnica/08` §4 e a `specs/EPIC-02` (loop/handoff) actualizada.

### Handoff para Claude Code
```text
És o executor do ticket AOS-077 do AOS. Depende de AOS-076 (Done).
Lê specs/EPIC-08 (AOS-077) e tecnica/08 §3–§4.
Desacopla o resumo-ao-pai (1–2k tokens, higiene) da árvore-de-spans-completa-ao-backend
(Princípio 4: contexto ≠ registo). Garante que a sub-árvore de cada sub-agente se liga ao
span do pai e que nada se perde no handoff, incluindo delegação recursiva. Testa run
pai→filho→neto. Não implementes evals aqui (AOS-084). Corre gates locais antes do PR.
```

---

## AOS-078 — Contabilidade de tokens/custo por span

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-076 |
| Bloqueia | AOS-080, AOS-085 |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md` §3, ADR-010, ADR-008 |

### Contexto
Cada tool call e turno de modelo tem de ser estruturado com tokens/custo, com contabilidade em USD por span. Esta informação alimenta simultaneamente o burn-down de custo apresentado ao utilizador, o orçamento por árvore (ADR-008) e o sinal de cost/token velocity do circuit breaker (AOS-080).

### Objectivo
Registar, em cada span de modelo, os atributos `gen_ai.usage.input_tokens` e `gen_ai.usage.output_tokens`, e derivar o custo em USD por span a partir de uma tabela de preços por modelo, agregável por trajectória.

### Critérios de Aceitação
- [ ] Cada span `chat` carrega `gen_ai.usage.input_tokens` e `gen_ai.usage.output_tokens` reais.
- [ ] O custo em USD é derivado por span a partir de uma tabela de preços versionada por `gen_ai.request.model`.
- [ ] O custo agrega correctamente por trajectória (soma dos spans-filho) e por sub-árvore de delegação.
- [ ] A contabilidade expõe o sinal de cost/token velocity consumível pelo circuit breaker (AOS-080) e pelo orçamento por árvore (ADR-008).

### Detalhes Técnicos
- Componentes: camada `otel_genai` (OBS), Model Gateway (GW) como fonte de contagem de tokens.
- Tabela de preços por modelo versionada; custo = f(tokens, preço, modelo, região).
- Evitar dupla contagem em delegação: o custo do pai é a soma dos próprios turnos, o agregado inclui sub-árvores explicitamente.

### Testes Requeridos
- Unit: derivação de custo USD a partir de tokens e tabela de preços.
- Integração: agregação de custo por trajectória com sub-agentes corresponde à soma esperada.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e verificados por teste.
- [ ] Consistência da agregação validada contra os totais do Model Gateway.
- [ ] Gates de CI/CD verdes; scan de segredos limpo.

### Handoff para Claude Code
```text
És o executor do ticket AOS-078 do AOS. Depende de AOS-076 (Done).
Lê specs/EPIC-08 (AOS-078) e tecnica/08 §3.
Adiciona gen_ai.usage.input_tokens/output_tokens a cada span chat e deriva custo USD por span
a partir de uma tabela de preços versionada por modelo. Garante agregação correcta por
trajectória e sub-árvore (sem dupla contagem) e expõe o sinal cost/token velocity para AOS-080
e para o orçamento por árvore (ADR-008). Testa derivação e agregação. Corre gates locais antes do PR.
```

---

## AOS-079 — Replay determinístico (captura de inputs não-determinísticos)

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-076, EPIC-02 (Execução durável) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md` §5, ADR-010, ADR-001 |

### Contexto
O replay fiel exige capturar todos os inputs não-determinísticos de cada passo. Mantém-se a montagem efémera do prompt em runtime (para estabilidade de cache), mas grava-se por turno um manifesto imutável: hash do prompt materializado, versão do código de montagem, `model-id`/params/seed e versões pinadas de skills/tools/memória. O replay infiel após evolução de código — RCA e evals inválidos — é mitigado precisamente por este manifesto de versões por trajectória.

### Objectivo
Implementar a captura por passo dos inputs não-determinísticos e o manifesto de dependências por trajectória, habilitando replay *resume-from-step* (ADR-001) com alvo de **100% dos passos reproduzíveis**.

### Critérios de Aceitação
- [ ] Cada turno grava um manifesto imutável: hash do prompt materializado, versão do código de montagem, `model-id`/params/seed, versões pinadas de skills/tools/memória.
- [ ] Os payloads completos residem em storage externo com IAM próprio (OTel content-capture mode 3), fora do caminho quente.
- [ ] O replay reconstrói exactamente a entrada de cada passo e reproduz o resultado (*resume-from-step*, não *resume-from-task*).
- [ ] Um run capturado atinge **100% de passos reproduzíveis** num teste de fidelidade de replay.
- [ ] É possível reexecutar contra um modelo actual para *trace-diffing* contra a baseline.

### Detalhes Técnicos
- Componentes: Agent Runtime (RT), Event Store (ES), storage de payloads externo, OBS.
- Manifesto de dependências ligado ao evento de turno no Event Store (cruza com EPIC-02).
- Content-capture mode 3: referência no span, payload por IAM separado; respeita minimização/redação (ADR-011).

### Testes Requeridos
- Replay: golden run reproduz 100% dos passos a partir do manifesto.
- Integração: trace-diffing entre execução original e reexecução detecta divergências.
- Negativo: inputs não-determinísticos não capturados falham a admissão de replay (fidelidade é condição, não opção).

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e verificados por teste de replay.
- [ ] Alvo de 100% de passos reproduzíveis demonstrado num run de referência.
- [ ] IAM do storage de payloads validado; minimização/redação respeitadas.
- [ ] Gates de CI/CD verdes; scan de segredos limpo.

### Handoff para Claude Code
```text
És o executor do ticket AOS-079 do AOS. Depende de AOS-076 (Done) e de EPIC-02 (durable execution).
Lê specs/EPIC-08 (AOS-079) e tecnica/08 §5; consulta specs/EPIC-02 para o modelo de eventos.
Implementa a captura por passo dos inputs não-determinísticos e o manifesto de dependências por
trajectória (hash do prompt, versão do código de montagem, model-id/params/seed, versões pinadas).
Payloads em storage externo com IAM próprio (content-capture mode 3). Garante replay resume-from-step
com 100% de passos reproduzíveis e trace-diffing. Corre teste de replay e gates locais antes do PR.
```

---

## AOS-080 — Circuit breaker multi-sinal (agente vivo em loop)

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-076, AOS-078 |
| Bloqueia | AOS-081 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md` §6, ADR-010, ADR-008, `tecnica/02`/`tecnica/03` |

### Contexto
A detecção de zumbis por lease/heartbeat apanha o worker **morto**: o lease expira, o fencing token invalida escritas obsoletas. Mas não vê o agente **vivo** preso em loop — o caso que o PID nunca detectava, porque o processo parece saudável. O AOS complementa a detecção de liveness com um circuit breaker multi-sinal que combina sinais independentes e faz *trip* quando qualquer um (ou uma composição) cruza o limiar.

### Objectivo
Implementar um circuit breaker multi-sinal que combina cost/token velocity, wall-clock e ausência de progresso (o sinal de action-dedup é entregue por AOS-081), e que ao abrir transita o run para estado durável sem o matar cegamente, preservando a trajectória para RCA.

### Critérios de Aceitação
- [ ] O breaker monitoriza, no agente vivo: cost/token velocity (partilhado com o orçamento por árvore, ADR-008), wall-clock e ausência de progresso (nenhum novo estado útil entre iterações).
- [ ] O *trip* ocorre quando qualquer sinal (ou composição configurável) cruza o limiar.
- [ ] Ao abrir, o run transita para estado durável (`paused` ou `timed_out`), **não** é morto cegamente.
- [ ] O *trip* emite um span dedicado e um alerta operacional, e permite escalar a humano ou abortar de forma graciosa.
- [ ] A trajectória é preservada para RCA após o *trip*.

### Detalhes Técnicos
- Componentes: Agent Runtime (RT), Escalonador (SCH), OBS; integração com a máquina de estados durável (`paused`/`timed_out`, EPIC-02).
- Limiares configuráveis por classe de agente; avaliador multi-sinal desacoplado dos colectores de sinal.
- O wall-clock leva ao estado durável `timed_out`; a token velocity partilha o disjuntor de custo com ADR-008.

### Testes Requeridos
- Integração: agente em loop de custo dispara *trip* por cost velocity e transita para `paused`.
- Integração: run que excede wall-clock transita para `timed_out`.
- Verificação: após *trip*, a trajectória permanece íntegra e o span de *trip* está presente.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e verificados por teste.
- [ ] Transições para estado durável coerentes com a máquina de estados (EPIC-02).
- [ ] Gates de CI/CD verdes; scan de segredos limpo.
- [ ] Cross-ref a `tecnica/08` §6 e `tecnica/02`/`tecnica/03` actualizada.

### Handoff para Claude Code
```text
És o executor do ticket AOS-080 do AOS. Depende de AOS-076 e AOS-078 (Done).
Lê specs/EPIC-08 (AOS-080) e tecnica/08 §6; consulta a máquina de estados em specs/EPIC-02.
Implementa o circuit breaker multi-sinal para o agente vivo em loop: cost/token velocity,
wall-clock e ausência de progresso (o action-dedup por hash chega em AOS-081). Ao dar trip,
transita o run para paused/timed_out (nunca kill cego), emite span de trip + alerta e preserva
a trajectória para RCA. Limiares configuráveis por classe de agente. Testa loop de custo e
excesso de wall-clock. Corre gates locais antes do PR.
```

---

## AOS-081 — Detecção de loop semântico (action-dedup por hash tool+args)

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-076, AOS-080 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md` §6, ADR-010 |

### Contexto
Entre os sinais do circuit breaker está o **action-dedup**: um `hash(tool+args)` repetido acima de um limiar indica o agente a repetir a mesma acção sem efeito — o loop semântico que a detecção de liveness por lease nunca via. Este ticket entrega o colector de sinal que alimenta o breaker de AOS-080.

### Objectivo
Implementar a detecção de loop semântico por deduplicação de acções via `hash(tool+args)`, sinalizando repetição acima de um limiar configurável e fornecendo o sinal ao avaliador multi-sinal do circuit breaker.

### Critérios de Aceitação
- [ ] Cada tool call é hasheada de forma estável por `hash(tool+args)` (já registada no span `execute_tool`, AOS-076).
- [ ] A repetição do mesmo `hash(tool+args)` acima de um limiar configurável é sinalizada como loop semântico.
- [ ] O sinal integra o avaliador multi-sinal do circuit breaker (AOS-080), contribuindo para o *trip*.
- [ ] Argumentos semanticamente equivalentes produzem hash estável (normalização determinística antes do hash).

### Detalhes Técnicos
- Componentes: Reference Monitor (RM) / OBS (produtor do hash), avaliador do circuit breaker (RT).
- Normalização canónica de `args` antes do hash para evitar falsos negativos por ordenação/formatação.
- Janela deslizante de contagem por trajectória; limiar por classe de agente.

### Testes Requeridos
- Unit: hash estável para args equivalentes; hash distinto para args diferentes.
- Integração: agente que repete a mesma tool call N vezes dispara o sinal e contribui para o *trip*.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e verificados por teste.
- [ ] Ausência de falsos negativos por formatação de args (normalização validada).
- [ ] Gates de CI/CD verdes; scan de segredos limpo.

### Handoff para Claude Code
```text
És o executor do ticket AOS-081 do AOS. Depende de AOS-076 e AOS-080 (Done).
Lê specs/EPIC-08 (AOS-081) e tecnica/08 §6.
Implementa a detecção de loop semântico por action-dedup: normaliza args de forma canónica,
calcula hash(tool+args) estável e sinaliza repetição acima de um limiar configurável por janela
deslizante. Liga o sinal ao avaliador multi-sinal do circuit breaker (AOS-080). Testa estabilidade
do hash e disparo por repetição. Corre gates locais antes do PR.
```

---

## AOS-082 — Wide events (capturar tudo, filtrar no query-time)

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-076 |
| Bloqueia | AOS-085 |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md` §7, ADR-010 |

### Contexto
O plano-base filtrava no *emit-time* (*"diagnósticos auto-limpam, só emito sinais operator-fixable"*), o que esconde padrões sistémicos: o que não parece accionável hoje é a pista da falha de amanhã. O AOS substitui-o pelo padrão wide events — capturar tudo, num evento largo e de alta cardinalidade por unidade de trabalho, e filtrar no query-time.

### Objectivo
Enriquecer cada span num wide event de alta cardinalidade, com todas as dimensões relevantes (principal, modelo, tokens, custo, latência, decisão de política, taint, versões pinadas), de modo que perguntas novas se respondam sobre dados já recolhidos, sem reinstrumentar.

### Critérios de Aceitação
- [ ] Cada unidade de trabalho emite um wide event com as dimensões: principal (NHI), modelo, tokens, custo, latência, decisão de política (PDP), taint e versões pinadas.
- [ ] Não há decisão de descarte no emit-time; a filtragem é sempre no query-time.
- [ ] Uma pergunta analítica nova (ex.: custo por tenant e por modelo) responde-se por agregação *ad hoc* sem reinstrumentar.
- [ ] Os wide events são marcados como diagnósticos efémeros com TTL, distintos do audit trail permanente (AOS-083).

### Detalhes Técnicos
- Componentes: camada `otel_genai` (OBS), backend de consulta de alta cardinalidade.
- Enriquecimento de spans com atributos de todas as dimensões relevantes; sem sampling que perca cardinalidade útil.
- TTL por classe de diagnóstico; separação física dos wide events face ao audit WORM.

### Testes Requeridos
- Integração: um run produz wide events com todas as dimensões exigidas.
- Query: pergunta analítica não prevista à instrumentação responde-se por agregação sobre eventos existentes.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e verificados por teste.
- [ ] Separação clara entre wide events efémeros (TTL) e audit permanente confirmada.
- [ ] Gates de CI/CD verdes; scan de segredos limpo.

### Handoff para Claude Code
```text
És o executor do ticket AOS-082 do AOS. Depende de AOS-076 (Done).
Lê specs/EPIC-08 (AOS-082) e tecnica/08 §7.
Implementa o padrão wide events: enriquece cada span com principal, modelo, tokens, custo,
latência, decisão de política, taint e versões pinadas. Sem filtragem no emit-time — tudo se
filtra no query-time. Marca wide events como diagnósticos efémeros com TTL, distintos do audit
WORM (AOS-083). Testa cobertura de dimensões e uma query analítica não prevista. Corre gates
locais antes do PR.
```

---

## AOS-083 — Audit hash-chain + WORM (pipeline)

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-076, EPIC-01 (Event Store) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md` §8.1, ADR-010, ADR-011, `tecnica/09` |

### Contexto
O audit trail deixa de ser *"append-only por convenção"* em SQLite e passa a ser hash-chained + WORM assinado, fisicamente separado dos diagnósticos efémeros. Cada registo inclui o hash do registo anterior, formando uma cadeia em que qualquer adulteração é detectável. É este pipeline que encerra o cenário *The Audit Log Lied*: o audit responde sempre à pergunta *quem autorizou*.

### Objectivo
Implementar o pipeline de audit *tamper-evident*: redação de PII na ingestão → registo (quem/o quê/quando/resultado) → hash-chain → assinatura e selo periódico → armazenamento WORM com retenção e legal hold, com verificação de integridade e integração de crypto-shredding.

### Critérios de Aceitação
- [ ] Cada registo de audit contém quem (NHI e cadeia de delegação), o quê (tool call, decisão do PDP), quando e o resultado.
- [ ] Cada registo inclui o hash do registo anterior (hash-chain); qualquer adulteração é detectável por verificação.
- [ ] A cadeia é periodicamente assinada e selada em armazenamento WORM, com retenção e legal hold configuráveis.
- [ ] A redação/tokenização de PII ocorre na ingestão; o audit é fisicamente separado dos diagnósticos efémeros (AOS-082).
- [ ] O crypto-shredding por titular torna os dados pessoais irrecuperáveis (GDPR Art. 17) mantendo a cadeia íntegra e verificável (ADR-011).
- [ ] Existe um verificador de integridade *tamper-evident* que valida a cadeia ponta-a-ponta.

### Detalhes Técnicos
- Componentes: OBS (pipeline de audit), Event Store (EPIC-01), vault de chaves para crypto-shredding, storage WORM.
- Hash-chain sobre registos canónicos; selagem periódica assinada; retenção/legal hold por classe.
- Integração de conformidade detalhada em `tecnica/09` e EPIC-09; aqui entrega-se o pipeline e a mecânica de shredding.

### Testes Requeridos
- Unit: encadeamento de hash e detecção de adulteração de um registo intermédio.
- Integração: crypto-shredding por titular remove o payload mas mantém a cadeia verificável.
- Verificação: verificador de integridade valida cadeia íntegra e falha em cadeia adulterada.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e verificados por teste.
- [ ] Verificador de integridade demonstrado contra cadeia íntegra e adulterada.
- [ ] Redação de PII e crypto-shredding validados (cruza com ADR-011 / EPIC-09).
- [ ] Gates de CI/CD verdes; scan de segredos limpo.

### Handoff para Claude Code
```text
És o executor do ticket AOS-083 do AOS. Depende de AOS-076 (Done) e de EPIC-01 (Event Store).
Lê specs/EPIC-08 (AOS-083) e tecnica/08 §8.1; consulta tecnica/09 para conformidade.
Implementa o pipeline de audit tamper-evident: redação de PII na ingestão → registo
(quem/o quê/quando/resultado) → hash-chain → assinatura e selo periódico → WORM com retenção e
legal hold. Adiciona verificador de integridade e crypto-shredding por titular (GDPR Art. 17,
ADR-011) que mantém a cadeia verificável. Separa fisicamente do audit os diagnósticos efémeros.
Testa detecção de adulteração e shredding. Corre gates locais e scan de segredos antes do PR.
```

---

## AOS-084 — Eval harness ligado ao trace (gen_ai.evaluation.result)

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-077, EPIC-11 (Eval harness / golden-sets) |
| Bloqueia | — |
| Responsável sugerido | QA |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md` §8.2, ADR-010, ADR-012, `specs/EPIC-11` |

### Contexto
O eval-driven development torna-se viável precisamente porque a trajectória completa está sempre no backend (AOS-077). Cada avaliação — de um golden-set curado e estável, ou de datasets derivados de falhas — é registada como span `gen_ai.evaluation.result` ligado ao trace que avaliou. A avaliação não é um relatório à parte, mas um span de primeira classe, correlacionável com tokens, custo e decisões de política da trajectória original.

### Objectivo
Ligar o eval harness (definido em EPIC-11) ao backend de traces, registando cada avaliação como span `gen_ai.evaluation.result` correlacionado ao trace avaliado, e expondo o resultado ao eval-gate de admissão de auto-modificações (ADR-012).

### Critérios de Aceitação
- [ ] Cada avaliação é registada como span `gen_ai.evaluation.result` ligado por `trace_id` à trajectória avaliada.
- [ ] O resultado da eval é correlacionável com os tokens, o custo e as decisões de política da trajectória original.
- [ ] O harness suporta golden-set curado e estável e datasets derivados de falhas.
- [ ] O trace-diffing contra baseline apanha regressões *novas* que os datasets de falhas passadas não apanhariam.
- [ ] O resultado da eval é consumível pelo eval-gate de auto-modificação (ADR-012, EPIC-11).

### Detalhes Técnicos
- Componentes: eval harness (EPIC-11), backend de observabilidade (OBS), camada `otel_genai`.
- Span `gen_ai.evaluation.result` com referência ao trace-alvo; suporta trace-diffing vs baseline.
- O harness em si (runner, golden-sets) pertence a EPIC-11; aqui entrega-se a ligação ao trace e o registo do span.

### Testes Requeridos
- Integração: uma eval sobre um trace existente produz span `gen_ai.evaluation.result` ligado por `trace_id`.
- Integração: trace-diffing detecta regressão nova introduzida numa alteração de skill.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e verificados por teste.
- [ ] Ligação ao eval-gate de auto-modificação demonstrada (cruza com EPIC-11 / ADR-012).
- [ ] Gates de CI/CD verdes; scan de segredos limpo.
- [ ] Cross-ref a `tecnica/08` §8.2 e `specs/EPIC-11` actualizada.

### Handoff para Claude Code
```text
És o executor do ticket AOS-084 do AOS. Depende de AOS-077 (Done) e de EPIC-11 (eval harness).
Lê specs/EPIC-08 (AOS-084), tecnica/08 §8.2 e specs/EPIC-11 (golden-sets/eval-gate).
Liga o eval harness ao backend de traces: regista cada avaliação como span
gen_ai.evaluation.result correlacionado por trace_id à trajectória avaliada. Suporta golden-set
curado e datasets de falhas, e trace-diffing vs baseline para apanhar regressões novas. Expõe o
resultado ao eval-gate de auto-modificação (ADR-012). Não reimplementes o runner de EPIC-11.
Testa ligação de span e trace-diffing. Corre gates locais antes do PR.
```

---

## AOS-085 — Dashboards + SLIs/SLOs

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-076, AOS-078, AOS-082 |
| Bloqueia | AOS-086 |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md` §7, `tecnica/10`, ADR-009, ADR-010 |

### Contexto
O pilar de métricas com SLIs/SLOs assenta na agregação *ad hoc* sobre os wide events (AOS-082). São SLIs críticos do AOS: cache-hit-rate (ADR-009), overhead de mediação p95 (< 15 ms), custo por trajectória e override-rate. Os dashboards tornam visíveis os padrões sistémicos que a filtragem no emit-time escondia, incluindo o cache thrash invisível.

> **Nota cruzada (AOS-034 — EPIC-03, Done).** O Escalonador já expõe **métricas de saturação e reserva de headroom** (`packages/control-plane/scheduler/metrics.go` + `slo.go`) através de uma **porta `Meter` zero-dep** análoga à `agentruntime.Tracer` (counters/gauges/histograms com **nomes/atributos OTel-estáveis**, sem SDK). Já traz SLIs/SLOs em **config versionada** (`SLOConfig`, SemVer fail-closed), **alertas deterministas** (headroom crítico / saturação sustentada) e um **dashboard mínimo agregado** (`DashboardSnapshot`) construído por agregação **query-time** sobre wide events (o `RecordingMeter` não filtra no emit-time). O AOS-076 (instrumentação OTel) e este ticket (AOS-085) são o **ponto de sutura**: o adaptador OTel real implementa a porta `scheduler.Meter` mapeando as strings estáveis para `instrument.Name`/`attribute.Key` **sem renomear**, e os dashboards deste ticket **consomem** os SLIs de saturação/headroom já definidos — sem reinstrumentar o plano de controlo. A saturação/headroom do EPIC-03 juntam-se assim ao cache-hit-rate/overhead/custo/override-rate do EPIC-08 no mesmo pilar de métricas.

### Objectivo
Construir dashboards operacionais e definir SLIs/SLOs sobre os wide events e spans, cobrindo cache-hit-rate, overhead de mediação p95, custo por trajectória e override-rate, com metas alinhadas aos drivers não-funcionais.

### Critérios de Aceitação
- [ ] Dashboard de observabilidade agrega, sobre wide events/spans: cache-hit-rate, overhead de mediação p95, custo por trajectória e override-rate.
- [ ] Cada SLI tem um SLO explícito alinhado aos drivers não-funcionais (ex.: cache-hit-rate > 80%, overhead de mediação p95 < 15 ms).
- [ ] O cache thrash torna-se visível via cache-hit-rate como SLI (ADR-009).
- [ ] Os dashboards permitem drill-down do agregado até ao trace/span individual.
- [ ] Os SLIs são consumíveis pelos alertas (AOS-086).

### Detalhes Técnicos
- Componentes: backend de observabilidade (OBS), camada de dashboards (cruza com `tecnica/10`).
- SLIs derivados de agregação sobre wide events; SLOs versionados; ligação drill-down span↔dashboard.
- Sem instrumentação nova: reutiliza AOS-076/078/082.

### Testes Requeridos
- Integração: dashboard reflecte SLIs correctos a partir de um run conhecido.
- Verificação: drill-down de um SLI degradado chega ao trace responsável.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e verificados por teste/validação.
- [ ] SLOs documentados e alinhados aos drivers não-funcionais do `_BRIEF` §4.
- [ ] Gates de CI/CD verdes; scan de segredos limpo.
- [ ] Cross-ref a `tecnica/10` (observação operacional) actualizada.

### Handoff para Claude Code
```text
És o executor do ticket AOS-085 do AOS. Depende de AOS-076, AOS-078 e AOS-082 (Done).
Lê specs/EPIC-08 (AOS-085), tecnica/08 §7 e tecnica/10.
Constrói dashboards e define SLIs/SLOs sobre wide events/spans: cache-hit-rate (>80%, ADR-009),
overhead de mediação p95 (<15 ms), custo por trajectória e override-rate. Suporta drill-down do
agregado ao trace. Não adiciones instrumentação nova — reutiliza AOS-076/078/082. Os SLIs devem
alimentar os alertas (AOS-086). Valida contra um run conhecido. Corre gates locais antes do PR.
```

---

## AOS-086 — Alertas a partir dos SLIs

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-085 |
| Bloqueia | — |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md` §7, `tecnica/10`, ADR-009, ADR-010 |

### Contexto
Os SLIs só protegem se dispararem acção. Os alertas a partir dos SLIs fecham o ciclo operacional: cache thrash invisível, explosão de custo silenciosa, overhead de mediação a degradar-se e override-rate a subir (approval theater) passam a produzir sinal accionável, em vez de padrões que só se descobrem post-mortem.

### Objectivo
Definir regras de alerta a partir dos SLIs/SLOs de AOS-085, com limiares, severidades e encaminhamento accionáveis, ligadas aos runbooks operacionais e evitando ruído de baixo valor.

### Critérios de Aceitação
- [ ] Cada SLO crítico tem uma regra de alerta com limiar e severidade explícitos (ex.: cache-hit-rate abaixo do alvo, custo por trajectória acima do orçamento).
- [ ] Os alertas encaminham para o responsável/runbook correcto (cruza com `tecnica/10`).
- [ ] O cache thrash e a explosão de custo silenciosa produzem alerta antes do impacto significativo.
- [ ] As regras minimizam falsos positivos (janelas e limiares calibrados), evitando fadiga de alerta.
- [ ] Existe um teste que dispara sinteticamente cada alerta crítico e verifica o encaminhamento.

### Detalhes Técnicos
- Componentes: camada de alerta sobre os SLIs (OBS/SRE), integração com runbooks (`tecnica/10`).
- Regras declarativas por SLO; severidades e rotas de escalonamento; supressão/agrupamento contra ruído.
- Reutiliza os SLIs de AOS-085; sem métricas novas.

### Testes Requeridos
- Integração: violação sintética de um SLO dispara o alerta esperado com a severidade certa.
- Verificação: encaminhamento do alerta chega ao destino/runbook correcto.

### Definition of Done
- [ ] Critérios de Aceitação satisfeitos e verificados por teste sintético.
- [ ] Regras calibradas para baixo ruído; encaminhamento validado.
- [ ] Gates de CI/CD verdes; scan de segredos limpo.
- [ ] Cross-ref a `tecnica/10` (runbooks/alertas) actualizada.

### Handoff para Claude Code
```text
És o executor do ticket AOS-086 do AOS. Depende de AOS-085 (Done).
Lê specs/EPIC-08 (AOS-086), tecnica/08 §7 e tecnica/10.
Define alertas declarativos a partir dos SLIs/SLOs de AOS-085: limiar, severidade e encaminhamento
para runbook. Garante que cache thrash e explosão de custo silenciosa alertam antes do impacto,
com limiares/janelas calibrados para baixo ruído. Reutiliza os SLIs existentes (sem métricas novas).
Testa violação sintética de cada SLO crítico e o encaminhamento. Corre gates locais antes do PR.
```

---

## Adenda pós-encerramento — defeito apurado em produção

Esta secção existe pelo mesmo motivo da adenda da `EPIC-25`: um defeito no que este epic entregou
foi medido **depois** do encerramento, e abrir um epic novo para um ticket seria espiral de
processo. O ticket entra aqui, no epic que é dono do artefacto.

## AOS-398 — O SLI de overhead de mediação mede a execução da tool, não a decisão, e acende dois `critical` em cada tool call

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 3 — Escala e controlo |
| Tipo | fix |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-085 (o SLI), AOS-086 (os alertas), AOS-274 (o avaliador no nó) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/19_Visao_End_to_End.md` §4/§7, `tecnica/08_Observabilidade_Evals.md` §7.1, `docs/adr/ADR-026-overhead-de-mediacao-e-a-janela-da-decisao.md`, `docs/runbooks/RB-04.md`, `docs/governance/REGISTO-Deferimentos.md` (`DEF-281`) |

### Contexto

Fecha **DEF-281**, aberto desde 2026-08-27 e declarado no código desde então.

O SLI `mediation_overhead_p95` derivava da latência do span `execute_tool`. Esse span **envolve a
execução da tool**: `Monitor.evaluate` chama `m.dispatch` antes de devolver a decisão, pelo que só
fecha depois de a tool correr. O que o SLO de 15 ms exprime (`tecnica/19` §4) é o overhead da
**decisão** — o que a mediação acrescenta —, que os selos `tool.call.mediated.latency_ns` mediram em
**2–8,6 ms** nos runs reais. A execução em gVisor mediu **0,6–1,8 s** no E2E de 2026-09-15. Duas
ordens de grandeza entre o que se media e o que se dizia medir.

**Observado em produção a 2026-09-15/16.** O run `run-delegado-1789519407` — dois turnos, **uma**
tool call `doc_read` — fez disparar `mediation_overhead_high` (catálogo `mediation`) e
`mediation_overhead_p95_high` (catálogo `operational`), ambos `critical`, com `valor=1.21099128e+09`
ns contra `slo=1.5e+07` ns, streak a subir até 4, sobre **uma** amostra. Quando a janela de 5 min
rolou, o SLI voltou a zero amostras e o alerta calou-se. Antes, a 2026-08-27: 1 amostra, `3,047 s`.

Em qualquer nó com sandbox real, uma tool call normal violava o SLO por duas ordens de grandeza e
produzia um `critical` com rota para o **RB-04 («Falha de PDP»)** — que manda depurar a peça sã. O
dano não é o ruído: é que um alerta que toca sempre ensina a ignorar a classe inteira, e o custo
cobra-se no `critical` verdadeiro que ninguém vai ver.

O `tecnica/19` contribuía para o erro: o §4 aplica os 15 ms à avaliação de política, o §7 (S-02f)
listava `EXEC` **dentro** da cadeia orçamentada, e nenhuma das leituras estava marcada como
vinculativa. A arbitragem está no **ADR-026**.

### Objectivo

Separar as duas medidas, decidindo qual delas o SLO governa: o SLI passa a medir só o **overhead da
decisão** (a janela que termina no selo pré-efeito, antes do despacho), o alvo de 15 ms mantém-se
porque passa a ser comparável com o que se mede, e a **duração da tool call mediada** fica
observável e **sem SLO** até haver alvo ratificado.

### Critérios de Aceitação

- [x] O Reference Monitor mede a janela da decisão — política, obrigações e selo pré-efeito,
      **excluindo** o despacho — e publica-a em `Decision.DecisionLatency` e no atributo de span
      `aos.mediation.decision_latency_ns`
- [x] `overheadP95SLI` deriva desse atributo; mantém o filtro da decisão e **não** cai para a
      latência do span quando o atributo falta (`Samples == 0`, `avaliavel="0"`)
- [x] O selo `tool.call.mediated.latency_ns` fica **inalterado** (contrato de fio ancorado no WORM)
- [x] Teste de regressão com os números do incidente: decisão de 8,6 ms + execução de 1,21 s **não**
      viola o SLO de 15 ms, e não acende nenhum dos dois `critical` em nenhum dos dois catálogos
- [x] Teste do sinal: uma **decisão** de 120 ms continua a acender `mediation_overhead_high` e
      `mediation_overhead_p95_high` — a correcção não é um silenciador
- [x] `tecnica/19` §4 (linha RM), §7 (S-02f) e §8 coerentes com a escolha; `tecnica/08` ganha a §7.1
      com a tabela dos quatro SLIs que o `slo.go` já citava e que não existia
- [x] ADR-026 ratificado; `DEF-281` fechado no registo de deferimentos
- [x] RTM regenerada; `rtm`, `ref-lint`, `estado-citado` e `deferrals` verdes

### Detalhes Técnicos

- `packages/kernel/reference-monitor/monitor.go`, `decision.go` — a leitura da janela e o atributo
  de span; a anotação vive no `defer` de `Mediate`, que cobre todos os caminhos de retorno.
- `packages/substrate/otel-genai/semconv.go`, `wide_event.go`, `slo.go` — a constante do atributo, o
  campo tipado derivado do bag, e a nova fonte do SLI.
- Sem instrumentação nova no sentido de cronómetro novo: a janela já era lida para selar o
  `tool.call.mediated`; o que faltava era atravessar a fronteira até ao wide event.

### Testes Requeridos

- Unidade (kernel): a janela da decisão exclui o despacho, com relógio manual; o span publica-a; num
  deny as duas janelas coincidem; o selo não muda de significado.
- Unidade (substrate): regressão com os números de produção; o sinal continua a disparar; um span
  sem a medida não entra na amostra; a derivação sobrevive à projecção span → wide event.
- Integração (nó): o avaliador de SLOs do AOS-274 continua a disparar sobre spans reais.

### Definition of Done

- [x] Critérios de Aceitação satisfeitos e verificados por teste
- [x] Gates de CI/CD verdes; scan de segredos limpo
- [x] ADR-026 e cross-refs (`tecnica/08`, `tecnica/19`, RB-04) actualizados

### Estado

**IMPLEMENTADO.** Criado e executado a 2026-09-16. Fecha `DEF-281`.

## AOS-401 — O SLI de overhead de mediação ainda conta a escrita do selo e continua a violar o SLO em produção

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Fase 3 — Escala e controlo |
| Tipo | fix |
| Prioridade | P0 |
| Estimativa | S |
| Dependências | AOS-398 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/adr/ADR-026-overhead-de-mediacao-e-a-janela-da-decisao.md` (Emenda), `tecnica/08_Observabilidade_Evals.md` §7.1, `tecnica/19_Visao_End_to_End.md` §4/§7, `docs/runbooks/RB-04.md` |

### Contexto

O AOS-398 entrou em produção como **v0.1.15** a 2026-09-16 (digest `3aeb256b…`). A verificação no
servidor, com o run `run-delegado-1789569005` (tool `doc_read` em gVisor), mediu o SLI
`mediation_overhead_p95` em **30,8 ms com 2 amostras e 32,7 ms com 7**, contra 15 ms, com
`aos_slo_breached=1` nos dois catálogos e o streak de `mediation_overhead_high` e
`mediation_overhead_p95_high` a subir até **2 de 3**.

A correcção funcionou na metade que prometia — o SLI desceu de 1,21 s para ~31 ms, a execução no
sandbox saiu —, mas o ADR-026 §1 tinha decidido deixar **dentro** da janela a escrita durável do selo
de auditoria. A política sempre coube em 2–8,6 ms (o `latency_ns` dos selos); a diferença
atribui-se, **por inferência**, à escrita no Event Store e no WORM — no run não foi possível separar
as duas metades. *(Correcção do AOS-404: no mesmo run uma das nove políticas levou 17,06 ms, e os ~31 ms
eram o p95 de poucas amostras dominado por duas calls lentas em todos os troços ao mesmo tempo.)* O resultado operacional é o mesmo defeito com outra causa: um `critical` com rota
RB-04 («Falha de PDP») em cada run normal.

### Objectivo

O SLO de 15 ms passa a governar só a **janela da política** (até antes da escrita do selo), e o
kernel publica as duas metades separadas, para que a escrita do selo deixe de ser inferida.

### Critérios de Aceitação

- [x] `Decision.PolicyLatency` e `Decision.AuditWriteLatency`; a política é lida no MESMO instante
      que o `latency_ns` do selo, uma única vez, e política + escrita = decisão num permit
- [x] Span `execute_tool` com `aos.mediation.policy_latency_ns` e `aos.mediation.audit_write_latency_ns`;
      `aos.mediation.decision_latency_ns` mantém o significado que já tinha na v0.1.15
- [x] O SLI deriva da política; um span da v0.1.15 (só com a decisão) fica fora da amostra
- [x] Num deny, a escrita do selo é medida à parte e não entra na política
- [x] Quando o selo do PERMIT falha e a decisão degrada para deny, a política é a medida antes dessa
      escrita e as duas escritas (a falhada e a do `fail()`) somam-se em `AuditWriteLatency` — um sink
      pendurado até ao prazo do pedido não acende o alerta do PDP
      (`TestAOS401_SeloDoPermitQueFalhaNaoEntraNaPolitica`; encontrado na revisão adversarial)
- [x] Regressão com os números da v0.1.15 (política 5 ms + escrita 27,7 ms): não viola o SLO nem acende
      nenhum `critical`; com a fonte da v0.1.15 o teste falha a publicar `3.27e+07`, o valor de produção
- [x] Uma política de 120 ms continua a acender os dois `critical`
- [x] ADR-026 emendado (§1, §2 e a Emenda); `tecnica/08` §7.1, `tecnica/19` §4/§7/§8 e RB-04 coerentes;
      RTM regenerada
- [x] Verificação em produção com a versão seguinte: `policy_latency_ns` abaixo de 15 ms, a escrita
      medida directamente, e os dois `critical` a 0 depois de um run com tool call
      *(**VERIFICADO EM PRODUÇÃO a 2026-09-17, excepto a escrita.** `v0.1.18` (commit `30d245e`, imagem
      `aos-node@sha256:adb7fb64…`, deploy às 00:41Z). O run `run-delegado-1789609369` correu
      `ready → running → complete` entre 00:42:51Z e 00:43:06Z, com uma tool call `doc_read` mediada e
      executada no sandbox. No `/metrics` do nó (lido pela rede `aos_default`), nos dois catálogos:
      `aos_slo_sli{sli="mediation_overhead_p95"} = 6.516382e+06` (**6,52 ms**, contra 30,8–32,7 ms na
      v0.1.15), `aos_slo_samples = 1`, `aos_slo_breached = 0`; `mediation_overhead_high` e
      `mediation_overhead_p95_high` com `avaliavel="1"`, `aos_alert_firing = 0` e `aos_alert_streak = 0`
      nas passagens do avaliador das 00:43:33Z, 00:44:33Z e 00:45:33Z (`a_disparar=0`). O valor do SLI é
      **exactamente** o `latency_ns` do selo `tool.call.mediated` desse run (`6516382`), o que prova
      que o SLI lê a janela da política, o mesmo instante do selo. **NÃO VERIFICADO — a escrita medida
      directamente:** o `aos.mediation.audit_write_latency_ns` é atributo de span, e o colector OTel de
      produção exporta os traces para `debug`, que os descarta sem atributos; o nó também não o expõe
      no `/metrics`. Fica por medir até haver um destino de traces ou uma métrica — a métrica é o **AOS-402**. **Fechado pelo AOS-402 a 2026-09-17** (v0.1.19): a escrita medida directamente no `/metrics` de produção deu p95 8,17 ms nos permits — ver a evidência nesse ticket. A amostra tem uma só
      tool call.)*

### Estado

**IMPLEMENTADO** a 2026-09-16 e **VALIDADO EM PRODUÇÃO** a 2026-09-17 na `v0.1.18`: o SLI mediu 6,52 ms (1 amostra), sem violação nem alertas; a escrita do selo passou a ser medida directamente em produção pelo AOS-402 (v0.1.19, p95 8,17 ms). Numerado AOS-399 na
sessão que o escreveu, sem commit; renumerado AOS-401 porque o AOS-399 foi atribuído entretanto a outro
ticket (EPIC-06). Verificado: suites `-race` do Reference Monitor, do `otel-genai`, de `cmd/aos` e de
`integration`; `build`, `lint`, `layer-lint`, `apex` e `event-catalog` verdes; falha-antes medida por
mutação na fonte do SLI (volta a publicar os 32,7 ms de produção) e no selo do permit falhado (a política
sai com 404 ms em vez de 4 ms). Revisão adversarial independente: nenhum defeito crítico; o médio (selo
do permit falhado) e os baixos foram corrigidos.

---

## AOS-402 — A escrita do selo de mediação fica legível no `/metrics` do nó

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Remediação pós-produção |
| Tipo | fix |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-401 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/adr/ADR-026-overhead-de-mediacao-e-a-janela-da-decisao.md` (Emenda), `tecnica/08_Observabilidade_Evals.md` §7.1, `packages/substrate/otel-genai/audit_write_latency.go`, `packages/cmd/aos/slo_evaluator.go` |

### Contexto

O AOS-401 separou a escrita durável do selo `tool.call.mediated` da janela da política e publicou-a no
span `execute_tool` (`aos.mediation.audit_write_latency_ns`). A verificação em produção da `v0.1.18`
(2026-09-17) confirmou a política (6,52 ms, igual ao `latency_ns` do selo), mas não conseguiu ler a
escrita: o colector OTel de produção exporta os traces para `debug`, que os descarta, e o nó não a
expunha no `/metrics`. A escrita — que era, por inferência, a maior parte dos ~31 ms da v0.1.15 —
continuava sem medida legível.

### Objectivo

O `/metrics` do nó publica a escrita do selo de mediação por decisão, na janela do avaliador de SLOs,
sem SLO nem alerta.

### Critérios de Aceitação

- [x] A medida é derivada no substrato, sobre os mesmos wide events do SLI de overhead, e não no nó:
      `otelgenai.MediationAuditWriteLatency` devolve, por decisão (permit, deny, escalate), as amostras,
      p50, p95 e máximo. Tipo próprio (`AuditWriteLatency`), fora do catálogo de SLIs: sem alvo, sem
      breach, sem alerta. *(Mesma amostra do `overheadP95SLI` — `execute_tool` que decidiu e traz a
      medida —, com duas exclusões: span sem o atributo (Reference Monitor anterior ao AOS-401) e recusa
      por contexto cancelado (`denied_by=context`), que sai antes de escrever e publicaria zero. Testes:
      `TestAOS402_EscritaDoSeloPorDecisaoComPercentis`, `TestAOS402_SpanSemAMedidaNaoEntra`,
      `TestAOS402_RecusaPorContextoCanceladoNaoConta`, `TestAOS402_DerivaDoBagDoSpan`.)*
- [x] O `/metrics` publica `aos_mediation_audit_write_samples{decision}` (sempre, também a zero) e
      `aos_mediation_audit_write_latency_ns{decision,stat="p50|p95|max"}` só para decisões com amostras.
      Nanossegundos, como o `aos_slo_sli` do overhead e o `latency_ns` do selo. *(DECISÃO: gauges da
      janela e não histograma: o avaliador já agrega por janela e um contador cumulativo exigiria uma
      segunda contabilidade no nó. A unidade segue as medidas que se comparam com esta, e não a
      convenção de segundos do Prometheus. `TestAOS402_MetricsExpoeAEscritaDoSeloPorDecisaoSemSLO`
      passa pela torneira de spans e pela passagem real do avaliador, verifica os valores, a ausência
      de percentis sem amostras, um HELP e um TYPE por nome, nomes e valores válidos no formato de
      exposição, e que nenhuma série de SLO ou alerta fala desta medida;
      `TestAOS402_JanelaSemMediacaoSoPublicaAmostrasAZero`; `TestAOS402_SemTorneiraNaoPublicaNada` (com a
      observabilidade OTLP desligada nada sai: `samples` a zero leria-se como «nenhuma mediação»). O
      guarda de formato geral `TestMetricsRespeitaOFormatoDeExposicao` corre sem o avaliador e não vê
      estas famílias, pelo que a validação de formato delas está no teste do AOS-402. O valor
      `denied_by=context` passou a constante partilhada (`otelgenai.DeniedByContext`) entre o
      Reference Monitor e o filtro. **FALHA-ANTES MEDIDA por mutação**: sem a publicação, as séries
      `aos_mediation_audit_write_latency_ns` não existem no `/metrics`. Revisão adversarial
      independente: nenhum crítico, alto ou médio; os quatro baixos foram corrigidos.)*
- [x] `tecnica/08` §7.1, ADR-026 (Emenda) e RB-04 dizem onde se lê a escrita; o banner do avaliador
      declara-a.
- [x] Evidência de sistema: depois de um deploy, um run com tool call deixa no `/metrics` de produção
      `aos_mediation_audit_write_samples{decision="permit"}` ≥ 1 e a escrita medida, o que fecha o
      critério `[~]` do AOS-401. *(**VERIFICADO EM PRODUÇÃO** a 2026-09-17 na `v0.1.19` (commit `e860d6e`,
      imagem `aos-node@sha256:34d137e4…`, deploy às 09:02Z). O run `run-delegado-1789639455` correu
      `ready → running → complete` entre 09:04:17Z e 09:04:39Z, com duas tool calls `doc_read` mediadas e
      executadas no sandbox. Na passagem do avaliador das 09:05:14Z o `/metrics` do nó deu:
      `aos_mediation_audit_write_samples` permit **2**, deny 0, escalate 0;
      `aos_mediation_audit_write_latency_ns{decision="permit"}` p50 **7 376 852**, p95 **8 171 602**,
      max **8 259 908** ns; `aos_slo_sli{sli="mediation_overhead_p95"}` **6 167 793** ns com 2 amostras,
      `aos_slo_breached` 0 e os dois `critical` a 0 (`a_disparar=0`). Os selos `tool.call.mediated` do run
      têm política `latency_ns` 6 306 124 e 3 539 510, cujo p95 interpolado é exactamente o valor do SLI —
      as amostras são as destas tool calls. **Correcção de registo:** a escrita do selo tinha sido
      atribuída, por inferência, a ~25 ms dos ~31 ms que a v0.1.15 mediu (ADR-026, `tecnica/19`, `tecnica/08`).
      Medida directamente custa 6,5–8,3 ms, e política + escrita ≈ 12–13 ms por tool call. A separação
      do AOS-401 mantém-se certa, mas a diferença para os ~31 ms da v0.1.15 não fica explicada pela
      escrita; os documentos foram corrigidos.)*

### Estado

**IMPLEMENTADO e VALIDADO EM PRODUÇÃO** a 2026-09-17 na `v0.1.19`: a escrita do selo mediu p95 8,17 ms nos permits, a primeira medida directa, que desmente a inferência de ~25 ms.

---

## AOS-404 — Os ~31 ms de overhead de mediação da v0.1.15 ficam explicados pelos dados de produção

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Remediação pós-produção |
| Tipo | spike |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-402 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/adr/ADR-026-overhead-de-mediacao-e-a-janela-da-decisao.md` (Emenda), `tecnica/08_Observabilidade_Evals.md` §7.1, `docs/runbooks/RB-04.md`, `packages/platform/audit/filestore.go`, `packages/substrate/eventstore/store.go`, `packages/platform/registry/revalidation` |

### Contexto

A v0.1.15 mediu o SLI de overhead de mediação — então a janela da decisão, política + escrita do
selo — em **30,8 ms com 2 amostras e 32,7 ms com 7** (run `run-delegado-1789569005`). O AOS-401
atribuiu a diferença para a política à escrita do selo, por inferência; o AOS-402 mediu a escrita
directamente na v0.1.19 (6,5–8,3 ms) e desmentiu-a, deixando os ~31 ms por explicar. Os documentos
repetiam ainda que «a política sempre coube em 2–8,6 ms».

### Objectivo

Explicar os ~31 ms com medidas, a partir dos ficheiros de produção, sem reinstalar a v0.1.15, e
corrigir o que o registo afirma.

### Critérios de Aceitação

- [x] **Método.** Há três escritas duráveis no WORM/Event Store por tool call permitida, e cada uma
      carimba o seu instante antes do append:
      1. o selo `registry.revalidation`, escrito pelo hook de revalidação DENTRO da cadeia de hooks
         (`integration/secured.go`, `Revalidator.recordRaw`) — portanto dentro da janela a que o
         `latency_ns` chama política;
      2. o selo de mediação no WORM (`audit.MediationSink`, 1.º sink do `TeeSink`);
      3. o evento `tool.call.mediated` no Event Store (carimba `Ts` antes do `fsync`, `eventstore/store.go`).

      Por tool call: `latency_ns` = política; «antes» = carimbo da revalidação − abertura da política;
      «depois» = carimbo do selo de mediação − carimbo da revalidação; «WORM» = `Ts` do evento − carimbo do
      selo de mediação (inclui também o `json.Marshal` do evento e a espera do stripe, ≈0 sem outro
      escritor do stream); `Ts(evento seguinte do stream) − Ts(tool.call.mediated)` = limite superior do
      Event Store (o seguinte, `sandbox.instance.created`, é causalmente posterior). Aplicado a cópias só
      de leitura do `events.wal` e do `worm.wal` do volume `aos_aos-data` (2026-09-17): **27** permits com
      selo, de runs entre 2026-09-14 e 2026-09-17 (v0.1.12 a v0.1.19); recusas e calls sem selo ficam de
      fora.
- [x] **Verificação do método** contra a medida directa do AOS-402 (run `run-delegado-1789639455`,
      v0.1.19, escritas 8,26 e 6,49 ms no `/metrics`). A decomposição dá WORM 5,14 + Event Store ≤ 3,36 ms e
      WORM 2,70 + Event Store ≤ 3,90 ms, com folga de 0,11–0,24 ms. Verifica a SOMA, não a divisão entre
      WORM e Event Store; e o 8,26/6,49 de cada call é o único emparelhamento compatível com o máximo e o
      p50 de duas amostras, não uma leitura por call.
- [x] **Explicação.** O run da v0.1.15 teve **9** permits e nenhuma recusa. Sete ficaram entre ~5 e ~13 ms
      de decisão (limites superiores). As outras duas foram lentas em **todos os troços ao mesmo tempo**:

      | Call | Política (antes / depois da revalidação) | WORM | Event Store |
      |---|---|---|---|
      | 1.ª | 8,92 ms (0,98 / 7,94) | 7,88 ms | ≤ 15,64 ms (≈15,3 inferido) |
      | 6.ª | 17,06 ms (**6,85** / 10,21) | 5,32 ms | ≤ 10,75 ms (≈10,6 inferido) |
      | outras 7 | 1,88–4,65 ms (0,29–0,49 / 1,59–4,16) | 1,28–4,65 ms | ≤ 2,07–3,92 ms |

      Num run normal, «antes» fica em ~0,3–0,5 ms e «depois» tem o tamanho de um append ao WORM. Na 6.ª,
      até «antes» (identidade e preparação da revalidação, sem escrita) levou 6,85 ms. Não é um PDP lento
      nem escrita a frio (o stream tinha sido escrito 21 ms antes da 1.ª call, e noutros runs a 1.ª call é
      rápida): os dados são compatíveis com **episódios transitórios de I/O ou do nó**, que não identificam.
      O SLI é um p95 com interpolação linear (`percentileNanos`) sobre a janela de 5 min, avaliado a cada
      minuto; com poucas amostras fica colado ao máximo. **Consistência, não prova independente:** os
      30,8 ms (1.ª passagem, calls 1–2) implicam Event Store ∈ [15,28; 15,51] ms na 1.ª call e os 32,7 ms
      (passagem seguinte, calls 1–7) implicam Event Store ∈ [10,41; 10,67] ms na 6.ª — ambos dentro dos
      limites medidos. A política também não coube sempre em 2–8,6 ms.
- [x] **Hipótese de contenção no WORM sem suporte.** O `FileStore.Append` detém um lock global do ficheiro
      durante o `fsync`. Nenhum selo de outra partição foi carimbado dentro das janelas; como esse teste não
      vê um escritor que carimbou antes e ainda detém o lock, as calls 1 e 6 foram revistas com margem de
      segundos: só o selo de revalidação, sequencial na mesma goroutine.
- [x] **Registo corrigido.** ADR-026 (nota posterior), `tecnica/08` §7.1, `tecnica/19`, RB-04 e os
      comentários de `monitor.go`, `decision.go` e `slo.go` deixam de dizer que a política sempre coube
      em 2–8,6 ms ou que os ~31 ms ficam por explicar.
- [ ] **Residual com decisão por tomar.** Das 27 políticas, **4 passaram os 15 ms** (17,06, 17,28, 40,16 e
      103,63 ms; mais uma de 14,19 ms); três estão em runs de 1 a 3 calls. Em todas, o excesso está em
      «depois» da revalidação — o append do selo de revalidação no WORM mais risk-classify, PDP, taint,
      scope, budget e egress —, que os selos guardados não separam. Duas consequências para o SLO actual
      (AOS-401): **a janela da política inclui um `fsync`** (o da revalidação, AOS-381), contra a premissa
      de que só a escrita do selo de mediação é custo de sink; e, sem mínimo de amostras e com
      `sustained_windows: 3`, **um run curto** com uma call lenta mantém o p95 acima do tecto durante a
      janela e pode disparar o `critical` de RB-04 sem PDP degradado (no run de 9 calls da v0.1.15, o p95
      só da política nunca passou os 15 ms). Medir por hook é o passo seguinte: **AOS-405**. A decisão
      entre corrigir e recalibrar espera pelos dados que ele trouxer de produção. *(Primeiros dados, v0.1.21,
      2026-09-17: num run de 5 calls a revalidação teve p50 6,20 ms e máximo 7,41 ms, e o PDP p50 0,52 ms
      — o grosso da política é o hook que escreve o selo durável no WORM, não a decisão. Ver a evidência
      do AOS-405.)*

### Estado

**CONCLUÍDO** a 2026-09-17 (investigação): os ~31 ms estão explicados e o registo corrigido. O
residual fica nomeado acima, à espera de decisão.

---

## AOS-405 — A janela da política de mediação fica partida por hook no span e no `/metrics` do nó

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Remediação pós-produção |
| Tipo | fix |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-404 |
| Bloqueia | a decisão do residual do AOS-404 (corrigir o caminho lento ou recalibrar o SLO) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/adr/ADR-026-overhead-de-mediacao-e-a-janela-da-decisao.md` (Emenda), `tecnica/08_Observabilidade_Evals.md` §7.1, `docs/runbooks/RB-04.md`, `packages/kernel/reference-monitor/monitor.go`, `packages/substrate/otel-genai/hook_latency.go` |

### Contexto

O SLO de overhead de mediação governa a janela da política inteira (AOS-401). O AOS-404 encontrou em
produção 4 de 27 políticas acima dos 15 ms (até 103,63 ms) e, pelo carimbo do selo
`registry.revalidation`, pôs o excesso todo **depois** dele: num troço que junta o `fsync` desse selo
com risk-classify, PDP, taint, scope, budget e egress. Nas 27 calls o troço anterior (identidade e
preparação da revalidação) ficou em ~0,3–1 ms, com uma excepção de 6,85 ms. Os selos guardados não
separam o troço lento, e escolher entre corrigir um hook e recalibrar o SLO sem saber qual pesa seria
voltar a inferir.

### Objectivo

Cada mediação publica a duração de cada hook da cadeia; o `/metrics` do nó publica-a por hook, na
janela do avaliador, sem SLO nem alerta.

### Critérios de Aceitação

- [x] **Kernel.** `Decision.HookLatencies` traz a duração de cada hook que correu, pela ordem da
      cadeia, medida à volta de `Evaluate` e anexada por `defer` em todos os caminhos de retorno de
      `evaluate`: numa recusa, escalada ou erro de hook ficam os hooks até ao que decidiu, esse
      incluído; na recusa por contexto cancelado é nil. Os hooks somam-se dentro de `PolicyLatency`
      (o resto é o próprio RM: registo da tool e imposição de obrigações). O span `execute_tool` ganha
      um atributo por hook, `aos.mediation.hook_latency_ns.<hook>`, somando hooks cuja chave sanitizada
      coincide. *(Com relógio manual: `TestAOS405_PermitTrazALatenciaDeCadaHookPelaOrdemDaCadeia`,
      `TestAOS405_RecusaSoTrazOsHooksQueCorreram`, `TestAOS405_HookComErroTambemTraz`,
      `TestAOS405_EscaladaTrazOsHooksAteAoQueEscalou`, `TestAOS405_ToolNaoRegistadaTrazTodosOsHooks`,
      `TestAOS405_SeloDoPermitQueFalhaGuardaOsHooks` (o caminho em que a política é reposta depois do
      selo falhado), `TestAOS405_NomesRepetidosSomamNoSpan`,
      `TestAOS405_NomesQueSanitizamParaAMesmaChaveSomam`; a soma dos hooks cabe na política em todos.
      `TestAOS405_ContextoCanceladoNaoTemHooks` é um guarda de contrato e passa também sem a
      implementação. **FALHA-ANTES por mutação**: sem anexar as latências à decisão, os testes do
      permit, da recusa, do erro de hook e dos nomes repetidos falham. Revisão adversarial independente:
      sem defeitos no código; somar pela chave sanitizada, voltar a sanitizar na derivação, os testes dos
      restantes caminhos e as correcções de documentação vieram dela.)*
- [x] **Substrato.** `otelgenai.MediationHookLatency` deriva, dos wide events da janela, amostras,
      p50, p95 e máximo por hook, por ordem de nome, sobre a mesma amostra do SLI de overhead e com a
      exclusão da recusa por contexto cancelado. Não se parte por decisão: o custo de um hook é o
      mesmo seja qual for o desfecho, e partir tornaria as amostras poucas demais.
      `MediationHookLatencyAttr` troca por `_` tudo o que no nome não for letra, dígito, `-` ou `_`, e a
      derivação volta a aplicá-lo ao nome que lê do bag: a chave pode vir de outro produtor, e um tab ou
      um byte inválido num rótulo partiria o formato de exposição do `/metrics` inteiro.
      *(`TestAOS405_LatenciaPorHookComPercentis`, `TestAOS405_ExclusoesDaAmostra`,
      `TestAOS405_NomeDoHookFicaSeguro`, `TestAOS405_DerivacaoVoltaASanitizar`,
      `TestAOS405_DerivaDoSpanData`.)*
- [x] **Nó.** O `/metrics` publica `aos_mediation_hook_samples{hook}` e
      `aos_mediation_hook_latency_ns{hook,stat="p50|p95|max"}` em nanossegundos, sem SLO nem alerta.
      *(DECISÃO: ao contrário da escrita do selo, os rótulos não são um conjunto fechado — são os hooks
      que a cadeia do nó compõe —, pelo que sem amostras nada sai, nem `samples` a zero; sem torneira de
      spans também nada sai. `TestAOS405_MetricsExpoeALatenciaPorHookSemSLO` passa pela torneira e
      pela passagem real do avaliador e verifica valores, formato, um HELP e um TYPE por nome e a
      ausência de SLO; `TestAOS405_JanelaSemMediacaoNaoPublicaHooks`. **FALHA-ANTES por mutação**: sem
      a publicação, o primeiro falha.)*
- [x] `tecnica/08` §7.1, ADR-026 (Emenda) e RB-04 dizem onde se lê a latência por hook e como a usar;
      o banner do avaliador declara-a.
- [x] **Evidência de sistema — as séries.** Depois de um deploy, um run com tool calls deixa no
      `/metrics` de produção `aos_mediation_hook_samples` e `aos_mediation_hook_latency_ns` para os hooks
      da cadeia real. *(**VERIFICADO EM PRODUÇÃO** a 2026-09-17 na `v0.1.21` (merge `4efad74`, imagem
      `aos-node@sha256:04778c90…`, nó `healthy`). O run `run-delegado-1789652697` fez 5 tool calls
      mediadas (permit, com selo) entre 12:45:05Z e 12:45:51Z. Na leitura das 12:49Z o `/metrics` deu
      `aos_mediation_hook_samples` = 5 para os **nove** hooks — approval, identity, revalidation,
      risk-classify, policy, taint, scope, budget e egress — e, em ns:

      | Hook | p50 | p95 | máx |
      |---|---|---|---|
      | revalidation | **6 198 446** | 7 187 654 | 7 405 053 |
      | policy | 517 980 | 1 217 162 | 1 255 900 |
      | risk-classify | 64 931 | 2 426 333 | 2 968 173 |
      | identity | 212 237 | 330 103 | 359 562 |
      | budget | 25 368 | 905 917 | 1 125 946 |
      | scope | 14 438 | 20 546 | 21 690 |
      | egress | 3 646 | 14 181 | 16 561 |
      | approval | 2 855 | 20 841 | 25 247 |
      | taint | 1 874 | 10 564 | 12 724 |

      `aos_slo_sli{sli="mediation_overhead_p95"}` = 11 066 053 ns com 5 amostras, sem violação e com os
      dois `critical` a 0. Os selos `tool.call.mediated` do run, lidos de uma cópia só de leitura do
      `events.wal`, têm políticas de 3,73, 11,87, 7,85, 7,77 e 3,36 ms, cujo p95 interpolado é
      exactamente o valor do SLI: as amostras dos hooks são destas cinco calls.)*
- [~] **Evidência de sistema — a soma por call.** O `/metrics` só tem agregados: o p50 ou o máximo de
      dois hooks podem vir de calls diferentes, e os spans com o valor por call são descartados pelo
      colector de produção. A verificação da soma faz-se numa janela com **uma só** mediação, em que cada
      estatística de cada hook é essa call: a soma dos hooks tem de caber em
      `aos_slo_sli{sli="mediation_overhead_p95"}` dessa mesma janela. *(Por verificar: o run acima teve
      cinco calls na mesma janela. Há só coerência — a soma dos p50 dos hooks (7,04 ms) fica abaixo da
      política p50 (7,77 ms), o máximo da revalidação (7,41 ms) abaixo da maior política (11,87 ms) —, e
      a divisão pelo carimbo do selo `registry.revalidation` põe o troço anterior a ele em 0,12–0,55 ms
      nas cinco calls, compatível com identidade p50 0,21 ms. A soma por call provada está nos testes do
      kernel.)*

### Estado

**IMPLEMENTADO e VALIDADO EM PRODUÇÃO** a 2026-09-17 na `v0.1.21`: as séries saem para os nove hooks da
cadeia real, e a primeira medida mostra a **revalidação com quase toda a política** (p50 6,20 ms de uma
política p50 7,77 ms) e o PDP em ~0,5 ms. A soma por call numa janela com uma só mediação fica por
verificar.

---

## Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

---

## AOS-512 — Banco de ensaio mínimo: uma bateria de casos sintéticos corre contra uma rota e devolve taxas, e a primeira corrida testa os separadores do protocolo

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: é uma ferramenta de medição fora do nó. O ADR-036 (a projecção do tail e as suas versões publicadas) e o ADR-039 (a nova tentativa) são citados só como o contrato que o banco mede e NÃO altera: as variantes de protocolo do banco não são versões de projecção. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals (código em `packages/qa`) |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A2 (estado opaco do provider); antecipa da A3 o banco de qualificação |
| Tipo | feat |
| Prioridade | P1: sem ele, cada pergunta sobre o comportamento de um modelo custa uma série de uma hora na fila de produção, e a hipótese dos separadores não se consegue testar de forma isolada |
| Estimativa | L |
| Dependências | AOS-507 (a ficha da forma da resposta, reutilizada), AOS-508 (os providers falsos e o padrão do gate atrás do proxy), AOS-506 (a projecção 1.2.0, o braço de referência). Decisão D5 do dono (tomada) para o modo com modelo real |
| Bloqueia | AOS-513 (a qualificação de um perfil de rota faz-se no banco), AOS-516 (a segunda família é ensaiada aqui antes de produção) |
| Responsável sugerido | Engenheiro de Qualidade |
| Documentos de referência | `docs/reports/desenho-a2-estado-opaco-2026-10-07.md` §5 e §6, `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (fase A2; decisão D5), `docs/reports/wire-live-aos508-2026-10-07.md`, `scripts/ci/wire-live.sh`, `scripts/ci/rota-live.sh`, `packages/platform/model-gateway/projection.go` |

### Contexto

- **Tudo o que se sabe do comportamento do modelo veio da fila de produção.** Uma série de 60
  planos demora cerca de uma hora, gasta o orçamento de produção e não isola variáveis: a versão
  da projecção e os interruptores são do nó inteiro (desenho §5.2).
- **A hipótese dos separadores nunca foi testada isoladamente.** A projecção 1.1.0 trouxe ao
  mesmo tempo a linha de fim de segmento e texto de protocolo novo, e a taxa de primeiras
  tentativas sem tool call passou de 10% para 32% (medido, acompanhamento §5). Em 33 de 34
  falhas da fase A1 o modelo escreveu a tool call como texto. O diagnóstico acordado com o
  dono a 2026-10-08: os separadores `<kind>` e `</kind>` do protocolo são a causa provável —
  **não testada isoladamente** —, e o texto do protocolo foi afinado para um só modelo.
- **O sintoma não é só deste modelo.** A documentação da Anthropic descreve o mesmo defeito num
  dos seus modelos com o raciocínio desligado: a resposta «writes a tool call into its text
  instead of emitting a `tool_use` block»
  (`https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting`, consultada
  a 2026-10-08). É indício, não prova, de que o defeito depende da combinação de modelo,
  raciocínio e texto do protocolo — o que só um banco por rota mede.
- **A resposta vazia.** Nas capturas seladas dos 3 runs `empty_output` (tamanho do criptograma,
  sem decifrar, 2026-10-08) há 5,3 a 5,8 bytes por token de saída, contra 3,5 a 4,8 nos resumos
  bem-sucedidos: a resposta veio toda no raciocínio com `content` vazio. Hipótese fortemente
  apoiada, não provada; a ficha do AOS-507 confirma-a em produção. O banco mede a mesma taxa
  em minutos, com a mesma ficha.
- **O que o proxy faz às formas** (`wire-live`): dá 500 a `content` em partes e a raciocínio em
  objecto; `thinking_blocks` em lista, `reasoning_tokens` e as assinaturas sobrevivem;
  `refusal`, `thinking` e `reasoning_details` vão para `message.provider_specific_fields`.

### Decidido pelo dono

1. **D5 (2026-10-07) — autorizado um posto de ensaio local com o modelo real**, com tecto
   diário. Revê a recusa de medição directa de 2026-10-06, só para o posto.
2. **Chaves e tectos (2026-10-08).** O dono preencheu um ficheiro **fora do repositório**
   (`%USERPROFILE%\.aos-ensaio\chaves.env`). Campos: `KIMI_API_KEY`, `KIMI_API_BASE`,
   `KIMI_MODELOS`, `ANTHROPIC_API_KEY`, `ANTHROPIC_MODELO`, `TECTO_PEDIDOS_DIA_KIMI`,
   `TECTO_PEDIDOS_DIA_ANTHROPIC`, `TECTO_USD_DIA_ANTHROPIC`,
   `ANTHROPIC_REGIAO_DE_PROCESSAMENTO`. Tectos escolhidos: **1000 pedidos por dia por
   fornecedor** e **5 USD por dia na Anthropic**. O posto lê o ficheiro pelo caminho e nunca
   imprime nem regista os valores.
3. **Só documentos de teste.** Nenhum caso contém conteúdo de titular. O ensaio não toca em
   produção.

### Objectivo

Uma ferramenta, fora do nó, que corre uma bateria fixa e versionada de casos sintéticos contra
**uma rota** e devolve um relatório de **taxas** — só contagens e fichas, sem texto das
respostas. Três modos, a mesma bateria: providers falsos (CI), proxy real com falsos (Docker), e
modelo real pelo posto local. A primeira corrida obrigatória com modelo real é a experiência
dos separadores.

### Critérios de Aceitação

**A bateria**

- [x] Casos T1 a T6 do desenho §5.1, com documentos e objectivos escritos por nós e versionados:
      T1 nó de leitura com uma tool; T2 plano de dois nós, leitura e resumo; T3 nó sem tools
      com `plan_input`; T4 duas tool calls no mesmo turno; T5 tool negada e continuação; T6
      argumentos grandes. A bateria tem um digest, e o relatório leva-o.
- [x] Um teste percorre todos os documentos da bateria e falha se algum não tiver a marca de
      documento sintético. Nenhum caso lê ficheiros fora da pasta da bateria.
- [x] As tools dos casos são falsas e locais (devolvem o documento de teste): nenhuma tem
      efeito externo, e todas passam pelo Reference Monitor do nó de ensaio como em produção.

**As taxas**, todas deterministas e em vocabulário fechado

- [x] **Chegada ao 2.º turno com tools**: o pedido que leva o `assistant` com tool calls e a
      mensagem `tool` foi aceite (código HTTP) e deu um turno — a medida do critério P3.
- [x] **Tool call em texto**: primeiras tentativas com `tool_calls_requested = 0` e motivo
      `stop` num caso que exige tool (o `contract_unmet_no_call` de hoje). Não se lê texto.
- [x] **Recusa do próprio objectivo**: medida pelo substituto determinista do desenho §5.1 — os
      factos conhecidos do documento sintético (números e nomes exactos) estão ou não na
      saída. É verificação de factos sobre dados nossos; não há juiz probabilístico. O
      relatório regista só «presentes» ou «ausentes» por caso.
- [x] **Resposta vazia** (`empty_output`), cortada, erro do provider por código HTTP,
      distribuição dos motivos de paragem, recuperado à 2.ª ou 3.ª tentativa.
- [x] **Forma das respostas**: a ficha do AOS-507, reutilizada sem cópia de código, agregada
      por classe.
- [x] Cada taxa sai com numerador, denominador e intervalo de confiança a 95%.

**Os três modos**

- [x] **Falsos (CI).** A bateria corre contra os providers falsos do AOS-508 com **taxas
      esperadas exactas**, presas por teste. Corre em cada PR, sem rede e sem Docker.
- [x] **Proxy real com falsos (Docker).** O mesmo, atrás da imagem de produção do proxy, no
      molde do `ci-wire-live`: opcional, salta sem Docker e redeclara o salto. Não entra no
      `run.sh` como gate obrigatório.
- [x] **Modelo real, pelo posto local.** Um nó de ensaio local, o proxy real e o modelo real.
      Só arranca com um caminho explícito para o ficheiro de chaves; sem ficheiro, ou com um
      campo obrigatório em falta ou ainda com o marcador do exemplo, recusa com exit próprio e
      uma mensagem que nomeia o **campo** e nunca o valor.
- [x] O modo com modelo real **nunca corre em CI**: recusa se a variável de ambiente de CI
      estiver definida, e não é chamado por nenhum script de `scripts/ci`.

**Tectos e contador**

- [x] Os tectos vêm do ficheiro do dono (`TECTO_PEDIDOS_DIA_*`, `TECTO_USD_DIA_ANTHROPIC`). Um
      tecto ausente, zero ou ilegível **recusa o arranque** — não há tecto por omissão.
- [x] Contador **persistente**, por fornecedor e por dia (UTC), num ficheiro na pasta do dono,
      fora do repositório; sobrevive a reinícios. O pedido é contado **antes** de ser enviado.
      Um contador corrompido ou ilegível recusa o arranque (não recomeça do zero).
- [x] Atingido o tecto, a corrida **pára**: o pedido seguinte não sai, o relatório parcial é
      escrito com a causa `tecto_atingido`, e o exit é distinto de sucesso. Teste: tecto de 3
      e uma bateria de 5 ⇒ exactamente 3 pedidos no falso.
- [x] O tecto em USD usa os tokens devolvidos no `usage` e uma tabela de preços declarada na
      configuração do banco. **Sem preço declarado para o modelo, o arranque é recusado.** O
      gasto é estimado, e o relatório diz que é estimativa.
- [x] Antes de começar, a corrida calcula o número de pedidos que vai fazer e recusa se não
      couber no que resta do tecto do dia.

**Segredos e conteúdo**

- [x] Os valores do ficheiro de chaves não aparecem em nenhuma saída: relatório, logs, stdout,
      stderr, mensagens de erro, argumentos de processo, nomes de ficheiro. Teste com
      sentinelas em todos os campos, incluindo no caminho de erro do proxy.
- [x] O relatório não contém texto de respostas nem de raciocínio: só contagens, fichas, os
      digests da bateria, da rota e da configuração, e o nome do modelo. Teste com sentinelas
      nas respostas do falso.
- [x] A região declarada pelo dono (`ANTHROPIC_REGIAO_DE_PROCESSAMENTO`) é copiada para o
      relatório como **declaração**, sem efeito no ensaio.

**A experiência dos separadores** (primeira corrida obrigatória com modelo real)

- [x] Quatro braços sobre o caso T1, uma variável por eixo:

      | Braço | Separadores | Texto do protocolo |
      |---|---|---|
      | A | `<kind>` … `</kind>` (os da 1.2.0) | 1.2.0 |
      | B | sem `<` nem `>` (por exemplo `[[kind]]` … `[[/kind]]`) | 1.2.0, com a mesma frase a nomear o separador |
      | C | `<kind>`, **sem linhas de fim** | 1.2.0 sem a frase da linha de fim |
      | D (controlo) | os da projecção 1.0.0 | 1.0.0 |

      A contra B mede os sinais de menor e maior; A contra C, a linha de fim; D é a linha de
      base medida em produção (10%).
- [x] **As variantes B e C existem só no código do banco.** Não são versões de projecção
      publicadas: `ParseNativeProjectionVersion` continua a recusá-las, nenhum interruptor do
      nó as selecciona, e um teste prova que o binário do nó não as contém.
- [x] Métrica: primeiras tentativas sem tool call. Amostra: **53 pedidos por braço, 212 no
      total** (distingue 10% de 32% com 80% de potência a 5%; efeitos menores não se vêem —
      desenho §5.3). Cabe no tecto diário do Kimi.
- [x] Os braços correm **intercalados** (não um braço de cada vez), com a ordem fixada por uma
      semente registada no relatório.
- [ ] O resultado fica registado na §5 do acompanhamento, por braço, com intervalo de
      confiança, e com a frase exacta do que **não** ficou provado. A experiência corre
      primeiro contra a rota de produção (`kimi-for-coding`).

**Geral**

- [x] Revisão adversarial independente antes da fusão, com mutações sobre o contador, o tecto
      e as sentinelas.

### Fora de âmbito

- **Decidir sozinho.** O banco devolve taxas; não aceita nem recusa um modelo. O arnês de
  qualificação com recusa automática é da fase A3.
- **Publicar uma projecção nova.** Se um braço ganhar, a versão de projecção correspondente
  abre-se em ticket próprio, com emenda ao ADR-036; este ticket só mede.
- Qualquer corrida em produção ou contra dados de titular; `ssh`; a fila de produção.
- Um juiz probabilístico para a recusa do objectivo ou para a tool call em texto.
- Streaming e conteúdo multimodal.
- Gerir as chaves: são do dono, ficam no ficheiro dele, e não passam a ser segredo do
  repositório nem do Vault de produção.

### Estado

**IMPLEMENTADO (2026-10-08); revisão adversarial feita e corrigida; experiência dos separadores corrida com o modelo real a 2026-10-10.**

- **Onde.** Módulo-folha `packages/qa/banco-ensaio` (o 50.º módulo; ninguém o importa), binário
  `aos-ensaio` em `cmd/aos-ensaio`. Não é parte do nó nem da imagem de produção
  (`TestAOS512_ONoNaoContemOBanco`). Como correr cada modo: `packages/qa/banco-ensaio/README.md`
  e `docs/runbooks/PROC-BANCO-DE-ENSAIO.md`.
- **O que é medido é o que o nó enviaria.** Cada caso é um run do Agent Runtime, com as tools
  pelo Reference Monitor, e o pedido sai por `modelgateway.NewProduction` com a projecção
  nativa. Do banco são só um decorador da porta do gateway (variantes e observação) e o
  transporte HTTP que conta o pedido antes de o enviar. O nó de ensaio é mínimo: sem PDP, WORM,
  sandbox nem `aos-orq`; a allowlist e o emissor de identidade são efémeros, criados no arranque.
- **Providers falsos.** O provider falso do banco serve, byte a byte, os corpos do AOS-508 nas
  formas anormais (vazia com `reasoning_content`, cortada, texto sem factos) por uma porta
  pública nova, `packages/platform/model-gateway/wiretest`, que só reexporta `internal/wirefake`.
- **Taxas exactas.** Os números de `TestAOS512_Falso_BateriaInteira_TaxasExactas` (16 runs, 13
  unidades, 27 pedidos) foram derivados à mão do roteiro antes de o teste correr.
- **Modo proxy.** Gate opcional `scripts/ci/banco-ensaio-proxy.sh` (`make ci-banco-ensaio-proxy`),
  fora do `run.sh`. Corrida local de 2026-10-08 com a imagem de produção do proxy: os mesmos
  desfechos do modo falso, 25 pedidos com 200, um 400 e um 500, nenhum 401.
- **Modo real.** Implementado e ensaiado só contra um fornecedor falso
  (`TestAOS512_Real_*`). **Nenhum pedido a um fornecedor real saiu desta entrega.**
- **O que os critérios dizem e o código faz de outra maneira, declarado.** (1) O tecto em
  dólares é opcional — o ficheiro de exemplo do dono di-lo —: ausente, não há tecto em dólares;
  presente e ilegível, a zero ou sem preço declarado, o arranque é recusado. O tecto de pedidos
  é sempre obrigatório. (2) «Tecto de 3 e uma bateria de 5 ⇒ 3 pedidos» está provado ao nível
  do nó de ensaio: pela corrida inteira não se chega lá, porque ela é recusada antes de começar
  quando não cabe no tecto. A paragem a meio da corrida está provada com o tecto em dólares.
  (3) O gasto em dólares só se conhece depois de cada resposta: o tecto pode ser ultrapassado
  pelo custo de um pedido. (4) O canário de recusa do AOS-504 vive no `aos-orq` e não é
  reutilizado: a medida é a dos factos, como este ticket manda.
- **Revisão adversarial (2026-10-08, sobre `6fbf2173`).** Sem bloqueantes; cinco achados
  importantes e cinco menores, todos corrigidos antes da primeira corrida real. (I1) O proxy
  efémero corre com `--rm` e um vigia: sem sinal de vida do banco durante 90 s, ou passado o
  prazo derivado do plano, mata-se; os modos com Docker varrem os órfãos `aos512-*` ao arrancar;
  há o subcomando `limpar`; trata-se o SIGTERM. (I2) O destino da chave é validado — `https`,
  sem utilizador, porta, query nem fragmento, host numa lista embutida por fornecedor, ou
  `--destino-fora-da-lista` com o host exacto — e mostrado antes de enviar. (I3) Ficheiro de
  exclusão no contador (`O_EXCL`, PID e hora): um segundo processo recusa arrancar; temporário
  de nome único. (I4) No modo real não há `--contador`; contador desaparecido com relatórios
  reais de hoje recusa (só `--reconstruir-contador`); campo repetido, tecto acima do máximo,
  `+500` e espaços interiores são erro. (I5) P-valor pelo teste exacto de Fisher e correcção de
  Holm nas três comparações. Menores: os 429 contam-se à parte e o arrefecimento do proxy está
  desligado; três recusas de chave seguidas abortam a corrida; aspas à volta de um valor são
  erro; a guarda «ninguém importa a porta dos falsos» lê os imports pelo parser; os limites do
  relatório declaram a configuração do proxy. As duas mutações que tinham sobrevivido têm teste.
  Ficam como notas, sem alteração: as permissões `0o600` não valem no Windows (m6), e o
  redactor é por coincidência literal (m8).
- **Primeira corrida real (2026-10-08) — medição, defeito e correcção.** A experiência dos
  separadores foi lançada duas vezes e **não teve nenhuma resposta do modelo**: 3 pedidos com
  401 (a chave era de outro produto do fornecedor; a corrida abortou com `chave_recusada`, como
  desenhado) e, com a chave certa, 212 pedidos com 429 por saldo insuficiente na conta. Defeito:
  o banco só abortava com recusas de autenticação, percorreu os 212 pedidos, gastou 212
  unidades do tecto do dia e o relatório só dizia «429». Correcção (`fix(AOS-512)`): o tipo do
  erro lê-se do `error.type` contra uma lista fechada (`chave_recusada`, `saldo_insuficiente`,
  `limite_de_ritmo`, `modelo_desconhecido`, `outro`; a `message` nunca é guardada) e é contado
  no relatório; três 429 desde o primeiro pedido abortam com a causa própria; dez 429 seguidos
  a meio param a corrida; e o modo real faz uma sonda de um pedido antes do primeiro caso — sem
  200, a corrida não começa.
- **A experiência dos separadores correu (2026-10-10), com o Kimi real** (`kimi-for-coding`,
  chave do plano): 212 pedidos e a sonda, todos com 200, sem nenhum limite de ritmo. Falhas de
  tool call à primeira: braço A (1.2.0, `<kind>`) 0 de 53; B (`[[kind]]`) 0 de 53; C (sem
  linhas de fim) 2 de 53; D (1.0.0) 1 de 53. Nenhuma comparação distingue a 5% (Fisher exacto,
  com a correcção de Holm); as três falhas têm o nome da tool no texto. A forma dos separadores
  não é a causa da tool call escrita como texto: a hipótese foi retirada. Limite: um caso (T1)
  e um turno. Registado na §5 do acompanhamento.

## AOS-518 — O banco de ensaio dá um veredicto sobre o modelo: `qualificado`, `recusado` ou `inconclusivo`, com controlos negativos e o digest do próprio relatório

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: é uma experiência nova de uma ferramenta de medição fora do nó. O ADR-036 §2.8 (o perfil da rota e o seu digest) e o ADR-040 §2.11 (a devolução do estado) são citados só como os contratos que o arnês mede e NÃO altera. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals (código em `packages/qa/banco-ensaio`) |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A3 (entrada automática). Rótulo no desenho: «A3-arnês» |
| Tipo | feat |
| Prioridade | P1: sem um veredicto com regra fixa não há nada que um perfil assinado possa citar, nem que o nó possa conferir |
| Estimativa | L |
| Dependências | AOS-512 (o banco, a bateria, os tectos), AOS-513 (o perfil candidato e o seu digest), AOS-516 (o molde do veredicto calculado, na devolução do estado), AOS-508 (os providers falsos). AOS-517 para o digest da imagem do proxy que o relatório cita. Decisões D2, D3 e D7 do dono (tomadas a 2026-10-11) |
| Bloqueia | AOS-519 (o nó confere o veredicto), AOS-522 (o comando encadeia a qualificação), AOS-523 |
| Responsável sugerido | Engenheiro de Qualidade |
| Documentos de referência | `docs/reports/desenho-a3-entrada-automatica-2026-10-11.md` §4.2, §5 e §6 (D2, D3, D7), `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (fase A3; §5 medições), `docs/runbooks/PROC-BANCO-DE-ENSAIO.md`, `packages/qa/banco-ensaio/relatorio.go`, `packages/qa/banco-ensaio/devolucao.go`, `packages/qa/banco-ensaio/tecto.go`, `packages/qa/banco-ensaio/bateria/casos.json` |

### Contexto

- **O banco devolve taxas; não aceita nem recusa um modelo.** Di-lo de si próprio
  (`packages/qa/banco-ensaio/relatorio.go`). Só existe veredicto calculado para a devolução do
  estado, com regra escrita e precedência fixa (`devolucao.go`, AOS-516). O relatório não tem
  digest de si próprio: nada o pode citar.
- **O banco deu verde falso duas vezes na A2** (desenho §5, lição 1): contava como
  «devolvido» o que o gateway decidiu enviar antes de o pedido sair, e como «estado capturado»
  um envelope só com identificadores. Foi a revisão adversarial que o apanhou, não os testes.
- **`cumprida` não prova leitura** (lição 2): a OpenRouter aceitou o segundo turno com e sem
  o raciocínio de volta. Um 2xx diz que o pedido não foi recusado.
- **A qualificação é da rota, não do modelo** (lição 3): modelo, fornecedor, imagem do proxy e
  perfil. Mudar um dos quatro é qualificar outra vez.
- **O tecto salvou dinheiro** (lição 6): a primeira corrida real da A2 gastou 212 pedidos em
  respostas 429 antes de o banco aprender a parar; a corrida de 8 passagens foi recusada antes
  de enviar (673 pedidos no pior caso contra um tecto de 200).
- **A conta sem créditos parou uma fase** (lição 9): quando o que falha é a conta, o veredicto
  não pode ser sobre o modelo.

### Decidido pelo dono (2026-10-11)

- **D2 — o que conta como `qualificado`.** A bateria de hoje (seis casos, sete nós por
  passagem, cinco deles com tools); **8 passagens**, ou seja 40 nós com tools; um só conjunto
  de limiares para todos os modelos; os três controlos negativos. Limiares:

  | # | Medida | Limiar |
  |---|---|---|
  | L1 | Runs com tools cumpridos no fim das tentativas | todos (40 de 40) |
  | L2 | Nós com tools sem tool call à primeira tentativa | no máximo 4 de 40 |
  | L3 | Respostas 4xx do provider nos segundos turnos | zero |
  | L4 | Respostas vazias no fim das tentativas | zero |
  | L5 | Turnos com o modelo servido diferente do esperado | zero |

- **D3 — o modo real corre no posto do dono**, como hoje, e continua a recusar correr em CI.
- **D7 — o orçamento.** Tecto próprio de **300 pedidos reais** por qualificação, com o pior
  caso declarado antes de começar (673 para 8 passagens); **3 USD** por qualificação num
  fornecedor pago ao pedido; o tecto diário do dono, por fornecedor, mantém-se e **sobe** para
  caber o pior caso — a conta continua a fazer-se pelo pior caso; **no máximo duas
  qualificações por perfil por dia**.

### Objectivo

Uma experiência nova do banco, `qualificacao`, que corre a bateria com um perfil candidato e os
controlos negativos, conta contra os limiares L1 a L5 e escreve um veredicto com regra fixa —
calculado das contagens, nunca declarado por quem corre — num relatório que traz o digest de si
próprio e tudo o que identifica a rota qualificada.

### Âmbito

- A experiência `qualificacao` nos três modos do banco (falso, proxy real com falso, modelo
  real).
- A regra do veredicto, no molde da que existe para a devolução: razões **firmes** dão
  `recusado`; razões **passageiras** sozinhas dão `inconclusivo`; `qualificado` só sem
  nenhuma. As razões saem em vocabulário fechado.
- Providers falsos «maus», um por modo de falha, e um falso «bom».
- O relatório ganha: o veredicto e as razões; as contagens por limiar, com a amostra e o
  intervalo de confiança; o digest da bateria e a sua versão; o digest do perfil; o digest da
  rota; o digest da imagem do proxy; o que a corrida **não prova**; o gasto; e o digest do
  próprio relatório, calculado sobre uma forma canónica que o exclui.
- O orçamento da D7, incluindo o contador de qualificações por perfil por dia.

### Critérios de Aceitação

**A regra**

- [ ] O veredicto é função pura das contagens do relatório: dadas as mesmas contagens, o mesmo
      veredicto e as mesmas razões, pela mesma ordem. Um teste de tabela cobre cada razão
      sozinha e as precedências (firme ganha a passageira).
- [ ] São **firmes** (dão `recusado`): L1 a L5 violados com a amostra completa; o perfil
      recusado pelo provider em todos os turnos (4xx de parâmetro).
- [ ] São **passageiras** (dão `inconclusivo`, nunca `qualificado`): a corrida não coube no
      tecto ou parou a meio; a sonda falhou por conta (`saldo_insuficiente`, chave recusada,
      limite de ritmo); três recusas iguais seguidas; erros 5xx ou de transporte acima do que
      deixa a amostra completa; amostra abaixo de 40 nós com tools por qualquer causa.
- [ ] Os limiares aplicam-se às **contagens**, como a D2 os escreveu. O intervalo de confiança
      vai no relatório ao lado de cada contagem e não entra na regra.
- [ ] Cada limiar tem uma **mutação dirigida** que o desloca (de «zero» para «no máximo um»,
      de 4 para 5, de `<=` para `<`) e avermelha pelo menos um teste. A lista das mutações e o
      resultado de cada uma ficam no Estado do ticket.

**Controlos negativos, em CI, em cada PR**

- [ ] Contra cada provider falso «mau», o veredicto é `recusado` com a razão certa, em 100%
      das corridas: (1) não chama a tool; (2) responde vazio; (3) recusa o segundo turno com
      4xx; (4) troca o modelo servido; (5) corta a resposta. Cinco falsos, cinco razões
      distintas — um falso que caia na razão de outro é falha do teste.
- [ ] Contra o falso «bom», `qualificado`, com 40 de 40 nós com tools.
- [ ] Uma corrida interrompida a meio e uma corrida cujo pior caso não cabe no tecto dão
      `inconclusivo`; a segunda **não envia nenhum pedido** (contador do falso a zero).
- [ ] Atrás da imagem fixada do proxy (gate opcional), o falso «bom» dá `qualificado` e pelo
      menos dois dos falsos «maus» dão `recusado`: a regra não depende de correr sem proxy.

**Controlos negativos com o modelo real, no posto do dono**

- [ ] **O Kimi de produção sai `qualificado`.** Se o modelo que já serve bem não passa, o
      arnês ou os limiares estão errados: nesse caso o ticket pára e os números vão ao dono
      antes de qualquer ajuste. Os limiares não se mexem para o resultado dar verde sem
      decisão registada na §4 do acompanhamento.
- [ ] **«Aceite» separa-se de «exigido».** Num perfil que declara devolução obrigatória, a
      qualificação inclui a corrida de controlo sem devolução; se essa corrida passar, o
      relatório traz `exigencia_nao_provada` e essa linha vem **à cabeça** do resumo. Não
      bloqueia o `qualificado` (D2); não pode ser omitida.
- [ ] A duração real de uma qualificação de 8 passagens com os controlos fica medida e
      registada na §5 do acompanhamento (a estimativa do desenho, 15 a 25 minutos, é hipótese).

**O relatório**

- [ ] Traz o digest da bateria, do perfil, da rota e da **imagem do proxy**. A qualificação
      recusa arrancar, antes de enviar, se a imagem do proxy em uso não for a declarada para
      produção (AOS-517), com causa própria.
- [ ] Traz o digest de si próprio: recalculá-lo sobre o ficheiro dá o mesmo valor; mudar um
      byte de qualquer contagem ou do veredicto dá outro. Preso por teste, nas duas direcções.
- [ ] Diz, em vocabulário fechado, o que a corrida **não prova** (pelo menos: seis casos
      sintéticos e documentos curtos; `cumprida` é 2xx e não prova de leitura; a amostra e o
      que ela permite afirmar — com 40 nós e zero falhas, taxa de falha abaixo de cerca de 9%
      a 95%).
- [ ] Não contém texto de respostas do modelo nem conteúdo de documentos: contagens,
      digests e vocabulário fechado, como hoje.

**O orçamento**

- [ ] O pior caso é declarado antes do primeiro pedido e conferido contra o que resta do
      tecto do dia; ao atingir 300 pedidos reais a corrida pára e o veredicto é
      `inconclusivo`. A terceira qualificação do mesmo perfil no mesmo dia é recusada antes
      de enviar, com causa própria. O gasto real (pedidos e USD estimados) fica no relatório.

### O que fica desligado por omissão, e o que tem de ficar inerte

- **Interruptor:** a experiência só corre quando pedida pelo nome (`qualificacao`). O modo
  real continua a exigir o ficheiro de chaves do dono e a recusar correr em CI.
- **Inerte:** as experiências que já existem (a bateria simples, os separadores, a devolução
  do estado) produzem, para as mesmas entradas, relatórios byte a byte iguais aos de antes —
  preso por digest sobre a linha de base do AOS-516. O nó e o gateway não mudam.

### Testes exigidos

- Tabela da regra; os cinco falsos «maus» e o «bom»; as mutações por limiar; o digest do
  relatório nas duas direcções; a inércia das experiências anteriores; o tecto e o contador
  de qualificações por dia.
- **Revisão adversarial por quem não escreveu o arnês, antes de o primeiro veredicto contar**
  (lição 1). O alvo da revisão é o verde falso: procurar uma corrida que dê `qualificado` sem
  o merecer — um provider que responde 500 a tudo, um que nunca traz raciocínio, um que aceita
  tudo. Os achados e as correcções ficam no Estado.

### Riscos

- **Limiares mal escolhidos:** apertados de mais recusam modelos bons, largos de mais não
  recusam nada. Daí o controlo com o Kimi de produção.
- **A bateria pode não distinguir um modelo mau de um bom** (desenho §8): seis casos
  sintéticos. A qualificação apanha um modelo **mau**; quem mede se é **bom** é a fatia em
  produção (AOS-520, AOS-521). O relatório di-lo.
- **O relatório é produzido noutro computador e ninguém prova que o arnês correu** (desenho
  §8). O digest prova que o relatório não foi alterado depois; não prova a corrida. Fica
  escrito no relatório e no runbook como limite, e não se fecha nesta fase.

### O que precisa do dono

- Subir o tecto diário do fornecedor no seu ficheiro, para caber o pior caso de 673 pedidos.
- Correr a qualificação do Kimi de produção no seu posto e entregar o relatório.
- Decidir, se o Kimi não sair `qualificado`, o que muda: o arnês ou os limiares.

### Fora de âmbito

- Casos novos na bateria: entram com versão nova, e um veredicto vale para a versão que cita.
- Limiares por classe de modelo; o limiar aplicado ao limite do intervalo de confiança.
- O arnês a assinar o relatório com chave própria.
- Correr o modo real em CI ou no servidor de produção (D3).
- O comando que encadeia a qualificação e prepara a assinatura: é o AOS-522.
- Qualquer juiz probabilístico: nenhum modelo avalia outro modelo.

### Estado

**Aberto (2026-10-11).** Nada implementado. Pode começar já, em paralelo com o AOS-517; o
critério do digest da imagem do proxy fecha-se quando o AOS-517 declarar o digest de produção.

## AOS-522 — Qualificar e preparar a assinatura num comando; sem veredicto positivo não há nada para assinar; runbook com relógio

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: é uma ferramenta do posto do dono e um procedimento escrito. O ADR-036 §2.8 (o perfil da rota, que o registo de entrada transporta) e o ADR-012 (ratificação humana assinada) são citados só como os contratos que o comando serve e NÃO altera. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals (código em `packages/qa/banco-ensaio`) |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A3 (entrada automática). Rótulo no desenho: «A3-comando» |
| Tipo | feat |
| Prioridade | P2: não acrescenta garantia nenhuma; é a peça que faz a hora do critério caber |
| Estimativa | M |
| Dependências | AOS-518 (a experiência `qualificacao` e o relatório com veredicto), AOS-519 (o formato do registo de entrada e a conferência que o nó faz). Decisões D3 e D4 do dono (tomadas a 2026-10-11) |
| Bloqueia | AOS-523 |
| Responsável sugerido | Engenheiro de Qualidade |
| Documentos de referência | `docs/reports/desenho-a3-entrada-automatica-2026-10-11.md` §3 (passos 1 a 5), §4.6 e §6 (D3), `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (fase A3), `docs/runbooks/PROC-BANCO-DE-ENSAIO.md`, `packages/qa/banco-ensaio/cli.go`, `packages/qa/banco-ensaio/relatorio.go`, `packages/cmd/aos-issuer/main.go`, `deploy/server/README.md` |

### Contexto

- **O critério da fase tem um relógio:** menos de uma hora do «tenho uma chave» ao «o modelo
  serve a primeira fatia», com a qualificação lá dentro (desenho §1 e §3). A qualificação
  são 15 a 25 minutos de máquina (hipótese, por medir no AOS-518); o resto são passos de
  pessoa, e é aí que a hora se perde.
- **O dono aparece três vezes — escrever, assinar, copiar — e só assina uma** (desenho §3).
  Entre a qualificação e a assinatura há hoje trabalho à mão: ler um relatório de taxas,
  calcular digests, compor um ficheiro.
- **O que o dono assina tem de mostrar o que a corrida não prova.** Na A2, `cumprida` foi
  lida como prova de leitura e não era (desenho §5, lição 2). O resumo põe os limites à
  frente, não em rodapé.
- **Repetir até passar é escolher o resultado** (D7): o comando não pode ser um botão de
  tentar outra vez.
- **O nó falha fechado no arranque** (AOS-519): um pacote mal formado, copiado para o
  servidor, é um nó que não arranca. O erro tem de aparecer no posto, antes da cópia.

### Decidido pelo dono (2026-10-11)

- **D3 — o modo real corre no posto do dono**, com o ficheiro de chaves dele; nunca em CI,
  nunca no servidor de produção.
- **D1 — assina o dono**, com a ferramenta que já usa e uma chave só para perfis de modelo,
  **fora do nó e fora do arnês**.
- **D4 — a fatia inicial é 10%**, escrita no registo assinado.

### Objectivo

Um comando do banco encadeia os passos 2 a 4 do caminho de um modelo novo: corre a
qualificação, mostra o resumo de uma página e — só com `qualificado` — deixa pronto o registo
de entrada para o dono assinar. E um procedimento escrito, com o tempo esperado de cada passo.

### Âmbito

- O subcomando do banco (`qualificar`) que recebe o perfil candidato e a fatia, corre a
  experiência `qualificacao` do AOS-518 e escreve, numa pasta de saída: o relatório, o resumo,
  e — só com veredicto positivo — o registo de entrada por assinar, no formato do AOS-519.
- O resumo de uma página.
- A linha de comando exacta para o dono assinar com o `aos-issuer`, impressa no fim.
- A conferência local de um pacote já assinado, pela mesma função que o nó usa.
- O runbook, com relógio: do perfil candidato escrito ao nó recriado.

### Critérios de Aceitação

**Sem veredicto positivo, nada para assinar**

- [ ] Com `recusado` e com `inconclusivo` (um teste cada, contra os falsos do AOS-518), a
      pasta de saída tem o relatório e o resumo e **não tem** registo de entrada — nem
      parcial, nem com outro nome. O código de saída distingue os três veredictos.
- [ ] Uma corrida interrompida a meio não deixa registo de entrada.
- [ ] **Mutação dirigida:** retirar a condição do veredicto faz aparecer um registo com
      `recusado` e avermelha o teste.
- [ ] O registo de entrada preparado cita o digest do relatório **que está na pasta**: um
      teste recalcula-o. O comando não aceita um relatório vindo de fora nem um veredicto por
      argumento: só prepara o registo da corrida que acabou de fazer.

**O resumo**

- [ ] Cabe numa página (no máximo 60 linhas de 100 colunas, preso por teste) e diz, **por
      esta ordem**: veredicto; razões; amostra; o que os controlos negativos mostraram; o que
      a corrida não prova; o gasto. Um teste prende a ordem.
- [ ] Com `exigencia_nao_provada` no relatório, essa linha vem antes de todas as outras,
      incluindo o veredicto.
- [ ] Traz os digests que o dono vai assinar (perfil, relatório, imagem do proxy), a fatia, o
      alias pedido e o modelo esperado — e, se o alias já tiver entrada na tabela em código,
      diz que o perfil a **substitui**.
- [ ] Não contém texto de respostas do modelo, conteúdo de documentos, chaves, nem o caminho
      do ficheiro de chaves.

**A chave privada**

- [ ] O comando não tem argumento, variável de ambiente nem leitura de ficheiro por onde uma
      chave privada de assinatura possa entrar. Preso por teste sobre a lista de argumentos e
      de variáveis lidas. Assinar é um passo separado, do dono, com o `aos-issuer`.
- [ ] O comando recusa correr com `CI` ou `GITHUB_ACTIONS` definidas no modo real, como o
      banco já faz.

**A conferência antes da cópia**

- [ ] Um pacote assinado (registo, assinatura, relatório) é conferido no posto pela **mesma
      função** que o nó usa no arranque, e não por uma cópia dela: aceite aqui, é aceite pelo
      nó; recusado aqui, a causa é a mesma palavra que o nó daria. Teste com um pacote bom e
      com três dos casos de recusa do AOS-519.

**O orçamento**

- [ ] O comando não contorna os tectos do AOS-518: a terceira qualificação do mesmo perfil no
      mesmo dia é recusada antes de enviar, e o pior caso é declarado antes do primeiro
      pedido.

**O runbook**

- [ ] `docs/runbooks/PROC-BANCO-DE-ENSAIO.md` (ou um procedimento próprio ao lado) descreve
      os passos 0 a 8 do desenho §3, com quem faz, o que fica como prova, e o tempo esperado
      de cada um; diz onde o relógio começa e onde pára; e diz o que fazer em cada veredicto.
- [ ] Traz a secção «antes de recriar o nó»: conferir o pacote no posto; drenar a rota;
      copiar; recriar; ler o banner. E a de recuo: retirar o ficheiro e recriar.
- [ ] **Ensaio do procedimento a seco**, com provider falso atrás do proxy fixado e um nó
      local: do perfil escrito ao banner do nó a declarar o candidato. O tempo de cada passo
      fica registado no Estado. Não substitui a medição do AOS-523, que é com modelo real e
      em produção.

### O que fica desligado por omissão, e o que tem de ficar inerte

- **Interruptor:** nenhum em produção — é uma ferramenta do posto do dono, que só corre
  quando chamada. O modo real exige o ficheiro de chaves e recusa CI.
- **Inerte:** os subcomandos e as experiências que o banco já tem dão, para as mesmas
  entradas, a mesma saída byte a byte. O nó não muda.

### Testes exigidos

- Os três veredictos e a pasta de saída; a mutação da condição; a ordem e o tamanho do
  resumo; a ausência de via para a chave privada; a conferência pela função partilhada; o
  ensaio a seco.
- **Controlo negativo do resumo:** um relatório com os controlos negativos em falta não dá
  um resumo que os mostre como passados — dá a linha «controlo não corrido», e o veredicto
  do AOS-518 para esse caso não é `qualificado`.

### Riscos

- **Baixo.** O risco real é o resumo ser lido como garantia: por isso «o que a corrida não
  prova» está na página que se assina.
- **A ferramenta de assinar pode precisar de um subcomando novo** para este tipo de ficheiro:
  é entregável do AOS-519; se ainda não existir, este ticket fica à espera dele para o
  ensaio a seco.

### O que precisa do dono

- Correr o comando no seu posto; ler o resumo; assinar com o `aos-issuer`.
- Copiar o pacote para a pasta de perfis do servidor e recriar o nó.

### Fora de âmbito

- Assinar por conta do dono, guardar a chave, ou automatizar a cópia para o servidor.
- Correr a qualificação no CI ou no servidor.
- Repetir a qualificação sozinho quando o fornecedor actualiza o modelo por baixo do nome.
- A regra do veredicto e os limiares: são do AOS-518.

### Estado

**Aberto (2026-10-11).** Nada implementado. Pode andar ao lado do AOS-519, depois de o
AOS-518 ter o formato do relatório estável.

---

## Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Emissão inicial | Equipa AOS |
| 1.1 | Setembro 2026 | Adenda pós-encerramento: AOS-398 (DEF-281 — o SLI de overhead de mediação media a execução da tool; ADR-026) | Equipa AOS |
| 1.2 | Setembro 2026 | AOS-401: emenda ao ADR-026 depois da verificação da v0.1.15 em produção — o SLO governa só a política, a escrita do selo sai da janela | Equipa AOS |
| 1.3 | Setembro 2026 | AOS-402: a escrita do selo de mediação passa a ser legível no `/metrics` do nó, sem SLO | Equipa AOS |
| 1.4 | Setembro 2026 | AOS-404: os ~31 ms da v0.1.15 explicados por duas tool calls lentas em todos os troços num p95 de poucas amostras; residual da política acima de 15 ms nomeado | Equipa AOS |
| 1.5 | Setembro 2026 | AOS-405: a janela da política partida por hook no span `execute_tool` e no `/metrics` do nó, sem SLO | Equipa AOS |
| 1.6 | Setembro 2026 | AOS-405 validado em produção (v0.1.21): nove hooks no `/metrics`, a revalidação com quase toda a política; soma por call ainda por verificar numa janela de uma mediação | Equipa AOS |
| 1.7 | 2026-10-08 | +AOS-512 (fase A2 da fronteira runtime↔modelo): banco de ensaio mínimo — bateria de casos sintéticos contra uma rota, relatório de taxas sem texto das respostas, três modos (falsos, proxy real com falsos, modelo real pelo posto local com tectos diários do dono), e a experiência dos separadores do protocolo como primeira corrida | Equipa AOS |
| 1.8 | 2026-10-08 | AOS-512 implementado: módulo `packages/qa/banco-ensaio`, três modos, tectos e contador, experiência dos separadores pronta; a primeira corrida com modelo real e a revisão adversarial ficam por fazer | Equipa AOS |
| 1.9 | 2026-10-08 | AOS-512: revisão adversarial feita (sem bloqueantes) e os seus cinco achados importantes e cinco menores corrigidos; falta a primeira corrida com modelo real | Equipa AOS |
| 1.10 | 2026-10-08 | AOS-512: primeira corrida real sem respostas do modelo (3 pedidos com 401, 212 com 429 por saldo insuficiente); o banco passa a abortar em conta sem saldo ou limite de ritmo e a sondar a rota antes da corrida; a experiência dos separadores continua por correr | Equipa AOS |
| 1.11 | 2026-10-11 | AOS-512: a experiência dos separadores correu a 2026-10-10 com o Kimi real (212 pedidos; nenhum braço se distingue a 5%; hipótese retirada) | Equipa AOS |
| 1.12 | 2026-10-11 | +AOS-518 (fase A3, entrada automática): o banco de ensaio ganha a experiência `qualificacao`, que dá um veredicto calculado sobre o modelo (`qualificado`, `recusado`, `inconclusivo`) com limiares do dono, providers falsos «maus» em CI, orçamento próprio e o digest do próprio relatório | Equipa AOS |
| 1.13 | 2026-10-11 | +AOS-522 (fase A3): um comando do banco encadeia a qualificação, o resumo de uma página e a preparação do registo de entrada para o dono assinar — sem veredicto `qualificado` não fica nada por assinar, e a chave privada nunca entra no arnês; conferência local do pacote pela função que o nó usa; runbook com relógio | Equipa AOS |
