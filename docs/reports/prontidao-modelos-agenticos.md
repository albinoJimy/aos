# Prontidão do AOS para modelos agênticos — relatório consolidado

> **Consolidado em:** 2026-08-08 · **Branch:** `feature/AOS-128-ux-dx-tests`
> **Natureza:** avaliação por evidência (execução ao vivo + verificação contra o código), não apenas documental.
>
> **Proveniência — quatro vagas, cruzadas neste documento:**
>
> 1. **2026-08-05 — avaliação viva** (imagem publicada, `demo-ciclo-completo`, caminho de permit).
> 2. **2026-08-06 — revisão adversarial** (5 lentes + cépticos; gaps 1 e 5 declarados resolvidos).
> 3. **2026-08-08 — sessão de execução** (ciclo de aprovação corrido ao vivo; gaps 2, 3, 5-bis fechados; 7+ commits) **+ análise aprofundada em 6 frentes** (inventário de costuras, N1–N7).
> 4. **2026-08-08 — desafios adversariais A1–A5** (13 agentes cada contra os planos de fecho: [A1](./desafio-A1-budget-admission-control.md), [A2](./desafio-A2-progress-surface.md), [A3](./desafio-A3-credential-broker.md), [A4](./desafio-A4-orquestrador.md), [A5](./desafio-A5-escalonador.md); achados N8–N13, F17–F18, correcções de plano). O A5 teve 2 dos 6 refutadores caídos — os seus dois achados altos foram **verificados à mão** pelo autor e re-classificados aqui como F15 (promovido) e F17.
>
> As vagas anteriores viviam como secções datadas com marcações cruzadas difíceis de seguir; esta consolidação substitui-as. Cada afirmação traz a fonte entre parênteses. Divergências entre avaliações estão em §9.

---

## 1. Veredicto executivo

**Arquitecturalmente sim; funcionalmente sim para efeitos locais governados, com ressalvas de segurança activas (F1–F4) e sem gestão de custo.**

O AOS contém modelos agênticos (nenhuma tool call fora da cadeia de mediação), deixa-os trabalhar em efeitos locais (autoridade derivada do token NHI; `cap:fs.read` executa ponta a ponta, incluindo em microVM real), e tem o circuito de escalada humana a funcionar ao vivo (escalar → suspender → four-eyes → retomar → executar, sem repetir efeitos). Os três problemas activos mais sérios **não são** os gaps originais — são defeitos encontrados pelas vagas 3–4: o step-ledger a persistir outputs de tools em claro (F1), o circuit breaker inerte no run comum (F2+F3) e a máquina de estados que nunca escreve o desfecho (F4).

| Cenário | Pronto? |
|---|---|
| Conter modelos agênticos (sandbox governada, auditoria total) | **Sim** — verificado ao vivo |
| Efeitos locais governados (ler/escrever em sandbox) | **Sim** — autoridade do token + `MediatedLauncher` em microVM Firecracker |
| Efeitos de **rede externa** originados pelo modelo | **Não — por desenho** (taint-gate: «untrusted não comanda»). A via sancionada existe: escalada → aprovação humana → reexecução pela cadeia inteira (o taint nunca muda) |
| Gestão de custo/billing dos modelos | **Não** — componentes verdes, nenhum ligado ao nó (§5, A1/A2) |
| Produção multi-tenant com soberania real | **Não** — tenant/IdP de soberania e HSM/KMS concreto são deferimentos com dono (D4/EPIC-16) |

---

## 2. Estado dos gaps originais (todos fechados)

