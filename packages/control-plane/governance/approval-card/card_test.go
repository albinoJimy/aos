package approvalcard

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/substrate/redaction"
)

// TestBuildCard_PreviewConcreto (AC1): o card apresenta o EFEITO CONCRETO RESOLVIDO —
// os valores reais (capability + recurso resolvido + args), não um template genérico
// da tool.
func TestBuildCard_PreviewConcreto(t *testing.T) {
	req := risk.ConfirmationRequest{
		Class:        risk.ClassDanger,
		Irreversible: true,
		// Efeito concreto resolvido: capability + destino real (não "executar tool X").
		Preview:    "cap:fs.delete -> file:/data/prod/customers.db [eu]",
		Principal:  "agent-1",
		Capability: "cap:fs.delete",
		Resource:   "/data/prod/customers.db",
	}
	card, err := BuildCard(req, WithRequestID("card-1"))
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}
	// O preview tem de conter os valores RESOLVIDOS, não um placeholder.
	for _, want := range []string{"cap:fs.delete", "/data/prod/customers.db", "eu"} {
		if !strings.Contains(card.Preview, want) {
			t.Fatalf("preview nao contem o valor resolvido %q: %q", want, card.Preview)
		}
	}
	if card.Capability != "cap:fs.delete" || card.Resource != "/data/prod/customers.db" {
		t.Fatalf("capability/resource resolvidos nao lidos: %+v", card)
	}
	if strings.Contains(strings.ToLower(card.Preview), "template") || strings.Contains(card.Preview, "{{") {
		t.Fatalf("preview parece um template, nao o efeito resolvido: %q", card.Preview)
	}
}

// TestBuildCard_LeituraDeRisco (AC2): o card LÊ a classe e a reversibilidade do gate
// (AOS-074) — não reclassifica. Prova NÃO-TAUTOLÓGICA: mesmo quando os valores da
// request CONTRARIAM o que um classificador local produziria (Class=safe com uma
// capability de delete irreversível), o card ecoa FIELMENTE o que o gate carrega, em
// vez de recalcular.
func TestBuildCard_LeituraDeRisco(t *testing.T) {
	// Request deliberadamente "inconsistente" com o que risk.Classify diria: o card
	// não pode chamar Classify, só ler.
	req := risk.ConfirmationRequest{
		Class:        risk.ClassSafe, // um classificador diria danger (delete/irreversible)
		Irreversible: true,
		Preview:      "cap:fs.delete -> file:/tmp/x",
		Principal:    "agent-9",
		Capability:   "cap:fs.delete",
		Resource:     "/tmp/x",
	}
	card, err := BuildCard(req, WithRequestID("card-2"))
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}
	if card.Class != risk.ClassSafe {
		t.Fatalf("card reclassificou: Class=%v, esperado o LIDO ClassSafe", card.Class)
	}
	if !card.Irreversible {
		t.Fatalf("card nao leu Irreversible do gate")
	}
	if card.Reversibility != risk.Irreversible {
		t.Fatalf("reversibilidade derivada incoerente: %v", card.Reversibility)
	}
	// A comparação canónica: o rótulo exibido == o do gate.
	if card.Class.String() != req.Class.String() {
		t.Fatalf("rotulo de classe divergente do gate: %q != %q", card.Class.String(), req.Class.String())
	}
}

// TestBuildCard_DualControlRequiredSoIrreversivel: DualControlRequired sse Irreversible.
func TestBuildCard_DualControlRequiredSoIrreversivel(t *testing.T) {
	rev := risk.ConfirmationRequest{Class: risk.ClassDanger, Irreversible: false, Principal: "a", Capability: "cap:http.post", Resource: "https://x"}
	card, err := BuildCard(rev, WithRequestID("c"))
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}
	if card.DualControlRequired {
		t.Fatalf("acao reversivel nao devia exigir dual-control")
	}
	irr := rev
	irr.Irreversible = true
	card2, err := BuildCard(irr, WithRequestID("c"))
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}
	if !card2.DualControlRequired {
		t.Fatalf("acao irreversivel tem de exigir dual-control")
	}
}

