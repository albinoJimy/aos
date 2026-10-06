# ADR-039 — A recuperação de um nó que não chamou a tool é uma nova tentativa do nó do plano, autorizada pelo nó

- **Estado:** Aceite
- **Data:** 2026-10-06
- **Deciders:** Dono do produto (decisões de 2026-10-06: o sistema tenta outra vez sozinho um
  passo em que o modelo não usou a ferramenta; até duas tentativas a mais, três runs no máximo
  por nó do plano; aplica-se a todos os nós não-verificadores com tools, com ou sem `consumes`;
  as tentativas contam no orçamento de quem pediu, com um tecto de tentativas a mais por plano;
  um sucesso à segunda ou à terceira aparece como sucesso normal, com a contagem registada; não
  há «aviso e mais um turno» nesta fase; a medição directa ao modelo não está autorizada) ·
  executor de AOS-502 e AOS-503
- **Tickets:** AOS-502 (o nó `aos` aceita a tentativa e prova, no seu log, que a anterior não
  pediu tools) e AOS-503 (o `aos-orq` grava o facto e volta a submeter o nó do plano),
  implementados; por rever de forma independente e por verificar em produção. As duas metades
  nascem desligadas (`AOS_RUN_RETRY_MAX` a zero no nó; `AOS_ORQ_NOVA_TENTATIVA=off` no `aos-orq`)
  e só ligam por decisão do dono.
- **Relacionados:** ADR-001 (execução durável ao nível do passo; a chave de idempotência é
  `f(run_id, step_id)`), ADR-002 (Reference Monitor), ADR-018 (o nó não conhece o documento do
  plano), ADR-022 (extensões ao grafo de plano), ADR-023 (posse do run do plano), ADR-027 (cada nó
  do plano é um run do nó), ADR-030 (não-oracularidade das rotas do nó), ADR-034 (autorização
  derivada do contexto), ADR-035 (o submissor do plano é derivado pelo nó), ADR-037 (o desfecho
  de um run é um veredicto do kernel), ADR-038 (a saída por referência)

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

### 2.6 O facto fica no log do plano antes do pedido

`plan.node_attempt_started{plan_id, node_id, attempt, retry_of, reason}`, com construtor
validado e um passo por (nó, tentativa): a primeira escrita é o facto, e uma retoma que o volte a
gravar não o duplica. Escreve-se **antes** do `POST /runs` da tentativa.

**A retoma lê primeiro.** A tentativa corrente de cada nó é a maior que o log regista. Um `serve`
que encontre um nó `running` com uma tentativa registada lê o estado do id dessa tentativa: se o
run existe, segue-o; se o nó `aos` responde 404, o processo anterior morreu entre o facto e o
pedido, e submete-a — uma vez. Nunca reenvia às cegas. Os factos lêem-se com o interruptor em
qualquer valor; o que `off` e `observe` deixam de fazer é começar tentativas.

### 2.7 O pedido repetido é o mesmo

A tentativa sai do mesmo código que a primeira submissão: o corpo difere só no `run_id` e em
`plan_request.attempt`. O objectivo, as tools, o contrato de conclusão, a origem declarada e os
`inputs` (conteúdo e digests) são iguais byte a byte. A recuperação não escreve nada ao modelo:
não há instrução nova, nem autoridade nova, nem eco.

O nó mede-o depois: compara o `prompt_hash` do primeiro turno da tentativa com o do turno da
anterior, e conta as diferenças em `aos_runs_retry_prompt_hash_diferente_total`, que tem de ser
zero. É medição e alerta, e não condição de hospedagem — o hash só existe depois de o prompt
estar montado.

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
o `run.plan_origin` — que a partir da segunda tentativa leva `attempt` e `retry_of`. No plano: um
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
  rota determinista.
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
- **A forma do id não é injectiva entre pedidos.** O id da tentativa `n` do nó `N` do pedido `P`
  é o da primeira tentativa do nó `"n"` de um pedido cujo id seja literalmente `P~N` (o
  `POST /plans` admite `~` no id). A prova não é afectada — a origem do run anterior tem de ser a
  do mesmo pedido e do mesmo nó —, e a colisão cai na regra de re-submissão do nó: 409 com
  credencial forte e a mesma região, que o `aos-orq` trata como recusa da tentativa. Não é classe
  nova: o `POST /runs` nunca reservou a forma `<plano>~<nó>`.
- **A medição do `prompt_hash` vive na memória do nó.** Um reinício entre a admissão e o fim da
  tentativa perde a comparação desse run.
- **A validade do NHI não se lê à parte.** O prazo do `serve` fica abaixo dela por construção; um
  NHI expirado é uma 403 do nó e conta como `recusada_pelo_no`.
- **Um binário `aos-orq` anterior não segue as tentativas.** Medido com o binário da base sobre
  um plano (sem ramos condicionais) que ficou com a tentativa 2 em voo: o binário anterior não lê
  o facto `plan.node_attempt_started`, sonda o run da PRIMEIRA tentativa — que está `failed` —,
  fecha o nó do plano `failed` com a causa desse run e o plano sai 13, sem publicar nada. A
  tentativa em voo fica órfã no nó `aos`: corre até ao fim, e ninguém a recolhe. Num plano COM
  ramos condicionais o despacho lê as decisões de ramo pela reconstrução do domínio do plano, que
  falha fechado num tipo `plan.*` desconhecido (inferido do código, não medido): aí o `serve` sai
  com erro e o pedido volta à fila até o tecto de gerações o fechar. Nos dois casos nada de uma
  tentativa é publicado. O rollback faz-se pelo interruptor, e não pela imagem, enquanto houver
  planos com tentativas em curso.
- **Um `serve` que aborta não larga a posse.** A retoma de um plano cujo `serve` saiu com erro
  espera pelo TTL da posse (ADR-023); a recuperação não muda isso.

## 6. O que cada ticket implementa

- **AOS-502 — o nó.** `plan_request.attempt`; a forma `<plano>~<nó>~<n>`; a prova do §2.3; o
  tecto `AOS_RUN_RETRY_MAX`; o anúncio no `GET /tools`; `attempt` e `retry_of` no
  `run.plan_origin`; as três séries `aos_runs_retry_*`.
- **AOS-503 — o `aos-orq`.** O interruptor `AOS_ORQ_NOVA_TENTATIVA` (`off`, `observe`, `on`); a
  elegibilidade do §2.5; o facto `plan.node_attempt_started` antes do pedido; a retoma do §2.6; o
  tecto por plano; a entrega por referência sobre a tentativa que teve êxito; as métricas e o
  `detail` do desfecho.

## 7. Emendas a outros ADR

- **ADR-027 §2.1 e §2.4:** o id do run filho admite o sufixo de tentativa; `failed` deixa de
  fechar sempre o nó do plano.
- **ADR-035 §2.2 e §5:** a forma `<plano>~<nó>~<n>`, e o que o nó prova para `n ≥ 2`.
- **ADR-037 §5:** «sem reparação» continua verdadeiro no kernel; a recuperação vive no plano e
  não muda o veredicto de nenhum run.
