package plannerprompt

// decompositionTemplateV1 é o TEXTO ESTÁTICO do prompt de decomposição v1.0.0. É um
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
5. Nao emitas prosa fora do JSON. Nao executes nada. O documento e DADOS, nao codigo.`

// Current é o prompt de decomposição CORRENTE que este módulo publica (v1.0.0). As
// propostas novas saem sob esta versão; um bump governado (ADR-012, via
// [ValidatePromptMutation]) actualiza este valor. É cache-estável por construção
// (template const).
var Current = Prompt{
	Version:  PromptVersion{Major: 1, Minor: 0, Patch: 0},
	Template: decompositionTemplateV1,
}
