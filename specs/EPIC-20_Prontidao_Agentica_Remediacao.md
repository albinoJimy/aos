# EPIC-20 — Prontidão Agêntica: remediação de achados, custo governado e extensões ADR-021/022

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Remediação dos achados de prontidão (F1–F16), billing token-only ligado ao nó, e implementação dos ADR-021/022 |
| Versão | 1.0 |
| Data | 2026-08-08 |
| Classificação | Documento de Referência — **Em execução** (34/34 implementados e no trunk; ADR-021 e ADR-022 ratificados e implementados; D1/D2 **decididas** pelo dono em 2026-08-13 e implementadas em AOS-259/AOS-260 — ver **§0 Balanço** e **§0-bis**) |
| Documento-fonte | **`docs/reports/prontidao-modelos-agenticos.md`** (relatório consolidado, 2026-08-08) |
| ADRs aprovados (RATIFICADOS 2026-08-13) | **ADR-021** (scoring determinístico no GW) · **ADR-022** (arestas condicionais, papel verificador, payload tipado) — ambos `Estado: Aceite` por **ratificação de dono**; antes eram premissa de planeamento (ver §0) |
| Documentos relacionados | `docs/reports/desafio-A1..A4-*.md` (planos de fecho corrigidos + decisões do dono), ADR-008/010/011/012/013/018/020, `tecnica/06`, `tecnica/18` |

---

## 0. Balanço de execução — 2026-08-13 (ADR-021/022 ratificados e implementados)

**Estado global: 34 dos 34 tickets IMPLEMENTADOS e integrados no trunk `feature/AOS-128-ux-dx-tests` (alguns com alcance PARCIAL declarado e eixo); 0 ABERTOS.** O eixo **D4** (autoridade de identidade real) está **fechado no código**. O **ADR-021** e o **ADR-022** foram **ratificados** (2026-08-13, autoridade de dono) e ambos estão **implementados**: AOS-269 (scoring determinístico no GW, com o alcance da emenda 1.1), e AOS-270..273 + DEF-274 (arestas condicionais, `role: verifier`, payload tipado, versionamento/janela/golden-sets, e o gate humano a ver as extensões). **AOS-259 e AOS-260 estão FECHADOS**: o canal de custo ponta a ponta com dedup por parentesco (AOS-259, fonte do preço declarada e residuais de cobertura de tabela e de pares alcançáveis com eixo em DEF-277/DEF-279) e a **admissão do turno de modelo** (AOS-260, D1=opção B) — reserva antes de `rt.model.Call`, saldo pelo consumo medido, esgotamento como degradação declarada e replay sem re-reserva, com os residuais em DEF-278. O epic fecha em **34/34**.

### Implementados (34; alguns com alcance PARCIAL declarado e eixo)
- **Risco activo + higiene (F1–F6, F11–F15):** AOS-245 · AOS-246 · AOS-247 · AOS-248 · AOS-249 · AOS-250 · AOS-251 · AOS-252 · AOS-255.
- **Durabilidade do run:** AOS-253 *(crash-resume: varredura de órfãos no arranque + `Resumer` composto; efeito já aplicado não repete, modelo não re-interrogado)* · **AOS-254 — PARCIAL** *(a CONDUÇÃO da saga está composta e o ramo alcançável — declarar a ausência com selo WORM — provado; a COMPENSAÇÃO REAL fica em **DEF-270**, à espera de um produtor de compensações e do registo run-scoped no kernel)*.
- **Billing token-only (dimensão de tokens):** AOS-256 · AOS-257 · AOS-258.
- **Burn-down + exaustão:** AOS-261 · AOS-262 · AOS-263 *(prompt durável; cutover das 3 decisões do dono — extend fora, reúso HITL, paridade com a pausa)*.
- **Broker de credenciais:** AOS-264 · AOS-265 *(consumo in-process v1)*.
- **Operações do nó:** AOS-266 *(attestation: challenge freshness + device enrollment)* · AOS-267 *(scheduler de retenção)* · AOS-274 *(SLOs)* · AOS-275 *(promoção)* · AOS-276 *(keypool)* · AOS-277 *(ingresso)*.
- **Eixo D4:** AOS-268 *(verificação ancorada do WORM no restart)* · AOS-278 *(cutover duro da identidade do GW)*.
- **ADR-021 (ratificado 2026-08-13):** AOS-269 *(scoring ponderado determinístico no GW: portas de factores em aritmética inteira, tabela de pesos embebida e assinada com trust anchor pinado, guard-test AST contra float/`rand`/relógio, cenário que prova que nenhum peso elege cross-border)*. **Alcance pela emenda 1.1 — SUPERADO em 2026-08-13 por AOS-280:** o scoring era composto **por opção** e não tinha efeito em produção enquanto o estágio de roteamento não fosse composto (lacuna **pré-existente**, **DEF-271**). O **DEF-271 está FECHADO**: `NewProduction` compõe a cadeia `failover` → `routingstage`+`router` com o scoring armado, pelo que a **regra 3 do ADR-021 passou a ter efeito real** — um deployment que declare `RoutingConfig.Tiers` **não arranca** sem tabela de pesos válida e assinada. Quem prepara um deployment tem, por isso, de versionar a tabela e passá-la pelo eval-gate ANTES de declarar a escada. Ver §AOS-280 (a emenda **1.2** do ADR-021, que actualiza o §5-bis, fica pendente de decisão de dono — **DEF-280-ADR021**).
- **ADR-022 (ratificado 2026-08-13) — FECHADO:** AOS-270 *(arestas condicionais; «ciclo disfarçado» rejeitado reutilizando o DAG de AOS-025; replay reproduz o ramo sem re-avaliar)* · AOS-271 *(`role: verifier` com semântica de sistema: read-only por construção, produtor ≠ verificador, veredicto tipado)* · AOS-272 *(payload tipado por aresta: contratos validados estaticamente, taint derivado da **autoridade** e não da palavra do planeador, transporte por referência — nunca blackboard)* · AOS-273 *(`plan_version` 1.2.0 com o carimbo **imposto** pelas features usadas, migração provada nas duas direcções com fixtures congeladas, janela de suporte ancorada em código, golden-sets)* · **DEF-274** *(o gate humano passa a **ver** as extensões — invariante §2.4(5) — em forma canónica e content-free, imposta também no wire)*.

### Abertos (0 do âmbito original) — o epic fecha em 34/34

> **Precisão de contagem (validação de 2026-08-13, actualizada):** o âmbito do epic são os **34** tickets `AOS-245..AOS-278`, todos implementados. A tabela §4 tem **35** linhas porque a remediação deste próprio epic **criou** o **AOS-279** para dar eixo executável ao DEF-276 — e esse **também já foi entregue** (2026-08-13): o golden-set do planeador passou a correr no gate de CI `evalgate.sh`, fechando a metade do AC2 de AOS-273 que estava declarada «NÃO ENTREGUE». Não tem secção própria no corpo, de propósito: nasceu aqui e a sua descrição vive na linha da tabela.

- **Eixo $ do billing:** ~~AOS-259 *(D2)*~~ **FECHADO** · ~~AOS-260 *(D1)*~~ **FECHADO**. A dimensão de **tokens** está ligada (AOS-256..258, e desde AOS-260 também no turno de modelo); a de **dólares** é **medida** ponta a ponta (AOS-259, tabela do operador em `AOS_MODEL_PRICING_PATH`), **debitada** na árvore de orçamento e **negadora** quando `AOS_BUDGET_MAX_COST_MICRO_USD` está definida (AOS-260). Residuais com eixo: **DEF-277** (curadoria da tabela de preços), **DEF-278** (tecto em `$` admitido um turno tarde no primeiro turno de cada incarnação; orçamento durável por-`run_id`; reclamação de provisão órfã armada e ainda inalcançável) e **DEF-279** (a cobertura de preço é verificada para o par PEDIDO — vale enquanto o inventário do keypool tiver uma só conta/região).

### Deferidos com eixo (infra-org, fora do código do nó)
Selagem periódica out-of-process dos checkpoints do WORM (**DEF-268/DEF-269**, custódia de chave D4/AOS-156) · instância do IdP tenant + adaptador HSM/KMS (costura `crypto.Signer`, EPIC-16) · injecção do broker no executor remoto (**D8-B**) · ~~composição do estágio de roteamento no pipeline do GW (**DEF-271**)~~ **FECHADO em 2026-08-13 por AOS-280**, ficando no seu lugar os residuais **DEF-280-PORTAS** (orçamento e *task-fit* continuam seams — as fontes vivem noutros módulos), **DEF-280-TOKENS** (o `Exchange` não transporta o prompt: a reserva de admissão coordena por pedidos, não por tokens), **DEF-280-NO** (o nó de referência ainda não declara escada, logo o binário do nó continua a rotear só pelo failover) e **DEF-280-ADR021** (emenda 1.2 do ADR-021, pendente de dono) · emissão do veredicto e transporte do payload sem chamador de produção (**DEF-272/273**, eixo AOS-238) · gate de CI `evalgate.sh` a correr os golden-sets do planeador (**DEF-276**, eixo **AOS-279**) · compensação real da saga (**DEF-270**: exige um *produtor* de compensações — o loop nunca popula `activity.Activity.Compensation` — **e** o registo run-scoped no kernel; enquanto faltarem, habilitá-la compensaria o run errado).

### Nota de reconciliação
Os campos `### Estado` por-ticket de **AOS-245, AOS-250 e AOS-266** estavam desactualizados — descreviam a não-entrada na W0 (245/250) ou o estado inicial (266), mas a implementação e os testes dedicados existem e passam (`aos245_ledger_titular_test.go`, `apiMaxTurnsOptionFromEnv`, `aos266_challenge_freshness_test.go`/`aos266_device_enrollment_test.go`). Corrigidos na revisão de 2026-08-12.

**Sobre o «assumidos aprovados» do cabeçalho (ADR-021/022) — RESOLVIDO em 2026-08-13.** A linha «ADRs assumidos aprovados» descrevia a *premissa de planeamento* da epic, não o estado real: ambos os documentos estavam em `Estado: Proposto — pendente de ratificação`, e enquanto assim fosse AOS-269..273 não podiam entrar (Carta §6: ratificar **antes** de implementar, não depois). O dono **ratificou ambos em 2026-08-13** (`Estado: Aceite`, ratificação de dono) — a premissa do cabeçalho passou a ser verdade e a frente ficou executável.

---

## 0-bis. Decisão do dono sobre D1/D2 — 2026-08-13

**DECIDIDO: D2 = o eixo $ entra na v1; D1 = opção B (o orçamento cobre tool calls *e* o turno de modelo).** Desbloqueia **AOS-259** e **AOS-260**, e com eles o epic fecha em **34/34**.

**A decisão AFASTA-SE da recomendação do desafio A1**, que era «A (tool-only) com o banner a dizê-lo» e «token-only na v1; $ só depois do contrato `port`». O afastamento é deliberado e a razão é que **os factos mudaram desde que a recomendação foi escrita**:

- **O que motivava a cautela** era que a inferência — a linha de custo dominante — corria **sem controlo nenhum**: `rt.model.Call` é invocado directamente (`kernel/agent-runtime/loop.go`), fora da cadeia do Reference Monitor, sem hook e sem reserva.
- **O que entretanto entrou** (AOS-261/262/263) foi o controlo *a posteriori*: o burn-down soma os tokens **reais** dos turnos de modelo a partir do ledger (`turn.recorded`), compara-os com o tecto por-run, **avisa** ao cruzar o limiar e pode **suspender o run para decisão humana**, com selo WORM. A inferência deixou de ser invisível ao orçamento; o que lhe falta é a **reserva prévia**.
- **O canal de custo é ligação, não construção**: o GW já tem subsistema de metering (`metering/cost`, `cost.Amount{Tokens,CostMicroUSD}` + `budgetbridge`) e o runtime já tem o campo do outro lado (`resp.CostMicroUSD`, `TotalCostMicroUSD`, span `AttrCostUSD`) — a receber zero. O que falta é o campo em `port.Usage`, o preenchimento no adaptador, e o **dedup por parentesco** (sem ele, dois spans `chat` por trace somam tokens a **2×** — achado A2-E).

**O que a decisão obriga a fazer bem** (e que fica como critério de aceitação, não como intenção): no esgotamento, **degradação declarada** — nunca um *deny-loop* cego que mate o run em silêncio; e **o replay não re-reserva** (dedup por `run_id:step_id`), senão a retoma cobrava duas vezes o mesmo turno.

**Consequência para o banner:** a frase de âmbito de AOS-255 (`BudgetScopeDeclaration`) descreve a postura *tool-only/token-only* e **passa a ser falsa** quando AOS-259/260 entrarem. Actualizá-la faz parte de AOS-260 — a postura anunciada tem de continuar a ser a postura ligada (AOS-203/AOS-248).

---

## 1. Visão do Epic

O relatório de prontidão consolidou quatro vagas de avaliação: os gaps originais (efeitos, feedback, aprovação humana) estão **fechados**, mas as vagas 3–4 expuseram defeitos **activos hoje** — o step-ledger a persistir outputs de tools em claro (F1), o circuit breaker inerte no run comum (F2+F3), a máquina de estados sem desfecho durável (F4) — e um corpo de capacidades verdes em teste sem costura no deployment (budget, progress-surface, broker, portas de attestation). Esta epic executa **todas as acções do relatório**: primeiro remove o risco activo, depois liga o billing token-only (plano corrigido pelos desafios A1/A2), depois as capacidades dependentes de decisão do dono, e por fim implementa os **ADR-021 e ADR-022 como aprovados**.

Invariantes congeladas: toda a tool call mediada pelo RM (ADR-002); fail-closed por omissão; determinismo/replay (ADR-010); o banner declara a postura real (AOS-203); nenhum wiring novo sem teste de composição **pela cadeia de produção** (a lição da sessão 2026-08-08: «um teste de composição que substitui a peça vizinha por um duplo NÃO é um teste de composição»).

## 2. Fronteira eu-construo vs. deployment/dependências

| Frente | Código desta epic | Fora (dependência/decisão) |
|---|---|---|
| Risco activo (F1–F6, F11–F15) | Tudo — wiring e guards no nó | — |
| Billing (tokens **e** $) | Wiring do hook, ciclo de vida por-run, envs, estimador, **canal de custo** | **D2 DECIDIDO 2026-08-13 (dono): eixo $ ENTRA na v1** — o custo real flui ponta-a-ponta (AOS-259). A recomendação do desafio A1 era «token-only na v1»; o dono decidiu graduar, e a razão está registada no §0-bis |
| Turno de modelo no orçamento | Porta de reserva/settle no `agent-runtime` (usa o custo de AOS-259) | **D1 DECIDIDO 2026-08-13 (dono): OPÇÃO B** — o orçamento passa a cobrir **tool calls + turno de modelo**, com reserva ANTES da inferência e settle pelo usage real (AOS-260). A recomendação era «A (tool-only) com o banner a dizê-lo»; o dono decidiu fechar o eixo |
| Exaustão graciosa completa | Retentor de spans, resolvedores, burn-down+aviso | **D4/D5/D6** (2.º tipo de PendingRecord, autoridade do `extend`, dono do tecto) |
| Broker de credenciais | Passo zero de política/identidade, cliente Vault, porta com contexto | **D7/D8 DECIDIDOS 2026-08-10 (dono):** D7 = cliente/token Vault SEPARADOS (`AOS_BROKER_VAULT_*`); D8 = consumo v1 IN-PROCESS (injecção remota deferida, D8-B) |
| Verificação ancorada do WORM | Env trust anchor + `VerifyFromCheckpointAtHead` no restart | **D4/AOS-156** — custódia da chave do operador (infra-org) |
| ADR-021 / ADR-022 | Toda a implementação | Gramática concreta de perfis/condições/payloads → `tecnica/06`/`tecnica/18` |
| ORQ/SCH distribuídos | **Nada** — deferimento ADR-018 mantido | EPIC-10 (frota multi-nó) |

## 3. Critérios de Saída do Epic

- [x] Nenhum output de tool call é persistido em claro: o step-ledger sela por-titular como o capturer (AOS-245), e o shred/expire apaga ambos (prova: erase → `ErrDecrypt` nos dois registos). **Verificado na validação de 2026-08-13:** as duas metades estão provadas em ficheiros distintos — o **ledger** em `packages/cmd/aos/aos245_ledger_titular_test.go` (`TestNode_AOS245_ToolOutputSealedInLedger`, asserção `audit.ErrDecrypt` após o erase) e o **capturer** em `packages/cmd/aos/aos093_substrate_erase_test.go` (mesma asserção sobre `OpenContent`). O critério estava por marcar, não por cumprir.
- [x] O breaker **dispara** no run comum: teste de nó repete a mesma call negada e assere trip **antes** de `MaxTurns` (AOS-251); ligar velocidades sem fonte **aborta o arranque** (AOS-246).
- [x] O log durável distingue desfecho de crash: `complete`/`failed` escritos em todos os caminhos; `CheckDeadlines` com caller periódico **que interrompe o run** (AOS-252). **Verificado na validação de 2026-08-13:** a ressalva antiga («falta o teste crash-simulado vs fim-normal») estava obsoleta — o subteste existe em `packages/cmd/aos/aos252_terminal_states_test.go` («crash simulado distingue-se do fim normal») e o próprio AC3 do ticket está `[x]` desde a W1. O critério estava por marcar, não por cumprir.
- [x] Um crash a meio de um run é retomado por varredura no arranque, sem re-executar efeitos (AOS-253). `NodeService.ResumeInterruptedRuns` (ligada em `main.go` entre `NewNodeService` e `Serve`) compõe o `durable.Resumer` (AOS-015, nunca antes lido), varre os streams em `running` (rasto de crash de AOS-252), reclama o lease via `worker.Assigner` (sem roubo) e retoma pelo replay-then-continue de AOS-021 (`RebuildLedger` + plano de replay); prova de não-double-execution em `aos253_crash_resume_test.go`.
- [x] Um run com `AOS_BUDGET_MAX_TOKENS` definido é **negado por orçamento** com o deny selado e atribuído, e um run dentro do tecto obtém **permit** — ambos ao nível do nó (AOS-256..258). Prova em `packages/cmd/aos/aos258_budget_permit_node_test.go` (`Bootstrap` real, tecto pela env do operador; permit **com a tool a executar**, deny com `denied_by=budget` + hash-chain verificada, e o mesmo run a permitir e depois negar).
- [x] O banner declara budget/broker/modelo/autonomia (AOS-248) — postura anunciada = postura ligada.
- [x] Burn-down visível no run real com aviso a ~80% (AOS-261/262) — fonte no **ledger de turnos** (não spans, que ninguém retinha), aviso **uma vez por run** no **log do nó** (canal que existe sempre; o span `aos.control.budget_warning` exige `AOS_OTLP_ENDPOINT`, sem a qual o tracer é o `NoopTracer`), e **erro explícito** em vez de 0% silencioso quando não há fonte **ou quando o ledger tem turnos mas somou zero tokens** (`ErrBurndownNoUsage`).
- [x] ADR-021: o router ordena por scoring determinístico com tabela de pesos assinada; guard-test prova função pura; nenhum peso elege candidato cross-border (AOS-269). — `routing/scoring` + `policy/weights` (assinada, trust anchor pinado) + `router.WithScoring` (opt-in declarado); cenários 6–8 e 2 meta-testes novos no gate `ci-routing`.
- [x] ADR-022: PlanDocument aceita arestas condicionais, `role: verifier` e payload tipado — validador puro rejeita ciclo disfarçado, auto-verificação e taint incompatível (AOS-270..273). O schema é agora um artefacto **versionado**: `plan_version` `1.2.0` com a linha e a **janela de suporte declaradas** (`tecnica/18` §3.6.1), documentos das linhas anteriores a reproduzirem-se **byte-a-byte** e MAJOR fora da janela recusado nas duas vias (AOS-273). *Alcance honesto:* o que fica por ligar é **wiring do ciclo-de-vida do run** (AOS-238) — emissão do veredicto (DEF-272) e transporte do payload (DEF-273) —, não admissão nem contrato.
- [ ] Gates CI verdes; cobertura sem regressão (pisos AOS-199).

## 4. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências | Fecha |
|---|---|---|---|---|---|---|
| AOS-245 | Step-ledger selado por-titular por-`Apply` | fix | M | **P0** | — | F1 |
| AOS-246 | Breaker: erro de construção fatal no arranque + saneamento das envs de velocidade | fix | S | **P0** | — | F2 |
| AOS-247 | Guarda de produção no fallback de credencial dev do GW | fix | S | **P0** | — | F5 |
| AOS-248 | Banner de verdade: budget/broker/modelo/autonomia + `WithSink` no autonomy registry | fix | S | **P0** | — | F11, F14 |
| AOS-249 | Higiene Vault DSAR: validação de esquema + renovação de token | fix | M | P1 | — | F6 |
| AOS-250 | Clamp de `MaxTurns` no submit (`AOS_MAX_TURNS`) | fix | S | **P0** | — | F15 |
| AOS-251 | Breaker efectivo: `observeAction` ligado + claim `ready→running` no arranque do run + teste de trip | fix | M | **P0** | — | F3 |
| AOS-252 | Estados terminais duráveis (`complete`/`failed`) + caller de `CheckDeadlines` | feature | M | **P0** | — | F4 |
| AOS-253 | Crash-resume: varredura de runs órfãos + `Resumer` composto | feature | L | P1 | AOS-252 | F9, F13 |
| AOS-254 | Saga/compensação com construtor de produção | feature | M | P2 | AOS-252 | F12 |
| AOS-255 | Declaração tool-only/token-only: texto do banner + doc | docs | S | **P0** | — | §7.1 |
| AOS-256 | Ciclo de vida do nó de orçamento por-run (`AddNode` + release no seam por-run) | feature | S | P1 | — | A1-risco 4 |
| AOS-257 | `BudgetCheck` no lugar do stub + `Settle` como decorator do `ActivityDispatcher` + envs `AOS_BUDGET_*` (tokens) | feature | M | P1 | AOS-256 | billing |
| AOS-258 | Estimador real via `CallContext` + teste de nó com permit (não-vacuoso) | feature | M | P1 | AOS-257 | billing |
| AOS-259 **(FECHADO)** | Canal de custo: campo em `port.Usage` + `translateResponse` + dedup por parentesco + `Cost` em `ProductionConfig`/wiring | feature | M | P2 | D2 | A1-risco 2, A2-E |
| AOS-260 **(FECHADO)** | Admissão do turno de modelo (reservar antes de `loop.go:549`) | feature | L | P2 | AOS-259, D1 | A1-risco 1 |
| AOS-261 | Retentor de spans por-run + resolvedores `runID→traceID/treeID` (ou `BurndownSource` por ledger de turnos) | feature | M | P1 | — | A2-C/F |
| AOS-262 | Burn-down + aviso de exaustão no nó (sem decisão) + `AOS_PROGRESS_THRESHOLD` fail-closed | feature | M | P1 | AOS-261, AOS-257 | A2 parcial |
| AOS-263 | Prompt de exaustão durável: 2.º tipo de `PendingRecord` + rota autenticada + autoridade do `extend` | feature | L | P2 | AOS-262, D4/D5/D6 | A2 |
| AOS-264 | Broker: passo zero de política/identidade + cliente Vault real em `internal/vault` + `AOS_BROKER_VAULT_*` | feature | L | P2 | D7/D8 | A3 |
| AOS-265 | Broker: porta de aquisição com contexto (principal/run) + consumo in-process (v1) | feature | M | P2 | AOS-264 | A3 |
| AOS-266 | Attestation: wiring de `ChallengeIssuance` + `DeviceEnrollment` (`AOS_DEVICE_ENROLLMENT_FILE`) + banner | feature | M | P1 | — | F10 |
| AOS-267 | Scheduler interno de retenção (ticker no loop de serviço) | feature | S-M | P2 | — | Grupo B |
| AOS-268 | Verificação ancorada do WORM: checkpoint assinado no restart (env trust anchor) | feature | M | P2 | D4/AOS-156 | Grupo C |
| AOS-269 | **ADR-021** — Scoring determinístico no GW: portas de factores (ponto-fixo) + tabela de pesos assinada + guard-tests | feature | L | P1 | ADR-021 | — |
| AOS-270 | **ADR-022** — PlanDocument: arestas condicionais declarativas (sem ciclo) + avaliação pura no despacho | feature | L | P2 | ADR-022, AOS-231/238 | — |
| AOS-271 | **ADR-022** — `role: verifier` com semântica de sistema (read-only, produtor≠verificador, veredicto estruturado) | feature | M | P2 | AOS-270 | — |
| AOS-272 | **ADR-022** — Payload tipado por aresta (`outputs`/`consumes`, validação tipo+taint) | feature | M | P2 | AOS-270 | — |
| AOS-273 | **ADR-022** — `plan_version` bump + migração (`planmigrate`) + golden-sets/adversariais das extensões | test | M | P2 | AOS-270..272 | — |
| AOS-274 | Produtor de SLOs/alertas em runtime (loop avaliador no nó) | feature | M | P2 | — | F8 |
| AOS-275 | Promotion controller: endpoint `POST /promote` autenticado | feature | M | P2 | AOS-096 | F7 |
| AOS-276 | Keypool do GW: fusível RPM de 120 chamadas/vida — janela real **ou** tecto declarado no LiteLLM | fix | S | **P0** | D11 | F17 |
| AOS-277 | Knobs de ingresso por env (token-bucket + tecto in-flight) + teste do 429 | feature | S | P1 | — | A5-passo 2 |
| AOS-278 | Estágio de identidade real do GW (substituir o stub `nodeModelAuthn`) | feature | M | P2 | D4/EPIC-16 | F18 |
| AOS-279 | Golden-set do planeador no gate de CI: ligar `plannerprompt.Evaluate` ao harness de `packages/platform/eval` (ou estender `evalgate.sh` a este conjunto), com o pass-rate a entrar no `AOS_EVAL_REPORT` | test | S-M | P3 | AOS-241, AOS-273 | DEF-276 — **ENTREGUE 2026-08-13** |
| AOS-280 | Compor o estágio de roteamento no pipeline do GW: `failover` → `routingstage`+`router` (scoring armado), com classificador de produção que povoa candidatos e perfil | feature | L | P2 | AOS-269, ADR-021 emenda 1.1 | DEF-271 — **ENTREGUE 2026-08-13** (residuais DEF-280-PORTAS/TOKENS/NO) |

---

## AOS-245 — Step-ledger selado por-titular por-`Apply`

### Contexto
**F1 (alta).** `bootstrap.go:825` compõe `durable.NewStepLedger(es, durable.WithContentSealer(contentCipher))` **sem** produtor; a selagem só corre com `producer.NHIID != ""` (`step_ledger.go:346-347`); `WithProducer` tem zero chamadores não-teste. Os mesmos bytes ficam cifrados em `replay.captured` e **em claro** em `step.ledger.applied`, fora do crypto-shredding. Activo com `AOS_DURABLE_EXECUTION=1` — exigida em produção. Fonte: desafio A3.

### Objectivo
Propagar o titular **por-`Apply`** (o ledger é um por processo, partilhado por todos os runs — não um produtor global), no molde de `tc.Subject` no capturer (`loop.go:384`).

### Critérios de Aceitação
- [ ] `step.ledger.applied` selado por-titular sempre que o run tem `Subject` resolvível; run sem subject ⇒ fail-closed ou registo sem payload (decisão documentada).
- [ ] `POST /dsar/erase` e a expiração TTL tornam o ledger indecifrável (`ErrDecrypt`) **e** o `replay.captured` — prova nos dois registos.
- [ ] Verificação pós-shred da hash-chain (AOS-221) continua verde; teste de nó com restart.
- [ ] Compatibilidade de leitura com WALs escritos pelo formato anterior (ou migração declarada).

### Estado
**IMPLEMENTADO — adoptado após a W0** (o `Estado` anterior descrevia a não-entrada na W0 e ficou por reconciliar). O step-ledger sela o `Result.Payload` por-titular (o titular chega agora ao cifrador; o capturer regista `subject→stream` no DSARIndex) e o shred/expire apaga ambos os registos. Prova: `packages/cmd/aos/aos245_ledger_titular_test.go`. Ver o Balanço (§0).

---

## AOS-246 — Breaker: erro de construção fatal no arranque + saneamento das envs de velocidade

### Contexto
**F2 (alta, fail-open).** O nó nunca cabla `VelocitySource` (`breaker_wiring.go:70-72`); `NewBreaker` devolve `ErrVelocitySourceMissing`; o `resolve()` **engole** o erro (`breaker_wiring.go:73-77`) — ligar `AOS_BREAKER_MAX_*_PER_SEC` desliga o disjuntor inteiro, em silêncio. Fonte: análise 08-08, confirmado pelos desafios A1/A2 (D-A1.4: recomendação B+C).

### Objectivo
Tornar o erro de construção do breaker **fatal** quando a config o exige; anotar/retirar as envs de velocidade da doc até existir fonte.

### Critérios de Aceitação
- [x] Velocidade > 0 sem `VelocitySource` ⇒ **aborta o arranque** com erro nomeado (nunca `return nil`).
- [x] `deploy/node/README.md` deixa de recomendar «ligar quando tiver dados» sem a ressalva; a env é marcada como inócua-até-fonte ou removida da tabela.
- [x] Teste: boot com a env definida falha fail-closed; boot sem ela compõe o breaker normal.

### Estado
**IMPLEMENTADO — auditado (qualidade + completude) e remediado.** O gate vive em
`newRunBreakers` (arranque), não no `resolve` por-run: interroga o `ThresholdProvider` com a
MESMA `breakerClass` que o `resolve` usará e devolve `ErrBreakerVelocitySourceUnwired`, que
`Bootstrap` propaga até `os.Exit(1)`. `deploy/node/README.md` declara as duas envs como
`0 (desligado; NÃO ligável hoje)` e diz que `>0` ABORTA. Testes fail-closed e de composição
normal em `aos246_breaker_failclosed_test.go`.

Residual fechado na remediação (achado F-A7): o `return nil` do `resolve` — inalcançável por
configuração depois do gate — denunciava-se com um `sync.Once` **por processo**, devolvendo o
silêncio de F2 aos runs 2..N. Passou a **uma denúncia por run** (`runBreakers.reported`, podado
pelo `forget`): cada run que corra sem disjuntor deixa rasto.

---

## AOS-247 — Guarda de produção no fallback de credencial dev do GW

### Contexto
**F5 (média).** Sem `AOS_MODEL_API_KEY_PATH`, o nó arranca — mesmo em `AOS_MODE=production` — a apresentar o bearer de dev `aos-dev-omniroute` embebido no binário (`modelgatewaywiring.go:78-81`). Não declarado. Fonte: desafio A3.

### Objectivo
Em produção, a ausência do ficheiro de credencial do modelo é fatal; fora de produção, o fallback é declarado no banner.

### Critérios de Aceitação
- [x] `AOS_MODE=production` sem `AOS_MODEL_API_KEY_PATH` (com gateway ligado) ⇒ arranque aborta com erro nomeado.
- [x] Modo referência com fallback ⇒ linha de banner declara «bearer de DEV em uso».
- [x] Testes nos dois sentidos.

### Estado
**IMPLEMENTADO — auditado (qualidade + completude), sem achados.** `ErrProductionNeedsModelCredential`
em `parseModelFromEnv(production)`, com a postura vinda da ÚNICA leitura de `AOS_MODE`
(passada por parâmetro, não relida). `devModelCredentialBanner` declara o fallback e é chamada
no arranque real com o predicado do estado COMPOSTO (`cfg.Model != nil`), não da intenção da
config. Sete casos em `aos247_model_credential_test.go` cobrem os dois sentidos, incluindo
asserções de NÃO-VAZAMENTO: o bearer de dev é obtido em runtime do próprio provider (sem
literal no ficheiro de teste) e assere-se a sua ausência do banner sem nunca o imprimir.

---

## AOS-248 — Banner de verdade: budget/broker/modelo/autonomia + `WithSink` no autonomy registry

### Contexto
**F11 (média, uma linha):** `buildAutonomyOracle` chama `NewLevelRegistry()` sem `WithSink` — mudanças de nível de autonomia não ficam registadas. **F14 (baixa):** o banner é mudo sobre budget (stub), broker (ausente) e modelo/gateway (real vs referência) — diverge da disciplina AOS-203. E `AOS_AUTONOMY_LEVELS` não tem linha (sem ela nenhum `escalate` é emitido). Fontes: desafios A3/A4, análise 08-08.

### Objectivo
Postura anunciada = postura ligada, para estas quatro superfícies.

### Critérios de Aceitação
- [x] `WithSink` ligado ao WORM composto; `SetLevel` de provisionamento selado com actor e motivo.
- [x] Banner declara: orçamento (NÃO COMPOSTO — eixo AOS-008/EPIC-20), broker (ausente — credenciais por ficheiro), modelo (gateway real vs `referenceModel`), autonomia (oráculo ligado/nil).
- [x] `env_surface_test` e testes de banner actualizados; nenhuma linha promete o que não está ligado.

### Estado
**IMPLEMENTADO — auditado (qualidade + completude) e remediado.** Registo construído com
`autonomy.WithSink` sobre um sink de LIGAÇÃO TARDIA que NEGA (`ErrAutonomySinkUnbound`) até
`Bootstrap` o ligar ao WORM composto — o mesmo store que `audit.VerifyStore` re-encadeia; a
selagem recusada aborta o arranque. As quatro linhas de banner saem do arranque real.

Três correcções de remediação, todas sobre afirmações não amarradas ao código:

- **F-A3** — a lista de credenciais do broker dizia-se EXAUSTIVA e omitia três chaves privadas
  que o nó lê do disco e retém em memória: `AOS_ISSUER_KEY_PATH` (a chave ed25519 de assinatura
  da autoridade co-localizada), `AOS_TLS_KEY_PATH` e `AOS_OTLP_CLIENT_KEY_PATH`. Um operador que
  lesse a lista à letra sub-dimensionava a rotação. As três passaram a estar declaradas, como
  família distinta das credenciais downstream.
- **F-A6** — «cada um SELADO» derivava de `len(w.specs)` (o DECLARADO). Passou a derivar de
  `autonomyWiring.sealedPairs` (os pares que o `SetLevel` aplicou E selou), com um TERCEIRO ramo
  de banner para a cablagem construída-e-não-provisionada; a cardinalidade conta pares distintos,
  não entradas. A guarda de teste estava invertida (chamava o banner sem provisionar e validava a
  linha «SELADO») — foi substituída pela guarda na direcção certa.
- **AC3** — `runWithoutTouchingBoardRegions` afirmava fixar TODA a superfície de ambiente com uma
  lista de 14 nomes escrita à mão, deixando `AOS_MODEL_*`, `AOS_AUTONOMY_LEVELS`,
  `AOS_POLICY_BUNDLE_DIR` e `AOS_BREAKER_*` herdados da máquina. A lista passou a ser DERIVADA do
  extractor AST do gate de AOS-203 (`envVarsReadBySources`): uma variável nova é fixada sozinha.

---

## AOS-249 — Higiene Vault DSAR: validação de esquema + renovação de token

### Contexto
**F6 (média).** `parseVaultDSARFromEnv` não valida o esquema de `AOS_DSAR_VAULT_ADDR` (o gémeo de attestation no mesmo binário exige https-ou-loopback); o token é lido uma vez e nunca renovado — tokens curtos (recomendados pelo README) matam a custódia silenciosamente, com `/readyz` verde (sonda não autenticada). Fonte: desafio A3 (eixo AOS-215/216).

### Objectivo
Validação de esquema fail-closed; renovação periódica do token (ou falha ruidosa antes da expiração).

### Critérios de Aceitação
- [x] `AOS_DSAR_VAULT_ADDR` não-https fora de loopback ⇒ aborta (molde `checkRemoteAttestationURL`).
- [x] Token renovado em background (molde do sweeper de aprovações) ou `/readyz` falha **antes** da expiração — nunca morte silenciosa da custódia.
- [x] Teste: token expirado ⇒ readiness vermelho e erase/expire fail-closed.

### Estado
**IMPLEMENTADO — pendente de auditoria.**

**(a) Esquema.** O critério de `checkRemoteAttestationURL` foi EXTRAÍDO para
`integration.CheckSecureTransportURL` (exportado) e o gémeo de attestation passou a delegar nele;
`parseVaultDSARFromEnv` chama o MESMO predicado e aborta com `ErrInsecureVaultDSARAddr`. Não há
segunda cópia do critério — era a divergência que o desafio A3 apanhou. Guarda
`TestAOS249_VaultAddrCriterionMatchesAttestationTwin` falha se os dois lados divergirem.

**(b) Token.** O `vaultKeyVault` deixou de congelar o token do arranque: guarda o CAMINHO do
ficheiro montado, re-lê-o (adopta um token rodado por AppRole/Kubernetes-auth sem reiniciar),
mede a validade por `auth/token/lookup-self` (**autenticado**) e renova por
`auth/token/renew-self` quando o TTL entra no dobro da margem. O laço é
`vaulttoken_renewer.go`, no molde EXACTO de `approval_sweeper.go` (ticker + `sweepStop` do
Shutdown), ligado em `NewNodeService`.

