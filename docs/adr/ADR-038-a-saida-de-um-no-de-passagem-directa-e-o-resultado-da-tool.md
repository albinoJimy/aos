# ADR-038 — A saída de um nó de passagem directa é o resultado da tool, por referência

- **Estado:** Aceite
- **Data:** 2026-10-05
- **Deciders:** Dono do produto (decisões de 2026-10-05: a origem declara-se e não se infere; sem
  origem designável o run não cumpre e nunca se entrega o texto do modelo; entrega-se o resultado
  tal como a tool o devolveu; os bytes passam pelo `aos-orq`; o texto final do produtor não é
  publicado; o vínculo da declaração é por run; depois da revisão adversarial de AOS-497, na
  mesma data: contam-se as chamadas pedidas e não as efectivas, o run com entradas tem estado
  próprio, a forma do nome valida-se no arranque, e a fonte dos bytes é o step-ledger — §8) ·
  executor de AOS-497 (implementação da §2.2 à §2.5)
- **Tickets:** AOS-497 (o kernel designa e sela a origem). As partes que ficam para AOS-498,
  AOS-499, AOS-500 e AOS-501 estão marcadas em cada secção e resumidas na §6.
- **Relacionados:** ADR-001 (execução durável ao nível do passo), ADR-002 (Reference Monitor),
  ADR-005 (taint), ADR-010 (manifesto por trajectória e replay), ADR-022 (extensões ao grafo de
  plano), ADR-027 (cada nó do plano é um run do nó), ADR-034 (autorização derivada do contexto),
  ADR-036 (o tail é a forma canónica da conversa), ADR-037 (o desfecho de um run é um veredicto
  do kernel)

## 1. Contexto

A saída de um nó de um plano é hoje o texto final do modelo. O resultado real da tool fica
selado no run (na captura do turno e, na via durável, no step-ledger) e não sai dele pelo caminho
do plano: o `aos-orq` publica o texto final e é esse texto que o nó seguinte recebe.

O veredicto do ADR-037 conta chamadas efectivas por tool do contrato e, por desenho, não olha
para o texto. Prova que a tool correu; não prova que a saída é o que ela devolveu. É o resíduo
«evidência de tool call não é fidelidade da saída» do ADR-037 §5.

Medido em produção a 2026-10-05 (v0.1.46, série de 21 planos): em dois planos o nó de leitura
chamou a tool com êxito e escreveu um resumo em vez do conteúdo. Perderam-se factos do documento
(num, um número; no outro, um número e uma tarefa), o nó seguinte herdou a perda, e o veredicto
deu «cumprido». Na mesma série o nó de leitura declarou a saída como `record` em 17 planos,
`summary` em 3 e `artifact` em 1: o tipo da saída não diz se ela é o resultado da tool.

Não havia um digest do resultado amarrado ao desfecho do run. O único digest do resultado em
claro era o `result_hash` do step-ledger, que só existe na via durável e não diz qual chamada é
«a» saída.

## 2. Decisão

### 2.1 A origem declara-se; não se infere

Uma saída é por referência se, e só se, quem compõe o run o declara. No plano, a declaração é um
campo da saída (`outputs[].from_tool`, o nome de uma tool do mesmo nó; schema 1.3.0), entra no
`contract_digest` do contrato aprovado e aparece no cartão de aprovação. **Fica para AOS-500.**

A inferência a partir da estrutura do nó (não-verificador, uma tool, uma saída aberta, sem
`consumes`) serve só para medir (AOS-499): decidir por ela mudaria o conteúdo que atravessa uma
aresta de um plano aprovado sem que ninguém o tivesse aprovado. O tipo da saída também não serve
de sinal (§1).

No run, a declaração é o campo `Goal.OutputFromTool` — o nome exacto da tool — acompanhado do
vínculo da §2.4. **Implementado em AOS-497.** O campo correspondente no `POST /runs`, validado
contra a lista-branca do mesmo pedido e anunciado no `GET /tools`, **está implementado
(AOS-498)**: `output_from_tool` e `output_source_binding`, que vêm sempre juntos. A porta recusa
com 400 o que se decide sem o tool set (declaração incompleta, vínculo fora do vocabulário, nome
que o selo recusaria, tool fora da lista-branca); o que depende do tool set continua a ser do
kernel, no arranque. Nenhum chamador de produção envia ainda os campos.

