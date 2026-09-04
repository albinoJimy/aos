# _BRIEF — Fonte de Verdade Canónica do Conjunto Documental AOS

> **Este ficheiro é obrigatório para todos os agentes que escrevem documentos.** Fixa nomes,
> decisões, listas e convenções para que os ~25 documentos sejam consistentes entre si.
> A fonte técnica autoritativa é `_FONTE_agentic-os-ideal.md` (a síntese "O Agentic OS ideal").
> Idioma: **português europeu (PT-PT)** — "arquitectura", "governação", "utilização", "objectivo".

---

## 1. Identidade do produto

- **Produto:** **AOS — Agentic OS de Referência** (runtime de referência deployável para correr, coordenar e governar agentes de IA).
- **Natureza:** para a **v1**, um **runtime/nó `aos` deployável** que se instala e corre, hospedando *runs* de agentes sob uma cadeia de governaça real. A visão de longo prazo de *blueprint/plataforma standalone* vive no `_FONTE_agentic-os-ideal.md` e só muda por emenda da Carta (`specs/00_AOS_Carta.md`). Genérico e reutilizável.
- **Versão do conjunto:** 1.0 · **Data:** Julho de 2026 · **Classificação:** Documento de Referência — Aberto.
- **Documento-fonte:** "O Agentic OS ideal — blueprint de referência" (`_FONTE_agentic-os-ideal.md`).
- **Tese central:** um Agentic OS só é excelente se tornar as falhas *arquitecturalmente impossíveis* — via três fundações não-negociáveis: **reference monitor** mandatório, **identidade por agente** com delegação até um humano responsável, e **execução durável ao nível do passo**. *(«arquitecturalmente impossível» é objectivo de desenho — eliminação estrutural do caminho de falha com risco residual gerido, não garantia absoluta.)*

### Bloco de metadados (colocar no topo de CADA documento, como tabela)

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | `<tipo>` — `<título do documento>` |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `<links relativos relevantes>` |

---

## 2. Modelo em camadas (canónico)

Governação e Observabilidade **envolvem** tudo; entre elas e o substrato está o kernel de mediação.

| Camada | Componentes |
|---|---|
| **Transversal** | Governação & Learning (PDP, políticas, identidade, RBAC); Observabilidade & Evals (spans OTel, custo, replay, audit WORM) |
| **Plano de controlo** | Orquestrador; Escalonador; Policy Decision Point (PDP) |
| **Plano de execução** | Agent Runtime (o loop); Reference Monitor (PEP — gate obrigatório de toda a tool call) |
| **Serviços de plataforma** | Memory Service; Skill/Tool Registry; Model Gateway; Credential Broker + Vault |
| **Log & substrato** | Event Store replicado (append-only, transporte push); Sandbox Substrate (microVM Firecracker/gVisor, egress default-deny) |

### Catálogo de componentes (usar SEMPRE estes nomes)

| Código | Componente | Responsabilidade (uma linha) |
|---|---|---|
| RM | **Reference Monitor** | Gate mandatório: toda a tool call atravessa-o (identidade, política, orçamento, egress, audit) antes de executar |
| RT | **Agent Runtime** | O loop do agente (montar prompt → chamar modelo → despachar tools → verificar) sobre execução durável |
| ORQ | **Orquestrador** | Decompõe objectivos em grafo de tarefas acíclico; delega a sub-agentes |
| SCH | **Escalonador** | Durable execution, leases/fencing, prioridade, backpressure, detecção de deadlock |
| PDP | **Policy Decision Point** | Avalia policy-as-code (Rego/OPA ou Cedar) por tool call; par do PEP (=RM) |
| MEM | **Memory Service** | Memória episódica, semântica, procedural e de trabalho; contexto ≠ registo |
| REG | **Skill/Tool Registry** | Catálogo versionado de skills/tools/MCP com pin + hash + assinatura |
| GW | **Model Gateway** | Interface unificada a LLMs (estilo LiteLLM); identidade por principal; allowlist regional; roteamento cost/load-aware |
| BRK | **Credential Broker + Vault** | Troca token scoped do agente por credenciais downstream JIT server-side; o agente nunca vê o segredo |
| ES | **Event Store** | Log append-only replicado, fonte de verdade; transporte push (NATS/Redis/Postgres) |
| SBX | **Sandbox Substrate** | Isolamento ao nível do kernel por execução; rede default-deny; egress allowlist |
| OBS | **Observabilidade & Evals** | Trajectória completa em OTel GenAI semconv; replay determinístico; audit hash-chain + WORM |
| GOV | **Governação & Learning** | Identidade não-humana, cadeia de delegação, L0–L5, conformidade, eval-gate de auto-modificação |

