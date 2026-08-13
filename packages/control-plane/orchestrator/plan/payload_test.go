package plan_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// payloadDoc devolve um documento válido na FORMA com os contratos de payload dados.
func payloadDoc(outputs []plan.Output, consumes []plan.PayloadEdge) plan.PlanDocument {
	return plan.PlanDocument{
		PlanVersion: plan.CurrentPlanVersion,
		Objective:   "objectivo",
		BudgetTotal: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10},
		PlannerMeta: plan.PlannerMeta{Model: "m", PromptVersion: "1.0.0", CapabilitiesHash: "h"},
		Nodes: []plan.Node{
			{NodeID: "a", Role: "r", Objective: "o", Outputs: outputs},
			{NodeID: "b", Role: "r", Objective: "o", DependsOn: []string{"a"}, Consumes: consumes},
		},
	}
}

// TestPayloadTaintDerivedFromType prova a decisão central de AOS-272: o taint de um
// output NÃO vem da palavra do planeador — vem do TIPO. Só as formas FECHADAS POR
// CONSTRUÇÃO (métricas, veredicto) têm piso `trusted`; tudo o que admite conteúdo do
// trabalho tem piso `untrusted`, porque o trabalho é saída de modelo (ADR-005).
func TestPayloadTaintDerivedFromType(t *testing.T) {
	tests := []struct {
		typ       plan.PayloadType
		closed    bool
		wantFloor plan.PayloadTaint
	}{
		{plan.PayloadSummary, false, plan.TaintUntrusted},
		{plan.PayloadRecord, false, plan.TaintUntrusted},
		{plan.PayloadArtifact, false, plan.TaintUntrusted},
		{plan.PayloadMetrics, true, plan.TaintTrusted},
		{plan.PayloadVerdict, true, plan.TaintTrusted},
	}
	for _, tc := range tests {
		t.Run(string(tc.typ), func(t *testing.T) {
			if got := tc.typ.ClosedForm(); got != tc.closed {
				t.Errorf("ClosedForm()=%v quer %v", got, tc.closed)
			}
			if got := tc.typ.TaintFloor(); got != tc.wantFloor {
				t.Errorf("TaintFloor()=%q quer %q", got, tc.wantFloor)
			}
		})
	}
	// FAIL-CLOSED PELO TIPO: um tipo inválido (ou um acrescentado sem tocar em
	// ClosedForm) nunca é trusted por omissão.
	var unknown plan.PayloadType = "coisa-nova"
	if unknown.ClosedForm() {
		t.Fatalf("tipo desconhecido nao devia ser forma fechada")
	}
	if got := unknown.TaintFloor(); got != plan.TaintUntrusted {
		t.Fatalf("piso de tipo desconhecido = %q, quer untrusted (fail-closed)", got)
	}
}

