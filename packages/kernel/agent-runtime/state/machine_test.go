package state

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

func unmarshalJSON(data []byte, v any) error { return json.Unmarshal(data, v) }

// ---------------------------------------------------------------------------
// Auxiliares de teste: relógio manual e stores instrumentados.
// ---------------------------------------------------------------------------

// manualClock é um [Clock] determinístico: o tempo só avança quando o teste o move.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock {
	return &manualClock{t: time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)}
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

// failingStore embrulha um EventStore e falha o próximo Append com um erro dado,
// para provar que uma falha de persistência NÃO corrompe o estado.
type failingStore struct {
	EventStore
	failNext bool
	failErr  error
	appends  int
}

func (s *failingStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if s.failNext {
		s.failNext = false
		return eventstore.AppendResult{}, s.failErr
	}
	s.appends++
	return s.EventStore.Append(ctx, streamID, in, opts...)
}

// countingObserver conta transições confirmadas e rejeitadas.
type countingObserver struct {
	mu             sync.Mutex
	transitioned   int
	rejected       int
	lastFrom       State
	lastTo         State
	lastRejectErr  error
	transitionSeen []transition
}

func (o *countingObserver) Transitioned(from, to State, _ string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.transitioned++
	o.lastFrom, o.lastTo = from, to
	o.transitionSeen = append(o.transitionSeen, transition{from, to})
}

func (o *countingObserver) Rejected(from, to State, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rejected++
	o.lastRejectErr = err
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

func mustMachine(t *testing.T, store EventStore, runID string, opts ...Option) *Machine {
	t.Helper()
	m, err := NewMachine(store, runID, opts...)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	return m
}

// tok é um fencing token válido de conveniência.
var tok = TransitionEvent{Token: Uint64Token(1)}

// ---------------------------------------------------------------------------
// Construção e validação de argumentos.
// ---------------------------------------------------------------------------

func TestNewMachineValidation(t *testing.T) {
	st := newStore(t)
	if _, err := NewMachine(nil, "run"); !errors.Is(err, ErrNilStore) {
		t.Errorf("store nil: err=%v; quero ErrNilStore", err)
	}
	if _, err := NewMachine(st, ""); !errors.Is(err, ErrEmptyRunID) {
		t.Errorf("runID vazio: err=%v; quero ErrEmptyRunID", err)
	}
	m := mustMachine(t, st, "run")
	if m.Current() != Ready {
		t.Errorf("estado inicial=%q; quero ready", m.Current())
	}
}

// ---------------------------------------------------------------------------
// Fencing token na entrada em running (o claim).
// ---------------------------------------------------------------------------

func TestClaimRequiresFencingToken(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	m := mustMachine(t, st, "run-claim")

	// Sem token → recusado, estado intacto.
	if err := m.Transition(ctx, Running, TransitionEvent{}); !errors.Is(err, ErrMissingFencingToken) {
		t.Fatalf("claim sem token: err=%v; quero ErrMissingFencingToken", err)
	}
	if m.Current() != Ready {
		t.Fatalf("estado após claim recusado=%q; quero ready", m.Current())
	}

	// Token inválido (0) → recusado.
	if err := m.Transition(ctx, Running, TransitionEvent{Token: Uint64Token(0)}); !errors.Is(err, ErrMissingFencingToken) {
		t.Fatalf("claim token 0: err=%v; quero ErrMissingFencingToken", err)
	}
	if m.Current() != Ready {
		t.Fatalf("estado=%q; quero ready", m.Current())
	}

	// Nenhum evento deve ter sido persistido pelos claims recusados.
	if _, err := st.Read(ctx, "run-claim", 1); !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Fatalf("claims recusados não deviam persistir eventos; Read err=%v", err)
	}

	// Token válido → aceite.
	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatalf("claim com token válido: %v", err)
	}
	if m.Current() != Running {
		t.Fatalf("estado após claim=%q; quero running", m.Current())
	}
}

