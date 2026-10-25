# Spike AOS-558 — o proxy LiteLLM fixado encaminha as APIs de imagem e de vídeo?

> Medição da pré-condição da **fase R3** do [ADR-043](../adr/ADR-043-harness-por-capacidades.md)
> (§7: «R2; spike do LiteLLM (§3 E)»). Corrida a **2026-10-25** no posto, com Docker, contra a
> imagem de produção do proxy fixada pelo digest e um fornecedor falso. **Sem nenhuma chamada
> real à OpenRouter:** sem custo e sem destino de egress novo.

| Campo | Valor |
|---|---|
| Ticket | AOS-558 (spike), EPIC-08 |
| Pergunta de origem | ADR-043 §3 E: a alternativa «um adaptador directo à OpenRouter, sem o LiteLLM» «decide-se no ticket do §2.2, pelo resultado de um spike que meça se o LiteLLM fixado encaminha as APIs de imagem e de vídeo» |
| Imagem medida | `ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63` |
| Versão, lida de dentro do contentor | **1.96.2** (`importlib.metadata.version("litellm")`) |
| Ferramenta | `packages/qa/banco-ensaio`, cenário `TestAOS558_ProxyReal_ImagemEVideoPeloProxy` |
| Precedente de método | AOS-534 (a parte `video_url` de **entrada** atravessa o proxy) |

## 1. A afirmação que se foi medir

A análise de base do ADR-043 escreve, na secção «o que falta medir ou desenhar»
(`analise-critica-harness-flexivel-2026-10-22.md`):

> **O LiteLLM é um obstáculo real.** Só fala chat e não declara o endpoint. Um adaptador directo
> à OpenRouter é um **destino de egress novo** (o guard-test AOS-069 recusa-o) e exige decisão.

Era **leitura de documentação, não medição** — tal como a §9 do ADR diz da documentação da
OpenRouter («lida a 2026-10-22, não medida»). Três fases dependiam dela: a R3, e a R4 e a R6 que
dependem da R3.

**A afirmação está errada nas duas metades, para a imagem, e certa só em parte para o vídeo.**

## 2. O que se mediu, e como

A medição tem dois níveis, e os dois são sobre o **artefacto fixado**, não sobre documentação.

**(a) Inventário de rotas**, lido da aplicação do proxy carregada dentro do contentor
(`litellm.proxy.proxy_server.app.routes`, 517 rotas). A imagem **declara**, entre outras:

```
/v1/chat/completions        POST
/v1/images/generations      POST        /images/generations         POST
/v1/images/edits            POST
/v1/videos                  POST, GET   /v1/videos/{id}             GET
/v1/videos/{id}/content     GET         /v1/videos/{id}/remix       POST
/v1/videos/edits            POST        /v1/videos/extensions       POST
/v1/responses               POST        /v1/audio/transcriptions    POST
```

**(b) Medição funcional**, com a imagem de produção do proxy num contentor entre um cliente e um
**fornecedor falso** (`FalsoDeEstado`, forma OpenRouter), pelas **duas formas de rota** que o
proxy tem para a OpenRouter — `openrouter/<autor>/<modelo>` (o adaptador próprio) e
`openai/<autor>/<modelo>` com a base da OpenRouter, que é a de produção.

**O `drop_params`, com precisão.** A configuração que o banco escreve tem **`drop_params: true`**
(`proxy.go`, a mesma de `deploy/node/dev-hardened/litellm/config.yaml:42`). **O proxy de produção
corre `false`** desde 2026-10-11, aplicado no servidor pelo AOS-517 e registado no
`acompanhamento-arquitectura-alvo-fronteira-modelo.md` («Aplicar `drop_params: false` no proxy do
servidor»), e a semente do repositório diz o mesmo (`deploy/server/litellm/config.yaml:88`). Os dois
valores falham de maneiras **opostas**: com `true` um parâmetro que o adaptador não suporte é
**descartado em silêncio**; com `false` passa a **erro visível**. Por isso a §3.2 mede os dois, e não
só o do banco.

