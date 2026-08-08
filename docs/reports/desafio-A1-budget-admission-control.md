# Desafio ao plano A1 — budget / admission control (AOS-008, ADR-008)

> **O que é:** uma avaliação adversarial do subtópico `#### A1. budget / admission control`
> de [`prontidao-modelos-agenticos.md`](./prontidao-modelos-agenticos.md), na secção
> *«Análise aprofundada por tópico (2026-08-08)»*. Não é uma segunda opinião educada: o
> objectivo declarado foi **desafiar** o plano, sobretudo o que ele dá por «em falta».
>
> **Data:** 2026-08-08 · **HEAD avaliado:** `ac5042f` (branch `feature/AOS-128-ux-dx-tests`)

## Proveniência e método

Seis lentes **independentes** — nenhuma viu o trabalho das outras — correram contra o
código, não contra a documentação. Cada uma foi seguida de um **céptico cuja missão era
refutá-la**, não confirmá-la: um achado só ficou CONFIRMADO se não foi possível derrubá-lo,
e a severidade foi revista em baixa por omissão. Uma síntese final consolidou o resultado.

| lente | a pergunta que só ela fez |
|---|---|
| `factos` | as alegações de «costura em falta» ainda são verdade **hoje**? |
| `retoma` | orçamento × suspensão/retoma — a interacção que o documento não podia conhecer |
| `fail-closed` | ligar o budget **inverte** a direcção da falha; produção devia recusar o stub? |
| `dinheiro` | um estimador placeholder sobre-reserva ou sub-reserva? o «0-overshoot» prova o quê? |
| `operador` | de onde vem o tecto: env, obligation do PDP, ou claim no **token assinado**? |
| `sequência` | que ordem é segura, e o que falta ao plano que ele não menciona de todo |

13 agentes, 0 erros, 478 chamadas a ferramentas, ~21 min. Dos achados produzidos, **12
foram refutados** pelo céptico e estão registados no fim — para ninguém os repetir.

### Porque as referências `ficheiro:linha` do A1 foram todas reconferidas

A sessão de 2026-08-08 alterou `packages/integration/secured.go` (nova ordem de hooks,
`ApprovalVerifier` acrescentado, `rewritingDispatcher` **removido** e substituído por
`agentruntime.WithCallRewriter`) e `packages/kernel/agent-runtime/loop.go`. As lentes foram
instruídas a **nunca** aceitar um número de linha do documento sem o confirmar no HEAD.
Resultado: `secured.go:324` **sobreviveu** à reescrita e continua exacto.

### O que foi verificado por quem

Os dois achados que sustentam o veredicto — o desvio da chamada ao modelo e a ausência de
canal de custo no `port` — foram **reconferidos à mão** depois da síntese, e só depois
relatados. A nota de método da síntese (imediatamente abaixo) distingue o que ela própria
re-executou do que herdou das lentes.

## Sumário executivo

1. **O fecho descrito no A1 não fecha o risco que o A1 invoca.** A chamada ao modelo não
   atravessa a cadeia de mediação: cumpridas as três alíneas, o tecto cobre os argumentos
   das tool calls e deixa a linha de custo dominante sem admission control.
2. **A metade micro-USD não tem fonte de dados** — `port.Usage` não tem campo de custo.
3. **As alegações (a), (b), (c), a linha 324 e o banner estão exactas.** O que falta ao
   documento são consequências, não factos.
4. **O passo 2 do plano, na ordem escrita, produz um nó que nega 100% das tool calls** —
   falha fechada e nomeada no WORM, mas é um arranque partido que o plano não avisa.
5. **Há uma acção que remove risco hoje** e não está no plano do A1: tornar fatal o erro de
   `NewBreaker` em vez de o engolir (a armadilha das envs de velocidade).

---


> Nota de método: re-verifiquei pessoalmente contra o HEAD as citações que carregam o veredicto (`secured.go`, `loop.go`, `rmadapter.go`, `budget.go`, `runtime_adapter.go`, `port.go`, `call.go`, `breaker_wiring.go`, `resume_model.go`, e os greps de chamadores de `budget.New`/`Rebuild`/`WithCost`/`AOS_BUDGET`/`observeAction`). As citações a `monitor.go`, `production.go`, `burndown.go`, `bootstrap.go`, `api.go` e `breaker.go` vêm das lentes já passadas pelo céptico e **não foram re-executadas por mim** — estão marcadas onde aparecem.

