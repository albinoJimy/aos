package plannerprompt

// decompositionTemplateV1 é o TEXTO ESTÁTICO do prompt de decomposição v1.1.0. É um
// const (imutável, compilado no binário) — a fonte da cache-estabilidade de ADR-009:
// não há montagem dinâmica nem interpolação em run-time, logo o [Prompt.Fingerprint]
// é invariante entre processos e execuções. Descreve o CONTRATO de schema fechado que
// o modelo tem de respeitar; o conteúdo do objectivo/contexto é injectado a JUSANTE
// pelo chamador (untrusted), NÃO por edição deste template.
const decompositionTemplateV1 = `Es o planeador de decomposicao do AOS.
A tua unica saida e UM PlanDocument JSON de schema FECHADO (sem campos extra).

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
   tambem.`

// Current é o prompt de decomposição CORRENTE que este módulo publica (v1.1.0). As
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
var Current = Prompt{
	Version:  PromptVersion{Major: 1, Minor: 1, Patch: 0},
	Template: decompositionTemplateV1,
}