| Gap (2026-08-05) | Estado | Prova |
|---|---|---|
| 1 · Nenhum efeito executa no deployment | ✅ **Fechado** (`303cf47`) | Autoridade de escopo derivada do token NHI verificado; `TestScopeTokenOnly_SemDirectorioExterno_ToolExecutes` + controlo negativo `TestScopeTokenOnly_CapForaDoTokenNegada`; ao vivo `run-fclive-2` (permit → microVM real) |
| 2 · Feedback de negação ao modelo | ✅ **Fechado** (`60f5d64` marcador no tail + `f0c2bd8` breaker cablado) | Mas ver **F3**: o breaker tem dois mecanismos de inércia encontrados depois |
| 3 · Circuito negação→aprovação humana | ✅ **Fechado e provado ao vivo** | `ApprovalGate` na cadeia (`aabf4eb`), oráculo reconhece a aprovação (`4214b3b`), suspensão/retoma durável (`bbd89b9`/`de014a9`/`1a04174`), replay fiel (`d676e04`/`1735d07`). Corrigiu 8 defeitos de composição que a suite verde não via |
| 4 · DEMO-GRADE declarados | ✅ Recalibrado | São deferimentos rastreáveis com dono e gatilho (DEF-201/212/213, D4/EPIC-16); o deployment endurecido já injecta Vault externo persistente |
| 5 · Labels OCI na imagem | ✅ **Fechado** | Imagem `bb0a8c00c4bf` com os 11 labels (desde `e23bcb6`). Residual real: atestação não anexada por OCI referrers (ADR-017 residual 1) |
| 5-bis · `AuthoritySource` externa | ✅ **Fechado** (`03afcbb`) | `AOS_AUTHORITY_FILE` liga um directório JSON montado; revogação provada ao vivo (mesmo token: allow → `deny\|scope`). Notas: revogar ≠ remover (ausente cai no token; revoga-se com `"capabilities": []`); o directório só pode restringir, nunca ampliar |
| Extra · audit não atribuía a negação | ✅ **Fechado** (`1b326d1` + `6c88a7a`) | WORM sela QUEM negou e PORQUÊ, com versão por-registo (236 partições reais re-verificadas); via de leitura `aos audit-trail` |

---

## 3. Evidência viva (o que foi executado, não só lido)

**Imagem publicada (2026-08-05).** `USER 65532:65532`, distroless, zero volumes, healthcheck por binário estático; arranque limpo sob `--read-only`; bind não-loopback sem operadores ⇒ `exit 1`; `POST /runs` sem headers soberanos ⇒ `403`; `AOS_MODE=production` sem requisitos ⇒ `exit 1`.

**Fluxo de negócio ponta a ponta** (`demo-ciclo-completo.sh`, run `run-ciclo-223212`): saúde ✅, identidade OIDC+NHI ✅, submissão com residência selada ✅ 201, toolset frozen ✅, leitura soberana ✅, anti-replay do Bearer ✅ (404), trajetória SSE ✅ (113 eventos), reconstrução ✅, auditoria WORM+OTLP ✅ (16 decisões + 18 selos de leitura). O run morreu em `MaxTurns` após 17 tool calls negadas por política — observação **anterior** a `303cf47`; hoje uma capability dentro do `Scope` do token obtém permit e executa.

**Caminho de permit, três níveis verdes** (cadeia `NewProductionSecure`, sem stubs): `TestDemo_PermitEndToEndInputOutput` (RM), `TestAOS169_Mediation_PermitPath_ToolExecutes` (nó), `TestDemo_PermitNodeEndToEnd` (runtime seguro; modelo propõe `doc_read`, executor corre 1×, output volta ao turno 2). Reforço: `TestDemo_SandboxNodeEndToEnd` — o mesmo permit com o efeito em sandbox real.

**Ciclo de aprovação ao vivo (2026-08-08):**

```
00:17:42  escalate  step-000001-tool-1            → run SUSPENSO (waiting_on_human)
          cerimónia four-eyes: alice+bob, 3 eixos distintos, attestation WebAuthn → grant durável
          retoma com credencial NHI FRESCA (a original nunca é persistida)
00:18:03  allow     step-000001-tool-1            ← atravessou a cadeia INTEIRA
00:18:03  [guest-agent] cmd="read" path="notes"   ← microVM Firecracker real
00:18:08  escalate  step-000002-tool-1            ← a acção seguinte exige a SUA aprovação
```

**Nota metodológica da sessão (a mais importante):** *um teste de composição que substitui a peça vizinha por um duplo NÃO é um teste de composição.* Os defeitos do ciclo de aprovação sobreviveram exactamente aí. A rede que ficou: `integration/approval_chain_real_test.go` + `cmd/aos/approval_cycle_node_test.go`, com âncoras de não-vacuidade.

---

## 4. O que está pronto (governança comprovada)

