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

- [x] Um pedido de alto nível produz um **PlanDocument** validado por função pura (schema fechado, aciclicidade, tools resolvidas contra snapshot pinado, tectos, risco **derivado**, orçamento) — fail-closed, sem plano fantasma (AOS-230/231/232).
- [x] O planeador corre como **agente governado** (NHI `agent:planner`, reserva de planeamento admitida antes da decomposição, RM, spans OTel ligados) (AOS-234).
- [x] O plano aprovado **materializa-se** no DAG (AOS-025) e no spawn delegado (AOS-026), com `tools[]` a vincular a `Authority[]` da NHI filha; o **Scheduler** despacha a jusante do gate, nunca a montante (AOS-237/238).
- [x] O **gate** renderiza o organigrama completo **triado por risco**, com cards por-efeito e edição→revalidação→aprovação sem round-trip ao LLM (AOS-236).
- [x] **Replay byte-a-byte** sem re-chamar o LLM: eventos `aos.planner.v1` append-only + janela de suporte de `plan_version` (AOS-235/243).
- [x] O **classificador de intake** é determinístico e respeita a invariante de não-bypass (AOS-233).
- [x] `capability_gap` bloqueia até ratificação via pipeline ADR-012, com agente-autor governado (AOS-240) — *sujeito à lacuna do executor de skills*.
- [x] O prompt de decomposição é artefacto comportamental SemVer com **eval-gate de golden-sets** (AOS-241); a promoção L0–L5 usa fiabilidade medida (AOS-242).
- [x] Suite de segurança adversarial verde (plano hostil, downgrade de risco, exaustão, gaming do intake, injecção via retry) (AOS-244).
- [x] Gate SAST/SCA (gosec/govulncheck) limpo ou triado para a baseline documentada. **SCA verde** (`scripts/ci/sca.sh` exit 0): os dois binários de entrega — `packages/cmd/aos` e `packages/cmd/aos-issuer` — subiram para `go 1.25`+`toolchain go1.25.12` (fecha o maior "Fixed in" observado, GO-2026-5856), pelo que govulncheck a esses módulos passou a ZERO findings afetantes; as 19 entradas obsoletas de `packages/cmd/aos` saíram de `baseline/govulncheck.txt`. Debt das bibliotecas (analisadas standalone a 1.24.5, não shipam isoladas) permanece triado, com a subida transversal reservada ao EPIC-10. **SAST verde** (`scripts/ci/sast.sh` exit 0): as 9 descobertas HIGH residuais (G115 de conversão comprimento/timestamp; G407 de proveniência de nonce em GCM) estão triadas em `baseline/gosec.txt` como falso-positivo/seguro-por-construção.

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
| AOS-388 | Decomposer LLM de produção: goal → PlanDocument multi-nó, ponta-a-ponta no aos-orq | feature | L | P1 | AOS-234, AOS-241, AOS-237, AOS-026, AOS-281 |
| AOS-389 | Guard fail-closed: recusar planos com arestas condicionais até o avaliador estar composto | feature | S | P0 | AOS-237, AOS-388 |
| AOS-390 | Compor o despacho governado do Planeador (`plandispatch.Dispatcher` sob Tenure) | feature | L | P0 | AOS-281, AOS-237, AOS-238, AOS-389 |
| AOS-391 | T2-B: compor o `decompose.Model` real via Model Gateway | feature | L | P1 | AOS-388, AOS-390, AOS-278 |
| AOS-393 | Fix fail-closed: o ramo papéis-que-expandem via `Delegator.Spawn` é recusado no `--goal` (depth_mismatch; `agent.spawn` latente) | feature (correcção) | S | P1 | AOS-388, AOS-026, AOS-237 |
| AOS-400 | O prompt de decomposição declara o schema do `PlanDocument` que o decode exige | fix | M | P1 | AOS-241, AOS-273, AOS-391 |
| AOS-408 | O gate de aprovação de plano fica composto no `aos-orq`: um plano de risco espera decisão humana antes de materializar | feature | L | P1 | AOS-236, AOS-237, AOS-390, AOS-388 |
| AOS-409 | O `IsEffectTool` ganha o 4.º eixo — mutação — a partir de uma fonte de verdade que não seja o próprio plano | feature | M | P2 | AOS-231, AOS-408 |

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
 Realiza **ADR-020** (planeador como agente governado), que nomeia este ticket em `docs/adr/ADR-020-planeador-agente-governado.md` §5 e §6.
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
 Realiza **ADR-020** (planeador como agente governado), que nomeia este ticket em `docs/adr/ADR-020-planeador-agente-governado.md` §6.
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
 Realiza **ADR-020** (planeador como agente governado), que nomeia este ticket em `docs/adr/ADR-020-planeador-agente-governado.md` §6.
### Objectivo
Renderizar o organigrama completo, triado por risco, com edição cidadã de primeira classe.

### Critérios de Aceitação
- [x] PlanCard (AOS-121) apresenta papéis, tools por papel, custo por ramo, classes de risco **resolvidas** — triado por risco: revisão item-a-item forçada dos nós ≥ gray e `capability_gap`, resto colapsável. *(Evidência: `roles.go` `RolesView()`/`RoleCapabilities` (papel→tools dedup+ordenado, worst-class, task_ids topo); `PlanNode.Cost` (custo por-ramo) em `ports.go`; `triage.go` modela `NodeReviews` Forced(≥gray/capability_gap)/Collapsible; `enforceForcedReview` recusa fail-closed aprovar um nó forçado por rever. `TestRolesView_GroupsToolsByRole`, `TestForcedReview_ApproveWithoutReviewingForcedNodeRejected`, `TestTriage_*`.)* ✅ **Postura = opção B do dono** (ver Estado): `WithForcedReview` opt-in na lib mas **ligado no composition root do nó** (aos-demo) ⇒ imposto no binário entregue.
- [x] Cards por-efeito para nós `danger` (AOS-120); edição → revalidação → aprovação **sem** round-trip ao LLM. *(Evidência: `DangerEffectCards`; `revalidateEdit` (revalidação estrutural local + porta `Revalidator` do wiring→planvalidate) ANTES de assinar — `TestEdit_RevalidatesViaPortBeforeApprove`, `TestEdit_LocalStructuralRevalidationRejectsCycle`; dual-control por-efeito inline `WithPerEffectDualControl`/`enforcePerEffectDualControl` (dois aprovadores distintos por efeito danger) — `TestPerEffectDualControl_EnforcedInlineForDangerNode`.)* ✅ Dual-control por-efeito ligado no mesmo wiring seguro do nó (opção B).
- [x] Decisão assinada (hitl.Channel) + diff estrutural da edição. *(Evidência: `planConfirmationRequest`→`channel.Confirm` sela no audit WORM (não-repúdio); `DiffPlans` produz o diff estrutural nós/arestas antes→depois. `TestGatedPlan_*NonRepudiation`.)*

### Estado
**FECHADO** (vaga 4 EPIC-19); pacote `packages/control-plane/governance/plan-approval/` estendido de forma aditiva (23 testes antigos verdes + novos; `go.mod` intacto; zero import do orchestrator — desacoplamento por portas preservado). A fronteira "até L3" é garantida pelo oráculo de autonomia (`mode.Runs()` só auto-aprova a níveis altos; L0–L3 exige gate humano + confirmação assinada). **DECISÃO DO DONO: opção B (seguro via wiring de produção).** O default da biblioteca mantém-se permissivo/flexível (opt-in), mas o composition root do nó liga a postura segura: `packages/cmd/aos-demo/main.go` compõe o gate com `WithReviewer(demoPlanReviewer)` + `WithForcedReview()` + `WithPerEffectDualControl()` ⇒ o nó entregue impõe revisão item-a-item dos nós ≥gray e dois aprovadores distintos por-efeito nos `danger`. Verificado: o teste end-to-end do demo passa `-race` e o demo aprova o plano `danger` sob a postura segura (nó revisto + dois aprovadores). Quando o nó real `cmd/aos` compuser o gate, herda o mesmo wiring seguro.

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
 Realiza **ADR-020** (planeador como agente governado), que nomeia este ticket em `docs/adr/ADR-020-planeador-agente-governado.md` §6.
### Objectivo
Converter o plano aprovado em eventos do DAG e spawns delegados.

### Critérios de Aceitação
- [x] `plan.materialized`: `node_id` → nó-folha `task.node.created` (AOS-025) **ou** papel-que-expande → `Delegator.Spawn` (AOS-026). *(Evidência: `planmaterialize/materialize.go` — switch determinístico por `SpawnKind` (folha→`LeafAdmitter`/`GraphBuilder.AddNode`; papel→`Spawner`/`Delegator.Spawn`); emite `plannerevents.EventMaterialized`. `TestRoleExpandsLeafBecomesNode`, `TestEmitsPlanMaterializedConstant`.)*
- [x] `tools[]` do plano vincula `Authority[]` da NHI filha (issuer_child). *(Evidência: `authorityForNode` deriva as caps **exclusivamente** de `node.Tools` (clamp intrínseco) → `Child.Authority` → `issuer.IssueChild`. `TestChildAuthorityClampedToRoleTools` prova que a tool de OUTRO nó (`cap:tool:toolB`) NÃO entra na Authority do papel — propriedade de segurança central, falha-antes documentado.)*
- [x] Admissão global por nó (AOS-027/028). *(Evidência: porta `Admission` (sem require novo); admissão em **duas fases** fail-closed — admite TODOS antes de qualquer efeito. `TestNodeNotAdmittedFailsClosed`: negação ⇒ `ErrNodeNotAdmitted` + zero spawns/folhas/records. Materialização determinística: `TestDeterministicMaterialization`.)*

### Estado
**FECHADO** (vaga 5 EPIC-19). Pacote `packages/control-plane/orchestrator/planmaterialize/`. Consome o documento APROVADO (não a saída crua); determinístico; Authority clampada às tools do papel; admissão fail-closed; reúsa `EventMaterialized`; zero-dep, `go.mod` intacto, `-race` verde. Fronteiras declaradas+testadas: DAG single-tool leva `tools[0]` (o conjunto coarse sobrevive em `plan.materialized.Nodes[].Tools`; estender o DAG a multi-tool exige emenda ao irmão congelado `contract.TaskSpec`); orçamento papel-sob-papel achatado ao root (aninhamento fica com o dispatch AOS-238).

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
- [x] Só despacha nós com `depends_on` satisfeitas, após `plan.materialized`. *(Evidência: `plandispatch/dispatch.go` — gate (materialização) é a 1ª porta; `unmetDependency` exige `NodeComplete` em todas as arestas, fail-closed em dep desconhecida. `TestDispatch_NoDispatchBeforeGate`, `TestDispatch_UnsatisfiedDependencyBlocks`.)*
- [x] Tecto de **concorrência** `max_spawn = f(headroom)` (AOS-028) — run-time, distinto dos tectos de tamanho (AOS-231). *(Evidência: porta `Headroom` com `Acquire` atómico (vs `Available` advisory, ignorado — `TestDispatch_IgnoresAdvisoryAvailable`); distinção AOS-028/AOS-231 documentada.)*
- [x] Espera no gate **não consome headroom**; nós `waiting_on_capability`/`danger` sem card resolvido não despacham. *(Evidência: retorno antes de `Acquire`; cartão avaliado antes do bloco de `Acquire`. `TestDispatch_NoDispatchBeforeGate` (`acquireCalls==0`), `TestDispatch_WaitingOnCardDoesNotConsumeHeadroom`.)*
- [x] Re-verificação TOCTOU no spawn: sob pressão, **adia** (spawn diferido), nunca oversubscreve nem spawn parcial silencioso. *(Evidência: `Acquire` atómico por nó; falha do sink faz `Release` real + propaga `ErrDispatchSink`, `Result` sempre completo. `TestDispatch_TOCTOU_DoesNotOversubscribe` (advertised=3/live=1 ⇒ despacha 1), `TestDispatch_SinkFailureReleasesSlotAndSurfaces`, `TestDispatch_SinkFailureCompletesResults`.)*

### Estado
**FECHADO** (vaga 6 EPIC-19). Pacote `packages/control-plane/orchestrator/plandispatch/`. SCH a jusante do gate; `LifecycleView` leitura-só (preserva ADR-018). A fronteira ADR-018 é agora um **guard-test executável** (`TestBoundary_ProductionImportsAreAllowlisted`: imports de produção só `{plan, plannerevents}`), não só documental. `validatePlan` deteta ciclo/auto-dep/aresta pendente (DFS 3-cores). Zero-dep (deps de outro módulo — scheduler AOS-028/029 — por porta), `go.mod` intacto, `-race` verde (16 testes).

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
- [x] Replan debita o orçamento da árvore; atravessa o **mesmo** gate conforme o nível L0–L5 do plano original (autonomia do replan ≤ original). *(Evidência: `replan/replan.go` — reserva CAS do residual→gate ao nível original; "autonomia ≤ original" **ancorada à ÁRVORE** (não ao pedido) via `pinAndCheckLevel`, que fixa+valida o nível numa **secção crítica única** — fecha o TOCTOU de duas primeiras invocações concorrentes (verificado por `TestReplan_ConcurrentFirstInvocations_NoAutonomyEscalation`, falha-antes confirmada por reversão). `TestReplan_NestedCannotEscalateAutonomyAbovePinnedLevel`.)*
- [x] Nós concluídos são **intocáveis** (imutabilidade do histórico — só opera sobre o futuro). *(Evidência: `guardImmutable` recusa `NodeCompleted` no subgrafo a substituir E no novo; nó ausente do snapshot ⇒ `ErrNodeStatusUnknown` (fecha o fail-open). `TestReplan_AbsentNodeInSnapshot_FailClosed`.)*
- [x] Tecto de replans por árvore (replans **aninhados** contam para o mesmo tecto); revisão humana forçada quando o custo acumulado excede fracção do orçamento. *(Evidência: contador por-`tree_id` (aninhados no mesmo); gatilho de fracção exacto sem overflow int64 (`crossGreater` via `math/big` — fecha um fail-open). `TestReplan_NestedIncrementsSameCounter`, `TestExceedsFraction_NoInt64Overflow`, `TestReplan_NoPermanentLoop_HumanCanStop`.)*

### Estado
**FECHADO** (vaga 6 EPIC-19). Pacote `packages/control-plane/orchestrator/replan/`. Dois regimes de recuperação (antes/após commit — retoma sempre o subgrafo original em falha). O QA fechou dois fail-opens (nó ausente do snapshot; overflow int64 na fração); **eu fechei à mão o TOCTOU de escalada de autonomia** (residual LOW declarado): `pinAndCheckLevel` torna check-and-pin atómico, com teste de concorrência falsificável (`-race`, falha-antes reproduzida por reversão). Zero-dep, `go.mod` intacto, `-race` verde.

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
- [x] Nó `capability_gap` bloqueia (`waiting_on_capability`) até ratificação. *(Evidência: `capabilitygap/capabilitygap.go` — `CanDispatch()` só admite `StateResolved`, alcançável só via ratificação assinada+verificada; estado inicial `StateWaiting`. Emite `plan.capability_gap_opened`/`_resolved` (constantes de plannerevents). `TestNode_DoesNotDispatch_UntilResolved`.)*
- [x] Skill gerada por **agente-autor governado** (NHI, orçamento, allowlist restrita) que trata a spec do gap como **input untrusted** com taint no eval-gate. *(Evidência: NHI on-behalf-of, orçamento `Reserve/Commit/Release`, allowlist imposta por este pacote; `TestAllowlistViolation_FailClosedAndReleasesBudget` (tool fora da allowlist ⇒ fica `waiting` + orçamento libertado).)*
- [x] Pipeline: dry-run (AOS-126) → eval-gate (AOS-114/115/189) → canary → ratificação assinada (AOS-096/206); humano pode substituir/rejeitar o nó. *(Evidência: sequência de estados imposta (`ErrStageOutOfOrder`); `TestSelfAuthored_NotToProduction_Unilaterally` (bypass `Author→Ratify` recusado); anti-transplante por content-hash + exigência de `Verified` — `TestRatification_TransplantRejected`/`_UnverifiedApprovalDoesNotPromote`.)*

