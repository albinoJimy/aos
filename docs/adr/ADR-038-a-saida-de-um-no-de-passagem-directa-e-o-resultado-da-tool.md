# ADR-038 — A saída de um nó de passagem directa é o resultado da tool, por referência

- **Estado:** Aceite
- **Data:** 2026-10-05
- **Deciders:** Dono do produto (decisões de 2026-10-05: a origem declara-se e não se infere; sem
  origem designável o run não cumpre e nunca se entrega o texto do modelo; entrega-se o resultado
  tal como a tool o devolveu; os bytes passam pelo `aos-orq`; o texto final do produtor não é
  publicado; o vínculo da declaração é por run) · executor de AOS-497 (implementação da §2.2 à
  §2.5)
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
contra a lista-branca do mesmo pedido e anunciado no `GET /tools`, **fica para AOS-498**; até lá
a declaração só entra por código e nenhum run de produção a tem.

**Uma declaração impossível não arranca.** Antes do primeiro turno o kernel verifica que a tool
declarada consta do tool set do run e, havendo lista-branca, está nela — a regra do contrato
impossível do ADR-037 §2.2, com comparação exacta de nomes. Se não, o run é recusado
(`ErrImpossibleOutputSource`), sem eventos e sem interrogar o modelo. Uma declaração sem vínculo,
com um vínculo desconhecido, ou um vínculo sem tool, também recusa o arranque
(`ErrBadOutputSourceBinding`). Vale nos dois vínculos; com o veredicto desligado (`off`) a
declaração não é lida, como o contrato.

### 2.2 A regra de designação

Qual chamada é «a» origem decide-se no kernel, sem ler texto nem conteúdo:

> A origem é a chamada **efectiva** (despachada, sem recusa e sem erro de tool) da tool
> declarada, feita no **primeiro turno do run que despachou tool calls**, e só se o contexto
> desse turno era **trusted**. Exactamente uma dá `designated`. Nenhuma dá `missing`. Mais de
> uma dá `ambiguous`.

- **Contexto trusted** é o rótulo de autoridade do ADR-034, lido a seguir à montagem do prompt
  do turno: só o prefixo, o objectivo, uma correcção de steer ou um aviso do runtime entraram no
  tail. É o que garante que os argumentos da chamada foram escolhidos por um modelo que ainda não
  tinha visto conteúdo de terceiros. Um documento que diga «lê também o documento X» provoca uma
  segunda leitura, pedida depois de um resultado de tool; essa nunca é a origem.
- **O primeiro turno que despachou tool calls** é o primeiro em que o loop despachou pelo menos
  uma chamada, de qualquer tool e com qualquer desfecho. As duas condições são verificadas em
  separado.
- **Várias chamadas no turno.** As de outras tools não contam. Duas efectivas da tool declarada
  dão `ambiguous`: não se escolhe nem se concatena, porque seria o runtime a compor conteúdo e o
  digest deixava de ser o de um resultado gravado. Uma recusada e uma efectiva dão `designated`:
  há exactamente uma efectiva.
- **Um run com `plan_input` ou memória** tem o contexto untrusted desde o turno 1 e nunca tem
  origem. O validador do plano recusará `from_tool` num nó com `consumes` (AOS-500); o kernel é
  a segunda linha.
- **Uma chamada servida pelo step-ledger** (um resultado memorizado, numa retoma) conta como
  qualquer outra, uma vez, com os bytes memorizados.

A designação é a mesma função no loop e no motor de replay, alimentada das mesmas entradas que o
tail e a captura do turno. **Implementado em AOS-497.**

### 2.3 A âncora

Num run que declarou a origem, o kernel devolve e o nó sela na transição terminal, ao lado do
veredicto, a **âncora** (`output_source`):

| Campo | Conteúdo |
|---|---|
| `tool` | A tool declarada |
| `binding` | O vínculo da declaração (§2.4) |
| `state` | `designated`, `missing` ou `ambiguous` |
| `step_id` | O passo da chamada designada: o `step_id` do seu evento de mediação. Só em `designated` |
| `digest` | `sha256:<hex>` dos bytes do resultado. Só em `designated` |
| `bytes` | O tamanho do resultado. Só em `designated`; ausente quer dizer zero |

O digest calcula-se sobre os bytes do resultado **tal como a tool os devolveu** ao despacho — os
mesmos de que saem o segmento `tool_result` do tail e a captura do turno —, sem rótulos,
delimitadores nem escape. É calculado por quem executou a tool, e não por quem transporta. A
âncora não leva conteúdo do titular.

A âncora existe em qualquer modo de aplicação do veredicto excepto `off`, nos dois vínculos, e
com qualquer estado. A máquina de estados só a aceita numa transição que termina o run e na
forma que o kernel produz (vocabulários fechados, digest `sha256:` com 64 dígitos hexadecimais,
campos da chamada só em `designated`); de outro modo rejeita a transição sem tocar no log.
**Implementado em AOS-497.**

### 2.4 O vínculo é por run, e as razões novas só valem com ele

A declaração leva um **vínculo**, dado pelo chamador e fixado por run
(`Goal.OutputSourceBinding`):