- **Mediação total** — nenhuma tool call fora do RM (ADR-002): identidade → revalidação de registry → PDP/Cedar (bundle assinado, trust anchor out-of-band) → taint → scope → egress; audit-before-effect no WORM.
- **Identidade agêntica real** — NHI ed25519 por agente, cadeia humano→agente, classes com allowlist de capabilities, TTL, modo trust-anchor-only.
- **«Untrusted não comanda»** — capabilities privilegiadas negadas a calls do modelo (verificado A/B); a aprovação humana remove UM obstáculo sem promover o taint.
- **Auditoria atribuída** — WORM com hash-chain verificada no arranque (AOS-221), selos com QUEM/PORQUÊ (`aos audit-trail`), spans OTLP GenAI, soberania de leitura com selo D6 e anti-replay.
- **Postura de produção fail-closed** — issuer externo, TLS, OIDC de soberania, board regions, KEK durável: todos exigidos e verificados.
- **Ciclo humano** — four-eyes com attestation, suspensão/retoma durável, replay fiel da trajectória.

---

## 5. Inventário consolidado — capacidades sem efeito no deployment

Construídas, testadas, verdes — e sem efeito. Três grupos, por tipo de remédio.

### Grupo A — Sem costura nenhuma (exige código novo; nenhum env liga)

| Componente | Ticket | Estado exacto | Fecho (corrigido pelos desafios) |
|---|---|---|---|
| **budget / admission control** | AOS-008 | `BudgetStub{}` (allow-incondicional) na cadeia, `integration/secured.go:324`; `SecuredConfig` sem campo; nenhuma env; **banner mudo** | Ver §7 — o plano original foi **corrigido**: cobre tool calls mas não o turno de modelo (a chamada ao modelo é directa, `loop.go:549`); exige `AddNode` por-run no seam `secured.go:460` e `Settle` como decorator do `ActivityDispatcher` |
| **progress-surface** (burn-down + exaustão) | AOS-123 | Zero consumidores de produção (só QA); nenhuma env | Ver §7 — correcções pesadas: só `extend` tem porta; o «timeout» não existe (sem relógio); sem spans retidos lê 0 **sem erro**; «decisão via SSE» é impossível |
| **Credential Broker (BRK)** | AOS-070 | Zero imports não-teste fora do módulo; credenciais entram por ficheiro; nenhuma env, nenhuma linha de banner | Ver §7 — o risco foi **reatribuído** (bearer nó→LiteLLM, não a chave do provider); exige passo zero de política/identidade antes do wiring |
| **Orquestrador (ORQ)** | EPIC-03/18/19 | Proibido por teste (`boundary_orq_sch_test.go`, directo+transitivo); deferimento **honesto** (ADR-018) | Para a v1: **nada**. Rota in-run é v1-legal mas grande (15 interfaces + `Decomposer` inexistente + `agent.plan` no catálogo assinado) |
| **Escalonador (SCH)** | EPIC-03 | Proibido pelo mesmo guarda. *(Correcções do desafio A5:)* o `tieradapter` do GW não está «por ligar» — é **código morto interdito** (zero importadores nos cinco binários; compô-lo arrastaria o SCH para o grafo do nó e reprovaria o guarda); o «sem backpressure» era **errado** — o ingresso tem token-bucket + tecto de runs em voo (429), o que falta são os knobs por env; e **filas (AOS-030) e prioridade (AOS-032) são single-node** — só routing/escala dependem de frota | Subconjunto com valor single-node: admission/budget como colaborador do run por **adaptador node-local** (a emenda ao guarda não é exequível sem quebrar o zero-dep — D12); filas+prioridade (**médio**, single-node); routing/escala (**grande**, EPIC-10) |
| **Attestation: portas `ChallengeIssuance` + `DeviceEnrollment`** | AOS-177 | Implementadas e testadas em `integration/`; **zero ocorrências em `cmd/aos`** | Sem elas a attestation prova posse mas **não liveness** (replay possível) nem atribuição dispositivo↔aprovador. Wiring + `AOS_DEVICE_ENROLLMENT_FILE` (**pequeno-médio**) + linha de banner |

*Nota:* o **verificador** de attestation sai do Grupo A — liga-se por `AOS_ATTESTATION_VERIFIER_URL` (é Grupo B). E atenção: o wiring inteiro está dentro de `if len(cfg.Approvers) > 0` — URL definida sem approvers é **ignorada em silêncio**.

### Grupo B — Ligáveis por env, inertes por omissão

