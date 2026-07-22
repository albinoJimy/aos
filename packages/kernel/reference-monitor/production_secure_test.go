package referencemonitor_test

import (
	"context"
	"errors"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
)

// fakeIdentityHook / fakeEgressHook são hooks REAIS (não-stub) que ocupam os slots
// "identity"/"egress" nos guard-tests de [NewProductionSecure]. Não fazem enforcement
// (permitem) — o que se testa é a GUARDA DE CONSTRUÇÃO (rejeição dos stubs neutros),
// não o comportamento de mediação; um hook real qualquer que não seja o stub basta.
type fakeIdentityHook struct{}

func (fakeIdentityHook) Name() string { return "identity" }
func (fakeIdentityHook) Evaluate(context.Context, *referencemonitor.Call) (referencemonitor.HookResult, error) {
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

type fakeEgressHook struct{}

func (fakeEgressHook) Name() string { return "egress" }
func (fakeEgressHook) Evaluate(context.Context, *referencemonitor.Call) (referencemonitor.HookResult, error) {
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// realChain devolve a cadeia canónica de produção com hooks REAIS nos slots de
// identidade e egress e um ScopeGate com autoridade — a base dos guard-tests, à qual
// cada teste retira UMA garantia para provar a recusa correspondente.
func realChain(priv referencemonitor.PrivilegedAuthorizer) []referencemonitor.Hook {
	return []referencemonitor.Hook{
		fakeIdentityHook{},
		referencemonitor.PolicyStub{},
		referencemonitor.NewTaintGate(priv),
		referencemonitor.NewScopeGate(authz.NewStaticAuthoritySource()),
		referencemonitor.BudgetStub{},
		fakeEgressHook{},
		referencemonitor.AuditStub{},
	}
}

// TestNewProductionSecureRejectsIdentityStub: a via ESTRITA recusa a cadeia default de
// [NewProduction] (que ainda embarca o IdentityStub neutro) — a identidade forjável de
// AOS-005 não passa a guarda.
func TestNewProductionSecureRejectsIdentityStub(t *testing.T) {
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)
	// Sem WithHooks ⇒ base = DefaultHooksWithTaint, que contém o IdentityStub (e o
	// EgressStub). A guarda dispara no primeiro: identidade.
	m, err := referencemonitor.NewProductionSecure(priv, referencemonitor.WithEventSink(&spySink{}))
	if !errors.Is(err, referencemonitor.ErrIdentityStub) {
		t.Fatalf("erro=%v want ErrIdentityStub", err)
	}
	if m != nil {
		t.Errorf("Monitor devia ser nil com IdentityStub na cadeia")
	}
}

// TestNewProductionSecureRejectsEgressStub: identidade real mas EgressStub ⇒ o egress
// default-deny (AOS-067) está inerte; recusado.
func TestNewProductionSecureRejectsEgressStub(t *testing.T) {
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)
	chain := realChain(priv)
	// Substitui o egress real pelo stub neutro.
	chain[5] = referencemonitor.EgressStub{}
	m, err := referencemonitor.NewProductionSecure(priv,
		referencemonitor.WithEventSink(&spySink{}),
		referencemonitor.WithHooks(chain...),
	)
	if !errors.Is(err, referencemonitor.ErrEgressStub) {
		t.Fatalf("erro=%v want ErrEgressStub", err)
	}
	if m != nil {
		t.Errorf("Monitor devia ser nil com EgressStub na cadeia")
	}
}

// TestNewProductionSecureRejectsMissingScopeGate: identidade e egress reais mas sem
// ScopeGate activo ⇒ o tecto de autoridade user∩classe (AOS-071) não é imposto; recusado.
func TestNewProductionSecureRejectsMissingScopeGate(t *testing.T) {
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)
	// Cadeia real SEM o ScopeGate.
	chain := []referencemonitor.Hook{
		fakeIdentityHook{},
		referencemonitor.PolicyStub{},
		referencemonitor.NewTaintGate(priv),
		referencemonitor.BudgetStub{},
		fakeEgressHook{},
		referencemonitor.AuditStub{},
	}
	m, err := referencemonitor.NewProductionSecure(priv,
		referencemonitor.WithEventSink(&spySink{}),
		referencemonitor.WithHooks(chain...),
	)
	if !errors.Is(err, referencemonitor.ErrScopeGateMissing) {
		t.Fatalf("erro=%v want ErrScopeGateMissing", err)
	}
	if m != nil {
		t.Errorf("Monitor devia ser nil sem ScopeGate activo")
	}
}

// TestNewProductionSecureRejectsScopeGateWithoutAuthority: um ScopeGate com
// AuthoritySource nil é um no-op (autoridade vazia) e NÃO conta como enforcement — a
// guarda é de gate ACTIVO, não só de tipo presente.
func TestNewProductionSecureRejectsScopeGateWithoutAuthority(t *testing.T) {
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)
	chain := realChain(priv)
	// ScopeGate sem fonte de autoridade (no-op).
	chain[3] = referencemonitor.NewScopeGate(nil)
	m, err := referencemonitor.NewProductionSecure(priv,
		referencemonitor.WithEventSink(&spySink{}),
		referencemonitor.WithHooks(chain...),
	)
	if !errors.Is(err, referencemonitor.ErrScopeGateMissing) {
		t.Fatalf("erro=%v want ErrScopeGateMissing (ScopeGate no-op não conta)", err)
	}
	if m != nil {
		t.Errorf("Monitor devia ser nil com ScopeGate sem autoridade")
	}
}

// TestNewProductionSecureInheritsProductionGuards: a via estrita é um SUPERSET —
// herda as recusas de [NewProduction] (privileged nil, sem audit durável).
func TestNewProductionSecureInheritsProductionGuards(t *testing.T) {
	// privileged nil ⇒ ErrNoPrivilegedAuthorizer (herdado).
	if _, err := referencemonitor.NewProductionSecure(nil, referencemonitor.WithEventSink(&spySink{})); !errors.Is(err, referencemonitor.ErrNoPrivilegedAuthorizer) {
		t.Fatalf("privileged nil: erro=%v want ErrNoPrivilegedAuthorizer", err)
	}
	// Sem WithEventSink ⇒ ErrNoDurableAudit (herdado; dispara antes das guardas novas).
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)
	if _, err := referencemonitor.NewProductionSecure(priv, referencemonitor.WithHooks(realChain(priv)...)); !errors.Is(err, referencemonitor.ErrNoDurableAudit) {
		t.Fatalf("sem audit durável: erro=%v want ErrNoDurableAudit", err)
	}
}

// TestNewProductionSecureAcceptsRealChain: a cadeia com identidade real, egress real e
// ScopeGate com autoridade — mais TaintGate activo e audit durável — é ACEITE.
func TestNewProductionSecureAcceptsRealChain(t *testing.T) {
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)
	m, err := referencemonitor.NewProductionSecure(priv,
		referencemonitor.WithEventSink(&spySink{}),
		referencemonitor.WithHooks(realChain(priv)...),
	)
	if err != nil {
		t.Fatalf("cadeia real válida recusada: %v", err)
	}
	if m == nil {
		t.Fatal("Monitor não devia ser nil numa construção válida")
	}
}
