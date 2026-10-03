# ADR-036 — O tail é a forma canónica da conversa; o que vai para o provider é uma projecção dele

- **Estado:** Aceite
- **Data:** 2026-10-03
- **Deciders:** Dono do produto (decisões D1 a D4 do desenho de 2026-10-03) · executor de
  AOS-489/490 (implementação)
- **Tickets:** AOS-489, AOS-490
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
- À terceira tool call idêntica de um run (mesma tool, mesmos argumentos) o tail leva, a seguir ao
  resultado, um segmento `notice`: trusted, de texto fixo, com o `id` da primeira dessas chamadas.
  É derivado das tool calls do run — não é capturado, e o replay recalcula-o.

### 2.3 O layout é versionado, e a versão fixa-se por run

O assembler monta mais do que um layout. O layout de um run é decidido no arranque e fica no
registo de retoma; uma retoma continua no layout em que o run começou. O replay escolhe o layout
por turno, pelo `assembly_version` gravado em cada `turn.recorded`. Uma versão desconhecida falha
fechada. Uma subida do assembler deixa de invalidar o replay dos runs gravados.

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
O bloco TOOLSET do prefixo de texto não tem equivalente: as tools vão no campo `tools` do pedido.

**O mapeamento.**

- Cada segmento que não é do modelo nem resultado de tool vai numa mensagem `user`, renderizado
  pelo kernel com a mesma linha de cabeçalho e o mesmo corpo neutralizado do texto. Segmentos
  seguidos partilham uma mensagem, cada um com o seu cabeçalho.
- O texto do modelo de um turno (`history`) é o `content` do `assistant`, neutralizado e sem
  cabeçalho. Os argumentos de cada chamada vão crus em `function.arguments`; os omitidos por
  tamanho vão como um JSON com `args_omitted_bytes` e `args_digest`. O nome da tool é levado ao
  alfabeto que o wire aceita em `function.name`.
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
pediu e o loop não despachou depois de uma escalada não têm segmento no tail e não aparecem.

O modo usado e a versão da projecção ficam gravados no manifesto do turno (`projection`,
`projection_version`; ausentes = texto único). A configuração é do nó, não do run: um run
re-hospedado depois de o nó mudar de modo segue no modo novo, e é o manifesto de cada turno que
diz em que forma ele foi.

### 2.5 O `prompt_hash` é o hash do tail canónico

O `prompt_hash` continua a ser o hash do prompt materializado em texto, que é a serialização
canónica do tail. Com a projecção nativa, os bytes enviados ao provider deixam de ser os bytes com
hash; o que o hash ancora é a conversa, e a projecção reconstrói-se dela pela versão gravada. As
frases de código e de documentação que dizem «os mesmos bytes que vão para o provider» passam a
valer só para a projecção de texto único.

### 2.6 A proveniência viaja dentro das mensagens

Numa mensagem nativa não há linha de delimitação. A marcação de proveniência passa para dentro do
conteúdo:

- O conteúdo de cada mensagem `tool` é um envelope que leva o `taint` e, numa recusa, os rótulos
  `tool_denied`, `denied_code` e `denied_by`. O conteúdo untrusted continua neutralizado.
- Segmentos trusted (`objective`, `correction`) e untrusted (`plan_input`, `memory`) não são
  fundidos no mesmo papel sem delimitação inforjável.
- A separação de privilégio continua a não depender disto: a autoridade deriva do tipo dos
  segmentos do tail (ADR-034) e é imposta pelo Reference Monitor.

### 2.7 O raciocínio do modelo é carga opaca

O contrato do gateway e a resposta do modelo transportam o raciocínio do turno
(`reasoning_content`) como texto opaco, byte a byte. A captura guarda-o, selado por titular; a
retoma e o replay devolvem-no igual.

**Não é devolvido ao provider nesta entrega; fica capturado.** Nenhum pedido o leva — a
serialização do pedido retira-o de todas as mensagens —, e não entra no tail, no prompt, em spans
nem em eventos em claro. Devolvê-lo custaria os seus tokens em cada turno seguinte e obrigava a
pô-lo no tail, que é de onde a projecção sai. Sem tecto próprio: é limitado pelo corpo da
resposta (1 MiB), como o texto do modelo, e truncá-lo quebrava a carga opaca.

Medido a 2026-10-03 contra o LiteLLM de produção (alias `gpt-4o-mini` → `kimi-for-coding`): o
raciocínio chega na resposta e sobrevive no pedido seguinte, e **não é exigido** por este provider
— o turno nativo sem ele foi aceite. Os blocos de raciocínio assinados e os itens cifrados de
outros fornecedores ficam fora desta decisão; o contrato fica preparado para carga opaca.

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

## 4. Consequências

- O modelo vê as suas próprias chamadas e a que chamada responde cada resultado.
- Os argumentos de uma tool call passam a ir no prompt de todos os turnos seguintes, com tecto de
  tamanho. Se contiverem dados pessoais ou segredos, são reenviados ao provider que sirva o turno.
- Com os argumentos ao lado do código de recusa, conteúdo injectado pode sondar a fronteira da
  política argumento a argumento. A `Reason` continua fora.
- O preâmbulo custa cerca de 280 tokens de entrada por turno na projecção de texto único (1 118
  bytes, a 4 bytes por token). O protocolo nativo custa cerca de 314 (1 256 bytes).
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
- **Argumentos que não são JSON.** Vão crus em `function.arguments`, como o modelo os emitiu. Um
  provider que valide esse campo recusa o pedido seguinte; não foi medido.
- **Run sem objectivo nem entradas.** A projecção nativa dá um pedido só com `system`; se o
  provider o aceita não foi medido.
