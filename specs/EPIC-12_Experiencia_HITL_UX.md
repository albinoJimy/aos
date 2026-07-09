# EPIC-12 — Experiência de Utilização e Controlo Humano (UX/DX)

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Experiência de Utilização e Controlo Humano (UX/DX) |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/15_Experiencia_HITL_UX.md`, `tecnica/09_Governacao_Conformidade.md`, `specs/EPIC-09_Governacao_Conformidade.md`, `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md`, `specs/EPIC-08_Observabilidade_Evals.md`, `specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md` |

---

## 1. Visão do Epic

Este epic materializa a **Dimensão 6 — Experiência de utilização (UX/DX)** do blueprint: a camada de *apresentação e interacção* que transforma um kernel governado num sistema que um humano consegue **ver, dirigir e aprovar** com confiança. O diagnóstico do plano-base era duro — «90% kernel e ~0% interacção»: oferecia streaming read-only e recuperação pós-falha (observação passiva), não controlo. Este epic fecha essa lacuna sem reabrir nenhuma das fronteiras já postas: é a *superfície*, não o *enforcement*.

A distinção é o princípio organizador de todo o epic. O estado durável `paused` e o gate `waiting_on_human`, a semântica de fail-closed, a assinatura de aprovações, o tiering SA-ROC de risco, a taxonomia L0–L5 e o pipeline de auto-modificação **já existem e são impostos noutros epics** — respectivamente EPIC-02 (AOS-023), EPIC-07 (AOS-074), EPIC-09 (AOS-089/090/095/096) e EPIC-08 (AOS-077). Os dez tickets deste epic (AOS-119 a AOS-128) **consomem** esses mecanismos e dão-lhes uma superfície de interacção coerente, multiplataforma e calibrada. Nenhum ticket aqui reimplementa uma decisão de segurança ou de política — se um approval-card recusa uma acção, quem recusa é o PEP; o card apenas apresenta o preview e recolhe a decisão assinada.

O epic entrega: um **contrato unificado da superfície de controlo out-of-band** para steer/interrupt em qualquer canal (AOS-119); o **modelo do approval-card** com preview do efeito concreto resolvido e dual-control para irreversíveis (AOS-120); o **gate de aprovação-de-plano** que deixa o humano ver e editar o grafo antes de queimar tokens (AOS-121); a **paridade de superfície** aprovação-como-card em Slack/Telegram/desktop via adaptador de plataforma (AOS-122); a **semântica de progresso + burn-down de custo** com prompt de exaustão graciosa a ~80% (AOS-123); a **calibração de confiança** com linguagem de incerteza e histórico de correcções (AOS-124); a **UX da autonomia progressiva** por maturidade do utilizador (AOS-125); o **loop de autoria de skills** com dry-run e atribuição visível (AOS-126); a **visualização e drill-down da trajectória do sub-agente** que consome os spans de AOS-077 (AOS-127); e os **testes de UX/DX** que medem usabilidade dos gates e a eficácia anti-fadiga (AOS-128).

O epic concretiza directamente **ADR-013** (steer/interrupt, gate de aprovação-de-plano, controlo bidireccional) na sua face de interacção, apoiando-se em **ADR-014** (taxonomia L0–L5) e **ADR-010** (observabilidade OTel como fonte da visualização). Pertence à **Fase 4 — UX e evolução** do roadmap. A referência técnica autoritativa é `tecnica/15_Experiencia_HITL_UX.md`.

---

## 2. Critérios de Saída do Epic

- [ ] Existe um **contrato unificado** da superfície de controlo out-of-band (steer/interrupt) que qualquer canal usa para sinalizar o loop, sobre o estado durável `paused` de AOS-023 — **sem** reimplementar a máquina de estados (AOS-119).
- [ ] O **approval-card** apresenta sempre o **preview do efeito concreto resolvido** e exige **dual-control** para acções irreversíveis, delegando a decisão de risco ao gate de EPIC-07/EPIC-09 (AOS-120).
- [ ] Um **gate de aprovação-de-plano** permite ver e editar o grafo de tarefas **antes** do spawn, separado dos gates de acção (AOS-121).
- [ ] A aprovação-como-card tem **paridade de superfície** em Slack, Telegram e desktop, através de um **adaptador de plataforma** com um único modelo canónico (AOS-122).
- [ ] Toda a resposta expõe **semântica de progresso + burn-down de custo**, e a ~80% do orçamento dispara um **prompt de exaustão graciosa** (estender / resumir e parar / abortar) em vez de hard-stop cego (AOS-123).
- [ ] A superfície faz **calibração de confiança**: linguagem de incerteza selectiva e histórico de correcções expostos ao utilizador (AOS-124).
- [ ] A **promoção de autonomia L0–L5** tem uma UX legível por maturidade do utilizador que reflecte (nunca decide) o nível imposto por AOS-089/090 (AOS-125).
- [ ] O **loop de autoria de skills** oferece **dry-run** e **atribuição visível** do autor/versão, ligado ao gate de ratificação de EPIC-09 (AOS-126).
- [ ] A **trajectória do sub-agente** é visualizável com drill-down, consumindo a árvore de spans de AOS-077 — sem duplicar o backend de observabilidade (AOS-127).
- [ ] Existem **testes de UX/DX** que medem usabilidade dos gates e a eficácia **anti-fadiga** (override-rate como sinal de qualidade da superfície) (AOS-128).
- [ ] Todos os tickets do epic têm DoD de domínio verde (spans OTel de interacção, sem segredos, acessibilidade das superfícies) conforme `specs/01_Engineering_Standards_e_Handoff.md`.

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-119 | Contrato unificado da superfície de controlo HITL out-of-band (steer/interrupt) [ADR-013] | feature | M | P1 | EPIC-02 (AOS-023 estado `paused`) |
| AOS-120 | Modelo do approval-card (preview do efeito concreto resolvido; dual-control p/ irreversíveis) | feature | M | P2 | AOS-119, EPIC-07 (AOS-074), EPIC-09 (AOS-095) |
| AOS-121 | Gate de aprovação-de-plano antes do spawn | feature | M | P2 | AOS-120, EPIC-03 (orquestrador/spawn) |
| AOS-122 | Paridade de superfície: aprovação-como-card em Slack/Telegram/desktop (adaptador de plataforma) | feature | M | P2 | AOS-120 |
| AOS-123 | Semântica de progresso + burn-down de custo + prompt de exaustão graciosa a ~80% | feature | M | P2 | EPIC-08 (custo por span), EPIC-03 (orçamento) |
| AOS-124 | Calibração de confiança (linguagem de incerteza, histórico de correcções) | feature | S | P2 | AOS-119, EPIC-08 (spans/evals) |
| AOS-125 | Autonomia progressiva por maturidade do utilizador (UX da promoção L0–L5) | feature | M | P2 | AOS-120, EPIC-09 (AOS-089/090) |
| AOS-126 | Loop de autoria de skills com dry-run e atribuição visível | feature | M | P2 | AOS-120, EPIC-09 (AOS-096), EPIC-05 (registry) |
| AOS-127 | Visualização/drill-down da trajectória do sub-agente | feature | M | P2 | EPIC-08 (AOS-077 árvore de spans) |
| AOS-128 | Testes de UX/DX (usabilidade dos gates; anti-fadiga / override-rate) | chore | S | P2 | AOS-119, AOS-120, AOS-121, AOS-123, AOS-125 |

Estimativas: XS/S/M/L (XL proibido). Prioridades: P0/P1/P2. Dependências cross-epic referidas por epic; intra-epic por ticket. Toda a Fase 4. Este epic é a *camada de apresentação/interacção*: não duplica enforcement (que vive em EPIC-02/07/08/09).

---

## AOS-119 — Contrato unificado da superfície de controlo HITL out-of-band (steer/interrupt) [ADR-013]

| Campo | Valor |
|---|---|
| Epic | EPIC-12 — Experiência de Utilização e Controlo Humano (UX/DX) |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | EPIC-02 (AOS-023 — estado durável `paused` + canal de steer/interrupt) |
| Bloqueia | AOS-120, AOS-124, AOS-128 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/15_Experiencia_HITL_UX.md` §2, `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md` (AOS-023), ADR-013 |

