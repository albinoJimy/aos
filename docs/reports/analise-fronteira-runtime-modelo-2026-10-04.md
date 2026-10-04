# Análise — a fronteira entre o runtime e o modelo (2026-10-04)

> Análise de decisão, sem alterações de código. Cinco perspectivas independentes produziram
> abordagens; dois avaliadores adversariais atacaram-nas, um contra o código e outro contra a
> exigência do dono. Este documento regista a evidência, as candidatas, o que sobreviveu, a
> composição eleita e as decisões que são do dono do produto.

## 1. Resposta curta

- **O defeito de fundo é do AOS, não do modelo.** O loop usa uma só condição para três perguntas
  diferentes: «o modelo não pediu tools», «o run acabou» e «o run acabou bem»
  (`packages/kernel/agent-runtime/loop.go:728`). Qualquer turno sem tool calls fecha o run como
  concluído: texto legítimo, tool call escrita como texto, texto vazio, resposta truncada, recusa.
- **Nenhuma das candidatas iniciais cumpre, sozinha, a exigência «todos os modelos sem atrito».**
  Todas tratam o mesmo eixo (o que fazer quando o modelo não emite a acção). O que hoje impede
  outras famílias de modelos de completar um run com tools é outro eixo: o estado opaco do
  provider que tem de voltar no turno seguinte, e que o gateway deita fora.
- **A composição eleita tem cinco fases** (§7): verdade do desfecho primeiro, recuperação depois de
  medida, estado opaco do provider com uma segunda rota real, proveniência da saída, e uma matriz
  de suporte por classe de modelo como promessa verificável.
- **«Todos os modelos» não é verificável e não deve ser prometido.** Verificável é a matriz da §8.

## 2. Evidência

### 2.1 A série de 2026-10-04 em produção (v0.1.45)

Dez planos com o objectivo «lê o documento `notes` com `doc_read`; num passo dependente, resume em
três pontos», pela fila real.

| | Antes (linha de base de 2026-10-03) | v0.1.45 |
|---|---|---|
| Runs que chamaram tools | 32 | 8 |
| Runs com repetição da mesma tool call | 17 | 0 |
| Tool calls repetidas | 45 de 75 | 0 de 8 |

`/metrics`: `aos_tool_calls_total 8`, `aos_tool_calls_repeated_total 0`,
`aos_runs_hosted_total{assembly_version="1.4.0"} 20`. O problema das repetições (AOS-489/490) está
resolvido nos oito runs que chamaram a tool.

### 2.2 O defeito novo

Em dois dos dez planos (`plan-e2e-v0145-1791115918` e `plan-e2e-v0145-1791117087`) o nó de leitura
terminou num turno, sem nenhuma tool call nativa, com a chamada escrita como texto:

1. `<tool_call id="call_doc_read_notes" name="doc_read">` seguido de um bloco `<aos_args>`. O
   cabeçalho imita a sintaxe de segmento que o protocolo do nó descreve; `<aos_args>` não existe no
   código.
2. Um bloco `<tool_use><invoke name="doc_read">…`, seguido de «a ferramenta não está disponível».
   Este formato não aparece em nada do que o nó envia.

O runtime tratou o texto como conclusão. O `aos-orq` publicou-o como saída do nó
(`plan.payload_published … type=record`), o nó de resumo respondeu que não tinha o documento, e os
dois planos saíram `terminal exit_code=0`. O plano declarava o nó com `tools: ["cap:tool:doc_read"]`.

### 2.3 O que não se sabe

- **Se é regressão.** Antes da v0.1.45, 13 de 86 runs com tools na oferta também concluíram sem
  pedir nenhuma, mas os textos finais já não são legíveis. 2 em 10 tem um intervalo de confiança a
  95% de 5,7% a 51%; 13 em 86 tem 9,1% a 24,2%. Não se distinguem.
- **A causa.** O primeiro caso sugere indução pelo texto do protocolo; o segundo, um hábito do
  modelo (uma tabela de capacidades de terceiros regista o mesmo sintoma para um modelo Kimi de
  código, lido em resumo de pesquisa e por reabrir).
- **Se uma reparação recupera o run.** Nunca foi medido.
- O campo `final` do `turn.recorded` não distingue os casos: os dois verdes falsos têm `final=true`,
  tal como as restantes 95 conclusões sem tool call do histórico.

## 3. Factos do código de que a decisão depende

Verificados por leitura pelo avaliador adversarial (não exercitados):