**(c) A denúncia.** `ready()` deixou de sondar só `/v1/sys/seal-status` (não-autenticado, verde
com o token morto): passou a exigir também o `lookup-self` e uma **margem**
(`AOS_DSAR_VAULT_TOKEN_MIN_TTL`, default 5m) — o `/readyz` fica **503 ANTES** da expiração, não
depois. `aos_dsar_vault_ready`/`aos_ready` seguem o mesmo veredicto.

**(d) Erase/expire.** `Delete` (o funil único do `/dsar/erase` e da expiração de retenção)
VERIFICA a destruição em vez de a assumir; uma destruição por confirmar mantém o nó UNREADY
(`ErrVaultShredUnconfirmed`) até ser confirmada — uma afirmação falsa de irrecuperabilidade
perante o titular deixa de ser possível em silêncio.

Envs novas na tabela AOS-203 (`deploy/node/README.md`): `AOS_DSAR_VAULT_TOKEN_MIN_TTL`; a linha de
`AOS_DSAR_VAULT_ADDR` passou a declarar a exigência de https e o exemplo `http://vault:8200` foi
substituído por loopback.

---

## AOS-250 — Clamp de `MaxTurns` no submit

### Contexto
**F15 (ALTA, promovido pelo desafio A5).** `max_turns` vem do corpo do `POST /runs` sem limite superior (`api.go:610` copia cru; `WithMaxTurns` tem zero chamadores de produção). Composto com o fusível do keypool (F17/AOS-276), **um único `POST /runs {max_turns: 200}` esgota o `LimitRPM=120` e desliga o nó para todos os runs** — o rate-limit de ingresso não protege (1 pedido), o tecto de in-flight não protege (1 run), o breaker está inerte (F3). Fontes: desafios A1 (risco 8) e A5 (F2).

### Objectivo
Tecto de operador para `MaxTurns` na fronteira de ingresso — node-local, sem tocar em módulos proibidos.

### Critérios de Aceitação
- [ ] `AOS_MAX_TURNS` (default 16) na tabela AOS-203; pedido acima ⇒ clamp declarado na resposta/log, ou 422 (decisão registada no ticket).
- [ ] Teste de API: `max_turns=200` ⇒ clamp aplicado; ausente ⇒ default; inválido ⇒ fail-closed.

### Estado
**IMPLEMENTADO — adoptado após a W0** (o `Estado` anterior descrevia a não-entrada na W0 e ficou por reconciliar). `apiMaxTurnsOptionFromEnv()` lê `AOS_MAX_TURNS` e clampa `max_turns` na fronteira de ingresso, resolvido ANTES de compor o serviço — valor malformado **aborta o arranque** em vez de degradar para o default (`packages/cmd/aos/main.go`). Ver o Balanço (§0).

---

## AOS-251 — Breaker efectivo: `observeAction` ligado + claim `ready→running` + teste de trip

### Contexto
**F3 (alta), dois mecanismos independentes:** (a) `observeAction` (`breaker_wiring.go:83`) tem zero chamadores ⇒ no-progress nunca dispara; (b) `Observe` é no-op fora de `Running` (`breaker.go:213-215`) e o lazy-claim só transita no primeiro steer/escalada (`steer_gates.go:117-118,141-142`) ⇒ um run comum fica em `ready` do princípio ao fim. Fontes: análise 08-08 + desafios A2/A4 (verificados à mão). Divergência com o desafio A1 registada no relatório §9 — este ticket inclui o teste que a resolve.

### Objectivo
O breaker dispara no run comum: alimentar o detector e armar o claim no arranque do run.

### Critérios de Aceitação
- [x] `observeAction(runID, hash)` chamado no fecho do `execute_tool` (reusa `agentruntime.AttrToolCallHash`); teste negativo que falha se ficar sem chamadores.
- [x] O run transita `ready→running` no início da execução (não no primeiro pause/escalada); máquina de estados e fencing ajustados.
- [x] **Teste de nó:** a mesma call negada repetida ⇒ trip do breaker **antes** de `MaxTurns`, com veredicto atribuído no WORM.
- [ ] Divergência A1↔A2/A4 resolvida por este teste e registada no relatório.

### Estado
**IMPLEMENTADO — pendente de auditoria.** (a) Nova porta `agentruntime.ActionObserver`
(`WithActionObserver`) invocada no fecho de cada mediação em `mediateToolCall` com
`otelgenai.CanonicalToolCallHash` — a mesma âncora do span execute_tool; o bootstrap liga-a
por method value a `runBreakers.observeAction`. Guarda AST anti-regressão
(`TestAOS251_ObserveActionHasProductionCaller`, falsificabilidade provada). (b) Claim
`ready→running` no arranque do run em `hostRun` (`runGate.claimRunning`, razão
`run_start_claim`, fencing token do lease; no-op idempotente na retoma; fail-closed se a
transição falhar); o lazy-claim de AOS-218 mantém-se como fallback do caminho directo
`Runtime.Run` sem serviço. (c) Teste de nó `TestAOS251_BreakerTripsOnDeniedLoop`: deny-loop
⇒ trip no turno 5 (< MaxTurns=12), alvo `paused`, razão `breaker_no_progress` relida do log
durável por máquina fresca; controlo `TestAOS251_RunWithProgressDoesNotTrip`. Falta só o
registo formal da divergência A1↔A2/A4 no relatório de prontidão §9 (artefacto documental,
fora do código).

**Remediação (achado F-A1, suite vermelha).** O controlo assertava que a máquina ficava em
`running` no FIM de um run bem-sucedido. Era verdade quando o claim era a única transição que
esse caminho escrevia; com o selo terminal de AOS-252 na mesma branch, o mesmo caminho de
saída escreve `running→complete` ANTES de o `Wait` devolver (o `defer` do selo está a montante
do `defer s.finish(rs)` na pilha LIFO de `hostRun`). A asserção tinha passado a exigir que o
desfecho do run NÃO ficasse no log durável — o defeito F4 que AOS-252 fecha. O invariante de
AOS-251 passou a ser asserido onde vive: a **aresta** `ready→running` com a razão
`run_start_claim`, relida do stream durável, mais o estado final `complete`. Prova a coexistência
dos dois mecanismos, que nenhuma das duas waves teria produzido isolada.

---

## AOS-252 — Estados terminais duráveis + caller de `CheckDeadlines`

### Contexto
**F4 (alta).** `grep state.Complete|state.Failed` não-teste: vazio. O nó conduz 5 das 13 arestas; `CheckDeadlines` (`state/machine.go:550`) tem zero chamadores de produção apesar de `liveness/doc.go` exigir execução periódica. O desfecho vive num mapa em memória com poda FIFO — um run acabado por erro/panic/MaxTurns é, no log durável, indistinguível de um crash. Fonte: desafio A4.

### Objectivo
Escrever `complete`/`failed` em todos os caminhos de saída do loop; correr `CheckDeadlines` periodicamente (molde do sweeper de aprovações).

### Critérios de Aceitação
- [x] Todo o fim de run (sucesso, erro, MaxTurns, breaker, panic recuperado) escreve o estado terminal no log durável.
- [x] `CheckDeadlines` com caller periódico composto no loop de serviço; `running→timed_out` materializado **e o run interrompido**.
- [x] Teste: crash simulado vs fim normal distinguem-se no log; `GET /runs/{id}` reflecte o desfecho durável após restart.

### Estado
**IMPLEMENTADO — pendente de auditoria.** (CA3 entregue na W1, fechando o parcial abaixo.)
`runGate.sealTerminal` sela no ponto único de saída (`hostRun`), a montante dos defers de
libertação e do recover de isolamento — no-op fora de `running`, para não reescrever os
desfechos que outros condutores (steer/breaker, escalada, deadlines) já materializaram.
`sweepDeadlines` é o caller periódico que `CheckDeadlines` nunca teve. CA3: o GET
`/runs/{id}` ganhou fallback durável (`NodeService.DurableState` + mapeamento em
`handleGet`: complete ⇒ completed/terminated; failed/timed_out/killed/paused pelo nome do
estado); `aos252_terminal_states_test.go` prova a distinção crash (claim sem desfecho) vs
fim normal no log e o GET pós-restart sobre o mesmo substrato (completed sem nada em
memória; crashado continua 404 — a retoma de órfãos é AOS-253).

**Remediação (achado F-A5, fail-open).** O varrimento marcava `running→timed_out` e deixava o
run A CORRER: o operador lia um estado terminal e parava de olhar enquanto o agente continuava
a emitir tool calls, com o disjuntor cego (`Observe` é no-op fora de `running`) e o selo terminal
já a no-op. Um timeout que não interrompe é pior do que timeout nenhum. O varrimento passou a
cancelar o contexto do run (`rs.cancel` — o MESMO mecanismo do `Shutdown` e do heartbeat de posse
perdida, não uma segunda via de paragem). Prova de nó em `aos252_deadline_interrupt_test.go`: um
run preso a meio do turno (o modelo só devolve quando o ctx é cancelado) sai, com `timed_out` no
log; falsificabilidade verificada — sem o cancelamento o teste não termina.

---

## AOS-253 — Crash-resume: varredura de runs órfãos + `Resumer` composto

### Contexto
**F9+F13 (média).** O `Resumer` (AOS-015) nunca é composto — checkpoints escritos, nunca lidos; `worker.Assigner.TryAcquire` só corre no submit; nenhuma varredura de arranque. Um crash não é retomado por ninguém; re-submeter recomeça do turno 1. Fontes: análise 08-08, desafio A4 (item 6; eixo AOS-015/099).

### Objectivo
No arranque do serviço, varrer streams com estado não-terminal, reconstruir o cursor (checkpoints + ledger) e retomar sem re-executar efeitos.

### Critérios de Aceitação
- [x] Varrimento no arranque reclama runs interrompidos (lease expirado, sem estado terminal) — depende de AOS-252 para distinguir órfão de terminado. `NodeService.ResumeInterruptedRuns` (`crash_resume.go`) enumera os streams do Event Store, reconstrói o estado durável de cada um pela máquina de AOS-017 e só age sobre `running` (o rasto de crash de AOS-252); a posse é reclamada pela MESMA `worker.Assigner.TryAcquire` (via `submit`), que SALTA sem roubo um lease vivo noutra réplica (`ErrRunLeaseHeldElsewhere`). Ligado no arranque em `main.go`, entre `NewNodeService` e `Serve`.
- [x] Retoma pelo `Resumer` continua do último checkpoint; efeitos não se repetem (dedup provado). O `durable.Resumer` (AOS-015) — a peça que nunca fora composta — é agora construído na varredura e os checkpoints passam a ser LIDOS no arranque (reconstrói o cursor/próximo-turno). A retoma SEM re-executar efeitos reutiliza o replay-then-continue de AOS-021 (`replayPlanFor` + `submit(withReplayPlan…, resuming=true)` → `hostRun` chama `RebuildLedger`): o already-applied do step-ledger PRECEDE a mediação, pelo que os efeitos capturados não repetem. Provado em `aos253_crash_resume_test.go` (contador partilhado fica em 1 após a retoma).
- [x] Teste de nó: kill a meio → restart → run completa sem double-execution e sem re-interrogar o modelo nos turnos já capturados. `TestAOS253_CrashResumeScanCompletesWithoutDoubleExecution` (cadeia REAL `obsPermitNodeWith`, substrato + KEK partilhados entre duas incarnações): o turno 1 aplica o efeito e "crasha" (`running` sem selo terminal); o nó NOVO retoma pela varredura, o run COMPLETA na continuação ao vivo (turno 2), o efeito NÃO repete (contador=1) e o modelo NÃO é interrogado no turno 1 (reproduzido do plano de replay). `TestAOS253_HostRunSeedsCrashResumeRecordAtStart` tranca a metade de produção (o hostRun semeia o registo de retoma no arranque de cada run, não só na escalada).
- [x] Banner declara o resultado da varredura (N runs retomados). `crashResumeBanner`/`crashResumeDisabledBanner` (funções puras) declaram N órfãos vistos, N retomados, N saltados por lease vivo e N fail-closed — e a postura DESLIGADA com a razão quando o substrato falta. Trancado em `TestAOS253_CrashResumeBannerDeclaresResult`.

### Estado
**FECHADO** (feature/epic20-consolidacao). Peças ligadas (nenhuma reinventada): `durable.Resumer` (AOS-015) + `worker.Assigner` (AOS-018) + estados terminais (AOS-252) + replay-then-continue (AOS-021). Alcance honesto declarado no banner: a retoma automática NÃO traz credencial fresca (um crash não tem humano no lacete); os turnos já capturados dispensam-na (already-applied precede a mediação), mas uma continuação AO VIVO que exija identidade de modelo (AOS-278) é NEGADA atribuivelmente — sem principal forjado.

---

## AOS-254 — Saga/compensação com construtor de produção

### Contexto
**F12 (média).** `kernel/agent-runtime/saga` está no fecho do nó mas `SagaCoordinator`/`WithCompensationRegistry` só têm chamadores de teste; sem `failed` (F4) a aresta de compensação é duplamente inalcançável. Fonte: desafio A4 (item 3).

### Objectivo
Compor a compensação no caminho de falha durável, para efeitos reversíveis registados.

### Critérios de Aceitação
- [~] `WithCompensationRegistry` chamado na composição de produção **[x]**; `failed→compensating` alcançável **[ ]** — ver o Estado: falta o **produtor** de compensações (o registo está sempre vazio), pelo que a transição é hoje código morto. Eixo **DEF-270**.
- [x] Abort com efeitos aplicados acciona compensação ou declara explicitamente a sua ausência (span + WORM) — o ramo alcançável (**declarar a ausência**) está entregue e provado.
- [x] Teste de composição pela cadeia real — `aos254_saga_compensation_test.go` (Bootstrap/`NewSecuredRuntime`, sem doubles).

### Estado
**PARCIAL — a CONDUÇÃO está entregue e provada; a COMPENSAÇÃO REAL fica deferida (DEF-270).** O nó compõe o `saga.SagaCoordinator` na cadeia de produção (`Node.Compensations` + `WithCompensationRegistry` no dispatcher durável) e `sealTerminalState` conduz o desfecho `failed` — a única origem da saga — para `driveSagaCompensation`. O **único caminho alcançável hoje** está trancado por teste de nó pela cadeia REAL: com o registo VAZIO (a postura de produção), a ausência de compensação é **DECLARADA** (span + selo WORM, `reason=saga_no_compensation_registered`, `Decision=Deny`, titular atribuído) e o run **permanece `failed`** — nunca em silêncio.

**Porque NÃO se declara fechado (honestidade de alcance):** faltam duas peças sem as quais `failed→compensating` é inalcançável — (a) um **PRODUTOR** de compensações (o loop base nunca popula `activity.Activity.Compensation`, logo o registo está sempre vazio); (b) o registo **RUN-SCOPED** (a struct `saga.Compensation` do kernel carrega só `StepID`; com compensações de vários runs, o gate `Len()==0` e o `Reversed()` global fariam um run compensar efeitos de OUTRO — landmine hoje inalcançável por (a), fechá-lo exige mudar o kernel). Testar aqui um caminho que a produção não percorre seria composição sem alcance real — o oposto da invariante do epic. Eixo: **DEF-270**.

---

## AOS-255 — Declaração tool-only/token-only (texto)

### Contexto
Desafio A1 (D-A1.1/D-A1.2, recomendações): um orçamento que cubra tool calls sem cobrir o turno de modelo é uma capacidade-fantasma **se o banner não o disser**. A v1 é TOOL-ONLY e TOKEN-ONLY — declarado, não fingido.

### Objectivo
Fixar o texto do banner e da doc **antes** de qualquer wiring de budget.

### Critérios de Aceitação
- [x] Texto aprovado: «orçamento: cobre tool calls em TOKENS; o gasto de inferência é travado por tempo (wall-clock), não por tecto».
  **REESCRITO por AOS-260** (a frase descrevia um eixo aberto, e o eixo fechou): «orçamento: cobre tool calls E o turno de modelo em TOKENS — reserva antes da inferência, saldo pelo consumo medido; o tecto em dólares é opcional e só decide quando configurado». A disciplina é simétrica: enquanto a inferência esteve fora do tecto o banner tinha de o dizer; agora que está dentro, manter a frase velha seria a promessa a **menos**, e um operador que a lesse desligaria protecções por causa de um texto obsoleto.
- [x] `deploy/node/README.md` e o relatório de prontidão referem a declaração.

### Estado
**IMPLEMENTADO.** A frase é a constante `BudgetScopeDeclaration` (`packages/cmd/aos/posture_banner.go`)
e entra nos **dois** estados de `budgetPostureBanner(composed bool)` — no estado composto porque é
o alcance, e no NÃO-composto porque o operador precisa de a ler *antes* de ligar o orçamento, não
depois. O parâmetro é novo: a linha deixou de ser incondicional para que AOS-257 mude o **estado**
sem reescrever a **declaração**. Documentos: secção «Orçamento / tecto de custo — o alcance
declarado» em `deploy/node/README.md` e §7 do relatório de prontidão.

Gate em `packages/cmd/aos/aos255_budget_scope_test.go`: a frase aprovada nos dois estados do
banner **e** nos dois documentos; lista de formulações de *over-claim* proibidas («todo o gasto»,
«cobre a inferência», …); e o guard `TestAOS255CallSiteMatchesComposition`, que avermelha se
alguma árvore (`cmd/aos`, `integration`) passar a importar `control-plane/budget` enquanto o
composition-root continuar a chamar `budgetPostureBanner(false)` — a mentira simétrica (negar por
escrito um orçamento que já decide) fica mecanicamente impedida; e, desde a remediação da wave,
avermelha também com um `budgetPostureBanner(true)` **hardcoded** (a mentira mais cara: anuncia
protecção que pode não estar composta) e com a **remoção** da chamada (voltar ao silêncio).
**Neste ticket** nenhum *budget* foi ligado e nenhuma env nova foi introduzida — o wiring e as
variáveis chegaram em **AOS-256/AOS-257**, nas duas secções abaixo.

---

## AOS-256 — Ciclo de vida do nó de orçamento por-run

### Contexto
Desafio A1 (risco 4): compor o hook sem registar o nó do run ⇒ `ErrUnknownNode` ⇒ **100% das tool calls negadas**. O seam por-run já existe: `integration/secured.go:460` (`SecuredRuntime.Run`, já com `Freeze`/`defer Release` sobre `goal.RunID`).

### Objectivo
`AddNode(goal.RunID, treeID, limite)` no início do run e libertação no fim, no seam existente.

### Critérios de Aceitação
- [x] Cada run tem nó de orçamento registado antes do primeiro turno; release garantido (incl. panic/erro).
- [x] Limite vem de config (env de AOS-257) com default declarado.
- [x] Teste: dois runs concorrentes não partilham tecto (declarar na doc: tecto por-run, nunca por-mandato — D-A1.3).

### Estado
**IMPLEMENTADO.** O ciclo de vida vive no seam existente (`SecuredRuntime.Run`,
`packages/integration/secured.go`), a seguir ao `Freeze`/`defer Release` do tool set:
`s.budget.acquire(goal.RunID)` regista o nó **antes do primeiro turno** e a libertação é
`defer` — cobre retorno, **erro** e **panic**. Fail-closed: se o nó não se consegue registar, o
run não arranca (correr sem nó seria correr com tudo negado).

O `AddNode`/`RemoveNode` por-run vivem em `packages/integration/budget.go` (`RunBudget`); o
`RemoveNode` foi acrescentado ao `control-plane/budget` (era o único lado do ciclo que faltava:
sem ele cada run deixava um nó vivo e a **retoma** do mesmo `RunID` colidia com `ErrNodeExists`).
A **raiz da árvore é ilimitada em tokens** de propósito — um tecto de árvore seria um tecto
global, e o run B seria negado pelo gasto do run A (o oposto de D-A1.3).

**Limite e default:** vem de `AOS_BUDGET_MAX_TOKENS` (AOS-257); o default declarado é a
**ausência de tecto** — não se inventa aqui um número de tokens que o nó não sabe justificar
(mesma disciplina das velocidades de queima do disjuntor, AOS-246). Documentado em
`deploy/node/README.md` (índice de configuração + secção do alcance), incluindo **tecto por-run,
nunca por-mandato**.

**Testes** (`packages/integration/aos256_budget_lifecycle_test.go`): o **caminho feliz** pela
cadeia REAL (`TestAOS256_CaminhoFeliz_...` — a tool call atravessa identity→…→budget→egress e
EXECUTA), a sua **prova negativa** (`TestAOS256_SemNoRegistadoOHookNegaTudo`, que reproduz o
`E_UNKNOWN_NODE` do risco 4), libertação em retorno/erro/panic, e dois runs concorrentes com
tectos independentes.

---

## AOS-257 — `BudgetCheck` no lugar do stub + `Settle` decorator + envs (tokens)

### Contexto
O plano original foi corrigido pelo desafio A1: o `Settle` vive num **decorator do `ActivityDispatcher`** (padrão de `secured.go:399-401`), cobrindo também o caminho de erro (`runtime_ports.go:293-294`); as fugas reais são negações a jusante e erros, não «permit sem Commit».

### Objectivo
Substituir `BudgetStub{}` (`secured.go:324`) pelo `BudgetCheck` real, com o ciclo de vida completo e envs token-only.

### Critérios de Aceitação
- [x] `SecuredConfig` ganha campo de orçamento; o hook é composto quando configurado, stub declarado no banner quando não.
- [x] `Settle` no decorator: commit em permit, release em deny/escalate/erro — teste de não-fuga após deny do egress e após erro.
- [x] `AOS_BUDGET_MAX_TOKENS` (e equivalentes) na tabela AOS-203, fail-closed em valor inválido.
- [x] Dependente de AOS-256 (sem nó por-run, nega tudo — ordem obrigatória).

### Estado
**IMPLEMENTADO.** `SecuredConfig.Budget *RunBudget`: presente ⇒ o ponto de injecção `budget` da
cadeia passa a ser o `budget.BudgetCheck` real; nil ⇒ `referencemonitor.BudgetStub{}` como antes
— e é esse o estado que o banner declara, com o argumento a **derivar** do que foi composto
(`budgetPostureBanner(runBudget != nil)`), nunca de um literal.

**Settle no decorator** (`budgetSettlingDispatcher`, `packages/integration/budget.go`), OUTERMOST
em relação ao dispatcher durável: commit em `permit` sem `ToolErr`; release em `deny`, `escalate`,
**erro fatal do despacho** e **panic** (via `defer` com retornos nomeados). O saldo corre sobre
`context.WithoutCancel` — o caminho que mais precisa de libertar headroom é justamente o do
contexto cancelado. Num dedup/replay do step-ledger não houve mediação, logo não há reserva
pendente e o saldo é um no-op honesto.