// TestEffectiveTaintNeedsFormAndAuthority prova as DUAS condições do rótulo `trusted`
// — forma fechada E produtor verificador — e o «só eleva» do advisory.
//
// FALHA-ANTES (blocker da auditoria da wave): o rótulo derivava SÓ do tipo, que é um
// campo do documento UNTRUSTED. Um nó qualquer declarava `type: metrics` num output que
// carregava o seu trabalho e o consumidor privilegiado recebia-o como `trusted` — a
// barreira P0 de ADR-005 contornada por uma palavra do planeador. As linhas «nó comum»
// abaixo são exactamente esse ataque, agora rotulado untrusted.
func TestEffectiveTaintNeedsFormAndAuthority(t *testing.T) {
	common := plan.Node{NodeID: "n", Role: "producer"}
	verifier := plan.Node{NodeID: "v", Role: plan.RoleVerifier}
	tests := []struct {
		name string
		node plan.Node
		out  plan.Output
		want plan.PayloadTaint
	}{
		{"metrics de um nó comum é UNTRUSTED (o tipo é palavra do documento)",
			common, plan.Output{Name: "m", Type: plan.PayloadMetrics}, plan.TaintUntrusted},
		{"verdict de um nó comum é UNTRUSTED",
			common, plan.Output{Name: "v", Type: plan.PayloadVerdict}, plan.TaintUntrusted},
		{"metrics de um VERIFICADOR é trusted (desclassificação sancionada por §2.2)",
			verifier, plan.Output{Name: "m", Type: plan.PayloadMetrics}, plan.TaintTrusted},
		{"verdict de um VERIFICADOR é trusted",
			verifier, plan.Output{Name: "v", Type: plan.PayloadVerdict}, plan.TaintTrusted},
		{"summary de um VERIFICADOR continua untrusted (a forma aberta carrega trabalho)",
			verifier, plan.Output{Name: "s", Type: plan.PayloadSummary}, plan.TaintUntrusted},
		{"advisory ELEVA: metrics de verificador declarado untrusted é honrado",
			verifier, plan.Output{Name: "m", Type: plan.PayloadMetrics, Taint: plan.TaintUntrusted}, plan.TaintUntrusted},
		{"advisory NÃO BAIXA: summary declarado trusted é IGNORADO",
			verifier, plan.Output{Name: "s", Type: plan.PayloadSummary, Taint: plan.TaintTrusted}, plan.TaintUntrusted},
		{"advisory NÃO BAIXA: metrics de nó comum declarado trusted é IGNORADO",
			common, plan.Output{Name: "m", Type: plan.PayloadMetrics, Taint: plan.TaintTrusted}, plan.TaintUntrusted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.node.EffectiveOutputTaint(tc.out); got != tc.want {
				t.Fatalf("EffectiveOutputTaint()=%q quer %q", got, tc.want)
			}
		})
	}
}

// TestPayloadShapeRejections cobre a FORMA: nomes fora da grammar, duplicados, enums
// fora do conjunto fechado e tectos de aridade. Fail-closed — o documento inteiro é
// recusado, nunca o contrato ofensor descartado.
func TestPayloadShapeRejections(t *testing.T) {
	tests := []struct {
		name     string
		outputs  []plan.Output
		consumes []plan.PayloadEdge
		wantErr  error
	}{
		{
			name:    "nome de output fora da grammar",
			outputs: []plan.Output{{Name: "Relatório Final", Type: plan.PayloadSummary}},
			wantErr: plan.ErrInvalidOutput,
		},
		{
			name:    "output duplicado no mesmo no",
			outputs: []plan.Output{{Name: "r", Type: plan.PayloadSummary}, {Name: "r", Type: plan.PayloadMetrics}},
			wantErr: plan.ErrInvalidOutput,
		},
		{
			name:    "tipo fora do enum fechado",
			outputs: []plan.Output{{Name: "r", Type: "json_livre"}},
			wantErr: plan.ErrInvalidOutput,
		},
		{
			name:    "taint fora do enum fechado",
			outputs: []plan.Output{{Name: "r", Type: plan.PayloadSummary, Taint: "quase-trusted"}},
			wantErr: plan.ErrInvalidOutput,
		},
		{
			name:     "consumes com from vazio",
			outputs:  []plan.Output{{Name: "r", Type: plan.PayloadSummary}},
			consumes: []plan.PayloadEdge{{From: "", Output: "r", Type: plan.PayloadSummary}},
			wantErr:  plan.ErrInvalidConsumes,
		},
		{
			name:     "consumes com output fora da grammar",
			outputs:  []plan.Output{{Name: "r", Type: plan.PayloadSummary}},
			consumes: []plan.PayloadEdge{{From: "a", Output: "R!", Type: plan.PayloadSummary}},
			wantErr:  plan.ErrInvalidConsumes,
		},
		{
			name:     "par (from,output) repetido",
			outputs:  []plan.Output{{Name: "r", Type: plan.PayloadSummary}},
			consumes: []plan.PayloadEdge{{From: "a", Output: "r", Type: plan.PayloadSummary}, {From: "a", Output: "r", Type: plan.PayloadMetrics}},
			wantErr:  plan.ErrInvalidConsumes,
		},
		{
			name:    "outputs acima do tecto de aridade",
			outputs: manyOutputs(9),
			wantErr: plan.ErrTooManyPayloadContracts,
		},
		{
			name:     "consumes acima do tecto de aridade",
			outputs:  []plan.Output{{Name: "r", Type: plan.PayloadSummary}},
			consumes: manyEdges(17),
			wantErr:  plan.ErrTooManyPayloadContracts,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := plan.Encode(payloadDoc(tc.outputs, tc.consumes))
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if _, err := plan.Decode(raw); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Decode err = %v, quer %v", err, tc.wantErr)
			}
		})
	}
}

