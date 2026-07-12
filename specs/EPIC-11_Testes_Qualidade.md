# EPIC-11 — Testes e Qualidade

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Testes e Qualidade |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/11_Convencoes_Engenharia_Evolucao.md`, `specs/EPIC-08_Observabilidade_Evals.md`, `specs/01_Engineering_Standards_e_Handoff.md` |

---

## 1. Visão do Epic

Um Agentic OS só torna as falhas **arquitecturalmente impossíveis** se existir um aparelho de testes capaz de o **provar** — de forma repetível, determinística e automatizada. Este epic constrói esse aparelho: a infra-estrutura de testes (unit/integração), os ambientes efémeros isolados, e as **suites de domínio** que verificam as propriedades não-negociáveis do AOS — idempotência por passo (ADR-001), replay determinístico *resume-from-step* (ADR-010), enforcement de política default-deny (ADR-011) e eval-gate como *admission control* da auto-modificação (ADR-012).

A tese central do produto — as fundações não podem ser corroídas ticket a ticket — depende de que cada propriedade crítica tenha um teste que **falha em vermelho** quando a propriedade é violada. Sem isso, a governação é teatro e o replay é aspiracional. Por isso este epic vai além do teste funcional clássico: cria um **eval harness** com *golden-sets* curados, **trace-diffing** contra baseline para apanhar regressões comportamentais silenciosas (*misevolution*/drift), testes de **carga e escala** que reproduzem o modo de falha "individualmente ok, agregadamente colapsa" (ADR-008), testes de **segurança adversariais** (red-team) contra prompt injection e exfiltração (ADR-005), e testes de **DR/replay end-to-end** que provam a recuperação sobre um Event Store replicado (ADR-007).

Estes testes não vivem à parte: são os **gates 3, 4, 7, 8 e 9** do *pipeline* fail-closed definido em `specs/01_Engineering_Standards_e_Handoff.md` §4, e alimentam as métricas de saúde (`Replay-fidelity`, `Eval-pass-rate`, `Gate escape rate`). Este epic transversal cobre principalmente a **Fase 2** (governação e observabilidade) e a **Fase 3** (escala e controlo), mas fornece fundações (AOS-109/110) exigíveis desde a Fase 0.

**Âmbito.** Frameworks e ambientes de teste; suites de domínio (replay, idempotência, política); eval harness e golden-sets; trace-diffing; carga/escala; red-team adversarial; DR end-to-end. **Fora de âmbito:** a instrumentação OTel em si (ver `EPIC-08`), a implementação do PDP (ver `EPIC-01`/`EPIC-09`) e o desenho do event store (ver `EPIC-01`) — este epic **consome e verifica** esses componentes.

---

## 2. Critérios de Saída do Epic

- [ ] Existe um **framework de testes** unit/integração adoptado, com relatório de cobertura ligado ao gate 3 e limiar que não regride.
- [ ] Todo o teste de integração corre sobre **ambientes efémeros isolados** (Testcontainers ou equivalente), sem estado partilhado entre execuções.
- [ ] Uma **suite de replay determinístico** prova, em CI, que qualquer trajectória se reproduz *resume-from-step* com hashes coincidentes (gate 8).
- [ ] Uma **suite de idempotência por passo** prova que reexecutar qualquer passo com a mesma *idempotency key* produz zero efeitos duplicados.
- [ ] Uma **suite de política/PDP** cobre *allow* e *deny* default-deny para toda a classe de decisão de autorização (gate 7).
- [ ] O **eval harness** corre golden-sets curados e emite `gen_ai.evaluation.result`; é *admission control* de qualquer artefacto comportamental (gate 9, ADR-012).
- [ ] O **trace-diffing** contra baseline detecta regressões comportamentais e bloqueia a promoção a canary/prod.
- [ ] Existem **testes de carga/escala** que exercem admission control global e backpressure, e validam os alvos de NFR relevantes.
- [ ] Existe uma **suite adversarial (red-team)** contra prompt injection e exfiltração, integrada em CI, com casos que falham em vermelho se a fronteira control/data-plane ceder.
- [ ] Existe um **teste de DR/replay end-to-end** que simula perda de nó e prova recuperação sem perda de eventos nem efeitos duplicados.

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-109 | Framework de testes unit/integração | chore | M | P0 | — |
| AOS-110 | Ambiente efémero de teste (Testcontainers ou equivalente) | feature | M | P0 | AOS-109 |
| AOS-111 | Testes de replay determinístico | feature | L | P0 | AOS-110 |
| AOS-112 | Testes de idempotência por passo | feature | M | P0 | AOS-110 |
| AOS-113 | Testes de política (PDP/policy-as-code) | feature | M | P1 | AOS-110 |
| AOS-114 | Eval harness + golden-sets curados | feature | L | P1 | AOS-110 |
| AOS-115 | Trace-diffing vs baseline | feature | M | P1 | AOS-111, AOS-114 |
| AOS-116 | Testes de carga/escala (admission control, backpressure) | feature | L | P1 | AOS-110 |
| AOS-117 | Testes de segurança adversariais (red-team) | feature | L | P0 | AOS-110, AOS-113 |
| AOS-118 | Testes de DR/replay end-to-end | feature | L | P1 | AOS-111, AOS-116 |

---

## AOS-109 — Framework de testes unit/integração

| Campo | Valor |
|---|---|
| Epic | EPIC-11 — Testes e Qualidade |
| Fase | 0 — Fundações |
| Tipo | chore |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | — |
| Bloqueia | AOS-110 … AOS-118 |
| Responsável sugerido | QA |
| Documentos de referência | `specs/01_Engineering_Standards_e_Handoff.md` (§3, §4), `tecnica/11_Convencoes_Engenharia_Evolucao.md` |

### Contexto

O backlog inteiro do AOS assenta numa Definition of Done que exige testes verdes e cobertura que não regride (`specs/01` §3). Isso pressupõe um **framework de testes** único, adoptado por todos os perfis executores, com convenções de organização, *fixtures*, *mocking* e relatório de cobertura consumível pelo gate 3 do *pipeline*. Sem esta fundação, cada equipa improvisa e a DoD torna-se inaplicável.

### Objectivo

Estabelecer e documentar o framework de testes unit/integração de referência, com estrutura de directórios, convenções de nomenclatura, *fixtures* partilhadas, *mocks* dos componentes canónicos (RM, PDP, ES, GW, BRK) e um relatório de cobertura (linhas/branches) ligado ao gate 3 de CI/CD com limiar configurável.

### Critérios de Aceitação

- [ ] Framework de testes seleccionado e configurado, com um comando único que corre toda a suite unit e reporta cobertura em formato máquina-legível (ex.: lcov/cobertura/junit).
- [ ] Estrutura de testes documentada: separação clara `unit/` vs `integração/`, convenção de nomes e localização das *fixtures*.
- [ ] *Mocks*/*stubs* de referência disponíveis para os componentes canónicos que a maioria dos tickets toca (RM, PDP, ES, GW, BRK), com contratos alinhados ao catálogo do `_BRIEF` §2.
- [ ] Limiar de cobertura configurado no gate 3; uma descida abaixo do limiar bloqueia o *merge* (fail-closed).
- [ ] `README`/secção de testes explica como escrever um teste de domínio (idempotência, replay, política) reutilizando as *fixtures*.

### Detalhes Técnicos

- Componentes-alvo: infra-estrutura de testes transversal; *harness* base reutilizado por AOS-110 a AOS-118.
- Definir *test fixtures* que materializam um `run_id`/`step_id` e um Event Store em memória para os testes unit; a variante integração usa o ambiente efémero de AOS-110.
- Integrar o relatório de cobertura no gate 3 (`specs/01` §4) sem introduzir *flakiness* (isolar tempo, aleatoriedade e I/O).

### Testes Requeridos

- Testes-exemplo (canário) para cada tipo de *fixture* que provam que o *harness* corre verde localmente e em CI.
- Teste que confirma que o gate falha quando a cobertura desce abaixo do limiar (teste do próprio gate).

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Código revisto (mínimo um revisor humano).
- [ ] Relatório de cobertura ligado ao gate 3; limiar activo e fail-closed.
- [ ] Documentação de convenções de teste publicada; `CHANGELOG` alimentado por Conventional Commits.
- [ ] Sem TODOs órfãos nem *flakiness* conhecida na suite canário.

### Handoff para Claude Code

```text
És o executor do ticket AOS-109 do Agentic OS de Referência (AOS).

