# ADR-031 — A leitura do estado de um pedido de plano vive no nó, e é por titularidade

- **Estado:** Aceite
- **Data:** 2026-09-24
- **Ticket:** AOS-430
- **Relacionados:** ADR-016 (fronteira de confiança da UI; §5 read-path soberano), ADR-018 (o nó é
  a única autoridade de ciclo de vida), ADR-027 (nós do plano como `<run>~<nó>`),
  ADR-028 (ingresso do caminho do plano), ADR-030 (reclamação da fila; §2.1 não-oracularidade)

## 1. Contexto

Quem submete um objectivo por `POST /plans` recebe `201 accepted` e **não tem por onde saber o
que lhe aconteceu**.

A causa é estrutural e foi confirmada em execução, não inferida: o `aos-orq` só submete ao nó os
runs **filhos** — `childRunID(runID, nodeID)` = `<topo>~<nó>` — e o run de **topo** nunca é
hospedado. Os dois processos têm Event Stores em volumes separados por desenho
(`aos-data` e `aos-orq-data`, declarados distintos no compose de produção), pelo que os eventos
do run de topo existem, mas no substrato do orquestrador.

`GET /runs/<topo>`, `GET /runs/<topo>/trajectory` e `GET /runs/<topo>/reconstruct` dão **404**.
O teste `TestAOS430ORunDeTopoNaoEServivelPeloReadPath` mede-o: o AOS-430 exigia esta verificação
em execução porque a conclusão anterior vinha de leitura de código.

## 2. Decisão

### 2.1 O nó serve o estado; o orquestrador continua a reportá-lo pela rota que já usa

O `aos-orq` **já** reporta o desfecho por `POST /plans/outcome` desde o AOS-423, com `classe`,
`codigo_saida` e `detalhe`. Essa metade estava feita e ninguém a lia.

Acrescenta-se a metade em falta: **`GET /plans/{id}`**, servida pelo nó a partir do mesmo stream
`aos-internal/plan-requests` onde o desfecho já é gravado.

### 2.2 A fronteira é a TITULARIDADE, e é ela que mantém o ADR-030 §2.1 de pé

A objecção óbvia é que uma rota de leitura de plano é o oráculo de existência que a reclamação
existe para não ser. A regra do ADR-030 §2.1, lida literalmente, é outra:

> «Uma superfície HTTP do nó não revela a EXISTÊNCIA de um recurso **a quem não pode agir sobre
> ele**.»

Quem submeteu **pode** agir sobre o seu pedido: foi ele que o criou e foi ele que escolheu o
`run_id`. Servir-lhe o estado não lhe revela nada que ele já não soubesse.

Logo a rota compara o `principal` do chamador com o `Principal` gravado no facto, e **as três
recusas dão a mesma resposta**: não existe, não é teu, é de outra região — todas `404`, corpo
igual. Há teste que as compara byte-a-byte.

**Este ADR não emenda o ADR-030. Aplica-o.**

Nota de implementação com consequência: o `planRequestPayload.Principal` era gravado desde o
AOS-417 e **nunca lido por código nenhum**. Esta rota é o seu primeiro leitor. Um campo gravado
que ninguém lê é uma afirmação por verificar.

### 2.3 O que a rota devolve, e o que deliberadamente não devolve

Devolve `{run_id, status, generation, exit_code, detail}`.

**Não devolve o objectivo.** Está selado sob a KEK do titular desde o AOS-429; devolvê-lo
obrigaria a repetir o caminho de decifragem e o tratamento do Art. 17 para não acrescentar
informação nenhuma — quem submeteu o objectivo já o tem.

**Não devolve os runs filhos nem a trajectória.** Vivem no Event Store do `aos-orq`. O nó não os
tem e não os pode inventar. Ver §4.

### 2.4 A leitura é do PRINCÍPIO do stream, contra o caminho quente

A projecção da fila lê a partir da **marca de água** do AOS-429 — o `seq` acima do qual todos os
pedidos estão terminados. Para contar pendentes é correcto e é o ponto.

Para esta rota seria o contrário do que se quer: um pedido terminado está, por definição, abaixo
da marca, e é precisamente o desfecho dele que quem submeteu vem procurar. Reutilizar o caminho
quente devolveria «não existe» para todos os planos que acabaram — uma resposta errada e
indistinguível da certa.

Custo aceite e declarado: linear no histórico da fila, por chamada. A rota é autenticada (não há
sondagem anónima), serve uma pergunta ocasional e não está em caminho quente nenhum.

## 3. Alternativas rejeitadas

**(a) O nó expõe o estado do plano lendo-o de onde?** Era a alternativa (a) do ticket, e não tem
resposta: o nó não tem os eventos do run de topo. Só serviria o que já está na fila — que é
exactamente o que esta decisão faz, sem a pretensão de servir mais.

**(b) O `aos-orq` ganha superfície de rede.** Rejeitada, e já estava: o AOS-417 evitou-a de
propósito e o ADR-028 escreveu porquê — «uma segunda superfície com postura diferente da do nó é
exactamente o modo de falha que o ADR-016 vem fechar». Custo de a aceitar: um segundo gate
soberano, um segundo read-path, e duas posturas para manter alinhadas sem gate que as ligue.

**(c) Substrato partilhado (JetStream para os dois).** Tornaria o run de topo legível pelo
read-path existente, sem rota nova. Rejeitada **para já**, não em princípio: é uma migração de
produção e uma decisão de infraestrutura, o nó corre hoje sobre ficheiro, e o AOS-431 acabou de
medir que o substrato replicado tem um defeito de diagnosticabilidade por fechar (AOS-432). É o
destino que o ADR-030 §3 nomeia, e quando lá se chegar esta rota continua correcta — passa a ser
a forma barata de fazer a mesma pergunta.

**(d) Devolver o estado no corpo do `POST /plans/outcome`.** Não serve: quem reporta o desfecho é
o consumidor, não quem submeteu. São pessoas diferentes a fazer perguntas diferentes.

## 4. Consequências

**O que fica melhor.** Quem submete um plano passa a poder segui-lo. A UI (EPIC-13) ganha a
superfície de que precisava e que estava a bloqueá-la.

**O que fica por fechar, e é honesto dizê-lo:**

1. **O estado é grosso.** Quatro valores (`pending`, `in_progress`, `terminal`,
   `aguarda_humano`) mais o código de saída. Não há progresso por nó do plano, porque esses
   factos vivem no outro processo.
2. **Nada drena a fila em produção.** O `aos-orq` é `profiles: ["orq"]`, `restart: "no"`, e o
   `consume` «drena uma vez e termina». Enquanto não houver quem o dispare, esta rota reporta
   `pending` para sempre — o que é **verdade**, e é informação, mas não é o que um utilizador
   espera. É trabalho de implantação, com âmbito próprio.
3. **A leitura não sela WORM.** As rotas de leitura de run selam (`sealSensitiveRead`); esta não.
   O que ela revela é estado de processo, não conteúdo de run, e o objectivo fica de fora — mas
   a assimetria com as rotas irmãs está declarada e não resolvida.
4. **O custo é linear no histórico.** Ver §2.4. A saída, se um dia doer, é persistir a marca de
   água — resíduo declarado do AOS-429.
