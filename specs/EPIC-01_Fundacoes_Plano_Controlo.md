# EPIC-01 — Fundações e Plano de Controlo

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | `Epic` — EPIC-01 — Fundações e Plano de Controlo |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/00_Arquitectura_Solucao.md`, `tecnica/01_Reference_Monitor_Plano_Controlo.md`, `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `tecnica/12_Contratos_de_Interface.md`, `tecnica/13_Modelo_Dados_Eventos.md` |

---

## Visão do Epic

Esta epic materializa a **Fase 0 — Fundações (P0)** do roadmap: sem ela, tudo o resto é aspiracional. O objectivo é levar a plataforma de M0 (Ad-hoc, "chatbot com plugins") para **M1 (Recuperável)** e cravar as âncoras que permitem alcançar **M2 (Mediado)** — nomeadamente o *reference monitor* físico e a identidade por agente.

Concretiza directamente as **três fundações não-negociáveis** da tese central do produto: (1) **mediação total** — um Reference Monitor mandatório por onde passa toda a tool call (ADR-002); (2) **identidade antes de autoridade** — cada agente é uma *non-human identity* (NHI) única com token *scoped/time-bound* numa cadeia de delegação *on-behalf-of* que termina num humano responsável (ADR-003); e (3) o substrato durável — um **Event Store replicado append-only** com transporte push que substitui o SQLite single-writer (SPOF e tecto de throughput) da abordagem ingénua (ADR-007).

Sobre estas fundações assentam quatro capacidades de controlo indispensáveis: o **Policy Decision Point (PDP)** com policy-as-code (ADR-011), a **capability allowlist default-deny**, o **orçamento hierárquico com reserva atómica** (ADR-008) e a **base de audit tamper-evident hash-chained** (ADR-010). Fecha-se o ciclo com o esqueleto do plano de controlo (Orquestrador/Escalonador em stubs contratuais) e o pipeline de CI com gates de qualidade e segurança, mais o monorepo e ambientes via IaC — a superfície mínima para que toda a construção subsequente (EPIC-02 em diante) seja verificável, reproduzível e governável desde o primeiro commit.

A regra de ouro herdada da fonte: **as fronteiras fazem o SO, não as features**. Esta epic instala as fronteiras nos sítios certos — segurança (RM), identidade (NHI) e durabilidade (Event Store) — para tornar as falhas *arquitecturalmente impossíveis*.

---

## Critérios de Saída do Epic

- [ ] Monorepo operacional com ambientes **dev** e **staging** provisionados por IaC, reproduzíveis por `apply` idempotente e destruíveis sem resíduos.
- [ ] **Event Store** replicado append-only em produção-staging, com esquema de eventos versionado e transporte push funcional (fan-out < 250 ms p95 intra-cluster).
- [ ] **Nenhum** caminho de código consegue executar uma tool sem atravessar o Reference Monitor (verificado por teste de negação e por análise estática); overhead de mediação p95 < 15 ms.
- [ ] **PDP** avalia policy-as-code (Rego/OPA ou Cedar) versionada e assinada, com decisão `allow/deny` determinística e explicável por tool call.
- [ ] Cada agente/sub-agente possui **NHI única** com token scoped/time-bound; cadeia de delegação on-behalf-of resolve sempre até um humano responsável.
- [ ] **Capability allowlist default-deny** activa: capability não listada é negada e o evento de negação é auditado.
- [ ] **Orçamento hierárquico** impõe limite por árvore com reserva atómica (compare-and-swap) sem corrida no contador partilhado.
- [ ] **Audit hash-chain** verificável: qualquer adulteração de um registo é detectável por re-verificação da cadeia.
- [ ] **CI** bloqueia merge em falha de build/lint/test/SAST/SCA/teste-de-política; cobertura de linhas ≥ 80% nos módulos do kernel.
- [ ] Esqueleto do plano de controlo (Orquestrador/Escalonador) publica e consome eventos do barramento com contratos estáveis, ainda que em stub.

---

## Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-001 | Bootstrap do monorepo e ambientes (dev/staging) via IaC | chore | M | P0 | — |
| AOS-002 | Event Store replicado append-only (esquema + transporte push) | feature | L | P0 | AOS-001 |
| AOS-003 | Reference Monitor: interposição obrigatória de toda a tool call (PEP) | feature | L | P0 | AOS-002 |
| AOS-004 | Policy Decision Point (PDP) com policy-as-code (Rego/OPA ou Cedar) | feature | L | P0 | AOS-003 |
| AOS-005 | Identidade não-humana por agente (token scoped/time-bound) | feature | M | P0 | AOS-002 |
| AOS-006 | Cadeia de delegação on-behalf-of até humano responsável | feature | M | P0 | AOS-005 |
| AOS-007 | Capability allowlist default-deny | feature | S | P0 | AOS-004, AOS-005 |
| AOS-008 | Orçamento hierárquico com reserva atómica (compare-and-swap) | feature | M | P1 | AOS-002, AOS-003 |
| AOS-009 | Barramento de eventos push + subscrições | feature | M | P1 | AOS-002 |
| AOS-010 | CI inicial + gates (build/lint/test/SAST/SCA/teste de política) | chore | M | P0 | AOS-001, AOS-004 |
| AOS-011 | Base de audit tamper-evident (hash-chain) | feature | M | P1 | AOS-002 |
| AOS-012 | Esqueleto do plano de controlo (Orquestrador/Escalonador stubs) | feature | S | P1 | AOS-003, AOS-009 |
| AOS-287 | Consumo de tool calls durável: fechar a fuga que o AOS-256 deixou declarada | fix | M | P1 | AOS-256, AOS-008 |

---

## AOS-001 — Bootstrap do monorepo e ambientes (dev/staging) via IaC

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | chore |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | — |
| Bloqueia | AOS-002, AOS-010 |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `tecnica/00_Arquitectura_Solucao.md`, `tecnica/10_Topologia_Implantacao_Operacao.md` |

