package eventstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// seedStream escreve n eventos sequenciais num stream e devolve o AppendResult
// final. Usa run_id == stream para exercitar também o índice de dedup por stream.
func seedStream(t *testing.T, s *Store, stream string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 1; i <= n; i++ {
		if _, err := s.Append(ctx, stream, input(stream, fmt.Sprintf("s%d", i), "t", `{"i":`+fmt.Sprint(i)+`}`)); err != nil {
			t.Fatalf("seed %s#%d: %v", stream, i, err)
		}
	}
}

func TestBackup_StreamsSortedAndComplete(t *testing.T) {
	s := mustNew(t)
	seedStream(t, s, "run-c", 2)
	seedStream(t, s, "run-a", 3)
	seedStream(t, s, "run-b", 1)

	got, err := s.Streams()
	if err != nil {
		t.Fatalf("Streams(): %v", err)
	}
	want := []string{"run-a", "run-b", "run-c"}
	if len(got) != len(want) {
		t.Fatalf("Streams()=%v, quero %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Streams()[%d]=%q, quero %q (ordenado)", i, got[i], want[i])
		}
	}
}

func TestBackup_SnapshotPreservesEnvelope(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	seedStream(t, s, "run-a", 4)

	// O snapshot tem de devolver o ENVELOPE intacto: os mesmos EventID/Ts/Seq que
	// um Read normal — nada é reatribuído.
	read, err := s.Read(ctx, "run-a", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	snap, err := s.SnapshotStream(ctx, "run-a", 0)
	if err != nil {
		t.Fatalf("SnapshotStream: %v", err)
	}
	if len(snap) != len(read) {
		t.Fatalf("snapshot tem %d eventos, Read tem %d", len(snap), len(read))
	}
	for i := range read {
		if snap[i].EventID != read[i].EventID || snap[i].Seq != read[i].Seq || snap[i].Ts != read[i].Ts {
			t.Fatalf("envelope divergente em %d: snap=%+v read=%+v", i, snap[i], read[i])
		}
	}
}

func TestBackup_SnapshotThroughSeq(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	seedStream(t, s, "run-a", 5)

	snap, err := s.SnapshotStream(ctx, "run-a", 3)
	if err != nil {
		t.Fatalf("SnapshotStream: %v", err)
	}
	if len(snap) != 3 {
		t.Fatalf("throughSeq=3 devolveu %d eventos, quero 3", len(snap))
	}
	if snap[2].Seq != 3 {
		t.Fatalf("último seq=%d, quero 3", snap[2].Seq)
	}

	head, err := s.StreamHead(ctx, "run-a")
	if err != nil {
		t.Fatalf("StreamHead: %v", err)
	}
	if head != 5 {
		t.Fatalf("StreamHead=%d, quero 5", head)
	}
}

func TestBackup_SnapshotUnknownStream(t *testing.T) {
	s := mustNew(t)
	if _, err := s.SnapshotStream(context.Background(), "nope", 0); !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("SnapshotStream(desconhecido)=%v, quero ErrStreamNotFound", err)
	}
}

func TestRestore_RoundTripPreservesEnvelope(t *testing.T) {
	ctx := context.Background()
	src := mustNew(t)
	seedStream(t, src, "run-a", 4)
	seedStream(t, src, "run-b", 2)

	// Exporta.
	type exported struct {
		stream string
		events []Event
	}
	var dump []exported
	streams, serr := src.Streams()
	if serr != nil {
		t.Fatalf("Streams(): %v", serr)
	}
	for _, st := range streams {
		evs, err := src.SnapshotStream(ctx, st, 0)
		if err != nil {
			t.Fatalf("snapshot %s: %v", st, err)
		}
		dump = append(dump, exported{st, evs})
	}

	// Restaura para um Store novo, preservando o envelope.
	dst := mustNew(t)
	for _, d := range dump {
		if err := dst.IngestStream(ctx, d.stream, d.events); err != nil {
			t.Fatalf("IngestStream %s: %v", d.stream, err)
		}
	}

	// O restauro tem de reproduzir o envelope byte-a-byte.
	for _, d := range dump {
		got, err := dst.Read(ctx, d.stream, 1)
		if err != nil {
			t.Fatalf("Read restaurado %s: %v", d.stream, err)
		}
		if len(got) != len(d.events) {
			t.Fatalf("%s: %d eventos restaurados, quero %d", d.stream, len(got), len(d.events))
		}
		for i := range got {
			if got[i].EventID != d.events[i].EventID || got[i].Seq != d.events[i].Seq ||
				got[i].Ts != d.events[i].Ts || got[i].IdempotencyKey != d.events[i].IdempotencyKey {
				t.Fatalf("%s#%d envelope divergente: got=%+v want=%+v", d.stream, i, got[i], d.events[i])
			}
		}
	}
}

