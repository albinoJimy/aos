# Banco de ensaio da fronteira runtime↔modelo (AOS-512)

Ferramenta de **medição**, fora do nó. Corre uma bateria fixa de casos sintéticos contra **uma
rota** e escreve um relatório de **taxas** — só contagens, fichas em vocabulário fechado e
identificadores de caso. Não decide nada: não aceita nem recusa um modelo — com uma excepção, a
experiência `qualificacao` (AOS-518), que dá um veredicto calculado por uma regra fixa (ver «A
qualificação do modelo»).

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

Esta é a bateria **1.0.0**, a das experiências `bateria`, `separadores` e `qualificacao`. A
**2.0.0**, por degraus, vive em `bateria2/` e só corre na experiência `degraus`: ver «A bateria
por degraus e a experiência `degraus`». A **3.0.0**, «difícil», vive em `bateria3/` e só corre na
experiência `conselheiro`: ver «O conselheiro e a experiência `conselheiro`».

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
(`chave_recusada`, `saldo_insuficiente`, `limite_de_ritmo`, `modelo_desconhecido`,
`rota_indisponivel`, `outro`).
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
| `--experiencia bateria\|separadores\|qualificacao\|degraus\|conselheiro` | todos | `qualificacao`: ver «A qualificação do modelo». `bateria` (omissão): T1 a T6 num braço. `separadores`: quatro braços sobre T1, 53 amostras por braço, um turno e um pedido por amostra — 212 pedidos. `degraus` (AOS-525): a bateria 2.0.0, sete degraus no braço A, taxas por degrau e sem veredicto — até 180 pedidos por passagem, com seis turnos por run; não aceita `--braco` nem `--estado`. `conselheiro` (AOS-528): a bateria 3.0.0 em dois braços, com e sem a tool `aconselhar`, sem veredicto — ver «O conselheiro e a experiência `conselheiro`». |
| `--amostras N` | todos | Passagens pela bateria (omissão 1), ou amostras por braço nos separadores. |
| `--semente N` | todos | Fixa a ordem dos braços dentro de cada bloco. Vai no relatório. |
| `--braco A\|B\|C\|D` | todos | O braço da bateria (omissão `A`). |
| `--saida PASTA` | todos | Onde escrever o relatório. |
| `--pausa DURACAO` | todos | Intervalo entre dois passos (por exemplo `2s`). |
| `--silencioso` | todos | Não mostra o resumo, só os caminhos. |
| `--guardar-respostas` | todos | **Desligada por omissão** (AOS-525). Grava, ao lado do relatório, um ficheiro por run com o texto das respostas do modelo e os argumentos das tool calls. Ver «As respostas guardadas, a pedido». |
| `--roteiro a,b,…` | falso, proxy | O roteiro do provider falso. |
| `--tecto-pedidos N --contador F` | falso, proxy | Ensaia o próprio tecto. |
| `--binario-do-falso F` | proxy | Binário Linux do banco, para o contentor do provider falso. |
| `--chaves F` | real | Caminho do ficheiro de chaves. Obrigatório (ou `AOS_ENSAIO_CHAVES`). |
| `--fornecedor kimi\|anthropic\|openrouter` | real | O fornecedor. |
| `--modelo M` | real | Um dos modelos do ficheiro (omissão: o primeiro). |
| `--precos F` | real; e falso e proxy na experiência `conselheiro` | Tabela de preços. Obrigatória quando há tecto em dólares. Na experiência `conselheiro`, dá o custo estimado por braço e por caso cumprido (pelo modelo de cada um). |
| `--perfil F` | todos | **Perfil de rota candidato** (AOS-513), em JSON, na forma dos campos de um perfil do gateway: `requested` (o nome que o nó pede: `rota-de-ensaio` ou, desde o AOS-519, o alias de produção — de 1 a 64 letras, dígitos, `_` e `-`, sem ponto; é o nome da rota efémera do proxy e o que a allowlist do nó de ensaio admite), `expected_model`, `wire_class`, `capabilities` e, opcionais, `params` (`thinking`, `reasoning_effort`, `max_tokens`, `reasoning` e, desde o AOS-526, a regra `tool_choice` — ver «A regra `params.tool_choice`»), `projection_version` e `devolver`. A leitura é fechada: uma chave ou um valor fora do conjunto recusa a corrida antes de qualquer pedido. O digest do perfil vai no relatório (`digests.perfil`) e entra no digest da configuração. É assim que um perfil se qualifica **antes** de entrar na tabela de perfis do nó. |
| `--estado exige\|proibe` | falso, proxy | O provider falso do **estado opaco** (AOS-516) em vez do do roteiro: emite raciocínio assinado em cada turno com tools e exige-o de volta (no modo falso, byte a byte, pelo falso exigente do AOS-515), ou recusa qualquer estado. No modo proxy pede `--perfil` com `expected_model` `anthropic/<modelo>`: a rota do proxy é essa, e o falso fala o wire de mensagens da Anthropic. |
| `--forma-do-falso openrouter` | falso, proxy | Só com `--estado`: o falso emite e exige a forma da **OpenRouter** (`reasoning` e `reasoning_details` com assinatura, no topo da mensagem). No modo proxy o `expected_model` do perfil é então `openrouter/<autor>/<modelo>` ou `openai/<autor>/<modelo>`. |
| `--turnos-do-falso N` | falso, proxy | Turnos com tool call do provider falso do estado (omissão 2). |
| `--host-esperado HOST` | todos | Só com um perfil que devolve estado: o host do endpoint que o proxy deve declarar ter servido. Sem ele compara-se só o modelo servido. O host não vai para o relatório. |
| `--so-plano` | real | Valida tudo, mostra o destino da chave e quantos pedidos faria, e não envia nenhum. |
| `--destino-fora-da-lista HOST` | real | Aceita um destino da chave que não é um host do fornecedor. `HOST` tem de ser exactamente o host do ficheiro; o `https` continua a ser exigido. |
| `--reconstruir-contador` | real | Recria um contador desaparecido a partir dos relatórios de hoje. |
| `--conselheiro-fornecedor kimi\|anthropic\|openrouter` | real | Só na experiência `conselheiro` (AOS-528): o fornecedor do conselheiro, com as chaves, os tectos e as listas do mesmo ficheiro de chaves. Liga o braço `executor+conselheiro`. |
| `--conselheiro-modelo M` | real | O modelo do conselheiro, da lista do ficheiro para o fornecedor dele (omissão: o primeiro). Fora da lista, recusado. |
| `--conselheiro-falso certo\|errado` | falso | O conselheiro falso: responde com os factos certos, ou com os errados, do que o executor lhe mandar. Liga o braço `executor+conselheiro`. |
| `--max-consultas N` | falso, real | Consultas por run do braço com conselheiro (1 a 5; omissão 2). Entra no pior caso. |

No modo real **não há `--contador`**: o contador é sempre `contador.json`, na pasta do dono
(`~/.aos-ensaio`; desde o AOS-518 já não depende de onde está o ficheiro de chaves). Uma flag que apontasse para outro ficheiro punha a contagem do dia a zero.

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
| 11 | Só `qualificar`, `qualificar-ocr` e `conferir` (AOS-522, AOS-530): o nó recusaria o registo de entrada, ou o pacote assinado. |
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
- **Contador.** `contador.json`, na pasta do dono (`~/.aos-ensaio`), esteja o ficheiro de chaves onde estiver. Persistente, por fornecedor e
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

## As respostas guardadas, a pedido (AOS-525)

O relatório não tem texto, e por isso não deixa **ler** o que um modelo respondeu. Com
`--guardar-respostas` (desligada por omissão; aceite nos modos `falso`, `proxy` e `real` e no
`qualificar`) a corrida deixa, ao lado do relatório, a pasta
`respostas-<nome do relatório, sem a extensão>/` (modo 0700) com **um ficheiro JSON por run**
(modo 0600), pela ordem da corrida:
`<ordem>-<corrida>-<caso>-<nó>-<braço>-a<amostra>-t<tentativa>.json`.

```json
{
  "formato": "aos-ensaio/respostas/v1",
  "corrida": "principal",
  "ordem": 1,
  "caso": "T1",
  "no": "ler",
  "braco": "A",
  "amostra": 0,
  "tentativa": 1,
  "desfecho": "cumprido",
  "texto_final": "…o texto com que o run fechou…",
  "turnos": [
    { "turno": 1, "texto": "", "tool_calls": [ { "nome": "arquivo", "argumentos": "{\"path\":\"notas-armazem.txt\"}" } ] },
    { "turno": 2, "texto": "…o texto com que o run fechou…", "tool_calls": [] }
  ]
}
```

- `caso`, `no`, `braco`, `amostra`, `tentativa` e `desfecho` são os da linha do run em
  `observacoes`; `ordem` é a posição dela (a contar de 1). `corrida` é `principal` ou `controlo`
  (a corrida de controlo sem devolução de uma qualificação).
- `turnos` tem uma entrada por cada pedido que o banco deu por **bem sucedido**: houve resposta
  do modelo e o seu uso ficou contado. Não entra um pedido que falhou (não há o que guardar),
  nem um turno cuja resposta chegou mas cuja contagem no contador do dia falhou — esse é
  tratado como pedido falhado, e o texto dele não fica guardado. `argumentos` é o texto que o
  modelo escreveu, tal como veio (pode nem ser JSON).
- `texto_final` é a saída do run, onde os factos são procurados; fica vazio quando o run não
  fechou `cumprido` (o texto de cada resposta está em `turnos`).
- **Não se guarda:** o prompt de sistema, os pedidos, os cabeçalhos, as credenciais nem os
  resultados das tools (os documentos sintéticos, que já estão em `bateria/`).

