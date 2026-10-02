package referencemonitor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// aos478_producer_test.go — AOS-478: o principal da tool call chega ao envelope de TODOS os
// factos que o RM grava, e aos factos que a tool grava a jusante.

func aos478Call() Call {
	c := baseCall()
	c.Principal.DelegationChain = []DelegationHop{{Sub: "human:ana", ActAs: "nhi-1"}}
	return c
}

// TestAOS478_DespachoLevaOPrincipalNoContexto: a tool despachada vê, no contexto, o principal
// da call tal como a cadeia de hooks o deixou — é por aí que a sandbox o põe no envelope.
// E a decisão de permit devolve-o, que é por onde o step-ledger o lê.
func TestAOS478_DespachoLevaOPrincipalNoContexto(t *testing.T) {
	m := New(WithHooks(&spyHook{name: "policy", result: HookResult{Decision: HookAllow}}), WithEventSink(&fakeSink{}))
	var visto eventstore.Producer
	if err := m.Register("tool.echo", func(ctx context.Context, _ []byte) ([]byte, error) {
		visto = eventstore.ProducerFromContext(ctx)
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	call := aos478Call()
	d, err := m.Mediate(context.Background(), call)
	if err != nil || d.Effect != EffectPermit {
		t.Fatalf("permit esperado: eff=%q err=%v", d.Effect, err)
	}
	if visto.NHIID != call.Principal.NHIID || len(visto.DelegationChain) != 1 || len(visto.Scope) != 1 {
		t.Fatalf("a tool não viu o principal: %+v", visto)
	}
	if d.Principal.NHIID != call.Principal.NHIID || len(d.Principal.DelegationChain) != 1 {
		t.Fatalf("Decision.Principal = %+v", d.Principal)
	}
	if p := eventstore.ProducerFromContext(context.Background()); p.NHIID != "" {
		t.Fatalf("ProducerFromContext sem mediação devia ser zero: %+v", p)
	}
}

// TestAOS478_DesfechoLevaOMesmoProducerQueAMediacao: `tool.call.outcome` identifica o mesmo
// principal que o `tool.call.mediated` (antes vinha vazio). Grava-se pelo sink real sobre um
// Event Store real.
func TestAOS478_DesfechoLevaOMesmoProducerQueAMediacao(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	call := aos478Call()
	ctx := context.Background()
	if _, err := NewEventStoreSink(es).RecordMediation(ctx, MediationRecord{
		RunID: call.RunID, StepID: call.StepID, ToolID: call.ToolID, Effect: EffectPermit, Principal: call.Principal,
	}); err != nil {
		t.Fatal(err)
	}
	// Passo distinto: o sink do desfecho usa a MESMA chave (run_id, step_id) que o selo.
	if err := NewEventStoreOutcomeSink(es).RecordOutcome(ctx, OutcomeRecord{
		RunID: call.RunID, StepID: "step-outcome", ToolID: call.ToolID, Outcome: OutcomeOK, Principal: call.Principal,
	}); err != nil {
		t.Fatal(err)
	}
	evs, err := es.Read(ctx, call.RunID, 1)
	if err != nil || len(evs) != 2 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	a, _ := json.Marshal(evs[0].Producer)
	b, _ := json.Marshal(evs[1].Producer)
	if string(a) != string(b) || evs[1].Producer.NHIID == "" || len(evs[1].Producer.DelegationChain) != 1 {
		t.Fatalf("producer do desfecho %s != da mediação %s", b, a)
	}
}

// TestAOS478_GateDeAprovacaoPassaOPrincipalAoVerificador: o verificador (que CONSOME a
// aprovação e grava `approval.consumed`) recebe o principal da call no contexto.
func TestAOS478_GateDeAprovacaoPassaOPrincipalAoVerificador(t *testing.T) {
	v := &aos478Verificador{}
	call := aos478Call()
	call.ApprovalEvidence = []byte("grant-1")
	if _, err := NewApprovalGate(v).Evaluate(context.Background(), &call); err != nil {
		t.Fatal(err)
	}
	if v.visto.NHIID != call.Principal.NHIID {
		t.Fatalf("o verificador não viu o principal: %+v", v.visto)
	}
}

type aos478Verificador struct{ visto eventstore.Producer }

func (v *aos478Verificador) VerifyApproval(ctx context.Context, _, _ []byte) (ApprovalProof, error) {
	v.visto = eventstore.ProducerFromContext(ctx)
	return ApprovalProof{Approvers: []string{"human:ana"}}, nil
}

// TestAOS478_NegacaoLevaOPrincipal: `tool.call.denied`, gravado pelo sink real sobre um Event
// Store real, leva o principal da call — com a cadeia, quando a call a traz.
func TestAOS478_NegacaoLevaOPrincipal(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	m := New(WithHooks(&spyHook{name: "policy", result: HookResult{Decision: HookDeny, Reason: "nao"}}), WithEventSink(NewEventStoreSink(es)))
	call := aos478Call()
	d, err := m.Mediate(context.Background(), call)
	if err != nil || d.Effect != EffectDeny {
		t.Fatalf("deny esperado: eff=%q err=%v", d.Effect, err)
	}
	evs, err := es.Read(context.Background(), call.RunID, 1)
	if err != nil || len(evs) != 1 || evs[0].Type != EventTypeDenied {
		t.Fatalf("Read: err=%v evs=%+v", err, evs)
	}
	if p := evs[0].Producer; p.NHIID != call.Principal.NHIID || len(p.DelegationChain) != 1 {
		t.Fatalf("producer da negação=%+v", p)
	}
}