---

## 3. Decisões de Arquitectura — núcleo fundacional (ADR-001 a ADR-014)

Referenciar por código. Cada documento técnico relevante deve citar os ADRs que o afectam.

> **Âmbito desta tabela, e onde está o inventário.** Estas catorze são o núcleo que enquadrou a
> v1, com o **enunciado** de cada uma — é isso que aqui se fixa, e é por isso que a tabela não
> muda: reescrever um enunciado histórico seria um defeito, não uma actualização (uma decisão que
> muda exige ADR novo, ou um ADR de supersessão explícito).
>
> **Quantos ADRs existem, e em que estado está cada um, NÃO se lê aqui.** Lê-se no registo
> canónico [`docs/adr/README.md`](docs/adr/README.md), que é de onde os gates `rtm` e `ref-lint`
> derivam a lista (AOS-317). Do ADR-015 em diante as decisões existem só lá e em ficheiro
> próprio, **deliberadamente não repetidas nesta secção**: uma terceira cópia da lista seria uma
> terceira coisa a envelhecer em silêncio, que foi exactamente o defeito que AOS-317 fechou — a
> §4 da RTM cobria dezanove ADRs por literal escrito à mão, contra um registo de vinte e três, e
> o glossário mandava o leitor a esta secção para uma lista que ela nunca teve.
>
> Quem escreve documentos: para **citar a letra** de uma destas catorze, esta tabela serve; para
> saber **que ADRs há** ou se um está em vigor, o registo.

| ADR | Decisão | Racional resumido |
|---|---|---|
| ADR-001 | **Execução durável como primitivo** | Idempotência por passo (key=f(run_id,step_id)), checkpoint intra-iteração, replay resume-from-step, efeitos externos isolados em *activities* (Temporal/Restate/DBOS ou contrato explícito). |
| ADR-002 | **Reference Monitor mandatório** | Nenhum caminho de código chama tools directamente; mediação total torna segurança/governação/observabilidade transversais. |
| ADR-003 | **Identidade não-humana por agente** | Token scoped/time-bound codifica (utilizador, agente) numa cadeia de delegação on-behalf-of que termina num humano; autoridade = utilizador ∩ classe. |
| ADR-004 | **Isolamento ao nível do kernel** | microVM (Firecracker/Kata) ou gVisor como fronteira primária; FS read-only + overlay efémero; seccomp; jails só como defesa secundária. |
| ADR-005 | **Separação control/data-plane + taint** | Conteúdo untrusted (tool results, web, memória, schemas MCP) é dados, nunca instruções (dual-LLM/CaMeL); mitiga prompt injection (OWASP LLM01). |
| ADR-006 | **Credential Broker com tokens JIT** | Segredos no vault; broker injecta credenciais downstream server-side, TTL curto, revogáveis. |
| ADR-007 | **Event Store replicado** | Substitui SQLite single-writer (SPOF/tecto de throughput) por log replicado append-only com transporte push. |
| ADR-008 | **Admission control global em tokens/$** | Orçamento por árvore em tokens e custo (não iterações), token-bucket distribuído sobre TPM/RPM real, circuit breaker, reserva de headroom no admit. |
| ADR-009 | **Layout de prompt cache-estável** | Prefixo imutável (system + tool set congelado no run) + tail append-only; compressão só em checkpoints assíncronos; cache-hit-rate como SLI. |
| ADR-010 | **Observabilidade OTel GenAI + audit WORM** | Trajectória completa como árvore de spans (semconv GenAI); replay determinístico; audit hash-chain + WORM separado de diagnósticos efémeros. |
| ADR-011 | **Policy-as-code + GDPR por desenho** | PDP/PEP com Rego/OPA ou Cedar versionado e assinado; minimização, TTL, redação de PII e crypto-shredding (Art. 17); soberania por board. |
| ADR-012 | **SemVer + eval-gate para auto-modificação** | Skills/prompts/schemas versionados; auto-modificação passa por staging → eval-gate (golden-set) → canary → ratificação assinada → prod, com rollback atómico. |
| ADR-013 | **Gates de risco SA-ROC + controlo bidireccional** | Tiering safe/gray/danger; steer/interrupt (pausar→corrigir→retomar); gate de aprovação-de-plano; timeout fail-closed; override-rate medido. |
| ADR-014 | **Taxonomia de autonomia L0–L5** | Oversight proporcional ao impacto; promoção baseada em fiabilidade medida (ex.: erro <2% por 30 dias); demoção automática em anomalia. |

