# Módulo `secrets`

Provisiona o cofre do **Credential Broker + Vault (BRK)** — onde vivem os segredos
para emissão de credenciais downstream JIT (ADR-006). O agente nunca vê o segredo.

- **dev** (`dev_mode = true`): Vault `-dev`, in-memory, unseal automático. O root
  token é **gerado em runtime** (`random_password`) e exposto como output
  `sensitive` — **nunca** entra num var-file nem no repositório. Descartável.
- **staging** (`dev_mode = false`): Vault server persistente (storage `file` em
  volume). **Arranca selado**: `vault operator init` + `unseal` são operação
  fora-de-banda; a IaC não coloca material secreto.
- **Idempotente / destroy limpo**: config injectada por `upload` (sem ficheiros no
  host); dados em volume removido no `destroy`.

> **Sem segredos no var-file** (DoD §3 / ADR-006). As `env/*.tfvars` só controlam
> `dev_mode`, imagem e porta. TLS está desativado no listener por simplicidade de
> ambiente — produção real exige TLS *(proposta pragmática de ambiente)*.

## Entradas

| Nome | Tipo | Descrição |
|---|---|---|
| `name` | string | Prefixo (ex.: `aos-dev`). |
| `network_name` | string | Rede do módulo `network`. |
| `image` | string | Imagem Vault pinada. |
| `dev_mode` | bool | `true` = dev in-memory; `false` = server persistente. |
| `api_port` | number | Porta da API no host. |
| `labels` | map(string) | Etiquetas comuns. |

## Saídas

| Nome | Descrição |
|---|---|
| `address` | Endereço da API do Vault — consumido por `platform/BRK`. |
| `dev_mode` | Se está em modo dev. |
| `dev_root_token` | Root token descartável (só dev; `sensitive`). |