Para o AOS-558 o falso passou a registar, de cada pedido, o **caminho** (vocabulário fechado,
`ClassificarCaminho`) e as **chaves do topo** do corpo (`chavesDoTopoDeGeracao`) — nomes, nunca
valores, nunca o caminho em bruto. E passou a saber responder em duas formas novas, para se poder
medir o **sentido de volta**: um `images` na mensagem de chat, na forma da OpenRouter (um PNG de
4×4 com sha256 conhecido), e a família do vídeo na forma da API de vídeo da OpenAI.

Cada caso dá duas medidas:

- **encaminhamento** — a que caminho o pedido chegou ao fornecedor, ou `recusado_pelo_proxy` se
  nenhum chegou;
- **saída** — se os bytes que o falso emitiu voltaram ao cliente com o mesmo sha256.

## 3. O resultado

Modelo da rota: `google/gemini-2.5-flash-image`. PNG do falso:
`sha256:ab957d406e6adfd30a414d30cb6014e21e8bc0cd2ae0ae07e9a5abb293259eb0`.

| Caso | Rota `openrouter/…` | Rota `openai/…` (a de produção) |
|---|---|---|
| **controlo**, chat só com texto | 200, `chat_completions` | 200, `chat_completions` |
| `POST /v1/images/generations` | 200, chegou como **`chat_completions`**, com `image_config`+`n`; **saída intacta, em `data[].b64_json`** | 200, chegou como **`images_generations`**; **saída intacta, em `data[].b64_json`** |
| `POST /v1/chat/completions` com `modalities`, `provider`, `plugins`, `transforms` | 200, `chat_completions`; **as quatro chaves chegaram ao fornecedor**; saída intacta, em `message.images[]` | 200, idem |
| `POST /v1/videos` | **500, nada chegou ao fornecedor** | 200, chegou a **`videos`** (`model`, `prompt`, `seconds`) |
| `GET /v1/videos/<id>` | **500, nada chegou** | 200, chegou a **`videos_id`** |
| `GET /v1/videos/<id>/content` | **nada chegou ao fornecedor**; a resposta ao cliente **não é estável** — 200 vazio em duas corridas, 500 ao cabo de 10 s na terceira | 200, chegou a **`videos_id_content`**; **bytes intactos** |

Relatório completo do cenário: `AOS_BANCO_ENDPOINTS_REPORT` na saída do teste.

### 3.1 A imagem passa, nas duas rotas e nos dois sentidos

O endpoint dedicado de geração de imagem **é servido pelas duas rotas**. A diferença é como:

- o adaptador **`openrouter`** não fala um endpoint de imagem do fornecedor — **traduz** a geração
  de imagem para `chat/completions`, com o `prompt` como mensagem `user` e o `size`/`quality`
  convertidos em `image_config.aspect_ratio`/`image_size`, e **extrai de volta** o
  `choices[].message.images[].image_url.url` para o `data[].b64_json` da resposta de imagem
  (`llms/openrouter/image_generation/transformation.py` da própria imagem: *«OpenRouter uses chat
  completion endpoints for image generation»*);
- o adaptador **`openai`** encaminha nativamente para `…/images/generations`.

Em ambos os casos o PNG que o fornecedor emitiu **voltou ao cliente com o mesmo sha256**. É a
medida que importa à R3, que acrescenta as **partes de saída**: o proxy não é um obstáculo ao
sentido de volta da imagem.

**E a tradução está medida, não só lida.** O cenário registra **em que forma** a imagem voltou. No
pedido ao endpoint dedicado, nas duas rotas, voltou em **`data[].b64_json`** — ou seja, na rota
`openrouter/…` o proxy **não** devolveu a resposta de chat em cru: converteu-a na forma da API de
imagem. No pedido de chat, nas duas rotas, voltou em **`message.images[]`**, isto é, o campo de
saída de uma resposta de chat também sobrevive à passagem. Sem este registo as duas hipóteses —
«traduz» e «devolve em cru» — davam a mesma medida, e a afirmação ficava a apoiar-se só na leitura
do `transformation.py`.

### 3.2 O proxy deixa declarar o endpoint

`modalities`, `provider` (`{"order":["Google"],"data_collection":"deny"}`), `plugins` e
`transforms` **chegaram todos ao fornecedor**, nas duas rotas, com o `drop_params: true` do banco.
É por `provider` e pelos filtros que a OpenRouter expressa a escolha de endpoint, a retenção e o
`zdr` — precisamente o que a análise dizia não ser possível declarar. A metade «não declara o
endpoint» da afirmação também cai.

