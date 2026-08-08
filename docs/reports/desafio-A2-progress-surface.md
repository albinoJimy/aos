# Desafio ao plano A2 — progress-surface (AOS-123)

> **O que é:** avaliação adversarial do subtópico `#### A2. progress-surface (AOS-123)` de
> [`prontidao-modelos-agenticos.md`](./prontidao-modelos-agenticos.md), na secção *«Análise
> aprofundada por tópico (2026-08-08)»*. Gémeo de
> [`desafio-A1-budget-admission-control.md`](./desafio-A1-budget-admission-control.md), com
> o mesmo método. O objectivo declarado foi **desafiar** o plano, sobretudo o que ele dá
> por «em falta».
>
> **Data:** 2026-08-08 · **HEAD avaliado:** `2f234bb` (branch `feature/AOS-128-ux-dx-tests`)

## Proveniência e método

Seis lentes **independentes** correram contra o código, não contra a documentação. Cada uma
foi seguida de um **céptico cuja missão era refutá-la**: um achado só ficou CONFIRMADO se
não foi possível derrubá-lo, e a severidade foi revista em baixa por omissão.

| lente | a pergunta que só ela fez |
|---|---|
| `factos` | as 4 portas têm assinatura **compatível**, ou «faltam só os adaptadores» é optimista? |
| `hitl` | o AOS já tem um caminho humano — o A2 é um **segundo**, e ninguém os comparou |
| `autoridade` | `extend` levanta o tecto de gasto: isso é decisão de **autorização**, não de UI |
| `falha` | o que acontece quando o humano **não** responde, com o wall-clock a correr |
| `números` | a retoma cria um **trace novo** — o que mostra uma barra agregada por trace? |
| `executável` | `summarize_stop` e `abort` — o nó sabe **executá-los**? |

13 agentes, 0 erros, 388 chamadas a ferramentas, ~18 min.

### O que foi verificado por quem

Reconferi **à mão**, depois da síntese e antes de relatar, os quatro achados que sustentam o
veredicto:

| achado | comando de verificação | resultado |
|---|---|---|
| as 3 opções **não** são delegadas | `decision.go:47-50` | `case OptionSummarizeStop, OptionAbort:` emite um span e retorna — só `extend` delega |
| o timeout **não existe** | nenhum ficheiro não-teste do pacote importa `time` | confirmado: há o *hook*, não há o relógio |
| o burn-down leria 0 por falta de **spans** | `bootstrap.go:858` → `otelgenai.NoopTracer{}` | confirmado |
| os adaptadores nomeados violam o ADR-018 | `boundary_orq_sch_test.go:25-28` lista `control-plane/scheduler`; `ports.go:12-15` aponta-lhe | confirmado |

O resto vem das lentes já passadas pelo céptico; a síntese marca onde a verificação foi
indirecta.

### Uma correcção ao que a síntese afirma

A síntese classifica o achado sobre `observeAction` (código morto ⇒ o sinal *no-progress*
nunca dispara) como **novo face ao documento**. **Não é.** O relatório de prontidão já o
regista, com as linhas certas, no item **5b** da secção *«Correcções ao inventário»*. O que
esta avaliação acrescenta é a sua **promoção a pré-requisito** — e a ligação causal ao A2:
é precisamente o sinal que travaria o *deny-loop* que a exaustão de orçamento produziria.
Fica registado aqui para não inflacionar a novidade.

## Sumário executivo

1. **«3 opções delegadas ao admission» é falso.** Só `extend` tem porta; `summarize_stop` e
   `abort` emitem um span e devolvem a decisão «ao orquestrador» — que não está composto no
   nó. Duas das três opções que o A2 promete ao humano não têm executor.
2. **«Degradação graciosa por timeout» existe como gancho, não como comportamento.** O
   pacote não tem relógio: `OnPromptTimeout` é um callback que alguém tem de agendar.
3. **O diagnóstico do «0/0» está errado no mecanismo.** Não é (só) a ausência de budget e de
   `WithCost`: com o tracer Noop por omissão **não há spans nenhuns**, e `ComputeBurndown` é
   um lookup de mapa que devolve zero **sem erro**. Uma superfície verde a mentir.
4. **O «fix» óbvio parte outra coisa.** Ligar `WithCost` resolve o custo e passa a contar os
   **tokens a dobrar** — dois spans `chat` no mesmo trace, e `AggregateByTrace` soma ambos.
5. **O transporte proposto é impossível.** «Devolução da decisão via SSE»: o SSE é
   servidor→cliente e está declarado fora do plano de controlo (não-autenticado, ADR-016).
   Serve para **mostrar**, nunca para **receber**.
