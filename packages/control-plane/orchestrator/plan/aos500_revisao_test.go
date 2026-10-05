package plan_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// AOS-500, depois da revisão adversarial — o que estava certo e não estava preso.

// TestAOS500_OPredicadoVeAOrigemEmQualquerSaidaEEmQualquerNo — [plan.Node.DeclaresOutputSource]
// é a única leitura de «este nó declara origem»: o piso de versão, o validador e quem corre o
// plano perguntam todos aqui. Os testes anteriores só punham a origem na primeira saída do
// primeiro nó, e um predicado que só olhasse para a primeira saída sobrevivia (mutação R22).
func TestAOS500_OPredicadoVeAOrigemEmQualquerSaidaEEmQualquerNo(t *testing.T) {
	semOrigem := []plan.Output{{Name: "a", Type: plan.PayloadRecord}, {Name: "b", Type: plan.PayloadArtifact}}
	for posicao := 0; posicao < 3; posicao++ {
		saidas := []plan.Output{
			{Name: "a", Type: plan.PayloadRecord},
			{Name: "b", Type: plan.PayloadArtifact},
			{Name: "c", Type: plan.PayloadRecord},
		}
		saidas[posicao].FromTool = "doc_read"
		no := plan.Node{NodeID: "ler", Role: "r", Objective: "o", Outputs: saidas}
		if !no.DeclaresOutputSource() {
			t.Fatalf("a origem na saida %d do no nao foi vista pelo predicado", posicao)
		}
		// O mesmo nó, em cada posição do documento.
		for onde := 0; onde < 3; onde++ {
			doc := payloadDoc(semOrigem, nil)
			doc.Nodes = []plan.Node{
				{NodeID: "n0", Role: "r", Objective: "o", Outputs: semOrigem},
				{NodeID: "n1", Role: "r", Objective: "o", Outputs: semOrigem},
				{NodeID: "n2", Role: "r", Objective: "o", Outputs: semOrigem},
			}
			doc.Nodes[onde].Outputs = saidas
			if !doc.DeclaresOutputSource() {
				t.Fatalf("a origem na saida %d do no %d nao foi vista pelo predicado do documento", posicao, onde)
			}
			piso, uso := plan.FeatureFloor(doc)
			if !piso.Equal(plan.PlanVersion{Major: 1, Minor: 3, Patch: 0}) || uso.Feature != "from_tool" || uso.NodeID != doc.Nodes[onde].NodeID {
				t.Fatalf("origem na saida %d do no %d: piso = %s por %q no no %q; quer 1.3.0 por from_tool no no %q",
					posicao, onde, piso, uso.Feature, uso.NodeID, doc.Nodes[onde].NodeID)
			}
		}
	}
	// NÃO-VACUIDADE: sem origem em lado nenhum, o predicado diz que não e o piso fica em 1.2.0.
	doc := payloadDoc(semOrigem, nil)
	if doc.DeclaresOutputSource() || doc.Nodes[0].DeclaresOutputSource() {
		t.Fatal("um documento sem `from_tool` foi dado como declarando origem")
	}
	if piso, _ := plan.FeatureFloor(doc); !piso.Equal(plan.PlanVersion{Major: 1, Minor: 2, Patch: 0}) {
		t.Fatalf("sem origem, o piso e o de `outputs` (1.2.0); veio %s", piso)
	}
}

// docComSaida devolve o wire de um documento de um nó cuja única saída é o objecto JSON dado.
func docComSaida(saida string) string {
	return `{"plan_version":"1.3.0","objective":"o","budget_total":{"tokens":1,"cost_micro_usd":1},
	 "planner_meta":{"model":"m","prompt_version":"1.0.0","capabilities_hash":"h"},
	 "nodes":[{"node_id":"a","role":"r","objective":"o","tools":[],"depends_on":[],
	   "budget_estimate":{"tokens":1,"cost_micro_usd":1},"risk_class":"safe",
	   "outputs":[` + saida + `]}]}`
}

