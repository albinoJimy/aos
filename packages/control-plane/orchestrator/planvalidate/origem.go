package planvalidate

import (
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// origem.go — A ORIGEM DECLARADA DE UMA SAÍDA (ADR-038 §2.1, AOS-500).
//
// Um output pode declarar `from_tool`: o nome de uma tool do mesmo nó de que essa saída é o
// resultado. A declaração é o que, mais tarde, faz o consumidor receber o que a tool devolveu em
// vez do que o modelo escreveu sobre isso (a entrega é do AOS-501). Aqui decide-se só se a
// declaração é SUSTENTÁVEL pela estrutura do plano — sem ler texto, sem snapshot e sem grafo.
//
// As regras, por ordem fixa em cada nó que declara:
//
//	(O1) o nó não é verificador — a saída de um verificador é o veredicto, derivado pelo sistema;
//	(O2) o nó não tem `consumes` — um nó com entradas tem o contexto untrusted desde o primeiro
//	     turno, e o kernel nunca designa a chamada como origem (ADR-038 §2.2: `inapplicable`).
//	     Aceitá-lo era aprovar um plano que só pode falhar;
//	(O3) no máximo UMA saída com origem — um run tem uma só origem designada;
//	(O4) só em `record` ou `artifact` — um `summary` é, por definição, transformado, e as formas
//	     fechadas são derivadas pelo sistema;
//	(O5) `from_tool` é o nome EXACTO de uma tool de `tools` do mesmo nó;
//	(O6) essa tool está pinada UMA SÓ VEZ no nó — a origem refere-a pelo nome, e duas `ToolRef`
//	     com o mesmo nome (versões ou digests diferentes) deixavam-na ambígua;
//	(O7) o nó que declara a origem de uma saída NÃO declara outra saída de forma aberta
//	     (`summary`, `record` ou `artifact` sem `from_tool`) — o NÓ MISTO não existe (AOS-501,
//	     revisão adversarial de 2026-10-06, achado I4). Uma saída aberta sem origem publica-se do
//	     texto final do run: no mesmo nó que promete o resultado da tool, o consumidor que a
//	     consumisse recebia o resumo do modelo vindo de um nó que declarou a origem — o defeito
//	     que a origem existe para fechar, e sem que nenhuma métrica o contasse. Uma saída de
//	     forma fechada (`metrics`) ao lado da origem continua admitida: nunca se publica do texto.
//
// A ORIGEM PODE SER QUALQUER TOOL DO NÓ, também uma de egress ou de efeito (`web_post`, por
// exemplo): é decisão do dono, e o limite que ela traz está declarado no ADR-038 §5 — quem
// controla a resposta de uma tool que não corre na sandbox escolhe o texto entregue.
//
// A sexta regra do ticket — usar o campo obriga a carimbar a linha 1.3.0 — é do piso de versão
// derivado das features ([plan.FeatureFloor], regra 1), com o sub-código que já existia.
//
// O QUE NÃO SE RECUSA: um nó que, pela estrutura, PODIA declarar a origem e não o faz. A origem
// declara-se, não se infere (ADR-038 §2.1); um plano sem o campo valida como sempre.
//
// O TAINT NÃO MUDA. Uma saída com origem continua `untrusted` ([plan.Node.EffectiveOutputTaint]
// não lê o campo), e a regra (P4) de payload.go continua a impedir um consumidor privilegiado de
// a consumir.

// checkOutputSources — REGRA 1-quinquies. Pura e determinística: itera nós e saídas pela ordem
// do slice. O [Locator] aponta ao nó que declara, que é o que o re-planeamento tem de corrigir.
func checkOutputSources(doc plan.PlanDocument) Verdict {
	for _, n := range doc.Nodes {
		if !n.DeclaresOutputSource() {
			// Sem origem declarada não há nada a decidir: o caminho de sempre.
			continue
		}
		loc := Locator{NodeID: n.NodeID}
		// (O1)
		if n.IsVerifier() {
			return reject(plannerevents.RuleSchema, ReasonFromToolOnVerifier, loc)
		}
		// (O2)
		if len(n.Consumes) != 0 {
			return reject(plannerevents.RuleSchema, ReasonFromToolWithConsumes, loc)
		}
		// (O3)
		declaradas := 0
		for _, o := range n.Outputs {
			if o.FromTool != "" {
				declaradas++
			}
		}
		if declaradas > 1 {
			return reject(plannerevents.RuleSchema, ReasonFromToolMultiple, loc)
		}
		for _, o := range n.Outputs {
			if o.FromTool == "" {
				continue
			}
			// (O4)
			if o.Type != plan.PayloadRecord && o.Type != plan.PayloadArtifact {
				return reject(plannerevents.RuleSchema, ReasonFromToolOutputType, loc)
			}
			// (O5) A forma do identificador já é da forma ([plan.Decode]); repete-se aqui
			// porque o validador também recebe documentos montados à mão.
			pinadas := toolRefsNamed(n, o.FromTool)
			if !plan.ValidIdentifier(o.FromTool) || pinadas == 0 {
				return reject(plannerevents.RuleSchema, ReasonFromToolUnknownTool, loc)
			}
			// (O6)
			if pinadas > 1 {
				return reject(plannerevents.RuleSchema, ReasonFromToolAmbiguousTool, loc)
			}
		}
		// (O7) Depois das regras sobre a saída COM origem: um nó que as viole morre pela razão
		// dessa saída, e só um nó cuja origem é sustentável é acusado de a misturar com texto.
		for _, o := range n.Outputs {
			if o.FromTool == "" && !o.Type.ClosedForm() {
				return reject(plannerevents.RuleSchema, ReasonFromToolWithTextOutput, loc)
			}
		}
	}
	return accepted
}

// toolRefsNamed conta as `ToolRef` do nó com este nome EXACTO. Comparação byte a byte, sem
// normalização: é o nome que o run vai declarar ao kernel, e o kernel compara assim. Zero quer
// dizer que a tool não é do nó (O5); mais de uma, que a origem é ambígua (O6).
func toolRefsNamed(n plan.Node, name string) int {
	pinadas := 0
	for _, t := range n.Tools {
		if t.Name == name {
			pinadas++
		}
	}
	return pinadas
}
