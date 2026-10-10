# Banco de ensaio da fronteira runtime↔modelo (AOS-512)

Ferramenta de **medição**, fora do nó. Corre uma bateria fixa de casos sintéticos contra **uma
rota** e escreve um relatório de **taxas** — só contagens, fichas em vocabulário fechado e
identificadores de caso. Não decide nada: não aceita nem recusa um modelo.

> **O ensaio não toca em produção.** Nenhum modo fala com o nó de produção, com a fila ou com
> o Vault. Os documentos são sintéticos (pasta `bateria/`). O binário `aos-ensaio` não faz
> parte do nó `aos`, não vai na imagem de produção, e o modo com modelo real recusa correr em
> CI.

Ticket: `specs/EPIC-08_Observabilidade_Evals.md` (`AOS-512`). Desenho:
`docs/reports/desenho-a2-estado-opaco-2026-10-07.md` §5. Procedimento para a corrida com
modelo real: `docs/runbooks/PROC-BANCO-DE-ENSAIO.md`.

## O que é medido

O pedido não é imitado: cada caso é um run do Agent Runtime real, com as tools mediadas pelo
Reference Monitor, e o pedido sai pelo Model Gateway de produção com a projecção nativa do
tail. O banco acrescenta duas coisas à volta: um decorador da porta do gateway (as variantes de
protocolo e a observação) e um transporte HTTP que conta cada pedido contra o tecto do dia
**antes** de o enviar.

O nó de ensaio é mínimo — sem PDP, WORM, sandbox nem `aos-orq`. Mede a fronteira com o modelo,
não a governação do nó.

| Taxa | Como se mede |
|---|---|
| Sem tool call na 1.ª tentativa (a tool call em texto) | O nó exige uma tool, o run não pediu nenhuma tool call nativa e parou com o motivo `stop`. **Não se lê texto.** |
| Nome da tool no texto | Heurística declarada, só para contagem: das anteriores, as que tinham no texto o nome exacto de uma tool oferecida. Nada corre por causa dela. |
| 2.º turno com tools aceite | O pedido com o `assistant` das tool calls e a mensagem `tool` teve HTTP 200 e deu um turno. |
| Factos ausentes | Substituto determinista da recusa do objectivo: os números e nomes exactos do documento sintético estão ou não na saída. |
| Resposta vazia, cortada | Os desfechos `empty_output` e `truncated` do kernel. |
| Erro do provider | Pedidos sem HTTP 200 nem 429, e a distribuição dos códigos. |
| Limite de taxa (429) | Contado à parte: tanto o dá o fornecedor como o próprio proxy, e pelo código não se distinguem. |
| Tipos de erro | Os pedidos com resposta sem 200, por tipo, em vocabulário fechado: `chave_recusada`, `saldo_insuficiente`, `limite_de_ritmo`, `modelo_desconhecido`, `outro`. Lê-se o `error.type` do corpo contra uma lista fechada; a `message` nunca é guardada. |
| Recuperado à 2.ª ou 3.ª tentativa | Nova tentativa do nó, como o `aos-orq` (com o aviso do kernel quando faltou a tool call). |
| Forma das respostas | A ficha do AOS-507 (`port.ProbeResponseShape`, pelo adaptador de produção), agregada por classe. |

Cada taxa sai com numerador, denominador e intervalo de confiança a 95% (Wilson).

## A bateria

`bateria/casos.json` e os documentos `bateria/*.txt`. T1 leitura com uma tool; T2 leitura e
resumo; T3 nó sem tools com `plan_input`; T4 duas tool calls no mesmo turno; T5 tool negada e
continuação; T6 argumentos grandes. Todos os documentos abrem pela marca
`DOCUMENTO SINTETICO AOS-512`; a bateria não carrega sem ela. O digest da bateria vai no
relatório.

```
go run ./cmd/aos-ensaio bateria
```

## Os três modos

Todos os comandos correm a partir de `packages/qa/banco-ensaio`.

