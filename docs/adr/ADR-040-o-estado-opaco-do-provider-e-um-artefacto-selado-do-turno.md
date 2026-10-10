# ADR-040 — O estado opaco do provider é um artefacto selado do turno, referido no tail por digest e pertencente à rota que o produziu

- **Estado:** Aceite
- **Data:** 2026-10-08
- **Deciders:** Dono do produto (decisões de 2026-10-07: **D3** — o raciocínio do modelo nunca
  é usado como resposta e nunca entra no texto do tail; **D4** — o segundo modelo é o Claude,
  com o raciocínio ligado, pelo mesmo proxy; e a forma «por referência» recomendada no desenho:
  os bytes ficam na captura selada e o tail leva só um digest) · executor de AOS-514
- **Tickets:** AOS-514 (a captura e a referência, **às escuras**: nada é reenviado ao
  provider), implementado e revisto de forma independente (2026-10-08, sem bloqueantes; as
  correcções da revisão estão neste texto); por verificar em produção. Nasce desligado
  (`AOS_MODEL_PROVIDER_STATE=off`) e só liga por decisão do dono, **com as duas condições do
  §2.10**. A **devolução** do
  estado ao provider é do AOS-515 (implementado a 2026-10-08, por rever): as regras estão no
  §2.9 e a decisão no §2.11.
- **Relacionados:** ADR-001 (execução durável; replay `resume-from-step`), ADR-002 (Reference
  Monitor), ADR-005 (untrusted é dados, nunca instruções), ADR-007 (Event Store), ADR-010
  (replay determinístico), ADR-011 (apagamento por titular), ADR-034 (autorização derivada do
  contexto), ADR-036 (o tail é a forma canónica; §2.7 é emendado aqui — ver §6), ADR-037 (o
  veredicto `empty_output`), ADR-039 (a nova tentativa por resposta vazia)

> **Resumo.** O que um provider devolve com um turno e pode exigir de volta no turno seguinte —
> o raciocínio em todos os nomes em que veio, os blocos assinados e os redigidos, o id e a
> assinatura de cada tool call — é o **estado opaco do turno**. É tirado do corpo cru **byte a
> byte**, fechado num envelope com a rota que o produziu e um nonce, e guardado **selado** na
> captura do turno, com a cifra por titular do resto do conteúdo. O tail refere-o por **digest**
> (o rótulo `state_digest`, layout 1.5.0), e é assim que o `prompt_hash` se compromete com ele
> sem o conter. É carga opaca e `untrusted`: nunca é resposta, nunca é instrução, não muda a
> autoridade de nenhum turno, e não aparece em eventos em claro, spans, métricas ou logs.
> **Este ADR não devolve nada ao provider.**

## 1. Contexto

Lido no código a 2026-10-07 (`docs/reports/desenho-a2-estado-opaco-2026-10-07.md` §1 e §10) e
medido atrás da imagem de produção do proxy (`docs/reports/wire-live-aos508-2026-10-07.md`):

- O raciocínio só chegava à captura por **um** campo: `reasoning_content` ou, desde o AOS-509,
  o primeiro dos outros nomes com conteúdo. Quando vinham vários, só um ficava. As assinaturas
  dentro de `thinking_blocks` ficavam por acaso, como bytes do valor escolhido.
- O **id de tool call do provider era descartado** na tradução para o runtime; o que volta ao
  provider é o id do runtime, `<passo>-tool-<n>`.
- Atrás do proxy, `thinking_blocks` em lista e as assinaturas **sobrevivem**; `thinking`,
  `reasoning_details` e `refusal` são **movidos** para `message.provider_specific_fields`.
- O que os fornecedores exigem está na tabela do ticket AOS-514 (consultada a 2026-10-08): a
  Anthropic exige os blocos de raciocínio **completos e sem alteração** ao devolver o resultado
  de uma tool, incluindo os redigidos e os de texto vazio, e recusa com 400 um bloco alterado.

Para a segunda família de modelos (decisão D4) completar o segundo turno com tools, o runtime
tem primeiro de **ter** esse estado, intacto. Este ADR decide o que ele é, onde vive, quem o lê
e o que não pode fazer. Devolvê-lo é outra decisão, tomada depois de medida (AOS-512, AOS-515).

## 2. Decisão

### 2.1 O que é o estado opaco de um turno

Tudo o que a resposta de um turno traz, o runtime não interpreta, e o provider pode exigir de
volta:

| Elemento | De onde se tira | O que se guarda |
|---|---|---|
| Raciocínio | Cada chave de `message` de entre `reasoning_content`, `reasoning`, `reasoning_details`, `thinking_blocks`, `thinking`, presente e não nula — **todas**, e não só a primeira | Os bytes JSON do valor |
| Raciocínio movido pelo proxy | As mesmas chaves dentro de `message.provider_specific_fields` | Os bytes JSON do valor |
| Blocos assinados e redigidos | Estão dentro dos valores acima (`thinking_blocks`) | Vão com o valor, pela ordem em que vieram; não se filtra por tipo |
| Id de tool call do provider | `tool_calls[n].id` | Os bytes JSON do valor, e a posição `n` |
| Assinatura por chamada | `thought_signature`, `signature`, `provider_specific_fields`, `extra_content`, na tool call e na sua `function`, com conteúdo | Os bytes JSON do valor |

As listas de nomes são **fechadas** e vivem no código da porta
(`packages/platform/model-gateway/port/state.go`). Uma chave que não esteja nelas — a recusa,
uma chave desconhecida — não é estado. O `content` nunca é estado.

Uma resposta **tem estado** quando traz pelo menos um campo de raciocínio com conteúdo, ou uma
tool call com id ou assinatura. Quando tem, guardam-se todos os campos de raciocínio presentes,
incluindo os vazios: o que um dia se devolve é o que veio. Lê-se a primeira escolha, como o
resto do gateway.

### 2.2 Os bytes são os recebidos

Cada valor guarda-se como os bytes do corpo: **sem re-serializar, sem normalizar espaços nem a
ordem das chaves, sem descodificar strings, sem neutralizar**. Uma assinatura não sobrevive a
nenhuma dessas operações, e quem a valida é o fornecedor. A ordem é a do corpo; uma chave
repetida guarda-se as vezes que veio. O runtime e o gateway **não lêem nem verificam**
assinaturas.

### 2.3 O envelope, a rota e o nonce

Quem faz o pedido — o adaptador do gateway — fecha o estado num **envelope**
(`port.ProviderStateEnvelope`), e é o envelope, em bytes, que atravessa para o runtime:

- **a rota a que pertence**: o digest do perfil da rota com que o turno foi comparado (vazio com
  a governação da rota desligada), o nome do modelo pedido e o modelo que serviu. **O estado é
  da rota que o produziu**: só a ela pode vir a ser devolvido (§2.9);
- **o resultado da comparação da rota** desse turno (`route_check`: `igual`, `diferente` ou
  `nao_reportado`; ausente com a governação desligada) — acrescentado pelo AOS-515, depois da
  revisão: o digest do perfil e o nome do modelo não dizem se a rota se **provou** (§2.11);
- **um nonce de 256 bits**, aleatório, por turno;
- os valores, em base64 (é o que guarda bytes arbitrários sem os tocar).

O runtime trata o envelope como **carga opaca**: não o abre.

### 2.4 Onde fica: na captura do turno, selado

O envelope vai para a captura de não-determinismo do turno (`replay.captured`), nos campos
`provider_state`, `provider_state_ref` e `provider_state_status` do registo da resposta. É
**conteúdo**, como o texto e o raciocínio de sempre:

| Modo da captura | O que fica |
|---|---|
| Cifra por titular (produção) | Dentro do envelope cifrado sob a chave do titular do run, com o resto do turno. No evento em claro não há um byte do estado, nem o digest, nem o nome do campo |
| Modo sensível (referência, sem cifra) | Só a **referência**: o digest do envelope. Os bytes não são guardados. Para efeitos de devolução **o estado não existe** — uma rota que o exija fica declarada como não suportada nesse modo (§2.9) |
| Armazenamento externo de payload (mode 3) | Com o resto do payload, sob o IAM próprio do store; o evento fica só com o consumo |
| Sem cifra nem modo sensível (desenvolvimento) | Em claro no evento, como o texto do modelo |

**Não entra** em `turn.recorded`, em spans, em métricas, em logs nem em mensagens de erro. O
que há em claro sobre o estado é só medição, em vocabulário fechado e inteiros: a ficha do
AOS-507 (`provider_state`: `capturado` ou `nao_devolvivel`; `provider_state_bytes`) e a métrica
`aos_model_provider_state_total{resultado}`.

### 2.5 O tail refere-o por digest (layout 1.5.0)

O digest do estado é `sha256` dos bytes do envelope, calculado **pelo runtime** — o que um
cliente de modelo declare não é lido. O layout **1.5.0** é o 1.4.0, byte a byte, mais um
rótulo: o **primeiro segmento** que um turno com estado acrescenta ao tail — o `history`, ou a
primeira `tool_call` quando o modelo não escreveu texto — leva, no fim da linha de delimitação,

```
state_digest=sha256:<hex>
```

no molde do `args_digest`: está na linha de delimitação, onde só o runtime escreve.