### Contexto

O blueprint exige **controlo bidireccional** — pausar, injectar correcção, retomar — em **qualquer** superfície, não só observação. O mecanismo durável já existe: AOS-023 (EPIC-02) implementa o estado `paused`, o *graceful pause* no fim do turno e o canal de steer/interrupt no runtime. O que falta é o **contrato de superfície** que qualquer canal (desktop, chatbot, API) usa para emitir esses sinais de forma uniforme e para receber o eco do estado. Sem este contrato, cada canal reinventaria a semântica de controlo e divergiria. Este ticket é *apresentação/protocolo de interacção*; **não** reimplementa a máquina de estados nem o pause durável.

### Objectivo

Definir e implementar o contrato unificado da superfície de controlo out-of-band: um protocolo estável (steer, interrupt, resume-com-correcção, query-de-estado) que traduz acções de utilizador de qualquer canal nos sinais que AOS-023 já consome, e que reflecte de volta o estado durável (`running`/`paused`/`waiting_on_human`) de forma consistente.

### Critérios de Aceitação

- [ ] Existe um **contrato único** (schema de mensagens de controlo) para `steer`, `interrupt`, `resume` (com payload de correcção) e `query-estado`, independente do canal.
- [ ] Um sinal emitido por qualquer canal provoca o **graceful pause** no fim do turno via o mecanismo de AOS-023 — **sem** este ticket implementar a transição de estado (delega no runtime).
- [ ] O contrato é **out-of-band**: o canal de controlo é distinto do canal de dados/conteúdo (uma correcção de utilizador nunca é confundida com output do modelo — respeita a separação control/data-plane).
- [ ] O estado durável corrente é **reflectido** de volta a todos os canais subscritos de forma consistente (o mesmo run mostra o mesmo estado em desktop e em chatbot).
- [ ] O contrato é **versionado** (SemVer) para que adaptadores de plataforma (AOS-122) o consumam sem acoplamento a implementação.
- [ ] Cada acção de controlo emite um **span OTel** de interacção (quem, quando, que sinal), ligado ao trace do run.

### Detalhes Técnicos

- Componentes: **RT** (consome sinais — via AOS-023, não reimplementar), **superfície de controlo** (novo, camada de apresentação), **OBS** (spans de interacção).
- Contrato: mensagens `control.steer` / `control.interrupt` / `control.resume{correction}` / `control.state`; transporte out-of-band (canal separado do stream de conteúdo).
- Reflexão de estado: subscrição ao estado durável exposto por EPIC-02; projecção read-model por run.
- Não implementar: `paused`, graceful pause, lease — pertencem a AOS-023/AOS-018.

### Testes Requeridos

- Teste de contrato: cada mensagem de controlo valida contra o schema versionado.
- Teste de graceful pause: sinal `interrupt` de um canal provoca pause no fim do turno (via AOS-023, mockado/integrado).
- Teste de out-of-band: correcção injectada entra como dado de controlo, nunca como instrução no data-plane.
- Teste de consistência: dois canais subscritos ao mesmo run vêem o mesmo estado.
- Teste de span: cada acção de controlo emite span ligado ao trace.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Contrato versionado (SemVer) documentado; sem reimplementação da máquina de estados (delega em AOS-023).
- [ ] Spans OTel de interacção emitidos e ligados ao trace.
- [ ] Separação control/data-plane preservada; sem segredos em código/logs/spans.
- [ ] Revisão por dois revisores (contrato transversal).

### Handoff para Claude Code

```text
És o executor do ticket AOS-119 do Agentic OS de Referência (AOS).
Lê specs/EPIC-12 e tecnica/15 §2, mais specs/EPIC-02 (AOS-023) e ADR-013.
Define e implementa o CONTRATO UNIFICADO da superfície de controlo out-of-band
(steer, interrupt, resume-com-correcção, query-estado) que QUALQUER canal usa para
sinalizar o loop. NÃO reimplementes o estado `paused`, o graceful pause nem a máquina
de estados — isso é AOS-023 (EPIC-02); este ticket só traduz acções de utilizador nos
sinais que o runtime já consome e reflecte o estado durável de volta. Mantém o canal
de controlo OUT-OF-BAND (separado do data-plane; uma correcção nunca é instrução).
Versiona o contrato em SemVer para os adaptadores de AOS-122. Emite spans OTel de
interacção ligados ao trace. Escreve testes de contrato, graceful pause, out-of-band,
consistência entre canais e span. Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-120 — Modelo do approval-card (preview do efeito concreto resolvido; dual-control p/ irreversíveis)

| Campo | Valor |
|---|---|
| Epic | EPIC-12 — Experiência de Utilização e Controlo Humano (UX/DX) |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-119, EPIC-07 (AOS-074 — gate de risco), EPIC-09 (AOS-095 — aprovação assinada, fail-closed) |
| Bloqueia | AOS-121, AOS-122, AOS-125, AOS-126, AOS-128 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/15_Experiencia_HITL_UX.md` §3, `specs/EPIC-09_Governacao_Conformidade.md` (AOS-095), `specs/EPIC-07_Seguranca_Isolamento.md` (AOS-074), ADR-013 |

### Contexto

