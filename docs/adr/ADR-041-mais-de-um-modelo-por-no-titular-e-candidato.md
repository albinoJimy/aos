# ADR-041 — Mais de um modelo por nó: titular e candidato, a rota fixada por run, e o canary como percentagem de planos

- **Estado:** Aceite
- **Data:** 2026-10-11
- **Deciders:** Dono do produto (decisões de 2026-10-11: **D4** — o canary é uma percentagem de
  **planos**, com a fatia escrita no registo assinado, até 40 planos servidos pelo candidato;
  **D6** — um titular e, no máximo, um candidato; **D8** — só entram pela via assinada perfis que
  não devolvem estado; e as cinco decisões registadas no ticket sobre a chave do plano, a
  fixação só com perfis assinados, o `serve` manual, o interruptor desligado a meio de um plano
  e o crash-resume) · executor de AOS-520
- **Tickets:** AOS-520 (PR 0, o perfil fixado por run — ADR-036 §2.8; **PR 1, este ADR**: a
  função de escolha, o modelo fixado por run, o despachante por alias e o livro do canary;
  **PR 2**, as séries por modelo — §2.8). AOS-521 (o disjuntor — §2.9, reservado). Nasce
  desligado (`AOS_MODEL_CANDIDATE=off`) e só liga por decisão do dono, depois de uma revisão
  adversarial independente; por verificar em produção e sobre JetStream.
- **Relacionados:** ADR-010 (replay determinístico), ADR-012 (auto-modificação: canary e
  ratificação), ADR-018 (fronteira nó↔ORQ), ADR-021 (scoring no gateway, proposto), ADR-025
  (despromoção automática), ADR-027 (cada nó do plano é um run do nó), ADR-029 (nomes de
  stream), ADR-030 (reclamação da fila de pedidos de plano), ADR-035 (o submissor do plano é
  derivado pelo nó), ADR-036 (§2.3 e §2.8: a rota e o seu perfil), ADR-039 (a nova tentativa de
  um nó do plano), ADR-040 (§2.11: o estado só volta à rota que o produziu)

> **Resumo.** O nó passa a poder servir dois modelos: o **titular**, que é o de sempre
> (`AOS_MODEL_NAME`), e no máximo um **candidato**, que vem de um registo de entrada assinado
> pelo dono. Quem decide qual serve um run é uma **função pura do identificador do plano** e da
> fatia assinada — não quem submete. A decisão toma-se **uma vez**, no início do run, fica
> **gravada** (num evento do run e no registo de retoma) e todas as leituras seguintes usam o
> valor gravado: o modelo de um run não muda a meio, nem numa retoma, nem depois de o nó ser
> recriado com outra configuração. Todos os nós e todas as tentativas de um plano usam o mesmo
> modelo. O canary acaba sozinho ao **40.º plano** servido pelo candidato; o candidato **nunca**
> passa a titular sem um acto assinado do dono. Sem candidato a servir, o nó é o de antes.

## 1. Contexto

Lido no código a 2026-10-11 (`docs/reports/desenho-a3-entrada-automatica-2026-10-11.md` §2 e o
plano do PR 1):

- **Um modelo por nó.** O nome pedido lê-se uma vez (`AOS_MODEL_NAME`), o adaptador do gateway
  constrói-se uma vez com esse nome, e `fixarModelo` sobrepõe o modelo do nó ao Goal em toda a
  hospedagem — submissão, retoma e crash-resume. Com um só modelo isso é correcto. Com dois,
  seria mudar de modelo a meio de um run.
- **O `POST /runs` não tem campo de modelo**, de propósito, e um nó do plano não tem campo de
  modelo, tier nem classe. Nada disso muda.
- **O AOS-519 trouxe a via assinada**: um registo de entrada assinado pelo dono, com o perfil da
  rota, o veredicto do banco de ensaio e uma **fatia** de canary — lida e validada, sem efeito.
- **O PR 0 do AOS-520 fixou o perfil por run** (ADR-036 §2.8): o digest do perfil fica no Goal,
  no registo de retoma e no manifesto de cada turno, e é conferido antes de re-hospedar e antes
  de cada pedido.
- **Não existia canary em sítio nenhum do código.** A palavra aparecia como pré-condição
  declarada (ADR-012), não como máquina.
- **O nó prova de que pedido de plano um run é trabalho** (ADR-035, ADR-039): o drenador envia
  `plan_request{run_id, generation, …}` e o nó confere a reclamação viva, o pedido e a forma do
  id. O `plan_id` é só declarado; o nó não o prova.

## 2. Decisão

### 2.1 Titular e candidato

- O **titular** é o modelo de `AOS_MODEL_NAME`. É o que serve tudo o que não for atribuído ao
  candidato, e é o único que existe num nó sem candidato a servir.
- Um **candidato** é um registo de entrada assinado (AOS-519) cujo nome pedido **não é** o do
  titular. Um registo assinado para o nome do titular não é um candidato: substitui o perfil do
  titular, como no AOS-519.
