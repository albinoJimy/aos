package planvalidate

import (
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// origem_revisao_test.go — o que a revisão adversarial do AOS-500 mostrou que estava só certo, e
// não preso por teste. Cada teste nomeia a mutação que sobrevivia sem ele.

// TestOrigemNaSegundaSaidaENoSegundoNoEntraNoValidador — o predicado de que tudo depende
// ([plan.Node.DeclaresOutputSource]) tem de ver a origem ONDE QUER QUE ELA ESTEJA: os testes
// anteriores só a punham na primeira saída do primeiro nó. Com um predicado que olhasse só para
// a primeira saída (mutação R22), o nó abaixo não entrava no validador e a saída `summary` com
// origem era aceite; e o carimbo 1.2.0 passava no piso.
func TestOrigemNaSegundaSaidaENoSegundoNoEntraNoValidador(t *testing.T) {
	// (a) origem na SEGUNDA saída, inválida pela regra (O4): tem de ser recusada.
	segunda := origemDoc()
	segunda.Nodes[0].Outputs = []plan.Output{
		{Name: "livre", Type: plan.PayloadRecord},
		{Name: "notas", Type: plan.PayloadSummary, FromTool: "inspect"},
	}
	segunda.Nodes[1].Consumes[0].Type = plan.PayloadSummary
	assertRejects(t, segunda, plannerevents.RuleSchema, ReasonFromToolOutputType, "ler")

	// (b) origem na segunda saída, válida, com o carimbo abaixo do piso: o piso tem de a ver.
	piso := origemDoc()
	piso.Nodes[0].Outputs = []plan.Output{
		{Name: "livre", Type: plan.PayloadArtifact},
		{Name: "notas", Type: plan.PayloadRecord, FromTool: "inspect"},
	}
	assertAceite(t, stamped(piso, 1, 3, 0))
	assertRejects(t, stamped(piso, 1, 2, 0), plannerevents.RuleSchema, ReasonVersionBelowFeatures, "ler")

	// (c) origem só no SEGUNDO nó do plano (o primeiro não declara nada), inválida pela (O5).
	segundoNo := baseDoc()
	segundoNo.Nodes = []plan.Node{
		{NodeID: "preparar", Role: "r", Objective: "prepara", Tools: []plan.ToolRef{readOnlyTool()}},
		{NodeID: "ler", Role: "reader", Objective: "le", DependsOn: []string{"preparar"}, Tools: []plan.ToolRef{readOnlyTool()},
			Outputs: []plan.Output{{Name: "livre", Type: plan.PayloadRecord}, {Name: "notas", Type: plan.PayloadRecord, FromTool: "search"}}},
	}
	assertRejects(t, segundoNo, plannerevents.RuleSchema, ReasonFromToolUnknownTool, "ler")
	assertRejects(t, stamped(segundoNo, 1, 2, 0), plannerevents.RuleSchema, ReasonVersionBelowFeatures, "ler")
	// NÃO-VACUIDADE: o mesmo plano com a tool certa passa.
	segundoNo.Nodes[1].Outputs[1].FromTool = "inspect"
	assertAceite(t, segundoNo)
}

// TestOrigemEmArtifactComConsumesERecusada — a regra (O2) é do NÓ, não do tipo da saída. O teste
// anterior só a exercitava com `record`; uma regra que só valesse para `record` (mutação R04)
// deixava passar um `artifact` com origem num nó com entradas.
func TestOrigemEmArtifactComConsumesERecusada(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "ler", Role: "reader", Objective: "le", Tools: []plan.ToolRef{readOnlyTool()},
			Outputs: []plan.Output{{Name: "notas", Type: plan.PayloadRecord}}},
		{NodeID: "reler", Role: "reader", Objective: "le outra vez", DependsOn: []string{"ler"},
			Tools:    []plan.ToolRef{readOnlyTool()},
			Consumes: []plan.PayloadEdge{{From: "ler", Output: "notas", Type: plan.PayloadRecord}},
			Outputs:  []plan.Output{{Name: "copia", Type: plan.PayloadArtifact, FromTool: "inspect"}}},
	}
	assertRejects(t, doc, plannerevents.RuleSchema, ReasonFromToolWithConsumes, "reler")

	// A origem na segunda saída, com a primeira de outro tipo: a regra continua a ser do nó.
	duas := doc
	duas.Nodes = append([]plan.Node(nil), doc.Nodes...)
	duas.Nodes[1].Outputs = []plan.Output{
		{Name: "anexo", Type: plan.PayloadArtifact},
		{Name: "copia", Type: plan.PayloadRecord, FromTool: "inspect"},
	}
	assertRejects(t, duas, plannerevents.RuleSchema, ReasonFromToolWithConsumes, "reler")

	// NÃO-VACUIDADE: sem `consumes`, o `artifact` com origem passa.
	sem := doc
	sem.Nodes = append([]plan.Node(nil), doc.Nodes...)
	sem.Nodes[1].Consumes = nil
	assertAceite(t, sem)
}

