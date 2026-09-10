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

<!-- rtm: adrs-mencionados -->
<!-- Os ADR-022/ADR-023 citados neste bloco são MENÇÃO — invariantes que o ticket respeita
     (poda `branch_not_taken`; SCH derivador, escritor único por run) — não implementação. O
     ticket compõe/wira o Dispatcher que já existe; é o eixo que corrige DEF-272/273/274/275. -->

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
| Fecha | DEF-274/DEF-275 (residual do wiring; eixo corrigido de AOS-238 para este ticket); a row de deferimento nova do despacho fail-open |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/control-plane/orchestrator/plandispatch/{dispatch.go,ports.go,branches.go,condition.go}`, `packages/control-plane/runlifecycle/{readers.go,emitters.go}`, `runlifecycle.Tenure`, ADR-022, ADR-023, DEF-272/273/274/275 |

### Contexto
Medido: `plandispatch.NewDispatcher` só aparece em `_test.go`; o `DispatchSink` não tem implementação de produção (só o duplo `sinkRegistador`); o `aos-orq` não importa `plandispatch`. O pipeline T2-A (`planner_wiring.go`) termina em `Materialize` + spawn-all eager, **sem** gating de elegibilidade — logo o `depends_on` e as arestas condicionais de um plano aprovado não são respeitados em runtime. As portas de leitura que o Dispatcher consome (`LifecycleView`/`ResultView`/`PayloadView`/`BranchJournal`) **já estão implementadas** por `runlifecycle` (DEF-272/273 fechados por AOS-281). O que falta é o **chamador** e o **sink**. Esta é a etapa que DEF-272/273/274 nomeavam como eixo `AOS-238` — mas AOS-238 está **fechado** e o seu guard-test `TestBoundary_ProductionImportsAreAllowlisted` proíbe o import do módulo de ciclo-de-vida; paga-se com **ticket novo** (precedente: AOS-281).

### Objectivo
Compor o `plandispatch.Dispatcher` de produção dentro do `aos-orq serve`, sob `runlifecycle.Tenure` (lease + fencing), como **escalonador re-invocável por passagem** que: lê o estado do ciclo de vida, decide elegibilidade (`NodePending` + `depends_on` cumpridos), **avalia arestas condicionais e poda `branch_not_taken`**, consulta o card oracle, aplica headroom de concorrência com diferimento, e entrega os nós **elegíveis** a um `DispatchSink` de produção — **in-process**, que delega o efeito ao `Delegator.Spawn`/`GraphBuilder` que já existem. Substitui a recusa do AOS-389 por avaliação real. O SCH continua **derivador** (não escreve ciclo de vida — invariante ADR-023).

> **Nota de fronteira (anti-scope-creep).** O sink é **in-process**: no modelo per-run do ADR-023 o processo dono corre o laço de despacho para os seus runs, e o efeito delega no `Delegator` local. **Não** é preciso um work-queue sobre JetStream para distribuir os NÓS de um run entre processos — isso seria distribuição intra-run, que o modelo per-run torna desnecessária, e fica **fora** da v1.1.

### Critérios de Aceitação
- [ ] `aos-orq serve` compõe um `plandispatch.Dispatcher` de produção sob `Tenure`; `NewDispatcher` passa a ter chamador fora de `_test.go` (um guard-test "aos-orq importa plandispatch em produção" fica verde).
- [ ] Existe uma implementação de produção de `DispatchSink` que delega o efeito ao `Delegator.Spawn` (papéis) e ao `GraphBuilder`/`LeafAdmitter` (folhas) — o mesmo efeito governado (RM + reserva CAS + NHI) que hoje corre eager, mas **só para nós elegíveis**.
- [ ] **Elegibilidade por estado**: só nós `NodePending` são despachados; um nó materializado/terminal não é re-despachado (idempotência por `(run_id, step_id)`).
- [ ] **Gating por `depends_on`**: um nó com dependência não concluída **não** é despachado (teste: `B depends_on A`, `A` pendente ⇒ `B` não spawna).
- [ ] **Arestas condicionais + poda `branch_not_taken`** (ADR-022 §2.1): `B conditional_on A {verdict=fail}` — se `A` passa, `B` é decidido `branch_not_taken` e **não** tem efeito; se `A` falha, `B` é despachado. Prova pela composição real, caso positivo **e** negativo. **Isto fecha a violação medida no AOS-389.**
- [ ] **Headroom de concorrência**: com headroom esgotado, os nós elegíveis excedentes ficam `OutcomeDeferredHeadroom` e são retomados numa passagem seguinte quando a concorrência liberta — sem os perder e sem fail-open.
- [ ] **Card oracle**: um nó cujo card exige aprovação humana (`danger`) fica `waiting`, não é spawnado sem o gate (AOS-236).
- [ ] **Semântica re-invocável**: o Dispatcher é função por passagem, sem laço próprio; o escalonador do `serve` re-invoca-o quando `ResultView`/`LifecycleView` mudam ou headroom liberta. **Não escreve ciclo de vida** (teste: o stream do run não cresce por escrita do dispatcher numa passagem só de leitura).
- [ ] **Sob Tenure**: um dispatcher cuja posse foi superada é recusado por fencing (`ErrStaleFencingToken`) sem tocar no log — herda a disciplina de AOS-281.
- [ ] **AOS-389 é superado**: o guard fail-closed de condicionais é substituído pela avaliação real; o guard-test de não-regressão de AOS-389 passa a assertar avaliação em vez de recusa.
- [ ] **Registo**: a row de deferimento do gap de despacho passa a `FECHADO-RESIDUAL`/removida; DEF-274 e DEF-275 têm o Eixo corrigido para este ticket. RTM regenerada.

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
| Fecha | DEF-803 (o marcador `STUB` sai do código) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos-orq/planner_wiring.go` (`fixtureModel`, `modeloDeDecomposicao`), `packages/control-plane/orchestrator/decompose/decompose.go`, `packages/platform/model-gateway/`, ADR-019 §2.5, ADR-020, ADR-005 |