## Veredicto sobre o A1 tal como está escrito

| Alegação do documento | Veredicto | Evidência (HEAD) |
|---|---|---|
| **PROPÓSITO**: orçamento hierárquico em tokens e micro-USD, reserva CAS «antes de cada spawn/tool call» | **exacto** | `packages/control-plane/budget/budget.go:165-200` (débito atómico ao subir a `ancestry()`, rollback do prefixo, evento `budget.reserved`). O âmbito declarado é mesmo *spawn/tool call*. |
| **PROVADO**: os 5 testes nomeados; consumidores reais `orchestrator/delegation.go`, `scheduler/breaker.go`, `metering/cost/budgetbridge` | **incompleto** | Os testes existem e os importadores também, mas nenhum é alcançável a partir do nó: `budget.New(` e `budget.Rebuild(` têm **zero chamadores não-teste** em todo `packages/` (grep: só `control-plane/scheduler/*_test.go` e `control-plane/orchestrator/*_test.go`); `budgetbridge` é um conversor de tipos sem chamador fora do próprio pacote. Chamar-lhes «consumidores reais» é verdade sobre o repo, não sobre o nó. |
| **Linha 324** — `integration/secured.go:324` é `BudgetStub{}`, allow-incondicional | **exacto, e sobreviveu à reescrita de hoje** | `packages/integration/secured.go:324` — `referencemonitor.BudgetStub{}, // budget (stub aceitável)`, entre `NewScopeGate` (:323) e `egressHook` (:325). O `ApprovalGate` entrou acima, em :315-317, e não deslocou a cadeia. |
| **(a)** `SecuredConfig` não tem campo de orçamento — «nem por código se injecta» | **exacto** | `grep -n "Budget" packages/integration/secured.go` devolve só :299 (comentário) e :324. Não existe campo, nem opção funcional. |
| **(b)** falta o chamador de `BudgetCheck.Settle` pós-`Mediate` no loop | **incompleto (o facto certo, o sítio errado)** | Zero chamadores de `Settle`/`Commit`/`Release` do `BudgetCheck` fora de `packages/control-plane/budget/`. Mas o *sítio* prescrito não serve: na via durável o `Mediate` corre dentro da closure do `ledger.Apply` (`activity/dispatch.go:221-246`) e o que o loop vê é o retorno de `rt.dispatcher.Dispatch` (`loop.go:712`), que em dedup sintetiza um permit sem que nenhum `Evaluate`/`Reserve` tenha corrido. Ver §2. |
| **(c)** nenhuma env `AOS_BUDGET_*` | **exacto mas enganador** | `grep -rn AOS_BUDGET packages/` → **zero ficheiros**. Enganador porque a única superfície de custo que o operador encontra hoje (`AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC`, documentada em `deploy/node/README.md:111-112`) é uma armadilha: ver §2, risco 3. Falta o link cruzado para o item 5a da mesma secção. |
| **«O banner não declara o stub» — diverge da AOS-203** | **exacto** | Não existe linha de budget no banner (`grep -i budget` em `packages/cmd/aos`: só `go.mod`). |
| **RISCO**: «loop agêntico sem tecto de gasto (caso observado: 16 turnos, só `MaxTurns` como travão)» | **exacto como citação histórica; incompleto como diagnóstico** | O parêntese cita um run anterior ao wiring do breaker — não é uma afirmação sobre o HEAD, e o documento não mente. Mas a conclusão que falta é a que interessa: **fechar (a)+(b)+(c) não põe tecto no gasto que o parêntese descreve.** Ver §2, risco 1. |
| **Esforço 1 — banner (pequeno)** | **exacto no tamanho, errado na ordem** | A sentinela/linha de banner é pequena, mas uma linha «budget: STUB» antes de existir env correctiva só encaixa no molde AOS-203 na forma «capacidade ausente + eixo», que o banner já usa noutros pontos (`bootstrap.go:1395`, `:1408` — via lente, não re-verificado). Pôr o banner **primeiro** é aceitável; pôr uma sentinela `ErrBudgetStub` em `production.go` primeiro bricava todos os chamadores de `NewProductionSecure`. |
| **Esforço 2 — campo em `SecuredConfig` + compor o hook + Settle (médio)** | **incompleto no âmbito, «médio» sobrevive** | Faltam três peças: (i) quem regista o nó de orçamento por-run — sem isso `budget.go:177` devolve `ErrUnknownNode` e `rmadapter.go:100-103` converte-o em `HookDeny`, negando **100% das tool calls**; (ii) o `Settle` tem de viver ao nível do `Reserve` (decorator do `ActivityDispatcher`, não o loop); (iii) o ponto do turno de modelo. O seam por-run existe e é `packages/integration/secured.go:460` (`SecuredRuntime.Run`), que já faz `Freeze`/`defer Release` sobre `goal.RunID`. |
| **Esforço 3 — envs + estimador real (médio)** | **subestimado: falta uma costura** | `WithEstimator` recebe uma `CostFunc(*rm.Call)`, e `rm.Call` (`packages/kernel/reference-monitor/call.go:96-140`) **não tem prompt, tokens nem usage** — só `Input []byte`. Um estimador baseado no prompt materializado + tabela de preços **não é injectável** pela seam existente. `DefaultEstimator` (`rmadapter.go:163-166`: `len(call.Input)/4 + 1`, `toks*10`) está honestamente documentado como placeholder. |
| **Esforço 4 — backend distribuído (grande/deferido)** | **exacto, mas falta a distinção** | O documento não distingue «tecto **por-processo**» de «tecto por-árvore que sobrevive a restart». Hoje é o primeiro: `budget.New` não tem via de re-hidratação (`Rebuild` devolve `map[string]NodeState` e nenhum método do `Budget` o aceita), e `doc.go:40-58` já declara o token-bucket distribuído fora de âmbito. |

