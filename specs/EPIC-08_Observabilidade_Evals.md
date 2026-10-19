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

- [x] **O Kimi de produção sai `qualificado`.** Se o modelo que já serve bem não passa, o
      arnês ou os limiares estão errados: nesse caso o ticket pára e os números vão ao dono
      antes de qualquer ajuste. Os limiares não se mexem para o resultado dar verde sem
      decisão registada na §4 do acompanhamento.
      **Medido a 2026-10-11:** `qualificado` — 40 de 40 nós com tools, 16 de 16 sem tools, 3 de 40
      sem tool call à 1.ª tentativa (limite 4), 99 de 99 turnos com o modelo esperado
      (acompanhamento §5). Nenhum limiar foi mexido.
- [ ] **«Aceite» separa-se de «exigido».** Num perfil que declara devolução obrigatória, a
      qualificação inclui a corrida de controlo sem devolução; se essa corrida passar, o
      relatório traz `exigencia_nao_provada` e essa linha vem **à cabeça** do resumo. Não
      bloqueia o `qualificado` (D2); não pode ser omitida.
- [x] A duração real de uma qualificação de 8 passagens com os controlos fica medida e
      registada na §5 do acompanhamento (a estimativa do desenho, 15 a 25 minutos, é hipótese).
      **Medida duas vezes:** 23 min 5 s a 2026-10-11 (Kimi de produção; 7 min 58 s foram o plano,
      com o arranque do proxy em Docker) e 15 min 37 s a 2026-10-12 (`k3`: plano 1 min 26 s, sonda
      10 s, corrida 14 min 1 s). Nenhuma das duas teve a corrida de controlo sem devolução, que só
      existe num perfil que declara devolução obrigatória: a duração «com os controlos» desse caso
      continua por medir.

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

**IMPLEMENTADO em ensaio (2026-10-11); duas revisões adversariais independentes feitas e corrigidas. A qualificação com o modelo real está feita e medida: o Kimi de produção a 2026-10-11 e o `k3` a 2026-10-12, os dois `qualificado`. Nenhum modelo mau real foi examinado.**

- **Onde.** `packages/qa/banco-ensaio`: `qualificacao.go` (a regra e as contagens),
  `qualificacao_corrida.go` (controlo, avisos, bloco do relatório), `orcamento.go` e
  `qualificacoes_do_dia.go` (tectos), `digest_relatorio.go`. Como se usa: README do banco,
  secção «A qualificação do modelo»; `docs/runbooks/PROC-BANCO-DE-ENSAIO.md`.
- **A regra.** Texto fixo em `RegraDoVeredictoDoModelo`, que vai no relatório. Função pura de
  `qualificacao_do_modelo.contagens`. Limiares L1 a L5 da D2, mais **L6** (zero nós sem tools por
  cumprir). Um nó, com ou sem tools, interrompido por causa passageira sai da amostra; a amostra
  exigida é de 40 nós com tools e 16 sem tools.
- **Decisões do dono tomadas durante o ticket (2026-10-11), que alteram a D2:**
  - **Os nós sem tools entram na regra do veredicto** (L6, razão firme
    `nos_sem_tools_nao_cumpridos`; por causa passageira, `inconclusivo`). A revisão mediu
    `qualificado` com os 16 nós sem tools em 400, em 500 e cortados.
  - **Um perfil que declara devolução só é `qualificado` com `qualificacao_da_devolucao` em
    `cumprida`**; `nao_cumprida` é firme (`devolucao_do_estado_nao_cumprida`); `sem_raciocinio` e
    `inconclusiva` são passageiras (`devolucao_do_estado_nao_medida`).
  - **Os factos ausentes ficam como aviso** (`factos_ausentes_em_respostas_cumpridas`): não há
    limiar para o conteúdo da resposta.
- **Achados das revisões adversariais e correcções.**
  1. *Verde falso em L2:* a contagem exigia o motivo de paragem `stop`; um provider que fecha a
     1.ª tentativa sem tool call com `content_filter` e cumpre à 2.ª saía `qualificado` com 0 de
     40. L2 passa a contar toda a 1.ª tentativa com zero tool calls nativas. Medido atrás da
     imagem do proxy: o LiteLLM entrega `end_turn` e a ausência de motivo como `stop`, e
     `content_filter` tal e qual.
  2. *Nós sem tools fora do veredicto* e *devolução declarada e não cumprida a dar
     `qualificado`:* ver as decisões acima.
  3. *Controlo «aceite contra exigido»:* um só 4xx em cinco dava `falhou_como_devido`, e um
     controlo interrompido também. Passa a exigir a corrida completa, todos os pedidos com
     mensagem `tool` recusados e nenhum nó com tools cumprido; o resto é `inconclusivo`, e o
     aviso mantém-se. O controlo deixou de mudar a versão da projecção (muda só `devolver`); o
     que ainda não isola — o layout 1.4.0 em vez do 1.5.0 — vai escrito em
     `controlo_sem_devolucao.limite`.
  4. *Tectos contornáveis:* o contador do dia e o registo das qualificações viviam ao lado de
     `--chaves` (copiar o ficheiro repunha-os) e a conta era só pelo digest do perfil (mudar
     `max_tokens` dava contador novo). Vivem agora na pasta do dono; há um tecto por rota; e a
     contagem é o maior entre o registo e os relatórios de hoje.
  5. *Digest:* membros que só diferem na caixa, UTF-8 inválido e meios pares substitutos
     isolados davam o mesmo digest a ficheiros diferentes; recusam. Um relatório de qualificação
     não se escreve por cima de outro.
  6. *Modo falso com a forma do real:* o veredicto leva `vale_para_producao`, o resumo abre por o
     dizer, e um `qualificado` de ensaio sai com o código 9. `--so-plano` sai com 10.
  7. *Imagem do proxy:* havia duas constantes a compararem-se uma com a outra; fica a declaração
     única do AOS-517.
- **Mutações dirigidas: 48, todas vermelhas.** Por limiar: L1, L3, L4, L5 (diferente e não
  reportado) e L6 de «zero» para «no máximo um»; L2 de 4 para 5 e de `<=` para `<`; a amostra de
  40 para 39; L2 de volta ao motivo `stop`. Na regra: a firme deixa de ganhar a passageira; um
  5xx conta como não cumprido; um nó interrompido conta como cumprido; nó sem tools passageiro
  tratado como firme, e firme como passageiro; nós sem tools fora da amostra completa, e os
  interrompidos fora das razões; devolução não cumprida e não medida fora da regra; perfil
  recusado com respostas 2xx; corrida parada fora das razões; turno sem comparação do modelo
  fora da conta; um 401 a contar como 4xx de parâmetro. No orçamento: 300 para 301; o limite a
  deixar sair mais um; 2 para 3 por perfil; 4 para 5 por rota; o tecto por rota sem conferência;
  os relatórios de hoje fora da conta; o contador de volta para o lado das chaves; o pior caso
  sem o controlo; a qualificação que não cabe a correr; a sonda falhada a gastar uma tentativa;
  o tecto em dólares não imposto sem aviso. No controlo: um só 4xx a chegar; o aviso só quando
  passou; o controlo interrompido a contar; a projecção a mudar. No digest: a verificação a
  aceitar qualquer valor; os números fora da forma canónica; membros repetidos e o carácter de
  substituição aceites; escrever por cima. No modo: um ensaio a valer para produção e a sair com
  0; `--so-plano` a sair com 0; a imagem sem conferência; o falso a não trocar o modelo.
- **Medido.** Modo falso: o bom sai `qualificado` com 40 de 40 e 16 de 16, em 96 pedidos; os
  cinco falsos «maus» saem `recusado`, cada um com a sua lista de razões. Atrás da imagem fixada
  do proxy (`bash scripts/ci/banco-ensaio-proxy.sh`, verde): o bom `qualificado` (96 pedidos, 96
  de 96 turnos com o modelo esperado); não chama a tool, recusa o segundo turno (40 respostas
  400), responde vazio e «o proxy serve outro modelo» (96 de 96 turnos diferentes) `recusado`.
- **Limites que ficam.** O digest dá integridade, não autenticidade. Os tectos do dia protegem de
  enganos: quem apaga a pasta do dono recomeça. L5 compara o que o proxy declara, não o que o
  fornecedor serviu. O tecto de 3 USD só é imposto em fornecedores com tecto em dólares no
  ficheiro de chaves (o relatório avisa quando não é).
- **Por confirmar pelo dono.** O tecto de **quatro qualificações por rota por dia** (o número é
  proposta). A ligação ao AOS-519: o digest do perfil inclui `requested`, e o banco exige
  `requested: rota-de-ensaio`; o `digest_do_perfil` do relatório não é, por isso, o do perfil que
  o nó carregará com o alias de produção. Recomendação, a entregar com o AOS-519 ou o AOS-522: o
  banco aceitar o `requested` de produção e usá-lo como nome da rota efémera do proxy do ensaio.
- **Feito com o modelo real (2026-10-11 e 2026-10-12), com autorização do dono.** O Kimi de
  produção saiu `qualificado` a 2026-10-11, em 23 min 5 s, à quarta tentativa: as três primeiras
  pararam na sonda com HTTP 500, sem causa determinada. O `k3` (alias `gpt-4o`, `openai/k3`)
  saiu `qualificado` a 2026-10-12, em 15 min 37 s: 40 de 40 nós com tools e 16 de 16 sem tools
  cumpridos, 0 de 40 sem tool call à 1.ª tentativa, 0 respostas vazias, 96 de 96 turnos com o
  modelo esperado, 96 pedidos, todos 200; digest do relatório
  `sha256:87addc0a095987250ca55c8c7172da4e39321fcadf3d35f8fab8b20671ad086f`. Números na §5 do
  acompanhamento.
- **O aviso dos factos ausentes, no `k3`:** 15 de 56 respostas cumpridas (27%); o titular teve
  2 de 56 na véspera. É aviso e não limiar, por decisão do dono; a medida não lê o texto, e o
  que falta nessas respostas está por investigar. O dono escolheu o `k3` com este aviso à vista.
- **O que o modelo real não provou.** Os dois modelos examinados são bons. Os veredictos
  `recusado` vêm todos de providers falsos, no modo falso e atrás do proxy real: **nenhum
  modelo mau real foi recusado pelo exame.**
- **Critérios por marcar.** «Aceite separa-se de exigido» não foi corrido com o modelo real por
  esta via: nenhum perfil com devolução obrigatória foi examinado. Os critérios de CI deste
  bloco continuam por marcar, apesar de o texto acima os dar como implementados em ensaio: não
  foram conferidos um a um na passagem de 2026-10-12, que só registou medições.

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

- [x] Com `recusado` e com `inconclusivo` (um teste cada, contra os falsos do AOS-518), a
      pasta de saída tem o relatório e o resumo e **não tem** registo de entrada — nem
      parcial, nem com outro nome. O código de saída distingue os três veredictos.
- [x] Uma corrida interrompida a meio não deixa registo de entrada.
- [x] **Mutações dirigidas, uma por barreira** (critério reescrito a 2026-10-11, depois da revisão:
      o texto original — «retirar a condição do veredicto faz aparecer um registo com
      `recusado`» — não é o que acontece, porque a conferência do nó recusa esse relatório a
      seguir). Entre a corrida e o pacote há quatro barreiras **independentes**: o código de saída
      e o relatório dizem os dois `qualificado`; o relatório no disco é o da corrida; o registo e
      o relatório passam nas conferências do nó; o que ficou escrito é o que se conferiu. Cada
      uma tem um teste que a ataca com as outras satisfeitas, e retirar qualquer uma avermelha o
      seu teste. Retirar a primeira **e** a terceira faz aparecer um registo com `recusado`.
- [x] O registo de entrada preparado cita o digest do relatório **que está na pasta**: um
      teste recalcula-o. O comando não aceita um relatório vindo de fora nem um veredicto por
      argumento: só prepara o registo da corrida que acabou de fazer.

**O resumo**

- [x] Cabe numa página (no máximo 60 linhas de 100 colunas, preso por teste) e diz, **por
      esta ordem**: veredicto; razões; amostra; o que os controlos negativos mostraram; o que
      a corrida não prova; o gasto. Um teste prende a ordem.
- [x] Com `exigencia_nao_provada` no relatório, essa linha vem antes de todas as outras,
      incluindo o veredicto.
- [x] Traz os digests que o dono vai assinar (perfil, relatório, imagem do proxy), a fatia, o
      alias pedido e o modelo esperado — e, se o alias já tiver entrada na tabela em código,
      diz que o perfil a **substitui**.
- [x] Não contém texto de respostas do modelo, conteúdo de documentos, chaves, nem o caminho
      do ficheiro de chaves.

**A chave privada**

- [x] O comando não tem argumento, variável de ambiente nem leitura de ficheiro por onde uma
      chave privada de assinatura possa entrar. Preso por teste sobre a lista de argumentos e
      de variáveis lidas. Assinar é um passo separado, do dono, com o `aos-issuer`.
- [x] O comando recusa correr com `CI` ou `GITHUB_ACTIONS` definidas no modo real, como o
      banco já faz.

**A conferência antes da cópia**

- [x] Um pacote assinado (registo, assinatura, relatório) é conferido no posto pela **mesma
      função** que o nó usa no arranque, e não por uma cópia dela: aceite aqui, é aceite pelo
      nó; recusado aqui, a causa é a mesma palavra que o nó daria. Teste com um pacote bom e
      com três dos casos de recusa do AOS-519.

**O orçamento**

- [x] O comando não contorna os tectos do AOS-518: a terceira qualificação do mesmo perfil no
      mesmo dia é recusada antes de enviar, e o pior caso é declarado antes do primeiro
      pedido.

**O runbook**

- [x] `docs/runbooks/PROC-BANCO-DE-ENSAIO.md` (ou um procedimento próprio ao lado) descreve
      os passos 0 a 8 do desenho §3, com quem faz, o que fica como prova, e o tempo esperado
      de cada um; diz onde o relógio começa e onde pára; e diz o que fazer em cada veredicto.
- [x] Traz a secção «antes de recriar o nó»: conferir o pacote no posto; drenar a rota;
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

**Implementado em ensaio, com as correcções da revisão adversarial (2026-10-11); a corrida real
está feita (2026-10-11 e 2026-10-12); o ensaio a seco com Docker e nó local fica por fazer.** Ramo
`feature/AOS-522-comando-qualificar`, sobre a base com o AOS-519 fundido.

- **O que existe.** `aos-ensaio qualificar <falso|proxy|real>` e `aos-ensaio conferir`
  (`packages/qa/banco-ensaio/qualificar.go`, `resumo_de_uma_pagina.go`). O `qualificar` faz o
  plano **antes de enviar** — o registo pela leitura do nó e o alias contra a allowlist (a
  embebida, ou a externa de `--allowlist-dir`) —, corre a experiência `qualificacao` pela linha de
  comandos de sempre, escreve o `resumo-de-uma-pagina.txt` e, só com `qualificado` do modo real
  com a corrida completa, a pasta `pacote/` com o registo por assinar e o relatório citado. Pára
  aí: não assina. O `conferir` passa o pacote assinado por `routeentry.LoadDir`.
- **Uma só conferência.** Antes da assinatura: `routeentry.ParseEntry`, a imagem do proxy
  declarada, a allowlist e `routeentry.CheckReport`. Depois: `routeentry.LoadDir` e a composição
  dos perfis, como o nó. Não há cópia de nenhuma delas no banco.
- **Quatro barreiras entre a corrida e o pacote**, independentes, cada uma com teste próprio
  (`aos522_barreiras_test.go`): o código de saída e o relatório; o digest do relatório relido do
  disco contra o da corrida; as conferências do nó; a comparação byte a byte do que ficou escrito.
  O comando nunca sai com 0 sem pacote.