**Env:** `AOS_BUDGET_MAX_TOKENS` (`packages/cmd/aos/budget_env.go`), documentada no índice de
`deploy/node/README.md` (gate AOS-203) e na secção do alcance. Valor ilegível/negativo/**`0`**
⇒ `ErrBadBudget`, **aborta o arranque** (o `0` merece a nota: não desliga o orçamento, negaria
todas as tool calls). **Não há equivalente em `$`** e a ausência é a decisão: sem o canal de
custo ponta a ponta (AOS-259) um tecto em dólares seria contado a zero — o estimador composto é
token-only (`TokenOnlyEstimator`, dimensão micro-USD zerada).

**Testes:** `packages/integration/aos256_budget_lifecycle_test.go`
(`TestAOS257_SemFugaAposDenyDoEgress` — pela cadeia REAL, com tecto apertado: a 1.ª call é negada
pelo **egress**, a jusante do orçamento, e a 2.ª só executa se o headroom tiver voltado;
verificado por mutação que o teste avermelha com o saldo desligado — e `TestAOS257_SaldoDoDecorator`,
que cobre permit/deny/escalate/erro/panic sobre o adaptador real) e
`packages/cmd/aos/aos257_budget_env_test.go` (default, valores inválidos, e a amarra
banner⇄composição).

**Alcance NÃO alargado:** continua TOOL-ONLY e TOKEN-ONLY. O banner do estado composto declara o
estimador **realmente composto** — o `integration.TokenOnlyEstimator` de AOS-258, por **átomos**
sobre a pegada inteira (argumentos na forma final **+** envelope `tool_id`/`capability`/
`resource`), em que `~1 token por 4 bytes` é o **piso** e não a fórmula. Declara também que o
tecto é **por-run E por-incarnação** (cada re-hospedagem recebe o tecto inteiro; a árvore é em
memória) — ver a remediação da wave.

---

## AOS-258 — Estimador real via `CallContext` + teste de nó com permit

### Contexto
Desafio A1 (esforço 9): `rm.Call` não transporta prompt/tokens — `WithEstimator` sozinho não chega. Alternativa mais barata: estimar fora do RM e passar por `CallContext`. E falta o teste que prove um **permit** com budget ligado (não só denies in-process).

### Objectivo
Estimador baseado no input materializado, injectado pela seam existente; prova não-vacuosa ponta a ponta.

### Critérios de Aceitação
- [x] Estimador real composto (documentado o que estima e o que não estima).
- [x] **Teste de nó:** run dentro do tecto obtém `permit` e a tool executa; run além do tecto é negado com `denied_by=budget` selado e atribuído.
- [x] O `DefaultEstimator` deixa de ser usado em produção (ou é declarado no banner).

### Estado
**IMPLEMENTADO.** O estimador vive em `packages/integration/budget_estimator.go`
(`TokenOnlyEstimator` — o nome de AOS-257 mantém-se, o corpo deixou de delegar no
`budget.DefaultEstimator`) e entra pela seam que já existia: `budget.WithEstimator`, chamada em
`NewRunBudget`. Estimar FORA do Reference Monitor é o que aqui se faz — a função vive na
composição, não no kernel, e lê a Call **já materializada** (pós-`EffectRewriter`), que é a
única forma de o RM «ver» o que vai correr.

**O que ESTIMA:** os ARGUMENTOS na forma FINAL do efeito **mais** o envelope que também ocupa
contexto (`tool_id`, `capability`, `resource` tipo/valor/região) — o `DefaultEstimator` só via
`call.Input`, pelo que uma call cujo peso está num URL de 300 bytes era estimada como grátis. A
contagem é uma aproximação **por átomos** sem dependências (ADR-017): corridas alfanuméricas
partidas a cada 4 caracteres, 1 token por sinal de pontuação/estrutura, 1 token por rune
não-ASCII, e o **piso** da heurística de bytes do estimador anterior — o piso existe para que a
troca **nunca baixe** uma estimativa (propriedade selada em `TestAOS258_NuncaSubestimaOPlaceholder`).

**O que NÃO estima**, declarado no banner e no README: o **turno de modelo** — este estimador
conta *tool calls*, e o *prompt* do turno é estimado pela **mesma contagem** do lado da admissão
do turno de modelo (`integration.ModelPromptTokens`, AOS-260), que o **reserva antes** da
inferência e o salda pelo consumo medido —, o **resultado da tool** (volta à transcrição mas só
é mensurável DEPOIS do efeito; o saldo confirma a RESERVA, não a medição) e **dólares** — o
estimador estima **tokens**, não preços, e continua a declarar `CostMicroUSD` a zero. Nota
pós-AOS-259: o canal de custo passou a alimentar a dimensão de dólares do **burn-down** (via
`turn.recorded`), mas isso é medição DEPOIS do turno; o que continua sem estimativa em dólares é
a **admissão ANTES** — eixo AOS-260. O alcance declarado em AOS-255 NÃO foi alargado.

**Alternativa REJEITADA, com a razão registada no cabeçalho do ficheiro:** ler uma estimativa
declarada pelo chamador em `CallContext.BudgetTokensRemaining`. Nos dois produtores reais
(`orchestrator/delegation.go`, `planner.go`) o campo é a fatia herdada/reserva de planeamento e
a sub-árvore do filho já é debitada por ancestralidade ⇒ **dupla contagem**; e no nó nenhuma tool
call do loop o preenche ⇒ campo lido que nunca decide, uma capacidade-fantasma. O sítio para uma
declaração honesta fica nomeado, e o combinador tem de ser um MÁXIMO com a pegada local.

**Teste de nó** (`packages/cmd/aos/aos258_budget_permit_node_test.go`), com `Bootstrap` REAL e o
tecto a entrar pela env do operador (`AOS_BUDGET_MAX_TOKENS`), três provas:
(1) **dentro do tecto ⇒ permit e a tool EXECUTA** — a prova não-vacuosa que faltava;
(2) **além do tecto ⇒ deny** com o registo `denied_by=budget` no WORM **atribuído**
(run/step/tool/capability/principal/reason) e **selado** (hash-chain verificada + `EntryHash`
recomputado do conteúdo); (3) **o mesmo run permite e depois nega** — duas calls idênticas com
tecto para uma só, o que amarra o permit e o deny ao MESMO nó de orçamento. Verificado por
MUTAÇÃO nos dois sentidos: sem composição do orçamento (2) e (3) avermelham; sem o nó por-run
registado (1) e (3) avermelham. Os tectos **derivam** de `TokenOnlyEstimator` (nunca constantes
calibradas à mão) — e por isso os tectos apertados de AOS-256/257 passaram também a derivar dele.

**Critério (c)** mecanizado em `TestAOS258_ProducaoNaoUsaODefaultEstimator` (AST, não grep: a
prosa nomeia o placeholder de propósito) sobre as duas árvores de produção. Nenhuma env nova.

---

## AOS-259 — Canal de custo ponta a ponta (contrato `port` + dedup por parentesco)

### Contexto
Desafios A1 (risco 2) e A2 (achado E): `port.Usage`/`ChatResponse` não têm campo de custo (o RT soma zeros); e ligar `WithCost` cria dois spans `chat` por trace somados sem dedup ⇒ tokens a 2×. **Pré-requisito da primeira env em $.** Dependente da decisão D2.

### Objectivo
Campo de custo no contrato, preenchido no adaptador, agregação deduplicada por parentesco, `Cost` em `ProductionConfig` + `WithCost` no wiring.

### Critérios de Aceitação
- [x] `CostMicroUSD` flui do GW para o RT/`TurnRecord`/span. — `port.Usage.CostMicroUSD` é o campo novo do contrato; `Gateway.Chat` escreve-o na resposta normalizada a partir da `cost.Reading` do metering (`recordCost`); `translateResponse` projecta-o em `agentruntime.ModelResponse.CostMicroUSD`, de onde o RT já o levava ao span `chat`, ao `Result.TotalCostMicroUSD` e ao `TurnRecord`. **Um canal só** — não se abriu contabilidade nova no runtime, ligou-se a que existia às duas pontas que já existiam.
- [x] `AggregateByTrace` deduplica por parentesco — teste prova tokens 1× com custo real. — A dedup é em `otel-genai/cost_aggregation.go` e vale nas **três** leituras do mesmo trace (`AggregateByTrace`, `RollupByTrace`, `VelocityByTrace`), via `countableChatSamples`. Regra: um `chat` não conta se, subindo os ancestrais, se encontrar outro `chat` **antes** de uma fronteira de turno (`invoke_agent`/`execute_tool`) — o que suprime a re-observação RT↔GW e **preserva a delegação** (`chat → execute_tool → invoke_agent → chat` continua a contar 2). Conta-se o span **de fora** (o do RT), por ser o que carrega `run_id`/`step_id` e o único presente quando o GW não é traçado: a leitura fica idêntica nas duas topologias.
- [x] Burn-down lê custo real (fonte de AOS-262). — O burn-down do nó lê `cost_micro_usd` dos eventos `turn.recorded` (AOS-261), e esse campo passa a trazer o custo derivado. Provado sobre o evento **durável**, não sobre o campo em memória.

**Fonte do número (declarada):** a tabela de preços versionada e *tamper-evident* que já existe (`model-gateway/pricing`, digest sha256, quatro rates por `(modelo, região)` em micro-USD inteiro por 1M tokens) × os quatro contadores de token que o provider ecoa. **Nenhum preço foi inventado.** A API compatível OpenAI não devolve custo — devolve tokens; o custo é sempre derivado. Toda a travessia é **micro-USD int64**, sem um único `float` no caminho de dinheiro (**ADR-008**): os dois lados da fronteira RT↔GW são inteiros e a projecção é uma cópia, sem conversão onde se pudesse perder um micro-USD.

**Residual declarado (com eixo): cobertura de preço num nó real.** A tabela **embebida** só cobre os pares de referência do repositório (`claude-sonnet`/`claude-haiku`/`gpt-4o` × `eu-west`/`us-east`) e o *default* `AOS_MODEL_REGION=eu` **nem sequer casa** com ela. Um nó real precisa de montar a sua tabela em **`AOS_MODEL_PRICING_PATH`** (mesmo formato, mesmo digest) — inventar rates para o modelo do operador seria pior do que não haver custo, porque o burn-down em dólares passaria a mentir com autoridade. Sem par coberto **o canal transporta zero declarado** no banner de postura (`custo do modelo / canal de custo (AOS-259)`), nunca um número fabricado. **Curar uma tabela de preços de mercado é decisão do dono / trabalho de operação, não de código.**

**Segundo residual, aberto na remediação adversarial da wave (DEF-279): a verificação de arranque cobre o par PEDIDO, o cálculo por chamada usa o par RESOLVIDO.** `resolveModelPricing` consulta `table.RateFor(AOS_MODEL_NAME, AOS_MODEL_REGION)`, mas `Gateway.recordCost` calcula com `ex.ResolvedModel`/`ex.ResolvedRegion` — a região do endpoint que o *failover* escolheu. Hoje coincidem sempre porque `newGatewayModelClient` compõe **um único** `InfraAccount` na região pedida; a garantia é, portanto, do **inventário** e não da verificação. Acrescentar uma segunda conta/região torna alcançável um par sem preço e o *fail-closed* por chamada vira **brownout** — o mecanismo de resiliência a causar a interrupção. A afirmação do banner foi **qualificada** em vez de mantida absoluta, e o limite está declarado no ponto de composição do inventário.

**Cobertura de preço verificada no arranque, não por chamada.** O cálculo de custo é *fail-closed* por chamada (`pricing.ErrNoPrice` recusa a chamada em vez de facturar zero) — postura correcta *dentro* do caminho metrado, mas compor a contabilidade num nó cujo par não tem preço transformaria esse *fail-closed* num nó que **erra todas as chamadas ao modelo**, um *brownout* total por falta de um dado de facturação. Por isso a cobertura é consultada **uma vez**, com a tabela em mão (`parseModelPricingFromEnv`): coberto ⇒ recorder ligado e o *fail-closed* por chamada vale de facto; não coberto ⇒ sem recorder, zero declarado. `AOS_MODEL_PRICING_PATH` definida mas ilegível/inválida ⇒ **aborta** (`ErrBadModelPricing`).

**Alcance do canal no caminho de *streaming* (declarado):** o custo do `ChatStream` fica no span e nos agregados por run/árvore mas **não** no `Usage` do *chunk* final — esse já foi entregue ao consumidor quando o metering corre (é a razão de o metering ser adiado: antes do fim do stream não há usage). Sem efeito no runtime: o adaptador RT→GW usa o caminho **síncrono**.

**Sem segundo canal de burn-down:** o `cost.Recorder` do nó é composto **sem sinks**. Ligar-lhe um `BurndownSink` criaria uma segunda contabilidade em memória, com outra chave e outra retenção — exactamente o que AOS-261 rejeitou. Quem decide o burn-down é o ledger de turnos, alimentado por este mesmo número.

### Estado
**FECHADO.** Ficheiros: `model-gateway/port/port.go`, `model-gateway/gateway.go`, `model-gateway/runtime_adapter.go`, `model-gateway/production.go`, `substrate/otel-genai/cost_aggregation.go`, `cmd/aos/model_pricing_env.go` (novo), `cmd/aos/modelgatewaywiring.go`, `cmd/aos/main.go`, `cmd/aos/posture_banner.go`, `cmd/aos/burndown_ledger.go` (doc), `deploy/node/README.md`. Testes: `model-gateway/aos259_cost_channel_test.go` (composição **real** RT+GW+Event Store+tracer partilhado — prova, na mesma passagem, custo no runtime, custo no evento durável, **2 spans `chat` mesmo** e agregação com **tokens 1× e custo real**), `otel-genai/cost_aggregation_nested_test.go` (dedup, não-supressão da delegação, conjunto parcial, coerência rollup/velocity), `cmd/aos/aos259_model_pricing_test.go` (armado / zero declarado / *fail-closed* / banner casa com o composto). Uma env nova: `AOS_MODEL_PRICING_PATH`.

---

## AOS-260 — Admissão do turno de modelo

### Contexto
Desafio A1 (risco 1, CONFIRMADO/alta): a chamada ao modelo é directa (`loop.go:549`), fora da cadeia — a linha de custo dominante sem admission control. Dependente de D1 (opção B) e de AOS-259.

### Objectivo
Reservar antes do turno de modelo e saldar com o usage/custo real da resposta.

### Critérios de Aceitação
- [x] Porta nova em `agent-runtime`: reserva antes de `loop.go:549`, settle com `resp.Usage`/`CostMicroUSD`.
- [x] Esgotamento ⇒ degradação declarada (não deny-loop cego — liga a AOS-262).
- [x] Replay não re-reserva (dedup por `run_id:step_id`).

### A porta, e porque tem esta forma

`agentruntime.ModelAdmission` (`kernel/agent-runtime/model_admission.go`) é **reserva + saldo**, e as
duas metades são obrigatórias. É o **mesmo** admission control de **ADR-008** — o orçamento hierárquico
com reserva atómica em tokens/$ — aplicado ao ponto que lhe escapava; nenhum tecto novo, nenhuma
segunda contabilidade: é o nó por-run de AOS-256 que passa a ser debitado também pela inferência. Contar depois é *burn-down* (AOS-261/262), que já existe e
declaradamente **não decide**; o que faltava era ADMITIR — decidir antes de gastar.

- `AdmitTurn` corre **imediatamente antes** de `rt.model.Call`, com o *prompt* já materializado (a
  única base honesta para estimar o input) e devolve um **veredicto**, não um erro: **negar não é
  avariar**. Um erro da porta é fatal (cegueira do tecto), como no `LivenessBreaker` e no
  `ProgressObserver`;
- `SettleTurn` corre logo a seguir com `Usage` + `CostMicroUSD` **medidos**. Sem ele, um tecto
  composto por provisões esgotava-se com consumo fantasma e negava *runs* saudáveis — com uma
  provisão de 1024 e turnos de 200 tokens, ~5× mais cedo do que o gasto real justifica.

O loop **nunca retenta**: povoa `Result.BudgetExhausted` + `BudgetExhaustionReason` e retorna. Um
*deny-loop* queimaria *wall-clock* sem progresso e o *run* morreria pelo disjuntor **com a causa
errada no log** — que é o ponto (a) da decisão do dono.

### A política de estimativa (declarada)

**Input:** o *prompt* materializado, contado pela **mesma** aproximação por átomos de AOS-258
(`integration.ModelPromptTokens` reutiliza `approxTokens`; nenhum segundo estimador é inventado).
**Output:** desconhecido antes da chamada ⇒ uma **provisão fixa** (`DefaultOutputProvisionTokens` =
1024), limitada a **1/8 do tecto por-run** (`OutputProvisionFor`). O limite não é zelo: uma provisão
fixa **maior que o tecto** tornaria o tecto inatingível — o turno 1 seria negado sem o *run* ter
gasto nada, e o operador leria «orçamento esgotado» num *run* que nunca correu. Quem nega tem de ser
o **prompt**, que é uma verdade sobre o *run*.

Provisão zero foi rejeitada (*fail-open* na dimensão que mais cresce: o output do último turno
escaparia ao tecto); provisão igual ao máximo do modelo também (negaria *runs* com *headroom* de
sobra). Sobre-provisionar custa **admissão momentânea**, nunca orçamento: o saldo devolve a folga no
mesmo turno.

**Dólares:** a projecção sai da **tarifa MEDIDA do próprio *run*** (custo/tokens já saldados), nunca
de uma tarifa inventada — a tabela de preços é do Model Gateway (AOS-259) e importá-la para o
*runtime* duplicaria a fonte de verdade do preço. Limitação declarada: no **primeiro turno de cada
incarnação** ainda não há medição e esse turno decide só por *tokens* (DEF-278 (a)).

### O saldo, com as primitivas que já existem

`budget.Commit` confirma pelo montante **reservado** — por desenho, não recebe quantia. Saldar pelo
real sem inventar uma primitiva nova faz-se **libertando a provisão e reservando o real**, nessa
ordem: o nó do *run* é debitado sequencialmente (as tool calls do turno só são despachadas **depois**
deste saldo) e a raiz da árvore é ilimitada, pelo que libertar E e reservar R ≤ E não pode falhar por
contenção. A ordem inversa exigiria *headroom* para E+R ao mesmo tempo, e um *run* perto do tecto
seria negado **no seu próprio saldo**, depois do dinheiro gasto.

Casos limite, todos selados: **R > E** (subestimámos) ⇒ cobra-se até ao topo (*run* a 100%) e arma-se
o *latch* de excedente, que faz a admissão seguinte negar com razão própria — o número exacto vive no
ledger durável; **usage a zero** (provider que não ecoa) ⇒ fica **cobrada** a provisão, nunca zero (um
orçamento que não desconta nada é pior do que nenhum) — mas **não é registada como medição**: a
estimativa não entra no medidor do *run*, senão diluía a tarifa de `projectCost` e a admissão
seguinte projectaria o custo abaixo do real, atravessando o tecto em `$` com turnos que devia ter
negado (*fail-open* na dimensão que este ticket veio fechar); **falha do modelo** ⇒ a provisão é
**libertada** inteira (sem isto, um provider intermitente esgotava o tecto com consumo inexistente).

### A degradação declarada (ponto (a) da decisão do dono)

Nenhum caminho novo. O adaptador do nó (`cmd/aos/model_admission_wiring.go`) reutiliza os dois que
existem: com o **prompt de exaustão ARMADO** (AOS-263) a negação levanta o **mesmo** pendente durável
e suspende o *run* em `waiting_on_human` — decidido em `POST /runs/{id}/exhaustion` (`continue`/
`abort`, assinado), *run* **retomável**; **desarmado**, o *run* pára com razão própria e o selo
terminal é **`timed_out`** com o rótulo **`budget_exhausted`** — ao lado de `max_turns_exhausted` e do
*wall-clock*. **Nunca `failed`**: um tecto defensivo atingido não é falha recuperável, e `failed`
accionaria a saga de *rollback* (AOS-254) sobre efeitos legítimos.

### Replay não re-reserva (ponto (b))

Duas camadas, e a que importa é a segunda: a **dedup por `run_id:step_id`** cobre a re-entrada no
mesmo processo (molde do `already-applied` do *step-ledger*); o **`ReplayDetector`** cobre o caso que
a decisão do dono nomeia — a **retoma**, onde o processo é outro e o mapa nasce vazio. O detector do
nó lê o **mesmo plano de replay** (no `ctx` da retoma) que faz o `resumeAwareModelClient` devolver a
captura, pelo que a simetria «**sem chamada, sem cobrança**» é estrutural e não duas regras a manter
alinhadas. Selada por `TestAOS260_DetectorLeOMesmoPlanoDoModelClient`, que verifica a bicondicional
contra o cliente real.

### O caminho quente (ponto (c))

Sem I/O novo: a admissão é O(prompt) + CAS em memória, e o estado por-run é podado pelo **seam que já
existe** (`RunBudget.onRunRelease`, o mesmo `defer` de AOS-256) em vez de um `Forget` que alguém tenha
de se lembrar de chamar. Determinismo do replay intacto: um turno reproduzido dá sempre o mesmo
veredicto (admitido, sem reserva).

### A raiz da árvore — a bomba que este ticket desarmou

A raiz era `Amount{Tokens: MaxInt64}`, com a dimensão **$ a zero**. Era correcto enquanto **nada**
reservava custo; a partir do momento em que o turno de modelo passou a debitar dólares, uma raiz a
zero negaria a árvore **inteira**, em todos os *runs*, com uma razão que parece falta de orçamento.
Selado por `TestAOS260_RaizIlimitadaNasDuasDimensoes`.

### O banner (a postura anunciada mudou com a postura ligada)

`BudgetScopeDeclaration` foi **reescrita** — ver AOS-255, cuja frase este ticket tornou falsa:

> **orçamento: cobre tool calls E o turno de modelo em TOKENS — reserva antes da inferência, saldo pelo consumo medido; o tecto em dólares é opcional e só decide quando configurado**

Os gates de texto acompanharam-na: saíram `TOOL-ONLY`/`TOKEN-ONLY` da lista de marcadores exigidos e
saíram «cobre a inferencia»/«cobre o turno de modelo» da lista de *over-claim* (passaram a ser
**verdade**); entraram `TURNO DE MODELO`, `DEGRADACAO DECLARADA`, `REPLAY NAO RE-RESERVA` e
`AOS_BUDGET_MAX_COST_MICRO_USD`, mais um gate **negativo** que impede a frase velha de sobreviver na
mesma linha que a nova.

### Remediação adversarial da wave (correcções sobre a entrega inicial)

Duas auditorias adversariais correram sobre AOS-259+AOS-260 e o que se segue é o que ficou corrigido.
Nenhuma destas correcções alarga o alcance do ticket — todas fecham a distância entre o que a
entrega **decidia** e o que ela **mostrava** ou **garantia**:

1. **O tecto em `$` deixou de ser invisível à decisão humana.** `runBudgetReader.Limit` devolvia
   `CostMicroUSD: 0` sempre, pelo que `consumedFraction` calculava a fracção **só sobre tokens**: um
   *run* a 100% do tecto em dólares e a 15% do de tokens **nunca era avisado** e era negado de
   repente. O `Limit` passa a trazer a dimensão `$` quando (e só quando)
   `MaxCostMicroUSDPerRun` reporta um tecto configurado; `evaluationFor` preenche as duas dimensões a
   partir do orçamento real; e o `PendingRecord` ganhou `consumed_cost_micro_usd`/`limit_cost_micro_usd`
   **e um campo `reason`** — a razão atribuível da admissão, que antes vivia só no log do processo, que
   não é o canal que a decisão assinada lê. Sem isso o operador respondia `continue` sobre a grandeza
   errada e o *run* era re-negado no turno seguinte pelo mesmo tecto.
2. **O tecto em `$` deixou de ser aceitável sem fonte de preço** (`ErrBudgetCostNoPriceSource`,
   molde de `ErrProgressBudgetUnwired`). Com o par (modelo, região) sem preço na tabela em vigor — o
   caso do *default* `AOS_MODEL_REGION=eu`, que a embebida não cobre — o custo medido seria **zero** em
   todas as chamadas e a dimensão `$` **nunca negaria**; com o **modelo de referência** seria pior, por
   dar autoridade de *enforcement* à constante fabricada de 1500 micro-USD (tarifa observada ~75
   micro-USD/token, ~25× a real). O arranque **aborta** em vez de servir a capacidade-fantasma.
3. **A poda de fim de *run* deixou de casar prefixos.** `pending` era chaveado por
   `run_id + ":" + step_id` e `forgetRun` procurava por prefixo: o fim do *run* `t1` libertava as
   provisões **vivas** de `t1:job` e o saldo desses turnos passava a *no-op* silencioso — o consumo
   real nunca era debitado. A chave passou a ser o par estruturado e a poda é por **igualdade** do
   `run_id`. O `run_id` vem do corpo do pedido sem validação de forma, pelo que era alcançável.
4. **A provisão deixou de poder ficar órfã.** A entrada de `pending` continua a sair à entrada do
   saldo (é o que torna um saldo repetido um *no-op* honesto) mas é **reposta** em cada caminho de
   erro, para que `forgetRun` continue a ser a rede de reclamação que promete ser. Inalcançável hoje
   (sem *emitter*, `Release`/`Commit` não falham) e **armado** para o orçamento durável — ver
   DEF-278 (d).
5. **`projectCost` deixou de poder transbordar.** O arredondamento passou a ser por quociente+resto
   nos **dois** ramos: `(p + d - 1) / d` transbordava para negativo perto de `MaxInt64` (e o ramo do
   produto inseguro devolvia **zero** com os três argumentos ao máximo — a projecção mais *fail-open*
   possível, no caso mais caro). Uma quantia inválida passou também a ser **negação atribuível** e não
   erro fatal da porta: `budget.ErrInvalidAmount` é um defeito de cálculo deste adaptador, não perda de
   visibilidade do tecto, e subi-la abortava o *run* como `failed` — accionando a saga de compensação
   sobre efeitos legítimos.
6. **Comentários e banners realinhados.** `progress_wiring.go` declarava «não há tecto em dólares no
   nó / alcance tool-only», o banner do *burn-down* dizia que a dimensão `$` continuava «a não ter
   tecto próprio», e o README recomendava apertar o *wall-clock* como **único** travão da inferência.
   Eram todos falsos depois desta wave — e o comentário que defende o defeito é o modo de falha que
   AOS-255 nomeia, aplicado ao código em vez do banner.

### Uma env nova

`AOS_BUDGET_MAX_COST_MICRO_USD` (opcional): tecto **em dólares** por *run*, em micro-USD inteiro. Sem
ela a dimensão `$` é **medida e debitada** mas nunca nega; com ela **decide a par dos tokens** (uma
reserva só cabe se couber nas duas). *Fail-closed* na configuração: ilegível/`0`/negativa aborta, e
**sem `AOS_BUDGET_MAX_TOKENS` também aborta** — não há orçamento onde pendurar o tecto em `$`, e
escrevê-lo faria o operador ler uma protecção inexistente.

### Estado
**IMPLEMENTADO.** Ficheiros: `kernel/agent-runtime/model_admission.go` (novo — a porta),
`kernel/agent-runtime/loop.go` (o gancho + `Result.BudgetExhausted`/`BudgetExhaustionReason`),
`integration/model_admission.go` (novo — o adaptador sobre o orçamento por-run),
`integration/budget.go` (raiz ilimitada nas duas dimensões, tecto em `$` opcional, `onRunRelease`),
`integration/secured.go` e `integration/budget_estimator.go` (alcance reescrito),
`cmd/aos/model_admission_wiring.go` (novo — degradação declarada + detector de replay),
`cmd/aos/bootstrap.go`, `cmd/aos/budget_env.go`, `cmd/aos/terminal_states.go`, `cmd/aos/service.go`,
`cmd/aos/posture_banner.go`, `deploy/node/README.md`, `docs/reports/prontidao-modelos-agenticos.md`.
Testes: `kernel/agent-runtime/model_admission_test.go` (ordem admit→call→settle, negação sem
*deny-loop*, replay sem saldo, falha do modelo, erro fatal vs negação, retro-compat),
`integration/aos260_model_admission_test.go` (árvore **real**: reserva/saldo contra gémeo derivado à
mão, contrafactual do *commit*-da-estimativa, esgotamento, replay nas duas camadas, excedente, eixo
`$` com tarifa medida, poda no *seam* de AOS-256, guarda AST anti-*float*, e a prova **composta** via
`SecuredRuntime.Run`), `cmd/aos/aos260_model_admission_test.go` (os dois desfechos da degradação, a
bicondicional detector⇄model client, selo terminal, env *fail-closed*, banner). Residuais: **DEF-278**.

---

## AOS-261 — Retentor de spans por-run + resolvedores de identificador

### Contexto
Desafio A2 (achados C/F): `Evaluate` recebe spans como parâmetro e nada no nó os produz/retém (NoopTracer por omissão; `SpanTracer` dispara-e-esquece) — superfície verde a mentir. E o nó não resolve `runID→traceID` nem `runID→treeID`; a retoma cria um trace novo por incarnação. Alternativa preferível: `BurndownSource` sobre `(runID, turn)` a partir do ledger de turnos (imune à re-emissão).

### Objectivo
Decorador de `Exporter` que retém `SpanData` com política de retenção (ou a fonte por ledger) + resolvedores explícitos com política multi-incarnação.

### Critérios de Aceitação
- [x] A fonte de burn-down devolve dados reais com OTLP ligado e **erro explícito** (não zero silencioso) sem tracer.
- [x] Política documentada para runs multi-incarnação (prefixo T1 vs reprodução T2).
- [x] Testes: retenção, query por trace/run, e o caso de retoma.

### Estado
**IMPLEMENTADO** — pela alternativa **(b)**, a `BurndownSource` sobre o **ledger de turnos**, e não pelo
decorador de `Exporter` que retém `SpanData`. A escolha está argumentada no cabeçalho de
`packages/control-plane/governance/progress-surface/burndown_source.go` e assenta em quatro pontos:

1. **imunidade à re-emissão** — o ledger é deduplicado na origem pela `idempotency_key` `run_id:step_id`
   do Event Store; um turno gravado duas vezes ocupa UMA entrada. Um retentor de spans contaria 2×,
   que é o modo de falha que o desafio A2 nomeia (`TestAOS261_FonteLeOLedgerRealDoTurnRecorder` grava o
   mesmo `(run_id, step_id)` de novo e o total não muda);
2. **não é preciso inventar retenção** — o retentor exigiria política nova (tecto de memória, despejo,
   TTL) com o seu próprio despejo silencioso; o ledger herda a retenção durável que o nó já tem
   (`AOS_RETENTION_*`) e sobrevive ao restart (`TestAOS261_RetencaoDoStoreEAFonteDaFonte` reabre o WAL);
3. **o resolvedor `runID→traceID` DEIXA DE SER NECESSÁRIO** — era ele o problema multi-incarnação (cada
   retoma abre um trace novo ⇒ o burn-down ressuscitava a zero). `runID→treeID` também sai do caminho
   crítico: o nó de orçamento por-run é registado com o próprio RunID, pelo que a resolução é a
   IDENTIDADE, **declarada** em `runBudgetReader` e não adivinhada;
4. **é o mesmo número que o run pagou** — `resp.Usage`/`CostMicroUSD` gravados na transacção que torna o
   turno durável; não há uma segunda contabilidade a divergir.

**Critério duro (erro explícito, nunca zero silencioso)**, em três camadas: sem fonte ⇒
`ErrNilBurndownSource`; sem ledger / ledger sem turnos / payload ilegível / erro do store ⇒
`ErrBurndownNoLedger` ou o erro do store tal-qual; e a via ANTIGA (por spans) passou a devolver
`ErrNoBurndownSpans` para uma fatia **nil** — que era o caso NORMAL (nenhum nó produz/retém spans) e
fazia `Evaluate` devolver «0% consumido» em todas as leituras.

**Política multi-incarnação** documentada em `BurndownSource` e no `doc.go`: consumo **cumulativo** sobre
o prefixo T1 (o gasto não volta por o processo ter reiniciado) e reprodução T2 **sem duplicação** (mesma
`idempotency_key` ⇒ `StatusDuplicate`, sem escrita). Um run delegado é outro `run_id` e outro stream — a
agregação por parentesco continua a ser AOS-259.

**Alcance declarado, não alargado:** a fonte conta os **turnos de modelo** e só eles (o ledger não pesa
tool calls), pelo que o burn-down é um **limite inferior** do consumo — dispara TARDE, nunca cedo por
engano. Está escrito em `RunConsumption`, no banner do nó e no README.

**Ficheiros:** `progress-surface/burndown_source.go` (porta + `RunConsumption` + `BurndownFromConsumption`),
`errors.go`, `surface.go` (`WithBurndownSource`, `EvaluateRun`, latch, `ForgetRun`, `ValidThreshold`),
`packages/cmd/aos/burndown_ledger.go` (o adaptador node-local, com cursor por-run para não reler o stream
inteiro a cada turno). **Zero dependências novas** (ADR-017): só `replace` internos já existentes.

---

## AOS-262 — Burn-down + aviso de exaustão no nó (primeira entrega: sem decisão)

### Contexto
Desafio A2 (plano revisto, passos 6-7): primeira entrega é **só burn-down + aviso** — sem `extend`/`summarize_stop`/`abort` (que não têm executor nem autoridade). O gancho de fim-de-turno já existe (`loop.go:469-518`, padrão `WithSteerSource`/`WithLivenessBreaker`).

### Objectivo
`Evaluate` na fronteira de fim-de-turno com as duas portas de leitura (`BudgetReader`, `ProgressReflector`), aviso emitido a ~80%, env fail-closed.

### Critérios de Aceitação
- [x] Adaptadores node-local das portas de leitura (scheduler-free — sem violar `boundary_orq_sch_test.go`); `ProgressSnapshot.Step` ou tem produtor ou o campo é declarado vazio.
- [x] `AOS_PROGRESS_THRESHOLD` recusa arrancar com valor inválido (padrão `ErrBadBreakerThresholds`), não o fallback silencioso de `WithThreshold`.
- [x] Aviso emitido uma vez por run (latch); **visível no canal de leitura existente** — o **log do nó** (o mesmo writer do banner de arranque, que existe sempre) e, **quando `AOS_OTLP_ENDPOINT` está definida**, também o span `aos.control.budget_warning`. Só o span **não** cumpriria o critério: sem OTLP o tracer do nó é o `NoopTracer` e o aviso não teria superfície nenhuma na configuração por omissão.
- [x] As opções de decisão NÃO são apresentadas nesta entrega.

### Estado
**IMPLEMENTADO.** O gancho é o que já existia: uma porta nova no kernel
(`agentruntime.ProgressObserver` + `WithProgressObserver`, molde de `WithSteerSource`/`WithLivenessBreaker`)
consultada na **mesma fronteira de fim-de-turno**, **depois** da pausa graciosa e do disjuntor (um run que
já parou não é avisado sobre um orçamento que deixou de queimar) e **depois** de `recordTurn`, pelo que o
turno corrente já está no ledger que a fonte lê. A assinatura devolve **só `error`** — não há
`(tripped bool, ...)`: um contrato que não pode parar o run não engana o leitor sobre quem manda.

**Adaptadores node-local** (`packages/cmd/aos/progress_wiring.go`), nenhum a importar
`control-plane/orchestrator` nem `control-plane/scheduler` — o guarda de fronteira (imports directos **e**
grafo de build transitivo, ADR-018) corre verde:

- `runBudgetReader` sobre o `integration.RunBudget` — o **tecto** (`Limit`) é o denominador. `Available`
  existe porque a porta o exige e devolve **erro** para um run sem nó vivo, mas **não** entra no
  burn-down: mede reservas de TOOL CALL (alcance tool-only de AOS-255) enquanto o numerador vem dos
  TURNOS DE MODELO — somá-los seria contabilidade nova. Selado em
  `TestAOS261_EvaluateRun_FraccaoDerivaDaFonteEDoTecto` (zero chamadas a `Available`);
- `nodeProgressReflector` sobre a `state.Machine` **que o steer/escalada/disjuntor já usam** (leitura em
  memória `Current()`, não `Rebuild` — isto corre a cada turno). **`ProgressSnapshot.Step` TEM produtor:**
  é `chat#<turno>`, o mesmo índice que identifica o turno no ledger.

**`AOS_PROGRESS_THRESHOLD` fail-closed** (`progress_env.go`, `ErrBadProgressThreshold`): valor ilegível ou
fora de `(0,1)` **aborta o arranque**. É deliberadamente mais estrito do que `WithThreshold` (que cai no
default) porque uma env má é erro do OPERADOR, que ninguém apanha: ele escreve `0.9`, recebe `0.80` e fica
convencido de que configurou o aviso. `0` avisaria em todos os turnos e `1` nunca avisaria. A validação usa
`progresssurface.ValidThreshold` — a MESMA função da superfície, para não haver duas noções de "válido".

**Segundo gate, molde de AOS-246** (`ErrProgressBudgetUnwired`): `AOS_PROGRESS_THRESHOLD` **sem**
`AOS_BUDGET_MAX_TOKENS` aborta o arranque — sem tecto não há denominador, a fracção seria 0 para sempre e o
aviso **nunca** dispararia, com o banner a prometê-lo.

**Aviso, não prompt:** `EvaluateRun` devolve `BudgetWarning` (sem campo de opções) e emite
`aos.control.budget_warning` — um op **distinto** de `aos.control.exhaustion_prompt`, para que nenhum
consumidor do canal de leitura infira que houve uma escolha a apresentar. **Uma vez por run** (latch por
`runID` na superfície, podado por `ForgetRun` no mesmo ponto de `runBreakers.forget`). `extend`,
`summarize_stop` e `abort` **não** são apresentados por ESTE aviso: à data de AOS-262 nenhum tinha executor
nem autoridade (o `abort` passou a ter em AOS-263 parte 3, e é o **prompt** que o apresenta — não o aviso), e
`BudgetExtender`/`Degrader` ficam **nil** — a superfície recusa-se se alguém as usar, em vez de haver um
adaptador que finge decidir.

**Banner** (`burndownPostureBanner`, argumento derivado do observador REALMENTE composto): declara a fonte,
que a leitura é um **limite inferior**, que a dimensão que decide é **tokens**, que o aviso **não decide
nada** e **quem** pára um run.

**Testes:** `packages/kernel/agent-runtime/progress_observer_test.go` (o seam: consulta por turno
não-terminal, erro fatal, não-decide, retro-compatibilidade), `packages/cmd/aos/aos262_progress_warning_test.go`
(env válida/inválida/ausente; os dois gates de arranque pelo `Bootstrap` REAL; o aviso ao limiar pelas peças
node-local reais com latch e `forget`; a leitura não muta o orçamento; a amarra banner⇄composição) e
`packages/control-plane/governance/progress-surface/aos261_burndown_source_test.go` (latch por-run, ausência
de spans de prompt/decisão). **Nenhuma dependência nova**; `AOS_PROGRESS_THRESHOLD` documentada no índice de
`deploy/node/README.md` (gate AOS-203).

---

## AOS-263 — Prompt de exaustão durável (2.º tipo de `PendingRecord`)

### Contexto
Desafio A2 (decisões D4/D5/D6): o desenho-alvo reutiliza a maquinaria HITL — pendente durável de segundo tipo (sem preview; amarrado a run+limiar+montante), suspensão `waiting_on_human`, rota autenticada, registo WORM com principal. `extend` exige autoridade (piso: paridade com `pause`) e um dono do tecto (o `budget.Budget` não tem mutador — ticket próprio). Bloqueado pelas decisões do dono.

### Objectivo
Implementar o prompt de exaustão como cidadão do plano de controlo, não como segundo mecanismo mais fraco.

### Critérios de Aceitação
- [x] `PendingRecord` generalizado; o prompt aparece em `GET /runs/{id}`; TTL varrido pelo sweeper.
- [~] `extend` exige assinatura de operador registado (piso D5) e escreve registo WORM próprio (principal, run, montante, razão). — **SAI por decisão do dono (iii)**: sem mutador de tecto não há executor; o prompt **não** o apresenta. **EIXO REGISTADO: `DEF-220`** em `docs/governance/REGISTO-Deferimentos.md` (âncora `packages/cmd/aos/exhaustion_decision.go`, nota §6 `N-DEF-220` com o ticket de `budget` descrito: evento próprio + `Rebuild` a consumi-lo + ADR que reabra a decisão de desenho + a autoridade que AOS-263 já entregou).
- [x] `abort` adaptado sobre a pausa graciosa durável; `summarize_stop` advisory declarado como tal (ou modo de terminação real). — **`abort` entregue** (rota autenticada `POST /runs/{id}/exhaustion`); **`summarize_stop` FICA FORA das opções apresentadas**, declarado com a razão (sem caminho de resumo no loop) no banner, no README do nó e em `exhaustion_decision.go`. **Desvio declarado:** o `abort` não é «sobre a pausa graciosa» — é sobre a aresta durável `waiting_on_human → killed` de AOS-017; a pausa graciosa é a via que a rota **nomeia** (409) quando o run voltou a correr, porque matar um run a meio de um turno seria o kill novo que este ticket não quer.
- [x] Suspensão repõe `enteredAt` — a deliberação não morre pelo wall-clock.
- [x] **(remediação)** A metade «continuar» da decisão tem a MESMA autoridade da metade «parar»: `continue` é uma decisão assinada da mesma rota, com selo WORM próprio, e a **retoma recusa (409) enquanto a pergunta estiver por responder** — sem isto, a resposta arriscada era a única sem assinatura de operador e sem registo de quem a tomou.

### Estado
**IMPLEMENTADO e MERGED** (PR #9, 2026-08-13; `exhaustion_prompt.go` + `exhaustion_decision.go` + testes; travão anti-contorno no `resume` com `ErrExhaustionPromptUnanswered`). O texto abaixo descrevia apenas o **desbloqueio** e ficou por reconciliar quando a entrega aconteceu — corrigido na validação de 2026-08-13.

**Decisões do dono registadas (2026-08-12).** As três decisões que o
desafio A2 numera (i)/(ii)/(iii) — o ticket citava-as como «D4/D5/D6», rótulos que não
existem no relatório:

- **(iii) dono do tecto ⇒ opção (c): o `extend` SAI desta entrega.** Facto verificado: o
  `budget.Budget` não tem mutador de tecto, e `events.go:107-109` declara que os limites
  são «configuração declarativa FORA do log de eventos — por design não reconstruíveis por
  `Rebuild`». Levantar o tecto exigiria quebrar essa decisão de desenho, com evento próprio
  e ADR. Fica como ticket de `budget`. O prompt apresenta só as opções que TÊM executor.
- **(i) mecanismo ⇒ opção (B): reutilizar a maquinaria HITL.** O prompt é um SEGUNDO TIPO
  de `PendingRecord` (sem preview, amarrado a run+limiar+montante); o run suspende em
  `waiting_on_human` pelo `runGate` já existente; aparece em `GET /runs/{id}`; a decisão
  entra por rota de controlo autenticada. A opção A foi recusada: criaria um caminho de
  decisão humana MAIS FRACO do que o four-eyes já entregue — regressão de postura.
- **(ii) autoridade ⇒ paridade com `pause`.** Ed25519 de operador registado + nonce durável
  + frescura, reutilizando o `Ed25519Authenticator` e `AOS_OPERATORS` que JÁ estão compostos
  no nó. Nada de esquema novo.

**PARTE 2 IMPLEMENTADA — o prompt é emitido e fica visível** (`packages/cmd/aos/exhaustion_prompt.go`).
O aviso de burn-down de AOS-262 (`progress_wiring.go`) é a FONTE do sinal — não se recalcula
consumo: ao disparar, sela um `PendingRecord` de tipo `exhaustion` (sem preview; amarrado a
run+limiar+consumido+tecto) e SUSPENDE o run em `waiting_on_human` pelo `runGate` já
existente. O prompt sai em `GET /runs/{id}` no campo próprio `pending_exhaustion` (uma
leitura só, partilhada com `pending_approvals`) com as opções que TÊM executor — as DUAS
decisões da rota assinada da parte 3 (`continue` e `abort`); o TTL é varrido pelo sweeper de
pendentes já existente. O sinal viaja
como `error` porque a porta `ProgressObserver` só devolve isso, e o serviço absorve-o na
saída do run como SUSPENSÃO (mesma contabilidade de AOS-021: registo de retoma + balde de
suspensos), não como falha. **Sem `AOS_APPROVERS_FILE`, ou sem operador pinado em
`AOS_OPERATORS`, o prompt fica DESARMADO** (no primeiro caso não há a quem perguntar nem como
re-hospedar; no segundo ninguém poderia responder e a resposta não teria onde ser selada — e
suspender um run para uma pergunta sem via seria matá-lo com outro nome) e o comportamento é o
de AOS-262, declarado no banner
(`exhaustionPromptPostureBanner`). **Fail-closed:** o sinal é uma-vez-por-run, pelo que uma
suspensão que falhe (registo ou transição) ABORTA o run em vez de o deixar seguir sem nunca
perguntar. **`enteredAt`:** reposto pela própria transição durável — a deliberação não paga
wall-clock, e a retoma recomeça o tecto de AOS-252 (selado com a metade não-vacuosa em
`aos263_exhaustion_prompt_test.go`).

**PARTE 3 IMPLEMENTADA — a decisão tem rota, e o `abort` executa**
(`packages/cmd/aos/exhaustion_decision.go`). `POST /runs/{id}/exhaustion` é a via por onde o
humano RESPONDE à pergunta que a parte 2 selou.

- **Autoridade — paridade com o `pause` (decisão (ii)), sem esquema novo.** Mesma admissão do
  `/approve` e do `/pause` (`admitControl` + `admitControlMTLS`) e o MESMO
  `Ed25519Authenticator` composto de `AOS_OPERATORS`, com o **nonce-store durável** de uso
  único e a janela de **frescura**. Non-signing: a chave privada do operador nunca entra no nó.
- **A assinatura cobre a DECISÃO e a PERGUNTA**, não só o run: *kind* próprio
  (`exhaustion_decision`, que o `SteerChannel` deliberadamente não conhece) e payload canónico
  **length-prefixed** com `(decisão, step_id)`. Fecha a confusão de sinal que a alternativa
  óbvia — reutilizar `SteerChannel.Pause` — abriria: um `pause` capturado antes de ser gasto
  seria submetível como `abort`, trocando «parar de forma retomável» por «terminar o run».
- **Registo WORM PRÓPRIO** na partição `governance.exhaustion`, com o **principal VERIFICADO**,
  `run_id`, o passo, o **montante consumido** (os números do aviso de AOS-262, nunca
  recalculados), o tecto, o limiar e a **razão** (`budget_exhaustion_abort`, rótulo estável —
  sem texto livre, que a assinatura não cobriria). O selo é **pré-condição do efeito**.
- **`abort` sobre o que já existe, nunca um kill novo.** Run **suspenso** ⇒
  `waiting_on_human → killed`, a única aresta de paragem que a tabela de AOS-017 dá a esse
  estado (a mesma de ADR-013, com razão própria) — TERMINAL no vocabulário de AOS-252, e o
  `/resume` passa a 404. Run que **voltou a correr** ⇒ **409** nomeando a **pausa graciosa**
  `POST /runs/{id}/pause`, que o pára na fronteira de fim-de-turno e o deixa **retomável**: um
  abort nunca mata um run a meio de um turno.
- **A pergunta respondida sai da lista por DECISÃO**, não por expiração
  (`PendingApprovals.Decide`, facto `approval.decided` distinto de `approval.expired`) — senão
  o varrimento acabaria por anunciar «expirado sem decisão» sobre algo decidido.
- **Opções apresentadas = as que têm executor, e as duas na MESMA rota assinada:** `continue` e
  `abort`. Não se apresentam `extend` (decisão (iii), eixo `DEF-220`) nem `summarize_stop` (sem
  caminho de resumo), ambos DECLARADOS com a razão no banner, no README e no código — e nenhum
  deles viaja no wire. A **retoma** (`POST /runs/{id}/resume`) NÃO é uma opção: é a execução que
  se segue a um `continue` selado.
- **Via de acesso do operador:** `aos continue|abort --run-id … --step-id … --emitter … --key …`
  (a CLI assina e transporta; quem decide é o nó), para a rota não repetir o defeito «mecanismo
  sem superfície» que AOS-275 fechou no promotion controller.

**REMEDIAÇÃO (2026-08-12, pós-auditoria).** Quatro correcções de eixo, todas dentro de AOS-263:

- **simetria de autoridade** — `continue` passou a ser uma DECISÃO desta rota (assinada, com
  razão/capability próprias no WORM e retirada do pendente por `Decide`), e
  `POST /runs/{id}/resume` **recusa com 409** enquanto a pergunta estiver por responder. Antes,
  a metade arriscada da decisão entrava por uma retoma com credencial NHI: sem assinatura de
  operador, sem selo, e deixando o pendente na lista para o varrimento o declarar «expirado sem
  decisão». O TTL continua a ser a escotilha (expirada a pergunta, a retoma volta a ser aceite);
- **chave de ocorrência** — o `step_id` do prompt ganhou âncora de ocorrência (instante +
  desambiguador CSPRNG). Só-turno colidia entre incarnações (o contador de turnos reinicia em
  cada re-hospedagem, o ledger de burn-down não), e a colisão era SILENCIOSA: o Event Store
  deduplicava, a retirada anterior continuava a valer e o run ficava suspenso com uma pergunta
  invisível — 404 na rota, ausente do `GET`, já expirada para o varrimento. `PendingApprovals.Put`
  passou também a RECUSAR o duplicado de exaustão (fail-closed) em vez de o engolir;
- **registo compensatório** — um `abort` selado cujo efeito falha (corrida com uma retoma)
  escreve na MESMA cadeia uma linha `deny`/`budget_exhaustion_abort_failed` que referencia o
  `audit_seq` corrigido. A cadeia deixou de poder afirmar um abort que não aconteceu;
- **fail-open de leitura declarado** — uma falha ao ler os pendentes já não é indistinguível de
  «nada por decidir»: fica no log do nó e no campo `pending_unavailable` da resposta de estado.

Além disso, o prompt só ARMA onde a rota de decisão está composta (operador pinado + WORM), e a
chave de deduplicação **em memória** das listagens passou a ser injectiva (a durável mantém-se
byte-idêntica, por compatibilidade).

**Testes (PELA ROTA, nunca in-process):** `packages/cmd/aos/aos263_exhaustion_decision_test.go`
— decisão sem assinatura válida recusada nas 7 formas (adulterada, emissor não pinado, anónima,
stale, outro passo, outra decisão, `pause` submetido como abort) com a não-vacuidade dupla (o
`pause` continua gastável no `/pause` e a cerimónia legítima executa); **replay do nonce**
recusado, distinguido do estado do run (nonce fresco dá outro código); WORM com principal, run,
montante e razão; abort não mata run vivo e nomeia a pausa graciosa; mTLS de controlo herdado;
vocabulário fechado sem queimar nonce; 501 fail-closed sem maquinaria (incl. sem gates de
estado); e o balde de suspensos esvaziado (o cache não pode contradizer o log).
`packages/cmd/aos/aos263_decisao_simetrica_test.go` — o `continue` pela rota (selo próprio,
pendente fechado, retoma destrancada, com o 409 do ANTES como contraste), o travão da retoma e a
escotilha do TTL, as duas perguntas do mesmo run que não colidem (com o abort da segunda a
provar que o run não fica preso) e o registo compensatório do abort falhado.
`packages/cmd/aos/aos263_cli_decisao_test.go` — `aos continue`/`aos abort` contra o nó REAL
(servidor HTTP, handler real): a assinatura da CLI é ACEITE e tem efeito no estado durável, uma
seed errada é 403, e os campos que amarram a decisão são exigidos antes de qualquer rede.
`packages/integration/aos263_pending_kind_test.go` — a decisão retira das DUAS listagens, é
idempotente e é POR TIPO; a listagem não funde chaves ambíguas; o duplicado de exaustão é
recusado e a aprovação re-escalada continua idempotente. **Nenhuma dependência nova; nenhuma env
nova.**

---

## AOS-264 — Broker: passo zero de política/identidade + cliente Vault real + envs

### Contexto
Desafio A3: ligar o broker sem passo zero ⇒ troca negada por identidade e default-deny do Cedar ⇒ **nenhum turno de modelo executa** (risco novo, hoje inexistente). O cliente KEK existente é Transit (recusa devolver material) — transporte reaproveitável por cópia. D7 recomenda cliente/token **separados** (`AOS_BROKER_VAULT_*`); D8 recomenda v1 **in-process**.

### Objectivo
Preparar a troca mediada: capability nomeada (ou reutilização declarada de `cap:http.post` já assinada), `Principal` real no ponto de aquisição, cliente Vault real dentro de `internal/vault/`.

### Critérios de Aceitação
- [ ] A capability da troca está no bundle assinado (ou a reutilização está declarada e testada); o pipeline do GW passa `WithRun`/`WithPrincipal` (dados já existem no call site).
  - Nota (2026-09-15): a metade do **run** deste critério está fechada por **AOS-394** — o run e o passo do turno chegam ao gateway pelo ctx de cada chamada (`agentruntime.ContextWithModelCall`) e entram nos selos `modelgw-gov`, sem fixar o run na construção do adaptador; a metade do **principal** já vinha de AOS-278. O que falta neste critério é a capability da troca no bundle assinado, que é do broker.
- [ ] Cliente Vault real (KV v2 vs dynamic secrets decidido e registado — só dynamic dá corte downstream) com `Secret` construído dentro do pacote.
- [ ] `AOS_BROKER_VAULT_*` na tabela AOS-203; banner declara o modo.
- [ ] Higiene pré-wiring: reaper de leases (molde `approval_sweeper.go`) e superfície para `Revoke`.

### Estado
**IMPLEMENTADO e MERGED** (wave do broker; passo zero de política/identidade + cliente Vault real em `internal/vault` + `AOS_BROKER_VAULT_*`, com a postura declarada no banner — a linha «broker vault / credenciais downstream (AOS-070/AOS-264)» é emitida no arranque). D7/D8 decididos pelo dono em 2026-08-10 (cliente/token Vault **separados** do `AOS_DSAR_VAULT_*`; consumo v1 **in-process**, injecção remota deferida em D8-B). O `Estado` anterior dizia «pronto para wave» e ficou por reconciliar depois da entrega — corrigido na validação de 2026-08-13.

---

## AOS-265 — Broker: porta de aquisição com contexto + consumo in-process (v1)

### Contexto
Desafio A3 (estimativas corrigidas): `CredentialProvider.Fetch` devolve `string` e não transporta identidade — não é adaptação, é mudança de assinatura; o que se liga é um adaptador que **consome** o broker. A invariante rainha (segredo nunca observável **pelo agente**) mantém-se; a garantia do processo passa a ser redacção — declarado.

### Objectivo
Alargar a porta com contexto de chamada e ligar o broker ao ponto de aquisição do GW (in-process), com audit de governação no store durável.

### Critérios de Aceitação
- [x] `Fetch` (ou porta nova) transporta principal/run; `recordExchange` distingue runs. — porta NOVA `Broker.AcquireInProcess` (`packages/platform/broker/inprocess.go`) consome o broker: transporta `Principal`/`RunID`/`StepID` do `ExchangeRequest`; `recordExchange` sela por partição de run (stream = RunID). O `CredentialProvider.Fetch` do GW NÃO foi adaptado (mantém `string`, sem identidade) — a porta com contexto é a que consome o broker.
- [x] Troca negada (identidade/política) ⇒ falha ruidosa atribuída, nunca bearer vazio. — `AcquireInProcess` propaga o `*DeniedError` da mediação (efeito/código/razão) e devolve `ProcessCredential{}` (IsZero); nunca um bearer vazio de sucesso. `ErrNoMaterial`/`ErrLeaseRevoked`/`ErrLeaseExpired` idem.
- [x] O `Audit` do GW aponta ao store durável (hoje `audit.NewMemStore()`). — `AOS_MODEL_AUDIT_PATH` ⇒ `audit.OpenFileStore` (WORM tamper-evident) injectado em `newGatewayModelClient`; vazio ⇒ MemStore (inalterado), banner declara o modo. Ver `model_audit_env.go`.
- [x] Teste de composição pela cadeia real; injecção no executor remoto fica **declaradamente deferida** (desenho-alvo: handle opaco até ao orchestrator, D8-B). — `aos265_inprocess_test.go` corre a aquisição pela cadeia REAL (RM com ScopeGate + EventSink durável); a injecção no executor REMOTO está deferida no doc de `inprocess.go` (handle opaco → orchestrator via `Injector.Inject`, D8-B).

### Estado
**IMPLEMENTADO (in-process v1). Porta `AcquireInProcess` (consome o broker, resolve o handle no processo via sink server-side, redige o valor — invariante rainha preservada), audit de governação do GW durável por `AOS_MODEL_AUDIT_PATH`, composição provada pela cadeia real. DEFERIDO com eixo: (D8-B) injecção no executor REMOTO (handle opaco até ao orchestrator); binding LIVE do broker→`CredentialProvider` do model GW no nó de REFERÊNCIA — a ordem de construção (cliente de modelo antes do RM) e o default-deny do PDP não-carregado negariam a troca fail-closed (reintroduzindo o risco A3), pelo que o LIGAR ao vivo exige o bundle assinado do PDP + identidade infra com `cap:http.post` (eixo D4/AOS-156). Partilha do WORM único do nó pelo audit do GW idem deferida (re-ordenação da composição do modelo).**

---

## AOS-266 — Attestation: `ChallengeIssuance` + `DeviceEnrollment` + banner

### Contexto
**F10 (média).** O verificador liga-se por env, mas as portas de frescura (`challenge_issuance.go`) e de atribuição (`device_attestation.go`) têm zero ocorrências em `cmd/aos`: replay de attestation capturada é possível e qualquer autenticador allowlisted serve qualquer aprovador — aquém de ADR-016 §4. E URL definida sem approvers é ignorada em silêncio (banner mudo). Fonte: análise 08-08.

### Objectivo
Ligar as duas portas e declarar o estado no arranque.

### Critérios de Aceitação
- [ ] Challenges emitidos/registados por (pedido, aprovador) com TTL; verificação exige challenge fresco.
- [ ] `AOS_DEVICE_ENROLLMENT_FILE` (padrão `AOS_APPROVERS_FILE`) alimenta `NewStaticDeviceEnrollment`; perna de dispositivo não registado ⇒ recusada.
- [ ] Banner declara attestation LIGADA/DORMENTE e avisa URL-sem-approvers.
- [ ] Testes de nó: replay recusado; dispositivo errado recusado.

### Estado
**IMPLEMENTADO** (o `Estado` anterior ficou por reconciliar). Frescura por-cerimónia (`AOS_CHALLENGE_ISSUANCE`/`AOS_CHALLENGE_TTL`) + atribuição dispositivo↔aprovador (`AOS_DEVICE_ENROLLMENT_FILE`), com o banner a declarar LIGADA/DORMENTE. Provas: `packages/cmd/aos/aos266_challenge_freshness_test.go`, `aos266_device_enrollment_test.go`. Ver o Balanço (§0).

---

## AOS-267 — Scheduler interno de retenção

### Contexto
Grupo B (análise 08-08): a expiração TTL só corre sob `POST /dsar/expire` — mesmo com a política definida, nada expira sem cron externo autenticado. Risco RGPD silencioso (*storage limitation*).

### Objectivo
Ticker interno no loop de serviço (molde do sweeper de aprovações) com credencial de governação em nome próprio — ou decisão documentada de manter externo.

### Critérios de Aceitação
- [x] A política `AOS_RETENTION_*` definida ⇒ expiração corre periodicamente sem acção externa.
- [x] Credencial em nome próprio com selo WORM por varrimento (quem/o quê), respeitando legal hold.
- [x] Env de cadência com default declarado; fail-closed em valor inválido.

### Estado
**IMPLEMENTADO — pendente de auditoria.**

**(a) O ticker.** `retention_sweeper.go`, no molde EXACTO de `approval_sweeper.go`/`deadline_sweeper.go`
(ticker + o MESMO `sweepStop` fechado pelo `Shutdown`), ligado em `NewNodeService`. Corre o
**MESMO** `audit.ExpirationJob` que a rota corre — não há segundo varredor escrito à mão, porque um
segundo varredor seria um segundo sítio onde o legal hold podia ficar mais fraco.

**(b) Legal hold, fail-closed em dois níveis.** A barreira é a do job (`ExpirationJob.held`:
por-titular, pela partição do registo e, via o índice titular→partição, pelas **demais** partições
do titular) — intacta. Acresce que o scheduler **não arranca** se o `audit.LegalHold` não estiver
composto: com `holds` nil o job nunca retém nada, e uma expiração sob demanda ainda tem um humano
autenticado a assumi-la, mas um varredor automático nessas condições não tem ninguém no caminho. O
banner declara a postura e a **razão** de estar dormente. Guarda:
`TestAOS267_ScheduledSweepRespectsLegalHold` (o titular sob hold sobrevive ao varrimento agendado e
só expira após o *release*) e `TestAOS267_SchedulerRefusesToArmWithoutLegalHold`.

**(c) Credencial em nome próprio.** Não há humano na origem, e as duas saídas erradas eram pedir
emprestada a credencial de um operador (uma mentira selada na cadeia) ou não selar nada (destruição
não-atribuível). O scheduler age sob `nhi:aos-node/retention-scheduler`, nomeado no selo junto do
trust anchor do issuer deste deployment. Cada passagem sela **dois** registos na cadeia
`governance.retention`: `retention.sweep.started` **ANTES** e **fail-closed** (WORM recusa ⇒ a
passagem **não corre**) e `retention.sweep.completed` **depois**, com contagens
(varridos/expirados/**retidos por hold**/saltados/não-expirados) e **sem PII**. O `started` era
indispensável: o `retention.expired` que o job sela por-registo **não leva `Principal`**
(`audit.BuildRetentionExpiredRecord`), pelo que a destruição automática ficaria na cadeia sem
ninguém a quem a atribuir. Os três eventos partilham a partição de propósito — numa cadeia gapless
a **ordem** é ela própria a prova (`TestAOS267_SweepSealsOwnCredentialAndCountsWithoutPII` asserta-a).