**Sem a opção nada muda:** a pasta de saída tem o relatório e o resumo, byte a byte os de
antes. **Com ela**, o relatório, o resumo e o `digest_do_relatorio` são os de uma corrida igual
sem ela; o texto guardado não vai para a saída normal nem para a de erros — onde só aparece uma
linha com o caminho da pasta e o número de ficheiros —, nem para o rasto do proxy, nem para
argumentos de processo nenhum. Está preso por teste (`aos525_respostas_test.go`), com um
marcador no texto e outro nos argumentos de uma tool call.

**O índice.** O último ficheiro a ser escrito é `indice.json` (modo 0600): o nome do ficheiro
do relatório, o sha256 dos seus bytes tal como ficaram em disco, e o número de runs.

```json
{ "formato": "aos-ensaio/respostas-indice/v1", "relatorio": "ensaio-falso-bateria-20261008T120000Z.json", "sha256_do_relatorio": "sha256:…", "runs": 8 }
```

É por ele que se confere que a pasta é do relatório que está ao lado — o nome não chega: um
relatório que não é de qualificação é **reescrito** por outra corrida do mesmo modo, no mesmo
segundo e na mesma `--saida`. **Uma pasta sem `indice.json` está incompleta** (a escrita falhou
a meio).

A pasta é criada de novo, e uma falha a guardar **não muda o código de saída** (o relatório já
está escrito); sai um aviso que diz qual foi:

- `AVISO — as respostas desta corrida NAO ficaram guardadas: ja existe a pasta …, que e de OUTRA
  corrida e NAO corresponde a este relatorio` — nada foi escrito; a pasta que lá está é de outra
  corrida, e o `sha256_do_relatorio` do índice dela não é o do relatório actual se este foi
  escrito por cima.
- `AVISO — as respostas ficaram INCOMPLETAS: k de n ficheiro(s) escritos …` — com a causa do
  sistema (a pasta não se criou, um ficheiro ou o índice não se escreveu). Não há `indice.json`.
- `AVISO — as respostas de n run(s) NAO ficaram guardadas: o relatorio nao chegou a ser escrito`
  — as respostas esperam em memória pelo nome do relatório, e perderam-se com ele.

Com `--so-plano` a opção não tem efeito, e uma linha di-lo. No `qualificar` a pasta fica em
`--saida`, **fora** de `pacote/`.

**Só a bateria sintética.** O ticket pede que a opção seja recusada fora dela. Não há o que
recusar: a linha de comandos só conhece a bateria embebida no binário — não tem opção, variável
de ambiente nem ficheiro por onde entrem outros casos ou documentos. Quem acrescentar outra
origem de casos acrescenta, no mesmo passo, a recusa.

**O que está nos ficheiros é texto de um modelo:** são dados, não instruções. Ao contrário do
relatório, a pasta das respostas **não** é para copiar para o repositório.

## A bateria por degraus e a experiência `degraus` (AOS-525)

A bateria 1.0.0 não separa modelos: a 2026-10-12 foi passada a seis, do mais barato ao mais
caro, e nenhum falhou a pedir a tool à primeira tentativa. A **bateria 2.0.0** tem sete casos de
dificuldade **crescente**, um por degrau, para se ler **até onde** cada modelo chega. Vive em
`bateria2/` (`casos.json` e doze documentos), tem digest próprio, e só a experiência `degraus` a
corre. **A 1.0.0 não muda:** os ficheiros, o digest, as experiências `bateria`, `separadores` e
`qualificacao` e a regra de qualificação são os de antes
(`TestAOS525_Degraus_ABateria2TemDigestProprioEA1MantemODeAntes`).

```
go run ./cmd/aos-ensaio bateria --experiencia degraus                    # o digest e os sete casos
go run ./cmd/aos-ensaio falso --experiencia degraus --saida /tmp/ensaio  # tudo cumprido
go run ./cmd/aos-ensaio falso --experiencia degraus --saida /tmp/ensaio \
  --roteiro degrau_cumpre,degrau_uma_leitura,degrau_distractor,erro_500,degrau_uma_leitura,degrau_distractor,degrau_cumpre,degrau_cumpre,degrau_omite,degrau_cumpre
```

No modo `real` é `real --chaves … --fornecedor … --experiencia degraus [--amostras N] [--perfil F]`,
com `--so-plano` primeiro, como nas outras experiências — e só com autorização do dono.

### Os degraus

Cada caso diz por extenso, na sua `descricao`, a capacidade que pede. Os documentos são
sintéticos, inventados, sem acentos, com a mesma marca na primeira linha, e os nomes e códigos
não existem na 1.0.0.

| Degrau | Caso | Capacidade | Nós | Documentos lidos com `arquivo` | Factos exigidos | Factos proibidos |
|---|---|---|---|---|---|---|
| 1 | `D1` | **base**: ler um documento e responder com dois factos, e só com eles | 1 | `guia-recepcao.txt` | `RM-5206`, `Duarte Meireles-Sardo` | `FD-30771` (o outro código do documento) |
| 2 | `D2` | **volume**: quatro documentos, um facto de cada (no mesmo passo ou um por turno) | 1 | `camara-norte.txt`, `camara-sul.txt`, `camara-leste.txt`, `camara-oeste.txt` | `SL-3391`, `Ines Portocarrero-Lima`, `4127`, `OM-60518` | os mesmos campos das outras três fichas: `SL-9313`, `SL-1939`, `SL-3919`; `Horacio Bettencourt-Rua`, `Vasco Almada-Teixeira`, `Graca Sequeira-Nobre`; `3058`, `2274`, `1863`; `OM-71204`, `OM-12470`, `OM-40721` |
| 3 | `D3` | **documento longo com distractores**: 28,5 KB, o facto a meio, entre três lotes retidos noutros cais | 1 | `registo-cais.txt` | `KV-7302` | `KV-7032`, `KV-3702`, `KV-7320` |
| 4 | `D4` | **cruzar dois documentos**: um diz o turno do cais 6, o outro quem coordena cada turno | 1 | `escala-cais.txt`, `coordenadores-turno.txt` | `Filipa Noronha-Teles` | `Rodrigo Vilarinho-Paz`, `Anselmo Queiroga-Brito`, `Leonor Vasques-Maia` |
| 5 | `D5` | **tool que falha e outro caminho**: o Reference Monitor nega o documento, o objectivo diz que há uma cópia | 1 | `inventario-reservado.txt` (negado), `inventario-copia.txt` | `CF-27745` | `AC-90436` (outro código da cópia) |
| 6 | `D6` | **instrução com restrição**: responder com o lote sem mencionar o fornecedor | 1 | `nota-fornecimento.txt` | `LF-8843` | `Quelhas-Bordalo` |
| 7 | `D7` | **cadeia funda**: ler → extrair → reescrever → resumir, por `entrada_de`; o facto tem de chegar ao último | 4 | `auto-avaria.txt` (só o primeiro nó) | `AV-3157`, `Baltasar Girao-Neves`, nos quatro nós | `EM-2290`, `PB-6612` (o que o `extrair` manda deixar de fora), nos três nós depois do `ler` |

Que os factos exigidos são os que o objectivo pede — e de que frase do documento vem cada um —
está preso por uma tabela, nó a nó (`TestAOS525_Degraus_OsFactosSaoOsQueOObjectivoPede`): o
digest prende os bytes, a tabela prende o sentido.

O carregamento é fail-closed como o da 1.0.0, e mais: todos os casos têm `degrau` (inteiro a
partir de 1, sem buracos, por ordem crescente) e a capacidade por extenso; nenhum facto —
exigido ou proibido — vem escrito no objectivo; não há dois objectivos iguais nem um contido
noutro. O campo `degrau` é recusado numa bateria que não seja por degraus.

**Seis turnos, e não quatro.** Um turno é uma invocação do modelo, e a que responde também
conta. No degrau 2, ler os quatro documentos um de cada vez são quatro turnos, e a resposta é o
quinto: com os quatro turnos das outras experiências, um modelo que lê bem mas não faz chamadas
em paralelo acabava em `turnos_esgotados`, e o degrau media o paralelismo em vez do volume. A
experiência `degraus` corre com seis (`MaxTurnosDosDegraus`); as outras ficam em quatro
(`TestAOS525_Degraus_LerUmDocumentoPorTurnoCumpreODegrau2`).

### Os factos proibidos

Um nó pode trazer `factos_proibidos`: valores dos seus documentos que **não** podem aparecer
na saída — os distractores, os valores que a pergunta não pede, e o facto que a instrução veda.
Um run com um deles na saída conta como **resposta errada**, à parte de **facto ausente**: o que
troca o lote pelo do cais ao lado tem o facto ausente *e* a resposta errada; o que responde bem
e acrescenta o fornecedor tem o facto presente e a resposta errada.

**O que é proibido está vedado no objectivo**, que é o que o modelo lê (a `descricao` do caso
não lhe chega): os degraus 1 a 5 terminam numa exclusão «…and with no other …» (código, nome ou número),
o 6 em «Do not mention…», e o nó `extrair` do 7 em «…leave out everything else». Sem isso, uma
resposta certa que citasse o contexto — o técnico ao lado do selo — contava como errada sem a
instrução o ter pedido. A excepção, declarada, são os nós `reescrever` e `resumir` do degrau 7:
os seus proibidos só lhes chegam se o `extrair` errou, e medem o erro a propagar-se
(`TestAOS525_Degraus_OsFactosSaoOsQueOObjectivoPede`).

**Servem para que despejar o documento não cumpra degrau nenhum.** Todos os degraus os têm (na
cadeia, a partir do segundo nó — o primeiro pede o texto inteiro). O provider falso de sempre,
que devolve tudo o que as tools entregaram, falha os **sete** degraus por resposta errada
(`TestAOS525_Degraus_OFalsoDeSempreDespejaOsDistractores`).

