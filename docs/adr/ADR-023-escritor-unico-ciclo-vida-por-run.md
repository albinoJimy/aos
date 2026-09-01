# ADR-023 — Escritor único do ciclo de vida por-run: a autoridade é o LEASE, não o componente

| Campo | Valor |
|---|---|
| **ADR** | 023 |
| **Título** | Quem escreve as transições de estado por-run na composição ORQ/SCH↔nó: o detentor do lease é o escritor único; o Escalonador DERIVA do log |
| **Estado** | **Aceite** — ratificado por autoridade de dono a 2026-08-30 (AOS-281) |
| **Data** | 2026-08-30 (proposto e **ratificado** na mesma data) |
| **Deciders** | Executor de AOS-281 (proposta) · **Dono do produto (ratificação, 2026-08-30)** |
| **Contexto-fonte** | `specs/EPIC-10_Topologia_Operacao_DR.md` §AOS-281; ADR-018 §2 e §«O que muda no distribuído»; `packages/kernel/agent-runtime/durable/lease.go` (AOS-018); `packages/kernel/agent-runtime/durable/fencing.go` (`FencedAppender`); `packages/kernel/agent-runtime/state/machine.go` (AOS-017); `packages/control-plane/orchestrator/graph.go` (AOS-025, `GraphBuilder`/`RebuildDAG`/`ErrLogAhead`); `packages/control-plane/orchestrator/plandispatch/ports.go` (AOS-239, `LifecycleView`); `docs/governance/REGISTO-Deferimentos.md` (DEF-272, DEF-273, DEF-803) |
| **ADRs relacionados** | **ADR-018** (fronteira nó↔ORQ/SCH — este ADR EXTENDE-O, não o emenda), ADR-007 (uma só fonte de verdade), ADR-015 (execução durável), ADR-010 (replay determinístico), ADR-022 (extensões do grafo de plano) |
| **Supersede** | — |

> **RATIFICADO (2026-08-30, autoridade de dono).** O estado passou de *Proposto* a
> *Aceite*: a decisão da §2 — **a autoridade de escrita É a posse do lease**, o SCH
> como DERIVADOR permanente, e o ORQ a escrever só sob posse e sobre grafo
> re-hidratado — é agora **autoridade congelada** e não se re-litiga sem emenda datada
> (Carta §6). Ficam igualmente congeladas as três REJEIÇÕES da §3, e em particular a
> (c): **serialização não é arbitragem** — dois escritores coordenados por
> `expected_seq` continuam a ser duas autoridades, e o `RebuildDAG` a falhar para
> sempre é a sua consequência, não um acidente.
>
> O que a ratificação **não** congela é o que a §4 declara como limite medido: a
> ausência de arbitragem entre processos no Event Store de referência (DEF-282, eixo
> AOS-100) continua a ser dívida com sensor, e a janela TOCTOU do caso token-igual
> continua aberta. Ratificar a decisão não converte os seus limites em propriedades.

> **O QUE ESTE ADR NÃO FAZ.** Não reabre a forma do produto v1 (Carta §7, emenda 1.2):
> o nó `aos` continua single-host e o seu grafo de build continua **sem** um pacote sob
> `control-plane/orchestrator` ou `control-plane/scheduler`. Não emenda o ADR-018 — as
> duas fronteiras guardadas por teste ficam **verdes e inalteradas** (§5). Não promove o
> `Scheduler` do EPIC-03 a dono de nada. O que decide é uma pergunta que o ADR-018
> deixou nomeada mas em aberto para o distribuído: **quando a autoridade passa a poder
> mudar de processo, quem tem o direito de escrever uma transição de estado de um run.**

---

## 1. Contexto: a pergunta que sobrou

O ADR-018 fixou, para a v1 single-host, que o loop de serviço do nó é a fonte única de
verdade do ciclo de vida, e nomeou a saída para o distribuído: ORQ e SCH tornam-se
componentes autónomos que coordenam **através do Event Store replicado**, com a
invariante «um só dono por run» arbitrada pelo **mesmo lease durável**.