**(d) Cadência.** `AOS_RETENTION_SWEEP_INTERVAL`, default **`1h`** declarado (não o minuto dos
outros varrimentos: um TTL de retenção mede-se em dias/meses e cada passagem lê **todos** os
streams do Event Store). Fail-closed: ilegível ou `≤0` ⇒ `ErrBadRetentionSweepInterval` e o nó não
arranca. **Nenhum valor desliga** o scheduler — nem `0`, porque desligá-lo com a política definida é
exactamente a violação de *storage limitation* que este ticket fecha; quem não quer expiração deixa
`AOS_RETENTION_*` por definir.

**(e) Serialização.** O guard `expireInFlight` **mudou do `apiHandler` para o `NodeService`**: um
guard no handler só excluiria as invocações da rota entre si, e `POST /dsar/expire` voltaria a poder
correr em simultâneo com o tick, selando dois `retention.expired` para o mesmo facto. Guarda:
`TestAOS267_RouteAndSchedulerShareOneGuard`.

**(f) Integridade.** Paridade AOS-221 com a rota: verificação pós-shred da hash-chain. Se ela deixar
de validar, o laço **PARA** (não se destrói mais sobre um log de auditoria que já não se verifica) e
o incidente fica no log com o eixo nomeado.

Env nova na tabela AOS-203 (`deploy/node/README.md`): `AOS_RETENTION_SWEEP_INTERVAL`. Campos novos
em `Node` (`Retention`, `IssuerID`) para que a decisão de armar e o selo em nome próprio digam a
verdade sem uma segunda leitura do ambiente.

---

## AOS-268 — Verificação ancorada do WORM (checkpoint assinado no restart)

### Contexto
**Grupo C.** A re-verificação detecta mutação/inserção/remoção-interna, não a **truncatura do tail** nem reescrita desde a génese. `Signer`/`VerifyFromCheckpointAtHead` existem e estão testados (único consumidor: `platform/dr`); nenhum ticket/DEF possui o wiring no nó. Fonte: análise 08-08 (frete AOS-221/072).

### Objectivo
No restart, verificar contra o último checkpoint assinado com `expectedHead` persistido; a selagem periódica é out-of-process (custódia D4/AOS-156).

### Critérios de Aceitação
- [x] Envs no molde `AOS_POLICY_TRUST_ANCHOR` (`AOS_WORM_TRUST_ANCHOR` pubkey out-of-band + `AOS_WORM_CHECKPOINT_FILE` + `AOS_WORM_EXPECTED_HEAD`, piso de frescura persistido); ausentes ⇒ comportamento actual declarado no banner (`wormAnchorPostureBanner`); parciais ⇒ arranque aborta (`ErrWormAnchorIncomplete`).
- [x] Tail truncado **até ao checkpoint selado** (head cai a/abaixo de `cp.AuditSeq`) ou cadeia re-escrita ⇒ arranque aborta com erro nomeado (`audit.VerifyFromCheckpointAtHead` na etapa 2a-bis do `Bootstrap`, com `to == cp.AuditSeq`: `ErrCheckpointStale`/`ErrRangeBeyondHead` para truncatura/rollback, `ErrCheckpointAnchor` para reescrita da génese, `ErrCheckpointSignature` para checkpoint forjado). LIMITE HONESTO: a **janela não-selada** acima do checkpoint (registos anexados após a última selagem) é coberta só pelo re-encadeamento de AOS-221 — que NÃO vê a truncatura do tail — e encolhe com a selagem periódica out-of-process, deferida em DEF-268 (eixo AOS-156/D4). O piso `AOS_WORM_EXPECTED_HEAD` fecha o rollback, não esta janela.
- [x] Teste: truncatura do WAL detectada; checkpoint forjado rejeitado (`packages/cmd/aos/aos268_worm_anchor_test.go`, com controlo de dois sentidos — o mesmo WORM truncado arranca SEM âncora e aborta COM âncora).
- [x] DEF registado para a selagem out-of-process (DEF-268/DEF-269, dono: custódia de chave, eixo AOS-156/D4).

### Estado
**FEITO** (verificação ancorada composta e testada; a selagem periódica out-of-process fica deferida em DEF-268, a jusante de D4/AOS-156).

---

## AOS-269 — ADR-021: scoring determinístico no Model Gateway

### Contexto
ADR-021 **assumido aprovado**: o router lexicográfico (AOS-059) passa a scoring ponderado determinístico **após** as guardas estruturais (soberania/allowlist/capacidade nunca são factores); factores como portas injectáveis em aritmética inteira/ponto-fixo (zero floats no data-plane); pesos como artefacto comportamental assinado (SemVer + eval-gate, ADR-012); calibração **offline**, nunca bandit/online (replay intacto, ADR-010).

### Objectivo
Implementar o estágio de scoring no GW com tabela de pesos assinada e sinal de *task-fit* dos evals (fecha o loop EPIC-08 → EPIC-06).

### Critérios de Aceitação
- [x] Portas de factores (health, headroom, custo, latência, task-fit, estabilidade) com impls de referência determinísticas; aritmética inteira. — porta comum `scoring.FactorProvider` + seis impls em `routing/scoring/providers.go` (`LadderCost`, `HeadroomFactor`, `StaticLatency`, `StaticHealth`, `StaticTaskFit`, `StaticStability`); **custo e headroom NÃO são sinal novo**: derivam da escada de tiers existente e da porta `LoadProvider` do router (`router.HeadroomReaderFrom`). Tudo inteiro em milésimos (`scoring.Scale = 1000`), normalização por divisão inteira.
- [x] Tabela de pesos (perfis nomeados) carregada fail-closed — sem tabela válida/assinada, o router recusa. — `policy/weights` no **molde exacto** de `policy/allowlist`: artefacto embebido + digest canónico + assinatura ed25519 + **trust anchor pinado em código**, com `ErrTableMalformed`/`ErrSignatureInvalid`/`ErrTrustAnchorMismatch`/`ErrPubKeyInvalid`; schema fechado e **SemVer validado** (ADR-012). `scoring.NewScorer` não se constrói sem tabela; um scorer não-armado faz o router **rejeitar** toda a rota (defesa em profundidade). ⚠️ **ALCANCE (não confundir com efeito em produção) — SUPERADO em 2026-08-13 por AOS-280.** À data deste ticket o fail-closed valia **quando o scoring estivesse composto**, e o `router.Router` **não estava composto no pipeline do GW** (`NewProduction` usava `failover.NewStage`; `router.New` não tinha chamador fora de testes) — lacuna **pré-existente**, registada em **DEF-271** e fixada em alcance pela **emenda 1.1 do ADR-021** (§5-bis). **Hoje o router ESTÁ composto** (`production_routing.go`, cadeia `failover` → `routingstage`+`router`): quando o deployment declara `RoutingConfig.Tiers`, este critério é verdade também sobre o caminho de produção — sem tabela válida o **GW não arranca**. Ver §AOS-280.
- [x] Guard-test: a decisão é função pura dos inputs (sem `rand`/relógio); cenário AOS-063 alargado prova que nenhum peso elege candidato cross-border ou fora da allowlist. — `TestScenario7_Scoring_PureFunctionDeterministicNoFloats` (64 repetições idênticas + independência da ordem + **análise AST** que proíbe floats/`rand`/`time.Now` em `routing/scoring`, `routing/router`, `policy/weights`, `routing/tiering`, `routing/degradation`, com auto-verificação de não-vacuidade); `TestScenario6_Scoring_GuardsFirstNeverElectsCrossBorderOrNonAllowlisted` (pesos gananciosos a favor do cross-border e de um modelo fora da allowlist ⇒ REJECT/rota intra) + meta-teste `TestMetaDetects_ScoringElectsCrossBorderWhenGuardsBypassed`.
- [x] Span `model_routing`/`DecisionSink` registam perfil, factores e score; `model_swap` inclui a razão de scoring. — atributos `aos.routing.scored|score|score_profile|score_weights_version|score_factors|score_scale`; `Decision` transporta `Scored`/`ScoreProfile`/`WeightsVersion`/`Score`/`ScoreFactors`; a razão de scoring é **preservada** mesmo quando a degradação por orçamento reescreve a razão, e a cadeia real (tabela assinada → scorer → router → `routingstage` → Gateway) é provada em `routing_scoring_wiring_test.go` a ver a variância `model_swap` carregar perfil+versão+score+factores.
- [x] Formato da tabela e pesos iniciais documentados em `tecnica/06`; cobertura ≥ `ROUTING_COVERAGE_MIN`. — `tecnica/06` §6.1 (as cinco regras e onde estão impostas, tabela de campos do schema, pesos iniciais dos perfis `balanced`/`fast`/`cheap`/`quality`, postura de compatibilidade, observabilidade), §8.4 (cenários 6–8) e §9 (três riscos novos). Cobertura do módulo GW: **88,2 %** (piso 80).

### Postura de compatibilidade (decisão declarada)
O scoring é **opt-in por composição** (`router.WithScoring`). Sem essa opção o router mantém AOS-059 **byte-a-byte** — um nó já implantado, sem tabela de pesos, não deixa de rotear (provado por `TestScenario8_Scoring_FailClosedWithoutSignedWeights` (D) e por `TestAOS269_Gateway_SemScoringMantemComportamento`). Com essa opção a tabela é **obrigatória** e a sua ausência/invalidez é uma **recusa** — a leitura da regra 3 do ADR-021 («sem tabela válida o router recusa») que se aplica *quando o scoring está composto*, sem impor uma tabela a quem não pediu scoring.

### Estado
**FEITO — a MÁQUINA; o WIRING fica em DEF-271.** Entregue: portas de factores em aritmética inteira, tabela de pesos embebida e assinada com carregamento fail-closed, guard-test AST (proíbe float/`rand`/relógio no caminho de decisão, com auto-verificação de não-vacuidade), cenário de soberania que prova que nenhum peso elege cross-border, e observabilidade (perfil/score/factores no span, `DecisionSink` e `model_swap`). Gate `scripts/ci/routing.sh` alargado: `REQUIRED` passa a 8 cenários + 10 meta-testes + relatório (novas entradas `scoring_guards_first`, `scoring_failclosed_weights`, `scoring_deterministic`, `scoring_compat_lexicographic` no `AOS_ROUTING_REPORT`).

⚠️ **SEM EFEITO EM PRODUÇÃO À DATA DESTE TICKET — SUPERADO em 2026-08-13 por AOS-280.** O `router.Router` (AOS-059) sobre o qual este scoring assenta **não estava composto no pipeline do gateway** — `NewProduction` compunha `failover.NewStage`, e `router.New` não tinha chamador fora de testes em todo o repositório. Era lacuna **pré-existente** a este ticket, registada em **DEF-271** (com o ticket próprio que a fechou) e fixada em alcance pela **emenda 1.1 do ADR-021** (§5-bis, 2026-08-13, autoridade de dono). **AOS-280 fechou o DEF-271**: o slot de roteamento é hoje a cadeia `failover` → `routingstage`+`router` com o scoring assinado armado, provada ao nível do gateway composto. A regra 3 do ADR-021 **deixou de estar inerte** no caminho de produção de quem declara a escada de tiers — e a actualização do §5-bis do ADR (autoridade congelada) fica como **emenda 1.2 pendente de dono**, registada em **DEF-280-ADR021**; o ADR **não** foi editado por esta remediação.

**NOTA DE CUSTÓDIA:** a seed ed25519 da tabela de pesos foi gerada com `crypto/rand` fora do repositório e **não** está comitada — o repo tem só `.json`/`.sig`/`.pub` (material público). Rodar a chave exige `AOS269_GENERATE_KEY=1 go run gen_signature.go` e actualizar `trustAnchorFingerprint` (code-review), como na allowlist regional.

---

## AOS-270 — ADR-022: arestas condicionais declarativas no PlanDocument

### Contexto
ADR-022 **assumido aprovado**: o `Node` admite arestas com condição (subconjunto fechado do schema, sem código arbitrário), avaliada deterministicamente sobre o resultado registado — nunca pelo LLM em runtime. Uma aresta condicional **nunca fecha ciclo**: o ramo de reprovação aponta para nós ainda não executados; o retorno a executados continua a ser replan de subgrafo (AOS-239).

### Objectivo
Estender o schema e o despacho com arestas condicionais, preservando aciclicidade, replay e orçamento.

### Critérios de Aceitação
- [x] Schema fechado (`DisallowUnknownFields`) com o campo de condição; validador puro (AOS-231) rejeita condicional que feche ciclo (reusa a aciclicidade incremental).
- [x] `plandispatch` avalia a condição como função pura do resultado registado; evento de decisão de ramo emitido; replay reproduz o ramo sem re-avaliação.
- [x] Avaliação de condições debita orçamento da árvore (ADR-008).
- [x] Adversarial: «ciclo disfarçado de condicional» rejeitado (AOS-244 alargado).

### Estado
**FECHADO.**

**O que ficou composto.**

- **Gramática** (`plan/condition.go`, documentada em `tecnica/18` §3.3.1): `Node.conditional_on[]` — arestas *origem→destino* guardadas por uma **conjunção plana** de 1..8 predicados `(observável, operador, operando)`. Três observáveis (`terminal_state`, `verdict`, `metric`) e seis operadores, todos enums fechados; operandos de enum fechado ou **inteiros** (nunca float — determinismo de replay). Sem aninhamento, sem disjunção, sem aritmética, sem interpolação: um predicado compara, não computa. Todas as combinações são conjunções, pelo que uma condição só torna um nó **menos** despachável — nunca relaxa um `depends_on` nem o gate.
- **Aciclicidade REUTILIZADA, não reimplementada** (`planvalidate/validate.go`): as arestas condicionais entram **no mesmo `orchestrator.NewDAG`/`AddEdge`** de AOS-025, numa segunda passagem (para o feedback nomear o canal culpado: `conditional_cycle` vs `cycle`). O «ciclo disfarçado de condicional» morre no primitivo que já existia. A regra 1 exige origem existente e recusa a sobreposição com `depends_on`; a regra 4 conta a **união** dos dois canais no `max_fanout`/`max_depth` (senão o canal novo era uma saída livre dos tectos).
- **Avaliação e replay** (`plandispatch/condition.go`, `branches.go`): função pura do **resultado registado**; a decisão é apensa como `plan.branch_decided` (constante junto do emissor em `plannerevents`, step id determinístico por nó ⇒ facto único) com o **digest canónico** da expressão. Numa segunda passagem a decisão é **lida** e o avaliador não é alcançado — provado destrutivamente (a vista de resultados é posta a falhar e o despacho reproduz o mesmo ramo). Digest divergente ⇒ documento editado ⇒ fail-closed. Um ramo não tomado **poda** o nó e a descendência (`branch_not_taken`) em vez de os deixar bloqueados em silêncio.
- **Orçamento** (`planbudget/`): `TreeBudgetMeter` liga a porta de débito ao `budget.Reserver` real (Reserve→Commit, cadeia de ancestrais). Debita **uma vez por decisão**, nunca por tentativa — uma condição indecisa não custa nada, senão o escalonador drenava a árvore à espera. Vive em pacote próprio porque o guard de imports de `plandispatch` (fronteira ADR-018) só admite `plan`+`plannerevents`; liga-se por tipo estrutural.
- **Escopo.** Só a extensão §2.1. `role: verifier` (§2.2) e payload tipado (§2.3) são AOS-271/272; o bump de `plan_version` e a migração são AOS-273 — o campo é **opcional e aditivo**, pelo que **não** consome MAJOR e a compatibilidade fica preservada nos dois sentidos declarados em `plan/doc.go`.

**Verificação:** `go test ./...` verde no módulo `control-plane/orchestrator` (inclui `plan`, `planvalidate`, `plandispatch`, `planbudget`, `planadversarial`); `gofmt`/`go vet` limpos; gate `event-catalog` OK (108 tipos, zero literais novos).

---

## AOS-271 — ADR-022: `role: verifier` com semântica de sistema

### Contexto
ADR-022 §2.2: o verificador deixa de ser rótulo — read-only por construção (NHI sem tools de efeito), produtor ≠ verificador (o validador rejeita auto-verificação na sub-árvore), veredicto estruturado como evento — o único resultado que as condições de qualidade (AOS-270) consomem. Verificar debita orçamento como qualquer nó.

### Objectivo
Materializar o papel verificador com enforcement pelo sistema, não por convenção.

### Critérios de Aceitação
- [x] `planmaterialize` emite a NHI do verificador sem autoridade de efeito; o RM/RiskGate é a segunda linha, fail-closed.
- [x] Validador rejeita verificador da própria sub-árvore de delegação.
- [x] Veredicto tipado (`pass/fail + razões + métricas`) **definido e imposto pelo construtor** (`plannerevents.NewVerdictRecorded`, com os sujeitos amarrados às arestas de entrada do verificador no documento aprovado) e **projectado** para o observável do ramo (`plandispatch.ResultFromVerdict`).
- [ ] Veredicto tipado **REGISTADO como evento** `aos.planner.v1` por um verificador em execução — **NÃO ENTREGUE**: `Recorder.RecordVerdict` não tem chamador de produção (o *wiring* do ciclo-de-vida do run é AOS-238). Residual com eixo em `docs/governance/REGISTO-Deferimentos.md` (**DEF-272**).
- [x] Adversarial: verificador auto-referente rejeitado.

### Estado
**FECHADO com residual declarado (DEF-272).**

**O que ficou composto.**

- **Papel reservado** (`plan/plandocument.go`): `role` continua texto livre; **um** literal — `plan.RoleVerifier` — passa a ser reservado e *case-sensitive* (`Verifier` não é o papel: comparar sem distinção daria a um plano hostil um rótulo que **parece** verificador ao revisor do PlanCard e escapa ao clamp). `Node.IsVerifier()` é a **única** leitura do literal no módulo. Aditivo em SIGNIFICADO, não em FORMA ⇒ não consome MAJOR de `plan_version`.
- **Critério de «tool de efeito», DECLARADO e DERIVADO** (`planvalidate.IsEffectTool`): o ADR §2.2 enumera exemplos («escrita MEM, egress, spawn»), não um critério — e uma allowlist de nomes envelheceria mal. O critério deriva-se dos **eixos de risco PINADOS** que a regra 6 (AOS-232) já consome: **efeito ⇔ `egress ≠ none` OU `reversibility = irreversible`**. Fail-closed **pelo tipo**, sem uma linha para isso (os valores-zero de ambos os eixos são os perigosos ⇒ uma capability por classificar conta como de efeito). A **sensibilidade não entra**: ler material sensível é uma leitura, e um verificador que não pudesse observá-lo não verificava nada.
- **«Produtor ≠ verificador» — a direcção, registada em `tecnica/18` §3.3.2.** A frase do ADR admite à letra uma leitura **impossível de satisfazer** (se «sub-árvore de W» fosse «descendentes de W», o organigrama do próprio ADR era rejeitado: um verificador tem de **ler** o trabalho, logo depende dele). A leitura que resta e tem conteúdo de segurança: o verificador pode estar **a jusante** do trabalho, nunca **a montante** — um nó de que o trabalho descende é o que **encabeça a sub-árvore que o produziu** (a definição que `planmaterialize.DefaultClassifier` já usava). Decidido por **alcançabilidade no MESMO DAG de admissão** de AOS-025: `orchestrator.DAG.Reachable` **expõe** a travessia que a aciclicidade já fazia, em vez de a duplicar; `checkAcyclic` passou a `buildAdmissionDAG`, que devolve o grafo construído uma só vez.
- **Três regras novas no validador** (`planvalidate/verifier.go`, sub-códigos próprios): `verdict_not_from_verifier` (só um `role: verifier` é origem de um predicado sobre `verdict` — **é isto que fecha o buraco de AOS-270**), `verifier_without_subject` (um verificador solto não observa nó nenhum; o seu `pass` é uma constante com nome de veredicto), `verifier_self_subtree` (produtor ≠ verificador) e `verifier_effect_tool` (read-only por construção). O **interruptor datado** `verdictSupported = false` de AOS-270 foi **removido**: o `verdict` deixou de ser recusado em bloco — passou a ser **atribuído**.
- **Veredicto tipado como facto** (`plannerevents`, `plan.verdict_recorded`): `node_id` do emissor + `subjects[]` (1..32, **nunca** o emissor) + `outcome` (`pass`\|`fail`, constantes **derivadas** de `plan.EnumPass/EnumFail` — não há tabela de tradução entre emissor e consumidor que possa divergir) + `reasons[]` (0..16 **códigos** de charset fechado) + `metrics[]` (0..32, nome fechado + **inteiro**). A validação é do construtor (`NewVerdictRecorded`), não do chamador. **As razões são códigos e nunca frases**: o verificador olha para material untrusted, e um campo de prosa fazia do veredicto o canal por onde o conteúdo do trabalho entrava no Event Store com a autoridade de um facto. `step_id` por nó ⇒ facto **único e imutável** por verificador (nenhum segundo `pass` substitui um `fail`).
- **Clamp da NHI** (`planmaterialize`): `authorityForNode` ganhou um **segundo clamp** para o papel verificador — as tools de efeito não são sequer mapeadas para capability, pelo que a NHI emitida não tem nada para o RM negar no caminho quente. O critério chega por `WithEffectOracle`, ligado a `planvalidate.Snapshot.EffectOracle()` **por tipo estrutural** (uma definição, dois pontos de enforcement, zero import entre os pacotes); o default é fail-closed (**tudo** conta como efeito ⇒ verificador com autoridade vazia: falha visivelmente em vez de silenciosamente). **Buraco fechado de passagem:** a tool call do nó-folha era `Tools[0]` — num verificador podia ser uma tool de efeito **fora** da autoridade clampada do próprio nó; passou a sair do mesmo filtro (`primaryTool`). O que o clamp retira fica **visível** em `plan.materialized`.
- **Circuito fechado** (`plandispatch/verdict.go`): `ResultFromVerdict` projecta o facto emitido no resultado registado que a condição observa — `outcome` e `metrics[]` atravessam, **as razões não** (se um ramo pudesse condicionar sobre elas, a gramática fechada de §2.1 ganhava um quarto observável sem passar pelo schema) e o estado terminal também não (é do ciclo de vida, AOS-017).
- **Grammar única.** O node_id e o identificador de métrica/código passaram a viver em `plan` (`ValidNodeID`, `ValidIdentifier`), consumidos pelo validador **e** pelos payloads de evento: uma segunda cópia divergiria, e a divergência entre «o que o validador aceita» e «o que o log admite» é ela própria uma superfície.

**Adversarial** (`planadversarial/verifier_test.go`, vector 7 — os três disfarces esgotados): produtor emite o próprio veredicto; verificador certifica a própria sub-árvore (a auto-certificação com um passo de indirecção); verificador com tool de efeito; nó que observa o próprio veredicto (morre no primitivo de AOS-025, sem regra nova). Em todos: o documento **passa a forma** (a defesa não é o parser), a validação rejeita com sub-código atribuível, e o sink **não é tocado**. Mais o vector de vazamento: uma razão em prosa não entra no veredicto.

**Verificação:** `go test ./...` verde no módulo `control-plane/orchestrator` (`plan`, `planvalidate`, `planmaterialize`, `plandispatch`, `plannerevents`, `planadversarial`); `gofmt`/`go vet` limpos; tipo de evento novo é constante junto do emissor, na família `plan.*` já catalogada.

**Residual nomeado — COM eixo de segurança (DEF-272).** A **emissão** do veredicto é a porta `Recorder.RecordVerdict` e **não tem chamador de produção**: quem a chamaria por um nó verificador em execução é o ciclo-de-vida do run (a jusante de **AOS-238**). O eixo de segurança **existe** e foi nomeado pela auditoria adversarial da wave: com a admissão a ACEITAR ramos sobre `verdict` e nenhum emissor ligado, um ramo de qualidade **admitido** seria **decidido** como `branch_not_taken` e o facto ficaria **registado** (imutável) — a poda silenciosa que o interruptor datado de AOS-270 existia para impedir. **Remediado nesta passagem, do lado do despacho:** a ausência de um observável no resultado registado passou a ser **INDECIDA** (o nó espera em `waiting_condition`), nunca falsa — nenhuma decisão de ramo é registada sobre um observável que ninguém produz (`plandispatch/condition.go`, provado por `TestQualityBranchWithoutEmitterRecordsNoDecision`). Enquanto o *wiring* não existir, um plano com ramo de qualidade fica **parado e visível**, não podado.

---

## AOS-272 — ADR-022: payload tipado por aresta

### Contexto
ADR-022 §2.3: nós declaram `outputs`, arestas declaram `consumes` — contratos (nome, schema, taint) validados estaticamente; o transporte é **referência** a registo no Event Store/MEM com proveniência («contexto ≠ registo»), nunca blackboard mutável.

### Objectivo
Validação estática de contratos entre nós e propagação explícita de taint pelas arestas.

### Critérios de Aceitação
- [x] O validador rejeita `consumes` inexistente, de tipo incompatível, ou com taint incompatível com a autoridade do consumidor (ADR-005).
- [x] O **contrato** de transporte por referência está definido e imposto na fronteira de emissão (`plan.payload_published`: locator + digest + taint + proveniência, **sem** campo de conteúdo para as formas abertas) e a resolução é **por contrato declarado** (`plandispatch.PayloadResolver.Inbox`, que re-verifica tipo, taint e `contract_digest` contra o documento aprovado).
- [ ] O **consumidor recebe** de facto a referência num run — **NÃO ENTREGUE**: não existe implementação de `plandispatch.PayloadView` nem chamador de `Recorder.RecordPayloadPublished` fora de testes (o *wiring* do ciclo-de-vida do run é AOS-238). Residual com eixo em `docs/governance/REGISTO-Deferimentos.md` (**DEF-273**).
- [x] Adversarial: payload com taint elevado para consumidor privilegiado rejeitado.

### Estado
**FECHADO com residual declarado (DEF-273).**

**O que ficou composto.**

