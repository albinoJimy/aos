# EPIC-20 — Prontidão Agêntica: remediação de achados, custo governado e extensões ADR-021/022

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Remediação dos achados de prontidão (F1–F16), billing token-only ligado ao nó, e implementação dos ADR-021/022 |
| Versão | 1.0 |
| Data | 2026-08-08 |
| Classificação | Documento de Referência — **Proposta** (tickets ABERTOS) |
| Documento-fonte | **`docs/reports/prontidao-modelos-agenticos.md`** (relatório consolidado, 2026-08-08) |
| ADRs assumidos aprovados | **ADR-021** (scoring determinístico no GW) · **ADR-022** (arestas condicionais, papel verificador, payload tipado) |
| Documentos relacionados | `docs/reports/desafio-A1..A4-*.md` (planos de fecho corrigidos + decisões do dono), ADR-008/010/011/012/013/018/020, `tecnica/06`, `tecnica/18` |

---

## 1. Visão do Epic

O relatório de prontidão consolidou quatro vagas de avaliação: os gaps originais (efeitos, feedback, aprovação humana) estão **fechados**, mas as vagas 3–4 expuseram defeitos **activos hoje** — o step-ledger a persistir outputs de tools em claro (F1), o circuit breaker inerte no run comum (F2+F3), a máquina de estados sem desfecho durável (F4) — e um corpo de capacidades verdes em teste sem costura no deployment (budget, progress-surface, broker, portas de attestation). Esta epic executa **todas as acções do relatório**: primeiro remove o risco activo, depois liga o billing token-only (plano corrigido pelos desafios A1/A2), depois as capacidades dependentes de decisão do dono, e por fim implementa os **ADR-021 e ADR-022 como aprovados**.

Invariantes congeladas: toda a tool call mediada pelo RM (ADR-002); fail-closed por omissão; determinismo/replay (ADR-010); o banner declara a postura real (AOS-203); nenhum wiring novo sem teste de composição **pela cadeia de produção** (a lição da sessão 2026-08-08: «um teste de composição que substitui a peça vizinha por um duplo NÃO é um teste de composição»).

## 2. Fronteira eu-construo vs. deployment/dependências

| Frente | Código desta epic | Fora (dependência/decisão) |
|---|---|---|
| Risco activo (F1–F6, F11–F15) | Tudo — wiring e guards no nó | — |
| Billing token-only | Wiring do hook, ciclo de vida por-run, envs, estimador | **D1/D2** (âmbito tool-only; eixo $) — recomendações registadas |
| Turno de modelo no orçamento | Pré-requisito de contrato (`port.Usage`) | **D1 opção B** — ticket separado, pós-decisão |
| Exaustão graciosa completa | Retentor de spans, resolvedores, burn-down+aviso | **D4/D5/D6** (2.º tipo de PendingRecord, autoridade do `extend`, dono do tecto) |
| Broker de credenciais | Passo zero de política/identidade, cliente Vault, porta com contexto | **D7/D8** (separar cliente Vault; v1 in-process) |
| Verificação ancorada do WORM | Env trust anchor + `VerifyFromCheckpointAtHead` no restart | **D4/AOS-156** — custódia da chave do operador (infra-org) |
| ADR-021 / ADR-022 | Toda a implementação | Gramática concreta de perfis/condições/payloads → `tecnica/06`/`tecnica/18` |
| ORQ/SCH distribuídos | **Nada** — deferimento ADR-018 mantido | EPIC-10 (frota multi-nó) |

## 3. Critérios de Saída do Epic

- [ ] Nenhum output de tool call é persistido em claro: o step-ledger sela por-titular como o capturer (AOS-245), e o shred/expire apaga ambos (prova: erase → `ErrDecrypt` nos dois registos).
- [ ] O breaker **dispara** no run comum: teste de nó repete a mesma call negada e assere trip **antes** de `MaxTurns` (AOS-251); ligar velocidades sem fonte **aborta o arranque** (AOS-246).
- [ ] O log durável distingue desfecho de crash: `complete`/`failed` escritos em todos os caminhos; `CheckDeadlines` com caller periódico (AOS-252).
- [ ] Um crash a meio de um run é retomado por varredura no arranque, sem re-executar efeitos (AOS-253).
- [ ] Um run com `AOS_BUDGET_MAX_TOKENS` definido é **negado por orçamento** com o deny selado e atribuído, e um run dentro do tecto obtém **permit** — ambos ao nível do nó (AOS-256..258).
- [ ] O banner declara budget/broker/modelo/autonomia (AOS-248) — postura anunciada = postura ligada.
- [ ] Burn-down visível no run real com aviso a ~80% (AOS-261/262).
- [ ] ADR-021: o router ordena por scoring determinístico com tabela de pesos assinada; guard-test prova função pura; nenhum peso elege candidato cross-border (AOS-269).
- [ ] ADR-022: PlanDocument aceita arestas condicionais, `role: verifier` e payload tipado — validador puro rejeita ciclo disfarçado, auto-verificação e taint incompatível (AOS-270..273).
- [ ] Gates CI verdes; cobertura sem regressão (pisos AOS-199).

