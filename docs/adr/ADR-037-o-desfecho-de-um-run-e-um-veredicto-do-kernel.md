# ADR-037 — O desfecho de um run é um veredicto do kernel sobre um contrato de conclusão

- **Estado:** Aceite
- **Data:** 2026-10-04
- **Deciders:** Dono do produto (decisões de 2026-10-04: contrato inferido das tools do nó, e
  «não cumprido» gravado como `failed` com razão própria) · executor de AOS-493 (implementação)
- **Revisto:** 2026-10-05, depois da revisão adversarial independente: §2.2 (contrato impossível),
  §2.7 (o que o replay reproduz), §4 (saga de compensação) e §5 (retoma, medição)
- **Emendado:** 2026-10-05, por AOS-497 (ADR-038): §2.4 (duas razões novas e a sua precedência)
  e §5 (o resíduo «evidência não é fidelidade» remete para o ADR-038)
- **Tickets:** AOS-493
- **Relacionados:** ADR-001 (execução durável ao nível do passo), ADR-002 (Reference Monitor),
  ADR-010 (manifesto por trajectória e replay), ADR-018 (o nó é a autoridade sobre o run),
  ADR-027 (cada nó do plano é um run do nó), ADR-034 (autorização derivada do contexto),
  ADR-036 (o tail é a forma canónica da conversa)

## 1. Contexto

Uma condição no loop respondia a três perguntas: «o modelo não pediu tools», «o run acabou» e «o
run acabou bem». Um turno sem tool calls fechava o run como concluído, fosse o texto uma
resposta, uma tool call escrita como texto, nada, uma resposta cortada ou uma recusa. A ausência
de tool calls é uma não-ocorrência; o código tratava-a como um acto.

Medido em produção a 2026-10-04 (v0.1.45): em dois de dez planos o nó de leitura respondeu num
turno, com a chamada a `doc_read` escrita como texto e nenhuma tool call nativa. O run fechou
`completed`, o `aos-orq` publicou o texto como saída do nó, e os dois planos saíram com
`exit_code=0` sem cumprir o objectivo. Antes dessa versão, 13 de 86 runs com tools na oferta
tinham concluído sem pedir nenhuma
(`docs/reports/analise-fronteira-runtime-modelo-2026-10-04.md`).

O AOS-491 trouxe o motivo de paragem do provider até ao runtime e à captura. O AOS-492 pôs a
regra de terminação numa função única, partilhada pelo loop e pelo motor de replay.

## 2. Decisão

### 2.1 Terminar não é concluir

`TurnEndsRun` continua a dizer se o run tem mais turnos, e só isso. Se o run acabou cumprido é
outra função, `ConcludeRun`, que o loop e o motor de replay chamam no turno terminal. A ausência
de tool calls deixa de ser, por si, conclusão.

### 2.2 O contrato de conclusão

O objectivo de um run pode declarar um contrato de conclusão: a lista de tools de que a conclusão
depende (`Goal.CompletionRequires`, por nome). Quem o declara é quem compõe o run; nunca sai de
conteúdo do modelo. O contrato está cumprido quando cada tool da lista teve pelo menos uma
chamada **efectiva** no run.

**Um contrato impossível não arranca.** Antes do primeiro turno o kernel verifica que cada tool
do contrato pode ser chamada neste run: consta do tool set do run e, havendo lista-branca, está
nela. Se não, o run é recusado com um erro próprio (`ErrImpossibleCompletionContract`), sem
gravar eventos nem interrogar o modelo. Sem esta verificação o run gastava todos os turnos e
acabava em `contract_unmet_no_call`, que diz «o modelo não chamou» de um defeito de quem compôs
o run. A comparação de nomes é exacta, como a do Reference Monitor: um nome com outra caixa ou
com espaços não é a tool, e cai nesta recusa. O nó sela o run recusado como um erro de loop
(`failed`, razão de auditoria `run_failed`, sem `outcome_reason`).

