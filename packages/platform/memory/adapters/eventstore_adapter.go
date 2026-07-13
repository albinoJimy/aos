package adapters

import (
	"context"
	"errors"
	"sort"
	"strconv"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/ports"
	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento de memória gravados no Event Store.
const (
	// EventTypeWritten — um registo de memória foi escrito.
	EventTypeWritten = "memory.record.written"
	// EventTypeDeleted — um registo de memória foi marcado como apagado
	// (tombstone). O log continua append-only: apagar é um NOVO evento.
	EventTypeDeleted = "memory.record.deleted"
)

// streamPrefix prefixa o stream por classe: "memory.<class>". Um stream por
// classe dá um log ordenado e auditável por classe e materializa a distinção
// entre as quatro classes já ao nível do particionamento do log.
const streamPrefix = "memory."

// appender é o subconjunto do Event Store de que este adaptador depende para
// escrita. Mantê-lo mínimo desacopla o adaptador da superfície completa do store.
type appender interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// EventStoreAdapter é o backend FONTE DE VERDADE da MemoryPort (ADR-007). Escreve
// cada operação como evento append-only e RECONSTRÓI toda a leitura por replay do
// log — não mantém estado autoritativo em RAM. Não depende de single-writer: a
// durabilidade e a ordenação são do Event Store replicado.
//
// O adaptador é sem-estado (além da referência ao store e ao tracer), pelo que é
// seguro para uso concorrente na medida em que o Event Store o é.
type EventStoreAdapter struct {
	store  appender
	tracer agentruntime.Tracer
}

// EventStoreOption configura o adaptador de Event Store.
type EventStoreOption func(*EventStoreAdapter)

// WithEventStoreTracer injecta a porta Tracer (default NoopTracer).
func WithEventStoreTracer(t agentruntime.Tracer) EventStoreOption {
	return func(a *EventStoreAdapter) { a.tracer = t }
}

// NewEventStoreAdapter constrói o adaptador sobre um Event Store. Um store nil é
// erro de programação; o construtor devolve um adaptador cujas operações falham
// fail-closed na primeira chamada (Read/Append em nil), o que os testes cobrem.
func NewEventStoreAdapter(store appender, opts ...EventStoreOption) *EventStoreAdapter {
	a := &EventStoreAdapter{store: store, tracer: agentruntime.NoopTracer{}}
	for _, o := range opts {
		o(a)
	}
	return a
}

// streamFor devolve o stream do Event Store para uma classe.
func streamFor(class domain.MemoryClass) string { return streamPrefix + string(class) }

// Version implementa ports.MemoryPort.
func (a *EventStoreAdapter) Version() string { return portVersion }

// Put implementa ports.MemoryPort: valida (fail-closed) e escreve um evento
// append-only. A idempotência é f(RunID, Class, ID) via a idempotency_key do
// Event Store: um retry após crash não duplica o registo — o duplicado devolve o
// registo original committed.
func (a *EventStoreAdapter) Put(ctx context.Context, rec domain.Record) (domain.Record, error) {
	_, span := startSpan(ctx, a.tracer, opPut, rec.Class, rec.ID)
	defer span.End()

	if err := rec.Validate(); err != nil {
		span.SetAttribute(attrResult, "rejected")
		return domain.Record{}, err
	}
	span.SetAttribute(attrProvenance, string(rec.Metadata.Provenance))

	payload, err := domain.MarshalRecord(rec)
	if err != nil {
		span.SetAttribute(attrResult, "rejected")
		return domain.Record{}, err
	}

	res, err := a.store.Append(ctx, streamFor(rec.Class), eventstore.EventInput{
		Type:    EventTypeWritten,
		Payload: payload,
		RunID:   rec.Metadata.RunID,
		StepID:  string(rec.Class) + ":put:" + rec.ID,
	})
	if err != nil {
		span.SetAttribute(attrResult, "error")
		return domain.Record{}, err
	}

	if res.Status == eventstore.StatusDuplicate {
		// O duplicado ganha: devolve o registo do evento original committed,
		// reconstruído do log (não o que o chamador acabou de passar).
		span.SetAttribute(attrResult, "duplicate")
		original, derr := domain.UnmarshalRecord(res.Event.Payload)
		if derr != nil {
			return domain.Record{}, derr
		}
		return original, nil
	}

	span.SetAttribute(attrResult, "committed")
	return rec.Clone(), nil
}

// Delete implementa ports.MemoryPort: escreve um tombstone append-only. O log
// nunca é mutado. O DeleteContext é validado fail-closed e ATRIBUI a remoção
// (run_id no envelope + Producer + payload) para a tornar auditável no log.
//
// Reconstrói o estado da classe ANTES de escrever, o que garante duas coisas:
//   - se (class,id) NÃO está vivo, é no-op sem tombstone (espelha o in-memory e
//     evita eventos espúrios no log para deletes de registos inexistentes);
//   - se está vivo, o StepID do tombstone é DISCRIMINADO pela seq do evento
//     "written" que o fixou. Assim cada "encarnação" de um dado (class,id) tem uma
//     idempotency_key de delete DISTINTA — a sequência put→delete→recriar→delete
//     já não é engolida pela dedup permanente/global do Event Store (o 2.º delete
//     de uma recriação legítima acrescenta um tombstone novo em vez de deduplicar
//     contra o 1.º), fechando o buraco de erasure/GDPR.
func (a *EventStoreAdapter) Delete(ctx context.Context, class domain.MemoryClass, id string, dc ports.DeleteContext) error {
	_, span := startSpan(ctx, a.tracer, opDelete, class, id)
	defer span.End()

	if err := dc.Validate(); err != nil {
		span.SetAttribute(attrResult, "rejected")
		return err
	}
	span.SetAttribute(attrProvenance, string(dc.Provenance))

	state, err := a.rebuild(ctx, class)
	if err != nil {
		span.SetAttribute(attrResult, "error")
		return err
	}
	entry, ok := state[id]
	if !ok {
		// Nada vivo com este id: no-op idempotente, sem poluir o log.
		span.SetAttribute(attrResult, "noop")
		return nil
	}

	payload, err := marshalTombstone(id, dc.AgentID, dc.RunID, dc.Provenance)
	if err != nil {
		span.SetAttribute(attrResult, "error")
		return err
	}

	_, err = a.store.Append(ctx, streamFor(class), eventstore.EventInput{
		Type:     EventTypeDeleted,
		Payload:  payload,
		RunID:    dc.RunID,
		StepID:   string(class) + ":del:" + id + ":" + strconv.FormatUint(entry.seq, 10),
		Producer: eventstore.Producer{NHIID: dc.AgentID},
	})
	if err != nil {
		span.SetAttribute(attrResult, "error")
		return err
	}
	span.SetAttribute(attrResult, "deleted")
	return nil
}

// Get implementa ports.MemoryPort reconstruindo o estado da classe a partir do
// log e devolvendo o registo (class,id) se vivo.
func (a *EventStoreAdapter) Get(ctx context.Context, class domain.MemoryClass, id string) (domain.Record, error) {
	_, span := startSpan(ctx, a.tracer, opGet, class, id)
	defer span.End()

	state, err := a.rebuild(ctx, class)
	if err != nil {
		span.SetAttribute(attrResult, "error")
		return domain.Record{}, err
	}
	if e, ok := state[id]; ok {
		span.SetAttribute(attrResult, "hit")
		return e.rec.Clone(), nil
	}
	span.SetAttribute(attrResult, "miss")
	return domain.Record{}, domain.ErrNotFound
}

// Query implementa ports.MemoryPort reconstruindo a classe do log e aplicando os
// filtros, por ordem estável de escrita (seq do último evento que fixou o valor).
func (a *EventStoreAdapter) Query(ctx context.Context, q ports.Query) ([]domain.Record, error) {
	_, span := startSpan(ctx, a.tracer, opQuery, q.Class, "")
	defer span.End()

	state, err := a.rebuild(ctx, q.Class)
	if err != nil {
		span.SetAttribute(attrResult, "error")
		return nil, err
	}

	entries := make([]rebuiltEntry, 0, len(state))
	for _, e := range state {
		if !matchesQuery(e.rec, q) {
			continue
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })

	out := make([]domain.Record, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.rec.Clone())
	}
	span.SetAttribute(attrCount, len(out))
	return out, nil
}