### Estado
**FECHADO** (vaga 5 EPIC-19). Pacote `packages/control-plane/orchestrator/capabilitygap/`. Entrega a GOVERNAÇÃO do gap (máquina de estados fail-closed, agente-autor governado, pipeline sem bypass, teto de gaps por plano); zero-dep, `go.mod` intacto, `-race` verde (14 testes). Fronteiras honestas (§5): o **executor de skills** (runtime que carrega/corre a skill ratificada) é desenho separado — este ticket NÃO o entrega; a assinatura cripto real da ratificação vive na porta `Ratifier` (espelha `hitl.RatificationGate`, AOS-096/206), ligada pelo wiring.

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
- [x] Prompt estático, cache-estável (ADR-009), SemVer; mudanças passam pelo pipeline ADR-012. *(Evidência: `plannerprompt/` — artefacto de prompt versionado (SemVer próprio); mutação sujeita a `ValidateGoldenMutation` (bump exigido).)*
- [x] Golden-set: entradas `(objectivo, contexto) → asserções` (estruturais + semânticas), verificáveis pelo validador (AOS-231) + rubrica. *(Evidência: `goldenset.go`/`eval.go` — estruturais via `planvalidate` (importado), semânticas via rubrica de predicados; `RejectsWith`/`Context` exercitados por `TestEvaluate_RejectsWithNegativeCase`.)*
- [x] Amostragem **K×** por objectivo: asserções de **segurança** a 100% de K; de **qualidade** por limiar ≥ M/K. *(Evidência: K amostras/objetivo como fixtures; segurança 100%/K, qualidade ≥M/K.)*
- [x] Trace-diffing = **regressão distribucional** sobre métricas (não plano cru); sem regressão de segurança. *(Evidência: `Regression` sobre pass-rate agregada; **fail-closed contra perda TOTAL de cobertura** — `baseline.Total>0 && candidate.Total==0 ⇒ SecurityRegressed` (o QA apanhou `0!=0=false` que deixava passar; corrigido, confirmado por reversão). `TestRegression_SecurityCoverageLossIsRegression`.)*
- [x] Mutar o golden-set é *gated* (anti-envenenamento). *(Evidência: `assertionSignature` (multiset severidade|natureza|id) apanha o **esvaziamento** (gutting) de um caso HARD retido — trocar uma asserção de segurança por rubrica sempre-verdadeira exige `RemovalApproval` explícita (`ErrHardCaseGutted`) + conta como mudança. `TestGoldenMutation_HardCaseGuttingRequiresApproval`. Limite honesto: enfraquecer o *corpo* de um predicado semântico sob o mesmo (id,severidade) não é detetável por assinatura — closures Go — mitigado por revisão na aprovação.)*

### Estado
**FECHADO** (vaga 3 EPIC-19). Pacote `packages/control-plane/orchestrator/plannerprompt/`. Eval-gate offline (staging), nunca por-run/produção; sinal de pass-rate para AOS-242. Dois fail-opens fechados pelo QA (perda total de cobertura de segurança; gutting de caso HARD). Zero-dep; `-race` verde. Handoff: ligação ao CI de staging real é fronteira declarada.

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
- [x] Sinais: taxa de aprovação **sem edição**, taxa de replan, calibração de custo (AOS-124), taxa de propostas inválidas; demoção automática em anomalia. *(Evidência: `planneraut/signals.go` `ComputeSignals` (puro) + `Envelope.Evaluate`; `ObserveWindow` demove a L0 em qualquer brecha. `TestObserveWindow_AnomalyDemotes`.)*
- [x] Promoção por domínio **recorrente** (janela sustentada, AOS-014); *ad-hoc* permanece L0 por desenho; granularidade de "domínio" declarada. *(Evidência: `MinRecurrence` janelas sãs; `domain.go` `DomainKey` = (tenant, assinatura estrutural de capabilities) — NUNCA o objective untrusted (ADR-005).)*
- [x] L4/L5 auto-aprova dentro de envelope declarado (avaliado sobre risco **derivado**); `capability_gap`/`danger` forçam sempre revisão. *(Evidência: `AuthorizeAutoApproval` sobre risco DERIVADO; **o QA fechou um fail-open real** — `elevate(RiskUnset,RiskSafe)==RiskSafe` deixava um derivado DESCONHECIDO auto-aprovar a L5 com rótulo safe; corrigido exigindo `in.Derived != RiskSafe` (o cru), mantendo "advisory só eleva". `TestAuthorizeAutoApproval_GrayAndUnsetForceReviewEvenAtL5` (falha-antes não-vacuosa), `_DerivedDangerForcesReviewEvenAtL5`, `_AdvisoryElevatesOnly`.)*
- [x] Travão de runtime independente do humano: eval-gate de decomposição (AOS-241) como pré-condição + amostragem post-hoc mesmo a L4/L5. *(Evidência: eval-gate por porta como pré-condição (reprova/erro ⇒ fail-closed) + amostragem post-hoc.)*
- [x] SLI: fracção de planeamento ≤ 5% (burn-down AOS-123; contabilidade AOS-062). *(Evidência: `PlanningFractionSLI`/`DefaultMaxPlanningFraction=0.05`; excede ⇒ demove. `TestGovernor_PlanningFractionSLIVisible`, `TestEnvelope_PlanningFractionSLIBreach`.)*

### Estado
**FECHADO** (vaga 7 EPIC-19). Pacote `packages/control-plane/orchestrator/planneraut/` (tipo `Level` L0–L5 local, sem dep de `governance/autonomy`). Promoção não-gameável ancorada no `OverrideRate` autoritativo (AOS-095, por porta — contadores próprios não bastam: `TestObserveWindow_OverridePortVetoesPromotion`); assimetria promover-devagar/demover-já. Zero-dep, `go.mod` intacto, `-race` verde. Fronteiras: aplicação a jusante (gate/override/eval-gate) por porta = do wiring; amostragem post-hoc é global (não per-domain) por desenho.

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
- [x] O plano aprovado (+ `capabilities_hash` + `prompt_version`) é persistido; o manifesto do run inclui-o. *(Evidência: `planmigrate/manifest.go` — `Manifest` fixa os 3 eixos + `PlanHash` (`HashPlan` = sha256 canónico); `TestManifestPinsThreeDistinctAxes`.)*
- [x] Replay **reproduz os eventos capturados** — nunca re-resolve o REG nem re-atravessa o RM. *(Evidência: `replay.go` — via de replay recebe só `EventReader` read-only; provado por **duplos envenenados** `failResolver`/`failMonitor` que falham o teste se tocados: `TestReplayIsDeterministic_NoREG_NoRM_NoLLM` (`reg.calls==0, rm.calls==0`) + assimetria vs escrita `TestMaterialize_TraversesREGandRM_ReplayDoesNot`.)*
- [x] Planos aprovados **congelados na versão**; nunca auto-migrados. Se a versão foi retirada antes da materialização ⇒ invalida → re-plano + re-aprovação (fail-closed). *(Evidência: `TestFrozenVersionNeverAutoMigrated` (plano 2.4.1 nunca vira `CurrentPlanVersion`); `TestRetiredVersionInvalidatesFailClosed` + `TestMaterializeRefusesRetiredBeforeTouchingREGorRM` (`ErrRetired` antes de tocar REG/RM).)*
- [x] **Janela de suporte** de MAJORs declarada; run fora da janela é **inadmissível** (como payload perdido). *(Evidência: `policy.go` `SupportWindow`/`Covers`/`Admit` ⇒ `ErrOutsideSupportWindow`; `TestRunOutsideSupportWindowIsInadmissible` com controlo (MAJOR 3 admite em [1,3], rejeita em [1,2]).)*
- [x] Bump MAJOR passa por ADR-012, com reader retido **ou** deprecação documentada (implicações AOS-079/093). *(Evidência: forma **executável** da regra (janela `MinMajor`/reader retido) em `policy.go`. O artefacto ADR-012 e as implicações AOS-079/093 são governança humana / dos seus epics — fronteira de escopo, referenciada nos doc-comments, não emitida por este pacote.)*

### Estado
**FECHADO** (vaga 3 EPIC-19). Pacote `packages/control-plane/orchestrator/planmigrate/`. Manifesto com 3 eixos pinados (schema≠comportamento≠ambiente); replay puro (duplos envenenados provam zero REG/RM/LLM); congelamento + invalidação fail-closed de versão retirada; janela de suporte de MAJORs. Reusa eventos de `plannerevents`; zero-dep; `-race` verde (14 testes).

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
 Realiza **ADR-020** (planeador como agente governado), que nomeia este ticket em `docs/adr/ADR-020-planeador-agente-governado.md` §6.
### Objectivo
Provar em teste que os vectores adversariais estão fechados.

### Critérios de Aceitação
- [x] **Plano adversarial**: objectivo/untrusted não induz spawn com efeitos indevidos (plano como dados + validação + gate + spawn mediado). *(Evidência: `planadversarial/adversarial_test.go` `TestVector_PlanoAdversarial_BarradoAntesDeQualquerEfeito` — `planvalidate.Validate` REAL rejeita com `ReasonToolInadmissible`+`Locator.NodeID=exfil`, sem mutar o doc.)*
- [x] **Downgrade de risco**: rótulo `safe` num nó irreversível é ignorado (piso derivado, AOS-232). *(Evidência: `TestVector_DowngradeDeRisco_RotuloSafeIgnoradoEmNoIrreversivel` — `deleteTool`⇒`Derived=danger`, `elevateOnly(danger,safe)=danger`, `Resolved=danger` (não auto-aprovável); + `_ResolvidoCarimbaRequiresCardNoDespachoReal` exercita a função de PRODUÇÃO `plandispatch.PlanFrom` ⇒ `RequiresCard`, bloqueio no despacho.)*
- [x] **Exaustão de fan-out**: plano gigante barrado por tectos (AOS-231) + teto por nó + breaker (AOS-029). *(Evidência: `TestVector_ExaustaoFanout_BarradaPorTectosEBreaker` — 3 camadas: `MaxFanout` estrutural (40>8⇒`ReasonMaxFanoutExceeded`), `checkBudget` teto por-nó, circuit breaker.)*
- [x] **Gaming do intake**: classificação forçada a "simples" reentra no gate (AOS-233). *(Evidência: `TestVector_GamingDoIntake_SimpleForcadoReentraNoGate` — `intake.Classify` avalia sinais de meta antes de simple + não-bypass do gate por-spawn (`DelegationGuard`).)*
- [x] **Injecção via retry**: feedback estruturado/allowlisted, sem re-injecção in-band. *(Evidência: `TestVector_InjeccaoViaRetry_FeedbackAllowlistedSemEco` — `validNodeID` rejeita id malformado com `Locator` vazio, marcador de injeção ausente de `renderVerdict` (coordenadas estruturais, não conteúdo cru).)*

### Estado
**FECHADO** (vaga 7 EPIC-19, capstone). Pacote `packages/control-plane/orchestrator/planadversarial/` — um teste NEGATIVO por vector, cada um a exercitar o pacote REAL (`planvalidate`/`intake`/`plandispatch`), não mocks. O QA fechou um gap: um vector usava um helper em vez da função de produção `plandispatch.PlanFrom` — corrigido a exercitá-la. Zero-dep, `go.mod` intacto, `-race` verde (6 testes). **DoD residual do épico:** o gate SAST/SCA (gosec/govulncheck) triado corre no fecho do épico (não é CA deste pacote).

### Detalhes Técnicos
- Cada teste mapeia a uma linha da tabela de riscos `tecnica/18` §9.

### Testes Requeridos
- Um teste negativo por vector; falso-negativo falha o gate.

### Definition of Done
- Suite verde; gate SAST/SCA triado.

### Handoff para Claude Code
- Guard-tests no estilo das 5 negações do composition-root.

---

## AOS-388 — Decomposer LLM de produção: goal → PlanDocument multi-nó, ponta-a-ponta no aos-orq

<!-- rtm: adrs-mencionados -->
<!-- Os ADR-NNN citados neste bloco (ADR-005/018/019/023) são MENÇÃO — constraints que o
     ticket respeita — não implementação. A realização vive nos tickets AOS-234/237/026 e no
     código; este é o ticket de composição/wiring. Mesmo molde de AOS-313. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | 3 — Escala e controlo (graduação da decomposição offline para viva) |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-234 (planeador governado), AOS-241 (prompt+golden-sets), AOS-237 (materialização), AOS-026 (Delegator), AOS-281 (composição do aos-orq sob lease) |