func TestRestore_RejectsNonGapless(t *testing.T) {
	ctx := context.Background()
	src := mustNew(t)
	seedStream(t, src, "run-a", 3)
	evs, err := src.SnapshotStream(ctx, "run-a", 0)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := mustNew(t)
	// Remover o primeiro evento abre um buraco (seq começa em 2) → rejeita.
	if err := dst.IngestStream(ctx, "run-a", evs[1:]); !errors.Is(err, ErrRestoreOrder) {
		t.Fatalf("IngestStream(com buraco)=%v, quero ErrRestoreOrder", err)
	}
}

func TestRestore_RejectsBadEnvelope(t *testing.T) {
	ctx := context.Background()
	src := mustNew(t)
	seedStream(t, src, "run-a", 2)
	evs, err := src.SnapshotStream(ctx, "run-a", 0)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := mustNew(t)
	// StreamID trocado → envelope incoerente.
	tampered := make([]Event, len(evs))
	copy(tampered, evs)
	tampered[0].StreamID = "outro"
	if err := dst.IngestStream(ctx, "run-a", tampered); !errors.Is(err, ErrRestoreEnvelope) {
		t.Fatalf("IngestStream(stream_id trocado)=%v, quero ErrRestoreEnvelope", err)
	}

	// EventID vazio → envelope incoerente.
	tampered2 := make([]Event, len(evs))
	copy(tampered2, evs)
	tampered2[0].EventID = ""
	if err := dst.IngestStream(ctx, "run-a", tampered2); !errors.Is(err, ErrRestoreEnvelope) {
		t.Fatalf("IngestStream(event_id vazio)=%v, quero ErrRestoreEnvelope", err)
	}
}

func TestRestore_IncrementalAppendPreservesGapless(t *testing.T) {
	ctx := context.Background()
	src := mustNew(t)
	seedStream(t, src, "run-a", 5)
	all, err := src.SnapshotStream(ctx, "run-a", 0)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := mustNew(t)
	// Restauro incremental: primeiro 1..3, depois 4..5.
	if err := dst.IngestStream(ctx, "run-a", all[:3]); err != nil {
		t.Fatalf("IngestStream 1..3: %v", err)
	}
	if err := dst.IngestStream(ctx, "run-a", all[3:]); err != nil {
		t.Fatalf("IngestStream 4..5: %v", err)
	}
	head, err := dst.StreamHead(ctx, "run-a")
	if err != nil {
		t.Fatalf("StreamHead: %v", err)
	}
	if head != 5 {
		t.Fatalf("head restaurado=%d, quero 5", head)
	}
}

func TestBackup_SovereigntyAccessors(t *testing.T) {
	s, err := New(WithReplicas(3), WithSovereigntyBoard("board-eu", "eu-west"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	if s.Region() != "eu-west" {
		t.Fatalf("Region()=%q, quero eu-west", s.Region())
	}
	if s.SovereigntyBoard() != "board-eu" {
		t.Fatalf("SovereigntyBoard()=%q, quero board-eu", s.SovereigntyBoard())
	}
}