## 4. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências | Fecha |
|---|---|---|---|---|---|---|
| AOS-245 | Step-ledger selado por-titular por-`Apply` | fix | M | **P0** | — | F1 |
| AOS-246 | Breaker: erro de construção fatal no arranque + saneamento das envs de velocidade | fix | S | **P0** | — | F2 |
| AOS-247 | Guarda de produção no fallback de credencial dev do GW | fix | S | **P0** | — | F5 |
| AOS-248 | Banner de verdade: budget/broker/modelo/autonomia + `WithSink` no autonomy registry | fix | S | **P0** | — | F11, F14 |
| AOS-249 | Higiene Vault DSAR: validação de esquema + renovação de token | fix | M | P1 | — | F6 |
| AOS-250 | Clamp de `MaxTurns` no submit | fix | S | P1 | — | F15 |
| AOS-251 | Breaker efectivo: `observeAction` ligado + claim `ready→running` no arranque do run + teste de trip | fix | M | **P0** | — | F3 |
| AOS-252 | Estados terminais duráveis (`complete`/`failed`) + caller de `CheckDeadlines` | feature | M | **P0** | — | F4 |
| AOS-253 | Crash-resume: varredura de runs órfãos + `Resumer` composto | feature | L | P1 | AOS-252 | F9, F13 |
| AOS-254 | Saga/compensação com construtor de produção | feature | M | P2 | AOS-252 | F12 |
| AOS-255 | Declaração tool-only/token-only: texto do banner + doc | docs | S | **P0** | — | §7.1 |
| AOS-256 | Ciclo de vida do nó de orçamento por-run (`AddNode` + release no seam por-run) | feature | S | P1 | — | A1-risco 4 |
| AOS-257 | `BudgetCheck` no lugar do stub + `Settle` como decorator do `ActivityDispatcher` + envs `AOS_BUDGET_*` (tokens) | feature | M | P1 | AOS-256 | billing |
| AOS-258 | Estimador real via `CallContext` + teste de nó com permit (não-vacuoso) | feature | M | P1 | AOS-257 | billing |
| AOS-259 | Canal de custo: campo em `port.Usage`/`ChatResponse` + `translateResponse` + dedup por parentesco + `WithCost` no wiring | feature | M | P2 | D2 | A1-risco 2, A2-E |
| AOS-260 | Admissão do turno de modelo (reservar antes de `loop.go:549`) | feature | L | P2 | AOS-259, D1 | A1-risco 1 |
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
**ABERTO.**

---

## AOS-246 — Breaker: erro de construção fatal no arranque + saneamento das envs de velocidade

### Contexto
**F2 (alta, fail-open).** O nó nunca cabla `VelocitySource` (`breaker_wiring.go:70-72`); `NewBreaker` devolve `ErrVelocitySourceMissing`; o `resolve()` **engole** o erro (`breaker_wiring.go:73-77`) — ligar `AOS_BREAKER_MAX_*_PER_SEC` desliga o disjuntor inteiro, em silêncio. Fonte: análise 08-08, confirmado pelos desafios A1/A2 (D-A1.4: recomendação B+C).

### Objectivo
Tornar o erro de construção do breaker **fatal** quando a config o exige; anotar/retirar as envs de velocidade da doc até existir fonte.

### Critérios de Aceitação
- [ ] Velocidade > 0 sem `VelocitySource` ⇒ **aborta o arranque** com erro nomeado (nunca `return nil`).
- [ ] `deploy/node/README.md` deixa de recomendar «ligar quando tiver dados» sem a ressalva; a env é marcada como inócua-até-fonte ou removida da tabela.
- [ ] Teste: boot com a env definida falha fail-closed; boot sem ela compõe o breaker normal.

### Estado
**ABERTO.**

---

## AOS-247 — Guarda de produção no fallback de credencial dev do GW

### Contexto
**F5 (média).** Sem `AOS_MODEL_API_KEY_PATH`, o nó arranca — mesmo em `AOS_MODE=production` — a apresentar o bearer de dev `aos-dev-omniroute` embebido no binário (`modelgatewaywiring.go:78-81`). Não declarado. Fonte: desafio A3.

### Objectivo
Em produção, a ausência do ficheiro de credencial do modelo é fatal; fora de produção, o fallback é declarado no banner.

### Critérios de Aceitação
- [ ] `AOS_MODE=production` sem `AOS_MODEL_API_KEY_PATH` (com gateway ligado) ⇒ arranque aborta com erro nomeado.
- [ ] Modo referência com fallback ⇒ linha de banner declara «bearer de DEV em uso».
- [ ] Testes nos dois sentidos.

### Estado
**ABERTO.**

---

## AOS-248 — Banner de verdade: budget/broker/modelo/autonomia + `WithSink` no autonomy registry

