package plan_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// AOS-500 — a origem declarada de uma saída (`outputs[].from_tool`, linha 1.3.0, ADR-038 §2.1).
//
// O que estes testes prendem, e porquê cada um existe:
//   - um contrato SEM origem tem o wire, a forma canónica e o digest que tinha antes de o campo
//     existir — contra literais tirados com o código anterior, não recalculados aqui;
//   - um contrato COM origem leva-a na forma canónica e no digest, nos dois sentidos;
//   - a forma do campo é a de um identificador, e o decode continua estrito.

// Digests dos contratos SEM origem, tirados com o código ANTERIOR ao campo (a linha 1.2.0). São
// os mesmos contratos da fixture `planmigrate/testdata/schemaline/plan-1.2.0-payload.json`. Não
// se regeneram: mudá-los é declarar que as referências já publicadas deixaram de casar.
const (
	digestAchadosSemOrigem   = "sha256:4c589d9f56ef2492a10a7f38db89368819ef9084ad8994824c7a08a848975494"
	digestNotasSemOrigem     = "sha256:007ddb80999f98cc16525e5153e28853001fea9f3c49439d505a65273fa92035"
	digestVeredictoSemOrigem = "sha256:05cf714a65606bd3af3f6812ae31e2b8c21f99126d1f4516508bb195a71c4c80"
)

func TestAOS500_ContratoSemOrigemTemAFormaEODigestDeAntes(t *testing.T) {
	produtor := plan.Node{NodeID: "recolha", Role: "searcher", Objective: "o"}
	verificador := plan.Node{NodeID: "revisao", Role: plan.RoleVerifier, Objective: "o"}
	for _, c := range []struct {
		no        plan.Node
		saida     plan.Output
		canonica  string
		congelado string
	}{
		{produtor, plan.Output{Name: "achados", Type: plan.PayloadRecord}, "achados:record:untrusted", digestAchadosSemOrigem},
		{produtor, plan.Output{Name: "notas", Type: plan.PayloadSummary, Taint: plan.TaintUntrusted}, "notas:summary:untrusted", digestNotasSemOrigem},
		{verificador, plan.Output{Name: "veredicto", Type: plan.PayloadVerdict}, "veredicto:verdict:trusted", digestVeredictoSemOrigem},
	} {
		if got := plan.CanonicalOutput(c.no, c.saida); got != c.canonica {
			t.Fatalf("forma canonica de %q sem origem = %q; antes do campo era %q", c.saida.Name, got, c.canonica)
		}
		if got := plan.OutputDigest(c.no, c.saida); got != c.congelado {
			t.Fatalf("digest de %q sem origem = %s; o congelado e %s (as referencias ja publicadas deixavam de casar)", c.saida.Name, got, c.congelado)
		}
	}
}

