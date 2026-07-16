package broker

import (
	"context"
	"sync"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// sentinel é o valor do segredo downstream usado nos testes. É ÚNICO e
// improvável para o scan de fuga: se aparecer em qualquer superfície do agente
// (output, logs, spans, Event Store) o teste falha.
const sentinel = "S3CR3T-stripe-eu-charge-9f3a"

// Chaves/identidades canónicas dos testes (todas NÃO-SECRETAS).
const (
	provInScopeCap = "cap:pay.charge"
	classScopedCap = "cap:pay.refund"
	unknownCap     = "cap:admin.delete"
	agentClass     = "payments"
	nhiID          = "nhi-agent-1"
	provider       = "stripe"
	region         = "eu"
	resourceURL    = "https://api.stripe.com/charges"
)

// testClock é um relógio determinista e concorrente-seguro para o TTL.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// defaultClassScopes: a classe "payments" autoriza charge+refund; o utilizador
// canónico só tem charge → a autoridade efectiva (∩) é apenas charge.
func defaultClassScopes() map[string][]string {
	return map[string][]string{
		agentClass: {provInScopeCap, classScopedCap},
	}
}

// stack agrega o material de teste totalmente ligado (RM ⟶ ES, broker, vault).
type stack struct {
	broker *Broker
	rm     *referencemonitor.Monitor
	es     *eventstore.Store
	vault  *MemoryVault
	guest  *MemoryGuest
	clock  *testClock
}

// newStack liga um Reference Monitor (com o ScopeGate do broker + sink durável no
// Event Store), o Vault de referência (com o segredo aprovisionado) e o broker
// com TTL/relógio deterministas.
func newStack(t *testing.T, ttl time.Duration) *stack {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	scopes := defaultClassScopes()
	hooks := append(referencemonitor.DefaultHooks(), NewScopeGate(DefaultExchangeToolID, scopes))
	rm := referencemonitor.New(
		referencemonitor.WithHooks(hooks...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)

	vlt := NewMemoryVault()
	vlt.Put(VaultKey{Provider: provider, Region: region, Capability: provInScopeCap}, sentinel)

	clock := newTestClock()
	b, err := New(rm, vlt, es,
		WithClock(clock.now),
		WithTTL(ttl),
		WithClassScopes(scopes),
	)
	if err != nil {
		t.Fatalf("broker.New: %v", err)
	}
	return &stack{broker: b, rm: rm, es: es, vault: vlt, guest: NewMemoryGuest(), clock: clock}
}

// principal devolve o principal canónico com a autoridade de UTILIZADOR dada.
func principal(userAuthority ...string) referencemonitor.Principal {
	return referencemonitor.Principal{
		NHIID:      nhiID,
		AgentID:    "agent-1",
		AgentClass: agentClass,
		Authority:  userAuthority,
	}
}

// request monta um ExchangeRequest para a capability dada.
func request(runID, capability string) ExchangeRequest {
	return ExchangeRequest{
		RunID:      runID,
		StepID:     "step-1",
		Principal:  principal(provInScopeCap), // utilizador tem charge
		Credential: "nhi-token-bearer",        // bearer efémero, NÃO o segredo downstream
		Downstream: Downstream{
			Provider:      provider,
			Region:        region,
			Capability:    capability,
			ResourceType:  "url",
			ResourceValue: resourceURL,
		},
	}
}

// readStream lê todos os eventos committed de um stream.
func readStream(t *testing.T, es *eventstore.Store, stream string) []eventstore.Event {
	t.Helper()
	evs, err := es.Read(context.Background(), stream, 1)
	if err != nil {
		t.Fatalf("es.Read(%q): %v", stream, err)
	}
	return evs
}