---

## O que o documento NÃO diz e devia

### Interacção com suspensão/retoma (primeiro, por instrução)

**S1 — [PARCIAL/media] A reserva é in-process; a suspensão é durável e re-hospedável noutra réplica.**
`rmadapter.go:27-28` (`pending map[string]Reservation` sob `sync.Mutex`) e `budget.go:224-228`/`:257-261` (`Commit`/`Release` começam por `b.lookup(r.ID)` sobre `b.res`, in-memory) são memória do processo. `budget.go:202` emite `budget.reserved` no Event Store. Do outro lado, `resume.go:73` (`suspendedDurably`) lê o log quando a memória desta réplica não conhece o run e `resume.go:112` re-submete com lease novo. Um run que escale, seja retomado noutra incarnação, deixa `budget.reserved` sem `committed`/`released` possível — o handle morreu com o processo.
**O que o céptico derrubou, e é importante:** a consequência **não** é bloqueio irreversível. `Rebuild` tem zero chamadores não-teste e o `Budget` não tem API de carregamento de estado, logo num restart os contadores nascem a **zero** — o efeito real é fail-**open** silencioso (o log durável diz `reserved`, a memória diz 0), não drenagem monotónica. Isto é uma restrição de desenho sobre wiring por escrever, não um defeito vivo.

**S2 — [PARCIAL/media] Os spans chat são reabertos e reanotados no replay; a via de tools tem a disciplina, a via de modelo não.**
`resume_model.go:70-77` devolve a `ModelResponse` **registada**, `Usage` e `CostMicroUSD` incluídos, e a intercepção acontece dentro de `rt.model.Call` (`loop.go:549`), **depois** de o span chat estar aberto (`loop.go:535-548`); `loop.go:554-562` anota tokens e custo nesse span. Contraste exacto: `activity/dispatch.go:208-216` mantém deliberadamente o custo **fora** do `durable.Result` «porque o replay re-emitiria um custo que o efeito nunca voltou a incorrer». O lado do modelo não tem essa regra.
**O que o céptico derrubou:** não há duplicação no WORM/ES (o `stepID` é determinístico e o store dedup por `run_id:step_id`), e o burn-down indexa **por trace**, com trace novo por incarnação. Sobrevive o caso estreito e latente: qualquer agregação **por run** que some vários traces conta duas vezes os turnos 1..N (`catalog.go:975-978` faz exactamente isso, sem chamador não-teste; um backend OTLP externo que agrupe por `aos.run_id` sofreria o mesmo).

