package integration

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/integration/oidc"
	identity "github.com/aos-ref/platform/identity"
)

// Este ficheiro prova a INTEGRAÇÃO fim-a-fim de AOS-174: um ID-token OIDC validado pelo
// [OIDCDirectory] autentica o humano e a [IssuerAuthority.MintForAssertion] minta um
// token NHI com human:<sub> na RAIZ da cadeia de delegação — tudo OFFLINE (httptest),
// substituindo a allowlist demo-grade pela prova real.

const (
	odIssuerID  = "iss:aos-idp"
	odAgent     = "agt-oidc-1"
	odClass     = "researcher"
	odCap       = "cap:doc.read"
	odKid       = "idp-rsa-1"
	odClientAud = "aos-node-client"
)

func odClock() func() time.Time { return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() } }

func odClasses() map[string]identity.ClassPolicy {
	return map[string]identity.ClassPolicy{
		odClass: {TTL: 10 * time.Minute, Scope: []string{odCap}},
	}
}

// odIDP é o IdP OIDC de teste (RSA, JWKS + discovery via httptest).
type odIDP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
}

func newODIDP(t *testing.T) *odIDP {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gerar RSA: %v", err)
	}
	idp := &odIDP{key: k}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := &idp.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": odKid, "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *odIDP) jwksURI() string { return idp.server.URL + "/jwks" }