// stubFencingAuthority é uma [FencingAuthority] fixa para os testes de staleness: o
// token corrente é constante e a leitura pode ser instruída a falhar.
type stubFencingAuthority struct {
	current uint64
	err     error
}

func (s stubFencingAuthority) CurrentTokenValue(context.Context, string) (uint64, error) {
	return s.current, s.err
}

// TestClaimStalenessWithFencingAuthority: com uma FencingAuthority ligada, a PRESENÇA
// do token não basta — um token inferior ao corrente (worker superado) é recusado com
// ErrStaleFencingToken sem materializar a transição; o token corrente é aceite.
func TestClaimStalenessWithFencingAuthority(t *testing.T) {
	ctx := context.Background()

	// Corrente = 2: um claim com token 1 (obsoleto) é fenced-out.
	st := newStore(t)
	m := mustMachine(t, st, "run-stale", WithFencingAuthority(stubFencingAuthority{current: 2}))
	if err := m.Transition(ctx, Running, TransitionEvent{Token: Uint64Token(1)}); !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("claim obsoleto: err=%v; quero ErrStaleFencingToken", err)
	}
	if m.Current() != Ready {
		t.Fatalf("estado após claim obsoleto=%q; quero ready (transição não materializada)", m.Current())
	}
	if _, err := st.Read(ctx, "run-stale", 1); !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Fatalf("claim obsoleto não devia persistir eventos; Read err=%v", err)
	}

	// Token == corrente → aceite.
	if err := m.Transition(ctx, Running, TransitionEvent{Token: Uint64Token(2)}); err != nil {
		t.Fatalf("claim com token corrente: %v", err)
	}
	if m.Current() != Running {
		t.Fatalf("estado=%q; quero running", m.Current())
	}

	// Um erro da autoridade é fail-closed: a transição é recusada e propaga o erro.
	authErr := errors.New("autoridade indisponível")
	st2 := newStore(t)
	m2 := mustMachine(t, st2, "run-authfail", WithFencingAuthority(stubFencingAuthority{err: authErr}))
	if err := m2.Transition(ctx, Running, TransitionEvent{Token: Uint64Token(5)}); !errors.Is(err, authErr) {
		t.Fatalf("erro da autoridade: err=%v; quero %v", err, authErr)
	}
	if m2.Current() != Ready {
		t.Fatalf("estado após erro da autoridade=%q; quero ready", m2.Current())
	}
}