Quando o gate de risco (AOS-074, EPIC-07) classifica uma acção como `danger`/irreversível e o gate HITL (AOS-095, EPIC-09) a escala para `waiting_on_human`, o humano tem de **ver o que vai acontecer** antes de decidir. A qualidade da supervisão depende inteiramente da qualidade desse *preview*: um card que mostra apenas "executar tool X" produz rubber-stamping; um card que mostra o **efeito concreto resolvido** (o comando exacto, os alvos reais, o diff previsto) produz escrutínio real. Este ticket define o **modelo canónico do approval-card** — a peça de apresentação. A **decisão de risco, a assinatura e o fail-closed pertencem a AOS-074/AOS-095**; o card apenas resolve o preview e recolhe a decisão para as devolver ao PEP.

### Objectivo

Implementar o modelo canónico do approval-card: uma estrutura de apresentação que resolve o **efeito concreto** de uma acção escalada (parâmetros resolvidos, alvos, reversibilidade, custo estimado), exige interacção de **dual-control** (4-eyes) para irreversíveis, e devolve a decisão do utilizador ao gate HITL de EPIC-09 para assinatura e enforcement.

### Critérios de Aceitação

- [ ] O card apresenta o **efeito concreto resolvido** — não o template da tool, mas os valores reais (comando/alvo/argumentos resolvidos, diff ou preview do resultado quando aplicável).
- [ ] O card exibe a **classe de risco** e a **reversibilidade** vindas do gate (AOS-074) — o card **lê**, não classifica.
- [ ] Para acções **irreversíveis**, o card exige **dual-control (4-eyes)**: dois aprovadores distintos antes de a decisão ser aceite.
- [ ] A decisão do utilizador é devolvida ao gate HITL (AOS-095) para **assinatura e enforcement fail-closed** — o card **não** decide nem assina por si.
- [ ] O card é um **modelo canónico** único, independente de plataforma, pronto a ser renderizado pelos adaptadores de AOS-122.
- [ ] A apresentação e a decisão emitem **spans OTel** de interacção ligados ao gate/trace, sem expor segredos nem PII em claro no preview.

### Detalhes Técnicos

- Componentes: **superfície de aprovação** (novo, apresentação), **RM/PEP** (fonte do efeito a resolver e destino da decisão), **GOV** (AOS-095: assinatura, fail-closed, override-rate).
- Resolução do preview: materializar os argumentos concretos da tool call (pós-taint, pós-redação) antes de renderizar; nunca mostrar segredo downstream (broker JIT).
- Dual-control: exigência de duas assinaturas distintas para `irreversível`; a lógica de assinatura/timeout vive em AOS-095.
- Modelo canónico serializável, consumido por AOS-122.

### Testes Requeridos

- Teste de preview concreto: card de uma acção mostra argumentos resolvidos, não o template.
- Teste de leitura de risco: classe/reversibilidade exibidas correspondem ao gate (AOS-074), sem reclassificação local.
- Teste de dual-control: acção irreversível não avança com um só aprovador.
- Teste de delegação: a decisão é devolvida ao gate HITL (AOS-095) e é este que assina/impõe fail-closed.
- Teste de ausência de segredo/PII: o preview não contém segredos downstream nem PII em claro.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Modelo canónico do card documentado e serializável (consumível por AOS-122).
- [ ] Delegação a AOS-074 (risco) e AOS-095 (assinatura/fail-closed) sem duplicar enforcement.
- [ ] Spans OTel de apresentação/decisão; sem segredos nem PII no preview.
- [ ] Revisão por dois revisores (artefacto de gate/segurança).

### Handoff para Claude Code

```text
És o executor do ticket AOS-120 do AOS. Lê specs/EPIC-12 e tecnica/15 §3, mais
specs/EPIC-09 (AOS-095), specs/EPIC-07 (AOS-074) e ADR-013.
Implementa o MODELO CANÓNICO do approval-card (camada de apresentação). Resolve e
mostra o EFEITO CONCRETO (argumentos/alvos resolvidos, diff/preview, custo estimado),
não o template da tool. LÊ a classe de risco e a reversibilidade do gate (AOS-074) —
não classifiques localmente. Exige DUAL-CONTROL (4-eyes) para irreversíveis. Devolve
a decisão ao gate HITL (AOS-095) que É QUEM assina e impõe fail-closed — o card não
decide nem assina. Garante que o preview nunca expõe segredos downstream (broker JIT)
nem PII em claro. Deixa o modelo serializável para os adaptadores de AOS-122. Emite
spans OTel de interacção. Escreve testes de preview concreto, leitura de risco,
dual-control, delegação da decisão e ausência de segredo/PII. Não expandas escopo.
```

---

## AOS-121 — Gate de aprovação-de-plano antes do spawn

| Campo | Valor |
|---|---|
| Epic | EPIC-12 — Experiência de Utilização e Controlo Humano (UX/DX) |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-120, EPIC-03 (orquestrador — grafo de tarefas e spawn) |
| Bloqueia | AOS-128 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/15_Experiencia_HITL_UX.md` §4, `specs/EPIC-03_Orquestracao_Escalonamento.md`, ADR-013 |

### Contexto

O blueprint introduz um **gate de aprovação-de-plano separado dos gates de acção**: o humano vê e edita o **grafo de tarefas antes** de o orquestrador queimar tokens no spawn (estilo AgentScope). Aprovar acção-a-acção depois do plano já estar a correr é tarde e caro; aprovar o plano à cabeça é barato e alinha expectativas. O grafo de tarefas e o spawn pertencem ao orquestrador (EPIC-03); este ticket é a **superfície de aprovação-de-plano** que se interpõe *antes* do spawn e reutiliza a mecânica de card de AOS-120.

### Objectivo

Implementar o gate de aprovação-de-plano: apresentar o grafo de tarefas proposto pelo orquestrador ao humano **antes do spawn**, permitir aprová-lo, editá-lo ou rejeitá-lo, e só libertar o spawn após aprovação — distinto e anterior aos gates de acção.

### Critérios de Aceitação

- [ ] O grafo de tarefas proposto é **apresentado antes do spawn**; nenhum sub-agente é lançado antes da decisão (o custo de tokens do plano é adiado).
- [ ] O humano pode **aprovar, editar (podar/reordenar/anotar) ou rejeitar** o plano; a edição é devolvida ao orquestrador antes do spawn.
- [ ] O gate de plano é **distinto dos gates de acção** (AOS-120): opera sobre o *grafo*, não sobre uma tool call individual.
- [ ] A aprovação-de-plano respeita o nível de autonomia (L0–L5) do par (agente, domínio) — a níveis altos pode ser auto-aprovada; a decisão de nível vem de EPIC-09, o gate só a **consome**.
- [ ] A decisão (aprovação/edição/rejeição) é **auditável** e emite spans OTel, ligada ao run.

### Detalhes Técnicos

- Componentes: **superfície de aprovação-de-plano** (novo), **ORQ** (fonte do grafo, destino da edição — EPIC-03), **GOV** (consulta de nível L0–L5, ver AOS-089).
- Interpõe-se no ponto de *pré-spawn* do orquestrador; reutiliza a renderização de card de AOS-120 para o grafo.
- Edição do plano devolvida como grafo revisto ao orquestrador antes da reserva de headroom/spawn.

### Testes Requeridos

- Teste de pré-spawn: nenhum sub-agente é lançado antes da aprovação do plano.
- Teste de edição: podar/reordenar o grafo devolve o plano revisto ao orquestrador.
- Teste de separação: o gate de plano opera sobre o grafo, não sobre tool call individual.
- Teste de nível: a níveis altos (L4/L5) o plano pode auto-aprovar consultando AOS-089 (consumo, não decisão).
- Teste de auditoria: decisão de plano gera span/evento ligado ao run.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Gate de plano distinto dos gates de acção; interposto antes do spawn (EPIC-03).
- [ ] Consumo do nível L0–L5 (AOS-089) sem reimplementar a taxonomia.
- [ ] Spans OTel da decisão de plano; sem segredos.
- [ ] Revisão por dois revisores.

### Handoff para Claude Code

```text
És o executor do ticket AOS-121 do AOS. Lê specs/EPIC-12 e tecnica/15 §4, mais
specs/EPIC-03 e ADR-013.
Implementa o GATE DE APROVAÇÃO-DE-PLANO, distinto e ANTERIOR aos gates de acção:
apresenta o grafo de tarefas do orquestrador ANTES DO SPAWN, deixa o humano aprovar,
EDITAR (podar/reordenar/anotar) ou rejeitar, e só liberta o spawn após aprovação. O
grafo e o spawn são do orquestrador (EPIC-03) — não os reimplementes; interpõe-te no
ponto de pré-spawn e reutiliza o card de AOS-120 para renderizar o grafo. CONSOME o
nível de autonomia L0–L5 (AOS-089) para permitir auto-aprovação a níveis altos, sem
decidir o nível. Torna a decisão auditável (spans ligados ao run). Escreve testes de
pré-spawn, edição do grafo, separação face aos gates de acção, nível e auditoria.
Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-122 — Paridade de superfície: aprovação-como-card em Slack/Telegram/desktop (adaptador de plataforma)

