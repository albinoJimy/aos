package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- helpers ---------------------------------------------------------------

func mustNew(t *testing.T, opts ...Option) *Store {
	t.Helper()
	s, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func input(runID, stepID, typ string, payload string) EventInput {
	return EventInput{
		Type:          typ,
		Payload:       json.RawMessage(payload),
		SchemaVersion: SchemaVersion,
		RunID:         runID,
		StepID:        stepID,
		Producer: Producer{
			NHIID:           "nhi:agent:test@v1",
			DelegationChain: []DelegationHop{{Sub: "human:armando", ActAs: "nhi:agent:test@v1"}},
			Scope:           []string{"fs:read"},
		},
	}
}

// --- append / read básico --------------------------------------------------

func TestAppendReadBasic(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		res, err := s.Append(ctx, "run-A", input("run-A", fmt.Sprintf("step-%d", i), "turn.recorded", `{"n":1}`))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if res.Status != StatusCommitted {
			t.Fatalf("append %d: status = %q, quero committed", i, res.Status)
		}
		if res.Seq != uint64(i) {
			t.Fatalf("append %d: seq = %d, quero %d", i, res.Seq, i)
		}
		if res.Event.EventID == "" || len(res.Event.EventID) != 26 {
			t.Fatalf("append %d: event_id inválido %q", i, res.Event.EventID)
		}
		if res.Event.IdempotencyKey != "run-A:"+fmt.Sprintf("step-%d", i) {
			t.Fatalf("append %d: idempotency_key = %q", i, res.Event.IdempotencyKey)
		}
	}
	evs, err := s.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("read: %d eventos, quero 3", len(evs))
	}
	for i, ev := range evs {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("read[%d].Seq = %d, quero %d", i, ev.Seq, i+1)
		}
	}
	// fromSeq inclusivo
	evs2, err := s.Read(ctx, "run-A", 2)
	if err != nil {
		t.Fatalf("read from 2: %v", err)
	}
	if len(evs2) != 2 || evs2[0].Seq != 2 {
		t.Fatalf("read from 2: %+v", evs2)
	}
}

// --- append-only: sem API de mutação --------------------------------------

func TestAppendOnly_NoMutationMethods(t *testing.T) {
	iface := reflect.TypeOf((*EventStore)(nil)).Elem()
	forbidden := []string{"update", "delete", "remove", "set", "put", "overwrite", "truncate", "mutate", "modify"}
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		lower := ""
		for _, r := range name {
			if r >= 'A' && r <= 'Z' {
				r = r + 32
			}
			lower += string(r)
		}
		for _, f := range forbidden {
			if lower == f || (len(lower) >= len(f) && lower[:len(f)] == f) {
				t.Fatalf("EventStore expõe método proibido %q (append-only estrito)", name)
			}
		}
	}
	// A superfície pública é exactamente Append/Read/Subscribe/Close.
	want := map[string]bool{"Append": true, "Read": true, "Subscribe": true, "Close": true}
	if iface.NumMethod() != len(want) {
		t.Fatalf("EventStore tem %d métodos, quero %d", iface.NumMethod(), len(want))
	}
	for i := 0; i < iface.NumMethod(); i++ {
		if !want[iface.Method(i).Name] {
			t.Fatalf("método inesperado %q", iface.Method(i).Name)
		}
	}
}