Lê AOS-109 em specs/EPIC-11_Testes_Qualidade.md e specs/01_Engineering_Standards_e_Handoff.md (§3, §4).

OBJECTIVO: estabelecer o framework de testes unit/integração de referência.
- Configura um framework único com comando de suite e cobertura máquina-legível.
- Documenta estrutura unit/ vs integração/, fixtures e convenções.
- Fornece mocks/stubs para RM, PDP, ES, GW, BRK alinhados ao _BRIEF §2.
- Liga o relatório de cobertura ao gate 3 com limiar fail-closed.
- Escreve testes canário por tipo de fixture.

Não expandas escopo. O ambiente efémero (Testcontainers) é o AOS-110 — não o implementes aqui.
Commits Conventional (chore(AOS-109): ...), branch feature/AOS-109-test-framework, PR pelo template da §7.
```

---

## AOS-110 — Ambiente efémero de teste (Testcontainers ou equivalente)

| Campo | Valor |
|---|---|
| Epic | EPIC-11 — Testes e Qualidade |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-109 |
| Bloqueia | AOS-111 … AOS-118 |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `specs/01_Engineering_Standards_e_Handoff.md` (§4), `tecnica/11_Convencoes_Engenharia_Evolucao.md` |

### Contexto

Os testes de integração do AOS tocam dependências com estado — Event Store replicado (ES), transporte push (NATS/Redis/Postgres), PDP e vault. Correr contra instâncias partilhadas produz testes *flaky* e acoplados: uma execução contamina a seguinte. A DoD e os gates 4/7/8 exigem determinismo. É preciso um mecanismo que **provisione e destrua** dependências reais por execução, isoladas e reprodutíveis.

### Objectivo

Fornecer um *harness* que arranca dependências efémeras (Testcontainers ou equivalente) por suite/execução — Event Store, transporte push e PDP — com *lifecycle* determinístico (arranque, *seed*, *teardown*), reutilizável por todas as suites de domínio deste epic.

### Critérios de Aceitação

- [ ] Um teste de integração pode declarar as dependências efémeras que precisa (ES, bus, PDP, vault de teste) e recebê-las provisionadas e destruídas automaticamente.
- [ ] Cada execução parte de estado limpo; nenhuma execução observa efeitos colaterais de outra (isolamento provado por um teste que corre duas vezes e obtém o mesmo resultado).
- [ ] O tempo de arranque das dependências é limitado e o *teardown* é garantido mesmo em falha do teste (sem *containers* órfãos).
- [ ] O ambiente efémero corre tanto localmente como no *runner* de CI, sem configuração manual adicional.
- [ ] *Helpers* de *seed* permitem popular o Event Store com uma trajectória conhecida para os testes de replay/idempotência (AOS-111/112).

### Detalhes Técnicos

- Componentes-alvo: ES (Event Store replicado), transporte push, PDP, vault de teste — versões pinadas por hash para reprodutibilidade (coerente com a disciplina de supply-chain do `_BRIEF` §2).
- Expor uma API de *fixture* que integra com o framework de AOS-109; garantir *teardown* idempotente.
- Pinar imagens/versões e registá-las, para que o ambiente de teste seja tão reprodutível quanto o manifesto por trajectória.

### Testes Requeridos

- Teste de isolamento: a mesma suite corre duas vezes e produz resultados idênticos, sem contaminação.
- Teste de *teardown*: forçar falha a meio e confirmar que não sobram recursos órfãos.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Código revisto (mínimo um revisor humano).
- [ ] Ambiente efémero corre em CI e localmente; imagens pinadas por versão/hash.
- [ ] Documentação de utilização publicada; `CHANGELOG` alimentado.
- [ ] Sem *containers*/recursos órfãos após a suite; sem segredos reais no ambiente de teste.

### Handoff para Claude Code

```text
És o executor do ticket AOS-110 do Agentic OS de Referência (AOS).

