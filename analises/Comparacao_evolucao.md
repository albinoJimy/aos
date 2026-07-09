# Evolução da Auditoria — v1 → v2 → v3

**Produto:** AOS — Agentic OS de Referência · **Data:** Julho de 2026 · **Classificação:** Documento de Referência — Aberto
**Método:** três passagens do mesmo painel multi-agente (6 auditores + contra-auditoria) sobre o corpus:
**v1** baseline · **v2** após remediação P0 · **v3** confirmação após P0.1b.

---

## 1. Scorecard nas três passagens

| Dimensão | v1 | v2 | v3 | Trajectória |
|----------|:--:|:--:|:--:|-------------|
| **Completude** | 6.5 | 7.5 | **7.5** | ⬆ estabilizou (docs 12/13/14). |
| **Coerência interna** | 7.0 | 7.5 | 7.0 | ⬆⬇ subiu com o fix de dependências; recuou por contagens de topo desactualizadas (já corrigidas). |
| **Rigor técnico** | 6.5 | 7.0 | **7.0** | ⬆ estabilizou (NFR/retórica/erros técnicos). |
| **Clareza** | 7.5 | 7.0 | **7.5** | ⬇⬆ recuperou ao fechar o defeito de rotulagem ticket→componente. |
| **Rastreabilidade** | 7.0 | 6.5 | 6.5 | ⬇ estável; criticals fechadas, mas falta a RTM (P1). |
| **Viabilidade de execução** | 7.5 | 6.5 | **7.0** | ⬇⬆ recuperou ao corrigir os rótulos residuais do EPIC-02. |
| **GLOBAL (síntese)** | **6.8** | **7.0** | **6.9** | Estável ~7; média das dimensões v3 = **7.1**. |
| **Constatações críticas** | **3** | 2 | **0** | ✅ **Zero críticas na v3.** |

> Nota sobre o número global: a síntese da v3 (6.9) é *mais conservadora* do que a média das dimensões (7.1) porque, ao contrário da v2, a **contra-auditoria da v3 completou** (a da v2 falhou por erro de API) e ponderou as lacunas P1 remanescentes. O sinal decisivo não é o decimal do global, mas a **eliminação de todas as constatações críticas**.

---

## 2. O que a v3 confirmou

A terceira passagem — independente e com a passagem adversarial de validação cruzada activa — confirma que **as remediações se mantêm**:

- ✅ **VIAB-01 (crítica v2) fechada:** 0 rótulos de dependência errados no EPIC-02 (tabela-resumo, notas e handoffs corrigidos). A v3 já não a reporta.
- ✅ **RAST-01 (crítica v2) fechada:** os docs 12/13/14 estão referenciados nos `Documentos relacionados` de 6 epics. A v3 nota que falta o back-link *por ticket* — mas isso é P1 (RTM), não crítico.
- ✅ **As 3 críticas originais (v1)** permanecem resolvidas (RM visível no grafo; gates com artefactos; retórica/NFR calibrados).
- ✅ **Gate de CI 2b** (lint de rótulos) presente para impedir a regressão.

Nenhuma dimensão tem constatações críticas na v3. O corpus passou de *«não implementável sem correcções»* (v1) para **«aprovado com reservas»** (v2/v3).

---

## 3. O que permanece aberto (P1 — elevação, não bloqueio)

As três auditorias convergem nos mesmos itens de elevação, agora sem nenhum bloqueador:

1. **UX/DX sem casa própria** — a 6ª dimensão de excelência ainda não tem documento técnico nem epic dedicados (contrato de superfície HITL, approval-card, paridade Slack/Telegram). *Recomendado: `tecnica/15_Experiencia_HITL_UX.md` + tickets.*
2. **RTM ausente** — sem IDs de requisito estáveis (`RF-`/`NFR-`) nem matrizes ADR×ticket / NFR×ticket; back-link técnico→ticket incompleto.
3. **STRIDE ausente** — o modelo OWASP LLM/ASI existe, mas falta a decomposição por fronteira de confiança que o `_BRIEF` pede.
4. **Contradição de latência residual** — ~20 critérios de aceitação ainda citam "< 15 ms" sem a decomposição por sub-passo já introduzida na fonte (re-propagação fina).

---

## 4. Veredicto final

O ciclo **auditar → remediar → re-auditar → confirmar** cumpriu o seu objectivo: **eliminou todas as constatações críticas** e elevou o corpus a *aprovado com reservas*, com um caminho P1 claro e não-bloqueador para atingir *«implementável e verificável»* (nível D3+).

Baselines preservadas: [`v1_baseline/`](v1_baseline/) · [`v2_baseline/`](v2_baseline/). Relatório corrente (v3): [`00_Relatorio_Auditoria.md`](00_Relatorio_Auditoria.md).
