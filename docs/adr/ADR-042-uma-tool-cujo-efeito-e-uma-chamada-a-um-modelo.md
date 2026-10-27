# ADR-042 — Uma tool cujo efeito é uma chamada a um modelo: o OCR por um modelo de visão, pelo Model Gateway do nó

- **Estado:** Aceite — **ratificado pelo dono a 2026-10-15** («ratifico»). Nada do que está aqui
  está implementado: a implementação é o AOS-530.
- **Data:** 2026-10-15
- **Deciders:** Dono do produto (a decisão de fundo, de 2026-10-15: a D2 do desenho
  `docs/reports/desenho-a5-a6-multimodal-2026-10-14.md` muda de «OCR local no gVisor» para «OCR
  por um modelo de visão, através de uma tool», depois da medição do AOS-529; o Tesseract fica
  como alternativa local). As decisões §2.1 a §2.9 deste ADR foram ratificadas pelo dono a
  2026-10-15.
- **Tickets:** AOS-530 (a tool `ocr_read`, reescrito a 2026-10-15 para esta decisão). Mede-se com
  o AOS-529 (o banco de ensaio, experiência `ocr`).
- **Relacionados:** ADR-002 (Reference Monitor mandatório), ADR-003 (NHI e cadeia de delegação),
  ADR-005/ADR-034 (taint e autoridade do contexto), ADR-010 (replay e WORM), ADR-019 (excepções
  de camada), ADR-036 (§2.8, o perfil da rota), ADR-037 (o veredicto e a resposta cortada),
  ADR-038 (a saída por referência), ADR-041 (§2.1, titular e candidato)

> **Resumo.** Uma tool pode ter como efeito **uma chamada a um modelo**. A primeira é a
> `ocr_read`: lê uma imagem da pasta semeada e devolve o texto transcrito por um modelo de visão.
> A chamada passa **pelo Model Gateway do nó** — allowlist assinada, perfil da rota com a
> comparação do modelo servido, custo pela tabela de preços, selos — e nunca directamente ao
> proxy nem a partir da sandbox, que não tem rede. A tool é uma função registada no Reference
> Monitor pela raiz de composição (`cmd/aos`), com `RegisterCosting`: o kernel continua sem
> conhecer o gateway, e a mediação é uma só. Chama o gateway **com o token NHI do run**, o mesmo
> que o RM verificou para a tool call, e com o run e o passo da tool call: a cadeia de delegação
> não ganha um elo. O modelo é um **alias próprio** (`ocr-visao`) na allowlist assinada, com
> perfil de rota num registo de entrada assinado **de uso «tool»**, que nunca é candidato a servir
> runs. A imagem é lida pelo nó e vai ao modelo como **parte de imagem do pedido** — uma capacidade
> nova da porta, que só uma rota que a declare aceita: o titular continua a não ver imagens. O
> resultado é texto, `untrusted`, com o digest da imagem e o modelo servido; o custo grava-se no
> run e conta no tecto em dólares; o replay usa o resultado capturado e **não** volta a chamar o
> modelo.

## 1. Contexto

### 1.1 A medição que a justifica (AOS-529, 2026-10-15)

Banco de ensaio, experiência `ocr`, bateria `bateria-ocr/` (digest `sha256:390f70c8…c703`):
quatro documentos sintéticos, treze imagens (PNG limpo, com ruído e rotação, de meia resolução,
JPEG, um PDF digitalizado e uma tabela), com e sem acentos.

| Motor | Onde correu | Factos (regra do banco) | Com acentos exactos | Tempo por imagem | Custo por imagem |
|---|---|---|---|---|---|
| Tesseract | Local, contentor fixado pelo digest, com os limites do gVisor | 82,5% | 76,2% | ~0,9 s | zero |
| Claude Haiku 5.5 (visão) | OpenRouter, com autorização do dono | **100%** | **100%** | 2 a 9 s | ~0,0001 USD |
| Gemini 3.8 Flash (visão) | OpenRouter, com autorização do dono | 89,2% | — | — | ~0,0034 USD |

**Ressalvas, que limitam o que a medição prova:**

- As imagens são **sintéticas**, com uma fonte bitmap 5×7 desenhada no banco: a taxa não é a de
  um documento impresso, e pode pesar de forma diferente num OCR clássico e num modelo de visão.
- **Doze imagens** entram nas contas do braço de visão (o PDF é a mesma página de outra imagem e
  fica fora — `packages/qa/banco-ensaio/ocr_visao.go`, comentário do braço). É uma amostra
  pequena; 100% em doze não é uma taxa de erro nula.
