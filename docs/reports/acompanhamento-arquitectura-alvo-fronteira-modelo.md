# Acompanhamento — arquitectura-alvo da fronteira runtime↔modelo

> Documento vivo. É a fonte única do estado da arquitectura-alvo aceite pelo dono a 2026-10-04.
> **Regra de actualização:** o PR que muda o estado de um ticket desta lista, que mede um critério
> de prova ou que regista uma decisão do dono actualiza este ficheiro no mesmo commit. Um estado
> aqui que não bata com o ticket na EPIC é um defeito do PR.

Última actualização: 2026-10-11 (**AOS-517 implementado no repositório — o compose de produção refere o proxy pelo digest em que se mede, com a igualdade presa por teste; os passos do servidor estão por fazer, pelo dono**; **fase A3: tickets AOS-517 a AOS-523 abertos**, os sete do desenho `desenho-a3-entrada-automatica-2026-10-11.md`, escritos com as recomendações D1 a D9 do desenho, que o dono aceitou todas no mesmo dia (§4); nada implementado — §2 e §3). Antes, no mesmo dia (**fase A2 fechada por decisão do dono, com os resíduos nomeados; abre a fase A3.** A A2 fica provada em ensaio, não em produção: o Claude, pela OpenRouter, completou 21 de 21 runs do banco de ensaio com o raciocínio devolvido, 15 de 15 segundos turnos com tools aceites; o controlo negativo mostrou que a OpenRouter não exige o raciocínio de volta, pelo que a devolução **obrigatória** só está provada com provider falso. A experiência dos separadores correu com o Kimi real e retirou a hipótese. v0.1.53 em produção desde 2026-10-10, com tudo o que é da A2 desligado. Desenho da A3 feito, `desenho-a3-entrada-automatica-2026-10-11.md`, com as decisões por tomar e sem tickets abertos — §2, §3, §4, §5 e §7). Antes, a 2026-10-08 (fase A2: AOS-507 a AOS-511 implementados, revistos e fundidos — saem na v0.1.52, com os interruptores desligados; **AOS-512 a AOS-516 abertos** (banco de ensaio, perfil de rota, estado opaco, projecção que o devolve, segunda família); chaves e tectos do posto de ensaio fornecidos pelo dono; regras dos fornecedores consultadas e citadas nos tickets; por decidir pelo dono: o nome do modelo e a região da segunda família — §3 e §4). Antes, a 2026-10-07 (fase A1, v0.1.51 em produção: aviso na nova tentativa e projecção 1.2.0 ligados (AOS-506); duas séries medidas, `v0151a` e `v0151b`; o critério «não cumprido abaixo de 2%» foi cumprido pela primeira vez na série `v0151b` — 1 em 62 planos, 1,6% —, com a ressalva de que a única causa que resta, `empty_output` num nó sem tools, não é coberta pela recuperação. As duas verificações que faltavam correram no mesmo dia: o plano de três passos (`v0151t`), em que o Reference Monitor negou por taint a tool do nó com `consumes` e não houve nova tentativa; e o AOS-505 em `observe` (série `v0151g`, 20 planos, 60 turnos com o modelo servido igual ao esperado). **Fase A1 fechada a 2026-10-07, por decisão do dono, com os resíduos nomeados. Fase A2 aberta no mesmo dia: desenho feito, decisões D1 a D3 tomadas, D4 e D5 por tomar, AOS-507 a AOS-511 abertos, e o resto planeado, por numerar**).

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
| **A2** | Estado opaco do provider por turno (raciocínio, assinaturas, identificadores), com sondas de protocolo deterministas | Duas famílias de modelos completam runs com tools | A0; escolha da segunda família | **fechada** (2026-10-11, por decisão do dono, com os resíduos nomeados). **Provada em ensaio, não em produção.** Duas famílias completam runs com tools: o Kimi em produção (82 planos com a projecção 1.2.0, 1,2% de falhas à primeira; série `v0153a`, 5 de 5), e o Claude (`anthropic/claude-sonnet-4.5`) no banco de ensaio, pela OpenRouter, com o raciocínio devolvido: 21 de 21 runs cumpridos à primeira, 15 de 15 nós com tools com o segundo turno aceite, 15 de 15 pedidos com estado aceites, 0 recusas. Resíduos nomeados (§7): a devolução **obrigatória** só está provada com provider falso (a OpenRouter não a exige, e a API directa da Anthropic não foi corrida); 15 nós com tools e não os 40 do critério P3; nenhuma rota de produção declara `params`, `devolver` nem `devolver_em`, e a captura do estado está por ligar; a causa do `empty_output` por confirmar (0 ocorrências desde a ficha) e a repetição do passo vazio ainda em `observe`; o Claude sem região UE, só em ensaio; o replay (P6) por medir com modelo real. Critérios um a um na §3. Histórico: 2026-10-07: desenho feito, `desenho-a2-estado-opaco-2026-10-07.md`; decisões D1 a D5 tomadas; AOS-507 a AOS-511 implementados e revistos, na v0.1.52, desligados; a 2026-10-08 o dono forneceu as chaves e os tectos do posto de ensaio, e abriram-se AOS-512 a AOS-516 — o banco de ensaio, o perfil de rota, o estado opaco, a projecção que o devolve e a segunda família — §3). Herda da A1 o `empty_output` (§7): a única causa de «não cumprido» que resta nas séries da v0.1.51 |
| **A3** | Entrada automática: arnês de qualificação, perfil do modelo como artefacto do registo, mais de um modelo por nó, canary, disjuntor | O terceiro modelo entra com zero PRs e uma assinatura em menos de uma hora; um modelo mau é recusado sozinho | A1, A2 | **em curso — tickets AOS-517 a AOS-523 abertos** (aberta pelo dono a 2026-10-11: desenho feito, `desenho-a3-entrada-automatica-2026-10-11.md`; os sete tickets do desenho abertos no mesmo dia, com as recomendações D1 a D9; nada implementado — §3). O banco de ensaio que a §4 propunha antecipar já existe desde a A2 (AOS-512); falta-lhe ser um arnês que emite um veredicto assinável. Herda da A2 os resíduos (a), (c), (e) e (h) da §7 |
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
| AOS-507 | EPIC-06 | A forma da resposta do provider fica registada em cada turno, em vocabulário fechado e sem conteúdo (`AOS_MODEL_RESPONSE_SHAPE=off\|observe`, omissão `off`) | AOS-491 | **Implementado e revisto; fundido (#457); sai na v0.1.52, desligado.** Detalhe: implementado, desligado (2026-10-07): ficha em `response_shape` do `turn.recorded` e `aos_model_response_shape_total` (342 séries no máximo). Revisto a 2026-10-08, sem bloqueantes; corrigido: o digest não cobre chaves escritas pelo modelo, e um campo de raciocínio vazio dá `vazio`. **Pela rota de produção:** a primeira corrida do `ci-wire-live` dizia que o proxy retira `refusal`, `thinking` e `reasoning_details`, e que a ficha não separava por isso H2-`thinking`, H2-`reasoning_details` e H5 de H6; a segunda (2026-10-08) mostrou que o proxy os MOVE para `message.provider_specific_fields`, com o valor, e a ficha passou a lê-los lá (`psf_refusal`, `psf_reasoning`): H1 a H8 continuam separadas depois do proxy, preso por teste sobre os corpos entregues. `reasoning_tokens` e as assinaturas sobrevivem ao proxy (medido). Invisível por esta rota: o `finish_reason` bruto, que o proxy normaliza. **Medição de 2026-10-08 sobre as capturas seladas dos 3 `empty_output`, sem decifrar (tamanho do criptograma):** 852, 450 e 628 bytes para 161, 77 e 114 tokens de saída — 5,3 a 5,8 bytes por token, contra 3,5 a 4,8 (mediana 4,1 a 4,2) nos 117 resumos bem-sucedidos: a resposta veio no raciocínio com `content` vazio; H1 ou H2-`reasoning` é a hipótese fortemente apoiada, não provada. A ficha em `observe` confirma-o numa série. Produção e critério P1 por verificar |
| AOS-508 | EPIC-06 | Providers falsos de wire para CI, e um gate opcional que os põe atrás da imagem real do proxy | AOS-505 | **Implementado e revisto; fundido (#457); sai na v0.1.52 (só CI e um gate opcional).** Detalhe: implementado (2026-10-07): 87 casos e linha de base em `internal/wirefake`; uma corrida do `ci-wire-live` em `docs/reports/wire-live-aos508-2026-10-07.md` (o proxy copia `reasoning` para `reasoning_content`, move `thinking`, `reasoning_details` e `refusal` para `provider_specific_fields`, e dá 500 a `content` em partes). Os corpos que o proxy ENTREGOU ficaram congelados como casos (`casos_pos_proxy`), ao lado dos falsos de provider. A matriz de suporte por referir os casos |
| AOS-509 | EPIC-06 | O gateway deixa de recusar a resposta inteira por `content` em partes de texto e por `arguments` em objecto, e lê os outros nomes do raciocínio como raciocínio — nunca como resposta. Sem interruptor | AOS-507, AOS-508 | **Implementado e revisto; fundido (#457); sai na v0.1.52 (sem interruptor: só alcança respostas que hoje dão erro).** Detalhe: implementado (2026-10-07): os 69 casos que já davam um turno ficam byte a byte, com a excepção declarada (24 casos, só o raciocínio); `aos_model_response_rejected_total{causa}` (existe no `/metrics` mesmo com tudo desligado). Revisto a 2026-10-08, sem bloqueantes; corrigido: chaves repetidas lêem-se como na base, `type` repetido recusa, valores vazios não são raciocínio. **Pela rota de produção é quase inerte** (o proxy dá 500 a `content` em partes e a raciocínio em objecto, e entrega `arguments` em string): o ganho real é `thinking_blocks` em lista e a segunda família. `content: []` passa de erro de descodificação a `empty_output` (interessa ao AOS-510). Produção por verificar |
| AOS-510 | EPIC-19 | O nó aceita a nova tentativa de um nó do plano que fechou `empty_output`, com prova própria no seu log (`AOS_RUN_RETRY_EMPTY=off\|on`, omissão `off`). Emenda o ADR-039. A tentativa não leva aviso | AOS-502, AOS-506 | **Implementado e revisto; fundido (#458); sai na v0.1.52, desligado.** Detalhe: implementado e desligado por omissão (2026-10-07); revisto de forma independente a 2026-10-08 (zero bloqueantes; diferencial base×HEAD nulo); por fazer o smoke sobre JetStream e por verificar em produção. Medição de 2026-10-08 que sustenta a classe: nas capturas seladas dos 3 runs `empty_output` (tamanho do criptograma, sem decifrar) há 5,3–5,8 bytes por token de saída, contra 3,5–4,8 nos 117 resumos bem-sucedidos ⇒ o conteúdo veio no raciocínio com `content` vazio (hipótese fortemente apoiada, não provada; a ficha do AOS-507 confirma-a). Repetir o passo é o tratamento certo, e a D3 mantém-se |
| AOS-511 | EPIC-19 | O `aos-orq` volta a submeter um nó do plano que fechou `empty_output` (`AOS_ORQ_NOVA_TENTATIVA_VAZIA=off\|observe\|on`, omissão `off`; os mesmos tectos, 2 por nó e 4 por plano) | AOS-510, AOS-503 | **Implementado e revisto; fundido (#458); sai na v0.1.52, desligado.** Detalhe: implementado e desligado por omissão (2026-10-07); revisto de forma independente a 2026-10-08 (zero bloqueantes; binário da base × HEAD: só a duração difere); por verificar em produção (`observe` primeiro). Antes de `on`: o recuo de IMAGEM do `aos-orq` com uma tentativa por vazio registada contamina as séries (runbook: interruptores a `off`, planos drenados, só depois a imagem) |

**Abertos a 2026-10-08: AOS-512 a AOS-516.** Eram o trabalho planeado, por numerar, do desenho
§6 («A2-banco», «A2-perfil», «A2-estado», «A2-projecção», «A2-família»). O dono tomou as decisões
D4 e D5 a 2026-10-07 e forneceu as chaves e os tectos a 2026-10-08 (§4). Nenhum foi agrupado:
cada rótulo deu um ticket, porque cada um tem um interruptor e um critério de prova próprios, e
o estado opaco entra em duas metades de propósito — primeiro às escuras, depois a devolver.

| Ticket | Epic | Rótulo do desenho | Título curto | Depende de | Estado |
|---|---|---|---|---|---|
| AOS-512 | EPIC-08 | «A2-banco» | Banco de ensaio mínimo: bateria de casos sintéticos contra uma rota, relatório de taxas sem texto das respostas; três modos (falsos em CI, proxy real com falsos, modelo real pelo posto local com tectos diários e contador persistente); primeira corrida, a experiência dos separadores, em quatro braços de 53 pedidos | AOS-506, AOS-507, AOS-508; D5 | **implementado** (2026-10-08): `packages/qa/banco-ensaio`, binário `aos-ensaio`; modo de falsos em CI com taxas exactas, modo proxy verde em local com a imagem de produção, modo real ensaiado só contra um fornecedor falso. Revisão adversarial feita a 2026-10-08, sem bloqueantes, e os seus achados corrigidos (proxy com vigia e varredura de órfãos, destino da chave validado e mostrado, exclusão entre processos no contador, tecto sem contornos, teste exacto de Fisher com correcção de Holm). **Experiência dos separadores feita a 2026-10-10 com o Kimi real** (212 pedidos e a sonda, todos com 200; nenhum braço se distingue a 5%; hipótese retirada — §5). Na v0.1.53 (o banco não faz parte do nó) |
| AOS-513 | EPIC-06 | «A2-perfil» | O perfil da rota declara parâmetros do pedido (lista fechada), a versão da projecção por rota e a classe de estado; inerte sem perfil que os declare; um perfil qualifica-se no banco antes de ser ligado | AOS-505, AOS-506, AOS-507, AOS-512 | **implementado (2026-10-08), por rever; inerte** — nenhum perfil da tabela declara os campos novos. Medido atrás do proxy fixado: numa rota `openai/…` o proxy recusa `thinking` e `reasoning_effort` (400) com `drop_params: false`, e retira-os em silêncio com `true`; `reasoning_effort` só passa com `allowed_openai_params`. **Em produção desde a v0.1.53 (2026-10-10), inerte.** Um perfil com `params` foi exercitado com modelo real só no banco, pela OpenRouter (`reasoning: {effort: medium}`, AOS-516). Por fazer: a medição com o Kimi real |
| AOS-514 | EPIC-06 (e EPIC-02) | «A2-estado» | **ADR-040** e transporte: o estado opaco do turno — raciocínio em todos os nomes, blocos assinados e redigidos, id e assinatura de tool call do provider — capturado selado na captura do turno, byte a byte, e referido no tail por digest (layout 1.5.0, rótulo `state_digest`). Às escuras: nada é reenviado. `AOS_MODEL_PROVIDER_STATE=off` por omissão; tecto por turno, sem truncar | AOS-507, AOS-508, AOS-509 | **implementado e revisto (2026-10-08, sem bloqueantes); desligado por omissão; `capture` condicionado ao AOS-515 e ao smoke JetStream**. Em produção desde a v0.1.53, desligado: `AOS_MODEL_PROVIDER_STATE=capture` continua por ligar, e o smoke sobre JetStream com o estado no tecto (ADR-040 §2.10) por correr — resíduo (c) da §7 |
| AOS-515 | EPIC-06 | «A2-projecção» | Projecção nativa 1.3.0: devolve o estado ao provider só à rota que o produziu e só se o perfil o exigir; estável por prefixo; a decisão D3 é critério de aceitação | AOS-513, AOS-514; AOS-508, AOS-512 | **implementado (2026-10-08), por rever; inerte** — a junção é pelo `state_digest` com verificação (condição do AOS-514 cumprida); provado com falsos que exigem e que proíbem o estado; atrás do proxy fixado o estado chega ao provider nas rotas `openai/…` e `anthropic/…`. **Em produção desde a v0.1.53, inerte; exercitada com modelo real no banco a 2026-10-10** (projecção 1.3.0, 15 de 15 pedidos com estado aceites pela OpenRouter — §5). Por fazer: um run com devolução no nó composto, incluindo um retomado, e o runbook da rota `obrigatorio`. O bloco do ticket tem o marcador de menção de ADR e por isso não aparece na RTM como implementador do ADR-040 — resíduo (g) da §7 |
| AOS-516 | EPIC-06 | «A2-família» | Qualificação da segunda família: o Claude pelo mesmo proxy, primeiro no banco, depois em produção só por decisão do dono. Fecha os critérios P3 e P4 | AOS-512 a AOS-515; D4; a decisão da região | **fechado com a fase, a 2026-10-11, por decisão do dono; o passo 3 (produção) não se fez.** Passo 1 (atrás do proxy fixado, com falsos): feito. Passo 2 (modelo real): feito a 2026-10-10 **pela OpenRouter** e não pela API directa da Anthropic, que não tem créditos — 21 de 21 runs cumpridos à primeira com o raciocínio devolvido, 15 de 15 segundos turnos aceites, veredicto `cumprida` nas duas corridas; o controlo negativo (`devolver: nunca`) também passou, logo a OpenRouter não exige o raciocínio de volta (§5). O perfil da rota ganhou `devolver_em` e `params.reasoning` (porta 1.10.0, PR #467, fundido depois da tag v0.1.53 e ainda sem release). **Ficou por fazer, e passa à §7:** a corrida pela API directa da Anthropic (a devolução obrigatória com fornecedor real e o bloco de texto que o proxy insere), o controlo negativo com a assinatura alterada, P6, e o passo de produção |

**O que depende do dono.**

| O quê | Para quê | Estado |
|---|---|---|
| Ficheiro de chaves fora do repositório (`%USERPROFILE%\.aos-ensaio\chaves.env`), com as chaves do Kimi e da Anthropic | O modo com modelo real do AOS-512, e os passos 2 do AOS-513 e do AOS-516. O posto lê o ficheiro pelo caminho e nunca imprime nem regista os valores | fornecido (2026-10-08) |
| Tectos diários: 1000 pedidos por fornecedor e 5 USD na Anthropic | O contador do AOS-512: atingido o tecto, a corrida pára | fornecidos (2026-10-08) |
| O nome do modelo da Anthropic (`ANTHROPIC_MODELO`) | A corrida do AOS-516 no banco | ultrapassado (2026-10-10): a corrida fez-se pela OpenRouter, com `anthropic/claude-sonnet-4.5`; a conta directa da Anthropic não tem créditos |
| A região da segunda família | O passo de produção do AOS-516. Pela API directa da Anthropic a inferência não se fixa na UE (`inference_geo` só aceita `global` ou `us`; consultado a 2026-10-08), e produção está selada para `eu-west` | **por decidir** para produção; a fase fechou com a opção (a) em vigor: o Claude só no banco, com documentos de teste. Passa à A3 (decisão D9 do desenho) |
| Assinar um perfil de rota com parâmetros, e a allowlist com uma rota nova | Ligar em produção o AOS-513 e o AOS-516 | depois do banco |

Não dependem das chaves: o modo de falsos e o de proxy real com falsos do AOS-512, o AOS-513
(até à qualificação), o AOS-514 e os testes do AOS-515 contra os falsos.

**Regras dos fornecedores, consultadas a 2026-10-08** (as fontes estão nos tickets):

- *Confirmado* — a Anthropic exige, ao devolver o resultado de uma tool, os blocos de raciocínio
  completos e sem alteração, incluindo os redigidos (`redacted_thinking`) e os de texto vazio
  com assinatura; um bloco alterado dá 400; em modelos recentes o bloco só é aceite com o
  prefixo da conversa inalterado (AOS-514, AOS-515).
- *Confirmado* — o proxy expõe `reasoning_content` e `thinking_blocks` (com assinatura), e
  liga o raciocínio por `reasoning_effort` ou `thinking`; o modelo leva o prefixo `anthropic/`
  (AOS-513, AOS-514).
- *Confirmado* — no Kimi, o `kimi-k2.6` aceita desligar o raciocínio, o `kimi-k2.7-code` não, e
  o `kimi-k3` raciocina sempre; a página manda devolver o `reasoning_content` dentro de um
  ciclo de tools, o que em produção não é exigido (AOS-513, AOS-514).
- *Por confirmar, a medir no banco* — o que o `kimi-for-coding` aceita; o que a Anthropic
  responde quando os blocos **faltam** (400 ou raciocínio desligado em silêncio); a regra do id
  de tool call; a forma de um bloco redigido atrás do proxy; o que a versão fixada do proxy
  faz; o identificador e o preço do modelo; um endpoint da UE num parceiro.

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
fica (saída 13), alguns segundos mais tarde.

Ordem de entrega dos tickets abertos a 2026-10-08: o **AOS-512** primeiro (falsos em CI, depois
o posto local e a experiência dos separadores na rota de produção de hoje); o **AOS-513** e o
**AOS-514** a seguir, em paralelo — os dois entram inertes; depois o **AOS-515**, que só começa
com o ADR do AOS-514 escrito e com os «por confirmar» medidos no banco; por fim o **AOS-516**:
passo 1 atrás do proxy, passo 2 no banco com o modelo real, passo 3 em produção só por decisão
do dono. O nó sai antes do `aos-orq`. Tudo entra desligado ou aditivo.

**O que as corridas de 2026-10-10 mostraram.**

*A experiência dos separadores* (Kimi real, `kimi-for-coding`, chave do plano). 212 pedidos e a
sonda, todos com 200, sem nenhum limite de ritmo. Falhas de tool call à primeira: 0 de 53 com a
1.2.0 e `<kind>`, 0 de 53 com `[[kind]]`, 2 de 53 sem linhas de fim, 1 de 53 com a 1.0.0.
Nenhuma comparação distingue a 5%. As três falhas têm o nome da tool no texto. A forma dos
separadores não é a causa da tool call escrita como texto: a hipótese sai da §7. O limite é o
da experiência — um caso (T1) e um turno.

*O Claude pela OpenRouter* (`anthropic/claude-sonnet-4.5`, raciocínio ligado, devolução
`obrigatorio` em `topo`, projecção 1.3.0). Duas corridas, 38 pedidos, todos com 200: 21 de 21
runs cumpridos à primeira, 15 de 15 nós com tools com o segundo turno aceite, 21 turnos com
raciocínio ou assinatura capturados, 15 de 15 pedidos com estado aceites, nenhuma recusa.

*O controlo negativo* (o mesmo modelo e os mesmos parâmetros, com `devolver: nunca` e a
projecção 1.2.0) passou da mesma maneira: 7 de 7 runs, 5 de 5 segundos turnos aceites. **A
OpenRouter não exige o raciocínio de volta.** O veredicto `cumprida` prova, portanto, que
devolver o estado não parte nada; não prova que seja necessário, nem que o fornecedor o leia.
A exigência só foi vista com providers falsos (§5, 2026-10-08 e 2026-10-10).

**Os critérios da fase, um a um, e onde ficou cada prova.**

| # | O que ficou | Onde |
|---|---|---|
| P1 | **Não cumprido.** A ficha da forma da resposta existe, mas não houve nenhum `empty_output` desde que existe (62 e 5 planos): a causa continua sem nome. A hipótese «veio tudo no raciocínio» mantém-se fortemente apoiada, não provada | produção |
| P2 | **Por medir.** A repetição do passo vazio continua em `observe`; a série de 120 planos em `on` não correu | produção |
| P3 | **Em parte.** Kimi: 82 planos com a projecção 1.2.0, 1,2% de falhas à primeira, e 5 de 5 na série `v0153a`. Claude: 15 nós com tools chegaram ao segundo turno, 15 de 15 aceites, 0 respostas 4xx — são 15, não os 40 do critério | Kimi em produção; Claude em ensaio com modelo real |
| P4 | **Cumprido na letra, em ensaio.** Um perfil com devolução `obrigatorio`: 21 turnos com raciocínio capturado e 15 de 15 pedidos seguintes levaram-no e foram aceites. O controlo negativo mostra que este fornecedor não o exige; a exigência só está provada com provider falso | ensaio com modelo real (devolver não parte nada); ensaio com provider falso atrás do proxy real (devolver é exigido) |
| P5 | **Cumprido.** A bateria passa com os falsos e atrás da imagem fixada do proxy, e cada cenário tem o controlo negativo vermelho (sem `devolver_em`, ou com `devolver: nunca`: respostas 400 e `nao_cumprida`) | CI com falsos; ensaio com provider falso atrás do proxy real |
| P6 | **Por medir com modelo real.** Com falsos, o replay com `topo` reproduz os pedidos byte a byte (teste do AOS-516). Um run retomado com devolução no nó composto não correu; o banco não tem captura selada nem retoma | testes com falsos |
| P7 | **Cumprido, com amostra pequena.** Série `v0153a`: 5 de 5 planos concluídos à primeira, 10 de 10 runs, 15 de 15 turnos `igual`, 0 respostas vazias. Nada da A2 foi ligado em produção, pelo que a salvaguarda quase não foi posta à prova | produção |

**Fecho da fase A2 (2026-10-11, por decisão do dono).** A fase fecha **provada em ensaio, não
em produção**, com os resíduos da §7 nomeados e por resolver; nenhum deles ficou fechado pelo
fecho. Dos sete critérios, dois estão cumpridos (P5, P7), dois em parte ou só em ensaio (P3,
P4) e três por cumprir ou por medir (P1, P2, P6). O dono abriu a fase A3 no mesmo dia.

### A3 — Entrada automática

Desenho: `desenho-a3-entrada-automatica-2026-10-11.md`. Onde o desenho e os tickets divergirem,
valem os tickets.

**Abertos a 2026-10-11: AOS-517 a AOS-523.** São os sete itens do desenho §7, pela mesma ordem,
escritos com as recomendações D1 a D9 do desenho.

| Ticket | Epic | Nome no desenho | Título curto | Depende de | Estado |
|---|---|---|---|---|---|
| AOS-517 | EPIC-06 | «A3-proxy» | O proxy de produção fixa-se pelo digest em que o banco mede, com a igualdade presa por teste; `drop_params: false` aplicado no servidor. Sem interruptor: é uma mudança de deploy, feita pelo dono (D8) | AOS-505, AOS-508, AOS-512 | **implementado no repositório (2026-10-11); servidor por fazer.** O compose refere o digest; `TestAOS517_ProxyDeProducaoEOProxyEmQueSeMede` prende seis sítios e uma varredura; o runbook tem os passos do dono. Faltam os cinco critérios do servidor |
| AOS-518 | EPIC-08 | «A3-arnês» | O banco de ensaio ganha a experiência `qualificacao`: veredicto calculado sobre o modelo (`qualificado`, `recusado`, `inconclusivo`) contra os limiares L1 a L5, cinco providers falsos «maus» em CI, orçamento próprio e o digest do próprio relatório (D2, D3, D7) | AOS-512, AOS-513, AOS-516, AOS-508; AOS-517 para o digest da imagem | **aberto** |
| AOS-519 | EPIC-06 | «A3-perfil» | O nó carrega perfis de rota de uma pasta, por um registo de entrada assinado pelo dono que cita o veredicto; falha fechado no arranque, com causa própria por caso; emenda ao ADR-036 §2.8 (`AOS_MODEL_ROUTE_PROFILES_DIR` e `AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR`; sem a pasta, o binário de hoje) (D1, D8) | AOS-518, AOS-517; AOS-505, AOS-513 | **aberto** |
| AOS-520 | EPIC-06 | «A3-dois-modelos» | O nó serve um titular e no máximo um candidato, escolhido uma vez por run por uma função do identificador do plano e da fatia assinada (10% dos planos, até 40 planos); modelo fixado por run; séries novas por modelo; ADR novo, por numerar (`AOS_MODEL_CANDIDATE=off\|on`, omissão `off`) (D4, D6) | AOS-519 | **aberto** |
| AOS-521 | EPIC-06 | «A3-disjuntor» | Disjuntor por modelo: cinco sinais sem ler texto, 3 falhas nos últimos 20 runs do candidato ou o primeiro turno com o modelo servido diferente; aberto, o candidato não recebe runs novos; durável, selado, e só um registo assinado novo o reabre (`AOS_MODEL_BREAKER=off\|observe\|enforce`, omissão `off`) (D5) | AOS-520 | **aberto** |
| AOS-522 | EPIC-08 | «A3-comando» | Um comando do banco encadeia a qualificação, o resumo de uma página e a preparação do registo de entrada; sem `qualificado` não fica nada por assinar; a chave privada nunca entra no arnês; runbook com relógio | AOS-518, AOS-519 | **aberto** |
| AOS-523 | EPIC-08 | «A3-prova» | A medição do critério da fase: o terceiro modelo (outro do Kimi, pela mesma conta) entra em produção em menos de uma hora, com zero PRs e uma assinatura; o modelo mau recusado e o candidato que degrada, em ensaio (D9) | AOS-517 a AOS-522, em produção | **aberto** |

**Ordem.** AOS-517 e AOS-518 primeiro, em paralelo. Depois AOS-519, com AOS-522 ao lado. Depois
AOS-520 e AOS-521, por esta ordem. AOS-523 fecha.

**ADR.** AOS-519 entrega uma emenda ao ADR-036 §2.8 (de onde vem o perfil). AOS-520 entrega um
ADR novo, por numerar (titular e candidato, modelo fixado por run, canary); AOS-521 entrega a
secção do disjuntor nesse ADR.

### A4 a A6

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
| — | Encaminhamento proposto: antecipar da fase A3 um banco de ensaio de qualificação automática por modelo, e tratar o texto do protocolo como parte do perfil do modelo, em vez de uma constante do nó | **Resolvida pelos factos:** o banco de ensaio existe desde a A2 (AOS-512) e a versão da projecção é parte do perfil da rota (AOS-513). O que falta — o arnês que emite um veredicto assinável — é a fase A3 |
| 2026-10-07 | Encaminhamento proposto: a fase A2 ganha prioridade, por causa do `empty_output` — a única causa de «não cumprido» que resta | **Resolvida:** o dono abriu a fase A2 (linhas abaixo) |
| 2026-10-07 | Ligar a rota sob governação em `observe` em produção (`AOS_MODEL_ROUTE_GOVERNANCE=observe`, `AOS_MODEL_ROUTE_API_HOST=api.kimi.com`) | Tomada (AOS-505); série `v0151g` lida |
| 2026-10-07 | **A fase A1 está fechada**, com os resíduos nomeados na §7: `empty_output` num nó sem tools; o nó com tools e `consumes` negado por taint; os separadores `<kind>` como risco aberto; AOS-505 por impor | Tomada |
| 2026-10-07 | Abrir a fase A2 | Tomada; desenho feito no mesmo dia (`desenho-a2-estado-opaco-2026-10-07.md`) |
| 2026-10-07 | **D1 — sim.** Medir a forma das respostas do provider em produção durante uma ou duas séries: que partes vieram e o tamanho de cada uma, nunca o texto | Tomada (AOS-507) |
| 2026-10-07 | **D2 — sim, primeiro só a contar.** Quando um passo sem ferramentas devolve uma resposta vazia, o sistema tenta outra vez sozinho, com o mesmo limite (duas tentativas a mais por nó, quatro por plano). O `aos-orq` entra em `observe` e só passa a `on` com o número lido | Tomada (AOS-510, AOS-511) |
| 2026-10-07 | **D3 — não.** O raciocínio do modelo nunca é usado como resposta. Porquê: o veredicto `empty_output` está certo (o modelo não respondeu) e promover a resposta um texto que o modelo não deu como resposta refaz o verde falso que a A0 fechou; o raciocínio é deliberação, com hipóteses abandonadas e cópias do material untrusted do passo anterior; o contrato existente dá-lhe a captura como único destino; seria uma regra nova de conclusão, com layout novo; e alargava a saída a um canal que o provider controla. As respostas vazias tratam-se pela D2; se a causa for um servidor que não separa raciocínio de resposta, corrige-se na rota | Tomada (registada no AOS-509) |
| 2026-10-07 | **D4 — o segundo modelo é o Claude (Anthropic), com o raciocínio ligado, pelo mesmo proxy.** Escolhido por obrigar a devolver-lhe o raciocínio assinado no passo seguinte quando há tools: é a parte difícil do estado opaco, e um modelo que não exigisse nada cumpria o critério no papel sem a provar. O Kimi `k3` (já configurado) serve para estrear o banco de ensaio e **não** conta como segunda família. As regras de cada fornecedor sobre devolver o raciocínio confirmam-se na documentação do fornecedor antes de se abrir o ticket | Tomada. Por fornecer pelo dono: a chave, o tecto de despesa diário e a região em que a conta processa os dados (produção está selada para `eu-west`; fora da UE o modelo só serve para ensaio com documentos de teste) |
| 2026-10-07 | **D5 — autorizado um posto de ensaio à parte, com o modelo real e uma chave com limite próprio.** Revê a recusa de medição directa de 2026-10-06, só para o posto de ensaio: com tecto diário de pedidos, documentos de teste, e chaves num ficheiro do dono fora do repositório, que o posto lê pelo caminho e nunca imprime nem regista | Tomada. Por fornecer pelo dono: a chave de ensaio do Kimi e o tecto diário |
| 2026-10-08 | **Chaves e tectos do posto de ensaio.** O dono preencheu o ficheiro fora do repositório (`%USERPROFILE%\.aos-ensaio\chaves.env`), com as chaves do Kimi e da Anthropic, e escolheu os tectos: 1000 pedidos por dia por fornecedor e 5 USD por dia na Anthropic. O posto lê o ficheiro pelo caminho e nunca imprime nem regista os valores | Tomada (AOS-512). O nome do modelo da Anthropic e a região de processamento ficaram por preencher |
| 2026-10-08 | **Diagnóstico acordado:** os separadores `<kind>` e `</kind>` do protocolo são a causa provável — não testada isoladamente — da tool call escrita como texto, e o texto do protocolo foi afinado para um só modelo. Logo: o banco de ensaio primeiro, com a experiência dos separadores, e a versão da projecção (com o seu texto) passa a ser parte do perfil por rota | Tomada (AOS-512, AOS-513) |
| — | **A região da segunda família.** Pela API directa da Anthropic a inferência não se fixa na UE; produção está selada para `eu-west`. Opções: (a) o Claude fica só no banco de ensaio, com documentos de teste, e os critérios P3 e P4 medem-se aí; (b) rota de produção por um endpoint regional da UE num parceiro, com conta própria; (c) emenda explícita ao board de soberania. Recomendação: (a) agora | Por tomar para produção; a fase A2 fechou com (a) em vigor. Passa à A3 (decisão D9 do desenho) |
| — | **O nome do modelo da Anthropic** para o ensaio (`ANTHROPIC_MODELO`) | Ultrapassada a 2026-10-10: a corrida fez-se pela OpenRouter, com `anthropic/claude-sonnet-4.5` |
| 2026-10-10 | **D4 alterada: o Claude qualifica-se pela OpenRouter**, e não pela API directa da Anthropic (a conta não tem créditos: duas tentativas, as duas paradas na sonda com HTTP 400, `saldo_insuficiente`, 2 pedidos gastos). Só no banco de ensaio, com documentos de teste | Tomada (AOS-516). Corrida feita no mesmo dia (§5) |
| 2026-10-10 | **O gateway prepara-se para vários tipos de IA pelo perfil da rota** («este componente deve estar preparado para vários cenários de usar vários tipos de IA»): `devolver_em` (`origem` ou `topo`) diz onde volta o estado que o proxy entrega em `provider_specific_fields`, e `params.reasoning` é a terceira forma de pedir o raciocínio. Vocabulário fechado, inerte por omissão; porta 1.10.0; emendas ao ADR-040 §2.11 e ao ADR-036 §2.8 | Tomada e implementada (AOS-516). Nenhuma rota de produção os declara |
| 2026-10-10 | Subir a toolchain Go de 1.25.13 para 1.26.9: nove vulnerabilidades da biblioteca padrão avermelharam o gate SCA | Tomada (PR #465); em produção na v0.1.53 |
| 2026-10-11 | Se P3 e P4 forem medidos só no banco: «provada em ensaio, não em produção» fecha a fase A2? | **Resolvida: sim** (linha seguinte) |
| 2026-10-11 | **A fase A2 está fechada**, provada em ensaio e não em produção, com os resíduos nomeados na §7: a devolução obrigatória só provada com provider falso; 15 nós com tools e não 40; nenhuma rota de produção com `params`, `devolver` ou `devolver_em`, e a captura por ligar; a causa do `empty_output` por confirmar e a repetição do passo vazio em `observe`; o Claude sem região UE; o replay com modelo real por medir; o AOS-515 fora da RTM do ADR-040; a rota em `observe`, `drop_params` e a imagem do proxy de produção por fixar | Tomada |
| 2026-10-11 | Abrir a fase A3 | Tomada; desenho feito no mesmo dia (`desenho-a3-entrada-automatica-2026-10-11.md`) |
| 2026-10-11 | **As decisões D1 a D9 do desenho da A3** (`desenho-a3-entrada-automatica-2026-10-11.md`): onde vive o perfil do modelo e quem o assina; o que conta como «qualificado»; onde corre o modo real do arnês; o canary; o disjuntor; se «mais de um modelo por nó» entra nesta fase; o orçamento por qualificação; os resíduos da A2 que bloqueiam; o terceiro modelo e a região | **Tomadas, todas como o desenho as recomenda** («concordo com todas, abre os tickets e avança»): D1 registo assinado numa pasta do servidor, assinado pelo dono com chave própria; D2 a bateria de hoje em 8 passagens (40 nós com tools), com três controlos negativos e limiar único; D3 o modo real corre no posto do dono; D4 canary de 10% dos planos, até 40 planos, sem passar a titular sozinho; D5 disjuntor a 3 falhas em 20 runs ou ao primeiro modelo servido diferente, reaberto só pelo dono, `observe` antes de `enforce`; D6 um titular e um candidato (a escolha por passo fica para a A4); D7 300 pedidos e 3 USD por qualificação, no máximo duas por perfil por dia; D8 o proxy de produção fixa-se por digest antes de tudo, `drop_params: false`, e até ao smoke sobre JetStream só entram automaticamente perfis que não devolvem estado; D9 o terceiro modelo da prova é outro modelo do Kimi, e o Claude fica só em ensaio enquanto não houver endpoint na UE. Tickets AOS-517 a AOS-523 abertos no mesmo dia. As interpretações dos tickets onde o desenho era ambíguo (fim do canary ao 40.º plano; plano a meio com o disjuntor aberto falha em vez de mudar de modelo; reabrir é assinar um registo novo) ficam por confirmar pelo dono antes de o AOS-520 e o AOS-521 se implementarem |
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
| 2026-10-08 | Capturas seladas dos 3 runs `empty_output`, sem decifrar: tamanho do criptograma por token de saída | 852, 450 e 628 bytes para 161, 77 e 114 tokens de saída — **5,3 a 5,8 bytes por token**, contra **3,5 a 4,8** nos 117 resumos bem-sucedidos. A resposta veio toda no raciocínio, com `content` vazio: hipótese fortemente apoiada, **não provada**; a ficha do AOS-507 confirma-a em produção. Sustenta a nova tentativa (AOS-510, AOS-511) e o parâmetro de raciocínio no perfil da rota (AOS-513); a decisão D3 mantém-se | AOS-507 (§Estado); os três runs das séries `v0151a` e `v0151b` |
| 2026-10-08 | Banco de ensaio, devolução do estado opaco, **sem modelo real**. Modo falso: o falso exigente do AOS-515, uma passagem pela bateria. Modo proxy: imagem fixada do LiteLLM, rota `anthropic/claude-sonnet-4-5`, provider falso no wire de mensagens, duas passagens | Falso: 22 pedidos com 200; 15 de 15 pedidos seguintes levaram o estado; com `devolver: nunca`, 5 respostas 400. Proxy: 44 pedidos com 200; 30 de 30 pedidos seguintes levaram o estado; ao provider chegaram 60 de 60 blocos de raciocínio, 60 de 60 blocos redigidos e 60 de 60 blocos de raciocínio de texto vazio com os valores emitidos, e `thinking` em 44 de 44 pedidos; **em 60 de 60 o proxy pôs um bloco de texto entre o raciocínio e a tool call**; com `devolver: nunca`, 10 respostas 400 (o proxy não retirou o `thinking`). Um provider falso não valida assinaturas: **nada disto diz o que o Claude aceita** | AOS-516 (§Estado); `bash scripts/ci/banco-ensaio-proxy.sh` |
| 2026-10-10 | **Experiência dos separadores com o Kimi real** (AOS-512): quatro braços, 53 amostras cada, um caso, um turno | Modelo `kimi-for-coding`, com a chave do plano. 212 pedidos e a sonda, todos com 200; 0 limites de ritmo. Falhas de tool call à primeira: A (1.2.0, `<kind>`) 0 de 53; B (`[[kind]]`) 0 de 53; C (sem linhas de fim) 2 de 53; D (1.0.0) 1 de 53. Nenhuma comparação distingue a 5% (Fisher exacto, Holm). As 3 falhas têm o nome da tool no texto. **A forma dos separadores não é a causa: hipótese retirada da §7.** Limite: um caso (T1), um turno | Relatório na pasta do dono; AOS-512 |
| 2026-10-10 | Corrida do Claude pela API directa da Anthropic (AOS-516), duas tentativas | As duas pararam na sonda com 400: `saldo_insuficiente` (conta sem créditos). 2 pedidos gastos do tecto; nenhuma observação | AOS-516 (§Estado) |
| 2026-10-10 | Banco de ensaio, forma da OpenRouter (`reasoning` + `reasoning_details`), **sem modelo real**. Modo falso, uma passagem. Modo proxy: imagem fixada do LiteLLM, rotas `openrouter/anthropic/claude-sonnet-4.5` e `openai/anthropic/claude-sonnet-4.5`, duas passagens | Falso: 22 pedidos com 200, 15 de 15 aceites, `cumprida`. Proxy: `thinking` e `reasoning_effort` chegam em 22 de 22 pedidos por `openrouter/` e em 0 de 22 por `openai/`; **`reasoning_details` volta no topo da mensagem em 0 de 10 turnos e dentro de `provider_specific_fields` em 10 de 10**, pelas duas rotas: 10 respostas 400, `nao_cumprida`. Posto à mão no topo, atravessa o proxy intacto. Erros 401, 402, 429, 404 e 400 na forma da OpenRouter passam com o código e caem no vocabulário fechado. **Nada disto diz o que a OpenRouter aceita** | AOS-516 (§Estado); `packages/qa/banco-ensaio/README.md` |
| 2026-10-10 | O mesmo, depois de o perfil declarar `devolver_em: topo` e `params.reasoning` (porta 1.10.0). `scripts/ci/banco-ensaio-proxy.sh` inteiro; rota `openrouter/anthropic/claude-sonnet-4.5`, duas passagens por cenário | Com `topo`: 44 pedidos com 200, `reasoning_details` no topo e intacto em 60 de 60 turnos, 30 de 30 aceites, o saco em nenhum, **`cumprida`** — com `reasoning` (`effort` e `max_tokens`), `reasoning_effort` e `thinking`, cada um em 44 de 44 pedidos no fornecedor. Controlo sem `devolver_em`: 10 respostas 400, `nao_cumprida`. Gate verde. **O fornecedor continua a ser um falso** | AOS-516 (§Estado); `packages/qa/banco-ensaio/README.md` |
| 2026-10-10 | Revisão adversarial do AOS-516: o que a imagem fixada do proxy envia por conta própria na rota `openrouter/…`, e os erros embrulhados | `HTTP-Referer: https://litellm.ai` e `X-Title: liteLLM` em todos os pedidos (22 de 22 e 44 de 44 por cenário), valores por omissão da imagem; o proxy embrulha o erro do fornecedor dentro da sua própria mensagem, e a classificação lê-o aí. Com `devolver_em: topo`, um nome de campo repetido no estado deixa de ser devolvido (`estado_com_nome_repetido`) | AOS-516 (§Estado); `bash scripts/ci/banco-ensaio-proxy.sh` |
| 2026-10-10 | Estado de produção | **v0.1.53** (tag sobre 09a17146), em produção desde as 18:00 UTC: Go 1.26.9 e AOS-512 a AOS-516 (do AOS-516, a parte do banco), com tudo o que é da A2 desligado. Série de validação `v0153a`: 5 de 5 planos concluídos à primeira, 10 de 10 runs, 15 de 15 turnos com `route_check: igual`, projecção 1.2.0, 0 respostas vazias. Fundidos depois da tag, ainda sem release: PR #466 (o banco classifica a conta da Anthropic sem créditos) e PR #467 (`devolver_em`, `params.reasoning`, porta 1.10.0, a OpenRouter no banco) | servidor; série `v0153a` |
| 2026-10-10 | **Qualificação do Claude pela OpenRouter, corrida 1** (23:35 a 23:41 UTC; `anthropic/claude-sonnet-4.5`, rota `openrouter/…`; perfil com `reasoning: {effort: medium}`, `devolver: obrigatorio`, `devolver_em: topo`, projecção 1.3.0; binário do commit 669b3f7e, igual em código ao squash 725df5e8). Uma passagem pela bateria | 13 pedidos (12 e a sonda), todos com 200. 7 de 7 runs cumpridos à primeira; 0 de 5 nós com tools sem tool call; segundo turno com tools aceite em 5 de 5. 7 turnos com estado capturado, os 7 com raciocínio ou assinatura, 0 só com ids. 5 de 5 pedidos com estado aceites (2xx); 0 recusas. Veredicto `cumprida` | AOS-516 (§Estado) |
| 2026-10-10 | **Controlo negativo** da corrida anterior: o mesmo modelo e os mesmos parâmetros, com `devolver: nunca` e a projecção 1.2.0 | 13 pedidos, todos com 200. 7 de 7 runs cumpridos; segundo turno aceite em 5 de 5; a mesma distribuição de formas da resposta. **A OpenRouter não exige o raciocínio de volta:** `cumprida` prova que devolver não parte nada, não que seja necessário nem que seja lido | AOS-516 (§Estado) |
| 2026-10-10 | **Qualificação do Claude pela OpenRouter, corrida 2:** o perfil da corrida 1, duas passagens | 25 pedidos, todos com 200. 14 de 14 runs cumpridos à primeira; 0 de 10 nós com tools sem tool call; segundo turno aceite em 10 de 10. 14 turnos com raciocínio capturado; 10 de 10 pedidos com estado aceites. Veredicto `cumprida`. «Factos ausentes da saída»: 2 de 14 (um em T2, um em T4), não lidos | AOS-516 (§Estado) |
| 2026-10-10 | Total do Claude com devolução (corridas 1 e 2), e o gasto das três corridas | 21 de 21 runs cumpridos; 15 de 15 nós com tools com o segundo turno aceite; 15 de 15 pedidos com estado aceites. 51 pedidos ao todo, cerca de 0,64 USD (estimativa). Uma corrida de 8 passagens foi recusada pelo banco antes de enviar: 673 pedidos no pior caso, contra o tecto de 200 | AOS-516 (§Estado) |
| 2026-10-11 | **Imagem do proxy em produção**, lida no servidor (só leitura), antes de qualquer mudança do AOS-517 | O contentor `aos-litellm-1` corre `ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63` (`litellm` 1.96.2, imagem criada a 2026-08-11): **é o digest do banco**. A tag `main-stable` resolveu para ele quando o contentor foi criado, três semanas antes. O `config.yaml` do servidor tem `drop_params: true` (a semente do repositório diz `false`). Por medir, pelo dono: o par de planos antes e depois de cada passo, e a série de 20 | AOS-517; `deploy/server/README.md`, «O proxy do modelo, fixado pelo digest» |
| 2026-10-11 | Critério da fase A2 (P1 a P7) | Cumpridos: P5 e P7. Em parte ou só em ensaio: P3 e P4. Por cumprir ou por medir: P1, P2 e P6. Um a um na §3. **Fase fechada pelo dono a 2026-10-11, provada em ensaio e não em produção**, com os resíduos da §7 | §3; esta tabela |

Por medir: se a Anthropic, pela API directa, exige o raciocínio de volta e o que responde quando falta (a OpenRouter não o exige — medido a 2026-10-10); se a Anthropic aceita o bloco de texto que o proxy acrescenta entre o raciocínio e a tool call quando o `content` do `assistant` é vazio (AOS-516, só o modelo real o diz); a projecção 1.2.0 sem o aviso (não se mediu, por decisão do dono); um nó com tools e
`consumes` recuperado por nova tentativa (não realizável em produção com a política de hoje,
§3); o AOS-505 em `enforce`; a causa do `empty_output` (a hipótese do campo de raciocínio foi corrigida pela leitura do código — o adaptador lê `reasoning_content` —, e a causa continua por medir: AOS-507; 0 ocorrências desde a ficha); a repetição do passo vazio em `on` (P2); o replay de um run com devolução e de um retomado, com modelo real (P6); a experiência dos separadores em mais de um caso e mais de um turno (a de 2026-10-10 retirou a hipótese só para o caso T1); o que o provider real devolve sobre o modelo que serviu; e o critério da fase A0,
cuja contagem dos 150 runs recomeça desde a projecção 1.1.0.

## 6. Matriz de suporte por classe de modelo

Estados: **qualificada** (com rota e data), **qualificada em ensaio** (com modelo real no banco
de ensaio, sem rota de produção), **desenhada e não testada**, **não suportada**.

| Classe | Estado | Rota e data | Fase que a qualifica |
|---|---|---|---|
| Tool calling nativo, sem estado opaco exigido | qualificada, com defeito conhecido (verde falso em cerca de 1 em 5 planos) | Kimi por LiteLLM, 2026-10-04 | A0 fecha o defeito |
| Tool calling nativo com raciocínio a devolver | **qualificada em ensaio, não em produção.** O estado captura-se (AOS-514) e devolve-se (AOS-515); com modelo real, 15 de 15 segundos turnos aceites. Limite: o fornecedor medido não exige a devolução; a exigência só está provada com provider falso. Nenhuma rota de produção a declara | Claude (`anthropic/claude-sonnet-4.5`) pela OpenRouter, por LiteLLM fixado, no banco de ensaio, 2026-10-10 | A2 (em ensaio); a A3 leva-a a produção, por decisão do dono |
| Tool calling nativo com assinaturas por chamada | desenhada e testada só com providers falsos (assinaturas e blocos redigidos chegam intactos atrás do proxy fixado, 2026-10-08). Com modelo real, nas corridas de 2026-10-10 os turnos trouxeram «raciocínio ou assinatura», sem se separar um do outro; a API directa da Anthropic, que valida a assinatura, não foi corrida | — | A2 não a qualificou; passa à A3 |
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
  raciocínio nunca é usado como resposta (decisão D3). **A 2026-10-11:** 0 ocorrências desde
  que a ficha existe (62 e 5 planos); a causa continua por confirmar e a repetição do passo
  vazio continua em `observe` — resíduo (d) da fase A2, abaixo.
- **Retirado a 2026-10-10:** o risco «os separadores `<kind>` e `</kind>` do protocolo são a
  causa de fundo da tool call escrita como texto». A experiência dos separadores, com o Kimi
  real, não distinguiu nenhum dos quatro braços (§5). Fica o limite da experiência: um caso
  (T1) e um turno.
- **Limite de produto: um nó com tools e `consumes` é negado pelo gate de taint.** Medido a
  2026-10-07 (`plan-e2e-v0151t-1791401536`): o nó que recebeu `plan_input` pediu `doc_read` e o
  Reference Monitor negou, com `cap:fs.read` armada (ADR-034, fase 1). Um plano em que um passo
  usa uma tool protegida com base no que outro passo leu falha sempre, com causa nomeada
  (`contract_unmet_after_denial`), e a recuperação não o alcança. Levantá-lo exige a forma
  forte do ADR-005 (opção A, dual-LLM) ou aprovação humana. Fora da fase A1.
- A rota sob governação corre em `observe` e não em `enforce`: uma troca de modelo seria contada
  e não recusada, e o cron do `alerta-rota.sh` está por instalar. A forma estreita mantém-se:
  não se detecta uma troca feita pelo provider por trás do mesmo nome e endpoint.
- O texto do protocolo foi afinado para um só modelo (o Kimi). A versão da projecção passou a
  ser parte do perfil da rota (AOS-513). Noutro modelo há uma só medição, pequena: o Claude pela
  OpenRouter cumpriu 7 de 7 runs com a 1.2.0 e 21 de 21 com a 1.3.0 (§5, 2026-10-10). Medir o
  texto do protocolo por modelo, com amostra que chegue, é trabalho do arnês da fase A3.
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

### Resíduos da fase A2 (fechada a 2026-10-11)

Nomeados no fecho. Nenhum ficou resolvido pelo fecho; os que a fase A3 herda estão marcados.

- **(a) A devolução obrigatória só está provada com provider falso.** A OpenRouter aceita o
  segundo turno com e sem o raciocínio de volta. A API directa da Anthropic, que o exige, não
  foi corrida (conta sem créditos). Por testar com o fornecedor real: o bloco de texto que o
  proxy insere, na rota `anthropic/`, entre o raciocínio e a tool call quando o `content` do
  `assistant` é vazio. *Herdado pela A3:* um arnês que só veja 2xx não distingue «aceite» de
  «exigido».
- **(b) 15 nós com tools, não os 40 que o AOS-516 pedia.** O critério P3 da segunda família
  ficou cumprido em parte.
- **(c) Provado em ensaio, não em produção.** Nenhuma rota de produção declara `params`,
  `devolver` nem `devolver_em`, e `AOS_MODEL_PROVIDER_STATE=capture` continua por ligar: falta
  o smoke sobre JetStream com o estado no tecto (ADR-040 §2.10). *Herdado pela A3:* sem a
  captura ligada, nenhum modelo que devolva estado serve runs em produção.
- **(d) `empty_output`:** 0 ocorrências desde a ficha (62 e 5 planos), causa por confirmar; a
  repetição do passo vazio continua em `observe` (P1 e P2 por cumprir).
- **(e) O Claude não tem região UE por esta via:** só em ensaio, com documentos de teste.
  *Herdado pela A3:* produção está selada para `eu-west`.
- **(f) Por medir:** um run retomado com devolução no nó composto, e o replay (P6) com modelo
  real.
- **(g) O AOS-515 não aparece na RTM como implementador do ADR-040:** o bloco do ticket tem o
  marcador de menção de ADR, e a RTM não o liga ao ADR cuja metade de devolução ele entrega.
- **(h) A rota de produção, tal como está.** A governação corre em `observe`, não em `enforce`.
  O `drop_params: false` no proxy do servidor é um passo do dono que está por tomar (§4): a
  semente no repositório já diz `false`, mas o deploy não reescreve o ficheiro do servidor. E
  a imagem do proxy em produção é a tag móvel `ghcr.io/berriai/litellm:main-stable`
  (`deploy/server/docker-compose.prod.yml`), não o digest fixado em que o banco mede
  (`packages/qa/banco-ensaio/proxy.go`): o que o banco prova sobre o proxy vale para a imagem
  do banco. *Herdado pela A3:* um modelo qualifica-se contra um proxy, e esse proxy tem de ser
  o que serve.
  *Actualização de 2026-10-11 (AOS-517).* **Medido: produção já corre o digest do banco.** A
  leitura no servidor (§5) mostrou que a `main-stable` tinha resolvido para o digest fixado
  quando o contentor foi criado; o que o banco provou sobre o proxy valia, de facto, para o
  proxy que servia — mas nada o garantia nem o dizia. O compose do repositório refere agora o
  digest e um teste prende a igualdade. Continuam por fazer, pelo dono: aplicar esse compose
  no servidor (a primeira release com o AOS-517 recria o proxy), e `drop_params: false`, que
  no servidor ainda é `true`. A governação continua em `observe` (D8, fora do AOS-517).

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
