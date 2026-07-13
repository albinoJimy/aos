package adapters_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aos-ref/platform/memory/adapters"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/ports"
	"github.com/aos-ref/substrate/eventstore"
)

// TestEventStore_ReadRebuildsFromAppendOnlyLog prova explicitamente o critério de
// AOS-035 "a leitura reconstrói a partir do Event Store append-only": um adaptador
// NOVO, sem qualquer estado em RAM partilhado, materializa o estado só a partir
// dos eventos do log, e o log é escrito por eventos (não por mutação).
func TestEventStore_ReadRebuildsFromAppendOnlyLog(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	writer := adapters.NewEventStoreAdapter(store)
	rec := recFor(domain.ClassEpisodic, "e1", "agent-a", "run-1", domain.ProvenanceTrusted)
	if _, err := writer.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Actualiza o registo com um segundo run (chave de idempotência distinta):
	// last-write-wins na reconstrução.
	rec2 := recFor(domain.ClassEpisodic, "e1", "agent-a", "run-2", domain.ProvenanceTrusted)
	rec2.Body = domain.EpisodicBody{TraceID: "trace-e1", Goal: "g2", Outcome: "failed", StepCount: 9, Summary: "s2"}
	if _, err := writer.Put(ctx, rec2); err != nil {
		t.Fatalf("Put#2: %v", err)
	}

	// O stream do Event Store contém DOIS eventos append-only (nada foi mutado).
	events, err := store.Read(ctx, "memory.episodic", 1)
	if err != nil {
		t.Fatalf("store.Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("log devia ter 2 eventos append-only, tem %d", len(events))
	}
	for _, ev := range events {
		if ev.Type != adapters.EventTypeWritten {
			t.Fatalf("tipo de evento inesperado: %q", ev.Type)
		}
	}

	// Um leitor FRESCO reconstrói o estado corrente só do log.
	reader := adapters.NewEventStoreAdapter(store)
	got, err := reader.Get(ctx, domain.ClassEpisodic, "e1")
	if err != nil {
		t.Fatalf("Get reconstruido: %v", err)
	}
	eb := got.Body.(domain.EpisodicBody)
	if eb.Outcome != "failed" || eb.StepCount != 9 {
		t.Fatalf("reconstrucao nao aplicou last-write-wins: %+v", eb)
	}
	if got.Metadata.RunID != "run-2" {
		t.Fatalf("reconstrucao devolveu run errado: %q", got.Metadata.RunID)
	}
}

// TestEventStore_DeleteIsTombstoneNotMutation prova que o Delete escreve um
// evento (tombstone) e não muta/remove eventos do log — o log continua a crescer.
func TestEventStore_DeleteIsTombstoneNotMutation(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	a := adapters.NewEventStoreAdapter(store)
	if _, err := a.Put(ctx, recFor(domain.ClassSemantic, "s1", "agent-a", "run-1", domain.ProvenanceTrusted)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := a.Delete(ctx, domain.ClassSemantic, "s1", delCtx()); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	events, err := store.Read(ctx, "memory.semantic", 1)
	if err != nil {
		t.Fatalf("store.Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("log devia ter 2 eventos (write+tombstone), tem %d", len(events))
	}
	if events[0].Type != adapters.EventTypeWritten || events[1].Type != adapters.EventTypeDeleted {
		t.Fatalf("ordem/tipos inesperados: %q, %q", events[0].Type, events[1].Type)
	}
}

// TestEventStore_TombstoneCarriesAttribution prova que o evento de tombstone é
// AUDITÁVEL no próprio log: leva run_id no envelope, o Producer (agent → NHI) e a
// atribuição (agent_id/run_id/provenance) no payload. Sem isto, uma remoção seria
// não-rastreável no log append-only.
func TestEventStore_TombstoneCarriesAttribution(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	a := adapters.NewEventStoreAdapter(store)
	if _, err := a.Put(ctx, recFor(domain.ClassSemantic, "s1", "agent-a", "run-1", domain.ProvenanceTrusted)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	dc := ports.DeleteContext{AgentID: "agent-del", RunID: "run-del", Provenance: domain.ProvenanceUntrusted}
	if err := a.Delete(ctx, domain.ClassSemantic, "s1", dc); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	events, err := store.Read(ctx, "memory.semantic", 1)
	if err != nil {
		t.Fatalf("store.Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("log devia ter 2 eventos, tem %d", len(events))
	}
	tomb := events[1]
	if tomb.Type != adapters.EventTypeDeleted {
		t.Fatalf("evento 2 nao e tombstone: %q", tomb.Type)
	}
	if tomb.RunID != "run-del" {
		t.Fatalf("tombstone sem run_id no envelope: %q", tomb.RunID)
	}
	if tomb.Producer.NHIID != "agent-del" {
		t.Fatalf("tombstone sem Producer (agent): %+v", tomb.Producer)
	}
	var payload struct {
		ID         string `json:"id"`
		AgentID    string `json:"agent_id"`
		RunID      string `json:"run_id"`
		Provenance string `json:"provenance"`
	}
	if err := json.Unmarshal(tomb.Payload, &payload); err != nil {
		t.Fatalf("payload do tombstone indescodificavel: %v", err)
	}
	if payload.ID != "s1" || payload.AgentID != "agent-del" ||
		payload.RunID != "run-del" || payload.Provenance != string(domain.ProvenanceUntrusted) {
		t.Fatalf("payload do tombstone sem atribuicao completa: %+v", payload)
	}
}

// TestEventStore_DeleteAbsentIsNoTombstone prova que apagar um (class,id) que NÃO
// está vivo é no-op sem escrever tombstone (espelha o in-memory; evita ruído no
// log). Um stream inexistente e um id inexistente num stream vivo são ambos no-op.
func TestEventStore_DeleteAbsentIsNoTombstone(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	a := adapters.NewEventStoreAdapter(store)
	// Stream inexistente: no-op, sem erro, sem stream criado.
	if err := a.Delete(ctx, domain.ClassSemantic, "ghost", delCtx()); err != nil {
		t.Fatalf("Delete de stream inexistente: %v", err)
	}
	if _, err := store.Read(ctx, "memory.semantic", 1); !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Fatalf("delete de registo inexistente nao devia criar stream/tombstone, err=%v", err)
	}
	// Id inexistente num stream vivo: continua a ser no-op (só o put fica no log).
	if _, err := a.Put(ctx, recFor(domain.ClassSemantic, "live", "agent-a", "run-1", domain.ProvenanceTrusted)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := a.Delete(ctx, domain.ClassSemantic, "absent", delCtx()); err != nil {
		t.Fatalf("Delete de id inexistente: %v", err)
	}
	events, err := store.Read(ctx, "memory.semantic", 1)
	if err != nil {
		t.Fatalf("store.Read: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("delete de id inexistente nao devia escrever tombstone, eventos=%d", len(events))
	}
}

// TestEventStore_PayloadCarriesMandatoryMetadata prova que o evento persistido no
// log leva todos os metadados obrigatórios no payload (auditável no próprio log).
func TestEventStore_PayloadCarriesMandatoryMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	a := adapters.NewEventStoreAdapter(store)
	if _, err := a.Put(ctx, recFor(domain.ClassProcedural, "p1", "agent-a", "run-1", domain.ProvenanceUntrusted)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	events, err := store.Read(ctx, "memory.procedural", 1)
	if err != nil {
		t.Fatalf("store.Read: %v", err)
	}
	rec, err := domain.UnmarshalRecord(events[0].Payload)
	if err != nil {
		t.Fatalf("UnmarshalRecord do payload do log: %v", err)
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("payload no log nao tem metadados obrigatorios: %v", err)
	}
}

// Assegura que os dois adaptadores são intermutáveis pela mesma variável de porta
// (backend-swap por configuração, sem alterar chamadores).
func TestBackendSwapByConfiguration(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	backends := map[string]ports.MemoryPort{
		"eventstore": adapters.NewEventStoreAdapter(store),
		"inmemory":   adapters.NewInMemoryAdapter(),
	}
	for name, p := range backends {
		rec := recFor(domain.ClassWorking, "w", "agent-a", "run-1", domain.ProvenanceTrusted)
		if _, err := p.Put(ctx, rec); err != nil {
			t.Fatalf("%s Put: %v", name, err)
		}
		if _, err := p.Get(ctx, domain.ClassWorking, "w"); err != nil {
			t.Fatalf("%s Get: %v", name, err)
		}
	}
}