| Bloqueia | — |
| Fecha | DEF-803 (decomposição goal→DAG é stub de nó único); resíduo de decomposição real atribuído a AOS-025 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/18` §3.3/§3.6, ADR-005 (untrusted), ADR-018/ADR-023 (fronteira nó↔ORQ), ADR-019 §2.5 (camadas), `packages/control-plane/orchestrator/planvalidate/budget.go` (precedente da porta `Pricer`) |

### Contexto
Esta epic (§1) entregou o planeador como agente governado, mas com a decomposição LLM validada por **doubles offline** — a §2 e o §6 nomeiam explicitamente o *wiring* vivo do Model Gateway como dependência **fora** de âmbito (EPIC-06/integração). Este ticket fecha essa dependência nomeada. Em concreto: `planner.Planner.Decompose` (mediação RM, reserva CAS, NHI `agent:planner`, N tentativas, gate de forma) está completo, mas delega a decomposição à porta `planner.Decomposer`, para a qual só existem *fakes* de teste. O `orchestrator.Submit` continua um stub de 1 nó (`contract.NewMinimalGraph`), e o binário `aos-orq` (que re-hidrata o `GraphBuilder` sob lease — AOS-281 — e materializa planos) recebe os nós por flags `--nodes`/`--plan-doc` e **recusa** o spawn (`recusaSpawn`). Falta a única peça viva: um `Decomposer` de produção que chame o modelo real e devolva um `plan.PlanDocument` multi-nó.

### Objectivo
Entregar o `Decomposer` de produção e compô-lo com o planeador e o pipeline existente dentro do `aos-orq`, ligando `Delegator.Spawn`, de modo a que um único `aos-orq serve --goal` produza, valide, aprove e materialize um DAG **realmente multi-nó** a partir de um objectivo.

### Critérios de Aceitação
- [x] `decompose.LLMDecomposer` satisfaz `planner.Decomposer`: monta `system = plannerprompt.Current.Template` e `user = goal (untrusted) + snapshot`; parseia a resposta com `plan.Decode` **fail-closed** (reutiliza o parser sancionado, não reimplementa); carimba `planner_meta{model, prompt_version, capabilities_hash}`. O documento é tratado como **untrusted** (ADR-005) — nunca executado, nunca marcado trusted.
- [x] **Camadas (decisão por precedente, não nova):** o Decomposer vive em `control-plane/orchestrator/decompose` e depende apenas de control-plane+kernel, através de uma **porta de modelo local injectada** — o **mesmo padrão** da porta `Pricer` de `planvalidate/budget.go`, que declarou evitar puxar `platform/model-gateway` para não abrir exceção nova ao layer-lint. O concreto do gateway compõe-se no `aos-orq` (binário exempto por ADR-018). `layer-lint` verde **sem** nova entrada de baseline nem emenda ao ADR-019 §2.5.
- [ ] `aos-orq serve --goal "…"` corre ponta-a-ponta: `Decompose` → `planvalidate.Validate` (sobre o snapshot pinado cujo hash é o carimbado em `planner_meta.capabilities_hash`) → `PlanGate.Approve` → `planmaterialize.Materialize`, com nós-folha no DAG (AOS-025) **e** papéis-que-expandem via `Delegator.Spawn` (AOS-026). `recusaSpawn` deixa de ser o caminho por omissão; `--nodes`/`--plan-doc` ficam só como override manual. **PARCIAL — ver Estado.**
- [x] **Determinismo de teste:** o modelo é injectado; um *fake* determinístico cobre o CI, sem chamada viva. O retry fica do `Planner` (N tentativas + reserva escalada) — **não** é duplicado no Decomposer.
- [ ] **Produção fail-closed:** o `aos-orq` exige credencial de modelo em produção (espelha `ErrProductionNeedsModelCredential` do nó); sem ela, o caminho vivo não arranca. **DEFERIDO a AOS-391 (T2-B) — ver Estado.**
- [x] O guard `boundary_orq_sch_test.go` (grafo de build de `cmd/aos`) permanece verde — nada disto entra no nó `aos`.

### Estado (actualizado 2026-09-10, após AOS-393 aterrar)
O núcleo do ticket está **entregue**; o que resta está **deferido e ticketado** (não é dívida por diagnosticar):

- **Cumpridos:** AC 1 (Decomposer untrusted + `plan.Decode` + proveniência carimbada), AC 2 (camadas / porta local, `layer-lint` verde), AC 4 (determinismo / *fake* + retry no Planner), AC 6 (guard `boundary_orq_sch_test.go` verde).
- **AC 3 — eixo *papéis-que-expandem*: CUMPRIDO por AOS-393.** O `--goal` corre `Decompose → planvalidate.Validate → planmaterialize.Materialize` com o **Delegator real**, materializando nós-folha **e** papéis-que-expandem (`spawn: no=…`); `recusaSpawn` deixou de ser o caminho por omissão. Provado por execução + `TestAOS393_GoalExpansaoSpawnaPapel`.
- **AC 3 — eixo `PlanGate.Approve`: DEFERIDO.** O gate humano (AOS-236) não está no pipeline `--goal`; o wiring `PlanDocument→gate` é o **DEF-274** (eixo AOS-238), fora do âmbito por decisão do dono.
- **AC 5 — modelo real + credencial de produção: DEFERIDO a AOS-391 (T2-B).** Hoje o `--goal` sem `--decompose-fixture` recusa fail-closed (não há LLM vivo neste binário); o mecanismo que espelha `ErrProductionNeedsModelCredential` chega com o Model Gateway composto.
- **Fronteira de deploy:** nada disto entra no nó deployado `cmd/aos` (AC 6 / ADR-018); o `aos-orq` não está no compose de produção. Ver `docs/reports/auditoria-revisao-AOS-388.md`.

### Detalhes Técnicos
- **Novo pacote:** `packages/control-plane/orchestrator/decompose/` (mesmo módulo que `planner`/`plan`/`plannerprompt`). Imports: `orchestrator/{planner,plan,plannerprompt}` + a porta de modelo local. Sem `require`/`replace` novo para o gateway.
- **Fluxo do `Decompose`:** montar mensagens → chamar o modelo pela porta → extrair o JSON → `plan.Decode` (fail-closed) → devolver `PlanDocument` untrusted. Erro de transporte ou documento malformado ⇒ erro (conta como tentativa; o `Planner` re-tenta).
- **Wiring no `aos-orq`:** adaptador porta-de-modelo → `modelgateway.NewModelClient` (reutilizar, não reescrever o cliente); `planner.NewPlanner(reserver, mediator, issuer, decompose.New(model))`; substituir `recusaSpawn` por `planmaterialize.NewDelegatorSpawner(delegator, …)` com um `orchestrator.Delegator` real.
- **Tasks:** **T1** Decomposer isolado + teste em ilha → **T2** wiring do planeador no `aos-orq` → **T3** `Delegator.Spawn` real → **T4** arranque/config (`--goal`, credencial, snapshot). T3 e T4 são paralelos após T2 (ambos editam `main.go` — coordenar merges).

### Testes Requeridos
- Unit em ilha (`decompose`): goal→multi-nó válido; malformado→erro fail-closed; carimbo de `planner_meta`; JSON com prosa à volta; `-race`.
- Wiring `aos-orq`: e2e goal→materializado com *fake* de modelo; o teste de dois-processos (lease) continua verde.
- Reutilizar a suite adversarial AOS-244 (plano hostil / downgrade de risco) sobre o caminho vivo.

### Definition of Done
- Pacote novo testado, zero-dep externa; `layer-lint`/`deferrals`/event-catalog verdes; DEF-803 fechado no `docs/governance/REGISTO-Deferimentos.md`; RTM (`tecnica/16`) regenerada; revisão por 2 revisores (artefacto P0-adjacente).

### Handoff para Claude Code
```text
Implementa AOS-388 (EPIC-19): Decomposer LLM de produção + wiring multi-nó no aos-orq.
- Novo pacote control-plane/orchestrator/decompose: LLMDecomposer satisfaz planner.Decomposer.
- Porta de modelo LOCAL injectada (padrão Pricer de planvalidate/budget.go); NÃO importar platform/model-gateway.
- Reutiliza plan.Decode (parsing) e plannerprompt.Current (system prompt); output untrusted (ADR-005).
- Wiring no aos-orq: adaptador → modelgateway.NewModelClient; Planner.Decompose alimentado; Delegator.Spawn real (fim do recusaSpawn).
- Retry fica do Planner; teste em ilha com modelo fake determinístico; produção exige credencial fail-closed.
- NÃO tocar em cmd/aos (guard boundary_orq_sch_test.go tem de ficar verde). Regenera a RTM. Abre PR com o template §7 dos Standards.
```

---

## AOS-389 — Guard fail-closed: recusar planos com arestas condicionais até o avaliador estar composto

<!-- rtm: adrs-mencionados -->
<!-- O ADR-022 citado neste bloco é MENÇÃO — a invariante §2.1 (`branch_not_taken`) que o
     ticket protege — não implementação. A avaliação real vive em AOS-390; este é o guard de
     transição que troca a violação fail-open por recusa declarada. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | Habilitador / correcção |
| Milestone | v1.1 (correcção fail-open no caminho de produção) |
| Tipo | feature (correcção) |
| Prioridade | P0 |
| Estimativa | S |
| Dependências | AOS-237 (materialização), AOS-388 (T2-A — o caminho `--goal` que este guard protege) |
| Bloqueia | AOS-391 (o T2-B não deve alargar o buraco fail-open) |
| Superado por | AOS-390 (que substitui a recusa por avaliação real) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/control-plane/orchestrator/planmaterialize/materialize.go`, `packages/cmd/aos-orq/planner_wiring.go`, `packages/cmd/aos-orq/main.go`, ADR-022 §2.1, `analises/10_Auditoria_ORQ_SCH_PDP_Adversarial.md` |

### Contexto
Medido em 2026-09-10, com teste na composição real: `Materialize` **ignora `conditional_on` em silêncio** e dá efeito a todos os nós eagerly. Via `--goal (+fixture)` um nó condicional-papel é **spawnado como sub-agente real** (NHI cunhada, orçamento reservado, `subagent.spawned`) sem a condição ser avaliada; via `--plan-doc` uma folha condicional materializa. Não existe guarda fail-closed específica de condicionais entre a entrada e o efeito: o schema aceita `conditional_on`, o `planvalidate.Validate` admite planos condicionais bem-formados, e o `--plan-doc` nem chama o validador. O único componente que avalia condições e poda `branch_not_taken` — o `plandispatch.Dispatcher` — **não tem chamador de produção** (ver AOS-390). O efeito é uma **violação fail-OPEN do ADR-022 §2.1**: um ramo que devia ser podado é executado — ao contrário de toda a disciplina do sistema, que recusa quando não consegue decidir.

### Objectivo
Enquanto o avaliador de ramos não estiver composto (AOS-390), o caminho de produção deve **recusar fail-closed** qualquer plano que contenha `conditional_on`, em vez de o executar em silêncio — trocando uma violação fail-open por uma recusa declarada e observável.

### Critérios de Aceitação
- [ ] `Materialize` (ou a fronteira de admissão a montante) recusa, com erro tipado (p.ex. `ErrConditionalNaoComposto`), qualquer `PlanDocument`/`Plan` cujo grafo contenha ≥1 aresta `conditional_on`; **nenhum** nó é materializado nem spawnado.
- [ ] A recusa vale nas **duas** superfícies: `--goal` (com e sem `--decompose-fixture`) e `--plan-doc --snapshot`. Um teste por cada.
- [ ] Não-regressão: um plano **sem** condicionais continua a materializar e a spawnar exactamente como hoje (caso negativo).
- [ ] Teste de composição real que reproduz o cenário medido (`A` produtor, `B conditional_on A {verdict=fail}`): antes, `B` era spawnado; depois, o plano é recusado e nem `A` nem `B` têm efeito.
- [ ] O erro nomeia o eixo que o fecha (AOS-390) e é declarado no banner/postura como fronteira conhecida, não como falha.
- [ ] Guard-test de fonte: um teste falha se algum caminho de materialização voltar a dar efeito a um nó com `conditional_on` sem avaliação (sela contra a regressão do fail-open).

---

## AOS-390 — Compor o despacho governado do Planeador (`plandispatch.Dispatcher` sob Tenure)

<!-- Este ticket IMPLEMENTA o ADR-024 (o seu próprio) e torna o ADR-022 §2.1 efectivo em
     runtime (a poda `branch_not_taken` deixa de ser schema-só e passa a decisão do despacho
     composto); opera sob o ADR-023 (SCH derivador, escritor único por run). Sem marcador
     `rtm:adrs-mencionados`: as citações contam como implementação. Consome os readers de
     DEF-272/273 (fechados por AOS-281); NÃO fecha DEF-274/275 (wiring do gate AOS-236). -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | 3 — Composição no nó |
| Milestone | v1.1 (distribuído) |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-281 (composição ORQ/SCH↔nó sob lease), AOS-237 (materialização), AOS-238 (porta do scheduler), AOS-389 (guard que este supersede) |
| Bloqueia | AOS-392 (prova multi-processo), AOS-391 (T2-B ponta-a-ponta) |
| Fecha | O gap de despacho não-composto (fail-open de condicionais, medido; documentado em `analises/10`): o `plandispatch.Dispatcher` passa a ter chamador de produção. **NÃO** fecha DEF-274/DEF-275 — esses são o wiring `PlanDocument→planapproval.Plan` do GATE de aprovação (AOS-236), eixo distinto do despacho. Consome os readers de DEF-272/273 (fechados por AOS-281) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | **ADR-024** (a mudança de fronteira do AOS-237: a materialização admite, o despacho produz efeito), `packages/control-plane/orchestrator/plandispatch/{dispatch.go,ports.go,branches.go,condition.go}`, `packages/control-plane/runlifecycle/{readers.go,emitters.go}`, `runlifecycle.Tenure`, ADR-022, ADR-023, DEF-272/273/274/275. Decisão de desenho (Opção A) na secção «Objectivo». |

### Contexto
Medido: `plandispatch.NewDispatcher` só aparece em `_test.go`; o `DispatchSink` não tem implementação de produção (só o duplo `sinkRegistador`); o `aos-orq` não importa `plandispatch`. O pipeline T2-A (`planner_wiring.go`) termina em `Materialize` + spawn-all eager, **sem** gating de elegibilidade — logo o `depends_on` e as arestas condicionais de um plano aprovado não são respeitados em runtime. As portas de leitura que o Dispatcher consome (`LifecycleView`/`ResultView`/`PayloadView`/`BranchJournal`) **já estão implementadas** por `runlifecycle` (DEF-272/273 fechados por AOS-281). O que falta é o **chamador** e o **sink**. Esta é a etapa que DEF-272/273/274 nomeavam como eixo `AOS-238` — mas AOS-238 está **fechado** e o seu guard-test `TestBoundary_ProductionImportsAreAllowlisted` proíbe o import do módulo de ciclo-de-vida; paga-se com **ticket novo** (precedente: AOS-281).

### Objectivo
Compor o `plandispatch.Dispatcher` de produção dentro do `aos-orq serve`, sob `runlifecycle.Tenure` (lease + fencing), como **escalonador re-invocável por passagem** que: lê o estado do ciclo de vida, decide elegibilidade (`NodePending` + `depends_on` cumpridos), **avalia arestas condicionais e poda `branch_not_taken`**, consulta o card oracle, aplica headroom de concorrência com diferimento, e entrega os nós **elegíveis** a um `DispatchSink` de produção — **in-process**, que delega o efeito ao `Delegator.Spawn`/`GraphBuilder` que já existem. Substitui a recusa do AOS-389 por avaliação real. O SCH continua **derivador** (não escreve ciclo de vida — invariante ADR-023).

**Decisão de desenho — Opção A (dono, 2026-09-11).** O discovery mostrou que o `Materializer` hoje **funde** admitir-no-DAG e produzir efeito (spawn de papel up-front) — compor o Dispatcher por cima daria **duplo-efeito**. Por isso o AOS-390 **separa** as duas coisas: a materialização passa a **admissão-pura** (apensa `plan.materialized` e admite todos os nós no DAG como pendentes, sem `Delegator.Spawn` nem arranque), e o efeito por-nó move-se INTEIRAMENTE para o `DispatchSink` do Dispatcher, disparado só quando o nó fica elegível. Papéis-que-expandem passam a ser representados no DAG como nós pendentes. Reconciliar também `planmigrate.Migrator.Materialize` (2ª porta de materialização, sem chamador de produção) ao mesmo modelo.

Alternativas rejeitadas: **(B)** o Dispatcher gateia ANTES da materialização — contradiz `plandispatch.PlanFrom`, que projecta o conjunto despachável a partir do MATERIALIZADO (luta contra o desenho do Dispatcher); **(C)** dois caminhos (eager sem condicionais, diferido com condicionais) — dois mecanismos divergentes para o mesmo problema, o anti-padrão que o ADR-021/DEF-271 já custou. Esta decisão **extende** o AOS-237 (a materialização continua a existir e a apensar `plan.materialized`); move apenas o *momento* e o *gating* do efeito, e mantém-se DENTRO do ADR-023 (o Dispatcher não escreve ciclo de vida; o efeito é sob a posse do lease). O ADR formal (que regista esta mudança de fronteira do AOS-237) é cunhado no canon e ratificado pelo dono na **conclusão** deste ticket, junto com a integração RTM.

> **Nota de fronteira (anti-scope-creep).** O sink é **in-process**: no modelo per-run do ADR-023 o processo dono corre o laço de despacho para os seus runs, e o efeito delega no `Delegator` local. **Não** é preciso um work-queue sobre JetStream para distribuir os NÓS de um run entre processos — isso seria distribuição intra-run, que o modelo per-run torna desnecessária, e fica **fora** da v1.1.

### Critérios de Aceitação
- [ ] `aos-orq serve` compõe um `plandispatch.Dispatcher` de produção sob `Tenure`; `NewDispatcher` passa a ter chamador fora de `_test.go` (um guard-test "aos-orq importa plandispatch em produção" fica verde).
- [ ] Existe uma implementação de produção de `DispatchSink` que delega o efeito ao `Delegator.Spawn` (papéis) e ao `GraphBuilder`/`LeafAdmitter` (folhas) — o mesmo efeito governado (RM + reserva CAS + NHI) que hoje corre eager, mas **só para nós elegíveis**.
- [ ] **Elegibilidade por estado**: só nós `NodePending` são despachados; um nó materializado/terminal não é re-despachado (idempotência por `(run_id, step_id)`).
- [ ] **Gating por `depends_on`**: um nó com dependência não concluída **não** é despachado (teste: `B depends_on A`, `A` pendente ⇒ `B` não spawna).
- [ ] **Arestas condicionais + poda `branch_not_taken`** (ADR-022 §2.1): `B conditional_on A {verdict=fail}` — se `A` passa, `B` é decidido `branch_not_taken` e **não** tem efeito; se `A` falha, `B` é despachado. Prova pela composição real, caso positivo **e** negativo. **Isto fecha a violação medida no AOS-389.**
- [ ] **Headroom de concorrência**: com headroom esgotado, os nós elegíveis excedentes ficam `OutcomeDeferredHeadroom` e são retomados numa passagem seguinte quando a concorrência liberta — sem os perder e sem fail-open.
- [x] **Card oracle**: um nó cujo card exige aprovação humana (`danger`) fica `waiting`, não é spawnado sem o gate (AOS-236). *(Fechado pelo **AOS-408**: o `cardsFailClosed` — que recusava sempre e nunca era consultado, porque o `needsCard` era `false` — deu lugar ao `runlifecycle.PlanDecisionReader` (decisão do plano derivada do log) e a um `needsCard` que é a projecção do cartão pelo risco RESOLVIDO. `TestAOS408_PlanoDeRiscoNaoMaterializaSemAprovacao` e `TestAOS408_DecisaoAssinadaAprovaEDepoisMaterializa`.)*
- [ ] **Semântica re-invocável**: o Dispatcher é função por passagem, sem laço próprio; o escalonador do `serve` re-invoca-o quando `ResultView`/`LifecycleView` mudam ou headroom liberta. **Não escreve ciclo de vida** (teste: o stream do run não cresce por escrita do dispatcher numa passagem só de leitura).
- [ ] **Sob Tenure**: um dispatcher cuja posse foi superada é recusado por fencing (`ErrStaleFencingToken`) sem tocar no log — herda a disciplina de AOS-281.
- [ ] **AOS-389 é superado**: o guard fail-closed de condicionais é substituído pela avaliação real; o guard-test de não-regressão de AOS-389 passa a assertar avaliação em vez de recusa.
- [ ] **Registo**: a row de deferimento do gap de despacho passa a `FECHADO-RESIDUAL`/removida; DEF-274 e DEF-275 têm o Eixo corrigido para este ticket. RTM regenerada. *(Esta caixa fica por marcar e assim deve ficar: o eixo de DEF-274/275 foi corrigido no **AOS-408** — para si próprio —, não neste ticket. Marcá-la seria afirmar que este ticket fez o que não fez; o AOS-408 fechou o DEF-274 e o DEF-275 continua ABERTO com eixo válido.)*

