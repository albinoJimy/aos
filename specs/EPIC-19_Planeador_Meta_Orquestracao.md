# EPIC-19 — Planeador Produtivo e Meta-Orchestração

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Graduação do Planeador (goal→DAG) e meta-orchestração governada |
| Versão | 1.0 |
| Data | 2026-08-02 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | **`tecnica/18_Planner_Meta_Orquestracao.md` v1.0 (Ratificado 2026-08-02)** |
| Documentos relacionados | `docs/reports/revisao-tecnica18-planner-para-ratificacao.md`, `specs/EPIC-03_Orquestracao_Escalonamento.md` (AOS-025..028), `specs/EPIC-12_Experiencia_HITL_UX.md` (AOS-120/121/123/124/128), ADR-005/008/012/013/014/018 |

---

## 1. Visão do Epic

O Orquestrador declara decompor objectivos em grafo de tarefas, mas a decomposição é hoje um **stub** (um único nó derivado do `Goal`). Toda a maquinaria a jusante é real e testada — DAG (AOS-025), delegação com orçamento (AOS-026), admissão global (AOS-027/028), gate pré-spawn (AOS-121) — mas nada a alimenta. Esta epic entrega a **peça em falta**: o Planeador (PLN) como componente produtivo, e a meta-orchestração (objectivo → organigrama executável de sub-agentes, aprovado por humano antes de qualquer *spawn*), conforme o desenho **ratificado** em `tecnica/18` v1.0.

Invariante congelado (autoridade de `tecnica/18`): o **plano proposto pelo LLM é dados untrusted** (ADR-005) — nunca executado; validado por função pura, orçamentado, aprovado no gate (ADR-013) e só então materializado. O planeador é **ele próprio um agente governado** (NHI, orçamento, RM, replay). Nada aqui altera a Carta; organizações **persistentes** ficam fora de âmbito (proposta, `tecnica/18` §8).

## 2. Fronteira eu-construo vs. deployment/dependências

| Frente | Código desta epic | Fora (dependência/deployment) |
|---|---|---|
| Decomposição LLM | Planeador-agente, prompt de decomposição versionado, retry bounded | *wiring* do Model Gateway no bootstrap (EPIC-06, integração) |
| Validação & risco | Validador puro sobre snapshot; risco derivado das tools | tabela de pricing/risco por tool (AOS-062/074, já existe) |
| Gate & materialização | PlanCard organigrama triado, materialização DAG+spawn | canais HITL (AOS-119..122, já existem) |
| Capability gaps | tipo de nó + agente-autor governado + bloqueio | **executor declarativo de skills** (lacuna honesta `tecnica/18` §5 — desenho separado) |
| Eval de decomposição | golden-sets + eval-gate + trace-diffing | curadoria contínua do golden-set (encargo de propriedade) |

## 3. Critérios de Saída do Epic

- [ ] Um pedido de alto nível produz um **PlanDocument** validado por função pura (schema fechado, aciclicidade, tools resolvidas contra snapshot pinado, tectos, risco **derivado**, orçamento) — fail-closed, sem plano fantasma (AOS-230/231/232).
- [ ] O planeador corre como **agente governado** (NHI `agent:planner`, reserva de planeamento admitida antes da decomposição, RM, spans OTel ligados) (AOS-234).
- [ ] O plano aprovado **materializa-se** no DAG (AOS-025) e no spawn delegado (AOS-026), com `tools[]` a vincular a `Authority[]` da NHI filha; o **Scheduler** despacha a jusante do gate, nunca a montante (AOS-237/238).
- [ ] O **gate** renderiza o organigrama completo **triado por risco**, com cards por-efeito e edição→revalidação→aprovação sem round-trip ao LLM (AOS-236).
- [ ] **Replay byte-a-byte** sem re-chamar o LLM: eventos `aos.planner.v1` append-only + janela de suporte de `plan_version` (AOS-235/243).
- [ ] O **classificador de intake** é determinístico e respeita a invariante de não-bypass (AOS-233).
- [ ] `capability_gap` bloqueia até ratificação via pipeline ADR-012, com agente-autor governado (AOS-240) — *sujeito à lacuna do executor de skills*.
- [ ] O prompt de decomposição é artefacto comportamental SemVer com **eval-gate de golden-sets** (AOS-241); a promoção L0–L5 usa fiabilidade medida (AOS-242).
- [ ] Suite de segurança adversarial verde (plano hostil, downgrade de risco, exaustão, gaming do intake, injecção via retry) (AOS-244).
- [ ] Gate SAST/SCA (gosec/govulncheck) limpo ou triado para a baseline documentada.