**A comparação dos proibidos é mais larga do que a dos exigidos.** Os exigidos comparam-se pela
regra dos factos (sem acentos, sem maiúsculas, espaços e hífenes tipográficos dobrados), que é
estrita de propósito: um nome mal escrito conta como ausente. Nos proibidos a mesma regra
jogava a favor do erro — «Quelhas Bordalo», com um espaço pelo hífen, passava por não ter
mencionado o fornecedor. Por isso os proibidos comparam-se, além disso, **sem hífenes e sem
espaços**. Um nome ou um código diferente continua a não disparar
(`TestAOS525_Degraus_ARegraDosFactosProibidos`).

A bateria não carrega se um proibido não estiver nos documentos do nó (não seria um distractor)
ou se, nessa forma larga, for igual a um facto exigido, ou um contido no outro. A observação de
um run ganha o campo `factos_proibidos` (`nenhum` ou `presente`) **só** quando o nó os tem —
nenhum nó da 1.0.0 os tem.

### O que o relatório traz a mais

Só nesta experiência, o relatório tem `taxas.por_degrau`: a versão da bateria, a regra do
agregado por extenso e, por degrau, com numerador, denominador e intervalo de Wilson a 95%:

| Campo | O que conta |
|---|---|
| `passagens`, `passagens_interrompidas`, `interrompidas_por_causa` | As passagens pelo degrau (um caso, numa amostra) que começaram; as que saíram das taxas por uma causa passageira; e a causa. |
| `degrau_cumprido` | Passagens **não interrompidas** em que **todos** os nós fecharam cumprido, com os factos exigidos na saída e nenhum proibido. |
| `nos_cumpridos` | Nós cuja última tentativa fechou cumprido — só o contrato de conclusão. Tudo o resto conta contra: sem tool call, turnos esgotados, resposta vazia ou cortada, um 4xx de parâmetro. Um nó que não correu, porque o anterior não concluiu, conta no denominador. |
| `sem_tool_call_na_primeira_tentativa` | A taxa de sempre, sobre os runs do degrau. Acima de zero não quer dizer degrau falhado: o nó pode ter recuperado à segunda tentativa. |
| `factos_ausentes` | A taxa de sempre, sobre os runs do degrau. |
| `respostas_erradas` | Runs concluídos de nós com factos proibidos, com pelo menos um na saída. |

**Um erro passageiro não é um degrau falhado.** Uma passagem em que a última tentativa de algum
nó fechou por uma causa passageira — 429, 5xx, erro de transporte, conta ou rota, tecto,
cancelamento: a classificação é a da qualificação (`estadoFinalDoNo`), não outra —, ou em que a
corrida parou antes de um nó correr (`corrida_parada`), **sai de todas as taxas do degrau** e
conta em `passagens_interrompidas`. Um degrau só com passagens interrompidas mostra `0/0`: não
foi medido (`TestAOS525_Degraus_UmErroPassageiroInterrompeAPassagemENaoFalhaODegrau`).

**Uma razão firme ganha a uma passageira**, como na qualificação, porque as contagens só
crescem. Se, antes da interrupção, a passagem já tinha uma falha firme — um nó que fechou sem
cumprir por culpa do modelo (incluindo uma tentativa anterior do próprio nó interrompido), ou
um nó concluído com um facto ausente ou um proibido presente —, ela **conta como não cumprida**
e não sai da amostra; os nós da interrupção em diante não entram em `nos_cumpridos`. Só sai a
passagem em que, até à interrupção, nada estava firmemente falhado
(`TestAOS525_Degraus_UmaFalhaFirmeNaoSePerdeNumaInterrupcao`).

**A amostra reduzida vem marcada.** No resumo, a coluna das interrompidas diz o total («9 de
10»), e a linha de um degrau em que **metade ou mais** das passagens saíram da amostra abre por
`!`, com a legenda por baixo da tabela: «1/1» com nove interrompidas em dez não é uma taxa em
que se possa confiar (`TestAOS525_Degraus_AMarcaDeAmostraReduzida`).

O resumo `.txt` abre o corpo com uma tabela curta, uma linha por degrau (as fracções e a
contagem das interrompidas; os intervalos estão no JSON). **Não há veredicto**: a corrida que
completa sai com 0, sejam quais forem as taxas. Os relatórios das outras experiências não ganham
campo nenhum (`TestAOS525_Degraus_OsCamposNovosSoAparecemNaExperienciaNova`, e o digest de
referência do AOS-516). O bloco vive dentro de `taxas`, e não no topo do relatório, de
propósito: o nó lê os relatórios de qualificação com uma lista fechada dos membros do topo
(AOS-519), e um membro novo no topo era uma alteração ao nó.

O **pior caso** é 180 pedidos por passagem — dez nós, três tentativas, seis turnos —, vezes
`--amostras`. Vai em `pedidos.previstos_max` e, com contador (sempre, no modo real), é conferido
contra o que resta do tecto do dia antes do primeiro pedido
(`TestAOS525_Degraus_OPiorCasoEDeclaradoEImposto`). Com o provider falso que cumpre, uma
passagem gasta 18 pedidos; o que um modelo real gasta está por medir.

### Os modelos falsos dos degraus

O `cumpre` de sempre devolve tudo o que as tools entregaram, e com isso falha os sete degraus
por resposta errada. A experiência tem os seus comportamentos, que respondem **só com os factos
exigidos do nó que encontraram no que receberam** (é o que faz a cadeia do degrau 7 ser uma
cadeia: um facto que um nó deixa cair não volta no seguinte):

| Comportamento | O que faz | Falha |
|---|---|---|
| `degrau_cumpre` (omissão) | Pede as leituras que o objectivo nomeia — no mesmo passo; no degrau 5, uma de cada vez, até uma dar um facto — e responde só com os factos pedidos. | Nenhum degrau. |
| `degrau_um_por_turno` | Como o anterior, com **uma leitura por turno**: o modelo que lê bem e não faz chamadas em paralelo. | Nenhum degrau (no degrau 2 faz cinco pedidos). |
| `degrau_omite` | Como o primeiro, e deixa cair o último facto. | Por **facto ausente** (preso por teste nos degraus 1 e 7). |
| `degrau_distractor` | Como o primeiro, e junta um facto proibido do nó que tenha recebido. | Por **resposta errada** (preso por teste nos degraus 1 a 6). |
| `degrau_uma_leitura` | Pede só a primeira leitura nomeada. | O degrau 2, por **facto ausente**; o degrau 5, pelo **contrato** (`contract_unmet_after_denial`). |

Os comportamentos de sempre também servem aqui: `texto` não pede a tool e falha pelo contrato
(`contract_unmet_no_call`), `erro_500` interrompe a passagem. O contrário não: um comportamento
`degrau_*` fora da experiência `degraus` é recusado pela linha de comandos (saída 2) — o falso
reconhece o nó pelo objectivo, na bateria 2.0.0 embebida, e a um pedido da 1.0.0 respondia 400.
O roteiro dá um comportamento por **conversa** (um run), pela ordem de chegada: numa passagem em
que nada se repete, `D1` a `D6` são as conversas 1 a 6 e os quatro nós de `D7` as 7 a 10. Cada
degrau tem quem o cumpra e quem o falhe, e o relatório distingue-os pela razão
(`TestAOS525_Degraus_CadaDegrauTemQuemOCumpraEQuemOFalhe`).

### O que a experiência `degraus` não prova

- **Não dá veredicto e não substitui a qualificação** (AOS-518), que continua a correr sobre a
  1.0.0 com a sua regra. Se o conteúdo passa a contar para o veredicto é decisão do dono.
- **Os degraus são sintéticos e curtos**: um caso por degrau, documentos inventados de poucas
  linhas (salvo o longo). Cumprir um degrau não diz que o modelo faz o mesmo com documentos
  reais. A ordem dos degraus é a que quem escreveu a bateria julgou crescente: não é uma escala
  medida, e um modelo pode falhar o 3 e cumprir o 5.
- **«Degrau cumprido» é verificação de factos por dobra, não um juiz**: diz que os valores
  exigidos estão na saída e os proibidos não. Não diz que a resposta é boa nem que respeita o
  resto da instrução (a forma, o tamanho, a língua). No degrau 7 não confere que o nó
  «reescrever» reescreveu: confere que o facto lá está.
- **Um proibido conta onde quer que apareça**, mesmo numa negação («não é o lote do cais 2»,
  com o código). E uma resposta errada que não seja um dos proibidos listados conta como facto
  ausente, não como resposta errada.
- **O degrau 5 mede «chega ao facto apesar da recusa», não «fez a segunda chamada»**: um modelo
  que vá directo à cópia, sem tentar o documento negado, cumpre-o. E o degrau 6 é provavelmente
  mais fácil do que os degraus 3 a 5.
- **Os degraus correm sempre pela mesma ordem**, do 1 ao 7, dentro de cada passagem: uma deriva
  da rota ao longo da corrida (carga, hora, um limite de taxa) cai nos últimos degraus, e não se
  reparte por todos.
- **Um degrau com passagens interrompidas foi medido com menos amostras** do que as pedidas; a
  coluna das interrompidas lê-se antes das taxas.
- **Um modelo que estoura sempre o prazo do pedido** (`tempo_esgotado`) sai como interrompido,
  pela classificação da qualificação: fica «não medido», não «falhado». Lê-se pela coluna das
  interrompidas e pela causa `erro_de_transporte`, não pela taxa.
- **`interrompidas_por_causa.cancelamento_da_corrida` não se reconstitui a partir das
  `observacoes` do JSON**: nesta experiência o cancelamento de um run fica em memória e não é
  serializado na observação. A contagem do relatório está certa; quem recalcular das observações
  vê `corrida_parada` ou outra coisa (preso por teste, com uma corrida cancelada de verdade:
  `TestAOS525_Degraus_UmaCorridaCanceladaNaoFalhaODegrauEmCurso`).
- **No degrau 7, um erro do `extrair` pode contar até três vezes em `respostas_erradas`** — uma
  por cada nó que o recebe e o repete — e uma só vez em `degrau_cumprido`.
- **Com uma passagem, cada degrau tem uma amostra**: a taxa é 0 ou 1 e o intervalo de 95% é
  quase todo o intervalo. Para comparar modelos é preciso `--amostras`, e ler os intervalos.
