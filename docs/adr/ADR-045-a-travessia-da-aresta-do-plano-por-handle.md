# ADR-045 — A travessia da aresta do plano por handle: o consumidor resolve pela declaração selada

- **Estado:** Proposto (2026-10-25, AOS-553) — desligado por omissão, atrás do interruptor do
  AOS-556 (`AOS_CONTEUDO_POR_HANDLE` + `conteudo_por_handle`); nunca correu em produção
- **Data:** 2026-10-25
- **Decisores:** Arquitecto de Plataforma; dono do produto (ratificação)
- **Tickets:** AOS-553 (EPIC-19). Depende de AOS-551 (o mecanismo) e AOS-552 (a linhagem), e do
  AOS-556 (o interruptor e a política de fluxo)
- **Contexto técnico:** fase R2 do ADR-043 (§2.3, o armazém e a entrega ao nó seguinte)
- **Supersessões:** nenhuma. **Reabre e decide** a alternativa que o **ADR-038 §3** rejeitou («o nó
  consumidor resolve a referência»), que o mesmo parágrafo declarou ser «a direcção certa a prazo»
  e disse merecer **ADR próprio**. **Emenda** o ADR-038 §2.6 (o transporte: os bytes de uma saída
  por referência deixam de ser necessariamente conteúdo) e **reafirma sem alterar** o ADR-043 §2.3
  («sem deduplicação entre runs»)

> **Resumo.** Num run num layout por handle o kernel designa como origem da saída a **própria
> referência** do resultado, não o seu conteúdo. Entregá-la ao nó seguinte punha no `plan_input`
> dele um token que ele nunca abre. Este ADR decide **quem resolve e onde**: o nó **consumidor**
> resolve, do armazém do run **produtor**, na sua admissão — e **re-sela** o conteúdo no espaço do
> seu próprio run. A autoridade para o fazer **não é a referência do corpo do pedido**: é o pedido
> de plano que o nó provou no seu próprio log (ADR-035), mais a âncora que o kernel selou no run
> produtor. O caso **cross-titular** é recusado por nome, antes de qualquer decifra.

---

## 1. Contexto

### 1.1 O gatilho, que estava escrito

O ADR-038 §3 rejeitou «o nó consumidor resolve a referência» com uma frase que vale citar inteira:

> É a direcção certa a prazo: os bytes deixam de passar pelo `aos-orq` e o tecto do corpo do pedido
> deixa de contar. **Rejeitada agora** porque põe o nó a decifrar conteúdo de um titular por conta
> de uma referência escrita num corpo de pedido, dentro do caminho de admissão (*confused deputy*):
> cada verificação esquecida ali é conteúdo de um run a entrar noutro que o chamador depois lê.
> Merece ADR próprio e não é precisa para fechar o defeito medido. **Gatilhos para a reabrir:** o
> primeiro payload legítimo acima do tecto; a fase A5 ou A6 da arquitectura-alvo; a reabertura do
> DEF-806.

O terceiro gatilho disparou: o AOS-551 **é** a reabertura do DEF-806. Este é o ADR próprio.

### 1.2 O defeito, medido

Com o produtor do `tool_result` composto (AOS-551), num layout 1.8.0 o corpo do resultado de cada
tool call permitida é selado no espaço de handles e substituído pela referência nua, **dentro do
efeito do step-ledger**. A consequência na origem da saída (ADR-038) é mecânica e não se contorna:

- o step-ledger grava a referência, logo a âncora que o kernel sela tem `digest = sha256(token)` e
  `bytes = len(token)`;
- os três passos de conferência do `aos-orq` **conferem** — os dois lados hasheiam a mesma
  referência —, pelo que `origem_nao_confere` não dispara;
- e o que se entregaria ao nó seguinte é o token.

O nó consumidor **não o pode abrir**, por três razões independentes, todas verificadas em código:

| Barreira | Onde | O que impede |
|---|---|---|
| Caminho | `abrir` lê `pastaDoRun(runID)/<hex>` (`handle_store.go`) | A pasta é por run; o conteúdo do produtor está noutra |
| Dados associados | `ligacaoDoRun` = `"handle:" + sha256(run_id)`, prefixado ao conteúdo antes de cifrar e exigido byte a byte (`ErrHandleOutroRun`) | Um blob copiado para a pasta de outro run não abre |
| Evento | `OpenQuarantined` exige um `handle.created` **do stream do run que pede** que declare o par `(digest, sealed_subject)` | Sem declaração própria não há bytes |

