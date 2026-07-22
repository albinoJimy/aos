package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestRunProductionRequiresHardenedIdentity prova o fail-closed do entrypoint: sob
// AOS_MODE=production o nó RECUSA arrancar no modo de referência (autoridade
// co-localizada) — exige o trust-anchor-only via AOS_ISSUER_PUBKEY. Um operador não pode,
// por engano, correr o arranque de referência como fronteira de produção endurecida.
func TestRunProductionRequiresHardenedIdentity(t *testing.T) {
	t.Setenv("AOS_MODE", "production")
	t.Setenv("AOS_ISSUER_PUBKEY", "") // sem trust anchor ⇒ só resta o modo de referência

	if err := run(io.Discard); !errors.Is(err, ErrProductionNeedsHardenedIdentity) {
		t.Fatalf("production sem AOS_ISSUER_PUBKEY devia abortar com ErrProductionNeedsHardenedIdentity, veio: %v", err)
	}
}

// TestRunRejectsMalformedIssuerPubKey prova que um AOS_ISSUER_PUBKEY malformado aborta
// fail-closed — um anchor inválido nunca compõe o verifier.
func TestRunRejectsMalformedIssuerPubKey(t *testing.T) {
	t.Setenv("AOS_MODE", "production")
	t.Setenv("AOS_ISSUER_PUBKEY", "nao-e-hex")

	if err := run(io.Discard); !errors.Is(err, ErrBadIssuerPubKey) {
		t.Fatalf("AOS_ISSUER_PUBKEY malformado devia abortar com ErrBadIssuerPubKey, veio: %v", err)
	}
}

// TestRunProductionWithTrustAnchorSucceeds prova que, dado um trust anchor válido, o modo
// de produção arranca (trust-anchor-only) sem qualquer chave de assinatura no processo.
func TestRunProductionWithTrustAnchorSucceeds(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	var sb strings.Builder
	t.Setenv("AOS_MODE", "production")
	t.Setenv("AOS_ISSUER_ID", "iss:external-authority")
	t.Setenv("AOS_ISSUER_PUBKEY", hex.EncodeToString(pub))

	if err := run(&sb); err != nil {
		t.Fatalf("production com trust anchor valido devia arrancar, veio: %v", err)
	}
	if !strings.Contains(sb.String(), IdentityModeRealHardened) {
		t.Fatalf("o banner devia declarar o modo %q, veio:\n%s", IdentityModeRealHardened, sb.String())
	}
}