- **A resposta do Gemini na tabela veio cortada**, provavelmente pelo limite de tokens de saída
  do adaptador do banco (`TokensDaVisao = 2048`, `ocr_visao.go:59`) — **por confirmar**. Os
  89,2% não são, por isso, a medida do Gemini.
- Nenhum documento real; nenhum documento em produção.

### 1.2 O que o código tem hoje

- **A porta do gateway só leva texto no pedido.** `port.Message.Content` é uma `string`
  (`packages/platform/model-gateway/port/port.go:75-77`); a versão da porta é `1.11.0` (`:37`).
  Na **resposta**, uma lista de partes com uma parte que não é texto é recusada com
  `ErrContentPartNotText` (`port.go:165`, recusa em `:203`) — de propósito, para não entregar como
  completa uma resposta a que falta conteúdo (AOS-509). Não existe parte de imagem no pedido.
- **O adaptador RT→GW monta o pedido a partir do prompt materializado**: uma mensagem `user` com
  `string(view.Materialized)` (`packages/platform/model-gateway/runtime_adapter.go:295`) ou a
  projecção nativa do tail; o pedido leva `Principal`, `Region`, `Board`, `RunID`, `StepID`
  (`:307-322`). Nada nele produz uma parte de media.
- **O perfil da rota só conhece uma capacidade**, `tools` (`route.go:94`); qualquer outra é
  recusada na validação (`route_profile.go:98`).
- **O gateway faz, por chamada, tudo o que uma chamada paga precisa:** a atribuição selada antes
  do efeito (`gateway.go:371`), o adaptador do provider (`:387`), o custo pela tabela de preços,
  fail-closed (`:408`, `recordCost` em `:727-752`), o custo na resposta normalizada (`:414`) e a
  comparação do modelo servido com o perfil (`:419`; `route.go:363`). Os embeddings
  (`gateway.go:549-590`) são o precedente de uma operação que não é um turno do executor e é
  contabilizada pelo mesmo caminho.
- **O token do run chega ao gateway pelo contexto.** O adaptador lê o principal por chamada
  (`runtime_adapter.go:80-93`, `:260-265`); o nó liga-o a `modelCredentialFromContext`
  (`packages/cmd/aos/modelgatewaywiring.go:330-356`), e o valor é anexado ao contexto do run em
  `service.go:928` (`withModelCredential`, `resume_model.go:65`; a retoma repõe a credencial
  fresca, `resume_model.go:56-60`). O token tem de selar `model:invoke`
  (`modelgatewaywiring.go:139`).
- **Uma tool é uma função registada no RM.** `ToolFunc` recebe só o contexto e o input
  (`packages/kernel/reference-monitor/monitor.go:23`); `CostingToolFunc` devolve também o custo
  medido (`:36`), registada com `RegisterCosting` (`:217`). O despacho é o único caminho de
  execução, sob permit de uso único (`:740-768`), e anexa ao contexto o produtor de eventos da
  call (`:760`). **Nenhuma tool do nó usa `RegisterCosting`.**
- **O custo medido de uma tool é só observabilidade:** vai ao span `aos.activity` e nunca ao
  durável (`decision.go:151-159`; `activity/dispatch.go:243-358`). O tecto em dólares por run
  (AOS-260, ligado em produção pelo AOS-527) é debitado **só** pela admissão do turno de modelo
  (`packages/integration/model_admission.go:141`); a estimativa das tool calls é zero em dólares
  (`integration/budget.go:107-112`), e o consumo durável de uma tool call grava a **reserva
  estimada**, sem dólares (`budget.go:545-583`).
- **O input real chega à tool pela reescrita do efeito.** O nó já traduz os args do modelo, com o
  `RunID`/`StepID` reais da Call, para o input de uma tool com binding (`sandboxwiring.go:80-99`).
- **A sandbox não tem rede** (`deploy/server/gvisor/component/main.go:147-157`) e o guest só
  conhece o verbo `read` (`deploy/server/gvisor/guest/main.go:69-88`). A pasta semeada está
  montada só no serviço `gvisor` (`deploy/server/docker-compose.prod.yml:943-946`); o nó não a
  tem, e `AOS_SANDBOX_SEED_DIR` vem vazia (`:359`).
