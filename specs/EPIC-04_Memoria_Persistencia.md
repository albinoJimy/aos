# EPIC-04 — Memória e Persistência

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Memória e Persistência |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/04_Memoria_Persistencia.md`, `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md`, `specs/EPIC-08_Observabilidade_Evals.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `tecnica/13_Modelo_Dados_Eventos.md` |

---

## 1. Visão do Epic

Este epic implementa o **Memory Service (MEM)** do AOS: o serviço de plataforma que dá ao agente as quatro classes de memória — **episódica, semântica, procedural e de trabalho** — e que impõe, ao nível arquitectural, a fronteira mais mal compreendida dos frameworks de 2025-2026: **contexto ≠ registo** (Princípio 4). O que se injecta no modelo é uma *projecção* higienizada, cache-estável e económica em tokens; o que se persiste no backend é a trajectória completa para replay, RCA e evals. Descartar da injecção é legítimo; descartar do registo nunca é.

A memória é também a superfície de dois riscos que a fonte identifica como críticos. Primeiro, o **memory poisoning** persistente (ASI06): memória derivada de conteúdo untrusted (tool results, web, schemas MCP) que, sem proveniência nem quarentena, contamina decisões futuras — daí AOS-042. Segundo, a **misevolution/drift**: a memória procedural (skills auto-escritas) é a mudança de maior risco do sistema e não pode chegar a produção sem eval-gate e ratificação (ADR-012), pelo que este epic entrega o versionamento de schema e as migrações **expand/contract** que tornam a evolução reversível (AOS-041, AOS-044).

Por fim, a memória tem de honrar o contrato de cache do Model Gateway: a compressão de contexto é **assíncrona, em checkpoints, fora da hot path** (ADR-009), nunca uma sumarização síncrona que invalida o prefixo (AOS-043). O serviço assenta sobre o **Event Store replicado** (EPIC-01) como fonte de verdade e alimenta a **Observabilidade** (EPIC-08) com a trajectória que o contexto do pai deliberadamente não vê.

**Fora de âmbito.** O Event Store em si (EPIC-01), o loop do runtime e o replay determinístico (EPIC-02), o registry de skills e o eval-gate de auto-modificação enquanto pipeline (EPIC-05 e EPIC-08), o taint tracking control/data-plane enquanto mecanismo primário (EPIC-07). Este epic *consome* essas fundações e aplica-as ao domínio da memória.

---

## 2. Critérios de Saída do Epic

- [ ] O Memory Service expõe as **quatro classes de memória** (episódica, semântica, procedural, de trabalho) com contratos de porta versionados (SemVer), substituíveis sem rearquitectura.
- [ ] A separação **contexto ≠ registo** é imposta em código: existe uma *projecção* de injecção distinta do *registo* persistido, e nenhum caminho descarta do registo o que o audit trail exige.
- [ ] Toda a escrita de memória derivada de conteúdo **untrusted** carrega proveniência e passa por quarentena; memória em quarentena é estruturalmente impedido de autorizar acções privilegiadas (ADR-005).
- [ ] O schema de memória é **versionado** e as migrações seguem o padrão **expand/contract** com rollback, sem downtime e sem perda de dados (ADR-012).
- [ ] A **compressão de contexto** ocorre apenas em checkpoints assíncronos, fora da hot path, sem invalidar o prefixo de cache; o cache-hit-rate não regride (ADR-009).
- [ ] A memória procedural (skills aprendidas) só é promovida a produção após eval-gate e ratificação assinada; existe rollback atómico (ADR-012).
- [ ] A gestão da **janela de contexto** (memória de trabalho) respeita o orçamento em tokens e o layout cache-estável; a projecção nunca muta o prefixo imutável.
- [ ] Existe uma suite de testes de **integridade e migração** verde, incluindo *round-trip* de migração expand/contract e prova de não-perda do registo.
- [ ] Todos os acessos de memória emitem **spans OTel GenAI** e respeitam GDPR por desenho (TTL por classe, redação de PII, crypto-shredding) através da integração com a camada de conformidade.

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-035 | Modelo de memória (episódica, semântica, procedural, de trabalho) | feature | L | P0 | EPIC-01 (Event Store) |
| AOS-036 | Projecção contexto vs registo (Princípio 4) | feature | M | P0 | AOS-035, EPIC-08 |
| AOS-037 | Memória de trabalho e gestão da janela de contexto | feature | M | P1 | AOS-035, AOS-036 |
| AOS-038 | Memória episódica (trajectórias) | feature | M | P1 | AOS-035, EPIC-01, EPIC-08 |
| AOS-039 | Memória semântica (base de conhecimento) | feature | M | P1 | AOS-035, AOS-042 |
| AOS-040 | Memória procedural (skills aprendidas) | feature | L | P1 | AOS-035, AOS-039, EPIC-05 |
| AOS-041 | Versionamento de schema de memória + migrações expand/contract | feature | L | P1 | AOS-035 |
| AOS-042 | Proveniência e quarentena de memória derivada de untrusted | feature | M | P0 | AOS-035, EPIC-07 |
| AOS-043 | Compressão de contexto assíncrona (checkpoints, fora da hot path) | feature | M | P1 | AOS-037, AOS-038 |
| AOS-044 | Testes de integridade e migração de memória | chore | M | P1 | AOS-041, AOS-035, AOS-042 |

---

## AOS-035 — Modelo de memória (episódica, semântica, procedural, de trabalho)

