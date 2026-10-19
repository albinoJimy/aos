# PROC-BANCO-DE-ENSAIO — Correr o banco de ensaio com um modelo real

| Campo | Valor |
|---|---|
| ID | PROC-BANCO-DE-ENSAIO |
| Versão | 1.0 |
| Tipo | Procedimento operacional (posto de ensaio local; não é procedimento de produção) |
| Ticket | AOS-512 (fase A2 da fronteira runtime↔modelo; decisão D5 do dono) |
| Componentes | `packages/qa/banco-ensaio` (binário `aos-ensaio`); imagem de produção do proxy, num contentor efémero |
| Referência | `packages/qa/banco-ensaio/README.md`; `docs/reports/desenho-a2-estado-opaco-2026-10-07.md` §5; `docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md` |

> **O ensaio não toca em produção.** Corre no posto local, com documentos sintéticos, contra o
> fornecedor do modelo. Não usa `ssh`, a fila de produção, o nó de produção nem o Vault. Se um
> passo deste procedimento parecer exigir alguma dessas coisas, o passo está errado: pára.

## Quem corre, e com quê

- **Só o dono**, no seu posto, com o seu ficheiro de chaves:
  `%USERPROFILE%\.aos-ensaio\chaves.env` (o modelo é `chaves.env.exemplo`, na mesma pasta).
- O ficheiro **não se abre, não se copia e não se cola** em lado nenhum. O programa lê-o pelo
  caminho. As chaves não vão em argumentos nem em variáveis de ambiente da consola.
- Precisa de Docker a correr e da imagem do proxy já descarregada (o banco não a descarrega):

  ```powershell
  docker image inspect ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63 --format "{{.Id}}"
  ```

- Nunca em CI: com `CI` ou `GITHUB_ACTIONS` definidas, o modo `real` recusa (exit 5).

## Antes da primeira corrida real

Lista de verificação da revisão adversarial (REV-512). Cada ponto tem hoje um mecanismo no
código; a lista fica porque é o dono que confirma.

1. **Um só processo.** Nenhum outro `aos-ensaio` a correr. Se houver, o segundo recusa (exit 3,
   «o contador esta em uso por outro processo»).
2. **Confirmar o destino impresso pelo `--so-plano`.** A linha
   `DESTINO DA CHAVE: https://api.kimi.com` tem de mostrar o host do fornecedor. Se o banco
   recusar o campo `KIMI_API_BASE`, é o ficheiro que está errado — não usar
   `--destino-fora-da-lista` para o fazer passar.
3. **Ficheiro de chaves sem aspas e sem campos repetidos.** O banco recusa os dois e diz o campo.
4. **Não fechar a janela nem matar o processo.** `Ctrl+C` pára entre dois pedidos e desmonta o
   proxy. Se o processo morrer à força, o proxy mata-se sozinho ao fim de cerca de 90 segundos
   sem sinal de vida — mas até lá guarda a chave em claro no ambiente do contentor.
5. **`limpar` no fim**, sempre, e ver que diz `removidos 0 contentor(es) e 0 rede(s)`:

   ```powershell
   & $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe limpar --chaves $env:USERPROFILE\.aos-ensaio\chaves.env
   ```

6. **Ler a comparação entre braços pelos intervalos e pelo p-valor corrigido** (Fisher exacto,
   Holm). Uma série de 429 a seguir a outro erro é do proxy, não do fornecedor.
7. **A sonda.** Antes do primeiro caso, o modo `real` faz **um** pedido mínimo ao modelo — conta
   no tecto do dia como um pedido, e o `--so-plano` anuncia-o («mais 1 de sonda»). Se a resposta
   não for 200, a corrida **não começa**: exit 4, um relatório sem observações com o campo
   `sonda`, e a causa em vocabulário fechado — `chave_recusada`, `saldo_insuficiente`,
   `limite_de_ritmo`, `modelo_desconhecido` ou `outro`. O `--so-plano` continua a não enviar
   nada, nem a sonda. Existe porque a primeira corrida real (2026-10-08) encontrou a conta sem
   saldo e gastou 212 pedidos do tecto a receber 429.

## Passos

1. **Compilar o binário para fora do repositório.**

   ```powershell
   cd C:\Jimy\AOS\packages\qa\banco-ensaio
   go build -o $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe .\cmd\aos-ensaio
   ```

2. **Ver o plano, sem enviar nada.** Valida o ficheiro de chaves, os tectos e o contador, e diz
   quantos pedidos a corrida faz e quantos restam hoje.

   ```powershell
   & $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor kimi --experiencia separadores --so-plano
   ```

   Saída esperada (em `stderr`): `modo REAL — fornecedor kimi, modelo <nome>; a corrida faz ate
   212 pedidos; hoje (<dia> UTC) restam N de 1000`, `DESTINO DA CHAVE: https://api.kimi.com
   (contador: <caminho>)` e `--so-plano: nenhum pedido foi enviado`. **Conferir o destino.**
   Exit 3 ⇒ a mensagem nomeia o campo do ficheiro a corrigir.

3. **Correr a experiência dos separadores** (a primeira corrida obrigatória): quatro braços sobre
   o caso T1, 53 amostras por braço, um pedido por amostra — **212 pedidos**, no modelo de
   produção (o primeiro de `KIMI_MODELOS`).

   ```powershell
   & $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor kimi --experiencia separadores
   ```

   O proxy demora até alguns minutos a arrancar. A corrida mostra uma linha de progresso a cada
   vinte passos. `Ctrl+C` pára entre dois pedidos, escreve o relatório parcial e desmonta o
   proxy. Se o fornecedor limitar a taxa (aparecem códigos 429 no relatório), repetir noutro dia
   com `--pausa 2s`.

4. **Ler o resultado.** O resumo sai no ecrã e fica em
   `%USERPROFILE%\.aos-ensaio\relatorios\ensaio-real-separadores-<data>.txt`; o relatório
   completo no `.json` ao lado. Olhar, por esta ordem, para: `terminou` (tem de ser
   `completa`), os códigos HTTP (erros do fornecedor tiram amostras ao denominador), e a taxa
   «sem tool call na 1.ª tentativa» por braço, com o intervalo de 95%.

5. **Registar** o resultado na §5 do acompanhamento, por braço, com o intervalo de confiança e
   a frase exacta do que **não** ficou provado (a secção «O QUE ESTA CORRIDA NAO PROVA» do
   resumo traz as frases). O relatório não tem segredos nem texto: pode ser copiado para o
   repositório ou colado numa conversa.

## A bateria inteira e a Anthropic

```powershell
# A bateria T1 a T6 (até 84 pedidos por passagem; na prática cerca de 12 a 30).
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor kimi --amostras 3

# A Anthropic: há tecto em dólares, logo a tabela de preços é obrigatória.
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor anthropic --precos $env:USERPROFILE\.aos-ensaio\precos.json --so-plano
```

A tabela de preços escreve-a quem corre, com a página de preços do fornecedor à frente (formato
no README do banco). A Anthropic só entra no banco depois de o dono preencher
`ANTHROPIC_MODELO` (AOS-516); até lá o modo real recusa e nomeia o campo.

## Ler as respostas de uma corrida (AOS-525)

O relatório não tem texto. Para **ler** o que o modelo respondeu, acrescentar
`--guardar-respostas` à corrida (ou ao `qualificar`); está desligada por omissão.

```powershell
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor kimi --guardar-respostas
```