| Capacidade | Env | Nota |
|---|---|---|
| Execução durável (AOS-180) | `AOS_DURABLE_EXECUTION=1` (+`AOS_EVENTSTORE_PATH`) | Ligada no dev-hardened. **Gap residual:** o `Resumer` nunca é composto — checkpoints escritos, nunca lidos; crash-resume é manual e recomeça do turno 1 |
| Observabilidade OTLP (AOS-173) | `AOS_OTLP_ENDPOINT` (+auth por ficheiro) | Funciona (dev-hardened tem colector). **Gap:** SLOs/alertas/dashboards (AOS-085/086) sem produtor em runtime; export só `/v1/traces` |
| Four-eyes (AOS-162) | `AOS_APPROVERS_FILE` | Totalmente ligado; 501 declarado sem gate; produção exige durable-exec |
| Promotion controller (AOS-159/206) | `AOS_RATIFIERS` | ⚠️ **Mesmo configurado, não há endpoint nem CLI** para submeter uma ratificação (`Promote` só in-process; deferido para AOS-096) |
| Retenção TTL (AOS-092/213) | `AOS_RETENTION_VERSION`+`_PERIODS` | Sem scheduler interno — só corre sob `POST /dsar/expire` (cron externo autenticado necessário) |
| Custódia externa da KEK (AOS-215/216) | `AOS_DSAR_VAULT_*` | Obrigatória em produção com substrato durável. **Gaps:** addr sem validação de esquema; token nunca renovado (F6) |
| mTLS do plano de controlo (DEF-012) | `AOS_CONTROL_MTLS_CA_PATH` | **Incompatível** com a topologia dev-hardened (TLS terminado no edge; o mTLS exige TLS no nó) |
| Níveis de autonomia (AOS-087) | `AOS_AUTONOMY_LEVELS` | ⚠️ Sem linha de banner; sem a variável o oráculo é nil e **nenhum `escalate` é emitido** — o circuito HITL fica inalcançável |
| Velocidades de queima do breaker | `AOS_BREAKER_MAX_*_PER_SEC` | ⚠️ **Armadilha fail-open** (F2) — não é «desligado por escolha» |

### Grupo C — Residual nomeado e aceite

- **Signer de checkpoints (AOS-221/072):** a verificação detecta mutação/inserção/remoção-interna, **não a truncatura do tail** (apagar os N registos mais recentes verifica verde) nem a reescrita desde a génese. O `Signer`/`VerifyFromCheckpointAtHead` existem e estão testados; falta a custódia da chave (bloqueio D4/AOS-156) e o wiring. **Sem ticket/DEF próprio** — o residual vive só na spec e no banner.

---

## 6. Achados activos consolidados (cruzados e deduplicados)

Severidade × esforço × fonte. **F1–F4 são anteriores a qualquer wiring novo** — estão activos hoje.