// TestBuildCard_ReversibilidadeCoerente: um rótulo de reversibilidade que contradiz o
// bool Irreversible é rejeitado fail-closed. Uma acção irreversível NÃO pode exibir
// "reversible" no eixo apresentado — o rótulo cosmético não pode contradizer o bool que
// motiva o dual-control (senão a supervisão veria "reversible" numa acção que exige dois
// aprovadores, minando a fidelidade do preview de AOS-120).
func TestBuildCard_ReversibilidadeCoerente(t *testing.T) {
	req := risk.ConfirmationRequest{
		Class:        risk.ClassDanger,
		Irreversible: true,
		Preview:      "cap:fs.delete -> file:/data/x",
		Principal:    "agent-1",
		Capability:   "cap:fs.delete",
		Resource:     "/data/x",
	}
	// Enriquecimento incoerente: irreversível mas exibindo "reversible".
	if _, err := BuildCard(req, WithRequestID("card-incoerente"), WithReversibility(risk.Reversible)); err == nil {
		t.Fatal("irreversivel com Reversibility=Reversible devia ser rejeitado (fail-closed)")
	}
	// Um enriquecimento coerente (Irreversible, o pior caso) é aceite.
	card, err := BuildCard(req, WithRequestID("card-coerente"), WithReversibility(risk.Irreversible))
	if err != nil {
		t.Fatalf("reversibilidade coerente devia ser aceite: %v", err)
	}
	if card.Reversibility != risk.Irreversible {
		t.Fatalf("reversibilidade nao lida: %v", card.Reversibility)
	}

	// A mesma incoerência introduzida no wire (Irreversible=true, reversibility="reversible")
	// é rejeitada no Unmarshal, via Validate.
	incoerente := `{"schema_version":"1.0.0","request_id":"r","requester":"a","class":"danger",` +
		`"irreversible":true,"reversibility":"reversible","preview":"p","capability":"c",` +
		`"resource":"x","batch":false,"dual_control_required":true}`
	var bad ApprovalCard
	if err := json.Unmarshal([]byte(incoerente), &bad); err == nil {
		t.Fatal("wire incoerente (irreversible+reversible) devia ser rejeitado no Unmarshal")
	}
}

// TestBuildCard_ValidacaoFailClosed: RequestID vazio e Requester vazio são recusados.
func TestBuildCard_ValidacaoFailClosed(t *testing.T) {
	if _, err := BuildCard(risk.ConfirmationRequest{Principal: "a", Irreversible: false}); err == nil {
		t.Fatal("RequestID vazio devia falhar (fail-closed)")
	}
	if _, err := BuildCard(risk.ConfirmationRequest{Irreversible: false}, WithRequestID("x")); err == nil {
		t.Fatal("Requester vazio devia falhar (fail-closed)")
	}
}

// TestCard_SemSegredoNemPII (AC6): o preview do card, JÁ redigido, não contém PII em
// claro — [redaction.Engine.Scan] devolve vazio — e o card NUNCA contém o Call.Input
// (o segredo). Prova por Scan == [].
func TestCard_SemSegredoNemPII(t *testing.T) {
	engine := redaction.NewEngine(nil) // RemoveAll não precisa de KeySource
	policy := redaction.RemoveAllPolicy("card-redact-v1")

	// O preview vindo do gate podia arrastar PII no destino resolvido; o Resource é um
	// email. O Call.Input (o segredo) NUNCA é passado ao card.
	const secretInput = "AWS_SECRET=abcd1234EXFIL"
	req := risk.ConfirmationRequest{
		Class:        risk.ClassDanger,
		Irreversible: true,
		Preview:      "cap:mail.send -> email:jane.doe@example.com",
		Principal:    "agent-1",
		Capability:   "cap:mail.send",
		Resource:     "jane.doe@example.com",
	}
	card, err := BuildCard(req, WithRequestID("card-pii"), WithRedaction(engine, "agent-1", policy))
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}

	// GATE de ausência: nenhuma PII em claro no preview nem no resource redigidos.
	if f := engine.ScanText(card.Preview); len(f) != 0 {
		t.Fatalf("preview do card contem PII em claro: %+v (%q)", f, card.Preview)
	}
	if f := engine.ScanText(card.Resource); len(f) != 0 {
		t.Fatalf("resource do card contem PII em claro: %+v (%q)", f, card.Resource)
	}
	// O email em claro tem de ter sido substituído pelo marcador.
	if strings.Contains(card.Preview, "jane.doe@example.com") {
		t.Fatalf("email em claro vazou no preview: %q", card.Preview)
	}

	// O segredo (Call.Input) nunca entra no card — nem na sua serialização.
	blob, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), secretInput) || strings.Contains(string(blob), "abcd1234EXFIL") {
		t.Fatalf("segredo vazou na serializacao do card: %s", blob)
	}
	if f := engine.ScanJSON(blob); len(f) != 0 {
		t.Fatalf("serializacao do card contem PII em claro: %+v", f)
	}
}

