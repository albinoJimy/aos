package network

import (
	"context"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// TestEmbeddedResolver cobre a impl de referência: carrega a allowlist embutida e
// resolve-a para qualquer principal (o escopo é aplicado por Evaluate).
func TestEmbeddedResolver(t *testing.T) {
	r, err := NewEmbeddedResolver()
	if err != nil {
		t.Fatalf("NewEmbeddedResolver: %v", err)
	}
	p, err := r.Resolve(context.Background(), principalClass("web-fetcher"))
	if err != nil || p == nil {
		t.Fatalf("Resolve: %v, policy=%v", err, p)
	}
	if r.Policy() == nil {
		t.Fatal("Policy() não deveria ser nil")
	}
}

// TestMapResolver_PorPrincipal demonstra a PORTA de resolução por principal: um
// resolver que devolve policies distintas por principal e nil (fail-closed) para um
// principal sem allowlist. É o molde por onde o PDP real se liga.
func TestMapResolver_PorPrincipal(t *testing.T) {
	pA, _ := Parse([]byte(`{"version":"vA","default":"deny","rules":[
		{"id":"a","principals":["class:a"],"destinations":[{"hosts":["a.io"],"ports":[443]}]}]}`))
	byPrincipal := map[string]*Policy{"a": pA}
	resolver := ResolverFunc(func(_ context.Context, pr referencemonitor.Principal) (*Policy, error) {
		return byPrincipal[pr.AgentClass], nil // ausente ⇒ nil ⇒ fail-closed
	})
	f, err := NewEgressFilter(resolver, WithSecurityAuditSink(&recordingSink{}), withClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewEgressFilter: %v", err)
	}
	// Principal 'a' com allowlist própria: permitido.
	if dec, _ := f.Decide(context.Background(), principalClass("a"), NewDestination("a.io", 443)); !dec.Allow {
		t.Fatal("principal 'a' deveria alcançar a.io:443")
	}
	// Principal 'b' sem allowlist resolúvel: negado (fail-closed).
	if dec, _ := f.Decide(context.Background(), principalClass("b"), NewDestination("a.io", 443)); dec.Allow {
		t.Fatal("principal 'b' sem allowlist deveria ser negado (fail-closed)")
	}
}

// TestNewEgressFilter_NilResolver cobre a recusa fail-closed de construção sem
// resolver.
func TestNewEgressFilter_NilResolver(t *testing.T) {
	if _, err := NewEgressFilter(nil); err != ErrNilResolver {
		t.Fatalf("NewEgressFilter(nil) err = %v, quero ErrNilResolver", err)
	}
}

// TestNewEgressFilter_NilSink cobre a recusa fail-closed de construção sem sink WORM:
// um filtro de enforcement SEM audit é impossível de compor (um bloqueio nunca fica
// por selar silenciosamente). Com resolver mas sem [WithSecurityAuditSink] a
// construção recusa ([ErrNilSink]).
func TestNewEgressFilter_NilSink(t *testing.T) {
	resolver, err := NewEmbeddedResolver()
	if err != nil {
		t.Fatalf("NewEmbeddedResolver: %v", err)
	}
	if _, err := NewEgressFilter(resolver); err != ErrNilSink {
		t.Fatalf("NewEgressFilter sem sink err = %v, quero ErrNilSink", err)
	}
	// Com sink, a construção passa.
	if _, err := NewEgressFilter(resolver, WithSecurityAuditSink(&recordingSink{})); err != nil {
		t.Fatalf("NewEgressFilter com sink: %v", err)
	}
}