| Campo | Valor |
|---|---|
| Epic | EPIC-12 — Experiência de Utilização e Controlo Humano (UX/DX) |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-120 (modelo canónico do card) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/15_Experiencia_HITL_UX.md` §5, ADR-013 |

### Contexto

O blueprint exige **paridade de superfície**: a mesma aprovação-como-card tem de estar disponível onde o utilizador está — Slack, Telegram, desktop — sem que o poder de decisão dependa do canal. Se a aprovação com preview e dual-control só existir no desktop, os utilizadores aprovarão às cegas pelo chat, e a governação evapora-se no canal mais fraco. Este ticket implementa um **adaptador de plataforma** que renderiza o **modelo canónico** de AOS-120 (e o gate de plano de AOS-121) em cada superfície, preservando a semântica. É pura tradução de apresentação — **nenhuma** decisão de risco vive no adaptador.

### Objectivo

Implementar o adaptador de plataforma que renderiza o modelo canónico do approval-card em Slack, Telegram e desktop com **paridade funcional** — preview do efeito concreto, dual-control para irreversíveis, e devolução da decisão ao gate — mantendo um único modelo canónico como fonte de verdade.

### Critérios de Aceitação

- [ ] O **mesmo modelo canónico** (AOS-120) é renderizado em Slack, Telegram e desktop; não há um segundo modelo por plataforma.
- [ ] Cada superfície preserva o **preview do efeito concreto** e o **dual-control** para irreversíveis — nenhuma superfície degrada a semântica de aprovação.
- [ ] A decisão recolhida em qualquer plataforma é devolvida ao **mesmo gate HITL** (AOS-095) com identidade do aprovador; a plataforma não decide nem assina.
- [ ] Uma **capacidade não representável** numa plataforma (ex.: dual-control num canal sem UI adequada) faz o card **degradar fail-closed** (recusa/encaminha), nunca aprovar por omissão.
- [ ] O adaptador consome o **contrato versionado** (AOS-119) e o modelo de card sem acoplamento a implementação interna.

### Detalhes Técnicos

- Componentes: **adaptador de plataforma** (novo, apresentação), consumindo o modelo canónico de AOS-120 e o contrato de AOS-119.
- Renderizadores por canal: blocos Slack, teclados inline Telegram, componentes desktop — todos derivados do modelo canónico.
- Degradação fail-closed quando uma capacidade (ex.: 4-eyes) não é representável no canal.
- Identidade do aprovador propagada por canal ao gate HITL (AOS-095).

### Testes Requeridos

- Teste de paridade: a mesma acção gera cards equivalentes (preview + dual-control) nas três superfícies.
- Teste de fonte única: alterar o modelo canónico propaga-se a todas as plataformas (sem modelo duplicado).
- Teste de degradação fail-closed: canal sem suporte a dual-control recusa/encaminha, nunca aprova.
- Teste de identidade: a decisão por canal chega ao gate com o aprovador correcto.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Um único modelo canónico (AOS-120) como fonte; adaptadores sem lógica de risco.
- [ ] Degradação fail-closed testada nos canais sem capacidade.
- [ ] Spans OTel de interacção por canal; sem segredos.
- [ ] Revisão por dois revisores.

### Handoff para Claude Code

```text
És o executor do ticket AOS-122 do AOS. Lê specs/EPIC-12 e tecnica/15 §5 e ADR-013.
Implementa o ADAPTADOR DE PLATAFORMA que renderiza o MODELO CANÓNICO do approval-card
(AOS-120) e o gate de plano (AOS-121) em Slack, Telegram e desktop com PARIDADE de
superfície: preview do efeito concreto e dual-control para irreversíveis em todas.
Mantém UM ÚNICO modelo canónico como fonte de verdade — nada de modelo por plataforma.
A decisão recolhida em qualquer canal é devolvida ao MESMO gate HITL (AOS-095) com a
identidade do aprovador; o adaptador NÃO decide nem assina. Se uma capacidade (ex.:
4-eyes) não for representável num canal, DEGRADA FAIL-CLOSED (recusa/encaminha), nunca
aprova por omissão. Consome o contrato versionado de AOS-119. Escreve testes de
paridade, fonte única, degradação fail-closed e identidade do aprovador. Não expandas
escopo; abre PR com o template padrão.
```

---

## AOS-123 — Semântica de progresso + burn-down de custo + prompt de exaustão graciosa a ~80%

| Campo | Valor |
|---|---|
| Epic | EPIC-12 — Experiência de Utilização e Controlo Humano (UX/DX) |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | EPIC-08 (custo por span, `gen_ai.usage.*`), EPIC-03 (orçamento por árvore em tokens/$) |
| Bloqueia | AOS-128 |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/15_Experiencia_HITL_UX.md` §6, `specs/EPIC-08_Observabilidade_Evals.md`, `specs/EPIC-03_Orquestracao_Escalonamento.md`, ADR-008 |