- **A allowlist do gateway é embebida e assinada** (`policy/allowlist/allowlist.go:59-65`, chave
  de confiança presa a uma constante), e autoriza no `board-eu` só `gpt-4o` e `gpt-4o-mini`
  (`allowlist_policy.json:17-27`). Um nome novo é uma allowlist re-assinada **e** uma imagem nova
  do nó.
- **Um registo de entrada assinado cujo alias não é o do titular é um candidato.**
  `naoTitulares` (`packages/cmd/aos/modelo_candidato.go:72-82`) conta-o; com
  `AOS_MODEL_CANDIDATE=on`, dois deles impedem o arranque (`:105-115`), e o nó constrói um
  adaptador de run para cada um (`modelgatewaywiring.go:374-385`). E o registo só entra com um
  relatório da experiência `qualificacao` com veredicto `qualificado`
  (`policy/routeentry/verdict.go:22-29`); o relatório do AOS-529 é da experiência `ocr`, sem
  veredicto (`packages/qa/banco-ensaio/ocr.go:225`).
- **O proxy não é assinado** (`deploy/server/litellm/config.yaml:9-15`): o nó governa que nomes se
  pedem; o ficheiro decide o que significam.
- **O resultado de uma tool é sempre `untrusted`** e torna o contexto do run `untrusted` até ao
  fim (`packages/kernel/agent-runtime/context_authority.go:8-24`).
- **O replay não re-executa efeitos** (`agent-runtime/replay/doc.go:1-4`); o resultado de cada tool
  call está no step-ledger e na captura, cifrados por titular (`cmd/aos/bootstrap.go:1702`,
  `:1721-1722`).

## 2. Decisão

### 2.1 Por onde passa a chamada: pelo Model Gateway do nó, dentro de uma tool mediada

- A `ocr_read` é uma **`CostingToolFunc` registada no RM do nó** com `RegisterCosting`, pela raiz
  de composição (`packages/cmd/aos`). O efeito dela é **uma** chamada `Gateway.Chat` ao gateway que
  o nó já compõe — o mesmo objecto que serve os turnos, não um segundo gateway.
- **A mediação é uma só:** o modelo pede `ocr_read`, o RM decide como para qualquer tool
  (identidade, capability, recurso `doc://{doc_id}`, taint, risco, aprovação), sela
  `tool.call.mediated` no WORM, e só depois do permit o despacho chama a função. A leitura da
  imagem e a chamada ao modelo são o efeito dessa call; nenhuma delas é uma segunda tool call.
- **Não muda o sentido das dependências** (ADR-019): o kernel não importa o gateway, o gateway não
  importa o RM para isto, e `cmd/aos` já importa os dois (`modelgatewaywiring.go`,
  `sandboxwiring.go`). Não há excepção nova na baseline do `layer-lint`.
- **Não corre na sandbox**: o gVisor não tem rede, e isso é uma garantia que não se abre para esta
  tool. **Não fala com o proxy directamente**: perdia a allowlist, o perfil, a comparação do
  modelo servido, o keypool e a contabilidade, que o gateway já faz por chamada.
- A tool entra pelo registo de tools do operador (`deploy/server/model-tools/tools.json`), com um
  bloco de ligação **novo e trusted**, ao lado do `sandbox` — por exemplo
  `"modelo": {"alias": "ocr-visao"}` (a forma exacta é do AOS-530). O modelo escolhe só o nome da
  tool e o `doc_id`; o alias, a instrução e os limites são do registo e do código, nunca dos args.
  Uma entrada com os dois blocos (`sandbox` e `modelo`) recusa o arranque.

### 2.2 Identidade: o token NHI do run, com o run e o passo da tool call

- A tool chama o gateway com **o token NHI do run** — o mesmo `Call.Credential` que o RM verificou
  para esta tool call —, lido do contexto do despacho pela mesma função que o adaptador dos turnos
  usa (`modelCredentialFromContext`). O estágio authn do gateway volta a verificá-lo e resolve o
  principal dele; o token tem de selar `model:invoke`, como para um turno.
- O pedido leva o **`RunID` e o `StepID` da tool call** (que chegam ao input pela reescrita do
  efeito, como no `sandboxwiring.go:80-99`), e um metadado novo de plataforma — o identificador
  da tool que fez o pedido —, que não vai no wire e entra no selo de atribuição do gateway. Um
  selo do gateway distingue assim um turno do executor de uma chamada feita pela `ocr_read`.
- **Na cadeia de delegação não entra nenhum elo:** humano → mandato → NHI do run → tool call
  `ocr_read` (selo do RM, com a cadeia) → chamada ao gateway (selo de atribuição, com o mesmo
  principal, o run e o passo). Quem pagou e quem autorizou é o mesmo que autorizou a tool call.