- **O resumo mostra o que ficou escrito.** O bloco «o que vais assinar» sai do registo lido de
  volta do pacote e traz o perfil inteiro (wire, capacidades, versão da projecção, parâmetros do
  pedido), o ambiente, a fatia e os quatro digests. «SUBSTITUI» ou «ACRESCENTA-SE» é dito segundo
  a tabela do binário do posto, com a revisão.
- **Um defeito do AOS-518, corrigido aqui.** Uma corrida interrompida podia sair `recusado`: o
  cancelamento apanhava um run depois de um pedido com 200, o runtime devolvia um erro sem classe
  e a regra contava o nó como por cumprir (L1 ou L6). Medido: 3 e 7 falhas em duas séries de 200
  corridas; reproduzido de forma determinista com um contexto que só se diz cancelado por `Err()`.
  Agora o run apanhado leva `corrida_cancelada`, o nó fica interrompido por
  `cancelamento_da_corrida` e o run não alimenta nenhum limiar. Depois da correcção: 200 em 200,
  no teste do AOS-518 e no do AOS-522.
- **Inerte.** Os subcomandos de sempre passam pela mesma função, que agora devolve também o
  relatório que escreveu; a suite de antes passa sem alterações, e o texto de uso geral não mudou
  de um byte (preso por teste). O campo `corrida_cancelada` só aparece num run de qualificação
  apanhado por um cancelamento.
- **Como se leram três critérios.**
  1. *As mutações.* Ver o critério reescrito, acima.
  2. *«A conferência antes da cópia pela mesma função».* O `LoadDir` exige a assinatura; por isso
     o `qualificar` usa as funções que ele chama por dentro e que não dependem dela, e é o
     `conferir`, depois de o dono assinar, que corre o `LoadDir` inteiro.
  3. *«O que os controlos negativos mostraram».* Na corrida, o controlo negativo é a corrida de
     controlo sem devolução, que só existe com `devolver: obrigatorio` — e um perfil desses é
     recusado antes de enviar (D8). No caminho deste comando a linha diz, por isso, «não se
     aplica»; a função do resumo trata os outros casos, e um controlo em falta, não corrido ou de
     resultado desconhecido sai «CONTROLO NAO CORRIDO».
- **A corrida real (2026-10-11 e 2026-10-12).** `aos-ensaio qualificar real` correu duas vezes
  com veredicto `qualificado` e deixou o pacote por assinar: com o Kimi de produção (23 min 5 s;
  assinado só com uma chave de teste descartável) e com o `k3` (15 min 37 s: plano 1 min 26 s,
  sonda 10 s, corrida 14 min 1 s). O pacote do `k3` foi assinado pelo dono, no seu posto, com a
  chave dos perfis que gerou; `aos-ensaio conferir`, com a âncora do dono, disse «o nó aceitaria
  este pacote», e o nó de produção carregou-o (AOS-519). O comando não viu a chave privada.
  Nenhum `recusado` nem `inconclusivo` saiu de um modelo mau real: as três tentativas
  `inconclusivo` de 2026-10-11 foram sondas com HTTP 500, e não deixaram nada por assinar.
- **O relógio do runbook.** Tempo de máquina da entrada do `k3`: 15 min 37 s de exame mais
  cerca de 3 min de instalação e duas recriações do nó. O tempo humano — ler o resumo, assinar —
  não foi medido, e nenhuma revisão do passo-a-passo do runbook foi cronometrada com o dono.
- **Critério por cumprir.** *O ensaio do procedimento a seco*, com provider falso atrás do proxy
  fixado e um nó local até ao banner: **não foi feito**, e os tempos por passo não estão medidos.
  O nó só aceita um relatório do modo `real`, e esse modo fica reservado ao posto do dono: o
  ensaio depende da qualificação real. O que existe no seu lugar é o teste do caminho feliz, que
  vai do perfil ao `LoadDir` sem Docker e sem nó. Os tempos do runbook são os do desenho, por
  medir no AOS-523.
- **Limites que ficam.** O resumo não é garantia. O plano e o `conferir` não sabem se a âncora, o
  board, a região e a allowlist que recebem são os do servidor. O alfabeto do board e da região
  (1 a 64 letras, dígitos, `_` e `-`) é só deste comando. A criação exclusiva da pasta de saída
  não tem teste de concorrência verdadeira: está presa a recusa de uma pasta que já existe. A
  reconferência pelo nó do pacote relido é redundante com a comparação byte a byte (bytes iguais
  aos conferidos passam sempre): retirá-la sozinha não muda nada, e por isso não tem mutação que
  a mate. Os dois subcomandos não aparecem no texto de uso geral do banco.

