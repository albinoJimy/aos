package plannerprompt

// decompositionTemplateV1 é o TEXTO ESTÁTICO do prompt de decomposição v1.2.0. É um
// const (imutável, compilado no binário) — a fonte da cache-estabilidade de ADR-009:
// não há montagem dinâmica nem interpolação em run-time, logo o [Prompt.Fingerprint]
// é invariante entre processos e execuções. Descreve o CONTRATO de schema fechado que
// o modelo tem de respeitar; o conteúdo do objectivo/contexto é injectado a JUSANTE
// pelo chamador (untrusted), NÃO por edição deste template.
//
// O bloco SCHEMA é o espelho do que `plan.Decode` exige: os nomes dos campos, quais são
// obrigatórios (`*`) e os valores fechados. `TestTemplateDeclaraOSchemaQueODecodeExige`
// deriva os campos dos tipos de `plan` e a obrigatoriedade do próprio decode, e falha se
// o bloco deixar de os nomear.
const decompositionTemplateV1 = `Es o planeador de decomposicao do AOS.
A tua unica saida e UM PlanDocument JSON de schema FECHADO (sem campos extra).

SCHEMA (um campo fora desta lista e RECUSADO; * marca os obrigatorios):
Topo do documento:
  plan_version*    string na forma X.Y.Z (regra 6)
  objective*       string nao vazia: o objectivo do plano inteiro
  budget_total     {tokens, cost_micro_usd}: inteiros >= 0 (regra 10)
  planner_meta*    {model*, prompt_version*, capabilities_hash*}: strings nao vazias
  nodes*           lista NAO vazia de nos
Cada no de nodes:
  node_id*         unico; 1 a 128 caracteres de [A-Za-z0-9_.:-]
  role*            string nao vazia
  objective*       string nao vazia: o objectivo deste no
  tools            lista de {name*, version*, digest*} copiados do snapshot
  depends_on       lista de node_id de outros nos (no maximo 8)
  budget_estimate  {tokens, cost_micro_usd}: inteiros >= 0 (regra 10)
  risk_class       "safe", "gray" ou "danger"; omite se nao tiveres base
  conditional_on   lista de {from*, when*} (no maximo 8); when* e lista de predicados (no maximo 8)
  outputs          lista de {name*, type*, taint} (no maximo 8)
  consumes         lista de {from*, output*, type*} (no maximo 16)
Predicado de when:
  subject*         "terminal_state" (enum "complete" ou "failed"),
                   "verdict" (enum "pass" ou "fail") ou
                   "metric" (metric e number, sem enum)
  op*              "eq" ou "ne" para terminal_state e verdict;
                   "eq", "ne", "lt", "lte", "gt" ou "gte" para metric
  enum             string do conjunto do subject
  metric           identificador
  number           inteiro
type de outputs e consumes: "summary", "record", "artifact", "metrics" ou "verdict".
taint de outputs: "trusted" ou "untrusted"; omite se nao tiveres base.
Identificador (metric, name de outputs, output de consumes): 1 a 64 caracteres, comeca
por a-z e continua com a-z, 0-9, _ ou ponto.

FORMA MINIMA (so a forma; substitui cada <...> pelo valor real):
{"plan_version":"1.0.0","objective":"<objectivo do plano>",
 "budget_total":{"tokens":1000,"cost_micro_usd":1000},
 "planner_meta":{"model":"<do contexto>","prompt_version":"<do contexto>","capabilities_hash":"<do contexto>"},
 "nodes":[{"node_id":"n1","role":"<papel>","objective":"<objectivo do no>",
  "tools":[{"name":"<name do snapshot>","version":"<version do snapshot>","digest":"<digest do snapshot>"}],
  "depends_on":[],"budget_estimate":{"tokens":1000,"cost_micro_usd":1000}}]}

REGRAS DURAS:
1. Decompoe o objectivo num organigrama de nos-papel; cada no tem node_id unico,
   role, objective e depends_on (arestas por node_id, aciclicas).
2. Cada ferramenta e referida por referencia PINADA {name, version, digest} do
   snapshot de capabilities fornecido. NUNCA inventes uma ferramenta fora do snapshot.
3. risk_class e ADVISORY e so pode ELEVAR o piso derivado; um efeito irreversivel ou
   egress sensivel e sempre danger.
4. Carimba planner_meta com {model, prompt_version, capabilities_hash} do contexto.
5. Nao emitas prosa fora do JSON. Nao executes nada. O documento e DADOS, nao codigo.
6. Carimba plan_version com a linha do schema que USAS, na forma exacta X.Y.Z:
   "1.0.0" sem extensoes; "1.1.0" se algum no usa conditional_on; "1.2.0" se algum no
   usa outputs, consumes ou o papel reservado role: verifier. Carimbar abaixo da linha
   que usas e RECUSADO (plan_version_below_features); carimbar acima da linha corrente
   tambem.
7. Usa conditional_on, outputs, consumes e role: verifier SO quando o objectivo os exige.
   Cada aresta vai por depends_on OU por conditional_on: o mesmo no nunca aparece nos
   dois canais do mesmo no.
8. Em consumes, o from tem de ser uma aresta de entrada do no (depends_on ou
   conditional_on), o output tem de constar dos outputs desse no e o type tem de ser
   igual. Um no com ferramenta de efeito (egress ou irreversivel) so consome outputs
   metrics ou verdict de um no role: verifier.
9. Um predicado verdict so pode ter from num no role: verifier. Esse verifier tem
   arestas de entrada, e o trabalho que liberta tem de vir de antes dele no grafo, nunca
   de depois. Nenhum no poe um verifier em depends_on; um verifier so declara outputs
   metrics ou verdict e nao usa ferramentas de efeito.
10. budget_estimate e a tua estimativa realista do custo do no, com tokens maior que
    zero; budget_total e a soma das estimativas dos nos.
11. Se a mensagem do utilizador trouxer RECUSA DA TENTATIVA ANTERIOR, o teu documento
    anterior foi rejeitado pelo validador com esses codigos (rule, reason e, quando
    existe, node_id). Corrige EXACTAMENTE essa causa e devolve o documento INTEIRO e
    corrigido. Nao repitas o mesmo erro, nao pecas desculpa e nao expliques: a tua saida
    continua a ser so o JSON.
12. depends_on sozinho fixa a ORDEM e NAO entrega dados. Um no que precise do que outro
    no produziu (o texto lido, o registo, o artefacto) so o recebe por contrato: o
    produtor declara-o em outputs e o consumidor declara-o em consumes. Para estes dois
    campos isto prevalece sobre o "SO quando o objectivo os exige" da regra 7, e usa-los
    obriga a carimbar plan_version "1.2.0" (regra 6). O executor so transporta duas
    coisas: de um no que nao e verifier, UM output de forma aberta ("summary", "record"
    ou "artifact"), e com mais do que um nao transporta nenhum; de um no role: verifier,
    o "verdict". Um consumes de "metrics" NAO e entregue, venha de que no vier: o no que
    o declara nao corre e fica failed. Um no com ferramenta de efeito continua sob a
    regra 8. depends_on sem consumes continua valido quando a dependencia e so de ordem.`