Lê AOS-110 em specs/EPIC-11_Testes_Qualidade.md e confirma o framework de AOS-109.

OBJECTIVO: harness de ambientes efémeros (Testcontainers ou equivalente) para integração.
- Provisiona e destrói ES, transporte push, PDP e vault de teste por execução.
- Garante estado limpo e teardown fiável mesmo em falha (sem órfãos).
- Corre local e em CI sem setup manual.
- Fornece helpers de seed para popular o Event Store com trajectórias conhecidas.
- Pina imagens por versão/hash.

Não uses segredos reais. Não expandas escopo para as suites de domínio (AOS-111+).
Commits Conventional (feat(AOS-110): ...), branch feature/AOS-110-ephemeral-env, PR pelo template da §7.
```

---

## AOS-111 — Testes de replay determinístico

| Campo | Valor |
|---|---|
| Epic | EPIC-11 — Testes e Qualidade |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-110 |
| Bloqueia | AOS-115, AOS-118 |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `specs/EPIC-08_Observabilidade_Evals.md`, `tecnica/11_Convencoes_Engenharia_Evolucao.md` (ADR-001, ADR-010) |

### Contexto

O replay determinístico *resume-from-step* é uma fundação: sem ele, a RCA e o eval são inválidos e o risco "replay infiel após evolução de código" concretiza-se (`_FONTE` — Riscos). O gate 8 do *pipeline* é bloqueante justamente por isso. Para o gate ter conteúdo, é preciso uma suite que reconstrói uma trajectória a partir do Event Store, captura todos os inputs não-determinísticos e verifica que os hashes coincidem passo a passo.

### Objectivo

Construir a suite de teste de replay determinístico que, dada uma trajectória gravada no Event Store, a reproduz *resume-from-step*, compara o hash do prompt materializado por turno e o estado resultante contra o registo original, e falha se qualquer passo divergir.

### Critérios de Aceitação

- [ ] A suite carrega uma trajectória conhecida (via *seed* de AOS-110) e reproduz-na *resume-from-step* a partir de um passo arbitrário.
- [ ] Todos os inputs não-determinísticos (tempo, aleatoriedade, respostas de modelo, resultados de tool) são capturados e injectados no replay — nenhuma chamada externa real ocorre no replay.
- [ ] O hash do prompt materializado por turno e o estado resultante coincidem 100% com o registo original; qualquer divergência falha o teste com diff legível.
- [ ] A suite está ligada ao gate 8 de CI/CD e bloqueia o *merge* em caso de infidelidade de replay.
- [ ] Um teste negativo prova que uma mudança que quebra a fidelidade (ex.: reordenar o prefixo do prompt) é detectada e falha em vermelho.

### Detalhes Técnicos

- Componentes-alvo: OBS (replay), ES (fonte de verdade), RT (loop). Alinhar com o manifesto por trajectória (model-id/params/seed + hash do prompt) descrito em `specs/01` §5 e `EPIC-08`.
- Usar o *content-capture* de spans OTel para reconstruir o input; efeitos externos servidos por *replay stubs*, nunca executados.
- Reportar a métrica `Replay-fidelity` (% de trajectórias 100% reproduzíveis; alvo 100%).
- **Reutilizar o harness fundacional de AOS-024** (`packages/kernel/agent-runtime/harness`), já entregue no EPIC-02: `Verify`/`FidelityReport` sobre o `ReplayEngine` de AOS-016, as golden trajectories (`GoldenSet`) e o gate 8 (`scripts/ci/replay.sh`) já existem e são *fail-closed*. Esta suite **estende** esse harness com trajectórias de domínio adicionais (via *seed* de AOS-110) — **não** reimplementa a mecânica de replay/relatório. **Distinto do eval harness de comportamento (AOS-114):** aqui o foco é replay/idempotência, não o *golden-set* de comportamento.

### Testes Requeridos

- Replay positivo *resume-from-step* de uma trajectória multi-passo com sub-agente.
- Replay negativo: mutação do prefixo do prompt / troca de seed detectada e falhada.
- Teste do gate: gate 8 vermelho bloqueia progressão.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Código revisto (mínimo um revisor humano; dois por ser P0).
- [ ] **Replay determinístico testado** — *resume-from-step*, hashes coincidem (ADR-010).
- [ ] Suite ligada ao gate 8; métrica `Replay-fidelity` reportada.
- [ ] Sem chamadas externas reais durante o replay; sem TODOs órfãos.

### Handoff para Claude Code

```text
És o executor do ticket AOS-111 do Agentic OS de Referência (AOS).

Lê AOS-111 em specs/EPIC-11_Testes_Qualidade.md, specs/EPIC-08_Observabilidade_Evals.md e ADR-001/ADR-010.

