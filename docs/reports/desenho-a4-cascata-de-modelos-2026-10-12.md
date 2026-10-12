# Desenho A4 — cascata de modelos: que modelo serve cada passo, e como se muda sem deploy

> **Estado deste documento.** Desenho apresentado ao dono a 2026-10-12, no dia em que a prova da
> fase A3 foi medida em produção. **Nenhum ticket está aberto e nenhum código foi escrito:** os
> tickets da A4 só se abrem depois de o dono tomar as decisões D1 a D10 (§7). Os itens de
> trabalho têm rótulos provisórios — «A4-medição», «A4-pacote», e assim por diante — que não são
> identificadores de ticket; a numeração faz-se na altura, com `scripts/ci/sessoes.py reservar`.
>
> Data: 2026-10-12. Base lida: `feature/AOS-128-ux-dx-tests` @ `25b85e9d`. Produção: v0.1.55.
> Trabalho de leitura e desenho: nada foi pedido a um modelo nem a produção.
>
> **De onde vêm os números da A3.** As medições da prova da A3 (o exame do `k3`, a série
> `v0155c`) ainda não estão na base lida: estão no ramo `docs/AOS-523-prova-da-a3`, commit
> `c3e03183`, que actualiza o documento de acompanhamento. Foram lidas daí e estão marcadas
> `[MEDIDO-A3]`. Este desenho não toca nesse documento.
>
> **Como ler as marcas.** `[CÓDIGO]` = lido no código, com ficheiro:linha. `[MEDIDO]` = número
> registado no documento de acompanhamento
> (`acompanhamento-arquitectura-alvo-fronteira-modelo.md`, §5). `[MEDIDO-A3]` = o mesmo, mas do
> ramo da prova da A3. `[HIPÓTESE]` = não confirmado. `[NÃO ENCONTREI]` = procurei e não está lá.
>
> Abreviaturas de caminho: `gw/…` = `packages/platform/model-gateway/…`; `nó/…` =
> `packages/cmd/aos/…`; `orq/…` = `packages/cmd/aos-orq/…`; `banco/…` =
> `packages/qa/banco-ensaio/…`; `plano/…` = `packages/control-plane/orchestrator/plan/…`.
>
> **Palavras usadas aqui.** *Passo*: um nó de um plano (ler um documento, resumir, guardar).
> *Degrau*: um lugar numa escada de modelos, do mais barato ao mais caro. *Subir*: repetir um
> passo num degrau acima. *Pacote de decisão* (o `decision pack` da tabela de fases): um ficheiro
> assinado pelo dono com as regras e os limiares da escolha. *Titular* e *candidato*: como na A3.

## 0. A conclusão, à cabeça

**A A4, tal como está escrita na tabela de fases, não se justifica com os modelos que há hoje.**
Não por estar mal pensada, mas porque lhe faltam as duas coisas de que vive: uma diferença de
custo e uma diferença de capacidade, medidas, entre os modelos que podem servir produção.

A evidência, em quatro linhas:

| O que a cascata precisa | O que há hoje |
|---|---|
| Um modelo barato e um caro | Em produção só podem servir dois modelos, o `kimi-for-coding` e o `k3`. São da mesma família, da mesma conta, e pagos pelo mesmo plano de preço fixo: em dólares, um passo custa o mesmo num e noutro `[MEDIDO-A3]` (o exame avisa «tecto em dólares não imposto, fornecedor por plano») |
| Um modelo caro que resolva o que o barato não resolve | O único modelo de outra família, o Claude, não tem região na UE por esta via e não pode servir produção, que está selada para `eu-west` `[MEDIDO]` |
| Passos em que o barato falha e o caro não | Não se conhece nenhum. Os dois modelos do Kimi cumprem 40 de 40 nós com tools e 16 de 16 sem tools na bateria `[MEDIDO]` `[MEDIDO-A3]`. A bateria são seis casos sintéticos fáceis |
| Uma falha que subir de modelo resolva | A falha que existe — o modelo não chama a tool à primeira, entre 1,2% e 12% conforme o dia — já é recuperada pela nova tentativa **com o mesmo modelo**: desde que o aviso está ligado, nenhum plano falhou por essa causa em 209 planos (as sete séries de `v0151a` a `v0155c`) `[MEDIDO]` `[MEDIDO-A3]` |

Há um sinal a favor, e um contra, e nenhum dos dois está lido:

- **A favor.** O `k3` não falhou nenhuma primeira tentativa: 0 em 40 no exame e 0 em 14 em
  produção. No mesmo dia o titular falhou 4 em 33 nós com tools (12%) e 2 em 5 `[MEDIDO-A3]`.
- **Contra.** O `k3` deixou de fora factos do documento em 15 de 56 respostas cumpridas (27%); o
  titular, 2 em 56 na véspera `[MEDIDO-A3]`. Ninguém leu as 15 respostas. Pode ser um modelo que
  resume pior, ou um que escreve «2 356» onde o banco procura «2356».

**O que este desenho recomenda, por ordem:**

1. **Medir antes de construir** (decisão D1). Uma medição barata, no banco de ensaio, sem tocar
   no nó: os dois modelos lado a lado, na bateria de hoje e numa versão com casos mais difíceis,
   e as 15 respostas do `k3` lidas. Custa um PR pequeno e cerca de uma hora de máquina. Responde
   à pergunta de que tudo depende: *há passos em que um modelo falha e o outro não?*
2. **Se a resposta for «não»:** a A4 fica desenhada e por construir, até entrar um modelo que
   seja de facto mais caro ou mais capaz — o que pede primeiro uma decisão de fornecedor e de
   região (a decisão D9 da A3, por tomar).
3. **Se a resposta for «sim»:** construir o mínimo da §5, que é mais pequeno do que o nome
   «cascata» sugere. Quase tudo o que é preciso já existe: cada passo de um plano já é um run à
   parte, a nova tentativa já existe com prova de que não repete efeitos, o modelo já fica
   gravado por run, e a pasta de ficheiros assinados já é lida no arranque.

O resto do documento descreve esse mínimo com o cuidado necessário para o dono poder decidir
já, e não ter de voltar a pedir um desenho se a medição disser «sim».

## 1. O que a A4 promete e o que não promete

### Um exemplo

Um plano de dois passos, dos que correm hoje em produção:

1. **Ler** o documento `escala-turnos.txt` com a tool de leitura.
2. **Resumir** o que foi lido, em duas frases, sem perder os nomes nem os números.

O passo 1 é fácil: pedir uma tool com o argumento certo. Qualquer modelo que saiba chamar tools
o faz. O passo 2 pode ser difícil: se o documento for longo, ou se o resumo tiver de cruzar
dados, um modelo fraco perde factos.

Hoje os dois passos vão ao mesmo modelo, sempre. A A4 quer que o passo 1 vá ao modelo barato e
que o passo 2 vá ao caro **só quando precisar** — e que a regra que decide «quando precisa» se
possa mudar com uma assinatura do dono, sem PR e sem imagem nova.

### Promete

- **Cada passo de um plano pode ser servido por um modelo diferente**, escolhido por uma regra
  escrita, e não por quem submete o plano.
- **A escolha é sempre a mesma para os mesmos factos.** Sem sorteio, sem relógio, sem um modelo a
  avaliar outro. Quem ler o registo de um run sabe que modelo o serviu e que regra o escolheu.
- **A regra vive num ficheiro assinado pelo dono** — o pacote de decisão. Mudá-la é assinar outro
  ficheiro e recriar o nó. Zero PRs, zero releases.
- **A divisão entre modelos mede-se.** Para cada série de planos lê-se quantos passos ficaram em
  cada degrau, quantos subiram, e porquê.
- **Nada piora por omissão.** Sem pacote, o nó é byte a byte o de hoje.

### Não promete

- **Não promete poupar dinheiro com os modelos de hoje.** Os dois modelos de produção custam o
  mesmo (§0). O que se pode medir com eles é tempo, tokens e taxa de falha.
- **Não promete adivinhar a dificuldade de um passo.** Não há hoje nenhum sinal medido que diga,
  antes de tentar, que um passo é difícil (§3). A promessa que se consegue cumprir é mais
  modesta: *tentar no degrau de baixo e subir quando o veredicto do kernel é negativo*.
- **Não promete apanhar uma resposta má que pareça boa.** Subir depende do veredicto do kernel,
  e o veredicto só vê duas coisas: a tool não foi chamada, ou a resposta veio vazia. Um resumo
  que perde factos é, para o kernel, um run cumprido. A lição da A2 — «`cumprida` não prova
  leitura» — vale aqui por inteiro.
