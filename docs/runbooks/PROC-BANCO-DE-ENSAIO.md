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
| Exit 6 | Sem Docker, sem a imagem, ou o proxy não arrancou | Arrancar o Docker; `docker pull` da imagem acima. A mensagem traz as últimas linhas do proxy, com os segredos ocultados |
| Contentores `aos512-*` a correr depois de uma corrida interrompida à força | A limpeza não chegou a correr. **Um proxy órfão guarda a chave do fornecedor em claro no ambiente do contentor** (`docker inspect`) e aceita pedidos fora do contador | `aos-ensaio limpar` (remove contentores e redes `aos512-*` e diz quantos). O proxy também se mata sozinho ao fim de cerca de 90 s sem sinal de vida, e a corrida seguinte varre o que restar. À mão: `docker ps -a --filter name=aos512- --format "{{.Names}}"` e `docker rm -f` de cada um |
| Códigos 429 no relatório | Limite de ritmo **ou** conta sem saldo: o código é o mesmo (o proxy do ensaio tem o arrefecimento desligado) | Ver «tipos de erro» no relatório. `limite_de_ritmo`: repetir com `--pausa 2s`. `saldo_insuficiente`: carregar a conta |

## O que este procedimento não faz

- Não publica uma versão de projecção: os braços B e C existem só no banco.
- Não decide se um modelo entra: devolve taxas.
- Não mede produção: outro prompt de sistema, outras tools, outros documentos.
