# Desenho — a fase R3 do ADR-043: a SAÍDA, 2026-10-27

> A fase R3 é a outra metade do primeiro exemplo do dono. A **via de entrada** (AOS-561 + AOS-563)
> fechou a metade de quem envia a fotografia; esta fecha a metade de quem devolve a projecção. O
> roteiro do ADR-043 §7 descreve-a em cinco palavras: «`model_invoke` genérica; partes de
> **saída**; preço por imagem; fornecedor fixado e selado; `ocr_read`/`video_read` como
> configuração» (`docs/adr/ADR-043-harness-por-capacidades.md:287`).
>
> Este documento desenha-a. Não implementa nada: o corte vai ao dono antes da primeira linha de
> código, de propósito.

---

## 1. O que o dono ganha, e o que NÃO ganha

**Ganha:** um run que **produz** uma imagem. O modelo pede uma tool de geração, o nó chama o
fornecedor pelo proxy que já está fixado, os bytes gerados ficam no armazém do run com proveniência
selada — modelo, fornecedor efectivo e custo —, e o dono descarrega-os pela rota soberana que a
fase R1b já lhe deu. Com a via de entrada mais esta fase, o exemplo 1 do dono — **fotografar um
compartimento e receber a projecção dos móveis** — funciona ponta-a-ponta.

Ganha também uma coisa que não se vê na demonstração e vale mais a prazo: **um uso novo de um
modelo deixa de ser uma tool em Go**. Hoje são duas (`ocr_read`, `video_read`) e cada uma custou um
ticket, um ADR e seis ficheiros. Depois desta fase são configurações.

**NÃO ganha, e vale dizê-lo antes de ler o resto:**

- **Vídeo gerado.** É a fase R6 e **não entra aqui** — §3.1. Não é falta de vontade: é a operação
  assíncrona, que tem um elo por medir, paga-se mesmo quando o run falha, tem uma pergunta de
  egress por responder e um proxy que responde de forma instável.
- **O modelo do run a produzir media.** Esta fase dá saída às **tools** de modelo, não aos turnos
  do titular — §3.3. A diferença não é arbitrária: é onde está medida a fronteira entre uma
  alteração contida e uma que atravessa o tail, a captura, o replay, o SSE e o transporte.
- **Escolher o modelo pela tarefa.** É a R4. Aqui o modelo de geração é um alias aprovado à mão,
  como o `ocr-visao` e o `video-visao` de hoje.
- **A R3 a correr em produção sem a cerimónia da chave.** A pré-condição da R3 é a **R2 inteira**
  (`ADR-043:287`), e a R2 está construída e **desligada** à espera de o dono ratificar o ADR-044 e
  assinar o bundle Cedar 1.2.0 (`ADR-044:3`, `:442-449`). As peças desta fase constroem-se e
  provam-se em teste sem essa cerimónia; **ligar-se não podem**. A §7 diz o que é dele.
- **Moderação do conteúdo gerado.** Resíduo já declarado: «fica com o fornecedor; o harness não
  modera» (`ADR-043:256`). Esta fase não o fecha e não finge fechá-lo.

---

## 2. O estado de partida, medido a 2026-10-27

Cada linha foi confirmada com comando próprio neste worktree. Onde o que me foi entregue estava
incompleto, está corrigido.