func manyOutputs(n int) []plan.Output {
	out := make([]plan.Output, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, plan.Output{Name: "o" + string(rune('a'+i)), Type: plan.PayloadSummary})
	}
	return out
}

func manyEdges(n int) []plan.PayloadEdge {
	out := make([]plan.PayloadEdge, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, plan.PayloadEdge{From: "a", Output: "o" + string(rune('a'+i)), Type: plan.PayloadSummary})
	}
	return out
}

// TestPayloadSchemaStaysClosed prova que a extensão NÃO abriu o schema: um campo
// desconhecido dentro de um contrato de payload continua a ser recusado por
// DisallowUnknownFields, em vez de ignorado.
func TestPayloadSchemaStaysClosed(t *testing.T) {
	raw := []byte(`{
		"plan_version":"1.0.0","objective":"o",
		"budget_total":{"tokens":1,"cost_micro_usd":1},
		"planner_meta":{"model":"m","prompt_version":"1.0.0","capabilities_hash":"h"},
		"nodes":[{"node_id":"a","role":"r","objective":"o",
		  "tools":null,"depends_on":null,"budget_estimate":{"tokens":1,"cost_micro_usd":1},"risk_class":"",
		  "outputs":[{"name":"r","type":"summary","taint":"untrusted","encoding":"base64"}]}]
	}`)
	if _, err := plan.Decode(raw); err == nil {
		t.Fatalf("campo desconhecido dentro de um output devia ser recusado")
	} else if !strings.Contains(err.Error(), "encoding") {
		t.Fatalf("erro devia nomear o campo desconhecido, got %v", err)
	}
}

// TestPayloadAdditiveNoMajor prova a retro-compatibilidade que dispensa o bump de
// MAJOR (que é AOS-273): um documento SEM os campos novos decodifica exactamente como
// antes e os campos ficam nil — a ausência significa «sem contratos», não «desconhecido».
func TestPayloadAdditiveNoMajor(t *testing.T) {
	raw := []byte(`{
		"plan_version":"1.0.0","objective":"o",
		"budget_total":{"tokens":1,"cost_micro_usd":1},
		"planner_meta":{"model":"m","prompt_version":"1.0.0","capabilities_hash":"h"},
		"nodes":[{"node_id":"a","role":"r","objective":"o",
		  "tools":null,"depends_on":null,"budget_estimate":{"tokens":1,"cost_micro_usd":1},"risk_class":""}]
	}`)
	doc, err := plan.Decode(raw)
	if err != nil {
		t.Fatalf("documento pré-AOS-272 devia decodificar: %v", err)
	}
	if doc.Nodes[0].Outputs != nil || doc.Nodes[0].Consumes != nil {
		t.Fatalf("campos ausentes deviam ficar nil, got outputs=%v consumes=%v", doc.Nodes[0].Outputs, doc.Nodes[0].Consumes)
	}
}

