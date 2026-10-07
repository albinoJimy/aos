# Acompanhamento — arquitectura-alvo da fronteira runtime↔modelo

> Documento vivo. É a fonte única do estado da arquitectura-alvo aceite pelo dono a 2026-10-04.
> **Regra de actualização:** o PR que muda o estado de um ticket desta lista, que mede um critério
> de prova ou que regista uma decisão do dono actualiza este ficheiro no mesmo commit. Um estado
> aqui que não bata com o ticket na EPIC é um defeito do PR.

Última actualização: 2026-10-07 (fase A1, v0.1.51 em produção: aviso na nova tentativa e projecção 1.2.0 ligados (AOS-506); duas séries medidas, `v0151a` e `v0151b`; o critério «não cumprido abaixo de 2%» foi cumprido pela primeira vez na série `v0151b` — 1 em 62 planos, 1,6% —, com a ressalva de que a única causa que resta, `empty_output` num nó sem tools, não é coberta pela recuperação. As duas verificações que faltavam correram no mesmo dia: o plano de três passos (`v0151t`), em que o Reference Monitor negou por taint a tool do nó com `consumes` e não houve nova tentativa; e o AOS-505 em `observe` (série `v0151g`, 20 planos, 60 turnos com o modelo servido igual ao esperado). **Fase A1 fechada a 2026-10-07, por decisão do dono, com os resíduos nomeados. Fase A2 aberta no mesmo dia: desenho feito, decisões D1 a D3 tomadas, D4 e D5 por tomar, AOS-507 a AOS-511 abertos, e o resto planeado, por numerar**).

## 1. Objectivo e promessa

**Objectivo do dono:** excelência no sentido de «qualquer modelo novo entra sem trabalho».

**Promessa que se consegue provar:** um modelo de uma classe de wire qualificada, com fornecedor e
região já aprovados, entra sem código nem configuração manual além de uma entrada de catálogo
assinada. Relaxar garantias exige uma segunda assinatura. Uma classe de wire nova é engenharia.

Origem: `analise-fronteira-runtime-modelo-2026-10-04.md` (cinco perspectivas e duas avaliações
adversariais) e a validação adversarial do desenho de IA de governação, da mesma data.

## 2. Estado por fase

Estados possíveis: **por começar**, **em curso**, **em produção por verificar**, **provada**,
**fechada**. Uma fase só passa a **provada** quando o critério de prova está medido e registado
na §5, e só passa a **fechada** por decisão do dono, registada na §4.

| Fase | Conteúdo | Critério de prova | Depende de | Estado |
|---|---|---|---|---|
| **A0** | O desfecho de um run é um veredicto do kernel sobre um contrato de conclusão; o `aos-orq` trata «não cumprido» como nó falhado | Zero verdes falsos em pelo menos 150 runs com tools na oferta | — | **em produção por verificar** (v0.1.46, imposição ligada a 2026-10-05; falta o critério de prova. A contagem dos 150 runs sem verde falso recomeça desde a projecção 1.1.0: as 3 recusas do próprio objectivo da série `v0150r`, com a 1.0.0, foram verdes sem cumprir) |
| **A0.5 — Saída por referência** | A saída de um nó de passagem directa é o resultado da tool, e não o texto do modelo: o plano declara a origem (`outputs[].from_tool`), o kernel designa e sela qual chamada é a origem, e o `aos-orq` publica e entrega esses bytes, conferidos contra o digest selado. O texto final continua capturado e deixa de ser a saída | Numa série de pelo menos 20 planos, a saída entregue ao nó seguinte é byte a byte o resultado selado da tool e nenhum facto do documento se perde | A0 | **provada** (2026-10-06, v0.1.49 com a entrega ligada: série de 21 planos, 18 entregas por referência, todas byte a byte iguais ao resultado selado; os outros 3 falharam antes da entrega por o modelo não chamar a tool) |
| **A1 — Recuperação** | Nova tentativa ao nível do plano: um nó que terminou sem chamar a tool volta a ser submetido, até duas vezes a mais, em qualquer nó com tools, e o nó `aos` só aceita a tentativa depois de provar no seu log que a anterior não pediu tool nenhuma. Projecção nativa 1.1.0 (fim de segmento inforjável e texto do protocolo reescrito), desligada por omissão e medida antes de ligar. Projecção nativa 1.2.0 e aviso constante na nova tentativa, para o modelo que escreve a tool call como texto (AOS-506), desligados por omissão. Rota sob governação (nome real do modelo, proxy sem descartar parâmetros, modelo servido comparado por turno) | «Não cumprido» abaixo de 2% numa série de pelo menos 40 planos com a recuperação ligada; uma troca de modelo por baixo é detectada | A0 | **fechada** (2026-10-07, por decisão do dono, com os resíduos nomeados). Provada em produção na v0.1.51. «Não cumprido» abaixo de 2%: cumprido na série `v0151b` (1 em 62, 1,6%) e na série `v0151g` (0 em 20); antes, 2,5%, 3,3% e 3,4%. Acumulado com a 1.2.0: 82 planos, 1 falha à primeira tentativa (1,2%), 1 `empty_output`. Troca de modelo por baixo: detectável na forma estreita (uma troca de configuração no proxy), e a comparação por turno corre em produção em `observe` — 60 turnos em 60 com o modelo servido igual ao esperado. Resíduos nomeados (§7): `empty_output` num nó sem tools (passa à A2); um nó com tools e `consumes` é negado pelo gate de taint enquanto a capacidade da tool estiver armada, e a recuperação não o alcança; os separadores `<kind>` como risco aberto; AOS-505 por impor (`enforce`) |
| **A2** | Estado opaco do provider por turno (raciocínio, assinaturas, identificadores), com sondas de protocolo deterministas | Duas famílias de modelos completam runs com tools | A0; escolha da segunda família | **em curso** (2026-10-07: desenho feito, `desenho-a2-estado-opaco-2026-10-07.md`; decisões D1 a D5 tomadas; AOS-507 a AOS-511 abertos; o banco de ensaio, o estado opaco e a segunda família estão planeados, por numerar, à espera das chaves que o dono fornece — §3). Herda da A1 o `empty_output` (§7): a única causa de «não cumprido» que resta nas séries da v0.1.51 |
| **A3** | Entrada automática: arnês de qualificação, perfil do modelo como artefacto do registo, mais de um modelo por nó, canary, disjuntor | O terceiro modelo entra com zero PRs e uma assinatura em menos de uma hora; um modelo mau é recusado sozinho | A1, A2 | por começar. Proposta por decidir (§4): antecipar daqui um banco de ensaio de qualificação por modelo |
| **A4** | Cascata: estimar a capacidade que o passo exige e eleger o modelo por roteamento determinista, com limiares num `decision pack` | A divisão entre modelos baratos e caros é medida e ajustada sem deploy | A3 | por começar |
| **A5** | Multimodal de entrada (media por referência) | Um modelo recebe imagem ou áudio num run, com replay | A2 | por começar |
| **A6** | Multimodal de geração, como tool | Uma modalidade gerada com proveniência e custo contabilizado | A5; A0.5 | por começar |

A **saída por referência** deixou de ser transversal: é a fase A0.5, antecipada pelo dono a
2026-10-05. É pré-requisito de A6 e fecha o resíduo «evidência não é fidelidade» para os nós que
passam o resultado sem o transformar. Não o fecha para os nós que transformam (§7).

## 3. Tickets por fase

### A0

| Ticket | Epic | Título curto | Depende de | Estado |
|---|---|---|---|---|
| AOS-491 | EPIC-06 | O motivo de paragem chega ao runtime, à captura e ao registo do turno | — | em produção (v0.1.46); motivos `stop` e `tool_calls` observados |
| AOS-492 | EPIC-02 | A regra de terminação vive num só sítio, partilhado por loop e replay | — | feito |
| AOS-493 | EPIC-02 | O desfecho de um run é um veredicto do kernel sobre um contrato de conclusão | AOS-491, AOS-492 | implementado (ADR-037) e revisto (2026-10-05, sem bloqueantes; achados corrigidos no ticket), nó em observação por omissão; por verificar em produção |
| AOS-494 | EPIC-19 | O nó aceita o contrato no `POST /runs` e devolve o desfecho no `GET /runs` | AOS-493 | implementado e revisto (2026-10-05, sem bloqueantes; achados corrigidos no ticket); smoke sobre JetStream por correr |
| AOS-495 | EPIC-19 | O `aos-orq` declara o contrato por nó e não publica saídas sem evidência | AOS-494 | implementado e revisto (2026-10-05, sem bloqueantes; achados corrigidos no ticket, com a elegibilidade alargada); por verificar em produção |

Ordem de entrega: AOS-491 e AOS-492 (sem mudança de comportamento), depois AOS-493 em modo de
observação, depois AOS-494 e AOS-495. O nó sai antes do `aos-orq`. A imposição liga-se depois de o
modo de observação dar a taxa de vermelhos falsos.

**A elegibilidade do contrato foi alargada pela revisão adversarial do AOS-495 (2026-10-05).** A
decisão de 2026-10-04 dava contrato a um nó não-verificador, com tools atribuídas e com uma saída
de forma aberta declarada. O planeador só declara `outputs` quando outro nó os consome: o plano
de um só nó, o último nó e os nós de escrita ficavam sem contrato, e o verde falso continuava
aberto para eles. Passa a levar contrato todo o nó não-verificador com tools atribuídas, com ou
sem `outputs`. Com o nó em `observe` isto não muda nenhum desfecho; aumenta o que se mede. O dono
valida a classe alargada antes de ligar `enforce` (§4).