| O que existe | Onde | O que diz |
|---|---|---|
| **A resposta é só texto, e é fail-closed** | `platform/model-gateway/port/port.go:186`, devolvido em `:224` | `ErrContentPartNotText`: uma lista de partes em que uma não é `{"type":"text","text":<string>}` **recusa a resposta inteira**. Classificado `content_parte_nao_texto` (`rejeicao.go:19,45`) e contado em `aos_model_response_rejected_total{causa}` |
| A razão da recusa, escrita | `port/port.go:142-143` | «aceitá-la deitando a parte fora entregava como completa uma resposta a que falta conteúdo» |
| **`model_invoke` não existe** | — | Zero ocorrências como tool. O que existe é a *capability* `model:invoke` no token do run (`cmd/aos/modelgatewaywiring.go:139`) — **outra coisa**, e confundi-las seria dar a fase por meio feita |
| A porta, versão | `port/port.go:48` | `Version = "1.14.0"`. As notas das três MINOR anteriores repetem «as respostas continuam só texto» (`:40`, `:43`) |
| A parte genérica **de entrada** já existe | `port/media.go:47-50`, `:34-38`, `:95-106` | `MediaPart{MediaType, Data}` + tabela única tipo→classe + `AttachMedia`. A simetria para a saída está meio construída |
| **Operações: duas, e nenhuma é geração** | `model-gateway/pipeline/pipeline.go:50,52` | `OpChat` e `OpEmbeddings`. Dois caminhos HTTP: `/chat/completions` e `/embeddings` (`internal/adapters/openai_http.go:137,200`). **Sem `generate`, sem `job`, sem polling** |
| **Uma só classe de wire** | `model-gateway/route.go:88` | `WireOpenAIChat`. Três capacidades de perfil: `tools`, `imagem`, `video` (`:94,98,102`), e **nenhum perfil da tabela em código declara as duas últimas** |
| **Preço só por token** | `pricing/table.go:83-92` | `Rate` tem exactamente quatro tarifas, todas `*PerMTokMicroUSD`. A chave é `{Model, Region}` (`:105-108`) — **sem eixo de modalidade nem de operação** |
| O preço é um documento **versionado e tamper-evident** | `pricing/table.go:210-226`, `:186` | `canonicalDigest` faz `json.Marshal` da `Entry` inteira. Uma alteração é **evento explícito** com diff e changelog (`pricing/change.go`) |
| Sem preço ⇒ **fail-closed** | `pricing/table.go:76`, `metering/cost/cost.go:180` | `ErrNoPrice`; `HasPrice` transforma a falha por chamada em **recusa de arranque** |
| **`provider_order` e `zdr` não existem** | — | Zero ocorrências em `packages/` e `deploy/` (só prosa no relatório do spike). O corpo que o gateway monta (`port/normalize.go`) **não tem campo `provider`** |
| O armazém de artefactos **recusa binário de raiz** | `cmd/aos/artifact_store.go:561` | `validarConteudo` exige **UTF-8 válido e sem NUL em todos os tipos**. Um PNG tem NUL no byte 1. Vocabulário: cinco tipos de texto (`:95-101`); tecto **1 MiB** por artefacto (`:88`) |
| …e o seu produtor é uma **tool mediada** | `artifact_store.go:454`, `entradaDoArtefacto.Content` é `string` | O conteúdo chega nos argumentos do modelo. **Não há caminho por onde o nó escreva bytes lá directamente** |
| O espaço de handles é **híbrido** e aceita binário | `specs/EPIC-07:2025`; `cmd/aos/handle_store.go:21` | «tipo opaco ao modelo, **armazém por baixo**»; vocabulário **aberto**; mas **nenhuma rota HTTP serve o conteúdo, por desenho** |
| …e o seu selo é um **hash sem chave** | `specs/EPIC-07:2025` | «um MAC com chave do nó **só é necessário quando um handle carregar conteúdo lido de fora, na R3**» |
| A captura do turno leva a resposta **inteira** | `kernel/agent-runtime/capture.go:49-53`; `replay/nondeterminism_capture.go:63` | `TurnCapture.Response` é um `ModelResponse` completo. O `replay.captured` é **o único evento de um turno que leva conteúdo** |
| O transporte tem **1 MiB por mensagem** | `kernel/agent-runtime/provider_state.go:48-58` | É a medição que fixou `MaxProviderStateBytes = 96 KiB`: três passagens por base64 até ao payload, ~2,37× |
| O SSE da trajectória **não tem tecto por evento** | `cmd/aos/trajectory.go:485` | `json.Marshal` do evento inteiro numa linha, servido com `read:trajectory` |
| «Os bytes nunca entram no tail» já é regra | `kernel/agent-runtime/media.go:17-19` (AOS-546); `layout.go:1052` (AOS-514) | Aplicada duas vezes e provada: `media_sha256` e `state_digest`. O tail leva a **referência nos rótulos** |
| A instrução de uma tool de modelo está presa **só por um teste** | `cmd/aos/aos530_ocr_instrucao_test.go:52-60` | O teste **parseia o código-fonte do banco** e compara digests. O registo de rota assinado **não tem membro de instrução** (`policy/routeentry/entry.go:70-93`) |
| O registo «assinado» de tools é **auto-assinado pelo nó** | `cmd/aos/modelcatalog.go:127-134` | Chave ed25519 **efémera, gerada no arranque**. Dá integridade dentro do processo, **não proveniência do dono** |
| **O guard-test AOS-069 recusa por omissão** | `cmd/aos/aos069_web_post_contexto_limpo_test.go:100-111` | Sobre o manifesto REAL de produção: nenhuma tool com `cap:http.post`, e **nenhuma tool com `egress != "none"`** |
| …com **uma excepção, por NOME literal** | `aos069_…_test.go:90-95` | `(name == "ocr_read" \|\| name == "video_read") && modelo.alias != "" && capability == "cap:fs.read" && resource_value == "doc://{doc_id}"`. Os literais estão lá «para que mudar a forma da tool não alargue a excepção em silêncio» (`:88-89`) |
| O que só as duas tools podem ter | `cmd/aos/ocr_tool.go:236-237` | `validarLigacaoModelo` recusa o arranque se outra tool declarar o bloco `modelo`: «o efeito e codigo do no, nao configuracao» |
| O molde do ADR-042 já existe | `cmd/aos/tool_de_modelo.go` (302 linhas) | `ligacaoDeModelo`: arranque fail-closed, chamada única sem retry nem failover, recusa paga, saldo. **É a base da `model_invoke`** |
| O ciclo de custo de uma tool-com-modelo já existe | `cmd/aos/ocr_custo.go:155,187,213,243` | reserva do **pior caso** antes do efeito → chamada → saldo pelo medido → evento durável `budget.toolcall.model_cost`, idempotente por (`run_id`, `step_id`) |
| A barreira de capacidade por rota já existe | `route_image.go:53-69`, `route_video.go:52-68`, chamadas em `gateway.go:374,379` | Corre **antes** da credencial, do selo, do adaptador e do custo. É o precedente exacto para a saída |
| O proxy encaminha a imagem, medido | `docs/reports/spike-litellm-imagem-video-2026-10-25.md:90-120` | Nas duas rotas e nos dois sentidos, com o **mesmo sha256**; em `chat/completions` a imagem de saída volta em `message.images[]`; `provider`/`modalities`/`plugins`/`transforms` chegam ao fornecedor |

**Quatro afirmações que me foram entregues e confirmei verdadeiras:** `model_invoke` não existe; a
parte não-texto na resposta é recusada; o preço só tem as quatro tarifas de token; `provider_order`
e `zdr` têm zero ocorrências.

**Três que estavam incompletas, e a terceira mudou o desenho:**
1. As tools em Go não são duas, mas duas *mais o molde*, mais `ocr_custo.go`, `erro_de_tool.go`,
   `catalogo_de_tools.go` e os dois `ocr_abrir_*`.
2. O obstáculo central à «configuração» não é o bloco `modelo` do registo — é o **guard-test
   AOS-069**, que ninguém tinha nomeado (§3.7).
3. «Partes de saída» não é uma mudança no caminho do turno, **se a geração for uma tool** — e é
   essa diferença que decide o tamanho da fase (§3.3).

---

## 3. As decisões

### 3.1 Uma porta ou duas: entra a `generate`, o `job` fica para a R6

O ADR-043 §2.2 escreve as duas de uma vez: «A porta ganha, além do `chat`, as operações `generate`
(síncrona) e `job` (assíncrona, para o vídeo), com um adaptador por classe de API»
(`ADR-043:92-93`). A §7 marca-as em fases diferentes: a R3 leva «partes de saída», a **R6** leva
«Vídeo gerado (API assíncrona)» (`ADR-043:290`). **A §7 ganha**, por quatro razões medidas:

1. **O elo que falta é do vídeo, não da imagem.** O spike mediu que o proxy encaminha o ciclo
   assíncrono completo pela rota `openai/…`, mas deixou escrito: «**que o que ele encaminha é
   aceite pela OpenRouter não está** [medido], e exige o fornecedor real e custo»
   (`spike:167-172`). A R6 tem esse elo na pré-condição (`ADR-043:290`); a imagem não tem elo
   nenhum por medir no encaminhamento.