// TestOutputDigestBindsEffectiveTaint prova que o digest do contrato fecha sobre o
// taint EFECTIVO (não o declarado): dois contratos com o mesmo nome e tipo mas rótulos
// efectivos diferentes têm digests diferentes — e um advisory ignorado NÃO muda o
// digest, porque não muda o que vale.
func TestOutputDigestBindsEffectiveTaint(t *testing.T) {
	verifier := plan.Node{NodeID: "v", Role: plan.RoleVerifier}
	common := plan.Node{NodeID: "n", Role: "producer"}
	base := plan.Output{Name: "m", Type: plan.PayloadMetrics}
	elevated := plan.Output{Name: "m", Type: plan.PayloadMetrics, Taint: plan.TaintUntrusted}
	if plan.OutputDigest(verifier, base) == plan.OutputDigest(verifier, elevated) {
		t.Fatalf("elevar o taint devia mudar o digest do contrato")
	}
	// O PRODUTOR entra no carimbo: o MESMO contrato declarado por um verificador e por
	// um nó comum tem rótulos efectivos opostos, logo digests diferentes. Sem isto, dois
	// contratos com significados de segurança opostos partilhavam carimbo.
	if plan.OutputDigest(verifier, base) == plan.OutputDigest(common, base) {
		t.Fatalf("o mesmo contrato num verificador e num nó comum NÃO pode ter o mesmo digest")
	}
	// Advisory IGNORADO (trusted sobre um resumo) ⇒ mesmo digest do contrato sem advisory.
	plain := plan.Output{Name: "s", Type: plan.PayloadSummary}
	laundered := plan.Output{Name: "s", Type: plan.PayloadSummary, Taint: plan.TaintTrusted}
	if plan.OutputDigest(common, plain) != plan.OutputDigest(common, laundered) {
		t.Fatalf("advisory ignorado nao devia mudar o digest (o que vale e o efectivo)")
	}
	// ESTABILIDADE sobre CONTEÚDO, não sobre identidade: o gémeo é construído de novo
	// (outro `Node`, outro `Output`, mesmos valores). Comparar `OutputDigest(x)` consigo
	// próprio seria uma tautologia — provaria que a função devolve o mesmo na mesma
	// chamada, não que a FORMA CANÓNICA é estável. É a segunda coisa que faz o carimbo
	// servir para amarrar um payload publicado ao contrato que o autorizou: o documento
	// re-lido do log tem structs novas e tem de dar o MESMO digest.
	verifierTwin := plan.Node{NodeID: verifier.NodeID, Role: verifier.Role}
	baseTwin := plan.Output{Name: base.Name, Type: base.Type, Taint: base.Taint}
	if plan.OutputDigest(verifier, base) != plan.OutputDigest(verifierTwin, baseTwin) {
		t.Fatalf("digest instável: o mesmo contrato construído de novo deu outro carimbo — a amarra payload↔contrato quebraria no replay")
	}
	if !strings.HasPrefix(plan.OutputDigest(verifier, base), "sha256:") {
		t.Fatalf("digest devia ter a forma sha256:<hex>, got %q", plan.OutputDigest(verifier, base))
	}
}

// TestIncomingEdgesUnifiesBothChannels — a definição ÚNICA de «aresta de entrada», que
// o validador, o emissor do veredicto e o despachante partilham. Duas cópias
// divergiriam, e a divergência entre «o que o validador conta como aresta» e «o que o
// log admite como sujeito de um veredicto» seria ela própria uma superfície.
func TestIncomingEdgesUnifiesBothChannels(t *testing.T) {
	n := plan.Node{
		NodeID:    "c",
		DependsOn: []string{"a"},
		ConditionalOn: []plan.ConditionalEdge{{From: "b",
			When: []plan.Predicate{{Subject: plan.SubjectVerdict, Op: plan.OpEq, Enum: plan.EnumPass}}}},
	}
	got := n.IncomingEdges()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("IncomingEdges = %v; queria [a b] (dependências primeiro, ordem declarada)", got)
	}
	// Sem condicionais devolve o slice de dependências tal-qual (zero alocações — o
	// caminho de todos os planos pré-ADR-022).
	plain := plan.Node{NodeID: "c", DependsOn: []string{"a"}}
	if got := plain.IncomingEdges(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("IncomingEdges sem condicionais = %v; queria [a]", got)
	}
}

// TestFindOutputIsTheOnlyResolution prova que a resolução nome→contrato é única e
// determinística.
func TestFindOutputIsTheOnlyResolution(t *testing.T) {
	n := plan.Node{NodeID: "a", Outputs: []plan.Output{
		{Name: "s", Type: plan.PayloadSummary},
		{Name: "m", Type: plan.PayloadMetrics},
	}}
	if o, ok := n.FindOutput("m"); !ok || o.Type != plan.PayloadMetrics {
		t.Fatalf("FindOutput(m) = %v,%v", o, ok)
	}
	if _, ok := n.FindOutput("ausente"); ok {
		t.Fatalf("FindOutput de um nome inexistente devia falhar")
	}
}