O AOS-556 fechou isto com a causa nomeada `origem_por_handle` — «uma causa nomeada em vez de lixo
silencioso». Mas uma causa nomeada é um nó do plano que **falha sempre**: com o interruptor ligado,
nenhuma saída por referência de um run da 1.8.0 se entrega. Este ADR é o que a torna desnecessária.

### 1.3 O que as três barreiras medem, e o que não medem

Esta é a medição que decide o desenho, e foi feita com comandos próprios antes de haver código.

**As barreiras bloqueiam um RUN a pedir pela sua própria pasta. Não bloqueiam o NÓ.** O
`abrir(ctx, subject, runProdutor, digest)` funciona hoje tal como está, e `declaracoesDeHandles` lê
o stream de qualquer run (`es.Read(ctx, runID, 1)`). O mecanismo da travessia **já existe**; o que
falta é **a autoridade para o invocar**. É exactamente o *confused deputy* que o ADR-038 §3
nomeou — e é por isso que este ADR é sobre autoridade, não sobre criptografia.

**E o token não é autoridade.** `MintHandleToken` é `sha256("aos-handle/v1" ‖ run ‖ digest ‖
ordinal)` truncado a 8 bytes, **sem chave**: quem souber o trio computa o token, e o próprio kernel
o diz no sítio da constante («qualquer um pode escrever estes bytes, um documento lido inclusive»).
Pior: digests de conteúdo circulam em claro — a âncora do ADR-038 leva um, e o
`plan.output_source_declared` leva o do contrato. Portanto um token apresentado num corpo de pedido
é, no máximo, um **selector**. Qualquer desenho que o tratasse como prova estaria a repetir o modo
de falha que fez o AOS-551 cair quatro vezes em revisão: uma afirmação sobre segurança que não foi
seguida até quem fornece o valor.

---

## 2. Decisão

### 2.1 A autoridade é o pedido de plano verificado, no molde do ADR-035

O ADR-035 resolveu o mesmo género de problema para o submissor de um run filho, e a sua frase é a
regra aqui: **o corpo diz QUAL, o nó verifica SE**. A travessia só corre sob seis conferências, e
nenhum elo da cadeia vem do corpo sem ser confinado por um facto que o nó selou ou verificou:

| # | Conferência | Fonte do valor | Causa da recusa |
|---|---|---|---|
| 1 | O run é trabalho de um pedido de plano que o nó **provou** no `POST /runs` | o log da fila do próprio nó (ADR-035, AOS-439/520) | `travessia_sem_pedido_verificado` |
| 2 | O run produtor nomeado é `<pedido>~<from>`, ou uma tentativa dele | o pedido provado + o `from` que já era o rótulo de proveniência | `travessia_run_fora_do_plano` |
| 3 | A última transição desse run selou âncora **vinculativa** e **designada** | o log durável do nó (ADR-038 §2.3) | `travessia_sem_ancora_vinculativa` |
| 4 | Essa âncora designa **exactamente** esta referência (`sha256` e tamanho) | a âncora selada pelo kernel | `travessia_referencia_nao_designada` |
| 5 | O par `(run, digest)` consta de um `handle.created` do stream do produtor, **e o token re-cunha-se** de `(run, digest do evento, ordinal)` | o stream do produtor + `MintHandleToken` | `travessia_sem_declaracao_do_produtor` |
| 6 | O **titular** vem do evento (`sealed_subject`); outro titular é recusado **por nome** | o `handle.created` | `travessia_outro_titular` |

**O passo 4 é o que faz a travessia ser «pela declaração selada».** Sem ele, o corpo podia nomear
qualquer handle intermédio de um run irmão — um resultado de tool que o plano nunca publicou. Com
ele, o mais que o corpo alcança é a **saída designada** de um nó do plano.

### 2.1.1 O que a travessia acrescenta, e o que a confina