6. **Os adaptadores nomeados fariam vermelho o guarda ADR-018.** As portas em si são
   *scheduler-free* — a saída é um adaptador node-local, não uma emenda de ADR.
7. **`extend` é a concessão mais forte pelo caminho mais fraco:** levanta o tecto de gasto
   sem exigir a assinatura que um simples `pause` já exige.

---


*HEAD `2f234bb`, branch `feature/AOS-128-ux-dx-tests`. Todas as citações verificadas no código; onde a verificação foi indirecta, está dito.*

## Veredicto sobre o A2 tal como está escrito

| Alegação do documento | Veredicto | Evidência |
|---|---|---|
| **Propósito:** «burn-down lido da agregação EPIC-08 (sem recontabilizar)» | **exacto** | `burndown.go:33-43` é literalmente `otelgenai.AggregateByTrace(spans)[traceID]` — não há re-soma. |
| **Propósito:** «3 opções … delegadas ao admission» | **errado** | Só `extend` tem porta. `decision.go:47-50`: `case OptionSummarizeStop, OptionAbort:` emite um span e devolve `res` — não toca em porta nenhuma. As 4 interfaces de `ports.go:22-88` são leitura, extensão, degradação e reflexão; nenhuma pára nem aborta um run. |
| **Propósito:** «degradação graciosa por timeout» | **incompleto** | Existe o *hook*, não existe o *timeout*. Nenhum dos 8 ficheiros não-teste do pacote importa `time`; `OnPromptTimeout` (`decision.go:61-67`) é um callback que alguém tem de agendar — os únicos chamadores são `progress_surface_test.go:319,336` e `qa/ux-dx/usability_test.go:227,242`. |
| **Provado:** os 4 testes + cobertura UX | **exacto, com uma ressalva** | Os quatro existem e provam o que nomeiam. Ressalva: `TestResolvePrompt_Extend_DelegatesAndDoesNotMutateBudget` (`progress_surface_test.go:199-241`) assere `ext.called==1` e a **não-mutação** do reader — isto é, assere precisamente a ausência do efeito que faltaria (ver §2, achado D). |
| «As 4 portas têm implementações concretas do outro lado» | **incompleto (verdadeiro à letra, falso na implicação)** | Os quatro tipos existem: `scheduler/spawn_admission.go:133` (`HeadroomController`), `scheduler/degradation.go:801` (`ExecuteChain`), `governance/control-surface/reflection.go` (`StateProjector`), `budget.Budget`. Mas duas contrapartes não são **semanticamente** compatíveis: `AdmitRequest` (`scheduler/admission.go:138-152`) = `{Key, Tenant, Board, EstimatedTokens, RequestID}` — sem `TreeID`, sem `RunID`, sem dimensão de custo, pelo que `ExtensionRequest.Additional.CostMicroUSD` (`ports.go:39`) não tem destino; e `ProgressSnapshot.Step` (`ports.go:79`) não tem produtor nenhum em `packages/`. |
| «faltam só os adaptadores» | **errado** | Faltam, além dos adaptadores: (a) a decisão de **onde vive a extensão** (o `budget.Budget` não tem mutador de tecto — ver §2 D); (b) um **produtor/retentor de spans** (§2 C); (c) os **resolvedores** `traceID` e `treeID`, que o nó não tem (grep em `packages/cmd/aos/*.go` não-teste: zero ocorrências de `TraceID` e de `treeID`/`TreeID`; zero imports de `control-plane/budget` em `cmd/aos` e `integration`); (d) **estado/durabilidade do prompt** (§2 G); (e) o **dono do relógio** do timeout. |
| «zero consumidores de produção (só QA)» | **exacto** | Fora do próprio módulo, as únicas referências são `qa/ux-dx/{usability,accessibility,helpers}_test.go`, comentários de doc e linhas `replace` de `go.mod`. |
| «nenhuma env» | **exacto** | Verificado. |
| «a superfície composta só mostraria 0/0» | **incompleto e impreciso** | Impreciso: `consumedFraction` (`burndown.go:57-68`) toma o **máximo** de duas dimensões e o eixo **tokens** não depende do canal de custo — com `Limit.Tokens > 0` o burn-down funcionaria sem micro-USD nenhum. Incompleto e mais grave: hoje o numerador é 0 **por não haver spans de todo** — `bootstrap.go:858` é `tracer := otelgenai.Tracer(otelgenai.NoopTracer{})`, só substituído se houver `OTLPExporter`/`OTLPEndpoint`. E o 0 chega **sem erro**: `ComputeBurndown` é um lookup de mapa. |
| **Risco:** «o operador volta ao hard-stop cego — o run morre ao esgotar sem aviso nem escolha» | **errado no mecanismo, certo na conclusão** | Com A1 fechado o run **não morre**: `BudgetCheck.Evaluate(ctx, call *rm.Call)` (`budget/rmadapter.go:87`) é um `rm.Hook` sobre **tool calls**; a chamada ao modelo é `rt.model.Call(chatCtx, view)` directa (`loop.go:549`), fora da cadeia. A exaustão produz um **deny-loop**: todas as tools negadas, o modelo continua a queimar turnos até `DefaultMaxTurns=16` ou ao wall-clock. E «cego» é falso: o disjuntor composto (`bootstrap.go:1141-1145`) pára com veredicto. O que se aguenta é «sem aviso nem escolha». |
| «Pré-requisito: A1» | **incompleto** | A1 é necessário mas não suficiente, e não é sequer o primeiro. Pré-requisitos reais: produtor de spans retentor; resolvedores `runID→traceID`/`treeID`; decisão do dono sobre o dono do tecto; canal de custo (ou assumir só tokens). |
| **Fecho 1:** «adaptadores das 4 portas + composição no nó (**médio**)» | **subestimado** | Os adaptadores nomeados em `ports.go:12-15` são sobre `control-plane/scheduler`, que `boundary_orq_sch_test.go:25-28` lista em `forbiddenLifecycleModules` e o segundo guarda verifica no **fecho transitivo** (`go list -deps .`), o que inclui `integration`. Segui-los à letra faz vermelho o guarda. As portas em si são scheduler-free (`ports.go:59-71` não referencia um tipo do scheduler), pelo que a saída é um adaptador *node-local* — re-desenho, não emenda de ADR. |
| **Fecho 2:** «ponto de invocação por turno + env do limiar (**médio**)» | **mistura sobrestimação e subestimação** | Sobrestima o **gancho**: a fronteira de fim-de-turno já existe e já é usada duas vezes com o padrão de opção aditiva — `rt.steer.GracefulPause`/`PendingCorrection` (`loop.go:469-495`) e `rt.breaker.Observe` (`loop.go:498-518`). Subestima tudo o resto: nada nesta linha cobre estado do prompt, durabilidade, relógio ou autoridade. |

