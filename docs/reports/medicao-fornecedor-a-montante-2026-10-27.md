# Medição AOS-565 — o proxy deixa passar o fornecedor a montante na volta?

> Medição do **primeiro critério de aceitação** do AOS-565 (fase R3 do ADR-043, §2.4 regra 3: «o
> fornecedor é fixado por pedido … e o fornecedor efectivo fica **selado**»). Corrida a
> **2026-10-27** no posto, com Docker, contra a **imagem de produção do proxy fixada pelo digest**
> e um **fornecedor falso**. **Sem nenhuma chamada real à OpenRouter:** sem custo e sem destino de
> egress novo.
>
> Escrito no molde de `docs/reports/spike-litellm-imagem-video-2026-10-25.md` (AOS-558), e pela
> mesma razão: uma medida que só vive numa mensagem de commit não é re-lível por quem vier a
> seguir.

---

## 1. A afirmação que se foi medir

O desenho da R3 (`docs/reports/desenho-r3-geracao-2026-10-27.md` §3.6) deixou escrito, depois de
uma revisão adversarial o ter apanhado:

> «"O fornecedor efectivo fica selado" pressupõe que o proxy **diz qual foi**. […] **`NÃO
> VERIFICADO`: que o proxy reporte o fornecedor efectivo de volta, e por que via.** Se não
> reportar, o AOS-565 entrega só a metade da ida.»

A pergunta parte-se em duas metades com donos diferentes, e **só uma se mede de graça**:

| | A pergunta | Mede-se como | Dono |
|---|---|---|---|
| **(i)** | o fornecedor a montante **emite** a identidade efectiva? | fornecedor real, **com custo** | o dono — AOS-570a |
| **(ii)** | se ela vier no corpo, o proxy **deixa-a passar**? | proxy fixado + fornecedor falso, sem custo | esta medição |

A **(ii)** é decisiva por si: se o proxy a removesse, o nó nunca a veria **mesmo que** a OpenRouter
a emitisse, e a metade «selar o fornecedor efectivo» teria de ir buscar o valor a um cabeçalho ou
cair.

---

## 2. O que se mediu, e como

- Imagem do proxy: `ghcr.io/berriai/litellm@sha256:154e23bb…25ce3c63` (LiteLLM **1.96.2**), a
  mesma que `policy/routeentry/load.go` fixa para produção.
- Instrumento: `packages/qa/banco-ensaio/aos565_fornecedor_test.go` (offline) e
  `aos565_proxyreal_test.go` (a medição), com as sentinelas em `falso_fornecedor.go`.
- Modelo da rota: `anthropic/claude-sonnet-4.5`. Pedido: `POST /v1/chat/completions`, sem tools,
  com `provider:{"order":["Anthropic"],"allow_fallbacks":false,"data_collection":"deny"}` na ida.
- **Duas formas de rota**, cada uma no seu contentor: `openrouter/<autor>/<modelo>` e
  `openai/<autor>/<modelo>` com a base da OpenRouter — **esta última é a de produção**.
- **Três corridas por rota.** Uma corrida só apresenta não-determinismo como determinismo, e o
  AOS-558 já apanhou uma resposta instável assim.

### 2.1 As três sentinelas, e porque são três

O falso responde, **além do corpo de sempre**, com três valores distintos:

| Sentinela | Onde | Para quê |
|---|---|---|
| `provider` = `AOS565-FORNECEDOR-DO-FALSO` | chave de topo do corpo | a pergunta |
| `aos_sonda` = `AOS565-SONDA-INVENTADA` | chave de topo **inventada** | **o controlo** |
| `X-Aos-Sonda-Fornecedor` = `AOS565-CABECALHO-DO-FALSO` | cabeçalho de resposta | a via alternativa |