### `falso` — CI, sem rede e sem Docker

O fornecedor é o provider falso do banco, em processo, com um roteiro fixo. É o que corre em
cada PR (gate `test`), com taxas esperadas exactas presas por teste.

```bash
go run ./cmd/aos-ensaio falso --saida /tmp/ensaio
go run ./cmd/aos-ensaio falso --experiencia separadores --saida /tmp/ensaio
```

```powershell
go run .\cmd\aos-ensaio falso --saida $env:TEMP\ensaio
go run .\cmd\aos-ensaio falso --experiencia separadores --saida $env:TEMP\ensaio
```

### `proxy` — a imagem real do proxy à frente do provider falso (Docker)

Gate **opcional**, fora do `run.sh`; sem Docker ou sem a imagem salta e declara o salto.

```bash
bash scripts/ci/banco-ensaio-proxy.sh     # a partir da raiz do repositório
make ci-banco-ensaio-proxy
```

```powershell
bash scripts/ci/banco-ensaio-proxy.sh     # no Git Bash, a partir da raiz do repositório
```

O script compila um binário Linux do banco (o provider falso corre num contentor) e corre o
teste `TestAOS512_ProxyReal_ABateriaAtrasDoProxy`.

### `real` — o modelo real, pelo proxy efémero (Docker)

**Só o dono o corre**, com o seu ficheiro de chaves. Ver o runbook. Resumo:

```powershell
cd C:\Jimy\AOS\packages\qa\banco-ensaio
go build -o $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe .\cmd\aos-ensaio

# 1. Só o plano: valida o ficheiro, os tectos e o contador. Não envia nada.
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor kimi --experiencia separadores --so-plano

# 2. A corrida.
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor kimi --experiencia separadores
```

```bash
cd packages/qa/banco-ensaio
go build -o "$HOME/.aos-ensaio/aos-ensaio" ./cmd/aos-ensaio
"$HOME/.aos-ensaio/aos-ensaio" real --chaves "$HOME/.aos-ensaio/chaves.env" --fornecedor kimi --experiencia separadores --so-plano
"$HOME/.aos-ensaio/aos-ensaio" real --chaves "$HOME/.aos-ensaio/chaves.env" --fornecedor kimi --experiencia separadores
```

O modo real levanta a imagem de produção do proxy num contentor só para a corrida, com uma
rota para o fornecedor (`openai/<modelo>` para o Kimi, `anthropic/<modelo>` para a Anthropic,
`openrouter/<autor>/<modelo>` para a OpenRouter), e desmonta-a no fim.

**A sonda.** Antes do primeiro caso, o modo real faz um único pedido mínimo ao modelo. Conta no
tecto do dia como um pedido (o plano anuncia «mais 1 de sonda») e fica no relatório, no campo
`sonda` — não é uma observação e não entra em taxa nenhuma. Se não der 200, a corrida não
começa: exit 4, relatório sem observações, e a causa em vocabulário fechado
(`chave_recusada`, `saldo_insuficiente`, `limite_de_ritmo`, `modelo_desconhecido`, `outro`).
O `--so-plano` não envia nada, nem a sonda.

### `limpar` — remover o que ficou de uma corrida anterior

```powershell
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe limpar --chaves $env:USERPROFILE\.aos-ensaio\chaves.env
```

Remove os contentores e as redes `aos512-*` que existirem e, se o processo que a criou já não
existir, a trava do contador. Não envia nada. Ver «O proxy efémero e os órfãos», abaixo.

## Opções

