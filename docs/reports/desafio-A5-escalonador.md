# Desafio ao plano A5 — Escalonador (SCH, AOS-012, EPIC-03)

> **O que é:** avaliação adversarial do subtópico `#### A5. Escalonador` de
> [`prontidao-modelos-agenticos.md`](./prontidao-modelos-agenticos.md). Quinto da série,
> depois de [A1](./desafio-A1-budget-admission-control.md),
> [A2](./desafio-A2-progress-surface.md), [A3](./desafio-A3-credential-broker.md) e
> [A4](./desafio-A4-orquestrador.md).
>
> **Data:** 2026-08-08 · **HEAD avaliado:** `44a31e2` (branch `feature/AOS-128-ux-dx-tests`)

## Aviso de método: metade do painel não passou pelo céptico

Este fluxo foi interrompido duas vezes por erros de API e por limite de sessão. As **6
lentes de descoberta concluíram**, mas **2 dos 6 refutadores caíram** (`sobrecarga` e
`emenda`) e a síntese automática nunca correu. Sintetizei o relatório à mão a partir do
journal.

Consequência que registo em vez de esconder: **os quatro achados de severidade mais alta
vêm da lente `sobrecarga`, cujo refutador falhou — não foram testados por um adversário.**
Por isso verifiquei-os pessoalmente contra o código, com rigor reforçado, antes de os
incluir. Cada um traz a marca de como foi validado.

## Sumário executivo

O A5, como o A4, avalia um subsistema **deliberadamente excluído por teste de fronteira** —
e o deferimento é honesto pela mesma razão. Mas o relatório, ao gastar a secção de RISCO no
que o escalonador faria, **não diz o que existe hoje no caminho vivo do nó** — e o que
existe tem um defeito grave.

1. **[ALTA — verificado à mão] O nó tem um fusível de 120 chamadas ao modelo por vida do
   processo.** O keypool do gateway nunca reinicia o contador RPM: não há janela, apesar de
   o comentário a chamar "janela corrente". À 121.ª chamada, tudo falha fechado até
   reiniciar.
2. **[ALTA — verificado à mão] `max_turns` é escolhido pelo submissor sem clamp.** Um único
   `POST /runs {max_turns: 200}` esgota esse fusível e desliga o nó para todos os runs.
3. **"Sem backpressure" é factualmente errado.** O ingresso tem token-bucket + tecto de
   runs em voo (429). O que falta é o operador poder afiná-los — as três opções existem, sem
   env.
4. **A contradição do tieradapter com o A4 resolve-se: não há contradição.** Ambas as
   leituras são verdadeiras porque o `tieradapter` é código morto — importa o módulo
   proibido mas não é importado por ninguém, em nenhum dos cinco binários do repo.
5. **A emenda proposta ao guarda ("sem `Scheduler.Start`") não é exequível** sem quebrar o
   zero-dep, e nomearia o símbolo errado. Decisão do dono.

---

## Veredicto sobre o A5 tal como está escrito

