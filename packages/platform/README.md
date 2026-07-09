# `platform/` — Serviços de plataforma

Capacidades partilhadas e substituíveis por contrato (portas versionadas).

| Componente | Código | Responsabilidade (uma linha) | Epic |
|---|---|---|---|
| Memory Service | **MEM** | Memória episódica, semântica, procedural e de trabalho; contexto ≠ registo | EPIC-04 |
| Skill/Tool Registry | **REG** | Catálogo versionado de skills/tools/MCP com pin + hash + assinatura | EPIC-05 |
| Model Gateway | **GW** | Interface unificada a LLMs; identidade por principal; allowlist regional; roteamento cost/load-aware | EPIC-06 |
| Credential Broker + Vault | **BRK** | Troca token scoped do agente por credenciais downstream JIT server-side; o agente nunca vê o segredo (ADR-006) | EPIC-06/07 |

## Invariantes

- **Segredos só via Broker/Vault** (ADR-006): nenhum segredo em código, logs ou
  spans; tokens JIT com TTL curto e revogáveis. O `BRK` liga-se ao Vault
  provisionado pelo módulo `infra/modules/secrets`.
- **Coerência por contrato** (Princípio 8): modelo, memória e tools são
  substituíveis sem rearquitectura.
- **SemVer para artefactos comportamentais** (ADR-012): skills/schemas/prompts
  versionados no `REG`.

## Estrutura (a preencher pelos tickets)

```
platform/
  src/
  README.md
```

Referência técnica: [tecnica/04_Memoria_Persistencia.md](../../tecnica/04_Memoria_Persistencia.md),
[tecnica/05_Skill_Tool_Registry_Supply_Chain.md](../../tecnica/05_Skill_Tool_Registry_Supply_Chain.md),
[tecnica/06_Model_Gateway_Custos.md](../../tecnica/06_Model_Gateway_Custos.md).
