package identity

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Registo de emissão E revogação como eventos no Event Store REAL (AOS-002)
// ---------------------------------------------------------------------------

func TestEvents_IssueAndRevoke_RealEventStore(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	iss, _ := newIssuer(t, researcherClasses(), store)
	tok, err := iss.Issue(ctx, IssueRequest{
		UserID: "human:alice", AgentID: "agt-1", AgentClass: "researcher",
		PolicyRef: "policy://r@1", UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	rev := NewRevocations(store, WithRevocationClock(fixedClock(baseTime)))
	if err := rev.Revoke(ctx, tok.Claims.JTI); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	events, err := store.Read(ctx, streamIdentity, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("esperava 2 eventos (issued+revoked), obtive %d", len(events))
	}

	// Evento 1: identity.nhi.issued.
	iev := events[0]
	if iev.Type != EventTypeIssued {
		t.Errorf("evento[0].Type=%q, esperava %q", iev.Type, EventTypeIssued)
	}
	if iev.Producer.NHIID != "agt-1" || len(iev.Producer.DelegationChain) != 1 {
		t.Errorf("producer do issued errado: %+v", iev.Producer)
	}
	var ip issuedPayload
	if err := json.Unmarshal(iev.Payload, &ip); err != nil {
		t.Fatalf("issuedPayload: %v", err)
	}
	if ip.JTI != tok.Claims.JTI || ip.UserID != "human:alice" || ip.AgentID != "agt-1" {
		t.Errorf("metadados do issued errados: %+v", ip)
	}
	if ip.AgentClass != "researcher" || ip.Expiry != tok.Claims.Expiry {
		t.Errorf("class/exp do issued errados: %+v", ip)
	}
	// NUNCA o token bearer nem a assinatura no evento.
	raw := string(iev.Payload)
	if strings.Contains(raw, tok.Compact) {
		t.Error("o evento de emissao NAO deve conter o token bearer")
	}
	sig := strings.SplitN(tok.Compact, ".", 3)[2]
	if strings.Contains(raw, sig) {
		t.Error("o evento de emissao NAO deve conter a assinatura")
	}

	// Evento 2: identity.nhi.revoked.
	rev2 := events[1]
	if rev2.Type != EventTypeRevoked {
		t.Errorf("evento[1].Type=%q, esperava %q", rev2.Type, EventTypeRevoked)
	}
	var rp revokedPayload
	if err := json.Unmarshal(rev2.Payload, &rp); err != nil {
		t.Fatalf("revokedPayload: %v", err)
	}
	if rp.JTI != tok.Claims.JTI {
		t.Errorf("jti do revoked errado: %+v", rp)
	}
}

func TestRevoke_IdempotentEvent_RealEventStore(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	rev := NewRevocations(store)
	for i := 0; i < 3; i++ {
		if err := rev.Revoke(ctx, "jti-dup"); err != nil {
			t.Fatalf("Revoke #%d: %v", i, err)
		}
	}
	events, err := store.Read(ctx, streamIdentity, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Idempotência por jti: 3 revogações ⇒ 1 evento.
	if len(events) != 1 {
		t.Fatalf("esperava 1 evento (idempotente), obtive %d", len(events))
	}
}

// ---------------------------------------------------------------------------
// Integração RM: proibição de chamada mediada sem NHI (deny + audit)
// ---------------------------------------------------------------------------

// buildRM constrói um RM com a cadeia [IdentityCheck, PolicyStub-allow] e sink
// no Event Store real; devolve o RM e o store para inspecção do audit.
func buildRM(t *testing.T, v *Verifier) (*rm.Monitor, eventstore.EventStore) {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	m := rm.New(
		rm.WithHooks(NewIdentityCheck(v), rm.PolicyStub{}),
		rm.WithEventSink(rm.NewEventStoreSink(store)),
	)
	if err := m.Register("tool.fetch", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m, store
}

func rmCall() rm.Call {
	return rm.Call{
		RunID: "run-1", StepID: "step-1", ToolID: "tool.fetch",
		Capability: "cap:http.get",
		Input:      []byte("x"),
	}
}

func TestRM_AnonymousDenied_WithAudit(t *testing.T) {
	t.Parallel()
	v := NewVerifier(WithVerifierClock(fixedClock(baseTime))) // sem trust anchor: nada verifica
	m, store := buildRM(t, v)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	// Chamada SEM credencial ⇒ deny fail-closed pelo hook de identidade.
	d, err := m.Mediate(ctx, rmCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny {
		t.Fatalf("chamada anonima devia ser negada, obtive Effect=%q", d.Effect)
	}
	if d.DeniedBy != "identity" {
		t.Errorf("DeniedBy=%q, esperava identity", d.DeniedBy)
	}
	// A negação é auditada no Event Store real.
	events, err := store.Read(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 1 || events[0].Type != rm.EventTypeDenied {
		t.Fatalf("esperava 1 evento denied auditado, obtive %+v", events)
	}
}

// ---------------------------------------------------------------------------
// Integração RM: NHI válida ⇒ permit + Principal resolvido e propagado ao audit
// ---------------------------------------------------------------------------

func TestRM_ValidNHI_PermitAndPrincipalResolved(t *testing.T) {
	t.Parallel()
	iss, pub := newIssuer(t, researcherClasses(), nil)
	tok, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "human:alice", AgentID: "agt-42", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime.Add(time.Minute))))
	m, store := buildRM(t, v)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	call := rmCall()
	call.Credential = tok.Compact
	d, err := m.Mediate(ctx, call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if !d.Permitted() {
		t.Fatalf("NHI valida devia permitir, obtive Effect=%q reason=%q", d.Effect, d.Reason)
	}

	// O Principal resolvido pela identidade propagou ao evento de mediação.
	events, err := store.Read(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 1 || events[0].Type != rm.EventTypeMediated {
		t.Fatalf("esperava 1 evento mediated, obtive %+v", events)
	}
	if events[0].Producer.NHIID != "agt-42" {
		t.Errorf("Principal nao resolvido no audit: NHIID=%q, esperava agt-42", events[0].Producer.NHIID)
	}
	if len(events[0].Producer.Scope) != 1 || events[0].Producer.Scope[0] != "cap:http.get" {
		t.Errorf("Authority nao propagada: %v", events[0].Producer.Scope)
	}
}

// ---------------------------------------------------------------------------
// Integração RM: token fora de escopo e revogado ⇒ deny (fail-closed)
// ---------------------------------------------------------------------------

func TestRM_OutOfScopeAndRevoked_Denied(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	iss, pub := newIssuer(t, researcherClasses(), store)
	// Token só com fs.read; a chamada pede http.get ⇒ fora de escopo.
	tokScoped, err := iss.Issue(ctx, IssueRequest{
		UserID: "u", AgentID: "a", AgentClass: "researcher",
		UserAuthority: []string{"cap:fs.read"},
	})
	if err != nil {
		t.Fatalf("Issue scoped: %v", err)
	}
	// Token com http.get para o caso de revogação.
	tokRevoked, err := iss.Issue(ctx, IssueRequest{
		UserID: "u", AgentID: "b", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue revoked: %v", err)
	}

	rev := NewRevocations(store)
	if err := rev.Revoke(ctx, tokRevoked.Claims.JTI); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	v := NewVerifier(
		WithTrustedIssuer(testIssuerID, pub),
		WithVerifierClock(fixedClock(baseTime.Add(time.Minute))),
		WithRevocations(rev),
	)
	m := rm.New(
		rm.WithHooks(NewIdentityCheck(v), rm.PolicyStub{}),
		rm.WithEventSink(rm.NewEventStoreSink(store)),
	)
	_ = m.Register("tool.fetch", func(_ context.Context, in []byte) ([]byte, error) { return in, nil })

	// Fora de escopo.
	call := rmCall() // pede cap:http.get
	call.Credential = tokScoped.Compact
	d, err := m.Mediate(ctx, call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny || d.DeniedBy != "identity" {
		t.Fatalf("fora de escopo devia negar em identity, obtive Effect=%q DeniedBy=%q", d.Effect, d.DeniedBy)
	}
	// Critério 3: a negação (fora de escopo) é AUDITADA no Event Store.
	assertDeniedAudited(t, store, "run-1", "step-1")

	// Revogado.
	call2 := rmCall()
	call2.StepID = "step-2"
	call2.Credential = tokRevoked.Compact
	d2, err := m.Mediate(ctx, call2)
	if err != nil {
		t.Fatalf("Mediate revogado: %v", err)
	}
	if d2.Effect != rm.EffectDeny || d2.DeniedBy != "identity" {
		t.Fatalf("revogado devia negar em identity, obtive Effect=%q DeniedBy=%q", d2.Effect, d2.DeniedBy)
	}
	// Critério 3: a negação (revogado) é AUDITADA no Event Store.
	assertDeniedAudited(t, store, "run-1", "step-2")
}

// TestRM_ExpiredToken_DeniedAndAudited cobre o Critério de Aceitação 3 para o
// caso EXPIRADO conduzido pelo RM (m.Mediate), não apenas ao nível do Verifier:
// um token expirado é rejeitado fail-closed pelo hook de identidade e a negação
// é auditada.
func TestRM_ExpiredToken_DeniedAndAudited(t *testing.T) {
	t.Parallel()
	iss, pub := newIssuer(t, researcherClasses(), nil)
	// Emitido em baseTime, TTL 5min ⇒ exp = baseTime+5min.
	tok, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "human:alice", AgentID: "agt-exp", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Relógio do verificador 10min após a emissão ⇒ token expirado.
	v := NewVerifier(
		WithTrustedIssuer(testIssuerID, pub),
		WithVerifierClock(fixedClock(baseTime.Add(10*time.Minute))),
	)
	m, store := buildRM(t, v)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	call := rmCall()
	call.Credential = tok.Compact
	d, err := m.Mediate(ctx, call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny || d.DeniedBy != "identity" {
		t.Fatalf("token expirado devia negar em identity, obtive Effect=%q DeniedBy=%q", d.Effect, d.DeniedBy)
	}
	// A negação por expiração é auditada no Event Store real.
	assertDeniedAudited(t, store, "run-1", "step-1")
}

// assertDeniedAudited confirma que o stream runID contém um evento
// tool.call.denied para o stepID dado, com denied_by == "identity" (a negação do
// hook de identidade ficou registada no Event Store).
func assertDeniedAudited(t *testing.T, store eventstore.EventStore, runID, stepID string) {
	t.Helper()
	events, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read audit: %v", err)
	}
	for _, e := range events {
		if e.StepID != stepID {
			continue
		}
		if e.Type != rm.EventTypeDenied {
			t.Fatalf("evento do step %q: Type=%q, esperava %q", stepID, e.Type, rm.EventTypeDenied)
		}
		var p struct {
			Decision string `json:"decision"`
			DeniedBy string `json:"denied_by"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("unmarshal denied payload: %v", err)
		}
		if p.DeniedBy != "identity" {
			t.Fatalf("evento denied do step %q: denied_by=%q, esperava identity", stepID, p.DeniedBy)
		}
		return
	}
	t.Fatalf("nenhum evento tool.call.denied auditado para o step %q em %q", stepID, runID)
}