OBJECTIVO: suite de replay determinístico resume-from-step.
- Reproduz uma trajectória gravada no Event Store a partir de um passo arbitrário.
- Captura e injecta todos os inputs não-determinísticos; zero chamadas externas reais.
- Compara hash do prompt materializado e estado por turno contra o registo; diff legível.
- Liga a suite ao gate 8 (fail-closed) e reporta Replay-fidelity.
- Inclui teste negativo (mutação de prefixo/seed detectada).

Não expandas escopo para trace-diffing (AOS-115). Respeita o manifesto por trajectória.
Commits Conventional (feat(AOS-111): ...), branch feature/AOS-111-replay-tests, PR pelo template da §7.
```

---

## AOS-112 — Testes de idempotência por passo

| Campo | Valor |
|---|---|
| Epic | EPIC-11 — Testes e Qualidade |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-110 |
| Bloqueia | AOS-118 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md`, `tecnica/11_Convencoes_Engenharia_Evolucao.md` (ADR-001) |

### Contexto

O risco "double-execution de efeito externo no retry" corrompe o mundo externo (`_FONTE` — Riscos). A defesa é a *idempotency key* `f(run_id, step_id)` com efeitos isolados em *activities* e saga de compensação. A DoD do domínio exige um teste que reexecuta o passo e prova zero efeitos duplicados. Este ticket cria a suite reutilizável que verifica essa propriedade em qualquer *activity*.

### Objectivo

Construir a suite que, para uma *activity* com efeito externo, injecta falha após o efeito mas antes do commit do resultado, força o retry com a mesma *idempotency key*, e prova que o efeito externo ocorre exactamente uma vez.

### Critérios de Aceitação

- [ ] A suite exercita uma *activity* representativa com efeito externo observável (ex.: escrita idempotente) e prova exactamente-uma-vez sob retry com a mesma `f(run_id, step_id)`.
- [ ] Um cenário de crash injectado (falha após efeito, antes do commit) é reexecutado e **não** duplica o efeito.
- [ ] Um cenário de *fencing*: uma escrita de worker obsoleto é rejeitada (não corrompe estado), coerente com liveness por lease/fencing token.
- [ ] A suite valida a saga de compensação: um passo falhado recuperável compensa e permite retry idempotente.
- [ ] Testes disponíveis como *helpers* reutilizáveis para que qualquer ticket com efeito externo cumpra a DoD de idempotência.

### Detalhes Técnicos

- Componentes-alvo: RT (loop), SCH (leases/fencing), ES. Usar o ambiente efémero de AOS-110 para o *store* de idempotência real.
- Modelar a *idempotency key* explicitamente e cobrir os estados `failed → compensating → ready` da máquina de estados durável.
- Evitar dependência de tempo real; injectar relógio controlável.
- **Reutilizar o harness fundacional de AOS-024**: a verificação de idempotência por passo (calendário *at-least-once* com crash intercalado → *zero* efeitos observáveis duplicados via `StepLedger` de AOS-014) e a detecção de efeito duplicado injectado já estão implementadas em `harness.Verify`/`harness.Effect`. Esta suite **estende** o harness com efeitos de domínio — não reimplementa a mecânica.

### Testes Requeridos

- Retry com a mesma key: 1 efeito, N tentativas.
- Crash pós-efeito/pré-commit: sem duplicação.
- Fencing token obsoleto: escrita rejeitada.
- Saga de compensação após falha recuperável.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Código revisto (mínimo um revisor humano; dois por ser P0).
- [ ] **Idempotência por passo verificada** — reexecução prova zero efeitos duplicados (ADR-001).
- [ ] *Helpers* reutilizáveis documentados para os restantes epics.
- [ ] Sem *flakiness* por tempo/aleatoriedade; relógio injectável.

### Handoff para Claude Code

```text
És o executor do ticket AOS-112 do Agentic OS de Referência (AOS).

Lê AOS-112 em specs/EPIC-11_Testes_Qualidade.md, specs/EPIC-02 e ADR-001.

OBJECTIVO: suite reutilizável de idempotência por passo.
- Prova exactamente-uma-vez sob retry com a mesma idempotency key f(run_id, step_id).
- Injecta crash após efeito e antes do commit; sem duplicação.
- Cobre fencing (escrita de worker obsoleto rejeitada) e saga de compensação.
- Expõe helpers reutilizáveis para a DoD de idempotência de outros tickets.
- Usa relógio injectável, sem dependência de tempo real.

Corre sobre o ambiente efémero de AOS-110. Não expandas escopo para DR (AOS-118).
Commits Conventional (feat(AOS-112): ...), branch feature/AOS-112-idempotency-tests, PR pelo template da §7.
```

---

## AOS-113 — Testes de política (PDP/policy-as-code)

| Campo | Valor |
|---|---|
| Epic | EPIC-11 — Testes e Qualidade |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-110 |
| Bloqueia | AOS-117 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `specs/EPIC-09_Governacao_Conformidade.md`, `tecnica/11_Convencoes_Engenharia_Evolucao.md` (ADR-011, ADR-002) |

### Contexto

A governação só é efectiva se o PDP negar por omissão (default-deny) e as decisões forem verificáveis. O gate 7 é bloqueante — "governação não pode regredir". Uma blocklist que *falha aberta* a cada tool nova é o antipadrão que o `_FONTE` (Dimensão 5) rejeita em favor de **allowlist capability-scoped default-deny**. É preciso uma suite que teste a política-como-código (Rego/OPA ou Cedar) exaustivamente sobre *allow* e *deny*.

### Objectivo

Construir a suite de testes de política que avalia o PDP contra casos *allow* e *deny*, prova o comportamento default-deny (uma capability não explicitamente permitida é negada), e valida que a política é versionada e assinada, cobrindo as classes de decisão de autorização por tool call.

### Critérios de Aceitação

