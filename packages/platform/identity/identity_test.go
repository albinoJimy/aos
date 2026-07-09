package identity

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const testIssuerID = "aos-issuer-test"

var baseTime = time.Unix(1_700_000_000, 0).UTC()

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func testKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// newIssuer devolve um emissor determinístico (relógio fixo, jti sequencial) e a
// sua chave pública.
func newIssuer(t *testing.T, classes map[string]ClassPolicy, store eventstore.EventStore) (*Issuer, ed25519.PublicKey) {
	t.Helper()
	pub, priv := testKeys(t)
	var n int
	iss, err := NewIssuer(testIssuerID, priv, classes,
		WithIssuerClock(fixedClock(baseTime)),
		WithIDSource(func() string { n++; return "jti-" + string(rune('0'+n)) }),
		WithEventStore(store),
	)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return iss, pub
}

func researcherClasses() map[string]ClassPolicy {
	return map[string]ClassPolicy{
		"researcher": {TTL: 5 * time.Minute, Scope: []string{"cap:http.get", "cap:fs.read"}},
	}
}

// ---------------------------------------------------------------------------
// Emissão + verificação de NHI válida
// ---------------------------------------------------------------------------

func TestIssueAndVerify_Valid(t *testing.T) {
	t.Parallel()
	iss, pub := newIssuer(t, researcherClasses(), nil)

	tok, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "human:alice", AgentID: "agt-1", AgentClass: "researcher",
		PolicyRef:     "policy://researcher@1",
		UserAuthority: []string{"cap:http.get", "cap:fs.read"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Claims correctos.
	if tok.Claims.UserID != "human:alice" || tok.Claims.AgentID != "agt-1" {
		t.Errorf("claims (user/agent) errados: %+v", tok.Claims)
	}
	if tok.Claims.AgentClass != "researcher" || tok.Claims.PolicyRef != "policy://researcher@1" {
		t.Errorf("claims (class/policy) errados: %+v", tok.Claims)
	}
	if tok.Claims.Issuer != testIssuerID || tok.Claims.JTI == "" {
		t.Errorf("claims (iss/jti) errados: %+v", tok.Claims)
	}
	if tok.Claims.Expiry-tok.Claims.IssuedAt != int64((5 * time.Minute).Seconds()) {
		t.Errorf("TTL nao respeitado: iat=%d exp=%d", tok.Claims.IssuedAt, tok.Claims.Expiry)
	}

	// Verificação resolve o Principal.
	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	p, err := v.Verify(context.Background(), tok.Compact)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.UserID != "human:alice" || p.AgentID != "agt-1" || p.AgentClass != "researcher" {
		t.Errorf("principal resolvido errado: %+v", p)
	}
	if !p.Allows("cap:http.get") || p.Allows("cap:http.post") {
		t.Errorf("escopo do principal errado: %+v", p.Scope)
	}
}

// ---------------------------------------------------------------------------
// Autoridade = utilizador ∩ classe (nunca alarga)
// ---------------------------------------------------------------------------

func TestIssue_UserIntersectClass(t *testing.T) {
	t.Parallel()
	iss, _ := newIssuer(t, researcherClasses(), nil)

	tok, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "u", AgentID: "a", AgentClass: "researcher",
		// utilizador tem http.get e http.post; a classe só concede http.get e fs.read.
		UserAuthority: []string{"cap:http.get", "cap:http.post"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Intersecção = {http.get}. Nem o que só o utilizador tem (http.post) nem o
	// que só a classe tem (fs.read) entram.
	if len(tok.Claims.Scope) != 1 || tok.Claims.Scope[0] != "cap:http.get" {
		t.Fatalf("intersecção user∩classe errada: %v", tok.Claims.Scope)
	}
}

// ---------------------------------------------------------------------------
// on-behalf-of: escopo de filho ⊆ pai (sem escalada)
// ---------------------------------------------------------------------------

func TestIssue_OnBehalfOf_ChildSubsetParent(t *testing.T) {
	t.Parallel()
	iss, _ := newIssuer(t, researcherClasses(), nil)
	ctx := context.Background()

	// Utilizador e classe permitiriam {http.get, fs.read}; mas o pai só tem
	// {http.get}. O filho não pode alargar para além do pai.
	child, err := iss.Issue(ctx, IssueRequest{
		UserID: "u", AgentID: "child", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get", "cap:fs.read"},
		ParentScope:   []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue filho: %v", err)
	}
	if len(child.Claims.Scope) != 1 || child.Claims.Scope[0] != "cap:http.get" {
		t.Fatalf("escopo do filho devia ser ⊆ pai {http.get}, obtive %v", child.Claims.Scope)
	}

	// Tentativa de escalada: pai vazio ⇒ filho vazio (não herda nada).
	empty, err := iss.Issue(ctx, IssueRequest{
		UserID: "u", AgentID: "child2", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get", "cap:fs.read"},
		ParentScope:   []string{},
	})
	if err != nil {
		t.Fatalf("Issue filho2: %v", err)
	}
	if len(empty.Claims.Scope) != 0 {
		t.Fatalf("pai vazio devia dar filho vazio, obtive %v", empty.Claims.Scope)
	}
}

// ---------------------------------------------------------------------------
// Rejeições fail-closed do Verifier
// ---------------------------------------------------------------------------

func TestVerify_Rejections(t *testing.T) {
	t.Parallel()
	pub, priv := testKeys(t)
	kid := testIssuerID

	// Um token válido de base (para depois adulterar).
	validClaims := Claims{
		UserID: "u", AgentID: "a", AgentClass: "researcher", Scope: []string{"cap:x"},
		Issuer: testIssuerID, IssuedAt: baseTime.Unix(), NotBefore: baseTime.Unix(),
		Expiry: baseTime.Add(5 * time.Minute).Unix(), JTI: "jti-valid",
	}
	validCompact, err := signToken(priv, kid, validClaims)
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}

	// alg=none confusion: header forjado com alg none e assinatura bogus.
	noneHeader := b64enc([]byte(`{"alg":"none","typ":"NHI"}`))
	pb := strings.SplitN(validCompact, ".", 3)[1]
	noneToken := noneHeader + "." + pb + "." + b64enc([]byte("bogus"))

	// Adulteração: muta o scope mas mantém a assinatura original.
	tampered := tamperClaims(t, priv, kid, validClaims, func(c *Claims) { c.Scope = []string{"cap:admin"} })

	// nbf no futuro.
	notYet, _ := signToken(priv, kid, Claims{
		UserID: "u", AgentID: "a", Issuer: testIssuerID,
		IssuedAt: baseTime.Unix(), NotBefore: baseTime.Add(time.Hour).Unix(),
		Expiry: baseTime.Add(2 * time.Hour).Unix(), JTI: "jti-future",
	})

	// Assinado por OUTRA chave, mas com iss reconhecido (emissor errado / chave errada).
	_, otherPriv := testKeys(t)
	wrongKey, _ := signToken(otherPriv, kid, validClaims)

	// iss desconhecido (sem trust anchor).
	unknownIss, _ := signToken(priv, kid, Claims{
		UserID: "u", AgentID: "a", Issuer: "iss-desconhecido",
		IssuedAt: baseTime.Unix(), NotBefore: baseTime.Unix(),
		Expiry: baseTime.Add(time.Hour).Unix(), JTI: "jti-u",
	})

	tests := []struct {
		name    string
		compact string
		clock   time.Time
		revoke  string // jti a revogar antes de verificar
		wantErr *IdentityError
	}{
		{"malformado_dois_segmentos", "aaa.bbb", baseTime, "", ErrTokenMalformed},
		{"malformado_base64", "!!!.$$$.@@@", baseTime, "", ErrTokenMalformed},
		{"alg_none_confusion", noneToken, baseTime, "", ErrUnsupportedAlg},
		{"assinatura_adulterada", tampered, baseTime, "", ErrSignatureInvalid},
		{"chave_errada", wrongKey, baseTime, "", ErrSignatureInvalid},
		{"emissor_desconhecido", unknownIss, baseTime, "", ErrUnknownIssuer},
		{"expirado", validCompact, baseTime.Add(10 * time.Minute), "", ErrTokenExpired},
		{"ainda_nao_valido", notYet, baseTime, "", ErrTokenNotYetValid},
		{"revogado", validCompact, baseTime.Add(time.Minute), "jti-valid", ErrTokenRevoked},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rev := NewRevocations(nil)
			if tc.revoke != "" {
				if err := rev.Revoke(context.Background(), tc.revoke); err != nil {
					t.Fatalf("Revoke: %v", err)
				}
			}
			v := NewVerifier(
				WithTrustedIssuer(testIssuerID, pub),
				WithVerifierClock(fixedClock(tc.clock)),
				WithRevocations(rev),
			)
			_, err := v.Verify(context.Background(), tc.compact)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("erro=%v, esperava sentinela %v", err, tc.wantErr)
			}
		})
	}
}