| Campo | Valor |
|---|---|
| Epic | EPIC-04 — Memória e Persistência |
| Fase | Fase 1 — Fronteira de segurança / serviços de plataforma |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | EPIC-01 (Event Store replicado) |
| Bloqueia | AOS-036, AOS-037, AOS-038, AOS-039, AOS-040, AOS-041, AOS-042 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/04_Memoria_Persistencia.md`, `_FONTE_agentic-os-ideal.md` (Dim. 7), ADR-007, ADR-012 |

### Contexto

O AOS trata a memória como um serviço de plataforma de primeira classe (MEM no catálogo canónico), não como um *buffer* no loop. A fonte distingue quatro classes com propósitos e ciclos de vida diferentes: **episódica** (trajectórias de execução passadas), **semântica** (base de conhecimento factual), **procedural** (skills e memória de procedimentos aprendidos) e **de trabalho** (o contexto activo do turno). Sem um modelo de domínio explícito que as separe, o sistema colapsa tudo num só *store* e perde tanto a higiene de contexto como a disciplina de governação que cada classe exige.

Este ticket estabelece a fundação sobre a qual todos os restantes deste epic assentam: as entidades, os contratos de porta e a fronteira com o Event Store.

### Objectivo

Definir e implementar o **modelo de domínio da memória** e a fachada do Memory Service, com as quatro classes como abstracções distintas, cada uma com contrato de porta versionado (SemVer) e substituível sem rearquitectura, ancorado ao Event Store como fonte de verdade.

### Critérios de Aceitação

- [ ] Existe um modelo de domínio com quatro entidades/classes distintas — `episodic`, `semantic`, `procedural`, `working` — cada uma com identidade, esquema tipado e ciclo de vida documentado.
- [ ] O Memory Service expõe uma **porta versionada** (interface `MemoryPort`) com operações CRUD/query por classe, sem vazar o backend de armazenamento para o chamador.
- [ ] Cada registo de memória carrega metadados obrigatórios: `agent_id`, `run_id`, `provenance` (trusted/untrusted — ver AOS-042), `created_at`, `ttl_class` e `schema_version`.
- [ ] O serviço lê/escreve a fonte de verdade a partir do **Event Store replicado** (ADR-007); nenhuma classe depende de um *single-writer* (SQLite single-writer é proibido como fonte primária).
- [ ] Um *swap* de backend de memória é possível por configuração, sem alterar chamadores — provado por um segundo adaptador de teste (in-memory) que passa o mesmo contrato.
- [ ] A documentação em `tecnica/04` descreve as quatro classes, com pelo menos dois diagramas Mermaid.

### Detalhes Técnicos

- **Componentes:** MEM (novo), ES (consumido).
- **Ficheiros sugeridos:** `services/memory/domain/` (entidades por classe), `services/memory/ports/memory_port.*` (interface versionada), `services/memory/adapters/eventstore_adapter.*`, `services/memory/adapters/inmemory_adapter.*`.
- Contrato de porta com SemVer; alterações de contrato seguem AOS-041.
- Metadados de proveniência preparam o terreno para AOS-042; TTL por classe prepara a conformidade GDPR (ADR-011).

### Testes Requeridos

- Testes de contrato executados contra dois adaptadores (Event Store e in-memory) — ambos verdes com a mesma suite.
- Teste de que todo o registo escrito tem os metadados obrigatórios preenchidos (falha-fecha se `provenance` ou `schema_version` em falta).
- Teste de que a leitura reconstrói a partir do Event Store append-only.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Porta `MemoryPort` versionada em SemVer; dois adaptadores passam o contrato.
- [ ] Spans OTel emitidos em cada operação de porta (`gen_ai.*` quando aplicável).
- [ ] Sem segredos; sem *single-writer* como fonte de verdade.
- [ ] Documentação `tecnica/04` actualizada com diagramas.
- [ ] Revisão por dois revisores (artefacto P0).

### Handoff para Claude Code

```
Implementa o modelo de domínio do Memory Service (MEM) do AOS com quatro classes
distintas: episódica, semântica, procedural e de trabalho. Cria uma porta
versionada MemoryPort (SemVer) com operações CRUD/query por classe. Cada registo
tem metadados obrigatórios: agent_id, run_id, provenance (trusted|untrusted),
created_at, ttl_class, schema_version. A fonte de verdade é o Event Store
replicado (ADR-007) — nada de single-writer. Fornece dois adaptadores
(eventstore, in-memory) que passam a MESMA suite de contrato. Emite spans OTel em
cada operação. Não expandas escopo para as classes específicas (isso são os
tickets AOS-037..040). Ler tecnica/04_Memoria_Persistencia.md e specs/01 (DoR/DoD).
```

---

## AOS-036 — Projecção contexto vs registo (Princípio 4)

| Campo | Valor |
|---|---|
| Epic | EPIC-04 — Memória e Persistência |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-035, EPIC-08 (Observabilidade) |
| Bloqueia | AOS-037, AOS-043 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/04_Memoria_Persistencia.md`, `specs/EPIC-08_Observabilidade_Evals.md`, `_FONTE_agentic-os-ideal.md` (Princípio 4, Dim. 4), ADR-010 |

### Contexto

A maior contradição dos frameworks que a fonte critica é "avaliamos trajectórias, não saídas" **contra** "o filho só devolve o resumo ao pai". A resolução do AOS é o Princípio 4: **contexto ≠ registo**. O que se injecta no modelo (higiene, cache, economia de tokens) é uma *projecção* distinta do que se persiste no backend (trajectória completa, replay, RCA). Descartar da injecção é legítimo; descartar do audit trail nunca é. Sem uma barreira explícita em código, qualquer optimização de tokens acaba por corroer o registo — e o auditor recebe "o pool" como resposta.

### Objectivo

Implementar a **separação física entre projecção de contexto e registo persistido**: uma função de projecção que produz o que o modelo vê (resumos, janela higienizada) e um caminho de registo independente que persiste sempre a trajectória completa no backend, garantindo que nenhuma operação de higiene toca no registo.

### Critérios de Aceitação

- [ ] Existe uma função de projecção `project_context(record) -> injected_view` separada da escrita de registo `persist(record) -> event`; as duas nunca partilham o caminho de descarte.
- [ ] O sub-agente entrega ao contexto do pai apenas um **resumo** (alvo 1–2k tokens, configurável), enquanto a **árvore de spans completa** vai sempre para o backend de observabilidade (EPIC-08).
- [ ] É arquitecturalmente impossível "descartar do registo" a partir da camada de projecção — provado por teste que tenta e falha (a API de projecção não tem acesso de escrita/apagamento ao registo).
- [ ] Cada turno grava o **hash do prompt materializado**, model-id/params e versões, alimentando o replay fiel (ADR-010), independentemente do que foi higienizado no contexto.
- [ ] A projecção é determinística e reproduzível a partir do registo (a mesma trajectória produz a mesma injecção dada a mesma política de projecção versionada).

