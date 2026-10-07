# Acompanhamento — arquitectura-alvo da fronteira runtime↔modelo

> Documento vivo. É a fonte única do estado da arquitectura-alvo aceite pelo dono a 2026-10-04.
> **Regra de actualização:** o PR que muda o estado de um ticket desta lista, que mede um critério
> de prova ou que regista uma decisão do dono actualiza este ficheiro no mesmo commit. Um estado
> aqui que não bata com o ticket na EPIC é um defeito do PR.

Última actualização: 2026-10-06 (fase A1: AOS-502 e AOS-503 implementados e revistos, desligados por omissão e por verificar em produção; AOS-504 e AOS-505 abertos; v0.1.49 em produção com a entrega por referência ligada).

## 1. Objectivo e promessa

**Objectivo do dono:** excelência no sentido de «qualquer modelo novo entra sem trabalho».

**Promessa que se consegue provar:** um modelo de uma classe de wire qualificada, com fornecedor e
região já aprovados, entra sem código nem configuração manual além de uma entrada de catálogo
assinada. Relaxar garantias exige uma segunda assinatura. Uma classe de wire nova é engenharia.

Origem: `analise-fronteira-runtime-modelo-2026-10-04.md` (cinco perspectivas e duas avaliações
adversariais) e a validação adversarial do desenho de IA de governação, da mesma data.

## 2. Estado por fase

Estados possíveis: **por começar**, **em curso**, **em produção por verificar**, **provada**.
Uma fase só passa a **provada** quando o critério de prova está medido e registado na §5.