// tamperClaims produz um token cujos claims foram mutados APÓS a assinatura
// (assinatura original mantida ⇒ deve falhar a verificação de assinatura).
func tamperClaims(t *testing.T, priv ed25519.PrivateKey, kid string, c Claims, mut func(*Claims)) string {
	t.Helper()
	orig, err := signToken(priv, kid, c)
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}
	parts := strings.SplitN(orig, ".", 3)
	mut(&c)
	pb, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return parts[0] + "." + b64enc(pb) + "." + parts[2]
}

// ---------------------------------------------------------------------------
// Emissão inválida
// ---------------------------------------------------------------------------

func TestIssue_InvalidRequests(t *testing.T) {
	t.Parallel()
	iss, _ := newIssuer(t, researcherClasses(), nil)
	ctx := context.Background()
	tests := []struct {
		name string
		req  IssueRequest
		want *IdentityError
	}{
		{"sem_user", IssueRequest{AgentID: "a", AgentClass: "researcher"}, ErrInvalidRequest},
		{"sem_agent", IssueRequest{UserID: "u", AgentClass: "researcher"}, ErrInvalidRequest},
		{"classe_desconhecida", IssueRequest{UserID: "u", AgentID: "a", AgentClass: "ghost"}, ErrUnknownClass},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := iss.Issue(ctx, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("erro=%v, esperava %v", err, tc.want)
			}
		})
	}
}

