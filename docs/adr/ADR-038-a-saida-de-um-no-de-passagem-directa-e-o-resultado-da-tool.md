# ADR-038 — A saída de um nó de passagem directa é o resultado da tool, por referência

- **Estado:** Aceite
- **Data:** 2026-10-05
- **Deciders:** Dono do produto (decisões de 2026-10-05: a origem declara-se e não se infere; sem
  origem designável o run não cumpre e nunca se entrega o texto do modelo; entrega-se o resultado
  tal como a tool o devolveu; os bytes passam pelo `aos-orq`; o texto final do produtor não é
  publicado; o vínculo da declaração é por run; depois da revisão adversarial de AOS-497, na
  mesma data: contam-se as chamadas pedidas e não as efectivas, o run com entradas tem estado
  próprio, a forma do nome valida-se no arranque, e a fonte dos bytes é o step-ledger — §8;
  decisões de 2026-10-06: de um envelope da sandbox entrega-se só o texto do documento, depois de
  conferir o envelope inteiro; uma tool que falha ou devolve vazio fecha o nó; qualquer tool do nó
  pode ser origem — §2.7 e §10) · executor de AOS-497 (implementação da §2.2 à §2.5)
- **Tickets:** AOS-497 (o kernel designa e sela a origem), AOS-498 (a declaração, a âncora e os
  bytes na API do nó) e AOS-499 (o `aos-orq` mede com o vínculo «só medição»), os três
  implementados e revistos; AOS-500 (a declaração no plano: `outputs[].from_tool`, schema 1.3.0,
  validador e cartão de aprovação), implementado e revisto; AOS-501 (a publicação e a entrega
  pelos bytes designados, atrás do interruptor `AOS_ORQ_SAIDA_POR_REFERENCIA=on`), implementado e
  revisto; a entrega só liga por decisão do dono, e está por verificar em produção — §10 e §11.
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
`contract_digest` do contrato aprovado e aparece no cartão de aprovação. **Implementado em
AOS-500** (emenda o ADR-022 §2.3). O validador do plano recusa, com um sub-código por caso, a
tool que não é do nó, a saída que não é `record` nem `artifact`, o nó verificador, o nó com
`consumes`, a segunda saída com origem no mesmo nó e a tool que o nó refere mais de uma vez;
usar o campo obriga a carimbar a linha 1.3.0. Um contrato sem o campo mantém a forma canónica e
o `contract_digest`. **Fora de `on` nenhum plano corre com o campo** (o interruptor da §10 nasce
desligado): o planeador não é instruído a emiti-lo, e o `aos-orq` trata a linha 1.3.0 do plano
— o documento com o campo, ou só com o carimbo — como o binário anterior a tratava: vinda do
planeador (`--goal`), é uma tentativa recusada, e o planeador tenta de novo; lida do disco
(`--plan-doc`, materialização, re-verificação de um pendente, `decide`), é recusada com a causa
`origem_sem_entrega`. Com `on` e um nó que anuncia o vínculo vinculativo, o planeador recebe o
prompt 1.5.0, que nomeia o campo, e o plano corre (AOS-501).

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
  do plano recusa `from_tool` num nó com `consumes` (AOS-500); o kernel é a segunda linha. Um
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

**O vínculo com que um run foi pedido fica no log do plano** (AOS-501): o `aos-orq` escreve o
facto `plan.output_source_declared` (nó, saída, tool, vínculo, digest do contrato) antes do
pedido ao nó. Quem recolhe o run — o mesmo processo ou outro — só entrega por referência com
esse facto a dizer `binding` para a tool do contrato **e** com a âncora selada a dizer o mesmo.
Um run pedido em «só medição» não se entrega por referência, qualquer que seja o interruptor de
quem o recolhe.

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
`aos-orq` confere `sha256(bytes)` e o tamanho contra a âncora selada, deriva deles o que se
entrega (§2.7), publica-o como payload do nó e entrega-o ao consumidor pelo canal `inputs` que
já existe. O evento `plan.payload_published` ganha a origem (`source`), e o seu `record.digest`
é o do que foi **entregue**:

| Campo de `source` | Conteúdo |
|---|---|
| `kind` | `tool_result`. Derivado do contrato aprovado |
| `tool` | A tool do `from_tool` do contrato. Derivada, nunca aceite de quem publica |
| `step_id` | O passo da chamada designada, tal como a âncora o traz. Validado na forma |
| `anchor_digest`, `anchor_bytes` | O digest e o tamanho dos bytes que o kernel selou: o resultado inteiro da tool |
| `extraction` | Como o entregue se deriva deles: `raw` ou `sandbox_stdout_text` (§2.7) |

