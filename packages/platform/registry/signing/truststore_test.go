package signing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
)

func fixedClock() func() time.Time {
	base := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return base }
}

func newTrustStore(t *testing.T) (*TrustStore, *audit.MemStore) {
	t.Helper()
	store := audit.NewMemStore()
	ts, err := NewTrustStore(store, WithTrustClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewTrustStore: %v", err)
	}
	return ts, store
}

func TestNewTrustStore_NilAuditFailsClosed(t *testing.T) {
	t.Parallel()
	if _, err := NewTrustStore(nil); !errors.Is(err, ErrNoAuditStore) {
		t.Fatalf("NewTrustStore(nil) = %v, quer ErrNoAuditStore", err)
	}
}

func TestTrustStore_AddThenLookup(t *testing.T) {
	t.Parallel()
	ts, _ := newTrustStore(t)
	pub := pubFromSeed(3)
	if err := ts.Add(context.Background(), "pub:acme", pub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, ok := ts.Lookup("pub:acme")
	if !ok {
		t.Fatal("Lookup apos Add: nao encontrou a chave confiavel")
	}
	if !bytesEqualPub(got, pub) {
		t.Fatal("Lookup devolveu chave diferente da registada")
	}
	// Lookup de um key id desconhecido e nao-confiavel (default-deny).
	if _, ok := ts.Lookup("pub:desconhecido"); ok {
		t.Fatal("Lookup de key id desconhecido devolveu confiavel")
	}
}

func TestTrustStore_AddFailClosed(t *testing.T) {
	t.Parallel()
	ts, _ := newTrustStore(t)
	pub := pubFromSeed(3)
	if err := ts.Add(context.Background(), "", pub); !errors.Is(err, ErrEmptyKeyID) {
		t.Fatalf("Add(keyid vazio) = %v, quer ErrEmptyKeyID", err)
	}
	if err := ts.Add(context.Background(), "pub:x", pub[:5]); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Add(chave curta) = %v, quer ErrInvalidKey", err)
	}
}

