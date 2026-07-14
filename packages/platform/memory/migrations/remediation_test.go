package migrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/migrations"
	"github.com/aos-ref/platform/memory/schema"
	"github.com/aos-ref/substrate/eventstore"
)

// scriptedStore é um EventAppender em memória, com deduplicação por
// (RunID,StepID) e uma falha INJECTÁVEL por predicado sobre o EventInput. Dá
// controlo fino para exercitar os caminhos de rollback/compensação do motor (ex.:
// fazer a fase contract falhar deixando expand/migrate já registados).
type scriptedStore struct {
	mu     sync.Mutex
	events []eventstore.Event
	failFn func(in eventstore.EventInput) error
}

func (s *scriptedStore) Append(_ context.Context, streamID string, in eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFn != nil {
		if err := s.failFn(in); err != nil {
			return eventstore.AppendResult{}, err
		}
	}
	for _, e := range s.events {
		if e.RunID == in.RunID && e.StepID == in.StepID {
			return eventstore.AppendResult{Seq: e.Seq, Status: eventstore.StatusDuplicate, Event: e}, nil
		}
	}
	ev := eventstore.Event{
		StreamID: streamID,
		Seq:      uint64(len(s.events) + 1),
		Type:     in.Type,
		Payload:  in.Payload,
		RunID:    in.RunID,
		StepID:   in.StepID,
	}
	s.events = append(s.events, ev)
	return eventstore.AppendResult{Seq: ev.Seq, Status: eventstore.StatusCommitted, Event: ev}, nil
}

func (s *scriptedStore) Read(_ context.Context, _ string, fromSeq uint64) ([]eventstore.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return nil, eventstore.ErrStreamNotFound
	}
	out := make([]eventstore.Event, 0, len(s.events))
	for _, e := range s.events {
		if e.Seq >= fromSeq {
			out = append(out, e)
		}
	}
	return out, nil
}

// failOnPhase devolve um failFn que falha o Append de um evento cuja fase (no
// payload) seja a dada — usado para forçar a falha de uma fase específica.
func failOnPhase(phase migrations.Phase, err error) func(eventstore.EventInput) error {
	return func(in eventstore.EventInput) error {
		var rec migrations.AppliedRecord
		if json.Unmarshal(in.Payload, &rec) == nil && rec.Phase == phase && in.Type == "memory.migration.applied" {
			return err
		}
		return nil
	}
}

// makeDataLosingMigration constrói uma migração rotulada MINOR cujo Up DESCARTA o
// Content (perde dados); o Down não o consegue reconstruir, logo Down(Up(x)) != x.
func makeDataLosingMigration(id, from, to string) migrations.Migration {
	m := makeMigration(id, from, to)
	m.Up = func(r domain.Record) (domain.Record, error) {
		b := r.Body.(domain.WorkingBody)
		b.Content = "" // perde o conteúdo
		out := r.Clone()
		out.Body = b
		out.Metadata.SchemaVersion = to
		return out, nil
	}
	return m
}

// TestExpandRejectsIrreversibleMigration (Finding gate-fail-open): uma migração
// MINOR (que o eval-gate deixa passar) mas que perde dados é RECUSADA pelo backstop
// semântico do Expand (round-trip Down(Up(x)) != x), sem tocar no estado.
func TestExpandRejectsIrreversibleMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from, to := "1.0.0", "1.1.0" // MINOR: gate deixaria passar
	mig := makeDataLosingMigration("mig-lossy", from, to)
	if mig.Kind() != schema.ChangeMinor {
		t.Fatalf("pre-condicao: Kind = %s, quero minor", mig.Kind())
	}
	r, err := migrations.NewRunner(mig, initialSet(from), migrations.WithGate(migrations.NewEvalGate()))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Expand(ctx); !errors.Is(err, migrations.ErrIrreversibleMigration) {
		t.Fatalf("Expand: erro = %v, quero ErrIrreversibleMigration", err)
	}
	if r.Phase() != migrations.PhaseNone {
		t.Fatalf("fase apos recusa = %s, quero none (nada aplicado)", r.Phase())
	}
	if r.HasBoth("w1") {
		t.Fatalf("recusa deixou forma nova pendurada")
	}
}