- **Os modelos falsos não dizem nada de um modelo real**: provam que a medida distingue quem
  cumpre de quem falha, e por que razão. A medição real fica por fazer.
- O modo `proxy` desta experiência (o falso dentro do contentor, atrás da imagem do proxy) não
  foi corrido.

## O conselheiro e a experiência `conselheiro` (AOS-528)

A fase A4 mediu que uma cascata de modelos não se justificava: com as tarefas que temos, o
modelo barato acertou em tudo. A alternativa que o dono quer avaliar é o **conselheiro**: o
executor barato pode consultar, a meio do run, um modelo mais forte por uma **tool**
(`aconselhar`), e decide ele quando. **Não é a ferramenta `advisor` da Anthropic** (só existe na
API directa dela e devolve o conselho cifrado): é uma tool do banco, registada no Reference
Monitor do nó de ensaio como `arquivo` e `guardar`, e o conselheiro é **qualquer modelo de
qualquer fornecedor** que o banco conheça — o executor e o conselheiro podem vir de fornecedores
diferentes (Haiku pela OpenRouter e Kimi pela conta Kimi, ou DeepSeek com Gemini).

```
go run ./cmd/aos-ensaio bateria --experiencia conselheiro                                    # o digest e os sete casos
go run ./cmd/aos-ensaio falso --experiencia conselheiro --conselheiro-falso certo --saida /tmp/ensaio
go run ./cmd/aos-ensaio falso --experiencia conselheiro --conselheiro-falso errado --roteiro conselho_ignora --saida /tmp/ensaio
```

### A bateria 3.0.0

Vive em `bateria3/` (`casos.json` e dezassete documentos), com digest próprio, e só a
experiência `conselheiro` a corre; **a 1.0.0 e a 2.0.0 não mudam** (digests presos em
`TestAOS528_Bateria3_TemDigestProprioEAsOutrasMantemOSeu`). Sete casos de um nó, cada um com a
capacidade dita por extenso na `descricao`, documentos sintéticos sem acentos com a marca de
sempre, nomes e códigos novos. Todos os nós têm factos **exigidos** e **proibidos** (as respostas
erradas plausíveis), e cada facto exigido está, literalmente, nos documentos: a resposta certa é
um valor que lá está, mas que só se identifica depois de vários passos.

| Caso | Capacidade | Exigidos | Proibidos |
|---|---|---|---|
| X1 | dedução em três passos sobre três documentos (dia → caldeira → bomba → prateleira) | `P-0917` | `P-0719`, `P-9071` |
| X2 | quatro documentos de nomes neutros que se contradizem; prevalece a data mais recente, com as datas em três formatos (por extenso, `AAAA-MM-DD` e `DD/MM/AAAA`), contra um número de revisão mais alto e uma nota intermédia com outro nome | `Cristovao Abreu-Taveira` | `Severino Lobato-Fragoso`, `Valdemar Quintela-Rosa`, `Odete Carrilho-Mendes` |
| X3 | três documentos de nomes neutros (`valvula-a`, `-b`, `-c`); prevalece a fonte de maior autoridade, que só se lê no texto, e a mais recente é a de menor | `Valverde-Esteves` | `Couto-Brandao`, `Pimentel-Sarmento` |
| X4 | documento longo (cerca de 50 KB), resposta de duas passagens a mais de 40 KB uma da outra, com um nome da frente certa a meio | `Amilcar Fontoura-Reis` | `Rogerio Antunes-Calado`, `Elisio Braganca-Mota` |
| X5 | várias restrições ao mesmo tempo: dar dois factos e calar quatro ao lado deles | `EN-4471`, `D-19` | `EN-7144`, `Mesquita-Alvim`, `PZ-8820`, `D-91` |
| X6 | tabela com condição: a zona sai do destino, noutro documento, e o código da zona e de um limiar de peso | `PT-6330` | `PT-6303`, `PT-3360`, `PT-3306` |
| X7 | regras com excepções e um calendário: o documento dá a data da inspecção e o dia da semana de OUTRO dia (1 de Outubro de 2026, quinta-feira); o dia da semana, o terceiro sábado e as férias são contas do modelo | `Benedita Ramalho-Quintas` | `Adelino Corte-Real`, `Celestino Viegas-Albano` |

### A tool `aconselhar`

Argumentos `pergunta` (obrigatório) e `contexto` (opcional): **é tudo o que o conselheiro vê** —
não o objectivo, não os documentos. O conselho volta ao executor como resultado da tool (dado,
não instrução). Só é oferecida aos runs do braço `executor+conselheiro`; um nó de ensaio sem
conselheiro não a regista nem a oferece, e o digest da configuração das corridas sem ele não
muda (a tool não está em `ToolsDoBanco`).

- **Pelo mesmo proxy, numa segunda rota.** No modo `real` o proxy efémero sobe com **duas
  rotas**: a do executor (o alias de sempre) e `rota-conselheiro`, cada uma com o adaptador, a
  chave (`ENSAIO_CONSELHEIRO_API_KEY`) e a base (`ENSAIO_CONSELHEIRO_API_BASE`) do seu
  fornecedor, pelo ambiente do processo `docker` — nunca pelos argumentos. Sem conselheiro, a
  configuração é a de antes, byte a byte (`TestAOS528_Proxy_SemConselheiroAConfigEADeAntes`). A
  consulta sai do banco directamente para essa rota, como a sonda: **não** passa pelo Model
  Gateway do nó de ensaio.
- **Contas e tectos do fornecedor DELE.** O conselheiro tem um contador «irmão» do da corrida
  (`Contador.Irmao`): o mesmo `contador.json` e a mesma trava, na linha do fornecedor dele, com os
  tectos dele e o preço do modelo dele. No mesmo fornecedor do executor, as duas contas somam na
  mesma linha e o tecto é um só. Um tecto em dólares sem preço do modelo do conselheiro recusa a
  corrida, como para o executor.
- **O pior caso declarado inclui as consultas.** Executor: `amostras × 2 braços × 7 nós × 3
  tentativas × 8 turnos` (336 por passagem; 168 sem conselheiro). Conselheiro: `amostras × 7 nós ×
  3 tentativas × --max-consultas` (42 por passagem com 2). Mais uma sonda por rota. O plano di-lo
  por fornecedor («PIOR CASO POR FORNECEDOR») e a corrida não arranca se o de algum não couber no
  que resta do dia dele. A consulta a mais num run é recusada pela tool, sem pedido.
- **Uma consulta é uma observação.** Por run do braço com conselheiro, o campo `conselho` da
  observação conta as chamadas da tool (`pedidas`), as respondidas, as vazias, as recusadas pelo
  limite, as falhadas por causa passageira ou de parâmetro, os pedidos, os códigos HTTP e os
  tokens. Nada de texto. Com `--guardar-respostas`, cada ficheiro de run desse braço leva
  `consultas`: a pergunta, o contexto e o conselho.
- **O tecto do dia do conselheiro atingido a meio** pára a corrida (`tecto_atingido`): a
  consulta não sai, e o braço deixava de medir o conselheiro.

### A experiência

Os dois braços — `so-executor` e `executor+conselheiro` — correm **na mesma corrida e com a
mesma semente**, intercalados em blocos por amostra (a ordem dos braços é baralhada pela
semente). O relatório traz o bloco `taxas.conselheiro` (nas taxas, e não no topo: a lista dos membros do topo é fechada na leitura do nó), e o resumo a tabela:

```
CONSELHEIRO (bateria 3.0.0; sem veredicto; conselheiro fornecedor=falso modelo=modelo-falso-do-conselheiro, ate 2 consulta(s) por run; …)
  pior caso de pedidos por fornecedor (com as sondas): falso=378
  braco                 cumpridos interrompidas consulta  cons/run ped.exec ped.cons USD exec    USD cons    USD por caso cumprido
  so-executor           0/7       0 de 7        -         -        14       0        0.030960    0.000000    - (nenhum caso cumprido)
  executor+conselheiro  7/7       0 de 7        7/7       1.00     21       7        0.060486    0.049038    0.015646
```

- **Caso cumprido** é a regra do degrau cumprido (`regra_do_caso_cumprido`): o nó fechou o
  contrato, com os factos exigidos e sem nenhum proibido. Uma passagem interrompida por causa
  passageira sai da amostra; no braço com conselheiro, também a de um run com uma consulta sem
  resposta por causa passageira (`conselheiro_sem_resposta`) — **acerte ou erre**: tirar só os
  que erraram deixava os acertos e subia o braço com um conselheiro instável.
- **Taxa de consulta**: runs medidos em que o executor chamou a tool, sobre os que a tinham (no
  braço sem conselheiro, `-`). **Consultas por run**: a média.
- **Pedidos e custo** do executor e do conselheiro, separados, de **todos** os runs do braço
  (o que se pagou), estimados pelos tokens e pelos preços de `--precos`; **custo por caso
  cumprido** é o total a dividir pelos casos cumpridos.
- **O tecto — o modelo forte sozinho — é outra corrida**, da mesma experiência, sem conselheiro
  e com o modelo forte como executor, com a mesma `--semente`: o nó de ensaio tem um executor por
  corrida (uma rota, um alias, uma allowlist). Os casos comparam-se entre os dois relatórios pelo
  digest da bateria.

### Os falsos

O executor falso tem quatro comportamentos novos, só desta experiência, que leem os documentos
que o objectivo nomeia (todos no primeiro turno) e respondem com valores deles:
`conselho_cumpre` (certo sozinho, nunca consulta), `conselho_nao_consulta` (a — nunca consulta e
erra), `conselho_segue` (b — consulta e responde com o que o conselho trouxe; sem a tool, erra) e
`conselho_ignora` (c — consulta e erra na mesma). O roteiro por omissão desta experiência é
`conselho_segue`. O conselheiro falso (`--conselheiro-falso certo|errado`) responde com os
factos exigidos — ou com os proibidos — que encontrar no que o executor lhe mandou.
`TestAOS528_OsBracosDistinguemOsFalsos` prova que os braços os distinguem.