Nomear a saída não é percorrê-la. No estado actual do repositório, quando se pergunta
«quem escreve uma transição de estado de um run?», existem **três candidatos com código
escrito** e nenhuma regra que os ordene:

| Candidato | O que escreve | Stream | Apresenta fencing token? |
|---|---|---|---|
| `NodeService` → `state.Machine` (AOS-017) | `state.transition` (run: `ready→running`, `→waiting_on_human`, selo terminal) | `run_id` | **Só** em `ready→running` (`RequiresFencingToken`) |
| `orchestrator.GraphBuilder` (AOS-025) | `task.node.created`, `task.edge.added`, `task.node.state_changed` (por-nó) | `run_id` | **Nunca** — o comentário do `MarkRunning` declara-o: «o Orquestrador NÃO detém o fencing token do claim» |
| `plandispatch.Dispatcher` (AOS-239) | — (lê por `LifecycleView`) | — | n/a |

Os dois primeiros escrevem factos de ciclo de vida **no mesmo stream `run_id`**, por
caminhos diferentes, **nenhum dos dois com concorrência optimista** (`state.Machine` usa
um `state-N` derivado de um contador **em memória**; o `GraphBuilder` faz `Append` sem
`expected_seq`). Em single-process isso é inofensivo, porque só há um processo. É
exactamente por isso que a v1 é honesta e é exactamente por isso que o distribuído não
pode ser feito por *wiring*: a serialização que hoje segura tudo é um **acidente da
topologia**, não uma propriedade do desenho.

Os defeitos que a auditoria adversarial de 2026-08-30 levantou no grafo — e que estão
documentados **no próprio código**, em voz alta — são sintomas disto e de mais nada:

- **`ErrLogAhead`** (`graph.go:48`): um `GraphBuilder` «retomado» sobre um run que já
  existe no log faz `AddNode`+`MarkRunning`, ambos devolvem `nil`, **zero eventos novos
  entram**, e o chamador fica com a ilusão de retoma. É a ausência de re-hidratação.
- **`AddEdge` / `jaExistia`** (`graph.go:493`): o revert em memória removia uma aresta
  que estava **durável** no log; a inversa deixava de parecer um ciclo, era admitida, e
  o `RebuildDAG` — função pura sobre um log append-only — **falhava para sempre**. É a
  ausência de um dono que saiba o que já está durável.
- **`restoreState`** (`graph.go:356`): repõe estado sem revalidar e sem CAS. É seguro
  só enquanto ninguém mais escrever.

Nenhum deles se resolve um a um. Todos se resolvem quando existe um **árbitro** — e o
árbitro já está construído e testado (`durable.LeaseManager`, AOS-018). O que falta é a
regra que diz quem, sob ele, tem caneta.

## 2. Decisão

### 2.1 A autoridade de escrita é a POSSE DO LEASE, não a identidade do componente

**O direito de escrever uma transição de estado de ciclo de vida do run `R` é,
exactamente, a posse do fencing token corrente de `lease:<R>`.** Não é um atributo de um
módulo, de um binário nem de um papel: é um facto durável, legível do Event Store
replicado, com ordem total e monotonicidade garantidas pela concorrência optimista do
stream de lease (`expected_seq`).

Consequências imediatas, e é por elas que esta formulação foi escolhida:

1. **«Um só escritor» deixa de ser uma atribuição a manter e passa a ser uma propriedade
   já garantida.** Em qualquer instante existe no máximo um detentor do token corrente,
   porque `Claim` recusa (`ErrLeaseHeld`) enquanto houver lease vivo. A invariante do
   ADR-018 §2.1 não é re-afirmada: é *herdada*.
2. **A passagem de autoridade entre processos deixa de ser uma excepção e passa a ser um
   evento ordinário** — `lease.released` seguido de `lease.claimed`. Nomear um componente
   como escritor permanente obrigaria a re-litigar a regra em cada handoff, ou a congelar
   a topologia single-host que o EPIC-10 existe para abrir.
3. **A regra é falsificável em runtime**, não só em revisão: uma escrita sem token, ou com
   token inferior ao corrente, é recusada pelo `FencedAppender` sem tocar no log.

### 2.2 Entre ORQ e SCH, o SCH é DERIVADOR — permanentemente e por construção

