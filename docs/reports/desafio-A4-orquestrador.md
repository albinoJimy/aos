# Desafio ao plano A4 — Orquestrador (ORQ, AOS-012 + EPIC-18/19)

> **O que é:** avaliação adversarial do subtópico `#### A4. Orquestrador` de
> [`prontidao-modelos-agenticos.md`](./prontidao-modelos-agenticos.md). Quarto da série,
> depois de [A1](./desafio-A1-budget-admission-control.md),
> [A2](./desafio-A2-progress-surface.md) e [A3](./desafio-A3-credential-broker.md).
>
> **Data:** 2026-08-08 · **HEAD avaliado:** `075ea87` (branch `feature/AOS-128-ux-dx-tests`)

## Este subtópico é diferente — e o método foi ajustado

Nos A1–A3 a pergunta era *«porque não está ligado»*. Aqui a exclusão é **deliberada e
imposta por um teste**, e o documento conclui: **«para a v1 single-node, nada»**.

Por isso instruí o céptico explicitamente: **um deferimento declarado não é um defeito**, e
que rejeitasse qualquer achado que se resumisse a *«isto devia estar ligado»* sem demonstrar
dano concreto. Só contavam quatro coisas: **costura morta**, **divergência entre duas
implementações do mesmo conceito**, **furo no guarda**, ou **afirmação factualmente errada**.

Sem essa instrução, seis lentes com licença para criticar produziriam uma lista de desejos.

## Resposta directa: o deferimento é honesto

**Sim.** Nada é prometido e não entregue — sem env, sem banner, sem capacidade anunciada. A
exclusão está registada em ADR-018 §5 e é **verificável**, não apenas afirmada: confirmei
com `go list -deps` que o fecho transitivo do nó contém **zero** pacotes sob
`control-plane/orchestrator` ou `control-plane/scheduler`.

O relatório podia terminar aqui. Não termina porque a avaliação encontrou **duas lacunas
graves no próprio nó** que a conclusão «fecho: nada» encobre por adjacência temática — e
uma atribuição de autoridade errada.

## ⚠️ Dois achados de severidade alta, nenhum causado pelo guarda

Ambos verificados à mão.

### 1. O circuit breaker é estruturalmente inerte no run comum

`breaker.go:213-215` — `Observe` devolve no-op se `!liveness.CountsAsActiveWork(machine.Current())`,
e `waiting_states.go:356` define `CountsAsActiveWork(s) = (s == state.Running)`.

Mas o lazy-claim de AOS-218 só reclama `ready→running` no **primeiro pause de steer**
(`steer_gates.go:117-118`) ou na **primeira escalada HITL** (`:141-142`). Um run que nunca é
steerado e nunca escala **fica em `ready` do princípio ao fim** — e o breaker provisionado
por `AOS_BREAKER_*` nunca acumula, nunca avalia, nunca dispara.

Inclusive no caso patológico que o motiva: uma sequência de negações não gera transição de
estado, logo não arma o breaker. É um **segundo** mecanismo de inércia, distinto do já
registado no item 5b do relatório (`observeAction` sem chamadores) — corrigir um sem o outro
não resolve.

### 2. A máquina durável nunca escreve `complete` nem `failed`

`grep state.Complete|state.Failed` em `cmd/aos` e `integration` (não-teste) devolve **vazio**.
A tabela declarativa tem 13 arestas; o nó conduz 5. E `Machine.CheckDeadlines` — que
materializa `running→timed_out` e o kill fail-closed de ADR-013 — tem **zero chamadores de
produção**, apesar de `liveness/doc.go:54-56` declarar que o consumidor «TEM de correr
periodicamente».

O desfecho vive num mapa em memória, com poda FIFO. **Um run que acaba por erro, panic ou
esgotamento de turnos é, no log durável, indistinguível de um crash a meio.** É a mesma
classe de problema do balde `suspended` corrigido nesta sessão — verdade operacional em
memória — mas mais largo.

## Uma atribuição de autoridade errada

O A4 escreve «para a v1 single-node, **nada** (é a Carta; fechar violaria o guarda)».

