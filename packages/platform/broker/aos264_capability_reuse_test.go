package broker

import (
	"context"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// signedClassScopes espelha o bundle ASSINADO do PDP
// (control-plane/pdp/policies/capabilities/allowlist.json): agent-worker tem
// cap:http.post + cap:fs.read; agent-reader só cap:fs.read. É a fonte da verdade
// que este teste amarra ao ScopeGate — se o bundle mudar, o reuso tem de ser
// re-verificado.
func signedClassScopes() map[string][]string {
	return map[string][]string{
		ClassAgentWorker: {ExchangeCapabilityHTTPPost, "cap:fs.read"},
		"agent-reader":   {"cap:fs.read"},
	}
}

// TestAOS264_ReutilizacaoCapHTTPPost prova a DECISÃO registada em capability.go: a
// troca declarada sob cap:http.post (já assinada) é PERMITIDA para agent-worker e
// NEGADA para quem não a tem — sem re-assinar o bundle.
func TestAOS264_ReutilizacaoCapHTTPPost(t *testing.T) {
	gate := NewScopeGate(DefaultExchangeToolID, signedClassScopes())

	tests := []struct {
		name       string
		class      string
		authority  []string
		capability string
		wantAllow  bool
	}{
		{
			name:       "agent-worker com cap:http.post na autoridade ⇒ PERMITE (reuso assinado)",
			class:      ClassAgentWorker,
			authority:  []string{ExchangeCapabilityHTTPPost},
			capability: ExchangeCapabilityHTTPPost,
			wantAllow:  true,
		},
		{
			name:       "agent-reader (so cap:fs.read) a pedir a troca ⇒ NEGA fail-closed",
			class:      "agent-reader",
			authority:  []string{ExchangeCapabilityHTTPPost}, // token pede, mas a classe nao concede
			capability: ExchangeCapabilityHTTPPost,
			wantAllow:  false,
		},
		{
			name:       "capability NAO-ASSINADA (cap:credential.exchange dedicada) ⇒ NEGA ate reeditar o bundle",
			class:      ClassAgentWorker,
			authority:  []string{"cap:credential.exchange"},
			capability: "cap:credential.exchange",
			wantAllow:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := gate.Evaluate(context.Background(), &referencemonitor.Call{
				ToolID:     DefaultExchangeToolID,
				Capability: tc.capability,
				Principal: referencemonitor.Principal{
					NHIID:      "nhi-worker",
					AgentClass: tc.class,
					Authority:  tc.authority,
				},
			})
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			allowed := res.Decision == referencemonitor.HookAllow
			if allowed != tc.wantAllow {
				t.Fatalf("decisao=%v (allow=%v), esperado allow=%v; reason=%q", res.Decision, allowed, tc.wantAllow, res.Reason)
			}
		})
	}
}

// TestAOS264_ExchangeEndToEndComCapAssinada liga a decisão à troca REAL pela cadeia
// do RM: um agent-worker com cap:http.post troca e obtém handle; a mesma troca sob
// uma capability não-assinada é negada com *DeniedError e não emite handle.
func TestAOS264_ExchangeEndToEndComCapAssinada(t *testing.T) {
	scopes := signedClassScopes()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	hooks := append(referencemonitor.DefaultHooks(), NewScopeGate(DefaultExchangeToolID, scopes))
	rm := referencemonitor.New(
		referencemonitor.WithHooks(hooks...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)
	vlt := NewMemoryVault()
	vlt.Put(VaultKey{Provider: provider, Region: region, Capability: ExchangeCapabilityHTTPPost}, sentinel)

	b, err := New(rm, vlt, es, WithClassScopes(scopes))
	if err != nil {
		t.Fatalf("broker.New: %v", err)
	}

	req := ExchangeRequest{
		RunID:  "run-worker",
		StepID: "step-1",
		Principal: referencemonitor.Principal{
			NHIID:      "nhi-worker",
			AgentClass: ClassAgentWorker,
			Authority:  []string{ExchangeCapabilityHTTPPost},
		},
		Credential: "nhi-token-bearer",
		Downstream: Downstream{
			Provider:      provider,
			Region:        region,
			Capability:    ExchangeCapabilityHTTPPost,
			ResourceType:  "url",
			ResourceValue: resourceURL,
		},
	}
	h, err := b.Exchange(context.Background(), req)
	if err != nil {
		t.Fatalf("troca com cap assinada devia permitir: %v", err)
	}
	if h == "" {
		t.Fatal("handle vazio na troca permitida")
	}
}
