# ADR-039 — A recuperação é uma nova tentativa do nó do plano, autorizada pelo nó

- **Estado:** Aceite
- **Data:** 2026-10-06
- **Deciders:** Dono do produto (decisões de 2026-10-06: o sistema tenta outra vez sozinho um
  passo em que o modelo não usou a ferramenta; até duas tentativas a mais, três runs no máximo
  por nó do plano; aplica-se a todos os nós não-verificadores com tools, com ou sem `consumes`;
  as tentativas contam no orçamento de quem pediu, com um tecto de tentativas a mais por plano;
  um sucesso à segunda ou à terceira aparece como sucesso normal, com a contagem registada; não
  há «aviso e mais um turno» nesta fase; a medição directa ao modelo não está autorizada) ·
  executor de AOS-502 e AOS-503
- **Tickets:** AOS-510 e AOS-511 (emenda de 2026-10-07 aos §2.3, §2.5, §2.6, §2.7, §2.8 e §5: a
  recuperação passa a ter **duas classes de causa**, cada uma com a sua prova e o seu
  interruptor — o run que não chamou a tool, e o run sem tools que respondeu vazio; decisão D2 do
  dono de 2026-10-07, «primeiro só a contar»); AOS-506 (emenda de 2026-10-07 aos §2.1, §2.7 e §2.11: a tentativa pode levar um
  aviso constante do runtime, desligado por omissão); AOS-502 (o nó `aos` aceita a tentativa e
  prova, no seu log, que a anterior não pediu tools) e AOS-503 (o `aos-orq` grava o facto e volta
  a submeter o nó do plano), implementados; por rever de forma independente e por verificar em produção. As duas metades
  nascem desligadas (`AOS_RUN_RETRY_MAX` a zero no nó; `AOS_ORQ_NOVA_TENTATIVA=off` no `aos-orq`)
  e só ligam por decisão do dono.
- **Relacionados:** ADR-001 (execução durável ao nível do passo; a chave de idempotência é
  `f(run_id, step_id)`), ADR-002 (Reference Monitor), ADR-018 (o nó não conhece o documento do
  plano), ADR-022 (extensões ao grafo de plano), ADR-023 (posse do run do plano), ADR-027 (cada nó
  do plano é um run do nó), ADR-030 (não-oracularidade das rotas do nó), ADR-034 (autorização
  derivada do contexto), ADR-035 (o submissor do plano é derivado pelo nó), ADR-037 (o desfecho
  de um run é um veredicto do kernel), ADR-038 (a saída por referência)

> **Resumo, depois da emenda de 2026-10-07 (AOS-510, AOS-511).** A recuperação é uma nova
> tentativa do nó do plano, como um run novo, e quem a autoriza é o nó `aos`, com uma prova lida
> do seu log. Tem **duas classes de causa**, que nunca se confundem: (1) o run que fechou
> `contract_unmet_no_call` sem pedir tool nenhuma (AOS-502/503); (2) o run que fechou
> `empty_output` sem pedir tool nenhuma, **sem contrato de tools e sem origem vinculativa da
> saída** (AOS-510/511). Cada classe tem a sua prova (§2.3) e os seus interruptores, desligados
> por omissão; os tectos são os mesmos e partilhados (§2.4). O pedido não escolhe a classe:
> decide-a o nó, pela razão do veredicto que leu.
>
> **A prova do nó e a elegibilidade do `aos-orq` não são a mesma condição.** A prova da segunda
> classe exige «sem contrato de tools», e não «sem tools oferecidas»: um run com tools na
> lista-branca, sem contrato, que respondeu vazio sem pedir nenhuma é admitido pelo nó — é seguro,
> porque as três fontes dizem zero tool calls. «Nó do plano **sem** tools» (e sem `from_tool`) é a
> elegibilidade do `aos-orq` (§2.5), que é mais estreita e serve para não pedir o que não faz
> sentido pedir. Por isso o nó admite **cadeias de classe mista** que o `aos-orq` nunca produz
> (uma tentativa com contrato sobre uma anterior sem ele, e o inverso): cada elo prova-se na
> classe da razão dele, o tecto de três runs é um só, e o aviso entra só no elo da primeira
> classe.

## 1. Contexto

Medido em produção de 2026-10-04 a 2026-10-06 (v0.1.45 a v0.1.49): em **12 de 74 planos (16%)**
o nó de leitura terminou num turno sem chamar a tool. Desde o ADR-037 o run fecha `failed` com
`outcome_reason=contract_unmet_no_call`, o nó do plano fecha `failed`, e o plano sai com o código
13. É honesto, e deixa cerca de um plano em seis por cumprir.

O pedido não determina o desfecho: o mesmo `prompt_hash` do turno 1 dá os dois (um pedido
observado sete vezes teve cinco chamadas e duas falhas; outro falhou e, 33 segundos depois,
chamou). Sob independência entre tentativas, uma tentativa a mais deixa um residual de 2,6%
(0,75% a 7,1%), acima do critério da fase; duas deixam 0,4% (0,07% a 1,9%). A amostra que
responde directamente a «repetir logo a seguir recupera?» é de dois casos, e os dois
recuperaram: os dados são compatíveis com a independência e não a provam.

Repetir era impossível. O id do run filho é `<plano>~<nó>`, e o nó `aos` recusava qualquer outra
forma em dois sítios (a regra da forma do ADR-035, e a conferência do `node_id` do AOS-477). E
repetir não é inócuo: um run novo tem chaves de idempotência novas (`f(run_id, step_id)`), pelo
que **repetiria um efeito que o run anterior tivesse aplicado**.

## 2. Decisão

### 2.1 A recuperação é um run novo do mesmo nó do plano