### Detalhes Técnicos

- **Componentes:** MEM, OBS (EPIC-08), RT (consumidor).
- **Ficheiros sugeridos:** `services/memory/projection/context_projection.*`, `services/memory/record/trajectory_record.*`.
- Política de projecção versionada (SemVer) para reprodutibilidade; resumo do sub-agente ligado ao trace por `trace_id`.
- Segregação de permissões: o módulo de projecção recebe uma vista *read-only* do registo.

### Testes Requeridos

- Teste de barreira: a projecção não consegue apagar/mutar o registo (falha esperada / erro de tipo).
- Teste de que o resumo ao pai é limitado em tokens e o backend recebe a trajectória completa (contagem de spans no backend > vista injectada).
- Teste de reprodutibilidade da projecção a partir do registo.

### Definition of Done

- [ ] Critérios satisfeitos; barreira contexto/registo provada por teste.
- [ ] Hash do prompt materializado gravado por turno (ADR-010).
- [ ] Spans OTel completos no backend; resumo ligado ao trace.
- [ ] Política de projecção versionada.
- [ ] Documentação e diagrama de fluxo (contexto vs backend) em `tecnica/04`.
- [ ] Revisão por dois revisores (artefacto P0).

### Handoff para Claude Code

```
Implementa o Princípio 4 (contexto ≠ registo) no Memory Service. Separa DUAS vias:
(1) project_context(record) -> vista injectada no modelo, higienizada e limitada em
tokens (resumo do sub-agente ~1-2k tokens); (2) persist(record) -> trajectória
COMPLETA no Event Store / backend de observabilidade. Torna arquitecturalmente
impossível apagar do registo a partir da camada de projecção (a projecção só tem
vista read-only do registo) e prova-o com um teste que tenta e falha. Grava por
turno o hash do prompt materializado + model-id/params + versões (ADR-010). A
política de projecção é versionada (SemVer). Liga o resumo ao trace_id do backend
(EPIC-08). Ler tecnica/04 e specs/EPIC-08.
```

---

## AOS-037 — Memória de trabalho e gestão da janela de contexto

| Campo | Valor |
|---|---|
| Epic | EPIC-04 — Memória e Persistência |
| Fase | Fase 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-035, AOS-036 |
| Bloqueia | AOS-043 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/04_Memoria_Persistencia.md`, `specs/EPIC-06_Model_Gateway_Custos.md`, `_FONTE_agentic-os-ideal.md` (Dim. 3), ADR-009, ADR-008 |

### Contexto

A memória de trabalho é o contexto activo do turno — a janela que o modelo efectivamente vê. É aqui que o contrato de cache do ADR-009 vive ou morre: **prefixo imutável** (system + tool set congelado no run) + **tail append-only** (memory_context, timestamps, resultados). A fonte é explícita: o plano-base reivindicava 85–95% de poupança de prefix caching e simultaneamente adoptava práticas que a destroem (prompt remontado, compressão na hot path, tools dinâmicas). A memória de trabalho tem de crescer só pelo tail, nunca mutar o prefixo, e respeitar o orçamento em tokens.

### Objectivo

Implementar a **gestão da janela de contexto** (memória de trabalho) segundo o layout cache-estável: montagem com prefixo imutável e tail append-only, contabilidade de tokens da janela, e políticas de eviction/prioridade que preparam a compressão assíncrona (AOS-043) sem tocar no prefixo.

### Critérios de Aceitação

- [ ] A memória de trabalho monta a janela com **prefixo byte-idêntico** ao longo do run e **tail append-only**; um teste prova que o prefixo nunca muta nem reordena entre turnos do mesmo run.
- [ ] A ocupação da janela é contabilizada em **tokens** (não em nº de mensagens) e exposta como métrica; existe alerta quando aproxima o limite do modelo.
- [ ] Ao aproximar-se do limite (alvo ~80%), a memória de trabalho emite um **sinal de exaustão graciosa** (marcar para compressão em checkpoint — AOS-043 — ou escalar ao runtime), nunca um hard-stop cego.
- [ ] Novas tools/entradas que quebrariam o prefixo só entram em **runs novos**, coerente com o pinning de supply-chain (ADR-009).
- [ ] A eviction do tail preserva o registo (o que sai da janela permanece no backend — Princípio 4, AOS-036).
- [ ] O cache-hit-rate da janela é exposto como **SLI** e não regride nos testes de referência.

### Detalhes Técnicos

- **Componentes:** MEM (working), RT (consumidor), GW (cache-hit-rate SLI — EPIC-06).
- **Ficheiros sugeridos:** `services/memory/working/window_manager.*`, `services/memory/working/token_accounting.*`.
- Zonas do prompt conforme tabela do ADR-009: prefixo imutável, tail append-only, compressão (delegada a AOS-043).
- Sinal de ~80% coerente com o burn-down de custo (ADR-008) e o prompt de exaustão graciosa da Dim. 6.

### Testes Requeridos

- Teste de imutabilidade do prefixo: hash do prefixo constante ao longo dos turnos de um run.
- Teste de contabilidade de tokens vs limite do modelo e disparo do sinal a ~80%.
- Teste de que a eviction do tail não apaga o registo no backend.
- Teste de SLI de cache-hit-rate acima do alvo (>80%) num cenário de referência.

### Definition of Done

- [ ] Critérios satisfeitos; prefixo imutável provado por hash.
- [ ] Métrica de ocupação em tokens e SLI de cache-hit-rate expostos.
- [ ] Sinal de exaustão graciosa integrado com o runtime (sem hard-stop cego).
- [ ] Eviction preserva o registo (cruza com AOS-036).
- [ ] Spans OTel com `gen_ai.usage.*`.
- [ ] Documentação em `tecnica/04`.

### Handoff para Claude Code

```
Implementa a memória de trabalho e a gestão da janela de contexto do MEM segundo o
ADR-009: prefixo imutável byte-idêntico (system + tool set congelado no run) + tail
append-only. Contabiliza a ocupação em TOKENS e expõe métrica + SLI de
cache-hit-rate. A ~80% emite sinal de exaustão graciosa (marca para compressão em
checkpoint / escala ao runtime), NUNCA hard-stop cego. Novas tools só entram em
runs novos. A eviction do tail nunca apaga o registo no backend (Princípio 4,
AOS-036). Prova a imutabilidade do prefixo com um teste de hash entre turnos. NÃO
implementes aqui a compressão em si — isso é AOS-043. Ler tecnica/04 e a tabela de
zonas do ADR-009.
```

---

## AOS-038 — Memória episódica (trajectórias)

| Campo | Valor |
|---|---|
| Epic | EPIC-04 — Memória e Persistência |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-035, EPIC-01 (Event Store), EPIC-08 (Observabilidade) |
| Bloqueia | AOS-043 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/04_Memoria_Persistencia.md`, `specs/EPIC-08_Observabilidade_Evals.md`, `_FONTE_agentic-os-ideal.md` (Dim. 4, Princípio 4), ADR-010, ADR-007 |