- **No máximo um candidato a servir.** Com `AOS_MODEL_CANDIDATE=on` e dois registos que não
  sejam o do titular, o nó não arranca. Com `off` a pasta pode ter vários: nenhum é servido.
- O candidato **não passa a titular** por nenhum caminho do código. Passar é o dono mudar
  `AOS_MODEL_NAME` (e a allowlist assinada que o autoriza).
- O candidato não é um nome novo: pede-se pelo alias que a allowlist assinada já autoriza para
  o board e a região do nó (D1, D9), conferido no arranque pelo AOS-519.

### 2.2 A chave do plano

A unidade do canary é o **plano**. A chave é:

- num run que é trabalho de um pedido de plano, com o vínculo **verificado pelo nó**: o
  `plan_request.run_id` — o identificador do pedido, que é o que o nó prova (ADR-035);
- em qualquer outro run: o **`run_id` inteiro**. Um run sem plano é um plano de um nó.

A chave não é o `plan_id` (declarado pelo drenador, não provado), e **o id nunca é partido pelo
`~`**: um identificador de pedido pode conter `~`, e partir daria a dois planos a mesma chave.
Um run do `aos-orq serve` manual (sem geração) não traz `plan_request`: cada nó conta como um
run independente (decisão do dono).

**A chave leva o tipo.** No livro, uma chave é `pedido` (o identificador de um pedido de plano
verificado) ou `run` (o id de um run sem pedido), e os dois espaços não se misturam: quem
submete um run avulso escolhe o seu id, e pode escolher o de um pedido de plano que já existe —
sem o tipo, a atribuição desse run ao candidato valia como «plano já atribuído» para os nós
seguintes do plano com o mesmo nome. Um pedido e um run com o mesmo identificador são dois
planos, e cada um conta um. **O tipo não entra na função de escolha**: a função reparte pelo
identificador, e os vectores do §2.3 são os mesmos.

A chave é calculada no `POST /runs`, do que o nó verificou, e entregue à hospedagem por um
parâmetro interno. Nenhum campo do corpo, nenhum cabeçalho e nenhum campo do plano a altera.

### 2.3 A função de escolha

```
balde = uint64_be( SHA-256( "aos-canary/v1" || 0x00 || chave )[0:8] ) mod 10000
candidato  sse  balde < fatia × 100
```

`chave` entra byte a byte, sem normalização. `fatia` é a percentagem inteira do registo
assinado, de 0 a 100. Sem relógio, sem estado, sem aleatoriedade, sem segredo. É **monótona**:
o balde não depende da fatia, pelo que subir a fatia nunca tira um plano ao candidato. Uma
fatia fora de 0..100 e uma chave vazia dão o titular. Vive em
`packages/platform/model-gateway/policy/canary`, que só importa a biblioteca-padrão.

O prefixo de domínio leva a versão (`aos-canary/v1`), que é também o que fica gravado como
«quem fixou o modelo deste run». Uma função que reparta de outra maneira é outra versão.

**Vectores** (presos por teste; mudar a função avermelha-os):

| chave | balde | a 10% | a 25% | a 50% |
|---|---|---|---|---|
| `0` | 168 | candidato | candidato | candidato |
| `Plano-0001` | 4773 | titular | titular | candidato |
| `aos-internal/x` | 6242 | titular | titular | titular |
| `p~no~2` | 6937 | titular | titular | titular |
| `a` | 8230 | titular | titular | titular |
| `plano-0001` | 8680 | titular | titular | titular |
| `plano com espaco` | 8712 | titular | titular | titular |
| `run-2026-10-11` | 9451 | titular | titular | titular |
| `fronteira-16149` | 999 | candidato | candidato | candidato |
| `fronteira-12733` | 1000 | titular | candidato | candidato |

Os dois últimos estão em cima da fronteira dos 10%: a comparação é estrita.

**Distribuição** (a série fixa `plano-0000` … `plano-0999`; intervalo de três desvios-padrão de
uma binomial; a contagem exacta está presa):

| fatia | intervalo esperado em 1000 | contagem |
|---|---|---|
| 10% | 72 a 128 | 110 |
| 25% | 209 a 291 | 254 |
| 50% | 453 a 547 | 498 |
| 0% | 0 | 0 |
| 100% | 1000 | 1000 |

Nos primeiros 200 da série, a 10%: 25 (intervalo 8 a 32). A mesma medição falha com uma função
que devolve sempre o titular e com uma que devolve sempre o candidato.

### 2.4 A rota fixada por run

A escolha faz-se **uma vez**, na hospedagem do run (`hostRun`), depois do lease e do claim
`ready→running`, antes do registo de retoma e do primeiro turno — e **só para um run novo**.