**A primeira versão deste ADR afirmava que a travessia não acrescenta poder nenhum, e estava
errada.** A frase era: «quem submete é o drenador que detém a reclamação viva do pedido, e esse já
lê a saída designada de qualquer run do plano pelo `GET /runs/{id}` — o mais que o corpo alcança é
exactamente o que já lhe é servido». **É falsa num run por handle:** o que essa rota serve é a
**referência**, não os bytes — é esse o defeito da §1.2 —, e o conteúdo por trás do handle não é
servido por rota nenhuma nem por tool nenhuma, por desenho (§2.4). A travessia converte referência
em texto em claro dentro de um run que o chamador compõe. **Isso é novo, e fica dito.** A frase foi
encontrada pela revisão adversarial deste ticket; ficou registada aqui porque uma afirmação sobre
segurança que entra num ADR passa a ser a razão pela qual ninguém a volta a verificar.

**O que confina a travessia são quatro perguntas a que o chamador não pode responder à sua
escolha:**

| Pergunta | Quem a fixa | Passo |
|---|---|---|
| **Qual** conteúdo | a âncora selada pelo kernel — a **saída designada** desse run, e não um handle qualquer dele. Não «a saída que esta aresta do documento declara»: o nó não lê o documento (ADR-018) | 4 |
| **De que run** | o pedido de plano que o nó provou fixa a **forma** do id: `<pedido>~<X>`, ou a sua tentativa | 1 e 2 |
| **De quem** | o `sealed_subject` do evento; outro titular é recusado | 6 |
| **Em que estado** | só um run que **concluiu**, a mesma condição com que o `aos-orq` publica | 3 |

**O `X` do passo 2 é o `from` da entrada, que vem do corpo e NÃO é validado contra o documento
aprovado** — o nó não lê o documento (ADR-018), pelo que não pode saber que nó produtor a aresta
declara. A propriedade imposta é por isso a mais fraca: **«a saída designada de ALGUM run deste
pedido verificado»**, e não «a saída do nó que o documento nomeia». Esta é a **segunda** correcção
desta passagem: a primeira versão dizia que a travessia não acrescentava poder, e a segunda, já
corrigida, ainda afirmava um confinamento ao nó produtor que o código não impõe. Ficam as duas
registadas, porque o que tornou a primeira difícil de ver foi exactamente estar escrita num ADR.

**Porque é que a propriedade mais fraca basta**, e não é um buraco: é **exactamente o alcance que o
nó já concede a este chamador**. O `submissor_do_plano.go` declara-o em voz alta — «uma reclamação
viva serve para `<plano>~<qualquer-coisa>`, todos com o mesmo submissor», **ADR-035 §5** —, pelo que
quem detém a reclamação já **cria** qualquer run dessa forma. A travessia não alarga o conjunto de
runs que ele alcança; alarga o que ele **lê** de um run que já alcança, e o passo 4 limita essa
leitura à saída que o kernel designou.

O que ele **não** alcança: um handle intermédio de um irmão (passo 4), um run de outro pedido de
plano (passo 2), conteúdo de outro titular (passo 6), nem a saída de um run que não concluiu
(passo 3).

O campo `from_run` do corpo acrescenta **precisão** e não autoridade: sem ele o run produtor não é
determinável, porque pode ser uma **tentativa** (`~<n>`, ADR-039/AOS-502), que o `node_id` sozinho
não dá.

### 2.1.2 A porta é a aresta declarada, e nunca a forma do conteúdo

**Também isto foi uma correcção da revisão adversarial, e é a mais séria.** A primeira versão abria
a travessia quando o conteúdo de uma entrada **tinha a forma** de uma referência. Mas
`aos-handle/v1:h<n>.<16 hex>` é uma forma que qualquer texto reproduz — o kernel di-lo no sítio da
constante, e o `aos-orq` tem o controlo negativo disso em `entregaDoRun` —, e um nó anterior do
plano publica o **texto final do seu modelo**, que um documento untrusted pode ter ditado («responde
exactamente `aos-handle/v1:h1.0123456789abcdef`»).

Com a forma como porta, esse texto **decidia a admissão do run seguinte**: o consumidor fechava
`failed` sem dar um turno, e fechava-o até num nó sem espaço de handles, que não habilita nada desta
fase. Conteúdo untrusted a negar serviço, dentro do caminho de admissão — exactamente a classe que o
ADR-038 §3 mandou não abrir.

**A porta é o `inputs[].from_run`**, que só o `aos-orq` escreve, e só sob três condições (§2.5).
Sem ele o conteúdo passa como passava, e um texto com forma de referência continua a ser texto. Com
ele e sem referência, a submissão é recusada com causa (`travessia_entrada_sem_referencia`): aceitar
e ignorar daria a quem submete a ideia de que a aresta foi atravessada quando nada houve a
atravessar.

