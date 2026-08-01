package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	identity "github.com/aos-ref/platform/identity"
)

// TestIssuer_MintProducesVerifiableToken fecha o ciclo do issuer externo: o token que a CLI
// `mint` produz VERIFICA contra a pubkey que a CLI `pubkey` exporta — exactamente como o nó o
// faria trust-anchor-only (AOS_ISSUER_PUBKEY = essa pubkey). Dois sentidos: um verifier com
// OUTRA pubkey (issuer não-confiado) RECUSA o token — a não-forjabilidade assenta no anchor.
func TestIssuer_MintProducesVerifiableToken(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "issuer.key")
	const issuerID = "iss:aos-issuer"

	// (1) `pubkey` — o trust anchor que o nó receberia em AOS_ISSUER_PUBKEY.
	var pubBuf bytes.Buffer
	if err := run([]string{"pubkey", "--key-file", key}, &pubBuf); err != nil {
		t.Fatalf("pubkey: %v", err)
	}
	pub, err := hex.DecodeString(strings.TrimSpace(pubBuf.String()))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("pubkey exportada inválida: %v (len=%d)", err, len(pub))
	}

	// (2) `mint` — o token NHI a apresentar ao nó como credencial.
	var tokBuf bytes.Buffer
	if err := run([]string{
		"mint", "--key-file", key, "--issuer", issuerID,
		"--human", "human:alice", "--agent", "agt-1", "--class", "agent-worker", "--caps", "cap:fs.read",
	}, &tokBuf); err != nil {
		t.Fatalf("mint: %v", err)
	}
	compact := strings.TrimSpace(tokBuf.String())
	if compact == "" {
		t.Fatal("mint não produziu token")
	}

	// (3) O nó verifica TRUST-ANCHOR-ONLY: o verifier com a pubkey EXPORTADA aceita o token.
	v := identity.NewVerifier(identity.WithTrustedIssuer(issuerID, ed25519.PublicKey(pub)))
	if _, err := v.Verify(context.Background(), compact); err != nil {
		t.Fatalf("o token do issuer devia VERIFICAR contra a pubkey exportada (trust-anchor-only): %v", err)
	}

	// (4) DOIS SENTIDOS: um verifier com OUTRA pubkey (issuer não-confiado) RECUSA — falha-antes
	// de qualquer confiança no anchor errado.
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	vBad := identity.NewVerifier(identity.WithTrustedIssuer(issuerID, other.Public().(ed25519.PublicKey)))
	if _, err := vBad.Verify(context.Background(), compact); err == nil {
		t.Fatal("um verifier com OUTRA pubkey NÃO devia aceitar o token — não-forjabilidade violada")
	}
}

// TestIssuer_MintFailClosed prova que `mint` recusa fail-closed sem os campos obrigatórios (não
// emite um token degenerado).
func TestIssuer_MintFailClosed(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "issuer.key")
	var out bytes.Buffer
	if err := run([]string{"mint", "--key-file", key, "--agent", "a", "--class", "c"}, &out); err == nil {
		t.Fatal("mint sem --human devia FALHAR fail-closed")
	}
	if out.Len() != 0 {
		t.Fatalf("mint recusado não devia imprimir nada, veio %q", out.String())
	}
}
