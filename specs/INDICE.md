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
**189 tickets atómicos ratificados (AOS-001 a AOS-189)** em **17 epics**, mais duas epics em **proposta** (EPIC-18 e EPIC-19; AOS-190..225 e AOS-230..244), cada uma com critérios de
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
| **EPIC-13 Frontend** | Epic | Operacionalização da superfície humana (BFF, WebAuthn, WYSIWYS, SSE, a11y) | [EPIC-13_Frontend.md](EPIC-13_Frontend.md) | 856 |
| **EPIC-14 Integração e Composition-Root** | Epic | Composition-root, seams e ápice de enforcement composto (resolução da dívida PR-0) | [EPIC-14_Integracao_Composition_Root.md](EPIC-14_Integracao_Composition_Root.md) | 1099 |
| **EPIC-15 Nó `aos` Runtime Deployável** | Epic | Graduação de `cmd/aos-demo` para o nó `aos` de produção (CLI + API stdlib + SSE) | [EPIC-15_No_AOS_Runtime_Deployavel.md](EPIC-15_No_AOS_Runtime_Deployavel.md) | 705 |
| **EPIC-16 Autoridade de Identidade Real (D4)** | Epic | IdP OIDC, custódia externa de chave, binding humano↔NHI, attestation WebAuthn | [EPIC-16_Autoridade_Identidade_Real_D4.md](EPIC-16_Autoridade_Identidade_Real_D4.md) | 120 |
| **EPIC-17 Remediação Auditoria Multiagente v3** | Epic | Wiring, gates CI e reconciliação documental dos achados da auditoria Carta ↔ codebase | [EPIC-17_Remediacao_Auditoria_Multiagente_v3.md](EPIC-17_Remediacao_Auditoria_Multiagente_v3.md) | 389 |
| **EPIC-18 Remediação Auditoria Multiagente v4** *(proposta)* | Epic | Remediação dos achados da auditoria v4: wiring/imposição/veracidade do composition-root do nó; §8-bis (deferimentos + STRIDE) | [EPIC-18_Remediacao_Auditoria_Multiagente_v4.md](EPIC-18_Remediacao_Auditoria_Multiagente_v4.md) | 1750 |
| **EPIC-19 Planeador e Meta-Orchestração** *(proposta)* | Epic | Graduação do planeador (goal→DAG), meta-runs e gate como fronteira; deriva `tecnica/18` v1.0 (Ratificado) | [EPIC-19_Planeador_Meta_Orquestracao.md](EPIC-19_Planeador_Meta_Orquestracao.md) | 489 |
| **EPIC-20 Prontidão Agêntica: remediação + custo governado + ADR-021/022** *(proposta)* | Epic | Remediação dos achados F1–F16 do relatório de prontidão, billing token-only ligado ao nó, implementação dos ADR-021/022 (assumidos aprovados) | [EPIC-20_Prontidao_Agentica_Remediacao.md](EPIC-20_Prontidao_Agentica_Remediacao.md) | ~600 |
| **EPIC-21 Remediação dos defeitos da auditoria adversarial RT/RM** *(proposta)* | Epic | Os doze DEFEITOS (não as dívidas) apurados na auditoria adversarial do Plano de Execução; os quatro limites aceites foram para DEF-904..907. **Fechado** (AOS-288..304 — os doze mais cinco residuais que a remediação produziu), com UMA lacuna de evidência declarada: o teste ao vivo da AC2 de AOS-292 exige cluster. Encerramento em [`docs/reports/EPIC-21-encerramento-2026-09-03.md`](../docs/reports/EPIC-21-encerramento-2026-09-03.md) | [EPIC-21_Remediacao_Auditoria_RT_RM.md](EPIC-21_Remediacao_Auditoria_RT_RM.md) | ~380 |

