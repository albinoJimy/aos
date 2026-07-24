package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/identity/delegation"
	"github.com/aos-ref/substrate/eventstore"
)

// Este ficheiro prova o BINDING humano↔NHI AUDITÁVEL (AOS-176, frente 3 do D4 /
// ADR-003): a emissão grava um registo append-only, resolvel por [BindingAudit], que
// responde "quem autorizou" (o humano na raiz), quando, e por que método/autoridade —
// sem segredos/PII — e recusa fail-closed uma cadeia órfã (NHI "pool").

// (1) O registo de binding capta o binding COMPLETO: humano-raiz + agente + classe +
// escopo + método de autorização + timestamps; e NÃO contém segredos (token bearer,
// assinatura) nem material da asserção.
func TestBindingAudit_ResolveByJTI_CapturesFullBinding(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	iss, _ := newIssuer(t, researcherClasses(), store)
	const method = "oidc:https://idp.corp.example"
	tok, err := iss.Issue(ctx, IssueRequest{
		UserID: "alice", AgentID: "agt-1", AgentClass: "researcher",
		PolicyRef:     "policy://r@1",
		UserAuthority: []string{"cap:http.get", "cap:fs.read"},
		AuthMethod:    method,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	audit := NewBindingAudit(store)
	rec, err := audit.ResolveByJTI(ctx, tok.Claims.JTI)
	if err != nil {
		t.Fatalf("ResolveByJTI: %v", err)
	}

	// "Quem autorizou": o humano na RAIZ da cadeia (derivado da cadeia, não do campo
	// de conveniência) — com o prefixo human: exigido por AOS-006.
	if rec.Human != "human:alice" {
		t.Errorf("Human=%q, quero human:alice (raiz da cadeia)", rec.Human)
	}
	if rec.AgentID != "agt-1" || rec.AgentClass != "researcher" {
		t.Errorf("agente/classe errados: %+v", rec)
	}
	if rec.JTI != tok.Claims.JTI {
		t.Errorf("JTI=%q, quero %q", rec.JTI, tok.Claims.JTI)
	}
	// Por que método/autoridade: o contexto de autorização.
	if rec.AuthMethod != method {
		t.Errorf("AuthMethod=%q, quero %q", rec.AuthMethod, method)
	}
	// Quando: timestamps do binding coincidem com os claims selados.
	if rec.IssuedAt != tok.Claims.IssuedAt || rec.Expiry != tok.Claims.Expiry {
		t.Errorf("timestamps errados: iat=%d exp=%d, quero iat=%d exp=%d", rec.IssuedAt, rec.Expiry, tok.Claims.IssuedAt, tok.Claims.Expiry)
	}
	if !containsAll(rec.Scope, "cap:http.get", "cap:fs.read") {
		t.Errorf("Scope=%v, quero conter cap:http.get e cap:fs.read", rec.Scope)
	}

	// SEM segredos/PII: o evento auditável NUNCA contém o token bearer nem a assinatura.
	events, err := store.Read(ctx, streamIdentity, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	raw := string(events[0].Payload)
	if strings.Contains(raw, tok.Compact) {
		t.Error("o registo de binding NAO deve conter o token bearer")
	}
	sig := strings.SplitN(tok.Compact, ".", 3)[2]
	if strings.Contains(raw, sig) {
		t.Error("o registo de binding NAO deve conter a assinatura do token")
	}
}

// (2) Idempotência por jti: uma RE-emissão com o mesmo jti não duplica o binding
// (StatusDuplicate no store) — a auditoria por agente devolve exactamente um registo.
func TestBindingAudit_IdempotentByJTI(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	_, priv := testKeys(t)
	iss, err := NewIssuer(testIssuerID, priv, researcherClasses(),
		WithIssuerClock(fixedClock(baseTime)),
		WithIDSource(func() string { return "jti-fixed" }), // mesmo jti nas duas emissões
		WithEventStore(store),
	)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}

	req := IssueRequest{
		UserID: "alice", AgentID: "agt-1", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"}, AuthMethod: "allowlist",
	}
	if _, err := iss.Issue(ctx, req); err != nil {
		t.Fatalf("Issue #1: %v", err)
	}
	if _, err := iss.Issue(ctx, req); err != nil {
		t.Fatalf("Issue #2 (re-emissao): %v", err)
	}

	// O log tem UM só evento: a idempotência por jti não duplicou o binding.
	events, err := store.Read(ctx, streamIdentity, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("esperava 1 evento (idempotencia por jti), obtive %d", len(events))
	}

	audit := NewBindingAudit(store)
	recs, err := audit.BindingsForAgent(ctx, "agt-1")
	if err != nil {
		t.Fatalf("BindingsForAgent: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("esperava 1 binding para o agente, obtive %d", len(recs))
	}
	if recs[0].JTI != "jti-fixed" || recs[0].Human != "human:alice" {
		t.Errorf("binding resolvido errado: %+v", recs[0])
	}
}

// (3) Fail-closed: um evento de emissão com cadeia ÓRFÃ (raiz NÃO-humana — uma NHI
// "pool") NUNCA é resolvido para um binding. A auditoria recusa-o com ErrOrphanChain,
// não atribuindo autoria a um principal não-humano ("The Audit Log Lied", ADR-003).
func TestBindingAudit_OrphanChainRejectedFailClosed(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	// Injecta directamente um evento com raiz NÃO-humana (o que o mint legítimo nunca
	// produz — delegation.NewRoot recusa-o —, mas que a auditoria tem de rejeitar se
	// alguma vez aparecer no log).
	pl := issuedPayload{
		JTI: "jti-orphan", UserID: "svc-pool", AgentID: "agt-pool",
		AgentClass: "researcher", Issuer: testIssuerID, AuthMethod: "allowlist",
	}
	raw, _ := json.Marshal(pl)
	if _, err := store.Append(ctx, streamIdentity, eventstore.EventInput{
		Type: EventTypeIssued, Payload: raw, RunID: streamIdentity, StepID: "nhi.issued:jti-orphan",
		Producer: eventstore.Producer{
			NHIID:           "agt-pool",
			DelegationChain: []eventstore.DelegationHop{{Sub: "svc-pool", ActAs: "agt-pool"}}, // raiz não-humana
		},
	}); err != nil {
		t.Fatalf("Append (orphan): %v", err)
	}

	audit := NewBindingAudit(store)
	if _, err := audit.ResolveByJTI(ctx, "jti-orphan"); !errors.Is(err, delegation.ErrOrphanChain) {
		t.Fatalf("ResolveByJTI de cadeia orfa: erro=%v, quero delegation.ErrOrphanChain", err)
	}
	if _, err := audit.BindingsForAgent(ctx, "agt-pool"); !errors.Is(err, delegation.ErrOrphanChain) {
		t.Fatalf("BindingsForAgent de cadeia orfa: erro=%v, quero delegation.ErrOrphanChain", err)
	}
}

// (4) Fail-closed no MINT: um pedido sem humano responsável é recusado e NENHUMA NHI
// "pool" é registada. A raiz humana é estrutural: delegation.NewRoot recusa uma raiz
// não-humana; e o Issuer recusa um user_id vazio antes de gravar qualquer evento.
func TestIssue_FailClosed_NoPoolNHIRecorded(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	iss, _ := newIssuer(t, researcherClasses(), store)
	if _, err := iss.Issue(ctx, IssueRequest{
		UserID: "", AgentID: "agt-x", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"},
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Issue sem humano: erro=%v, quero ErrInvalidRequest", err)
	}

	// Nada foi gravado: o stream de identidade nem sequer existe.
	if _, err := store.Read(ctx, streamIdentity, 1); !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Fatalf("Read: erro=%v, quero ErrStreamNotFound (nenhuma NHI pool registada)", err)
	}

	// E a auditoria confirma-o de forma limpa: sem binding registado.
	if _, err := NewBindingAudit(store).ResolveByJTI(ctx, "qualquer"); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("ResolveByJTI: erro=%v, quero ErrBindingNotFound", err)
	}

	// A invariante estrutural da raiz humana (o primitivo que impede uma NHI pool).
	if _, err := delegation.NewRoot("svc-robot", "agt-x", nil); !errors.Is(err, delegation.ErrOrphanChain) {
		t.Fatalf("NewRoot raiz nao-humana: erro=%v, quero ErrOrphanChain", err)
	}
}

// (5) Guardas do auditor: jti/agente vazios e store nil ⇒ ErrBindingNotFound (nunca
// panica nem inventa autoria).
func TestBindingAudit_Guards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	nilAudit := NewBindingAudit(nil)
	if _, err := nilAudit.ResolveByJTI(ctx, "x"); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("store nil ResolveByJTI: erro=%v, quero ErrBindingNotFound", err)
	}
	if _, err := nilAudit.BindingsForAgent(ctx, "x"); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("store nil BindingsForAgent: erro=%v, quero ErrBindingNotFound", err)
	}

	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	audit := NewBindingAudit(store)
	if _, err := audit.ResolveByJTI(ctx, ""); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("jti vazio: erro=%v, quero ErrBindingNotFound", err)
	}
	if _, err := audit.BindingsForAgent(ctx, ""); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("agente vazio: erro=%v, quero ErrBindingNotFound", err)
	}
}