- **Não promete mudar de modelo a meio de um passo.** Um run começa e acaba no mesmo modelo,
  como na A3. Mudar a meio partia o estado opaco (ADR-040 §2.11).
- **Não promete mudar a quente.** O pacote lê-se no arranque, como os perfis de rota. «Sem
  deploy» quer dizer sem PR e sem imagem; o nó tem de ser recriado.
- **Não decide com um juiz probabilístico**, e não aprende em produção. As duas coisas estão
  fora da arquitectura-alvo (acompanhamento, §8) e do ADR-021 (regra 4: calibração offline,
  nunca online).
- **Não cobre o planeador.** O `aos-orq` chama o modelo por conta própria, com um gateway seu,
  fora da governação da rota `[CÓDIGO]` (`nó/model_route_env.go:22-24`). Como na A3, fica de
  fora, e dito.

## 2. O que já existe e se reaproveita

Tudo o que está nesta tabela foi lido no código da base. Onde não encontrei, está dito.

| Peça | Onde está `[CÓDIGO]` | O que falta para a A4 |
|---|---|---|
| **Cada passo de um plano é um run à parte** | O `aos-orq` submete cada nó do plano ao nó `aos` como um `POST /runs` próprio, com as tools do passo, as entradas e o contrato de conclusão (`nó/api.go:765` e seguintes: `tools`, `inputs`, `plan_request`, `completion_requires`). A nova tentativa é outro run novo (`nó/nova_tentativa.go:134`) | Nada. **É o facto que torna a A4 pequena:** escolher por passo é escolher por run, e o nó já escolhe por run |
| **O modelo fica fixado e gravado por run** | `escolherModeloDoRun` corre uma vez na hospedagem, grava `run.model_pinned` (papel, alias, digest do perfil, digest do registo, versão da função, razão, chave do plano) e só depois o run leva a escolha (`nó/modelo_fixado.go:457-517`). A retoma não a sobrepõe | Mais campos no mesmo evento: o pacote, a regra que disparou, o número da tentativa. Uma versão nova da função de escolha |
| **Um despachante com um adaptador por modelo** | `clientePorModelo`, um adaptador por alias sobre o mesmo gateway, que nunca cai no titular (`nó/modelo_candidato.go:185-211`) | Aceitar mais de dois aliases. Hoje o tecto é dois, de propósito (`nó/modelo_series.go:35`; `ErrModelCandidateMoreThanOne`, `nó/modelo_candidato.go:47`) |
| **A nova tentativa, com prova** | O `aos-orq` volta a submeter um passo que acabou sem chamar a tool (`orq/nova_tentativa.go`), e o nó só hospeda a tentativa depois de provar **no seu próprio log** que a anterior não pediu tool nenhuma (`nó/nova_tentativa.go:257`, ADR-039). Em produção, ligada, até duas tentativas a mais | **É a escada pronta.** Falta só uma coisa: a tentativa seguinte poder ir a outro modelo. Hoje vai ao mesmo, por regra escrita (ADR-041, §6: «a nova tentativa é um run novo do mesmo pedido — e por isso do mesmo modelo») |
| **Ficheiros assinados lidos de uma pasta** | Os registos de entrada dos perfis: pasta, assinatura ed25519, âncora por configuração, conferência no arranque com causa própria por falha (`gw/policy/routeentry/load.go:83`), e o carregamento selado | O molde exacto para o pacote de decisão. Um tipo de ficheiro novo na mesma pasta |
| **Uma tabela de pesos assinada** | Quatro perfis de pesos (`balanced`, `fast`, `cheap`, `quality`) num ficheiro assinado (`gw/policy/weights/weights_table.json`). **Embebido no binário** (`gw/policy/weights/weights.go:63-70`): mudá-lo é um PR e uma imagem. Existe uma função que aceita uma tabela vinda de fora (`LoadSignedTable`, `:191`); não encontrei quem a use para carregar de uma pasta | Não serve tal como está: é o contrário de «sem deploy». Fica como precedente de formato |
| **Tiers no gateway** | Uma escada com três graus de capacidade — `basic`, `standard`, `frontier` — e uma ordem de custo (`gw/routing/tiering/tiering.go:33-41`, `:60-76`). A regra: o degrau mais barato que satisfaz a capacidade pedida | Ver a linha seguinte: a «capacidade pedida» não vem do passo |
| **De onde vem a «capacidade exigida» hoje** | **Do modelo que foi pedido, não do passo.** O classificador de produção lê o tier do modelo pedido e usa-o como piso (`gw/routing/routingstage/classifier.go:168-179`): «quem pede um modelo frontier exige raciocínio». Não olha para o passo. O campo de tokens estimados fica a zero, porque o estágio não vê o prompt (`:181-193`) | **Não há estimador de dificuldade em sítio nenhum** `[NÃO ENCONTREI]`. É a peça que a linha da A4 dá por adquirida e que não existe |
| **A escada está ligada no nó?** | **Não.** O nó não declara tiers e roteia só pelo failover (`nó/modelgatewaywiring.go:246-250`; dívida DEF-280-NO, aberta no registo de deferimentos). O scoring do ADR-021 está composto no módulo do gateway e não corre no binário de produção. Há um ticket aberto para isso, o AOS-421, que diz de si: «não existe fonte de verdade para a escada: não há env, não há formato, não há artefacto assinado» | A A4 tem de decidir se usa esta escada ou não (decisão D10). Este desenho recomenda que não |
| **A «descida de tier»** | É a degradação por orçamento: perto de 80% do orçamento gasto, o gateway *oferece* um degrau mais barato (`gw/routing/degradation/degradation.go:1-28`). Actua **por pedido**, isto é, por turno, e pode trocar o modelo a meio de um run. Sem tiers declarados não corre. No AOS-520 ficou por confirmar o que lhe acontece num run fixado | Fica desligada. Trocar de modelo por turno é o que a A3 proibiu |
| **O que o plano diz sobre um passo** | Papel, objectivo, tools, dependências, estimativa de orçamento, classe de risco, saídas e entradas (`plano/plandocument.go:119-148`). **Nenhum campo de modelo, de tier ou de dificuldade.** A estimativa de orçamento é uma proposta do modelo planeador que o sistema volta a calcular e não ecoa (`:45-49`). A classe de risco é só conselho: pode subir o risco derivado, nunca baixá-lo (`:10-19`) | O precedente para uma dificuldade declarada, se o dono a quiser: um campo que só pode **subir** o degrau. Muda o schema do plano |
| **Custo por turno** | Cada turno gravado tem tokens de entrada, de saída, de cache, e o custo em micro-dólares (`packages/kernel/agent-runtime/turn.go:63-138`), calculado de uma tabela de preços por modelo e região (`nó/model_pricing_env.go`). A tabela embebida tem só o Claude e o `gpt-4o`; sem preço para o par em uso, o custo gravado é zero, declarado no banner | Uma tabela de preços do Kimi `[NÃO ENCONTREI]` no repositório (o caminho é uma variável do servidor, que não li). Num plano de preço fixo, um preço por token é uma convenção, não uma factura |
| **Orçamento por run** | Tecto em tokens (`AOS_BUDGET_MAX_TOKENS`) e, opcional, em micro-dólares (`AOS_BUDGET_MAX_COST_MICRO_USD`), por run (`nó/budget_env.go:1-30`) | Continua a valer por run, qualquer que seja o modelo. Um passo que sobe gasta dois orçamentos de run, como já acontece numa nova tentativa |
| **O que se mede por modelo** | Com um candidato a servir: turnos por motivo de paragem, respostas recusadas e runs terminados por desfecho, com o modelo no rótulo (`nó/modelo_series.go:1-24`) | Tokens, custo e **tempo** por modelo não estão no `/metrics` `[NÃO ENCONTREI]`. Os tokens e o custo lêem-se do registo dos turnos; o tempo de um turno não fica gravado em lado nenhum `[NÃO ENCONTREI]` — mede-se por fora, pela duração dos planos |
| **Banco de ensaio** | Bateria de seis casos e sete nós, com verificação de factos por texto exacto (`banco/bateria.go:87-93`); veredicto calculado; aceita um perfil candidato | Comparar **dois** modelos na mesma corrida e por caso; casos mais difíceis; o tempo por pedido, que o banco não regista `[NÃO ENCONTREI]` |
| **Limiares que chegam ao nó sem deploy** | Três vias existem: o bundle de política do PDP numa pasta com âncora (`nó/main.go:93-114`), a allowlist externa, e os perfis de rota assinados. A fatia e o limite do canary (10%, 40 planos) vêm no registo assinado; a regra do disjuntor (3 em 20) está em código (`nó/modelo_disjuntor_regra.go`) | O pacote de decisão é a quarta via, pelo mesmo molde |
| **«Decision pack»** | A expressão aparece uma vez em todo o repositório: na linha da A4 da tabela de fases. Formato, código ou ADR `[NÃO ENCONTREI]` | Define-se aqui (§3.2) |