`grep -i "orquestrad|orchestrat|escalonad|scheduler" specs/00_AOS_Carta.md` devolve **zero
linhas**. A Carta é literalmente muda sobre ORQ/SCH. A proibição é de **ADR-018 §2/§5**, e o
ADR-018 §2 **permite** consumo in-run via colaborador dedicado — que o próprio A4 orçamenta
em «grande».

A diferença importa: invocar a Carta faz o deferimento parecer **inamovível até EPIC-10**,
quando na verdade a rota in-run é v1-legal e apenas cara. Um leitor da tabela do Grupo A tira
a conclusão errada.

## Proveniência e método

Seis lentes independentes, cada uma seguida de um céptico encarregado de a refutar.
13 agentes, ~24 min. **A síntese caiu com erro de ligação da API**; os 12 agentes de lente e
céptico tinham terminado e escrito ~120 KB no journal, pelo que retomei o fluxo pelo
`runId` — os concluídos replicaram do cache e só a síntese correu de novo.

| lente | a pergunta que só ela fez |
|---|---|
| `factos` | o guarda é falsificável, e as citações do documento resistem? |
| `fantasma` | há orquestração no nó **sob outro nome**, e diverge da excluída? |
| `fronteira` | a linha é principiada, ou é uma lista de dois nomes? |
| `pendurado` | que costuras ficaram mortas noutros módulos por causa da exclusão? |
| `planner` | EPIC-19: 15 tickets fechados — que efeito? |
| `carta` | «para a v1, nada» — é mesmo nada? |

### O que verifiquei à mão

| achado | verificação | resultado |
|---|---|---|
| «é a Carta» | `grep` em `specs/00_AOS_Carta.md` | **falso** — 0 ocorrências; a fonte é o ADR-018 |
| breaker inerte | `breaker.go:213`, `waiting_states.go:356`, `steer_gates.go:117,141` | confirmado |
| sem estados terminais | `grep state.Complete\|state.Failed` não-teste | confirmado — nenhum |
| `CheckDeadlines` sem chamador | `grep` em `packages/` | confirmado — só comentários e a definição |
| guarda sem furos | `go list -deps` em `packages/cmd/aos` | confirmado — 0 pacotes proibidos |

---

## Veredicto sobre o A4 tal como está escrito