## 4. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-230 | PlanDocument: schema fechado + `plan_version` SemVer | feature | M | P0 | AOS-025 |
| AOS-231 | Validador puro sobre snapshot de capabilities pinado | feature | L | P0 | AOS-230, AOS-025, AOS-005 |
| AOS-232 | Risco derivado + orçamento re-preçado com teto por nó | feature | M | P0 | AOS-231, AOS-062, AOS-074 |
| AOS-233 | Classificador de intake determinístico + invariante de não-bypass | feature | M | P0 | AOS-230 |
| AOS-234 | Planeador como agente governado (NHI, reserva, OTel) | feature | L | P0 | AOS-026, AOS-027, AOS-077 |
| AOS-235 | Domínio de eventos `aos.planner.v1` append-only + replay | feature | M | P0 | AOS-013, AOS-016 |
| AOS-236 | Gate de aprovação-de-plano: organigrama triado por risco | feature | L | P0 | AOS-121, AOS-120, AOS-232 |
| AOS-237 | Materialização: plano aprovado → DAG + spawn delegado | feature | L | P0 | AOS-236, AOS-025, AOS-026 |
| AOS-238 | Integração do Scheduler: despacho a jusante do gate | feature | M | P1 | AOS-237, AOS-028, AOS-029 |
| AOS-239 | Re-planeamento de subgrafo com orçamento residual | feature | M | P1 | AOS-237, AOS-236 |
| AOS-240 | `capability_gap`: agente-autor governado + pipeline ADR-012 | feature | L | P2 | AOS-236, AOS-096, AOS-114 |
| AOS-241 | Prompt de decomposição SemVer + golden-sets + eval-gate | feature | L | P1 | AOS-234, AOS-114, AOS-115 |
| AOS-242 | Autonomia L0–L5 do planeador + SLIs de planeamento | feature | M | P1 | AOS-236, AOS-014, AOS-124 |
| AOS-243 | Determinismo & migração de `plan_version` | feature | M | P1 | AOS-235, AOS-016 |
| AOS-244 | Suite de segurança adversarial do plano | test | L | P0 | AOS-231, AOS-232, AOS-236, AOS-238 |

---

## AOS-230 — PlanDocument: schema fechado + `plan_version` SemVer

### Contexto
O contrato entre o LLM e o sistema é o PlanDocument (`tecnica/18` §3.3): artefacto declarativo com schema fechado, à imagem dos contratos de porta.

### Objectivo
Definir o schema do PlanDocument com `plan_version` SemVer e desserialização fail-closed.

### Critérios de Aceitação
- [x] Campos por nó: `node_id`, `role`, `objective`, `tools[]` (nome+versão+digest), `depends_on`, `budget_estimate`, `risk_class` (advisory). *(Evidência: `packages/control-plane/orchestrator/plan/plandocument.go` — struct `Node`.)*
- [x] Campos de topo: `objective`, `budget_total`, `planner_meta` (modelo, `prompt_version`, `capabilities_hash`). *(Evidência: struct `PlanDocument`/`PlannerMeta` no mesmo ficheiro.)*
- [x] Desserialização com `DisallowUnknownFields` — campos desconhecidos rejeitados. *(Evidência: `Decode` usa `dec.DisallowUnknownFields()` + `dec.More()`→`ErrTrailingData`; `TestDecode_RejectsUnknownField` é não-vacuoso — prova primeiro que o payload-base é aceite por `json.Unmarshal` permissivo, logo a diferença é só o campo desconhecido; `-race` verde.)*

### Estado
**FECHADO** (vaga 1 EPIC-19). Pacote `packages/control-plane/orchestrator/plan/` (`plandocument.go`, `semver.go`). Testes `-race` verdes; round-trip por `reflect.DeepEqual`; `ParsePlanVersion` estrito (rejeita whitespace); zero-dep; gates deferrals/event-catalog verdes. Rejeição de MAJOR incompatível é fronteira deliberada de AOS-231 (semântica, não forma).

### Detalhes Técnicos
- `plan_version` SemVer: MAJOR=quebra, MINOR=aditivo, PATCH=clarificação.
- `risk_class` documentado como **advisory** que só pode elevar o piso derivado (ver AOS-232).

### Testes Requeridos
- Round-trip de serialização; rejeição de campo desconhecido; tipos/cardinalidades inválidos recusados.

### Definition of Done
- Schema versionado, testado, sem I/O; `risk_class` marcado advisory no próprio contrato.

### Handoff para Claude Code
- Novo pacote de contrato do plano; espelhar a disciplina de config-loading do nó (`DisallowUnknownFields`).

## AOS-231 — Validador puro sobre snapshot de capabilities pinado

