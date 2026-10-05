# Acompanhamento — arquitectura-alvo da fronteira runtime↔modelo

> Documento vivo. É a fonte única do estado da arquitectura-alvo aceite pelo dono a 2026-10-04.
> **Regra de actualização:** o PR que muda o estado de um ticket desta lista, que mede um critério
> de prova ou que regista uma decisão do dono actualiza este ficheiro no mesmo commit. Um estado
> aqui que não bata com o ticket na EPIC é um defeito do PR.

Última actualização: 2026-10-05 (AOS-494 implementado, por rever; AOS-493 revisto e corrigido; AOS-491 e AOS-493 por verificar em produção).

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
| **A0** | O desfecho de um run é um veredicto do kernel sobre um contrato de conclusão; o `aos-orq` trata «não cumprido» como nó falhado | Zero verdes falsos em pelo menos 150 runs com tools na oferta | — | **em curso** |
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
| AOS-491 | EPIC-06 | O motivo de paragem chega ao runtime, à captura e ao registo do turno | — | implementado; por verificar em produção |
| AOS-492 | EPIC-02 | A regra de terminação vive num só sítio, partilhado por loop e replay | — | feito |
| AOS-493 | EPIC-02 | O desfecho de um run é um veredicto do kernel sobre um contrato de conclusão | AOS-491, AOS-492 | implementado (ADR-037) e revisto (2026-10-05, sem bloqueantes; achados corrigidos no ticket), nó em observação por omissão; por verificar em produção |
| AOS-494 | EPIC-19 | O nó aceita o contrato no `POST /runs` e devolve o desfecho no `GET /runs` | AOS-493 | implementado (2026-10-05); por rever; smoke sobre JetStream por correr |
| AOS-495 | EPIC-19 | O `aos-orq` declara o contrato por nó e não publica saídas sem evidência | AOS-494 | aberto |

Ordem de entrega: AOS-491 e AOS-492 (sem mudança de comportamento), depois AOS-493 em modo de
observação, depois AOS-494 e AOS-495. O nó sai antes do `aos-orq`. A imposição liga-se depois de o
modo de observação dar a taxa de vermelhos falsos.

### A1 a A6

Sem tickets abertos. Abrem-se quando a fase anterior estiver em produção e as decisões da §4
correspondentes estiverem tomadas.

## 4. Decisões do dono

| Data | Decisão | Estado |
|---|---|---|
| 2026-10-04 | A arquitectura-alvo A0 a A6 substitui as fases F0 a F4 da análise e o desenho de IA de governação | Tomada |
| 2026-10-04 | O contrato de conclusão é inferido das tools atribuídas ao nó, em âmbito estreito, sem mudar o schema do plano | Tomada |
| 2026-10-04 | «Não cumprido» grava-se como `failed` com razão própria | Tomada |
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
- Tool chamada e saída fabricada, em nós que transformam conteúdo.
- Nó sem tools que conclui a dizer que não conseguiu.
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
  `output_unavailable` (AOS-494), e o nó do plano que a esperava fica falhado.

## 8. Fora da arquitectura-alvo

- O Jev como dependência de produção.
- Qualquer juiz probabilístico a decidir sucesso, terminação ou qualificação de um modelo.
- Um parser tolerante que converta texto do modelo em tool calls.
- `tool_choice: required` como fundação.