| # | Achado | Sev. | Esforço | Fonte |
|---|---|---|---|---|
| **F1** | **Step-ledger persiste outputs de tools EM CLARO no WAL** — `bootstrap.go:825` passa o cifrador sem o produtor (`WithProducer` tem zero chamadores); os mesmos bytes ficam cifrados em `replay.captured` e em claro em `step.ledger.applied`, **fora do crypto-shredding**. Activo com durable-exec (exigida em produção). O remédio é propagar o titular por-`Apply` (como `tc.Subject` no capturer), não um produtor global — o ledger é partilhado por todos os runs | **Alta** | Médio (mudança de assinatura) | desafio A3 |
| **F2** | **Armadilha fail-open das velocidades do breaker** — o nó nunca cabla `VelocitySource`; `NewBreaker` devolve `ErrVelocitySourceMissing`; o erro é **engolido** (`breaker_wiring.go:73-77`) e o run fica **sem disjuntor nenhum** (perde no-progress e wall-clock). Remédio: erro fatal no arranque + anotar/retirar as envs da doc até haver fonte | **Alta** | Pequeno | análise 08-08, confirmado A1/A2 |
| **F3** | **Breaker inerte no run comum — dois mecanismos independentes:** (a) `observeAction` é código morto (zero chamadores ⇒ no-progress nunca dispara); (b) `Observe` é no-op fora de `Running` e o lazy-claim só transita `ready→running` no primeiro steer/escalada ⇒ um run comum **fica em `ready` do princípio ao fim** e o breaker nunca arma — inclusive no deny-loop que o motiva. Corrigir um sem o outro não resolve. Não existe teste de trip ao nível do nó. ⚠️ Divergência entre desafios — ver §9 | **Alta** | Médio | análise 08-08 + desafios A2/A4 |
| **F4** | **A máquina durável nunca escreve `complete`/`failed`** — 13 arestas declarativas, o nó conduz 5; `CheckDeadlines` tem zero chamadores de produção apesar de `liveness/doc.go` exigir execução periódica; o desfecho vive num mapa em memória com poda FIFO. **Um run acabado por erro/panic/MaxTurns é, no log durável, indistinguível de um crash a meio** | **Alta** | Médio | desafio A4 |
| **F5** | **Fallback de dev sem guarda de produção** — sem `AOS_MODEL_API_KEY_PATH`, o nó arranca em `AOS_MODE=production` a apresentar o bearer `aos-dev-omniroute` embebido (`modelgatewaywiring.go:78-81`). Não declarado | Média | Pequeno | desafio A3 |
| **F6** | **Vault DSAR sem validação de esquema + token nunca renovado** — seguir a recomendação do README (tokens curtos) mata a custódia silenciosamente com `/readyz` verde (a sonda usa seal-status, não autenticado) | Média | Pequeno/Médio | desafio A3 |
| **F7** | **Promotion controller sem superfície** — composto sempre, sem endpoint/CLI (Grupo B) | Média | Médio (depende de AOS-096) | análise 08-08 |
| **F8** | **SLOs/alertas/dashboards sem produtor em runtime** — funções puras verdes que nunca disparam; runbooks mapeados a alertas que ninguém emite | Média | Médio | análise 08-08 |
| **F9** | **`Resumer` nunca composto** — crash-resume manual, recomeça do turno 1 (Grupo B, durable) | Média | Médio→Grande | análise 08-08 |
| **F10** | **Attestation sem liveness nem atribuição** — portas `ChallengeIssuance`/`DeviceEnrollment` sem wiring (Grupo A) | Média | Médio | análise 08-08 |
| **F11** | **Autonomy registry sem sink** — `NewLevelRegistry()` sem `WithSink`; as mudanças de nível não ficam registadas. Correcção de uma linha | Média | **Pequeno** | desafio A4 |
| **F12** | **Saga/compensação sem construtor de produção** — o pacote está no fecho do nó; `WithCompensationRegistry` nunca é chamado; e sem `failed` (F4) a aresta de compensação é inalcançável por dois motivos | Média | Médio | desafio A4 |
| **F13** | **Nenhum caminho reclama runs órfãos** — `TryAcquire` só no submit; sem varredura de arranque; um crash não é retomado por ninguém (eixo AOS-015/099) | Média | Médio | desafio A4 |
| **F14** | **Banner mudo** sobre budget, broker, e modelo/gateway — diverge da disciplina AOS-203 («postura anunciada = postura ligada») | Baixa | Pequeno | análise 08-08 + A3 |
| **F15** | **`MaxTurns` do corpo do pedido sem clamp** — `max_turns=100000` é aceite. Composto com F17 é um **DoS de um pedido**: um único `POST /runs {max_turns: 200}` esgota o fusível RPM e desliga o nó para todos os runs; o rate-limit de ingresso não protege (1 pedido), o tecto de in-flight não protege (1 run) e o breaker está inerte (F3) | **Alta** *(promovido pelo desafio A5)* | Pequeno | desafios A1/A5 |
| **F16** | **`CallContext.BudgetTokensRemaining`/`Sensitivity` sempre zero no nó** — sem produtor; dívida de higiene (não chega à política Cedar, logo sem risco de decisão errada) | Baixa | — | desafios A1/A4 |
| **F17** | **O keypool do gateway é um fusível de 120 chamadas ao modelo por vida do processo** — `modelgatewaywiring.go:135-137` compõe uma conta com `LimitRPM: 120` e o contador **nunca reinicia** (não há janela, apesar do comentário «janela corrente»): `keypool.go:171` incrementa a cada `Select`, `saturated()` aos 120, e `gateway.go:520-523` falha **fail-closed para sempre** à 121.ª chamada (~8 runs com `MaxTurns=16`). Brownout permanente e silencioso, indistinguível de avaria do provider. Remédio: janela com relógio injectável (o GW já tem `WithClock`) **ou** `LimitRPM=0` com o tecto declarado como sendo do LiteLLM externo (D11) | **Alta** | Pequeno | desafio A5 (verificado à mão) |
| **F18** | **O estágio de identidade do GW é um stub allow-all** — `production.go:178` guarda só contra nil; o nó passa `nodeModelAuthn{}` (`modelgatewaywiring.go:93-103`), que forja o principal e permite tudo; o estágio real (`pipeline/authn`, EdDSA + raiz humana ADR-003) tem zero importadores. Declarado no código como dívida de AOS-057/D4, mas ausente deste relatório | Média | Médio | desafio A5 |

