package main

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	identity "github.com/aos-ref/platform/identity"
)

// fakeTransit é um Vault Transit MÍNIMO: detém uma keypair ed25519 REAL, serve a pubkey em
// GET keys/<k> e assina em POST sign/<k>. Prova que o vaultTransitSigner fala o protocolo certo
// e que a chave privada NUNCA sai (o teste é que quem assina é o "vault", não o signer).
func fakeTransit(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/keys/"):
			resp := map[string]any{"data": map[string]any{
				"type": "ed25519", "latest_version": 1,
				"keys": map[string]any{"1": map[string]any{"public_key": base64.StdEncoding.EncodeToString(pub)}},
			}}
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/sign/"):
			var body struct {
				Input string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			msg, _ := base64.StdEncoding.DecodeString(body.Input)
			sig := ed25519.Sign(priv, msg) // a "chave no vault" assina.
			resp := map[string]any{"data": map[string]any{"signature": "vault:v1:" + base64.StdEncoding.EncodeToString(sig)}}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

func TestVaultTransitSigner_PublicAndSign(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := fakeTransit(t, pub, priv)
	defer srv.Close()

	s, err := newVaultTransitSigner(srv.URL, "transit", "aos-issuer", "tok")
	if err != nil {
		t.Fatalf("construir signer: %v", err)
	}
	// Public() é a pubkey do vault (o trust-anchor do nó).
	if got := s.Public().(ed25519.PublicKey); !got.Equal(pub) {
		t.Fatal("Public() nao devolveu a pubkey ed25519 do vault")
	}
	// Sign() produz uma assinatura que valida contra a pubkey — como o Issuer chama (Hash(0)).
	msg := []byte("aos.identity.signing.input")
	sig, err := s.Sign(rand.Reader, msg, crypto.Hash(0))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !ed25519.Verify(pub, msg, sig) {
		t.Fatal("a assinatura do vault NAO valida contra a pubkey — protocolo Transit errado")
	}
}

// TestVaultTransitSigner_EndToEndIssuer: um Issuer construído com o signer Vault emite um token
// NHI que um Verifier ancorado na pubkey do Vault ACEITA — a cadeia completa "chave no vault →
// nó verifica só com a pubkey" (D4), sem a privada entrar no processo do issuer.
func TestVaultTransitSigner_EndToEndIssuer(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := fakeTransit(t, pub, priv)
	defer srv.Close()

	signer, err := newVaultTransitSigner(srv.URL, "transit", "aos-issuer", "tok")
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	iss, err := identity.NewIssuerWithSigner("iss:aos-issuer", signer, map[string]identity.ClassPolicy{
		"agent-worker": {TTL: 15 * 60 * 1e9, Scope: []string{"cap:http.post"}},
	})
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	tok, err := iss.Issue(t.Context(), identity.IssueRequest{
		UserID: "human:alice", AgentID: "agt-x", AgentClass: "agent-worker",
		PolicyRef: "policy://agent-worker", UserAuthority: []string{"cap:http.post"}, AuthMethod: "manual",
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// O nó verifica trust-anchor-only com a pubkey do Vault.
	v := identity.NewVerifier(identity.WithTrustedIssuer("iss:aos-issuer", pub))
	if _, err := v.Verify(t.Context(), tok.Compact); err != nil {
		t.Fatalf("o token assinado pelo Vault devia verificar contra a pubkey do Vault, veio: %v", err)
	}
}