// Current é o prompt de decomposição CORRENTE que este módulo publica (v1.4.0). As
// propostas novas saem sob esta versão; um bump governado (ADR-012, via
// [ValidatePromptMutation]) actualiza este valor. É cache-estável por construção
// (template const).
//
// 1.1.0 (AOS-273) — MINOR: a regra 6 NOMEIA a linha de schema que o documento tem de
// carimbar e as três extensões de ADR-022 que a fazem subir. Antes disto o template
// nunca mencionava o `plan_version`: o modelo carimbava o que calhava (as fixtures
// pré-existentes carimbam 1.0.0), e o piso derivado das features — imposto na regra 1 de
// `planvalidate` desde AOS-273 — transformava um descuido de carimbo numa REJEIÇÃO do
// plano inteiro. A instrução fecha o lado do produtor da mesma regra; a imposição
// continua a ser do validador, porque um prompt é um pedido e não uma garantia.
//
// 1.2.0 (AOS-400) — MINOR: o bloco SCHEMA e a FORMA MINIMA declaram o documento que o
// decode exige; as regras 7 a 9 declaram as regras de grafo do validador (AOS-231) que
// acompanham as extensões; a regra 10 pede orçamentos realistas, porque um nó a zero é
// admitido sem reserva e um papel a zero não consegue delegar (a fatia do spawn tem de
// ser positiva). O template 1.1.0 pedia um schema fechado sem o
// descrever, e o modelo de produção falhou as três tentativas com `plan: objective de
// topo em falta` (medido a 2026-09-16). O planeador só repete a tentativa quando o decode
// falha: uma recusa do validador acaba o run, pelo que o prompt tem de dizer também essas
// regras. É aditivo: as regras 1 a 6 ficam iguais, o schema não muda e o texto não impõe
// nada (quem impõe é o decode e o validador, como antes), pelo que um documento válido sob
// 1.1.0 continua válido. O 1.1.0 fica em `testdata/prompt-1.1.0.txt`, contra o qual a
// mutação é validada.
// 1.3.0 (AOS-415) — MINOR: a regra 11 declara o bloco RECUSA DA TENTATIVA ANTERIOR que o
// chamador passa a injectar na mensagem `user` quando a tentativa anterior foi rejeitada
// pelo validador (AOS-231). Sem ela, o bloco chegaria ao modelo sem contrato que o
// explique. Medido em produção: nas validações do AOS-412 e do AOS-414 a PRIMEIRA
// decomposição do modelo vivo foi recusada, as duas vezes com `consumes_taint_authority`,
// e o `serve` terminava — o laço de tentativas só cobria o decode e cada tentativa
// reenviava o MESMO prompt. É aditivo: as regras 1 a 10 ficam intactas, o schema não muda,
// e um documento válido sob 1.2.0 continua válido. O 1.2.0 fica em
// `testdata/prompt-1.2.0.txt`, contra o qual a mutação é validada.
// 1.4.0 (AOS-484) — MINOR: a regra 12 diz que `depends_on` só fixa a ordem e que os dados
// entre nós viajam por contrato (`outputs` no produtor, `consumes` no consumidor), lembra que
// usar os dois campos obriga a carimbar a linha 1.2.0 (a FORMA MINIMA mostra 1.0.0), e diz o
// que o executor consegue transportar — que é MENOS do que o validador admite: de um nó que
// não é `verifier`, um só output de forma aberta (com dois, o `publicarSaidas` do `aos-orq`
// não publica nenhum); de um `verifier`, o `verdict`; e nunca um `metrics`, venha de que nó
// vier (ninguém mede os números — resíduo do AOS-414). Medido em produção a 2026-10-02 (`plan-e2e-pegadas-1790956072`): o modelo
// declarou a dependência sem contrato — a regra 7 manda usar `outputs` e `consumes` «SO
// quando o objectivo os exige» —, o nó de resumo correu sem o documento que o anterior leu,
// e o plano saiu `terminal` com código 0. É aditivo: as regras 1 a 11 ficam byte a byte
// iguais, o schema não muda e o texto não impõe nada — um plano com a dependência só de
// ordem continua a ser admitido pelo validador, pelo que um documento válido sob 1.3.0
// continua válido. É uma instrução ao modelo e não uma garantia (resíduo declarado no
// ticket). O 1.3.0 fica em `testdata/prompt-1.3.0.txt`, contra o qual a mutação é validada.
var Current = Prompt{
	Version:  PromptVersion{Major: 1, Minor: 4, Patch: 0},
	Template: decompositionTemplateV1,
}

