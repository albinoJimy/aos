package bus

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestRetomaPorCursorZeroSkips cobre o critério central: consumir+ACK alguns,
// "reiniciar" a subscrição com o MESMO cursor durável, e retomar do último ACK
// sem saltar nenhum evento — e re-entregar (at-least-once) o não-confirmado.
func TestRetomaPorCursorZeroSkips(t *testing.T) {
	cs := NewMemoryCursorStore()
	b, es := newTestBus(t, cs)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		appendEv(t, es, "s1", "t1", "p1") // seq 1..5
	}

	// Run 1: confirma apenas os 3 primeiros; 4 e 5 são entregues mas NÃO
	// confirmados (simula queda antes do ACK).
	run1 := newRecorder()
	h1 := func(d *Delivery) {
		run1.record(d.Event)
		if d.Event.Seq <= 3 {
			d.Ack()
		}
		// seq 4,5: nem Ack nem Nack → não confirmado.
	}
	sub1, err := b.Subscribe(ctx, SubConfig{
		Name: "worker", Filter: Filter{Streams: []string{"s1"}}, Handler: h1,
	})
	if err != nil {
		t.Fatal(err)
	}
	run1.waitN(t, 5, 2*time.Second) // todos os 5 entregues
	if got := run1.seqs(); !equalU64(got, []uint64{1, 2, 3, 4, 5}) {
		t.Fatalf("run1 entregou %v, esperado 1..5", got)
	}
	// O cursor durável deve estar em 3 (último ACK contíguo).
	if seq, ok := cs.Load("worker", "s1"); !ok || seq != 3 {
		t.Fatalf("cursor durável = (%d,%v), esperado 3", seq, ok)
	}
	sub1.Unsubscribe() // "reinício"

	// Run 2: mesma subscrição/cursor. Deve retomar de seq 4 (cursor+1), 0 skips,
	// re-entregando 4 e 5 (at-least-once).
	run2 := newRecorder()
	sub2, err := b.Subscribe(ctx, SubConfig{
		Name: "worker", Filter: Filter{Streams: []string{"s1"}}, Handler: ackAll(run2),
	})
	if err != nil {
		t.Fatal(err)
	}
	run2.waitN(t, 2, 2*time.Second)
	got := run2.seqs()
	if !equalU64(got, []uint64{4, 5}) {
		t.Fatalf("run2 retomou %v, esperado exactamente [4 5] (0 skips, at-least-once)", got)
	}
	// Após ACK de 4 e 5, o cursor avança para 5.
	deadline := time.Now().Add(time.Second)
	for {
		if seq, ok := cs.Load("worker", "s1"); ok && seq == 5 {
			break
		}
		if time.Now().After(deadline) {
			seq, _ := cs.Load("worker", "s1")
			t.Fatalf("cursor não avançou para 5 (=%d)", seq)
		}
		time.Sleep(5 * time.Millisecond)
	}
	sub2.Unsubscribe()
}

// TestRetomaCobreEventosNovosAposReinicio verifica que, além do não-confirmado,
// eventos NOVOS acrescentados entre reinícios também são entregues sem buracos.
func TestRetomaCobreEventosNovosAposReinicio(t *testing.T) {
	cs := NewMemoryCursorStore()
	b, es := newTestBus(t, cs)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		appendEv(t, es, "s1", "t1", "p1") // 1..3
	}
	run1 := newRecorder()
	sub1, err := b.Subscribe(ctx, SubConfig{
		Name: "w", Filter: Filter{Streams: []string{"s1"}}, Handler: ackAll(run1),
	})
	if err != nil {
		t.Fatal(err)
	}
	run1.waitN(t, 3, 2*time.Second)
	sub1.Unsubscribe()

	// Novos eventos enquanto ninguém subscreve.
	for i := 0; i < 2; i++ {
		appendEv(t, es, "s1", "t1", "p1") // 4..5
	}
	run2 := newRecorder()
	sub2, err := b.Subscribe(ctx, SubConfig{
		Name: "w", Filter: Filter{Streams: []string{"s1"}}, Handler: ackAll(run2),
	})
	if err != nil {
		t.Fatal(err)
	}
	run2.waitN(t, 2, 2*time.Second)
	if got := run2.seqs(); !equalU64(got, []uint64{4, 5}) {
		t.Fatalf("run2 = %v, esperado [4 5]", got)
	}
	sub2.Unsubscribe()
}