| Fase | Conteúdo | Critério de prova | Depende de | Estado |
|---|---|---|---|---|
| **A0** | O desfecho de um run é um veredicto do kernel sobre um contrato de conclusão; o `aos-orq` trata «não cumprido» como nó falhado | Zero verdes falsos em pelo menos 150 runs com tools na oferta | — | **em produção por verificar** (v0.1.46, imposição ligada a 2026-10-05; falta o critério de prova) |
| **A0.5 — Saída por referência** | A saída de um nó de passagem directa é o resultado da tool, e não o texto do modelo: o plano declara a origem (`outputs[].from_tool`), o kernel designa e sela qual chamada é a origem, e o `aos-orq` publica e entrega esses bytes, conferidos contra o digest selado. O texto final continua capturado e deixa de ser a saída | Numa série de pelo menos 20 planos, a saída entregue ao nó seguinte é byte a byte o resultado selado da tool e nenhum facto do documento se perde | A0 | **provada** (2026-10-06, v0.1.49 com a entrega ligada: série de 21 planos, 18 entregas por referência, todas byte a byte iguais ao resultado selado; os outros 3 falharam antes da entrega por o modelo não chamar a tool) |
| **A1 — Recuperação** | Nova tentativa ao nível do plano: um nó que terminou sem chamar a tool volta a ser submetido, até duas vezes a mais, em qualquer nó com tools, e o nó `aos` só aceita a tentativa depois de provar no seu log que a anterior não pediu tool nenhuma. Projecção nativa 1.1.0 (fim de segmento inforjável e texto do protocolo reescrito), desligada por omissão e medida antes de ligar. Rota sob governação (nome real do modelo, proxy sem descartar parâmetros, modelo servido comparado por turno) | «Não cumprido» abaixo de 2% numa série de pelo menos 40 planos com a recuperação ligada; uma troca de modelo por baixo é detectada | A0 | **em curso** (2026-10-06: AOS-502 e AOS-503 implementados e revistos, desligados por omissão; AOS-504 e AOS-505 por implementar; nada ligado em produção) |
| **A2** | Estado opaco do provider por turno (raciocínio, assinaturas, identificadores), com sondas de protocolo deterministas | Duas famílias de modelos completam runs com tools | A0; escolha da segunda família | por começar |
| **A3** | Entrada automática: arnês de qualificação, perfil do modelo como artefacto do registo, mais de um modelo por nó, canary, disjuntor | O terceiro modelo entra com zero PRs e uma assinatura em menos de uma hora; um modelo mau é recusado sozinho | A1, A2 | por começar |
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
| AOS-502 | EPIC-19 | O nó aceita a nova tentativa de um nó do plano e prova, no seu próprio log, que a anterior não pediu tools. Escreve o ADR novo da recuperação e as emendas ao ADR-027, ao ADR-035 e ao ADR-037 | AOS-493, AOS-494 | implementado (ADR-039) e revisto (2026-10-06, sem bloqueantes); **invisível com `AOS_RUN_RETRY_MAX` a zero, que é a omissão** — o nó é o de antes, medido byte a byte. A revisão não achou caminho em que uma tentativa repita um efeito ou seja concedida sem a prova. Corrigido no ticket: o `GET /runs/{id}` de uma tentativa admitida passa a dizer de que pedido, plano, nó e tentativa ela é (`plan_attempt`), e os ramos de falha da leitura da prova (estado durável ou residência ilegíveis, residência não selada) ficaram presos por teste. Por fazer: smoke sobre JetStream de um run `<plano>~<nó>~2`, teste do 429 do lado do nó, verificação em produção |
| AOS-503 | EPIC-19 | O `aos-orq` volta a submeter um nó do plano que terminou sem chamar a tool (`off`, `observe`, `on`; até duas tentativas a mais; métricas e alerta) | AOS-502, AOS-495 | implementado e revisto (2026-10-06, sem bloqueantes); **desligado por omissão** (`AOS_ORQ_NOVA_TENTATIVA=off`: o `aos-orq` é o de antes, 312 cenários sem diferença). Dois achados importantes corrigidos no ticket: um id de tentativa acima de 128 bytes prendia o plano em erro transitório (agora corre, e um facto que não caiba fecha o nó com `tentativa_recusada=facto_invalido`); e a retoma seguia qualquer run com o id da tentativa (agora só o segue depois de conferir a origem que o nó declara; senão o nó fecha `failed` com `tentativa_recusada=run_de_outra_origem`, nunca «recuperado»). Pior caso de custo medido: seis nós que falham sempre dão dez runs (o tecto por plano trava nas quatro a mais); o orçamento do plano não debita os runs filhos. Resíduo aceite e escrito no ADR-039 §5: quem pré-criar os ids das tentativas desliga a recuperação desse nó. Por fazer: alerta automático das primeiras falhas (hoje é regra do runbook), dois `serve` concorrentes, rollback da imagem na topologia em que a decisão de ramo se lê depois do facto, verificação em produção |
| AOS-504 | EPIC-06 | Projecção nativa 1.1.0: fim de segmento inforjável, e o objectivo deixa de se confundir com dados; canário de medição | — (independente) | implementado e revisto (2026-10-06, sem bloqueantes); desligado por omissão; por medir em produção. A revisão corrigiu, só na 1.1.0, o texto do protocolo (repõe a reserva sobre o que parece um cabeçalho e corta «because it is not data»), o escape das quase-forjas atrás de caracteres invisíveis, a recusa de um kind vazio ou com `/`, e a contagem do canário (só depois da conclusão escrita, e um nó `failed` não conta) |
| AOS-505 | EPIC-06 | Rota sob governação: o proxy deixa de descartar parâmetros e o modelo que serviu cada turno é comparado com o esperado | — (independente; cada mudança de configuração de produção liga-se por decisão do dono) | implementado e revisto (2026-10-07, sem bloqueantes; emenda ao ADR-036 §2.8); **desligado por omissão**; por ligar em produção (`AOS_MODEL_ROUTE_GOVERNANCE=off`: o nó é o de antes, medido byte a byte contra goldens da base). O primeiro critério foi medido localmente com a imagem de produção do LiteLLM (pelo digest, 1.96.2) contra providers falsos, por decisão do dono de 2026-10-06, em vez de um plano pela fila: o `model` do corpo é sempre o nome pedido; o modelo e o endpoint configurados vêm nos cabeçalhos `x-litellm-model-name` e `x-litellm-model-api-base`. Detecta uma troca de **configuração no proxy** (provado com o proxy real: `make ci-rota-live`); não detecta uma troca feita pelo provider por trás do mesmo nome e endpoint, e o que o provider real devolve não foi medido. Limites: não detecta uma troca feita pelo provider, as chamadas ao modelo feitas pelo `aos-orq` (o planeador) ficam fora da comparação, e `enforce` exige o host do endpoint (`AOS_MODEL_ROUTE_API_HOST`). Por fazer, por decisão do dono, os passos de produção — `drop_params: false` no servidor, ligar `observe` (série de 20 planos), a troca do nome pedido pelo nome real (exige re-assinar a allowlist) e a passagem a `enforce` |

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