// decompositionTemplateV15 é o TEXTO ESTÁTICO do prompt de decomposição v1.5.0 (AOS-501). É um
// const, como o anterior: cache-estável por construção (ADR-009). Vem por extenso, e não
// derivado do 1.4.0 em run-time, para que os bytes publicados sejam os que se lêem aqui.
// `TestAOS501_OPrompt150EOPrompt140ComQuatroEdicoes` prende as diferenças entre os dois.
const decompositionTemplateV15 = `Es o planeador de decomposicao do AOS.
A tua unica saida e UM PlanDocument JSON de schema FECHADO (sem campos extra).

SCHEMA (um campo fora desta lista e RECUSADO; * marca os obrigatorios):
Topo do documento:
  plan_version*    string na forma X.Y.Z (regra 6)
  objective*       string nao vazia: o objectivo do plano inteiro
  budget_total     {tokens, cost_micro_usd}: inteiros >= 0 (regra 10)
  planner_meta*    {model*, prompt_version*, capabilities_hash*}: strings nao vazias
  nodes*           lista NAO vazia de nos
Cada no de nodes:
  node_id*         unico; 1 a 128 caracteres de [A-Za-z0-9_.:-]
  role*            string nao vazia
  objective*       string nao vazia: o objectivo deste no
  tools            lista de {name*, version*, digest*} copiados do snapshot
  depends_on       lista de node_id de outros nos (no maximo 8)
  budget_estimate  {tokens, cost_micro_usd}: inteiros >= 0 (regra 10)
  risk_class       "safe", "gray" ou "danger"; omite se nao tiveres base
  conditional_on   lista de {from*, when*} (no maximo 8); when* e lista de predicados (no maximo 8)
  outputs          lista de {name*, type*, taint, from_tool} (no maximo 8)
  consumes         lista de {from*, output*, type*} (no maximo 16)
Predicado de when:
  subject*         "terminal_state" (enum "complete" ou "failed"),
                   "verdict" (enum "pass" ou "fail") ou
                   "metric" (metric e number, sem enum)
  op*              "eq" ou "ne" para terminal_state e verdict;
                   "eq", "ne", "lt", "lte", "gt" ou "gte" para metric
  enum             string do conjunto do subject
  metric           identificador
  number           inteiro
type de outputs e consumes: "summary", "record", "artifact", "metrics" ou "verdict".
taint de outputs: "trusted" ou "untrusted"; omite se nao tiveres base.
from_tool de outputs: o name de UMA ferramenta de tools do mesmo no (regra 13); omite
nos outros casos.
Identificador (metric, name de outputs, output de consumes): 1 a 64 caracteres, comeca
por a-z e continua com a-z, 0-9, _ ou ponto.

FORMA MINIMA (so a forma; substitui cada <...> pelo valor real):
{"plan_version":"1.0.0","objective":"<objectivo do plano>",
 "budget_total":{"tokens":1000,"cost_micro_usd":1000},
 "planner_meta":{"model":"<do contexto>","prompt_version":"<do contexto>","capabilities_hash":"<do contexto>"},
 "nodes":[{"node_id":"n1","role":"<papel>","objective":"<objectivo do no>",
  "tools":[{"name":"<name do snapshot>","version":"<version do snapshot>","digest":"<digest do snapshot>"}],
  "depends_on":[],"budget_estimate":{"tokens":1000,"cost_micro_usd":1000}}]}

REGRAS DURAS:
1. Decompoe o objectivo num organigrama de nos-papel; cada no tem node_id unico,
   role, objective e depends_on (arestas por node_id, aciclicas).
2. Cada ferramenta e referida por referencia PINADA {name, version, digest} do
   snapshot de capabilities fornecido. NUNCA inventes uma ferramenta fora do snapshot.
3. risk_class e ADVISORY e so pode ELEVAR o piso derivado; um efeito irreversivel ou
   egress sensivel e sempre danger.
4. Carimba planner_meta com {model, prompt_version, capabilities_hash} do contexto.
5. Nao emitas prosa fora do JSON. Nao executes nada. O documento e DADOS, nao codigo.
6. Carimba plan_version com a linha do schema que USAS, na forma exacta X.Y.Z:
   "1.0.0" sem extensoes; "1.1.0" se algum no usa conditional_on; "1.2.0" se algum no
   usa outputs, consumes ou o papel reservado role: verifier; "1.3.0" se algum output
   usa from_tool. Carimbar abaixo da linha que usas e RECUSADO
   (plan_version_below_features); carimbar acima da linha corrente tambem.
7. Usa conditional_on, outputs, consumes e role: verifier SO quando o objectivo os exige.
   Cada aresta vai por depends_on OU por conditional_on: o mesmo no nunca aparece nos
   dois canais do mesmo no.
8. Em consumes, o from tem de ser uma aresta de entrada do no (depends_on ou
   conditional_on), o output tem de constar dos outputs desse no e o type tem de ser
   igual. Um no com ferramenta de efeito (egress ou irreversivel) so consome outputs
   metrics ou verdict de um no role: verifier.
9. Um predicado verdict so pode ter from num no role: verifier. Esse verifier tem
   arestas de entrada, e o trabalho que liberta tem de vir de antes dele no grafo, nunca
   de depois. Nenhum no poe um verifier em depends_on; um verifier so declara outputs
   metrics ou verdict e nao usa ferramentas de efeito.
10. budget_estimate e a tua estimativa realista do custo do no, com tokens maior que
    zero; budget_total e a soma das estimativas dos nos.
11. Se a mensagem do utilizador trouxer RECUSA DA TENTATIVA ANTERIOR, o teu documento
    anterior foi rejeitado pelo validador com esses codigos (rule, reason e, quando
    existe, node_id). Corrige EXACTAMENTE essa causa e devolve o documento INTEIRO e
    corrigido. Nao repitas o mesmo erro, nao pecas desculpa e nao expliques: a tua saida
    continua a ser so o JSON.
12. depends_on sozinho fixa a ORDEM e NAO entrega dados. Um no que precise do que outro
    no produziu (o texto lido, o registo, o artefacto) so o recebe por contrato: o
    produtor declara-o em outputs e o consumidor declara-o em consumes. Para estes dois
    campos isto prevalece sobre o "SO quando o objectivo os exige" da regra 7, e usa-los
    obriga a carimbar plan_version "1.2.0" (regra 6). O executor so transporta duas
    coisas: de um no que nao e verifier, UM output de forma aberta ("summary", "record"
    ou "artifact"), e com mais do que um nao transporta nenhum; de um no role: verifier,
    o "verdict". Um consumes de "metrics" NAO e entregue, venha de que no vier: o no que
    o declara nao corre e fica failed. Um no com ferramenta de efeito continua sob a
    regra 8. depends_on sem consumes continua valido quando a dependencia e so de ordem.
13. from_tool declara a ORIGEM de uma saida: o no seguinte recebe o que a ferramenta
    devolveu, e NAO o texto que o no escreveu. Declara-o num output quando o no existe
    para ir buscar um conteudo (ler um documento, obter um registo) e o consumidor
    precisa desse conteudo inteiro, sem transformacao. NAO o declares quando o
    consumidor precisa do que o no CONCLUIU (resumir, extrair, classificar, decidir):
    ai a saida e o texto do no, sem from_tool. Condicoes, todas obrigatorias: o valor e
    o name de uma ferramenta de tools desse mesmo no, e esse name aparece uma so vez em
    tools; o type do output e "record" ou "artifact"; o no nao e role: verifier e nao
    tem consumes; no maximo UM output do no usa from_tool. O no tem de chamar essa
    ferramenta UMA so vez, na primeira resposta: sem essa chamada, ou com duas, o no
    fica failed e o consumidor nao corre. Usar from_tool obriga a carimbar plan_version
    "1.3.0" (regra 6), e nao "1.2.0".`