A regra é simétrica e imposta na construção do evento: um contrato com `from_tool` obriga a
`source`; sem ele, `source` é proibido. Com `raw`, `record.digest` tem de ser igual a
`anchor_digest`. O evento continua sem conteúdo.

O nó consumidor não muda: recebe um `plan_input`, untrusted, com os tectos de hoje (128 KiB por
payload, 512 KiB no conjunto). Um resultado acima do tecto ou que não seja texto válido não se
transporta, e o produtor falha com causa própria; nada é truncado. O tecto de 128 KiB do nó
aplica-se aos bytes designados — o envelope —, e o do `aos-orq` ao que é entregue.

Quem lê, com que autorização e por que canal ficam como estão: a leitura do desfecho pelo gate
soberano, depois do selo WORM. O que muda é de onde vêm os bytes e a que se amarra o digest.
**A leitura e a resposta do nó estão implementadas (AOS-498); a publicação e a entrega estão
implementadas (AOS-501), atrás do interruptor da §10.**

O que o AOS-498 fixou ao implementar a leitura:

- os bytes lêem-se do step-ledger nos **dois** ramos do `GET` (em memória e durável): o registo
  de desfechos em memória só tem a âncora;
- `output` só sai num run **concluído**; um run `failed` com âncora `designated` responde com os
  metadados;
- a falta dos bytes manifesta-se conforme o vínculo — com `measure` nunca muda o que a resposta
  diz do run (ADR-037 §2.8, emenda de AOS-498);
- o nó anuncia o suporte no `GET /tools` (`output_source`, com os vínculos e o tecto), e não o
  anuncia com o veredicto desligado;
- a leitura do step-ledger só devolve conteúdo que o opener por-titular abriu, e o opener só
  abre atrás do escopo do leitor que o gate admitiu: um registo em claro não sai, e sem leitor
  autenticado na mão nada se abre (§9);
- o que não se lê por causa do log — um registo do ledger ilegível, uma âncora cujo passo não
  forma chave de idempotência, um registo em claro — é **definitivo**, como o passo sem registo
  e os bytes que não conferem. Só a custódia fechada e o erro de leitura do Event Store são
  transitórios (§9).

### 2.7 O que se entrega: o texto do documento, depois de conferir o envelope inteiro

**Decisão do dono de 2026-10-06, que substitui a de 2026-10-05** («entrega-se o resultado tal
como a tool o devolveu»). A leitura de um ficheiro na sandbox devolve um envelope que leva o
documento duas vezes — em `stdout_text` e outra vez, em base64, em `artifacts` —, e entregá-lo
inteiro dava ao consumidor mais do dobro dos tokens e a moldura de uma execução no lugar de um
documento. A regra é:

1. O `aos-orq` confere **sempre os bytes inteiros** que o nó serviu contra o digest e o tamanho
   da âncora, **antes** de olhar para o que eles são. O que não confere não se entrega.
2. Se os bytes são um envelope da sandbox reconhecível, com `exit_code` zero e o stdout em texto,
   entrega-se **só o `stdout_text`**, uma vez (`extraction: sandbox_stdout_text`).
3. Se não são um envelope reconhecível, entrega-se o resultado cru, tal como a tool o devolveu
   (`extraction: raw`).
4. **Uma tool que corre e falha, ou que devolve vazio, fecha o nó do plano** — o consumidor não
   corre: envelope com `exit_code` diferente de zero (`origem_tool_falhou`); texto extraído ou
   resultado cru vazios, só com espaços (`origem_vazia`); envelope com o stdout binário
   (`origem_nao_transportavel`).

O que se entrega é sempre uma função dos bytes que o kernel selou, e o evento regista a âncora,
o digest do entregue e a forma da extracção: quem audita lê os bytes designados do run produtor,
confere-os contra `anchor_digest`, aplica a extracção nomeada e chega a `record.digest`.

O sistema passa a interpretar a forma do resultado, que é conteúdo untrusted. O que interpreta é
só a moldura — um objecto JSON com as chaves do envelope —, e o texto extraído segue untrusted,
pelo mesmo canal e com as mesmas defesas. Reconhecer a forma não prova que os bytes vieram da
sandbox: uma tool que devolva por conta própria um JSON com a forma do envelope é lida como
envelope, e o que se entrega é o `stdout_text` dela (§5).

### 2.8 O texto final do produtor não é publicado

