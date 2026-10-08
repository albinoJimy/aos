# ADR-036 — O tail é a forma canónica da conversa; o que vai para o provider é uma projecção dele

- **Estado:** Aceite
- **Data:** 2026-10-03
- **Deciders:** Dono do produto (decisões D1 a D4 do desenho de 2026-10-03) · executor de
  AOS-489/490 (implementação)
- **Tickets:** AOS-489, AOS-490, AOS-504 (emenda de 2026-10-06 aos §2.4 a §2.6: projecção nativa
  1.1.0), AOS-505 (emenda de 2026-10-07: §2.8, a rota que serviu o turno), AOS-506 (emenda de
  2026-10-07 ao §2.4: projecção nativa 1.2.0, e o aviso de nova tentativa como segmento da
  semente), AOS-514 (emenda de 2026-10-08 aos §2.3 e §2.7, feita pelo ADR-040: o layout 1.5.0
  e o estado opaco do provider), AOS-513 (emenda de 2026-10-08 ao §2.8: o que o perfil da rota
  passa a poder declarar)
- **Emenda:** ADR-034 §2.1 (a tabela de segmentos ganha o `tool_call`) — a emenda vive no próprio
  ADR-034.
- **Relacionados:** ADR-005 (conteúdo untrusted é dados, nunca instruções), ADR-009 (prefixo
  cache-estável), ADR-010 (atribuição e hash-chain), ADR-034 (autorização derivada do contexto),
  ADR-027 (cada nó do plano é um run do nó)

## 1. Contexto

Medido sobre o `events.wal` de produção inteiro a 2026-10-03: desde que há eventos de mediação, 17
dos 32 runs que pediram tools repetiram a mesma tool sobre o mesmo recurso, e 45 das 75 tool calls
registadas são repetições. Numa das três validações da v0.1.43/44 o modelo desistiu depois de a
releitura ser negada e o plano saiu verde sem o objectivo cumprido
(`docs/reports/desenho-protocolo-tool-use-2026-10-03.md`).

A causa está no que o modelo recebe. O nó enviava o prompt inteiro como uma mensagem de utilizador.
No turno a seguir a uma tool call, o modelo via o objectivo e um `<tool_result taint=untrusted>`:
a chamada que ele próprio fez não estava registada, o resultado não dizia a que chamada respondia,
e nada explicava o protocolo. A documentação do provider de produção lista esta forma como a
primeira causa de tool calls repetidas.

O contrato do gateway já tinha os turnos nativos (`assistant` com `tool_calls`, `tool` com
`tool_call_id`), e o planeador do `aos-orq` já enviava `system` e `user` separados pela mesma
pipeline. O adaptador do nó não os usava.

## 2. Decisão

### 2.1 O tail é canónico

A conversa de um run é o tail: a sequência append-only de segmentos tipados que o runtime escreve.
É o tail que tem hash (`prompt_hash`), que o replay reproduz e de onde a autoridade de cada turno é
derivada (ADR-034). Tudo o que o modelo precisa de saber sobre a conversa — incluindo as chamadas
que ele próprio fez — está no tail, estruturado.

### 2.2 A tool call do modelo é um segmento (assembler 1.4.0, AOS-489)

Por cada tool call, antes do seu resultado, o tail leva um segmento `tool_call` com o `id` da
chamada e o nome da tool na linha de delimitação e os argumentos, tal como o modelo os emitiu, no
corpo. O `tool_result` leva o mesmo `id` e nome.

- O `id` é **cunhado pelo runtime** (`<passo>-tool-<n>`, o `step_id` do evento de mediação). É
  determinístico, reconstrói-se das capturas já gravadas, e **não decide nada**: nem aprovação, nem
  idempotência, nem política. O id do provider não é guardado.
- O segmento regista só o que o modelo emitiu. Capability, recurso, região, reversibilidade, o
  input reescrito, a `Reason` de uma recusa e os metadados de hook ficam fora do prompt.
- O tipo `tool_call` é classificado como output do modelo: não eleva nem baixa a autoridade.
- O prefixo ganha um preâmbulo de protocolo fixo e versionado, que diz ao modelo o que são os
  segmentos.
- Quando a mesma tool call (mesma tool, mesmos argumentos) dá o mesmo desfecho três vezes seguidas
  num run, o tail leva, a seguir ao terceiro resultado, um segmento `notice`: trusted, de texto
  fixo, com o `id` da primeira chamada dessa série. É derivado das tool calls do run e dos seus
  resultados — não é capturado, e o replay recalcula-o.
- O `tool_result` de uma tool que falhou leva o rótulo `tool_error=1` na linha de delimitação; a
  mensagem do erro fica no corpo.

### 2.3 O layout é versionado, e a versão fixa-se por run

O assembler monta mais do que um layout. O layout de um run é decidido no arranque e fica no
registo de retoma; uma retoma continua no layout em que o run começou. O replay escolhe o layout
por turno, pelo `assembly_version` gravado em cada `turn.recorded`. Uma versão desconhecida falha
fechada. Uma subida do assembler deixa de invalidar o replay dos runs gravados.

#### Emenda de 2026-10-08 (AOS-514, ADR-040): o layout 1.5.0

O assembler monta um terceiro layout, o **1.5.0**: é o 1.4.0, byte a byte, mais o rótulo
`state_digest=sha256:<hex>` no fim da linha de delimitação do primeiro segmento que um turno
acrescenta (o `history`, ou a primeira `tool_call`), quando o provider devolveu estado opaco com
esse turno. Um turno sem estado materializa os bytes do 1.4.0 e tem o mesmo `prompt_hash`. **Não
é o layout dos runs novos**: só o é num nó com a captura do estado ligada
(`AOS_MODEL_PROVIDER_STATE=capture`). A regra, o digest e o que o rótulo não pode fazer estão no
ADR-040 §2.5 e §2.6.

