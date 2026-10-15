# Desenho A5 e A6 — imagem, vídeo e outras modalidades: ferramentas primeiro

> **Nota de 2026-10-15.** O AOS-527 fundiu-se (#495) e está em produção na v0.1.56: o nó conta o
> custo do modelo titular por uma tabela de preços do operador, e há um tecto em dólares por run.
> Não toca no custo das tools (D6). As menções abaixo ao ramo `feature/AOS-527-custo-do-modelo`
> são de antes da fusão. A fase A5 foi aberta pelo dono a 2026-10-15; as decisões continuam por tomar.

> **Estado deste documento.** Desenho para o dono, 2026-10-14. **Nenhum ticket está aberto e
> nenhum código foi escrito.** Os tickets só se abrem depois das decisões D1 a D9 (§7). Os itens
> de trabalho têm rótulos provisórios — «A5-ocr», «A6-video», e assim por diante — que não são
> identificadores de ticket; a numeração faz-se na altura, com `scripts/ci/sessoes.py reservar`.
>
> Data: 2026-10-14. Base lida: worktree `a3-compose`, ramo `feature/AOS-527-custo-do-modelo` @
> `57ed4a0e` (o mesmo commit que a base `feature/AOS-128-ux-dx-tests` tinha nessa altura).
> Produção: v0.1.55, titular `gpt-4o` → `openrouter/anthropic/claude-haiku-5.5` (acompanhamento,
> §5, linha de 2026-10-14 «Estado de produção»). Trabalho de leitura e desenho: nada foi pedido a
> um modelo, a produção ou à rede.
>
> **Como ler as marcas.** `[CÓDIGO]` = lido no código, com ficheiro:linha. `[MEDIDO]` = número
> registado no documento de acompanhamento (`docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md`,
> abreviado *acomp*). `[POR CONFIRMAR]` = não está no repositório; ninguém o verificou.
> `[NÃO ENCONTREI]` = procurei e não está lá.
>
> Abreviaturas de caminho: `gw/…` = `packages/platform/model-gateway/…`; `nó/…` =
> `packages/cmd/aos/…`; `orq/…` = `packages/cmd/aos-orq/…`; `rt/…` =
> `packages/kernel/agent-runtime/…`; `rm/…` = `packages/kernel/reference-monitor/…`; `sbx/…` =
> `packages/substrate/sandbox/…`; `gv/…` = `deploy/server/gvisor/…`.
>
> **Palavras usadas aqui.** *Media*: um ficheiro que não é texto — imagem, vídeo, áudio.
> *Tool de leitura de media*: uma tool que recebe a referência a um media e devolve **texto**
> (OCR, descrição, transcrição). *Titular*: o modelo que serve os runs (hoje o Haiku). *Pasta
> semeada*: a pasta só de leitura de onde a `doc_read` lê documentos.

## 0. A conclusão, à cabeça

**A forma mais simples de pôr imagem e vídeo num run não é ensinar o titular a ver: é dar ao
modelo ferramentas que vêem por ele e lhe devolvem texto.** Quase tudo o que isso pede já existe.
O que falta é pequeno e está bem localizado.

| O que o caminho «ferramentas primeiro» precisa | O que já existe |
|---|---|
| Uma tool mediada que lê um ficheiro por id | A `doc_read`: registo em `deploy/server/model-tools/tools.json:1-20`, mediação pelo RM, execução no gVisor sem rede (`gv/component/main.go:147-157`) `[CÓDIGO]` |
| O resultado da tool como texto no prompt, com taint | O envelope com `stdout_text` (`sbx/mediated.go:122-135`) e o taint `untrusted` imposto pelo driver (`sbx/driver_gvisor.go:77-93`) `[CÓDIGO]` |
| Replay sem voltar a correr a tool | O resultado fica no step-ledger e na captura, cifrados por titular (`nó/bootstrap.go:1702`, `:1721`); o replay não re-executa efeitos (`rt/replay/doc.go:1-4`) `[CÓDIGO]` |
| Passar o texto lido ao nó seguinte sem perder factos | A saída por referência (A0.5), provada em produção: 18 de 18 entregas byte a byte (acomp, §2) `[MEDIDO]` |
| Uma tool que reporta o custo do que fez | `RegisterCosting` no RM (`rm/monitor.go:211-222`) `[CÓDIGO]` — existe, **nenhuma tool do nó a usa** |
| Uma chamada a um modelo com custo, allowlist e proveniência | O Model Gateway: custo por (modelo, região) (`gw/gateway.go:727-747`), allowlist assinada (`gw/policy/allowlist/allowlist_policy.json:16-35`) |

E há um facto que torna a ferramenta necessária, não só cómoda: **hoje, se a `doc_read` ler uma
imagem, o modelo recebe a imagem em base64** (`sbx/mediated.go:128-129`). Já se viu o que isso
dá, com documentos de texto: o modelo recebeu base64, tentou ler outra vez, foi negado e desistiu
(AOS-487, `sbx/mediated.go:124-127`).

**O que este desenho recomenda, por ordem:**

1. **OCR como tool** («A5-ocr»). Um verbo novo, `ocr`, no executor do gVisor, e uma entrada
   `ocr_read` no `tools.json`. Serve qualquer titular, não tem rede, não custa dinheiro por
   chamada, e o replay vem de graça. Primeiro mede-se no banco de ensaio (D2).
2. **Vídeo como tool que chama um modelo capaz de vídeo** («A6-video»), pelo Model Gateway, para
   o custo e a proveniência ficarem num só sítio. Precisa de três peças novas: a porta do gateway
   aceitar partes de media no **pedido**; a tool registar o custo de forma durável; e um alias
   novo na allowlist assinada (D3, D6).
3. **A A5 propriamente dita** — o titular recebe a imagem por referência, com replay — **só
   quando aparecer um caso em que o modelo precisa de ver a imagem ele próprio** («A5-titular»).
   Reaproveita a porta do passo 2.
4. **Escolha de modelo por capacidade** (determinista, lida da entrada) **só se aparecer um caso
   que as tools não cubram** (D9). Não é a cascata por custo: essa a medição da A4 não
   justificou (acomp, §3, «Orientação do dono (2026-10-13)»).

**Um aviso honesto sobre os critérios.** O critério da A5 é «um modelo recebe imagem ou áudio
num run, com replay». O OCR como tool **não o cumpre à letra**: quem recebe a imagem é a tool, e
o modelo recebe texto. O da A6 é «uma modalidade **gerada**»; descrever um vídeo não gera uma
modalidade. Este desenho não redefine os critérios por baixo: propõe sub-fases com critério
próprio e deixa os critérios originais à espera de um caso (D7).

## 1. O que a A5 e a A6 prometem e o que não prometem

### Um exemplo

O operador põe `fatura-0912.png` na pasta semeada. Um plano pede «lê a fatura 0912 e diz o total
e o NIF do fornecedor». O nó 1 chama `ocr_read` com `doc_id = fatura-0912.png`. O Reference
Monitor decide, como para a `doc_read`; o gVisor corre o OCR sem rede; o resultado volta como
texto, com o digest da imagem. O modelo lê o texto e chama mais nada. Pela saída por referência,
o texto do OCR passa ao nó 2 byte a byte; o nó 2 escreve o resumo. O replay do run reproduz os
dois prompts sem voltar a correr o OCR. O WORM tem o selo da mediação da `ocr_read`.

Com vídeo é igual, com `video_describe` no lugar de `ocr_read`, e com uma diferença: a tool chama
um modelo pago, e o custo dessa chamada fica gravado no run, no passo da tool.

### Prometem (o caminho recomendado)

- Uma imagem na pasta semeada chega a um run como **texto lido por OCR**, por uma tool mediada,
  com o digest da imagem no resultado e replay fiel.
- Um vídeo na pasta semeada chega a um run como **descrição ou transcrição** feita por um modelo
  capaz de vídeo, por uma tool mediada, com o modelo servido, o digest do vídeo e o custo gravados.
- Qualquer titular serve. Trocar de titular não muda nada disto.
- Tudo desligado por omissão: uma tool só existe se o operador a puser no `tools.json`.

### Não prometem

- **Que o titular veja a imagem.** Isso é a «A5-titular», adiada até haver um caso (D1).
- **Gerar imagem, áudio ou vídeo.** Sem caso conhecido; fica de fora (D8).
- **Áudio** como fase própria. O executor do vídeo serve-o com outra entrada no `tools.json` e
  outro modelo, se aparecer o caso (D8).
- **Upload de media por API.** O media entra pela pasta semeada, como os documentos de hoje (D4).
- **Redacção do conteúdo do media.** Hoje o conteúdo lido por tools vai ao fornecedor sem
  redacção; só o objectivo é redigido (acomp, §4, 2026-10-13) `[MEDIDO]`. Uma imagem não tem
  redacção possível. Vale a premissa do dono: o sistema não trata dados pessoais de pessoas na UE.

## 2. O que já existe e se reaproveita

### 2.1 Tools: registo, mediação, execução

| Peça | Onde | O que importa para a A5/A6 |
|---|---|---|
| Registo das tools (face do modelo + ligação de governação) | `nó/modeltools.go:45-80`; `tools.json:1-20` | Uma tool nova é uma entrada JSON: nome, schema, `capability`, recurso, `egress`, `sandbox`. O modelo só escolhe o nome; a capability e o recurso vêm do registo (`nó/modeltools.go:16-22`) |
| Recurso parametrizado pelo argumento | `nó/modeltools.go:191-245` | `resource_value: "doc://{doc_id}"` — o WORM sela o ficheiro que a tool tocou de facto. Uma `ocr_read` herda isto |
| Região da tool dentro dos boards | `nó/modeltools.go:92-125` | A `resource_region` tem de estar no mapa de soberania, ou o nó não arranca |
| Mediação no RM, default-deny | `rm/monitor.go:200-232` | Uma tool não registada é negada. `Register` (custo 0) ou `RegisterCosting` (custo medido) |
| Execução em sandbox, sem rede | `nó/sandboxwiring.go:111-160`; `gv/component/main.go:1-12`, `:147-157` | Um bundle OCI novo por chamada, rootfs só de leitura, `--network=none`. O verbo vem do registo, nunca do modelo |
| Os verbos que o guest conhece | `gv/guest/main.go:69-88` | Só um: `read`. Qualquer outro é recusado. Um `ocr` é um verbo novo aqui |
| O rootfs do guest | `gv/component/main.go:128-140` | Só o binário estático do guest. Um motor de OCR precisa de entrar neste rootfs |
| Limites do executor | `gv/component/main.go:60`; `deploy/server/docker-compose.prod.yml:937-939` | 45 s por chamada; 1 GB de memória; 512 processos |
| O envelope do resultado | `sbx/mediated.go:122-135` | Texto em `stdout_text`; binário em `stdout`, base64 |
| Egress real no RM | `integration/secured.go:401-412`; `sbx/network/hook.go:9-41` | Uma capability de rede com recurso que não é de rede é negada (CamoLeak) |
| Tecto de egress do catálogo | `nó/bootstrap.go:2169-2174`; `packages/platform/registry/revalidation/ports.go:105-135` | Sem catálogo assinado, o tecto é `external` |

### 2.2 Taint e autoridade

- O resultado de uma tool é sempre `untrusted` (`sbx/driver_gvisor.go:77-93`) `[CÓDIGO]`.
- A autoridade de uma tool call é a do contexto do turno: depois do primeiro `tool_result`, o
  run é `untrusted` até ao fim (`rt/context_authority.go:10-21`; `rt/loop.go:646`, `:826`)
  `[CÓDIGO]`.
- Com `cap:fs.read` armada, **uma segunda leitura no mesmo run é negada** — medido em produção na
  série `v0151t` (acomp, §3 A1, linhas 159-165; §4, 2026-10-06) `[MEDIDO]`.

**Consequência para a `ocr_read`:** se partilhar a capability da `doc_read`, o modelo só pode
chamar uma das duas, e só uma vez, por run. Num nó que lê uma imagem isso basta. Num nó que lê
um texto e **depois** uma imagem, não. A D2 trata disto.

### 2.3 A saída por referência (A0.5)

- O kernel designa e sela a origem da saída: a chamada efectiva da tool declarada, no primeiro
  turno com tools, com contexto trusted (`rt/output_source.go:80-100`, `:202-222`) `[CÓDIGO]`.
- O `aos-orq` entrega o `stdout_text` do envelope, conferido contra o digest selado
  (`orq/entrega_por_referencia.go:205-222`) `[CÓDIGO]`.
- **Um envelope binário não se entrega**: `formaEnvelopeBinario` → `causaOrigemNaoTransportavel`
  (`orq/saida_por_referencia.go:224-226`; `orq/entrega_por_referencia.go:207-208`) `[CÓDIGO]`.
- Tecto de 128 KiB por payload e 1 MiB no corpo do `POST /runs` (`orq/node_executor.go:395-399`)
  `[CÓDIGO]`.
- A alternativa em que o nó consumidor resolve a referência ficou rejeitada **com gatilho**, e
  um dos gatilhos é «a fase A5 ou A6» (acomp, §4, linha de 2026-10-05; desenho
  `desenho-saida-por-referencia-2026-10-05.md:298-306`). A razão de rejeição foi o *confused
  deputy*: o nó a decifrar conteúdo de outro run por uma referência escrita num pedido.

**O que isto quer dizer aqui:** com ferramentas primeiro, **os bytes do media nunca viajam entre
nós**. Viaja o texto que a tool produziu, que já cabe no caminho da A0.5. O gatilho «A5/A6» da
opção (b) **não dispara** enquanto o media não sair da tool. Só a «A5-titular» o tocaria, e mesmo
aí os bytes ficam no nó que os lê (§5.4).

### 2.4 O protocolo com o modelo

| Peça | Onde | Estado para media |
|---|---|---|
| `Message.Content` | `gw/port/port.go:72-77` | **É uma string.** Não há partes de imagem, vídeo ou áudio no pedido |
| Leitura de `content` em partes | `gw/port/port.go:115-121`, `:161-170` | Uma resposta com uma parte que não é texto é **recusada** (`ErrContentPartNotText`), de propósito |
| Versão da porta | `gw/port/port.go:37` | `1.11.0`. Partes de media no pedido seriam um MINOR |
| A projecção para mensagens nativas | `gw/projection.go:585-596`, `:701-725` | `user` e `tool` são texto: `Content: string(corpo)` |
| O prompt do runtime | `rt/prompt.go:98-107`, `:136-148` | Segmentos de bytes opacos; o `prompt_hash` é `sha256` do texto materializado |
| Tipos de segmento | `rt/prompt.go:65-95` | Não há um segmento de media |
| Capacidades no perfil da rota | `gw/route.go:91-110`; `gw/route_profile.go:97-101` | Só `tools`. Qualquer outra é recusada na validação |

### 2.5 Replay, captura e tamanho dos eventos

- A captura de cada turno grava a resposta e os resultados das tools, cifrados por titular, no
  evento `replay.captured` (`rt/replay/nondeterminism_capture.go:219-263`; `nó/bootstrap.go:1702`)
  `[CÓDIGO]`.
- O step-ledger grava o resultado de cada tool call, cifrado com a mesma chave
  (`nó/bootstrap.go:1706-1722`) `[CÓDIGO]`. **Um resultado vai ao log duas vezes.**
- O NATS de produção aceita mensagens até 1 MB (`deploy/nats/aos-nats.sh:310`) `[CÓDIGO]`.
- Existe um armazém de payloads fora do Event Store, endereçado por digest e com IAM próprio
  (`rt/replay/payload_store.go:11-35`) — **só a implementação em memória; não está ligado no nó**
  (`nó/bootstrap.go:1702` não o passa) `[CÓDIGO]`.
- O replay não chama o modelo nem re-executa efeitos; re-materializa o prompt e compara o
  `prompt_hash` (`rt/replay/doc.go:1-24`) `[CÓDIGO]`.

**O que isto quer dizer aqui:** um resultado de OCR de alguns KiB cabe sem mudança nenhuma. Uma
imagem em base64 dentro de um resultado de tool ia ao log duas vezes e passava facilmente 1 MB.
**Os bytes do media não podem entrar nos eventos.** Só o digest.

### 2.6 Custo

- O custo do modelo vem da tabela de preços por (modelo, região), em micro-USD por milhão de
  tokens (`nó/model_pricing_env.go:3-48`; `gw/pricing/pricing_table.json`) `[CÓDIGO]`. Só há preço
  por token: nem por imagem, nem por segundo de vídeo, nem por chamada.
- O titular de hoje não é contado: «O custo do modelo passa a ser por token na OpenRouter, e o nó
  não o conta» (acomp, §4, 2026-10-14) `[MEDIDO]`. Há um ramo de trabalho com o nome
  `feature/AOS-527-custo-do-modelo`, sem ticket na base lida `[POR CONFIRMAR]` o que cobre.
- **O custo de uma tool call é zero em dólares por desenho**: a estimativa das tool calls só conta
  tokens, porque «o custo de uma tool call não é conhecido antes do efeito»
  (`integration/budget.go:107-112`) `[CÓDIGO]`.
- O custo medido de uma tool (`RegisterCosting`) é **só um sinal de observabilidade**: vai ao span
  `aos.activity` e **nunca ao durável** (`rm/decision.go:151-159`) `[CÓDIGO]`. Não entra no
  burn-down, que é o livro de turnos (`nó/model_pricing_env.go:40-48`).
- O consumo das tool calls regista-se depois de confirmado (`integration/budget.go:545-583`,
  AOS-287), mas é a **reserva estimada**, sem dólares.
- O Model Gateway já tem uma operação que não é chat e é contabilizada: os embeddings
  (`gw/gateway.go:549-590`) `[CÓDIGO]`. É o precedente para uma chamada de outra forma com custo.

**O que falta para a A6 («custo contabilizado»):** a tool que chama um modelo tem de deixar o
custo **gravado no run**, no passo dela, e somado ao consumo do run. Hoje nenhum caminho faz isso.

### 2.7 Proveniência

- Cada mediação é selada no WORM (`tool.call.mediated`, `rm/eventsink.go:14`) `[CÓDIGO]`, com o
  recurso efectivamente tocado (`nó/modeltools.go:191-208`).
- Cada turno do modelo grava o manifesto (`turn.recorded`: hashes, modelo, parâmetros — 
  `rt/capture.go:9-22`), e o modelo servido é comparado por turno com o perfil (acomp, §2, A1)
  `[MEDIDO]`.
- A allowlist do gateway e o perfil carregado são selados na activação (`gw/production.go:40-49`,
  `:388-391`) `[CÓDIGO]`.
- **O que falta:** o digest do media que a tool leu. Hoje o resultado da `doc_read` tem os bytes
  lidos, não o digest deles.

### 2.8 Os ADR

Procurei `multimodal`, `imagem`, `image`, `áudio`, `audio`, `OCR` e `media` em `docs/adr/`.
**Nenhum ADR trata de media** `[NÃO ENCONTREI]`; as ocorrências de «imagem» são imagens Docker.
Os que esta fase toca: ADR-034 (autoridade do contexto), ADR-036 (o tail como forma canónica; a
rota e o perfil), ADR-038 (saída por referência), ADR-040 (estado opaco selado), ADR-041 (titular
e, no máximo, um candidato — `docs/adr/ADR-041-mais-de-um-modelo-por-no-titular-e-candidato.md:23-30`).
No EPIC-06, o conteúdo multimodal está declarado fora de âmbito com remissão para a A5 e a A6
(`specs/EPIC-06_Model_Gateway_Custos.md:2726`).

## 3. As perguntas centrais

### 3.1 Quem vê o media: uma tool, ou o modelo?

| | Tool que devolve texto | O titular vê o media |
|---|---|---|
| Serve qualquer titular | Sim | Só titulares com essa capacidade |
| Muda a porta do gateway | Não (OCR); sim, no pedido (vídeo) | Sim, pedido e prompt |
| Muda o prompt e o `prompt_hash` | Não | Sim: segmento novo de media |
| Replay | O de hoje (resultado no ledger) | Novo: o digest no prompt, os bytes fora |
| Custo por turno | Uma vez, na tool | **Em cada turno seguinte**: a conversa reenvia-se inteira |
| Taint | O de hoje | O media é untrusted; uma imagem pode trazer instruções escritas |
| Auditável | O texto lido fica selado | O que o modelo «viu» não se lê |

A linha do custo por turno é a menos óbvia: um run reenvia a conversa toda a cada turno
(`gw/projection.go:585-614`), por isso uma imagem dada ao titular no turno 1 paga-se outra vez em
cada turno seguinte. Uma tool paga uma vez. O valor exacto depende de como o fornecedor conta
imagens `[POR CONFIRMAR]`.

### 3.2 Onde corre a tool que precisa de rede?

O gVisor não tem rede, e isso é uma garantia, não um acaso (`gv/component/main.go:147-149`). Uma
tool de vídeo que chama um modelo **não pode correr lá**. Há duas casas possíveis:

- **No nó, como tool que usa o Model Gateway.** Registada no RM com `RegisterCosting`; o efeito é
  uma chamada ao gateway. Herda a allowlist, o perfil, a comparação do modelo servido, as
  credenciais pelo broker e a contabilidade. É o que «custo e proveniência num só sítio» quer dizer.
- **Num componente à parte**, com chave própria para a OpenRouter. Duplica a contabilidade, as
  credenciais e a verificação do modelo servido.

A primeira é a recomendada (D3). Pede uma coisa que o repositório não tem: **uma tool cujo efeito
é uma chamada ao modelo**. Com que identidade a tool chama o gateway, e se o token do run serve,
está `[POR CONFIRMAR]` — é a primeira pergunta do ticket e pede um ADR.

### 3.3 O que o replay precisa de guardar

- **Para uma tool:** nada de novo. O resultado (texto) está no ledger; o replay usa-o e não chama
  a tool. Guardar o digest do media no resultado basta para saber, depois, o que foi lido.
- **Para o titular que vê:** o `prompt_hash` pode cobrir o **digest**, não os bytes. O replay
  verifica sem os bytes. Os bytes só fazem falta para **continuar** um run vivo (cada turno novo
  reenvia o media) e para responder «o que é que o modelo viu». Se os bytes desapareceram, a
  continuação tem de falhar com causa própria, nunca reenviar outra coisa.

## 4. O que as fases anteriores ensinaram

1. **Medir antes de construir.** A A4 desenhou uma cascata e a medição mostrou que não se
   justificava (acomp, §3 «A4 a A6»). Aqui: medir o OCR no banco antes de o pôr num run.
2. **Base64 no prompt não serve.** Com documentos de texto, o modelo recebeu base64 e desistiu
   (`sbx/mediated.go:124-127`). Com uma imagem seria igual ou pior.
3. **Um só ficheiro de leitura por run, com `cap:fs.read` armada** (série `v0151t`). Uma tool de
   media que partilhe a capability herda a regra.
4. **O binário não atravessa o `aos-orq`** (`orq/saida_por_referencia.go:224-226`). O desenho
   respeita-o: entre nós passa texto.
5. **O conteúdo selado não se lê sem a identidade do dono** (acomp, §5, 2026-10-13). As saídas do
   OCR medem-se no banco, que guarda respostas a pedido (acomp, §3, AOS-525 #484), e não em
   produção.
6. **O proxy não é assinado** (`deploy/server/litellm/config.yaml:11-15`). O modelo de vídeo por
   trás de um alias muda sem assinatura; a comparação do modelo servido é a única defesa. Se ela
   corre para chamadas feitas por uma tool está `[POR CONFIRMAR]`.
7. **Gates e smoke só provam o que corre sobre o mesmo transporte** (defeitos específicos do
   JetStream passaram dez gates). O smoke da tool nova corre sobre JetStream.
8. **Desligado por omissão, `observe` antes de `enforce`.** Uma tool só existe se estiver no
   `tools.json`; o executor novo só arranca se configurado.
9. **Não chamar provado ao que só foi medido com simulações.** A A3 fechou com «um modelo mau é
   recusado» provado só com modelos simulados. Aqui: o critério de cada sub-fase mede-se com
   media real (sintético, sem dados pessoais) e modelo real.

## 5. O caminho mínimo, pela ordem em que dá valor

### 5.1 «A5-ocr-medição» — o OCR serve? (no banco, sem tocar no nó)

Um conjunto pequeno de imagens sintéticas (faturas, recibos, capturas de ecrã, uma foto inclinada),
com os factos esperados escritos à partida, como a bateria do AOS-525. Correr o motor candidato
(D2) e medir: factos presentes, tempo por imagem, memória. Com a mesma regra de factos da bateria
(sem contar acentos, maiúsculas nem espaços — acomp, §3, AOS-525 #485).

**Pergunta que responde:** o OCR local acerta o suficiente nos documentos que interessam? Se não,
a D2 muda antes de haver código no nó.

### 5.2 «A5-ocr» — a tool `ocr_read`

- Verbo `ocr` no guest (`gv/guest/main.go:69-88`), com a mesma regra de caminho do `read`.
- O motor de OCR dentro do rootfs do sandbox (`gv/component/main.go:128-140`), versão fixa.
- O resultado é texto, mais o `sha256` do ficheiro lido e a versão do motor, num formato fixo.
- Entrada `ocr_read` no `tools.json`: `egress: none`, `mutation: none`, `reversible`, recurso
  `doc://{doc_id}`, região do board.
- Nada no kernel, no gateway nem no `aos-orq`.

**Valor:** imagens legíveis em qualquer run, com qualquer titular. É provavelmente **um PR**, mais
o da revisão.

### 5.3 «A6-custo-tools» — o custo de uma tool fica no run

O custo que a tool reporta (`RegisterCosting`) passa a ser gravado de forma durável, por run e
passo, e somado ao consumo do run, ao lado dos turnos. Uma tool sem custo continua a gravar zero
**declarado**. É pré-requisito do vídeo. Deve conversar com o que o ramo `AOS-527` estiver a fazer
`[POR CONFIRMAR]`. Um a dois PR.

### 5.4 «A5-porta-media» — a porta do gateway aceita media no pedido

- `port.Message` ganha partes de media **no pedido** (imagem, vídeo), com os bytes resolvidos no
  último momento a partir de uma referência com digest; MINOR 1.12.0.
- As respostas continuam só texto: `ErrContentPartNotText` fica (`gw/port/port.go:161-170`).
- Os bytes nunca vão a eventos nem a spans; só o digest e o tamanho.
- Uma capacidade nova no perfil da rota (`image`, `video`), validada como a `tools`
  (`gw/route_profile.go:97-101`). Uma rota sem a capacidade recusa o pedido antes de sair.

Serve o vídeo (5.5) e, mais tarde, a «A5-titular» (5.6). Um PR médio.

### 5.5 «A6-video» — a tool `video_describe`

- Tool registada no RM com `RegisterCosting`; o efeito é uma chamada ao gateway com o vídeo como
  parte do pedido, a um alias próprio (D3).
- Devolve texto (descrição ou transcrição), o digest do vídeo e o modelo servido.
- O custo vem do gateway (tabela de preços) e grava-se por 5.3.
- `egress: internal` (o proxy), não `external`: a tool fala com o gateway, o gateway com o proxy.
- ADR novo: uma tool cujo efeito é uma chamada ao modelo — identidade, custo, proveniência,
  taint do resultado.

Dois a três PR, mais um da allowlist re-assinada e da entrada na tabela de preços.

### 5.6 «A5-titular» — o titular recebe a imagem (só com caso)

- Um segmento novo no tail: referência a media com digest, que entra no `prompt_hash` pelo digest.
- A projecção resolve o digest em bytes, conferidos, na mensagem `user`.
- Replay pelo digest; continuação sem bytes falha com causa própria.
- Como o media entra num run sem passar por uma tool (D4): um campo novo no `POST /runs`, com a
  referência e o digest.
- ADR novo; emenda ao ADR-036.

Três a cinco PR. **Não se abre sem um caso que as tools não sirvam.**

### O que fica de fora, de propósito

Geração de media; áudio como fase; upload por API; escolha de modelo por capacidade; streaming;
media nas respostas do modelo; a opção (b) da A0.5 (o nó consumidor resolve a referência).

## 6. A quem isto toca, e o que não muda

| Componente | 5.2 OCR | 5.3 custo | 5.4 porta | 5.5 vídeo | 5.6 titular |
|---|---|---|---|---|---|
| `tools.json` e guest do gVisor | sim | — | — | sim (`tools.json`) | — |
| RM | — | leitura do custo | — | registo da tool | — |
| Agent Runtime (prompt, replay) | — | consumo do run | — | — | sim |
| Model Gateway | — | — | sim | alias e preço | sim |
| `aos-orq` | — | — | — | — | talvez |
| ADR | — | — | — | novo | novo + emenda ADR-036 |

O OCR (5.2) não muda o kernel, o gateway, o prompt, o replay nem o `aos-orq`. É a razão principal
para vir primeiro.

## 7. Decisões para o dono

Nove decisões. Em cada uma: as opções, a recomendação e o que acontece se ficar por tomar. Os
números que aparecem como limiares são **propostas**, não medições.

### D1 — Ferramentas primeiro, ou o modelo multimodal primeiro?

| Opção | O que é | Custo | Serve que titulares |
|---|---|---|---|
| (a) Ferramentas primeiro | OCR como tool; depois vídeo como tool pelo gateway; o titular a ver só com caso; escolha por capacidade só com caso | 5.1 a 5.5: seis a nove PR, o primeiro valor ao segundo PR | Todos |
| (b) Titular multimodal primeiro | 5.4 e 5.6 já; tools depois | Sete a dez PR antes do primeiro valor; prompt, replay e ADR | Só os que aceitam imagem |
| (c) Adiar A5 e A6 | Nada se faz | Nenhum | — |

**Recomendação: (a)**, na ordem da conclusão: (1) OCR como tool; (2) vídeo como tool que chama um
modelo capaz de vídeo, pelo gateway; (3) a A5 propriamente dita só quando o modelo precisar de ver
a imagem ele próprio; (4) escolha por capacidade só se aparecer um caso que as tools não cubram
(D9). Razões: o OCR é mais barato, mais rigoroso e auditável do que um LLM a ler a imagem (o texto
lido fica selado; o que o modelo «viu» não); serve qualquer titular; e o replay já existe. É a
orientação já discutida com o dono.

*Se não decidir:* nada se constrói. A5 e A6 ficam «por começar», com este desenho.

### D2 — Que motor de OCR, e onde corre

| Opção | Onde corre | Rede | Custo por chamada | Riscos |
|---|---|---|---|---|
| (a) Motor local, dentro do gVisor | No executor de hoje, verbo novo | Nenhuma | Zero em dinheiro; CPU e memória do servidor | O rootfs passa a levar o motor e os dados de língua; 45 s e 1 GB por chamada podem não chegar a imagens grandes |
| (b) Motor local num contentor próprio | Um serviço novo, sem rede de saída | Interna | Zero em dinheiro | Mais um serviço a operar; precisa de executor novo no nó |
| (c) API de OCR externa | Fornecedor externo | Externa | Por página `[POR CONFIRMAR]` | Credencial nova pelo broker; egress externo; o documento sai do servidor |
| (d) O próprio LLM a ler a imagem | Pelo gateway | Interna | Por token de imagem `[POR CONFIRMAR]` | Menos rigoroso; inventa texto ilegível; é a 5.5 com outro media |

**Recomendação: (a)**, com um motor de código aberto e versão fixa; **qual motor decide-se na
medição 5.1** (Tesseract é o candidato óbvio; a qualidade em português e o tamanho no rootfs estão
`[POR CONFIRMAR]`). Se a 5.1 mostrar que 45 s ou 1 GB não chegam, sobe-se o limite da chamada
`ocr` antes de passar a (b).

Duas sub-perguntas:
- **Capability.** `cap:fs.read`, a mesma da `doc_read` (uma leitura por run, §2.2), ou uma
  própria. Recomendação: **a mesma**. Um nó que lê uma imagem é um nó de leitura; a regra de uma
  leitura por run é uma defesa medida, e não se abre uma excepção para a primeira tool nova.
- **Formatos.** PNG, JPEG e PDF digitalizado `[POR CONFIRMAR]` o suporte do motor. Recomendação:
  PNG e JPEG primeiro; PDF quando houver um.

*Se não decidir:* a 5.1 não tem motor para medir.

### D3 — O modelo de vídeo: qual, por que via, tool ou rota

| Pergunta | Opções | Recomendação |
|---|---|---|
| Tool ou rota | (a) tool que chama o modelo; (b) rota: o titular recebe o vídeo | **(a).** O Haiku não aceita vídeo (indicação do dono; o repositório não o diz `[POR CONFIRMAR]`). (b) obrigava a escolher modelo por capacidade (D9) |
| Por que via | (a) pelo Model Gateway, com alias próprio; (b) componente à parte com chave própria | **(a).** Custo, allowlist, credencial e modelo servido num só sítio (§3.2) |
| Que alias | (a) um alias novo; (b) reusar o `gpt-4o-mini`, hoje sem tráfego (acomp, §5, 2026-10-14) | **(a).** O `gpt-4o-mini` tem um perfil de chat com tools e é o recuo natural do titular. Um alias novo exige re-assinar a allowlist (`gw/policy/allowlist/allowlist_policy.json:19-24`) e uma imagem nova do nó |
| Que modelo | Um modelo capaz de vídeo pela OpenRouter (por exemplo um Gemini) | **Escolhe o dono**, como na A3. Que modelos aceitam vídeo pela OpenRouter, e por que forma (URL ou bytes), está `[POR CONFIRMAR]`; o banco de ensaio mede-o antes |
| Exame | Passa pelo exame da A3? | **Não pelo exame de hoje**: ele mede tool calling, e esta rota não oferece tools. Pede uma bateria pequena própria (vídeos sintéticos, factos esperados) |

*Se não decidir:* a 5.5 não tem modelo nem alias. O OCR (5.2) não depende disto.

### D4 — Como o media entra no sistema, onde fica e por quanto tempo

| Opção | O que é | Quem guarda | Cifra |
|---|---|---|---|
| (a) Pasta semeada, como os documentos | O operador põe o ficheiro em `deploy/server/gvisor/seed` (montada só de leitura — `docker-compose.prod.yml:945-946`) | O operador | Nenhuma; o texto lido é cifrado por titular no log (`nó/bootstrap.go:1702`, `:1721`) |
| (b) Upload por API para um armazém do nó | `POST` com o ficheiro; guardado por digest no volume do nó | O nó | Cifrado por titular; apagável por DSAR |
| (c) URL externa | A tool vai buscar | Ninguém | — |

**Recomendação: (a) agora; (b) quando houver um fluxo de utilizador real.** A (c) fica rejeitada:
pôr egress externo numa tool por causa da entrada é a classe de maior risco
(`packages/platform/registry/domain/contract.go:17-18`).

Avisos de (a), a decidir se se aceitam:
- **O crypto-shredding não alcança o media**: o ficheiro na pasta não é cifrado; só o texto lido
  o é. Apagar o media é apagar o ficheiro, à mão.
- **A retenção é a do operador.** Recomendação: um ficheiro de media fica enquanto houver um run
  que o cite e o dono o queira; o digest no resultado diz qual era.
- **O nó também lê a pasta semeada para memória**, se `AOS_SANDBOX_SEED_DIR` estiver definida
  (`nó/sandboxwiring.go:186-213`): um vídeo grande lá dentro ocupa memória do nó no arranque. Em
  produção a variável vem vazia por omissão (`docker-compose.prod.yml:359`); o valor no servidor
  está `[POR CONFIRMAR]`.

*Se não decidir:* vale (a), que é o que já existe.

### D5 — Replay de conteúdo binário

| Opção | O que se guarda | O replay faz | Custo |
|---|---|---|---|
| (a) O texto da tool e o digest do media | O resultado (como hoje) + o digest | Usa o resultado do ledger; não chama a tool | Nenhum novo |
| (b) Também os bytes do media, num armazém por digest | + o media | Igual a (a); permite responder «o que foi lido» | Armazém novo; o modo 3 está desenhado mas não ligado (`rt/replay/payload_store.go:11-35`) |
| (c) Re-correr a tool no replay | Só o pedido | Chama a tool outra vez | Contraria a execução durável: re-executa um efeito, paga outra vez, e o media pode ter mudado |

**Recomendação: (a) para as tools; (c) rejeitada.** Para a «A5-titular»: o digest no
`prompt_hash`, os bytes resolvidos do sítio da D4; sem bytes, o replay verifica e a continuação
falha com causa própria. (b) só se o dono quiser guardar o media para auditoria.

*Se não decidir:* (a), que não pede trabalho.

### D6 — O custo das tools

| Pergunta | Opções | Recomendação |
|---|---|---|
| Contabilizar como | (a) por chamada, medido pela tool, gravado no run; (b) só no span, como hoje; (c) estimado à cabeça | **(a)** (§5.3). (b) é o que existe e não cumpre «custo contabilizado» |
| A tool de OCR local | Custo zero declarado, ou uma estimativa de CPU | **Zero declarado.** Não se inventa um preço |
| Tecto por chamada | (a) uma reserva fixa por chamada de vídeo, antes do efeito; (b) sem tecto, só a medição | **(a).** O custo de um vídeo só se sabe depois; a reserva fixa limita o pior caso. O valor propõe-se depois da medição do banco |
| Tecto do dia | O tecto da conta na OpenRouter, do dono | **Fica do dono**, como nas medições da A4 (acomp, §4, 2026-10-12) |
| Preço | Entrada na tabela de preços para o par do alias novo | **Obrigatória** antes de ligar: sem par na tabela, o gateway recusa a chamada (`nó/model_pricing_env.go:30-38`) ou conta zero |

*Se não decidir:* o vídeo (5.5) não abre. O OCR (5.2) não depende disto.

### D7 — O critério de cada fase: o que conta como provado

| Opção | O que é |
|---|---|
| (a) Critérios originais à letra | A5 fecha só quando o titular recebe uma imagem com replay; A6 só quando uma modalidade é gerada |
| (b) Reescrever A5 e A6 para caberem nas tools | A5 = «o conteúdo de uma imagem chega a um run»; A6 = «uma tool chama um modelo de outra modalidade» |
| (c) Sub-fases com critério próprio; os originais ficam | A5.1, A6.1 provadas pelas tools; A5 e A6 originais à espera de caso |

**Recomendação: (c).** (b) seria mudar a meta para a acertar. Propostas de critério, a medir no
banco de ensaio e depois numa série em produção autorizada pelo dono:

| Sub-fase | Critério proposto | Banco de ensaio | Produção |
|---|---|---|---|
| **A5.1 — OCR como tool** | Planos que lêem uma imagem por `ocr_read` entregam os factos da imagem ao nó seguinte, com o digest no resultado e o replay a reproduzir todos os `prompt_hash` | Bateria de imagens sintéticas, factos presentes em pelo menos 90% `[proposta]` | Série de 20 planos com imagens sintéticas; 0 verdes falsos; replay de todos |
| **A6.1 — vídeo como tool** | Um run descreve um vídeo com o custo da chamada gravado no passo da tool, igual ao que o gateway contou, e com o modelo servido e o digest do vídeo selados | Bateria pequena de vídeos sintéticos | 5 planos; custo do run = soma de turnos e tools |
| **A5 — titular** (original) | Como está | — | Só com caso |
| **A6 — geração** (original) | Como está | — | Só com caso |

*Se não decidir:* vale (a), e nenhuma das tools fecha uma fase — o trabalho faz-se, mas a tabela
das fases não se move.

### D8 — O que fica de fora

| Item | Recomendação | Gatilho para reabrir |
|---|---|---|
| Áudio | **Fora agora.** Se vier, é uma tool como a de vídeo, com outro modelo e outra entrada no `tools.json` — o executor é o mesmo | Um caso real com áudio |
| Geração de imagem, áudio ou vídeo | **Fora** | Um caso real; seria uma tool cujo resultado é media, e aí a A0.5 (b) volta à mesa, porque o binário não atravessa o `aos-orq` |
| Upload por API | **Fora** (D4) | Um fluxo de utilizador real |
| Media nas respostas do modelo | **Fora**; `ErrContentPartNotText` fica | — |
| Streaming | **Fora** (já estava: acomp, §6) | — |

*Se não decidir:* fica tudo de fora até haver caso, que é o mesmo que a recomendação.

### D9 — Escolha de modelo por capacidade

Se um passo precisasse de um modelo que vê vídeo e o titular não vê, o nó podia escolher o modelo
pela **capacidade que a entrada exige**: uma regra determinista, lida da entrada (há um vídeo ⇒
rota com `video`). **Não é a cascata por custo** da A4: aquela escolhia pelo preço e pela
dificuldade estimada, e a medição não a justificou; esta escolhe pelo que o modelo **consegue**
fazer, e só quando o titular não consegue.

| Opção | O que pede |
|---|---|
| (a) Agora | Mais do que titular e candidato; **emenda ao ADR-041** (no máximo um candidato, escolha por plano — `ADR-041:23-30`); mais um alias na allowlist; um modelo novo a servir runs inteiros, com tools |
| (b) Só com um caso que as tools não cubram | Nada agora |

**Recomendação: (b).** Com o vídeo como tool, o titular nunca precisa de ver o vídeo: a tool vê e
devolve texto. O único caso que a (a) cobre e as tools não é «o modelo precisa de raciocinar
**sobre** o media, não só sobre o texto dele» — e esse ainda não apareceu.

*Se não decidir:* (b).

## 8. Proposta de tickets

Sem número. A numeração faz-se depois das decisões, com `scripts/ci/sessoes.py reservar`.

| Rótulo | Título | Uma linha | Depende de |
|---|---|---|---|
| «A5-ocr-medição» | O banco de ensaio mede um motor de OCR com imagens sintéticas | Bateria de imagens com factos esperados; factos presentes, tempo e memória por imagem; relatório | D1, D2 |
| «A5-ocr» | A tool `ocr_read`: verbo `ocr` no gVisor, motor no rootfs, digest no resultado | Nada no kernel nem no gateway; entrada no `tools.json` só pelo dono | «A5-ocr-medição» |
| «A6-custo-tools» | O custo medido de uma tool fica gravado no run e conta no consumo | `RegisterCosting` → registo durável por passo; zero declarado nas tools sem custo | D6; o que o ramo AOS-527 fizer |
| «A5-porta-media» | A porta do gateway aceita partes de media no pedido, por referência com digest | MINOR 1.12.0; capacidades `image`/`video` no perfil; os bytes nunca em eventos | D4, D5 |
| «A6-video» | A tool `video_describe`: uma tool cujo efeito é uma chamada ao modelo | ADR novo; alias e preço; allowlist re-assinada; bateria própria | D3, D6; «A6-custo-tools», «A5-porta-media» |
| «A5-titular» (com caso) | O titular recebe uma imagem por referência, com replay pelo digest | Segmento de media no tail; projecção; continuação sem bytes falha com causa; ADR novo | Um caso; «A5-porta-media» |

**Quantos PR, com honestidade.**
- **Só o OCR** (5.1 e 5.2): **dois a três PR**, mais um de correcções da revisão adversarial.
- **OCR e vídeo** (5.1 a 5.5): **sete a dez PR**. O maior e mais arriscado é o «A6-video»: é a
  primeira tool que chama um modelo, e toca na identidade e no custo.
- **A «A5-titular»**, se vier: mais três a cinco.

**Ordem.** «A5-ocr-medição» sozinho, e lido antes de continuar. Depois «A5-ocr». O vídeo só depois
de o OCR estar em produção e de D3 e D6 tomadas.

## 9. O que este desenho não sabe

- **Se há um caso real.** Não encontrei no repositório nenhum pedido de run com imagem ou vídeo
  `[NÃO ENCONTREI]`. Qual é o primeiro documento que precisa de OCR é pergunta para o dono.
- **A qualidade do OCR em documentos portugueses**, o tempo por imagem no gVisor e se 45 s e 1 GB
  chegam. É o que a 5.1 mede.
- **Se o Haiku aceita imagens pela OpenRouter e pelo LiteLLM fixado**, e em que forma. A
  indicação é do dono; o repositório não o regista `[POR CONFIRMAR]`.
- **Que modelos aceitam vídeo pela OpenRouter**, com que limite de tamanho e duração, por URL ou
  por bytes, e se o LiteLLM fixado passa partes de vídeo sem as deitar fora `[POR CONFIRMAR]`.
- **Como a OpenRouter conta uma imagem ou um vídeo** — tokens, segundos, por chamada — e se o
  custo vem na resposta `[POR CONFIRMAR]`. A tabela de preços só sabe tokens.
- **Se o fornecedor aceita imagens dentro de uma mensagem `tool`**, ou só em `user`
  `[POR CONFIRMAR]`. Importa só para a «A5-titular».
- **Com que identidade uma tool chama o gateway** (§3.2), e se a comparação do modelo servido
  corre para essa chamada `[POR CONFIRMAR]`.
- **Se um resultado de tool tem tecto de tamanho no nó.** Procurei e não encontrei um tecto
  explícito no caminho da sandbox `[NÃO ENCONTREI]`; o tecto conhecido é o do `aos-orq`
  (128 KiB por payload).

## 10. O que esta nota não verificou

- O servidor de produção: nada foi lido lá. O conteúdo da pasta semeada, a configuração real do
  proxy, o valor de `AOS_SANDBOX_SEED_DIR` e de `AOS_PRIVILEGED_CAPS` vêm do repositório e do
  acompanhamento.
- O que o ramo `feature/AOS-527-custo-do-modelo` vai mudar: na base lida tem só os commits da base.
- O percurso completo de `Decision.CostMicroUSD` até ao span `aos.activity`
  (`rt/activity/dispatch.go:243`, `:307`): lido nos pontos citados, não linha a linha.
- Os selos por chamada do gateway (`gw/gateway.go:650-760`): lidos pelos nomes das funções, não
  linha a linha. Que o selo de uma chamada feita por uma tool leve o run e o passo está
  `[POR CONFIRMAR]`.
- O módulo `packages/platform/registry` além do tecto de egress: o catálogo assinado de tools e a
  revalidação foram lidos pela superfície.
- Capacidades e preços de modelos: nenhum foi verificado fora do repositório, e nenhum aparece
  aqui como facto.