A regra de terminação não muda: o modelo dá o turno final, o texto é capturado e selado, e
continua disponível como texto final do run. Para uma saída por referência não é publicado nem
entregue ao consumidor, nem como comentário: seria repor no caminho conteúdo escrito por um
modelo que já leu untrusted. Fica para auditoria. **Implementado em AOS-501:** o ramo que
publica uma saída com origem vem antes dos do texto final e não cai para eles, e a regra
simétrica do evento (§2.6) recusa publicar sem `source` sob um contrato que a declara.

Um nó pode declarar, além da saída com origem, uma saída de texto (sem `from_tool`): essa
publica-se do texto final, como sempre, e a regra «saída vazia não se publica» vale para ela.

Custo conhecido: o modelo continua a transcrever o documento para um texto que ninguém consome.
Avisá-lo no objectivo, ou concluir o run logo a seguir à chamada designada, ficam fora desta
decisão; a segunda mexe na regra única de terminação.

### 2.9 Sem origem designável, o produtor falha com causa

Independentemente do modo do nó, o `aos-orq` não publica uma saída por referência sem uma âncora
`designated` e sem bytes que batam com o digest, e fecha o nó do plano com causa em vocabulário
fechado. **Implementado em AOS-501.** Com o run concluído e a origem por entregar, as causas são
do `aos-orq`:

| Causa | Quando |
|---|---|
| `origem_em_falta` | Âncora `missing` |
| `origem_ambigua` | Âncora `ambiguous` |
| `origem_inaplicavel` | Âncora `inapplicable` (o run tinha entradas) |
| `origem_sem_vinculo` | Sem o facto `plan.output_source_declared` a dizer `binding` para a tool do contrato; ou sem âncora, com uma âncora mal formada, com o vínculo `measure` ou de outra tool |
| `origem_nao_confere` | O nó serviu bytes cujo digest ou tamanho não são os da âncora |
| `origem_indisponivel` | Origem designada e os bytes não vieram, de vez (o segundo sentido de `output_unavailable`, separado de `saida_indisponivel`, que é o texto final) |
| `origem_nao_transportavel` | Acima do tecto, não é texto válido, ou stdout binário |
| `origem_tool_falhou` | Envelope com `exit_code` diferente de zero |
| `origem_vazia` | O que se entregaria está vazio |

Com o nó em imposição, um run sem origem designável já fecha `failed` no kernel, e a causa é a
razão do kernel (`output_source_missing`, `output_source_ambiguous`). Em todos os casos o
consumidor fecha `entrada_por_cumprir` sem correr, e o plano sai com 13. Um 503 do nó (os bytes
não se lêem neste momento) não fecha o nó do plano: volta-se a ler.

Um nó `aos` que não anuncie o vínculo vinculativo faz o `serve` recusar um plano que declare a
origem (saída 10, `no_sem_saida_por_referencia`); não cai para o texto.

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
  uma cadeia de digests conferível: o digest selado dos bytes da tool, e o digest do que foi
  entregue, ligados pela forma da extracção que o evento nomeia (AOS-501, §2.6 e §2.7). Só com a
  entrega ligada (§10); desligada, existe a âncora e nada é entregue por ela.
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
- O ficheiro de métricas da drenagem do `aos-orq` ganha oito famílias (AOS-499), todas de
  vocabulário fechado e sem conteúdo: a classe estrutural do nó, o estado da designação, o
  tamanho do que se transportaria, o que o nó fez dos bytes e, sobre o conteúdo desembrulhado do
  resultado, a forma, a relação com o texto final, os números e a razão de tamanhos. Só têm
  valores com o interruptor em `observe`.

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
  designável e o envelope — com o `stderr` e o código — é o que fica selado. **Decidido em
  AOS-501: não se entrega**; o nó do plano fecha `origem_tool_falhou` (§2.7).
- **Bytes designados e texto final vazio.** Com `binding` em imposição, a saída vazia avalia os
  bytes designados: um run com origem designada, bytes presentes e texto final vazio é
  **cumprido**, com `FinalText` vazio. **Decidido em AOS-501:** num nó cuja única saída aberta
  tem origem, o texto final vazio não conta — a regra da saída vazia avalia o que se entregaria
  dos bytes designados (§2.7).
- **O `step_id` da âncora identifica a chamada, não um evento.** Num run retomado o mesmo passo
  pode ter mais de um evento de mediação (uma escalada e, depois da aprovação, a mediação que
  executou). Quem audita lê o último permit desse passo.
- **A captura não tem os bytes de um turno designado que mudou de desfecho entre vidas** (§2.3,
  «A fonte dos bytes»). Tem-nos o step-ledger, e só na via durável.