**S3 — [PARCIAL/baixa] O `Reserve` não tem chave de idempotência no backend.**
`budget.go:193` — `id := b.treeID + "-r" + strconv.FormatUint(b.idseq.Add(1), 10)`: id novo por chamada. A chave `run_id:step_id` existe **só** no índice do adaptador (`rmadapter.go:157`), não no `Budget`. Um resume que repita o step reserva de novo, enquanto o step-ledger deduplica o efeito. Latente enquanto nada estiver composto; torna-se defeito no dia em que o backend durável for ligado.

### Riscos que não dependem de retoma

**1 — [CONFIRMADO/alta] Fechar o A1 como está escrito não põe tecto no gasto que domina um loop agêntico.**
`packages/kernel/agent-runtime/loop.go:324` chama `rt.callModel(...)` e `loop.go:549` faz `resp, err := rt.model.Call(chatCtx, view)` — invocação directa, sem `Mediate`, sem dispatcher, sem hook. O custo só é somado **a posteriori** em `loop.go:333` (`res.TotalCostMicroUSD += resp.CostMicroUSD`). O `BudgetCheck` é um `rm.Hook` (`rmadapter.go`, `var _ rm.Hook`) e a cadeia onde entraria (`secured.go:318-326`) só corre por **tool call** (`loop.go:712`). O estimador confirma o eixo: `len(call.Input)/4` sobre os args da tool. Consequência: executados os três passos do fecho, o tecto cobre a estimativa dos bytes dos argumentos das tool calls e deixa a linha de custo dominante sem admission control. **O A1 precisa de nomear dois pontos de reserva ou declarar-se TOKEN-ONLY/TOOL-ONLY em voz alta.**

**2 — [PARCIAL/alta] A metade micro-USD do orçamento não tem fonte de dados: o custo do GW não tem sequer canal de retorno.**
`packages/platform/model-gateway/runtime_adapter.go:89-95` — `translateResponse` povoa só `Usage{InputTokens, OutputTokens}`; `CostMicroUSD` fica no zero-value. E **não podia ser de outra forma**: `port.Usage` (`port/port.go:97-103`) e `port.ChatResponse` não têm campo nenhum de custo. `WithCost` (`gateway.go:229`) tem zero chamadores não-teste. O único `CostMicroUSD` não-nulo do nó é o literal `1500` do modelo de referência (`bootstrap.go:1643`, via lente). Consequência: com o GW real, `loop.go:333` soma zeros para sempre; um `AOS_BUDGET_MAX_COST_MICRO_USD` seria um tecto que nunca dispara, e desta vez com um número no banner.
**Precisão que o documento não tem:** a correcção implícita («ligar o `WithCost`») é **insuficiente para o RT** e **suficiente para o burn-down**. O GW abre o seu próprio span chat filho do `chatCtx` do RT e `recordCost` anota esse span (`gateway.go:300-302`, `:603-607`, via lente); `AggregateByTrace` soma todos os spans chat do trace, logo 0 (RT) + real (GW) = real. Mas `res.TotalCostMicroUSD`, o `TurnRecord` e o atributo do span do RT continuam exactamente 0. Fechar o eixo micro-USD do **orçamento** exige tocar em `port.Usage`/`ChatResponse` + `translateResponse` — e `ProductionConfig` (`model-gateway/production.go:100-145`, via lente) nem tem campo `Cost` para ligar o Recorder pelo caminho que o nó usa (`modelgatewaywiring.go`).

**3 — [CONFIRMADO/media] A única env de custo que o operador vê hoje desarma o disjuntor inteiro em silêncio.**
`packages/cmd/aos/breaker_wiring.go:70-72` constrói `breaker.NewBreaker(gate.m, b.provider, "", breaker.WithProgressSource(...))` — **nenhuma `VelocitySource`**; `breaker.go:146-148` devolve `ErrVelocitySourceMissing` quando qualquer limiar de velocidade é >0 e `b.velocity == nil`; e `breaker_wiring.go:73-77` engole o erro (`return nil // o run corre sem ele, como antes`). O operador que ligue `AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC` perde também o no-progress e o wall-clock. O documento já regista isto no item 5a da secção anterior (com as linhas certas) — **falta só a referência cruzada a partir da alínea (c) do A1**, que é onde o leitor vai procurar «tecto de custo».

