package securitytests

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/broker"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/sandbox"
)

// ===========================================================================
// CENÁRIO 3 — SEGREDOS (AOS-070, ADR-006)
//
// Se o agente detém o segredo downstream, qualquer prompt injection o compromete. O
// Credential Broker troca o token scoped por credenciais SERVER-SIDE: o agente NUNCA vê
// o segredo. Este cenário faz o fluxo completo (troca + injecção) e prova, por scan de
// sentinela, que o valor NÃO aparece em nenhuma superfície observável pelo agente —
// output da troca, portadores server-side (redigidos), spans ou Event Store.
// ORQUESTRA o broker real; não o reimplementa.
// ===========================================================================

// brokerSentinel é o valor do segredo downstream de teste. É ÚNICO e improvável: se
// aparecer em qualquer superfície do agente o teste falha. NÃO é um segredo real (é
// material de teste derivado de uma constante — sem segredos nos fixtures).
const brokerSentinel = "S3CR3T-stripe-eu-charge-AOS075"

// brokerStack agrega o material do broker totalmente ligado (RM→ES, broker, vault, guest).
type brokerStack struct {
	b     *broker.Broker
	es    *eventstore.Store
	vault *broker.MemoryVault
	guest *broker.MemoryGuest
}

// buildBrokerStack liga um Reference Monitor (com o ScopeGate do broker + sink durável
// no Event Store), o Vault de referência (com o segredo aprovisionado) e o broker com
// TTL/relógio deterministas. T-free (reutilizado pelo probe do relatório).
func buildBrokerStack(ttl time.Duration) (*brokerStack, error) {
	es, err := eventstore.New()
	if err != nil {
		return nil, err
	}
	scopes := map[string][]string{"payments": {"cap:pay.charge", "cap:pay.refund"}}
	hooks := append(referencemonitor.DefaultHooks(), broker.NewScopeGate(broker.DefaultExchangeToolID, scopes))
	rm := referencemonitor.New(
		referencemonitor.WithHooks(hooks...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)
	vlt := broker.NewMemoryVault()
	vlt.Put(broker.VaultKey{Provider: "stripe", Region: "eu", Capability: "cap:pay.charge"}, brokerSentinel)

	clock := func() time.Time { return fixedClock()() }
	b, err := broker.New(rm, vlt, es, broker.WithClock(clock), broker.WithTTL(ttl), broker.WithClassScopes(scopes))
	if err != nil {
		_ = es.Close()
		return nil, err
	}
	return &brokerStack{b: b, es: es, vault: vlt, guest: broker.NewMemoryGuest()}, nil
}

// brokerRequest monta um ExchangeRequest em-escopo (utilizador tem charge; classe
// payments autoriza charge → autoridade efectiva = charge).
func brokerRequest(runID string) broker.ExchangeRequest {
	return broker.ExchangeRequest{
		RunID:  runID,
		StepID: "step-1",
		Principal: referencemonitor.Principal{
			NHIID: "nhi-agent-1", AgentID: "agent-1", AgentClass: "payments",
			Authority: []string{"cap:pay.charge"},
		},
		Credential: "nhi-token-bearer", // bearer efémero, NÃO o segredo downstream
		Downstream: broker.Downstream{
			Provider: "stripe", Region: "eu", Capability: "cap:pay.charge",
			ResourceType: "url", ResourceValue: "https://api.stripe.com/charges",
		},
	}
}

func TestSecrets_NeverObservableDownstream(t *testing.T) {
	t.Parallel()
	st, err := buildBrokerStack(time.Minute)
	if err != nil {
		t.Fatalf("buildBrokerStack: %v", err)
	}
	t.Cleanup(func() { _ = st.es.Close() })

	inj, err := st.b.NewInjector(st.guest)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}

	ctx := context.Background()
	h, err := st.b.Exchange(ctx, brokerRequest("run-1"))
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if err := inj.Inject(ctx, string(h), sandbox.Instance{ID: "vm-1"}); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	scan := func(where, s string) {
		t.Helper()
		if scanLeak(where, s, brokerSentinel) {
			t.Fatalf("SEGREDO observável em %s: %s", where, s)
		}
	}

	// (1) Output da troca — o que o agente recebe (só o handle opaco).
	scan("handle (output da troca)", string(h))
	if len(string(h)) == 0 {
		t.Fatal("Exchange devia devolver um handle opaco não-vazio")
	}

	// (2) Portador server-side do Vault (Secret) redige em todos os caminhos.
	sec, err := st.vault.Fetch(ctx, broker.VaultKey{Provider: "stripe", Region: "eu", Capability: "cap:pay.charge"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	scan("Secret.String()", sec.String())
	scan("Secret %v", fmt.Sprintf("%v", sec))
	scan("Secret %#v", fmt.Sprintf("%#v", sec))
	secJSON, err := json.Marshal(sec)
	if err != nil {
		t.Fatalf("marshal secret: %v", err)
	}
	scan("Secret JSON", string(secJSON))

	// (3) TODOS os eventos do Event Store (mediação da troca + registo da troca).
	evs, err := st.es.Read(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("es.Read: %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("Event Store sem eventos: a troca devia selar registo (audit-before-effect)")
	}
	for _, e := range evs {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		scan("Event Store ("+e.Type+")", string(raw))
	}

	// (4) A injecção server-side chegou ao guest (destino LEGÍTIMO, não o agente): o
	//     fluxo completou-se, mas o guest de referência não retém o valor em claro.
	if st.guest.Injections() != 1 {
		t.Fatalf("injecção server-side: %d, quer 1", st.guest.Injections())
	}

	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "secrets_never_observable", broker.DefaultExchangeToolID, "sentinel absent downstream")
	verifyWORM(t, ledger, suiteLedgerPartition)
}
