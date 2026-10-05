package planapproval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// AOS-500 — o cartão de aprovação mostra a ORIGEM DECLARADA de uma saída (ADR-038 §2.1).
//
// Um plano pode declarar, numa saída, a tool do mesmo nó de que ela é o resultado
// (`outputs[].from_tool`). O que isso muda é o que o nó seguinte recebe; o humano que aprova o
// plano tem de o ver. Os testes prendem três coisas:
//   - o cartão de um plano SEM o campo é byte a byte o de antes (hash tirado com o código
//     anterior);
//   - o cartão de um plano COM o campo é diferente, e a diferença é legível;
//   - o campo é um símbolo, na construção e no wire — não é um sítio por onde entre texto.

// cartaoSemOrigemCongelado é o SHA-256 (e o tamanho) do wire do cartão de
// [planoComVerificador], tirado com o código ANTERIOR a `from_tool`.
const (
	cartaoSemOrigemCongelado    = "2cd7087486e214fc7fd75d72b109ffb346d6731238c4c02a0d9faeb9ba170b89"
	cartaoSemOrigemCongeladoLen = 1868
)

// planoDeLeitura devolve o organigrama medido em produção: um nó que lê um documento com uma
// tool e outro que o resume. `origem` é o `from_tool` da saída do leitor ("" ⇒ sem origem).
func planoDeLeitura(origem string) Plan {
	return Plan{
		RunID:  "run-aos500",
		Agent:  "agent:planner",
		Domain: "docs",
		Nodes: []PlanNode{
			{
				TaskID: "ler", Class: risk.ClassSafe, Capability: "doc.read",
				Resource: "doc://actas/1", Preview: "cap:doc.read -> doc://actas/1",
				Role:    "reader",
				Outputs: []PlanOutput{{Name: "notas", Type: "record", FromTool: origem}},
			},
			{
				TaskID: "resumir", Class: risk.ClassSafe, Capability: "none",
				Resource: "mem://run/ler", Preview: "resume as notas",
				Role:     "writer",
				Consumes: []PlanConsume{{From: "ler", Output: "notas", Type: "record"}},
			},
		},
		Edges: [][2]string{{"ler", "resumir"}},
	}
}

func wireDoCartao(t *testing.T, p Plan) []byte {
	t.Helper()
	card, err := BuildPlanCard(p)
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return raw
}

func TestAOS500_CartaoDeUmPlanoSemOrigemEOdeAntes(t *testing.T) {
	raw := wireDoCartao(t, planoComVerificador())
	soma := sha256.Sum256(raw)
	if got := hex.EncodeToString(soma[:]); got != cartaoSemOrigemCongelado || len(raw) != cartaoSemOrigemCongeladoLen {
		t.Fatalf("o cartao de um plano sem origem declarada mudou: sha256=%s (%d bytes); antes do campo era %s (%d bytes)\n%s",
			got, len(raw), cartaoSemOrigemCongelado, cartaoSemOrigemCongeladoLen, raw)
	}
	if strings.Contains(string(raw), "tool=") || strings.Contains(string(raw), "from_tool") {
		t.Fatalf("um cartao sem origem declarada fala dela:\n%s", raw)
	}
	// E o CARIMBO deste cartão não subiu: um cartão sem origem usa o contrato 1.1.0 e carimba-o,
	// mesmo depois de o AOS-501 ter subido a versão corrente para os cartões com origem.
	var relido PlanCard
	if err := json.Unmarshal(raw, &relido); err != nil {
		t.Fatal(err)
	}
	if relido.SchemaVersion != (PlanCardSchemaVersion{Major: 1, Minor: 1, Patch: 0}) {
		t.Fatalf("o cartao de um plano sem origem carimba %s; tem de continuar a carimbar 1.1.0", relido.SchemaVersion)
	}
}