### A0.5 — Saída por referência

| Ticket | Epic | Título curto | Depende de | Estado |
|---|---|---|---|---|
| AOS-497 | EPIC-02 | O kernel designa e sela a origem da saída de um run: o resultado da chamada efectiva da tool declarada. Escreve o ADR-038 e as emendas ao ADR-037; as do ADR-027 e do ADR-022 ficam para os tickets que mudam o que eles descrevem (AOS-501 e AOS-500) | AOS-493 | implementado (ADR-038) e revisto (2026-10-05, sem bloqueantes; achados corrigidos no ticket: a regra conta as chamadas pedidas, o run com entradas tem o estado `inapplicable`, a forma do nome valida-se no arranque, a fonte dos bytes é o step-ledger); invisível até haver chamador; por verificar em produção |
| AOS-498 | EPIC-19 | O nó aceita `output_from_tool` no `POST /runs`, devolve a origem e a saída no `GET /runs/{id}` e anuncia-o no `GET /tools` | AOS-497, AOS-494; relaciona AOS-496 | implementado e revisto (2026-10-05, sem bloqueantes), por verificar em produção. Os bytes lêem-se do step-ledger nos dois ramos e conferem-se contra o digest selado; a falta deles manifesta-se conforme o vínculo e nunca muda o desfecho de um run «só medição». Da revisão: o que não se lê por causa do log é definitivo (deixou de dar 503 para sempre); a leitura exige o opener e o leitor; `output` não sai de um run que não concluiu, agora com teste; o envelope real da sandbox de ponta a ponta. Emenda o ADR-037 §2.8. Por fazer: smoke sobre JetStream e um run designado pelo binário do driver |
| AOS-499 | EPIC-19 | O `aos-orq` mede a saída por referência sem mudar a entrega | AOS-498 | implementado e revisto (2026-10-05, sem bloqueantes). `AOS_ORQ_SAIDA_POR_REFERENCIA=observe`: os candidatos por estrutura declaram a origem com o vínculo `measure`; a entrega, os estados dos nós, o código de saída e os eventos do plano são os de `off`. Da revisão: a medição comparava o texto final com o envelope da sandbox e não distinguia uma transcrição de um resumo; passou a desembrulhar o conteúdo e a publicar classes (forma, relação com o texto final, números). **Por fazer: a série de pelo menos 20 planos em `observe` em produção, e ler as métricas aqui** |
| AOS-500 | EPIC-19 | O plano declara a origem de uma saída: `outputs[].from_tool`, schema 1.3.0 | — | implementado e revisto (2026-10-05); **invisível até ao AOS-501**. O campo é opcional e entra no `contract_digest` só quando presente: um plano sem ele tem o documento, os digests, os eventos e o cartão de antes. Seis regras no validador, com código próprio, e o piso de versão 1.3.0. O prompt do planeador não muda (a excepção no teste que deriva o schema do prompt contém só `from_tool`, sai no AOS-501, e um teste liga-a ao golden-set). Até ao AOS-501 o `aos-orq` trata a linha 1.3.0 — o campo, ou só o carimbo — como o binário anterior: tentativa recusada no laço do planeador com `--goal`; `origem_sem_entrega`, saída 10, em todo o documento lido do disco. Emenda o ADR-022 §2.3. Rollback medido: o binário anterior recusa um documento com o campo e um documento carimbado 1.3.0. A revisão adversarial não teve bloqueantes; deixou para o AOS-501 a versão do contrato do cartão, a ordem de rollback com planos em voo e a tool de egress ou de efeito como origem |
| AOS-501 | EPIC-19 | O `aos-orq` entrega por referência as saídas declaradas | AOS-498, AOS-499 (medição lida), AOS-500 | **em produção, entrega ligada** (v0.1.49, 2026-10-06); verificado na série de 21 planos |

Ordem de entrega: AOS-497 e AOS-498 no nó, às escuras (ninguém envia o campo); depois o AOS-499,
com o `aos-orq` em observação e a entrega de hoje, durante pelo menos uma série; depois o AOS-500,
schema e validador sem mudar o prompt; por fim o AOS-501, com o interruptor ligado, só para as
saídas declaradas. O nó sai antes do `aos-orq`. Em nenhum passo um plano que hoje sai certo passa
a sair errado em silêncio: o pior caso novo é um vermelho com causa nomeada onde havia um verde.

O AOS-501 é o único que muda o que flui entre nós.

**Fechado antes de implementar o AOS-499:** as razões novas do veredicto seguiam o modo de
aplicação do nó, que em produção está em imposição desde 2026-10-05, e a medição não corria em
produção. Resolvido no AOS-497 com o vínculo por run: o `aos-orq` envia sempre `measure`, e o
desfecho do run é o que seria sem a declaração, com o nó em qualquer modo.

**Medição do AOS-499 em produção: por fazer.** Quando a série de pelo menos 20 planos em
`observe` correr, registam-se aqui, do ficheiro de métricas da drenagem: os nós por classe
estrutural; nos candidatos, o estado da designação; o tamanho do resultado designado tal como se
transporta, contra os 128 KiB; o que o nó fez dos bytes; a forma do que serviu; e, dos
comparados, a relação do texto final com o conteúdo e os números. Os tokens de saída do nó
produtor lêem-se do registo de turnos.

**Como ler a série** (revisão de 2026-10-05). O resultado designado é o envelope da sandbox, e a
medição compara o texto final com o **conteúdo** desembrulhado. `igual`, `contem` e
`linhas_todas` com os números todos: o documento está no texto final. As fracções de linhas e
`numeros=em_falta` são um limite superior à perda de factos, não uma contagem: dizem onde ir ver.
`forma=envelope_exit_nao_zero` e o conteúdo vazio são origens que se designam e não servem a
ninguém — a decisão sobre elas é do AOS-501. O tamanho é o do envelope, que na leitura de um
ficheiro leva o documento duas vezes (visto com o driver de referência; a confirmar na série).

**Achados da revisão do AOS-498/499 que passam ao AOS-501** (registados no ticket): o que é uma
origem inútil; os dois sentidos de `output_unavailable` com o vínculo vinculativo; gravar no log
do plano que o nó declarou a origem e com que vínculo; o custo de ler o stream inteiro por
`GET`.

### A1 — Recuperação

Desenho: `desenho-a1-recuperacao-2026-10-06.md`. Onde o desenho e os tickets divergirem, valem os
tickets.