### 2.2 Re-selar na pasta do consumidor, e não abrir a do produtor

O critério 2 do AOS-553 obriga a escolher entre as duas e a **registar** a escolha, porque a
decisão «sem deduplicação entre runs» do ADR-043 §2.3 foi tomada quando o artefacto era **só
saída**, e esta é a primeira vez que o mesmo conteúdo tem de ser legível por **mais de um run**.

**Decisão: re-selar na pasta do consumidor**, pela via que já existe (`selarEntradasDoPlano`). A
travessia só **lê** do armazém do produtor; quem escreve é o selo das entradas, que corre logo a
seguir e cunha o token, o ordinal e os dados associados **deste** run.

| Eixo | Re-selar (escolhida) | Abrir a do produtor (recusada) |
|---|---|---|
| Cifra por titular | O mesmo titular, dados associados próprios. O crypto-shredding da DSAR alcança as duas cópias | O mesmo titular, e também alcançado — mas a retenção do run produtor passa a poder tornar ilegível a entrada de outro run |
| Amarra ao run | **Fica invariante.** `ErrHandleOutroRun` continua a ser uma propriedade | Torna-se **parâmetro**: `handleCriado` ganha run de origem, `abrir` passa a receber dois runs, e o guarda da declaração passa a aceitar uma que aponta para fora. É desmontar a barreira 2 para passar por ela |
| `handle.created` | O consumidor tem o **seu**, com o seu ordinal. A linhagem da aresta é o digest, que é o mesmo nos dois eventos, mais o `plan_input_from`/`_output` do tail | O consumidor declara um handle cujos bytes não são dele |
| Replay e retoma | **Autónomos.** O consumidor lê o seu stream, o seu blob, a sua amarra. Nada muda face a hoje | O replay do consumidor passa a depender do stream e do blob de outro run sobreviverem |
| Tectos | Conta no tecto do consumidor: até 16 entradas (`maxPlanInputs`) de ≤ 256 KiB (`handleMaxBytes`) = 4 MiB, dentro dos 16 MiB de `handleMaxBytesPorRun`. Gasta até 16 das 64 posições de `handleMaxPorRun`, deixando 48 tool calls | Não conta. É o único eixo em que ganha |
| Deduplicação (ADR-043 §2.3) | **Reafirma-a**: a chave continua `(run, sha256)` e os bytes existem duas vezes | **É** deduplicação entre runs, e exigia emenda ao §2.3 |

**A razão dirimente são as duas linhas do meio.** Abrir a pasta do produtor compra um tecto e paga
com a invariante que o espaço de handles existe para ter, e com a autonomia do replay do
consumidor. O custo da escolha feita é conteúdo duplicado em disco, limitado pelo tecto por handle
e pelo tecto agregado do canal de entrada — e fica **declarado**, não escondido.

### 2.3 A recusa é da hospedagem, e o cross-titular tem nome

Uma entrada que venha por referência e não se resolva **recusa a hospedagem do run consumidor**: o
run fecha `failed` sem dar um turno. As duas alternativas são piores e foram medidas: entregar o
literal ao modelo é o lixo silencioso que este ticket fecha, e deixar passar a referência como se
fosse conteúdo põe no `plan_input` um token que este run não abre.

O **cross-titular** é recusado **explicitamente e antes de qualquer decifra**. A cifra por titular
bloquearia a abertura de qualquer modo — e é essa a propriedade que se quer —, mas um erro de
decifra não distingue «outro titular» de «disco corrompido». O que o passo 6 acrescenta é o nome.

### 2.4 Não nasce caminho de leitura

A resolução é **interna ao nó**. Não nasce rota HTTP para o conteúdo do espaço de handles e não
nasce tool de leitura: o `artifact_read` que o roteiro do ADR-043 §7 punha na R1b está **fora de
âmbito por emenda** desta fase, porque uma tool daria ao modelo um pedido por digest — o vector que
o guarda do evento (passo 5) fecha. A travessia não serve bytes a ninguém: troca o conteúdo de um
campo do `Goal` antes de o run arrancar.

### 2.5 O que o `aos-orq` passa a fazer, e sob que condição

A entrega de uma referência, e a escrita do `from_run` que a acompanha, ficam atrás de **três
condições** — as duas primeiras são correcções da revisão adversarial:

