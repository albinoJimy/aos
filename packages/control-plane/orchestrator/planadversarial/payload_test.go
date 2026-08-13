package planadversarial

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// payload_test.go — VECTOR 8: PAYLOAD COM TAINT ELEVADO PARA CONSUMIDOR PRIVILEGIADO
// (ADR-022 §5, AOS-272 AC3).
//
// O vector que o ADR nomeia é um: «payload com *taint* elevado para consumidor
// privilegiado». Os testes abaixo esgotam os DISFARCES com que um plano hostil o
// tentaria — declarar o taint que lhe convém, esconder a origem numa aresta que
// ninguém declarou, mentir no tipo — e o padrão é sempre o mesmo, e é o que interessa:
//
//	(a) o documento PASSA A FORMA — a defesa não é o parser, é a admissão;
//	(b) a validação REJEITA com sub-código ATRIBUÍVEL — o re-planeamento sabe o que
//	    corrigir;
//	(c) o sink NÃO É TOCADO — nenhum spawn, nenhum token queimado.

// exfilPlanDoc é o organigrama do ataque: `harvest` recolhe material (um resumo —
// untrusted por derivação, porque carrega trabalho, que é saída de modelo) e `exfil`
// consome-o com uma tool de EGRESS pinada. É a injecção indirecta clássica com o
// carimbo de um contrato: o conteúdo untrusted a comandar a acção privilegiada.
func exfilPlanDoc() plan.PlanDocument {
	doc := baseValidDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "harvest", Role: "r", Objective: "recolhe", Tools: []plan.ToolRef{benignTool()},
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
			Outputs:        []plan.Output{{Name: "achados", Type: plan.PayloadSummary}}},
		{NodeID: "exfil", Role: "r", Objective: "publica", DependsOn: []string{"harvest"},
			Tools:          []plan.ToolRef{deleteTool()}, // egress externo + irreversível
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
			Consumes:       []plan.PayloadEdge{{From: "harvest", Output: "achados", Type: plan.PayloadSummary}}},
	}
	return doc
}

