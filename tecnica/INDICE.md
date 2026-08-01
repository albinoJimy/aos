# Documentação Técnica — AOS (Agentic OS de Referência)

**Produto:** AOS — Agentic OS de Referência (blueprint de plataforma para correr, coordenar e governar agentes de IA)
**Natureza:** Produto/plataforma standalone (sem cliente institucional)
**Documento-fonte:** "O Agentic OS ideal — blueprint de referência" (`../_FONTE_agentic-os-ideal.md`)
**Versão do conjunto documental:** 1.0
**Data de emissão:** Julho de 2026
**Classificação:** Documento de Referência — Aberto

---

## 1. Propósito deste conjunto documental

Este conjunto materializa a **documentação técnica detalhada** do AOS, um desenho de referência para
um *Agentic OS* — a camada de plataforma que trata o agente de IA como cidadão de primeira classe
(processo→agente, RAM→contexto, syscall→tool call, IPC→mensagem agente-a-agente, kernel→runtime de
orquestração). Complementa a síntese de arquitectura (`_FONTE_agentic-os-ideal.md`) com documentos de
desenho por subsistema, decisões de arquitectura (ADRs), drivers não-funcionais, vista de qualidade,
topologia de operação e convenções de engenharia.

A tese que atravessa todos os documentos: um Agentic OS só é excelente se tornar as falhas
*arquitecturalmente impossíveis* — através de três fundações não-negociáveis: **Reference Monitor**
mandatório, **identidade não-humana por agente** com cadeia de delegação até um humano responsável, e
**execução durável ao nível do passo** sobre um Event Store replicado.

O conjunto destina-se a: arquitectos de plataforma; engenheiros de runtime, segurança, dados/memória,
observabilidade e governação; DevOps/SRE; responsáveis de produto; e revisão arquitectural e de segurança.

---

## 2. Índice de documentos

| Nº | Documento | Foco | Ficheiro | Linhas |
|----|-----------|------|----------|--------|
| 00 | **Arquitectura da Solução** | Âncora: C4; 5 camadas; control/data-plane; catálogo de componentes; 14 ADRs; NFRs; vista de qualidade; maturidade | [00_Arquitectura_Solucao.md](00_Arquitectura_Solucao.md) | 377 |
| 01 | **Reference Monitor e Plano de Controlo** | RM/PEP, PDP, policy-as-code, mediação total, allowlist default-deny | [01_Reference_Monitor_Plano_Controlo.md](01_Reference_Monitor_Plano_Controlo.md) | 270 |
| 02 | **Agent Runtime e Execução Durável** | Loop do agente, idempotência por passo, replay, máquina de estados durável, sagas | [02_Agent_Runtime_Execucao_Duravel.md](02_Agent_Runtime_Execucao_Duravel.md) | 214 |
| 03 | **Orquestração e Escalonamento** | Grafo de tarefas, leases, admission control global, backpressure | [03_Orquestracao_Escalonamento.md](03_Orquestracao_Escalonamento.md) | 216 |
| 04 | **Memória e Persistência** | 4 tipos de memória, contexto≠registo, Event Store, migrações expand/contract | [04_Memoria_Persistencia.md](04_Memoria_Persistencia.md) | 231 |
| 05 | **Skill/Tool Registry e Supply-chain** | Registry versionado, MCP, pin+hash+assinatura, tool set congelado | [05_Skill_Tool_Registry_Supply_Chain.md](05_Skill_Tool_Registry_Supply_Chain.md) | 209 |
| 06 | **Model Gateway e Custos** | Gateway unificado, OAuth multi-provedor, allowlist regional, cache-estável | [06_Model_Gateway_Custos.md](06_Model_Gateway_Custos.md) | 249 |
| 07 | **Segurança e Isolamento** | Threat model, microVM, taint/dual-LLM, credential broker, audit tamper-evident | [07_Seguranca_Isolamento.md](07_Seguranca_Isolamento.md) | 259 |
| 08 | **Observabilidade e Evals** | OTel GenAI semconv, trajectórias, replay, circuit breaker multi-sinal, audit WORM | [08_Observabilidade_Evals.md](08_Observabilidade_Evals.md) | 227 |
| 09 | **Governação e Conformidade** | Identidade NHI, PDP/PEP, GDPR/EU AI Act, L0–L5, responsabilização | [09_Governacao_Conformidade.md](09_Governacao_Conformidade.md) | 246 |
| 10 | **Topologia, Implantação e Operação** | Deployment, escala horizontal, DR por replay, runbooks | [10_Topologia_Implantacao_Operacao.md](10_Topologia_Implantacao_Operacao.md) | 271 |
| 11 | **Convenções de Engenharia e Evolução** | SemVer de artefactos comportamentais, governação da auto-modificação, provider abstraction | [11_Convencoes_Engenharia_Evolucao.md](11_Convencoes_Engenharia_Evolucao.md) | 231 |
| 12 | **Contratos de Interface** | Contratos de porta RM↔PDP, RT↔ES, RM↔BRK, GW↔provider, REG (schema request/response, erro, idempotência, SemVer) + política Rego de referência | [12_Contratos_de_Interface.md](12_Contratos_de_Interface.md) | 387 |
| 13 | **Modelo de Dados e Eventos** | Envelope de evento append-only, registo de audit hash-chain/WORM, schema de memória versionado, manifesto por trajectória | [13_Modelo_Dados_Eventos.md](13_Modelo_Dados_Eventos.md) | 318 |
| 14 | **Matriz de Conformidade (EU AI Act + GDPR)** | Artigo → controlo AOS → ticket; classificação de risco; responsabilidades do operador | [14_Matriz_Conformidade.md](14_Matriz_Conformidade.md) | 159 |
| 15 | **Experiência de Utilização e Controlo Humano (UX/DX)** | Superfície HITL out-of-band, approval-card, aprovação-de-plano, paridade Slack/Telegram, anti-fadiga SA-ROC | [15_Experiencia_HITL_UX.md](15_Experiencia_HITL_UX.md) | 289 |
| 16 | **Matriz de Rastreabilidade (RTM)** | Catálogo RF-/NFR- com IDs estáveis; matrizes ADR×ticket e NFR×ticket; back-link técnico→ticket | [16_Rastreabilidade_RTM.md](16_Rastreabilidade_RTM.md) | 234 |
| 17 | **Análise STRIDE** | Decomposição por fronteira de confiança (9 elementos × 6 categorias) → controlo → ADR → ticket | [17_Analise_STRIDE.md](17_Analise_STRIDE.md) | 268 |
| 18 | **Planeador de Objectivos e Meta-Orchestração** *(Ratificado, v1.0)* | PlanDocument untrusted, validação fail-closed, gate como fronteira, meta-runs e organizações efémeras; organizações persistentes marcadas *(proposta)* | [18_Planner_Meta_Orquestracao.md](18_Planner_Meta_Orquestracao.md) | 263 |

