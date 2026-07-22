package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// erroringToolSetStore falha em cada operação — para provar o fail-closed de [Freeze].
type erroringToolSetStore struct{}

func (erroringToolSetStore) Append(context.Context, string, eventstore.EventInput, ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return eventstore.AppendResult{}, errors.New("store indisponível")
}
func (erroringToolSetStore) Read(context.Context, string, uint64) ([]eventstore.Event, error) {
	return nil, errors.New("store indisponível")
}

// TestRunToolSets_FreezeFailClosedOnPersistError: se a persistência do snapshot falha,
// [Freeze] aborta com erro e NÃO regista em memória — o run não seria crash-safe, pelo
// que não deve prosseguir (fail-closed, DoD de AOS-155).
func TestRunToolSets_FreezeFailClosedOnPersistError(t *testing.T) {
	ctx := context.Background()
	entry := signedEntry(t, testSigner(t), "echo", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	frozen, err := toolset.FreezeToolSet(ctx, &fakeCatalog{entries: []domain.Entry{entry}}, "run-x", nil, toolset.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}
	rts := NewRunToolSets(WithToolSetStore(erroringToolSetStore{}))
	if err := rts.Freeze(ctx, frozen); err == nil {
		t.Fatal("Freeze devia falhar fail-closed quando a persistência falha")
	}
	if _, ok := rts.Frozen("run-x"); ok {
		t.Fatal("frozen não devia ficar em memória após falha de persistência (fail-closed)")
	}
}

// TestRunToolSets_DurableRebuildAfterFailover é o AC central de AOS-155: um tool set
// congelado persistido no arranque é RECONSTRUÍDO após um failover, para a revalidação
// por chamada NÃO colapsar para default-deny (que negaria TODA a tool call do run em
// curso). Prova o ciclo persist(Freeze) → restart → Rebuild → Frozen idêntico.
func TestRunToolSets_DurableRebuildAfterFailover(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()

	entry := signedEntry(t, testSigner(t), "echo", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	catalog := &fakeCatalog{entries: []domain.Entry{entry}}

	const runID = "run-failover"
	frozen, err := toolset.FreezeToolSet(ctx, catalog, runID, nil, toolset.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}

	// (1) Registo DURÁVEL: persiste o snapshot no arranque.
	rts := NewRunToolSets(WithToolSetStore(store))
	if err := rts.Freeze(ctx, frozen); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if _, ok := rts.Frozen(runID); !ok {
		t.Fatal("frozen ausente após Freeze")
	}

	// (2) FAILOVER: um novo registo (mapa in-memory vazio) sobre o MESMO Event Store —
	// como um processo que reinicia. Antes do Rebuild, a revalidação negaria tudo.
	resumed := NewRunToolSets(WithToolSetStore(store))
	if _, ok := resumed.Frozen(runID); ok {
		t.Fatal("registo pós-failover devia estar vazio antes do Rebuild (default-deny)")
	}

	// (3) Rebuild reconstrói o snapshot do Event Store.
	ok, err := resumed.Rebuild(ctx, runID)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if !ok {
		t.Fatal("Rebuild devia ter reconstruído o snapshot persistido")
	}

	// (4) Após Rebuild: a revalidação obtém o MESMO snapshot — NÃO default-deny. O hash
	// idêntico prova a reconstrução byte-a-byte (mesmas expectativas por tool).
	got, ok := resumed.Frozen(runID)
	if !ok {
		t.Fatal("frozen ausente após Rebuild (a revalidação colapsaria para default-deny)")
	}
	if got.Hash() != frozen.Hash() {
		t.Fatalf("snapshot reconstruído difere: hash %q != %q", got.Hash(), frozen.Hash())
	}
	if got.RunID() != runID || got.Len() != frozen.Len() {
		t.Fatalf("snapshot reconstruído inconsistente: runID=%q len=%d (quero %q/%d)", got.RunID(), got.Len(), runID, frozen.Len())
	}
	if de, dok := got.ExpectedDigest("echo"); !dok || de != entry.Digest {
		t.Fatalf("expectativa da tool 'echo' não reconstruída: digest=%q ok=%v (quero %q)", de, dok, entry.Digest)
	}
}

// TestRunToolSets_RebuildNoSnapshot cobre os caminhos sem snapshot: run sem registo
// durável e registo in-memory (sem store) devolvem (false, nil) — não fabricam um
// snapshot vazio (que seria pior que default-deny).
func TestRunToolSets_RebuildNoSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()

	// Run sem snapshot persistido no store durável.
	durable := NewRunToolSets(WithToolSetStore(store))
	if ok, err := durable.Rebuild(ctx, "run-inexistente"); ok || err != nil {
		t.Fatalf("Rebuild(inexistente)=(%v,%v), quero (false,nil)", ok, err)
	}

	// Registo in-memory (sem store) não é crash-safe: Rebuild é sempre (false, nil).
	mem := NewRunToolSets()
	if ok, err := mem.Rebuild(ctx, "run-x"); ok || err != nil {
		t.Fatalf("Rebuild sem store=(%v,%v), quero (false,nil)", ok, err)
	}
}