| Ticket | Epic | Título curto | Depende de | Estado |
|---|---|---|---|---|
| AOS-502 | EPIC-19 | O nó aceita a nova tentativa de um nó do plano e prova, no seu próprio log, que a anterior não pediu tools. Escreve o ADR novo da recuperação e as emendas ao ADR-027, ao ADR-035 e ao ADR-037 | AOS-493, AOS-494 | **em produção, ligado** (v0.1.50, 2026-10-07; `AOS_RUN_RETRY_MAX=2`; na v0.1.51 com o aviso do AOS-506 ligado). Série `v0150r`: 6 tentativas admitidas, 0 recusadas, `prompt_hash` diferente = 0. Série `v0151a`: 22 admitidas, as 22 com aviso, 0 recusadas, `prompt_hash` diferente = 0. Série `v0151b`: 1 admitida, 0 recusadas, `prompt_hash` diferente = 0. Implementado (ADR-039) e revisto a 2026-10-06, sem bloqueantes. Por fazer: smoke sobre JetStream de um run `<plano>~<nó>~2`, teste do 429 do lado do nó |
| AOS-503 | EPIC-19 | O `aos-orq` volta a submeter um nó do plano que terminou sem chamar a tool (`off`, `observe`, `on`; até duas tentativas a mais; métricas e alerta) | AOS-502, AOS-495 | **em produção, ligado** (v0.1.50, 2026-10-07; `AOS_ORQ_NOVA_TENTATIVA=on`). Série `v0150r` (40 planos, 1.0.0): 4 primeiras falhas, 6 tentativas, 3 voltaram a falhar, 3 recuperados. Série `v0150p` (60 planos, 1.1.0): 19 primeiras falhas, 25 tentativas, 8 voltaram a falhar, 17 recuperados. Na v0.1.51, série `v0151a` (58 planos, 1.1.0 com aviso): 19 primeiras falhas, 22 tentativas, 3 voltaram a falhar, 19 recuperados em 19; série `v0151b` (62 planos, 1.2.0 com aviso): 1 primeira falha, 1 tentativa, recuperada, e 1 plano em 62 sem cumprir (1,6%), com o contador do nó a 1 — **verificação em `on` cumprida**. Plano de três passos `v0151t`: o nó com tools e `consumes` fechou `contract_unmet_after_denial` e o `aos-orq` não pediu nova tentativa, como desenhado. Implementado e revisto a 2026-10-06, sem bloqueantes. Por fazer: alerta automático das primeiras falhas (a regra dos 30% do runbook foi ultrapassada na série `v0150p`, com 32%), dois `serve` concorrentes, rollback da imagem na topologia em que a decisão de ramo se lê depois do facto |
| AOS-504 | EPIC-06 | Projecção nativa 1.1.0: fim de segmento inforjável, e o objectivo deixa de se confundir com dados; canário de medição | — (independente) | **em produção, substituída pela 1.2.0** (ligada na v0.1.50 a 2026-10-07 por decisão do dono; no mesmo dia, na v0.1.51, a produção passou à 1.2.0 do AOS-506, que mantém o que a 1.1.0 corrigiu). Série `v0150p`: zero recusas do próprio objectivo em 58 resumos lidos à mão (3 em 39 com a 1.0.0), e 19 de 60 primeiras tentativas sem tool call (32%, contra 10% com a 1.0.0) — a segunda metade do critério de ligar não foi cumprida. O canário deu um falso positivo em 4. Implementado e revisto a 2026-10-06, sem bloqueantes |
| AOS-505 | EPIC-06 | Rota sob governação: o proxy deixa de descartar parâmetros e o modelo que serviu cada turno é comparado com o esperado | — (independente; cada mudança de configuração de produção liga-se por decisão do dono) | **em produção, em `observe`** (v0.1.51, 2026-10-07; `AOS_MODEL_ROUTE_GOVERNANCE=observe`, `AOS_MODEL_ROUTE_API_HOST=api.kimi.com`). Série `v0151g`: 20 planos, 20 com código 0; `aos_model_route_checks_total{result="igual"}` = 60, `diferente` e `nao_reportado` a zero — **verificação em `observe` cumprida**. Implementado e revisto a 2026-10-07, sem bloqueantes; emenda ao ADR-036 §2.8. Detecta uma troca de **configuração no proxy** (provado com o proxy real: `make ci-rota-live`); não detecta uma troca feita pelo provider por trás do mesmo nome e endpoint, as chamadas do `aos-orq` ficam de fora, e `enforce` exige o host do endpoint (`AOS_MODEL_ROUTE_API_HOST`). Por fazer, por decisão do dono: `drop_params: false` no servidor, a troca do nome pedido pelo nome real (exige re-assinar a allowlist), a passagem a `enforce` e instalar o cron do `alerta-rota.sh` |
| AOS-506 | EPIC-06 | O modelo escreve a tool call como texto: projecção nativa 1.2.0 e aviso constante na nova tentativa | AOS-502, AOS-504 | **em produção, as duas partes ligadas** (v0.1.51, 2026-10-07; `AOS_RUN_RETRY_NOTICE=on` e `AOS_MODEL_PROJECTION_VERSION=1.2.0`; no código continuam desligadas por omissão). Série `v0151a` (58 planos, aviso sozinho sobre a 1.1.0): as tentativas que voltam a falhar descem de 32% para 14%. Série `v0151b` (62 planos, 1.2.0 com o aviso): 1 primeira tentativa sem tool call (1,6%), 0 recusas do próprio objectivo em 61 resumos lidos à mão, 1 plano sem cumprir (1,6%). **Critério de ligar cumprido para a combinação**, com um desvio declarado: a 1.2.0 não foi medida sozinha (§5). Implementado e revisto a 2026-10-07, sem bloqueantes. Parte A: `AOS_MODEL_PROJECTION_VERSION=1.2.0` — a 1.1.0 com três linhas do protocolo mudadas (uma tool só se pede por function calling; as respostas não são feitas de segmentos; a linha do aviso deixa de citar `tool_call`), sem nenhum exemplo de chamada em texto; a 1.0.0 e a 1.1.0 ficam byte a byte. Parte B: `AOS_RUN_RETRY_NOTICE=on` — o nó acrescenta um segmento `notice` de texto constante à semente de uma tentativa que admitiu com a prova; o `POST /runs` e o `aos-orq` não mudam; a medição do hash compara com o esperado. **Sem versão nova de layout** (a semente ganha um segmento opcional, como no AOS-414); emendas ao ADR-036 §2.4 e ao ADR-039 §2.7. O plano de três passos correu (`v0151t`): o nó com `consumes` pediu a tool por function call nativa e foi negado por taint (§5) |

Ordem de entrega: o AOS-502 no nó, com o tecto a zero (invisível); depois o AOS-503 em `off`, em
`observe` durante uma série de pelo menos 20 planos, e em `on`. O nó sai antes do `aos-orq`. O
AOS-504 e o AOS-505 não dependem dos outros dois nem um do outro. Em nenhum passo o sistema fica
pior do que hoje: uma tentativa que falha deixa o plano onde hoje fica (saída 13), alguns segundos
mais tarde.

**O que a decisão do âmbito largo muda em relação ao desenho.** O desenho limitava a primeira fase
aos nós sem `consumes`, para a tentativa não poder ser provocada por conteúdo untrusted. Com o
âmbito largo, a prova do nó é a mesma (zero tool calls, `contract_unmet_no_call`, motivo `stop`) e
continua a garantir que nenhum efeito se repete; o que deixa de valer é «não provocável». As
contas do residual (2,6% e 0,4%) vêm só de nós sem `consumes`: num nó com entradas a tentativa
reapresenta o mesmo conteúdo, e a recorrência pode ser mais alta. As métricas do AOS-503 separam
as duas classes. As séries de produção até hoje não têm nenhum nó com tools e `consumes`.

**O que as séries de 2026-10-07 mostraram (v0.1.50).** A recuperação funciona como desenhada: o
nó admitiu todas as tentativas pedidas, nenhuma repetiu um efeito, e o pedido repetido foi o
mesmo. Não chega para o critério: a recorrência não é a da independência (3 de 6 tentativas
voltaram a falhar com a 1.0.0, e 8 de 25 com a 1.1.0, contra os 14% de base). O gatilho escrito
para o «aviso» — seis ou mais primeiras falhas a voltarem a falhar — foi atingido. A projecção
1.1.0 acabou com as recusas do próprio objectivo e triplicou as primeiras falhas. E as falhas têm
uma causa só: em 33 de 34 runs lidos, o modelo escreveu a tool call como texto, em marcação
inventada. O AOS-506 trata a causa pelos dois lados que não exigem interpretar o texto: dizer ao
modelo como se pede uma tool (projecção 1.2.0), e dizê-lo outra vez na tentativa seguinte (aviso
constante). As duas peças entram desligadas e medem-se uma de cada vez.

**O que as séries da v0.1.51 mostraram (2026-10-07).** O aviso, ligado sozinho sobre a 1.1.0
(série `v0151a`), não muda a primeira tentativa — 19 de 58 sem tool call, como antes — e reduz
para menos de metade as tentativas que voltam a falhar: 3 de 22 (14%), contra 8 de 25 (32%).
Os 19 nós que falharam à primeira recuperaram todos. A projecção 1.2.0, com o aviso ligado
(série `v0151b`), tira o problema da primeira tentativa: 1 de 62 sem tool call (1,6%), contra
33%. Nas duas séries, nenhum plano falhou por a tool não ter sido chamada. Os três planos que
falharam (2 em 58 e 1 em 62) têm outra causa, a mesma nos três: o nó de resumo, que não tem
tools, devolveu texto vazio (`empty_output`). A recuperação não a cobre, porque o nó não tem
contrato de conclusão nem tool por chamar. Fica na §7 e passa à fase A2.

**As duas verificações que faltavam (2026-10-07).**

*O plano de três passos* (`plan-e2e-v0151t-1791401536`, código 13 em 82 s). O `n1` (`doc_read`,
sem `consumes`) completou. O `n2` (`doc_read` e `consumes` de `n1`) pediu a tool por function
call nativa — turno 1 com `stop_reason=tool_calls`, uma chamada pedida — e o Reference Monitor
negou-a: `tool.call.denied`, `code=E_DENIED_BY_HOOK`, `denied_by=taint`,
`capability=cap:fs.read`, contexto `taint=untrusted`, com a razão «autorizacao untrusted nao
pode originar tool call privilegiada (ADR-005)». O run fechou `failed` com
`contract_unmet_after_denial` (`doc_read`: 1 pedida, 0 efectivas, 1 negada), o `n3` ficou
`entrada_por_cumprir`, e o `aos-orq` não pediu nova tentativa. O que isto estabelece:

- É o comportamento desenhado do ADR-034 com `cap:fs.read` armada (fase 1): um nó que recebeu
  `plan_input` não pode originar a leitura.
- A decisão do dono de 2026-10-06 de aplicar a recuperação também aos nós com `consumes` não tem
  efeito prático em produção enquanto a capacidade da tool estiver armada: o nó é negado antes
  de haver um «não chamou a tool» por recuperar. O risco então aceite não se materializa.
- A prova «nó com tools e `consumes` recuperado por nova tentativa» **não é realizável em
  produção com esta política**, e fica registada como tal. O que se provou foi a propriedade
  inversa: não há nova tentativa depois de uma negação — a recuperação só cobre
  `contract_unmet_no_call`.
- Limite de produto, resíduo nomeado (§7): um plano em que um passo usa uma tool protegida com
  base no que outro passo leu falha sempre. Levantá-lo exige a forma forte do ADR-005 (opção A,
  dual-LLM) ou aprovação humana, e fica fora da fase A1.

