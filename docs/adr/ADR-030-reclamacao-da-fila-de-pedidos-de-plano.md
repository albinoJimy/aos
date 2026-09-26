# ADR-030 — A fila de pedidos de plano ganha uma rota de RECLAMAÇÃO, e a não-oracularidade ganha casa

| Campo | Valor |
|---|---|
| Estado | **Aceite (2026-09-23, AOS-423)** — §2.6 **emendado** (2026-09-25, AOS-442): o `aguarda_humano` ESTACIONA e é re-oferecido; já não fecha o pedido — §4 com **nota** (2026-09-26, AOS-447): a forma do trabalhador, decidida |
| Decisores | Arquitecto de Plataforma |
| Consultados | ADR-016 (fronteira de confiança da UI), ADR-018 (o nó é a única autoridade de ciclo de vida), ADR-023 (escritor único sob lease), ADR-028 (ingresso do caminho do plano), DEF-282 (o substrato de ficheiro não arbitra entre processos) |
| Supera / emenda | **Nada supera.** Corrige uma ATRIBUIÇÃO errada no ADR-028 §2.3 e em `plan_ingress.go` — ver §1.2 |

## 1. Contexto

### 1.1 O `201` promete uma corrida que não começa

O AOS-417 abriu o `POST /plans`: aceita um objectivo, grava um facto durável em
`aos-internal/plan-requests` e devolve `201 accepted`. **Nada consome esse facto.** O ADR-028 §2.2
decidiu que haveria «um trabalhador que lê o facto» e o AOS-417 não o entregou — deixou a metade
do consumo por fazer, declarada.

O resultado é um produto que promete e não cumpre: um cliente que chama o ingresso recebe uma
confirmação e nunca nada acontece. É a forma de defeito que este eixo inteiro existe para fechar.

### 1.2 A CORRECÇÃO QUE ESTE ADR TEM DE FAZER ANTES DE DECIDIR

O `plan_ingress.go` e o ADR-028 §2.3 justificam ambos a postura do `201` com a mesma frase: «é a
garantia de **NÃO-ORACULARIDADE do ADR-016**». Fui reargumentar essa tese para o caso de um
consumidor autenticado — e **a tese não está no ADR-016**.

Medido: o ADR-016 tem 322 linhas e zero ocorrências de «oráculo», «enumerar», `201`, `409` ou
`404`. O que ele decide é adjacente e, para o que aqui interessa, **mais forte**:

- **§5 — read-path SOBERANO.** «Toda a leitura servida […] é soberana e fail-closed: cada endpoint
  resolve `board → região` via a **mesma** autoridade imutável que o PEP usa
  (`sovereignty.Registry.Authorized`) e **recusa** servir fora da região autorizada.»
- **§6 — separação canal-controlo / canal-dados.** O canal de dados é *untrusted* e «nada que
  chegue por aqui pode tornar-se instrução»; o canal de controlo é POST com decisão **assinada**
  verificada por um `Authenticator`.

A prática do `201`-nunca-`409` **é real e está imposta em código** (`POST /runs`, `POST /plans`,
com teste). O que não existia era a sua **fonte canónica**: a atribuição ao ADR-016 é uma paráfrase
que sedimentou, citada por dois sítios que se citam um ao outro.

**Este ADR dá-lhe casa** (§2.1) em vez de a deixar a apontar para onde ela não está. É a mesma
classe de defeito que o AOS-424 e o AOS-425 encontraram três vezes: uma afirmação que ninguém
re-derivou, e que passa a ser a razão pela qual mais ninguém verifica.

### 1.3 Porque é que o consumidor não pode simplesmente ler o stream

O `aos-orq` não partilha substrato com o nó. Sobre ficheiro, o Event Store **não arbitra entre
processos** (DEF-282): quem detém o `LockWAL` é o nó, e um segundo leitor-escritor do mesmo WAL é
precisamente o que o AOS-285/286 recusa. Sobre JetStream arbitraria — mas isso exige levantar NATS
em produção e migrar o nó, que é decisão de infraestrutura e não deste ticket.

## 2. Decisão

### 2.1 A NÃO-ORACULARIDADE passa a ter fonte: é esta secção

**Uma superfície HTTP do nó não revela a EXISTÊNCIA de um recurso a quem não pode agir sobre ele.**
Um pedido para um run já pedido responde `201 accepted`, igual a um pedido novo; um `409` só é
devido a quem traz credencial forte **e** residência selada coincidente.