### Contexto
Todo o conjunto documental assume um monorepo com camadas bem separadas (plano de controlo, plano de execução, serviços de plataforma, log & substrato). Sem uma base reproduzível de repositório e ambientes, cada ticket subsequente diverge em convenções e a promoção dev→staging torna-se manual e não-auditável. IaC é o único mecanismo compatível com a exigência de disponibilidade sem SPOF (ADR-007) e com replay/reprodutibilidade.

### Objectivo
Estabelecer o monorepo com estrutura de pacotes canónica, *tooling* partilhado (lint, format, test, versionamento) e ambientes **dev** e **staging** provisionados 100% por Infrastructure-as-Code, idempotentes e efémeros.

### Critérios de Aceitação (SMART)
- [ ] Monorepo criado com pastas de topo `packages/` (kernel, control-plane, platform, substrate), `specs/`, `tecnica/`, `infra/`, `.github/` e um gestor de workspaces único.
- [ ] `infra apply` provisiona um ambiente completo (rede, Event Store, secret store placeholder) em < 15 min e é idempotente (segundo `apply` sem alterações reporta 0 mudanças).
- [ ] `infra destroy` remove todos os recursos sem resíduos verificáveis (0 recursos órfãos no relatório de estado).
- [ ] Ambientes `dev` e `staging` isolados por *namespace*/conta, com parametrização por ficheiro de variáveis versionado (sem segredos em claro).
- [ ] README de arranque permite a um novo engenheiro obter ambiente dev funcional em ≤ 30 min seguindo apenas passos documentados.

### Detalhes Técnicos
- `infra/` com IaC declarativo (Terraform ou Pulumi — *(proposta)*); estado remoto com locking.
- Módulos separados: `infra/modules/network`, `infra/modules/eventstore`, `infra/modules/secrets`.
- Workspaces do monorepo (pnpm/turbo, cargo workspaces ou equivalente conforme stack escolhida em `tecnica/00`).
- Convenções de código e commits herdadas de `specs/01_Engineering_Standards_e_Handoff.md`.

### Testes Requeridos
- Teste de idempotência do `apply` (dois runs consecutivos → diff vazio).
- Teste de *tear-down* (destroy → verificação de ausência de recursos).
- Smoke test de conectividade entre ambiente e Event Store placeholder.

### Definition of Done
- [ ] Código IaC revisto e merged na branch principal.
- [ ] Pipeline consegue provisionar `dev` e `staging` a partir do zero.
- [ ] Documentação de arranque validada por um segundo engenheiro.
- [ ] Nenhum segredo em claro no repositório (verificado por scanner).

### Handoff para Claude Code
```
Cria o esqueleto do monorepo AOS e a IaC de dev/staging.
- Estrutura: packages/{kernel,control-plane,platform,substrate}, infra/, specs/, tecnica/.
- IaC declarativo com estado remoto e locking; módulos network/eventstore/secrets.
- Garante `apply` idempotente e `destroy` limpo; parametriza dev vs staging por var-file versionado (sem segredos).
- Adiciona README de arranque (<=30 min). Segue specs/01_Engineering_Standards_e_Handoff.md.
Não implementes lógica de negócio; só fundação e ambientes.
```

---

## AOS-002 — Event Store replicado append-only (esquema de eventos + transporte push)

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-001 |
| Bloqueia | AOS-003, AOS-005, AOS-008, AOS-009, AOS-011 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/00_Arquitectura_Solucao.md`, `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `tecnica/04_Memoria_Persistencia.md` |

### Contexto
A fonte é explícita: o event bus deixa de ser SQLite single-writer (SPOF e tecto de throughput) para ser um **event store replicado com transporte push** (ADR-007). É a fonte de verdade de toda a plataforma — turnos, tool calls, resultados, decisões de política e audit derivam dele. Sem este substrato durável não há M1 (Recuperável) nem replay determinístico.

### Objectivo
Implementar um Event Store append-only replicado com esquema de eventos versionado, escrita durável e transporte push para subscritores, servindo de fonte de verdade única e ordenada por stream.

### Critérios de Aceitação (SMART)
- [ ] Eventos são **append-only**: nenhuma API permite update/delete de um evento já persistido (tentativa devolve erro e é auditada).
- [ ] Esquema de evento versionado com campos mínimos: `event_id`, `stream_id`, `seq` (monotónico por stream), `type`, `ts`, `producer` (principal/NHI), `payload`, `schema_version`.
- [ ] Ordenação total por `(stream_id, seq)` garantida; escritas concorrentes ao mesmo stream resolvidas por sequência sem perda.
- [ ] Replicação activa (factor ≥ 3 *(proposta)*): perda de um nó não perde eventos confirmados (teste de falha de nó passa).
- [ ] Transporte **push** entrega eventos a subscritores com latência de fan-out < 250 ms p95 intra-cluster.
- [ ] Idempotência de escrita por `idempotency_key` = f(`run_id`, `step_id`): re-escrita do mesmo passo não duplica evento (ADR-001).

### Detalhes Técnicos
- Backend: NATS JetStream, Redis Streams ou Postgres logical (conforme decisão em `tecnica/00`); transporte push nativo.
- `packages/substrate/eventstore`: API `append(stream, event)`, `read(stream, from_seq)`, `subscribe(filter)`.
- Registo de esquemas com `schema_version` e validação na escrita; evolução compatível (expand/contract).
- Escrita com confirmação de quórum antes de ACK ao produtor.

### Testes Requeridos
- Teste de append-only (rejeição de mutação/apagamento).
- Teste de ordenação e monotonicidade de `seq` sob concorrência.
- Teste de idempotência por `idempotency_key`.
- Teste de tolerância a falha de nó (kill de réplica → 0 eventos confirmados perdidos).
- Teste de latência de fan-out push.

### Definition of Done
- [ ] API do Event Store documentada e coberta por testes (≥ 80% linhas).
- [ ] Replicação e failover validados em staging.
- [ ] Esquema de eventos publicado e versionado no registo.
- [ ] Benchmark de throughput e latência registado.

### Handoff para Claude Code
```
Implementa o Event Store replicado append-only do AOS em packages/substrate/eventstore.
- API: append/read/subscribe; ordenação total por (stream_id, seq) monotónico.
- Esquema de evento versionado (event_id, stream_id, seq, type, ts, producer, payload, schema_version).
- Append-only estrito (sem update/delete); idempotência por f(run_id, step_id).
- Transporte push a subscritores; replicação com quórum e failover sem perda de eventos confirmados.
Escreve testes de append-only, ordenação, idempotência, failover e latência de fan-out.
```