| Opção | Modos | O que faz |
|---|---|---|
| `--experiencia bateria\|separadores` | todos | `bateria` (omissão): T1 a T6 num braço. `separadores`: quatro braços sobre T1, 53 amostras por braço, um turno e um pedido por amostra — 212 pedidos. |
| `--amostras N` | todos | Passagens pela bateria (omissão 1), ou amostras por braço nos separadores. |
| `--semente N` | todos | Fixa a ordem dos braços dentro de cada bloco. Vai no relatório. |
| `--braco A\|B\|C\|D` | todos | O braço da bateria (omissão `A`). |
| `--saida PASTA` | todos | Onde escrever o relatório. |
| `--pausa DURACAO` | todos | Intervalo entre dois passos (por exemplo `2s`). |
| `--silencioso` | todos | Não mostra o resumo, só os caminhos. |
| `--roteiro a,b,…` | falso, proxy | O roteiro do provider falso. |
| `--tecto-pedidos N --contador F` | falso, proxy | Ensaia o próprio tecto. |
| `--binario-do-falso F` | proxy | Binário Linux do banco, para o contentor do provider falso. |
| `--chaves F` | real | Caminho do ficheiro de chaves. Obrigatório (ou `AOS_ENSAIO_CHAVES`). |
| `--fornecedor kimi\|anthropic\|openrouter` | real | O fornecedor. |
| `--modelo M` | real | Um dos modelos do ficheiro (omissão: o primeiro). |
| `--precos F` | real | Tabela de preços. Obrigatória quando há tecto em dólares. |
| `--perfil F` | todos | **Perfil de rota candidato** (AOS-513), em JSON, na forma dos campos de um perfil do gateway: `requested` (tem de ser `rota-de-ensaio`), `expected_model`, `wire_class`, `capabilities` e, opcionais, `params` (`thinking`, `reasoning_effort`, `max_tokens`), `projection_version` e `devolver`. A leitura é fechada: uma chave ou um valor fora do conjunto recusa a corrida antes de qualquer pedido. O digest do perfil vai no relatório (`digests.perfil`) e entra no digest da configuração. É assim que um perfil se qualifica **antes** de entrar na tabela de perfis do nó. |
| `--estado exige\|proibe` | falso, proxy | O provider falso do **estado opaco** (AOS-516) em vez do do roteiro: emite raciocínio assinado em cada turno com tools e exige-o de volta (no modo falso, byte a byte, pelo falso exigente do AOS-515), ou recusa qualquer estado. No modo proxy pede `--perfil` com `expected_model` `anthropic/<modelo>`: a rota do proxy é essa, e o falso fala o wire de mensagens da Anthropic. |
| `--forma-do-falso openrouter` | falso, proxy | Só com `--estado`: o falso emite e exige a forma da **OpenRouter** (`reasoning` e `reasoning_details` com assinatura, no topo da mensagem). No modo proxy o `expected_model` do perfil é então `openrouter/<autor>/<modelo>` ou `openai/<autor>/<modelo>`. |
| `--turnos-do-falso N` | falso, proxy | Turnos com tool call do provider falso do estado (omissão 2). |
| `--host-esperado HOST` | todos | Só com um perfil que devolve estado: o host do endpoint que o proxy deve declarar ter servido. Sem ele compara-se só o modelo servido. O host não vai para o relatório. |
| `--so-plano` | real | Valida tudo, mostra o destino da chave e quantos pedidos faria, e não envia nenhum. |
| `--destino-fora-da-lista HOST` | real | Aceita um destino da chave que não é um host do fornecedor. `HOST` tem de ser exactamente o host do ficheiro; o `https` continua a ser exigido. |
| `--reconstruir-contador` | real | Recria um contador desaparecido a partir dos relatórios de hoje. |

No modo real **não há `--contador`**: o contador é sempre `contador.json`, ao lado do ficheiro
de chaves. Uma flag que apontasse para outro ficheiro punha a contagem do dia a zero.

### Variáveis de ambiente

| Variável | Efeito |
|---|---|
| `AOS_ENSAIO_CHAVES` | **Caminho** do ficheiro de chaves, quando não vem por `--chaves`. Nunca leva uma chave. |
| `CI`, `GITHUB_ACTIONS` | Se alguma estiver definida, o modo `real` recusa (exit 5), antes de ler seja o que for. |
| `AOS_BANCO_PROXY`, `AOS_BANCO_FALSO_BIN` | Só do teste do gate `banco-ensaio-proxy`; quem as define é o script. |
| `AOS_BANCO_PROXY_REQUIRED=1` | Torna vermelho o salto do gate `banco-ensaio-proxy`. |