A recusa não depende do modo, excepto em `off`, onde o contrato não é lido. Em `observe` isto
muda o desfecho de um run com contrato impossível: arrancava e concluía, e passa a não arrancar.
É aceitável porque nenhum contrato chega ainda pela API (o campo no `POST /runs` é do AOS-494):
hoje só código declara um contrato, e nenhum run de produção o tem. Quando o AOS-494 e o AOS-495
o trouxerem, um contrato impossível passa a ser um erro de quem submete, visível à primeira.

### 2.3 A evidência são os contadores do loop

Uma chamada é efectiva quando foi despachada, sem recusa e sem erro de tool. É a condição com que
o loop já confirma a activity no checkpoint. Uma chamada escalada conta como recusa: nenhum
efeito ocorreu.

Os contadores são do próprio loop, somados das mesmas entradas que o tail e a captura do turno. O
veredicto não lê o evento de desfecho da tool (`tool.call.outcome`), que é opcional e fail-open:
uma evidência lida de lá perdia-se quando essa escrita falhasse, e um run que leu o documento
saía vermelho.

### 2.4 O veredicto é um vector, com razão em vocabulário fechado

Por cada tool do contrato, o veredicto leva as chamadas pedidas, efectivas, negadas e falhadas e
o desfecho da última. A razão de um veredicto negativo é uma de:

| `outcome_reason` | Quando |
|---|---|
| `truncated` | O turno terminal parou com o motivo `length` |
| `contract_unmet_no_call` | A tool em falta nunca foi pedida |
| `contract_unmet_after_denial` | O último pedido da tool em falta foi recusado |
| `contract_unmet_after_tool_error` | O último pedido da tool em falta falhou na execução |
| `empty_output` | O turno terminal não trouxe texto |

A ordem da tabela é a de precedência. A razão do contrato é a da primeira tool em falta, na ordem
em que o contrato foi declarado. `truncated` e `empty_output` valem com ou sem contrato.

**Emenda de 2026-10-05 (AOS-497, ADR-038 §2.4).** O vocabulário ganha duas razões, que só
existem num run que declarou a origem da saída como **vinculativa** e com o nó em imposição:

| `outcome_reason` | Quando |
|---|---|
| `output_source_missing` | Nenhuma chamada é designável como origem da saída: a âncora está em `missing` ou em `inapplicable` (ADR-038 §2.2) |
| `output_source_ambiguous` | A tool declarada foi pedida mais de uma vez no turno da designação |

Na precedência entram **depois** das três razões do contrato e **antes** de `empty_output`:
`truncated`, as do contrato, as da origem, `empty_output`. O contrato vem primeiro porque diz
porquê não há chamada efectiva. Nesse mesmo caso (declaração vinculativa em imposição)
`empty_output` avalia os bytes do resultado designado e não o texto do turno terminal. Num run
sem declaração, com a declaração «só medição», ou com o nó em observação, a tabela acima vale
sem alteração.

Um motivo de paragem `content_filter`, ou fora do mapa conhecido, não é veredicto negativo. O
vocabulário de outros providers não está medido, e tratar o desconhecido como falha poria
vermelhos todos os runs de um provider que use outra palavra para «acabei».

### 2.5 O estado durável é `failed`; não há estado novo

Um run com veredicto negativo, em modo de imposição, termina no estado durável `failed`, com a
razão de auditoria `objective_unfulfilled` e sem texto final. O evento da transição terminal leva
`outcome_reason` e o vector (`verdict`). Um `status` que um `aos-orq` anterior não conhecesse
deixava-o a sondar até ao prazo.

### 2.6 O modo de aplicação é do nó e fixa-se por run

O nó escolhe no arranque (`AOS_COMPLETION_VERDICT`) o que faz com o veredicto nos runs novos:

- `observe` (por omissão): calcula-o, grava-o na transição terminal, conta-o e di-lo no log. O
  desfecho não muda.
- `enforce`: um veredicto negativo fecha o run em `failed`.
- `off`: não calcula nada.

Um valor desconhecido recusa o arranque. O modo e o contrato ficam no registo de retoma e no
manifesto de cada turno (`manifest.completion`). Um run retomado continua no modo em que começou;
um registo de retoma sem o campo é de um run anterior a esta decisão e retoma-se sem veredicto.

### 2.7 O replay reproduz o veredicto dos runs de uma só vida