- **Resultados acima do tecto ou que não são texto** falham; fecham-se com a alternativa
  rejeitada agora.
- **A separação de planos (DEF-806) não fecha.** O conteúdo untrusted continua em linha no tail
  do consumidor. Tira-se o modelo do caminho do produtor; não se cria um handle. O consumidor
  passa a receber os bytes exactos do atacante — o texto do documento, sem o modelo do produtor
  de permeio —: as defesas são as de hoje (`plan_input` é dados, autoridade untrusted, gate de
  taint). **Exercitadas por este caminho na revisão do AOS-501** (§11): as 11 entradas de injecção
  do corpus do gate `security` (25 documentos: o payload efectivo, o cru quando difere, e cada
  um dentro de um documento que forja os cabeçalhos de segmento do prompt) passam por uma cadeia
  de ficheiros de fio — o envelope escrito pelo codificador real da sandbox, o `aos-orq consume`
  real em `on`, e o corpo do `POST /runs` entregue ao nó real. O documento chega byte a byte,
  como `plan_input` `taint=untrusted`; os cabeçalhos forjados ficam escapados; e uma tool call
  privilegiada pedida no turno 1 por um modelo que leu o documento é negada pelo gate de taint.
  **O que isso não cobre:** é um corpus de 11 entradas e não uma prova; o modelo é um modelo
  falso que obedece à injecção, e não o de produção; a tool é a de leitura da sandbox, lida pelo
  driver de referência; e a cadeia é de ficheiros, sem um nó e um `aos-orq` a falar um com o
  outro em processo. A defesa contra um texto que não forja cabeçalhos nem pede uma tool
  privilegiada — «resume isto assim» — continua a ser a instrução do prefixo, e não é medida.
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
- **O resultado designado de uma tool com sandbox é o envelope, e o envelope leva o documento
  duas vezes.** A leitura de um ficheiro devolve-o em `stdout_text` e outra vez, em base64, como
  artefacto: o que se sela, se serve e se conta contra os 128 KiB é mais do dobro do documento.
  Visto com o driver de referência, no teste de ponta a ponta do AOS-498; em produção está por
  verificar. Um documento vazio lido na sandbox não é um resultado vazio: são os bytes do
  envelope. **Decidido em AOS-501** (§2.7): entrega-se só o `stdout_text`, e um envelope sem
  texto fecha o nó (`origem_vazia`). O tecto de transporte do nó continua a aplicar-se ao
  envelope: um documento de cerca de 50 KiB lido com artefacto não cabe nos 128 KiB, e o nó do
  plano fecha `origem_nao_transportavel`. É limite declarado.
- **A medição do AOS-499 é um limite superior à perda, não uma contagem de factos perdidos.**
  Compara, letra a letra depois de normalizar espaços, as linhas e os números do conteúdo com o
  texto final. Um número reformatado, uma data por extenso ou texto escapado contam como em
  falta. Não mede se o documento é o certo, nem a qualidade de um resumo, nem os artefactos. Só
  compara quando o nó serve os bytes e eles conferem: sem execução durável ou sem o gate soberano
  de leitura, as classes de conteúdo ficam «não comparado».
- **Que um candidato por estrutura declarou a origem, em observação, vive na memória do
  `serve`.** Um candidato submetido por um `serve` em `observe` e recolhido por outro não é
  medido. A declaração VINCULATIVA, a de um plano com origem, fica no log do plano
  (`plan.output_source_declared`, AOS-501, §2.4); a de medição não ficou — gravá-la mudava os
  eventos de um plano em `observe`.
- **Ler os bytes custa uma leitura do stream inteiro do run**, em cada `GET` de um run concluído
  com a origem designada. Medido na revisão: de 0,4 ms para 26,9 ms com um resultado de 6 MiB
  noutro passo. Com a entrega ligada o `aos-orq` faz uma por nó com origem, e mais uma por
  payload a reidratar em cada retoma. Não foi revisto em AOS-501: fica por medir em produção.
- **Em «só medição» o nó decifra e envia os bytes** (até 128 KiB do titular) a um chamador que só
  os mede; e o digest e o tamanho do conteúdo de um titular apagado continuam a sair, na âncora.
- **`output_unavailable` tem dois sentidos com o vínculo vinculativo:** o texto final que não se
  lê, e os bytes designados que não se lêem. O nó continua a responder com o mesmo campo; o
  `aos-orq` separa-os na causa (AOS-501): num nó com origem só contam os bytes designados
  (`origem_indisponivel`), e `saida_indisponivel` fica para o texto final de um nó sem origem.