- **Onde vive o `consumes`, e porquê** (`plan/payload.go`, documentado em `tecnica/18` §3.3.3). O `consumes` teria de servir os **dois** canais de aresta (`depends_on` e `conditional_on`), e `depends_on` é `[]string`: alargá-lo a objectos é quebra de **forma**, logo MAJOR — que é AOS-273, não este ticket. A aresta de dados declara-se por isso **no extremo consumidor**: `Node.consumes[]` nomeia a origem, o output e o tipo esperado; o par (origem → este nó) *é* a aresta. Opcional e aditivo ⇒ **sem MAJOR** (a compatibilidade nas duas direcções já declarada em `plan/doc.go` mantém-se).
- **O grafo de dados é sub-grafo do DAG de admissão.** A origem de um `consumes` **tem** de ser uma aresta de entrada já declarada. Duas consequências, ambas desejadas: não se lê o trabalho de quem não se espera (uma leitura sem precedência é uma corrida com o produtor, e o resultado passaria a depender do escalonador — parte §3.4/ADR-010); e o grafo de payload é **acíclico por construção**, sem travessia nova e sem um detector que se possa esquecer de correr — o mesmo movimento de AOS-270 (as arestas guardadas por condição entram no MESMO DAG).
- **Os tipos** (enum fechado de cinco): `summary`, `record`, `artifact` (admitem conteúdo) · `metrics`, `verdict` (**forma fechada**: só símbolos, códigos de charset fechado e inteiros, validados na fronteira de emissão). **A compatibilidade é IDENTIDADE** — sem subtipagem nem coerção: uma relação de subtipagem obrigaria o validador a **raciocinar** sobre tipos, e um raciocínio é uma superfície onde uma incompatibilidade real se perde.
- **O taint NÃO deriva da palavra do planeador — e o TIPO sozinho não chega** (corrigido pela auditoria adversarial da wave). Não há taxonomia nova: são os dois rótulos de ADR-005. A forma **fechada** (`metrics`/`verdict`) é **necessária mas não suficiente** para `trusted`: `type` é um campo do documento *untrusted*, e um plano hostil declarava `type: metrics` num output de conteúdo para entregar material untrusted a um consumidor privilegiado com rótulo trusted. O rótulo `trusted` exige agora **as duas** condições: *(i)* o tipo é de forma fechada **e o produtor é um nó `role: verifier`** — o ponto de desclassificação que §2.2 sanciona (independente do trabalho, read-only por construção, e sem produzir trabalho ele próprio); *(ii)* a forma é **imposta na emissão** — um `plan.payload_published` de tipo fechado carrega o conteúdo **INLINE** (símbolos de enum, códigos de charset fechado e inteiros, validados pelo construtor) e **não tem locator**, pelo que não há referência opaca por onde prosa possa viajar com rótulo trusted. Tudo o resto é `untrusted`, o que subsume a propagação pelo reticulado (um nó não-verificador não pode publicar trusted, logo não pode **lavar** untrusted→trusted num salto). O campo `taint` do documento é **advisory e só eleva**; declarar `trusted` num resumo é **ignorado**. Fail-closed pelo tipo e pelo papel.
- **«Autoridade do consumidor» reutiliza o critério de AOS-271.** O `TaintGate` do RM classifica «privilegiado» com uma allowlist de **nomes** do operador; o validador de planos não tem esse conjunto (nem devia — é política de ápice) mas tem o que os nomes representam: os **eixos pinados**. O critério é **exactamente** `planvalidate.IsEffectTool` (`egress ≠ none` **ou** `irreversible`) — uma definição, duas perguntas («um verificador pode pinar isto?», «este consumidor é privilegiado?»), zero taxonomias novas. Fail-closed em toda a direita da resolução (tool que não resolve ⇒ privilegiada); um nó **sem tools** não é privilegiado.
- **Quatro rejeições com sub-código próprio** (`planvalidate/payload.go`, regra 1-quater, a jusante de `checkTools` porque a autoridade deriva de tools que RESOLVEM): `consumes_unknown_edge`, `consumes_unknown_output`, `consumes_type_mismatch`, `consumes_taint_authority`. A consequência prática de a última é a desejada e está declarada: **um nó com egress não consome um resumo de outro nó** — é a barreira P0 de ADR-005 aplicada na admissão, antes de queimar um token, em vez de no RM depois do spawn. O caminho legítimo escreve-se no organigrama (verificador §2.2, ou nó `danger` com approval-card); um payload de forma **fechada** alimenta um consumidor privilegiado sem fricção. O que deixa de existir é o caminho **silencioso**.
- **O transporte é REFERÊNCIA — e as quatro propriedades do blackboard estão negadas por construção.** *(i)* qualquer-um-lê-qualquer-coisa → o `plandispatch.PayloadResolver.Inbox` entrega **só** os contratos declarados pelo nó; não existe operação que devolva «tudo o que há». *(ii)* o-valor-muda-debaixo-de-quem-lê → `plan.payload_published` tem `step_id` **por contrato** ⇒ facto único e imutável, e o `digest` do conteúdo deixa quem resolve **verificar** o que leu. *(iii)* o-conteúdo-viaja → o facto **não tem campo de conteúdo** (provado sobre o JSON serializado, não por disciplina do chamador): atravessa locator + digest + tipo + taint + proveniência. *(iv)* a-proveniência-dilui-se → `derived_from[]` carrega os contratos de que o payload deriva, pela ordem publicada.
- **Fronteira ADR-018 intacta.** `plandispatch` continua a importar só `plan`+`plannerevents`: a `PayloadView` é uma **porta** e `RefFromPublished` é uma **projecção** pura do facto — o mesmo desenho de `ResultFromVerdict` (AOS-271), não uma superfície nova.

**Adversarial** (`planadversarial/payload_test.go`, vector 8 — os disfarces esgotados): payload untrusted para consumidor privilegiado (os **dois** eixos, egress e irreversibilidade, provados em separado); taint lavado por declaração (`taint: trusted` num resumo); taint lavado por **tipo** (pedir o mesmo output como `metrics`, na esperança de que a compatibilidade fosse avaliada sobre o tipo pedido); aresta de dados **escondida** (consumir sem declarar a dependência); output fantasma. Em todos: o documento **passa a forma** (a defesa não é o parser), a validação rejeita com sub-código atribuível, e o sink **não é tocado**. Mais a não-vacuidade: um resumo para consumidor sem autoridade **passa**, e um payload de forma fechada para consumidor privilegiado **passa**.

**Verificação:** `go test -count=1 ./...` verde nos 16 pacotes do módulo `control-plane/orchestrator`; `gofmt`/`go vet` limpos; tipo de evento novo é constante junto do emissor, na família `plan.*` já catalogada (gate `event-catalog`).

**Residual nomeado — COM eixo de segurança (DEF-273).** A **implementação** da porta `plandispatch.PayloadView` sobre o Event Store/MEM — e a chamada a `Recorder.RecordPayloadPublished` por um nó que termina — é *wiring* do ciclo-de-vida do run, a jusante de **AOS-238**, exactamente onde a emissão do veredicto de AOS-271 ficou. O contrato, a validação estática, a derivação do taint, o schema da referência e a resolução por contrato estão fechados; até esse wiring existir, o consumo é fail-closed (`ErrPayloadNotPublished`: sem referência publicada não há leitura, nunca um valor por omissão). **A afirmação anterior de que este residual não tinha eixo de segurança era FALSA** e foi retirada: sem o oráculo de efeito ligado (`planmaterialize.WithEffectOracle`) o materializador usa o `DefaultEffectOracle` — **tudo** conta como efeito — e emite verificadores com `Authority[]` **vazia**, isto é, o AC1 de AOS-271 cumpre-se por o verificador não poder fazer nada. O eixo está registado; o gap não é «uma porta por ligar», é uma capacidade por entregar.

---

## AOS-273 — ADR-022: `plan_version` bump, migração e golden-sets

### Contexto
ADR-022 §4: o schema cresce — nova `plan_version`, migração via `planmigrate` (AOS-243); golden-sets do planeador (AOS-241) e cenários adversariais (AOS-244) têm de cobrir as extensões.

### Objectivo
Tornar as extensões um artefacto comportamental versionado, com janela de suporte e eval-gate.

### Critérios de Aceitação
- [x] `plan_version` **incrementada**: `1.1.0` → **`1.2.0`** (MINOR), com a justificação escrita onde é lida (`plan/semver.go`) e a linha MINOR-a-MINOR em `tecnica/18` §3.6.1.
- [x] **O bump é IMPOSTO, não confiado** (remediação da auditoria adversarial). O `plan_version` é um campo do documento *untrusted* e nada ligava a versão **declarada** às *features* **usadas**: um produtor carimbava `1.1.0`, emitia `outputs`/`consumes`, e o plano era admitido, aprovado e congelado com um carimbo que não identificava o schema — deixando um *reader* dessa linha, retido legitimamente, a falhar o replay com um erro **não atribuível** por nenhuma política de `planmigrate`. A regra 1 de `planvalidate` deriva agora o **piso** de versão da tabela `plan.FeatureFloor` (`conditional_on` ⇒ ≥1.1.0; `outputs`/`consumes`/`role: verifier` ⇒ ≥1.2.0) e recusa com `plan_version_below_features`; o simétrico (MINOR acima do que este leitor publica) morre em `plan_version_ahead_of_reader`. Par de ouro no golden-set: a **mesma fixture byte-a-byte** carimbada `1.2.0` é admitida e carimbada `1.1.0` é recusada — a rejeição é do carimbo, não do conteúdo. Lado do produtor fechado por via governada: `prompt_version` `1.0.0` → **`1.1.0`** (ADR-012), com a regra 6 do template a nomear a linha e as três extensões.
- [x] **Migração da versão anterior testada.** Nesta linha a migração É a ausência de migração — e é isso que está provado, não declarado: documentos de `1.0.0` e `1.1.0` congelados em `planmigrate/testdata/schemaline` continuam a decodificar pelo leitor `1.2.0`, re-serializam-se nos **mesmos bytes** e produzem o **mesmo** hash de binding, contra constantes congeladas **fora do binário**.
- [x] **Rejeição de MAJOR incompatível**, nos **dois** gates que existem e são distintos de propósito: por **proposta** (`planvalidate` → `plan_version_incompatible`, regra 1) e por **reader** (`planmigrate.Policy.Admit` → `ErrOutsideSupportWindow`, na leitura *e* na escrita, e na escrita antes de tocar no REG/RM).
- [ ] O gate por *reader* a correr numa **composição de produção** — **NÃO ENTREGUE**. `planmigrate.NewPolicy` não tem chamador fora de testes: o wiring do ciclo-de-vida do run é **AOS-238**, o mesmo eixo de DEF-272/273/274. O gate por proposta corre no caminho real do validador; o gate por *reader* é hoje **capacidade de contrato**, não facto de fim-a-fim, e a wave anterior não o tinha declarado. O que a remediação fechou foi a divergência silenciosa: a janela deixou de viver só em prosa e numa variável de teste e passou a ter âncora em código (`planmigrate.DeclaredWindow`, com `tecnica/18` §3.6.1 citada), com teste de coerência contra `plan.CurrentPlanVersion` e meta-teste a exigir uma fixture congelada por cada MINOR anterior («bumpaste a linha e não congelaste a anterior»).
- [x] Golden-sets com planos **condicionais / verificador / payload** passam no **eval-gate do planeador** (`plannerprompt.Evaluate`, AOS-241): 5 casos novos (3 positivos + 2 negativos — o segundo negativo, `adr022-must-reject-stale-version`, entrou na remediação e cobre o carimbo), segurança **12/12** (a regra 100% de K), qualidade **8/10**, e **sem regressão distribucional** face ao baseline pré-extensões. O corpus publicado está **pinado como literal fora da árvore** (`adr022PinnedCorpus`): sem esse ponto fixo a governação de `ValidateGoldenMutation` comparava duas expressões do mesmo código e não prendia um editor que apagasse um caso difícil dos dois lados.
- [x] Os mesmos casos correm no **gate de CI `evalgate.sh`** — **ENTREGUE por AOS-279** (2026-08-13, fecha DEF-276). O gate passou a correr o módulo do planeador **separadamente** e a consumir uma linha marcada própria (`AOS_PLANNER_EVAL_REPORT`, emitida por `TestPlannerEvalReportEmitted`), impondo: cobertura **não-vazia** (um relatório a zeros passaria por ausência de evidência), **segurança 100%** (regra, não limiar) e o **veredicto da política do próprio planeador** (`passed`). A qualidade fica como SINAL reportado e não medida contra o piso do outro harness: é **8/10 por desenho** — dois candidatos fracos existem para falhar a rubrica, e impor-lhes 0.90 convidaria a apagá-los, que é a cobertura que dá valor ao conjunto. NÃO se ligou `Evaluate` ao harness de `platform/eval`: isso faria esse módulo importar `control-plane/orchestrator` (acoplamento que o layer-lint guarda). Falsificável nos quatro vectores (cobertura vazia, segurança degradada, `passed:false`, linha truncada).
- [x] **Janela de suporte documentada**: `tecnica/18` §3.6.1 — `MinMajor=1`, `MaxMajor=1`, o que cada MINOR acrescentou, e como se depreca um *reader* (avançar o `MinMajor`, com a sinalização de retenção/legal-hold de AOS-079/093 **antes** de perder admissibilidade).
- [x] **Replay de planos antigos reproduz-se byte-a-byte**: forma canónica e `sha256:` de binding congelados por fixture; o manifesto do replay vem sempre na versão **aprovada**, nunca em `CurrentPlanVersion`.

### Estado
**FECHADO com residual declarado (DEF-276).**

**A decisão do ticket: MINOR, não MAJOR — e porquê isso não é encolher o âmbito.**

O ponto de falha deste ticket era inventar um MAJOR para ter uma migração a exercitar. MAJOR é «campo removido ou semântica alterada»; nenhuma das três extensões de ADR-022 faz nenhuma das duas: `conditional_on` (AOS-270), `outputs` e `consumes` (AOS-272) são campos **novos, opcionais e omitidos quando vazios**, e a reserva do literal `verifier` (AOS-271) é aditiva em **significado** — `role` era e continua a ser texto livre, e um documento antigo que já usasse a palavra decodifica igual, apenas passando a ser lido por um validador que impõe **mais** regras (a direcção segura). Forçar um MAJOR seria quebrar compatibilidade de graça e dar ao `planmigrate` trabalho a fingir.

O que **obriga** ao MINOR é o outro lado, e está escrito em `plan/semver.go`: com `DisallowUnknownFields`, um leitor `1.1.0` recusa um documento que use `outputs`/`consumes`. Sem o bump, dois binários carimbavam a mesma versão e discordavam sobre o schema aceite — o carimbo deixava de identificar o schema, que é exactamente o que ADR-012/§3.6 exige dele.

**As duas direcções, ambas provadas** (`planmigrate/schemaline_test.go`; fixtures em `testdata/schemaline`).

- **(a) Um documento da versão anterior continua admissível e reproduz-se.** As constantes de forma canónica e de hash são **pontos fixos fora do binário** — não se recalculam no teste, que é a diferença entre provar e tautologizar. Um `omitempty` esquecido, um campo com valor-zero visível ou uma reordenação mudavam o hash, partiam o binding `plan.approved`↔reader de **todos** os runs aprovados nessa linha, e o teste avermelha antes disso chegar a um run. O argumento «isto é o que o binário anterior produzia» é **auditável à vista** e não uma promessa: `TestFrozenLinesLeaveNoTraceOfLaterFields` confere que nenhuma chave de uma linha posterior aparece no wire de um documento anterior — e, se aparecesse, a conclusão certa seria a oposta (a extensão não era aditiva no wire e o bump devia ter sido MAJOR).
- **(b) Um MAJOR fora da janela é rejeitado**, na leitura e na escrita, e na escrita **antes** de tocar no REG/RM (duplos envenenados + contadores a zero). Não-vacuidade explícita: o **mesmo** documento carimbado dentro da janela é admitido — a rejeição é do carimbo, não do conteúdo.
- **Estabilidade sem tautologia.** `TestIndependentTwinOfBaselineHashesTheSame` constrói um **gémeo por outro caminho** (structs montadas a partir de variáveis separadas, sem tocar no ficheiro nem no desserializador) e exige-lhe o mesmo hash congelado — e depois usa-o como *reader* admissível da mesma captura. Prova que a forma canónica depende dos **valores** e não do texto do documento.

**Golden-sets (AOS-241) — o que entrou, e pelo caminho governado.**

Cinco casos novos, todos `Hard`: `adr022-conditional` (§2.1, ramo por resultado), `adr022-verifier` (§2.2, ramo de qualidade libertado por um verificador independente), `adr022-payload` (§2.3, contrato de dados declarado), o caso **negativo** `adr022-must-reject-stale-version` (a mesma fixture do caso do payload, byte-a-byte, carimbada na linha anterior à que usa ⇒ `plan_version_below_features`) e o caso **negativo** `adr022-must-reject-self-verdict` (o produtor a emitir o veredicto que liberta o próprio consumidor ⇒ `verdict_not_from_verifier`, exigido pelo sub-código EXACTO). Mutar um golden-set é *gated* contra envenenamento; **acrescentar** é o caminho normal e `TestADR022GoldenSetIsAGovernedAddition` prova-o nos dois sentidos — a adição passa sem `RemovalApproval` (nada foi removido nem esvaziado) e, uma vez dentro, remover *ou* esvaziar um caso novo é recusado com o seu sub-código. Nenhum caso pré-existente foi tocado.

- **As rubricas medem.** Cada rubrica de qualidade tem um **contra-exemplo provado** (`TestADR022RubricsAreNonVacuous`): um documento admissível que a falha. Uma rubrica que nada falha inflaciona o pass-rate e engana a comparação distribucional.
- **O caso do payload não passa por o consumidor ser inofensivo.** `payload-2` põe um consumidor **privilegiado** (pina uma tool que, sem eixos de risco declarados, conta como de efeito — fail-closed) a ler o output de forma **fechada** de um `role: verifier`: é a desclassificação que §2.2 sanciona. Dois contra-factos, que morrem em regras **diferentes**: o mesmo consumidor a ler um output de forma aberta de um não-verificador ⇒ `consumes_taint_authority`; o verificador a publicar forma aberta ⇒ `verifier_produces_work`, uma camada acima — a desclassificação não se obtém alargando o que o verificador publica.

**Fronteira que este ticket NÃO atravessa** (e por isso não a reivindica): os cenários adversariais das extensões já eram de AOS-271/272 (`planadversarial`, vectores 7 e 8); o módulo `packages/platform/eval` não foi tocado; e o *wiring* do ciclo-de-vida do run (AOS-238) continua a ser o eixo de DEF-272/DEF-273 — **e também do gate por *reader***, que não tem composição de produção (ver o AC partido acima).

**O que a remediação adversarial fechou depois do fecho** (registado aqui e não escondido no diff): o bump de MINOR era, à data do fecho, **um carimbo que ninguém impunha** — a justificação escrita em `plan/semver.go` não se realizava, porque um produtor podia continuar a carimbar a linha antiga e usar os campos novos. O piso derivado das *features* (AC acima) é a correcção; o corpus do golden-set passou a estar **pinado como literal fora da árvore** (`adr022PinnedCorpus`), porque a governação de `ValidateGoldenMutation` comparava duas expressões da MESMA árvore e não prendia um editor que apagasse um caso difícil dos dois lados; e as três referências futuras a «trabalho de AOS-273» que sobravam no código (`plan/payload.go`, `plan/payload_test.go`) passaram a remeter para §3.6.1 e `plan.CurrentPlanVersion`, no mesmo sentido da correcção já feita em `tecnica/18` §3.3.3.

**Verificação:** `go test -count=1 ./...` verde nos 16 pacotes do módulo `control-plane/orchestrator`; `gofmt`/`go vet` limpos; `go.mod` intocado; zero dependências novas.

**Residual nomeado — SEM eixo de segurança (DEF-276).** O golden-set do planeador não corre no gate de CI chamado `evalgate.sh` — corre como teste do pacote. Não há eixo de segurança: a cobertura **existe e bloqueia** (um plano de ADR-022 que o validador recusasse avermelha o módulo), o que falta é a *sinalização* no gate com esse nome e a agregação do pass-rate no relatório `AOS_EVAL_REPORT`. Ligar os dois harnesses é a fronteira que AOS-241 declarou ao desenhar `plannerprompt` como biblioteca pura.

---

## AOS-274 — Produtor de SLOs/alertas em runtime

### Contexto
**F8 (média).** `EvaluateAlerts`/`BuildDashboard`/`EvaluateOperationalAlerts` (AOS-085/086) só correm em testes; os runbooks mapeiam alertas que ninguém produz; o export OTLP é só traces. Fonte: análise 08-08.

### Objectivo
Loop avaliador periódico no nó: constrói `WideEvent`s a partir dos spans/WORM, avalia alertas, expõe o resultado.

### Critérios de Aceitação
- [x] Avaliador composto no loop de serviço (molde sweeper); alertas disparam sobre dados reais.
- [x] Resultado exposto (endpoint/span/log estruturado) e ligado ao registo de runbooks (AOS-106).
- [x] Fail-open declarado (a observabilidade nunca derruba o nó); `otel-genai/doc.go` corrigido (o «DIFERIDO» do exporter fechou em AOS-173).

### Estado
**IMPLEMENTADO.** `slo_evaluator.go` corre um laço periódico no loop de serviço, no molde EXACTO
de `approval_sweeper.go`/`retention_sweeper.go` (mesmo `sweepStop`, parado pelo Shutdown com um só
`close`), e é um avaliador **composto**: as DUAS famílias na MESMA passagem e sobre a MESMA janela
— os 4 SLIs da mediação (AOS-085/086) e os 7 canónicos operacionais (AOS-104/105), cada uma com a
sua config, a sua janela sustentada e o seu vocabulário (não fundidas: fundi-las obrigaria a
escolher um dos dois SLOs para os dois SLIs que ambas observam).

**Nenhuma métrica nova e nenhum dado sintético.** A fonte dos SLIs derivados de spans é uma
TORNEIRA (`slo_span_tap.go`): um T no caminho de exportação que entrega ao exportador OTLP real e
guarda uma cópia num anel limitado — o avaliador agrega, byte a byte, o que o colector recebeu, em
vez de uma segunda instrumentação divergente. As duas fontes que NÃO dependem de spans (e são as
de maior severidade) correm sempre: a sonda de prontidão do plano de controlo (a mesma condição do
`/readyz`, acumulada como fracção na janela) e a verificação da hash-chain do WORM. O que não tem
produtor no nó (headroom do scheduler, fidelidade de replay) fica **sem amostras** pela regra
anti-vacuidade de AOS-085 — não dispara nem se declara cumprido.

**Exposição + runbooks:** `GET /metrics` ganha `aos_slo_sli`/`aos_slo_target`/`aos_slo_samples`/
`aos_slo_breached`/`aos_alert_firing`/`aos_alert_streak`, com o **runbook como label** (e
`runbook_orphan="1"` quando não resolve); e cada alerta disparado deixa uma linha de log
estruturado com o título, o doc e o ADR resolvidos em `runbooks.Lookup` (AOS-106). Os `Offenders`
(trace_ids) ficam DE FORA do `/metrics` — a rota é não-autenticada e a filosofia não-enumerável do
nó vale para ela; o drill-down por trace vive no log.

**FAIL-OPEN declarado**, com a fronteira explícita: a AVALIAÇÃO é fail-open (pânico contido por
`recover` — sem ele um pânico na goroutine derrubaria o processo; sem propagação ao caminho de
execução; sondas com prazo próprio; e o laço NÃO pára nem ao detectar adulteração, ao contrário do
de retenção, porque não destrói nada); a CONFIGURAÇÃO é fail-closed como todo o resto
(`AOS_SLO_EVAL_INTERVAL`/`AOS_SLO_WINDOW` ilegíveis ABORTAM o arranque; `0` na cadência desliga
explicitamente e o banner di-lo). Ambas na tabela AOS-203. `otel-genai/doc.go` corrigido: o
«DIFERIDO» passou a declarar o adapter REAL (`otlpexporter.go`, stdlib-only) e a separar o que
continua diferido (o SDK oficial/gRPC).

Testes em `aos274_slo_evaluator_test.go` (13 casos): alerta a disparar sobre spans REAIS emitidos
pelo tracer do nó nas duas famílias, o sentido inverso (abaixo do SLO não dispara, com o SLI
avaliado), a janela sustentada (não dispara antes), os três estados da sonda do WORM (íntegro /
adulterado ⇒ PROC-DR / ilegível ⇒ SEM amostra, nunca falso positivo de DR), anti-vacuidade,
resolução de runbook para TODO o alerta, log estruturado com doc+ADR, pânico contido com o nó a
continuar a aceitar runs, sonda pendurada que não segura o laço, config fail-closed, banner, e o
laço a arrancar no loop de serviço e a parar no Shutdown. Falsificabilidade verificada: retirada a
torneira da composição, os testes de alerta e de `/metrics` FALHAM.

---

## AOS-275 — Promotion controller: endpoint `POST /promote` autenticado

### Contexto
**F7 (média).** O controller é composto sempre (AOS-206) mas **não há endpoint nem CLI** — `Promote` só in-process (deferido para AOS-096, `bootstrap.go:1395`). Com ratificadores definidos, um operador não consegue promover nada. Fonte: análise 08-08.

### Objectivo
Superfície de submissão de ratificação no molde de `/approve` (admissão do plano de controlo + assinatura + frescura + nonce durável).

### Critérios de Aceitação
- [x] `POST /promote` (ou subcomando CLI) autenticado; ratificação registada no WORM com principal.
- [x] Anti-replay (`ratification_replayed`) provado pela rota externa, não só in-process.
- [x] Banner deixa de dizer «deferido» quando a rota existir.

### Estado
**IMPLEMENTADO.** `promotion_api.go` acrescenta `POST /promote` ao mux do nó com a **mesma
admissão do `/approve`** — `admitControl` (token-bucket dedicado do plano de controlo) +
`admitControlMTLS` (DEF-012, quando composto) — e **nenhum esquema novo** de autenticação: a
barreira que decide continua a ser a **assinatura ed25519** do ratificador verificada contra a
pubkey **pinada** em `AOS_RATIFIERS` (non-signing, ADR-016 §1). O handler **não decide nada**:
descodifica o wire, recusa fail-closed o que é estruturalmente inválido *antes* de tocar no gate,
e delega em `PromotionController.Promote` — o **mesmo** `hitl.NewProductionRatificationGate` de
AOS-159/AOS-206, com *freshness* + *nonce-store* **durável** forçados por construção. Nenhuma
`env` nova (a superfície AOS-203 fica intacta) e nenhuma dependência externa nova (ADR-017).

**Anti-replay PELA ROTA (o CA central).** `aos275_promote_route_test.go` submete a ratificação
assinada **só por HTTP** (nunca por `node.Promotion.Promote`): a 1.ª chamada devolve `200` e sela
`reason=ratified` no WORM com `Principal = human:ratifier-route`; a 2.ª, com o **mesmo corpo**
(mesmo *nonce*), devolve `403` e sela `ratification_replayed`. É falsificável: contra o
`NewRatificationGate` cru a 2.ª chamada daria `200`/`ratified`. As provas de fronteira cobrem
ainda: assinatura forjada ⇒ `403` **sem** consumir o *nonce* (a legítima que se segue promove — a
verificação precede o consumo, pelo que ninguém pode *queimar* a ratificação de um humano);
ratificador não-pinado ⇒ `403` e **nada** na cadeia do artefacto; mTLS de controlo ligado sem
certificado ⇒ `403` **antes** do gate (nada selado); corpo malformado ⇒ `400` com o gate intocado;
campo desconhecido ⇒ `400` (wire fechado).

**Banner.** A linha «a submissão de ratificações … fica DEFERIDA» **desapareceu**; no lugar, o
banner nomeia a rota, declara o que ela **não** afrouxa e aponta a ferramenta de assinatura. O
teste `TestAOS275_BannerDeixaDeDizerDeferido` corre o **arranque real** e falha se qualquer linha
do promotion controller voltar a dizer «deferid…».

**Via de acesso do operador (fecha a mesma classe de defeito um nível acima).** Sem ferramenta, o
corpo do `POST /promote` era inderivável à mão (assinatura sobre canónico com separador de domínio;
`request_id` = `RatificationID` = SHA-256 do canónico do artefacto+eval) — a rota nasceria como
outra capacidade-fantasma. `aos-issuer ratify-sign` (contraparte exacta do `approve-sign`, no
mesmo binário externo) imprime o corpo COMPLETO, pronto a `curl`; a seed do ratificador vem de
ficheiro montado e **nunca** é ecoada; o *nonce* é CSPRNG por invocação e não é configurável.
`ratifysign_test.go` prova que o `request_id` emitido é o `RatificationID` do artefacto
reconstruído a partir do próprio JSON de saída, e que a assinatura é **byte-a-byte** a de
`hitl.SignApproval` (ed25519 determinístico) — ou seja, cobre o canónico que o gate verifica.

**Residual nomeado.** O *pipeline* de promoção (staging → eval-gate → canary → produção, AOS-096)
continua a montante e **fora** do nó: a rota ratifica um candidato **apresentado** pelo operador. O
que o nó garante é que nenhum artefacto chega a produção sem ratificação humana assinada, fresca e
de uso-único. Uma submissão não-autenticada sela na partição de quarentena
`ratification-unratified` (ingresso não-autenticado com escrita limitada no WORM) — **mesma**
postura do `/approve`, limitada pelo token-bucket e, quando composto, pelo mTLS; o eixo para a
fechar é o do resto do plano de controlo (DEF-012, PKI de cliente).

---

## AOS-276 — Keypool do GW: fusível RPM de 120 chamadas por vida do processo

### Contexto
**F17 (alta).** `modelgatewaywiring.go:135-137` compõe o gateway com uma conta `LimitRPM: 120, LimitTPM: 200_000` e o contador **nunca reinicia** — `keypool.go:171` incrementa a cada `Select`, `saturated()` (`keypool.go:72-73`) aos 120, e `gateway.go:520-523` propaga `ErrNoCapacity` **fail-closed para sempre**. À 121.ª chamada ao modelo desde o arranque (~8 runs com `MaxTurns=16`), tudo falha até reiniciar — brownout permanente e silencioso, sem métrica de saturação, indistinguível de avaria do provider. O comentário `keypool.go:67` diz «janela corrente» — a janela não existe. Fonte: desafio A5 (F1, verificado à mão).

### Objectivo
Eliminar o fusível: ou o pool ganha uma janela real, ou o tecto é declarado como pertencendo ao gateway externo (D11 — recomendação do desafio: LiteLLM).

### Critérios de Aceitação
- [ ] **Opção A (janela real):** relógio injectável (padrão `WithClock` já existente no GW) que zera os contadores ao cruzar o minuto; teste prova recuperação após a janela.
- [ ] **Opção B (tecto externo):** `LimitRPM/LimitTPM: 0` (o contrato diz `<=0 = ilimitado`) + linha na tabela AOS-203 a declarar que o rate-limit vive no LiteLLM externo.
- [ ] Qualquer das opções: saturação expõe sinal observável (span/métrica) — nunca brownout silencioso; teste de nó com >120 chamadas não morre.
- [x] A decisão A/B fica registada (D11). **DECIDIDO 2026-08-10 (dono): Opção B — tecto externo no LiteLLM.** O rate-limit de RPM/TPM pertence ao gateway externo, não ao nó; o keypool perde o fusível de vida-inteira (`LimitRPM/LimitTPM: 0` = ilimitado no contrato) e a tabela AOS-203 declara que o limite vive no LiteLLM.

### Estado
**IMPLEMENTADO (2026-08-10, Opção B).** `LimitRPM/LimitTPM: 0` em `modelgatewaywiring.go` (keypool = selector por throughput, não rate-limiter; o tecto real vive no LiteLLM externo com janela e backpressure). Trabalho adotado da sessão `determined-greider`, verificado verde e committado com proveniência.

---

## AOS-277 — Knobs de ingresso por env (token-bucket + tecto de in-flight)

### Contexto
O desafio A5 corrigiu o «sem backpressure» do relatório: o ingresso **já tem** token-bucket + tecto de runs em voo com 429 (`api.go:534-546`) — o que falta é o operador poder afiná-los. As três opções existem, sem env.

### Objectivo
Expor os limites de ingresso na superfície AOS-203, com defaults declarados e teste do 429.

### Critérios de Aceitação
- [x] Envs (ex.: `AOS_INGRESS_RATE`, `AOS_INGRESS_BURST`, `AOS_INGRESS_MAX_INFLIGHT`) lidas uma vez no arranque, fail-closed em valor inválido, na tabela AOS-203.
- [x] Teste de API: burst excedido ⇒ 429; in-flight no tecto ⇒ 429; dentro dos limites ⇒ 201/202.
- [x] Banner declara os limites em vigor.

### Estado
**IMPLEMENTADO.** `ingress_env.go` lê as três variáveis **uma só vez** em `serveAPI` (antes de
compor o serviço) e devolve as `APIOption` que já existiam (`WithRateLimit`/`WithMaxInFlight`) —
**nenhum** mecanismo novo de backpressure foi escrito, como a correcção do A5 exige. Fail-closed
com erro nomeado `ErrBadIngressLimits`: ilegível, não-finito, negativo **ou zero** abortam o
arranque nas três, e o zero é recusado *por nome* nas três razões que o tornam uma armadilha
(rate 0 ⇒ balde que nunca reabastece; burst < 1 ⇒ nada é admitido; max-in-flight 0 ⇒ **desliga**
o tecto). Defaults inalterados (64/s, 128, 512) ⇒ nó não-configurado comporta-se exactamente como
antes. Banner emitido em `serveAPI` a partir do **mesmo** valor que alimentou as opções, com o
alcance declarado (cobre `POST /runs` e só; plano de controlo tem balde dedicado; por-processo e
global entre chamadores; run suspenso sai da contagem; `/resume` não consulta o tecto; sem
`Retry-After`). Testes em `aos277_ingress_knobs_test.go` (13 casos de config inválida, 429 por
burst, 429 por in-flight com prova de readmissão, banner pela via de `serveAPI`).

---

## AOS-278 — Estágio de identidade real do GW (substituir o stub allow-all)

### Contexto
**F18 (média).** `production.go:178` exige `Authn` fail-closed mas só guarda contra nil; o nó passa `nodeModelAuthn{}` (`modelgatewaywiring.go:93-103`), que forja o principal e devolve allow incondicional. O estágio real (`pipeline/authn`, valida EdDSA + raiz humana ADR-003) tem zero importadores não-teste. Declarado no código como dívida de AOS-057 — liga-se ao eixo D4. Fonte: desafio A5 (achado E).

### Objectivo
Ligar o estágio `pipeline/authn` real na composição do GW, com o principal do RUN propagado. **CUTOVER DURO** (decisão do dono, 2026-08-12): o GW passa a EXIGIR SEMPRE um token NHI EdDSA real cuja cadeia on-behalf-of enraíza num humano (ADR-003); sem modo env-gated nem fallback silencioso para o stub.

### Resolução
- **Estágio real, sem stub.** `nodeModelAuthn` (forjava `aos-node/aos-agent` e devolvia allow) e a const `nodeReferencePrincipal` foram REMOVIDOS. `newGatewayModelClient` compõe agora `authn.New(verifier, nodeModelAuthority{}, authn.LoadPolicy())` — o estágio `pipeline/authn` REAL (EdDSA + janela + revogação + raiz humana + policy-as-code default-deny `model:invoke`). O `nodeModelAuthority` concede `model:invoke` a qualquer principal VERIFICADO, reconciliado com o escopo SELADO no token (menor privilégio; a autoridade deriva do token, não de um directório — molde AOS-071/AOS-156). Vale em ambos os modos (referência e endurecido).
- **Threading por-run.** O token do run (`Goal.Credential`, o MESMO que as tool calls já verificam) viaja no `ctx` por-run: `service.go` anexa-o ao `runCtx` (`withModelCredential`, a par do plano de replay) e o adaptador do GW SOURCE o principal por-chamada via a nova opção `modelgateway.WithPrincipalFromContext(modelCredentialFromContext)`. Sem credencial no ctx ⇒ `ex.Principal` vazio ⇒ authn nega ATRIBUÍVELMENTE (nenhum principal forjado). Sobrevive à RETOMA (o Resume repovoa `Goal.Credential` com a credencial fresca antes de re-submeter).
- **Cutover fail-closed de composição.** O GW é construído na fronteira de ambiente ANTES de a identidade existir; o seu estágio authn arranca com um verifier LIGADO TARDIAMENTE (`lateBoundModelVerifier`) que NEGA (`ErrModelIdentityNotComposed`). O `Bootstrap` liga-o (`Config.ModelIdentityBinder`) ao MESMO verifier REAL das tool calls (`integration.NewVerifierFromAuthority`), em ambos os modos. Sem esse bind, nenhum turno de modelo passa.

### Critérios de Aceitação
- [x] `nodeModelAuthn{}` substituído pelo estágio real (CUTOVER DURO: estágio real sempre; sem fallback para stub).
- [x] Atribuição correcta: o principal do pedido ao GW é o do run (`Goal.Credential`), não forjado.
- [x] Teste: credencial inválida ⇒ deny atribuível; credencial válida ⇒ pipeline segue (`aos278_model_identity_test.go`, contentor, 6/6 PASS).
- [x] Banner: declara a postura verdadeira — «IDENTIDADE REAL do GW LIGADA (AOS-278, CUTOVER DURO)» na dobra do gateway de `modelPostureBanner`, no lugar de qualquer implicação de stub.