**Dois caminhos, separados por um facto do nó.** Antes de hospedar um `run_id`, o serviço lê
o seu **estado durável** — já o lia, para recusar um run terminado. É esse facto, e não o
caminho por que a hospedagem foi pedida, que separa:

**(i) O `run_id` não tem estado durável: é um run novo.** Não se lê história nenhuma — não há.
Sem candidato a servir, é exactamente o caminho de antes deste ADR: em comportamento, em custo
(nenhuma leitura do Event Store a mais) e em erro (nenhuma leitura nova que possa falhar). Com
candidato a servir, é aqui que a escolha corre. **Só neste caminho uma recusa é terminal** (o
run fecha `failed`): o plano do candidato sem candidato a servir, o livro ilegível ou em
contenção, a fixação que não se grava, e um Goal novo que traga uma marca que nada selou.

**(ii) O `run_id` já existe (`running`): um `POST /runs` de um run em curso.** É uma **retoma
por outro nome**, e tem o contrato da retoma pela API e do crash-resume, com ou sem a pasta de
perfis. Lê-se o que está **selado** para o run — o registo de retoma, e o `run.model_pinned` do
seu stream — e:

- **fixação gravada que o nó serve** ⇒ o run continua com o que está selado (o alias, o digest
  e a marca); não se volta a escolher;
- **fixação gravada que o nó não serve** — o alias não está carregado, o perfil é outro, a marca
  é de uma versão da função que o binário não conhece, ou o nó não tem a pasta de perfis ⇒ a
  submissão é **recusada antes de hospedar** (409), com a causa no log;
- **história sem fixação** ⇒ o titular de sempre;
- **história que não se lê**, ou **log que se contradiz** (um registo sem a marca e um
  `run.model_pinned`; ou os dois com aliases ou digests diferentes) ⇒ recusada antes de hospedar.

**Uma recusa no caminho (ii) não mata o run.** Nada é escrito — nem um evento no stream do
run, nem um registo de retoma — e o run fica como estava, `running` e **recuperável**: repõe-se
o perfil ou o registo em que está fixado, ou retoma-se num nó que o sirva. É a mesma decisão do
dono para a retoma (o órfão recuperável): o re-POST não é a via que mata um run, nem a que o
leva ao titular em silêncio. Em particular, um nó **sem** a pasta de perfis que receba o
re-POST de um run fixado no candidato não o hospeda — e por isso não grava um registo de
retoma sem a marca que depois envenenasse a retoma num nó com a pasta.

**O custo do caminho (ii)** são duas leituras, e só ele as paga: o stream do run, e os
registos de retoma — que é uma **varredura do stream das aprovações inteiro**, que cresce com a
vida do nó. É por isso que não correm na hospedagem de um run novo. Medido contra o PR 0: a
hospedagem de um run novo sem candidato a servir faz as mesmas leituras (seis ao stream do
run, uma ao das aprovações, nenhuma ao livro), preso por teste.

Um nó sem máquina de estados composta não conhece o estado durável de um run: trata todos os
`run_id` como novos, como antes.

**A marca no Goal só vale se for a que está selada para o run.** Quem chega com a marca é a
retoma, que a leu do registo. Um Goal com a marca para um run novo é recusado
(`marca_sem_fixacao_gravada`, terminal: é um run novo); para um run que já existe, a marca, o
alias e o digest têm de ser os gravados, ou a submissão é recusada antes de hospedar. A marca é
de um **conjunto fechado** — as versões da função que o binário conhece; um run fixado por uma
versão desconhecida não é reinterpretado, nem na retoma, nem no re-POST.

O que fica gravado:

- **No Goal** (e por ele no manifesto de cada turno): `Model.ModelID` — o alias escolhido —,
  `RouteProfileDigest` — o digest do perfil desse alias (PR 0) — e `ModelPinnedBy` — a marca de
  que o modelo foi fixado por escolha, com a versão da função.
- **No stream do run, o evento `run.model_pinned`**, escrito em fail-closed (se não ficar
  gravado, o run não arranca): o papel (`titular` ou `candidato`), o alias, o digest do perfil,
  o digest do registo de entrada do candidato, a fatia em vigor, a versão da função, a chave do
  plano com o seu tipo, e a razão — de vocabulário fechado: `funcao`, `plano_ja_atribuido`,
  `plano_anterior_a_abertura`, `canary_fechado`, `livro_de_versao_mais_recente`.
- **No registo de retoma**, o campo `ModelPinnedBy` (ausente num run não fixado por escolha —
  os bytes de sempre), ao lado do modelo e do digest do perfil.

Regras, por esta ordem:

1. **Um Goal que já traz a marca** veio do registo de retoma: não se toca no modelo. Confere-se
   que o alias fixado é servido por este nó e que o perfil em vigor para ele tem o digest
   fixado. Senão, a retoma pela API é recusada (409) e a varredura de crash-resume deixa o run
   como órfão declarado, com as causas do PR 0: `modelo_fixado_nao_servido` e
   `perfil_diferente_do_fixado`. **Nunca passa para o titular.**