- **Sem credencial no contexto, a tool falha antes de qualquer leitura** — nunca se forja um
  principal (a regra do AOS-278).

### 2.3 Que modelo: um alias próprio, na allowlist assinada, com perfil num registo de uso «tool»

- **Um alias novo, `ocr-visao`**, acrescentado à allowlist assinada do `board-eu` (regiões `eu` e
  `eu-west`). Exige **re-assinar a allowlist** e uma **imagem nova do nó**, porque a allowlist é
  embebida. Não se reaproveita o `gpt-4o-mini`: tem um perfil de chat com tools e é o recuo natural
  do titular.
- **O perfil da rota chega por um registo de entrada assinado pelo dono** (`policy/routeentry`),
  como os outros, com duas mudanças no formato do registo:
  - um campo **`uso`**, assinado, com `run` (a omissão: o que existe hoje) ou `tool`. **Um registo
    de uso `tool` nunca é candidato:** não conta em `naoTitulares`, não ganha adaptador de run no
    despachante, não entra no canary nem no disjuntor, e um run não se pode fixar nele. Um registo
    `tool` com fatia de canary maior que zero recusa o arranque. É uma **emenda ao ADR-041 §2.1**
    («um candidato é um registo assinado cujo alias não é o do titular» passa a «… e cujo uso é
    `run`»);
  - o relatório que o registo cita pode ser da experiência **`ocr`**, com um **veredicto**
    calculado pelo banco contra limiares do dono (a proposta da D7 é «factos presentes em pelo
    menos 90%»). Hoje a experiência `ocr` não dá veredicto: o banco ganha-o, e o nó aceita para um
    registo de uso `tool` só um relatório `ocr` com veredicto `qualificado` e modo `real`.
- **O perfil declara a capacidade nova `imagem`** (§2.4), **não** declara `tools`, e declara o
  `expected_model` que o proxy tem de dizer ter servido. A comparação do modelo servido corre
  nesta chamada como num turno — está dentro de `Gateway.Chat` (`gateway.go:419`). Com a
  governação da rota em `enforce`, um modelo servido diferente faz a tool falhar.
- **O proxy** ganha a rota `ocr-visao` no `config.yaml` (sem assinatura, como as outras). Hoje
  seria `openrouter/anthropic/claude-haiku-5.5`, o modelo medido. Trocar o modelo por trás do alias
  sem mudar o `expected_model` do perfil assinado é apanhado pela comparação; mudar o perfil é
  assinar um registo novo, com um relatório novo.
- **Preço obrigatório.** O par (`ocr-visao`, região do nó) tem de ter preço na tabela de preços do
  operador (AOS-527). Com a contabilidade de custo composta e a `ocr_read` no registo, um par sem
  preço **recusa o arranque** (o mesmo princípio do `ErrModelCandidateWithoutPrice`): a
  contabilidade recusaria todas as chamadas.

### 2.4 Entrada: a imagem por referência, lida pelo nó, como parte de imagem só deste pedido

- **O media entra pela pasta semeada** (D4 (a)), referido por `doc_id`, com o recurso
  `doc://{doc_id}` selado pelo RM. Sem URL.
- **EMENDA (AOS-561 + AOS-563, 2026-10-27): há uma SEGUNDA origem, e o «sem upload por API» caiu.**
  Quem submete um run pode mandar os bytes de uma imagem no corpo do `POST /runs` (`media_inputs`);
  o nó confere-os pelos primeiros bytes e sela-os no espaço de handles do run antes de o run abrir
  (ADR-043 §2.6). O `doc_id` resolve-se então por **duas** origens, nesta ordem: as **entradas de
  media do run** primeiro, a **pasta semeada** depois — nunca as duas para o mesmo `doc_id`, e uma
  colisão resolve pelo espaço do run e é contada no `/metrics`. A emenda escreve-se aqui, e não no
  ADR-043, porque é esta a secção que afirmava a exclusividade; o ADR-043 §6 manda que cada emenda
  se escreva no ticket da fase que a torna verdadeira, e quem a torna é o AOS-563.
  **O que NÃO muda, e é o que faz disto emenda e não supersessão:** a pasta semeada FICA — é a
  origem de tudo o que está provado em produção — e as regras de leitura dela, abaixo, ficam byte a
  byte iguais; o recurso continua `doc://{doc_id}`, a capability `cap:fs.read` e a classe de destino
  a do ADR-044. Qual das origens serve um nó é **config trusted do operador**, não autorização: a
  segunda origem só existe com a via **COMPOSTA** — `AOS_ENTRADA_DE_MEDIA=on` **e** `AOS_HANDLE_DIR`
  definida —, e é esse valor composto que liga o plano de handles às duas tools, que a porta confere
  para recusar um `media_inputs` com 400, e que o `GET /tools` anuncia. Um nó com `AOS_HANDLE_DIR`
  só pelo layout 1.8.0 **não** ganha a segunda origem, e o banner de arranque de cada tool declara
  qual das duas posturas o nó tem (um só eixo decide as quatro coisas, para que não possam
  divergir). Da origem nova o tipo da parte é o **conferido na porta** (dos factos do handle), não
  uma segunda inferência dos bytes. A `video_read` fica de fora: o tecto de um vídeo não é o de uma
  imagem, e é ticket próprio.