## O que o documento NÃO diz e devia

### A. Relação com o caminho humano entregue: complementaridade na pergunta, **colisão no mecanismo** (média)

As duas perguntas **são diferentes** — a escalada decide a *autorização* de uma tool call sob veredicto de autonomia; o prompt decide a *continuação orçamental* do run. O que colide é o mecanismo:

- **A forma do pendente é moldada a uma tool call.** `escalation_sink.go:56-72` preenche `integration.PendingRecord{ToolID, Capability, ResourceType/Value/Region, Preview}`; o grant é amarrado ao digest da preview (`approval_broker.go:52-55`) e a retoma carrega `replayPlanFor` porque a aprovação está amarrada à preview da call original (`resume.go:91-96`). Um prompt de orçamento não tem call, capability, recurso nem preview: **não pode reutilizar `POST /approve` nem `POST /resume` tal-qual**.
- **Logo não é visível.** `GET /runs/{id}` devolve só `PendingApprovals` (`api.go:762-765,800-806`). Um prompt de exaustão não apareceria em lado nenhum.
- **A colisão NÃO é na tabela de estados** (ver §5): `{Running, WaitingOnHuman}` existe (`transitions.go:90`) e `resumeIfWaiting` (`steer_gates.go:157-161`) já desarmou a re-escalada.

**Consequência accionável:** ou o prompt de exaustão ganha um **segundo tipo de `PendingRecord`** (sem preview, amarrado ao par run+limiar), ou não tem durabilidade nem visibilidade nenhuma.

### B. `observeAction` sem chamador — o **único defeito vivo em produção** desta avaliação (alta, e é ortogonal ao A2)

`runBreakers.observeAction(runID, hash)` (`breaker_wiring.go:83-88`) é a única via para `progress.Observe`, e um grep em `packages/` devolve **apenas a própria definição** — zero chamadores. Sem observações, `Detector.MadeProgress` devolve sempre true, `b.stale` é reposto em cada `Observe` e `MaxStaleIterations=3` (ligado por omissão, `breaker_thresholds.go:39-43`) **nunca dispara**. Pior: o detector é *armado* (`Threshold: 3` em `breaker_wiring.go:45`), pelo que a guarda fail-closed de `NewBreaker` passa **vacuosamente** — o nó arranca convencido de que o sinal protege. É exactamente o sinal que o cabeçalho de `breaker_thresholds.go:11-14` justifica como «o sinal com evidência directa» para poupar turnos num run que repetia a mesma tool call negada — a patologia que o deny-loop de orçamento (§1, linha do RISCO) produziria. **Ticket próprio, independente de A1/A2, minutos de trabalho.**