(Na rota `openrouter/…` o adaptador acrescenta por conta própria `stream` e uma chave fora do
vocabulário fechado do falso, como o AOS-516 já tinha registado.)

**E com o `drop_params: false` que produção corre?** Medido à parte, à mão, porque a configuração
do banco é fixa em `true` e mudá-la mexia em todos os outros cenários. Um proxy da mesma imagem, com
`api_base` apontada para a **porta 9 (discard)** dentro do próprio contentor — assim distingue-se
«recusado antes de sair» de «tentou sair» sem fornecedor nenhum:

| Rota | `drop_params` | HTTP | Excepção do proxy |
|---|---|---|---|
| `openrouter/…` | `false` | 500 | `OpenrouterException — Cannot connect to host 127.0.0.1:9` |
| `openai/…` | `false` | 500 | `OpenAIException — Connection error` |
| `openrouter/…` | `true` (controlo) | 500 | `OpenrouterException — Cannot connect to host 127.0.0.1:9` |

Em nenhum dos três o proxy recusou por parâmetro (`UnsupportedParamsError` não aparece): **o pedido
com as quatro chaves chega sempre à tentativa de ligação**. Os dois valores de `drop_params` são
indistinguíveis neste ponto — o que é o resultado, porque é o `false` que produção corre e é com
`false` que um parâmetro não suportado passaria a erro visível.

**Limite desta medida, declarado:** ela prova que **nada recusa** o pedido antes de sair; não prova
que as quatro chaves iam no corpo, porque a ligação falhou e nenhum fornecedor o leu. Quem prova
isso é a medição com o falso, acima, feita com `drop_params: true`. As duas juntas: **com `true` as
chaves chegam; com `false` nada as recusa.** Que elas cheguem **com `false`** não está medido
directamente — é o resíduo honesto deste ponto, e o README do servidor já registava uma medição
anterior na mesma direcção («para `openai/<nome>` com `api_base` próprio, o LiteLLM 1.96.2
reencaminha tudo com `true` ou com `false`»).

### 3.3 O vídeo: o buraco é do adaptador `openrouter`, e só dele

Na 1.96.2 a geração de vídeo tem configuração para **cinco** fornecedores — `openai`, `azure`,
`gemini`, `vertex_ai`, `runwayml` (`ProviderConfigManager.get_provider_video_config`) — e
**`openrouter` não é um deles**. Medido funcionalmente: `POST /v1/videos` numa rota
`openrouter/…` sai **500 sem nada chegar ao fornecedor**, com o rasto a terminar em
`main.py:261 video_generation | _map_openrouter_exception`.

Pela rota **`openai/…`** — a forma de rota que produção já usa — o **ciclo assíncrono completo
atravessa o proxy**: criar (`videos`), sondar o estado com o id que o proxy devolve
(`videos_id`), descarregar o conteúdo (`videos_id_content`, bytes intactos). O id que o proxy
devolve tem 146 bytes e não é alfanumérico — o proxy codifica nele o modelo e o fornecedor, e é
por isso que a sondagem com ele volta à mesma `api_base`.

## 4. O que NÃO se conseguiu medir

Declarado, não contornado:

1. **`NÃO VERIFICADO` — se a OpenRouter real fala a API de vídeo da OpenAI.** O fornecedor falso
   respondeu na forma da *OpenAI Videos API* **por construção**, porque é a que o adaptador
   `openai` do proxy espera. A OpenRouter documenta o vídeo como uma API própria
   (`/api/v1/videos`, com catálogo próprio). **Que o proxy encaminha** está medido; **que o que
   ele encaminha é aceite pela OpenRouter** não está, e exige o fornecedor real e custo. É o elo
   que falta, e é o mesmo padrão de divisão de elos do AOS-534.