Isto não deriva do ADR-016 — deriva da mesma postura que o ADR-016 §5 aplica às LEITURAS
(soberana, fail-closed, recusa fora da região) estendida às **respostas de escrita**: se a leitura
não pode revelar fora da fronteira, a resposta a uma escrita também não pode fazê-lo por um canal
lateral. Quem cite esta tese cita **este ADR**, e o ADR-028 §2.3 e o `plan_ingress.go` passam a
apontar para aqui.

### 2.2 A rota é de RECLAMAÇÃO, e vive no NÓ

`POST /plans/claim` — o `aos-orq` pede UM pedido pendente; o nó reclama-o atomicamente e devolve-o.
Nenhuma rota enumera a fila, e nenhuma rota devolve um pedido sem o reclamar.

**Porque no nó, e não um consumidor a ler o stream:** mantém as duas fronteiras que custaram a
estabelecer. O nó continua a **não correr** o plano (ADR-018) — só entrega trabalho —, e a posse
de um run continua a ser o lease (ADR-023), tomada pelo `aos-orq` como já é hoje.

**Porque reclamação e não leitura:** uma rota que LÊ a fila é um oráculo de existência para quem a
alcance; uma rota que RECLAMA devolve no máximo um item, consome-o, e não diz nada sobre o que
resta. A fila não é enumerável por construção, não por filtro.

### 2.3 A postura é `planoDados` + Bearer OIDC + gate soberano

**Não** `planoControlo`. A razão é um precedente medido: o `POST /runs` **cria um run** — acção
mutante, chamada pelo `aos-orq` com Bearer OIDC (`client_credentials`, `jti` de uso único) e NHI no
corpo — e está classificado `planoDados`. Uma rota de reclamação com a mesma postura é consistente
com o sistema que existe.

O gate soberano (`readGov`) aplica-se, e é isso que cumpre o ADR-016 §5: a reclamação resolve
`board → região` pela mesma autoridade que o PEP usa, e **um pedido submetido fora da região do
reclamante não lhe é entregue**.

**A alternativa mais rigorosa foi rejeitada com o custo escrito.** Classificar como `planoControlo`
exigiria assinatura ed25519 sobre payload canónico com nonce durável. O `aos-orq` gera hoje uma
chave ed25519 **efémera por execução** (`planner_wiring.go`) e não tem env de chave persistente:
seria material criptográfico novo montado em produção, com rotação e pinagem no roster do nó. O
risco que o §6 do ADR-016 fecha é **conteúdo untrusted tornar-se sinal de controlo** — e uma
reclamação não é dirigida por conteúdo: não transporta payload do chamador, e o que ela devolve já
estava no log.

### 2.4 A rota traz a sua própria autorização, porque não herda nenhuma

Medido, e é a razão de esta secção existir: a fila está protegida hoje por **duas coisas que uma
rota nova não herda**.

1. **A barra.** `aos-internal/plan-requests` tem uma `/`, e o padrão `{id}` do `http.ServeMux` casa
   **um só segmento** — logo `GET /runs/aos-internal/plan-requests/trajectory` dá 404 por
   roteamento, não por política.
2. **`runIDReservado`**, que recusa o prefixo nas duas rotas de submissão.

A trava do AOS-426 (`streamDeRun`) **não** protege a fila: o `RunID` sintético dos seus eventos é
igual ao nome do stream, pelo que ela devolveria `true`. Está escrito assim no próprio teste do
AOS-426, que põe a fila no grupo «os que a barra já protegia».

**Qualquer rota cujo caminho não seja um `{id}` de um segmento contorna as duas.** A rota de
reclamação é uma dessas, e por isso a sua autorização é explícita e testada, nunca herdada.

### 2.5 Reclamar UMA vez, e sobreviver a um consumidor que morre

O molde é o `approval_store_durable.go`: um facto de reclamação com `StepID = "claim-<gen>-<id>"`,
e o `StatusDuplicate` do Event Store como árbitro. Não se apaga nada — reclama-se.

**Com uma diferença deliberada face ao molde.** O `Consume` das aprovações reclama ANTES de ler, e
se o processo morrer a seguir o item fica **queimado** — lado seguro escolhido para um grant de
autoridade humana. Para um pedido de plano o lado seguro é o **oposto**: um pedido perdido em
silêncio é exactamente o defeito que este ticket fecha. Daí a **geração** (`<gen>`), que é o padrão
de re-encarnação que o mesmo ficheiro já usa: uma reclamação que expira sem desfecho pode ser
reclamada na geração seguinte.

### 2.6 Desfecho: transitório e permanente NÃO se tratam igual

