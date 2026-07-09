# `substrate/` — Log & substrato

A fonte de verdade durável e a fronteira de isolamento. Não conhece as camadas acima.

| Componente | Código | Responsabilidade (uma linha) | Epic |
|---|---|---|---|
| Event Store | **ES** | Log append-only replicado, fonte de verdade; transporte push (NATS/Redis/Postgres) (ADR-007) | EPIC-01 |
| Sandbox Substrate | **SBX** | Isolamento ao nível do kernel por execução; rede default-deny; egress allowlist (ADR-004) | EPIC-07 |

## Invariantes

- **Append-only, fonte de verdade** (ADR-007): o `ES` substitui o SQLite
  single-writer; replicado, com transporte push. O cliente do `ES` liga-se à
  infra provisionada por `infra/modules/eventstore`.
- **Isolamento primário por microVM/gVisor** (ADR-004): FS read-only + overlay
  efémero; seccomp; jails só como defesa secundária.
- **Egress default-deny** (Princípio 7): rede negada por omissão, allowlist
  explícita. Corresponde ao módulo `infra/modules/network`.

## Estrutura (a preencher pelos tickets)

```
substrate/
  src/
  README.md
```

Referência técnica: [tecnica/04_Memoria_Persistencia.md](../../tecnica/04_Memoria_Persistencia.md),
[tecnica/07_Seguranca_Isolamento.md](../../tecnica/07_Seguranca_Isolamento.md).