2. **Um Goal de registo sem a marca** — um run de antes deste ADR, ou de um nó sem candidato a
   servir — é retomado como sempre: o modelo do nó sobrepõe-se.
3. **Um run novo sem candidato a servir**: o `fixarModelo` de sempre. A função de escolha não é
   chamada, não há evento, e o registo de retoma não ganha o campo.
4. **Um run novo com candidato a servir**: consulta-se o livro do canary (§2.6), escolhe-se,
   grava-se, e só depois se escreve o registo de retoma.

Só um nó com perfis assinados carregados fixa (decisão do dono, a mesma do PR 0). Os runs
fixados como **titular** durante um canary também levam a marca: retomados num nó cujo titular
mudou entretanto, continuam no modelo em que começaram, ou não continuam.

### 2.5 Um gateway, um adaptador por alias, um despachante

O gateway é **um só**: o perfil resolve-se por pedido e o failover mantém o modelo pedido. O
que é por modelo é o **adaptador** RT→GW, que guarda o alias que pede. O nó constrói um
adaptador para o titular e um por cada alias assinado que não seja o do titular, todos sobre o
mesmo gateway e com as mesmas opções, e um **despachante** escolhe pelo alias que a hospedagem
pôs no contexto do run:

- sem alias no contexto ⇒ o adaptador do titular (todos os runs não fixados por escolha);
- alias conhecido ⇒ o adaptador desse alias;
- **alias desconhecido ⇒ erro** (`modelo_fixado_nao_servido`). Nunca o titular.

O despachante existe sempre que há um perfil assinado não-titular carregado, **mesmo com o
interruptor a `off`**: é o que deixa um run já fixado no candidato acabar enquanto o perfil
estiver carregado. O interruptor só controla atribuições novas. Sem perfil não-titular, o
cliente de modelo do nó é o de antes, sem despachante.

A conferência do perfil fixado do PR 0 passa a ser feita contra o **alias fixado no run**: é o
adaptador desse alias que a faz em cada turno, e a retoma confere o alias do registo. Um run
fixado no candidato cujo contexto perdesse o alias cairia no adaptador do titular — e seria
recusado aí, porque o digest fixado não é o do perfil do titular.

Com `on` e a contabilidade de custo composta, um candidato **sem preço** na tabela em vigor
aborta o arranque: a contabilidade recusa uma chamada a um par sem preço, e o candidato
falharia todos os turnos.

### 2.6 O canary: o livro, a marca, o fim aos 40, reabrir

O estado do canary é **durável e partilhado entre réplicas**: um stream do Event Store,
`aos-internal/model-canary`, escrito com concorrência optimista (`WithExpectedSeq`). O audit de
governação do gateway é local à réplica e não serve para contar. Três tipos de evento:

- **`model.canary.opened`** — um por **época** de um registo de entrada (pelo digest do
  registo): o alias, o digest do perfil, a fatia, o limite (**40**) e a **marca de abertura** —
  o último `seq` da fila de pedidos de plano nesse momento. A **primeira** abertura de um
  registo fixa a fatia e o limite, e são esses valores gravados que decidem — não os do binário
  que lê. Há uma **época nova sempre que o registo em vigor no livro muda**, incluindo um
  registo antigo que volta depois de outro: a época traz a marca **actual** da fila, e é a
  marca da época mais recente que decide. Um reinício com o mesmo registo não abre época.
- **`model.canary.plan_assigned`** — um por plano **atribuído** ao candidato (os do titular não
  se gravam): a chave do plano e o seu tipo, o alias e o digest do perfil. A primeira escrita
  ganha: a chave de idempotência é a do plano, e uma segunda escrita devolve a primeira.
  **Atribuído não é servido:** grava-se antes do `run.model_pinned` do run, e um run recusado a
  seguir já gastou o lugar. A ordem é de propósito — erra para o lado de fechar mais cedo, e
  nunca dá ao candidato mais planos do que o limite.
- **`model.canary.closed`** — quando a contagem chega ao limite, com as contagens.

Para um **run novo**, com candidato a servir, por esta ordem:

0. **O livro tem eventos de uma versão mais recente** (abaixo, «Como o livro evolui») ⇒ nenhum
   plano novo vai para o candidato: o run é do titular (`livro_de_versao_mais_recente`); e um
   plano já atribuído ao candidato é recusado, com a causa do mesmo nome.
1. **O plano já foi atribuído ao candidato** (em qualquer abertura) ⇒ candidato, **se** o alias
   atribuído for o do candidato em vigor com o mesmo digest de perfil. Senão o run é recusado
   (`plano_do_candidato_sem_candidato`): um plano não troca de modelo a meio.
2. **O pedido de plano é anterior à abertura** (o seu `seq` na fila é menor ou igual à marca da
   época mais recente do registo) ⇒ titular: os nós que esse plano já correu foram servidos
   pelo titular.