- **O `prompt_hash` compromete-se com o estado sem o conter.** O prompt do turno seguinte leva
  o digest; dois estados diferentes dão dois `prompt_hash`.
- **Um turno sem estado é o de sempre.** Sem rótulo, a mesma captura, o mesmo `prompt_hash`: um
  run sem estado em 1.5.0 materializa os bytes do 1.4.0. Nos layouts 1.3.0 e 1.4.0 o digest é
  ignorado. O turno que acaba o run não acrescenta segmentos e não leva rótulo.
- **O digest não é um oráculo.** O tail é enviado ao provider do turno seguinte, que depois de
  um failover não é o que produziu o estado. O `sha256` de um raciocínio curto confirmava-se
  por tentativas; com o nonce do envelope (§2.3) o digest não revela nada do que compromete.
  **Sem nonce não se guarda estado**: se a fonte de aleatoriedade falhar, o estado fica «não
  devolvível» (§2.7).
- **O layout dos runs novos só muda com a captura ligada.** Desligada, os runs novos ficam em
  1.4.0 e o `assembly_version` de cada turno é o de sempre. O layout é fixado por run
  (ADR-036 §2.3): um run começado em 1.5.0 continua nela.
- **A projecção nativa** (ADR-036 §2.4) não escreve a linha de delimitação do `history` nem da
  `tool_call`: as mensagens de um tail em 1.5.0 são, byte a byte, as do mesmo tail em 1.4.0, em
  todas as versões publicadas (1.0.0, 1.1.0, 1.2.0). Em texto único o rótulo — o digest, nunca
  o estado — está no prompt.

### 2.6 É carga opaca e untrusted: o que o estado não pode fazer

O estado é escrito pelo modelo e pelo provider. Por definição é `untrusted` (ADR-005, ADR-034).

1. **Nunca é resposta (D3).** O texto de um turno vem só do `content`. Um turno que acaba o run
   sem texto e com estado continua a fechar `empty_output` (ADR-037), e a recuperação continua
   a ser a do ADR-039.
2. **Nunca é instrução.** Não entra no texto de nenhum segmento do tail; o runtime e o gateway
   não o interpretam.
3. **Não altera a autoridade do turno.** A autoridade deriva do **tipo** (`Kind`) dos segmentos
   do tail (ADR-034), e essa derivação não lê rótulos. O segmento que leva o rótulo (`history`
   ou `tool_call`) tem a marca textual `taint=untrusted`, mas na autoridade vale o que valia o
   contexto em que foi produzido — que no primeiro turno de um run sem entradas pode ser
   trusted. O rótulo não muda isso em nenhum dos sentidos: é por a derivação só ler o tipo, e
   não por o segmento ser untrusted, que o estado não dá autoridade.
4. **O id do provider não é identidade.** A tool call do runtime não tem id de autorização
   vindo do provider. O Reference Monitor, a chave de idempotência (`f(run_id, step_id)`), o
   step-ledger, os checkpoints e os eventos usam o id do runtime, como sempre. O id do provider
   liga-se a ele pela **posição** (`n`), nunca como chave.
5. **O id do provider é limitado antes de qualquer uso.** Os bytes guardam-se como vieram, e
   o id fica marcado como **utilizável** só se for uma string JSON cujo valor **descodificado**
   tem de 1 a 128 bytes no alfabeto `[A-Za-z0-9_.:-]`. O valor descodificado guarda-se ao lado
   (`id_value`), e é **ele** — nunca os bytes crus, que podem escrever o mesmo valor com
   escapes — que pode vir a ser posto num pedido ou usado como chave.
6. **A sonda e o descodificador têm de ver a mesma mensagem.** A sonda do estado lê o corpo
   cru; a resposta do turno é descodificada pelo `encoding/json`, que aceita chaves noutra
   caixa e funde objectos repetidos. Num corpo anómalo as duas leituras discordam. Quando o
   número de tool calls ou o raciocínio não batem, o estado do turno **não é guardado**
   (`nao_devolvivel_desalinhado`): nunca fica um estado em que a n-ésima entrada não é a
   n-ésima tool call do turno.
7. **O nonce é contrato de quem constrói o envelope.** O runtime faz o `sha256` do que qualquer
   cliente de modelo lhe entregue. Um cliente que entregue estado tem de pôr no envelope 256
   bits aleatórios por turno; o adaptador do gateway fá-lo e é, no nó, o único que produz
   estado. Um cliente novo sem nonce reabria o oráculo do §2.5 sem o runtime o notar.

### 2.7 O tecto, e o que acontece acima dele