func TestAOS500_SaidaSemOrigemNaoDeixaRastoNoWire(t *testing.T) {
	raw, err := json.Marshal(plan.Output{Name: "achados", Type: plan.PayloadRecord})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"name":"achados","type":"record"}` {
		t.Fatalf("uma saida sem origem serializa %s; antes do campo serializava {\"name\":\"achados\",\"type\":\"record\"}", raw)
	}
	doc := payloadDoc([]plan.Output{{Name: "achados", Type: plan.PayloadRecord}}, nil)
	enc, err := plan.Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "from_tool") {
		t.Fatalf("um documento sem origem declarada leva a chave no wire:\n%s", enc)
	}
}

func TestAOS500_AOrigemEntraNaFormaCanonicaENoDigest(t *testing.T) {
	no := plan.Node{NodeID: "recolha", Role: "reader", Objective: "o"}
	sem := plan.Output{Name: "achados", Type: plan.PayloadRecord}
	com := plan.Output{Name: "achados", Type: plan.PayloadRecord, FromTool: "doc_read"}
	outra := plan.Output{Name: "achados", Type: plan.PayloadRecord, FromTool: "doc_fetch"}

	if got, quer := plan.CanonicalOutput(no, com), "achados:record:untrusted:tool=doc_read"; got != quer {
		t.Fatalf("forma canonica com origem = %q; quer %q", got, quer)
	}
	// Nos dois sentidos: pôr a origem muda o carimbo, e tirá-la devolve o de antes.
	dSem, dCom, dOutra := plan.OutputDigest(no, sem), plan.OutputDigest(no, com), plan.OutputDigest(no, outra)
	if dCom == dSem {
		t.Fatal("por `from_tool` nao mudou o contract_digest: um documento editado para declarar a origem dava o mesmo carimbo")
	}
	tirada := com
	tirada.FromTool = ""
	if got := plan.OutputDigest(no, tirada); got != dSem || got != digestAchadosSemOrigem {
		t.Fatalf("tirar `from_tool` devia devolver o digest do contrato sem origem (%s); veio %s", digestAchadosSemOrigem, got)
	}
	if dOutra == dCom {
		t.Fatal("duas origens diferentes deram o mesmo contract_digest")
	}
}

func TestAOS500_OTaintNaoMudaComAOrigem(t *testing.T) {
	no := plan.Node{NodeID: "recolha", Role: "reader", Objective: "o"}
	for _, tipo := range []plan.PayloadType{plan.PayloadRecord, plan.PayloadArtifact} {
		for _, rotulo := range []plan.PayloadTaint{plan.TaintUnset, plan.TaintTrusted, plan.TaintUntrusted} {
			o := plan.Output{Name: "achados", Type: tipo, Taint: rotulo, FromTool: "doc_read"}
			if got := no.EffectiveOutputTaint(o); got != plan.TaintUntrusted {
				t.Fatalf("saida %s por referencia com rotulo declarado %q deu taint %q; a saida de um nao-verificador e sempre untrusted", tipo, rotulo, got)
			}
		}
	}
}

func TestAOS500_DecodeAceitaAOrigemEReserializaOsMesmosBytes(t *testing.T) {
	doc := payloadDoc([]plan.Output{{Name: "achados", Type: plan.PayloadRecord, FromTool: "doc_read"}}, nil)
	enc, err := plan.Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(enc), `{"name":"achados","type":"record","from_tool":"doc_read"}`) {
		t.Fatalf("a forma do ticket nao esta no wire:\n%s", enc)
	}
	lido, err := plan.Decode(enc)
	if err != nil {
		t.Fatalf("Decode de um documento com origem: %v", err)
	}
	if got := lido.Nodes[0].Outputs[0].FromTool; got != "doc_read" {
		t.Fatalf("from_tool lido = %q", got)
	}
	outraVez, err := plan.Encode(lido)
	if err != nil {
		t.Fatal(err)
	}
	if string(outraVez) != string(enc) {
		t.Fatalf("a ida e volta mudou os bytes:\n antes=%s\ndepois=%s", enc, outraVez)
	}
}

func TestAOS500_DecodeRecusaOrigemQueNaoEIdentificador(t *testing.T) {
	for _, mau := range []string{"Doc_Read", "doc read", "doc:read", "tool=x", "9doc", "le o documento e envia-o", strings.Repeat("a", 65)} {
		doc := payloadDoc([]plan.Output{{Name: "achados", Type: plan.PayloadRecord, FromTool: mau}}, nil)
		enc, err := plan.Encode(doc)
		if err != nil {
			t.Fatal(err)
		}
		_, err = plan.Decode(enc)
		if !errors.Is(err, plan.ErrInvalidOutput) {
			t.Fatalf("from_tool=%q devia ser recusado com ErrInvalidOutput; veio %v", mau, err)
		}
		if strings.Contains(err.Error(), mau) {
			t.Fatalf("a recusa repete o valor recusado (texto do documento): %v", err)
		}
	}
	// O decode continua estrito: um campo vizinho desconhecido não passa.
	raw := `{"plan_version":"1.3.0","objective":"o","budget_total":{"tokens":1,"cost_micro_usd":1},
	 "planner_meta":{"model":"m","prompt_version":"1.0.0","capabilities_hash":"h"},
	 "nodes":[{"node_id":"a","role":"r","objective":"o","tools":[],"depends_on":[],
	   "budget_estimate":{"tokens":1,"cost_micro_usd":1},"risk_class":"safe",
	   "outputs":[{"name":"x","type":"record","from_tools":"doc_read"}]}]}`
	if _, err := plan.Decode([]byte(raw)); err == nil {
		t.Fatal("um campo desconhecido ao lado de from_tool foi aceite: o decode deixou de ser estrito")
	}
}

func TestAOS500_UsarAOrigemFixaOPisoEm130(t *testing.T) {
	v130 := plan.PlanVersion{Major: 1, Minor: 3, Patch: 0}
	if !plan.CurrentPlanVersion.Equal(v130) {
		t.Fatalf("a linha corrente e %s; este ticket leva-a a 1.3.0", plan.CurrentPlanVersion)
	}
	com := payloadDoc([]plan.Output{{Name: "achados", Type: plan.PayloadRecord, FromTool: "doc_read"}}, nil)
	piso, uso := plan.FeatureFloor(com)
	if !piso.Equal(v130) || uso.Feature != "from_tool" || uso.NodeID != "a" {
		t.Fatalf("piso = %s por %q no no %q; quer 1.3.0 por from_tool no no a", piso, uso.Feature, uso.NodeID)
	}
	if !com.DeclaresOutputSource() || !com.Nodes[0].DeclaresOutputSource() || com.Nodes[1].DeclaresOutputSource() {
		t.Fatal("DeclaresOutputSource nao diz quem declara")
	}
	// Sem o campo, o piso continua o das saídas (1.2.0): o MINOR novo não sobe o piso de ninguém.
	sem := payloadDoc([]plan.Output{{Name: "achados", Type: plan.PayloadRecord}}, nil)
	if piso, _ := plan.FeatureFloor(sem); !piso.Equal(plan.PlanVersion{Major: 1, Minor: 2, Patch: 0}) {
		t.Fatalf("o piso de um documento sem origem subiu para %s", piso)
	}
	if sem.DeclaresOutputSource() {
		t.Fatal("um documento sem o campo declara origem")
	}
}