- **O nó lê o ficheiro**, de uma montagem só de leitura da **mesma** pasta semeada que o serviço
  `gvisor` usa, por uma variável própria (não `AOS_SANDBOX_SEED_DIR`, que carrega a pasta inteira
  em memória no arranque). A leitura é confinada à pasta com `os.Root` (Go ≥ 1.24: recusa `..`,
  caminhos absolutos e ligações simbólicas que saiam da raiz), só no nível de topo.
- **Limites antes de ler:** só ficheiros regulares; **tamanho máximo de 4 MiB** (proposta, a
  confirmar contra o limite do fornecedor); formato **PNG ou JPEG**, decidido pelos primeiros bytes
  do ficheiro e não pela extensão; PDF fica de fora (D2) até haver um caso. Fora disto, a tool
  falha com causa própria e **não chama o modelo**.
- **O digest** (`sha256` dos bytes lidos) calcula-se na leitura; é o que fica no resultado.
- **A porta do gateway ganha partes de media no pedido**, numa versão MINOR (`1.12.0`): uma
  mensagem pode levar, além do texto, uma parte de imagem com o tipo e os bytes, que o adaptador
  OpenAI escreve como `image_url` numa data URL (a forma medida pelo banco). **As respostas
  continuam só texto**: `ErrContentPartNotText` fica.
- **A parte de imagem fica restrita a esta chamada.** Três barreiras:
  1. só a `ocr_read` constrói pedidos com partes de imagem; o adaptador RT→GW dos turnos não tem
     por onde as produzir (o prompt é texto, `runtime_adapter.go:295`) e não é alterado;
  2. o gateway **recusa antes de sair** um pedido com uma parte de imagem para uma rota cujo
     perfil não declare `imagem` — o perfil do titular não a declara;
  3. a chamada da tool **não oferece tools** ao modelo de visão (`Tools` vazio) e o perfil do
     alias não declara `tools`: o modelo que lê a imagem não pode pedir efeitos.
- **Os bytes da imagem nunca entram em eventos, spans, métricas nem logs** — só o digest, o
  tamanho e o formato. O pedido leva uma **instrução fixa e versionada**, a mesma que o AOS-529
  mediu (`ocr_visao.go`, `InstrucaoDaVisao`), cujo digest vai no resultado.

### 2.5 Resultado: texto `untrusted`, com o digest do media e o modelo servido

- O resultado é um JSON de forma fixa e versionada (`aos-ocr/v1`): o **texto transcrito**, o
  `sha256` e o tamanho da imagem, o formato, o **alias** pedido, o **modelo servido** que o proxy
  declarou, o digest do perfil da rota, o digest da instrução e o motor (`modelo`). Sem os bytes
  da imagem.
- **É `untrusted`**, como o resultado de qualquer tool: entra no tail como `tool_result`, e o
  contexto do run passa a `untrusted` (ADR-034). Uma imagem pode trazer instruções escritas; o
  modelo de visão transcreve-as e o executor recebe-as como dados.
- **Entre nós de um plano**, o resultado é «outro resultado» no sentido do ADR-038: o `aos-orq`
  entrega os bytes do JSON tal como estão (`formaCru`, `cmd/aos-orq/saida_por_referencia.go:237-238`
  — não é o envelope da sandbox). O nó seguinte recebe o texto **com** o digest e o modelo. Ensinar
  o `aos-orq` a extrair só o texto fica de fora, com gatilho: um plano em que o JSON atrapalhe o
  nó seguinte.

### 2.6 Custo: medido pelo gateway, gravado no run, contado no tecto em dólares