O envelope de um turno tem um **tecto de bytes**, configurado no nó
(`AOS_MODEL_PROVIDER_STATE_MAX_BYTES`, de 1024 a 98304; por omissão 65536) e aplicado por quem
faz o pedido. O runtime aplica ainda o tecto absoluto de 98304 bytes (96 KiB) a qualquer cliente.

**Acima do tecto o estado não é truncado** — um bloco assinado cortado é inválido. Ou cabe
inteiro, ou **não é guardado**: o turno leva a marca `nao_devolvivel` (na captura e na ficha),
sem um byte e sem digest, a causa conta em `aos_model_provider_state_total`
(`nao_devolvivel_tecto`, `nao_devolvivel_nonce` ou `nao_devolvivel_desalinhado`), e o run segue
como um turno sem estado.

**Porque o máximo é 96 KiB — medido, e não estimado** (revisão do ticket, achado A1). A captura
é um evento do Event Store, e o NATS de produção limita a mensagem a 1 MiB. Entre o envelope e
o evento há **três** passagens por base64 (o envelope dentro do conteúdo do turno; o conteúdo
cifrado dentro do envelope de cifra; esse dentro do payload do evento), cerca de 2,37 vezes; e
**o raciocínio fica duas vezes na captura**, porque o primeiro campo de raciocínio com conteúdo
também vai para o `reasoning` de sempre (ADR-036 §2.7). Com o cifrador real do nó e o mesmo
raciocínio ao lado do estado (`TestAOS514_No_Tecto_OEventoSeladoCabeNoTransporte`):

| Envelope | Evento selado | Do limite de 1 MiB |
|---|---|---|
| 65 536 (a omissão) | 284 328 bytes | 27% |
| 98 304 (o máximo) | 426 316 bytes | 41% |
| 262 144 (o máximo da primeira versão) | 1 140 565 bytes (medido pela revisão) | **acima** — a captura falhava, e com ela o run |

O tecto conta só o envelope. O `reasoning` de sempre **não tem tecto próprio** (é limitado pelo
corpo da resposta, 1 MiB) e não é limitado por este: um raciocínio muito maior do que o estado,
ou com muitos caracteres que o JSON escapa, já podia não caber antes deste ADR.

### 2.8 Quem o pode ler; o replay; a retoma; o apagamento

- **Quem lê.** O conteúdo de uma captura selada só se abre atrás do gate soberano do replay
  (o escopo de leitura de conteúdo, ADR-011): o motor de replay e a retoma do próprio nó. A API
  do nó não expõe o estado em nenhuma rota.
- **Replay.** O motor reconstrói o rótulo a partir do estado que leu da captura — o mesmo
  digest que o loop pôs no tail. Um run com estado reproduz sem divergir; uma captura anterior
  a este ADR, sem os campos, reproduz como antes. Um estado trocado na captura diverge no
  `prompt_hash` do turno seguinte — **num turno não-final de um run em 1.5.0**, que é onde o
  compromisso do §2.5 existe. O estado do turno que acaba o run, e o de turnos de um run em
  1.4.0 retomado com a captura ligada, ficam guardados sem rótulo e sem compromisso: não há
  turno seguinte que os refira.
- **Retoma.** A retoma reidrata o estado da captura igual ao gravado, byte a byte.
- **Apagamento do titular.** O estado está dentro do envelope cifrado do turno. Destruída a
  chave do titular, **não há estado** — nem turno: a leitura falha fechada com a causa do
  apagamento, e a retoma não continua sem ele. Não há causa separada para o estado, porque ele
  não existe fora do conteúdo do turno.
- **O que sobrevive ao apagamento** é o que já sobrevivia: tokens, custo, motivo de paragem — e,
  se a medição da forma estava ligada, que o turno trouxe estado e com que tamanho.

### 2.9 O que fica para o AOS-515

Este ADR **não devolve nada**: o pedido continua a sair pela serialização de sempre, que retira
o raciocínio de todas as mensagens e não conhece o estado. Ficam decididas aqui as regras que a
devolução terá de cumprir, e por decidir a devolução em si:

- o estado só pode ser devolvido **à rota e ao modelo que o produziram** (os do envelope);
- devolve-se **sem alterar um byte**, ou não se devolve;
- um turno cujo estado é `nao_devolvivel`, é só referência (modo sensível), ou foi apagado,
  **não tem estado para devolver**;
- a devolução entra por uma versão nova da projecção e por emenda ao ADR-036 §2.4, e só para
  rotas cujo perfil o exija (AOS-513).

### 2.10 Quando se pode ligar `capture`