// assertHostilePlanDies corre o padrão completo: forma OK, validação rejeita com o
// sub-código esperado, e o despacho — condicionado ao veredicto real, como em
// produção — não entrega nó nenhum ao sink.
func assertHostilePlanDies(t *testing.T, doc plan.PlanDocument, want planvalidate.Reason) {
	t.Helper()
	mustDecode(t, doc) // (a) o documento hostil é DADOS válidos

	v := planvalidate.Validate(doc, advSnapshot(), generousCeilings())
	if !v.Rejected() { // (b) rejeição atribuível
		t.Fatalf("plano hostil devia ser rejeitado, veio: %+v", v)
	}
	if v.Reason != want {
		t.Fatalf("sub-código = %q, quer %q (o feedback tem de dizer o que corrigir)", v.Reason, want)
	}

	// (c) o gate só materializa o que a validação aceitou; nada despacha.
	sink := &spySink{}
	d, err := plandispatch.NewDispatcher(fixedGate{materialized: !v.Rejected()}, allPendingLifecycle{},
		grantingHeadroom{}, clearingCards{}, sink)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := d.Dispatch(context.Background(), dispatchPlanFrom(doc)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if sink.calls != 0 {
		t.Fatalf("plano rejeitado NÃO devia despachar; sink foi chamado %d vezes (%v)", sink.calls, sink.nodes)
	}
}

// TestVector8_UntrustedPayloadToPrivilegedConsumer — o vector nomeado pelo ADR.
//
// FALHA-ANTES: o plano era admitido, o humano aprovava no gate um organigrama onde um
// nó com egress recebe um resumo de material recolhido, e a barreira P0 de ADR-005 só
// disparava no RM — depois do spawn, depois dos tokens, e como uma negação isolada que
// não dizia ao operador que o organigrama a garantia desde o primeiro nó.
func TestVector8_UntrustedPayloadToPrivilegedConsumer(t *testing.T) {
	assertHostilePlanDies(t, exfilPlanDoc(), planvalidate.ReasonConsumesTaintAuthority)
}

// TestVector8_TaintLaunderedByDeclaration — DISFARCE 1: declarar o taint que convém.
// O rótulo do documento é untrusted como o resto do documento e SÓ ELEVA; o piso
// derivado do TIPO vence. Não há caminho por onde o planeador baixe um rótulo.
func TestVector8_TaintLaunderedByDeclaration(t *testing.T) {
	doc := exfilPlanDoc()
	doc.Nodes[0].Outputs = []plan.Output{{Name: "achados", Type: plan.PayloadSummary, Taint: plan.TaintTrusted}}
	assertHostilePlanDies(t, doc, planvalidate.ReasonConsumesTaintAuthority)
}

// TestVector8_TaintLaunderedByTypeMismatch — DISFARCE 2: pedir o mesmo output com um
// TIPO de forma fechada (`metrics`, que teria piso trusted), na esperança de que a
// compatibilidade de taint fosse avaliada sobre o tipo PEDIDO em vez do DECLARADO pelo
// produtor. Morre antes, na identidade de tipos.
func TestVector8_TaintLaunderedByTypeMismatch(t *testing.T) {
	doc := exfilPlanDoc()
	doc.Nodes[1].Consumes = []plan.PayloadEdge{{From: "harvest", Output: "achados", Type: plan.PayloadMetrics}}
	assertHostilePlanDies(t, doc, planvalidate.ReasonConsumesTypeMismatch)
}

// TestVector8_HiddenDataEdge — DISFARCE 3: a aresta de dados sem aresta de execução.
// O consumidor privilegiado deixa de declarar a dependência (para o taint «não vir de
// lado nenhum») e lê o output à mesma. FALHA-ANTES: `consumes` seria um canal de
// aresta invisível ao DAG de admissão — leitura em corrida com o produtor, e um grafo
// de dados capaz de fechar ciclos que a aciclicidade de AOS-025 nunca veria.
func TestVector8_HiddenDataEdge(t *testing.T) {
	doc := exfilPlanDoc()
	doc.Nodes[1].DependsOn = nil
	assertHostilePlanDies(t, doc, planvalidate.ReasonConsumesUnknownEdge)
}

// TestVector8_PhantomOutput — DISFARCE 4: consumir um output que a origem não declara.
// Um contrato que ninguém se comprometeu a cumprir não é um contrato.
func TestVector8_PhantomOutput(t *testing.T) {
	doc := exfilPlanDoc()
	doc.Nodes[1].Consumes = []plan.PayloadEdge{{From: "harvest", Output: "fantasma", Type: plan.PayloadSummary}}
	assertHostilePlanDies(t, doc, planvalidate.ReasonConsumesUnknownOutput)
}

// TestVector8_ClosedFormFromOrdinaryNode — DISFARCE 5, e o BLOCKER que a auditoria
// adversarial da wave apanhou: declarar `type: metrics` num output cujo conteúdo real é
// o trabalho do nó. `type` é palavra do planeador, tão untrusted como o resto do
// documento.
//
// FALHA-ANTES (verificada): o plano era ADMITIDO — `TaintFloor()` dizia
// metrics⇒trusted, logo (P4) nunca disparava —, e a jusante nada verificava a FORMA
// real do que era publicado: o conteúdo vivia atrás de um locator opaco e nunca
// atravessava validador de forma fechada nenhum. A barreira P0 de ADR-005, que é a
// única razão de existir de (P4), era contornada por uma palavra do documento.
//
// ESTE TESTE ERA, ATÉ À AUDITORIA, UM CASO DE «FLUXO LEGÍTIMO». Está aqui invertido de
// propósito, com o histórico à vista: o fluxo legítimo é o de
// [TestVector8_DeclassificationByVerifierSurvives], onde a forma fechada vem de um
// VERIFICADOR.
func TestVector8_ClosedFormFromOrdinaryNode(t *testing.T) {
	doc := exfilPlanDoc()
	doc.Nodes[0].Outputs = []plan.Output{{Name: "achados", Type: plan.PayloadMetrics}}
	doc.Nodes[1].Consumes = []plan.PayloadEdge{{From: "harvest", Output: "achados", Type: plan.PayloadMetrics}}
	assertHostilePlanDies(t, doc, planvalidate.ReasonConsumesTaintAuthority)
}

// TestVector8_TaintLaunderedByIntermediateNode — DISFARCE 6: a LAVAGEM POR SALTO. O nó
// hostil não pede o resumo directamente — mete um nó SEM tools no meio, que (por não
// ser privilegiado) pode consumir untrusted à vontade, e faz esse nó re-publicar o
// material como forma fechada.
//
// FALHA-ANTES (verificada): ADMITIDO. Mesmo assumindo tipos honestos, 32 pares (nome,
// inteiro) escolhidos por um nó cujo único input é material untrusted chegavam ao nó
// com egress rotulados `trusted` — canal mais que suficiente para dirigir a acção
// privilegiada (que registo apagar, que destino usar). O `derived_from[]` do facto
// existia e nenhuma regra o lia.
func TestVector8_TaintLaunderedByIntermediateNode(t *testing.T) {
	doc := baseValidDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "scrape", Role: "r", Objective: "recolhe", Tools: []plan.ToolRef{benignTool()},
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
			Outputs:        []plan.Output{{Name: "notas", Type: plan.PayloadSummary}}},
		{NodeID: "distil", Role: "r", Objective: "destila", DependsOn: []string{"scrape"},
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
			Consumes:       []plan.PayloadEdge{{From: "scrape", Output: "notas", Type: plan.PayloadSummary}},
			Outputs:        []plan.Output{{Name: "score", Type: plan.PayloadMetrics}}},
		{NodeID: "exfil", Role: "r", Objective: "age", DependsOn: []string{"distil"},
			Tools:          []plan.ToolRef{deleteTool()},
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
			Consumes:       []plan.PayloadEdge{{From: "distil", Output: "score", Type: plan.PayloadMetrics}}},
	}
	assertHostilePlanDies(t, doc, planvalidate.ReasonConsumesTaintAuthority)
}