3. **O canary está fechado** (a contagem chegou ao limite) ⇒ titular.
4. Caso contrário **a função decide** (§2.3). «Candidato» só vale depois de o `plan_assigned`
   ficar gravado com o `seq` que se leu: se outra réplica escreveu entretanto, relê-se e
   decide-se de novo, até um tecto de tentativas; esgotado, o run falha
   (`livro_do_canary_em_contencao`).

Com o candidato **não** servido (interruptor a `off`, fatia a 0, registo retirado da pasta) e a
pasta de perfis configurada, o livro lê-se **uma vez no arranque**: um run novo de um plano já
atribuído ao candidato é recusado com a mesma causa — o plano falha, não continua no titular
(decisão do dono). Sem a pasta configurada não se lê nada.

**O fim.** Ao 40.º plano atribuído o candidato deixa de receber planos novos; os planos já
atribuídos acabam com ele. A contagem é a do livro, e é por registo: um reinício não a repõe a
zero, e repor um registo antigo também não. **Reabrir é assinar um registo novo** — outro digest
de registo, outro `opened`, a contagem a zero e uma marca nova.

**O fim é reconciliado — por um nó com o candidato a servir.** Quem vê o canary fechado pela
contagem — no arranque e em cada decisão — garante as duas coisas que o fim deixa: a marca `closed` no livro (se o nó que
atribuiu o último plano morreu antes de a gravar, grava-a o seguinte) e o **selo no audit de
governação do gateway**. O selo pede-se a uma função que **lê o audit** e só sela se lá não
estiver; um selo que falhou repete-se. O audit é **local à réplica**: cada réplica sela o fim
no seu, uma vez — a fonte do fim e da contagem é o livro, que é partilhado. **A reconciliação
não corre num nó sem candidato a servir** (interruptor a `off`, fatia a 0, registo retirado):
esse nó só lê o livro. Se o canary fechou e o nó foi logo a seguir posto a `off`, a marca ou o
selo que tenham ficado por gravar ficam por gravar até um nó voltar a servir esse registo.

**Como o livro evolui (contrato).** O stream é append-only e não se repara, e vai ter mais
escritores (o disjuntor do AOS-521). A regra:

- factos novos entram por **tipos de evento novos da família `model.*`**; o significado e a
  forma de um tipo que já existe não mudam;
- um binário que encontre um tipo da família que **não conhece** ignora-o na contagem e regista
  que o viu. Com isso o canary passa a **não decidível por essa versão**: não atribui nenhum
  plano novo ao candidato, recusa os runs novos de planos já atribuídos ao candidato, e serve o
  titular normalmente. Um nó sem candidato a servir arranca sempre. O arranque avisa, e a
  métrica `aos_model_canary_newer_events` conta-os;
- um evento **malformado de um tipo conhecido**, e um tipo de **fora da família**, continuam a
  ser ilegíveis: fail-closed (o run é recusado; no arranque, o nó não arranca).

É o que deixa uma versão mais recente escrever no livro durante um deploy rolante, ou antes de
um recuo de imagem, sem derrubar os nós da versão anterior.

**Falhas, sempre pelo lado fechado.** Livro ilegível ⇒ o run falha (`livro_do_canary_ilegivel`).
Candidato indisponível ⇒ os runs dele falham; nenhum cai no titular. Nenhuma decisão do canary
lê o relógio.

**As recusas.** Um run **novo** recusado pela fixação acaba `failed`, com a razão
`model_pin_refused` na transição terminal e a causa no log do nó; o `aos-orq` trata-o como a
falha terminal de um nó do plano. As causas, em vocabulário fechado:
`plano_do_candidato_sem_candidato`, `livro_do_canary_ilegivel`, `livro_do_canary_em_contencao`,
`livro_de_versao_mais_recente`, `fixacao_nao_gravada`, `fixacao_diferente_da_gravada` e
`marca_sem_fixacao_gravada`.

Um run que **já existe** e que o nó não pode continuar — pela retoma, pelo crash-resume ou por
um `POST /runs` do mesmo id (§2.4) — é recusado **antes de hospedar** e fica `running`,
recuperável; nunca `failed`. As causas: as do PR 0 (`modelo_fixado_nao_servido`,
`perfil_diferente_do_fixado`, `perfil_assinado_sobre_estado_sem_fixacao`) e, no re-POST,
`fixacao_diferente_da_gravada` (o log contradiz-se), `marca_sem_fixacao_gravada` e
`historia_do_run_ilegivel`.

### 2.7 O interruptor e a inércia

`AOS_MODEL_CANDIDATE=off|on`, omissão `off`; outro valor não arranca. **O candidato está a
servir** quando, e só quando: o interruptor está a `on`, há exactamente um registo assinado
não-titular, e a sua fatia assinada é maior que zero. A fatia é o segundo travão.

