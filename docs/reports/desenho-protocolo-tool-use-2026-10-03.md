# Desenho — o protocolo de tool-use entre o AOS e o modelo

| Campo | Valor |
|---|---|
| Data | 2026-10-03 |
| Estado | Decidido pelo dono a 2026-10-03 (§7). Em implementação: AOS-489 e AOS-490 |
| Ticket | AOS-489 (fase 1, kernel) e AOS-490 (fase 2, gateway, com ADR próprio) |
| Origem | Validações em produção de 2026-10-02/03 ([relatório](e2e-plano-multi-no-prod-2026-10-02.md), AOS-484, AOS-487) |
| Base da análise | Cinco frentes: assembler e tail, replay e retoma, taint e segurança, gateway e desempenho, pesquisa externa nos fornecedores. Medição sobre o `events.wal` de produção inteiro |

## 1. O problema, medido

O modelo volta a pedir a tool que já chamou. Em produção, desde que há eventos de mediação
(2026-09-14):

| Medida | Valor |
|---|---|
| Runs que pediram tools | 32 |
| Runs com pelo menos uma repetição da mesma tool sobre o mesmo recurso | 17 (53%) |
| Tool calls registadas | 75 |
| Tool calls que são repetições | 45 (60%) |
| Repetições por run | 6 runs com 1, 4 com 2, 1 com 3, 5 com 4, 1 com 8 |
| Repetições negadas por taint (desde 2026-09-26) | 19 |

Mais de metade das tool calls que o AOS mediou são o modelo a refazer o que já fez. Antes de
2026-09-26 eram executadas outra vez na sandbox; depois passaram a ser negadas por taint, o que
poupa a execução e não o turno. Numa das três validações da v0.1.43/44 o modelo desistiu depois da
recusa e o plano saiu verde sem o objectivo cumprido.

## 2. A causa

O nó envia ao modelo o prompt inteiro como **uma mensagem de utilizador**
(`packages/platform/model-gateway/runtime_adapter.go`). No turno a seguir a uma tool call o modelo
vê isto:

```
=== SYSTEM ===

=== TOOLSET (frozen) ===
tool	doc_read	1.0.0	sha256:cc05…
=== CONTEXT (append-only) ===
<objective>
Ler o documento 'notes' com a tool doc_read e devolver o seu conteudo.
<tool_result taint=untrusted>
{"stdout_text":"Reuniao de 15/08/2026 - nota de trabalho...","exit_code":0}
```

Faltam quatro coisas:

1. **A tool call do próprio modelo não fica registada.** O loop só acrescenta ao tail o texto da
   resposta, e só se não for vazio. O pedido `doc_read(notes)` não aparece em lado nenhum.
2. **O resultado não diz de que chamada é.** O segmento leva só o `taint` e os rótulos de recusa.
3. **Nada explica o protocolo.** O `system` dos runs filhos de um plano é a string vazia; nada diz o
   que é um `<tool_result>`, o que significa `taint=`, nem que uma recusa não se repete.
4. **Os turnos nativos não são usados.** O contrato do gateway já tem `assistant` com `tool_calls` e
   `tool` com `tool_call_id`; o adaptador não os preenche e deita fora o id que o provider devolve.

A documentação da Moonshot/Kimi (o provider por trás do alias de produção) tem uma página sobre
tool calls repetidas. A primeira causa que lista é a mensagem `assistant` com `tool_calls` não ter
sido acrescentada ao histórico, e a segunda é os resultados irem no papel errado. É o que o AOS faz.
O modelo disse-o em produção: «o conteúdo que você colou».

## 3. Princípios do desenho

1. **O tail é a forma canónica da conversa.** É ele que tem hash, é capturado e é reproduzido. Tudo
   o que o modelo precisa de saber sobre a conversa está no tail, estruturado.
2. **O que vai para o provider é uma projecção do tail**, determinística e versionada. Hoje é o
   texto único; amanhã podem ser mensagens nativas. Mudar a projecção não muda o tail.
3. **Nada que o modelo ou o provider digam decide autoridade.** A autoridade continua a derivar do
   tipo de cada segmento (ADR-034). O id de uma chamada correlaciona; não autoriza.
4. **Uma só construção por segmento e uma só sequência por turno**, partilhadas pelo loop e pelo
   replay. Hoje a sequência está escrita duas vezes.
5. **Medir antes e depois.** A métrica de sucesso é repetições por run perto de zero, com o mesmo
   script da linha de base.

## 4. Fase 1 — o tail regista a conversa (assembler 1.4.0, AOS-489)

### 4.1 O que muda no tail