**Os princípios e os ADR que mandam aqui.**

- **ADR-041** (titular e candidato). Três regras suas colidem com a A4 e têm de ser emendadas
  por um ADR novo, não contornadas: *um plano, um modelo*; *no máximo um modelo além do titular*;
  *a nova tentativa usa o mesmo modelo*. A §4 trata disto.
- **ADR-021** (scoring determinista). As cinco regras valem para o pacote de decisão: guardas
  primeiro (soberania e allowlist nunca são factores de escolha); pesos e limiares como
  política assinada e versionada; calibração offline, sem exploração em produção; uma troca de
  modelo é sempre registada com a razão. A regra 3 pede eval-gate para uma tabela nova
  (ADR-012): o fim da §3.2 diz como se cumpre sem PR.
- **ADR-040 §2.11** e **ADR-036 §2.8**: o estado opaco e o perfil ficam presos à rota que os
  produziu. Por isso a escolha é por run, nunca por turno.
- **ADR-039** (a nova tentativa): a prova de que a tentativa anterior não pediu tools é o que
  permite repetir sem repetir efeitos. Subir de modelo usa a mesma prova, sem a alargar.
- **ADR-018**: a escolha do modelo de um run é do nó, não do `aos-orq`.
- *Soberania regional* e *política default-deny* (`AGENTS.md` §7): um degrau só pode ter um
  modelo que a allowlist assinada já autoriza para o board e a região. O pacote escolhe entre
  modelos autorizados; não autoriza nenhum.

## 3. As duas perguntas centrais

### 3.1 Como se estima o que um passo exige?

Há cinco maneiras. A tabela diz, para cada uma, o que é, se dá sempre a mesma resposta para os
mesmos factos (determinista), se o replay a reproduz, e o que a sustenta hoje.

| Maneira | O que é | Determinista? | O replay reproduz? | O que a sustenta hoje |
|---|---|---|---|---|
| **(1) Declarada por quem escreve o plano** | Uma pessoa escreve no plano «este passo é difícil» | Sim: é um campo do documento | Sim | Nada medido. E colide com uma regra da A3: quem submete não escolhe o modelo (ADR-041 §3). Só é aceitável como o risco já é — um campo que **sobe** o degrau e nunca o desce |
| **(2) Declarada pelo planeador** | O modelo que decompõe o plano classifica cada passo | **Não** na origem: é um modelo a opinar. Depois de escrito no plano, é um facto fixo | Sim, do plano gravado | Nada medido. O planeador chama o modelo fora da governação da rota, e pedir-lhe que julgue dificuldade é pôr um juízo probabilístico a decidir custo. Muda o schema do plano e o prompt do planeador |
| **(3) Inferida de sinais do passo** | Uma regra sobre factos que o nó já tem quando o run chega: tem tools? tem entradas de outro passo? quantas e de que tamanho? tem contrato de conclusão? é verificador? | **Sim**: função pura de factos selados no run | Sim | **Nenhum sinal está provado como preditor.** O que se mediu: as falhas «não chamou a tool» só ocorrem em passos com tools; as respostas vazias só em passos sem tools; nem os tokens de saída nem a cache predizem a falha `[MEDIDO]` (2026-10-06). Não há medição de «passo com entrada grande falha mais» |
| **(4) Medida: tentar e subir** | O passo corre no degrau de baixo. Se o veredicto do kernel for negativo por uma causa da lista, a tentativa seguinte corre no degrau acima | **Sim**: função do número da tentativa e do veredicto selado da anterior, que o nó prova no seu log | Sim: cada tentativa é um run com o modelo gravado | A máquina existe e está em produção (a nova tentativa). O que não se sabe é se subir **ajuda**: repetir no mesmo modelo já recupera quase tudo (§0) |
| **(5) Aprendida do histórico** | Uma análise offline dos runs passados diz que classes de passo falham em que modelo, e o resultado vira uma regra nova do tipo (3) | A análise não; a regra que dela sai, sim | Sim, da regra | Não há volume: as séries de produção são 20 a 60 planos, lançadas à mão, sempre com o mesmo objectivo. Aprender em produção, a quente, está proibido (ADR-021, regra 4) |

**Recomendação: (4) como base, com (3) preparado e vazio.**

- **(4) é a única que não precisa de adivinhar.** Não estima a dificuldade: observa-a. E usa
  só o que o kernel calcula e sela — o veredicto —, nunca o texto do modelo.
- **(3) fica na forma do pacote, sem regras.** A tabela «classe de passo → degrau de partida»
  existe no ficheiro e começa com uma só linha: «tudo começa no degrau 1». Uma regra nova entra
  quando uma medição a sustentar, e entra por assinatura. É assim que (5) chega a produção: como
  uma regra (3) assinada.
- **(1) e (2) ficam de fora por agora.** Se um dia entrarem, entram como o risco: só sobem.

**O limite de (4), dito sem rodeios.** Subir só acontece quando o kernel diz «não cumprido», e
o kernel só o diz em duas situações: a tool do contrato não foi chamada, ou a resposta veio
vazia. Um passo de resumo que devolve um resumo pobre fica no degrau de baixo para sempre. Para
esses passos, a única defesa determinista é uma regra do tipo (3) — «passos sem tools e com
entradas vão directos ao degrau 2» —, e essa regra só se deve assinar com uma medição que
mostre que o degrau 2 resume melhor. Hoje o único número que há aponta ao contrário (os factos
ausentes do `k3`), e está por ler.

**O custo de (4).** Um passo que sobe paga uma tentativa falhada primeiro: mais 20 a 40 segundos
e os tokens dessa tentativa `[HIPÓTESE]` (os planos de dois passos demoram cerca de 40 s
`[MEDIDO]`). Com 1,2% a 12% de primeiras falhas, é pouco. Com um degrau de baixo que falhasse
metade das vezes, seria pior do que ir directo ao de cima — e é por isso que a divisão se mede.

### 3.2 Onde vivem os limiares, e como mudam sem deploy?

**O que é o pacote de decisão.** Um ficheiro pequeno, assinado pelo dono, que o nó lê no
arranque. Contém, em vocabulário fechado:

| Parte | O que diz | Exemplo |
|---|---|---|
| Identificação | Versão do formato, número de sequência, data | sequência 3 |
| A escada | Os degraus, do mais barato ao mais caro. Cada degrau aponta para um modelo **pelo digest do registo de entrada assinado** — não pelo nome | degrau 1 → o registo do `kimi-for-coding`; degrau 2 → o registo do `k3` |
| Onde se começa | A tabela «classe de passo → degrau de partida». As classes são combinações fechadas de sinais do passo | «qualquer passo → degrau 1» |
| Quando se sobe | Que causas do veredicto fazem a tentativa seguinte subir, e quantos degraus | «`contract_unmet_no_call` → sobe 1»; «`empty_output` → sobe 1» |
| Tectos | Quantas subidas por passo; a partir de que taxa de subidas se avisa o dono | 1 subida por passo; aviso acima de 20% em 50 passos |
| Modo | `observe` (grava o que escolheria, não muda nada) ou `enforce` | `observe` |

**Onde pode viver.** As alternativas:

| Opção | Muda sem deploy? | Assinado? | Custo | Problema |
|---|---|---|---|---|
| (a) Variáveis de ambiente | Sim | Não | nenhum | Quem edita o `.env` do servidor decide para onde vai o tráfego, sem prova de quem foi |
| (b) Ficheiro assinado na pasta dos perfis | Sim | Sim, pelo dono | pequeno: o molde existe | Lê-se só no arranque |
| (c) A tabela de pesos do ADR-021 | **Não**: está embebida no binário | Sim | médio | Mudar é um PR e uma imagem |
| (d) O bundle de política do PDP | Sim | Sim | médio | É a política de **autorização** das tools, com outra chave e outro significado. Misturar «quem pode» com «que modelo serve» confunde duas decisões |
| (e) Um artefacto do Registry | Sim, depois de montado | Sim | grande | O Registry não corre em nenhum binário de produção (desenho da A3, §2) |

**Recomendação: (b).** É o mesmo raciocínio da decisão D1 da A3, pela mesma razão: cumpre o
critério com o que já está escrito.