O motor de replay chama `ConcludeRun` com o modo e o contrato que o manifesto do turno terminal
gravou, e com contadores refeitos dos resultados que a captura registou. Não conhece a
configuração do nó que reproduz. Um turno gravado sem `manifest.completion` reproduz-se com o
desfecho de sempre: concluído, sem veredicto.

O modo que vale é o do turno **terminal**. Num log misto (os turnos de um run não gravaram todos
o mesmo `manifest.completion`, que é o que um rollback do binário a meio do run deixa) quem selou
o run foi quem deu o último turno, e é o manifesto desse turno que diz com que regra.

Isto vale para um run que correu numa só vida, ou cuja retoma reproduziu os turnos anteriores com
o mesmo desfecho. Não vale para um run retomado cujo turno re-executado mudou de desfecho: ver §5.

A regra nova entra por estes campos e não por um layout de prompt novo. Um layout é a forma dos
bytes do prompt, e esta decisão não muda nenhum. Um layout que a projecção nativa não conhecesse
cairia em silêncio na projecção de texto.

### 2.8 Ler o desfecho depois de um reinício decifra, sob o selo da leitura de desfecho

Nota de 2026-10-05 (AOS-494 e a sua revisão adversarial). O `GET /runs/{id}` de um run concluído
que já não está na memória do nó lê a saída da captura do turno terminal, cifrada por-titular.
É decifração de conteúdo histórico do titular numa rota que, até ao AOS-494, não decifrava nada.

As verificações, pela ordem em que correm:

1. a autorização soberana do leitor (credencial, board→região, residência do run): a mesma
   chamada da reconstrução soberana (`GET /runs/{id}/reconstruct`), antes de qualquer ramo;
2. a trava do AOS-426: o stream de onde saiu o estado `complete` é o de um run com esse id;
   senão, o 404 uniforme;
3. o selo WORM de leitura sensível, **antes** de abrir conteúdo; se o WORM não selar, 503;
4. o gate soberano e a cifra por-titular compostos; sem eles não se decifra;
5. a decifração, pelo motor de replay com o cifrador por-titular.

**O selo diz `read:outcome`, e não `read:reconstruct`.** É a mesma resposta, ao mesmo leitor,
que o ramo em memória entrega sob `read:outcome`: o rótulo diz o que o leitor recebeu (o texto
final do run), e não de onde o nó o foi buscar. `read:reconstruct` fica para a rota que entrega
todos os turnos e as saídas das tools. O rótulo não autoriza nada: nenhuma política decide pela
capability do selo, e a autorização é a do ponto 1, igual nas duas rotas.

O que isto custa a quem audita: quem conta decifrações só por `read:reconstruct` não vê estas.
As leituras que podem ter entregue conteúdo do titular são as de `read:outcome` e as de
`read:reconstruct`. O selo `read:outcome` não distingue a leitura servida da memória da que
decifrou o log, nem a que acabou sem conteúdo: é anterior à decifração.

**Indisponível não é apagado.** Se a saída não se lê de vez (titular apagado, captura em falta,
incompleta ou corrompida, nó sem o gate de leitura) a resposta é `completed` com
`output_unavailable`. Se não se lê agora (custódia das KEK fechada ou sem resposta, Event Store
que não leu, ou um erro que ninguém classificou) a resposta é 503, como na reconstrução
soberana, e nenhum desfecho é escrito. A lista fechada é a dos erros definitivos; o resto cai do
lado de voltar a perguntar.

## 3. Alternativas rejeitadas

- **Detectar a forma do texto** (o texto «parece» uma tool call) como critério de controlo. É um
  classificador de conteúdo untrusted a decidir o desfecho: um canal do plano de dados para o
  plano de controlo. As duas respostas de produção também não seguem formato declarado nenhum:
  uma imita o cabeçalho de segmento do AOS, a outra é XML de outra família. Pode existir como
  rótulo de medição.
- **Parser tolerante no gateway**, que converte o texto em tool call. O nó de leitura transcreve
  o documento, pelo que ecoar texto untrusted é o seu comportamento normal: um documento que
  contenha uma tool call em texto seria executado, com o argumento escolhido por quem o escreveu.
  A lista-branca do run deixa passar a tool, e o gate de taint só nega capabilities privilegiadas.
  Com a capability armada a chamada é negada, e nenhum documento com esse texto se consegue
  transcrever.