Quando o run de um nó do plano fecha `failed` por `contract_unmet_no_call` **sem ter pedido tool
nenhuma**, o `aos-orq` não fecha o nó do plano: volta a submeter o **mesmo pedido** como um run
novo — a tentativa seguinte. O nó do grafo fica `running` entre tentativas, e só a última decide
`complete` ou `failed`.

Não muda o kernel, a regra de terminação, o layout do prompt nem a projecção: cada tentativa é um
run como os outros, com o seu log, e reproduz-se sozinha byte a byte. O veredicto de nenhum run
muda — a tentativa que falhou fica `failed`, com a razão e o vector selados.

*Emenda de 2026-10-07 (AOS-506).* «Não muda o kernel» deixou de ser inteiramente verdade: o
kernel ganhou um campo no que quem compõe o run declara e um segmento opcional na semente do tail
(§2.7). A regra de terminação, a versão do layout, a prova do §2.3 e o veredicto não mudam, e com
o interruptor desligado — a omissão — nada disto se vê.

### 2.2 A forma: `plan_request.attempt` e o id `<plano>~<nó>~<n>`

O `plan_request` do `POST /runs` ganha `attempt` (inteiro, `n ≥ 2`). O id do run da tentativa `n`
é `<plano>~<nó escapado>~<n>`, com `n` em decimal canónico. O nó compõe esse id a partir dos
campos do vínculo e compara por **igualdade**; o sufixo do id recebido nunca se interpreta, pelo
que `~02` e `~+2` não passam. `attempt` exige `plan_id` e `node_id`.

Sem `attempt` o nó faz o que fazia: um id com segundo `~` é recusado, e o id tem de ser o
`idDoRunFilho(plano, nó)`.

O `node_id` escapado nunca contém `~`, pelo que dentro de um pedido a decomposição é única: um
separador depois do pedido é a primeira tentativa; dois, uma tentativa. O limite entre pedidos
está no §5.

### 2.3 Quem autoriza é o nó, e prova-o no seu log

O `aos-orq` pede; o nó `aos` só hospeda a tentativa `n` depois de ler, **do seu próprio Event
Store e do seu WORM**, sobre a tentativa `n − 1`, tudo o que se segue. A falta de qualquer ponto
recusa.

| Facto | De onde se lê |
|---|---|
| O run existe, e foi hospedado por este nó com o vínculo verificado ao **mesmo pedido** e ao **mesmo nó** | `run.plan_origin`, escrito pelo nó (`nhi:aos-node/plan-origin`): stream da fila, `run_id` do pedido, `node_id`; a geração é anterior ou igual à do pedido |
| É a tentativa imediatamente anterior | para `n = 2`, a origem não tem `attempt`; para `n = 3`, tem `attempt = 2` e `retry_of` certo |
| Está `failed` | a última `run.state.transition` |
| Fechou por `contract_unmet_no_call`, com **zero tool calls pedidas** | o veredicto selado nessa transição: a razão e o total do vector |
| O stream não tem **nenhum** evento `tool.call.*` | `tool.call.mediated`, `.denied`, `.escalated`, `.outcome` — pela família, e não por uma lista |
| Tem **um só** turno, sem tool calls, que parou com o motivo `stop` | o único `turn.recorded`: `tool_calls_requested = 0`, `stop_reason = stop` |
| A residência dele é a região de quem pede | o selo de residência do run, no WORM |

**Nada disto vem do corpo do pedido.** Do corpo vêm o pedido de plano, a geração, o nó e o
número da tentativa; tudo o resto é lido de eventos que só o nó escreve. É a regra do ADR-035 (o
vínculo deriva-se, nunca se aceita do corpo) e a lição do AOS-408: um veredicto recalculado do
que o chamador fornece não é um gate. Um `aos-orq` com defeito, ou comprometido, que declare uma
tentativa sobre um run que usou tools é recusado.

**A razão do veredicto, sozinha, não chega.** `contract_unmet_no_call` diz que a tool *em falta*
do contrato nunca foi pedida. Um run cujo contrato exige duas tools, que chamou uma e parou,
fecha com essa mesma razão — e com um efeito aplicado. Por isso «zero tool calls pedidas» lê-se
de três fontes independentes: o total do vector selado, a ausência de eventos de mediação e o
contador do turno.

**A geração da reclamação pode ser posterior.** A prova é sobre o que o run anterior *fez*, que
não muda com quem o reclama. A geração do pedido da tentativa continua a ter de ser a última e
viva (ADR-035); a do run anterior pode ser anterior — é o caso de um `serve` que morreu e de
outro que retomou. Exigir a mesma geração tornava a recuperação impossível exactamente quando um
processo cai a meio.

**As recusas são uniformes.** Todas as causas da prova respondem a mesma 403 e o mesmo corpo de
todas as recusas da rota; a causa fica no log do nó e em `aos_runs_retry_refused_total{causa}`,
em vocabulário fechado. Um log que não se leu *agora* (substrato, WORM) responde 503 e não
hospeda. As admissões contam em `aos_runs_retry_admitted_total`, quando o run foi de facto
hospedado.

#### Emenda de 2026-10-07 (AOS-510): a segunda prova — o run que respondeu vazio

Medido em produção a 2026-10-07 (v0.1.51), com a recuperação acima ligada: **3 planos em 140
(2,1%)** saíram 13 por `empty_output`, sempre no nó de resumo, que não tem tools — um só turno,
`stop_reason=stop`, texto final vazio. A prova acima exige `contract_unmet_no_call`, que um run
sem contrato de conclusão nunca dá.