O banco não lê mais nenhuma variável, e nenhuma destas chega ao nó `aos`.

### Códigos de saída

| Código | Significado |
|---|---|
| 0 | A corrida completou e o relatório foi escrito. |
| 2 | Argumentos inválidos. |
| 3 | Recusada antes de arrancar: ficheiro de chaves, tecto, preço, contador, ou a corrida não cabe no tecto. Nenhum pedido saiu. |
| 4 | Parou a meio (`tecto_atingido`, `contador_inutilizavel`, `interrompida`, `chave_recusada`, `saldo_insuficiente`, `limite_de_ritmo`, `so_respostas_429`, `serie_de_429`) ou a sonda não a deixou começar (as três primeiras, mais `modelo_desconhecido` e `sonda_falhou`). O relatório parcial foi escrito e diz a causa. |
| 5 | Modo `real` pedido em CI. |
| 6 | Falta o Docker ou a imagem do proxy, ou o proxy não arrancou. |
| 1 | Outra falha. |

## O ficheiro de chaves, os tectos e o contador

O ficheiro de chaves é do dono e vive **fora do repositório**
(`%USERPROFILE%\.aos-ensaio\chaves.env`; o modelo está em `chaves.env.exemplo`, na mesma
pasta). Só o programa o lê, pelo caminho que lhe dão.

- **Recusas.** Sem ficheiro, com um campo obrigatório em falta ou ainda com o marcador do
  exemplo (`<…>`, inteiro ou só com um dos sinais), o modo real sai com 3 e uma mensagem que
  nomeia o **campo** — nunca o valor. Também recusa: um valor **entre aspas** (as aspas iriam
  para o fornecedor); um **campo repetido** (a mensagem diz o nome e os números das linhas —
  não é «ganha o último»).
- **Destino da chave.** A base do Kimi tem de ser `https`, sem utilizador, porta, query nem
  fragmento, e o host tem de estar na lista embutida no banco: `api.kimi.com`,
  `api.moonshot.ai`, `api.moonshot.cn`. A Anthropic não tem base no ficheiro: o destino é o
  endpoint por omissão do adaptador `anthropic` do proxy, e o banco di-lo. A OpenRouter também
  não tem base no ficheiro: o destino é fixo, `https://openrouter.ai`, escrito pelo banco e
  passado explícito ao proxy. Outro host só
  com `--destino-fora-da-lista <host exacto>`. O `--so-plano` e o início da corrida mostram
  `DESTINO DA CHAVE: https://<host>` — o host de um fornecedor público não é segredo; o caminho
  da base e as chaves continuam fora de todas as saídas.
- **Tectos.** `TECTO_PEDIDOS_DIA_KIMI`, `TECTO_PEDIDOS_DIA_ANTHROPIC`,
  `TECTO_USD_DIA_ANTHROPIC`, `TECTO_PEDIDOS_DIA_OPENROUTER` e `TECTO_USD_DIA_OPENROUTER` (os
  dois em dólares são opcionais; com um deles, `--precos` é obrigatório). Não há tecto por omissão: ausente, zero ou ilegível recusa o
  arranque. Um tecto escreve-se só com algarismos (`+500`, `5 00`, `1e3` são recusados) e tem
  máximo: 100 000 pedidos e 1000 USD por dia. As opções da linha de comandos não mexem nos
  tectos do modo real.
- **Contador.** `contador.json`, ao lado do ficheiro de chaves. Persistente, por fornecedor e
  por dia (UTC). Cada pedido é contado **antes** de ser enviado. Um contador que existe e não
  se lê recusa tudo — não recomeça do zero; repara-se à mão. Um contador que **desapareceu**,
  havendo relatórios de corridas reais de hoje na pasta, recusa também: só
  `--reconstruir-contador` o recria, com a soma dos pedidos desses relatórios.