// TestResumeToRunningNoTokenNeeded confirma que as retomas de suspensão para running
// não re-exigem token.
func TestResumeToRunningNoTokenNeeded(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	m := mustMachine(t, st, "run-resume")

	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	// running → waiting_on_tool → running (sem token na retoma).
	if err := m.Transition(ctx, WaitingOnTool, TransitionEvent{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Transition(ctx, Running, TransitionEvent{}); err != nil {
		t.Fatalf("retoma waiting_on_tool→running sem token devia passar: %v", err)
	}
	// pause/resume sem token.
	if err := m.Pause(ctx, TransitionEvent{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Resume(ctx, TransitionEvent{}); err != nil {
		t.Fatalf("Resume paused→running sem token: %v", err)
	}
	if m.Current() != Running {
		t.Fatalf("estado=%q; quero running", m.Current())
	}
}

// ---------------------------------------------------------------------------
// Integração: sequência realista persistida e reconstruída.
// ---------------------------------------------------------------------------

func TestRealisticSequencePersistedAndRebuilt(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	obs := &countingObserver{}
	m := mustMachine(t, st, "run-seq", WithObserver(obs))

	// ready → running → waiting_on_tool → running → complete.
	steps := []struct {
		to    State
		event TransitionEvent
	}{
		{Running, tok},
		{WaitingOnTool, TransitionEvent{Reason: "http.get"}},
		{Running, TransitionEvent{}},
		{Complete, TransitionEvent{Reason: "final"}},
	}
	for i, s := range steps {
		if err := m.Transition(ctx, s.to, s.event); err != nil {
			t.Fatalf("passo %d (→%s): %v", i, s.to, err)
		}
	}
	if m.Current() != Complete {
		t.Fatalf("estado final=%q; quero complete", m.Current())
	}
	if obs.transitioned != 4 {
		t.Fatalf("observou %d transições; quero 4", obs.transitioned)
	}

	// Reconstrução por replay: nova máquina no MESMO store/run.
	m2 := mustMachine(t, st, "run-seq")
	got, err := m2.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if got != Complete || m2.Current() != Complete {
		t.Fatalf("estado reconstruído=%q; quero complete", got)
	}

	// O log tem exactamente 4 eventos de transição, por ordem.
	events, err := st.Read(ctx, "run-seq", 1)
	if err != nil {
		t.Fatal(err)
	}
	var got4 []transition
	for _, e := range events {
		if e.Type != EventTypeTransition {
			continue
		}
		got4 = append(got4, decodePair(t, e))
	}
	want4 := []transition{
		{Ready, Running}, {Running, WaitingOnTool}, {WaitingOnTool, Running}, {Running, Complete},
	}
	if len(got4) != len(want4) {
		t.Fatalf("persistiu %d transições; quero %d", len(got4), len(want4))
	}
	for i := range want4 {
		if got4[i] != want4[i] {
			t.Errorf("evento %d = %v; quero %v", i, got4[i], want4[i])
		}
	}
}

func decodePair(t *testing.T, e eventstore.Event) transition {
	t.Helper()
	var rec transitionRecord
	if err := unmarshalJSON(e.Payload, &rec); err != nil {
		t.Fatalf("descodifica payload: %v", err)
	}
	return transition{rec.From, rec.To}
}

// ---------------------------------------------------------------------------
// Fail-closed: waiting_on_human sem aprovação dentro do TTL → killed.
// ---------------------------------------------------------------------------

func TestHumanGateFailClosedToKilled(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	clk := newManualClock()
	m := mustMachine(t, st, "run-hitl",
		WithClock(clk),
		WithHumanApprovalTTL(30*time.Second),
	)

	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	if err := m.Transition(ctx, WaitingOnHuman, TransitionEvent{Reason: "danger_gate"}); err != nil {
		t.Fatal(err)
	}

	// Antes do TTL: CheckDeadlines não dispara.
	clk.Advance(29 * time.Second)
	if state, fired, err := m.CheckDeadlines(ctx); err != nil || fired || state != WaitingOnHuman {
		t.Fatalf("antes do TTL: state=%q fired=%v err=%v; quero waiting_on_human/false/nil", state, fired, err)
	}

	// Ao exceder o TTL: fail-closed → killed (NUNCA running).
	clk.Advance(2 * time.Second)
	state, fired, err := m.CheckDeadlines(ctx)
	if err != nil {
		t.Fatalf("CheckDeadlines: %v", err)
	}
	if !fired || state != Killed {
		t.Fatalf("no TTL: state=%q fired=%v; quero killed/true", state, fired)
	}
	if m.Current() != Killed {
		t.Fatalf("estado=%q; quero killed", m.Current())
	}

	// killed é terminal: idempotente e sem re-disparo.
	if state, fired, _ := m.CheckDeadlines(ctx); fired || state != Killed {
		t.Fatalf("após killed: state=%q fired=%v; quero killed/false", state, fired)
	}

	// A razão persistida é o fail-closed.
	if r := lastReason(t, st, "run-hitl"); r != ReasonHumanTimeout {
		t.Fatalf("razão persistida=%q; quero %q", r, ReasonHumanTimeout)
	}

	// Reconstrução confirma killed.
	m2 := mustMachine(t, st, "run-hitl", WithClock(clk))
	if got, _ := m2.Rebuild(ctx); got != Killed {
		t.Fatalf("reconstruído=%q; quero killed", got)
	}
}

// TestHumanGateApprovedBeforeTTL confirma o caminho feliz: aprovação antes do TTL
// retoma para running e o CheckDeadlines já não mata.
func TestHumanGateApprovedBeforeTTL(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	clk := newManualClock()
	m := mustMachine(t, st, "run-hitl-ok", WithClock(clk), WithHumanApprovalTTL(30*time.Second))

	mustSeq(t, ctx, m, []step{{Running, tok}, {WaitingOnHuman, TransitionEvent{}}})
	clk.Advance(10 * time.Second)
	if err := m.Transition(ctx, Running, TransitionEvent{Reason: "approval"}); err != nil {
		t.Fatalf("aprovação: %v", err)
	}
	// Mesmo avançando muito, running sem wall-clock configurado não morre.
	clk.Advance(time.Hour)
	if state, fired, _ := m.CheckDeadlines(ctx); fired || state != Running {
		t.Fatalf("running sem wall-clock não devia disparar: state=%q fired=%v", state, fired)
	}
}

// ---------------------------------------------------------------------------
// timed_out: running excede o wall-clock.
// ---------------------------------------------------------------------------

func TestRunningWallClockTimedOut(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	clk := newManualClock()
	m := mustMachine(t, st, "run-wc", WithClock(clk), WithRunWallClock(time.Minute))

	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	clk.Advance(59 * time.Second)
	if _, fired, _ := m.CheckDeadlines(ctx); fired {
		t.Fatal("não devia disparar antes do wall-clock")
	}
	clk.Advance(2 * time.Second)
	state, fired, err := m.CheckDeadlines(ctx)
	if err != nil || !fired || state != TimedOut {
		t.Fatalf("no wall-clock: state=%q fired=%v err=%v; quero timed_out/true", state, fired, err)
	}
	if r := lastReason(t, st, "run-wc"); r != ReasonWallClockTimeout {
		t.Fatalf("razão=%q; quero %q", r, ReasonWallClockTimeout)
	}
}

// ---------------------------------------------------------------------------
// Transição inválida NÃO corrompe o estado persistido.
// ---------------------------------------------------------------------------

func TestInvalidTransitionDoesNotCorrupt(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	obs := &countingObserver{}
	m := mustMachine(t, st, "run-inv", WithObserver(obs))

	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	// Par inválido: running → ready não existe.
	if err := m.Transition(ctx, Ready, TransitionEvent{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("running→ready: err=%v; quero ErrInvalidTransition", err)
	}
	if m.Current() != Running {
		t.Fatalf("estado após inválida=%q; quero running (intacto)", m.Current())
	}
	if obs.rejected != 1 {
		t.Fatalf("observou %d rejeições; quero 1", obs.rejected)
	}

	// O log só deve ter a transição VÁLIDA (ready→running); a inválida não persistiu.
	events, err := st.Read(ctx, "run-inv", 1)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Type == EventTypeTransition {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("persistiu %d transições; quero 1 (a inválida não devia deixar rasto)", n)
	}

	// Reconstrução confirma running (não corrompido).
	m2 := mustMachine(t, st, "run-inv")
	if got, _ := m2.Rebuild(ctx); got != Running {
		t.Fatalf("reconstruído=%q; quero running", got)
	}
}

// TestAppendFailureDoesNotCorrupt prova que uma FALHA do Event Store (fail-closed)
// deixa o estado — in-memory e persistido — como estava.
func TestAppendFailureDoesNotCorrupt(t *testing.T) {
	ctx := context.Background()
	base := newStore(t)
	fs := &failingStore{EventStore: base}
	m := mustMachine(t, fs, "run-fail")

	// Primeiro claim ok.
	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	// Próximo Append falha (simula perda de quórum).
	fs.failNext = true
	fs.failErr = eventstore.ErrNoQuorum
	if err := m.Transition(ctx, Complete, TransitionEvent{}); !errors.Is(err, eventstore.ErrNoQuorum) {
		t.Fatalf("append falhado: err=%v; quero ErrNoQuorum", err)
	}
	if m.Current() != Running {
		t.Fatalf("estado após falha de append=%q; quero running (não avançou)", m.Current())
	}

	// A transição seguinte (store recuperado) deve funcionar e usar o step_id certo.
	if err := m.Transition(ctx, Complete, TransitionEvent{}); err != nil {
		t.Fatalf("após recuperação: %v", err)
	}
	if m.Current() != Complete {
		t.Fatalf("estado=%q; quero complete", m.Current())
	}
	// Reconstrução vê exactamente 2 transições (claim + complete), sem a falhada.
	m2 := mustMachine(t, base, "run-fail")
	if got, _ := m2.Rebuild(ctx); got != Complete {
		t.Fatalf("reconstruído=%q; quero complete", got)
	}
}

// ---------------------------------------------------------------------------
// Recuperação: estado corrente reconstruído após "crash" a partir do log.
// ---------------------------------------------------------------------------

func TestCrashRecoveryRebuild(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Worker 1 avança até compensating, depois "morre".
	w1 := mustMachine(t, st, "run-crash")
	mustSeq(t, ctx, w1, []step{
		{Running, tok},
		{Failed, TransitionEvent{Reason: "boom"}},
		{Compensating, TransitionEvent{}},
	})
	if w1.Current() != Compensating {
		t.Fatalf("w1 estado=%q; quero compensating", w1.Current())
	}

	// Worker 2 (novo processo) reconstrói do log e continua compensating→ready→running.
	w2 := mustMachine(t, st, "run-crash")
	got, err := w2.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if got != Compensating {
		t.Fatalf("reconstruído=%q; quero compensating", got)
	}
	if err := w2.Transition(ctx, Ready, TransitionEvent{Reason: "compensated"}); err != nil {
		t.Fatalf("compensating→ready: %v", err)
	}
	if err := w2.Transition(ctx, Running, TransitionEvent{Token: Uint64Token(2)}); err != nil {
		t.Fatalf("re-claim ready→running: %v", err)
	}
	if w2.Current() != Running {
		t.Fatalf("estado=%q; quero running", w2.Current())
	}
}

// TestRebuildEmptyStreamIsReady confirma que um run sem eventos reconstrói para ready.
func TestRebuildEmptyStreamIsReady(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	m := mustMachine(t, st, "run-empty")
	got, err := m.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild stream vazio: %v", err)
	}
	if got != Ready {
		t.Fatalf("reconstruído=%q; quero ready", got)
	}
}

// TestRebuildIgnoresNonTransitionEvents confirma que o Rebuild só olha para eventos
// de transição, ignorando outros tipos que partilhem o stream do run.
func TestRebuildIgnoresNonTransitionEvents(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	// Um evento de outro tipo primeiro (ex.: turn.recorded) no mesmo stream.
	if _, err := st.Append(ctx, "run-mixed", eventstore.EventInput{
		Type: "turn.recorded", RunID: "run-mixed", StepID: "step-000001",
	}); err != nil {
		t.Fatal(err)
	}
	m := mustMachine(t, st, "run-mixed")
	// Máquina precisa de reconstruir o nStates a partir do que existe (0 transições).
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if m.Current() != Ready {
		t.Fatalf("estado=%q; quero ready (só há turn.recorded)", m.Current())
	}
	// Agora uma transição real convive com o evento estranho.
	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	m2 := mustMachine(t, st, "run-mixed")
	if got, _ := m2.Rebuild(ctx); got != Running {
		t.Fatalf("reconstruído=%q; quero running", got)
	}
}

// TestRebuildRejectsUnknownState prova o fail-closed contra um log corrompido.
func TestRebuildRejectsUnknownState(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	// Injecta um evento de transição com um To forjado.
	payload := []byte(`{"from":"running","to":"zombie","at":"2026-07-12T10:00:00Z"}`)
	if _, err := st.Append(ctx, "run-bad", eventstore.EventInput{
		Type: EventTypeTransition, Payload: payload, RunID: "run-bad", StepID: "state-1",
	}); err != nil {
		t.Fatal(err)
	}
	m := mustMachine(t, st, "run-bad")
	if _, err := m.Rebuild(ctx); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("Rebuild log corrompido: err=%v; quero ErrUnknownState", err)
	}
}

// ---------------------------------------------------------------------------
// Eventos pause/resume/kill expostos.
// ---------------------------------------------------------------------------

func TestPauseResumeKillExposed(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Pause/Resume.
	m := mustMachine(t, st, "run-pr")
	mustSeq(t, ctx, m, []step{{Running, tok}})
	if err := m.Pause(ctx, TransitionEvent{Reason: "steer"}); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if m.Current() != Paused {
		t.Fatalf("estado=%q; quero paused", m.Current())
	}
	if err := m.Resume(ctx, TransitionEvent{}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if m.Current() != Running {
		t.Fatalf("estado=%q; quero running", m.Current())
	}

	// Pause fora de running é rejeitado (governado pela tabela).
	m3 := mustMachine(t, st, "run-pr3")
	if err := m3.Pause(ctx, TransitionEvent{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Pause em ready: err=%v; quero ErrInvalidTransition", err)
	}

	// Kill só a partir de waiting_on_human.
	m2 := mustMachine(t, st, "run-kill")
	mustSeq(t, ctx, m2, []step{{Running, tok}, {WaitingOnHuman, TransitionEvent{}}})
	if err := m2.Kill(ctx, TransitionEvent{Reason: "policy"}); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if m2.Current() != Killed {
		t.Fatalf("estado=%q; quero killed", m2.Current())
	}
	// Kill a partir de running (não na tabela) é rejeitado.
	m4 := mustMachine(t, st, "run-kill4")
	mustSeq(t, ctx, m4, []step{{Running, tok}})
	if err := m4.Kill(ctx, TransitionEvent{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Kill em running: err=%v; quero ErrInvalidTransition", err)
	}
}

// ---------------------------------------------------------------------------
// Concorrência (-race): transições serializadas não corrompem step_ids.
// ---------------------------------------------------------------------------

func TestConcurrentTransitionsRaceSafe(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	m := mustMachine(t, st, "run-race")
	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}

	// Muitas goroutines competem por transições e leituras; a máquina serializa.
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Transition(ctx, WaitingOnTool, TransitionEvent{})
			_ = m.Transition(ctx, Running, TransitionEvent{})
			_ = m.Current()
			_, _, _ = m.CheckDeadlines(ctx)
		}()
	}
	wg.Wait()

	// Estado final coerente (running ou waiting_on_tool) e reconstruível.
	final := m.Current()
	if final != Running && final != WaitingOnTool {
		t.Fatalf("estado final inesperado=%q", final)
	}
	m2 := mustMachine(t, st, "run-race")
	if got, err := m2.Rebuild(ctx); err != nil || got != final {
		t.Fatalf("reconstruído=%q (err=%v); quero %q", got, err, final)
	}
}

// ---------------------------------------------------------------------------
// Reconciliação fail-closed do dedup do Event Store (StatusDuplicate).
// ---------------------------------------------------------------------------

// dupStore força o próximo Append a devolver StatusDuplicate com um evento
// persistido escolhido (payload possivelmente DIFERENTE do pedido), simulando a
// dedup do Event Store sob uma idempotency_key "state-N" já ocupada por outro
// escritor/retry (o cenário que AOS-018 previne mas que a persistência defende).
type dupStore struct {
	EventStore
	dupNext  bool
	dupEvent eventstore.Event
}

func (s *dupStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if s.dupNext {
		s.dupNext = false
		return eventstore.AppendResult{Seq: s.dupEvent.Seq, Status: eventstore.StatusDuplicate, Event: s.dupEvent}, nil
	}
	return s.EventStore.Append(ctx, streamID, in, opts...)
}

func mustRecordPayload(t *testing.T, from, to State) []byte {
	t.Helper()
	p, err := json.Marshal(transitionRecord{From: from, To: to, At: "2026-07-12T10:00:00Z"})
	if err != nil {
		t.Fatalf("marshal transitionRecord: %v", err)
	}
	return p
}

// TestDuplicateDivergenceFailsClosed prova que um StatusDuplicate cujo payload
// persistido difere da transição pedida é recusado (ErrStateDivergence) sem mutar o
// estado — em vez de avançar silenciosamente e divergir do log.
func TestDuplicateDivergenceFailsClosed(t *testing.T) {
	ctx := context.Background()
	base := newStore(t)
	ds := &dupStore{EventStore: base}
	obs := &countingObserver{}
	m := mustMachine(t, ds, "run-dup", WithObserver(obs))

	// Claim real (committed).
	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	// O próximo Append devolve duplicado com running→complete, mas pedimos running→failed.
	ds.dupNext = true
	ds.dupEvent = eventstore.Event{Seq: 99, Payload: mustRecordPayload(t, Running, Complete)}
	if err := m.Transition(ctx, Failed, TransitionEvent{}); !errors.Is(err, ErrStateDivergence) {
		t.Fatalf("duplicado divergente: err=%v; quero ErrStateDivergence", err)
	}
	if m.Current() != Running {
		t.Fatalf("estado após divergência=%q; quero running (intacto)", m.Current())
	}
	if obs.rejected != 1 {
		t.Fatalf("observou %d rejeições; quero 1", obs.rejected)
	}
}

// TestDuplicateIdenticalIsIdempotent confirma que um StatusDuplicate cujo payload
// BATE exactamente a transição pedida (retry benigno) reconcilia e avança o estado.
func TestDuplicateIdenticalIsIdempotent(t *testing.T) {
	ctx := context.Background()
	base := newStore(t)
	ds := &dupStore{EventStore: base}
	m := mustMachine(t, ds, "run-dup2")

	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	ds.dupNext = true
	ds.dupEvent = eventstore.Event{Seq: 42, Payload: mustRecordPayload(t, Running, WaitingOnTool)}
	if err := m.Transition(ctx, WaitingOnTool, TransitionEvent{}); err != nil {
		t.Fatalf("duplicado idêntico devia reconciliar e avançar: %v", err)
	}
	if m.Current() != WaitingOnTool {
		t.Fatalf("estado=%q; quero waiting_on_tool", m.Current())
	}
}

// TestDuplicateCorruptPayloadFailsClosed prova que um duplicado com payload
// indescodificável é recusado sem mutar o estado.
func TestDuplicateCorruptPayloadFailsClosed(t *testing.T) {
	ctx := context.Background()
	base := newStore(t)
	ds := &dupStore{EventStore: base}
	m := mustMachine(t, ds, "run-dup3")

	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	ds.dupNext = true
	ds.dupEvent = eventstore.Event{Seq: 7, Payload: []byte(`{not-json`)}
	if err := m.Transition(ctx, Complete, TransitionEvent{}); err == nil {
		t.Fatal("duplicado com payload corrompido devia falhar")
	}
	if m.Current() != Running {
		t.Fatalf("estado=%q; quero running (intacto)", m.Current())
	}
}

// ---------------------------------------------------------------------------
// Rebuild: continuidade da cadeia e nStates do maior step_id.
// ---------------------------------------------------------------------------

func appendTransition(t *testing.T, st *eventstore.Store, runID, stepID string, from, to State) {
	t.Helper()
	if _, err := st.Append(context.Background(), runID, eventstore.EventInput{
		Type: EventTypeTransition, Payload: mustRecordPayload(t, from, to), RunID: runID, StepID: stepID,
	}); err != nil {
		t.Fatalf("append transição %s: %v", stepID, err)
	}
}

// TestRebuildRejectsBrokenChain prova o fail-closed contra um log bifurcado: o From
// de uma transição não bate o To da anterior.
func TestRebuildRejectsBrokenChain(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	appendTransition(t, st, "run-fork", "state-1", Ready, Running)
	// from=failed não bate o to=running anterior (cadeia partida).
	appendTransition(t, st, "run-fork", "state-2", Failed, Compensating)
	m := mustMachine(t, st, "run-fork")
	if _, err := m.Rebuild(ctx); !errors.Is(err, ErrCorruptChain) {
		t.Fatalf("Rebuild cadeia partida: err=%v; quero ErrCorruptChain", err)
	}
}

// TestRebuildDerivesNStatesFromMaxStepID prova que nStates vem do MAIOR N dos
// step_ids "state-N" (não de uma contagem posicional): um furo (state-1, state-3)
// faz o próximo step_id ser state-4, sem colidir e sem disparar dedup silencioso.
func TestRebuildDerivesNStatesFromMaxStepID(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	appendTransition(t, st, "run-hole", "state-1", Ready, Running)
	appendTransition(t, st, "run-hole", "state-3", Running, WaitingOnTool) // furo em state-2
	m := mustMachine(t, st, "run-hole")
	got, err := m.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if got != WaitingOnTool {
		t.Fatalf("reconstruído=%q; quero waiting_on_tool", got)
	}
	if m.nStates != 3 {
		t.Fatalf("nStates=%d; quero 3 (maior N, não a contagem posicional 2)", m.nStates)
	}
	// A próxima transição usa state-4 — não colide com o state-3 existente.
	if err := m.Transition(ctx, Running, TransitionEvent{}); err != nil {
		t.Fatalf("retoma após furo (devia usar state-4): %v", err)
	}
	if m.Current() != Running {
		t.Fatalf("estado=%q; quero running", m.Current())
	}
}

// ---------------------------------------------------------------------------
// Semântica do wall-clock: por-segmento (reinicia a cada entrada em running).
// ---------------------------------------------------------------------------

// TestWallClockIsPerSegment documenta a escolha de design: o wall-clock de timed_out
// conta a partir da ENTRADA no running corrente, pelo que oscilar running↔suspensão
// reinicia o relógio e nunca acumula tempo total. Um único segmento que excede o
// wall-clock, esse sim, dispara timed_out.
func TestWallClockIsPerSegment(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	clk := newManualClock()
	m := mustMachine(t, st, "run-osc", WithClock(clk), WithRunWallClock(time.Minute))
	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	// Oscila running→waiting_on_tool→running; cada segmento em running < wall-clock.
	for i := 0; i < 5; i++ {
		clk.Advance(59 * time.Second)
		if _, fired, _ := m.CheckDeadlines(ctx); fired {
			t.Fatalf("iteração %d: disparou dentro do segmento (<60s)", i)
		}
		if err := m.Transition(ctx, WaitingOnTool, TransitionEvent{}); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Hour) // tempo em suspensão não conta para o wall-clock de running.
		if err := m.Transition(ctx, Running, TransitionEvent{}); err != nil {
			t.Fatal(err)
		}
	}
	// Apesar de muito tempo total decorrido, a retoma reiniciou o relógio: não dispara.
	if _, fired, _ := m.CheckDeadlines(ctx); fired {
		t.Fatal("wall-clock por-segmento não devia disparar logo após a retoma")
	}
	// Confirmação positiva: um único segmento que EXCEDE o wall-clock dispara timed_out.
	clk.Advance(61 * time.Second)
	if state, fired, err := m.CheckDeadlines(ctx); err != nil || !fired || state != TimedOut {
		t.Fatalf("segmento a exceder: state=%q fired=%v err=%v; quero timed_out/true", state, fired, err)
	}
}

// ---------------------------------------------------------------------------
// Helpers de sequência.
// ---------------------------------------------------------------------------

type step struct {
	to    State
	event TransitionEvent
}

func mustSeq(t *testing.T, ctx context.Context, m *Machine, steps []step) {
	t.Helper()
	for i, s := range steps {
		if err := m.Transition(ctx, s.to, s.event); err != nil {
			t.Fatalf("passo %d (→%s): %v", i, s.to, err)
		}
	}
}

func lastReason(t *testing.T, st EventStore, runID string) string {
	t.Helper()
	events, err := st.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != EventTypeTransition {
			continue
		}
		var rec transitionRecord
		if err := unmarshalJSON(events[i].Payload, &rec); err != nil {
			t.Fatalf("descodifica: %v", err)
		}
		return rec.Reason
	}
	t.Fatalf("sem eventos de transição para %s", runID)
	return ""
}