1. **O nó anunciou a travessia** (`GET /tools`, `handle_traversal.plan_input`, no molde do
   `plan_input_deferral` do AOS-542). Sem isto o campo ia para um nó que não o conhece, cujo
   `POST /runs` o recusa com **400** (`DisallowUnknownFields`) e mata o nó do plano — a combinação
   «`aos-orq` novo × nó antigo».
2. **O payload foi publicado POR REFERÊNCIA.** O mapa de payloads do `aos-orq` é indiferenciado:
   lá entram a entrega por referência, a forma fechada e o **texto final** do run filho. Decidir
   pela forma do texto punha `from_run` numa aresta cujo payload é o que um documento untrusted
   ditou — e do outro lado é o `from_run` que abre a travessia.
3. **E a forma é de facto a de uma referência.** Mantém o corpo do pedido byte a byte o de antes
   em qualquer aresta que transporte conteúdo.

O run produtor que o campo leva vem do **`Record.Stream` do evento de publicação**, e não de uma
re-derivação no momento do consumo: o valor autoritativo está no artefacto selado, e a reidratação
já o confere. Re-derivá-lo fazia o campo depender de estado em memória com um argumento em
comentário no lugar de uma conferência.

Com as três condições satisfeitas:

- nó que anuncia: a referência entrega-se inteira, com a forma de extracção `handle_ref` no evento
  de publicação, e é o consumidor que a resolve;
- nó que não anuncia (anterior a este ticket, ou sem o layout habilitado): fica
  `origem_por_handle`, como hoje.

**A extracção não corre sobre uma referência.** Ela não é envelope da sandbox nem conteúdo a aparar,
e o que se entrega têm de ser os bytes designados **inteiros** — senão a conferência do passo 4, que
o consumidor faz contra a âncora, não bate. É também por isso que a forma tem nome próprio em vez
de contar como `raw`: quem audita o log do plano tem de poder distinguir uma aresta atravessada de
uma entrega de conteúdo, porque a cadeia de confiança de quem a lê depois é outra.

### 2.6 Desligado por omissão

Nada disto corre sem o interruptor que já existe. O anúncio do nó exige o layout por handle
habilitado (`AOS_CONTEUDO_POR_HANDLE` **e** o produtor do `tool_result` composto) e o espaço de
handles aberto. A porta do nó impõe a mesma condição: um `inputs[].from_run` num nó que não
atravessa é **400**, pelo molde do `conteudo_por_handle`, para a porta e o anúncio não poderem
divergir.

E a travessia é **inerte** num nó que não o habilite — mas a razão **não** é «nenhuma referência
chega», que era o que a primeira versão dizia e é falso (§2.1.2): um texto com forma de referência
chega sem produtor nenhum. A razão é que **nenhum `from_run` chega**, porque nem o `aos-orq` o envia
nem a porta o aceita. Um Goal sem nenhuma aresta declarada sai tal como entrou, sem uma leitura e
sem uma alocação.

---

## 3. Alternativas consideradas

- **O nó PRODUTOR resolve, e o `aos-orq` continua a transportar conteúdo.** A mais simples e, em
  isolamento, a mais segura: o produtor resolve um handle do **mesmo** run que o selou — mesma
  pasta, mesma amarra, evento do próprio stream —, pelo que não há *confused deputy* nenhum e o
  caminho de admissão do consumidor não muda uma linha. **Recusada** por duas razões. A primeira é
  de contrato: o critério 2 do AOS-553 diz «o nó **consumidor** resolve do armazém do run
  produtor». A segunda é de substância, e é a que o ADR-038 §3 já tinha escrito: assim os bytes
  continuam a passar pelo `aos-orq` e o tecto do corpo do pedido continua a contar — isto é, não se
  ganha nada do que fazia a alternativa ser «a direcção certa a prazo». Exigiria ainda que a âncora
  passasse a atestar duas coisas (a referência e o conteúdo), ou que o nó servisse os factos do
  handle para o `aos-orq` re-cunhar o token; mais superfície, pelo mesmo resultado.
- **Reusar o token do produtor no run consumidor.** Impossível, e por construção:
  `MintHandleToken` liga o token a `(run, digest, ordinal)`, e `ligacaoDoRun` liga o blob ao
  `sha256(run_id)` do produtor. O token tem de ser **re-cunhado**, que é o que o re-selo faz.
