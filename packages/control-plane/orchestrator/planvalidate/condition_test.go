package planvalidate

import (
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// condition_test.go — ARESTAS CONDICIONAIS no VALIDADOR PURO (AOS-270, ADR-022
// §2.1/§5). O que estes testes provam não é «há uma regra nova»: é que o campo
// novo atravessa as regras que JÁ existiam — integridade referencial (regra 1),
// aciclicidade REUTILIZADA do DAG de AOS-025 (regra 2) e tectos estruturais
// (regra 4) — sem abrir uma porta lateral em nenhuma delas.

// passTerminal é o predicado canónico destes testes: «a origem concluiu».
//
// PORQUE `terminal_state` E NÃO `verdict`: o observável `verdict` arrasta consigo a
// semântica de sistema do papel verificador (AOS-271, verifier.go) — usá-lo aqui
// faria estes testes provarem a regra errada (rejeitariam por atribuição do
// veredicto, e não pelo ciclo/tecto que cada um existe para provar). As regras do
// veredicto têm testes PRÓPRIOS (verifier_test.go).
func passTerminal() []plan.Predicate {
	return []plan.Predicate{{Subject: plan.SubjectTerminalState, Op: plan.OpEq, Enum: plan.EnumComplete}}
}

// failTerminal é o ramo de reprovação (a origem falhou).
func failTerminal() []plan.Predicate {
	return []plan.Predicate{{Subject: plan.SubjectTerminalState, Op: plan.OpEq, Enum: plan.EnumFailed}}
}

// branchingDoc é o organigrama LEGÍTIMO de ADR-022 §2.1: `a` produz, `v` verifica,
// e dois ramos DECLARADOS À PRIORI consomem o veredicto — `ok` no caminho feliz,
// `fix` no caminho de reprovação. Ambos apontam para nós AINDA NÃO EXECUTADOS, e o
// grafo continua acíclico.
func branchingDoc() plan.PlanDocument {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "producer", Objective: "produz", Tools: []plan.ToolRef{searchTool()}},
		{NodeID: "v", Role: "verifier", Objective: "verifica", DependsOn: []string{"a"}},
		{NodeID: "ok", Role: "r", Objective: "publica", ConditionalOn: []plan.ConditionalEdge{{From: "v", When: passTerminal()}}},
		{NodeID: "fix", Role: "r", Objective: "corrige", ConditionalOn: []plan.ConditionalEdge{{From: "v", When: failTerminal()}}},
	}
	return doc
}

// TestConditionalBranchAccepted — o caso LEGÍTIMO passa. Sem este teste, os
// negativos abaixo poderiam estar a passar por acidente (uma regra que rejeitasse
// tudo).
func TestConditionalBranchAccepted(t *testing.T) {
	doc := branchingDoc()
	mustBeShapeValid(t, doc)
	if v := Validate(doc, baseSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("organigrama com ramos legítimos rejeitado: %+v", v)
	}
}

// TestConditionalCannotCloseCycle — «CICLO DISFARÇADO DE CONDICIONAL» (ADR-022 §5).
// O plano declara a cadeia a→b por `depends_on` e depois um «ramo de revisão» de
// volta a `a` por `conditional_on`. Estruturalmente é um ciclo — e é REJEITADO
// pelo MESMO primitivo de AOS-025 que já rejeita um `depends_on` cíclico.
//
// FALHA-ANTES: se as arestas condicionais NÃO entrassem no DAG de admissão, este
// documento passava a validação e o loop-back rejeitado pelo ADR (§3-b) entrava
// pela porta das traseiras.
func TestConditionalCannotCloseCycle(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "r", Objective: "oa", Tools: []plan.ToolRef{searchTool()},
			ConditionalOn: []plan.ConditionalEdge{{From: "b", When: failTerminal()}}},
		{NodeID: "b", Role: "r", Objective: "ob", DependsOn: []string{"a"}},
	}
	mustBeShapeValid(t, doc) // o documento é FORMA válida: a defesa é semântica

	v := Validate(doc, baseSnapshot(), Ceilings{})
	if !v.Rejected() {
		t.Fatal("ciclo disfarçado de condicional ACEITE: o loop-back de §3-b entrou pela porta lateral")
	}
	if v.Rule != plannerevents.RuleAcyclicity || v.Reason != ReasonConditionalCycle {
		t.Fatalf("veredicto = (%s/%s); queria (acyclicity/conditional_cycle)", v.Rule, v.Reason)
	}
}