**O controlo é o que torna a medida válida.** Sem ele, uma `provider` que chegasse não distinguiria
«o proxy preserva chaves de topo desconhecidas» de «o proxy trata a `provider` de forma especial» —
e as duas hipóteses dão conclusões diferentes para o desenho. É a correcção que a §7 do spike
AOS-558 fez ao seu próprio instrumento, aplicada aqui desde o início.

Antes de cada série corre um **pedido de controlo da montagem** (só texto): se ele não chegar ao
falso, a série não vale, e o classificador diz
`nao_medida_o_pedido_nao_chegou_ao_fornecedor` em vez de `removida`. Dizer «removida» quando o
falso nunca chegou a emitir sentinela nenhuma seria uma medida falsa.

---

## 3. O resultado

| Rota | Corrida | HTTP | Encaminhamento | `provider` | `aos_sonda` (controlo) | Cabeçalho |
|---|---|---|---|---|---|---|
| `openrouter/…` | 1 | 200 | `chat_completions` | **intacta no topo** | **intacta no topo** | chegou |
| `openrouter/…` | 2 | 200 | `chat_completions` | **intacta no topo** | **intacta no topo** | chegou |
| `openrouter/…` | 3 | 200 | `chat_completions` | **intacta no topo** | **intacta no topo** | chegou |
| `openai/…` (produção) | 1 | 200 | `chat_completions` | **intacta no topo** | **intacta no topo** | chegou |
| `openai/…` (produção) | 2 | 200 | `chat_completions` | **intacta no topo** | **intacta no topo** | chegou |
| `openai/…` (produção) | 3 | 200 | `chat_completions` | **intacta no topo** | **intacta no topo** | chegou |

**6/6 observações idênticas, em dois contentores de proxy diferentes.**

Chaves de topo que chegaram ao cliente, nas seis corridas:
`aos_sonda, choices, created, id, model, object, provider, usage`.

### 3.1 O LiteLLM re-serializa, e mesmo assim preserva

**O falso não emite `created`.** Ele aparece no corpo que o cliente recebe. Ou seja, o proxy **não
faz passagem byte a byte**: reconstrói o documento — e **ainda assim** preserva as duas chaves de
topo que não conhece. A preservação é comportamento, não acidente de não tocar no corpo.

### 3.2 O veredicto

> **O proxy deixa passar.** A chave de topo `provider` do corpo da resposta chega intacta ao
> cliente nas duas formas de rota, e **não é tratada de forma especial** — a chave inventada
> sobreviveu exactamente da mesma maneira. O LiteLLM 1.96.2 preserva chaves de topo desconhecidas
> na resposta de chat em geral.

**A conclusão não depende do nome da chave.** Se a OpenRouter nomear o fornecedor noutra chave de
topo, a passagem mantém-se — é o controlo que generaliza a medida para além da hipótese sobre o
nome.

**Consequência para o ticket:** a metade «selar o fornecedor efectivo» **não cai por causa do
proxy**. Fica a depender só da metade (i), que é do dono. Não é preciso recorrer a um cabeçalho.

### 3.3 Uma via que nunca tinha sido enumerada

Os 27 cabeçalhos vistos ao cliente são iguais nas seis corridas. Dois achados:

- **Nenhum `x-litellm-*` nomeia o fornecedor.** Há modelo (`x-litellm-model-name`,
  `x-litellm-model-group`, `x-litellm-model-id`) e endpoint (`x-litellm-model-api-base`). Confirma
  o que `port/route.go:26-30` já dava por medido.
- **O LiteLLM reemite TODOS os cabeçalhos de resposta do fornecedor com o prefixo
  `llm_provider-`.** O cabeçalho inventado do falso chegou como
  `llm_provider-x-aos-sonda-fornecedor`, ao lado de `llm_provider-content-type`,
  `llm_provider-date` e `llm_provider-content-length`. **É uma segunda via viável, independente do
  corpo, e é informação nova:** a enumeração que fundamentou o `NÃO VERIFICADO` do ticket olhou só
  para os `x-litellm-*` que o proxy **inventa**, não para os que ele **reemite**.

