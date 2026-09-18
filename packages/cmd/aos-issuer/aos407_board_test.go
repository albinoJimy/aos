package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/integration/oidc"
)

// AOS-407 — o board de soberania do NHI vem do IdP (claim `board`) com --assertion, ou de --board
// com --human; sem board não se cunha.

func signIDTokenComBoard(t *testing.T, idp *testIDP, sub, board string) string {
	t.Helper()
	now := idpClock()().Unix()
	claims := map[string]any{
		"iss": idpIssuer, "sub": sub, "aud": idpAudience,
		"exp": now + 3600, "iat": now - 30, "nbf": now - 30,
	}
	if board != "" {
		claims["board"] = board
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

func TestAOS407_AsserçãoDevolveOBoardVerificadoDoIdP(t *testing.T) {
	idp := newTestIDP(t)
	cfg := oidc.Config{Issuer: idpIssuer, Audience: idpAudience, JWKSURI: idp.jwksURI(), HTTPClient: idp.server.Client(), Clock: idpClock()}

	_, _, board, err := authenticateOIDCComBoard(context.Background(), cfg, signIDTokenComBoard(t, idp, "alice", "board:prod"))
	if err != nil {
		t.Fatalf("authenticateOIDCComBoard: %v", err)
	}
	if board != "board:prod" {
		t.Fatalf("board = %q, quero o da claim verificada (board:prod)", board)
	}

	_, _, board, err = authenticateOIDCComBoard(context.Background(), cfg, signIDTokenComBoard(t, idp, "alice", ""))
	if err != nil || board != "" {
		t.Fatalf("sem a claim o board fica vazio (o mint recusa a seguir): board=%q err=%v", board, err)
	}
}

func TestAOS407_MintSemBoardRecusa(t *testing.T) {
	key := filepath.Join(t.TempDir(), "issuer.key")
	err := run([]string{"mint", "--key-file", key, "--human", "human:alice", "--agent", "agt-1", "--class", "agent-worker", "--caps", "cap:fs.read"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "board") {
		t.Fatalf("mint --human sem --board tem de recusar a nomear o board; veio %v", err)
	}
}

func TestAOS407_BoardPorFlagComAsserçãoRecusa(t *testing.T) {
	key := filepath.Join(t.TempDir(), "issuer.key")
	err := run([]string{"mint", "--key-file", key, "--assertion", "x.y.z", "--oidc-issuer", idpIssuer, "--oidc-audience", idpAudience,
		"--board", "board:outro", "--agent", "agt-1", "--class", "agent-worker", "--caps", "cap:fs.read"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--board só se usa com --human") {
		t.Fatalf("com --assertion o board vem do IdP e a flag é recusada; veio %v", err)
	}
}
