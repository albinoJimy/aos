package pricing

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
)

func TestDiffDetectsAddedRemovedUpdated(t *testing.T) {
	old, _ := NewTable("v1", []Entry{
		{Model: "m-a", Region: "eu", Rate: Rate{InputPerMTokMicroUSD: 1000}},
		{Model: "m-b", Region: "eu", Rate: Rate{InputPerMTokMicroUSD: 2000}},
	})
	newT, _ := NewTable("v2", []Entry{
		{Model: "m-a", Region: "eu", Rate: Rate{InputPerMTokMicroUSD: 1500}}, // updated
		{Model: "m-c", Region: "eu", Rate: Rate{InputPerMTokMicroUSD: 3000}}, // added
		// m-b removed
	})
	ev := Diff(old, newT)
	if ev.Empty() {
		t.Fatalf("esperava alteracoes")
	}
	byKey := map[Key]ChangeKind{}
	for _, c := range ev.Changes {
		byKey[c.Key] = c.Kind
	}
	if byKey[Key{"m-a", "eu"}] != ChangeUpdated {
		t.Fatalf("m-a devia ser updated: %v", byKey)
	}
	if byKey[Key{"m-b", "eu"}] != ChangeRemoved {
		t.Fatalf("m-b devia ser removed: %v", byKey)
	}
	if byKey[Key{"m-c", "eu"}] != ChangeAdded {
		t.Fatalf("m-c devia ser added: %v", byKey)
	}
	if ev.FromVersion != old.Version() || ev.ToVersion != newT.Version() {
		t.Fatalf("versoes do evento erradas: %s -> %s", ev.FromVersion, ev.ToVersion)
	}
}

func TestDiffInitialActivationAllAdded(t *testing.T) {
	newT, _ := NewTable("v1", []Entry{{Model: "m", Region: "r", Rate: Rate{InputPerMTokMicroUSD: 1}}})
	ev := Diff(nil, newT)
	if len(ev.Changes) != 1 || ev.Changes[0].Kind != ChangeAdded {
		t.Fatalf("activacao inicial devia ser tudo added: %+v", ev.Changes)
	}
	if ev.FromVersion != "" {
		t.Fatalf("from-version devia ser vazio na activacao inicial")
	}
}

func TestDiffNoChangeEmpty(t *testing.T) {
	e := []Entry{{Model: "m", Region: "r", Rate: Rate{InputPerMTokMicroUSD: 1}}}
	a, _ := NewTable("v1", e)
	b, _ := NewTable("v1", e)
	if !Diff(a, b).Empty() {
		t.Fatalf("tabelas iguais nao deviam produzir alteracoes")
	}
}

// TestActivateSealsExplicitEvent prova que uma ALTERAÇÃO de preço é um EVENTO
// EXPLÍCITO: é emitida para o sink E selada no changelog WORM (nunca silenciosa).
func TestActivateSealsExplicitEvent(t *testing.T) {
	store := audit.NewMemStore()
	var emitted []ChangeEvent
	sink := ChangeSinkFunc(func(_ context.Context, ev ChangeEvent) { emitted = append(emitted, ev) })
	rec := NewChangeRecorder(store, WithChangeSink(sink))

	old, _ := NewTable("2026.06", []Entry{{Model: "m", Region: "eu", Rate: Rate{InputPerMTokMicroUSD: 1000}}})
	newT, _ := NewTable("2026.07", []Entry{{Model: "m", Region: "eu", Rate: Rate{InputPerMTokMicroUSD: 1200}}})

	ev, err := rec.Activate(context.Background(), old, newT, time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if ev.Empty() {
		t.Fatalf("esperava evento nao-vazio")
	}
	if len(emitted) != 1 {
		t.Fatalf("esperava 1 evento emitido, obtive %d", len(emitted))
	}
	// Selado no changelog WORM.
	head, err := store.Head(context.Background(), changelogPartition)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != 1 {
		t.Fatalf("esperava 1 registo no changelog WORM, obtive %d", head)
	}
	arr, err := store.Read(context.Background(), changelogPartition, 1, 1)
	if err != nil || len(arr) != 1 {
		t.Fatalf("Read changelog: %v (%d)", err, len(arr))
	}
	if arr[0].PolicyVersion != newT.Version() {
		t.Fatalf("changelog devia selar a versao nova: %s", arr[0].PolicyVersion)
	}
	if arr[0].Capability != capabilityActivate {
		t.Fatalf("capability errada: %s", arr[0].Capability)
	}
}

// TestActivateEmptyEventNotSealed prova que reactivar a MESMA tabela não polui o
// changelog (só uma alteração REAL é evento).
func TestActivateEmptyEventNotSealed(t *testing.T) {
	store := audit.NewMemStore()
	rec := NewChangeRecorder(store)
	e := []Entry{{Model: "m", Region: "eu", Rate: Rate{InputPerMTokMicroUSD: 1000}}}
	a, _ := NewTable("v1", e)
	b, _ := NewTable("v1", e)
	ev, err := rec.Activate(context.Background(), a, b, time.Unix(1, 0))
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !ev.Empty() {
		t.Fatalf("evento devia ser vazio")
	}
	head, _ := store.Head(context.Background(), changelogPartition)
	if head != 0 {
		t.Fatalf("changelog nao devia ter registos para um evento vazio, obtive %d", head)
	}
}

func TestActivateEmptyVersionFailClosed(t *testing.T) {
	rec := NewChangeRecorder(nil)
	if _, err := rec.Activate(context.Background(), nil, nil, time.Now()); err == nil {
		t.Fatalf("esperava fail-closed com tabela destino nil")
	}
}