### C. A entrada de dados do burn-down não é porta nenhuma e não tem produtor (alta para o A2)

`Evaluate` recebe `spans []otelgenai.SpanData` como **parâmetro** (`surface.go:88`); as quatro portas de `ports.go:22-88` não produzem spans. No nó: `bootstrap.go:858` é `NoopTracer` por omissão, e o `SpanTracer` **dispara e esquece** (`spantracer.go:143`, `_ = s.tracer.exporter.Export(...)`). Não existe retentor de spans em `packages/cmd/aos` nem em `packages/integration`. Com A1 fechado e os 4 adaptadores escritos, `Evaluate` recebe nil → `AggregateByTrace` devolve zero-value → `Fraction=0` → o limiar **nunca dispara, sem erro**. Verde a mentir. O fecho barato é um **decorador de `Exporter` que retém** (a montante da interface, imune aos drops de `otlpexporter.go`), não pipeline nova.

### D. `extend` não consegue mover o denominador que a própria superfície lê (média-alta)

O denominador é `BudgetReader.Limit` (`surface.go:92` → `ports.go:22-27`), documentado como `budget.Budget.Snapshot()[treeID].Limit`. A superfície pública de `*Budget` é `New:95, AddNode:115, TreeID:133, Available:136, Reserve:165, Commit:224, Release:257, Snapshot:293` — **não existe mutador de tecto**; as únicas atribuições a `.limit` são `budget.go:105` (construção) e `budget.go:128` (nó filho novo). Reforço: `budget/events.go:107-109` declara que limites e topologia são **configuração**, não reproduzida por `Rebuild` — mesmo que existisse mutação, não seria durável. Do outro lado, `Admit` reserva num token-bucket de provider (`admission.go:155-173`), outro plano, como o próprio `ports.go:37-38` admite por escrito.

Combinado com a **ausência de latch** (`surface.go:14-22` não tem campo de prompt; `surface.go:103-111` emite `aos.control.exhaustion_prompt` a cada `Evaluate` acima do limiar; `ResolvePrompt` devolve por valor sem escrever no receptor): um `extend` com `Granted=true` deixa a fracção onde estava e o prompt **re-dispara em todos os turnos seguintes**. Falsificável com um teste de duas chamadas a `Evaluate` à volta de um `ResolvePrompt(OptionExtend)`.

### E. O «fix» óbvio do custo **duplica os tokens** (média — verificado ponta a ponta por mim)

Ligar `WithCost` + `Tracer` na composição do gateway (`modelgatewaywiring.go:128-140` não passa nenhum dos dois; `ProductionConfig.Tracer` existe em `production.go:147` e aplica-se em `:242-243`) resolve o custo **mas** cria **dois spans `chat` no mesmo trace**:

- runtime: `loop.go:536` abre `OpChat` e escreve `AttrInputTokens`/`AttrOutputTokens` (`loop.go:554-555`) + `AttrCostMicroUSD` = 0 (porque `translateResponse`, `runtime_adapter.go:88-95`, só povoa `PromptTokens`/`CompletionTokens`);
- gateway: `gateway.go:300` abre outro `OpChat` no ctx filho, `gateway.go:343` chama `finishResponse`, que em `gateway.go:547` chama `setUsageAttrs` → escreve **os mesmos tokens**, e `recordCost` (`gateway.go:333` → `metering/cost/recorder.go:322-323`) escreve o custo real.

`AggregateByTrace` (`cost_aggregation.go:156-166`) soma **todo** o span `chat` do trace sem deduplicar por parentesco (o `parentHex` existe, mas só é usado na agregação por sub-árvore, `cost_aggregation.go:180-222`). Resultado: custo correcto, **tokens a 2×**. É este o defeito a acautelar, não o zero em dólares.

### F. A retoma parte o burn-down em dois traces e o nó não sabe nomear nenhum (média)