---

## AOS-003 — Reference Monitor: interposição obrigatória de toda a tool call (PEP) [ADR-002]

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-002 |
| Bloqueia | AOS-004, AOS-008, AOS-012 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/01_Reference_Monitor_Plano_Controlo.md`, `tecnica/00_Arquitectura_Solucao.md` |

### Contexto
O Reference Monitor (RM) é o **Policy Enforcement Point (PEP)** e a primeira fundação não-negociável: **nenhum caminho de código chama uma tool directamente** (ADR-002). É isto que torna segurança, governação e observabilidade *transversais* em vez de aspiracionais. Toda a tool call atravessa-o para aplicar identidade, política, orçamento, egress e audit **antes** de executar.

### Objectivo
Implementar o RM como gate físico obrigatório entre o Agent Runtime e qualquer efeito externo, com uma superfície única `mediate(call)` que nenhum caminho de execução possa contornar, cumprindo overhead p95 < 15 ms.

### Critérios de Aceitação (SMART)
- [ ] Toda a tool call passa por `RM.mediate(context, call)`; não existe API pública de execução de tool fora do RM (verificado por análise estática e por teste de negação de bypass).
- [ ] O RM invoca, por ordem, os pontos de decisão: identidade (AOS-005), política (AOS-004, stub inicial `allow`), orçamento (AOS-008, stub), audit (AOS-011, stub) — com interfaces estáveis para injecção posterior.
- [ ] Decisão `deny` **impede** a execução (fail-closed) e emite evento de negação no Event Store.
- [ ] Overhead de mediação p95 < 15 ms com política em memória (medido em benchmark).
- [ ] Cada mediação grava um span/evento com `call`, decisão, latência e principal, correlacionado por `run_id`/`step_id`.

### Detalhes Técnicos
- `packages/kernel/reference-monitor`: interface `mediate(ctx, call) -> Decision`.
- Cadeia de *decision hooks* pluggable: `IdentityCheck`, `PolicyCheck`, `BudgetCheck`, `EgressCheck`, `AuditSink` (contratos definidos; implementações reais chegam em AOS-004/005/008/011).
- Chamada a tools apenas via *dispatcher* interno accionado pelo RM; despacho directo proibido por lint rule/arquitectura.
- Escritas de audit e eventos via AOS-002.

### Testes Requeridos
- Teste de negação de bypass (tentativa de executar tool sem passar pelo RM falha em build/test).
- Teste fail-closed (deny impede efeito e é auditado).
- Teste de ordem de hooks e de propagação de contexto (principal, run_id, step_id).
- Benchmark de overhead p95.

### Definition of Done
- [ ] RM merged com hooks contratuais e stubs neutros.
- [ ] Regra de arquitectura/lint que proíbe despacho directo de tools activa no CI.
- [ ] Benchmark de overhead documentado (< 15 ms p95).
- [ ] Eventos de mediação visíveis no Event Store.

### Handoff para Claude Code
```
Implementa o Reference Monitor (PEP) do AOS em packages/kernel/reference-monitor.
- Superfície única mediate(ctx, call) -> Decision; nenhum caminho executa tools fora dele.
- Cadeia de hooks: IdentityCheck, PolicyCheck, BudgetCheck, EgressCheck, AuditSink (contratos + stubs neutros).
- Fail-closed em deny; grava evento de mediação no Event Store (AOS-002) com run_id/step_id/latência.
- Adiciona regra de arquitectura/lint que proíbe despacho directo de tools; teste de bypass deve falhar.
Objectivo de overhead p95 < 15 ms; inclui benchmark.
```

---

## AOS-004 — Policy Decision Point (PDP) com policy-as-code (Rego/OPA ou Cedar) [ADR-011]

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-003 |
| Bloqueia | AOS-007, AOS-010 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/01_Reference_Monitor_Plano_Controlo.md`, `tecnica/09_Governacao_Conformidade.md` |

### Contexto
O PDP é o par do PEP (=RM): avalia **policy-as-code** por tool call (ADR-011). As políticas são versionadas em git, assinadas, com o changelog no próprio audit trail. Substitui blocklists frágeis por decisões declarativas, explicáveis e auditáveis — base do GDPR/EU AI Act por desenho.

### Objectivo
Implementar o PDP com um motor de política declarativo (Rego/OPA ou Cedar), políticas versionadas e assinadas, e uma interface `decide(input) -> {allow|deny, reason}` invocada pelo `PolicyCheck` do RM.

### Critérios de Aceitação (SMART)
- [ ] `PDP.decide(input)` devolve decisão determinística `allow/deny` com `reason` legível, dado `input` = (principal/NHI, capability, recurso, contexto).
- [ ] Políticas escritas em policy-as-code (Rego/OPA ou Cedar), versionadas em git e **assinadas**; a versão de política usada em cada decisão é registada no evento de audit.
- [ ] Política **compilada em memória** para cumprir overhead do RM (< 15 ms p95 combinado).
- [ ] Alteração de política requer commit assinado; changelog reflectido no audit trail (verificável).
- [ ] Conjunto inicial de políticas cobre default-deny de capabilities (integra AOS-007) e regras base de identidade.

### Detalhes Técnicos
- `packages/control-plane/pdp`: bundle de políticas + motor (OPA embutido ou Cedar).
- Verificação de assinatura do bundle no carregamento; rejeição fail-closed de bundle não-assinado.
- Contrato de `input`/`decision` estável, consumido pelo `PolicyCheck` do RM (AOS-003).
- Hot-reload de bundle só em versão nova assinada (sem mutação em runs vivos).

### Testes Requeridos
- Testes de política (golden): casos allow/deny com asserções de `reason`.
- Teste de rejeição de bundle não-assinado (fail-closed).
- Teste de registo da versão de política no evento de decisão.
- Benchmark de latência de avaliação em memória.

