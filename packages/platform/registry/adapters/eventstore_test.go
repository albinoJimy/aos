package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

func TestJournal_NilStoreFailsClosed(t *testing.T) {
	t.Parallel()
	j := NewJournal(nil, "registry")
	if j.Configured() {
		t.Fatal("Journal sem store nao devia estar Configured")
	}
	if _, err := j.Append(context.Background(), "t", json.RawMessage(`{}`), "s", "pub", 0); !errors.Is(err, eventstore.ErrClosed) {
		t.Fatalf("Append sem store = %v, quer ErrClosed", err)
	}
	if _, _, err := j.ReadAll(context.Background()); !errors.Is(err, eventstore.ErrClosed) {
		t.Fatalf("ReadAll sem store = %v, quer ErrClosed", err)
	}
}

func TestJournal_AppendAndReadAll(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	j := NewJournal(store, "registry")

	// Stream vazio -> nao e erro (catalogo vazio), last = 0.
	evs, last, err := j.ReadAll(ctx)
	if err != nil {
		t.Fatalf("ReadAll vazio: %v", err)
	}
	if len(evs) != 0 || last != 0 {
		t.Fatalf("stream vazio devia dar 0 eventos/last=0, got %d/%d", len(evs), last)
	}

	// Append no fim (expectedSeq=0 = "nada committed ainda").
	res, err := j.Append(ctx, "registry.artifact.published", json.RawMessage(`{"id":"x"}`), "published:x@1.0.0", "pub:acme", 0)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if res.Seq != 1 {
		t.Fatalf("primeiro seq = %d, quer 1", res.Seq)
	}

	evs, last, err = j.ReadAll(ctx)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(evs) != 1 || last != 1 {
		t.Fatalf("apos 1 append: %d eventos, last=%d", len(evs), last)
	}
	if evs[0].Producer.NHIID != "pub:acme" {
		t.Fatalf("produtor devia ser o publicador, got %q", evs[0].Producer.NHIID)
	}

	// expectedSeq desactualizado -> conflito de concorrencia (propagado).
	if _, err := j.Append(ctx, "registry.artifact.published", json.RawMessage(`{"id":"y"}`), "published:y@1.0.0", "pub:acme", 0); !errors.Is(err, eventstore.ErrAppendOnlyViolation) {
		t.Fatalf("append com expectedSeq stale = %v, quer ErrAppendOnlyViolation", err)
	}
}