**O Escalonador NUNCA escreve uma transição de estado de ciclo de vida.** Lê o estado
pela porta `plandispatch.LifecycleView` e o resultado registado por `ResultView`, e
DERIVA dele toda a decisão de despacho.

Isto não é uma restrição nova imposta a código existente — é a declaração de uma
propriedade que o `plandispatch` **já tem por construção** e que ninguém tinha escrito
como regra:

- o pacote não conhece o Event Store (o seu guard-test admite só `plan` e
  `plannerevents`);
- a `LifecycleView` documenta a assimetria — «LÊ factos, nunca os escreve» — como sendo
  precisamente o que preserva a fronteira ADR-018;
- até o slot de headroom é declarado como não-seu: «libertado pelo escalonador na
  conclusão do nó — **NUNCA por este pacote**, que não detém autoridade de ciclo de vida».

**A razão de fundo, que é o que torna esta metade da decisão não-negociável:** o
despachante é **re-invocável à discrição**. Uma passagem de `Dispatch` é idempotente por
desenho e o escalonador re-invoca-a sempre que o headroom liberta ou uma origem termina.
Um componente que pode ser re-invocado sem limite **não pode ter autoridade para mover
uma máquina de estados**, ou «re-invocar» passa a significar «re-transitar».

**Limite declarado (para não haver ambiguidade sobre a palavra «escreve»):** o SCH escreve
os **seus próprios factos de decisão** — `plan.branch_decided`, via `BranchJournal.Record`
— e continuará a escrevê-los. Não são estado de ciclo de vida: são o registo append-only
de uma avaliação que ADR-022 §2.4(3) exige que seja um *facto* e não um cálculo repetido,
e vivem no stream do **plano** (`plan_id`), não no stream do **run** (`run_id`). A regra
desta decisão é sobre transições de estado por-run, e é essa a fronteira exacta.

### 2.3 O ORQ escreve — mas SÓ sob o lease, e SÓ a partir de um grafo re-hidratado

O ORQ não pode ser derivador: decompor `goal→DAG` **é** uma escrita (`task.node.created`,
`task.edge.added`), e um ORQ que não escreve não é um ORQ. O que se decide não é *se*
escreve, mas *quando* e *como*:

- **Quando:** só enquanto detém o token corrente de `lease:<run_id>`. Fora disso não tem
  via de escrita — não por disciplina do chamador, mas porque o único appender que lhe é
  fornecido é fenced.
- **Como:** por uma via de construção que parte de `RebuildDAG` e **nunca** de um DAG
  vazio sobre um run que já existe no log. O `ErrLogAhead` deixa de ser o detector do
  erro depois de ele acontecer e passa a ser inalcançável pelo caminho normal — o
  builder cego deixa de ser **construível**.

### 2.4 Corolário forçado: toda a escrita de ciclo de vida passa a apresentar o token

O critério de aceitação 1 de AOS-281 («todas as escritas de ciclo de vida apresentam o
fencing token corrente; uma escrita com token obsoleto é recusada, não aplicada») **não é
satisfeito pelo mecanismo actual** e é honesto dizê-lo antes de escrever código:

- `state.Machine` exige token **só** em `ready→running`; nas restantes transições
  regista-o se estiver presente, mas não o impõe;
- `GraphBuilder` não o apresenta em transição nenhuma;
- o `FencedAppender` — o enforcement que recusaria a escrita obsoleta — **existe no kernel
  e não está composto em lado nenhum de produção** (ADR-018 §5-bis).

A decisão implica, portanto, que **na composição nova todas as escritas de ciclo de vida
sejam encaminhadas pelo `FencedAppender`** ligado ao `LeaseManager` como `TokenSource`.
Isto não inventa mecanismo: o `LeaseManager` já satisfaz `TokenSource` **e**
`LeaseExpiryAuthority`, e é essa segunda capacidade que fecha a janela que interessa —
ver §2.5.

