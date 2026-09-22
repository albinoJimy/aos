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

### O que a discovery mediu, e que condiciona quem implementar

- **O facto não traz o que o `serve` exige.** O payload tem `run_id` e `objective`; o
  `serve --goal` exige **também** `--snapshot` (ficheiro pinado, hoje colocado à mão em
  `/etc/aos-orq/snapshot.json`) e o Model Gateway por ambiente (`AOS_MODEL_ENDPOINT`/`_NAME`), sem
  os quais recusa. De onde vem o snapshot por pedido? Um snapshot global significa que **todos** os
  pedidos correm contra o mesmo catálogo pinado.
- **`region` e `board` viajam no facto e nada no `serve` os honra.** Não há flag de região; com
  `--nats-region` a fronteira é do store inteiro, não do run. Um consumidor que ignore o `region`
  do facto viola a intenção escrita no ingresso — e a soberania é o eixo onde o AOS-417 já
  tropeçou uma vez.
- **O molde do `approval_store_durable` NÃO é importável:** `packages/integration` não está no
  `go.mod` do `aos-orq`, e o tipo vive em `package integration`. Terá de ser **reescrito**, não
  reutilizado. Em contrapartida o `control-plane/scheduler` **já está** no grafo (indirect, com
  `replace`), pelo que o vocabulário de backpressure é alcançável.
- **Do backpressure do EPIC-03 reaproveita-se o vocabulário, não a fila.** O `Degrader` e o
  `PolicyEngine` são injectáveis e as acções estão modeladas (`ActionShed`, `ActionDefer`,
  `ActionDowngrade`, `ActionReject`), mas o `PartitionedQueues` é **em memória** — e o ADR-028 §3
  rejeita-o explicitamente por isso. O `Defer` exige um `DeferSink` que para uma fila durável não
  existe.
- **Mapeamento dos códigos de saída**, que a decisão (4) tem de fixar: `3` (lease detido por outro)
  é **transitório**; `8` (prazo esgotado com nós em voo) é **retomável**; `6` (pendente de decisão
  humana) e `7` (decisão recusada) são **terminais para esta invocação**; `9` (plano recusado pela
  validação) é **permanente**. Os códigos `6/7/8/9` **largam** a posse; qualquer outro `1`
  retém-na até ao TTL de 30s — o que importa para a cadência de re-tentativa.
- **Custo de leitura O(n).** O molde existente varre o stream inteiro por passagem. O
  `approval_store_durable` declara-o aceitável «porque a partição é pequena»; uma fila sem
  retenção não o é. É a razão PRÁTICA pela qual a decisão (3) importa, e não só a de espaço.
- **Determinismo dos testes:** o molde do repositório é o `ExportBackupNow`/`SweepRetentionNow` —
  um método exportado que conduz UM ciclo sem esperar pelo ticker. O `testkit` tem
  `NewManualClock`, `MustEventStore` e `IdempotencyKey(runID, stepID)` — a **mesma** função pura
  que o Event Store usa, pelo que a chave se assere sem a reescrever. E o
  `dois_processos_test.go` do `aos-orq` já compila o binário e corre dois processos reais — é lá
  que o teste dos dois consumidores pertence.
- **Efeito colateral garantido:** o `TestAOS417BannerDoConsumidorNaoApodrece` fica **vermelho** no
  instante em que qualquer ficheiro de `packages/cmd/aos-orq/` contiver o nome do stream —
  incluindo um teste. É intencional, e obriga o PR do consumidor a corrigir o literal `false` no
  `bootstrap.go` do nó.
- **LACUNA por fechar, herdada do AOS-417:** não há evidência de que o caminho novo passe pelo
  orçamento por árvore (AOS-027). O `materializar` usa tectos `1<<30` vindos do próprio comando,
  declaradamente de demonstração. Um consumidor torna trivial disparar corridas; sem isto
  verificado, torna trivial disparar **custo**.

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

- [x] Decisão (A)/(B)/(C) registada (emenda ao ADR-027 ou ADR novo), com o impacto em ADR-005.
      *(Evidência: **(A)**, decidida pelo dono a 2026-09-20 e emendada no ADR-027 §2.4.)*
- [x] O `POST /runs` ganha um canal de ENTRADA de dados distinto do `objective`, e o conteúdo
      entra no tail como segmento **marcado `taint=untrusted`** com proveniência — a mesma
      marcação de `tailFromHistory`/resultados de tool, nunca uma tag in-band inventada.
      *(Evidência: campo `inputs` → `Goal.Inputs` → segmento `TailPlanInput`, com
      `plan_input_from/output/digest` nos rótulos da linha de delimitação. O nó VERIFICA o digest
      e recusa na fronteira (400) um payload sem contrato, sem digest, com digest que não bate ou
      acima dos tectos — `TestAOS414_InputsNaFronteiraDoNo` (5 casos);
      `TestAOS414_PayloadEntraMarcadoUntrustedComProveniencia`;
      `TestAOS414_PayloadSubmetidoChegaAoPromptDoRun` pelo nó real, com mutação.)*
- [x] O `aos-orq` publica `plan.payload_published` por cada output declarado que cumpra
      (referência + digest, derivados do contrato), e entrega ao consumidor só o que o `consumes`
      DELE declara — não o que o produtor quiser dar. *(Evidência:
      `TestAOS414_OVerificadorRecebeOQueONoAnteriorLeu` e `TestAOS414_SoOQueOConsumesDeclara`;
      um `metrics` sem fonte NÃO se publica, em vez de se inventarem números.)*
- [x] Um payload de taint efectivo `untrusted` continua a NÃO alimentar um consumidor com
      autoridade privilegiada: a regra do validador (AOS-231/ADR-022 §2.3) continua a valer e tem
      teste que o prova pelo processo real. *(Inalterada: o plano é recusado na validação, antes
      de existir transporte; o canal não lhe mexe.)*
- [x] O prompt materializado do run consumidor MOSTRA a proveniência (nó, contrato, digest), e há
      teste que prova que o conteúdo não aparece como `objective` nem como directiva trusted.
      *(Evidência: o teste confirma que o segmento do objectivo não contém o conteúdo, e
      `TestAOS414_PayloadNaoForjaSegmentoTrusted` prova que um payload com `<correction>` no corpo
      não forja o único rótulo trusted da janela.)*
- [x] Verificado em produção com o modelo vivo: o caso do `run-aos413-vivo-1` passa a ter o
      verificador a decidir sobre o documento que o `read_notes` leu — `pass` liberta o nó
      `danger` aprovado, `fail` mantém-no retido. *(Ver abaixo: `run-aos414-vivo-2`, v0.1.25.)*

### Fora de âmbito

- **A separação de planos (DEF-806/AOS-069) continua aberta.** Este ticket dá ao conteúdo
  untrusted um canal PRÓPRIO e marcado; não o executa num plano separado do que planeia. Dizer o
  contrário seria fechar por decreto uma dívida que não se fechou.
- A autorização estruturalmente infalsificável do taint (DEF-807).

### Estado

**FEITO.**

**Verificado em produção a 2026-09-20** (`v0.1.25`, imagem `sha256:51ca557c…`), com o modelo vivo,
no run `run-aos414-vivo-2`. O planeador decompôs em `n1_read_notes` (lê com `doc_read`),
`n2_verify_content` (verificador, sem tools) e `n3_publish_external` (`web_post`, `danger`,
condicional ao `pass`). O plano ficou pendente, foi aprovado por decisão assinada fora do servidor,
e o `serve --plan-doc` com o executor composto levou a cadeia ao fim:

| Facto no log | Conteúdo |
|---|---|
| `plan.payload_published` (n1) | `output=notes_content type=record taint=untrusted`, referência `stream=run-aos414-vivo-2~n1_read_notes` com digest |
| `plan.verdict_recorded` (n2) | `subjects=["n1_read_notes"] outcome=pass reasons=["conteudo_nao_contem_segredos","sem_credenciais_chaves_ou_tokens","sem_dados_pessoais_sensiveis_apenas_nomes_proprios","informacao_tecnica_generica_sem_identificadores_internos"]` |
| `plan.branch_decided` (n3) | `taken=true sources=["n2_verify_content"]` |
| Fim do `serve` | `nos_despachados=3`, `execucao: n1_read_notes=complete n2_verify_content=complete n3_publish_external=complete`, `EXIT=0` |

**A prova está nas razões do veredicto.** Na validação do AOS-413 o verificador reprovava com
`documento_nao_fornecido`; aqui pronuncia-se sobre o QUE LEU — quatro razões sobre segredos,
credenciais, dados pessoais e identificadores internos. É a diferença entre o canal existir e não
existir. O nó `danger` aprovado correu porque a condição se cumpriu, e não porque alguém o deixou
passar.

**Observado, fora deste ticket:**

- A primeira decomposição viva foi recusada pela regra AOS-231 (`consumes_taint_authority`): o
  modelo tentou alimentar um consumidor com autoridade privilegiada a partir de um payload
  untrusted. A regra fez o seu trabalho — e o `serve` não re-planeia (resíduo do AOS-412), pelo
  que foi preciso repetir.
- Esse `serve` recusado **reteve a posse do run**: a invocação seguinte com o mesmo `--run` saiu
  com `3` (lease detido) e a validação seguiu num run novo. Largar a posse numa recusa de
  validação é candidato a ticket.
- Ler o WAL com `grep` deu contagens FALSAS (zero veredictos) por causa do enquadramento binário
  do ficheiro; só `strings` mostrou os 33 eventos. Quem verificar um WAL de produção à mão que
  use `strings`, ou lerá um log incompleto e concluirá o contrário do que lá está.

**Decisão do dono: opção (A)** — o conteúdo vive na memória do `serve`, e no log fica a
referência com o digest. Uma retoma sem o material recusa-se a correr o consumidor
(`ErrPayloadPerdido`), em vez de o correr às cegas.

**Revisão adversarial independente.** O gate, os contratos e a marcação do prompt aguentaram
(incluindo a injecção de rótulos pela proveniência, que o assembler saneia). Oito achados, todos
tratados:

| Achado | O que mudou |
|---|---|
| Um contrato impossível de cumprir (`metrics`) abortava o `serve` e repetia-se em todas as retomas | O CONSUMIDOR fecha em `failed`, com a razão, e o plano termina |
| O aborto acontecia a meio da passagem, deixando irmãos em voo por recolher | A poda corre ANTES dos retratos da passagem |
| Os tectos por payload (16×256 KiB) eram inalcançáveis: o corpo do `POST /runs` corta a 1 MiB | 128 KiB por payload, 512 KiB agregado, e tecto no produtor |
| O replay não semeava os payloads: um run com entradas divergia no turno 1 | `TrajectorySpec.Inputs` semeado pelo mesmo construtor do loop |
| Dois contratos de forma aberta recebiam os MESMOS bytes | Um nó com mais do que um contrato aberto não publica nenhum |
| O digest era descrito como prova do que o plano publicou | É um controlo de integridade do transporte, e está dito assim |
| O banner e o cabeçalho ainda diziam que nada é transportado | Corrigidos |
| O `PayloadResolver` ficou sem chamador | Declarado como resíduo |

**Resíduos declarados:** um contrato `metrics` não se publica (ninguém mede os números, e
inventá-los era pior), e um segundo contrato de forma aberta também não; o conteúdo não sobrevive
à morte do `serve`; o `PayloadResolver` continua por ligar; e a separação de planos
(DEF-806/AOS-069) continua aberta — este ticket dá canal próprio e marcação, não plano separado.

---