- **Qualquer tool atribuída ao nó pode ser origem, incluindo uma tool com egress externo ou com
  efeito** (decisão do dono de 2026-10-06; o validador não ganha regra). O que isso implica: a
  resposta de uma tool de efeito — o corpo que um serviço externo devolveu a uma escrita — passa
  ao nó seguinte como dados untrusted, com as defesas de hoje (`plan_input`, autoridade
  untrusted, gate de taint; um consumidor com tool de efeito continua a não a poder consumir). A
  chamada tem de ser a única da tool no primeiro turno com tools, pelo que um nó que escreve duas
  vezes não tem origem. **O caminho não foi exercitado em produção**: as tools de produção com
  origem medida são de leitura.
- **O envelope é reconhecido só pela forma, e qualquer tool pode ser origem.** Uma tool que
  devolva um JSON só com as chaves do envelope é lida como envelope. Com a decisão de permitir
  qualquer tool do nó como origem, isto dá a quem controla a resposta de uma tool CRUA — uma que
  não corre na sandbox, por exemplo um serviço externo chamado por uma tool de egress — três
  coisas: **escolher o texto entregue** (o `stdout_text` do JSON que devolve); **esconder o
  resto** (em `artifacts`, que não se lêem nem se validam); e **fazer o nó falhar** (`exit_code`
  diferente de zero fecha o nó `origem_tool_falhou`, e o consumidor não corre). O evento diz
  `sandbox_stdout_text` de um resultado que não veio da sandbox, e a derivação só é reproduzível
  por quem leia o JSON como este binário (com uma chave repetida, ganha a última). O conteúdo é
  do terceiro de qualquer modo, e chega untrusted; o que ele ganha é a forma, e a capacidade de
  parar o plano. Não se muda o código: fecha-se quando a origem levar a classe da tool, ou
  quando o nó disser que os bytes são de uma execução da sandbox.
- **Na reidratação, uma leitura que falha agora é definitiva para o consumidor.** O payload por
  referência reconstrói-se relendo o run filho; se o nó responder 503 nesse momento (ou 404, ou
  bytes que já não conferem), o payload não entra e o consumidor fecha `entrada_por_cumprir`. É a
  regra que o texto final já tem (AOS-418): o que não se confirma não entra.
- **`derived_from` continua por preencher**, e o critério do AOS-501 que o pedia fica por
  cumprir. A razão: o campo diz de que payloads uma saída DERIVA, e as saídas que derivam de
  outras são as de TEXTO de um nó com `consumes` — precisamente as que um plano sem origem
  publica. Preenchê-lo mudava os bytes do `plan.payload_published` de planos que não usam
  `from_tool`, fora de `on`, e este ticket obriga-se a não mudar um byte aí. Uma saída por
  referência não deriva de payload nenhum (o nó com origem não tem `consumes`). Fica para o
  ticket que tratar a proveniência ao longo da cadeia.
- **Com `on`, o defeito medido fica aberto em três casos.** A entrega fecha-o onde a origem é
  declarada, e em mais lado nenhum:
  1. **o planeador omite `from_tool`** num nó de leitura — conta-se
     (`aos_orq_consume_candidatos_sem_origem_total`), e o nó entrega o texto do modelo, como
     hoje;
  2. **o planeador declara o nó de leitura como `summary`** (3 dos 21 planos medidos): o
     validador não deixa pôr origem num `summary`, o nó não é candidato por estrutura a mais
     nada, e nem a série da omissão o apanha por essa razão;
  3. **leituras encadeadas**: um nó de leitura com `consumes` nunca tem origem (§2.2), e a saída
     dele é o texto do modelo.

  O quarto caso que a revisão encontrou — o **nó misto**, uma saída com origem e outra de texto
  no mesmo nó — deixou de existir: o validador recusa-o (`from_tool_with_text_output`, §11).
  O inverso também não se mede: **declarar a origem num nó cujo trabalho é transformar** não
  falha, e o consumidor recebe o documento cru.
- **Planos legítimos que passam a falhar com `on`.** Um nó com origem só conclui se a designação
  for limpa, e por isso fecha `failed`, com causa, em situações que hoje concluem:
  - o modelo chama a tool **duas vezes** no primeiro turno, ou chama **outra tool antes**, ou a
    primeira tentativa **falha e a segunda acerta** (`origem_ambigua`, `origem_em_falta`);
  - o documento está **vazio** (`origem_vazia`) ou **não existe** (`origem_tool_falhou`) — antes
    o modelo explicava-o e o plano seguia;
  - o documento passa **cerca de 50 KiB** lido com artefacto, ou **não é texto**
    (`origem_nao_transportavel`);
  - a tool **escreve o documento num ficheiro e imprime outra coisa** no stdout: não falha, e o
    consumidor recebe o stdout;
  - o run tem **memória untrusted no primeiro turno** (`origem_inaplicavel`).

  Quantos são mede-se em `observe` (os estados `missing` e `ambiguous`, e os tamanhos), antes de
  ligar.