### Estado
**FEITO** (cutover duro; raiz humana real do EPIC-16/D4 já composta pelo verifier do nó).

---

## AOS-456 — Rate-limit do ingresso POR-CHAMADOR (o balde global não separa utilizadores)

### Contexto
O AOS-277 deu ao ingresso um token-bucket e um tecto de in-flight, ambos **por-nó**:
`apiHandler.bucket` é **um** balde consumido por `handleSubmit` para todo o `POST /runs`. Com um
utilizador — o dono — é anti-exaustão correcta, e era o que aquele ticket pedia. Com N é negação de
serviço **entre pares**, sem malícia necessária: quem submeter em rajada esgota o balde de todos, e
o segundo utilizador vê `429` por causa do primeiro. O banner de arranque declara-o em voz alta.

Apurado na auditoria de prontidão para terceiros
(`docs/reports/auditoria-prontidao-terceiros-2026-09-27.md` §3.1).

### ⛔ TENTATIVA 1 (2026-09-27) — IMPLEMENTADA, REVERTIDA. Leia isto antes de tentar outra vez.
Uma implementação foi escrita, passou **31 check runs de CI** e foi **revertida** depois de uma
revisão adversarial independente a medir contra o handler real. **Não entregava nada do que
prometia, em nenhuma configuração.** O que se aprendeu é o valor deste ticket, e custa menos ler
do que redescobrir.

**O DEFEITO DE RAIZ — a ordem das duas etapas.** O desenho era «mantém o balde global como 1.ª
etapa e acrescenta o por-chamador como 2.ª, depois de o `authorize` resolver o principal». O token
global é consumido em `api.go:676`; a 2.ª etapa decidia em `api.go:781`. Logo **cada pedido
recusado pela 2.ª etapa já tinha gasto um token global**: A inunda, os seus pedidos recusados
drenam o comum, e B leva `429` na **1.ª** etapa. Medido no handler real, na configuração que o
banner anunciava como protectora:

```
alice #1,#2 -> passam           alice #3..#10 -> 429 da 2.ª etapa (e gastam o global)
BOB   #1    -> 429 da 1.ª etapa (global vazio)
```

**Nenhum valor de configuração o corrige.** E o cabeçalho do ficheiro implementado afirmava o
desenho correcto — «pedido atribuível ⇒ balde desse principal, **e só dele**» — que o código não
fazia. A prosa descrevia a solução; o código fazia outra coisa.

**A RESTRIÇÃO REAL, que qualquer tentativa nova tem de resolver primeiro.** Para B não ser
afectado por A, os pedidos **atribuíveis** de A não podem consumir um recurso partilhado. Isso
obriga a resolver a identidade **antes** do balde global — e aí abre um vector oposto: o
`authorize` faz verificação criptográfica.

**➜ DESENHADO (2026-09-27):** [`docs/reports/AOS-456-desenho-do-trade-off-de-ordem.md`](../docs/reports/AOS-456-desenho-do-trade-off-de-ordem.md),
com os custos medidos (recusar anónimo: **41 ns**; `ed25519.Verify`: **59,9 µs**, igual para
assinatura válida e inválida; razão **1461x**) e três opções. O desenho **recomenda dividir este
ticket**, e a razão vale ler antes de tocar em código:

- **AOS-456a — justiça em CONCORRÊNCIA** (tecto de runs EM CURSO por principal). Não exige
  reordenação **nenhuma**, porque um pedido recusado **não ocupa lugar** — ao contrário de um token,
  que é gasto. Toda a maquinaria já existe (`len(s.runs)` e `goal.Principal`). É o que dá a
  propriedade que interessa sobre o recurso que interessa.
- **AOS-456b — justiça em TAXA** (orçamento de verificação + balde por-chamador + cache de
  verificação). Exige a reordenação, e tem um residual que **não se elimina** neste ponto do
  sistema: quem queime verificação degrada-a para todos. A mitigação vive no `edge`, não no nó — o
  que pode tornar o 456b desnecessário, e é decisão a tomar antes de escrever código.

O desenho **não decide nada** e não implementa. Fica a escolha ao dono.

**OS OUTROS DOZE ACHADOS, todos medidos** (a evidência completa está no relatório da revisão; aqui
ficam os que restringem o desenho):

| # | Achado |
|---|---|
| ALTO | O banner declarava «PROTEGE» uma postura que medidamente não protege. Verificado no binário real. |
| ALTO | O critério de aceitação central estava `[x]` com um teste que exercitava a **tabela isolada**, nunca o `handleSubmit`. Com os defaults a 2.ª etapa era **inalcançável como recusa**. |
| ALTO | O banner derivava a postura só da **config**; a composição dependia de `readGov != nil`. Sem `AOS_BOARD_REGIONS` anunciava «LIGADA» e «PROTEGE» com a tabela a `nil` — e o ramo «NÃO COMPOSTA» era **inalcançável por env**, porque o único valor que o produzia abortava o arranque. |
| ALTO | `AOS_INGRESS_PER_CALLER_MAX` sem tecto **superior**: `262144` fazia **uma** inserção segurar o mutex global **8,79 s**; `1e8` era aceite sem uma palavra. Fail-closed contra `0`, aberto contra o absurdo. |
| MÉDIO | DoS novo: 3x de latência no caso benigno, 6,76 ms de pior caso para um chamador benigno com um atacante a rodar principais. Antes era um `allow()` sem varredura. |
| MÉDIO | «O(8) amortizado por pedido» era **falso** — busca linear do mínimo por entrada dá O(n) por pedido. Medido: 4x no tecto ⇒ ~3x no custo/pedido. Exige heap ou limiar. |
| MÉDIO | Rotação de principais anulava a etapa na via legada por headers (principal auto-declarado): 30 pedidos rotativos ⇒ **0** recusas; 30 do mesmo ⇒ 28. |
| MÉDIO | `POST /plans` é segunda porta, consome o global e **não** debita o balde do chamador. |
| MÉDIO | `tuned` passou a ser accionado pelas variáveis novas mas o texto de origem só nomeava as antigas: o banner afirmava «AFINADO por» três variáveis que o operador não definira. |
| MÉDIO | A condição do banner era um `OR` de dois eixos: bastava o burst ser 1 abaixo para declarar «PROTEGE» com a taxa 100 000x acima. Os três casos do teste não distinguiam a condição de nenhum mutante de um eixo. |
| MÉDIO | `aos-orq` submete todos os runs-filho com **um** principal (o dele), colapsando todos os humanos num balde; e o `429` novo chegava ao executor como erro genérico, sem retry/backoff. |
| BAIXO | O teste do critério era satisfeito por um contador que **nunca reabastece** (`rate=0` passava-o palavra por palavra). |

**O que sobreviveu e foi confirmado por medição independente:** a tabela nunca cresce acima do
tecto (8000 inserções concorrentes, `-race` limpo); a selecção dos mais cheios está correcta (200
repetições, ordem aleatória de map como vector); não há data race; a config é fail-closed por
baixo; e o lote melhora 9x face a um-a-um. **Nada disso compensa o defeito de raiz.**

### Objectivo
Dar a cada **principal** um tecto próprio de forma que a rajada de um **não** produza `429` noutro,
resolvendo primeiro o trade-off de ordem descrito acima.

### DIVIDIDO (2026-09-28), como o desenho recomendou
O ticket foi partido nos dois eixos, e o eixo da **concorrência** está **FEITO**:

- **AOS-456a — justiça em CONCORRÊNCIA:** tecto de runs EM CURSO por submissor. ✅ **FEITO.**
- **AOS-456b — justiça em TAXA:** balde por-chamador + orçamento de verificação. **ABERTO**, e o
  desenho põe em causa que deva existir no nó (a mitigação vive no `edge`). Não é pré-requisito de
  nada; **não** foi reservado ticket novo — vive aqui até alguém decidir que se faz.

### Critérios de Aceitação — AOS-456a (CONCORRÊNCIA) ✅
- [x] **Medido no `handleSubmit`, não na tabela isolada:** com A em rajada, B é admitido.
      `TestAOS456ARajadaDeUmNaoTiraLugaresAoOutro` submete por HTTP contra um nó com o gate soberano
      composto, com o **modelo bloqueado** (sem isso os runs terminavam entre submissões e o teste
      passaria com o tecto REMOVIDO) e o **balde global largo com o relógio parado** (um `429` que
      viesse do balde tornaria o teste vacuoso). Mutação `M1` (tecto removido) ⇒ **6** testes vermelhos (a primeira versão desta linha dizia 5 — contei mal).
- [x] **Uma recusa não ocupa LUGAR** — e ⚠️ **gasta um token do balde global.** A primeira versão
      deste critério dizia «não consome recurso partilhado» e era **falsa**: o balde é consumido no
      topo do `handleSubmit` e a decisão por-chamador acontece no `submit`, pelo que cada 429
      por-chamador gastou um token comum. O teste que o «provava» só era verde porque punha o balde
      a **4096 com o relógio parado** — pôs o recurso partilhado fora do alcance do sensor, que é o
      movimento exacto que fez a tentativa 1 passar 31 gates. **Apanhado por revisão adversarial
      independente, não por mim.** O teste
      (`TestAOS456AUmaRECUSANaoOcupaLUGAR_MasGASTAUmTOKEN`) passou a medir as DUAS metades com um
      balde apertado, e a afirmar a tabela verdadeira:
      `alice #1 -> 201; #2..#10 -> 429 por-chamador (gastam tokens); bob #1 -> 429 "rate limit"`.
      O que este eixo dá é que A não tira **lugares** a B; **taxa** é o 456b e não está feita.
- [x] **A verificação e a reserva são atómicas** (partilham a secção crítica de `s.mu`).
      `TestAOS456ATectoEAtomico`: 40 pedidos concorrentes, tecto 3, admitidos **exactamente** 3.
      ⚠️ **O sensor é probabilístico e o teste di-lo, com a tabela medida:** apanha
      `runtime.Gosched()` e 50 µs, **não** apanha unlock/relock imediato nem 1 µs. Apanha a janela
      que o defeito REAL teria (a verificação no handler, com `authorize` + `ed25519.Verify` 59,9 µs
      + selagem WORM pelo meio); contra um reordering de nanossegundos a garantia vem da estrutura.
- [x] O banner deriva a postura da **composição real**, e há **quatro** posturas, todas alcançáveis
      e testadas. Duas correcções de revisão adversarial entraram aqui:
      - **ALTO-1 (mentira no banner):** o predicado de composição testava «gate soberano composto»,
        que **não é** «principal verificado». Com o gate composto e a credencial forte AUSENTE
        (`AOS_BOARD_REGIONS` definida, `AOS_SOVEREIGN_OIDC_*` ausentes, fora de produção) o
        principal vem do header `X-Aos-Reader` — que o chamador escreve. **Medido: 60 submissões com
        o header a rodar, 60 admitidas com o tecto a 2**, e o banner dizia «SUBMISSOR VERIFICADO».
        Agravante: o meu próprio teste de aceitação distingue alice de bob **por esse header**, logo
        o critério foi provado na única configuração em que o mecanismo se contorna. Corrigido com
        um predicado próprio (`principalDoRunEVerificavel`, que exige a credencial forte) e uma
        **quarta** postura no banner: `LIGADO sobre principal DEMO-GRADE`, que nomeia o header, diz
        que se contorna e diz como se fecha. O tecto continua composto nessa postura — vale contra
        rajada honesta — mas deixa de ser anunciado como o que não é.
      - **ALTO-2 (sensor ausente no elo que este critério declarava fechado):** duas mutações no
        wiring de `serveAPI` — remover a condição de composição (`N1`) e passar `true` fixo ao banner
        (`N2`) — **sobreviviam à suite inteira**, e a `N2` é literalmente o defeito ALTO da
        tentativa 1. Nenhum teste tocava `serveAPI`. Fechado com
        `TestAOS456AServeAPIComporEAnunciarNoARRANQUEREAL`, que arranca o servidor real em **cinco**
        posturas e mata `N1`, `N2` e `N5`. (Uma versão desta linha citava um `N4` que nunca foi
        definido em sítio nenhum — erro meu, apanhado pela segunda revisão.) A primeira
        versão desse teste ainda deixava a `N1` sobreviver, porque usava o `countingModel` e o run
        terminava entre os dois POSTs — corrigido com o modelo bloqueado.
      - **ALTO-1b (SEGUNDA revisão adversarial): a correcção do ALTO-1 estava incompleta, e pela
        mesma razão.** O predicado passou a exigir `SovereignReadCredential != nil` — a EXISTÊNCIA da
        credencial no nó — mas `NewAPIHandler` compõe a read-governance em DOIS ramos e passa a
        credencial em UM só: `case node.SovereignAuthority != nil` passa-a, `case
        node.SovereignReadRegions != nil` passa `nil`. Um nó com registo board→região e credencial
        forte mas SEM autoridade tinha a credencial **composta e ignorada**, e o banner dizia
        «VERIFICADO» sobre um header — **medido: 60 submissões rotativas, todas admitidas com o tecto
        a 2**. O predicado passou a exigir `SovereignAuthority != nil` também: é a condição do ramo
        que realmente USA a credencial.
        **Agravante, e é o que importa:** o caso (C) do teste que eu escrevi para provar a postura
        VERIFICADO compunha exactamente este estado — o sensor validava o estado errado e não cobria
        o caminho que o `Bootstrap` produz. Corrigido: (C) compõe a autoridade a sério, e um caso (E)
        novo fixa o estado defeituoso como NÃO-verificado. Mutação `N5` (repor o predicado que lê a
        existência em vez do ramo) ⇒ vermelho, e só nesse caso.
        Pelo `Bootstrap` o estado é inalcançável (`readRegions` só é atribuído dentro de
        `if sovAuthority != nil`, bootstrap.go:2457), logo era alcançável in-process — mas um
        predicado não pode depender dessa coincidência para estar certo.
      - **ALTO-B (2.ª revisão): a decisão de compor o tecto na postura DEMO-GRADE não tinha sensor
        nenhum.** Está escrita em quatro sítios — «o tecto CONTINUA composto, porque vale contra rajada
        honesta» — e a mutação que a quebra (compor só com credencial forte) **sobrevivia à suite
        inteira**. Medido na postura DEMO-GRADE, que é a **única que um nó configurado por env fora de
        produção alcança**: sem mutação 2 admitidas / 6 recusadas; com mutação **8 admitidas / 0
        recusadas**, e o banner a dizer «LIGADO … vale contra rajada HONESTA» com nada composto. É o
        defeito ALTO da tentativa 1 outra vez.
        **Causa: 4 dos 5 casos do meu teste passavam `medir = nil` — só liam texto.** A afirmação do
        commit anterior («arranca o servidor real em quatro posturas e **MEDE** o comportamento») era
        falsa em 4 de 5. O caso (B) passou a medir; mutação `N6` ⇒ vermelho.
        E a primeira versão desse sensor **apanhou-se a si mesma**: usava o `countingModel`, os runs
        terminavam entre submissões e a medição passava com o tecto desligado — o mesmo erro que eu já
        tinha corrigido no caso (A).
      - **Achado próprio, anterior:** o predicado escrito à mão omitia o ramo `SovereignAuthority`.
        Não era alcançável pelo `Bootstrap` (`readRegions = readAuthority.Registry()`), era-o
        in-process. Extraído para `noTemGateSoberanoDeLeitura`. Mutação `M8` ⇒ vermelho.
- [x] `AOS_INGRESS_MAX_INFLIGHT_PER_CALLER` tem tecto superior **e** inferior, com o valor recusado
      medido: 12 casos em `TestAOS456AEnvFailClosedNOSDOISSENTIDOS`. O superior tem significado
      próprio — acima do global **nunca morde** e anunciaria uma barreira inerte. Mutações `M6`/`M7`
      ⇒ vermelho. A variável está no README do operador e no `docker-compose.prod.yml` (vazia por
      omissão) — o gate `TestManifestoDeDeployPassaTodaAConfigQueONoLe` apanhou a falta.
- [x] **A latência não degrada, com número — e o número passou a vir do código de produção.** A
      primeira versão (`BenchmarkAOS456ContagemPorChamador`) media uma **cópia** do laço sobre um
      `map` local: nunca chamava `submit`, nunca tomava `s.mu`. **Provado inútil por mutação (achado
      de revisão):** pôr o laço de produção a fazer 20x o trabalho não mexia um nanossegundo.
      Substituído por `BenchmarkAOS456SubmitRecusadoInSitu`, que percorre o caminho real
      (`Lock → varredura → Unlock → recusa`): **75,9 ns** com 1 run, **760 ns** com 64, **6,28 µs**
      com 512, **61,1 µs** com 4096. A mesma mutação N3 agora move o número **18–19x**.
      Os 6,28 µs estão dentro de `s.mu`; a `ed25519.Verify` do mesmo pedido custa 59,9 µs e fora do
      mutex, e o rate-limit por omissão admite 64/s.
      ⚠️ **A premissa `s.runs ≤ 512` é FALSA**, e é resíduo declarado: nem `handleResume` nem o
      `ResumeInterruptedRuns` consultam o tecto global, e o check global do handler é um TOCTOU fora
      do mutex. Acima de 512 a varredura degrada linearmente segurando o mutex (medido pela revisão:
      1,37 ms com 32768 runs). A via que faz `s.runs` crescer exige four-eyes composto e credencial
      fresca por retoma.
      **Porquê varredura e não contador O(1):** um `map[principal]int` teria de ser decrementado nos
      **quatro** sítios onde um run sai de `s.runs`, e uma entrada a mais tranca o chamador para
      sempre — fail-**closed** e silencioso, pior do que 6,65 µs. A contagem derivada de `s.runs`
      não tem estado próprio e não pode dessincronizar-se.
- [x] `POST /plans` — **exclusão declarada, e estrutural:** essa rota **não hospeda runs**
      (`TestAOS417IngressoNaoHospedaORun`), logo não há lugar a ocupar e uma guarda ali nunca
      dispararia. O `plan_ingress.go` já o diz para o tecto global, pela mesma razão. E a porta que
      *conta* — os runs-filho que o `aos-orq` submete por `POST /runs` — **está coberta**: ver
      abaixo.
- [x] **O achado do `aos-orq` está FECHADO, não declarado como resíduo.** O drenador submete todos
      os runs-filho sob o **seu** principal; imputar ao chamador colapsaria os planos de todos os
      humanos num tecto — o defeito deste ticket reaparecido noutra porta. A imputação vai ao
      `RequestedBy`, **derivado pelo nó** do `planrequest.submitted` sob reclamação viva (AOS-439),
      nunca do corpo — o mesmo valor que o `Goal.Subject` já usa como titular (AOS-440).
      `TestAOS456ARunFilhoEImputadoAQuemPediuOPlano`; mutações `M9` (imputar ao drenador) e `M10`
      (imputar sempre ao `RequestedBy`, tirando o tecto a quem não submete planos) ⇒ vermelho.
- [x] O teste do critério **não** passa com um balde que nunca reabastece: não há balde nenhum neste
      eixo. É um tecto de ocupação, e o que o torna não-vacuoso são as mutações acima.

### AOS-456b (TAXA) — ⛔ DECIDIDO: **NÃO SE FAZ NO NÓ** (2026-09-28)

O primeiro critério deste eixo era uma **decisão**, não código: «decidido, antes de escrever código,
se a mitigação pertence ao nó ou ao `edge`». Está decidido, e contra o eixo. A razão é medida.

- [x] **Decidido.** A justiça em taxa por-origem pertence ao `edge` — e **já lá está, em produção**:
      `deploy/server/nginx.conf` declara `limit_req_zone $binary_remote_addr rate=16r/s` com
      `burst=32`, contra os 64/s + burst 128 globais do nó. O comentário ao lado já declara o alcance
      («uma origem sozinha deixa de poder esgotar o orçamento de todas; quatro em simultâneo ainda
      podem — NÃO fecha um flood distribuído»). Não havia nada a construir: havia a verificar se
      estava lá, e estava.

- [x] **O custo de verificação já tem tecto — é o próprio balde, e o 456b removia-o.** Este é o
      achado que fecha o eixo, e inverte a leitura do desenho.

      ⛔ **ERRADO, e corrigido pelo AOS-458.** Esta análise afirmava que **as duas** portas que
      verificam credenciais põem um balde à frente, e concluía que «nenhuma permite forçar verificação
      sem tecto». **Era falso.** A enumeração foi de `ed25519.Verify` (a credencial do *run*); a
      verificação que o nó faz **primeiro e em mais sítios** é o JWS de `readGovernance.authorize`, e
      **sete** rotas de leitura chamavam-no sem tecto de taxa nenhum. MEDIDO: 200 pedidos ⇒ 200
      verificações, zero 429, contra 1 verificação / 199 recusas no `POST /runs`.
      **A refutação estava escrita no banner do próprio nó** (`ingress_env.go`: «NÃO cobre as
      LEITURAS. `GET /runs/{id}` não é limitado por taxa nenhuma») — no ficheiro que este ticket
      editou repetidamente. Apanhado por revisão adversarial independente, não por mim.
      O que segue vale para as duas portas que a análise nomeou, e **não** para o nó:

      | porta | barreira | onde está |
      |---|---|---|
      | `POST /runs` | `h.bucket.allow()` na **primeira** linha; a 1.ª `ed25519.Verify` ~150 linhas depois | no handler |
      | `POST /runs/{id}/resume` | `admitControl` (o `ctrlBucket` **dedicado**) antes de o handler correr | no **registo** da rota (`planos.go`, `planoControlo`) |

      A segunda quase me escapou: a barreira está no **registo** da rota e não no corpo do handler,
      pelo que ler `handleResume` não a mostra — a primeira versão desta análise dava-a como ausente.
      São dois baldes **independentes**, logo o tecto agregado do vector é `128/s`, não `64/s`.
      Medido em `BenchmarkAOS456BCustoDaVerificacao` (neste contentor, e **não** reciclando o número
      do desenho):

      | operação | custo |
      |---|---|
      | `ed25519.Verify`, assinatura **válida** | 53,6 µs |
      | `ed25519.Verify`, assinatura **inválida** | 52,7 µs |
      | recusar no balde, **sem** verificar | 30,2 ns |
      | **rácio** | **1742x** |

      Tecto **destas duas portas**: `128/s × 52,7 µs` ≈ 6,7 ms/s de CPU. ⚠️ **Não era o tecto do
      nó** — era a propriedade de um subconjunto apresentada como propriedade do sistema, que é a
      mesma classe de erro que este ticket cometeu três vezes. O AOS-458 fecha as sete rotas que
      faltavam. O custo é
      idêntico para assinatura válida e inválida porque `ed25519.Verify` é de tempo constante — um
      atacante não precisa de credenciais válidas, qualquer lixo bem-formado serve.

      **O 456b exige inverter esta ordem** (a identidade tem de ser resolvida antes de o balde
      decidir, ou não há a quem atribuir taxa). Isso **remove o limitador**, e o «orçamento de
      verificação» que o desenho propunha existiria para fechar um buraco que a própria mudança
      abriu. O desenho leu o rácio como *o custo de atribuir*; é também *o preço de admissão do
      vector*.

- [x] **Prova negativa, e é um gate.** `TestAOS456BOBaldeCorreANTESDaVerificacaoCriptografica`
      (`packages/cmd/aos/aos456b_ordem_do_balde_test.go`) prende a ordem pela métrica **real**
      (`aos_ingress_credential_denials_total`, em `/metrics`, que só sobe quando uma credencial é
      efectivamente verificada e recusada): com o balde a 1 token e o relógio parado, o 1.º pedido
      com credencial inválida dá **403** e o contador **sobe**; o 2.º dá **429** e o contador **não
      sobe**. Verificado por mutação: mover o `bucket.allow()` para depois do gate soberano — que é
      literalmente o que o 456b pedia — **avermelha** o gate, com a razão na mensagem de falha.

      E um segundo caso, `TestAOS456BAAdmissaoDeCONTROLOPrecedeARotaDeRetoma`, cobre a **outra**
      porta: com o `ctrlBucket` a 1 token, o 1.º `POST /runs/{id}/resume` corre a rota (qualquer erro
      menos 429) e o 2.º leva **429** sem a rota correr. Sensor pelo **código**, e não pelo contador,
      de propósito: não obriga o teste a compor um run realmente suspenso, o que mediria outra coisa.
      Mutação: reclassificar a rota de `planoControlo` para `planoDados` ⇒ **vermelho**.

### O que fica NÃO COBERTO, e é a fronteira honesta deste fecho
O `edge` limita por **IP**, não por **principal**. Consequências, declaradas em vez de resolvidas:

- Dois chamadores atrás do **mesmo NAT** partilham a quota de 16 r/s.
- Um chamador com **muitos IPs** obtém 16 r/s por cada um.
- Um **flood distribuído** não é fechado por nenhuma das camadas (o próprio `nginx.conf` di-lo).

Fechar isto no nó exige a inversão, e a inversão custa o vector acima. **Se um dia a justiça em taxa
por-principal for necessária, o caminho não é o 456b como desenhado:** é uma zona por-principal no
`edge` (derivada do JWT/header quando existe, com fallback por IP), que atribui **sem** o nó ter de
verificar antes de admitir. Isso é trabalho de `deploy/`, não de `packages/`, e abre-se como ticket
próprio quando houver necessidade medida — não por simetria com o 456a.

### Estado (AOS-456b)
**FECHADO — DECIDIDO E NÃO FEITO, com a justificação CORRIGIDA pelo AOS-458.**

O **núcleo** da decisão aguenta-se: inverter a ordem em `POST /runs` removeria um limitador real, e
isso é razão legítima para não fazer o 456b **tal como desenhado**. O que não se aguentou foi a
**justificação publicada**, e falhou do lado que importa: dizia que a inversão *abriria* um vector
que o nó não tinha, quando o nó **já o tinha em sete rotas, na configuração obrigatória de
produção**. Com isso, o dano marginal de inverter numa porta era muito menor do que o texto afirmava.

**E o eixo certo era outro, mais simples:** não «inverter a ordem no `POST /runs`», mas «pôr tecto de
taxa nas superfícies que verificam e não têm» — as leituras —, que **não exige inversão nenhuma**. É
o AOS-458, e está feito. O eixo da **concorrência**
(AOS-456a) está feito e mergeado; o da **taxa por-origem** vive no `edge` e está em produção; o da
**taxa por-principal** fica declarado como não-coberto, com o caminho nomeado caso venha a ser
preciso.

### Disciplina desta entrega, e onde ela falhou
**Catorze mutações injectadas no código real, uma a uma.** As dez primeiras (o mecanismo, a
imputação, a env, o predicado) avermelharam à primeira. Quatro **não**, e nenhuma foi encontrada por
mim:

| | Mutação | Quem apanhou |
|---|---|---|
| `M2` | separar contagem de reserva com janela **nula** | eu, ao mutar |
| `N1` | remover a condição de composição em `serveAPI` | **1.ª revisão adversarial** |
| `N2` | passar `true` fixo ao banner em `serveAPI` | **1.ª revisão adversarial** |
| `N3` | laço de produção 20x mais lento (o benchmark não via) | **1.ª revisão adversarial** |
| `N5` | predicado a ler a EXISTÊNCIA da credencial em vez do ramo que a USA | **2.ª revisão adversarial** |
| `N6` | compor o tecto SÓ com credencial forte (nada composto na postura DEMO-GRADE) | **2.ª revisão adversarial** |
| `N7` | mover o tecto para ANTES das guardas de estado do run | **2.ª revisão adversarial** |
| `N8` | zerar o bloco `alcance` do banner | **2.ª revisão adversarial** |

A `M2` está registada no teste como limite **conhecido** do sensor, com a tabela de sensibilidade
medida, em vez de tapada. As `N1`–`N3` eram **lacunas de sensor sobre critérios que este ticket
declarava fechados** — e a `N2` é literalmente o defeito ALTO da tentativa 1, reintroduzível sem uma
linha vermelha. Estão fechadas.

**A lição, e é contra mim:** a tentativa 1 morreu de prosa correcta sobre código que fazia outra
coisa. Esta entrega repetiu a forma em três sítios — o critério «não consome recurso partilhado»
(falso, e o teste escondia o balde), o banner a dizer «VERIFICADO» sobre um header, e o benchmark a
medir uma cópia do laço. **O mecanismo funcionava; as afirmações sobre ele não.** Nenhum dos três foi
apanhado por mutação minha, porque as minhas mutações atacaram o mecanismo e as afirmações estavam
noutro lado. Mutar o código não basta: há que mutar aquilo de que a afirmação depende, incluindo a
configuração do próprio teste.

### Também fechado pela 2.ª revisão adversarial
- **O piso do `aos-orq` estava ERRADO, e documentação errada é pior do que nenhuma.** Eu escrevi «16
  runs-filho por plano, abaixo de 16 parte planos» — o que sugere que 16 basta. **Não basta:** a
  imputação é ao **humano** que pediu o plano, não ao plano, logo dois planos concorrentes do mesmo
  humano **partilham** o tecto e o piso real é `16 × (planos concorrentes do mesmo humano)`. Medido:
  com o tecto em 16 e um plano de `human:alice` no tecto, o 1.º filho de um **segundo** plano de
  `human:alice` é recusado e um de `human:bob` passa. E com `aos-orq serve` manual não há submissor
  derivado: **todos** os filhos vão ao principal do `aos-orq`. Corrigido no banner, no README e no
  compose.
- **A ordem das guardas em `submit` é *load-bearing* e não tinha sensor.** Mover o tecto para antes das
  guardas duplicado/suspenso/terminado passava a suite inteira, e a consequência é real: com o
  chamador no tecto, uma re-submissão do MESMO `run_id` passaria de «duplicado» (201, idempotente)
  para **429** — e o `nodeClient.Submit` do `aos-orq` **depende** dessa idempotência, porque repete o
  `submeter` por passagem. Regra fixada em teste: **o tecto é a ÚLTIMA guarda**; um pedido cuja
  resposta é determinada pelo ESTADO do run recebe essa resposta, não uma quota. Mutação `N7`.
- **O bloco `alcance` do banner era removível com a suite verde.** O commit afirmava que «nenhuma
  das três frases é opcional» e nada as prendia. Mutação `N8` ⇒ vermelho, nas duas posturas.
- **`PER_CALLER == global` era aceite e é INERTE.** O teste declarava a igualdade «coerente, degenera
  no global». Medido com global=3/per-caller=3: a 4.ª submissão dá 429 **igual com e sem** o tecto
  composto, porque o check global corre no handler **antes** do submit. Passou a exigir-se
  **estritamente menor** — a mesma razão que já recusava «acima».
- **O ramo VERIFICADO do banner ignorava `gateComposto`.** Inalcançável hoje pelos predicados reais,
  mas era a forma do ALTO-1b (confiar numa coincidência de outro sítio). Conjunção explícita + caso.
- **A formulação declarada FALSA sobrevivia em 8 sítios**, incluindo a **mensagem de erro voltada ao
  operador** (`ErrCallerInFlightCeiling`) e a primeira frase da célula do README, que se contradizia
  com o aviso da mesma célula. Corrigidos.
- **Dois erros factuais meus:** citei uma mutação `N4` que nunca defini, e disse «5 testes vermelhos»
  onde são 6.
- **Flake latente:** o arranque do teste de gate usava `time.Sleep(250ms)` em vez do `esperarPorta`
  que o pacote já tem. Trocado.

### Residuais DECLARADOS (não defeitos: fronteiras conhecidas)
- Um run **SUSPENSO** à espera de aval humano sai de `s.runs` e **não** ocupa lugar — igual ao tecto
  global. Um chamador pode acumular suspensos sem bater no tecto. Está no banner e no README.
- A **retoma** (`/resume`) é **isenta**. Contá-la tornaria um run irretomável por quota, e um run
  que não se pode retomar é um run **preso**, não limitado.
- O tecto é **por-réplica**, como todo o resto da admission: N réplicas valem N vezes o tecto.
- Sem o **gate soberano** composto o tecto **não** entra em vigor, e o banner declara-o
  (`CONFIGURADO (n) mas NAO COMPOSTO`): sobre um principal auto-declarado no corpo, um tecto
  contorna-se mudando o valor — pior do que não existir, porque seria anunciado.
- O predicado à mão que este ticket removeu do wiring **sobrevive no banner do kill-switch de
  soberania** (`main.go:417`, AOS-203). Mesma classe, outro ticket — registado, não corrigido aqui.
- **`s.runs` NÃO está limitado por `AOS_INGRESS_MAX_INFLIGHT`:** `handleResume` e o
  `ResumeInterruptedRuns` (arranque e varredura periódica) não o consultam, e o check do handler é um
  TOCTOU fora do mutex. Consequência para este eixo: a varredura O(n) pode degradar acima de 512
  (1,37 ms com 32768 runs, medido). Pré-condição para o provocar: four-eyes/`ResumeRecords` composto
  e credencial fresca por retoma — não é via para criar trabalho novo.
- **PISO PRÁTICO ditado pelo `aos-orq`, e não validável no nó:** ele despacha até **16** runs-filho
  por plano (`dispatchMaxConcurrency`), todos imputados ao mesmo submissor — o que é o acerto deste
  ticket, e faz com que **qualquer valor abaixo de 16 parta planos com *fan-out* de forma
  reprodutível**. Pior: `nodeClient.Submit` trata o 429 como erro genérico (sem `Retry-After`, sem
  backoff, sem ler o corpo que distingue as três recusas) e o erro aborta a passagem de despacho
  inteira. O nó não conhece o orquestrador, logo não pode validar este piso: está declarado no
  banner, no README do operador e no compose. **O retry/backoff no `aos-orq` é trabalho do lado dele
  — ticket próprio, não escopo deste.**
- **Um drenador configurado (`AOS_PLAN_DRAINERS`) pode imputar runs a qualquer humano com reclamação
  viva**, logo um drenador comprometido esgota o tecto de uma vítima. Fronteira aceitável (o drenador
  é confiado por configuração), declarada aqui porque não estava.

### Estado
**AOS-456a FEITO** (2026-09-28), depois de DUAS revisões adversariais independentes. **AOS-456b
FECHADO como DECIDIDO-E-NÃO-FEITO** no mesmo dia — ver a secção própria acima. A primeira
encontrou 3 ALTO e 3 MÉDIO — dois contra afirmações que este ticket declarava provadas. A segunda
encontrou **2 ALTO, 3 MÉDIO e 9 BAIXO**: a correcção do ALTO-1 estava **incompleta pela mesma razão**
(predicado a descrever uma garantia que o código não dava), o teste escrito para a provar compunha o
estado defeituoso, e a decisão de compor o tecto na postura DEMO-GRADE — escrita em quatro sítios —
não tinha sensor nenhum porque 4 dos 5 casos do teste só liam texto.

**O padrão, três revisões seguidas: o mecanismo esteve sempre certo; as afirmações sobre ele não.**
Das oito mutações que sobreviveram à primeira tentativa, **sete foram encontradas pelas revisões e
uma por mim**. Mutar o código de produção não basta — as afirmações falsas vivem na configuração do
teste, no wiring que nenhum teste toca, no texto que nada prende, e em predicados que leem a
existência de uma coisa em vez do sítio onde ela é usada.
As correcções estão acima, cada uma com a mutação que agora a guarda. **AOS-456b ABERTO**, e
possivelmente desnecessário — a decisão precede o código.

---

## AOS-458 — As LEITURAS não tinham tecto de taxa, e verificam JWS

### Contexto
Nasceu de uma revisão adversarial ao fecho do AOS-456b, que mediu o que esse fecho afirmava não
existir. Sete rotas do `planoDados` chamavam `readGovernance.authorize` **sem tecto de taxa nenhum**:

| rota | chamador |
|---|---|
| `GET /runs/{id}` | `api.go` |
| `GET /runs/{id}/trajectory` | `trajectory.go` |
| `GET /runs/{id}/reconstruct` | `sovereign_replay.go` |
| `GET /tools` | `catalogo_de_tools.go` |
| `GET /plans/{id}` | `plan_estado.go` |
| `POST /plans/claim` | `plan_claim.go` |
| `POST /plans/outcome` | `plan_claim.go` |

Em produção (`AOS_MODE=production` **exige** a credencial forte OIDC) esse `authorize` verifica um
**JWS**: RS256 ≈ **42 µs**, ES256 ≈ **91 µs** — mais caro que a `ed25519.Verify` de 52 µs do
`POST /runs`. **Medido:** 200 pedidos ⇒ 200 verificações e zero 429, contra 1 verificação e 199
recusas no `POST /runs`. Um core dá ~24 000 verificações RS256/s; via *edge* (16 r/s por IP), ~1500
IPs saturam um core — ao passo que no `POST /runs` **nenhum número de IPs** ultrapassa os 64/s.

**Porque é que ninguém viu:** só **dois** handlers consumiam um balde (`handleSubmit`,
`handlePlanRequest`), e o invólucro do `planoDados` não aplicava barreira — com a razão escrita no
comentário do plano: pô-la ali «passaria a limitar rotas que hoje não são limitadas». Passava, e era
isso que faltava querer.