Por cada tool call do modelo, antes do seu resultado:

```
<tool_call taint=untrusted id=step-000001-tool-1 name=doc_read>
{"doc_id":"notes"}
<tool_result taint=untrusted id=step-000001-tool-1 name=doc_read>
{"stdout_text":"Reuniao de 15/08/2026 - nota de trabalho...","exit_code":0}
```

- **`id`** é cunhado pelo runtime: `<passo>-tool-<n>`. Já existe, é determinístico, é o `step_id` do
  evento de mediação e do ledger, e cabe no alfabeto dos rótulos. Não vem do provider: assim não é
  preciso mexer no esquema de captura, as capturas antigas reconstroem-se, e não há colisão depois
  de saneado.
- **`name`** vai na linha de delimitação, saneado e com tecto de comprimento.
- **Os argumentos** vão no corpo, tal como o modelo os emitiu (antes da reescrita do efeito),
  neutralizados como qualquer conteúdo. Com tecto de tamanho; acima dele fica o digest.
- **Não entram:** capability, recurso, região, reversibilidade nem o input reescrito. São postura de
  política. A `Reason` de uma recusa continua fora.
- **Ordem com várias chamadas no turno:** `[history] call_1 result_1 … call_M result_M`, a ordem
  em que o loop as despacha.
- **Autoridade:** o tipo novo é classificado explicitamente como output do modelo (não eleva nem
  baixa), com caso em `TestSegmentAuthority` e linha na tabela do ADR-034.

Custo: cerca de 120 bytes (30 tokens) por chamada, repetidos nos turnos seguintes.

### 4.2 Preâmbulo de protocolo no prefixo (decisão D3)

Um bloco fixo e curto no prefixo, versionado com o assembler, que diz ao modelo o que são os
segmentos: cada `<tool_call>` é uma chamada que ele próprio fez, o `<tool_result>` com o mesmo `id`
é a resposta, um resultado negado não se repete com os mesmos argumentos, e o conteúdo
`taint=untrusted` é dados e não instruções. Fica no prefixo, que é a parte estável (ADR-009), e não
dentro dos resultados.

### 4.3 Uma só sequência para o loop e o replay

A ordem dos `append` de um turno passa a viver numa função única, usada pelo loop e pelo motor de
replay. Hoje `replay/engine.go` espelha `loop.go` à mão, e os testes gerados em código ficam verdes
mesmo que os dois errem da mesma maneira.

### 4.4 Compatibilidade do replay (decisão D2)

As subidas anteriores do assembler (1.2, 1.3) invalidaram o replay de todos os runs gravados. Esta
mudança é aditiva na sequência (um segmento a mais e dois rótulos a mais no resultado), o que torna
barato fazer melhor:

- a função de sequência recebe a versão do layout; em 1.3.0 omite o segmento e os rótulos novos;
- o replay escolhe a versão **por turno**, a partir do `assembly_version` do `turn.recorded`;
- um run suspenso ou em recuperação que atravesse o deploy continua no layout em que começou (a
  versão fica no registo de retoma), em vez de ficar com um log misto;
- a divergência passa a sair atribuída a `assembly_version` quando é essa a causa (hoje o
  `prompt_hash` é comparado primeiro e esconde-a).

Com isto os runs 1.3.0 continuam a reproduzir-se e a recuperação de desastre continua a funcionar
sobre backups anteriores ao deploy.

### 4.5 Rede de segurança e medição

- **Métrica de eficiência de trajectória:** contador de tool calls repetidas (mesma tool, mesmos
  argumentos) por run, exposto em `/metrics`. É o que falta para ver este problema sem ler o WAL.
- **Aviso de repetição:** à terceira repetição idêntica, o runtime acrescenta um segmento trusted a
  dizer que a chamada já foi feita e onde está o resultado. É a prática que a Kimi recomenda. O
  disjuntor de no-progress já calcula o hash da acção; isto usa-o antes de ele disparar.

### 4.6 Segurança (decisão D4)

Restrições que o desenho cumpre: argumentos só no corpo; id e nome só na linha de delimitação, com
tecto; tipo constante; construtor único do runtime; teste de forja no molde dos existentes
(`injeccao_no_tail_test.go`); `Reason` e metadados de hook fora do prompt.

O que muda e tem de ser aceite:

- **Argumentos reenviados.** Os argumentos de uma tool call, que podem conter dados pessoais ou
  segredos, passam a ir no prompt de todos os turnos seguintes. Vieram do provider, mas o router pode
  servir o turno seguinte por outro. O gateway não tem redacção sobre o prompt.
