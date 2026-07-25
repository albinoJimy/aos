# ADR-019 — Excepções intencionais às fronteiras canónicas de camada

| Campo | Valor |
|---|---|
| **ADR** | 019 |
| **Título** | Excepções intencionais às fronteiras canónicas de camada (v1 single-host) |
| **Estado** | Aceite |
| **Data** | 2026-07-25 |
| **Deciders** | Equipa AOS |
| **Contexto-fonte** | `specs/EPIC-17_Remediacao_Auditoria_Multiagente_v3.md` (AOS-179); gate `scripts/ci/layer-lint.sh` (AOS-178); baseline `scripts/ci/baseline/layer-lint-exceptions.txt`; `AGENTS.md` §3; `tecnica/06_Model_Gateway_Custos.md` §6 |
| **ADRs relacionados** | ADR-002 (RM mandatório), ADR-003 (identidade NHI), ADR-015 (durable execution), ADR-017 (supply-chain do nó), ADR-018 (fronteira nó↔ORQ/SCH) |
| **Supersede** | — |

> Este ADR **regista** excepções à regra canónica de dependências entre camadas. Não
> re-litiga a regra nem a forma do produto; define, de forma verificável, que inversões
> são aceites na v1 e porquê, para que o gate AOS-178 seja uma protecção contra novas
> violações e não um ruído de dívidas já conhecidas.

---

## 1. Contexto

A arquitectura canónica do AOS impõe o seguinte sentido de dependências
(`AGENTS.md` §3):

- `control-plane` → `kernel` → `platform` / `substrate`;
- `substrate` só conhece `substrate`;
- o **Reference Monitor (RM)** é o único caminho para *tool calls*.

Durante a implementação da v1, o gate `layer-lint` (AOS-178) detectou inversões a
esta regra. Algumas resultam de tipos de domínio partilhados entre o kernel e a
plataforma; outras de adaptadores finos que satisfazem portas de camadas
superiores; outras ainda reflectem o facto de a governação (control-plane)
precisar de consumir serviços de plataforma/substrato (identidade, audit,
messaging, event store, observabilidade). Remover **todas** estas inversões numa
única refactorização exigiria:

- mover dezenas de tipos nucleares (`RunID`, `StepID`, `Goal`, envelopes de
  *tool call*, tipos de `durable`/`replay`) para um pacote de contrato partilhado,
  ou duplicá-los;
- redefinir a fronteira RM↔sandbox (`MediatedLauncher` constrói um `Call` do RM);
- quebrar os adaptadores `tieradapter` e `budgetbridge`, forçando o escalonador/orçamento
  a importar internos do *Model Gateway* — o sentido oposto, igualmente indesejável.

O custo/risco dessa refactorização global supera o benefício para a v1. O ticket
AOS-179 prevê exactamente esta saída: *“Se uma inversão for intencional e
permanente, emitir ADR/emenda que a autorize e actualizar `AGENTS.md` §3.”*

## 2. Decisão

As seguintes inversões são **excepções intencionais e documentadas** na v1:

### 2.1 Substrato de execução importa o RM

`packages/substrate/sandbox` (e `packages/substrate/sandbox/network`) importa
`kernel/reference-monitor` para:

- construir o envelope `Call` no `MediatedLauncher`;
- aplicar políticas de rede/egress/DNS/resolver sobre os tipos do RM;
- garantir que **nenhuma execução de tool bypassa o RM**.

O sandbox é o substrato que **materializa** uma chamada já autorizada; não decide.
Importar os tipos do RM mantém o contrato canónico e evita duplicação do
envelope de chamada.

### 2.2 Serviços de plataforma importam o RM para tipos de autorização

`packages/platform/{broker,identity,audit}` importam `kernel/reference-monitor`
para:

- verificação de *scope* de credenciais (`broker/scope.go`);
- adaptadores de identidade e audit ao RM (`identity/rmadapter.go`,
  `audit/rmadapter.go`, `audit/teesink.go`);
- partilha dos tipos `Authorization`, `Call` e `Action`.

Estes serviços vivem na fronteira de confiança do RM; o seu trabalho é
*implementar ou auditar* mediação, não executar tools directamente.

### 2.3 Serviços de plataforma importam o agent-runtime para tipos de domínio partilhado

`packages/platform/{memory,registry,dr,model-gateway}` (e subpacotes) importam
`kernel/agent-runtime` para tipos de domínio partilhado:

- identificadores de run/passo (`RunID`, `StepID`);
- objectivos, trajectórias e envelopes de *tool call*;
- tipos de `durable`, `replay` e `worker` usados em DR e memória.