**4 — [PARCIAL/media] Compor o hook sem registar o nó de orçamento do run nega 100% das tool calls.**
`rmadapter.go:72` — o selector default é `func(c *rm.Call) string { return c.RunID }`; `budget.go:105-107` — `New(treeID, rootLimit)` só cria a raiz; `budget.go:177` — `Reserve` devolve `ErrUnknownNode`; `rmadapter.go:100-103` — qualquer erro do backend vira `HookDeny`. Como `NewSecuredRuntime` é construído **uma vez** ao arranque e `SecuredRuntime.Run` corre **por-run** (`secured.go:460`), um `budget.New(<treeID fixo>)` ao arranque + selector default nega tudo.
Atenuante honesta (que baixa isto de alta para media): a falha é **fechada e ruidosa** — `Name() = "budget"` vai a `Decision.DeniedBy` e a razão literal «no de orcamento inexistente» é selada no WORM; o primeiro smoke test apanha-o, e a correcção é uma linha (`AddNode(goal.RunID, treeID, limite)` no seam de `secured.go:460`, ou `WithNodeSelector` a devolver o `TreeID()`). Merece uma frase no plano, não um bloqueio.

**5 — [PARCIAL/media] Fugas de reserva nos caminhos de negação a jusante e de erro.**
O budget está **antes** do egress na cadeia (`secured.go:324` vs `:325`) e o `Monitor` retorna de imediato num `HookDeny` (`monitor.go:309`, via lente) e nega por falha do audit sink (`monitor.go:351-353`, via lente) — em ambos os casos sem tocar na reserva. Terceiro caminho: `runtime_ports.go:293-294` devolve `Decision{}, err` para tudo o que não seja `ErrMediationDenied` e `loop.go:712-714` aborta o turno — um `Settle` colocado «pós-Dispatch» nunca corre. Cada fuga debita até à raiz (`budget.go:184-191`) e faz crescer `pending` sem limite.
**Correcção importante ao que se poderia pensar:** o caminho «permit sem Commit» **não** é fuga — `available = limit − reserved − committed` e `commitReserved` só move o montante de `reserved` para `committed`; em headroom são indistinguíveis.

**6 — [PARCIAL/baixa] O `Settle` no loop está ao nível errado, e a semântica difere entre as duas vias.**
Na via durável (`AOS_DURABLE_EXECUTION=1`, obrigatória sob `AOS_MODE=production` com four-eyes) o `Reserve` acontece dentro da closure do ledger e o `Settle` prescrito aconteceria no loop; em dedup não há `Mediate`, logo não há reserva e o `take()` sai vazio — correcto **por construção**, mas por uma razão que o documento não regista. Na via directa `Dispatch == Mediate` (`ports.go:156-164`). O ponto sancionado é um decorator sobre `agentruntime.ActivityDispatcher` (`WithActivityDispatcher`, já usado em `secured.go:399-401`), que fecha também a fuga do caminho de erro.

**7 — [PARCIAL/baixa] O mandato não transporta tecto; a obligation é via morta.**
`rm.CallContext.BudgetTokensRemaining` (`call.go:66-67`) existe mas os únicos produtores são `orchestrator/delegation.go:456` e `planner/planner.go:368` — nenhum composto no nó; `loop.go:652-659` constrói `CallContext{Taint: ...}` e mais nada; `identity.Claims` não tem campo de orçamento; e `pdp/input.go:71-73` diz que o campo «não é avaliado pela política». As obligations chegam **depois** de todos os hooks (`monitor.go:335`, via lente) e um tipo `budget` novo obrigaria a editar o kernel. Logo: um tecto por env é por-**run**, nunca por-**mandato**/utilizador. É uma limitação a declarar, não a resolver agora.

