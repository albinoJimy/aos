package dr_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/backup"
	"github.com/aos-ref/platform/dr"
	"github.com/aos-ref/substrate/eventstore"
)

// newRecoverer monta um Recoverer sobre o pipeline (factory na fronteira eu).
func (p *pipeline) newRecoverer(t *testing.T) *dr.Recoverer {
	t.Helper()
	rec, err := dr.NewRecoverer(dr.MapResolver{drBoard: drRegion}, p.drFactory(), p.restorer,
		dr.WithClock(stepClock(baseTime, time.Minute)))
	if err != nil {
		t.Fatalf("NewRecoverer: %v", err)
	}
	return rec
}

// ---------------------------------------------------------------------------
// FAIL-CLOSED: uma ADULTERAÇÃO do backup ABORTA o DR (VerifyManifest embutido no
// RestoreTo). O serviço não é dado por restabelecido.
// ---------------------------------------------------------------------------

func TestRecover_TamperedBackup_Aborts(t *testing.T) {
	var trajHits, effectHits effectCounter
	p := buildOriginal(t, &trajHits, &effectHits)
	rec := p.newRecoverer(t)

	var resumeHits effectCounter
	r := p.recovery(t, &resumeHits)
	// Adultera o manifesto: vira um byte do content-hash do primeiro segmento.
	m := p.exporter.Manifest()
	if len(m.Segments) == 0 || len(m.Segments[0].ContentHash) == 0 {
		t.Fatal("manifesto sem segmentos/content-hash")
	}
	m.Segments[0].ContentHash[0] ^= 0xFF
	r.Manifest = m

	_, err := rec.Recover(context.Background(), r)
	if !errors.Is(err, backup.ErrSegmentTampered) {
		t.Fatalf("esperava ErrSegmentTampered, obtive %v", err)
	}
	if resumeHits.n() != 0 {
		t.Fatalf("retoma correu apesar do abort: %d efeitos", resumeHits.n())
	}
}

// ---------------------------------------------------------------------------
// FAIL-CLOSED: uma DIVERGÊNCIA de replay (Spec evoluído) ABORTA o DR (AC3). O
// restauro e a verificação do WORM passam, mas a fidelidade < 100% recusa a retoma.
// ---------------------------------------------------------------------------

func TestRecover_ReplayDivergence_Aborts(t *testing.T) {
	var trajHits, effectHits effectCounter
	p := buildOriginal(t, &trajHits, &effectHits)
	rec := p.newRecoverer(t)

	var resumeHits effectCounter
	r := p.recovery(t, &resumeHits)
	// System evoluído ⇒ o prompt re-materializado diverge do gravado ⇒ Fidelity<1.0.
	r.Spec.System = "SYSTEM PROMPT DIFERENTE (código evoluído)"

	_, err := rec.Recover(context.Background(), r)
	if !errors.Is(err, dr.ErrReplayInfidelity) {
		t.Fatalf("esperava ErrReplayInfidelity, obtive %v", err)
	}
	if resumeHits.n() != 0 {
		t.Fatalf("retoma correu apesar da divergência: %d efeitos", resumeHits.n())
	}
}

// ---------------------------------------------------------------------------
// FAIL-CLOSED: um rollback do checkpoint do WORM (ExpectedHead > AuditSeq) ABORTA o
// DR ANTES de retomar (AC5).
// ---------------------------------------------------------------------------

func TestRecover_StaleAuditCheckpoint_Aborts(t *testing.T) {
	var trajHits, effectHits effectCounter
	p := buildOriginal(t, &trajHits, &effectHits)
	rec := p.newRecoverer(t)

	var resumeHits effectCounter
	r := p.recovery(t, &resumeHits)
	// Piso de frescura acima do head selado ⇒ o checkpoint é um rollback.
	r.Audit.ExpectedHead = p.wormHead + 1

	_, err := rec.Recover(context.Background(), r)
	if !errors.Is(err, audit.ErrCheckpointStale) {
		t.Fatalf("esperava ErrCheckpointStale, obtive %v", err)
	}
	if resumeHits.n() != 0 {
		t.Fatalf("retoma correu apesar do WORM stale: %d efeitos", resumeHits.n())
	}
}