**Uma declaração impossível não arranca.** Antes do primeiro turno o kernel verifica que a tool
declarada consta do tool set do run e, havendo lista-branca, está nela — a regra do contrato
impossível do ADR-037 §2.2, com comparação exacta de nomes. Se não, o run é recusado
(`ErrImpossibleOutputSource`), sem eventos e sem interrogar o modelo. Uma declaração sem vínculo,
com um vínculo desconhecido, ou um vínculo sem tool, também recusa o arranque
(`ErrBadOutputSourceBinding`). Vale nos dois vínculos; com o veredicto desligado (`off`) a
declaração não é lida, como o contrato.

**A forma do nome valida-se no arranque, com a função do selo.** O nome da tool de origem vai
para um evento em claro (§2.3), e a máquina de estados só aceita uma âncora cujo nome tenha até
128 bytes e não traga espaços nem caracteres de controlo. A mesma função responde nas duas
pontas: um nome que o selo recusaria recusa o arranque (`ErrImpossibleOutputSource`, sem repetir
o nome na mensagem), mesmo que a tool exista no tool set com esse nome. Sem isto, um run com um
nome assim corria, terminava em memória e a transição terminal era recusada — ficava em
`running` no log, indistinguível de um crash. Uma âncora que um run produza é sempre selável.

O nó sela um run recusado no arranque em `failed`, e o `GET /runs/{id}` responde `failed` com
`terminated=false` enquanto o desfecho vive em memória, como para o contrato impossível.

### 2.2 A regra de designação

Qual chamada é «a» origem decide-se no kernel, sem ler texto nem conteúdo:

> No **primeiro turno do run que despachou tool calls**, e só se o contexto desse turno era
> **trusted**: se a tool declarada foi **pedida exactamente uma vez** e essa chamada foi
> **efectiva** (despachada, sem recusa e sem erro de tool), o estado é `designated`. Se foi
> pedida **mais de uma vez** nesse turno, qualquer que seja o desfecho de cada chamada, é
> `ambiguous`. Se foi pedida uma vez e a chamada não foi efectiva, se não foi pedida, ou se
> nenhum turno despachou tool calls, é `missing`. Se o contexto desse turno já era untrusted, é
> `inapplicable`.

- **Contexto trusted** é o rótulo de autoridade do ADR-034, lido a seguir à montagem do prompt
  do turno: só o prefixo, o objectivo, uma correcção de steer ou um aviso do runtime entraram no
  tail. É o que garante que os argumentos da chamada foram escolhidos por um modelo que ainda não
  tinha visto conteúdo de terceiros. Um documento que diga «lê também o documento X» provoca uma
  segunda leitura, pedida depois de um resultado de tool; essa nunca é a origem.
- **O primeiro turno que despachou tool calls** é o primeiro em que o loop despachou pelo menos
  uma chamada, de qualquer tool e com qualquer desfecho. As duas condições são verificadas em
  separado.
- **Várias chamadas no turno.** As de outras tools não contam. Duas chamadas da tool declarada
  dão `ambiguous`: não se escolhe nem se concatena, porque seria o runtime a compor conteúdo e o
  digest deixava de ser o de um resultado gravado.
- **Contam-se as chamadas pedidas, não as efectivas.** Uma recusada e uma efectiva, ou uma
  falhada e uma efectiva, dão `ambiguous`. As duas foram pedidas pelo modelo em contexto trusted,
  pelo que um atacante não escolhe os argumentos de nenhuma; mas se só contassem as efectivas,
  quem controla a falha de uma chamada (um servidor remoto que devolve erro, um recurso que a
  política nega) convertia um turno ambíguo em `designated` e escolhia qual dos dois resultados
  atravessa a aresta, com selo. A regra não deixa o desfecho de uma chamada decidir qual é a
  origem.
