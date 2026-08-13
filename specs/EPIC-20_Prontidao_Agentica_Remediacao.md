# EPIC-20 — Prontidão Agêntica: remediação de achados, custo governado e extensões ADR-021/022

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Remediação dos achados de prontidão (F1–F16), billing token-only ligado ao nó, e implementação dos ADR-021/022 |
| Versão | 1.0 |
| Data | 2026-08-08 |
| Classificação | Documento de Referência — **Em execução** (27/34 implementados e no trunk, 1 parcial; 7 abertos — 5 DESBLOQUEADOS pela ratificação dos ADR-021/022 em 2026-08-13, 2 à espera de D1/D2 — ver **§0 Balanço**) |
| Documento-fonte | **`docs/reports/prontidao-modelos-agenticos.md`** (relatório consolidado, 2026-08-08) |
| ADRs aprovados (RATIFICADOS 2026-08-13) | **ADR-021** (scoring determinístico no GW) · **ADR-022** (arestas condicionais, papel verificador, payload tipado) — ambos `Estado: Aceite` por **ratificação de dono**; antes eram premissa de planeamento (ver §0) |
| Documentos relacionados | `docs/reports/desafio-A1..A4-*.md` (planos de fecho corrigidos + decisões do dono), ADR-008/010/011/012/013/018/020, `tecnica/06`, `tecnica/18` |

---

## 0. Balanço de execução — 2026-08-13 (ADR-021/022 ratificados)

**Estado global: 27 dos 34 tickets IMPLEMENTADOS e integrados no trunk `feature/AOS-128-ux-dx-tests` (1 deles PARCIAL, com eixo); 7 ABERTOS — dos quais 5 DESBLOQUEADOS em 2026-08-13.** O eixo **D4** (autoridade de identidade real) está **fechado no código**. A **ratificação de dono do ADR-021 e do ADR-022** (2026-08-13) desbloqueou **AOS-269..273**, que passam a ser trabalho executável. Ficam à espera de decisão apenas **AOS-259/260** (eixo $, D1/D2).

### Implementados (27, um deles PARCIAL)
- **Risco activo + higiene (F1–F6, F11–F15):** AOS-245 · AOS-246 · AOS-247 · AOS-248 · AOS-249 · AOS-250 · AOS-251 · AOS-252 · AOS-255.
- **Durabilidade do run:** AOS-253 *(crash-resume: varredura de órfãos no arranque + `Resumer` composto; efeito já aplicado não repete, modelo não re-interrogado)* · **AOS-254 — PARCIAL** *(a CONDUÇÃO da saga está composta e o ramo alcançável — declarar a ausência com selo WORM — provado; a COMPENSAÇÃO REAL fica em **DEF-270**, à espera de um produtor de compensações e do registo run-scoped no kernel)*.
- **Billing token-only (dimensão de tokens):** AOS-256 · AOS-257 · AOS-258.
- **Burn-down + exaustão:** AOS-261 · AOS-262 · AOS-263 *(prompt durável; cutover das 3 decisões do dono — extend fora, reúso HITL, paridade com a pausa)*.
- **Broker de credenciais:** AOS-264 · AOS-265 *(consumo in-process v1)*.
- **Operações do nó:** AOS-266 *(attestation: challenge freshness + device enrollment)* · AOS-267 *(scheduler de retenção)* · AOS-274 *(SLOs)* · AOS-275 *(promoção)* · AOS-276 *(keypool)* · AOS-277 *(ingresso)*.
- **Eixo D4:** AOS-268 *(verificação ancorada do WORM no restart)* · AOS-278 *(cutover duro da identidade do GW)*.

### Abertos (7) — 5 DESBLOQUEADOS, 2 à espera de decisão
- **DESBLOQUEADOS (2026-08-13) — os ADR foram RATIFICADOS:** AOS-269 *(ADR-021, scoring determinístico)* · AOS-270 · AOS-271 · AOS-272 · AOS-273 *(ADR-022: PlanDocument, `role: verifier`, payload tipado, migração)*. Zero código ainda, mas **executáveis**: `ADR-021` e `ADR-022` passaram a **`Estado: Aceite` (ratificação de dono, 2026-08-13)**, pelo que o desenho está congelado e a implementação deixa de comprometer decisão não-tomada. A gramática concreta (perfis/pesos em `tecnica/06`; condições/veredicto/payload em `tecnica/18`) permanece **fora do ADR por desenho** — é trabalho destes tickets, não decisão re-aberta.
- **AINDA bloqueados por decisão do dono — eixo $ do billing:** AOS-259 *(D2)* · AOS-260 *(D1)*. A dimensão de **tokens** está ligada (AOS-256..258); a de **dólares** aguarda D1/D2.

