package bus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// TestReplaySemStreamsRejeitado (AOS-009-Q1): pedir replay (FromSeq) sem Streams
// no filtro é irrealizável — a leitura histórica é por stream — e deve falhar
// rápido com ErrConfig em vez de entregar silenciosamente só live.
func TestReplaySemStreamsRejeitado(t *testing.T) {
	b, _ := newTestBus(t, nil)
	from := uint64(1)
	cases := []struct {
		name string
		f    Filter
	}{
		{"type-only", Filter{Types: []string{"t1"}}},
		{"producer-only", Filter{Producers: []string{"p1"}}},
		{"sem filtro", Filter{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := b.Subscribe(context.Background(), SubConfig{
				Name: "x", Filter: tc.f, FromSeq: &from, Handler: ackAll(newRecorder()),
			})
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("esperado ErrConfig para replay sem Streams, obtido %v", err)
			}
		})
	}
	// Replay COM Streams continua válido.
	if _, err := b.Subscribe(context.Background(), SubConfig{
		Name: "ok", Filter: Filter{Streams: []string{"s1"}}, FromSeq: &from, Handler: ackAll(newRecorder()),
	}); err != nil {
		t.Fatalf("replay com Streams não devia falhar: %v", err)
	}
}

// TestTypeOnlyLiveOnlySemCatchUp (AOS-009-Q1): documenta a limitação intencional
// — uma subscrição só por type (sem Streams) é fan-out válido mas recebe APENAS
// live a partir da subscrição, sem catch-up da história anterior.
func TestTypeOnlyLiveOnlySemCatchUp(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	// História ANTES de subscrever (noutro stream).
	appendEv(t, es, "s1", "t1", "p1") // s1#1
	appendEv(t, es, "s1", "t1", "p1") // s1#2

	rec := newRecorder()
	sub, err := b.Subscribe(ctx, SubConfig{
		Name: "type-only", Filter: Filter{Types: []string{"t1"}}, Handler: ackAll(rec),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Evento live após a subscrição.
	appendEv(t, es, "s2", "t1", "p1") // s2#1 (live)
	rec.waitN(t, 1, 2*time.Second)

	// Folga para garantir que a história anterior NÃO é entregue (só live).
	time.Sleep(80 * time.Millisecond)
	if got := rec.count(); got != 1 {
		t.Fatalf("type-only devia receber só o live (1), recebeu %d: %v", got, rec.seqs())
	}
	sub.Unsubscribe()
}

// TestDropOldestCursorAvancaNaoPreso (AOS-009-Q2): sob DropOldest, os eventos
// descartados são marcados como buracos conhecidos e o cursor AVANÇA para além
// deles — não fica preso no primeiro descarte. No reinício não relê quase todo o
// log.
func TestDropOldestCursorAvancaNaoPreso(t *testing.T) {
	cs := NewMemoryCursorStore()
	b, es := newTestBus(t, cs)
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
	for i := 0; i < n; i++ {
		appendEv(t, es, "s1", "t1", "p1") // seq 1..200
	}
	// Espera que haja descartes.
	waitFor(t, 2*time.Second, "descartes", func() bool { return b.Metrics().Dropped > 0 })
	close(gate)
	// Estabiliza: todos os appends processados (entregues OU descartados).
	waitFor(t, 3*time.Second, "estabilizar", func() bool {
		return int64(b.Metrics().Dropped)+delivered.Load() >= n
	})
	// O cursor durável deve alcançar a cabeça (200): cada seq foi resolvido (ack
	// ou descarte). Sem a marca de buraco, ficaria preso perto de 0.
	waitFor(t, 2*time.Second, "cursor na cabeça", func() bool {
		seq, ok := cs.Load("drop", "s1")
		return ok && seq == n
	})
	sub.Unsubscribe()

	// "Reinício": com o cursor na cabeça, 0 re-entregas (não relê o log).
	run2 := newRecorder()
	sub2, err := b.Subscribe(ctx, SubConfig{
		Name: "drop", Filter: Filter{Streams: []string{"s1"}}, Handler: ackAll(run2),
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if got := run2.count(); got != 0 {
		t.Fatalf("reinício releu %d eventos apesar do cursor na cabeça (esperado 0)", got)
	}
	sub2.Unsubscribe()
}

// TestDeadLetterOverflowCursorAvanca (AOS-009-Q2): sob DeadLetter em overflow, o
// evento fica capturado na DLQ e o cursor avança para além dele (não fica preso).
func TestDeadLetterOverflowCursorAvanca(t *testing.T) {
	cs := NewMemoryCursorStore()
	b, es := newTestBus(t, cs)
	ctx := context.Background()

	const n = 200
	gate := make(chan struct{})
	h := func(d *Delivery) {
		<-gate
		d.Ack()
	}
	sub, err := b.Subscribe(ctx, SubConfig{
		Name: "dlq", Filter: Filter{Streams: []string{"s1"}},
		Handler: h, Buffer: 4, Overflow: DeadLetter,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		appendEv(t, es, "s1", "t1", "p1")
	}
	waitFor(t, 2*time.Second, "dead-letters", func() bool { return b.DeadLetter().Len() > 0 })
	close(gate)
	waitFor(t, 3*time.Second, "cursor na cabeça", func() bool {
		seq, ok := cs.Load("dlq", "s1")
		return ok && seq == n
	})
	sub.Unsubscribe()

	run2 := newRecorder()
	sub2, err := b.Subscribe(ctx, SubConfig{
		Name: "dlq", Filter: Filter{Streams: []string{"s1"}}, Handler: ackAll(run2),
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if got := run2.count(); got != 0 {
		t.Fatalf("reinício releu %d apesar do cursor na cabeça (esperado 0)", got)
	}
	sub2.Unsubscribe()
}

// TestCursorFailClosedNaoAvancaSemDurabilidade (AOS-009-Q4): com um
// SnapshotCursorStore fail-closed cujo sink falha, o cursor em memória NÃO avança
// (não se dá por durável o que não foi persistido); o reinício re-entrega
// (at-least-once). Quando o sink recupera, o cursor passa a persistir.
func TestCursorFailClosedNaoAvancaSemDurabilidade(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	boom := errors.New("sink em baixo")
	cs := NewSnapshotCursorStore(nil, func(sub, stream string, seq uint64) error {
		if failing.Load() {
			return boom
		}
		return nil
	})
	b, es := newTestBus(t, cs)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		appendEv(t, es, "s1", "t1", "p1") // 1..3
	}
	run1 := newRecorder()
	sub1, err := b.Subscribe(ctx, SubConfig{
		Name: "fc", Filter: Filter{Streams: []string{"s1"}}, Handler: ackAll(run1),
	})
	if err != nil {
		t.Fatal(err)
	}
	run1.waitN(t, 3, 2*time.Second)
	// Deu Ack a 1..3, mas o Save falha → nada fica durável.
	time.Sleep(120 * time.Millisecond)
	if seq, ok := cs.Load("fc", "s1"); ok {
		t.Fatalf("cursor não devia estar durável com sink em falha (=%d)", seq)
	}
	sub1.Unsubscribe()

	// Reinício com sink ainda em falha: re-entrega 1..3.
	run2 := newRecorder()
	sub2, err := b.Subscribe(ctx, SubConfig{
		Name: "fc", Filter: Filter{Streams: []string{"s1"}}, Handler: ackAll(run2),
	})
	if err != nil {
		t.Fatal(err)
	}
	run2.waitN(t, 3, 2*time.Second)
	if got := run2.seqs(); !equalU64(got, []uint64{1, 2, 3}) {
		t.Fatalf("reinício com cursor não-durável devia re-entregar 1..3, obtido %v", got)
	}
	sub2.Unsubscribe()

	// Sink recupera: um novo Ack já persiste o piso contíguo.
	failing.Store(false)
	run3 := newRecorder()
	sub3, err := b.Subscribe(ctx, SubConfig{
		Name: "fc", Filter: Filter{Streams: []string{"s1"}}, Handler: ackAll(run3),
	})
	if err != nil {
		t.Fatal(err)
	}
	run3.waitN(t, 3, 2*time.Second)
	waitFor(t, time.Second, "cursor durável=3", func() bool {
		seq, ok := cs.Load("fc", "s1")
		return ok && seq == 3
	})
	sub3.Unsubscribe()
}

// TestLatenciaEntregaPushP95Metrica (AOS-009-Q3): o p95 de latência é EXPOSTO em
// Bus.Metrics() (não só calculado dentro de um teste) e mantém-se < 250 ms sob
// carga concorrente e incluindo entregas de catch-up.
func TestLatenciaEntregaPushP95Metrica(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	const streams = 4
	const preHist = 50
	const live = 50

	// Fase de história (catch-up): pré-preenche vários streams antes de subscrever.
	for s := 0; s < streams; s++ {
		st := fmt.Sprintf("s%d", s)
		for i := 0; i < preHist; i++ {
			appendEv(t, es, st, "t1", "p1")
		}
	}

	recs := make([]*recorder, streams)
	subs := make([]Subscription, streams)
	for s := 0; s < streams; s++ {
		recs[s] = newRecorder()
		st := fmt.Sprintf("s%d", s)
		sub, err := b.Subscribe(ctx, SubConfig{
			Name: "lat-" + st, Filter: Filter{Streams: []string{st}}, Handler: ackAll(recs[s]),
		})
		if err != nil {
			t.Fatal(err)
		}
		subs[s] = sub
	}

	// Produz live concorrentemente durante/após a subscrição (força a costura).
	var wg sync.WaitGroup
	errCh := make(chan error, streams)
	for s := 0; s < streams; s++ {
		wg.Add(1)
		st := fmt.Sprintf("s%d", s)
		go func(st string) {
			defer wg.Done()
			for i := 0; i < live; i++ {
				if _, err := es.Append(ctx, st, eventstore.EventInput{
					Type: "t1", Producer: eventstore.Producer{NHIID: "p1"}, Payload: []byte(`{}`),
				}); err != nil {
					errCh <- err
					return
				}
			}
		}(st)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("append live concorrente: %v", err)
	}

	for s := 0; s < streams; s++ {
		recs[s].waitN(t, preHist+live, 5*time.Second)
	}

	total := uint64(streams * (preHist + live))
	snap := b.Metrics()
	if snap.Delivered < total {
		t.Fatalf("entregas insuficientes: %d < %d", snap.Delivered, total)
	}
	if snap.P95Latency <= 0 {
		t.Fatalf("P95Latency não observável via Metrics() (=%v)", snap.P95Latency)
	}
	if snap.P95Latency >= 250*time.Millisecond {
		t.Fatalf("P95 exposto = %v, esperado < 250ms", snap.P95Latency)
	}
	if !(snap.P50Latency <= snap.P95Latency &&
		snap.P95Latency <= snap.P99Latency &&
		snap.P99Latency <= snap.MaxLatency) {
		t.Fatalf("percentis inconsistentes: p50=%v p95=%v p99=%v max=%v",
			snap.P50Latency, snap.P95Latency, snap.P99Latency, snap.MaxLatency)
	}
	for _, sub := range subs {
		sub.Unsubscribe()
	}
}

// waitFor faz polling de cond até true ou timeout (falha o teste no timeout).
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout à espera de: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