- **Um run com `plan_input` ou memória** tem o contexto untrusted desde o turno 1 e nunca tem
  origem. Como um turno sem tool calls termina o run, antes do primeiro turno com tools não há
  resultado de tool no tail: o que tornou o contexto untrusted foram as entradas do run. O estado
  é `inapplicable` — um defeito de quem compôs o run (um consumidor declarado como produtor), que
  a medição não confunde com «o modelo não chamou» (`missing`). O kernel **não recusa o
  arranque** por isto: em «só medição» mudaria o desfecho de um run que hoje conclui. O validador
  do plano recusará `from_tool` num nó com `consumes` (AOS-500); o kernel é a segunda linha. Um
  run com entradas em que nenhum turno despachou tools fica `missing`: não houve turno de
  designação.
- **Uma chamada servida pelo step-ledger** (um resultado memorizado, numa retoma) conta como
  qualquer outra, uma vez, com os bytes memorizados.

A designação é a mesma função no loop e no motor de replay, alimentada das mesmas entradas que o
tail e a captura do turno. Os factos — quantas vezes a tool foi pedida, o desfecho da chamada e
o digest dos seus bytes — tiram-se **no momento em que o turno é observado**, e não no turno
terminal: os bytes do resultado são partilhados com o tail e a captura, e nada do que lhes
aconteça depois muda a âncora. Num run sem origem declarada nenhum resultado é lido.
**Implementado em AOS-497.**

### 2.3 A âncora

Num run que declarou a origem, o kernel devolve e o nó sela na transição terminal, ao lado do
veredicto, a **âncora** (`output_source`):

| Campo | Conteúdo |
|---|---|
| `tool` | A tool declarada |
| `binding` | O vínculo da declaração (§2.4) |
| `state` | `designated`, `missing`, `ambiguous` ou `inapplicable` |
| `step_id` | O passo da chamada designada: o `step_id` dos seus eventos de mediação. Só em `designated` |
| `digest` | `sha256:<hex>` dos bytes do resultado. Só em `designated` |
| `bytes` | O tamanho do resultado. Só em `designated`; ausente quer dizer zero |

O digest calcula-se sobre os bytes do resultado **tal como o despacho os devolveu nesta vida do
run**, sem rótulos, delimitadores nem escape. É calculado por quem executou a tool, e não por
quem transporta. A âncora não leva conteúdo do titular.

**A fonte dos bytes.** Na via durável, o digest selado é o `result_hash` do `step.ledger.applied`
do passo designado (`"sha256:" + result_hash`, chave `<run_id>:<step_id>`): o step-ledger grava o
resultado de cada chamada com êxito e é dele que uma retoma o volta a servir. A captura do turno
só contém esses bytes quando o turno designado **não mudou de desfecho entre vidas**. Em dois
casos muda, e a captura fica a da primeira vida (a re-captura do turno é um duplicado):

- a chamada **escala** na primeira vida e executa depois da aprovação, numa retoma — a captura
  tem a escalada, sem resultado;
- a chamada **falha** na primeira vida e tem êxito depois de uma retoma — a captura tem a falha.

Nos dois a âncora é `designated` e o digest confere com o step-ledger, não com a captura. Por
isso **quem serve os bytes (AOS-498) lê-os de onde o digest confere — o step-ledger, na via
durável — e confere sempre `sha256(bytes)` contra o digest selado; se não conferir, não serve.**
Reconstruí-los da captura falhava a conferência, para sempre, em todo o nó cuja leitura precise
de aprovação humana. A igualdade entre o `result_hash` e o digest selado está fixada por teste
nos três cenários (uma vida; escalada e aprovação; falha e retoma). Fora da via durável não há
step-ledger: um nó sem execução durável não tem de onde servir os bytes de um run retomado.

A âncora existe em qualquer modo de aplicação do veredicto excepto `off`, nos dois vínculos, e
com qualquer estado. A máquina de estados só a aceita numa transição que termina o run e na
forma que o kernel produz (vocabulários fechados, digest `sha256:` com 64 dígitos hexadecimais,
campos da chamada só em `designated`); de outro modo rejeita a transição sem tocar no log.
**Implementado em AOS-497.**