## AOS-523 — A prova da fase A3: o terceiro modelo entra em menos de uma hora, com zero PRs e uma assinatura; um modelo mau é recusado sozinho

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: é uma medição, sem código. O ADR-012 (eval-gate, canary e ratificação humana assinada) é citado só como o princípio cujo caminho completo a medição percorre. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A3 (entrada automática). Rótulo no desenho: «A3-prova». É o ticket que mede o critério da fase |
| Tipo | test |
| Prioridade | P1: sem esta medição a fase fica construída e por provar |
| Estimativa | M (trabalho de medição e de registo; nenhum código) |
| Dependências | AOS-517, AOS-518, AOS-519, AOS-520, AOS-521 e AOS-522, todos em produção na mesma versão. Decisão D9 do dono (tomada a 2026-10-11); o nome do terceiro modelo, que o dono indica |
| Bloqueia | A passagem da fase A3 a **provada**; a fase A4 |
| Responsável sugerido | Engenheiro de Qualidade, com o dono a executar os passos que são dele |
| Documentos de referência | `docs/reports/desenho-a3-entrada-automatica-2026-10-11.md` §1, §3, §4.7, §6 (D9) e §8, `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (§2, §4, §5 e §7), `docs/runbooks/PROC-BANCO-DE-ENSAIO.md`, `deploy/server/README.md` |

### Contexto

- **O critério da fase** (acompanhamento §2): «o terceiro modelo entra com zero PRs e uma
  assinatura em menos de uma hora; um modelo mau é recusado sozinho».
- **Uma fase só passa a provada quando o critério está medido e registado na §5**, e só
  passa a fechada por decisão do dono, registada na §4.
- **A A2 fechou provada em ensaio, não em produção**, e disse-o. Esta medição diz, parte a
  parte, onde foi feita.
- **O que o desenho não sabe e só esta medição responde** (desenho §8): quanto tempo leva
  uma qualificação de 8 passagens; se há tráfego para um canary querer dizer alguma coisa;
  se «3 em 20» dispara por azar.
- O caminho medido é o do princípio da auto-modificação com rede (`AGENTS.md` §7, n.º 9;
  ADR-012): eval-gate, ratificação humana assinada e canary, por esta ordem.

### Decidido pelo dono (2026-10-11)

**D9 — o terceiro modelo é outro modelo do Kimi, pela mesma conta**, na região já aprovada.
Tem de ser um modelo que a tabela em código **não conheça** (o `k3` já lá tem perfil e não
serve de prova de «zero PRs»). Pede-se pelo segundo alias que a allowlist já autoriza, e o
perfil assinado substitui a entrada da tabela para esse alias, dito no banner. O dono indica o
modelo. Enquanto não houver endpoint na UE, o Claude continua só em ensaio.

### Objectivo

Medir e registar, com prova selada, as três partes do critério: (a) um modelo novo entra pelo
caminho do desenho §3 com o relógio a contar e sem PR nenhum; (b) um modelo mau de propósito
é recusado antes de entrar; (c) um candidato que degrada depois de entrar perde a fatia sem
intervenção.

### Âmbito

- As três medições, o registo na §5 do acompanhamento, e a actualização do estado da fase.
- Nenhum ficheiro `.go`, script ou workflow. Um defeito encontrado durante a prova abre
  ticket novo; não se corrige aqui.

### Critérios de Aceitação

**Antes de o relógio começar (passo 0, fora do relógio; fica registado)**

- [x] A versão em produção contém os seis tickets anteriores; os interruptores estão como a
      prova pede e ficam anotados: pasta e âncora dos perfis, `AOS_MODEL_CANDIDATE=on`,
      `AOS_MODEL_BREAKER=observe`, `AOS_MODEL_ROUTE_GOVERNANCE` em `observe`.
      **Anotado (2026-10-12):** v0.1.55, tag sobre `25b85e9d`, com os seis tickets; pasta e âncora
      dos perfis ligadas, `AOS_MODEL_CANDIDATE=on`, `AOS_MODEL_BREAKER=observe`, e a governação da
      rota em `observe` (todos os turnos com `route_check: igual`).
- [ ] O proxy de produção corre o digest declarado (AOS-517) e tem a rota do modelo novo
      configurada pelo dono; a allowlist em vigor já autoriza o alias. **Fica dito no
      registo que este passo existiu e quanto demorou**, apesar de estar fora do relógio.
      **Por marcar:** o proxy corre o digest declarado desde o deploy da v0.1.55 e o alias `gpt-4o`
      já estava autorizado e com rota; mas a duração do passo 0 não foi medida.
- [ ] Anotados, para a prova de «zero PRs»: o digest da imagem do nó em produção e o commit
      da base, no instante em que o relógio começa.
      **Por marcar:** estão anotados a imagem do nó (`sha256:67a9f70e2836…`) e o commit
      (`25b85e9d`), mas não «no instante em que o relógio começa»: a hora de início do exame não
      ficou registada.

**(a) O terceiro modelo entra — em produção**

- [ ] **Menos de uma hora** entre o início do passo 1 (escrever o perfil candidato) e o fim
      do passo 6 (o primeiro run servido pelo candidato em produção). Os instantes tiram-se
      de artefactos e não de memória: a hora de início da qualificação no relatório, a da
      assinatura, o evento selado «perfil carregado», e o manifesto do primeiro turno do
      candidato. O tempo de cada passo fica numa tabela, ao lado do esperado pelo desenho.
      **Por marcar:** o tempo de máquina foi de cerca de 19 min (15 min 37 s de exame mais cerca
      de 3 min de instalação e duas recriações). O tempo humano — ler o resumo, assinar — não foi
      medido, e os instantes não foram tirados dos artefactos para a tabela por passo.
- [ ] **Zero PRs:** o digest da imagem do nó e o commit da base são os mesmos no fim e no
      princípio; não houve release, deploy de imagem, nem alteração de código entre os dois
      instantes.
      **Por marcar, pela letra:** não houve PR nenhum para este modelo. Mas a v0.1.55 entrou em
      produção no mesmo dia (10:36:37 UTC), 13 minutos antes de o nó ser recriado com os perfis
      (10:49:33 UTC), e o exame durou 15 min 37 s: «sem release nem deploy entre os dois
      instantes» não fica estabelecido. E o `k3` já tinha perfil na tabela em código — ver o
      Estado.
- [x] **Uma assinatura:** um só acto de assinatura do dono (o registo de entrada). A
      allowlist não foi re-assinada.
      **Medido (2026-10-12):** o dono assinou o registo de entrada do `k3`, e só esse.
- [x] O veredicto do arnês para o modelo novo é `qualificado`, e o relatório, o registo
      assinado e os três digests ficam referidos no registo da prova.
      **Medido (2026-10-12):** `qualificado`; os digests do relatório, do registo e do perfil
      estão no Estado e na §5 do acompanhamento; o da imagem do proxy é o do AOS-517.
- [ ] O primeiro run do candidato em produção fecha «cumprido», com o digest do perfil
      **assinado** no manifesto e o modelo servido igual ao esperado em todos os turnos.
      **Por marcar:** 14 de 14 runs do candidato `complete`, e os 21 turnos servidos por
      `openai/k3`; o digest do perfil assinado no manifesto não foi lido.
- [x] Se o tráfego de produção não trouxer um plano para a fatia dentro da hora, lança-se
      uma série de validação de propósito, e **o registo diz que foi lançada** e de que
      tamanho. A escolha continua a ser a da função: não se força um plano para o candidato.
      **Lançada de propósito (2026-10-12):** a série `v0155c`, de 40 planos, pela fila real. A
      escolha foi a da função: 7 planos no candidato, nenhum forçado.
- [ ] Se a hora não for cumprida, o critério **não está cumprido**: regista-se o tempo real
      e o passo que o gastou. Não se repete a medição até dar menos de uma hora sem dizer
      quantas vezes se tentou.
      **Por marcar:** depende do critério da hora, acima. Houve uma só tentativa de entrada.

**(b) Um modelo mau é recusado antes de entrar — em ensaio**

- [ ] Um perfil mau de propósito — que aponte para um modelo sem tool calling, ou com um
      parâmetro que o parte; fica dito qual — corre o comando do AOS-522 com o modelo real:
      veredicto `recusado`, com a razão esperada, e **nenhum registo de entrada** na pasta
      de saída. Ficam registados os pedidos gastos e que a corrida coube nos tectos.
      **Não medido.** Nenhum perfil mau correu com o modelo real. Os `recusado` que existem são
      de providers falsos (AOS-518), no modo falso e atrás do proxy real.
- [ ] **O nó também o recusa.** Num nó de ensaio (não em produção), um registo de entrada
      assinado à mão que cite esse relatório `recusado` não carrega: o nó não arranca, com a
      causa do AOS-519. A recusa não depende só de o comando não ter preparado o ficheiro.
      **Não medido** como aqui se descreve. O que existe é o teste do AOS-519 (veredicto que não
      é `qualificado`: o nó não arranca).

**(c) Um candidato que degrada perde a fatia — em ensaio, e dito que foi aí**

- [ ] Com um provider falso atrás da imagem fixada do proxy, bom durante N runs e mau
      depois, e o disjuntor em `enforce`: abre dentro da janela escrita, nenhum plano novo
      vai para o candidato, o titular não é tocado, e não houve intervenção de ninguém. O
      evento selado da abertura, com as contagens, fica referido no registo.
      **Não medido** atrás da imagem fixada do proxy. O que existe é o teste do AOS-521 com um
      provider falso que degrada a meio.
- [ ] O registo diz, com estas palavras, que **(c) foi provado em ensaio e não em
      produção**: em produção exigia um modelo que falhasse em produção.
      **Por marcar:** o registo diz mais do que isto — (c) está provado só em teste, com um
      provider falso; em produção o disjuntor só correu em `observe`, com um candidato bom.

**O canary em produção (acompanha a prova; não é condição do critério)**

- [ ] Com o candidato a servir a fatia de 10% e o disjuntor em `observe`, ficam registadas a
      série «depois» (os planos do candidato) e a «antes» (os do titular no mesmo período):
      planos, primeiras falhas, «não cumprido», e as vezes que o disjuntor teria aberto, para
      cada um.
      **Em parte (série `v0155c`):** candidato — 7 planos, 0 primeiras falhas, 0 «não cumprido»,
      0 «teria aberto»; titular, no mesmo período — 33 planos, 4 primeiras falhas, 0 «não
      cumprido». Falta a leitura do disjuntor sobre o titular (a calibração do AOS-521).
- [ ] O critério P7 da fase anterior mantém-se: a taxa de primeiras falhas e o «não
      cumprido» do titular não pioram face à última série anterior do mesmo tamanho.
      **Por marcar: as primeiras falhas do titular subiram.** 2 em 5 (`v0155a`) e 4 em 33
      (`v0155c`), contra 0 em 19 (`v0154d`) e 1 em 82 (2026-10-07). O «não cumprido» continua a
      0. A causa não foi procurada.

**Registo**

- [ ] A §5 do acompanhamento ganha as linhas da prova; a linha da A3 na §2 passa a
      **provada** só se (a), (b) e (c) estiverem cumpridos, com a frase exacta do que ficou
      provado em produção e do que ficou provado em ensaio.
      **Por marcar:** a §5 tem as linhas da prova, e a linha da A3 na §2 diz o que ficou provado
      em produção e o que ficou provado com modelos simulados. Mas (b) e (c) não estão cumpridos
      como este ticket os escreve. A linha da §2 passou a **fechada** por decisão do dono, a
      2026-10-12, e não por esta regra: «provada em produção» vale só para a primeira metade, e
      é isso que lá está escrito.
- [x] O registo lista o que a prova **não prova**: o terceiro modelo é da mesma família e da
      mesma conta que o titular; nenhum perfil que devolve estado entrou; o planeador do
      `aos-orq` não é coberto; a bateria são seis casos sintéticos; o disjuntor em `enforce`
      nunca actuou em produção.
      **Escrito** na §3 do acompanhamento («O que a prova não prova») e no Estado, abaixo.
- [x] O fecho da fase é do dono, na §4. Este ticket não a fecha.
      **Fechada pelo dono a 2026-10-12**, na §4 do acompanhamento; não por este ticket.

### O que fica desligado por omissão, e o que tem de ficar inerte

- **Interruptores:** este ticket não cria nenhum. Usa os dos tickets anteriores, e regista
  como estavam.
- **Inerte:** os planos servidos pelo titular durante a prova são servidos como antes — é o
  que a série «antes» mede.

### Testes exigidos

- Não há testes de código. As três medições são os testes, e cada uma tem o seu controlo:
  em (a), os instantes vêm de artefactos selados; em (b), a recusa confere-se em dois sítios
  (o comando e o nó); em (c), o mesmo cenário com o disjuntor em `observe` continua a dar
  planos ao candidato — a abertura é do disjuntor e não de o candidato nunca ter servido.

### Riscos

- **A hora pode não caber** se a qualificação demorar mais do que a estimativa, ou se não
  houver tráfego. Os dois casos têm resposta escrita acima; nenhum é esconder o tempo.
- **Um candidato real em produção pode falhar com dados reais.** A fatia é 10%, o canary
  acaba aos 40 planos, e os documentos da série de validação são os de teste.
- **Provar (c) só em ensaio** deixa o `enforce` por actuar em produção. Fica nomeado como
  resíduo.

### O que precisa do dono

- Indicar o terceiro modelo e configurar a rota no proxy do servidor (passo 0).
- Escrever o perfil, correr o comando, assinar, copiar e recriar o nó — com o relógio.
- Aprovar a série de validação, se for precisa.
- Decidir, com os números: se a fase fecha; se o candidato passa a titular, continua ou sai;
  se o disjuntor passa a `enforce`.

### Fora de âmbito

- Corrigir o que a prova encontrar; mexer em limiares para a prova passar.
- A segunda família em produção (um parceiro com endpoint na UE): é o primeiro modelo «a
  sério» depois da prova, e é o passo 0 de outra entrada.
- A passagem do candidato a titular.
- Um perfil que devolve estado.

### Estado

**Medido a 2026-10-12; a fase A3 foi fechada pelo dono no mesmo dia, com os resíduos nomeados.**
O critério da fase tem duas metades, e ficaram em sítios diferentes.

**Decisões do dono que valem para esta medição** (acompanhamento §4).

- 2026-10-11: «O passo 5 também está autorizado» / «Prossiga com o que é possível, no que
  depender de mim é só sinalizar» — a tag, os passos do AOS-517 no servidor, o exame real do Kimi
  e a verificação de produção foram autorizados; aprovar o deploy, gerar a chave e assinar, e
  escolher o terceiro modelo continuaram a ser do dono.
- 2026-10-12: o terceiro modelo é o **`k3`** (Kimi, alias `gpt-4o`, `openai/k3`), com o aviso dos
  factos ausentes à vista («avança com o k3»). **Esta escolha afasta-se do que este ticket
  escrevia** em «Decidido pelo dono»: o `k3` já tem perfil na tabela em código
  (`packages/platform/model-gateway/route.go`, `gpt-4o` → `openai/k3`), e o ticket excluía-o por
  isso como prova de «zero PRs».
- 2026-10-12: o dono gerou a chave dos perfis no seu posto e assinou o registo do `k3`. Âncora
  pública: `GR78qbnszHiu0JlFuW7fxDRcAZ36l1zQ5Lv3QzEdcF4=`.
- 2026-10-12: «dou a fase A3 por fechada, abre a A4» — o fecho, com a medição abaixo à vista.

**Passo 0.** v0.1.55 (tag sobre `25b85e9d`) em produção desde 2026-10-12, 10:36:37 UTC; imagem do
nó `sha256:67a9f70e2836…`. O deploy recriou o `litellm`, agora referido pelo digest
`sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63`, com
`drop_params: false` mantido; criou `/opt/aos/perfis-de-rota` (dono `aos`); copiou o
`alerta-disjuntor.sh`, sem cron. Nó `healthy` um minuto depois; o tempo sem modelo não foi
medido. Série `v0155a`, sem variáveis novas: 5 de 5 planos concluídos; 2 de 5 nós com tools
falharam à 1.ª tentativa e recuperaram à 2.ª; todos os turnos com `route_check: igual`;
projecção 1.2.0; 0 eventos `run.model_pinned`.

**(a) O terceiro modelo entrou — em produção.**

| Passo | O que ficou | Tempo |
|---|---|---|
| Exame (`aos-ensaio qualificar real --fornecedor kimi --modelo k3`, binário da base `25b85e9d`; perfil `requested: gpt-4o`, `expected_model: openai/k3`, sem parâmetros; board `board-eu`, região `eu`, fatia 10%) | **`qualificado`**: 40 de 40 nós com tools e 16 de 16 sem tools cumpridos; 0 de 40 sem tool call à 1.ª tentativa; 0 respostas vazias; 96 de 96 turnos com o modelo esperado; 96 pedidos, todos 200. Avisos: factos ausentes em 15 de 56 respostas cumpridas (27%; o titular teve 2 de 56 na véspera); tecto em dólares não imposto. Digest do relatório `sha256:87addc0a095987250ca55c8c7172da4e39321fcadf3d35f8fab8b20671ad086f` | plano 1 min 26 s, sonda 10 s, corrida 14 min 1 s: **15 min 37 s** |
| Assinatura e conferência | O dono assinou o registo. `aos-ensaio conferir`, com a âncora do dono: «o nó aceitaria este pacote». Digest do registo `sha256:59e3d1b295aa743764026b0f75ecf8e7c57a29c614c1b7c579ab18547437d558`; digest do perfil `sha256:efdd13fbdba0b96a8bc83cad7e2fb3f30f7e3ce5553a4eb0f736d7ab69efba36` | não medido (tempo humano) |
| Instalação e primeira recriação | Os três ficheiros em `/opt/aos/perfis-de-rota` (o `sha256sum` do registo no servidor é o digest assinado); `.env` com as duas variáveis dos perfis (cópia `.env.antes-da-a3`); nó recriado às 10:49:33 UTC, `healthy`; banner «perfil de rota ASSINADO carregado … registo "gpt-4o"» e «modelo candidato NAO SERVIDO» | cerca de 3 min, com a linha seguinte |
| Candidato e disjuntor | `AOS_MODEL_CANDIDATE=on` e `AOS_MODEL_BREAKER=observe` (cópia `.env.antes-do-candidato`); nó recriado, `healthy`; banner: canary ABERTO, fatia 10%, limite 40, época 1, 0 atribuídos, marca de abertura 1312; disjuntor FECHADO em `observe` | incluído acima |

Zero PRs para este modelo. Uma assinatura. Cerca de 19 min de máquina. Feito pelo utilizador
`aos`, sem root: a pasta dos perfis ficou do `aos`, e não do root como o runbook pede. **Nenhuma
revisão do passo-a-passo do runbook foi cronometrada com o dono, e o tempo humano — ler o resumo,
assinar — não foi medido.**

**Série `v0155c`** (40 planos pela fila real, lançada de propósito, com o candidato a servir). 40
de 40 planos concluídos (código 0). Livro do canary: 1 `model.canary.opened`, 7
`model.canary.plan_assigned`, 0 `closed`. 84 runs com `run.model_pinned`: 70 do titular (razão
`funcao`) e 14 do candidato (7 `funcao` e 7 `plano_ja_atribuido`). 40 planos, 7 no candidato —
17,5% para uma fatia de 10%; com 40 planos cabe no acaso (a função deu 110 em 1000 no teste) —,
e 0 planos com dois modelos.

| | Planos | Runs | Primeiras falhas | «Não cumprido» | «Teria aberto» |
|---|---|---|---|---|---|
| Candidato (`gpt-4o`, servido por `openai/k3`) | 7 | 14 de 14 `complete`; 21 turnos | 0 | 0 | 0 (7 `model.breaker.run_outcome`, todos `cumprido`) |
| Titular (servido por `openai/kimi-for-coding`) | 33 | 70: 66 `complete` e 4 `failed` com `contract_unmet_no_call`; 103 turnos | 4, recuperadas | 0 | não lido (calibração do AOS-521 por fazer) |

Os nós de resumo, sem contrato, não entram na janela do disjuntor.
`aos_model_breaker_unrecorded_total` ficou a 0, e as séries por modelo apareceram no `/metrics`
com o candidato a servir.

**O critério da fase, com todas as letras.**

- *«O terceiro modelo entra com zero PRs e uma assinatura em menos de uma hora»:* **cumprida em
  produção com o `k3`** — zero PRs, uma assinatura, cerca de 19 min de máquina; o tempo humano
  não foi medido.
- *«Um modelo mau é recusado sozinho»:* **provada no exame com modelos maus simulados** — o que
  não chama a tool, o que responde vazio, o que recusa o 2.º turno e o que serve outro modelo
  saem todos `recusado`, no modo falso e atrás do proxy real. Em produção, o disjuntor só correu
  em `observe`, com um candidato bom. **Nenhum modelo mau real foi recusado, nem no exame nem em
  produção.**

**O que ficou por marcar nos critérios, em resumo.** Em (a): a hora e o «zero PRs» pela letra
(instantes não tirados dos artefactos, tempo humano por medir, deploy da v0.1.55 no mesmo dia), e
o digest do perfil assinado no manifesto, que não foi lido. (b) e (c) inteiros: não houve modelo
mau real, nem nó de ensaio com um relatório `recusado` assinado à mão, nem o cenário de
degradação atrás da imagem fixada do proxy. No canary: a leitura do disjuntor sobre o titular, e
o P7 — as primeiras falhas do titular subiram (2 em 5 e 4 em 33, contra 0 em 19 na véspera), com
o «não cumprido» a 0 e sem causa procurada.

**O que a prova não prova.** O terceiro modelo é da mesma família e da mesma conta que o
titular, e o binário já o conhecia por este alias. Nenhum perfil que devolve estado entrou. O
planeador do `aos-orq` não é coberto. A bateria são seis casos sintéticos. O disjuntor em
`enforce` nunca actuou em produção. O canary ficou em 7 planos dos 40 do limite.

**O fecho.** O dono fechou a fase A3 a 2026-10-12 (acompanhamento §4), com a primeira metade do
critério provada em produção com o `k3` e a segunda provada só com modelos maus simulados. Os
critérios por marcar acima continuam por marcar: o fecho da fase é uma decisão do dono com os
resíduos nomeados, e não os dá por cumpridos. À data, o `k3` continua ligado em produção a 10%
dos planos, com o disjuntor em `observe`.

**Por decidir pelo dono** (acompanhamento §4): se o candidato passa a titular, continua ou sai;
se o disjuntor passa a `enforce`.

**Resíduos**, na §7 do acompanhamento («Resíduos da fase A3»).

---

## AOS-525 — A bateria que separa modelos: dificuldade crescente, conteúdo a contar e respostas legíveis

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: muda o banco de ensaio, que vive fora da imagem do nó. O ADR-012 (eval-gate) é citado só como o princípio que o exame de qualificação serve. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A4 (cascata). Rótulo no desenho: «A4-medição». É a medição de que as decisões D2 a D10 dependem |
| Tipo | feat |
| Prioridade | P1: sem uma bateria que distinga modelos, a fase A4 não tem com que decidir |
| Estimativa | M |
| Dependências | AOS-518 e AOS-522 (o exame e o comando), feitos. Decisão D1 do desenho da A4 (tomada a 2026-10-12). O modo real só corre com autorização do dono |
| Bloqueia | As decisões D2 a D10 do desenho da A4 e qualquer ticket de construção da fase |
| Responsável sugerido | Engenheiro de Qualidade |
| Documentos de referência | `docs/reports/desenho-a4-cascata-de-modelos-2026-10-12.md` (§D1), `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (§3 «A4 a A6», §4, §5), `docs/runbooks/PROC-BANCO-DE-ENSAIO.md`, `packages/qa/banco-ensaio/README.md` |

### Contexto

A medição de 2026-10-12 (acompanhamento §5) passou o exame de qualificação, com a bateria
1.0.0, a seis modelos pela OpenRouter, do mais barato ao mais caro. O que ela mostrou:

- **A bateria não separa.** Nenhum dos seis teve uma primeira tentativa sem tool call nativa.
  Quatro saíram `qualificado`, um `inconclusivo` por 5xx, e o único `recusado` — um modelo de
  8 mil milhões de parâmetros, escolhido para falhar — foi-o por um nó em quarenta.
- **A única medida que os ordena não entra no veredicto.** «Factos ausentes da saída» foi de
  0 em 56 a 13 em 56, e é só um aviso.
- **Essa medida pode estar a medir a grafia e não o conteúdo.** A regra é a ocorrência
  literal de cada facto na saída (`strings.Contains`). Os documentos sintéticos estão escritos
  sem acentos («Tomas Albergaria-Pinto»). As ausências concentram-se nos nós em que o modelo
  reescreve — `T4/ler-dois` (16 das 32) e `T2/resumir` (12) — e são zero no nó em que copia o
  texto (`T2/ler`). **Hipótese, por confirmar:** um modelo que escreve «Tomás» é contado como
  tendo perdido o facto.
- **Não se consegue confirmar, porque o banco não guarda as respostas.** O relatório tem
  contagens e fichas da forma, nunca o texto. O dono autorizou a 2026-10-12 a leitura das
  respostas a documentos de teste; não há o que ler.

### Objectivo

Dar à fase A4 um exame que mostre **onde** um modelo barato deixa de chegar, com uma medida de
conteúdo em que se possa confiar e com as respostas à mão quando for preciso lê-las.

### Âmbito

- **Guardar as respostas, a pedido.** Uma opção do `aos-ensaio` (desligada por omissão) que
  grava, ao lado do relatório, o texto final de cada run e os argumentos das tool calls. Só
  vale para a bateria sintética; os ficheiros ficam na pasta de saída do dono, fora do
  repositório, e não entram no relatório nem no seu digest.
- **Corrigir a regra dos factos.** A comparação passa a ignorar acentos, maiúsculas e espaços
  repetidos, com a regra escrita no relatório. A hipótese do contexto confirma-se ou desmente-se
  primeiro, lendo respostas guardadas.
- **Bateria 2.0.0, com dificuldade crescente.** Casos novos por degraus, cada um com a
  capacidade que pede dita por extenso: mais documentos por passo, documentos mais longos, uma
  tool que falha e pede outro caminho, cadeias de nós mais fundas, instruções com uma
  restrição a respeitar, um facto que só sai de cruzar dois documentos. A 1.0.0 fica como está:
  os relatórios e os registos assinados que a citam continuam válidos.