- **Terminação explícita por acto** (o run só conclui quando o modelo chama uma tool de fim).
  Põe a conclusão no mesmo canal em que o modelo de produção falha. Com cerca de 20% de falha por
  acto nativo, exigir mais um acto dá uma estimativa de 8% a 10% de runs vermelhos (assume
  independência entre actos, que não foi medida). Um modelo sem tool calling nunca concluiria.
- **`tool_choice` forçado.** O suporte varia entre providers, vários modelos com raciocínio não o
  aceitam, e o proxy de produção corre com `drop_params: true`: o parâmetro seria retirado sem
  aviso e o manifesto registaria uma estratégia que não foi aplicada.
- **Um estado `unfulfilled` na máquina de estados.** Obrigava a mexer na tabela de transições e
  em todos os leitores do estado, e um `aos-orq` anterior não o reconheceria como terminal.
- **Ler a evidência do evento de desfecho da tool.** É fail-open (§2.3).

## 4. Consequências

- Com contrato e em imposição, o verde falso medido passa a vermelho com razão: o run termina
  `failed`, o `aos-orq` dá o nó por falhado e o plano não sai com `exit_code=0`.
- Em observação nada muda no desfecho. A série `aos_runs_finished_total{outcome="complete"}` com
  uma razão diferente de `none` diz quantos runs a imposição teria fechado em `failed`.
- **Um run não cumprido é `failed`, e segue o caminho de qualquer `failed`.** `failed` é a
  origem da saga de compensação, e o run entra nela. Os efeitos das tools que correram bem ficam
  sujeitos às compensações que estiverem registadas. **Hoje não há nenhuma**: o loop não regista
  compensações, a saga não desfaz nada, declara a ausência no log e no WORM (partição da saga,
  razão `saga_no_compensation_registered`, uma escrita por run) e o run fica em `failed` com o
  efeito **aplicado**. Um run cujo contrato é `[doc_read, doc_write]`, que escreveu e nunca leu,
  acaba `failed` com a escrita feita.
- **Ponto a decidir antes de haver compensações reais.** No dia em que uma tool registar a sua
  compensação, a saga passa a desfazer, num run não cumprido, efeitos de tools que correram bem —
  incluindo os que cumpriam parte do contrato. Pode ser o que se quer (o run não concluiu, nada
  do que fez fica) ou não (a escrita era boa, faltou só a leitura). A decisão não está tomada, e
  `objective_unfulfilled` é a razão de auditoria por que a saga os pode distinguir de um erro de
  loop. O comportamento de hoje está fixado por teste, para a mudança se ver.
- Um run recusado por contrato impossível (§2.2) também é `failed` e também entra na saga, onde
  não há efeito nenhum a desfazer: não chegou a dar um turno.
- Em imposição, um run sem contrato cujo último turno foi cortado ou veio vazio passa a `failed`.
- O manifesto de cada turno de um run com veredicto ganha o campo `completion`. Um run com o modo
  desligado grava os bytes de antes.
- O `GET /runs/{id}` de um run não cumprido responde `status: "failed"`, `terminated=false`, com
  `outcome_reason` e o vector, e sem texto final, quer o desfecho esteja em memória quer se leia
  do log (AOS-494). Até ao AOS-494 o ramo em memória respondia `status: "completed"`.

## 5. Resíduos declarados

- **Evidência de tool call não é fidelidade da saída.** O veredicto prova que a tool exigida
  correu com êxito pelo menos uma vez. Não prova que o texto final deriva do que ela devolveu: um
  run que chama a tool e publica outra coisa cumpre o contrato. Só a saída por referência ao
  resultado da tool fecha isto, e só para nós de passagem directa. **Nota de 2026-10-05:** é a
  decisão do ADR-038. Com AOS-497 o kernel designa e sela a origem da saída (a âncora, com o
  digest do resultado); o resíduo fecha para a passagem directa quando a entrega se fizer pelos
  bytes designados (ADR-038 §2.6, §6).