É uma **segunda classe de tentativa**, e não um alargamento da primeira. **O pedido não escolhe a
classe:** o `plan_request.attempt` é o mesmo, e nada do corpo diz porque se pede a tentativa. O
nó lê a razão do veredicto da tentativa anterior no seu log e aplica a prova dessa razão —
`contract_unmet_no_call` ⇒ a tabela acima, sem alteração; `empty_output` ⇒ a tabela abaixo, e só
com `AOS_RUN_RETRY_EMPTY=on`; qualquer outra ⇒ recusa. Com o interruptor desligado (a omissão),
um run `empty_output` é recusado como sempre, com a causa de sempre (`anterior_outra_razao`).

| Facto exigido sobre a tentativa `n − 1` | De onde se lê |
|---|---|
| O run existe, com o vínculo ao **mesmo pedido** e ao **mesmo nó**; é a tentativa imediatamente anterior; está `failed`; a residência é a de quem pede | como na primeira prova: `run.plan_origin`, a última `run.state.transition`, o selo de residência |
| Fechou por **`empty_output`** | o veredicto selado nessa transição |
| **Zero tool calls pedidas**, pelas três fontes | o total do vector selado; nenhum evento `tool.call.*` no stream; `tool_calls_requested = 0` no turno |
| **Um só** `turn.recorded`, com `stop_reason = stop` | o único turno do stream |
| **Sem contrato de tools** | o `completion` do manifesto do turno, gravado pelo kernel: `requires` vazio, e o vector selado sem linhas de tool |
| **Sem origem vinculativa da saída** | o mesmo `completion` (`output_binding` diferente de `binding`) e a âncora selada |

**Porque é que `empty_output`, sozinha, não chega.** O kernel fecha com essa razão em dois casos
(ADR-037, ADR-038). Sem origem vinculativa, é o texto do turno final que veio vazio: num run que
não pediu tool nenhuma, nada aconteceu, e nada se pode repetir. Com origem vinculativa, é a tool
designada que devolveu zero bytes — houve uma tool call. E um run que chamou uma tool e depois
respondeu vazio fecha com a mesma razão, com um efeito aplicado.

**O estado impossível recusa-se com causa própria.** Um run com contrato de tools, zero chamadas
e `empty_output` é um log que o kernel não escreve (o contrato tem precedência e fechava
`contract_unmet_no_call`). Se aparecer, o nó não o interpreta nem o repete: `estado_impossivel`.

**O tecto é um só** (§2.4): as tentativas das duas classes contam para o mesmo
`AOS_RUN_RETRY_MAX`. A prova decide-se por elo — cada tentativa prova a imediatamente anterior,
na classe da razão dela.

**As séries são próprias.** As recusas posteriores à leitura da razão contam em
`aos_runs_retry_empty_refused_total{causa}` e as admissões em
`aos_runs_retry_empty_admitted_total`; as séries da primeira classe não ganham rótulos nem mudam
de valor. As recusas anteriores a essa leitura (tecto, forma, origem, sequência, estado) não têm
classe, e contam onde sempre contaram. A resposta a quem pede é a mesma 403 uniforme.

**O `run.plan_origin`** de uma tentativa desta classe leva, além de `attempt` e `retry_of`, o
campo aditivo `retry_reason=empty_output`. O `GET /tools` anuncia a classe em
`run_retry.empty_output`, só com o interruptor ligado e o tecto acima de zero.

### 2.4 Os tectos

- **No nó:** `AOS_RUN_RETRY_MAX` ∈ {0, 1, 2}, a omissão é 0. Com 0 todo o pedido com `attempt` é
  recusado, o `GET /tools` não anuncia nada e o `/metrics` não ganha séries. `attempt` acima de
  `tecto + 1` é recusado. O tecto anuncia-se no `GET /tools` (`run_retry.max`), só acima de zero.
- **No `aos-orq`, por nó:** duas tentativas a mais. Vence o mais apertado entre este e o que o
  nó anuncia. Contra um nó que não anuncia não se pede tentativa nenhuma.
- **No `aos-orq`, por plano:** `AOS_ORQ_NOVA_TENTATIVA_MAX_POR_PLANO`, quatro por omissão. O
  contador é o número de factos do log do plano, pelo que uma retoma não o repõe.
- **O prazo:** uma tentativa não começa depois do prazo do `serve`.

### 2.5 O que o `aos-orq` exige antes de pedir

Lido só do vocabulário fechado que o nó devolve: o nó do plano não é verificador e tem tools
atribuídas (com ou sem `consumes`); a resposta é sobre o run da tentativa corrente; o run está
`failed` com a razão exactamente `contract_unmet_no_call`; e o vector diz zero tool calls pedidas.

Esta condição serve para **não pedir** o que o nó ia recusar. Não é ela que protege de repetir
um efeito: é a prova do §2.3. A decisão não lê texto nenhum do modelo — o nó nem devolve o texto
de um run que não concluiu.

*Emenda de 2026-10-07 (AOS-511): a elegibilidade da segunda classe.* Lida do mesmo vocabulário
fechado e da estrutura do plano: o nó do plano não é verificador, **não tem tools atribuídas**
(logo, não leva contrato de conclusão) e **não declara a origem de nenhuma saída** (`from_tool`);
a resposta é sobre o run da tentativa corrente; o run está `failed` com a razão exactamente
`empty_output`; e o vector diz zero tool calls pedidas. As duas elegibilidades são disjuntas —
uma exige tools, a outra exige não as ter. Interruptor próprio, `AOS_ORQ_NOVA_TENTATIVA_VAZIA`
(`off`, `observe`, `on`; a omissão é `off`), independente do `AOS_ORQ_NOVA_TENTATIVA`. **Os
tectos do §2.4 são partilhados:** duas tentativas a mais por nó e o mesmo tecto por plano, a
somar as das duas classes — um plano não ganha tentativas por ter as duas. Contra um nó que não
anuncia `run_retry.empty_output` não se pede esta tentativa (`nao_anunciado`).

### 2.6 O facto fica no log do plano antes do pedido

