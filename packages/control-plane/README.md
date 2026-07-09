# `control-plane/` — Plano de controlo

Decide **o quê**, **por que ordem** e **se é permitido**.

| Componente | Código | Responsabilidade (uma linha) | Epic |
|---|---|---|---|
| Orquestrador | **ORQ** | Decompõe objectivos em grafo de tarefas acíclico; delega a sub-agentes | EPIC-03 |
| Escalonador | **SCH** | Durable execution, leases/fencing, prioridade, backpressure, detecção de deadlock | EPIC-03 |
| Policy Decision Point | **PDP** | Avalia policy-as-code (Rego/OPA ou Cedar) por tool call; par do PEP (=RM) | EPIC-09 |

## Invariantes

- **Default-deny** (Princípio 7): o PDP nega por omissão; toda a decisão de
  autorização é policy-as-code versionada e assinada (ADR-011).
- **Admission control em tokens/$** (ADR-008): orçamento por árvore, não por
  iterações; token-bucket distribuído com reserva de headroom.
- **PDP p95 < 15 ms** (NFR-01): política compilada, avaliada em memória.

## Estrutura (a preencher pelos tickets)

```
control-plane/
  src/
  policies/       # Rego/Cedar versionado (default-deny), com testes de PDP
  README.md
```

Referência técnica: [tecnica/03_Orquestracao_Escalonamento.md](../../tecnica/03_Orquestracao_Escalonamento.md),
[tecnica/09_Governacao_Conformidade.md](../../tecnica/09_Governacao_Conformidade.md).