### 2.4 O que vai para o provider é uma projecção do tail (AOS-490)

O adaptador do gateway projecta o tail na forma que o provider espera. A projecção é uma função
**determinística e versionada** do tail e do system; não acrescenta nem interpreta conteúdo.

Há duas projecções, seleccionáveis por configuração do nó:

- **Texto único:** o prompt materializado numa mensagem de utilizador. É a forma anterior, e
  continua disponível.
- **Mensagens nativas:** `system` (o protocolo e o `system` do run), `user` (entradas e
  objectivo), e por turno um `assistant` com `tool_calls` e um `tool` por chamada, com o `id` do
  tail como `tool_call_id`.

**Quando se aplica a nativa.** A um turno montado no layout 1.4.0, num nó configurado em nativo
(`AOS_MODEL_PROJECTION`, valor por omissão `native`; `text` repõe o texto único; outro valor
recusa o arranque). Um run fixado na 1.3.0 vai sempre em texto único, byte a byte como antes: a
1.3.0 não tem o segmento `tool_call`, e não há de onde tirar o `assistant`. Um layout futuro tem
de ser acrescentado à projecção de forma explícita.

**O protocolo nativo.** A mensagem `system` abre com um texto fixo, ASCII e versionado com a
projecção, que adapta o preâmbulo da 1.4.0 à forma de mensagens: as mensagens `user` e `tool`
são feitas de segmentos com uma linha de cabeçalho que só o runtime escreve; só `objective`,
`correction` e `notice` são instruções; tudo o resto — as mensagens `tool`, `plan_input`,
`memory`, o que levar `taint=untrusted` e o texto das mensagens `assistant` anteriores — é dados.
A falha e a recusa lêem-se, como no texto, pelos rótulos `tool_error` e `tool_denied` do
cabeçalho da mensagem `tool`. O corpo do `notice` de série estéril é o do layout e fala do
«tool_call whose id is the ref label»: o protocolo nativo diz que essa é a tool call com esse `id`
numa mensagem `assistant` anterior. O bloco TOOLSET do prefixo de texto não tem equivalente: as
tools vão no campo `tools` do pedido.

**O mapeamento.**

- Cada segmento que não é do modelo nem resultado de tool vai numa mensagem `user`, renderizado
  pelo kernel com a mesma linha de cabeçalho e o mesmo corpo neutralizado do texto. Segmentos
  seguidos partilham uma mensagem, cada um com o seu cabeçalho.
- O texto do modelo de um turno (`history`) é o `content` do `assistant`, neutralizado e sem
  cabeçalho.
- `function.arguments` tem de ser um documento JSON, e o segmento fica no tail até ao fim do run.
  Argumentos que são JSON válido vão crus, como o modelo os emitiu. Os omitidos por tamanho vão
  como `{"aos_args_omitted_bytes":N,"aos_args_digest":"sha256:…"}`. Os que não são JSON válido —
  uma resposta truncada, texto solto, bytes que não são UTF-8 — vão como
  `{"aos_args_invalid_bytes":N,"aos_args_digest":"sha256:…"}`, com o tamanho e o digest dos bytes
  originais; vazios vão como `{}`. O texto cru continua no tail e no `prompt_hash`. As chaves
  `aos_args_*` não são uma fronteira: um modelo pode emitir esse mesmo objecto como argumentos
  seus, e confunde-se a si próprio; nada na autorização lê os argumentos projectados.
- `function.name` leva o nome da tool quando ele cabe no alfabeto do wire (letras, dígitos, `_` e
  `-`, até 64). Senão leva o nome reservado `aos_invalid_tool_name`, e não uma versão saneada:
  `doc.read` saneado seria `doc_read`, que pode ser uma tool que existe. O nome original fica,
  saneado como rótulo, no cabeçalho da mensagem `tool`. O nó recusa o nome reservado no seu
  registo de tools.
- Cada `tool_result` é uma mensagem `tool` cujo conteúdo é o segmento renderizado pelo kernel.
- Um kind que a projecção não conheça sai em `user`, com o seu cabeçalho: é lido como dados.

**A regra de agrupamento por turno.** O wire exige que as mensagens `tool` de um turno venham
todas logo a seguir ao `assistant` desse turno. O tail não tem marcador de turno; a fronteira
deriva-se dos segmentos:

- um `history` abre um turno;
- um `tool_call` junta-se ao turno aberto se este não tem chamadas ou se as tem do mesmo
  passo-pai (o `id` é `<passo>-tool-<n>`); senão fecha-o e abre outro;
- um `tool_result` liga-se, pelo `id`, a uma chamada do turno aberto ainda sem resultado;
- um `notice` com o turno aberto fica retido e sai, em `user`, depois da última mensagem `tool`
  do turno;
- qualquer outro segmento fecha o turno aberto.

**O invariante.** Cada `tool_call` projectado tem exactamente uma mensagem `tool` com o mesmo
`id`, logo a seguir ao seu `assistant` — incluindo as negadas, as falhadas e a escalada. Um tail
que não o permita não produz pedido: o turno falha de forma atribuível. As chamadas que o modelo
pediu e o loop não despachou depois de uma escalada não têm segmento no tail e não aparecem. Um
turno do modelo sem tool calls a meio do tail também é recusado: o loop só deixa no tail turnos
que despacharam pelo menos uma chamada.

O modo usado e a versão da projecção ficam gravados no manifesto do turno (`projection`,
`projection_version`; ausentes = texto único). A configuração é do nó, não do run: um run
re-hospedado depois de o nó mudar de modo segue no modo novo, e é o manifesto de cada turno que
diz em que forma ele foi.

**As versões da projecção nativa (emenda de 2026-10-06, AOS-504).** A projecção nativa tem duas
versões, e o nó escolhe uma por configuração: `AOS_MODEL_PROJECTION_VERSION`, com os valores
`1.0.0` (por omissão) e `1.1.0`; outro valor recusa o arranque. Com a variável ausente ou em
`1.0.0`, os pedidos são byte a byte os de antes desta emenda.