- **Um processo de cada vez.** Ao abrir o contador o banco cria `contador.json.trava` (com o
  PID e a hora); um segundo processo recusa arrancar, com exit 3. A trava de um processo que
  morreu não se remove sozinha: `aos-ensaio limpar --chaves <ficheiro>` remove-a, depois de
  confirmar que o processo já não existe.
- **Chave recusada.** Se os três primeiros pedidos da corrida real forem todos 401 ou 403, a
  corrida aborta (`chave_recusada`, exit 4) em vez de gastar o tecto do dia.
- **Conta sem saldo e limite de ritmo.** Os dois chegam com HTTP 429; distingue-os o
  `error.type` do corpo, lido contra uma lista fechada (qualquer outro valor é `outro`; a
  `message`, que traz identificadores da conta, nunca é guardada). Se os três primeiros pedidos
  da corrida real forem todos 429, a corrida aborta com `saldo_insuficiente`, `limite_de_ritmo`
  ou — com um tipo desconhecido — `so_respostas_429`. Dez 429 seguidos a meio da corrida
  param-na com `serie_de_429`. A mensagem final diz o que fazer: carregar a conta (repetir não
  adianta), ou `--pausa`.
- **Antes de começar**, a corrida calcula o máximo de pedidos que pode fazer e recusa se não
  couber no que resta do dia.
- **Tecto em dólares.** Usa os tokens do `usage` e a tabela de `--precos`:

  ```json
  {"modelos": {"<nome do modelo>": {"entrada_micro_usd_por_mtok": 0, "saida_micro_usd_por_mtok": 0}}}
  ```

  (micro-USD por milhão de tokens: 3 USD por milhão escreve-se `3000000`). O banco não traz
  preços; quem os declara é quem corre, com a tabela do fornecedor à frente. Sem preço para o
  modelo, o arranque é recusado. O gasto é uma **estimativa**, e o relatório di-lo; como só se
  conhece depois de cada resposta, o tecto em dólares pode ser ultrapassado pelo custo de um
  pedido.

## O proxy efémero e os órfãos

Enquanto o contentor do proxy existir, **a chave do fornecedor está em claro no seu ambiente**
(`docker inspect`), e o proxy aceita pedidos — fora do contador — de quem lá for buscar a chave
mestra. Numa corrida que acaba normalmente (incluindo `Ctrl+C`, `SIGTERM` e, no Windows, o
fecho da janela, na medida em que o sistema dê tempo) o banco remove-o. Para o caso de o
processo morrer à força, a limpeza não depende dele:

- o contentor é criado com `--rm` e leva um **vigia**: o banco manda-lhe um sinal de vida de 10
  em 10 segundos, e se o vigia passar 90 segundos sem o ver — ou se passar o prazo máximo de
  vida, derivado do plano da corrida — mata o proxy e o contentor apaga-se;
- os modos `proxy` e `real` **varrem**, ao arrancar, os contentores e as redes `aos512-*` de
  corridas anteriores, e dizem quantos removeram;
- `aos-ensaio limpar` faz só essa varredura.

O proxy do ensaio **não tem a configuração de produção**: corre com `num_retries: 0` (um pedido
do banco é um pedido ao fornecedor — é o que faz o tecto contar o que sai), `drop_params: true`
e `disable_cooldowns: true` (sem isto, depois de um erro do fornecedor o proxy responde ele
próprio 429 aos pedidos seguintes). O relatório di-lo nos limites.

## O relatório

Dois ficheiros por corrida, na pasta `--saida` (omissão: `relatorios/` ao lado do ficheiro de
chaves no modo real; `~/.aos-ensaio/relatorios` nos outros):
`ensaio-<modo>-<experiência>-<data>.json` e `.txt`.

