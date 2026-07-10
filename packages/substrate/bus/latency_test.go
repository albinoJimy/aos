package bus

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// TestLatenciaEntregaPushP95 cobre o critério: latência de entrega push (do
// commit no Event Store até à invocação do Handler) com p95 < 250 ms.
func TestLatenciaEntregaPushP95(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	const n = 300
	var mu sync.Mutex
	lat := make([]time.Duration, 0, n)
	done := make(chan struct{})

	h := func(d *Delivery) {
		// Latência = agora - Event.Ts (instante de commit).
		if ts, err := time.Parse(time.RFC3339Nano, d.Event.Ts); err == nil {
			delta := time.Now().UTC().Sub(ts)
			if delta < 0 {
				delta = 0
			}
			mu.Lock()
			lat = append(lat, delta)
			ndone := len(lat)
			mu.Unlock()
			if ndone == n {
				close(done)
			}
		}
		d.Ack()
	}
	if _, err := b.Subscribe(ctx, SubConfig{
		Name: "lat", Filter: Filter{Streams: []string{"s1"}}, Handler: h,
	}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < n; i++ {
		appendEv(t, es, "s1", "t1", "p1")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		mu.Lock()
		got := len(lat)
		mu.Unlock()
		t.Fatalf("só %d/%d entregues dentro do prazo", got, n)
	}

	mu.Lock()
	samples := append([]time.Duration(nil), lat...)
	mu.Unlock()
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p95 := samples[int(float64(len(samples))*0.95)]
	if p95 >= 250*time.Millisecond {
		t.Fatalf("p95 de entrega = %v, esperado < 250ms", p95)
	}
	t.Logf("latência de entrega: p50=%v p95=%v max=%v", samples[len(samples)/2], p95, samples[len(samples)-1])

	// As métricas expostas do barramento também refletem a latência.
	if snap := b.Metrics(); snap.Delivered < n || snap.MaxLatency < 0 {
		t.Fatalf("métricas inconsistentes: %+v", snap)
	}
}

// BenchmarkPublishDeliver mede o custo do caminho publish→push→entrega.
func BenchmarkPublishDeliver(b *testing.B) {
	es, err := eventstore.New()
	if err != nil {
		b.Fatal(err)
	}
	defer es.Close()
	bus, err := New(es)
	if err != nil {
		b.Fatal(err)
	}
	defer bus.Close()

	delivered := make(chan struct{}, 1024)
	if _, err := bus.Subscribe(context.Background(), SubConfig{
		Name: "bench", Filter: Filter{Streams: []string{"s1"}},
		Handler: func(d *Delivery) { d.Ack(); delivered <- struct{}{} },
		Buffer:  b.N + 8,
	}); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := bus.Publish(context.Background(), "s1", eventstore.EventInput{
			Type: "t1", Producer: eventstore.Producer{NHIID: "p1"}, Payload: []byte(`{}`),
		}); err != nil {
			b.Fatal(err)
		}
		<-delivered
	}
}