### Deferidos com eixo (infra-org, fora do código do nó)
Selagem periódica out-of-process dos checkpoints do WORM (**DEF-268/DEF-269**, custódia de chave D4/AOS-156) · instância do IdP tenant + adaptador HSM/KMS (costura `crypto.Signer`, EPIC-16) · injecção do broker no executor remoto (**D8-B**) · compensação real da saga (**DEF-270**: exige um *produtor* de compensações — o loop nunca popula `activity.Activity.Compensation` — **e** o registo run-scoped no kernel; enquanto faltarem, habilitá-la compensaria o run errado).

### Nota de reconciliação
Os campos `### Estado` por-ticket de **AOS-245, AOS-250 e AOS-266** estavam desactualizados — descreviam a não-entrada na W0 (245/250) ou o estado inicial (266), mas a implementação e os testes dedicados existem e passam (`aos245_ledger_titular_test.go`, `apiMaxTurnsOptionFromEnv`, `aos266_challenge_freshness_test.go`/`aos266_device_enrollment_test.go`). Corrigidos na revisão de 2026-08-12.

**Sobre o «assumidos aprovados» do cabeçalho (ADR-021/022) — RESOLVIDO em 2026-08-13.** A linha «ADRs assumidos aprovados» descrevia a *premissa de planeamento* da epic, não o estado real: ambos os documentos estavam em `Estado: Proposto — pendente de ratificação`, e enquanto assim fosse AOS-269..273 não podiam entrar (Carta §6: ratificar **antes** de implementar, não depois). O dono **ratificou ambos em 2026-08-13** (`Estado: Aceite`, ratificação de dono) — a premissa do cabeçalho passou a ser verdade e a frente ficou executável.

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
| Broker de credenciais | Passo zero de política/identidade, cliente Vault, porta com contexto | **D7/D8 DECIDIDOS 2026-08-10 (dono):** D7 = cliente/token Vault SEPARADOS (`AOS_BROKER_VAULT_*`); D8 = consumo v1 IN-PROCESS (injecção remota deferida, D8-B) |
| Verificação ancorada do WORM | Env trust anchor + `VerifyFromCheckpointAtHead` no restart | **D4/AOS-156** — custódia da chave do operador (infra-org) |
| ADR-021 / ADR-022 | Toda a implementação | Gramática concreta de perfis/condições/payloads → `tecnica/06`/`tecnica/18` |
| ORQ/SCH distribuídos | **Nada** — deferimento ADR-018 mantido | EPIC-10 (frota multi-nó) |

## 3. Critérios de Saída do Epic

