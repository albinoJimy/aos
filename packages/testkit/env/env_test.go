package env_test

import (
	"context"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	tk "github.com/aos-ref/testkit"
	"github.com/aos-ref/testkit/env"
)

// TestNew_DeclarativeProvisioning cobre AC1: um teste DECLARA as deps efémeras
// que precisa por options e recebe-as provisionadas. Aqui pede as quatro deps
// core e confirma que todas chegam prontas a usar.
func TestNew_DeclarativeProvisioning(t *testing.T) {
	t.Parallel()
	e := env.New(t,
		env.WithEventStore(),
		env.WithBus(),
		env.WithPDP(),
		env.WithVault(),
	)
	if e.EventStore == nil {
		t.Fatal("EventStore nao provisionado")
	}
	if e.Bus == nil {
		t.Fatal("Bus nao provisionado")
	}
	if e.PDP == nil {
		t.Fatal("PDP nao provisionado")
	}
	if e.Vault == nil {
		t.Fatal("Vault nao provisionado")
	}
	// As deps estao funcionais: um append flui pelo transporte push (TAP do bus).
	res, err := e.EventStore.Append(context.Background(), "run-x", eventstore.EventInput{
		Type: "turn.recorded", RunID: "run-x", StepID: "step-000001",
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if res.Status != eventstore.StatusCommitted {
		t.Fatalf("esperava committed, obtive %s", res.Status)
	}
	// PDP determinista permite por omissao.
	dec, err := e.PDP.Decide(context.Background(), tk.PolicyInput{Capability: "cap:x"})
	if err != nil {
		t.Fatalf("PDP.Decide: %v", err)
	}
	if !dec.Permitted() {
		t.Fatal("FakePDP devia permitir por omissao")
	}
}

// TestNew_DefaultProvisionsCore confirma que sem opcoes o Env provisiona o
// conjunto CORE (ES + Bus + PDP + Vault) — o caso comum de uma suite de
// integracao — e NAO o broker (opt-in).
func TestNew_DefaultProvisionsCore(t *testing.T) {
	t.Parallel()
	e := env.New(t)
	if e.EventStore == nil || e.Bus == nil || e.PDP == nil || e.Vault == nil {
		t.Fatalf("default devia provisionar ES+Bus+PDP+Vault: %+v", e)
	}
	if e.Broker != nil {
		t.Fatal("Broker deve ser opt-in (WithBroker), nao no default")
	}
}

// TestNew_SelectiveProvisioning confirma que so as deps pedidas sao
// provisionadas — as restantes ficam nil (declaratividade estrita, AC1).
func TestNew_SelectiveProvisioning(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithPDP())
	if e.PDP == nil {
		t.Fatal("PDP pedido mas nil")
	}
	if e.EventStore != nil || e.Bus != nil || e.Vault != nil || e.Broker != nil {
		t.Fatal("deps nao pedidas deviam ficar nil")
	}
}

// TestNew_BusImpliesEventStore confirma que WithBus activa o Event Store
// implicitamente (o bus e servido pelas subscricoes do Store).
func TestNew_BusImpliesEventStore(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithBus())
	if e.EventStore == nil {
		t.Fatal("WithBus devia activar o Event Store implicitamente")
	}
	if e.Bus == nil {
		t.Fatal("Bus nao provisionado")
	}
}

// TestNew_WithBrokerOptIn confirma o opt-in do Credential Broker.
func TestNew_WithBrokerOptIn(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithBroker())
	if e.Broker == nil {
		t.Fatal("Broker pedido mas nil")
	}
}

// TestNew_WithEventStoreOptions confirma que as eventstore.Option adicionais sao
// propagadas (ex.: numero de replicas).
func TestNew_WithEventStoreOptions(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithEventStore(eventstore.WithReplicas(5), eventstore.WithQuorum(3)))
	if e.EventStore == nil {
		t.Fatal("EventStore nao provisionado")
	}
	// Um append tem de funcionar com o quorum configurado.
	if _, err := e.EventStore.Append(context.Background(), "s", eventstore.EventInput{
		Type: "t", RunID: "r", StepID: "step-1",
	}); err != nil {
		t.Fatalf("append com replicas=5 quorum=3: %v", err)
	}
}