---

## AOS-391 — T2-B: compor o `decompose.Model` real via Model Gateway

<!-- rtm: adrs-mencionados -->
<!-- Os ADR-005/019/020 citados neste bloco são MENÇÃO — constraints que o ticket respeita
     (documento untrusted; camadas; fidelidade do token NHI) — não implementação. A realização
     do wiring vivo do Model Gateway vive no código do aos-orq; fecha DEF-803. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | Habilitador do goal→DAG real |
| Milestone | v1.1 |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-388 (T2-A), AOS-390 (para que goals reais com condicionais sejam avaliados, não recusados), AOS-278 (cutover de identidade NHI) |
| Bloqueia | AOS-392 (prova ponta-a-ponta com goal real) |
| Fecha | — (DEF-803 já **FECHADO-RESIDUAL** via AOS-388/AOS-393; este ticket não o re-fecha) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos-orq/planner_wiring.go` (`fixtureModel`, `modeloDeDecomposicao`), `packages/control-plane/orchestrator/decompose/decompose.go`, `packages/platform/model-gateway/`, ADR-019 §2.5, ADR-020, ADR-005 |

### Contexto
Medido: sem `--decompose-fixture`, `--goal` recusa fail-closed com erro que nomeia o "Model Gateway" (`TestAOS388_GoalFailClosed`); o único `decompose.Model` é o `fixtureModel` (marcado **NÃO-PRODUÇÃO**); o `aos-orq` não importa `platform/model-gateway`. **DEF-803** já está **FECHADO-RESIDUAL** (a decomposição multi-nó produtiva aterrou com AOS-388/AOS-393); o `STUB` de `orchestrator.Submit` fica como contraste, não como dívida. AOS-391 é **ortogonal**: entrega o **LLM vivo** (hoje só o `fixtureModel` corre), não re-fecha o DEF-803. Ligar exige token NHI com `model:invoke` verificado e uma decisão **ADR-020** sobre a fidelidade do token (token do run vs. `agent:planner`).

### Objectivo
Compor um `decompose.Model` de produção que invoca o Model Gateway para produzir o `PlanDocument` a partir do `goal`, sob a identidade e o orçamento corretos, substituindo o `fixtureModel` no caminho `--goal`. Fecha DEF-803 e o critério de saída do goal→DAG real.

### Critérios de Aceitação
- [x] `--goal` **sem** `--decompose-fixture` produz um `PlanDocument` via Model Gateway (deixa de recusar); `--decompose-fixture` continua disponível para testes offline. *(Cablagem entregue: `packages/cmd/aos-orq/model_gateway_wiring.go` — `gatewayDecomposeModel` fala directo com `port.Gateway.Chat` (system+user); composição via `modelgateway.NewProduction` em `decomporEMaterializar`. Build OFFLINE verde. O caminho VIVO é **env-gated** — `AOS_MODEL_ENDPOINT`+`AOS_MODEL_NAME`+credencial+rede — corre onde há endpoint, como os testes `--nats` cluster-gated.)* **Reaberto a 2026-09-16:** com o modelo de produção a chamada chega ao gateway e é selada, mas o planeador recusa os três planos (`plan: objective de topo em falta`), porque o prompt não declara o schema que o decode exige. **Fechado outra vez a 2026-09-16 pelo AOS-400:** com o prompt 1.2.0, o run `run-aos400-prod-1789593240` decompôs à primeira tentativa um plano de 2 nós, que foi validado, materializado e despachado.
- [x] A invocação corre sob NHI com `model:invoke` **verificada**; a decisão ADR-020 está documentada e implementada. *(O token do run sela `model:invoke` (`planner_wiring.go`, `coordCaps`); o estágio authn REAL do gateway (`authn.New(verifier, autoridadeModelo, LoadPolicy())`) verifica-o fail-closed. Fidelidade ADR-020 RESIDUAL declarada: usa-se o token do RUN, não o `agent:planner`, porque o `planner.Planner` não expõe o token filho ao decompositor — follow-up no control-plane.)*
- [~] A reserva de planeamento é admitida antes da decomposição (AOS-234) — SIM (via `planner.Planner`). O custo do turno para o burn-down (AOS-259) — `Cost: nil` (sem tabela de preços montada) ⇒ transporta ZERO declarado; montar o `cost.Recorder` (à imagem de `cmd/aos/model_pricing_env.go`) é follow-up.
- [x] O `PlanDocument` passa pelo validador puro (AOS-231) e, se tiver condicionais, são **avaliadas** por AOS-390 (landed) — nem recusadas nem fail-open. *(O caminho `--goal` valida com `planvalidate.Validate` e despacha via o Dispatcher composto.)*
- [x] Fail-closed preservado: falha do gateway / token sem `model:invoke` / resposta sem escolhas ⇒ erro, nada spawnado. *(Testado: `TestAOS391_GatewayDecomposeModel_SemEscolhasFailClosed`; deny do authn propaga; `main` recusa sem fixture nem gateway.)*
- [~] Golden-set/eval-gate (AOS-241) com o modelo atrás de doubles: o adaptador é provado offline com `fakeGateway` (`TestAOS391_*`); a integração com o harness de eval-gate do planeador não foi tocada — follow-up.

> **Realização (2026-09-11).** Bloqueio 1 (AOS-390 não-landed) CAÍDO — AOS-390 fundido, condicionais avaliadas. Bloqueio 2 (LLM vivo offline) tratado como os testes cluster-gated: **cablagem + fake offline entregues; o caminho vivo é env-gated**. `go.mod` do aos-orq ganhou `model-gateway` (+ scheduler/audit/registry/memory/eval) por `replace` path-local — build offline verde, zero deps externas. DEF-803 mantém-se FECHADO-RESIDUAL (este ticket não o re-fecha).

### Pré-requisitos e bloqueios de ambiente (discovery 2026-09-10)
Discovery read-only registada para não perder o trabalho. **Dois bloqueios independentes impedem o fecho de AOS-391 num ambiente offline:**

- **BLOQUEIO 1 — dependência AOS-390 não-landed.** O AC "arestas condicionais são avaliadas por AOS-390" não é satisfazível: `plandispatch.Dispatcher` não tem chamador de produção (`plandispatch/dispatch.go` é a biblioteca; `grep plandispatch.(New|Dispatcher)` fora de `_test.go` = 0), e o `--goal` do `aos-orq` faz spawn **eager** de todos os nós (`planner_wiring.go`, sem gating de elegibilidade). Com AOS-389 (guard) e sem AOS-390, um plano real com arestas condicionais é **recusado**, não avaliado. **AOS-390 tem de aterrar primeiro.**
- **BLOQUEIO 2 — o LLM vivo é inverificável offline.** O AC de cabeçalho exige uma chamada de modelo viva (`--goal` sem `--decompose-fixture`), que precisa de rede + endpoint OpenAI-compatível; este ambiente é `GOPROXY=off`, sem rede. A cablagem + um *fake* são testáveis; o caminho vivo só num ambiente com rede/endpoint.

**Pré-requisitos técnicos apurados (evidência, para quem implementar):**
- **Build offline da cablagem: FAZÍVEL** — o fecho transitivo de `platform/model-gateway` é `aos-ref` path-local + stdlib, **zero deps externas** (nenhum `go.sum` na cadeia). Adicionar ao `aos-orq/go.mod` 6 pares `require v0.0.0`+`replace` path-local (`model-gateway`, `scheduler`, `audit`, `registry`, `memory`, `eval`). *(Análise estática dos go.mod; não build-testado.)*
- **Adaptador de forma necessário:** a porta `decompose.Model.Complete(ctx, system, user) (string, error)` (`decompose/decompose.go:40`) **não casa** com `ModelClientAdapter.Call(PromptView)→ModelResponse` (colapsa system/user numa só mensagem). Via fiel: falar direto com `port.Gateway.Chat` passando `[]port.Message{{RoleSystem, system}, {RoleUser, user}}` e devolver `resp.Choices[0].Message.Content`.
- **Identidade (ADR-020):** a chamada tem de correr sob a NHI `agent:planner`, mediada pelo RM. Hoje o token `agent:planner` no `aos-orq` **não sela `model:invoke`** (`planner_wiring.go` — classes só selam `cap:plan`+tool caps); há que selá-lo e replicar no `aos-orq` o estágio authn do cutover **AOS-278** (landed em `cmd/aos`, ausente no `aos-orq`).
- **Construção do gateway:** `modelgateway.NewProduction(ctx, ProductionConfig)` → `NewModelClient(gw, model, WithPrincipalFromContext(...))`, espelhando `cmd/aos/modelgatewaywiring.go:newGatewayModelClient` (credencial `AOS_MODEL_API_KEY_PATH`, endpoint `AOS_MODEL_ENDPOINT`, egress SSRF `AOS_MODEL_EGRESS_HOSTS`, audit WORM, custo AOS-259). O token do run/planeador viaja pelo ctx (equivalente a `modelCredentialFromContext`), não fixado na construção.
- **Ponto de injeção:** `modeloDeDecomposicao(fixturePath)` em `planner_wiring.go` — manter o `fixtureModel` como override de teste e adicionar o ramo de produção (gateway) quando não há fixture e há credencial.

---

## AOS-393 — Fix fail-closed: o ramo papéis-que-expandem via `Delegator.Spawn` é recusado no `--goal` (depth_mismatch; `agent.spawn` latente)

<!-- rtm: adrs-mencionados -->
<!-- O ADR-018 (fronteira nó↔ORQ) citado é MENÇÃO — a disciplina que este fix respeita — não
     implementação. A materialização é do ticket AOS-237 e a delegação com orçamento do ticket
     AOS-026; este é o fix de integração do seam entre ambas, exposto pelo wiring de AOS-388
     (T2-A). -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | Habilitador / correcção |
| Milestone | v1.1 (correcção fail-closed no caminho de produção) |
| Tipo | feature (correcção) |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-388 (T2-A — o wiring `--goal` que expôs o defeito), AOS-026 (Delegator), AOS-237 (materialização / `RoleSpawn`) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/control-plane/orchestrator/planmaterialize/materialize.go` (`RoleSpawn`, caso `SpawnRole`), `packages/control-plane/orchestrator/delegation.go` (`ErrDepthMismatch`, `parentChainDepth`, `spawnToolID`), `packages/cmd/aos-orq/planner_wiring.go` (RM mínimo + `NewDelegator`), `docs/reports/auditoria-revisao-AOS-388.md` (§2.1) |

### Contexto
Medido em 2026-09-10 por **execução real** do binário `aos-orq` (não por leitura): um `--goal (+fixture)` cujo `PlanDocument` tenha um nó com dependentes — um papel-que-expande (`SpawnRole` por `DefaultClassifier`) — **aborta a materialização fail-closed** e nenhum plano com expansão materializa. Comando e saída reproduzidos:

```
aos-orq serve --goal "…" --snapshot snap.json --decompose-fixture plano-expansao.json --worker p1
  → decomposto: objectivo -> plano de 2 nos (tentativas=1, planner_nhi=agent:planner)
  → aos-orq: materialização: planmaterialize: spawn do papel "recolha":
    orchestrator: spawn recusado — profundidade declarada abaixo da autoritativa
    (fail-closed): declarada=0 autoritativa=1
```

**Causa raiz (proven):** `planmaterialize.RoleSpawn` (materialize.go:174-185) **não tem campo de profundidade**; o adaptador `NewDelegatorSpawner` passa portanto `Depth=0` ao `Delegator.Spawn`, enquanto a profundidade **autoritativa** derivada da cadeia do token do run (`parentChainDepth`) é 1. Como `req.Depth (0) < autoritativa (1)`, o Delegator recusa com `ErrDepthMismatch` (delegation.go:415-423) — a guarda anti-subdeclaração de `max_depth` dispara contra o próprio wiring. Todos os testes anteriores usavam um `fakeSpawner` (nunca o Delegator real), pelo que o seam nunca foi exercitado; o wiring de AOS-388 é a **primeira composição real** e expõe-o.

**Defeito latente (não alcançado em runtime, static-only):** mesmo corrigida a profundidade, o RM mínimo do wiring (`planner_wiring.go:130`) só regista `agent.plan`; o `Delegator` medeia o spawn com `agent.spawn` (default `spawnToolID`), que o RM nega por default-deny (`agent.spawn` não registado). Provável segunda recusa, hoje **mascarada** pela primeira.

**Conflito a reconciliar (fonte):** o ticket AOS-389 afirma que, via `--goal (+fixture)`, "um nó condicional-papel **é spawnado** como sub-agente real". A execução acima mostra o oposto — o spawn de **qualquer** papel é recusado (depth) antes de qualquer avaliação de condição. A premissa fail-open de AOS-389 pode não ser alcançável pelo caminho `--goal` com o wiring actual; reconciliar o cenário medido de AOS-389 contra esta evidência.

### Objectivo
Tornar o caminho `--goal` do `aos-orq` capaz de materializar um plano **multi-nó com expansão** — nós-folha **E** papéis-que-expandem via `Delegator.Spawn` (AOS-026), como o AC 3 de AOS-388 exige — em vez de recusar fail-closed todo o spawn de papel.

### Critérios de Aceitação
- [x] A `RoleSpawn` (ou o adaptador `NewDelegatorSpawner`) propaga uma profundidade **coerente com a autoritativa** do token do run, de modo que o `Delegator.Spawn` não recuse por `ErrDepthMismatch`. A subdeclaração continua recusada (não enfraquecer a guarda anti-`max_depth`).
- [x] O RM composto no `--goal` (`planner_wiring.go`) admite genuinamente o `agent.spawn` (registo ou `WithSpawnCapability`), de modo que a mediação do spawn não seja default-deny; a mediação continua **obrigatória** (não se contorna o RM).
- [x] **Teste de composição real** (Delegator real, não `fakeSpawner`) que reproduz o cenário medido: um plano com `analise depends_on:[recolha]` materializa com `recolha` a **spawnar** (`subagent.spawned`, NHI cunhada, reserva) e `analise` como folha. Falha-antes: sem o fix, o teste apanha `ErrDepthMismatch`.
- [x] Não-regressão: o e2e `TestAOS388_GoalPipelineGovernadoPontoAPonto` (duas folhas independentes) continua verde, E ganha um irmão que exercita o ramo `SpawnRole` (o gap de cobertura §2.2 da auditoria).
- [x] O AC 3 de AOS-388 deixa de estar parcialmente-cumprido no eixo "papéis-que-expandem".

### Resolução (2026-09-10)
**FECHADO.** O fix exigiu **quatro** elementos, não dois — os dois diagnosticados eram necessários mas **não suficientes**, e os outros dois só apareceram por **execução do binário** (não por leitura):

1. **Profundidade** — `planmaterialize/adapters.go`: o `delegatorSpawner` passa a declarar `SpawnRequest.Depth` a partir da cadeia do token do pai, via a nova `orchestrator.ChainDepth` (wrapper exportado de `parentChainDepth`). Sem isto, `Depth=0 < autoritativa=1` ⇒ `ErrDepthMismatch`. A guarda **mantém-se**: o Delegator recomputa a autoritativa e continua a recusar quem declarar menos.
2. **`agent.spawn`** — `cmd/aos-orq/planner_wiring.go`: o RM mínimo passa a registar `agent.spawn` (mediação obrigatória, não contornada).
3. **Classe `worker`** *(descoberto por execução)* — o emissor efémero configura a classe com que o Materializer cunha a NHI filha (`childClass` default `worker`); sem ela, `E_UNKNOWN_CLASS`.
4. **Autoridade sobre tools** *(descoberto por execução)* — o token do run e as classes `coordinator`/`worker` passam a carregar a UNIÃO das capabilities coarse do snapshot pinado (`cap:tool:*`, via `toolCapabilities`/`DefaultCapabilityMapper`); sem isto, `IssueChild` recusa porque `Authority ⊄ folha-do-pai`. O clamp **por-nó** (`authorityForNode`) mantém cada filho restrito às suas próprias tools.