Campos do JSON: `data_utc`, `modo`, `experiencia`, `terminou`, `rota` (fornecedor, modelo,
digest), `regiao_de_processamento_declarada` (uma declaração do dono, sem efeito),
`protocolo` (projecção, layout, versão publicada por braço), `digests` (bateria, rota,
configuração), `plano` (amostras, semente, tentativas, turnos, casos, braços), `pedidos`
(previstos, enviados, tecto do dia, gastos, restantes), `custo` (estimativa), `sonda` (só no
modo real: o código HTTP e o resultado do pedido de sonda), `taxas` (com `http`, os códigos por
pedido, e `tipos_de_erro`, os pedidos sem 200 por tipo),
`por_braco`, `por_caso`, `comparacoes` (só nos separadores), `limites` (o que a corrida **não**
prova) e `observacoes` (uma linha por run, em vocabulário fechado).

**Não leva:** chaves, cabeçalhos, o endereço da rota (só o seu digest), nem um byte de texto de
pedidos, de respostas ou de raciocínio. Está preso por teste, com sentinelas em todos os campos
do ficheiro de chaves e no texto de todas as respostas do provider falso
(`TestAOS512_Real_SemSegredosNemTextoEmSaidaNenhuma`,
`TestAOS512_Real_SegredosNoCaminhoDeErroDoProxy`), e com um identificador de conta na
`message` dos erros do fornecedor (`TestAOS512_Real_SaldoERitmo_AMensagemDoFornecedorNaoSai`).

## A devolução do estado opaco (AOS-516)

Com um perfil candidato cujo `devolver` não seja `nunca`, o nó de ensaio compõe o gateway com a
captura do estado (`capture`), a governação da rota em `observe` e o layout 1.5.0, e a projecção
é a do perfil (1.3.0). Sem esse perfil nada disto se liga: os pedidos e o relatório são, byte a
byte, os de antes (`TestAOS516_SemPerfilQueDevolva_PedidosERelatorioSaoOsDeAntes`).

O relatório ganha, na corrida, por braço e por caso (`devolucao_do_estado`), só contagens:

| Campo | O que conta |
|---|---|
| `turnos_com_estado_capturado` | Turnos cuja resposta trouxe estado e a captura o guardou |
| `pedidos_com_turnos_anteriores` | Pedidos que levavam pelo menos um turno anterior com tool calls |
| `turnos_com_raciocinio_capturado` / `turnos_so_com_ids_capturados` | Os turnos capturados que trouxeram raciocínio ou assinatura, e os que só trouxeram ids de tool call (fora da medida) |
| `decididos_a_devolver` | Pedidos em que o gateway armou o estado de todos os turnos — uma **decisão**, antes do envio |
| `aceites_pelo_fornecedor` / `taxa_de_devolucao` | Dos pedidos que deviam levar raciocínio, os armados **e** respondidos com 2xx (critério P4) |
| `nao_devolvido_por_causa` | A causa, no vocabulário do AOS-515 (inclui `estado_de_rota_nao_provada`) |
| `recusas_por_falta_de_estado` | Pedidos que o gateway **não enviou** (`StateReturnError`); o run fecha `estado_nao_devolvido` |
| `pedidos_com_estado` | O que aconteceu no transporte aos pedidos com estado, por tentativa: `http_2xx`, `http_4xx` (sem o 429), `http_429`, `http_5xx`, `erro_de_transporte`, `nao_enviado` |

O veredicto está em `qualificacao_da_devolucao` (`cumprida`, `nao_cumprida`, `sem_raciocinio`,
`inconclusiva`, com as `razoes`): é esse campo que se lê.

Nos modos sem modelo real, `forma_no_fornecedor` diz com que **forma** as mensagens `assistant`
com tool calls chegaram ao provider falso — chaves e tipos de bloco, nunca valores. Um perfil de
exemplo para o Claude está em `perfis/claude-devolucao.exemplo.json` (sem chaves; o nome do
modelo é um marcador). O procedimento está em `docs/runbooks/PROC-BANCO-DE-ENSAIO.md`.

