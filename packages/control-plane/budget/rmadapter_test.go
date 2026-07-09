package budget

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// call constrói um Call mínimo com run/step e input dado.
func call(run, step string, input []byte) rm.Call {
	return rm.Call{
		RunID: run, StepID: step, ToolID: "tool.spawn", Capability: "cap:spawn",
		Principal: rm.Principal{NHIID: "nhi-1", AgentClass: "agent-worker"},
		Input:     input,
	}
}

// fixedCost é um estimador determinístico para os testes.
func fixedCost(a Amount) CostFunc { return func(*rm.Call) Amount { return a } }

// denyHook nega sempre — colocado APÓS o budget para forçar um deny global
// depois de o budget já ter reservado (exercita o release-on-deny do consumidor).
type denyHook struct{}

func (denyHook) Name() string { return "deny-after-budget" }
func (denyHook) Evaluate(context.Context, *rm.Call) (rm.HookResult, error) {
	return rm.HookResult{Decision: rm.HookDeny, Reason: "negado a jusante"}, nil
}

// buildRM compõe o RM com o BudgetCheck no lugar canónico do hook budget.
func buildRM(store eventstore.EventStore, bc *BudgetCheck, tail ...rm.Hook) *rm.Monitor {
	hooks := []rm.Hook{rm.IdentityStub{}, rm.PolicyStub{}, bc}
	hooks = append(hooks, tail...)
	return rm.New(rm.WithHooks(hooks...), rm.WithEventSink(rm.NewEventStoreSink(store)))
}

// TestBudgetCheck_DenyNoHeadroom_FailClosedAndAudit: o BudgetCheck nega um Call
// sem headroom (fail-closed) e o RM AUDITA a negação no Event Store (AOS-002).
func TestBudgetCheck_DenyNoHeadroom_FailClosedAndAudit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Árvore com headroom minúsculo; a estimativa (10 tokens) não cabe.
	b, _ := New("run-poor", amt(1, 1))
	bc := NewBudgetCheck(b, WithEstimator(fixedCost(amt(10, 10))))
	m := buildRM(store, bc, rm.EgressStub{}, rm.AuditStub{})
	_ = m.Register("tool.spawn", func(_ context.Context, in []byte) ([]byte, error) { return in, nil })

	d, err := m.Mediate(ctx, call("run-poor", "s1", []byte("payload")))
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny {
		t.Fatalf("Effect = %q, quero deny", d.Effect)
	}
	if d.DeniedBy != "budget" {
		t.Errorf("DeniedBy = %q, quero budget", d.DeniedBy)
	}
	// Nada ficou reservado (o Reserve interno falhou → sem leak).
	if got := b.Snapshot()["run-poor"].Reserved; got != (Amount{}) {
		t.Errorf("reservado = %v, quero 0 apos deny", got)
	}
	// A negação foi auditada de forma durável.
	events, err := store.Read(ctx, "run-poor", 1)
	if err != nil {
		t.Fatalf("Read audit: %v", err)
	}
	if len(events) != 1 || events[0].Type != rm.EventTypeDenied {
		t.Fatalf("esperava 1 evento denied auditado, obtive %d (%v)", len(events), events)
	}
	var pl struct {
		Decision string `json:"decision"`
		DeniedBy string `json:"denied_by"`
	}
	if err := json.Unmarshal(events[0].Payload, &pl); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if pl.Decision != "deny" || pl.DeniedBy != "budget" {
		t.Errorf("audit payload = %+v, quero deny/budget", pl)
	}
}

// TestBudgetCheck_CommitOnPermit: num permit, o consumidor confirma a reserva
// (Settle) e o débito torna-se final (reserved→committed), sem leak.
func TestBudgetCheck_CommitOnPermit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	b, _ := New("run-ok", amt(1000, 1000))
	bc := NewBudgetCheck(b, WithEstimator(fixedCost(amt(100, 50))))
	m := buildRM(store, bc, rm.EgressStub{}, rm.AuditStub{})

	var dispatched bool
	_ = m.Register("tool.spawn", func(_ context.Context, in []byte) ([]byte, error) {
		dispatched = true
		return in, nil
	})

	c := call("run-ok", "s1", []byte("x"))
	d, err := m.Mediate(ctx, c)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if !d.Permitted() {
		t.Fatalf("esperava permit, obtive %q (%s)", d.Effect, d.Reason)
	}
	if !dispatched {
		t.Error("a tool devia ter sido despachada")
	}
	// Enquanto pendente, o headroom está RESERVADO (ainda não committed).
	if got := b.Snapshot()["run-ok"].Reserved; got != amt(100, 50) {
		t.Fatalf("reservado pendente = %v, quero {100,50}", got)
	}
	// Consumidor confirma consoante a Decision (commit-em-permit).
	if err := bc.Settle(ctx, &c, d); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	snap := b.Snapshot()["run-ok"]
	if snap.Reserved != (Amount{}) || snap.Committed != amt(100, 50) {
		t.Errorf("apos commit = {res:%v com:%v}, quero {res:0 com:{100,50}}", snap.Reserved, snap.Committed)
	}
}