> **Emendado por AOS-442 — ver [Emenda](#emenda-aos-442--o-aguarda_humano-estaciona-e-é-re-oferecido-não-fecha-o-pedido).**
> A linha «nem um nem outro» da tabela fica como registo da decisão original; a implementação
> tratava-a como terminal, e um plano aprovado depois da decisão humana nunca mais corria.

Os códigos de saída do `serve` já distinguem, e a distinção mapeia-se assim:

| Classe | Códigos | Tratamento |
|---|---|---|
| **Transitório** | 3 (lease detido), 4 (fenced), 5 (WAL detido), 8 (nós em voo) | a reclamação caduca; o pedido volta à fila na geração seguinte |
| **Permanente** | 7 (decisão recusada), 9 (plano rejeitado) | facto de desfecho terminal; **não** se retenta |
| **Nem um nem outro** | 6 (pendente de aprovação humana) | o pedido fica marcado à espera de humano; nem retentativa nem desfecho |

Confundir transitório com permanente dá um de dois defeitos, e ambos são piores do que a fila
parada: um pedido perdido, ou um laço a retentar para sempre uma recusa determinista.

### 2.7 Tecto: recusar pedidos novos, nunca descartar antigos

Com tecto atingido, o ingresso recusa **pedidos novos**. Descartar os antigos em silêncio é a mesma
classe de defeito que este eixo fecha.

## Emenda (AOS-442) — o `aguarda_humano` estaciona e é re-oferecido; não fecha o pedido

**O que a produção e a discovery mostraram.** A §2.6 diz que um plano pendente de aprovação humana
não é «nem retentativa nem desfecho», mas não diz como sai desse estado. A implementação do AOS-423
preencheu o silêncio da forma errada: `aguarda_humano` contava como terminado na projecção da fila
(`plan_claim.go`), e depois de o humano decidir **nada no caminho da fila voltava a correr o
pedido** — ficava aprovado e parado. Em paralelo, o `consume` retomava sempre por `serve --goal`: o
modelo re-decompunha, saía outro organigrama, e o gate recusava-o (AOS-412, saída `7`). Uma falha
transitória depois da aprovação tornava-se definitiva (`plan-e2e-437-1790336067`; AOS-438 resíduo 1).

**Decisão emendada.**

1. **O nó ESTACIONA um pedido cujo último desfecho é `aguarda_humano`**, e volta a oferecê-lo a quem
   drena a fila depois de um intervalo fixo (`intervaloDeReverificacao`, 10 min), numa geração
   nova. Só a **última** geração estaciona; só um desfecho `terminal` fecha. O estacionado **não**
   conta como terminado para a marca de água do AOS-429.
2. **O nó não sabe o que é uma decisão** — a fronteira do ADR-018 fica onde estava. Re-oferecer é
   mecânica de fila, do mesmo género do TTL da reclamação (§2.5). Quem reclama a re-oferta é que
   verifica, **lendo o log do run no `aos-orq` antes de correr o `serve`**: com decisão, corre o
   pedido pelo documento validado e o gate decide (aprovado corre, recusado fecha com `7`); sem
   decisão e com o plano a exigir humano, reporta `aguarda_humano` **sem correr o `serve`** (sem
   posse, sem modelo, sem gastar o `--max` da drenagem); sem decisão e sem nós de risco (uma
   auto-aprovação que ficou a meio), corre-o pelo documento e o gate auto-aprova. O prazo do
   pendente (24 h, o do `decide`) é imposto pelo `consume` e pelo `serve`: fora dele, `7`, e a
   re-oferta acaba. Um carimbo do `plan.validated` ilegível conta como expirado.
3. **A retoma de um pedido corre pelo documento validado, nunca por decomposição nova.** O documento
   continua fora do log (ADR-005); o `serve` escreve-o por `--plan-out`, de forma atómica, quando o
   plano é validado — pendente ou aprovado —, **antes** de apensar os factos, e o `consume` guarda um
   por pedido numa pasta do volume do `aos-orq` (`planos/`, ao lado do WAL; o nome é o SHA-256 do
   `run_id`). O LOG decide se o ficheiro é usado: sem `plan.validated` no run, decompõe-se
   (`serve --goal`) e um documento que lá esteja é substituído, nunca usado; validado e sem
   documento, `7` sem correr o `serve` — uma decomposição nova seria recusada pelo gate.
4. **O que a retoma por documento garante, e só isso.** Num run com `plan.validated` e ainda sem
   decisão, o `serve --plan-doc` exige que o hash do documento seja o do validado, **em qualquer
   ramo** do gate (com ou sem risco); com decisão, o gate exige o hash DECIDIDO e recusa (`7`) o
   resto. Passa pela mesma validação estrutural e pelo mesmo gate que o `--goal`, e o gate confronta
   o conteúdo do snapshot com o selado. **Não cobre** o `serve --goal` repetido: um organigrama sem
   risco decomposto por cima de um pendente continua a auto-aprovar-se com o seu próprio hash — é
   comportamento do AOS-408 com teste que o fixa (`TestAOS408_AprovacaoDeOutroOrganigramaNaoServe`,
   passo 2), e o `consume` não o exerce, porque nunca decompõe um run já validado. Num run SEM
   `plan.validated`, um `serve --plan-doc` manual continua a aceitar o documento que o operador lhe
   dá, como sempre — é o caminho do AOS-412, e o gate aplica-se-lhe como a qualquer outro.
5. **Uma recusa determinista FECHA o pedido.** Um documento que não descodifica, não valida, não é o
   do `plan.validated`, ou não se consegue ler, e um snapshot que não é o declarado ou o selado, saem
   com um código novo, `10` (`exitDocumentoRecusado`), de classe `terminal`. Como `1` genérico eram
   transitórios, e o pedido voltava à cabeça da fila para sempre.

**Tabela da §2.6 à luz da emenda** — muda só a última linha:

| Classe | Códigos | Tratamento |
|---|---|---|
| **Nem um nem outro** | 6 (pendente de aprovação humana) | o pedido fica **estacionado**; re-oferecido de 10 em 10 min para o consumidor re-verificar, até um desfecho de outra classe |
| **Permanente** (acrescento) | 10 (documento ou snapshot recusado) | facto de desfecho terminal; **não** se retenta |

**O vocabulário não muda.** As três classes (`transitorio`, `terminal`, `aguarda_humano`) e os
quatro estados do `GET /plans/{id}` (ADR-031) continuam os mesmos; o estado servido de um pedido
estacionado é `aguarda_humano` enquanto a última geração o disser, `in_progress` durante uma
re-verificação, e `terminal` depois do desfecho que o fecha. O ADR-031 não é emendado.

**Custo aceite e declarado.** Um pedido à espera de humano escreve uma reclamação e um desfecho no
stream da fila por re-oferta — no máximo ~144 pares no dia que o prazo lhe dá. E a latência entre a
decisão humana e a execução é até ao intervalo de re-oferta mais o do timer de drenagem.

**Resíduos declarados da emenda.**

- **A cópia em claro do documento.** O `planos/<sha256>.plan.json` contém o organigrama — o
  objectivo derivado pelo modelo e o dos nós — em claro, fora do alcance do apagamento DSAR (que
  destrói a KEK do titular, e este ficheiro não está cifrado sob ela). O `consume` apaga-o quando
  reporta um desfecho `terminal`; **enquanto o pedido não fecha, a cópia existe**, e um `/dsar/erase`
  do titular não a alcança.
- **`--nats` exige a pasta PARTILHADA entre as réplicas.** O `--plan-dir` é obrigatório sobre o
  substrato replicado, mas nada verifica que seja partilhado: uma pasta local a cada réplica faz a
  re-oferta, noutra réplica, ver um plano validado sem documento — e fechá-lo com `7`. Produção corre
  sobre `--wal`, com uma só réplica.
- **Um pedido cujo objectivo selado já não abre** (KEK do titular destruída) não fecha: o nó salta-o
  e entrega o seguinte, e ele fica reclamado até ao TTL, para ser saltado outra vez. Fechá-lo exige
  decidir QUEM escreve esse desfecho e com que código — o nó não conhece os códigos do `serve`.
- **A composição nó↔`consume` não corre num só teste** (dois binários de módulos distintos).

**Alternativas rejeitadas.** (a) O `decide` notificar o nó por uma rota nova: acoplava a cerimónia
humana à disponibilidade do nó, exigia uma rota e um tipo de facto novos, e uma notificação perdida
deixava o pedido parado sem forma de a repetir (o `decide` recusa uma segunda decisão). (b) O
`serve --goal` descobrir sozinho o documento guardado: mudava o sentido do `--plan-out` (de escrita
para leitura) e o do `--goal` repetido, que é caminho testado (AOS-408, AOS-415).

## 3. Alternativas consideradas

**NATS partilhado entre o nó e o consumidor.** É o único substrato que arbitra entre processos e o
único que escala para além de um consumidor. Rejeitada **por agora**, não por princípio: exige
levantar JetStream em produção e migrar o nó de `AOS_EVENTSTORE_PATH` para NATS. Continua a ser o
destino, e é também o que destranca o teste sobre JetStream que o AOS-424 e o AOS-425 deixaram
declarado como resíduo.

**Consumidor DENTRO do processo do nó.** Eliminaria o problema da tranca — quem tem o `LockWAL` é o
nó. Rejeitada: poria o nó a invocar o `aos-orq`, e o nó deixaria de apenas CONHECER o caminho do
plano para o DESENCADEAR. É o ADR-018 de frente, e exigiria emenda a uma fronteira cara.

**Uma rota que LÊ a fila** (em vez de reclamar). Rejeitada pela §2.2: seria um oráculo de
existência, e a postura da §2.1 acabou de lhe fechar a porta.

## 4. Consequências

**O que fica melhor.** O `201` deixa de ser uma promessa vazia. E a não-oracularidade deixa de ser
uma paráfrase circular: tem fonte, e os dois sítios que a citavam passam a apontar para ela.

**O que fica pior, e é preciso dizê-lo.** Existe agora uma superfície HTTP que devolve o conteúdo
de um pedido de plano — objectivo incluído — a um chamador autenticado. O AOS-417 fechou-a de
propósito; reabre-se com autorização explícita, gate soberano e teste, mas **reabre-se**. Se o gate
soberano não estiver composto (`readGov == nil`), a rota tem de recusar, não de servir.

**Resíduos declarados:**

- **A forma do trabalhador** (processo longo vs temporizador) **não é decidida aqui**. Entrega-se um
  comando que DRENA UMA VEZ e termina; quem o invoca — um timer do host, como o `aos-tls-sync.timer`
  que já existe, ou um serviço — é decisão de implantação, e o código é o mesmo nos dois casos.
  **Decidida depois** — ver a nota abaixo (AOS-447).
- **O submissor continua sem forma directa de saber o desfecho.** A §2.6 grava factos de desfecho,
  mas se o `run_id` de topo nunca existir como run legível (ADR-027 materializa nós como
  `<run>~<nó>`), a leitura pelo read-path não o alcança. **POR CONFIRMAR** — e é critério do
  AOS-423, não deste ADR.
- **A varredura linear do stream** que o molde usa não tem tecto numa fila de pedidos. Declarado.

### Nota (2026-09-26, AOS-447) — a forma do trabalhador: um timer de 1 minuto, um pedido de cada vez

**Decidido pelo dono**, sobre a medição de produção de 2026-09-25 (um pedido esperou até 5 min para
começar, com `OnUnitInactiveSec=5min` e até 3 pedidos em série por drenagem):

- **Um trabalhador**: o timer `aos-drenar-planos` do host, que volta **1 min depois** de a drenagem
  anterior acabar (`OnUnitInactiveSec=1min`, `AccuracySec=5s`). O systemd nunca arranca um oneshot
  que ainda está activo, pelo que continua a haver no máximo uma drenagem de cada vez.
- **Um pedido por drenagem** (`Environment=DRENAR_MAX=1` na unidade, que muda junto com o timer): um pedido espera no
  máximo o plano em curso mais ~1 min, e não os dois que calhassem à frente dele na mesma drenagem.
- **Não** um `consume` contínuo: o comando continua a drenar uma vez e terminar, e nenhum binário além
  do nó passa a ser um serviço de longa duração.

**Vários trabalhadores só com uma medição que o justifique**, e não antes das duas pré-condições que
o desenho encontrou:

1. **O WAL do `consume` é de posse sequencial.** Dois trabalhadores sobre o mesmo `consume.wal`
   serializam-se no `LockWAL` (saída 5); o caminho é um WAL **por run** (ou o substrato replicado da
   §3). O mesmo WAL explica o achado vizinho: o `decide` da cerimónia de aprovação toma posse de
   escrita desse WAL, e sai com 5 enquanto o `serve` de um plano o detém — o plano inteiro
   (`TestAOS447DecideBloqueadoEnquantoUmServeDetemOWAL`).
2. **O TTL da reclamação (30 min, `ttlDaReclamacao`) é mais curto do que o prazo do plano (40 min,
   `prazoDoPlanoPorOmissao` do `aos-orq`).** Um plano entre os 30 e os 40 min vê a reclamação expirar
   e o pedido volta a ser elegível — e o `GET /plans/{id}` diz `pending` — enquanto ainda corre. Com um
   só trabalhador é inofensivo (ninguém mais o reclama; o desfecho tardio é aceite e fecha-o). Com
   dois, o segundo reclamá-lo-ia e correria o mesmo plano (o lease do run arbitra, com saída 3). A
   pré-condição é **TTL da reclamação ≥ prazo do plano** (mais a decomposição).