// signIDToken minta um ID-token RS256 para o sub dado (claims válidas por omissão;
// mutate permite tocar nelas para os casos de recusa).
func (idp *odIDP) signIDToken(t *testing.T, sub string, mutate func(map[string]any)) string {
	t.Helper()
	now := odClock()().Unix()
	claims := map[string]any{
		"iss": "https://idp.corp.example",
		"sub": sub,
		"aud": odClientAud,
		"exp": now + 3600,
		"iat": now - 30,
		"nbf": now - 30,
	}
	if mutate != nil {
		mutate(claims)
	}
	hdr, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": odKid})
	pb, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(pb)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("assinar id-token: %v", err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// newOIDCAuthority constrói uma [IssuerAuthority] cuja porta de autenticação humana é o
// [OIDCDirectory] real (não a allowlist).
func newOIDCAuthority(t *testing.T, idp *odIDP) *IssuerAuthority {
	t.Helper()
	dir, err := NewOIDCDirectory(oidc.Config{
		Issuer:     "https://idp.corp.example",
		Audience:   odClientAud,
		JWKSURI:    idp.jwksURI(),
		HTTPClient: idp.server.Client(),
		Clock:      odClock(),
	})
	if err != nil {
		t.Fatalf("NewOIDCDirectory: %v", err)
	}
	auth, err := NewIssuerAuthority(AuthorityConfig{
		IssuerID:      odIssuerID,
		Classes:       odClasses(),
		Directory:     dir,
		IssuerOptions: []identity.IssuerOption{identity.WithIssuerClock(odClock())},
	})
	if err != nil {
		t.Fatalf("NewIssuerAuthority: %v", err)
	}
	return auth
}

// (1) ID-token VÁLIDO → humano autenticado → MintForAssertion minta com human:<sub> na
// RAIZ e o token verifica sob o trust anchor.
func TestOIDCDirectory_ValidAssertionMintsWithHumanRoot(t *testing.T) {
	ctx := context.Background()
	idp := newODIDP(t)
	auth := newOIDCAuthority(t, idp)

	idToken := idp.signIDToken(t, "alice@corp.example", nil)
	tok, err := auth.MintForAssertion(ctx, idToken, odAgent, odClass, []string{odCap})
	if err != nil {
		t.Fatalf("MintForAssertion com ID-token valido: %v", err)
	}

	verifier := NewVerifierFromAuthority(auth, identity.WithVerifierClock(odClock()))
	principal, err := verifier.Verify(ctx, tok.Compact)
	if err != nil {
		t.Fatalf("Verify do token mintado: %v", err)
	}
	if principal.AgentID != odAgent {
		t.Fatalf("AgentID=%q, quero %q", principal.AgentID, odAgent)
	}
	human, err := principal.HumanPrincipal()
	if err != nil {
		t.Fatalf("HumanPrincipal: %v", err)
	}
	// O sub VERIFICADO do ID-token está na raiz da cadeia (reconstrução de autoria).
	if want := "human:alice@corp.example"; human != want {
		t.Fatalf("raiz da cadeia=%q, quero %q", human, want)
	}
}

// (2) Assinatura adulterada → MintForAssertion RECUSA fail-closed, envolvendo o erro
// concreto do OIDC. Nenhum token é emitido.
func TestOIDCDirectory_TamperedAssertionRefused(t *testing.T) {
	ctx := context.Background()
	idp := newODIDP(t)
	auth := newOIDCAuthority(t, idp)

	idToken := idp.signIDToken(t, "alice@corp.example", nil)
	tampered := adulterarAssinatura(t, idToken)

	tok, err := auth.MintForAssertion(ctx, tampered, odAgent, odClass, []string{odCap})
	if err == nil {
		t.Fatal("ID-token adulterado ACEITE — fail-closed violado")
	}
	if !errors.Is(err, ErrHumanNotAuthenticated) {
		t.Fatalf("erro=%v, quero envolver ErrHumanNotAuthenticated", err)
	}
	if !errors.Is(err, oidc.ErrSignatureInvalid) {
		t.Fatalf("erro=%v, quero envolver oidc.ErrSignatureInvalid (causa concreta)", err)
	}
	if tok.Compact != "" {
		t.Fatal("token nao-vazio numa recusa")
	}
}

// (3) aud errada → RECUSA, envolvendo oidc.ErrAudienceMismatch.
func TestOIDCDirectory_WrongAudienceRefused(t *testing.T) {
	ctx := context.Background()
	idp := newODIDP(t)
	auth := newOIDCAuthority(t, idp)

	idToken := idp.signIDToken(t, "alice@corp.example", func(c map[string]any) {
		c["aud"] = "outro-servico"
	})
	_, err := auth.MintForAssertion(ctx, idToken, odAgent, odClass, []string{odCap})
	if !errors.Is(err, oidc.ErrAudienceMismatch) {
		t.Fatalf("erro=%v, quero envolver oidc.ErrAudienceMismatch", err)
	}
	if !errors.Is(err, ErrHumanNotAuthenticated) {
		t.Fatalf("erro=%v, quero envolver ErrHumanNotAuthenticated", err)
	}
}

// (4) A via só-humanID de um OIDCDirectory é recusada fail-closed: sem asserção não há
// prova. Garante que não se contorna o ID-token via MintForHuman.
func TestOIDCDirectory_AuthenticateWithoutAssertionRefused(t *testing.T) {
	ctx := context.Background()
	idp := newODIDP(t)
	auth := newOIDCAuthority(t, idp)

	// MintForHuman usa a via Authenticate(humanID) — o OIDCDirectory recusa sempre.
	tok, err := auth.MintForHuman(ctx, "alice@corp.example", odAgent, odClass, []string{odCap})
	if err == nil {
		t.Fatal("MintForHuman com OIDCDirectory ACEITE — a via sem-prova devia recusar")
	}
	if !errors.Is(err, ErrAssertionRequired) {
		t.Fatalf("erro=%v, quero envolver ErrAssertionRequired", err)
	}
	if !errors.Is(err, ErrHumanNotAuthenticated) {
		t.Fatalf("erro=%v, quero envolver ErrHumanNotAuthenticated", err)
	}
	if tok.Compact != "" {
		t.Fatal("token nao-vazio numa recusa")
	}

	// Chamada directa ao directório confirma o erro tipado.
	if err := NewOIDCDirectoryMust(t, idp).Authenticate(ctx, "alice@corp.example"); !errors.Is(err, ErrAssertionRequired) {
		t.Fatalf("Authenticate=%v, quero ErrAssertionRequired", err)
	}
}

// NewOIDCDirectoryMust é um helper de teste que constrói um OIDCDirectory ou falha.
func NewOIDCDirectoryMust(t *testing.T, idp *odIDP) *OIDCDirectory {
	t.Helper()
	d, err := NewOIDCDirectory(oidc.Config{
		Issuer: "https://idp.corp.example", Audience: odClientAud,
		JWKSURI: idp.jwksURI(), HTTPClient: idp.server.Client(), Clock: odClock(),
	})
	if err != nil {
		t.Fatalf("NewOIDCDirectory: %v", err)
	}
	return d
}

// (5) AllowlistDirectory continua a ser um double coerente na nova via de asserção
// (demo-grade: a "asserção" é o humanID).
func TestAllowlistDirectory_AssertionDoubleStillWorks(t *testing.T) {
	ctx := context.Background()
	dir := NewAllowlistDirectory("bob")
	got, err := dir.AuthenticateAssertion(ctx, "bob")
	if err != nil {
		t.Fatalf("AuthenticateAssertion(bob): %v", err)
	}
	if got != "bob" {
		t.Fatalf("humanID=%q, quero bob", got)
	}
	if _, err := dir.AuthenticateAssertion(ctx, "mallory"); !errors.Is(err, ErrHumanNotRegistered) {
		t.Fatalf("erro=%v, quero ErrHumanNotRegistered", err)
	}
}

// adulterarAssinatura corrompe a assinatura de um JWT de forma que os BYTES mudem SEMPRE — e
// PROVA-O antes de devolver.
//
// DUAS VERSÕES ANTERIORES ESTAVAM ERRADAS, e a segunda foi PIOR do que a primeira:
//
//  1. `tok[:len(tok)-2] + "AA"` — se a cauda já fosse "AA", devolvia o token INTACTO. ~1 em 4096.
//
//  2. trocar só o ÚLTIMO caractere por um diferente — falha ~25% DAS VEZES. Uma assinatura
//     ed25519 tem 64 bytes = 512 bits, mas os 86 caracteres base64url transportam 516: os
//     últimos 4 bits NÃO SÃO USADOS. O último caractere carrega apenas 2 bits significativos, e
//     o descodificador do Go (não-estrito) ignora os restantes. Trocar 'A'→'B' mexe só em
//     enchimento: os bytes ficam IGUAIS, a assinatura verifica, e o teste acusa o sistema de
//     aceitar um token falsificado. O mesmo vale para RSA-2048 (256 bytes em 342 caracteres).
//
// A lição não é sobre base64. É que uma função auxiliar de teste também precisa de prova: as duas
// versões anteriores pareciam obviamente correctas, e nenhuma verificava o que afirmava fazer.
// Esta verifica, e falha ruidosamente se alguma vez não adulterar.
func adulterarAssinatura(t *testing.T, tok string) string {
	t.Helper()
	i := strings.LastIndex(tok, ".")
	if i < 0 || i == len(tok)-1 {
		t.Fatalf("token sem segmento de assinatura: %q", tok)
	}
	sig := tok[i+1:]
	// O PRIMEIRO caractere do segmento carrega 6 bits significativos — ao contrário do último,
	// que partilha o byte final com bits de enchimento.
	novo := byte('A')
	if sig[0] == 'A' {
		novo = 'B'
	}
	adulterado := tok[:i+1] + string(novo) + sig[1:]

	// CONTROLO INTERNO: os bytes DESCODIFICADOS têm de diferir. Sem isto, um helper que não
	// adultera transforma um teste de segurança num sorteio cujo resultado se lê como
	// "fail-closed violado" — a acusação mais grave que aquele teste sabe fazer.
	a, err1 := base64.RawURLEncoding.DecodeString(sig)
	b, err2 := base64.RawURLEncoding.DecodeString(adulterado[i+1:])
	if err1 != nil || err2 != nil || bytes.Equal(a, b) {
		t.Fatalf("adulterarAssinatura NAO alterou os bytes da assinatura (err=%v/%v) — o teste seria um sorteio", err1, err2)
	}
	return adulterado
}

// TestAdulterarAssinaturaMudaSempre é o controlo do próprio helper, e existe porque DUAS versões
// dele estiveram erradas sem que nada o dissesse.
//
// Corre sobre assinaturas REAIS e muitas: é nelas que os bits de enchimento do base64 aparecem, e
// foi exactamente aí que a versão anterior falhava um quarto das vezes.
func TestAdulterarAssinaturaMudaSempre(t *testing.T) {
	for i := 0; i < 300; i++ {
		_, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte("m")))
		tok := "aGRy.cGF5." + sig
		got := adulterarAssinatura(t, tok)

		a, _ := base64.RawURLEncoding.DecodeString(sig)
		b, _ := base64.RawURLEncoding.DecodeString(got[strings.LastIndex(got, ".")+1:])
		if bytes.Equal(a, b) {
			t.Fatalf("iteracao %d: os BYTES da assinatura nao mudaram — so o enchimento", i)
		}
	}
}