func TestNewIssuer_InvalidKey(t *testing.T) {
	t.Parallel()
	if _, err := NewIssuer("", ed25519.PrivateKey{}, nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("esperava ErrInvalidRequest, obtive %v", err)
	}
	_, priv := testKeys(t)
	if _, err := NewIssuer("", priv, nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("iss vazio devia dar ErrInvalidRequest, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Revogação: registo em memória + idempotência
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// jti: aleatoriedade e fail-closed em falha do CSPRNG (Q-01/AOS005-C3)
// ---------------------------------------------------------------------------

func TestRandomJTI_UniqueAndNonEmpty(t *testing.T) {
	t.Parallel()
	a, err := randomJTI()
	if err != nil {
		t.Fatalf("randomJTI: %v", err)
	}
	b, err := randomJTI()
	if err != nil {
		t.Fatalf("randomJTI: %v", err)
	}
	// 16 bytes em base64url raw = 22 chars; nunca a constante de zeros.
	if len(a) != 22 || a == "AAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("jti degenerado: %q", a)
	}
	if a == b {
		t.Fatalf("dois jti consecutivos colidiram: %q", a)
	}
}

// Uma falha da fonte de jti tem de negar a emissão (fail-closed): NUNCA se emite
// uma NHI sem jti único.
func TestIssue_JTISourceFailure_FailClosed(t *testing.T) {
	t.Parallel()
	iss, _ := newIssuer(t, researcherClasses(), nil)
	sentinel := errors.New("csprng indisponivel")
	iss.newJTI = func() (string, error) { return "", sentinel }

	_, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "u", AgentID: "a", AgentClass: "researcher",
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("falha do CSPRNG devia negar a emissao, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Verifier: defesa-em-profundidade (typ e user/agent não-vazios) (Q-02)
// ---------------------------------------------------------------------------

func signWithHeader(t *testing.T, priv ed25519.PrivateKey, h header, c Claims) string {
	t.Helper()
	hb, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	pb, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	si := b64enc(hb) + "." + b64enc(pb)
	sig := ed25519.Sign(priv, []byte(si))
	return si + "." + b64enc(sig)
}

func TestVerify_DefenseInDepth(t *testing.T) {
	t.Parallel()
	pub, priv := testKeys(t)

	base := Claims{
		UserID: "u", AgentID: "a", AgentClass: "researcher", Scope: []string{"cap:x"},
		Issuer: testIssuerID, IssuedAt: baseTime.Unix(), NotBefore: baseTime.Unix(),
		Expiry: baseTime.Add(5 * time.Minute).Unix(), JTI: "jti-did",
	}

	// typ errado (assinatura válida): tem de ser rejeitado por defesa-em-profundidade.
	wrongTyp := signWithHeader(t, priv, header{Alg: algEdDSA, Typ: "JWT", Kid: testIssuerID}, base)

	// user_id vazio (assinatura válida, typ correcto).
	noUser := base
	noUser.UserID = ""
	emptyUser := signWithHeader(t, priv, header{Alg: algEdDSA, Typ: typNHI, Kid: testIssuerID}, noUser)

	// agent_id vazio.
	noAgent := base
	noAgent.AgentID = ""
	emptyAgent := signWithHeader(t, priv, header{Alg: algEdDSA, Typ: typNHI, Kid: testIssuerID}, noAgent)

	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	for _, tc := range []struct {
		name    string
		compact string
	}{
		{"typ_errado", wrongTyp},
		{"user_vazio", emptyUser},
		{"agent_vazio", emptyAgent},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := v.Verify(context.Background(), tc.compact); !errors.Is(err, ErrTokenMalformed) {
				t.Fatalf("erro=%v, esperava ErrTokenMalformed", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Verifier: RevocationChecker de tipo-nil não entra em panic (Q-04)
// ---------------------------------------------------------------------------

func TestVerify_NilTypedRevocations_NoPanic(t *testing.T) {
	t.Parallel()
	iss, pub := newIssuer(t, researcherClasses(), nil)
	tok, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "u", AgentID: "a", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// *Revocations nil embrulhado em interface não-nil: o guard != nil no Verifier
	// passa; sem a protecção em IsRevoked, IsRevoked entraria em panic.
	v := NewVerifier(
		WithTrustedIssuer(testIssuerID, pub),
		WithVerifierClock(fixedClock(baseTime.Add(time.Minute))),
		WithRevocations((*Revocations)(nil)),
	)
	p, err := v.Verify(context.Background(), tok.Compact)
	if err != nil {
		t.Fatalf("Verify com revocations tipo-nil devia funcionar, obtive %v", err)
	}
	if p.AgentID != "a" {
		t.Fatalf("principal errado: %+v", p)
	}

	// IsRevoked directamente sobre receiver nil também não entra em panic.
	if ok, err := (*Revocations)(nil).IsRevoked(context.Background(), "jti-x"); ok || err != nil {
		t.Fatalf("IsRevoked(nil) = (%v, %v), esperava (false, nil)", ok, err)
	}
}

func TestRevocations_Behaviour(t *testing.T) {
	t.Parallel()
	rev := NewRevocations(nil)
	ctx := context.Background()

	if err := rev.Revoke(ctx, ""); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("jti vazio devia dar ErrInvalidRequest, obtive %v", err)
	}
	if ok, _ := rev.IsRevoked(ctx, "jti-x"); ok {
		t.Error("jti-x nao devia estar revogado")
	}
	if err := rev.Revoke(ctx, "jti-x"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// Idempotente.
	if err := rev.Revoke(ctx, "jti-x"); err != nil {
		t.Fatalf("Revoke idempotente: %v", err)
	}
	if ok, _ := rev.IsRevoked(ctx, "jti-x"); !ok {
		t.Error("jti-x devia estar revogado")
	}
}
