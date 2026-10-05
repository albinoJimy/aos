# Acompanhamento — arquitectura-alvo da fronteira runtime↔modelo

> Documento vivo. É a fonte única do estado da arquitectura-alvo aceite pelo dono a 2026-10-04.
> **Regra de actualização:** o PR que muda o estado de um ticket desta lista, que mede um critério
> de prova ou que regista uma decisão do dono actualiza este ficheiro no mesmo commit. Um estado
> aqui que não bata com o ticket na EPIC é um defeito do PR.

Última actualização: 2026-10-05 (imposição do veredicto ligada em produção; saída por referência antecipada).

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
| **A1** | Recuperação do run (aviso ou repetição do pedido) e rota sob governação (nome real do modelo, proxy sem descartar parâmetros, modelo servido comparado por turno) | «Não cumprido» abaixo de 2%; uma troca de modelo por baixo é detectada | A0; medição de até 150 pedidos | por começar |
| **A2** | Estado opaco do provider por turno (raciocínio, assinaturas, identificadores), com sondas de protocolo deterministas | Duas famílias de modelos completam runs com tools | A0; escolha da segunda família | por começar |
| **A3** | Entrada automática: arnês de qualificação, perfil do modelo como artefacto do registo, mais de um modelo por nó, canary, disjuntor | O terceiro modelo entra com zero PRs e uma assinatura em menos de uma hora; um modelo mau é recusado sozinho | A1, A2 | por começar |
| **A4** | Cascata: estimar a capacidade que o passo exige e eleger o modelo por roteamento determinista, com limiares num `decision pack` | A divisão entre modelos baratos e caros é medida e ajustada sem deploy | A3 | por começar |
| **A5** | Multimodal de entrada (media por referência) | Um modelo recebe imagem ou áudio num run, com replay | A2 | por começar |
| **A6** | Multimodal de geração, como tool | Uma modalidade gerada com proveniência e custo contabilizado | A5; saída por referência | por começar |

Transversal: **saída por referência** ao resultado da tool (a saída de um nó deriva de facto da
tool). Não tem fase própria ainda; é pré-requisito de A6 e fecha o resíduo «evidência não é
fidelidade» para nós que passam o resultado sem o transformar.

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
- Tool chamada e saída fabricada, em nós que transformam conteúdo. **Medido a 2026-10-05: 2 em 21 planos** perderam factos do documento no nó de leitura. Só a saída por referência o fecha.
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