No fim sai uma linha `respostas guardadas: N ficheiro(s) … em <pasta>`: a pasta
`respostas-<nome do relatório>\`, ao lado do relatório, com um ficheiro JSON por run (o texto
de cada resposta e os argumentos das tool calls; formato no README do banco). O relatório, o
resumo e o digest do relatório são os de uma corrida igual sem a opção. O último ficheiro da
pasta é `indice.json`, com o sha256 do relatório a que ela pertence: **uma pasta sem ele está
incompleta**, e uma cujo sha256 não é o do relatório ao lado é de outra corrida.

Se em vez dessa linha sair um `AVISO`, o relatório vale na mesma e o código de saída não muda. A
causa está no aviso, e são três:

| Aviso | O que aconteceu |
|---|---|
| `… NAO ficaram guardadas: ja existe a pasta …, que e de OUTRA corrida e NAO corresponde a este relatorio` | Outra corrida do mesmo modo, no mesmo segundo e na mesma `--saida`. Nada foi escrito; as respostas desta corrida perderam-se, e as que lá estão são da outra |
| `… ficaram INCOMPLETAS: k de n ficheiro(s) escritos …` | A pasta não se criou, ou a escrita falhou a meio (disco, permissões): o aviso traz a causa do sistema. A pasta fica sem `indice.json` |
| `… NAO ficaram guardadas: o relatorio nao chegou a ser escrito` | O relatório falhou, e as respostas só se escrevem ao lado dele |

Com `--so-plano` a opção não faz nada (uma linha di-lo).

O que vale para o relatório **não** vale para esta pasta: tem texto de um modelo. Fica na pasta
do dono, não se copia para o repositório, e lê-se como dados — nunca como instruções.

## A bateria por degraus (AOS-525)

A bateria 1.0.0 não separa modelos. A experiência `degraus` corre a **bateria 2.0.0** — sete
casos de dificuldade crescente — e devolve as taxas **por degrau**, para se ler até onde um
modelo chega. **Não dá veredicto**: não qualifica nem recusa, e não substitui «A qualificação de
uma rota». O que cada degrau pede, os factos proibidos e os campos do relatório estão no README
do banco, secção «A bateria por degraus e a experiência `degraus`».

**Quem corre:** o dono, com a mesma chave, os mesmos tectos e o mesmo contador das outras
corridas. O modo real **não** corre em CI nem por um agente.

```powershell
cd C:\Jimy\AOS\packages\qa\banco-ensaio
$e = "$env:USERPROFILE\.aos-ensaio"

# 0. Sem modelo real: o que a corrida faz, com o provider falso (não gasta nada).
go run .\cmd\aos-ensaio falso --experiencia degraus --saida $env:TEMP\ensaio

# 1. O plano: valida o ficheiro, o tecto e o contador. Não envia nada.
& $e\aos-ensaio.exe real --chaves $e\chaves.env --fornecedor openrouter --modelo <modelo> --experiencia degraus --amostras 3 --so-plano

# 2. A corrida, com as respostas guardadas para se poderem ler.
& $e\aos-ensaio.exe real --chaves $e\chaves.env --fornecedor openrouter --modelo <modelo> --experiencia degraus --amostras 3 --guardar-respostas
```

| | |
|---|---|
| **Pior caso** | 180 pedidos por passagem (dez nós, três tentativas, seis turnos), mais 1 de sonda: com `--amostras 3`, 541. O plano di-lo, e a corrida não arranca se não couber no que resta do tecto do dia. Com o provider falso que cumpre, uma passagem gasta 18 pedidos; o que um modelo real gasta está por medir. |
| **Tokens** | O degrau 3 lê um documento de 28,5 KB: é o que pesa. Com tecto em dólares, a tabela de preços é obrigatória, como sempre. |
| **Amostras** | Com uma passagem cada degrau tem **uma** amostra, e a taxa é 0 ou 1. Para comparar modelos, três ou mais — e lêem-se os intervalos de 95% do JSON, não só a fracção. |
| **Ordem** | Os degraus correm sempre do 1 ao 7 dentro de cada passagem: uma deriva da rota a meio da corrida cai nos últimos. |
| **Perfil** | Aceita `--perfil`, como a experiência `bateria`. Não aceita `--braco` (corre no braço A) nem `--estado`. |
| **Código de saída** | 0 se a corrida completou, **sejam quais forem as taxas**; 4 se parou a meio; 3 se não coube no tecto. |

**Como se lê.** O resumo abre com a tabela por degrau (o exemplo abaixo tem números
inventados, só para mostrar a forma):

```
POR DEGRAU (bateria 2.0.0; sem veredicto — taxas por degrau; os intervalos de 95% estao no JSON)
  degrau cumprido  interrompidas nos cumpridos sem tool 1.a  factos ausentes respostas erradas capacidade
  1      3/3       0 de 3        3/3           0/3           0/3             0/3               base
  3      1/2       1 de 3        2/2           0/2           1/2             1/2               documento longo com distractores
! 4      1/1       2 de 3        1/1           0/1           0/1             0/1               cruzar dois documentos
  ! = amostra reduzida por interrupcoes (metade ou mais das passagens do degrau sairam da amostra): a taxa nao e fiavel
```

- **`interrompidas` lê-se primeiro.** É o número de passagens que saíram de todas as taxas do
  degrau por uma causa passageira — 429, 5xx, erro de transporte, conta ou rota, tecto, corrida
  parada (a causa está em `interrompidas_por_causa`, no JSON). Não são falhas do modelo. No
  exemplo, o degrau 3 foi medido em duas passagens, não em três; um degrau com `0/0` não foi
  medido, e repete-se.
- **Uma linha que abre por `!` não se usa.** Metade ou mais das passagens desse degrau saíram
  da amostra: o «1/1» do degrau 4 do exemplo é uma passagem em três. Repete-se a corrida.
- Uma passagem que **já tinha falhado** antes de ser interrompida (um facto ausente, um
  proibido, um nó que não cumpriu por culpa do modelo) não sai: conta como não cumprida.
- Um modelo que estoura **sempre** o prazo do pedido aparece como interrompido, não como
  falhado: muitas interrompidas com a causa `erro_de_transporte` num só modelo são um resultado
  sobre esse modelo, e não um azar da rota.
- `cumprido` é o agregado, sobre as passagens que ficaram: todos os nós do caso fecharam o
  contrato, com os factos exigidos na saída e nenhum proibido.
- **Porque é que um degrau não saiu cumprido** lê-se nas três colunas seguintes, que não se
  excluem:
  - `nos cumpridos` abaixo do total: algum nó não fechou o **contrato de conclusão**. Cabe aqui
    tudo o que não é «cumprido» — não pediu a tool, a leitura foi negada e não houve outra,
    turnos esgotados, resposta vazia ou cortada, um 4xx de parâmetro. Qual deles, está nos
    desfechos do caso, mais abaixo no resumo («POR CASO»).
  - `factos ausentes` acima de zero: o nó concluiu e falta na saída um facto pedido.
  - `respostas erradas` acima de zero: o nó concluiu e a saída traz um valor proibido — um
    distractor, um valor que a pergunta não pedia, ou o que a instrução vedava.
- `sem tool 1.a` acima de zero **não** quer dizer degrau falhado: é a primeira tentativa, e o
  nó pode ter recuperado à segunda. É a mesma medida das outras experiências.
- Antes de concluir de uma ausência ou de uma resposta errada, **lêem-se as respostas** (ver
  «Ler as respostas de uma corrida»): a comparação é de texto, e conta um proibido mesmo numa
  frase que o nega.

**O que isto não prova.** Os degraus são sintéticos e curtos, um caso por degrau, e a ordem é a
que quem os escreveu julgou crescente — não uma escala medida (o 6 é provavelmente mais fácil
do que os 3 a 5). O degrau 5 mede «chega ao facto apesar da recusa», não «fez a segunda
chamada»: quem vai directo à cópia cumpre-o. «Degrau cumprido» é verificação
de factos por comparação de texto, não um juiz: não diz que a resposta é boa. Não substitui a
qualificação, e nenhum registo de entrada se assina com base nela. O que se regista na §5 do
acompanhamento são as contagens por modelo e por degrau, com as amostras e a data.

## O conselheiro, com dois fornecedores (AOS-528)

A experiência `conselheiro` corre a **bateria 3.0.0** («difícil», sete casos) em dois braços na
mesma corrida e com a mesma semente: `so-executor` (o executor sozinho) e `executor+conselheiro`
(o executor com a tool `aconselhar`, que consulta um modelo mais forte). **Não dá veredicto**:
dá, por braço e por caso, os casos cumpridos, a taxa de consulta, os pedidos e o custo estimado
do executor e do conselheiro separados, e o custo por caso cumprido. Os casos, a tool e os
campos do relatório estão no README do banco, secção «O conselheiro e a experiência
`conselheiro`».

**Quem corre:** o dono, com autorização para os dois fornecedores. O modo real **não** corre em
CI nem por um agente.

**As chaves necessárias.** As do executor e as do conselheiro, no MESMO ficheiro de chaves, pelos
campos de sempre: para o Kimi `KIMI_API_KEY`, `KIMI_API_BASE`, `KIMI_MODELOS` e
`TECTO_PEDIDOS_DIA_KIMI`; para a Anthropic `ANTHROPIC_API_KEY`, `ANTHROPIC_MODELO`,
`TECTO_PEDIDOS_DIA_ANTHROPIC` (e `TECTO_USD_DIA_ANTHROPIC`, opcional); para a OpenRouter
`OPENROUTER_API_KEY`, `OPENROUTER_MODELO` (uma lista; o conselheiro escolhe dela com
`--conselheiro-modelo`) e `TECTO_PEDIDOS_DIA_OPENROUTER` (e `TECTO_USD_DIA_OPENROUTER`). Um
modelo de conselheiro fora da lista do ficheiro é recusado, como o do executor. Com tecto em
dólares num dos fornecedores, `--precos` tem de ter o preço do modelo dele.
`--destino-fora-da-lista` só vale para o executor.

```powershell
cd C:\Jimy\AOS\packages\qa\banco-ensaio
$e = "$env:USERPROFILE\.aos-ensaio"