Sem candidato a servir — sem pasta, com `off` (haja ou não registo), ou com `on` e a fatia a 0
— **o caminho de um run novo é o de antes**: não passa pela função de escolha nem pelo livro,
não escreve `run.model_pinned`, não abre o canary, não faz uma leitura do Event Store a mais
(e por isso não ganha uma falha nova), e o pedido ao provider, o manifesto de cada turno, o
registo de retoma e o `/metrics` são os de antes. A única coisa a mais é uma consulta a um mapa
em memória — os planos que o livro dizia atribuídos ao candidato no arranque —, para recusar o
run novo de um deles. O que muda sem candidato a servir é o caminho (ii) do §2.4: o `POST /runs`
de um run em curso passou a ler o que está selado para ele, e pode ser recusado. No arranque, um registo não-titular na pasta
acrescenta uma linha a dizer «candidato não servido»; as linhas que já existiam não mudam.

### 2.8 Métricas

Com candidato a servir, e só então: `aos_model_canary_plans_assigned` (planos atribuídos ao
candidato pelo registo em vigor, da última leitura do livro por esta réplica) e
`aos_model_canary_closed` (0 ou 1). Sem rótulos. `aos_model_canary_newer_events` (eventos do
livro de tipos que o binário não conhece) sai com candidato a servir e, sem ele, só quando os há.

**As séries por modelo (PR 2).** Três famílias **novas**, ao lado das que já existiam — que não
mudam de nome, de rótulos nem de valor:

| Família | Rótulos | O que conta |
|---|---|---|
| `aos_model_turns_by_model_total` | `model`, `stop_reason` | pedidos ao vivo que devolveram um turno, pelo motivo de paragem |
| `aos_model_response_rejected_by_model_total` | `model`, `causa` | respostas que o gateway recusou (as causas do AOS-509) |
| `aos_runs_finished_by_model_total` | `model`, `outcome`, `reason` | runs que o processo selou, por estado e razão do veredicto |

- **O rótulo `model` é de conjunto fechado**: o alias do titular e o do candidato a servir — os
  nomes **pedidos**, que vêm de `AOS_MODEL_NAME` e do registo assinado. Nunca o modelo que o
  fornecedor declara ter servido (esse continua em `aos_model_route_checks_total`, num conjunto
  também fechado). Quem conta os turnos e as recusas é o despachante, pelo alias que escolheu;
  quem conta os runs é a hospedagem, pelo alias que ficou no Goal.
- **Só existem com um candidato a servir** (§2.7). Sem pasta, com `off` ou com a fatia a 0 não se
  cria contador nenhum e o `/metrics` é o de antes. Consequência: um run já fixado no candidato
  que acabe depois de o interruptor passar a `off` conta só nas famílias de sempre.
- **O tecto: 70 séries** — dois modelos × (6 motivos + 5 causas + 3 estados × 8 razões) —, todas
  sempre presentes (o zero é um zero verdadeiro), preso por teste.
- **O que não somam.** Um run recusado pela fixação (`model_pin_refused`) não chegou a ter modelo
  e não conta em nenhum: fica só em `aos_runs_finished_total`. Os turnos por modelo são os **ao
  vivo**; `aos_model_turns_total` conta os turnos percorridos, incluindo os reproduzidos numa
  retoma — as duas não têm de somar o mesmo. Os desfechos somam: por estado e razão, a soma dos
  modelos é `aos_runs_finished_total` enquanto todos os runs selados tiverem modelo.
- `deploy/server/alerta-rota.sh` e as métricas que lê não mudaram.

### 2.9 O disjuntor — reservado ao AOS-521

Este ADR só fecha a fatia pelo fim do canary. Tirar a fatia a um candidato que falha, antes do
40.º plano, é o AOS-521, que entrega esta secção por emenda: onde vive o estado do disjuntor, o
que o abre e que só o dono o volta a fechar. O que o AOS-521 encontra feito: o livro durável
com CAS, o evento `closed`, a regra «canary fechado ⇒ titular» no ponto único da escolha, e o
**contrato de evolução do livro** (§2.6): os factos do disjuntor entram por tipos novos da
família `model.*`, e um nó desta versão que os encontre deixa de atribuir planos ao candidato
em vez de cair — que é o lado certo para um disjuntor que ele não sabe ler.

## 3. Alternativas rejeitadas

- **Canary por run ou por turno.** Um plano com nós servidos por dois modelos não deixa
  atribuir uma falha do plano a um modelo, e por turno partia o estado opaco (ADR-040 §2.11).
- **Quem submete escolhe** (um campo no `POST /runs`, um cabeçalho, um campo do plano). Tornava
  a fatia uma sugestão, e a amostra do canary passava a ser a que o submissor quisesse.