- [ ] Cada regra de política tem casos de teste *allow* e *deny* explícitos; a suite falha se uma regra ficar sem cobertura.
- [ ] Um pedido para uma capability não permitida é **negado por omissão** (default-deny provado por teste dedicado).
- [ ] Testes cobrem a cadeia de delegação/autoridade = utilizador ∩ classe de agente (um agente não excede o escopo do seu principal).
- [ ] A suite verifica que a política avaliada corresponde a uma versão assinada e pinada (rejeita política não assinada/alterada).
- [ ] Suite ligada ao gate 7 de CI/CD, bloqueando *merge* em regressão de governação.

### Detalhes Técnicos

- Componentes-alvo: PDP, RM (PEP). Política em Rego/OPA ou Cedar, versionada em git e assinada (`specs/01` §5).
- Testar decisões sobre eixos de risco (sensibilidade de dados + egress + reversibilidade) coerentes com o modelo SA-ROC de `EPIC-09`.
- Medir `PDP deny-rate` como sinal de saúde da governação.

### Testes Requeridos

- Matriz *allow*/*deny* por regra; cobertura de regras verificada.
- Default-deny para capability desconhecida.
- Delegação: agente não excede autoridade do utilizador.
- Rejeição de política não assinada.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Código revisto (mínimo um revisor humano; dois por tocar em governação/segurança).
- [ ] **Políticas (policy-as-code) com teste** — *allow*/*deny* default-deny cobertos (ADR-011).
- [ ] Suite ligada ao gate 7; `PDP deny-rate` reportado.
- [ ] Nenhuma política não assinada aceite; sem TODOs órfãos.

### Handoff para Claude Code

```text
És o executor do ticket AOS-113 do Agentic OS de Referência (AOS).

Lê AOS-113 em specs/EPIC-11_Testes_Qualidade.md, specs/EPIC-09 e ADR-011/ADR-002.

OBJECTIVO: suite de testes de política (PDP/policy-as-code).
- Casos allow e deny por regra; cobertura de regras verificada.
- Prova default-deny para capabilities não permitidas.
- Cobre delegação (autoridade = utilizador ∩ classe de agente).
- Rejeita política não assinada/alterada; valida versão pinada.
- Liga a suite ao gate 7 (fail-closed) e reporta PDP deny-rate.

Não implementes o PDP (EPIC-09) — testa-o. Não expandas escopo para red-team (AOS-117).
Commits Conventional (feat(AOS-113): ...), branch feature/AOS-113-policy-tests, PR pelo template da §7.
```

---

## AOS-114 — Eval harness + golden-sets curados

| Campo | Valor |
|---|---|
| Epic | EPIC-11 — Testes e Qualidade |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-110 |
| Bloqueia | AOS-115 |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `specs/EPIC-08_Observabilidade_Evals.md`, `tecnica/11_Convencoes_Engenharia_Evolucao.md` (ADR-012, ADR-010) |

### Contexto

A auto-modificação (memória procedural, skills auto-escritas) é a **mudança de maior risco** do sistema; sem eval-gate ela chega a produção unilateralmente e ocorre *misevolution*/drift mesmo sem atacante (`_FONTE` — Dimensão 7). O ADR-012 exige eval-gate como *admission control*: staging → **eval-gate (golden-set curado)** → canary → ratificação assinada → prod. Este ticket constrói o harness e os golden-sets que dão substância ao gate 9.

### Objectivo

Construir o eval harness que corre *golden-sets* curados e estáveis (complementados por datasets derivados de falhas) sobre um artefacto comportamental candidato, produz um veredicto pass/fail com métricas, emite o resultado como `gen_ai.evaluation.result` ligado ao trace, e funciona como *admission control* (gate 9) da promoção a canary/prod.

### Critérios de Aceitação

- [ ] Existe um formato de *golden-set* curado (input → expectativa/critério) versionado e revisável, com um *set* inicial não-trivial por classe de artefacto comportamental.
- [ ] O harness corre o golden-set sobre um candidato e produz veredicto pass/fail com métricas (success-rate, unsafe-action-rate) reprodutíveis.
- [ ] O resultado é emitido como `gen_ai.evaluation.result` e ligado ao trace da execução (interoperável com `EPIC-08`).
- [ ] Um artefacto que falha o eval-gate é **rejeitado sem ir a produção**; um que passa fica elegível a canary.
- [ ] O harness distingue golden-set curado (regressões novas) de datasets derivados de falhas (regressões conhecidas) e corre ambos.

### Detalhes Técnicos

- Componentes-alvo: OBS (evals), GOV (eval-gate de auto-modificação). Alinhar `gen_ai.evaluation.result` com a semconv de `EPIC-08`.
- Golden-sets estáveis e curados apanham regressões *novas* que o histórico de falhas nunca apanharia (`_FONTE` — Dimensão 7).
- Reportar `Eval-pass-rate` (% que passa à primeira; alvo ≥ 90%).

### Testes Requeridos

- Candidato "bom" passa; candidato com regressão injectada falha.
- Veredicto reprodutível entre execuções (determinismo do harness).
- Emissão de `gen_ai.evaluation.result` ligada ao trace verificada.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Código revisto (mínimo um revisor humano).
- [ ] **Eval-gate verde para artefactos comportamentais** — golden-set + admission control (ADR-012).
- [ ] Golden-sets versionados; `Eval-pass-rate` reportado.
- [ ] Resultado ligado ao trace; sem TODOs órfãos.

### Handoff para Claude Code