**Evidência:** execução real do binário com fixture de expansão (`analise depends_on:[recolha]`) ⇒ `spawn: no=recolha` + `materializado: nos=2` (recolha=role, analise=leaf), exit 0. `TestAOS393_GoalExpansaoSpawnaPapel` (`-race`) e as suites de `orchestrator`/`planmaterialize`/`cmd/aos-orq` verdes; `layer-lint` e `lint` verdes.

**Nota de segurança:** o elemento 4 alarga a autoridade do token do run ao catálogo pinado — aceitável neste caminho **NÃO-PRODUÇÃO** (fixture; T2-B pendente) e limitado pelo snapshot, com clamp por-nó preservado. Recomenda-se `security-review` antes de o caminho `--goal` ser promovido a produção.

### Handoff para Claude Code
```text
Corrige o ramo papéis-que-expandem do --goal do aos-orq (AOS-393, EPIC-19).
- Raiz: planmaterialize.RoleSpawn não carrega profundidade -> Delegator.Spawn recusa ErrDepthMismatch (declarada=0 < autoritativa=1).
- Latente: o RM do planner_wiring.go só regista agent.plan; agent.spawn fica default-deny.
- Propaga a profundidade autoritativa e admite agent.spawn no RM composto, SEM enfraquecer as guardas (anti-subdeclaração de depth + mediação obrigatória).
- Teste de composição REAL (Delegator real, não fakeSpawner) com um nó de expansão; falha-antes = ErrDepthMismatch. Mantém o e2e das duas folhas verde.
- Reconcilia a premissa de AOS-389 (papel "spawnado" via --goal) contra a evidência de execução deste ticket.
- Não toques em cmd/aos (guard boundary_orq_sch_test.go verde). Regenera a RTM. PR com o template §7 dos Standards.
```

---

## AOS-400 — O prompt de decomposição declara o schema do `PlanDocument` que o decode exige

<!-- rtm: adrs-mencionados -->
<!-- Os ADR-009 (cache-estabilidade do prompt) e ADR-012 (mutação governada do prompt) citados
     neste bloco são MENÇÃO — restrições que o ticket respeita — não implementação. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-241 (prompt SemVer, golden-sets e eval-gate), AOS-273 (precedente: a regra do `plan_version`), AOS-391 (decomposição pelo Model Gateway) |
| Bloqueia | AOS-391 (critério «`--goal` sem fixture produz um `PlanDocument`», que não se cumpre com o modelo vivo) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/control-plane/orchestrator/plannerprompt/artifact.go` (`decompositionTemplateV1`, `Current`), `packages/control-plane/orchestrator/plan/plandocument.go` (`Decode`, `validateShape`), `packages/control-plane/orchestrator/decompose/decompose.go` (`renderUser`, `extractJSON`), `packages/control-plane/orchestrator/plannerprompt/prompt.go` (`ValidatePromptMutation`) |

### Contexto

Medido em produção a 2026-09-16, na validação do AOS-395. O `aos-orq` compilado do `3a5aaa6` correu duas vezes contra o litellm de produção (`gpt-4o-mini`) com `serve --goal`. Nas duas, o litellm respondeu 200 às três tentativas e o planeador recusou os três planos:

```
aos-orq: decomposição do objectivo: planner: decomposição falhou em todas as tentativas:
  decompose: documento invalido: plan: objective de topo em falta