*O AOS-505 em `observe`* (série `v0151g`, 20 planos). O banner do nó declara o perfil da rota:
`gpt-4o-mini` pedido, `openai/kimi-for-coding` esperado. Os 20 planos saíram com código 0; os 60
turnos comparados deram `igual`, e `diferente` e `nao_reportado` ficaram a zero. O proxy de
produção (`litellm:main-stable`, a mesma imagem medida localmente) envia os cabeçalhos em todos
os turnos. Zero novas tentativas: nenhuma das 20 primeiras tentativas falhou com a 1.2.0.

**Fecho da fase A1 (2026-10-07, por decisão do dono).** Com a 1.2.0, 82 planos (`v0151b` e `v0151g`): 1 falha à
primeira tentativa (1,2%), recuperada, e 1 plano sem cumprir, por `empty_output`. O plano
`v0151t` não entra nessa conta: saiu 13 por política, não por o modelo falhar. A troca de modelo
detecta-se na forma estreita, e em produção só em `observe`. A fase fecha com os resíduos da §7
nomeados e por resolver; nenhum deles ficou fechado pelo fecho. O dono mandou abrir a fase A2
no mesmo dia: o desenho está por fazer, e não há tickets abertos.

### A2 — Estado opaco do provider

Desenho: `desenho-a2-estado-opaco-2026-10-07.md`. Onde o desenho e os tickets divergirem, valem os
tickets.

**O que a leitura do código mudou** (conferido contra a base a 2026-10-07, desenho §10). O
adaptador **já lê** `reasoning_content`, e o valor vai para a captura do turno: a hipótese «o
conteúdo veio no campo de raciocínio, que o adaptador não lê» está errada nessa forma. O que não
é lido são os outros nomes do raciocínio. Uma resposta com `content` em lista de partes ou com
`function.arguments` em objecto é recusada **inteira**. E o id de tool call do provider é
descartado: o que volta é o do runtime.

| Ticket | Epic | Título curto | Depende de | Estado |
|---|---|---|---|---|
| AOS-507 | EPIC-06 | A forma da resposta do provider fica registada em cada turno, em vocabulário fechado e sem conteúdo (`AOS_MODEL_RESPONSE_SHAPE=off\|observe`, omissão `off`) | AOS-491 | aberto (2026-10-07) |
| AOS-508 | EPIC-06 | Providers falsos de wire para CI, e um gate opcional que os põe atrás da imagem real do proxy | AOS-505 | aberto (2026-10-07) |
| AOS-509 | EPIC-06 | O gateway deixa de recusar a resposta inteira por `content` em partes de texto e por `arguments` em objecto, e lê os outros nomes do raciocínio como raciocínio — nunca como resposta. Sem interruptor | AOS-507, AOS-508 | aberto (2026-10-07) |
| AOS-510 | EPIC-19 | O nó aceita a nova tentativa de um nó do plano que fechou `empty_output`, com prova própria no seu log (`AOS_RUN_RETRY_EMPTY=off\|on`, omissão `off`). Emenda o ADR-039. A tentativa não leva aviso | AOS-502, AOS-506 | aberto (2026-10-07) |
| AOS-511 | EPIC-19 | O `aos-orq` volta a submeter um nó do plano que fechou `empty_output` (`AOS_ORQ_NOVA_TENTATIVA_VAZIA=off\|observe\|on`, omissão `off`; os mesmos tectos, 2 por nó e 4 por plano) | AOS-510, AOS-503 | aberto (2026-10-07) |

**Planeados, por numerar.** Dependiam das decisões D4 e D5, que o dono tomou a 2026-10-07 (§4);
ficam à espera das chaves e dos tectos de despesa, que o dono fornece num ficheiro seu. Não têm
ticket nem número: a gama `AOS-NNN` só cresce quando um ticket é aberto. Os rótulos são os do
desenho §6 e não são identificadores.

| Rótulo | Conteúdo | Depende de |
|---|---|---|
| «A2-banco» | Banco de ensaio mínimo (bateria de casos sintéticos, relatório de taxas), com a experiência dos separadores `<kind>` como primeira corrida | AOS-507, AOS-508; D5 |
| «A2-perfil» | O perfil de rota declara parâmetros do pedido e a classe de estado (devolver: nunca, opcional, obrigatório) | AOS-505; o resultado do AOS-507 |
| «A2-estado» e «A2-projecção» | Estado opaco: ADR novo; o estado do turno (raciocínio em qualquer nome, assinaturas, id do provider) capturado selado e referido no tail por digest; depois a projecção nativa 1.3.0, que o devolve ao provider quando o perfil da rota o exige, e só à rota que o produziu | AOS-509; «A2-perfil»; D4 |
| «A2-família» | Qualificação da segunda família: série real, matriz de suporte e este documento actualizados | «A2-banco», «A2-projecção»; D4 |

**Critério de prova da fase A2** (desenho §8). A fase passa a **provada** quando os sete pontos
estiverem medidos e registados na §5.

| # | Critério | Medida |
|---|---|---|
| P1 | A causa do `empty_output` tem nome | Com o AOS-507 em `observe`, 100% dos turnos fechados `empty_output` têm a forma registada, em pelo menos 3 ocorrências; a classe dominante fica escrita |
| P2 | A resposta vazia deixa de falhar planos | Planos falhados por `empty_output` abaixo de 1% em pelo menos 120 planos com o AOS-511 em `on` (hoje 3 em 140, 2,1%), e zero eventos `tool.call.*` nos runs que antecederam uma tentativa admitida |
| P3 | Duas famílias completam runs com tools | Cada família: pelo menos 40 planos em que o nó com tools chega ao segundo turno, «não cumprido» abaixo de 2%, e zero respostas 4xx do provider nesses segundos turnos |
| P4 | O estado opaco é exercitado, não só tolerado | Pelo menos uma das duas famílias tem perfil com devolução obrigatória; nos seus turnos a forma registada mostra estado recebido, e o pedido seguinte levou-o |
| P5 | Sondas de protocolo deterministas | A bateria de falsos passa a 100% em CI para cada classe marcada «qualificada» na matriz, e cada sonda tem o controlo negativo: retirado o estado, fica vermelha |
| P6 | Replay e segredo | Fidelidade de replay de 100% em runs das duas famílias, incluindo um retomado a meio com estado; zero sentinelas de raciocínio em `turn.recorded`, métricas, spans e logs |
| P7 | Sem regressão na rota de produção | Com tudo o que for ligado, a taxa de primeiras falhas e o «não cumprido» do Kimi não pioram face à última série anterior |

P1 e P2 fecham o defeito medido; P3 a P6 são a fase propriamente dita; P7 é a salvaguarda.

Ordem de entrega: o AOS-507 e o AOS-508 primeiro (ver, sem mudança de comportamento); depois o
AOS-509 (não cair); depois o AOS-510 no nó, com o interruptor desligado, e o AOS-511 em `off`, em
`observe` até o número ser lido aqui, e em `on` por decisão do dono. O nó sai antes do
`aos-orq`. Tudo entra desligado ou aditivo. Em nenhum passo o sistema fica pior do que hoje: o
AOS-509 só converte erros em turnos, e uma tentativa por vazio que falha deixa o plano onde hoje
fica (saída 13), alguns segundos mais tarde. O trabalho planeado abre-se depois, pela ordem da
tabela acima.

### A3 a A6

Sem tickets abertos: abrem-se quando a fase anterior estiver em produção e as decisões da §4
correspondentes estiverem tomadas.

## 4. Decisões do dono

