package bus

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestDeadLetterHandlerFalhaRepetida cobre o critério: um Handler que falha
// repetidamente (> Retry) manda o evento para a dead-letter e a subscrição
// prossegue (não fica presa).
func TestDeadLetterHandlerFalhaRepetida(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	boom := errors.New("processamento falhou")
	var attempts atomic.Int64
	poison := uint64(1)

	rec := newRecorder()
	h := func(d *Delivery) {
		if d.Event.Seq == poison {
			attempts.Add(1)
			d.Nack(boom)
			return
		}
		rec.record(d.Event)
		d.Ack()
	}
	sub, err := b.Subscribe(ctx, SubConfig{
		Name: "dl", Filter: Filter{Streams: []string{"s1"}},
		Handler: h, Retry: 2, // 1 tentativa + 2 retries = 3 invocações
	})
	if err != nil {
		t.Fatal(err)
	}

	appendEv(t, es, "s1", "t1", "p1") // seq 1 (venenoso)
	appendEv(t, es, "s1", "t1", "p1") // seq 2 (bom)

	// O evento bom (seq 2) deve ser entregue, provando que a subscrição não ficou
	// presa no venenoso.
	rec.waitN(t, 1, 2*time.Second)
	if got := rec.seqs(); !equalU64(got, []uint64{2}) {
		t.Fatalf("esperado entregar apenas seq 2, obtido %v", got)
	}

	// Dead-letter tem exactamente 1 entrada, com 3 tentativas e a causa correcta.
	deadline := time.Now().Add(time.Second)
	for b.DeadLetter().Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("evento venenoso não foi para dead-letter")
		}
		time.Sleep(5 * time.Millisecond)
	}
	ents := b.DeadLetter().Entries()
	if len(ents) != 1 {
		t.Fatalf("dead-letters = %d, esperado 1", len(ents))
	}
	dl := ents[0]
	if dl.Event.Seq != poison || dl.Attempts != 3 || dl.Reason != "handler" || !errors.Is(dl.Cause, boom) {
		t.Fatalf("entrada dead-letter inesperada: %+v", dl)
	}
	if a := attempts.Load(); a != 3 {
		t.Fatalf("Handler invocado %d vezes, esperado 3 (Retry=2)", a)
	}
	sub.Unsubscribe()
}

// TestNackComRetrySucessoAntesDeDeadLetter verifica que um Nack seguido de
// sucesso dentro do orçamento de retries NÃO vai para a dead-letter.
func TestNackComRetrySucessoAntesDeDeadLetter(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	var tries atomic.Int64
	done := make(chan struct{})
	h := func(d *Delivery) {
		if tries.Add(1) < 3 {
			d.Nack(errors.New("transitório"))
			return
		}
		d.Ack()
		close(done)
	}
	sub, err := b.Subscribe(ctx, SubConfig{
		Name: "retry-ok", Filter: Filter{Streams: []string{"s1"}},
		Handler: h, Retry: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	appendEv(t, es, "s1", "t1", "p1")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handler não teve sucesso dentro do orçamento de retries")
	}
	if b.DeadLetter().Len() != 0 {
		t.Fatalf("dead-letter não devia ter entradas: %d", b.DeadLetter().Len())
	}
	sub.Unsubscribe()
}