`plan.node_attempt_started{plan_id, node_id, attempt, retry_of, reason}`, com construtor
validado e um passo por (nó, tentativa): a primeira escrita é o facto, e uma retoma que o volte a
gravar não o duplica. Escreve-se **antes** do `POST /runs` da tentativa.

**A retoma lê primeiro.** A tentativa corrente de cada nó é a maior que o log regista. Um `serve`
que encontre um nó `running` com uma tentativa registada lê o estado do id dessa tentativa: se o
nó `aos` responde 404, o processo anterior morreu entre o facto e o pedido, e submete-a — uma
vez. Nunca reenvia às cegas. Os factos lêem-se com o interruptor em qualquer valor; o que `off` e
`observe` deixam de fazer é começar tentativas.

**Um run de tentativa que este processo não submeteu só se segue com a origem conferida.** O id
`<plano>~<nó>~<n>` não é reservado no nó (§5): existir um run com ele não diz que foi este pedido
a criá-lo. O `GET /runs/{id}` de um run que o nó hospedou como nova tentativa leva `plan_attempt`
— o pedido, a geração, o plano, o nó e a tentativa do `run.plan_origin` que só o nó escreve, e só
depois da prova do §2.3. Quem retoma confere-o contra o seu pedido, o seu plano, o seu nó e a
tentativa que o log regista, e exige que a geração não seja posterior à sua. Se o run não o traz
(um run terminal sem `plan_attempt`, ou em curso sem ele passados 30 s), ou o traz de outro
pedido, nó ou tentativa, **não o segue**: o nó do plano fecha `failed` com a causa do run
anterior e `tentativa_recusada=run_de_outra_origem`. O mesmo vale quando a re-submissão da
retoma responde 409. Um `POST` a que o nó respondeu 201 neste processo não precisa da conferência:
é o nó a dizer que hospedou o run agora.

**Um facto que o log não admite fecha o nó, e não o `serve`.** O `retry_of` é um id de run, com
o tecto de um id de run (1024 bytes: os 128 do `node_id`, triplicados no pior caso do escape, os
separadores, e o resto para o id do pedido). Se mesmo assim o construtor do facto o recusar, o
nó do plano fecha `failed` com `tentativa_recusada=facto_invalido` — a forma é determinista para
o plano, e um erro repetia-se em todas as gerações.

*Emenda de 2026-10-07 (AOS-511): o facto diz a classe.* O `reason` do
`plan.node_attempt_started` passa a ter dois valores, `contract_unmet_no_call` e `empty_output`;
o construtor recusa qualquer outro. É pela razão do facto — e não pelo interruptor que estiver
ligado — que quem retoma sabe de que classe a tentativa é: que interruptor a retoma, e em que
séries conta. A retoma e a conferência da origem (`plan_attempt`) são as mesmas das duas classes.

### 2.7 O pedido repetido é o mesmo

A tentativa sai do mesmo código que a primeira submissão: o corpo difere só no `run_id` e em
`plan_request.attempt`. O objectivo, as tools, o contrato de conclusão, a origem declarada e os
`inputs` (conteúdo e digests) são iguais byte a byte. A recuperação não escreve nada ao modelo:
não há instrução nova, nem autoridade nova, nem eco.

O nó mede-o depois: compara o `prompt_hash` do primeiro turno da tentativa com o do turno da
anterior, e conta as diferenças em `aos_runs_retry_prompt_hash_diferente_total`, que tem de ser
zero. É medição e alerta, e não condição de hospedagem — o hash só existe depois de o prompt
estar montado.

#### Emenda de 2026-10-07 (AOS-506): o pedido repetido é o mesmo, *mais* um aviso constante do runtime, quando ligado

**Porquê.** Na v0.1.50, 3 de 6 tentativas (projecção 1.0.0) e 8 de 25 (1.1.0) voltaram a falhar
como a anterior — acima do que a independência previa, e acima do gatilho escrito no §3. E a
causa das falhas é uma só: o modelo escreve a tool call como texto, em vez de a pedir pelo
mecanismo de function calling.

**O que muda.** Com `AOS_RUN_RETRY_NOTICE=on` no nó (a omissão é `off`), o run de uma tentativa
leva, na semente do tail e a seguir ao objectivo, um segmento `notice` com o rótulo
`about=previous_attempt` e um corpo de **texto constante**: a tentativa anterior acabou com uma
resposta sem nenhuma function call, pelo que nenhuma tool correu, e falhou por uma tool de que
dependia nunca ter sido chamada; esta é uma nova tentativa; uma tool só se pede por uma function
call; o runtime não lê um pedido de tool escrito como texto.

**«A recuperação não escreve nada ao modelo» passa a valer só com o interruptor desligado.** Com
ele ligado há uma instrução nova. Continua a não haver autoridade nova nem eco:

- **Quem o acrescenta é o nó, e só ele.** A decisão é a conjunção do interruptor com a prova do
  §2.3 que *este* pedido passou. O `POST /runs` não ganha campo nenhum — um corpo que traga um é
  recusado —, e o `aos-orq` continua a enviar o mesmo pedido: não o pede, não o recusa, não lhe
  escreve. Um run que não é uma tentativa admitida nunca o leva.
- **O texto é do kernel.** O nó declara um valor de vocabulário fechado (`no_function_call`), e o
  kernel escreve a constante. Um valor desconhecido recusa o run antes de qualquer efeito.
- **Nenhum byte do run anterior.** O que a tentativa anterior respondeu não é lido. Ecoá-lo era
  mostrar, num segmento trusted, a forma errada que se quer evitar, e pôr nesse segmento bytes
  cuja origem pode ser um documento lido.