- **Derivar o run produtor sem o corpo o nomear** (`<pedido>~<from>`, só isso). **Recusada por
  medição:** o run produtor pode ser uma **tentativa** (`<pedido>~<from>~<n>`, AOS-502/503), e qual
  delas concluiu não é derivável do `node_id` sozinho. As saídas são: varrer tentativas (N leituras
  de stream por entrada) ou deixar o corpo nomear e confinar o que ele pode nomear. A segunda é a
  do ADR-035 e é a escolhida.
- **O corpo levar o digest do conteúdo em vez da referência.** Dava ao chamador um pedido por
  digest, que é o vector que o guarda do evento fecha, e perdia a amarra do passo 4 — o digest do
  conteúdo não é o que a âncora designa.
- **Resolver no `CallRewriter`, a jusante, em vez da admissão.** A entrada do plano não é um
  argumento de uma call: ela entra no tail do primeiro turno. Um handle de `plan_input` que só
  resolvesse quando o modelo o usasse num argumento deixava o primeiro turno sem material e punha
  a decisão de resolver dentro de um contexto que já é untrusted.
- **Publicar o conteúdo na memória, para o consumidor o ler.** A memória não está ligada em
  produção e é fail-closed na autoridade — a mesma razão com que o ADR-038 §3 a recusou.
- **Entregar a referência a qualquer nó, sem anúncio.** É o defeito que este ADR fecha, com um
  passo a mais: um nó anterior a este ticket receberia um token e não teria nem a travessia nem a
  causa — e, com o `from_run`, o seu `POST /runs` responderia 400.
- **A FORMA do conteúdo como porta da travessia** (atravessar sempre que uma entrada «parece» uma
  referência). Foi a primeira versão deste ADR e **foi recusada por medição**, na revisão
  adversarial: a forma não é autoridade, e um texto que um documento untrusted dite a um modelo
  reproduz-na — passando a decidir a admissão de um run noutro nó. Ver §2.1.2.

---

## 4. Consequências

- Um plano **sem** saídas por referência não muda: os mesmos corpos de pedido (o `from_run` é
  `omitempty` e só vai com uma referência), os mesmos eventos, o mesmo desfecho.
- Um plano com saída por referência e um nó que **não** anuncia a travessia fecha com
  `origem_por_handle`, como no AOS-556. Não há regressão para quem já corre.
- O `/metrics` do nó ganha dez valores no vocabulário fechado de `aos_handle_total`:
  `travessia_feita` e as nove causas. Nenhum leva um byte de conteúdo, um token, um digest nem um
  titular. `travessia_arestas_incoerentes` tem de ser **zero**: é defeito de composição do nó.
- O ficheiro de métricas do `aos-orq` ganha um valor em `aos_orq_consume_entrega_extraccao_total`
  (`handle_ref`) e mantém o vocabulário fechado.
- O `GET /tools` ganha `handle_traversal`, aditivo. Um `aos-orq` anterior ignora-o e continua a
  fechar com `origem_por_handle`.
- O `POST /runs` ganha `inputs[].from_run`, aditivo e `omitempty`. Um nó anterior recusaria o campo
  com 400 (`DisallowUnknownFields`), que é a postura certa: um `aos-orq` só o envia a um nó que
  anunciou a travessia.
- **Conteúdo duplicado em disco** por aresta atravessada, limitado pelo tecto por handle (256 KiB)
  e pelo número de entradas (16). Em troca, o replay do consumidor fica autónomo.
- **O tecto do corpo do pedido deixa de contar, e isso é o ganho que a §3 do ADR-038 prometia** —
  mas tem a sua consequência, que fica dita. O que viaja no `POST /runs` é a referência, pelo que
  os tectos do canal de entrada (`maxPlanInputBytes` 128 KiB, `maxPlanInputsBytes` 512 KiB) deixam
  de limitar o CONTEÚDO: quem passa a limitá-lo é o tecto por handle (`handleMaxBytes`, 256 KiB,
  imposto pelo produtor na selagem) e os tectos por run do espaço. Um payload legítimo entre 128 e
  256 KiB passa a atravessar a aresta — que é exactamente o primeiro gatilho que o ADR-038 §3
  escreveu («o primeiro payload legítimo acima do tecto»).