| Alegação | Veredicto | Evidência (HEAD 075ea87) |
|---|---|---|
| **PROPÓSITO** — «decomposição goal→DAG… planner, validador puro, intake, dispatch/materialização/migração/replan, autonomia L0–L5, capability-gap» | **EXACTO.** É uma enumeração dos subpacotes de `control-plane/orchestrator`, e cada item mapeia 1:1. «Autonomia L0–L5» refere `planneraut`, não o oráculo de governança | `packages/control-plane/orchestrator/planneraut/doc.go:1-6` («a AUTONOMIA L0–L5 DO PLANEADOR… AOS-242, EPIC-18»); sob o prefixo proibido |
| **PROVADO** — lista de invariantes | **EXACTO com duas imprecisões.** (a) «suite adversarial (`planadversarial`)» está encostada ao planner mas não o exercita: `grep 'orchestrator/planner"' planadversarial/` devolve **vazio**. (b) «planner com mediação RM antes de decompor» prova uma **ordem** (verdadeira), contra um Monitor cru — não contra a cadeia do produto | ordem em `packages/control-plane/orchestrator/planner/planner.go:356-377` (mediate antes de reserva/NHI/decompose); cadeia real em `packages/integration/secured.go:334` (`NewProductionSecure`, 7 hooks) |
| **Guarda por teste** (`boundary_orq_sch_test.go:26-29`, directo + transitivo) | **EXACTO.** `forbiddenLifecycleModules` = exactamente dois módulos; `matchesForbidden` é prefix-match (`:32-39`); o 2.º teste interroga `go list -deps .` | verificado: `go list -deps .` em `packages/cmd/aos` devolve **zero** pacotes sob `control-plane/orchestrator|scheduler` |
| **`service.go:552`** — «o nó corre `Runtime.Run(ctx, goal)` directamente — um goal, um agente, N turnos» | **LITERAL EXACTO, DESCRIÇÃO INCOMPLETA.** A linha é `res, _, err := s.node.Runtime.Run(ctx, goal, nil)`. Mas o nó conduz hoje 5 arestas da máquina durável de AOS-017 | `packages/cmd/aos/steer_gates.go:117-118,122,128,142,146,168` (ready→running lazy-claim, pause/resume, waiting_on_human/resume) |
| **Banner** — «não anuncia orquestração (sem promessa falsa)» | **EXACTO.** Zero linhas de banner sobre orquestração; e não há capacidade-fantasma anunciada | `grep -i 'autonom\|orquestr' --exclude=*_test.go packages/cmd/aos` não devolve nenhuma linha de `log(`/banner |
| **RISCO** — «nenhum meta-objectivo multi-passo… tudo verde em teste, sem efeito» | **EXACTO mas incompleto.** Verdadeiro para o pipeline do ORQ; omite custos colaterais que o guarda **não** causa nem proíbe (secção 3) | ver secção 3 |
| **«para a v1 single-node, NADA»** | **VERDADEIRO PARA O ORQ, FALSO COMO CONTABILIDADE.** Nada há a ligar sob os dois prefixos proibidos. Mas existe trabalho de ciclo de vida na v1 que o guarda não toca (saga, estados terminais, breaker inerte, sink de autonomia) | `go list -deps .` do nó **inclui** `kernel/agent-runtime/saga`, que não consta de `forbiddenLifecycleModules` |
| **«(é a Carta)»** | **FALSO — atribuição de autoridade errada.** `grep -i 'orquestrad\|orchestrat\|escalonad\|scheduler' specs/00_AOS_Carta.md` devolve **zero linhas**. A proibição é de ADR-018 §2/§5, não da Carta (a emenda 1.2 difere a **topologia**, não o consumo in-run) | `specs/00_AOS_Carta.md` (grep vazio); `docs/adr/ADR-018-fronteira-no-orq-sch.md:46-52` |
| **Estimativa «GRANDE (5+ portas, env nova, revisão do boundary test)»** | **NÃO REFUTÁVEL PARA CIMA (já é o tecto da escala), mas SUB-SINALIZADA.** São 15 interfaces nomeáveis (planner 5, plandispatch 5, planmaterialize 4, planvalidate 1) e o `Decomposer` **não tem implementação produtiva** — só dois duplos de teste | `planner/planner.go:24,33,39,48,56`; comentário `planner.go:44-46` («o Decomposer real vive fora deste pacote») |
| **Citação ADR-018 §4** («a v1 corre a forma mínima, declarada — não fingida») | **FIEL À FONTE** | `docs/adr/ADR-018-fronteira-no-orq-sch.md:104-110`, incluindo «obrigará a rever conscientemente a fronteira (o teste sinaliza a mudança)» |

Custo adicional **certo**, não orçamentado: com a cadeia real, a primeira mediação do planeador é **negada**. O planeador emite `ToolID: "agent.plan"` (`planner/planner.go:260-261`, fixo no construtor) e o `RevalidationHook` está incondicional na cadeia — `packages/integration/revalhook.go:174` («sem tool set congelado para o run») ou `:189` («tool ausente do backing store»). O trabalho é pequeno (entrada no catálogo assinado, `cmd/aos/modeltools.go`), mas o bloqueio é garantido.

## O deferimento é honesto?

**Sim.** Três provas independentes:

1. **Nada é prometido e não entregue.** Não há env, não há banner, não há capacidade anunciada. `POST /runs` aceita um `submitRequest` mínimo e um objectivo multi-passo obtém um loop de agente com N turnos — que é o produto v1 definido pela Carta, não um modo de falha.
2. **A exclusão está registada e é verificável**, não apenas afirmada: ADR-018 §5 + `boundary_orq_sch_test.go`, com o fecho transitivo interrogado por `go list -deps .` (verificado: 0 pacotes proibidos).
3. **O ADR já preçou a rota alternativa** e o A4 é a única secção do relatório que nomeia o custo da emenda de fronteira («revisão do boundary test»).

Duas reservas, ambas pequenas e ambas de **redacção**, não de substância:

- O parêntese **«é a Carta» é falso** (a Carta é literalmente muda sobre ORQ/SCH). A rota in-run é v1-legal por ADR-018 §2 e está orçamentada em «grande» no próprio A4 — logo não está vedada até EPIC-10, ao contrário do que a tabela do Grupo A (`:187`) e a «Leitura honesta» (`:211`) sugerem ao agregar «orquestração e escalonamento **distribuídos**».
- A linha **PROVADO** encosta `planadversarial` ao planner sem que o exercite.

Nada disto torna o deferimento desonesto. Torna-o **mal atribuído num parêntese**.

## Custo do deferimento: costuras mortas e divergências

Ordenado por severidade. **Distinção crítica: nenhum destes itens é causado pelo guarda ORQ/SCH.** São lacunas do próprio nó que o «fecho: nada» do A4 encobre por adjacência temática.

**1. [ALTA] O circuit breaker composto é estruturalmente inerte no run comum.**
`breaker.Breaker.Observe` devolve no-op se `!liveness.CountsAsActiveWork(machine.Current())` (`packages/kernel/agent-runtime/breaker/breaker.go:213-215`), e `CountsAsActiveWork(s) = (s == state.Running)` (`packages/kernel/agent-runtime/liveness/waiting_states.go:356`). Mas o lazy-claim de AOS-218 só reclama `ready→running` no **primeiro pause de steer** (`steer_gates.go:117-118`) ou na **primeira escalada HITL** (`:141-142`). Um run que nunca é steerado e nunca escala fica em `ready` do princípio ao fim: o breaker provisionado por `AOS_BREAKER_*` nunca acumula, nunca avalia, nunca dispara — **incluindo no caso patológico que o motiva** (uma sequência de DENY não gera transição de estado, logo não arma o breaker). Não existe teste de trip ponta-a-ponta em `cmd/aos`. Falsificável em minutos: um teste de nó com `AOS_BREAKER_MAX_STALE_ITERATIONS` a repetir a mesma call negada esgota `MaxTurns` em vez de disparar.

**2. [ALTA] A máquina durável nunca escreve `complete` nem `failed`.**
`grep 'state.(Complete|Failed|Compensating|Killed|WaitingOnTool)'` em `packages/cmd/aos` e `packages/integration` (não-teste) devolve **vazio**. A tabela declarativa tem 13 arestas; o nó conduz 5. `Machine.CheckDeadlines` (`state/machine.go:550`) — que materializa `running→timed_out` e o kill fail-closed de ADR-013 — tem **zero chamadores de produção** em todo o repo, apesar de `liveness/doc.go:54-56` declarar que o consumidor «TEM de correr periodicamente». Correcção importante: `state.TimedOut` **é** escrito, mas só pelo breaker (`breaker/evaluator.go:93-98`, `targetFor(SignalWallClock)`) — que o item 1 mostra estar inerte no run comum. O desfecho vive num mapa em memória (`service.go:143`, `finish` em `:642`, poda em `:680`). Um run que acaba por erro, panic ou esgotamento de turnos é, no log durável, indistinguível de um crash a meio.

**3. [MÉDIA] Saga/compensação: costura morta dentro do fecho do nó, que o guarda não proíbe.**
`go list -deps .` do nó **inclui** `github.com/aos-ref/kernel/agent-runtime/saga`. Fora do próprio pacote e de testes, `saga.` só aparece em referências de tipo (`activity/contract.go:206,240`; `activity/dispatch.go:272`) — zero construtores de `SagaCoordinator`/`CompensationRegistry` em produção, e `activity.WithCompensationRegistry` não tem chamador de produção. Como o nó nunca escreve `failed`, a aresta `failed→compensating→ready` é inalcançável por dois motivos independentes.

**4. [MÉDIA] O nó deita fora o registo auditável das alterações de nível de autonomia.**
`buildAutonomyOracle` (`packages/cmd/aos/autonomy_levels.go:107-118`) chama `autonomy.NewLevelRegistry()` **sem** `autonomy.WithSink(...)` (`registry.go:67`), embora execute `SetLevel` com motivo («provisionamento por `AOS_AUTONOMY_LEVELS`») e actor («config:node»). O escritor existe e está pronto (`registry.go:163` → `Sink.SealLevelChange`; `events.go:80-83` `AuditSink`). O nó tem WORM composto. Falta **uma linha**.