- **O número é o do gateway**: o custo da chamada pela tabela de preços (`recordCost`), devolvido
  na resposta normalizada (`gateway.go:414`). A tool reporta-o pelo canal do `RegisterCosting`.
  Nenhum preço novo no código; nenhuma estimativa por imagem.
- **Grava-se no run, de forma durável**, por (`run_id`, `step_id`) da tool call, idempotente por
  essa chave — um evento novo no stream do run (nome a fixar no AOS-530), só com o custo, os
  tokens, o alias e o modelo servido. **Conta no consumo durável do run** ao lado dos turnos, e é
  lido por quem retoma o run.
- **Conta no tecto em dólares por run** (`AOS_BUDGET_MAX_COST_MICRO_USD`): antes do efeito, a tool
  **reserva** um máximo por chamada, calculado pela tabela de preços a partir de dois tectos da
  tool — os tokens de entrada que uma imagem pode custar e o `max_tokens` de saída —; se a reserva
  não cabe no que resta ao run, a tool é negada sem chamar o modelo. Depois do efeito salda-se com
  o número medido. Um custo medido acima da reserva grava-se como foi, nunca truncado.
- **Uma chamada que o provider respondeu e a tool recusou** (resposta cortada, vazia, modelo
  servido diferente) **custou dinheiro e grava-se na mesma**.
- **Replay e dedup não gravam custo** (o custo da `Decision` nunca vai ao ledger,
  `decision.go:151-159`): o custo de uma tool call é um facto do run original.
- O tecto do dia é o da conta na OpenRouter, do dono (D6).

### 2.7 Replay: o texto capturado, sem nova chamada

- O resultado da `ocr_read` fica no step-ledger e na captura do turno, **cifrado por titular**,
  como o de qualquer tool. O replay usa-o e **não chama a tool nem o modelo** (D5 (a)); o
  `prompt_hash` reproduz-se porque o texto que entrou no tail é o capturado.
- Os bytes da imagem **não** se guardam (D5 (b) recusada agora): o digest diz qual era. Se a
  imagem mudar ou desaparecer da pasta, o replay não muda; uma **nova** chamada (um run novo) lê o
  que lá estiver, e o digest diferente mostra-o.
- Re-correr a tool no replay (D5 (c)) fica **rejeitado**: pagava outra vez e podia ler outra
  imagem.

### 2.8 Falhas e limites

- **Tempo:** a chamada ao gateway tem um prazo próprio, **60 s** (proposta; a medição deu 2 a 9 s),
  dentro do prazo da tool call. Esgotado, a tool falha com causa própria.
- **O modelo não responde, ou responde com erro:** a tool falha com o erro atribuível do gateway
  (sem o corpo da resposta do provider); **não há nova tentativa dentro da tool** nem failover para
  outro modelo — o modelo de uma transcrição é o do perfil assinado, ou nenhum. O run vê um erro de
  tool, como hoje.
- **Resposta cortada** (o motivo de paragem é `length`): **falha** (`ocr_cortado`). Um texto
  cortado não passa por transcrição completa — é a lição do Gemini na medição. O `max_tokens` da
  chamada é uma constante da tool, proposta **4096**, a confirmar na medição.
- **Resposta vazia:** falha (`ocr_vazio`). Uma imagem sem texto devolve um texto vazio só se o
  modelo o disser explicitamente — a forma fica para o AOS-530.
- **Modelo servido diferente do perfil**, com a governação em `enforce`: falha; em `observe`,
  passa e fica registado como num turno.
- **Ficheiro em falta, fora da raiz, grande demais, ou de formato não aceite:** falha **antes** de
  chamar o modelo, sem custo.
- **Uma leitura por run.** A `ocr_read` usa `cap:fs.read`, a capability da `doc_read` (sub-decisão
  da D2): com a capability armada, uma segunda leitura no mesmo run é negada, venha de qual das
  duas tools vier.
- **Todas as falhas** são causas de vocabulário fechado no resultado de erro da tool e numa
  métrica do nó; nenhuma leva o texto do modelo.

### 2.9 O que fica de fora

- **O titular continua a não ver imagens** (D1, D9): nenhuma parte de imagem entra num turno do
  executor; a «A5-titular» continua à espera de um caso.
- **Vídeo e áudio:** este ADR é o mecanismo que a «A6-video» vai usar (outra tool, outro alias,
  outra capacidade no perfil), mas não os decide — cada um com o seu alias, a sua medição e o seu
  registo assinado.