- **A travessia NÃO é governada pelo layout do CONSUMIDOR**, e é deliberado: quem decide se há
  referência a resolver é o run PRODUTOR, e `conteudo_por_handle` é por run. Um consumidor numa
  1.7.0 que consuma um produtor da 1.8.0 recebe os bytes **inline** no seu tail, como qualquer
  payload de plano — o que é o desfecho certo (a alternativa era o token) e não viola tecto
  nenhum, porque o corpo do tail não tem tecto até à 1.7.0 (resíduo declarado do AOS-550). O que
  muda é que esse segmento pode agora chegar a 256 KiB onde o tecto do wire o deixava em 128.
- O tecto de 64 handles por run passa a ser partilhado entre as entradas atravessadas e as tool
  calls do run, o que estreita o resíduo que o AOS-556 declarou (`handleMaxPorRun` como tecto de
  tool calls na 1.8.0): com 16 entradas ficam 48 calls.
- Uma retoma **não re-atravessa**: o registo de retoma leva os `Inputs` com os bytes já resolvidos
  e o handle que os sela, e `selarEntradasDoPlano` não re-sela uma entrada que já traz handle. É o
  que mantém o `prompt_hash` da retoma igual ao do run que ela continua.

## 5. Resíduos e limites declarados

- **Um `serve` que reinicie sem o anúncio recusa um payload publicado como referência.** A
  reidratação repete a derivação com a postura de agora, e o consumidor não corre. É o desfecho
  certo (entregar um token que o nó já não resolve seria pior), mas é perda de progresso de um
  plano a meio, e fica dito.
- **Um nó multi-processo não atravessa.** A travessia lê o stream e o blob do run produtor do
  **seu** armazém. Se o produtor correu noutro nó, não há `handle.created` local nem ficheiro, e a
  recusa é `travessia_sem_declaracao_do_produtor` ou `travessia_conteudo_indisponivel`. Fail-closed
  e nomeado, mas é um limite real da fase: o ADR-018 mantém a decomposição multi-nó fora do `aos`.
- **O vocabulário aberto do espaço encontra aqui o mesmo limite que na resolução.** O espaço de
  handles aceita binário, e desde o AOS-551 o produtor sela o corpo de qualquer tool call
  permitida — logo um resultado binário pode ter handle. A travessia **recusa** conteúdo que não
  seja UTF-8, com a MESMA causa da resolução dentro de um run (`handle_nao_textual`): o conteúdo
  atravessado vai para o `PlanInput.Content` e, num consumidor até à 1.7.0, inline para o tail e
  dali para o pedido ao modelo. A assimetria (a resolução recusava, a travessia não) foi um achado
  da revisão de segurança deste ticket.
- **`NÃO VERIFICADO`: a prova em produção.** Nada disto correu em produção, e não pode correr antes
  de o `aos-orq` **pedir** o layout 1.8.0 (`conteudo_por_handle` no `POST /runs`), que ele não envia
  hoje — é do AOS-556, cujo critério «o interruptor da 1.8.0 passa a accionável» o cobre. Até lá a
  travessia é exercida por testes e não por um plano real.
- **A janela entre o selo das entradas e o primeiro efeito do run foi FECHADA pelo AOS-557** para os
  dois portões accionáveis por quem submete — o contrato de conclusão e a origem da saída —, que
  passaram a correr ANTES do selo, com a oferta do catálogo e pela mesma função que decide no `Run`.
  A travessia nunca a alargou: ela só lê, pelo que uma recusa dela não deixa bytes atrás de si; o
  que deixa bytes é o selo, que corre depois dela. **Continua aberta a janela do CRASH** — o
  processo morrer entre o selo e o registo de retoma —, que é o **AOS-554**.
- **O `well_formed` de um handle de `tool_result` é `false`**, porque o resultado de uma tool não
  traz media type e o nó não o adivinha. O AOS-551 declarou que o facto «é lido por quem atravessa
  a aresta do plano» — e esta decisão **não o lê**: a travessia não precisa da forma dos bytes, só
  da sua identidade. O resíduo do AOS-551 resolve-se assim, por não haver leitor.
- **A linhagem da aresta não é um campo.** O `handle.created` do consumidor não nomeia o run
  produtor; o que liga as duas pontas é o **digest**, que é o mesmo nos dois eventos, mais o
  `plan_input_from`/`plan_input_output` do tail e o `Record.Stream` do evento de publicação do
  plano. É recuperável, e não é um elo em campo próprio — se um caso o exigir, é ticket.

## 6. Conformidade / enforcement