`loop.go:274-281` só herda o trace se `goal.ParentTraceParent != ""`, e no nó nada o preenche (a única ocorrência não-teste é a cópia em `service.go:591` → `integration/resume_records.go:69`, vazia para um run submetido pela API); `resume.go:110-117` **re-submete** o run e `resume_model.go:70-76` devolve a `ModelResponse` registada, que `loop.go:554-562` volta a escrever num span `chat` novo. Logo um run retomado tem ≥2 traces: T1 com os turnos 1..k, T2 com 1..k reproduzidos + k+1..n. Escolher T2 **perde** o prefixo; somar ambos **conta-o duas vezes**. E `Evaluate` (`surface.go:88`) exige um `traceID` que o nó não produz. Alternativa que remove o problema de raiz: ler o burn-down do ledger durável indexado por turno (`TurnRecord{Usage, CostMicroUSD}`, `agent-runtime/turn.go`), que é *last-wins* por turno e imune à re-emissão.

### G. Zero estado, zero durabilidade, zero relógio (média — é uma correcção de **estimativa**)

`ProgressSurface` (`surface.go:14-22`) tem sete campos fixados em `New` e nunca mutados; `PromptWarned` (`prompt.go:51`) é literalmente inerte (só a definição e o `String()`); nenhum ficheiro do pacote importa `eventstore` (entra só como `// indirect` no `go.mod`, arrastado pelo budget). Um restart perde o prompt sem rasto, `OnPromptTimeout` nunca dispara, e a mesma decisão gera N prompts. Nada disto está nas duas linhas de «Fecho».

### H. Autoridade e atribuição: `extend` é a concessão mais forte pelo caminho mais fraco (média)

`ExtensionRequest` (`ports.go:32-42`) não tem principal, assinatura, sessão nem nonce; `emitDecisionSpan` (`span.go:69-81`) grava `run_id` + opção + `extension_granted` + `degrade_reason` e **nenhum identificador de quem decidiu** — e vai para o tracer, que por omissão é Noop. Contraste: `SteerChannel.newRecord` (`steer_channel.go:296-310`) sela `EmitterID` + assinatura no evento; `Ed25519Authenticator.Authenticate` (`steer_authenticator.go:187-228`) exige pubkey registada default-deny, assinatura sobre `run_id‖kind‖payload‖nonce‖issued_at`, janela de frescura e `ConsumeNonce` durável; `approval_broker.go:43-62` amarra o grant ao digest, uso-único e TTL.

Duas notas de honestidade: (i) não há convenção de principal-em-contexto no repositório — as únicas `context.WithValue` não-teste são `resume_model.go:42`, `otel-genai/spancontext.go:43` e `sandbox/network/correlation.go:21` —, pelo que o adaptador **não pode** ir buscar o principal ao `ctx`; (ii) a falta de principal em transições manuais é pré-existente (`breaker.manualTransition`, `breaker.go:296-333`, também não regista quem). Mas o `Admit` também não transporta `RunID`/`TreeID`/`Reason` (`admissionPayload`, `admission.go:108-122`): o evento durável do admission existiria **sem chave de join** para o run nem para o humano.

### I. Meia solução para `summarize_stop` e nenhuma para `abort` — mas o mecanismo existe (média)

`decision.go:47-50` devolve ao chamador e mais nada. No nó: `breaker.Abort` (`breaker.go:289`) tem **zero chamadores não-teste**; `saga.NewSagaCoordinator` (`saga/compensation.go:139`) tem zero chamadores fora do próprio pacote (a compensação nunca corre); não há rota de abort/cancel (`api.go:483-500` expõe `steer`/`pause`/`approve`/`resume`). **O que existe e serve:** a pausa graciosa é durável, composta e autenticada (`loop.go:474-482` → `running→paused`; `POST /runs/{id}/pause`, `api.go:492`). Falta a **porta** na progress-surface, não o mecanismo. Para o «summarize», o único canal é `SteerSource.PendingCorrection` (`steer.go:21-23`), **advisory** — o modelo pode ignorá-lo, e o prompt devia dizê-lo.

## Decisões que são do dono (não minhas)

**(i) O A2 é um segundo caminho humano ou reutiliza o que existe?**

- *Opção A — segundo caminho próprio:* prompt e resolução com transporte, durabilidade e TTL próprios. Trade-off: duas semânticas de espera humana, dois registos, dois relógios, duas superfícies de autenticação a manter e a auditar; o custo de manutenção é permanente.
- *Opção B — reutilizar a maquinaria da escalada:* o prompt torna-se um **segundo tipo de `PendingRecord`** (sem preview; amarrado a run+limiar+montante), o run suspende em `waiting_on_human` pelo `runGate` já existente (`steer_gates.go:140-147`), aparece em `GET /runs/{id}`, e a decisão entra por uma rota de controlo autenticada. Trade-off: obriga a generalizar `PendingRecord` e `handleApprove`, hoje moldados ao digest da preview (`approval_broker.go:52-55`) — trabalho real, mas num sítio só.
- *Opção C — reduzir o A2 ao executável:* burn-down + aviso, sem opções de decisão, até haver dono do tecto.