- **É um `notice`, e não uma `correction`.** A `correction` é de um humano autenticado pelo canal
  de controlo. A autoridade do contexto (ADR-034) fica como estava: num run sem entradas o aviso
  vem depois do objectivo, que já era trusted; num run com entradas o contexto já era untrusted
  antes dele, e o join não o eleva.
- **Cada frase é verdadeira sempre que o aviso sai**, porque assenta nos factos da prova: zero
  tool calls pedidas, nenhum evento de mediação, e a razão selada `contract_unmet_no_call`.
  Conteúdo untrusted consegue *provocar* o aviso (num nó com entradas, levando o modelo a não
  chamar a tool); não lhe consegue escrever nada.

**A prova do §2.3 não muda.** Uma tentativa 3 é admitida sobre uma tentativa 2 com aviso pelos
mesmos sete factos.

**A medição passa a comparar com o hash esperado.** Com aviso, o prompt da tentativa difere do
anterior de propósito, e a comparação directa deixava de dizer alguma coisa. O que tem de
continuar verdadeiro é que a **única** diferença é o aviso. Um hash não se estende, pelo que o
nó recalcula o prompt do primeiro turno a partir da semente que o serviço **hospedou** nesta
tentativa — o Goal depois da ingestão, que minimiza o objectivo, e não o do pedido —, com o
layout e o tool set que ela gravou no manifesto: com o aviso da tentativa anterior (lido do
`run.plan_origin` dela) tem de dar o hash que a anterior gravou, e com o aviso desta tem de dar o
que esta gravou. Outro objectivo, outras entradas, outro system, outras tools ou um aviso com
outros bytes falham uma das duas igualdades; um recálculo que falhe conta como diferença.
`aos_runs_retry_prompt_hash_diferente_total` continua a ter de ser zero. Sem aviso em nenhuma das
duas tentativas, a comparação é a directa de sempre. `aos_runs_retry_notice_total` conta as
tentativas hospedadas com aviso.

A semente é a hospedada, e não a do pedido, porque as duas diferem sempre que o objectivo tem
dados que a ingestão redige (um e-mail, um telefone): a primeira redacção recalculava com o texto
do pedido e a série subia numa tentativa que só diferia pelo aviso (revisão do AOS-506, I-1). O
serviço mostra ao handler o Goal que entrega ao run; sem aviso em nenhuma das duas tentativas
nada se observa e nada do pedido fica em memória até ao fim do run.

**A retoma e o replay reproduzem.** O registo de retoma leva o valor declarado, e a semente do
replay também; a construção da semente é uma só, a do kernel. O motor de replay recusa um aviso
fora do vocabulário, ou num layout sem `notice`, com o erro do loop — não o lê como «sem aviso».

**Um binário anterior diverge em silêncio.** Um binário anterior ao AOS-506 que retome uma
tentativa com aviso não conhece o campo do registo de retoma: semeia o tail sem o aviso, reproduz
o turno 1 pela resposta gravada sem o voltar a comparar, e envia o turno seguinte ao modelo sem o
aviso. O run pode fechar `complete`, e nada alerta. O recuo faz-se por isso nesta ordem: a
variável primeiro, esperar que não haja tentativas com aviso em voo, e só depois a imagem. A
única forma de um binário antigo **recusar** em vez de divergir calado é a semente-com-aviso ser
uma versão de layout; a decisão e a condição em que passa a ser devida estão no ADR-036 §2.4.

#### Emenda de 2026-10-07 (AOS-510): a tentativa por resposta vazia NÃO leva aviso

Uma tentativa admitida pela segunda classe é hospedada **sem** segmento `notice`, mesmo com
`AOS_RUN_RETRY_NOTICE=on`: o pedido repetido é o mesmo, byte a byte, o `prompt_hash` do turno 1
é igual ao da tentativa anterior, e `aos_runs_retry_notice_total` não conta. Porquê:

- o texto do AOS-506 diz ao modelo como se pede uma tool; num nó sem tools não se aplica, e pode
  induzir uma tool call onde não há nenhuma;
- um texto próprio para o vazio teria de dizer ao modelo o que fez mal, e **a causa do vazio não
  é conhecida** (o AOS-507 mede a forma da resposta): seria afinado às cegas, e para um só
  modelo;
- sem aviso, a medição «`prompt_hash` igual entre tentativas» mantém a forma simples.

**Gatilho para reabrir:** com o AOS-511 em `on`, três ou mais das primeiras dez tentativas por
vazio voltam a fechar `empty_output`, **e** o AOS-507 nomeia uma causa sobre a qual um texto
constante, de vocabulário fechado, possa actuar. A decisão é então do dono, num ticket novo.

### 2.8 Nunca há nova tentativa depois de uma tool call, de uma resposta cortada ou de outra razão

- **Uma tool call pedida** — efectiva, negada, falhada ou escalada — fecha a porta. Depois de
  uma efectiva repetia-se um efeito; depois de uma negada ou falhada empurrava-se o modelo a
  pedir outra vez o que lhe foi recusado.
- **Uma resposta truncada** fecha por `truncated` (que tem precedência no veredicto), e o nó
  exige ainda `stop_reason = stop` no turno: `length`, `content_filter`, `other` e o motivo não
  reportado não se repetem.
- **Qualquer outra razão** — `contract_unmet_after_denial`, `contract_unmet_after_tool_error`,
  `empty_output`, as da origem da saída, `timed_out`, um run que não concluiu, um run perdido —
  fecha o nó do plano como antes.
- **Um nó em observação do veredicto** (`AOS_COMPLETION_VERDICT=observe`) deixa o run
  `completed`: nunca é elegível.