**Quem o pode mudar.** Só quem tem a chave de assinar. A pasta do servidor não dá esse poder:
quem lá escreve pode **retirar** um pacote ou **repor um antigo**, mas não criar um novo. Repor
um antigo é o resíduo (h) da A3 («os registos de entrada não têm validade nem anti-recuo»); o
pacote nasce com número de sequência para o fechar do seu lado: o nó grava a sequência mais alta
que já carregou e recusa arrancar com uma mais baixa.

**Como se prova que mudou, e quando.** Por quatro rastos, três dos quais já são o hábito da A3:

1. No arranque, o banner do nó diz o digest e a sequência do pacote em vigor.
2. Um evento selado «pacote carregado», com o digest, a sequência e a hora do nó.
3. Cada run grava, no `run.model_pinned`, o digest do pacote que o escolheu. Uma série lida do
   log diz, run a run, que pacote estava em vigor.
4. Uma série no `/metrics` com a sequência do pacote, para um alerta poder ver que mudou.

**O que o nó confere no arranque** (e recusa arrancar se falhar, com causa própria para cada):
a assinatura; a sequência; que cada degrau aponta para um registo de entrada que está na pasta,
é válido e está `qualificado`; que a allowlist autoriza o modelo de cada degrau; que nenhuma
regra refere uma classe ou uma causa fora do vocabulário fechado. Um pacote que refere um modelo
que não passou o exame não é um pacote com um aviso: é um nó que não arranca.

**O eval-gate (ADR-012 e ADR-021, regra 3).** Um pacote novo muda o comportamento, e a regra é
que nada que mude o comportamento entra sem avaliação, canary e ratificação. Cumpre-se assim,
sem PR: cada modelo do pacote já passou o exame e a assinatura da A3 (a avaliação); o pacote
entra em `observe` antes de `enforce` (o canary — vê-se a divisão que daria antes de a dar); e a
assinatura do dono é a ratificação. Opcional, e útil: um comando do banco que aplica o pacote
novo aos runs já gravados e mostra a divisão que ele **teria** dado. É uma função pura sobre o
log; não pede nada a modelo nenhum.

## 4. Conviver com a A3

### 4.1 Onde colide, com precisão

A decisão D6 da A3 disse «a escolha por passo fica para a A4». O que ficou no código e no
ADR-041 são quatro regras; só três colidem.

| Regra da A3 | Onde está | Colide com a A4? |
|---|---|---|
| **O modelo fica fixado por run** e não muda a meio, nem numa retoma | ADR-041 §2.4; `nó/modelo_fixado.go:457` | **Não.** Um passo de um plano é um run. Escolher por passo é escolher por run. A A4 nunca muda o modelo dentro de um run |
| **Um plano, um modelo**: todos os passos e todas as tentativas de um plano usam o mesmo | ADR-041 §2.2 («a unidade do canary é o plano») e §3 | **Sim.** É exactamente o contrário do que a A4 quer |
| **A nova tentativa usa o mesmo modelo** | ADR-041 §6; a chave do plano é a mesma em todas as tentativas | **Sim**, com «tentar e subir» |
| **No máximo um modelo além do titular** | ADR-041 §2.1; `nó/modelo_candidato.go:47` | **Sim**, se a escada tiver um degrau que não seja o titular **e** houver um candidato em canary ao mesmo tempo |
| **Quem submete não escolhe** | ADR-041 §3; `nó/api.go:761-766` | Não, desde que a A4 não ponha um campo de modelo no plano nem no pedido |

**Porque é que a A3 quis «um plano, um modelo».** Duas razões, escritas no ADR-041 §3: sem isso
não se sabe a que modelo atribuir a falha de um plano; e o passo seguinte recebia a saída de
outro modelo. As duas são razões de **medição do canary**, não de segurança. A A4 não as pode
ignorar: tem de dizer como se mede um modelo quando os planos passam a ser mistos.

### 4.2 Três formas de conciliar

| Forma | O que é | A favor | Contra |
|---|---|---|---|
| **(a) A cascata só entre titulares; o canary fica à parte** | A escada só tem modelos que já acabaram a entrada da A3 (exame, assinatura, canary, promoção pelo dono). Um plano atribuído a um candidato em canary **não entra na cascata**: corre inteiro no candidato, como hoje | O canary continua a medir o que media. As regras da A3 mantêm-se para o candidato. Um modelo só chega a um degrau depois de provado | «Titular» passa a ser «titular de um degrau»: deixa de haver um só. Um candidato é sempre testado em planos inteiros, mesmo que o seu destino seja o degrau 2 |
| **(b) O canary passa a medir por passo** | A unidade do canary deixa de ser o plano e passa a ser o run | A cascata e o canary correm juntos sem regra especial | Desfaz a decisão D4 da A3 no dia seguinte a ser provada. O disjuntor e o livro do canary contam por plano; mudava-se tudo |
| **(c) Não correm ao mesmo tempo** | Com um canary aberto, a cascata desliga-se; com a cascata ligada, não se abre canary | A mais simples de escrever | Enquanto houver um candidato, não há cascata. Com a entrada de modelos a tornar-se rotina, a cascata estaria quase sempre desligada |

**Recomendação: (a).** A leitura que a torna natural: a A3 é a **porta de entrada** de um
modelo; a A4 é a **distribuição do trabalho** entre os modelos que já entraram. Um modelo passa
primeiro por uma, depois pela outra.

Na prática, com os modelos de hoje: o `kimi-for-coding` é o degrau 1 (já é o titular). Para o
`k3` ser o degrau 2, o dono tem de o tirar do estado de candidato — a decisão «passar a titular,
ou sair» que a A3 deixou fora do relógio — e um pacote assinado tem de o nomear. Enquanto for
candidato, continua a receber 10% dos planos, inteiros, e a cascata não lhe toca.

### 4.3 O que acontece ao disjuntor

O disjuntor da A3 vigia **o candidato** e nunca toca no titular (ADR-041 §2.9). Com a forma (a),
os modelos da escada não são candidatos: **nenhum disjuntor os vigia.** Fica dito como limite, e
compensa-se com duas coisas mais simples do que um segundo disjuntor:

- **Um degrau de cima que falha não tem para onde subir**: o passo falha, como hoje falha depois
  de esgotar as tentativas. Nada de novo.
- **Um degrau de baixo que degrada faz tudo subir.** É o risco próprio da cascata: sem ninguém
  reparar, todo o trabalho passa para o modelo caro. A defesa é o **tecto de subidas** do
  pacote: acima de uma taxa escrita, sai um alerta (o molde é o `alerta-disjuntor.sh`). Na
  primeira versão é só alerta, lido do log e das métricas por um script; não corta nada. A razão
  está no ponto seguinte.

**Porque o tecto não corta, na primeira versão.** Para cortar, o nó teria de manter um contador
partilhado e escrevê-lo com concorrência optimista — exactamente o que o livro do canary faz, e
exactamente o que não funciona com mais de uma réplica do nó sobre JetStream (AOS-524). A
escolha da cascata, tal como está desenhada, **não precisa de contador nenhum**: depende só dos
factos do próprio run e do pacote. Mantê-la assim é o que a deixa fora do alcance desse defeito.

### 4.4 O que fica gravado por passo

Para o replay reproduzir a escolha, e para a divisão se poder medir só do log, o
`run.model_pinned` de cada run passa a levar, além do que já leva:

| Campo | Para quê |
|---|---|
| O digest e a sequência do pacote de decisão | Saber que regras estavam em vigor |
| A versão da função de escolha (uma nova, ao lado de `aos-canary/v1`) | Uma função que escolha de outra maneira é outra versão |
| O degrau escolhido e o modelo desse degrau, com o digest do registo | O que serviu |
| A regra que decidiu: «partida» ou «subida», e qual linha do pacote | Porquê |
| Os sinais usados, em vocabulário fechado (tem tools, tem entradas, tem contrato, é verificador) | Conferir a regra sem ler o pedido |
| O número da tentativa e, numa subida, o run anterior, o seu modelo e a causa do seu veredicto | A cadeia completa de um passo que subiu |
| O modo (`observe` ou `enforce`) e, em `observe`, o que teria sido escolhido | Medir antes de ligar |

**O replay não volta a escolher.** Lê o modelo do evento gravado, como já faz para o canary. A
função de escolha é testada à parte, com vectores presos, como a do canary.