### Definition of Done
- [ ] PDP integrado no RM via `PolicyCheck`.
- [ ] Bundle de políticas versionado e assinado no repositório.
- [ ] Suite de testes de política no CI (ver AOS-010).
- [ ] Decisões registam versão de política no audit.

### Handoff para Claude Code
```
Implementa o PDP do AOS em packages/control-plane/pdp com policy-as-code (OPA/Rego ou Cedar).
- decide(input) -> {allow|deny, reason}; input = (principal/NHI, capability, recurso, contexto).
- Políticas versionadas em git e assinadas; verifica assinatura no load (fail-closed) e regista versão no evento de decisão.
- Compila política em memória; integra como PolicyCheck do Reference Monitor (AOS-003).
- Inclui suite de testes golden de política (allow/deny + reason) para o CI.
```

---

## AOS-005 — Identidade não-humana por agente (token scoped/time-bound) [ADR-003]

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-002 |
| Bloqueia | AOS-006, AOS-007 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md`, `tecnica/01_Reference_Monitor_Plano_Controlo.md` |

### Contexto
Segunda fundação não-negociável: **identidade antes de autoridade** (ADR-003). Cada agente e sub-agente é uma *non-human identity* (NHI) única, com token *scoped* e *time-bound*. O `credential pool` round-robin anónimo destrói a atribuição de identidade — base de toda a conformidade — e é proibido. Autoridade = utilizador ∩ classe de agente, imposta pelo kernel.

### Objectivo
Implementar a emissão e verificação de identidades não-humanas por agente: tokens scoped/time-bound que codificam o par (utilizador, agente) e a classe/política sob a qual o agente actua, consumidos pelo `IdentityCheck` do RM.

### Critérios de Aceitação (SMART)
- [ ] Cada agente/sub-agente recebe uma **NHI única** com token *scoped* (capabilities/recursos) e *time-bound* (TTL curto configurável).
- [ ] O token codifica `(user_id, agent_id, agent_class, policy_ref)` e é verificável criptograficamente (assinatura).
- [ ] Token expirado ou fora de escopo é **rejeitado** pelo RM (fail-closed) e a negação é auditada.
- [ ] **Proibição de identidade anónima/round-robin**: nenhuma chamada mediada prossegue sem NHI resolvida (teste de negação passa).
- [ ] Emissão e revogação de NHI registadas como eventos no Event Store.

### Detalhes Técnicos
- `packages/platform/identity`: emissor de tokens (JWT/PASETO assinado *(proposta)*) com claims scoped/time-bound.
- `IdentityCheck` do RM valida assinatura, TTL e escopo; resolve principal para o PDP.
- Revogação por lista/introspecção; TTL curto minimiza janela.
- Nunca expõe chaves de infra do provider (separação identidade vs chaves — ver EPIC-06/07).

### Testes Requeridos
- Teste de emissão e verificação de NHI válida.
- Teste de rejeição de token expirado e fora de escopo (fail-closed).
- Teste de proibição de chamada sem NHI.
- Teste de registo de emissão/revogação no Event Store.

### Definition of Done
- [ ] Emissor/verificador de NHI merged e integrado no RM.
- [ ] Escopo e TTL configuráveis por classe de agente.
- [ ] Eventos de identidade auditáveis.
- [ ] Cobertura de testes ≥ 80% no módulo de identidade.

### Handoff para Claude Code
```
Implementa a identidade não-humana (NHI) por agente do AOS em packages/platform/identity.
- Emite tokens scoped/time-bound assinados com claims (user_id, agent_id, agent_class, policy_ref).
- IdentityCheck do Reference Monitor (AOS-003) valida assinatura, TTL e escopo; resolve o principal para o PDP.
- Proíbe qualquer chamada mediada sem NHI resolvida (fail-closed); regista emissão/revogação no Event Store.
Não implementes credential broker downstream (fica para EPIC-07); só a identidade do agente.
```

---

## AOS-006 — Cadeia de delegação on-behalf-of até humano responsável

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-005 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md`, `tecnica/01_Reference_Monitor_Plano_Controlo.md` |

### Contexto
A identidade por agente só cumpre a sua função de governação se cada acção puder ser rastreada até um humano responsável. A cadeia de delegação *on-behalf-of* resolve o cenário *The Audit Log Lied*: quando o regulador pergunta *quem autorizou* uma acção, a resposta nunca pode ser "o pool". A cadeia termina **sempre** num humano (ADR-003).

### Objectivo
Implementar a propagação e verificação da cadeia de delegação on-behalf-of, garantindo que qualquer sub-agente herda uma cadeia que resolve, sem lacunas, até um humano responsável identificável.

### Critérios de Aceitação (SMART)
- [ ] Ao criar um sub-agente, a sua NHI incorpora a **cadeia de delegação** do agente-pai (`delegation_chain`) mais o novo elo.
- [ ] Qualquer ponto da cadeia é resolúvel até um `human_principal` único (0 cadeias órfãs em teste).
- [ ] A profundidade e a autoridade delegada **nunca aumentam** ao descer a cadeia (autoridade do filho ⊆ autoridade do pai — verificado).
- [ ] O RM/PDP pode negar uma acção se a cadeia não resolver até humano (fail-closed) e audita a negação.
- [ ] A cadeia é registada em cada evento de tool call, permitindo reconstruir *quem autorizou* qualquer efeito.

### Detalhes Técnicos
- `packages/platform/identity/delegation`: estrutura `delegation_chain` assinada e encadeada (cada elo assina o seguinte).
- Verificação de monotonicidade de autoridade (intersecção de escopos) na criação de sub-NHI.
- Integração com AOS-005 (emissão) e AOS-003 (verificação no RM).
- `human_principal` como raiz obrigatória.

### Testes Requeridos
- Teste de propagação da cadeia pai→filho→neto até humano.
- Teste de recusa de escopo alargado no filho.
- Teste de detecção de cadeia órfã (sem humano na raiz).
- Teste de reconstrução de autoria a partir de um evento de tool call.

### Definition of Done
- [ ] Cadeia de delegação integrada na emissão de sub-NHI.
- [ ] Verificação de não-escalada de autoridade activa.
- [ ] Eventos de tool call incluem `delegation_chain`.
- [ ] Testes de auditoria de autoria a passar.