### Contexto
**F11 (média, uma linha):** `buildAutonomyOracle` chama `NewLevelRegistry()` sem `WithSink` — mudanças de nível de autonomia não ficam registadas. **F14 (baixa):** o banner é mudo sobre budget (stub), broker (ausente) e modelo/gateway (real vs referência) — diverge da disciplina AOS-203. E `AOS_AUTONOMY_LEVELS` não tem linha (sem ela nenhum `escalate` é emitido). Fontes: desafios A3/A4, análise 08-08.

### Objectivo
Postura anunciada = postura ligada, para estas quatro superfícies.

### Critérios de Aceitação
- [ ] `WithSink` ligado ao WORM composto; `SetLevel` de provisionamento selado com actor e motivo.
- [ ] Banner declara: orçamento (NÃO COMPOSTO — eixo AOS-008/EPIC-20), broker (ausente — credenciais por ficheiro), modelo (gateway real vs `referenceModel`), autonomia (oráculo ligado/nil).
- [ ] `env_surface_test` e testes de banner actualizados; nenhuma linha promete o que não está ligado.

### Estado
**ABERTO.**

---

## AOS-249 — Higiene Vault DSAR: validação de esquema + renovação de token

### Contexto
**F6 (média).** `parseVaultDSARFromEnv` não valida o esquema de `AOS_DSAR_VAULT_ADDR` (o gémeo de attestation no mesmo binário exige https-ou-loopback); o token é lido uma vez e nunca renovado — tokens curtos (recomendados pelo README) matam a custódia silenciosamente, com `/readyz` verde (sonda não autenticada). Fonte: desafio A3 (eixo AOS-215/216).

### Objectivo
Validação de esquema fail-closed; renovação periódica do token (ou falha ruidosa antes da expiração).

### Critérios de Aceitação
- [ ] `AOS_DSAR_VAULT_ADDR` não-https fora de loopback ⇒ aborta (molde `checkRemoteAttestationURL`).
- [ ] Token renovado em background (molde do sweeper de aprovações) ou `/readyz` falha **antes** da expiração — nunca morte silenciosa da custódia.
- [ ] Teste: token expirado ⇒ readiness vermelho e erase/expire fail-closed.

### Estado
**ABERTO.**

---

## AOS-250 — Clamp de `MaxTurns` no submit

### Contexto
**F15 (baixa).** `max_turns` vem do corpo do `POST /runs` sem limite superior (`api.go:525`); hoje é um dos poucos travões reais de custo. Fonte: desafio A1 (risco 8).

### Objectivo
Tecto de operador para `MaxTurns`, com default sensato e clamp auditado.

### Critérios de Aceitação
- [ ] Env de tecto (ex.: `AOS_MAX_TURNS_CEILING`) na tabela AOS-203; pedido acima ⇒ clamp declarado ou 422 (decisão registada).
- [ ] Teste de API nos dois modos.

### Estado
**ABERTO.**

---

## AOS-251 — Breaker efectivo: `observeAction` ligado + claim `ready→running` + teste de trip

### Contexto
**F3 (alta), dois mecanismos independentes:** (a) `observeAction` (`breaker_wiring.go:83`) tem zero chamadores ⇒ no-progress nunca dispara; (b) `Observe` é no-op fora de `Running` (`breaker.go:213-215`) e o lazy-claim só transita no primeiro steer/escalada (`steer_gates.go:117-118,141-142`) ⇒ um run comum fica em `ready` do princípio ao fim. Fontes: análise 08-08 + desafios A2/A4 (verificados à mão). Divergência com o desafio A1 registada no relatório §9 — este ticket inclui o teste que a resolve.

### Objectivo
O breaker dispara no run comum: alimentar o detector e armar o claim no arranque do run.

### Critérios de Aceitação
- [ ] `observeAction(runID, hash)` chamado no fecho do `execute_tool` (reusa `agentruntime.AttrToolCallHash`); teste negativo que falha se ficar sem chamadores.
- [ ] O run transita `ready→running` no início da execução (não no primeiro pause/escalada); máquina de estados e fencing ajustados.
- [ ] **Teste de nó:** a mesma call negada repetida ⇒ trip do breaker **antes** de `MaxTurns`, com veredicto atribuído no WORM.
- [ ] Divergência A1↔A2/A4 resolvida por este teste e registada no relatório.

### Estado
**ABERTO.**

---

## AOS-252 — Estados terminais duráveis + caller de `CheckDeadlines`

### Contexto
**F4 (alta).** `grep state.Complete|state.Failed` não-teste: vazio. O nó conduz 5 das 13 arestas; `CheckDeadlines` (`state/machine.go:550`) tem zero chamadores de produção apesar de `liveness/doc.go` exigir execução periódica. O desfecho vive num mapa em memória com poda FIFO — um run acabado por erro/panic/MaxTurns é, no log durável, indistinguível de um crash. Fonte: desafio A4.

