# Backlog Executável — AOS (Agentic OS de Referência)

**Produto:** AOS — Agentic OS de Referência
**Natureza:** Produto/plataforma standalone (sem cliente institucional)
**Documento-fonte:** "O Agentic OS ideal — blueprint de referência" (`../_FONTE_agentic-os-ideal.md`)
**Referência técnica:** conjunto [`../tecnica/`](../tecnica/INDICE.md)
**Versão do conjunto:** 1.0
**Data de emissão:** Julho de 2026
**Classificação:** Documento de Referência — Aberto

---

## 1. Propósito

Este conjunto materializa o **backlog executável** do AOS. Decompõe a totalidade do trabalho em
**128 tickets atómicos (AOS-001 a AOS-128)**, organizados em **12 epics**, cada um com critérios de
aceitação SMART, Definition of Done, prompt de handoff para o **Claude Code (CLI)** e gates de qualidade.

O backlog é a interface operacional entre o conjunto técnico (`../tecnica/`) e a execução. Cada ticket é
uma unidade de trabalho que um developer pode aceitar, implementar com assistência do Claude Code, validar
e fechar de forma independente.

---

## 2. Estrutura do conjunto

| Documento | Tipo | Foco | Ficheiro | Linhas |
|-----------|------|------|----------|--------|
| **00 System Spec** | Foundation | Visão executiva, capacidades, modelo de domínio, ADRs, fases, epics, KPIs/SLOs | [00_System_Spec.md](00_System_Spec.md) | 332 |
| **01 Engineering Standards e Handoff** | Foundation | DoR/DoD, gates CI/CD, template PR, prompt mestre Claude Code | [01_Engineering_Standards_e_Handoff.md](01_Engineering_Standards_e_Handoff.md) | 268 |
| **EPIC-01 Fundações e Plano de Controlo** | Epic | Reference Monitor, Event Store, identidade, PDP | [EPIC-01_Fundacoes_Plano_Controlo.md](EPIC-01_Fundacoes_Plano_Controlo.md) | 747 |
| **EPIC-02 Agent Runtime e Execução Durável** | Epic | Loop, idempotência, replay, máquina de estados, sagas | [EPIC-02_Agent_Runtime_Execucao_Duravel.md](EPIC-02_Agent_Runtime_Execucao_Duravel.md) | 871 |
| **EPIC-03 Orquestração e Escalonamento** | Epic | Grafo de tarefas, admission control, backpressure | [EPIC-03_Orquestracao_Escalonamento.md](EPIC-03_Orquestracao_Escalonamento.md) | 745 |
| **EPIC-04 Memória e Persistência** | Epic | 4 tipos de memória, contexto≠registo, migrações | [EPIC-04_Memoria_Persistencia.md](EPIC-04_Memoria_Persistencia.md) | 766 |
| **EPIC-05 Registry e Supply-chain** | Epic | Registry versionado, MCP, pin+hash+assinatura | [EPIC-05_Registry_Supply_Chain.md](EPIC-05_Registry_Supply_Chain.md) | 812 |
| **EPIC-06 Model Gateway e Custos** | Epic | Gateway, OAuth multi-provedor, roteamento, cache | [EPIC-06_Model_Gateway_Custos.md](EPIC-06_Model_Gateway_Custos.md) | 689 |
| **EPIC-07 Segurança e Isolamento** | Epic | microVM, taint, credential broker, egress default-deny | [EPIC-07_Seguranca_Isolamento.md](EPIC-07_Seguranca_Isolamento.md) | 781 |
| **EPIC-08 Observabilidade e Evals** | Epic | OTel spans, replay, circuit breaker, audit WORM | [EPIC-08_Observabilidade_Evals.md](EPIC-08_Observabilidade_Evals.md) | 674 |
| **EPIC-09 Governação e Conformidade** | Epic | Policy-as-code, L0–L5, GDPR, HITL, ratificação | [EPIC-09_Governacao_Conformidade.md](EPIC-09_Governacao_Conformidade.md) | 833 |
| **EPIC-10 Topologia, Operação e DR** | Epic | Deployment, dashboards, runbooks, backup/DR | [EPIC-10_Topologia_Operacao_DR.md](EPIC-10_Topologia_Operacao_DR.md) | 616 |
| **EPIC-11 Testes e Qualidade** | Epic | Testes, eval harness, carga, golden-sets, red-team | [EPIC-11_Testes_Qualidade.md](EPIC-11_Testes_Qualidade.md) | 776 |
| **EPIC-12 Experiência HITL/UX** | Epic | Superfície de controlo, approval-card, aprovação-de-plano, paridade Slack/Telegram, anti-fadiga | [EPIC-12_Experiencia_HITL_UX.md](EPIC-12_Experiencia_HITL_UX.md) | 781 |

**Total: 14 ficheiros, ~9.690 linhas, 128 tickets.**

---

## 3. Inventário de tickets

### 3.1 Por epic

