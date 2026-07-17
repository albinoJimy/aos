package breaker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ---------------------------------------------------------------------------
// Auxiliares de teste
// ---------------------------------------------------------------------------

// manualClock é um relógio determinístico partilhado pela máquina e pelo breaker.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock {
	return &manualClock{t: time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// captureSink recolhe os alertas emitidos (para asserir que o trip alerta uma vez).
type captureSink struct {
	mu     sync.Mutex
	alerts []Alert
}

func (s *captureSink) Alert(_ context.Context, a Alert) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts = append(s.alerts, a)
}

func (s *captureSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.alerts)
}

func (s *captureSink) last() Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alerts[len(s.alerts)-1]
}

// failingStore embrulha o Event Store e falha o PRÓXIMO Append (para provar fail-closed).
type failingStore struct {
	*eventstore.Store
	failNext bool
	failErr  error
}

func (s *failingStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if s.failNext {
		s.failNext = false
		return eventstore.AppendResult{}, s.failErr
	}
	return s.Store.Append(ctx, streamID, in, opts...)
}

func newStore(t *testing.T) *eventstore.Store {
	t.Helper()
	st, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// runningMachine constrói uma máquina e leva-a a running (ready → running com token).
func runningMachine(t *testing.T, store state.EventStore, runID string, clk state.Clock) *state.Machine {
	t.Helper()
	m, err := state.NewMachine(store, runID, state.WithClock(clk))
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(context.Background(), state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("claim ready→running: %v", err)
	}
	return m
}

// costVelocity fabrica um CostVelocity com uma dada cost velocity (micro-USD/s) e token
// velocity (tokens/s), via uma janela de 1s.
func costVelocity(microUSDPerSec, tokensPerSec int64) otelgenai.CostVelocity {
	return otelgenai.CostVelocity{
		Totals:    otelgenai.UsageTotals{CostMicroUSD: microUSDPerSec, InputTokens: tokensPerSec},
		Turns:     1,
		WallClock: time.Second,
	}
}

func tripSpans(tr *otelgenai.RecordingTracer) []*otelgenai.RecordedSpan {
	return tr.SpansByOperation(OpBreakerTrip)
}

// ---------------------------------------------------------------------------
// Construção
// ---------------------------------------------------------------------------

func TestNewBreaker_NilMachine(t *testing.T) {
	if _, err := NewBreaker(nil, nil, "class"); !errors.Is(err, ErrNilMachine) {
		t.Fatalf("máquina nil: err=%v; quero ErrNilMachine", err)
	}
}

// TestNewBreaker_EnabledSignalWithoutSource: fail-closed de cablagem. Um limiar de
// velocity/no-progress ligado (>0) SEM a fonte respectiva cablada recusa na construção —
// senão o sinal ficaria silenciosamente inerte (o falso-negativo catastrófico em
// CompositionAll). O wall-clock não precisa de fonte (deriva da máquina).
func TestNewBreaker_EnabledSignalWithoutSource(t *testing.T) {
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-wiring", clk)

	cases := []struct {
		name    string
		th      Thresholds
		opts    []Option
		wantErr error
	}{
		{
			name:    "cost_velocity_ligado_sem_fonte",
			th:      Thresholds{MaxCostMicroUSDPerSecond: 500_000},
			wantErr: ErrVelocitySourceMissing,
		},
		{
			name:    "token_velocity_ligado_sem_fonte",
			th:      Thresholds{MaxTokensPerSecond: 1000},
			wantErr: ErrVelocitySourceMissing,
		},
		{
			name:    "no_progress_ligado_sem_fonte",
			th:      Thresholds{MaxStaleIterations: 3},
			wantErr: ErrProgressSourceMissing,
		},
		{
			// Catastrófico visado pelo achado: MaxStaleIterations>0 + CompositionAll sem
			// ProgressSource → breaker morto. A construção recusa.
			name:    "compositionAll_no_progress_sem_fonte",
			th:      Thresholds{MaxCostMicroUSDPerSecond: 1, MaxStaleIterations: 3, Composition: CompositionAll},
			opts:    []Option{WithVelocitySource(VelocityFunc(func() otelgenai.CostVelocity { return costVelocity(0, 0) }))},
			wantErr: ErrProgressSourceMissing,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewBreaker(m, NewStaticThresholdProvider(c.th), "c", c.opts...)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err=%v; quero %v", err, c.wantErr)
			}
		})
	}
}