- **Um nó `aos` que deixa de anunciar `binding` mata os planos com origem em voo.** Com o
  `aos-orq` em `on`, um anúncio que se LEU e não traz `binding` (o nó reiniciado com o veredicto
  desligado, ou revertido) fecha o plano na geração seguinte, antes da posse: terminal, saída 10,
  `no_sem_saida_por_referencia`; o documento é apagado, o nó do plano fica `running` e o run
  filho órfão. Um anúncio ILEGÍVEL é transitório. É coerente com «nunca cair para o texto», e
  custa planos: para um plano que já tem o facto vinculativo no log, podia ser transitório. Não
  se mudou; o runbook tem a ordem segura.
- **Um `on` posto por engano liga a entrega.** Antes do AOS-501 o valor era inválido e parava o
  `consume`; agora é válido, e um engano no `.env` passa a mudar o que flui entre nós em vez de
  se ver no arranque. O banner é a única coisa que o diz.
- **A mensagem de `origem_sem_entrega` deixou de ser exacta.** Diz que o binário «ainda nao
  entrega por referencia»; a causa é o interruptor fora de `on` no processo que leu o documento
  (incluindo um `decide` sem a variável do `consume`). Não se corrigiu: sai precisamente fora de
  `on`, onde o binário tem de ser byte a byte o anterior. O runbook diz a causa certa.

## 6. O que cada ticket implementa

| Parte | Secção | Ticket |
|---|---|---|
| Declaração e vínculo no objectivo do run; recusa da declaração impossível | §2.1, §2.4 | AOS-497 |
| Regra de designação, igual no loop e no replay | §2.2 | AOS-497 |
| Âncora no desfecho do run e na transição terminal | §2.3 | AOS-497 |
| Razões novas e precedência; métrica do estado da âncora | §2.4 | AOS-497 |
| Manifesto, registo de retoma, paridade do replay | §2.5 | AOS-497 |
| Campo no `POST /runs`, âncora e bytes no `GET /runs/{id}`, anúncio no `GET /tools` (implementado e revisto) | §2.1, §2.6 | AOS-498 |
| Medição pelo `aos-orq` com o vínculo `measure`, sem mudar a entrega (implementado e revisto: interruptor `AOS_ORQ_SAIDA_POR_REFERENCIA=observe`; só declara num pedido que já leva o contrato sobre a mesma tool; compara o texto final com o conteúdo desembrulhado do resultado e publica só classes) | §2.4 | AOS-499 |
| `outputs[].from_tool` no plano, schema 1.3.0, validador, cartão de aprovação (implementado e revisto. O prompt do planeador não muda, e o `aos-orq` não corre a linha 1.3.0 do plano até à entrega) | §2.1 | AOS-500 |
| Publicação e entrega pelos bytes designados (de um envelope da sandbox, só o texto); origem no evento do plano; a declaração e o vínculo no log do plano; texto final não publicado; causas de falha; prompt do planeador 1.5.0; versão do cartão com origem (implementado e revisto; interruptor `AOS_ORQ_SAIDA_POR_REFERENCIA=on`, que só liga por decisão do dono) | §2.4, §2.6 a §2.9, §10, §11 | AOS-501 |

## 7. Emendas a outros ADR

- **ADR-037 §2.4** ganha as duas razões e a precedência da §2.4 acima, e **§5** remete para
  aqui no resíduo «evidência não é fidelidade». Feitas com AOS-497.
- **ADR-037 §2.8** (o que a leitura de desfecho cobre, quando passa a servir um resultado de
  tool) foi emendada com AOS-498, que é quem muda essa leitura.
- **ADR-022 §2.3** (a origem declarada de um output) foi emendado com AOS-500, que é quem dá o
  campo ao plano.
- **ADR-027 §2.4** (a origem do payload publicado) foi emendado com AOS-501: o payload de um
  contrato com origem é o resultado designado da tool, e não o texto final do run filho.

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

## 9. O que a revisão adversarial de AOS-498 e AOS-499 mudou