**De onde vêm os sinais, e porque isso importa.** As tools, as entradas e o contrato chegam no
pedido de quem submete. Usar factos do pedido para escolher o modelo parece contrariar a lição
«nada recalculado de entradas do chamador». Não contraria, por três razões que ficam escritas:
(i) a escolha não é um gate de segurança — todos os modelos da escada foram assinados pelo dono
e autorizados pela allowlist, e o pior que um chamador consegue é encaminhar o seu passo para
outro modelo autorizado, limite da mesma natureza do que o dono já aceitou na A3 («quem dá o
nome ao plano influencia a escolha»); (ii) o que encarece — subir — **não** vem do chamador: vem
do veredicto que o kernel calculou e selou, e da prova que o nó faz no seu log; (iii) o
orçamento por run e o tecto de tentativas continuam a limitar o gasto. O limite que fica: quem
submete passos pode escolher a classe em que caem. Enquanto a tabela de partida tiver uma só
linha, isso não muda nada.

## 5. O caminho mínimo, pela ordem em que dá valor

Cinco peças e uma opcional. A primeira não toca no nó e decide se as outras se fazem. Cada uma
das restantes entra desligada ou inerte: sem pacote na pasta, produção faz o que faz hoje.

### 5.1 «A4-medição» — há diferença entre os modelos?

- **O que é.** Uma experiência do banco de ensaio, no posto do dono, com modelo real. Três
  partes. (i) **Ler as 15 respostas do `k3` com factos ausentes**, do relatório que já existe:
  é reformatação ou é perda? (ii) **A bateria de hoje com os dois modelos na mesma corrida**,
  caso a caso, com os tokens e o tempo de cada pedido ao lado. (iii) **Uma versão nova da
  bateria com casos mais difíceis** — documento longo, vários documentos a cruzar, três ou mais
  tools em sequência, um resumo com muitos factos a preservar — corrida nos dois modelos.
- **Critério de aceitação.** Um relatório que responde, com contagens e intervalos de confiança,
  a três perguntas: há algum caso em que um modelo falha e o outro não? Um deles é mais lento ou
  gasta mais tokens, e quanto? Os factos ausentes do `k3` são perda verdadeira? Cada caso novo
  tem um controlo negativo: um provider falso que o falha tem de aparecer como falha. O
  relatório diz o que **não** distingue a 5%, como o da experiência dos separadores.
- **Desligado por omissão.** É do banco; o nó não muda. O modo real continua a ser só do dono.
- **Custo.** Cerca de 100 pedidos por modelo na bateria de hoje (o exame fez 96 e 99
  `[MEDIDO]` `[MEDIDO-A3]`), mais os casos novos: 300 a 400 pedidos ao todo `[HIPÓTESE]`, dentro
  do tecto de 1000 por dia do Kimi, sem custo em dólares a mais. Uma hora de máquina, mais ou
  menos.
- **Risco.** Casos «difíceis» mal escolhidos, em que os dois modelos passam ou os dois falham:
  não se aprende nada. Mitiga-se com casos em graus (curto, médio, longo) em vez de um só nível.
- **É a porta.** Se os modelos não se distinguirem, pára-se aqui, com um resultado: «com estes
  dois modelos, a cascata não tem o que dividir». Tudo o que se segue fica desenhado e por
  construir.

### 5.2 «A4-pacote» — o nó carrega um pacote de decisão assinado

- **O que é.** O formato da §3.2, a ferramenta de o assinar (o mesmo `aos-issuer` dos perfis), e
  o carregamento no arranque pela pasta que já existe, com as conferências da §3.2. Nesta peça o
  pacote **não decide nada**: é lido, conferido, selado e mostrado no banner.
- **Critério de aceitação.** Sem pacote na pasta, o binário comporta-se byte a byte como o da
  base (preso por digest dos mesmos goldens e pela contagem de leituras do Event Store, como na
  A3). Com assinatura inválida, sequência mais baixa do que a já vista, um degrau que aponta
  para um registo ausente, não qualificado ou fora da allowlist, uma classe ou uma causa fora do
  vocabulário: o nó não arranca, com causa própria para cada caso. O carregamento fica selado.
- **Desligado por omissão.** Sim: sem pacote, nada.
- **Risco.** Baixo. É o molde do registo de entrada, com menos campos.

### 5.3 «A4-escolha» — a função de escolha, a gravar e sem mandar

- **O que é.** A função pura que, dos sinais do run, do número da tentativa, do veredicto da
  tentativa anterior e do pacote, devolve um degrau. Com o pacote em `observe`: cada run grava
  no `run.model_pinned` o que a função **teria** escolhido, e é servido como hoje. Mais as
  séries no `/metrics`: passos por degrau, subidas por causa, e tokens e turnos por modelo.
- **Critério de aceitação.** A função está presa por vectores (mudar a função avermelha-os) e
  por um teste que proíbe relógio, sorteio e leitura de texto do modelo no seu caminho. Em
  `observe`, o modelo que serve cada run, os bytes enviados ao modelo e os eventos que já
  existiam são os mesmos que sem pacote; só o evento de fixação ganha campos. Numa série de
  pelo menos 40 planos em produção, lê-se do log a divisão que a regra daria, e bate com o que
  um script calcula à parte sobre os mesmos runs.
- **Desligado por omissão.** Sim, e mesmo ligado em `observe` não muda que modelo serve.
- **Risco.** O evento de fixação hoje só é gravado com um canary a servir; passa a ser gravado
  com um pacote carregado. Toca no caminho de todos os runs, como o AOS-520 tocou.
- **Com esta peça já se cumpre metade do critério da fase:** a divisão fica **medida**, e uma
  troca de pacote — assinar, copiar, recriar — muda a divisão medida, sem PR. Sem ainda mudar um
  único modelo.

### 5.4 «A4-subir» — a tentativa seguinte vai ao degrau de cima

- **O que é.** Com o pacote em `enforce`: a primeira tentativa de um passo corre no degrau de
  partida; se fechar com uma causa da lista do pacote, e o nó provar no seu log que ela não
  pediu tool nenhuma (a prova que já existe), a tentativa seguinte corre no degrau acima. O
  `aos-orq` não muda: continua a pedir «outra tentativa», e é o nó que decide em que modelo.
  Inclui o ADR novo que emenda o ADR-041 nas três regras da §4.1, e a regra da forma (a): um
  plano do canary não entra na cascata.
- **Critério de aceitação.** Com um provider falso no degrau 1 que nunca chama a tool e um bom
  no degrau 2: 100% dos passos com tools acabam cumpridos no degrau 2, com a cadeia gravada
  (tentativa 1, causa, tentativa 2, modelo). Com os dois bons: zero subidas. Com um falso no
  degrau 1 que chama a tool e depois falha: **zero subidas** e zero efeitos repetidos — só sobe
  o que a prova do ADR-039 cobre. Um plano atribuído ao candidato do canary corre inteiro no
  candidato. Um run retomado depois de o pacote mudar acaba no modelo em que foi fixado. O
  alerta do tecto de subidas dispara com o falso que nunca chama a tool.
- **Desligado por omissão.** Sim: `observe` até o dono assinar um pacote em `enforce`.
- **Risco.** Médio. Passa a haver planos com passos de dois modelos — o que a A3 evitou de
  propósito. E um degrau 1 que degrada passa todo o trabalho para o degrau 2 até alguém ler o
  alerta.

### 5.5 «A4-prova» — a medição do critério

- **O que é.** Não é código. Duas séries em produção, com dois pacotes diferentes, e nenhum PR
  entre elas. Por exemplo: o pacote A só sobe por «não chamou a tool»; o pacote B sobe também
  por resposta vazia, ou manda uma classe de passo directa ao degrau 2.
- **Critério de aceitação.** É o da fase, lido assim: (1) em cada série, a fracção de passos
  concluídos em cada degrau e a taxa de subidas estão registadas, tiradas do log; (2) entre as
  duas séries, a divisão mudou no sentido que a mudança do pacote previa; (3) zero PRs e zero
  imagens entre as duas, uma assinatura, e o digest do pacote gravado em cada run; (4) o «não
  cumprido» não piorou face à última série sem cascata.
- **Risco.** Com os modelos de hoje, as subidas são raras: 1% a 12% dos passos com tools. Em 40
  planos podem ser zero a cinco. Para a divisão «mudar» de forma legível pode ser precisa uma
  regra de partida (do tipo 3) em vez de só subidas — e essa só se deve assinar se a medição da
  5.1 a sustentar.

### 5.6 Opcional — «A4-partida»: regras de partida por classe de passo

- **O que é.** Preencher a tabela «classe de passo → degrau de partida» com mais do que uma
  linha. Não é código novo: a função da 5.3 já a lê. É uma decisão, com um pacote assinado.