// TestAOS500_FromToolPresenteEVazioERecusado — `"from_tool": ""` e `"from_tool": null` não são
// «sem origem». Antes desta correcção descodificavam como ausentes e a chave desaparecia na
// re-serialização: o documento lido não era o documento escrito.
func TestAOS500_FromToolPresenteEVazioERecusado(t *testing.T) {
	for _, saida := range []string{
		`{"name":"x","type":"record","from_tool":""}`,
		`{"name":"x","type":"record","from_tool":null}`,
		`{"name":"x","type":"record","from_tool": "" }`,
		// A chave lê-se como o encoding/json a lê: sem distinguir caixa.
		`{"name":"x","type":"record","FROM_TOOL":""}`,
		// A última ocorrência ganha, como no resto do documento.
		`{"name":"x","type":"record","from_tool":"doc_read","from_tool":""}`,
		// Um valor que não é uma string.
		`{"name":"x","type":"record","from_tool":7}`,
		`{"name":"x","type":"record","from_tool":["doc_read"]}`,
	} {
		_, err := plan.Decode([]byte(docComSaida(saida)))
		if !errors.Is(err, plan.ErrInvalidOutput) {
			t.Fatalf("a saida %s devia ser recusada com ErrInvalidOutput; veio %v", saida, err)
		}
	}
}

// TestAOS500_SemAChaveODecodeEODeSempre — a recusa do campo vazio não muda nada para um
// documento que não o traz: descodifica, re-serializa nos mesmos bytes, e continua estrito.
func TestAOS500_SemAChaveODecodeEODeSempre(t *testing.T) {
	// Sem a chave: aceite, sem origem, e a ida e volta não a inventa.
	for _, saida := range []string{
		`{"name":"x","type":"record"}`,
		`{"name":"x","type":"record","taint":"untrusted"}`,
		`{"name":"x","type":"artifact","taint":"trusted"}`,
	} {
		doc, err := plan.Decode([]byte(docComSaida(saida)))
		if err != nil {
			t.Fatalf("a saida %s sem from_tool devia descodificar: %v", saida, err)
		}
		if got := doc.Nodes[0].Outputs[0]; got.FromTool != "" || got.Name != "x" {
			t.Fatalf("saida lida = %+v", got)
		}
		enc, err := plan.Encode(doc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(enc), `"outputs":[`+saida+`]`) {
			t.Fatalf("a ida e volta mudou a saida %s:\n%s", saida, enc)
		}
	}
	// Com a chave e um valor: aceite, e lida.
	doc, err := plan.Decode([]byte(docComSaida(`{"name":"x","type":"record","from_tool":"doc_read"}`)))
	if err != nil || doc.Nodes[0].Outputs[0].FromTool != "doc_read" {
		t.Fatalf("from_tool com valor: doc=%+v err=%v", doc.Nodes, err)
	}
	// O que já era recusado continua a sê-lo, e pelo erro de sempre.
	for saida, quer := range map[string]error{
		`{"name":"x","type":"record","campo_novo":1}`: nil, // campo desconhecido: erro do decode
		`{"name":"x","type":"nao_existe"}`:            plan.ErrInvalidOutput,
		`{"name":"X Y","type":"record"}`:              plan.ErrInvalidOutput,
		`{"name":"x","type":"record","taint":"meio"}`: plan.ErrInvalidOutput,
		`{"name":"x","type":7}`:                       nil, // tipo errado: erro do decode
	} {
		_, err := plan.Decode([]byte(docComSaida(saida)))
		if err == nil {
			t.Fatalf("a saida %s foi aceite", saida)
		}
		if quer != nil && !errors.Is(err, quer) {
			t.Fatalf("a saida %s devia dar %v; deu %v", saida, quer, err)
		}
		if quer == nil && !strings.HasPrefix(err.Error(), "plan: decode: ") {
			t.Fatalf("a saida %s devia falhar no decode do JSON; deu %v", saida, err)
		}
	}
}
