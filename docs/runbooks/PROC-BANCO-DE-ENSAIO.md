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
   212 pedidos; hoje (<dia> UTC) restam N de 1000` e `--so-plano: nenhum pedido foi enviado`.
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
| Exit 3, «contador de pedidos inutilizavel» | `contador.json` não se lê | **Não o apagar às cegas**: abrir, ver o que tem, repor a contagem de hoje à mão. Apagá-lo zera a contagem do dia |
| Exit 4, `tecto_atingido` | O tecto foi atingido a meio (o de dólares só se conhece depois de cada resposta) | O relatório parcial está escrito. Nada a repor |
| Exit 6 | Sem Docker, sem a imagem, ou o proxy não arrancou | Arrancar o Docker; `docker pull` da imagem acima. A mensagem traz as últimas linhas do proxy, com os segredos ocultados |
| Contentores `aos512-*` a correr depois de uma corrida interrompida à força | A limpeza não chegou a correr | `docker ps -a --filter name=aos512- --format "{{.Names}}"` e `docker rm -f` de cada um; `docker network ls --filter name=aos512-` e `docker network rm` |
| Muitos 401 no relatório | A chave não é aceite pelo fornecedor nessa base | Confirmar `KIMI_API_BASE` e a chave no ficheiro (sem os mostrar a ninguém) |

## O que este procedimento não faz

- Não publica uma versão de projecção: os braços B e C existem só no banco.
- Não decide se um modelo entra: devolve taxas.
- Não mede produção: outro prompt de sistema, outras tools, outros documentos.
