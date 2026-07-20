package testkit_test

import (
	"context"
	"errors"
	"testing"

	tk "github.com/aos-ref/testkit"
)

// TestFakePDP_PermitPorOmissao: o PDP de referência permite por omissão com uma
// versão de política fixa.
func TestFakePDP_PermitPorOmissao(t *testing.T) {
	t.Parallel()
	var pdp tk.PolicyDecisionPoint = tk.NewFakePDP()
	d, err := pdp.Decide(context.Background(), tk.PolicyInput{Capability: "cap:http.get"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !d.Permitted() || d.PolicyVersion != "1.0.0" {
		t.Fatalf("esperava permit v1.0.0, obtive %+v", d)
	}
}

// TestFakePDP_DenyEscalatePorCapability: regras por capability sobrepõem o default.
func TestFakePDP_DenyEscalatePorCapability(t *testing.T) {
	t.Parallel()
	pdp := tk.NewFakePDP().
		DenyOn("cap:payments.charge", "fora do escopo").
		EscalateOn("cap:admin.delete", "gate humano")

	deny, _ := pdp.Decide(context.Background(), tk.PolicyInput{Capability: "cap:payments.charge"})
	if deny.Effect != tk.PolicyDeny {
		t.Fatalf("esperava deny, obtive %q", deny.Effect)
	}
	esc, _ := pdp.Decide(context.Background(), tk.PolicyInput{Capability: "cap:admin.delete"})
	if esc.Effect != tk.PolicyEscalate {
		t.Fatalf("esperava escalate, obtive %q", esc.Effect)
	}
	// Capability sem regra ⇒ default permit.
	ok, _ := pdp.Decide(context.Background(), tk.PolicyInput{Capability: "cap:other"})
	if !ok.Permitted() {
		t.Fatalf("esperava permit por omissao para capability sem regra")
	}

	if len(pdp.Seen()) != 3 {
		t.Fatalf("esperava 3 inputs observados, obtive %d", len(pdp.Seen()))
	}
}

// TestFakePDP_ErroFailClosed: com Err definido, Decide devolve deny + o erro.
func TestFakePDP_ErroFailClosed(t *testing.T) {
	t.Parallel()
	pdp := tk.NewFakePDP()
	pdp.Err = errors.New("politica indisponivel")
	d, err := pdp.Decide(context.Background(), tk.PolicyInput{Capability: "cap:x"})
	if err == nil {
		t.Fatal("esperava erro de porta")
	}
	if d.Effect != tk.PolicyDeny {
		t.Fatalf("fail-closed devia dar deny, obtive %q", d.Effect)
	}
}
