package migrations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/memory/migrations"
)

// TestMajorGateFailClosed cobre o critério central do gate: uma migração MAJOR sem
// aprovação é RECUSADA (fail-closed); MINOR/PATCH passam sem gate.
func TestMajorGateFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		name       string
		from, to   string
		approve    bool
		wantDenied bool
	}{
		{name: "major_sem_aprovacao_recusada", from: "1.2.0", to: "2.0.0", approve: false, wantDenied: true},
		{name: "major_aprovada_passa", from: "1.2.0", to: "2.0.0", approve: true, wantDenied: false},
		{name: "minor_passa_sem_gate", from: "1.0.0", to: "1.1.0", approve: false, wantDenied: false},
		{name: "patch_passa_sem_gate", from: "1.0.0", to: "1.0.1", approve: false, wantDenied: false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			mig := makeMigration("mig-"+c.name, c.from, c.to)
			gate := migrations.NewEvalGate()
			if c.approve {
				gate.Approve(mig.ID)
			}
			r, err := migrations.NewRunner(mig, initialSet(c.from), migrations.WithGate(gate))
			if err != nil {
				t.Fatalf("NewRunner: %v", err)
			}
			err = r.Expand(ctx)
			if c.wantDenied {
				if !errors.Is(err, migrations.ErrMigrationDenied) {
					t.Fatalf("Expand: erro = %v, quero ErrMigrationDenied", err)
				}
				if r.Phase() != migrations.PhaseNone {
					t.Fatalf("fase apos recusa = %s, quero none (nada aplicado)", r.Phase())
				}
				// Estado inalterado: nenhuma forma nova.
				if r.HasBoth("w1") {
					t.Fatalf("recusa MAJOR deixou forma nova pendurada")
				}
				return
			}
			if err != nil {
				t.Fatalf("Expand: erro inesperado %v", err)
			}
			if r.Phase() != migrations.PhaseExpand {
				t.Fatalf("fase = %s, quero expand", r.Phase())
			}
		})
	}
}

// TestDefaultGateDeniesMajor prova que, SEM gate injectado, o default é fail-closed
// para MAJOR (nunca deixa passar uma quebra de contrato por falta de configuração).
func TestDefaultGateDeniesMajor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mig := makeMigration("mig-default-deny", "1.9.0", "2.0.0")
	r, err := migrations.NewRunner(mig, initialSet("1.9.0")) // sem WithGate
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Expand(ctx); !errors.Is(err, migrations.ErrMigrationDenied) {
		t.Fatalf("Expand sem gate: erro = %v, quero ErrMigrationDenied", err)
	}
}

// TestMajorApprovedFullRun prova que uma MAJOR aprovada completa o ciclo inteiro.
func TestMajorApprovedFullRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mig := makeMigration("mig-major-full", "1.0.0", "2.0.0")
	gate := migrations.NewEvalGate(mig.ID) // pré-aprovada
	r, err := migrations.NewRunner(mig, initialSet("1.0.0"), migrations.WithGate(gate))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run MAJOR aprovada: %v", err)
	}
	if r.Phase() != migrations.PhaseContract {
		t.Fatalf("fase final = %s, quero contract", r.Phase())
	}
}