**8 — [PARCIAL/baixa] `MaxTurns` vem do corpo do pedido sem clamp do operador.**
`api.go:525` → `api.go:610` (`MaxTurns: req.MaxTurns`) e `loop.go:250-252` só aplica o default quando `<=0`; `WithMaxTurns` não tem chamador. Um pedido com `max_turns=100000` é aceite. Adjacente ao A1, barato, e hoje é um dos poucos travões reais.

---

## Decisões que são do dono (não minhas)

**D-A1.1 — Âmbito da v1 do orçamento: tool-only ou tool+turno de modelo?**
*Opção A (tool-only):* fecha o A1 como escrito, esforço médio, mas o banner passa a declarar orçamento activo sem cobrir a linha de custo dominante — capacidade-fantasma da classe que o próprio relatório persegue.
*Opção B (tool + turno):* porta nova em `agent-runtime` (reservar antes de `loop.go:549`, saldar com `resp.Usage`/`CostMicroUSD` reais), esforço médio-a-grande, mexe no kernel do loop.
**Recomendação:** B para o desenho, A para a entrega — implementar A **com o texto do banner e da doc a dizer explicitamente «cobre tool calls; o gasto de inferência é travado por tempo, não por tecto»**, e abrir B com eixo. O que não é aceitável é A com o banner a dizer «orçamento activo».

**D-A1.2 — Prioridade do eixo micro-USD.**
*Opção A:* v1 **token-only** (limite em tokens funciona ponta-a-ponta hoje: `translateResponse` povoa `Usage`, e o burn-down toma o máximo das duas dimensões). Zero trabalho no GW.
*Opção B:* fechar o eixo $ primeiro — campo de custo em `port.Usage`/`ChatResponse`, preencher em `translateResponse`, campo `Cost` em `ProductionConfig` + `WithCost`. Toca no contrato `port`.
**Recomendação:** A na v1, com a dimensão $ desactivada por construção (não «configurável a 0», que lê como tecto). B como pré-requisito nomeado da primeira env em $.

**D-A1.3 — Granularidade do tecto: por-run (env) ou por-mandato (token/delegação)?**
Por-run é o que a arquitectura suporta hoje sem costura nova. Por-mandato exige campo no token NHI e propagação no `CallContext` — trabalho de identidade, adjacente ao eixo D4.
**Recomendação:** por-run agora, por-mandato deferido com eixo. Registar em voz alta que dois runs do mesmo utilizador não partilham tecto.

**D-A1.4 — O que fazer com a armadilha das envs de velocidade (risco 3).**
*Opção A:* cablar uma `VelocitySource`. *Opção B:* fazer o erro de `NewBreaker` ser **fatal** no arranque em vez de engolido. *Opção C:* remover as envs da doc até haver fonte.
**Recomendação:** B + C no imediato (fail-closed no arranque é a disciplina da casa), A quando o custo real chegar aos spans. É barato e é a única acção que hoje **remove** risco em vez de adicionar superfície.

**D-A1.5 — Durabilidade do tecto.**
Por-processo (o que existe) vs sobrevive a restart (exige mapear a reserva pela chave durável `run_id:step_id`, persistir esse mapeamento no `budget.reserved`, e uma via de reconciliação/TTL no `Rebuild`).
**Recomendação:** declarar «tecto por-processo» explicitamente no A1 e manter o backend durável deferido — mas **não** ligar o hook em produção multi-réplica sem essa declaração escrita, porque o comportamento após restart é fail-open, não fail-closed.

---

## Plano A1 revisto

**Aviso em voz alta:** o passo 2 do plano do documento («compor o hook»), feito na ordem escrita e sem o passo 0 abaixo, produz um nó que arranca verde e **nega todas as tool calls**. Falha fechada e nomeada no WORM (não é silenciosa), mas é um arranque partido que o plano actual não avisa.