// TestRunRollbackRevertsClassVersion (Findings rollback-parcial / partial-state):
// se Contract falhar após Migrate ter avançado a versão da classe, o rollback
// transacional de Run repõe a versão da classe em From (sem estado híbrido).
func TestRunRollbackRevertsClassVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from, to := "1.0.0", "1.1.0"
	boom := errors.New("store indisponivel na fase contract")
	store := &scriptedStore{failFn: failOnPhase(migrations.PhaseContract, boom)}
	reg := migrations.NewRegistry(store)
	classReg := schema.DefaultClassRegistry()
	mig := makeMigration("mig-rollback-classver", from, to)

	r, err := migrations.NewRunner(mig, initialSet(from),
		migrations.WithGate(migrations.NewEvalGate()),
		migrations.WithRegistry(reg),
		migrations.WithClassRegistry(classReg),
	)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Run(ctx); !errors.Is(err, boom) {
		t.Fatalf("Run: erro = %v, quero boom da fase contract", err)
	}
	if r.Phase() != migrations.PhaseNone {
		t.Fatalf("fase apos Run falhado = %s, quero none", r.Phase())
	}
	// A versão da classe TEM de ter voltado a From — o coração do finding.
	if cur, _ := classReg.Current(mig.Class); cur.String() != from {
		t.Fatalf("versao da classe apos rollback = %s, quero %s (sem estado hibrido)", cur, from)
	}
	// E a linhagem durável reflecte o estado efectivo (compensação): fase efectiva none.
	eff, err := reg.EffectivePhase(ctx, mig.ID)
	if err != nil {
		t.Fatalf("EffectivePhase: %v", err)
	}
	if eff != migrations.PhaseNone {
		t.Fatalf("fase efectiva apos rollback = %s, quero none (expand/migrate compensados)", eff)
	}
}

// TestRunRollbackNoRegistryRevertsClassVersion cobre o rollback quando Migrate
// falha (Register não-monótono) e NÃO há registo durável ligado: a versão da classe
// mantém-se coerente e a compensação é um no-op silencioso.
func TestRunRollbackNoRegistryRevertsClassVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from, to := "1.0.0", "1.1.0"
	classReg := schema.DefaultClassRegistry()
	// A classe working já está à frente (2.0.0): Migrate.Register(1.1.0) será rejeitado.
	if err := classReg.Register(domain.ClassWorking, ver("2.0.0")); err != nil {
		t.Fatalf("setup Register 2.0.0: %v", err)
	}
	mig := makeMigration("mig-migrate-fail", from, to)
	r, err := migrations.NewRunner(mig, initialSet(from),
		migrations.WithGate(migrations.NewEvalGate()),
		migrations.WithClassRegistry(classReg),
	)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Run(ctx); !errors.Is(err, schema.ErrNonMonotonic) {
		t.Fatalf("Run: erro = %v, quero ErrNonMonotonic da fase migrate", err)
	}
	if r.Phase() != migrations.PhaseNone {
		t.Fatalf("fase apos Run falhado = %s, quero none", r.Phase())
	}
	// A versão da classe fica intacta (o snapshot capturou 2.0.0; restore não regride).
	if cur, _ := classReg.Current(mig.Class); cur.String() != "2.0.0" {
		t.Fatalf("versao da classe = %s, quero 2.0.0 (inalterada)", cur)
	}
}

// TestRevertMigrateRevertsClassVersion (Findings reversibility-gap /
// rollback-inconsistente): RevertMigrate repõe a versão de schema da classe em From,
// cumprindo o contrato documentado.
func TestRevertMigrateRevertsClassVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from, to := "1.0.0", "1.1.0"
	classReg := schema.DefaultClassRegistry()
	mig := makeMigration("mig-revmig-classver", from, to)
	r, err := migrations.NewRunner(mig, initialSet(from),
		migrations.WithGate(migrations.NewEvalGate()),
		migrations.WithClassRegistry(classReg),
	)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Expand(ctx); err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if err := r.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if cur, _ := classReg.Current(mig.Class); cur.String() != to {
		t.Fatalf("apos migrate versao da classe = %s, quero %s", cur, to)
	}
	if err := r.RevertMigrate(ctx); err != nil {
		t.Fatalf("RevertMigrate: %v", err)
	}
	if r.Phase() != migrations.PhaseExpand {
		t.Fatalf("fase apos RevertMigrate = %s, quero expand", r.Phase())
	}
	// A versão da classe TEM de ter voltado a From — o contrato do doc agora cumpre-se.
	if cur, _ := classReg.Current(mig.Class); cur.String() != from {
		t.Fatalf("apos RevertMigrate versao da classe = %s, quero %s", cur, from)
	}
}