2. **Um job paga-se mesmo que o run falhe** — resíduo já declarado (`ADR-043:257`). É um ciclo de
   vida novo, com dono, tecto e varrimento para jobs que ninguém vai buscar: a mesma família de
   problema que a via de entrada rejeitou na sua §3.4 («o problema das declarações órfãs do
   AOS-556, desta vez por desenho»).
3. **A sondagem é uma pergunta de egress por responder.** O spike: «um adaptador que sonde jobs tem
   de provar que a sondagem sai para a `api_base` da rota e não para a omissão do fornecedor»
   (`spike:179-182`). Hoje o nó tem **uma** allowlist derivada do endpoint
   (`AOS_MODEL_EGRESS_HOSTS`); um polling que escape dela é um destino novo.
4. **O proxy responde de forma instável a um download falhado** — 200 com corpo vazio em duas
   corridas e 500 na terceira (`spike:183-190`), com a nota explícita «não se conclua daqui um
   comportamento determinista». Construir um consumidor sobre isso é construir sobre o que a
   medição diz para não construir.

**Decisão: a R3 leva uma operação nova, a `generate` síncrona. O `job` não é desenhado aqui, nem
«preparado» aqui.** Preparar meia porta assíncrona seria entregar um leitor de uma forma que nada
escreve — a lição do AOS-551 — e de uma forma que a medição diz ser instável.

### 3.2 A `generate` vai pelo `chat/completions` que já existe — não nasce classe de wire nova

Esta é a decisão que mais trabalho poupa, e é medida.

O spike mediu que, num pedido de **chat**, nas duas formas de rota, a imagem gerada volta em
`choices[].message.images[]` e sobrevive à passagem com o mesmo sha256 (`spike:100-104`, `:83`). E
mediu que `modalities` chega ao fornecedor (`spike:114-120`) — que é como se pede ao modelo que
responda com imagem. Então a geração **não precisa** do endpoint dedicado: precisa de pôr
`modalities` no corpo e de saber ler uma parte de imagem na resposta.

| Alternativa | Porque não |
|---|---|
| Classe de wire nova (`openai-images-generations`) e caminho HTTP novo no adaptador | Duplica o que a governação da rota já cobre: `ExpectedAPIHost`, comparação do modelo servido, selo de atribuição, `usage`. O adaptador real é um só (`openai_http.go`) com dois caminhos; um terceiro é uma superfície de governação nova por onde o modelo servido e o custo teriam de voltar a passar |
| O endpoint dedicado pela rota `openrouter/…` | Ali o proxy **traduz** para `chat/completions` e converte de volta (`spike:92-98`). Pedir pelo endpoint dedicado é pedir-lhe uma tradução que podemos não lhe pedir, e ganhar uma forma de resposta (`data[].b64_json`) que não é a que o resto do sistema lê |
| Um adaptador directo à OpenRouter | **Rejeitada por medição** a 2026-10-25 (`ADR-043:216-231`): destino de egress novo que o guard-test AOS-069 recusa |

**Decisão: a `generate` é uma operação da porta que viaja na classe de wire que já existe.** O que
muda no corpo é um campo (`modalities`); o que muda na resposta é a leitura de `message.images[]`.
A operação nova serve para o selo, a métrica e o preço distinguirem uma geração de um turno — não
para abrir um caminho HTTP.

**`NÃO VERIFICADO`:** o spike mediu `message.images[]` **com o fornecedor falso**. Que a OpenRouter
real devolva nessa forma, com o modelo que o dono escolher, mede-se na peça AOS-570 (§5).

### 3.3 Partes de saída: a geração é uma TOOL, e por isso não atravessa o caminho do turno

Esta é a decisão que mais reduz o risco da fase, e só aparece quando se segue cada consumidor até
ao fim — que é o que esta secção faz.

**O facto que decide tudo:** no molde do ADR-042, a chamada de uma tool de modelo **não é um turno
do run**. A `ocr_read` chama `Gateway.Chat` por sua conta (`tool_de_modelo.go:233-243`); o que
entra no tail, na captura e no replay do run é **o resultado da tool** — um JSON de forma fixa
(`resultadoOCR`) —, nunca a resposta do modelo de visão. O mesmo vale para uma geração.

Seguindo cada consumidor da saída de um turno até ao fim, com a geração desenhada como tool:

| Consumidor | Onde | Está no caminho? | Porquê |
|---|---|---|---|
| Descodificador da porta | `port/port.go:224` | **SIM — é o único que muda** | É aqui que `ErrContentPartNotText` recusa a parte. É a mudança da fase |
| `translateResponse` | `model-gateway/runtime_adapter.go:480` | **SIM**, mas só para o caminho da tool | Hoje faz `out.Text = choice.Message.Content` e deitaria a parte fora em silêncio se a porta a aceitasse |
| Tail do run | `kernel/agent-runtime/prompt.go:511`, `layout.go:1108` | **NÃO** | Recebe o `tool_result` (JSON com a referência), como já recebe o da `ocr_read`. **Nenhum layout novo** |
| Captura (`replay.captured`) | `replay/nondeterminism_capture.go:63` | **NÃO** | Captura a resposta dos **turnos** do run. A chamada da tool entra como `CapturedToolResult`, cujo corpo é o JSON |
| Replay / goldens | `replay/engine.go:662`, `harness/fixtures.go:75` | **NÃO** | O replay devolve o resultado registado da tool e **não re-executa o efeito** — é o que o `TestAOS530_No_ReplaySemNovaChamada` já prova |
| SSE `/runs/{id}/trajectory` | `cmd/aos/trajectory.go:485` | **NÃO** | Não vê bytes, porque nenhum evento os leva |
| Âncora da saída (`OutputSource`) | `kernel/agent-runtime/output_source.go:92-110` | **NÃO** | Ancora o digest do resultado da **tool**, que é exactamente o que queremos ancorar |
| Transporte (NATS, 1 MiB/mensagem) | `provider_state.go:48-58` | **NÃO** | Nenhum evento cresce |

**Então a regra é uma só, e é dura: os bytes gerados nunca entram num evento, num tail, numa
captura nem no SSE.** Saem do adaptador, vão para o armazém, e o que sobe é
`{media_type, bytes, sha256}`.