### Contexto

A memória episódica guarda **trajectórias de execução passadas** — o que o agente fez, em que sequência, com que resultados. É a base do replay fiel, da RCA e do eval-driven development. Coincide com o registo do Princípio 4 (AOS-036): a trajectória completa como árvore de spans OTel GenAI persistida no backend, distinta do resumo injectado no contexto. Diferencia-se da observabilidade "efémera" por ser recuperável e consultável como memória do agente — o agente pode recordar episódios anteriores para informar decisões, sem que isso obrigue a reinjectar toda a trajectória.

### Objectivo

Implementar a classe de **memória episódica**: persistência append-only das trajectórias como árvores de spans, indexação e recuperação de episódios relevantes, e integração com o replay (EPIC-02) e a observabilidade (EPIC-08), preservando a distinção contexto/registo.

### Critérios de Aceitação

- [ ] Cada trajectória é persistida como **árvore de spans** (OTel GenAI semconv) no backend, ligada por `run_id`/`trace_id`, de forma append-only (ADR-007/ADR-010).
- [ ] O agente pode **recuperar episódios** relevantes por consulta (por objectivo, por tags, por similaridade) sem reinjectar a trajectória completa — a recuperação devolve uma projecção resumida (AOS-036).
- [ ] Um episódio recuperado é suficiente para **replay determinístico resume-from-step** em conjunto com o Event Store (não substitui, complementa a indexação).
- [ ] A memória episódica respeita TTL por classe e crypto-shredding para conformidade (ADR-011) — episódios expiram/apagam-se por política sem quebrar a cadeia de hash.
- [ ] A escrita episódica não bloqueia a hot path do loop (assíncrona / fora do turno crítico).

### Detalhes Técnicos

- **Componentes:** MEM (episodic), ES, OBS.
- **Ficheiros sugeridos:** `services/memory/episodic/trajectory_store.*`, `services/memory/episodic/retrieval.*`.
- Reutiliza a projecção de AOS-036 na recuperação; índice por tags/embedding (embedding opcional *(proposta)*).
- TTL e crypto-shredding via camada de conformidade (EPIC-09) — apagar a chave por titular preserva a integridade do log.

### Testes Requeridos

- Teste de persistência append-only e ligação por `trace_id`.
- Teste de recuperação de episódio por objectivo/tags devolvendo projecção resumida (não a trajectória crua).
- Teste de replay a partir de episódio + Event Store (resume-from-step).
- Teste de crypto-shredding: episódio torna-se irrecuperável após apagamento de chave, log encadeado intacto.

### Definition of Done

- [ ] Critérios satisfeitos; trajectórias como árvore de spans OTel.
- [ ] Recuperação devolve projecção (Princípio 4 respeitado).
- [ ] Replay determinístico testado (cruza com EPIC-02).
- [ ] TTL/crypto-shredding integrados (ADR-011).
- [ ] Escrita fora da hot path.
- [ ] Documentação em `tecnica/04` com diagrama.

### Handoff para Claude Code

```
Implementa a memória episódica (trajectórias) do MEM. Persiste cada trajectória
como árvore de spans OTel GenAI, append-only, ligada por run_id/trace_id (ADR-010,
ADR-007), fora da hot path. Fornece recuperação de episódios por objectivo/tags
(embedding opcional) que devolve uma PROJECÇÃO resumida — nunca a trajectória crua
reinjectada (Princípio 4, reutiliza AOS-036). Garante que episódio + Event Store
permitem replay resume-from-step (EPIC-02). Integra TTL por classe e crypto-shredding
(ADR-011): apagar a chave por titular torna o episódio irrecuperável sem partir a
cadeia de hash — prova com teste. Ler tecnica/04 e specs/EPIC-08.
```

---

## AOS-039 — Memória semântica (base de conhecimento)