### O que a experiência `conselheiro` não prova

- **Não dá veredicto**: não diz se o conselheiro compensa.
- **Sintética e pequena**: sete casos de um nó. Com poucas passagens os intervalos de 95% são
  quase todo o intervalo; uma diferença entre braços lê-se com `--amostras` e os intervalos.
- **O conselheiro só vê o que o executor lhe passa.** Um conselho errado pode ser culpa do que
  o executor mandou, e o relatório não separa as duas causas.
- **A taxa de consulta depende do texto da tool** (fixo, no digest da configuração), do prompt de
  sistema e do esforço do modelo executor. Não é uma propriedade do conselheiro.
- **A consulta não passa pelo Model Gateway** do nó de ensaio: do conselheiro mede-se o código
  HTTP, os tokens e se trouxe texto — não a forma da resposta.
- **O custo é uma estimativa**, conta os runs das passagens interrompidas, e não é a factura.
- **O tecto é outra corrida**: uma deriva da rota entre as duas não se reparte.
- **Um proibido conta onde quer que apareça**, mesmo numa negação, como nos degraus.
- **Acertar por palpite não é raro.** Em X1, X2, X3, X6 e X7 a resposta é um de três ou quatro
  valores parecidos que estão nos documentos: um modelo que dá um só deles ao acaso acerta cerca
  de 1 em 3 a 1 em 4. Uma diferença entre braços lê-se contra essa base, e com amostras.
- **X5 mede obediência a instruções** (calar valores ao lado dos pedidos): a falha está na
  escrita da resposta do executor, e o conselheiro ajuda pouco aí.
- **Uma consulta que esgota o prazo, ou um 200 ilegível, não regista uso**: o fornecedor pode
  tê-la cobrado, e o gasto estimado e o tecto em dólares do conselheiro ficam subestimados —
  como os do executor no mesmo caso.
- **Um contador desaparecido reconstrói-se** (`--reconstruir-contador`) dos relatórios de hoje
  para todos os fornecedores que neles aparecem — o do executor, o do conselheiro e outros; um
  gasto feito fora desses relatórios não se reconstitui.
- **Os falsos não dizem nada de um modelo real**; o modo `proxy` não tem conselheiro (o falso em
  contentor não faz de conselheiro). A medição real fica por fazer, com autorização do dono.

## A medição do OCR e a experiência `ocr` (AOS-529)

Na fase A5 o dono escolheu **ferramentas primeiro**: uma imagem chega a um run como texto lido
por uma tool de OCR mediada pelo Reference Monitor, que corre localmente dentro do gVisor, sem
rede (desenho `docs/reports/desenho-a5-a6-multimodal-2026-10-14.md`, D1 e D2). O motor
escolhe-se **medindo aqui** antes de construir a tool (AOS-530). A experiência `ocr` é um
subcomando com as suas opções e o seu uso (`aos-ensaio ocr -h`); as corridas de antes não mudam.

```
go run ./cmd/aos-ensaio ocr bateria                              # o digest e as treze imagens
go run ./cmd/aos-ensaio ocr bateria --imagens /tmp/ocr-imagens   # escreve-as, para as ver
go run ./cmd/aos-ensaio ocr --motor falso --falso erra-factos --saida /tmp/ensaio
go run ./cmd/aos-ensaio ocr --motor tesseract --imagem sha256:<ID da imagem do posto> --saida /tmp/ensaio
go run ./cmd/aos-ensaio ocr --motor visao --chaves F --visao-fornecedor openrouter --visao-modelo M --precos P --so-plano
```

### A bateria de OCR

Vive em `bateria-ocr/casos.json`, com digest próprio, e **só tem texto**: quatro documentos
sintéticos (duas faturas, uma com acentos e outra sem; um recibo em minúsculas com acentos e
cedilhas; uma guia de remessa com uma tabela de quatro colunas e grelha), os factos de cada um
(códigos, nomes, números, que ocorrem literalmente no texto) e as treze imagens a gerar:

| Imagem | Documento | Degradação | Formato |
|---|---|---|---|
| `fatura-a-limpo`, `-ruido`, `-baixa`, `-jpeg` | fatura sem acentos | limpo, ruído e rotação, meia resolução, limpo | PNG, PNG, PNG, JPEG |
| `fatura-b-limpo`, `-ruido`, `-baixa`, `-jpeg`, `-pdf` | fatura com acentos | as mesmas, e ruído e rotação | PNG, PNG, PNG, JPEG, PDF |
| `recibo-limpo`, `-ruido` | recibo com acentos | limpo, ruído e rotação | PNG |
| `guia-tabela-limpo`, `-ruido` | tabela sem acentos | limpo, ruído e rotação | PNG |

As imagens **desenham-se em memória** ao carregar a bateria, com a biblioteca-padrão e uma fonte
bitmap 5×7 desenhada no banco (`ocr_fonte.go`, com as letras acentuadas do português; o zero sem
barra e mais estreito do que o «O», como na maior parte dos impressos): o repositório não guarda
binários, e nenhuma dependência nova entrou. Tudo é aritmética inteira — a rotação de 1,5° em
vírgula fixa e o ruído com um gerador de semente fixa —, para que os píxeis sejam os mesmos em
qualquer máquina. O **digest da bateria** cobre o `casos.json` e os píxeis de cada imagem antes
da codificação; o **digest de cada ficheiro** (PNG, JPEG ou PDF — o que o motor lê) vai por
imagem no relatório. A degradação «ruído e rotação» soma um tom de fundo variável e 0,4% de
píxeis soltos; «meia resolução» desenha a fonte a 2 píxeis por célula em vez de 4; o JPEG tem
qualidade 55; o PDF é uma página com a imagem de `fatura-b-ruido`, sem camada de texto, como sai
de um digitalizador. A validação é fail-closed: marca na primeira linha, factos no texto, letras
que a fonte tem, vocabulário fechado de degradações e formatos.

### O motor é plugável

Um motor é a interface `MotorOCR`: recebe uma imagem e devolve texto, e diz as condições em que
corre. **O tempo mede-o a experiência**, por fora, à volta da chamada; **a memória mede-a quem
corre o motor** (no contentor, o `memory.peak` do cgroup), não o motor. Acrescentar um candidato
é escrever um adaptador e dar-lhe um nome em `--motor`; a bateria, a regra dos factos e o
relatório não mudam. Há três:

- **`tesseract`** — o Tesseract num contentor Docker **fixado pelo conteúdo**: `--imagem
  nome@sha256:<64 hex>` (sem etiqueta), ou o **ID** `sha256:<64 hex>` de uma imagem construída no
  posto, que não tem digest de registo. A imagem constrói-se com `ocr/Dockerfile` (base Debian 12
  fixada pelo digest, `tesseract-ocr`, `tesseract-ocr-por`, `tesseract-ocr-eng` e
  `poppler-utils`); construir precisa de rede e é decisão do dono, correr não:

  ```
  docker build -t aos-ensaio-ocr:tesseract packages/qa/banco-ensaio/ocr
  docker image inspect aos-ensaio-ocr:tesseract --format "{{.Id}}"    # o ID a dar em --imagem
  ```

  Corre com os limites do executor gVisor de produção: `--network none`, 1 GiB e 512 processos,
  45 s por chamada (passado o limite o contentor é removido), raiz só de leitura, sem
  capabilities, `no-new-privileges`, uid 65532, a imagem montada só de leitura, e `--pull never`
  (o banco nunca descarrega a imagem; sem ela sai com 6). Línguas `por+eng` por omissão. O
  Tesseract não lê PDF: o guião do contentor rasteriza-o com o `pdftoppm` a 300 ppp, pelo stdout,
  e recolhe o código de **cada** estágio do pipe (o `dash` da Debian não tem `pipefail`). Uma
  falha conta como **memória** só com o pico do cgroup acima de 90% do limite, venha de que
  estágio vier; uma saída 137 sem esse pico é falha do motor. O contentor corre com o **runc** do
  Docker, não com o gVisor, e uma chamada de cada vez. O nome começa por `aos512-`, e o
  `aos-ensaio limpar` varre-o.
- **`visao`** — um **modelo de visão** pago, a opção (d) da D2, medida ao lado para a comparação
  (`ocr_visao.go`). Cada imagem vai como parte `image_url` numa data URL em base64, com uma
  instrução fixa de transcrever o texto tal como está, sem comentar. O pedido sai pelo **mesmo
  proxy efémero** do banco (a imagem fixada do LiteLLM), numa rota com alias próprio
  (`rota-visao`), com a chave e a base do fornecedor do modelo — como o conselheiro do AOS-528 —,
  e segue as regras do modo real: o ficheiro de chaves do dono, a lista de modelos dele
  (`--visao-fornecedor`, `--visao-modelo`), os tectos e o contador do fornecedor, o preço do
  modelo (`--precos`), uma sonda antes, `--so-plano`, e nunca em CI. **Pior caso: 13 pedidos**
  (12 imagens e a sonda). O **PDF fica fora deste braço** (estado `fora`, não conta): é a mesma
  página de `fatura-b-ruido`, e rasterizá-lo daria ao modelo os mesmos píxeis — um pedido pago a
  mais, sem informação nova. Uma recusa da chave, da conta ou do ritmo (401, 402, 403, 429) pára
  a corrida; outro erro falha só a imagem. O relatório chama-se `ensaio-real-ocr-visao-…`, traz os
  tokens e o custo estimado por imagem, e tem os campos das corridas reais, para o contador do
  dia se reconstruir também a partir dele.
- **`falso`** — devolve o texto de origem da imagem: `--falso certo` (tal e qual), `sem-acentos`,
  `erra-factos` (troca os zeros do código por «O» e corta o nome a meio) ou `falha-pdf`. Prova
  que o relatório distingue os quatro; não mede nada.

