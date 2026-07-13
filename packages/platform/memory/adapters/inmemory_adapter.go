package adapters

import (
	"context"
	"sort"
	"sync"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/ports"
)

// InMemoryAdapter é o backend de TESTE da MemoryPort: um armazém em RAM. Não é
// fonte de verdade (não sobrevive a crash) — existe para provar, via o contract
// test partilhado, que a MemoryPort é substituível por configuração sem alterar
// chamadores. A sua semântica observável espelha EXACTAMENTE a do adaptador de
// Event Store: idempotência por f(RunID, Class, ID), last-write-wins entre chaves
// de idempotência distintas, e tombstone lógico no Delete.
type InMemoryAdapter struct {
	tracer agentruntime.Tracer

	mu sync.Mutex
	// idemp mapeia a chave de idempotência → registo persistido na primeira
	// escrita (nunca limpa), espelhando o dedup do Event Store: o duplicado ganha.
	idemp map[string]domain.Record
	// live é o estado corrente por classe: class → id → entrada viva.
	live map[domain.MemoryClass]map[string]*memEntry
	// seq é um contador monotónico que dá ordem estável de escrita às consultas.
	seq uint64
}

type memEntry struct {
	rec   domain.Record
	order uint64
}

// InMemoryOption configura o adaptador in-memory.
type InMemoryOption func(*InMemoryAdapter)

// WithInMemoryTracer injecta a porta Tracer (default NoopTracer).
func WithInMemoryTracer(t agentruntime.Tracer) InMemoryOption {
	return func(a *InMemoryAdapter) { a.tracer = t }
}

// NewInMemoryAdapter constrói o adaptador in-memory vazio.
func NewInMemoryAdapter(opts ...InMemoryOption) *InMemoryAdapter {
	a := &InMemoryAdapter{
		tracer: agentruntime.NoopTracer{},
		idemp:  make(map[string]domain.Record),
		live:   make(map[domain.MemoryClass]map[string]*memEntry),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Version implementa ports.MemoryPort.
func (a *InMemoryAdapter) Version() string { return portVersion }

// Put implementa ports.MemoryPort.
func (a *InMemoryAdapter) Put(ctx context.Context, rec domain.Record) (domain.Record, error) {
	_, span := startSpan(ctx, a.tracer, opPut, rec.Class, rec.ID)
	defer span.End()

	if err := rec.Validate(); err != nil {
		span.SetAttribute(attrResult, "rejected")
		return domain.Record{}, err
	}
	span.SetAttribute(attrProvenance, string(rec.Metadata.Provenance))

	key := idempotencyKey(rec.Metadata.RunID, rec.Class, rec.ID)

	a.mu.Lock()
	defer a.mu.Unlock()

	if existing, ok := a.idemp[key]; ok {
		span.SetAttribute(attrResult, "duplicate")
		return existing.Clone(), nil
	}

	stored := rec.Clone()
	a.idemp[key] = stored.Clone()

	byID := a.live[rec.Class]
	if byID == nil {
		byID = make(map[string]*memEntry)
		a.live[rec.Class] = byID
	}
	a.seq++
	byID[rec.ID] = &memEntry{rec: stored, order: a.seq}

	span.SetAttribute(attrResult, "committed")
	return stored.Clone(), nil
}

// Get implementa ports.MemoryPort.
func (a *InMemoryAdapter) Get(ctx context.Context, class domain.MemoryClass, id string) (domain.Record, error) {
	_, span := startSpan(ctx, a.tracer, opGet, class, id)
	defer span.End()

	a.mu.Lock()
	defer a.mu.Unlock()

	if byID, ok := a.live[class]; ok {
		if e, ok := byID[id]; ok {
			span.SetAttribute(attrResult, "hit")
			return e.rec.Clone(), nil
		}
	}
	span.SetAttribute(attrResult, "miss")
	return domain.Record{}, domain.ErrNotFound
}

// Query implementa ports.MemoryPort.
func (a *InMemoryAdapter) Query(ctx context.Context, q ports.Query) ([]domain.Record, error) {
	_, span := startSpan(ctx, a.tracer, opQuery, q.Class, "")
	defer span.End()

	a.mu.Lock()
	defer a.mu.Unlock()

	var entries []*memEntry
	for _, e := range a.live[q.Class] {
		if !matchesQuery(e.rec, q) {
			continue
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].order < entries[j].order })

	out := make([]domain.Record, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.rec.Clone())
	}
	span.SetAttribute(attrCount, len(out))
	return out, nil
}

// Delete implementa ports.MemoryPort. Tombstone lógico (idempotente). Valida o
// DeleteContext fail-closed, espelhando EXACTAMENTE o adaptador de Event Store: a
// atribuição obrigatória de uma remoção é a mesma em ambos os backends.
func (a *InMemoryAdapter) Delete(ctx context.Context, class domain.MemoryClass, id string, dc ports.DeleteContext) error {
	_, span := startSpan(ctx, a.tracer, opDelete, class, id)
	defer span.End()

	if err := dc.Validate(); err != nil {
		span.SetAttribute(attrResult, "rejected")
		return err
	}
	span.SetAttribute(attrProvenance, string(dc.Provenance))

	a.mu.Lock()
	defer a.mu.Unlock()

	if byID, ok := a.live[class]; ok {
		delete(byID, id)
	}
	span.SetAttribute(attrResult, "deleted")
	return nil
}

// Assegura em tempo de compilação que o adaptador satisfaz a porta.
var _ ports.MemoryPort = (*InMemoryAdapter)(nil)
