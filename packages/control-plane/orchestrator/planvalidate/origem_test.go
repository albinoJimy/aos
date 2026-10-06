package planvalidate

import (
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// origem_test.go — a origem declarada de uma saída na admissão (ADR-038 §2.1, AOS-500).
//
// Um teste por regra, cada um com o caso que passa e o que é recusado, e o sub-código exacto.
// O documento de partida é o nó de leitura medido em produção: um não-verificador, uma tool,
// uma saída `record`, sem entradas.

// origemDoc devolve o organigrama de base: `ler` lê com uma tool e declara que a sua saída é o
// resultado dela; `resumir` espera por `ler` e consome-a.
func origemDoc() plan.PlanDocument {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "ler", Role: "reader", Objective: "le o documento", Tools: []plan.ToolRef{readOnlyTool()},
			Outputs: []plan.Output{{Name: "notas", Type: plan.PayloadRecord, FromTool: "inspect"}}},
		{NodeID: "resumir", Role: "writer", Objective: "resume", DependsOn: []string{"ler"},
			Consumes: []plan.PayloadEdge{{From: "ler", Output: "notas", Type: plan.PayloadRecord}}},
	}
	return doc
}

func assertAceite(t *testing.T, doc plan.PlanDocument) {
	t.Helper()
	mustBeShapeValid(t, doc)
	if v := Validate(doc, verifierSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("o plano devia ser aceite; veio (%s/%s) no no %q", v.Rule, v.Reason, v.Locator.NodeID)
	}
}

// TestOrigemDeclaradaAceite — NÃO-VACUIDADE: a forma do ticket passa. Sem isto, os testes de
// recusa abaixo passavam com um validador que recusasse o campo por inteiro.
func TestOrigemDeclaradaAceite(t *testing.T) {
	assertAceite(t, origemDoc())
	// `artifact` é a outra forma admitida.
	doc := origemDoc()
	doc.Nodes[0].Outputs[0].Type = plan.PayloadArtifact
	doc.Nodes[1].Consumes[0].Type = plan.PayloadArtifact
	assertAceite(t, doc)
}

// TestOrigemTemDeSerToolDoNo — regra (O5). Passa: a tool está em `tools` do nó. Recusado: uma
// tool que existe no snapshot mas não está atribuída a ESTE nó; uma tool de outro nó; e o nome
// com outra caixa (a comparação é exacta).
func TestOrigemTemDeSerToolDoNo(t *testing.T) {
	assertAceite(t, origemDoc())

	fora := origemDoc()
	fora.Nodes[0].Outputs[0].FromTool = "search" // admissível no snapshot, não atribuída ao nó
	assertRejects(t, fora, plannerevents.RuleSchema, ReasonFromToolUnknownTool, "ler")

	deOutro := origemDoc()
	deOutro.Nodes = append(deOutro.Nodes, plan.Node{NodeID: "procurar", Role: "r", Objective: "o", Tools: []plan.ToolRef{searchTool()}})
	deOutro.Nodes[0].Outputs[0].FromTool = "search"
	assertRejects(t, deOutro, plannerevents.RuleSchema, ReasonFromToolUnknownTool, "ler")

	semTools := origemDoc()
	semTools.Nodes[0].Tools = nil
	assertRejects(t, semTools, plannerevents.RuleSchema, ReasonFromToolUnknownTool, "ler")

	// Um documento montado à mão (não passa pelo decode) com um nome que não é identificador:
	// o validador recusa-o com o mesmo código, mesmo que uma tool do nó tivesse esse nome.
	maiusculas := origemDoc()
	maiusculas.Nodes[0].Outputs[0].FromTool = "Inspect"
	if v := Validate(maiusculas, verifierSnapshot(), Ceilings{}); v.Reason != ReasonFromToolUnknownTool || v.Locator.NodeID != "ler" {
		t.Fatalf("nome com outra caixa: veio (%s/%s) no no %q; quer %s no no ler", v.Rule, v.Reason, v.Locator.NodeID, ReasonFromToolUnknownTool)
	}
}

// TestOrigemSoEmRecordOuArtifact — regra (O4). Um `summary` é transformado por definição, e uma
// forma fechada é derivada pelo sistema.
func TestOrigemSoEmRecordOuArtifact(t *testing.T) {
	for _, tipo := range []plan.PayloadType{plan.PayloadRecord, plan.PayloadArtifact} {
		doc := origemDoc()
		doc.Nodes[0].Outputs[0].Type = tipo
		doc.Nodes[1].Consumes[0].Type = tipo
		assertAceite(t, doc)
	}
	for _, tipo := range []plan.PayloadType{plan.PayloadSummary, plan.PayloadMetrics, plan.PayloadVerdict} {
		t.Run(string(tipo), func(t *testing.T) {
			doc := origemDoc()
			doc.Nodes[0].Outputs[0].Type = tipo
			doc.Nodes[1].Consumes[0].Type = tipo
			assertRejects(t, doc, plannerevents.RuleSchema, ReasonFromToolOutputType, "ler")
		})
	}
}

