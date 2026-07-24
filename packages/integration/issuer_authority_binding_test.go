package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/aos-ref/integration/oidc"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/substrate/eventstore"
)

// Este ficheiro prova o BINDING humano↔NHI AUDITÁVEL fim-a-fim na autoridade de
// identidade (AOS-176, frente 3 do D4 / ADR-003): o método/autoridade de autenticação
// (allowlist ou OIDC) entra no registo auditável do binding — SEM o token/asserção cru
// —, resolvel por [identity.BindingAudit].

// newAuthorityWithStore constrói uma [IssuerAuthority] (allowlist demo) cujo Issuer
// grava as emissões no store dado, para auditar o binding.
func newAuthorityWithStore(t *testing.T, store eventstore.EventStore) *IssuerAuthority {
	t.Helper()
	auth, err := NewIssuerAuthority(AuthorityConfig{
		IssuerID:  iaIssuerID,
		Classes:   iaClasses(),
		Directory: NewAllowlistDirectory(iaHuman),
		IssuerOptions: []identity.IssuerOption{
			identity.WithIssuerClock(iaClock()),
			identity.WithEventStore(store),
		},
	})
	if err != nil {
		t.Fatalf("NewIssuerAuthority: %v", err)
	}
	return auth
}

// (1) MintForHuman via allowlist ⇒ o binding auditável regista o método "allowlist"
// (o rótulo de [AllowlistDirectory.AuthorizationMethod]) + humano-raiz + agente.
func TestMintForHuman_BindingRecordsAllowlistMethod(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	auth := newAuthorityWithStore(t, store)
	tok, err := auth.MintForHuman(ctx, iaHuman, iaAgent, iaClass, []string{iaCapRead})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}

	rec, err := identity.NewBindingAudit(store).ResolveByJTI(ctx, tok.Claims.JTI)
	if err != nil {
		t.Fatalf("ResolveByJTI: %v", err)
	}
	if rec.Human != "human:"+iaHuman {
		t.Errorf("Human=%q, quero human:%s", rec.Human, iaHuman)
	}
	if rec.AgentID != iaAgent {
		t.Errorf("AgentID=%q, quero %q", rec.AgentID, iaAgent)
	}
	if rec.AuthMethod != "allowlist" {
		t.Errorf("AuthMethod=%q, quero allowlist", rec.AuthMethod)
	}
}

// (2) MintForAssertion via OIDCDirectory real ⇒ o binding regista o método
// "oidc:<issuer>" e o humano-raiz é o sub VERIFICADO; o ID-token CRU NUNCA aparece no
// registo auditável (sem segredos/PII).
func TestMintForAssertion_BindingRecordsOIDCMethod_NoRawToken(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	idp := newODIDP(t)
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
		IssuerID:  odIssuerID,
		Classes:   odClasses(),
		Directory: dir,
		IssuerOptions: []identity.IssuerOption{
			identity.WithIssuerClock(odClock()),
			identity.WithEventStore(store),
		},
	})
	if err != nil {
		t.Fatalf("NewIssuerAuthority: %v", err)
	}

	idToken := idp.signIDToken(t, "alice@corp.example", nil)
	tok, err := auth.MintForAssertion(ctx, idToken, odAgent, odClass, []string{odCap})
	if err != nil {
		t.Fatalf("MintForAssertion: %v", err)
	}

	rec, err := identity.NewBindingAudit(store).ResolveByJTI(ctx, tok.Claims.JTI)
	if err != nil {
		t.Fatalf("ResolveByJTI: %v", err)
	}
	// Quem autorizou: o sub verificado, na raiz da cadeia.
	if rec.Human != "human:alice@corp.example" {
		t.Errorf("Human=%q, quero human:alice@corp.example", rec.Human)
	}
	// Por que autoridade: OIDC contra o issuer configurado.
	if want := "oidc:https://idp.corp.example"; rec.AuthMethod != want {
		t.Errorf("AuthMethod=%q, quero %q", rec.AuthMethod, want)
	}

	// SEM segredos/PII: o ID-token CRU nunca entra no registo auditável.
	events, err := store.Read(ctx, "identity", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, ev := range events {
		if strings.Contains(string(ev.Payload), idToken) {
			t.Fatal("o registo de binding contem o ID-token OIDC cru — fuga de segredo")
		}
	}
	// E também não contém o bearer NHI nem a sua assinatura.
	raw := string(events[0].Payload)
	if strings.Contains(raw, tok.Compact) {
		t.Error("o registo de binding contem o token bearer NHI")
	}
}

// (3) O rótulo do OIDCDirectory reflecte o issuer configurado (fonte do contexto de
// autorização gravado no binding).
func TestOIDCDirectory_AuthorizationMethodLabel(t *testing.T) {
	idp := newODIDP(t)
	dir := NewOIDCDirectoryMust(t, idp)
	if want := "oidc:https://idp.corp.example"; dir.AuthorizationMethod() != want {
		t.Fatalf("AuthorizationMethod=%q, quero %q", dir.AuthorizationMethod(), want)
	}

	// E o double allowlist reporta "allowlist".
	if got := NewAllowlistDirectory("bob").AuthorizationMethod(); got != "allowlist" {
		t.Fatalf("AllowlistDirectory.AuthorizationMethod=%q, quero allowlist", got)
	}
}