# 0. Sem modelo real: os dois braços com o conselheiro falso (não gasta nada).
go run .\cmd\aos-ensaio falso --experiencia conselheiro --conselheiro-falso certo --saida $env:TEMP\ensaio

# 1. O plano: executor Haiku pela OpenRouter, conselheiro Kimi pela conta Kimi. Não envia nada.
& $e\aos-ensaio.exe real --chaves $e\chaves.env --fornecedor openrouter --modelo anthropic/<haiku> --experiencia conselheiro --conselheiro-fornecedor kimi --conselheiro-modelo <kimi> --precos $e\precos.json --amostras 3 --so-plano

# 2. A corrida, com as respostas guardadas (as perguntas e os conselhos ficam nelas).
& $e\aos-ensaio.exe real --chaves $e\chaves.env --fornecedor openrouter --modelo anthropic/<haiku> --experiencia conselheiro --conselheiro-fornecedor kimi --conselheiro-modelo <kimi> --precos $e\precos.json --amostras 3 --guardar-respostas

# 3. O tecto: o modelo forte sozinho, outra corrida com a mesma semente, sem conselheiro.
& $e\aos-ensaio.exe real --chaves $e\chaves.env --fornecedor kimi --modelo <kimi> --experiencia conselheiro --precos $e\precos.json --amostras 3 --so-plano
```

O mesmo vale para outros pares: DeepSeek com Gemini são dois modelos da OpenRouter
(`--fornecedor openrouter --modelo deepseek/<…> --conselheiro-fornecedor openrouter
--conselheiro-modelo google/<…>`), e aí as duas contas somam na mesma linha do contador e o tecto
do dia é um só.

| | |
|---|---|
| **Pior caso** | Executor: 336 pedidos por passagem (dois braços, sete nós, três tentativas, oito turnos), mais 1 de sonda. Conselheiro: 7 × 3 × `--max-consultas` por passagem (42 com a omissão de 2), mais 1 de sonda. Com `--amostras 3` e dois fornecedores: 1009 no do executor e 127 no do conselheiro; no mesmo fornecedor, 1136 nele. A corrida do tecto (sem conselheiro): 168 por passagem, mais 1. O plano escreve o «PIOR CASO POR FORNECEDOR», e a corrida não arranca se o de algum não couber no que resta do dia dele. |
| **Contas** | Cada consulta conta no contador do fornecedor do CONSELHEIRO (o mesmo `contador.json`, na linha dele) e no tecto do dia dele; o custo estimado, pelo preço do modelo dele. O relatório traz-as à parte (`taxas.conselheiro.pedidos_do_conselheiro`, `taxas.conselheiro.custo_do_conselheiro`). |
| **Tokens** | O caso X4 lê um documento de cerca de 50 KB; o executor decide quanto dele manda ao conselheiro. |
| **Amostras** | Com uma passagem cada caso tem uma amostra por braço. Para comparar braços, três ou mais, e lêem-se os intervalos de 95% do JSON. |
| **Código de saída** | 0 se a corrida completou, sejam quais forem os números; 4 se parou a meio (também se o tecto do conselheiro se esgotou); 3 se o pior caso de algum fornecedor não coube. |

**Como se lê.** A tabela `CONSELHEIRO` do resumo, braço a braço: `cumpridos` (a regra do
degrau cumprido), `interrompidas` (lê-se primeiro, como nos degraus), `consulta` (runs que
consultaram, sobre os que tinham a tool), `cons/run`, os pedidos e o custo de cada modelo e o
**custo por caso cumprido**. Por baixo, caso a caso, os cumpridos de cada braço e as consultas.
Um caso em que o braço com conselheiro cumpre e o outro não é o que a experiência procura; antes
de o contar, **lêem-se as respostas** (a pergunta, o contexto e o conselho estão no ficheiro do
run).

**O que isto não prova.** A bateria é sintética e pequena. O conselheiro só vê o que o executor
lhe passa — um conselho errado pode ser culpa do que lhe mandaram. A taxa de consulta depende do
texto da tool e do esforço do modelo executor, e não é uma propriedade do conselheiro. A consulta
não passa pelo Model Gateway do nó de ensaio. O custo é estimado e conta as passagens
interrompidas. O tecto é outra corrida: uma deriva da rota entre as duas não se reparte. O modo
`proxy` não tem conselheiro. Acertar por palpite rende cerca de 1 em 3 a 1 em 4 em X1, X2, X3, X6
e X7 (a resposta é um de três ou quatro valores parecidos), e X5 mede obediência a instruções,
onde o conselheiro ajuda pouco. Um run com uma consulta sem resposta por causa passageira sai da
amostra, acerte ou erre. Uma consulta que esgota o prazo não regista uso: o gasto do conselheiro
pode ficar subestimado. O que se regista na §5 do acompanhamento são as contagens por
braço e por caso, os modelos e fornecedores, as amostras e a data.

## A qualificação da devolução do estado opaco (AOS-516)

Um perfil candidato com `devolver` diferente de `nunca` faz o banco ligar, sozinho, o que a
devolução exige (ADR-040 §2.11): a captura do estado, a governação da rota em `observe`, o
layout 1.5.0 e a projecção que o perfil nomeia (1.3.0). Sem um perfil desses o banco é o de
sempre. O relatório ganha o bloco «DEVOLUCAO DO ESTADO OPACO» (`taxas.devolucao_do_estado` no
JSON, também por caso): turnos com estado capturado, pedidos que o levaram, causas de não
devolução, recusas (o pedido **não saiu**) e respostas 4xx em pedidos que levaram estado.

**Sem modelo real** (não gasta tecto, não precisa do ficheiro de chaves):

```powershell
# Modo falso: o provider falso EXIGE de volta, byte a byte, o estado que emitiu.
go run ./packages/qa/banco-ensaio/cmd/aos-ensaio falso --estado exige --turnos-do-falso 3 --perfil <perfil.json> --saida <pasta>
# Controlos negativos: --estado exige com um perfil `nunca` (400 ao segundo pedido), e
# --estado proibe com um perfil `obrigatorio` (400 em pedidos que levaram estado).

