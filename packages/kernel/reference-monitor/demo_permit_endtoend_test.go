package referencemonitor

import (
	"context"
	"encoding/json"
	"testing"
)

// TestDemo_PermitEndToEndInputOutput demonstra uma tool call PERMITIDA que executa de ponta a
// ponta, mostrando o INPUT e o OUTPUT. Usa o Reference Monitor REAL: um executor registado por
// Register + Mediate → (cadeia de mediação) → permit → dispatch → Output. Corre com -v para ver
// input/output. É também uma asserção: o output tem de ser o conteúdo do documento pedido.
func TestDemo_PermitEndToEndInputOutput(t *testing.T) {
	m := New(WithEventSink(&fakeSink{}))

	// Executor REAL da tool doc_read: recebe {"doc_id"} e devolve o conteúdo do documento.
	docs := map[string]string{
		"notes": "Reuniao 3a: rever o plano de migracao. Owner: alice.",
	}
	if err := m.Register("doc_read", func(_ context.Context, input []byte) ([]byte, error) {
		var in struct {
			DocID string `json:"doc_id"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, err
		}
		out, _ := json.Marshal(map[string]string{"doc_id": in.DocID, "content": docs[in.DocID]})
		return out, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	input := []byte(`{"doc_id":"notes"}`)
	call := Call{
		RunID: "run-demo", StepID: "step-1", ToolID: "doc_read", Capability: "cap:fs.read",
		Resource:  Resource{Type: "doc", Value: "notes", Region: "eu"},
		Principal: Principal{NHIID: "nhi-alice", AgentID: "agt-app", Authority: []string{"cap:fs.read"}},
		Context:   CallContext{Taint: "trusted", BudgetTokensRemaining: 1000},
		Input:     input,
	}

	d, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if !d.Permitted() {
		t.Fatalf("esperava PERMIT, veio Effect=%q", d.Effect)
	}
	var got struct{ Content string }
	_ = json.Unmarshal(d.Output, &got)
	if got.Content != docs["notes"] {
		t.Fatalf("output inesperado: %s", d.Output)
	}

	t.Logf("\n"+
		"  TOOL         : doc_read (cap:fs.read)\n"+
		"  INPUT        : %s\n"+
		"  DECISAO      : %s  (MediationSeq=%d, auditado antes do efeito)\n"+
		"  OUTPUT       : %s\n"+
		"  result_taint : o output volta ao loop marcado UNTRUSTED (fronteira de dados)",
		input, d.Effect, d.MediationSeq, d.Output)
}