- **Tool call escrita como texto num turno posterior**, depois de uma chamada efectiva. O
  contrato está cumprido e o run conclui. Não há critério estrutural que o apanhe.
- **Desistência depois de uma recusa posterior à primeira chamada efectiva.** O vector regista
  que a última chamada foi recusada; o veredicto é positivo.
- **Acabar sobre uma recusa ou uma falha de tool é medido, e não é veredicto negativo.** Um run
  com o contrato cumprido, ou sem contrato, que acaba logo a seguir a uma recusa ou a uma falha
  de tool tem veredicto positivo e sai `reason="none"`. A análise pedia uma classe própria para
  estes runs; ficou como medição e não como razão negativa nova, porque um run pode acabar bem
  depois de uma recusa (pediu o que não podia, e respondeu com o que tinha) e o kernel não tem
  como os separar sem ler o texto. A família
  `aos_runs_finished_by_last_tool_outcome_total{outcome,verdict,last}` conta cada run selado pelo
  desfecho do último turno que despachou tool calls, de qualquer tool: `none`, `effective`,
  `denied` (pelo menos uma recusada) ou `tool_error` (nenhuma recusada e pelo menos uma falhou).
  É o pior do turno e não a última chamada: as chamadas de um turno chegam juntas ao modelo. A
  classe em causa é `verdict="none"` com `last="denied"` ou `last="tool_error"`. Não vai em
  evento nenhum; o período de observação lê-a do `/metrics`.
- **Contrato com mão larga.** Uma tool exigida que o objectivo afinal não precisava dá um
  vermelho falso. O âmbito em que o `aos-orq` declara o contrato é do AOS-495: todo o nó
  não-verificador com tools atribuídas, com ou sem saída declarada. A revisão adversarial
  alargou-o (o critério inicial exigia uma saída de forma aberta), o que aumenta este resíduo
  para os nós sem saída declarada; é o que o período de observação mede.
- **Sem reparação.** Um veredicto negativo fecha o run; não há outro turno para o modelo.
- **Retoma depois de uma falha de tool: dois ramos, medidos.** A retoma reproduz os turnos já
  dados. Uma chamada que teve êxito tem o resultado memorizado e não volta a executar. Uma que
  **falhou** não tem, e é mediada outra vez. O que acontece depende de a retoma ter credencial:
  - **Retoma por crash (a varredura de arranque), sem credencial.** A chamada é **negada** pelo
    Reference Monitor na segunda vida: não há credencial de agente com que a autorizar. A tool
    não volta a executar. O vector sai «pedida 1, negada 1» e, em `enforce`, o run sela `failed`
    com `contract_unmet_after_denial` — a razão diz «recusa» de uma chamada que na primeira vida
    foi permitida e falhou na execução. A causa é anterior a esta decisão (a re-hospedagem por
    crash nunca teve credencial); passou a decidir o desfecho.
  - **Retoma com credencial (a retoma por aprovação).** A chamada volta a correr e pode ter
    êxito; nesse caso o contrato fica cumprido e o run conclui.

  Nos dois ramos o veredicto selado é o da vida que terminou.
- **O replay destes runs pára em divergência; não reproduz veredicto nenhum.** A captura do turno
  re-executado é a da primeira vida (a deduplicação do Event Store fica com a primeira escrita),
  e o turno seguinte foi gravado na segunda, com outro resultado no tail. O motor remonta o
  prompt do turno seguinte a partir da captura, o `prompt_hash` não bate com o gravado, e o
  replay pára aí: divergência de `prompt_hash`, fidelidade parcial, sem desfecho e sem veredicto.
  Medido nos dois ramos acima. Vale por inferência para o caminho normal de aprovação humana (a
  chamada escalada na primeira vida e aprovada na segunda), que não foi reproduzido. É uma classe
  anterior a esta decisão — um run retomado cujo turno re-executado muda de desfecho nunca se
  reproduziu com fidelidade total —, e o veredicto não a agrava nem a resolve: o replay não
  contradiz o desfecho selado, mas também não o confirma.
- **`content_filter` e motivos de paragem desconhecidos** concluem como hoje.
- **O modo é do nó, não do run.** Dois nós com modos diferentes dão desfechos diferentes ao mesmo
  objectivo.