- As seis conferências são impostas num só sítio (`cmd/aos/travessia_da_aresta.go`), antes do selo
  das entradas e antes de o run arrancar.
- O vocabulário das causas é **fechado** e é o mesmo do espaço de handles, com teste que o fecha.
- Testes do ticket, por nome:
  - `TestAOS553_ATravessiaEntregaOConteudoDoProdutorEReSelaNoConsumidor` — ponta a ponta no nó,
    com as três barreiras a continuarem válidas depois (o token do produtor não resolve na
    quarentena do consumidor) e as duas cópias em disco;
  - `TestAOS553_UmTextoComFormaDeReferenciaNaoDecideNada` — a porta é a aresta declarada (§2.1.2),
    nos três nós em que a versão anterior recusava, com não-vacuidade;
  - `TestAOS553_CadaConferenciaTemControloNegativo` — as seis conferências, cada caso a partir do
    caso bom e com pré-condição de que o caso bom passa na mesma bancada;
  - `TestAOS553_UmHandleIntermedioDoProdutorNaoAtravessa` — o passo 4, com a não-vacuidade de que
    o handle intermédio **é** abrível quando é ele o designado;
  - `TestAOS553_CrossTitularRecusadoPorNomeENaoPelaDecifra` — o passo 6, com o controlo negativo
    de que a cifra bloquearia de qualquer modo;
  - `TestAOS553_RunDoNoProdutor` — a forma do id, incluindo o `~` escapado e a tentativa;
  - `TestAOS553_ATravessiaNaoReCorreNumaRetoma` — o mesmo handle e o mesmo conteúdo;
  - `TestAOS553_UmEventoComTokenEDigestQueNaoSeCorrespondemNaoResolve` — a re-cunhagem do token;
  - `TestAOS553_OVocabularioDaTravessiaEntraNoDoEspaco` e `TestAOS553_OAnuncioExigeAsDuasCoisas`;
  - no `aos-orq`: `TestAOS553_AEntregaDaReferenciaSegueOAnuncioDoNo`,
    `TestAOS553_OAnuncioNaoRelaxaAsOutrasGarantias`,
    `TestAOS553_OPrefixoContinuaANaoSerAutoridade`, `TestAOS553_FromRunSegueAsTresCondicoes` e
    `TestAOS553_OAnuncioDoNoLeSeDoGETTools`;
  - no `plannerevents`: `TestAOS553_FormaHandleRefNoVocabulario` e
    `TestAOS553_ADigestDaReferenciaTemDeSerODaAncora`.
- **Cinco mutações dirigidas**, cada uma vermelha pela razão certa: tirar o passo 4; o titular vir
  do pedido em vez do evento; conferir o run produtor por prefixo em vez da forma completa; tirar a
  re-cunhagem do token; e repor a FORMA como porta da travessia, que avermelhou nos três caminhos
  de recusa que a revisão adversarial descreveu.
- Gates do eixo: `apex`, `security`, `replay`, `deferrals`, `rtm`, `ref-lint`.

## 7. Referências

- `docs/adr/ADR-038-a-saida-de-um-no-de-passagem-directa-e-o-resultado-da-tool.md` — §2.3 (a
  âncora), §2.6 (o transporte), **§3** (a alternativa rejeitada e os seus gatilhos)
- `docs/adr/ADR-043-harness-por-capacidades.md` — §2.3 (o armazém, a cifra por titular, «sem
  deduplicação entre runs»), §7 (o roteiro R0–R6)
- `docs/adr/ADR-044-a-politica-de-fluxo-de-dados-decide-por-destino-e-proveniencia.md` — a política
  que dá sentido à fase; esta decisão não a altera
- `docs/adr/ADR-035-submissor-do-plano-derivado-pelo-no.md` — o molde da autoridade: o corpo diz
  qual, o nó verifica se
- `docs/adr/ADR-027-execucao-dos-nos-do-plano-como-runs-do-no.md` — `<plano>~<nó>`
- `docs/adr/ADR-034-autorizacao-derivada-do-contexto.md` — resíduo 9 (a amarra do cifrado ao run)
- `docs/reports/desenho-r2-entradas-por-handle-2026-10-23.md` — §3 (o handle), §7 (os resíduos)
- `specs/EPIC-19_Planeador_Meta_Orquestracao.md` — AOS-553
- `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md` — AOS-551, AOS-556 (resíduo 5(a)), AOS-557