// TestCard_SerializacaoVersionada (AC5): JSON estável round-trip com schema_version
// carimbado; um MAJOR incompatível é REJEITADO fail-closed.
func TestCard_SerializacaoVersionada(t *testing.T) {
	req := risk.ConfirmationRequest{
		Class:        risk.ClassDanger,
		Irreversible: true,
		Preview:      "cap:fs.delete -> file:/data/x",
		Principal:    "agent-1",
		Capability:   "cap:fs.delete",
		Resource:     "/data/x",
	}
	card, err := BuildCard(req, WithRequestID("card-ser"), WithEstimatedCost(1200, 3400))
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}
	blob, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// schema_version carimbado.
	if !strings.Contains(string(blob), `"schema_version":"1.0.0"`) {
		t.Fatalf("schema_version nao carimbado: %s", blob)
	}
	// Round-trip fiel.
	var back ApprovalCard
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.RequestID != card.RequestID || back.Class != card.Class || back.Irreversible != card.Irreversible ||
		back.Preview != card.Preview || back.Capability != card.Capability || back.Resource != card.Resource ||
		back.DualControlRequired != card.DualControlRequired || !back.SchemaVersion.Equal(card.SchemaVersion) {
		t.Fatalf("round-trip divergente:\n a=%+v\n b=%+v", card, back)
	}
	if back.EstimatedCost == nil || back.EstimatedCost.EstimatedTokens != 1200 || back.EstimatedCost.MicroUSD != 3400 {
		t.Fatalf("custo estimado nao round-trip: %+v", back.EstimatedCost)
	}

	// MAJOR incompatível: rejeitado fail-closed no Unmarshal (via Validate).
	incompat := strings.Replace(string(blob), `"schema_version":"1.0.0"`, `"schema_version":"2.0.0"`, 1)
	var bad ApprovalCard
	if err := json.Unmarshal([]byte(incompat), &bad); err == nil {
		t.Fatal("MAJOR incompativel (2.0.0) devia ser rejeitado (fail-closed)")
	}

	// schema_version malformada: rejeitada.
	malformed := strings.Replace(string(blob), `"schema_version":"1.0.0"`, `"schema_version":"1.0"`, 1)
	var bad2 ApprovalCard
	if err := json.Unmarshal([]byte(malformed), &bad2); err == nil {
		t.Fatal("schema_version malformada devia ser rejeitada (fail-closed)")
	}
}

// TestParseCardSchemaVersion_FailClosed cobre as formas rejeitadas e a ordenação.
func TestParseCardSchemaVersion_FailClosed(t *testing.T) {
	for _, bad := range []string{"", "1", "1.0", "1.0.0.0", "1.0.x", "-1.0.0", "a.b.c", " "} {
		if _, err := ParseCardSchemaVersion(bad); err == nil {
			t.Fatalf("versao %q devia falhar", bad)
		}
	}
	v, err := ParseCardSchemaVersion("1.2.3")
	if err != nil || v.Major != 1 || v.Minor != 2 || v.Patch != 3 {
		t.Fatalf("parse 1.2.3: %v %+v", err, v)
	}
	if !CurrentVersion.Compatible(CardSchemaVersion{Major: 1, Minor: 9, Patch: 9}) {
		t.Fatal("mesmo MAJOR devia ser compativel")
	}
	if CurrentVersion.Compatible(CardSchemaVersion{Major: 2}) {
		t.Fatal("MAJOR diferente nao devia ser compativel")
	}
	if Classify(CardSchemaVersion{1, 0, 0}, CardSchemaVersion{2, 0, 0}) != ChangeMajor {
		t.Fatal("mudanca de MAJOR devia ser ChangeMajor")
	}
	if Classify(CardSchemaVersion{1, 0, 0}, CardSchemaVersion{1, 1, 0}) != ChangeMinor {
		t.Fatal("mudanca de MINOR devia ser ChangeMinor")
	}
}