### A2 a A6

Sem tickets abertos. Abrem-se quando a fase anterior estiver em produção e as decisões da §4
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
| — | Pôr `drop_params: false` no proxy de produção e passar a pedir o nome real do modelo (AOS-505) | Por tomar: liga-se por decisão do dono, com um pedido de verificação antes e depois |
| — | O que a saga de compensação faz, num run não cumprido, aos efeitos das tools que correram bem (ADR-037 §4). Hoje não há compensações registadas e o efeito fica aplicado; decide-se antes de a primeira tool registar a sua | Por tomar |
| 2026-10-06 | Recuperação por aviso ou por repetição do pedido | **Resolvida:** nova tentativa ao nível do plano, com o mesmo pedido (linhas seguintes). O aviso fica adiado com gatilho |
| 2026-10-06 | Recuperação: o sistema tenta outra vez sozinho um passo em que o modelo não usou a ferramenta | Tomada (AOS-502, AOS-503) |
| 2026-10-06 | Recuperação: **até duas tentativas a mais** (no máximo três runs do mesmo nó do plano) | Tomada (AOS-502, AOS-503) |
| 2026-10-06 | Recuperação: aplica-se a **todos os nós com tools**, incluindo os que recebem material de outros nós (`consumes`). O desenho recomendava começar só pelos nós sem `consumes`. Risco aceite: num nó com `consumes`, conteúdo untrusted do passo anterior pode levar o modelo a não chamar a tool e gastar as tentativas; o dano é limitado pelo tecto de tentativas e pelo orçamento, e a tentativa nunca dá autoridade | Tomada (AOS-502, AOS-503) |
| 2026-10-06 | A correcção do texto de instruções (o nó que recusa o próprio objectivo) entra na fase A1, desligada por omissão e medida antes de ligar | Tomada (AOS-504) |
| 2026-10-06 | Recuperação: as tentativas contam no orçamento de quem pediu, com um tecto de tentativas a mais por plano; um sucesso à segunda ou à terceira aparece como sucesso normal, com a contagem de tentativas registada e visível nas métricas | Por omissão (recomendação do desenho; o dono não decidiu em contrário) |
| 2026-10-06 | Recuperação: o «aviso e mais um turno» fica adiado, com o gatilho do desenho (seis ou mais das primeiras falhas voltam a falhar na tentativa seguinte, ou a entrada de uma rota determinista); a decisão toma-se com os números dos primeiros 100 planos com a recuperação ligada | Por omissão (recomendação do desenho; o dono não decidiu em contrário) |
| 2026-10-06 | A recusa do próprio objectivo não se detecta com segurança: corrige-se a causa provável e vigia-se por um contador | Por omissão (recomendação do desenho; o dono não decidiu em contrário) |
| — | Qual é a segunda família de modelos (condiciona A2) | Por tomar |
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

Por medir: o vocabulário de motivos de paragem que o provider de produção envia; taxa de vermelhos falsos do contrato em modo de observação; eficácia da recuperação;
o que o proxy devolve no campo `model`.

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