| Alegação | Veredicto | Evidência (HEAD 44a31e2) |
|---|---|---|
| **PROPÓSITO** — governação de carga completa (AOS-027…107) | **exacto** | Todas as peças existem no módulo plano `control-plane/scheduler` (`admission.go`, `queue.go`, `breaker.go`, `priority.go`, `routing.go`, `scale.go`). |
| **PROVADO** — os testes nomeados | **exacto, com uma imprecisão** | Existem. Mas «routing **≥90%** contra round-robin» é falso: o teste assere `wins*4 < n*3` = **≥75%** (`routing_test.go:452`), e a execução real deu **81,8%** (27/33). O «≥90%» vem do `README.md:465` do módulo — número que nem o teste impõe nem a execução produz. |
| **Nuance do tieradapter** — «importa tipos do SCH em código produtivo» | **verdade literal, engana na substância** | `tieradapter.go:36` importa mesmo `control-plane/scheduler` em ficheiro não-teste. Mas zero importadores não-teste em todo o repo, e ausente do fecho dos **cinco** binários. Ver secção própria. |
| **Distinção dos dois breakers** | **exacto** | `breaker_thresholds.go` (AOS-080/081, agente vivo) vs `scheduler/breaker.go` (AOS-029, orçamento por árvore) são disjuntos. Adenda: o do agente vivo está ele próprio inerte no run comum (achado do A4). |
| **RISCO** — «sem TPM/RPM agregado» | **parcial** | Verdadeiro para *agregado cross-process*. Mas existe um limite RPM **por-processo** no caminho vivo — e tem o defeito F1. |
| **RISCO** — «sem backpressure» | **errado** | `api.go:534-546`: `POST /runs` passa por token-bucket + tecto de in-flight, 429 ao exceder. |
| **RISCO** — «sem degradação graciosa, sem prioridades» | **exacto** | `degradation.go`/`priority.go` fora do fecho do nó. |
| **Ressalva «só in-process com o Store de referência»** | **exacto** | Compressão fiel de `scheduler/README.md:146-155`; `EventLog` é uma porta e a `Admission` é stateless. |
| **Fecho 1** — «admission/budget no composition-root (médio, exige emendar o boundary test)» | **sub-especificado / possivelmente errado** | A emenda é a parte cara e talvez inexequível (secção própria). O A2 concluiu, para as **mesmas** portas, que adaptadores node-local resolvem **sem** emenda. |
| **Fecho 2** — «breaker por árvore (médio)» | **parcial** | Vive no módulo proibido e exige um `TreeBudgetReader` não-nil satisfeito por `*budget.Budget` — **depende do fecho do A1**. Detalhe abaixo. |
| **Fecho 3** — «filas/prioridade/routing/escala (grande, EPIC-10)» | **parte errada** | Filas (AOS-030) e prioridade (AOS-032) são **single-node** e não dependem de multi-nó; só routing least-loaded e escala por SLIs dependem. |

---

## ⚠️ Os dois achados que verifiquei à mão (refutador caiu)

### F1 — O nó tem um fusível de 120 chamadas ao modelo por vida do processo

`modelgatewaywiring.go:135-137` compõe o gateway com **uma** conta:
`{KeyID: "model-upstream", …, LimitRPM: 120, LimitTPM: 200_000}`.

O keypool nunca reinicia o contador. Verifiquei que **em todo o pacote** não há `Ticker`,
nem `time.`, nem nenhum `rpm = 0`:

- `keypool.go:171` — `best.rpm++` a cada `Select`;
- `keypool.go:72-73` — `saturated()` devolve true quando `rpm >= LimitRPM`;
- `keypool.go:150-172` — se todas as contas saturam, `Select` devolve `ErrNoCapacity`;
- `gateway.go:520-523` — `g.credential()` propaga esse erro **fail-closed** em cada
  Chat/ChatStream/Embeddings;
- o comentário `keypool.go:67` diz «janela corrente» — **a janela não existe**.

**À 121.ª chamada ao modelo desde o arranque, toda a chamada ao modelo passa a falhar, para
sempre, até reiniciar o processo.** Com `DefaultMaxTurns = 16` são ~8 runs completos. Não é
degradação graciosa: é um brownout permanente e silencioso (nenhuma métrica de saturação é
exposta), indistinguível de uma avaria do provider.

Correcção pequena, duas opções: dar uma janela ao pool (relógio injectável que zere ao
cruzar o minuto — o gateway já tem o padrão `WithClock`), **ou**, se o rate-limit vive no
LiteLLM externo, pôr `LimitRPM/LimitTPM: 0` (o contrato diz `<=0 = ilimitado`) e declarar no
AOS-203 que o tecto é do gateway externo. O que não é defensável é declarar um limite que se
comporta como fusível.

### F2 — `max_turns` sem clamp do operador é um DoS de um pedido

`api.go:610` copia `MaxTurns: req.MaxTurns` cru do corpo do pedido; `loop.go:250-252` só
aplica o default quando `<= 0`; `WithMaxTurns` tem **zero chamadores de produção** — o tecto
do operador não existe.

Composto com F1: **um único `POST /runs {max_turns: 200}` esgota o `LimitRPM=120` e deixa o
nó incapaz de chamar o modelo até reiniciar.** O rate-limit de ingresso não protege (é 1
pedido, abaixo do burst), o tecto de in-flight não protege (é 1 run), e o wall-clock do
breaker está inerte (achado do A4). Em modo soberano o submissor é autenticado, logo é abuso
de insider; em modo legado o plano de dados é anónimo por ADR-016.

E mesmo sem malícia: um run legítimo e comprido consome o orçamento RPM de todos os outros,
porque o orçamento é do **processo**, não do run.