### Objectivo
Escrever `complete`/`failed` em todos os caminhos de saída do loop; correr `CheckDeadlines` periodicamente (molde do sweeper de aprovações).

### Critérios de Aceitação
- [ ] Todo o fim de run (sucesso, erro, MaxTurns, breaker, panic recuperado) escreve o estado terminal no log durável.
- [ ] `CheckDeadlines` com caller periódico composto no loop de serviço; `running→timed_out` materializado.
- [ ] Teste: crash simulado vs fim normal distinguem-se no log; `GET /runs/{id}` reflecte o desfecho durável após restart.

### Estado
**ABERTO.**

---

## AOS-253 — Crash-resume: varredura de runs órfãos + `Resumer` composto

### Contexto
**F9+F13 (média).** O `Resumer` (AOS-015) nunca é composto — checkpoints escritos, nunca lidos; `worker.Assigner.TryAcquire` só corre no submit; nenhuma varredura de arranque. Um crash não é retomado por ninguém; re-submeter recomeça do turno 1. Fontes: análise 08-08, desafio A4 (item 6; eixo AOS-015/099).

### Objectivo
No arranque do serviço, varrer streams com estado não-terminal, reconstruir o cursor (checkpoints + ledger) e retomar sem re-executar efeitos.

### Critérios de Aceitação
- [ ] Varrimento no arranque reclama runs interrompidos (lease expirado, sem estado terminal) — depende de AOS-252 para distinguir órfão de terminado.
- [ ] Retoma pelo `Resumer` continua do último checkpoint; efeitos não se repetem (dedup provado).
- [ ] Teste de nó: kill a meio → restart → run completa sem double-execution e sem re-interrogar o modelo nos turnos já capturados.
- [ ] Banner declara o resultado da varredura (N runs retomados).

### Estado
**ABERTO.**

---

## AOS-254 — Saga/compensação com construtor de produção

### Contexto
**F12 (média).** `kernel/agent-runtime/saga` está no fecho do nó mas `SagaCoordinator`/`WithCompensationRegistry` só têm chamadores de teste; sem `failed` (F4) a aresta de compensação é duplamente inalcançável. Fonte: desafio A4 (item 3).

### Objectivo
Compor a compensação no caminho de falha durável, para efeitos reversíveis registados.

### Critérios de Aceitação
- [ ] `WithCompensationRegistry` chamado na composição de produção; `failed→compensating` alcançável.
- [ ] Abort com efeitos aplicados acciona compensação ou declara explicitamente a sua ausência (span + WORM).
- [ ] Teste de composição pela cadeia real.

### Estado
**ABERTO.**

---

## AOS-255 — Declaração tool-only/token-only (texto)

### Contexto
Desafio A1 (D-A1.1/D-A1.2, recomendações): um orçamento que cubra tool calls sem cobrir o turno de modelo é uma capacidade-fantasma **se o banner não o disser**. A v1 é TOOL-ONLY e TOKEN-ONLY — declarado, não fingido.

### Objectivo
Fixar o texto do banner e da doc **antes** de qualquer wiring de budget.

### Critérios de Aceitação
- [ ] Texto aprovado: «orçamento: cobre tool calls em TOKENS; o gasto de inferência é travado por tempo (wall-clock), não por tecto».
- [ ] `deploy/node/README.md` e o relatório de prontidão referem a declaração.

### Estado
**ABERTO.**

---

## AOS-256 — Ciclo de vida do nó de orçamento por-run

### Contexto
Desafio A1 (risco 4): compor o hook sem registar o nó do run ⇒ `ErrUnknownNode` ⇒ **100% das tool calls negadas**. O seam por-run já existe: `integration/secured.go:460` (`SecuredRuntime.Run`, já com `Freeze`/`defer Release` sobre `goal.RunID`).

### Objectivo
`AddNode(goal.RunID, treeID, limite)` no início do run e libertação no fim, no seam existente.

### Critérios de Aceitação
- [ ] Cada run tem nó de orçamento registado antes do primeiro turno; release garantido (incl. panic/erro).
- [ ] Limite vem de config (env de AOS-257) com default declarado.
- [ ] Teste: dois runs concorrentes não partilham tecto (declarar na doc: tecto por-run, nunca por-mandato — D-A1.3).

### Estado
**ABERTO.**

---

## AOS-257 — `BudgetCheck` no lugar do stub + `Settle` decorator + envs (tokens)

### Contexto
O plano original foi corrigido pelo desafio A1: o `Settle` vive num **decorator do `ActivityDispatcher`** (padrão de `secured.go:399-401`), cobrindo também o caminho de erro (`runtime_ports.go:293-294`); as fugas reais são negações a jusante e erros, não «permit sem Commit».

### Objectivo
Substituir `BudgetStub{}` (`secured.go:324`) pelo `BudgetCheck` real, com o ciclo de vida completo e envs token-only.