- **A alternativa local (Tesseract no gVisor)** fica descrita no AOS-530 e **não é implementada**:
  é o caminho para documentos que não podem sair do servidor. Quando existir, é outra ligação da
  **mesma** tool (`motor` no resultado), sem rede e sem custo.
- **Redacção do conteúdo da imagem:** não há; a imagem vai ao fornecedor como está. Vale a
  premissa do dono — o sistema não trata dados pessoais de pessoas na UE — e a regra «só UE» para
  modelos foi levantada (2026-10-13).
- **Upload por API, URL externa, PDF, geração de media, streaming.**

## 3. Alternativas consideradas

- **A tool chama o proxy directamente, com uma chave própria.** Duplicava a allowlist, a
  credencial, a contabilidade e a comparação do modelo servido; o custo não entrava no tecto do
  run. Rejeitada (D3 do desenho, «custo e proveniência num só sítio»).
- **O OCR por modelo dentro da sandbox.** O gVisor não tem rede por garantia. Abrir rede a um
  executor de tools para uma delas é a classe de risco que a sandbox existe para fechar. Rejeitada.
- **Um componente à parte para tools com modelo** (um serviço novo com a sua chave). As mesmas
  duplicações da primeira, mais um serviço a operar. Rejeitada.
- **O titular recebe a imagem e lê-a ele próprio** («A5-titular»). Muda o prompt, o `prompt_hash` e
  o replay, paga a imagem em cada turno seguinte, e o que o modelo «viu» não fica legível.
  Adiada, com o gatilho de um caso (D1).
- **Uma NHI própria da tool** (cunhada para a `ocr_read`). Precisava de um mandato novo e de
  cunhagem por chamada, e a cadeia passava a ter um ramo que não é o do run — o gateway não
  saberia que run pagou. **A identidade do nó** (um principal fixo): é o principal forjado que o
  AOS-278 removeu. Rejeitadas: o token do run já é verificado pelo RM e pelo gateway e liga a
  chamada ao humano do run.
- **A tool recebe o token pelo input** (a reescrita do efeito pô-lo-ia lá). O input da call vai ao
  step-ledger e ao span; um bearer não pode ir. Rejeitada: o token viaja pelo contexto, como para
  os turnos.
- **Reaproveitar um alias existente** (`gpt-4o-mini`). Tem um perfil de chat com tools e é o recuo
  do titular; misturava uma rota de runs com uma rota de tool. Rejeitada.
- **O perfil do alias na tabela em código** (`routeProfiles`), em vez de num registo assinado.
  Entraria por PR e release, sem o veredicto do banco nem a assinatura do dono sobre ele — a regra
  da fase A3 é que nenhum modelo entra sem veredicto assinado. Rejeitada.
- **O registo de uso «tool» tratado como um candidato com fatia zero.** Contava como candidato
  (com `AOS_MODEL_CANDIDATE=on` e um candidato real o nó não arrancava) e ganhava um adaptador de
  run. Rejeitada: o uso tem de estar assinado e separado.
- **O nó lê a imagem pela sandbox** (o verbo `read` do gVisor) e depois chama o modelo. Pedia uma
  segunda mediação — que a regra de uma leitura por run negaria — ou uma via exportada para
  executar a sandbox fora do RM, que o desenho fecha de propósito (`substrate/sandbox/mediated.go`,
  despacho não exportado). Rejeitada: o nó lê da montagem só de leitura, confinado por `os.Root`.
- **Aceitar a parte de imagem em qualquer pedido** (sem capacidade no perfil). Um engano no
  adaptador dos turnos mandava imagens ao titular sem que nada falhasse. Rejeitada.
- **Custo só no span** (o que existe). Não cumpre «custo no run» e não conta no tecto. Rejeitada.
- **Re-correr a tool no replay.** Rejeitada (§2.7).

## 4. Consequências

- O repositório passa a ter **uma tool cujo efeito é uma chamada paga a um modelo**, com custo
  no run e no tecto em dólares. É o primeiro uso de `RegisterCosting` no nó e o primeiro custo de
  tool que chega ao durável.
- **A porta do gateway muda de versão** (`1.12.0`, MINOR): partes de imagem no pedido, um metadado
  de origem, a capacidade `imagem` no perfil. Os perfis de hoje não a declaram: o digest deles e
  os pedidos às rotas de hoje não mudam um byte.
- **O formato do registo de entrada muda** (o campo `uso`, e o relatório `ocr` com veredicto), e o
  ADR-041 §2.1 é emendado. Um registo sem `uso` é `run`, como hoje.