### Contexto
`tecnica/18` §3.3 (BLK-2 resolvido na ratificação): a validação é **função pura sobre o documento e um snapshot de capabilities pinado no `propose`** — sem I/O vivo.

### Objectivo
Implementar as regras 1–4 de validação como função pura e determinística.

### Critérios de Aceitação
- [x] Regra 1 (schema), Regra 2 (aciclicidade — mesma verificação do DAG AOS-025), Regra 3 (resolução de `tools[]` contra **snapshot** pinado: versão, digest, admissibilidade), Regra 4 (tectos `max_depth`/`max_fanout`/`max_nodes` **próprios do plano**). *(Evidência: `planvalidate/validate.go` — `checkSemantics`/`checkAcyclic` (reusa `orchestrator.NewDAG` do `contract`, AOS-025)/`checkTools`/`checkCeilings`; `maxDepth` por ordenação topológica de Kahn (pilha O(1), testado com cadeia de 60000 nós); tool inexistente/deprecada rejeita **sem trimming** (`TestToolDesconhecidaRejeitadaSemTrimming` prova não-mutação).)*
- [x] Proposta inválida volta ao LLM com diagnóstico (máx. N=3), depois esgota em falha de intake — fail-closed. *(Evidência: `retry.go` `Ledger`/`MaxAttempts=3`/`ErrIntakeExhausted`; diagnóstico estruturado/allowlisted em `verdict.go` — `node_id` validado por grammar fechada a montante para não ecoar conteúdo cru; `TestEsgotamentoIntake`, `TestFeedbackSemConteudoCru`. `-race` verde.)*

### Detalhes Técnicos
- O snapshot é capturado no `propose` e o seu hash é `capabilities_hash` em `planner_meta` (liga ao replay AOS-243).
- Tectos de **cardinalidade** são próprios do plano — distintos do tecto de **concorrência** de AOS-028 (que é run-time, ver AOS-238).

### Testes Requeridos
- Determinismo (mesmo input → mesmo veredicto); ciclo rejeitado; tool inexistente/deprecada rejeitada (nunca *trimming*); tectos excedidos rejeitados; esgotamento de retries = falha de intake.

### Definition of Done
- Validador sem I/O nem LLM; feedback de retry estruturado/allowlisted (sem eco de conteúdo cru).

### Handoff para Claude Code
- Reutilizar a verificação de aciclicidade do AOS-025; snapshot de REG como argumento, nunca lookup vivo.

### Estado
**FECHADO** (vaga 2 EPIC-19). Pacote `packages/control-plane/orchestrator/planvalidate/`. Validador puro sem I/O/LLM; `maxDepth` iterativo (Kahn); zero-dep; `-race` verde. Derivação de risco (regras 5–6) fica para AOS-232; hash do snapshot para AOS-243.

## AOS-232 — Risco derivado + orçamento re-preçado com teto por nó

### Contexto
`tecnica/18` §3.3 regras 5–6 (BLK-1 resolvido): o risco é **derivado das ferramentas pinadas**, não lido do rótulo do LLM; o custo por ramo é **re-preçado**, não ecoado.

### Objectivo
Implementar as regras 5 (orçamento) e 6 (risco derivado) da validação.

### Critérios de Aceitação
- [x] `risk_class` **derivado** por nó das tools resolvidas: efeito irreversível ou egress externo sensível ⇒ `danger` (nunca auto-aprovável); o campo do LLM só é aceite se ≥ ao piso derivado. *(Evidência: `planvalidate/risk.go` — `resolveNodeRisk` deriva o piso via `risk.Classify` (kernel/reference-monitor/risk) das tools pinadas; `elevateOnly(floor, declared)` só devolve o rótulo do LLM se `riskRank(declared) > riskRank(floor)` ⇒ **downgrade ignorado, `Resolved ≥ Derived` sempre**; `AutoApprovable()==false` para `danger`. `TestDowngradeDeRiskClassEIgnorado`, `TestNoIrreversivelClassificadoDanger`, `TestEgressExternoSensivelDanger`.)*
- [x] Custo por ramo **re-preçado** determinísticamente (tabela AOS-062); divergência acima de tolerância rejeita/clamp. *(Evidência: `planvalidate/budget.go` — `Pricer` injetado puro re-preça (não ecoa o documento); divergência > tolerância ⇒ `ReasonBranchCostDivergence` (rejeita). Fail-closed contra custo declarado adversarial ≥ 2^63 via `clampU64ToInt64` (satura em `MaxInt64`, sem wrap negativo) — `TestDeclaredCostOverflowFailClosed` (falha-antes reproduzido: `int64(u)` directo aceitava o nó). Handoff: a tabela concreta AOS-062 liga-se no composition root de produção via o `Pricer` real.)*
- [x] `budget_total` ≤ orçamento raiz remanescente; **teto de custo duro por nó** dispara o breaker (AOS-029). *(Evidência: `fitsWithin`/`exceedsCeiling`/`checkedAdd`; o teto por-nó é realizado como **gate de admissão** — rejeição determinística ANTES de `budget.Reserve` — não invocação do componente runtime AOS-029 (documentado no doc-comment). `TestTotalExcedeRaizRejeita`, `TestOverrunPorRamoBloqueia`.)*

