# Plano de Hipercare — AOS (EPIC-10 / AOS-108)

| Campo | Valor |
|---|---|
| Ticket | AOS-108 — Hipercare e operacionalização (FECHO do EPIC-10) |
| Tipo | chore (operacionalização e afinação; **sem alterar o comportamento do sistema**) |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §7–9, `specs/01_Engineering_Standards_e_Handoff.md` §9, `specs/EPIC-10` |
| Harness de relatório | `packages/platform/hipercare` (gate fail-closed `CanExit`) |
| Versão | 1.0 — Julho 2026 |

> **Natureza.** O hipercare é operação vigiada de perto entre a entrega do EPIC-10 e a
> operação em regime. É **afinação** — calibrar limiares de alerta, medir MTTR, revalidar
> DR — **nunca** alteração de lógica. Todo o veredicto de encerramento é produzido pelo
> gate fail-closed `hipercare.HipercareReport.CanExit`, que **compõe** (não reimplementa)
> os contratos de AOS-102/104/105/106.

---

## 1. Duração

| Parâmetro | Valor |
|---|---|
| **Duração base** | **14 dias corridos** de operação vigiada. |
| Janela de conformidade de SLO | os SLOs canónicos têm de ser cumpridos de forma **sustentada** durante toda a janela (não instantâneo). |
| Extensão | +7 dias por cada critério de saída não cumprido ao dia 14 (o hipercare **não** encerra sobre ausência de dados nem sobre um critério em falha). |
| Encerramento | apenas quando `CanExit == true` (todos os critérios de saída cumpridos) **e** o relatório de transição é aprovado por um revisor. |

## 2. Escalões de resposta

Durante o hipercare a supervisão é reforçada. Os escalões seguem a severidade dos alertas
de AOS-105 (o agrupamento/supressão por plano de `GroupAndSuppress` mantém o padrão
sistémico visível — a supressão reduz páginas, nunca esconde o incidente).

| Escalão | Gatilho | Resposta | Prazo-alvo de reacção |
|---|---|---|---|
| **N1 — On-call SRE** | Alerta `WARNING` (cold-start p95, cache-hit, headroom sustentado) | Diagnóstico pelo runbook referenciado na `Route.Runbook`; mitigação. | 15 min |
| **N2 — Engenharia de plataforma** | Alerta `CRITICAL` (plano de controlo em baixo, overhead p95, headroom colapso/esgotamento, fidelidade de replay) | Runbook RB-01/RB-03/RB-04/RB-05; se DR necessário, PROC-DR. | 30 min (dentro do RTO-alvo) |
| **N3 — Segurança/Governação** | `AuditWORMIntegrityBroken` (quebra da hash-chain) | Escalar segurança + PROC-DR (recuperação por replay determinista). | Imediato |
| **N4 — Ponte de arquitectura** | Critério de saída reprovado ao fim da janela | Revisão de causa-raiz; decisão de extensão do hipercare. | 1 dia útil |

Todo o alerta encaminha para um runbook (invariante não-órfão de AOS-106,
`runbooks.ValidateAgainstAlerts`) — nenhum escalão fica sem procedimento.

## 3. Critérios de saída (explícitos, fail-closed)

O hipercare **só** encerra quando **TODOS** os critérios abaixo estão cumpridos. O gate
`CanExit` é fail-closed: qualquer critério em falha, ou qualquer ausência de dados,
**impede** o encerramento e a lista `ExitCriteria.Missing` enumera o que falta.

| # | Critério de saída | Fonte (composição) | Regra de cumprimento | AC |
|---|---|---|---|---|
| **S1** | **SLOs canónicos cumpridos de forma sustentada** | `otelgenai.OperationalSnapshot` (AOS-104) projectado sobre a janela | Cada SLO de saída **avaliado** (`Samples>0`), **cumprido** (`Met`) e **sem breach**; **nenhum** painel do snapshot em breach. **Anti-vacuidade**: um SLO com `Samples==0` **não** conta como cumprido. | AC2 |
| **S2** | **Runbooks validados com MTTR** | `runbooks.CanonicalIDs` (AOS-106) | Cada RB-01..RB-05 validado em incidente real/simulado **com MTTR medido (> 0)**. MTTR não medido **não** conta. | AC3 |
| **S3** | **Alertas calibrados** | `otelgenai.OperationalAlertConfig` (AOS-105) | Calibração conduzida; taxa de falsos-positivos **depois ≤ antes** (reduz ou mantém o ruído, nunca agrava); `override-rate` e `gate escape rate` medidos. | AC4 |
| **S4** | **DR revalidado (RPO/RTO)** | `dr.GameDayEvidence` (AOS-102) | Game day **repetido** com `Passed && RPOWithin && RTOWithin`. | AC5 |

### 3.1 Os SLOs canónicos de saída (S1)

| SLO | Alvo | SLI (`otel-genai`) | Direcção |
|---|---|---|---|
| Disponibilidade do plano de controlo | **99,9%** | `control_plane_availability` | ≥ |
| Overhead de mediação p95 | **< 15 ms** | `mediation_overhead_p95` | ≤ |
| Cold-start de sandbox p95 | **< 125 ms** | `sandbox_cold_start_p95` | ≤ |
| Cache-hit-rate de prompt | **> 80%** | `cache_hit_rate` | ≥ |
| Fidelidade de replay | **100%** | `replay_fidelity` | ≥ |

> Os dois SLIs canónicos restantes do catálogo (headroom de tokens, integridade do audit
> WORM) aparecem no relatório de conformidade; o **breach** de qualquer painel avaliado
> — incluindo estes — impede o encerramento (fail-closed).

## 4. Instrumentação e evidência

- **Conformidade de SLOs (S1)** — `hipercare.SLOConformanceFromSnapshot(window, snapshot)`
  projecta o snapshot renderizado pelo catálogo de AOS-104 sobre os dados da janela.
- **MTTR por runbook (S2)** — um `hipercare.RunbookValidation` por RB-01..RB-05, com o
  MTTR medido no exercício (game-day ou incidente real).
- **Calibração (S3)** — `hipercare.AlertCalibration` com falsos-positivos antes/depois,
  `override-rate`, `gate escape rate` e os `ThresholdChange` aplicados.
- **DR (S4)** — `hipercare.DRRevalidation` embebe a `dr.GameDayEvidence` do game day
  repetido no hipercare.
- **Relatório** — `hipercare.HipercareReport` agrega tudo, é JSON-serializável
  (round-trip reproduzível) e produz o veredicto `CanExit`.

## 5. Encerramento

1. Ao dia 14, gerar o `HipercareReport` da janela.
2. Correr `report.ExitCriteria()`. Se `CanExit == false`, publicar `Missing`, accionar o
   escalão N4 e **estender** o hipercare (§1) — não encerrar.
3. Se `CanExit == true`, produzir o **relatório de transição** (`docs/hipercare/transicao.md`)
   com as métricas DORA (MTTR, change failure rate, deploy freq.) e as acções de
   acompanhamento (`FollowUpAction`).
4. Aprovação por um revisor → transição formal para operação em regime.

---

## Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Emissão inicial (AOS-108, fecho do EPIC-10) | Equipa AOS |
