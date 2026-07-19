# Módulo `network`

Provisiona a rede de um **plano** — fronteira de isolamento dos componentes AOS. Instanciado
**uma vez por plano** (controlo e dados) pela raiz, materializando a separação topológica.

## Postura DEFAULT-DENY + egress allowlist (ADR-004)

Substitui o antigo binário `internal = true/false` por uma **allowlist de egress explícita**:

- **`egress_allowlist` vazia = deny-all**: a rede nasce `internal = true` (sem gateway, sem
  egress). É o default fail-closed — omitir configuração **nega**.
- **`egress_allowlist` não-vazia**: a rede continua fechada por omissão; cada CIDR autorizado
  fica registado como etiqueta `aos.egress.allow.<i>` para o egress-proxy / Model Gateway
  aplicar a jusante. A etiqueta `aos.egress.posture` regista `deny-all` ou `allowlist`.
- **Validação fail-closed**: rejeita rotas permissivas — `0.0.0.0/0`, `::/0` e qualquer prefixo
  `/0` — e CIDRs inválidos. A validação dispara em *input-time* (offline, antes do provider).

> Nota: o substrato Docker não filtra egress por CIDR; a allowlist é **propagada como
> metadado** para a camada de enforcement (egress-proxy). A rede em si permanece default-deny.

- **Idempotente**: um único `docker_network`; reexecutar `apply` não recria.
- **Destroy limpo**: `destroy` remove a rede após os módulos dependentes.

## Entradas

| Nome | Tipo | Descrição |
|---|---|---|
| `name` | string | Prefixo (ex.: `aos-dev-cp`). Rede fica `aos-dev-cp-net`. |
| `subnet` | string | CIDR da sub-rede. |
| `egress_allowlist` | list(string) | CIDRs autorizados a egress. Vazio = deny-all. Sem `/0`. |
| `labels` | map(string) | Etiquetas comuns. |

## Saídas

| Nome | Descrição |
|---|---|
| `network_name` | Nome da rede (usado pelos módulos do plano). |
| `network_id` | ID da rede. |
| `internal` | `true` quando deny-all (allowlist vazia). |
| `egress_posture` | `deny-all` ou `allowlist`. |
| `egress_allowlist` | CIDRs autorizados. |
