package migrations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/migrations"
)

// initialSet devolve um snapshot determinístico de registos na versão From.
func initialSet(from string) []domain.Record {
	return []domain.Record{
		workingRec("w1", from, "alpha", 3),
		workingRec("w2", from, "beta", 5),
		workingRec("w3", from, "gamma", 7),
	}
}

// TestRoundTripNoDataLoss cobre o round-trip expand → migrate → contract SEM perda
// de dados: no fim, cada registo está na forma To e a inversão (Down) reproduz
// EXACTAMENTE o registo From original.
func TestRoundTripNoDataLoss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from, to := "1.0.0", "1.1.0" // MINOR: sem gate
	initial := initialSet(from)

	mig := makeMigration("mig-roundtrip", from, to)
	r, err := migrations.NewRunner(mig, initial, migrations.WithGate(migrations.NewEvalGate()))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	if err := r.Expand(ctx); err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if err := r.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := r.Contract(ctx); err != nil {
		t.Fatalf("Contract: %v", err)
	}
	if r.Phase() != migrations.PhaseContract {
		t.Fatalf("fase final = %s, quero contract", r.Phase())
	}

	if r.Count() != len(initial) {
		t.Fatalf("contagem final = %d, quero %d (nenhum registo perdido)", r.Count(), len(initial))
	}

	// Cada registo canónico está em To e o seu conteúdo carrega o sufixo (todo o
	// dado migrado); invertê-lo reproduz o original -> sem perda.
	mig2 := makeMigration("inv", from, to) // par Up/Down para inverter
	for _, orig := range initial {
		got, err := r.ReadCanonical(orig.ID)
		if err != nil {
			t.Fatalf("ReadCanonical(%s): %v", orig.ID, err)
		}
		if got.Metadata.SchemaVersion != to {
			t.Fatalf("%s: schema_version = %s, quero %s", orig.ID, got.Metadata.SchemaVersion, to)
		}
		back, err := mig2.Down(got)
		if err != nil {
			t.Fatalf("Down(%s): %v", orig.ID, err)
		}
		if contentOf(back) != contentOf(orig) {
			t.Fatalf("%s: apos round-trip+inversao content = %q, quero %q", orig.ID, contentOf(back), contentOf(orig))
		}
		if back.Metadata.SchemaVersion != from {
			t.Fatalf("%s: inversao schema_version = %s, quero %s", orig.ID, back.Metadata.SchemaVersion, from)
		}
	}
}

// TestDualWriteDualRead cobre a fase expand: um registo é legível em AMBOS os
// schemas (dual-read), e uma escrita nova durante expand grava as duas formas
// (dual-write).
func TestDualWriteDualRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from, to := "1.0.0", "1.1.0"
	fromV, toV := ver(from), ver(to)
	initial := initialSet(from)

	mig := makeMigration("mig-dual", from, to)
	r, err := migrations.NewRunner(mig, initial, migrations.WithGate(migrations.NewEvalGate()))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Expand(ctx); err != nil {
		t.Fatalf("Expand: %v", err)
	}

	// Leitura canónica em expand ainda serve From (sem downtime).
	c, _ := r.ReadCanonical("w1")
	if c.Metadata.SchemaVersion != from {
		t.Fatalf("canonica em expand = %s, quero %s (leitura ainda em From)", c.Metadata.SchemaVersion, from)
	}

	// Dual-read: ambas as formas legíveis para todos os registos iniciais.
	for _, orig := range initial {
		if !r.HasBoth(orig.ID) {
			t.Fatalf("%s: esperava ambas as representacoes em expand", orig.ID)
		}
		oldForm, err := r.Read(orig.ID, fromV)
		if err != nil {
			t.Fatalf("Read(%s, From): %v", orig.ID, err)
		}
		newForm, err := r.Read(orig.ID, toV)
		if err != nil {
			t.Fatalf("Read(%s, To): %v", orig.ID, err)
		}
		if oldForm.Metadata.SchemaVersion != from {
			t.Fatalf("%s: forma From = %s", orig.ID, oldForm.Metadata.SchemaVersion)
		}
		if newForm.Metadata.SchemaVersion != to {
			t.Fatalf("%s: forma To = %s", orig.ID, newForm.Metadata.SchemaVersion)
		}
		if contentOf(oldForm) != contentOf(orig) {
			t.Fatalf("%s: forma From content = %q, quero %q", orig.ID, contentOf(oldForm), contentOf(orig))
		}
		if contentOf(newForm) != contentOf(orig)+suffix {
			t.Fatalf("%s: forma To content = %q, quero %q", orig.ID, contentOf(newForm), contentOf(orig)+suffix)
		}
	}

	// Dual-write: uma escrita nova em expand grava AMBAS as formas.
	if err := r.Put(ctx, workingRec("w4", from, "delta", 9)); err != nil {
		t.Fatalf("Put dual-write: %v", err)
	}
	if !r.HasBoth("w4") {
		t.Fatalf("w4: dual-write nao gravou ambas as formas")
	}
	nf, _ := r.Read("w4", toV)
	if contentOf(nf) != "delta"+suffix {
		t.Fatalf("w4 forma To = %q, quero %q", contentOf(nf), "delta"+suffix)
	}
}

