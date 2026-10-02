package identity

// aos439_hook_preserva_test.go — O HOOK DE IDENTIDADE SUBSTITUI A IDENTIDADE, E NÃO A ATRIBUIÇÃO
// (AOS-439/440).
//
// O `IdentityCheck` reescreve o Principal inteiro a partir do token verificado — é a defesa contra
// um Call que afirme uma identidade. O `RequestedBy` e o `Subject` não são identidade: são derivados
// pelo nó e viajam do Goal. Se o hook os apagasse, a decisão selada perdia quem pediu o run.

import (
	"context"
	"testing"
	"time"

	rm "github.com/aos-ref/kernel/reference-monitor"
)

func TestAOS439HookPreservaSubmissorETitularESelaOMandato(t *testing.T) {
	c := novoCenario(t)
	tok := c.cunharHonesto(t, &c.mandato)
	hook := NewIdentityCheck(c.verificador(t0.Add(time.Hour)))

	call := &rm.Call{
		Credential: tok.Compact, Capability: "run:submit",
		Principal: rm.Principal{
			NHIID: "forjado", AgentID: "forjado", MandateID: "m-forjado", UserID: "forjado",
			RequestedBy: "sub-bob", Subject: "sub-bob",
		},
	}
	res, err := hook.Evaluate(context.Background(), call)
	if err != nil || res.Decision != rm.HookAllow {
		t.Fatalf("o token dentro do mandato tinha de passar: %+v %v", res, err)
	}
	if call.Principal.NHIID != "agent:aos-orq" {
		t.Fatalf("a identidade vem do token, nao do Call: %q", call.Principal.NHIID)
	}
	if call.Principal.UserID != "alice" {
		t.Fatalf("o UserID vem do token verificado (alice): %q", call.Principal.UserID)
	}
	if call.Principal.MandateID != "m-1" {
		t.Fatalf("o MandateID vem do Verify (m-1), nao do Call: %q", call.Principal.MandateID)
	}
	if call.Principal.RequestedBy != "sub-bob" || call.Principal.Subject != "sub-bob" {
		t.Fatalf("o hook apagou a atribuicao derivada pelo no: %+v", call.Principal)
	}
}