## AOS-415 — O veredicto da validação volta ao planeador, e a recusa deixa de acabar o run

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-231 (validador e o enum de razões), AOS-388/AOS-391 (planeador governado com laço de tentativas), AOS-400 (prompt 1.2.0) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/control-plane/orchestrator/planner/planner.go` (laço de tentativas, `DecomposeInput`), `packages/control-plane/orchestrator/decompose/decompose.go` (`Decomposer`, `renderUser`), `packages/control-plane/orchestrator/planvalidate/verdict.go` (`Verdict`, `Reason`, `Locator`), `packages/cmd/aos-orq/planner_wiring.go` (`decomporEMaterializar`, `validarEstrutura`) |

### Contexto

Nas **duas** validações em produção com o modelo vivo, a PRIMEIRA decomposição foi recusada pela
validação AOS-231 — as duas vezes por `consumes_taint_authority` — e o `serve` terminou com `1`:

| Quando | Run | Resultado da 1.ª tentativa |
|---|---|---|
| v0.1.23 (AOS-412) | `run-aos412-vivo-1` | recusada (`consumes_taint_authority`), repetida à mão |
| v0.1.25 (AOS-414) | `run-aos414-vivo-2` | recusada (`consumes_taint_authority`), repetida à mão num run NOVO |

O caminho principal do produto falha em cerca de metade das corridas, e a recuperação é manual. A
razão é estrutural, e está declarada no próprio epic (§AOS-391): **o laço de tentativas do
planeador só cobre falhas do decompositor** — erro de chamada, resposta vazia, `plan.Decode`
falhado (`planner.go:442`, `decompose.go:138-148`; o default são 3 tentativas). A validação
estrutural corre **fora e depois** desse laço (`planner_wiring.go`, `validarEstrutura`), pelo que
uma recusa do validador é terminal, com zero retentativas.

E o pior: **cada tentativa reenvia o mesmo prompt**. O `DecomposeInput` leva o número da tentativa
e mais nada; o modelo não sabe o que fez de errado.

A matéria-prima existe e foi desenhada exactamente para isto. O `planvalidate.Reason` é um enum
FECHADO e sem conteúdo, e o comentário do ficheiro di-lo: existe «para dar ao re-planeamento um
sinal accionável sem vazar conteúdo untrusted» (`verdict.go`). Hoje o `validarEstrutura` colapsa o
veredicto numa string e **deita fora a `Rule` e o `Locator`**.

### Objectivo

Uma decomposição recusada pela validação é reapresentada ao modelo com a razão — em código
fechado, sem conteúdo — e o `serve` só desiste depois de esgotar as tentativas.

### O PROBLEMA CENTRAL, medido: hoje o consumidor não consegue alcançar a fila

Isto não é um detalhe de implementação — é a pergunta que tem de ser respondida antes de se
escrever código, e **os três caminhos possíveis estão fechados no código de hoje**.

A fila vive no Event Store **do nó**. Reclamar um item, no molde do `approval_store_durable`, é
uma **escrita** (`Append` com idempotency-key). Ora:

| Caminho | Estado hoje | Onde se mede |
|---|---|---|
| **Ficheiro (`--wal`)** | **FECHADO.** O nó toma `LockWAL` sobre o seu WAL enquanto corre. O `aos-orq` só consegue `abrirParaLeitura` (`OpenReadOnly`, sem tranca); `abrirParaEscrita` faz `LockWAL` primeiro e devolve `ErrWALHeld` ⇒ saída **5** | `packages/cmd/aos/wal_posse.go` (`guardDePosseAplicavel`), `packages/cmd/aos-orq/substrato.go` (`abrirParaEscrita`) |
| **JetStream (`--nats`)** | **Estava fechado por um defeito do ingresso**, entretanto corrigido: o nome da fila tinha um ponto e o `subjectDe` recusa-o, pelo que o `POST /plans` respondia `503` a tudo sobre NATS. Ver o bloco do AOS-417 sobre isso. **Mas continua sem topologia**: não há serviço NATS no `docker-compose.prod.yml` e o `AOS_EVENTSTORE_NATS` tem default vazio | `packages/substrate/eventstore/jetstream/store.go` (`subjectDe`), `deploy/server/docker-compose.prod.yml` |
| **HTTP** | **FECHADO POR DESENHO.** Não há rota de leitura da fila, e a barra em `aos-internal/` existe precisamente para que `GET /runs/{id}/...` não a alcance — foi um dos dois defeitos críticos que a revisão do AOS-417 encontrou. Abrir uma rota reabriria as questões do ADR-016 que o ingresso fechou | `packages/cmd/aos/planos.go`, `aos417_soberania_test.go` |

E há um facto de produção que agrava a pergunta: **o Event Store do `aos-orq` em produção não é o
do nó.** A receita em vigor usa um WAL **por run** (`--wal /var/lib/aos-orq/run-X.wal`), em volume
próprio (`aos-orq-data`), criado no momento pelo operador. Não existe hoje nenhum store partilhado
entre os dois processos.

**Consequência para este ticket:** a decisão (1) abaixo não é sobre o *feitio* do trabalhador — é
sobre **que substrato passa a ser partilhado**, e isso é uma mudança de topologia de produção, não
uma opção de código. Qualquer desenho que ignore isto escreve um consumidor que não corre.

### Decisões a tomar primeiro (do dono)

1. **Onde vive o laço.** (a) A validação entra no planeador, que já tem o laço, por uma porta
   nova (`Validator`) — o planeador passa a devolver só planos válidos, e o `aos-orq` deixa de
   validar a jusante; (b) o laço fica no `aos-orq`, que já conhece o snapshot, e o planeador não
   muda. **(a)** mantém uma só autoridade sobre «o que é um plano admissível» e é a recomendada;
   **(b)** é menor mas espalha o critério por dois sítios.
2. **Que forma tem o feedback no prompt.** O que se reenvia é `rule`, `reason` e `node_id` do
   `Locator` — nunca texto do documento nem do modelo. Falta decidir se entra como bloco próprio
   do prompt (e se isso obriga a subir a versão do prompt, hoje 1.2.0) ou como instrução no
   `user`.
3. **Quantas tentativas.** Hoje são 3 para o decode. A recusa de validação partilha o mesmo tecto
   (recomendado: o custo de planeamento já é debitado por tentativa) ou tem tecto próprio?

### Critérios de Aceitação

- [x] Decisão (1)/(2)/(3) registada no ticket; se mudar a versão do prompt, ADR ou nota no epic.
      *(Decidido a 2026-09-20: **(1)** o laço vive no PLANEADOR, por uma porta `Validator` que o
      `aos-orq` injecta com o snapshot pinado — uma só autoridade sobre o que é admissível;
      **(2)** bloco próprio no `user` e o CONTRATO no template, que sobe a **1.3.0** (regra 11)
      sob o gate ADR-012; **(3)** tecto PARTILHADO de 3 tentativas.)*
- [x] Uma recusa da validação AOS-231 gera nova tentativa, com `rule`/`reason`/`node_id` no
      prompt — e o teste prova que o conteúdo do documento **não** é reenviado. *(Evidência:
      `TestAOS415_RecusaDoValidadorGeraNovaTentativaComARazao` (a 1.ª tentativa sem recusa, a 2.ª
      com ela); `TestAOS415_RecusaEntraNoUserEmCodigos` (e o template NÃO é tocado, ADR-009);
      `TestAOS415_ODocumentoRecusadoNaoVolta` (nenhum valor do documento recusado aparece).)*
- [x] O tecto é respeitado: esgotadas as tentativas, o `serve` sai como hoje, com a razão da
      ÚLTIMA recusa. *(Evidência: `TestAOS415_TectoEsgotadoDevolveARecusa` — e o erro é
      `ErrPlanRejected`, NÃO `ErrDecomposition`: o modelo produziu documento, o que não é
      admissível é o documento.)*
- [x] Cada tentativa continua a ser debitada no orçamento de planeamento e a ter o seu span
      (`planner.go` mantém a contabilidade actual). *(Evidência:
      `TestAOS415_TentativaRecusadaContinuaAAbrirSpanPorTentativa` — duas tentativas, dois spans
      `chat`, cada um anotado com o custo por tentativa. A RESERVA continua dimensionada para
      `maxAttempts`, e uma recusa não acrescenta chamadas ao modelo além do tecto.)*
- [~] O facto durável do planeador regista quantas tentativas foram recusadas pelo validador e
      com que razão (observabilidade de fiabilidade, hoje inexistente). *(Feito no SPAN
      (`aos.planner.validator_rejections`) e no `PlanResult.ValidatorRejections`. POR FAZER no
      facto durável: o `plan.planner_admitted` é apensado ANTES das tentativas (é a admissão, não
      o desfecho), e acrescentar-lhe um contador exigiria um facto novo — que se abre quando
      alguém precisar dele para medir fiabilidade ao longo do tempo.)*
- [x] **Falha-antes por processo real:** um decompositor-fixture que devolve um plano recusado na
      1.ª tentativa e um válido na 2.ª — hoje o `serve` sai com 1; depois, materializa.
      *(Evidência: `TestAOS415_RecusaDaValidacaoGeraNovaTentativaNoBinario`; o `--decompose-fixture`
      passa a aceitar vários ficheiros separados por vírgula, um por tentativa — superfície
      NÃO-PRODUÇÃO, como o próprio flag. Com a mutação que tira o validador do laço, o binário
      reproduz a falha de produção: `tentativas=1` e `consumes_taint_authority`.)*
- [x] Verificado em produção: uma corrida `--goal` com o modelo vivo que recupere de uma recusa
      sem intervenção. *(Ver abaixo: `run-aos415-vivo-3`, `tentativas=3`, v0.1.26.)*

### Âmbito acrescentado, e porquê

- **Largar a posse do run quando a validação recusa.** Observado na validação do AOS-414: o
  `serve` recusado reteve o lease, e a invocação seguinte com o mesmo `--run` saiu com `3`. É o
  mesmo caminho de falha que este ticket toca (`largarSePendente` já trata o pendente e a recusa
  de decisão), e deixá-lo de fora obrigaria o operador a esperar pelo TTL na corrida seguinte.
  *(Feito: `ErrPlanRejected` larga a posse; `TestAOS415_TectoEsgotadoLargaAPosse` prova que a
  invocação seguinte com o MESMO run toma a posse e materializa.)*

### Fora de âmbito

- **O `replan.Coordinator` (AOS-239), que continua sem chamador de produção.** Governa o
  re-plano de uma árvore EM EXECUÇÃO — orçamento residual, autonomia fixada, nós concluídos
  intocáveis — e não a primeira decomposição. Ligá-lo é outro ticket, com outra justificação.
- A separação de planos (DEF-806/AOS-069) e o eval-gate com modelo vivo (§5, lacuna declarada).

### Estado

**FEITO.**

**Verificado em produção a 2026-09-20** (`v0.1.26`, imagem `sha256:d0dd2667…`), com o modelo vivo.
Cinco corridas, todas reportadas — não só as que favorecem:

| Run | Resultado |
|---|---|
| `run-aos415-vivo-1` (1.ª invocação) | **3 tentativas, todas recusadas** ⇒ saída **9** e posse LARGADA |
| `run-aos415-vivo-1` (2.ª invocação, MESMO run) | Tomou a posse (`token=2`) — antes disto saía `3` («lease detido») —, `tentativas=1`, pendente |
| `run-aos415-vivo-2` | `tentativas=1`, pendente |
| **`run-aos415-vivo-3`** | **`tentativas=3` e o plano passou**: recuperação DENTRO da corrida, sem intervenção, seguida do gate (`EXIT=6`, pendente) |
| `run-aos415-vivo-4` | `tentativas=1`, pendente |

**O que isto prova:** o laço repete sobre a recusa e uma corrida recuperou sozinha — antes deste
ticket, a 1.ª recusa acabava o run com `1`. E a posse é largada: o segundo `serve` com o MESMO
`--run` tomou-a, que é exactamente o que falhava na validação do AOS-414.

**O que isto NÃO prova, e é preciso dizer:** a realimentação não garante sucesso. Na 1.ª corrida
as três tentativas foram recusadas e a razão MUDOU pelo caminho — de `consumes_taint_authority`
para `verifier_commissions_work` —, ou seja, o modelo reagiu ao feedback e caiu noutra regra do
validador. Com quatro corridas em cinco a decompor à primeira e uma a esgotar o tecto, esta
amostra não mede taxa de sucesso: isso é o eval-gate com modelo vivo, que continua a não existir
(§5, lacuna declarada desde o AOS-400).

**Resíduo confirmado em produção:** o tecto de 3 é atingível. Subi-lo é trocar custo por
probabilidade de sucesso, e essa decisão precisa de dados do eval-gate, não de uma corrida.

**Decisões do dono:** o laço no planeador (porta `Validator`), bloco próprio com o prompt a subir
para **1.3.0** (regra 11, sob o gate ADR-012 do AOS-273/AOS-400), e tecto PARTILHADO de 3
tentativas.

**Revisão adversarial independente.** Nove achados, todos tratados. Os três que mudaram
comportamento:

| Achado | O que mudou |
|---|---|
| Uma recusa seguida de falha de decode devolvia `ErrDecomposition`: o `serve` RETINHA a posse e a invocação seguinte saía com `3` — o sintoma do AOS-414, de forma intermitente | Se houve recusa do validador, o desfecho é `ErrPlanRejected`, seja qual for a última falha (`TestAOS415_RecusaSeguidaDeFalhaDeDecodeContinuaARecusa`) |
| A garantia de «só códigos» vivia em quem implementa a porta, não no ponto que escreve no prompt | O decompositor valida os campos da recusa onde os escreve, e OMITE o que não reconhece (`TestAOS415_CampoHostilDaRecusaNaoEntraNoPrompt`) |
| O objectivo era escrito ANTES do bloco: um objectivo hostil podia sintetizar o seu próprio bloco de recusa, que a regra 11 manda levar a sério | O bloco passa a vir primeiro (`TestAOS415_ORecusaVemAntesDoObjectivo`) |

Mais: a recusa passou a ter **código de saída próprio (9)**, porque larga a posse e todo o outro
`1` a retém; uma recusa velha deixa de ser reapresentada depois de uma tentativa que nem produziu
documento; a ajuda do `--decompose-fixture` deixou de mentir; e dois testes que afirmavam mais do
que mediam foram reformulados.

**Resíduo declarado:** o contador de recusas vive no span e no `PlanResult`, não num facto
durável — medir fiabilidade ao longo do tempo exige um facto novo, que este ticket não inventa.

---

## AOS-416 — O executor de nós obtém o segredo do IdP sem uma cópia legível por todos

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa o ADR-027: corrige a superfície de deploy que o ADR-027 assume
     (o executor obtém um Bearer do IdP com o segredo do cliente num ficheiro montado). As
     citações ao ADR-027 são menções. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orchestração |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P0 |
| Estimativa | S |
| Dependências | AOS-413 (executor de nós e a superfície `AOS_ORQ_OIDC_CLIENT_SECRET_FILE`) |
| Bloqueia | Qualquer uso do executor de nós numa instalação limpa |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `deploy/server/docker-compose.prod.yml` (serviço `aos-orq`, montagem de `./secrets/reader-client-secret`), `deploy/server/README.md` (passos do operador para o executor), `packages/cmd/aos-orq/node_client.go` (`nodeClientDoAmbiente`, leitura do ficheiro do segredo), `docs/adr/ADR-027-execucao-dos-nos-do-plano-como-runs-do-no.md` |

### Contexto

O `docker-compose.prod.yml` monta o segredo do cliente do IdP no serviço `aos-orq`:

```yaml
- ./secrets/reader-client-secret:/run/aos-orq/reader-client-secret:ro
```

O ficheiro no servidor está em `0400`, dono `aos`. O contentor corre como `65532:65532`
(`docker inspect`, campo `Config.User`). **O uid do contentor não consegue ler o ficheiro que o
compose lhe monta.** Medido em produção a 2026-09-20, com o uid real e o ficheiro real:

```console
$ docker run --rm --user 65532:65532 \
    -v /opt/aos/secrets/reader-client-secret:/s:ro --entrypoint /bin/sh busybox \
    -c "cat /s >/dev/null && echo LEGIVEL || echo ILEGIVEL"
