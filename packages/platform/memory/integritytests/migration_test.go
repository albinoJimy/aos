package integritytests

import (
	"context"
	"reflect"
	"testing"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/migrations"
)

// initialRecords é a fixture golden das migrações: dois registos working em v1.0.0.
func initialRecords() []domain.Record {
	return []domain.Record{
		workingRec("w1", "1.0.0", "alpha", 1),
		workingRec("w2", "1.0.0", "beta", 2),
	}
}

// TestMigrationRoundTripNoLoss — expand→migrate→contract sem perda, com dual-read na
// fase expand (ambos os schemas legíveis, leitura canónica em From = sem downtime) e
// revert completo que devolve o estado byte-idêntico ao inicial.
func TestMigrationRoundTripNoLoss(t *testing.T) {
	ctx := context.Background()
	initial := initialRecords()
	mig := makeReversibleMigration("m-roundtrip", "1.0.0", "1.1.0")

	r, err := migrations.NewRunner(mig, initial)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	// EXPAND: dual-write/dual-read; a leitura canónica MANTÉM-SE em From (sem downtime).
	if err := r.Expand(ctx); err != nil {
		t.Fatalf("Expand: %v", err)
	}
	oldRepr, err := r.Read("w1", ver("1.0.0"))
	if err != nil {
		t.Fatalf("Read(From): %v", err)
	}
	newRepr, err := r.Read("w1", ver("1.1.0"))
	if err != nil {
		t.Fatalf("Read(To): %v", err)
	}
	if contentOf(oldRepr) != "alpha" || contentOf(newRepr) != "alpha"+migSuffix {
		t.Fatalf("dual-read divergente: from=%q to=%q", contentOf(oldRepr), contentOf(newRepr))
	}
	if can, _ := r.ReadCanonical("w1"); contentOf(can) != "alpha" {
		t.Fatalf("leitura canónica em expand devia ser From (%q), foi %q", "alpha", contentOf(can))
	}

	// MIGRATE: a leitura canónica passa para To.
	if err := r.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if can, _ := r.ReadCanonical("w1"); contentOf(can) != "alpha"+migSuffix {
		t.Fatalf("leitura canónica em migrate devia ser To, foi %q", contentOf(can))
	}

	// CONTRACT: só To subsiste.
	if err := r.Contract(ctx); err != nil {
		t.Fatalf("Contract: %v", err)
	}
	recs := r.CanonicalRecords()
	if len(recs) != 2 || contentOf(recs[0]) != "alpha"+migSuffix {
		t.Fatalf("pós-contract inesperado: %+v", recs)
	}

	// Round-trip completo (via verificador partilhado): revert devolve o inicial.
	if err := verifyMigrationRoundTrip(initial, mig); err != nil {
		t.Fatalf("round-trip com perda: %v", err)
	}
}

// TestMigrationFailedRollbackIdentical — uma migração cujo Up FALHA a meio deixa o
// estado BYTE-IDÊNTICO ao inicial (rollback transacional, sem perda nem corrupção).
func TestMigrationFailedRollbackIdentical(t *testing.T) {
	ctx := context.Background()
	initial := initialRecords()
	// O Up falha para o registo cujo Content é "beta" (w2) — a meio da fase.
	failing := makeFailingMigration("m-fail", "1.0.0", "1.1.0", "beta")

	r, err := migrations.NewRunner(failing, initial)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Run(ctx); err == nil {
		t.Fatal("Run devia falhar (Up rebenta em w2)")
	}
	got := r.CanonicalRecords()
	if len(got) != len(initial) {
		t.Fatalf("nº de registos = %d, quero %d", len(got), len(initial))
	}
	for i := range got {
		if !reflect.DeepEqual(got[i], initial[i]) {
			t.Fatalf("estado divergente após rollback em %d: %+v != %+v", i, got[i], initial[i])
		}
	}
}

// TestMigrationIdempotentReapply — reaplicar uma migração é um no-op: no registo
// durável (a 2ª gravação deduplica, applied=false) E ao nível do motor (Expand
// repetido não altera a fase).
func TestMigrationIdempotentReapply(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	reg := migrations.NewRegistry(es)
	mig := makeReversibleMigration("m-idem", "1.0.0", "1.1.0")

	// Registo durável idempotente.
	first, err := reg.Record(ctx, mig, migrations.PhaseExpand)
	if err != nil {
		t.Fatalf("Record #1: %v", err)
	}
	second, err := reg.Record(ctx, mig, migrations.PhaseExpand)
	if err != nil {
		t.Fatalf("Record #2: %v", err)
	}
	if !first || second {
		t.Fatalf("idempotência do registo falhou: first=%v second=%v (quero true,false)", first, second)
	}
	list, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("linhagem = %d entradas, quero 1 (reaplicar = no-op)", len(list))
	}

	// Idempotência ao nível do motor: Expand repetido não muda a fase.
	r, err := migrations.NewRunner(mig, initialRecords())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Expand(ctx); err != nil {
		t.Fatalf("Expand #1: %v", err)
	}
	phase := r.Phase()
	if err := r.Expand(ctx); err != nil {
		t.Fatalf("Expand #2 (no-op): %v", err)
	}
	if r.Phase() != phase || r.Phase() != migrations.PhaseExpand {
		t.Fatalf("Expand repetido mudou a fase: %v", r.Phase())
	}
}