### Mitos refutados (para ninguém repetir)

Os desafios registam listas completas de achados refutados pelo céptico. Os mais relevantes: «permit sem Commit é fuga de reserva» (falso — em headroom são indistinguíveis); «o `Rebuild` drena a sub-árvore após restart» (falso — contadores nascem a zero; o efeito é fail-**open**); «a retoma duplica custo no WORM/ES» (falso — dedup por `run_id:step_id`; sobrevive só a agregação por-run sobre múltiplos traces, hoje sem chamador); «o guarda ORQ/SCH está furado» (falso — `go list -deps` limpo); «o 4-eyes tem TTL que mataria runs à espera» (falso no HEAD — `humanTTL=0` e `CheckDeadlines` sem chamador; o modo de falha real é o inverso: suspensão indefinida).

---

## 7. Billing/custos — o plano corrigido (cruzamento da análise com os desafios A1/A2)

**Componentes verdes:** `budget` (CAS 0-overshoot, Rebuild do log, hook `BudgetCheck` com deny auditado), `metering/cost` (micro-USD inteiro, fail-closed sem preço), `pricing` (tabela versionada, alterações seladas no WORM), `progress-surface`, `planvalidate`. **Só o `budget` está ligado ao nó** (AOS-256/AOS-257: nó de orçamento por-run no seam de `SecuredRuntime.Run`, `BudgetCheck` no lugar do `BudgetStub`, saldo da reserva no decorator do `ActivityDispatcher`, opt-in por `AOS_BUDGET_MAX_TOKENS`) — e **token-only**, no alcance declarado abaixo; `metering/cost` e `pricing` continuam **sem chamador de produção** (eixo AOS-259).

**Declaração de alcance da v1 (AOS-255, fixada antes de qualquer wiring)** — é este o texto que o
nó imprime no banner e que `deploy/node/README.md` documenta, e é o vocabulário que os tickets
AOS-256..262 usam:

> **orçamento: cobre tool calls em TOKENS; o gasto de inferência é travado por tempo (wall-clock), não por tecto**

**TOOL-ONLY** porque o *hook* vive na cadeia do Reference Monitor, atravessada só por tool call;
**TOKEN-ONLY** porque a dimensão micro-USD não tem canal de dados ponta a ponta. O tecto é
**por-run**, nunca por-mandato. Fixar a frase primeiro é o que impede que a linha de banner seja
escrita, no calor do ticket que liga o hook, como se cobrisse o gasto todo.

O que os desafios corrigiram no plano de fecho:

1. **A chamada ao modelo está fora da cadeia** (`loop.go:549` — directa, sem `Mediate`). Um orçamento sobre tool calls deixa a **linha de custo dominante (inferência) sem tecto**. A v1 deve declarar-se **TOOL-ONLY em voz alta** no banner, com o turno de modelo como eixo separado (reservar antes de `loop.go:549`, saldar com o usage real).
2. **O eixo micro-USD não tem canal de dados** — `port.Usage` não tem campo de custo; `translateResponse` deixa `CostMicroUSD=0`. v1 **token-only por construção** (funciona ponta a ponta hoje); a primeira env em $ exige o contrato `port` primeiro. E ligar `WithCost` **duplica os tokens** (dois spans `chat` por trace somados sem dedup por parentesco) — tem de sair no mesmo commit que a deduplicação.
3. **Ordem errada = arranque partido:** compor o hook sem `AddNode(goal.RunID, …)` no seam `secured.go:460` ⇒ `ErrUnknownNode` ⇒ **100% das tool calls negadas** (fail-closed e ruidoso, mas partido). O `Settle` vive num **decorator do `ActivityDispatcher`**, não no loop.
4. **A progress-surface precisa de um retentor de spans** (hoje o tracer é Noop por omissão e o `SpanTracer` dispara-e-esquece: `Evaluate` lê 0 **sem erro** — superfície verde a mentir) e de resolvedores `runID→traceID/treeID` que o nó não tem.
5. **Primeira entrega da exaustão: só burn-down + aviso.** `extend` não tem mutador de tecto nem autoridade (a concessão mais forte pelo caminho mais fraco); `summarize_stop`/`abort` não têm executor; o «timeout» não existe. O desenho-alvo é um **segundo tipo de `PendingRecord`** reutilizando a maquinaria HITL (decisão do dono, D4 abaixo).