**5. [MÉDIA] Omissão de inventário: `AOS_AUTONOMY_LEVELS` não consta do Grupo B, e o banner não o declara.**
Não é defeito do A4 — é do relatório. O Grupo B define-se como «costura env… o banner declara **cada um**» (`:195`) e lista 8 itens (`:196-203`); `grep 'AUTONOMY_LEVELS'` sobre o ficheiro devolve **zero**. E a cláusula «o banner declara cada um» é falsa para este item (grep por linhas de `log(` com «autonom» em `cmd/aos` não-teste: **vazio**). Consequência concreta: um operador que provisione a partir do relatório não define a variável, o oráculo fica `nil`, `applyAutonomy` é no-op e **nenhum `escalate` é emitido** — tornando inalcançável o bridge HITL que o mesmo relatório declara resolvido (`:77`) e demonstra no run ao vivo (`:224-229`). O próprio código diz isto em texto: `autonomy_levels.go:10-11`.

**6. [MÉDIA→BAIXA] Nenhum caminho reclama um run órfão.** `worker.Assigner.TryAcquire` tem um único chamador de produção, dentro de `submit()` (`service.go:385`). O único laço periódico é `sweepApprovals`, que declara não retomar nada (`approval_sweeper.go:9-12`). Não há varredura de arranque. Um run interrompido por crash não é retomado por ninguém, mesmo com step-ledger e checkpoints no Event Store. **Eixo AOS-015/AOS-099 (`worker.Worker`), não A4** — o mecanismo em falta vive no kernel, fora dos dois módulos proibidos.

**7. [BAIXA] `CallContext.BudgetTokensRemaining` e `CallContext.Sensitivity` são sempre valor-zero no nó.** `loop.go:648-660` constrói o único `Call` do loop com `Context: CallContext{Taint: …}` e mais nada; ambos atravessam ≥3 hops sem produtor. Sem risco de decisão errada: `pdp/engine_cedar.go:141-142` só traduz duas chaves para o Cedar (`taint`, `sensitivity`), pelo que `BudgetTokensRemaining` **não chega à política**. Dívida de higiene. Correcção ao A1: `planvalidate/risk.go:98-100` **não** é produtor da string `Sensitivity` (é um `risk.Sensitivity` tipado noutro domínio) — não existe produtor em lado nenhum, proibido ou não. Compor o ORQ não fecharia esta costura.

**8. [BAIXA, sem acção] `Goal.ParentTraceParent` vazio.** É a semântica **documentada e correcta** de um run-raiz (`loop.go:56-57`: «Vazio ⇒ run-raiz»). Não é código morto.

### Conceitos implementados duas (ou três) vezes

- **Ordinal L0–L5, três definições independentes:** `governance/autonomy/level.go:7`, `orchestrator/planneraut/level.go:12`, `orchestrator/replan/replan.go:20` — todas `type Level uint8`, nenhuma das do orchestrator importa a canónica (`orchestrator/go.mod` não requer `control-plane/governance`). Sem ponte e sem adaptador: se o ORQ for consumido dentro de um run, mexer em `AOS_AUTONOMY_LEVELS` **não** moverá o nível do planeador. Declarado (`planneraut/level.go:5-8`), sem dano hoje.
- **Duas máquinas de ciclo de vida:** a do kernel (`kernel/agent-runtime/state`, que o nó conduz) e `orchestrator/contract/state.go`, cujo `validTransitions` se auto-declara «autoridade única do que a máquina mínima aceita: ready→running→complete|failed». Esta é a razão substantiva do guarda, não a sua excepção.
- **Duas retomas:** replay-then-continue sobre capturas do modelo (`resume.go` + `resume_model.go`, motivada pela fidelidade da aprovação AOS-021) vs. cursor de passo do kernel (`durable/resume.go`, `ResumePoint`). Operam em camadas distintas. O ponto de duplicação real é o **heartbeat**, re-escrito à mão (`service.go:608-637`) — e está declarado com eixo em ADR-018 §5-bis, com guarda de veracidade dedicada (`aos222_fencing_truthfulness_test.go`) que auto-desactiva quando o `FencedAppender`/`worker.Worker` for composto.
- **Derivação dos eixos SA-ROC em dois sítios:** os helpers de `kernel/reference-monitor/risk_gate.go:96,114,125,171,185` (não-exportados) e `planvalidate/risk.go:57-79`. Um adaptador REG→Snapshot seria a terceira. Sem consumidor hoje.