```

Nada foi materializado (fail-closed correcto). A decomposição por LLM vivo nunca tinha produzido um plano aceite: os testes do AOS-388/391/395 usam fixtures ou upstreams falsos que já devolvem o schema certo.

**Causa.** O template `decompositionTemplateV1` (prompt 1.1.0) exige «UM PlanDocument JSON de schema FECHADO (sem campos extra)», mas nunca diz quais são os campos. Nomeia só os campos por nó `node_id`, `role`, `objective` e `depends_on`, mais `planner_meta` e `plan_version`. O `plan.Decode` recusa campos desconhecidos (`DisallowUnknownFields`) e o `validateShape` exige:
- no topo: `plan_version`, `objective`, `planner_meta` completo e `nodes` não vazio;
- em cada nó: `node_id`, `role` e `objective` não vazios, `risk_class` num valor válido, e cada referência de ferramenta com `name`, `version` e `digest`.

O template não menciona o `objective` de topo, o `budget_total`, o nome da lista `nodes`, nem os campos de nó `tools`, `budget_estimate` e `risk_class`. O modelo não tem como adivinhar um schema fechado que não vê. O primeiro campo em falta é o `objective` de topo. É a mesma classe de lacuna que o AOS-273 fechou para o `plan_version`, que o template também não nomeava: um prompt é um pedido e o validador impõe, mas o pedido tem de conter o contrato.

### Objectivo

Com o modelo de produção, `serve --goal` sem fixture produz um `PlanDocument` que passa o `plan.Decode` e o validador AOS-231, sem mudar o contrato do schema nem enfraquecer a validação.

### Critérios de Aceitação

- [x] O template declara o schema completo do `PlanDocument`: os campos de topo e de nó, quais são obrigatórios, a forma das referências de ferramenta e dos orçamentos, os valores de `risk_class`, e que campos fora do schema são recusados. O texto continua estático (cache-estável, ADR-009); o conteúdo variável continua no `renderUser`. *(`plannerprompt/artifact.go`: bloco SCHEMA com o topo, o nó, os tipos aninhados entre chavetas, os tectos de cardinalidade, o predicado por subject e todos os valores fechados; FORMA MINIMA com placeholders `<...>`, uma ferramenta e orçamentos positivos. **Acrescento à discovery (revisão adversarial)**: mostrar as extensões sem as regras do validador que as acompanham seria ensinar planos que o AOS-231 recusa, e o planeador só repete a tentativa quando o decode falha — uma recusa do validador acaba o run. As regras 7 a 9 dizem essas regras (aresta por um só canal, `consumes` sobre aresta de entrada com o mesmo `type`, `verdict` só de um `role: verifier`), e a 10 pede orçamentos realistas: um nó a zero é admitido sem reserva e um papel a zero não consegue delegar, porque a fatia do spawn tem de ser positiva. O template continua `const`.)*
- [x] A mudança é um bump governado por `ValidatePromptMutation` (ADR-012), com a classe SemVer justificada (MINOR se só acrescenta o contrato, como no AOS-273) e o `Current` actualizado. *(`Current` = 1.2.0. MINOR: as regras 1 a 6 ficam iguais byte a byte, o schema não muda e o texto não impõe nada que o decode e o validador não impusessem já. `TestAOS400_Mutacao110Para120PassaOGateADR012` é a primeira chamada do gate sobre a mutação real: o template 1.1.0 publicado fica em `testdata/prompt-1.1.0.txt`, preso pelo SHA-256 `25c9732a…`, e o teste confere também o MINOR e as regras intactas. O gate em si não classifica a classe SemVer — aceita qualquer versão estritamente maior —, por isso a classe é verificada no teste.)*
- [x] Os golden-sets e o eval-gate do prompt (AOS-241) ficam verdes com a nova versão, e há um caso que falha com o template 1.1.0: um documento sem `objective` de topo é o que um modelo produz sem o schema. *(`scripts/ci/evalgate.sh` verde: planeador com segurança 12/12 e qualidade 8/10, golden-set 1.2.0 inalterado. O eval-gate avalia documentos já descodificados e não lê o template, pelo que não consegue ver esta lacuna: o caso que falha com o 1.1.0 vive no teste do template. `TestTemplateDeclaraOSchemaQueODecodeExige` mede o 1.1.0 e exige as faltas `topo.objective*`, `topo.nodes*`, `topo.budget_total` e `no.objective*`; `TestAOS400_SemOObjectiveDeTopoAFaltaEExactamenteEssa` isola-a, retirando do template corrente só a linha do `objective` de topo e exigindo que a única falta seja essa. `TestAOS400_DocumentoDaProducaoERecusadoSemObjectiveDeTopo` documenta o lado do decode.)*
- [x] Um teste impede a regressão da lacuna: cada campo obrigatório de `validateShape` aparece nomeado no template. Um campo novo obrigatório no `PlanDocument` sem menção no prompt faz o teste falhar. *(`TestTemplateDeclaraOSchemaQueODecodeExige`: os nomes saem das tags JSON dos tipos de `plan`; a obrigatoriedade sai do próprio `plan.Decode`, retirando cada campo de um documento válido; cada campo procura-se na linha onde o template o declara (topo, nó, ou as chavetas do campo-pai), com `*` se e só se o decode o exige; um campo novo que o documento de teste não use faz o teste falhar. `TestAOS400_FormaMinimaPassaODecodeEOValidador` prova que a forma mínima passa o decode e, preenchida, o validador. **FALHA-ANTES MEDIDA por mutação** no template: sem a linha do `objective` de topo, com `name` sem `*` em `outputs`, sem `"gray"` em `risk_class`, sem `"verdict"` na linha de `type`, e sem o `objective` na forma mínima — cada uma falha com a falta exacta. **Limites declarados**: a obrigatoriedade dos campos do predicado depende do subject e fica fixa no teste; os valores fechados são uma lista do teste tirada das constantes de `plan`, pelo que um valor novo não é detectado sozinho.)*
- [x] Evidência de sistema: um run avulso em produção (a forma registada no AOS-395) produz `decomposto: objectivo -> plano de N nos` com o modelo real, e o selo do planeador fica no WORM como antes. Se o modelo continuar a falhar por outra forma, a causa fica registada neste ticket. *(2026-09-16, run `run-aos400-prod-1789593240`: `aos-orq` linux compilado desta árvore (template 1.2.0, fingerprint `07c2ae7b…`), contentor efémero na rede `aos_default`, litellm de produção (`gpt-4o-mini`), mesmo objectivo e snapshot dos runs do AOS-395. Saída: `decomposto: objectivo -> plano de 2 nos (tentativas=1, planner_nhi=agent:planner)`, `materializado: … nos=2` (`n1.read_config` papel com `cap:tool:fs.read`, `n2.summarize_config` folha), `despacho: papel n1.read_config spawnado`, exit 0. O WORM relido tem um selo `allow` com `RunID` do run e `StepID=planstep:decompose:1`, cadeia íntegra. Com o 1.1.0, o mesmo objectivo falhou 3/3 em dois runs. Pasta temporária apagada.)*
- [x] O critério do AOS-391 «`--goal` sem fixture produz um `PlanDocument`» volta a `[x]` com esta evidência.

### Estado

**IMPLEMENTADO (2026-09-16).** O prompt de decomposição passa a 1.2.0 e declara o schema, as regras de grafo do validador que acompanham as extensões e orçamentos realistas; o modelo de produção decompôs à primeira tentativa um objectivo que falhava 3/3 com o 1.1.0. Verificado: suites `-race` verdes no orchestrator e em `cmd/aos-orq`; `evalgate`, `build`, `lint` e `layer-lint` verdes; falha-antes medida por mutação em cinco pontos do template. Revisão adversarial independente: nenhum crítico ou alto; os dois médios (regras de grafo ausentes, forma mínima com orçamentos a zero) e os baixos foram corrigidos, excepto os limites declarados no critério do teste. **Riscos que ficam fora deste ticket**: o catálogo que o modelo vê (`renderCapabilities`) não marca ferramentas inadmissíveis nem deprecadas, e o modelo não conhece a folga de orçamento do run — um plano pode ainda ser recusado por essas razões. Uma prova com um só objectivo não mede a taxa de sucesso do modelo; isso é o eval-gate com modelo vivo, que não existe.

---

## AOS-408 — O gate de aprovação de plano fica composto no `aos-orq`: um plano de risco espera decisão humana antes de materializar

<!-- rtm: adrs-mencionados -->
<!-- ADR-005 (o plano é dados; o documento cru não vive no log de eventos), ADR-016 §1 (a
     assinatura do humano é produzida FORA do processo que a verifica) e ADR-019 (fronteiras de
     camada; o composition root é que pode cruzar governance × orchestrator) são MENÇÃO —
     restrições que este ticket respeita — não implementação. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-236 (contrato do gate e do cartão), AOS-237 (materialização consome `plan.approved`), AOS-390 (despacho governado, de onde vem o `CardOracle`), AOS-388/AOS-391 (decomposição viva no `aos-orq`) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma / Responsável de Segurança |
| Documentos de referência | `packages/control-plane/governance/plan-approval/{gate.go,ports.go,plancard.go,triage.go}`, `packages/cmd/aos-orq/{planner_wiring.go,dispatch_wiring.go,main.go,snapshot.go}`, `packages/control-plane/orchestrator/{plan/plandocument.go,plan/payload.go,planvalidate/resources.go}`, `packages/control-plane/runlifecycle/emitters.go`, `packages/control-plane/governance/hitl/{channel.go,approval.go,nonce_store.go}` |

### Contexto

O gate de aprovação-de-plano (AOS-236) está **entregue como contrato e ausente como caminho**. O
`plan-approval` tem a porta, o cartão `aos.plan.card.v1` 1.1.0, a triagem por risco e o
dual-control; o único consumidor é o `aos-demo`, que constrói o `planapproval.Plan` **à mão**
(`packages/cmd/aos-demo/main.go:235-255`) com um revisor de demonstração. É o residual que o
DEF-274 nomeia: «o mapeamento `PlanDocument → planapproval.Plan` vive a jusante e NÃO existe em
produção».

No `aos-orq` — o binário que decompõe e materializa de verdade desde a v0.1.20 — a sequência é
`decompose → planvalidate.Validate → materializar → despachar`, **sem gate**. Duas consequências
medidas na leitura do código:

1. **Nada consome `plan.approved`.** O catálogo `aos.planner.v1` declara `plan.approved` e
   `plan.rejected` e o `aos-orq` emite `plan.materialized` sem que exista decisão nenhuma no
   stream. A DoD do AOS-237 («consome `plan.approved`; emite `plan.materialized`») está meia.
2. **O despacho mente por omissão.** `cardsFailClosed` devolve `false`
   (`packages/cmd/aos-orq/dispatch_wiring.go:95-101`) e o `needsCard` do `plandispatch` deriva de
   `Node.RiskClass`, que é o rótulo **advisory do LLM** (`plandispatch/dispatch.go:546`). Um plano
   que declare `"risk_class":"safe"` sobre uma tool `irreversible` no snapshot pinado despacha
   **sem cartão**: o piso de risco autoritativo é o do `planvalidate` (`elevateOnly`), e ninguém o
   consulta no despacho.

Havia ainda um defeito de governação a montante: DEF-274 e DEF-275 tinham como **eixo** o AOS-238,
que está **FECHADO** — um deferimento cujo gatilho aponta para um ticket fechado não tem quem o
reavalie, que é exactamente o que o §1 do registo existe para impedir. O AOS-390 tinha «corrigir o
eixo destes dois» na DoD e fechou sem o cumprir. Este ticket corrige o eixo para si.

### Objectivo

No `aos-orq`, um plano cujo cartão traga risco (`danger`) ou lacuna de capability (`gap`) **não
materializa nem despacha** sem uma decisão humana assinada, durável e verificável; os restantes
planos continuam a passar sem atrito. O veredicto passa a ser a fonte do `CardOracle` do despacho,
substituindo o `cardsFailClosed`.

### Decisões do dono (2026-09-17)

- **Aprovação ASSÍNCRONA.** O plano fica pendente como FACTO no stream do plano
  (`plan.proposed` + `plan.validated`, sem decisão terminal); a decisão chega por fora e a
  materialização continua nessa passagem. Não se bloqueia o processo à espera de um humano, e a
  ausência de decisão **não** é uma recusa.
- **Âmbito `danger` ou `gap`.** Um plano `safe`/`gray` auto-aprova pelo nível de autonomia; o
  atrito humano é só para risco resolvido `danger` ou lacuna de capability.

### Critérios de Aceitação

- [x] **Mapeador de produção.** `plan.PlanDocument` → `planapproval.Plan` no composition root do
      `aos-orq`, com as extensões do DEF-274 (`Role`, `ConditionalOn`, `Outputs`, `Consumes`) e o
      taint **efectivo** do output (`plan.Node.EffectiveOutputTaint`), não o declarado. A `Class` de
      cada nó é o risco **resolvido** pelo `planvalidate` (o advisory do LLM só eleva), nunca o
      `risk_class` cru. O `Preview` do cartão não transporta texto livre do modelo.
      **A amarra do snapshot é parte da regra, e tem duas camadas.** O rótulo: o snapshot tem de ser
      o que o documento DECLARA (`planner_meta.capabilities_hash`). E o CONTEÚDO: o `hash` de um
      snapshot é um rótulo que o ficheiro declara sobre si mesmo — copiá-lo para um catálogo com
      eixos benignos passava a primeira camada (a 2.ª revisão reproduziu-o). Por isso o digest dos
      eixos de cada tool é SELADO no `plan.validated` quando o plano fica pendente, e a decisão e a
      materialização têm de apresentar o mesmo conteúdo.
      *(`packages/cmd/aos-orq/plan_gate_wiring.go`. O risco resolvido entra por
      `planvalidate.ResolveRisks`, ponto de entrada novo que isola a regra 6: o `ValidateResources`
      rejeitava o plano inteiro sem `Pricer`, e o `aos-orq` não compõe tabela de preços — sem isto o
      binário cairia no rótulo advisory, que é exactamente o que a regra 6 existe para não fazer.
      Cinco testes em `aos408_mapeador_test.go`: o nó que se declara `safe` sobre uma tool
      irreversível chega ao cartão como `danger`; um `summary` declarado `trusted` por um
      não-verificador fica `untrusted` e só sobe com produtor verificador E forma fechada; o
      `objective` do modelo não aparece no cartão; as condições e arestas de dados atravessam.)*
- [x] **Risco autoritativo no despacho, e o oráculo consultado de facto.** O `needsCard` passa a
      ser a projecção do cartão (`danger` ou `gap`) derivada do risco resolvido — antes vinha do
      `risk_class` ADVISORY do LLM, pelo que um plano que se declarasse `safe` sobre uma tool
      irreversível não exigia cartão nenhum. *(Duas metades inertes a anularem-se deram lugar a duas
      ligadas: o `cardsFailClosed` — que recusava sempre e cujo comentário dizia «não é consultada em
      prática» — deu lugar ao `PlanDecisionReader`. **Correcção vinda da revisão:** trocar as duas
      metades não bastava. O despacho só corre pelo caminho do `--goal`, e esse caminho só chegava
      ao despacho quando NENHUM nó exigia cartão — o oráculo continuava provadamente nunca
      consultado, e um plano aprovado não tinha caminho para correr. O `gatearPlano` passou a
      reconhecer a decisão já tomada e a prosseguir: repetir a mesma invocação depois da aprovação
      materializa E despacha, e é no despacho que o oráculo autoriza o nó `danger` — com o MESMO
      predicado do gate (decisão humana, deste hash), e não o `Approved()` sozinho que a 1.ª versão
      usava (`TestAOS408_DepoisDaAprovacaoODespachoConsultaOOraculo`, que afirma o ARRANQUE do nó
      `danger` — `nos_despachados=2` —; **falha-antes por mutação**: com o oráculo a recusar, o nó de
      risco fica parado e `nos_despachados=1`. A 1.ª versão do teste usava um plano em que o nó de
      risco dependia do outro e nunca chegava ao oráculo — apanhado no ensaio da validação de
      produção).) **Âmbito real:** isto vale
      quando a repetição produz o MESMO documento — uma decomposição determinística. Com o modelo
      vivo, a segunda decomposição produz outro organigrama (outro hash) e fica pendente sem poder
      ser decidida (o plano já tem decisão terminal); o plano aprovado materializa por
      `--plan-doc`, mas esse caminho nunca despachou — limitação anterior a este ticket, que fica
      como resíduo com eixo próprio.*
- [x] **Gate composto.** `planapproval.NewPlanGate` no caminho do `--goal`, entre a validação e a
      materialização, com revisão forçada dos nós de risco. Um plano `danger` não materializa: o
      processo apensa os factos do pendente, larga a posse e sai com código próprio (**6**); um
      plano sem risco auto-aprova pelo nível de autonomia e materializa como antes.
      *(PAR FALHA-ANTES pelo processo real, `aos408_gate_plano_test.go`: o MESMO pipeline com o
      plano de risco sai 6, não imprime `materializado:` e deixa o grafo durável a `nos=0`; o plano
      sem risco sai 0 e despacha os 2 nós. Quem fica pendente LARGA o lease — um pendente é o fim do
      trabalho deste processo, e retê-lo bloquearia o próprio `decide`, que escreve no stream do
      plano: o gate a travar-se a si mesmo.)*
- [x] **O `gap` força humano.** A auto-aprovação por nível de autonomia deixa de ignorar
      `CapabilityGap`: a `autonomy.Oversight` é função de (nível, classe) e não conhece o gap, pelo
      que um plano com lacuna auto-aprovava a L5 desde que a classe o permitisse — o contrário do
      que o contrato do campo declara e do que a triagem do cartão já fazia.
      *(`temLacunaDeCapacidade` em `ports.go`, guarda em `gate.go`;
      `TestAOS408_LacunaDeCapacidadeNaoAutoAprova` com contraprova (sem gap, auto-aprova e o canal
      nem é chamado) e **FALHA-ANTES por mutação**: sem a guarda, o plano com gap auto-aprova.
      DECLARADO: no `aos-orq` nada abre um gap hoje, pelo que esta metade do âmbito é contrato e não
      facto — o banner diz-lo.)*
- [x] **Pendente durável, derivado do log.** O pendente é `plan.validated` sem decisão terminal,
      dentro do prazo, lido pelo `runlifecycle.PlanDecisionReader` (irmão do `GateReader`: relê o
      stream uma vez e fixa um retrato imutável). Os três factos que faltavam ter chamador de
      produção — `plan.proposed`, `plan.validated`, `plan.approved`/`rejected` — passam pelo
      `PlanRecorder`, logo pelo appender FENCED. *(Sem estes factos, «à espera do humano» era a
      AUSÊNCIA de factos, indistinguível de «nunca foi proposto», e um restart perdia o caso. A
      precedência terminal é explícita porque o step id é `planstep:decision:<decisão>`, o que
      deixa um `approved` e um `rejected` coexistirem no mesmo stream.)*
- [x] **Decisão assinada fora do processo.** `aos-issuer plan-approve-sign` produz a decisão
      (ed25519, chave privada lida de ficheiro montado, nunca vista pelo verificador — ADR-016 §1);
      `aos-orq decide` verifica-a pelo `hitl.Channel` contra chaves **pinadas** com autoridade por
      classe, no MESMO formato de `AOS_APPROVERS_FILE` do nó. `aos-orq plans` imprime o `request_id`
      a assinar. **A pinagem é por ficheiro escolhido pelo operador** (`--approvers`, ou
      `AOS_APPROVERS_FILE` do compose): protege contra quem não tem chave, não contra quem controla
      esse ficheiro — ver «Fronteira de confiança». *(Seis testes pelo processo real em
      `aos408_decide_test.go`: aprovação legítima
      aprova e só então materializa; assinatura de chave não-pinada recusa; aprovador com
      `approve:gray` não aprova `danger`; replay da mesma aprovação recusa (nonce por CAS durável);
      documento adulterado recusa por divergência de hash; recusa assinada fecha o plano. Um bug
      real que esta prova apanhou: com `issued_at` em RFC3339 de segundos, a assinatura — que cobre
      o instante em `UnixNano` — deixava de verificar; o wire passou a RFC3339Nano nos três sítios.)*
      **Quatro furos ALTA que a revisão adversarial reproduziu com os binários reais, e as
      correcções:** (1) o `--snapshot` do `decide` era arbitrário — com um snapshot benigno o risco
      resolvia-se `safe`, a auto-aprovação por nível SALTAVA o canal e uma assinatura de lixo
      aprovava um plano `danger`: passou a exigir-se o snapshot declarado pelo documento e a
      cerimónia corre a L1, onde o canal é sempre chamado; (2) a âncora era o hash do primeiro
      `plan.validated`, pelo que a auto-aprovação de um plano inócuo no mesmo `plan_id` autorizava o
      perigoso: o predicado passou a exigir decisão APROVADA, do MESMO hash e com referência
      `hitl:`; (3) o ramo de recusa gravava `plan.rejected` antes de qualquer verificação — quem não
      tinha chave fechava um plano pendente e o log culpava um aprovador pinado: um facto terminal só
      se escreve com aprovador VERIFICADO (o `plan-approval` passou a levar o aprovador também na
      recusa, que antes descartava), e o nonce só é consumido DEPOIS da verificação, senão uma forja
      queimava o nonce de uma decisão legítima; (4) um nó `gray` ao lado do `danger` tornava o plano
      INAPROVÁVEL (a revisão forçada cobre `>=gray` e o revisor declarava só `danger|gap`) — e a
      primeira tentativa legítima fechava-o: o revisor passou a declarar os nós que o CARTÃO força.
      Cada um tem teste em `aos408_furos_test.go`.
- [x] **TTL imposto na decisão, e a expiração DERIVADA.** Um pendente fora do prazo é recusado no
      momento da decisão, sem varredor — a disciplina do `handleApprove` do nó. A expiração NÃO
      escreve facto: é derivada do instante do `plan.validated` e do prazo, como o próprio pendente.
      *(A 1.ª versão gravava `plan.rejected` ao expirar, e o teste chamava-se «...FechaOCaso»: a
      2.ª revisão mostrou que isso ERA o ataque — `decide --ttl 1ns` com ficheiros que nem existiam
      fechava qualquer plano pendente para sempre. O prazo passou a só poder ser ENCURTADO por quem
      decide, nunca alargado nem desligado (`--ttl` ≤ 24 h e positivo), e a decisão assinada tem
      janela de frescura. `TestAOS408_PrazoExpiradoRecusaSemFecharOPlano` (recusa, o plano continua
      pendente e a decisão legítima aprova a seguir), `TestAOS408_PrazoNaoPodeSerAlargadoPorQuemDecide`,
      `TestAOS408_PrazoNaoPositivoERecusado`, `TestAOS408_ExpiracaoEDerivadaENaoEscrita`.)*
- [x] **Postura declarada no arranque.** `bannerDoGateDePlano` declara o gate composto e o nível,
      e diz em voz alta as três limitações que, caladas, seriam ilusões de governação: o `gap` é
      contrato e não facto neste binário; o 4-eyes é **fraco** aqui (o solicitante é a NHI do run,
      logo qualquer humano o satisfaz — a garantia é «um humano com autoridade pinada», não «dois
      humanos»); e o nível de autonomia é um default do processo, não um nível durável por par
      (agente, domínio) como no nó.
- [x] **Prova falsificável.** 33 testes: 5 do mapeador, 2 do par falha-antes, 9 da cerimónia
      (incluindo o prazo), 10 dos furos das duas revisões — todos estes pelo processo real —, 5 do
      predicado de decisão no `runlifecycle` e 2 do gap no `plan-approval`. Falha-antes por mutação no gap e na condição `hitl:` do predicado.
      O contorno do `--plan-doc` fica fechado **contra o plano e contra quem decide**
      (`TestAOS408_PlanDocSemDecisaoNaoContornaOGate`,
      `TestAOS408_PlanDocComSnapshotBenignoNaoContorna`); NÃO contra um operador que escreve um
      catálogo com eixos benignos num run novo — ver «Fronteira de confiança».
- [x] **Governação.** O eixo de DEF-274/275 passou de AOS-238 (**fechado** — um deferimento cujo
      gatilho aponta para um ticket fechado não tem quem o reavalie) para AOS-408; o DEF-274 passa a
      **FECHADO-RESIDUAL** (o estado terminal do registo — a linha fica como contraste, com os
      residuais nomeados) e o critério «Card oracle» do AOS-390 fica marcado com evidência. O
      DEF-275 continua ABERTO, agora com eixo válido.

### Fora de âmbito (declarado)

- **API HTTP no `aos-orq`.** O binário não tem superfície de rede nenhuma (o único `net/http` é
  cliente do Model Gateway); abrir uma exige barreiras próprias (mTLS, token-bucket, OIDC) e é
  outro ticket. A cerimónia aqui é CLI + ficheiro assinado, que é a disciplina mais forte
  (ADR-016 §1: quem verifica não assina), não a mais fraca.
- **`Pricer`/tabela de preços** no `aos-orq` (regra 5 do AOS-232) — residual já declarado no
  AOS-391; por isso o risco resolvido entra por um ponto de entrada só-risco, sem orçamento.
- **DEF-275** (4.º eixo de mutação no `IsEffectTool`) fica ABERTO, com eixo no **AOS-409** — este
  ticket não o implementa, e apontá-lo para aqui repetiria, ao fechar, o defeito que corrigiu (um
  eixo num ticket fechado). A premissa de bloqueio do registo já é falsa (há construção de
  `planvalidate.Capability` em produção); o eixo tem decisões próprias.

### Fronteira de confiança (declarada)

Este gate governa o **PLANO**: um organigrama de risco vindo do modelo não materializa nem despacha
sem uma decisão humana explícita, atribuível e amarrada àquele organigrama e àquele catálogo. É isso
que o dono pediu, e é isso que está provado — contra o documento (que não escolhe o seu risco),
contra quem decide sem chave (a assinatura é sempre verificada), contra a reutilização de decisões
(noutro hash, noutro catálogo, noutro run, noutro nonce) e contra o fecho de planos por quem não
decidiu.

**NÃO é uma fronteira de segurança contra quem opera o CLI no servidor.** O Event Store não assina
eventos (quem tem escrita no WAL pode apensar um `plan.approved`), o snapshot é um ficheiro pinado e
NÃO assinado (quem o escreve escolhe os eixos de risco de um run novo), e o registo de aprovadores é
um ficheiro que o operador escolhe. No deploy, o `aos-orq` corre no contentor que detém o WAL: o
acesso ao CLI é o acesso ao store. Endurecer isso exige snapshot assinado, decisões assinadas no log
e verificadas no consumo, e pinagem fora do alcance de quem invoca — trabalho de outro ticket, que
não se finge feito aqui. Declarado também no banner.
- **Dual-control por-efeito** (dois humanos distintos) e **edição de plano pela CLI**: as portas
  existem, a correspondência assinatura↔chamada e a UX são trabalho separado.

### Estado

**IMPLEMENTADO** (2026-09-17). Verificado: suites `cmd/aos-orq`, `cmd/aos-issuer`,
`governance/plan-approval`, `runlifecycle` e `orchestrator` verdes; `build`, `lint`, `layer-lint`,
`event-catalog` e os gates documentais verdes; falha-antes medida por mutação no gap e pelo par de
processos no gate.

**Verificado em produção a 2026-09-18** (`v0.1.22`, imagem `sha256:35ea82a0…`), com o plano do
caso adversarial: o nó `publicacao` usa uma tool irreversível com egress externo e DECLARA-SE
`safe`. O snapshot, o plano e uma chave de aprovador **só de validação** foram postos em
`/opt/aos/orq/` e retirados no fim (a chave privada nunca saiu da máquina do aprovador).

| Passo | Saída em produção |
|---|---|
| `serve --goal` | `EXIT=6`, `pendente de aprovacao humana … 1 no(s) de risco: publicacao`, `plan_hash=sha256:b14d2336…`, nada materializado |
| `decide` | `EXIT=0`, `decisao APROVADA por human:validacao-aos408` — assinatura feita fora do servidor e verificada contra a chave pinada; o hash é o do ensaio local, o que confirma o `request_id` determinístico |
| `serve --goal` repetido | `EXIT=0`, `gate de plano: APROVADO por humano`, `folha publicacao a arrancar`, `nos_despachados=2` — o nó de risco só arrancou porque o oráculo de cartão o autorizou |
| `plans` / `inspect` (leitura) | `estado=DECIDIDO decisao=approved em=2026-09-18T22:32:34Z` com o mesmo hash; `nos=2 ordem=leitura,publicacao` |

A primeira tentativa falhou por um defeito dos COMANDOS, não do gate: o PowerShell 5.1 estraga as
aspas duplas dentro de `ssh '…'`, o `--goal "dois termos"` chegou partido e o parser de flags parou.
Nesse estado tudo recusou — `--goal exige --snapshot`, e o `decide` recusou por não haver
`plan.validated` no log — e nada foi materializado.

O ensaio local desta validação apanhou ainda um defeito do teste do oráculo: com o nó de risco
dependente do outro, ele nunca chegava ao oráculo numa passagem de despacho. O teste e o plano de
validação passaram a ter o nó de risco sem dependências, e a mutação do oráculo passou a ser
detectada.

**Duas revisões adversariais independentes**, ambas com reprodução nos binários reais. A 1.ª
encontrou quatro furos ALTA (snapshot escolhido por quem decide; âncora no `plan.validated`; recusa
gravada antes de verificar; nó `gray` a tornar o plano inaprovável) e o oráculo ainda nunca
consultado. A 2.ª mostrou que duas correcções só fechavam a variante exacta dos testes — o rótulo
`hash` do snapshot copia-se (fechado selando o CONTEÚDO), e a expiração gravada era a mesma arma que
a recusa (fechado derivando-a) — e encontrou a decisão de um run a servir outro pelo `--plan` do
`serve`, uma aprovação negada pelo canal gravada como recusa humana, e o oráculo com critério mais
fraco que o gate. Todos corrigidos e com teste.

**Resíduos declarados:** o `plan_id` da cerimónia DERIVA do run (`<run>-plan`) porque a ligação
run→plano não é um facto do log — um run cujo plano tenha outro id não é decidível por esta via; os
nós de `--nodes` entram no grafo antes do gate do `--plan-doc` (sem tools, sem efeito, mas entram);
o `Cleared` do oráculo é por PLANO e não por nó — um auditor vê «este
plano foi aprovado», não «este nó foi revisto»; não há dual-control por-efeito (dois humanos
distintos); o selo do canal HITL é in-memory neste binário (a decisão durável vive no Event Store) e
um WORM próprio para o gate fica para ticket separado — o não-repúdio da decisão fica, portanto,
na referência `hitl:<principal>` do log e NÃO na assinatura, que não é re-verificável depois; com o
modelo vivo, um plano aprovado não despachava (o `--plan-doc` nunca despachou) — fechado pelo
AOS-412; o nonce é consumido
antes de o facto da decisão ser escrito, pelo que uma escrita falhada obriga a reassinar; um
`plan_id` admite uma só decisão — re-planear exige um run novo; a regra Cedar `allow_http_post`
continua a exigir `region == "eu"`, resíduo herdado do AOS-407.

---

## AOS-409 — O `IsEffectTool` ganha o 4.º eixo — mutação — a partir de uma fonte de verdade que não seja o próprio plano

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-231 (validador e `IsEffectTool`), AOS-408 (o gate que consome o risco resolvido) |
| Bloqueia | — |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | `packages/control-plane/orchestrator/planvalidate/verifier.go` (`IsEffectTool`), `packages/cmd/aos-orq/snapshot.go` (`carregarSnapshot`) |

### Contexto

É o eixo do **DEF-275**. O `IsEffectTool` classifica uma tool como «com efeito» por egress ou
irreversibilidade, e ignora a **mutação**: uma tool que altera estado sem egress e reversível passa
por inócua, e um verificador pode pinná-la. O registo dava como bloqueio «não existe construção de
`planvalidate.Capability` fora de testes» — premissa que deixou de ser verdadeira: o `aos-orq`
constrói-as do snapshot pinado (`carregarSnapshot`). O eixo do DEF-275 apontava para o AOS-238
(fechado) e passou provisoriamente para o AOS-408, que não o implementa — este ticket existe para o
eixo apontar para quem o fará.

### Objectivo

Uma tool que muta estado conta como efeito na validação e no risco, com o dado a vir de uma fonte
que não seja o documento do plano.

### Critérios de Aceitação

- [ ] Decisão registada sobre a FONTE do eixo (campo do snapshot pinado vs. classificação do REG) e
      sobre a omissão para snapshots existentes — fail-closed (todo o catálogo passa a «mutador») ou
      transição declarada. Uma decisão fail-closed pode impedir planos de quem já corre.
- [ ] `IsEffectTool` com o 4.º eixo e teste sobre o CATÁLOGO, não sobre literal de teste.
- [ ] DEF-275 fecha com evidência.

### Estado

**POR FAZER.**

---

## AOS-412 — Com o modelo vivo, um plano de risco aprovado corre pelo `--plan-doc`

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-408 (o gate de aprovação de plano), AOS-390 (materialização admit-only, efeito no despacho) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos-orq/main.go` (`materializar`), `packages/cmd/aos-orq/planner_wiring.go` (`comporBaseDeExecucao`, `materializarEDespachar`), `packages/cmd/aos-orq/plan_gate_wiring.go` (`gatearPlano`) |