cat: can't open '/s': Permission denied
ILEGIVEL
```

Sem o segredo não há Bearer; sem Bearer o `nodeClientDoAmbiente` não compõe e o executor de nós
não arranca. Ou seja, **o caminho que o AOS-413 entregou e que o compose documenta não funciona
numa instalação limpa**.

A validação do AOS-413 e do AOS-415 só passou porque existia uma segunda cópia do mesmo segredo,
criada à mão pelo operador em `0444` — legível por qualquer processo da máquina. A cópia era
byte a byte idêntica ao original (`cmp` deu igual) e foi **apagada** a 2026-09-20; a partir daí o
executor deixou de ter caminho para o segredo. Hoje o estado é este: ou o executor não arranca, ou
volta a existir um segredo do IdP legível por todos. Nenhum dos dois serve.

Isto contradiz uma convenção explícita do repositório — «nenhum segredo em texto claro: env var,
Vault, ou ficheiro montado em runtime» — no único ponto onde a convenção tinha de valer.

### Objectivo

Numa instalação limpa, o executor de nós obtém o segredo do cliente do IdP **sem que o segredo
fique legível por processos que não sejam o próprio `aos-orq`**, e sem um passo manual do operador
que crie uma cópia.

### Decisões a tomar primeiro (do dono)

1. **Por onde entra o segredo.** (a) Continua a ser ficheiro montado, mas com dono/modo que o uid
   do contentor lê e mais ninguém — `0400` com dono `65532`, posto pelo mesmo mecanismo que hoje
   instala os outros segredos; (b) passa a vir do Vault, como os segredos do nó, e o compose deixa
   de montar ficheiro nenhum. **(a)** é mais pequena e resolve o bloqueio medido; **(b)** alinha
   com o resto da postura mas arrasta o arranque do `aos-orq` para uma dependência nova.
2. **Se o `aos-orq` partilha o cliente `aos-reader` ou tem cliente próprio.** Hoje partilha o do
   E2E de leitura. Um cliente próprio torna a rotação independente e o alcance do segredo menor;
   partilhar é menos configuração.

### Critérios de aceitação

- [x] Numa instalação limpa, o `aos-orq` com o executor composto obtém um Bearer sem que o segredo
      fique exposto — **provado pela leitura real do ficheiro no arranque**, e não por afirmação.
      *(O critério dizia «sem que nenhum ficheiro esteja legível por outro utilizador que não o uid
      do contentor». **A premissa estava errada e foi emendada**: a fronteira do segredo é o
      DIRECTÓRIO `secrets/`, que o `bootstrap.sh` cria em 0700 — medido em produção,
      `drwx------ aos aos`. Um `0644` lá dentro não é legível por mais ninguém, e é a convenção que
      todos os outros segredos montados seguem.)*
- [x] Nenhuma cópia do segredo existe fora do caminho único
      *(a cópia `0444` da validação do AOS-415 foi apagada; o README deixa de sugerir cópias)*.
- [x] Um teste falha se uma credencial montada voltar a ficar ilegível pelo uid do contentor —
      `TestAOS416_SegredoIlegivelRecusaNoArranque` e `TestAOS416_NHIIlegivelRecusaNoArranque`;
      e `TestAOS416_ODirectorioDosSegredosEAFronteira` avermelha se o `secrets/` deixar de ser
      0700, que é a premissa em que a escolha de modo assenta.
- [x] O `deploy/server/README.md` descreve o caminho real, e a cópia manual desaparece dos passos.
- [~] Verificado em produção (`v0.1.27`): o ARRANQUE está medido nos três estados — ausente,
      ilegível e legível *(ver abaixo)*. O que falta é o Bearer **em uso**: a corrida positiva
      parou antes de o pedir, e essa metade continua **POR FAZER**.

### O que a revisão adversarial corrigiu, escrito para não se repetir

A primeira versão desta rota tinha **dois defeitos CRÍTICOS**, ambos com a mesma raiz: espelhou o
`handleSubmit` linha a linha e, ao fazê-lo, copiou passos cuja razão de ser não foi verificada no
destino. Nenhum foi encontrado pelos gates — todos estavam verdes — nem pela auto-validação.

| # | Defeito | Porque escapou |
|---|---|---|
| 1 | **A fila estava no espaço de nomes dos runs.** O stream chamava-se `plan.requests`, e o read-path de trajectória endereça streams POR `run_id`: `GET /runs/plan.requests/trajectory` servia a fila INTEIRA, ao vivo e por SSE, a um leitor de QUALQUER região — os objectivos de todos os tenants. Uma fila não tem residência selada, logo a verificação cross-region caía no ramo «run legado, sem check». O `run_id` também não era validado, pelo que `POST /runs {run_id:"plan.requests"}` injectava eventos de run dentro da fila | O ADR-028 §2.2 decidiu «o Event Store já é a fila» sem notar que, no nó, o espaço de nomes de streams **é** o espaço de nomes de `run_id` — e que esse espaço tem um read-path público |
| 2 | **Selava residência de um run que não criava.** O `POST /runs` sela porque VAI HOSPEDAR; esta rota selava sem criar nada e deixava o `run_id` LIVRE. Como a residência é fixada pelo PRIMEIRO registo e não é re-negociável, quem pedisse um plano primeiro fixava a fronteira de soberania de um run que OUTRA pessoa viria a criar: a vítima corria o run e recebia 404 no seu próprio resultado; a região do atacante lia-o | É **pior** do que o squat que o `POST /runs` já permitia: ali o id fica ocupado e a vítima não corre (negação de serviço); aqui a vítima corre e o conteúdo sai (exfiltração) |

Ambos estão fechados e com sensor — `aos417_soberania_test.go`, com as quatro mutações
verificadas (repor o nome do stream, repor o selo, remover o bloco de soberania, e tirar a
reserva do `POST /runs`) a produzir vermelho.

**Três sensores que faltavam**, e que valem mais do que os defeitos que apanharam:

- o bloco INTEIRO de soberania podia ser apagado sem uma única falha — os testes do ticket
  corriam com `readGov == nil`, pelo que a rota nunca era exercitada AUTENTICADA;
- remover a admissão deixava a suite COMPLETA do pacote verde;
- remover a chamada ao banner no composition-root idem.

A lição é a mesma das três: **copiar a forma de um handler é barato; copiar a justificação tem
de ser feito à mão.** Um passo cuja razão de ser não se verifica no destino não é defesa em
profundidade — é um efeito colateral por escrever.

### O defeito que só apareceu DEPOIS do merge, e o que ele ensina

Corrigido em `fix/AOS-417-nome-do-stream`. **O nome que a fila recebeu — `aos.internal/plan-requests`
— tornava a rota inutilizável sobre JetStream.**

O `stream_id` do AOS é livre, mas um subject NATS não é: o ponto separa tokens, e o
`jetstream.Store.subjectDe` **recusa** qualquer `stream_id` que o contenha — em vez de escapar em
silêncio para um subject vizinho onde outro stream leria os nossos eventos, que é a escolha certa.
O `Append` chama-o antes de tudo, pelo que o `POST /plans` respondia **`503` a todo o pedido** num
nó replicado. Medido a correr, não inferido:

```text
subjectDe("aos.internal/plan-requests") -> err=E_CONFIG: ... não é representável num subject NATS
subjectDe("aos-internal/plan-requests") -> subject="aos.aos-internal/plan-requests" err=<nil>
```

**O que o torna mais do que um erro de digitação.** O substrato de ficheiro NÃO arbitra entre
processos (DEF-282) e o JetStream é o único que arbitra — ou seja, o único substrato onde um
consumidor da fila pode sequer existir era exactamente aquele onde o ingresso não gravava. A rota
funcionava em tudo o que se mede hoje e não funcionaria na única topologia em que ela serve para
alguma coisa.

**Porque escapou a tudo.** Dez gates verdes, uma revisão adversarial que encontrou dois críticos, e
um smoke de dez passos — **todos correm sobre o substrato de FICHEIRO**. Não há teste de ingresso
sobre JetStream, e o defeito só apareceu na discovery do trabalho SEGUINTE — o consumidor da
fila —, quando alguém perguntou como é que ele alcançaria o stream.

A lição não é «falta um teste»: é que **uma superfície nova foi validada só na topologia
conveniente**, e a topologia que importa para o seu propósito nunca foi exercitada. O guard que
agora o impede (`TestAOS417NomeDoStreamERepresentavelNoNATS`) **lê a regra da fonte** em vez de a
repetir — duplicá-la daria um teste verde no dia em que a regra do NATS apertasse.

**RESÍDUO NÃO FECHADO, encontrado ao lado:** `approvalStream = "gov.approvals"`
(`packages/integration/approval_store_durable.go`) tem **o mesmo defeito** e é anterior a este
ticket — o que sugere que esta classe de streams nunca foi exercitada sobre JetStream. Não se
corrigiu aqui porque renomear um stream com histórico não é trocar uma constante: os factos já
escritos ficam no nome antigo. Precisa de ticket próprio.

### Resíduos DECLARADOS deste ticket — o que o ADR-028 §4 lhe atribuiu e não foi feito

Isto estava a faltar ao ticket, e a revisão apanhou-o: o ADR-028 §4 atribui explicitamente ao
ticket de implementação a **retenção e o tecto de pendentes**, e a primeira versão nem o fez nem
o declarou.

1. **Tecto de pendentes da fila e retenção — POR DECIDIR (do dono).** A fila não tem tecto, não
   tem retenção e não tem métrica de profundidade. A retenção do nó (`audit.RetentionConfig`)
   actua sobre partições WORM, não sobre streams do Event Store. **Não se inventou uma política**
   porque um tecto sem consumidor bloqueia a rota permanentemente ao fim de N pedidos, e decidir
   isso é escolher entre recusar pedidos novos e descartar antigos — uma decisão de produto. O
   que **foi** feito, por ser inequívoco: tecto de 16 KiB no objectivo (`maxObjetivoBytes`),
   porque um objectivo é uma frase e não um ficheiro, e sem ele cada pedido escrevia até ~1 MiB
   de texto untrusted no WAL e nos backups.
2. **O tecto de runs em curso NÃO se aplica a esta rota, e isso está agora escrito em vez de
   simulado.** A primeira versão copiou-o do `handleSubmit`: conta runs HOSPEDADOS, esta rota não
   hospeda nenhum, logo nunca disparava — uma guarda decorativa que fazia o código, a tabela de
   rotas e o banner afirmarem uma protecção inexistente.
3. **O objectivo fica em claro no log durável.** É texto livre e untrusted, sem titular, fora do
   alcance do crypto-shredding por-titular (AOS-093/AOS-217) que existe para tornar o
   apagamento do Art.º 17 possível por destruição de chave. **NÃO é regressão deste ticket** — o
   `payload` do Event Store é inline e em claro por desenho actual (`tecnica/13` §3.2, pendência
   §8.1) — mas esta rota passa a alimentar esse log com texto escrito por um utilizador final,
   que é um perfil de conteúdo diferente do que lá entrava.
4. **O schema do payload não está publicado.** O consumidor vive noutro módulo e não pode
   importar o tipo (`package main`): vai reescrever a struct à mão e nenhum gate liga as duas
   cópias. Mitigado com um campo de versão (`v`) e um teste que fixa as chaves JSON
   (`TestAOS417FormaDoFactoEEstavel`); publicar o schema em
   `packages/substrate/eventstore/schemas/`, como o envelope já tem, fecharia-o melhor e fica
   por fazer.

### Fora de âmbito, declarado

- **A renovação do NHI e o tecto de 45 minutos** (resíduo declarado no ADR-027): é a outra metade
  da credencial do executor, mas é outro eixo — o NHI é cunhado pelo operador por decisão do
  ADR-027, e mudá-lo exige decidir quem o renova. Não entra aqui, e continua sem ticket próprio.
- A rotação do segredo do cliente `aos-reader` no IdP, recomendada por ter existido uma cópia
  legível: é operação, não código.

### A migração do `gov.approvals`, e como se sabe que funcionou

**Desenho: copiar e cortar, numa só vez.** Os factos do nome antigo são copiados para
`aos-internal/gov/approvals` preservando `(RunID, StepID)`. A idempotency-key é
`run_id + ":" + step_id`; copiada verbatim, um `used-<id>` que existia no antigo bloqueia, no
novo, qualquer tentativa de reclamar o mesmo grant. **O uso-único atravessa a migração**, e é
essa a propriedade inteira.

Pela mesma razão a cópia é **idempotente**: re-corrê-la devolve `StatusDuplicate` em cada facto
e não duplica nada — o que a torna segura de pôr no arranque e retomável se falhar a meio.

**A ordem relativa preserva-se** (lê-se por `seq` ascendente, apende-se nessa ordem). Os `seq`
do stream novo são outros; o que os consumidores usam é a ordem — o `lookup` varre do fim para
o início e o `geracaoDe` conta ocorrências.

**Duas coisas que se verificaram antes de escrever o código, e que podiam ter invalidado tudo:**

- o `resume_records.go` passa o nome do stream para dentro da cifra. **Não é usado como dados
  autenticados**: o `SealContent` cifra só por titular e o `streamID` serve para ligar
  `subject→partição` no índice. A decifração sobrevive ao rename;
- ~~esse índice é reconstruído a cada arranque~~ **— ESTA AFIRMAÇÃO ERA FALSA, e a direcção
  é a contrária.** O `restoreSubjectIndex` filtra por `subjectOf`, que reconhece apenas
  `replay.captured` e `step.ledger.applied`; nenhum facto de aprovação é de uma dessas
  famílias, pelo que o índice não religa ao nome novo NEM ao antigo. A única ligação é feita
  ao vivo pelo `contentSealer`, em memória, e passa a apontar para o nome novo.
  **Consequência:** um legal hold DURÁVEL sobre a partição `gov.approvals` deixa de
  intersectar as partições de qualquer titular — **SUB-cobertura**, o fail-open que o AOS-352
  documenta como o pior dos quatro. A migração **não re-chaveia holds**: quem os tiver sobre
  `gov.approvals` tem de os repor sobre o nome novo. Encontrado por revisão adversarial, que
  o provou correndo o `restoreSubjectIndex` contra os dois streams (`ligou n=0`).

**Evidência, e não é só de teste unitário.** No smoke do `run-aos`, sobre um WAL persistido de
corridas anteriores: **53 factos copiados na primeira passagem, ZERO na segunda**, com o nó
composto a arrancar e os dez passos verdes nas duas. É a migração e a idempotência observadas
no produto, não em fixture.

Sete testes em `approval_stream_migracao_test.go`, com o controlo de não-vacuidade que importa:
um grant **por consumir** continua consumível depois da migração — sem ele, uma migração que
copiasse um `used-` para todos os grants passaria no teste central e partiria o produto.

### A migração dos streams de memória, e o que NÃO podia ficar para trás

O prefixo `memory.` formava as quatro classes e passou a `aos-internal/memory/`. Mesmo desenho
da migração das aprovações — copiar preservando `(RunID, StepID)` e a ordem — mas o facto que
não pode ficar para trás é **outro**, e vale a pena nomeá-lo:

**O TOMBSTONE.** Apagar uma memória é um evento NOVO (`memory.record.deleted`) e o `rebuild`
reconstrói o estado por replay: «written» fixa o registo, «deleted» remove-o. Se um tombstone
não atravessar, **o registo que ele apagava RESSUSCITA**. É o análogo do `used-` das aprovações.

**CALIBRAÇÃO, porque a primeira versão deste parágrafo exagerou:** dizia «uma memória que alguém,
possivelmente um `/dsar/erase`, mandou apagar», e **as duas metades estavam erradas**. O
`/dsar/erase` faz crypto-shred da KEK por-titular e o `subjectOf` não reconhece os eventos de
memória, pelo que o DSAR não alcança estes streams; e o `MemoryPort.Delete` **não tem um único
chamador** fora de testes, pelo que **não existe um tombstone em produção hoje**. O invariante
continua a ser o certo a preservar — é o que torna o apagamento possível quando alguém o
compuser — mas quem calibrasse a severidade pela frase antiga ficava com o número errado.

E o modo de falha desta camada é **SILÊNCIO**: o `rebuild` trata `ErrStreamNotFound` como «classe
vazia, não é erro». Sobre um backend que recusa o nome, a memória do nó não dava erro nenhum —
desaparecia.

**Uma coisa verificada antes de escrever o código:** o `StepID` do tombstone embebe o `seq` do
registo apagado (`<class>:del:<id>:<seq>`), e os `seq` mudam na cópia. **Não é problema** — o
`rebuild` obtém o id a apagar do PAYLOAD, nunca do `StepID`; o `seq` ali só torna a chave única
por apagamento.

**A cópia passou a viver no substrato.** A subtileza do `ErrConfig` — a que custou o defeito
CRÍTICO da migração das aprovações — não pode existir em duas cópias, porque é assim que um
defeito fechado volta. `eventstore.CopiarStream` concentra-a, e a migração das aprovações foi
refeita para a usar.

**E uma causa fechada, não só um sintoma.** O rename partiu QUATRO sítios que tinham o nome
antigo escrito à mão — incluindo o teste do AOS-426, que pela **segunda vez no mesmo ticket**
passou a medir streams MORTOS sem que nada avisasse. Em vez de corrigir os literais, exportou-se
`memadapters.StreamFor(class)`: quem precisa de nomear um stream de memória chama-o. Uma cópia
do valor deriva em silêncio; uma chamada não.

#### O que a revisão adversarial encontrou nesta migração

**Sem crítico** — o primeiro dos quatro ciclos desta série em que isso acontece. Sete eixos
foram verificados e estão limpos, entre eles os três que eu tinha nomeado como dúvidas: o helper
partilhado não perdeu nenhuma das cinco verificações da versão anterior, o rename não toca em
legal holds nem em crypto-shredding (e a afirmação falsa que a migração das aprovações teve
sobre o `restoreSubjectIndex` **não se repetiu**), e nenhum consumidor composto antes do ponto de
cablagem lê memória.

**ALTO — mudei o código e deixei o teste para trás.** A lição do `ErrConfig` foi movida para o
`CopiarStream`, mas o único sensor dela ficou no pacote `integration` — precisamente aquele cuja
constante legada está marcada para desaparecer. Medido: desligando o ramo do `ErrConfig`, as
suites do `eventstore` e da memória ficavam VERDES. No dia da limpeza, o defeito CRÍTICO voltaria
na terceira migração. Fechado com `substrate/eventstore/migracao_test.go` (sete testes),
incluindo o caso que **não tinha sensor em lado nenhum**: um `ErrConfig` na ESCRITA do destino
tem de propagar.

**MÉDIO — um duplicado com conteúdo diferente era descartado em silêncio.** O
`StatusDuplicate` era tratado como «já lá estava» sem comparar o payload: sem erro, sem
contagem, sem linha de log, com a migração a declarar-se bem-sucedida. O mesmo ficheiro recusa
`origem == destino` por essa razão exacta — o padrão estava aplicado a um eixo e não ao outro.
Passa a ser erro, com controlo de não-vacuidade para a idempotência não partir.

**MÉDIO — a ordem só se preserva DENTRO do conjunto copiado.** Contra o que já está no destino
é ordem de CHEGADA, e num consumidor last-write-wins isso INVERTE desfechos: um facto velho pode
ganhar a um recente, e um apagamento copiado tarde pode apagar uma escrita posterior. Provado em
dois cenários. Está agora escrito com precisão, e é o que motiva a postura de rollback.

**MÉDIO — nenhuma postura de rollback**, ao contrário da migração das aprovações. Acrescentada,
com as duas pernas separadas: a que está VIVA (um `Put` na janela de rollback ganha, por
last-write-wins, a escritas mais recentes) e a que está LATENTE (a ressurreição, que precisa de
haver tombstones).

**BAIXO — o sensor do AOS-426 mudou de categoria em silêncio.** Com a barra no nome novo, o
`{id}` da stdlib deixa de casar, pelo que o 404 das quatro classes passa a vir do ROTEAMENTO e
não da trava. Não é buraco (a trava tem teste próprio), mas o comentário que eu tinha escrito
— «torna a deriva impossível» — dizia mais do que se conseguiu. Corrigido.

**BAIXO — mutantes sobreviventes do envelope.** `RunID: ""` e `Producer{}` deixavam a suite de
memória verde. O `RunID` não é decorativo: é o que a trava do AOS-426 lê para classificar um
stream. Fechado, e os três mutantes morrem agora nos dois pacotes.

**Evidência:** no smoke, sobre um WAL persistido, **56 factos copiados na primeira passagem,
ZERO na segunda**, com o nó composto e os dez passos verdes nas duas. Sete testes, com o teste
central a medir pelo caminho REAL (o `Get` do adaptador em vigor, que é o que a MemoryPort usa)
e o controlo de não-vacuidade que importa: um registo NÃO apagado continua legível e com o
conteúdo certo — sem ele, uma migração que copiasse um tombstone para tudo passaria no teste
central e apagaria a memória inteira do nó. Duas mutações verificadas: não copiar tombstones, e
migrar só uma das quatro classes.

#### O que a revisão adversarial encontrou, e que os gates não viam

**CRÍTICO — a migração impedia o nó de arrancar sobre JetStream. Para sempre.** O
`MigrarAprovacoes` só tolerava `ErrStreamNotFound`. Sobre JetStream o `Read` do nome legado
devolve **`ErrConfig`** — o `subjectDe` recusa o ponto LEXICALMENTE, antes de tocar na rede — e
o `Bootstrap` abortava. **Um nó JetStream com four-eyes não arrancava, tivesse ou não factos
legados**, porque a recusa é do NOME e não do stream: até um nó fresco falhava.

Era o **inverso exacto do propósito do ticket** — a migração que existe para destrancar o
four-eyes sobre JetStream era a única coisa que o impedia de correr lá. E o smoke não o via
porque corre sobre WAL de ficheiro: **o único substrato onde isto importa era o único onde não
foi medido.**

Fechado tratando o `ErrConfig` do nome LEGADO como «nada para migrar», com âmbito estreito. Não
é tolerância a erro: o `Append` e o `IngestStream` passam pelo MESMO `subjectDe`, logo um stream
com ponto **nunca pôde receber uma escrita** nesse backend, nem por restauro de backup — «não
consigo ler o nome legado» e «o nome legado não tem factos» são, ali, a mesma afirmação. Um
`ErrConfig` na ESCRITA do nome novo continua a abortar.

**ALTO — nenhum dos sete testes olhava para o CORPO dos factos.** A revisão mutou a cópia para
`Payload: nil` e a suite INTEIRA do pacote passou. É o sobrevivente mais perigoso possível: a
propriedade de SEGURANÇA continua a valer (os `used-` bloqueiam) e os DADOS desaparecem todos —
e como o `Consume` reclama ANTES de ler, cada grant seria QUEIMADO e só depois se descobriria
ilegível. Fechado com um teste que usa o caminho REAL nas duas pontas (o wire do `Put`, o
`Consume` da store em vigor) e verifica a preview, os aprovadores, o dual-control e a validade.

**ALTO — a cablagem não tinha sensor.** Substituir a chamada por `copiados, merr := 0, nil`
deixava a suite do `cmd/aos` verde. Fechado com `aos424_migracao_cablagem_test.go`, que exige
que a chamada exista, que PRECEDA os três consumidores do stream, e que seja fail-closed. Duas
mutações verificadas: remover a chamada e movê-la para depois da store.

**ALTO — o rollback reabre o duplo-consumo, e não estava declarado.** Um grant migrado e
consumido pelo binário novo tem o `used-` só no stream novo; o binário antigo lê o legado, não
o encontra, e o grant volta a ser consumível. Além disso um facto escrito pelo binário antigo
durante a janela pode vir `StatusDuplicate` no roll-forward e não atravessar — em silêncio.
**Declarado** no cabeçalho da migração: o rollback depois desta migração não é seguro para a
cerimónia, e não há código que o torne seguro — um log append-only não desfaz.

**E um teste meu que passava pela razão errada**, apanhado pela minha própria mutação ao
verificar as correcções: o sensor do `SchemaVersion` usava `"1.0"`, que é **o default que o
store preenche quando o campo vem vazio** — apagar a cópia era indistinguível de a preservar.
Corrigido para uma versão não-default, e o mutante passa a morrer.

**O que esta migração NÃO garante, declarado:** assume **um único escritor** durante a cópia. O
substrato de ficheiro impõe-o (`LockWAL`) e é o que corre em produção. Sobre `--nats` com várias
réplicas, um binário ANTIGO ainda a escrever no nome antigo depois da cópia deixa um facto por
copiar — e se for um `used-`, abre-se a janela de duplo-consumo. **Todas as réplicas têm de
estar no binário novo** antes de a garantia valer.

O stream antigo **não é apagado** (um log append-only não apaga): fica inerte. A constante
`approvalStreamLegado` continua na baseline do gate, com natureza diferente — não é um stream
em uso, é o nome que a migração precisa de LER. Sai quando nenhuma implantação tiver factos por
migrar.

### Riscos

| Risco | Mitigação |
|---|---|
| Mudar dono/modo de um segredo em produção parte outro consumidor do mesmo ficheiro | Verificar quem mais monta o `reader-client-secret` antes de tocar; o E2E de leitura usa-o |
| A correcção volta a ser um passo manual do operador, e o próximo instalador repete o erro | O critério de aceitação exige um sensor que falhe, não documentação |

### Estado

**IMPLEMENTADO** (2026-09-20), verificação em produção por fazer.

**Decisões do dono, tomadas:** (1) **ficheiro montado**, não Vault — a via do Vault arrastaria o
arranque do `aos-orq` para uma dependência nova e não é exercitável neste ambiente; (2) **o cliente
`aos-reader` continua partilhado** — um cliente próprio exige criá-lo no Keycloak, que é acção do
operador no servidor. As duas ficam reversíveis.

**A correcção que importa não é o modo do ficheiro: é o arranque passar a LER a credencial.** O
banner do executor decidia por `cli == nil` e `cli.bearer != nil`, e com o segredo ilegível
imprimia na mesma `COMPOSTO`; a falha aparecia na PRIMEIRA SUBMISSÃO de nó — o modo de falha do
AOS-413 (o plano despacha, nada executa) a voltar por outra porta. Um `os.Stat` teria passado por
cima do defeito, porque existir não é o mesmo que ser legível.

A verificação cobre **as duas** credenciais montadas. O `AOS_ORQ_NODE_CREDENTIAL_FILE` (o NHI do
run) tinha o mesmo defeito, o mesmo uid e o mesmo sintoma, e é *mais* provável estar mal: o
operador copia-o à mão com `umask 077`, o que dá `0600` do utilizador dele. Fechar uma porta e
deixar a outra aberta na mesma parede não fecharia nada.

**Uma premissa deste ticket estava errada, e a revisão adversarial apanhou-a.** A primeira versão
recusava em produção qualquer ficheiro com bits de grupo ou de outros, por entender que `0644`
punha o segredo «ao alcance de qualquer processo da máquina». **É falso neste deployment**:
`bootstrap.sh` cria `secrets/` com `install -d -m 700` e o `provision.sh` reforça-o — medido em
produção, `drwx------ aos aos`. Sem travessia do directório, o modo do ficheiro lá dentro não abre
nada a ninguém. A regra teria recusado a configuração CORRECTA e foi removida.

**E o `chown 65532` que a primeira versão instalava partia produção de três maneiras**, todas
verificadas no servidor:

| O que partia | Evidência |
|---|---|
| O **backup nocturno** | `backup.sh` corre no cron do `aos` (`17 3 * * *`, medido) e tara o `secrets/` inteiro; com o ficheiro em `0400` do 65532 o `tar` falha — e o `2>/dev/null` do script engole a única linha que o explicaria |
| O **próprio provisionamento** | `provision-identity.sh` corre como `aos`, que não tem sudo (medido): o `chown` falharia, o `|| fail` mataria o script, e os passos 5 e 6 nunca correriam |
| A **instalação real** | o `chown` estava dentro do guard `[[ ! -s ]]`, que numa instalação existente é falso — ou seja, nunca tocaria no ficheiro com o defeito |

O provisionamento passou a `chmod 644`, **fora do guard**, para reparar também as instalações
anteriores ao ticket.

**Falha-antes medida em LINUX, como uid 65532** — não em Windows, onde os bits POSIX não são
significativos e três destes testes saltariam. O binário de teste foi compilado para `linux/amd64`
e corrido sob `setpriv --reuid=65532 --regid=65532 --clear-groups`, que é a identidade real do
contentor. Com a validação removida:

```console
--- FAIL: TestAOS416_SegredoIlegivelRecusaNoArranque
--- FAIL: TestAOS416_NHIIlegivelRecusaNoArranque
--- FAIL: TestAOS416_CredencialAusenteDizQueEstaAusente
--- FAIL: TestAOS416_CredencialVaziaERecusada
```

`TestAOS416_OModoDaConvencaoNaoERecusado` é **controlo positivo** e passa dos dois lados por
desenho: é ele que mata a versão recusada acima, porque «recusa sempre» satisfaria os outros
quatro.

**O sensor do provisionamento amarra-se à cadeia executável, não à prosa.** A primeira versão fazia
`grep` do texto do script e sobrevivia a **comentar** a linha que verifica — a revisão demonstrou-o.
É a mesma disciplina que o `scripts/ci/deploy-gate-lint.sh` já tinha escrito para si próprio.

**O que o banner prova, e o que não prova:** o arranque lê as credenciais, por isso «do ficheiro
montado» deixou de ser promessa. Não prova autenticação — um segredo legível mas obsoleto (rodado
no IdP, ficheiro por actualizar) dá `401` na primeira submissão, e o segredo relê-se a cada chamada
de propósito, para que rodá-lo não exija reiniciar.

**Âmbito que NÃO foi alargado, e fica nomeado:** o `model-api.key` e o `vault-token` estão em
`0644` — o que, dentro de um `secrets/` em 0700, é a postura coerente e não um defeito. O que
*seria* defeito é o directório afrouxar, e é isso que o
`TestAOS416_ODirectorioDosSegredosEAFronteira` passa a vigiar.

**Verificado em produção a 2026-09-20** (`v0.1.27`, imagem `sha256:fe6f363e…`), com o
`chmod 644` aplicado ao ficheiro vivo pelo operador. Os três estados do arranque, medidos no
servidor com o `aos-orq` real:

| Estado da credencial montada | O que o arranque fez |
|---|---|
| **Ausente** (`AOS_ORQ_NODE_CREDENTIAL_FILE` a apontar para um caminho inexistente) | recusou: «está configurado mas o ficheiro NÃO existe — sem ele o executor não fala com o nó» |
| **Presente e ILEGÍVEL** pelo uid do contentor (`-rw------- aos aos`, criado com `umask 077`) | recusou com **saída 1**: «o ficheiro existe mas este processo NÃO o consegue ler. O contentor corre como uid 65532: no host, `chmod 0644 …` … NÃO faça uma cópia do ficheiro» |
| **Legível** (`-rw-r--r--`, as duas credenciais) | compôs, e o banner declarou `executor de nos (AOS-413/AOS-414, ADR-027): COMPOSTO` |

O caso do meio é a **reprodução exacta do defeito que originou o ticket**: é o mesmo estado que em
produção fazia o `aos-orq` anunciar `COMPOSTO` e falhar só na primeira submissão de nó. Agora é
recusado à cabeça, com o gesto na mensagem.

**O que isto NÃO prova, e é preciso dizer:** o Bearer **nunca chegou a ser pedido**. A corrida
positiva parou antes, em `--goal exige --snapshot`, e a contagem de `401`/«token do IdP» no log deu
**zero**. Ou seja, está provado que o arranque deixou de mentir; **não** está provado que o token
funciona contra o IdP. Fechar essa metade é a validação completa do executor, e exige um NHI
cunhado pelo operador (tecto de 45 min) mais os aprovadores do gate.

**Mudança de comportamento em produção, declarada:** a partir da `v0.1.27` o `serve` RECUSA
arrancar com uma credencial montada ilegível, onde antes arrancava e falhava mais tarde. O
`chmod 644` no `secrets/reader-client-secret` foi feito no mesmo deploy; sem ele o executor teria
ficado indisponível. O nó `aos` não é afectado — só o `aos-orq`, que corre por invocação.

**Por verificar:** o `restore-drill.sh` extrai o bundle sem `-p` e como não-root, pelo que a
ownership arquivada é ignorada e o modo passa a ser o do umask de quem extrai — **não verificado**
se um restauro repõe um modo que o contentor não lê. E, da mesma release, o **AOS-359** e o
**AOS-411** continuam sem verificação em produção: o AOS-411 só se observa num ciclo de
`AOS_CRASH_RESUME_INTERVAL` no log do nó.

---

## AOS-417 — Por onde entra um objectivo no caminho do plano: o orquestrador não tem superfície de rede

<!-- Este ticket IMPLEMENTA o ADR-028 (o seu próprio), e por isso o marcador
     `rtm: adrs-mencionados` SAIU: enquanto lá esteve, o ref-lint tratava TODAS as citações do
     bloco como menções, e o ADR-028 ficaria sem ticket implementador — gate vermelho. O preço
     de o tirar é que o ADR-018, o ADR-023 e o ADR-027, que aqui são RESTRIÇÕES e não entregas,
     passam a contar como implementados por este ticket na RTM. Fica dito porque o parser é
     textual e o marcador é tudo-ou-nada: não há forma de separar os dois papéis no mesmo bloco. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orquestração |
| Fase | Prontidão para utilizadores reais |
| Milestone | v1.1 |
| Tipo | decisão de arquitectura (ADR) + implementação |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | ADR-018, ADR-023, ADR-027 (restrições, não pré-requisitos) |
| Bloqueia | AOS-133 (BFF) e, por arrasto, todo o EPIC-13; qualquer uso do caminho do plano sem operador |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `deploy/server/docker-compose.prod.yml` (serviço `aos-orq`, `profiles: ["orq"]`), `packages/cmd/aos-orq/main.go` (subcomandos `serve|inspect|plans|decide`), `packages/cmd/aos/planos.go` (a tabela de rotas do nó), `docs/adr/ADR-023-*.md`, `docs/adr/ADR-018-*.md` |

### Contexto

O caminho do **agente único** já é utilizável sem operador: `POST /runs` aceita um objectivo em
linguagem natural e autentica-se por `client_credentials`, que é automatizável.

O caminho do **plano multi-nó não tem superfície de rede nenhuma.** Medido: `ListenAndServe` e
`http.Server` em `packages/cmd/aos-orq/`, fora de testes, devolvem **zero ocorrências**. O
`aos-orq` é só CLI, e o compose exclui-o deliberadamente do arranque:

```yaml
profiles: ["orq"]
restart: "no"
# está no profile `orq` para que o deploy.sh (up -d) NUNCA o arranque:
# um `serve` possui um run e termina, não é um daemon
```

Não existe agendador, fila, nem caminho em que um pedido de utilizador desencadeie um `serve`.
Os únicos temporizadores do deploy são a sincronização de TLS e a recolha de backups; nenhum
toca no orquestrador.

**A consequência mede-se nos passos manuais.** Uma corrida com nós executados em produção exige
hoje, por esta ordem de bloqueio:

| # | Passo manual |
|---|---|
| 1 | invocar `aos-orq serve` à mão no servidor (`docker compose --profile orq run --rm`) |
| 2 | não há UI nenhuma — tudo é `curl`, `docker compose` e PowerShell |
| 3 | cunhar o NHI na máquina do operador, copiá-lo para o servidor, e apagá-lo no fim |
| 4 | **dois** logins no browser no caminho de produção (delegação + chamada) |
| 5 | para um plano de risco, assinar a decisão numa terceira máquina e copiar o ficheiro |
| 6 | preparar o snapshot pinado à mão, com nomes de tool que coincidam com os do nó |

Os passos 2 a 6 podem ser atacados isoladamente e continuariam a não dar um produto utilizável,
porque **o passo 1 permanece**: alguém tem de estar no terminal do servidor. O EPIC-13 já o diz
à sua maneira — «o bloqueador duro é a dívida de wiring de backend, não o frontend» —, e o
AOS-133 (o BFF) não tem o que chamar para o caminho do plano.

**Não existe ticket que cubra isto.** Varridos os 25 epics.

### Objectivo

Um objectivo submetido por um utilizador autenticado desencadeia uma corrida do caminho do plano
**sem que ninguém esteja num terminal do servidor**.

### Porque é que isto é um ADR e não só um ticket

A frase «um `serve` possui um run e termina, não é um daemon» não é um acaso de operação: é o
**ADR-023** a manifestar-se — a autoridade sobre o ciclo de vida de um run é o LEASE, e um
processo que o detém não é partilhável. Dar ingresso de rede ao caminho do plano obriga a decidir
coisas que o ADR-023 e o ADR-018 hoje respondem por omissão, e que não se decidem em código:

- **Quem detém o lease** quando o pedido chega por rede — o processo que atende, ou um trabalhador
  que ele desencadeia?
- **O que acontece a um segundo pedido** para um run que já tem posse: recusa (o actual código 3),
  fila, ou coalescência?
- **Onde vive o ingresso** — no nó `aos`, que o ADR-018 declara única autoridade do ciclo de vida
  e que o `layer-lint` impede de importar o orquestrador; ou num serviço próprio que fala com o nó
  como o executor já fala (ADR-027)?
- **O modelo de execução**: daemon que aceita e executa, ou ingresso que só ENFILEIRA e um
  trabalhador consome? A segunda preserva melhor «um `serve` possui um run», mas introduz uma fila
  durável que hoje não existe.

### Decisões a tomar primeiro (do dono)

1. **Onde vive o ingresso.** (a) Rota nova no nó `aos`, que enfileira e um trabalhador do
   `aos-orq` consome — mantém uma só porta de entrada e reaproveita a autenticação que já existe,
   mas o nó passa a conhecer a existência do caminho do plano; (b) serviço próprio do `aos-orq`
   com porta própria, atrás do mesmo edge — não mexe no nó, mas duplica autenticação, admissão e
   observabilidade.
2. **Daemon ou fila.** Aceitar-e-executar no mesmo processo é mais simples e contradiz
   frontalmente a nota do compose; enfileirar preserva-a, ao custo de uma fila durável nova.
3. **O que fazer a um pedido cujo run já tem posse** — recusar, enfileirar, ou devolver o estado
   do run em curso.

### Critérios de aceitação

- [x] Um **ADR novo** regista a decisão, cita o ADR-018/023/027 e diz explicitamente o que
      SUPERA ou EMENDA da nota «não é um daemon» — ou porque não a contradiz.
      *(ADR-028, aceite 2026-09-21. Não emenda nada: o ingresso enfileira e não executa, pelo
      que um `serve` continua a possuir um run e a terminar.)*
- [ ] Um utilizador autenticado submete um objectivo por rede e obtém um identificador com que
      acompanha a corrida, **sem sessão no servidor**.
      *(**METADE FEITA, e a metade que falta é a que conta para o utilizador.** A submissão por
      rede existe — `POST /plans` aceita o objectivo, autentica pela mesma credencial forte do
      `POST /runs` e devolve o identificador. O que NÃO existe é quem consuma a fila: o pedido
      fica gravado e espera, e `acompanha a corrida` não é hoje verdade porque corrida nenhuma
      começa. O banner de arranque di-lo por palavras nessas — ver o critério do banner abaixo
      — em vez de deixar o operador descobri-lo a meio. O trabalhador do `aos-orq` é o passo
      seguinte, e está bloqueado numa decisão do dono: retenção e tecto da fila, declarados
      como residuais no ADR-028.)*
- [x] Um segundo pedido para um run com posse tem o desfecho decidido em (3), e há teste que o
      fixa — não é comportamento acidental do lease.
      *(Decidido no ADR-028 §2.3 e imposto por DOIS testes, porque são dois casos distintos e a
      primeira versão destes testes só cobria o segundo: `TestAOS417PedidoParaRunComPosse`
      hospeda um run REAL, espera que ele entre no modelo e fique com lease, e só então pede
      o plano — é este o caso do critério. `TestAOS417PedidoRepetidoNaoDuplicaNemRevela`
      cobre o pedido repetido, que é outra coisa: a dedup da fila é por `pedido-deste-run`,
      não por posse, pelo que o desfecho certo saía por coincidência de nomes até o primeiro
      teste existir. Este segundo assere que
      `201 accepted` idempotente, **nunca** o estado do run. O teste assere as DUAS metades — a
      resposta indistinguível byte-a-byte E um só facto na fila — porque cada uma sozinha deixa
      passar um defeito diferente: só o código deixa passar a gravação em duplicado, só a
      contagem deixa passar um `409` que seria um oráculo de existência.)*
- [x] O `layer-lint` continua verde: se a opção for (a), o nó **não** importa o orquestrador.
      *(A opção foi (a). Dos pacotes do repositório, o `plan_ingress.go` importa apenas
      `substrate/eventstore` (mais `encoding/json`, `net/http` e `strings` da stdlib); o
      guard-test de fronteira do ADR-018 não foi tocado. Medido com `go list -deps` sobre
      `packages/cmd/aos`: zero `orchestrator`/`scheduler`, directo ou transitivo. E há prova pelo COMPORTAMENTO, não só
      pelos imports: `TestAOS417IngressoNaoHospedaORun` falha se a rota hospedar o run.)*
- [x] O banner de arranque declara a postura do ingresso, como o resto do sistema já faz.
      *(`planIngressPostureBanner`, com sensor: `TestAOS417BannerDeclaraAPosturaReal` fixa o que
      cada postura tem de dizer, e `TestAOS417BannerDoConsumidorNaoApodrece` varre a árvore do
      `aos-orq` e fica VERMELHO no dia em que alguém lá nomear o stream da fila — que é
      exactamente o instante em que a linha passaria a mentir. Sem ele, o literal `false` do
      composition-root sobreviveria ao consumidor, porque quem escrever o consumidor não passa
      por `bootstrap.go` (é outro módulo). Molde: `aos255_budget_scope_test.go`, que existe
      pela mesma razão. Declara três coisas separadas porque falham de maneiras
      diferentes: se há substrato onde gravar, se ele é durável, e — a que importa hoje — que
      **ninguém consome a fila ainda**, pelo que um `201` significa «o pedido está durável» e
      não «a corrida começou».)*
- [ ] Verificado em produção: uma corrida desencadeada por rede, sem ninguém no terminal.
      *(**NÃO VERIFICÁVEL AINDA, e não por falta de acesso:** sem consumidor não há corrida que
      se desencadeie. O que se pode medir hoje em produção é estritamente menos do que este
      critério pede — que a rota aceita, grava e deduplica — e mede-se lendo o stream
      `plan.requests`. Deixa-se por marcar de propósito: marcar com a medição menor seria
      trocar o critério por outro mais fácil.)*

### Fora de âmbito, declarado

- **A cunhagem automática do NHI** (passos 3 e 4). É a segunda maior barreira, mas é uma decisão
  de SEGURANÇA — a `issuer.key` não vai para o servidor por desenho — e não se resolve com
  ingresso. Continua sem ticket próprio.
- **A UI** (passo 2). Fica desbloqueada por este ticket, mas é o EPIC-13.
- **A cerimónia de aprovação** de planos de risco (passo 5): o custo manual ali é o desenho do
  AOS-408, não um defeito.

### Riscos

| Risco | Mitigação |
|---|---|
| Um ingresso que aceite e execute no mesmo processo ressuscita o problema de dois escritores que o ADR-023 fechou | O ADR tem de responder «quem detém o lease» antes de existir código |
| Duplicar autenticação e admissão num serviço próprio abre uma segunda superfície com postura diferente da do nó | Se for a opção (b), reaproveitar a mesma admissão e o mesmo edge, e prová-lo com teste |
| O ingresso torna trivial disparar corridas, e o custo do modelo deixa de ter quem o trave | O orçamento por árvore já existe (AOS-027); verificar que o caminho novo passa por ele |

---

## AOS-426 — O read-path dos runs servia treze streams internos do nó, incluindo aprovações, memória e nonces de ratificação

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa ADR nenhum: fecha uma exposição de leitura. As citações ao
     ADR-016 (o canal não é oráculo de existência) e ao ADR-011 são RESTRIÇÕES que a correcção
     tem de preservar — nomeadamente o 404 uniforme. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orquestração (por proximidade ao AOS-424/425, onde a classe foi descoberta; o âmbito que toca é o read-path soberano do EPIC-09) |
| Fase | Prontidão para utilizadores reais |
| Milestone | v1.1 |
| Tipo | correcção — exposição de leitura |
| Prioridade | **P0** |
| Estimativa | S |
| Dependências | descoberto a partir do AOS-424; **não depende dele** — a correcção aqui não precisa de renomear nada |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos/streams_internos.go` (a trava e a sua justificação), `packages/cmd/aos/trajectory.go`, `packages/cmd/aos/sovereign_replay.go`, `packages/cmd/aos/aos426_streams_internos_test.go` |