*Emenda de 2026-10-07 (AOS-510).* `empty_output` sai da lista do que nunca se repete **só** sob a
segunda prova do §2.3, e só com os interruptores dessa classe ligados. Continua a nunca se
repetir: um `empty_output` depois de qualquer tool call pedida; um `empty_output` com origem
vinculativa da saída (a tool designada devolveu zero bytes); um `empty_output` com mais de um
turno, ou cujo turno não parou com `stop`; e, do lado do plano, o de um nó verificador, de um nó
com tools ou de um nó com `from_tool`.

### 2.9 O orçamento

Cada tentativa é um run como os outros no nó: passa pela admissão por turno, pelo tecto por run
e pela quota do principal (AOS-457), e conta para a quota do submissor do plano. A árvore de
orçamento do `aos-orq` **não** debita o consumo dos runs filhos (debita estimativas declaradas);
o que limita o custo das tentativas são os tectos do §2.4 e os travões do nó.

Um 429 na submissão de uma tentativa pára as tentativas: o nó do plano fecha `failed` com a causa
do run e `tentativa_recusada=quota`, e o plano sai 13 — nunca 8, que reabria o pedido na fila.

### 2.10 A saída por referência (ADR-038) funciona sobre a tentativa que teve êxito

O facto `plan.output_source_declared` é um por nó e não depende do run: vale para todas as
tentativas. A âncora, os bytes e o `run_id` conferem-se contra o run da tentativa **corrente**; o
`plan.payload_published` aponta para ele; e a reidratação, no arranque de um `serve`, só aceita
um payload por referência cujo run seja o da tentativa corrente segundo o log. O texto de uma
tentativa falhada nunca é publicado nem entregue — o `aos-orq` nem o recebe.

### 2.11 O que fica auditável de cada tentativa descartada

No nó: um run `failed` inteiro, com as transições, a razão e o vector selados, o `turn.recorded`
com o `prompt_hash` e o motivo de paragem, a captura cifrada por titular, a residência selada, e
o `run.plan_origin` — que a partir da segunda tentativa leva `attempt` e `retry_of` e, numa
tentativa hospedada com aviso, `retry_notice` (emenda de 2026-10-07, AOS-506). No plano: um
`plan.node_attempt_started` por tentativa. No desfecho: `tentativas=` e `recuperados=`, que
distinguem um 0 depois de recuperação de um 0 à primeira.

## 3. Alternativas

- **Reamostragem do turno dentro do run — rejeitada.** Dá a mesma recuperação estatística, e
  paga-se no núcleo determinista: a regra de terminação, a sequência do tail, o motor de replay,
  a captura e o registo de turnos de **todos** os runs. A tentativa descartada passava a ser um
  conceito novo no registo de turnos; aqui é um run inteiro. O ADR-037 continua a dizer «sem
  reparação», e continua a ser verdade no kernel.
- **Aviso e mais um turno — adiado, com gatilho.** É a única alternativa que muda o pedido, e por
  isso a única que actua se as falhas forem correlacionadas com o pedido ou se a rota for
  determinista. Custa um layout novo, uma versão da projecção, emendas aos ADR-034 e ADR-037, e a
  sua eficácia nunca foi medida. **Gatilho:** nos primeiros 100 planos com a recuperação ligada,
  seis ou mais das primeiras falhas voltam a falhar na tentativa seguinte; ou a entrada de uma
  rota determinista. *Emenda de 2026-10-07:* o gatilho foi atingido (11 de 31 tentativas voltaram
  a falhar em 100 planos). O dono decidiu um aviso na **nova tentativa** (§2.7, AOS-506), que é
  um run novo com a semente acrescentada — sem layout novo nem emenda ao ADR-034 ou ao ADR-037.
  «Mais um turno» no mesmo run continua adiado: era o que tocava na regra de terminação.
- **`tool_choice` forçado — adiado, com dependência.** É prevenção e não recuperação; o suporte
  no provider é incerto; e o proxy de produção descarta parâmetros não suportados, pelo que o
  envio podia não ter efeito sem o AOS saber. Depende da rota sob governação (AOS-505).
- **A prova feita pelo `aos-orq` — rejeitada.** Quem tem os factos selados é o nó; o `aos-orq`
  lê-os por uma rota. Uma afirmação do chamador sobre o que o run anterior fez não é prova.

## 4. Consequências

- **O nó `aos` muda no contrato do `POST /runs`:** um campo opcional, uma forma de id e uma
  leitura do próprio log antes de hospedar. Com o tecto a zero é o de antes.
- **Um run filho pode ter três streams.** Cada tentativa tem o seu stream, as suas chaves de
  idempotência, a sua partição do WORM e a sua residência.
- **O alarme de runs `failed` do nó sobe** com as tentativas descartadas. É verdade, e lê-se com
  as métricas de recuperados.
- **A recuperação pode esconder um modelo a degradar.** Por isso a taxa de **primeiras** falhas é
  uma métrica própria, com alerta acima de 30% numa janela de pelo menos 20 planos.
- **Compatibilidade nos dois sentidos.** Um `aos-orq` anterior nunca envia `attempt`; um nó
  anterior não anuncia, e o `aos-orq` novo não o envia.

## 5. Resíduos, riscos aceites e limites

- **Âmbito largo — risco aceite pelo dono.** O desenho recomendava começar só pelos nós sem
  `consumes`; o dono escolheu todos os nós com tools. Num nó com `consumes`, conteúdo untrusted
  do passo anterior pode levar o modelo a não chamar a tool e gastar as tentativas. O dano é
  limitado pelos tectos e pelo orçamento; a tentativa não dá autoridade nenhuma (corre com o
  contexto untrusted desde o turno 1, e uma tool call privilegiada é negada pelo gate de taint
  como na primeira); e não há efeito, porque só se repete um run que não pediu tools. As
  métricas separam esta classe (`com_consumes`).