Os outros candidatos que o dono está a avaliar — Docling com Tesseract, EasyOCR, RapidOCR ou
surya; MinerU; Marker; LightOnOCR-2-1B; Chandra OCR 2 — entram como adaptadores, em contentor
fixado como o Tesseract, ou, os que são modelos, como o de visão.

### O relatório

`ensaio-ocr-<motor>-<data>.json` e `.txt`. Por imagem: documento, degradação, formato, com ou sem
acentos, bytes e dimensões, o digest do ficheiro, o estado (`lida`; `falhou` com a causa `tempo`,
`memoria`, `motor` ou — desde o AOS-530 — `cortada`, uma resposta do modelo de visão parada pelo
limite de tokens; ou `fora`), os factos presentes **pela regra do banco** (sem acentos nem
maiúsculas, `factos.go`) e **com os acentos exactos**, os ausentes e os que só faltam pelo
acento, o tempo, a memória máxima, o tamanho do texto e, no modelo de visão, os tokens e o custo
estimado. No resumo, uma tabela curta por degradação, por formato, com e sem acentos, e no total
(as imagens `fora` contam-se à parte); o veredicto (abaixo); e os limites da medição. Desde o
AOS-530 o relatório traz `versao_do_relatorio` (`1`) e o **digest de si próprio**
(`digest_do_relatorio`, a mesma forma canónica do da qualificação).

### O veredicto (AOS-530)

O relatório traz `veredicto_do_ocr`, contra o **limiar de factos do dono** — a D7 do desenho,
decidida a 2026-10-15: pelo menos **90%** dos factos presentes (`LimiarDeFactosDoOCR`, que vai no
relatório; `--limiar-de-factos F` aperta, de 0,90 a 1, e nunca alarga). A medida é a do AOS-529:
`presentes_pela_regra` sobre `factos`, somados nas imagens que entram nas contas (as `fora` não
entram) — a regra dobrada do banco, a mesma das qualificações; os acentos exactos ficam no
relatório e não decidem.

`qualificado` exige, **todas**: a corrida completa; pelo menos uma imagem nas contas; **nenhuma
imagem nas contas por ler** (tempo, memória, erro do motor ou resposta cortada); e a fracção no
limiar. Senão `recusado`, com as razões. A imagem por ler recusa mesmo com a fracção acima do
limiar: o modelo da tool é um só, sem nova tentativa nem recuo (ADR-042 §2.8), e uma imagem que
ele não lê é uma chamada da tool que falha — com doze imagens nas contas, uma falhada tira menos
de 10% dos factos e passava sem se ver. `vale_para_producao` só num `qualificado` do **modo real**
(o motor `visao`); os motores `falso` e `tesseract` não têm modo e nunca valem. A comparação é em
milésimas inteiras, para 90 de 100 passar e 89,9% não.

Com `--perfil FICHEIRO` (só nos motores `visao` e `falso`) o veredicto leva a **rota medida**: o
fornecedor, o modelo, o `expected_model`, o digest do perfil e, no `visao`, a imagem do proxy e o
seu digest. É ela que liga o relatório a um registo de entrada de uso `tool`. O perfil tem de
declarar a capacidade `imagem` e não pode declarar `tools` (o modelo que lê a imagem não pode
pedir efeitos). Um relatório com a rota medida nunca se escreve por cima de outro.

**A saída do `ocr` não depende do veredicto**: 0 se a corrida completou, como antes. Quem age
sobre ele é o `qualificar-ocr`.

### Preparar o registo de uso `tool` (`qualificar-ocr`, AOS-530)

```
go run ./cmd/aos-ensaio qualificar-ocr --motor falso --perfil <perfil-ocr.json> --board board-eu --regiao eu --saida /tmp/entrada-ocr
go run ./cmd/aos-ensaio qualificar-ocr --motor visao --chaves F --visao-fornecedor openrouter --visao-modelo M --precos P \
                                       --perfil <perfil-ocr.json> --board B --regiao R --saida PASTA-NOVA
```

É o `qualificar` (AOS-522) para o alias de uma tool cujo efeito é uma chamada a um modelo de visão
(ADR-042): corre a experiência `ocr` com o perfil e, **só com o veredicto `qualificado` de uma
corrida completa do motor `visao`** (o modo real), deixa em `PASTA/pacote/` o registo de entrada
de **uso `tool`** — `"uso": "tool"`, fatia `0` — e o relatório que ele cita. As mesmas barreiras:
o registo provisório passa pela leitura do nó antes de qualquer pedido (um alias fora da
allowlist sai 11 sem enviar nada); com o motor `visao`, o `expected_model` do perfil tem de ser a
rota desta corrida (`<prefixo do proxy>/<modelo medido>`, como no `qualificar`), senão sai 3 antes
de abrir o contador e o proxy; a pasta de saída é nova; o relatório relido do disco tem de ser
o da corrida; e o pacote escrito volta a passar nas conferências do nó. **Não assina**: assina o
dono, com a mesma linha do `aos-issuer route-profile-sign` que o comando imprime, e confere com o
`aos-ensaio conferir`.

Saídas: **0** pacote preparado; **7** recusado; **9** qualificado com o motor `falso` (nada para
assinar); **11** o nó recusaria o registo; 4 parou a meio; 3 recusada antes de enviar; 5 o motor
`visao` em CI; 6 sem infra-estrutura; 2 argumentos; 1 outra falha (nunca fica pacote).

O que o nó faz com o registo de uso `tool` (pacote `routeentry`, `cmd/aos/modelo_candidato.go`):
aceita-o só com um relatório `ocr` com veredicto `qualificado` do modo real, para o perfil e a
imagem do proxy que o registo cita; recusa-o com um perfil sem `imagem` ou com `tools`
(`perfil_de_tool_com_capacidades_erradas`), com fatia maior que zero (`uso_tool_com_fatia`), com
um relatório de outra experiência (`relatorio_de_outra_experiencia`) ou para o alias do titular;
e **nunca o trata como candidato** — não conta como não-titular, não ganha adaptador de run, não
entra no canary nem no disjuntor, e um run fixado nele é recusado.

**O texto lido não sai do banco**: nem no relatório, nem no resumo, nem nas saídas — só
contagens e os factos sintéticos ausentes. Com `--guardar-respostas` fica, ao lado do relatório,
na pasta `respostas-<nome do relatório>/`, um ficheiro por imagem.

### O que a experiência `ocr` não prova

- **É sintética**: quatro documentos, treze imagens, uma fonte bitmap sem serifas. Um documento
  impresso tem fontes vectoriais, e uma foto tem perspectiva, sombra e dobras que a bateria não
  simula. A fonte pode favorecer um motor face a outro — um modelo de visão e um OCR clássico
  podem reagir de maneira diferente a uma fonte de píxeis —, e isso vale para a **comparação**,
  não só para a taxa de cada um.
- **O Tesseract corre com o runc, não com o gVisor**, e uma chamada de cada vez; no gVisor, 1 GB é
  do serviço inteiro, partilhado pelas chamadas em curso. O custo do gVisor não está medido.
- **O modelo de visão não é o caminho da tool**: tem rede e custo por chamada, e o custo é uma
  estimativa pelos tokens e pela tabela de preços, não a factura.
- **A corrida real não foi feita aqui.** Os testes provam o adaptador do Tesseract com um Docker
  de teste e o de visão com um modelo de visão falso, não os motores. O guião do contentor não
  foi corrido contra a imagem real.
- **O modelo servido não é comparado com o `expected_model` do perfil** nesta experiência (é dito
  nos limites do relatório quando há `--perfil`): quem o compara é o gateway do nó, em cada chamada
  da tool, com a governação da rota ligada.

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
| Extras do adaptador | `usage` no pedido; `HTTP-Referer: https://litellm.ai` e `X-Title: liteLLM` (valores por omissão da imagem, em todos os pedidos) | nenhum |
| Erros 401, 402, 429, 404 e 400 na forma da OpenRouter | passam com o código; `chave_recusada`, `saldo_insuficiente`, `limite_de_ritmo`, `modelo_desconhecido` | igual |

Os erros dela classificam-se pelo código (HTTP ou `error.code`, que valem o mesmo) e por frases
fixas: «no endpoints found» só é `modelo_desconhecido` quando nomeia o modelo e mais nada — com
«matching your data policy» ou «that support tool use» é `rota_indisponivel` (o modelo existe; o
que o exclui são as definições da conta); um 403 com frase de moderação é `outro`, não
`chave_recusada`; e «rate limit exceeded» só conta num 429.

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

**No modo real, `cumprida` não é prova de leitura.** O banco só vê códigos: `cumprida` é 2xx a
todos os pedidos que levavam o raciocínio devolvido. A qualificação faz-se com a corrida
principal **e** um controlo negativo (o mesmo perfil com `devolver: nunca`): só se o controlo
levar 4xx nos segundos turnos é que a corrida principal mostra que o fornecedor exige o estado e
o aceitou. O relatório e o resumo trazem esta frase ao lado do veredicto; o procedimento está no
runbook.

**O que não se sabe sem o modelo real:** se a OpenRouter aceita o segundo turno com
`reasoning_details` no topo e `reasoning_content` ao lado (o proxy cria este último a partir de
`reasoning`), e se valida as assinaturas como o falso não valida. O falso emite a forma
documentada, escrita à mão.

## A qualificação do modelo (AOS-518)

A experiência `qualificacao` é a única em que o banco dá um **veredicto**: `qualificado`,
`recusado` ou `inconclusivo`. Corre a bateria **8 vezes** no braço A (40 nós com tools e 16 sem
tools) com um perfil candidato, que é obrigatório, e calcula o veredicto das contagens — ninguém
o declara, e nenhum modelo avalia outro. A qualificação é da **rota**: modelo, fornecedor, imagem
do proxy e perfil. Mudar um dos quatro é qualificar outra vez.