## A OpenRouter (AOS-516)

O Claude qualifica-se pela OpenRouter, só em ensaio (decisão do dono de 2026-10-10). Campos do
ficheiro de chaves: `OPENROUTER_API_KEY`, `OPENROUTER_MODELO` (na forma `<autor>/<modelo>` da
OpenRouter, **sem** o prefixo `openrouter/`), `TECTO_PEDIDOS_DIA_OPENROUTER` e, opcional,
`TECTO_USD_DIA_OPENROUTER`. O contador do dia é o do fornecedor `openrouter`. Perfil de exemplo:
`perfis/claude-openrouter.exemplo.json`; o `expected_model` é `openrouter/<OPENROUTER_MODELO>`,
que é o que o proxy declara ter servido (medido).

**A forma da rota foi medida** atrás da imagem fixada do proxy, com o provider falso na forma
da OpenRouter (`TestAOS516_ProxyReal_ADevolucaoPelaOpenRouter`, 2026-10-10; 22 pedidos e 10
turnos com tool calls por cenário):

| | `openrouter/<autor>/<modelo>` (escolhida) | `openai/<autor>/<modelo>` com a base da OpenRouter |
|---|---|---|
| `thinking` do perfil chega ao fornecedor | 22 de 22, com `type` e `budget_tokens` | 0 de 22 (o proxy deita-o fora) |
| `reasoning_effort` do perfil chega ao fornecedor | 22 de 22 | 0 de 22 |
| `reasoning` do perfil (`effort` ou `max_tokens`) chega ao fornecedor | 44 de 44 | 44 de 44 |
| Onde o gateway recebe o estado | `reasoning_content` em `message`; `reasoning` e `reasoning_details` em `provider_specific_fields` | igual |
| `reasoning_details` no segundo turno, sem `devolver_em` no perfil | 0 de 10 no topo; 10 de 10 dentro de `provider_specific_fields` | igual |
| `reasoning_details` no segundo turno, com `devolver_em: topo` | 60 de 60 no topo, intacto; o saco não volta | igual |
| `reasoning_details` posto à mão no topo da mensagem | chega com os valores intactos | igual |
| Modelo que o proxy declara ter servido | `openrouter/<autor>/<modelo>` | `openai/<autor>/<modelo>` |
| Extras do adaptador | `usage` no pedido; cabeçalhos `HTTP-Referer` e `X-Title` do LiteLLM | nenhum |
| Erros 401, 402, 429, 404 e 400 na forma da OpenRouter | passam com o código; `chave_recusada`, `saldo_insuficiente`, `limite_de_ritmo`, `modelo_desconhecido` | igual |

Escolheu-se `openrouter/`: é a única das duas em que **todas** as formas do parâmetro de
raciocínio do perfil chegam ao fornecedor.

**O perfil declara duas coisas para esta rota** (contrato da porta 1.10.0; ADR-040 §2.11 e
ADR-036 §2.8, emendas de 2026-10-10):

- `"devolver_em": "topo"` — os campos de raciocínio que o proxy entrega dentro de
  `provider_specific_fields` voltam no topo da mensagem `assistant`, e o saco não volta. Sem
  isto o gateway devolve-os ao sítio de onde vieram, e a OpenRouter não os lê.
- `"params": {"reasoning": {"effort": "medium"}}` (ou `{"max_tokens": N}`) — a forma em que a
  OpenRouter documenta o pedido do raciocínio.

Medido com o gate inteiro (`bash scripts/ci/banco-ensaio-proxy.sh`, 2026-10-10), rota
`openrouter/anthropic/claude-sonnet-4.5`, duas passagens pela bateria por cenário:

| Perfil | Pedidos | `reasoning_details` no topo, intacto | Parâmetro que chegou ao fornecedor | Veredicto |
|---|---|---|---|---|
| `topo` + `reasoning: {effort}` | 44, todos 200 | 60 de 60 turnos; 30 de 30 aceites | `reasoning.effort`, 44 de 44 | `cumprida` |
| `topo` + `reasoning: {max_tokens}` | 44, todos 200 | 60 de 60 | `reasoning.max_tokens`, 44 de 44 | `cumprida` |
| `topo` + `reasoning_effort` | 44, todos 200 | 60 de 60 | `reasoning_effort`, 44 de 44 | `cumprida` |
| `topo` + `thinking` | 44, todos 200 | 60 de 60 | `thinking` (`type`, `budget_tokens`), 44 de 44 | `cumprida` |
| sem `devolver_em` (controlo) | 12 com 200, 10 com 400 | 0 de 10 (10 de 10 só no saco) | `reasoning.effort`, 22 de 22 | `nao_cumprida` |
| `devolver: nunca` | 12 com 200, 10 com 400 | não foi | `reasoning.effort`, 22 de 22 | `nao_cumprida` |

Com `topo` o saco não chegou ao fornecedor em nenhum turno. Posto à mão no topo **e** no saco,
chega nos dois sítios: por isso, com `topo`, o gateway não manda o saco. Pela rota `openai/` com
`topo` a devolução também se cumpre, mas dos parâmetros só o `reasoning` chega.

**A guarda contra o verde falso.** Um fornecedor real que ignore o saco responde 2xx sem ter
lido o estado, e o banco só vê o código. Por isso, numa corrida cujo perfil **não** declara
`devolver_em: topo`, o relatório conta os turnos cujo estado só veio dentro de
`provider_specific_fields` (`turnos_com_estado_so_no_saco_do_proxy`, lido pelos nomes dos
campos) e, havendo algum, o veredicto não pode ser `cumprida`: fica `inconclusiva` com a razão
`estado_devolvido_so_no_saco_do_proxy`
(`TestAOS516_OpenRouter_EstadoSoNoSacoDoProxyNaoDaCumprida`).

**O que não se sabe sem o modelo real:** se a OpenRouter aceita o segundo turno com
`reasoning_details` no topo e `reasoning_content` ao lado (o proxy cria este último a partir de
`reasoning`), e se valida as assinaturas como o falso não valida. O falso emite a forma
documentada, escrita à mão.

## A experiência dos separadores

Quatro braços sobre T1, intercalados em blocos (cada bloco tem os quatro braços, numa ordem
baralhada pela semente):

| Braço | Separadores | Texto do protocolo |
|---|---|---|
| A | `<kind>` … `</kind>` | projecção 1.2.0 publicada |
| B | `[[kind]]` … `[[/kind]]` | 1.2.0, com as frases que nomeiam o separador trocadas |
| C | `<kind>`, sem linhas de fim | 1.2.0 sem as frases da linha de fim |
| D | os da 1.0.0 | projecção 1.0.0 publicada (controlo) |

**As variantes B e C existem só no banco.** Não são versões de `AOS_MODEL_PROJECTION_VERSION`:
`ParseNativeProjectionVersion` recusa-as e nenhum binário do nó contém este módulo
(`TestAOS512_ONoNaoContemOBanco`). Se um braço ganhar, a versão de projecção correspondente
abre-se em ticket próprio, com emenda ao ADR-036.

Métrica: primeiras tentativas sem tool call. Com 53 por braço só se distingue 10% de 32%
(potência de 80% a 5%); diferenças menores não se vêem, e não as ver não prova que não existem.
O relatório dá, por braço, a taxa com o intervalo de 95% e, para cada braço contra o A, a
diferença, se os intervalos se sobrepõem, o p-valor do **teste exacto de Fisher** (bilateral) e
o p-valor **corrigido pelo método de Holm** para as três comparações — é o corrigido que decide
o campo `distingue_a_5_por_cento`. É aritmética sobre as contagens, não conclusões. (Uma
aproximação normal dava 0,041 para 0 em 53 contra 4 em 53; o teste exacto dá 0,118.)