### Contexto

O Event Store tem **um** espaço de nomes de streams, e o `run_id` de um run **é** o seu stream.
As rotas de leitura por-run endereçam esse espaço directamente a partir do URL:

```text
GET /runs/{id}/trajectory   ->  Read(ctx, id, ...) + Subscribe(Streams: [id])
GET /runs/{id}/reconstruct  ->  Read(ctx, id, 1)
```

O padrão da stdlib casa `{id}` com UM segmento de caminho. Logo **qualquer stream interno do nó
cujo nome não contenha barra era endereçável por estas rotas** — e ambas serviam qualquer stream
que EXISTISSE, porque a única guarda era o 404 para o stream INEXISTENTE.

### O que foi medido

A 2026-09-21, com o **gate soberano composto** e um leitor **autenticado de OUTRA região**,
`GET /runs/<stream>/trajectory` devolvia `200` e servia o conteúdo de treze streams internos:

| Stream | O que guarda |
|---|---|
| `gov.approvals` | Grants de aprovação four-eyes, pendentes por decidir, e os **registos de retoma**, que carregam o `Goal` |
| `memory.episodic` · `memory.semantic` · `memory.procedural` · `memory.working` | A memória do nó (compostas em produção, `bootstrap.go` (composição da MemoryPort; a linha mudou com esta entrega)) |
| `memory.semantic.knowledge` · `memory.episodic.trajectories` · `memory.migrations` | Idem (sem compositor hoje) |
| `identity` | Eventos de identidade NHI |
| `registry` | Registo de artefactos |
| `lease:<run>` | Posse de run |
| `ratify-nonce:<escopo>:<hex>` · `4eyes-challenge:<escopo>:<hex>` | **Primitivos de frescura e anti-replay da ratificação humana** |