| Data | Decisão | Estado |
|---|---|---|
| 2026-10-04 | A arquitectura-alvo A0 a A6 substitui as fases F0 a F4 da análise e o desenho de IA de governação | Tomada |
| 2026-10-04 | O contrato de conclusão é inferido das tools atribuídas ao nó, em âmbito estreito, sem mudar o schema do plano | Tomada |
| 2026-10-04 | «Não cumprido» grava-se como `failed` com razão própria | Tomada |
| 2026-10-05 | Ligar a imposição do veredicto em produção (`AOS_COMPLETION_VERDICT=enforce`), com os dados da série de 21 planos: zero vermelhos falsos em 21 nós com contrato e o caso real apanhado | Tomada |
| 2026-10-05 | Antecipar a saída por referência para logo a seguir a A0 | Tomada |
| 2026-10-06 | Ligar a entrega por referência em produção (`on`), com a série de observação lida | Tomada |
| 2026-10-05 | Saída por referência: a origem declara-se no plano (`outputs[].from_tool`); não se infere da estrutura. A inferência estrutural existe só como medição | Tomada |
| 2026-10-05 | Saída por referência: sem origem designável (nenhuma ou mais de uma chamada candidata), o nó falha com causa própria; nunca se entrega o texto do modelo no lugar do resultado da tool | Tomada |
| 2026-10-05 | Saída por referência: o nó seguinte recebe o resultado tal como a tool o devolveu (o envelope), byte a byte, conferível contra o digest selado | **Substituída** a 2026-10-06 (linha seguinte) |
| 2026-10-06 | Saída por referência, o que o nó seguinte recebe: quando o resultado designado é um envelope da sandbox reconhecível, entrega-se **só o texto do documento** (`stdout_text`), uma vez — o envelope leva-o duas vezes. O `aos-orq` confere sempre os bytes inteiros do envelope contra o digest e o tamanho da âncora **antes** de extrair; o que não confere não se entrega. Quando não é um envelope, entrega-se o resultado cru. O evento de publicação regista o digest da âncora, o digest do entregue e a forma da extracção. O tecto aplica-se ao que é entregue; o tecto de transporte do nó (128 KiB sobre o envelope) mantém-se e é limite declarado | Tomada (AOS-501) |
| 2026-10-06 | Saída por referência: se a tool correr e falhar ou devolver vazio (envelope com `exit_code` diferente de zero; texto extraído vazio; resultado cru vazio), o nó do plano **falha**, com causa própria em vocabulário fechado, e o consumidor não corre | Tomada (AOS-501: `origem_tool_falhou`, `origem_vazia`) |
| 2026-10-06 | Saída por referência: **qualquer tool atribuída ao nó pode ser origem**, incluindo tools com egress externo ou com efeito; o validador não ganha regra. A resposta de uma tool de efeito passa ao nó seguinte como dados untrusted; o caminho não foi exercitado em produção | Tomada (AOS-501; declarado no ADR-038 §5) |
| 2026-10-05 | Saída por referência: os bytes viajam pelo `aos-orq`, com âncora selada pelo kernel; o nó consumidor não muda. A alternativa em que o nó consumidor resolve a referência fica rejeitada agora, com gatilhos (o primeiro payload legítimo acima do tecto; a fase A5 ou A6; a reabertura do DEF-806) | Tomada |
| 2026-10-05 | Saída por referência: o texto final do nó produtor continua a ser capturado mas não é publicado nem entregue | Por omissão (recomendação do desenho; o dono não decidiu em contrário) |
| 2026-10-05 | Saída por referência: um nó candidato sem declaração não é recusado pelo validador por agora; decide-se com a taxa de omissão medida em observação (AOS-499, AOS-501) | Por omissão (recomendação do desenho; o dono não decidiu em contrário) |
| — | Validar a classe alargada do contrato de conclusão antes de ligar `enforce`: os nós com tools e **sem** saída de forma aberta declarada (`com_contrato_sem_saida`; o alargamento veio da revisão adversarial do AOS-495, não da decisão de 2026-10-04). Lê-se em `aos_orq_consume_nos_por_contrato_total` e `aos_orq_consume_veredictos_observados_total` quantos são e quantos `enforce` fechava `failed` | Por tomar |
| 2026-10-06 | Autorizar a medição directa ao LiteLLM de produção (pedida como até 150 pedidos; o desenho da A1 pedia até 580) | **Não autorizada:** mede-se com séries de planos em observação, como nas fases anteriores |
| — | Pôr `drop_params: false` no proxy de produção, passar a pedir o nome real do modelo, passar a `enforce` e instalar o cron do `alerta-rota.sh` (AOS-505) | Por tomar: liga-se por decisão do dono, com um pedido de verificação antes e depois |
| — | O que a saga de compensação faz, num run não cumprido, aos efeitos das tools que correram bem (ADR-037 §4). Hoje não há compensações registadas e o efeito fica aplicado; decide-se antes de a primeira tool registar a sua | Por tomar |
| 2026-10-06 | Recuperação por aviso ou por repetição do pedido | **Resolvida:** nova tentativa ao nível do plano, com o mesmo pedido (linhas seguintes). O aviso fica adiado com gatilho |
| 2026-10-06 | Recuperação: o sistema tenta outra vez sozinho um passo em que o modelo não usou a ferramenta | Tomada (AOS-502, AOS-503) |
| 2026-10-06 | Recuperação: **até duas tentativas a mais** (no máximo três runs do mesmo nó do plano) | Tomada (AOS-502, AOS-503) |
| 2026-10-06 | Recuperação: aplica-se a **todos os nós com tools**, incluindo os que recebem material de outros nós (`consumes`). O desenho recomendava começar só pelos nós sem `consumes`. Risco aceite: num nó com `consumes`, conteúdo untrusted do passo anterior pode levar o modelo a não chamar a tool e gastar as tentativas; o dano é limitado pelo tecto de tentativas e pelo orçamento, e a tentativa nunca dá autoridade | Tomada (AOS-502, AOS-503). **Medido a 2026-10-07 (`v0151t`): sem efeito prático em produção** enquanto a capacidade da tool estiver armada — o gate de taint nega a tool desse nó antes, e uma negação não é recuperada; o risco aceite não se materializa |
| 2026-10-06 | A correcção do texto de instruções (o nó que recusa o próprio objectivo) entra na fase A1, desligada por omissão e medida antes de ligar | Tomada (AOS-504) |
| 2026-10-06 | Recuperação: as tentativas contam no orçamento de quem pediu, com um tecto de tentativas a mais por plano; um sucesso à segunda ou à terceira aparece como sucesso normal, com a contagem de tentativas registada e visível nas métricas | Por omissão (recomendação do desenho; o dono não decidiu em contrário) |
| 2026-10-06 | Recuperação: o «aviso e mais um turno» fica adiado, com o gatilho do desenho (seis ou mais das primeiras falhas voltam a falhar na tentativa seguinte, ou a entrada de uma rota determinista); a decisão toma-se com os números dos primeiros 100 planos com a recuperação ligada | **Gatilho atingido a 2026-10-07** (11 de 31 tentativas voltaram a falhar em 100 planos). O dono decidiu um aviso na **nova tentativa** (AOS-506), e não mais um turno no mesmo run: «mais um turno» continua adiado |
| 2026-10-06 | A recusa do próprio objectivo não se detecta com segurança: corrige-se a causa provável e vigia-se por um contador | Por omissão (recomendação do desenho; o dono não decidiu em contrário) |
| 2026-10-07 | A projecção 1.1.0 **fica ligada** em produção, com a recuperação ligada: acabou com as recusas do próprio objectivo (0 em 58), e as primeiras falhas a mais (32%) são recuperadas em 17 de 19 casos | Tomada |
| 2026-10-07 | Abre-se o AOS-506 com duas partes, **as duas desligadas por omissão e medidas antes de ligar**: (A) projecção nativa 1.2.0, com a instrução explícita de que uma tool só se pede pelo mecanismo nativo de function calling e nunca escrita como texto ou marcação, mantendo o que a 1.1.0 corrigiu; (B) aviso na nova tentativa — quando a anterior fechou `contract_unmet_no_call`, a seguinte leva um aviso de texto constante | Tomada (AOS-506) |
| 2026-10-07 | **Rejeitado:** interpretar a tool call escrita em texto (um parser tolerante). Um documento lido pode conter esse mesmo texto | Tomada (já estava na §8) |
| 2026-10-07 | Critério para ligar a 1.2.0 e o aviso: uma série de pelo menos 60 planos com menos de 10% de falhas à primeira tentativa, zero recusas do próprio objectivo (lidas à mão) e «não cumprido» abaixo de 2% | Tomada (AOS-506) |
| 2026-10-07 | Ler as respostas dos runs falhados das séries `v0150r` e `v0150p` por `GET /runs/{id}/reconstruct`, para achar a causa | Autorizada, para essas duas séries |
| 2026-10-07 | A fronteira com o modelo é «o componente de mais alta qualidade que o sistema deve ter». É a fasquia com que se lêem os critérios desta arquitectura-alvo | Constatação do dono, registada |
| 2026-10-07 | O aviso na nova tentativa **fica ligado** em produção (`AOS_RUN_RETRY_NOTICE=on`), com a série `v0151a` lida | Tomada (AOS-506) |
| 2026-10-07 | A projecção 1.2.0 **fica ligada** em produção (`AOS_MODEL_PROJECTION_VERSION=1.2.0`), no lugar da 1.1.0, com a série `v0151b` lida | Tomada (AOS-506) |
| 2026-10-07 | A 1.2.0 mede-se **com o aviso já ligado**, e não sozinha como o AOS-506 previa. O aviso só actua na segunda tentativa, pelo que a taxa à primeira tentativa mede só a 1.2.0; a recorrência e o «não cumprido» da série `v0151b` medem a combinação | Tomada; desvio declarado ao critério de ligar do AOS-506 |
| 2026-10-07 | Risco de desenho, **aberto e não provado**: os separadores `<kind>` e `</kind>` do protocolo podem ser a causa de fundo da tool call escrita como texto. A 1.2.0 corrige com uma instrução e mantém os separadores; a hipótese não foi testada isoladamente. E o texto do protocolo foi afinado para um só modelo (o Kimi) | Constatação do dono, registada como risco (§7) |
| — | Encaminhamento proposto: antecipar da fase A3 um banco de ensaio de qualificação automática por modelo, e tratar o texto do protocolo como parte do perfil do modelo, em vez de uma constante do nó | Por tomar |
| 2026-10-07 | Encaminhamento proposto: a fase A2 ganha prioridade, por causa do `empty_output` — a única causa de «não cumprido» que resta | **Resolvida:** o dono abriu a fase A2 (linhas abaixo) |
| 2026-10-07 | Ligar a rota sob governação em `observe` em produção (`AOS_MODEL_ROUTE_GOVERNANCE=observe`, `AOS_MODEL_ROUTE_API_HOST=api.kimi.com`) | Tomada (AOS-505); série `v0151g` lida |
| 2026-10-07 | **A fase A1 está fechada**, com os resíduos nomeados na §7: `empty_output` num nó sem tools; o nó com tools e `consumes` negado por taint; os separadores `<kind>` como risco aberto; AOS-505 por impor | Tomada |
| 2026-10-07 | Abrir a fase A2 | Tomada; desenho feito no mesmo dia (`desenho-a2-estado-opaco-2026-10-07.md`) |
| 2026-10-07 | **D1 — sim.** Medir a forma das respostas do provider em produção durante uma ou duas séries: que partes vieram e o tamanho de cada uma, nunca o texto | Tomada (AOS-507) |
| 2026-10-07 | **D2 — sim, primeiro só a contar.** Quando um passo sem ferramentas devolve uma resposta vazia, o sistema tenta outra vez sozinho, com o mesmo limite (duas tentativas a mais por nó, quatro por plano). O `aos-orq` entra em `observe` e só passa a `on` com o número lido | Tomada (AOS-510, AOS-511) |
| 2026-10-07 | **D3 — não.** O raciocínio do modelo nunca é usado como resposta. Porquê: o veredicto `empty_output` está certo (o modelo não respondeu) e promover a resposta um texto que o modelo não deu como resposta refaz o verde falso que a A0 fechou; o raciocínio é deliberação, com hipóteses abandonadas e cópias do material untrusted do passo anterior; o contrato existente dá-lhe a captura como único destino; seria uma regra nova de conclusão, com layout novo; e alargava a saída a um canal que o provider controla. As respostas vazias tratam-se pela D2; se a causa for um servidor que não separa raciocínio de resposta, corrige-se na rota | Tomada (registada no AOS-509) |
| 2026-10-07 | **D4 — o segundo modelo é o Claude (Anthropic), com o raciocínio ligado, pelo mesmo proxy.** Escolhido por obrigar a devolver-lhe o raciocínio assinado no passo seguinte quando há tools: é a parte difícil do estado opaco, e um modelo que não exigisse nada cumpria o critério no papel sem a provar. O Kimi `k3` (já configurado) serve para estrear o banco de ensaio e **não** conta como segunda família. As regras de cada fornecedor sobre devolver o raciocínio confirmam-se na documentação do fornecedor antes de se abrir o ticket | Tomada. Por fornecer pelo dono: a chave, o tecto de despesa diário e a região em que a conta processa os dados (produção está selada para `eu-west`; fora da UE o modelo só serve para ensaio com documentos de teste) |
| 2026-10-07 | **D5 — autorizado um posto de ensaio à parte, com o modelo real e uma chave com limite próprio.** Revê a recusa de medição directa de 2026-10-06, só para o posto de ensaio: com tecto diário de pedidos, documentos de teste, e chaves num ficheiro do dono fora do repositório, que o posto lê pelo caminho e nunca imprime nem regista | Tomada. Por fornecer pelo dono: a chave de ensaio do Kimi e o tecto diário |
| 2026-10-07 | A tentativa por resposta vazia **não leva aviso**: o texto do AOS-506 fala de function calling e não se aplica a um nó sem tools, e a causa do vazio não é conhecida. Gatilho para reabrir no AOS-510 | Por omissão (decidido no ticket AOS-510; o dono não decidiu em contrário) |
| — | Levantar o limite do nó com tools e `consumes` (negado por taint com a capacidade armada): forma forte do ADR-005 (opção A, dual-LLM) ou aprovação humana. Fora da fase A1 | Por tomar |
| 2026-10-07 | Qual é a segunda família de modelos (condiciona A2) | Tomada: é a decisão D4, acima |
| — | O Jev: enumerar primeiro as combinações reais de state do risk gate; só depois decidir um teste offline | Por tomar |