### Contexto

O plano-base dava um **hard-stop cego por budget**: o run morria ao esgotar o orçamento, sem aviso nem escolha. O blueprint substitui-o por **semântica de progresso + burn-down de custo** visível e, a ~80% do orçamento, um **prompt de exaustão graciosa** que oferece ao utilizador *estender / resumir e parar / abortar*. O orçamento e a contabilidade de custo já existem — o orçamento por árvore em tokens/$ vive no admission control (EPIC-03, ADR-008) e o custo por span vem da observabilidade (EPIC-08). Este ticket é a **superfície** que lê esses sinais, mostra o burn-down e apresenta a escolha a ~80% — sem reimplementar a contabilidade nem o enforcement do budget.

### Objectivo

Implementar a superfície de progresso e custo: expor semântica de progresso legível e burn-down de custo em tempo real a partir dos spans/orçamento existentes, e disparar a ~80% do orçamento um prompt de exaustão graciosa (estender / resumir e parar / abortar) cuja decisão é devolvida ao orquestrador.

### Critérios de Aceitação

- [ ] Cada resposta expõe **semântica de progresso** (o que está a acontecer, que passo) e **burn-down de custo** (consumido vs orçamento) lidos dos spans (EPIC-08) e do orçamento (EPIC-03).
- [ ] A ~80% do orçamento dispara um **prompt de exaustão graciosa** com três opções: **estender**, **resumir e parar**, **abortar**.
- [ ] A decisão do prompt é devolvida ao orquestrador/admission control (EPIC-03) — a superfície **não** altera o orçamento por si; solicita a extensão ao controlo que a impõe.
- [ ] O burn-down usa a contabilidade **existente** (custo em USD por span, `gen_ai.usage.*`) — **sem** recontabilizar custo localmente.
- [ ] O limiar (~80%) é **configurável**; ausência de resposta ao prompt aplica a política de degradação de EPIC-03 (não morre silenciosamente sem sinal).

### Detalhes Técnicos

- Componentes: **superfície de progresso** (novo), **OBS** (fonte: custo/tokens por span — EPIC-08), **ADM/SCH** (orçamento por árvore, extensão — EPIC-03).
- Burn-down como projecção read-time sobre spans; sem duplicar a contabilidade.
- Prompt a ~80%: opções estender/resumir/abortar; extensão pedida ao admission control (reserva de headroom), nunca concedida pela superfície.
- Limiar configurável; ligação à degradação graciosa (shed/defer/downgrade) de EPIC-03.

### Testes Requeridos

- Teste de burn-down: o custo mostrado corresponde à soma dos spans (sem recontabilizar).
- Teste de limiar: a ~80% dispara o prompt com as três opções.
- Teste de delegação: "estender" pede ao admission control (EPIC-03), que decide; a superfície não altera o budget.
- Teste de progresso: a semântica de progresso reflecte o passo/estado corrente.
- Teste de configurabilidade do limiar.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Burn-down derivado dos spans de EPIC-08 (sem recontabilizar custo).
- [ ] Extensão delegada ao admission control de EPIC-03 (ADR-008).
- [ ] Spans OTel do prompt de exaustão e da decisão; sem segredos.
- [ ] Documentação e ADR-008 referenciados.

### Handoff para Claude Code

```text
És o executor do ticket AOS-123 do AOS. Lê specs/EPIC-12 e tecnica/15 §6, mais
specs/EPIC-08, specs/EPIC-03 e ADR-008.
Implementa a SUPERFÍCIE de progresso e custo: expõe semântica de progresso e
BURN-DOWN de custo em tempo real, LIDOS dos spans (custo/tokens por span, EPIC-08) e
do orçamento por árvore (EPIC-03) — NÃO recontabilizes custo nem reimplementes o
budget. A ~80% do orçamento dispara um PROMPT DE EXAUSTÃO GRACIOSA com três opções:
estender / resumir e parar / abortar. "Estender" PEDE a extensão ao admission control
(EPIC-03), que a impõe — a superfície não altera o budget. Torna o limiar (~80%)
configurável e liga a ausência de resposta à degradação graciosa de EPIC-03 (nunca
morrer em silêncio). Escreve testes de burn-down, limiar, delegação da extensão,
progresso e configurabilidade. Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-124 — Calibração de confiança (linguagem de incerteza, histórico de correcções)

| Campo | Valor |
|---|---|
| Epic | EPIC-12 — Experiência de Utilização e Controlo Humano (UX/DX) |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-119, EPIC-08 (spans/evals, histórico) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/15_Experiencia_HITL_UX.md` §7, `specs/EPIC-08_Observabilidade_Evals.md`, ADR-013 |

### Contexto

O over-trust é tão perigoso quanto o under-trust: um utilizador que confia cegamente aprova alucinações; um que desconfia de tudo anula o valor do agente. O blueprint pede **calibração activa de confiança** — **linguagem de incerteza selectiva** (sinalizar quando o agente está menos seguro) e **histórico de correcções** (mostrar quantas vezes, em contextos semelhantes, o agente foi corrigido). Esta é uma peça de *apresentação/DX*: consome os sinais e o histórico da observabilidade (EPIC-08) e da superfície de controlo (AOS-119, que regista as correcções via steer), sem inventar métricas de confiança novas nem alterar o comportamento do modelo.

### Objectivo

Implementar a superfície de calibração de confiança: apresentar linguagem de incerteza de forma selectiva (só quando informativa) e expor ao utilizador o histórico de correcções relevante para a acção/contexto corrente, a partir dos sinais existentes.

### Critérios de Aceitação

- [ ] A **linguagem de incerteza é selectiva**: só é apresentada quando há sinal de baixa confiança/ambiguidade — não um disclaimer genérico em toda a resposta (evita ruído e fadiga).
- [ ] O **histórico de correcções** relevante (quantas/que tipo de correcções via steer em contextos semelhantes) é exposto junto da acção, derivado dos registos de AOS-119 e dos spans/evals de EPIC-08.
- [ ] Os sinais de confiança/incerteza são **consumidos**, não inventados: a superfície não recalcula confiança do modelo — usa os sinais de eval/observabilidade existentes.
- [ ] A calibração está disponível nas superfícies de aprovação (informa a decisão do humano sem a substituir).
- [ ] A apresentação não expõe PII em claro no histórico e emite spans de interacção.

### Detalhes Técnicos

- Componentes: **superfície de calibração** (novo, DX), **OBS** (fonte: evals, `gen_ai.evaluation.result`, histórico de correcções), **superfície de controlo** (AOS-119: correcções via steer).
- Linguagem de incerteza accionada por limiar de sinal (evita disclaimer universal).
- Histórico de correcções como projecção sobre eventos de steer/correcção agrupados por contexto semelhante.

