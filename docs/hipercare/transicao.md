# Relatório de Transição para Operação em Regime — AOS (AOS-108)

> **Molde (template).** Preenche-se no encerramento do hipercare, gerado a partir do
> `hipercare.HipercareReport` da janela. O relatório **só** é válido se
> `report.CanExit() == true` — o gate fail-closed de critérios de saída (AC1..AC5). As
> métricas DORA e as acções de acompanhamento fecham a AC6. Sem segredos nem PII.

| Campo | Valor |
|---|---|
| Ticket | AOS-108 (FECHO do EPIC-10) |
| Board | `{{board}}` |
| Janela de hipercare | `{{window_start}}` → `{{window_end}}` |
| Versão do relatório | `{{version}}` (esquema `hipercare.ReportVersion`) |
| Veredicto de saída | **`{{CanExit}}`** |

---

## 1. Critérios de saída (gate fail-closed)

| Critério | Estado | Fonte |
|---|---|---|
| S1 — SLOs canónicos sustentados | `{{slos_sustained}}` | conformidade de SLOs (§2) |
| S2 — Runbooks validados com MTTR | `{{runbooks_validated}}` | MTTR por runbook (§3) |
| S3 — Alertas calibrados | `{{alerts_calibrated}}` | calibração de alertas (§4) |
| S4 — DR revalidado (RPO/RTO) | `{{dr_revalidated}}` | game day de DR (§5) |

> Se algum critério for `false`, `ExitCriteria.Missing` lista o que falta e o hipercare
> **não** encerra (fail-closed). A tabela acima só fica toda `true` quando `CanExit==true`.

## 2. Conformidade de SLOs na janela (AC2)

Projecção do `OperationalSnapshot` (AOS-104). Um SLO é cumprido de forma sustentada sse
**avaliado** (`Samples>0`) **e** `Met` **e** sem breach — um SLO não avaliado **não**
conta (anti-vacuidade).

| SLI | Plano | Valor | SLO | Amostras | Avaliado | Cumprido | Breach | Gate |
|---|---|---|---|---|---|---|---|---|
| `control_plane_availability` | control | … | 0,999 | … | … | … | … | ✔ |
| `mediation_overhead_p95` | control | … | < 15 ms | … | … | … | … | ✔ |
| `sandbox_cold_start_p95` | data | … | < 125 ms | … | … | … | … | ✔ |
| `cache_hit_rate` | data | … | > 0,80 | … | … | … | … | ✔ |
| `replay_fidelity` | data | … | 1,00 | … | … | … | … | ✔ |
| `headroom_tokens` | control | … | ≥ 1 | … | … | … | … | — |
| `audit_worm_integrity` | data | … | 1,00 | … | … | … | … | — |

## 3. MTTR por runbook (AC3)

Cada runbook canónico RB-01..RB-05 (AOS-106) validado em incidente real/simulado, com
MTTR medido (> 0 — anti-vacuidade).

| Runbook | Título | Validado | Real/Simulado | MTTR | Incidente |
|---|---|---|---|---|---|
| RB-01 | Colapso de rate limit | … | … | … | … |
| RB-02 | Zumbi cross-host | … | … | … | … |
| RB-03 | Esgotamento de orçamento | … | … | … | … |
| RB-04 | Falha de PDP | … | … | … | … |
| RB-05 | Rollback de auto-modificação | … | … | … | … |

## 4. Calibração de alertas (AC4)

| Métrica | Valor |
|---|---|
| Versão da config de alertas | `{{alert_config_version}}` |
| Taxa de falsos-positivos — **antes** | `{{fp_before}}` |
| Taxa de falsos-positivos — **depois** | `{{fp_after}}` (tem de ser ≤ antes) |
| Override-rate (specs/01 §9) | `{{override_rate}}` |
| Gate escape rate (specs/01 §9) | `{{gate_escape_rate}}` |

**Ajustes de limiar/janela aplicados** (`ThresholdChange`): _lista dos alertas
recalibrados com base no ruído observado (ex.: alargar `sustained_windows` de p95
ruidoso)._

## 5. Game day de DR revalidado (AC5)

Composição de `dr.GameDayEvidence` (AOS-102). Revalidado sse
`Passed && RPOWithin && RTOWithin`.

| Métrica | Medido | Alvo | Dentro |
|---|---|---|---|
| RPO | `{{rpo_window}}` | `{{rpo_target}}` (≤ 1 min) | `{{rpo_within}}` |
| RTO | `{{rto}}` | `{{rto_target}}` (≤ 30 min) | `{{rto_within}}` |
| Fidelidade de replay | 100% | 100% | ✔ |
| Efeitos duplicados | 0 | 0 | ✔ |
| Veredicto | **`{{passed}}`** | — | — |

## 6. Métricas operacionais DORA (AC6, specs/01 §9)

| Métrica | Valor | Alvo indicativo |
|---|---|---|
| **MTTR** | `{{mttr}}` | ↓ tendência |
| **Change failure rate** | `{{change_failure_rate}}` | < 15% |
| **Deploy frequency** | `{{deploy_frequency_per_week}}` / semana | ↑ com estabilidade |
| Lead time (opcional) | `{{lead_time}}` | ↓ tendência |

## 7. Acções de acompanhamento (AC6)

Ficam abertas para operação em regime (`FollowUpAction`, com dono):

| ID | Dono | Acção | Prazo | Prioridade |
|---|---|---|---|---|
| `{{id}}` | `{{owner}}` | `{{summary}}` | `{{due_by}}` | `{{priority}}` |

## 8. Aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| SRE / DevOps | | | |
| Responsável de Segurança | | | |
| Arquitecto de Plataforma | | | |

---

## Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Molde inicial de transição (AOS-108) | Equipa AOS |
