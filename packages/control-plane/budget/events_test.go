package budget

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// producer de teste (NHI do admission control com cadeia de delegação mínima).
func testProducer() eventstore.Producer {
	return eventstore.Producer{
		NHIID:           "nhi-orq",
		DelegationChain: []eventstore.DelegationHop{{Sub: "nhi-orq", ActAs: "human-root"}},
		Scope:           []string{"budget:admit"},
	}
}

// TestRebuild_FromEventStore: com um Budget ligado ao Event Store REAL (AOS-002),
// uma sequência de reserve/commit/release emite eventos; Rebuild a partir desses
// eventos reproduz EXACTAMENTE os contadores in-memory (Reserved/Committed) de
// cada nó. É a prova de reconstrução do estado (consistência com AOS-002).
func TestRebuild_FromEventStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	b, err := New("tree-42", amt(1000, 1000), WithEmitter(NewEventStoreEmitter(store, testProducer())))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := b.AddNode("child-a", "tree-42", amt(500, 500)); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if err := b.AddNode("child-b", "tree-42", amt(500, 500)); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	// Movimentos variados que exercitam reserved/committed/released e a hierarquia.
	r1, err := b.Reserve(ctx, "child-a", amt(100, 40))
	if err != nil {
		t.Fatalf("Reserve r1: %v", err)
	}
	if err := b.Commit(ctx, r1); err != nil { // reserved→committed em child-a e raiz
		t.Fatalf("Commit r1: %v", err)
	}
	// r2 fica PENDENTE (reservado, não confirmado) — não é settled.
	if _, err := b.Reserve(ctx, "child-b", amt(70, 30)); err != nil {
		t.Fatalf("Reserve r2: %v", err)
	}
	r3, err := b.Reserve(ctx, "child-a", amt(50, 10))
	if err != nil {
		t.Fatalf("Reserve r3: %v", err)
	}
	if err := b.Release(ctx, r3); err != nil { // rollback, volta a Available
		t.Fatalf("Release r3: %v", err)
	}

	// Lê TODOS os eventos do stream da árvore e reconstrói.
	events, err := store.Read(ctx, "tree-42", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Esperamos 4 eventos: reserved(r1), committed(r1), reserved(r2), reserved(r3), released(r3) = 5.
	if len(events) != 5 {
		t.Fatalf("esperava 5 eventos de orcamento, obtive %d", len(events))
	}

	rebuilt, err := Rebuild(events)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// Compara Reserved/Committed nó-a-nó com o estado in-memory.
	live := b.Snapshot()
	for _, id := range []string{"tree-42", "child-a", "child-b"} {
		if rebuilt[id].Reserved != live[id].Reserved {
			t.Errorf("no %s: Reserved reconstruido %v != in-memory %v", id, rebuilt[id].Reserved, live[id].Reserved)
		}
		if rebuilt[id].Committed != live[id].Committed {
			t.Errorf("no %s: Committed reconstruido %v != in-memory %v", id, rebuilt[id].Committed, live[id].Committed)
		}
	}

	// Valores esperados explícitos (âncora de sanidade):
	//  child-a: committed {100,40}, reserved 0
	//  child-b: reserved {70,30} (r2 pendente)
	//  raiz:    committed {100,40} + reserved {70,30}
	want := map[string]NodeState{
		"child-a": {Committed: amt(100, 40)},
		"child-b": {Reserved: amt(70, 30)},
		"tree-42": {Committed: amt(100, 40), Reserved: amt(70, 30)},
	}
	for id, w := range want {
		got := NodeState{Reserved: rebuilt[id].Reserved, Committed: rebuilt[id].Committed}
		if !reflect.DeepEqual(got, w) {
			t.Errorf("no %s reconstruido = %+v, quero %+v", id, got, w)
		}
	}
}