**Total: 23 ficheiros referenciados, ~12.900+ linhas. Backlog ratificado/aceite: EPIC-01..17 (AOS-001..189). Propostas por ratificar: EPIC-18 (AOS-190..225), EPIC-19 (AOS-230..244) e EPIC-20 (AOS-245..278) e EPIC-21 (AOS-288..304).**

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
| EPIC-13 Frontend | AOS-129 – AOS-143 | 15 | Fase 5 |
| EPIC-14 Integração e Composition-Root | AOS-144 – AOS-162 | 19 | Fase 5 |
| EPIC-15 Nó `aos` Runtime Deployável | AOS-163 – AOS-173 | 11 | Fase 5 |
| EPIC-16 Autoridade de Identidade Real (D4) | AOS-174 – AOS-177 | 4 | Fase 5 |
| EPIC-17 Remediação Auditoria Multiagente v3 | AOS-178 – AOS-189 | 12 | Transversal |
| EPIC-18 Remediação Auditoria Multiagente v4 *(proposta)* | AOS-190 – AOS-225 | 36 | Transversal |
| EPIC-19 Planeador e Meta-Orchestração *(proposta)* | AOS-230 – AOS-244 | 15 | Fase 3/5 |
| EPIC-20 Prontidão Agêntica *(proposta)* | AOS-245 – AOS-278 | 34 | Transversal |
| **TOTAL ratificado** | **AOS-001 – AOS-189** | **189** | --- |
| **+ propostas** | **AOS-190–225, AOS-230–244, AOS-245–278** | **85** | --- |

> **Nota:** AOS-226–229 (continuação do D4 — OIDC do *issuer*/verificador) são trabalho entregue fora das epics listadas neste índice.

### 3.2 Por fase do roadmap (concentração-alvo)

| Fase | Foco | Epics concentrados |
|------|------|--------------------|
| **Fase 0 — Fundações** | Reference monitor, identidade, durabilidade, Event Store | EPIC-01, EPIC-02, parte de EPIC-04 |
| **Fase 1 — Fronteira de segurança** | microVM, egress, broker, supply-chain | EPIC-07, EPIC-05 |
| **Fase 2 — Governação & observabilidade** | policy-as-code, OTel, audit WORM, GDPR | EPIC-08, EPIC-09, parte de EPIC-06 |
| **Fase 3 — Escala & controlo** | admission global, backpressure, operação | EPIC-03, EPIC-06, EPIC-10 |
| **Fase 4 — UX & evolução** | steer/interrupt, L0–L5, SemVer + eval-gate | EPIC-09, EPIC-11, parte de EPIC-10 |
| **Fase 5 — Operacionalização** | composition-root, nó deployável, autoridade real, fronteira humana, remediação | EPIC-13, EPIC-14, EPIC-15, EPIC-16, EPIC-17, EPIC-18 *(proposta)* |
| **Fase 3/5 — Planeamento** | planeador produtivo, meta-runs, gate como fronteira | EPIC-19 *(proposta)* |

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
| **Specs (este conjunto)** | [INDICE.md](INDICE.md) | 20 epics (17 ratificados + EPIC-18/19/20 propostos) + backlog AOS-001..275 |

---

## 10. Aprovação e versionamento

Cada documento contém a sua tabela de aprovação (Arquitecto de Plataforma · Responsável de Segurança · Responsável de Produto).

| Versão | Data | Descrição | Autor |
|--------|------|-----------|-------|
| 1.0 | Julho 2026 | Emissão inicial: System Spec + Standards + 11 epics decompostos em 118 tickets AOS, alinhados com o conjunto técnico v1.0 e a síntese "O Agentic OS ideal". | Equipa AOS |
| 1.1 | Julho 2026 | Remediação P1 da auditoria: +EPIC-12 (Experiência HITL/UX, AOS-119–128). Total: 12 epics, 128 tickets. | Equipa AOS |
| 1.2 | Julho 2026 | Reconciliação documental P0: +EPIC-13..EPIC-17 (Frontend, Composition-Root, Nó `aos`, Autoridade de Identidade Real, Remediação Auditoria Multiagente v3). Total: 17 epics, AOS-001..AOS-189. | Equipa AOS |
| 1.3 | 2026-08-02 | Indexação das epics em proposta: +EPIC-18 (Remediação Auditoria v4, AOS-190..225) e +EPIC-19 (Planeador e Meta-Orchestração, AOS-230..244, deriva `tecnica/18` v1.0 ratificado). Nota sobre AOS-226..229 (continuação D4, fora das epics listadas). | Equipa AOS |
| 1.4 | 2026-08-08 | +EPIC-20 (Prontidão Agêntica: remediação dos achados F1–F18, billing token-only, ADR-021/022 assumidos aprovados; AOS-245..278, 34 tickets — inclui o desafio A5: fusível do keypool, clamp de `max_turns`, knobs de ingresso). Deriva `docs/reports/prontidao-modelos-agenticos.md` (consolidado) e os desafios A1–A5. | Equipa AOS |

---

*Fim do Índice do Backlog Executável. Ver também o [Índice Técnico](../tecnica/INDICE.md).*