### Critérios de Aceitação
- [ ] `SecuredConfig` ganha campo de orçamento; o hook é composto quando configurado, stub declarado no banner quando não.
- [ ] `Settle` no decorator: commit em permit, release em deny/escalate/erro — teste de não-fuga após deny do egress e após erro.
- [ ] `AOS_BUDGET_MAX_TOKENS` (e equivalentes) na tabela AOS-203, fail-closed em valor inválido.
- [ ] Dependente de AOS-256 (sem nó por-run, nega tudo — ordem obrigatória).

### Estado
**ABERTO.**

---

## AOS-258 — Estimador real via `CallContext` + teste de nó com permit

### Contexto
Desafio A1 (esforço 9): `rm.Call` não transporta prompt/tokens — `WithEstimator` sozinho não chega. Alternativa mais barata: estimar fora do RM e passar por `CallContext`. E falta o teste que prove um **permit** com budget ligado (não só denies in-process).

### Objectivo
Estimador baseado no input materializado, injectado pela seam existente; prova não-vacuosa ponta a ponta.

### Critérios de Aceitação
- [ ] Estimador real composto (documentado o que estima e o que não estima).
- [ ] **Teste de nó:** run dentro do tecto obtém `permit` e a tool executa; run além do tecto é negado com `denied_by=budget` selado e atribuído.
- [ ] O `DefaultEstimator` deixa de ser usado em produção (ou é declarado no banner).

### Estado
**ABERTO.**

---

## AOS-259 — Canal de custo ponta a ponta (contrato `port` + dedup por parentesco)

### Contexto
Desafios A1 (risco 2) e A2 (achado E): `port.Usage`/`ChatResponse` não têm campo de custo (o RT soma zeros); e ligar `WithCost` cria dois spans `chat` por trace somados sem dedup ⇒ tokens a 2×. **Pré-requisito da primeira env em $.** Dependente da decisão D2.

### Objectivo
Campo de custo no contrato, preenchido no adaptador, agregação deduplicada por parentesco, `Cost` em `ProductionConfig` + `WithCost` no wiring.

### Critérios de Aceitação
- [ ] `CostMicroUSD` flui do GW para o RT/`TurnRecord`/span.
- [ ] `AggregateByTrace` deduplica por parentesco (ou o span do RT é suprimido) — teste prova tokens 1× com custo real.
- [ ] Burn-down lê custo real (fonte de AOS-262).

### Estado
**ABERTO.**

---

## AOS-260 — Admissão do turno de modelo

### Contexto
Desafio A1 (risco 1, CONFIRMADO/alta): a chamada ao modelo é directa (`loop.go:549`), fora da cadeia — a linha de custo dominante sem admission control. Dependente de D1 (opção B) e de AOS-259.

### Objectivo
Reservar antes do turno de modelo e saldar com o usage/custo real da resposta.

### Critérios de Aceitação
- [ ] Porta nova em `agent-runtime`: reserva antes de `loop.go:549`, settle com `resp.Usage`/`CostMicroUSD`.
- [ ] Esgotamento ⇒ degradação declarada (não deny-loop cego — liga a AOS-262).
- [ ] Replay não re-reserva (dedup por `run_id:step_id`).

### Estado
**ABERTO.**

---

## AOS-261 — Retentor de spans por-run + resolvedores de identificador

### Contexto
Desafio A2 (achados C/F): `Evaluate` recebe spans como parâmetro e nada no nó os produz/retém (NoopTracer por omissão; `SpanTracer` dispara-e-esquece) — superfície verde a mentir. E o nó não resolve `runID→traceID` nem `runID→treeID`; a retoma cria um trace novo por incarnação. Alternativa preferível: `BurndownSource` sobre `(runID, turn)` a partir do ledger de turnos (imune à re-emissão).

### Objectivo
Decorador de `Exporter` que retém `SpanData` com política de retenção (ou a fonte por ledger) + resolvedores explícitos com política multi-incarnação.

### Critérios de Aceitação
- [ ] A fonte de burn-down devolve dados reais com OTLP ligado e **erro explícito** (não zero silencioso) sem tracer.
- [ ] Política documentada para runs multi-incarnação (prefixo T1 vs reprodução T2).
- [ ] Testes: retenção, query por trace/run, e o caso de retoma.

### Estado
**ABERTO.**

---

## AOS-262 — Burn-down + aviso de exaustão no nó (primeira entrega: sem decisão)

### Contexto
Desafio A2 (plano revisto, passos 6-7): primeira entrega é **só burn-down + aviso** — sem `extend`/`summarize_stop`/`abort` (que não têm executor nem autoridade). O gancho de fim-de-turno já existe (`loop.go:469-518`, padrão `WithSteerSource`/`WithLivenessBreaker`).

### Objectivo
`Evaluate` na fronteira de fim-de-turno com as duas portas de leitura (`BudgetReader`, `ProgressReflector`), aviso emitido a ~80%, env fail-closed.