Com `off` este ADR não muda um byte, e a decisão do layout 1.5.0 é **reversível**. A partir do
primeiro run gravado em 1.5.0 deixa de o ser: o assembler, o replay e a projecção têm de saber
montá-la para sempre. Por isso `capture` **não se liga em produção** antes de duas coisas: (1) o desenho do AOS-515 confirmar que USA o rótulo — junção por digest com verificação (`sha256` dos bytes igual ao rótulo), e «sem estado» em caso de desacordo; (2) o smoke sobre JetStream com um turno com estado **no tecto** passar (§2.7). Se o
AOS-515 acabar por juntar o estado ao turno pelo passo, e não pelo digest, o rótulo é peso
morto permanente, e é mais barato retirá-lo **antes** do primeiro run em 1.5.0.

**`capture` com a projecção em texto único não é suportado em produção**: nessa projecção o
rótulo vai no prompt e o preâmbulo de protocolo não o explica ao modelo; o efeito não está
medido. O nó não recusa a combinação — o texto único é o recuo da projecção —, mas o arranque
avisa.

**A ordem**, com um ou mais nós (runbook em `deploy/server/README.md`):

- *Ligar:* a imagem nova em **todos** os nós; só depois `capture`. Com `capture` num nó e a
  imagem antiga noutro, o run em 1.5.0 que o segundo tente retomar fica órfão (falha fechado:
  o layout é desconhecido para ele).
- *Recuar:* `off` e recriar; esperar que não haja runs em 1.5.0 em `running` nem à espera de
  aprovação; só então a imagem anterior — aceitando que os runs em 1.5.0 já terminados deixam
  de ser reproduzíveis por ela.

### 2.11 A devolução (AOS-515)

**A junção é pelo rótulo do tail, com verificação.** É a condição (1) do §2.10, e fica
confirmada: o AOS-515 **usa** o rótulo. O loop entrega a quem faz o pedido os envelopes dos
turnos anteriores, pela chave do digest; a projecção lê o `state_digest` do primeiro segmento de
cada turno, procura os bytes com essa chave, e só os usa se o `sha256` **deles** for o rótulo. A
chave do mapa não é de confiança; o rótulo é — só o runtime o escreve, e é com ele que o
`prompt_hash` se comprometeu. Em caso de desacordo, de bytes em falta (modo sensível) ou de
envelope ilegível, **o turno vai sem estado**. O estado não se junta ao turno pelo passo nem
pela posição.

**São precisas três coisas, todas.** O estado de um turno só sai num pedido com (a) a projecção
nativa **1.3.0** (ADR-036 §2.4), (b) um perfil de rota com `devolver` diferente de `nunca`
(ADR-036 §2.8), e (c) o envelope a dizer que foi **essa rota** que o produziu: o digest do perfil
e o modelo servido que o envelope gravou são os da rota a que o pedido vai, **e o turno que o
produziu teve a rota comparada como `igual`**. Esta última condição lê-se do `route_check` que o
envelope gravou, e não se recalcula: em `observe` um turno com o endpoint diferente, com o
endpoint por reportar, ou com o nome do modelo só igual depois de saneado segue — e deixava um
envelope com o digest e o nome certos (achado F1 da revisão). Esse estado não se devolve. A decisão (b) e (c)
toma-se no gateway **depois do roteamento**: num failover, a segunda rota não recebe um byte do
que a primeira produziu. Como o envelope só leva o digest do perfil com a governação da rota
ligada, **a devolução exige `AOS_MODEL_ROUTE_GOVERNANCE` em `observe` ou `enforce`**.

**`obrigatorio` falha fechado.** Numa rota que exige o estado, um turno com tool calls sem estado
devolvível faz o pedido **não sair**: o gateway devolve um erro com a causa em vocabulário fechado
e o run falha de forma atribuível. Nunca se envia sem o estado à espera de que o provider
aceite — ou de que desligue o raciocínio em silêncio. Numa rota `opcional` o pedido segue sem o
estado desse turno, e conta-se.

| Causa | Quando |
|---|---|
| `estado_ausente` | O tail não refere estado para o turno: o provider não o mandou, ou ficou «não devolvível» (tecto, nonce, desalinhado) |
| `estado_so_referencia` | Há rótulo e não há bytes (captura em modo sensível) |
| `estado_digest_diferente` | O `sha256` dos bytes não é o rótulo |
| `estado_ilegivel` | Os bytes conferem e não são um envelope |
| `estado_desalinhado` | O envelope tem outro número de tool calls (o turno escalou a meio) |
| `projeccao_sem_estado` | O pedido não vem da projecção 1.3.0 |
| `estado_sem_rota` | O envelope não diz de que rota é (governação da rota desligada) |
| `estado_de_rota_nao_provada` | O turno que produziu o estado não teve a rota comparada como `igual` |
| `estado_de_outra_rota` | Outro perfil, ou outro modelo servido |
| `id_do_provider_inutilizavel` | A rota pede os ids do provider e os do turno não servem |