// ---------------------------------------------------------------------------
// SOBERANIA (AC6): o failover de DR NÃO cruza a fronteira.
// ---------------------------------------------------------------------------

// (a) Ao nível do Event Store: uma réplica fora da fronteira do board é recusada por
// construção (WithSovereigntyBoard + WithReplicaRegions cross-border).
func TestSovereignty_CrossBorderReplica_Rejected(t *testing.T) {
	_, err := eventstore.New(
		eventstore.WithReplicas(3),
		eventstore.WithSovereigntyBoard(drBoard, drRegion),
		eventstore.WithReplicaRegions(drRegion, "us-east", drRegion), // uma réplica cross-border
	)
	if !errors.Is(err, eventstore.ErrSovereigntyViolation) {
		t.Fatalf("esperava ErrSovereigntyViolation, obtive %v", err)
	}
}

// (b) Ao nível do orquestrador: uma fábrica que devolve um Store noutra região é
// apanhada pela asserção região(Store)==região-alvo — o DR aborta cross-border.
func TestRecover_CrossBorderFactory_Rejected(t *testing.T) {
	var trajHits, effectHits effectCounter
	p := buildOriginal(t, &trajHits, &effectHits)

	// Fábrica desalinhada: constrói o Store numa região DIFERENTE da resolvida.
	crossFactory := dr.StoreFactory(func(board, region string) (*eventstore.Store, error) {
		s, err := eventstore.New(eventstore.WithReplicas(3), eventstore.WithSovereigntyBoard(board, "us-east"))
		if err != nil {
			return nil, err
		}
		return s, nil
	})
	rec, err := dr.NewRecoverer(dr.MapResolver{drBoard: drRegion}, crossFactory, p.restorer,
		dr.WithClock(stepClock(baseTime, time.Minute)))
	if err != nil {
		t.Fatalf("NewRecoverer: %v", err)
	}

	var resumeHits effectCounter
	_, err = rec.Recover(context.Background(), p.recovery(t, &resumeHits))
	if !errors.Is(err, dr.ErrRegionMismatch) {
		t.Fatalf("esperava ErrRegionMismatch, obtive %v", err)
	}
	if resumeHits.n() != 0 {
		t.Fatalf("retoma correu cross-border: %d efeitos", resumeHits.n())
	}
}