// TestRollbackFailedMigration cobre o rollback de uma migração FALHADA: o estado
// fica IDÊNTICO ao inicial (sem perda nem corrupção). Duas vias: a falha directa
// da fase Expand e o rollback transacional de Run.
func TestRollbackFailedMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from, to := "1.0.0", "1.1.0"
	initial := initialSet(from)

	t.Run("expand_falha_estado_inalterado", func(t *testing.T) {
		t.Parallel()
		mig := makeFailingMigration("mig-fail", from, to, "beta") // w2 dispara boom
		r, err := migrations.NewRunner(mig, initialSet(from), migrations.WithGate(migrations.NewEvalGate()))
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		err = r.Expand(ctx)
		if !errors.Is(err, migrations.ErrTransformFailed) {
			t.Fatalf("Expand: erro = %v, quero ErrTransformFailed", err)
		}
		if r.Phase() != migrations.PhaseNone {
			t.Fatalf("fase apos falha = %s, quero none (rollback)", r.Phase())
		}
		// Estado idêntico ao inicial: cada registo canónico == original, sem novas formas.
		for _, orig := range initial {
			got, err := r.ReadCanonical(orig.ID)
			if err != nil {
				t.Fatalf("ReadCanonical(%s): %v", orig.ID, err)
			}
			if got.Metadata.SchemaVersion != from || contentOf(got) != contentOf(orig) {
				t.Fatalf("%s: estado alterado apos rollback: %q@%s", orig.ID, contentOf(got), got.Metadata.SchemaVersion)
			}
			if r.HasBoth(orig.ID) {
				t.Fatalf("%s: rollback deixou forma nova pendurada", orig.ID)
			}
		}
	})

	t.Run("run_transacional_reverte_ao_inicial", func(t *testing.T) {
		t.Parallel()
		mig := makeFailingMigration("mig-fail-run", from, to, "gamma") // w3 dispara boom
		r, err := migrations.NewRunner(mig, initialSet(from), migrations.WithGate(migrations.NewEvalGate()))
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		if err := r.Run(ctx); !errors.Is(err, migrations.ErrTransformFailed) {
			t.Fatalf("Run: erro = %v, quero ErrTransformFailed", err)
		}
		if r.Phase() != migrations.PhaseNone {
			t.Fatalf("fase apos Run falhado = %s, quero none", r.Phase())
		}
		for _, orig := range initial {
			got, _ := r.ReadCanonical(orig.ID)
			if contentOf(got) != contentOf(orig) || got.Metadata.SchemaVersion != from {
				t.Fatalf("%s: Run falhado nao reverteu ao inicial", orig.ID)
			}
		}
	})
}