### Estado
**FECHADO** (vaga 3 EPIC-19). Regras 5–6 no pacote `packages/control-plane/orchestrator/planvalidate/` (`risk.go`, `budget.go`, `resources.go`). Risco resolvido (não declarado) sem caminho em que o LLM baixe o piso; custo re-preçado com guarda de divergência fail-closed inclusive contra overflow. `go.mod` intacto (só `import "math"`); custo pela abstração `Pricer` (tabela AOS-062 ligada em produção); breaker AOS-029 como gate de admissão. `-race` verde (15 testes). Approval-card/timeout do nó `danger` fica com AOS-236/AOS-120.

### Detalhes Técnicos
- Derivação via os classificadores de risco/reversibilidade já existentes (AOS-074, `risk.Classify`).
- Nó `danger` ⇒ approval-card por efeito concreto (AOS-120), confirmação individual, timeout fail-closed.

### Testes Requeridos
- Downgrade de `risk_class` no documento é ignorado (piso derivado vence); nó irreversível classificado `danger`; overrun por-ramo bloqueia, não *overrun* silencioso.

### Definition of Done
- Risco resolvido, não declarado; sem caminho em que o rótulo do LLM baixe o risco.

### Handoff para Claude Code
- Partilha superfície com AOS-236 (o gate consome o risco resolvido).

## AOS-233 — Classificador de intake determinístico + invariante de não-bypass

### Contexto
`tecnica/18` §3.5: a classificação é *routing*, não autoridade.

### Objectivo
Classificar um `Goal` como meta-nível vs. tarefa simples, deterministicamente, e garantir a invariante de não-bypass.

### Critérios de Aceitação
- [x] Função pura sobre campos **declarativos** do `Goal` (nunca o texto do `objective`): `intake_mode`, orçamento vs. limiar de tenant, tectos pedidos, cardinalidade de papéis. *(Evidência: `intake/classify.go` — o tipo `Signals` **não tem campo `Objective`**; imunidade a injeção é estrutural, não convenção. `TestClassify_InjectionNoObjectiveInput`, `TestClassify_Deterministic` (1000×).)*
- [x] Ambiguidade ⇒ **meta** (fail-safe para supervisão). *(Evidência: ramo fail-safe + normalização de modo inválido para `Unset`; `TestClassify_AmbiguityToMeta`, `TestClassify_AntiGaming`.)*
- [x] **Invariante de não-bypass:** um run "simples" que tente delegar reentra no gate por-spawn (ADR-013) ao nível L0–L5 do chamador. *(Evidência: `intake/nonbypass.go` `DelegationGuard.Delegate` consulta o gate antes de spawnar; `CallerLevel` validado fail-closed em L0–L5 (`ErrInvalidCallerLevel`); `TestNonBypass_SimpleRunDelegationIsGated`, `TestNonBypass_InvalidCallerLevelFailClosed`.)*
- [x] Evento `plan.intake_classified` com a heurística aplicada. *(Evidência: `intake/emit.go` reutiliza a constante `EventIntakeClassified` e `IntakeClassifiedPayload` de `plannerevents` (sem tipo novo); `TestClassifyAndRecord_EmitsHeuristicNoObjective` prova igualdade exacta do payload sem eco do objective.)*

### Detalhes Técnicos
- O texto do `objective` **não** é input de classificação — imune a injecção no objectivo.

### Testes Requeridos
- Injecção no `objective` não troca a rota; run "simples" a delegar dispara plano just-in-time gated; ambiguidade→meta.

### Definition of Done
- Classificador determinístico e replayable; invariante coberta por teste.

### Handoff para Claude Code
- O ponto de reentrada é o mesmo delegador AOS-026 + gate AOS-121/236.

### Estado
**FECHADO** (vaga 2 EPIC-19). Pacote `packages/control-plane/orchestrator/intake/`. Classificação pura determinística (tipo `Signals` sem `Objective` ⇒ imune a injeção por construção); invariante de não-bypass com `CallerLevel` L0–L5 fail-closed; reutiliza o evento de `plannerevents`; zero-dep; `-race` verde.

## AOS-234 — Planeador como agente governado (NHI, reserva, OTel)

