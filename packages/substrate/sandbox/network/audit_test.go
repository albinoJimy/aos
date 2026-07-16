package network

import (
	"context"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
)

// TestWORMSink_FailClosed_SemAtribuicao cobre o fail-closed do audit: um evento de
// egress sem principal (não-atribuível) é RECUSADO antes de qualquer escrita — um
// bloqueio anónimo é inaceitável (ADR-010).
func TestWORMSink_FailClosed_SemAtribuicao(t *testing.T) {
	sink := NewWORMSecuritySink(audit.NewMemStore())
	err := sink.Seal(context.Background(), SecurityEvent{
		Principal:   referencemonitor.Principal{}, // sem NHI nem classe
		Destination: NewDestination("evil.io", 443),
		Decision:    SecurityBlocked,
	})
	if err != ErrNoAttribution {
		t.Fatalf("Seal sem principal err = %v, quero ErrNoAttribution", err)
	}
}

// TestWORMSink_CadeiaMultiEvento sela vários bloqueios do mesmo principal e verifica
// que a cadeia por principal está íntegra ponta-a-ponta (verifica o WORM
// tamper-evident) e cresce em ordem gapless.
func TestWORMSink_CadeiaMultiEvento(t *testing.T) {
	store := audit.NewMemStore()
	sink := NewWORMSecuritySink(store)
	principal := principalNHI("agent-fetcher-01")

	for i := 0; i < 3; i++ {
		if err := sink.Seal(context.Background(), SecurityEvent{
			Principal:     principal,
			Destination:   NewDestination("evil.io", 443),
			Decision:      SecurityBlocked,
			Reason:        ReasonNotInList,
			PolicyVersion: "sbx-egress/v1#abcdef012345",
		}); err != nil {
			t.Fatalf("Seal #%d: %v", i, err)
		}
	}
	part := EgressAuditPartition(principal)
	head, _ := store.Head(context.Background(), part)
	if head != 3 {
		t.Fatalf("esperava 3 registos, head=%d", head)
	}
	if err := audit.Verify(context.Background(), store, part, 1, 3); err != nil {
		t.Fatalf("audit.Verify da cadeia: %v", err)
	}
	// A atribuição usa a NHI quando presente.
	recs, _ := store.Read(context.Background(), part, 1, 1)
	if recs[0].Principal.NHIID != "agent-fetcher-01" {
		t.Fatalf("atribuição por NHI = %q", recs[0].Principal.NHIID)
	}
}

// TestWORMSink_AllowComDelegacao cobre o registo opcional de um egress PERMITIDO
// (observabilidade) e a projecção da cadeia de delegação para o audit.
func TestWORMSink_AllowComDelegacao(t *testing.T) {
	store := audit.NewMemStore()
	sink := NewWORMSecuritySink(store)
	principal := referencemonitor.Principal{
		NHIID: "agent-fetcher-01",
		DelegationChain: []referencemonitor.DelegationHop{
			{Sub: "user:ana", ActAs: "agent-fetcher-01"},
		},
	}
	if err := sink.Seal(context.Background(), SecurityEvent{
		Principal:     principal,
		Destination:   NewDestination("api.github.com", 443),
		Decision:      SecurityAllowed,
		Reason:        ReasonAllowed,
		PolicyVersion: "sbx-egress/v1#abcdef012345",
	}); err != nil {
		t.Fatalf("Seal allow: %v", err)
	}
	part := EgressAuditPartition(principal)
	recs, _ := store.Read(context.Background(), part, 1, 1)
	if len(recs) != 1 || recs[0].Decision != audit.DecisionAllow {
		t.Fatalf("esperava 1 registo allow, obtive %+v", recs)
	}
	if len(recs[0].Principal.DelegationChain) != 1 || recs[0].Principal.DelegationChain[0].Sub != "user:ana" {
		t.Fatalf("cadeia de delegação não selada: %+v", recs[0].Principal.DelegationChain)
	}
}

// TestEgressAuditPartition cobre a derivação da partição por principal (NHI preferida,
// classe como fallback, vazia quando anónimo).
func TestEgressAuditPartition(t *testing.T) {
	if got := EgressAuditPartition(principalNHI("nhi-1")); got != "sbx-egress:nhi-1" {
		t.Fatalf("partição por NHI = %q", got)
	}
	if got := EgressAuditPartition(principalClass("cls")); got != "sbx-egress:class:cls" {
		t.Fatalf("partição por classe = %q", got)
	}
	if got := EgressAuditPartition(referencemonitor.Principal{}); got != "" {
		t.Fatalf("partição anónima deveria ser vazia, got %q", got)
	}
}