### Handoff para Claude Code
```
Implementa a cadeia de delegação on-behalf-of do AOS em packages/platform/identity/delegation.
- Sub-agentes herdam delegation_chain assinada do pai + novo elo; a raiz é sempre um human_principal.
- Garante autoridade(filho) ⊆ autoridade(pai) (sem escalada); nega e audita cadeias que não resolvam até humano.
- Regista a cadeia em cada evento de tool call para reconstruir "quem autorizou".
Integra com AOS-005 (emissão NHI) e AOS-003 (verificação no RM). Inclui testes de propagação, não-escalada e cadeia órfã.
```

---

## AOS-007 — Capability allowlist default-deny

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | S |
| Dependências | AOS-004, AOS-005 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md`, `tecnica/01_Reference_Monitor_Plano_Controlo.md` |

### Contexto
A blocklist de tools de sub-agente **falha aberta** a cada tool nova — vulnerabilidade estrutural. A fonte substitui-a por uma **allowlist capability-scoped default-deny**: o que não está explicitamente permitido é negado. É o comportamento seguro por omissão que sustenta a governação.

### Objectivo
Implementar o modelo de capabilities com allowlist default-deny, integrada no PDP, de modo que uma capability não listada para o principal seja sempre negada e auditada.

### Critérios de Aceitação (SMART)
- [ ] Modelo de capabilities declarativo: cada NHI/classe tem uma **allowlist** explícita de capabilities.
- [ ] Capability **não listada** → decisão `deny` por omissão (fail-closed), com evento de negação auditado (0 falsos allow em teste de fuzz de capabilities).
- [ ] A allowlist é avaliada pelo PDP (AOS-004) usando a identidade do principal (AOS-005).
- [ ] Adição de capability requer alteração explícita e assinada da política (sem allow implícito).
- [ ] Capability nova introduzida sem entrada na allowlist é negada até ser explicitamente permitida (teste de "tool nova" passa).

### Detalhes Técnicos
- Definição de capabilities como recurso de política no PDP (`packages/control-plane/pdp/policies/capabilities`).
- Resolução `principal → capabilities permitidas` no `PolicyCheck` do RM.
- Sem *wildcards* perigosos por omissão; wildcards só com justificação explícita na política.

### Testes Requeridos
- Teste default-deny (capability ausente → deny + audit).
- Teste de "tool nova falha fechada".
- Teste de allow explícito por política assinada.
- Fuzz de capabilities aleatórias (0 allow indevido).

### Definition of Done
- [ ] Allowlist default-deny activa no PDP.
- [ ] Negações auditadas com capability e principal.
- [ ] Política de capabilities versionada e assinada.
- [ ] Testes de fuzz a passar no CI.

### Handoff para Claude Code
```
Implementa a capability allowlist default-deny do AOS no PDP (packages/control-plane/pdp/policies/capabilities).
- Cada NHI/classe tem allowlist explícita; capability não listada -> deny (fail-closed) + evento de negação auditado.
- Avalia via PolicyCheck do RM usando a identidade (AOS-005); adição de capability exige política assinada.
- Sem wildcards por omissão. Inclui teste "tool nova falha fechada" e fuzz de capabilities (0 allow indevido).
```

---

## AOS-008 — Orçamento hierárquico com reserva atómica (compare-and-swap) [ADR-008]

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-002, AOS-003 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/03_Orquestracao_Escalonamento.md`, `tecnica/01_Reference_Monitor_Plano_Controlo.md` |

### Contexto
O cap de delegação fixo de 2 e o contador partilhado da abordagem ingénua sofrem de corrida (race) no spawn. A fonte substitui-os por **orçamento hierárquico configurável com reserva atómica** — compare-and-swap antes do spawn — eliminando a corrida e permitindo map-reduce recursivo legítimo (ADR-008). O orçamento é denominado em tokens/$ (não iterações, proxy péssimo).

### Objectivo
Implementar o orçamento hierárquico por árvore de execução, com reserva atómica (compare-and-swap) de débito antes de spawn ou tool call, integrado no `BudgetCheck` do RM.

### Critérios de Aceitação (SMART)
- [ ] Orçamento definido por **árvore de execução** (raiz e sub-árvores), denominado em tokens **e** custo ($), não em iterações.
- [ ] Reserva de débito por **compare-and-swap atómico**: dois spawns concorrentes nunca ultrapassam o limite (teste de concorrência com N goroutines/threads passa, 0 overshoot).
- [ ] `BudgetCheck` do RM nega tool call/spawn sem headroom reservado (fail-closed) e audita a negação.
- [ ] Reserva não consumida é **libertada** (rollback) em falha/cancelamento, sem *leak* de orçamento.
- [ ] Estado de orçamento reconstruível a partir de eventos do Event Store (consistência com AOS-002).

### Detalhes Técnicos
- `packages/control-plane/budget`: contador hierárquico com operação CAS (backing distribuído — Redis/consenso).
- API `reserve(tree_id, amount) -> {ok|denied}`, `commit(reservation)`, `release(reservation)`.
- Integração como `BudgetCheck` do RM (AOS-003); débito reservado antes de spawn/execução.
- Circuit breaker por trip de custo/token (integração leve; detalhe completo em EPIC-08).

### Testes Requeridos
- Teste de concorrência CAS (0 overshoot com reservas simultâneas).
- Teste de libertação em falha (sem leak).
- Teste de negação por falta de headroom (fail-closed + audit).
- Teste de reconstrução do estado a partir de eventos.

### Definition of Done
- [ ] Orçamento hierárquico integrado no RM.
- [ ] Reserva atómica validada sob concorrência.
- [ ] Rollback de reserva testado.
- [ ] Contabilidade coerente com o Event Store.

### Handoff para Claude Code
```
Implementa o orçamento hierárquico do AOS em packages/control-plane/budget.
- Orçamento por árvore de execução em tokens E custo ($); reserva por compare-and-swap atómico antes de spawn/tool call.
- API reserve/commit/release; BudgetCheck do RM (AOS-003) nega sem headroom (fail-closed) e audita.
- Liberta reserva não consumida (sem leak); estado reconstruível a partir do Event Store (AOS-002).
Inclui teste de concorrência (0 overshoot), rollback e negação por falta de headroom.
```