// TestOrigemNaoEmVerificador — regra (O1). O par que passa é o MESMO nó sem o papel reservado.
// O verificador declara uma forma fechada para o código próprio ser o que responde: com uma
// saída aberta, um verificador já morre antes por `verifier_produces_work`.
func TestOrigemNaoEmVerificador(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "ler", Role: "reader", Objective: "le", Tools: []plan.ToolRef{readOnlyTool()},
			Outputs: []plan.Output{{Name: "notas", Type: plan.PayloadRecord}}},
		{NodeID: "julgar", Role: plan.RoleVerifier, Objective: "julga", DependsOn: []string{"ler"},
			Tools:   []plan.ToolRef{readOnlyTool()},
			Outputs: []plan.Output{{Name: "medidas", Type: plan.PayloadMetrics, FromTool: "inspect"}}},
	}
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonFromToolOnVerifier, "julgar")

	// Com uma saída aberta a recusa continua a existir, por outra regra — nunca é aceite.
	aberta := doc
	aberta.Nodes = append([]plan.Node(nil), doc.Nodes...)
	aberta.Nodes[1].Outputs = []plan.Output{{Name: "medidas", Type: plan.PayloadRecord, FromTool: "inspect"}}
	if v := Validate(aberta, verifierSnapshot(), Ceilings{}); !v.Rejected() {
		t.Fatal("um verificador com saida aberta por referencia foi aceite")
	}

	// O mesmo verificador SEM origem é o plano de sempre.
	sem := doc
	sem.Nodes = append([]plan.Node(nil), doc.Nodes...)
	sem.Nodes[1].Outputs = []plan.Output{{Name: "medidas", Type: plan.PayloadMetrics}}
	assertAceite(t, sem)
}

// TestOrigemNaoEmNoComConsumes — regra (O2). Um nó que recebe entradas tem o contexto untrusted
// desde o primeiro turno; o kernel nunca designaria a chamada (ADR-038 §2.2, `inapplicable`).
func TestOrigemNaoEmNoComConsumes(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "ler", Role: "reader", Objective: "le", Tools: []plan.ToolRef{readOnlyTool()},
			Outputs: []plan.Output{{Name: "notas", Type: plan.PayloadRecord}}},
		{NodeID: "reler", Role: "reader", Objective: "le outra vez", DependsOn: []string{"ler"},
			Tools:    []plan.ToolRef{readOnlyTool()},
			Consumes: []plan.PayloadEdge{{From: "ler", Output: "notas", Type: plan.PayloadRecord}},
			Outputs:  []plan.Output{{Name: "copia", Type: plan.PayloadRecord, FromTool: "inspect"}}},
	}
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonFromToolWithConsumes, "reler")

	// O mesmo nó SEM `consumes` (só a aresta de ordem) passa: a regra é sobre entradas, não
	// sobre dependências.
	semEntradas := doc
	semEntradas.Nodes = append([]plan.Node(nil), doc.Nodes...)
	semEntradas.Nodes[1].Consumes = nil
	assertAceite(t, semEntradas)
}

// TestOrigemNoMaximoUmaPorNo — regra (O3).
func TestOrigemNoMaximoUmaPorNo(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "ler", Role: "reader", Objective: "le", Tools: []plan.ToolRef{readOnlyTool()},
			Outputs: []plan.Output{
				{Name: "notas", Type: plan.PayloadRecord, FromTool: "inspect"},
				{Name: "anexo", Type: plan.PayloadArtifact, FromTool: "inspect"},
			}},
	}
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonFromToolMultiple, "ler")

}