**O que sai, e onde.** Cada campo volta ao **sítio** de onde veio, com o **nome** com que veio e
os **bytes** que vieram, copiados para o corpo do pedido sem passar por nenhum codificador: os
campos de raciocínio na mensagem `assistant` do turno (`reasoning_content`, `thinking_blocks`, …),
os que o proxy entregou em `provider_specific_fields` dentro de um objecto com esse nome, e as
assinaturas por chamada na tool call ou na sua `function`. Não se filtra por tipo: os blocos
redigidos e os de texto vazio vão. Os nomes são os das listas fechadas com que a sonda leu o
estado: um envelope não escreve `content`, `role` nem `tool_calls`.

**O id de tool call.** Por omissão continua a ir o id do runtime. Um perfil pode declarar
`tool_call_id: provider`: nos turnos cujo estado é devolvido, o `id` da tool call e o
`tool_call_id` da mensagem `tool` passam a ser o id que o provider deu — o valor descodificado e
já limitado (§2.6), e só se todos os do turno forem utilizáveis, diferentes entre si e de todos
os ids já usados no pedido, e nenhum tiver a forma de um id do runtime (`<passo>-tool-<n>`): um
turno posterior que fosse com os ids do runtime podia repeti-lo. O id do provider continua a não ser identidade de nada no runtime.
Resíduo: com o id do provider no wire, o rótulo `id=` do cabeçalho da mensagem `tool` e o `ref`
de um aviso continuam a ser os do runtime.

**Estável por prefixo.** O pedido do turno N+1 é o do turno N com mensagens acrescentadas no fim.
O tail é append-only; cada turno abre uma mensagem `assistant` nova; a omissão de argumentos por
tamanho decide-se quando o segmento é criado; o aviso de nova tentativa está na semente; e a
decisão de devolução de um turno só depende desse turno e dos anteriores. Preso por teste com um
provider falso que recusa um pedido cujo anterior não seja prefixo exacto.

**O que não muda (D3).** O estado nunca é texto: não entra no `content` de nenhuma mensagem, no
tail, em `turn.recorded`, em spans nem em métricas. Um run com `content` vazio e estado continua
a fechar `empty_output`. O campo `reasoning_content` de sempre continua a ser retirado de todos
os pedidos; o raciocínio só volta a um provider como carga opaca do estado. Quando o pedido
levou estado, o corpo de um erro 4xx do provider **não sobe** na mensagem de erro (o provider
pode ecoar o que recebeu).

**Modo sensível.** A captura em modo sensível guarda só a referência do estado. Para a devolução
o estado **não existe**, ao vivo como na retoma: o capturer diz ao loop que não guarda os bytes, e
o loop não os entrega a quem faz o pedido (achado F3 da revisão — antes, o run devolvia o estado
enquanto corria e deixava de o devolver depois de retomado). Uma rota `obrigatorio` falha fechado
nesse modo, com `estado_so_referencia`.

**Mudar um perfil `obrigatorio`.** Qualquer alteração ao perfil muda o seu digest, e os estados
já capturados deixam de ser «desta rota»: os runs em curso nessa rota falham no turno seguinte
(`estado_de_outra_rota`). É por desenho. A mudança faz-se com a rota drenada — sem runs em
`running` nem à espera de aprovação —, como o recuo da projecção.

**Limite: o fallback dentro do proxy.** Se o próprio proxy reencaminhar um pedido para outro
deployment, o estado que o pedido leva chega a esse deployment antes de o gateway saber quem
serviu. Detecta-se na resposta, pelo `route_check` desse turno; e o estado que esse turno
produzir já não é devolvido (não é `igual`). Rotas `obrigatorio` configuram-se no proxy sem
fallbacks.

**Por medir (AOS-516).** Um fornecedor que numere os ids de tool call por índice repete-os entre
turnos; com `tool_call_id: provider` o segundo turno com um id já usado não leva estado
(`id_do_provider_inutilizavel`), e numa rota `obrigatorio` o run falha.

**Replay e retoma.** A retoma reproduz os turnos já dados pela captura, que devolve o estado
igual; o loop junta-os pelo digest, e o pedido do primeiro turno ao vivo é o que teria sido. O
nonce do envelope não vai no pedido, pelo que os pedidos de um run se reconstroem byte a byte dos
segmentos do tail e dos envelopes capturados.