// Um board desconhecido (sem região resolvida) aborta fail-closed (fronteira desconhecida).
func TestRecover_UnknownBoard_Aborts(t *testing.T) {
	var trajHits, effectHits effectCounter
	p := buildOriginal(t, &trajHits, &effectHits)
	rec, err := dr.NewRecoverer(dr.MapResolver{"outro-board": drRegion}, p.drFactory(), p.restorer,
		dr.WithClock(stepClock(baseTime, time.Minute)))
	if err != nil {
		t.Fatalf("NewRecoverer: %v", err)
	}
	var resumeHits effectCounter
	_, err = rec.Recover(context.Background(), p.recovery(t, &resumeHits))
	if !errors.Is(err, dr.ErrUnknownBoard) {
		t.Fatalf("esperava ErrUnknownBoard, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// FAIL-CLOSED: se a retoma reportar efeitos duplicados, o orquestrador aborta
// (defesa em profundidade sobre a garantia estrutural do StepLedger).
// ---------------------------------------------------------------------------

func TestRecover_DuplicatedEffects_Aborts(t *testing.T) {
	var trajHits, effectHits effectCounter
	p := buildOriginal(t, &trajHits, &effectHits)
	rec := p.newRecoverer(t)

	r := p.recovery(t, &effectCounter{})
	// Uma retoma que MENTE ao reportar duplicação tem de abortar o DR.
	r.Resume = func(_ context.Context, _ *eventstore.Store) (dr.ResumeEvidence, error) {
		return dr.ResumeEvidence{ResumeTurn: 1, Executed: 1, DuplicatedEffects: 1}, nil
	}
	_, err := rec.Recover(context.Background(), r)
	if !errors.Is(err, dr.ErrDuplicatedEffects) {
		t.Fatalf("esperava ErrDuplicatedEffects, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Unidade: resolver, construção fail-closed, validação e alvos do game day.
// ---------------------------------------------------------------------------

func TestMapResolver(t *testing.T) {
	r := dr.MapResolver{drBoard: drRegion, "vazio": "  "}
	if got, err := r.RegionForBoard(drBoard); err != nil || got != drRegion {
		t.Fatalf("RegionForBoard(%s)=%q,%v", drBoard, got, err)
	}
	if _, err := r.RegionForBoard("ausente"); !errors.Is(err, dr.ErrUnknownBoard) {
		t.Fatalf("board ausente devia ser ErrUnknownBoard, obtive %v", err)
	}
	if _, err := r.RegionForBoard("vazio"); !errors.Is(err, dr.ErrUnknownBoard) {
		t.Fatalf("região vazia devia ser ErrUnknownBoard, obtive %v", err)
	}
}

func TestResolverFunc(t *testing.T) {
	r := dr.ResolverFunc(func(b string) (string, error) { return "eu", nil })
	if got, _ := r.RegionForBoard("qq"); got != "eu" {
		t.Fatalf("ResolverFunc=%q", got)
	}
}

func TestNewRecoverer_NilDeps(t *testing.T) {
	f := dr.StoreFactory(func(_, _ string) (*eventstore.Store, error) { return nil, nil })
	rst := &backup.Restorer{}
	if _, err := dr.NewRecoverer(nil, f, rst); !errors.Is(err, dr.ErrNilResolver) {
		t.Fatalf("esperava ErrNilResolver, obtive %v", err)
	}
	if _, err := dr.NewRecoverer(dr.MapResolver{}, nil, rst); !errors.Is(err, dr.ErrNilFactory) {
		t.Fatalf("esperava ErrNilFactory, obtive %v", err)
	}
	if _, err := dr.NewRecoverer(dr.MapResolver{}, f, nil); !errors.Is(err, dr.ErrNilRestorer) {
		t.Fatalf("esperava ErrNilRestorer, obtive %v", err)
	}
}

func TestRecover_Validate(t *testing.T) {
	f := dr.StoreFactory(func(_, _ string) (*eventstore.Store, error) { return nil, nil })
	rec, err := dr.NewRecoverer(dr.MapResolver{drBoard: drRegion}, f, &backup.Restorer{})
	if err != nil {
		t.Fatalf("NewRecoverer: %v", err)
	}
	ctx := context.Background()
	base := dr.Recovery{
		Board: drBoard, RunID: drRunID,
		Resume: func(context.Context, *eventstore.Store) (dr.ResumeEvidence, error) { return dr.ResumeEvidence{}, nil },
		Audit:  dr.AuditCheck{Store: audit.NewMemStore()},
	}
	noBoard := base
	noBoard.Board = ""
	if _, err := rec.Recover(ctx, noBoard); !errors.Is(err, dr.ErrEmptyBoard) {
		t.Fatalf("esperava ErrEmptyBoard, obtive %v", err)
	}
	noRun := base
	noRun.RunID = ""
	if _, err := rec.Recover(ctx, noRun); !errors.Is(err, dr.ErrEmptyRunID) {
		t.Fatalf("esperava ErrEmptyRunID, obtive %v", err)
	}
	noResume := base
	noResume.Resume = nil
	if _, err := rec.Recover(ctx, noResume); !errors.Is(err, dr.ErrNilResume) {
		t.Fatalf("esperava ErrNilResume, obtive %v", err)
	}
	noWorm := base
	noWorm.Audit = dr.AuditCheck{}
	if _, err := rec.Recover(ctx, noWorm); !errors.Is(err, dr.ErrNilAuditStore) {
		t.Fatalf("esperava ErrNilAuditStore, obtive %v", err)
	}
}

func TestNewGameDay_Invalid(t *testing.T) {
	var trajHits, effectHits effectCounter
	p := buildOriginal(t, &trajHits, &effectHits)
	rec := p.newRecoverer(t)
	if _, err := dr.NewGameDay(nil, p.exporter, time.Minute, time.Minute, time.Hour); err == nil {
		t.Fatal("esperava erro com recoverer nil")
	}
	if _, err := dr.NewGameDay(rec, nil, time.Minute, time.Minute, time.Hour); err == nil {
		t.Fatal("esperava erro com RPO source nil")
	}
	if _, err := dr.NewGameDay(rec, p.exporter, 0, time.Minute, time.Hour); err == nil {
		t.Fatal("esperava erro com rpoTarget 0")
	}
}