**Total: 19 documentos, ~4.890 linhas, 60 diagramas Mermaid.**

> Os documentos 12–14 foram acrescentados na remediação **P0** da auditoria (resolvem COMP-01, COMP-02, COMP-03): contratos de interface + política Rego, modelo de dados/eventos, e matriz de conformidade. Os documentos 15–17 foram acrescentados na remediação **P1**: o documento de UX/DX (a 6ª dimensão de excelência), a Matriz de Rastreabilidade (IDs RF-/NFR- + ADR×ticket) e a Análise STRIDE. O documento 18 foi **ratificado** (v1.0, 2026-08-02, após revisão adversarial multi-perspectiva): especifica o planeador real (goal→DAG) e a meta-orchestração dentro da forma congelada da Carta, marcando explicitamente o que exigiria emenda.

---

## 3. Como navegar este conjunto

### 3.1 Por perfil de leitor

| Perfil | Leitura recomendada (por ordem) |
|--------|---------------------------------|
| **Arquitecto de Plataforma** | 00 → 01 → 02 → 03 → 07 → 09 → 10 (visão integral) |
| **Engenheiro de Runtime** | 00 → 02 → 03 → 04 → 08 |
| **Engenheiro de Segurança** | 00 → 07 → 01 → 09 → 05 |
| **Engenheiro de Dados/Memória** | 00 → 04 → 02 → 08 |
| **Engenheiro de Observabilidade** | 00 → 08 → 02 → 10 |
| **Engenheiro de Governação/Conformidade** | 00 → 09 → 01 → 07 → 11 |
| **DevOps/SRE** | 00 → 10 → 03 → 08 → 07 |
| **Responsável de Produto** | 00 → ../_FONTE (síntese) → 09 → 11 |
| **Onboarding** | 00 → 02 → 01 → 07 → subsistema específico |

### 3.2 Por camada arquitectural

