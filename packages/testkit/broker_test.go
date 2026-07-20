package testkit_test

import (
	"context"
	"errors"
	"testing"

	tk "github.com/aos-ref/testkit"
)

// TestFakeBroker_IssueDevolveHandleOpaco: Issue devolve um handle determinista e
// NUNCA o segredo (invariante de AOS-070).
func TestFakeBroker_IssueDevolveHandleOpaco(t *testing.T) {
	t.Parallel()
	var brk tk.CredentialBroker = tk.NewFakeBroker()
	h, err := brk.Issue(context.Background(), tk.BrokerIssueRequest{
		RunID:        tk.FixtureRunID,
		PrincipalNHI: "nhi-1",
		Downstream:   tk.BrokerDownstream{Provider: "stripe", Region: "eu", Capability: "cap:payments.charge"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if h != "h-1" {
		t.Fatalf("handle=%q, esperava h-1 (determinista)", h)
	}
	// INVARIANTE: o handle não é o segredo.
	fb := brk.(*tk.FakeBroker)
	if fb.LeakedInto(string(h)) {
		t.Fatal("o handle NAO deve conter o valor do segredo (AOS-070)")
	}
	if fb.Issued() != 1 {
		t.Fatalf("Issued=%d, esperava 1", fb.Issued())
	}
}

// TestFakeBroker_Revoke: Revoke corta o acesso; um handle desconhecido falha.
func TestFakeBroker_Revoke(t *testing.T) {
	t.Parallel()
	brk := tk.NewFakeBroker()
	h, _ := brk.Issue(context.Background(), tk.BrokerIssueRequest{RunID: tk.FixtureRunID})
	if brk.Revoked(h) {
		t.Fatal("a lease nao devia comecar revogada")
	}
	if err := brk.Revoke(context.Background(), h); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !brk.Revoked(h) {
		t.Fatal("a lease devia estar revogada")
	}
	if err := brk.Revoke(context.Background(), "h-desconhecido"); !errors.Is(err, tk.ErrBrokerUnknownHandle) {
		t.Fatalf("Revoke de handle desconhecido: err=%v, esperava ErrBrokerUnknownHandle", err)
	}
}

// TestFakeBroker_DenyFailClosed: com Deny, Issue nega fail-closed.
func TestFakeBroker_DenyFailClosed(t *testing.T) {
	t.Parallel()
	brk := tk.NewFakeBroker()
	brk.Deny = true
	if _, err := brk.Issue(context.Background(), tk.BrokerIssueRequest{}); !errors.Is(err, tk.ErrBrokerDenied) {
		t.Fatalf("Issue com Deny: err=%v, esperava ErrBrokerDenied", err)
	}
}

// TestFakeBroker_LeakedInto_DetectaFuga: o predicado de fuga é não-vacuoso — se o
// segredo aparecer numa superfície observada, é detectado.
func TestFakeBroker_LeakedInto_DetectaFuga(t *testing.T) {
	t.Parallel()
	brk := tk.NewFakeBroker()
	brk.Secret = "s3cr3t-abc"
	if !brk.LeakedInto("prefixo s3cr3t-abc sufixo") {
		t.Fatal("LeakedInto devia detectar o segredo presente (predicado vacuoso)")
	}
	if brk.LeakedInto("nada aqui") {
		t.Fatal("LeakedInto nao devia disparar sem o segredo")
	}
}