**A medição que torna esta regra obrigatória, e não uma preferência.** Se os bytes entrassem na
captura, três coisas aconteciam, todas medidas:
- rebentavam o **limite de 1 MiB por mensagem** do transporte — é exactamente o cálculo que fixou
  `MaxProviderStateBytes = 96 KiB`, com ~2,37× de inflação por três passagens de base64
  (`provider_state.go:48-58`);
- apareciam no **SSE da trajectória sem tecto por evento** (`trajectory.go:485` faz `json.Marshal`
  do evento inteiro), isto é, **uma superfície de leitura nova para binário servida com
  `read:trajectory` em vez de `read:artifact`** — a capability errada;
- se entrassem no tail, `neutralizarDelimitadores` (`prompt.go:910`) corrompia-os byte a byte.

E o molde correcto já foi aplicado e provado **duas vezes**: `state_digest` (AOS-514) e
`media_sha256` (AOS-546, com a regra escrita em maiúsculas: «OS BYTES NUNCA ENTRAM NO TAIL»,
`media.go:17-19`).

**Três regras do canon que a fase implementa, as três no descodificador da porta:**

- **(a) O perfil declara, e o que não declara é recusado.** É a emenda ao ADR-036 §2.8
  (`ADR-043:276`) e o guard-test que a §8 exige: «uma parte não declarada é recusada»
  (`ADR-043:300`). O precedente de código é exacto: `recusarImagemSemCapacidade` corre antes da
  credencial, do selo e do custo (`gateway.go:374`).
- **(b) Tipos exactos, não classes.** Na entrada a declaração é por classe (`imagem`, `video`), e o
  AOS-546 deixou escrito o que ficava para aqui: «Declarar tipos exactos fica para as partes de
  saída (fase R3)» (`specs/EPIC-06:5400`). Na saída a classe não chega — o armazém tem de saber o
  que grava e com que extensão o entrega.
- **(c) Recusar, nunca descartar.** A razão escrita em `port.go:142-143` continua a valer ao
  contrário: aceitar uma parte declarada e deitar fora uma não declarada entregaria como completa
  uma resposta a que falta conteúdo.

**O que fica deliberadamente de fora: o modelo do RUN a produzir media.** O ADR-043 §2.2 fala da
porta em geral, e um dia o titular responderá com imagem. **Nesta fase não.** A razão é a tabela
acima lida ao contrário: a partir do momento em que é um *turno* a produzir a parte, todos os
consumidores que estão «NÃO» passam a «SIM» — layout novo do tail (a 1.7.0 só tem segmento `media`
de **entrada**, construído de um resultado da `media_view`, `media.go:187`), campos novos na
captura, goldens novas, tecto por evento no SSE, e o limite do transporte. É uma fase inteira por
si, e separá-la é o mesmo que a R1a fez: a tool (AOS-530) veio antes do titular (AOS-546).

### 3.4 Onde vive o conteúdo gerado: o armazém, com o handle por cima

O ADR é explícito: «**O resultado vai para o armazém** e o modelo recebe a referência»
(`ADR-043:139`). O código acrescenta a emenda que o dono decidiu a 2026-10-23: os factos que o
modelo vê são «de NÍVEL-ESQUEMA, E NUNCA UM RESUMO» (`kernel/agent-runtime/taint_plane.go:75-83`),
porque «um resumo — mesmo gerado pelo nó — seria conteúdo untrusted reescrito em prosa».

**As duas fontes parecem discordar — «armazém» e «handle» — e não discordam.** O handle é
**híbrido**: «tipo opaco ao modelo, **armazém por baixo**» (`specs/EPIC-07:2025`). Os bytes vivem no
armazém; o que o modelo vê é um handle com factos de nível-esquema.

**E a razão que a via de entrada deu para não misturar direcções NÃO se aplica ao contrário.** Ela
escreveu: «A entrada não vai ao armazém de artefactos. Ele é a SAÍDA — tem rota de download, tipos
de texto e validação de conteúdo desenhadas para o que o run produz. Misturar as duas direcções no
mesmo sítio juntaria uma superfície de leitura a conteúdo que vem de fora»
(`desenho-via-de-entrada-2026-10-25.md:131-134`; repetido em `aos561_entrada_de_media.go:26-28`). O
argumento é **sobre a rota de download**: ingerir não deve criar superfície de leitura. Uma imagem
**gerada** é precisamente o que o dono quer descarregar — ali a superfície de leitura é a
funcionalidade. **Medido e aplicado ao contrário, o argumento inverte-se e confirma o armazém.** E
o simétrico fecha-o: o espaço de handles **não tem rota de leitura, por desenho**, pelo que não
serve para a saída.

**O que o armazém tem de ganhar, e o que isso custa:**

| O que muda | Hoje | Porquê |
|---|---|---|
| Tipos aceites | 5, todos de texto (`artifact_store.go:95-101`) | `image/png` e `image/jpeg` entram, **pelos primeiros bytes** e nunca pelo que o produtor declara — a mesma `port.ImagePart.Validate` que a `ocr_read` já usa |
| Validação de conteúdo | `validarConteudo` exige **UTF-8 e sem NUL em todos os tipos** (`:561`); JSON que compila; SVG sem script | Para raster **não há equivalente semântico**, e a via de entrada já o tinha dito (`:218-221`). O que substitui é magic bytes + tecto + a entrega já endurecida (`attachment`, `nosniff`, `CSP: sandbox`). **É menos do que para o SVG, e tem de ser dito** |
| Tecto por artefacto | **1 MiB** (`:88`) | Uma imagem gerada não cabe. Sobe para o que o tecto de media já impõe a montante (`MaxImageBytes`, 4 MiB) — **nunca mais**, pela regra da via de entrada: um tecto de saída maior do que o da porta só aceita o que vai ser recusado depois |
| Tecto por run | 16 MiB / 16 artefactos | Revê-se com o tecto novo. **O número é do ticket** |
| **Quem escreve** | só a tool mediada `artifact_write`, com `content` em **string** nos argumentos do modelo (`:454`) | Uma imagem gerada **não tem quem a escreva**: não vem dos argumentos do modelo. A geração escreve no armazém **directamente**, como o `handle_store` já faz, com o mesmo `artifact.created` e o mesmo taint `untrusted` |

### 3.5 O preço com unidades: o difícil não é a tarifa, é o documento