- **Quando.** Só depois de a 5.1 mostrar uma classe de passo em que o degrau 1 falha, ou rende
  pior, de forma mensurável.

### O que fica de fora, de propósito

| O quê | Para onde vai | Porquê |
|---|---|---|
| Três ou mais degraus | depois | O formato aceita uma lista; a prova faz-se com dois. Um terceiro degrau pede um terceiro modelo que valha a pena |
| Um campo de dificuldade no plano, escrito pelo planeador ou por uma pessoa | depois, se alguma vez | Muda o schema do plano e o prompt do planeador, e traz um juízo de um modelo para dentro da escolha. Sem medição que o peça |
| Aprender regras do histórico | depois | Não há volume. Quando houver, o resultado entra como um pacote assinado, nunca a quente |
| Armar a escada de tiers e o scoring do gateway no nó (AOS-421) | fora desta fase | Essa escada escolhe por **pedido**, pode trocar de modelo a meio de um run, e tira a «capacidade exigida» do modelo pedido. Serve para escolher região e conta; não serve para escolher modelo por passo (decisão D10) |
| O tecto de subidas a cortar, e não só a avisar | depois do AOS-524 | Precisa de um contador partilhado, que hoje não é seguro com mais de uma réplica |
| Subir a meio de um run, ou depois de uma tool ter corrido | fora | Repetia efeitos, ou mudava de modelo com estado opaco pendurado |
| A cascata no planeador do `aos-orq` | fora | Outro binário, outro gateway, fora da governação da rota |
| Descer de degrau por falta de orçamento | fora | É a «descida de tier» do gateway. Fica desligada, como está |

## 6. O que as fases anteriores ensinaram e a A4 tem de respeitar

1. **Veredicto calculado, com controlos negativos.** O banco deu verde falso duas vezes na A2, e
   na A3 o arnês chegou a dar `qualificado` a um modelo que falhava todos os passos sem tools.
   *Para a A4:* a medição da 5.1 e a prova da 5.5 têm, cada uma, um controlo que as avermelha. A
   «divisão medida» confere-se por dois caminhos independentes (as séries do nó e um script
   sobre o log). E a função de escolha é revista por quem não a escreveu.
2. **`cumprida` não prova leitura — e «cumprido» não prova qualidade.** *Para a A4:* subir por
   veredicto negativo apanha o modelo que não chama a tool e o que responde vazio. **Não** apanha
   o que responde mal. O documento da prova tem de o dizer à cabeça, e a divisão não pode ser
   lida como «o degrau 1 chega para X% dos passos»: só como «X% dos passos não tiveram veredicto
   negativo no degrau 1».
3. **Mede-se por rota, atrás do proxy fixado.** Um modelo por outra porta é outra rota. *Para a
   A4:* cada degrau aponta para um **registo de entrada** (modelo, fornecedor, proxy, perfil),
   pelo digest — não para um nome. A medição da 5.1 corre atrás da imagem do proxy que serve
   produção.
4. **Nada recalculado de entradas do chamador.** *Para a A4:* o que faz subir é o veredicto
   selado e a prova do nó; nunca um campo do pedido. O que o chamador influencia é só a classe
   de partida, entre modelos que o dono assinou (§4.4). Nenhum campo novo no `POST /runs`.
5. **Inércia medida por digest e por custo.** Sem pacote, e com pacote em `observe`, o nó é o de
   antes: os mesmos bytes para o modelo, os mesmos eventos, as mesmas leituras do Event Store.
   Na A3 isto prendeu-se por digest contra o binário anterior e por contagem de leituras; aqui
   também.
6. **O que não está numa lista fechada de exclusões conta.** A revisão do disjuntor encontrou
   falhas que o nó não sabia classificar e que não contavam. *Para a A4:* é ao contrário, e pela
   mesma razão. **Só sobe o que está na lista fechada de causas do pacote.** Uma causa que o nó
   não conhece não sobe, e conta numa série própria, para se ver. Subir por engano custa
   dinheiro e pode repetir trabalho; não subir deixa o passo onde hoje fica.
7. **Um defeito do substrato em multi-réplica (AOS-524).** O livro do canary não é seguro com
   duas réplicas sobre JetStream. *Para a A4:* a escolha não usa estado partilhado (§4.3). O que
   precisaria dele — um tecto que corta — fica para depois. E a prova da nova tentativa, que lê
   o log do run anterior, tem o smoke sobre JetStream por fazer desde a A1: a A4 herda-o.
8. **Uma amostra pequena não fecha nada.** O `k3` tem 0 falhas em 14 runs de produção: com 95%
   de confiança, isso só diz que a taxa verdadeira está abaixo de cerca de 20%. *Para a A4:*
   nenhuma regra de partida se assina com menos de 40 amostras por classe e por modelo, e o
   relatório traz o intervalo.
9. **O que se liga em produção mede-se com uma série antes e outra depois**, e liga-se em
   `observe` primeiro. Foi assim em todas as fases.
10. **Repetir até passar é escolher o resultado.** *Para a A4:* os casos difíceis da 5.1
    escrevem-se **antes** de se correrem, e correm-se as vezes declaradas à partida.

## 7. Decisões para o dono

Dez decisões. Em cada uma: as opções, a recomendação e a razão, e o que acontece se ficar por
tomar. Os números que aparecem como limiares são **propostas para discutir**, não medições.
A D1 vem primeiro porque pode tornar as outras desnecessárias por agora.

### D1 — Medir primeiro, ou construir já?

| Opção | O que é | Custo | O que se fica a saber |
|---|---|---|---|
| (a) Medir primeiro | A peça 5.1: ler as 15 respostas do `k3`, correr os dois modelos lado a lado, juntar casos difíceis | Um PR pequeno no banco, cerca de uma hora de máquina, 300 a 400 pedidos | Se há alguma coisa para a cascata dividir |
| (b) Construir já, medir em produção | As peças 5.2 a 5.4, e ver a divisão em `observe` | Seis a nove PR | O mesmo, mais tarde e mais caro; e só para os planos que correm em produção, que são sempre o mesmo |
| (c) Adiar a A4 e avançar para a A5 | A fase fica desenhada | nenhum | Nada |

**Recomendação: (a).** A razão é a da §0: hoje não há evidência de que os dois modelos difiram
no que conta. Uma hora de medição decide se seis a nove PR valem a pena. E a medição tem valor
mesmo que a resposta seja «não»: diz se o `k3` serve para titular, que é uma decisão que a A3
deixou em aberto.

*Se não decidir:* nada se constrói. A fase fica «por começar», com este desenho.

### D2 — Como se estima o que um passo exige

| Opção | Determinista? | Precisa de quê |
|---|---|---|
| (1) Quem escreve o plano declara | Sim | Um campo novo no plano; contraria «quem submete não escolhe» |
| (2) O planeador declara | Não na origem | Campo novo, prompt novo do planeador, e um modelo a julgar dificuldade |
| (3) Regra sobre sinais do passo | Sim | Uma medição que mostre que o sinal prediz alguma coisa — não há |
| (4) Tentar no degrau de baixo e subir | Sim | Nada de novo: usa a nova tentativa e o veredicto |
| (5) Aprender do histórico, offline | A regra final, sim | Volume de runs que não existe |

**Recomendação: (4), com a tabela de (3) presente no pacote e com uma só linha.** As razões
estão na §3.1. **(1) e (2) ficam recusadas por agora**; se voltarem, voltam só como «pode subir,
nunca descer».

*Se não decidir:* a peça 5.3 não tem função para escrever.

### D3 — «Escolher antes», «tentar e subir», ou as duas

| Opção | O que dá | O que custa |
|---|---|---|
| (a) Só escolher antes | Nenhuma tentativa desperdiçada | Exige saber de antemão que passos são difíceis. Não se sabe |
| (b) Só tentar e subir | Funciona sem saber nada sobre os passos | Uma tentativa falhada antes de cada subida; não apanha respostas más que pareçam boas |
| (c) As duas: o pacote tem as duas tabelas | A (b) hoje, a (a) quando houver medição | O formato fica um pouco maior |

**Recomendação: (c)**, com a tabela de partida a começar com uma linha. É a (b) na prática e a
(a) por assinatura, sem PR, no dia em que uma medição a justificar.

*Se não decidir:* fica (b), que é o que a função faz com a tabela de partida vazia.

### D4 — Quantos modelos, e quais

