package allowlist

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// TestVerifyTrustAnchor_EmbeddedMatches — a pública EMBEBIDA bate com o fingerprint
// pinado (o caminho de produção). Guard de drift: se allowlist_policy.pub for
// regenerada com outra chave sem actualizar o anchor revisto, este teste falha.
func TestVerifyTrustAnchor_EmbeddedMatches(t *testing.T) {
	if err := verifyTrustAnchor(strings.TrimSpace(string(embeddedPub))); err != nil {
		t.Fatalf("pública embebida devia bater com o trust anchor pinado: %v", err)
	}
	// O fingerprint pinado é, de facto, o sha256 da pública embebida.
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(embeddedPub)))
	if err != nil {
		t.Fatalf("pública embebida nao-base64: %v", err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != trustAnchorFingerprint {
		t.Fatalf("fingerprint da pública embebida = %s; anchor pinado = %s", hex.EncodeToString(sum[:]), trustAnchorFingerprint)
	}
}

// TestVerifyTrustAnchor_WrongKeyRejected — uma pública DIFERENTE (a que um atacante
// embeberia para reassinar uma allowlist adulterada) é recusada fail-closed. É a
// prova de que trocar allowlist_policy.pub não chega: sem a privada custodiada
// offline, é inviável produzir uma pública que bata com o fingerprint pinado.
func TestVerifyTrustAnchor_WrongKeyRejected(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	got := verifyTrustAnchor(base64.StdEncoding.EncodeToString(pub))
	if !errors.Is(got, ErrTrustAnchorMismatch) {
		t.Fatalf("pública atacante devia falhar ErrTrustAnchorMismatch; got %v", got)
	}
}

// TestVerifyTrustAnchor_MalformedRejected — uma pública malformada (não-base64) é
// recusada como chave inválida (fail-closed), nunca aceite.
func TestVerifyTrustAnchor_MalformedRejected(t *testing.T) {
	if err := verifyTrustAnchor("nao-e-base64!!!"); !errors.Is(err, ErrPubKeyInvalid) {
		t.Fatalf("pública malformada devia falhar ErrPubKeyInvalid; got %v", err)
	}
}