### Contexto
`tecnica/18` §3.2: o planeador é um agente que corre no kernel, invocado pelo ORQ (autoridade = ORQ, ADR-018).

### Objectivo
Correr a decomposição como agente com NHI própria, orçamento e observabilidade.

### Critérios de Aceitação
- [x] NHI `agent:planner` na cadeia de delegação do run; chamadas mediadas pelo RM (ADR-002). *(Evidência: `planner/planner.go` — `IssueChild` on-behalf-of o token do run (cadeia hash-linked raiz humana→run→`agent:planner`, exposta em `PlanResult.PlannerToken`); mediação obrigatória `Mediate` **antes** da reserva, `ErrMediationDenied` fail-closed; `TestDecompose_MediationError_FailClosed`.)*
- [x] **Reserva de planeamento** admitida **antes** da decomposição (contexto × AOS-062 × factor de retry), fail-closed; evento `plan.planner_admitted`. *(Evidência: `Decompose` reserva antes de decompor (`ErrNoPlanningBudget` se sem headroom ⇒ o `Decomposer` NÃO é chamado); a reserva é **libertada em todos os caminhos de falha** (identidade, emissor, decomposição, gate) e só consolidada (`Commit`) no sucesso; emite `plan.planner_admitted` (constante de `plannerevents`). `TestDecompose_*_ReleasesReserve` (5 caminhos).)*
- [x] Toda a fase de planeamento emite spans OTel filhos do `traceparent` do run (AOS-077): N tentativas, gate. *(Evidência: spans por tentativa + span de gate parented ao âncora do run, mesmo trace; `TestDecompose_Admitted_SpansChildrenOfRun_AndEvent`.)* **O span de _materialização_ é deliberadamente do AOS-237** (spec §186 «separar PLN-decompositor do materializador»); completa-se quando AOS-237 fechar — não é over-claim (o doc-comment declara-o).

### Detalhes Técnicos
- Separar PLN-decompositor (produz PlanDocument) do materializador (ORQ, AOS-237).

### Testes Requeridos
- Sem reserva admitida ⇒ decomposição não arranca; spans presentes para as N tentativas.

### Definition of Done
- Planeamento custa tokens contabilizados na árvore; ponto não-cego na trajectória.

### Handoff para Claude Code
- Respeitar a fronteira ADR-018 (guard-test `boundary_orq_sch_test.go`).

### Estado
**FECHADO** (vaga 2 EPIC-19; sub-item _materialização_ de CA-3 remetido a AOS-237 por desenho). Pacote `packages/control-plane/orchestrator/planner/`. Planeador governado: NHI `agent:planner` hash-linked, mediação RM antes da reserva, reserva fail-closed libertada em todos os caminhos de falha e consolidada só no sucesso, spans OTel filhos do run; `Decomposer` é interface injetada (sem LLM real); zero-dep; `-race` verde (cobertura 92%).

## AOS-235 — Domínio de eventos `aos.planner.v1` append-only + replay

### Contexto
`tecnica/18` §6.1: sequência de eventos append-only que reconstrói o ciclo por replay.

### Objectivo
Emitir e persistir os eventos do domínio `aos.planner.v1` e reconstruí-los por replay.

### Critérios de Aceitação
- [x] Eventos: `plan.intake_classified`, `plan.planner_admitted`, `plan.proposed`, `plan.validation_failed`, `plan.validated`, `plan.approved`/`rejected`/`edited`, `plan.materialized`, `plan.capability_gap_opened`/`resolved`, `plan.replan_requested`/`applied`. *(Evidência: 13 constantes em `plannerevents/events.go`; família `plan.*` registada na taxonomia `tecnica/13` §3.3 (a); gate `event-catalog` verde — 98 tipos.)*
- [x] Ordem idêntica na reconstrução (ADR-010); sem eco de conteúdo sensível em `validation_failed`. *(Evidência: `TestReplayReconstructsSequenceByteForByte` (ordem canónica não-alfabética preservada); `TestValidationFailedDoesNotEchoSensitiveContent` injeta SSN+api-key no `RawDetail`, relê o payload REAL do store e exige ausência do segredo e dos fragmentos, com guarda de não-vacuidade nos metadados classificados; `-race` verde.)*

### Estado
**FECHADO** (vaga 1 EPIC-19). Pacote `packages/control-plane/orchestrator/plannerevents/` (`events.go`, `recorder.go`, `replay.go`). Domínio `aos.planner.v1` versionado; replay read-only (contador `Proposer==1` inalterado, nenhum re-chamada ao LLM); fail-closed em tipo/versão desconhecidos; zero-dep; `-race` verde. Taxonomia `tecnica/13` §3.3 reconciliada (família `plan.*`, contagens 91/98).