// TestTrustStore_RevokedKeyStopsValidating cobre o Teste Requerido: uma chave
// REVOGADA deixa de validar.
func TestTrustStore_RevokedKeyStopsValidating(t *testing.T) {
	t.Parallel()
	ts, _ := newTrustStore(t)
	pub := pubFromSeed(4)
	if err := ts.Add(context.Background(), "pub:acme", pub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, ok := ts.Lookup("pub:acme"); !ok {
		t.Fatal("pre-condicao: chave devia estar confiavel antes da revogacao")
	}
	if err := ts.Revoke(context.Background(), "pub:acme"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := ts.Lookup("pub:acme"); ok {
		t.Fatal("chave REVOGADA continuou a validar (deveria ser nao-confiavel)")
	}
}

// TestTrustStore_RevocationIsTerminal cobre AOS-048 Q4: a revogacao e TERMINAL por
// keyID — um Add subsequente sobre um keyID revogado e RECUSADO (ErrKeyRevoked) e
// NAO o re-activa. A tentativa e auditada como recusa e a chave permanece revogada.
func TestTrustStore_RevocationIsTerminal(t *testing.T) {
	t.Parallel()
	ts, store := newTrustStore(t)
	ctx := context.Background()
	pub := pubFromSeed(7)

	if err := ts.Add(ctx, "pub:acme", pub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := ts.Revoke(ctx, "pub:acme"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// Re-Add do MESMO keyID revogado (mesma ou nova chave) e RECUSADO — nao re-activa.
	if err := ts.Add(ctx, "pub:acme", pub); !errors.Is(err, ErrKeyRevoked) {
		t.Fatalf("Add sobre keyID revogado = %v, quer ErrKeyRevoked", err)
	}
	// A chave TEM de continuar nao-confiavel (a revogacao nao foi revertida).
	if _, ok := ts.Lookup("pub:acme"); ok {
		t.Fatal("keyID revogado voltou a validar apos Add (revogacao nao e terminal)")
	}
	// Tentar com uma chave DIFERENTE tambem e recusado (o keyID e que e terminal).
	if err := ts.Add(ctx, "pub:acme", pubFromSeed(8)); !errors.Is(err, ErrKeyRevoked) {
		t.Fatalf("Add(keyID revogado, chave nova) = %v, quer ErrKeyRevoked", err)
	}
	// Audit: add(allow) + revoke(deny) + 2 tentativas de re-add recusadas(deny) = 4.
	head, err := store.Head(ctx, DefaultTrustStorePartition)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != 4 {
		t.Fatalf("esperava 4 registos de audit (add+revoke+2 re-add recusados), tem %d", head)
	}
	recs, err := store.Read(ctx, DefaultTrustStorePartition, 3, 4)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for i, r := range recs {
		if r.Capability != capTrustAdd || r.Decision != audit.DecisionDeny {
			t.Fatalf("tentativa de re-add %d = (%s,%s), quer (%s,deny)", i, r.Capability, r.Decision, capTrustAdd)
		}
	}
	// A cadeia mantem-se integra (tamper-evident).
	if err := audit.Verify(ctx, store, DefaultTrustStorePartition, 1, head); err != nil {
		t.Fatalf("audit.Verify: %v", err)
	}
	// Rotacao para um keyID NOVO e o caminho legitimo de re-confianca.
	if err := ts.Add(ctx, "pub:acme-2", pubFromSeed(9)); err != nil {
		t.Fatalf("Add de keyID novo (rotacao) devia ser aceite: %v", err)
	}
	if _, ok := ts.Lookup("pub:acme-2"); !ok {
		t.Fatal("keyID novo (rotacao) devia estar confiavel")
	}
}

// TestTrustStore_ChangesAreAudited cobre o Teste Requerido: cada mudanca do trust
// store (add/revoke) sela-se no audit WORM, e a cadeia continua integra.
func TestTrustStore_ChangesAreAudited(t *testing.T) {
	t.Parallel()
	ts, store := newTrustStore(t)
	pub := pubFromSeed(5)
	ctx := context.Background()

	if err := ts.Add(ctx, "pub:acme", pub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := ts.Revoke(ctx, "pub:acme"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	head, err := store.Head(ctx, DefaultTrustStorePartition)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != 2 {
		t.Fatalf("esperava 2 registos de audit (add+revoke), tem %d", head)
	}
	recs, err := store.Read(ctx, DefaultTrustStorePartition, 1, 2)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// add: allow + capability add; revoke: deny + capability revoke.
	if recs[0].Capability != capTrustAdd || recs[0].Decision != audit.DecisionAllow {
		t.Fatalf("registo add = (%s,%s), quer (%s,allow)", recs[0].Capability, recs[0].Decision, capTrustAdd)
	}
	if recs[0].ToolID != "pub:acme" || recs[0].Resource.Value != "pub:acme" {
		t.Fatalf("registo add nao atribui o key id: %+v", recs[0].Resource)
	}
	if recs[1].Capability != capTrustRevoke || recs[1].Decision != audit.DecisionDeny {
		t.Fatalf("registo revoke = (%s,%s), quer (%s,deny)", recs[1].Capability, recs[1].Decision, capTrustRevoke)
	}
	// A hash-chain do trust store tem de estar integra (tamper-evident, AOS-011).
	if err := audit.Verify(ctx, store, DefaultTrustStorePartition, 1, head); err != nil {
		t.Fatalf("audit.Verify da cadeia do trust store: %v", err)
	}
}

// TestTrustStore_AuditFailClosed prova que uma mudanca NAO-AUDITAVEL e recusada e
// NAO toma efeito (fail-closed, ADR-010).
func TestTrustStore_AuditFailClosed(t *testing.T) {
	t.Parallel()
	failing := &failingAuditStore{}
	ts, err := NewTrustStore(failing, WithTrustClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewTrustStore: %v", err)
	}
	pub := pubFromSeed(6)
	if err := ts.Add(context.Background(), "pub:acme", pub); !errors.Is(err, ErrAuditFailed) {
		t.Fatalf("Add com audit em falha = %v, quer ErrAuditFailed", err)
	}
	// A chave nao pode ter tomado efeito (a mudanca nao-auditavel foi recusada).
	if _, ok := ts.Lookup("pub:acme"); ok {
		t.Fatal("Add com audit em falha nao devia ter registado a chave")
	}
}