// TestConditionalSelfLoopRejected — a forma mais curta do mesmo ataque: o nó
// condiciona-se a si próprio (um «retry» disfarçado). O auto-laço é ciclo.
func TestConditionalSelfLoopRejected(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "r", Objective: "oa", Tools: []plan.ToolRef{searchTool()},
			ConditionalOn: []plan.ConditionalEdge{{From: "a", When: failTerminal()}}},
	}
	mustBeShapeValid(t, doc)
	if v := Validate(doc, baseSnapshot(), Ceilings{}); v.Reason != ReasonConditionalCycle {
		t.Fatalf("auto-laço condicional: reason = %q; queria conditional_cycle", v.Reason)
	}
}

// TestConditionalCycleLongerThanTwo — o ciclo pode ser LONGO e passar por vários
// nós, com a condicional a fechá-lo no fim. A aciclicidade incremental apanha-o
// na aresta que fecha, sem detector novo nem limite de comprimento.
func TestConditionalCycleLongerThanTwo(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "r", Objective: "oa", Tools: []plan.ToolRef{searchTool()},
			ConditionalOn: []plan.ConditionalEdge{{From: "d", When: failTerminal()}}},
		{NodeID: "b", Role: "r", Objective: "ob", DependsOn: []string{"a"}},
		{NodeID: "c", Role: "r", Objective: "oc", DependsOn: []string{"b"}},
		{NodeID: "d", Role: "r", Objective: "od", DependsOn: []string{"c"}},
	}
	mustBeShapeValid(t, doc)
	if v := Validate(doc, baseSnapshot(), Ceilings{}); v.Reason != ReasonConditionalCycle {
		t.Fatalf("ciclo de comprimento 4 fechado por condicional: reason = %q; queria conditional_cycle", v.Reason)
	}
}

// TestPureDependencyCycleStillReportsPlainCycle — o feedback nomeia o canal
// CULPADO: um ciclo que já existe só nas dependências continua a ser [ReasonCycle],
// não `conditional_cycle`. É o que dá valor ao sub-código novo (e o que a ordem em
// duas passagens de [checkAcyclic] existe para garantir).
func TestPureDependencyCycleStillReportsPlainCycle(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "r", Objective: "oa", Tools: []plan.ToolRef{searchTool()}, DependsOn: []string{"b"}},
		{NodeID: "b", Role: "r", Objective: "ob", DependsOn: []string{"a"}},
		// Aresta condicional PERFEITAMENTE legítima e alheia ao ciclo: se a ordem das
		// duas passagens estivesse trocada, esta seria acusada do ciclo que não fechou.
		{NodeID: "c", Role: "r", Objective: "oc", ConditionalOn: []plan.ConditionalEdge{{From: "a", When: passTerminal()}}},
	}
	v := Validate(doc, baseSnapshot(), Ceilings{})
	if v.Rule != plannerevents.RuleAcyclicity || v.Reason != ReasonCycle {
		t.Fatalf("veredicto = (%s/%s); queria (acyclicity/cycle) — o canal culpado é o depends_on", v.Rule, v.Reason)
	}
}

// TestDanglingConditionalRejected — a origem tem de existir NO MESMO PLANO. Uma
// condição sobre um nó de fora do documento não é uma aresta: é um oráculo externo
// («declarados à priori», ADR-022 §2.1).
func TestDanglingConditionalRejected(t *testing.T) {
	doc := branchingDoc()
	doc.Nodes[2].ConditionalOn[0].From = "nao-existe"
	mustBeShapeValid(t, doc)
	v := Validate(doc, baseSnapshot(), Ceilings{})
	if v.Rule != plannerevents.RuleSchema || v.Reason != ReasonDanglingConditional {
		t.Fatalf("veredicto = (%s/%s); queria (schema/dangling_conditional)", v.Rule, v.Reason)
	}
	if v.Locator.NodeID != "ok" {
		t.Fatalf("locator = %q; queria o nó ofensor \"ok\"", v.Locator.NodeID)
	}
}

