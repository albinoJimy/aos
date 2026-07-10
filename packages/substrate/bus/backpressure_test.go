package bus

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestBackpressureBlockNaoBloqueiaProdutorNemOutros cobre o critério: um
// consumidor lento (política Block, buffer pequeno) não bloqueia o produtor nem
// os outros subscritores.
func TestBackpressureBlockNaoBloqueiaProdutorNemOutros(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	const n = 100
	gate := make(chan struct{}) // fecha para libertar o consumidor lento

	// Consumidor lento: bloqueia no gate antes de confirmar.
	var slowDelivered atomic.Int64
	slow := func(d *Delivery) {
		<-gate
		slowDelivered.Add(1)
		d.Ack()
	}
	subSlow, err := b.Subscribe(ctx, SubConfig{
		Name: "slow", Filter: Filter{Streams: []string{"s1"}},
		Handler: slow, Buffer: 2, Overflow: Block,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Consumidor rápido.
	fast := newRecorder()
	subFast, err := b.Subscribe(ctx, SubConfig{
		Name: "fast", Filter: Filter{Streams: []string{"s1"}},
		Handler: ackAll(fast), Buffer: 1024, Overflow: Block,
	})
	if err != nil {
		t.Fatal(err)
	}

	// O produtor deve conseguir escrever os n eventos rapidamente, apesar do
	// consumidor lento estar preso.
	start := time.Now()
	for i := 0; i < n; i++ {
		appendEv(t, es, "s1", "t1", "p1")
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("produtor bloqueado pelo consumidor lento: %v para %d appends", elapsed, n)
	}

	// O consumidor rápido recebe tudo, sem depender do lento.
	fast.waitN(t, n, 3*time.Second)

	// O lento praticamente não avançou (preso no gate); no máximo terá começado a
	// 1.ª entrega.
	if got := slowDelivered.Load(); got > 1 {
		t.Fatalf("consumidor lento não estava bloqueado: entregou %d", got)
	}

	// Liberta o lento e confirma que acaba por receber tudo (nada se perde com Block).
	close(gate)
	deadline := time.Now().Add(3 * time.Second)
	for slowDelivered.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("consumidor lento não drenou: %d/%d", slowDelivered.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	subSlow.Unsubscribe()
	subFast.Unsubscribe()
}

// TestBackpressureDropOldest verifica a política DropOldest: sob overload, o
// buffer descarta os mais antigos (sheds load) e o produtor não bloqueia.
func TestBackpressureDropOldest(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	const n = 200
	gate := make(chan struct{})
	var delivered atomic.Int64
	h := func(d *Delivery) {
		<-gate
		delivered.Add(1)
		d.Ack()
	}
	sub, err := b.Subscribe(ctx, SubConfig{
		Name: "drop", Filter: Filter{Streams: []string{"s1"}},
		Handler: h, Buffer: 4, Overflow: DropOldest,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	for i := 0; i < n; i++ {
		appendEv(t, es, "s1", "t1", "p1")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("produtor bloqueado sob DropOldest: %v", el)
	}

	// Dá tempo ao pipeline para encher e descartar.
	deadline := time.Now().Add(2 * time.Second)
	for b.Metrics().Dropped == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("DropOldest não descartou nada (buffer=4, n=%d)", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	// Deve ter entregue estritamente menos do que n (houve descarte).
	time.Sleep(100 * time.Millisecond)
	if got := delivered.Load(); got >= int64(n) {
		t.Fatalf("DropOldest não reduziu entregas: %d de %d", got, n)
	}
	sub.Unsubscribe()
}

// TestBackpressureDeadLetterOverflow verifica a política DeadLetter em overflow:
// os eventos em excesso vão para a dead-letter, inspecionáveis, sem bloquear.
func TestBackpressureDeadLetterOverflow(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	const n = 200
	gate := make(chan struct{})
	h := func(d *Delivery) {
		<-gate
		d.Ack()
	}
	sub, err := b.Subscribe(ctx, SubConfig{
		Name: "dlq-of", Filter: Filter{Streams: []string{"s1"}},
		Handler: h, Buffer: 4, Overflow: DeadLetter,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		appendEv(t, es, "s1", "t1", "p1")
	}
	deadline := time.Now().Add(2 * time.Second)
	for b.DeadLetter().Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("overflow não produziu dead-letters")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, dl := range b.DeadLetter().Entries() {
		if dl.Reason != "overflow" {
			t.Fatalf("razão inesperada: %q", dl.Reason)
		}
	}
	close(gate)
	sub.Unsubscribe()
}
