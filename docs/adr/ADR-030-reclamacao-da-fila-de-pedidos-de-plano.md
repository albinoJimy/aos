# ADR-030 — A fila de pedidos de plano ganha uma rota de RECLAMAÇÃO, e a não-oracularidade ganha casa

| Campo | Valor |
|---|---|
| Estado | **Aceite (2026-09-23, AOS-423)** |
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
- **O submissor continua sem forma directa de saber o desfecho.** A §2.6 grava factos de desfecho,
  mas se o `run_id` de topo nunca existir como run legível (ADR-027 materializa nós como
  `<run>~<nó>`), a leitura pelo read-path não o alcança. **POR CONFIRMAR** — e é critério do
  AOS-423, não deste ADR.
- **A varredura linear do stream** que o molde usa não tem tecto numa fila de pedidos. Declarado.