// TestConditionalShadowingDependencyRejected — a mesma origem nos DOIS canais é
// recusada: as semânticas de espera são diferentes (conclusão vs terminalidade +
// predicado), e a sobreposição esconde do revisor humano qual delas vale.
func TestConditionalShadowingDependencyRejected(t *testing.T) {
	doc := branchingDoc()
	doc.Nodes[2].DependsOn = []string{"v"} // já é conditional_on "v"
	mustBeShapeValid(t, doc)
	if v := Validate(doc, baseSnapshot(), Ceilings{}); v.Reason != ReasonConditionalShadowsDependency {
		t.Fatalf("reason = %q; queria conditional_shadows_dependency", v.Reason)
	}
}

// TestConditionalCountsTowardCeilings — os tectos estruturais (regra 4) contam a
// UNIÃO dos dois canais. FALHA-ANTES: se só `depends_on` contasse, um plano de
// exaustão escapava ao MaxFanout/MaxDepth apenas por declarar as arestas no outro
// canal — mesmo grafo, mesmo custo, tecto nenhum.
func TestConditionalCountsTowardCeilings(t *testing.T) {
	t.Run("fanout", func(t *testing.T) {
		doc := baseDoc()
		nodes := []plan.Node{{NodeID: "a", Role: "r", Objective: "oa", Tools: []plan.ToolRef{searchTool()}}}
		for _, id := range []string{"c1", "c2", "c3"} {
			nodes = append(nodes, plan.Node{NodeID: id, Role: "r", Objective: "o",
				ConditionalOn: []plan.ConditionalEdge{{From: "a", When: passTerminal()}}})
		}
		doc.Nodes = nodes
		mustBeShapeValid(t, doc)
		v := Validate(doc, baseSnapshot(), Ceilings{MaxFanout: 2})
		if v.Reason != ReasonMaxFanoutExceeded {
			t.Fatalf("reason = %q; queria max_fanout_exceeded (out-degree 3 > 2 via condicionais)", v.Reason)
		}
	})
	t.Run("profundidade", func(t *testing.T) {
		doc := baseDoc()
		doc.Nodes = []plan.Node{
			{NodeID: "a", Role: "r", Objective: "oa", Tools: []plan.ToolRef{searchTool()}},
			{NodeID: "b", Role: "r", Objective: "ob", ConditionalOn: []plan.ConditionalEdge{{From: "a", When: passTerminal()}}},
			{NodeID: "c", Role: "r", Objective: "oc", ConditionalOn: []plan.ConditionalEdge{{From: "b", When: passTerminal()}}},
		}
		mustBeShapeValid(t, doc)
		if v := Validate(doc, baseSnapshot(), Ceilings{MaxDepth: 2}); v.Reason != ReasonMaxDepthExceeded {
			t.Fatalf("reason = %q; queria max_depth_exceeded (cadeia de 3 via condicionais)", v.Reason)
		}
	})
}

// TestValidateStaysDeterministicWithConditionals — determinismo: o mesmo input dá
// sempre o mesmo veredicto (o [Verdict] é comparável por ==).
func TestValidateStaysDeterministicWithConditionals(t *testing.T) {
	doc := branchingDoc()
	doc.Nodes[3].ConditionalOn[0].From = "nao-existe"
	first := Validate(doc, baseSnapshot(), Ceilings{})
	for i := 0; i < 50; i++ {
		if got := Validate(doc, baseSnapshot(), Ceilings{}); got != first {
			t.Fatalf("veredicto instável na iteração %d: %+v != %+v", i, got, first)
		}
	}
}

// ===========================================================================
// REMEDIAÇÃO da auditoria adversarial da wave — as duas regras de ADMISSÃO que
// faltavam às arestas condicionais (condition.go).
// ===========================================================================

