package bus

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// newTestBus constrói um Event Store de referência e um Bus por cima, com um
// CursorStore partilhável para os testes de retoma.
func newTestBus(t *testing.T, cs CursorStore, opts ...Option) (*Bus, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	all := opts
	if cs != nil {
		all = append(all, WithCursorStore(cs))
	}
	b, err := New(es, all...)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	t.Cleanup(func() {
		_ = b.Close()
		_ = es.Close()
	})
	return b, es
}

// appendEv escreve um evento no stream e devolve o seq atribuído. Sem
// idempotency key (run/step vazios) para nunca colapsar por dedup.
func appendEv(t *testing.T, es *eventstore.Store, stream, typ, producer string) uint64 {
	t.Helper()
	res, err := es.Append(context.Background(), stream, eventstore.EventInput{
		Type:     typ,
		Producer: eventstore.Producer{NHIID: producer},
		Payload:  []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("append(%s,%s,%s): %v", stream, typ, producer, err)
	}
	return res.Seq
}

// recorder acumula os eventos entregues (por qualquer razão) e sinaliza cada
// entrega num canal com folga.
type recorder struct {
	mu     sync.Mutex
	events []eventstore.Event
	ch     chan eventstore.Event
}

func newRecorder() *recorder {
	return &recorder{ch: make(chan eventstore.Event, 4096)}
}

func (r *recorder) record(ev eventstore.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	r.ch <- ev
}

// waitN espera por n sinais de entrega (falha no timeout).
func (r *recorder) waitN(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for i := 0; i < n; i++ {
		select {
		case <-r.ch:
		case <-deadline:
			t.Fatalf("timeout: esperados %d eventos, recebidos %d", n, i)
		}
	}
}

func (r *recorder) seqs() []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]uint64, len(r.events))
	for i, ev := range r.events {
		out[i] = ev.Seq
	}
	return out
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// ackAll é um Handler que regista e confirma tudo.
func ackAll(rec *recorder) Handler {
	return func(d *Delivery) {
		rec.record(d.Event)
		d.Ack()
	}
}

func sortedU64(xs []uint64) []uint64 {
	out := append([]uint64(nil), xs...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func equalU64(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