O ADR-043 §2.8: «A tabela de preços passa a conhecer várias unidades: tokens (as quatro tarifas
actuais), imagem, segundo de vídeo, pedido. **A reserva é o pior caso declarado.** O saldo faz-se
pela tabela, e o custo reportado pelo fornecedor fica gravado como observação. **Um par sem preço
continua a recusar o arranque**» (`:185-188`).

A tarifa é trivial. Três coisas medidas não são:

**(a) Acrescentar um campo a `Rate` muda o digest de TODAS as entradas.** `canonicalDigest` faz
`json.Marshal` da `Entry` inteira (`pricing/table.go:219-222`). Um campo `int64` sem `omitempty`
serializa `0` em todas as linhas, muda o digest da tabela que produção tem pinada e dispara o
evento de alteração de preço — **sem nenhum preço ter mudado**. Um burn-down e uma reconciliação
que se apoiam no digest passam a ver uma alteração fantasma.

**(b) `omitempty` sozinho é a correcção errada**, e é a armadilha fina. Com `int64` e `omitempty` o
valor `0` desaparece do JSON — e `0` é um preço legítimo («grátis»), enquanto a ausência significa
«esta unidade não tem preço», que o §2.8 manda tratar como recusa de arranque. Colapsar os dois
reintroduz exactamente o «0 silencioso» que o pacote existe para impedir: «um 0 silencioso
falsificaria o burn-down (uma chamada cara pareceria grátis)» (`pricing/table.go:40-43`). **As
tarifas novas são ponteiros** (`*int64`): ausente ≠ zero, e ausente é fail-closed.

**(c) A reserva deixa de ser uma fórmula de tokens.** Hoje `cost.Calculator.Cost` recebe
`TokenCounts` (`metering/cost/cost.go:255`). O pior caso de uma geração é «n imagens × tarifa por
imagem (+ os tokens do prompt)», e quem reserva é o molde (`ocr_custo.go:187`). Muda em cadeia:
`cost.TokenCounts` → `cost.Calculator` → a reserva do molde → a estimativa de admissão
(`integration/model_admission.go`) → o burn-down durável.

**Resíduos que a fase herda e não resolve:** «os `overrides` de preço da OpenRouter tornam a reserva
uma estimativa. O tecto é o pior caso declarado» (`ADR-043:258-259`); e «**por confirmar** que o
`usage` que o proxy devolve para um pedido com imagem conta os tokens da imagem»
(`ADR-042:387-388`).

### 3.6 Fornecedor fixado e selado: o que fecha, o que não fecha, e o que nem está medido

O spike mediu que o proxy deixa declarar `provider: {"order":["Google"],"data_collection":"deny"}`
e que as chaves **chegam ao fornecedor** (`spike:114-120`). É por aí que se expressa a ordem de
fornecedores e o ZDR. No nó isso não existe: zero ocorrências de `provider_order` e `zdr`, e o
corpo que o gateway monta não tem campo `provider`.

**O que esta fase fecha:** a ordem de fornecedores e a retenção passam a ser declaradas **no perfil
assinado da rota**, viajam no corpo do pedido, e **o fornecedor efectivo fica selado** no evento e
no selo de atribuição (`gateway.go:725-750`, que hoje leva modelo, região e `KeyID` mas não
fornecedor).

**O que NÃO fecha, declarado desde o ADR:** «a comparação da rota não a detecta (`route.go:18-30`),
e o snapshot do catálogo não versiona pesos. Fixar o fornecedor e a quantização por pedido **reduz
o risco mas não o fecha**» (`ADR-043:252-255`). Dizer «o fornecedor está fixado» sem esta frase ao
lado seria prometer o que o código não impõe.

**E uma terceira coisa, que acrescento porque a medição a deixou aberta e ninguém a herdou:** o
spike mediu as quatro chaves a chegarem **com `drop_params: true`**, que é a configuração do banco.
**Produção corre `drop_params: false`**, e aí ficou medido apenas que *nada as recusa* — que elas
**chegam** com `false` «não está medido directamente — é o resíduo honesto deste ponto»
(`spike:141-147`). Ou seja: **o mecanismo pelo qual esta fase fixa o fornecedor não está medido na
configuração que produção corre.** Fica como critério de aceitação da prova em produção, não como
suposição.

### 3.7 `ocr_read`/`video_read` como configuração: o efeito fica em Go, e o que se perde é nomeável

O ADR-042 §2.1 dizia «o efeito é código do nó, não configuração». O ADR-043 emenda-o: «o efeito
continua a ser código (uma implementação), e **só o uso passa a configuração**» (`ADR-043:273`). O
inventário das duas tools diz onde fica a fronteira: das ~50 validações da `ocr_read` e ~28 da
`video_read`, a esmagadora maioria **não é expressável em configuração declarativa**.

| Família | Exemplos | Para onde vai |
|---|---|---|
| **Comum às duas, já no molde** | arranque fail-closed; credencial NHI antes de qualquer leitura; leitura confinada com `os.Root`; re-verificação no descritor; `os.SameFile` contra troca; reserva→chamada→saldo; vocabulário de erro fechado; chamada única sem retry nem failover | **Fica no molde** (`tool_de_modelo.go`, `ocr_custo.go`). Já lá está |
| **Específica da classe de media** | PNG/JPEG pelos primeiros bytes com fallback ordenado; parsing de caixas MP4, `mvhd`, duração, guardas de overflow; reserva função da duração | **Fica em Go, num *handler* por classe.** Um parser de contentor MP4 não é uma linha de JSON |
| **Genuinamente declarativa** | alias, `max_tokens`, prazo, tectos de bytes, nome do schema do resultado, sensibilidade, classe de media, a instrução | **Passa a configuração** |

**O que se perde, por ordem de gravidade:**

1. **A instrução perde o único trinco que tem.** Hoje a instrução da `ocr_read` está presa por
   `TestAOS530_OCR_AInstrucaoEADoBanco`, que **parseia o código-fonte do banco de ensaio** e compara
   digests (`aos530_ocr_instrucao_test.go:52-60`). É um trinco de compilação. O registo de rota
   assinado **não tem membro de instrução** — li a lista de membros (`routeentry/entry.go:70-93`).
   Mover a instrução para `tools.json` destrói o trinco e **não põe nada no lugar**: o «registo
   assinado de tools» do nó é auto-assinado com uma chave efémera gerada no arranque
   (`modelcatalog.go:127-134`), que dá integridade dentro do processo, não proveniência do dono.
   **Correcção obrigatória: o registo de rota assinado ganha o digest da instrução, e o nó recusa
   arrancar se a instrução configurada não bater com ele.** Sem isto, a configuração é uma
   regressão de segurança disfarçada de generalização.
