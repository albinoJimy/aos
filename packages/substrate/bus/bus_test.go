package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

func TestNewRejectsNilStore(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrNilStore) {
		t.Fatalf("esperado ErrNilStore, obtido %v", err)
	}
}

func TestSubscribeValidation(t *testing.T) {
	b, _ := newTestBus(t, nil)
	rec := newRecorder()
	cases := []struct {
		name string
		cfg  SubConfig
	}{
		{"sem nome", SubConfig{Handler: ackAll(rec)}},
		{"sem handler", SubConfig{Name: "x"}},
		{"buffer negativo", SubConfig{Name: "x", Handler: ackAll(rec), Buffer: -1}},
		{"retry negativo", SubConfig{Name: "x", Handler: ackAll(rec), Retry: -1}},
		{"policy invalida", SubConfig{Name: "x", Handler: ackAll(rec), Overflow: OverflowPolicy(99)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := b.Subscribe(context.Background(), tc.cfg); !errors.Is(err, ErrConfig) {
				t.Fatalf("esperado ErrConfig, obtido %v", err)
			}
		})
	}
}

func TestPublishPassThrough(t *testing.T) {
	b, es := newTestBus(t, nil)
	res, err := b.Publish(context.Background(), "s1", eventstore.EventInput{
		Type: "t1", Producer: eventstore.Producer{NHIID: "p1"}, Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Seq != 1 || res.Status != eventstore.StatusCommitted {
		t.Fatalf("resultado inesperado: %+v", res)
	}
	// Confirma que o evento está no Event Store.
	evs, err := es.Read(context.Background(), "s1", 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("read: %v evs=%d", err, len(evs))
	}
}

// TestFanoutPorFiltro cobre o critério: múltiplos subscritores por
// type/stream/producer recebem exactamente os eventos que casam.
func TestFanoutPorFiltro(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	byType := newRecorder()
	byStream := newRecorder()
	byProducer := newRecorder()

	if _, err := b.Subscribe(ctx, SubConfig{
		Name: "por-type", Filter: Filter{Types: []string{"t1"}}, Handler: ackAll(byType),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Subscribe(ctx, SubConfig{
		Name: "por-stream", Filter: Filter{Streams: []string{"s2"}}, Handler: ackAll(byStream),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Subscribe(ctx, SubConfig{
		Name: "por-producer", Filter: Filter{Producers: []string{"p1"}}, Handler: ackAll(byProducer),
	}); err != nil {
		t.Fatal(err)
	}

	// Eventos: (stream, type, producer) -> seq por stream.
	appendEv(t, es, "s1", "t1", "p1") // s1#1
	appendEv(t, es, "s1", "t2", "p2") // s1#2
	appendEv(t, es, "s2", "t1", "p2") // s2#1
	appendEv(t, es, "s2", "t2", "p1") // s2#2

	// por-type (t1): s1#1 e s2#1 → 2 eventos.
	byType.waitN(t, 2, 2*time.Second)
	// por-stream (s2): s2#1 e s2#2 → 2 eventos.
	byStream.waitN(t, 2, 2*time.Second)
	// por-producer (p1): s1#1 e s2#2 → 2 eventos.
	byProducer.waitN(t, 2, 2*time.Second)

	assertTypes := func(rec *recorder, want map[string]bool) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		for _, ev := range rec.events {
			if !want[ev.Type+"|"+ev.StreamID+"|"+ev.Producer.NHIID] {
				t.Fatalf("evento inesperado: %s/%s/%s", ev.Type, ev.StreamID, ev.Producer.NHIID)
			}
		}
	}
	assertTypes(byType, map[string]bool{"t1|s1|p1": true, "t1|s2|p2": true})
	assertTypes(byStream, map[string]bool{"t1|s2|p2": true, "t2|s2|p1": true})
	assertTypes(byProducer, map[string]bool{"t1|s1|p1": true, "t2|s2|p1": true})

	// Garante que nenhum recebeu a mais (dá folga para entregas espúrias).
	time.Sleep(50 * time.Millisecond)
	if byType.count() != 2 || byStream.count() != 2 || byProducer.count() != 2 {
		t.Fatalf("contagens: type=%d stream=%d producer=%d", byType.count(), byStream.count(), byProducer.count())
	}
}

func TestCatchUpEntregaHistorico(t *testing.T) {
	b, es := newTestBus(t, nil)
	// História ANTES de subscrever.
	for i := 0; i < 5; i++ {
		appendEv(t, es, "s1", "t1", "p1")
	}
	rec := newRecorder()
	if _, err := b.Subscribe(context.Background(), SubConfig{
		Name: "catchup", Filter: Filter{Streams: []string{"s1"}}, Handler: ackAll(rec),
	}); err != nil {
		t.Fatal(err)
	}
	rec.waitN(t, 5, 2*time.Second)
	if got := rec.seqs(); !equalU64(got, []uint64{1, 2, 3, 4, 5}) {
		t.Fatalf("catch-up fora de ordem/incompleto: %v", got)
	}
}