// TestVector8_LegitimateFlowSurvives — NÃO-VACUIDADE. Sem este teste, uma regra que
// rejeitasse TUDO passaria por correcta. O fluxo legítimo é um resumo para um
// consumidor SEM autoridade — o nó que agrega e redige.
func TestVector8_LegitimateFlowSurvives(t *testing.T) {
	doc := exfilPlanDoc()
	doc.Nodes[1].Tools = nil
	mustDecode(t, doc)
	if v := planvalidate.Validate(doc, advSnapshot(), generousCeilings()); v.Rejected() {
		t.Fatalf("resumo para consumidor sem autoridade devia passar, veio: %+v", v)
	}
}

// TestVector8_DeclassificationByVerifierSurvives — a OUTRA metade da não-vacuidade, e
// o desenho que ADR-022 §2.2/§2.3 pretende: material untrusted alcança uma acção
// privilegiada por um caminho DECLARADO no organigrama — um VERIFICADOR entre os dois.
//
// O verificador é o único ponto de desclassificação, e o que o ganha são as
// propriedades que o sistema lhe impõe: independência (não certifica a sua sub-árvore,
// e nenhum nó o declara em `depends_on`), read-only por construção, e não produzir
// trabalho (só declara outputs de forma FECHADA). Se este plano fosse rejeitado, a
// disciplina de taint teria deixado de ter caminho legítimo — e uma regra sem caminho
// legítimo é uma regra que alguém desliga.
func TestVector8_DeclassificationByVerifierSurvives(t *testing.T) {
	doc := baseValidDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "harvest", Role: "r", Objective: "recolhe", Tools: []plan.ToolRef{benignTool()},
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
			Outputs:        []plan.Output{{Name: "achados", Type: plan.PayloadSummary}}},
		{NodeID: "review", Role: plan.RoleVerifier, Objective: "examina", DependsOn: []string{"harvest"},
			Tools:          []plan.ToolRef{inspectTool()},
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
			Consumes:       []plan.PayloadEdge{{From: "harvest", Output: "achados", Type: plan.PayloadSummary}},
			Outputs:        []plan.Output{{Name: "veredicto", Type: plan.PayloadVerdict}}},
		{NodeID: "act", Role: "r", Objective: "age", DependsOn: []string{"harvest"},
			ConditionalOn:  []plan.ConditionalEdge{{From: "review", When: verdictPass()}},
			Tools:          []plan.ToolRef{deleteTool()},
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
			Consumes:       []plan.PayloadEdge{{From: "review", Output: "veredicto", Type: plan.PayloadVerdict}}},
	}
	mustDecode(t, doc)
	if v := planvalidate.Validate(doc, verifierSnapshot(), generousCeilings()); v.Rejected() {
		t.Fatalf("o caminho de desclassificação por verificador (o desenho de §2.2) foi rejeitado: %+v", v)
	}
}