| Campo | Valor |
|---|---|
| Epic | EPIC-04 — Memória e Persistência |
| Fase | Fase 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-035, AOS-042 (proveniência/quarentena) |
| Bloqueia | AOS-040 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/04_Memoria_Persistencia.md`, `specs/EPIC-07_Seguranca_Isolamento.md`, `_FONTE_agentic-os-ideal.md` (Dim. 2, Princípio 5), ADR-005 |

### Contexto

A memória semântica é a **base de conhecimento factual** do agente — factos, entidades, relações consultáveis. É também a superfície clássica do **memory poisoning** (ASI06): conhecimento derivado de web/tool results/schemas MCP que, se tratado como verdade sem proveniência, contamina permanentemente as decisões. Por isso esta classe depende directamente de AOS-042 (proveniência e quarentena) — conhecimento derivado de fontes untrusted entra em quarentena e é dados, nunca instruções (ADR-005, Princípio 5).

### Objectivo

Implementar a classe de **memória semântica**: armazenamento e consulta de conhecimento factual com proveniência obrigatória, curadoria/promoção de conhecimento em quarentena para confiável, e recuperação que respeita o taint (o que é untrusted nunca autoriza acções).

### Critérios de Aceitação

- [ ] Cada facto/entidade na base de conhecimento carrega **proveniência** (fonte, trusted/untrusted, `run_id` de origem) obrigatória — escrita sem proveniência é rejeitada (fail-closed).
- [ ] Conhecimento derivado de fontes **untrusted** entra em **quarentena** (AOS-042) e é servido como dados marcados por taint, incapaz de autorizar acções privilegiadas.
- [ ] Existe um caminho de **curadoria/promoção** de quarentena para confiável que requer validação explícita (humana ou por política), registada no audit trail.
- [ ] A consulta suporta recuperação por chave/tags/similaridade e devolve sempre a etiqueta de proveniência com o resultado.
- [ ] A base de conhecimento respeita TTL, redação de PII e crypto-shredding (ADR-011).

### Detalhes Técnicos

- **Componentes:** MEM (semantic), integra taint de EPIC-07.
- **Ficheiros sugeridos:** `services/memory/semantic/knowledge_base.*`, `services/memory/semantic/curation.*`.
- Proveniência reutiliza o metadado de AOS-035 e o mecanismo de AOS-042.
- Recuperação preserva taint até ao consumidor (o planeador só opera sobre confiável).

### Testes Requeridos

- Teste de rejeição de escrita sem proveniência.
- Teste de que conhecimento untrusted recuperado não consegue autorizar uma tool call privilegiada (fica em quarentena/dados).
- Teste de promoção curada com registo no audit trail.
- Teste de crypto-shredding e TTL sobre factos com PII.

### Definition of Done

- [ ] Critérios satisfeitos; proveniência obrigatória e fail-closed.
- [ ] Quarentena de untrusted integrada com AOS-042.
- [ ] Curadoria auditável.
- [ ] Consulta devolve proveniência; taint preservado.
- [ ] Conformidade (TTL/PII/crypto-shredding).
- [ ] Documentação em `tecnica/04`.

### Handoff para Claude Code

```
Implementa a memória semântica (base de conhecimento) do MEM. Todo o facto tem
proveniência obrigatória (fonte, trusted|untrusted, run_id) — escrita sem
proveniência é rejeitada (fail-closed). Conhecimento derivado de untrusted entra em
quarentena (usa AOS-042) e é servido como dados com taint, incapaz de autorizar
acções (ADR-005, Princípio 5). Fornece um caminho de curadoria/promoção
quarentena->confiável com validação explícita registada no audit trail. A consulta
devolve sempre a etiqueta de proveniência. Integra TTL/redação PII/crypto-shredding
(ADR-011). Prova com teste que conhecimento untrusted não consegue autorizar uma
tool call. Ler tecnica/04 e specs/EPIC-07.
```

---

## AOS-040 — Memória procedural (skills aprendidas)

| Campo | Valor |
|---|---|
| Epic | EPIC-04 — Memória e Persistência |
| Fase | Fase 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-035, AOS-039, EPIC-05 (Registry/eval-gate) |
| Bloqueia | AOS-044 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/04_Memoria_Persistencia.md`, `specs/EPIC-05_Registry_Supply_Chain.md`, `specs/EPIC-08_Observabilidade_Evals.md`, `_FONTE_agentic-os-ideal.md` (Dim. 7, Princípio 7), ADR-012 |

### Contexto

A memória procedural guarda **skills e procedimentos aprendidos** — e é, nas palavras da fonte, "a mudança de maior risco do sistema". A auto-modificação (memória procedural, skills auto-escritas) sofre de *misevolution*/drift mesmo sem atacante. Por isso nunca chega a produção unilateralmente: passa por **staging → eval-gate (golden-set + trace-diffing) → canary → ratificação humana assinada → produção**, com SemVer e rollback atómico (ADR-012, Princípio 7). Este ticket integra a classe procedural com o pipeline de auto-modificação já definido em EPIC-05/EPIC-08.

### Objectivo

Implementar a classe de **memória procedural** como artefacto comportamental versionado, ligado ao pipeline de promoção estagiada: uma skill aprendida entra em staging, é submetida ao eval-gate e canary, e só é activada em produção após ratificação assinada, com rollback atómico disponível.

### Critérios de Aceitação

- [ ] Uma skill/procedimento aprendido é persistido como **artefacto versionado (SemVer)** com manifesto (autor-agente, `run_id` de origem, hash).
- [ ] A activação em produção é **bloqueada** até: passar eval-gate (golden-set + trace-diffing vs baseline), passar canary (success-rate e unsafe-action rate) e ter **ratificação humana assinada** (ADR-012).
- [ ] Existe **rollback atómico automático** em regressão detectada, com retorno à versão anterior sem downtime.
- [ ] Skills em staging **nunca** são executáveis em produção (allowlist fail-closed) — provado por teste.
- [ ] Toda a transição de estado (staging/canary/prod/rollback) é registada no audit trail assinado.
- [ ] A memória procedural integra o Skill/Tool Registry (EPIC-05) para pin+hash+assinatura.

### Detalhes Técnicos

- **Componentes:** MEM (procedural), REG (EPIC-05), OBS/eval (EPIC-08), GOV.
- **Ficheiros sugeridos:** `services/memory/procedural/skill_memory.*`, `services/memory/procedural/promotion_hooks.*`.
- Reutiliza o pipeline de auto-modificação (não o reimplementa): este ticket liga a classe procedural aos hooks de staging/eval/canary/ratificação.
- Estados coerentes com o fluxo Mermaid da Dim. 7 da fonte.

### Testes Requeridos

- Teste de bloqueio: skill em staging não executável em prod (fail-closed).
- Teste de gate: sem eval-gate verde + ratificação assinada, a activação é recusada.
- Teste de rollback atómico em regressão simulada.
- Teste de registo assinado de cada transição.

