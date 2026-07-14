package signing

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/aos-ref/platform/registry/domain"
)

// --- helpers deterministas -------------------------------------------------

// keyFromSeed produz um par Ed25519 DETERMINÍSTICO a partir de um byte de seed
// (testes reproduzíveis; nunca rand numa asserção).
func keyFromSeed(b byte) ed25519.PrivateKey {
	seed := bytes.Repeat([]byte{b}, ed25519.SeedSize)
	return ed25519.NewKeyFromSeed(seed)
}

func ver(mj, mn, p int) domain.Version {
	return domain.Version{Major: mj, Minor: mn, Patch: p}
}

// --- SigningInput ----------------------------------------------------------

func TestSigningInput_DeterministicAndDomainSeparated(t *testing.T) {
	t.Parallel()
	a := SigningInput("tool.http", ver(1, 2, 3), "sha256:abc")
	b := SigningInput("tool.http", ver(1, 2, 3), "sha256:abc")
	if !bytes.Equal(a, b) {
		t.Fatal("SigningInput nao e determinista para o mesmo tuplo")
	}
	// Domain separation por comprimento: mover um caracter da fronteira id|version
	// tem de produzir bytes DIFERENTES (senao (a,1.0.0) colidiria com (a1,.0.0)).
	c := SigningInput("tool.http1", ver(1, 2, 3), "sha256:abc")
	if bytes.Equal(a, c) {
		t.Fatal("SigningInput colidiu em fronteira de campo (domain separation quebrada)")
	}
	// Qualquer mudanca do digest muda os bytes assinados.
	d := SigningInput("tool.http", ver(1, 2, 3), "sha256:abd")
	if bytes.Equal(a, d) {
		t.Fatal("SigningInput ignorou uma mudanca de digest")
	}
}

// --- NewSigner / NewVerifier config ---------------------------------------

func TestNewSigner_FailClosed(t *testing.T) {
	t.Parallel()
	priv := keyFromSeed(1)
	cases := []struct {
		name  string
		keyID string
		priv  ed25519.PrivateKey
		want  error
	}{
		{"ok", "pub:acme", priv, nil},
		{"empty keyid", "", priv, ErrEmptyKeyID},
		{"short key", "pub:acme", priv[:10], ErrInvalidKey},
		{"nil key", "pub:acme", nil, ErrInvalidKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewSigner(tc.keyID, tc.priv)
			if !errors.Is(err, tc.want) {
				t.Fatalf("NewSigner = %v, quer %v", err, tc.want)
			}
		})
	}
}

// --- Sign / Verify ---------------------------------------------------------

func TestVerify_ValidSignaturePasses(t *testing.T) {
	t.Parallel()
	priv := keyFromSeed(7)
	s, err := NewSigner("pub:acme", priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	id, v, dig := "tool.http", ver(2, 0, 1), "sha256:deadbeef"
	sig := s.Sign(id, v, dig)
	if err := Verify(s.PublicKey(), id, v, dig, sig); err != nil {
		t.Fatalf("Verify de assinatura valida falhou: %v", err)
	}
}

// TestVerify_FailClosed cobre os Testes Requeridos de AOS-048: assinatura
// ADULTERADA, DIGEST TROCADO e CHAVE DESCONHECIDA falham (fail-closed).
func TestVerify_FailClosed(t *testing.T) {
	t.Parallel()
	priv := keyFromSeed(9)
	other := keyFromSeed(10)
	s, _ := NewSigner("pub:acme", priv)
	id, v, dig := "tool.db", ver(1, 0, 0), "sha256:cafe"
	good := s.Sign(id, v, dig)

	// Assinatura adulterada: flip de um bit na base64 decodificada.
	tampered := flipLastSigByte(t, good)

	cases := []struct {
		name string
		pub  ed25519.PublicKey
		id   string
		v    domain.Version
		dig  string
		sig  string
		want error
	}{
		{"assinatura adulterada", s.PublicKey(), id, v, dig, tampered, ErrSignatureInvalid},
		{"digest trocado", s.PublicKey(), id, v, "sha256:0000", good, ErrSignatureInvalid},
		{"id trocado", s.PublicKey(), "tool.OTHER", v, dig, good, ErrSignatureInvalid},
		{"version trocada", s.PublicKey(), id, ver(1, 0, 1), dig, good, ErrSignatureInvalid},
		{"chave desconhecida", other.Public().(ed25519.PublicKey), id, v, dig, good, ErrSignatureInvalid},
		{"assinatura ausente", s.PublicKey(), id, v, dig, "", ErrSignatureMissing},
		{"base64 corrompida", s.PublicKey(), id, v, dig, "!!!nao-base64!!!", ErrSignatureInvalid},
		{"chave publica invalida", ed25519.PublicKey{1, 2, 3}, id, v, dig, good, ErrInvalidKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := Verify(tc.pub, tc.id, tc.v, tc.dig, tc.sig)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Verify = %v, quer %v", err, tc.want)
			}
		})
	}
}

// flipLastSigByte devolve uma assinatura base64 cujo ultimo byte foi alterado
// (continua a ter o tamanho certo, mas ja nao valida).
func flipLastSigByte(t *testing.T, sig string) string {
	t.Helper()
	raw, err := base64.RawStdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	raw[len(raw)-1] ^= 0xFF
	return base64.RawStdEncoding.EncodeToString(raw)
}
