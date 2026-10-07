# Desenho A2 — estado opaco do provider por turno, e a resposta vazia de um nó sem tools

> **Nota de entrada no repositório (2026-10-07).** Este documento é o desenho apresentado ao dono,
> revisto para ficar auto-contido. **Os tickets AOS-507 a AOS-511 são a fonte que vale onde
> divergirem deste documento.** O que foi corrigido na entrada está na §10.
>
> **Decisões do dono (2026-10-07):**
>
> 1. **D1 — sim.** Mede-se a forma das respostas em produção, sem conteúdo (AOS-507).
> 2. **D2 — sim, primeiro só a contar.** Um passo sem ferramentas que devolve uma resposta vazia
>    é repetido pelo sistema; o `aos-orq` entra em `observe` antes de `on` (AOS-510, AOS-511).
> 3. **D3 — não.** O raciocínio do modelo nunca é usado como resposta (§3b; registado no AOS-509).
> 4. **D4** (qual é o segundo modelo) e **D5** (posto de ensaio com o modelo real) estão **por
>    tomar**. O trabalho que depende delas fica planeado e **sem número**: a gama `AOS-NNN` só
>    cresce quando um ticket é aberto. Neste documento esses itens têm rótulos provisórios —
>    «A2-banco», «A2-perfil», «A2-estado», «A2-projecção» e «A2-família» — que não são
>    identificadores de ticket.
>
> Data: 2026-10-07. Base lida: `feature/AOS-128-ux-dx-tests` @ `35109224`, e conferida de novo
> em `8b692c6f` (entre os dois commits não há nenhuma diferença em `packages/`; as referências
> ficheiro:linha valem para os dois). Produção: v0.1.51. Trabalho de leitura e desenho: nada foi
> pedido ao modelo nem a produção.
>
> **Como ler as marcas.** `[CÓDIGO]` = lido no código, com ficheiro:linha. `[MEDIDO]` = número de
> produção registado no documento de acompanhamento (`acompanhamento-arquitectura-alvo-fronteira-modelo.md`, §5). `[HIPÓTESE]` = não confirmado.
> `[NÃO VERIFICADO]` = não li o sítio que o confirmaria.
>
> Abreviaturas de caminho: `adapters/openai_http.go` = `packages/platform/model-gateway/internal/adapters/openai_http.go`;
> `port/…` = `packages/platform/model-gateway/port/…`; `runtime_adapter.go` e `projection.go` =
> `packages/platform/model-gateway/…`; `model.go`, `completion.go`, `termination.go` =
> `packages/kernel/agent-runtime/…`; `captura` = `packages/kernel/agent-runtime/replay/nondeterminism_capture.go`.

## 0. O que muda em relação ao que se supunha

Três achados da leitura corrigem a formulação do problema. Estão à cabeça porque mudam a ordem do trabalho.

1. **O adaptador JÁ lê `reasoning_content`.** `[CÓDIGO]` `port/port.go:92` declara o campo, `port/port.go:103-131`
   aceita-o em qualquer forma JSON, `runtime_adapter.go:379` leva-o para `ModelResponse.Reasoning`, e a
   captura guarda-o selado (`captura:91`, `:466`, redigido em modo sensível `:483-486`, devolvido na retoma
   `:563`). A hipótese «o conteúdo veio em `reasoning_content`, que o adaptador não lê» está, nessa forma,
   **errada**: se veio nesse campo, está na captura dos três runs, hoje — dentro do envelope
   selado quando há cifra por titular, e trocado por uma referência se o nó correr em modo
   sensível (`captura:483-486`). O que o adaptador não lê
   são os **outros nomes** do raciocínio (`reasoning`, `thinking_blocks`, `reasoning_details`,
   `provider_specific_fields`) — ver §1.
2. **Há respostas que hoje não dão «vazio», dão erro.** `[CÓDIGO]` `content` é `string` (`port/port.go:72`) e
   `function.arguments` é `string` (`port/port.go:56`). Um provider que mande `content` como lista de partes,
   ou `arguments` como objecto JSON, faz o `encoding/json` recusar a resposta **inteira**
   (`port/port.go:110-112` → `port/normalize.go:167-169` → `adapters/openai_http.go:122-125`). É o mesmo
   defeito que o AOS-490 fechou para o `reasoning_content` (`port/port.go:98-102`), ainda aberto para estes
   dois campos. Para «qualquer modelo novo entra sem trabalho» isto pesa mais do que o raciocínio.
3. **O identificador de tool call do provider nunca volta.** `[CÓDIGO]` `tool_calls[].id` é descodificado
   (`port/port.go:62`) e deitado fora na tradução: `runtime_adapter.go:380-385` copia só o nome e os
   argumentos, e `ToolInvocation` não tem campo de id (`model.go:67-95`). O que volta ao provider é o id do
   runtime, `<passo>-tool-<n>` (`projection.go:502`, `:632-636`, `:656`). Funciona com o provider de produção
   `[MEDIDO: 2026-10-04, 0 repetições]`; é o primeiro ponto de quebra previsível numa rota que valide a forma
   ou o comprimento do id `[HIPÓTESE]`.

## 1. O que é «estado opaco do provider» neste código, hoje

**Definição operacional.** Tudo o que o provider devolve numa resposta e que (a) o runtime não interpreta e
(b) o provider pode exigir, ou aproveitar, de volta no turno seguinte: raciocínio, assinaturas de raciocínio,
identificadores de chamada, recusas, impressões digitais do sistema.

### 1.1 Caminho da resposta (síncrono — o único que os runs usam)

`adapters/openai_http.go:113-132` (POST, corpo limitado a 1 MiB em `:210`) → `port.UnmarshalChatResponse`
(`port/normalize.go:165-173`) → `translateResponse` (`runtime_adapter.go:335-393`) → `ModelResponse`
(`model.go:98-178`) → captura e `turn.recorded`. O `encoding/json` ignora em silêncio qualquer chave que a
struct não declare; não há `DisallowUnknownFields`.