// TestEffectivePhaseFromLog (Finding idempotencia-durabilidade): o motor consegue
// consultar a linhagem durável para determinar a fase EFECTIVA (aplicada e não
// compensada), em vez de arrancar sempre às cegas em none.
func TestEffectivePhaseFromLog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reg := migrations.NewRegistry(newStore(t))
	mig := makeMigration("mig-effphase", "1.0.0", "1.1.0")

	// Stream ainda inexistente: fase efectiva none.
	if eff, err := reg.EffectivePhase(ctx, mig.ID); err != nil || eff != migrations.PhaseNone {
		t.Fatalf("EffectivePhase inicial = %s, %v; quero none, nil", eff, err)
	}

	for _, p := range []migrations.Phase{migrations.PhaseExpand, migrations.PhaseMigrate, migrations.PhaseContract} {
		if _, err := reg.Record(ctx, mig, p); err != nil {
			t.Fatalf("Record %s: %v", p, err)
		}
		eff, err := reg.EffectivePhase(ctx, mig.ID)
		if err != nil {
			t.Fatalf("EffectivePhase apos %s: %v", p, err)
		}
		if eff != p {
			t.Fatalf("fase efectiva apos aplicar %s = %s", p, eff)
		}
	}

	// Compensa contract: a fase efectiva recua para migrate (log honesto).
	if applied, err := reg.RecordRevert(ctx, mig, migrations.PhaseContract); err != nil || !applied {
		t.Fatalf("RecordRevert contract: applied=%v err=%v", applied, err)
	}
	if eff, _ := reg.EffectivePhase(ctx, mig.ID); eff != migrations.PhaseMigrate {
		t.Fatalf("fase efectiva apos compensar contract = %s, quero migrate", eff)
	}
	// RecordRevert é idempotente.
	if applied, err := reg.RecordRevert(ctx, mig, migrations.PhaseContract); err != nil || applied {
		t.Fatalf("RecordRevert repetido: applied=%v err=%v (quero false, nil)", applied, err)
	}

	// Registry nil: no-ops seguros.
	var nilReg *migrations.Registry
	if eff, err := nilReg.EffectivePhase(ctx, mig.ID); err != nil || eff != migrations.PhaseNone {
		t.Fatalf("nil EffectivePhase: %s %v", eff, err)
	}
	if applied, err := nilReg.RecordRevert(ctx, mig, migrations.PhaseExpand); err != nil || applied {
		t.Fatalf("nil RecordRevert: %v %v", applied, err)
	}
}

// TestRecordRejectsRedefinedMigration (Finding idempotency-key): reutilizar um ID
// já registado com um From/To diferente é RECUSADO fail-closed; a mesma definição
// continua a deduplicar (no-op).
func TestRecordRejectsRedefinedMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reg := migrations.NewRegistry(newStore(t))

	orig := makeMigration("mig-shared-id", "1.0.0", "1.1.0")
	if applied, err := reg.Record(ctx, orig, migrations.PhaseExpand); err != nil || !applied {
		t.Fatalf("Record original: applied=%v err=%v", applied, err)
	}

	// Mesmo ID e fase, mas To diferente: redefinição in-place -> fail-closed.
	redef := makeMigration("mig-shared-id", "1.0.0", "1.2.0")
	if _, err := reg.Record(ctx, redef, migrations.PhaseExpand); !errors.Is(err, migrations.ErrMigrationRedefined) {
		t.Fatalf("Record redefinido: erro = %v, quero ErrMigrationRedefined", err)
	}

	// A MESMA definição volta a deduplicar (no-op), sem erro.
	if applied, err := reg.Record(ctx, orig, migrations.PhaseExpand); err != nil || applied {
		t.Fatalf("Record mesma def: applied=%v err=%v (quero false, nil)", applied, err)
	}
}

// TestRecordRevertPropagatesStoreError cobre o ramo de erro de RecordRevert.
func TestRecordRevertPropagatesStoreError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	boom := errors.New("append da compensacao falhou")
	reg := migrations.NewRegistry(errAppender{err: boom})
	mig := makeMigration("mig-revert-err", "1.0.0", "1.1.0")
	if _, err := reg.RecordRevert(ctx, mig, migrations.PhaseExpand); !errors.Is(err, boom) {
		t.Fatalf("RecordRevert erro = %v, quero boom", err)
	}
}
