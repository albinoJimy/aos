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
    zero; budget_total e a soma das estimativas dos nos.`

// Current é o prompt de decomposição CORRENTE que este módulo publica (v1.2.0). As
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
var Current = Prompt{
	Version:  PromptVersion{Major: 1, Minor: 2, Patch: 0},
	Template: decompositionTemplateV1,
}