| Campo da resposta | O que acontece | Onde `[CÓDIGO]` | Chega onde |
|---|---|---|---|
| `choices[0].message.content` (string) | Lido | `port/port.go:72`; `runtime_adapter.go:376` | `Text` → captura (selado), veredicto, `history` do tail se o turno teve tools |
| `content: null` | Vira `""`, sem erro | `port/port.go:72` (null em string não altera) | `Text == ""` |
| `content` como lista de partes | **Erro: a resposta inteira é recusada** | `port/port.go:72`, `:110-112` | O turno falha; não é `empty_output` |
| `reasoning_content` (qualquer forma JSON) | Lido como carga opaca | `port/port.go:92`, `:103-131`; `runtime_adapter.go:379` | Só a captura, selado (`captura:91`, `:466`) |
| `reasoning`, `thinking`, `thinking_blocks`, `reasoning_details`, `provider_specific_fields` | **Não descodificado** (chave não declarada) | `port/port.go:70-93` | Lado nenhum |
| Assinaturas de raciocínio (`thinking_blocks[].signature`, `thought_signature` numa tool call) | **Não descodificado** | `port/port.go:61-65`, `:70-93` | Lado nenhum |
| `refusal` | **Não descodificado** | `port/port.go:70-93` | Lado nenhum |
| `tool_calls[].id` | Descodificado, **descartado na tradução** | `port/port.go:62`; `runtime_adapter.go:380-385`; `model.go:67-95` | Lado nenhum; o id do tail é do runtime |
| `tool_calls[].type`, `.index`, campos extra | `type` descodificado e ignorado; o resto não descodificado | `port/port.go:61-65` | Lado nenhum |
| `tool_calls[].function.arguments` (string) | Lido, cru | `port/port.go:56`; `runtime_adapter.go:383` | `Input` da chamada, tail |
| `function.arguments` como objecto | **Erro: resposta inteira recusada** | `port/port.go:56` | O turno falha |
| `message.function_call` (forma antiga) | **Não descodificado**; mas `finish_reason: function_call` é mapeado para `tool_calls` | `port/port.go:70-93`; `runtime_adapter.go:413` | Um turno com zero tool calls e motivo `tool_calls`; o run termina (`termination.go:53`) |
| `finish_reason` | Texto tal como veio na porta; normalizado em 6 valores; fora do mapa ⇒ `other`, **valor bruto não guardado** | `port/port.go:303`; `runtime_adapter.go:407-422` | Captura e `turn.recorded` (em claro) |
| `finish_reason` ausente ou `null` | `""` ⇒ não reportado; conta como final se não houver tool calls | `runtime_adapter.go:387`, `:409-410` | idem |
| `choices[1..]` | Ignoradas; só a primeira é lida | `runtime_adapter.go:375` | Lado nenhum |
| `choices` vazio | Erro fail-closed | `runtime_adapter.go:371-374` | Turno falha |
| `usage.prompt_tokens`, `completion_tokens`, `total_tokens` | Lidos | `port/port.go:161-164` | `InputTokens`, `OutputTokens` (`runtime_adapter.go:337-339`); `total` não atravessa |
| `usage.prompt_tokens_details.cached_tokens` | Lido por sonda | `port/normalize.go:178-206` | `CacheReadTokens` |
| `usage.completion_tokens_details.reasoning_tokens` (e restantes detalhes) | **Não lido** | `port/port.go:161-217` | Lado nenhum. **Não se sabe quantos dos tokens de saída foram raciocínio** |
| `system_fingerprint`, `service_tier` | **Não descodificado** | `port/port.go:324-336` | Lado nenhum |
| `id`, `created`, `object` | Descodificados, não propagados | `port/port.go:325-327` | Lado nenhum |
| `model` do corpo | Saneado, 256 bytes | `runtime_adapter.go:313-333`, `:358` | `served_model_id` (substituído pelo cabeçalho quando a rota está sob governação, `:366-370`) |
| Cabeçalhos da resposta | Só os dois da rota (AOS-505) | `adapters/openai_http.go:126-130` | `Route` |

Caminho de streaming, para registo: o delta não tem `reasoning_content` nem `refusal`
(`adapters/openai_http.go:240-257`) e o agregador fabrica `finish_reason = "stop"` quando nenhum chunk o traz
(`port/normalize.go:327-329`). Os runs não usam streaming (acompanhamento §6), pelo que não é causa de nada
medido; é dívida para o dia em que usarem.

### 1.2 Caminho do pedido

| Facto | Onde `[CÓDIGO]` |
|---|---|
| O wire leva só `model`, `messages`, `tools`, `tool_choice`, `stream`, `temperature`, `seed`, `max_tokens` | `port/normalize.go:25-34` |
| O adaptador do runtime preenche só `model`, `messages` e `tools`. Não envia temperatura, semente, tecto de tokens, `tool_choice`, nem nenhum parâmetro de raciocínio | `runtime_adapter.go:238-247` |
| O raciocínio é retirado de **todas** as mensagens de **todos** os pedidos, no único sítio por onde um pedido sai | `port/normalize.go:86-98`, `:102-119` |
| Um `assistant` com tool calls e sem texto vai com `"content": ""` (não `null`) | `port/port.go:72` (sem `omitempty`); `projection.go:583` |

### 1.3 O que se perde na ida e volta

O tail é a forma canónica (ADR-036 §2.1) e a projecção é função pura de `(System, Tail, AssemblyVersion)`
(`projection.go:28-33`). O que não está no tail não pode voltar ao provider.