### Contexto

O AOS-408 compôs o gate e validou-o em produção (v0.1.22) com o planeador de fixture, que é
determinístico: repetir o `serve --goal` depois do `decide` re-decompõe no MESMO organigrama e o
plano aprovado corre. Com o **modelo vivo** não é assim — a segunda decomposição produz outro
organigrama (outro `plan_hash`), e o registo do AOS-408 declarava-o como resíduo: «com o modelo
vivo, um plano aprovado não despacha». Havia dois becos:

- o `--goal` repetido dizia «pendente» sobre um `plan_id` que JÁ tinha decisão terminal, e o
  `decide` recusava-o depois («ja tem decisao terminal») — um pendente sem saída;
- o `--plan-doc`, a via determinística, **parava na admissão**: materializava com um token de
  faz-de-conta (`"nhi:"+worker`), não despachava (os nós ficavam pendentes para sempre) e nem
  sequer corria a validação estrutural AOS-231, que só o `--goal` chamava.

Ou seja: o caso para que o gate existe — aprovar um organigrama de risco e vê-lo correr — não era
possível em produção com o planeador real.

### Objectivo

O `serve --plan-doc` percorre o mesmo caminho do `--goal`, menos a decomposição: validação
estrutural → o MESMO gate → base de execução real (identidade, RM, orçamento) → materializar →
**despachar**. E o `--goal` sobre um plano já decidido recusa em vez de mentir «pendente».

### Critérios de Aceitação

- [x] `serve --plan-doc <documento aprovado>` materializa E despacha o organigrama aprovado,
      incluindo o nó de risco. *(Evidência: `TestAOS412_ComModeloVivoOPlanoAprovadoCorrePeloPlanDoc`
      — aprova H1, simula a re-decomposição com um H2 diferente e corre H1 pelo `--plan-doc`:
      «APROVADO por humano», `folha publicacao a arrancar`, `nos_despachados=2`.)*
- [x] O `--goal` sobre um `plan_id` com decisão terminal para OUTRO organigrama sai com **7** e
      indica o caminho (`--plan-doc`), sem materializar. *(Evidência: o mesmo teste, passo 1.)*
- [x] O documento do `--plan-doc` é untrusted como o do modelo: passa pela validação AOS-231.
      *(Evidência: `TestAOS412_PlanDocValidaAEstrutura` — uma tool fora do snapshot é recusada
      com a regra AOS-231.)*
- [x] Um plano sem risco pelo `--plan-doc` passa pelo MESMO gate (auto-aprova, com os factos no
      log) e despacha. *(Evidência: `TestAOS412_PlanDocSemRiscoAutoAprovaEDespacha`.)*
- [x] Um só gate para as duas vias: `exigirDecisaoParaDocumento` (a verificação paralela do
      `--plan-doc`) sai; o `gatearPlano` lê a decisão ANTES de apensar factos.
- [x] Um segundo organigrama de risco sobre um plano JÁ pendente de outro sai com **7** e não
      reescreve o documento pendente (o `plan.validated` é de primeira-escrita e o `decide` ancora
      nele). *(Evidência: `TestAOS412_SegundoOrganigramaSobrePendenteRecusa`.)*
- [x] Reutilizar uma aprovação (ramo sem risco) exige o catálogo SELADO, como o ramo de risco.
      *(Evidência: `TestAOS412_AprovacaoReutilizadaExigeOSnapshotSelado`.)*
- [x] O runbook da cerimónia (`deploy/server/README.md`) executa o plano aprovado pelo `--plan-doc`.

**FALHA-ANTES:** os três testes, contra os ficheiros de produção da base (`main.go`,
`planner_wiring.go`, `plan_gate_wiring.go` repostos), falham pela razão que medem — o H2 saía **6**
(pendente) em vez de 7; o documento com tool desconhecida não era recusado pela AOS-231; o plano sem
risco não passava pelo gate nem despachava.

**Fixtures corrigidas.** Dois testes antigos usavam documentos que a regra AOS-231 recusa e que
passavam só porque o `--plan-doc` não validava: o do DEF-273 (um `verifier` com `plan_version` 1.0.0
e uma tool de efeito) e o do AOS-390 (um ramo condicional sobre o `verdict` de um nó que não é
verificador). Passaram a documentos admissíveis. O do DEF-273 prova agora as duas linhas pelo
processo real: a regra (V3) recusa o verificador com tool de efeito (`verifier_effect_tool`) e o
oráculo do snapshot continua composto para o verificador read-only. O clamp da materialização
(segunda linha) deixou de ser alcançável por esta via, porque a primeira apanha o documento antes;
a sua cobertura é a de unidade.

### Fora de âmbito

- O `plan_id` continua a admitir UMA decisão: re-planear depois de uma recusa exige um run novo
  (resíduo do AOS-408, inalterado).
- Os nós de `--nodes` continuam a entrar no grafo antes do gate (resíduo do AOS-408, inalterado).

### Estado

**FEITO.**

**Verificado em produção a 2026-09-19** (`v0.1.23`, imagem `sha256:b59db1fb…`), com o **modelo
vivo** (sem `--decompose-fixture`) no run `run-aos412-vivo-1`. O snapshot com uma tool `danger`
(`http.post`, irreversível, egress externo) e a chave pública de um aprovador **só de validação**
foram postos em `/opt/aos/orq/` e retirados no fim; a chave privada nunca saiu da máquina do
aprovador.

| Passo | Saída em produção |
|---|---|
| `serve --goal` | `EXIT=6`, `plan_hash=sha256:3c370e34…` (H1), `1 no(s) de risco: n3`, documento em `--plan-out` |
| `serve --goal` repetido, H1 pendente | `EXIT=7` — o modelo re-decompôs em `sha256:96617bd1…` e o `serve` recusou («ja esta pendente para o organigrama … decida o documento pendente … `serve --plan-doc`») em vez de um segundo pendente indecidível |
| `decide` | `EXIT=0`, `decisao APROVADA por human:validacao-aos408` sobre o H1 — o documento pendente não foi reescrito pelo passo anterior (senão o hash não batia) |
| `serve --goal` depois da decisão | `EXIT=7` — outro organigrama (`sha256:844511f7…`), recusado com a indicação do `--plan-doc` |
| `serve --plan-doc` | `EXIT=0`, `gate de plano: APROVADO por humano`, `materializado: … nos=3 oraculo=snapshot(sha256:snap-aos408-validacao)`, `despacho: papel n1 spawnado`, `nos_despachados=1` |
| `plans` / `inspect` (leitura) | `estado=DECIDIDO decisao=approved plan_hash=sha256:3c370e34…`; `nos=3 ordem=n1,n2,n3` |

O despacho parou no `n1` pela TOPOLOGIA que o modelo escolheu, não pelo gate: o documento aprovado
(lido do volume, só leitura) tem `n1` a ler, `n2` como `verifier` sobre o que o `n1` leu, e o `n3`
(`http.post`, `danger`) com `conditional_on: n2 verdict eq pass`. O `n3` espera, correctamente, pelo
veredicto — que só existe depois de os filhos correrem, fora de um `serve` de uma passagem. A
execução do nó de risco depois da aprovação ficou vista na validação do AOS-408 (fixture,
`nos_despachados=2`); com o modelo vivo, NÃO VERIFICADO nesta corrida.

**Observado, fora deste ticket:** a primeira decomposição viva produziu um plano que a regra AOS-231
recusou (`consumes_taint_authority`) e o `serve` saiu com `1` sem voltar a pedir ao modelo
(`tentativas=1`) — a recusa estrutural não realimenta o planeador. A segunda corrida decompôs num
plano admissível.

---