### Definition of Done

- [ ] Critérios satisfeitos; nenhuma skill em prod sem eval-gate + ratificação.
- [ ] SemVer e manifesto por skill; integra Registry (EPIC-05).
- [ ] Rollback atómico testado.
- [ ] Transições no audit trail assinado.
- [ ] Spans OTel com `gen_ai.evaluation.result` ligados ao trace.
- [ ] Revisão por dois revisores (segurança/governação no circuito).

### Handoff para Claude Code

```
Implementa a memória procedural (skills aprendidas) do MEM como artefacto
comportamental versionado (SemVer) com manifesto (agente-autor, run_id, hash).
Liga-a ao pipeline de auto-modificação existente (EPIC-05/EPIC-08): staging ->
eval-gate (golden-set + trace-diffing) -> canary -> ratificação humana assinada ->
prod, com rollback atómico automático (ADR-012, Princípio 7). NÃO reimplementes o
pipeline — usa os hooks. Garante fail-closed: skill em staging nunca executa em prod
(prova com teste). Regista cada transição no audit trail assinado. Integra o Registry
para pin+hash+assinatura. Ler tecnica/04, specs/EPIC-05 e specs/EPIC-08.
```

---

## AOS-041 — Versionamento de schema de memória + migrações expand/contract

| Campo | Valor |
|---|---|
| Epic | EPIC-04 — Memória e Persistência |
| Fase | Fase 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-035 |
| Bloqueia | AOS-044 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/04_Memoria_Persistencia.md`, `tecnica/11_Convencoes_Engenharia_Evolucao.md`, `_FONTE_agentic-os-ideal.md` (Dim. 7), ADR-012 |

### Contexto

A fonte exige "schema de memória versionado com migrações expand/contract" como requisito inegociável da manutenção evolutiva (Dim. 7). A memória ganha a mesma disciplina de contrato de porta do Model Gateway — coerência por contrato, não por vendor único. O padrão **expand/contract** (também conhecido como *parallel change*) permite migrar sem downtime e com rollback: primeiro **expandir** (adicionar o novo schema, escrever em ambos), depois migrar leituras, e só depois **contrair** (remover o antigo), com cada fase reversível.

### Objectivo

Implementar o **versionamento de schema de memória (SemVer)** e um motor de migrações **expand/contract** com rollback, garantindo compatibilidade retroactiva durante a transição e nenhuma perda de dados persistidos.

### Critérios de Aceitação

- [ ] Todo o schema de memória (por classe) tem **versão SemVer**; cada registo carrega o seu `schema_version` (cruza com AOS-035).
- [ ] O motor de migração suporta as fases **expand → migrate → contract** explícitas, cada uma aplicável e reversível de forma independente.
- [ ] Durante a fase expand, escritas e leituras funcionam sobre **ambos** os schemas (dual-write/dual-read) sem downtime — provado por teste de compatibilidade.
- [ ] Uma migração falhada faz **rollback** para o estado anterior sem perda nem corrupção de dados.
- [ ] Mudanças de schema **MAJOR** (quebra de contrato) exigem eval-gate/aprovação conforme convenções de engenharia (ADR-012, `tecnica/11`).
- [ ] Existe um registo de migrações versionado e idempotente (reaplicar uma migração já aplicada é no-op).

### Detalhes Técnicos

- **Componentes:** MEM, integra convenções de `tecnica/11`.
- **Ficheiros sugeridos:** `services/memory/schema/versions/`, `services/memory/migrations/expand_contract_runner.*`, `services/memory/migrations/registry.*`.
- Migrações idempotentes com idempotency key (coerente com ADR-001); dual-write durante expand.
- Compatibilidade: leituras degradam graciosamente para a versão suportada.

### Testes Requeridos

- Teste de *round-trip* expand → migrate → contract sem perda de dados.
- Teste de dual-write/dual-read na fase expand (ambos os schemas legíveis).
- Teste de rollback de migração falhada (estado idêntico ao inicial).
- Teste de idempotência da migração (reaplicar = no-op).

### Definition of Done

- [ ] Critérios satisfeitos; expand/contract reversível e sem downtime.
- [ ] SemVer por schema; `schema_version` em cada registo.
- [ ] MAJOR passa por gate (ADR-012).
- [ ] Migrações idempotentes e registadas.
- [ ] Documentação da estratégia em `tecnica/04` e `tecnica/11`.
- [ ] Revisão por dois revisores.

### Handoff para Claude Code

```
Implementa o versionamento de schema de memória (SemVer por classe) e um motor de
migrações expand/contract com rollback (ADR-012). Fases explícitas e reversíveis:
expand (adiciona novo schema, dual-write/dual-read) -> migrate (converte leituras)
-> contract (remove antigo). Sem downtime; migração falhada faz rollback sem perda
de dados. Cada registo carrega schema_version (cruza AOS-035). Migrações
idempotentes (reaplicar = no-op, idempotency key). Mudanças MAJOR exigem
eval-gate/aprovação (tecnica/11). Prova com testes: round-trip completo,
dual-read/write no expand, rollback, idempotência. Ler tecnica/04 e tecnica/11.
```

---

## AOS-042 — Proveniência e quarentena de memória derivada de untrusted

| Campo | Valor |
|---|---|
| Epic | EPIC-04 — Memória e Persistência |
| Fase | Fase 1 — Fronteira de segurança |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-035, EPIC-07 (taint control/data-plane) |
| Bloqueia | AOS-039, AOS-044 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/04_Memoria_Persistencia.md`, `tecnica/07_Seguranca_Isolamento.md`, `specs/EPIC-07_Seguranca_Isolamento.md`, `_FONTE_agentic-os-ideal.md` (Dim. 2, Princípio 5), ADR-005 |

### Contexto

