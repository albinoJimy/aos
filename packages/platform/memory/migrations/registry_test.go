package migrations_test

import (
	"context"
	"testing"

	"github.com/aos-ref/platform/memory/migrations"
	"github.com/aos-ref/platform/memory/schema"
	"github.com/aos-ref/substrate/eventstore"
)

func newStore(t *testing.T) *eventstore.Store {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	return store
}

// TestRegistryIdempotent cobre a idempotência do registo durável: gravar a mesma
// fase da mesma migração duas vezes é um NO-OP (a segunda deduplica no Event Store).
func TestRegistryIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reg := migrations.NewRegistry(newStore(t))
	mig := makeMigration("mig-idem", "1.0.0", "1.1.0")

	applied, err := reg.Record(ctx, mig, migrations.PhaseExpand)
	if err != nil {
		t.Fatalf("Record 1: %v", err)
	}
	if !applied {
		t.Fatalf("primeira gravacao devia ser applied=true")
	}

	// Reaplicar: no-op (duplicado).
	applied, err = reg.Record(ctx, mig, migrations.PhaseExpand)
	if err != nil {
		t.Fatalf("Record 2: %v", err)
	}
	if applied {
		t.Fatalf("reaplicacao devia ser applied=false (no-op)")
	}

	ok, err := reg.IsApplied(ctx, mig.ID, migrations.PhaseExpand)
	if err != nil {
		t.Fatalf("IsApplied: %v", err)
	}
	if !ok {
		t.Fatalf("IsApplied devia ser true apos gravacao")
	}

	// Uma fase diferente da mesma migração é uma entrada distinta.
	applied, err = reg.Record(ctx, mig, migrations.PhaseMigrate)
	if err != nil {
		t.Fatalf("Record migrate: %v", err)
	}
	if !applied {
		t.Fatalf("fase migrate devia ser nova (applied=true)")
	}

	list, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Só duas entradas ÚNICAS foram gravadas (expand + migrate); o duplicado nao conta.
	if len(list) != 2 {
		t.Fatalf("linhagem = %d entradas, quero 2 (sem duplicados)", len(list))
	}
}

// TestRunnerIdempotentPhases cobre a idempotência ao nível do motor: reaplicar uma
// fase já concluída é um no-op observável, e o registo durável não acumula duplicados.
func TestRunnerIdempotentPhases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from, to := "1.0.0", "1.1.0"
	reg := migrations.NewRegistry(newStore(t))
	mig := makeMigration("mig-runner-idem", from, to)
	r, err := migrations.NewRunner(mig, initialSet(from),
		migrations.WithGate(migrations.NewEvalGate()),
		migrations.WithRegistry(reg),
	)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	// Expand duas vezes: segunda é no-op, estado e linhagem estáveis.
	if err := r.Expand(ctx); err != nil {
		t.Fatalf("Expand 1: %v", err)
	}
	snapContent, _ := r.ReadCanonical("w1")
	if err := r.Expand(ctx); err != nil {
		t.Fatalf("Expand 2 (no-op): %v", err)
	}
	again, _ := r.ReadCanonical("w1")
	if contentOf(again) != contentOf(snapContent) {
		t.Fatalf("Expand repetido alterou o estado")
	}

	if err := r.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := r.Migrate(ctx); err != nil {
		t.Fatalf("Migrate 2 (no-op): %v", err)
	}
	if err := r.Contract(ctx); err != nil {
		t.Fatalf("Contract: %v", err)
	}
	if err := r.Contract(ctx); err != nil {
		t.Fatalf("Contract 2 (no-op): %v", err)
	}

	list, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("linhagem = %d, quero 3 (expand/migrate/contract sem duplicados)", len(list))
	}
	for _, e := range list {
		if e.MigrationID != mig.ID || e.From != from || e.To != to {
			t.Fatalf("entrada inconsistente: %+v", e)
		}
	}
}

// TestRunnerAdvancesClassSchema prova a integração com o versionamento por classe:
// ao migrar, a versão de schema corrente da classe avança para To (monótona).
func TestRunnerAdvancesClassSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from, to := "1.0.0", "1.1.0"
	classReg := schema.DefaultClassRegistry()
	mig := makeMigration("mig-classver", from, to)
	r, err := migrations.NewRunner(mig, initialSet(from),
		migrations.WithGate(migrations.NewEvalGate()),
		migrations.WithClassRegistry(classReg),
	)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if cur, _ := classReg.Current(mig.Class); cur.String() != from {
		t.Fatalf("versao inicial da classe = %s, quero %s", cur, from)
	}
	if err := r.Expand(ctx); err != nil {
		t.Fatalf("Expand: %v", err)
	}
	// Ainda em From durante expand (leitura canonica nao mudou).
	if cur, _ := classReg.Current(mig.Class); cur.String() != from {
		t.Fatalf("apos expand versao da classe = %s, quero ainda %s", cur, from)
	}
	if err := r.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if cur, _ := classReg.Current(mig.Class); cur.String() != to {
		t.Fatalf("apos migrate versao da classe = %s, quero %s", cur, to)
	}
}