---

## AOS-009 — Barramento de eventos push + subscrições

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-002 |
| Bloqueia | AOS-012 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/00_Arquitectura_Solucao.md`, `tecnica/03_Orquestracao_Escalonamento.md` |

### Contexto
O plano de controlo escala horizontalmente com **push event-driven** (o Escalonador empurra trabalho a workers stateless). O barramento de eventos com subscrições é a camada de distribuição sobre o Event Store (AOS-002) que desacopla produtores de consumidores e evita *polling*.

### Objectivo
Implementar o barramento de eventos com transporte push e um modelo de subscrições por filtro (tipo, stream, principal), com entrega fiável e reprocessamento a partir de um cursor.

### Critérios de Aceitação (SMART)
- [ ] Subscritores registam-se por filtro (`type`, `stream_id`, `producer`) e recebem eventos por **push** (sem polling).
- [ ] Entrega **at-least-once** com cursor durável por subscritor; reinício retoma do último `seq` confirmado (0 eventos saltados).
- [ ] Reprocessamento a partir de um `seq` arbitrário suportado (replay de subscrição).
- [ ] Consumidores lentos não bloqueiam produtores (backpressure/buffer com política de degradação declarada).
- [ ] Latência de entrega push < 250 ms p95 intra-cluster (coerente com AOS-002).

### Detalhes Técnicos
- `packages/substrate/bus`: camada sobre o Event Store (AOS-002); `subscribe(filter, handler, from_seq?)`.
- Cursores duráveis por consumidor (ACK explícito); *dead-letter* para handlers que falham repetidamente.
- Idempotência do lado do consumidor recomendada (documentar contrato at-least-once).

### Testes Requeridos
- Teste de fan-out por filtro (múltiplos subscritores, entregas correctas).
- Teste de retoma por cursor após reinício (0 skips).
- Teste de replay de subscrição a partir de `seq`.
- Teste de backpressure com consumidor lento.

### Definition of Done
- [ ] Barramento push com subscrições operacional sobre o Event Store.
- [ ] Cursores duráveis e replay validados.
- [ ] Contrato at-least-once documentado.
- [ ] Métricas de latência de entrega expostas.

### Handoff para Claude Code
```
Implementa o barramento de eventos push do AOS em packages/substrate/bus sobre o Event Store (AOS-002).
- subscribe(filter, handler, from_seq?) por (type, stream_id, producer); entrega push, sem polling.
- Entrega at-least-once com cursor durável por subscritor; retoma do último seq confirmado; replay a partir de seq.
- Backpressure para consumidores lentos + dead-letter. Documenta o contrato at-least-once.
Inclui testes de fan-out, retoma por cursor, replay e backpressure.
```

---

## AOS-010 — CI inicial + gates (build/lint/test/SAST/SCA/teste de política)

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | chore |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-001, AOS-004 |
| Bloqueia | — |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `specs/01_Engineering_Standards_e_Handoff.md`, `tecnica/11_Convencoes_Engenharia_Evolucao.md` |

### Contexto
Os padrões de engenharia (`specs/01`) exigem gates de CI/CD como *admission control* de qualidade e segurança desde o primeiro commit. Sem gates, a supply-chain e a política ficam por convenção — precisamente o modo de falha que a fonte quer tornar arquitecturalmente impossível.

### Objectivo
Estabelecer o pipeline de CI que executa e **bloqueia merge** em falha de build, lint, testes, SAST, SCA e teste de política (bundle do PDP), com relatórios visíveis no PR.

### Critérios de Aceitação (SMART)
- [ ] CI corre em cada PR: `build` → `lint` → `test` → `SAST` → `SCA` → `policy-test`; qualquer falha **bloqueia** o merge.
- [ ] Cobertura de linhas dos módulos do kernel ≥ 80% (gate).
- [ ] SAST sem findings de severidade alta/crítica por omissão (gate); SCA falha em dependência com CVE crítico.
- [ ] `policy-test` valida o bundle de políticas do PDP (AOS-004): golden allow/deny e verificação de assinatura.
- [ ] Pipeline reproduz-se localmente por um único comando documentado.

### Detalhes Técnicos
- `.github/workflows/ci.yml` (ou equivalente) com jobs paralelos e *required checks*.
- SAST (Semgrep/CodeQL *(proposta)*), SCA (dependência + lockfile audit), assinatura de bundle verificada.
- Regra de arquitectura de AOS-003 (proibição de despacho directo de tools) incluída no lint/análise.
- Cache de dependências para tempos de CI aceitáveis.

### Testes Requeridos
- Teste de que um PR com falha de lint/test é bloqueado.
- Teste de que política inválida/não-assinada falha o `policy-test`.
- Teste de que CVE crítico simulado falha o SCA.
- Verificação do comando local reproduzir os gates.

### Definition of Done
- [ ] Pipeline de CI activo com *required checks* na branch principal.
- [ ] Gates de cobertura, SAST, SCA e política a bloquear correctamente.
- [ ] Documentação de execução local.
- [ ] Tempos de CI dentro de alvo aceitável documentado.

### Handoff para Claude Code
```
Configura o CI inicial do AOS em .github/workflows.
- Jobs: build, lint, test, SAST, SCA, policy-test; qualquer falha bloqueia merge (required checks).
- Gate de cobertura >=80% no kernel; SAST bloqueia high/critical; SCA bloqueia CVE crítico.
- policy-test valida o bundle do PDP (AOS-004): golden allow/deny + assinatura.
- Inclui a lint rule de proibição de despacho directo de tools (AOS-003). Documenta execução local num comando.
```

---

## AOS-011 — Base de audit tamper-evident (hash-chain) [ADR-010]

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-002 |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/08_Observabilidade_Evals.md`, `tecnica/09_Governacao_Conformidade.md` |

### Contexto
O audit trail deixa de ser "append-only por convenção" em SQLite e passa a **hash-chain + WORM assinado** (ADR-010), separado dos diagnósticos efémeros. "Imutável" significa *tamper-evidence do registo*, não retenção eterna do payload — o que se reconcilia depois com crypto-shredding para o GDPR (EPIC-09). Esta base garante que qualquer adulteração é detectável.