---

## 4. Drivers não-funcionais (alvos canónicos)

| Driver | Alvo | Mecanismo |
|---|---|---|
| Durabilidade de execução | Entrega *at-least-once* com idempotência downstream (0 efeitos **observáveis** duplicados) | Idempotency key por passo propagada e honrada pelo serviço externo; saga de compensação (ADR-001) |
| Disponibilidade do plano de controlo | 99,9% | ES replicado; workers stateless; sem SPOF (ADR-007) |
| Cold-start de sandbox | < 125 ms (restore 5–30 ms) | microVM snapshot + pool pré-aquecido (ADR-004) |
| Latência de avaliação do PDP p95 | < 15 ms | Política compilada avaliada em memória (ADR-002/011) |
| Overhead total de mediação por *tool call* | orçamento decomposto por sub-passo (PDP + CAS de admissão + broker->vault + append ao ES + egress/DNS) | Alvo agregado a ratificar por benchmark |
| Cache-hit-rate de prompt | > 80% (SLI) | Layout cache-estável (ADR-009) |
| Fidelidade de replay | 100% dos passos reproduzíveis | Captura de inputs não-determinísticos + hash de prompt (ADR-010) |
| Isolamento de segredos | Agente nunca vê segredo downstream | Credential broker JIT (ADR-006) |
| Conformidade | GDPR/EU AI Act por desenho | Crypto-shredding, PDP, HITL efectivo (ADR-011/013) |
| Segurança de auto-evolução | 0 auto-modificações não avaliadas em prod | Eval-gate como admission control (ADR-012) |

---

## 5. Vista de qualidade (7 dimensões de excelência)

Toda análise de qualidade organiza-se por estas dimensões (a mesma taxonomia do painel):
**Arquitectura · Segurança · Escalabilidade · Observabilidade · Governação · Experiência de utilização (UX/DX) · Manutenção evolutiva.**

---

## 6. Modelo de maturidade (M0–M4)

| Nível | Nome | Marca |
|---|---|---|
| M0 | Ad-hoc | for-loop + tool calls, sem estado durável ("chatbot com plugins") |
| M1 | Recuperável | run-ID, event log, máquina de estados; crashes sobrevivem |
| M2 | Mediado | reference monitor físico, identidade por agente, microVM + broker |
| M3 | Governado | policy-as-code, L0–L5, audit WORM, GDPR, eval-gate de auto-modificação |
| M4 | Auto-evolutivo seguro | durable execution distribuída, admission global, SemVer, promoção por fiabilidade |

Roadmap por fases: **Fase 0** Fundações · **Fase 1** Fronteira de segurança · **Fase 2** Governação & observabilidade · **Fase 3** Escala & controlo · **Fase 4** UX & evolução.

---

## 7. Conjunto TÉCNICA (pasta `tecnica/`) — documentos canónicos