| # | Passo | Depende de | Esforço | Muda face ao documento |
|---|---|---|---|---|
| **0** | Decidir D-A1.1 e D-A1.2 (âmbito e dimensão). Sem isto o texto do banner não é escrevível. | — | decisão | **NOVO.** O documento assume o âmbito. |
| **1** | Corrigir o próprio A1: alínea (c) remete para o item 5a; `PROVADO` distingue «testado» de «alcançável no nó»; `RISCO` acrescenta a conclusão «o fecho descrito não cobre o gasto de inferência»; esforço 4 distingue por-processo de durável. | 0 | pequeno | **NOVO.** É trabalho de documento, não de código. |
| **2** | Linha de banner no molde «capacidade ausente + eixo»: «orçamento: NÃO COMPOSTO — sem tecto de gasto (eixo AOS-008)». | 1 | pequeno | Igual ao documento, mas com o **texto** definido pelo passo 1 e sem sentinela `ErrBudgetStub` (que hoje bricaria os guard-tests). |
| **3** | **Fail-closed no arranque do breaker** (D-A1.4 opção B) + retirar/anotar as envs de velocidade na doc. | — (independente) | pequeno | **NOVO e prioritário.** É a única acção que remove risco hoje; não está no plano do A1. |
| **4** | Ciclo de vida do nó de orçamento por-run: `AddNode(goal.RunID, treeID, limite)` + `defer` de libertação no seam que já existe, `packages/integration/secured.go:460`. | 0 | pequeno | **NOVO.** Ausente do plano; sem ele o passo 5 nega tudo. |
| **5** | Campo de orçamento em `SecuredConfig` + compor `budget.NewBudgetCheck` no lugar de `BudgetStub{}` (`secured.go:324`) **no mesmo commit** que o passo 6. | 4 | médio | Âmbito corrigido. |
| **6** | `Settle` como **decorator do `ActivityDispatcher`** (`WithActivityDispatcher`, padrão de `secured.go:399-401`), não no loop — cobre também o caminho de erro de `runtime_ports.go:293-294`. | 5 | médio | **MUDA** a alínea (b): o loop não precisa de ser tocado, e o Settle passa a viver ao nível do Reserve nas duas vias. |
| **7** | Envs `AOS_BUDGET_*` (tokens; $ só depois de D-A1.2/B) + linha de banner promovida a «composto». | 5,6 | pequeno-médio | Igual, com a dimensão condicionada. |
| **8** | Teste ao nível do nó que prove um **permit** de tool call com budget ligado (não só o deny), e uma fuga não-existente após negação do egress. | 6 | pequeno | **NOVO.** O documento só lista testes in-process do pacote. |
| **9** | Estimador real: **requer costura nova** — `rm.Call` não transporta prompt/tokens, logo `WithEstimator` sozinho não chega. Alternativa mais barata: estimar fora do RM e passar por `CallContext`. | 5 | médio-grande | **MUDA** o esforço 3 do documento, que assume seam suficiente. |
| **10** | Eixo micro-USD ponta-a-ponta: campo de custo em `port.Usage`/`ChatResponse`, `translateResponse`, `Cost` em `ProductionConfig`, `WithCost` no wiring. | D-A1.2/B | médio | **MUDA** o documento, que só diz «ligar o `WithCost`». |
| **11** | Ponto de admissão do **turno de modelo** (D-A1.1/B): reservar antes de `loop.go:549`, saldar com o custo real. | 10 | médio-grande | **NOVO.** É o passo que fecha o risco que o A1 invoca. |
| **12** | Durabilidade: reserva indexada por `run_id:step_id` no evento, reconciliação/TTL no `Rebuild`, e um consumidor de recuperação. | 6 | grande | Substitui «backend distribuído (grande/deferido)» por um pré-requisito nomeado *antes* de multi-réplica. |

Ordem mínima defensável se só houver apetite para um lote: **1 → 3 → 2**. Entrega honestidade e remove uma armadilha, sem ligar enforcement cego.

---

## O que foi REFUTADO

