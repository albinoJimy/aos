# `packages/` — Código do AOS

Monorepo dividido por **camadas do modelo canónico** (ver
[_BRIEF.md](../_BRIEF.md) §2). Cada pacote agrupa componentes do catálogo canónico
pelo seu papel na arquitectura. As nove subpastas de produção contêm a
implementação de referência entregue pelos tickets `AOS-NNN` dos epics
respectivos; alguns componentes (ex.: composition-root completo do nó `aos`)
ainda têm seams condicionais documentados nos respectivos epics.

| Subpasta | Papel |
|---|---|
| `cmd/` | Binários: `aos` (nó de produção), `aos-demo` |
| `control-plane/` | Plano de controlo: ORQ, SCH, PDP, budget, governance/* |
| `integration/` | Composition-root / wiring / ápice de enforcement composto |
| `kernel/` | Plano de execução: RM (Reference Monitor), RT (Agent Runtime) |
| `platform/` | Serviços de plataforma: MEM, REG, GW, BRK, identity, audit, … |
| `qa/` | Testes de qualidade (dr-e2e, ux-dx) |
| `security-tests/` | Cenários adversariais de segurança |
| `substrate/` | Log & substrato: ES, bus, sandbox, otel-genai, redaction |
| `testkit/` | Fixtures, mocks deterministas e conversor cov2lcov (AOS-109) |

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