| Nº | Ficheiro | Foco |
|---|---|---|
| 00 | `00_Arquitectura_Solucao.md` | Solution Architecture; C4; camadas; control/data-plane; ADRs; NFRs; vista de qualidade; riscos; roadmap; glossário |
| 01 | `01_Reference_Monitor_Plano_Controlo.md` | RM/PEP, PDP, policy-as-code, mediação total, plano de controlo |
| 02 | `02_Agent_Runtime_Execucao_Duravel.md` | Loop do agente, durable execution, idempotência, replay, máquina de estados |
| 03 | `03_Orquestracao_Escalonamento.md` | ORQ+SCH, grafo de tarefas, leases/fencing, admission control, backpressure |
| 04 | `04_Memoria_Persistencia.md` | MEM (episódica/semântica/procedural/trabalho), contexto≠registo, Event Store |
| 05 | `05_Skill_Tool_Registry_Supply_Chain.md` | REG, MCP, SemVer, pin+hash+assinatura, supply-chain |
| 06 | `06_Model_Gateway_Custos.md` | GW, LiteLLM, OAuth multi-provedor, allowlist regional, roteamento, custos |
| 07 | `07_Seguranca_Isolamento.md` | Threat model, SBX/microVM, taint, BRK, dual-LLM, egress |
| 08 | `08_Observabilidade_Evals.md` | OTel GenAI, trajectórias, replay, circuit breaker multi-sinal, audit WORM, evals |
| 09 | `09_Governacao_Conformidade.md` | Identidade NHI, PDP/PEP, GDPR/EU AI Act, L0–L5, responsabilização |
| 10 | `10_Topologia_Implantacao_Operacao.md` | Deployment, escala horizontal, DR, runbooks, observação operacional |
| 11 | `11_Convencoes_Engenharia_Evolucao.md` | Versionamento, governação da auto-modificação, eval-gates, padrões de código |
| 12 | `12_Contratos_de_Interface.md` | Contratos de porta (RM↔PDP, RT↔ES, RM↔BRK, GW↔provider, REG) + política Rego de referência |
| 13 | `13_Modelo_Dados_Eventos.md` | Envelope de evento, audit hash-chain/WORM, schema de memória, manifesto por trajectória |
| 14 | `14_Matriz_Conformidade.md` | Matriz EU AI Act + GDPR → controlo → ticket |
| 15 | `15_Experiencia_HITL_UX.md` | Superfície HITL, approval-card, aprovação-de-plano, paridade Slack/Telegram, anti-fadiga (P1) |
| 16 | `16_Rastreabilidade_RTM.md` | Catálogo RF-/NFR-; matrizes ADR×ticket e NFR×ticket; back-link técnico→ticket (P1) |
| 17 | `17_Analise_STRIDE.md` | STRIDE por fronteira de confiança (9 elementos × 6 categorias) (P1) |
| — | `INDICE.md` | Índice do conjunto técnico (escrito pelo orquestrador) |

Estrutura interna de CADA doc técnico (secções numeradas): **1. Introdução** (Propósito, Âmbito, Audiência, Definições) · **2. Princípios/decisões aplicáveis (ADRs)** · **secções de conteúdo em profundidade** · **Vista de qualidade** (as dimensões relevantes) · **Riscos e mitigações** · **Glossário** · **Tabela de aprovação**. Usar diagramas **Mermaid** (mínimo 2 por doc; docs de arquitectura 3–5).

---

## 8. Conjunto SPECS (pasta `specs/`) — backlog executável

| Ficheiro | Tipo | Foco |
|---|---|---|
| `00_System_Spec.md` | Foundation | Visão executiva, capacidades, modelo de domínio, ADRs, fases, epics, NFRs, KPIs/SLOs, glossário |
| `01_Engineering_Standards_e_Handoff.md` | Foundation | DoR/DoD, gates CI/CD, template PR, prompt mestre Claude Code |
| `EPIC-01_Fundacoes_Plano_Controlo.md` | Epic | Reference Monitor, Event Store replicado, identidade por agente |
| `EPIC-02_Agent_Runtime_Execucao_Duravel.md` | Epic | Loop, durable execution, idempotência, replay, máquina de estados |
| `EPIC-03_Orquestracao_Escalonamento.md` | Epic | Grafo de tarefas, leases, admission control, backpressure |
| `EPIC-04_Memoria_Persistencia.md` | Epic | Tipos de memória, contexto≠registo, migrações expand/contract |
| `EPIC-05_Registry_Supply_Chain.md` | Epic | Registry versionado, MCP, pin+hash+assinatura |
| `EPIC-06_Model_Gateway_Custos.md` | Epic | Gateway, OAuth multi-provedor, roteamento, custos |
| `EPIC-07_Seguranca_Isolamento.md` | Epic | microVM, taint, credential broker, egress default-deny |
| `EPIC-08_Observabilidade_Evals.md` | Epic | OTel spans, replay, circuit breaker, audit WORM, evals |
| `EPIC-09_Governacao_Conformidade.md` | Epic | Policy-as-code, L0–L5, GDPR, HITL, ratificação |
| `EPIC-10_Topologia_Operacao_DR.md` | Epic | Deployment, dashboards, alertas, runbooks, backup/DR |
| `EPIC-11_Testes_Qualidade.md` | Epic | Testes, eval harness, carga, golden-sets, gates de qualidade |
| `EPIC-12_Experiencia_HITL_UX.md` | Epic | Superfície de controlo HITL, approval-card, aprovação-de-plano, paridade Slack/Telegram, anti-fadiga (P1) |
| `INDICE.md` | Índice | Inventário de tickets, dependências, métricas (escrito pelo orquestrador) |