```text
És o executor do ticket AOS-114 do Agentic OS de Referência (AOS).

Lê AOS-114 em specs/EPIC-11_Testes_Qualidade.md, specs/EPIC-08 e ADR-012/ADR-010.

OBJECTIVO: eval harness + golden-sets curados como admission control (gate 9).
- Define formato de golden-set curado versionado; set inicial por classe de artefacto.
- Corre golden-sets sobre um candidato; veredicto pass/fail com success-rate e unsafe-action-rate.
- Emite gen_ai.evaluation.result ligado ao trace (interop com EPIC-08).
- Rejeita candidato que falha (sem ir a prod); elegível a canary se passa.
- Corre golden-set curado E datasets derivados de falhas.

Não implementes o canary/ratificação (EPIC-09) — só o eval-gate. Reporta Eval-pass-rate.
Commits Conventional (feat(AOS-114): ...), branch feature/AOS-114-eval-harness, PR pelo template da §7.
```

---

## AOS-115 — Trace-diffing vs baseline

| Campo | Valor |
|---|---|
| Epic | EPIC-11 — Testes e Qualidade |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-111, AOS-114 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `specs/EPIC-08_Observabilidade_Evals.md`, `tecnica/11_Convencoes_Engenharia_Evolucao.md` (ADR-012, ADR-010) |

### Contexto

O eval-gate do `_FONTE` (Dimensão 7) combina *golden-set curado* **e** *trace-diffing vs baseline*. Um artefacto pode passar as métricas agregadas do golden-set e, ainda assim, alterar o **comportamento passo a passo** (usar tools diferentes, mudar a ordem de acções, aumentar o custo) — regressão silenciosa que só o diff da árvore de spans contra uma baseline revela. Este ticket adiciona esse segundo sinal ao gate.

### Objectivo

Construir a capacidade de *trace-diffing* que compara a árvore de spans OTel de uma execução candidata contra uma baseline aprovada, classifica as diferenças (acções, ordem, tokens/custo, resultado) e sinaliza regressões comportamentais que bloqueiam a promoção a canary/prod.

### Critérios de Aceitação

- [ ] Dada uma baseline e uma execução candidata sobre o mesmo input, o *trace-diffing* produz um diff estruturado da árvore de spans (acções, sequência, `gen_ai.usage.*`, custo, veredicto).
- [ ] Diferenças são classificadas em ruído tolerável vs regressão significativa segundo limiares configuráveis (ex.: variação de custo/tokens, desvio de sequência de tools).
- [ ] Uma regressão comportamental significativa **bloqueia** a promoção do artefacto (integra com o eval-gate de AOS-114 / gate 9).
- [ ] O diff é legível e accionável (mostra o passo divergente e a natureza da divergência), útil para eval-driven development.
- [ ] Um teste com regressão injectada (troca de tool / salto de custo) é detectado e falha; uma variação dentro do ruído tolerável não gera falso-positivo.

### Detalhes Técnicos

- Componentes-alvo: OBS (replay + spans). Reutiliza o replay determinístico de AOS-111 para gerar traces comparáveis e o veredicto de AOS-114.
- Normalizar campos não-determinísticos antes do diff (timestamps, IDs) para evitar ruído; comparar sobre a estrutura semântica da trajectória.
- Registar o diff como evidência ligada ao trace (contexto ≠ registo).

### Testes Requeridos

- Diff idêntico (baseline == candidato) → zero regressões.
- Regressão injectada (tool trocada, custo +X%) → detectada e bloqueada.
- Variação dentro do limiar → não falso-positivo.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Código revisto (mínimo um revisor humano).
- [ ] Trace-diffing integrado no eval-gate (gate 9) e bloqueante em regressão.
- [ ] Diffs registados como evidência ligada ao trace; limiares documentados.
- [ ] Sem falsos-positivos por não-determinismo; sem TODOs órfãos.

### Handoff para Claude Code

```text
És o executor do ticket AOS-115 do Agentic OS de Referência (AOS).

Lê AOS-115 em specs/EPIC-11_Testes_Qualidade.md, specs/EPIC-08 e ADR-012/ADR-010.

OBJECTIVO: trace-diffing da árvore de spans vs baseline aprovada.
- Produz diff estruturado (acções, sequência, gen_ai.usage.*, custo, veredicto).
- Classifica ruído tolerável vs regressão significativa por limiares configuráveis.
- Bloqueia promoção em regressão comportamental (integra com o eval-gate de AOS-114).
- Normaliza campos não-determinísticos para evitar falsos-positivos.
- Inclui testes: idêntico=0 regressões, regressão injectada detectada, ruído não falha.

Reutiliza o replay de AOS-111 e o veredicto de AOS-114. Regista o diff ligado ao trace.
Commits Conventional (feat(AOS-115): ...), branch feature/AOS-115-trace-diffing, PR pelo template da §7.
```

---

## AOS-116 — Testes de carga/escala (admission control, backpressure)

| Campo | Valor |
|---|---|
| Epic | EPIC-11 — Testes e Qualidade |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-110 |
| Bloqueia | AOS-118 |
| Responsável sugerido | Engenheiro de Runtime |
| Documentos de referência | `specs/EPIC-03_Orquestracao_Escalonamento.md`, `tecnica/11_Convencoes_Engenharia_Evolucao.md` (ADR-008) |

### Contexto

O modo de falha central do plano-base é "individualmente ok, agregadamente colapsa": múltiplos boards, cada um dentro do seu `max_spawn`, saturam colectivamente o rate limit partilhado (`_FONTE` — Dimensão 3). A resolução é admission control **global** com reserva de headroom e backpressure com degradação graciosa (shed → defer → degradar → rejeitar). Estas propriedades exigem testes de carga que reproduzem a saturação agregada e provam que o sistema degrada em vez de colapsar.

### Objectivo

Construir testes de carga/escala que geram concorrência agregada suficiente para exercer o admission control global (token-bucket distribuído sobre TPM/RPM real) e o backpressure, provando reserva de headroom no admit, degradação graciosa em ordem, e ausência de colapso agregado ou acumulação ilimitada de filas.