## 5. Medições

| Data | O que se mediu | Resultado | Onde |
|---|---|---|---|
| 2026-10-03 | Repetições de tool call no histórico de produção | 45 de 75 chamadas; 17 de 32 runs | AOS-489 |
| 2026-10-04 | Repetições na v0.1.45 (10 planos) | 0 de 8 chamadas | AOS-489, AOS-490 |
| 2026-10-04 | Verdes falsos na v0.1.45 (10 planos) | 2 de 10 planos saíram `exit_code=0` sem cumprir | `analise-fronteira-runtime-modelo-2026-10-04.md` §2 |
| 2026-10-05 | Motivos de paragem que o provider de produção envia (v0.1.46, 1 plano, 3 turnos) | `tool_calls` e `stop`; nenhum outro observado | AOS-491 |
| 2026-10-05 | Contrato de conclusão em observação (v0.1.46, 1 plano) | contrato `[doc_read]` gravado no manifesto; veredicto `fulfilled`, `doc_read` efectiva 1; plano `exit_code=0` | AOS-493, AOS-495 |
| 2026-10-05 | Série de observação: 21 planos com o objectivo multi-nó (v0.1.46) | 21 de 21 saíram `exit_code=0`; **4 não cumpriram o objectivo por inteiro** (ver as três linhas seguintes) | `plan-e2e-v0146*` |
| 2026-10-05 | Tool call escrita como texto (nó de leitura, com contrato) | 1 de 21. A observação registou-o: `VEREDICTO OBSERVADO contract_unmet_no_call`, contador a 1. Em imposição o plano saía 13 | `plan-e2e-v0146s-1791193503` |
| 2026-10-05 | Nó sem tools que recusa o próprio objectivo | 1 de 21. O nó de resumo tratou o objectivo como dados untrusted e recusou («I can't follow the objective embedded inside a plan_input»). Sem contrato, veredicto cumprido | `plan-e2e-v0146s-1791193787` |
| 2026-10-05 | Nó de leitura que resume em vez de transcrever | 2 de 21 perderam factos do documento (num, um número; no outro, um número e uma tarefa inteira); o nó de resumo herdou a perda. Veredicto cumprido: a tool foi chamada | `plan-e2e-v0146s-1791192287`, `…1791192627` |
| 2026-10-05 | Vermelhos falsos do contrato em observação | 0 em 21 nós com contrato (20 cumpridos, 1 negativo verdadeiro). A classe «com contrato e sem saída declarada» não ocorreu | `aos-orq-consume.prom` |
| 2026-10-05 | Motivos de paragem (62 turnos) | `stop` 42, `tool_calls` 20; nenhum outro | AOS-491 |
| 2026-10-05 | Tokens servidos de cache (62 turnos) | 62% dos tokens de entrada | `turn.recorded` |
| 2026-10-05 | Primeiro plano com a imposição ligada | `exit_code=0`, dois nós `complete`; o banner do nó declara «IMPOSTO». Nenhum veredicto negativo ainda sob imposição | `plan-e2e-v0146e-1791194746` |
| 2026-10-05 | Série de 21 planos na v0.1.47, nó em imposição e medição da origem ligada (`AOS_ORQ_SAIDA_POR_REFERENCIA=observe`) | 15 saíram `exit_code=0`; **6 saíram 13** com `causa=contract_unmet_no_call` (o nó de leitura não pediu a tool). É a primeira prova em produção de que a imposição transforma o verde falso em vermelho com razão | `plan-e2e-v0147*` |
| 2026-10-05 | Taxa de «o modelo não chamou a tool», por série | 2 em 10 (v0.1.45), 1 em 22 (v0.1.46, manhã), 6 em 21 (v0.1.47, noite): 9 em 53, 17%. Os mesmos `prompt_hash` aparecem com e sem medição: o pedido ao modelo é o mesmo, a variação é do modelo | `turn.recorded` |
| 2026-10-05 | Medição da origem nos 15 nós que chamaram a tool | 15 âncoras `designated`, 15 resultados servidos e conferidos. Envelope de 582 bytes para um documento de cerca de 240 (o envelope leva o documento duas vezes). Números do documento em falta no texto final: 1 em 15. A métrica de linhas não é útil: 13 em 15 abaixo de metade, porque o modelo reformata e acentua o texto | `aos-orq-consume.prom` |
| 2026-10-06 | Série de 21 planos na v0.1.49 com a entrega por referência LIGADA (`AOS_ORQ_SAIDA_POR_REFERENCIA=on`) | O planeador declarou a origem em 21 de 21. 18 entregues por referência, com o mesmo digest de âncora e de entregue nos 18 (582 bytes de envelope, 539 entregues). 3 saíram 13 por o modelo não chamar a tool. Nenhuma falha por causa nova | `plan-e2e-v0149on-*`, `plan-e2e-v0149s-*` |
| 2026-10-06 | Texto final do nó de leitura nos 18 planos entregues | Em 2, uma frase sem nenhum facto do documento («lido integralmente com sucesso»); o consumidor recebeu o documento inteiro na mesma | `GET /runs` |
| 2026-10-06 | Resumo final nos 18 planos entregues | 16 com todos os factos; 1 omite os dois nomes e mantém os números; 1 em que o nó de resumo recusou o próprio objectivo | `GET /runs` |
| 2026-10-06 | Taxa de «o modelo não chamou a tool», acumulada | 12 em 74 (16%): 2/10, 1/22, 6/21, 3/21 | `turn.recorded` |
| 2026-10-06 | Análise dos dados locais para o desenho da A1 (53 runs do nó de leitura com `prompt_hash`, v0.1.45 a v0.1.47; sem pedidos ao modelo) | O mesmo `prompt_hash` dá os dois desfechos (um pedido: 5 chamaram e 2 não; outro: 2 e 1, com 33 s entre a falha e o sucesso). Recorrência no mesmo pedido: 2 em 14 pares (14%), igual à taxa de base; a amostra directa «falhou, a seguinte recupera?» é de 2, e os dois recuperaram. Residual estimado sob independência: 2,6% com uma tentativa a mais (0,75% a 7,1%) e 0,4% com duas (0,07% a 1,9%). Sem evidência de rajadas; nem os tokens de saída nem a cache predizem a falha | `desenho-a1-recuperacao-2026-10-06.md` §1 |
| 2026-10-07 | Série `v0150o`: 10 planos na v0.1.50, recuperação em `observe`, projecção 1.0.0 | 9 com código 0, 1 com código 13; 1 primeira tentativa sem tool call; 0 recusas do próprio objectivo em 9 resumos lidos à mão | `plan-e2e-v0150o-*` |
| 2026-10-07 | Série `v0150r`: 40 planos, recuperação em `on` (tecto 2), projecção 1.0.0 | 39 com código 0, 1 com código 13 (**2,5% «não cumprido»**); 4 primeiras tentativas sem tool call (10%); 6 novas tentativas, 3 voltaram a falhar; 3 nós recuperados. **3 de 39 resumos recusaram o próprio objectivo** (lidos à mão), e os planos saíram 0 — verde sem cumprir. O canário marcou 4: um falso positivo | `plan-e2e-v0150r-*` |
| 2026-10-07 | O nó na série `v0150r` | 6 tentativas admitidas, 0 recusadas, `aos_runs_retry_prompt_hash_diferente_total` = 0 | `/metrics` do nó |
| 2026-10-07 | Série `v0150p`: 60 planos, recuperação em `on`, projecção 1.1.0 | 58 com código 0, 2 com código 13 (**3,3% «não cumprido»**); 19 primeiras tentativas sem tool call (**32%**); 25 novas tentativas, 8 voltaram a falhar; 17 nós recuperados. **0 de 58 resumos recusaram o próprio objectivo** (lidos à mão) | `plan-e2e-v0150p-*` |
| 2026-10-07 | Causa das falhas: as respostas dos 34 runs falhados das séries `v0150r` e `v0150p`, lidas por `GET /runs/{id}/reconstruct` com autorização do dono | Em **33 de 34** o texto inteiro do turno é uma tool call escrita como texto, com a tool e o argumento certos, em mais de dez notações inventadas (marcação com nomes como `functions.<tool>`, `tool_call`, `invoke`, `tool_use`); 1 foi `<correct>`. Motivo de paragem `stop`, zero tool calls nativas | AOS-506 |
| 2026-10-07 | Critério da fase A1 («não cumprido» abaixo de 2% em pelo menos 40 planos com a recuperação ligada) | **Não cumprido:** 2,5% (1.0.0, 40 planos) e 3,3% (1.1.0, 60 planos) | esta tabela |
| 2026-10-07 | Critério de ligar a 1.1.0 (AOS-504) | (a) recusa do objectivo: cumprido, 0 em 58 lidos à mão; (b) primeiras falhas no máximo 16 em 60: **não cumprido**, 19 em 60. Ligada na mesma por decisão do dono | AOS-504 |
| 2026-10-07 | Estado de produção com a v0.1.50 (substituído pela última linha desta tabela) | v0.1.50; `AOS_COMPLETION_VERDICT=enforce`, `AOS_ORQ_SAIDA_POR_REFERENCIA=on`, `AOS_ORQ_NOVA_TENTATIVA=on`, `AOS_RUN_RETRY_MAX=2`, `AOS_MODEL_PROJECTION_VERSION=1.1.0`; `AOS_MODEL_ROUTE_GOVERNANCE` por ligar. O modelo real por trás do alias é o Kimi (`kimi-for-coding`) | servidor |
| 2026-10-07 | Série `v0151a`: v0.1.51, recuperação em `on`, projecção 1.1.0, **aviso ligado** | 58 planos (2 das 60 submissões não saíram do cliente): 56 com código 0, 2 com código 13 (3,4%). 19 primeiras tentativas sem tool call (33%); 22 novas tentativas, **3 voltaram a falhar (14%; sem aviso, 8 de 25, 32%)**; 19 nós recuperados em 19, 16 à segunda tentativa e 3 à terceira. **0 planos falhados por tool não chamada.** 0 de 56 resumos recusaram o próprio objectivo (lidos à mão) | `plan-e2e-v0151a-*` |
| 2026-10-07 | O nó na série `v0151a` | 22 tentativas admitidas, as 22 com aviso, 0 recusadas, `aos_runs_retry_prompt_hash_diferente_total` = 0 | `/metrics` do nó |
| 2026-10-07 | As 2 falhas da série `v0151a` | As duas no nó de resumo, que não tem tools, com `empty_output`: um turno, `stop_reason=stop`, 161 e 77 tokens de saída, e texto vazio. Hipótese **não confirmada**: o conteúdo veio no campo de raciocínio, que o adaptador não lê (matéria da fase A2) | `plan-e2e-v0151a-1791385126~n2`, `plan-e2e-v0151a-1791385255~n2` |
| 2026-10-07 | Série `v0151b`: v0.1.51, recuperação em `on`, **projecção 1.2.0**, aviso ligado | 62 planos: 61 com código 0, 1 com código 13 (**1,6% «não cumprido»**). **1 primeira tentativa sem tool call (1,6%)**; 1 nova tentativa, recuperada. 0 planos falhados por tool não chamada. 0 de 61 resumos recusaram o próprio objectivo (lidos à mão), e os 61 contêm os três factos do documento (gVisor, 89 pods, Kimi). A única falha é `empty_output` no nó de resumo, com o padrão da série `v0151a`. Tempo médio por plano: 40 s (45 a 47 s nas séries anteriores) | `plan-e2e-v0151b-*` |
| 2026-10-07 | Critério da fase A1 («não cumprido» abaixo de 2% em pelo menos 40 planos com a recuperação ligada) | **Cumprido pela primeira vez, na série `v0151b`:** 1 em 62 (1,6%). Ressalva: a causa que resta (`empty_output`) não é coberta pela recuperação, e 1 plano a mais na mesma série dava 3,2%. A metade «uma troca de modelo por baixo é detectada» não se mediu nestas séries | esta tabela |
| 2026-10-07 | Critério de ligar do AOS-506 (pelo menos 60 planos, menos de 10% de falhas à primeira tentativa, zero recusas lidas à mão, «não cumprido» abaixo de 2%) | **Cumprido para a combinação 1.2.0 com aviso** (série `v0151b`: 62 planos, 1,6%, 0 em 61, 1,6%). O aviso sozinho (série `v0151a`) não o cumpre, nem era de esperar: não actua na primeira tentativa (33%), e o «não cumprido» ficou em 3,4%. **Desvio declarado:** o ticket previa cada parte ligada sozinha; a 1.2.0 foi medida com o aviso ligado, por decisão do dono (§4) | AOS-506 |
| 2026-10-07 | O nó na série `v0151b` (o nó foi recriado antes da série: o contador é só dela) | `aos_runs_retry_admitted_total` = 1, igual à 1 tentativa do `aos-orq`; 0 recusadas; `aos_runs_retry_prompt_hash_diferente_total` = 0 | `/metrics` do nó |
| 2026-10-07 | Plano de três passos, com um nó com tools e `consumes` (v0.1.51) | 3 nós, **código 13** em 82 s. `n1` (`doc_read`, sem `consumes`): completo. `n2` (`doc_read` e `consumes` de `n1`): turno 1 com `stop_reason=tool_calls`, 1 chamada pedida por function call nativa, **negada pelo Reference Monitor** — `tool.call.denied`, `code=E_DENIED_BY_HOOK`, `denied_by=taint`, `capability=cap:fs.read`, contexto `taint=untrusted`; run `failed`, `contract_unmet_after_denial` (`doc_read`: 1 pedida, 0 efectivas, 1 negada). `n3`: `entrada_por_cumprir`. **O `aos-orq` não pediu nova tentativa** | `plan-e2e-v0151t-1791401536` |
| 2026-10-07 | Série `v0151g`: 20 planos na v0.1.51, projecção 1.2.0, aviso ligado, rota sob governação em `observe` | 20 com código 0. `aos_model_route_checks_total{result="igual",served="openai/kimi-for-coding"}` = 60; todas as outras séries a zero (`diferente` 0, `nao_reportado` 0). 0 primeiras tentativas sem tool call em 20, 0 novas tentativas. Tempo médio por plano: 39 s. O proxy de produção (`litellm:main-stable`) envia os cabeçalhos em todos os turnos | `plan-e2e-v0151g-*`; `/metrics` do nó |
| 2026-10-07 | Acumulado com a projecção 1.2.0 (séries `v0151b` e `v0151g`) | 82 planos: 1 primeira tentativa sem tool call (1,2%), recuperada; 1 plano sem cumprir (1,2%), por `empty_output`. O plano `v0151t` fica fora desta conta (saiu 13 por política) | esta tabela |
| 2026-10-07 | Critério da fase A1, as duas metades | «Não cumprido» abaixo de 2%: cumprido (`v0151b` 1 em 62; `v0151g` 0 em 20). Troca de modelo por baixo: detectável na forma estreita, com a comparação por turno a correr em produção em `observe` (60 em 60 `igual`); nenhuma troca real ocorreu em produção, a detecção foi provada com o proxy real em `make ci-rota-live`. **Fase fechada pelo dono a 2026-10-07**, com os resíduos da §7 | esta tabela |
| 2026-10-07 | Estado de produção | v0.1.51 (`ghcr.io/albinojimy/aos-node@sha256:a3d06030ad173736eef94dd1943caf38605058b38099f3571d139edcf55b7e29`; PR #454, release 37636422928). `AOS_COMPLETION_VERDICT=enforce`, `AOS_ORQ_SAIDA_POR_REFERENCIA=on`, `AOS_ORQ_NOVA_TENTATIVA=on`, `AOS_RUN_RETRY_MAX=2`, `AOS_RUN_RETRY_NOTICE=on`, `AOS_MODEL_PROJECTION_VERSION=1.2.0`, `AOS_MODEL_ROUTE_GOVERNANCE=observe`, `AOS_MODEL_ROUTE_API_HOST=api.kimi.com`. Perfil da rota no banner: `gpt-4o-mini` pedido, `openai/kimi-for-coding` esperado. O modelo real por trás do alias é o Kimi (`kimi-for-coding`). Cópias do `.env` de cada passo em `/opt/aos`: `.env.antes-observe-tentativa-20261007`, `.env.antes-on-tentativa-20261007`, `.env.antes-projeccao-110-20261007`, `.env.antes-aviso-20261007`, `.env.antes-projeccao-120-20261007`, `.env.antes-rota-observe-20261007` | servidor |

Por medir: a projecção 1.2.0 sem o aviso (não se mediu, por decisão do dono); um nó com tools e
`consumes` recuperado por nova tentativa (não realizável em produção com a política de hoje,
§3); o AOS-505 em `enforce`; a causa do `empty_output` (a hipótese do campo de raciocínio foi corrigida pela leitura do código — o adaptador lê `reasoning_content` —, e a causa continua por medir: AOS-507); se a
notação de cabeçalhos e linhas de fim induz o modelo a imitar marcação (hipótese, não testada
isoladamente); o que o provider real devolve sobre o modelo que serviu; e o critério da fase A0,
cuja contagem dos 150 runs recomeça desde a projecção 1.1.0.

## 6. Matriz de suporte por classe de modelo

Estados: **qualificada** (com rota e data), **desenhada e não testada**, **não suportada**.

| Classe | Estado | Rota e data | Fase que a qualifica |
|---|---|---|---|
| Tool calling nativo, sem estado opaco exigido | qualificada, com defeito conhecido (verde falso em cerca de 1 em 5 planos) | Kimi por LiteLLM, 2026-10-04 | A0 fecha o defeito |
| Tool calling nativo com raciocínio a devolver | não suportada | — | A2 |
| Tool calling nativo com assinaturas por chamada | não suportada | — | A2 |
| Tool calling em texto convertido pelo servidor | desenhada e não testada | — | A2, A3 |
| Sem tool calling | não suportada para nós com tools | — | depois de A3, só com um modelo concreto |
| Multimodal de entrada | não suportada | — | A5 |
| Multimodal de geração | não suportada | — | A6 |
| Streaming do modelo em runs | não suportada | — | sem fase |

## 7. Resíduos declarados

Não fechados por nenhuma fase até decisão em contrário:

- Tool call em texto num turno posterior a uma chamada efectiva.
- Tool call em texto no primeiro turno. **Medido a 2026-10-07: é a causa de 33 de 34 falhas** por
  «não chamou a tool». O contrato de conclusão apanha-a (o run fecha `failed`), a nova tentativa
  recupera a maior parte, e o AOS-506 actua sobre o que se diz ao modelo. Não se interpreta o
  texto: fica fora da arquitectura-alvo (§8). **Medido a 2026-10-07, na v0.1.51:** com a 1.2.0 e
  o aviso, 1 primeira tentativa em 62 acabou sem tool call, e nenhum plano falhou por isso em
  120 (séries `v0151a` e `v0151b`). O resíduo continua aberto: a taxa desceu, a classe não
  fechou.
- Nó sem tools que devolve texto vazio (`empty_output`). **Medido a 2026-10-07: 3 em 120
  planos** (2 em 58 e 1 em 62), sempre no nó de resumo, com um turno, motivo de paragem `stop` e
  tokens de saída contados. É a única causa de «não cumprido» que resta nas séries da v0.1.51.
  O plano sai 13, e portanto não é um verde falso; a recuperação não o cobre, porque o nó não
  tem contrato de conclusão. Passou à fase A2. **Corrigido a 2026-10-07 pela leitura do
  código:** a hipótese «o conteúdo veio no campo de raciocínio, que o adaptador não lê» está
  errada nessa forma — o adaptador lê `reasoning_content` e grava-o na captura; não lê os outros
  nomes do raciocínio. Oito formas de resposta dão este mesmo registo (desenho A2, §2.2), e não
  há dados para escolher. O AOS-507 mede a causa; o AOS-510 e o AOS-511 repetem o nó; o
  raciocínio nunca é usado como resposta (decisão D3).
- **Risco de desenho aberto, não provado:** os separadores `<kind>` e `</kind>` do protocolo
  podem ser a causa de fundo da tool call escrita como texto — o modelo imitaria a marcação que
  lê. A 1.2.0 corrige com uma instrução e mantém os separadores; a hipótese não foi testada
  isoladamente.
- **Limite de produto: um nó com tools e `consumes` é negado pelo gate de taint.** Medido a
  2026-10-07 (`plan-e2e-v0151t-1791401536`): o nó que recebeu `plan_input` pediu `doc_read` e o
  Reference Monitor negou, com `cap:fs.read` armada (ADR-034, fase 1). Um plano em que um passo
  usa uma tool protegida com base no que outro passo leu falha sempre, com causa nomeada
  (`contract_unmet_after_denial`), e a recuperação não o alcança. Levantá-lo exige a forma
  forte do ADR-005 (opção A, dual-LLM) ou aprovação humana. Fora da fase A1.
- A rota sob governação corre em `observe` e não em `enforce`: uma troca de modelo seria contada
  e não recusada, e o cron do `alerta-rota.sh` está por instalar. A forma estreita mantém-se:
  não se detecta uma troca feita pelo provider por trás do mesmo nome e endpoint.
- O texto do protocolo foi afinado para um só modelo (o Kimi). Nada mede o que a 1.2.0 faz
  noutro modelo, e a promessa da §1 pede o contrário. O encaminhamento proposto está na §4, por
  decidir.
- Tool chamada e saída fabricada, em nós que transformam conteúdo. **Medido a 2026-10-05: 2 em 21 planos** perderam factos do documento no nó de leitura. Só a saída por referência o fecha, e só para os nós de passagem directa (fase A0.5, AOS-497 a AOS-501).
- Nó sem tools que conclui a dizer que não conseguiu. **Medido a 2026-10-05: 1 em 21 planos**, por o nó tratar o próprio objectivo como dados untrusted — o texto do protocolo nativo pode estar a induzi-lo. O AOS-504 corrige a causa provável e conta os casos; não o detecta nem o recupera.
- O `prompt_hash` não cobre a projecção nativa.
- Run que acaba sobre uma recusa ou uma falha de tool com o contrato cumprido, ou sem contrato:
  veredicto positivo. É medido (`aos_runs_finished_by_last_tool_outcome_total`), não é fechado.
- Retoma por crash depois de uma falha de tool: a re-hospedagem não tem credencial, a tool é
  negada na segunda vida e, em imposição, o run sela `contract_unmet_after_denial` de uma chamada
  que tinha sido permitida. Anterior ao AOS-493; passou a decidir o desfecho.
- O replay de um run retomado cujo turno re-executado mudou de desfecho pára em divergência de
  `prompt_hash` e não reproduz veredicto nenhum. Anterior ao AOS-493.
- Depois de um reinício do nó, a saída de um run concluído só se lê com o gate soberano de
  leitura e com a custódia das KEK no Vault. Sem isso o `GET /runs/{id}` responde
  `output_unavailable` (AOS-494), e o nó do plano que a esperava fica falhado. Com o Vault
  ainda selado a resposta é 503 e o `aos-orq` volta a ler; mas um `serve` que **retome** o
  plano nessa janela não reidrata a saída do produtor e fecha o consumidor
  (`entrada_por_cumprir`, regra do AOS-418).
- O `/dsar/erase` não limpa o registo de desfechos em memória do nó: o `GET /runs/{id}` continua
  a servir o `final_text` de um titular apagado até ao reinício ou à poda. Visto na revisão do
  AOS-494, não reproduzido, sem ticket.

### O que a série de 2026-10-05 muda na ordem das fases

Em 21 planos, quatro saíram verdes sem cumprir o objectivo por inteiro. A imposição de A0 apanha
um deles. Os outros três são os dois resíduos acima, agora com taxa. Proposta ao dono, por decidir:
antecipar a **saída por referência** para logo a seguir a A0, e tratar a recusa do próprio objectivo
na higiene do texto do protocolo (parte de A1).

Achado operacional da mesma série: as unidades do systemd da drenagem no servidor não foram
reinstaladas depois do AOS-447. O temporizador corre de 5 em 5 minutos (devia ser de minuto a
minuto) e a unidade não tem `DRENAR_MAX=1`. Exige root.

## 8. Fora da arquitectura-alvo

- O Jev como dependência de produção.
- Qualquer juiz probabilístico a decidir sucesso, terminação ou qualificação de um modelo.
- Um parser tolerante que converta texto do modelo em tool calls.
- `tool_choice: required` como fundação.