// TestReplayPorFromSeq cobre o critério: reprocessar a partir de um seq
// arbitrário.
func TestReplayPorFromSeq(t *testing.T) {
	b, es := newTestBus(t, nil)
	for i := 0; i < 6; i++ {
		appendEv(t, es, "s1", "t1", "p1") // 1..6
	}
	from := uint64(3)
	rec := newRecorder()
	sub, err := b.Subscribe(context.Background(), SubConfig{
		Name: "replayer", Filter: Filter{Streams: []string{"s1"}}, FromSeq: &from, Handler: ackAll(rec),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec.waitN(t, 4, 2*time.Second)
	if got := rec.seqs(); !equalU64(got, []uint64{3, 4, 5, 6}) {
		t.Fatalf("replay = %v, esperado [3 4 5 6]", got)
	}
	sub.Unsubscribe()
}

// TestCosturaCatchUpLiveSemSaltos exercita a fronteira: história + eventos que
// chegam durante a transição, sem buracos nem duplicados descontrolados.
func TestCosturaCatchUpLiveSemSaltos(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	// História inicial.
	for i := 0; i < 20; i++ {
		appendEv(t, es, "s1", "t1", "p1") // 1..20
	}
	rec := newRecorder()
	sub, err := b.Subscribe(ctx, SubConfig{
		Name: "seam", Filter: Filter{Streams: []string{"s1"}}, Handler: ackAll(rec),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Continua a produzir imediatamente (durante/após a subscrição) para forçar a
	// costura catch-up→live.
	for i := 0; i < 30; i++ {
		appendEv(t, es, "s1", "t1", "p1") // 21..50
	}
	rec.waitN(t, 50, 3*time.Second)

	got := sortedU64(rec.seqs())
	want := make([]uint64, 50)
	for i := range want {
		want[i] = uint64(i + 1)
	}
	if !equalU64(got, want) {
		t.Fatalf("costura com saltos/buracos: got %v", got)
	}
	// Ordem monotónica por stream na sequência de entrega (sem reordenação).
	raw := rec.seqs()
	for i := 1; i < len(raw); i++ {
		if raw[i] <= raw[i-1] {
			t.Fatalf("ordem não monotónica na posição %d: %v", i, raw[:i+1])
		}
	}
	sub.Unsubscribe()
}

// TestSnapshotCursorStoreDurabilidade verifica a variante durável: cada ACK é
// espelhado para o sink.
func TestSnapshotCursorStoreDurabilidade(t *testing.T) {
	persisted := map[string]uint64{}
	var mu sync.Mutex
	cs := NewSnapshotCursorStore(nil, func(sub, stream string, seq uint64) error {
		mu.Lock()
		persisted[cursorKey(sub, stream)] = seq
		mu.Unlock()
		return nil
	})
	b, es := newTestBus(t, cs)
	for i := 0; i < 3; i++ {
		appendEv(t, es, "s1", "t1", "p1")
	}
	rec := newRecorder()
	sub, err := b.Subscribe(context.Background(), SubConfig{
		Name: "dur", Filter: Filter{Streams: []string{"s1"}}, Handler: ackAll(rec),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec.waitN(t, 3, 2*time.Second)
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		v := persisted[cursorKey("dur", "s1")]
		mu.Unlock()
		if v == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sink durável não recebeu cursor=3 (=%d)", v)
		}
		time.Sleep(5 * time.Millisecond)
	}
	sub.Unsubscribe()
}