# Modo proxy: a imagem de produção do proxy, rota anthropic/<modelo> do perfil, falso num contentor.
bash scripts/ci/banco-ensaio-proxy.sh
```

**Com o Claude** (só o dono; gasta o tecto do dia):

1. Preencher `ANTHROPIC_MODELO` no ficheiro de chaves e a tabela de preços desse modelo.
2. Copiar `packages/qa/banco-ensaio/perfis/claude-devolucao.exemplo.json` para a pasta do dono
   e trocar `PREENCHER-NOME-DO-MODELO` pelo **mesmo** nome. O `expected_model` tem de ser
   `anthropic/<ANTHROPIC_MODELO>`: é o que o proxy declara ter servido, e com outro nome o banco
   recusa antes de enviar (exit 3).
3. Ver o plano, sem enviar nada, e depois correr:

```powershell
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor anthropic --precos $env:USERPROFILE\.aos-ensaio\precos.json --perfil $env:USERPROFILE\.aos-ensaio\perfil-claude.json --amostras 8 --pausa 2s --so-plano
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor anthropic --precos $env:USERPROFILE\.aos-ensaio\precos.json --perfil $env:USERPROFILE\.aos-ensaio\perfil-claude.json --amostras 8 --pausa 2s
```

Oito passagens pela bateria são 40 nós com tools (critério P3) e, no máximo, 672 pedidos mais a
sonda: cabe no tecto de 1000 por dia. O tecto em dólares pára a corrida a meio se for atingido.

**Como ler o resultado.** Lê-se **um campo**: `qualificacao_da_devolucao.veredicto`. É o banco
que o calcula; as contagens só o explicam.

| Veredicto | O que quer dizer |
|---|---|
| `cumprida` | Houve turnos com raciocínio, todos os pedidos que o deviam levar foram **aceites pelo fornecedor** (2xx), e nada falhou pelo caminho. Só este qualifica P4 |
| `nao_cumprida` | A devolução falhou: há recusas do gateway, respostas 4xx a pedidos com estado, ou nós com tools que não fecharam `cumprido` |
| `sem_raciocinio` | Nenhum turno trouxe raciocínio nem assinatura: não havia nada a devolver, e a corrida **não prova nada** (o `thinking` não chegou ao fornecedor — ver o passo 2) |
| `inconclusiva` | Só há razões passageiras (429, 5xx, erro de transporte, tecto do dia, corrida a meio): repete-se |

`razoes` diz porquê, em vocabulário fechado. O banco conta como devolvido **só o que o
fornecedor aceitou**: a decisão do gateway (`decididos_a_devolver`) toma-se antes de o pedido
sair e, sozinha, não conta. Um envelope só com os ids das tool calls conta em
`turnos_so_com_ids_capturados` e fica fora da medida. Uma recusa com a causa
`estado_de_rota_nao_provada` não é do fornecedor: o proxy não declarou o modelo servido igual ao
do perfil (ver o passo 2).

Na corrida contra o Claude não se passa `--host-esperado`: o relatório leva
`endpoint_comparado: false`, e a rota prova-se só pelo modelo servido que o proxy declara.

**O que o banco ainda não mede.** O segundo turno com **um byte da assinatura alterado** contra o
modelo real (o controlo negativo do AOS-516) não tem opção no banco: o controlo «sem o estado»
faz-se com um perfil igual e `devolver: nunca` (com `projection_version` 1.2.0). E um provider
falso não valida assinaturas: o bloco de texto que o proxy acrescenta ao `assistant` de
`content` vazio, entre o raciocínio e a tool call, só o Claude diz se é aceite.

## O Claude pela OpenRouter (AOS-516)

Decisão do dono de 2026-10-10: o Claude qualifica-se pela OpenRouter, só no banco de ensaio e
com documentos de teste. A rota no proxy efémero é `openrouter/<autor>/<modelo>`; o destino da
chave é fixo (`https://openrouter.ai`) e o contador do dia é o do fornecedor `openrouter`.

**O perfil de exemplo já declara o que esta rota precisa** (medido atrás da imagem fixada do
proxy, com um provider falso, a 2026-10-10): `"devolver_em": "topo"` — o estado do turno
(`reasoning_details`) volta no topo da mensagem, onde a OpenRouter o lê, e não dentro de
`provider_specific_fields` — e `"params": {"reasoning": {"effort": "medium"}}`, a forma em que
ela documenta o pedido do raciocínio. Com este perfil o veredicto pode ser `cumprida`. Sem
`devolver_em` não pode: o melhor possível é `inconclusiva`, com a razão
`estado_devolvido_so_no_saco_do_proxy`.

1. O dono acrescenta ao seu ficheiro de chaves, à mão (sem aspas):

   ```
   OPENROUTER_API_KEY=<a chave da OpenRouter>
   OPENROUTER_MODELO=anthropic/claude-sonnet-4.5
   TECTO_PEDIDOS_DIA_OPENROUTER=200
   TECTO_USD_DIA_OPENROUTER=3
   ```

   `OPENROUTER_MODELO` é o nome na OpenRouter (`<autor>/<modelo>`), **sem** o prefixo
   `openrouter/`. Os tectos acima são um exemplo: são do dono. O tecto em dólares é opcional;
   com ele, a tabela de preços tem de ter uma entrada com a chave igual a `OPENROUTER_MODELO`
   (os preços confirmam-se na página do modelo na OpenRouter).
2. Copiar `packages/qa/banco-ensaio/perfis/claude-openrouter.exemplo.json` para a pasta do dono
   (por exemplo `perfil-claude-openrouter.json`) e trocar `PREENCHER-NOME-DO-MODELO`, de modo
   que `expected_model` fique `openrouter/<OPENROUTER_MODELO>` — com o modelo acima,
   `openrouter/anthropic/claude-sonnet-4.5`. É o que o proxy declara ter servido (medido); com
   outro nome o banco recusa antes de enviar (exit 3).
3. Ver o plano, sem enviar nada, e confirmar a linha `DESTINO DA CHAVE: https://openrouter.ai`.
   Depois correr, primeiro com uma passagem (no máximo 84 pedidos mais a sonda):

```powershell
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor openrouter --precos $env:USERPROFILE\.aos-ensaio\precos.json --perfil $env:USERPROFILE\.aos-ensaio\perfil-claude-openrouter.json --amostras 1 --pausa 2s --so-plano
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor openrouter --precos $env:USERPROFILE\.aos-ensaio\precos.json --perfil $env:USERPROFILE\.aos-ensaio\perfil-claude-openrouter.json --amostras 1 --pausa 2s
```

4. **O controlo negativo — faz parte da qualificação, não é opcional.** No modo real o banco só
   vê códigos HTTP: `cumprida` quer dizer que a OpenRouter respondeu 2xx a todos os pedidos que
   levavam o raciocínio devolvido, e **não** que o leu ou validou — um fornecedor que ignore o
   campo responde 2xx na mesma. Com `devolver_em: topo` a guarda do saco não se aplica e só
   resta o código. O indício possível sem ler conteúdo é correr a **mesma** corrida com um
   perfil igual que não devolve: copiar o perfil, pôr `"devolver": "nunca"`,
   `"projection_version": "1.2.0"` e tirar a linha `devolver_em`.

   | Corrida principal | Controlo (`devolver: nunca`) | Leitura |
   |---|---|---|
   | `cumprida` | 4xx nos segundos turnos (`nao_cumprida`) | A OpenRouter exige o raciocínio de volta e aceitou o que o gateway devolveu. É a qualificação |
   | `cumprida` | 2xx em tudo | A OpenRouter não exige o raciocínio de volta nesta rota: a corrida principal **não prova** a devolução |
   | `nao_cumprida` | qualquer | A devolução falhou; o código está no relatório |

   O relatório e o resumo em texto da corrida real trazem esta frase ao lado do veredicto.

**Como ler.** `saldo_insuficiente` na sonda: a conta da OpenRouter não tem créditos (402).
`rota_indisponivel` na sonda: a OpenRouter conhece o modelo mas não tem endpoint que sirva o
pedido com as definições da conta — ver a política de dados e os fornecedores permitidos na
conta, e se o modelo suporta tools; repetir não adianta.
`cumprida`: houve turnos com raciocínio e a OpenRouter aceitou todos os pedidos que levaram o
estado no topo. `sem_raciocinio`: o `reasoning` do perfil chegou à OpenRouter (medido) mas não
ligou o raciocínio — experimentar `{"max_tokens": 2048}` em vez de `{"effort": "medium"}`.
`nao_cumprida` com 4xx em pedidos com estado: a OpenRouter recusou o segundo turno; o código
está no relatório. `inconclusiva` com `estado_devolvido_so_no_saco_do_proxy`: o perfil não tem
`devolver_em: topo`.