2. **`NÃO VERIFICADO` — para onde vai uma sondagem que o proxy não consegue decifrar.** Na rota
   `openrouter/…`, o `GET /v1/videos/<id>` com um id que o proxy não emitiu sai 500 ao cabo de
   ~4 s **sem nada chegar ao fornecedor**, com o rasto a cair no mapeador de excepções do
   `openai` (`_map_openai_exception`). A latência não é compatível com um erro local, mas **o
   destino que ele tentou não foi medido**. E o próprio rasto é um instrumento grosseiro: vem de
   `docker logs --tail 400` filtrado por `rastoSemMensagens`, **sem fronteira por pedido** — que a
   classe de excepção pertence a este pedido e não ao anterior é plausível, não garantido (herdado
   do AOS-534). Para a R3 e a R6 isto é uma pergunta de egress, não
   de funcionalidade: um adaptador que sonde jobs tem de provar que a sondagem sai para a
   `api_base` da rota e não para a omissão do fornecedor.
3. **`NÃO VERIFICADO` — a resposta ao download falhado não é estável.** Na rota `openrouter/…` o
   `GET /v1/videos/<id>/content` **nunca chegou ao fornecedor** em nenhuma das três corridas — isso
   é consistente —, mas o que o proxy respondeu ao cliente **variou**: **200 com corpo vazio** em
   duas corridas e **500 ao cabo de ~10 s** na terceira (rasto a terminar em
   `_map_openai_exception`). Um download que nunca aconteceu e responde 200 numas vezes e 500
   noutras é uma forma que quem consumir este endpoint tem de tratar nas duas; a causa da variação
   no proxy não foi investigada. **Não se conclua daqui um comportamento determinista em nenhum dos
   dois sentidos.**
4. **Fora de âmbito, como no AOS-534:** que modelos geram imagem ou vídeo pela OpenRouter, com
   que preço por imagem e com que limites. É o que a R3 precisa para o preço por imagem, e mede-se
   no banco com o fornecedor real.
5. O MP4 e o PNG do ensaio são **sintéticos**. O PNG é um PNG válido; os bytes do conteúdo de
   vídeo do falso não são um MP4 — o spike mede encaminhamento, não descodificação.

## 5. O que isto decide, e o que fica para o dono

**Fica resolvido:** a pré-condição «spike do LiteLLM (§3 E)» da fase R3. Para a **imagem** — que é
o que a R3 entrega (`model_invoke` genérica, partes de saída, preço por imagem) — o proxy fixado
encaminha, nas duas rotas, e nos dois sentidos. **A R3 pode seguir pelo proxy, e a alternativa (E)
não tem justificação para a imagem.** Não há destino de egress novo, não se toca no guard-test
AOS-069, não se toca na allowlist.

**Fica por decidir, e é do dono — DECISÃO DE ARQUITECTURA PENDENTE, não a tomei:**

> **O vídeo gerado (fase R6) pela rota `openai/…` do proxy, ou um adaptador directo?**
>
> O ciclo assíncrono de vídeo atravessa o proxy fixado **pela forma de rota `openai/…`**, que é a
> que produção já usa — sem egress novo. Mas o que ele encaminha é a **API de vídeo da OpenAI**, e
> a OpenRouter documenta o vídeo como uma **API própria**. Se a OpenRouter não aceitar essa forma,
> a R6 fica com três saídas, todas com custo de decisão:
>
> 1. **um fornecedor de vídeo que a 1.96.2 suporta** (`gemini`, `vertex_ai`, `runwayml`, `azure`,
>    `openai`) em vez da OpenRouter — é um **fornecedor novo**, com qualificação no banco,
>    assinatura do registo e, possivelmente, uma região nova;
> 2. **um adaptador directo à OpenRouter para o vídeo** — a alternativa (E), que é um **destino de
>    egress novo que o guard-test AOS-069 recusa hoje**: exige ADR e a decisão do dono;
> 3. **subir a versão do proxy**, se uma posterior trouxer configuração de vídeo para o
>    `openrouter` — é supply-chain (ADR-017) e um digest novo, que se volta a medir no banco.
>
> A escolha **não é deste ticket** e nenhuma das três se preparou aqui. O que a R6 precisa antes
> de qualquer uma é o elo não medido do ponto 4.1: medir, com o fornecedor real e com custo, se a
> OpenRouter aceita a forma que o proxy monta.