### Objectivo
Implementar a base de audit tamper-evident: cada registo de audit encadeia o hash do anterior, tornando qualquer alteração ou remoção detectável por re-verificação da cadeia; alimentado pelo `AuditSink` do RM.

### Critérios de Aceitação (SMART)
- [ ] Cada registo de audit inclui `hash(prev_record)`, formando uma **hash-chain** contígua por partição.
- [ ] Verificador de integridade percorre a cadeia e detecta **qualquer** adulteração (alteração/remoção/inserção) — teste de tampering passa (100% detecção).
- [ ] Registos de audit são separados dos diagnósticos efémeros (store dedicado, escrita WORM-like *(proposta MVP: append-only + verificação)*).
- [ ] O `AuditSink` do RM (AOS-003) escreve decisões (allow/deny), principal, capability e versão de política no audit.
- [ ] Âncoras periódicas do hash-chain (checkpoints assinados) permitem verificação eficiente de grandes intervalos.

### Detalhes Técnicos
- `packages/platform/audit`: escrita encadeada + `verify(from, to)`.
- Hash criptográfico (SHA-256) do registo canónico serializado; encadeamento por partição/stream.
- Checkpoints assinados periódicos (âncoras); integração com o Event Store (AOS-002) como origem.
- Preparado para WORM real e crypto-shredding em EPIC-08/09 (interfaces estáveis).

### Testes Requeridos
- Teste de detecção de tampering (mutar 1 registo → verify falha).
- Teste de detecção de remoção/inserção na cadeia.
- Teste de verificação por checkpoints (âncoras).
- Teste de que decisões do RM chegam ao audit com os campos exigidos.

### Definition of Done
- [ ] Hash-chain de audit operacional e integrado no RM.
- [ ] Verificador de integridade a detectar adulterações.
- [ ] Checkpoints assinados implementados.
- [ ] Interfaces preparadas para WORM/crypto-shredding futuros.

### Handoff para Claude Code
```
Implementa a base de audit tamper-evident do AOS em packages/platform/audit.
- Cada registo encadeia hash(prev_record) (SHA-256) formando hash-chain por partição; verify(from,to) detecta qualquer adulteração.
- AuditSink do RM (AOS-003) escreve decisão (allow/deny), principal, capability e versão de política.
- Checkpoints assinados periódicos como âncoras. Deixa interfaces estáveis para WORM/crypto-shredding (EPIC-08/09).
Inclui testes de tampering (mutação/remoção/inserção) com 100% de detecção.
```

---

## AOS-012 — Esqueleto do plano de controlo (Orquestrador/Escalonador stubs)

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações e Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-003, AOS-009 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/00_Arquitectura_Solucao.md`, `tecnica/03_Orquestracao_Escalonamento.md` |

### Contexto
O plano de controlo (Orquestrador + Escalonador) é o cérebro de coordenação: decompõe objectivos em grafo de tarefas acíclico e faz durable execution com leases/fencing e backpressure. A implementação plena é EPIC-03; aqui estabelece-se o **esqueleto** com contratos e stubs para que o resto da fundação (RM, barramento, eventos) tenha um consumidor/produtor de referência e os limites de módulo fiquem cravados.

### Objectivo
Criar os stubs contratuais do Orquestrador (ORQ) e do Escalonador (SCH), publicando e consumindo eventos do barramento (AOS-009) e invocando o RM (AOS-003), com máquina de estados mínima e interfaces estáveis, sem lógica de escalonamento avançada.

### Critérios de Aceitação (SMART)
- [ ] `Orquestrador.submit(goal)` cria um run com `run_id`, emite evento de criação e devolve identificador (grafo de tarefas mínimo: 1 nó).
- [ ] `Escalonador` consome eventos de tarefa do barramento (AOS-009) e despacha via RM (AOS-003), respeitando a máquina de estados mínima (`ready → running → complete|failed`).
- [ ] Interfaces `ORQ`/`SCH` documentadas e estáveis (contratos que EPIC-03 estende sem quebra).
- [ ] Um fluxo end-to-end de brinquedo (submit → schedule → tool call mediada → evento de resultado) executa com sucesso em staging.
- [ ] Stubs claramente marcados como *não-produtivos* para lógica avançada (sem leases/fencing/deadlock ainda), documentando o que fica para EPIC-03.

### Detalhes Técnicos
- `packages/control-plane/orchestrator` e `packages/control-plane/scheduler` com interfaces + implementação stub.
- Estados persistidos como eventos no Event Store (AOS-002) via barramento (AOS-009).
- Toda a execução de tool passa pelo RM (AOS-003) — sem excepção, mesmo no stub.
- Máquina de estados mínima alinhada com a completa de `tecnica/02`/`tecnica/03`.

### Testes Requeridos
- Teste end-to-end de brinquedo (submit → resultado) com evento correlacionado por `run_id`.
- Teste de que o Escalonador só executa tools via RM.
- Teste de estabilidade de contrato (assinaturas ORQ/SCH).
- Teste de emissão/consumo de eventos no barramento.

### Definition of Done
- [ ] Stubs de ORQ e SCH merged com contratos estáveis.
- [ ] Fluxo end-to-end de brinquedo a passar em staging.
- [ ] Execução de tools exclusivamente via RM verificada.
- [ ] Pontos de extensão para EPIC-03 documentados.

### Handoff para Claude Code
```
Cria o esqueleto do plano de controlo do AOS: packages/control-plane/orchestrator e /scheduler (stubs contratuais).
- Orquestrador.submit(goal) -> run_id + evento de criação (grafo mínimo de 1 nó).
- Escalonador consome tarefas do barramento (AOS-009) e despacha SEMPRE via Reference Monitor (AOS-003).
- Máquina de estados mínima ready->running->complete|failed; estado persistido como eventos (AOS-002).
- Interfaces estáveis que a EPIC-03 estende sem quebra; marca claramente o que é stub (sem leases/fencing/deadlock ainda).
Inclui um fluxo end-to-end de brinquedo (submit -> schedule -> tool call mediada -> resultado).
```

---

## AOS-287 — Consumo de tool calls durável: fechar a fuga que o AOS-256 deixou declarada

| Campo | Valor |
|---|---|
| Epic | EPIC-01 — Fundações do Plano de Controlo |
| Fase | Fase 0 — Fundações |
| Tipo | fix |
| Prioridade | P1 |
| Estimativa | M |
| Milestone | **v1** — é um defeito de single-host, não do distribuído |
| Dependências | AOS-256 (consumo durável de turnos, ENTREGUE), AOS-008 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/integration/budget.go` (`limiteParaIncarnacao`, `ConsumoDuravel` e a nota de alcance), `packages/cmd/aos/budget_env.go` (`consumoDuravelParaOrcamento`), ADR-008, `docs/reports/auditoria-das-minhas-proprias-afirmacoes-2026-08-31.md` |