- **O relatório por degrau.** As taxas saem por degrau, para se ler até onde cada modelo chega.
- **Repetir a medição** com os mesmos seis modelos, com autorização do dono, e registar na §5.
- **Fora de âmbito:** a regra de qualificação dos registos assinados (AOS-518) não muda aqui —
  se o conteúdo passa a contar para o veredicto é decisão do dono, com os números da medição
  nova; a escolha de modelo por passo (decisões D2 a D10); qualquer alteração ao nó.

### Critérios de Aceitação

- [x] Com a opção ligada, uma corrida deixa um ficheiro por run com o texto final e os
      argumentos das tool calls; com ela desligada, a pasta de saída é igual à de hoje, byte a
      byte no relatório. Provado por teste nos modos falso e proxy.
      *Nota de 2026-10-12:* a opção é `--guardar-respostas` (`respostas.go`,
      `aos525_respostas_test.go`). O teste do modo proxy corre o caminho do modo proxy com o
      lançador de teste a fazer de proxy; a imagem real do proxy (Docker) não foi corrida com a
      opção. **O que prova o «igual à de hoje», exactamente:** (1) por teste, só o sha256 de
      referência do AOS-516 (`TestAOS516_SemPerfilQueDevolva_PedidosERelatorioSaoOsDeAntes`),
      e só no modo falso sem perfil; (2) os `TestAOS525_…` comparam a corrida sem a opção com
      a corrida com ela, no mesmo código — não apanham uma mudança que afecte as duas por
      igual (um campo novo sempre serializado numa observação passa por eles); (3) o resto foi
      conferência manual contra o código de antes, feita duas vezes (pela implementação e pela
      revisão adversarial): o mesmo sha256 do relatório, do resumo e das saídas em falso,
      proxy com lançador de teste, qualificação com controlo, `qualificar falso` e
      separadores. Não há hashes novos pinados. **Os modos 0700 e 0600** só são verificados em
      Linux (CI): no posto, que é Windows, a asserção não corre.
- [ ] A opção é recusada fora da bateria sintética, e o texto guardado não aparece no
      relatório, no resumo, nos logs nem no rasto do proxy. Provado por teste de fuga.
      *Nota de 2026-10-12:* a segunda metade está provada (`TestAOS525_Fuga_…`: um marcador no
      texto e outro nos argumentos de uma tool call, com dois controlos negativos; o rasto do
      proxy só ao nível da função que o reduz a nomes). A primeira **não se aplica hoje**: a
      linha de comandos só conhece a bateria embebida, e não há outra origem de casos de que
      recusar a opção — está escrito em `respostas.go` e no README. A caixa fica por marcar até
      o dono dizer se isso chega.
- [x] A hipótese da grafia fica **confirmada ou desmentida** com respostas lidas, e o resultado
      registado na §5 com a contagem: quantas das ausências eram grafia e quantas eram conteúdo.
      **Confirmada (2026-10-13)**, numa qualificação real do `deepseek/deepseek-v4.1-flash` com
      `--guardar-respostas`, autorizada pelo dono: 12 ausências em 56 runs, e **as 12 eram
      grafia** — o modelo escreveu «Tomás Albergaria-Pinto» e o documento diz «Tomas». Zero de
      conteúdo. Só este modelo foi relido: as ausências dos outros cinco, da véspera, ficam por
      classificar, porque nessa medição as respostas não foram guardadas.
- [x] A regra nova dos factos tem testes com acentos, maiúsculas e espaços, e um controlo
      negativo: um facto de facto ausente continua a contar como ausente.
      **Feito (2026-10-13)**: `factos.go` e `TestAOS525_RegraDosFactos`. A regra não perdoa um
      nome trocado, um número diferente, um hífen a mais ou a menos nem uma letra em falta; os
      hífenes tipográficos valem o simples. **Limites declarados**, que continuam a contar como
      ausência: separador de milhares, formatação a meio do facto, ordem trocada. A
      definição da taxa no relatório diz a regra; um relatório anterior a esta versão foi
      medido com a regra literal, e os dois não se comparam nesta taxa.
- [x] A bateria 2.0.0 tem digest próprio; a 1.0.0 mantém o seu, e um relatório antigo continua
      a conferir.
      *Nota de 2026-10-13:* a 2.0.0 vive em `bateria2/` (sete casos, doze documentos), com o
      digest `sha256:fcc9bd8d…0c5a`, e só a experiência nova, `degraus`, a corre. A 1.0.0
      mantém `sha256:9f78316b…9a72`: o valor foi calculado dos ficheiros do commit anterior por
      um programa à parte e está preso em
      `TestAOS525_Degraus_ABateria2TemDigestProprioEA1MantemODeAntes`. **O que prova o
      «continua a conferir», exactamente:** o digest da 1.0.0 é o de antes, e o sha256 de
      referência do AOS-516 — os pedidos e o relatório de uma corrida da bateria sem perfil —
      não mudou nem foi tocado. **Não** foi conferido um relatório real antigo nem um registo
      assinado: a pasta do dono não se lê daqui.
- [x] Cada degrau tem um modelo falso que o cumpre e um que o falha, e o relatório distingue-os.
      *Nota de 2026-10-13:* `TestAOS525_Degraus_CadaDegrauTemQuemOCumpraEQuemOFalhe`. O
      comportamento `degrau_cumpre` cumpre os sete; cada degrau tem um roteiro que o falha —
      e só a ele —, e o relatório diz a razão: o contrato (degraus 1 e 5), um facto ausente
      (1, 2 e 7) ou uma resposta errada, com um facto proibido na saída (1 a 6). O provider
      falso de sempre, que despeja o que as tools entregaram, falha os sete por resposta errada,
      e um erro passageiro (um 500) tira a passagem das taxas em vez de contar como falha
      (correcções da revisão adversarial, no mesmo dia). Na segunda ronda: o que é proibido
      passou a estar vedado no próprio objectivo (degraus 1, 2 e 5), uma falha firme observada
      antes de uma interrupção conta como não cumprida, e o resumo marca o degrau com metade ou
      mais das passagens interrompidas. O cancelamento de uma corrida a meio está provado com
      um cancelamento verdadeiro, fora da qualificação
      (`TestAOS525_Degraus_UmaCorridaCanceladaNaoFalhaODegrauEmCurso`). Só no modo
      `falso`: o modo `proxy` desta experiência não foi corrido. Os modelos falsos provam que a
      medida distingue; não dizem nada de um modelo real.
- [x] Medição real registada na §5: por modelo e por degrau, nós cumpridos, falhas à primeira
      tentativa e factos ausentes pela regra nova; e a resposta, por extenso, a «há algum degrau
      em que um modelo barato falha e um caro passa?».
      **Medido (2026-10-13)**, seis modelos, 5 passagens cada. A §5 regista os degraus cumpridos
      por modelo e a leitura das respostas que falharam; as restantes taxas por degrau estão
      nos relatórios, na pasta do dono, e não foram transcritas. Resposta: sim para o modelo
      mais barato (volume e documento longo por inteiro); entre os outros cinco a bateria não
      separa.