func TestAppendOnly_OverwriteRejected(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if _, err := s.Append(ctx, "run-A", input("run-A", fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	// Tentar escrever numa posição no passado (expected < último) → violação.
	_, err := s.Append(ctx, "run-A", input("run-A", "sX", "t", `{}`), WithExpectedSeq(1))
	if !errors.Is(err, ErrAppendOnlyViolation) {
		t.Fatalf("overwrite passado: err = %v, quero ErrAppendOnlyViolation", err)
	}
	// O log permanece inalterado (3 eventos).
	evs, err := s.Read(ctx, "run-A", 1)
	if err != nil || len(evs) != 3 {
		t.Fatalf("log alterado após violação: %d eventos, err=%v", len(evs), err)
	}
}

func TestReadReturnsCopies(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, "run-A", input("run-A", "s1", "t", `{"secret":"x"}`)); err != nil {
		t.Fatalf("append: %v", err)
	}
	evs, err := s.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Mutar a cópia devolvida.
	evs[0].Payload[0] = 'Z'
	if len(evs[0].Producer.Scope) > 0 {
		evs[0].Producer.Scope[0] = "ADMIN"
	}
	if len(evs[0].Producer.DelegationChain) > 0 {
		evs[0].Producer.DelegationChain[0].Sub = "attacker"
	}
	// Reler: o estado guardado tem de estar intacto.
	evs2, err := s.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if string(evs2[0].Payload) != `{"secret":"x"}` {
		t.Fatalf("payload mutado no store: %s", evs2[0].Payload)
	}
	if evs2[0].Producer.Scope[0] != "fs:read" {
		t.Fatalf("scope mutado no store: %v", evs2[0].Producer.Scope)
	}
	if evs2[0].Producer.DelegationChain[0].Sub != "human:armando" {
		t.Fatalf("delegation mutada no store: %v", evs2[0].Producer.DelegationChain)
	}
}

func TestReadStreamNotFound(t *testing.T) {
	s := mustNew(t)
	_, err := s.Read(context.Background(), "inexistente", 1)
	if !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("err = %v, quero ErrStreamNotFound", err)
	}
}

// --- ordenação + monotonicidade sob concorrência ---------------------------

func TestSeqMonotonicUnderConcurrency(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	const n = 200
	seqs := make([]uint64, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			res, err := s.Append(ctx, "run-hot", input("run-hot", fmt.Sprintf("s%d", i), "t", `{}`))
			if err != nil {
				t.Errorf("append %d: %v", i, err)
				return
			}
			seqs[i] = res.Seq
		}(i)
	}
	wg.Wait()

	// Todos os seqs 1..n únicos e contíguos.
	seen := make(map[uint64]bool, n)
	for _, sq := range seqs {
		if sq < 1 || sq > n {
			t.Fatalf("seq fora de intervalo: %d", sq)
		}
		if seen[sq] {
			t.Fatalf("seq duplicado: %d", sq)
		}
		seen[sq] = true
	}
	if len(seen) != n {
		t.Fatalf("%d seqs distintos, quero %d", len(seen), n)
	}
	// Ordem total: Read devolve 1..n em ordem estrita.
	evs, err := s.Read(ctx, "run-hot", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(evs) != n {
		t.Fatalf("read: %d, quero %d", len(evs), n)
	}
	for i, ev := range evs {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("read[%d].Seq = %d, quero %d (gapless/monotónico)", i, ev.Seq, i+1)
		}
	}
}

// --- expected_seq CAS ------------------------------------------------------