**Recomendação (marcada como recomendação):** B, com C como primeira entrega. A opção A cria um segundo mecanismo de decisão humana **mais fraco** do que o que acabou de ser entregue — sem lease, sem pendente durável, sem assinatura — e isso é uma regressão de postura, não uma funcionalidade.

**(ii) Que autoridade se exige para `extend`?**

- *Opção 1 — nenhuma (como está):* insustentável, é a concessão de gasto pelo caminho mais fraco do nó.
- *Opção 2 — paridade com `pause`:* Ed25519 de operador registado + nonce durável + frescura, reutilizando `Ed25519Authenticator` e `AOS_OPERATORS` (já compostos). Implica levar o principal verificado até ao `ExtensionRequest`, ou instanciar o `BudgetExtender` **por-principal** no wiring (o padrão do `runGate`, que já detém o fencing token — `steer_gates.go:141-146`).
- *Opção 3 — four-eyes acima de um limiar de valor:* `integration/foureyes.go:14-51`, duas pernas distintas em principal/sessão/credencial.

**Recomendação (marcada como recomendação):** Opção 2 como piso obrigatório, com a Opção 3 acima de um limiar de montante a definir pelo dono. Em ambos os casos, o adaptador escreve o seu **próprio registo WORM** (principal, run_id, tree_id, montante, razão, resultado) — o `Admit` não tem campos para o transportar.

**(iii) Quem é o dono do tecto?** Decisão prévia a qualquer adaptador: (a) o `budget` ganha uma mutação de tecto auditada e reconstruível por `Rebuild` (hoje inexistente e explicitamente fora do modelo de eventos, `events.go:107-109`); ou (b) `extend` é redefinido como *nova incarnação com novo tree budget*, encaixando na retoma já entregue; ou (c) `extend` sai das opções apresentadas. **Recomendação:** (c) na primeira entrega, (a) como ticket de `budget` com o seu próprio evento.

**(iv) Onde vivem os adaptadores face ao ADR-018?** `ports.go:12-15` aponta para o `scheduler`, proibido no fecho transitivo do nó. Ou adaptadores *node-local* (as portas permitem-no), ou colaborador fora do processo. **Recomendação:** node-local — o próprio guarda antecipa isto por escrito (`boundary_orq_sch_test.go:20-23`).

## Plano A2 revisto

**Ordem, dependências, esforço.** O que muda face ao documento está marcado.

0. **[SAI DO A2 — ticket próprio, PEQUENO, sem dependências]** Ligar `breakers.observeAction(runID, hash)` no ponto onde o Reference Monitor já anota o hash canónico no span `execute_tool` (`breaker_wiring.go:9-11` descreve a intenção), + teste negativo que falhe se o registo voltar a ficar sem chamadores. **Novo face ao documento.** É o único defeito vivo aqui e é pré-requisito de qualquer discussão sobre «o que trava um run que queima».

1. **[DECISÕES DO DONO — bloqueante]** (i), (ii), (iii), (iv) acima. **Novo face ao documento**, que classifica tudo como trabalho mecânico. ⚠️ **Perigoso na ordem do documento:** começar pelos «adaptadores das 4 portas» sem (iii) produz um `extend` que devolve `Granted=true` e não move nada — uma concessão vazia, pior que uma recusa, e um re-prompt por turno.

2. **[PEQUENO]** Retentor de spans por-run: decorador de `Exporter` que retém `SpanData` com política de retenção e query por trace, injectado via `cfg.OTLPExporter` (`bootstrap.go:835-838`). **Novo face ao documento.** Sem isto, todos os passos seguintes entregam uma superfície verde a mentir (§2 C).

3. **[PEQUENO]** Canal de custo: passar `Tracer` + `WithCost` em `modelgatewaywiring.go:128` (campos já existentes em `ProductionConfig`). ⚠️ **Perigoso:** faz duplicar os tokens (§2 E) — tem de sair no **mesmo** commit que a deduplicação por parentesco em `AggregateByTrace`, ou que a supressão do span `chat` do runtime. **Corrige o documento**, que diz «falta o `WithCost`» sem dizer o que isso parte. E **não** exige tocar em `port.ChatResponse` nem em `translateResponse`.