// TestBudgetCheck_ReleaseOnDeny: se um hook A JUSANTE do budget nega, a reserva
// já feita pelo budget é libertada pelo consumidor (release-em-deny), sem leak.
func TestBudgetCheck_ReleaseOnDeny(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	b, _ := New("run-deny", amt(1000, 1000))
	bc := NewBudgetCheck(b, WithEstimator(fixedCost(amt(100, 50))))
	// denyHook a seguir ao budget força deny global depois da reserva.
	m := buildRM(store, bc, denyHook{})
	_ = m.Register("tool.spawn", func(_ context.Context, in []byte) ([]byte, error) { return in, nil })

	c := call("run-deny", "s1", []byte("x"))
	d, err := m.Mediate(ctx, c)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny || d.DeniedBy != "deny-after-budget" {
		t.Fatalf("esperava deny por deny-after-budget, obtive %q/%q", d.Effect, d.DeniedBy)
	}
	// A reserva do budget existe (pendente) e é libertada pelo consumidor.
	if got := b.Snapshot()["run-deny"].Reserved; got != amt(100, 50) {
		t.Fatalf("reservado pendente = %v, quero {100,50}", got)
	}
	if err := bc.Settle(ctx, &c, d); err != nil {
		t.Fatalf("Settle (release): %v", err)
	}
	// Sem leak: headroom recuperado.
	snap := b.Snapshot()["run-deny"]
	if snap.Reserved != (Amount{}) || snap.Committed != (Amount{}) {
		t.Errorf("leak apos release: {res:%v com:%v}", snap.Reserved, snap.Committed)
	}
}

// TestBudgetCheck_CircuitBreakerTrip: um Call cuja estimativa excede o trip é
// negado de imediato, sem sequer reservar (integração leve, EPIC-08).
func TestBudgetCheck_CircuitBreakerTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, _ := New("run-cb", amt(1_000_000, 1_000_000))
	bc := NewBudgetCheck(b,
		WithEstimator(fixedCost(amt(5000, 10))), // 5000 tokens
		WithCircuitBreaker(amt(1000, 1000)),     // trip a 1000 tokens
	)
	res, err := bc.Evaluate(ctx, ptr(call("run-cb", "s1", nil)))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Decision != rm.HookDeny {
		t.Fatalf("Decision = %v, quero HookDeny (trip)", res.Decision)
	}
	// Não reservou nada (o breaker curto-circuitou antes do Reserve).
	if got := b.Snapshot()["run-cb"].Reserved; got != (Amount{}) {
		t.Errorf("reservado = %v, quero 0 (breaker nao reserva)", got)
	}
}

// TestBudgetCheck_NilBackendFailClosed: sem backend, todo o Evaluate nega.
func TestBudgetCheck_NilBackendFailClosed(t *testing.T) {
	t.Parallel()
	bc := NewBudgetCheck(nil)
	res, err := bc.Evaluate(context.Background(), ptr(call("r", "s", nil)))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Decision != rm.HookDeny {
		t.Errorf("Decision = %v, quero HookDeny (backend nil)", res.Decision)
	}
}

// TestBudgetCheck_SettleNoPendingIsNoop: Commit/Release sem reserva pendente
// (ex.: o budget negou) são no-op seguros.
func TestBudgetCheck_SettleNoPendingIsNoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, _ := New("run-np", amt(100, 100))
	bc := NewBudgetCheck(b)
	c := call("run-np", "s1", nil)
	if err := bc.Commit(ctx, &c); err != nil {
		t.Errorf("Commit sem pendente = %v, quero nil", err)
	}
	if err := bc.Release(ctx, &c); err != nil {
		t.Errorf("Release sem pendente = %v, quero nil", err)
	}
}

// TestDefaultEstimator: deriva tokens do tamanho do input a uma tarifa fixa.
func TestDefaultEstimator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input []byte
		want  Amount
	}{
		{nil, amt(1, 10)},                // 0/4+1 = 1 token
		{[]byte("12345678"), amt(3, 30)}, // 8/4+1 = 3 tokens
	}
	for _, tc := range tests {
		c := call("r", "s", tc.input)
		if got := DefaultEstimator(&c); got != tc.want {
			t.Errorf("DefaultEstimator(%q) = %v, quero %v", tc.input, got, tc.want)
		}
	}
}

func ptr(c rm.Call) *rm.Call { return &c }

// TestBudgetCheck_NodeSelector: o mapeamento Call→nó de orçamento é configurável
// (ex.: debitar uma sub-árvore em vez da raiz do run).
func TestBudgetCheck_NodeSelector(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, _ := New("tree-sel", amt(1000, 1000))
	if err := b.AddNode("sub", "tree-sel", amt(1000, 1000)); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if b.TreeID() != "tree-sel" {
		t.Errorf("TreeID = %q, quero tree-sel", b.TreeID())
	}
	bc := NewBudgetCheck(b,
		WithEstimator(fixedCost(amt(10, 10))),
		WithNodeSelector(func(*rm.Call) string { return "sub" }),
	)
	res, err := bc.Evaluate(ctx, ptr(call("run-x", "s1", nil)))
	if err != nil || res.Decision != rm.HookAllow {
		t.Fatalf("Evaluate = %v/%v, quero allow", res.Decision, err)
	}
	// O débito recaiu na sub-árvore seleccionada (e no ancestral raiz).
	snap := b.Snapshot()
	if snap["sub"].Reserved != amt(10, 10) || snap["tree-sel"].Reserved != amt(10, 10) {
		t.Errorf("reservas = sub:%v raiz:%v, quero {10,10} em ambas", snap["sub"].Reserved, snap["tree-sel"].Reserved)
	}
}

// Confirma que os erros do budget são comparáveis por errors.Is (contrato).
func TestErrorsAreSentinels(t *testing.T) {
	t.Parallel()
	if !errors.Is(ErrNoHeadroom, ErrNoHeadroom) {
		t.Error("ErrNoHeadroom nao e comparavel consigo mesmo")
	}
}
