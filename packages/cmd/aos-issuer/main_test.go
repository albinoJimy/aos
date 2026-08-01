package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oidc "github.com/aos-ref/integration/oidc"
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

// --- IdP OIDC de teste (RSA, JWKS via httptest) — replica mínima do padrão de AOS-174 ---

const (
	idpIssuer   = "https://idp.dev.example"
	idpAudience = "aos-issuer-client"
	idpKid      = "idp-dev-1"
)

func idpClock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

type testIDP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
}

func newTestIDP(t *testing.T) *testIDP {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gerar RSA: %v", err)
	}
	idp := &testIDP{key: k}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := &idp.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": idpKid, "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *testIDP) jwksURI() string { return idp.server.URL + "/jwks" }

// signIDToken minta um ID-token RS256 para o sub dado (claims válidas para idpClock).
func (idp *testIDP) signIDToken(t *testing.T, sub string) string {
	t.Helper()
	now := idpClock()().Unix()
	claims := map[string]any{
		"iss": idpIssuer, "sub": sub, "aud": idpAudience,
		"exp": now + 3600, "iat": now - 30, "nbf": now - 30,
	}
	hdr, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": idpKid})
	pb, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(pb)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("assinar id-token: %v", err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// TestIssuer_OIDCAuthenticatesHumanBeforeMint prova a costura front-1 do D4 (AOS-174) no issuer:
// com um ID-token VÁLIDO, `authenticateOIDC` deriva o humano-raiz do `sub` VERIFICADO e o método
// `oidc:<issuer>`; um ID-token ADULTERADO é RECUSADO fail-closed (nenhum humano derivado). É o que
// permite ao `mint --assertion` autenticar o humano contra um IdP real em vez de o auto-declarar.
func TestIssuer_OIDCAuthenticatesHumanBeforeMint(t *testing.T) {
	idp := newTestIDP(t)
	cfg := oidc.Config{
		Issuer:     idpIssuer,
		Audience:   idpAudience,
		JWKSURI:    idp.jwksURI(),
		HTTPClient: idp.server.Client(),
		Clock:      idpClock(),
	}

	// (1) VÁLIDO: humano derivado do sub verificado; método = oidc:<issuer>.
	idToken := idp.signIDToken(t, "alice@corp.example")
	human, method, err := authenticateOIDC(context.Background(), cfg, idToken)
	if err != nil {
		t.Fatalf("authenticateOIDC (ID-token válido): %v", err)
	}
	if human != "human:alice@corp.example" {
		t.Fatalf("humano derivado = %q, quero human:alice@corp.example (o sub verificado)", human)
	}
	if want := "oidc:" + idpIssuer; method != want {
		t.Fatalf("método = %q, quero %q (contexto de binding audit)", method, want)
	}

	// (2) ADULTERADO: recusa fail-closed — nenhum humano é derivado de um token não-verificado.
	tampered := idToken[:len(idToken)-2] + "AA"
	if h, _, err := authenticateOIDC(context.Background(), cfg, tampered); err == nil {
		t.Fatalf("ID-token adulterado devia ser RECUSADO fail-closed, veio humano=%q", h)
	}
}