### Critérios de Aceitação
- [ ] Adaptadores node-local das portas de leitura (scheduler-free — sem violar `boundary_orq_sch_test.go`); `ProgressSnapshot.Step` ou tem produtor ou o campo é declarado vazio.
- [ ] `AOS_PROGRESS_THRESHOLD` recusa arrancar com valor inválido (padrão `ErrBadBreakerThresholds`), não o fallback silencioso de `WithThreshold`.
- [ ] Span de aviso emitido uma vez por run (latch); visível no canal de leitura existente.
- [ ] As opções de decisão NÃO são apresentadas nesta entrega.

### Estado
**ABERTO.**

---

## AOS-263 — Prompt de exaustão durável (2.º tipo de `PendingRecord`)

### Contexto
Desafio A2 (decisões D4/D5/D6): o desenho-alvo reutiliza a maquinaria HITL — pendente durável de segundo tipo (sem preview; amarrado a run+limiar+montante), suspensão `waiting_on_human`, rota autenticada, registo WORM com principal. `extend` exige autoridade (piso: paridade com `pause`) e um dono do tecto (o `budget.Budget` não tem mutador — ticket próprio). Bloqueado pelas decisões do dono.

### Objectivo
Implementar o prompt de exaustão como cidadão do plano de controlo, não como segundo mecanismo mais fraco.

### Critérios de Aceitação
- [ ] `PendingRecord` generalizado; o prompt aparece em `GET /runs/{id}`; TTL varrido pelo sweeper.
- [ ] `extend` exige assinatura de operador registado (piso D5) e escreve registo WORM próprio (principal, run, montante, razão).
- [ ] `abort` adaptado sobre a pausa graciosa durável; `summarize_stop` advisory declarado como tal (ou modo de terminação real).
- [ ] Suspensão repõe `enteredAt` — a deliberação não morre pelo wall-clock.

### Estado
**ABERTO** (bloqueado por D4/D5/D6).

---

## AOS-264 — Broker: passo zero de política/identidade + cliente Vault real + envs

### Contexto
Desafio A3: ligar o broker sem passo zero ⇒ troca negada por identidade e default-deny do Cedar ⇒ **nenhum turno de modelo executa** (risco novo, hoje inexistente). O cliente KEK existente é Transit (recusa devolver material) — transporte reaproveitável por cópia. D7 recomenda cliente/token **separados** (`AOS_BROKER_VAULT_*`); D8 recomenda v1 **in-process**.

### Objectivo
Preparar a troca mediada: capability nomeada (ou reutilização declarada de `cap:http.post` já assinada), `Principal` real no ponto de aquisição, cliente Vault real dentro de `internal/vault/`.

### Critérios de Aceitação
- [ ] A capability da troca está no bundle assinado (ou a reutilização está declarada e testada); o pipeline do GW passa `WithRun`/`WithPrincipal` (dados já existem no call site).
- [ ] Cliente Vault real (KV v2 vs dynamic secrets decidido e registado — só dynamic dá corte downstream) com `Secret` construído dentro do pacote.
- [ ] `AOS_BROKER_VAULT_*` na tabela AOS-203; banner declara o modo.
- [ ] Higiene pré-wiring: reaper de leases (molde `approval_sweeper.go`) e superfície para `Revoke`.

### Estado
**ABERTO** (bloqueado por D7/D8).

---

## AOS-265 — Broker: porta de aquisição com contexto + consumo in-process (v1)

### Contexto
Desafio A3 (estimativas corrigidas): `CredentialProvider.Fetch` devolve `string` e não transporta identidade — não é adaptação, é mudança de assinatura; o que se liga é um adaptador que **consome** o broker. A invariante rainha (segredo nunca observável **pelo agente**) mantém-se; a garantia do processo passa a ser redacção — declarado.

### Objectivo
Alargar a porta com contexto de chamada e ligar o broker ao ponto de aquisição do GW (in-process), com audit de governação no store durável.

### Critérios de Aceitação
- [ ] `Fetch` (ou porta nova) transporta principal/run; `recordExchange` distingue runs.
- [ ] Troca negada (identidade/política) ⇒ falha ruidosa atribuída, nunca bearer vazio.
- [ ] O `Audit` do GW aponta ao store durável (hoje `audit.NewMemStore()`).
- [ ] Teste de composição pela cadeia real; injecção no executor remoto fica **declaradamente deferida** (desenho-alvo: handle opaco até ao orchestrator, D8-B).

### Estado
**ABERTO** (bloqueado por AOS-264).

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
**ABERTO.**

---

## AOS-267 — Scheduler interno de retenção

### Contexto
Grupo B (análise 08-08): a expiração TTL só corre sob `POST /dsar/expire` — mesmo com a política definida, nada expira sem cron externo autenticado. Risco RGPD silencioso (*storage limitation*).

### Objectivo
Ticker interno no loop de serviço (molde do sweeper de aprovações) com credencial de governação em nome próprio — ou decisão documentada de manter externo.