2. **As duas fórmulas de digest da instrução são DIFERENTES.** A `ocr_read` usa sha256 sobre JSON
   canónico `{pedido, sistema}` (`ocr_tool.go:122-129`); a `video_read` usa sha256 de
   `sistema + "\n" + pedido` (`video_tool.go:112-115`). Não é desleixo: cada uma bate com o
   relatório do banco que o registo assinado cita. **Unificar as fórmulas invalida os dois
   relatórios qualificados e obriga a re-qualificar com custo real.** A fase mantém as duas, por
   classe, e regista a dívida.
3. **O guard-test AOS-069 excepciona por NOME literal.** Recusa qualquer tool de produção com
   `egress != "none"`, excepto `name == "ocr_read" || name == "video_read"` **e** `capability ==
   "cap:fs.read"` **e** `resource_value == "doc://{doc_id}"` (`aos069_…_test.go:90-95`). Uma
   `model_invoke` configurável quebra-o por construção — e o ADR-044 obriga a quebrá-lo mais: a
   tool tem de ter «um argumento de CONTEÚDO que não seja o slot do recurso» (`ADR-044:452-463`),
   isto é, `resource_value == "doc://{doc_id}"` **deixa de valer**. **O guard-test passa de uma
   lista de nomes para uma forma declarada e limitada**, e é a parte mais delicada da fase: é o
   teste que impede que uma tool de egress entre em produção por acidente; alargá-lo de menos trava
   a fase, de mais abre a porta que ele existe para fechar. O próprio teste avisa: os literais
   estão lá «para que mudar a forma da tool não alargue a excepção em silêncio».

### 3.8 O que NÃO se faz, e porquê

- **Não nasce caminho HTTP novo no adaptador** (§3.2) — a geração vai pelo chat.
- **Não nasce armazém novo** — o armazém de artefactos é ele (§3.4).
- **Não nasce destino de egress** — a alternativa (E) caiu por medição e o AOS-069 continua a
  recusar destinos novos.
- **Não nasce layout novo do tail nem campo novo na captura** (§3.3) — e é por isso que a fase cabe.
- **Não se liga a R2** — está desligada à espera da cerimónia da chave do dono.
- **Não se desenha o `job`** nem se «prepara» meia porta assíncrona (§3.1).
- **Não se unificam as fórmulas de digest da instrução** (§3.7, ponto 2).

---

## 4. As consequências que ninguém pediu e têm de ser ditas

**(a) `tools.json` ganha autoridade, e a sua assinatura não é do dono.** Hoje a instrução, os
tectos, o `max_tokens` e o prazo são constantes Go: mudam por revisão de código e por release.
Depois desta fase são linhas de um ficheiro no servidor, auto-assinado pelo nó com uma chave
efémera. O que os prende volta a ser só o que o dono assina — o alias na allowlist, o perfil da
rota, o relatório do banco. **A instrução tem de entrar nessa lista** (§3.7, ponto 1); os tectos e
o `max_tokens` não entram, e isso é uma perda de controlo que o dono aceita ou recusa.

**(b) O conteúdo gerado não é moderado, e agora há conteúdo gerado.** O resíduo já estava escrito
(`ADR-043:256`), mas era abstracto: o sistema não gerava nada. Passa a gerar, e o que sai do
fornecedor vai para o armazém do run e para a rota de download do dono. O que o contém é o que já
existe: o artefacto é sempre untrusted, a entrega leva `attachment`/`nosniff`/`CSP: sandbox`, e o
crypto-shred alcança-o. O que **não** existe é moderação.

**(c) O armazém passa a guardar binário que não sabe validar.** Para JSON e SVG valida
semanticamente; para raster valida magic bytes e tamanho, e mais nada (§3.4). Um PNG malformado que
o fornecedor devolva fica gravado e é servido ao dono. A defesa é a entrega endurecida, não a
inspecção.

**(d) Se um dia os bytes entrarem num evento, abrem uma superfície de leitura com a capability
errada.** O SSE da trajectória serializa o evento inteiro sem tecto por evento
(`trajectory.go:485`) e é servido com `read:trajectory` — enquanto o armazém tem
`read:artifact`. É mais um motivo, além do limite de 1 MiB do transporte, para a regra dura da
§3.3. **Não é um defeito desta fase: é a razão pela qual ela é desenhada assim.**

**(e) O selo do handle é um hash sem chave, e esta fase é o gatilho declarado.** «Um MAC com chave
do nó só é necessário quando um handle carregar conteúdo lido de fora, **na R3**»
(`specs/EPIC-07:2025`). Uma imagem gerada é conteúdo lido de fora. **`NÃO VERIFICADO`: se o gatilho
já disparou antes desta fase** — num layout 1.8.0, o `tool_result` da `ocr_read` já é um handle que
carrega texto vindo de fora, pelo que é plausível que a dívida esteja viva desde a R2. Medi que a
dívida existe e que nomeia a R3; **não medi se a R2 já a tornou exigível**. Se a medição disser que
já estava viva, isso torna a peça mais urgente, não menos.

**(f) Uma geração que o provider respondeu e a tool recusou custou dinheiro.** Herdado do ADR-042
(`:269-270`), e uma geração custa mais por chamada do que uma transcrição. O mesmo para a queda
entre a resposta e o ledger: «a chamada paga-se duas vezes» (`ADR-042:392-394`).

**(g) Fora de âmbito, e NÃO é um defeito: a assimetria da recusa paga é deliberada.** O
`PaidRejectionError` só se constrói quando `req.OriginTool != ""` (`gateway.go:451`), e o código
diz porquê, em comentário: «a recusa de uma resposta paga leva o custo a quem fez o pedido — só a
uma tool; um turno vê o erro de sempre» (`gateway.go:452-454`, AOS-530). Levantei-o como suspeita
e **a medição desmentiu-a**; fica aqui porque a R3 acrescenta chamadas caras, e vale saber que o
caminho é esse por desenho. **`NÃO VERIFICADO`, e é a parte que sobra:** se o custo de um turno
recusado chega ao burn-down durável do nó, que se alimenta do `turn.recorded`. O `recordCost` do
gateway corre antes da governação da rota (`gateway.go:434`), logo o número existe; se o turno
falhado grava `turn.recorded`, não medi. Não é desta fase.

