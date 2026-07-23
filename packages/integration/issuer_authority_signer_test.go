package integration

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"errors"
	"io"
	"testing"

	identity "github.com/aos-ref/platform/identity"
)

// AOS-175: custódia de chave EXTERNA ao nível da AUTORIDADE. A [IssuerAuthority] pode ser
// construída com um crypto.Signer (HSM/KMS) em vez de uma chave ed25519 crua. Estes
// testes provam que a autoridade continua a NÃO devolver a chave privada (TrustAnchor só
// pubkey), que um mint verifica sob essa pubkey com human:<...> na raiz, e que a
// precedência de fonte de chave é inequívoca (fail-closed se ambígua).

// custodySigner modela um HSM/KMS: encapsula a chave ed25519 e expõe SÓ Public()+Sign.
// Nenhuma via devolve os bytes da chave privada.
type custodySigner struct {
	priv ed25519.PrivateKey
}

func newCustodySigner(t *testing.T) *custodySigner {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return &custodySigner{priv: priv}
}

func (s *custodySigner) Public() crypto.PublicKey { return s.priv.Public() }
func (s *custodySigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts.HashFunc() != crypto.Hash(0) {
		return nil, errors.New("custodySigner: espera crypto.Hash(0)")
	}
	return ed25519.Sign(s.priv, digest), nil
}

// TestAuthority_ExternalSigner_MintVerifies — a autoridade construída com um Signer
// externo minta um token cujo verifier (só a pubkey via TrustAnchor) ACEITA, com
// human:<humano> na raiz da cadeia. A chave privada nunca sai da autoridade.
func TestAuthority_ExternalSigner_MintVerifies(t *testing.T) {
	t.Parallel()
	signer := newCustodySigner(t)
	auth, err := NewIssuerAuthority(AuthorityConfig{
		IssuerID:      iaIssuerID,
		Classes:       iaClasses(),
		Directory:     NewAllowlistDirectory(iaHuman),
		Signer:        signer,
		IssuerOptions: []identity.IssuerOption{identity.WithIssuerClock(iaClock())},
	})
	if err != nil {
		t.Fatalf("NewIssuerAuthority(Signer): %v", err)
	}

	// TrustAnchor só devolve pubkey — e é a do signer externo.
	gotIss, pub := auth.TrustAnchor()
	if gotIss != iaIssuerID {
		t.Fatalf("TrustAnchor issuer = %q, esperava %q", gotIss, iaIssuerID)
	}
	if !pub.Equal(signer.priv.Public().(ed25519.PublicKey)) {
		t.Fatal("TrustAnchor pubkey nao corresponde a do signer externo")
	}

	tok, err := auth.MintForHuman(context.Background(), iaHuman, iaAgent, iaClass, []string{iaCapRead})
	if err != nil {
		t.Fatalf("MintForHuman (via signer externo): %v", err)
	}

	v := NewVerifierFromAuthority(auth, identity.WithVerifierClock(iaClock()))
	p, err := v.Verify(context.Background(), tok.Compact)
	if err != nil {
		t.Fatalf("Verify do token mintado via signer externo devia aceitar: %v", err)
	}
	human, err := p.HumanPrincipal()
	if err != nil {
		t.Fatalf("HumanPrincipal: %v", err)
	}
	if human != "human:"+iaHuman {
		t.Fatalf("raiz da cadeia = %q, esperava human:%s", human, iaHuman)
	}
}

// TestAuthority_SignerAndKey_Ambiguous — fornecer AMBOS Signer e SigningKey é recusado
// fail-closed (fonte de chave ambígua).
func TestAuthority_SignerAndKey_Ambiguous(t *testing.T) {
	t.Parallel()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	_, err = NewIssuerAuthority(AuthorityConfig{
		IssuerID:   iaIssuerID,
		Classes:    iaClasses(),
		Directory:  NewAllowlistDirectory(iaHuman),
		Signer:     newCustodySigner(t),
		SigningKey: priv,
	})
	if !errors.Is(err, ErrAmbiguousSigningKey) {
		t.Fatalf("Signer+SigningKey devia dar ErrAmbiguousSigningKey, obtive %v", err)
	}
}

// TestAuthority_InvalidExternalSigner_FailClosed — um Signer cuja Public() não é ed25519
// é recusado (o erro do identity.Issuer propaga).
func TestAuthority_InvalidExternalSigner_FailClosed(t *testing.T) {
	t.Parallel()
	_, err := NewIssuerAuthority(AuthorityConfig{
		IssuerID:  iaIssuerID,
		Classes:   iaClasses(),
		Directory: NewAllowlistDirectory(iaHuman),
		Signer:    badPubSigner{},
	})
	if !errors.Is(err, identity.ErrInvalidSigner) {
		t.Fatalf("signer nao-ed25519 devia dar ErrInvalidSigner, obtive %v", err)
	}
}

// badPubSigner implementa crypto.Signer mas Public() devolve um tipo não-ed25519.
type badPubSigner struct{}

func (badPubSigner) Public() crypto.PublicKey { return "not-a-key" }
func (badPubSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("nao devia ser chamado")
}