### 2.4 O vínculo é por run, e as razões novas só valem com ele

A declaração leva um **vínculo**, dado pelo chamador e fixado por run
(`Goal.OutputSourceBinding`):

- `measure` (só medição): a âncora calcula-se e sela-se. A designação não entra no veredicto: o
  veredicto e o desfecho de um run **que arranca** são os de um run sem declaração, qualquer que
  seja o modo do nó. O estado da âncora conta-se em
  `aos_runs_output_source_total{binding,state}`.
- `binding` (vinculativa): com o nó em imposição (`enforce`), uma origem que não ficou designada
  é veredicto negativo e o run termina `failed`. Com o nó em observação comporta-se como
  `measure`.

**«Só medição» não é estritamente neutra.** Uma declaração impossível ou mal formada (§2.1: tool
fora do tool set ou da lista-branca, nome que o selo recusa, vínculo fora do vocabulário) recusa
o arranque **nos dois vínculos** — um run que sem a declaração teria corrido não corre. É por
desenho: medir sobre uma tool que o run não pode chamar contava um defeito de composição como se
fosse do modelo. A consequência é para quem declara: o `aos-orq`, ao medir (AOS-499), tem de
garantir que a tool declarada está no tool set do nó e na lista-branca do pedido. Um run recusado
assim não conta em `aos_runs_output_source_total` (não tem âncora); vê-se em
`aos_runs_finished_total{outcome="failed"}` e no erro do run.

O vínculo existe porque o modo de aplicação é do nó (ADR-037 §2.6) e a produção está em
imposição desde 2026-10-05: se as razões novas seguissem só o modo do nó, medir a designação em
produção obrigava a falhar runs. Com o vínculo, o `aos-orq` mede com `measure` (AOS-499) e só
entrega por referência com `binding` (AOS-501).

O vocabulário do veredicto (ADR-037 §2.4) ganha duas razões, e a precedência fica:

| Ordem | `outcome_reason` | Quando |
|---|---|---|
| 1 | `truncated` | O turno terminal parou com o motivo `length` |
| 2 | `contract_unmet_no_call`, `contract_unmet_after_denial`, `contract_unmet_after_tool_error` | O contrato de conclusão não está cumprido |
| 3 | `output_source_ambiguous`, `output_source_missing` | Declaração vinculativa em imposição, e a origem não ficou designada: `ambiguous` dá a primeira; `missing` e `inapplicable` dão a segunda (não há terceira razão — a âncora distingue-os) |
| 4 | `empty_output` | Declaração vinculativa em imposição: a origem designada tem zero bytes. Nos outros casos: o turno terminal não trouxe texto |

O contrato vem antes da origem porque diz porquê não há chamada efectiva (nunca pedida,
recusada, falhada); as razões da origem aparecem quando há chamada efectiva, ou nenhum contrato
a exige, e mesmo assim nenhuma é designável. Num run vinculado em imposição a saída é o
resultado designado: `empty_output` avalia os seus bytes e deixa de avaliar o texto final.

Nunca há queda para o texto do modelo. **Implementado em AOS-497.**

### 2.5 O replay e a retoma reproduzem sem ler configuração

A origem e o vínculo ficam gravados onde o contrato de conclusão fica: no manifesto de cada turno
(`manifest.completion.output_from` e `output_binding`) e no registo de retoma. O motor de replay
lê-os do manifesto do turno terminal, refaz os factos do primeiro turno com tools a partir da
captura (em claro ou decifrada por-titular) e chega à mesma âncora: o mesmo estado, o mesmo passo
e o mesmo digest. O `prompt_hash` não muda com a declaração: o modelo não é avisado.

Numa retoma (por aprovação ou depois de um crash) o run é re-hospedado desde o turno 1 e a
designação recalcula-se. Um resultado com êxito está memorizado no step-ledger e reproduz os
mesmos bytes; a tool não volta a correr e o digest selado é o da chamada da primeira vida. Uma
chamada que na vida anterior escalou ou falhou volta a ser despachada, e o digest selado é o dos
bytes desta vida — os do step-ledger (§2.3, «A fonte dos bytes»).