**O §5-bis do ADR-018 fica intacto e continua verdadeiro.** Ele é uma afirmação sobre **o
nó**, e o nó não muda: continua a não compor o `FencedAppender`, e a guarda de veracidade
`aos222_fencing_truthfulness_test.go` — que varre o *source* do caminho de posse **do
pacote do nó** — continua a impor exactamente o que impunha. A composição nova acontece
noutro pacote, noutro binário.

### 2.5 O handoff é por anúncio, não por expiração — e não tem janela de dupla-posse

A passagem de autoridade a jusante do gate é `lease.released` + `lease.claimed`, nunca a
expiração por TTL. A expiração é a semântica certa para uma réplica que **morreu** (não há
quem anuncie); para uma que **parou de propósito**, o anúncio é a semântica certa — é
literalmente a razão pela qual `LeaseManager.Release` existe (AOS-021).

O que torna isto uma passagem **sem janela**, e não apenas uma passagem rápida:

1. `Release` só tem efeito escrito pelo detentor do token corrente (`ErrLeaseSuperseded`
   caso contrário) e o seu único efeito é **encurtar a expiração do próprio token para
   `now`**;
2. a partir desse instante, `LeaseManager.CurrentLeaseExpired` devolve `expirado=true` —
   e o `FencedAppender`, que consulta essa capacidade, **rejeita as escritas do detentor
   anterior imediatamente**, mesmo antes de existir um novo claim, e mesmo sendo o token
   dele ainda o «corrente»;
3. só então um `Claim` de outro processo passa (a expiração já ficou para trás) e minta
   `token+1`, serializado contra qualquer concorrente pelo `expected_seq` do stream
   `lease:<run_id>` — um vence, os outros relêem.

Não há instante em que dois processos possam escrever com sucesso. O intervalo entre (1) e
(3) é um intervalo em que **ninguém** escreve, que é a forma correcta de um handoff.

**Invariante de disciplina que daqui decorre e que os testes têm de exercer:** `Release` é
o **último** acto da posse. Um escritor que largue antes de acabar de escrever perde as
escritas seguintes — fail-closed, ruidosamente, nunca em silêncio.

### 2.6 Onde a composição vive: um terceiro sítio, que é um processo próprio

O ticket diz, e está certo: «não existe hoje um terceiro sítio a quem isso pertença». Os
três sítios que existem estão todos excluídos, cada um por uma razão diferente e todas
boas:

| Sítio | Porque não |
|---|---|
| `packages/cmd/aos` (o nó) | `TestBoundary_NodeDoesNotImportConcurrentOrchestratorOrScheduler` — e a v1 single-host não é reaberta |
| `packages/control-plane/orchestrator/plandispatch` | `TestBoundary_ProductionImportsAreAllowlisted` — o SCH não importa ciclo de vida (e §2.2 diz que nem devia) |
| `packages/integration` (raiz de composição) | está **dentro do grafo de build do nó**: o guarda transitivo `TestBoundary_NodeBuildGraphExcludesConcurrentOrchestratorOrScheduler` dispararia |

A composição vive, por isso, num **módulo novo** — `packages/control-plane/runlifecycle`
— conduzido por um **binário novo**, `packages/cmd/aos-orq` (o repositório já tem o
padrão: `aos-attestation`, `aos-issuer`). É o componente autónomo que o ADR-018 nomeou
para o distribuído.

**As duas fronteiras guardadas ficam verdes e inalteradas, e a razão é direccional:**

- o nó não passa a importar nada — o módulo novo **não entra** no seu grafo de build
  (`go list -deps` do nó não o alcança: nenhum `require` o traz);
- o `plandispatch` não passa a importar nada — a dependência aponta
  `runlifecycle → plandispatch`, nunca ao contrário, e o guard-test do dispatcher
  inspecciona **os imports do próprio pacote**, que não mudam.

Nenhuma emenda ao ADR-018 é necessária. Este ADR **extende-o** ao caso que ele próprio
deferiu, com a mesma invariante e o mesmo mecanismo.

## 3. Alternativas consideradas

- **(a) O nó continua o escritor único também no distribuído; ORQ e SCH derivam.**
  Rejeitada: um ORQ que não escreve não decompõe. A decomposição *é* a emissão de
  `task.node.created`/`task.edge.added`; sem ela o ORQ não tem função. E fazer o nó emitir
  o grafo em nome do ORQ exigiria que o nó importasse o orquestrador — a violação exacta
  do guarda do ADR-018.