4. **[MÉDIO]** Resolvedores de identificador no nó: `runID→traceID` (com política explícita para runs multi-incarnação, §2 F) e `runID→treeID` (que hoje não existe: zero imports de `control-plane/budget` em `cmd/aos`/`integration`). Alternativa preferível: `BurndownSource` sobre `(runID, turn)` a partir do ledger de turnos, que dispensa o traceID. **Novo face ao documento.**

5. **[MÉDIO]** A1 — orçamento composto. **Mantém-se, mas deixa de ser o primeiro pré-requisito.**

6. **[PEQUENO-MÉDIO]** Adaptadores **só de leitura**: `BudgetReader` sobre `budget.Budget.Snapshot()` e `ProgressReflector` sobre `controlsurface.StateProjector`. ⚠️ `ProgressSnapshot.Step` não tem produtor — ou se acrescenta um `CurrentStep` ao control-surface, ou o campo fica vazio e diz-se isso. **Corrige o documento**, que trata as 4 portas como um bloco: estas duas são adaptáveis hoje, as outras duas não.

7. **[PEQUENO]** Ponto de invocação e env: `Evaluate` na fronteira de fim-de-turno já existente (`loop.go:469-518`), com o padrão de opção aditiva (`WithSteerSource`/`WithLivenessBreaker`), + `AOS_PROGRESS_THRESHOLD` que **recusa arrancar** com valor inválido (padrão `ErrBadBreakerThresholds`, `breaker_thresholds.go:22-34`) — **não** o padrão silencioso de `WithThreshold` (`surface.go:30-36`). **Reduz** a estimativa do documento de MÉDIO para PEQUENO. Nesta fase o A2 é **só burn-down + aviso**: ainda não há decisão a tomar.

8. **[GRANDE]** Prompt durável: latch por-run (armado/resolvido), `PendingRecord` de segundo tipo, suspensão de estado, TTL varrido pelo `approval_sweeper`, rota de controlo autenticada, registo WORM com principal. **Substitui inteiramente** a segunda linha de «Fecho» do documento. ⚠️ **Perigoso na ordem do documento:** «devolução da decisão via SSE» é **impossível** — `GET /runs/{id}/trajectory` (`api.go:486`) é servidor→cliente e `api.go:170` declara-o fora do plano de controlo (não-autenticado por ADR-016). O SSE serve para **mostrar**, nunca para receber. ⚠️ Se a deliberação deixar o run em `running`, herda o wall-clock de 30 min (`breaker_thresholds.go:42`, ligado por omissão) com precedência terminal, e a atribuição no WORM dirá `wall_clock`, nunca «o humano demorou» — a suspensão durável repõe o `enteredAt` (`machine.go:513`) e resolve isto por construção.

9. **[MÉDIO]** Executores das opções: 5.ª porta de terminação adaptada sobre a pausa graciosa durável já composta (para `abort`), e decisão explícita sobre `summarize_stop` (advisory via `PendingCorrection`, e dizê-lo no prompt, ou modo de terminação real no loop). **Novo face ao documento.** ⚠️ Se `abort` for adaptado sobre `running→failed`, os efeitos já aplicados ficam **sem compensação** — `SagaCoordinator` não está composto.

10. **[BLOQUEADO por (iii)]** `extend` — só entra depois de existir dono do tecto e autoridade decidida.

**Nota transversal:** `BudgetReader.Available` (`ports.go:26`) nunca é chamado — `Evaluate` só usa `Limit` (`surface.go:92`). Ou se usa, ou se remove, antes de obrigar um adaptador a implementá-lo.

## O que foi REFUTADO

Registado para ninguém repetir:

1. **«O prompt reutilizado como `waiting_on_human` mataria o run pelo par ausente `{WaitingOnHuman, WaitingOnHuman}`.»** Falso na prática: um run em `waiting_on_human` não executa turnos (`loop.go:428-435` devolve imediatamente após `Escalate`), pelo que `Evaluate` por turno só dispara em `running` ⇒ o par pedido é `{Running, WaitingOnHuman}`, que existe (`transitions.go:90`). E a re-escalada após retoma já está desarmada por `resumeIfWaiting` (`steer_gates.go:157-161`).
2. **«O TTL fail-closed mataria o run à espera do prompt.»** Falso no HEAD: `humanTTL` é 0 por omissão e as duas construções de máquina do nó (`steer_gates.go:55,75`) não passam `WithHumanApprovalTTL`; além disso `CheckDeadlines` **não tem chamador de produção**. O modo de falha real é o inverso — suspensão indefinida sem varrimento. (Isto é um achado **ortogonal**, sobre o caminho humano já entregue: o sweeper expira o **pendente**, não o **run** — `approval_sweeper.go:8-12`.)
3. **«O operador decidiria sobre dinheiro inventado (custo de referência de 1500 µUSD).»** Falso no caminho por omissão: sem exporter o tracer é Noop e não há span nenhum; o custo de referência exige `cfg.Model == nil` + observabilidade ligada + retenção cablada, e está declarado como tal no código.
4. **«A superfície não se queixa quando as portas faltam.»** Falso: falha fechada e ruidosamente à primeira chamada — `ErrNilBudgetReader` (`surface.go:89-91`), `ErrNilBudgetExtender` (`decision.go:37-39`), `ErrNilDegrader` (`decision.go:62-64`). O que resta é detecção em tempo-de-chamada em vez de tempo-de-construção. O único silêncio genuíno é `Limit==0 ⇒ Fraction 0` (`burndown.go:57-68`).
5. **«`AOS_PROGRESS_THRESHOLD=80` daria 0.80 sem o operador saber.»** O exemplo auto-destrói-se: a env não existe, e `validThreshold(80)` é falso, pelo que cai no `DefaultThreshold = 0.80` — exactamente o que o operador queria.
6. **«A agregação por-run duplicaria o gasto e a barra dispararia com o dobro.»** Não alcançável no HEAD: `BuildRunView` (`catalog.go:919`), o único agregador que soma sobre todos os traces, tem **zero chamadores não-teste**. O mecanismo da duplicação é real (§2 F); a consequência alegada não é.
7. **«O `extend` traria dano colateral permanente na quota alheia.»** Sobrevendido: o bucket tem refill temporizado e `HeadroomController.Release` existe — o colateral é limitado a uma janela.
8. **«Compor os adaptadores exige emenda ao ADR-018.»** Não: as portas são scheduler-free (`ports.go:59-71`), `ports.go:12-15` é um comentário de intenção, e o próprio guarda antecipa por escrito o colaborador dedicado (`boundary_orq_sch_test.go:20-23`). É re-desenho de adaptador.
9. **«O documento está errado ao dizer que o ponto de invocação não existe.»** Não está: a frase é sobre `Evaluate`, que tem zero consumidores não-teste. A fronteira de fim-de-turno existe (`loop.go:469-518`), mas isso torna o documento **conservador**, não errado.
10. **«O `AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC` já dá o sinal de custo.»** Inverso: é uma armadilha — `breaker_wiring.go:70-72` constrói o breaker **sem** `VelocitySource`, `breaker.go:146-148` devolve `ErrVelocitySourceMissing` assim que o limiar é > 0, e `breaker_wiring.go:73-77` engole o erro — ligar a env **desliga o disjuntor inteiro**. Já registado como item 5a do relatório.
11. **«O deny-loop duraria 30 minutos.»** `DefaultMaxTurns = 16` (`loop.go:13`) fecha-o antes, tipicamente. Os 30 min só se tornam o travão se o cliente pedir `max_turns` alto — e `max_turns` vem do corpo do `POST /runs` sem limite superior (`api.go:525`), o que é um achado à parte.
12. **«O `Degrader` sobre `ExecuteChain` degeneraria por omissão num hard-stop cego.»** Duas correcções: a ordem e o item são construídos pelo adaptador, nada obriga ao zero-value; e o `Reject` emite `EventWorkRejected`, é fail-closed sobre razão vazia e devolve erro accionável — é paragem **ruidosa e auditada**, não silenciosa. Sobrevive só que a porta `Degrade(ctx, reason)` não transporta a classificação que `ExecuteChain` exige, e que `NewDegrader` recusa um router nil.
13. **«Ligar o custo não precisa de acautelar nada porque o gateway não escreve tokens.»** Falso — verifiquei: `Chat` chama `finishResponse` (`gateway.go:343`), que chama `setUsageAttrs` (`gateway.go:547`). Os tokens **são** escritos nos dois spans. Ver §2 E.
---

## Ver também

[`desafio-A3-credential-broker.md`](./desafio-A3-credential-broker.md) — o terceiro da série.

## Rastreabilidade

Transcrições por agente: `.claude/projects/…/subagents/workflows/wf_951d3a23-2b8/journal.jsonl`
(uma linha `{"type":"result",…}` por agente, com o valor de retorno completo).
Script do fluxo: `…/workflows/scripts/desafio-a2-progress-surface-wf_951d3a23-2b8.js`.

> **Nota de âmbito:** este relatório NÃO altera `prontidao-modelos-agenticos.md`. A secção
> A2 avaliada faz parte de trabalho ainda por committar de outro autor; as correcções
> propostas ao texto do A2 estão no plano revisto, para quem o escreveu decidir.