A revisão independente de 2026-10-05, feita sobre os dois tickets antes da fusão, não encontrou
bloqueantes: sem declaração o nó não muda, e medir não muda um run nem um plano. Levou a estas
alterações, feitas nos dois tickets.

| Achado | O que mudou | Secção |
|---|---|---|
| A medição comparava o texto final com o envelope da sandbox, e não com o documento: «igual» era impossível e uma transcrição curta contava como resumo | O `aos-orq` desembrulha o conteúdo do envelope, só para medir, e publica classes sobre a relação entre os dois textos e sobre os números | §2.4, §5 |
| Nada fixava, pela API, que os bytes não saem de um run que não concluiu | Teste nos dois ramos e nos dois vínculos, com o cenário real (run `failed` com âncora designada) | §2.6 |
| «Medir não muda nada» só estava provado com a mesma resposta do nó nos dois modos | O teste dá ao modo desligado a resposta sem os campos novos e ao modo de observação a resposta com eles | §2.4 |
| Um registo do ledger ilegível e uma âncora cujo passo não forma chave eram transitórios: 503 para sempre com o vínculo vinculativo | São definitivos, com sentinela próprio | §2.6 |
| A leitura do ledger devolvia um registo em claro sem passar pelo opener | Exige o opener e só devolve conteúdo que ele abriu | §2.6 |
| A guarda da leitura dos bytes era só a ordem das chamadas | O leitor admitido vai na mão, e o opener só abre atrás do escopo dele | §2.6 |
| Nenhum teste servia um envelope real da sandbox | Teste de ponta a ponta no nó com a tool da sandbox; os envelopes dos testes do `aos-orq` são escritos pelo codificador real | §2.6, §5 |
| O catálogo de decisões e a matriz de rastreabilidade diziam que só a designação estava implementada | Os dois tickets passam a constar como implementadores | §6 |
| Limites em falta | O envelope com o documento duas vezes; o que a medição não conclui; a declaração na memória do `serve`; o custo da leitura; os dois sentidos de `output_unavailable` | §5 |

Por verificar: a medição em produção (a série em observação), o smoke sobre JetStream, e um run
com a origem designada pelo binário de um nó de desenvolvimento.

## 10. O que o AOS-501 implementa, e o que decidiu

A entrega fica atrás do interruptor do `aos-orq` `AOS_ORQ_SAIDA_POR_REFERENCIA`, que ganha o
valor `on`. A omissão continua a ser `off`.

| Ponto | Decisão |
|---|---|
| O interruptor | `off` e `observe` deixam o binário como estava, incluindo a recusa da linha 1.3.0 do plano (§2.1). Em `on`, a entrega está **activa** num `serve` quando o nó anuncia o vínculo `binding`; em `on` não se mede por estrutura |
| `on` contra um nó sem `binding` | Um plano que declare a origem não corre: `--plan-doc` é recusado antes da posse (saída 10, `no_sem_saida_por_referencia`); no `--goal` o planeador fica com o prompt 1.4.0 e um documento com origem é uma tentativa recusada. Um plano sem origem corre como sempre |
| O carimbo 1.3.0 sem o campo | Volta a ser aceite em `on`: decide o validador (piso das funcionalidades, tecto da linha corrente). Fora de `on` continua recusado |
| O prompt do planeador | O binário conhece duas versões: a corrente (1.4.0, a de omissão, byte a byte a de antes) e a 1.5.0, que nomeia `from_tool`, a linha 1.3.0 e a regra 13. O `aos-orq` só usa a 1.5.0 com a entrega activa. Os dois fingerprints estão fixados por teste |
| A declaração e o vínculo | No evento `plan.output_source_declared`, escrito antes do pedido ao nó (§2.4). Quem decide se um nó é por referência é o documento aprovado; o interruptor governa a admissão do plano |
| A reidratação | O contrato da saída no documento aprovado é a autoridade: uma saída com `from_tool` só entra de um evento com `source`, com o facto vinculativo no log para esse nó, essa tool e esse contrato (`contract_digest`); uma saída sem origem só entra de um evento sem `source`. O payload com `source` relê-se do run filho e só entra se tudo o que o evento registou se confirmar; uma leitura que falha não entra (§5, §11) |
| O nó misto | Não existe: um nó com uma saída com origem não declara outra de forma aberta. O validador recusa-o com `from_tool_with_text_output`; o executor não o submete nem publica nada dele do texto final (§11) |
| `output_unavailable` | Separado na causa: `origem_indisponivel` e `saida_indisponivel` (§2.9, §5) |
| O cartão de aprovação | A versão do contrato sobe a 1.2.0 nos cartões que mostram a origem; os outros continuam a carimbar 1.1.0, com os mesmos bytes. Um cartão com origem carimbado abaixo de 1.2.0 é recusado |
| Rollback | O `aos-orq` anterior, e este com o interruptor fora de `on`, recusam com a saída 10 um documento 1.3.0 guardado e a retoma de um plano com origem. Antes de voltar atrás: parar a entrada de pedidos, deixar acabar COM `on` (ou fechar) os planos com origem em voo e os pendentes de humano, e só então mudar o interruptor ou trocar o binário — mudá-lo primeiro mata os que estão em voo. Ordem: o `aos-orq` antes do nó; e um nó que deixe de anunciar `binding` com o `aos-orq` em `on` mata-os também (§5) |