```bash
go run ./cmd/aos-ensaio falso --experiencia qualificacao --perfil <perfil.json> --roteiro cumpre --saida /tmp/ensaio
```

Não aceita `--amostras` nem `--braco`: a amostra é a da decisão D2 do dono (2026-10-11).

**Só o modo `real` qualifica.** Nos modos `falso` e `proxy` o fornecedor é um provider falso: o
veredicto leva `vale_para_producao: false`, o resumo abre por o dizer, e um `qualificado` sai com
o código 9 e não com 0.

### A regra

Vai escrita no relatório, ao lado do veredicto (`qualificacao_do_modelo.regra`). É função pura de
`qualificacao_do_modelo.contagens`. Limiares:

| # | Contagem | Máximo |
|---|---|---|
| L1 | Nós com tools por cumprir no fim das tentativas | 0 |
| L2 | Nós com tools cuja 1.ª tentativa fechou sem nenhuma tool call nativa, seja qual for o motivo de paragem | 4 |
| L3 | Respostas 4xx de parâmetro a pedidos que levavam uma mensagem `tool` | 0 |
| L4 | Nós (com e sem tools) com resposta vazia no fim das tentativas | 0 |
| L5 | Turnos com o modelo servido diferente do esperado, ou não reportado | 0 |
| L6 | Nós **sem** tools por cumprir no fim das tentativas (decisão do dono de 2026-10-11) | 0 |

- **Razões firmes** dão `recusado`: `nos_com_tools_nao_cumpridos`,
  `sem_tool_call_a_primeira_acima_do_limiar`, `http_4xx_nos_segundos_turnos`,
  `respostas_vazias_no_fim_das_tentativas`, `modelo_servido_diferente_do_esperado`,
  `modelo_servido_nao_reportado`, `nos_sem_tools_nao_cumpridos`,
  `devolucao_do_estado_nao_cumprida`, `perfil_recusado_pelo_provider`.
- **Razões passageiras**, sozinhas, dão `inconclusivo`: `pior_caso_nao_cabe_no_tecto`,
  `tecto_do_dia_atingido`, `tecto_da_qualificacao_atingido`, `chave_recusada`,
  `saldo_insuficiente`, `limite_de_ritmo_ou_serie_de_429`, `sonda_falhou`,
  `corrida_interrompida`, `amostra_incompleta`, `nos_interrompidos_por_429`, `_por_5xx`,
  `_por_erro_de_transporte`, `_por_conta_ou_rota`, `_pelo_tecto`, e
  `devolucao_do_estado_nao_medida`.
- Uma razão firme ganha a uma passageira: as contagens só crescem, e um limiar violado numa
  amostra parcial continua violado na amostra inteira.
- Um nó, com ou sem tools, só entra na amostra quando a sua última tentativa fechou `cumprido`
  ou falhou por uma causa que não é passageira. Um 429, um 5xx, um erro de transporte, o tecto
  ou a conta **tiram o nó da amostra**: não o dão como falhado nem como cumprido, e uma amostra
  abaixo de 40 e 16 não qualifica. Um nó de resumo cujo nó de leitura não cumpriu não chega a
  correr, e a amostra dos nós sem tools fica incompleta por isso.
- Um 4xx é «de parâmetro» quando fala do pedido: não é 429 nem 408, e o tipo do erro é `outro`.
- **Devolução declarada** (decisão do dono de 2026-10-11): um perfil com `devolver` diferente de
  `nunca` só é `qualificado` com `qualificacao_da_devolucao` em `cumprida`. `nao_cumprida` é
  firme; `sem_raciocinio` e `inconclusiva` são passageiras (o perfil promete o que a corrida não
  mediu).
- Os limiares aplicam-se às **contagens**. O intervalo de confiança vai ao lado de cada uma
  (`limiares[].medida`) e não entra na regra.

### Os avisos

Não mudam o veredicto, vão no campo `qualificacao_do_modelo.avisos` e **à cabeça** do resumo:

- `exigencia_nao_provada` — o perfil declara `devolver: obrigatorio` e a corrida de controlo sem
  devolução não mostrou que o fornecedor exige o estado. A qualificação de um perfil desses
  inclui essa corrida (uma passagem, o mesmo perfil só com `devolver: nunca`; a versão da
  projecção não muda), que só se faz se a principal deu `qualificado`. Resultado em
  `controlo_sem_devolucao.resultado`: `falhou_como_devido` exige a corrida completa, **todos**
  os pedidos com mensagem `tool` com 4xx de parâmetro e nenhum nó com tools cumprido; `passou`;
  `inconclusivo` (qualquer mistura, ou um controlo que não terminou); `nao_correu`. Só o
  primeiro tira o aviso. O que o controlo não isola vai em `controlo_sem_devolucao.limite`.
- `factos_ausentes_em_respostas_cumpridas` — houve runs cumpridos cuja saída não tinha os factos
  do documento sintético. Desde o AOS-525 a comparação não conta acentos, maiúsculas, a forma
  dos espaços nem a do hífen (`factos.go`, com os limites conhecidos): os documentos estão escritos sem acentos, e um modelo que
  escrevia «Tomás» por «Tomas» era contado como tendo perdido o facto. Um relatório anterior
  foi medido com a regra literal. Não há limiar para o conteúdo (o dono manteve-o como aviso).
- `tecto_em_dolares_nao_imposto` — modo real, fornecedor sem tecto em dólares no ficheiro de
  chaves: o banco não estima o gasto, e o tecto de 3 USD não actuou.

### Os controlos negativos

| Provider falso | Como se pede | Veredicto e razões firmes |
|---|---|---|
| Bom | `--roteiro cumpre` | `qualificado`, 40 de 40 e 16 de 16 |
| Não chama a tool | `--roteiro texto` | `recusado`: L1, L2 |
| Responde vazio | `--roteiro vazia` | `recusado`: L1, L4, L6 |
| Recusa o segundo turno com 4xx | `--roteiro rejeita_segundo` | `recusado`: L1, L3 |
| Troca o modelo servido | `--roteiro troca_modelo` | `recusado`: só L5 |
| Corta a resposta | `--roteiro cortada` | `recusado`: L1, L6, com `truncated` nos desfechos |
| Sem tool call, com outro motivo de paragem, e cumpre à 2.ª | `--roteiro sem_tool_end_turn,cumpre` (ou `sem_tool_content_filter`, `sem_tool_sem_motivo`) | `recusado`: só L2, 40 de 40 |
| 500 a tudo | `--roteiro erro_500` | `inconclusivo` |
| Anuncia a tool e não a chama, a não ser que o pedido a peça (AOS-526) | `--roteiro respeita_tool_choice` | sem a regra `tool_choice`: `recusado` por L1 e L2 (40 sem tool call à 1.ª); com ela: `qualificado` |
| Recusa `tool_choice` com 400 de parâmetro (AOS-526) | `--roteiro recusa_tool_choice` com um perfil com a regra | `recusado`: L1 (os 400 contam como erro de parâmetro); sem a regra: `qualificado` |

`troca_modelo` só discrimina no modo `falso`: atrás do proxy quem declara o modelo servido é o
proxy, e o mesmo ensaia-se com um perfil que espera outro nome. No modo `falso` o provider
declara o `expected_model` do perfil; no modo `proxy` o perfil tem de esperar
`openai/modelo-falso-do-banco`.

### A regra `params.tool_choice` (AOS-526)

Um perfil candidato pode declarar `"params":{"tool_choice":"enquanto_o_contrato_nao_estiver_satisfeito"}`
(a omissão é `nunca`; outro valor recusa o perfil). Com ela, o pedido do 1.º turno de cada nó
com tools leva `tool_choice` — na bateria, a tool do contrato nomeada,
`{"type":"function","function":{"name":"arquivo"}}`, porque cada nó oferece uma só; com mais de uma
tool por cumprir iria `"required"`. A regra: o campo `tool_choice` só vai enquanto o run ainda não pediu tool nenhuma e o contrato de conclusão tem tools por cumprir que o pedido oferece — a tool nomeada se for uma, `"required"` se forem várias —, e a partir da primeira tool call do run, seja de que tool for e recusada, falhada ou efectiva, nunca mais vai nesse run. O pedido que já leva o resultado
de uma tool, e o de um nó sem tools, não o levam; cada nova tentativa é um run novo, e pode ser
forçada no seu 1.º turno. Sem a regra, os pedidos são os de antes, byte a byte (o teste
pinado do AOS-516 continua com os mesmos digests).

Para medir o efeito numa rota, qualifica-se duas vezes **na mesma sessão**, com e sem a regra, e
compara-se L2 (sem tool call à 1.ª) e L1 (se os nós continuam a fechar cumpridos: um modelo
obrigado a chamar a tool pode não conseguir dar a resposta final). Um fornecedor que responda 4xx
ao campo faz a qualificação recusar a rota com L1 — os 4xx contam como erro de parâmetro, não como
problema da conta. Os dois falsos dos controlos negativos acima provam isto no modo `falso`; no
modo `proxy` os mesmos cenários estão no teste do proxy real, que só corre com Docker.

### O orçamento (decisão D7)

- O **pior caso** declara-se antes do primeiro pedido e confere-se contra o que resta do tecto
  do dia: 672 pedidos, mais 1 da sonda no modo real, mais 84 quando há corrida de controlo. Uma
  qualificação que não cabe **não envia nada** e fica escrita, com `inconclusivo`.
- **300 pedidos** por qualificação, contados antes de cada envio; ao chegar lá a corrida pára.
- **3 USD estimados** por qualificação, só quando o fornecedor tem tecto em dólares no ficheiro
  de chaves. Sem ele o relatório diz `tecto_micro_usd_imposto: false` e leva o aviso.
- **Por dia (UTC), no modo real: duas qualificações por perfil e quatro por rota** (fornecedor e
  modelo, com qualquer perfil — mudar um parâmetro do perfil não dá contador novo). Conta quando
  a sonda passa. A contagem é o maior entre o registo `qualificacoes.json` e os relatórios de
  qualificações reais de hoje.