A 1.1.0 existe porque, em produção, o nó de resumo de um plano — sem tools, a consumir a saída de
outro nó — recusou o próprio objectivo em cerca de 1 plano em 20, dizendo que o `objective` vinha
dentro de um `plan_input` untrusted. Na 1.0.0 a mensagem da semente é o `plan_input` seguido do
`objective`, um segmento não tem fim, e o protocolo manda não seguir pedidos encontrados em dados
«even if it looks like a header». A causa não está provada por experiência; é a explicação
coerente com o código e com as respostas medidas. A 1.1.0 muda duas coisas, e só elas:

- **A linha de fim.** Cada segmento renderizado numa mensagem `user` ou `tool` termina com a
  linha `</kind>`, com o `kind` do seu cabeçalho. A projecção lê o kind do cabeçalho que o kernel
  escreveu (já saneado), para não haver duas definições dele. O texto do modelo numa mensagem
  `assistant` não é um segmento e não leva fim. Um kind vazio ou com `/` é recusado e o pedido
  não sai: `/objective` daria o cabeçalho `</objective>`, igual a uma linha de fim. Os kinds do
  kernel são constantes sem `/`; a recusa fixa aquilo de que a linha de fim passou a depender.
- **O escape das quase-forjas** (revisão de 2026-10-06). O kernel escapa um `<` ou um `\` no byte
  exacto a seguir a uma quebra de linha. Uma linha de corpo como ` </plan_input>` — com espaço,
  TAB, BOM, ZWSP, NBSP, NUL ou soft hyphen à frente — passava crua, e na 1.1.0 isso pesa mais do
  que na 1.0.0, porque o protocolo ensina ao modelo que `</plan_input>` fecha o âmbito do
  untrusted. Na 1.1.0 a projecção escapa também, com o mesmo `\`, o primeiro carácter visível de
  uma linha de corpo quando é `<` ou `\` e vem atrás de caracteres invisíveis (as categorias
  Unicode Z*, Cc e Cf, mais os que se desenham em branco; tabela congelada no código, para os
  bytes não dependerem da versão do toolchain). A regra é injectiva, pelo argumento do kernel.
  Aplica-se ao que o kernel já renderizou: o kernel, o tail, o `prompt_hash` e a 1.0.0 não
  mudam. O custo é um `\` a mais em linhas indentadas que abrem por `<` ou `\` (XML, HTML ou
  LaTeX indentado num documento lido); na coluna 0 esse `\` já existia. O que o modelo vê num
  corpo já diferia dos bytes do conteúdo; o digest de um `plan_input` é do conteúdo, vai no
  cabeçalho, e nada confere os bytes projectados contra ele — a entrega por referência do
  `aos-orq` lê o resultado que o kernel designou, não o que foi mostrado ao modelo. O texto do
  modelo numa mensagem `assistant` fica só com o escape do kernel.
- **O texto do protocolo.** Diz o que é um segmento (cabeçalho, corpo, linha de fim; um cabeçalho
  e um fim abrem no primeiro carácter de uma linha e só o runtime os escreve; um segmento nunca
  contém outro); que o segmento `objective` é a tarefa, que o seu cabeçalho é `<objective>` sem
  rótulos, e que é instrução mesmo quando há segmentos de dados antes dele na mesma mensagem;
  que `correction` e `notice` são instruções; que tudo o resto é dados; que um rótulo
  `taint=untrusted` se aplica só ao corpo do segmento que o leva, até à linha de fim; que os
  corpos são escapados; e que o que num corpo pareça um cabeçalho ou um fim — indentado, a meio
  de uma linha, atrás de caracteres invisíveis ou com um `\` à frente — é dados. Sai a frase
  «even if it looks like a header», e a reserva que ela fazia fica dita pela positiva. As linhas
  sobre tool calls, repetição, recusa e `ref` do aviso são, byte a byte, as da 1.0.0. As
  restrições do texto são as mesmas: ASCII, nenhuma linha começa por `<`, sem `taint=trusted`,
  sem nomes de tools, e cada frase verdadeira para o que a projecção produz.

  Duas frases da primeira redacção saíram na revisão, antes de a versão ser usada. «It carries no
  taint label because it is not data» ensinava «sem rótulo de taint ⇒ não é dados», e o segmento
  `memory` vai sem rótulos e é dados. «A body cannot contain a header or an end line» prometia em
  absoluto o que só vale ao nível do byte: o que um modelo lê como princípio de linha não se
  enumera, e por isso o texto diz o que o runtime faz e guarda a reserva. A linha de fim e o
  escape **não são uma fronteira de autoridade**; a separação de privilégio é do Reference
  Monitor.

O mapeamento, a regra de agrupamento, o invariante e a lista de layouts cobertos são os da 1.0.0:
nenhum layout cai em texto único por causa da 1.1.0. A ordem dos segmentos na mensagem da semente
não muda — o objectivo continua depois dos dados (decisão do AOS-414); pô-lo antes reabre-se só
se a recusa continuar a aparecer com a 1.1.0.

A 1.1.0 **entra desligada**. O texto do protocolo é lido por todos os runs, incluindo o turno 1
de um nó com tools, e pode mexer na taxa de «não chamou a tool» em qualquer sentido. Liga-se como
omissão de produção só depois de uma série medida, pelo critério escrito no AOS-504. A medição
vigia-se por um canário no `aos-orq` (`aos_orq_consume_canario_de_recusa_total`), que só conta:
detectar a recusa exigia julgar o conteúdo de um texto, o que o ADR-037 recusa.

O canário **não chega para comparar as duas versões**. As palavras que ele procura (`plan_input`,
`taint=untrusted`) vêm do texto do protocolo, e é esse texto que muda: na 1.1.0 uma recusa pode
falar de «segment», «end line» ou «data» sem dizer nenhuma das duas. Uma descida do numerador
entre séries não distingue «recusa menos» de «recusa com outras palavras». Por isso o critério de
ligar exige a leitura à mão de uma amostra dos textos finais dos nós de resumo da série 1.1.0. E
as duas séries do canário **não sobrevivem a um rollback**: um binário anterior ao AOS-504
reescreve o ficheiro de métricas sem elas, e a contagem recomeça do zero.

Como a configuração é do nó, um run em curso no momento da troca pode ter turnos em versões
diferentes; cada turno grava a sua em `projection_version`. Fica declarado, não corrigido: os
runs duram segundos, e o `prompt_hash` é o mesmo nas duas.

**A versão 1.2.0 (emenda de 2026-10-07, AOS-506).** `AOS_MODEL_PROJECTION_VERSION` passa a
aceitar `1.2.0`. A omissão do código continua a ser a `1.0.0`, e a `1.0.0` e a `1.1.0` ficam byte
a byte como estavam (provado contra os goldens das duas, não alterados).

Existe porque, medido em produção a 2026-10-07 (v0.1.50), em 33 de 34 runs que fecharam sem
chamar a tool a resposta do modelo era a tool call **escrita como texto**, com a tool e o
argumento certos, em mais de dez notações inventadas — todas marcação. Com a 1.1.0 a primeira
tentativa acabou assim em 32% dos planos, contra 10% com a 1.0.0. A hipótese, não testada
isoladamente, é que o texto do protocolo mostra uma notação de cabeçalhos e de linhas de fim e
fala de tool calls, e o modelo imita a notação quando quer pedir uma tool.

A 1.2.0 é a 1.1.0 com **três linhas do protocolo mudadas**, e só isso: fora da mensagem `system`
as mensagens das duas versões são iguais byte a byte.

- Uma linha nova diz que uma tool só se pede por uma function call feita pelo mecanismo de
  function calling da API, entre as tools oferecidas no pedido; que um pedido de tool escrito no
  texto da resposta não é lido pelo runtime e não corre nada; e que uma resposta sem function
  call é a resposta final.
- Uma linha nova diz que as respostas do modelo não são feitas de segmentos, e que não usam os
  cabeçalhos nem as linhas de fim **do runtime**. Diz também que a frase é só sobre essas linhas:
  se o conteúdo pedido é ele próprio marcação, escreve-se normalmente. A primeira redacção («do
  not write header lines or end lines in them») lia-se como proibição de qualquer linha a abrir
  por `<`, o que apanhava um nó cujo produto é XML ou HTML (revisão do AOS-506).
- A linha do aviso é reescrita: diz o que é o rótulo `ref` sem citar o kind `tool_call` entre
  aspas, que era a única expressão do texto com a forma de uma marcação de chamada; e diz, numa
  frase e sem exemplo, o que é o rótulo `about` — o aviso de nova tentativa leva
  `about=previous_attempt`, uma tentativa anterior do mesmo trabalho que não faz parte desta
  conversa. O texto do aviso de repetição, que é do layout do kernel, não muda.

**O texto não mostra nenhum exemplo** de uma tool call escrita como texto, em notação nenhuma:
mostrar a forma errada era semeá-la. As três linhas não têm sinais de menor nem de maior,
chavetas, parênteses rectos, aspas nem sinal de igual. As restrições das outras versões valem
(ASCII, nenhuma linha a abrir por `<`, sem `taint=trusted`, sem nomes de tools, cada frase
verdadeira para o que a projecção e o adaptador fazem).

**Não é um parser, e não passa a haver um.** O runtime não interpreta texto do modelo como tool
call, em versão nenhuma (decisão do dono de 2026-10-07): um documento lido pode conter esse mesmo
texto, e um modelo que o ecoasse pedia uma tool por conta de conteúdo untrusted. As tool calls de
um turno saem só do campo de tool calls da resposta do provider; é isso que torna verdadeira a
frase «the runtime does not look for tool requests in reply text».

A 1.2.0 **entra desligada**, pela razão da 1.1.0: o texto é lido por todos os runs. O critério de
a ligar está no AOS-506 (uma série de pelo menos 60 planos com menos de 10% de falhas à primeira
tentativa, zero recusas do próprio objectivo lidas à mão, e «não cumprido» abaixo de 2%). A
mensagem `system` muda, e os tokens servidos de cache de prefixo caem na troca.

**O aviso de nova tentativa é um segmento da semente (AOS-506).** Um run que o nó hospeda como
nova tentativa pode levar, a seguir ao objectivo, um segmento `notice` de texto constante (ADR-039
§2.7). É um kind que o layout 1.4.0 já tinha; o que muda é a **semente** do tail, que é função do
que quem compõe o run declara — ganhou um segmento opcional, como ganhou os payloads do plano. A
projecção trata-o como qualquer `notice` fora de um turno: sai na mensagem `user` da semente, com
os bytes do kernel e, a partir da 1.1.0, a sua linha de fim. **Não há versão nova de layout:** a
forma de um segmento, o preâmbulo e a neutralização são os de antes, e um run sem aviso
materializa os mesmos bytes. O aviso entra no `prompt_hash`, porque está no tail.

**Decisão sobre o layout, e o resíduo que fica nomeado (revisão do AOS-506, 2026-10-07).** Não
se cria a 1.5.0 agora. Com as omissões nada muda; a 1.4.0 já define `notice` como instrução do
runtime sem dizer que só existe a meio do tail, pelo que o aviso não torna falso nenhum byte do
preâmbulo; e uma versão só para os runs com aviso obrigava a estender a projecção nativa, as
métricas por layout e a medição do hash por uma peça que ainda vai ser medida e pode ser
retirada. O custo aceite é conhecido e está medido: um binário anterior que retome uma tentativa
com aviso **diverge em silêncio** (ADR-039 §2.7), porque nada no registo lhe diz que a semente é
outra. **Se o aviso passar a ligado em permanência — ou a ser a omissão —, a semente-com-aviso é
promovida a versão de layout nesse ticket:** é a única forma de um binário antigo recusar a
retoma em vez de a fazer com outro prompt. Até lá, o recuo de imagem faz-se pela ordem do
runbook (a variável, esperar pelas tentativas em voo, a imagem).

**O que o registo permite, e o que ainda não tem ferramenta.** O replay reconstrói o tail de cada
turno, e `ProjectNative` — a função que o adaptador usa, exportada e pura — dá as mensagens a
partir da vista desse turno (`ProjectNativeVersion` para a versão que o manifesto disser). Não existe ainda um leitor que junte as duas coisas: nada lê
`manifest.projection` nem `projection_version`, o motor de replay não expõe a vista
reconstruída, e não recusa uma `projection_version` que não conheça. Fica por fazer.

### 2.5 O `prompt_hash` é o hash do tail canónico

O `prompt_hash` continua a ser o hash do prompt materializado em texto, que é a serialização
canónica do tail. Com a projecção nativa, os bytes enviados ao provider deixam de ser os bytes com
hash; o que o hash ancora é a conversa, e a projecção é função dela e da versão gravada (§2.4
diz o que existe hoje para a reconstruir). As
frases de código e de documentação que dizem «os mesmos bytes que vão para o provider» passam a
valer só para a projecção de texto único.

O `prompt_hash` também não depende da **versão** da projecção nativa (emenda de 2026-10-06,
AOS-504): a linha de fim e o texto do protocolo da 1.1.0 existem só no pedido, não no tail. O
mesmo run dá o mesmo `prompt_hash` na 1.0.0 e na 1.1.0, e os logs gravados reproduzem-se sem
alteração.

### 2.6 A proveniência viaja dentro das mensagens

Numa mensagem nativa não há linha de delimitação. A marcação de proveniência passa para dentro do
conteúdo:

- O conteúdo de cada mensagem `tool` é um envelope que leva o `taint` e, numa recusa, os rótulos
  `tool_denied`, `denied_code` e `denied_by`. O conteúdo untrusted continua neutralizado.
- Segmentos trusted (`objective`, `correction`) e untrusted (`plan_input`, `memory`) não são
  fundidos no mesmo papel sem delimitação inforjável.
- Na projecção 1.1.0 (emenda de 2026-10-06, AOS-504) a delimitação tem dois lados: o cabeçalho e
  a linha de fim `</kind>`. O fim é inforjável pelo mecanismo do cabeçalho, sem regra nova: o
  kernel escreve o corpo neutralizado — uma linha que comece por `<` ou por `\` sai com um `\` à
  frente, em todas as quebras de linha que o layout reconhece — e fecha-o com uma quebra de
  linha, pelo que a linha de fim abre sempre uma linha. Um corpo que contenha `</plan_input>`
  aparece como `\</plan_input>`, e um que já traga essa forma escapada ganha outro `\`. Numa
  mensagem `user` ou `tool`, uma linha que abra por `<` só pode ter sido escrita pelo runtime.
  Isto diz ao modelo onde acaba o que é untrusted; continua a não ser uma fronteira de
  autoridade.
- A separação de privilégio continua a não depender disto: a autoridade deriva do tipo dos
  segmentos do tail (ADR-034) e é imposta pelo Reference Monitor.

### 2.7 O raciocínio do modelo é carga opaca

O contrato do gateway e a resposta do modelo transportam o raciocínio do turno
(`reasoning_content`) como texto opaco, byte a byte. A captura guarda-o, selado por titular; a
retoma e o replay devolvem-no igual.

**Não é devolvido ao provider nesta entrega; fica capturado.** Nenhum pedido o leva — a
serialização do pedido retira-o de todas as mensagens —, e não entra no tail, no prompt nem em
spans. Só a captura do turno o guarda, e segue a regra do texto do modelo: com a captura selada
por titular (produção) vai dentro do conteúdo cifrado; no modo sensível é redigido; sem selo nem
modo sensível (desenvolvimento) fica em claro no `replay.captured`. O campo aceita qualquer valor
JSON — uma string guarda-se como string, outra forma como os bytes JSON que vieram — e nunca
derruba a resposta. Devolvê-lo custaria os seus tokens em cada turno seguinte e obrigava a
pô-lo no tail, que é de onde a projecção sai. Sem tecto próprio: é limitado pelo corpo da
resposta (1 MiB), como o texto do modelo, e truncá-lo quebrava a carga opaca.

Medido a 2026-10-03 contra o LiteLLM de produção (alias `gpt-4o-mini` → `kimi-for-coding`): o
raciocínio chega na resposta e sobrevive no pedido seguinte, e **não é exigido** por este provider
— o turno nativo sem ele foi aceite. Os blocos de raciocínio assinados e os itens cifrados de
outros fornecedores ficam fora desta decisão; o contrato fica preparado para carga opaca.

#### Emenda de 2026-10-08 (AOS-514, ADR-040): os blocos assinados deixam de estar fora

A última frase do parágrafo acima deixa de valer. Com a captura do estado ligada, **tudo** o que
o provider manda e pode exigir de volta — o raciocínio em todos os nomes de campo, os blocos
assinados e os redigidos, o id e a assinatura de cada tool call — é capturado byte a byte, como
o **estado opaco do turno**: o que é, onde fica, quem o lê e o tecto estão no ADR-040. O campo
`reasoning` da captura, de que este parágrafo fala, não muda: continua a ser o primeiro campo de
raciocínio com conteúdo, sem tecto próprio. O resto mantém-se, e o ADR-040 repete-o como regra:
**nada é devolvido ao provider**, e o raciocínio não entra no texto do tail, em spans nem em
eventos em claro. O que passa a entrar no tail é um **digest** do estado, nunca o estado.

### 2.8 A rota que serviu o turno é comparada com um perfil (AOS-505, emenda de 2026-10-07)

O registo de cada turno diz o que foi pedido (o `prompt_hash`, a projecção) e passa a dizer **que
rota o serviu**, comparada com o que o nó esperava dela.

**O problema.** A allowlist assinada governa que **nome** o nó pode pedir ao proxy de modelos. O
que esse nome significa — que modelo e que endpoint o servem — decide-se na configuração do proxy,
fora de qualquer assinatura. E o `served_model_id` do manifesto não o dizia: medido com a imagem de
produção do proxy (LiteLLM 1.96.2), o campo `model` do corpo da resposta é sempre o nome pedido,
porque o proxy o carimba. Trocar o modelo por baixo do nome não mudava nenhum evento.

**O que se lê, e de onde.** Dois cabeçalhos da resposta do proxy: `x-litellm-model-name` (o modelo
que o proxy está configurado a pedir ao provider) e `x-litellm-model-api-base` (o endpoint). Do
segundo fica **só o host** — o esquema, as credenciais, o caminho e a query são deitados fora à
leitura —, e o host só se compara dentro do gateway: não chega ao runtime, a eventos, a métricas
nem a logs. O cabeçalho `x-litellm-model-id` **não é lido**: é um SHA-256 sem sal de todos os
parâmetros do deployment, incluindo a chave do provider — um derivado de segredo. Um cabeçalho
ausente deixa o campo **por reportar**; nunca se preenche com o nome pedido nem com o `model` do
corpo.

**Compara-se o valor cru; grava-se o saneado.** O nome que o proxy declara é texto de terceiros
que acaba em claro num evento, e por isso é saneado (sem caracteres não imprimíveis, cortado a 256
bytes). A comparação **não** se faz sobre esse texto: se o saneamento alterar o valor — um
carácter de largura zero, uma marca de direcção do texto, o corte —, o resultado é `diferente`,
com a causa `modelo_diferente`, e nunca `igual`. Lêem-se **todas** as ocorrências de cada
cabeçalho: repetido com valores diferentes entre si é `diferente`. O host compara-se normalizado
dos dois lados — minúsculas e sem o ponto final do nome absoluto —, e a **porta compara-se como
está**: se o proxy a declara, o host esperado leva-a.

**O perfil da rota.** Vive em código (`route.go` do gateway): nome pedido, modelo esperado, classe
de wire e capacidades declaradas. Não contém segredos nem endereços; o host esperado do endpoint
é configuração do nó. O digest do perfil (`sha256:` sobre o JSON canónico) fica em
`manifest.model.route_profile_digest` de cada turno comparado. O perfil como artefacto assinado do
registo é da fase A3.

**A comparação, por turno.** Um interruptor de três valores, `AOS_MODEL_ROUTE_GOVERNANCE`:

- `off` (por omissão) — nada é comparado. O gateway apaga o que o adaptador leu dos cabeçalhos, e
  os pedidos, os eventos, os manifestos, as capturas e o `/metrics` são byte a byte os de antes.
- `observe` — o resultado (`igual`, `diferente` ou `nao_reportado`) fica em `route_check` do
  `turn.recorded`; o `served_model_id` passa a ser o modelo que o proxy declarou (ausente se não
  o declarou); uma variância é selada no audit de governação do gateway, com o run, o passo, o
  modelo esperado e o servido; e o contador `aos_model_route_checks_total` conta-a. O turno
  segue.
- `enforce` — um turno cuja rota não se prove igual à do perfil **falha**, com causa em
  vocabulário fechado (`modelo_diferente`, `modelo_nao_reportado`, `endpoint_diferente`,
  `endpoint_nao_reportado`, `rota_sem_perfil`), depois de selada a variância. A resposta já foi
  paga e o seu custo já foi contado; o que se recusa é usá-la. `nao_reportado` também falha: o
  que não se prova não passa.

Um valor fora do vocabulário recusa o arranque; com a governação ligada, um modelo sem perfil
também. **`enforce` sem o host esperado do endpoint recusa o arranque**: sem ele uma troca só de
endpoint passaria por `igual` num modo que promete falhar o que não se prova. `observe` aceita-o
por definir e declara no arranque que o endpoint não é comparado. O nome do modelo servido só entra num rótulo de métrica se for um dos modelos esperados
dos perfis; qualquer outro texto conta como `outro`.

**A captura e o replay.** Num turno comparado, a captura guarda o modelo servido, o resultado e o
digest do perfil, e a retoma e o replay devolvem-nos iguais. Num turno não comparado a captura
não guarda nenhum dos três, como nunca guardou, e uma captura anterior reproduz-se sem
divergência: a rota não entra no `prompt_hash` nem na âncora `model` do replay.

**O que isto detecta, e o que não detecta.** Detecta uma **troca de configuração no proxy**: outro
modelo por baixo do mesmo nome pedido, ou outro endpoint (este, só com o host esperado definido).
Provado com a imagem de produção do proxy à frente de dois providers falsos. **Não detecta** uma
troca feita pelo provider por trás do mesmo nome e do mesmo endpoint: os cabeçalhos dizem o que o
proxy está configurado para pedir, não o que o provider serviu. E **não são atestação**: quem os
emite é o proxy, sem prova de origem, e valem enquanto o canal entre o nó e o proxy for de
confiança. O que o provider real devolve sobre si próprio não foi medido.

**Fora da comparação, por decisão deste ticket.** As chamadas ao modelo feitas pelo `aos-orq` (o
planeador, AOS-391/395): esse binário compõe o seu próprio gateway, sem governação da rota, e as
suas chamadas passam pelo mesmo proxy sem serem comparadas — em nenhum dos modos, `enforce`
incluído. `enforce` no nó não impede que um plano seja decomposto por outro modelo. É um limite
escrito e um resíduo nomeado, não uma propriedade provada. O streaming e os embeddings também não
são comparados.

#### O que o perfil passa a poder declarar (AOS-513, emenda de 2026-10-08)

O perfil da rota ganha três campos, **todos opcionais**. Um perfil que não declare nenhum — os
quatro da tabela de hoje — dá o pedido, o digest, o manifesto, a captura e o `/metrics` de antes,
byte a byte.

| Campo | O que declara | Vocabulário |
|---|---|---|
| `params` | Os parâmetros a enviar no pedido a essa rota | Conjunto **fechado e com tipo**: `thinking` (`type` em `enabled`, `disabled` ou `adaptive`; `budget_tokens` só com `enabled`, de 1024 a 1 048 576), `reasoning_effort` (`none`, `minimal`, `low`, `medium`, `high`) e `max_tokens` (de 1 a 1 048 576). Não há parâmetros livres: um nome fora do conjunto não tem onde ser escrito |
| `projection_version` | A versão da projecção nativa (§2.4) a usar nos runs dessa rota | Uma versão **publicada**. O perfil escolhe uma versão; não transporta texto de protocolo |
| `devolver` | A classe de estado: se o estado opaco de um turno (ADR-040) se devolve ao provider | `nunca` (a omissão; é também a classe de uma rota que o proíbe), `opcional`, `obrigatorio`. Quem a consome é a projecção 1.3.0 (AOS-515) |

**De onde vêm, e de onde não vêm.** Só do perfil. O perfil continua a viver **em código**, na
tabela do gateway: muda com uma imagem nova do nó, e é essa a «configuração assinada». O nó não lê
perfis de ficheiro, de ambiente, de um plano, de um manifesto de run, do corpo de um pedido HTTP,
do conteúdo de um run nem da resposta de um modelo. Os campos de raciocínio do pedido da porta não
se lêem de JSON, e o gateway **sobrepõe-nos** em cada pedido com os do perfil — o que um chamador
lá tenha posto é deitado fora.

**Decide-se depois do roteamento.** Os parâmetros são os do perfil da rota **a que o pedido vai**
(o nome resolvido), e não os da rota que o chamador pediu: num failover, a segunda rota recebe os
seus parâmetros, ou nenhum.

**O perfil candidato.** Quem compõe um gateway fora do nó — o banco de ensaio (AOS-512), que
qualifica um perfil **antes** de ele entrar na tabela — pode acrescentar perfis candidatos, lidos
por uma leitura fechada (uma chave fora do conjunto, um valor de tipo errado ou um texto com forma
de credencial recusam, e a mensagem não repete o valor). Um candidato passa pela validação da
tabela e substitui a entrada com o mesmo nome pedido. O relatório do banco leva o digest do perfil.

**O digest.** Cobre os campos novos **quando declaram alguma coisa**: `params` sem nenhum valor e
`devolver` em `nunca` são a omissão, e a omissão não muda o digest. Mudar um parâmetro muda-o.

**No registo do turno.** Os parâmetros enviados ficam em `manifest.model.params`, por cima dos do
Goal, em chaves e valores de vocabulário fechado (`thinking` é o modo, com `:` e o orçamento quando
o tem). O runtime fecha a forma à entrada, como faz ao motivo de paragem.

**A versão fica presa ao run.** Um run novo numa rota cujo perfil declara versão fica **fixado**
nela: o nó escreve-a no Goal e no registo de retoma, e o runtime leva-a na vista de cada turno. A
ordem, em cada turno: a versão em que o run está fixado; senão a do perfil da rota do run; senão o
interruptor do nó (`AOS_MODEL_PROJECTION_VERSION`), que passa a ser a omissão. O perfil lido é o da
rota **do run**, e não o da rota a que o gateway mande o pedido: a projecção de um run não muda num
failover. Um run que começou sem versão fixada continua sem ela depois de uma retoma, mesmo que o
perfil entretanto declare uma. Um run fixado numa versão que o binário não conhece falha fechado,
sem pedido.

**Um 4xx num turno com parâmetros tem nome, e o nó não o contorna.** Medido atrás da imagem de
produção do proxy: o que o perfil manda chega ao provider (o proxy reencaminha), e um parâmetro que
o provider não aceite dá 4xx em todos os turnos da rota. Cada um conta em
`aos_model_route_params_rejected_total{rota,codigo}` — a rota é uma das que têm parâmetros, o
código é de um conjunto fechado, e nenhum byte do corpo do erro sai do gateway. O nó **nunca**
retira o parâmetro para repetir o pedido sozinho. Por isso um perfil com parâmetros, ou com versão
própria, só entra na tabela depois de uma corrida do banco com esse perfil.

## 3. Alternativas rejeitadas

- **Só o preâmbulo de protocolo no `system`.** É uma instrução ao modelo sobre um facto que ele
  continuava a não ver.
- **Mensagens nativas como forma canónica, sem tail estruturado.** Reabria o contrato do
  `prompt_hash` e do replay, e deixava a autoridade a derivar de uma estrutura que o provider dita.
- **O id do provider como id da chamada.** Não existe nas capturas já gravadas, é texto untrusted
  sem tecto, e colide depois de saneado. Medido: o provider aceita um id cunhado pelo runtime.
- **Só o nome e o digest dos argumentos no segmento.** O modelo saberia que chamou a tool, mas não
  com quê, e a projecção nativa ficava impossível.
- **Invalidar o replay dos runs gravados**, como nas subidas 1.2 e 1.3 do assembler. A mudança é
  aditiva e o layout por versão é barato; a recuperação de desastre continua a funcionar sobre
  backups anteriores ao deploy.
- **Ler o modelo servido do `model` do corpo.** É o que se fazia. O proxy carimba-o com o nome
  pedido; não distingue nada.
- **Usar o `x-litellm-model-id` como identidade da rota.** Muda com qualquer alteração do
  deployment num só valor, mas é derivado da chave do provider, dispara com uma rotação de chave
  sem o modelo ter mudado, e fica cego se a configuração fixar um id.
- **Pedir ao proxy o `model` cru do provider** por uma chave interna de metadados do pedido. É o
  único sinal que atravessa o proxy vindo do provider, mas a chave não tem contrato, muda o corpo
  do pedido, e em streaming devolve o nome da configuração.

## 4. Consequências

- O modelo vê as suas próprias chamadas e a que chamada responde cada resultado.
- Os argumentos de uma tool call passam a ir no prompt de todos os turnos seguintes, com tecto de
  tamanho. Se contiverem dados pessoais ou segredos, são reenviados ao provider que sirva o turno.
- Com os argumentos ao lado do código de recusa, conteúdo injectado pode sondar a fronteira da
  política argumento a argumento. A `Reason` continua fora.
- O preâmbulo custa cerca de 286 tokens de entrada por turno na projecção de texto único (1 144
  bytes, a 4 bytes por token). O protocolo nativo custa cerca de 390 (1 559 bytes); o da
  projecção 1.1.0 cerca de 562 (2 247 bytes), mais a linha de fim de cada segmento e um `\` por
  linha de corpo com quase-forja. O da 1.2.0 cerca de 758 (3 033 bytes). O aviso de nova
  tentativa (AOS-506) custa cerca de 110 tokens (440 bytes), só nos runs de tentativa e só com o
  interruptor ligado.
- Trocar a versão da projecção nativa muda a mensagem `system`, que é a cabeça do pedido: os
  tokens servidos de cache de prefixo caem na troca, nos dois sentidos. Lê-se em
  `cache_read_tokens` do registo de turnos, antes e depois.
- A estimativa de admissão de um turno continua a fazer-se sobre o prompt materializado, também
  quando o pedido vai em mensagens nativas.
- O `prompt_hash` deixa de ser, na projecção nativa, o hash dos bytes enviados.
- Um run sem tool calls grava os mesmos eventos que antes, salvo a versão e o prefixo.

## 5. Resíduos declarados

- **Separação de planos (DEF-806).** Fica mais conteúdo untrusted inline no tail. O segmento de
  argumentos é o sítio onde a separação por handle poria uma referência em vez de bytes; o desenho
  deixa-o compatível com isso e não a implementa.
- **Rollback de binário.** Um binário anterior a esta decisão ignora a versão no registo de retoma
  e retoma em 1.3.0 um run começado em 1.4.0; o log fica misto por turno e reproduz-se, mas o
  modelo vê o layout mudar a meio.
- **Modo de captura sensível** (não é o de produção): o `Input` redigido deixa de reproduzir o
  segmento novo no replay.
- **Chamadas paralelas, streaming, blocos de raciocínio assinados e itens cifrados:** fora desta
  decisão.
- **Argumentos JSON que não são um objecto** (`"tick"`, `42`). Vão crus em `function.arguments`.
  Se um provider exige ali um objecto não foi medido.
- **Reconstrução do pedido a partir do registo.** O registo permite-a e a função está exportada;
  falta a ferramenta que a faça de ponta a ponta, e a recusa de uma `projection_version`
  desconhecida no replay.
- **A recusa do próprio objectivo não se detecta** (AOS-504). A projecção 1.1.0 corrige a causa
  provável e um canário conta os casos que usam o vocabulário do protocolo; é um limite inferior,
  e nada decide por ele. Quem precisa de garantia semântica declara um `verifier` no plano.
- **A tool call escrita como texto não é lida** (AOS-506). A 1.2.0 e o aviso de nova tentativa
  mudam o que se diz ao modelo; a eficácia de um e de outro não está medida, e a hipótese de que a
  notação de cabeçalhos induz a imitação não foi testada isoladamente. Um modelo que continue a
  escrever a chamada como texto fecha o run como não cumprido, como antes.
- **Um binário anterior ao AOS-506 não conhece o aviso de nova tentativa, e diverge em
  silêncio.** Se retomar um run que o levou, semeia o tail sem ele, reproduz o turno 1 pela
  resposta gravada sem o comparar e envia o turno seguinte sem o aviso; o run pode fechar
  `complete` e nada alerta. A versão do layout não o explica, porque não mudou (§2.4: fica
  devida se o aviso passar a ligado em permanência). O recuo faz-se pelo interruptor, espera-se
  pelas tentativas em voo, e só depois a imagem.
- **Run com turnos em versões diferentes da projecção** (AOS-504): possível quando o nó é
  recriado com outra versão a meio de um run; cada turno grava a sua.
- **A rota sob governação não vê o provider** (AOS-505). Uma troca feita pelo provider por trás
  do mesmo nome e endpoint não muda nenhum cabeçalho do proxy. O `system_fingerprint`, o `id` e
  os cabeçalhos que o provider emita são auto-declarados por ele e não são lidos.
- **O endpoint só é comparado com o host esperado definido** (`AOS_MODEL_ROUTE_API_HOST`). Sem
  ele, uma troca só de endpoint, com o mesmo nome de modelo, passa por `igual`.
- **Streaming e embeddings** não são comparados: o adaptador do runtime usa a chamada síncrona de
  chat, e só essa lê os cabeçalhos.
- **Com escada de tiers**, o perfil procura-se pelo nome que o gateway de facto pede ao proxy (o
  resolvido pelo roteamento). O arranque só verifica o perfil do modelo do nó; um tier sem perfil
  conta como `diferente` em observação e falha em imposição.
- **Run sem objectivo nem entradas.** A projecção nativa dá um pedido só com `system`; se o
  provider o aceita não foi medido.