// TestRebuild_IgnoresForeignEvents: eventos de outros tipos no mesmo stream são
// ignorados pela reconstrução (robustez do parser).
func TestRebuild_IgnoresForeignEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Evento alheio.
	if _, err := store.Append(ctx, "tree-9", eventstore.EventInput{
		Type: "tool.call.mediated", Payload: []byte(`{"foo":1}`), RunID: "tree-9", StepID: "s1",
	}); err != nil {
		t.Fatalf("Append alheio: %v", err)
	}

	b, _ := New("tree-9", amt(100, 100), WithEmitter(NewEventStoreEmitter(store, testProducer())))
	r, _ := b.Reserve(ctx, "tree-9", amt(10, 5))
	_ = b.Commit(ctx, r)

	events, _ := store.Read(ctx, "tree-9", 1)
	rebuilt, err := Rebuild(events)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if rebuilt["tree-9"].Committed != amt(10, 5) {
		t.Errorf("committed reconstruido = %v, quero {10,5} (evento alheio ignorado)", rebuilt["tree-9"].Committed)
	}
}

// failingEmitter falha sempre — para provar o fail-closed do Reserve.
type failingEmitter struct{}

func (failingEmitter) Emit(context.Context, string, string, string, eventPayload) error {
	return context.DeadlineExceeded
}

// TestReserve_FailClosedOnEmitError: se o log durável falhar, a reserva é
// revertida por inteiro (não se concede headroom que não se consegue registar).
func TestReserve_FailClosedOnEmitError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, _ := New("tree-x", amt(100, 100), WithEmitter(failingEmitter{}))

	_, err := b.Reserve(ctx, "tree-x", amt(10, 10))
	if err == nil {
		t.Fatal("Reserve devia falhar quando o emit falha")
	}
	// Sem débito residual e sem reserva registada (rollback + limpeza).
	if got := b.Snapshot()["tree-x"].Reserved; got != (Amount{}) {
		t.Errorf("reservado residual %v apos emit falhado", got)
	}
	b.mu.RLock()
	nRes := len(b.res)
	b.mu.RUnlock()
	if nRes != 0 {
		t.Errorf("reservas registadas = %d, quero 0 (limpeza apos emit falhado)", nRes)
	}
}

// failOnceEmitter falha o PRIMEIRO emit do tipo-alvo e passa a partir daí. Serve
// para provar que Commit/Release são fail-closed no emit (sem divergir do log) e
// que um RETRY após a falha consegue efectivar a operação (re-emit).
type failOnceEmitter struct {
	target string
	failed atomic.Bool
}

func (e *failOnceEmitter) Emit(_ context.Context, _, evType, _ string, _ eventPayload) error {
	if evType == e.target && e.failed.CompareAndSwap(false, true) {
		return context.DeadlineExceeded
	}
	return nil
}

// TestCommit_FailClosedOnEmitError: se o emit do committed falhar, o Commit NÃO
// confirma a mutação — o estado permanece reserved/pending (a coincidir com o log
// durável, que só tem o reserved), preservando Rebuild==in-memory. Um retry após
// a recuperação do log efectiva a confirmação. Cobre o simétrico do Reserve para
// Commit (findings Q-1 / AOS008-DOD).
func TestCommit_FailClosedOnEmitError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	em := &failOnceEmitter{target: EventCommitted}
	b, _ := New("tree-c", amt(100, 100), WithEmitter(em))

	r, err := b.Reserve(ctx, "tree-c", amt(10, 10)) // emite reserved (passa)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// 1º Commit: o emit committed falha → erro, e o estado NÃO pode divergir.
	if err := b.Commit(ctx, r); err == nil {
		t.Fatal("Commit devia falhar quando o emit falha")
	}
	if s := b.Snapshot()["tree-c"]; s.Reserved != amt(10, 10) || s.Committed != (Amount{}) {
		t.Fatalf("divergencia apos emit falhado: {res:%v com:%v}, quero {res:{10,10} com:0}", s.Reserved, s.Committed)
	}
	// Retry: o emit já passa → a confirmação efectiva-se (contadores movem).
	if err := b.Commit(ctx, r); err != nil {
		t.Fatalf("retry do Commit: %v", err)
	}
	if s := b.Snapshot()["tree-c"]; s.Reserved != (Amount{}) || s.Committed != amt(10, 10) {
		t.Fatalf("apos retry: {res:%v com:%v}, quero {res:0 com:{10,10}}", s.Reserved, s.Committed)
	}
}