- **(b) O SCH torna-se o escritor único (o `Scheduler` do EPIC-03 conduz
  `ready→running→complete|failed`, como a sua porta descreve).** Rejeitada por duas razões
  independentes, qualquer uma suficiente: (i) obrigaria o `plandispatch` a importar o
  módulo de ciclo de vida, contra o seu guard-test, e a trocar a assimetria «lê, não
  escreve» que a `LifecycleView` documenta como sendo o que preserva a fronteira; (ii) o
  despachante é re-invocável por desenho (§2.2) — dar autoridade de transição a um
  componente re-invocável é dar-lhe autoridade para re-transitar.

- **(c) Ambos escrevem, coordenados por concorrência optimista (`expected_seq`) no stream
  do run.** Rejeitada, e é a alternativa que mais merece ser registada porque é a que
  parece resolver o problema. **Serialização não é arbitragem.** O CAS garante que as
  escritas ficam ordenadas; não garante que sejam mutuamente coerentes. Dois ORQs com
  vistas diferentes do mesmo DAG apenderiam factos alternadamente válidos-em-isolado e
  contraditórios-em-conjunto — e o `RebuildDAG`, que é uma **função pura sobre um log
  append-only**, falharia para sempre, sem reparação em banda. É textualmente o modo de
  falha já documentado em `graph.go:493`. O que impede isso não é ordem: é haver **uma
  vista só**, e uma vista só é o que a posse exclusiva dá.

- **(d) Deixar a decisão ao wiring («quem compuser que decida»).** Rejeitada: é o que o
  ticket proíbe explicitamente, e com razão. Os três defeitos do §1 são o resultado
  medido de a regra não estar escrita — cada um foi introduzido por alguém que compôs de
  boa-fé sem uma regra a que obedecer.

## 4. Consequências

**Positivas**

- «Um só dono por run» passa a ser verificável em runtime, e não só em revisão de código:
  um token obsoleto é recusado no ponto de escrita.
- Os defeitos do grafo do §1 resolvem-se **por construção** e não um a um: a
  re-hidratação obrigatória torna o builder cego inconstruível; a posse exclusiva torna o
  `restoreState` sem CAS inofensivo (não há segundo escritor a correr contra ele).
- `DEF-272` e `DEF-273` ganham finalmente uma casa legítima: o emissor do veredicto e a
  implementação da `PayloadView` são wiring do ciclo-de-vida do run, e o `runlifecycle` é
  o primeiro sítio no repositório que pode legalmente conter os dois — o eixo anterior
  (AOS-238) estava **proibido por guard-test** de o fazer, o que está registado como
  correcção no próprio registo de deferimentos.
- O ADR-018 §5-bis deixa de ser um limite permanente e passa a ser um limite **do nó**: a
  frase «se um dia o nó compuser o `FencedAppender`, a premissa da guarda inverte-se»
  mantém-se verdadeira e por cumprir, sem que o distribuído fique refém dela.

**Custos e risco residual — declarados, não minimizados**