- **Uma função com segredo** (HMAC com uma chave do nó). Fechava o limite «quem dá o nome ao
  plano influencia a escolha», mas a escolha deixava de ser reproduzível só do log e passava a
  depender de uma chave que tem de ser a mesma em todas as réplicas e sobreviver a rotações. O
  dono aceitou o limite (§5).
- **Dois gateways.** Duplicava a allowlist, o audit, a contabilidade e o keypool, e punha em
  dúvida qual deles selava um turno. O gateway não tem o modelo na construção.
- **O adaptador lê o modelo do contexto.** Um valor de contexto que não seja re-anexado
  perde-se em silêncio, e o adaptador passava a pedir o titular para um run do candidato sem
  que nada falhasse. Com um adaptador por alias, o modelo é um campo fixo de cada um, e a
  conferência do perfil fixado apanha o engano.
- **Contar fora do Event Store** (em memória, ou no audit do gateway). Em memória perde-se no
  reinício; o audit é local à réplica, e duas réplicas dariam 80 planos ao candidato.
- **Partir o id do run pelo `~`** para chegar ao plano. O id de um pedido pode conter `~`, e o
  `serve` manual produz ids com `~` sem pedido nenhum por trás. Usa-se o que o nó prova.

## 4. Consequências

- O modelo de um run passa a ser um facto gravado, e não a configuração do nó no momento:
  reconstrói-se só do log que modelo e que perfil serviram cada run, e porquê.
- A retoma deixa de «corrigir» o modelo para o do nó num run fixado por escolha. Um run fixado
  num modelo que o nó já não serve fica parado — `running`, recuperável repondo o perfil — em
  vez de mudar de modelo. Vale para as três vias que re-hospedam um run que já existe: a retoma
  pela API, o crash-resume e o `POST /runs` do mesmo id. Nenhuma o sela `failed`.
- O Event Store ganha um stream partilhado, pequeno (por registo assinado: uma abertura por
  época, no máximo 40 atribuições e um fecho), lido em cada run novo enquanto o candidato serve.
- **As admissões de runs novos passam uma a uma pelo livro.** Com um candidato a servir, todas
  as hospedagens de runs novos de uma réplica — as do titular incluídas — esperam pela vez no
  livro, depois do lease e do claim, e cada uma lê-o. Medido na revisão: com a leitura a
  100 ms, dez runs do titular que cheguem juntos esperam um segundo. A espera respeita o
  contexto do run: cancelado, o run falha com a causa de contenção. É o preço de a contagem
  nunca passar do limite; sem candidato a servir não existe.
- O `POST /runs` de um run em curso passou a ler o que está selado para ele (o stream do run e
  o dos registos de retoma, este uma varredura do stream das aprovações inteiro), com ou sem a
  pasta de perfis, e pode responder 409. A hospedagem de um run **novo** não lê nada a mais.
- O nó ganha um evento em claro por run fixado, no stream do run: só identificadores e digests.
- Recuar para um binário anterior a este ADR com runs fixados no candidato em curso faz esses
  runs serem retomados com o titular, em silêncio (o binário antigo ignora a marca). Recua-se
  com a rota drenada, como no PR 0.

## 5. Limites e resíduos

- **O planeador do `aos-orq` chama o modelo fora da governação da rota** e fora deste canary:
  a decomposição de um plano é sempre feita pelo modelo que o `aos-orq` tem configurado. O
  canary cobre os runs do nó, não o planeamento.
- **Quem escolhe o identificador de um plano influencia a escolha** (a função não tem segredo).
  Aceite pelo dono: o canary mede um modelo, não defende de quem dá o nome aos planos.
- **Quem pode submeter runs avulsos com ids à escolha esgota o canary.** Quarenta ids que a
  função dê ao candidato — calculam-se offline — bastam para gastar os 40 lugares, e os planos
  a sério ficam todos no titular. É negação do canary, não de serviço: nenhum run é servido
  pelo modelo errado nem recusado por isso. Aceite com a decisão anterior; o sinal é o canary
  terminar muito antes do esperado, com runs avulsos nas atribuições (`plan_key_kind: run`).
- **O `aos-orq serve` manual** não traz pedido de plano: cada nó é um run independente, e os
  nós de um mesmo plano manual podem cair em modelos diferentes.
- **Retirar a variável da pasta de perfis** (e não só o registo) faz o nó deixar de ler o livro:
  um run novo de um plano já atribuído ao candidato é servido pelo titular. Para retirar um
  candidato, tira-se o registo ou desliga-se o interruptor, com a pasta configurada.
- **Com mais de uma réplica do nó sobre JetStream, o candidato a servir NÃO é suportado**, até
  um defeito do backend JetStream do Event Store estar corrigido: a escrita com concorrência
  optimista compara o `seq` esperado com a vista local do stream, que não é refrescada quando
  outra réplica escreve. A réplica que fica atrás passa a recusar todos os planos novos do
  candidato (`livro_do_canary_em_contencao`) até reiniciar. Nenhum plano é servido pelo modelo
  errado, e a contagem não passa do limite; o que se perde é disponibilidade. Reproduzido em
  `packages/substrate/eventstore/jetstream/aos520_cas_vista_atrasada_test.go`; a quota por
  principal tem o mesmo padrão. O nó não sabe quantas réplicas partilham o Event Store, pelo
  que **avisa** no arranque e não recusa.
