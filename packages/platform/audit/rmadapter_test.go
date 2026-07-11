package audit

import (
	"context"
	"errors"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// policyHook é um hook de teste que permite (ou nega) e propaga uma policy_version,
// para exercitar o fluxo RM → audit com os campos exigidos.
type policyHook struct {
	name    string
	decide  referencemonitor.HookDecision
	version string
}

func (h policyHook) Name() string { return h.name }

func (h policyHook) Evaluate(context.Context, *referencemonitor.Call) (referencemonitor.HookResult, error) {
	return referencemonitor.HookResult{Decision: h.decide, PolicyVersion: h.version}, nil
}

func newMonitor(t *testing.T, store Store, hooks ...referencemonitor.Hook) *referencemonitor.Monitor {
	t.Helper()
	sink := NewMediationSink(store, withSinkClock(func() time.Time { return fixedTime }))
	return referencemonitor.New(
		referencemonitor.WithHooks(hooks...),
		referencemonitor.WithEventSink(sink),
	)
}

// TestRMDecisionReachesAudit — uma mediação permit do RM entra na hash-chain com
// decisão=allow, principal, capability e policy_version (Testes Requeridos).
func TestRMDecisionReachesAudit(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	mon := newMonitor(t, store, policyHook{name: "policy", decide: referencemonitor.HookAllow, version: "2.1.0"})

	if err := mon.Register("tool.http", func(context.Context, []byte) ([]byte, error) {
		return []byte("ok"), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	call := referencemonitor.Call{
		RequestID: "req-9", RunID: "run-1", StepID: "s1", ParentStepID: "s0",
		ToolID:     "tool.http",
		Capability: "cap:http.post",
		Resource:   referencemonitor.Resource{Type: "url", Value: "https://api.example.com/orders", Region: "eu"},
		Context:    referencemonitor.CallContext{Taint: "trusted", Reversibility: "reversible", Sensitivity: "confidential"},
		Principal: referencemonitor.Principal{
			NHIID:           "nhi:agent-7",
			DelegationChain: []referencemonitor.DelegationHop{{Sub: "human:alice", ActAs: "nhi:agent-7"}},
		},
	}
	dec, err := mon.Mediate(ctx, call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("esperado permit, veio %s", dec.Effect)
	}

	rec, ok, _ := store.At(ctx, "run-1", 1)
	if !ok {
		t.Fatal("nenhum registo de audit para run-1")
	}
	if rec.Decision != DecisionAllow {
		t.Errorf("decision=%s, esperado allow", rec.Decision)
	}
	if rec.Capability != "cap:http.post" {
		t.Errorf("capability=%q", rec.Capability)
	}
	if rec.PolicyVersion != "2.1.0" {
		t.Errorf("policy_version=%q, esperado 2.1.0", rec.PolicyVersion)
	}
	if rec.Principal.NHIID != "nhi:agent-7" {
		t.Errorf("principal.NHIID=%q", rec.Principal.NHIID)
	}
	if len(rec.Principal.DelegationChain) != 1 || rec.Principal.DelegationChain[0].Sub != "human:alice" {
		t.Errorf("delegation chain nao propagada: %+v", rec.Principal.DelegationChain)
	}

	// Correlação e alvo selados (AOS-011-Q3 / completude schema-fidelity): o registo
	// é atribuível à chamada mediada concreta, não apenas ao run+seq.
	if rec.RunID != "run-1" || rec.StepID != "s1" || rec.ParentStepID != "s0" || rec.RequestID != "req-9" {
		t.Errorf("correlacao nao selada: run=%q step=%q parent=%q req=%q",
			rec.RunID, rec.StepID, rec.ParentStepID, rec.RequestID)
	}
	if rec.ToolID != "tool.http" {
		t.Errorf("tool_id nao selado: %q", rec.ToolID)
	}
	if rec.Resource.Type != "url" || rec.Resource.Value != "https://api.example.com/orders" || rec.Resource.Region != "eu" {
		t.Errorf("resource nao selado: %+v", rec.Resource)
	}
	if rec.Context.Taint != "trusted" || rec.Context.Reversibility != "reversible" || rec.Context.Sensitivity != "confidential" {
		t.Errorf("contexto de decisao nao selado: %+v", rec.Context)
	}

	// A cadeia resultante é íntegra e verificável.
	if err := Verify(ctx, store, "run-1", 1, 1); err != nil {
		t.Fatalf("cadeia de audit da mediacao devia verificar: %v", err)
	}
}

// obligationHook é um hook de teste que permite e impõe obligations, para
// exercitar a selagem de obligations na cadeia (completude schema-fidelity §5).
type obligationHook struct{ obs []referencemonitor.Obligation }

func (obligationHook) Name() string { return "obligations" }

func (h obligationHook) Evaluate(context.Context, *referencemonitor.Call) (referencemonitor.HookResult, error) {
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow, Obligations: h.obs}, nil
}

// TestRMSealsObligations — as obligations impostas pela política entram na cadeia
// tamper-evident (schema §5, obligations), e adulterá-las depois faz o verify falhar.
func TestRMSealsObligations(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	sink := NewMediationSink(store, withSinkClock(func() time.Time { return fixedTime }))
	mon := referencemonitor.New(
		referencemonitor.WithHooks(obligationHook{obs: []referencemonitor.Obligation{
			{Type: "redact_pii", Fields: []string{"email", "phone"}, Params: map[string]string{"ttl_days": "30"}},
		}}),
		referencemonitor.WithEventSink(sink),
	)
	_ = mon.Register("tool.http", func(context.Context, []byte) ([]byte, error) { return nil, nil })

	if _, err := mon.Mediate(ctx, referencemonitor.Call{
		RunID: "run-o", StepID: "s1", ToolID: "tool.http", Capability: "cap:x",
		Principal: referencemonitor.Principal{NHIID: "nhi:a"},
	}); err != nil {
		t.Fatalf("Mediate: %v", err)
	}

	rec, ok, _ := store.At(ctx, "run-o", 1)
	if !ok {
		t.Fatal("sem registo de audit")
	}
	if len(rec.Obligations) != 1 || rec.Obligations[0].Type != "redact_pii" {
		t.Fatalf("obligations nao seladas: %+v", rec.Obligations)
	}
	if len(rec.Obligations[0].Fields) != 2 || rec.Obligations[0].Params["ttl_days"] != "30" {
		t.Fatalf("obligation incompleta: %+v", rec.Obligations[0])
	}

	// Íntegra antes; adulterar as obligations seladas faz o verify falhar.
	if err := Verify(ctx, store, "run-o", 1, 1); err != nil {
		t.Fatalf("cadeia devia verificar: %v", err)
	}
	store.parts["run-o"][0].Obligations[0].Params["ttl_days"] = "9999"
	if err := Verify(ctx, store, "run-o", 1, 1); !errors.Is(err, ErrTampered) {
		t.Fatalf("adulteracao das obligations seladas nao detectada: %v", err)
	}
}

// TestRMDenyReachesAudit — uma negação (fail-closed) também é auditada como deny.
func TestRMDenyReachesAudit(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	mon := newMonitor(t, store, policyHook{name: "policy", decide: referencemonitor.HookDeny, version: "2.1.0"})

	call := referencemonitor.Call{
		RunID: "run-2", StepID: "s1", ToolID: "tool.http", Capability: "cap:http.post",
		Principal: referencemonitor.Principal{NHIID: "nhi:agent-7"},
	}
	dec, _ := mon.Mediate(ctx, call)
	if dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("esperado deny, veio %s", dec.Effect)
	}
	rec, ok, _ := store.At(ctx, "run-2", 1)
	if !ok || rec.Decision != DecisionDeny {
		t.Fatalf("negacao nao auditada como deny: ok=%v rec=%+v", ok, rec)
	}
}

// TestRMAuditThenTamperFails — após a mediação, mutar o registo de audit faz o
// verify falhar (integração ponta-a-ponta do tamper-evidence).
func TestRMAuditThenTamperFails(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	mon := newMonitor(t, store, policyHook{name: "policy", decide: referencemonitor.HookAllow, version: "1.0.0"})
	_ = mon.Register("t", func(context.Context, []byte) ([]byte, error) { return nil, nil })

	for i := 0; i < 3; i++ {
		if _, err := mon.Mediate(ctx, referencemonitor.Call{
			RunID: "run-x", StepID: "s", ToolID: "t", Capability: "cap:x",
			Principal: referencemonitor.Principal{NHIID: "nhi:a"},
		}); err != nil {
			t.Fatalf("Mediate #%d: %v", i, err)
		}
	}
	if err := Verify(ctx, store, "run-x", 1, 3); err != nil {
		t.Fatalf("cadeia devia verificar antes do tampering: %v", err)
	}

	store.parts["run-x"][1].Decision = DecisionDeny // adulterar a decisão registada
	if err := Verify(ctx, store, "run-x", 1, 3); !errors.Is(err, ErrTampered) {
		t.Fatalf("tampering na decisao auditada nao detectado: %v", err)
	}
}

// failStore devolve erro em Append para exercitar o fail-closed de auditoria.
type failStore struct{ Store }

func (failStore) Append(context.Context, AuditRecord) (AuditRecord, error) {
	return AuditRecord{}, errors.New("audit indisponivel")
}

// TestRMFailClosedOnAuditError — se o audit não conseguir gravar num permit, o RM
// degrada a decisão para Deny (uma acção não-auditável não é permitida).
func TestRMFailClosedOnAuditError(t *testing.T) {
	ctx := context.Background()
	mon := newMonitor(t, failStore{NewMemStore()},
		policyHook{name: "policy", decide: referencemonitor.HookAllow})
	_ = mon.Register("t", func(context.Context, []byte) ([]byte, error) {
		t.Fatal("a tool NAO devia ser despachada quando o audit falha")
		return nil, nil
	})

	dec, _ := mon.Mediate(ctx, referencemonitor.Call{
		RunID: "run-z", StepID: "s", ToolID: "t", Capability: "cap:x",
		Principal: referencemonitor.Principal{NHIID: "nhi:a"},
	})
	if dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("esperado deny (fail-closed), veio %s", dec.Effect)
	}
	if dec.Code != referencemonitor.CodeAuditUnavailable {
		t.Fatalf("esperado CodeAuditUnavailable, veio %q", dec.Code)
	}
}

