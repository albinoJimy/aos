# Um objectivo entra no caminho do plano por um FACTO gravado pelo nó, e o `aos-orq` consome-o sob a posse que já governa o run

| Campo | Valor |
|---|---|
| ADR | ADR-028 |
| Título | Ingresso do caminho do plano: facto no nó, consumo sob lease no `aos-orq` |
| Estado | Aceite |
| Data | 2026-09-21 |
| Deciders | Arquitecto de Plataforma |
| Contexto-fonte | AOS-417 (`specs/EPIC-19_Planeador_Meta_Orquestracao.md`) |
| ADRs relacionados | ADR-018 (o nó é a autoridade única do ciclo de vida), ADR-023 (escritor único por lease), ADR-016 (o canal não é oráculo de existência), ADR-027 (cada nó do plano é um run do nó) |
| Supersede | — |

## 1. Contexto

O caminho do **agente único** já é utilizável sem operador: `POST /runs` aceita um objectivo em
linguagem natural e autentica-se por `client_credentials`, que é automatizável.

O caminho do **plano multi-nó não tem superfície de rede nenhuma.** Medido: `ListenAndServe` e
`http.Server` em `packages/cmd/aos-orq/`, fora de testes, dão **zero**; nenhum ficheiro não-teste
preenche `RoutingConfig`… nem, para o caso, existe rota alguma. O serviço está no profile `orq` do
compose precisamente para que o `deploy.sh` **nunca** o arranque, com a razão escrita no ficheiro:
*«um `serve` possui um run e termina, não é um daemon»*.

A consequência mede-se em passos manuais: **alguém tem de estar no terminal do servidor** a
escrever `docker compose run`. Tudo o resto que falta para o produto ser usável — não haver UI,
cunhar o NHI à mão, dois logins no browser, a cerimónia de aprovação — pode ser resolvido e o
problema permanece, porque este passo permanece.

Este ADR decide **por onde entra um objectivo**, sem contradizer o que já está congelado.

## 2. Decisão

### 2.1 O ingresso é uma rota do nó que grava um FACTO — não invoca o orquestrador

O nó `aos` expõe o ingresso. Ao receber um pedido, **grava um facto durável** e devolve. Não
compõe, não importa e não conhece o orquestrador.

Isto respeita as duas fronteiras que estão congeladas:

- **ADR-018** — o nó continua a ser a fonte única de verdade do ciclo de vida, e o `cmd/aos`
  continua sem importar `control-plane/orchestrator` nem `control-plane/scheduler`, nem directa
  nem transitivamente. O guard-test de fronteira (`boundary_orq_sch_test.go`) não muda.
- **ADR-023** — gravar um facto que **não** é uma transição de ciclo de vida é permitido e tem
  precedente no próprio ADR: o SCH «escreve os seus próprios factos de decisão… que vivem no
  stream do plano, não no stream do run». O facto de ingresso é dessa família.

O nó declara a **sua** constante de tipo de evento, como já faz noutros pontos; **não** reutiliza
as constantes de `plannerevents`, que vivem no módulo proibido.

### 2.2 Fila, não daemon: a posse continua a ser o LEASE, e um `serve` continua a terminar

O `aos-orq` **consome** o facto e corre o `serve` como hoje: reclama o lease, possui o run,
termina. Nada no seu modelo de posse muda.

A frase do compose — «um `serve` possui um run e termina, não é um daemon» — **mantém-se
verdadeira**. O que muda é quem o invoca: em vez de um humano num terminal, um trabalhador que lê
o facto.

**Não se inventa substrato de fila.** O Event Store já é a fila, e o consumo-uma-só-vez já tem
molde no repositório: o `approval_store_durable` consome factos duráveis por `Append` com
idempotency-key, usando o `StatusDuplicate` como primitivo de arbitragem. É esse molde que se
segue — não um broker novo, não um estado paralelo (que o ADR-018 §4 proíbe).

Consequência directa: **quem arbitra entre dois consumidores continua a ser o lease**, exactamente
como o ADR-023 fixou. O ingresso não introduz uma segunda autoridade.

### 2.3 Um pedido sobre um run com posse responde `201 accepted`, idempotente

Um pedido cujo run já tem posse **não** recebe uma recusa que revele o estado do run, e **não**
recebe o estado do run.

Recebe o mesmo `201 accepted` idempotente que o `POST /runs` já devolve hoje para
`ErrRunAlreadyInProgress`, `ErrRunLeaseHeldElsewhere` e as restantes colisões.

**Esta é a parte deste ADR que mudou durante a sua própria redacção, e a razão fica escrita.** A
formulação inicial era «devolve o estado do run em curso», com o argumento de que recusar com o
código 3 é um conceito de *operador* a vazar para uma API de *utilizador*. O argumento está certo
— mas a solução proposta colidia com o **ADR-016**: o nó responde `201 accepted` de propósito para
**não ser oráculo de existência**, e só dá `409` a quem traz credencial forte e residência selada
coincidente. Devolver o estado reabriria esse oráculo a qualquer chamador. O ADR-027 fixa a
leitura simétrica do outro lado: para o executor, um `409` na submissão significa run alheio e
**recusa-se**.

Quem quiser o estado do run pede-o pela rota de leitura, que passa pela governação de leitura.

**O código de saída 3 do `serve` fica intacto.** É saída de um PROCESSO, não de um pedido HTTP: o
runbook de despacho multiprocesso, as provas de multiprocesso e o ADR-023 continuam a dizer a
verdade. Mudá-lo não era preciso para resolver o que se queria resolver.

### 2.4 O que o ingresso NÃO faz

