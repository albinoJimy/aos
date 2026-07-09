# `packages/` — Código do AOS

Monorepo dividido por **camadas do modelo canónico** (ver
[_BRIEF.md](../_BRIEF.md) §2). Cada pacote agrupa componentes do catálogo canónico
pelo seu papel na arquitectura. **Esta entrega é apenas esqueleto**: estrutura,
fronteiras e READMEs. Nenhuma lógica de negócio é implementada aqui — cada
componente é entregue pelos tickets `AOS-NNN` dos epics respectivos.

| Pacote | Camada canónica | Componentes (catálogo) | Epics de origem |
|---|---|---|---|
| [`kernel/`](kernel/) | Plano de execução | **RM** (Reference Monitor / PEP), **RT** (Agent Runtime) | EPIC-01, EPIC-02 |
| [`control-plane/`](control-plane/) | Plano de controlo | **ORQ** (Orquestrador), **SCH** (Escalonador), **PDP** (Policy Decision Point) | EPIC-03, EPIC-09 |
| [`platform/`](platform/) | Serviços de plataforma | **MEM** (Memory Service), **REG** (Skill/Tool Registry), **GW** (Model Gateway), **BRK** (Credential Broker + Vault) | EPIC-04, EPIC-05, EPIC-06 |
| [`substrate/`](substrate/) | Log & substrato | **ES** (Event Store), **SBX** (Sandbox Substrate) | EPIC-01, EPIC-07 |

Governação (**GOV**) e Observabilidade (**OBS**) são **transversais**: atravessam
todos os pacotes e materializam-se como bibliotecas partilhadas e políticas
(policy-as-code) versionadas, não como um pacote isolado.

## Fronteiras (invariantes de arquitectura)

- **RM é o único caminho para tool calls** (ADR-002). Nenhum pacote chama tools
  directamente; `platform/` e `substrate/` são alcançados através do `kernel/RM`.
- **Portas versionadas, não lock-in** (Princípio 8): as dependências entre
  pacotes fazem-se por contratos de interface — ver
  [tecnica/12_Contratos_de_Interface.md](../tecnica/12_Contratos_de_Interface.md).
- **Sem dependências circulares.** Sentido permitido: `control-plane` → `kernel` →
  `platform`/`substrate`. O substrato não conhece camadas acima.

Cada subpasta tem o seu `README.md` com o mapa de componentes e os tickets que a preenchem.