### Critérios de Aceitação
- [ ] A política `AOS_RETENTION_*` definida ⇒ expiração corre periodicamente sem acção externa.
- [ ] Credencial em nome próprio com selo WORM por varrimento (quem/o quê), respeitando legal hold.
- [ ] Env de cadência com default declarado; fail-closed em valor inválido.

### Estado
**ABERTO.**

---

## AOS-268 — Verificação ancorada do WORM (checkpoint assinado no restart)

### Contexto
**Grupo C.** A re-verificação detecta mutação/inserção/remoção-interna, não a **truncatura do tail** nem reescrita desde a génese. `Signer`/`VerifyFromCheckpointAtHead` existem e estão testados (único consumidor: `platform/dr`); nenhum ticket/DEF possui o wiring no nó. Fonte: análise 08-08 (frete AOS-221/072).

### Objectivo
No restart, verificar contra o último checkpoint assinado com `expectedHead` persistido; a selagem periódica é out-of-process (custódia D4/AOS-156).

### Critérios de Aceitação
- [ ] Envs no molde `AOS_POLICY_TRUST_ANCHOR` (pubkey out-of-band + caminho do checkpoint); ausentes ⇒ comportamento actual declarado no banner.
- [ ] Tail truncado ou cadeia re-escrita ⇒ arranque aborta com erro nomeado.
- [ ] Teste: truncatura do WAL detectada; checkpoint forjado rejeitado.
- [ ] DEF registado para a selagem out-of-process (dono: custódia de chave).

### Estado
**ABERTO** (depende de D4/AOS-156 para a selagem).

---

## AOS-269 — ADR-021: scoring determinístico no Model Gateway

### Contexto
ADR-021 **assumido aprovado**: o router lexicográfico (AOS-059) passa a scoring ponderado determinístico **após** as guardas estruturais (soberania/allowlist/capacidade nunca são factores); factores como portas injectáveis em aritmética inteira/ponto-fixo (zero floats no data-plane); pesos como artefacto comportamental assinado (SemVer + eval-gate, ADR-012); calibração **offline**, nunca bandit/online (replay intacto, ADR-010).

### Objectivo
Implementar o estágio de scoring no GW com tabela de pesos assinada e sinal de *task-fit* dos evals (fecha o loop EPIC-08 → EPIC-06).

### Critérios de Aceitação
- [ ] Portas de factores (health, headroom, custo, latência, task-fit, estabilidade) com impls de referência determinísticas; aritmética inteira.
- [ ] Tabela de pesos (perfis nomeados) carregada fail-closed — sem tabela válida/assinada, o router recusa.
- [ ] Guard-test: a decisão é função pura dos inputs (sem `rand`/relógio); cenário AOS-063 alargado prova que nenhum peso elege candidato cross-border ou fora da allowlist.
- [ ] Span `model_routing`/`DecisionSink` registam perfil, factores e score; `model_swap` inclui a razão de scoring.
- [ ] Formato da tabela e pesos iniciais documentados em `tecnica/06`; cobertura ≥ `ROUTING_COVERAGE_MIN`.

### Estado
**ABERTO.**

---

## AOS-270 — ADR-022: arestas condicionais declarativas no PlanDocument

### Contexto
ADR-022 **assumido aprovado**: o `Node` admite arestas com condição (subconjunto fechado do schema, sem código arbitrário), avaliada deterministicamente sobre o resultado registado — nunca pelo LLM em runtime. Uma aresta condicional **nunca fecha ciclo**: o ramo de reprovação aponta para nós ainda não executados; o retorno a executados continua a ser replan de subgrafo (AOS-239).

### Objectivo
Estender o schema e o despacho com arestas condicionais, preservando aciclicidade, replay e orçamento.

### Critérios de Aceitação
- [ ] Schema fechado (`DisallowUnknownFields`) com o campo de condição; validador puro (AOS-231) rejeita condicional que feche ciclo (reusa a aciclicidade incremental).
- [ ] `plandispatch` avalia a condição como função pura do resultado registado; evento de decisão de ramo emitido; replay reproduz o ramo sem re-avaliação.
- [ ] Avaliação de condições debita orçamento da árvore (ADR-008).
- [ ] Adversarial: «ciclo disfarçado de condicional» rejeitado (AOS-244 alargado).

### Estado
**ABERTO.**

---

## AOS-271 — ADR-022: `role: verifier` com semântica de sistema

### Contexto
ADR-022 §2.2: o verificador deixa de ser rótulo — read-only por construção (NHI sem tools de efeito), produtor ≠ verificador (o validador rejeita auto-verificação na sub-árvore), veredicto estruturado como evento — o único resultado que as condições de qualidade (AOS-270) consomem. Verificar debita orçamento como qualquer nó.

### Objectivo
Materializar o papel verificador com enforcement pelo sistema, não por convenção.