O que este ticket **não** fez, e fica declarado: `derived_from` (§5, com a razão); a revisão do
custo da leitura; pôr o prompt 1.5.0 diante de um modelo; a verificação em produção, primeiro em
`observe` e depois em `on`.

## 11. O que a revisão adversarial de AOS-501 mudou

A revisão independente de 2026-10-06 não encontrou bloqueantes: fora de `on` o binário é byte a
byte o anterior (refeito pelo revisor, e outra vez depois das correcções). Levou a estas
alterações, feitas no mesmo ticket.

| Achado | O que mudou | Secção |
|---|---|---|
| A reidratação não consultava o documento aprovado: um evento sem `source` para uma saída com origem entrava pelo texto final do run filho, e um evento com `source` entrava para uma saída sem origem | O contrato do documento é a autoridade na leitura, como já era na escrita; exige-se o facto vinculativo, para a tool e para o `contract_digest` do contrato | §2.4, §10 |
| Um nó podia ter uma saída com origem e outra de texto: o consumidor da segunda recebia o resumo do modelo, e nenhuma métrica o contava | O nó misto é recusado pelo validador, com código próprio; o prompt 1.5.0 di-lo (e deixa de dizer que o executor «não transporta nenhum»); o golden-set ganhou o caso; o executor falha fechado | §2.1, §10 |
| O runbook dizia que os planos em voo fechavam `origem_sem_vinculo` quando o nó deixa de anunciar `binding` | O runbook diz o que o código faz (terminal, saída 10, antes da posse, de vez), com a ordem segura; preso por teste. O comportamento não mudou | §5 |
| «Sem queda para o texto» não tinha teste com os bytes presentes | Run `failed`, `timed_out` e `completed` sem `terminated`, com âncora designada e bytes que conferem: nunca se entrega | §2.9 |
| O corpus adversarial não passava pela extracção do `aos-orq` | Cadeia de ficheiros de fio pelas 11 entradas do corpus, até ao prompt e ao gate de taint do nó real | §5 |
| A ordem «conferir antes de extrair» e «o facto antes do pedido» não estavam presas | Testes; bytes que não conferem dão sempre `origem_nao_confere` | §2.4, §2.7 |
| A âncora aceita um passo que o evento de publicação recusa: o `serve` abortava em todas as gerações | O nó fecha com causa (`origem_sem_vinculo`) | §2.9 |
| O `run_id` da resposta do nó não era conferido | Outro `run_id` não se entrega (`origem_sem_vinculo`) | §2.6 |
| O taint do evento de um payload por referência não estava preso | Teste: é sempre `untrusted`, diga o documento o que disser | §2.6 |
| O piso da versão do cartão com origem era a versão corrente | É a versão em que a origem entrou (1.2.0), fixa | §10 |
| A métrica contava `entregue` antes de o nó fechar | Conta o desfecho do nó, depois de publicado | §10 |
| O prompt não dizia que `from_tool` tem de ser o nome exacto de uma tool, com a forma de identificador | O prompt 1.5.0 di-lo | §2.1 |
| Limites em falta | O envelope reconhecido pela forma com qualquer tool como origem; os planos legítimos que passam a falhar; os três casos em que o defeito fica aberto; `derived_from`, com a razão; o nó que deixa de anunciar `binding`; `on` por engano; a mensagem de `origem_sem_entrega` | §5 |

Por fazer antes de ligar `on` em produção: pôr o prompt 1.5.0 diante do modelo de produção (a
taxa de declaração nos nós de leitura, e as declarações em nós que transformam — o golden-set
avalia documentos de amostra, não o prompt); ler a série de `observe` (quantos nós de leitura
dariam `missing` ou `ambiguous`, e o tamanho dos envelopes contra os 128 KiB); ensaiar o
rollback do runbook; e garantir que o `decide` corre com a mesma variável do `consume`.
