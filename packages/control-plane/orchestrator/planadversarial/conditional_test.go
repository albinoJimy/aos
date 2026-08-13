package planadversarial

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// =============================================================================
// VECTOR 6 — CICLO DISFARÇADO DE CONDICIONAL (ADR-022 §5; AOS-270, AOS-244
// alargado). O ADR REJEITOU os ciclos livres por aresta (§3-b): loop-back à
// LangGraph queima tokens sem tecto e destrói o replay. Um plano hostil — ou
// simplesmente um planeador que «aprendeu» o padrão da indústria — tenta
// reintroduzi-los pela porta das traseiras: declara as arestas de precedência
// impecavelmente acíclicas e mete o retorno no CANAL NOVO, `conditional_on`,
// contando que o validador só olhe para `depends_on`.
//
// O que este ficheiro prova é que o canal novo NÃO é uma porta lateral: as
// condicionais entram no MESMO DAG de admissão de AOS-025, pelo que fechar um
// ciclo com uma delas é rejeitado pelo MESMO primitivo — e o nó ofensor nunca
// chega ao sink.
//
// FALHA-ANTES: se `checkAcyclic` ignorasse `conditional_on`, todos os documentos
// deste ficheiro passariam a validação, seriam materializados, e o «ramo de
// revisão» ressuscitaria nós já executados — exactamente o loop-back que o ADR
// congelou como rejeitado.
// =============================================================================

// failBranch é o predicado do «ramo de reprovação»: a forma inocente com que o
// loop-back se apresenta («se o revisor reprovar, volta ao autor»).
func failBranch() []plan.Predicate {
	// `terminal_state` e não `verdict`: o observável `verdict` arrasta a semântica de
	// sistema do papel verificador (AOS-271) e estes vectores provam o CICLO/os
	// tectos, não a atribuição do veredicto — que tem ficheiro próprio
	// (verifier_test.go).
	return []plan.Predicate{{Subject: plan.SubjectTerminalState, Op: plan.OpEq, Enum: plan.EnumFailed}}
}

// cycleDisguisedDoc é o ataque na sua forma mais persuasiva: `draft → review` por
// dependência (acíclico, benigno à vista) e `review → draft` por condição de
// reprovação. O grafo REAL tem um ciclo; o `depends_on`, isolado, não.
func cycleDisguisedDoc() plan.PlanDocument {
	doc := baseValidDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "draft", Role: "writer", Objective: "escreve", Tools: []plan.ToolRef{benignTool()},
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
			// O «loop-back de revisão»: volta a um nó JÁ EXECUTADO do mesmo plano.
			ConditionalOn: []plan.ConditionalEdge{{From: "review", When: failBranch()}}},
		{NodeID: "review", Role: "verifier", Objective: "revê", DependsOn: []string{"draft"},
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10}},
	}
	return doc
}