**O que esta corrida não prova:** a rota `anthropic/` directa, nem o bloco de texto que o proxy
acrescenta nessa rota; e o adaptador `openrouter` do proxy identifica-se à OpenRouter com dois cabeçalhos que são do
LiteLLM, não do banco — medido na imagem fixada: `HTTP-Referer: https://litellm.ai` e
`X-Title: liteLLM`, em todos os pedidos. São os valores por omissão da imagem; o banco não passa
ao contentor nenhuma variável de onde pudessem vir outros, e o cenário atrás do proxy fica
vermelho se chegar outro valor.

## A qualificação de uma rota (AOS-518)

A experiência `qualificacao` dá um veredicto — `qualificado`, `recusado` ou `inconclusivo` —
calculado das contagens por uma regra fixa, que vai escrita no relatório. A regra, as razões, os
avisos e o digest do relatório estão no README do banco, secção «A qualificação do modelo».
**Só o modo `real` qualifica**: um `qualificado` dos modos `falso` e `proxy` leva
`vale_para_producao: false` e sai com o código 9.

**Antes da primeira qualificação, no ficheiro de chaves** (só o dono lhe mexe):

- O tecto diário de pedidos do fornecedor tem de caber o **pior caso**: **673** pedidos (672 e a
  sonda), ou **757** se o perfil declarar `devolver: obrigatorio` (mais 84 da corrida de
  controlo). Para duas qualificações no mesmo dia, 1500 chega. O gasto real medido ronda 13
  pedidos por passagem; a conta faz-se pelo pior caso de propósito.
- Num fornecedor com tecto em dólares (`TECTO_USD_DIA_…`), esse tecto tem de estar acima de
  3 USD, e `--precos` é obrigatório: a qualificação pára aos 3 USD estimados. Num fornecedor sem
  tecto em dólares o tecto de 3 USD **não é imposto**, e o relatório di-lo.

O contador do dia (`contador.json`) e o registo das qualificações (`qualificacoes.json`) vivem
na pasta do dono, `%USERPROFILE%\.aos-ensaio`, esteja o ficheiro de chaves onde estiver.

**Sem modelo real** (não gasta tecto):

```powershell
cd C:\Jimy\AOS\packages\qa\banco-ensaio
go run .\cmd\aos-ensaio falso --experiencia qualificacao --perfil <perfil.json> --roteiro cumpre --saida $env:TEMP\ensaio
```

**Com o modelo real**, no posto do dono (o Kimi de produção, como exemplo):

```powershell
cd C:\Jimy\AOS\packages\qa\banco-ensaio
go build -o $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe .\cmd\aos-ensaio

# 1. O plano: valida o ficheiro, o perfil, a imagem do proxy, o tecto e as qualificações de hoje. Não envia nada. Sai com 10.
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor kimi --experiencia qualificacao --perfil <perfil.json> --so-plano

# 2. A qualificação.
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor kimi --experiencia qualificacao --perfil <perfil.json>
```

O perfil é o JSON de sempre (`--perfil`), com o `expected_model` igual à rota da corrida:
`openai/<modelo do Kimi>`, `anthropic/<modelo>` ou `openrouter/<autor>/<modelo>`. Com outro
`expected_model` a qualificação é recusada antes de enviar. O `requested` é, desde o AOS-519, o
**nome que o nó de produção vai pedir** (o alias, por exemplo `gpt-4o`) — o digest do perfil
inclui-o, e só assim o `digest_do_perfil` do relatório é o do perfil que o nó carrega. Serve
qualquer nome de 1 a 64 letras, dígitos, `_` e `-` (sem ponto: entra num nome de stream);
`rota-de-ensaio` continua a servir para corridas que não são para assinar.

**Ler o resultado.** As primeiras linhas do resumo (e do ficheiro `.txt`) são os avisos, se os
houver; a seguir vem o veredicto com as razões. O código de saída diz o mesmo: 0 `qualificado`,
7 `recusado`, 8 `inconclusivo` com a corrida completa, 4 se parou a meio, 3 se foi recusada
antes de enviar.

| Saída | O que quer dizer | O que fazer |
|---|---|---|
| `QUALIFICADO`, sem avisos | 40 nós com tools e 16 sem tools cumpridos, nenhum limiar violado | O relatório `.json` e o seu `digest_do_relatorio` são o que o registo de entrada cita (AOS-519). Para o preparar sem trabalho à mão: «Entrar um modelo novo, com relógio», abaixo |
| `AVISO — EXIGENCIA_NAO_PROVADA` | O perfil declara devolução obrigatória e o controlo sem devolução não foi recusado em todos os segundos turnos | Não bloqueia. Quem assina fica a saber que «aceite» não é «exigido» |
| `AVISO — FACTOS_AUSENTES_EM_RESPOSTAS_CUMPRIDAS` | Há respostas cumpridas sem os factos do documento | Não bloqueia. Ver a taxa «factos ausentes da saída» antes de assinar |
| `AVISO — TECTO_EM_DOLARES_NAO_IMPOSTO` | O fornecedor não tem tecto em dólares no ficheiro de chaves | Actuou só o tecto de 300 pedidos |
| `RECUSADO` | Uma razão firme. Repetir não a tira | Ler as razões e a tabela de limiares. **Os limiares não se mexem para dar verde** sem decisão do dono registada na §4 do acompanhamento |
| `INCONCLUSIVO` | Só razões passageiras: conta, ritmo, 5xx, tecto, corrida parada, devolução declarada e não medida | Resolver a causa e repetir. Uma corrida que passou da sonda conta como uma das do dia |
| Exit 3, `qualificacoes_do_dia_esgotadas` | Duas qualificações do perfil, ou quatro da rota, hoje (UTC) | Esperar pelo dia seguinte. Apagar o registo não repõe: contam também os relatórios de hoje |
| Exit 3, `imagem_do_proxy_nao_e_a_de_producao` | A imagem do proxy em uso não é a declarada | Não há opção que o contorne |
| Exit 3, «o pior caso … nao cabe no tecto do dia» | O que resta do tecto diário é menos do que o pior caso | Subir o tecto no ficheiro de chaves, ou esperar pelo dia seguinte. Fica um relatório `inconclusivo`; nenhum pedido saiu |

**Limites que este procedimento não fecha.** O relatório é produzido no posto de quem corre e
não é assinado pelo banco: o digest dá integridade, não autenticidade, e não prova que a corrida
aconteceu. Os tectos do dia protegem de enganos, não de quem apaga a pasta do dono. A
qualificação apanha um modelo mau; se é bom mede-se com a fatia em produção (AOS-520, AOS-521).
O modo real não corre em CI nem no servidor (D3).

## Entrar um modelo novo, com relógio (AOS-522)

Do «tenho uma chave» ao «o nó carregou o perfil». São os passos 0 a 8 do desenho da A3 (§3).
**O relógio começa no passo 1 e pára no fim do passo 6**; o critério da fase é menos de uma hora.
Os tempos são os **esperados** (hipótese do desenho): a medição é do AOS-523, com modelo real, e
o resumo do passo 2 traz a hora de início e de fim de cada fase da máquina para ela.