// TestRevertsPerPhase prova que CADA fase é reversível de forma independente e
// que o revert restaura exactamente o estado anterior.
func TestRevertsPerPhase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from, to := "1.0.0", "1.1.0"
	fromV, toV := ver(from), ver(to)
	initial := initialSet(from)

	mig := makeMigration("mig-revert", from, to)
	r, err := migrations.NewRunner(mig, initial, migrations.WithGate(migrations.NewEvalGate()))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	// expand -> revert -> estado inicial.
	if err := r.Expand(ctx); err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if err := r.RevertExpand(ctx); err != nil {
		t.Fatalf("RevertExpand: %v", err)
	}
	if r.Phase() != migrations.PhaseNone || r.HasBoth("w1") {
		t.Fatalf("RevertExpand nao voltou ao inicial")
	}

	// expand -> migrate -> revert migrate -> volta a expand (canonica From).
	if err := r.Expand(ctx); err != nil {
		t.Fatalf("Expand2: %v", err)
	}
	if err := r.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	cm, _ := r.ReadCanonical("w1")
	if cm.Metadata.SchemaVersion != to {
		t.Fatalf("canonica em migrate = %s, quero To", cm.Metadata.SchemaVersion)
	}
	if err := r.RevertMigrate(ctx); err != nil {
		t.Fatalf("RevertMigrate: %v", err)
	}
	ce, _ := r.ReadCanonical("w1")
	if r.Phase() != migrations.PhaseExpand || ce.Metadata.SchemaVersion != from {
		t.Fatalf("RevertMigrate nao repos leitura em From")
	}

	// migrate -> contract -> revert contract -> repõe forma antiga (via Down).
	if err := r.Migrate(ctx); err != nil {
		t.Fatalf("Migrate2: %v", err)
	}
	if err := r.Contract(ctx); err != nil {
		t.Fatalf("Contract: %v", err)
	}
	if r.HasBoth("w1") {
		t.Fatalf("contract devia ter removido a forma antiga")
	}
	if err := r.RevertContract(ctx); err != nil {
		t.Fatalf("RevertContract: %v", err)
	}
	if r.Phase() != migrations.PhaseMigrate || !r.HasBoth("w1") {
		t.Fatalf("RevertContract nao recompos a forma antiga")
	}
	oldForm, _ := r.Read("w1", fromV)
	if contentOf(oldForm) != "alpha" {
		t.Fatalf("forma antiga recomposta = %q, quero alpha", contentOf(oldForm))
	}
	_, _ = r.Read("w1", toV)
}

// TestPhaseOrderFailClosed prova que fases fora de ordem são recusadas.
func TestPhaseOrderFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mig := makeMigration("mig-order", "1.0.0", "1.1.0")
	r, err := migrations.NewRunner(mig, initialSet("1.0.0"), migrations.WithGate(migrations.NewEvalGate()))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Migrate(ctx); !errors.Is(err, migrations.ErrPhaseOrder) {
		t.Fatalf("Migrate antes de Expand: erro = %v, quero ErrPhaseOrder", err)
	}
	if err := r.Contract(ctx); !errors.Is(err, migrations.ErrPhaseOrder) {
		t.Fatalf("Contract antes de Migrate: erro = %v, quero ErrPhaseOrder", err)
	}
}

// TestNewRunnerRejectsWrongSchema prova o fail-closed na construção: registos que
// não estejam na versão From são rejeitados.
func TestNewRunnerRejectsWrongSchema(t *testing.T) {
	t.Parallel()
	mig := makeMigration("mig-mismatch", "1.0.0", "1.1.0")
	bad := []domain.Record{workingRec("x", "1.1.0", "wrong", 1)} // já em To
	if _, err := migrations.NewRunner(mig, bad); !errors.Is(err, migrations.ErrRecordSchemaMismatch) {
		t.Fatalf("NewRunner: erro = %v, quero ErrRecordSchemaMismatch", err)
	}
}

func TestInvalidMigrationRejected(t *testing.T) {
	t.Parallel()
	cases := map[string]migrations.Migration{
		"id_vazio":     {Class: domain.ClassWorking, From: ver("1.0.0"), To: ver("1.1.0"), Up: idTransform, Down: idTransform},
		"classe_inval": {ID: "x", Class: domain.MemoryClass("bad"), From: ver("1.0.0"), To: ver("1.1.0"), Up: idTransform, Down: idTransform},
		"up_nil":       {ID: "x", Class: domain.ClassWorking, From: ver("1.0.0"), To: ver("1.1.0"), Down: idTransform},
		"from_eq_to":   {ID: "x", Class: domain.ClassWorking, From: ver("1.0.0"), To: ver("1.0.0"), Up: idTransform, Down: idTransform},
	}
	for name, m := range cases {
		m := m
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := migrations.NewRunner(m, nil); !errors.Is(err, migrations.ErrInvalidMigration) {
				t.Fatalf("erro = %v, quero ErrInvalidMigration", err)
			}
		})
	}
}

func idTransform(r domain.Record) (domain.Record, error) { return r, nil }