| Epic | Range | N.º Tickets | Fases predominantes |
|------|-------|-------------|---------------------|
| EPIC-01 Fundações e Plano de Controlo | AOS-001 – AOS-012 | 12 | Fase 0 |
| EPIC-02 Agent Runtime e Execução Durável | AOS-013 – AOS-024 | 12 | Fase 0 |
| EPIC-03 Orquestração e Escalonamento | AOS-025 – AOS-034 | 10 | Fase 3 |
| EPIC-04 Memória e Persistência | AOS-035 – AOS-044 | 10 | Fase 0–2 |
| EPIC-05 Registry e Supply-chain | AOS-045 – AOS-054 | 10 | Fase 1 |
| EPIC-06 Model Gateway e Custos | AOS-055 – AOS-063 | 9 | Fase 2–3 |
| EPIC-07 Segurança e Isolamento | AOS-064 – AOS-075 | 12 | Fase 1 |
| EPIC-08 Observabilidade e Evals | AOS-076 – AOS-086 | 11 | Fase 2–3 |
| EPIC-09 Governação e Conformidade | AOS-087 – AOS-097 | 11 | Fase 2–4 |
| EPIC-10 Topologia, Operação e DR | AOS-098 – AOS-108 | 11 | Fase 3–4 |
| EPIC-11 Testes e Qualidade | AOS-109 – AOS-118 | 10 | Fase 0–4 |
| EPIC-12 Experiência HITL/UX | AOS-119 – AOS-128 | 10 | Fase 4 |
| **TOTAL** | **AOS-001 – AOS-128** | **128** | --- |

### 3.2 Por fase do roadmap (concentração-alvo)

| Fase | Foco | Epics concentrados |
|------|------|--------------------|
| **Fase 0 — Fundações** | Reference monitor, identidade, durabilidade, Event Store | EPIC-01, EPIC-02, parte de EPIC-04 |
| **Fase 1 — Fronteira de segurança** | microVM, egress, broker, supply-chain | EPIC-07, EPIC-05 |
| **Fase 2 — Governação & observabilidade** | policy-as-code, OTel, audit WORM, GDPR | EPIC-08, EPIC-09, parte de EPIC-06 |
| **Fase 3 — Escala & controlo** | admission global, backpressure, operação | EPIC-03, EPIC-06, EPIC-10 |
| **Fase 4 — UX & evolução** | steer/interrupt, L0–L5, SemVer + eval-gate | EPIC-09, EPIC-11, parte de EPIC-10 |

---

## 4. Como usar o backlog com o Claude Code

### 4.1 Fluxo padrão

```
1. Developer seleciona ticket pronto (DoR cumprido) na sprint actual
2. Abre o ficheiro do epic (ex.: specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md)
3. Localiza a secção do ticket (ex.: ## AOS-016: Replay determinístico resume-from-step)
4. Lê o ticket + documentos técnicos referenciados (tecnica/NN)
5. Verifica que dependências estão merged em main
6. Cola o prompt mestre do Claude Code (ver 01_Engineering_Standards §Prompt mestre)
7. Claude Code implementa, escreve testes, executa gates locais
8. Developer revê, abre PR "AOS-NNN: <título>" com template
9. Code review + CI verde (gates incl. teste de política, replay, eval-gate) → merge
10. Deploy staging → smoke → aprovação → deploy prod → tag SemVer
```

### 4.2 Gates de qualidade específicos do domínio

Além dos gates clássicos (build/lint/test/SAST/SCA/image scan), o AOS acrescenta gates próprios,
detalhados em [01_Engineering_Standards_e_Handoff.md](01_Engineering_Standards_e_Handoff.md):
**teste de política (PDP)**, **teste de replay determinístico**, **teste de idempotência por passo** e
**eval-gate** (admission control para artefactos comportamentais auto-modificados).

---

## 5. Critérios universais

### 5.1 Definition of Ready (DoR)
Critérios de aceitação SMART; dependências resolvidas ou listadas; estimativa (XS/S/M/L; XL proibido);
documentos técnicos identificados; riscos e responsável sugerido atribuídos.

### 5.2 Definition of Done (DoD)
Critérios de aceitação verificados; cobertura adequada; **idempotência por passo verificada**;
**replay determinístico testado**; **toda tool call mediada pelo Reference Monitor**; **spans OTel GenAI
adicionados**; **políticas com teste (PDP)**; **eval-gate verde** para artefactos comportamentais; sem
segredos; código revisto; CI verde em todos os gates.

Detalhes: [01_Engineering_Standards_e_Handoff.md](01_Engineering_Standards_e_Handoff.md).

---

## 6. Princípios de execução