O agent-runtime define o vocabulário de uma *run*. A plataforma opera sobre
runs e trajectórias; sem estes tipos, cada serviço teria de redeclará-los ou
exigiria um pacote de contrato transversal que ainda não existe. Esta excepção é
**estritamente de tipos partilhados**; não autoriza lógica de execução a fugir do
kernel.

### 2.4 Model Gateway importa control-plane em adaptadores finos de fronteira

`packages/platform/model-gateway/routing/tieradapter` importa
`control-plane/scheduler`, e
`packages/platform/model-gateway/metering/cost/budgetbridge` importa
`control-plane/budget`.

São **adaptadores de fronteira finos** que permitem ao GW satisfazer as portas do
escalonador/orçamento sem reimplementar lógica de *tiering*/orçamento. O núcleo
do GW (`routing/tiering`, `routing/router`, `routing/degradation`) mantém-se
**zero-dep de control-plane**, como documentado em
`tecnica/06_Model_Gateway_Custos.md` §6 (satisfação da porta do Escalonador).

### 2.5 Plano de controlo importa plataforma e substrato

`packages/control-plane/{orchestrator,scheduler,governance/*,pdp,budget}` importam
`platform/{identity,audit,messaging}` e `substrate/{bus,eventstore,otel-genai,redaction}`
para:

- resolver delegações de identidade NHI (`platform/identity`);
- emitir/reter registos de audit (`platform/audit`);
- trocar mensagens de HITL/approval (`platform/messaging`);
- ler/escrever eventos duráveis (`substrate/eventstore`, `substrate/bus`);
- emitir spans OTel (`substrate/otel-genai`) e redigir PII (`substrate/redaction`).

Na v1 single-host, o plano de controlo governa mas **consome serviços de
plataforma/substrato** para o fazer. Esta dependência vertical é o reflexo da
topologia escolhida: o substrato é a camada de log/comunicação/observabilidade
partilhada, não uma camada que pode ser ignorada pelo controlo.

## 3. Alternativas consideradas

- **Refactorização total num só passo.** Rejeitada: tocar em dezenas de módulos,
  `go.mod`, testes e adaptadores introduziria risco desproporcionado para a v1,
  sem alterar o comportamento do sistema.
- **Mover tipos partilhados para um pacote `kernel/contract`.** Rejeitada para a
  v1: é a solução correcta a longo prazo, mas exige migrar todo o repo e validar
  todos os imports; fica como opção para EPIC-10/futuro, com ADR de supersessão.
- **Inverter os adaptadores `tieradapter`/`budgetbridge` para dentro do
  control-plane.** Rejeitada: faria o escalonador/orçamento depender de
  internos do GW, invertendo o sentido desejado de abstração (a plataforma
  adapta-se à porta do controlo, não o contrário).

## 4. Consequências

- **Positivas:** o gate AOS-178 fica verde; as inversões existentes deixam de ser
  dívida escondida e passam a ser decisão registada; novas violações continuam a
  bloquear a CI.
- **Custos/risco residual:** as excepções são um **teto**, não um cartão
  branco. Qualquer nova inversão deve ou ser justificada e adicionada a este ADR
  (via supersessão) ou ser removida. A regra canónica mantém-se como objectivo de
  desenho para versões futuras.

## 5. Conformidade / Enforcement

- **Baseline `scripts/ci/baseline/layer-lint-exceptions.txt`.** Cada padrão de
  excepção passa a citar o ADR-019; o gate `scripts/ci/layer-lint.sh` tolera
  apenas o que a baseline contém.
- **Gate AOS-178.** Continua a correr em `make ci-lint`; qualquer inversão fora
  da baseline falha a CI.
- **Referência em `AGENTS.md` §3.** A regra de sentido de dependências é
  acompanhada de nota que remete para este ADR.
- **Não contradiz ADR-018.** A fronteira nó↔ORQ/SCH (`cmd/aos` não importar
  `control-plane/orchestrator` nem `control-plane/scheduler` como donos do ciclo
  de vida) mantém-se intacta; as excepções aqui são ortogonais (plano de controlo
  consumindo plataforma/substrato, não o nó a consumir ORQ/SCH).

## 6. Referências

- `specs/EPIC-17_Remediacao_Auditoria_Multiagente_v3.md` — ticket AOS-179.
- `scripts/ci/layer-lint.sh` — gate AOS-178.
- `scripts/ci/baseline/layer-lint-exceptions.txt` — lista verificável de padrões.
- `AGENTS.md` §3 — invariantes de fronteira e excepções.
- `tecnica/06_Model_Gateway_Custos.md` §6 — documentação do `tieradapter` e
  `budgetbridge`.
- ADR-002, ADR-003, ADR-015, ADR-017, ADR-018.