// TestNewBreaker_EnabledSignalWithSource_OK: o mesmo limiar ligado, agora COM a fonte
// cablada, constrói sem erro (a validação fail-closed não é um falso-positivo).
func TestNewBreaker_EnabledSignalWithSource_OK(t *testing.T) {
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-wiring-ok", clk)

	// Velocity + no-progress ligados, ambas as fontes cabladas, e wall-clock (sem fonte,
	// derivado da máquina). Não deve recusar.
	th := Thresholds{MaxCostMicroUSDPerSecond: 1, MaxTokensPerSecond: 1, MaxStaleIterations: 1, MaxWallClock: time.Minute}
	if _, err := NewBreaker(m, NewStaticThresholdProvider(th), "c",
		WithVelocitySource(VelocityFunc(func() otelgenai.CostVelocity { return costVelocity(0, 0) })),
		WithProgressSource(ProgressFunc(func() bool { return true })),
	); err != nil {
		t.Fatalf("construção com fontes cabladas não devia recusar: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Teste requerido: loop de custo → trip por cost velocity → paused
// ---------------------------------------------------------------------------

func TestBreaker_CostLoop_TripsToPaused(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-cost", clk)

	tr := otelgenai.NewRecordingTracer(nil)
	sink := &captureSink{}
	prov := NewStaticThresholdProvider(Thresholds{MaxCostMicroUSDPerSecond: 500_000})

	b, err := NewBreaker(m, prov, "greedy",
		WithVelocitySource(VelocityFunc(func() otelgenai.CostVelocity {
			return costVelocity(1_000_000, 0) // 1e6 micro-USD/s > 5e5 limiar
		})),
		WithTracer(tr),
		WithAlertSink(sink),
	)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}

	dec, err := b.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !dec.Trip || dec.Reason != SignalCostVelocity || dec.Target != state.Paused {
		t.Fatalf("decisão=%+v; quero trip cost_velocity → paused", dec)
	}
	if m.Current() != state.Paused {
		t.Fatalf("estado=%q; quero paused", m.Current())
	}
	if got := tripSpans(tr); len(got) != 1 || !got[0].Ended {
		t.Fatalf("span de trip: got=%d spans (ended=%v); quero 1 fechado", len(got), len(got) == 1 && got[0].Ended)
	}
	if got := tripSpans(tr)[0].Attributes[attrBreakerSignal]; got != string(SignalCostVelocity) {
		t.Errorf("atributo signal=%v; quero %q", got, SignalCostVelocity)
	}
	if sink.count() != 1 {
		t.Fatalf("alertas=%d; quero exactamente 1", sink.count())
	}
	if a := sink.last(); a.Kind != AlertTrip || a.Target != state.Paused {
		t.Errorf("alerta=%+v; quero trip → paused", a)
	}
}

// ---------------------------------------------------------------------------
// Teste requerido: excede wall-clock → timed_out
// ---------------------------------------------------------------------------

func TestBreaker_WallClock_TripsToTimedOut(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-wall", clk)

	tr := otelgenai.NewRecordingTracer(nil)
	prov := NewStaticThresholdProvider(Thresholds{MaxWallClock: 5 * time.Minute})
	b, err := NewBreaker(m, prov, "slow", WithTracer(tr))
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}

	// Ainda dentro do wall-clock: sem trip.
	clk.Advance(4 * time.Minute)
	if dec, err := b.Observe(ctx); err != nil || dec.Trip {
		t.Fatalf("dentro do wall-clock: dec=%+v err=%v; não devia disparar", dec, err)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado=%q; quero running (ainda)", m.Current())
	}

	// Excede o wall-clock: trip → timed_out.
	clk.Advance(2 * time.Minute) // total 6m >= 5m
	dec, err := b.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !dec.Trip || dec.Reason != SignalWallClock || dec.Target != state.TimedOut {
		t.Fatalf("decisão=%+v; quero trip wall_clock → timed_out", dec)
	}
	if m.Current() != state.TimedOut {
		t.Fatalf("estado=%q; quero timed_out", m.Current())
	}
	if got := tripSpans(tr); len(got) != 1 {
		t.Fatalf("spans de trip=%d; quero 1", len(got))
	}
}

// ---------------------------------------------------------------------------
// Teste requerido: após trip, trajectória íntegra + span de trip presente
// ---------------------------------------------------------------------------

func TestBreaker_TrajectoryIntact_And_SpanPresent(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-rca", clk)

	tr := otelgenai.NewRecordingTracer(nil)
	prov := NewStaticThresholdProvider(Thresholds{MaxCostMicroUSDPerSecond: 100_000})
	b, err := NewBreaker(m, prov, "c",
		WithVelocitySource(VelocityFunc(func() otelgenai.CostVelocity { return costVelocity(1_000_000, 0) })),
		WithTracer(tr),
	)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	if _, err := b.Observe(ctx); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if m.Current() != state.Paused {
		t.Fatalf("estado=%q; quero paused", m.Current())
	}

	// Trajectória PRESERVADA: o event log append-only mantém as duas transições intactas
	// (ready→running, running→paused), por ordem. Nada foi destruído.
	evs, err := st.Read(ctx, "run-rca", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var transitions []struct{ From, To string }
	for _, ev := range evs {
		if ev.Type != state.EventTypeTransition {
			continue
		}
		var rec struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			t.Fatalf("unmarshal transição: %v", err)
		}
		transitions = append(transitions, struct{ From, To string }{rec.From, rec.To})
	}
	if len(transitions) != 2 {
		t.Fatalf("transições no log=%d (%+v); quero 2 (a trajectória não pode ser destruída)", len(transitions), transitions)
	}
	if transitions[0] != (struct{ From, To string }{"ready", "running"}) {
		t.Errorf("1ª transição=%+v; quero ready→running", transitions[0])
	}
	if transitions[1] != (struct{ From, To string }{"running", "paused"}) {
		t.Errorf("2ª transição=%+v; quero running→paused", transitions[1])
	}

	// A máquina reconstrói-se do log íntegro (prova de que a trajectória serve RCA).
	m2, err := state.NewMachine(st, "run-rca", state.WithClock(clk))
	if err != nil {
		t.Fatalf("NewMachine (rebuild): %v", err)
	}
	if got, err := m2.Rebuild(ctx); err != nil || got != state.Paused {
		t.Fatalf("Rebuild=%q err=%v; quero paused sem erro (cadeia íntegra)", got, err)
	}

	// Span de trip PRESENTE.
	if got := tripSpans(tr); len(got) != 1 || !got[0].Ended {
		t.Fatalf("span de trip ausente/aberto: %d spans", len(got))
	}
}

// ---------------------------------------------------------------------------
// Teste requerido/bónus: ausência de progresso → trip
// ---------------------------------------------------------------------------

func TestBreaker_NoProgress_TripsToPaused(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-stuck", clk)

	prov := NewStaticThresholdProvider(Thresholds{MaxStaleIterations: 3})
	// Porta de progresso: NUNCA faz progresso (o agente preso a repetir a mesma acção).
	b, err := NewBreaker(m, prov, "looper",
		WithProgressSource(ProgressFunc(func() bool { return false })),
	)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}

	// Duas iterações estéreis: ainda abaixo do limiar (3), sem trip.
	for i := 0; i < 2; i++ {
		if dec, err := b.Observe(ctx); err != nil || dec.Trip {
			t.Fatalf("iteração %d: dec=%+v err=%v; não devia disparar ainda", i, dec, err)
		}
	}
	if m.Current() != state.Running {
		t.Fatalf("estado=%q; quero running (ainda)", m.Current())
	}
	// 3ª iteração estéril: cruza o limiar → trip por no-progress → paused.
	dec, err := b.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !dec.Trip || dec.Reason != SignalNoProgress || dec.Target != state.Paused {
		t.Fatalf("decisão=%+v; quero trip no_progress → paused", dec)
	}
	if m.Current() != state.Paused {
		t.Fatalf("estado=%q; quero paused", m.Current())
	}
}

