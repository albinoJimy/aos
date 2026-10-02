package identity

import (
	"context"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// TestAOS478_RevogacaoLevaOJTI (AOS-478): `identity.nhi.revoked` leva no envelope o `jti`
// revogado — o elo para o `identity.nhi.issued` do mesmo token.
func TestAOS478_RevogacaoLevaOJTI(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	if err := NewRevocations(store).Revoke(ctx, "jti-478"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	evs, err := store.Read(ctx, streamIdentity, 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	if evs[0].Type != EventTypeRevoked || evs[0].Producer.NHIID != "jti-478" {
		t.Fatalf("%s producer=%+v, quer o jti", evs[0].Type, evs[0].Producer)
	}
}