Todos os campos novos são omitidos quando vazios: um run sem origem declarada grava os bytes de
antes, e um log gravado antes desta decisão reproduz-se sem âncora. **Implementado em AOS-497.**

### 2.6 O transporte: os bytes passam pelo `aos-orq`, com a âncora no kernel

O `GET /runs/{id}` de um run que declarou a origem devolve a âncora e os bytes designados, lidos
do step-ledger e conferidos contra o digest selado antes de saírem (§2.3, «A fonte dos bytes»); o
`aos-orq` confere `sha256(bytes)` contra o digest selado, publica-os como payload do nó e
entrega-os ao consumidor pelo canal `inputs` que já existe. O evento `plan.payload_published`
ganha a origem (`source`: tipo, tool e passo), derivada do contrato aprovado, e o seu digest
passa a ser o selado pelo kernel. O nó consumidor não muda: recebe um `plan_input`, untrusted,
com os tectos de hoje (128 KiB por payload, 512 KiB no conjunto). Um resultado acima do tecto ou
que não seja texto válido não se transporta, e o produtor falha com causa própria; nada é
truncado.

Quem lê, com que autorização e por que canal ficam como estão: a leitura do desfecho pelo gate
soberano, depois do selo WORM. O que muda é de onde vêm os bytes e a que se amarra o digest.
**A leitura e a resposta do nó estão implementadas (AOS-498); a publicação e a entrega ficam
para AOS-501.**

O que o AOS-498 fixou ao implementar a leitura:

- os bytes lêem-se do step-ledger nos **dois** ramos do `GET` (em memória e durável): o registo
  de desfechos em memória só tem a âncora;
- `output` só sai num run **concluído**; um run `failed` com âncora `designated` responde com os
  metadados;
- a falta dos bytes manifesta-se conforme o vínculo — com `measure` nunca muda o que a resposta
  diz do run (ADR-037 §2.8, emenda de AOS-498);
- o nó anuncia o suporte no `GET /tools` (`output_source`, com os vínculos e o tecto), e não o
  anuncia com o veredicto desligado.

### 2.7 Entrega-se o resultado tal como a tool o devolveu

O consumidor recebe os bytes do resultado sem transformação. Para uma tool com sandbox é o
envelope (`{"stdout_text": …, "exit_code": …}`), que é o que o modelo do produtor lê. Desembrulhar
punha o sistema a interpretar conteúdo untrusted e a entregar bytes que nenhum digest selado
cobre. A rever com a medição de qualidade do consumidor (AOS-501).

### 2.8 O texto final do produtor não é publicado

A regra de terminação não muda: o modelo dá o turno final, o texto é capturado e selado, e
continua disponível como texto final do run. Para uma saída por referência não é publicado nem
entregue ao consumidor, nem como comentário: seria repor no caminho conteúdo escrito por um
modelo que já leu untrusted. Fica para auditoria. **Fica para AOS-501.**

Custo conhecido: o modelo continua a transcrever o documento para um texto que ninguém consome.
Avisá-lo no objectivo, ou concluir o run logo a seguir à chamada designada, ficam fora desta
decisão; a segunda mexe na regra única de terminação.

### 2.9 Sem origem designável, o produtor falha com causa

Independentemente do modo do nó, o `aos-orq` não publica uma saída por referência sem uma âncora
`designated` e sem bytes que batam com o digest, e fecha o nó do plano com causa em vocabulário
fechado. Um nó `aos` que não anuncie a capacidade faz o `serve` recusar um plano que a declare;
não cai para o texto. **Fica para AOS-501.**

## 3. Alternativas rejeitadas