// TestBreaker_ProgressResetsCounter: uma iteração com progresso reinicia o contador,
// evitando o trip (prova de que a porta plugável liga/desliga o sinal por iteração).
func TestBreaker_ProgressResetsCounter(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-progress", clk)

	progress := true
	prov := NewStaticThresholdProvider(Thresholds{MaxStaleIterations: 2})
	b, _ := NewBreaker(m, prov, "c", WithProgressSource(ProgressFunc(func() bool { return progress })))

	// Estéril, estéril (contador=2 cruzaria)… mas intercalamos progresso.
	progress = false
	if dec, _ := b.Observe(ctx); dec.Trip {
		t.Fatal("1ª estéril não devia disparar")
	}
	progress = true // progresso reinicia o contador
	if dec, _ := b.Observe(ctx); dec.Trip {
		t.Fatal("progresso não devia disparar")
	}
	progress = false
	if dec, _ := b.Observe(ctx); dec.Trip {
		t.Fatal("após reset, 1 estéril não devia cruzar o limiar 2")
	}
	if m.Current() != state.Running {
		t.Fatalf("estado=%q; quero running", m.Current())
	}
}

// ---------------------------------------------------------------------------
// Composição de sinais
// ---------------------------------------------------------------------------

func TestBreaker_CompositionAll(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-comp", clk)

	prov := NewStaticThresholdProvider(Thresholds{
		MaxCostMicroUSDPerSecond: 500_000,
		MaxWallClock:             5 * time.Minute,
		Composition:              CompositionAll,
	})
	b, _ := NewBreaker(m, prov, "c",
		WithVelocitySource(VelocityFunc(func() otelgenai.CostVelocity { return costVelocity(1_000_000, 0) })),
	)

	// Só o custo cruza (wall ainda baixo): CompositionAll não dispara.
	clk.Advance(time.Minute)
	if dec, err := b.Observe(ctx); err != nil || dec.Trip {
		t.Fatalf("all com só custo: dec=%+v err=%v; não devia disparar", dec, err)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado=%q; quero running", m.Current())
	}

	// Agora o wall-clock também cruza: dispara, alvo timed_out (precedência wall-clock).
	clk.Advance(5 * time.Minute)
	dec, err := b.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !dec.Trip || dec.Target != state.TimedOut {
		t.Fatalf("decisão=%+v; quero trip → timed_out", dec)
	}
	if m.Current() != state.TimedOut {
		t.Fatalf("estado=%q; quero timed_out", m.Current())
	}
}