- **A independência entre tentativas não está provada.** Se as falhas forem correlacionadas no
  tempo (um estado do provider), as tentativas imediatas falham juntas. A entrada em produção
  mede a recorrência; não há intervalo entre tentativas nesta fase.
- **Numa rota determinista a recuperação é zero.** Repetir o mesmo pedido só recupera com
  amostragem não determinista.
- **Só serve runs de plano.** Um `POST /runs` directo com contrato não recupera; quem o chama
  pode repetir.
- **Um nó sem tools não se repete.** O nó que recusa o próprio objectivo não tem sinal
  estrutural, e não se julga a forma do texto (AOS-504).
- **A recusa do próprio objectivo num nó COM tools é repetida — custo aceite.** Um modelo que
  responde «não consigo» num turno, sem chamar a tool, num nó com tools atribuídas fecha por
  `contract_unmet_no_call` com zero chamadas: é, para a regra, o mesmo caso do nó que se esqueceu
  de a chamar. O mesmo pedido volta a ser submetido, com o mesmo prompt, até duas vezes a mais.
  Se a recusa for estável, são dois runs de um turno gastos por nó; não há efeito (nenhuma tool
  foi pedida) nem autoridade nova. Distingui-los exigia julgar o texto, que é o que esta fase não
  faz.
- **O pior caso de custo, medido.** Um plano de 6 nós elegíveis que falham sempre sem chamar a
  tool, com os tectos por omissão: 10 `POST /runs` em vez de 6 — as 4 tentativas a mais do tecto
  por plano —, 4 factos no log, e os nós seguintes fecham com `tentativa_recusada=tecto_do_plano`.
  O tecto por plano é o que limita: sem ele seriam 18. O orçamento do plano **não** debita os
  runs filhos (§2.9): quem limita o custo de cada run é o nó.
- **A forma do id não é injectiva entre pedidos.** O id da tentativa `n` do nó `N` do pedido `P`
  é o da primeira tentativa do nó `"n"` de um pedido cujo id seja literalmente `P~N` (o
  `POST /plans` admite `~` no id). E não é preciso um pedido com `~`: quem tem credencial de
  submissão na mesma região cria um run com o id de uma tentativa por um `POST /runs` directo. A
  prova do nó não é afectada — a origem do run anterior tem de ser a do mesmo pedido e do mesmo
  nó, pelo que uma tentativa nunca é admitida sobre um run alheio. Do lado do `aos-orq`, a colisão
  tem dois caminhos, e os dois fecham o nó do plano `failed`: no caminho normal, o nó responde
  409 (com credencial forte e a mesma região) à submissão da tentativa, que conta como
  `recusada_pelo_no`; na retoma, o run que existe com o id só é seguido com a origem conferida
  (§2.6), e um run alheio conta como `run_de_outra_origem`. A primeira versão seguia-o na retoma
  sem conferir — a revisão adversarial reproduziu o nó consumidor a receber o texto de um run
  que o plano nunca pediu. O que sobra é uma negação de serviço dirigida: quem pré-criar os ids
  das tentativas de um plano desliga a recuperação desse nó, e precisa para isso de credencial de
  submissão na região e de conhecer o pedido e o nó. Não é classe nova: o `POST /runs` nunca
  reservou a forma `<plano>~<nó>`, e a primeira tentativa em retoma tem o mesmo limite.
- **A conferência da origem exige um nó que a declare.** Um nó `aos` anterior a esta correcção
  não devolve `plan_attempt`: um `aos-orq` novo que retome, contra ele, uma tentativa que não
  submeteu fecha o nó do plano `failed` com `run_de_outra_origem`. É a direcção segura, e só
  acontece se a imagem do nó for revertida com tentativas em voo.
- **A eficácia do aviso não está medida** (AOS-506). Entra desligado; liga-se depois de uma
  série, pelo critério do ticket. Numa rota determinista, é a única peça que muda o pedido.
- **Interpretar a tool call escrita como texto fica rejeitado** (decisão do dono de 2026-10-07):
  um documento lido pode conter esse mesmo texto.
- **A medição do `prompt_hash` vive na memória do nó.** Um reinício entre a admissão e o fim da
  tentativa perde a comparação desse run.
- **A validade do NHI não se lê à parte.** O prazo do `serve` fica abaixo dela por construção; um
  NHI expirado é uma 403 do nó e conta como `recusada_pelo_no`.
- **Um binário `aos-orq` anterior não segue as tentativas.** Medido com o binário da base sobre
  um plano que ficou com a tentativa 2 em voo, em duas topologias — um plano sem ramos, e
  leitor → verificador → nó condicional: o binário anterior não lê o facto
  `plan.node_attempt_started`, sonda o run da PRIMEIRA tentativa — que está `failed` —, fecha o
  nó do plano `failed` com a causa desse run e o plano sai terminal 13
  (`contract_unmet_no_call:1,entrada_por_cumprir:1`), sem publicar nada. A tentativa em voo fica
  órfã no nó `aos`: corre até ao fim, e ninguém a recolhe. A primeira redacção afirmava que num
  plano com ramos o `serve` anterior saía com erro, por a leitura das decisões de ramo falhar
  fechado no tipo desconhecido; era inferência do código, e a medição não a confirmou nessa
  topologia. **Não foi medida** a topologia em que uma decisão de ramo é lida DEPOIS do facto (um
  outro ramo do plano a concluir com a tentativa em voo): aí a inferência pode valer, e o `serve`
  sair com erro até o tecto de gerações fechar o pedido. Um plano com ramos e sem tentativas no
  log corre normalmente no binário anterior (medido: terminal 0). Em nenhum caso algo de uma
  tentativa é publicado. O rollback faz-se pelo interruptor, e não pela imagem, enquanto houver
  planos com tentativas em curso.