## Decisões que são do dono (não minhas)

**(i) A fronteira é principiada ou é uma lista de dois nomes?**

Factos: o guarda enuncia um **critério** («módulos cujo import indicaria uma segunda autoridade de ciclo de vida co-residente», `:24-25`) mas implementa uma **lista de dois prefixos** (`:26-29`). O critério e a lista divergem em ambas as direcções:
- *Falsos positivos:* `orchestrator/plan` (tipos JSON + semver, só stdlib) é proibido sem exibir maquinaria de ciclo de vida. Apenas 1 dos 15 pacotes do módulo é contra-exemplo legítimo — `orchestrator/contract` e `planvalidate` **não** são (o primeiro declara `Scheduler.Start`; o segundo importa o pacote raiz `orchestrator`, cujo `Submit` emite `run.created`).
- *Falsos negativos:* `kernel/agent-runtime/saga` está no fecho do nó e não é apanhado; `cmd/aos-demo` compõe 5 pacotes de control-plane e **não tem guarda equivalente** (o seu fecho está limpo hoje — verificado — mas nada o impõe).

*Opção A — deixar como está.* Custo: continua a haver divergência entre o que o teste diz proibir e o que proíbe; o desenho de outros subtópicos (A2, A5) é condicionado sem que a razão seja legível. Benefício: zero trabalho, e a largura é **deliberada** (ADR-018 §4 promete um fio-armadilha de revisão, não uma taxonomia).
*Opção B — reescrever como critério («sem `Scheduler.Start`», «sem segundo escritor do stream de run»).* Custo: um critério enunciável tem de ser verificável sem julgamento humano, e nenhuma formulação óbvia o é sem análise de chamadas. Alto risco de ficar mais fraco.
*Opção C — manter a lista e corrigir a **descrição**, replicando o mesmo teste em `cmd/aos-demo`.* Custo: baixo.

**Recomendação (minha, não decisão): Opção C.** A largura é uma escolha registada e defensável; o defeito é que o A4 a apresenta pelo que ela *enuncia* em vez de pelo que *faz*. Corrigir o texto custa uma frase; replicar o teste custa um ficheiro. Não tocar no ADR-018.

**(ii) As costuras mortas ficam, são anotadas, ou saem?**

Aplica-se aos itens 1–4 e 7 da secção anterior.

*Opção A — ficam como estão.* O risco não é uniforme: a costura 1 (breaker inerte) é uma **postura de segurança anunciada que não existe** — o operador provisiona `AOS_BREAKER_*`, o banner declara-o, e o mecanismo não dispara no run comum. A 2 (sem estado terminal) degrada a auditabilidade. As 3 e 7 são inertes e inofensivas.
*Opção B — anotar todas com eixo (padrão DEF-xxx / `aos222_fencing_truthfulness_test.go`).* Custo baixo, e há precedente forte no repositório para exactamente isto.
*Opção C — fechar as que são baratas.* Item 4 é **uma linha** (`WithSink`). Item 2 é uma aresta que a tabela declarativa já expõe e que não exige fencing token. Item 1 exige decidir onde reclamar `ready→running` (candidato natural: no arranque do run, não no primeiro pause).

**Recomendação (minha, não decisão): C para os itens 1, 2 e 4; B para 3 e 7.** Os itens 1 e 4 não são dívida arquitectural — são omissões de wiring com correcção conhecida, e o item 1 é a única discrepância desta lista entre postura anunciada e postura efectiva. **Nenhum deles pertence ao A4** e nenhum toca no guarda: devem ser registados no seu próprio eixo (AOS-080/081 para o breaker, AOS-017/218 para os estados, AOS-087 para o sink), não como trabalho de orquestração.

**(iii) Qual das duas retomas é canónica, e quem é o dono do heartbeat.** Já parcialmente registada em ADR-018 §5-bis com guarda de veracidade. A decisão em falta é apenas: **antes** de compor `worker.Worker`, declarar se o replay-then-continue do nó passa a ser adaptador do `ResumePoint` do kernel ou se coexistem. Não é código a mudar agora.