### Detalhes Técnicos
- Assenta no event store/replay existentes (AOS-013/016).

### Testes Requeridos
- Replay reconstrói a sequência byte-a-byte; nenhum evento re-chama o LLM.

### Definition of Done
- Domínio de eventos versionado, testado, sem PII.

### Handoff para Claude Code
- O documento aprovado é o input do restante run (ligação a AOS-243).

## AOS-236 — Gate de aprovação-de-plano: organigrama triado por risco

### Contexto
`tecnica/18` §4.3: com o planeador real, o gate deixa de ser UX e torna-se **a** fronteira de segurança (até L3).

### Objectivo
Renderizar o organigrama completo, triado por risco, com edição cidadã de primeira classe.

### Critérios de Aceitação
- PlanCard (AOS-121) apresenta papéis, tools por papel, custo por ramo, classes de risco **resolvidas** — triado por risco: revisão item-a-item forçada dos nós ≥ gray e `capability_gap`, resto colapsável.
- Cards por-efeito para nós `danger` (AOS-120); edição → revalidação → aprovação **sem** round-trip ao LLM.
- Decisão assinada (hitl.Channel) + diff estrutural da edição.

### Detalhes Técnicos
- `PlanNode` ganha campo de custo; *threading* por card (`WithEstimatedCost`).

### Testes Requeridos
- Edição re-valida (AOS-231) antes de aprovar; nó `danger` força card individual; override-rate registado (AOS-095).

### Definition of Done
- Nenhum spawn sem passagem pelo gate até L3; custo por ramo visível no card.

### Handoff para Claude Code
- Estender o builder do PlanCard (AOS-121), não criar card novo.

## AOS-237 — Materialização: plano aprovado → DAG + spawn delegado

### Contexto
`tecnica/18` §6.1/§4.1: a materialização lê o documento **aprovado**, não a saída crua do modelo.

### Objectivo
Converter o plano aprovado em eventos do DAG e spawns delegados.

### Critérios de Aceitação
- `plan.materialized`: `node_id` → nó-folha `task.node.created` (AOS-025) **ou** papel-que-expande → `Delegator.Spawn` (AOS-026).
- `tools[]` do plano vincula `Authority[]` da NHI filha (issuer_child).
- Admissão global por nó (AOS-027/028).

### Detalhes Técnicos
- Reconciliar granularidade tools-pinadas-no-REG vs capabilities coarse na Authority.

### Testes Requeridos
- Papel expande em sub-árvore; folha vira nó único; Authority da filha limitada às tools do papel.

### Definition of Done
- Materialização determinística a partir do documento gravado.

### Handoff para Claude Code
- Consome `plan.approved`; emite `plan.materialized`.

## AOS-238 — Integração do Scheduler: despacho a jusante do gate

### Contexto
`tecnica/18` §4.4: o SCH despacha o que o ORQ materializou; nunca planeia.

### Objectivo
Despachar nós prontos sob admissão, a jusante do gate.

### Critérios de Aceitação
- Só despacha nós com `depends_on` satisfeitas, após `plan.materialized`.
- Tecto de **concorrência** `max_spawn = f(headroom)` (AOS-028) — run-time, distinto dos tectos de tamanho (AOS-231).
- Espera no gate **não consome headroom**; nós `waiting_on_capability`/`danger` sem card resolvido não despacham.
- Re-verificação TOCTOU no spawn: sob pressão, **adia** (spawn diferido), nunca oversubscreve nem spawn parcial silencioso.

### Detalhes Técnicos
- Degradação graciosa reutiliza AOS-028/031.

### Testes Requeridos
- Plano aprovado que já não cabe fica em espera de headroom; nenhum despacho antes do gate.

### Definition of Done
- SCH a jusante do gate; fronteira ADR-018 preservada.

### Handoff para Claude Code
- Não importar o módulo de ciclo-de-vida (guard-test); só despachar.

## AOS-239 — Re-planeamento de subgrafo com orçamento residual

### Contexto
`tecnica/18` §4.2: a falha de um nó não derruba o organigrama.

### Objectivo
Re-planear um subgrafo afectado com orçamento residual e novo ciclo de aprovação.

### Critérios de Aceitação
- Replan debita o orçamento da árvore; atravessa o **mesmo** gate conforme o nível L0–L5 do plano original (autonomia do replan ≤ original).
- Nós concluídos são **intocáveis** (imutabilidade do histórico — só opera sobre o futuro).
- Tecto de replans por árvore (replans **aninhados** contam para o mesmo tecto); revisão humana forçada quando o custo acumulado excede fracção do orçamento.