- [ ] Nenhum output de tool call é persistido em claro: o step-ledger sela por-titular como o capturer (AOS-245), e o shred/expire apaga ambos (prova: erase → `ErrDecrypt` nos dois registos).
- [x] O breaker **dispara** no run comum: teste de nó repete a mesma call negada e assere trip **antes** de `MaxTurns` (AOS-251); ligar velocidades sem fonte **aborta o arranque** (AOS-246).
- [ ] O log durável distingue desfecho de crash: `complete`/`failed` escritos em todos os caminhos; `CheckDeadlines` com caller periódico **que interrompe o run** (AOS-252 — escrito e a correr; falta o teste crash-simulado vs fim-normal, ver AOS-252 CA3).
- [x] Um crash a meio de um run é retomado por varredura no arranque, sem re-executar efeitos (AOS-253). `NodeService.ResumeInterruptedRuns` (ligada em `main.go` entre `NewNodeService` e `Serve`) compõe o `durable.Resumer` (AOS-015, nunca antes lido), varre os streams em `running` (rasto de crash de AOS-252), reclama o lease via `worker.Assigner` (sem roubo) e retoma pelo replay-then-continue de AOS-021 (`RebuildLedger` + plano de replay); prova de não-double-execution em `aos253_crash_resume_test.go`.
- [x] Um run com `AOS_BUDGET_MAX_TOKENS` definido é **negado por orçamento** com o deny selado e atribuído, e um run dentro do tecto obtém **permit** — ambos ao nível do nó (AOS-256..258). Prova em `packages/cmd/aos/aos258_budget_permit_node_test.go` (`Bootstrap` real, tecto pela env do operador; permit **com a tool a executar**, deny com `denied_by=budget` + hash-chain verificada, e o mesmo run a permitir e depois negar).
- [x] O banner declara budget/broker/modelo/autonomia (AOS-248) — postura anunciada = postura ligada.
- [x] Burn-down visível no run real com aviso a ~80% (AOS-261/262) — fonte no **ledger de turnos** (não spans, que ninguém retinha), aviso **uma vez por run** no **log do nó** (canal que existe sempre; o span `aos.control.budget_warning` exige `AOS_OTLP_ENDPOINT`, sem a qual o tracer é o `NoopTracer`), e **erro explícito** em vez de 0% silencioso quando não há fonte **ou quando o ledger tem turnos mas somou zero tokens** (`ErrBurndownNoUsage`).
- [x] ADR-021: o router ordena por scoring determinístico com tabela de pesos assinada; guard-test prova função pura; nenhum peso elege candidato cross-border (AOS-269). — `routing/scoring` + `policy/weights` (assinada, trust anchor pinado) + `router.WithScoring` (opt-in declarado); cenários 6–8 e 2 meta-testes novos no gate `ci-routing`.
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
| AOS-250 | Clamp de `MaxTurns` no submit (`AOS_MAX_TURNS`) | fix | S | **P0** | — | F15 |
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
| AOS-276 | Keypool do GW: fusível RPM de 120 chamadas/vida — janela real **ou** tecto declarado no LiteLLM | fix | S | **P0** | D11 | F17 |
| AOS-277 | Knobs de ingresso por env (token-bucket + tecto in-flight) + teste do 429 | feature | S | P1 | — | A5-passo 2 |
| AOS-278 | Estágio de identidade real do GW (substituir o stub `nodeModelAuthn`) | feature | M | P2 | D4/EPIC-16 | F18 |

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

**O que NÃO estima**, declarado no banner e no README: o **turno de modelo** (invocado fora da
cadeia; nenhuma reserva o admite — AOS-260), o **resultado da tool** (volta à transcrição mas só
é mensurável DEPOIS do efeito; o saldo confirma a RESERVA, não a medição — AOS-259) e **dólares**
(dimensão `CostMicroUSD` a zero — AOS-259). O alcance declarado em AOS-255 NÃO foi alargado.

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
**DESBLOQUEADO — decisões do dono registadas (2026-08-12).** As três decisões que o
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
- [ ] Cliente Vault real (KV v2 vs dynamic secrets decidido e registado — só dynamic dá corte downstream) com `Secret` construído dentro do pacote.
- [ ] `AOS_BROKER_VAULT_*` na tabela AOS-203; banner declara o modo.
- [ ] Higiene pré-wiring: reaper de leases (molde `approval_sweeper.go`) e superfície para `Revoke`.