| Afirmação | Estado | Onde |
|---|---|---|
| Qualquer turno sem tool calls termina o run como concluído | Confirmado | `kernel/agent-runtime/loop.go:728` |
| O `finish_reason` é descartado antes do runtime | Confirmado | `platform/model-gateway/runtime_adapter.go:342` |
| A regra de terminação está duplicada no motor de replay | Confirmado | `kernel/agent-runtime/replay/engine.go:636` |
| A projecção nativa 1.0.0 recusa um turno só-texto a meio do tail | Confirmado | `platform/model-gateway/projection.go:322-327` |
| Um layout que a projecção nativa não conhece cai em silêncio na de texto | Confirmado | `projection.go:76-78` |
| `terminated=false` no run filho já dá nó `failed` e saída 13 | Confirmado | `cmd/aos-orq/node_client.go:65-67`, `node_executor.go:819-822` |
| `tool.call.mediated` é o permit, gravado antes do despacho | Confirmado | `kernel/reference-monitor/monitor.go` |
| O evento de desfecho da tool é opcional e fail-open | Confirmado | `monitor.go:596-613` |
| Após reinício do nó, o `GET /runs/{id}` responde `completed` sem texto e o `aos-orq` publicaria uma saída vazia | Confirmado no código | `cmd/aos/api.go` (~1545), `node_executor.go:611-619` |
| O `notice` do layout 1.4.0 pressupõe vir depois de um `tool_result` | Confirmado | `context_authority.go`, `layout.go:336-337`, ADR-034 |
| O `Decode` do plano e o `POST /runs` recusam campos desconhecidos | Confirmado | `plan/plandocument.go:278`, `cmd/aos/api.go:2790` |
| O gateway retira o raciocínio de todos os pedidos | Confirmado | `port/normalize.go:89,102-119` |
| `port.ToolCall` não tem campo de extensão e o id do provider é descartado | Confirmado | `port/port.go:61-65`, `runtime_adapter.go:335-340` |
| O runtime só usa a chamada síncrona ao modelo | Confirmado | `runtime_adapter.go:218` |
| O LiteLLM de produção tem `drop_params: true` | Confirmado | `deploy/server/litellm/config.yaml:50` |

O `record.digest` do `plan.payload_published` é o hash do texto final calculado pelo próprio
`aos-orq`: prova o transporte, não a origem. O campo `DerivedFrom` existe no schema e não é
preenchido.

## 4. As candidatas

| | Abordagem | Garante | Não garante |
|---|---|---|---|
| K1 | **Rede estrutural.** Desfecho classificado, contrato de conclusão por run vindo do plano, desfecho «não cumprido» que o `aos-orq` trata como nó falhado | Fim do verde falso medido | Recuperação do run; fidelidade da saída |
| K2 | **K1 com reparação.** Um `notice` de corpo constante e mais um turno (layout 1.5.0, projecção nativa 1.1.0) | Possibilidade de recuperar | Que a reparação resulte (por medir) |
| K2′ | **Reamostragem do turno.** Repetir o mesmo pedido em vez de acrescentar um aviso | Recuperação sem layout novo, se resultar | Não foi verificada contra o replay nem contra a captura |
| K3 | **Terminação explícita.** O run só conclui por um acto estruturado do modelo | A conclusão deixa de ser inferida | Põe a conclusão no mesmo canal em que o modelo falha |
| K4 | **Perfil de capacidades e estratégias por modelo**, com suite de conformidade | Um processo para qualificar modelos | Não é correcção; é um epic |
| K5 | **Saída por referência** ao resultado da tool | A saída deriva de facto da tool | Só contratos de passagem directa |
| K6 | **Parser tolerante** de tool calls em texto | Atrito zero quando acerta | Rejeitada por todos (§5.3) |
| K7 | **Estado opaco do provider por turno.** O gateway guarda e devolve o que cada provider exige (raciocínio, assinaturas, ids) | Desbloqueia outras famílias de modelos | Não trata o defeito medido |

K2′ e K7 nasceram na avaliação adversarial.

## 5. O que a avaliação adversarial mudou

### 5.1 Alterações obrigatórias a K1

- **O veredicto calcula-se no kernel**, a partir dos contadores do próprio loop, e sela-se na
  transição terminal com razão própria. Não se deriva do evento de desfecho da tool, que é
  fail-open.
- **Nenhum `status` novo.** O desfecho é `failed` com um `outcome_reason` aditivo em vocabulário
  fechado. Um `status` desconhecido para um `aos-orq` anterior dava saída 8 em laço.
- **O ramo durável do `GET /runs/{id}` devolve o veredicto e a saída.** Sem isso, cada reinício do
  nó dá um vermelho ou uma saída vazia.