- **O banco de ensaio ganha um veredicto na experiência `ocr`**, com limiares do dono.
- **Entrar em produção exige actos do dono:** re-assinar a allowlist (e uma release), assinar o
  registo do alias com o relatório qualificado, pôr o preço na tabela, acrescentar a rota ao proxy
  e a entrada `ocr_read` ao `tools.json`. Sem a entrada no `tools.json`, o nó é o de antes.
- **O nó passa a montar a pasta semeada** (só de leitura) quando a `ocr_read` está ligada.
- O conteúdo das imagens **sai do servidor** para o fornecedor do modelo, como o prompt de cada
  turno. Para documentos que não podem sair, a alternativa local fica descrita, por fazer.
- O **audit de governação do gateway** é um armazém próprio, não o WORM do nó
  (`cmd/aos/posture_banner.go:409`): o selo de atribuição da chamada da tool tem o mesmo limite
  que o de um turno. A prova durável no log do nó é o selo da mediação, o resultado no ledger e o
  evento de custo.

## 5. Limites e resíduos

- **Por confirmar** que o contexto do despacho leva a credencial do run nos dois caminhos (o
  directo e o durável, `activity/dispatch.go:200`, `:288`) e depois de uma retoma. O código lido
  passa o contexto do loop até ao despacho; falta o teste que o prove para esta tool.
- **Por confirmar** que o `usage` que o proxy devolve para um pedido com imagem conta os tokens da
  imagem (o custo do banco é estimado pelo preço do fornecedor; o do nó vem do `usage`).
- **Por confirmar** se uma tool call que falhou consome a leitura única de `cap:fs.read`.
- **Por confirmar** o limite de tamanho e de dimensões de imagem do fornecedor (os 4 MiB são uma
  proposta).
- **Uma queda do nó entre a resposta do provider e a escrita do ledger** faz o despacho repetir o
  efeito: a chamada paga-se duas vezes, e o registo de custo, idempotente por (`run_id`,
  `step_id`), guarda só uma. Declarado; o AOS-530 mede-o.
- **O modelo pode transcrever texto que não está na imagem.** 100% em doze imagens sintéticas não
  prova o contrário em documentos reais. O digest e o modelo servido no resultado dizem o que foi
  lido e por quem; não dizem que está certo.
- **O proxy não é assinado.** A comparação do modelo servido é a defesa, e só vale com a
  governação da rota ligada.
- **O Gemini não ficou medido** (resposta cortada). Não entra neste ADR como alternativa medida.

## 6. Relação com outros ADR

- **ADR-002** — a tool é mediada pelo RM como qualquer outra; o efeito só corre sob permit.
- **ADR-003** — nenhuma identidade nova; a cadeia é a do run.
- **ADR-019** — nenhuma excepção nova: a ligação vive em `cmd/aos`, que já importa o RM e o gateway.
- **ADR-034** — o resultado é `untrusted` e o contexto do run segue a regra do join.
- **ADR-036 §2.8** — o perfil da rota ganha a capacidade `imagem`; a comparação do modelo servido
  corre na chamada da tool.
- **ADR-037** — a recusa de uma transcrição cortada segue o mesmo princípio do `truncated`: cortado
  não é completo.
- **ADR-038** — a saída por referência entrega os bytes do resultado `aos-ocr/v1` como «outro
  resultado».
- **ADR-041 §2.1** — emendado: um registo de uso `tool` não é candidato.
- **ADR-043 §2.6** — o espaço de handles do run passa a ser a **segunda origem** do `doc_id` das
  tools de imagem (emenda a §2.4, AOS-561 + AOS-563). A ordem é: entrada de media do run, depois a
  pasta semeada.

## 7. O que cada ticket implementa

| Ticket | O que entrega |
|---|---|
| AOS-529 | A medição que justifica a decisão (§1.1) |
| AOS-530 | §2.1 a §2.8: a tool `ocr_read`, a porta `1.12.0`, o registo de uso `tool` e o veredicto `ocr` no banco, o custo durável e no tecto. Pode ser partido em PR pela ordem: porta e perfil; registo e veredicto; tool, custo e replay |
| AOS-561 | A via de entrada: os bytes de uma imagem no corpo do `POST /runs`, conferidos pelos bytes e selados no espaço de handles do run (emenda a §2.4) |
| AOS-563 | As duas tools de imagem resolvem o `doc_id` no espaço de handles do run **antes** da pasta semeada (emenda a §2.4) |