### Esquema de tickets: **AOS-NNN** (ranges por epic)

| Epic | Range |
|---|---|
| EPIC-01 | AOS-001 – AOS-012 |
| EPIC-02 | AOS-013 – AOS-024 |
| EPIC-03 | AOS-025 – AOS-034 |
| EPIC-04 | AOS-035 – AOS-044 |
| EPIC-05 | AOS-045 – AOS-054 |
| EPIC-06 | AOS-055 – AOS-063 |
| EPIC-07 | AOS-064 – AOS-075 |
| EPIC-08 | AOS-076 – AOS-086 |
| EPIC-09 | AOS-087 – AOS-097 |
| EPIC-10 | AOS-098 – AOS-108 |
| EPIC-11 | AOS-109 – AOS-118 |
| EPIC-12 | AOS-119 – AOS-128 |

### Estrutura de CADA ficheiro EPIC
1. Bloco de metadados (tabela). 2. **Visão do Epic**. 3. **Critérios de Saída do Epic** (checkboxes). 4. **Tabela Resumo de Tickets** (ID, Título, Tipo, Estimativa, Prioridade, Dependências). 5. Um bloco por ticket **AOS-NNN** com:
- Tabela de metadados (Epic, Fase, Tipo, Prioridade, Estimativa, Dependências, Bloqueia, Responsável sugerido, Documentos de referência).
- **Contexto** · **Objectivo** · **Critérios de Aceitação** (checkboxes SMART) · **Detalhes Técnicos** (ficheiros/componentes) · **Testes Requeridos** · **Definition of Done** (checkboxes) · **Handoff para Claude Code** (prompt sugerido em bloco de código).

Tipos: `feature`/`chore`/`spike`/`fix`. Estimativas: XS/S/M/L (XL proibido). Prioridades: P0/P1/P2. Responsáveis sugeridos (perfis genéricos): Arquitecto de Plataforma, Engenheiro de Runtime, Engenheiro de Segurança, Engenheiro de Dados/Memória, Engenheiro de Observabilidade, Engenheiro de Governação, DevOps/SRE, QA.

---

## 9. Convenções transversais

- **Idioma:** PT-PT. Termos técnicos consagrados podem ficar em inglês (reference monitor, taint, durable execution, prefix caching, eval-gate) — explicar na 1ª ocorrência.
- **Cross-references:** usar caminhos relativos, ex.: `tecnica/07_Seguranca_Isolamento.md`, `specs/EPIC-07_Seguranca_Isolamento.md`. Referir ADRs por código (ADR-005).
- **Fidelidade:** basear-se no `_FONTE_agentic-os-ideal.md` e neste brief. Onde algo não estiver na fonte e for necessário, marcar com *(proposta)* ou fundamentar em boa prática — nunca contradizer os ADRs.
- **Diagramas Mermaid:** válidos. Em `sequenceDiagram` NUNCA usar `;` dentro de mensagens; usar aspas em nós com parênteses `A["texto (detalhe)"]`.
- **Tabela de aprovação** (fim de cada doc), genérica standalone:

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

- **Controlo de versões** (fim de cada doc): tabela `| Versão | Data | Descrição | Autor |` com linha `1.0 | Julho 2026 | Emissão inicial | Equipa AOS`.