| # | Passo | Quem | Prova que fica | Tempo esperado | Onde se pára |
|---|---|---|---|---|---|
| 0 | Pré-condições, uma vez: fornecedor e região aprovados; a allowlist assinada autoriza o alias; o proxy de produção tem a rota e é a imagem fixada; a chave dos perfis existe e a âncora está no servidor (`deploy/server/README.md`, «Perfis de rota assinados») | dono | allowlist assinada; âncora no `.env` | fora do relógio | Sem uma delas não se começa |
| 1 | Escrever o perfil candidato (`requested` = o alias de produção; `expected_model` = a rota) | dono | o ficheiro | 5 min | — |
| 2 | `aos-ensaio qualificar real …` (abaixo): plano, sonda, bateria 8 vezes, veredicto, resumo de uma página | máquina | `--saida`: relatório, resumo e, só com `qualificado`, `pacote\` | 15 a 25 min | Exit 11 **antes de enviar**: o nó recusaria o registo, ou a allowlist não autoriza o alias (a causa vem na linha) |
| 3 | Recusa automática | máquina | relatório e resumo, **sem** `pacote\` | — | Exit 7, 8, 4 ou 3: **acaba aqui** (ver a tabela dos veredictos) |
| 4 | Ler o resumo todo e assinar, com a linha que o passo 2 imprimiu | dono | `pacote\<alias>.registo.sig` | 5 min | Se o resumo não é o que queres assinar, não assinas |
| 5 | Conferir no posto, drenar a rota, copiar os três ficheiros, recriar o nó, ler o banner («Antes de recriar o nó», abaixo) | dono; a máquina confere | saída do `conferir`; banner `AOS-519` | 5 min, mais o tempo de drenar | `conferir` com exit 11: **não se copia** |
| 6 | O nó serve a fatia assinada ao candidato | máquina | manifesto dos turnos | minutos, havendo tráfego | **O relógio pára aqui.** Enquanto o AOS-520 não estiver em produção a fatia é só lida e mostrada |
| 7 | Vigiar e recuar (disjuntor) | máquina | evento selado | contínuo | É do AOS-521 |
| 8 | Passar a titular, ou sair: outro acto assinado | dono | segundo registo | fora do relógio | — |

**Passo 2 — o comando.** No posto do dono; nunca em CI nem no servidor (D3). `--board` e
`--regiao` são os do **nó de produção** e não têm valor por omissão; `--fatia` é a fatia inicial
(a decisão D4 é 10; só algarismos). A pasta de `--saida` **não pode existir**: é criada pela
corrida, uma por corrida — duas corridas para a mesma pasta, a segunda sai com 3 sem tocar em
nada. Se o nó monta uma allowlist externa, juntar `--allowlist-dir <pasta> --ancora-da-allowlist
<base64>`; sem isso o plano confere a embebida, e di-lo.

```powershell
cd C:\Jimy\AOS\packages\qa\banco-ensaio
go build -o $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe .\cmd\aos-ensaio
$saida = "$env:USERPROFILE\.aos-ensaio\entradas\<alias>-<data>"

# O plano: le o registo como o no o le, confere o alias na allowlist, o tecto e as qualificacoes de hoje. Nao envia nada. Sai com 10.
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe qualificar real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor kimi --perfil <perfil.json> --fatia 10 --board <board do no> --regiao <regiao do no> --saida $saida --so-plano

