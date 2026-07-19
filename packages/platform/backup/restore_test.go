package backup

import (
	"context"
	"testing"
)

// exportSeeded monta um exportador, semeia streams e corre um ciclo, devolvendo o
// exportador, o destino e o manifesto/checkpoint correntes.
func exportSeeded(t *testing.T) (*Exporter, *InMemoryImmutableStore) {
	t.Helper()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 4, "m")
	seed(t, src, "run-b", 2, "m")
	exp, dst := newExporter(t, src, "eu-west")
	if _, err := exp.Export(context.Background()); err != nil {
		t.Fatalf("Export: %v", err)
	}
	return exp, dst
}

func TestRestore_FullRoundTrip(t *testing.T) {
	ctx := context.Background()
	exp, dst := exportSeeded(t)

	rst, err := NewRestorer(dst, exp.Vault(), exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	out := freshDest(t, "board-eu", "eu-west")
	ev, err := rst.RestoreTo(ctx, exp.Manifest(), exp.Checkpoint(), 0, nil, out)
	if err != nil {
		t.Fatalf("RestoreTo: %v", err)
	}
	if !ev.Verified || ev.EventsRestored != 6 {
		t.Fatalf("evidência inesperada: %+v", ev)
	}
	// O Store restaurado tem de reproduzir os streams com o envelope preservado.
	got, err := out.Read(ctx, "run-a", 1)
	if err != nil {
		t.Fatalf("Read run-a: %v", err)
	}
	orig, _ := exp.src.SnapshotStream(ctx, "run-a", 0)
	if len(got) != len(orig) {
		t.Fatalf("run-a restaurado com %d eventos, quero %d", len(got), len(orig))
	}
	for i := range got {
		if got[i].EventID != orig[i].EventID || got[i].Seq != orig[i].Seq || got[i].Ts != orig[i].Ts {
			t.Fatalf("run-a#%d envelope divergente", i)
		}
	}
}

func TestRestore_PITRToTargetSeq(t *testing.T) {
	ctx := context.Background()
	exp, dst := exportSeeded(t)

	rst, err := NewRestorer(dst, exp.Vault(), exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	out := freshDest(t, "board-eu", "eu-west")

	// PITR: run-a até seq 2, run-b por inteiro (ausente do target).
	target := map[string]uint64{"run-a": 2}
	ev, err := rst.RestoreTo(ctx, exp.Manifest(), exp.Checkpoint(), 0, target, out)
	if err != nil {
		t.Fatalf("RestoreTo: %v", err)
	}
	if ev.HeadSeq["run-a"] != 2 {
		t.Fatalf("run-a head restaurado=%d, quero 2", ev.HeadSeq["run-a"])
	}
	if ev.HeadSeq["run-b"] != 2 {
		t.Fatalf("run-b head restaurado=%d, quero 2 (completo)", ev.HeadSeq["run-b"])
	}
	head, err := out.StreamHead(ctx, "run-a")
	if err != nil {
		t.Fatalf("StreamHead: %v", err)
	}
	if head != 2 {
		t.Fatalf("PITR run-a head=%d, quero 2", head)
	}
	if ev.EventsRestored != 4 { // run-a(2) + run-b(2)
		t.Fatalf("EventsRestored=%d, quero 4", ev.EventsRestored)
	}
}

func TestRestore_EvidenceRecorded(t *testing.T) {
	ctx := context.Background()
	exp, dst := exportSeeded(t)
	rst, err := NewRestorer(dst, exp.Vault(), exp.Public(), WithRestoreClock(fixedClock(t0)))
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	// Antes de restaurar não há evidência.
	if rst.LastEvidence().Verified {
		t.Fatalf("não devia haver evidência antes do restauro")
	}
	out := freshDest(t, "board-eu", "eu-west")
	if _, err := rst.RestoreTo(ctx, exp.Manifest(), exp.Checkpoint(), 0, nil, out); err != nil {
		t.Fatalf("RestoreTo: %v", err)
	}
	last := rst.LastEvidence()
	if !last.Verified || last.Timestamp != t0 || last.Cycle != 1 {
		t.Fatalf("evidência do último restauro inesperada: %+v", last)
	}
}

func TestRestore_MultiSegmentPITR(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	exp, dst := newExporter(t, src, "eu-west")

	// Três ciclos incrementais.
	seed(t, src, "run-a", 2, "m")
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export#1: %v", err)
	}
	seed(t, src, "run-a", 2, "m")
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export#2: %v", err)
	}
	seed(t, src, "run-a", 2, "m")
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export#3: %v", err)
	}

	rst, err := NewRestorer(dst, exp.Vault(), exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	out := freshDest(t, "board-eu", "eu-west")
	// PITR a seq 5, atravessa os três segmentos.
	ev, err := rst.RestoreTo(ctx, exp.Manifest(), exp.Checkpoint(), 0, map[string]uint64{"run-a": 5}, out)
	if err != nil {
		t.Fatalf("RestoreTo: %v", err)
	}
	if ev.HeadSeq["run-a"] != 5 || ev.EventsRestored != 5 {
		t.Fatalf("PITR multi-segmento inesperado: %+v", ev)
	}
}