- **O nó consumidor resolve a referência** (o payload leva só a referência; o nó lê a captura do
  run produtor, decifra, confere e monta o `plan_input`). É a direcção certa a prazo: os bytes
  deixam de passar pelo `aos-orq` e o tecto do corpo do pedido deixa de contar. **Rejeitada
  agora** porque põe o nó a decifrar conteúdo de um titular por conta de uma referência escrita
  num corpo de pedido, dentro do caminho de admissão (*confused deputy*): cada verificação
  esquecida ali é conteúdo de um run a entrar noutro que o chamador depois lê. Merece ADR próprio
  e não é precisa para fechar o defeito medido. **Gatilhos para a reabrir:** o primeiro payload
  legítimo acima do tecto; a fase A5 ou A6 da arquitectura-alvo; a reabertura do DEF-806. As
  peças desta decisão que não dependem do transporte (a declaração, a designação, a âncora, a
  origem no evento do plano) servem as duas.
- **Inferir a passagem directa do tipo da saída** (`record`). Falha na série medida, e parte os
  nós que transformam e declaram `record`, que é para o que o tipo foi definido.
- **Inferir da estrutura do nó.** Um nó «lê e extrai as acções» tem a mesma estrutura de um nó
  de leitura e passaria a entregar o documento cru. Fica como medição.
- **Um tipo de saída novo** (`tool_result`). Com mais de uma tool no nó não diz qual, e mistura
  forma com origem.
- **O consumidor vai buscar o conteúdo com as suas tools.** Devolve a decisão ao modelo, num
  contexto que já é untrusted.
- **O runtime repete a tool call no consumidor.** Reexecuta um efeito, o conteúdo pode ter
  mudado, e seria uma chamada mediada que nenhum modelo pediu.
- **Publicar na memória.** A memória não está ligada em produção e é fail-closed na autoridade.
- **Designar a última chamada da tool, ou a de qualquer turno.** Uma chamada pedida depois de
  conteúdo untrusted pode ter os argumentos escolhidos por esse conteúdo.
- **Concatenar várias chamadas.** O digest deixava de ser o de um resultado gravado.
- **As razões novas seguirem só o modo do nó.** Medir em produção obrigava a falhar runs (§2.4).
- **Cair para o texto do modelo quando não há origem.** Publicava texto sob um contrato que
  promete o resultado da tool: o verde falso que esta decisão fecha.

## 4. Consequências

- Um run sem origem declarada não muda: os mesmos eventos, o mesmo manifesto, o mesmo registo de
  retoma, o mesmo desfecho. Fixado por uma fixture gravada antes da implementação.
- Para um nó de passagem directa, o resíduo «evidência não é fidelidade» do ADR-037 passa a ser
  uma igualdade de digests — quando a entrega existir (AOS-501). Com AOS-497 existe a âncora;
  nada é ainda entregue por ela.
- O `/metrics` do nó ganha `aos_runs_output_source_total{binding,state}` (8 séries: 2 vínculos ×
  4 estados; só conta runs cujo desfecho ficou selado) e duas razões em
  `aos_runs_finished_total`. As razões novas chegam também aos contadores do `aos-orq`
  que enumeram o vocabulário do kernel; um `aos-orq` anterior lê uma razão que não conhece como
  `razao_desconhecida`.
- Mais um digest de conteúdo do titular em claro, na transição terminal do run. O step-ledger já
  tem em claro o `result_hash` do mesmo resultado; depois de um apagamento por crypto-shredding o
  digest sobrevive, como esse já sobrevive.
- O kernel passa a escolher um resultado. A escolha não depende de conteúdo untrusted: é o que a
  regra do contexto trusted garante.
- `Machine.RebuildOutcome` devolve a âncora com o veredicto, do mesmo evento.
- Com a declaração vinculativa em imposição haverá mais runs `failed`, com causa nomeada onde
  hoje há um verde (por vezes falso): duas leituras no mesmo turno (mesmo que uma falhe), a tool
  chamada só depois de outra, um nó com `consumes`. A medição com `measure` conta-os antes de se
  impor, e separa o último (`inapplicable`) dos que são do modelo.

## 5. Resíduos e limites declarados

