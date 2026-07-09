package pdp

import (
	"context"
	"encoding/json"
	"testing"

	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// buildRM constrói um Reference Monitor com o PDP como hook de política e stubs
// neutros nos restantes hooks, gravando no Event Store real (AOS-002).
//
// FRONTEIRA DE CONFIANÇA (AOS-007). Este harness usa DELIBERADAMENTE o
// rm.IdentityStub neutro (pass-through): a identidade está fora do âmbito destes
// testes focados no PDP, pelo que a agent_class chega do Call tal-qual. NÃO é a
// composição segura de produção — sob IdentityStub a agent_class é input do
// caller e forjável. A composição REAL (IdentityCheck a re-derivar o Principal do
// token verificado antes do gate) e a prova de que a classe forjada é ignorada
// estão em identity_gate_integration_test.go.
func buildRM(t testing.TB, store eventstore.EventStore) *rm.Monitor {
	t.Helper()
	p := mustOpen(t)
	return rm.New(
		rm.WithHooks(
			rm.IdentityStub{},
			NewPolicyCheck(p), // PDP real no lugar do PolicyStub
			rm.BudgetStub{},
			rm.EgressStub{},
			rm.AuditStub{},
		),
		rm.WithEventSink(rm.NewEventStoreSink(store)),
	)
}

// permitCall é um Call que a política PERMITE.
func permitCall() rm.Call {
	return rm.Call{
		RequestID: "req-permit", RunID: "run-permit", StepID: "step-1",
		ToolID: "tool.http", Capability: "cap:http.post",
		Resource:  rm.Resource{Type: "url", Value: "https://api.example.com/orders", Region: "eu"},
		Principal: rm.Principal{NHIID: "nhi-1", AgentClass: "agent-worker", Authority: []string{"cap:http.post"}},
		Context:   rm.CallContext{Taint: "trusted", Sensitivity: "confidential"},
		Input:     []byte("body"),
	}
}

// denyCall é um Call que a política NEGA (região != eu).
func denyCall() rm.Call {
	c := permitCall()
	c.RequestID, c.RunID = "req-deny", "run-deny"
	c.Resource.Region = "us"
	return c
}

// payloadView é a vista mínima do payload do evento de mediação (o tipo real é
// não-exportado no RM; lemos os campos por JSON).
type payloadView struct {
	Decision      string `json:"decision"`
	PolicyVersion string `json:"policy_version"`
	Capability    string `json:"capability"`
}

func readOne(t testing.TB, store eventstore.EventStore, stream string) eventstore.Event {
	t.Helper()
	events, err := store.Read(context.Background(), stream, 1)
	if err != nil {
		t.Fatalf("Read(%s): %v", stream, err)
	}
	if len(events) != 1 {
		t.Fatalf("esperava 1 evento em %s, obtive %d", stream, len(events))
	}
	return events[0]
}

// TestIntegration_RM_PDP_Permit: um call permitido pela política é mediado
// permit e o evento de mediação regista a policy_version.
func TestIntegration_RM_PDP_Permit(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	m := buildRM(t, store)
	var dispatched bool
	if err := m.Register("tool.http", func(_ context.Context, in []byte) ([]byte, error) {
		dispatched = true
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	d, err := m.Mediate(context.Background(), permitCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if !d.Permitted() {
		t.Fatalf("esperava permit, obtive %q (%s)", d.Effect, d.Reason)
	}
	if !dispatched {
		t.Error("a tool devia ter sido despachada")
	}
	// Obrigações da política chegam à Decision do RM (audit + redact_pii porque
	// sensitivity=confidential).
	if len(d.Obligations) != 2 {
		t.Errorf("esperava 2 obligations (redact_pii + audit), obtive %+v", d.Obligations)
	}

	ev := readOne(t, store, "run-permit")
	if ev.Type != rm.EventTypeMediated {
		t.Errorf("Type=%q, esperava %q", ev.Type, rm.EventTypeMediated)
	}
	var pl payloadView
	if err := json.Unmarshal(ev.Payload, &pl); err != nil {
		t.Fatalf("payload invalido: %v", err)
	}
	if pl.Decision != "permit" {
		t.Errorf("payload.decision=%q, esperava permit", pl.Decision)
	}
	if pl.PolicyVersion != "1.0.0" {
		t.Errorf("payload.policy_version=%q, esperava 1.0.0 (registo no audit)", pl.PolicyVersion)
	}
}

// stubDecider é um duplo do decisor para exercitar directamente os ramos do
// adaptador (contrato C1) sem depender do PDP real.
type stubDecider struct {
	dec Decision
	err error
}

func (s stubDecider) Decide(context.Context, Input) (Decision, error) { return s.dec, s.err }

// TestPolicyCheck_Evaluate_FailClosedBranches cobre directamente os ramos
// fail-closed e o mapeamento de efeitos do adaptador C1 (PolicyCheck.Evaluate):
// PDP nil, política indisponível (unloaded), permit, deny e escalate.
func TestPolicyCheck_Evaluate_FailClosedBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	call := permitCall()

	t.Run("pdp_nil_fail_closed", func(t *testing.T) {
		t.Parallel()
		c := NewPolicyCheck(nil) // sem decisor ⇒ fail-closed
		res, err := c.Evaluate(ctx, &call)
		if err != nil {
			t.Fatalf("Evaluate nao devia devolver erro: %v", err)
		}
		if res.Decision != rm.HookDeny {
			t.Errorf("Decision=%v, esperava HookDeny", res.Decision)
		}
	})

	t.Run("politica_indisponivel_deny", func(t *testing.T) {
		t.Parallel()
		// PDP sem bundle: Decide devolve E_POLICY_UNAVAILABLE (ramo de erro de porta).
		c := NewPolicyCheck(NewUnloaded())
		res, err := c.Evaluate(ctx, &call)
		if err != nil {
			t.Fatalf("Evaluate nao devia propagar erro (fail-closed): %v", err)
		}
		if res.Decision != rm.HookDeny {
			t.Errorf("Decision=%v, esperava HookDeny", res.Decision)
		}
		if res.PolicyVersion != "" {
			t.Errorf("PolicyVersion=%q, esperava vazia sem bundle", res.PolicyVersion)
		}
	})

	t.Run("escalate_mapeia_hook_escalate", func(t *testing.T) {
		t.Parallel()
		c := &PolicyCheck{pdp: stubDecider{dec: Decision{Effect: Escalate, Reason: "gate humano", PolicyVersion: "1.0.0"}}}
		res, err := c.Evaluate(ctx, &call)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if res.Decision != rm.HookEscalate {
			t.Errorf("Decision=%v, esperava HookEscalate", res.Decision)
		}
		if res.PolicyVersion != "1.0.0" {
			t.Errorf("PolicyVersion=%q, esperava 1.0.0", res.PolicyVersion)
		}
	})

	t.Run("efeito_desconhecido_fail_closed", func(t *testing.T) {
		t.Parallel()
		// Um efeito não reconhecido cai no default → deny (fail-closed).
		c := &PolicyCheck{pdp: stubDecider{dec: Decision{Effect: Effect("weird"), PolicyVersion: "1.0.0"}}}
		res, _ := c.Evaluate(ctx, &call)
		if res.Decision != rm.HookDeny {
			t.Errorf("Decision=%v, esperava HookDeny para efeito desconhecido", res.Decision)
		}
	})
}

// TestIntegration_RM_PDP_Deny: um call negado pela política é mediado deny e o
// evento de negação também regista a policy_version.
func TestIntegration_RM_PDP_Deny(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	m := buildRM(t, store)
	var dispatched bool
	_ = m.Register("tool.http", func(_ context.Context, in []byte) ([]byte, error) {
		dispatched = true
		return in, nil
	})

	d, err := m.Mediate(context.Background(), denyCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny {
		t.Fatalf("esperava deny, obtive %q", d.Effect)
	}
	if d.DeniedBy != "policy" {
		t.Errorf("DeniedBy=%q, esperava policy", d.DeniedBy)
	}
	if dispatched {
		t.Error("a tool NAO devia ser despachada num deny")
	}

	ev := readOne(t, store, "run-deny")
	if ev.Type != rm.EventTypeDenied {
		t.Errorf("Type=%q, esperava %q", ev.Type, rm.EventTypeDenied)
	}
	var pl payloadView
	if err := json.Unmarshal(ev.Payload, &pl); err != nil {
		t.Fatalf("payload invalido: %v", err)
	}
	if pl.Decision != "deny" {
		t.Errorf("payload.decision=%q, esperava deny", pl.Decision)
	}
	if pl.PolicyVersion != "1.0.0" {
		t.Errorf("payload.policy_version=%q, esperava 1.0.0 na negacao", pl.PolicyVersion)
	}
}
