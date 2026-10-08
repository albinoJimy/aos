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
| Erro do provider | Pedidos sem HTTP 200, e a distribuição dos códigos. |
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
rota para o fornecedor (`openai/<modelo>` para o Kimi, `anthropic/<modelo>` para a Anthropic),
e desmonta-a no fim.

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
| `--fornecedor kimi\|anthropic` | real | O fornecedor. |
| `--modelo M` | real | Um dos modelos do ficheiro (omissão: o primeiro). |
| `--precos F` | real | Tabela de preços. Obrigatória quando há tecto em dólares. |
| `--contador F` | real | Ficheiro do contador (omissão: ao lado do ficheiro de chaves). |
| `--so-plano` | real | Valida tudo, diz quantos pedidos faria e não envia nenhum. |

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
| 4 | Parou a meio (`tecto_atingido`, `contador_inutilizavel`, `interrompida`). O relatório parcial foi escrito e diz a causa. |
| 5 | Modo `real` pedido em CI. |
| 6 | Falta o Docker ou a imagem do proxy, ou o proxy não arrancou. |
| 1 | Outra falha. |

## O ficheiro de chaves, os tectos e o contador

O ficheiro de chaves é do dono e vive **fora do repositório**
(`%USERPROFILE%\.aos-ensaio\chaves.env`; o modelo está em `chaves.env.exemplo`, na mesma
pasta). Só o programa o lê, pelo caminho que lhe dão.

- **Recusas.** Sem ficheiro, com um campo obrigatório em falta ou ainda com o marcador `<…>`
  do exemplo, o modo real sai com 3 e uma mensagem que nomeia o **campo** — nunca o valor.
- **Tectos.** `TECTO_PEDIDOS_DIA_KIMI`, `TECTO_PEDIDOS_DIA_ANTHROPIC` e
  `TECTO_USD_DIA_ANTHROPIC`. Não há tecto por omissão: ausente, zero ou ilegível recusa o
  arranque. As opções da linha de comandos não mexem nos tectos do modo real.
- **Contador.** `contador.json`, ao lado do ficheiro de chaves (ou `--contador`). Persistente,
  por fornecedor e por dia (UTC). Cada pedido é contado **antes** de ser enviado. Um contador
  que existe e não se lê recusa tudo — não recomeça do zero; repara-se à mão.
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
- **Um processo de cada vez.** O contador não coordena dois ensaios sobre o mesmo ficheiro.

## O relatório

Dois ficheiros por corrida, na pasta `--saida` (omissão: `relatorios/` ao lado do ficheiro de
chaves no modo real; `~/.aos-ensaio/relatorios` nos outros):
`ensaio-<modo>-<experiência>-<data>.json` e `.txt`.

Campos do JSON: `data_utc`, `modo`, `experiencia`, `terminou`, `rota` (fornecedor, modelo,
digest), `regiao_de_processamento_declarada` (uma declaração do dono, sem efeito),
`protocolo` (projecção, layout, versão publicada por braço), `digests` (bateria, rota,
configuração), `plano` (amostras, semente, tentativas, turnos, casos, braços), `pedidos`
(previstos, enviados, tecto do dia, gastos, restantes), `custo` (estimativa), `taxas`,
`por_braco`, `por_caso`, `comparacoes` (só nos separadores), `limites` (o que a corrida **não**
prova) e `observacoes` (uma linha por run, em vocabulário fechado).

**Não leva:** chaves, cabeçalhos, o endereço da rota (só o seu digest), nem um byte de texto de
pedidos, de respostas ou de raciocínio. Está preso por teste, com sentinelas em todos os campos
do ficheiro de chaves e no texto de todas as respostas do provider falso
(`TestAOS512_Real_SemSegredosNemTextoEmSaidaNenhuma`,
`TestAOS512_Real_SegredosNoCaminhoDeErroDoProxy`).

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
diferença, se os intervalos se sobrepõem e o p-valor do teste de duas proporções — aritmética
sobre as contagens, não conclusões.