- **Uma falha no primeiro turno com tools tira a origem ao run.** Se a tool declarada falha ou é
  recusada no primeiro turno que despachou tool calls e tem êxito num turno posterior, o estado
  é `missing`: o resultado da primeira tentativa já tornou o contexto untrusted, e a segunda
  chamada nunca é origem. O mesmo quando o primeiro turno com tools chama outra tool. É uma falha
  honesta e conta contra a taxa de «não cumprido»; mede-se antes de impor.
- **A chamada errada.** A fidelidade é ao resultado da chamada feita. Se o modelo leu o documento
  errado, a origem é esse resultado. O `step_id` liga ao evento de mediação do mesmo passo, onde
  um auditor vê o recurso.
- **O run que não chama a tool** continua vermelho, agora com causa própria.
- **A tool devolver dados errados ou desactualizados** não é visível aqui.
- **Nós que transformam** (resumir, classificar, extrair). O modelo continua no caminho. Só um
  verificador com a fonte autêntica o fecha, e a saída por referência é o que lhe dá a fonte.
- **Agregações** (duas leituras num nó) são `ambiguous`.
- **Uma nova tentativa no mesmo turno é `ambiguous`.** Um modelo que peça duas vezes a mesma
  leitura no primeiro turno, com uma a falhar, deixa de ter origem. É o preço de o desfecho de
  uma chamada não escolher a origem (§2.2); mede-se antes de impor.
- **Um envelope de sandbox com código de saída diferente de zero é uma chamada efectiva.** Para o
  runtime, «efectiva» é despachada, sem recusa e sem erro de tool; uma tool com sandbox que
  termina com `exit_code` diferente de zero devolve o envelope sem erro de tool. Essa chamada é
  designável e o envelope — com o `stderr` e o código — é o que fica selado e o que seria
  entregue. Decidir se um envelope de falha se entrega é do AOS-501.
- **Bytes designados e texto final vazio.** Com `binding` em imposição, a saída vazia avalia os
  bytes designados: um run com origem designada, bytes presentes e texto final vazio é
  **cumprido**, com `FinalText` vazio. O `aos-orq` de hoje fecha `failed` um nó de saída aberta
  com texto vazio; o tratamento do lado do `aos-orq` é do AOS-501.
- **O `step_id` da âncora identifica a chamada, não um evento.** Num run retomado o mesmo passo
  pode ter mais de um evento de mediação (uma escalada e, depois da aprovação, a mediação que
  executou). Quem audita lê o último permit desse passo.
- **A captura não tem os bytes de um turno designado que mudou de desfecho entre vidas** (§2.3,
  «A fonte dos bytes»). Tem-nos o step-ledger, e só na via durável.
- **Resultados acima do tecto ou que não são texto** falham; fecham-se com a alternativa
  rejeitada agora.
- **A separação de planos (DEF-806) não fecha.** O conteúdo untrusted continua em linha no tail
  do consumidor. Tira-se o modelo do caminho do produtor; não se cria um handle. O consumidor
  passa a receber os bytes exactos do atacante, no envelope cru: as defesas são as de hoje
  (`plan_input` é dados, autoridade untrusted, gate de taint), e têm de ser exercitadas por este
  caminho antes de se ligar a entrega.
- **Em `off` não há âncora.** A declaração vive com o contrato em `manifest.completion`, que não
  existe com o veredicto desligado. Um nó em `off` não serve saídas por referência.
- **Em observação, uma declaração vinculativa não deixa razão no veredicto.** O que a imposição
  teria feito lê-se no estado da âncora, não em `outcome_reason`.
- **A captura em modo de referência não reproduz o digest, e nem sempre o diz.** Sem cifra
  por-titular e com a guarda de segredos ligada, a captura guarda só o hash do resultado e o
  replay devolve um marcador em vez dos bytes. Quando há um turno a seguir ao designado, a
  reprodução diverge nele (o `prompt_hash` não bate) e não chega a dar âncora. Quando o turno
  designado é também o **terminal** (uma resposta final que ainda traz tool calls), não há turno
  seguinte onde divergir: o replay sai fiel, sem divergência visível, com uma âncora de digest e
  tamanho **diferentes** dos selados (os do marcador). Não é o caminho de produção: o nó sela
  por-titular, e o gateway só dá uma resposta final sem tool calls. Quem comparar a âncora de um
  replay com a selada tem de saber em que modo a captura foi feita.
