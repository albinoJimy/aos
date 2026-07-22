package integration

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// TestNewRevalidationHook_FailClosed confirma que o adaptador recusa qualquer
// colaborador nil (um hook sem revalidador/expectativa/definição/política não pode
// decidir e não deve existir).
func TestNewRevalidationHook_FailClosed(t *testing.T) {
	rv := newRevalidator(t,
		newTrust(t, context.Background(), audit.NewMemStore(), testSigner(t)),
		audit.NewMemStore(), NoopQuarantinerForTest{}, NoopAlerterForTest{})
	frozen := NewRunToolSets()
	current := staticResolver{present: true}
	policy := StaticPolicy{}

	tests := []struct {
		name    string
		rv      *revalidation.Revalidator
		frozen  FrozenProvider
		current CurrentResolver
		policy  PolicyProvider
		want    error
	}{
		{"nil revalidator", nil, frozen, current, policy, ErrNilRevalidator},
		{"nil frozen", rv, nil, current, policy, ErrNilFrozenProvider},
		{"nil current", rv, frozen, nil, policy, ErrNilCurrentResolver},
		{"nil policy", rv, frozen, current, nil, ErrNilPolicyProvider},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRevalidationHook(tc.rv, tc.frozen, tc.current, tc.policy)
			if !errors.Is(err, tc.want) {
				t.Fatalf("erro=%v, esperado %v", err, tc.want)
			}
		})
	}

	if h, err := NewRevalidationHook(rv, frozen, current, policy, WithHookName("custom")); err != nil {
		t.Fatalf("construção válida falhou: %v", err)
	} else if h.Name() != "custom" {
		t.Fatalf("WithHookName ignorado: %q", h.Name())
	}
}

// TestNewSecuredRuntime_FailClosed confirma o fail-closed de cada dependência
// obrigatória do composition root.
func TestNewSecuredRuntime_FailClosed(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()

	base := SecuredConfig{
		Model:       &scriptedModel{},
		Recorder:    agentruntime.NewTurnRecorder(store),
		Catalog:     &fakeCatalog{},
		Revalidator: newRevalidator(t, newTrust(t, context.Background(), audit.NewMemStore(), testSigner(t)), audit.NewMemStore(), NoopQuarantinerForTest{}, NoopAlerterForTest{}),
		Policy:      StaticPolicy{},
		// WORM é o único audit.Store partilhado (RM EventSink + egress). Os restantes
		// colaboradores da cadeia real (Verifier/PDP/Privileged/Authority/EgressResolver)
		// caem para defaults demo-grade fail-closed (hooks REAIS, nunca stubs).
		WORM: audit.NewMemStore(),
	}
	if _, err := NewSecuredRuntime(base); err != nil {
		t.Fatalf("config válida falhou: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SecuredConfig)
		want   error
	}{
		{"nil model", func(c *SecuredConfig) { c.Model = nil }, ErrNoModel},
		{"nil recorder", func(c *SecuredConfig) { c.Recorder = nil }, ErrNoRecorder},
		{"nil catalog", func(c *SecuredConfig) { c.Catalog = nil }, ErrNoCatalog},
		{"nil revalidator", func(c *SecuredConfig) { c.Revalidator = nil }, ErrNoRevalidator},
		{"nil policy", func(c *SecuredConfig) { c.Policy = nil }, ErrNoPolicy},
		{"nil worm", func(c *SecuredConfig) { c.WORM = nil }, ErrNoWORM},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			if _, err := NewSecuredRuntime(cfg); !errors.Is(err, tc.want) {
				t.Fatalf("erro=%v, esperado %v", err, tc.want)
			}
		})
	}
}