Correcção node-local, na fronteira de ingresso (`api.go:610`): clamp `req.MaxTurns` a um
tecto vindo de env (`AOS_MAX_TURNS`, por omissão 16). Não importa nada do SCH, não colide com
o boundary test.

> Estes dois são ortogonais ao escalonador. Estão neste relatório porque foi a lente de
> sobrecarga do A5 que os desenterrou, e porque respondem à pergunta que o A5 não faz — «o
> que protege o nó de carga hoje». Ambos têm ticket próprio.

---

## A contradição do tieradapter, resolvida

O A4 verificou **zero** pacotes de scheduler no fecho do nó; o A5 diz que o `tieradapter`
importa tipos do SCH em código produtivo. **Ambas são verdadeiras, e a razão é que o
`tieradapter` é código morto.**

Prova (executada neste commit, e reforçada além do que a lente trouxe):

1. `tieradapter.go:36` importa mesmo `control-plane/scheduler` num ficheiro não-teste.
2. `grep -rn "model-gateway/routing/tieradapter" --include=*.go packages/` → **um único
   import real**, o próprio `tieradapter_test.go`. As outras 6 ocorrências são comentários.
3. `go list -deps .` nos **cinco** binários com `main` do repo (`cmd/aos`, `cmd/aos-demo`,
   `cmd/aos-attestation`, `cmd/aos-issuer`, `platform/eval`) — **nenhum** puxa
   `routing/{router,tiering,degradation,routingstage,tieradapter}` nem `control-plane/scheduler`.

O subgrafo AOS-059 inteiro é composto **exclusivamente por testes**. A frase «importa tipos
do SCH em código produtivo» significa apenas «em ficheiro não-teste» e lê-se como «em uso».
Não há furo no guarda — o `tieradapter` vive noutro módulo (`platform`), fora do alcance do
prefix-match por desenho, e o guarda transitivo apanhá-lo-ia no instante em que entrasse no
fecho.

O que o A5 devia dizer, e não diz: o `tieradapter` não está «por ligar» — está **interdito
enquanto o nó consumir `NewProduction`**. Compô-lo dentro do `NewProduction` do GW
arrastaria `control-plane/scheduler` para o grafo do nó e reprovaria o guarda. É dívida a
declarar com eixo, não uma opção esquecida. Correcção de redacção, não erro factual.

---

## O que o documento NÃO diz e devia (achados que passaram pelo céptico)

### A. A emenda ao guarda talvez não seja exequível — e nomeia o símbolo errado (média→alta)

O A5 propõe emendar o boundary test de «módulo proibido» para «sem `Scheduler.Start`». Dois
problemas, ambos com evidência:

- **Não é expressável com a toolchain zero-dep.** `Admission`, `Breaker` e `Scheduler.Start`
  vivem **todos** em `package scheduler` — nenhum prefix-match admite um e nega o outro. Uma
  regra sobre um símbolo exige grafo de chamadas (`x/tools/go/callgraph`), que o repo não usa
  (`grep golang.org/x/tools` → zero) e que consumiria uma excepção de Carta ainda não
  concedida. A versão barata (AST só sobre `cmd/aos`) reabre exactamente o ponto-cego
  transitivo que o segundo guarda foi escrito para fechar.
- **Nomeia o símbolo errado.** `scale.go:740` tem um segundo laço de ciclo de vida —
  `HorizontalScaler.Run`, um `time.NewTicker` bloqueante que conduz a degradação global. Uma
  regra «sem `Scheduler.Start`» deixá-lo-ia passar: o guarda ficaria mais fraco *e* menos
  verificável.

Alternativa que preserva a hermeticidade (do céptico que sobreviveu): extrair as portas
puras (`AdmitRequest`, etc.) para um módulo-folha neutro fora do prefixo proibido — a regra
continua verificável pelo mesmo `go list -deps`, custo de CI zero. E o A2 já concluiu que,
para as mesmas portas, um adaptador node-local resolve **sem emenda nenhuma**.

### B. O «breaker por árvore» depende do fecho do A1, e o modo de falha é pior do que um deny (média)

`breaker.go:415` exige um `TreeBudgetReader` não-nil (fail-closed); `:279` satisfá-lo com
`*budget.Budget`; `:544` lê `Available(b.node)`. O A1 verificou que `budget.New/Rebuild` têm
zero chamadores não-teste. Logo o segundo «médio» **não é independente do primeiro** — e sem
o ciclo de vida do nó de orçamento por-run (o mesmo que o A1 identificou), o breaker lê
sempre um orçamento que ninguém povoa.