// TestUnreachableJunctionRejected — o «ponto de junção depois de um ramo»: um nó a
// jusante de DOIS ramos mutuamente exclusivos sobre a MESMA origem é inalcançável
// em qualquer execução, e é recusado na ADMISSÃO.
//
// FALHA-ANTES: o validador admitia-o (não havia regra de alcançabilidade), o humano
// aprovava no gate um organigrama cuja cauda — o OBJECTIVO do plano — nunca correria,
// e a impossibilidade só aparecia como contadores `NotTaken` numa passagem de
// despacho. Ver a nota de [checkBranchReachability].
func TestUnreachableJunctionRejected(t *testing.T) {
	doc := branchingDoc()
	// `publish` junta os dois ramos exclusivos — a junção natural que um planeador
	// escreve, e que a poda mata sempre.
	doc.Nodes = append(doc.Nodes, plan.Node{
		NodeID: "publish", Role: "r", Objective: "publica", DependsOn: []string{"ok", "fix"},
	})
	mustBeShapeValid(t, doc)
	v := Validate(doc, baseSnapshot(), Ceilings{})
	if v.Rule != plannerevents.RuleSchema || v.Reason != ReasonUnreachableJunction {
		t.Fatalf("veredicto = (%s/%s); queria (schema/unreachable_junction)", v.Rule, v.Reason)
	}
	if v.Locator.NodeID != "publish" {
		t.Fatalf("locator = %q; queria o nó inalcançável \"publish\"", v.Locator.NodeID)
	}
}

// TestJunctionViaConditionalChannelAlsoRejected — a poda propaga-se pelos DOIS
// canais de aresta, pelo que declarar a junção como `conditional_on` sobre ambos os
// ramos não a salva (a conjunção exige as duas arestas satisfeitas). A regra tem de
// a apanhar pelo mesmo motivo.
func TestJunctionViaConditionalChannelAlsoRejected(t *testing.T) {
	doc := branchingDoc()
	doc.Nodes = append(doc.Nodes, plan.Node{
		NodeID: "publish", Role: "r", Objective: "publica",
		ConditionalOn: []plan.ConditionalEdge{
			{From: "ok", When: passTerminal()},
			{From: "fix", When: passTerminal()},
		},
	})
	mustBeShapeValid(t, doc)
	if v := Validate(doc, baseSnapshot(), Ceilings{}); v.Reason != ReasonUnreachableJunction {
		t.Fatalf("reason = %q; queria unreachable_junction", v.Reason)
	}
}

// TestReachableTailPerBranchAccepted — a forma CORRECTA de escrever a junção
// (cauda duplicada por ramo) continua a passar. Sem este teste, a regra podia estar
// a rejeitar tudo o que tem dois ramos — e a extensão ficava inútil.
func TestReachableTailPerBranchAccepted(t *testing.T) {
	doc := branchingDoc()
	doc.Nodes = append(doc.Nodes,
		plan.Node{NodeID: "publish-ok", Role: "r", Objective: "publica", DependsOn: []string{"ok"}},
		plan.Node{NodeID: "publish-fix", Role: "r", Objective: "publica", DependsOn: []string{"fix"}},
	)
	mustBeShapeValid(t, doc)
	if v := Validate(doc, baseSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("cauda duplicada por ramo (a junção correcta) rejeitada: %+v", v)
	}
}

// TestNonExclusiveBranchesJoinAccepted — dois ramos sobre a MESMA origem que NÃO se
// excluem (o mesmo predicado) podem juntar-se: a regra é SÓ sobre exclusão mútua
// provada, nunca sobre «ter dois ramos».
func TestNonExclusiveBranchesJoinAccepted(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "r", Objective: "oa", Tools: []plan.ToolRef{searchTool()}},
		{NodeID: "b1", Role: "r", Objective: "o", ConditionalOn: []plan.ConditionalEdge{{From: "a", When: passTerminal()}}},
		{NodeID: "b2", Role: "r", Objective: "o", ConditionalOn: []plan.ConditionalEdge{{From: "a", When: passTerminal()}}},
		{NodeID: "j", Role: "r", Objective: "junta", DependsOn: []string{"b1", "b2"}},
	}
	mustBeShapeValid(t, doc)
	if v := Validate(doc, baseSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("junção de ramos NÃO-exclusivos rejeitada: %+v", v)
	}
}
