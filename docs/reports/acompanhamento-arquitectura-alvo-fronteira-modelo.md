# Acompanhamento — arquitectura-alvo da fronteira runtime↔modelo

> Documento vivo. É a fonte única do estado da arquitectura-alvo aceite pelo dono a 2026-10-04.
> **Regra de actualização:** o PR que muda o estado de um ticket desta lista, que mede um critério
> de prova ou que regista uma decisão do dono actualiza este ficheiro no mesmo commit. Um estado
> aqui que não bata com o ticket na EPIC é um defeito do PR.

Última actualização: 2026-10-05 (saída por referência: AOS-497 implementado e revisto — o kernel designa e sela a origem da saída; AOS-498 implementado e revisto — o nó aceita a declaração e devolve a âncora e os bytes; AOS-499 implementado e revisto — o `aos-orq` mede com o interruptor em `observe`, desligado por omissão, e a revisão refez a medição para comparar o texto final com o conteúdo do resultado e não com o envelope; AOS-500 implementado, com a revisão adversarial por fazer — o plano pode declarar a origem de uma saída e nenhum plano a usa ainda; AOS-501 aberto).

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
| **A0.5 — Saída por referência** | A saída de um nó de passagem directa é o resultado da tool, e não o texto do modelo: o plano declara a origem (`outputs[].from_tool`), o kernel designa e sela qual chamada é a origem, e o `aos-orq` publica e entrega esses bytes, conferidos contra o digest selado. O texto final continua capturado e deixa de ser a saída | Numa série de pelo menos 20 planos, a saída entregue ao nó seguinte é byte a byte o resultado selado da tool e nenhum facto do documento se perde | A0 | **em curso** (AOS-497 implementado e revisto a 2026-10-05; AOS-498 e AOS-499 implementados a 2026-10-05, por rever; sem efeito enquanto `AOS_ORQ_SAIDA_POR_REFERENCIA` não for `observe`; AOS-500 e AOS-501 abertos) |
| **A1** | Recuperação do run (aviso ou repetição do pedido) e rota sob governação (nome real do modelo, proxy sem descartar parâmetros, modelo servido comparado por turno) | «Não cumprido» abaixo de 2%; uma troca de modelo por baixo é detectada | A0; medição de até 150 pedidos | por começar |
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
| AOS-500 | EPIC-19 | O plano declara a origem de uma saída: `outputs[].from_tool`, schema 1.3.0 | — | implementado (2026-10-05); **revisão adversarial por fazer**. O campo é opcional e entra no `contract_digest` só quando presente: um plano sem ele tem o documento, os digests, os eventos e o cartão de antes. Cinco regras no validador, com código próprio, e o piso de versão 1.3.0. O prompt do planeador não muda (a excepção no teste que deriva o schema do prompt contém só `from_tool` e sai no AOS-501). Até ao AOS-501 o `aos-orq` recusa correr um plano com o campo (`origem_sem_entrega`, saída 10). Emenda o ADR-022 §2.3. Rollback medido: o binário anterior recusa um documento com o campo e um documento carimbado 1.3.0 |
| AOS-501 | EPIC-19 | O `aos-orq` entrega por referência as saídas declaradas | AOS-498, AOS-499 (medição lida), AOS-500 | aberto |

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

### A1 a A6

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
| 2026-10-05 | Saída por referência: a origem declara-se no plano (`outputs[].from_tool`); não se infere da estrutura. A inferência estrutural existe só como medição | Tomada |
| 2026-10-05 | Saída por referência: sem origem designável (nenhuma ou mais de uma chamada candidata), o nó falha com causa própria; nunca se entrega o texto do modelo no lugar do resultado da tool | Tomada |
| 2026-10-05 | Saída por referência: o nó seguinte recebe o resultado tal como a tool o devolveu (o envelope), byte a byte, conferível contra o digest selado | Tomada |
| 2026-10-05 | Saída por referência: os bytes viajam pelo `aos-orq`, com âncora selada pelo kernel; o nó consumidor não muda. A alternativa em que o nó consumidor resolve a referência fica rejeitada agora, com gatilhos (o primeiro payload legítimo acima do tecto; a fase A5 ou A6; a reabertura do DEF-806) | Tomada |
| 2026-10-05 | Saída por referência: o texto final do nó produtor continua a ser capturado mas não é publicado nem entregue | Por omissão (recomendação do desenho; o dono não decidiu em contrário) |
| 2026-10-05 | Saída por referência: um nó candidato sem declaração não é recusado pelo validador por agora; decide-se com a taxa de omissão medida em observação (AOS-499, AOS-501) | Por omissão (recomendação do desenho; o dono não decidiu em contrário) |
| — | Validar a classe alargada do contrato de conclusão antes de ligar `enforce`: os nós com tools e **sem** saída de forma aberta declarada (`com_contrato_sem_saida`; o alargamento veio da revisão adversarial do AOS-495, não da decisão de 2026-10-04). Lê-se em `aos_orq_consume_nos_por_contrato_total` e `aos_orq_consume_veredictos_observados_total` quantos são e quantos `enforce` fechava `failed` | Por tomar |
| — | Autorizar a medição de até 150 pedidos ao LiteLLM de produção e pôr `drop_params: false` (condiciona A1) | Por tomar |
| — | O que a saga de compensação faz, num run não cumprido, aos efeitos das tools que correram bem (ADR-037 §4). Hoje não há compensações registadas e o efeito fica aplicado; decide-se antes de a primeira tool registar a sua | Por tomar |
| — | Recuperação por aviso ou por repetição do pedido (depois da medição) | Por tomar |
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
- Nó sem tools que conclui a dizer que não conseguiu. **Medido a 2026-10-05: 1 em 21 planos**, por o nó tratar o próprio objectivo como dados untrusted — o texto do protocolo nativo pode estar a induzi-lo.
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
