# ADR-037 — O desfecho de um run é um veredicto do kernel sobre um contrato de conclusão

- **Estado:** Aceite
- **Data:** 2026-10-04
- **Deciders:** Dono do produto (decisões de 2026-10-04: contrato inferido das tools do nó, e
  «não cumprido» gravado como `failed` com razão própria) · executor de AOS-493 (implementação)
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

### 2.7 O replay reproduz o veredicto

O motor de replay chama `ConcludeRun` com o modo e o contrato que o manifesto do turno terminal
gravou, e com contadores refeitos dos resultados que a captura registou. Não conhece a
configuração do nó que reproduz. Um turno gravado sem `manifest.completion` reproduz-se com o
desfecho de sempre: concluído, sem veredicto.

A regra nova entra por estes campos e não por um layout de prompt novo. Um layout é a forma dos
bytes do prompt, e esta decisão não muda nenhum. Um layout que a projecção nativa não conhecesse
cairia em silêncio na projecção de texto.

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
- `failed` é a origem da saga de compensação. Um run não cumprido que tenha feito efeitos entra
  nela, como qualquer outro run falhado.
- Em imposição, um run sem contrato cujo último turno foi cortado ou veio vazio passa a `failed`.
- O manifesto de cada turno de um run com veredicto ganha o campo `completion`. Um run com o modo
  desligado grava os bytes de antes.
- Enquanto o desfecho de um run não cumprido estiver em memória, o `GET /runs/{id}` responde
  `status: "completed"` com `terminated=false` e sem texto final; depois lê o estado durável e
  responde `failed`. O `aos-orq` decide por `terminated`, pelo que dá o nó por falhado nos dois
  casos. Corrigir esse `status` e devolver a razão é do AOS-494.

## 5. Resíduos declarados

- **Evidência de tool call não é fidelidade da saída.** O veredicto prova que a tool exigida
  correu com êxito pelo menos uma vez. Não prova que o texto final deriva do que ela devolveu: um
  run que chama a tool e publica outra coisa cumpre o contrato. Só a saída por referência ao
  resultado da tool fecha isto, e só para nós de passagem directa.
- **Tool call escrita como texto num turno posterior**, depois de uma chamada efectiva. O
  contrato está cumprido e o run conclui. Não há critério estrutural que o apanhe.
- **Desistência depois de uma recusa posterior à primeira chamada efectiva.** O vector regista
  que a última chamada foi recusada; o veredicto é positivo.
- **Contrato com mão larga.** Uma tool exigida que o objectivo afinal não precisava dá um
  vermelho falso. O âmbito em que o `aos-orq` declara o contrato é do AOS-495.
- **Sem reparação.** Um veredicto negativo fecha o run; não há outro turno para o modelo.
- **Retoma depois de uma falha de tool.** Uma tool que falhou numa vida do run volta a correr na
  retoma e pode ter êxito. O veredicto selado é o da vida que terminou; a captura do turno é a da
  vida em que ele foi gravado, e o replay pode chegar a outro vector.
- **`content_filter` e motivos de paragem desconhecidos** concluem como hoje.
- **O modo é do nó, não do run.** Dois nós com modos diferentes dão desfechos diferentes ao mesmo
  objectivo.