A fonte identifica o **memory poisoning** persistente (ASI06) como risco de primeira ordem: "memória derivada de conteúdo untrusted é marcada com proveniência e posta em quarentena". O vector nº1 (OWASP LLM01/ASI01) não se combate com tags in-band — que não são separação de privilégio — mas com **taint tracking real**: conteúdo untrusted (tool results, web, memória, descrições MCP) é dados, nunca instruções (ADR-005, Princípio 5), e é estruturalmente impedido de autorizar acções privilegiadas. Este ticket aplica esse princípio ao domínio da memória e é pré-requisito da memória semântica (AOS-039).

### Objectivo

Implementar **proveniência e quarentena** para toda a memória derivada de fontes untrusted: marcação de taint na ingestão, isolamento em quarentena (dados, não instruções), e uma barreira que impede que memória em quarentena autorize acções privilegiadas ou entre no plano de controlo.

### Critérios de Aceitação

- [ ] Toda a escrita de memória regista **proveniência** (fonte, classificação trusted/untrusted, `run_id`) — obrigatória e imutável.
- [ ] Memória derivada de tool results, web, schemas MCP ou outra memória untrusted é automaticamente marcada **untrusted** e colocada em **quarentena**.
- [ ] Memória em quarentena é **estruturalmente impedido** de autorizar acções privilegiadas — servida ao modelo como dados taint-marcados, nunca no caminho do planeador/control-plane (ADR-005). Provado por teste.
- [ ] A promoção de untrusted → trusted requer validação explícita (política ou humano), registada no audit trail (cruza com curadoria de AOS-039).
- [ ] O taint é **propagado transitivamente**: memória derivada de memória untrusted herda untrusted.
- [ ] Integra o mecanismo de taint control/data-plane de EPIC-07 (não o reimplementa).

### Detalhes Técnicos

- **Componentes:** MEM, integra taint de EPIC-07 (SBX/dual-LLM/CaMeL).
- **Ficheiros sugeridos:** `services/memory/provenance/taint.*`, `services/memory/provenance/quarantine.*`.
- Proveniência assente no metadado obrigatório de AOS-035; propagação transitiva do taint na derivação.
- Barreira control/data-plane: o planeador só lê memória trusted.

### Testes Requeridos

- Teste de marcação automática de untrusted na ingestão a partir de tool result/web/MCP.
- Teste de barreira: memória em quarentena não autoriza tool call privilegiada (falha esperada).
- Teste de propagação transitiva do taint.
- Teste de promoção auditável untrusted → trusted.

### Definition of Done

- [ ] Critérios satisfeitos; quarentena incapaz de autorizar acções (provado).
- [ ] Proveniência obrigatória e imutável em toda a escrita.
- [ ] Taint propagado transitivamente; integra EPIC-07.
- [ ] Promoção auditável.
- [ ] Spans OTel; sem segredos.
- [ ] Revisão por dois revisores (segurança no circuito, artefacto P0).

### Handoff para Claude Code

```
Implementa proveniência e quarentena de memória derivada de untrusted no MEM,
mitigando memory poisoning (ASI06) / OWASP LLM01 (ADR-005, Princípio 5). Toda a
escrita regista proveniência imutável (fonte, trusted|untrusted, run_id). Memória
derivada de tool results/web/schemas MCP/memória untrusted é marcada untrusted e
posta em quarentena — servida como DADOS taint-marcados, estruturalmente impedido de
autorizar acções privilegiadas (o planeador só lê trusted). Propaga o taint
transitivamente. Promoção untrusted->trusted requer validação explícita auditável.
Integra o taint control/data-plane de EPIC-07 (não reimplementes). Prova com testes:
marcação automática, barreira de quarentena, propagação transitiva. Ler tecnica/04,
tecnica/07 e specs/EPIC-07.
```

---

## AOS-043 — Compressão de contexto assíncrona (checkpoints, fora da hot path)

| Campo | Valor |
|---|---|
| Epic | EPIC-04 — Memória e Persistência |
| Fase | Fase 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-037, AOS-038 |
| Bloqueia | AOS-044 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/04_Memoria_Persistencia.md`, `specs/EPIC-06_Model_Gateway_Custos.md`, `_FONTE_agentic-os-ideal.md` (Dim. 3, Princípio 4), ADR-009 |

### Contexto

A fonte é categórica: a compressão de contexto é uma das práticas que **destroem o prefix caching** se feita na hot path. A tabela de zonas do ADR-009 fixa a regra: a compressão (sumarização auxiliar) ocorre **só em checkpoints assíncronos, fora da hot path**. Comprimir de forma síncrona no turno reordena/muta o prompt, invalida o cache e degrada custo e latência silenciosamente (risco "cache thrash invisível"). Este ticket implementa a compressão como processo de fundo que produz sumários auxiliares sem tocar no prefixo nem no registo.

### Objectivo

Implementar a **compressão de contexto assíncrona**: um processo em checkpoints, fora da hot path do loop, que sumariza a memória de trabalho/episódica para caber na janela, preservando o prefixo cache-estável e o registo completo (Princípio 4).

### Critérios de Aceitação

- [ ] A compressão executa **apenas em checkpoints assíncronos**, nunca no caminho crítico do turno — provado por teste que verifica ausência de compressão síncrona na hot path.
- [ ] A compressão **nunca muta nem reordena o prefixo imutável** (ADR-009); actua sobre o tail / sumários auxiliares.
- [ ] O sumário resultante é uma **projecção** (AOS-036): o registo completo permanece intacto no backend; nada do audit trail é descartado.
- [ ] A compressão é **idempotente** e reproduzível a partir do registo (mesmo input → mesmo sumário, dada a política versionada).
- [ ] O cache-hit-rate (SLI de AOS-037) **não regride** após a introdução da compressão; existe alerta para *cache thrash*.
- [ ] A compressão é accionada pelo sinal de exaustão graciosa (~80%) de AOS-037, não por hard-stop.

### Detalhes Técnicos

- **Componentes:** MEM (working/episodic), GW (cache-hit-rate SLI).
- **Ficheiros sugeridos:** `services/memory/compression/async_compactor.*`, `services/memory/compression/checkpoint_trigger.*`.
- Executa como *activity* durável fora do turno (coerente com ADR-001); política de compressão versionada.
- Liga-se ao burn-down/checkpoint do runtime (EPIC-02).

### Testes Requeridos

- Teste de que a compressão não corre na hot path (medição/asserção de caminho).
- Teste de invariância do prefixo após compressão (hash constante).
- Teste de que o registo completo permanece após compressão (Princípio 4).
- Teste de SLI: cache-hit-rate mantém-se acima do alvo com compressão activa.

### Definition of Done

- [ ] Critérios satisfeitos; compressão fora da hot path (provado).
- [ ] Prefixo imutável preservado; registo completo intacto.
- [ ] Idempotente e versionada.
- [ ] SLI de cache-hit-rate sem regressão; alerta de cache thrash.
- [ ] Spans OTel do checkpoint.
- [ ] Documentação em `tecnica/04`.

### Handoff para Claude Code

```
Implementa a compressão de contexto assíncrona do MEM conforme ADR-009: só em
checkpoints assíncronos, FORA da hot path (como activity durável), accionada pelo
sinal de exaustão graciosa (~80%) de AOS-037. A compressão NUNCA muta/reordena o
prefixo imutável e produz apenas um sumário auxiliar sobre o tail. O sumário é uma
projecção (AOS-036): o registo completo permanece no backend, nada do audit trail é
descartado. Torna-a idempotente e reproduzível (política versionada). Garante que o
cache-hit-rate (SLI de AOS-037) não regride e adiciona alerta de cache thrash. Prova
com testes: ausência de compressão síncrona, invariância do prefixo, registo intacto,
SLI. Ler tecnica/04 e a tabela de zonas do ADR-009.
```

---

## AOS-044 — Testes de integridade e migração de memória

| Campo | Valor |
|---|---|
| Epic | EPIC-04 — Memória e Persistência |
| Fase | Fase 4 — UX e evolução |
| Tipo | chore |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-041, AOS-035, AOS-042 |
| Bloqueia | — |
| Responsável sugerido | QA |
| Documentos de referência | `tecnica/04_Memoria_Persistencia.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `specs/EPIC-11_Testes_Qualidade.md`, ADR-012, ADR-005, ADR-009 |

