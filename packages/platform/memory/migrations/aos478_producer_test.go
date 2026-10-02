package migrations_test

import (
	"context"
	"testing"

	"github.com/aos-ref/platform/memory/migrations"
)

// TestAOS478_RegistoDeMigracaoLevaIdentidadeDeComponente (AOS-478): `memory.migration.applied`
// e `memory.migration.reverted` levam a identidade de componente das migrações, nunca um
// envelope vazio.
func TestAOS478_RegistoDeMigracaoLevaIdentidadeDeComponente(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	reg := migrations.NewRegistry(store)
	mig := makeMigration("mig-478", "1.0.0", "1.1.0")
	if _, err := reg.Record(ctx, mig, migrations.PhaseExpand); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := reg.RecordRevert(ctx, mig, migrations.PhaseExpand); err != nil {
		t.Fatalf("RecordRevert: %v", err)
	}
	evs, err := store.Read(ctx, "aos-internal/memory/migrations", 1)
	if err != nil || len(evs) != 2 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	for _, ev := range evs {
		if ev.Producer.NHIID != migrations.MigrationProducerNHI {
			t.Errorf("%s producer=%+v, quer %q", ev.Type, ev.Producer, migrations.MigrationProducerNHI)
		}
	}
}