**Medido atrás da imagem de produção do proxy (2026-10-08, `ci-wire-live`).** Numa rota
`openai/…`, o `reasoning_content`, os `thinking_blocks` (assinados, redigidos e de texto vazio),
o `provider_specific_fields` e a `thought_signature` da tool call chegam ao provider. Numa rota
`anthropic/…`, o proxy traduz os `thinking_blocks` da mensagem `assistant` em blocos `thinking` e
`redacted_thinking` do wire da Anthropic, com o texto e a assinatura; e **acrescenta um bloco de
texto** («Empty message content sanitised…») quando o `content` do `assistant` é a string vazia.
O proxy re-serializa o JSON: a igualdade byte a byte vale até ao proxy, e daí em diante vale a
igualdade dos valores. O que o fornecedor real aceita só o AOS-516 mede.

**Emenda de 2026-10-10 (AOS-516, decisão do dono) — o perfil da rota diz onde o estado volta.**
A regra do §2.9, «cada campo volta ao sítio de onde veio», passa a ser a **omissão**, e deixa de
ser a única forma. Medido atrás da imagem fixada do proxy, numa rota de um agregador
(`openrouter/…`): o proxy entrega `reasoning_details` dentro de
`message.provider_specific_fields` — o saco onde põe os campos que não conhece —, reenvia esse
saco ao fornecedor tal e qual, e o fornecedor só lê `reasoning_details` no topo da mensagem. O
estado «voltava» em 10 de 10 turnos e não era lido em nenhum.

O perfil da rota ganha o campo `devolver_em`, de vocabulário fechado: `origem` (a omissão: o
§2.9, sem mudar um byte) ou `topo`. Com `topo`, os campos de raciocínio das listas fechadas que
vieram em `message.provider_specific_fields` voltam como chaves da própria mensagem `assistant`,
com o nome e os **bytes** com que vieram, e o saco **não volta**: o fornecedor nunca o mandou, e
a medição mostra que, enviado, lhe chegaria em duplicado. Um campo do saco com o nome de um que
já está no topo não se escreve (uma mensagem não leva duas chaves iguais). Tudo o resto do §2.11
vale igual: a junção pelo rótulo com `sha256` conferido, as três condições, a rota provada, o
fail-closed de `obrigatorio`. `topo` só se declara numa rota que devolve estado; só entra no
digest do perfil quando declarado; e é o gateway que o escreve em cada pedido — o que um chamador
ponha no estado é sobreposto. Contrato da porta `1.10.0` (`MessageState.Placement`). Não há
nome de fornecedor no código: um modelo novo, servido por outro agregador com a mesma forma,
entra pelo perfil.

## 3. Alternativas

1. **Um segmento novo no tail com os bytes do estado** (a forma «d1» do desenho). Rejeitada:
   engrossa todos os prompts, põe conteúdo do provider no texto canónico (contra a D3), e
   obriga a projecção de texto único a escondê-lo.
2. **Guardar só o raciocínio escolhido, como hoje, e o id à parte.** Rejeitada: perde os outros
   campos e os blocos do saco do proxy, que são os que o fornecedor da D4 exige.
3. **Re-serializar o estado numa forma canónica.** Rejeitada: invalida as assinaturas (§2.2).
4. **Truncar acima do tecto.** Rejeitada: uma assinatura truncada é inválida, e um estado
   parcial devolvido como completo é pior do que nenhum.
5. **Um digest sem nonce**, no molde do `args_digest`. Rejeitada: o digest vai para o tail e daí
   para um provider que pode não ser o que produziu o estado; sobre conteúdo curto é um oráculo.
   O AOS-507 resolveu o mesmo problema no `shape_digest` deixando o conteúdo de fora do hash;
   aqui o hash tem de cobrir o conteúdo, e por isso leva um nonce.
6. **Um evento próprio para o estado.** Rejeitada: o estado é conteúdo do turno, e a captura já
   tem a cifra por titular, o modo sensível, o apagamento e a leitura atrás do gate. Um evento
   novo obrigava a repetir os quatro. Não há evento novo nem stream novo.
7. **Usar o id do provider como id da tool call do runtime.** Rejeitada: é escolhido pelo
   provider ou pelo modelo, pode repetir-se e pode ser hostil (§2.6).

## 4. Consequências

- **Positivas.** O runtime passa a ter, intacto, o que a segunda família exige de volta. O
  `prompt_hash` passa a cobrir o estado. Com a captura desligada nada muda: pedidos,
  `turn.recorded`, capturas, `prompt_hash` e `/metrics` são byte a byte os de antes.
- **Custos.** A captura de um turno com estado cresce: o envelope pesa no evento selado cerca
  de 2,37 vezes o seu tamanho, e **o raciocínio fica duas vezes na captura** — no `reasoning`
  de sempre e dentro do estado —, o que dobra o custo de armazenamento de um turno com
  raciocínio (§2.7). Um layout novo (1.5.0) entra no vocabulário do assembler, do replay e da
  projecção, e fica lá para sempre a partir do primeiro run (§2.10).