// ---------------------------------------------------------------------------
// Idempotência
// ---------------------------------------------------------------------------

func TestBreaker_Idempotent_ReTrip(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-idem", clk)

	sink := &captureSink{}
	prov := NewStaticThresholdProvider(Thresholds{MaxCostMicroUSDPerSecond: 1})
	b, _ := NewBreaker(m, prov, "c",
		WithVelocitySource(VelocityFunc(func() otelgenai.CostVelocity { return costVelocity(1_000_000, 0) })),
		WithAlertSink(sink),
	)

	// Primeiro trip.
	if _, err := b.Observe(ctx); err != nil {
		t.Fatalf("Observe 1: %v", err)
	}
	// Re-trip sem resume intermédio: no-op (o run já está paused, não running).
	dec, err := b.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe 2 (re-trip): %v", err)
	}
	if dec.Trip {
		// Re-trip é no-op: o run já parado não conta como trabalho activo, pelo que Observe
		// devolve uma decisão sem trip (a exclusão a montante evita a reavaliação).
		t.Errorf("re-trip devia ser no-op (sem trip); dec=%+v", dec)
	}
	if m.Current() != state.Paused {
		t.Fatalf("estado=%q; quero paused (inalterado)", m.Current())
	}
	if sink.count() != 1 {
		t.Fatalf("alertas=%d; quero 1 (re-trip não duplica)", sink.count())
	}

	// O log tem exactamente 2 transições (ready→running, running→paused) — sem duplicar.
	evs, _ := st.Read(ctx, "run-idem", 1)
	n := 0
	for _, ev := range evs {
		if ev.Type == state.EventTypeTransition {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("transições no log=%d; quero 2 (re-trip não apende)", n)
	}
}