**Um leitor SEM credencial recebia `404`** — o alcance era de quem já tem credencial válida. O que
NÃO se aplicava era a fronteira de REGIÃO: um stream interno não tem residência selada, pelo que
a verificação cross-region caía no ramo «run legado, sem check» (retro-compatibilidade do
AOS-182) e servia. **O leitor US leu tal como o EU.**

Oito streams estavam seguros — os do scheduler e a fila de pedidos de plano — e o discriminador
era um só: **têm barra no nome**. A barra estava lá para namespacing; a protecção veio de lambuja.

**NÃO É LATENTE.** Ao contrário do AOS-424/425, isto não depende do JetStream nem de migração
nenhuma: mede-se no substrato de FICHEIRO, que é o que corre em produção.

### Como foi encontrado, e porque é que isso importa

A revisão adversarial do **AOS-417** encontrou exactamente este defeito **numa superfície NOVA**
— a fila de pedidos de plano, que ficou corrigida com o prefixo `aos-internal/`. Ninguém
perguntou, na altura, se as superfícies ANTIGAS tinham o mesmo. Tinham, há muito mais tempo.

A lição é de método: **quando uma revisão encontra um defeito de forma numa superfície nova,
a pergunta seguinte é sempre se a forma é partilhada.** Aqui era, e só se viu três tickets
depois.

### A correcção, e porque não é uma lista de nomes proibidos

O read-path dos runs serve **runs**, e isso passa a ser um **facto positivo lido dos dados**: num
stream de run os eventos declaram o run a que pertencem, e esse run é o stream. Um stream interno
não satisfaz isto — e não por convenção de nomes, mas porque os seus eventos pertencem a outra
coisa:

- `gov.approvals` grava com o `RunID` SINTÉTICO `approval` (é a fila de aprovações);
- `memory.semantic` grava com o `RunID` do run que ESCREVEU a memória, enquanto o stream é a
  CLASSE;
- `lease:<run>` grava a posse do run `<run>`, e o stream é `lease:<run>`, não `<run>`.

Uma lista de nomes internos seria um conjunto **ABERTO**: o stream interno seguinte nasceria
servível e ninguém seria avisado — o mesmo modo de falha que o `planos.go` fechou para as rotas
(«uma rota só existe se estiver registada»). Com a regra lida dos dados, **um stream interno novo
fica coberto no dia em que nasce**, sem ninguém declarar nada.

O status é o MESMO `404` uniforme do run desconhecido: um código próprio diria ao chamador
«este stream existe mas não é teu», que é o oráculo de existência que o ADR-016 fecha.

### Critérios de Aceitação

- [x] Os treze streams expostos respondem `404` e não servem conteúdo, nas DUAS rotas que leem o
      store por id. *(`TestAOS426ReadPathNaoServeStreamsInternos`, que enumera os 21 — os treze
      expostos e os oito que a barra já protegia, para que a protecção deixe de depender dela.)*
- [x] Um run LEGÍTIMO continua a ser servido ao seu leitor. *(`TestAOS426RunLegitimoContinuaAServirTrajectoria`
      — sem esta metade, uma trava que recusasse toda a gente passaria no critério acima.)*
- [x] A decisão sai dos DADOS e não do nome. *(`TestAOS426TravaDecidePelosDadosENaoPeloNome`: o
      mesmo nome de stream é servível ou não consoante os eventos declararem pertencer-lhe.)*
- [x] O sensor foi verificado: removida a trava, o teste acusa **26 falhas** (13 streams × 2
      asserções — status e corpo).
- [x] A não-oracularidade é preservada: `404` uniforme, nunca um código próprio.

### O que isto custa, declarado

Um run cujo `run_id` colida com um stream interno — hoje possível, porque o `POST /runs` **não
valida o `run_id`** (eixo do AOS-424) — deixa de ser legível por estas rotas. **É a consequência
pretendida:** um run que partilha stream com o interior do nó já estava a misturar os seus eventos
com os dele, que é um problema pior do que não o conseguir ler.

### Resíduos declarados

- **O `handleGet` (`GET /runs/{id}`) já dava `404`** para estes nomes, porque não consulta o
  store — resolve por estado local do serviço. Não foi tocado.
- **A exposição existiu.** Este ticket fecha-a; não diz nada sobre se foi explorada. Avaliar isso
  exige os logs de acesso de produção e **não foi feito aqui**.
- **A causa de fundo — streams internos a viverem no mesmo espaço de nomes dos runs — continua
  aberta**, e o fim-de-linha limpo é movê-los todos para o prefixo reservado `aos-internal/`. Isso
  é o AOS-424, e tem o custo de renomear streams com histórico. Esta trava **não** o dispensa: é a
  defesa que não obriga a migrar dados.

### Estado

**FECHADO.** Trava composta nas duas rotas, com teste que enumera os 21 streams, controlo de
não-vacuidade e sensor verificado por mutação. Suite do pacote verde com `-race`.

---

## AOS-425 — Metade do espaço de nomes de streams é composto em runtime, a partir de valores que ninguém valida

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa ADR nenhum: fecha a metade da classe do AOS-424 que não é
     alcançável por renomear constantes. As citações ao ADR-007 e ao ADR-011 são RESTRIÇÕES. Se
     a decisão (2) levar a validar na carga da POLÍTICA, isso é desenho de fronteira e pode
     exigir ADR — o marcador sai nesse caso. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orquestração (por proximidade ao AOS-424; o âmbito que toca é o do EPIC-03 e do EPIC-06) |