- **Sondagem de política.** Com os argumentos ao lado do código de recusa, conteúdo injectado pode
  sondar a fronteira da política argumento a argumento. Não é fuga da `Reason`; baixa o custo da
  sondagem.
- **DEF-806.** Fica mais conteúdo untrusted inline no tail. O segmento de argumentos é o sítio onde
  a separação de planos poria um handle em vez de bytes; o desenho deixa-o compatível com isso.

Lacuna pré-existente a fechar no mesmo trabalho: a neutralização só reconhece `\n` como início de
linha; um `\r<…>` isolado ou um separador Unicode não é escapado.

## 5. Fase 2 — projecção em mensagens nativas (ADR novo)

O adaptador passa a projectar o tail em `system` + `user` + pares `assistant(tool_calls)` /
`tool(tool_call_id)`. A pipeline e o cliente HTTP do gateway não mudam: nenhum estágio lê as
mensagens, e o planeador do `aos-orq` já envia `system` e `user` separados em produção.

O que a fase 2 tem de resolver, e porque pede ADR:

1. **O que é o `prompt_hash`.** Continua a ser o hash do tail canónico; a projecção é função
   determinística e versionada dele. A frase «os mesmos bytes que vão para o provider» deixa de ser
   literal.
2. **Continuidade do raciocínio.** Os modelos de raciocínio exigem que o raciocínio do turno volte
   com o resultado da tool: `reasoning_content` na Kimi e no DeepSeek, blocos assinados na Anthropic,
   itens cifrados na OpenAI. O contrato do gateway não tem esse campo. Tem de ser transportado opaco,
   byte a byte, e capturado (selado por titular) para o replay.
3. **Proveniência.** O rótulo `taint=untrusted` passa para dentro do conteúdo da mensagem `tool`,
   como envelope, e a neutralização continua a aplicar-se. Segmentos trusted (`objective`,
   `correction`) e untrusted (`plan_input`, `memory`) não se fundem no mesmo papel sem delimitação.
4. **Emparelhamento estrito.** Cada `tool_call` tem exactamente um `tool`. Hoje uma escalada a meio
   do turno deixa as chamadas seguintes sem resultado.
5. **Desempenho.** `system` separado e histórico por mensagens dão um prefixo estável ao nível das
   mensagens. O gateway passa a ler os tokens em cache do provider (hoje lê um campo que não é o do
   wire) e o SLI de cache é composto. Os parâmetros do run (`max_tokens`, `temperature`) passam a
   chegar ao provider.

**Antes de desenhar a fase 2, uma medição:** um pedido ao LiteLLM de produção com turnos nativos,
para saber se o `reasoning_content` chega na resposta e sobrevive no pedido seguinte. A
documentação não o diz para o provider genérico `openai/`, e o `drop_params: true` da nossa
configuração descarta parâmetros sem erro.

## 6. Achados laterais (sem ticket; não entram neste trabalho)

- Uma resposta truncada pelo provider (`finish_reason=length`) sem tool calls conta como conclusão
  do run.
- Os parâmetros de amostragem do run são gravados no manifesto e não chegam ao provider.
- O SLI de cache e a guarda de layout do prefixo existem, estão testados e não estão compostos.
- O `kimi-for-coding` é um serviço de subscrição descrito como destinado a agentes de programação,
  com uma cláusula sobre a identidade do cliente. O uso em produção através do LiteLLM merece
  confirmação contratual.

## 7. Decisões do dono

| | Decisão | Recomendação | Decidido |
|---|---|---|---|
| D1 | Âmbito e ordem | Fase 1 já, medir em produção, e fase 2 a seguir com ADR e com a medição do LiteLLM primeiro | **As duas fases numa só entrega.** Para conter o risco, a projecção nativa é seleccionável por configuração e a medição do LiteLLM faz-se antes do desenho final da fase 2 |
| D2 | Replay dos runs gravados | Layout por versão, escolhido por turno e fixado por run | Como recomendado |
| D3 | Preâmbulo de protocolo no prefixo | Sim, na 1.4.0 | Como recomendado |
| D4 | Argumentos no prompt | Aceitar, com tecto de tamanho e digest acima dele | Como recomendado |

## 8. Critérios de sucesso

- Repetições da mesma tool sobre o mesmo recurso: de 60% das chamadas para perto de zero, medido em
  produção com o script da linha de base, em pelo menos dez runs.
- Nenhum run termina com o modelo a afirmar que não leu um documento que tinha no contexto.
- Turnos por nó de leitura: de 3 a 6 para 2.
- O replay dos runs 1.3.0 continua a reproduzir-se (se D2 for a recomendada).