// ---------------------------------------------------------------------------
// Fail-closed: falha da transição durável propaga o erro e não altera o estado
// ---------------------------------------------------------------------------

func TestBreaker_FailClosed_TransitionError(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	fs := &failingStore{Store: newStore(t)}
	m := runningMachine(t, fs, "run-fc", clk)

	sink := &captureSink{}
	prov := NewStaticThresholdProvider(Thresholds{MaxCostMicroUSDPerSecond: 1})
	b, _ := NewBreaker(m, prov, "c",
		WithVelocitySource(VelocityFunc(func() otelgenai.CostVelocity { return costVelocity(1_000_000, 0) })),
		WithAlertSink(sink),
	)

	// A próxima escrita (running→paused) falha.
	fs.failNext = true
	fs.failErr = errors.New("event store indisponível")

	dec, err := b.Observe(ctx)
	if err == nil {
		t.Fatalf("Observe devia propagar o erro da transição durável; dec=%+v", dec)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado=%q; quero running (transição falhada não corrompe)", m.Current())
	}
	if sink.count() != 0 {
		t.Fatalf("alertas=%d; quero 0 (sem transição durável, sem alerta)", sink.count())
	}
}

// ---------------------------------------------------------------------------
// Escalada a humano / abort gracioso
// ---------------------------------------------------------------------------

func TestBreaker_EscalateToHuman(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-esc", clk)
	sink := &captureSink{}
	b, _ := NewBreaker(m, NewStaticThresholdProvider(Thresholds{}), "c", WithAlertSink(sink))

	if err := b.EscalateToHuman(ctx, "revisão do operador"); err != nil {
		t.Fatalf("EscalateToHuman: %v", err)
	}
	if m.Current() != state.WaitingOnHuman {
		t.Fatalf("estado=%q; quero waiting_on_human", m.Current())
	}
	if a := sink.last(); a.Kind != AlertEscalate || a.Target != state.WaitingOnHuman {
		t.Errorf("alerta=%+v; quero escalate → waiting_on_human", a)
	}
	// Idempotente: já em waiting_on_human é no-op.
	if err := b.EscalateToHuman(ctx, "de novo"); err != nil {
		t.Fatalf("EscalateToHuman idempotente: %v", err)
	}
	if sink.count() != 1 {
		t.Errorf("alertas=%d; quero 1 (idempotente)", sink.count())
	}
}

func TestBreaker_Abort_Graceful(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-abort", clk)
	b, _ := NewBreaker(m, NewStaticThresholdProvider(Thresholds{}), "c")

	if err := b.Abort(ctx, "trabalho inútil"); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if m.Current() != state.Failed {
		t.Fatalf("estado=%q; quero failed (abort gracioso → saga)", m.Current())
	}
}

func TestBreaker_Escalate_NotRunning(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	// Máquina em ready (não running).
	m, _ := state.NewMachine(st, "run-nr", state.WithClock(clk))
	b, _ := NewBreaker(m, NewStaticThresholdProvider(Thresholds{}), "c")

	if err := b.EscalateToHuman(ctx, "x"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("escalar em ready: err=%v; quero ErrNotRunning", err)
	}
	if err := b.Abort(ctx, "x"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("abortar em ready: err=%v; quero ErrNotRunning", err)
	}
}