- **Réplicas com configurações diferentes** (uma com `on`, outra com `off`; ou com registos
  diferentes) não são suportadas, e a consequência é esta: decidem de maneira diferente para o
  mesmo plano, e podem dá-lo a modelos diferentes. A réplica com `off` só conhece os planos
  atribuídos até ao seu arranque. E `decidir` não olha para a época em vigor de outra réplica:
  cada réplica usa a abertura do **seu** registo, e cada arranque de uma réplica cujo registo
  não é o da última abertura grava uma **época nova**, sem tecto — em crash-loop com
  configuração mista, o stream do livro deixa de ser pequeno, e é lido em cada run novo. A
  configuração do candidato muda-se em todas as réplicas, com a rota drenada.
- **Os runs anteriores a este ADR, e os de um nó sem candidato a servir, não são fixados**: são
  retomados com o modelo do nó, como sempre.
- **A marca de abertura só protege os planos com pedido.** Um run sem plano é um plano de um
  nó, e não tem «antes».
- **Um titular sem perfil de rota** fixa o modelo mas não o digest (o PR 0 não fixa um alias
  sem perfil).
- **O selo do fim é por réplica.** O audit de governação do gateway é local: «selado» quer dizer
  selado no audit da réplica que o leu. Uma réplica que nunca decida nem reinicie depois do fim
  não o tem no seu.
- **Um livro com um evento malformado de um tipo conhecido não se repara** (o stream é
  append-only): o nó não arranca com a pasta de perfis configurada, mesmo com o interruptor a
  `off`. Sai-se retirando a variável da pasta, que é desligar os perfis assinados todos.
- **As séries por modelo são por processo** e desde o arranque, como as de sempre: um reinício
  repõe-nas a zero. Não são a fonte de nenhuma decisão.
- **Por fazer:** o smoke sobre JetStream; o disjuntor (AOS-521).

## 6. Relação com outros ADR

- **ADR-018** — a fronteira nó↔ORQ não muda: o `aos-orq` não escolhe modelo, não conhece o
  canary e não foi alterado. O nó continua a ser a fonte de verdade do ciclo de vida do run.
- **ADR-021** (proposto) — o scoring por tiers escolhe uma rota por pedido; este ADR fixa o
  alias por run. Os dois só se encontram na fase A4. Hoje o nó não arma tiers.
- **ADR-012** — a auto-modificação pede staging, eval-gate, canary e ratificação assinada. Aqui
  o «eval-gate» é o veredicto do banco de ensaio citado no registo (AOS-518), a ratificação é
  a assinatura do dono no registo (AOS-519), e o canary é este. **Discrepância conhecida, que
  não é deste ADR:** o texto do ADR-012 ainda afirma que `NewProductionRatificationGate` não tem
  chamador de produção; o nó compõe-o hoje (`packages/cmd/aos/bootstrap.go`, AOS-206). O perfil
  de um modelo não passa por esse gate — entra pela via assinada do AOS-519.
- **ADR-036 §2.3 e §2.8** — o manifesto do turno e o perfil da rota. A fixação do digest do
  perfil (PR 0) é a base desta; a conferência passa a usar o alias fixado no run.
- **ADR-040 §2.11** — o estado opaco só volta à rota que o produziu. Com o modelo fixado por
  run, um run não muda de rota; e pela via assinada só entram perfis que não devolvem estado (D8).
- **ADR-010** — o replay de um run do candidato reproduz-se das capturas, como qualquer outro;
  o `run.model_pinned` é um facto do log, não uma entrada do replay.
- **ADR-025** — o precedente de «despromove sozinho, só um humano devolve», que o AOS-521 segue.
- **ADR-027, ADR-035, ADR-039** — cada nó do plano é um run do nó; o nó deriva o pedido de que
  o run é trabalho; a nova tentativa é um run novo do mesmo pedido — e por isso do mesmo modelo.
- **ADR-029** — o nome do stream do livro não tem pontos; **ADR-030** — a marca de abertura é um
  `seq` da fila de pedidos de plano.

## 7. O que cada ticket implementa

| Ticket | O que entrega |
|---|---|
| AOS-519 | O registo de entrada assinado, com a fatia (lida, sem efeito) |
| AOS-520, PR 0 | O digest do perfil fixado por run (ADR-036 §2.8) |
| AOS-520, PR 1 | §2.1 a §2.7, e as duas métricas do canary do §2.8 |
| AOS-520, PR 2 | As séries por modelo do §2.8 |
| AOS-521 | §2.9, o disjuntor (por fazer) |