A §3 E do ADR-043 fica com o resultado escrito, e a alternativa (E) deixa de estar «em aberto»
para a imagem; para o vídeo passa a estar **em aberto só para a R6**, com as três saídas acima.

## 6. Como repetir a medição

Precisa de Docker, da imagem já descarregada (não a descarrega) e de um binário **linux** do
banco:

```bash
cd packages/qa/banco-ensaio
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/aos-ensaio-linux ./cmd/aos-ensaio
AOS_BANCO_PROXY=1 AOS_BANCO_FALSO_BIN=/tmp/aos-ensaio-linux \
  go test -run '^TestAOS558_ProxyReal_ImagemEVideoPeloProxy$' -v -count=1 -timeout 40m .
```

O registo do caminho e das chaves no falso tem testes que **correm sem Docker**
(`TestAOS558_Falso_*`, `TestAOS558_ClassificarCaminho`). O inventário de rotas da §2 (a) reproduz-se
com:

```bash
docker run --rm --network none --entrypoint python ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63 -c 'from litellm.proxy.proxy_server import app
print(sorted({(r.path, tuple(sorted(r.methods or []))) for r in app.routes if "video" in r.path or "image" in r.path}))'
```

O cenário **mede, não julga**: fica vermelho só se o ensaio não se montar, se o proxy não chegar
ao falso no pedido de controlo só com texto, se o PNG do falso não for o deste teste, ou se uma
chave aparecer no relatório.

## 7. O que a revisão adversarial mudou no instrumento

A primeira versão do cenário mediu o que está nas §2 e §3, mas tinha três maneiras de **mentir** que
uma revisão independente encontrou antes do PR. Ficam registadas porque são o tipo de defeito que,
não apanhado, se torna a razão pela qual ninguém volta a verificar:

1. **Uma recusa do fornecedor era lida como «o proxy não encaminhou».** O falso responde 401 a uma
   chave errada **antes** de contar o pedido, e o veredicto do encaminhamento olhava só para
   «quantos pedidos chegaram». Um pedido que o proxy **encaminhou** e o falso recusou ficava
   indistinguível de um que o proxy nunca mandou — e a conclusão «o vídeo não sai pela rota
   `openrouter/…`» é exactamente dessa forma. O falso passou a contar o 401
   (`respostas_401_por_causa`), o vocabulário ganhou `recusado_pelo_falso` (que o AOS-534 já tinha e
   este cenário deixou cair), e `recusado_pelo_proxy` passou a chamar-se
   **`nao_chegou_ao_fornecedor`** — porque há casos em que o proxy responde 200 e nada chega lá, e o
   nome antigo dizia o contrário do que aconteceu. *A conclusão desta corrida não muda:* os 500 da
   rota `openrouter/…` têm corroboração independente no rasto do proxy, que falha antes do egress.
2. **A sentinela era produzida num binário e verificada noutro, sem ninguém os confrontar.** O PNG
   viaja no binário **linux** do falso, dentro do contentor; o sha esperado é do processo do teste.
   Se não fossem da mesma árvore, **todos** os casos de imagem davam «saída alterada» e a conclusão
   invertia-se sem aviso. O falso passou a publicar o sha do PNG que emite
   (`sha256_do_png_do_falso`), e o cenário confronta-o na montagem do ensaio — onde um vermelho não
   se confunde com um resultado do spike.
3. **Um corpo `multipart` num POST de vídeo envenenava a medida central.** O marcador
   `corpo_nao_json` escrevia um segundo nome no mapa dos **caminhos** para o mesmo pedido, e o
   veredicto decide por «mais do que um caminho». Não disparou nesta corrida (os três corpos são
   JSON), mas o endpoint do proxy aceita `multipart` — é o caso do elo que falta (§4.1). O marcador
   passou para as chaves do topo, que é onde uma observação sobre o corpo pertence.

Mais duas afirmações que o código não sustentava: a saída da imagem dizia `intacta` sem dizer **em
que forma** voltou (uma rota que devolvesse a resposta de chat em cru media o mesmo que uma que a
traduzisse — agora há `forma_da_saida`), e um caso **recusado** recebia `alterada_ou_perdida`,
dizendo que uma saída que nunca existiu se perdeu (agora fica `nao_aplicavel`, como no vídeo).