- **A arbitragem entre PROCESSOS depende de uma propriedade que o Event Store de
  referência NÃO tem — medido, não suposto.** Toda esta decisão assenta em o
  `expected_seq` do stream `lease:<run_id>` ser atómico **entre escritores**. O
  `LeaseManager` está correcto, mas a sua correcção é *condicional* a essa propriedade.
  As réplicas de AOS-100 são cópias **in-process** do log e o índice de dedup vive em
  memória; um `Open` faz replay do WAL no arranque e, a partir daí, cada processo tem a
  **sua** cabeça. Medido deterministicamente a 2026-08-30 (dois `eventstore.Open` sobre
  o mesmo ficheiro, dois `Claim` do mesmo run): **ambos passam e ambos mintam o token
  1**. Consequência directa e dita em voz alta: **o AC1 de AOS-281 — «dois processos a
  disputar o mesmo run: só um ganha o claim, o outro vê o `expected_seq` falhar» — NÃO é
  satisfazível com o Event Store de referência**, e correr dois `aos-orq` em paralelo
  sobre um WAL partilhado não é seguro. Exige um backend genuinamente
  partilhado/replicado (NATS JetStream, da tabela de stack), que é infraestrutura e não
  deste ticket. O que fica provado com processos reais é a posse **sequencial** — o
  handoff por anúncio (AC2) e a re-hidratação (AC4) a atravessarem a fronteira do
  processo; a contenção concorrente fica provada **in-process**, onde o log é
  efectivamente partilhado. O limite tem sensor:
  `TestLimite_EventStoreDeReferenciaNaoArbitraEntreProcessos`.

  > **RESOLVIDO por AOS-100 (2026-08-31) — o backend passou a existir, e o limite era do
  > SUBSTRATO, não desta decisão.** `eventstore/jetstream` implementa o contrato sobre
  > JetStream, onde o `expected_seq` é imposto pelo servidor. O **AC1 de AOS-281 passa a
  > ser satisfazível e está MEDIDO com PROCESSOS REAIS**: quatro `aos-orq serve --nats` em
  > paralelo sobre o mesmo run dão `vencedores=1 negados-pelo-lease=3 guardados(WAL)=0` —
  > todos admitidos ao Event Store, e o vencedor decidido pelo **lease**
  > (`TestAOS100_NServeEmParaleloSobreOSubstratoReplicado`).
  >
  > **O que NÃO muda, e por isso o sensor fica:** o Event Store de REFERÊNCIA continua a
  > não arbitrar entre processos. O sensor mede-o a ele, não «o substrato» em geral, pelo
  > que continua verde e continua CERTO — a propriedade foi ganha por um substrato
  > DIFERENTE, não por aquele. Sobre `--wal` a topologia suportada continua a ser a posse
  > sequencial, com o guard de ficheiro (AOS-285/286) a impedir a configuração insegura.
  > Ver `DEF-282` (FECHADO-RESIDUAL) e o doc de `packages/cmd/aos-orq`, que descreve as
  > duas topologias e o código de saída que as distingue.
- **A janela TOCTOU do `FencedAppender` mantém-se aberta no caso token-IGUAL.** O
  `fencing.go` documenta-a: o token é lido *externamente* e não é dobrado no `expected_seq`
  do evento de negócio, pelo que entre a leitura e o `Append` um novo claim pode elevar o
  corrente. O caso token-estritamente-inferior fica fechado; o caso do detentor a ser
  superado **naquele instante exacto** fica delegado à implementação de produção do
  substrato. Esta decisão **não fecha** essa janela e não deve ser lida como fechando-a.
- **Duas máquinas continuam a escrever no stream `run_id`** (`state.Machine` por-run e
  `GraphBuilder` por-nó). A decisão garante que ambas correm **sob o mesmo dono**, não que
  sejam uma só. Unificá-las é trabalho maior e não é deste ticket; o que este ticket
  impede é que corram sob **donos diferentes**.
- **O `DEF-803`** (decomposição `goal→DAG` é um stub de nó único) **não é fechado por esta
  decisão** e é reavaliado, não resolvido: ter um escritor legítimo do grafo é condição
  necessária para uma decomposição real, não suficiente. Continua aberto, com o eixo
  agora coerente.
- **Um binário novo é superfície operacional nova** (deploy, observabilidade, runbook).
  Fica **desligado por omissão**: nenhum deployment v1 o arranca, e o nó não sabe que ele
  existe.

## 5. Conformidade / Enforcement

Esta decisão só vale se for falsificável. Os guardas que a impõem (a entregar pela parte
de código de AOS-281):

1. **As duas fronteiras existentes, inalteradas.** `TestBoundary_NodeDoesNotImportConcurrentOrchestratorOrScheduler`,
   `TestBoundary_NodeBuildGraphExcludesConcurrentOrchestratorOrScheduler` e
   `TestBoundary_ProductionImportsAreAllowlisted` continuam a correr **sem uma linha
   alterada**. Uma alteração a qualquer um deles é o sinal de que esta decisão foi
   contornada, e exige emenda datada a este ADR **e** ao ADR-018.
