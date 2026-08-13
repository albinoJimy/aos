package weights

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
// pinado (o caminho de produção). Guard de drift: se weights_table.pub for
// regenerada com outra chave sem actualizar o anchor revisto em código, este teste
// fica vermelho. É o gémeo do guard da allowlist regional (AOS-058).
func TestVerifyTrustAnchor_EmbeddedMatches(t *testing.T) {
	if err := verifyTrustAnchor(strings.TrimSpace(string(embeddedPub))); err != nil {
		t.Fatalf("pública embebida devia bater com o trust anchor pinado: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(embeddedPub)))
	if err != nil {
		t.Fatalf("pública embebida nao-base64: %v", err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != trustAnchorFingerprint {
		t.Fatalf("fingerprint da pública embebida = %s; anchor pinado = %s", hex.EncodeToString(sum[:]), trustAnchorFingerprint)
	}
}

// TestVerifyTrustAnchor_WrongKeyRejected — a pública que um atacante embeberia para
// reassinar uma tabela de pesos adulterada (e assim mudar a DECISÃO do gateway sem
// tocar em código) é recusada fail-closed.
func TestVerifyTrustAnchor_WrongKeyRejected(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if got := verifyTrustAnchor(base64.StdEncoding.EncodeToString(pub)); !errors.Is(got, ErrTrustAnchorMismatch) {
		t.Fatalf("pública atacante devia falhar ErrTrustAnchorMismatch; got %v", got)
	}
}

// TestVerifyTrustAnchor_MalformedRejected — pública malformada é recusada.
func TestVerifyTrustAnchor_MalformedRejected(t *testing.T) {
	if err := verifyTrustAnchor("nao-e-base64!!!"); !errors.Is(err, ErrPubKeyInvalid) {
		t.Fatalf("pública malformada devia falhar ErrPubKeyInvalid; got %v", err)
	}
}