| Fase | Prontidão para utilizadores reais |
| Milestone | v1.1 |
| Tipo | correcção de classe (fronteiras de entrada) |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | **AOS-424** — a decisão (1) de lá determina se este ticket encolhe ou muda de natureza; AOS-100/101 (Event Store replicado) |
| Bloqueia | a migração para JetStream, em conjunto com o AOS-424 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/control-plane/scheduler/{admission,quota}.go`, `packages/platform/model-gateway/policy/allowlist/allowlist_policy.json`, `packages/control-plane/governance/hitl/{challenge_issuer,nonce_store}.go`, `packages/substrate/eventstore/jetstream/store.go` (`subjectDe`) |

### Contexto

O AOS-424 inventariou os `stream_id` **literais** que não são representáveis num subject NATS, e
esses corrigem-se a renomear constantes. Este ticket é a outra metade, e não se corrige assim:
**os nomes de stream compostos em RUNTIME, a partir de valores que entram no sistema por
configuração, por ficheiro de política ou por token externo.**

Um grep de literais não os vê. Só aparecem quando o valor que os alimenta muda — e aí já estão
em produção.

### A tese, e o que a torna concreta

O exemplo que a mostra inteira é a admissão de quota. Medido:

```text
scheduler/admission.go:64   bucketStreamPrefix = "admission/bucket/"
scheduler/quota.go:27       ProviderKey.String() = Provider + ":" + Model + ":" + Region
scheduler/admission.go:463  bucketID := bucketStreamPrefix + keyStr
scheduler/admission.go:575  a.log.Append(ctx, bucketID, ...)
```

O `stream_id` de admissão **contém o nome do modelo**. E o nome do modelo não é escolhido por
quem escreve código: vem da allowlist **assinada** em
`platform/model-gateway/policy/allowlist/allowlist_policy.json`.

**Hoje isto NÃO é um defeito vivo, e foi medido:** a allowlist em vigor para `board-eu` traz
`gpt-4o`, `gpt-4o-mini` e `text-embedding-3-large` — **nenhum tem ponto**. A barreira que segura
esta linha é a política assinada, não o código.

**E é exactamente isso o problema.** `gpt-4.1` é um nome de modelo real. Acrescentá-lo à allowlist
— uma alteração de POLÍTICA, revista por quem revê política, assinada e promovida como política —
partiria os streams de admissão sobre JetStream. **Um ficheiro de política e o espaço de nomes do
substrato estão acoplados, e nada no repositório diz que estão.** Ninguém que revê aquele JSON
tem razão nenhuma para saber que está a mexer em nomes de stream.

### Inventário dos sítios de composição

Proveniência: varredura de 2026-09-21. As duas primeiras linhas foram **confirmadas por leitura
directa**; as restantes vêm da varredura e estão marcadas como tal.

| Risco | Composição | Onde | De onde vem o valor |
|---|---|---|---|
| **ALTO** | `admission/bucket/<provider>:<model>:<region>` e `admission/audit/…` | `scheduler/admission.go:463,679,956`; `quota.go:27-29` | **Allowlist assinada.** Hoje sem pontos (medido); um modelo novo pode trazer um |
| MÉDIO | `plan_id` | `orchestrator/plannerevents/recorder.go:98`; `runlifecycle/emitters.go:108` *(varredura)* | `planner.go:410` usa `req.RunID` quando vazio — herda o que o AOS-424 fechar para o `run_id` |
| MÉDIO | `4eyes-challenge:<scope>:<hex>` | `hitl/challenge_issuer.go:175-176` *(varredura)* | `scope` é o `RatificationID`, token OPACO de fonte externa |
| MÉDIO | `ratify-nonce:<scope>:<hex>` | `hitl/nonce_store.go:62` *(varredura)* | idem |
| BAIXO | `backpressure/queue/<name>`, `degradation/<name>`, `routing/<name>`, `scheduling/dispatch/<name>`, `backpressure/policy-audit/<name>` | `scheduler/{queue,degradation,routing,priority,policy}.go` *(varredura)* | nome de instância, dado por quem compõe (interno) |
| BAIXO | `budget-breaker/<treeID>`, `<treeID>` | `scheduler/breaker.go:882`; `budget/events.go:91` *(varredura)* | id de árvore de orçamento |

**O `run_id` NÃO está nesta lista de propósito** — é critério de aceitação do AOS-424.
**ATENÇÃO, e isto mudou:** o AOS-424 validou-o **só no `POST /plans`**. No `POST /runs` — o
único sítio onde um `run_id` de cliente se torna um `stream_id` — **continua sem validação**,
bloqueado por um conflito de invariantes (o `ValidNodeID` admite `.` e `:`). Quem executar este
ticket **não pode considerar o `run_id` fechado**: ou o eixo do `ValidNodeID` é resolvido no
AOS-424, ou esta lista tem de o incluir.

### Porque é que o AOS-424 sozinho não fecha isto

A decisão (1) do AOS-424 é validar o `stream_id` no contrato do `eventstore`, imposto pelos dois
backends. Isso **apanha** estes casos — mas repare-se no QUANDO e no QUE ACONTECE:

- a validação dá-se no `Append`, isto é, **no ponto de USO**, muito depois de o valor ter entrado
  no sistema;
- o efeito é uma recusa. Para a admissão de quota, uma recusa no `Append` significa **runs a
  deixarem de ser admitidos** — em produção, por causa de uma alteração de política feita horas
  antes e aprovada por quem não podia saber.

Ou seja: apertar o contrato do Event Store converte um defeito SILENCIOSO numa **avaria VISÍVEL**,
que é melhor, mas continua a ser uma avaria — e no sítio errado. Para valores compostos em
runtime, a validação tem de estar **onde o valor ENTRA**, não onde é usado: na carga da allowlist,
na emissão do `RatificationID`, na composição do scheduler. É a mesma disciplina que o resto do
sistema já aplica às env vars — uma `AOS_*_INTERVAL` mal formada **aborta o arranque** em vez de
degradar em silêncio.

### Decisões a tomar primeiro (do dono)

1. **Esperar pelo AOS-424 ou correr em paralelo?** Se a decisão (1) de lá for «só renomear», este
   ticket passa a ser a única defesa desta metade e sobe para P1. Se for a causa-raiz, este ticket
   muda de natureza: deixa de ser «impedir nomes inválidos» e passa a ser «antecipar a recusa para
   a fronteira de entrada».
2. **Onde validar cada valor.** A allowlist é ASSINADA — validar na carga significa que um bundle
   assinado válido pode ser **recusado** por conter um modelo com ponto. Isso é desejável (falha
   cedo, num arranque, e não a meio da admissão de um run), mas é uma decisão de fronteira: passa
   a haver políticas assinadas que o nó recusa por uma razão que não é de política.
3. **O `RatificationID` é um token de fonte externa.** Recusar um `scope` com ponto significa
   recusar uma ratificação — numa cerimónia humana, com assinaturas já dadas. Recusar cedo (na
   emissão) ou tarde (no uso) tem custos humanos diferentes.
4. **Escapar em vez de recusar, para os casos internos?** Para nomes de instância do scheduler e
   `treeID`, uma normalização determinista (`.` → `-`) seria transparente. **NÃO se propõe para os
   outros**: o `subjectDe` recusa em vez de escapar precisamente para não produzir colisões
   silenciosas entre streams vizinhos, e essa razão vale aqui na mesma.

### Critérios de Aceitação

- [ ] Cada sítio da tabela tem a sua decisão tomada — validar na entrada, normalizar, ou declarar
      que se aceita a recusa tardia — e **nenhum fica sem decisão escrita**.
- [ ] A allowlist de modelos é validada **na carga**, com teste que prova que um modelo com ponto
      é recusado e nomeia a razão (o nome entra num `stream_id`).
- [ ] O acoplamento **política ↔ espaço de nomes** fica escrito no sítio onde alguém que revê
      política o veja — no próprio `allowlist_policy.json` ou ao lado dele. Hoje nada liga os dois,
      e essa é a falha de fundo deste ticket.
- [ ] Os casos de risco MÉDIO têm teste com um valor que contém ponto — hoje **os testes só usam
      valores sem ponto** (`Model: "claude"`, `"gpt"`, `"m"`), que é a razão pela qual isto nunca
      foi exercitado.
- [ ] O gate de alcance de repositório do AOS-424 **declara explicitamente** que não apanha
      composição em runtime, e aponta para este ticket. Um gate que parece cobrir a classe inteira
      e não cobre é pior do que um gate que declara o seu alcance.

### Fora de âmbito, declarado

- **O `run_id`** — é do AOS-424.
- **Renomear os nove streams com nome LITERAL** — idem.
- **Levantar JetStream em produção** — é do AOS-423.

### Riscos

| Risco | Mitigação |
|---|---|
| Validar na carga da allowlist faz o nó recusar um bundle ASSINADO e válido | Decisão (2). A recusa tem de nomear a razão real — «este modelo entra num nome de stream» — e não parecer um erro de política |
| Recusar um `RatificationID` com ponto aborta uma cerimónia humana com assinaturas já dadas | Decisão (3): validar na EMISSÃO, não no uso |
| Normalizar (`.` → `-`) onde não se deve cria colisões silenciosas entre streams vizinhos | É a razão pela qual o `subjectDe` recusa em vez de escapar. Só se normaliza onde o valor é interno e a colisão é impossível |
| Este ticket ser lido como «já está coberto pelo AOS-424» e ser fechado sem trabalho | O AOS-424 valida no USO; esta classe precisa de validação na ENTRADA. São coisas diferentes e está escrito acima porquê |

### Estado

**ABERTO.** Nada implementado. **Não é defeito vivo hoje** — a allowlist em vigor não tem modelos
com ponto (medido) e não há NATS em produção. É uma dívida que só se manifesta quando alguém
mudar uma política, e por isso vale mais escrever o acoplamento do que confiar em que ninguém o
faça.

---

## AOS-424 — Nove streams não são representáveis no JetStream, e o `run_id` do cliente também não é validado

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa ADR nenhum: corrige uma classe de defeito latente e propõe uma
     regra de nomenclatura. As citações ao ADR-007 (Event Store replicado) e ao ADR-001 são
     RESTRIÇÕES. A decisão (1) abaixo — validar o `stream_id` no contrato do `eventstore` — É
     uma decisão de arquitectura com quebra de compatibilidade: se for aceite, abre-se ADR
     próprio e este marcador sai. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orquestração (por ser onde o resíduo ficou registado; o âmbito que toca é o do EPIC-24, e o eixo funcional é EPIC-02/AOS-021) |
| Fase | Prontidão para utilizadores reais |
| Milestone | v1.1 |
| Tipo | correcção de classe + decisão de contrato |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-417 (onde o resíduo foi declarado); AOS-021 (a cerimónia four-eyes, o consumidor mais afectado); AOS-100/101 (donos do Event Store replicado) |
| Bloqueia | **AOS-423** — uma das três vias para o consumidor da fila é migrar para JetStream, e esta dívida torna essa via destrutiva |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/substrate/eventstore/jetstream/store.go` (`subjectDe`), `packages/substrate/eventstore/store.go` (o backend que NÃO valida), `packages/integration/approval_store_durable.go`, `packages/platform/memory/**`, `packages/cmd/aos/aos417_nome_do_stream_test.go` (o guard de alcance limitado), `scripts/ci/event-catalog.py` (o molde do gate que falta) |

### Contexto

O `jetstream.Store.subjectDe` **recusa** qualquer `stream_id` que contenha `.`, `*`, `>`, espaço,
tab, CR ou LF — porque um subject NATS não os representa, e escapar em silêncio para um subject
vizinho seria pior. É a escolha certa.

O problema é o outro lado: **o store de FICHEIRO não valida nada.** O `eventstore.Store.Append`
aceita qualquer `streamID`. É essa assimetria — e não o rigor do NATS — a causa-raiz: um nome
inválido funciona em desenvolvimento, em CI e em produção-sobre-ficheiro, e só falha na topologia
que nada exercita.