// TestOrigemComparaONomeExactoDaTool — a regra (O5) compara o nome byte a byte. Os casos
// anteriores usavam nomes de comprimento diferente, ou um `from_tool` que nem identificador era:
// uma comparação só pelo comprimento (mutação R13) e uma que ignorasse a caixa (R13b) passavam.
func TestOrigemComparaONomeExactoDaTool(t *testing.T) {
	// Outro nome, com o MESMO comprimento do da tool do nó (`inspect`, 7 bytes).
	mesmoComprimento := origemDoc()
	mesmoComprimento.Nodes[0].Outputs[0].FromTool = "inspecz"
	if len("inspecz") != len(readOnlyTool().Name) {
		t.Fatal("pre-condicao: os dois nomes tem o mesmo comprimento")
	}
	assertRejects(t, mesmoComprimento, plannerevents.RuleSchema, ReasonFromToolUnknownTool, "ler")

	// A tool do nó chama-se `Inspect`; o plano declara `inspect`. São nomes diferentes: o run
	// declararia ao kernel uma tool que o nó não tem.
	snap := verifierSnapshot()
	snap.Tools = append(snap.Tools, Capability{Name: "Inspect", Version: "1.0.0", Digest: "sha256:outra-caixa", Admissible: true})
	outraCaixa := origemDoc()
	outraCaixa.Nodes[0].Tools = []plan.ToolRef{{Name: "Inspect", Version: "1.0.0", Digest: "sha256:outra-caixa"}}
	mustBeShapeValid(t, outraCaixa)
	if v := Validate(outraCaixa, snap, Ceilings{}); v.Rule != plannerevents.RuleSchema || v.Reason != ReasonFromToolUnknownTool || v.Locator.NodeID != "ler" {
		t.Fatalf("tool `Inspect` no no e from_tool `inspect`: veio (%s/%s) no no %q; quer %s no no ler",
			v.Rule, v.Reason, v.Locator.NodeID, ReasonFromToolUnknownTool)
	}
	// NÃO-VACUIDADE: com as duas no nó, `inspect` refere a que tem esse nome exacto, e passa —
	// a de outra caixa não conta como repetição.
	asDuas := origemDoc()
	asDuas.Nodes[0].Tools = []plan.ToolRef{readOnlyTool(), {Name: "Inspect", Version: "1.0.0", Digest: "sha256:outra-caixa"}}
	mustBeShapeValid(t, asDuas)
	if v := Validate(asDuas, snap, Ceilings{}); v.Rejected() {
		t.Fatalf("`inspect` e `Inspect` sao tools diferentes e a origem refere uma so; veio (%s/%s)", v.Rule, v.Reason)
	}
}

// TestOrigemAmbiguaComToolRepetidaNoNo — regra (O6). Duas `ToolRef` com o mesmo nome no nó
// (versões diferentes) deixam `from_tool` sem dizer de qual das duas a saída é o resultado.
// Antes desta regra o plano era aceite.
func TestOrigemAmbiguaComToolRepetidaNoNo(t *testing.T) {
	snap := verifierSnapshot()
	snap.Tools = append(snap.Tools, Capability{Name: "inspect", Version: "2.0.0", Digest: "sha256:inspect2", Admissible: true})
	segunda := plan.ToolRef{Name: "inspect", Version: "2.0.0", Digest: "sha256:inspect2"}

	ambiguo := origemDoc()
	ambiguo.Nodes[0].Tools = []plan.ToolRef{readOnlyTool(), segunda}
	mustBeShapeValid(t, ambiguo)
	if v := Validate(ambiguo, snap, Ceilings{}); v.Rule != plannerevents.RuleSchema || v.Reason != ReasonFromToolAmbiguousTool || v.Locator.NodeID != "ler" {
		t.Fatalf("duas ToolRef `inspect` e from_tool `inspect`: veio (ok=%v %s/%s) no no %q; quer %s no no ler",
			!v.Rejected(), v.Rule, v.Reason, v.Locator.NodeID, ReasonFromToolAmbiguousTool)
	}

	// NÃO-VACUIDADE (1): a regra é da ORIGEM. O mesmo nó, com a tool repetida e sem `from_tool`,
	// valida como validava antes deste ticket.
	semOrigem := origemDoc()
	semOrigem.Nodes[0].Tools = []plan.ToolRef{readOnlyTool(), segunda}
	semOrigem.Nodes[0].Outputs[0].FromTool = ""
	mustBeShapeValid(t, semOrigem)
	if v := Validate(semOrigem, snap, Ceilings{}); v.Rejected() {
		t.Fatalf("um no com a tool repetida e sem origem foi recusado (%s/%s): a regra nova so vale para quem declara", v.Rule, v.Reason)
	}

	// NÃO-VACUIDADE (2): a repetição é da tool NOMEADA. Com outra tool repetida no nó e a
	// origem a referir uma que só lá está uma vez, passa.
	snap2 := verifierSnapshot()
	snap2.Tools = append(snap2.Tools, Capability{Name: "search", Version: "2.0.0", Digest: "sha256:search2", Admissible: true})
	outraRepetida := origemDoc()
	outraRepetida.Nodes[0].Tools = []plan.ToolRef{readOnlyTool(), searchTool(), {Name: "search", Version: "2.0.0", Digest: "sha256:search2"}}
	mustBeShapeValid(t, outraRepetida)
	if v := Validate(outraRepetida, snap2, Ceilings{}); v.Rejected() {
		t.Fatalf("a origem refere `inspect`, pinada uma so vez; a repeticao de `search` nao a torna ambigua (%s/%s)", v.Rule, v.Reason)
	}
}