| Elemento | O tail guarda? | A projecção reenvia? |
|---|---|---|
| Texto do modelo num turno com tools | Sim (segmento `history`) | Sim, **neutralizado** — linhas que abram por `<` ou `\` ganham um `\` (`projection.go:622-629`). Os bytes podem não ser os que o modelo emitiu |
| Texto do modelo no turno final | Não (o run acaba; `projection.go:571-575`) | — |
| Nome e argumentos da tool call | Sim (segmento `tool_call`) | Nome tal e qual ou o nome reservado (`projection.go:368-378`); argumentos crus se forem JSON válido, senão um substituto com tamanho e digest (`:426-446`) |
| Id da tool call do provider | **Não** | **Não.** Vai o id do runtime (`projection.go:656`, `:686`) |
| Raciocínio (`reasoning_content`) | **Não** (só a captura; ADR-036 §2.7) | **Não**, por regra (`port/normalize.go:89`) |
| Assinaturas, blocos assinados, `thought_signature` | **Não** (nem descodificados) | **Não** |
| `refusal`, `system_fingerprint`, detalhes de `usage` | **Não** | n/a |

Consequência para a matriz de suporte (acompanhamento §6): as classes «tool calling nativo com raciocínio a
devolver» e «com assinaturas por chamada» falham ao **segundo turno** com tools `[HIPÓTESE, inferida do
código e da documentação dos providers; a análise de 2026-10-04 §6 di-lo igual e também não mediu]`. Uma
assinatura sobre o texto do `assistant` seria ainda invalidada pela neutralização, mesmo que voltasse.

## 2. Diagnóstico do `empty_output`

### 2.1 O que o código exige para dar este desfecho

`[CÓDIGO]` `ConcludeRun` fecha `empty_output` quando o turno que termina o run tem
`strings.TrimSpace(resp.Text) == ""` (`completion.go:465-466`), **depois** de excluídos o corte por tokens
(`:450-451`) e o contrato (`:452-453`). O turno termina o run porque não tem tool calls (`termination.go:53`).
Com os factos medidos — um turno, `stop_reason = stop`, nó sem tools, `output_tokens` 161 e 77 — a resposta
do provider tinha de ser: **HTTP 200, JSON válido, pelo menos uma `choice`, `finish_reason: "stop"`,
`tool_calls` ausente ou vazio, `usage.completion_tokens > 0`, e `choices[0].message.content` nulo, ausente,
vazio ou só com brancos.**

### 2.2 As respostas que encaixam nisso

| # | Forma da resposta | O adaptador hoje | Como se distingue |
|---|---|---|---|
| H1 | `content` vazio/nulo e o texto todo em `reasoning_content` | Lê-o; está na captura selada | **Já hoje**: tamanho do `reasoning` na captura dos 3 runs. Com a sonda: `raciocinio=reasoning_content`, bytes > 0 |
| H2 | `content` vazio/nulo e o texto noutro campo de raciocínio (`reasoning`, `thinking_blocks`, `reasoning_details`, `provider_specific_fields`) | Não descodifica | Só com a sonda |
| H3 | Raciocínio **escondido** pelo provider: gastou tokens a raciocinar, não devolveu campo nenhum, e a resposta visível saiu vazia | Nada para ler | `usage.completion_tokens_details.reasoning_tokens` ≈ `completion_tokens`, e nenhum campo de raciocínio |
| H4 | `content` só com brancos ou quebras de linha | Lê-o; `TrimSpace` dá vazio | `conteudo=so_brancos`, bytes > 0 |
| H5 | `refusal` preenchido e `content` nulo | Não descodifica | `recusa=texto` |
| H6 | O modelo tentou uma tool call num nó sem tools; o servidor (provider ou proxy) reconheceu os marcadores, retirou-os do texto e não os converteu em `tool_calls` por não haver tools no pedido | Nada para ler | Nenhum campo com bytes; tokens de raciocínio a zero ou ausentes. É diagnóstico por exclusão |
| H7 | `message.function_call` (forma antiga) com `finish_reason: stop` | Não descodifica | `function_call_antigo=sim` |
| H8 | A resposta está em `choices[1]` | Ignora | `choices_n > 1` |

**Excluídas pelo código:** `content` em partes (dava erro, não vazio — §0.2); resposta cortada (dava
`truncated`); `choices` vazio (dava erro); `tool_calls` presentes (o run não terminava nesse turno).

**Avaliação honesta.** H1 é a mais provável à partida — o raciocínio chega neste provider `[MEDIDO:
2026-10-03, ADR-036 §2.7]` — mas é exactamente a que já se podia ter confirmado e não se confirmou, porque
ninguém olhou para o tamanho do `reasoning` capturado. H6 é coerente com o defeito dominante da fase A1
(o modelo escreve tool calls como texto em 33 de 34 falhas `[MEDIDO]`) e com o sítio (o nó de resumo vê
no `plan_input` material vindo de um nó com tools) `[HIPÓTESE]`. Não há dados para escolher.

### 2.3 Passo zero, sem código: ler o que já está gravado

A captura dos três runs tem o campo `reasoning` dentro do envelope selado. Saber **só o comprimento** dele
decide entre H1 e todas as outras. `[NÃO VERIFICADO]` se o nó de produção corre em modo sensível:
nesse modo a captura guarda uma referência no lugar do raciocínio, e o comprimento não se lê dali. Exige: autorização do dono para decifrar esses três envelopes (precedente:
a leitura por `reconstruct` autorizada a 2026-10-07 para duas séries) e uma leitura que devolva o tamanho e
não o texto. `[NÃO VERIFICADO]` se o `GET /runs/{id}/reconstruct` expõe o raciocínio: uma procura por
`reasoning` em `packages/cmd/aos` (fora de testes) não devolve nada, pelo que provavelmente não expõe. Se
não expõe, o passo zero custa uma ferramenta de leitura e deixa de compensar face ao ticket da §2.4.

### 2.4 Primeiro ticket: a forma da resposta, sem conteúdo (AOS-507)

**O que se grava.** Um objecto `response_shape` por turno, calculado no gateway a partir do corpo cru com uma
sonda própria — o molde das duas sondas que já existem (`wireUsageProbe` e `wireCachedProbe`,
`port/normalize.go:141-154`, `:178-184`): só conhece os campos que lhe interessam e não pode fazer falhar a
resposta. Tudo em **vocabulário fechado ou inteiros**; nenhum byte de valor, nenhum nome de chave do provider.

| Campo | Valores |
|---|---|
| `content` | `ausente`, `nulo`, `vazio`, `so_brancos`, `texto`, `partes`, `outro` |
| `content_bytes` | inteiro |
| `reasoning` | `nenhum`, `reasoning_content`, `reasoning`, `thinking_blocks`, `reasoning_details`, `varios` |
| `reasoning_form` | `string`, `objecto`, `lista`, `outro` |
| `reasoning_bytes` | inteiro (bytes do valor JSON) |
| `reasoning_signed` | `sim`, `nao` (há alguma chave `signature` ou `thought_signature`) |
| `refusal` | `ausente`, `nulo`, `texto` |
| `tool_calls_n`, `tool_call_id` | inteiro; `nenhum`, `call_`, `functions_ponto`, `uuid`, `numerico`, `vazio`, `outro`; e `tool_call_id_max_bytes` |
| `arguments_form` | `string`, `objecto`, `outro` |
| `legacy_function_call` | `sim`, `nao` |
| `choices_n` | inteiro |
| `reasoning_tokens` | inteiro, ou ausente |
| `system_fingerprint` | `sim`, `nao` (presença) |
| `unknown_keys_n` | inteiro: chaves de `message` que a sonda não conhece |
| `shape_digest` | `sha256` da lista ordenada de (caminho da chave, tipo JSON) da resposta — agrupa formas iguais sem guardar texto do provider |

**Onde se grava.** No `turn.recorded`, como campo opcional (`omitempty`), ao lado de `stop_reason` e
`tools_offered` (`packages/kernel/agent-runtime/turn.go:120`, `:128`), e numa métrica do nó
`aos_model_response_shape_total{content,reasoning,stop_reason}`. **Não na captura**: segue o precedente do
`ToolsOffered` (`model.go:161-163`) — a captura não muda de bytes nem de digest, nenhuma golden muda, e um
turno reproduzido volta sem o campo. Atravessa a fronteira como `ModelResponse.Shape`, declarado por quem
fez o pedido, no molde de `Projection` e `RouteCheck` (`model.go:140-141`, `:176-177`); na porta é um campo
`json:"-"` de `ChatResponse`, como o `Route` (`port/port.go:335`), versão de porta MINOR.

**Porque em claro é aceitável.** É a decisão já registada para o `stop_reason` (`captura:99-105`): facto
sobre o turno, vocabulário fechado, sem conteúdo do titular, sobrevive ao apagamento do titular como já
sobrevivem os tokens e o custo. Os tamanhos não revelam mais do que os tokens de saída já revelam.

**Interruptor.** `AOS_MODEL_RESPONSE_SHAPE=off|observe`; omissão `off`. Em `off` a sonda não corre e o
`turn.recorded` tem os bytes de hoje. Não há `enforce`: é medição, não decide nada.

**Responde numa série?** Com a taxa medida (3 em 140, 2,1% — séries `v0151a`, 58 planos e 2
vazios; `v0151b`, 62 e 1; `v0151g`, 20 e 0), uma série de 60 planos tem cerca de 73% de
probabilidade de conter pelo menos um vazio, e duas séries cerca de 93% `[cálculo: 1 − (1 − 3/140)^60]`.
Não é certo que uma chegue. Em contrapartida, os turnos **não** vazios da mesma série respondem logo a metade
da pergunta: se o nó de resumo traz normalmente `reasoning_content` com bytes e `content` com bytes, H1 fica
muito provável; se nunca traz raciocínio, H1 e H2 caem.

**Antes de produção, em local** (há Docker; não toca no modelo real): a imagem do proxy contra um provider
falso que devolve as formas H1 a H8. Mostra **o que o proxy faz a cada forma** — que nome dá ao raciocínio,
se reescreve `content: null`, se acrescenta `provider_specific_fields` — e isso estreita as hipóteses sem
esperar por uma série. O `scripts/ci/rota-live.sh` (AOS-505) já corre a imagem de produção do proxy num gate
opcional; reutiliza-se o padrão.

## 3. Opções para tratar a resposta vazia

### (a) Recuperação alargada a `empty_output`

Hoje o ADR-039 §2.8 exclui `empty_output` por nome. A proposta é uma **segunda classe de tentativa**, com
prova própria, não um alargamento da primeira.

**A prova, num nó sem tools** (lida pelo nó no seu Event Store e no seu WORM, nunca do corpo do pedido —
ADR-039 §2.3):

| Facto exigido sobre a tentativa anterior | Fonte |
|---|---|
| Hospedada por este nó, com vínculo ao mesmo pedido e ao mesmo nó do plano; é a tentativa imediatamente anterior | `run.plan_origin` (igual a hoje) |
| Está `failed`, e fechou por **`empty_output`** | a última `run.state.transition` e o veredicto selado |
| **Zero tool calls pedidas** | total do vector selado; ausência de qualquer evento `tool.call.*`; `tool_calls_requested = 0` no turno (as três fontes de hoje) |
| **Um só turno**, com motivo `stop` | o único `turn.recorded` |
| O run **não declarou origem de saída vinculativa** | o `Completion` do manifesto. Com origem vinculativa, «vazio» quer dizer que a tool devolveu zero bytes (`completion.go:460-464`) — houve tool, nunca se repete |
| Residência igual à de quem pede | selo de residência (igual a hoje) |

O que garante: nenhum efeito pode repetir-se, porque nenhum ocorreu — a mesma garantia do AOS-502. O que
é diferente: não é preciso «o nó não tem tools»; chega «não pediu nenhuma». Um nó com tools que responda vazio
à primeira sem chamar nada fecha `contract_unmet_no_call` (o contrato tem precedência, `completion.go:452`) e
já é coberto pelo AOS-502.

| | |
|---|---|
| **Prós** | Reutiliza toda a máquina da A1 (tectos, orçamento, `off/observe/on`, métricas). Não interpreta texto. Não muda o kernel. Trata o sintoma seja qual for a causa (H1 a H8) |
| **Contras** | Não trata a causa. Se a causa for determinada pelo pedido (um `plan_input` que leva sempre o modelo a gastar a resposta a raciocinar), a recorrência é alta e as tentativas queimam orçamento — a A1 mediu recorrência de 32% a 50%, muito acima da taxa de base `[MEDIDO]` |
| **Segurança** | O nó de resumo tem sempre `consumes`: conteúdo untrusted do passo anterior pode induzir a resposta vazia e gastar as tentativas. É o risco já aceite pelo dono a 2026-10-06 para a A1, limitado pelo tecto e pelo orçamento; a tentativa nunca dá autoridade. **Novo:** o aviso do AOS-506 fala de function calling; não se reutiliza aqui. A tentativa por vazio vai **sem aviso**: a decisão e a justificação estão no AOS-510 |

### (b) Ler o raciocínio como texto final

**Rejeitar.** Razões, por ordem de peso:

1. **Autoridade do veredicto.** `empty_output` é o veredicto certo: o modelo não respondeu. Promover a
   resposta um texto que o modelo não deu como resposta é refazer o verde falso que a A0 fechou.
2. **Não é a resposta.** O raciocínio é deliberação: hipóteses abandonadas, cópias literais do `plan_input`
   untrusted, instruções que o modelo decidiu não seguir. Publicado como saída do nó, passava ao nó seguinte e
   ao pedinte material que o modelo excluiu.
3. **Contrato existente.** ADR-036 §2.7 e `model.go:124-131`: o runtime não lê nem interpreta o raciocínio; o
   único destino é a captura. O campo aceita qualquer forma JSON e guarda bytes crus (`port/port.go:120-131`)
   — a «resposta» podia ser um objecto JSON de um fornecedor.
4. **Replay.** «Se o texto vier vazio, usa o raciocínio» é uma regra nova de conclusão; teria de entrar por
   layout novo e pelo teste diferencial do AOS-492 (`termination.go:20-28`).
5. **Taint.** Não cria autoridade (é saída do modelo, untrusted como o texto), mas alarga o que conta como
   saída a um canal que o provider controla.

Ressalva honesta: se a medição mostrar H1 **e** o texto em `reasoning_content` for a resposta bem formada (um
servidor que falhou a separar raciocínio de resposta), a correcção é **na rota** — configuração do proxy ou
parâmetro do pedido, opção (c) — e não uma regra do runtime.

### (c) Pedir ao provider que não raciocine / parâmetros do pedido

Hoje é impossível sem código: o wire não tem onde levar o parâmetro (§1.2).

| | |
|---|---|
| **Prós** | Se a causa for H1, H2 ou H3, trata-a na origem e poupa tokens. É o lugar certo para as diferenças entre modelos: um **perfil de rota** assinado, com digest no manifesto do turno (o AOS-505 já tem perfil e digest — `model.go:165-177`) |
| **Contras** | Cada provider tem o seu vocabulário; sem perfil, é configuração manual por modelo, o contrário do objectivo. O proxy reencaminha tudo (`drop_params` não fez diferença `[MEDIDO]`): um parâmetro que o provider não conheça pode dar 400 em **todos** os turnos. Muda o comportamento do modelo em todos os nós — incluindo a taxa de tool calls em texto, para melhor ou para pior; tem de ser medido numa série antes de ligar. `[NÃO VERIFICADO]` se o `kimi-for-coding` aceita desligar o raciocínio |
| **Segurança** | Os parâmetros só podem vir de configuração assinada do nó, nunca do plano nem de conteúdo de um run. Entram no manifesto (`ModelConfig.Params`, `model.go:8-16`) para o replay saber com que parâmetros o turno correu |

### (d) O estado opaco como parte do tail (guardar e reenviar)

É o conteúdo literal da fase A2. O que exige, camada a camada:

- **Porta.** Um lugar para carga opaca por mensagem e por tool call (assinaturas, id do provider), e
  `MarshalWire` deixa de retirar o raciocínio **quando o perfil da rota o manda devolver**
  (`port/normalize.go:86-98` é hoje incondicional). `ToolInvocation` continua sem id de autorização; o id do
  provider é carga opaca, não identidade.
- **Layout.** Duas formas. *(d1)* um segmento novo `provider_state` no tail (layout 1.5.0): entra no
  `prompt_hash`, engrossa todos os prompts, obriga a projecção de texto único a não o renderizar. *(d2)* **por
  referência**: os bytes ficam na captura selada (onde o raciocínio já está) e o tail leva só um rótulo
  `state_digest=sha256:…` no segmento do turno — o precedente é o `args_digest` dos argumentos omitidos
  (`projection.go:408-410`). O `prompt_hash` compromete-se com o estado sem o conter. **Recomendo (d2).**
- **Projecção.** Versão nova (1.3.0): função pura de `(vista, estado por turno)`. Anexa o estado ao
  `assistant` do turno a que pertence, **sem neutralizar** (uma assinatura não sobrevive a um `\` a mais). O
  estado fica ligado ao digest do perfil da rota e ao modelo que serviu: estado de uma rota **nunca** vai para
  outra — sem isso, um failover enviava a um fornecedor o raciocínio produzido por outro.
- **Replay e retoma.** A retoma tem de reidratar o estado da captura antes de projectar o turno seguinte (a
  captura já devolve o `Reasoning`, `captura:563`). O resíduo «o `prompt_hash` não cobre a projecção nativa»
  (acompanhamento §7) cresce se o digest não for para o tail; com (d2) não cresce.
- **WORM e dados pessoais.** O raciocínio é conteúdo do titular: selado por titular (já é), nunca em spans
  nem em eventos em claro. Apagado o titular, o estado desaparece e um run em curso não continua — correcto.
  Em modo sensível a captura troca o raciocínio por uma referência (`captura:483-486`): **nesse modo não há
  estado para devolver**, e as rotas que o exijam ficam declaradas como não suportadas. Reenviar custa os
  tokens do raciocínio em cada turno seguinte (ADR-036 §2.7): precisa de tecto de bytes e conta no orçamento.
- **Segurança.** É um canal modelo→modelo entre turnos que o runtime não inspecciona. Regras: nunca autoriza;
  é sempre `untrusted`; não altera a autoridade do turno (cunhada dos segmentos do tail, ADR-034) — o turno
  que o produziu já viu o mesmo material. Um provider hostil pode pôr lá o que quiser: nunca é lido como
  instrução do runtime nem interpretado pelo gateway.

| | |
|---|---|
| **Prós** | É o que desbloqueia duas classes inteiras da matriz. Fecha também o id do provider |
| **Contras** | A mudança mais funda da fase (porta, captura, layout ou rótulo, projecção, replay). **Não trata o `empty_output`**: o provider de produção não exige o raciocínio de volta `[MEDIDO: 2026-10-03]` |

### Composição recomendada, por fases

1. **Ver** — AOS-507 (forma da resposta) e os providers falsos (AOS-508). Sem mudança de comportamento.
2. **Não cair** — descodificação tolerante (AOS-509): as formas que hoje dão erro passam a dar um turno.
3. **Recuperar** — (a), em `observe` e depois `on` (AOS-510, AOS-511). Trata o sintoma medido.
4. **Tratar a causa**, só depois de a §2 ter resposta — (c) pelo perfil de rota («A2-perfil»), medido no banco.
5. **Abrir a segunda família** — (d), só se a família escolhida o exigir («A2-estado», «A2-projecção»).
6. **(b) fica rejeitada.**

## 4. Segunda família de modelos

O critério do acompanhamento é «duas famílias de modelos completam runs com tools». Lido à letra, cumpre-se
com uma segunda família que **não** exija estado opaco — e a fase não provava o que o seu título promete.

**O que o dono tem de decidir:**

| Decisão | Opções e o que cada uma prova |
|---|---|
| **Que classe de família** | (i) Raciocínio que **tem de voltar** no turno seguinte — prova a linha 2 da matriz. (ii) Assinaturas por chamada ou blocos assinados — prova a linha 3, a mais difícil. (iii) Família sem estado exigido — cumpre a letra do critério e não prova o estado opaco. **Recomendo (i) ou (ii)** |
| **Por que proxy** | O mesmo LiteLLM (um só wire do nosso lado; o proxy traduz) ou ligação directa (um adaptador novo — «classe de wire nova é engenharia», acompanhamento §1). **Recomendo o mesmo proxy**: isola a variável «família» da variável «wire» |
| **Credenciais, região, custo** | Fornecedor com contrato; chave entregue pelo Broker/Vault; região dentro do board (produção sela em `eu-west`); tecto de despesa. Não tenho como saber que contratos existem: é decisão inteira do dono |
| **Nome na allowlist** | Uma rota nova exige re-assinar a allowlist de modelos (mesmo custo já identificado no AOS-505) |

**O que se prova em local, com providers falsos** (AOS-508) — as diferenças de **wire** conhecidas:

- Ids de tool call: `call_…`, `functions.<tool>:<n>`, numéricos, ausentes, repetidos, de comprimento fixo; e um
  falso que **recusa** um `tool_call_id` que não emitiu ou que exceda um comprimento.
- `content`: `null`, `""`, só brancos, lista de partes; `arguments` como string e como objecto.
- Raciocínio em cada um dos nomes da §1, em string, objecto e lista; com e sem assinatura.
- Um falso que **dá 400 ao segundo turno** se o `assistant` com tool calls não trouxer o raciocínio ou a
  assinatura do primeiro — a sonda de protocolo determinista que o título da fase pede. Com controlo
  negativo real: o mesmo teste, com o estado retirado, tem de ficar vermelho.
- `finish_reason` fora do mapa (`end_turn`, `tool_use`, `STOP`, ausente, `function_call`), várias `choices`,
  tool calls paralelas, `usage` em todas as formas (ausente, vazio, só total, com detalhes).
- Tudo isto **atrás da imagem real do proxy**, para ver o que ele faz a cada forma.

**O que só o modelo real prova:** que o fornecedor exige de facto o que a documentação diz; que as
assinaturas são aceites (um falso não valida assinaturas); o que a versão do proxy em produção emite para
esse fornecedor; as **taxas de comportamento** (tool call em texto, resposta vazia, recusa); tokens, custo
e cache; os motivos de paragem que realmente usa.

## 5. Banco de ensaio / qualificação (antecipado da A3)

### 5.1 Desenho mínimo

Uma **bateria** fixa de casos sintéticos, uma **rota** alvo, e um **relatório de taxas**. Vive em
`packages/qa/` (ao lado de `dr-e2e` e `ux-dx`), com um script em `scripts/ci/` no molde do `rota-live.sh`.

- **Casos** — documentos e objectivos escritos por nós, versionados, sem conteúdo de titular nenhum:
  T1 nó de leitura com uma tool (um turno com tool call, um turno final); T2 plano de dois nós, leitura e
  resumo (o cenário do `empty_output`); T3 nó sem tools com `plan_input`, isolado; T4 duas tool calls no mesmo
  turno; T5 tool negada e continuação; T6 argumentos grandes.
- **Taxas**, todas deterministas e em vocabulário fechado: «não chamou a tool» (`contract_unmet_no_call` com
  motivo `stop`), resposta vazia (`empty_output`), cortada, erro do provider por código HTTP, distribuição dos
  motivos de paragem, **distribuição da forma das respostas** (AOS-507), recuperado à 2.ª ou 3.ª tentativa.
- **Recusa do objectivo e tool call em texto** não se detectam com segurança (acompanhamento §4, 2026-10-06; o
  canário deu 1 falso positivo em 4). No banco, como os documentos são sintéticos, mede-se um substituto
  determinista: **os factos conhecidos do documento estão ou não no resumo** (números e nomes exactos). É
  verificação de factos sobre dados nossos, não um juiz probabilístico (§8 do acompanhamento mantém-se).
- **Saída:** um ficheiro de taxas com intervalo de confiança e o digest da bateria, da rota e da
  configuração. Não decide nada sozinho nesta fase; a recusa automática de um modelo é da A3.

### 5.2 Onde corre

| Opção | O que prova | Custo | Decisão do dono |
|---|---|---|---|
| **O1 — fila de produção, prefixo próprio** | Comportamento do modelo real, na rota real | 1 h por série de 60. Gasta o orçamento de produção. **Não isola variáveis**: a versão da projecção e os interruptores são do nó inteiro; mudar um para o ensaio muda-o para todos os runs. Variantes experimentais teriam de ser publicadas em produção | Nenhuma nova (é o que se faz hoje) |
| **O2 — nó de ensaio local, proxy real, modelo real** | O mesmo, com variáveis isoladas e em minutos; permite comparar braços lado a lado | Uma chave própria com tecto; os segredos continuam a vir do Broker/Vault | **Autorização de medição directa** (recusada a 2026-10-06) e uma chave com tecto diário |
| **O3 — providers falsos, em CI** | O **protocolo**: que o gateway não cai, devolve o que deve, recusa o que deve | Zero pedidos; determinista; corre em cada PR | Nenhuma |

**Recomendação:** O3 já (AOS-508) e O1 para as taxas, como hoje. Pedir ao dono O2 com tecto: é a única forma
de fazer experiências isoladas — a dos separadores não se faz bem de outra maneira.

### 5.3 Primeira experiência: a hipótese dos separadores

**Hipótese** `[HIPÓTESE; o código di-la «não testada isoladamente», `projection.go:218-220`]`: mostrar ao modelo
uma notação `<kind …>` … `</kind>` leva-o a imitar marcação quando quer pedir uma tool. Os dados de produção
confundem duas mudanças: a 1.1.0 trouxe a linha de fim **e** texto de protocolo novo (10% → 32% de primeiras
falhas `[MEDIDO]`).

**Desenho isolado** — caso T1, uma só variável por eixo:

| Braço | Delimitadores | Texto de protocolo |
|---|---|---|
| A | `<kind>` … `</kind>` (a 1.2.0 de hoje) | 1.2.0 |
| B | sem sinais de menor/maior (por exemplo `[[kind]]` … `[[/kind]]`) | 1.2.0, com a mesma frase a nomear o delimitador |
| C | `<kind>`, sem linha de fim | 1.2.0 sem a frase da linha de fim |
| D | como B | 1.1.0 adaptada |

A contra B mede o efeito dos sinais de menor/maior; A contra C, o da linha de fim; B contra D, o do texto. As
variantes B, C e D vivem **no código do banco**, fora do vocabulário fechado do nó
(`ParseNativeProjectionVersion`, `projection.go:89-96`): produção não as consegue seleccionar.

**Métrica:** primeiras tentativas sem tool call (`tool_calls_requested = 0` e motivo `stop`) — determinista,
sem ler texto. **Amostra:** para distinguir 10% de 32% com 80% de potência a 5%, cerca de 53 pedidos por
braço; quatro braços ≈ 212 pedidos de um turno. Efeitos mais pequenos do que este não se vêem com esta amostra.

**Onde:** em O2, 212 pedidos e alguns minutos. Em O1 seriam quatro séries (4 h) **e** as variantes teriam de
existir em produção, ligadas para todos os runs — não recomendo. Em O3 não se faz: é comportamento.
**Sem a autorização O2, esta experiência não se faz de forma isolada**; o que resta é a série da 1.2.0 já
prevista no AOS-506, que continua a confundir texto e separadores.

## 6. Proposta de tickets

Ordem de entrega = ordem das linhas. O nó sai antes do `aos-orq`. Tudo entra desligado ou aditivo.
**Abertos a 2026-10-07: AOS-507 a AOS-511.** As cinco linhas seguintes são trabalho planeado, sem
ticket e sem número, à espera das decisões D4 e D5; «A2-estado» e «A2-projecção» são as duas
metades do estado opaco (transporte às escuras, e depois a projecção que o devolve).

| Ticket | Epic | Objectivo | Interruptor e omissão | Critério de aceitação central | Depende de | Muda produção quando ligado? |
|---|---|---|---|---|---|---|
| **AOS-507** | EPIC-06 | Gravar a forma da resposta do provider em cada turno, em vocabulário fechado e sem conteúdo | `AOS_MODEL_RESPONSE_SHAPE=off\|observe`; omissão `off` | Em `off` o `turn.recorded` e a captura são byte a byte os de hoje. Em `observe`, cada uma das formas H1–H8 dá uma classe distinta; um corpo com sentinelas em todos os valores não deixa nenhuma sentinela no evento, na métrica nem em spans | — | Não: só acrescenta um campo de medição |
| **AOS-508** | EPIC-06 (fixtures em `testkit`) | Providers falsos que imitam as diferenças de wire conhecidas, e um gate opcional que os põe atrás da imagem real do proxy | Gate opcional, no molde do `ci-rota-live`; salta sem Docker e redeclara o salto | Cada linha da matriz de suporte tem um falso; o que exige estado dá 400 ao segundo turno sem ele, e o teste fica vermelho quando o estado é retirado | — | Não: só CI |
| **AOS-509** | EPIC-06 | A resposta deixa de ser recusada inteira por `content` em partes ou `arguments` em objecto; os outros nomes do raciocínio passam a ser carga opaca | Sem interruptor (corrige um erro); só altera respostas que hoje falham | Nenhuma resposta que hoje é aceite muda um byte na captura (goldens). `content` em partes de texto ⇒ texto concatenado; `arguments` objecto ⇒ os bytes JSON crus; parte que não é texto ⇒ recusa com causa própria (ver §10) | AOS-507, AOS-508 | Só para respostas que hoje dão erro (nenhuma observada em produção) |
| **AOS-510** | EPIC-19 | O nó aceita a nova tentativa de um nó do plano que fechou `empty_output`, com a prova da §3(a) lida do seu log | `AOS_RUN_RETRY_EMPTY=off\|on`; omissão `off`; partilha o tecto `AOS_RUN_RETRY_MAX` | Cada ponto da prova tem teste de recusa: uma tool call, dois turnos, motivo diferente de `stop`, origem vinculativa, outra razão. Emenda ao ADR-039 §2.8 | AOS-502 | Não até o `aos-orq` pedir |
| **AOS-511** | EPIC-19 | O `aos-orq` volta a submeter um nó que fechou `empty_output` | `AOS_ORQ_NOVA_TENTATIVA_VAZIA=off\|observe\|on`; omissão `off` | Em `observe` conta o que faria sem pedir nada. Em `on`, as tentativas, as recorrências e os recuperados contam em métricas separadas das do AOS-503; tentativa sem aviso | AOS-510, AOS-503 | Sim: um passo com resposta vazia é repetido em vez de falhar o plano |
| **«A2-banco»** (planeado, por numerar) | EPIC-08 (código em `packages/qa`) | Banco de ensaio mínimo: bateria T1–T6, relatório de taxas, e a experiência dos separadores como primeira corrida | Ferramenta fora do nó; não é gate do `run.sh` | A mesma bateria corre contra os falsos (O3) com taxas esperadas exactas; o relatório leva os digests da bateria, da rota e da configuração; nenhum caso contém dados de titular | AOS-507, AOS-508; decisão D5 para O2 | Não (em O1 gasta orçamento de produção com prefixo próprio) |
| **«A2-perfil»** (planeado, por numerar) | EPIC-06 | O perfil de rota declara parâmetros do pedido e a classe de estado (`devolver: nunca\|opcional\|obrigatório`) | Sem campo no perfil ⇒ pedido de hoje, byte a byte | Os parâmetros só vêm do perfil assinado; entram no manifesto do turno; um perfil com parâmetros muda o digest; nenhum caminho os aceita de um plano ou de um run | AOS-505; resultado do AOS-507 | Sim, quando o dono assinar um perfil com parâmetros (por exemplo, raciocínio desligado) |
| **«A2-estado»** (planeado, por numerar) | EPIC-06, EPIC-02 | ADR novo e transporte: o estado opaco do turno (raciocínio em qualquer nome, assinaturas, id do provider) é capturado selado e referido no tail por digest | Às escuras: nada o devolve ao provider | Captura de um turno sem estado é byte a byte a de hoje; a retoma devolve o estado igual; nenhum byte do estado aparece em claro (sentinelas); em modo sensível fica a referência | AOS-509 | Não |
| **«A2-projecção»** (planeado, por numerar) | EPIC-06 | Projecção nativa 1.3.0: devolve ao provider o estado do turno quando o perfil da rota o exige, e só à rota que o produziu | `AOS_MODEL_PROJECTION_VERSION=1.3.0` **e** perfil com `devolver` diferente de `nunca`; omissão: não devolve | Com `nunca`, as mensagens são byte a byte as da 1.2.0. Os falsos que exigem estado completam o segundo turno; estado de outra rota nunca sai (teste de failover); tecto de bytes; diferencial loop/replay igual | «A2-perfil», «A2-estado» | Sim, só para rotas cujo perfil o exija; a rota de produção fica em `nunca` |
| **«A2-família»** (planeado, por numerar) | EPIC-06 | Qualificar a segunda família: série real, matriz de suporte e acompanhamento actualizados | Rota nova na allowlist, assinada pelo dono | O critério da §8, medido e registado | «A2-banco», «A2-projecção»; decisão D4 | Sim: uma segunda rota passa a poder servir runs |

**Porque nenhum passo deixa o sistema pior.** AOS-507/508/512/514 não mudam comportamento. AOS-509 só
converte erros em turnos. AOS-510/511 trocam um plano falhado por um plano que tenta outra vez; a tentativa
que falha deixa o plano onde hoje fica, segundos mais tarde. «A2-perfil»/515 são inertes sem perfil assinado.

**Fora desta lista, de propósito:** ler o raciocínio como resposta (rejeitado, §3b); streaming; um parser de
tool calls em texto (acompanhamento §8).

## 7. Decisões do dono antes de implementar

**D1 — Medir a forma das respostas em produção durante uma ou duas séries.**
Hoje, quando uma resposta vem vazia, o sistema não regista que partes a resposta trazia. Proponho registar,
por cada resposta, só a «ficha»: que partes vieram e o tamanho de cada uma — nunca o texto.
*Recomendo: sim.* Se sim: numa ou duas séries fica-se a saber porque vêm vazias. Se não: escolhe-se o remédio
às cegas.

**D2 — Quando um passo sem ferramentas devolve uma resposta vazia, o sistema tenta outra vez sozinho?**
Hoje o plano falha (3 em 140). O sistema já repete passos em que o modelo não usou a ferramenta; isto estende
a mesma regra, com o mesmo limite de duas tentativas a mais, e só quando o passo não usou ferramenta nenhuma —
não há nada que possa ser feito duas vezes.
*Recomendo: sim, primeiro só a contar o que faria, depois ligado.* Se sim: a maior parte destes planos passa a
acabar bem, com mais uns segundos e mais algum custo. Se não: continuam a falhar, com a causa bem identificada.

**D3 — Se a resposta vier vazia mas o modelo tiver deixado «notas de raciocínio», usamo-las como resposta?**
*Recomendo: não.* As notas são rascunho: podem conter coisas que o modelo decidiu não dizer e bocados do
material do passo anterior. Se sim: menos falhas, mas o sistema passa a entregar como resposta algo que o
modelo não deu como resposta. Se não: essas respostas continuam a contar como vazias e tratam-se pela D2.

**D4 — Qual é o segundo modelo, e com que conta?**
A fase só fica provada com um segundo tipo de modelo a trabalhar a sério. Preciso de saber: que fornecedor,
em que região, com que chave e com que limite de despesa.
*Recomendo: um modelo que obrigue a devolver-lhe as suas notas de raciocínio no passo seguinte, ligado pelo
mesmo intermediário que já usamos.* É o caso difícil; um modelo «fácil» cumpria a letra da fase sem provar
nada de novo. Se escolher um fácil: a fase fecha mais depressa e o problema difícil fica por resolver.

**D5 — Autoriza um posto de ensaio à parte, com o modelo verdadeiro e uma chave com limite próprio?**
Hoje todos os ensaios passam pela fila de produção: uma hora por série, e qualquer mudança de teste afecta
todos os pedidos reais. Um posto de ensaio faz o mesmo em minutos, sem tocar em produção. A 2026-10-06 a
medição directa foi recusada; peço que seja reconsiderada **com um limite diário de pedidos**.
*Recomendo: sim, com limite (por exemplo, 500 pedidos por dia).* Se sim: o teste dos separadores faz-se numa
tarde e cada modelo novo é ensaiado antes de chegar a produção. Se não: continua-se pela fila, e o teste dos
separadores não se consegue fazer de forma limpa.

## 8. Critério de prova da fase A2

A fase passa a **provada** quando os sete pontos estiverem medidos e registados no acompanhamento §5.

| # | Critério | Medida |
|---|---|---|
| P1 | **A causa do `empty_output` tem nome** | Com o AOS-507 em `observe`, 100% dos turnos fechados `empty_output` têm forma registada, em pelo menos 3 ocorrências; a classe dominante fica escrita |
| P2 | **A resposta vazia deixa de falhar planos** | Planos falhados por `empty_output` abaixo de 1% em pelo menos 120 planos com o AOS-511 em `on` (hoje 3 em 140, 2,1%), e zero eventos `tool.call.*` nos runs que antecederam uma tentativa admitida |
| P3 | **Duas famílias completam runs com tools** | Cada família: pelo menos 40 planos em que o nó com tools chega ao segundo turno (o pedido que leva o `assistant` com tool calls e a mensagem `tool`), «não cumprido» abaixo de 2%, e **zero** respostas 4xx do provider nesses segundos turnos |
| P4 | **O estado opaco é exercitado, não só tolerado** | Pelo menos uma das duas famílias tem perfil `devolver = obrigatório`; nos seus turnos, a forma registada mostra estado recebido e o pedido seguinte levou-o (contador por turno) |
| P5 | **Sondas de protocolo deterministas** | A bateria de falsos passa a 100% em CI para cada classe marcada «qualificada» na matriz, e cada sonda tem o controlo negativo: retirado o estado, fica vermelha |
| P6 | **Replay e segredo** | Fidelidade de replay de 100% em runs das duas famílias, incluindo um retomado a meio com estado; zero sentinelas de raciocínio em `turn.recorded`, métricas, spans e logs |
| P7 | **Sem regressão na rota de produção** | Com tudo o que for ligado, a taxa de primeiras falhas e o «não cumprido» do Kimi não pioram face à última série anterior (mesmo tamanho de série) |

P1 e P2 fecham o defeito medido; P3 a P6 são a fase propriamente dita; P7 é a salvaguarda.

## 9. O que esta nota não verificou

- Onde o loop escreve o segmento `history` e o `turn.recorded` (`loop.go`, `turn.go` lidos só por procura).
- O que o `gateway.go` e os estágios da pipeline fazem à resposta entre o adaptador e o tradutor.
- O que a versão do proxy em produção faz a cada forma de resposta — é precisamente o que o AOS-508 mede.
- Se o `kimi-for-coding` aceita parâmetros de raciocínio, e que campos devolve de facto.
- Se o `reconstruct` expõe o raciocínio capturado (indício de que não).
- Se já existe, noutro ramo, um ADR com o número seguinte ao último deste: o número do ADR novo
  reserva-se na altura.

## 10. Verificação na entrada no repositório (2026-10-07)

As três afirmações da §0 foram conferidas contra o código da base (`8b692c6f`) antes de este
documento ser versionado. As três confirmam-se.

| Afirmação | Resultado | Onde `[CÓDIGO]` |
|---|---|---|
| O adaptador lê `reasoning_content`, e o valor vai para a captura | **Confirmada.** O campo está declarado na mensagem da porta e aceita qualquer valor JSON; o tradutor copia-o para `ModelResponse.Reasoning`; a captura grava-o como conteúdo (envelope selado com cifra por titular, referência em modo sensível) e devolve-o na retoma | `port/port.go:92`, `:103-131`; `runtime_adapter.go:379`; `captura:85-91`, `:466`, `:483-486`, `:563` |
| `content` em partes e `function.arguments` em objecto fazem recusar a resposta inteira | **Confirmada.** Os dois campos são `string`; o `json.Unmarshal` da mensagem devolve o erro, `UnmarshalChatResponse` devolve uma resposta vazia com esse erro, e o adaptador propaga-o sem turno nenhum | `port/port.go:56`, `:72`, `:110-112`; `port/normalize.go:165-169`; `adapters/openai_http.go:122-125` |
| O id de tool call do provider é descartado | **Confirmada.** `ToolCall.ID` é descodificado; o tradutor copia só o nome e os argumentos; `ToolInvocation` não tem campo de id. O id que volta ao provider tem a forma `<passo>-tool-<n>`, do runtime | `port/port.go:62`; `runtime_adapter.go:380-385`; `model.go:67-95`; `projection.go:502`, `:632-636`, `:656` |

Foram ainda conferidas por amostragem, e batem, as referências a `turn.go:120` e `:128`,
`model.go:8-16`, `:124-131`, `:140-141`, `:161-163` e `:176-177`, `termination.go:53`,
`completion.go:450-453` e `:465-466`, `runtime_adapter.go:238-247` e `:407-422`,
`port/normalize.go:25-34`, `:86-119`, `:141-154` e `:327-329`, `port/port.go:161-164`, `:303` e
`:324-336`, `projection.go:89-96`, `:218-220`, `:408-410` e `:622-629`,
`adapters/openai_http.go:210` e `captura:99-105`. A procura por `reasoning` em `packages/cmd/aos`,
fora de testes, voltou a não devolver nada (§2.3). As contas da §2.4 (73% e 93%) e da §5.3 (53
pedidos por braço) foram refeitas e batem.

**O que mudou em relação ao texto apresentado ao dono:**

1. **Numeração.** O desenho propunha dez tickets numerados. Abriram-se cinco (AOS-507 a
   AOS-511); os outros dependem das decisões D4 e D5 e passaram a rótulos sem número.
2. **A ressalva do modo sensível** (§0.1 e §2.3). O texto dizia que o raciocínio «está na captura
   selada dos três runs». É verdade com cifra por titular e fora do modo sensível; em modo
   sensível a captura guarda uma referência no lugar do raciocínio, e o passo zero da §2.3 não se
   faz. Não foi verificado em que modo corre o nó de produção.
3. **O aviso na tentativa por vazio** (§3a). O texto deixava-o «por omissão, sem aviso». A
   decisão passou para o AOS-510, com a justificação e o gatilho para a reabrir.
4. **A origem dos 140 planos** (§2.4) ficou escrita: são as três séries da v0.1.51 com a
   recuperação ligada, e os três vazios estão nas duas primeiras.
5. **Partes que não são texto** (§6, linha do AOS-509). O desenho dizia que uma parte de
   `content` que não é texto era «contada na forma, nunca erro». O ticket decide o contrário:
   continua a recusar a resposta, com causa própria — aceitar deitando a parte fora entregava
   como completa uma resposta a que falta conteúdo. A sonda do AOS-507 conta-a na mesma.
6. **A excepção do «byte a byte»** (§6, linha do AOS-509). Uma resposta que hoje passa e traz o
   raciocínio noutro nome, sem `reasoning_content`, passa a ter o raciocínio na captura. É a
   única diferença em respostas hoje aceites, e o ticket declara-a.
7. **A base.** O desenho leu `35109224`; a base do ramo é hoje `8b692c6f`, sem diferenças em
   `packages/`.