### Critérios de Aceitação

- [ ] Um teste gera carga agregada acima do headroom e prova que o escalonador **não faz spawn sem débito reservado** no token-bucket global (reserva atómica).
- [ ] O backpressure segue a política declarativa de degradação (shed → defer → degradar para modelo mais barato → rejeitar), verificada por ordem.
- [ ] Sob sobrecarga, as filas permanecem limitadas (sem acumulação ilimitada nem cascata de timeouts); o sistema rejeita/degrada em vez de colapsar.
- [ ] O circuit breaker de orçamento (tokens/$) dispara nos limiares esperados e é observável.
- [ ] O teste reporta os NFRs relevantes (ex.: overhead de mediação p95, comportamento sob saturação) como sinais, não apenas pass/fail binário.

### Detalhes Técnicos

- Componentes-alvo: SCH (admission control, backpressure), ORQ, RM (overhead). Correr sobre o ambiente efémero de AOS-110 com transporte push real.
- Cenário-chave: N boards concorrentes dentro do respectivo `max_spawn` a saturar o rate limit partilhado; provar que `max_spawn` é derivado dinamicamente do headroom.
- Instrumentar com spans/métricas para observar burn-down e deny-rate sob carga.

### Testes Requeridos

- Saturação agregada: spawn negado sem headroom reservado.
- Degradação graciosa na ordem declarada.
- Filas limitadas sob sobrecarga; sem cascata de timeouts.
- Circuit breaker de orçamento dispara no limiar.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Código revisto (mínimo um revisor humano).
- [ ] Admission control global e backpressure exercidos e provados (ADR-008).
- [ ] NFRs de escala reportados como sinais; resultados reprodutíveis.
- [ ] Sem *flakiness* dependente de máquina; parametrização de carga documentada.

### Handoff para Claude Code

```text
És o executor do ticket AOS-116 do Agentic OS de Referência (AOS).

Lê AOS-116 em specs/EPIC-11_Testes_Qualidade.md, specs/EPIC-03 e ADR-008.

OBJECTIVO: testes de carga/escala para admission control global e backpressure.
- Gera carga agregada acima do headroom; prova que não há spawn sem débito reservado.
- Verifica degradação graciosa na ordem shed → defer → degradar → rejeitar.
- Prova filas limitadas sob sobrecarga, sem cascata de timeouts nem colapso agregado.
- Confirma circuit breaker de orçamento (tokens/$) no limiar.
- Reporta NFRs de escala como sinais.

Reproduz o cenário "N boards saturam o rate limit partilhado". Usa o ambiente efémero de AOS-110.
Commits Conventional (feat(AOS-116): ...), branch feature/AOS-116-load-tests, PR pelo template da §7.
```

---

## AOS-117 — Testes de segurança adversariais (red-team)

| Campo | Valor |
|---|---|
| Epic | EPIC-11 — Testes e Qualidade |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-110, AOS-113 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `specs/EPIC-07_Seguranca_Isolamento.md`, `tecnica/11_Convencoes_Engenharia_Evolucao.md` (ADR-005, ADR-002, ADR-004) |

### Contexto

O vector nº1 é a prompt injection (OWASP LLM01 / ASI01) que leva a exfiltração via tools "benignas" (padrão CamoLeak, CVSS 9.6) — o risco real não é `rm -rf` mas a fuga de dados (`_FONTE` — Dimensão 2). A defesa arquitectural é a separação control/data-plane com taint tracking (dual-LLM/CaMeL) + reference monitor + egress default-deny. Uma defesa só é credível se for **atacada** continuamente: este ticket cria a suite adversarial que tenta quebrar essas fronteiras e falha em vermelho se alguma ceder.

### Objectivo

Construir uma suite de red-team automatizada que injecta ataques de prompt injection e tentativas de exfiltração através de conteúdo untrusted (tool results, web, memória, schemas MCP), e prova que o conteúdo untrusted é incapaz de autorizar acções privilegiadas, que o egress default-deny bloqueia a fuga e que a tentativa fica auditada.

### Critérios de Aceitação

- [ ] Um corpus de payloads adversariais (injecção directa e indirecta, ofuscação por base64/metacaracteres/symlinks, memory poisoning) é executado contra o runtime.
- [ ] Conteúdo *untrusted* nunca autoriza uma tool call privilegiada — o taint impede-o (teste falha em vermelho se uma injecção conseguir escalar privilégio).
- [ ] Tentativas de exfiltração para destinos fora da allowlist são bloqueadas pelo egress default-deny; nenhum dado sensível sai.
- [ ] Cada tentativa de ataque é mediada pelo Reference Monitor e deixa rasto auditável (o ataque falha **e** fica registado).
- [ ] A suite corre em CI de forma determinística; um enfraquecimento da fronteira control/data-plane é detectado e bloqueia o *merge*.

### Detalhes Técnicos

- Componentes-alvo: RM (PEP), SBX (egress default-deny), MEM (proveniência/quarentena), GOV. Alinhar com o threat model de `EPIC-07`.
- Cobrir o *hallucination gate* endurecido (autenticar origem + autoridade + referência via assinatura) e a re-aprovação em mudança de schema MCP (anti rug-pull).
- Não usar segredos reais nem alvos externos reais; simular destinos de exfiltração dentro do ambiente efémero.

### Testes Requeridos

- Injecção indirecta via tool result / web / memória → não autoriza acção.
- Exfiltração via tool "benigna" para destino fora da allowlist → bloqueada.
- Ofuscação (base64/metacaracteres/symlink) não contorna a fronteira.
- Memory poisoning marcado com proveniência e posto em quarentena.
- Tentativa auditada (rasto tamper-evident presente).

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Código revisto (dois revisores por ser P0 de segurança).
- [ ] **Toda a tool call mediada pelo Reference Monitor**; taint impede escalada (ADR-002, ADR-005).
- [ ] Egress default-deny provado; tentativas auditadas.
- [ ] Sem segredos/alvos reais; suite determinística em CI.