- **Recuo.** Voltar a `off`. Os runs começados em 1.5.0 continuam nela e reproduzem-se. **Um
  binário anterior a este ADR não conhece a 1.5.0**: não se recua de imagem com runs em 1.5.0
  por acabar ou por auditar. A ordem está no §2.10. Um run em 1.5.0 hospedado por um nó em
  `off` tem série em `aos_runs_hosted_total` assim que é hospedado.

## 5. Resíduos, riscos aceites e limites

1. **Só o caminho síncrono, e só o nó.** O streaming não captura estado (os runs não o usam), e
   as chamadas do `aos-orq` ao modelo também não.
2. **Em texto único o digest vai no prompt.** É um digest com nonce, mas é texto que o modelo
   vê e que o preâmbulo de protocolo não explica. A projecção nativa não o envia. A combinação
   `capture` + texto único não é suportada em produção, e o arranque avisa (§2.10).
3. **A ligação do rótulo ao turno é posicional.** O rótulo vai no primeiro segmento do turno;
   quem vier a devolver o estado (AOS-515) localiza-o por esse segmento e pelo passo.
4. **Em modo sensível o replay de um run não é fiel de qualquer forma** (os argumentos das
   tools são referências); o estado segue a mesma regra e não a agrava.
5. **O que o fornecedor realmente exige não está medido** (os «por confirmar» do ticket). Este
   ADR guarda tudo o que a documentação diz poder ser exigido; o AOS-512 mede.
6. **Não há tecto por run**, só por turno. Um run de muitos turnos com estado grande ocupa o
   Event Store na proporção; o orçamento em tokens do run já limita quantos turnos há.
7. **Com estado, o `prompt_hash` dos turnos a partir do segundo deixa de ser reprodutível
   entre duas execuções do mesmo run.** O nonce é novo em cada captura, o digest muda, e o
   rótulo entra no tail. A propriedade «o mesmo run re-executado dá os mesmos `prompt_hash`»
   só se mantém para o **primeiro** turno. O que isto **não** parte: (a) o **replay** e a
   retoma, que relêem a captura e reconstroem o mesmo digest; (b) a **medição das tentativas**
   (ADR-039), que compara só o prompt do turno 1, e esse nunca leva rótulo — o hash do turno 1
   é o mesmo em 1.4.0, em 1.5.0 sem estado e em 1.5.0 com estado; (c) a **estabilidade da
   cache** (ADR-009): o prefixo não muda e o rótulo é escrito quando o segmento entra e nunca
   mais muda, e em projecção nativa nem vai no pedido. Não há hoje consumidor que compare o
   `prompt_hash` de turnos seguintes entre runs; um que apareça tem de saber disto.
8. **Ninguém lê o rótulo neste ticket.** É escrita sem leitor até ao AOS-515 (§2.10).

## 6. Emendas a outros ADR

- **ADR-036 §2.8** (o que o perfil da rota declara): acrescentam-se `devolver_em` (§2.11 deste
  ADR, emenda de 2026-10-10) e o parâmetro `reasoning`. A emenda vive no próprio ADR-036.

- **ADR-036 §2.7** («O raciocínio do modelo é carga opaca»). A frase «Os blocos de raciocínio
  assinados e os itens cifrados de outros fornecedores ficam fora desta decisão» deixa de valer:
  passam a ser capturados, por este ADR. A frase «Sem tecto próprio» continua a valer para o
  campo `reasoning` de sempre, que não muda; o estado opaco tem o tecto do §2.7 deste ADR. O
  resto do §2.7 mantém-se: nada é devolvido ao provider, e o raciocínio não entra no texto do
  tail, em spans nem em eventos em claro.
- **ADR-036 §2.3** (o layout é versionado e fixa-se por run): acrescenta-se a versão 1.5.0, com
  a regra do §2.5 deste ADR.

## 7. O que cada ticket implementa

| Ticket | O quê |
|---|---|
| AOS-514 | A sonda do estado e o envelope na porta do gateway (contrato 1.7.0); a captura selada, o layout 1.5.0 e o rótulo `state_digest` no kernel; o replay e a retoma; o interruptor, o tecto e a métrica no nó. Nada é devolvido |
| AOS-515 | A devolução do estado ao provider (§2.11): a projecção nativa 1.3.0, a decisão por rota no gateway depois do roteamento, e a serialização que copia os bytes (contrato da porta 1.9.0). Inerte com os perfis de hoje |
