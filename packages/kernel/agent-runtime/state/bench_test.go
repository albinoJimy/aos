package state

import (
	"context"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// BenchmarkIsValidTransition mede o caminho quente da validação declarativa (lookup
// em mapa) — deve ser barato e sem alocações.
func BenchmarkIsValidTransition(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = IsValidTransition(Running, Complete)
		_ = IsValidTransition(Complete, Running) // inválido
	}
}

// BenchmarkTransition mede uma transição durável ponta-a-ponta (validação + Append
// replicado + avanço de estado).
func BenchmarkTransition(b *testing.B) {
	ctx := context.Background()
	st, err := eventstore.New()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		m, _ := NewMachine(st, "bench-run-"+itoa(i))
		b.StartTimer()
		if err := m.Transition(ctx, Running, TransitionEvent{Token: Uint64Token(1)}); err != nil {
			b.Fatal(err)
		}
		_ = m.Transition(ctx, Complete, TransitionEvent{})
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// TestOptionsAccessorsAndTracer cobre os acessores triviais, as opções de produtor/
// tracer/observer, o ClockFunc, e VERIFICA que cada transição emite um span
// observável (DoD: transições observáveis via spans/estado).
func TestOptionsAccessorsAndTracer(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	tr := &agentruntime.RecordingTracer{}
	obs := &countingObserver{}
	fixed := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	m, err := NewMachine(st, "run-opts",
		WithClock(ClockFunc(func() time.Time { return fixed })),
		WithProducer(eventstore.Producer{NHIID: "nhi:agent"}),
		WithTracer(tr),
		WithObserver(obs),
	)
	if err != nil {
		t.Fatal(err)
	}
	if m.RunID() != "run-opts" {
		t.Fatalf("RunID=%q", m.RunID())
	}

	if err := m.Transition(ctx, Running, TransitionEvent{Token: Uint64Token(7), Reason: "claim"}); err != nil {
		t.Fatal(err)
	}
	if !m.EnteredAt().Equal(fixed) {
		t.Fatalf("EnteredAt=%v; quero %v (relógio injectado)", m.EnteredAt(), fixed)
	}
	if err := m.Transition(ctx, Complete, TransitionEvent{}); err != nil {
		t.Fatal(err)
	}

	// Dois spans, com from/to correctos.
	spans := tr.SpansByOperation(agentruntime.OpInvokeAgent)
	if len(spans) != 2 {
		t.Fatalf("emitiu %d spans; quero 2", len(spans))
	}
	if spans[0].Attributes["aos.state.to"] != string(Running) {
		t.Errorf("span[0].to=%v; quero running", spans[0].Attributes["aos.state.to"])
	}
	if spans[0].Attributes[agentruntime.AttrRunID] != "run-opts" {
		t.Errorf("span[0].run_id=%v", spans[0].Attributes[agentruntime.AttrRunID])
	}
	if !spans[1].Ended {
		t.Error("span[1] devia estar fechado")
	}
	if obs.transitioned != 2 {
		t.Fatalf("observou %d; quero 2", obs.transitioned)
	}

	// Os defaults Nop não fazem nada (cobertura das implementações no-op).
	NopTransitionObserver{}.Transitioned(Ready, Running, "x")
	NopTransitionObserver{}.Rejected(Ready, Running, ErrInvalidTransition)

	// O token válido foi persistido como valor monotónico (contrato AOS-018).
	events, err := st.Read(ctx, "run-opts", 1)
	if err != nil {
		t.Fatal(err)
	}
	var rec transitionRecord
	if err := unmarshalJSON(events[0].Payload, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.TokenValue != 7 {
		t.Fatalf("token_value persistido=%d; quero 7", rec.TokenValue)
	}
}

// TestSystemClockDefault cobre o relógio de sistema por omissão.
func TestSystemClockDefault(t *testing.T) {
	st := newStore(t)
	m := mustMachine(t, st, "run-sysclock")
	if m.EnteredAt().IsZero() {
		t.Fatal("systemClock devia ter dado um EnteredAt não-zero")
	}
	if got := (systemClock{}).Now(); got.IsZero() {
		t.Fatal("systemClock.Now() devolveu zero")
	}
}