- **Saída vazia nunca se publica.**
- **O contrato no `POST /runs` só se envia a um nó que anuncie suportá-lo;** o nó sai primeiro. O
  `POST /runs` recusa campos desconhecidos.
- **A evidência é um vector, não um booleano:** pelo menos uma chamada efectiva por tool exigida,
  e uma classe própria quando o run acaba sobre uma recusa ou uma falha de tool.
- **Modo de observação antes de impor,** com âmbito estreito: nó não-verificador, com tools
  atribuídas e saída de forma aberta.

### 5.2 Condições para a reparação (K2)

- Dispara só com **zero tool calls pedidas no run**. Depois de uma chamada negada, um aviso empurra
  o modelo a repetir a chamada.
- Nunca em resposta truncada. Tecto de uma por run.
- O segmento é um `notice` de corpo constante, sem nomes de tools nem excertos do texto recusado.
  O segmento `correction` é de um humano autenticado e é capturado; reutilizá-lo divergia o replay.
  Duas das perspectivas propunham-no e estavam erradas.
- Layout 1.5.0, projecção nativa 1.1.0 e a lista de layouts que a projecção suporta entram no
  mesmo PR, com emenda ao ADR-034 (o `notice` deixa de vir sempre depois de um `tool_result`).

### 5.3 Porque K6 fica rejeitada

A razão inicial («transforma texto do modelo em autoridade») estava mal fundamentada: uma tool call
derivada de texto passa pelo mesmo Reference Monitor, pela mesma lista-branca e pelos mesmos gates.
O risco real é outro. O nó de leitura transcreve o documento, por isso ecoar texto untrusted é o
seu comportamento normal. Um documento que contenha uma tool call em texto seria executado: a
lista-branca deixa passar `doc_read`, e o `TaintGate` só nega capabilities privilegiadas. Em
produção `cap:fs.read` está armada, o que neste caso negaria a chamada, mas com a consequência de
nenhum documento com esse texto poder ser transcrito.

Um protocolo de texto **declarado** por perfil de modelo (a mensagem inteira é um envelope com
gramática restrita) é admissível para modelos sem canal nativo, com as condições que a perspectiva
de segurança lista. Só se constrói quando existir um modelo concreto dessa classe.

### 5.4 Porque K3 e K4 não entram já

- **K3:** com cerca de 20% de falha por acto nativo, exigir mais um acto para concluir dá uma
  estimativa de 8% a 10% de vermelhos, contra cerca de 4% com reparação (assume independência, não
  medida). Vários modelos com raciocínio não aceitam tool use forçado.
- **K4 completo:** com um só provider, a suite reprovaria o Kimi e escolheria a estratégia de
  reparação, que é K2. Assinar um perfil enquanto o `config.yaml` do LiteLLM pode reencaminhar o
  alias não protege nada. Entra o mínimo: um perfil de rota em código, com o seu digest no
  manifesto do turno.

### 5.5 A medição

A medição de 550 a 1 000 pedidos proposta por uma das perspectivas compara variantes do primeiro
turno. Não mede a pergunta que condiciona a parte cara: **a reparação recupera o run?** Uma
medição de até 150 pedidos, desenhada para essa pergunta e para comparar aviso com reamostragem,
responde ao que é preciso.

### 5.6 O que nenhuma candidata fecha

- Tool call em texto num turno posterior, depois de uma chamada efectiva.
- Tool chamada e saída fabricada, em nós que transformam conteúdo.
- Nó sem tools que conclui a dizer que não conseguiu.
- Desistência depois de uma recusa posterior à primeira leitura boa.
- O proxy reescrever o pedido, ou o alias mudar de modelo, sem o AOS saber.
- O `prompt_hash` não cobre a projecção nativa.

Estes ficam declarados como resíduos. Os dois primeiros só se fecham com K5 ou com um verificador.

## 6. O segundo eixo: o que impede outras famílias de modelos

Independentemente do defeito medido, o gateway hoje não completaria um run com tools em várias
rotas. As quebras de Gemini e Anthropic são inferência a partir do código e da documentação; não
foram medidas.

| Rota | Hoje | Causa no gateway |
|---|---|---|
| OpenAI chat-completions | Funciona | — |
| Kimi (produção) | Verde falso em cerca de 1 em 5 | Terminação implícita |
| DeepSeek com raciocínio | 400 ao segundo turno com tools | O raciocínio é retirado de todos os pedidos |
| Gemini com thought signatures | Quebra ao segundo turno | `port.ToolCall` sem campo de extensão |
| Anthropic com thinking | 400 ou degradação silenciosa | `port.Message` sem lugar para blocos assinados |
| Modelo atrás de vLLM ou Ollama | Verde falso possível | Terminação implícita; ids de tool call |
| Modelo sem tool calling | Verde falso | Não há protocolo de texto declarado |
| Streaming do modelo em runs | Não existe | O runtime só usa a chamada síncrona |