// TestOrigemNaoSeMisturaComSaidaDeTexto — regra (O7), AOS-501. O nó misto não existe: um nó que
// declara a origem de uma saída não declara outra de forma aberta, que se publicaria do texto do
// modelo. Vale para os três tipos abertos e em qualquer ordem; uma forma fechada ao lado passa.
func TestOrigemNaoSeMisturaComSaidaDeTexto(t *testing.T) {
	com := plan.Output{Name: "notas", Type: plan.PayloadRecord, FromTool: "inspect"}
	no := func(saidas ...plan.Output) plan.PlanDocument {
		doc := baseDoc()
		doc.Nodes = []plan.Node{{NodeID: "ler", Role: "reader", Objective: "le", Tools: []plan.ToolRef{readOnlyTool()}, Outputs: saidas}}
		return stamped(doc, 1, 3, 0)
	}
	for _, tipo := range []plan.PayloadType{plan.PayloadSummary, plan.PayloadRecord, plan.PayloadArtifact} {
		aberta := plan.Output{Name: "resumo", Type: tipo}
		assertRejects(t, no(com, aberta), plannerevents.RuleSchema, ReasonFromToolWithTextOutput, "ler")
		assertRejects(t, no(aberta, com), plannerevents.RuleSchema, ReasonFromToolWithTextOutput, "ler")
	}
	// Só a origem, ou a origem com uma forma fechada (que nunca se publica do texto): passa.
	assertAceite(t, no(com))
	assertAceite(t, no(com, plan.Output{Name: "medidas", Type: plan.PayloadMetrics}))
	// Um nó SEM origem com duas saídas abertas não é desta regra: valida como sempre.
	assertAceite(t, no(plan.Output{Name: "a", Type: plan.PayloadRecord}, plan.Output{Name: "b", Type: plan.PayloadSummary}))
	// A ATRIBUIÇÃO: um nó misto cuja origem já é insustentável morre pela razão da origem.
	assertRejects(t, no(plan.Output{Name: "notas", Type: plan.PayloadRecord, FromTool: "nao_e_do_no"}, plan.Output{Name: "resumo", Type: plan.PayloadSummary}),
		plannerevents.RuleSchema, ReasonFromToolUnknownTool, "ler")
}

// TestOrigemObrigaACarimbar130 — a regra do carimbo, pelo piso derivado das features: o mesmo
// documento carimbado 1.2.0 é recusado e carimbado 1.3.0 é aceite. Sem a origem, o 1.2.0 passa.
func TestOrigemObrigaACarimbar130(t *testing.T) {
	doc := origemDoc()
	assertAceite(t, stamped(doc, 1, 3, 0))
	assertRejects(t, stamped(doc, 1, 2, 0), plannerevents.RuleSchema, ReasonVersionBelowFeatures, "ler")

	sem := origemDoc()
	sem.Nodes[0].Outputs[0].FromTool = ""
	assertAceite(t, stamped(sem, 1, 2, 0))
}

// TestCandidatoPorEstruturaSemOrigemPassa — decidido pelo dono (2026-10-05): um nó que PODIA
// declarar a origem (não-verificador, uma tool, uma saída aberta, sem entradas) e não o faz não
// é recusado. A origem declara-se, não se infere.
func TestCandidatoPorEstruturaSemOrigemPassa(t *testing.T) {
	doc := origemDoc()
	doc.Nodes[0].Outputs[0].FromTool = ""
	for _, v := range [][3]int{{1, 2, 0}, {1, 3, 0}} {
		assertAceite(t, stamped(doc, v[0], v[1], v[2]))
	}
}

// TestSaidaPorReferenciaContinuaUntrusted — o taint não muda com a origem, e a regra (P4) de
// payload.go continua a impedir um consumidor com autoridade privilegiada de a consumir. O par
// que passa é o mesmo consumidor sem tools.
func TestSaidaPorReferenciaContinuaUntrusted(t *testing.T) {
	doc := origemDoc()
	if got := doc.Nodes[0].EffectiveOutputTaint(doc.Nodes[0].Outputs[0]); got != plan.TaintUntrusted {
		t.Fatalf("taint efectivo de uma saida por referencia = %q; quer untrusted", got)
	}
	// Nem declarando `trusted` no documento.
	doc.Nodes[0].Outputs[0].Taint = plan.TaintTrusted
	if got := doc.Nodes[0].EffectiveOutputTaint(doc.Nodes[0].Outputs[0]); got != plan.TaintUntrusted {
		t.Fatalf("o rotulo declarado baixou o taint de uma saida por referencia: %q", got)
	}
	for _, privilegiada := range []plan.ToolRef{egressTool(), effectTool(), mutatorTool()} {
		com := origemDoc()
		com.Nodes[1].Tools = []plan.ToolRef{privilegiada}
		assertRejects(t, com, plannerevents.RuleSchema, ReasonConsumesTaintAuthority, "resumir")
	}
	assertAceite(t, origemDoc())
}

// TestOrigemEDeterministica — o mesmo documento dá sempre o mesmo veredicto.
func TestOrigemEDeterministica(t *testing.T) {
	doc := origemDoc()
	doc.Nodes[0].Outputs[0].Type = plan.PayloadSummary
	primeiro := Validate(doc, verifierSnapshot(), Ceilings{})
	for i := 0; i < 50; i++ {
		if v := Validate(doc, verifierSnapshot(), Ceilings{}); v != primeiro {
			t.Fatalf("veredicto %d = %+v; o primeiro foi %+v", i, v, primeiro)
		}
	}
}