| Camada | Documentos |
|--------|------------|
| **Arquitectura global** | 00 |
| **Plano de controlo** | 01, 03, 18 |
| **Plano de execução** | 02 |
| **Serviços de plataforma** | 04, 05, 06 |
| **Segurança / substrato** | 07 |
| **Transversal (Obs & Gov)** | 08, 09 |
| **Operação** | 10 |
| **Engenharia / evolução** | 11 |

### 3.3 Por fase do roadmap

| Fase | Documentos relevantes |
|------|-----------------------|
| **Fase 0 — Fundações** | 00, 01, 02, 04 (Event Store) |
| **Fase 1 — Fronteira de segurança** | 07, 05, 06 (broker) |
| **Fase 2 — Governação & observabilidade** | 08, 09, 01 (PDP) |
| **Fase 3 — Escala & controlo** | 03, 06, 10 |
| **Fase 4 — UX & evolução** | 11, 09 (L0–L5) |

---

## 4. Hierarquia documental e dependências

```
                 _FONTE — O Agentic OS ideal (síntese)
                                │
                                ▼
                  00 Arquitectura da Solução
                                │
        ┌───────────────┬───────┴───────┬───────────────┐
        ▼               ▼               ▼               ▼
  01 Reference    02 Agent Runtime  03 Orquestração  04 Memória
     Monitor      (execução durável) e Escalonamento  e Persistência
        │               │               │               │
        └───────┬───────┴───────┬───────┴───────────────┘
                ▼               ▼
        05 Registry &     06 Model Gateway
          Supply-chain      e Custos
                │               │
                ▼               ▼
        07 Segurança      08 Observabilidade
          e Isolamento      e Evals
                │               │
                ▼               ▼
        09 Governação     10 Topologia,
          e Conformidade    Operação e DR
                            │
                            ▼
                  11 Convenções e Evolução
```

---

## 5. Decisões de arquitectura (ADRs) — síntese

Os 14 ADRs canónicos estão definidos no [00_Arquitectura_Solucao.md](00_Arquitectura_Solucao.md) §8 e referenciados por código ao longo de todos os documentos.

| ADR | Decisão | Doc principal |
|-----|---------|---------------|
| ADR-001 | Execução durável como primitivo | 02 |
| ADR-002 | Reference Monitor mandatório | 01 |
| ADR-003 | Identidade não-humana por agente | 09 |
| ADR-004 | Isolamento ao nível do kernel (microVM) | 07 |
| ADR-005 | Separação control/data-plane + taint | 07 |
| ADR-006 | Credential Broker com tokens JIT | 07 |
| ADR-007 | Event Store replicado | 04 |
| ADR-008 | Admission control global em tokens/$ | 03 |
| ADR-009 | Layout de prompt cache-estável | 06 |
| ADR-010 | Observabilidade OTel GenAI + audit WORM | 08 |
| ADR-011 | Policy-as-code + GDPR por desenho | 09 |
| ADR-012 | SemVer + eval-gate para auto-modificação | 11 |
| ADR-013 | Gates de risco SA-ROC + controlo bidireccional | 01/09 |
| ADR-014 | Taxonomia de autonomia L0–L5 | 09 |

---

## 6. Ligação com o conjunto de specs

Este conjunto técnico é a referência de desenho; o conjunto executável em [`../specs/`](../specs/INDICE.md)
decompõe-o em 118 tickets AOS-NNN organizados em 11 epics. Cada documento técnico mapeia para o epic
homónimo (ex.: `tecnica/07` ↔ `specs/EPIC-07`).

---

## 7. Aprovação, versionamento e manutenção

- **Aprovação:** cada documento contém a sua tabela de aprovação (Arquitecto de Plataforma · Responsável de Segurança · Responsável de Produto).
- **Versionamento:** SemVer para documentos formais (MAJOR = reformulação de arquitectura; MINOR = novo capítulo; PATCH = correcções).
- **Manutenção:** divergências entre documento e código/desenho são bugs documentais; novas decisões arquitecturais registadas como ADRs.

| Versão | Data | Descrição | Autor |
|--------|------|-----------|-------|
| 1.0 | Julho 2026 | Emissão inicial do conjunto técnico (12 documentos) derivado da síntese "O Agentic OS ideal". | Equipa AOS |
| 1.1 | Julho 2026 | Remediação P0 da auditoria: +3 documentos (12 Contratos de Interface, 13 Modelo de Dados e Eventos, 14 Matriz de Conformidade). Total: 15 documentos. | Equipa AOS |

---

*Fim do Índice Técnico. Ver também o [Índice do Backlog Executável](../specs/INDICE.md).*