# A qualificacao. No fim imprime o resumo e, com `qualificado`, a linha exacta de assinar e a de conferir.
& $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe qualificar real --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --fornecedor kimi --perfil <perfil.json> --fatia 10 --board <board do no> --regiao <regiao do no> --saida $saida
```

**O que o plano confere, e o que não.** Confere, com as funções do nó: o formato do registo, a
fatia (0 a 100), o perfil pela leitura fechada, o alias, que o perfil não devolve estado, a imagem
do proxy declarada, e que a allowlist (a embebida, ou a externa que lhe deres) autoriza o alias
para esse board e essa região. **Não confere**, porque ainda não existem: o relatório — é
conferido no fim da corrida, antes de haver pacote — e a assinatura, que só o `conferir` vê. E
não sabe se o board, a região e a allowlist que lhe deste são os do servidor.

O comando **não assina** e não tem opção, variável nem ficheiro por onde a chave de assinar
entre. Só prepara o registo da corrida que acabou de fazer: não aceita um relatório de fora nem
um veredicto por argumento. `resumo-de-uma-pagina.txt` diz, por esta ordem: os avisos; o
veredicto; as razões; a amostra; os controlos negativos; **o que a corrida não prova**; o gasto;
o que vais assinar — lido de volta do registo que ficou escrito no pacote: alias, modelo
esperado, fatia, board, região, wire, capacidades, versão da projecção, parâmetros do pedido, se
**substitui** a tabela em código (a do binário do posto, que pode não ser a do nó: a linha diz a
revisão), e os quatro digests; e o relógio.

**O que fazer em cada veredicto.**

| Saída | O que ficou | O que fazer |
|---|---|---|
| 0, `QUALIFICADO` | relatório, resumo e `pacote\` (registo e relatório) | Ler o resumo. Se há avisos, são a primeira coisa da página: decide com eles à frente. Passo 4 |
| 7, `RECUSADO` | relatório e resumo | Acaba aqui. Uma razão firme não sai a repetir; os limiares não se mexem sem decisão registada |
| 8 ou 4, `INCONCLUSIVO` | relatório e resumo | Resolver a causa (conta, ritmo, tecto) e repetir **noutra pasta**. Conta como uma das duas do dia se passou da sonda. **Um Ctrl-C dá sempre `inconclusivo`, nunca `recusado`**: o run apanhado pelo cancelamento sai da amostra |
| 3 | nada, ou um relatório `inconclusivo` | Tecto do dia, qualificações do dia esgotadas, imagem do proxy, ou a pasta de saída já existia. A linha diz qual |
| 11, antes de enviar | nada | O nó recusaria o registo: `perfil_devolve_estado`, `nome_da_rota_invalido`, `fatia_fora_do_intervalo`, `perfil_invalido`, `nome_fora_da_allowlist`; no `qualificar-ocr` (registo de uso `tool`, AOS-530) também `perfil_de_tool_com_capacidades_erradas` (o perfil não declara `imagem`, ou declara `tools`). Corrigir o perfil, a fatia ou a allowlist. Nenhum pedido saiu |
| 11, depois da corrida | relatório e resumo, **sem** `pacote\` | O relatório que a corrida escreveu não passa nas conferências do nó (a causa vem na linha e no resumo). Não devia acontecer: guardar a pasta e não repetir às cegas — a corrida contou como uma das do dia |
| 1 | relatório e resumo, **sem** `pacote\`; ou nada | Outra falha, e nunca fica pacote: o relatório no disco deixou de ser o da corrida (`relatorio_trocado_depois_da_corrida`), o pacote foi alterado depois de escrito (`pacote_alterado_depois_de_escrito`), o código de saída e o relatório não dizem o mesmo, ou um ficheiro não se escreveu. Alguma coisa mexeu na pasta durante a corrida: ver o quê antes de repetir |
| 2 | nada | Argumentos: falta uma opção; `--fatia` que não é só algarismos; `--board` ou `--regiao` fora de 1 a 64 letras, dígitos, `_` e `-`; `--saida` com aspas ou caracteres de controlo; alias com nome de dispositivo do Windows (`NUL`, `CON`, `COM1`…); allowlist externa que não se carrega |
| 9 | relatório e resumo | Foi um ensaio (`falso` ou `proxy`): não vale para produção e não há nada para assinar |

**Passo 4 — assinar.** A linha vem no fim do passo 2; só falta o caminho da tua seed.

```powershell
aos-issuer route-profile-sign --entry "$saida\pacote\<alias>.registo.json" --report "$saida\pacote\<alias>.relatorio.json" --key-file "$env:USERPROFILE\.aos-perfis\perfis.seed" --out "$saida\pacote\<alias>.registo.sig"
```

### Antes de recriar o nó

1. **Conferir o pacote no posto**, pela função que o nó corre no arranque. Aceite aqui, é aceite
   pelo nó; recusado aqui, a causa é a palavra que o nó daria — e um nó que recusa **não arranca**.

   ```powershell
   & $env:USERPROFILE\.aos-ensaio\aos-ensaio.exe conferir --pacote "$saida\pacote" --ancora <ancora dos perfis> --board <board do no> --regiao <regiao do no>
   ```

   Se o nó monta uma allowlist externa, juntar `--allowlist-dir <pasta> --ancora-da-allowlist
   <base64>` (a linha que o `qualificar` imprimiu já os leva, se lhos deste); sem isso confere-se
   a embebida, e a saída di-lo. Comparar o `digest do registo` com
   o que o `route-profile-sign` imprimiu. O `conferir` não sabe se a âncora, o board e a região
   que lhe deste são os do servidor: isso confirma-se no `.env` de lá.
2. **Drenar a rota** — obrigatório, e o nó não o faz por ti: `deploy/server/README.md`, «Perfis de
   rota assinados», passo 5.
3. **Copiar** os três ficheiros de `pacote\` para a pasta de perfis do servidor. Só esses três: um
   ficheiro a mais (o resumo, por exemplo) faz o nó recusar com `ficheiro_nao_reconhecido`.
4. **Recriar** o nó: `docker compose -f docker-compose.prod.yml up -d --force-recreate aos`.
5. **Ler o banner**: `docker logs aos-aos-1 2>&1 | grep 'AOS-519'` — uma linha `perfil de rota
   ASSINADO carregado` com o alias, `SUBSTITUI` ou `ACRESCENTA-SE`, e o digest do registo, que tem
   de ser o que o `conferir` mostrou. Voltar a ligar o que submete.

**Recuo.** Drenar, retirar os três ficheiros da pasta do servidor, recriar o nó, ler o banner
(`zero perfis assinados`, ou os que ficaram). O nó volta à entrada da tabela em código.

**Limites.** O resumo é uma leitura do relatório, não uma garantia: o que a corrida não prova
está na página. O `conferir` vê os ficheiros, não o servidor. As duas variáveis e a montagem da
pasta estão no compose de produção, desligadas; ligá-las no servidor é do dono
(`deploy/server/README.md`, «Ligar a fase A3 em produção»). Um registo não expira e não há anti-recuo.

## Medir um motor de OCR (AOS-529)

A experiência `ocr` corre um motor de OCR sobre a bateria de imagens sintéticas e escreve o
relatório. Não toca em produção. Detalhe em `packages/qa/banco-ensaio/README.md`, «A medição do
OCR».

1. **A imagem do Tesseract tem de estar no posto.** O banco não a descarrega (`--pull never`).
   Constrói-se com o Dockerfile do banco (precisa de rede para o `apt-get`; é decisão do dono):

   ```powershell
   docker build -t aos-ensaio-ocr:tesseract packages/qa/banco-ensaio/ocr
   docker image inspect aos-ensaio-ocr:tesseract --format "{{.Id}} {{.Size}}"
   ```

   O ID (`sha256:<64 hex>`) é o que se dá a `--imagem`: uma imagem construída no posto não tem
   digest de registo. Uma imagem de registo dá-se como `nome@sha256:<64 hex>`, sem etiqueta.

2. **Ver as imagens da bateria**, se for a primeira vez:

   ```powershell
   go run ./cmd/aos-ensaio ocr bateria --imagens $env:TEMP\ocr-imagens
   ```

3. **Ensaiar com o motor falso** (prova que o relatório distingue; não mede nada):

   ```powershell
   go run ./cmd/aos-ensaio ocr --motor falso --falso erra-factos --saida $env:TEMP\ensaio
   ```

4. **Medir o Tesseract**, com os limites do gVisor (sem rede, 45 s, 1 GiB, 512 processos, raiz só
   de leitura, uid 65532) mas com o runc do Docker:

   ```powershell
   go run ./cmd/aos-ensaio ocr --motor tesseract --imagem sha256:<ID>
   ```

   Sai com 0 se a corrida completou, sejam quais forem as taxas; com 6 se não há Docker ou a
   imagem não está no posto; com 2 se a imagem não está fixada. Uma imagem que passa os 45 s ou
   esgota a memória fica `falhou:tempo` ou `falhou:memoria`, e a corrida continua.

5. **Medir um modelo de visão** (pago; só o dono, com o seu ficheiro de chaves, nunca em CI).
   Primeiro o plano, que não envia nada e mostra o pior caso (13 pedidos) e o destino da chave:

   ```powershell
   go run ./cmd/aos-ensaio ocr --motor visao --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --visao-fornecedor openrouter --visao-modelo M --precos P --so-plano
   ```

   Depois a mesma linha sem `--so-plano`. O modelo tem de estar na lista do fornecedor no
   ficheiro de chaves; os pedidos contam no contador e nos tectos desse fornecedor. O PDF fica
   fora deste braço. Uma recusa da chave, da conta ou do ritmo pára a corrida (sai com 4).

6. **Ler o resumo**: os factos pela regra do banco e com os acentos exactos, por degradação, por
   formato e com e sem acentos, e no modelo de visão o custo estimado. Desde o AOS-530 o resumo
   traz o **veredicto** contra o limiar de **90% dos factos** — já não é proposta: é a decisão D7
   do dono, de 2026-10-15 (`--limiar-de-factos` só aperta). `qualificado` exige também que nenhuma
   imagem nas contas tenha ficado por ler (uma resposta cortada pelo limite de tokens falha a
   imagem). A saída do `ocr` não depende do veredicto. Com `--guardar-respostas`, o texto lido de
   cada imagem fica na pasta `respostas-<relatório>/`, e só lá.

7. **Preparar o registo de uso `tool` do alias da tool de OCR** (AOS-530, ADR-042 §2.3), quando o
   dono decidir. É o `qualificar` para uma tool: mede o perfil do alias com o modelo de visão e,
   só com `qualificado` do modo real, deixa o registo (`"uso": "tool"`, fatia 0) e o relatório em
   `pacote\`, por assinar. Primeiro ensaiar com o motor falso (sai 9, sem pacote):

   ```powershell
   $saida = "$env:TEMP\entrada-ocr-$(Get-Date -Format yyyyMMdd-HHmmss)"
   go run ./cmd/aos-ensaio qualificar-ocr --motor falso --perfil <perfil-ocr.json> --board <board do no> --regiao <regiao do no> --saida $saida
   ```

   Depois o modo real (pago; só o dono):

   ```powershell
   go run ./cmd/aos-ensaio qualificar-ocr --motor visao --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --visao-fornecedor openrouter --visao-modelo M --precos P --perfil <perfil-ocr.json> --board <board do no> --regiao <regiao do no> --saida <pasta nova>
   ```

   O perfil declara a capacidade `imagem` e não declara `tools`, e o seu `expected_model` tem de
   ser a rota da corrida (`openrouter/<modelo>` na OpenRouter, `openai/<modelo>` no Kimi,
   `anthropic/<modelo>` na Anthropic): senão sai 3 antes de enviar. Conta nas **qualificações do
   dia** (duas por perfil, quatro por rota), no mesmo registo das qualificações dos candidatos.
   Saídas: 0 pacote preparado (assinar e conferir como no passo 4 do «Entrar um modelo novo»); 7
   recusado; 9 qualificado com o motor falso; 11 o nó recusaria o registo; 3 recusada antes de
   enviar (tecto, qualificações do dia, `expected_model` de outra rota). O nó recusa ainda um
   registo `tool` para o alias do seu titular — o `conferir` lembra-o, porque o posto não o sabe.

## Medir um modelo de vídeo (AOS-537)

A experiência `video` manda cada vídeo da bateria sintética, inteiro, a um modelo capaz de vídeo
(parte `video_url`, data URL `video/mp4`), e escreve o relatório com o veredicto contra 90% dos
factos. Não toca em produção. Detalhe em `packages/qa/banco-ensaio/README.md`, «A medição do vídeo».

1. **A imagem do ffmpeg tem de estar no posto.** O banco não a descarrega (`--pull never`). Já foi
   descarregada pelo dono; para confirmar:

   ```powershell
   docker image inspect jrottenberg/ffmpeg@sha256:8ec1ee1f6a0fcd37c97725827b6b7832795c9596e3439b8da56d7700d61ae778 --format "{{.Id}} {{.Size}}"
   ```

2. **Gerar a bateria com o ffmpeg real e conferir os MP4**, se for a primeira vez (não gasta nada):

   ```powershell
   cd packages/qa/banco-ensaio
   $env:AOS_BANCO_FFMPEG = "1"; go test -run '^TestAOS537_FFmpegReal_GeraABateria$' -v -count=1 .; Remove-Item Env:AOS_BANCO_FFMPEG
   go run ./cmd/aos-ensaio video bateria --videos $env:TEMP\video-mp4 --fotogramas $env:TEMP\video-mp4
   ```

3. **Ensaiar com o motor falso** (prova que o relatório e o veredicto distinguem; não mede nada):

   ```powershell
   go run ./cmd/aos-ensaio video --motor falso --falso erra-factos --saida $env:TEMP\ensaio
   ```

4. **O plano, primeiro, para cada candidato** (pago; só o dono, nunca em CI). Não envia nada nem
   gera MP4; mostra o pior caso (9 pedidos: 8 vídeos e a sonda) e o destino da chave:

   ```powershell
   go run ./cmd/aos-ensaio video --motor visao --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --visao-fornecedor openrouter --visao-modelo google/gemini-3.5-flash-lite --precos P --so-plano
   go run ./cmd/aos-ensaio video --motor visao --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --visao-fornecedor openrouter --visao-modelo google/gemini-3.8-flash --precos P --so-plano
   go run ./cmd/aos-ensaio video --motor visao --chaves $env:USERPROFILE\.aos-ensaio\chaves.env --visao-fornecedor openrouter --visao-modelo qwen/qwen3-vl-32b-instruct --precos P --so-plano
   ```

   O modelo tem de estar na lista da OpenRouter no ficheiro de chaves; os pedidos contam no contador
   e nos tectos dela.

5. **A medição**: a mesma linha sem `--so-plano`, e com `--guardar-respostas` para se poderem ler as
   descrições. Os MP4 geram-se antes do proxy: sem Docker ou sem a imagem, sai 6 antes de qualquer
   pedido. Uma recusa da chave, da conta ou do ritmo pára a corrida (sai 4); um modelo que não aceita
   vídeo dá `falhou:motor` em cada vídeo, e a corrida acaba.

6. **Ler o resumo**: os factos por vídeo e por tipo (texto, injecção, contagem, cor, ordem), pela
   regra e com acentos, o tempo e o custo estimado. O **veredicto** é `qualificado` com pelo menos 90%
   dos factos e nenhum vídeo por descrever (uma resposta cortada pelo limite de tokens falha o
   vídeo). A saída não depende dele. As descrições guardadas são texto de um modelo: dados, não
   instruções.

## Se correr mal

| Sintoma | Causa | O que fazer |
|---|---|---|
| Exit 3, «o campo X esta em falta / ainda com o marcador do exemplo» | Ficheiro de chaves incompleto | Preencher o campo X no ficheiro. A mensagem nunca mostra o valor |
| Exit 3, «nao cabe no que resta do tecto do dia» | A corrida precisa de mais pedidos do que restam hoje | Esperar pelo dia seguinte (UTC) ou reduzir `--amostras`. O tecto só muda no ficheiro de chaves |
| Exit 3, «contador de pedidos inutilizavel» | `contador.json` não se lê | **Não o apagar às cegas**: abrir, ver o que tem, repor a contagem de hoje à mão |
| Exit 3, «o contador esta em uso por outro processo» | Há outro ensaio a correr, ou um que morreu deixou `contador.json.trava` | Se não houver outro a correr: `aos-ensaio limpar --chaves <ficheiro>` remove a trava do processo morto |
| Exit 3, «o contador nao existe e ha relatorios de corridas reais de hoje» | O contador foi apagado; a contagem do dia perdeu-se | Repetir o comando com `--reconstruir-contador`: recria-o com a soma dos pedidos dos relatórios de hoje |
| Exit 3, «o campo KIMI_API_BASE esta … com um host que nao e do fornecedor» | A base no ficheiro não é `https` de um host do fornecedor | Corrigir o ficheiro. `--destino-fora-da-lista <host>` só para um destino que o dono conhece e quer |
| Exit 4, `chave_recusada` | A sonda, ou os três primeiros pedidos, levaram 401 ou 403 (por exemplo, a chave é de outro produto do mesmo fornecedor) | A chave não é aceite nessa base: confirmar a chave e `KIMI_API_BASE` no ficheiro (sem os mostrar a ninguém) |
| Exit 4, «a SONDA nao teve 200 … a corrida NAO COMECOU» | O pedido de sonda não teve 200. A causa vem na mesma linha e no campo `sonda` do relatório | Seguir a linha da causa, abaixo. Gastou-se um pedido do tecto |
| Exit 4, `saldo_insuficiente` | A conta do fornecedor não tem saldo ou quota: foi o que a sonda recebeu, ou os três primeiros pedidos levaram 429 com esse tipo | Carregar a conta no fornecedor. **Repetir a corrida não adianta** e gasta o tecto. `--pausa` não resolve |
| Exit 4, `limite_de_ritmo` | Pedidos a mais por unidade de tempo: a sonda, ou os três primeiros pedidos, levaram 429 com esse tipo | Repetir com `--pausa 5s` (ou mais) |
| Exit 4, `so_respostas_429` | Os três primeiros pedidos levaram 429 com um tipo que o banco não conhece | Ver «tipos de erro» no relatório. Se for ritmo, `--pausa`; se for saldo, carregar a conta |
| Exit 4, `serie_de_429` | Dez respostas 429 seguidas a meio da corrida (o saldo acabou, ou o ritmo apertou) | Ver «tipos de erro» no relatório: com `saldo_insuficiente`, carregar a conta; com `limite_de_ritmo`, repetir com `--pausa` |
| Exit 4, `modelo_desconhecido` | A sonda: o fornecedor não conhece o modelo | Corrigir o nome do modelo no ficheiro de chaves, ou `--modelo` |
| Exit 4, `rota_indisponivel` | A sonda: o fornecedor conhece o modelo mas não tem endpoint que sirva o pedido com as definições da conta (na OpenRouter: política de dados, fornecedores permitidos, suporte de tools) | Corrigir as definições na conta do fornecedor. Repetir não adianta |
| Exit 4, `sonda_falhou` | A sonda não teve 200, por uma causa fora do vocabulário (`outro`) | Ver o código HTTP na linha da sonda; repetir com `--so-plano` para confirmar o destino |
| Exit 4, `tecto_atingido` | O tecto foi atingido a meio (o de dólares só se conhece depois de cada resposta) | O relatório parcial está escrito. Nada a repor |
| Exit 3, «o perfil devolve estado e o seu expected_model nao e a rota desta corrida» | O `expected_model` do perfil não é `anthropic/<ANTHROPIC_MODELO>` (ou `openai/<modelo>` no Kimi, ou `openrouter/<OPENROUTER_MODELO>` na OpenRouter) | Corrigir o perfil. Nenhum pedido saiu |
| Desfecho `estado_nao_devolvido` no relatório | A rota exige o estado e o de um turno não se podia devolver: o pedido seguinte não foi enviado | Ver `nao_devolvido_por_causa`: `estado_de_rota_nao_provada` é a rota (modelo ou endpoint declarados pelo proxy); `estado_ausente` é o fornecedor não ter mandado estado nesse turno |
| Exit 6 | Sem Docker, sem a imagem, ou o proxy não arrancou | Arrancar o Docker; `docker pull` da imagem acima. A mensagem traz as últimas linhas do proxy, com os segredos ocultados |
| Contentores `aos512-*` a correr depois de uma corrida interrompida à força | A limpeza não chegou a correr. **Um proxy órfão guarda a chave do fornecedor em claro no ambiente do contentor** (`docker inspect`) e aceita pedidos fora do contador | `aos-ensaio limpar` (remove contentores e redes `aos512-*` e diz quantos). O proxy também se mata sozinho ao fim de cerca de 90 s sem sinal de vida, e a corrida seguinte varre o que restar. À mão: `docker ps -a --filter name=aos512- --format "{{.Names}}"` e `docker rm -f` de cada um |
| Códigos 429 no relatório | Limite de ritmo **ou** conta sem saldo: o código é o mesmo (o proxy do ensaio tem o arrefecimento desligado) | Ver «tipos de erro» no relatório. `limite_de_ritmo`: repetir com `--pausa 2s`. `saldo_insuficiente`: carregar a conta |

## O que este procedimento não faz

- Não publica uma versão de projecção: os braços B e C existem só no banco.
- Não decide se um modelo entra: devolve taxas e, na experiência `qualificacao`, um veredicto
  calculado. Quem decide a entrada é o dono, ao assinar o registo que o cita (AOS-519).
- Não mede produção: outro prompt de sistema, outras tools, outros documentos.