func TestVector_CicloDisfarcadoDeCondicional_BarradoNaAdmissao(t *testing.T) {
	doc := cycleDisguisedDoc()

	// (i) É DADOS. O documento é sintacticamente impecável: a gramática da condição
	// está dentro do subconjunto fechado e o `depends_on` é acíclico. A defesa NÃO é
	// o parser — tem de ser a validação de grafo.
	mustDecode(t, doc)

	// (ii) VALIDAÇÃO PURA rejeita, e nomeia o canal CULPADO. O sub-código distingue
	// `conditional_cycle` de um `cycle` vulgar: sem isso, o feedback ao planeador
	// mandaria arranjar dependências que já estavam bem.
	v := planvalidate.Validate(doc, advSnapshot(), generousCeilings())
	if !v.Rejected() {
		t.Fatalf("CICLO ACEITE pela validação: o loop-back rejeitado em ADR-022 §3-b entrou pelo canal condicional (%+v)", v)
	}
	if v.Rule != plannerevents.RuleAcyclicity || v.Reason != planvalidate.ReasonConditionalCycle {
		t.Fatalf("veredicto = (%s/%s); queria (acyclicity/conditional_cycle)", v.Rule, v.Reason)
	}

	// (iii) NENHUM EFEITO. Um plano rejeitado não é materializado: o despachante a
	// jusante do gate deixa tudo em espera e o sink não é tocado.
	sink := &spySink{}
	disp, err := plandispatch.NewDispatcher(
		fixedGate{materialized: !v.Rejected()},
		allPendingLifecycle{}, grantingHeadroom{}, clearingCards{}, sink,
	)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := disp.Dispatch(context.Background(), dispatchPlanFrom(doc)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if sink.calls != 0 {
		t.Fatalf("EFEITO INDEVIDO: %d despachos de um plano com ciclo condicional", sink.calls)
	}
}

// TestVector_CicloCondicional_VariantesDoMesmoAtaque — o vector não se fecha com um
// único formato. Estas são as outras formas de escrever o mesmo retorno; todas têm
// de morrer na admissão.
func TestVector_CicloCondicional_VariantesDoMesmoAtaque(t *testing.T) {
	variants := map[string]func() plan.PlanDocument{
		// (a) AUTO-LAÇO: o «retry» de um nó sobre si próprio.
		"auto-laco": func() plan.PlanDocument {
			d := baseValidDoc()
			d.Nodes = []plan.Node{{NodeID: "solo", Role: "r", Objective: "o", Tools: []plan.ToolRef{benignTool()},
				BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
				ConditionalOn:  []plan.ConditionalEdge{{From: "solo", When: failBranch()}}}}
			return d
		},
		// (b) CICLO LONGO: o retorno esconde-se a quatro nós de distância, onde uma
		// inspecção humana do organigrama já não o vê.
		"ciclo-longo": func() plan.PlanDocument {
			d := baseValidDoc()
			d.Nodes = []plan.Node{
				{NodeID: "n1", Role: "r", Objective: "o", Tools: []plan.ToolRef{benignTool()},
					BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
					ConditionalOn:  []plan.ConditionalEdge{{From: "n4", When: failBranch()}}},
				{NodeID: "n2", Role: "r", Objective: "o", DependsOn: []string{"n1"}, BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10}},
				{NodeID: "n3", Role: "r", Objective: "o", DependsOn: []string{"n2"}, BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10}},
				{NodeID: "n4", Role: "r", Objective: "o", DependsOn: []string{"n3"}, BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10}},
			}
			return d
		},
		// (c) CICLO SÓ DE CONDICIONAIS: nenhum `depends_on` sequer — o ciclo vive
		// inteiramente no canal novo.
		"so-condicionais": func() plan.PlanDocument {
			d := baseValidDoc()
			d.Nodes = []plan.Node{
				{NodeID: "x", Role: "r", Objective: "o", Tools: []plan.ToolRef{benignTool()},
					BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
					ConditionalOn:  []plan.ConditionalEdge{{From: "y", When: failBranch()}}},
				{NodeID: "y", Role: "r", Objective: "o", BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
					ConditionalOn: []plan.ConditionalEdge{{From: "x", When: failBranch()}}},
			}
			return d
		},
	}
	for name, mk := range variants {
		t.Run(name, func(t *testing.T) {
			doc := mk()
			mustDecode(t, doc)
			v := planvalidate.Validate(doc, advSnapshot(), generousCeilings())
			if v.Reason != planvalidate.ReasonConditionalCycle {
				t.Fatalf("variante %q: reason = %q; queria conditional_cycle (%+v)", name, v.Reason, v)
			}
		})
	}
}

// TestVector_CondicionalNaoEscapaAosTectos — a segunda cara do mesmo ataque: já que
// o ciclo não passa, tenta-se a EXAUSTÃO pelo canal novo (fan-out gigante declarado
// em `conditional_on` em vez de `depends_on`). Os tectos estruturais contam a UNIÃO
// dos canais, pelo que o desvio não compra nada.
//
// FALHA-ANTES: se o fanout só contasse `depends_on`, este plano passava com um
// out-degree de 5 sob um tecto de 2.
func TestVector_CondicionalNaoEscapaAosTectos(t *testing.T) {
	doc := baseValidDoc()
	nodes := []plan.Node{{NodeID: "root", Role: "r", Objective: "o", Tools: []plan.ToolRef{benignTool()},
		BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10}}}
	for _, id := range []string{"f1", "f2", "f3", "f4", "f5"} {
		nodes = append(nodes, plan.Node{NodeID: id, Role: "r", Objective: "o",
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
			ConditionalOn:  []plan.ConditionalEdge{{From: "root", When: failBranch()}}})
	}
	doc.Nodes = nodes
	mustDecode(t, doc)

	ceil := generousCeilings()
	ceil.MaxFanout = 2
	v := planvalidate.Validate(doc, advSnapshot(), ceil)
	if v.Rule != plannerevents.RuleStructuralCeiling || v.Reason != planvalidate.ReasonMaxFanoutExceeded {
		t.Fatalf("fan-out de exaustão pelo canal condicional NÃO barrado: (%s/%s)", v.Rule, v.Reason)
	}
}

// TestVector_CondicionalNaoEcoaConteudoUntrusted — o feedback de uma rejeição de
// aresta condicional continua ALLOWLISTED: o marcador de injecção que o plano
// esconde nos campos de texto livre NÃO aparece em nenhuma superfície do veredicto.
func TestVector_CondicionalNaoEcoaConteudoUntrusted(t *testing.T) {
	doc := cycleDisguisedDoc()
	doc.Objective = injMarker
	doc.Nodes[0].Objective = injMarker
	doc.Nodes[0].Role = injMarker

	v := planvalidate.Validate(doc, advSnapshot(), generousCeilings())
	if !v.Rejected() {
		t.Fatal("pré-condição: o documento devia continuar rejeitado")
	}
	if rendered := renderVerdict(v); contains(rendered, injMarker) {
		t.Fatalf("VAZAMENTO: conteúdo untrusted no veredicto de uma rejeição condicional: %s", rendered)
	}
}

// contains é um `strings.Contains` local (o ficheiro evita importar `strings` só
// para uma asserção).
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