- `measure` (só medição): a âncora calcula-se e sela-se. O veredicto e o desfecho do run são
  exactamente os de um run sem declaração, qualquer que seja o modo do nó. O estado da âncora
  conta-se em `aos_runs_output_source_total{binding,state}`.
- `binding` (vinculativa): com o nó em imposição (`enforce`), uma origem em falta ou ambígua é
  veredicto negativo e o run termina `failed`. Com o nó em observação comporta-se como `measure`.

O vínculo existe porque o modo de aplicação é do nó (ADR-037 §2.6) e a produção está em
imposição desde 2026-10-05: se as razões novas seguissem só o modo do nó, medir a designação em
produção obrigava a falhar runs. Com o vínculo, o `aos-orq` mede com `measure` (AOS-499) e só
entrega por referência com `binding` (AOS-501).

O vocabulário do veredicto (ADR-037 §2.4) ganha duas razões, e a precedência fica:

| Ordem | `outcome_reason` | Quando |
|---|---|---|
| 1 | `truncated` | O turno terminal parou com o motivo `length` |
| 2 | `contract_unmet_no_call`, `contract_unmet_after_denial`, `contract_unmet_after_tool_error` | O contrato de conclusão não está cumprido |
| 3 | `output_source_missing`, `output_source_ambiguous` | Declaração vinculativa em imposição, e a origem não é designável |
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
mesmos bytes; a tool não volta a correr e o digest selado é o da chamada da primeira vida.

Todos os campos novos são omitidos quando vazios: um run sem origem declarada grava os bytes de
antes, e um log gravado antes desta decisão reproduz-se sem âncora. **Implementado em AOS-497.**

### 2.6 O transporte: os bytes passam pelo `aos-orq`, com a âncora no kernel

O `GET /runs/{id}` de um run que declarou a origem devolve a âncora e os bytes designados; o
`aos-orq` confere `sha256(bytes)` contra o digest selado, publica-os como payload do nó e
entrega-os ao consumidor pelo canal `inputs` que já existe. O evento `plan.payload_published`
ganha a origem (`source`: tipo, tool e passo), derivada do contrato aprovado, e o seu digest
passa a ser o selado pelo kernel. O nó consumidor não muda: recebe um `plan_input`, untrusted,
com os tectos de hoje (128 KiB por payload, 512 KiB no conjunto). Um resultado acima do tecto ou
que não seja texto válido não se transporta, e o produtor falha com causa própria; nada é
truncado.

Quem lê, com que autorização e por que canal ficam como estão: a leitura do desfecho pelo gate
soberano, depois do selo WORM. O que muda é de onde vêm os bytes e a que se amarra o digest.
**A leitura e a resposta do nó ficam para AOS-498; a publicação e a entrega para AOS-501.**

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
- O `/metrics` do nó ganha `aos_runs_output_source_total{binding,state}` (6 séries) e duas
  razões em `aos_runs_finished_total`. As razões novas chegam também aos contadores do `aos-orq`
  que enumeram o vocabulário do kernel; um `aos-orq` anterior lê uma razão que não conhece como
  `razao_desconhecida`.
- Mais um digest de conteúdo do titular em claro, na transição terminal do run. O step-ledger já
  tem em claro o `result_hash` do mesmo resultado; depois de um apagamento por crypto-shredding o
  digest sobrevive, como esse já sobrevive.
- O kernel passa a escolher um resultado. A escolha não depende de conteúdo untrusted: é o que a
  regra do contexto trusted garante.
- `Machine.RebuildOutcome` devolve a âncora com o veredicto, do mesmo evento.
- Com a declaração vinculativa em imposição haverá mais runs `failed`, com causa nomeada onde
  hoje há um verde (por vezes falso): duas leituras no mesmo turno, a tool chamada só depois de
  outra, um nó com `consumes`. A medição com `measure` conta-os antes de se impor.

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
- **A captura em modo de referência não reproduz o digest.** Sem cifra por-titular e com a
  guarda de segredos ligada, a captura guarda só o hash do resultado e o replay devolve um
  marcador; a reprodução já divergia aí no turno seguinte. Não é o caminho de produção, que sela
  por-titular.
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
| Medição pelo `aos-orq` com o vínculo `measure`, sem mudar a entrega | §2.4 | AOS-499 |
| `outputs[].from_tool` no plano, schema 1.3.0, validador, cartão de aprovação | §2.1 | AOS-500 |
| Publicação e entrega pelos bytes designados; origem no evento do plano; texto final não publicado; causas de falha; prompt do planeador | §2.6 a §2.9 | AOS-501 |

## 7. Emendas a outros ADR

- **ADR-037 §2.4** ganha as duas razões e a precedência da §2.4 acima, e **§5** remete para
  aqui no resíduo «evidência não é fidelidade». Feitas com AOS-497.
- **ADR-037 §2.8** (o que a leitura de desfecho cobre, quando passa a servir um resultado de
  tool) é emendada com AOS-498, que é quem muda essa leitura.
- **ADR-027 §2.4** (a origem do payload publicado) é emendado com AOS-501, e **ADR-022 §2.3** (a
  origem declarada de um output) com AOS-500. AOS-497 não muda o que esses dois descrevem: nenhum
  payload é ainda publicado por referência e o plano ainda não tem o campo.