---

## 5. O elo que falta medir, e é do dono

O spike deixou escrito, como fora de âmbito: «que modelos geram imagem ou vídeo pela OpenRouter,
**com que preço por imagem e com que limites. É o que a R3 precisa para o preço por imagem, e
mede-se no banco com o fornecedor real**» (`spike:191-193`).

Não é um detalhe de implementação: é a **entrada** da §3.5. Sem a tarifa por imagem de um modelo
concreto não há linha na tabela de preços, e sem linha o nó recusa arrancar — que é o comportamento
correcto, e também o que bloqueia a fase.

Junta-se-lhe o resíduo da §3.2: que a OpenRouter real devolva a imagem em `message.images[]` com o
modelo escolhido está **`NÃO VERIFICADO`** — o spike mediu-o com fornecedor falso.

**Não resolvi nenhum dos dois, e não gastei dinheiro nenhum.** Os dois medem-se na mesma corrida do
banco com fornecedor real (peça AOS-570), que custa e é decisão do dono.

---

## 6. As peças, por ticket

A cada peça fiz a pergunta que o AOS-551 nos ensinou — *esta peça é um leitor ou um validador de
uma forma que nada escreve?* — e a resposta está na última coluna. Onde era «sim», a peça foi
fundida com quem escreve.

| # | Ticket | Conteúdo | Epic | Depende de | Prova-se sozinha com | Tem produtor? |
|---|---|---|---|---|---|---|
| **1** | **AOS-565** | **O fornecedor fixado e selado, por pedido.** O perfil assinado declara a ordem de fornecedores e a retenção; o gateway escreve-as no corpo; o **fornecedor efectivo** entra no selo de atribuição e no evento. Aplica-se a **todas** as chamadas de hoje, turnos incluídos | EPIC-06 | — | Um pedido cujo corpo leva as chaves e um selo que nomeia o fornecedor efectivo | **Sim** — o perfil assinado escreve, o gateway lê, o selo regista |
| **2** | **AOS-566** | **A `model_invoke`: o efeito é uma implementação, o uso é configuração.** `ocr_read` e `video_read` passam a duas configurações com comportamento **idêntico**; *handlers* por classe de media ficam em Go; a instrução passa a config **e ganha o trinco que perde** (membro de digest no registo de rota assinado, conferido no arranque); o **guard-test AOS-069 passa de nomes literais a forma declarada e limitada**. Escreve a emenda ao ADR-042 | EPIC-07 | — | **As 88 funções de teste que já cobrem as duas tools**, a passar sem alteração de asserção | **Sim** — o oráculo são os testes existentes |
| **3** | **AOS-567** | **O preço com unidades, e a recusa fail-closed sem preço.** `Rate` ganha tarifas por imagem, por segundo e por pedido, **como ponteiros** (ausente ≠ zero); o digest das entradas existentes **não muda**; `cost.Calculator` conta unidades; um perfil que declare classe de **saída** sem a tarifa dessa unidade **recusa o arranque** | EPIC-06 | — | Uma tabela com tarifa por imagem que carrega, digere e diffa; e um arranque recusado por falta de tarifa | **Sim** — escrevem o documento de preços do operador e o perfil assinado |
| **4** | **AOS-568** | **A geração síncrona.** Operação `generate` na classe de wire que já existe; o perfil declara os **tipos exactos** da saída; uma parte não declarada é **recusada, nunca descartada**; os bytes **nunca** entram em evento, tail, captura ou SSE; o armazém aceita `image/png`/`image/jpeg` pelos primeiros bytes, com tecto próprio e **um produtor que não é uma tool mediada**; o `artifact.created` leva **modelo, fornecedor efectivo e custo**; o modelo recebe um handle com factos de nível-esquema. Escreve a emenda ao ADR-036 §2.8 | EPIC-06 + EPIC-07 | **2, 3** (e 1 para o fornecedor no evento) | Um run que gera uma imagem, a grava no armazém e a entrega, com replay sem nova chamada | **Sim** — é a peça que produz |
| **5** | **AOS-569** | **O selo do handle ganha MAC com chave do nó.** O resíduo declarado do AOS-550, cujo gatilho escrito é esta fase | EPIC-07 | — (tem de aterrar **antes** de 4 ir a produção) | Um selo forjado que o índice aceitava e o MAC recusa | **Sim** — o espaço de handles já sela hoje |
| **6** | **AOS-570** | **O banco mede a geração, e o registo de perfil com saída exige essa medida.** Uma experiência que mede um modelo a gerar imagem, com veredicto e relatório selado; **mede o preço por imagem e os limites reais** (o que o spike deixou por medir); um registo de perfil que declare classe de saída é recusado sem citar esse relatório | EPIC-08 | **4** | A qualificação real, assinada pelo dono | **Sim** — mas **custa dinheiro**: é a §7 |

**Ordem recomendada.** 1, 3 e 5 são independentes entre si e de tudo o resto — podem correr em
paralelo, cada uma no seu worktree. 2 é independente mas é a maior e a mais delicada (o
guard-test), pelo que começa cedo. 4 só depois de 2 e 3. 6 por fim.

**Uma ressalva sobre o paralelo, que não é teórica.** 2 e 5 tocam no **mesmo módulo Go**
(`packages/cmd/aos`), em ficheiros diferentes — a 2 em `ocr_tool.go`/`video_tool.go`/
`tool_de_modelo.go` e no guard-test, a 5 em `handle_store.go`. Ficheiros disjuntos não bastam:
duas tarefas no mesmo módulo quase nunca são independentes na prática, e o `bootstrap.go` é o
sítio onde as duas se encontram. Se correrem ao mesmo tempo, correm em worktrees separados e com
o registo de sessões a declarar os ficheiros — e a integração das duas é uma passagem própria,
não um merge cego.

**Porque é que o preço (3) não está dentro da geração (4).** Pensei fundi-los: uma geração que
reserva por tarifas de token é um tecto que mente. Mas 3 prova-se sozinha e é um **portão** — o nó
recusa arrancar sem tarifa —, escrito pelo perfil assinado e pelo documento de preços do operador,
que são produtores reais sem a 4. O que não se pode é **ligar** a 4 em produção sem a 3; isso é um
critério de aceitação da 4, não uma fusão.