**Ordem mínima defensável (desafio A1):** corrigir texto → F2 (fail-closed do breaker) → linha de banner. Entrega honestidade e remove a armadilha, sem ligar enforcement cego.

---

## 8. Plano de acção consolidado

### 8.1 Imediato (dias, sem decisões prévias) — remove risco activo

| Ordem | Acção | Fecha |
|---|---|---|
| 1 | Propagar o titular por-`Apply` no step-ledger (selar outputs por-titular) | **F1** |
| 2 | `NewBreaker` fail-closed no arranque + anotar as envs de velocidade na doc | **F2** |
| 3 | **Janela no keypool ou `LimitRPM=0` declarado** (desarmar o fusível de 120 chamadas) | **F17** |
| 4 | Guarda de produção no fallback `aos-dev-omniroute` | **F5** |
| 5 | `WithSink` no autonomy registry (uma linha) + linhas de banner (budget/broker/modelo/autonomia) | **F11, F14** |
| 6 | Validação de esquema em `AOS_DSAR_VAULT_ADDR` | **F6** (parcial) |
| 7 | Clamp de `MaxTurns` (`AOS_MAX_TURNS`, default 16) | **F15** |
| 8 | Expor por env os três knobs de ingresso já implementados (token-bucket + tecto in-flight) + teste do 429 | correctivo do «sem backpressure» (A5) |

### 8.2 Curto prazo (tickets pequenos-médios)

- **Breaker efectivo (F3):** ligar `observeAction` no fecho do `execute_tool` (o hash canónico já existe no span) **e** decidir onde reclamar `ready→running` no arranque do run; teste de trip ao nível do nó (o desafio A4 descreve-o: repetir a mesma call negada e asserir trip antes de `MaxTurns`).
- **Estados terminais (F4):** escrever `complete`/`failed` no log durável + chamador periódico de `CheckDeadlines`. Desbloqueia F12 (compensação).
- **Crash-resume (F9+F13):** varredura de runs órfãos no arranque + `Resumer` composto (a infra de suspensão de AOS-021 é o molde).
- **F6 restante:** renovação do token Vault.
- **F10:** wiring de `ChallengeIssuance` + `AOS_DEVICE_ENROLLMENT_FILE` + banner de attestation.
- **Billing token-only (§7):** banner → `AddNode` por-run → hook no lugar do stub + `Settle` decorator → envs `AOS_BUDGET_*` (tokens) → teste de nó com permit (não só deny) e sem fuga após deny do egress.

### 8.3 Decisões do dono (bloqueiam trabalho; recomendações registadas nos desafios)