// TestDefaultPartition — RunID vazio cai em "global".
func TestDefaultPartition(t *testing.T) {
	if p := defaultPartition(referencemonitor.MediationRecord{RunID: "r"}); p != "r" {
		t.Fatalf("esperado 'r', veio %q", p)
	}
	if p := defaultPartition(referencemonitor.MediationRecord{}); p != "global" {
		t.Fatalf("esperado 'global', veio %q", p)
	}
}

// TestCustomPartitioner — WithPartitioner permite partição por tenant/board.
func TestCustomPartitioner(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	sink := NewMediationSink(store,
		WithPartitioner(func(referencemonitor.MediationRecord) string { return "tenant-42" }),
		withSinkClock(func() time.Time { return fixedTime }),
	)
	if _, err := sink.RecordMediation(ctx, referencemonitor.MediationRecord{
		RunID: "run-1", Effect: referencemonitor.EffectPermit, Capability: "cap:x",
		Principal: referencemonitor.Principal{NHIID: "nhi:a"},
	}); err != nil {
		t.Fatalf("RecordMediation: %v", err)
	}
	if h, _ := store.Head(ctx, "tenant-42"); h != 1 {
		t.Fatalf("particao custom nao usada: head(tenant-42)=%d", h)
	}
}

// TestEscalateMapping — escalate do RM mapeia para DecisionEscalate.
func TestEscalateMapping(t *testing.T) {
	if d := decisionFor(referencemonitor.EffectEscalate); d != DecisionEscalate {
		t.Fatalf("escalate mal mapeado: %s", d)
	}
}