1. Não expandir escopo silenciosamente — parar e consultar o Arquitecto de Plataforma.
2. Bug descoberto durante a implementação → novo ticket, não arrastar para o actual.
3. Ambiguidade → confirmar antes de assumir.
4. Branch única por ticket: `feature/AOS-NNN-<slug>`.
5. Commits atómicos (Conventional Commits: `feat(AOS-016): ...`).
6. **Idempotência primeiro** — toda mutação relevante idempotente (key = f(run_id, step_id)).
7. **Mediação sempre** — nenhuma tool call fora do Reference Monitor.
8. **Observabilidade desde o código** — span/metric OTel GenAI faz parte da DoD.
9. **Segurança não-negociável** — segredos no vault, egress default-deny, audit para mutações.
10. **Evolução com rede** — auto-modificação só via eval-gate + ratificação assinada.

---

## 7. Mapa de dependências (caminho crítico)

```
              Fase 0 — Fundações
              ──────────────────
  AOS-001 (bootstrap) ─► AOS-002 (Event Store) ─► AOS-009 (barramento)
       │                      │
       ▼                      ▼
  AOS-003 (Reference Monitor) ─► AOS-004 (PDP) ─► AOS-007 (allowlist)
  AOS-005 (identidade) ─► AOS-006 (delegação)
  AOS-008 (orçamento hierárquico) ─► AOS-012 (esqueleto control-plane)
       │
       ▼
  AOS-013 (loop) ─► AOS-014 (idempotência) ─► AOS-015 (checkpoint) ─► AOS-016 (replay)
  AOS-017 (máquina de estados) ─► AOS-018 (lease/fencing) ─► AOS-020 (sagas)

              Fase 1 — Fronteira de segurança
              ───────────────────────────────
  AOS-064 (microVM) ─► AOS-065 (pool snapshot) ─► AOS-067 (egress default-deny)
  AOS-069 (taint) + AOS-070 (credential broker) + AOS-072 (audit WORM)
  AOS-045..048 (registry + pin/hash/assinatura)

              Fase 2 — Governação & observabilidade
              ─────────────────────────────────────
  AOS-076 (OTel) ─► AOS-077 (spans sub-agente) ─► AOS-079 (replay) ─► AOS-080 (circuit breaker)
  AOS-087 (PDP/PEP enforcement) ─► AOS-088 (policy versionada) ─► AOS-091..093 (GDPR)

              Fase 3 — Escala & controlo
              ──────────────────────────
  AOS-027 (admission global) ─► AOS-028 (headroom) ─► AOS-030 (backpressure)
  AOS-055..060 (gateway + cache-estável) ; AOS-098..102 (topologia + DR)

              Fase 4 — UX & evolução
              ──────────────────────
  AOS-023 (paused/steer) ; AOS-089/090 (L0–L5 + promoção) ; AOS-096 (ratificação)
  AOS-114 (eval harness + golden-sets) ─► AOS-115 (trace-diffing)
```

Detalhes das dependências em cada ticket (campos "Dependências" e "Bloqueia").

---

## 8. Métricas operacionais do backlog

| # | Métrica | Alvo |
|---|---------|------|
| 1 | Velocity por sprint | A calibrar |
| 2 | Lead time por ticket (P50) | ≤ 5 dias úteis |
| 3 | Taxa de retrabalho | < 10% |
| 4 | Cobertura em componentes críticos (RM, Runtime, PDP) | ≥ 90% |
| 5 | **Eval-pass-rate de auto-modificações** | 100% antes de prod |
| 6 | **Cache-hit-rate de prompt (SLI)** | > 80% |
| 7 | **Overhead de mediação (RM) p95** | < 15 ms |
| 8 | Taxa de falha do CI | < 15% |
| 9 | Aderência ao DoD | 100% (gate obrigatório) |

---

## 9. Ligações entre conjuntos

| Conjunto | Localização | Propósito |
|----------|-------------|-----------|
| Síntese-fonte | [`../_FONTE_agentic-os-ideal.md`](../_FONTE_agentic-os-ideal.md) | Blueprint autoritativo "O Agentic OS ideal" |
| Técnica | [`../tecnica/INDICE.md`](../tecnica/INDICE.md) | 18 docs de desenho por subsistema (inclui 12–17 das remediações P0/P1) |
| **Specs (este conjunto)** | [INDICE.md](INDICE.md) | 12 epics + 128 tickets executáveis |

---

## 10. Aprovação e versionamento

Cada documento contém a sua tabela de aprovação (Arquitecto de Plataforma · Responsável de Segurança · Responsável de Produto).

| Versão | Data | Descrição | Autor |
|--------|------|-----------|-------|
| 1.0 | Julho 2026 | Emissão inicial: System Spec + Standards + 11 epics decompostos em 118 tickets AOS, alinhados com o conjunto técnico v1.0 e a síntese "O Agentic OS ideal". | Equipa AOS |
| 1.1 | Julho 2026 | Remediação P1 da auditoria: +EPIC-12 (Experiência HITL/UX, AOS-119–128). Total: 12 epics, 128 tickets. | Equipa AOS |

---

*Fim do Índice do Backlog Executável. Ver também o [Índice Técnico](../tecnica/INDICE.md).*