// TestBreaker_Accessors_And_Adapters cobre getters e adaptadores triviais (superfície
// pública), incluindo a fonte wall-clock injectada e os sinks default/func.
func TestBreaker_Accessors_And_Adapters(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-acc", clk)

	th := Thresholds{MaxWallClock: time.Minute}
	// Fonte wall-clock injectada (WallClockFunc via WithWallClockSource) + AlertFunc.
	var alerted int
	b, err := NewBreaker(m, NewStaticThresholdProvider(th), "acc",
		WithWallClockSource(WallClockFunc(func() time.Duration { return 2 * time.Minute })),
		WithAlertSink(AlertFunc(func(context.Context, Alert) { alerted++ })),
	)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	if b.Class() != "acc" {
		t.Errorf("Class()=%q; quero acc", b.Class())
	}
	if b.Thresholds().MaxWallClock != time.Minute {
		t.Errorf("Thresholds()=%+v; quero MaxWallClock 1m", b.Thresholds())
	}
	if snap := b.Snapshot(); snap.Wall != 2*time.Minute {
		t.Errorf("Snapshot().Wall=%v; quero 2m (fonte injectada)", snap.Wall)
	}

	dec, err := b.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !dec.Trip || dec.Target != state.TimedOut {
		t.Fatalf("dec=%+v; quero trip → timed_out", dec)
	}
	if alerted != 1 {
		t.Errorf("AlertFunc chamado %d vezes; quero 1", alerted)
	}

	// NopAlertSink não entra em pânico.
	NopAlertSink{}.Alert(ctx, Alert{})
}

// TestNewBreaker_NilProviderInert: provider nil ⇒ limiares zero ⇒ breaker inerte (nunca
// dispara), e o wall-clock default deriva da máquina.
func TestNewBreaker_NilProviderInert(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-inert", clk)
	b, err := NewBreaker(m, nil, "c")
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	clk.Advance(100 * time.Hour)
	if dec, err := b.Observe(ctx); err != nil || dec.Trip {
		t.Fatalf("breaker inerte: dec=%+v err=%v; nunca devia disparar", dec, err)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado=%q; quero running", m.Current())
	}
}

// TestBreaker_ExcludesWaitingTime: chamado com o run FORA de running (espera legítima),
// Observe é no-op — os sinais wall-clock/no-progress não acumulam tempo de espera
// (exclusão AOS-019 / tecnica/08 §6). Preserva o contador de estéreis através da espera.
func TestBreaker_ExcludesWaitingTime(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-excl", clk)

	prov := NewStaticThresholdProvider(Thresholds{MaxStaleIterations: 2, MaxWallClock: time.Minute})
	stuck := false
	b, _ := NewBreaker(m, prov, "c", WithProgressSource(ProgressFunc(func() bool { return !stuck })))

	// Uma iteração estéril em running: contador=1 (abaixo do limiar 2).
	stuck = true
	if dec, _ := b.Observe(ctx); dec.Trip {
		t.Fatal("1 estéril não devia disparar")
	}

	// O run entra em espera legítima (waiting_on_tool). Enquanto lá, muitas "iterações" e
	// muito tempo de parede NÃO devem acumular nem disparar (exclusão).
	if err := m.Transition(ctx, state.WaitingOnTool, state.TransitionEvent{}); err != nil {
		t.Fatalf("running→waiting_on_tool: %v", err)
	}
	clk.Advance(time.Hour) // tempo de espera longo
	for i := 0; i < 10; i++ {
		if dec, err := b.Observe(ctx); err != nil || dec.Trip {
			t.Fatalf("Observe em espera devia ser no-op; dec=%+v err=%v", dec, err)
		}
	}
	if m.Current() != state.WaitingOnTool {
		t.Fatalf("estado=%q; quero waiting_on_tool (inalterado)", m.Current())
	}

	// Retoma: o contador de estéreis preservou-se (1). Mais UMA iteração estéril cruza o
	// limiar 2 → trip por no-progress.
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{}); err != nil {
		t.Fatalf("waiting_on_tool→running: %v", err)
	}
	dec, err := b.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !dec.Trip || dec.Reason != SignalNoProgress {
		t.Fatalf("dec=%+v; quero trip no_progress (contador preservado através da espera)", dec)
	}
	if m.Current() != state.Paused {
		t.Fatalf("estado=%q; quero paused", m.Current())
	}
}