### Contexto

A memória concentra três propriedades não-negociáveis que só uma suite dedicada garante ao longo do tempo: **integridade do registo** (Princípio 4 — nunca se perde o que o auditor precisa), **segurança da proveniência** (ASI06 — untrusted não comanda) e **segurança da evolução** (expand/contract — migrar sem perda nem downtime). Este ticket entrega o *safety net* de testes que torna estas propriedades verificáveis em CI, cruzando com os gates de replay determinístico e política do EPIC-11 e do documento de Engineering Standards.

### Objectivo

Construir a **suite de testes de integridade e migração de memória** que valida, em CI, a não-perda do registo, a barreira contexto/registo, a quarentena de untrusted, e o *round-trip* das migrações expand/contract com rollback — como gate bloqueante.

### Critérios de Aceitação

- [ ] Existe uma suite de **integridade**: prova que nenhuma operação de memória (projecção, eviction, compressão) apaga do registo o que o audit trail exige (Princípio 4).
- [ ] Existe uma suite de **migração**: *round-trip* expand → migrate → contract sem perda de dados, com rollback de migração falhada e idempotência (cruza AOS-041).
- [ ] Existe uma suite de **proveniência/segurança**: memória em quarentena não autoriza acções; taint propaga transitivamente (cruza AOS-042).
- [ ] Os testes correm em CI como **gate bloqueante** (fail-closed) e não regridem cobertura abaixo do limiar (§4 do Engineering Standards).
- [ ] Inclui teste de **crypto-shredding/TTL**: dados pessoais tornam-se irrecuperáveis sem partir a cadeia de hash (ADR-011).
- [ ] Inclui teste de **estabilidade de cache** (prefixo imutável) sob compressão (cruza AOS-043).

### Detalhes Técnicos

- **Componentes:** MEM (todas as classes), CI.
- **Ficheiros sugeridos:** `services/memory/tests/integrity/`, `services/memory/tests/migration/`, `services/memory/tests/provenance/`.
- Integra os gates 7 (política), 8 (replay) e o gate de migração no pipeline (Engineering Standards §4).
- Usa fixtures de trajectória e datasets golden para regressão.

### Testes Requeridos

- Integridade: projecção/eviction/compressão não apagam o registo.
- Migração: round-trip expand/contract, rollback, idempotência.
- Proveniência: quarentena não autoriza, taint transitivo.
- Conformidade: crypto-shredding e TTL.
- Cache: prefixo imutável sob compressão.

### Definition of Done

- [ ] Suites de integridade, migração e proveniência verdes.
- [ ] Gate bloqueante em CI (fail-closed); cobertura não regride.
- [ ] Testes de crypto-shredding/TTL e estabilidade de cache incluídos.
- [ ] Fixtures/golden datasets versionados.
- [ ] Documentação da suite em `tecnica/04` / `specs/EPIC-11`.
- [ ] Revisão por QA + Engenheiro de Dados/Memória.

### Handoff para Claude Code

```
Constrói a suite de testes de integridade e migração de memória do MEM como gate
bloqueante em CI (fail-closed). Três blocos: (1) INTEGRIDADE — projecção/eviction/
compressão nunca apagam o registo (Princípio 4); (2) MIGRAÇÃO — round-trip
expand->migrate->contract sem perda, rollback de migração falhada, idempotência
(AOS-041); (3) PROVENIÊNCIA — quarentena não autoriza acções, taint transitivo
(AOS-042). Inclui crypto-shredding/TTL (ADR-011) e estabilidade do prefixo de cache
sob compressão (AOS-043). Não deixes a cobertura regredir abaixo do limiar (§4 do
Engineering Standards). Usa fixtures de trajectória e golden datasets versionados.
Ler tecnica/04, specs/01 e specs/EPIC-11.
```

---

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
