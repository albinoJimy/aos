package authn_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/identity/delegation"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/pipeline/authn"
)

// t0 é o instante base determinista dos testes.
var t0 = time.Unix(1_700_000_000, 0)

// realIdentity constrói um issuer + verifier REAIS (AOS-005/006) com relógio
// determinista, para exercitar a validação de token verdadeira do estágio.
func realIdentity(t *testing.T, clock func() time.Time) (*identity.Issuer, *identity.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	classes := map[string]identity.ClassPolicy{
		"reader": {TTL: time.Hour, Scope: []string{"model:invoke", "fs:read"}},
	}
	iss, err := identity.NewIssuer("iss-test", priv, classes, identity.WithIssuerClock(clock))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	ver := identity.NewVerifier(
		identity.WithTrustedIssuer("iss-test", pub),
		identity.WithVerifierClock(clock),
	)
	return iss, ver
}

func readerResolver() *authn.StaticAuthority {
	return authn.NewStaticAuthority().
		SetUser("alice", "model:invoke", "admin").
		SetClass("reader", "model:invoke", "fs:read")
}

// TestStage_ValidToken_Allows: um token válido passa e popula o eixo de identidade
// do Exchange (principal/classe/humano/autoridade/cadeia) — a base da atribuição.
func TestStage_ValidToken_Allows(t *testing.T) {
	t.Parallel()
	clock := func() time.Time { return t0 }
	iss, ver := realIdentity(t, clock)
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID: "alice", AgentID: "agent-1", AgentClass: "reader",
		UserAuthority: []string{"model:invoke", "admin"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	pol, _ := authn.LoadPolicy()
	st := authn.New(ver, readerResolver(), pol)

	ex := &pipeline.Exchange{Op: pipeline.OpChat, Principal: tok.Compact}
	if err := st.Process(context.Background(), ex); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ex.PrincipalUser != "alice" || ex.PrincipalAgent != "agent-1" || ex.AgentClass != "reader" {
		t.Fatalf("identidade nao resolvida: %+v", ex)
	}
	if ex.HumanRoot != "human:alice" {
		t.Fatalf("humano responsavel = %q, quer human:alice", ex.HumanRoot)
	}
	// Autoridade efectiva = utilizador ∩ classe = {model:invoke} (admin nao esta na
	// classe; fs:read nao esta no utilizador).
	if len(ex.EffectiveAuthority) != 1 || ex.EffectiveAuthority[0] != "model:invoke" {
		t.Fatalf("autoridade efectiva = %v, quer [model:invoke]", ex.EffectiveAuthority)
	}
	if len(ex.DelegationChain) == 0 || ex.DelegationChain[0].Sub != "human:alice" {
		t.Fatalf("cadeia nao projectada: %+v", ex.DelegationChain)
	}
	if ex.PolicyVersion == "" {
		t.Fatalf("versao da politica nao selada no Exchange")
	}
	// O token bruto NAO deve permanecer em ex.Principal (evita fuga para variancia).
	if ex.Principal == tok.Compact {
		t.Fatalf("token bruto permaneceu em ex.Principal")
	}
	if ex.Principal != "alice/agent-1" {
		t.Fatalf("ex.Principal = %q, quer alice/agent-1", ex.Principal)
	}
}

// TestStage_NoToken_Deny: chamada sem token do principal é recusada fail-closed.
func TestStage_NoToken_Deny(t *testing.T) {
	t.Parallel()
	_, ver := realIdentity(t, func() time.Time { return t0 })
	pol, _ := authn.LoadPolicy()
	st := authn.New(ver, readerResolver(), pol)
	ex := &pipeline.Exchange{Op: pipeline.OpChat, Principal: ""}
	if err := st.Process(context.Background(), ex); !errors.Is(err, authn.ErrNoToken) {
		t.Fatalf("erro = %v, quer ErrNoToken", err)
	}
}

// TestStage_ExpiredToken_Deny: token expirado -> deny fail-closed (via Verifier).
func TestStage_ExpiredToken_Deny(t *testing.T) {
	t.Parallel()
	issueClock := func() time.Time { return t0 }
	iss, _ := realIdentity(t, issueClock)
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID: "alice", AgentID: "agent-1", AgentClass: "reader",
		UserAuthority: []string{"model:invoke"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Verificador sobre a MESMA chave do issuer mas com relógio MUITO depois da
	// expiração (TTL 1h) — exercita a EXPIRAÇÃO, não o emissor desconhecido.
	verSame := identity.NewVerifier(
		identity.WithTrustedIssuer("iss-test", iss.PublicKey()),
		identity.WithVerifierClock(func() time.Time { return t0.Add(2 * time.Hour) }),
	)
	pol, _ := authn.LoadPolicy()
	st := authn.New(verSame, readerResolver(), pol)
	ex := &pipeline.Exchange{Op: pipeline.OpChat, Principal: tok.Compact}
	err = st.Process(context.Background(), ex)
	if !errors.Is(err, authn.ErrTokenRejected) {
		t.Fatalf("erro = %v, quer ErrTokenRejected (expirado)", err)
	}
	if !errors.Is(err, identity.ErrTokenExpired) {
		t.Fatalf("causa = %v, quer identity.ErrTokenExpired", err)
	}
}

// TestStage_PolicyDeny_AuthorityMissingCap: um principal cuja autoridade efectiva
// NÃO inclui model:invoke é recusado (gate de capability + policy default-deny).
func TestStage_PolicyDeny_AuthorityMissingCap(t *testing.T) {
	t.Parallel()
	clock := func() time.Time { return t0 }
	// Classe "reader" concede fs:read; utilizador só tem fs:read -> efectiva
	// {fs:read}, sem model:invoke.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	classes := map[string]identity.ClassPolicy{
		"reader": {TTL: time.Hour, Scope: []string{"fs:read"}},
	}
	iss, _ := identity.NewIssuer("iss-test", priv, classes, identity.WithIssuerClock(clock))
	ver := identity.NewVerifier(identity.WithTrustedIssuer("iss-test", pub), identity.WithVerifierClock(clock))
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID: "alice", AgentID: "agent-1", AgentClass: "reader",
		UserAuthority: []string{"fs:read"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	resolver := authn.NewStaticAuthority().SetUser("alice", "fs:read").SetClass("reader", "fs:read")
	pol, _ := authn.LoadPolicy()
	st := authn.New(ver, resolver, pol)
	ex := &pipeline.Exchange{Op: pipeline.OpChat, Principal: tok.Compact}
	if err := st.Process(context.Background(), ex); !errors.Is(err, authn.ErrPolicyDenied) {
		t.Fatalf("erro = %v, quer ErrPolicyDenied", err)
	}
}

// TestStage_AuthorityEmpty_Deny: utilizador e classe disjuntos -> efectiva vazia.
func TestStage_AuthorityEmpty_Deny(t *testing.T) {
	t.Parallel()
	clock := func() time.Time { return t0 }
	iss, ver := realIdentity(t, clock)
	tok, _ := iss.Issue(context.Background(), identity.IssueRequest{
		UserID: "alice", AgentID: "agent-1", AgentClass: "reader",
		UserAuthority: []string{"model:invoke"},
	})
	// Resolver com utilizador SEM capabilities em comum com a classe.
	resolver := authn.NewStaticAuthority().SetUser("alice", "billing").SetClass("reader", "model:invoke")
	pol, _ := authn.LoadPolicy()
	st := authn.New(ver, resolver, pol)
	ex := &pipeline.Exchange{Op: pipeline.OpChat, Principal: tok.Compact}
	if err := st.Process(context.Background(), ex); !errors.Is(err, authn.ErrAuthorityEmpty) {
		t.Fatalf("erro = %v, quer ErrAuthorityEmpty", err)
	}
}

// --- Ramos de defesa-em-profundidade via Verifier duplo ---

type fakeVerifier struct {
	p   identity.Principal
	err error
}

func (f fakeVerifier) Verify(_ context.Context, _ string) (identity.Principal, error) {
	return f.p, f.err
}

// TestStage_OrphanChain_Deny: um Principal cuja cadeia não enraiza num humano é
// recusado (defesa em profundidade sobre um Verifier comprometido/buggy).
func TestStage_OrphanChain_Deny(t *testing.T) {
	t.Parallel()
	fv := fakeVerifier{p: identity.Principal{
		UserID: "alice", AgentID: "agent-1", AgentClass: "reader",
		Scope:           []string{"model:invoke"},
		DelegationChain: nil, // sem cadeia -> HumanPrincipal falha
	}}
	pol, _ := authn.LoadPolicy()
	st := authn.New(fv, readerResolver(), pol)
	ex := &pipeline.Exchange{Op: pipeline.OpChat, Principal: "qualquer-token"}
	if err := st.Process(context.Background(), ex); !errors.Is(err, authn.ErrNoHumanRoot) {
		t.Fatalf("erro = %v, quer ErrNoHumanRoot", err)
	}
}

// TestStage_ScopeExceedsSeal_Deny: a autoridade efectiva (do resolver) é disjunta
// do escopo SELADO no token -> após reconciliar fica vazia -> deny.
func TestStage_ScopeExceedsSeal_Deny(t *testing.T) {
	t.Parallel()
	chain, err := delegation.NewRoot("human:bob", "agent-x", []string{"other"})
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	fv := fakeVerifier{p: identity.Principal{
		UserID: "bob", AgentID: "agent-x", AgentClass: "c",
		Scope:           []string{"other"}, // token selou {other}
		DelegationChain: chain,
	}}
	// Resolver concede {admin} — disjunto de {other}.
	resolver := authn.NewStaticAuthority().SetUser("bob", "admin").SetClass("c", "admin")
	pol, _ := authn.LoadPolicy()
	st := authn.New(fv, resolver, pol)
	ex := &pipeline.Exchange{Op: pipeline.OpChat, Principal: "qualquer-token"}
	if err := st.Process(context.Background(), ex); !errors.Is(err, authn.ErrScopeExceedsSeal) {
		t.Fatalf("erro = %v, quer ErrScopeExceedsSeal", err)
	}
}

// TestStage_ResolverError_Deny: falha do resolver -> deny fail-closed.
func TestStage_ResolverError_Deny(t *testing.T) {
	t.Parallel()
	chain, _ := delegation.NewRoot("human:bob", "agent-x", []string{"model:invoke"})
	fv := fakeVerifier{p: identity.Principal{
		UserID: "bob", AgentID: "agent-x", AgentClass: "c",
		Scope: []string{"model:invoke"}, DelegationChain: chain,
	}}
	pol, _ := authn.LoadPolicy()
	st := authn.New(fv, errResolver{}, pol)
	ex := &pipeline.Exchange{Op: pipeline.OpChat, Principal: "qualquer-token"}
	if err := st.Process(context.Background(), ex); !errors.Is(err, authn.ErrResolver) {
		t.Fatalf("erro = %v, quer ErrResolver", err)
	}
}

type errResolver struct{}

func (errResolver) UserAuthority(_ context.Context, _ string) ([]string, error) {
	return nil, errors.New("directorio indisponivel")
}
func (errResolver) ClassAuthority(_ context.Context, _ string) ([]string, error) {
	return nil, errors.New("directorio indisponivel")
}

// TestStage_NotConfigured_Deny: um estágio sem verificador/resolver/política
// recusa toda a chamada (nunca fail-open).
func TestStage_NotConfigured_Deny(t *testing.T) {
	t.Parallel()
	st := authn.New(nil, nil, nil)
	ex := &pipeline.Exchange{Op: pipeline.OpChat, Principal: "x"}
	if err := st.Process(context.Background(), ex); !errors.Is(err, authn.ErrTokenRejected) {
		t.Fatalf("erro = %v, quer ErrTokenRejected", err)
	}
}
