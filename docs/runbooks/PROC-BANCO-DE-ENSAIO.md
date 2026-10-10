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

**Ler antes de correr.** Medido atrás da imagem fixada do proxy, com um provider falso
(2026-10-10): o estado do turno (`reasoning_details`) volta ao fornecedor dentro de
`provider_specific_fields`, e não no topo da mensagem, que é onde a OpenRouter o lê. Enquanto o
gateway não o repuser no topo (está descrito no Estado do AOS-516), **esta corrida não pode dar
`cumprida`**: o melhor veredicto possível é `inconclusiva`, com a razão
`estado_devolvido_so_no_saco_do_proxy`. O que a corrida mede hoje: se a sonda passa (chave,
créditos, nome do modelo), se o raciocínio liga (`turnos_com_raciocinio_capturado` maior do que
zero) e o que a OpenRouter responde a um segundo turno sem `reasoning_details`.

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

**Como ler.** `saldo_insuficiente` na sonda: a conta da OpenRouter não tem créditos (402).
`sem_raciocinio`: o `reasoning_effort` do perfil chegou à OpenRouter (medido) mas não ligou o
raciocínio — a forma que ela documenta é `reasoning: {…}`, que o perfil de uma rota ainda não
sabe exprimir. `nao_cumprida` com 4xx em pedidos com estado: a OpenRouter recusou o segundo
turno sem `reasoning_details`. `inconclusiva` com `estado_devolvido_so_no_saco_do_proxy`: ela
aceitou-o, e o banco não sabe se leu o estado.

**O que esta corrida não prova:** a rota `anthropic/` directa, nem o bloco de texto que o proxy
acrescenta nessa rota; e o adaptador `openrouter` do proxy identifica-se à OpenRouter com os
cabeçalhos `HTTP-Referer` e `X-Title` do LiteLLM, que o banco não escolhe.

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
| Exit 4, `sonda_falhou` | A sonda não teve 200, por uma causa fora do vocabulário (`outro`) | Ver o código HTTP na linha da sonda; repetir com `--so-plano` para confirmar o destino |
| Exit 4, `tecto_atingido` | O tecto foi atingido a meio (o de dólares só se conhece depois de cada resposta) | O relatório parcial está escrito. Nada a repor |
| Exit 3, «o perfil devolve estado e o seu expected_model nao e a rota desta corrida» | O `expected_model` do perfil não é `anthropic/<ANTHROPIC_MODELO>` (ou `openai/<modelo>` no Kimi, ou `openrouter/<OPENROUTER_MODELO>` na OpenRouter) | Corrigir o perfil. Nenhum pedido saiu |
| Desfecho `estado_nao_devolvido` no relatório | A rota exige o estado e o de um turno não se podia devolver: o pedido seguinte não foi enviado | Ver `nao_devolvido_por_causa`: `estado_de_rota_nao_provada` é a rota (modelo ou endpoint declarados pelo proxy); `estado_ausente` é o fornecedor não ter mandado estado nesse turno |
| Exit 6 | Sem Docker, sem a imagem, ou o proxy não arrancou | Arrancar o Docker; `docker pull` da imagem acima. A mensagem traz as últimas linhas do proxy, com os segredos ocultados |
| Contentores `aos512-*` a correr depois de uma corrida interrompida à força | A limpeza não chegou a correr. **Um proxy órfão guarda a chave do fornecedor em claro no ambiente do contentor** (`docker inspect`) e aceita pedidos fora do contador | `aos-ensaio limpar` (remove contentores e redes `aos512-*` e diz quantos). O proxy também se mata sozinho ao fim de cerca de 90 s sem sinal de vida, e a corrida seguinte varre o que restar. À mão: `docker ps -a --filter name=aos512- --format "{{.Names}}"` e `docker rm -f` de cada um |
| Códigos 429 no relatório | Limite de ritmo **ou** conta sem saldo: o código é o mesmo (o proxy do ensaio tem o arrefecimento desligado) | Ver «tipos de erro» no relatório. `limite_de_ritmo`: repetir com `--pausa 2s`. `saldo_insuficiente`: carregar a conta |

## O que este procedimento não faz

- Não publica uma versão de projecção: os braços B e C existem só no banco.
- Não decide se um modelo entra: devolve taxas.
- Não mede produção: outro prompt de sistema, outras tools, outros documentos.