// TestRunToolSets_Lifecycle cobre Put/Frozen/Release e o default-deny de um run sem
// entrada.
func TestRunToolSets_Lifecycle(t *testing.T) {
	ctx := context.Background()
	entry := signedEntry(t, testSigner(t), "echo", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	frozen, err := toolset.FreezeToolSet(ctx, &fakeCatalog{entries: []domain.Entry{entry}}, "runX", nil, toolset.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}

	rts := NewRunToolSets()
	if _, ok := rts.Frozen("runX"); ok {
		t.Fatalf("run sem Put não devia estar presente")
	}
	rts.Put(nil) // ignorado
	rts.Put(frozen)
	got, ok := rts.Frozen("runX")
	if !ok || got.RunID() != "runX" {
		t.Fatalf("Frozen após Put: ok=%v runID=%v", ok, got)
	}
	rts.Release("runX")
	if _, ok := rts.Frozen("runX"); ok {
		t.Fatalf("run devia ter sido libertado")
	}
	rts.Release("runX") // idempotente
}

// TestApplyFrozenToGoal materializa o snapshot no goal sem mutar o argumento.
func TestApplyFrozenToGoal(t *testing.T) {
	ctx := context.Background()
	entry := signedEntry(t, testSigner(t), "echo", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	frozen, err := toolset.FreezeToolSet(ctx, &fakeCatalog{entries: []domain.Entry{entry}}, "runY", nil, toolset.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}

	orig := agentruntime.Goal{RunID: "outro", System: "sys", Skills: []agentruntime.ToolSpec{{Name: "s"}}}
	got := ApplyFrozenToGoal(orig, frozen)

	if got.RunID != "runY" {
		t.Fatalf("RunID não alinhado com o snapshot: %s", got.RunID)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "echo" {
		t.Fatalf("Tools não materializados: %+v", got.Tools)
	}
	if got.Skills != nil {
		t.Fatalf("Skills deviam ficar nil (fundidos em Tools): %+v", got.Skills)
	}
	// Não mutou o argumento.
	if orig.RunID != "outro" || len(orig.Skills) != 1 {
		t.Fatalf("ApplyFrozenToGoal mutou o argumento: %+v", orig)
	}
	// frozen nil é no-op.
	if got2 := ApplyFrozenToGoal(orig, nil); got2.RunID != "outro" {
		t.Fatalf("frozen nil devia ser no-op")
	}
}

// TestCatalogResolver_Current cobre os caminhos found/not-found.
func TestCatalogResolver_Current(t *testing.T) {
	ctx := context.Background()
	entry := signedEntry(t, testSigner(t), "echo", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	r := CatalogResolver{Cat: &fakeCatalog{entries: []domain.Entry{entry}}}

	got, ok, err := r.Current(ctx, "echo")
	if err != nil || !ok || got.ID != "echo" {
		t.Fatalf("Current(echo): got=%v ok=%v err=%v", got.ID, ok, err)
	}
	_, ok, err = r.Current(ctx, "ausente")
	if err != nil || ok {
		t.Fatalf("Current(ausente): ok=%v err=%v (esperado false, nil)", ok, err)
	}
}

// TestProvenanceQuarantiner_Admits confirma que a identidade divergente é isolada
// como memória untrusted (a barreira estrutural de AOS-042).
func TestProvenanceQuarantiner_Admits(t *testing.T) {
	q := NewProvenanceQuarantiner(nil, WithQuarantineClock(fixedClock()),
		WithQuarantineAgentID("agent"), WithQuarantineRunID("run"))
	if err := q.Quarantine(context.Background(), revalidation.Artifact{
		ID: "echo", Version: "1.0.0", Digest: "sha256:dead", Reason: revalidation.ReasonDigestMismatch,
	}); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	items := q.Partition().Quarantine().Items()
	if len(items) != 1 {
		t.Fatalf("esperava 1 item em quarentena, tenho %d", len(items))
	}
	// O item é servido como DADO taint-marcado (untrusted) — nunca control-plane.
	if string(items[0].Taint()) != "untrusted" {
		t.Fatalf("item de quarentena não está marcado untrusted: %v", items[0].Taint())
	}
}

// --- fakes/adaptadores locais para os testes unitários ---------------------

// NoopQuarantinerForTest satisfaz [revalidation.Quarantiner] sem efeito.
type NoopQuarantinerForTest struct{}

func (NoopQuarantinerForTest) Quarantine(context.Context, revalidation.Artifact) error { return nil }

// NoopAlerterForTest satisfaz [revalidation.Alerter] sem efeito.
type NoopAlerterForTest struct{}

func (NoopAlerterForTest) Alert(context.Context, revalidation.Alert) {}