// (6) Robustez de disponibilidade da auditoria: um registo identity.nhi.issued
// MALFORMADO isolado (payload ilegível) NÃO aborta a varredura — os bindings válidos
// não relacionados continuam resolvéis. Sem esta salvaguarda, um só evento corrupto em
// qualquer posição do stream partilhado seria um DoS de auditoria sobre TODOS os
// bindings. Fail-closed mantém-se: o corrupto é saltado (nunca legitima um binding),
// e a garantia contra adulteração é o append-only/WORM do log (ADR-010).
func TestBindingAudit_MalformedEventDoesNotBlockValidBindings(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	// Um evento de emissão com payload ILEGÍVEL, gravado ANTES do binding válido (posição
	// anterior no stream) — o caso que, sem a correcção, abortaria toda a resolução.
	if _, err := store.Append(ctx, streamIdentity, eventstore.EventInput{
		Type: EventTypeIssued, Payload: []byte("{ nao-json"), RunID: streamIdentity,
		StepID: "nhi.issued:corrupt",
		Producer: eventstore.Producer{
			NHIID:           "agt-corrupt",
			DelegationChain: []eventstore.DelegationHop{{Sub: "human:mallory", ActAs: "agt-corrupt"}},
		},
	}); err != nil {
		t.Fatalf("Append (malformado): %v", err)
	}

	// Um binding VÁLIDO a seguir ao corrupto.
	iss, _ := newIssuer(t, researcherClasses(), store)
	tok, err := iss.Issue(ctx, IssueRequest{
		UserID: "alice", AgentID: "agt-good", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"}, AuthMethod: "allowlist",
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	audit := NewBindingAudit(store)

	// O evento corrupto (posição anterior) NÃO impede a resolução do binding válido.
	rec, err := audit.ResolveByJTI(ctx, tok.Claims.JTI)
	if err != nil {
		t.Fatalf("ResolveByJTI apos evento corrupto: %v", err)
	}
	if rec.Human != "human:alice" || rec.AgentID != "agt-good" {
		t.Errorf("binding valido mal resolvido apos evento corrupto: %+v", rec)
	}
	recs, err := audit.BindingsForAgent(ctx, "agt-good")
	if err != nil {
		t.Fatalf("BindingsForAgent apos evento corrupto: %v", err)
	}
	if len(recs) != 1 || recs[0].Human != "human:alice" {
		t.Errorf("esperava 1 binding valido, obtive %+v", recs)
	}

	// Fail-closed preservado: o agente do registo corrupto NÃO resolve para binding
	// (o corrupto foi saltado, nunca legitimado como uma NHI atribuída).
	if _, err := audit.BindingsForAgent(ctx, "agt-corrupt"); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("agente do registo corrupto: erro=%v, quero ErrBindingNotFound", err)
	}
}

// containsAll indica se xs contém todos os valores dados.
func containsAll(xs []string, vals ...string) bool {
	set := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		set[x] = struct{}{}
	}
	for _, v := range vals {
		if _, ok := set[v]; !ok {
			return false
		}
	}
	return true
}