### Testes Requeridos

- Teste de selectividade: incerteza mostrada só acima do limiar de sinal; ausente quando confiança alta.
- Teste de histórico: correcções passadas em contexto semelhante são expostas correctamente.
- Teste de consumo: nenhum recálculo de confiança local; usa sinais de EPIC-08.
- Teste de ausência de PII no histórico apresentado.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Sinais consumidos de EPIC-08 e das correcções de AOS-119 (sem métricas inventadas).
- [ ] Spans OTel de interacção; histórico sem PII em claro.
- [ ] Sem segredos; scan limpo.
- [ ] Documentação referenciada.

### Handoff para Claude Code

```text
És o executor do ticket AOS-124 do AOS. Lê specs/EPIC-12 e tecnica/15 §7, mais
specs/EPIC-08, AOS-119 e ADR-013.
Implementa a SUPERFÍCIE de calibração de confiança (DX). Mostra LINGUAGEM DE INCERTEZA
de forma SELECTIVA — só quando há sinal de baixa confiança/ambiguidade, nunca um
disclaimer genérico em toda a resposta. Expõe o HISTÓRICO DE CORRECÇÕES relevante
(correcções via steer em contextos semelhantes), derivado dos registos de AOS-119 e
dos evals/spans de EPIC-08. CONSOME os sinais existentes — não recalcules confiança do
modelo nem inventes métricas. Disponibiliza a calibração nas superfícies de aprovação
para informar (não substituir) a decisão humana. Não exponhas PII no histórico; emite
spans de interacção. Escreve testes de selectividade, histórico, consumo de sinais e
ausência de PII. Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-125 — Autonomia progressiva por maturidade do utilizador (UX da promoção L0–L5)

| Campo | Valor |
|---|---|
| Epic | EPIC-12 — Experiência de Utilização e Controlo Humano (UX/DX) |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-120, EPIC-09 (AOS-089 taxonomia L0–L5, AOS-090 promoção/demoção) |
| Bloqueia | AOS-128 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/15_Experiencia_HITL_UX.md` §8, `specs/EPIC-09_Governacao_Conformidade.md` (AOS-089, AOS-090), ADR-014 |

### Contexto

A taxonomia de autonomia **L0–L5** e a sua promoção/demoção por fiabilidade medida são **impostas em EPIC-09** (AOS-089 e AOS-090): o nível de cada par (agente, domínio) e as suas transições são decisões de governação. O que falta é a **UX da autonomia progressiva**: tornar o nível corrente, os critérios de promoção e as transições **legíveis e accionáveis** pelo utilizador, adaptados à sua maturidade. Um utilizador novato precisa de gates a mais; um experiente que já demonstrou fiabilidade merece ver e (dentro do que a política permite) solicitar mais autonomia. Este ticket **reflecte e opera** a superfície de nível — **nunca decide** o nível, que continua a ser calculado por AOS-090.

### Objectivo

Implementar a UX da autonomia progressiva: apresentar ao utilizador o nível L0–L5 corrente por (agente, domínio), os critérios e o progresso rumo à próxima promoção, e as transições (promoção/demoção) com o seu motivo — tudo consumido de AOS-089/090, mais o fluxo pelo qual o utilizador solicita revisão de nível dentro da política.

### Critérios de Aceitação

- [ ] O **nível corrente** L0–L5 por (agente, domínio) é apresentado ao utilizador de forma legível, lido de AOS-089.
- [ ] O utilizador vê os **critérios de promoção** e o **progresso** (ex.: fiabilidade medida vs limiar) rumo ao próximo nível, derivados dos sinais de AOS-090.
- [ ] As **transições** (promoção/demoção) são apresentadas com o seu **motivo/métrica** — a superfície explica a decisão que AOS-090 tomou, sem a tomar.
- [ ] A UX é **progressiva por maturidade**: um utilizador com histórico de fiabilidade vê opções de solicitar mais autonomia; a decisão final é sempre da política (AOS-090), não da superfície.
- [ ] Uma **demoção automática** é comunicada de forma clara e imediata, com o motivo, sem esconder o rebaixamento.

### Detalhes Técnicos

- Componentes: **superfície de autonomia** (novo), **GOV** (fonte: AOS-089 nível, AOS-090 transições/métricas).
- Projecção read-model do estado de nível e do progresso de fiabilidade; pedido de revisão encaminhado ao controlador de autonomia (AOS-090), que decide.
- Comunicação de demoção event-driven (reactiva ao evento de AOS-090).

### Testes Requeridos

- Teste de leitura de nível: o nível apresentado corresponde a AOS-089.
- Teste de progresso: o progresso rumo à promoção reflecte a métrica de AOS-090.
- Teste de transição: promoção/demoção mostradas com motivo, sem serem decididas pela superfície.
- Teste de solicitação: pedido de mais autonomia é encaminhado a AOS-090 e respeita a decisão.
- Teste de demoção: rebaixamento automático é comunicado de imediato com motivo.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Nível e transições consumidos de AOS-089/090 (a superfície não decide nível).
- [ ] Spans OTel de interacção; sem segredos.
- [ ] Documentação e ADR-014 referenciados.
- [ ] Revisão por dois revisores.

### Handoff para Claude Code

