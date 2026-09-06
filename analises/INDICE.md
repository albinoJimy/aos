# Auditoria à Documentação — AOS (Agentic OS de Referência)

**Produto:** AOS — Agentic OS de Referência
**Método:** auditoria multi-agente — painel de 6 auditores especializados + contra-auditoria adversarial + síntese
**Âmbito:** conjunto `tecnica/` (00–11 + INDICE) e `specs/` (System Spec, Standards, EPIC-01..11, INDICE), mais `_BRIEF` e `_FONTE`
**Versão:** 1.0 · **Data:** Julho de 2026 · **Classificação:** Documento de Referência — Aberto

---

## Veredicto (v3 — confirmação pós-P0.1b)

**Prontidão documental: «Aprovado com reservas»** (subiu de *D2 — não implementável sem correcções*, na v1). **Score global de síntese: 6.9** (média das 6 dimensões = 7.1). **Constatações críticas: 0.**

> A 3ª auditoria — independente e com a contra-auditoria adversarial activa — **confirma a eliminação de todas as constatações críticas**. As 3 críticas da v1 e as 2 da v2 estão fechadas. O que resta é P1 (elevação, não bloqueio). Evolução completa: **[Comparacao_evolucao.md](Comparacao_evolucao.md)**.

## Scorecard (v1 → v2 → v3)

| # | Dimensão | v1 | v2 | v3 |
|---|----------|:--:|:--:|:--:|
| 1 | [Completude](01_Completude.md) | 6.5 | 7.5 | **7.5** |
| 2 | [Coerência interna](02_Coerencia_Interna.md) | 7.0 | 7.5 | **7.0** |
| 3 | [Rigor técnico](03_Rigor_Tecnico.md) | 6.5 | 7.0 | **7.0** |
| 4 | [Clareza](04_Clareza.md) | 7.5 | 7.0 | **7.5** |
| 5 | [Rastreabilidade](05_Rastreabilidade.md) | 7.0 | 6.5 | **6.5** |
| 6 | [Viabilidade de execução](06_Viabilidade_Execucao.md) | 7.5 | 6.5 | **7.0** |
| — | **Críticas** | **3** | 2 | **0** |

**Constatações v3:** 57, das quais **0 críticas**. Registo: [Registo_de_Constatacoes.md](Registo_de_Constatacoes.md). Baselines: [`v1_baseline/`](v1_baseline/) · [`v2_baseline/`](v2_baseline/).

## Auditoria Carta ↔ codebase (2026-07-24)

Executada sobre o working tree do AOS com 12 agentes especializados + 4 fiscais adversariais (workflow descrito em [`docs/runbooks/RB-Auditoria_Multiagente_CartavCodebase.md`](../docs/runbooks/RB-Auditoria_Multiagente_CartavCodebase.md)).

| Documento | Conteúdo |
|---|---|
| **[07_Relatorio_Auditoria_Multiagente.md](07_Relatorio_Auditoria_Multiagente.md)** | Veredicto global, sumário executivo, achados críticos/altos consolidados, refutações do contra-exame, implicações para o DoD da v1 e recomendações. |
| [phase1_findings_normalized.md](phase1_findings_normalized.md) | 62 achados normalizados da Fase 1 (evidência `caminho:linha` por dimensão). |

---

## Documentos desta auditoria

| Documento | Conteúdo |
|-----------|----------|
| **[00_Relatorio_Auditoria.md](00_Relatorio_Auditoria.md)** | Relatório consolidado: sumário executivo, metodologia, constatações por dimensão, transversais, registo priorizado, **plano de remediação (P0/P1/P2)** e veredicto de prontidão (escala D0–D4). |
| [01–06_*.md](.) | Um relatório por dimensão (score, pontos fortes, constatações, lacunas). |
| [Registo_de_Constatacoes.md](Registo_de_Constatacoes.md) | Todas as constatações ordenadas por severidade + riscos globais + constatações disputadas na contra-auditoria. |

## As 3 constatações críticas (resumo)

1. **O Reference Monitor fica invisível no grafo executável.** Rótulos de dependência errados em EPIC-02/03/07 fazem com que a invariante nuclear ("mediação total", AOS-003) não seja dependência real de nenhum ticket cross-epic — um executor pode construir o Runtime e a microVM **sem o RM merged**. *(Coerência + Viabilidade)*
2. **Gates de CI que validam artefactos inexistentes.** Os gates "contratos entre componentes" e "teste de política Rego/Cedar" são celebrados como força, mas há **0 contratos de interface e 0 políticas** no corpus — no dia 1 são no-ops ocos ou bloqueadores permanentes. *(Rigor + Completude)*
3. **Overclaims de segurança + conformidade não sustentada.** A retórica "arquitecturalmente impossível" sobre-promete garantias residuais e ancora uma alegação "EU AI Act por desenho" em que só o Art. 14 é citado. *(Rigor + Completude)*

O plano de remediação priorizado (P0/P1/P2) está na §6 do [relatório consolidado](00_Relatorio_Auditoria.md).

---

## Auditorias adversariais ao código

Método comum: lentes independentes → refutação com o ónus da prova invertido (*na dúvida, o achado
cai*) → medição executada. Âmbitos disjuntos e complementares.

| Documento | Âmbito | Data |
|---|---|---|
| **[09_Auditoria_RT_RM_Adversarial.md](09_Auditoria_RT_RM_Adversarial.md)** | **RT** (`kernel/agent-runtime`) e **RM** (`kernel/reference-monitor`) | 2026-09-02 |
| **[10_Auditoria_ORQ_SCH_PDP_Adversarial.md](10_Auditoria_ORQ_SCH_PDP_Adversarial.md)** | **Plano de controlo**: ORQ, SCH e PDP — o âmbito que a 09 §7 declarou por cobrir | 2026-09-03 |
| **[11_Auditoria_MEM_REG_GW_BRK_Adversarial.md](11_Auditoria_MEM_REG_GW_BRK_Adversarial.md)** | **Serviços de plataforma**: MEM, REG, GW e BRK — o âmbito que a 10 §7 deixou por cobrir. 32 hipóteses, 16 sobreviventes; o digest constante do `mcp_server` e a lacuna de governação que a 10 não tinha | 2026-09-04 |
| **[12_Auditoria_ES_SBX_Adversarial.md](12_Auditoria_ES_SBX_Adversarial.md)** | **Substrato**: ES (Event Store, incl. JetStream/NATS) e SBX (sandbox, isolamento e egress) — o âmbito que a 11 §7.2 deixou por medir. 42 hipóteses, 12 sobreviventes, mais 2 achados nascidos na refutação; inclui validação **experimental** do WAL | 2026-09-06 |

---

*Auditoria gerada por painel multi-agente sobre o conjunto documental AOS v1.0. Ver [Índice Técnico](../tecnica/INDICE.md) e [Índice do Backlog](../specs/INDICE.md).*