| Pergunta | Opções | Recomendação |
|---|---|---|
| Quantos degraus | dois; três | **Dois.** O formato aceita mais; a prova não precisa |
| O degrau 1 | o `kimi-for-coding` (o titular) | **Esse.** É o que serve hoje |
| O degrau 2 | (a) o `k3`; (b) o Claude; (c) um modelo pago ao pedido com endpoint na UE; (d) nenhum, até haver um que se distinga | **Decide-se com a medição da D1.** Se o `k3` não perder factos e falhar menos, é ele. Se não, **(d)** |

Três avisos sobre o degrau 2:

- **O `k3` só pode ser degrau depois de deixar de ser candidato** (§4.2). É uma decisão do dono
  que a A3 deixou fora do relógio: «passar a titular, ou sair».
- **O Claude não pode servir produção**: não tem região na UE por esta via, e produção está
  selada para `eu-west`. Só entra por um parceiro com endpoint na UE, com conta e aprovação — a
  decisão D9 da A3, que continua por tomar. Seria o primeiro modelo **de facto mais caro**
  (pago ao token), e o primeiro com que a cascata pouparia dinheiro que se vê numa factura.
- **«O mais forte» não está medido.** O `k3` chama tools com mais certeza e pode resumir pior.
  Pode acontecer que o degrau certo dependa do tipo de passo: um para ler, outro para resumir.
  Se a medição mostrar isso, a cascata deixa de ser uma escada e passa a ser uma tabela — o
  formato do pacote já o permite, pela tabela de partida.

*Se não decidir:* o pacote não tem o que pôr no degrau 2, e a fase pára na peça 5.3.

### D5 — O que quer dizer «barato» e «caro»

Com o Kimi num plano de preço fixo, o custo em dólares de um passo é o mesmo nos dois modelos.

| Opção | O que mede | Problema |
|---|---|---|
| (a) Dólares, pela tabela de preços | O custo por turno que o nó já grava | Num plano fixo é zero, ou uma convenção. Não distingue |
| (b) Uma ordem declarada pelo dono | O pacote diz «o degrau 2 é mais caro do que o 1», sem dizer quanto | Não é uma medida; é uma decisão. Mas é honesta |
| (c) Tokens de saída e turnos por passo concluído | O que o modelo gasta, já gravado em cada turno | Mede esforço, não dinheiro |
| (d) Tempo por passo | O que o utilizador espera | O nó não grava o tempo de um turno; mede-se pela duração dos planos, por fora |
| (e) Quota do plano | Quanto do limite do fornecedor se consome | Não sei o que o plano do Kimi conta nem se os dois modelos contam igual |

**Recomendação: (b) para ordenar, (c) e (d) para medir, (a) só quando houver um modelo pago ao
pedido.** A ordem dos degraus é uma decisão assinada; o que cada degrau gasta mede-se em tokens
e em tempo, por passo concluído — incluindo as tentativas falhadas que o antecederam. O dono
indica o que sabe sobre (e): se o plano do Kimi tem um limite que o `k3` gasta mais depressa,
esse é o «caro» verdadeiro.

*Se não decidir:* a divisão mede-se, mas ninguém pode dizer se uma divisão é melhor do que
outra.

### D6 — Onde vive o pacote de decisão, e quem o assina

| Pergunta | Opções | Recomendação |
|---|---|---|
| Onde vive | variáveis de ambiente; ficheiro assinado na pasta dos perfis; a tabela de pesos embebida; o bundle do PDP; o Registry (§3.2) | **Ficheiro assinado na pasta dos perfis.** O mesmo molde, a mesma conferência no arranque |
| Quem assina | o dono | **O dono**, fora do nó e fora do banco, com a ferramenta que já usa |
| Com que chave | (a) a dos perfis de rota; (b) uma só para pacotes | **(a).** O pacote só escolhe entre perfis que essa chave já assinou; uma segunda chave não separava poderes que hoje são da mesma pessoa. Fica dito como limite |
| Anti-recuo | (a) número de sequência, e o nó recusa descer; (b) nada, como nos perfis hoje | **(a).** Fecha, para o pacote, o resíduo que os perfis ainda têm |
| Validade no tempo | (a) o pacote expira; (b) não expira | **(b)** agora. Um pacote que expira sozinho pára a cascata sem ninguém decidir |

*Se não decidir:* fica em variáveis de ambiente ou em código, e a metade «ajustada sem deploy»
ou não se cumpre ou cumpre-se sem assinatura.

### D7 — O que fazer ao canary e ao «um plano, um modelo»

| Opção | O que é | Consequência |
|---|---|---|
| (a) A cascata só entre modelos já promovidos; um plano do canary fica fora dela | §4.2 | O canary mede como hoje. Passa a haver um «titular por degrau». ADR novo a emendar o ADR-041 |
| (b) O canary passa a contar por passo | A unidade deixa de ser o plano | Refaz o livro do canary e o disjuntor, um dia depois de provados |
| (c) Cascata e canary nunca ao mesmo tempo | Um interruptor exclui o outro | Simples; a cascata fica desligada sempre que houver um candidato |

**Recomendação: (a).** E, com ela, três coisas que ficam escritas no ADR novo: a nova tentativa
pode ir a outro modelo, **só** pela regra de subida de um pacote em `enforce`; a atribuição de
uma falha passa a fazer-se por passo e não por plano, fora do canary; e um passo recebe, por
desenho, a saída de um passo servido por outro modelo. Este último ponto pesa menos do que
pesava: desde a fase A0.5, a saída de um passo de leitura é o resultado da tool, byte a byte, e
não o texto do modelo.

*Se não decidir:* a peça 5.4 contradiz um ADR em vigor, e não se pode fundir.

### D8 — Como se mede a divisão: qual é o número do critério

O critério da fase diz «a divisão entre modelos baratos e caros é medida e ajustada sem deploy».
Falta dizer que número é «a divisão».

| Número | O que diz | Recomendação |
|---|---|---|
| **Fracção de passos concluídos em cada degrau** | A divisão propriamente dita | **O número principal** |
| **Taxa de subidas**, por causa | Quanto custa a cascata em tentativas perdidas | Ao lado, sempre |
| Tokens de saída e duração por passo concluído, por degrau | O que cada degrau gasta | Ao lado, sempre |
| «Não cumprido» por plano | Se a cascata piorou alguma coisa | **Salvaguarda:** não pode piorar |
| Dólares por plano | A poupança | Só com um modelo pago ao pedido |

**Recomendação para «ajustada sem deploy»:** duas séries de pelo menos 40 planos cada, com dois
pacotes assinados diferentes; a divisão muda no sentido previsto; zero PRs e zero imagens entre
as duas; o digest do pacote em cada run. É a peça 5.5.

Uma honestidade: com só a regra de subida, e 1% a 12% de primeiras falhas, a diferença entre
duas séries de 40 planos pode não se ver. Há duas saídas — séries maiores (120 planos), ou uma
regra de partida que mande uma classe inteira para o degrau 2. A segunda mostra melhor o
mecanismo, mas é uma regra assinada só para a prova. O dono escolhe.

*Se não decidir:* a prova não tem critério, e a fase não se pode dar por provada.

### D9 — Os resíduos da A3 que tocam na A4

| Resíduo da A3 | Bloqueia a A4? | Opções | Recomendação |
|---|---|---|---|
| **Mais de uma réplica sobre JetStream** (AOS-524) | Não, se a escolha não usar estado partilhado | (a) desenhar a escolha sem contador; (b) esperar pelo AOS-524 | **(a).** Fica de fora só o tecto que corta. Produção corre um nó sobre ficheiro |
| **O disjuntor nunca correu em `enforce`** | Não: o disjuntor é do candidato, e os degraus não são candidatos | (a) seguir; (b) ligar `enforce` primeiro | **(a)**, com o alerta do tecto de subidas como a defesa da cascata. Ligar `enforce` continua a ser uma decisão da A3 |
| **Os factos ausentes do `k3`** (27%) | **Sim**, para o `k3` ser degrau 2 | (a) ler as 15 respostas na medição da D1; (b) aceitar | **(a)** |
| **As primeiras falhas do titular subiram a 2026-10-12** (12% e 2 em 5), sem causa procurada | Não bloqueia, mas é a linha de base da cascata | (a) procurar a causa antes; (b) medir em cima | **(a).** Se a causa for passageira, a cascata não tem o que recuperar; se for uma regressão, corrige-se na origem e não com outro modelo |
| **O `k3` ainda é candidato** | **Sim**, para a forma (a) da D7 | o dono promove-o, ou tira-o | Decide-se com a medição da D1 |
| **Registos sem validade nem anti-recuo** | Não | — | O pacote nasce com sequência (D6); os perfis ficam como estão |
| **Smokes sobre JetStream por fazer** (AOS-519, AOS-520, AOS-521, e o da nova tentativa) | Não, com um nó sobre ficheiro | — | A A4 herda-os e di-lo |

