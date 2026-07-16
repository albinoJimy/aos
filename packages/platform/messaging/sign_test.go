package messaging

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

func TestSignMessage_ValidAndDeterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vault := newFakeVault()
	vault.provision("nhi:a", 1)

	msg := Message{
		Origin:    "nhi:a",
		Authority: []string{"cap:summarize"},
		Action:    "cap:summarize",
		Reference: Reference{ID: "ref-1", Hash: contentHash([]byte("r"))},
		Payload:   []byte("hello"),
		Nonce:     newNonce(t),
		IssuedAt:  time.Unix(1_700_000_000, 0).UTC(),
	}

	s1, err := SignMessage(ctx, vault, msg)
	if err != nil {
		t.Fatalf("SignMessage: %v", err)
	}
	if len(s1.Signature) != ed25519.SignatureSize {
		t.Fatalf("assinatura de dimensao %d, quer %d", len(s1.Signature), ed25519.SignatureSize)
	}
	// ed25519 é determinista: assinar o mesmo conteúdo produz a mesma assinatura.
	s2, err := SignMessage(ctx, vault, msg)
	if err != nil {
		t.Fatalf("SignMessage (2): %v", err)
	}
	if string(s1.Signature) != string(s2.Signature) {
		t.Fatal("assinatura não-determinista para o mesmo conteúdo")
	}
	// O argumento não é mutado.
	if msg.Signature != nil {
		t.Fatal("SignMessage mutou o argumento")
	}
}

func TestSignMessage_AuthorityOrderIndependent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vault := newFakeVault()
	vault.provision("nhi:a", 1)

	base := Message{
		Origin:    "nhi:a",
		Action:    "cap:summarize",
		Reference: Reference{ID: "ref-1", Hash: contentHash([]byte("r"))},
		Nonce:     newNonce(t), // partilhado por m1/m2 para as assinaturas coincidirem
		IssuedAt:  time.Unix(1_700_000_000, 0).UTC(),
	}
	m1 := base
	m1.Authority = []string{"cap:a", "cap:b", "cap:a"} // com duplicado
	m2 := base
	m2.Authority = []string{"cap:b", "cap:a"} // ordem trocada

	s1, err := SignMessage(ctx, vault, m1)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := SignMessage(ctx, vault, m2)
	if err != nil {
		t.Fatal(err)
	}
	if string(s1.Signature) != string(s2.Signature) {
		t.Fatal("a canonicalização da autoridade não é independente da ordem/duplicados")
	}
}

func TestSignMessage_InvalidAndNilDeps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vault := newFakeVault()
	vault.provision("nhi:a", 1)

	if _, err := SignMessage(ctx, nil, Message{Origin: "nhi:a", Action: "x"}); !errors.Is(err, ErrNilDeps) {
		t.Fatalf("signer nil: erro %v, quer ErrNilDeps", err)
	}
	if _, err := SignMessage(ctx, vault, Message{Action: "x"}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("origem vazia: erro %v, quer ErrInvalidMessage", err)
	}
	if _, err := SignMessage(ctx, vault, Message{Origin: "nhi:a"}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("acção vazia: erro %v, quer ErrInvalidMessage", err)
	}
	// Nonce curto ⇒ mensagem inválida (fail-closed sobre o material anti-replay).
	if _, err := SignMessage(ctx, vault, Message{
		Origin: "nhi:a", Action: "x", Nonce: []byte("curto"), IssuedAt: time.Unix(1, 0),
	}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("nonce curto: erro %v, quer ErrInvalidMessage", err)
	}
	// Timestamp zero ⇒ mensagem inválida.
	if _, err := SignMessage(ctx, vault, Message{
		Origin: "nhi:a", Action: "x", Nonce: newNonce(t),
	}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("timestamp zero: erro %v, quer ErrInvalidMessage", err)
	}
	// NHI sem material no custodiante ⇒ erro propagado (fail-closed).
	if _, err := SignMessage(ctx, vault, Message{
		Origin: "nhi:unknown", Action: "x", Nonce: newNonce(t), IssuedAt: time.Unix(1_700_000_000, 0),
	}); err == nil {
		t.Fatal("assinatura sem material devia falhar")
	}
}