## Plano A4 revisto

**A conclusão «para a v1 single-node, nada» mantém-se para o Orquestrador.** Não há trabalho de composição a fazer, e fazê-lo violaria uma decisão registada. Não inventei substituto.

Acções pequenas e honestas, todas de declaração ou inventário (nenhuma toca no guarda, nenhuma emenda ADRs):

1. **Corrigir o parêntese `(é a Carta)` para `(é ADR-018 §2; a Carta é muda sobre ORQ/SCH)`.** Uma frase. Evita que um deferimento defensável fique irrefutável pelo motivo errado.
2. **Qualificar a linha PROVADO:** `planadversarial` valida `plan`+`planvalidate`+`plandispatch` sobre fixtures, **não** o `planner`; a mediação do planeador foi exercitada contra `rm.New()` cru, nunca contra `NewProductionSecure`.
3. **Acrescentar ao Fecho o pré-requisito de supply-chain:** `agent.plan` tem de entrar no catálogo assinado, senão a primeira mediação é negada pelo `RevalidationHook` (`revalhook.go:174/189`). Trabalho pequeno, bloqueio certo.
4. **Substituir «5+ portas» por «15 interfaces nomeáveis + construir o `Decomposer`, que hoje só existe como duplo de teste»**. A classificação «grande» não muda (já é o tecto); o que muda é o sinal.
5. **Acrescentar `AOS_AUTONOMY_LEVELS` ao Grupo B do relatório** (item 5 da secção 3) e acrescentar-lhe a linha de banner em falta, no molde de `bootstrap.go:1397-1438`. Isto é do inventário, não do A4, mas é a correcção com maior consequência operacional de toda esta avaliação.
6. **Alinhar as estimativas de A2 e A5:** `:351` estima «médio» para adaptadores + composição da progress-surface; `:375` preça o wiring equivalente com «exige emendar o boundary test». Duas das quatro portas (`BudgetReader`, `ProgressReflector`) são scheduler-free — verificado, `go list -deps` de `progress-surface`, `control-surface` e `budget` devolve zero pacotes proibidos; as outras duas são interfaces de **um método** (`RequestExtension`, `Degrade`), satisfazíveis localmente sem tocar no scheduler. O bloqueio não é estrutural.
7. **Replicar `boundary_orq_sch_test.go` em `cmd/aos-demo`.** Um ficheiro. `packages/integration` não precisa (está dentro do fecho de `cmd/aos`).
8. **Registar os itens 1–4 da secção 3 nos seus eixos próprios** — explicitamente **fora** do A4, para não os fazer parecer custo do deferimento de orquestração.

Uma nota de acção que não é «pequena» e não recomendo agora: acrescentar uma frase ao A4 e/ou ao Grupo A dizendo que o consumo in-run é **v1-legal** por ADR-018 §2, contra a agregação «distribuídos» de `:187`/`:211`. É correcção de coerência interna do relatório, não do A4.

## O que foi REFUTADO