Nuance que o céptico corrigiu: na composição natural (um budget por run, cuja raiz `New`
regista), `Available` lê a raiz e **não** dá `ErrUnknownNode` na primeira avaliação — o modo
de falha catastrófico só surge para sub-árvores (`AddNode`, delegação). Continua a valer que
o item está sub-especificado.

### C. Filas e prioridade são single-node, não «depende de EPIC-10» (média, confirmado)

`queue.go` (filas por partição tenant:priority, backpressure) e `priority.go` (aging + EDF)
são inteiramente in-process — nenhum conceito de frota. Só `routing.go` («a frota EPIC-10 não
é implementada aqui») e `scale.go` deferem mesmo. Agrupá-las num balde «grande, EPIC-10 sem
horizonte datado» **arquiva o maior ganho single-node disponível**: limitar runs em voo,
propagar backpressure, evitar starvation — exactamente o que `service.go` hoje **não** tem
(`Submit` não tem tecto de concorrência).

### D. Assimetria de durabilidade entre admission e budget, não documentada (baixa)

O admission é genuinamente stateless (relê o stream), logo com store durável sobrevive a um
restart. O budget de referência guarda contadores em memória e `Rebuild` não os re-hidrata —
após restart nascem a **zero** (fail-open). O relatório dá a ressalva in-process ao admission
e agrupa os dois no mesmo item de fecho. *(O céptico refutou a versão forte deste achado — o
budget TEM um seam `Reserver` declarado, como o admission tem `EventLog`; sobrevive só a nota
editorial de que a assimetria não está escrita.)*

### E. Um estágio de identidade real do GW está órfão, satisfeito por um stub allow-all (média)

`production.go:178` exige `Authn` fail-closed, mas é guarda de **nil**, não de comportamento.
O nó passa `nodeModelAuthn{}` (`modelgatewaywiring.go:93-103`), que forja o principal e
devolve `allow` incondicional. O estágio real (`pipeline/authn`, valida EdDSA + raiz humana
ADR-003) tem zero importadores não-teste. *O deferimento está declarado no código*
(`production.go:128-133`, «dívida de AOS-057») e liga-se ao eixo **D4** já registado — mas
nenhuma secção do relatório o nomeia. Doc-incompleto, não gap escondido.

---

## Decisões que são do dono (não minhas)

**(i) A emenda ao guarda de fronteira.** Três caminhos, e a escolha é de política de
arquitectura:
- *Emendar para «sem Scheduler.Start»* — **não recomendo**: troca um guarda hermético e
  falsificável por `go list` por uma heurística de call-site mais fraca, que precisa de
  `x/tools` (excepção de Carta) ou fica não-automatizada.
- *Extrair as portas para um módulo-folha neutro* (`control-plane/loadgov` ou
  `scheduler-ports`) — mantém o guarda actual intacto; é um refactor de módulo, não uma
  edição de teste. **Recomendo esta se o token-bucket real do AOS-027 for mesmo necessário.**
- *Adaptador node-local* (a via do A2) — para as portas que são scheduler-free, resolve **sem
  tocar no guarda**. **Recomendo para o admission/budget do fecho 1.**

**(ii) Onde vive o rate-limit de throughput.** F1 força a decisão: ou o nó tem uma janela
RPM a sério, ou o tecto é do LiteLLM externo e o nó declara `ilimitado`. **Recomendo a
segunda** — é coerente com o deployment endurecido, que já põe o LiteLLM entre o nó e os
providers — com a condição de o AOS-203 o dizer e F1 deixar de fingir um limite.

**(iii) Um leitor da tabela do Grupo A.** Como no A4, «para a v1, nada» sob os prefixos
proibidos é verdade, mas o A5 tem trabalho **node-local** real (F1, F2, afinação do ingresso)
que não pertence ao escalonador e que a classificação «Grupo A / exige código novo» esconde.

---

## Plano A5 revisto

O que é do escalonador continua deferido e honesto. O que muda é separar dele o trabalho
node-local que a avaliação desenterrou.