### Critérios de Aceitação ✅
- [x] **As sete rotas têm tecto**, medido rota a rota: com o balde a 1 token, o 1.º pedido atravessa
      o invólucro e o 2.º leva **429** sem a rota correr (`TestAOS458AsLeiturasTemTectoDeTaxa`).
      Mutação `M1` (remover o balde do invólucro — o estado anterior) ⇒ **vermelho nas sete**.
- [x] **O tecto PRECEDE a criptografia**, e não só limita a taxa: com credencial forte composta e um
      verificador que **conta** as chamadas, 20 pedidos com o balde a 1 token produzem **1**
      verificação (`TestAOS458OTectoPRECEDEAVerificacao`). Mutação `M2` (consumir o balde **depois**
      do handler) ⇒ vermelho.
      ⚠️ **A credencial forte no teste não é detalhe:** os gates do AOS-456b usavam
      `WithReadSovereignty(regions, worm)` ⇒ `cred == nil` ⇒ via legada por **headers**, sem
      criptografia nenhuma. Eram cegos ao que diziam medir.
- [x] **O balde é SEPARADO do de submissão.** Alargar o da submissão faria um *scrape* de leitura
      negar submissões — uma regressão funcional em troca de menos código. `POST /runs` e
      `POST /plans` atravessam os dois; o seu é mais apertado e morde primeiro, pelo que o
      comportamento deles não muda.
- [x] **Defaults GENEROSOS, e é deliberado:** 256/s com burst 512. O objectivo é que o tecto
      **exista**, não que aperte — a 256/s o vector fica em ~1,1% de um core em RS256, e nenhum
      cliente de leitura razoável o alcança.
- [x] **Superfície do operador:** `AOS_INGRESS_READ_RATE`/`AOS_INGRESS_READ_BURST`, fail-closed como
      as outras (ilegível, não-finito, negativo ou `0` ⇒ aborta), documentadas no README do operador
      e no compose. Os gates `TestAOS203EnvSurfaceIsDocumented` e
      `TestManifestoDeDeployPassaTodaAConfigQueONoLe` apanharam as duas faltas antes de eu as ver.
- [x] **O banner deixou de mentir.** A linha que dizia «NÃO cobre as LEITURAS» era verdadeira e era a
      refutação do AOS-456b; passa a declarar o balde novo e a nomear o resíduo que fica.
- [x] **O fail-open da guarda nil está fechado.** `tokenBucket.allow()` passou a tratar receiver nil
      (dezenas de testes compõem `apiHandler` à mão, e um nil deref no caminho de pedido é pior do
      que a ausência de tecto). O que impede isso de ser fail-open em produção é o construtor compor
      sempre os três baldes, e `TestAOS458OsTresBaldesMORDEMNumHandlerDoConstrutor` mede o **efeito**
      de cada um — não os campos, porque `NewAPIHandler` devolve o mux.

### Residuais DECLARADOS
- ~~**O tecto de streams SSE continua GLOBAL.**~~ ✅ **FECHADO pelo AOS-459** — repartido por leitor,
  com o global a ganhar também a variável de ambiente que nunca teve.
- O tecto é **por-réplica** e **global entre chamadores**, como todo o resto da admission: não é por
  IP nem por principal. A justiça por-origem vive no `edge`.
- **Nada no repositório verifica o `deploy/server/nginx.conf`.** A perna da decisão do 456b que
  depende do `edge` («a justiça por-origem já está em produção») não tem sensor nenhum, e o ficheiro
  chega ao servidor por `scp` manual. O outro `nginx.conf` do repo (`deploy/node/dev-hardened/`)
  **não tem `limit_req`**. Fica registado como resíduo próprio.

### Estado
**FEITO** (2026-09-29).

---

## AOS-459 — O tecto de streams SSE é repartido por LEITOR

### Contexto
Resíduo declarado no AOS-458 e apanhado pela mesma revisão adversarial. `trajConns` limitava 256
streams SSE concorrentes de forma **global**: um leitor autenticado abria os 256 e negava
`GET /runs/{id}/trajectory` a **todos** os outros. E não é uma rajada que passa — é **ocupação que
fica** enquanto ele mantiver as ligações abertas.

**Nenhuma das barreiras existentes o fechava**, e vale a pena ver porquê:

| barreira | porque não cobre |
|---|---|
| `AOS-456a` (concorrência por submissor) | conta **runs** em curso, não **ligações** |
| `AOS-458` (taxa do plano de dados) | limita **taxa**; abrir um stream custa **um** token e a ligação vive minutos |
| *edge* (`nginx.conf`) | tem `limit_req` (taxa) e **não** tem `limit_conn` (ligações vivas) |

**Lacuna adjacente, fechada ao mesmo tempo:** o tecto global existia como constante do binário e
**não era afinável por ambiente**. Validar «o por-leitor é estritamente menor que o global» sem poder
configurar o global deixaria o par inútil.

### Critérios de Aceitação ✅
- [x] **Um leitor não ocupa os lugares dos outros:** alice enche o seu tecto e é recusada; bob passa,
      e tem o **seu** tecto (não passe livre).
- [x] **Medido na rota REAL, com um stream VIVO** (`TestAOS459ARotaREALComUmStreamVIVODevolve429AoSegundo`):
      servidor HTTP, run com residência selada, o 1.º stream aberto e a ler backfill, o 2.º do mesmo
      leitor a levar **429**. Um pedido que já terminou não ocupa lugar, e o teste passaria com a
      repartição desligada — foi a lição da tentativa 1 do AOS-456.
      ⚠️ A primeira versão deste caso dava **404**: o `admitSovereignRead` recusa um run inexistente
      **antes** da admission, pelo que o teste não alcançava o mecanismo. Só com residência selada mede.
- [x] **A contagem volta a zero e a entrada DESAPARECE**
      (`TestAOS459AContagemVOLTAAZeroEAEntradaDesaparece`, três voltas). São dois defeitos distintos:
      um decremento em falta **tranca o leitor para sempre** (fail-**closed** silencioso, pior do que
      não ter tecto), e uma entrada que fique no mapa é uma **fuga sem tecto** num nó de vida longa.
      Este eixo tem **estado próprio**, ao contrário do AOS-456a, que deriva a contagem de `s.runs` e
      por isso não pode dessincronizar-se.
- [x] **A reserva é atómica** (60 pedidos concorrentes, tecto 3 ⇒ exactamente 3 reservas, `-race`).
- [x] **Uma recusa por-leitor DEVOLVE o lugar global.** Se não devolvesse, **N recusas esgotariam o
      tecto do nó** — um DoS que esta correcção introduziria, pior do que o defeito que fecha.
      **Esta mutação SOBREVIVEU à primeira volta**, porque eu perdi a asserção ao reescrever o teste
      da rota. Fechada com `TestAOS459ARecusaPorLeitorDEVOLVEOLugarGlobal`, que mede pela métrica
      **real** (`aos_trajectory_streams_active`, nova): 10 recusas ⇒ ainda **1** activo; com a mutação,
      **11**.
- [x] **`por-leitor >= global` é INERTE e é recusado**, e a razão foi verificada **neste** eixo, não
      copiada do AOS-456a: os dois tectos são verificados no **mesmo** ponto, um após o outro. Com
      ambos a N, um leitor chega a N sem exceder nenhum e na (N+1)-ésima é o **global** que corta — o
      por-leitor nunca dispara. 14 casos em `TestAOS459EnvFailClosedEORRACIOENTREOSDOIS`.
- [x] **As fronteiras estão fixadas em teste, não só em prosa:** principal vazio (modo legado — não há
      a quem imputar, degenera no global), repartição desligada (`<= 0`), e mapa não composto (um
      `apiHandler` construído à mão; um panic no caminho de pedido é pior do que a ausência de tecto —
      o mesmo compromisso da guarda nil de `tokenBucket.allow`).
- [x] **Superfície do operador:** `AOS_TRAJECTORY_MAX_CONNS` (a que faltava) e
      `AOS_TRAJECTORY_MAX_CONNS_PER_READER`, fail-closed, no README e no compose. E a métrica
      `aos_trajectory_streams_active`, sem a qual o operador tinha dois tectos para afinar e nenhuma
      leitura de quantos lugares estão ocupados.

### Residuais DECLARADOS
- ⛔ **ERRADO, e corrigido pelo AOS-460.** Este bullet dizia duas coisas contraditórias no mesmo
  parágrafo, e a segunda era falsa. Há **TRÊS** posturas, não duas:

  | composição | principal | consequência |
  |---|---|---|
  | `readGov == nil` | **vazio** | sem repartição: degenera no global |
  | `readGov != nil`, `cred == nil` | do header `X-Aos-Reader` | reparte, mas **contorna-se rodando o header** |
  | `readGov != nil`, credencial composta | da credencial **verificada** | inforjável |

  A do meio é a de um nó com `AOS_BOARD_REGIONS` e sem OIDC — **e é a que os testes deste ticket
  compõem**. Medido: 12 streams vivos com o tecto a 1, só a rodar o header. Ou seja, os testes do
  AOS-459 **só passam porque o principal não é vazio, e a prosa afirmava que era**. E o banner não
  declarava nenhuma delas, ao contrário do AOS-456a — que tem as três e cujo próprio comentário diz
  que a do meio «é a que a revisão adversarial apanhou».
- O tecto é **por-réplica**: N réplicas valem N vezes os lugares.
- **Um stream ocupa um lugar durante toda a sua vida**, e este eixo não põe limite à *duração*. Um
  leitor dentro do seu tecto pode mantê-lo ocupado indefinidamente; o que deixa de poder é ocupar o
  dos outros.

### Estado
**FEITO** (2026-09-29).

---

## AOS-460 — A repartição corre ANTES do tecto global, e o banner declara as três posturas

### Contexto
Correcção de dois ALTO que uma revisão adversarial encontrou no AOS-459 **depois de mergeado**.

### ALTO-1 — uma rajada de RECUSAS negava a rota aos outros pelo tecto GLOBAL
O AOS-459 pôs a reserva por-leitor **depois** do incremento global, argumentando que «o global é a
barreira do nó e é a mais barata». Falso por duas razões: o passo caro (`admitSovereignRead`, JWS +
residência) corre **antes dos dois**; e um pedido destinado a ser **recusado** tomava primeiro um
lugar global, ocupando-o enquanto estava em voo.

Medido na rota real, `global=2`, `por-leitor=1`, alice presa a **um** stream:

| | bob 200 | bob 429 pelo tecto **global** |
|---|---|---|
| sem rajada | 199/199/198 | 1/1/2 |
| 32 recusas de alice em voo | 142/122/146 | **58/78/54** (27–39%) |

É a assimetria que o **AOS-456a declara e cumpre** — «exceder responde 429 **sem ocupar lugar
nenhum**» — e que o AOS-459 dizia replicar. Nenhum dos seis testes do AOS-459 distinguia as duas
ordens, porque todos mediam **um** pedido de cada vez.
`TestAOS460RecusasEmVOONaoTiramLugaresGlobaisAOutroLeitor` é o sensor que faltava.

> ⚠️ **A tabela acima é incompleta, e a frase que a seguia («Trocada a ordem: 0 de 200») era uma
> afirmação que a medição não sustenta.** Contava só a categoria de 429 que a ordem *move* e descartava
> em silêncio a categoria para onde ela a move. Corrigido e re-medido no **AOS-461**, com as duas
> categorias separadas; ver a tabela de lá. O que esta correcção entrega é que **uma recusa por-leitor
> não consome lugar global** — não que o outro leitor deixe de levar 429.

### ALTO-2 — a fronteira de segurança estava declarada ao contrário
Ver o bullet corrigido na secção do AOS-459: há **três** posturas e o ticket colapsou-as em duas,
descrevendo a do meio (principal do header, contornável) como se fosse a primeira (principal vazio).
O banner ganhou as três, no molde do AOS-456a, mais um ramo de aviso quando o par fica **inerte**
(`por-leitor >= global`, que a env recusa mas a composição in-process não).

> ⚠️ **O aviso «inerte» NÃO é uma protecção do operador**, e este ticket contou-o como tal. Todos os
> estados que o disparam abortam o arranque em `ingressLimitsFromEnv`, e o banner tem um só chamador de
> produção alimentado por essa leitura — logo nenhuma configuração por ambiente o alcança. A barreira
> que morde é o abort. Corrigido no **AOS-461**; o ramo fica como cinto-e-suspensórios para quem compõe
> in-process.

### E dois testes que não mediam o que prometiam
- **A libertação DUPLA** passava incólume pelos seis testes do AOS-459 — e também pela primeira versão
  do caso novo, que exercitava a **função** quando a duplicação vive no **handler**.
  `TestAOS460ALibertacaoDUPLANoHandlerDaStreamsAMais` mede pela rota: fechado 1 de 2 streams com o
  tecto em 2, admitidos **2** em vez de 1.
- **A atomicidade.** O teste do AOS-459 era um detector de ~4%. A barreira de arranque melhora-o, e
  **não chega** — medido:

  | janela entre verificar e incrementar | detecta |
  |---|---|
  | nenhuma (`Unlock`/`Lock` adjacentes) | **1 de 10** |
  | `runtime.Gosched()` | 10 de 10 |
  | `time.Sleep(1µs)` | 9 de 10 |

  Registado no teste: **não prova atomicidade**; prova a ausência de uma janela da ordem que um
  defeito real teria. A garantia vem da estrutura. É o mesmo limite, medido da mesma forma, que o
  AOS-456a registou para a sua própria asserção.

### Também corrigido
- **Dois godoc SEQUESTRADOS** pelo AOS-459: `WithMaxTrajectoryConns` ficou **sem documentação
  nenhuma** e o doc de `handleTrajectory` passou a descrever `reservarStreamDoLeitor`. Terceira vez na
  mesma sessão; nenhum gate apanha (não são símbolos exportados).
- O banner afirmava «AFINADO por» quatro variáveis que podiam não estar definidas, e continuava a
  dizer que **«as leituras não têm limite de taxa nenhum»** — falso desde o AOS-458, cujo número o
  próprio banner exibe.
- `TestAOS456ABannerDistingueAsTRESPosturas` procurava no banner **inteiro**, e o vocabulário passou a
  ser partilhado por dois eixos. Passou a procurar na sua própria dobra — o que também o **fortalece**.

> ⚠️ **E enfraqueceu quatro asserções do OUTRO sensor no mesmo movimento.** A dobra nova usa o mesmo
> vocabulário, e `TestAOS456AServeAPIComporEAnunciarNoARRANQUEREAL` — que asserta sobre o banner
> inteiro — passou a ser satisfeito por ela. Medido: detecção da mutação que neutraliza a dobra AOS-456
> passou de **5/5 para 1/5**. É regressão introduzida por este ticket, fechada no **AOS-461**.

### Residual NÃO fechado, e é enumeração parcial outra vez
**`tectoDePendentes = 1000`** (`plan_claim.go`) é um **segundo** tecto de ocupação global, sem
repartição por chamador, sem variável de ambiente, no mesmo plano de dados. Um submissor autenticado
enche os 1000 e todos os outros levam **503** em `POST /plans`. O AOS-459 afirmou que `trajConns` era
o único; não era. **Fechado em AOS-464.**

### Estado
**FEITO** (2026-09-29).

---

## AOS-461 — Os residuais da sétima revisão: um sensor cego, um sensor desarmado e uma barreira que não morde

### Contexto
Uma sétima revisão adversarial correu sobre o AOS-460 **já mergeado**, com o mandato de testar a
hipótese de que a correcção tinha introduzido o seu simétrico. Não tinha — mas encontrou **dois ALTO**,
um deles regressão do próprio AOS-460. Todos os achados abaixo foram **reproduzidos de forma
independente** antes de serem aceites; um deles teve o enquadramento da revisão **refutado** pela
medição (ver ALTO-1).

### ALTO-1 — a afirmação «0 de 200» não era a medida, e o sensor filtrava o que sobra
`TestAOS460RecusasEmVOO…` contava o 429 do tecto **global** e descartava com
`!strings.Contains(corpo, "deste leitor")` a outra categoria — exactamente a categoria para onde o dano
migra. O comentário que o justificava («bob nunca devia vê-lo») é **falso e mensurável**.

Re-medido, mesma rota, mesmo cenário (`global=2 / por-leitor=1`, alice presa a um stream, 32 recusas
dela em voo, bob sequencial), **cinco corridas por ordem, as duas categorias separadas**:

| ordem | 429 pelo tecto **GLOBAL** | 429 pelo tecto **DE BOB** | total negado a bob |
|---|---|---|---|
| AOS-459 (antiga) | 34–52 | 0 | 34–52 (17–26%) |
| AOS-460 (esta) | **0** | 29–42 | 29–42 (15–21%) |

**Onde a revisão acertou:** o total negado a bob move-se pouco, e a afirmação «0 de 200» convida a ler
«bob deixa de ser negado», que é falso.

**Onde a revisão errou, e a medição refuta-a:** ela apresenta o resíduo como se a rajada de alice
continuasse a negar a bob 15–20% «da mesma ordem de grandeza» do defeito fechado. Não é o mesmo
fenómeno, e não é propriedade do mecanismo. Medido, mesma rajada, proporção sã (`por-leitor ≤
global/2`):

| composição | bob 200 | 429-global | 429-por-leitor | corridas |
|---|---|---|---|---|
| `global=16 / por-leitor=8` | 200/200 | **0** | **0** | 3 |
| `global=64 / por-leitor=32` (o **default** é 32) | 200/200 | **0** | **0** | 3 |

O resíduo é **artefacto do tecto por-leitor a 1**: sob contenção, o pedido N+1 de bob chega antes de o
lugar do N ser libertado, e bob colide com o **seu próprio** tecto. É recusa por razão **certa**,
confinada ao próprio principal. A que desapareceu era recusa por razão **errada** — o nó cheio de
pedidos destinados a serem recusados. Aplanar as duas numa coluna de «total 429» apaga a distinção que
é o objecto do ticket.

**E um resultado que não estava em nenhum dos dois lados:** nessa proporção sã as **duas ordens
empatam** (ambas 0). A ordem só é observável quando a folga global é de **um** lugar. Isso não torna a
correcção dispensável — torna-a a diferença entre um nó apertado que degrada com razão e um que degrada
sem razão —, mas é o alcance real e não estava declarado.

- [x] O sensor conta e **registra** as duas categorias; só a global avermelha, e o `t.Logf` publica o
  resíduo em toda a corrida. O critério (global ≤ 5%) fica igual: continua a detectar a ordem antiga.
- [x] A afirmação corrigida nos **quatro** sítios onde viajava: cabeçalho de
  `aos460_ordem_da_admissao_test.go`, prosa de `handleTrajectory`, banner de produção
  (`ingress_env.go`, postura VERIFICADO) e a secção do AOS-460 acima.

### ALTO-2 — a dobra nova desarmou o sensor de arranque real do AOS-456a (regressão)
`TestAOS456AServeAPIComporEAnunciarNoARRANQUEREAL` asserta `strings.Contains` sobre o banner
**inteiro**. A dobra SSE que o AOS-460 acrescentou usa o mesmo vocabulário (`NAO COMPOSTO`,
`DEMO-GRADE`, `LIGADO sobre principal VERIFICADO`), pelo que quatro dos cinco casos passaram a ser
satisfeitos por ela.

Medido nas duas árvores, com a dobra AOS-456 neutralizada (quatro posturas trocadas por tokens):

| árvore | subtestes que **detectam** |
|---|---|
| `3ff4611` (AOS-460, antes desta correcção) | **1 de 5** |
| esta | **5 de 5** |

- [x] O helper devolve **a dobra do eixo**, não a saída do arranque (`arrancarMedirELerDobra456`), e
  aborta se a dobra não existir — o que os `Contains` de tipo «NÃO pode declarar X» não viam: passavam
  num banner vazio.
- [x] `dobraDoEixo` deixa de receber um `fim` à mão e delimita-se por um **registo** de marcadores
  (`marcadoresDeDobra`): acrescentar uma dobra passa a fechar automaticamente a anterior. O AOS-460
  delimitava a sua com o sentinela `"\x00"`, o que a fazia ir até ao fim do texto e reporia a confusão
  na dobra seguinte (BAIXO-3).
- [x] `TestAOS461TodasAsDobrasDoBannerEstaoREGISTADAS` vigia o registo nos dois sentidos (dobra não
  registada, marcador obsoleto). **Limite declarado:** só apanha dobras que sigam a convenção
  «TECTO … (AOS-NNN):», que é a das duas que existem.

### MÉDIO-1 — ramo sem guarda: reintrodução da forma do ALTO-1b, no ficheiro que a proíbe
`case lim.trajMaxConnsPerReader > 0 && principalVerificavel:` — **sem `gateComposto`**, 25 linhas
abaixo do `switch` do AOS-456 que a tem, com o comentário «a conjunção é explícita … era exactamente a
forma do ALTO-1b». Nesse estado o banner anuncia «LIGADO sobre principal VERIFICADO … credencial FORTE
verificada» num nó onde `admitSovereignRead` devolve principal **vazio** e **não há repartição
nenhuma**.

- [x] `&& gateComposto` acrescentado, com a razão nomeada no código.
- [x] O caso que faltava na tabela (`verificavel SEM gate: nada esta composto`) — o gémeo do que a
  tabela do AOS-456 tem. Medido: é o **único** dos cinco casos que detecta a remoção da guarda.

### MÉDIO-2 — ⚠️ **ESTA CORRECÇÃO ESTAVA ERRADA. Ver AOS-463.**
O AOS-461 afirmou que `ingressLimitsFromEnv` aborta em todos os estados que disparam o aviso «par
INERTE», logo o texto nunca chegaria a um operador, e **rebaixou** o aviso de protecção do operador a
cinto-e-suspensórios.

**Era falso, e a falsidade tapava um fail-open.** A comparação `por-leitor < global` vivia **dentro** do
ramo `if rawTrajPer != ""`: baixar só `AOS_TRAJECTORY_MAX_CONNS` deixava o por-leitor no default 32 e
ninguém comparava nada. `AOS_TRAJECTORY_MAX_CONNS=4` **arrancava** com a repartição INERTE, e este aviso
era a única coisa que o dizia ao operador — precisamente enquanto o AOS-461 o declarava inalcançável.
Corrigido no **AOS-463**, que valida o **par final**.

### MÉDIO-3 / BAIXO-1 — o rollback do tecto GLOBAL não tinha sensor nenhum
A hipótese do defeito simétrico **não se confirma**: o `defer libertar()` é registado antes do bloco
global e cobre o seu `return`; e um pedido destinado a recusa global toma um lugar por-leitor que fica
**confinado ao próprio principal** (medido pela revisão: 0/200 para um leitor terceiro, três corridas).

Mas nenhum dos dois sentidos estava vigiado, e a lacuna é **anterior** ao AOS-460: a mutação que remove
`h.trajConns.Add(-1)` do ramo de recusa global deixava a suite **inteira** do pacote verde, com uma
fuga **permanente** — cada recusa retém um lugar e o tecto do nó esgota-se sem uma única ligação viva.

- [x] `TestAOS461RecusaGLOBALDEVOLVEOLugarGlobal` mede os dois sentidos num só cenário
  (`global=3 / por-leitor=2`, o global cheio de leitores dentro da sua quota, 10 recusas globais de um
  terceiro): a métrica `aos_trajectory_streams_active` tem de ficar em 3, e o leitor recusado tem de ser
  **admitido** quando um lugar liberta. Medido, detecção **5/5** para cada mutação:

  | mutação | detecta |
  |---|---|
  | remover `h.trajConns.Add(-1)` do ramo de recusa global | **5 de 5** |
  | `defer libertar()` deixa de cobrir o ramo de recusa global | **5 de 5** |

- [x] O godoc de `TestAOS459ARecusaPorLeitorDEVOLVEOLugarGlobal` passa a dizer que **passa por
  construção** desde o AOS-460, e porque se mantém (fixa a ordem pelo lado do efeito).
- [x] O comentário de `aos_trajectory_streams_active` deixa de nomear um invariante sem sujeito.

### BAIXO-5 — «12 streams vivos com o tecto a 1» viajava no banner de produção sem sensor
A afirmação estava no commit, no ticket, no comentário do código **e no banner** que um operador lê. O
eixo AOS-456 tem a gémea («60 submissões rotativas») fixada no arranque real; este não tinha nada.

- [x] `TestAOS461ODEMOGRADEContornaSeRodandoOHeader` mede-a na rota: 12 principais distintos, 12
  streams vivos com o tecto a 1 — **e o controlo** que a torna um sensor: o **mesmo** principal leva
  429 ao segundo. Sem o controlo, um tecto simplesmente desligado passaria.

### Residuais NÃO fechados
- **Godoc sequestrados, a classe.** `WithControlRateLimit` ficou **sem documentação nenhuma** —
  sequestrado pelo `WithReadRateLimit` do AOS-458, o commit imediatamente anterior, no mesmo ficheiro.
  Reposto aqui (**quarta** ocorrência na sessão). Uma varredura AST encontra ~8 hijacks reais noutros
  6 ficheiros do pacote; um sensor mecânico para a classe é **AOS-462**, por abrir — corrigir a
  instância e não a classe garante uma quinta vez.
- **`tectoDePendentes = 1000`** (`plan_claim.go`) continua sem repartição por chamador nem variável de
  ambiente. Herdado do AOS-460; **fechado em AOS-464**.
- **Enumeração de tectos.** `maxInFlight=512` tem repartição **não composta por omissão**, e o
  `http.Server` de produção não tem `LimitListener` nem `MaxHeaderBytes` — **não há tecto de ligações
  aceites**, que é o tecto por baixo de todos os outros. Por abrir.

### Estado
**FEITO** (2026-09-29).

---

## AOS-462 — Godoc SEQUESTRADO: um sensor para a classe, não para a instância

### Contexto
Em Go, um bloco de comentário imediatamente antes de uma declaração (sem linha em branco) **é** o godoc
dessa declaração. Inserir uma declaração entre um comentário e o símbolo que ele documenta transfere o
doc para o símbolo errado e deixa o original **sem documentação nenhuma** — e `gofmt`, `go vet` e
`staticcheck` não dizem nada, porque nada está sintacticamente errado (o `ST1020` só cobre
identificadores exportados).

Aconteceu **cinco vezes na mesma sessão**, sempre igual e sempre corrigido só na instância:
`WithCompletedRetention` e `newReadGovernance` (AOS-456a), `WithMaxTrajectoryConns` e o doc de
`handleTrajectory` (AOS-459), `WithControlRateLimit` (AOS-458, reposto no AOS-461) e — **no próprio
commit do AOS-463, que declarou que corrigir a instância e não a classe garantia uma quinta vez** —
`ingressPostureBanner`, o maior documento de contrato do `ingress_env.go`, que passou a ser o godoc de um
helper de quatro linhas enquanto a função ficava com zero.

**A justificação para adiar este ticket foi refutada pelo commit que a escreveu.** Foi isso, e não um
pedido novo, que o trouxe para dentro do AOS-463.

### Critérios de aceitação
- [x] Sensor mecânico: `TestAOS462NenhumGodocSequestrado` varre a AST do pacote e falha quando a
  primeira palavra de um godoc é **um identificador declarado no pacote** diferente do símbolo que
  documenta.
- [x] **O discriminante é «existe como símbolo declarado»**, e é ele que separa o hijack real da prosa.
  Medido: a varredura ingénua (primeira palavra ≠ nome do símbolo, com heurística de maiúscula) dá 15
  achados, ~7 falsos — docs de grupo antes de blocos `var`/`const` («`// Erros …`»), asserções de
  interface («`// Assegura …`») e prosa PT-PT («`// O …`», «`// PRODUTOR …`»). Com o discriminante:
  **11 achados, zero falsos positivos.**
- [x] **O identificador branco está fora, e é correcção do sensor, não do código.** Uma asserção de
  interface (`var _ Porta = (*tipo)(nil)`) declara o símbolo `_`, que não é documentável, e um comentário
  que nomeia o tipo afirmado está correcto ali (`dsar.go` tem um). Um sensor que manda corrigir código
  correcto ensina a ignorá-lo, o que é pior do que não vigiar.
- [x] Os **11 hijacks pré-existentes corrigidos**, não baselinados, movendo o **comentário** e nunca o
  código — a operação de menor risco: `broker_vault_env.go`, `main.go` ×3, `service.go`,
  `posture_banner.go` ×3, `sovereignty.go` ×2, mais o `ingress_env.go` do AOS-463.
- [x] Provado por mutação: inserir uma função entre um comentário e o seu símbolo avermelha **3/3**
  corridas, nas duas formas testadas (`ingressPostureBanner` e `budgetPostureBanner`).
- [x] Vive como **teste Go no pacote** (corre em `ci-test`), e não como gate novo: o `AGENTS.md` §6 avisa
  que `scripts/ci/` é território de sessões concorrentes, e o alcance necessário é um pacote.

### O que o sequestro estava a mascarar
Corrigidos os 11, quatro símbolos ficaram **sem doc nenhum** — que é o estado verdadeiro: nunca tiveram,
apenas carregavam o de outro. Escreveram-se docs para `taintGatePostureBanner`, `revogacaoNoBanner`,
`autorizarComCausa` e `imputadoA`, **só com o que se verifica por leitura do corpo**; inventar
descrições é o modo de falha que esta sessão passou o dia a pagar.

### Limite declarado
Não apanha um doc cuja primeira palavra nomeie um símbolo de **outro** pacote, nem um que descreva o
símbolo errado sem o nomear. Apanha a forma que custou cinco ocorrências, e só o pacote
`packages/cmd/aos` — alargá-lo aos outros 48 módulos é trabalho que ninguém mediu ainda.

### Estado
**FEITO** (2026-09-29).

---

## AOS-463 — O par inerte era alcançável com UMA variável, e duas correcções seguidas juraram que não

### Contexto
`ingressLimitsFromEnv` validava `AOS_TRAJECTORY_MAX_CONNS_PER_READER < AOS_TRAJECTORY_MAX_CONNS`
**dentro** do ramo `if rawTrajPer != ""`. Só um par **explícito** era validado. Baixar apenas o global —
a coisa mais natural de fazer num nó pequeno — deixava o por-leitor no **default 32** e ninguém
comparava nada.

Medido (determinista, `ingressLimitsFromEnv` directamente, 3 corridas idênticas):

| `AOS_TRAJECTORY_MAX_CONNS` | `..._PER_READER` | resultado | repartição |
|---|---|---|---|
| `4` | ausente | **ARRANCA** `global=4 por-leitor=32` | **INERTE** |
| `32` | ausente | **ARRANCA** `global=32 por-leitor=32` | **INERTE** |
| `33` | ausente | arranca `global=33 por-leitor=32` | activa |

Com a repartição inerte, um leitor ocupa os quatro lugares globais e nega
`GET /runs/{id}/trajectory` a todos os outros — **o DoS que o AOS-459 existe para fechar**, alcançável
com uma variável de ambiente.

### Porque nenhum dos dois tickets anteriores deu por isso
- A tabela de 14 casos de `TestAOS459EnvFailClosedEORRACIOENTREOSDOIS` **não cobria a combinação**:
  todos os casos com `global` explícito punham também o `porLeitor` explícito. A asserção final do teste
  (`por-leitor >= global ⇒ Fatal`) estava certa e nunca era alcançada por um par com um default.
- O **AOS-461 afirmou o contrário do facto** — «nenhuma configuração por ambiente alcança o par
  inerte» — em três sítios novos (banner, godoc do teste, ticket) e **rebaixou** o aviso do banner de
  protecção do operador a cinto-e-suspensórios. Esse aviso era a única coisa que nomeava o estado.
- O `deploy/node/README.md` promete ao operador «`>=` global ⇒ **ABORTA**», sem dizer que a promessa só
  valia quando ele definisse a variável.

### Critérios de aceitação
- [x] A comparação sai de dentro do ramo e valida o **par final**, depois de lidas as duas variáveis.
- [x] O erro **nomeia a origem de cada valor** (`definida` / `default do binario`): sem isso o operador
  que definiu uma variável lê uma recusa sobre um número que não escreveu.
- [x] Três casos novos na tabela (`{"4",""}`, `{"32",""}`, `{"33",""}`). Medido: devolvendo a validação
  para dentro do ramo, os **dois** primeiros avermelham e mais nenhum — não são vácuos.
- [x] O comentário do ramo INERTE do banner passa a contar a história correcta: o AOS-460 chamou-lhe
  correcção entregue, o AOS-461 chamou-lhe ramo morto, **as duas estavam erradas**, e hoje é o abort que
  morde *por causa desta correcção*.
- [x] O README do operador deixa de prometer um abort incondicional.

### E os residuais da oitava revisão, no mesmo commit
- [x] **O comentário de `aos_trajectory_streams_active` contradizia o próprio commit que o escreveu:**
  dizia que o rollback do tecto global «não tem teste nenhum» e apontava para o teste tautológico,
  quando o AOS-461 acabara de acrescentar `TestAOS461RecusaGLOBALDEVOLVEOLugarGlobal`. Um leitor futuro
  concluiria que podia removê-lo.
- [x] **Referência godoc pendurada** introduzida pelo AOS-461: `[TestAOS460OBannerDeclaraAsTRESPosturasDoSSE]`
  não existe. O `ref-lint` não vê referências godoc em Go — fica como limite conhecido do gate.
- [x] **TRÊS gamas contraditórias para a mesma grandeza**, no ficheiro que o AOS-461 editou (`34–52` no
  cabeçalho, `44–78` no comentário do critério, `54-78` na mensagem de falha). Duas séries de cinco
  corridas no mesmo contentor sob carga diferente deram `34–52/29–42` e `51–75/51–59` — ~10 pontos
  percentuais de deslocamento. **As gamas saíram do banner de produção** (AOS-461 pô-las lá) e ficam no
  cabeçalho do teste, com a condição declarada. O que reproduz 5/5 nas duas séries e nas duas ordens é a
  **forma**: qual das colunas vai a zero. É isso que o critério mede.
- [x] **Prosa obsoleta no CORPO do teste cujo godoc o AOS-461 corrigiu** (`aos459_…:421` e `:433`),
  80 linhas abaixo da correcção.
- [x] **O guarda do registo de dobras varria UMA postura** e o godoc dizia que varria todas. Basta para
  as duas dobras de hoje e é falso para uma dobra **condicional** — a forma do ramo INERTE que já
  existe. Medido: uma dobra com nome conforme emitida só quando `!gateComposto` escapava. Passa a varrer
  12 combinações (3 formas de `lim` × 4 posturas); verificado que apanha o caso que escapava. Limite que
  fica, declarado: uma dobra com nome fora da convenção continua invisível.
- [x] **O sensor do rollback acertava pelo sítio errado.** Com a quota do leitor a 2 e dez recusas, a
  fuga por-leitor disparava a **guarda de cenário** («o cenário deixou de medir o que diz medir»), que
  convida a corrigir o *teste*, e a asserção que o godoc diz medir era inalcançável. Com a quota a 10 e
  dez recusas, cada mutação passa a disparar na sua própria asserção — medido 5/5 cada, com o
  diagnóstico certo.
- [x] `abrirTrajComo` passa o `context.Context` em primeiro, pela convenção Go.

### QUEBRA DE COMPATIBILIDADE OPERACIONAL, declarada
Esta correcção transforma um arranque silencioso numa **recusa de arranque**. Um nó com
`AOS_TRAJECTORY_MAX_CONNS <= 32` e **sem** `AOS_TRAJECTORY_MAX_CONNS_PER_READER` arrancava antes (com a
repartição desligada em silêncio) e **deixa de arrancar** depois. É deliberado — é a postura fail-closed
que o `AGENTS.md` §7.8 exige, e trocar um buraco silencioso por uma recusa audível é o objectivo do
ticket — mas é uma quebra e tem de estar dita onde o operador olha.

- [x] Nada no repositório é afectado: o único sítio que define as variáveis
  (`deploy/server/docker-compose.prod.yml`) define **as duas**, com os defaults 256/32 (par válido).
- [x] O erro nomeia os dois valores e a origem de cada um, para que a correcção seja óbvia sem ler
  código.
- [x] A nota de migração está na linha do **`AOS_TRAJECTORY_MAX_CONNS`** no `deploy/node/README.md`, e
  não só na do por-leitor: quem baixa uma variável lê a linha dela. A primeira versão deste ticket pôs a
  nota só na linha do por-leitor — colocação parcial, a sexta desta sessão.

### E a NONA revisão adversarial, sobre este mesmo commit
Mandato estreito (só `c91e3b9..2e24838`, focado na validação de arranque). **Sem ALTO** — o mecanismo
está certo e medido: o fail-open fecha, o abort morde no binário real, nenhuma configuração do
repositório passa a abortar, e os três casos novos não são vácuos. Todos os achados foram afirmações
falsas ou parciais em torno de um mecanismo correcto, que é o padrão desta sessão à nona vez.

- [x] **`ingressPostureBanner` perdeu o godoc INTEIRO para `origemDoLimite`** — o helper foi inserido
  entre o bloco de comentário e a função, sem linha em branco, e o maior documento de contrato do
  ficheiro (alvo de `[ingressPostureBanner]` em três outros ficheiros) passou a documentar quatro linhas.
  **Quinta ocorrência da classe, no commit que declarou que a quinta viria.** Reposto, e a classe fechada
  em **AOS-462**, que este commit tinha adiado.
