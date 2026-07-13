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

## Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

---

## Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Emissão inicial | Equipa AOS |