## AOS-413 — Os nós despachados de um organigrama executam até ao fim e o plano produz resultado

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-390 (despacho governado sob Tenure), AOS-412 (o plano aprovado corre pelo `--plan-doc`), ADR-018, ADR-024, ADR-027 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos-orq/dispatch_wiring.go` (`dispatchSink.Dispatch`), `packages/control-plane/orchestrator/graph.go` (`MarkRunning`), `packages/control-plane/orchestrator/plandispatch/ports.go`, `packages/control-plane/runlifecycle/emitters.go`, `packages/cmd/aos/api.go` (`POST /runs`) |

### Contexto

Os tickets AOS-400 a AOS-412 fecharam a cadeia do `aos-orq` até ao despacho: modelo vivo → plano →
validação → gate humano → materializar → despachar. A cadeia **acaba aí**. Despachar um nó é, hoje,
cunhar a NHI (se for papel) e `MarkRunning` — e nada executa o trabalho do nó nem o conclui. O próprio
código o diz: «Sem um executor a concluir nós, o ponto fixo alcança-se numa ou duas passagens».

Em produção (v0.1.23, run `run-aos412-vivo-1`) o organigrama aprovado despachou o `n1` (ler o
relatório) e parou: o `n1` fica `running` para sempre, o verificador `n2` nunca emite veredicto e o
nó de risco `n3` (`conditional_on: n2 verdict eq pass`) nunca publica. **Um objectivo entregue ao
`aos-orq` não produz resultado.** O caminho de um só run no nó `aos` executa tools na sandbox e
conclui (E2E de 2026-09-15); o multi-nó é o único que não chega ao fim.

O lado de LEITURA já existe e está composto — falta quem ESCREVA:

| Peça | Leitor (composto) | Produtor (em falta) |
|---|---|---|
| Dependência cumprida | `LifecycleView.State` sobre `task.node.state_changed` (`RebuildDAG` já honra um `To=complete`) | nenhuma transição `running→complete\|failed` no `GraphBuilder` — só existe `MarkRunning` |
| Veredicto de um `verifier` | `runlifecycle.ResultReader` sobre `plan.verdict_recorded` | `PlanRecorder.RecordVerdict` sem chamador de produção |
| Payload entre nós (`consumes`) | `PayloadResolver` sobre `plan.payload_published` | `PlanRecorder.RecordPayloadPublished` sem chamador de produção |
| Headroom | a porta diz que o liberta quem conclui o nó | o `boundedHeadroom` do `aos-orq` é em memória e nunca liberta |
| O trabalho do nó | — | **indefinido**: nenhum ciclo de modelo corre para um nó do plano (o `aos-orq` nunca chama `agentruntime.Run`) |

Não há deferimento registado para isto: a lacuna não estava declarada.

### Decisão a tomar primeiro (do dono)

Onde corre o trabalho de um nó. O ADR-018 faz do laço de serviço do nó `aos` a fonte única do ciclo
de vida de um run e proíbe-o de importar orquestrador/scheduler (`boundary_orq_sch_test.go`); o
ADR-024 põe a composição do despacho no `aos-orq serve`. Opções, com o que cada uma custa:

- **(A) Cada folha é um run no nó `aos`.** O `aos-orq` submete-a por `POST /runs` e acompanha-a por
  `GET /runs/{id}`; as tool calls ficam governadas pelo RM e pela sandbox que já estão em produção.
  Não mexe no ADR-018. Custa: um cliente HTTP autenticado no `aos-orq` (não existe), o corpo do
  `POST /runs` não leva as tools nem o `node_id` do plano (o nó tem de ficar restrito às tools
  pinadas do nó do plano, senão o clamp da materialização é decorativo), e a espera por um run
  remoto num binário que hoje é de uma passagem.
- **(B) O `aos-orq` corre o ciclo de modelo em processo** (`agentruntime` com o RM do próprio
  binário). Custa: um segundo sítio a executar tools, fora da sandbox e da cadeia PDP completa do nó
  — o RM do `aos-orq` é mínimo e sem PDP (fora de âmbito declarado no AOS-407).
- **(C) Emendar o ADR-018** para o nó `aos` hospedar o multi-nó. Custa: reabre a decisão que mantém
  uma só autoridade sobre o ciclo de vida.

A recomendação à partida é **(A)**, por reutilizar a execução governada que já corre em produção —
mas a escolha é do dono, e fica num ADR.

**Decidido (2026-09-19) — ADR-027:** opção **(A)**. O NHI do run é cunhado pelo operador com o
`aos-issuer` e montado em ficheiro (o nó continua a confiar num só emissor); o `POST /runs` ganha um
campo `tools` como lista-branca imposta pelo RM; a validade do NHI (45 min) é o tecto de um plano por
agora, com a renovação como resíduo declarado.

### Objectivo

Um organigrama aprovado executa até ao fim: cada folha faz o seu trabalho com as tools pinadas do seu
nó, conclui (`complete`/`failed`) de forma durável sob a posse do run, um `verifier` emite o
veredicto, o despacho avança para os dependentes e para os ramos condicionais, e o plano termina com
um resultado legível.

### Critérios de Aceitação

- [x] Decisão (A)/(B)/(C) registada num ADR, com o impacto no ADR-018/ADR-024. *(Evidência: ADR-027.)*
- [x] Transição durável `running→complete|failed` de um nó do plano, escrita só sob o lease
      (ADR-023), e `RebuildDAG` a reconstituí-la depois de um crash. *(Evidência:
      `GraphBuilder.MarkTerminal`; `TestAOS413_MarkTerminalFicaDuravelESobreviveAoReplay`,
      `…RecusaOQueNaoEConclusao` (ready→complete e killed recusados), `…RevertidoSeOAppendFalha`.)*
- [x] O trabalho de uma folha executa restrito às tools pinadas DO NÓ (as do `plan.materialized`),
      não às do run inteiro — com teste que prova que uma tool de outro nó é negada. *(Evidência:
      campo `tools` do `POST /runs` → `Goal.AllowedTools`, imposto antes da mediação; lista
      AUSENTE ⇒ sem restrição, PRESENTE e vazia ⇒ nenhuma tool (um nó sem tools pinadas não herda
      as do NHI do run); preservada pela retoma. `TestAOS413_ToolsDoPostRunsCortaAToolForaDaLista`
      pelo nó real (a call não chega ao RM, nas duas variantes), `TestAOS413_ListaBrancaVaziaNegaTudo`,
      `TestAOS413_RetomaDistingueListaVaziaDeAusente`; mutação no guarda e no mapeamento da API.)*
- [~] Um `verifier` concluído emite `plan.verdict_recorded`; os outputs declarados emitem
      `plan.payload_published`; o `conditional_on` passa a ser avaliado sobre veredictos reais.
      *(Feito: o veredicto lê-se da saída final por gramática fechada — texto à volta, campo a mais,
      outcome ou razão fora da gramática ⇒ `fail` `verdict_unparseable`; os `subjects` vêm do plano.
      `TestAOS413_VeredictoFailOuIlegivelNaoLibertaORisco`. POR FAZER: os payloads — a saída de um
      nó não chega ao run seguinte, porque o conteúdo é untrusted e o prompt não tem canal
      separado por taint (DEF-806); publicar referências sem consumidor seria decorativo.)*
- [x] O headroom liberta-se na conclusão (o laço não esgota o tecto de concorrência). *(E a
      retoma re-adquire-o para os nós que um `serve` anterior deixou a correr.)*
- [x] O `serve` termina quando o plano chega a estado terminal (ou declara, com código de saída
      próprio, que deixou nós a correr), e o `inspect` mostra o resultado por nó. *(Evidência: a
      linha `execucao: n1=complete …`; `--plan-timeout` esgotado ⇒ saída **8**, posse largada, e a
      invocação seguinte retoma sem re-materializar (lê o `plan.materialized` do log) —
      `TestAOS413_PrazoComNosEmVooSai8ERetoma`. O estado por nó fica no stream do run; o `inspect`
      não mudou.)*
- [x] Um nó `danger` aprovado executa e um nó não aprovado não executa — pelo processo real.
      *(Evidência: `TestAOS413_OrganigramaAprovadoExecutaAteAoFim` — o plano do `run-aos412-vivo-1`
      contra um nó falso: nada é submetido enquanto está pendente; aprovado, `n1`, `n2` e `n3` são
      runs do nó, cada um com a lista-branca do SEU nó (`n2` com `[]`), e o `n3` só corre depois do
      `pass`.)*
- [ ] Verificado em produção com o modelo vivo: um organigrama com `verifier` e ramo condicional
      chega ao fim (o caso do `run-aos412-vivo-1`).

### Lacunas a verificar no desenho

- O comentário de `dispatchSink.Dispatch` diz que o spawn de um PAPEL falha fail-closed (o token do
  run traz `cap:plan` e o `IssueChild` exige `Authority` ⊆ folha do pai), mas em produção o `n1` foi
  «papel spawnado» com sucesso. Ou o comentário ficou desactualizado, ou o spawn passa por uma razão
  que convém conhecer antes de lhe pendurar execução.
- O que é o «trabalho» de um nó sem skills: o `tecnica/18` declara como lacuna honesta que os nós só
  correm sobre tools registadas. O objectivo do nó é o prompt; as tools pinadas são o que pode fazer.
- A PR aberta que torna as arestas do plano duráveis no grafo (`task.edge.added`, DEF-913) toca no
  mesmo `RebuildDAG`: coordenar a ordem.

### Fora de âmbito

- Um executor de skills (a lacuna do `capability_gap`, AOS-240).
- O re-planeamento quando a regra AOS-231 recusa uma decomposição viva (observado na validação do
  AOS-412) — é outro ticket, se se quiser.

- [x] Verificado em produção com o modelo vivo: um organigrama com `verifier` e ramo condicional
      chega ao fim (o caso do `run-aos412-vivo-1`). *(Ver abaixo.)*

### Estado

**FEITO.**

**Verificado em produção a 2026-09-20** (`v0.1.24`, imagem `sha256:d808d964…`), com o **modelo
vivo**, no run `run-aos413-vivo-1`. O snapshot de validação usa os nomes de tool DO NÓ
(`doc_read`, `web_post`) — com outros nomes a lista-branca nega tudo e o plano não faz nada.

O planeador decompôs em três nós: `read_notes` lê com `doc_read`; `verify_publication` é o
verificador do que ele leu; `publish_external` publica com `web_post` (`danger`) e só corre com
`verdict eq pass`. O plano ficou pendente (`EXIT=6`), foi aprovado por decisão assinada fora do
servidor, e o `serve --plan-doc` correu com o executor composto:

| Passo | Saída em produção |
|---|---|
| Banner | `executor de nos (AOS-413, ADR-027): COMPOSTO — cada no despachado e um run do no aos em http://aos:8080 (chamador autenticado pelo IdP …)` |
| Gate | `gate de plano: APROVADO por humano … nos_de_risco=1` |
| Materialização | `nos=3 oraculo=snapshot(sha256:snap-aos413-validacao)`, com `cap:tool:doc_read`, `cap:tool:web_post` e o verificador SEM tools |
| Execução | `no read_notes complete (run run-aos413-vivo-1~read_notes)` e `no verify_publication complete (run …~verify_publication)` — **runs reais do nó `aos`** |
| Veredicto no log | `node_id=verify_publication subjects=["read_notes"] outcome=fail reasons=["documento_nao_fornecido"]` |
| Fim | `nos_despachados=2`, `execucao: publish_external=ready read_notes=complete verify_publication=complete`, `EXIT=0` |

**O que isto prova:** os nós do plano executam e concluem de forma durável (antes ficavam
`running` para sempre); cada run levou a lista-branca do SEU nó; o veredicto veio do modelo vivo
na gramática fechada, com os sujeitos tirados do plano; e o nó `danger` APROVADO **não** correu,
porque a condição que o liberta não se cumpriu — o ramo condicional é avaliado sobre um veredicto
real.

**E confirma o limite declarado (DEF-806).** A razão do `fail` é `documento_nao_fornecido`: o
verificador não vê o que o `read_notes` leu, porque a saída de um nó não chega ao run do nó
seguinte. O plano executa-se e governa-se; os nós ainda não trocam dados. É o próximo passo
natural desta linha, e precisa de um canal de entrada separado por taint.

**Higiene da validação:** o NHI do run (45 min) foi cunhado pelo operador com login no IdP, ficou
num ficheiro montado e não passou por variável de ambiente. O segredo do cliente do IdP está em
`0400` do utilizador `aos` e o contentor corre como uid 65532; sem `root` na sessão, a validação
usou uma CÓPIA legível localmente (`orq-client-secret`), a apagar no fim, com rotação do segredo
recomendada. A forma correcta — cópia com dono 65532 e `0400` — exige `sudo` com terminal.


---

## AOS-414 — Os nós de um plano trocam dados por um canal de entrada marcado como untrusted

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-413 (os nós executam como runs do nó), ADR-027, ADR-022 §2.3 (payload tipado por aresta), ADR-005 (taint) |
| Bloqueia | — |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | `packages/cmd/aos/api.go` (`POST /runs`), `packages/kernel/agent-runtime/loop.go` (montagem do tail), `packages/kernel/agent-runtime/prompt.go` (`tailFromHistory`, marcação `taint=`), `packages/control-plane/runlifecycle/readers.go` (`PayloadReader`), `packages/control-plane/runlifecycle/emitters.go` (`RecordPayloadPublished`) |

### Contexto

O AOS-413 pôs os nós a executar, e a validação em produção (v0.1.24, run `run-aos413-vivo-1`)
mediu exactamente onde a cadeia ainda se parte: o verificador respondeu

```
outcome=fail reasons=["documento_nao_fornecido"]
```

— porque a saída do nó anterior **não chega** ao run do nó seguinte. O nó `danger` aprovado não
correu, e fez bem: a condição que o liberta nunca se cumpriu. Enquanto isto não mudar, **qualquer
plano com verificação termina em `fail`**, e o `consumes` de ADR-022 §2.3 é um contrato que
ninguém pode cumprir.

Não é um esquecimento do AOS-413: está declarado no ADR-027. O conteúdo produzido por um run é
**untrusted** (ADR-005), e as duas entradas do prompt que existiam não servem — o `objective` é
**trusted** (vem de uma submissão autenticada) e o `memory_context` **não tem separação de taint**
(é o DEF-806, cujo eixo é o AOS-069). Levar conteúdo por qualquer uma delas era branquear o taint.

### Objectivo

Um nó recebe, no seu run, os payloads que o plano lhe declarou em `consumes` — com a marca de
untrusted e a proveniência (nó de origem, contrato, digest) visíveis no prompt materializado —,
sem que isso enfraqueça a fronteira de privilégio.

### Decisão a tomar primeiro (do dono): onde vive o conteúdo

O nó cifra o conteúdo não-determinístico por-titular no seu Event Store; o `aos-orq` **não** tem
acesso a esse conteúdo, e o `final_text` de um run filho vive na memória do nó (um reinício
perde-o). Opções:

- **(A) Em memória do `serve`, por referência no log.** O `aos-orq` lê o `final_text` do run
  filho, publica `plan.payload_published` (referência + digest, sem conteúdo) e entrega o conteúdo
  ao run seguinte. Custa: um `serve` que retome depois de morrer não tem o conteúdo, e os nós cujo
  produtor já concluiu têm de voltar a correr.
- **(B) Durável no stream do plano.** O conteúdo passa a ser um facto do log do `aos-orq`. Custa:
  saída de modelo **em claro** no WAL do orquestrador, que não tem a cifra por-titular do nó — uma
  fronteira de dados nova, e o crypto-shredding do nó deixa de a alcançar.
- **(C) Sem transporte: o consumidor vai buscar.** O nó seguinte lê o artefacto com as SUAS tools
  (por exemplo, o mesmo `doc_read`). Custa: só funciona quando o produto do nó é um recurso
  endereçável, e o veredicto de um verificador não o é.

A recomendação à partida é **(A)**, com a duração do plano já limitada pelo NHI (45 min) e a
retoma a re-executar o que falte; **(B)** exige decisão explícita sobre guardar conteúdo untrusted
em claro no orquestrador.

### Critérios de Aceitação

- [ ] Decisão (A)/(B)/(C) registada (emenda ao ADR-027 ou ADR novo), com o impacto em ADR-005.
- [ ] O `POST /runs` ganha um canal de ENTRADA de dados distinto do `objective`, e o conteúdo
      entra no tail como segmento **marcado `taint=untrusted`** com proveniência — a mesma
      marcação de `tailFromHistory`/resultados de tool, nunca uma tag in-band inventada.
- [ ] O `aos-orq` publica `plan.payload_published` por cada output declarado que cumpra
      (referência + digest, derivados do contrato), e entrega ao consumidor só o que o `consumes`
      DELE declara — não o que o produtor quiser dar.
- [ ] Um payload de taint efectivo `untrusted` continua a NÃO alimentar um consumidor com
      autoridade privilegiada: a regra do validador (AOS-231/ADR-022 §2.3) continua a valer e tem
      teste que o prova pelo processo real.
- [ ] O prompt materializado do run consumidor MOSTRA a proveniência (nó, contrato, digest), e há
      teste que prova que o conteúdo não aparece como `objective` nem como directiva trusted.
- [ ] Verificado em produção com o modelo vivo: o caso do `run-aos413-vivo-1` passa a ter o
      verificador a decidir sobre o documento que o `read_notes` leu — `pass` liberta o nó
      `danger` aprovado, `fail` mantém-no retido.

### Fora de âmbito

- **A separação de planos (DEF-806/AOS-069) continua aberta.** Este ticket dá ao conteúdo
  untrusted um canal PRÓPRIO e marcado; não o executa num plano separado do que planeia. Dizer o
  contrário seria fechar por decreto uma dívida que não se fechou.
- A autorização estruturalmente infalsificável do taint (DEF-807).

### Estado

**POR FAZER.**

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
| 1.1 | 2026-09-09 | +AOS-388 (Decomposer LLM de produção + wiring multi-nó no aos-orq): gradua a decomposição LLM offline (doubles) para viva, fechando DEF-803 e a dependência de Model Gateway nomeada em §2/§6. | Equipa AOS |
| 1.2 | 2026-09-16 | +AOS-400 (o prompt de decomposição declara o schema do `PlanDocument`): a validação em produção do AOS-395 mostrou o modelo real a falhar 3/3 com `objective de topo em falta`; o critério de cabeçalho do AOS-391 passa a `[~]`. | Equipa AOS |
| 1.3 | 2026-09-16 | AOS-400 implementado: prompt de decomposição 1.2.0 com o schema e as regras de grafo; o modelo de produção decompõe à primeira tentativa e o critério de cabeçalho do AOS-391 volta a `[x]`. | Equipa AOS |
| 1.4 | 2026-09-17 | +AOS-408 (o gate de aprovação de plano fica composto no `aos-orq`): fecha o residual do DEF-274 (o mapeador `PlanDocument`→`planapproval.Plan` não existia em produção) e o fail-open do `needsCard` derivado do `risk_class` advisory; corrige o eixo de DEF-274/275, que citava o AOS-238 (fechado). | Equipa AOS |
| 1.5 | 2026-09-18 | +AOS-409 (4.º eixo de mutação no `IsEffectTool`): passa a ser o eixo do DEF-275, que o AOS-408 não implementa. AOS-408: duas revisões adversariais e a fronteira de confiança declarada. | Equipa AOS |
| 1.2 | 2026-09-10 | +AOS-389/390/391 (despacho governado do Planeador para v1.1 distribuído): guard fail-closed de condicionais (389), composição do `plandispatch.Dispatcher` sob Tenure com avaliação de elegibilidade/condicionais/headroom (390), e T2-B do Model Gateway (391). Origem: análise adversarial que mediu a violação fail-open do ADR-022 §2.1 no spawn-eager. | Equipa AOS |
| 1.6 | 2026-09-19 | +AOS-412 (com o modelo vivo, um plano de risco aprovado corre pelo `--plan-doc`): fecha o resíduo do AOS-408 «com o modelo vivo, um plano aprovado não despacha». | Equipa AOS |
| 1.7 | 2026-09-19 | AOS-412 verificado em produção (`v0.1.23`) com o modelo vivo: as duas re-decomposições recusadas com 7, o organigrama aprovado materializado e despachado pelo `--plan-doc`. | Equipa AOS |
| 1.8 | 2026-09-19 | +AOS-413 (os nós despachados executam até ao fim): a cadeia do `aos-orq` acabava no despacho — nada executava nem concluía um nó do plano, e a lacuna não estava registada. Decisão de onde corre o trabalho (ADR) antes da implementação. | Equipa AOS |
| 1.9 | 2026-09-20 | AOS-413 implementado (ADR-027) e verificado em produção (`v0.1.24`): dois nós do plano correram como runs do nó `aos`, o veredicto do verificador veio do modelo vivo na gramática fechada e o nó `danger` aprovado não correu por não ter `pass`. O `fail` foi `documento_nao_fornecido` — o limite do DEF-806 medido em produção. | Equipa AOS |
| 1.10 | 2026-09-20 | +AOS-414 (canal de entrada marcado como untrusted): a validação do AOS-413 em produção mediu a cadeia a partir-se — o verificador reprovou com `documento_nao_fornecido` porque a saída de um nó não chega ao run do seguinte, e sem isso qualquer plano com verificação termina em `fail`. | Equipa AOS |