### Contexto
Medido: sem `--decompose-fixture`, `--goal` recusa fail-closed com erro que nomeia o "Model Gateway" (`TestAOS388_GoalFailClosed`); o único `decompose.Model` é o `fixtureModel` (marcado **NÃO-PRODUÇÃO**); o `aos-orq` não importa `platform/model-gateway`. **DEF-803** continua `STUB`/`ABERTO`, ticketado como AOS-388. Ligar exige token NHI com `model:invoke` verificado e uma decisão **ADR-020** sobre a fidelidade do token (token do run vs. `agent:planner`).

### Objectivo
Compor um `decompose.Model` de produção que invoca o Model Gateway para produzir o `PlanDocument` a partir do `goal`, sob a identidade e o orçamento corretos, substituindo o `fixtureModel` no caminho `--goal`. Fecha DEF-803 e o critério de saída do goal→DAG real.

### Critérios de Aceitação
- [ ] `--goal` **sem** `--decompose-fixture` produz um `PlanDocument` via Model Gateway (deixa de recusar); `--decompose-fixture` continua disponível para testes offline.
- [ ] A invocação corre sob NHI com autoridade `model:invoke` **verificada** (não um token sem escopo); a decisão ADR-020 sobre qual identidade usar está documentada e implementada.
- [ ] A reserva de planeamento é admitida antes da decomposição (AOS-234) e o custo do turno flui para o burn-down (AOS-259).
- [ ] O `PlanDocument` produzido passa pelo validador puro (AOS-231); se o modelo emitir arestas condicionais, elas são **avaliadas** por AOS-390 (nem recusadas por AOS-389, nem executadas fail-open).
- [ ] Fail-closed preservado: falha do Gateway, token sem `model:invoke`, ou plano inválido ⇒ o run não avança com plano fantasma (erro declarado, nada spawnado).
- [ ] **DEF-803 passa a FECHADO** (o marcador `STUB` sai do código); RTM regenerada.
- [ ] O golden-set/eval-gate do planeador (AOS-241) continua verde com o modelo real atrás de doubles no gate offline.

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
| 1.2 | 2026-09-10 | +AOS-389/390/391 (despacho governado do Planeador para v1.1 distribuído): guard fail-closed de condicionais (389), composição do `plandispatch.Dispatcher` sob Tenure com avaliação de elegibilidade/condicionais/headroom (390), e T2-B do Model Gateway (391). Origem: análise adversarial que mediu a violação fail-open do ADR-022 §2.1 no spawn-eager. | Equipa AOS |