A segunda via fica **registada e não implementada** (comentário da sonda em `openai_http.go` e
cabeçalho de `port/provider.go`): uma via chega, e duas seriam duas superfícies a manter.

---

## 4. O que NÃO se conseguiu medir

Declarado, não contornado.

1. **`NÃO VERIFICADO` — a metade (i): a OpenRouter real emite a identidade efectiva do fornecedor,
   e com que nome e forma.** Não foi tentada, por exigir custo. É o **AOS-570a**. **O resultado
   desta medida não licencia nenhuma conclusão sobre isso**, e o código do AOS-565 diz que
   `nao_reportado` é o caso esperado hoje em vez de prometer um valor.
2. **`NÃO VERIFICADO` — a forma do valor.** O falso emitiu uma **string**, por construção. Se a
   OpenRouter puser o fornecedor num objecto (`{"provider":{"name":…}}`), a sonda devolve
   `nao_reportado` — deliberado, porque não adivinha. Muda num sítio só quando a (i) responder.
3. **`NÃO VERIFICADO` — o caminho de STREAMING na volta.** Mediu-se o chat síncrono, que é onde
   vivem o corpo cru e as duas sondas que esta fase imita. Numa resposta SSE não há «chave de
   topo» no mesmo sentido. **O risco é baixo e medido:** nada em produção chama o `ChatStream` — os
   únicos chamadores são o próprio gateway a falar com o adaptador (`gateway.go:533`) e o
   decorador do banco de ensaio.
4. **`NÃO VERIFICADO` — o `drop_params: false` que produção corre.** A configuração do banco é fixa
   em `true`, como no spike AOS-558. É o mesmo resíduo que ele declarou para a ida, e fecha-se na
   prova em produção.
5. **Só `chat/completions`.** `images/generations` e a família do vídeo não entraram: o AOS-565
   aplica-se aos turnos e às duas tools de modelo, que falam chat.
6. **Vale para a 1.96.2 fixada pelo digest.** Um digest novo volta a medir-se (ADR-017).

---

## 5. Hipóteses do instrumento

- **Que a `provider` que o cliente viu veio do falso e não do LiteLLM.** Fundada na igualdade
  exacta com a sentinela; assume que o proxy não emite por acaso a mesma cadeia. Risco desprezável.
- **Que a OpenRouter nomeia o fornecedor numa chave de topo chamada `provider`.** Tirada do
  enunciado do ticket, **não confirmada**. **Mitigada pelo controlo:** a conclusão vale para
  qualquer chave de topo desconhecida.
- **Que o `drop_params` não actua no sentido de volta.** É descrito e medido como tratamento de
  parâmetros de **pedido**. Não se correu o `false`.
- **Que o `created` ausente no falso e presente no cliente prova re-serialização.** É a leitura
  mais directa; não se leu o código da imagem a confirmá-lo.
- **Que o prefixo `llm_provider-` é reemissão sistemática.** Inferido de quatro cabeçalhos baterem
  certo. Não se leu o código do LiteLLM, nem se sabe se há lista de exclusão.

---

## 6. Como repetir

```bash
cd packages/qa/banco-ensaio
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o <tmp>/aos-ensaio-linux ./cmd/aos-ensaio
AOS_BANCO_PROXY=1 AOS_BANCO_FALSO_BIN=<tmp>/aos-ensaio-linux \
  go test -run '^TestAOS565_ProxyReal_FornecedorDeVolta$' -v -count=1 -timeout 40m .
```

O relatório completo sai em `AOS_BANCO_FORNECEDOR_REPORT`. O instrumento tem testes que correm
**sem Docker**: `TestAOS565_Falso_EmiteAsTresSentinelas`,
`TestAOS565_Falso_SemBandeiraOCorpoEODeSempre` (regressão byte a byte: sem a bandeira o corpo é o
de hoje), `TestAOS565_Classificar` e `TestAOS565_CLI_BandeiraSoNaFormaOpenRouter`.