- **Um run retomado cujo turno re-executado mudou de desfecho** tem a âncora da vida que
  terminou, e o replay pára em divergência sem a reproduzir (a classe do ADR-037 §5).

## 6. O que cada ticket implementa

| Parte | Secção | Ticket |
|---|---|---|
| Declaração e vínculo no objectivo do run; recusa da declaração impossível | §2.1, §2.4 | AOS-497 |
| Regra de designação, igual no loop e no replay | §2.2 | AOS-497 |
| Âncora no desfecho do run e na transição terminal | §2.3 | AOS-497 |
| Razões novas e precedência; métrica do estado da âncora | §2.4 | AOS-497 |
| Manifesto, registo de retoma, paridade do replay | §2.5 | AOS-497 |
| Campo no `POST /runs`, âncora e bytes no `GET /runs/{id}`, anúncio no `GET /tools` | §2.1, §2.6 | AOS-498 |
| Medição pelo `aos-orq` com o vínculo `measure`, sem mudar a entrega (implementado: interruptor `AOS_ORQ_SAIDA_POR_REFERENCIA=observe`; só declara num pedido que já leva o contrato sobre a mesma tool) | §2.4 | AOS-499 |
| `outputs[].from_tool` no plano, schema 1.3.0, validador, cartão de aprovação | §2.1 | AOS-500 |
| Publicação e entrega pelos bytes designados; origem no evento do plano; texto final não publicado; causas de falha; prompt do planeador | §2.6 a §2.9 | AOS-501 |

## 7. Emendas a outros ADR

- **ADR-037 §2.4** ganha as duas razões e a precedência da §2.4 acima, e **§5** remete para
  aqui no resíduo «evidência não é fidelidade». Feitas com AOS-497.
- **ADR-037 §2.8** (o que a leitura de desfecho cobre, quando passa a servir um resultado de
  tool) foi emendada com AOS-498, que é quem muda essa leitura.
- **ADR-027 §2.4** (a origem do payload publicado) é emendado com AOS-501, e **ADR-022 §2.3** (a
  origem declarada de um output) com AOS-500. AOS-497 não muda o que esses dois descrevem: nenhum
  payload é ainda publicado por referência e o plano ainda não tem o campo.

## 8. O que a revisão adversarial de AOS-497 mudou

A revisão independente de 2026-10-05, feita antes de existir qualquer chamador e qualquer âncora
selada, não encontrou bloqueantes e levou a estas alterações, decididas pelo dono do produto no
mesmo dia. Mudar a regra depois de haver âncoras seladas mudaria o significado do selo; por isso
entraram com AOS-497.

| Achado | O que mudou | Secção |
|---|---|---|
| Um nome de tool que o tool set aceita e o selo recusa deixava o run sem transição terminal | A forma do nome valida-se no arranque, com a função do selo | §2.1 |
| O `GET /runs/{id}` em memória respondia `completed` a um run recusado no arranque | Responde `failed`, como o log durável | §2.1 |
| «Exactamente uma efectiva» deixava quem controla a falha de uma chamada escolher a origem | Contam-se as chamadas pedidas: mais de uma é `ambiguous` | §2.2 |
| Um run com entradas contava como `missing`, igual a «o modelo não chamou» | Estado próprio, `inapplicable`; fecha com `output_source_missing` | §2.2, §2.4 |
| Num run retomado com mudança de desfecho, os bytes designados não estão na captura | A fonte dos bytes é o step-ledger; quem serve confere sempre | §2.3, §2.6 |
| O digest calculava-se no turno terminal, sobre bytes partilhados | Calcula-se quando o turno é observado | §2.2 |
| «Só medição» descrita como neutra | Declarado: uma declaração impossível ou mal formada recusa o arranque nos dois vínculos | §2.4 |
| Limites em falta | Captura de referência com turno terminal designado; envelope de sandbox com saída diferente de zero; `step_id` num run retomado; texto final vazio com bytes designados | §5 |