> **VERSÃO ANTERIOR DESTE TICKET ERA FALSA.** A primeira redacção (2026-08-31) afirmava
> que «o consumo do orçamento não sobrevive a um reinício» e mandava ligar
> `budget.WithEmitter` + `budget.Rebuild`. **Não é verdade** — o `AOS-256` já resolveu a
> maior parte disso por outra via — e ligar aquelas duas peças criaria uma SEGUNDA
> contabilidade do mesmo tecto. A dissecação do erro está no relatório de auditoria acima;
> este cabeçalho fica para que ninguém reconstitua a versão errada a partir do histórico.

**Contexto — o que JÁ está feito, e é preciso saber antes de tocar em nada.**

O `AOS-256` fechou a fuga principal: o nó de orçamento de um run **não** nasce com o tecto
inteiro a cada hospedagem. `RunBudget.limiteParaIncarnacao` lê o consumo já registado de uma
fonte durável e faz o nó nascer com `tecto − já consumido`. Está ligado em produção
(`bootstrap.go`, `LigarConsumoDuravel`), e degrada em voz alta (não em silêncio) se o ledger
for ilegível.

**O que falta, e está declarado no código em dois sítios:**

> o ledger conta turnos de **MODELO** e só eles. As tool calls reservam do mesmo nó e **não
> entram no ledger**, pelo que a fuga **ENCOLHE** — do tecto inteiro por incarnação para o
> consumo de tool calls por incarnação — em vez de fechar.

**Consequência real.** Um run que escale/retome N vezes pode gastar até
`tecto + N × (consumo de tool calls por incarnação)`. Escalar e retomar é o fluxo **normal**
de tudo o que exige aprovação humana, não um caso exótico — é por isso que a fuga vale a
pena fechar, mesmo tendo encolhido.

**Objectivo.** Que o consumo de **tool calls** de um run sobreviva à re-incarnação, pela
MESMA via que o consumo de turnos já usa.

**Critérios de Aceitação**
- [ ] O consumo de tool calls de um run é registado de forma **durável**, chaveado por
      `run_id` e sobrevivente à retoma — como o ledger de turnos já faz.
- [ ] A fonte que `integration.ConsumoDuravel` devolve passa a incluir esse consumo: o nó da
      incarnação seguinte nasce com `tecto − (turnos + tool calls)`.
- [ ] Um run que consome tecto em tool calls, escala e retoma, **não** recupera esse tecto.
      Falha-antes: hoje recupera-o.
- [ ] A degradação declarada mantém-se: ledger ilegível ⇒ tecto inteiro **com linha no log**,
      nunca em silêncio. Não se troca uma fuga por um run encravado — o orçamento é controlo
      de custo, não de segurança.
- [ ] As notas de alcance parcial em `integration/budget.go` e `cmd/aos/bootstrap.go` são
      **actualizadas**: deixar «a fuga ENCOLHE» escrito depois de ela fechar seria a mentira
      simétrica da que este ticket corrige.

**Detalhes Técnicos.**

**A VIA ESCOLHIDA É O LEDGER, não o event-sourcing do `budget`.** `budget.WithEmitter` e
`budget.Rebuild` existem e não têm chamadores — e isso é **deliberado**, não esquecimento: o
`AOS-256` avaliou reter o nó vivo, rejeitou-o por escrito («nós vivos para sempre num
processo de vida longa, e zerados na mesma ao primeiro restart») e escolheu ler o consumo de
onde ele já era durável. Ligar agora o `Rebuild` daria duas contabilidades do mesmo tecto, a
divergir em silêncio — o modo de falha que este repositório trata como defeito de primeira
ordem.

O trabalho é, portanto: **onde se regista o custo de uma tool call de forma durável**, e
**como o `ConsumedByRun` passa a somá-lo**. O saldo da reserva já existe
(`budgetSettlingDispatcher`); o que falta é o facto durável.

**Testes Requeridos.** Run consome em tool calls → re-incarna → o tecto restante reflecte-o.
Ledger ilegível ⇒ degradação declarada com linha no log. Turnos + tool calls somam sem
dupla contagem (a mesma tool call não conta duas vezes num retry idempotente).

**Definition of Done**
- [ ] Critérios de Aceitação satisfeitos, com o teste de re-incarnação a falhar-antes.
- [ ] `-race` verde.
- [ ] Notas de alcance parcial actualizadas nos dois sítios onde estão escritas.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-287 do Agentic OS de Referência (AOS).
Lê AOS-287 na íntegra, e lê ANTES a nota de alcance em packages/integration/budget.go
(a que começa em «O TECTO POR-RUN DEIXA DE RECOMEÇAR A CADA HOSPEDAGEM»): metade deste
problema JÁ está resolvida pelo AOS-256, e o que falta é só o consumo de TOOL CALLS.
NÃO ligues budget.WithEmitter nem budget.Rebuild. Não têm chamadores por DECISÃO — o
AOS-256 rejeitou essa via por escrito. Ligá-los criaria duas contabilidades do mesmo
tecto, a divergir em silêncio.
A via é a do ledger: registar o custo da tool call de forma durável por run_id, e somá-lo
no ConsumedByRun que o ConsumoDuravel já lê.
Não expandas escopo: este ticket NÃO reabre a forma do produto v1 (Carta §7).
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