func TestAOS500_CartaoMostraAOrigemDeclarada(t *testing.T) {
	com, err := BuildPlanCard(planoDeLeitura("doc_read"))
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	var doLeitor NodeExtension
	for _, e := range com.NodeExtensions {
		if e.TaskID == "ler" {
			doLeitor = e
		}
	}
	// Este literal é a ISOMORFIA com a `plan.CanonicalOutput` do orquestrador, pinada aqui
	// porque o módulo não o importa (o mesmo pino das condições, em def274_extensions_test.go).
	if len(doLeitor.Outputs) != 1 || doLeitor.Outputs[0] != "notas:record:untrusted:tool=doc_read" {
		t.Fatalf("outputs do leitor no cartao = %v; quer [notas:record:untrusted:tool=doc_read]", doLeitor.Outputs)
	}

	// O MESMO plano sem a origem dá OUTRO cartão — e só a saída do leitor muda.
	comWire, semWire := wireDoCartao(t, planoDeLeitura("doc_read")), wireDoCartao(t, planoDeLeitura(""))
	if string(comWire) == string(semWire) {
		t.Fatal("o plano com from_tool e o mesmo plano sem ele deram o mesmo cartao: o aprovador nao via a diferenca")
	}
	// A diferença são duas coisas, e só elas: a origem na saída do leitor, e o carimbo do
	// contrato, que sobe a 1.2.0 quando o cartão a mostra (AOS-501).
	got := strings.Replace(string(comWire), ":tool=doc_read", "", 1)
	got = strings.Replace(got, `{"schema_version":"1.2.0","run_id"`, `{"schema_version":"1.1.0","run_id"`, 1)
	if got != string(semWire) {
		t.Fatalf("a diferenca entre os dois cartoes devia ser so a origem da saida e o carimbo:\n com=%s\n sem=%s", comWire, semWire)
	}

	// O cartão com origem faz a ida e volta pelo wire.
	var relido PlanCard
	if err := json.Unmarshal(comWire, &relido); err != nil {
		t.Fatalf("um cartao com origem declarada nao desserializa: %v", err)
	}
	if err := relido.Validate(); err != nil {
		t.Fatalf("um cartao com origem declarada nao valida depois do wire: %v", err)
	}
}

func TestAOS500_EdicaoQueMudaAOrigemApareceNoDiff(t *testing.T) {
	sem, com := planoDeLeitura(""), planoDeLeitura("doc_read")
	if sem.Nodes[0].extensionSignature() == com.Nodes[0].extensionSignature() {
		t.Fatal("por ou tirar a origem de uma saida nao mudou a assinatura das extensoes do no: a edicao passava por «sem mudanca»")
	}
	if com.Nodes[1].extensionSignature() != sem.Nodes[1].extensionSignature() {
		t.Fatal("a assinatura do no consumidor mudou sem o no ter mudado")
	}
}

func TestAOS500_AOrigemEUmSimboloNaConstrucaoENoWire(t *testing.T) {
	// Construção: texto livre no campo recusa o plano; não é saneado nem mostrado.
	for _, mau := range []string{"Doc_Read", "doc read", "doc:read", "tool=doc_read", segredoSentinela, strings.Repeat("a", 65)} {
		if _, err := BuildPlanCard(planoDeLeitura(mau)); !errors.Is(err, ErrNonCanonicalExtension) {
			t.Fatalf("from_tool=%q devia recusar o plano com ErrNonCanonicalExtension; veio %v", mau, err)
		}
		// E na porta do plano, antes de haver cartão: quem valida o plano sem o construir
		// (a edição humana revalida assim) tem a mesma recusa.
		if err := planoDeLeitura(mau).Validate(); !errors.Is(err, ErrNonCanonicalExtension) {
			t.Fatalf("Plan.Validate com from_tool=%q devia dar ErrNonCanonicalExtension; veio %v", mau, err)
		}
	}
	// Wire: o quarto segmento é exactamente `tool=<símbolo>`.
	for _, c := range []struct {
		saida string
		quer  bool
	}{
		{"notas:record:untrusted", true},
		{"notas:record:untrusted:tool=doc_read", true},
		{"notas:record:untrusted:tool=", false},
		{"notas:record:untrusted:doc_read", false},
		{"notas:record:untrusted:tool=Doc_Read", false},
		{"notas:record:untrusted:tool=doc read", false},
		{"notas:record:untrusted:tool=doc_read:tool=outra", false},
		{"notas:record:untrusted:fonte=doc_read", false},
		{"notas:record:untrusted:tool=" + segredoSentinela, false},
	} {
		if got := validCanonicalOutput(c.saida); got != c.quer {
			t.Fatalf("validCanonicalOutput(%q) = %v; quer %v", c.saida, got, c.quer)
		}
	}
	// E de ponta a ponta: um cartão de wire com texto no segmento da origem não entra.
	bom := wireDoCartao(t, planoDeLeitura("doc_read"))
	mau := strings.Replace(string(bom), "tool=doc_read", "tool=o cliente disse", 1)
	var card PlanCard
	if err := json.Unmarshal([]byte(mau), &card); err == nil {
		if err := card.Validate(); err == nil {
			t.Fatal("um cartao de wire com texto livre na origem da saida foi aceite")
		}
	}
}