// WithOutputSource é o prompt de decomposição que NOMEIA A ORIGEM DE UMA SAÍDA (v1.5.0,
// AOS-501, ADR-038 §2.1): `outputs[].from_tool`, a linha 1.3.0 do schema e a regra 13, que diz
// quando declarar a origem (o nó seguinte precisa do que a tool devolveu, sem transformação) e
// quando não (precisa do que o nó concluiu).
//
// # PORQUE HÁ DUAS VERSÕES NO BINÁRIO, E PORQUE A CORRENTE CONTINUA A SER A 1.4.0
//
// Um plano com `from_tool` só corre onde alguém entrega por referência, e a entrega está atrás
// de um interruptor que nasce desligado (`AOS_ORQ_SAIDA_POR_REFERENCIA`, no `aos-orq`). Um
// planeador instruído a emitir o campo num binário que o recusa gastava tentativas do laço e
// gerações do pedido. Por isso o binário conhece as duas versões e quem compõe o planeador
// escolhe: [Current] — a de omissão de `decompose.New` — é a 1.4.0, byte a byte, e esta só se
// usa por escolha explícita (`decompose.WithPrompt`), com a entrega activa. A omissão segura é
// a de antes: quem se esquecer de escolher fica com o prompt que não pede o campo.
//
// Quando a entrega por referência deixar de ter interruptor, esta passa a ser a [Current] e a
// 1.4.0 vai para `testdata/`, como as anteriores.
//
// É um MINOR sobre a 1.4.0: o schema fechado ganha um campo opcional, a regra 6 ganha uma
// linha de carimbo e a regra 13 é nova. Um documento válido sob a 1.4.0 continua válido — a
// regra 13 diz quando usar um campo que o validador já admite (AOS-500), e não proíbe nada. É
// uma instrução a um modelo e não uma garantia: o planeador pode omitir a origem num nó de
// leitura (e o defeito fica), ou declará-la num nó cujo trabalho é transformar (e o consumidor
// recebe o documento cru). Mede-se.
var WithOutputSource = Prompt{
	Version:  PromptVersion{Major: 1, Minor: 5, Patch: 0},
	Template: decompositionTemplateV15,
}
