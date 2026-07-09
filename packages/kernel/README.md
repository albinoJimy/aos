# `kernel/` — Plano de execução

O coração de mediação e execução do AOS.

| Componente | Código | Responsabilidade (uma linha) | Epic |
|---|---|---|---|
| Reference Monitor | **RM** | Gate mandatório: toda a tool call atravessa-o (identidade, política, orçamento, egress, audit) antes de executar (ADR-002) | EPIC-01 |
| Agent Runtime | **RT** | Loop do agente (montar prompt → chamar modelo → despachar tools → verificar) sobre execução durável (ADR-001) | EPIC-02 |

## Invariantes

- **Mediação total**: nenhum caminho de código no monorepo executa uma tool sem
  passar pelo RM. O RM é o PEP; consulta o **PDP** (`control-plane/`).
- **Durabilidade ao passo**: cada efeito externo isolado em *activity* com
  *idempotency key* `f(run_id, step_id)` (ADR-001).
- **Untrusted não comanda**: tool results / web / memória entram marcados por
  *taint* e nunca autorizam acções (ADR-005).

## Estrutura (a preencher pelos tickets)

```
kernel/
  src/            # implementação (vazio — entregue por AOS-001.. / AOS-013..)
  README.md
```

Referência técnica: [tecnica/01_Reference_Monitor_Plano_Controlo.md](../../tecnica/01_Reference_Monitor_Plano_Controlo.md),
[tecnica/02_Agent_Runtime_Execucao_Duravel.md](../../tecnica/02_Agent_Runtime_Execucao_Duravel.md).