**Porque é que o armazém binário NÃO é ticket próprio.** Seria exactamente o defeito do AOS-551: um
armazém que aceita um tipo que nada escreve. A `artifact_write` recebe `content` em string nos
argumentos do modelo; **nenhum caminho actual lá põe bytes de imagem**. Quem escreve é a geração —
logo o armazém binário vai dentro da 4, com o seu produtor.

**Três coisas que considerei e NÃO abri como ticket.** O catálogo qualificado e o router por
requisitos (é a R4, de que esta fase é pré-condição); o `job` assíncrono (§3.1); e o fecho do
**critério 8 do AOS-556** — a prova em produção da política de fluxo, que o ADR-044 diz só poder
ser feita com a `model_invoke` da R3 (`ADR-044:458-467`). O terceiro não abri **porque pertence ao
AOS-556, que já existe**; mas é consequência desta fase e tem de ser dito: quando a 2 e a 4
aterrarem, o AOS-556 passa a ser fechável.

---

## 7. O que é do dono

1. **A cerimónia da chave da R2** — ratificar o ADR-044 e assinar o bundle Cedar 1.2.0
   (`ADR-044:442-449`). **É a pré-condição dura da R3** (`ADR-043:287`). As seis peças constroem-se
   e provam-se em teste sem ela; **nenhuma vai a produção sem ela**.
2. **Autorizar a medição com fornecedor real** (§5, peça AOS-570) — a única com custo. Sem ela não
   há tarifa por imagem, logo não há linha na tabela, logo o nó recusa arrancar. É também onde se
   confirma que a OpenRouter devolve a imagem na forma que o spike mediu com o falso.
3. **Escolher o modelo de geração** e assinar o que ele exige: o alias na allowlist regional, o
   perfil de rota com a classe de saída, e o lote do banco. É o caminho do `ocr-visao`.
4. **Aceitar por escrito a consequência (a) da §4**: a instrução, os tectos e o `max_tokens` de uma
   tool de modelo passam de constantes revistas em código para linhas de um ficheiro no servidor. A
   instrução fica presa por assinatura (peça 2); os tectos e o `max_tokens` **não**.
5. **Aceitar a consequência (c)**: o armazém passa a guardar binário que não sabe validar
   semanticamente, e a rota de download serve-o.
6. **Decidir se o MAC do selo do handle (peça 5) entra nesta fase ou é adiantado** — a medição
   sugere que a dívida pode já estar viva desde a R2 (§4(e), `NÃO VERIFICADO`).
7. **A prova em produção** da fase: um run que recebe uma fotografia pela via de entrada e devolve
   uma imagem gerada.

---

## 8. Resíduos e o que fica fora

**Fica fora, por decisão deste desenho:**

- **O `job` assíncrono e o vídeo gerado** — é a R6, com um elo por medir, um ciclo de vida que se
  paga quando o run falha, uma pergunta de egress aberta e um proxy instável no download (§3.1).
- **O modelo do RUN a produzir media** — §3.3: é a fase que atravessa o tail, a captura, o replay,
  o SSE e o transporte, e merece o seu desenho, como o AOS-546 mereceu depois do AOS-530.
- **O catálogo qualificado e o router por requisitos** — é a R4, que depende desta fase.
- **O `code_exec` e o MCP** — é a R5, que depende da R2 e não desta.
- **O PDF** — continua a decisão do dono da §5 do desenho da via de entrada.
- **Moderação do conteúdo gerado** — resíduo declarado do ADR, não fechado aqui.
- **Unificar as fórmulas de digest da instrução** — invalidaria os dois relatórios qualificados
  (§3.7, ponto 2).
- **A recusa paga de um turno do executor** (§4(g)) — levantada como suspeita e **desmentida pela
  medição**: é deliberada (`gateway.go:452-454`). Fica o que não medi: se o custo de um turno
  recusado chega ao burn-down durável.

**Resíduos que a fase herda e não fecha:**

- **Troca de modelo por trás do mesmo nome**: fixar o fornecedor «reduz o risco mas não o fecha»
  (`ADR-043:252-255`).
- **Os `overrides` de preço tornam a reserva uma estimativa** (`ADR-043:258-259`).
- **Pagamento a dobrar** numa queda entre a resposta e o ledger (`ADR-042:392-394`).
- **O proxy não é assinado**; a comparação do modelo servido é a única defesa (`ADR-042:398-399`).
- **`external_read` assume que «não muda nada no mundo» vale para o fornecedor** (`ADR-044:420-423`).

**O que fica `NÃO VERIFICADO`, e é informação, não desleixo:**

1. **Que a OpenRouter real devolva a imagem em `message.images[]`** com o modelo escolhido — o
   spike mediu-o com fornecedor falso. Mede-se na peça 6.
2. **Que as chaves de fixação do fornecedor cheguem com `drop_params: false`**, que é o valor que
   produção corre. Medido só que nada as recusa (`spike:141-147`). Critério da prova em produção.
3. **Que o `usage` do proxy conte os tokens da imagem** (`ADR-042:387-388`) — por confirmar desde o
   ADR-042, e agora conta para o saldo de uma geração.
4. **Se o gatilho do MAC do selo do handle já disparou com a R2** (§4(e)).
5. **Os valores exactos dos tectos** do armazém binário e do `max_tokens` de uma geração. São do
   ticket, depois da medição da peça 6.

---

## 9. O que isto desbloqueia, dos exemplos do dono

| Exemplo do dono | Antes desta fase | Depois |
|---|---|---|
| Foto de um compartimento → projectar os móveis | só a metade da ENTRADA (AOS-561/563) | **fecha** |
| Enviar imagens e fazer OCR | fecha | — |
| Enviar PDF e fazer OCR | — | continua a decisão da §5 do desenho da via de entrada |
| Objectivo → harness até terminar, com artefactos | funciona, com artefactos de texto | **artefactos binários** |
| Artefactos que podem ser qualquer coisa | só os cinco tipos de texto | imagem entra; o resto continua decisão com custo próprio |

E, fora dos exemplos: a **R4** e a **R6** deixam de estar bloqueadas, e o **critério 8 do AOS-556**
passa a ser fechável.