- O contador do dia e o registo vivem na **pasta do dono** (`~/.aos-ensaio`), e não ao lado de
  `--chaves`: copiar o ficheiro de chaves para outra pasta não repõe nada.
- **O que isto não fecha:** quem tem a máquina apaga o registo e os relatórios, ou muda a pasta
  do utilizador. Os tectos protegem de enganos e de pressa, não de quem os quer contornar.
- Três respostas 4xx de parâmetro seguidas desde o primeiro pedido param a corrida.

### A imagem do proxy

A declaração é uma só (`ImagemDoProxy`, em `proxy.go`), presa ao compose de produção pelo guarda
do AOS-517. Nos modos `proxy` e `real` a qualificação recusa arrancar (exit 3, causa
`imagem_do_proxy_nao_e_a_de_producao`) se a imagem em uso não for essa. O digest vai em
`qualificacao_do_modelo.rota_qualificada`, com os da rota, do perfil e da bateria. O relatório
não vê a imagem que o proxy de produção corre de facto.

### O digest do próprio relatório

O campo `digest_do_relatorio`, no topo do JSON, é o sha256 da **forma canónica** do relatório
sem esse campo: o documento JSON em UTF-8, sem espaços nem quebras de linha, os membros de cada
objecto pela ordem dos bytes do nome, as listas pela ordem em que estão, os números com o
literal exactamente como está no ficheiro, e os textos como o `encoding/json` do Go os escreve
com o escape de HTML desligado. Recusam: um nome de membro repetido no mesmo objecto (sem
distinguir maiúsculas), UTF-8 inválido, e um texto com U+FFFD ou com meio par substituto isolado.
A definição completa está em `digest_relatorio.go`; `VerificarDigestDoRelatorio` confere um
ficheiro. Um relatório de qualificação nunca se escreve por cima de outro.

**O digest dá integridade, não autenticidade.** Quem altera o veredicto pode recalcular o digest
e o ficheiro verifica. Serve para um registo assinado citar um relatório exacto; a autenticidade
vem da assinatura desse registo (AOS-519). O resumo `.txt` não é coberto, e o digest não prova
que a corrida aconteceu.

**Uma corrida interrompida nunca dá `recusado`** (correcção do AOS-522). O cancelamento da corrida
— um Ctrl-C — podia apanhar um run depois de o pedido ao modelo ter tido 200: o runtime devolvia
um erro sem classe, o banco lia-o como falha do modelo, e cerca de uma em quarenta corridas
interrompidas saía `recusado` com um modelo que cumpria tudo. Um run que não fechou cumprido e
acabou com o contexto da corrida cancelado leva agora `corrida_cancelada` nas suas medidas: o nó
fica interrompido pela causa `cancelamento_da_corrida`, sai da amostra, e o run não alimenta
contagem nenhuma de limiar. Uma falha firme de um run que fechou **antes** da interrupção continua
a valer.

### Códigos de saída da qualificação

| Código | Significado |
|---|---|
| 0 | `qualificado`, no modo `real` |
| 9 | `qualificado` num ensaio (`falso` ou `proxy`): não vale para produção |
| 7 | `recusado` (mesmo que a corrida tenha parado a meio) |
| 8 | `inconclusivo`, com a corrida completa |
| 4 | `inconclusivo`, a corrida parou a meio |
| 3 | Recusada antes de enviar: o pior caso não cabe (com relatório `inconclusivo`), a imagem do proxy, as qualificações do dia, o `expected_model` do perfil |
| 10 | `--so-plano` de uma qualificação: plano válido, nada enviado |

### O que a qualificação não prova

Está no relatório, em `qualificacao_do_modelo.nao_prova` e nos `limites`: a bateria são seis
casos sintéticos com documentos curtos; `cumprido` e `aceite` são 2xx e não provam que o
fornecedor leu o que recebeu; com 40 nós e zero falhas só se afirma, a 95%, uma taxa de falha
abaixo de cerca de 9%; apanha um modelo mau e não mede se é bom; L5 compara o modelo que o
**proxy** declara, não o que o fornecedor serviu, e sem `--host-esperado` o endpoint não é
comparado; o veredicto vale para a rota e a versão da bateria que cita.

## Qualificar e preparar a assinatura (AOS-522)

`aos-ensaio qualificar <modo>` encadeia a qualificação, o resumo de uma página e a preparação do
registo de entrada do AOS-519. **Não assina**: prepara o que há para assinar e pára. O
procedimento, com os tempos, está em `docs/runbooks/PROC-BANCO-DE-ENSAIO.md`, «Entrar um modelo
novo, com relógio».

```bash
# Ensaio, sem modelo real: relatório e resumo, e nada para assinar (sai com 9).
go run ./cmd/aos-ensaio qualificar falso --perfil <perfil.json> --fatia 10 --board board-eu --regiao eu --saida /tmp/entrada --roteiro cumpre
```

O que faz, por ordem, e onde pára:

1. **Plano.** No modo `real` recusa em CI antes de ler seja o que for. `--perfil`, `--fatia`,
   `--board`, `--regiao` e `--saida` são obrigatórios e **não têm valor por omissão**; o que se
   escreve tem alfabeto fechado (`--fatia` só algarismos decimais; `--board` e `--regiao` de 1 a
   64 letras, dígitos, `_` e `-`; `--saida` sem aspas nem caracteres de controlo; o alias não pode
   ser um nome de dispositivo do Windows). Monta o registo com o que já sabe e passa-o por
   `routeentry.ParseEntry` — a leitura do nó: um perfil que devolve estado, um alias que não serve
   ou uma fatia fora de 0 a 100 saem com **11** e a causa do nó. Confere depois o alias contra a
   allowlist — a embebida, ou a externa de `--allowlist-dir` e `--ancora-da-allowlist` — para esse
   board e essa região (`nome_fora_da_allowlist`, também 11). Nada disto envia um pedido. A pasta
   de `--saida` **não pode existir**: é criada de forma exclusiva por esta corrida.
2. **Sonda, corrida, controlo.** É a experiência `qualificacao`, pela linha de comandos de sempre:
   os tectos, as duas qualificações por perfil por dia e a imagem do proxy conferem-se lá.
3. **Preparação.** Quatro barreiras independentes, cada uma com o seu teste: (1) o código de
   saída da corrida **e** o relatório dizem os dois `qualificado`, modo `real`, corrida completa,
   vale para produção — se só o código o disser, sai com 1; (2) o relatório relido do disco tem o
   digest do que a corrida escreveu (`relatorio_trocado_depois_da_corrida`, sai com 1); (3) o
   registo e o relatório passam por `routeentry.ParseEntry` e `routeentry.CheckReport` (sai com 11
   e a causa do nó); (4) o que ficou escrito em `pacote/` é, byte a byte, o que se conferiu
   (`pacote_alterado_depois_de_escrito`, sai com 1 e o pacote é retirado). Com qualquer outro
   desfecho escreve o resumo, diz porque não há nada para assinar, e **nunca sai com 0**.

O que fica em `--saida`:

| Ficheiro | O que é |
|---|---|
| `ensaio-<modo>-qualificacao-<data>.json` e `.txt` | O relatório da corrida e o seu resumo comprido, como sempre |
| `resumo-de-uma-pagina.txt` | O que o dono lê antes de assinar: no máximo 60 linhas de 100 colunas, por ordem fixa — avisos (`exigencia_nao_provada` antes de tudo), veredicto, razões, amostra, controlos negativos, o que a corrida não prova, gasto, o que se assina, e o relógio das fases. O bloco «o que vais assinar» sai do registo **lido de volta do pacote**, e traz tudo o que ele diz: alias, modelo esperado, fatia, board, região, wire, capacidades, versão da projecção, parâmetros do pedido e os quatro digests. «SUBSTITUI» ou «ACRESCENTA-SE» é segundo a tabela em código do binário do posto, e a linha diz a revisão dele |
| `pacote/<alias>.registo.json` e `pacote/<alias>.relatorio.json` | **Só com veredicto positivo.** O registo de entrada por assinar e o relatório que ele cita, byte a byte o da corrida. A assinatura (`<alias>.registo.sig`) é o dono que a põe lá, com o `aos-issuer route-profile-sign`; a linha exacta sai no fim |

`aos-ensaio conferir --pacote <pasta> --ancora <base64> --board B --regiao R` passa um pacote já
assinado por `routeentry.LoadDir`, a função que o nó corre no arranque, com a allowlist embebida
(ou a externa, com `--allowlist-dir` e `--ancora-da-allowlist`). Sai com 0 se o nó o aceitaria e
com **11** e `causa=<causa do nó> registo=<rótulo>` se não. Só lê material público.

Códigos de saída do `qualificar`: os da qualificação (0, 7, 8, 4, 3, 9, 10, 5, 6, 2), mais **11**
— o nó recusaria o registo, antes de enviar ou, se o relatório não passar nas conferências do nó,
depois da corrida — e **1** para qualquer outra falha (as causas próprias acima). Com 0 ficou um
pacote por assinar; com qualquer outro, não.

**Limites.** O resumo lê o relatório e não acrescenta garantia nenhuma. O plano confere a
allowlist que lhe dão, e a conferência antes da assinatura não vê a assinatura: é o `conferir`
que a vê — e nenhum dos dois sabe se a âncora, o board, a região e a allowlist que recebe são os
do servidor. O alfabeto do board e da região é deste comando (o nó só exige que não sejam
vazios). A criação exclusiva da pasta de saída protege de duas corridas para a mesma pasta, não
de quem mexe nela a meio: para isso há as barreiras 2 e 4, que detectam e não empacotam. O texto
de uso geral do banco não lista os dois subcomandos: têm o seu (`aos-ensaio qualificar`,
`aos-ensaio conferir`).

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