- **«"Só `MaxTurns` como travão" está desactualizado e o documento mente.»** Falso: é um parêntese a citar um run histórico. O disjuntor de wall-clock (30 min) e no-progress estão compostos; o que o documento **já diz** noutro item é que `observeAction` (`breaker_wiring.go:83`) tem zero chamadores, o que confirmei.
- **«O A1 esconde a dependência do `WithCost`.»** Falso: o campo RISCO do A1 e o campo COSTURA do A2 declaram-no textualmente («sem `WithCost` no gateway, o burn-down não tem dados» / «bloqueio duplo … só mostraria 0/0»).
- **«Ligar (a) sem (b) troca "sem tecto" por brick determinístico.»** Metade falsa: o caminho «permit sem Commit» não é fuga (`available = limit − reserved − committed`; commit só move o montante). A fuga real são as negações a jusante e os erros.
- **«O `Rebuild` ressuscita headroom consumido e drena a sub-árvore até tudo negar.»** Não alcançável: `Rebuild` e `budget.New` têm zero chamadores não-teste e o `Budget` não tem API de re-hidratação. O efeito real após restart é o inverso — contadores a zero, fail-open.
- **«A retoma duplica o custo no WORM/Event Store.»** Falso: `stepID` determinístico + dedup por `run_id:step_id` no store. Sobrevive só a dupla-contagem em agregações **por run** sobre múltiplos traces, hoje latente.
- **«Uma linha de banner de budget seria a única sem acção correspondente, logo é teatro» / «o `BudgetStub` é o único que escapa a `NewProductionSecure`» / «é mais permissivo que os outros».** Todas falsas: o banner já tem linhas de capacidade ausente com eixo; `PolicyStub` e `AuditStub` escapam igualmente ao `containsHook`; `BudgetStub` e `EgressStub` têm a mesma forma.
- **«Não existe seam por-run onde chamar `AddNode`.»** Falso: `packages/integration/secured.go:460` é exactamente esse seam e já corre um ciclo de vida por-run sobre `goal.RunID`.
- **«`WithEmitter` omitido é o mesmo padrão de fail-open que o breaker.»** Falso: `budget.go:83-84` documenta o default explicitamente; o breaker **engole um erro**. São coisas diferentes.
- **«"Pré-requisito: A1" está trocado no A2.»** Falso: o denominador do burn-down vem do `BudgetReader` (`surface.go:92`) e `consumedFraction` devolve 0 sem limite. A1 é mesmo pré-requisito. E com limite em **tokens** o burn-down funciona com o GW real — só a dimensão $ lê 0.
- **«Fechar o A2 arrasta o scheduler para o grafo do nó e parte o guarda ADR-018.»** Falso: as portas podem ser nil por contrato, são interfaces, e `control-plane/budget` não está na lista proibida de `boundary_orq_sch_test.go`.
- **«O `DefaultEstimator` usa uma tarifa (10 µUSD/tok) que não corresponde a nenhuma célula da tabela de preços.»** Factualmente errado: `pricing_table.json`, `gpt-4o`/`us-east`, output = exactamente 10 µUSD/token.
- **«Sem headroom o modelo re-tenta até `MaxTurns` e não há terminação.»** Desactualizado: o action-dedup (janela 8, limiar 3) está cablado e pára uma repetição idêntica ao 3.º turno. Sobrevive só o caso da **reformulação** (hash diferente), onde resta o wall-clock.
- **«`AOS_MODE=production` deve recusar o `BudgetStub` pelo molde de `ErrProductionNeedsDurableApproval`.»** A leitura do molde está certa mas não é nova: `production.go:175-180` já testa por tipo e `NewProductionHardenedTaint` já é o molde do «construtor mais estrito adoptado quando o real existir».
---

## Ver também

[`desafio-A2-progress-surface.md`](./desafio-A2-progress-surface.md) — a avaliação gémea do
subtópico A2, que depende deste. Dois achados desta avaliação são lá reutilizados como
factos verificados: a ausência de canal de custo em `port.Usage`, e o trace novo por
incarnação na retoma.

[`desafio-A3-credential-broker.md`](./desafio-A3-credential-broker.md) — o terceiro da série.

## Rastreabilidade

Transcrições por agente: `.claude/projects/…/subagents/workflows/wf_5587aefe-a93/journal.jsonl`
(uma linha `{"type":"result",…}` por agente, com o valor de retorno completo).
Script do fluxo: `…/workflows/scripts/desafio-a1-budget-wf_5587aefe-a93.js`.

> **Nota de âmbito:** este relatório NÃO altera `prontidao-modelos-agenticos.md`. A secção
> A1 avaliada faz parte de trabalho ainda por committar de outro autor; as correcções
> propostas ao texto do A1 estão no passo 1 do plano revisto, para quem o escreveu decidir.