O AOS-417 apanhou UM caso (a fila de pedidos de plano, corrigida no PR #354) e declarou o
`gov.approvals` como resíduo. **A varredura mostrou que são nove**, mais uma superfície aberta a
clientes externos.

### O que foi medido

**Nove `stream_id` com ponto**, todos lidos linha a linha:

| Stream | Onde | Composto em produção? |
|---|---|---|
| `gov.approvals` | `integration/approval_store_durable.go:48` (à data da medição) | **SIM**, quando o four-eyes está ligado (`bootstrap.go:1855`, `:1865`). Serve TAMBÉM os registos de retoma (`resume_records.go:132`) |
| `memory.episodic` · `memory.semantic` · `memory.procedural` · `memory.working` | `platform/memory/adapters/eventstore_adapter.go:27` (`streamPrefix = "memory."`) | **SIM** — `bootstrap.go` (composição da MemoryPort; a linha mudou com esta entrega) compõe o `NewEventStoreAdapter` |
| `memory.semantic.knowledge` | `memory/semantic/knowledge_base.go:66` | não (sem compositor) |
| `memory.episodic.trajectories` | `memory/episodic/trajectory_store.go:75` | não |
| `memory.compression.summaries` | `memory/compression/async_compactor.go:71` | não |
| `memory.migrations` | `memory/migrations/registry.go:24` | não |

**O `subjectDe` tem QUATRO chamadores em produção**, e o quarto não era conhecido:
`Append` (`store.go:301`), `Read` (`:455`), `StreamHead` (`backup.go:61`) e **`IngestStream`**
(`backup.go:129`) — o caminho de **restauro / DR**. Consequência: restaurar para um nó JetStream
um backup tirado de um nó WAL **aborta ao primeiro stream com ponto**, e o `restore.go:168-182`
pára no primeiro erro. Como `gov.approvals` é alfabeticamente anterior a `memory.*` e a `run-*`,
**nem os streams de run chegam a ser tentados**. Isto também fecha a via de migração «copiar do
nome antigo para o novo pelo backup».

**O `Subscribe` falha em SILÊNCIO.** Não chama o `subjectDe`: o consumidor durável usa
`FilterSubject: prefixo + ".>"` (`store.go:1228`) e o filtro por stream é aplicado **em processo**.
Um filtro por um nome não representável não dá erro — nunca casa nada. É um modo de falha pior
do que o `E_CONFIG`.

**O `run_id` NÃO é validado.** O `POST /runs` verifica «vazio» e «prefixo reservado» e mais
nada (`api.go:628`, `:639`) — e o `run_id` de um run **é** o seu stream. Um cliente que submeta
`run_id: "cliente.pedido-1"` recebe `201` sobre WAL e `E_CONFIG` sobre JetStream. Propaga-se a
tudo o que deriva do run: `lease:<run>`, step-ledger, checkpoint, steer, eventsink do RM,
sandbox, broker. **É a maior superfície da lista, e a única controlável por um cliente externo.**

**Sem teste sobre JetStream em lado nenhum que toque nisto.** O
`approval_store_durable_test.go` usa o store in-memory; os únicos testes que importam
`eventstore/jetstream` saltam sem `AOS_NATS_URL`, e **nenhum ficheiro de CI define essa
variável**. O gate `dormencia` inventaria-as e emite *warn*, não *fail*.

### Gravidade: LATENTE hoje, DESTRUTIVA no dia da migração

**Não há serviço NATS no `docker-compose.prod.yml` e o `AOS_EVENTSTORE_NATS` tem default vazio**
(medido). Logo isto não é uma avaria em curso — é dívida latente. Mas:

- **o AOS-423 identificou a migração para JetStream como uma das três vias** para o consumidor da
  fila existir, porque o substrato de ficheiro não arbitra entre processos (DEF-282). Esta dívida
  torna essa via destrutiva;
- `AOS_MODE=production` **exige** substrato durável para o four-eyes
  (`ErrProductionNeedsDurableApproval`), e o JetStream é uma das duas opções sancionadas — a
  configuração afectada não é exótica, é suportada.

O que acontece no dia em que alguém ligue o JetStream, por ordem de gravidade:

1. **A cerimónia four-eyes fica inoperante por inteiro.** O `PendingApprovals.Put` falha ⇒ **o
   operador nunca vê o que tem para aprovar**: a escalada acontece, o registo não grava, a lista
   fica vazia e o run fica suspenso. Fail-closed **e invisível** — a pior combinação.
2. **A memória desaparece em silêncio.** As quatro classes estão compostas no nó, e o
   `ErrStreamNotFound` é tratado como «vazio» nessa camada: não há erro, há degradação da
   qualidade do agente.
3. **O restauro/DR aborta** ao primeiro stream com ponto.

### Decisões a tomar primeiro (do dono)

1. **Corrigir a CAUSA-RAIZ, ou só os nomes?** A causa-raiz é a assimetria: o backend de ficheiro
   aceita o que o JetStream recusa. Corrigi-la é validar o `stream_id` **no contrato do
   `eventstore`**, imposto pelos DOIS backends. Isso torna os nove nomes actuais **ilegítimos em
   qualquer substrato** — é uma quebra de compatibilidade deliberada e precisa de ADR. A
   alternativa (renomear e pôr um gate estático) é mais barata e deixa a classe reabrir-se a cada
   stream novo composto em runtime. **Recomendação: corrigir a causa-raiz**, porque enquanto os
   dois backends discordarem, «funciona em dev, falha em produção» continua a ser o desfecho por
   omissão.
2. **Que nome de substituição.** Hífen (`gov-approvals`) ou barra (`gov/approvals`)? A barra tem
   precedente (`aos-internal/`) e uma propriedade adicional: mantém o stream **fora do alcance de
   `GET /runs/{id}/...`**, porque o padrão da stdlib casa `{id}` com um só segmento. Para o
   `gov.approvals` — que guarda grants de aprovação — essa propriedade parece desejável.
3. **A migração do `gov.approvals` — DECIDIDA e FEITA (copiar e cortar).** O que se segue
   continua válido como registo do porquê.

   **A migração NÃO admite leitura dupla ingénua.** O uso-único atómico do
   `Consume` assenta na dedup do Event Store, que é **por stream**. Com dois streams vivos, um
   grant consumido no antigo não deduplica no novo, e a garantia «uma aprovação destrava no
   máximo UMA execução» quebra-se durante a janela — é o primitivo de SEGURANÇA do four-eyes.
   Ou se copia tudo e se corta de uma vez, ou se aceita que os factos antigos ficam órfãos. E o
   `platform/backup` **não** serve de veículo (ver `IngestStream` acima).
4. **Validar o `run_id` na fronteira** é uma mudança de contrato público: um cliente que hoje use
   um ponto passa a receber `400`. Aceitável? (O sítio óbvio é ao lado do `runIDReservado`, que
   as duas rotas de submissão já chamam.)

### Âmbito proposto, e o que é barato AGORA

- **Barato e com prazo de validade:** `memory.semantic.knowledge`,
  `memory.episodic.trajectories`, `memory.compression.summaries` e `memory.migrations` **não têm
  compositor** — hoje renomear é literalmente trocar uma constante. O `memory.migrations` é o mais
  urgente dos quatro porque o seu modo de falha é ACTIVO: `IsApplied` responde «não aplicada»
  sobre stream vazio, logo renomear depois de ligado faz **reaplicar todas as migrações**.
- **Caro e com histórico:** `gov.approvals` e as quatro classes `memory.*`, todas compostas em
  produção.
- **Aditivo, sem histórico:** a validação do `run_id` e o gate de reincidência.

### Critérios de Aceitação

- [ ] Decisão (1) tomada e registada — em ADR se for a causa-raiz.
      *(**POR DECIDIR.** Apertar o contrato do `eventstore` torna ilegítimos os dois nomes que
      ainda têm histórico em produção, pelo que a ordem obrigatória é renomear PRIMEIRO. Esses
      dois renames precisam da decisão (3), e por isso este critério fica aberto.)*
- [~] Há **gate que impõe a regra**, e quatro dos seis nomes estão corrigidos — mas **NÃO é
      verdade que nenhum `stream_id` da árvore contenha carácter não representável**: dois
      contêm, e estão em baseline. A primeira versão desta caixa dizia `[x]` com esta mesma
      nota por baixo a contradizê-la; uma revisão adversarial apanhou-o. Uma caixa que afirma
      o contrário da sua própria nota avermelha a confiança em todas as outras.
      *(`scripts/ci/stream-names.{py,sh}`, no molde do `event-catalog`: lê os ficheiros e por
      isso vê os 49 módulos, que um teste Go num módulo não vê. **Lê a regra da FONTE** —
      extrai o `ContainsAny` do `subjectDe` — em vez de a duplicar, com controlo de
      não-vacuidade contra um valor conhecidamente mau, e fail-closed a zero constantes.
      Registado nos QUATRO sítios onde a lista de checks vive — `ci.yml` (`needs` e
      comentário), `CONTRIBUTING.md` e o `ALL_GATES` de `scripts/ci/run.sh` —, mais o
      `Makefile` (`ci-stream-names`). O quarto faltava, e faltava de forma consequente: sem
      ele o gate não corria no `make ci`, e o gate é o **único** sensor dos quatro renames —
      revertendo um deles, todas as suites locais ficavam verdes. O self-test §M passou a
      cruzar os quatro (antes cruzava três). Verificados 24 nomes.
      **Alcance exacto, sem exagero:** o gate lê `packages/**/*.go` e **salta `*_test.go`**.
      Os quatro módulos Go fora de `packages/` (três em `deploy/`, `scripts/ci/attest`) não
      importam o `eventstore` nem chamam `Append` — verificado —, pelo que a omissão não é
      um buraco; mas «alcance de repositório» era demasiado forte. **Dois ficam em BASELINE**, com dono e com o custo escrito — ver
      abaixo; a baseline é dívida declarada, não verde.)*
- [x] **Os SEIS nomes não representáveis corrigidos** — quatro por rename simples, dois com
      MIGRAÇÃO dos factos.
      *(`memory/{semantic,episodic,compression,migrations}` → `aos-internal/memory/...`. Custou
      uma constante cada porque **nenhum é composto pelo nó** — medido: nada em `cmd/aos` nem
      em `integration` os importa, logo não há factos no nome antigo. Usou-se **barra**, e não
      hífen: um nome sem barra é um segmento de caminho e o AOS-426 mediu treze streams internos
      a serem servidos por `GET /runs/{id}/...` — o prefixo mantém estes fora desse alcance por
      CONSTRUÇÃO, e não só pela trava. Suites dos cinco pacotes de memória verdes.)*
- [~] O `run_id` é validado na fronteira das duas rotas de submissão, com teste.
      *(**METADE, e a outra metade está BLOQUEADA por um conflito de invariantes que este ticket
      descobriu.** Ver a secção abaixo. Feito no `POST /plans`; **NÃO** feito no `POST /runs`,
      onde partiria o caminho do plano em produção.
      **E a metade feita é a que NÃO tem efeito hoje**, o que tem de ser dito: no `/plans` o
      `run_id` do cliente nunca se torna um `stream_id` — o append é ao stream fixo da fila e
      o id vai no payload e no `StepID`. O único sítio onde um `run_id` de cliente se torna
      stream é o `POST /runs`, que continua permissivo; logo o objectivo da guarda é
      alcançável pela outra porta, hoje. O que ela vale, e não é nada: é validação **no ponto
      de entrada** de um valor que se torna stream quando o plano correr — a tese do AOS-425 —
      e impede que um pedido irrepresentável entre na fila para o consumidor falhar mais
      tarde, longe de quem o submeteu. A assimetria está fixada por teste
      (`TestAOS424PostRunsAindaNaoValidaEPorque`), que **lê o charset do `ValidNodeID` da
      fonte** e fica VERMELHO no dia em que ele deixar de admitir `.` e `:`, com o remédio na
      mensagem. A primeira versão afirmava isto e era FALSA: reagia à consequência (alguem
      ligar a guarda), não à causa — uma revisão adversarial apertou o `ValidNodeID` e o teste
      ficou verde. Verificado por mutação depois de corrigido.)*
- [ ] Existe **pelo menos um teste da cerimónia four-eyes sobre JetStream**.
      *(**NÃO FEITO, e re-classificado.** Este critério pedia um teste que exige `AOS_NATS_URL`
      no CI — que nenhum ficheiro de CI define hoje, e o gate `dormencia` inventaria essa
      ausência como *warn*. Levantar NATS no CI é trabalho de infraestrutura com âmbito próprio,
      não um efeito lateral deste ticket. **Fica por marcar**; ver resíduos.)*
- [x] `tecnica/13` ganha a **regra escrita de nomenclatura de `stream_id`**.
      *(Nova §3.1.1. O documento definia `stream_id` como «fronteira de ordenação» e não impunha
      restrição nenhuma — era a lacuna documental na origem da classe. A secção diz a regra,
      **porque** existe, a armadilha da assimetria entre backends, a convenção de namespacing
      (`-` em vez de `.`, `/` para níveis, `aos-internal/` para streams do nó), o enforcement e
      o que o gate NÃO cobre.)*
- [x] A migração do `gov.approvals` tem plano escrito que **preserva o uso-único** do `Consume`.
      *(**FEITA**, não só planeada. `integration/approval_stream_migracao.go`: copia os factos do
      nome antigo para `aos-internal/gov/approvals` **preservando `(RunID, StepID)`** — e é daí
      que vem a correcção inteira, porque a idempotency-key é `run_id + ":" + step_id`: um
      `used-<id>` copiado verbatim passa a bloquear, no stream NOVO, a reclamação de um grant
      que já tinha sido reclamado no antigo. Corre no arranque, ANTES de a cerimónia ser
      composta, e é fail-closed: um `used-` por copiar é um grant consumível duas vezes.
      **Não se fez leitura dupla**, pela razão que este ticket já registava: a dedup é por
      stream.)*
- [ ] O comportamento SILENCIOSO do `Subscribe` é fechado ou declarado.
      *(**NÃO FEITO.** O `Subscribe` não chama o `subjectDe` — usa `FilterSubject: prefixo + ".>"`
      e filtra em processo —, pelo que um filtro por um nome impossível **não dá erro: nunca casa
      nada**. Fechá-lo é mexer no backend replicado e não cabe num ticket de nomes.)*

### O CONFLITO DE INVARIANTES que este ticket descobriu, e que bloqueia metade do critério do `run_id`

Ligar a validação do `run_id` ao `POST /runs` **partiria o caminho do plano em produção, hoje.**
Medido, e a suite do pacote apanhou-o (o `TestAOS413_ToolsDoPostRunsCortaAToolForaDaLista` usa o
`run_id` `run-413.n1`):

| Fonte | O que declara | Onde |
|---|---|---|
| `plan.ValidNodeID` | O charset FECHADO de um `node_id` **admite explicitamente `.` e `:`**, e é a «grammar ÚNICA do node_id no módulo», imposta pelo validador semântico do AOS-231 | `orchestrator/plan/plandocument.go:88-115` |
| `jetstream.Store.subjectDe` | Um `stream_id` com `.` **não é representável** e é RECUSADO | `substrate/eventstore/jetstream/store.go:1075` |
| `childRunID` | Compõe `<run>~<node_id>` e submete-o ao nó por `POST /runs` | `cmd/aos-orq/node_executor.go:113,224` → `node_client.go:331` |

Um nó de plano chamado `analise.dados` produz o run filho `run-x~analise.dados`. Sobre JetStream
esse run **já está partido hoje**; sobre WAL funciona. Validar no `POST /runs` converteria
«funciona sobre WAL, parte sobre JetStream» em «parte em todo o lado» — uma **regressão** para
quem corre sobre WAL, que é o que corre em produção.

Não se resolveu em silêncio, e não é escolha de quem escreve o handler: os dois invariantes estão
REGISTADOS, um pelo AOS-231 e outro pelo ADR-007.

**Recomendação registada:** tornar o `node_id` **stream-safe por construção** — apertar o
`ValidNodeID` para excluir `.` e `:` — e só então ligar a guarda ao `POST /runs`. É a tese do
AOS-425 aplicada: validar onde o valor ENTRA (o documento de plano, na validação semântica), e
não onde é usado. Um plano com um `node_id` mal formado passa a ser recusado na validação, com
razão legível, em vez de falhar a meio da execução. **Custo:** é uma mudança semântica noutro
módulo, afecta o que o planeador pode produzir, e o prompt de decomposição tem de o saber.

### Riscos

| Risco | Mitigação |
|---|---|
| Renomear `gov.approvals` ou o prefixo `memory.` num nó com histórico perde grants, pendentes, registos de retoma e a memória — e o backup não os transporta | Decisão (3). Ficam em baseline até existir plano de migração |
| **A validação na fronteira quebra um cliente que use pontos no `run_id`** — decisão (4), **TOMADA nesta entrega para o `POST /plans`** | Superfície nova (AOS-417, mergida no mesmo dia) e sem consumidor: nenhum cliente depende dela. No `POST /runs`, onde há comportamento a preservar, a guarda **não** foi ligada |
| Um gate com evasões dá falsa segurança, que é pior do que gate nenhum | Quatro evasões fechadas depois da revisão: segundo `ContainsAny` no ficheiro (âncora no `subjectDe` + piso da regra), chamada com parênteses no 1.º argumento, concatenação de literais, e escapes `\t`/`\r`/`\n`. Todas verificadas por mutação |
| O gate corre só no CI e não no `make ci` | Fechado: `ALL_GATES` + self-test §M a cruzar quatro listas |

### Resíduos deste ticket, declarados

1. **Os dois nomes com histórico** (`gov.approvals` e o prefixo `memory.`) estão na baseline do
   gate, com o custo escrito. Saem quando a decisão (3) existir.
2. **Apertar o contrato do `eventstore`** (a causa-raiz) depende de (1) — a ordem é renomear
   primeiro.
3. **Ligar a guarda do `run_id` ao `POST /runs`** depende de apertar o `ValidNodeID`.
4. **Um teste da cerimónia four-eyes sobre JetStream** depende de haver NATS no CI.
5. **O `Subscribe` que falha em silêncio** — fechá-lo é mexer no backend replicado.

### Estado

**PARCIAL.** Entregue: o gate de alcance de repositório (registado nos quatro sítios da lista
de checks), **os SEIS renames** — quatro por troca de constante e dois **com migração dos
factos** (`gov.approvals` e as quatro classes de memória) —, a validação do `run_id` no
`POST /plans`, e a regra de nomenclatura em `tecnica/13` §3.1.1.

**Não resta nenhum `stream_id` da árvore com carácter não representável em uso.** As duas
entradas que ficam na baseline do gate são constantes do nome ANTIGO, que existem só para as
migrações conseguirem LER — nada escreve nelas, e saem quando puderem desaparecer.

**Por fechar, e cada uma com a sua razão escrita:** o aperto do contrato do `eventstore` — que
já **não está bloqueado por renames** e passa a depender só da decisão (1) —, a guarda no
`POST /runs` (depende do `ValidNodeID`), o teste sobre JetStream (depende de NATS no CI) e o
`Subscribe` silencioso.

## AOS-423 — A fila de pedidos de plano não tem quem a consuma: o `201` promete uma corrida que não começa

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa ADR nenhum: materializa a metade do ADR-028 §2.2 que o AOS-417
     deixou por fazer (o CONSUMO do facto), e as citações ao ADR-018, ADR-023 e ADR-028 são
     RESTRIÇÕES sob as quais o consumidor tem de caber, não entregas deste ticket. Se vier a
     exigir decisão nova — e a pergunta (1) abaixo pode exigi-la — abre-se ADR próprio e este
     marcador sai. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orquestração |
| Fase | Prontidão para utilizadores reais |
| Milestone | v1.1 |
| Tipo | implementação |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-417 (o ingresso, **FEITO** — PR #353); ADR-028 §2.2 (a decisão de forma); DEF-282 (o substrato de ficheiro não arbitra entre processos) |
| Bloqueia | AOS-133 (BFF) e, por arrasto, o EPIC-13; o critério por marcar do AOS-417 («uma corrida desencadeada por rede, sem ninguém no terminal») |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos/plan_ingress.go` (a forma do facto), `packages/integration/approval_store_durable.go` (o molde de consumo-uma-só-vez), `packages/cmd/aos-orq/substrato.go` (`--wal` vs `--nats`), `deploy/server/docker-compose.prod.yml` (serviço `aos-orq`, `profiles: ["orq"]`), `docs/adr/ADR-028-ingresso-do-caminho-do-plano.md` |

### Contexto

O AOS-417 abriu a porta e não pôs ninguém do outro lado. `POST /plans` aceita um objectivo,
grava `planrequest.submitted` no stream `aos-internal/plan-requests` e devolve `201 accepted` —
e o pedido fica lá, a acumular. **Nada o lê.**

Isto não é uma omissão silenciosa: o banner de arranque do nó di-lo por palavras nessas, e o
critério de aceitação do AOS-417 sobre a verificação em produção ficou deliberadamente **por
marcar**, porque sem consumidor não há corrida que se desencadeie. Mas é exactamente o modo de
falha que o próprio AOS-417 existia para fechar — **prometer uma corrida que ninguém vai
consumir** —, deslocado um passo para a frente: antes o `serve` não tinha quem o invocasse por
rede; agora o pedido chega por rede e continua sem quem o invoque.

Do ponto de vista de quem usa o produto, **nada mudou ainda**. Continua a ser preciso alguém no
terminal do servidor.

### Objectivo

Um pedido gravado na fila desencadeia a corrida do plano **sem ninguém no terminal do servidor**,
uma só vez, e o desfecho fica ao alcance de quem o submeteu.

### O que o ADR-028 §2.2 JÁ decidiu, e que este ticket NÃO reabre

- **O `aos-orq` consome o facto e corre o `serve` como hoje**: reclama o lease, possui o run,
  termina. Nada no modelo de posse muda.
- **A frase do compose mantém-se verdadeira** — «um `serve` possui um run e termina, não é um
  daemon». O que muda é **quem o invoca**: em vez de um humano num terminal, um trabalhador que
  lê o facto.
- **Não se inventa substrato de fila.** O Event Store é a fila e o consumo-uma-só-vez segue o
  molde do `approval_store_durable` (claim-before-read por `Append` com idempotency-key,
  `StatusDuplicate` como primitivo de arbitragem). Não um broker novo, não um estado paralelo
  (que o ADR-018 §4 proíbe).
- **Quem arbitra entre dois consumidores continua a ser o LEASE** (ADR-023). O ingresso não
  introduziu uma segunda autoridade, e o consumidor também não pode introduzir.

### Decisões a tomar primeiro (do dono)

1. **COMO É QUE O CONSUMIDOR ALCANÇA A FILA.** É a decisão de que tudo o resto depende, e a
   tabela acima mostra que não há opção gratuita:
   - **(a) NATS partilhado entre nó e consumidor.** É o único substrato que arbitra entre
     processos, e o único em que mais do que um consumidor é seguro. Custo: levantar JetStream em
     produção, migrar o nó de `AOS_EVENTSTORE_PATH` para `AOS_EVENTSTORE_NATS`, e o `aos-orq`
     passar a apontar ao mesmo. É a mudança de infraestrutura mais pesada das três e a única que
     escala para além de um consumidor.
   - **(b) Consumidor DENTRO do processo do nó.** Elimina o problema da tranca — quem já tem o
     `LockWAL` é o nó — e reaproveita os laços que o nó já tem (molde do `backup_scheduler.go`).
     **Mas põe o nó a invocar o `aos-orq`**, e isso toca a fronteira do ADR-018 de frente: o nó
     deixaria de apenas CONHECER a existência do caminho do plano para o DESENCADEAR. Exigiria ADR
     de emenda, e não é óbvio que deva ser aceite.
   - **(c) Rota de leitura/reclamação no nó, consumida pelo `aos-orq` por HTTP.** O canal
     `aos-orq`→nó já existe (`node_client.go`, credencial NHI + Bearer OIDC). Mantém a fronteira
     do ADR-018 (o nó continua a não correr o plano) e não exige infraestrutura nova. Custo: uma
     rota que EXPÕE a fila, com tudo o que o ADR-016 e a revisão do AOS-417 obrigam a pensar — e
     foi deliberadamente fechada por essa razão.

   **Recomendação registada: (c)**, e a razão é que preserva as duas fronteiras que custaram mais a
   estabelecer — o nó não corre o plano (ADR-018) e a posse continua a ser o lease (ADR-023) — sem
   pedir uma migração de substrato em produção. **Mas exige ADR**, porque abre uma superfície de
   leitura que o AOS-417 fechou de propósito, e a não-oracularidade tem de ser reargumentada para
   um consumidor autenticado (que é caso diferente do chamador anónimo que o ADR-016 considerou).
2. **A forma do trabalhador**, uma vez resolvido (1). O ADR-028 diz «um trabalhador que lê o
   facto» e não diz o que ele é: (a) processo de longa duração; (b) temporizador que acorda, drena
   e termina. **Nenhum dos dois contradiz o compose** — a frase «um `serve` possui um run e
   termina» é sobre o `serve`, que continua a terminar. O que pesa é outro facto medido: **nenhum
   binário do AOS corre hoje como serviço de longa duração além do nó** (os `restart: unless-stopped`
   do compose são todos imagens de terceiros), e o único temporizador do host é o
   `aos-tls-sync.timer`. Um trabalhador contínuo seria o primeiro, e traz healthcheck, reinicio e
   observabilidade próprios.
3. **Tecto de pendentes e retenção**, que o ADR-028 §4 atribuiu ao ticket de implementação «com
   o molde de backpressure que o EPIC-03 já descreve». O AOS-417 **não** o fez e declarou-o: um
   tecto sem consumidor bloqueia a rota para sempre ao fim de N pedidos, pelo que a decisão só
   fica bem informada **depois** de existir quem drene. Com consumidor, a pergunta passa a ser de
   parâmetros e não de modelo. Recomendação registada: **recusar pedidos novos com tecto alto**,
   em vez de descartar antigos — um pedido descartado em silêncio é a mesma classe de defeito
   que este eixo inteiro existe para fechar.
4. **O que acontece a um pedido cujo `serve` falha.** O facto é reclamado UMA vez; se a corrida
   terminar em recusa (o `serve` tem códigos de saída distintos para lease detido, fenced, WAL
   detido, humano pendente, recusa e plano rejeitado), o pedido não pode simplesmente desaparecer.
   Retentar? Marcar como falhado num facto de desfecho? Uma falha transitória (lease detido por
   outra réplica) e uma permanente (plano rejeitado) **não podem ter o mesmo tratamento**, e
   confundi-las dá um de dois defeitos: um pedido perdido, ou um laço a retentar para sempre uma
   recusa determinista.
5. **Como é que quem submeteu sabe o desfecho.** Hoje recebe `201` e mais nada. O ADR-028 §2.3
   proíbe devolver o estado do run na resposta ao pedido (não-oracularidade), e a leitura passa
   pelo read-path soberano — mas **o `run_id` que o submissor nomeou chega sequer a ser um run
   legível?** Se o plano materializa nós como `<run>~<nó>` (ADR-027), o id de topo pode nunca
   existir como run, e o submissor fica sem nada para consultar. *(Continua **POR CONFIRMAR**: a
   discovery não inspeccionou `decomporEMaterializar`/`materializarEDespachar` em profundidade.)*

### Critérios de Aceitação

- [ ] Um pedido gravado na fila desencadeia a corrida **uma só vez**, e há teste que o prova com
      DOIS consumidores em simultâneo — não só com um, que não exercita a arbitragem.
- [ ] O consumo usa o molde do `approval_store_durable` (claim-before-read, `StatusDuplicate`) e
      **não** um estado paralelo; a arbitragem final continua a ser o LEASE (ADR-023).
- [ ] O `layer-lint` continua verde e o guard-test de fronteira do ADR-018 **não muda**.
- [ ] Um pedido cujo `serve` falhe tem o desfecho decidido em (4), com teste que distingue falha
      TRANSITÓRIA de PERMANENTE — não é comportamento acidental do código de saída.
- [ ] O banner de arranque do nó **deixa de dizer que ninguém consome a fila**, e o
      `TestAOS417BannerDoConsumidorNaoApodrece` — que existe precisamente para ficar vermelho
      neste momento — volta ao verde pela razão certa (o literal `false` foi corrigido), e não
      por se ter relaxado o teste.
- [ ] A profundidade da fila é observável no `/metrics`. Sem isto não há como saber se o
      consumidor está a acompanhar o ingresso, e o AOS-422 já mostrou o que custa uma guarda sem
      sensor.
- [ ] Tecto de pendentes e retenção implementados segundo (3), ou **declarados** com a razão —
      nunca omitidos em silêncio.
- [ ] Verificado em produção: um objectivo submetido por rede corre até ao fim **sem ninguém no
      terminal**. É este o critério que o AOS-417 deixou por marcar, e é aqui que fecha.

### Fora de âmbito, declarado

- **A cunhagem automática do NHI.** Continua a ser a barreira seguinte ao uso sem operador, é
  decisão de SEGURANÇA (a `issuer.key` não vai para o servidor por desenho) e continua sem ticket
  próprio. Um consumidor que corra sem humano **não** dispensa a credencial que o `serve` precisa.
- **A UI** (EPIC-13). Fica desbloqueada por este ticket; não é feita nele.
- **O crypto-shredding do objectivo**, declarado como resíduo no AOS-417: o payload do Event Store
  é inline e em claro por desenho actual (`tecnica/13` §3.2, pendência §8.1). Não é regressão
  deste ticket nem se fecha nele.

### Riscos

| Risco | Mitigação |
|---|---|
| Um trabalhador de longa duração ressuscita, por outra via, o problema de dois escritores que o ADR-023 fechou | A arbitragem tem de continuar a ser o LEASE, e o teste de dois consumidores é o que o prova. Se a forma escolhida em (1) exigir mais, abre-se ADR |
| O consumo reclama o facto e o processo morre antes de o `serve` arrancar: o pedido fica reclamado e por correr | É o modo de falha central deste ticket. O claim tem de ser recuperável — ou o desfecho tem de ser um facto próprio, não a ausência de um |
| Retentar uma recusa determinista (plano rejeitado pela AOS-231) num laço infinito | Decisão (4): distinguir transitório de permanente pelos códigos de saída, e prová-lo com teste |
| Um consumidor torna trivial disparar corridas e o custo do modelo deixa de ter quem o trave | O orçamento por árvore já existe (AOS-027); verificar que o caminho novo passa por ele — o mesmo risco que o AOS-417 registou e que o ingresso sozinho não exercitava |

### Estado

**ABERTO.** Nada implementado. O ingresso (AOS-417) está em `main` desde o PR #353; a fila existe,
está vazia em produção e não tem leitor.

---

## AOS-418 — Os payloads de um plano reconstroem-se do log: um `serve` que morra deixa de os levar consigo

<!-- rtm: adrs-mencionados -->
<!-- Este ticket EMENDA uma decisão registada no ADR-027 §2.4 (decisão (A) do dono no AOS-414:
     conteúdo em memória) sem a superar: o regime continua a ser memória, e o que muda é que ela
     passa a ser RECONSTRUÍVEL. As citações ao ADR-022/027 são menções. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-19 — Planeador Produtivo e Meta-Orquestração |
| Fase | Remediação pós-produção |
| Milestone | v1.1 |
| Tipo | fix |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-414 (o canal de entrada e o `plan.payload_published`) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos-orq/node_executor.go` (`publicarSaidas`, `entradasDe`, `ErrPayloadPerdido`), `packages/cmd/aos-orq/dispatch_wiring.go` (composição do executor), `packages/control-plane/orchestrator/plannerevents/events.go` (`PayloadPublishedPayload`) |

### Contexto

O conteúdo que os nós de um plano trocam vivia **só** no mapa em memória do executor — decisão (A)
do dono no AOS-414, declarada como resíduo. Um `serve` novo sobre o MESMO plano via o mapa vazio e
o consumidor falhava com `ErrPayloadPerdido`, **apesar de a saída do produtor existir, durável**.

Enquanto cada corrida é conduzida por um operador, isto é um incómodo: repete-se a corrida. Com o
ingresso do AOS-417 e corridas a tornarem-se rotina, **passa a ser perda de dados visível ao
utilizador**. A fragilidade não muda; muda quem a sofre.

### Objectivo

Um `serve` que arranca sobre um plano a meio reconstrói os payloads dos contratos já cumpridos, e
o que não conseguir confirmar não entrega.

### O que torna isto possível sem evento novo

O `plan.payload_published` já carrega o suficiente, de duas formas:

| Forma | O que o evento carrega | Como se reconstrói |
|---|---|---|
| **Fechada** (veredicto) | o conteúdo INTEIRO, em `Closed` | pela mesma função canónica que o publicou |
| **Aberta** | a REFERÊNCIA durável (o run filho) e o DIGEST | relê-se o run filho pelo nó, e o digest diz se é o mesmo |

### Critérios de aceitação

- [x] A forma fechada reconstrói-se do log sem falar com ninguém
      (`TestAOS418_FormaFechadaReconstroiSeDoLog`).
- [x] A forma aberta relê-se do run filho e **confere o digest**
      (`TestAOS418_FormaAbertaReleDoRunFilhoEConfereODigest`).
- [x] Um digest que não bate **não entra** no mapa — entregar o que voltou seria substituir o
      payload por outro sem ninguém dar por isso (`TestAOS418_DigestQueNaoBateNaoEntra`).
- [x] Um run filho que o nó já não conhece não entra, e o consumidor falha como antes
      (`TestAOS418_RunFilhoDesaparecidoNaoEntra`).
- [x] A forma canónica do conteúdo fechado vive numa só função **que NORMALIZA** — uma função
      partilhada fecha o eixo da expressão e não o das entradas, e era por aí que divergia
      (`TestAOS418_RazoesVaziasNaoDivergemEntrePublicarEReidratar`).
- [x] Um `plan.payload_published` ilegível **não tranca o plano**: é ignorado com aviso, em vez de
      abortar todas as retomas seguintes (`TestAOS418_EventoIlegivelNaoTrancaOPlano`).
- [x] A reidratação corre no CONSTRUTOR do executor, não no wiring — um passo que se pode esquecer
      sem nenhum teste dar por isso não é um passo.
- [ ] Verificado em produção: um `serve` morto a meio de um plano, e o seguinte a concluir o
      consumidor. **POR FAZER** (exige deploy), e ver o alcance real abaixo.

### Estado

**IMPLEMENTADO** (2026-09-20), verificação em produção por fazer.

**Falha-antes, pelo processo real.** Com a reidratação neutralizada, os dois testes que provam a
reconstrução ficam vermelhos:

```console
--- FAIL: TestAOS418_FormaFechadaReconstroiSeDoLog
      o payload de forma FECHADA não foi reconstruído do log
--- FAIL: TestAOS418_FormaAbertaReleDoRunFilhoEConfereODigest
      payload de forma ABERTA = "", quero "o texto que o no produziu"
```

Os outros três passam dos dois lados **por desenho**, e digo-o em vez de os contar como prova: dois
são guardas (digest que não bate; run filho desaparecido) e o terceiro é o controlo que prova que
o mapa vazio É o modo de falha — sem ele, «reconstrói sempre alguma coisa» satisfazia os
primeiros.

**A decisão de desenho que não tomei.** Não pus o conteúdo da forma aberta dentro do evento. Seria
mais simples de reidratar e poria conteúdo untrusted, até 128 KiB por payload, no log de
governação — que é append-only e vai ao WORM. A referência + digest dá a mesma durabilidade sem
engordar o log, e o digest é o que impede que a releitura devolva outra coisa.

**Revisão adversarial independente: onze achados, um crítico.** Os cinco que mudaram
comportamento:

| Achado | O que mudou |
|---|---|
| **A forma fechada DIVERGIA entre publicar e reidratar**, e o critério dizia que não podia. A publicação calculava o conteúdo das razões CRUAS e a reidratação das do evento, que o `normalizeClosed` reduz — uma lista vazia vira `nil` (campo `omitempty`). Medido: publicado `{"outcome":"pass","reasons":[]}`, reidratado `{"outcome":"pass","reasons":null}`, para um veredicto que a gramática fechada aceita | `conteudoFechado` passou a NORMALIZAR, tornando-se total sobre as duas entradas |
| **A composição não tinha sensor nenhum:** tirar as três linhas do wiring que chamavam a reidratação deixava a suite INTEIRA verde, porque nada no pacote exercita o `composeEDespachar`. O «falha-antes» declarado cobria o corpo da função e não o facto de ela ser chamada | A reidratação passou para o CONSTRUTOR. A mesma mutação agora mata dois testes |
| **Um evento ilegível trancava o plano para sempre**, e contradizia o resíduo que eu tinha declarado: o evento não desaparece de um log append-only, logo todas as retomas batiam no mesmo ponto | Ignora-se com aviso, como o `fechar` já fazia a um veredicto ilegível |
| **A mensagem acusava adulteração no caso mais provável**: com o nó reiniciado, o `final_text` volta vazio e o digest não bate — mas o facto é «o nó já não retém a saída», não «a saída mudou» | Caso próprio, com a razão certa |
| **Latência de arranque ilimitada e fora do `--plan-timeout`**: uma chamada ao nó por payload, sequencial, com `ctx` sem deadline | Prazo de 2 min para a reidratação inteira |

**O ALCANCE REAL, que a revisão mediu e que muda o valor deste ticket.** A forma aberta relê-se do
`GET /runs/{id}` do nó, e o `final_text` que esse endpoint devolve vem de um registo de desfechos
**em memória**, com poda FIFO. O ramo durável responde `completed` **sem** `final_text`. Logo:

- um `serve` que morra sozinho e volte — **os payloads voltam**, que é o caso que o ticket fecha;
- um restart do STACK inteiro (o `aos-orq` e o `aos` correm no mesmo compose) — **os de forma
  aberta NÃO voltam**, porque o nó já não retém o texto. Os de forma fechada voltam sempre, porque
  vêm do evento.

Ou seja: isto fecha a morte do orquestrador, **não** a morte do nó. Dizê-lo aqui porque a
verificação em produção pode passar sem medir o caso que interessa — se o operador reiniciar só o
`aos-orq`, mede o caso fácil.

**A alternativa que existe e que não usei:** o `runlifecycle.PayloadReader` já faz a metade do log
(ler o stream, indexar por `(produtor, output)`, primeiro vence) e alimenta o
`plandispatch.PayloadResolver`, que re-verifica tipo, taint efectivo e `contract_digest` contra o
documento aprovado — defesa-em-profundidade que esta implementação **salta**. Reusá-lo é o caminho
certo e é trabalho a mais do que cabe aqui; fica nomeado em vez de ignorado.

**Resíduo declarado:** a reidratação é *best-effort por payload*. Um payload que não se confirme
não aborta o arranque — fica por cumprir, e o consumidor falha com `ErrPayloadPerdido` como antes.
Abortar o `serve` inteiro por causa de um payload de um nó seria trocar uma falha localizada por
uma total. E o digest protege INTEGRIDADE, não origem nem contrato: quem calcula e quem compara
são o mesmo processo.

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
| 1.11 | 2026-09-20 | +AOS-415 (o veredicto da validação volta ao planeador): nas DUAS validações em produção com o modelo vivo a 1.ª decomposição foi recusada pela AOS-231 e o `serve` terminou — o laço de tentativas só cobre o decode, e cada tentativa reenvia o mesmo prompt. | Equipa AOS |
| 1.12 | 2026-09-20 | +AOS-416 (o segredo do IdP do executor de nós): a limpeza do servidor depois do AOS-415 mediu que o uid do contentor (`65532`) não lê o ficheiro `0400` que o compose lhe monta — o executor só funcionou porque existia uma cópia `0444` do segredo, entretanto apagada. | Equipa AOS |
| 1.13 | 2026-09-20 | +AOS-417 (ingresso do caminho do plano): medido que o `aos-orq` não tem superfície de rede nenhuma (`ListenAndServe` fora de testes = zero) e que o compose o exclui do arranque por desenho — logo toda a corrida com nós executados exige um humano no terminal do servidor, e nenhum ticket cobria isso. | Equipa AOS |
| 1.14 | 2026-09-20 | +AOS-418 (payloads reconstroem-se do log): o conteúdo vivia só em memória e um `serve` que morresse levava-o consigo, apesar de a saída do produtor existir durável — incómodo com operador, perda de dados quando o ingresso do AOS-417 tornar as corridas rotina. | Equipa AOS |
| 1.15 | 2026-09-21 | +AOS-423 (consumidor da fila de pedidos): o AOS-417 abriu a porta e não pôs ninguém do outro lado — `POST /plans` grava o facto e devolve `201`, e nada o lê. O modo de falha é o MESMO que o AOS-417 existia para fechar (prometer uma corrida que ninguém consome), deslocado um passo à frente. | Equipa AOS |
| 1.16 | 2026-09-21 | +AOS-424 (nomes de stream não representáveis): a correcção do AOS-417 revelou uma classe — são NOVE os streams com ponto, dois deles compostos em produção (four-eyes e memória), o `run_id` do cliente não é validado, e a causa-raiz é o backend de ficheiro aceitar o que o JetStream recusa. Latente hoje (sem NATS em prod), destrutivo no dia da migração que o AOS-423 precisa. | Equipa AOS |
| 1.17 | 2026-09-21 | +AOS-425 (composição de nomes de stream em runtime): a outra metade da classe do AOS-424, que um grep de literais não vê. O `stream_id` de admissão contém o NOME DO MODELO, que vem da allowlist assinada — hoje sem pontos (medido), mas acrescentar um `gpt-4.1` é uma alteração de POLÍTICA que partiria o substrato, e nada no repositório liga as duas coisas. | Equipa AOS |
| 1.18 | 2026-09-21 | +AOS-426 (read-path servia streams internos): medido que `GET /runs/<stream>/trajectory` devolvia 200 e servia treze streams internos do nó a um leitor autenticado de OUTRA região — aprovações four-eyes, memória, identidade e os nonces de ratificação. NÃO latente: mede-se no substrato de ficheiro, que é o de produção. Fechado com uma trava que lê os DADOS, não uma lista de nomes. | Equipa AOS |