| # | Decisão | Recomendação do desafio |
|---|---|---|
| D1 | Âmbito do orçamento v1: tool-only ou tool+turno de modelo? (A1) | Recomendação: A (tool-only) **com o banner a dizê-lo**; B como eixo. **DECIDIDO 2026-08-13 (dono): OPÇÃO B** — tool calls **+** turno de modelo, com reserva antes da inferência (AOS-260). Razão do afastamento em `specs/EPIC-20` §0-bis |
| D2 | Prioridade do eixo micro-USD (A1) | Recomendação: token-only na v1; $ só depois do contrato `port`. **DECIDIDO 2026-08-13 (dono): o eixo $ ENTRA na v1** (AOS-259 fecha o contrato `port` + dedup por parentesco). Razão em `specs/EPIC-20` §0-bis |
| D3 | Tecto por-run (env) ou por-mandato (token)? (A1) | Por-run agora; por-mandato deferido (trabalho de identidade, D4/EPIC-16) |
| D4 | Prompt de exaustão: caminho próprio ou 2.º tipo de `PendingRecord`? (A2) | **Reutilizar a maquinaria HITL**; primeira entrega = só aviso |
| D5 | Autoridade exigida para `extend` (A2) | Piso: paridade com `pause` (Ed25519 de operador); four-eyes acima de limiar |
| D6 | Dono do tecto (mutação auditada no `budget` vs nova incarnação) (A2) | `extend` fora das opções na 1.ª entrega; mutação como ticket do `budget` |
| D7 | Partilhar ou separar o cliente/token Vault do broker (A3) | **Separar** (`AOS_BROKER_VAULT_*`) — partilhar acumula destruir-chaves + ler-segredos num só token |
| D8 | Rota do valor sob composição (A3) | **C na v1** (broker só para credenciais in-process); B (handle opaco até ao orchestrator) como desenho-alvo |
| D9 | Guarda ORQ/SCH: lista de dois nomes vs critério (A4) | Manter a lista, corrigir a descrição, **replicar o teste em `cmd/aos-demo`** |
| D10 | Qual retoma é canónica e quem é o dono do heartbeat (A4) | Declarar antes de compor `worker.Worker` (ADR-018 §5-bis já o antecipa) |
| D11 | Onde vive o rate-limit de throughput (A5 — forçada por F17) | **Tecto no LiteLLM externo** com `LimitRPM=0` declarado na tabela AOS-203 — coerente com o deployment endurecido; alternativa: janela real no keypool |
| D12 | Como consumir admission/filas sem violar o guarda (A5) | **Adaptador node-local** (a via do A2) para as portas scheduler-free; extracção de módulo-folha só se o token-bucket real (AOS-027) for necessário; a emenda «sem `Scheduler.Start`» **não é exequível** (zero-dep + símbolo errado) |

---

## 9. Divergências entre avaliações e ressalvas

- **Breaker — divergência real entre desafios.** A secção de refutados do desafio A1 afirma que o action-dedup «está cablado e pára uma repetição idêntica ao 3.º turno»; os desafios A2 e A4 **verificaram à mão** que `observeAction` tem zero chamadores e que o `Observe` é no-op fora de `Running`. Este relatório fica com A2/A4 (verificação manual directa, em dois HEADs independentes). É falsificável em minutos com o teste proposto pelo A4 — até ele existir, F3 fica como confirmado-com-ressalva.
- **A execução em microVM (`run-fclive-2`) foi observada ao vivo, não em CI** — o mecanismo (ScopeGate) está provado em CI; a execução viva não.
- **O «16 turnos, nada selado» do run de 2026-08-05 não é verificável a partir do código** (não há log do run no repo); o mecanismo causal e o caminho ao vivo estão estabelecidos. A causa atribuída originalmente à ausência de shred estava errada (a cifra é conduzida pela resposta do modelo capturada a cada turno, não por efeitos de tool) — e F1 mostra que o problema era pior e noutro sítio (o step-ledger).
- **Citações `ficheiro:linha`** dos desafios foram reconferidas à mão nos pontos que carregam veredictos; o resto vem de lentes que passaram pelo céptico (marcado como tal nos documentos-fonte). O repositório move-se depressa — as linhas referem os HEADs avaliados (`ac5042f`, `2f234bb`, `d6a334a`, `075ea87`, e o actual).

## 10. Apêndice — proveniência e histórico deste documento

- **2026-08-05:** avaliação viva original (imagem, fluxo de negócio, caminho de permit; gaps 1–5).
- **2026-08-06:** revisão adversarial (5 lentes + cépticos): gaps 1 e 5 resolvidos; gap 2 agravado; gap 3 cindido; gap 4 recalibrado.
- **2026-08-08 (manhã):** avaliação de billing + inventário Grupo A/B/C.
- **2026-08-08 (tarde):** análise aprofundada em 6 frentes (swarm); achados N1–N7. Commit `27ce602`.
- **2026-08-08:** sessão de execução ao vivo do ciclo de aprovação (outro autor): gaps 2, 3, 5-bis fechados; audit com atribuição; suspensão durável.
- **2026-08-08:** desafios adversariais A1–A4 (outro autor): N8–N13, correcções de plano, decisões do dono.
- **2026-08-08:** esta consolidação — substitui as secções datadas; o detalhe integral das vagas 3–4 permanece nos ficheiros-fonte ligados no cabeçalho.
- **2026-08-08:** desafio A5 (escalonador) incorporado: F17 (fusível do keypool) e F18 (authn do GW) novos; F15 promovido a alta (DoS composto); correcções ao SCH (backpressure de ingresso existe; tieradapter é código morto interdito; filas/prioridade são single-node); D11/D12 novas.