- [x] **A história do ramo INERTE era a TERCEIRA versão errada.** A nota dizia «o ramo fica para a
  composição in-process, que não passa por essa validação». Falso, medido: a função tem **um** chamador
  de produção alimentado pelo `ingressLim` do ambiente, o `ingressLimits` é não-exportado, e
  `WithMaxTrajectoryConns*` configuram o `apiHandler.cfg` — a composição in-process **não chega ao banner
  de todo**. Logo o ramo é hoje inalcançável pelos dois caminhos, e a conclusão do AOS-461 voltou a ser
  verdadeira **por causa deste commit**, não por já o ser. O ramo fica porque está fixado em teste.
- [x] **«VARRE TODAS AS COMBINAÇÕES DE POSTURA» era falso:** a varredura variava 2 de 8 campos de `lim` e
  deixava `tuned` a `false` — um campo em que a função **já ramifica**. Medido: uma dobra condicionada a
  `lim.tuned`, com nome conforme, escapava 5/5. Passa a varrer 24 combinações (3 formas × `tuned` × gate ×
  verif); verificado que apanha o caso, 5/5. Sexta enumeração parcial da sessão.
- [x] **Dos três sítios que o próprio ticket nomeia, corrigiu um.** O godoc de
  `TestAOS460OBannerDECLARAUmParINERTE` e a secção do AOS-460 continuavam a dizer «não é alcançável» e
  «catorze casos» — deixando o repositório com **duas declarações contraditórias** sobre o mesmo ramo,
  em que a marcada como errada era a exacta. Corrigidos, e a contagem passa a **17**.
- [x] **O commit introduziu a contradição de gamas que se propôs a remover:** `trajectory.go` ficou com
  a tabela **pré-alargamento** (`34–52 / 29–42`) enquanto o cabeçalho do teste levou `34–75 / 29–59` e a
  nota de dependência da carga. Em `c91e3b9` os dois concordavam; o sítio em **produção** ficou o menos
  honesto dos dois.
- [x] **«com a maquina declarada» sobre-promete** — o que está declarado é a condição (mesmo contentor,
  carga diferente), não a máquina, os cores nem o `GOMAXPROCS`. Corrigido para o que é.
- [x] **Fronteira declarada:** `origemDoLimite` quase nunca dirá «default do binario» no deployment
  recomendado, porque o `docker-compose.prod.yml` exporta **sempre** as duas variáveis — quem baixa só o
  global lê «PER_READER=32 (definida)», um 32 que não escreveu em sítio nenhum. O propósito do helper é
  derrotado onde mais faria falta, e a correcção é no compose. E o quarto caso do helper é **vácuo**: com
  nenhuma definida o par é 32 < 256 e o abort não dispara.

### Residual declarado
- **Dívida herdada, não tocada:** o texto de ajuda do `ErrBadIngressLimits` enumera **4 das 8** variáveis
  de ingresso, e o operador lê esse prefixo colado a uma mensagem sobre `AOS_TRAJECTORY_MAX_CONNS*`, que
  não está na lista. Antecede o AOS-459.
- **Fronteira não declarada (mantida):** o sensor de «12 streams vivos com o tecto a 1» fixa
  `global=100 / por-leitor=1`, uma composição que **nenhum nó por defeito tem** (256/32). A frase que o
  operador lê cita um número medido sob um tecto que o seu nó não corre. O eixo gémeo do AOS-456 tem o
  mesmo problema («60 submissões, tecto a 2»). Não é defeito; é escopo que passa a estar dito.

### Estado
**FEITO** (2026-09-29).

---

## AOS-464 — A fila de pedidos de plano reparte-se por SUBMISSOR

### Contexto
`tectoDePendentes = 1000` (`plan_claim.go`) era uma constante do binário, **sem variável de ambiente**,
que protegia o NÓ e não dizia nada sobre QUEM ocupa a fila: um submissor autenticado enfileirava os 1000
e todos os outros levavam **503** em `POST /plans` até alguém drenar.

Não é uma rajada que passa — é **ocupação que fica**: um pedido só sai da fila com desfecho terminal ou
reclamação viva, e nenhum dos dois depende de quem submeteu. É o MESMO defeito que o AOS-456a fechou no
`POST /runs`, um plano ao lado, e nenhuma das outras barreiras o cobria:

| barreira | porque não fecha |
|---|---|
| balde de admissão | é **taxa**, e global entre chamadores: uma submissão custa um token e o pedido fica na fila horas |
| tecto de runs em curso (AOS-456a) | conta runs **hospedados**, e esta rota não hospeda nenhum |
| `edge` / `nginx.conf` | tem `limit_req` (taxa) e **não** `limit_conn` |

Declarado como residual no AOS-460 e repetido no AOS-461 e no AOS-463; fecha aqui.

### O que é DIFERENTE do eixo AOS-456/459, e importa
A contagem **não vive em memória**: sai da projecção da fila, derivada do log. Não há mapa a manter, não
há caminho de libertação e **não há TOCTOU** — as duas contagens saem da MESMA leitura, pelo que a
segunda camada não custa uma varredura a mais. O que há em troca é uma atribuição **degenerada** quando o
gate soberano não está composto (ver os critérios).

### Critérios de aceitação
- [x] `pendentesNaFila` devolve o total **e** a contagem do submissor dado. Numa só função, pela razão
  que `plan_marca_de_agua.go` já declara para a marca de água: a definição de «está na fila» em dois
  sítios deriva.
- [x] Duas guardas em `handlePlanRequest`, **a repartição ANTES do global**. A ordem é pelo
  **diagnóstico**, não pelo custo: quando as duas condições valem, o submissor acima da quota é a causa
  provável, e responder-lhe 503 («o nó não tem quem drene») manda-o procurar o consumidor quando o
  problema são os pedidos dele. Medido: a ordem trocada dá 503, e o teste da ordem avermelha.
- [x] **Códigos distintos:** `429` no por-submissor (o chamador tem de drenar o que é dele), `503` no
  global (o nó não tem consumidor, e a espera certa é a de um operador).
- [x] **A distinguibilidade que isto cria está DECLARADA, e é uma decisão em aberto para o dono.** O
  ADR-030 §2.7 não fixa o código, logo o 429 não contradiz decisão congelada. Mas a §2.1 — e o
  comentário que manda verificar o tecto DEPOIS da autorização — tratam «a fila está cheia» como
  informação interna, e até aqui um chamador recusado não sabia se a culpa era dele ou do nó. Agora,
  ao receber 503 **dentro** da quota, infere que OUTROS encheram a fila: **um bit novo** sobre
  actividade de terceiros. Aceita-se porque (a) só um chamador autenticado o vê, e já via a fila cheia
  antes; (b) é o bit que torna o erro accionável, e uma recusa sobre a qual o chamador não pode agir é
  o defeito que este eixo fecha; (c) não revela a existência de pedido ou run nenhum, nem permite
  contar os pedidos de outro submissor. **Se o dono decidir que um bit é demais**, a correcção está
  escrita no código: 503 nas duas camadas, distinção só no log. Não é uma omissão a descobrir em
  revisão — é uma escolha do implementador, posta por escrito para poder ser revertida sem
  reargumentação.
- [x] `AOS_PLAN_MAX_PENDING` e `AOS_PLAN_MAX_PENDING_PER_SUBMITTER`, com o par validado **FINAL** — a
  correcção do AOS-463 aplicada ao nascer em vez de paga em revisão. Três casos na tabela cobrem
  exactamente a lacuna que o AOS-463 pagou (`global` definido, `porSubmissor` ausente).
- [x] **A repartição NÃO se compõe sem gate soberano de leitura**, e a razão é mais forte do que nos
  outros eixos: [planRequest] tem `run_id` e `objective` e mais nada, pelo que sem `readGov` o principal
  fica **vazio para TODOS**. Um tecto chaveado no vazio não seria contornável — seria um tecto **global
  mais apertado** (125 em vez de 1000), a recusar com 429 chamadores que não excederam nada e anunciado
  como equidade.
- [x] **QUATRO posturas no banner**, não três: «não configurada» e «configurada mas não composta» exigem
  acções DIFERENTES do operador (definir a variável, ou compor o gate), e colapsá-las mandaria metade
  deles editar o ficheiro errado. A dobra segue a convenção de nome e **está registada** em
  `marcadoresDeDobra` — uma dobra fora da convenção é invisível ao guarda e fica engolida pela anterior,
  que foi o achado BAIXO-3/MÉDIO-4 da 8.ª e 9.ª revisões.
- [x] O «1000» em texto fixo **saiu** do banner do caminho do plano: os números vivem na linha do
  ingresso, onde são lidos. Repeti-los em dois sítios é a forma de um deles ficar obsoleto em silêncio, e
  este banner já tinha o 1000 fixo quando o tecto passou a ser afinável.
- [x] `aos_plan_queue_ceiling` publica o valor **em vigor** e não o default; nova série
  `aos_plan_queue_ceiling_per_submitter`, que vale **0** quando a repartição não está composta —
  publicar o valor configurado aí faria o painel afirmar uma equidade que não existe.
- [x] A constante `tectoDePendentes` foi **removida**, não mantida ao lado do novo default: duas
  constantes com o mesmo valor divergem, e o compilador não se queixa de uma `const` de pacote que
  ninguém usa — teria ficado dívida silenciosa no primeiro commit deste ticket.

### Mutações medidas
| mutação | detecta |
|---|---|
| a guarda por-submissor nunca dispara | **2 de 5** testes (3 corridas) |
| ordem trocada (global antes da repartição) | 1 de 5 — o teste da ordem |
| `pendentesNaFila` conta os SEM principal como «de um submissor» | 1 de 5 — **5/5 corridas** |
| a validação do par volta para dentro do ramo | 2 casos da tabela, e só esses |
| ramo VERIFICADO do banner sem `gateComposto` | 2 casos, e só esses |

### O que os testes NÃO cobrem, medido e declarado
O `h.readGov != nil` na atribuição é **cinto-e-suspensórios**: removê-lo sobrevive aos quatro testes de
rota, porque sem `readGov` o `p.Principal` já é vazio e `pendentesNaFila` devolve 0 para submissor vazio.
A protecção vive em dois sítios e os testes de rota só detectam a **conjunção** quebrada. A metade que se
mede sozinha é a de dentro (`TestAOS464ContagemDeSubmISSORVazioNaoContaOsSEMPrincipal`, 5/5). A de fora
fica declarada no código como o que é: vale se alguém vier a preencher o principal por outro caminho.

### Residuais declarados
- **Origem dos valores no compose (herdado do AOS-463, não fechado aqui):** o
  `docker-compose.prod.yml` exporta SEMPRE as duas variáveis (`:-1000`/`:-125`), como as ~30 outras,
  pelo que `origemDoLimite` dirá «definida» sobre um número que o operador não escreveu. A correcção é
  no padrão do ficheiro, para todas, e não para duas — fazê-lo só para estas deixaria o ficheiro
  inconsistente **e** o residual aberto.
- **O tecto global continua sem sensor directo** de que recusa a 1000: nenhum teste submetia até ao
  limite antes deste ticket e nenhum o faz agora. O que se fixou é o comportamento com o tecto
  BAIXADO pela opção; a leitura do valor por ambiente tem a sua própria tabela.

### Estado
**FEITO** (2026-09-30).

---

## AOS-457 — Orçamento POR-PRINCIPAL (o tecto por-run não contém quem submete N runs)

### Contexto
`AOS_BUDGET_MAX_TOKENS` e `AOS_BUDGET_MAX_COST_MICRO_USD` (AOS-257/AOS-260) são o tecto que
**CADA run** recebe, de uma variável de ambiente única — `packages/cmd/aos/budget_env.go` di-lo na
primeira linha. Não existe tecto **por-principal**: N runs × tecto = despesa ilimitada por um só
chamador. Num nó que fala com um modelo pago, é o risco financeiro mais directo de abrir a
terceiros, e o único item da auditoria de prontidão (§3.2) cujo dano é irreversível — tokens gastos
não se devolvem.

### Objectivo
Um tecto de consumo **agregado por principal**, numa janela declarada, que negue a admissão de um
run novo quando o principal já esgotou a sua quota — sem tocar na semântica por-run, que continua a
ser o que o disjuntor e o burn-down usam.

### O que torna este ticket MAIOR do que o AOS-456, e a auditoria não distinguiu
O orçamento por-run é composto **na admissão** e vive na árvore daquele run. Um tecto por-principal
é **estado agregado que atravessa runs** e, portanto:

- tem de ser **durável** (um restart não pode zerar a quota de quem já gastou — senão o tecto
  contorna-se reiniciando o nó, e um tecto que se contorna é pior do que nenhum, porque é anunciado);
- tem de ter **semântica de janela declarada** (diária? mensal? deslizante?) e de **reposição** —
  e essa é uma decisão de produto, não de engenharia;
- interage com o `burndown_ledger` e com o crypto-shredding por-titular: um agregado por principal é
  um **registo sobre uma pessoa**, logo cai no alcance do Art. 17 e tem de ser apagável sem partir
  a contabilidade (o precedente é o AOS-429 — um TTL próprio destruiria a KEK partilhada).

**Por isso este ticket não deve ser implementado sem a janela e a reposição decididas pelo dono.**
Implementá-lo com uma janela inventada entregaria um tecto que ninguém pediu e que o DPO tem de
avaliar.

### Critérios de Aceitação
- [ ] Janela e reposição **declaradas** (decisão do dono registada no ticket antes de implementar).
- [ ] O agregado é **durável**: um restart do nó não repõe a quota consumida — provado por teste de
      crash/retoma, não por inspecção.
- [ ] Principal com quota esgotada é **negado na admissão** do run novo, com recusa atribuível.
- [ ] O tecto por-run continua a valer de forma independente (os dois compõem-se; nenhum substitui
      o outro).
- [ ] O agregado por-principal é alcançável pelo `/dsar/erase` sem destruir a contabilidade dos
      outros titulares (molde AOS-429).
- [ ] `0` ou valor ilegível **aborta o arranque** (molde `ErrBadBudget`).

### Estado
**ABERTO — BLOQUEADO por decisão de produto** (janela + reposição). Reservado por
`sessoes.py reservar`. Dependência: AOS-456 não é pré-requisito; são eixos independentes
(um limita frequência, o outro limita despesa).

---

## Mapa de dependências desta epic

```
P0 (risco activo):  AOS-245 · AOS-246 · AOS-247 · AOS-248 · AOS-250 · AOS-251 · AOS-252 · AOS-255 · AOS-276
P1:                 AOS-249 · AOS-253 (dep. 252) · AOS-256 → AOS-257 → AOS-258
                    AOS-261 → AOS-262 · AOS-266 · AOS-269 · AOS-277
P2 (c/ decisão):    AOS-254 (dep. 252) · AOS-259 (D2) → AOS-260 (D1) · AOS-263 (D4/D5/D6)
                    AOS-264 (D7/D8) → AOS-265 · AOS-267 · AOS-268 (D4/AOS-156)
                    AOS-270 → AOS-271/272 → AOS-273 · AOS-274 · AOS-275 (AOS-096) · AOS-278 (D4)
```

Cadeia causal registada no relatório §8: **F1 → F2 → F3 → F4 → F5** primeiro — fechar wiring novo (budget, exaustão) sobre esta base partida replica o padrão «capacidade verde, efeito zero». O desafio A5 acrescenta **F17 → F15** à cabeça da cadeia de disponibilidade: o fusível do keypool e o clamp de `max_turns` são a ordem mínima defensável de um lote (com os knobs de ingresso, AOS-277, e a correcção documental já aplicada ao relatório).

---

## Modelo de execução — uma branch por wave, agentes em paralelo, revisão adversarial no PR

Cada wave é **uma branch** (`feature/epic20-wN-<slug>`), executada por um agente/modelo **em paralelo** com as outras. O agrupamento é por subsistema — minimiza conflitos de merge (todas as waves tocam `cmd/aos`, mas em ficheiros/funções diferentes). **Ordem de merge:** W0 → W1 → W2 → resto; cada wave seguinte faz rebase sobre a anterior já merged (W0 e W1 partilham `breaker_wiring.go`: W0 merge primeiro).

| Wave | Branch | Tickets | Foco |
|---|---|---|---|
| **W0 — risco activo** | `feature/epic20-w0-risco-activo` | AOS-246, 247, 248 **+ 251, 252** (ver nota) | Os fixes P0 de segurança/disponibilidade que a wave entregou (F2, F5, F11, F14) mais os dois de ciclo de vida que vieram com ela (F3, F4) |
| **W1 — ciclo de vida do run** | `feature/epic20-w1-ciclo-vida` | ~~AOS-251, 252~~, 253, 254 | Crash-resume e saga (F9, F12, F13); breaker efectivo e estados terminais saíram na W0 |
| **W2 — billing & exaustão** | `feature/epic20-w2-billing` | AOS-255, 256, 257, 258, 261, 262, 263, 277 | Budget token-only, burn-down, knobs de ingresso (§7 do relatório) |
| **W3 — identidade & credenciais** | `feature/epic20-w3-identidade` | AOS-264, 265, 266, 278 | Broker, attestation, authn do GW (D4/D7/D8-dependentes marcados) |
| **W4 — ADR-021 scoring** | `feature/epic20-w4-scoring-gw` | AOS-269 | Scoring determinístico no GW |
| **W5 — ADR-022 plano** | `feature/epic20-w5-plano-v2` | AOS-270, 271, 272, 273 | Arestas condicionais, verificador, payload tipado, migração |
| **W6 — operações** | `feature/epic20-w6-operacoes` | AOS-249, 267, 268, 274, 275 | Vault, retenção, WORM ancorado, SLOs, promote |

**Nota de âmbito da W0 (emenda registada, não violação silenciosa).** A branch `feature/epic20-w0-risco-activo` entregou **AOS-246/247/248 + AOS-251/252**, e **não** entregou AOS-245, AOS-250 nem AOS-276 (zero linhas de código). A tabela acima foi corrigida para dizer o que a branch é, em vez de a branch contradizer a tabela. As duas consequências, ambas registadas:

- **Porque a mistura importou:** AOS-251 e AOS-252 disputam a MESMA aresta de estado do run. Escritos como se fossem independentes, produziram uma contradição determinista — o controlo de AOS-251 exigia a máquina em `running` no fim de um run bem-sucedido, o selo terminal de AOS-252 escreve `running→complete` no mesmo caminho de saída, e a suite ficou vermelha. Reconciliado na remediação: o invariante de AOS-251 é a **aresta** `ready→running` com a razão do claim de arranque (relida do log), não o estado final; o estado final é `complete`, como AOS-252 exige. É a prova de que os dois mecanismos coexistem — que nenhuma das duas waves teria produzido sozinha.
- **Ordem de merge:** cai a ressalva «W0 e W1 partilham `breaker_wiring.go`: W0 merge primeiro» — com AOS-251/252 dentro da W0, a W1 fica reduzida a AOS-253/254 e faz rebase sobre esta branch.
- **Como entraram na W0.** Não foi alargamento de âmbito de nenhum agente da wave: o AOS-251 e o AOS-252 estavam a ser escritos **em simultâneo, na mesma working tree**, pela sessão da W1. O agente do AOS-248 chegou a avisar disso no seu relatório («outro agente está a modificar esta working tree ao mesmo tempo»); o aviso passou-me ao lado e o commit da W0 varreu o trabalho dele em curso. A causa é a partilha da árvore por duas sessões, não a conduta de nenhuma delas. *(Uma versão anterior desta nota — e a mensagem do commit `1100bcb` — atribuíam isto a alargamento de âmbito do agente do AOS-248. Era falso; fica corrigido aqui.)*
- **⚠️ AOS-251 e AOS-252 têm DUAS implementações independentes.** A da W0/W1 (acima) e uma anterior, por committar noutros worktrees: `.claude/worktrees/nice-cartwright-529d11` (AOS-251, branch `claude/compassionate-curie-e2866b` — `breaker_trip_node_test.go`, `reference-monitor/action_observer_test.go`) e `.claude/worktrees/admiring-wright-dec4c1` (AOS-252, branch `claude/vibrant-wilson-ce1127` — `deadline_sweeper.go`, `terminal_durable_test.go`). Nomes de ficheiro diferentes, mesmos ficheiros de produção tocados (`bootstrap.go`, `service.go`, `steer_gates.go`). **Quem fizer o merge tem de escolher UMA das duas e descartar a outra** — juntá-las cegamente compõe dois claims de arranque e dois selos terminais sobre a mesma aresta de estado. A da W0 é a única com prova de coexistência dos dois mecanismos; a dos worktrees não foi avaliada.
- **AOS-245, AOS-250 e AOS-276 SAEM da W0** e continuam `ABERTO`, sem código nesta branch. Eixo: AOS-245 e AOS-250 vão para a wave que tocar o step-ledger e a fronteira de ingresso; AOS-276 depende de D11 (decisão do dono) e não podia entrar na mesma. Nenhum dos três está fechado por omissão — estão declarados por fechar.

**Regras do modelo:**

1. **Um agente por wave**, com a branch como fronteira — não toca nos ficheiros das outras waves (a tabela de dependências da §4 manda nas intra-wave).
2. **Revisão adversarial obrigatória no PR** — o modelo adversário revê o diff contra os Critérios de Aceitação do ticket e contra o código (não contra a intenção), no molde dos desafios A1–A5: achado só confirmado se não for refutável. O adversário **pode melhorar o código directamente no PR** (commit de revisão identificado), mas nunca alarga o escopo do ticket — desvios abrem ticket novo.
3. **Gates fail-closed por PR:** `make ci-build`, `ci-lint`, `ci-test` + o gate de domínio aplicável; cobertura sem regressão (pisos AOS-199). O PR só merge verde **e** com a revisão adversarial aprovada.
4. **Tickets bloqueados por decisão do dono (D1–D12)** não entram na wave até a decisão estar registada no ticket — o agente não decide pelo dono.
5. **Cada PR actualiza o relatório de prontidão** (estado do finding que fecha) e o `Estado` do ticket na epic — o documento continua a ser a fonte de verdade do que falta.
## AOS-279 — Golden-set do planeador no gate de CI (fecha DEF-276)

### Contexto
O AC2 de AOS-273 pedia que os casos das extensões de ADR-022 «passassem no eval-gate». Passavam — no eval-gate do **planeador** (`plannerprompt.Evaluate`, AOS-241), que a CI corre como testes do módulo. Mas o gate de CI `evalgate.sh` corre **outro** harness (`packages/platform/eval`) e não conhecia este conjunto: uma regressão de cobertura ali não tinha sinal **com esse nome**. Essa metade ficou declarada «NÃO ENTREGUE» e registada em DEF-276.

### Critérios de Aceitação
- [x] Os casos do golden-set do planeador correm no gate de CI `evalgate.sh` e o pass-rate entra no relatório do gate — via a linha marcada **própria** `AOS_PLANNER_EVAL_REPORT` (emitida por `TestPlannerEvalReportEmitted`), capturada e imposta pelo gate.
- [x] **Sem acoplar módulos:** o gate corre `control-plane/orchestrator` **separadamente** de `platform/eval`. Ligar `Evaluate` a esse harness faria `platform/eval` importar o orquestrador — acoplamento que o `layer-lint` guarda e que o `go.mod` teria de declarar.
- [x] **Guardas do gate:** cobertura **não-vazia** primeiro (um relatório a zeros passaria por ausência de evidência), **segurança 100%** (regra, não limiar) e o **veredicto da política do próprio planeador** (`passed`).
- [x] **Falsificável** nos quatro vectores: aceita o relatório real e recusa cobertura vazia, segurança degradada (11/12), `passed:false` e linha truncada.

### Nota de desenho — a qualidade é SINAL, não limiar
A taxa de qualidade é **8/10 por desenho**: dois candidatos fracos existem para **falhar** a rubrica do seu caso, e é isso que torna o conjunto discriminante. Impor-lhe o piso `EVAL_PASS_RATE_MIN` (0.90) do harness de acções seria medir uma coisa com a régua de outra — e a «correcção» natural seria apagar os candidatos fracos, ou seja apagar a cobertura que dá valor ao conjunto. O par de qualidade entra no log do gate como sinal; quem decide se é aceitável é a política do planeador.

### Estado
**FECHADO** (PR #22, 2026-08-13). Ticket **criado pela remediação deste próprio epic** para dar eixo executável a DEF-276 — não pertence ao âmbito original dos 34 (ver a nota de contagem no §0).

---

## AOS-280 — Compor o estágio de roteamento no pipeline do Model Gateway (fecha DEF-271)

### Contexto
**DEF-271.** O `router.Router` de **AOS-059** — soberania, carga, tiering, degradação por orçamento e, desde AOS-269, o scoring ponderado de ADR-021 — **nunca esteve composto em produção**: o `NewProduction` do gateway compõe `failover.NewStage` como estágio de roteamento e `router.New` não tem, em todo o repositório, um chamador fora de testes. O scoring foi construído **sobre** esse router e herdou o facto de não estar ligado (alcance fixado na **emenda 1.1 do ADR-021**).

### Decisão de desenho — ENCADEAR (dono, 2026-08-13)
`failover` **→** `routingstage`+`router`. Não substituir nem fundir. É o desenho **já declarado** no doc do pacote `failover` («AOS-059 refina a escolha SEMPRE dentro dos sobreviventes intra-fronteira que esta guarda autoriza — nunca a expande»), e é o único que não perde nada:

- o **`failover`** mantém o que só ele faz: impor a soberania, fazer *failover por saúde* dentro da fronteira, e **selar o deny cross-border no WORM** (`audit.DecisionDeny`, via o `allowlist.Recorder` de AOS-058);
- o **`router`** refina **dentro** dessa fronteira: carga (headroom real), tier mais barato capaz, degradação por orçamento e o ranking ponderado.

A alternativa (o router substituir o failover) foi **recusada** com razão nomeada: a saúde passaria de decisão a *peso* do score, e perder-se-ia a selagem WORM do deny de soberania — o router só tem `DecisionSink`, que é análise post-hoc, não trilho de auditoria.

### O que a investigação encontrou (e que o DEF-271 não nomeava)
1. **Encadear ingenuamente está partido.** `routingstage.Stage.Process` lê `ex.RequestedRegion` e depois **sobrescreve** `ex.ResolvedRegion` — colocado a seguir ao `failover`, **descartaria a decisão dele em silêncio**, incluindo um failover por saúde deliberado.
2. **Falta o classificador de produção.** Só existe o `DefaultClassifier`, que **não povoa `Candidates`** (o próprio doc diz «produção injecta um classificador que lê os hints da tarefa»). Sem candidatos, o `partition` trata a região pedida como único sobrevivente e **o headroom, o tiering e o scoring ficam degenerados** — compor o estágio assim seria ligar uma capacidade que continua sem efeito, exactamente o erro que DEF-271 regista.
3. **As portas de factores precisam de fontes reais** — `LoadProvider` do keypool, `BudgetProvider` da degradação, *task-fit* do eval harness.

### Critérios de Aceitação
- [x] `routingstage` consome a região **RESOLVIDA** pelo estágio anterior quando existe (e a pedida quando não existe), em vez de ler sempre a pedida — sem o que o encadeamento descarta o `failover`. Teste que prova que uma decisão de failover por saúde **sobrevive** ao routing.
  - Forma escolhida e **declarada no doc do pacote**: FALLBACK (`ex.ResolvedRegion` → `ex.RequestedRegion`; idem para o provider), não uma Option — não há composição correcta em que o refino deva ignorar uma resolução já tomada, e o fallback preserva byte-a-byte o uso standalone. `routingstage.InputRegion/InputProvider` expõem a regra ao classificador para que as duas metades não divirjam.
  - O AC pede mais do que a região de entrada: o failover por saúde só sobrevive porque o classificador de produção **também não oferece como candidato** o endpoint que a guarda de saúde excluiu (a saúde é DECISÃO do failover, não peso do score). Provado com gémeo independente (`TestAOS280_Chain_HealthFailoverSurvivesRefinement`) que mostra o encadeamento ingénuo a voltar à região pedida.
- [x] Classificador de **produção** que povoa `Candidates` a partir do inventário de contas de infra e resolve o `Profile` de pesos a partir da classe/hints da tarefa; `DefaultClassifier` fica como referência.
  - `routingstage.ProductionClassifier`: capacidade do **tier do modelo pedido** na escada; candidatos = inventário ∩ `policy.AllowedRegions(board, modelo)` ∩ saudáveis; perfil por classe (`ProfileByClass`), validado contra a tabela assinada **no arranque**. Modelo fora da escada ⇒ `Task.NoRefine` com razão registada (mantém a rota soberana; nunca capacidade inventada).
- [x] Portas de factores ligadas às fontes reais (`LoadProvider` do keypool via `router.HeadroomReaderFrom`, `BudgetProvider`, *task-fit*).
  - `LoadProvider` **derivado do keypool** (`keypool.Registry.Headroom`, leitura pura nova, mesma aritmética inteira do `worstAxisLoad` que já governa a selecção); custo da escada; latência pelo p95/bit `Fast`; saúde da mesma `Health` do failover.
  - [ ] **NÃO ENTREGUE — o `BudgetProvider` e o *task-fit* ficam como SEAMS injectados, sem fonte ligada dentro do módulo.** O burn-down real vive no control-plane (EPIC-03) e o *task-fit* no eval harness (EPIC-08); ligá-los daqui obrigaria o caminho quente do GW a importar outro módulo, contra o layering de `budgetbridge`/`tieradapter`. Residual **DEF-280-PORTAS** (registo de deferimentos).
  - [ ] **NÃO ENTREGUE — `EstimatedTokens` fica a 0**: o `pipeline.Exchange` não transporta o prompt, pelo que a reserva de admissão coordena na dimensão de PEDIDOS e não na de tokens. Residual **DEF-280-TOKENS**.
- [x] `NewProduction` compõe `failover` **→** `routingstage(router.New(…, WithScoring(…)))` com a tabela de pesos embebida; sem tabela válida o scoring **recusa** (regra 3 do ADR-021, agora com efeito real).
  - Encadeados por `pipeline.Chain` (novo primitivo de composição no slot, que pára fail-closed no primeiro estágio que recuse e propaga o erro TAL QUAL). A cadeia arma-se quando o deployment **declara a escada** (`RoutingConfig.Tiers`); sem escada, o slot fica só com o failover (sem regressão). Tabela de pesos inválida ⇒ o GW **não arranca** (`ErrRoutingWeights`), perfil desconhecido ⇒ recusa de **boot** (`ErrRoutingProfileUnknown`).
  - [ ] **NÃO ENTREGUE — o nó de referência (`packages/cmd/aos`) não declara escada**, pelo que o binário do nó continua a rotear só pelo failover. É outro módulo (fora do âmbito deste ticket) e a escada é decisão de deployment (cobertura de preços/credenciais por modelo). Residual **DEF-280-NO** com eixo.
- [x] **Prova pela cadeia real, ao nível do gateway composto** (não do router isolado): soberania (deny cross-border continua selado no WORM), saturação, degradação por orçamento, e o scoring a ordenar sobreviventes — mais o negativo de cada um.
  - `packages/platform/model-gateway/routing_chain_aos280_test.go`: 9 testes sobre `NewProduction`+`Chat` contra um servidor OpenAI-compatible que regista o **modelo despachado** (corpo do pedido) e a **região efectiva** (bearer por região). Acrescentados à lista REQUIRED do gate `routing.sh` (bloco 1-bis) para não poderem passar vazios.
- [x] Sem regressão: os cenários de AOS-063 e o gate `routing.sh` (11 cenários) continuam verdes.
  - `go test ./...` do módulo verde (incluindo `routingtests`); cobertura do módulo **87.9%** (piso 80%).

### Remediação adversarial (2026-08-13, pós-entrega)
Duas auditorias correram sobre a árvore entregue. O que despachar um modelo **diferente do pedido** quebrou — e que a entrega inicial deixou em prosa ou em silêncio — foi corrigido:

- [x] **Cobertura de preço é recusa de ARRANQUE, não uma surpresa por chamada** (`ErrRoutingPriceCoverage`). A escada é cruzada com a contabilidade de custo no boot, sobre o produto **alcançável** (modelos da escada × regiões das contas de infra, filtrado pelo que a allowlist autoriza a algum board). Sem esta guarda, um tier sem preço fazia o GW **invocar e ser facturado pelo** provider e só depois falhar em `recordCost` com `pricing.ErrNoPrice` — dinheiro gasto, chamador sem resposta, retry a repetir o gasto. A porta de introspecção é nova e **pura** (`cost.Calculator.HasPrice`/`cost.Recorder.HasPrice`); `RoutingConfig.PriceCoverage` deixa um deployment com outra contabilidade impor a mesma invariante. Sem `ProductionConfig.Cost` não há verificação — e é correcto: sem recorder nenhuma chamada é recusada por falta de preço.
- [x] **Uma `RoutingConfig` preenchida SEM `Tiers` deixa de arrancar verde** (`ErrNoRoutingTiers`). Antes, `Admission`/`Budget`/`Profile`/`DecisionSink` desligavam-se em silêncio — o tecto **global** do ADR-008 ficava por reservar sem que nada o dissesse. O **valor-zero** continua a ser «sem refino» (retro-compatibilidade intacta), e um guard-test por reflexão obriga quem acrescentar um campo a `RoutingConfig` a revisitar o predicado.
- [x] **A troca de modelo é SELADA no audit WORM** (terceiro elo da cadeia, `modelSwapRecorder`). O estágio de allowlist sela o par **pedido**; quando o refino despacha outro modelo, o par **efectivo** (modelo + região) passa a ter o seu `allowlist.GovRecord` no mesmo eixo tamper-evident em que o deny cross-border já ficava, atribuível a principal + board, com a razão do router. O `DecisionSink` continua a ser o que sempre foi — análise post-hoc, não audit. Fail-closed: selagem falhada aborta a chamada **antes** de o provider ser invocado.
- [x] **Um perfil por omissão desconhecido acusa a peça certa** (`ErrRoutingProfileUnknown`, não `ErrRoutingWeights`), com a cadeia do sentinela preservada (`%w`). Um typo em `Profile` mandava o operador depurar a assinatura/trust anchor da tabela **embebida e intacta** — o modo de falha que ADR-010/AOS-011 nomeiam.
- [x] **O guard AST de determinismo passa a cobrir quem DECIDE**, não só quem é importado por quem decide: o fecho parte de **duas** raízes (`routing/router` **e** `routing/routingstage`, que importa o router e por isso nunca era visitado) e varre, por ficheiro, `production_routing.go` (o pacote raiz não é varrível — a fachada usa o relógio legitimamente). A fronteira do varrimento (`pipeline`, transporte do Exchange e DTOs do wire) é **declarada com razão nomeada** em vez de ser um efeito lateral do grafo de imports.
- [x] Quatro cenários novos na lista REQUIRED do `routing.sh` (bloco 1-bis), cada um com positivo **e** negativo: `TestAOS280_Chain_ModelSwapSealedInWORM`, `TestAOS280_PartialRoutingConfigWithoutTiers_FailsClosedAtBoot`, `TestAOS280_UnpricedLadderTier_FailsClosedAtBoot`, `TestAOS280_UnknownDefaultProfile_BlamesProfileNotWeightsArtefact`.
- [ ] **NÃO ENTREGUE — a selagem cobre a troca de MODELO, não uma mudança só de REGIÃO.** Quando o refino mantém o modelo e muda a região dentro da fronteira já validada, mantém-se a postura de AOS-058 (a guarda de soberania decide-a; o registo de atribuição sela a região efectiva), inalterada por este ticket. Residual **DEF-280-REGIAO**.
- [x] **O ADR-021 ficou alinhado com o código — emenda 1.2 RATIFICADA pelo dono em 2026-08-14.** O §5-bis dizia que o scoring «não tem efeito em produção» enquanto DEF-271 não fechasse, e DEF-271 fechou aqui. A remediação **não editou** o ADR (autoridade congelada; actualizá-lo é emenda de dono, não do implementador — Carta §6) e registou a divergência em **DEF-280-ADR021** com o texto proposto — que foi o comportamento certo. O dono ratificou: o ADR ganhou o **§5-ter**, o §5-bis ficou marcado **SUPERADO** (por baixo, não reescrito, para se ver *quando* deixou de valer) e o **DEF-280-ADR021 fechou**. O doc do pacote `router` deixou de declarar divergência e passa a citar a emenda.

### Estado
**FECHADO (com residuais declarados)** — cadeia composta, classificador de produção entregue e provada pela cadeia real; remediação adversarial aplicada (cobertura de preço e config parcial como recusa de arranque, troca de modelo selada no WORM, atribuibilidade do erro de perfil, guard AST alargado a quem decide). Ficam com eixo: as duas portas de factor cuja fonte vive noutro módulo (DEF-280-PORTAS), a estimativa de tokens (DEF-280-TOKENS), a declaração da escada no nó de referência (DEF-280-NO), a selagem de uma mudança só de região (DEF-280-REGIAO) e a emenda 1.2 do ADR-021 (DEF-280-ADR021, decisão do dono).

---