- **«Autonomia L0–L5 está listada como não-ligada mas está composta».** Levantado por **quatro** lentes independentes; refutado nas quatro. São dois subsistemas homónimos: o A4 refere `orchestrator/planneraut` (AOS-242, autonomia **do planeador**, `doc.go:1-6`), proibido e não-composto; o composto é `governance/autonomy` (AOS-087). Colisão de nome, não erro factual. **O que sobrevive não é do A4**: a omissão do Grupo B e a ausência de banner (item 5 acima).
- **«Fechar violaria o guarda é falso — metade das portas do A2 é composível».** O facto é verdadeiro; a atribuição é falsa. O sujeito de «fechar» em `:367` é o ORQ, onde a afirmação é correcta por construção. O documento que sobre-generaliza é `docs/reports/desafio-A2-progress-surface.md:93,169`.
- **«O guarda proíbe por prefixo mas o orchestrator não exibe a maquinaria que o princípio nomeia».** Os contra-exemplos são auto-derrotantes: `orchestrator/contract/ports.go:29-32` declara literalmente `Scheduler{Start;Stop}` e `contract/state.go` uma segunda tabela de transições; `planvalidate` importa o pacote raiz `orchestrator`. Sobrevive apenas `orchestrator/plan`, 1 de 15.
- **«A máquina de estados durável fica presa em `running` para sempre».** Falso: pelo lazy-claim, um run que nunca pausa nem escala fica em `ready` — **nunca transita**. O defeito real é pior e diferente (itens 1 e 2 acima).
- **«O supervisor `worker.Worker` nunca composto é dívida não nomeada».** Está nomeada com eixo em ADR-018 §5-bis, declarada in-line em `service.go:600-604`, e tem guarda de veracidade que auto-desactiva ao ser composta. A não-retomabilidade de conteúdo crypto-shredded está declarada em `resume.go:120-124` («NÃO é retomável, **por desenho**»).
- **«O silêncio do banner é virtude no A4 e violação no A6 — duplo padrão».** A assimetria é principiada: o A6 tem uma env **composta** cuja postura configurada diverge da efectiva (armadilha de ignorar-em-silêncio); o A4 não tem composição nem env, logo não há estado a declarar. Acresce que AOS-203 é a disciplina de **superfície de env** (`env_surface_test.go:111`), que literalmente não se aplica a um subsistema sem env.
- **«O A4 é o único deferimento sem entrada no registo».** Falso: `docs/governance/REGISTO-Deferimentos.md:193` (DEF-502, ADR-018) e `:208` (DEF-803, decomposição stub). E o registo tem âmbito declarado (§2.1: marcadores em código de produção) — um import **ausente** está fora do âmbito por construção.
- **«O `go list -m all` do nó já carrega orchestrator e scheduler».** Verdadeiro mas invertido como risco: o guarda interroga `go list -deps` (fecho de **pacotes**), pelo que o cenário descrito («bastaria `failover` chamar `tieradapter`») fica **vermelho** — é o guarda a funcionar. Resíduo real: só a ausência do guarda em `cmd/aos-demo` (item 7).
- **«Fechar A4 e fechar A1 são o mesmo trabalho».** Falso: `control-plane/budget` não está sob prefixo proibido; a costura de A1 está no **kernel** (`loop.go:648-660`) e é independente do guarda.
- **«A validação sobre snapshot pinado exige alargar o contrato do registo com dois eixos de risco».** Falso: o nó já deriva os três eixos SA-ROC a partir da string de capability (`risk_gate.go:96,114,125,171,185`), e o catálogo congelado carrega os inputs. Sobra só a não-exportação dos helpers.
- **«O deferimento deixa o leitor a concluir que está vedado até EPIC-10».** O próprio `:367` separa as duas rotas e orça a in-run.

**Não verificado por mim** (herdado das lentes já passadas pelo céptico, sem re-verificação independente): a contagem exacta das 15 interfaces em `plandispatch`/`planmaterialize`/`planvalidate`; as linhas de `orchestrator/delegation.go:456` e `planner.go:368` como únicos produtores de `BudgetTokensRemaining`; o conteúdo de `docs/governance/REGISTO-Deferimentos.md:193,208`; o fecho de build de `cmd/aos-demo`; e o conteúdo de `aos222_fencing_truthfulness_test.go`.
---

## Ver também

- [`desafio-A1-budget-admission-control.md`](./desafio-A1-budget-admission-control.md)
- [`desafio-A2-progress-surface.md`](./desafio-A2-progress-surface.md)
- [`desafio-A3-credential-broker.md`](./desafio-A3-credential-broker.md)

- [`desafio-A5-escalonador.md`](./desafio-A5-escalonador.md)

- [`desafio-A6-attestation.md`](./desafio-A6-attestation.md)

## Rastreabilidade

Transcrições por agente: `.claude/projects/…/subagents/workflows/wf_ea5c25f0-cb2/journal.jsonl`.
Script do fluxo: `…/workflows/scripts/desafio-a4-orquestrador-wf_ea5c25f0-cb2.js`.

> **Nota de âmbito:** este relatório NÃO altera `prontidao-modelos-agenticos.md` nem corrige
> nenhum dos defeitos que descreve. Os dois achados de severidade alta têm ticket próprio.
