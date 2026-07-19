# Módulo `control-plane`

**Scaffold** do **plano de controlo** (decide) — AOS-098 / EPIC-10 §3.

Materializa a separação topológica plano-controlo/plano-dados e a **escala independente**
do plano de controlo. Aloja os quatro componentes de decisão como *placeholders* inertes:

| Componente | Papel (tecnica/10 §3) |
|---|---|
| `orq` | Orquestrador — grafo de tarefas acíclico |
| `sch` | Escalonador — leases, fencing, prioridade |
| `adm` | Admission control global — token-bucket distribuído |
| `pdp` | Policy Decision Point — Rego/Cedar versionado |

Cria `replicas` instâncias por componente (`aos-<env>-cp-<role>-<i>`), attachadas à rede do
plano de controlo (default-deny), sem portas expostas e sem egress.

## Âmbito (fronteira de escopo)

Este módulo entrega **apenas topologia/scaffolding**. A **lógica interna** de cada componente
**não** é de AOS-098:

- ORQ/SCH/ADM — detalhe em `specs/EPIC-03`.
- PDP — detalhe em ADR-011.

Os *placeholders* provam a separação de planos, a etiquetagem de soberania e a escala
parametrizada sem antecipar a implementação.

## Entradas

| Nome | Tipo | Descrição |
|---|---|---|
| `name` | string | Prefixo (ex.: `aos-dev`). |
| `network_name` | string | Rede do plano de controlo. |
| `image` | string | Imagem placeholder (pinada). |
| `replicas` | number | Réplicas por componente (1–10). Escala independente. |
| `labels` | map(string) | Etiquetas comuns (inclui `aos.plane=control`). |

## Saídas

| Nome | Descrição |
|---|---|
| `roles` | Lista de componentes de decisão. |
| `replicas` | Réplicas por componente. |
| `component_names` | Nomes dos containers placeholder. |