```text
És o executor do ticket AOS-125 do AOS. Lê specs/EPIC-12 e tecnica/15 §8, mais
specs/EPIC-09 (AOS-089, AOS-090) e ADR-014.
Implementa a UX DA AUTONOMIA PROGRESSIVA. A taxonomia L0–L5 e a promoção/demoção são
IMPOSTAS em EPIC-09 (AOS-089/090) — NÃO as reimplementes nem decidas nível. Apresenta
o nível corrente por (agente, domínio), os critérios e o PROGRESSO rumo à próxima
promoção (fiabilidade medida vs limiar), e as TRANSIÇÕES com o seu motivo/métrica.
Adapta a UX à maturidade do utilizador (quem já demonstrou fiabilidade vê opções de
solicitar mais autonomia), mas a DECISÃO FINAL é sempre da política (AOS-090); encaminha
o pedido de revisão para o controlador. Comunica demoções automáticas de imediato e com
motivo, sem esconder. Escreve testes de leitura de nível, progresso, transição,
solicitação e demoção. Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-126 — Loop de autoria de skills com dry-run e atribuição visível

| Campo | Valor |
|---|---|
| Epic | EPIC-12 — Experiência de Utilização e Controlo Humano (UX/DX) |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-120, EPIC-09 (AOS-096 gate de ratificação de auto-modificação), EPIC-05 (registry SemVer) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `tecnica/15_Experiencia_HITL_UX.md` §9, `specs/EPIC-09_Governacao_Conformidade.md` (AOS-096), `specs/EPIC-05_Registry_Supply_Chain.md`, ADR-012 |

### Contexto

A auto-modificação (skills auto-escritas, memória procedural) é a mudança de **maior risco** e o seu pipeline de admissão — staging → eval-gate → canary → **ratificação assinada** → prod, com SemVer e rollback — é imposto por EPIC-09 (AOS-096) e ADR-012. O que este epic acrescenta é a **face de interacção** desse ciclo: um **loop de autoria** onde quem escreve/revê uma skill pode fazer **dry-run** (executar em modo simulado, ver o efeito sem o cometer) e onde a **atribuição é visível** (que agente/humano autorou, que versão, que proveniência). Sem dry-run, a ratificação é feita às cegas; sem atribuição visível, perde-se a cadeia de responsabilização à superfície. Este ticket **não** implementa o eval-gate nem a promoção — liga-se a eles.

### Objectivo

Implementar o loop de autoria de skills na superfície: um fluxo que permite executar uma skill candidata em **dry-run** (sem efeitos externos cometidos), exibe a **atribuição** (autor, versão SemVer, proveniência) e encaminha a candidata para o gate de ratificação de AOS-096, mostrando o resultado do eval/canary.

### Critérios de Aceitação

- [ ] Uma skill candidata pode correr em **dry-run**: os efeitos externos são **simulados/isolados** (nada é cometido no mundo externo), reutilizando o sandbox e o taint existentes.
- [ ] A **atribuição é visível**: autor (agente/humano), versão **SemVer** (do registry, EPIC-05) e proveniência são apresentados em todo o loop.
- [ ] A candidata é **encaminhada ao gate de ratificação** (AOS-096) — este ticket **não** ratifica nem promove; apresenta e submete.
- [ ] O **resultado do eval-gate/canary** (EPIC-09/EPIC-08) é apresentado ao autor/ratificador antes da decisão.
- [ ] O dry-run e a submissão emitem spans OTel ligados à trajectória, sem cometer efeitos nem expor segredos.

### Detalhes Técnicos

- Componentes: **superfície de autoria** (novo), **REG** (versão SemVer, proveniência — EPIC-05), **GOV** (submissão ao gate AOS-096), **SBX** (dry-run isolado).
- Dry-run: execução em sandbox com efeitos externos interceptados/simulados (nada cometido); reutiliza isolamento de EPIC-07.
- Atribuição derivada do manifesto de versão/registry; apresentação do eval result (`gen_ai.evaluation.result`).

### Testes Requeridos

- Teste de dry-run: efeitos externos são simulados/isolados; nada é cometido.
- Teste de atribuição: autor, versão SemVer e proveniência apresentados correctamente.
- Teste de encaminhamento: candidata submetida ao gate de ratificação (AOS-096), sem ratificação local.
- Teste de apresentação de eval: resultado do eval-gate/canary exibido antes da decisão.
- Teste de ausência de efeito/segredo no dry-run.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Dry-run isolado no sandbox (EPIC-07); sem efeitos cometidos.
- [ ] Submissão ao gate de ratificação de AOS-096 sem duplicar o pipeline (ADR-012).
- [ ] Atribuição/versão SemVer do registry (EPIC-05); spans OTel do loop.
- [ ] Sem segredos; scan limpo.

### Handoff para Claude Code

```text
És o executor do ticket AOS-126 do AOS. Lê specs/EPIC-12 e tecnica/15 §9, mais
specs/EPIC-09 (AOS-096), specs/EPIC-05 e ADR-012.
Implementa o LOOP DE AUTORIA de skills (superfície). Permite DRY-RUN de uma skill
candidata com efeitos externos SIMULADOS/ISOLADOS no sandbox (nada cometido no mundo
externo; reutiliza o isolamento de EPIC-07 e o taint). Torna a ATRIBUIÇÃO VISÍVEL:
autor (agente/humano), versão SemVer (do registry, EPIC-05) e proveniência em todo o
loop. ENCAMINHA a candidata ao gate de ratificação (AOS-096) — NÃO ratifiques nem
promovas aqui; o pipeline staging→eval→canary→ratificação é de EPIC-09/ADR-012.
Apresenta o resultado do eval-gate/canary antes da decisão. Emite spans do dry-run e da
submissão, sem cometer efeitos nem expor segredos. Escreve testes de dry-run isolado,
atribuição, encaminhamento, apresentação de eval e ausência de efeito/segredo. Não
expandas escopo; abre PR com o template padrão.
```

---

## AOS-127 — Visualização/drill-down da trajectória do sub-agente

| Campo | Valor |
|---|---|
| Epic | EPIC-12 — Experiência de Utilização e Controlo Humano (UX/DX) |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | EPIC-08 (AOS-077 — árvore de spans completa de sub-agentes) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/15_Experiencia_HITL_UX.md` §10, `specs/EPIC-08_Observabilidade_Evals.md` (AOS-077), ADR-010 |

### Contexto

O Princípio 4 (contexto ≠ registo) resolveu a contradição «avaliamos trajectórias, não saídas» vs «o filho só devolve o resumo»: ao pai vai um resumo higienizado, mas a **árvore de spans completa** do sub-agente é sempre persistida no backend (AOS-077, EPIC-08). Isso torna o eval-driven development e o debug viáveis — **desde que exista uma superfície para os ver**. Este ticket implementa a **visualização e o drill-down** dessa trajectória: navegar a árvore de spans, expandir sub-agentes, inspeccionar cada tool call com tokens/custo. É puro *consumo* dos spans de AOS-077 — **não** altera o que é capturado nem o formato OTel.

### Objectivo

Implementar a superfície de visualização e drill-down da trajectória: renderizar a árvore de spans completa (`invoke_agent`/`execute_tool`/`chat`) de um run e dos seus sub-agentes, permitir expandir/colapsar e inspeccionar cada span (atributos, tokens, custo, resultado), consumindo directamente o backend de observabilidade de AOS-077.

### Critérios de Aceitação

- [ ] A **árvore de spans completa** de um run e dos seus sub-agentes é visualizável (hierarquia `invoke_agent` → `execute_tool` → `chat`), lida de AOS-077.
- [ ] O utilizador faz **drill-down**: expandir um sub-agente, inspeccionar uma tool call individual e ver os seus atributos (tokens, custo em USD, resultado, taint).
- [ ] A visualização **consome** os spans OTel existentes — **não** captura, muta nem re-emite spans; sem lock-in ao dashboard interno.
- [ ] Conteúdo **untrusted/PII** é apresentado respeitando taint e redação (não expõe PII em claro nem confunde dados com instruções).
- [ ] A superfície suporta ligar um span a **eval** (`gen_ai.evaluation.result`) e ao replay, quando disponíveis, sem os reimplementar.

### Detalhes Técnicos

