package audit

import (
	"bytes"
	"testing"
	"time"
)

// fixedTime é um timestamp determinístico para testes.
var fixedTime = time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

func sampleRecord(partition string, dec Decision) AuditRecord {
	return AuditRecord{
		Partition: partition,
		Timestamp: fixedTime,
		Decision:  dec,
		Principal: Principal{
			NHIID: "nhi:agent-1",
			DelegationChain: []DelegationHop{
				{Sub: "human:alice", ActAs: "nhi:agent-1"},
			},
		},
		Capability:    "fs:write:/reports/*",
		PolicyVersion: "1.2.0",
	}
}

// TestCanonicalDeterminism — o mesmo conteúdo produz sempre os mesmos bytes e o
// mesmo EntryHash (determinismo da serialização canónica).
func TestCanonicalDeterminism(t *testing.T) {
	r1 := sampleRecord("p", DecisionAllow)
	r2 := sampleRecord("p", DecisionAllow)

	if !bytes.Equal(canonicalContent(r1), canonicalContent(r2)) {
		t.Fatal("serializacao canonica nao deterministica para conteudo igual")
	}
	prev := GenesisHash("p")
	if !bytes.Equal(ComputeEntryHash(prev, r1), ComputeEntryHash(prev, r2)) {
		t.Fatal("EntryHash nao deterministico para conteudo igual")
	}
}

// TestCanonicalSensitivity — qualquer alteração de um campo de conteúdo muda o
// EntryHash (nenhum campo escapa ao selo).
func TestCanonicalSensitivity(t *testing.T) {
	base := sampleRecord("p", DecisionAllow)
	prev := GenesisHash("p")
	baseHash := ComputeEntryHash(prev, base)

	mutators := map[string]func(*AuditRecord){
		"decision":        func(r *AuditRecord) { r.Decision = DecisionDeny },
		"capability":      func(r *AuditRecord) { r.Capability = "fs:read:/x" },
		"policy_version":  func(r *AuditRecord) { r.PolicyVersion = "9.9.9" },
		"nhi":             func(r *AuditRecord) { r.Principal.NHIID = "nhi:other" },
		"delegation":      func(r *AuditRecord) { r.Principal.DelegationChain[0].Sub = "human:bob" },
		"timestamp":       func(r *AuditRecord) { r.Timestamp = fixedTime.Add(time.Second) },
		"seq":             func(r *AuditRecord) { r.AuditSeq = 42 },
		"partition":       func(r *AuditRecord) { r.Partition = "q" },
		"payload_present": func(r *AuditRecord) { r.PayloadRef = &PayloadRef{ContentHash: []byte{1}, SubjectID: "s"} },
		// Campos de correlação/alvo/contexto/obligations selados (AOS-011).
		"run_id":          func(r *AuditRecord) { r.RunID = "run-forjado" },
		"step_id":         func(r *AuditRecord) { r.StepID = "s-forjado" },
		"parent_step_id":  func(r *AuditRecord) { r.ParentStepID = "sp-forjado" },
		"request_id":      func(r *AuditRecord) { r.RequestID = "req-forjado" },
		"tool_id":         func(r *AuditRecord) { r.ToolID = "tool.forjada" },
		"resource_type":   func(r *AuditRecord) { r.Resource.Type = "db" },
		"resource_value":  func(r *AuditRecord) { r.Resource.Value = "s3://forjado" },
		"resource_region": func(r *AuditRecord) { r.Resource.Region = "us" },
		"ctx_taint":       func(r *AuditRecord) { r.Context.Taint = "untrusted" },
		"ctx_revers":      func(r *AuditRecord) { r.Context.Reversibility = "irreversible" },
		"ctx_sens":        func(r *AuditRecord) { r.Context.Sensitivity = "confidential" },
		"obligation_type": func(r *AuditRecord) { r.Obligations = []Obligation{{Type: "redact_pii"}} },
		"obligation_field": func(r *AuditRecord) {
			r.Obligations = []Obligation{{Type: "redact_pii", Fields: []string{"email"}}}
		},
		"obligation_param": func(r *AuditRecord) {
			r.Obligations = []Obligation{{Type: "ttl", Params: map[string]string{"seconds": "3600"}}}
		},
	}
	for name, mut := range mutators {
		t.Run(name, func(t *testing.T) {
			r := sampleRecord("p", DecisionAllow)
			mut(&r)
			if bytes.Equal(ComputeEntryHash(prev, r), baseHash) {
				t.Fatalf("mutacao %q nao alterou o EntryHash", name)
			}
		})
	}
}

// TestObligationParamsDeterministic — Params é um mapa, mas a serialização
// canónica ordena as chaves, pelo que o mesmo conteúdo produz sempre o mesmo hash
// independentemente da ordem de iteração do mapa (AOS-011, completude schema-fidelity).
func TestObligationParamsDeterministic(t *testing.T) {
	prev := GenesisHash("p")
	mk := func() AuditRecord {
		r := sampleRecord("p", DecisionAllow)
		r.Obligations = []Obligation{{
			Type:   "ttl",
			Fields: []string{"email", "phone"},
			Params: map[string]string{"seconds": "3600", "region": "eu", "z": "1", "a": "2"},
		}}
		return r
	}
	first := ComputeEntryHash(prev, mk())
	for i := 0; i < 200; i++ {
		if !bytes.Equal(ComputeEntryHash(prev, mk()), first) {
			t.Fatal("hash de obligations com Params (mapa) nao deterministico")
		}
	}
}

// TestGenesisPerPartition — a génese é determinística e distinta por partição.
func TestGenesisPerPartition(t *testing.T) {
	g1a := GenesisHash("tenant-a")
	g1b := GenesisHash("tenant-a")
	g2 := GenesisHash("tenant-b")

	if !bytes.Equal(g1a, g1b) {
		t.Fatal("genese nao deterministica para a mesma particao")
	}
	if bytes.Equal(g1a, g2) {
		t.Fatal("genese identica para particoes distintas")
	}
	if len(g1a) != 32 {
		t.Fatalf("genese deve ser SHA-256 (32 bytes), tem %d", len(g1a))
	}
}

// TestPayloadRefAbsentVsEmpty — presença de PayloadRef vazio difere de ausência.
func TestPayloadRefAbsentVsEmpty(t *testing.T) {
	prev := GenesisHash("p")
	absent := sampleRecord("p", DecisionAllow)
	empty := sampleRecord("p", DecisionAllow)
	empty.PayloadRef = &PayloadRef{}

	if bytes.Equal(ComputeEntryHash(prev, absent), ComputeEntryHash(prev, empty)) {
		t.Fatal("PayloadRef ausente e presente-vazio devem produzir hashes distintos")
	}
}