### Detalhes Técnicos
- `plan.replan_requested`/`applied`; o SCH suspende o subgrafo e retoma no `applied` (AOS-238).

### Testes Requeridos
- Replan não re-despacha histórico; replan aninhado incrementa o mesmo contador; esgotamento força revisão.

### Definition of Done
- Sem loop de replan permanente; orçamento residual respeitado.

### Handoff para Claude Code
- Reutilizar o gate de AOS-236 para o sub-plano.

## AOS-240 — `capability_gap`: agente-autor governado + pipeline ADR-012

### Contexto
`tecnica/18` §5: o plano que precisa de uma capability inexistente estende o sistema **com rede**. *Bloqueado em parte pela lacuna do executor de skills.*

### Objectivo
Modelar o nó `capability_gap` e encaminhar a skill candidata pelo pipeline ADR-012.

### Critérios de Aceitação
- Nó `capability_gap` bloqueia (`waiting_on_capability`) até ratificação.
- Skill gerada por **agente-autor governado** (NHI, orçamento, allowlist restrita) que trata a spec do gap como **input untrusted** com taint no eval-gate.
- Pipeline: dry-run (AOS-126) → eval-gate (AOS-114/115/189) → canary → ratificação assinada (AOS-096/206); humano pode substituir/rejeitar o nó.

### Detalhes Técnicos
- Sem executor de skills (lacuna honesta §5), os nós executam sobre tools concretas já registadas — este ticket entrega a governação do gap, não o executor.

### Testes Requeridos
- Nó não despacha até `capability_gap_resolved`; artefacto auto-escrito não chega a produção unilateralmente.

### Definition of Done
- Tecto de gaps por plano; nenhum bypass do pipeline.

### Handoff para Claude Code
- Marcar dependência do executor de skills (desenho separado) no ticket.

## AOS-241 — Prompt de decomposição SemVer + golden-sets + eval-gate

### Contexto
`tecnica/18` §6.2/§6.3: o prompt de decomposição é artefacto comportamental; avaliar um gerador não-determinístico exige asserções, não igualdade de plano.

### Objectivo
Versionar o prompt e montar o eval-gate de golden-sets.

### Critérios de Aceitação
- Prompt estático, cache-estável (ADR-009), SemVer; mudanças passam pelo pipeline ADR-012.
- Golden-set: entradas `(objectivo, contexto) → asserções` (estruturais + semânticas), verificáveis pelo validador (AOS-231) + rubrica.
- Amostragem **K×** por objectivo: asserções de **segurança** a 100% de K; de **qualidade** por limiar ≥ M/K.
- Trace-diffing = **regressão distribucional** sobre métricas (não plano cru); sem regressão de segurança.
- Mutar o golden-set é *gated* (anti-envenenamento).

### Detalhes Técnicos
- Corre offline no eval-gate (staging), nunca por-run nem em produção.

### Testes Requeridos
- Prompt regressivo em segurança bloqueia o gate; remoção de caso difícil do golden-set exige aprovação.

### Definition of Done
- Golden-set versionado com dono; sinal de pass-rate disponível para promoção (AOS-242).

### Handoff para Claude Code
- Fontes do golden-set: prod anonimizada + adversarial (red-team) + regressão.

## AOS-242 — Autonomia L0–L5 do planeador + SLIs de planeamento

### Contexto
`tecnica/18` §7.2/§7.3: o planeador nasce a L0; a promoção é por fiabilidade medida.

### Objectivo
Controlador de promoção/demoção por (planner, domínio) e SLIs de planeamento.

### Critérios de Aceitação
- Sinais: taxa de aprovação **sem edição**, taxa de replan, calibração de custo (AOS-124), taxa de propostas inválidas; demoção automática em anomalia.
- Promoção por domínio **recorrente** (janela sustentada, AOS-014); *ad-hoc* permanece L0 por desenho; granularidade de "domínio" declarada.
- L4/L5 auto-aprova dentro de envelope declarado (avaliado sobre risco **derivado**); `capability_gap`/`danger` forçam sempre revisão.
- Travão de runtime independente do humano: eval-gate de decomposição (AOS-241) como pré-condição + amostragem post-hoc mesmo a L4/L5.
- SLI: fracção de planeamento ≤ 5% (burn-down AOS-123; contabilidade AOS-062).

### Detalhes Técnicos
- Override-rate autoritativo de AOS-095; AOS-128 é a suite que o testa, não o controlo.

### Testes Requeridos
- Domínio ad-hoc não promove; envelope L4/L5 usa risco derivado; SLI de fracção visível.

### Definition of Done
- Promoção assente em sinal não-gameável; sem rubber-stamp por conveniência.

