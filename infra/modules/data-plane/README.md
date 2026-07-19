# Módulo `data-plane`

**Scaffold** do **plano de dados** (executa e regista) — AOS-098 / EPIC-10 §3.

Materializa a separação topológica plano-controlo/plano-dados e a **escala independente** do
plano de dados. Aloja como *placeholders* inertes:

| Recurso | Papel (tecnica/10 §3) |
|---|---|
| `worker` | Worker stateless que executa os passos |
| `microvm` | Nó do pool de microVMs pré-aquecidas (isolamento primário, ADR-004) |

Cria `worker_replicas` workers e `microvm_pool_size` nós de pool, attachados à rede do plano
de dados (default-deny), sem portas expostas e sem egress. O **Event Store** e o **audit WORM**
(também plano de dados) vivem no módulo `eventstore` e na cadeia de audit já existentes.

## Âmbito (fronteira de escopo)

Este módulo entrega **apenas topologia/scaffolding**. Fica **fora** de AOS-098:

- Workers stateless + estado particionado por *run* — **AOS-099**.
- Replicação interna do Event Store para além do módulo `eventstore` — **AOS-100**.
- Aquecimento/snapshot/restore do pool de microVMs — **AOS-103**.

Os *placeholders* provam a separação de planos e a escala parametrizada sem antecipar a
implementação. AOS-098 **bloqueia** estes tickets — prepara o terreno sem os concluir.

## Entradas

| Nome | Tipo | Descrição |
|---|---|---|
| `name` | string | Prefixo (ex.: `aos-dev`). |
| `network_name` | string | Rede do plano de dados. |
| `image` | string | Imagem placeholder (pinada). |
| `worker_replicas` | number | Workers stateless (1–50). Escala independente. |
| `microvm_pool_size` | number | Nós do pool (0–50). 0 = sem pool. |
| `labels` | map(string) | Etiquetas comuns (inclui `aos.plane=data`). |

## Saídas

| Nome | Descrição |
|---|---|
| `worker_replicas` | Nº de workers. |
| `microvm_pool_size` | Nº de nós do pool. |
| `worker_names` / `microvm_names` | Nomes dos containers placeholder. |