- **Não cunha credenciais.** O NHI do run continua a ser cunhado por um humano, com o binding
  humano↔NHI auditável do ADR-003. Este ADR leva o produto de «é preciso alguém no terminal do
  servidor» para «é preciso alguém com credencial» — que é um salto grande e **não** é
  auto-serviço. O tecto de 45 minutos do NHI continua a ser a fricção seguinte, e continua sem
  ticket próprio.
- **Não decide planos.** Um plano de risco continua a esperar a decisão humana assinada
  (ADR-020/AOS-408). O ingresso desencadeia o `serve`; não o absolve do gate.
- **Não substitui o `docker compose run`.** O caminho manual continua a existir, e é o que um
  operador usa quando quer conduzir a corrida à mão.

## 3. Alternativas rejeitadas

**(B) Serviço HTTP próprio no `aos-orq`.** Não mexeria no nó e daria liberdade de desenho. Foi
rejeitada porque duplica **três** coisas que já existem e estão provadas no nó: autenticação,
admissão e observabilidade. Uma segunda superfície com postura diferente da do nó é exactamente o
modo de falha que o ADR-016 vem fechar — e seria preciso replicar a não-oracularidade, o
anti-replay por `jti` e a residência selada, sem gate que o imponha.

**(C) Daemon que aceita e executa no mesmo processo.** Mais simples de escrever. Rejeitada porque
contradiz frontalmente a posse: um processo que atende N pedidos e executa N runs **é** uma
autoridade concorrente sobre o ciclo de vida, e o ADR-023 fixa que o direito de escrever é a posse
do fencing token corrente — não um atributo do processo. Seria preciso emendar o ADR-023, e não há
razão que o justifique quando a fila resolve o mesmo com o mecanismo já ratificado.

**(D) Reutilizar o `PartitionedQueues` do scheduler.** Rejeitada por duas razões independentes: é
**em memória** (não sobrevive a um reinício, que é a fragilidade que o AOS-418 acabou de fechar
noutro sítio) e vive num módulo que o nó está **proibido** de importar.

## 4. Consequências

**O que fica melhor.** Um objectivo pode ser submetido por rede, com a autenticação que o nó já
tem, sem sessão no servidor. O EPIC-13 deixa de estar bloqueado: o BFF passa a ter o que chamar.

**O que fica pior, e é preciso dizê-lo.** O nó ganha conhecimento da **existência** do caminho do
plano — não do seu código, mas do facto de ele existir. É uma fronteira que se aproxima sem se
atravessar, e a única coisa que a mantém honesta é o guard-test de import, que **não muda** com
este ADR. Se alguém o alterar, é sinal de que esta decisão foi contornada.

**Resíduos declarados:**

- **A cunhagem do NHI continua manual** (dois logins no browser, tecto de 45 min). É a barreira
  seguinte ao uso sem operador, é decisão de SEGURANÇA — a `issuer.key` não vai para o servidor
  por desenho — e continua sem ticket próprio.
- **Retenção e tecto da fila não são decididos aqui.** Um facto de ingresso que ninguém consuma
  fica no stream indefinidamente. Definir retenção, tecto de pendentes e o que acontece quando o
  tecto é atingido é trabalho do ticket de implementação, com o molde de backpressure que o
  EPIC-03 já descreve.
- **O substrato de ficheiro não arbitra entre processos** (DEF-282): com `--wal`, dois
  consumidores em simultâneo continuam a ser recusados pelo guard de ficheiro, e a topologia
  suportada continua a ser posse sequencial. Só `--nats` arbitra. O ingresso não muda isto, e
  herda-o.
- **A janela TOCTOU do caso token-igual** que o ADR-023 declara continua aberta. Este ADR não a
  fecha nem a alarga.

## 5. Conformidade / Enforcement

- O guard-test de fronteira (`packages/cmd/aos/boundary_orq_sch_test.go`) continua a proibir o
  import de `control-plane/orchestrator` e `control-plane/scheduler` pelo `cmd/aos`, em imports
  directos e no grafo de build transitivo. **Alterá-lo é o sinal de que esta decisão foi
  contornada**, e exige emenda datada a este ADR, ao ADR-018 e ao ADR-023.
- O tipo de evento do facto de ingresso é declarado como constante junto do emissor, no nó, e a
  sua família tem de constar da taxonomia de `tecnica/13_Modelo_Dados_Eventos.md` — o gate
  `event-catalog` recusa literais e famílias não declaradas.
- A rota nova declara o seu plano na tabela de rotas do nó; o valor-zero aborta o arranque, pelo
  que uma rota sem plano declarado não chega a produção.
- A resposta `201 accepted` idempotente sobre um run com posse tem de ter teste próprio: é a
  garantia de não-oracularidade do ADR-016, e um `409` acidental é uma regressão de segurança, não
  de comportamento.

## 6. Referências

- `specs/EPIC-19_Planeador_Meta_Orquestracao.md` — AOS-417 (contexto-fonte e critérios).
- `docs/adr/ADR-018-fronteira-no-orq-sch.md` — a autoridade única do ciclo de vida.
- `docs/adr/ADR-023-escritor-unico-ciclo-vida-por-run.md` — a posse é o lease; serialização não é
  arbitragem.
- `docs/adr/ADR-027-execucao-dos-nos-do-plano-como-runs-do-no.md` — o executor e a leitura do
  `409`.
- `packages/integration/approval_store_durable.go` — o molde de consumo-uma-só-vez por
  idempotency-key.
- `tecnica/13_Modelo_Dados_Eventos.md` — a taxonomia de famílias de evento.

## 7. Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