// TestRelease_FailClosedOnEmitError: se o emit do released falhar, o Release NÃO
// liberta o headroom — permanece reserved/pending (a coincidir com o log, que
// mantém a reserva). Um retry após a recuperação liberta de facto. Simétrico do
// Reserve para Release (findings Q-1 / AOS008-DOD).
func TestRelease_FailClosedOnEmitError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	em := &failOnceEmitter{target: EventReleased}
	b, _ := New("tree-r", amt(100, 100), WithEmitter(em))

	r, err := b.Reserve(ctx, "tree-r", amt(10, 10))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// 1º Release: o emit released falha → erro, headroom permanece reservado.
	if err := b.Release(ctx, r); err == nil {
		t.Fatal("Release devia falhar quando o emit falha")
	}
	if s := b.Snapshot()["tree-r"]; s.Reserved != amt(10, 10) || s.Committed != (Amount{}) {
		t.Fatalf("divergencia apos emit falhado: {res:%v com:%v}, quero {res:{10,10} com:0}", s.Reserved, s.Committed)
	}
	// Retry: o emit já passa → a libertação efectiva-se (headroom recuperado).
	if err := b.Release(ctx, r); err != nil {
		t.Fatalf("retry do Release: %v", err)
	}
	if s := b.Snapshot()["tree-r"]; s.Reserved != (Amount{}) || s.Committed != (Amount{}) {
		t.Fatalf("apos retry: {res:%v com:%v}, quero tudo a zero", s.Reserved, s.Committed)
	}
	// E a capacidade libertada volta a estar disponível.
	if _, err := b.Reserve(ctx, "tree-r", amt(100, 100)); err != nil {
		t.Fatalf("apos release a capacidade total devia estar livre: %v", err)
	}
}

// TestRebuild_RejectsCorruptEvents: Rebuild é fail-closed perante entrada
// corrompida/adversarial do Event Store — amount com dimensão negativa, ou soma
// que transborda int64 — devolvendo [ErrCorruptEvent] em vez de contadores
// errados/negativos (finding Q-3).
func TestRebuild_RejectsCorruptEvents(t *testing.T) {
	t.Parallel()

	mkEvent := func(evType string, a Amount) eventstore.Event {
		raw, _ := json.Marshal(eventPayload{
			TreeID: "t", ReservationID: "r1", NodeID: "t", Nodes: []string{"t"}, Amount: a,
		})
		return eventstore.Event{Type: evType, Payload: raw}
	}

	t.Run("amount negativo", func(t *testing.T) {
		t.Parallel()
		_, err := Rebuild([]eventstore.Event{mkEvent(EventReserved, amt(-5, 10))})
		if !errors.Is(err, ErrCorruptEvent) {
			t.Fatalf("Rebuild com amount negativo = %v, quero ErrCorruptEvent", err)
		}
	})

	t.Run("overflow na acumulacao", func(t *testing.T) {
		t.Parallel()
		big := amt(math.MaxInt64, 1)
		// Duas reservas de MaxInt64 no mesmo nó transbordam ao acumular.
		_, err := Rebuild([]eventstore.Event{mkEvent(EventReserved, big), mkEvent(EventReserved, big)})
		if !errors.Is(err, ErrCorruptEvent) {
			t.Fatalf("Rebuild com overflow = %v, quero ErrCorruptEvent", err)
		}
	})

	t.Run("evento valido reconstroi", func(t *testing.T) {
		t.Parallel()
		out, err := Rebuild([]eventstore.Event{mkEvent(EventReserved, amt(10, 5))})
		if err != nil {
			t.Fatalf("Rebuild valido: %v", err)
		}
		if out["t"].Reserved != amt(10, 5) {
			t.Errorf("reservado = %v, quero {10,5}", out["t"].Reserved)
		}
	})
}
