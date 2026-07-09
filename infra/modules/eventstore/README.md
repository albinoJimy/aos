# Módulo `eventstore`

Provisiona o **Event Store (ES)** — NATS com JetStream, o log append-only que é a
fonte de verdade do AOS, com transporte push (ADR-007).

- **dev**: `cluster_size = 1` (single-node, arranque rápido).
- **staging**: `cluster_size >= 3` (cluster replicado full-mesh; sem SPOF).
- **Persistência**: JetStream em volume Docker por nó → `apply` idempotente,
  `destroy` limpo (o volume é removido com o container).
- **Exposição mínima**: só o nó 0 publica portas no host; os restantes comunicam
  apenas na rede interna do ambiente.

> Nota: consome as portas de cliente/monitorização a partir do nó 0. Um cluster
> real de produção usaria orquestração multi-host — aqui provisiona-se o suficiente
> para dev/staging locais *(proposta pragmática de ambiente, não desenho de produção)*.

## Entradas

| Nome | Tipo | Descrição |
|---|---|---|
| `name` | string | Prefixo (ex.: `aos-dev`). |
| `network_name` | string | Rede do módulo `network`. |
| `image` | string | Imagem NATS pinada. |
| `cluster_size` | number | Nº de nós (1 = dev; ≥3 = staging). |
| `client_port` | number | Porta de cliente do nó 0 (host). |
| `monitoring_port` | number | Porta de monitorização do nó 0 (host). |
| `store_dir` | string | Diretório de JetStream no container. |
| `labels` | map(string) | Etiquetas comuns. |

## Saídas

| Nome | Descrição |
|---|---|
| `client_url` | URL de cliente NATS (host) — consumido por `substrate/ES`. |
| `monitoring_url` | URL de monitorização HTTP. |
| `internal_client_urls` | URLs internos de todos os nós. |
| `cluster_size` / `node_names` | Metadados do cluster. |
