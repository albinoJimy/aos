# Módulo `network`

Provisiona a rede do ambiente — fronteira de isolamento dos componentes AOS.

- **Egress default-deny**: com `internal = true`, a rede não tem gateway para a
  Internet (staging endurecido). Em dev, `internal = false` para conveniência de
  desenvolvimento. Corresponde ao ADR-004 / Princípio 7.
- **Idempotente**: um único `docker_network`; reexecutar `apply` não recria.
- **Destroy limpo**: `destroy` remove a rede após os módulos dependentes.

## Entradas

| Nome | Tipo | Descrição |
|---|---|---|
| `name` | string | Prefixo (ex.: `aos-dev`). Rede fica `aos-dev-net`. |
| `subnet` | string | CIDR da sub-rede. |
| `internal` | bool | `true` = sem egress. |
| `labels` | map(string) | Etiquetas comuns. |

## Saídas

| Nome | Descrição |
|---|---|
| `network_name` | Nome da rede (usado pelos módulos `eventstore`/`secrets`). |
| `network_id` | ID da rede. |
| `internal` | Se a rede é interna. |