// rebuiltEntry é um registo vivo reconstruído do log, com a seq do evento que
// fixou o seu valor corrente (para ordenação estável).
type rebuiltEntry struct {
	rec domain.Record
	seq uint64
}

// rebuild reconstrói o estado corrente de uma classe por replay do log
// append-only: os eventos "written" fixam/actualizam o registo (last-write-wins
// por seq) e os "deleted" removem-no (tombstone). É esta função que materializa
// "a leitura reconstrói a partir do Event Store" — não há estado em RAM.
func (a *EventStoreAdapter) rebuild(ctx context.Context, class domain.MemoryClass) (map[string]rebuiltEntry, error) {
	events, err := a.store.Read(ctx, streamFor(class), 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			// Classe ainda sem eventos: estado vazio, não é erro.
			return map[string]rebuiltEntry{}, nil
		}
		return nil, err
	}

	state := make(map[string]rebuiltEntry, len(events))
	for _, ev := range events {
		switch ev.Type {
		case EventTypeWritten:
			rec, derr := domain.UnmarshalRecord(ev.Payload)
			if derr != nil {
				return nil, derr
			}
			state[rec.ID] = rebuiltEntry{rec: rec, seq: ev.Seq}
		case EventTypeDeleted:
			id, derr := decodeDeletedID(ev.Payload)
			if derr != nil {
				return nil, derr
			}
			delete(state, id)
		default:
			// Tipo desconhecido no stream de memória: ignora (forward-compat).
		}
	}
	return state, nil
}

// Assegura em tempo de compilação que o adaptador satisfaz a porta.
var _ ports.MemoryPort = (*EventStoreAdapter)(nil)