### Critérios de Aceitação
- [ ] `planmaterialize` emite a NHI do verificador sem autoridade de efeito; o RM/RiskGate é a segunda linha, fail-closed.
- [ ] Validador rejeita verificador da própria sub-árvore de delegação.
- [ ] Veredicto tipado (`pass/fail + razões + métricas`) registado como evento `aos.planner.v1`.
- [ ] Adversarial: verificador auto-referente rejeitado.

### Estado
**ABERTO.**

---

## AOS-272 — ADR-022: payload tipado por aresta

### Contexto
ADR-022 §2.3: nós declaram `outputs`, arestas declaram `consumes` — contratos (nome, schema, taint) validados estaticamente; o transporte é **referência** a registo no Event Store/MEM com proveniência («contexto ≠ registo»), nunca blackboard mutável.

### Objectivo
Validação estática de contratos entre nós e propagação explícita de taint pelas arestas.

### Critérios de Aceitação
- [ ] O validador rejeita `consumes` inexistente, de tipo incompatível, ou com taint incompatível com a autoridade do consumidor (ADR-005).
- [ ] O consumidor recebe referência/resumo, não o histórico bruto; proveniência preservada.
- [ ] Adversarial: payload com taint elevado para consumidor privilegiado rejeitado.

### Estado
**ABERTO.**

---

## AOS-273 — ADR-022: `plan_version` bump, migração e golden-sets

### Contexto
ADR-022 §4: o schema cresce — nova `plan_version`, migração via `planmigrate` (AOS-243); golden-sets do planeador (AOS-241) e cenários adversariais (AOS-244) têm de cobrir as extensões.

### Objectivo
Tornar as extensões um artefacto comportamental versionado, com janela de suporte e eval-gate.

### Critérios de Aceitação
- [ ] `plan_version` incrementada; migração da versão anterior testada; rejeição de MAJOR incompatível.
- [ ] Golden-sets com planos condicionais/verificador/payload passam no eval-gate.
- [ ] Janela de suporte documentada; replay de planos antigos reproduz-se byte-a-byte.

### Estado
**ABERTO.**

---

## AOS-274 — Produtor de SLOs/alertas em runtime

### Contexto
**F8 (média).** `EvaluateAlerts`/`BuildDashboard`/`EvaluateOperationalAlerts` (AOS-085/086) só correm em testes; os runbooks mapeiam alertas que ninguém produz; o export OTLP é só traces. Fonte: análise 08-08.

### Objectivo
Loop avaliador periódico no nó: constrói `WideEvent`s a partir dos spans/WORM, avalia alertas, expõe o resultado.

### Critérios de Aceitação
- [ ] Avaliador composto no loop de serviço (molde sweeper); alertas disparam sobre dados reais.
- [ ] Resultado exposto (endpoint/span/log estruturado) e ligado ao registo de runbooks (AOS-106).
- [ ] Fail-open declarado (a observabilidade nunca derruba o nó); `otel-genai/doc.go` corrigido (o «DIFERIDO» do exporter fechou em AOS-173).

### Estado
**ABERTO.**

---

## AOS-275 — Promotion controller: endpoint `POST /promote` autenticado

### Contexto
**F7 (média).** O controller é composto sempre (AOS-206) mas **não há endpoint nem CLI** — `Promote` só in-process (deferido para AOS-096, `bootstrap.go:1395`). Com ratificadores definidos, um operador não consegue promover nada. Fonte: análise 08-08.

### Objectivo
Superfície de submissão de ratificação no molde de `/approve` (admissão do plano de controlo + assinatura + frescura + nonce durável).

### Critérios de Aceitação
- [ ] `POST /promote` (ou subcomando CLI) autenticado; ratificação registada no WORM com principal.
- [ ] Anti-replay (`ratification_replayed`) provado pela rota externa, não só in-process.
- [ ] Banner deixa de dizer «deferido» quando a rota existir.

### Estado
**ABERTO** (depende de AOS-096 para candidatos reais; a rota pode anteceder).

---

## Mapa de dependências desta epic

```
P0 (risco activo):  AOS-245 · AOS-246 · AOS-247 · AOS-248 · AOS-251 · AOS-252 · AOS-255
P1:                 AOS-249 · AOS-250 · AOS-253 (dep. 252) · AOS-256 → AOS-257 → AOS-258
                    AOS-261 → AOS-262 · AOS-266 · AOS-269
P2 (c/ decisão):    AOS-254 (dep. 252) · AOS-259 (D2) → AOS-260 (D1) · AOS-263 (D4/D5/D6)
                    AOS-264 (D7/D8) → AOS-265 · AOS-267 · AOS-268 (D4/AOS-156)
                    AOS-270 → AOS-271/272 → AOS-273 · AOS-274 · AOS-275 (AOS-096)
```

Cadeia causal registada no relatório §8: **F1 → F2 → F3 → F4 → F5** primeiro — fechar wiring novo (budget, exaustão) sobre esta base partida replica o padrão «capacidade verde, efeito zero».