### Handoff para Claude Code

```text
És o executor do ticket AOS-117 do Agentic OS de Referência (AOS).

Lê AOS-117 em specs/EPIC-11_Testes_Qualidade.md, specs/EPIC-07 e ADR-005/ADR-002/ADR-004.

OBJECTIVO: suite red-team adversarial (prompt injection, exfiltração).
- Corpus de payloads: injecção directa/indirecta, ofuscação (base64/metacaracteres/symlink), memory poisoning.
- Prova que conteúdo untrusted não autoriza tool calls privilegiadas (taint).
- Prova que egress default-deny bloqueia exfiltração para fora da allowlist.
- Cada tentativa é mediada pelo RM e fica auditada (falha E regista).
- Endurece o hallucination gate (assinatura) e re-aprovação de schema MCP.

NÃO uses segredos reais nem alvos externos reais — simula no ambiente efémero de AOS-110.
Um enfraquecimento da fronteira control/data-plane tem de falhar em vermelho.
Commits Conventional (feat(AOS-117): ...), branch feature/AOS-117-redteam-tests, PR pelo template da §7.
```

---

## AOS-118 — Testes de DR/replay end-to-end

| Campo | Valor |
|---|---|
| Epic | EPIC-11 — Testes e Qualidade |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-111, AOS-116 |
| Bloqueia | — |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `specs/EPIC-10_Topologia_Operacao_DR.md`, `specs/EPIC-08_Observabilidade_Evals.md`, `tecnica/11_Convencoes_Engenharia_Evolucao.md` (ADR-007, ADR-001, ADR-010) |

### Contexto

A disponibilidade do plano de controlo (99,9%) e a durabilidade (0 efeitos duplicados no retry) dependem de um Event Store replicado sem SPOF e de workers *stateless* (ADR-007). Estas propriedades só valem se forem verificadas end-to-end: perder um nó, promover a réplica e retomar as trajectórias em curso *resume-from-step* sem perder eventos nem duplicar efeitos. Este ticket é o teste de fogo que junta idempotência (AOS-112), replay (AOS-111) e escala (AOS-116) num cenário de desastre realista.

### Objectivo

Construir um teste de DR/replay end-to-end que, com trajectórias em curso e carga concorrente, simula a perda de um nó do Event Store, exerce o failover para a réplica e prova que as trajectórias retomam *resume-from-step* sem perda de eventos, sem efeitos duplicados e sem violação de soberania (failover não cruza fronteira).

### Critérios de Aceitação

- [ ] Com trajectórias activas e carga concorrente, a injecção de falha de um nó do Event Store não perde eventos confirmados (durabilidade provada por reconciliação do log).
- [ ] Após o failover, as trajectórias em curso retomam *resume-from-step* e concluem com o mesmo resultado esperado (replay fiel end-to-end).
- [ ] Nenhum efeito externo é duplicado durante o failover/retry (idempotência sob desastre, reutilizando AOS-112).
- [ ] O failover **não** cruza fronteira de soberania (allowlist regional respeitada), coerente com o requisito de conformidade.
- [ ] O teste mede o tempo de recuperação (MTTR) e confirma que o objectivo de disponibilidade do plano de controlo não é violado no cenário.

### Detalhes Técnicos

- Componentes-alvo: ES (replicado), SCH (leases/fencing), OBS (replay), GOV (soberania). Correr sobre o ambiente efémero de AOS-110 com replicação real e carga de AOS-116.
- Cenário: matar o nó primário a meio de escritas; promover réplica; verificar fencing tokens (nenhuma escrita de nó obsoleto vinga) e retoma das trajectórias.
- Reportar `MTTR` e `Replay-fidelity` no cenário de desastre.

### Testes Requeridos

- Perda de nó com trajectórias activas: zero eventos confirmados perdidos.
- Failover + retoma *resume-from-step* até conclusão correcta.
- Zero efeitos duplicados sob failover (idempotência).
- Failover não cruza fronteira de soberania.

### Definition of Done

- [ ] Todos os Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Código revisto (mínimo um revisor humano).
- [ ] **Replay determinístico testado** end-to-end e **idempotência por passo verificada** sob desastre (ADR-010, ADR-001).
- [ ] Durabilidade sem SPOF exercida; soberania respeitada (ADR-007).
- [ ] `MTTR` e `Replay-fidelity` reportados; cenário reprodutível.

### Handoff para Claude Code

```text
És o executor do ticket AOS-118 do Agentic OS de Referência (AOS).

Lê AOS-118 em specs/EPIC-11_Testes_Qualidade.md, specs/EPIC-10, specs/EPIC-08 e ADR-007/ADR-001/ADR-010.

OBJECTIVO: teste de DR/replay end-to-end.
- Com trajectórias activas e carga concorrente, mata um nó do Event Store.
- Prova zero eventos confirmados perdidos e failover para réplica.
- Retoma trajectórias resume-from-step até conclusão correcta (replay fiel end-to-end).
- Prova zero efeitos duplicados sob failover (reutiliza AOS-112).
- Confirma que o failover NÃO cruza fronteira de soberania; reporta MTTR e Replay-fidelity.

Reutiliza replay (AOS-111), idempotência (AOS-112) e carga (AOS-116) sobre o ambiente de AOS-110.
Commits Conventional (feat(AOS-118): ...), branch feature/AOS-118-dr-replay-e2e, PR pelo template da §7.
```

---

## Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

## Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Emissão inicial | Equipa AOS |