- **`observe` sobrestima o que `on` faria.** Conta «tentaria» pela elegibilidade do §2.5, e não
  aplica o anúncio do nó, os tectos por nó e por plano, nem o prazo do `serve`: aplicá-los
  escrevia `tentativa_recusada=` no desfecho, e `observe` deixava de ser, byte a byte, o `off`.
  A série de `observe` é um majorante das tentativas de `on`.
- **Um `serve` que aborta não larga a posse.** A retoma de um plano cujo `serve` saiu com erro
  espera pelo TTL da posse (ADR-023); a recuperação não muda isso.

- **O conteúdo untrusted pode gastar as tentativas da segunda classe** (emenda de 2026-10-07,
  AOS-510). O nó sem tools de um plano recebe quase sempre material de outro nó: conteúdo
  untrusted do passo anterior pode induzir a resposta vazia e gastar as tentativas. O dano é
  limitado pelo tecto por nó, pelo tecto por plano e pelo orçamento de quem pediu, e a tentativa
  nunca dá autoridade — corre com o mesmo contexto untrusted desde o turno 1, e uma tool call
  privilegiada é negada pelo gate de taint como na primeira. É o risco que o dono aceitou a
  2026-10-06 para a primeira classe, agora numa classe em que se materializa (o nó com tools e
  `consumes` é negado antes pelo gate de taint; o nó sem tools não tem o que lhe seja negado).
- **Reverter a imagem do `aos-orq` com uma tentativa por vazio registada contamina a medição**
  (medido na revisão de 2026-10-08). O binário anterior lê o facto sem interpretar a razão:
  volta a submeter a tentativa que encontra no log sem run enquanto o nó tiver
  `AOS_RUN_RETRY_EMPTY=on`, conta-a nas séries da primeira classe, e apaga do ficheiro de
  métricas as séries desta (não são do catálogo dele). Os desfechos dos planos ficam certos; o
  que se perde é a leitura da recorrência e da igualdade entre as admissões do nó e as
  tentativas do `aos-orq`. O recuo de imagem faz-se por esta ordem: os interruptores das duas
  pontas a `off`, os planos com tentativas em curso drenados, e só depois a imagem.
- **Repetir pode não recuperar** (AOS-511). Se o vazio for determinado pelo pedido, as tentativas
  queimam orçamento sem recuperar: a fase A1 mediu recorrências de 32% a 50%. Por isso a classe
  entra em `observe` antes de `on`, e a recorrência (`voltou_a_falhar` sobre o total da tentativa
  2) lê-se na série própria antes de se decidir mantê-la.
- **A causa do vazio não é tratada.** Esta classe trata o sintoma seja qual for a causa, e não
  interpreta texto nenhum. O raciocínio do modelo nunca é usado como resposta (decisão D3 do
  dono, AOS-509).
- **O smoke sobre JetStream não foi feito neste ticket.** A prova lê o stream pelas mesmas
  chamadas do AOS-502 (`Read`, a máquina de estados, o selo de residência), que já correm sobre
  JetStream em produção; mas a tentativa `<plano>~<nó>~2` de um run `empty_output` só foi
  exercitada sobre o substrato de ficheiro. Fica por fazer no cluster, antes de ligar.

## 6. O que cada ticket implementa

- **AOS-502 — o nó.** `plan_request.attempt`; a forma `<plano>~<nó>~<n>`; a prova do §2.3; o
  tecto `AOS_RUN_RETRY_MAX`; o anúncio no `GET /tools`; `attempt` e `retry_of` no
  `run.plan_origin`; o `plan_attempt` no `GET /runs/{id}` de uma tentativa hospedada (§2.6); as
  três séries `aos_runs_retry_*`.
- **AOS-506 — o aviso.** `AOS_RUN_RETRY_NOTICE` no nó; a decisão no ponto em que a prova passou;
  o valor de vocabulário fechado e o texto constante no kernel; `retry_notice` no
  `run.plan_origin`; a medição com o hash esperado; `aos_runs_retry_notice_total`.
- **AOS-503 — o `aos-orq`.** O interruptor `AOS_ORQ_NOVA_TENTATIVA` (`off`, `observe`, `on`); a
  elegibilidade do §2.5; o facto `plan.node_attempt_started` antes do pedido; a retoma do §2.6; o
  tecto por plano; a conferência da origem de uma tentativa que o processo não submeteu (§2.6);
  a entrega por referência sobre a tentativa que teve êxito; as métricas e o `detail` do desfecho.

- **AOS-510 — o nó, segunda classe.** `AOS_RUN_RETRY_EMPTY` (`off`, `on`); a classe decidida
  pela razão lida no log; a segunda prova do §2.3; `retry_reason` no `run.plan_origin`; o anúncio
  `run_retry.empty_output`; a tentativa sem aviso (§2.7); as séries
  `aos_runs_retry_empty_admitted_total` e `aos_runs_retry_empty_refused_total{causa}`.
- **AOS-511 — o `aos-orq`, segunda classe.** `AOS_ORQ_NOVA_TENTATIVA_VAZIA` (`off`, `observe`,
  `on`); a elegibilidade do §2.5; o facto com `reason=empty_output`; os tectos partilhados; a
  retoma pela classe do facto; as séries `aos_orq_consume_*_vazia_*` e
  `aos_orq_consume_primeiras_respostas_vazias_total`, separadas das do AOS-503.

## 7. Emendas a outros ADR

- **ADR-027 §2.1 e §2.4:** o id do run filho admite o sufixo de tentativa; `failed` deixa de
  fechar sempre o nó do plano.
- **ADR-035 §2.2 e §5:** a forma `<plano>~<nó>~<n>`, e o que o nó prova para `n ≥ 2`.
- **ADR-037 §5:** «sem reparação» continua verdadeiro no kernel; a recuperação vive no plano e
  não muda o veredicto de nenhum run.