2. **Guarda de não-escrita do SCH (§2.2).** O `plandispatch` não adquire porta de escrita
   de ciclo de vida — imposto pelo guard-test de imports que já existe, e a declarar no
   `doc.go` do pacote como regra, não como observação.
3. **Guarda de re-hidratação (§2.3).** Nenhum caminho de produção do `runlifecycle`
   constrói um `GraphBuilder` que não venha de `RebuildDAG`.
4. **Guarda de escrita fenced (§2.4).** Nenhuma escrita de ciclo de vida do `runlifecycle`
   chega ao Event Store por outra via que não o `FencedAppender`.
5. **Prova com dois processos reais (DoD de AOS-281):** disputa do mesmo claim (um vence,
   o outro vê `expected_seq` falhar); escrita com token obsoleto recusada; handoff a
   jusante do gate sem instante de dupla-posse; tomada de posse a meio que re-hidrata e
   não diverge do log.

## 6. Referências

- `specs/EPIC-10_Topologia_Operacao_DR.md` — AOS-281 (critérios de aceitação e DoD).
- `docs/adr/ADR-018-fronteira-no-orq-sch.md` — §2 (v1), §«O que muda no distribuído», §5 (guardas), §5-bis (limite de veracidade do fencing no nó).
- `packages/kernel/agent-runtime/durable/lease.go` — `LeaseManager` (`Claim`/`Heartbeat`/`Release`/`CurrentToken`/`CurrentLeaseExpired`), stream `lease:<run_id>` serializado por `expected_seq`.
- `packages/kernel/agent-runtime/durable/fencing.go` — `FencedAppender`, `TokenSource`, `LeaseExpiryAuthority`, e a janela TOCTOU declarada.
- `packages/kernel/agent-runtime/state/machine.go`, `state/transitions.go` — máquina AOS-017 e `RequiresFencingToken`.
- `packages/control-plane/orchestrator/graph.go` — `GraphBuilder`, `RebuildDAG`, `ErrLogAhead`, e os três defeitos de 2026-08-30 registados em comentário.
- `packages/control-plane/orchestrator/plandispatch/ports.go` — `LifecycleView`, `ResultView`, `BranchJournal`.
- `docs/governance/REGISTO-Deferimentos.md` — DEF-272, DEF-273 (eixo corrigido para AOS-281), DEF-803.

---

## Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Dono do produto | Equipa AOS — Produto | **RATIFICADO** (autoridade de dono) | 2026-08-30 |
| Arquitecto de Plataforma | Equipa AOS — Arquitectura de Plataforma | Assinado — registo AOS-281 (revisão do código que a implementa) | 2026-08-31 |
| Responsável de Segurança | Equipa AOS — Segurança | Assinado — registo AOS-281 (revisão do código que a implementa) | 2026-08-31 |

> **As duas assinaturas técnicas cobrem coisa diferente da ratificação.** A ratificação
> de dono (2026-08-30) fixou a DECISÃO da §2 — e é ela, e só ela, que a torna autoridade
> congelada (Carta §6.1). As assinaturas de Arquitectura e Segurança (2026-08-31) são
> sobre o CÓDIGO que a implementa, entregue e verificado entretanto: `runlifecycle` +
> `aos-orq` mergidos em [#185](https://github.com/albinoJimy/aos/pull/185), com as duas
> fronteiras guardadas por teste **verdes e sem uma linha alterada**, `-race` verde em CI
> e `DEF-272`/`DEF-273` fechados.
>
> **O que estas assinaturas NÃO cobrem**, porque assinar o que não se verificou seria
> pior do que não assinar:
>
> - o **`DEF-282`** — o Event Store de referência não arbitra entre processos — continua
>   **ABERTO**, com sensor. A mitigação entregue (AOS-285/286) impede a configuração
>   insegura; não dá ao substrato a garantia que lhe falta;
> - a **janela TOCTOU do caso token-igual** do `FencedAppender` (§4) continua aberta e
>   delegada à implementação de produção do substrato;
> - a decisão de **mudar a forma da v1** não é tocada por este ADR nem por estas
>   assinaturas — ver `docs/reports/analise-v1-single-host-para-distribuido.md`.
