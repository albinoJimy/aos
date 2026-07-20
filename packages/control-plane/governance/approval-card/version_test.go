package approvalcard

import (
	"context"
	"encoding/json"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// TestVersion_StringCompareClassify cobre a álgebra SemVer (String/Compare/Classify/
// ChangeKind.String) em todos os ramos.
func TestVersion_StringCompareClassify(t *testing.T) {
	if got := (CardSchemaVersion{1, 2, 3}).String(); got != "1.2.3" {
		t.Fatalf("String: %q", got)
	}
	base := CardSchemaVersion{1, 1, 1}
	cases := []struct {
		o    CardSchemaVersion
		want int
	}{
		{CardSchemaVersion{2, 0, 0}, -1}, // major maior
		{CardSchemaVersion{0, 9, 9}, 1},  // major menor
		{CardSchemaVersion{1, 2, 0}, -1}, // minor maior
		{CardSchemaVersion{1, 0, 9}, 1},  // minor menor
		{CardSchemaVersion{1, 1, 2}, -1}, // patch maior
		{CardSchemaVersion{1, 1, 0}, 1},  // patch menor
		{CardSchemaVersion{1, 1, 1}, 0},  // igual
	}
	for _, c := range cases {
		if got := base.Compare(c.o); got != c.want {
			t.Fatalf("Compare(%v,%v)=%d, esperado %d", base, c.o, got, c.want)
		}
	}
	if !base.Equal(CardSchemaVersion{1, 1, 1}) {
		t.Fatal("Equal falhou")
	}
	// Classify em todos os ramos.
	if Classify(CardSchemaVersion{1, 0, 0}, CardSchemaVersion{1, 0, 1}) != ChangePatch {
		t.Fatal("esperado ChangePatch")
	}
	if Classify(CardSchemaVersion{1, 0, 0}, CardSchemaVersion{1, 0, 0}) != ChangeNone {
		t.Fatal("esperado ChangeNone")
	}
	for k, want := range map[ChangeKind]string{ChangeNone: "none", ChangePatch: "patch", ChangeMinor: "minor", ChangeMajor: "major", ChangeKind(99): "unknown"} {
		if k.String() != want {
			t.Fatalf("ChangeKind(%d).String()=%q, esperado %q", k, k.String(), want)
		}
	}
}

// TestBuildCard_WithReversibilityEParseLabels cobre WithReversibility e o round-trip
// dos rótulos de classe (safe/gray) e reversibilidade (reversible).
func TestBuildCard_WithReversibilityEParseLabels(t *testing.T) {
	// Reversível com WithReversibility explícito (valor LIDO da classificação).
	rev := risk.ConfirmationRequest{Class: risk.ClassGray, Irreversible: false, Preview: "p", Principal: "a", Capability: "cap:http.get", Resource: "https://x"}
	card, err := BuildCard(rev, WithRequestID("c"), WithReversibility(risk.Reversible))
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}
	if card.Reversibility != risk.Reversible {
		t.Fatalf("WithReversibility nao aplicado: %v", card.Reversibility)
	}
	// Round-trip de um card gray reversível: exercita parseClass("gray") e
	// parseReversibility("reversible").
	blob, _ := json.Marshal(card)
	var back ApprovalCard
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal gray/reversible: %v", err)
	}
	if back.Class != risk.ClassGray || back.Reversibility != risk.Reversible {
		t.Fatalf("parse de rotulos gray/reversible incorrecto: %+v", back)
	}

	// Round-trip de um card safe (parseClass("safe")).
	safe := risk.ConfirmationRequest{Class: risk.ClassSafe, Irreversible: false, Preview: "p", Principal: "a", Capability: "cap:fs.read", Resource: "/x"}
	sc, err := BuildCard(safe, WithRequestID("s"))
	if err != nil {
		t.Fatalf("BuildCard safe: %v", err)
	}
	sb, _ := json.Marshal(sc)
	var sback ApprovalCard
	if err := json.Unmarshal(sb, &sback); err != nil {
		t.Fatalf("unmarshal safe: %v", err)
	}
	if sback.Class != risk.ClassSafe {
		t.Fatalf("parseClass(safe) incorrecto: %v", sback.Class)
	}
}

// TestNewDualControlCollector_NilFailClosed: canal nil é fail-closed.
func TestNewDualControlCollector_NilFailClosed(t *testing.T) {
	if _, err := NewDualControlCollector(nil); err != ErrNilChannel {
		t.Fatalf("canal nil devia dar ErrNilChannel, deu %v", err)
	}
}

// TestValidate_IncoerenciaDualControl: um card com dual-control incoerente com o
// irreversível é recusado (fail-closed), tal como um Unmarshal desse wire.
func TestValidate_IncoerenciaDualControl(t *testing.T) {
	// Construído à mão (não via BuildCard) para forçar a incoerência.
	bad := ApprovalCard{SchemaVersion: CurrentVersion, RequestID: "r", Requester: "a", Irreversible: false, DualControlRequired: true}
	if err := bad.Validate(); err != ErrInvalidCard {
		t.Fatalf("dual-control sem irreversivel devia ser ErrInvalidCard, deu %v", err)
	}
}

// TestSpan_DenyEmiteDecisaoDeny cobre decisionLabel(false) via um span de apresentação
// com decisão negada.
func TestSpan_DenyEmiteDecisaoDeny(t *testing.T) {
	tracer := &agentruntime.RecordingTracer{}
	coll, _ := NewDualControlCollector(risk.DenyChannel{}, WithTracer(tracer))
	if _, err := coll.Authorize(context.Background(), irreversibleCard(t)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	spans := tracer.SpansByOperation(OpApprovalCard)
	if len(spans) != 1 || spans[0].Attributes[AttrDecision] != "deny" {
		t.Fatalf("esperava um span com decisao deny, obtive %+v", spans)
	}
	if spans[0].Attributes[AttrCardApproverCount] != 0 {
		t.Fatalf("deny devia ter 0 aprovadores: %v", spans[0].Attributes[AttrCardApproverCount])
	}
}

// TestAuthorize_CardInvalidoNaoApresenta: um card inválido é recusado ANTES de qualquer
// apresentação (o canal nunca é chamado).
func TestAuthorize_CardInvalidoNaoApresenta(t *testing.T) {
	spy := &spyChannel{}
	coll, _ := NewDualControlCollector(spy)
	bad := ApprovalCard{SchemaVersion: CurrentVersion} // RequestID/Requester vazios
	dec, err := coll.Authorize(context.Background(), bad)
	if err == nil || dec.Authorized {
		t.Fatalf("card invalido devia falhar fail-closed, deu dec=%+v err=%v", dec, err)
	}
	if len(spy.seen) != 0 {
		t.Fatalf("canal nao devia ser chamado para um card invalido, chamado %d vezes", len(spy.seen))
	}
}