### Estado
**DESBLOQUEADO (D7/D8 decididos 2026-08-10: cliente Vault separado `AOS_BROKER_VAULT_*` + consumo in-process v1) — pronto para wave.**

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
- [x] Tabela de pesos (perfis nomeados) carregada fail-closed — sem tabela válida/assinada, o router recusa. — `policy/weights` no **molde exacto** de `policy/allowlist`: artefacto embebido + digest canónico + assinatura ed25519 + **trust anchor pinado em código**, com `ErrTableMalformed`/`ErrSignatureInvalid`/`ErrTrustAnchorMismatch`/`ErrPubKeyInvalid`; schema fechado e **SemVer validado** (ADR-012). `scoring.NewScorer` não se constrói sem tabela; um scorer não-armado faz o router **rejeitar** toda a rota (defesa em profundidade). ⚠️ **ALCANCE (não confundir com efeito em produção):** o fail-closed vale **quando o scoring está composto**. O `router.Router` **não está composto no pipeline do GW** (`NewProduction` usa `failover.NewStage`; `router.New` não tem chamador fora de testes) — lacuna **pré-existente** a este ticket, registada em **DEF-271** e fixada em alcance pela **emenda 1.1 do ADR-021** (§5-bis). Enquanto essa dívida não fechar, este critério é verdade sobre a máquina entregue, **não** sobre o caminho de produção.
- [x] Guard-test: a decisão é função pura dos inputs (sem `rand`/relógio); cenário AOS-063 alargado prova que nenhum peso elege candidato cross-border ou fora da allowlist. — `TestScenario7_Scoring_PureFunctionDeterministicNoFloats` (64 repetições idênticas + independência da ordem + **análise AST** que proíbe floats/`rand`/`time.Now` em `routing/scoring`, `routing/router`, `policy/weights`, `routing/tiering`, `routing/degradation`, com auto-verificação de não-vacuidade); `TestScenario6_Scoring_GuardsFirstNeverElectsCrossBorderOrNonAllowlisted` (pesos gananciosos a favor do cross-border e de um modelo fora da allowlist ⇒ REJECT/rota intra) + meta-teste `TestMetaDetects_ScoringElectsCrossBorderWhenGuardsBypassed`.
- [x] Span `model_routing`/`DecisionSink` registam perfil, factores e score; `model_swap` inclui a razão de scoring. — atributos `aos.routing.scored|score|score_profile|score_weights_version|score_factors|score_scale`; `Decision` transporta `Scored`/`ScoreProfile`/`WeightsVersion`/`Score`/`ScoreFactors`; a razão de scoring é **preservada** mesmo quando a degradação por orçamento reescreve a razão, e a cadeia real (tabela assinada → scorer → router → `routingstage` → Gateway) é provada em `routing_scoring_wiring_test.go` a ver a variância `model_swap` carregar perfil+versão+score+factores.
- [x] Formato da tabela e pesos iniciais documentados em `tecnica/06`; cobertura ≥ `ROUTING_COVERAGE_MIN`. — `tecnica/06` §6.1 (as cinco regras e onde estão impostas, tabela de campos do schema, pesos iniciais dos perfis `balanced`/`fast`/`cheap`/`quality`, postura de compatibilidade, observabilidade), §8.4 (cenários 6–8) e §9 (três riscos novos). Cobertura do módulo GW: **88,2 %** (piso 80).

### Postura de compatibilidade (decisão declarada)
O scoring é **opt-in por composição** (`router.WithScoring`). Sem essa opção o router mantém AOS-059 **byte-a-byte** — um nó já implantado, sem tabela de pesos, não deixa de rotear (provado por `TestScenario8_Scoring_FailClosedWithoutSignedWeights` (D) e por `TestAOS269_Gateway_SemScoringMantemComportamento`). Com essa opção a tabela é **obrigatória** e a sua ausência/invalidez é uma **recusa** — a leitura da regra 3 do ADR-021 («sem tabela válida o router recusa») que se aplica *quando o scoring está composto*, sem impor uma tabela a quem não pediu scoring.

### Estado
**FEITO — a MÁQUINA; o WIRING fica em DEF-271.** Entregue: portas de factores em aritmética inteira, tabela de pesos embebida e assinada com carregamento fail-closed, guard-test AST (proíbe float/`rand`/relógio no caminho de decisão, com auto-verificação de não-vacuidade), cenário de soberania que prova que nenhum peso elege cross-border, e observabilidade (perfil/score/factores no span, `DecisionSink` e `model_swap`). Gate `scripts/ci/routing.sh` alargado: `REQUIRED` passa a 8 cenários + 10 meta-testes + relatório (novas entradas `scoring_guards_first`, `scoring_failclosed_weights`, `scoring_deterministic`, `scoring_compat_lexicographic` no `AOS_ROUTING_REPORT`).

⚠️ **SEM EFEITO EM PRODUÇÃO, e isso está declarado, não escondido.** O `router.Router` (AOS-059) sobre o qual este scoring assenta **nunca esteve composto no pipeline do gateway** — `NewProduction` compõe `failover.NewStage`, e `router.New` não tem chamador fora de testes em todo o repositório. É lacuna **pré-existente** a este ticket. Registada em **DEF-271** (com o ticket próprio que a fecha: compor `routingstage`+`router` no pipeline, com prova pela cadeia real) e fixada em alcance pela **emenda 1.1 do ADR-021** (§5-bis, 2026-08-13, autoridade de dono): na v1 o scoring é composto **por opção** e a regra 3 aplica-se **quando está composto**.

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