- Componentes: **superfície de trajectória** (novo, apresentação), **OBS** (fonte: árvore de spans OTel GenAI de AOS-077).
- Renderização de árvore a partir de spans (semconv GenAI); drill-down por span com atributos `gen_ai.usage.*`.
- Respeito por taint/redação na apresentação; ligação a eval/replay como navegação, não como cálculo.

### Testes Requeridos

- Teste de árvore: hierarquia de spans de um run com sub-agentes é renderizada correctamente.
- Teste de drill-down: expandir e inspeccionar uma tool call mostra tokens/custo/resultado.
- Teste de consumo: nenhuma captura/mutação de spans; leitura pura de AOS-077.
- Teste de taint/PII: conteúdo untrusted/PII apresentado sem expor PII em claro.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Visualização consome os spans de AOS-077 (sem capturar/mutar; ADR-010).
- [ ] Taint/redação respeitados na apresentação; sem PII em claro.
- [ ] Spans OTel de interacção da própria superfície; sem segredos.
- [ ] Documentação e ADR-010 referenciados.

### Handoff para Claude Code

```text
És o executor do ticket AOS-127 do AOS. Lê specs/EPIC-12 e tecnica/15 §10, mais
specs/EPIC-08 (AOS-077) e ADR-010.
Implementa a SUPERFÍCIE de visualização e DRILL-DOWN da trajectória do sub-agente.
Renderiza a ÁRVORE DE SPANS completa (invoke_agent → execute_tool → chat) de um run e
dos seus sub-agentes, com expandir/colapsar e inspecção de cada span (atributos,
tokens, custo em USD, resultado). CONSOME directamente os spans OTel de AOS-077 — NÃO
capturas, mutas nem re-emites spans, e não reimplementas o backend. Respeita taint e
redação na apresentação (nunca exponhas PII em claro nem confundas dados com
instruções). Liga um span a eval/replay quando disponível, como navegação, sem os
recalcular. Escreve testes de árvore, drill-down, consumo puro e taint/PII. Não
expandas escopo; abre PR com o template padrão.
```

---

## AOS-128 — Testes de UX/DX (usabilidade dos gates; anti-fadiga / override-rate)

| Campo | Valor |
|---|---|
| Epic | EPIC-12 — Experiência de Utilização e Controlo Humano (UX/DX) |
| Fase | 4 — UX e evolução |
| Tipo | chore |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-119, AOS-120, AOS-121, AOS-123, AOS-125 |
| Bloqueia | — |
| Responsável sugerido | QA |
| Documentos de referência | `tecnica/15_Experiencia_HITL_UX.md` §11, `specs/EPIC-11_Testes_Qualidade.md`, `specs/01_Engineering_Standards_e_Handoff.md`, ADR-013 |

### Contexto

A UX de governação não é acessória: um gate confuso produz *approval fatigue* e click-through — utilizadores experientes que auto-aprovam mais de 40% dos pedidos, anulando a supervisão que dizem proteger. A eficácia da Dimensão 6 mede-se, e a métrica-chave já existe: o **override-rate** medido em AOS-095. Este ticket estabelece a bateria de **testes de UX/DX** que valida a **usabilidade dos gates** (o preview é compreensível? a decisão é inequívoca?) e usa o override-rate como sinal **anti-fadiga** — um override-rate cronicamente alto indica que a superfície está a ser rubber-stamped, não que a política está errada. Consome métricas existentes; não cria enforcement.

### Objectivo

Estabelecer os testes de UX/DX do epic: testes de usabilidade dos gates (aprovação-de-acção, aprovação-de-plano, exaustão graciosa, autonomia progressiva) e a instrumentação/asserção anti-fadiga sobre o override-rate (AOS-095), integrados no harness de qualidade de EPIC-11.

### Critérios de Aceitação

- [ ] Existem **testes de usabilidade dos gates** para AOS-120 (approval-card), AOS-121 (aprovação-de-plano), AOS-123 (exaustão graciosa) e AOS-125 (autonomia): o preview é completo, as opções são inequívocas e a decisão fail-closed é respeitada.
- [ ] O **override-rate** (medido em AOS-095) é usado como **sinal anti-fadiga**: os testes asseguram que é exposto e que um limiar cronicamente alto é sinalizado como problema de superfície.
- [ ] Os testes cobrem a **paridade de superfície** (AOS-122): cards equivalentes nas três plataformas, degradação fail-closed nos canais sem capacidade.
- [ ] Os testes verificam **acessibilidade** básica das superfícies (contraste/rótulos/navegação por teclado nos canais que o suportam).
- [ ] A bateria integra-se no **harness de qualidade** de EPIC-11 e corre em CI; consome métricas existentes, sem criar enforcement próprio.

### Detalhes Técnicos

- Componentes: **QA/harness de UX** (novo), consumindo as superfícies de AOS-119–125 e a métrica `approval.override_rate` (AOS-095).
- Testes de usabilidade dirigidos (script de interacção) + asserções sobre completude do preview e clareza das opções.
- Anti-fadiga: asserção sobre exposição e limiar do override-rate; não altera a métrica, só a valida como sinal de superfície.
- Integração com EPIC-11 (gates de qualidade, CI).

### Testes Requeridos

- Teste de usabilidade por gate: preview completo e opções inequívocas em AOS-120/121/123/125.
- Teste anti-fadiga: override-rate exposto; limiar alto sinalizado como problema de superfície.
- Teste de paridade: cards equivalentes e degradação fail-closed (AOS-122).
- Teste de acessibilidade básica das superfícies.
- Teste de integração no harness de EPIC-11 / CI.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Bateria de UX/DX integrada no harness de EPIC-11 e a correr em CI.
- [ ] Override-rate consumido de AOS-095 como sinal anti-fadiga (sem criar enforcement).
- [ ] Cobertura de usabilidade, paridade e acessibilidade documentada.
- [ ] Sem segredos; scan limpo.

### Handoff para Claude Code

```text
És o executor do ticket AOS-128 do AOS. Lê specs/EPIC-12 e tecnica/15 §11, mais
specs/EPIC-11, specs/01 e ADR-013.
Estabelece os TESTES DE UX/DX do epic (chore de qualidade). Cria testes de USABILIDADE
DOS GATES para AOS-120 (approval-card), AOS-121 (aprovação-de-plano), AOS-123 (exaustão
graciosa) e AOS-125 (autonomia): preview completo, opções inequívocas, fail-closed
respeitado. Usa o OVERRIDE-RATE medido em AOS-095 como sinal ANTI-FADIGA — assegura que
é exposto e que um limiar cronicamente alto é sinalizado como problema de SUPERFÍCIE
(não recries a métrica nem enforcement). Cobre paridade de superfície (AOS-122) com
degradação fail-closed e acessibilidade básica. Integra a bateria no harness de EPIC-11
e no CI. Escreve os testes de usabilidade por gate, anti-fadiga, paridade,
acessibilidade e integração em CI. Não expandas escopo; abre PR com o template padrão.
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