func TestExpectedSeqCAS(t *testing.T) {
	tests := []struct {
		name     string
		expected uint64
		wantErr  error
		wantSeq  uint64
	}{
		{"match", 3, nil, 4},
		{"ahead_conflict", 5, ErrSeqConflict, 0},
		{"past_append_only", 1, ErrAppendOnlyViolation, 0},
		{"zero_on_nonempty_past", 0, ErrAppendOnlyViolation, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := mustNew(t)
			ctx := context.Background()
			for i := 1; i <= 3; i++ {
				if _, err := s.Append(ctx, "st", input("st", fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			res, err := s.Append(ctx, "st", input("st", "cas", "t", `{}`), WithExpectedSeq(tc.expected))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, quero %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err inesperado: %v", err)
			}
			if res.Seq != tc.wantSeq {
				t.Fatalf("seq = %d, quero %d", res.Seq, tc.wantSeq)
			}
		})
	}
}

// --- idempotência ----------------------------------------------------------

func TestIdempotency(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	in := input("run-9", "step-014", "tool_result", `{"v":1}`)
	res1, err := s.Append(ctx, "run-9", in)
	if err != nil {
		t.Fatalf("1º append: %v", err)
	}
	if res1.Status != StatusCommitted {
		t.Fatalf("1º status = %q", res1.Status)
	}
	// 2ª escrita com a mesma idempotency_key (payload diferente, ignorado).
	in2 := input("run-9", "step-014", "tool_result", `{"v":999}`)
	res2, err := s.Append(ctx, "run-9", in2)
	if err != nil {
		t.Fatalf("2º append: %v", err)
	}
	if res2.Status != StatusDuplicate {
		t.Fatalf("2º status = %q, quero duplicate", res2.Status)
	}
	if res2.Seq != res1.Seq {
		t.Fatalf("duplicate seq = %d, quero original %d", res2.Seq, res1.Seq)
	}
	// O evento original (não o 2º payload) é o que fica.
	if string(res2.Event.Payload) != `{"v":1}` {
		t.Fatalf("duplicate devolveu payload errado: %s", res2.Event.Payload)
	}
	// Log inalterado: exactamente 1 evento.
	evs, err := s.Read(ctx, "run-9", 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("log duplicado: %d eventos, err=%v", len(evs), err)
	}
}

func TestIdempotencySurvivesFailover(t *testing.T) {
	s := mustNew(t, WithReplicas(3), WithQuorum(2))
	ctx := context.Background()
	in := input("run-9", "step-014", "tool_result", `{"v":1}`)
	res1, err := s.Append(ctx, "run-9", in)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	// Matar o líder → failover.
	leader := s.Leader()
	if err := s.Kill(leader); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if s.Leader() == leader || s.Leader() == -1 {
		t.Fatalf("falha na eleição: líder = %d", s.Leader())
	}
	// Reescrita com a mesma chave, no novo líder, ainda é duplicate + seq original.
	res2, err := s.Append(ctx, "run-9", in)
	if err != nil {
		t.Fatalf("append pós-failover: %v", err)
	}
	if res2.Status != StatusDuplicate || res2.Seq != res1.Seq {
		t.Fatalf("dedup não sobreviveu ao failover: status=%q seq=%d (quero duplicate/%d)", res2.Status, res2.Seq, res1.Seq)
	}
}

// --- failover / quórum -----------------------------------------------------

func TestFailover_NoConfirmedLoss(t *testing.T) {
	s := mustNew(t, WithReplicas(3), WithQuorum(2))
	ctx := context.Background()
	const n = 5
	for i := 1; i <= n; i++ {
		if _, err := s.Append(ctx, "run-A", input("run-A", fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	leader := s.Leader()
	if err := s.Kill(leader); err != nil {
		t.Fatalf("kill líder: %v", err)
	}
	if s.Leader() == -1 {
		t.Fatalf("sem líder após kill")
	}
	// 0 eventos confirmados perdidos.
	evs, err := s.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("read pós-failover: %v", err)
	}
	if len(evs) != n {
		t.Fatalf("perda de eventos confirmados: %d, quero %d", len(evs), n)
	}
	for i, ev := range evs {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("ordem quebrada pós-failover em [%d]: seq %d", i, ev.Seq)
		}
	}
	// O cluster continua a aceitar escritas (quórum 2 ainda satisfeito).
	res, err := s.Append(ctx, "run-A", input("run-A", "s6", "t", `{}`))
	if err != nil {
		t.Fatalf("append pós-failover: %v", err)
	}
	if res.Seq != n+1 {
		t.Fatalf("seq pós-failover = %d, quero %d", res.Seq, n+1)
	}
}

func TestSubQuorumRejected_NoTrace(t *testing.T) {
	s := mustNew(t, WithReplicas(3), WithQuorum(2))
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		if _, err := s.Append(ctx, "run-A", input("run-A", fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	// Derrubar duas réplicas → apenas 1 viva (< quórum 2).
	alive := s.Replicas()
	killed := 0
	for _, id := range alive {
		if killed == 2 {
			break
		}
		if s.IsAlive(id) {
			if err := s.Kill(id); err != nil {
				t.Fatalf("kill %d: %v", id, err)
			}
			killed++
		}
	}
	if s.AliveCount() != 1 {
		t.Fatalf("vivas = %d, quero 1", s.AliveCount())
	}
	// Escrita sub-quórum é rejeitada (fail-closed).
	_, err := s.Append(ctx, "run-A", input("run-A", "s3", "t", `{}`))
	if !errors.Is(err, ErrNoQuorum) {
		t.Fatalf("err = %v, quero ErrNoQuorum", err)
	}
	// Não deixou rasto: ainda 2 eventos, mesmo após reviver o cluster (failover).
	for _, id := range s.Replicas() {
		if !s.IsAlive(id) {
			if err := s.Revive(id); err != nil {
				t.Fatalf("revive %d: %v", id, err)
			}
		}
	}
	evs, err := s.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("escrita sub-quórum deixou rasto: %d eventos, quero 2", len(evs))
	}
}

func TestNoLeaderRejects(t *testing.T) {
	s := mustNew(t, WithReplicas(1), WithQuorum(1))
	ctx := context.Background()
	if _, err := s.Append(ctx, "st", input("st", "s1", "t", `{}`)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Kill(0); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if s.Leader() != -1 {
		t.Fatalf("líder = %d, quero -1", s.Leader())
	}
	if _, err := s.Append(ctx, "st", input("st", "s2", "t", `{}`)); !errors.Is(err, ErrNoQuorum) {
		t.Fatalf("append sem líder: err = %v, quero ErrNoQuorum", err)
	}
	if _, err := s.Read(ctx, "st", 1); !errors.Is(err, ErrNoQuorum) {
		t.Fatalf("read sem líder: err = %v, quero ErrNoQuorum", err)
	}
}

// --- transporte push: latência + ordem -------------------------------------

func TestFanoutLatencyAndOrder(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	const (
		m = 50  // subscritores
		k = 100 // eventos
	)
	sendTimes := make([]time.Time, k)

	type subState struct {
		lat  []time.Duration
		seqs []uint64
	}
	states := make([]*subState, m)
	var wg sync.WaitGroup
	wg.Add(m * k)

	for i := 0; i < m; i++ {
		st := &subState{lat: make([]time.Duration, 0, k), seqs: make([]uint64, 0, k)}
		states[i] = st
		_, err := s.Subscribe(ctx, Filter{Streams: []string{"run-A"}}, func(ev Event) {
			st.lat = append(st.lat, time.Since(sendTimes[ev.Seq-1]))
			st.seqs = append(st.seqs, ev.Seq)
			wg.Done()
		})
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
	}

	for i := 0; i < k; i++ {
		sendTimes[i] = time.Now()
		if _, err := s.Append(ctx, "run-A", input("run-A", fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	wg.Wait()

	// Ordem preservada em cada subscritor.
	for i, st := range states {
		if len(st.seqs) != k {
			t.Fatalf("sub %d recebeu %d, quero %d", i, len(st.seqs), k)
		}
		for j, sq := range st.seqs {
			if sq != uint64(j+1) {
				t.Fatalf("sub %d ordem quebrada em [%d]: seq %d", i, j, sq)
			}
		}
	}

	// p95 de fan-out < 250 ms.
	all := make([]time.Duration, 0, m*k)
	for _, st := range states {
		all = append(all, st.lat...)
	}
	sort.Slice(all, func(a, b int) bool { return all[a] < all[b] })
	p95 := all[int(float64(len(all))*0.95)]
	if p95 >= 250*time.Millisecond {
		t.Fatalf("p95 de fan-out = %v, quero < 250ms", p95)
	}
	t.Logf("fan-out p95 = %v (n=%d, m=%d subs, k=%d eventos)", p95, len(all), m, k)
}

func TestSlowSubscriberDoesNotBlockOthers(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	const k = 20

	release := make(chan struct{})
	var slowGot atomic.Int64
	_, err := s.Subscribe(ctx, Filter{}, func(ev Event) {
		<-release // subscritor lento: bloqueia até libertação
		slowGot.Add(1)
	})
	if err != nil {
		t.Fatalf("subscribe lento: %v", err)
	}

	var fastGot atomic.Int64
	var wg sync.WaitGroup
	wg.Add(k)
	_, err = s.Subscribe(ctx, Filter{}, func(ev Event) {
		fastGot.Add(1)
		wg.Done()
	})
	if err != nil {
		t.Fatalf("subscribe rápido: %v", err)
	}

	for i := 0; i < k; i++ {
		if _, err := s.Append(ctx, "run-A", input("run-A", fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// O subscritor rápido recebe tudo mesmo com o lento bloqueado.
	wg.Wait()
	if fastGot.Load() != k {
		t.Fatalf("subscritor rápido recebeu %d, quero %d", fastGot.Load(), k)
	}
	if slowGot.Load() != 0 {
		t.Fatalf("subscritor lento processou %d, quero 0 (ainda bloqueado)", slowGot.Load())
	}
	close(release) // liberta o lento; não deve deixar leaks
}

func TestSubscribeFilter(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	var got atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2) // só esperamos 2 (type=keep no stream run-A)
	_, err := s.Subscribe(ctx, Filter{Streams: []string{"run-A"}, Types: []string{"keep"}}, func(ev Event) {
		got.Add(1)
		wg.Done()
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// Não corresponde (stream errado).
	_, _ = s.Append(ctx, "run-B", input("run-B", "s1", "keep", `{}`))
	// Não corresponde (type errado).
	_, _ = s.Append(ctx, "run-A", input("run-A", "s2", "skip", `{}`))
	// Correspondem.
	_, _ = s.Append(ctx, "run-A", input("run-A", "s3", "keep", `{}`))
	_, _ = s.Append(ctx, "run-A", input("run-A", "s4", "keep", `{}`))
	wg.Wait()
	if got.Load() != 2 {
		t.Fatalf("recebidos %d, quero 2", got.Load())
	}
}

func TestUnsubscribeNoLeak(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	before := runtime.NumGoroutine()
	subs := make([]Subscription, 0, 20)
	for i := 0; i < 20; i++ {
		sub, err := s.Subscribe(ctx, Filter{}, func(Event) {})
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		subs = append(subs, sub)
	}
	for _, sub := range subs {
		sub.Unsubscribe()
		sub.Unsubscribe() // idempotente
	}
	assertGoroutinesSettle(t, before)
}

func TestCloseStopsSubscribers(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		if _, err := s.Subscribe(ctx, Filter{}, func(Event) {}); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("2º close: err = %v, quero ErrClosed", err)
	}
	assertGoroutinesSettle(t, before)
}

func assertGoroutinesSettle(t *testing.T, before int) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fuga de goroutines: antes=%d agora=%d", before, runtime.NumGoroutine())
}

// --- estados fechados / config ---------------------------------------------

func TestClosedRejects(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ctx := context.Background()
	if _, err := s.Append(ctx, "st", input("st", "s1", "t", `{}`)); !errors.Is(err, ErrClosed) {
		t.Fatalf("append pós-close: %v", err)
	}
	if _, err := s.Read(ctx, "st", 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("read pós-close: %v", err)
	}
	if _, err := s.Subscribe(ctx, Filter{}, func(Event) {}); !errors.Is(err, ErrClosed) {
		t.Fatalf("subscribe pós-close: %v", err)
	}
}

func TestContextCancelled(t *testing.T) {
	s := mustNew(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Append(ctx, "st", input("st", "s1", "t", `{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("append: err = %v, quero context.Canceled", err)
	}
	if _, err := s.Read(ctx, "st", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("read: err = %v, quero context.Canceled", err)
	}
	if _, err := s.Subscribe(ctx, Filter{}, func(Event) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("subscribe: err = %v, quero context.Canceled", err)
	}
}

func TestNewConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		ok   bool
	}{
		{"default", nil, true},
		{"custom_ok", []Option{WithReplicas(5), WithQuorum(3)}, true},
		{"zero_replicas", []Option{WithReplicas(0)}, false},
		{"quorum_gt_replicas", []Option{WithReplicas(3), WithQuorum(4)}, false},
		{"quorum_default_majority", []Option{WithReplicas(4)}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(tc.opts...)
			if tc.ok {
				if err != nil {
					t.Fatalf("err inesperado: %v", err)
				}
				_ = s.Close()
			} else if !errors.Is(err, ErrConfig) {
				t.Fatalf("err = %v, quero ErrConfig", err)
			}
		})
	}
	// Quórum de maioria por omissão.
	s := mustNew(t, WithReplicas(5))
	if s.Quorum() != 3 {
		t.Fatalf("quórum default = %d, quero 3", s.Quorum())
	}
}

func TestKillReviveInvalid(t *testing.T) {
	s := mustNew(t, WithReplicas(3), WithQuorum(2))
	if err := s.Kill(99); !errors.Is(err, ErrInvalidReplica) {
		t.Fatalf("kill inválido: %v", err)
	}
	if err := s.Revive(0); !errors.Is(err, ErrInvalidReplica) {
		t.Fatalf("revive de réplica viva: %v", err)
	}
	if err := s.Kill(0); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if err := s.Kill(0); !errors.Is(err, ErrInvalidReplica) {
		t.Fatalf("kill duplo: %v", err)
	}
	if err := s.Revive(0); err != nil {
		t.Fatalf("revive: %v", err)
	}
}

// --- observabilidade -------------------------------------------------------

type countingObserver struct {
	committed, duplicate, rejected, published atomic.Int64
}

func (c *countingObserver) AppendCommitted(string, uint64, time.Duration) { c.committed.Add(1) }
func (c *countingObserver) AppendDuplicate(string, uint64)                { c.duplicate.Add(1) }
func (c *countingObserver) AppendRejected(string, error)                  { c.rejected.Add(1) }
func (c *countingObserver) Published(_ string, _ uint64, n int)           { c.published.Add(int64(n)) }

func TestObserverHooks(t *testing.T) {
	obs := &countingObserver{}
	s := mustNew(t, WithObserver(obs))
	ctx := context.Background()
	_, _ = s.Append(ctx, "st", input("st", "s1", "t", `{}`))
	_, _ = s.Append(ctx, "st", input("st", "s1", "t", `{}`)) // duplicate
	_, _ = s.Append(ctx, "st", input("st", "s2", "t", `{}`), WithExpectedSeq(99))
	if obs.committed.Load() != 1 {
		t.Fatalf("committed = %d, quero 1", obs.committed.Load())
	}
	if obs.duplicate.Load() != 1 {
		t.Fatalf("duplicate = %d, quero 1", obs.duplicate.Load())
	}
	if obs.rejected.Load() != 1 {
		t.Fatalf("rejected = %d, quero 1", obs.rejected.Load())
	}
}

// --- envelope / ULID -------------------------------------------------------

func TestEventMarshalJSONFields(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	res, err := s.Append(ctx, "run-A", input("run-A", "step-1", "tool.call.dispatched", `{"k":"v"}`))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	b, err := json.Marshal(res.Event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"event_id", "stream_id", "seq", "type", "ts", "producer", "payload", "schema_version", "run_id", "step_id", "idempotency_key"} {
		if _, ok := m[field]; !ok {
			t.Fatalf("campo obrigatório em falta no envelope JSON: %q", field)
		}
	}
	if string(m["schema_version"]) != `"1.0"` {
		t.Fatalf("schema_version = %s, quero \"1.0\"", m["schema_version"])
	}
}

func TestULIDFormatAndUniqueness(t *testing.T) {
	const n = 5000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id := newULID()
		if len(id) != 26 {
			t.Fatalf("ULID comprimento = %d, quero 26: %q", len(id), id)
		}
		for _, r := range id {
			if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z')) {
				t.Fatalf("carácter inválido em ULID %q: %c", id, r)
			}
		}
		if seen[id] {
			t.Fatalf("ULID duplicado: %q", id)
		}
		seen[id] = true
	}
}

func TestEncodeULIDDeterministic(t *testing.T) {
	var b [16]byte // tudo a zero → 26 x '0'
	if got := encodeULID(b); got != "00000000000000000000000000" {
		t.Fatalf("encode zeros = %q", got)
	}
	for i := range b {
		b[i] = 0xFF
	}
	got := encodeULID(b)
	if len(got) != 26 {
		t.Fatalf("comprimento = %d", len(got))
	}
	// 128 bits a 1 com 2 bits de padding à esquerda: primeiro char = 0b00111 = 7.
	if got[0] != '7' {
		t.Fatalf("primeiro char = %c, quero 7", got[0])
	}
	if got[25] != 'Z' {
		t.Fatalf("último char = %c, quero Z", got[25])
	}
}

// --- durabilidade: revive de réplica stale após perda de quórum -------------

// TestReviveStaleAfterQuorumLoss_NoSilentTruncation cobre o furo de durabilidade:
// perdido o quórum, reviver uma réplica desactualizada NÃO a promove a líder
// autoritativo — o store fica indisponível (ErrNoQuorum) em vez de servir um log
// truncado como completo. Quando uma réplica actualizada regressa, o store
// recupera e serve o log íntegro (prova também que a eleição prefere a réplica
// mais actualizada — logs divergentes).
func TestReviveStaleAfterQuorumLoss_NoSilentTruncation(t *testing.T) {
	s := mustNew(t, WithReplicas(3), WithQuorum(2))
	ctx := context.Background()

	// Commit de 5 eventos a todas as réplicas.
	for i := 1; i <= 5; i++ {
		if _, err := s.Append(ctx, "run-A", input("run-A", fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	// Matar uma réplica NÃO-líder → fica stale em count=5 (sem disparar eleição).
	staleID := -1
	for _, id := range s.Replicas() {
		if id != s.Leader() {
			staleID = id
			break
		}
	}
	if staleID == -1 {
		t.Fatal("não encontrei réplica não-líder")
	}
	if err := s.Kill(staleID); err != nil {
		t.Fatalf("kill stale: %v", err)
	}

	// Commit de +3 eventos (seq 6,7,8) confirmados com quórum 2 nas 2 vivas.
	for i := 6; i <= 8; i++ {
		if _, err := s.Append(ctx, "run-A", input("run-A", fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// Matar as 2 réplicas actualizadas → sem líder vivo.
	var updated []int
	for _, id := range s.Replicas() {
		if s.IsAlive(id) {
			updated = append(updated, id)
		}
	}
	for _, id := range updated {
		if err := s.Kill(id); err != nil {
			t.Fatalf("kill updated %d: %v", id, err)
		}
	}
	if s.Leader() != -1 {
		t.Fatalf("líder = %d, quero -1 (todas mortas)", s.Leader())
	}

	// Reviver a réplica stale: NÃO deve tornar-se líder autoritativo.
	if err := s.Revive(staleID); err != nil {
		t.Fatalf("revive stale: %v", err)
	}
	if s.Leader() != -1 {
		t.Fatalf("réplica stale foi promovida a líder (leader=%d); log truncado seria servido como autoritativo", s.Leader())
	}
	// Read NÃO deve devolver 5 eventos truncados com err=nil; deve recusar.
	if evs, err := s.Read(ctx, "run-A", 1); !errors.Is(err, ErrNoQuorum) {
		t.Fatalf("read com réplica stale: n=%d err=%v, quero ErrNoQuorum (sem servir log truncado)", len(evs), err)
	}
	if _, err := s.Append(ctx, "run-A", input("run-A", "s9", "t", `{}`)); !errors.Is(err, ErrNoQuorum) {
		t.Fatalf("append com réplica stale: err=%v, quero ErrNoQuorum", err)
	}

	// Regressa uma réplica actualizada → store recupera e serve o log íntegro (8).
	if err := s.Revive(updated[0]); err != nil {
		t.Fatalf("revive updated: %v", err)
	}
	if s.Leader() == -1 {
		t.Fatal("store não recuperou após regresso de réplica actualizada")
	}
	if s.Leader() == staleID {
		t.Fatalf("eleição preferiu a réplica stale (leader=%d)", s.Leader())
	}
	evs, err := s.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("read pós-recuperação: %v", err)
	}
	if len(evs) != 8 {
		t.Fatalf("pós-recuperação: %d eventos, quero 8 (log íntegro)", len(evs))
	}
	for i, ev := range evs {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("ordem quebrada em [%d]: seq %d", i, ev.Seq)
		}
	}
}

// --- CAS concorrente: erro devolvido ao perdedor ---------------------------

// TestConcurrentCASLoserError documenta o erro devolvido ao perdedor de uma
// corrida de CAS (dois WithExpectedSeq iguais): exactamente um vence (committed
// em n+1) e o outro recebe um erro re-tentável. Confirma o mapeamento actual
// (ErrAppendOnlyViolation, por o vencedor avançar last>expected) e que o
// chamador deve tratar tanto ErrAppendOnlyViolation como ErrSeqConflict como
// sinal para reler e re-tentar.
func TestConcurrentCASLoserError(t *testing.T) {
	s := mustNew(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if _, err := s.Append(ctx, "st", input("st", fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	var mu sync.Mutex
	var successes int
	var errs []error
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			res, err := s.Append(ctx, "st", input("st", fmt.Sprintf("cas-%d", i), "t", `{}`), WithExpectedSeq(3))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
				if res.Seq != 4 || res.Status != StatusCommitted {
					t.Errorf("vencedor: seq=%d status=%q, quero 4/committed", res.Seq, res.Status)
				}
			} else {
				errs = append(errs, err)
			}
		}(i)
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("vencedores = %d, quero exactamente 1", successes)
	}
	if len(errs) != 1 {
		t.Fatalf("perdedores = %d, quero exactamente 1", len(errs))
	}
	// O perdedor deve receber um erro re-tentável de concorrência optimista.
	if !errors.Is(errs[0], ErrAppendOnlyViolation) && !errors.Is(errs[0], ErrSeqConflict) {
		t.Fatalf("erro do perdedor = %v, quero ErrAppendOnlyViolation ou ErrSeqConflict (re-tentável)", errs[0])
	}
	// Estado íntegro: exactamente 4 eventos, o vencedor materializou seq 4.
	evs, err := s.Read(ctx, "st", 1)
	if err != nil || len(evs) != 4 {
		t.Fatalf("log = %d eventos, err=%v, quero 4", len(evs), err)
	}
}

// --- subscrição: ctx governa o ciclo de vida --------------------------------

// TestSubscribeCtxCancelUnsubscribes prova que cancelar o ctx do Subscribe
// desregista a subscrição e liberta a goroutine (sem fuga) e que deixa de haver
// entrega após o cancelamento.
func TestSubscribeCtxCancelUnsubscribes(t *testing.T) {
	s := mustNew(t)
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())

	var got atomic.Int64
	sub, err := s.Subscribe(ctx, Filter{}, func(Event) { got.Add(1) })
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// Uma entrega antes do cancelamento, para confirmar que estava viva.
	if _, err := s.Append(context.Background(), "run-A", input("run-A", "s1", "t", `{}`)); err != nil {
		t.Fatalf("append: %v", err)
	}

	cancel()
	// O cancelamento deve desregistar a subscrição e libertar as goroutines.
	assertGoroutinesSettle(t, before)

	baseline := got.Load()
	// Após o cancelamento não há mais entrega (a subscrição saiu do store).
	if _, err := s.Append(context.Background(), "run-A", input("run-A", "s2", "t", `{}`)); err != nil {
		t.Fatalf("append pós-cancel: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if got.Load() != baseline {
		t.Fatalf("entrega após ctx cancel: %d > baseline %d", got.Load(), baseline)
	}
	// Unsubscribe explícito continua idempotente após teardown por ctx.
	sub.Unsubscribe()
}

// --- benchmarks (DoD: throughput e latência registados) ---------------------

func BenchmarkAppend(b *testing.B) {
	s, err := New()
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Append(ctx, "run-bench", input("run-bench", fmt.Sprintf("s%d", i), "t", `{"k":"v"}`)); err != nil {
			b.Fatalf("append: %v", err)
		}
	}
}

func BenchmarkFanout(b *testing.B) {
	const subs = 50
	s, err := New()
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < subs; i++ {
		if _, err := s.Subscribe(ctx, Filter{}, func(Event) { wg.Done() }); err != nil {
			b.Fatalf("subscribe: %v", err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wg.Add(subs)
		if _, err := s.Append(ctx, "run-bench", input("run-bench", fmt.Sprintf("s%d", i), "t", `{}`)); err != nil {
			b.Fatalf("append: %v", err)
		}
		wg.Wait() // mede fan-out ponta-a-ponta (append + entrega a subs subscritores)
	}
}