| # | Passo | Depende de | Esforço | Face ao documento |
|---|---|---|---|---|
| **0** | **F1** — janela no keypool **ou** `LimitRPM=0` + nota AOS-203 | — | pequeno | **NOVO.** Defeito vivo, não é do SCH. |
| **1** | **F2** — clamp de `max_turns` na fronteira (`AOS_MAX_TURNS`) | — | pequeno | **NOVO.** DoS de um pedido. |
| **2** | Expor os três knobs de ingresso já implementados por env + teste do tecto de in-flight | — | pequeno | **NOVO.** Corrige «sem backpressure». |
| **3** | Corrigir o texto do A5: «≥90%»→«≥75%»; «sem backpressure»→«backpressure de ingresso existe»; tieradapter é código morto interdito; separar filas/prioridade (single-node) de routing/escala (EPIC-10) | 0-2 | pequeno | **NOVO.** Trabalho de documento. |
| **4** | Ciclo de vida do nó de orçamento por-run (partilhado com o A1) — node-local | A1 | pequeno-médio | **NOVO.** Bloqueante de qualquer admission. |
| **5** | Decidir (i): adaptador node-local vs extracção de módulo | decisão | — | **MUDA** o fecho 1. |
| **6** | admission/budget como colaborador do run, pela via escolhida em 5 | 4,5 | médio | Âmbito corrigido. |
| **7** | breaker por árvore — só depois de 4+6; declarar o modo de falha (trip+ParkTree) | 6 | médio | **MUDA** o fecho 2. |
| **8** | filas (AOS-030) + prioridade (AOS-032) — single-node | 6 | médio | **MUDA** o fecho 3 (tira-as do balde EPIC-10). |
| **9** | routing least-loaded + escala por SLIs | EPIC-10 | grande | Igual. |

Ordem mínima defensável para um lote: **0 → 1 → 2 → 3**. Fecha dois defeitos vivos de
disponibilidade e alinha o documento, sem tocar em nenhum módulo proibido.

---

## O que foi REFUTADO

- **«O admission via GW é uma opção esquecida numa chamada.»** Meia-refutado: são dois
  estágios que disputam o mesmo slot `"roteamento"`, mas o slot aceita composição (interface
  de 2 métodos) — não é «bifurcação estrutural», e o A5 nem propõe a via GW. Severidade
  rebaixada de alta para baixa.
- **«O archlint de no-bypass do GW nunca analisa código real.»** Factualmente errado:
  `TestAnalyzeTree_ConsumidoresLimpos` corre `AnalyzeTree("../../..")` sobre todo o `packages/`
  e passa em CI via `scripts/ci/routing.sh`.
- **«Nomear o `SpawnCoordinator` resolveria a fuga de duas-fases.»** O nó não tem caminho de
  spawn (um goal, um agente); sem sub-agentes não há fuga a reintroduzir, e o `SpawnCoordinator`
  importa também o *orchestrator*, agravando a fronteira.
- **«"Com o Store de referência" nomeia um componente inexistente.»** O Model Gateway
  existe; inexistente é só a implementação partilhada do `EventLog`, que é exactamente o que
  o README declara em falta.
- **«O A1 omite a limitação de durabilidade do budget.»** Não omite — enuncia-a em `:158` e
  `:341`, com menos resolução.
- **«O risco "sem TPM/RPM agregado" não tem item de fecho.»** O requisito que o fecharia
  (EventLog partilhado) é o EPIC-10, para onde o item grande remete.

---

## Ver também

- [`desafio-A1-budget-admission-control.md`](./desafio-A1-budget-admission-control.md)
- [`desafio-A2-progress-surface.md`](./desafio-A2-progress-surface.md)
- [`desafio-A3-credential-broker.md`](./desafio-A3-credential-broker.md)
- [`desafio-A4-orquestrador.md`](./desafio-A4-orquestrador.md)

- [`desafio-A6-attestation.md`](./desafio-A6-attestation.md)

## Rastreabilidade

Transcrições por agente: `.claude/projects/…/subagents/workflows/wf_ed92f85a-1ba/journal.jsonl`
(as 6 lentes e 4 dos 6 refutadores; `refutar:sobrecarga`, `refutar:emenda` e a síntese
caíram por erro de API / limite de sessão — daí a síntese à mão e a verificação reforçada de
F1/F2).

> **Nota de âmbito:** este relatório NÃO altera `prontidao-modelos-agenticos.md`. Os dois
> achados de disponibilidade (F1, F2) têm ticket próprio.