- [x] Gates locais verdes; o módulo `qa/banco-ensaio` sem regressão de cobertura.
      **Medido (2026-10-13)**: cobertura do pacote 86,8% antes do ticket (commit `a33cdc1a`) e
      88,1% depois (`go test -cover .`); lint, scan de segredos, referências e deferimentos
      verdes antes de cada PR, e os 30 gates do CI verdes nos quatro PRs (#484 a #487).

### Notas

- O modo real corre um processo de cada vez, com `--so-plano` primeiro, e só com autorização
  do dono. Subagentes não correm o modo real.
- A 2026-10-12 o proxy efémero não ficou vivo a tempo em cinco arranques, com o posto sem
  memória livre. Se se repetir com o posto folgado, abre ticket próprio.

---

## AOS-528 — O conselheiro: o executor barato consulta um modelo mais forte por uma tool, de qualquer fornecedor

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: muda o banco de ensaio, que vive fora da imagem do nó. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — depois da A4 (resíduo (c) da §7 do acompanhamento: «a cascata não foi construída») |
| Tipo | feat |
| Prioridade | P2: decide se vale a pena construir alguma forma de escolha de modelo por passo; nada em produção depende dele |
| Estimativa | M |
| Dependências | AOS-525 (a bateria por degraus, a regra dos factos proibidos e as respostas guardadas), AOS-512 a AOS-518 (o banco, o proxy efémero, os tectos), feitos. O modo real só corre com autorização do dono |
| Bloqueia | Uma decisão do dono sobre construir, ou não, o conselheiro no nó |
| Responsável sugerido | Engenheiro de Qualidade |
| Documentos de referência | `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` (§5, de 2026-10-12 a 2026-10-14; §7, «Resíduos da fase A4», resíduo (c)), `docs/runbooks/PROC-BANCO-DE-ENSAIO.md`, `packages/qa/banco-ensaio/README.md` |

### Contexto

A fase A4 mediu que uma **cascata** de modelos (o barato primeiro, o caro quando o barato
falha) não se justificava com as tarefas que temos (acompanhamento §5):

- **A bateria 1.0.0 não separou modelos** (2026-10-12): seis modelos pela OpenRouter, nenhuma
  primeira tentativa sem tool call; o único `recusado` (um modelo de 8 mil milhões de parâmetros)
  foi-o por um nó em quarenta.
- **A bateria 2.0.0, por degraus, só separou o mais barato** (2026-10-13, 5 passagens por
  modelo): o llama-3.1-8b falhou volume e documento longo por inteiro; o Claude Haiku, o
  DeepSeek, o Kimi K2.7, o Gemini e o Claude Sonnet — de 0,03 a 0,52 USD por corrida — cumpriram
  os sete degraus (as falhas do Haiku em D5 e do DeepSeek em D7 eram o proibido citado numa
  negação, não conteúdo errado).
- **O canary fechou com o Haiku a 0 primeiras falhas em 165** (2026-10-14), contra 14 em 165 do
  Kimi, e o Haiku passou a titular de produção. O dono fechou a A4 no mesmo dia; o resíduo (c)
  diz que, se o trabalho ficar mais difícil, a bateria por degraus e o canary são o caminho para
  medir de novo.

O dono quer avaliar uma alternativa que resolve três dos quatro obstáculos da cascata — o
**conselheiro**: o modelo executor, barato, pode consultar a meio do turno um modelo mais forte
através de uma TOOL, e decide ele quando consultar. Não há regra de escalada a afinar, não se
repete o trabalho todo com o modelo caro, e o modelo caro só é pago quando é chamado. O quarto
obstáculo — haver tarefas em que o barato falha e o caro acerta — só se mede com tarefas mais
difíceis do que as que temos.

**Requisito do dono:** tem de funcionar com **vários fornecedores e vários modelos** — o executor
e o conselheiro podem vir de fornecedores diferentes (executor Haiku pela OpenRouter e conselheiro
Kimi pela conta Kimi, ou DeepSeek com Gemini). Não se usa a ferramenta `advisor` nativa da
Anthropic (só existe na API directa e devolve o conselho cifrado): o conselheiro é uma tool nossa.

### Objectivo

Dar ao dono, no banco de ensaio, a medida que decide se o conselheiro compensa: em casos
difíceis, quantos cumpre o executor sozinho, quantos cumpre com o conselheiro, quantas vezes
consulta, e quanto custa cada caso cumprido — com o executor e o conselheiro de quaisquer
fornecedores que o banco conheça.

### Âmbito

- **Bateria 3.0.0, «difícil».** Pasta embebida própria, documentos sintéticos sem acentos com a
  marca de sempre, nomes e códigos novos, digest próprio. Casos em que um modelo barato tem
  probabilidade real de falhar: dedução em vários passos sobre vários documentos; documentos
  contraditórios com uma regra explícita de qual prevalece; um documento longo (40 a 60 KB) com a
  resposta em duas passagens afastadas; várias restrições ao mesmo tempo. Cada facto exigido
  ocorre literalmente nos documentos; factos proibidos são as respostas erradas plausíveis. A
  1.0.0 e a 2.0.0 ficam como estão.
- **A tool `aconselhar`**, registada no Reference Monitor do nó de ensaio e oferecida só quando a
  corrida a liga: argumentos `pergunta` e, opcional, `contexto`; chama o modelo conselheiro e
  devolve o texto da resposta como resultado da tool. O conselheiro configura-se à parte
  (fornecedor e modelo), com as mesmas chaves, tectos e listas do ficheiro do dono, e passa pelo
  MESMO proxy efémero numa segunda rota com alias próprio. As consultas contam no contador e nos
  tectos do fornecedor do conselheiro, com o preço do modelo dele; o pior caso declarado inclui-as.
- **A experiência `conselheiro`**: a bateria 3.0.0 em braços — `so-executor` e
  `executor+conselheiro` — na mesma corrida e com a mesma semente, com o relatório por braço e
  por caso. Sem veredicto.
- **Falsos** que provam que a medida distingue: o executor que não consulta e erra, o que
  consulta e segue o conselho, o que consulta e o ignora; e um conselheiro que dá o conselho
  certo, ou o errado.
- **Fora de âmbito:** o conselheiro no nó de produção; a medição com modelos reais (só com
  autorização do dono, depois deste ticket); o modo `proxy` com conselheiro (o provider falso em
  contentor não faz de conselheiro); a ferramenta `advisor` da Anthropic.

### Critérios de Aceitação

- [x] A bateria 3.0.0 carrega com digest próprio, a validação é fail-closed (sem factos
      proibidos, facto no objectivo, objectivo repetido, degrau, facto fora dos documentos), e a
      1.0.0 e a 2.0.0 mantêm os digests.
      *Nota de 2026-10-15:* sete casos (X1 a X7) e dezassete documentos em `bateria3/`, digest
      `sha256:1475fdf0…7c37` (depois da revisão adversarial: nomes neutros nos documentos de X2 e
      X3, um quarto documento e três formatos de data em X2, e em X7 o calendário passa a ser do
      modelo). `TestAOS528_Bateria3_TemDigestProprioEAsOutrasMantemOSeu`,
      `…_OsCasosPedemOQueDizem` e `…_OCarregamentoEFailClosed`.
- [x] O proxy efémero sobe com duas rotas quando há conselheiro — cada uma com a chave e a base do
      seu fornecedor, pelo ambiente e nunca pelos argumentos — e, sem conselheiro, com a
      configuração de antes, byte a byte.
      *Nota de 2026-10-15:* `TestAOS528_Proxy_…`. A imagem real do proxy com duas rotas não foi
      levantada (precisava de Docker com a imagem descarregada e não foi corrida aqui).
- [x] As consultas contam no contador e nos tectos do fornecedor do conselheiro, e o custo no
      preço do modelo dele; um modelo de conselheiro fora da lista do dono é recusado; um tecto do
      conselheiro que não chega recusa a corrida antes de enviar, e um que se esgota a meio pára-a.
      *Nota de 2026-10-15:* no modo real com o lançador de teste
      (`TestAOS528_Real_ConselheiroDeOutroFornecedor`: executor Kimi e conselheiro Anthropic, e
      os dois no Kimi) e no motor da tool (`TestAOS528_OTectoDoConselheiroParaAConsulta`).
- [x] O pior caso declarado inclui as consultas (`--max-consultas`, omissão 2), é dito por
      fornecedor e é imposto — no tecto do dia e por run.
      *Nota de 2026-10-15:* `TestAOS528_OPiorCasoIncluiAsConsultasEEImposto`. Executor 336
      pedidos por passagem (168 sem conselheiro); conselheiro 21 por consulta permitida.
- [x] Os braços distinguem os falsos (a), (b) e (c) e o conselheiro certo do errado, e o custo
      por caso cumprido está certo.
      *Nota de 2026-10-15:* `TestAOS528_OsBracosDistinguemOsFalsos` e
      `TestAOS528_CustoPorCasoCumpridoEContasPorBraco`. Os falsos provam que a medida distingue;
      não dizem nada de um modelo real.
- [x] A pergunta e o conselho não aparecem no relatório, no resumo nem nas saídas; com
      `--guardar-respostas` ficam no ficheiro de respostas do run, e só lá.
      *Nota de 2026-10-15:* `TestAOS528_Fuga_PerguntaEConselhoSoNasRespostasGuardadas`.
- [x] As experiências de antes, o falso por omissão e os testes pinados (AOS-516, digests da
      1.0.0 e da 2.0.0) não mudam.
      *Nota de 2026-10-15:* `TestAOS528_AsExperienciasDeAntesNaoMudam` e a suite inteira do
      módulo verde, com os testes pinados do AOS-516 e do AOS-525 por tocar.
- [ ] Medição real registada na §5 do acompanhamento: pelo menos um par executor/conselheiro de
      fornecedores diferentes, os dois braços e a corrida do tecto, com a resposta, por extenso, a
      «há casos em que o barato falha sozinho e cumpre com o conselheiro, e a que custo por caso
      cumprido?». **Por fazer, com autorização do dono.**
      *Nota de 2026-10-15:* medida em parte, com autorização do dono («sim autorizo») — fica por
      marcar porque a corrida do tecto não se fez. Triagem: os executores sozinhos (llama 8b,
      Haiku, DeepSeek); o Haiku e o DeepSeek acertam o conteúdo dos sete casos — as seis «falhas»
      do Haiku são o proibido citado na explicação —, e só o llama falha por conteúdo. Dois pares
      com o llama: um na OpenRouter (conselheiro Haiku) e um com fornecedores diferentes
      (conselheiro `k3` na conta Kimi). Resposta: **não** — o llama consultou em 3 de 19 e 2 de 21
      runs, e as diferenças entre braços estão dentro da variação do llama sozinho (8 de 20 a 12
      de 19). Os outros pares e o tecto não se correram: sem falhas de raciocínio nos modelos
      bons, não mudariam a resposta. Detalhe na §5 do acompanhamento.

### Notas

- O modo real corre um processo de cada vez, com `--so-plano` primeiro, e só com autorização do
  dono. Subagentes não correm o modo real.
- O tecto (o modelo forte sozinho) é outra corrida da mesma experiência, sem conselheiro e com a
  mesma semente: o nó de ensaio tem um executor por corrida.
- A taxa de consulta depende do texto da tool e do esforço do modelo executor: é uma medida do
  par (executor, texto da tool), não do conselheiro.

---

## AOS-529 — O banco de ensaio mede um motor de OCR com imagens sintéticas

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: muda o banco de ensaio, que vive fora da imagem do nó. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A5 (multimodal), sub-fase A5.1 «OCR como tool»; rótulo do desenho «A5-ocr-medição» |
| Tipo | feat |
| Prioridade | P2: decide o motor do AOS-530 antes de haver código no nó; nada em produção depende dele |
| Estimativa | M |
| Dependências | AOS-525 (a regra dos factos), AOS-512 (o banco de ensaio), feitos. Decisões D1, D2, D5 e D7 do desenho, tomadas pelo dono a 2026-10-15 como o desenho as recomenda |
| Bloqueia | AOS-530 (a tool `ocr_read`) |
| Responsável sugerido | Engenheiro de Qualidade |
| Documentos de referência | `docs/reports/desenho-a5-a6-multimodal-2026-10-14.md` (§5.1, §7 D1, D2, D5, D7, §8), `docs/runbooks/PROC-BANCO-DE-ENSAIO.md`, `packages/qa/banco-ensaio/README.md` |

### Contexto

O dono abriu a fase A5 a 2026-10-15 e tomou as decisões D1 a D9 do desenho
`docs/reports/desenho-a5-a6-multimodal-2026-10-14.md` como ele as recomenda:

- **D1 (a), ferramentas primeiro.** Uma imagem chega a um run como texto lido por uma tool
  mediada pelo Reference Monitor; o titular não vê a imagem.
- **D2 (a), o OCR corre localmente, dentro do gVisor, sem rede.** O motor escolhe-se **medindo**
  no banco de ensaio, antes de construir a tool (§5.1, «medir antes de construir», a lição da A4).
  O Tesseract é o candidato óbvio; a qualidade em português, o tempo e a memória dentro de 45 s e
  1 GB estão por confirmar.
- **D5 (a), replay pelo texto da tool e pelo digest do media**, sem re-correr a tool.
- **D7 (c), sub-fases com critério próprio.** A A5.1 propõe «factos presentes em pelo menos 90%»
  na bateria de imagens sintéticas — uma proposta, não uma medida.

O executor gVisor de produção corre cada chamada com 45 s (`GV_EXEC_TIMEOUT`,
`deploy/server/gvisor/component/main.go:60`), sem rede (`--network=none`, `:154-159`), raiz só
de leitura, sem capabilities e com o uid 65532 (`:200-236`). A memória (1 GB) e os processos
(512) são do serviço `gvisor` inteiro (`deploy/server/docker-compose.prod.yml:938-939`), não de
cada chamada: o `config.json` do sandbox não declara limites. O rootfs do guest tem só o binário
estático do guest (`component/main.go:128-140`), que conhece um verbo, `read`
(`deploy/server/gvisor/guest/main.go:69-88`).

### Objectivo

Dar ao dono, no banco de ensaio, a medida que decide o motor de OCR: por imagem sintética e por
degradação, os factos presentes, o tempo, a memória máxima e o tamanho do texto, com o motor a
correr nas mesmas condições em que a tool vai correr.

### Âmbito

- **Bateria de imagens sintéticas**, pasta embebida própria (`bateria-ocr/`), digest próprio:
  documentos com e sem acentos (os documentos reais em português têm acentos), uma tabela com
  grelha, e imagens geradas de forma determinista com a biblioteca-padrão (uma fonte bitmap
  desenhada no banco) em PNG limpo, PNG com ruído e rotação ligeira, PNG de meia resolução, JPEG
  e um PDF digitalizado de uma página. Cada documento tem os seus factos (códigos, nomes,
  números), que ocorrem literalmente no texto. Nenhum binário no repositório.
- **A experiência `ocr`** (`aos-ensaio ocr`), com o **motor plugável**: uma interface pequena
  (imagem → texto); o tempo mede-se por fora, à volta da chamada, e a memória mede-a quem corre
  o motor. O motor escolhe-se por opção da linha de comandos; acrescentar um candidato é escrever
  um adaptador. Os factos contam-se pela regra do banco (sem acentos nem maiúsculas) e, à parte,
  com os acentos exactos. Relatório JSON e resumo `.txt` com uma tabela por imagem, outra por
  degradação, formato e acentos, e os limites declarados. Sem veredicto.
- **Três adaptadores:**
  - o **Tesseract** num contentor Docker **fixado pelo conteúdo** — `nome@sha256:<64 hex>` ou o
    ID `sha256:<64 hex>` de uma imagem construída no posto com `ocr/Dockerfile` (base fixada pelo
    digest) —, com os limites do gVisor (sem rede, 45 s, 1 GiB, 512 processos, raiz só de
    leitura, sem capabilities, uid 65532) e `--pull never`;
  - um **modelo de visão** pelo proxy efémero do banco (decisão do dono de 2026-10-15), como o
    conselheiro do AOS-528: a imagem como parte `image_url` numa data URL, uma instrução fixa de
    transcrever, o fornecedor e o modelo da lista do ficheiro de chaves do dono, os tectos, o
    contador e o preço do fornecedor dele, o pior caso declarado, o tempo e o custo estimado por
    imagem, e o texto do modelo nunca nas saídas nem no relatório (só com `--guardar-respostas`).
    O PDF fica fora deste braço: é a mesma página de outra imagem da bateria;
  - um **motor falso** com quatro perfis — lê tudo, perde os acentos, erra factos, falha os PDF.
- **Outros candidatos entram como adaptadores**, sem mudar a experiência: os motores que o dono
  está a avaliar (por exemplo Docling com Tesseract, EasyOCR, RapidOCR ou surya; MinerU; Marker;
  LightOnOCR-2-1B; Chandra OCR 2). Quais, decide o dono.
- **Fora de âmbito:** a tool `ocr_read` (AOS-530); qualquer mudança no nó, no gVisor ou no
  gateway; descarregar imagens Docker (o banco não as descarrega); os adaptadores dos outros
  candidatos; documentos reais.

### Critérios de Aceitação

- [x] A bateria de OCR carrega com digest próprio e estável, cobre documentos com e sem acentos,
      as três degradações, os três formatos e uma tabela; a validação é fail-closed (marca, facto
      fora do texto, letra que a fonte não tem, vocabulário, referências, repetidos, tabela
      irregular); as baterias 1.0.0, 2.0.0 e 3.0.0 mantêm os digests.
      *Nota de 2026-10-15:* quatro documentos e treze imagens em `bateria-ocr/casos.json`, digest
      `sha256:390f70c8…c703` (depois da revisão adversarial: o zero sem barra e mais estreito do
      que o «O»). `TestAOS529_BateriaOCR_TemDigestProprioEAsOutrasMantemOSeu`,
      `…_CobreAcentosDegradacoesEFormatos`, `…_OCarregamentoEFailClosed` e
      `TestAOS529_OZeroNaoTemBarraEDistingueSeDoO`.
- [x] A experiência `ocr` com os motores falsos distingue os quatro no relatório e no resumo: o
      que lê tudo (100% pela regra e com acentos), o que perde os acentos (100% pela regra, menos
      com acentos, e só nas imagens com acentos), o que erra factos (o número exacto de ausentes
      por imagem e no resumo) e o que falha uma imagem (estado `falhou`, causa, e os factos dela
      ausentes).
      *Nota de 2026-10-15:* `TestAOS529_ORelatorioDistingueOsTresMotoresFalsos`,
      `TestAOS529_UmMotorQueErraFactosTemOsAusentesContados` (58 de 80 factos; a mutação «conta
      sempre presente» morre nele) e `TestAOS529_LinhaDeComandos`.
- [x] O adaptador do Tesseract recusa uma imagem não fixada e uma imagem que não está no posto,
      sem a descarregar (`--pull never`); aceita o ID de uma imagem do posto; corre com
      `--network none`, 1 GiB, 512 processos, raiz só de leitura, sem capabilities e uid 65532,
      com a pasta montada legível por esse uid; classifica o limite de tempo (e remove o
      contentor) e a memória esgotada pelo pico do cgroup, venha a falha de que estágio vier.
      *Nota de 2026-10-15:* com um Docker de teste, não com o Tesseract:
      `TestAOS529_TesseractCorreNasCondicoesDoGVisor`, `…_TesseractRecusaImagemNaoFixadaOuAusente`,
      `…_TesseractClassificaTempoEMemoria`, `…_TesseractAceitaOIDDeUmaImagemDoPosto`,
      `…_APastaMontadaELegivelPeloUidDoContentor` e `…_AFalhaClassificaSePeloEstagioEPeloPico`.
- [x] O braço do modelo de visão segue as regras do modo real — nunca em CI, o ficheiro de chaves
      explícito, um modelo fora da lista do dono recusado, o pior caso (13 pedidos) a caber no
      tecto antes de enviar, `--so-plano`, a sonda — e dá o custo estimado por imagem; uma recusa
      do fornecedor ou o tecto param a corrida; o texto do modelo não chega às saídas nem ao
      relatório; o relatório entra na reconstrução do contador do dia.
      *Nota de 2026-10-15:* com um modelo de visão falso, não com um modelo:
      `TestAOS529_Visao_LeAsImagensDeixaOPDFForaEContaOCusto`, `…_UmModeloQuePerdeAcentosVeSe`,
      `…_OTectoEARecusaParamACorrida` e `…_LinhaDeComandos`.
- [x] Os testes pinados de antes (AOS-516, digests das baterias) e as experiências de antes não
      mudam.
      *Nota de 2026-10-15:* a suite inteira do módulo verde, sem tocar nos testes de antes. O uso
      geral (`usoDoBanco`) ganhou uma linha a apontar para `aos-ensaio ocr`; a verificação do
      contador do dia que não existe passou para uma função (`verificarContadorDoDia`), partilhada
      com o braço de visão, sem mudar o comportamento.
- [x] Medição real registada: o Tesseract (a imagem `aos-ensaio-ocr:tesseract`, construída pelo
      dono a 2026-10-15) e, com autorização do dono, um ou mais modelos de visão, sobre a bateria,
      com os factos, o tempo, a memória e o custo por imagem.
      *Nota de 2026-10-15:* o Tesseract correu localmente, no posto do dono; os modelos de visão
      pela OpenRouter, com autorização do dono. Pela regra do banco (e com os acentos exactos):
      **Tesseract 82,5% dos factos (76,2%)**, ~0,9 s por imagem, custo zero; **Claude Haiku 5.5
      100% (100%)**, 2 a 9 s por imagem, ~0,0001 USD por imagem; **Gemini 3.8 Flash 89,2%**,
      ~0,0034 USD por imagem — mas a resposta da tabela veio **cortada**, provavelmente pelo limite
      de tokens de saída do adaptador (`TokensDaVisao = 2048`, `ocr_visao.go:59`), por confirmar:
      os 89,2% não são a medida do Gemini. Ressalvas: imagens sintéticas com a fonte bitmap 5×7;
      doze imagens no braço de visão (o PDF fica fora). A memória de pico por imagem não é
      transcrita nesta nota: fica no relatório da corrida, no posto do dono. **A decisão que a
      medição motivou:** o dono mudou a D2 do desenho — o OCR passa a ser feito por um modelo de
      visão através de uma tool, pelo Model Gateway do nó (ADR-042, proposto; AOS-530 reescrito),
      e o Tesseract fica como alternativa local para documentos que não podem sair do servidor.

### Notas

- O tempo inclui o arranque do contentor, como no gVisor inclui o do `runsc`. A memória é a do
  cgroup do contentor (`memory.peak`). O contentor corre com o runc do Docker, não com o gVisor, e
  uma chamada de cada vez.
- O Tesseract não lê PDF: o adaptador rasteriza-o com o `pdftoppm` a 300 ppp, pelo stdout, sem
  escrever em disco, e recolhe o código de cada estágio do pipe. Se a tool do AOS-530 aceitar
  PDF, o rootfs precisa dos dois.
- A fonte é bitmap 5×7, sem serifas, desenhada no banco: a taxa medida com ela não é a de um
  documento impresso, e pode pesar de maneira diferente num OCR clássico e num modelo de visão —
  vale para a comparação entre motores, não só para a taxa de cada um. É um limite declarado no
  relatório.

---

## AOS-534 — Spike A6.1: a parte `video_url` atravessa o proxy LiteLLM fixado intacta, nas duas rotas da OpenRouter?

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: muda o banco de ensaio, que vive fora da imagem do nó. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A6 (multimodal), sub-fase A6.1 «vídeo como tool»; rótulo do desenho «A6-video» |
| Tipo | spike |
| Prioridade | P2: o primeiro de três elos por verificar antes de haver código no nó; nada em produção depende dele |
| Estimativa | S |
| Dependências | AOS-512 (o banco de ensaio), AOS-516 (o falso na forma da OpenRouter e as duas rotas do proxy), feitos. Decisão do dono de 2026-10-18 (opção A, vídeo nativo) |
| Bloqueia | AOS-535, AOS-536 (a seguir) |
| Responsável sugerido | Engenheiro de Qualidade |
| Documentos de referência | `docs/reports/desenho-a5-a6-multimodal-2026-10-14.md` (§5.4, §5.5), `packages/qa/banco-ensaio/README.md`, `scripts/ci/banco-ensaio-proxy.sh`, a página de vídeo da OpenRouter (`https://openrouter.ai/docs/guides/overview/multimodal/videos`) |

### Contexto

**A decisão do dono (2026-10-18).** A sub-fase A6.1 segue a **opção A — vídeo nativo**: o ficheiro
de vídeo (MP4) vai inteiro ao modelo, como uma parte `{"type":"video_url","video_url":{"url":"data:video/mp4;base64,…"}}`
ao lado do texto numa mensagem `user`, pela OpenRouter, através do proxy LiteLLM de produção
(`ghcr.io/berriai/litellm@sha256:154e23bb…`, versão 1.96.2). É a forma que a página de vídeo da
OpenRouter documenta.

**A discovery: três elos por verificar** antes de escrever código no nó:

1. **O proxy.** O LiteLLM fixado, nas duas formas de rota que tem para a OpenRouter
   (`openrouter/<autor>/<modelo>`, o adaptador próprio, e `openai/<autor>/<modelo>` com a base da
   OpenRouter), entrega ao fornecedor a parte `video_url` intacta — o tipo, a data URL, os mesmos
   bytes —, recusa-a, ou estraga-a (troca-lhe o tipo, re-codifica, corta)? Uma parte que o proxy
   não conhece pode ser descartada pelo `drop_params`, convertida noutra (`file`, `image_url`) ou
   rejeitada antes de sair. E tem um tecto de corpo?
2. **Os modelos.** Que modelos aceitam vídeo pela OpenRouter, e com que preço por segundo ou por
   token de vídeo. Por medir, com custo — não é deste ticket.
3. **Os limites.** O tamanho e a duração máximos que a OpenRouter e cada modelo aceitam numa data
   URL. Por medir, com custo — não é deste ticket.

O primeiro elo mede-se sem custo: o banco de ensaio já põe a imagem de produção do proxy entre um
cliente e um fornecedor falso num contentor (AOS-512, AOS-516), e o falso regista a forma do que
recebe, nunca o conteúdo.

### Objectivo

Dizer, com uma medida, o que o proxy fixado faz a uma parte `video_url` em cada rota da
OpenRouter, sem modelo real e sem custo.

### Âmbito

Só o elo do proxy.

- **O falso do estado regista as partes do `content`** das mensagens que recebe: o `type` de cada
  parte (vocabulário fechado; um tipo fora dele conta como `outra`), as chaves do objecto da parte,
  e, numa parte com uma data URL (`video_url.url`, `image_url.url`, `file.file_data`), a classe da
  URL (`data:video/mp4;base64,`, outra data URL, URL remota, outra), o sha256 e o tamanho dos
  bytes descodificados. Os bytes não se gravam. O tecto de corpo do falso sobe para 32 MiB e um
  corpo acima dele conta-se com causa própria, para o tecto do falso não se confundir com o do
  proxy.
- **O cenário `TestAOS534_ProxyReal_VideoPeloProxy`**, no padrão das medições directas do
  AOS-516: um pedido directo ao proxy real, pelas duas rotas, com texto e uma parte `video_url`
  com um MP4 **sintético**, válido como contentor (caixas `ftyp`, `moov`/`mvhd` com duração
  conhecida e `mdat`) e não descodificável; um caso pequeno e um de 3 a 6 MB. Confere no rasto do
  falso o tipo, a data URL e o sha256. **Mede, não julga:** uma recusa ou uma alteração do proxy é
  um resultado, que o relatório do teste diz por rota.
- **Testes do registo do falso** que correm sem Docker.

### Critérios de Aceitação

- [x] O falso regista, por parte do `content`, o tipo, as chaves e, numa data URL, a classe, o
      sha256 e o tamanho; nenhum byte do media nem texto da mensagem aparece no que regista; um
      tipo ou uma chave fora do vocabulário conta como `outra`; um corpo acima do tecto do falso
      conta-se com causa própria. Com testes que correm sem Docker.
- [x] O cenário com o proxy real corre pelas duas rotas, com o MP4 pequeno e o de 3 a 6 MB, e o
      seu relatório (`AOS_BANCO_VIDEO_REPORT`) diz, por rota e por tamanho, o código HTTP do proxy,
      os tipos de parte que chegaram ao fornecedor e se o sha256 é o enviado. Fica vermelho só se o
      ensaio não se montar, se o proxy não chegar ao falso ou se uma chave aparecer numa saída.
- [x] A medida registada neste ticket, numa nota com a data, com a conclusão para o elo 1.
      *(Medido a 2026-10-19 no posto, com Docker, `TestAOS534_ProxyReal_VideoPeloProxy`, proxy
      `ghcr.io/berriai/litellm@sha256:154e23bb…` (1.96.2) contra o falso na forma da OpenRouter,
      modelo `google/gemini-2.5-flash`: nas duas rotas (`openrouter/…` e `openai/…`), o MP4 pequeno
      (4 248 bytes) e o grande (4 194 456 bytes, pedido de 5 592 829 bytes) chegaram ao fornecedor
      como `video_url` com data URL `video/mp4` e o **sha256 igual ao enviado** — `intacta` em 4 de 4;
      o controlo só com texto chegou com 200 nas duas. **Conclusão para o elo 1: o proxy fixado
      passa o vídeo nativo intacto, sem tecto de corpo até 5,6 MB.** Ficam os elos 2 (que modelos
      aceitam vídeo pela OpenRouter, a medir com o modelo real) e 3 (limites do fornecedor).)*

### Fora de âmbito

- Os elos 2 e 3 (que modelos aceitam vídeo pela OpenRouter, e os limites de tamanho e duração):
  precisam do modelo real e de custo.
- Qualquer mudança no nó, no gateway, no perfil de rota ou no `tools.json`.
- A porta 1.13.0 e a capacidade `video` (AOS-535) e a tool de vídeo (AOS-536), a seguir.

### Notas

- O MP4 sintético tem a magia e o tamanho certos, não imagem: o proxy não o descodifica, por isso
  basta para medir o que ele faz aos bytes. Um fornecedor real recusá-lo-ia.
- O cenário é opcional e precisa de Docker e da imagem já descarregada; não descarrega nada.
  Corre-se com `AOS_BANCO_PROXY=1 AOS_BANCO_FALSO_BIN=<binário linux do banco> go test -run '^TestAOS534_ProxyReal_VideoPeloProxy$' -v -count=1 .`
  em `packages/qa/banco-ensaio`.

---

## AOS-537 — O banco de ensaio mede o vídeo: a experiência `video`, com uma bateria sintética gerada pelo ffmpeg e um veredicto contra o limiar de factos

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: muda o banco de ensaio, que vive fora da imagem do nó. O ADR-042 é citado só como precedente do veredicto do OCR. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A6 (multimodal), sub-fase A6.1 «vídeo como tool»; rótulo do desenho «A6-video-medição» |
| Tipo | feature (banco de ensaio) |
| Prioridade | P2: os elos 2 e 3 da A6.1 (que modelos lêem vídeo pela OpenRouter, e com que limites) medem-se com isto; nada em produção depende dele |
| Estimativa | M |
| Dependências | AOS-529 e AOS-530 (a experiência `ocr`, o seu veredicto e o braço de visão pelo proxy do banco), AOS-534 (o proxy passa a parte `video_url` intacta até 5,6 MB), feitos. Decisões do dono de 2026-10-18 (opção A; o ffmpeg só no banco, com a imagem fixada pelo digest; os três candidatos; o limiar de 90%) |
| Bloqueia | AOS-536 (a tool de vídeo precisa de um modelo medido) |
| Responsável sugerido | Engenheiro de Qualidade |
| Documentos de referência | `docs/reports/desenho-a5-a6-multimodal-2026-10-14.md` (§5.4, §5.5), `packages/qa/banco-ensaio/README.md` («A medição do vídeo»), `docs/runbooks/PROC-BANCO-DE-ENSAIO.md` («Medir um modelo de vídeo») |

### Contexto

O AOS-534 fechou o elo 1 da A6.1: o proxy LiteLLM fixado entrega ao fornecedor a parte
`{"type":"video_url","video_url":{"url":"data:video/mp4;base64,…"}}` intacta, nas duas rotas da
OpenRouter, até 5,6 MB. Faltam os elos 2 (que modelos aceitam e **lêem** vídeo pela OpenRouter) e
3 (os limites de tamanho e duração), que só o modelo real diz, com custo. Para os medir, o banco
precisa do que já tem para o OCR: uma bateria sintética com factos conhecidos, uma experiência com
o motor plugável e um veredicto.

**Decisões do dono (2026-10-18).** Vídeos de teste gerados com o **ffmpeg só no banco**, pela imagem
`jrottenberg/ffmpeg@sha256:8ec1ee1f6a0fcd37c97725827b6b7832795c9596e3439b8da56d7700d61ae778`,
autorizada e já descarregada no posto. Candidatos a medir pela OpenRouter:
`google/gemini-3.5-flash-lite`, `google/gemini-3.8-flash`, `qwen/qwen3-vl-32b-instruct` (o Kimi
directo fica fora). Limiar do veredicto: **90% dos factos**, o do OCR (D7). O alias de produção
futuro é `video-visao` (não é deste ticket).

### Objectivo

Medir, por modelo, se ele descreve os factos de um vídeo curto — o texto que aparece, as contagens,
as cores e a ordem dos acontecimentos — e se transcreve como dado uma instrução escrita no
fotograma, com tempo, tokens e custo por vídeo, e um veredicto contra o limiar do dono.

### Âmbito

- **A bateria de vídeo** (`bateria-video/casos.json`, digest próprio): oito vídeos de 6 a 30 s, de
  1 a 4 fps, de 320×240 a 640×360, cada um uma sequência de cenas estáticas (fundo de cor pura,
  linhas de texto na fonte bitmap do OCR, grupos de formas). 34 factos de cinco tipos, todos
  verificáveis pelo texto da resposta com a regra dos factos do banco: `texto`, `contagem`
  (`COUNT: <n> <cor> <forma>s`), `cor` (`BACKGROUND <cena>: <cor>`), `ordem` (duas linhas, a
  primeira antes da segunda) e `injeccao` (as linhas de uma instrução escrita no fotograma: o facto
  é transcrevê-las como dado). Validação fail-closed (dimensões, ritmo, duração, vocabulário, fundos
  seguidos iguais, cada facto ser do seu vídeo, todos os tipos cobertos).
- **Os fotogramas desenham-se em Go**, deterministas e em memória; o digest cobre o ficheiro dos
  casos e os píxeis. **O MP4 (H.264, `yuv420p`, `+faststart`) gera-o o ffmpeg** num contentor
  `--rm --network none --read-only --cap-drop ALL --pull never`, uid 65532, 1 GiB, 512 processos,
  120 s, com a imagem fixada pelo digest; os fotogramas entram por stdin em RGBA cru; o MP4 sai
  para uma pasta temporária, a única montada. O banco recusa se a imagem não estiver no posto e
  diz o `docker pull` a fazer; confere cada MP4 (`ftyp`, `moov` antes da `mdat`, a duração da `mvhd`,
  as dimensões da `tkhd`, até 4 MiB) antes de o usar; cache por processo, pelo digest da bateria.
- **A experiência `video`** (`aos-ensaio video`), com dois motores: o **falso** (sem rede nem
  Docker; lê os factos de origem — `certo`, `erra-factos`, `cortado`) e o **`visao`**, pelo proxy
  efémero do banco, com a parte `video_url` e uma instrução fixa e versionada (1.0.0, digest no
  relatório), `max_tokens` 8192 e a resposta cortada (`finish_reason: length`) como falha do vídeo.
  As regras do modo real são as do braço de visão do OCR, extraídas para `rota_paga.go` sem mudar
  uma mensagem: o ficheiro de chaves, a lista de modelos, o contador e o tecto do dia, o pior caso
  (um pedido por vídeo e a sonda) antes de enviar, `--so-plano` com o destino da chave, nunca em CI,
  e as qualificações do dia com `--perfil`. Os MP4 geram-se depois do plano e antes do proxy.
- **O relatório e o veredicto**, como os do OCR: por vídeo e por tipo de facto, pela regra e com
  acentos, tempo, tokens e custo pela tabela de preços; o digest da bateria, do codificador e do
  próprio relatório; `qualificado` só com pelo menos 90% dos factos e nenhum vídeo por descrever.
  O texto do modelo não sai do relatório nem das saídas (só com `--guardar-respostas`).

### Critérios de Aceitação

- [x] A bateria de vídeo carrega com digest próprio e estável, cobre os cinco tipos de facto, as
      dimensões e os ritmos pedidos e um vídeo de 30 s; a validação é fail-closed; as baterias de
      antes mantêm os digests.
      *Nota de 2026-10-19:* `TestAOS537_BateriaVideo_TemDigestProprioECobreOPedido` (bateria 1.1.0,
      digest `sha256:6a52cb40…9b97`), `…_OCarregamentoEFailClosed` (21 defeitos, cada um pela sua razão) e
      `TestAOS537_Fotogramas_DesenhamOQueOCasoDiz` (fundos, texto e as formas contadas nos píxeis).
- [x] Os MP4 conferem-se antes de ir ao modelo, e o ffmpeg corre nas condições fixadas.
      *Nota de 2026-10-19:* sem Docker, `TestAOS537_MP4_LeAFormaERecusaOQueNaoEOVideo`,
      `TestAOS537_ComMP4_ConfereOQueOCodificadorDevolve` e `TestAOS537_FFmpeg_CorreNasCondicoesFixadas`
      (com um Docker de teste: argumentos, fotogramas pelo stdin, recusa com o `docker pull`).
- [x] Com Docker, a bateria gera-se com o ffmpeg real e cada MP4 tem `ftyp`, a duração e as
      dimensões esperadas: `AOS_BANCO_FFMPEG=1 go test -run '^TestAOS537_FFmpegReal_GeraABateria$' -v -count=1 .`
      em `packages/qa/banco-ensaio` — por correr no posto do dono.
      *(2026-10-19, no posto: `TestAOS537_FFmpegReal_GeraABateria` gerou os 8 MP4 da bateria 1.1.0 com a duração e as dimensões pedidas, 2 a 14 KB, pela imagem `jrottenberg/ffmpeg@sha256:8ec1ee1f…`.)*
- [x] Os motores falsos e o veredicto: `certo` ⇒ `qualificado` (e não vale para produção, por não
      ser do modo real), `erra-factos` ⇒ `recusado` com o número exacto de presentes, `cortado` ⇒
      `recusado` com o vídeo mais longo `falhou:cortada`; a fronteira dos 90%.
      *Nota de 2026-10-19:* `TestAOS537_Falsos_OVeredictoDistingueOsTres`, `TestAOS537_ARegraDosFactos`.
- [x] O pedido do motor `visao` leva a parte `video_url` com a data URL `video/mp4` e os bytes do MP4,
      a instrução fixa e nenhuma tool; o tecto e uma recusa param a corrida, um 400 falha o vídeo,
      o custo conta-se; pela linha de comandos, as regras do modo real e o relatório que entra na
      reconstrução do contador do dia e nas qualificações do dia.
      *Nota de 2026-10-19:* com um fornecedor falso em `httptest`, `TestAOS537_Visao_OPedidoLevaOVideoInteiroSemTools`,
      `…_DescreveOsVideosEContaOCusto`, `TestAOS537_LinhaDeComandos` e `TestAOS537_Perfil_ContaNasQualificacoesDoDia`.
- [x] Medição real, com autorização do dono, dos três candidatos, com o `--so-plano` primeiro: os
      factos por tipo, o tempo e o custo por vídeo, e o veredicto de cada um.
      *(2026-10-19, com autorização do dono, cinco candidatos em vez de três (o `qwen3-vl-32b-instruct` não aceita vídeo pela API da OpenRouter; o dono aceitou cinco de cinco fornecedores): Gemini 3.5 Flash-Lite 34/34 qualificado (0,0048 USD); Kimi K3 34/34 qualificado (0,0653 USD); Qwen 3.8 Flash 31/34 qualificado; GLM 5.3 Flash 25/34 e Nova 2 Lite 25/34 recusados, com as respostas lidas. O dono escolheu o Gemini 3.5 Flash-Lite.)*

### Fora de âmbito

- A porta do gateway 1.13.0 e a capacidade `video` do perfil (AOS-535): até lá, o `--perfil` da
  experiência identifica-se só pelo digest e não pode declarar `tools`.
- A tool de vídeo e o alias `video-visao` (AOS-536).
- A medição real paga: corre-a o dono, com autorização, no posto.
- Vídeos reais, som, movimento; descarregar imagens Docker.

### Notas

- O MP4 não sai pelo stdout do contentor, ao contrário do pedido inicial: o `+faststart` obriga o
  ffmpeg a reescrever o ficheiro no fim, e o muxer de MP4 recusa uma saída sem `seek`; a
  alternativa (MP4 fragmentado) não leva a duração na `mvhd`. Sai para uma pasta temporária do
  posto, a única montada, que o banco lê e apaga.
- As contagens e as cores verificam-se nas linhas que a instrução pede (`COUNT`, `BACKGROUND`): um
  modelo que vê certo e escreve noutra forma perde esses factos. É um limite declarado no relatório.
- **Revisão independente (2026-10-19), corrigida no mesmo ramo; relatório versão 2, bateria 1.1.0.**
  Um `qualificado` era de confiança, mas um `recusado` podia vir da formatação: uma resposta perfeita
  em negrito (`**COUNT:** 4 red circles`) dava 18 de 34. Os factos de contagem e de cor passaram a
  ler-se das linhas rotuladas com uma dobra própria do vídeo (sem `*`, `_`, crases nem `|`, sem o
  espaço antes de `:`, várias contagens numa linha), e uma contradição (duas cores para a mesma cena,
  hipóteses, dois números para a mesma forma e cor) falha o facto; a regra comum dos factos, a do OCR
  e dos degraus, não mudou. O vídeo mais longo passou a contagem decrescente, para o par de ordem
  contrariar a ordem alfabética e a numérica. A pasta montada passou de 0777 a 0733, e o MP4 lê-se só
  como ficheiro regular, sem seguir ligações; o custo de um pedido soma-se antes do registo no
  contador. Regressões: `aos537_revisao_test.go`.

---

## AOS-535 — A porta do gateway 1.13.0 com a parte de vídeo e a capacidade `video` no perfil da rota

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: segue a opção A do dono de 2026-10-18 e o precedente da peça 1 do AOS-530. O ADR-042 é citado só como a origem dessa peça. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A6 (multimodal), sub-fase A6.1 «vídeo como tool»; rótulo do desenho «A6-video-porta» |
| Tipo | feature (Model Gateway) |
| Prioridade | P2: a tool de vídeo (AOS-536) precisa de uma porta que leve o vídeo; nada em produção o pede hoje |
| Estimativa | S |
| Dependências | AOS-530 peça 1 (a porta 1.12.0 com as partes de imagem e a capacidade `imagem`, o precedente exacto), AOS-534 (o proxy fixado passa a parte `video_url` intacta até 5,6 MB) e AOS-537 (o banco gera MP4 e manda-os nessa forma), feitos. Opção A do dono de 2026-10-18 |
| Bloqueia | AOS-536 (a tool de vídeo) |
| Responsável sugerido | Engenheiro de Plataforma |
| Documentos de referência | `tecnica/06_Model_Gateway_Custos.md` §5, `docs/reports/desenho-a5-a6-multimodal-2026-10-14.md` (§5), `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` §4 |

### Contexto

O AOS-534 provou que o proxy LiteLLM fixado entrega ao fornecedor a parte
`{"type":"video_url","video_url":{"url":"data:video/mp4;base64,…"}}` intacta, nas duas rotas da
OpenRouter, com um MP4 de 4 194 456 bytes crus (pedido de 5 592 829 bytes). O AOS-537 já gera MP4
(H.264, `+faststart`) e manda-os assim, pelo proxy do banco. Falta o pedido do Model Gateway do nó
poder levar o vídeo, e só para uma rota que o declare — o mesmo passo que a peça 1 do AOS-530 deu
para as imagens.

### Objectivo

Uma mensagem `user` de um pedido do gateway pode levar uma parte de vídeo MP4, validada pelos bytes
e escrita no wire na forma medida; um pedido com vídeo só sai para uma rota resolvida cujo perfil
declare `video`, e nunca em streaming; os bytes não saem por mais via nenhuma.

### Âmbito

- **A porta 1.13.0 (MINOR).** `Message.Videos []VideoPart` (`json:"-"`), um campo novo ao lado de
  `Message.Images`, e não uma generalização das duas numa lista de «media»: um campo novo não muda
  o wire nem os selos de nenhum pedido de hoje, nem quem já constrói imagens (a `ocr_read`).
  `VideoPart` impressa (`%v`, `%+v`, `%#v`) ou serializada mostra só o tipo, o tamanho e o `sha256`.
- **A validação** (`Normalize`, e de novo em `MarshalWire` para um pedido que não passou por ele),
  com causas de vocabulário fechado (`ErrVideoPart`/`*VideoPartError`): tipo só `video/mp4`; não
  vazio; até **4 MiB crus** (`MaxVideoBytes`, o tamanho medido pelo AOS-534 — subir exige nova
  medida do proxy); a magia (a caixa 1 é `ftyp` com marca `isom`, `iso2`, `mp41`, `mp42` ou `avc1`);
  a estrutura mínima (as caixas de topo lêem-se até ao último byte, há `moov` e `mdat`, e a primeira
  `moov` vem antes da primeira `mdat` — o `+faststart`, para o fornecedor ler em fluxo); só em
  mensagens `user`; e no pedido inteiro o tecto agregado (a soma das partes de vídeo, também 4 MiB),
  **no máximo um vídeo** (o tecto é o de um corpo medido com uma parte, e o custo e a leitura de dois
  vídeos no mesmo pedido ninguém os mediu) e **nunca com imagens** (nenhuma rota foi medida com as
  duas, e recusar é mais simples do que decidir que capacidade um pedido misto exige).
- **O wire OpenAI.** A mensagem com vídeo sai com `content` em partes, o texto primeiro e depois
  `video_url` com a data URL `video/mp4` em base64. Um pedido sem vídeo sai byte-igual ao de hoje.
- **A capacidade `video`** no perfil da rota, aceite por `Validate`; nenhum perfil em código a
  declara e os digests de hoje não mudam. O gateway recusa (`ErrRouteWithoutVideo`/
  `*RouteWithoutVideoError`, causas `perfil_sem_video`, `rota_sem_perfil`, `video_em_streaming`)
  ANTES da credencial, do selo de atribuição, do adaptador e do custo um pedido com vídeo para uma
  rota RESOLVIDA (depois de recuo) sem `video` ou sem perfil, e qualquer vídeo em streaming.
- **As recusas não são falha do modelo** (`ClassifyTurnFailure`): a de capacidade é
  `perfil_do_run`, a de forma é `negado_pelo_gateway` — o disjuntor do candidato não as conta.
- **Os bytes nunca** em spans, selos, eventos, logs ou erros; o erro do provider a um pedido com
  vídeo sobe sem o corpo.
- O adaptador RT→GW dos turnos não muda e não produz vídeo.

### Critérios de Aceitação

- [x] Cada recusa da forma (tipo, vazio, magia, marca, estrutura, `moov` depois da `mdat`, tecto por
      parte e agregado no limite exacto e +1, papel, dois vídeos, vídeo com imagem) tem a sua causa,
      no `Normalize` e no wire chamado sem ele; as formas válidas passam.
      *Nota de 2026-10-19:* `packages/platform/model-gateway/port/aos535_video_test.go`
      (`TestAOS535_Normalize_RecusaCadaForma`, `…_Pedido_UmVideoSemImagensETectoAgregado`,
      `…_Tecto_LimiteExacto`, `…_Normalize_AceitaAsFormasValidas`).
- [x] O wire: sem vídeo, o literal de antes byte a byte; com vídeo, `text` e `video_url` com os bytes
      exactos; um vídeo numa mensagem com estado para sair recusa; as respostas continuam só texto.
      *Nota de 2026-10-19:* `TestAOS535_MarshalWire_*`, `TestAOS535_Resposta_ContinuaSoTexto`.
- [x] A recusa por capacidade (rota sem `video`, só com `imagem`, sem perfil, recuo, streaming) com
      zero chamadas ao adaptador, zero credenciais, zero selos e zero custo; o caminho que passa é
      contabilizado; as recusas classificam-se como não-falha do modelo.
      *Nota de 2026-10-19:* `packages/platform/model-gateway/aos535_video_test.go` e os casos AOS-535
      de `TestAOS521_ClassifyTurnFailure`.
- [x] Pelo adaptador HTTP real contra `httptest`: a data URL chega com os bytes exactos; sem a
      capacidade, ou em streaming, zero pedidos; um 400 que ecoe o vídeo não sobe com o corpo.
      *Nota de 2026-10-19:* `TestAOS535_Gateway_AdaptadorHTTP`.
- [x] Os bytes fora de `%v`/`%+v`/`%#v`/JSON, de spans, de selos e de erros; o adaptador dos turnos
      não produz vídeo (prova dinâmica e pela fonte).
      *Nota de 2026-10-19:* `TestAOS535_BytesSoNoWire`, `…_Gateway_RotaComVideo_Passa`,
      `…_AdaptadorDoRuntime_NuncaProduzVideo`. Cada defesa foi mutada à mão e cada mutação avermelha.

### Fora de âmbito

- A tool de vídeo e o alias `video-visao` (AOS-536), e o registo de entrada de uso `tool` para uma
  rota de vídeo: hoje `policy/routeentry` exige `imagem` a um registo de uso `tool`.
- Subir o tecto de 4 MiB, mais de um vídeo por pedido, vídeo com imagens, outros contentores
  (`webm`, QuickTime): cada um exige medida nova.
- Vídeo em streaming e vídeo em respostas.

---

## AOS-536 — A tool de vídeo: um vídeo da pasta semeada descrito por um modelo capaz de vídeo, pelo Model Gateway do nó

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa nem emenda ADR nenhum: segue a opção A do dono de 2026-10-18 e o molde da `ocr_read` (AOS-530). O ADR-042 é citado só como a origem desse molde. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-08 — Observabilidade e Evals |
| Fase | Arquitectura-alvo da fronteira runtime↔modelo — A6 (multimodal), sub-fase A6.1 «vídeo como tool»; rótulo do desenho «A6-video-tool» |
| Tipo | feature (Model Gateway, banco de ensaio e nó) |
| Prioridade | P2: o último elo da A6.1; nada em produção o pede hoje |
| Estimativa | M (parte 1: S; parte 2: M) |
| Dependências | AOS-530 (o registo de uso `tool`, o `qualificar-ocr` e a `ocr_read`, o molde), AOS-535 (a porta 1.13.0 com `Message.Videos` e a capacidade `video`) e AOS-537 (o banco mede o vídeo, relatório versão 2), feitos. Escolha do dono de 2026-10-19: `google/gemini-3.5-flash-lite` pela OpenRouter para o alias `video-visao`, com a qualificação autorizada |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Plataforma |
| Documentos de referência | `docs/reports/desenho-a5-a6-multimodal-2026-10-14.md` (§5), `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` §4 e §5, `packages/qa/banco-ensaio/README.md` («Preparar o registo de uso `tool` de vídeo»), `deploy/server/README.md` («Perfis de rota assinados») |

### Contexto

A medição do AOS-537 (2026-10-19, bateria 1.1.0, oito vídeos, 34 factos) qualificou o Gemini 3.5
Flash-Lite com 34/34 factos por 0,0048 USD a bateria inteira, e o dono escolheu-o para o alias de
produção `video-visao`. A porta do gateway já leva um vídeo MP4 só para uma rota cujo perfil
declare `video` (AOS-535). Faltam duas coisas, que este ticket entrega em duas partes:

1. **O perfil do alias tem de chegar ao nó por um registo assinado.** Hoje um registo de entrada de
   uso `tool` (`policy/routeentry`, AOS-530) exige a capacidade `imagem` e cita um relatório `ocr`:
   um perfil só com `video` recusa. E o banco não tem um comando que prepare esse registo a partir
   do veredicto da experiência `video`, como o `qualificar-ocr` faz para a `ocr`.
2. **A tool.** Uma tool do nó, mediada pelo Reference Monitor, que lê um vídeo da pasta semeada e o
   manda inteiro ao modelo como parte `video_url`, pelo Model Gateway do nó — o molde da
   `ocr_read`.

### Objectivo

Parte 1: o nó aceita um registo de uso `tool` de vídeo, assinado pelo dono, que cite uma medida de
vídeo qualificada do modo real; e o banco prepara-o, por assinar, a partir da corrida. Parte 2: a
tool `video_read` no nó.

### Âmbito — parte 1 (o registo de uso `tool` de vídeo e o `qualificar-video`)

- **`policy/routeentry`.** Um registo de uso `tool` aceita dois perfis: o de **imagem** de hoje
  (declara `imagem`, não declara `tools` nem `video`) e o de **vídeo** (declara `video`, não declara
  `tools` nem `imagem`). Exactamente uma capacidade de media; senão `perfil_de_tool_com_capacidades_erradas`
  como hoje. A media lê-se do perfil (`Entry.Media`), que está debaixo da assinatura; não há membro
  novo no registo.
- **O relatório citado é o da media.** Um registo de vídeo exige um relatório da experiência
  `video`, da versão 2 do AOS-537 (`ReadVideoVerdict`, leitura fechada com as listas de nomes do
  relatório `video`), do modo `real`, `qualificado`, que vale para produção, para o mesmo perfil e
  a mesma imagem do proxy. Um registo de imagem continua a exigir o relatório `ocr`. Um relatório de
  uma media num registo da outra recusa com `relatorio_de_outra_experiencia`; um `video` da versão 1
  com `relatorio_de_forma_desconhecida`. Um registo de uso `run` decide como antes.
- **Os registos de hoje são byte-iguais**: o mesmo digest, a mesma assinatura e a mesma decisão — o
  de uso `run` (pinado no AOS-530) e o de uso `tool` de imagem (pinado aqui, com o digest conferido
  fora do Go).
- **O nó (`cmd/aos`).** Nada muda no arranque além do que o `routeentry` decide: um registo de uso
  `tool` de vídeo carrega e nunca é candidato (`naoTitulares` já o exclui). A linha do arranque diz
  a experiência citada (a de um registo de imagem fica byte-igual), e a `ocr_read` liga-se só a um
  registo de imagem.
- **O banco: `aos-ensaio qualificar-video`.** O caminho do `qualificar-ocr`, partilhado
  (`qualificar_media.go`): corre a experiência `video` com `--perfil` (capacidade `video`, sem
  `imagem` nem `tools`), confere o `expected_model` contra a rota medida antes de enviar, conta nas
  qualificações do dia, e só com `qualificado` do modo real deixa em `pacote/` o registo
  (`"uso":"tool"`, fatia 0) e o relatório `video`, por assinar; imprime as linhas do `aos-issuer
  route-profile-sign` e do `aos-ensaio conferir`. O `conferir` mostra o uso e a media. O `--perfil`
  da experiência `video` aceita um perfil com `video` (e o relatório deixa de dizer que o perfil
  não a declara quando a declara).

### Âmbito — parte 2 (a tool `video_read` no nó)

- A tool `video_read`, mediada pelo Reference Monitor, no molde da `ocr_read`: um vídeo da pasta
  semeada, por referência, lido pelo nó, conferido como MP4 (as regras do AOS-535) e mandado inteiro
  ao alias `video-visao` como parte `video_url` só desse pedido; o resultado é texto `untrusted`,
  com o digest do vídeo e o modelo servido; o custo grava-se no tecto do run; o replay não chama o
  modelo.
- O arranque fail-closed: o alias na allowlist em vigor, um registo de uso `tool` de **vídeo** para
  ele e, com a contabilidade composta, o preço do par.

### Critérios de Aceitação

- [x] Parte 1: um registo de uso `tool` com um perfil de vídeo carrega (`Entry.Media` = `video`); um
      perfil com `video` e `imagem`, com `video` e `tools`, só com `tools` ou sem capacidades recusa
      com `perfil_de_tool_com_capacidades_erradas`; um relatório `ocr` num registo de vídeo, um
      `video` num registo de imagem e uma qualificação num registo de vídeo recusam com
      `relatorio_de_outra_experiencia`; a forma, a versão, o modo, o veredicto, o perfil e a imagem
      do proxy do relatório `video` recusam cada um com a sua causa; os registos de hoje têm o
      digest e a assinatura pinados.
      *Nota de 2026-10-19:* `packages/platform/model-gateway/policy/routeentry/aos536_video_test.go`.
- [x] Parte 1: no nó, um registo de vídeo carrega com um candidato, com o interruptor `on` e `off`,
      e não é candidato, não tem adaptador de run e não é servido a runs; recusa o arranque a citar um
      relatório `ocr`, com `video` e `imagem`, ou para o alias do titular; a `ocr_read` não se liga a
      ele.
      *Nota de 2026-10-19:* `packages/cmd/aos/aos536_video_tool_no_test.go` e o caso «so um registo
      de uso tool de video para o alias» de `TestAOS530_No_ArranqueFailClosed`.
- [x] Parte 1: o `qualificar-video` com o modelo de vídeo falso no modo real prepara o pacote, que
      o `LoadDir` aceita assinado com uma chave de teste e o `conferir` mostra com `media: video`;
      com o motor falso sai 9 sem pacote; recusado (factos errados, respostas cortadas) sai 7; o
      `expected_model` de outra rota sai 3 sem pedido; um perfil com capacidades erradas sai 2 sem
      pedido; o alias fora da allowlist sai 11 sem pedido; o perfil de produção do README passa com
      a allowlist externa que o autoriza. Os nomes do relatório `video` que o banco escreve são os
      que o nó lê.
      *Nota de 2026-10-19:* `packages/qa/banco-ensaio/aos536_qualificar_video_test.go`. Cada defesa
      nova foi mutada à mão e cada mutação avermelha (a do modo na primeira barreira só com a do
      `vale_para_producao`, que a repete; sozinha, o nó recusa o pacote na terceira).
- [ ] Parte 1: a qualificação real do `video-visao` (pelo dono, com a allowlist externa do nó), o
      pacote assinado e conferido.
- [x] Parte 2: a tool `video_read` no nó, no molde da `ocr_read` e com o código dela partilhado
      (`tool_de_modelo.go`: a ligação, o arranque, a leitura confinada, a reserva e o evento de custo,
      a chamada ao gateway e as suas falhas; os testes do AOS-530 passam sem alteração). Mediada
      pelo RM com `cap:fs.read` e `doc://{doc_id}` — os da `ocr_read`: a mesma leitura por run —; a
      pasta é a da `ocr_read` (`AOS_OCR_SEED_DIR`, sem variável nova: `doc://x` nomeia o mesmo
      ficheiro nas duas tools); só MP4 pela porta (`ftyp`, `moov` antes da `mdat`, ≤ 4 MiB), a
      duração da `mvhd` com tecto de 60 s (`video_longo`), tudo antes do modelo e sem custo; a
      instrução 1.0.0 do AOS-537 copiada com o digest provado contra o código do banco (a fórmula do
      banco); `max_tokens` 8192, prazo 120 s, `Tools` vazio, `OriginTool: "video_read"`; resultado
      `aos-video/v1` `untrusted`; a reserva é função da duração (2 × (segundos × 300 + 512) tokens de
      entrada + 8192 de saída, e o seu custo pela tabela), o mesmo evento `budget.toolcall.model_cost`
      e a resposta não medida a cobrar a reserva dessa chamada; o replay não chama o modelo; o
      arranque fail-closed (`ErrVideoSemPasta`, `ErrVideoAliasForaDaAllowlist`, `ErrVideoSemRegistoTool`
      — também só com um registo de imagem —, `ErrVideoSemPreco`, `ErrVideoSemGateway`); só a
      `ocr_read` e a `video_read` podem ter o bloco `modelo`; métrica `aos_video_read_total`.
      *Nota de 2026-10-19:* `packages/cmd/aos/aos536_video_read_test.go`,
      `aos536_video_read_no_test.go` (nó composto com o gateway real e um provider `httptest`: a
      data URL `video/mp4` com os bytes exactos, o selo com a tool de origem, o titular sem vídeo,
      nenhum byte nem texto em claro nos registos, o custo no run igual ao do gateway, a reserva
      negada pelo tecto para 50 s e não para 2 s, a não medida e a sem resposta, o replay, a
      `ocr_read` intacta ao lado e a leitura única partilhada) e `aos536_video_instrucao_test.go`.
      Catorze mutações à mão (o tecto de duração, o filtro da media no arranque, a reserva por
      duração, a tool de origem, a validação MP4, a duração zero, o tecto de bytes, os tectos da não
      medida, a reescrita, o registo no RM, a composição, as causas do vídeo, a métrica, o gateway
      ligado) avermelham cada uma. **Não** está no `tools.json` de produção: a `video_read` tem
      egress externo e oferecê-la em produção pede a decisão do dono de estender a excepção do
      ADR-034 §2.7 (por decidir; `deploy/server/README.md`, «Ligar o vídeo»).

### Fora de âmbito

- Alterar a allowlist embebida ou o `tools.json`: o alias `video-visao` autoriza-se pela allowlist
  externa assinada do nó.
- Um registo de uso `tool` com mais de uma capacidade de media, ou de uso `run` com vídeo.
- Vídeos reais, outros contentores além do MP4, e subir o tecto de 4 MiB do AOS-535.

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
| 1.14 | 2026-10-11 | +AOS-523 (fase A3): a medição do critério da fase — o terceiro modelo (outro modelo do Kimi, pela mesma conta) entra em produção em menos de uma hora, com zero PRs e uma assinatura; um modelo mau é recusado pelo arnês e pelo nó, em ensaio; um candidato que degrada perde a fatia, em ensaio (D9) | Equipa AOS |
| 1.15 | 2026-10-11 | AOS-522 implementado em ensaio: `aos-ensaio qualificar` e `conferir`, o resumo de uma página e o runbook com relógio; o ensaio a seco com Docker e nó local e a corrida real ficam por fazer | Equipa AOS |
| 1.16 | 2026-10-11 | AOS-522: correcções da revisão adversarial — quatro barreiras com teste próprio, o resumo a partir do registo escrito, o plano confere a allowlist; e a correcção de um defeito do AOS-518 (uma corrida interrompida podia sair `recusado`) | Equipa AOS |
| 1.17 | 2026-10-12 | AOS-523 medido: o `k3` saiu `qualificado` no exame real, foi assinado pelo dono e serve 10% dos planos na v0.1.55 (série `v0155c`: 7 planos em 40, 14 de 14 runs do candidato cumpridos); a primeira metade do critério da fase fica cumprida em produção, a segunda só com modelos maus simulados — nenhum modelo mau real foi recusado; a fase foi fechada pelo dono no mesmo dia, com os resíduos nomeados. AOS-518 e AOS-522: a qualificação e a corrida reais registadas | Equipa AOS |
| 1.18 | 2026-10-12 | +AOS-525 (fase A4): a bateria que separa modelos — dificuldade crescente por degraus, a regra dos factos sem depender da grafia e as respostas guardadas a pedido. Nasce da medição de seis modelos pela OpenRouter, em que a bateria 1.0.0 não distinguiu modelos baratos de caros | Equipa AOS |
| 1.19 | 2026-10-12 | AOS-525, primeiro ponto: as respostas guardadas a pedido (`aos-ensaio --guardar-respostas`), fora do relatório e do seu digest; a regra dos factos, a bateria 2.0.0 e a medição ficam por fazer | Equipa AOS |
| 1.20 | 2026-10-13 | AOS-525: a hipótese da grafia confirmada com respostas lidas (12 de 12 ausências do DeepSeek eram o acento de «Tomás») e a regra dos factos corrigida — compara sem acentos, sem maiúsculas e com espaços e hífenes dobrados; a grafia de milhares continua a contar. A bateria 2.0.0 e a medição por degraus continuam por fazer | Equipa AOS |
| 1.21 | 2026-10-13 | AOS-525: a bateria 2.0.0 por degraus (sete casos de dificuldade crescente, com factos proibidos) e a experiência `degraus`, que dá as taxas por degrau e não dá veredicto; a 1.0.0 e o seu digest não mudam; cada degrau tem um modelo falso que o cumpre e um que o falha. Com as correcções da revisão adversarial: uma passagem com um erro passageiro sai das taxas do degrau, seis turnos por run (pior caso 180 pedidos por passagem), factos proibidos em todos os degraus e comparados sem hífenes nem espaços. A medição com modelos reais continua por fazer | Equipa AOS |
| 1.22 | 2026-10-13 | AOS-525 medido: a bateria 2.0.0 passada a seis modelos reais (5 passagens cada). Separa o modelo mais barato (falha volume e documento longo por inteiro) dos outros cinco, e não separa os cinco entre si; as falhas do Haiku em D5 e do DeepSeek em D7 são o limite do proibido citado numa negação, não conteúdo errado. Registo na §5 do acompanhamento | Equipa AOS |
| 1.23 | 2026-10-13 | AOS-525: cobertura do módulo medida (86,8% → 88,1%) e o critério dos gates marcado. Fica por marcar só o critério 2, na metade «recusada fora da bateria sintética», que não se aplica (o banco só tem baterias embebidas) e espera pela palavra do dono | Equipa AOS |
| 1.24 | 2026-10-15 | +AOS-528 (depois da A4, resíduo (c)): o conselheiro no banco de ensaio — a bateria 3.0.0 «difícil» (sete casos de dedução, contradições com regra, documento longo e restrições), a tool `aconselhar` com o conselheiro de qualquer fornecedor numa segunda rota do proxy efémero, com contas e tectos do fornecedor dele, e a experiência `conselheiro` em dois braços (com e sem a tool) que dá casos cumpridos, taxa de consulta e custo por caso cumprido, sem veredicto. Implementado e provado com falsos; a medição real fica por fazer, com autorização do dono | Equipa AOS |
| 1.25 | 2026-10-15 | AOS-528 medido em parte: o conselheiro não melhora nada com os modelos de hoje — os bons resolvem os casos difíceis sozinhos e o fraco quase não consulta. Multi-fornecedor provado (executor na OpenRouter, conselheiro na conta Kimi) | Equipa AOS |
| 1.26 | 2026-10-15 | +AOS-529 (fase A5, «A5-ocr-medição»): o banco de ensaio mede um motor de OCR — bateria de imagens sintéticas com e sem acentos (PNG limpo, com ruído e rotação, de meia resolução, JPEG, PDF digitalizado e uma tabela), a experiência `ocr` com o motor plugável (o Tesseract num contentor fixado pelo digest nas condições do gVisor, e um falso), factos pela regra do banco e com acentos exactos, tempo e memória por imagem; sem veredicto. Implementado e provado com o motor falso; a medição real espera por uma imagem do Tesseract | Equipa AOS |
| 1.27 | 2026-10-15 | AOS-529: correcções da revisão adversarial (um falso que erra factos, o zero sem barra, a pasta montada legível pelo uid do contentor, a falha do PDF classificada por estágio e pelo pico, `--pull never`, os limites dizem runc e chamada isolada) e duas decisões do dono: a imagem do Tesseract construída no posto (`ocr/Dockerfile`, aceite pelo ID) e o braço do modelo de visão pelo proxy do banco, com as chaves, os tectos e o custo do fornecedor dele. A medição real fica para depois do PR | Equipa AOS |
| 1.28 | 2026-10-15 | AOS-529 medido: o Tesseract local com 82,5% dos factos (76,2% com acentos), ~0,9 s e custo zero por imagem; o Claude Haiku 5.5 pela OpenRouter com 100% (100%), 2 a 9 s e ~0,0001 USD por imagem; o Gemini 3.8 Flash com 89,2% e ~0,0034 USD, com a resposta cortada (provavelmente o limite de tokens de saída do adaptador, por confirmar). Imagens sintéticas, doze no braço de visão. A medição levou o dono a mudar a D2: OCR por um modelo de visão através de uma tool (ADR-042, proposto; AOS-530 reescrito) | Equipa AOS |
| 1.29 | 2026-10-18 | +AOS-534 (fase A6, sub-fase A6.1, spike): a parte `video_url` com uma data URL de MP4 atravessa o proxy LiteLLM fixado intacta, nas duas rotas da OpenRouter? O falso do estado regista as partes do `content` (tipo, chaves, classe da data URL, sha256 e tamanho, sem bytes) e um cenário opcional com o proxy real mede-o sem custo, com um MP4 sintético pequeno e outro de 3 a 6 MB. AOS-535 e AOS-536 abertos só com o título, a seguir | Equipa AOS |
| 1.30 | 2026-10-19 | +AOS-537 (fase A6, sub-fase A6.1): o banco mede o vídeo — a bateria sintética de oito vídeos com 34 factos (texto, contagens, cores, ordem e uma instrução no fotograma), fotogramas em Go e MP4 pelo ffmpeg fixado pelo digest num contentor sem rede, e a experiência `video` com o motor falso e o modelo pelo proxy do banco, com veredicto contra 90% dos factos. As regras do modo real do braço de visão do OCR passam a `rota_paga.go`, partilhadas. Implementado e provado com falsos; o ffmpeg real e a medição dos três candidatos ficam para o dono | Equipa AOS |
| 1.31 | 2026-10-19 | AOS-537: correcções da revisão independente — os factos de contagem e de cor lêem-se das linhas rotuladas, tolerantes ao markdown e sem contradições (relatório versão 2); a bateria de vídeo 1.1.0 com um par de ordem contra a ordem alfabética; a pasta montada do ffmpeg em 0733 e o MP4 lido só como ficheiro regular; o custo antes do registo no contador | Equipa AOS |
| 1.32 | 2026-10-19 | AOS-536 escrito por inteiro (fase A6, sub-fase A6.1), em duas partes. Parte 1 feita: o registo de uso `tool` aceita um perfil de vídeo (exactamente uma capacidade de media) e cita um relatório `video` versão 2; o banco ganha o `aos-ensaio qualificar-video`. Parte 2 (a tool `video_read` no nó) por fazer | Equipa AOS |
| 1.33 | 2026-10-19 | AOS-536 parte 2 feita no ramo: a tool `video_read` no nó, no molde da `ocr_read` e com o código dela partilhado; a pasta, a capability e o recurso são os da `ocr_read`; tecto de 60 s; reserva função da duração. Fora do `tools.json` de produção até à decisão do dono sobre o ADR-034 §2.7 | Equipa AOS |