*Se não decidir:* as duas linhas com «Sim» param a fase na peça 5.3.

### D10 — A escada do gateway (ADR-021, AOS-421): usa-se ou não?

Já existe no gateway uma escada de tiers com scoring, composta e testada, que o nó não liga. É
tentador dizer que a A4 é «ligar isso». Não é, e convém o dono decidi-lo de olhos abertos.

| Opção | O que é | Problema |
|---|---|---|
| (a) A A4 escolhe no nó, por run, e não liga a escada do gateway | Este desenho | Ficam duas noções de «degrau» no repositório: a do gateway (por pedido) e a do pacote (por run) |
| (b) A A4 liga a escada do gateway (faz o AOS-421) | O scoring escolhe por pedido | Escolhe por **turno** e pode trocar de modelo a meio de um run — o que a A3 proibiu e o estado opaco não suporta. Tira a capacidade exigida do modelo pedido, não do passo. A tabela de pesos está embebida: mudar é deploy |
| (c) As duas, em níveis diferentes | O nó escolhe o modelo por run; o gateway escolhe a região e a conta por pedido | É o destino razoável. Mas o segundo nível só interessa com mais de uma conta ou região, que o nó não tem |

**Recomendação: (a) agora, com (c) como destino.** O AOS-421 fica como está, independente desta
fase. O ADR-021 não é contrariado: as suas cinco regras aplicam-se ao pacote (§2). O ADR novo da
A4 diz em que nível vive cada escolha. A «descida de tier» por orçamento continua desligada.

*Se não decidir:* quem implementar vai encontrar as duas escadas e ter de escolher sozinho.

## 8. Proposta de tickets

Sem número. A numeração faz-se depois das decisões, com `scripts/ci/sessoes.py reservar`.

| Rótulo | Título | Uma linha | Depende de |
|---|---|---|---|
| «A4-medição» | O banco de ensaio compara dois modelos caso a caso, com casos em graus de dificuldade | Corrida emparelhada, tokens e tempo por pedido, bateria nova com casos difíceis e os seus controlos negativos; leitura das 15 respostas do `k3`; relatório que diz o que distingue e o que não distingue | D1 |
| «A4-pacote» | O nó carrega um pacote de decisão assinado e confere-o no arranque | Formato fechado, assinatura, sequência com anti-recuo, cada degrau preso a um registo de entrada qualificado; inerte sem pacote | D6; a medição a dizer «sim» |
| «A4-escolha» | A função de escolha do degrau, a gravar em `observe` | Função pura presa por vectores; campos novos no `run.model_pinned`; séries de passos por degrau, subidas por causa, tokens e turnos por modelo | D2, D3, D8; «A4-pacote» |
| «A4-subir» | A tentativa seguinte de um passo corre no degrau de cima | `enforce`; só as causas da lista fechada e só com a prova do ADR-039; mais de dois modelos no despachante; um plano do canary fica fora; alerta do tecto de subidas; ADR novo a emendar o ADR-041 | D4, D7, D9, D10; «A4-escolha» |
| «A4-prova» | A divisão é medida e muda com uma assinatura | Duas séries, dois pacotes, zero PRs; registada no acompanhamento | D5, D8; todos os anteriores |
| «A4-partida» (opcional) | Regras de partida por classe de passo | Uma decisão e um pacote assinado, não código; só com medição que a sustente | «A4-medição», «A4-escolha» |

**Quantos PR, com honestidade.**

- **Só a medição** (se a D1 ficar em (a) e a resposta for «não»): **um ou dois PR**, no banco.
  Nada no nó.
- **A fase inteira:** cinco tickets, **sete a dez PR**. Um para a medição; um para o pacote;
  dois para a escolha (a função com o evento, e as séries); dois para subir (o nó, e o ADR com
  os alertas e o runbook); nenhum de código para a prova; mais um a três de correcções depois
  das revisões adversariais — na A3 cada ticket que tocou no caminho dos runs teve pelo menos
  uma.
- **O maior e o mais arriscado é o «A4-subir»**: é o único que muda que modelo serve um run.
  Os outros são pequenos ou médios.

**Ordem.** «A4-medição» primeiro e sozinho. Só com o resultado lido se decide o resto. Depois
«A4-pacote», «A4-escolha» (com uma série em `observe` lida antes de continuar), «A4-subir»,
«A4-prova».

## 9. O que este desenho não sabe

Perguntas que só uma medição responde.

- **Se há algum passo em que um modelo falha e o outro não.** É a pergunta da fase. A bateria de
  hoje não a responde: os dois modelos passam tudo.
- **O que falta nas 15 respostas do `k3`.** A verificação procura o texto exacto dos nomes e dos
  números. Um número escrito com espaço, ou um nome sem hífen, conta como ausente.
- **Qual dos dois modelos é mais lento e qual gasta mais tokens.** O exame do `k3` demorou 14
  minutos para 96 pedidos e o do titular 14 min 41 s para 99: parecidos, mas são tempos de
  corrida, não de pedido, e o banco não regista o tempo de cada pedido.
- **O que o plano de preço fixo do Kimi conta**, e se os dois modelos contam igual. Só o dono ou
  a página do fornecedor o dizem.
- **Porque é que as primeiras falhas do titular subiram a 2026-10-12** (de 1 em 82 para 4 em 33
  e 2 em 5). Se foi o `drop_params: false`, a recriação do proxy, a hora do dia ou o acaso.
- **Se subir de modelo recupera melhor do que repetir no mesmo.** Com o aviso ligado, 3 de 22
  segundas tentativas voltaram a falhar no mesmo modelo (14%) `[MEDIDO]`. Não há um único caso
  medido de «falhou no titular, a seguir foi ao `k3`».
- **Se o aviso da nova tentativa funciona noutro modelo.** O texto foi afinado para o Kimi. Uma
  tentativa que sobe leva o mesmo aviso a outro modelo.
- **Se uma resposta vazia se recupera subindo.** A repetição do passo vazio está em `observe`
  em produção, e não houve nenhuma resposta vazia desde que a ficha existe.
- **Se há tráfego para a divisão querer dizer alguma coisa.** As séries são lançadas à mão, com
  o mesmo objectivo de dois passos. A divisão de uma série assim diz pouco sobre planos reais.
- **Que sinais do passo predizem dificuldade.** Nenhum está medido. A tabela de partida fica com
  uma linha até haver um.
- **Quanto pesa gravar o evento de fixação em todos os runs.** Hoje só se grava com um canary a
  servir. A A3 mediu a espera pelo livro do canary; a escolha da cascata não usa o livro, mas a
  escrita a mais por run não foi medida.
- **Se três aliases cabem na allowlist.** O board da UE tem dois nomes autorizados (`gpt-4o-mini`
  e `gpt-4o`). Titular, degrau 2 e um candidato em canary ao mesmo tempo são três: pede uma
  allowlist nova, assinada.

## 10. O que esta nota não verificou

- O servidor de produção: nada foi lido lá. O que se diz do `.env`, da tabela de preços em
  vigor e da pasta dos perfis vem do repositório e do acompanhamento.
- As medições da A3 marcadas `[MEDIDO-A3]` foram lidas de um ramo ainda por fundir
  (`docs/AOS-523-prova-da-a3`, commit `c3e03183`). Se esse ramo mudar antes de entrar, os
  números deste desenho têm de ser conferidos outra vez.
- O estado da fase A3. O pedido deste desenho dá-a como fechada a 2026-10-12; no repositório
  que li, incluindo esse ramo, ainda está «provada em produção, por fechar pelo dono». Nada
  neste desenho depende da diferença: os resíduos são os mesmos, e o `k3` continua candidato
  até o dono o promover ou tirar.
- O caminho completo de `escolherModeloDoRun` até ao adaptador, para lá do que está citado. A
  peça 5.3 começa por aí.
- O corpo do `router` e do `scoring` do gateway (cerca de 1500 linhas): lidos pelo cabeçalho,
  pelo classificador e pelo ADR-021, não linha a linha.
- Que a prova da nova tentativa (ADR-039) cobre a tentativa por resposta vazia da mesma maneira
  que a tentativa por tool não chamada: li o primeiro caso, não o segundo.
- O prompt do planeador do `aos-orq`: não confirmei se ele recebe ou produz algum sinal de
  dificuldade além da estimativa de orçamento e da classe de risco.
- As referências ficheiro:linha foram conferidas por amostragem, não todas.