É por isto que K1 a K6 não chegam: nenhuma actua nas linhas 3 a 5.

## 7. Composição eleita

Cada fase entrega valor por si e nenhuma deixa o sistema pior do que hoje.

| Fase | Conteúdo | Depende de |
|---|---|---|
| **F0** | `finish_reason` até ao runtime, à captura e ao `turn.recorded`; regra de terminação num só sítio, partilhada por loop e replay, sem mudar comportamento; contadores (runs concluídos sem tool call com tools na oferta); contrato em modo de observação; as duas amostras de produção como fixtures. Em paralelo, a medição de até 150 pedidos | Autorização da medição |
| **F1** | K1 em imposição, com as alterações da §5.1. O verde falso passa a vermelho honesto (saída 13 com razão) | F0 |
| **F2** | Recuperação: aviso (K2) ou reamostragem (K2′), escolhida pela medição; perfil de rota mínimo com digest no manifesto; higiene do texto do protocolo | F1 e a medição |
| **F3** | K7: estado opaco do provider por turno, qualificado contra uma segunda rota real | Escolha da segunda rota |
| **F4** | K5: saída por referência para contratos de passagem directa | F1 |

F1 não depende de medição nenhuma. Entre F1 e F2, um plano em cinco sai 13 sem recuperação: é o
custo de deixar de reportar sucesso falso.

**Rejeitado:** K6; K3 como fundação; K4 completo agora; a medição de 550 a 1 000 pedidos;
`tool_choice: required` como fundação (suporte incerto e `drop_params: true`); detecção da forma do
texto como critério de controlo (serve só de medição).

## 8. A promessa verificável

Uma matriz de suporte por classe de modelo, com três estados: **qualificada** (com rota e data),
**desenhada e não testada**, **não suportada**. Hoje tem uma linha qualificada (Kimi, com o
defeito conhecido). A classe sem tool calling fica declarada não suportada para nós com tools até
existir o protocolo de texto declarado.

Critérios de «feito» da fronteira: verde falso igual a zero em pelo menos 150 runs; «não cumprido»
abaixo de 2%; replay byte a byte com reparação; cada flag provada nos dois sentidos; e uma segunda
rota de outra família qualificada antes de se afirmar suporte a mais de um modelo.

## 9. Riscos do caminho eleito

1. **Vermelhos falsos.** Se o planeador atribuir tools de que o nó não precisa, o contrato falha
   planos correctos. A taxa não está medida; o modo de observação existe para a medir, e com
   dezenas de runs por semana demora a dar um número.
2. **K2′ não está verificada.** Registar uma tentativa descartada pode custar tanto como K2, e se
   as falhas forem correlacionadas com o prompt a reamostragem não recupera.
3. **A segunda rota chegar de surpresa** por uma edição do `config.yaml` do LiteLLM. O primeiro
   sinal seria um 400 ao segundo turno em produção, e a pressão empurraria para o parser tolerante.
   A rejeição de K6 deve ficar num ADR antes.
4. **Evidência não é fidelidade.** Depois de F1, um nó que chamou a tool e publicou uma saída
   errada continua a passar. Só F4 o fecha, e só para passagem directa.

## 10. Decisões do dono

1. **Classes de modelo que importam e qual é a segunda rota** (condiciona F3).
2. **Autorização para a medição** de até 150 pedidos ao LiteLLM de produção, e para pôr
   `drop_params: false`.
3. **Contrato de conclusão:** inferido em âmbito estreito agora, ou um campo `tool_use:
   required|optional` no schema do plano (versão 1.3.0, com prompt do planeador novo).
4. **Estado durável de «não cumprido»:** `failed` com razão própria (recomendado) ou outro.
5. **Recuperação:** aviso ou reamostragem, depois da medição.
6. **Streaming:** reter o texto até ao veredicto do turno ou transmiti-lo como provisório (só
   quando o streaming entrar).

## 11. Limites desta análise

- Nada foi executado nem medido para além da série da §2.1 e das contagens sobre a cópia local do
  `events.wal`. As afirmações da §3 são leitura de código.
- A documentação de terceiros (Kimi, DeepSeek, Gemini, Anthropic, LiteLLM, runtimes de agentes) foi
  lida em parte através de resumos de pesquisa. Tem de ser reaberta antes de se depender de uma
  frase exacta.
- As estimativas de taxa (20% de base, 4% com reparação, 8% a 10% com K3) assentam em dez planos e
  numa hipótese de independência.