### Handoff para Claude Code
- Reutilizar o controlador L0–L5 existente (AOS-014).

## AOS-243 — Determinismo & migração de `plan_version`

### Contexto
`tecnica/18` §3.4/§3.6: replay sem re-chamar o LLM; schema evolui sob SemVer.

### Objectivo
Persistir o plano aprovado e gerir a evolução/deprecação de `plan_version`.

### Critérios de Aceitação
- O plano aprovado (+ `capabilities_hash` + `prompt_version`) é persistido; o manifesto do run inclui-o.
- Replay **reproduz os eventos capturados** — nunca re-resolve o REG nem re-atravessa o RM.
- Planos aprovados **congelados na versão**; nunca auto-migrados. Se a versão foi retirada antes da materialização ⇒ invalida → re-plano + re-aprovação (fail-closed).
- **Janela de suporte** de MAJORs declarada; run fora da janela é **inadmissível** (como payload perdido).
- Bump MAJOR passa por ADR-012, com reader retido **ou** deprecação documentada (implicações AOS-079/093).

### Detalhes Técnicos
- `plan_version` (schema) ≠ `prompt_version` (comportamento) ≠ `capabilities_hash` (ambiente) — os três pinados.

### Testes Requeridos
- Replay não re-chama LLM nem RM; plano de versão retirada invalida fail-closed; run fora da janela = inadmissível.

### Definition of Done
- "Replayable enquanto captura **e** reader forem admissíveis".

### Handoff para Claude Code
- Alinhar com a inadmissibilidade já modelada no motor de replay (AOS-016).

## AOS-244 — Suite de segurança adversarial do plano

### Contexto
`tecnica/18` §7.1/§9: a superfície nova é o **plano enquanto vector**.

### Objectivo
Provar em teste que os vectores adversariais estão fechados.

### Critérios de Aceitação
- **Plano adversarial**: objectivo/untrusted não induz spawn com efeitos indevidos (plano como dados + validação + gate + spawn mediado).
- **Downgrade de risco**: rótulo `safe` num nó irreversível é ignorado (piso derivado, AOS-232).
- **Exaustão de fan-out**: plano gigante barrado por tectos (AOS-231) + teto por nó + breaker (AOS-029).
- **Gaming do intake**: classificação forçada a "simples" reentra no gate (AOS-233).
- **Injecção via retry**: feedback estruturado/allowlisted, sem re-injecção in-band.

### Detalhes Técnicos
- Cada teste mapeia a uma linha da tabela de riscos `tecnica/18` §9.

### Testes Requeridos
- Um teste negativo por vector; falso-negativo falha o gate.

### Definition of Done
- Suite verde; gate SAST/SCA triado.

### Handoff para Claude Code
- Guard-tests no estilo das 5 negações do composition-root.

---

## 5. Vista de qualidade

- **Segurança:** o plano é dados (ADR-005); validação pura fecha schema/aciclicidade/tools/tectos e **deriva** o risco; gate humano com risco resolvido; spawn mediado nó a nó. Planeador taintado como qualquer consumidor de untrusted.
- **Autonomia:** nasce a L0; promoção por fiabilidade medida, nunca por conveniência (AOS-242).
- **Custo:** planeamento debita a árvore antes de qualquer spawn; alvo ≤ 5% medido por SLI (AOS-242).
- **Determinismo:** replay orientado a eventos, sem LLM (AOS-235/243).

## 6. Riscos e mitigações

| Risco | Impacto | Mitigação |
|---|---|---|
| Executor de skills inexistente limita `capability_gap` | Repertório reduzido no v1 | AOS-240 entrega a governação do gap; executor é desenho separado (lacuna honesta §5) |
| Model Gateway não cablado no bootstrap | Fluxo central incompleto | Dependência de integração (EPIC-06); tickets validáveis com *doubles* offline |
| Golden-sets caros de manter | Eval-gate degrada | Propriedade de primeira classe; regressões viram entradas permanentes (AOS-241) |
| Promoção L0–L5 gameável | Auto-aprovação indevida | Sinal do eval-gate + amostragem post-hoc + risco derivado (AOS-242) |

## 7. Glossário

- **PlanDocument:** contrato declarativo schema-fechado com `plan_version` SemVer.
- **Meta-run:** run cujo plano expande num organigrama de sub-agentes.
- **Capability gap:** nó que exige skill inexistente; bloqueia até ratificação ADR-012.
- **Invariante de não-bypass:** qualquer delegação reentra no gate por-spawn, independentemente da classificação de intake.

## 8. Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

## 9. Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | 2026-08-02 | Emissão inicial: decomposição do `tecnica/18` v1.0 (Ratificado) em 15 tickets AOS-230..244. | Equipa AOS |
