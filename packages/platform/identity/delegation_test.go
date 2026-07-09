package identity

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/identity/delegation"
	"github.com/aos-ref/substrate/eventstore"
)

// delegationClasses configura três classes com escopos-máximos decrescentes,
// para exercitar a propagação e o estreitamento pai→filho→neto.
func delegationClasses() map[string]ClassPolicy {
	return map[string]ClassPolicy{
		"orchestrator": {TTL: 10 * time.Minute, Scope: []string{"cap:http.get", "cap:fs.read", "cap:db.read"}},
		"planner":      {TTL: 10 * time.Minute, Scope: []string{"cap:http.get", "cap:fs.read", "cap:db.read"}},
		"worker":       {TTL: 10 * time.Minute, Scope: []string{"cap:http.get", "cap:fs.read", "cap:db.read"}},
	}
}

// ---------------------------------------------------------------------------
// Propagação pai→filho→neto: a cadeia resolve à raiz human:*
// ---------------------------------------------------------------------------

func TestIssueChild_PropagationToHuman(t *testing.T) {
	t.Parallel()
	iss, pub := newIssuer(t, delegationClasses(), nil)
	ctx := context.Background()

	// Raiz: orquestrador on-behalf-of human:alice.
	root, err := iss.Issue(ctx, IssueRequest{
		UserID: "human:alice", AgentID: "orchestrator", AgentClass: "orchestrator",
		UserAuthority: []string{"cap:http.get", "cap:fs.read", "cap:db.read"},
	})
	if err != nil {
		t.Fatalf("Issue raiz: %v", err)
	}

	// Filho: planner, estreita para {http.get, fs.read}.
	child, err := iss.IssueChild(ctx, root.Compact, ChildRequest{
		AgentID: "planner", AgentClass: "planner",
		Authority: []string{"cap:http.get", "cap:fs.read"},
	})
	if err != nil {
		t.Fatalf("IssueChild planner: %v", err)
	}

	// Neto: worker, estreita para {http.get}.
	grand, err := iss.IssueChild(ctx, child.Compact, ChildRequest{
		AgentID: "worker", AgentClass: "worker",
		Authority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("IssueChild worker: %v", err)
	}

	// A cadeia do neto tem 3 elos e resolve até human:alice.
	if len(grand.Claims.DelegationChain) != 3 {
		t.Fatalf("esperava 3 elos na cadeia do neto, obtive %d", len(grand.Claims.DelegationChain))
	}
	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	p, err := v.Verify(ctx, grand.Compact)
	if err != nil {
		t.Fatalf("Verify neto: %v", err)
	}
	human, err := p.HumanPrincipal()
	if err != nil {
		t.Fatalf("HumanPrincipal: %v", err)
	}
	if human != "human:alice" {
		t.Fatalf("human_principal=%q, esperava human:alice", human)
	}
	// Autoridade estreitou monotonicamente ao descer.
	if !p.Allows("cap:http.get") || p.Allows("cap:fs.read") || p.Allows("cap:db.read") {
		t.Fatalf("escopo do neto devia ser so {http.get}, obtive %v", p.Scope)
	}
	// Encadeamento correcto (sub de cada elo = act_as do anterior).
	c := p.DelegationChain
	if c[0].Sub != "human:alice" || c[1].Sub != "orchestrator" || c[2].Sub != "planner" {
		t.Fatalf("encadeamento errado: %+v", c)
	}
	if c[2].ActAs != "worker" {
		t.Fatalf("folha errada: %+v", c[2])
	}
}

// ---------------------------------------------------------------------------
// Recusa de escopo alargado no filho (escalada ⇒ deny)
// ---------------------------------------------------------------------------

func TestIssueChild_EscalationRejected(t *testing.T) {
	t.Parallel()
	iss, _ := newIssuer(t, delegationClasses(), nil)
	ctx := context.Background()

	// Pai só tem {http.get}.
	root, err := iss.Issue(ctx, IssueRequest{
		UserID: "human:alice", AgentID: "orchestrator", AgentClass: "orchestrator",
		UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue raiz: %v", err)
	}

	// Filho pede {http.get, db.read}: db.read não está na folha do pai ⇒ escalada.
	_, err = iss.IssueChild(ctx, root.Compact, ChildRequest{
		AgentID: "planner", AgentClass: "planner",
		Authority: []string{"cap:http.get", "cap:db.read"},
	})
	if !errors.Is(err, ErrDelegationInvalid) {
		t.Fatalf("escalada devia dar ErrDelegationInvalid, obtive %v", err)
	}
	if !errors.Is(err, delegation.ErrScopeEscalation) {
		t.Fatalf("erro devia envolver ErrScopeEscalation, obtive %v", err)
	}
}

// Um pai expirado não pode gerar filhos (fail-closed).
func TestIssueChild_ExpiredParentRejected(t *testing.T) {
	t.Parallel()
	iss, _ := newIssuer(t, delegationClasses(), nil)
	ctx := context.Background()
	root, err := iss.Issue(ctx, IssueRequest{
		UserID: "human:alice", AgentID: "orchestrator", AgentClass: "orchestrator",
		UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue raiz: %v", err)
	}
	// Avança o relógio do emissor para além do TTL do pai (10min).
	iss.now = fixedClock(baseTime.Add(20 * time.Minute))
	_, err = iss.IssueChild(ctx, root.Compact, ChildRequest{
		AgentID: "planner", AgentClass: "planner", Authority: []string{"cap:http.get"},
	})
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("pai expirado devia dar ErrTokenExpired, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Detecção de cadeia órfã: raiz não-humana ⇒ Verify nega
// ---------------------------------------------------------------------------

// mintOrphanToken forja um token assinado pelo emissor de teste cuja cadeia de
// delegação tem uma raiz NÃO-humana (órfã). Serve para provar que o Verifier
// nega mesmo um token com assinatura válida se a cadeia não resolver até humano.
func mintOrphanToken(t *testing.T, priv []byte) string {
	t.Helper()
	orphan := delegation.Chain{
		{Sub: "agt-root", ActAs: "agt-1", Authority: []string{"cap:http.get"}, Depth: 0},
	}
	claims := Claims{
		UserID: "agt-root", AgentID: "agt-1", AgentClass: "worker",
		Scope: []string{"cap:http.get"}, Issuer: testIssuerID,
		IssuedAt: baseTime.Unix(), NotBefore: baseTime.Unix(),
		Expiry: baseTime.Add(5 * time.Minute).Unix(), JTI: "jti-orphan",
		DelegationChain: orphan,
	}
	compact, err := signToken(priv, testIssuerID, claims)
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}
	return compact
}

func TestVerify_OrphanChainRejected(t *testing.T) {
	t.Parallel()
	pub, priv := testKeys(t)
	compact := mintOrphanToken(t, priv)

	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	_, err := v.Verify(context.Background(), compact)
	if !errors.Is(err, ErrDelegationInvalid) {
		t.Fatalf("cadeia orfa devia dar ErrDelegationInvalid, obtive %v", err)
	}
	if !errors.Is(err, delegation.ErrOrphanChain) {
		t.Fatalf("erro devia envolver ErrOrphanChain, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Integração RM: cadeia órfã ⇒ RM nega + audita (fail-closed)
// ---------------------------------------------------------------------------

func TestRM_OrphanChain_DeniedAndAudited(t *testing.T) {
	t.Parallel()
	pub, priv := testKeys(t)
	compact := mintOrphanToken(t, priv)

	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	m, store := buildRM(t, v)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	call := rmCall()
	call.Credential = compact
	d, err := m.Mediate(ctx, call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny || d.DeniedBy != "identity" {
		t.Fatalf("cadeia orfa devia negar em identity, obtive Effect=%q DeniedBy=%q", d.Effect, d.DeniedBy)
	}
	assertDeniedAudited(t, store, "run-1", "step-1")
}

// ---------------------------------------------------------------------------
// Reconstrução de autoria a partir de um evento de tool call REAL
// ---------------------------------------------------------------------------

func TestAuthorshipReconstruction_FromRealEvent(t *testing.T) {
	t.Parallel()
	iss, pub := newIssuer(t, delegationClasses(), nil)
	ctx := context.Background()

	// human:alice → orchestrator → planner.
	root, err := iss.Issue(ctx, IssueRequest{
		UserID: "human:alice", AgentID: "orchestrator", AgentClass: "orchestrator",
		UserAuthority: []string{"cap:http.get", "cap:fs.read"},
	})
	if err != nil {
		t.Fatalf("Issue raiz: %v", err)
	}
	child, err := iss.IssueChild(ctx, root.Compact, ChildRequest{
		AgentID: "planner", AgentClass: "planner", Authority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("IssueChild: %v", err)
	}

	// O planner faz uma tool call mediada pelo RM; o evento vai ao Event Store real.
	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	m, store := buildRM(t, v)
	defer func() { _ = store.Close() }()

	call := rmCall()
	call.Credential = child.Compact
	d, err := m.Mediate(ctx, call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if !d.Permitted() {
		t.Fatalf("NHI valida devia permitir, obtive Effect=%q reason=%q", d.Effect, d.Reason)
	}

	// Lê o evento de tool call do Event Store e reconstrói "quem autorizou".
	events, err := store.Read(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 1 || events[0].Type != rm.EventTypeMediated {
		t.Fatalf("esperava 1 evento mediated, obtive %+v", events)
	}
	ev := events[0]

	// A cadeia completa (2 elos) foi registada no Producer do evento.
	if len(ev.Producer.DelegationChain) != 2 {
		t.Fatalf("esperava cadeia de 2 elos no evento, obtive %+v", ev.Producer.DelegationChain)
	}

	// Reconstrução de autoria: o autor responsável é o humano na raiz.
	author, err := AuthorFromEvent(ev)
	if err != nil {
		t.Fatalf("AuthorFromEvent: %v", err)
	}
	if author != "human:alice" {
		t.Fatalf("autor reconstruido=%q, esperava human:alice", author)
	}

	// A cadeia também está no payload da mediação (alteração aditiva AOS-006).
	var payload struct {
		Principal struct {
			DelegationChain []struct {
				Sub   string `json:"sub"`
				ActAs string `json:"act_as"`
			} `json:"delegation_chain"`
		} `json:"principal"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.Principal.DelegationChain) != 2 || payload.Principal.DelegationChain[0].Sub != "human:alice" {
		t.Fatalf("cadeia no payload errada: %+v", payload.Principal.DelegationChain)
	}
}

// ---------------------------------------------------------------------------
// IssueChild: rejeições de pedido e de token pai (fail-closed)
// ---------------------------------------------------------------------------

func TestIssueChild_Rejections(t *testing.T) {
	t.Parallel()
	iss, _ := newIssuer(t, delegationClasses(), nil)
	ctx := context.Background()
	good, err := iss.Issue(ctx, IssueRequest{
		UserID: "human:alice", AgentID: "orchestrator", AgentClass: "orchestrator",
		UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue raiz: %v", err)
	}

	// Emissor com OUTRA chave, para forjar um pai com iss reconhecido mas
	// assinatura que não valida contra a chave do nosso emissor.
	_, otherPriv := testKeys(t)
	wrongKeyParent, err := signToken(otherPriv, testIssuerID, good.Claims)
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}

	tests := []struct {
		name   string
		parent string
		req    ChildRequest
		want   *IdentityError
	}{
		{"sem_agent_id", good.Compact, ChildRequest{AgentClass: "planner"}, ErrInvalidRequest},
		{"classe_desconhecida", good.Compact, ChildRequest{AgentID: "x", AgentClass: "ghost"}, ErrUnknownClass},
		{"pai_malformado", "aaa.bbb", ChildRequest{AgentID: "x", AgentClass: "planner"}, ErrTokenMalformed},
		{"pai_chave_errada", wrongKeyParent, ChildRequest{AgentID: "x", AgentClass: "planner"}, ErrSignatureInvalid},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := iss.IssueChild(ctx, tc.parent, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("erro=%v, esperava %v", err, tc.want)
			}
		})
	}
}

// Um pai com iss desconhecido é rejeitado na verificação de emissão do filho.
func TestIssueChild_UnknownIssuerParent(t *testing.T) {
	t.Parallel()
	iss, _ := newIssuer(t, delegationClasses(), nil)
	ctx := context.Background()
	_, priv := testKeys(t)
	claims := Claims{
		UserID: "human:alice", AgentID: "orchestrator", AgentClass: "orchestrator",
		Scope: []string{"cap:http.get"}, Issuer: "iss-alheio",
		IssuedAt: baseTime.Unix(), NotBefore: baseTime.Unix(),
		Expiry: baseTime.Add(5 * time.Minute).Unix(), JTI: "jti-x",
	}
	foreign, err := signToken(priv, "iss-alheio", claims)
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}
	if _, err := iss.IssueChild(ctx, foreign, ChildRequest{AgentID: "x", AgentClass: "planner", Authority: []string{"cap:http.get"}}); !errors.Is(err, ErrUnknownIssuer) {
		t.Fatalf("pai de emissor alheio devia dar ErrUnknownIssuer, obtive %v", err)
	}
}

// DelegationError.Error() formata código + mensagem.
func TestDelegationError_String(t *testing.T) {
	t.Parallel()
	if got := delegation.ErrOrphanChain.Error(); got == "" || got[:2] != "E_" {
		t.Fatalf("Error() inesperado: %q", got)
	}
}

// Invariantes de atribuição do Verifier (AOS-006): um emissor confiável
// comprometido/buggy que sele um token cuja RAIZ diverge do claims.UserID, ou
// cujo escopo efectivo EXCEDE a autoridade selada na folha, é NEGADO
// fail-closed. Ambos os tokens são validamente assinados pela chave do emissor
// (cenário coberto pelo bloco de defesa-em-profundidade do Verifier).
func TestVerify_AttributionInvariants(t *testing.T) {
	t.Parallel()
	pub, priv := testKeys(t)
	kid := testIssuerID
	ctx := context.Background()

	sign := func(c Claims) string {
		s, err := signToken(priv, kid, c)
		if err != nil {
			t.Fatalf("signToken: %v", err)
		}
		return s
	}
	claims := func(chain delegation.Chain, userID, agentID string, scope []string) Claims {
		return Claims{
			UserID: userID, AgentID: agentID, AgentClass: "researcher", Scope: scope,
			Issuer: testIssuerID, IssuedAt: baseTime.Unix(), NotBefore: baseTime.Unix(),
			Expiry: baseTime.Add(5 * time.Minute).Unix(), JTI: "jti-attr",
			DelegationChain: chain,
		}
	}

	rootBob, err := delegation.NewRoot("human:bob", "a", []string{"cap:http.get"})
	if err != nil {
		t.Fatalf("NewRoot bob: %v", err)
	}
	rootAlice, err := delegation.NewRoot("human:alice", "a", []string{"cap:http.get"})
	if err != nil {
		t.Fatalf("NewRoot alice: %v", err)
	}

	// (a) raiz da cadeia (human:bob) diverge do claims.UserID (human:alice).
	divergentRoot := sign(claims(rootBob, "human:alice", "a", []string{"cap:http.get"}))
	// (b) escopo do token (cap:admin) excede a autoridade selada na folha.
	scopeExceeds := sign(claims(rootAlice, "human:alice", "a", []string{"cap:http.get", "cap:admin"}))
	// Coerente: raiz == user_id e escopo ⊆ folha ⇒ aceite.
	okToken := sign(claims(rootAlice, "human:alice", "a", []string{"cap:http.get"}))

	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))

	if _, err := v.Verify(ctx, divergentRoot); !errors.Is(err, ErrDelegationInvalid) {
		t.Fatalf("raiz divergente: erro=%v, esperava ErrDelegationInvalid", err)
	}
	if _, err := v.Verify(ctx, scopeExceeds); !errors.Is(err, ErrDelegationInvalid) {
		t.Fatalf("escopo excede folha: erro=%v, esperava ErrDelegationInvalid", err)
	}
	if _, err := v.Verify(ctx, okToken); err != nil {
		t.Fatalf("token coerente devia ser aceite: %v", err)
	}
}

// Reconstrução de autoria falha fail-closed para cadeia órfã/vazia num evento.
func TestAuthorFromEventChain_FailClosed(t *testing.T) {
	t.Parallel()
	if _, err := AuthorFromEventChain(nil); !errors.Is(err, delegation.ErrEmptyChain) {
		t.Fatalf("cadeia vazia devia dar ErrEmptyChain, obtive %v", err)
	}
	orphan := []eventstore.DelegationHop{{Sub: "agt-x", ActAs: "agt-y"}}
	if _, err := AuthorFromEventChain(orphan); !errors.Is(err, delegation.ErrOrphanChain) {
		t.Fatalf("cadeia orfa devia dar ErrOrphanChain, obtive %v", err)
	}
}
