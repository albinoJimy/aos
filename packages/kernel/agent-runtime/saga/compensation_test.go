package saga

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Helpers de teste.
// ---------------------------------------------------------------------------

func newStore(t *testing.T) *eventstore.Store {
	t.Helper()
	st, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mustLedger(t *testing.T, store durable.EventStore) *durable.StepLedger {
	t.Helper()
	l, err := durable.NewStepLedger(store)
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	return l
}

func mustMachine(t *testing.T, store state.EventStore, runID string) *state.Machine {
	t.Helper()
	m, err := state.NewMachine(store, runID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	return m
}

// driveToFailed leva um run real de ready até failed (ready→running→failed), o ponto
// de entrada da saga.
func driveToFailed(t *testing.T, ctx context.Context, m *state.Machine) {
	t.Helper()
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("ready→running: %v", err)
	}
	if err := m.Transition(ctx, state.Failed, state.TransitionEvent{Reason: "boom"}); err != nil {
		t.Fatalf("running→failed: %v", err)
	}
}

// effectLog regista, de forma segura para concorrência, a ORDEM e a CONTAGEM de
// reversões efectivamente executadas (Action que devolveu sucesso). É o instrumento
// que prova ordem inversa e 0 duplicados.
type effectLog struct {
	mu    sync.Mutex
	order []string
	count map[string]int
}

func newEffectLog() *effectLog { return &effectLog{count: map[string]int{}} }

func (e *effectLog) record(stepID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.order = append(e.order, stepID)
	e.count[stepID]++
}

func (e *effectLog) snapshotOrder() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.order...)
}

func (e *effectLog) countFor(stepID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.count[stepID]
}

// registerSteps regista K compensações (step-1..step-K) cujas acções, quando correm,
// registam o seu step_id no effectLog. Devolve os step_ids por ordem de aplicação.
func registerSteps(r *CompensationRegistry, k int, log *effectLog) []string {
	ids := make([]string, 0, k)
	for i := 1; i <= k; i++ {
		id := fmt.Sprintf("step-%d", i)
		ids = append(ids, id)
		sid := id
		_ = r.Register(Compensation{StepID: sid, Action: func(context.Context) error {
			log.record(sid)
			return nil
		}})
	}
	return ids
}

// countingObserver conta os desfechos observáveis da saga (para asserções sobre dedup
// e escalada) sem reter segredos.
type countingObserver struct {
	mu             sync.Mutex
	started        int
	compensatedRan int
	compensatedDup int
	retries        int
	escalated      int
	completed      int
}

func (o *countingObserver) Started(string, int) { o.mu.Lock(); o.started++; o.mu.Unlock() }
func (o *countingObserver) Compensated(_ string, ranNow bool) {
	o.mu.Lock()
	if ranNow {
		o.compensatedRan++
	} else {
		o.compensatedDup++
	}
	o.mu.Unlock()
}
func (o *countingObserver) Retry(string, int, error) { o.mu.Lock(); o.retries++; o.mu.Unlock() }
func (o *countingObserver) Escalated(string, error)  { o.mu.Lock(); o.escalated++; o.mu.Unlock() }
func (o *countingObserver) Completed(string)         { o.mu.Lock(); o.completed++; o.mu.Unlock() }

// fakeMachine é uma máquina controlável que VALIDA as transições contra a tabela real
// de AOS-017 (coerência), mas permite pinar o estado em compensating para exercitar a
// reexecução da saga (crash exactamente antes do commit de compensating→ready).
type fakeMachine struct {
	mu              sync.Mutex
	runID           string
	cur             state.State
	log             []state.State
	pinCompensating bool
}

func (f *fakeMachine) Current() state.State { f.mu.Lock(); defer f.mu.Unlock(); return f.cur }
func (f *fakeMachine) RunID() string        { return f.runID }
func (f *fakeMachine) Transition(_ context.Context, to state.State, _ state.TransitionEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !state.IsValidTransition(f.cur, to) {
		return state.ErrInvalidTransition
	}
	f.log = append(f.log, to)
	if f.pinCompensating && to == state.Ready {
		// Simula um crash imediatamente ANTES do commit de compensating→ready: a
		// transição foi "tentada" mas o estado durável não avançou.
		return nil
	}
	f.cur = to
	return nil
}

// ---------------------------------------------------------------------------
// Construção.
// ---------------------------------------------------------------------------

func TestNewSagaCoordinatorValidation(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	m := mustMachine(t, store, "run-x")
	l := mustLedger(t, store)
	r := NewCompensationRegistry()

	if _, err := NewSagaCoordinator(nil, l, r); !errors.Is(err, ErrNilMachine) {
		t.Fatalf("machine nil devia dar ErrNilMachine, veio %v", err)
	}
	if _, err := NewSagaCoordinator(m, nil, r); !errors.Is(err, ErrNilLedger) {
		t.Fatalf("ledger nil devia dar ErrNilLedger, veio %v", err)
	}
	if _, err := NewSagaCoordinator(m, l, nil); !errors.Is(err, ErrNilRegistry) {
		t.Fatalf("registry nil devia dar ErrNilRegistry, veio %v", err)
	}
	if _, err := NewSagaCoordinator(m, l, r); err != nil {
		t.Fatalf("construção válida: %v", err)
	}
}

func TestCompensateRejectsWrongState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore(t)
	m := mustMachine(t, store, "run-wrong") // fica em ready
	c, _ := NewSagaCoordinator(m, mustLedger(t, store), NewCompensationRegistry())
	if err := c.Compensate(ctx); !errors.Is(err, ErrNotCompensating) {
		t.Fatalf("Compensate em ready devia dar ErrNotCompensating, veio %v", err)
	}
	if m.Current() != state.Ready {
		t.Fatalf("estado não devia mudar, veio %s", m.Current())
	}
}

// ---------------------------------------------------------------------------
// Teste requerido 1: saga feliz — falha após K passos, compensa por ordem inversa.
// ---------------------------------------------------------------------------

func TestSagaHappyReverseOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run-happy"
	const k = 5

	store := newStore(t)
	m := mustMachine(t, store, runID)
	driveToFailed(t, ctx, m)

	reg := NewCompensationRegistry()
	log := newEffectLog()
	ids := registerSteps(reg, k, log)

	obs := &countingObserver{}
	c, _ := NewSagaCoordinator(m, mustLedger(t, store), reg, WithObserver(obs))

	if err := c.Compensate(ctx); err != nil {
		t.Fatalf("Compensate: %v", err)
	}

	// Ordem inversa exacta (LIFO): step-5, step-4, ..., step-1.
	got := log.snapshotOrder()
	if len(got) != k {
		t.Fatalf("correram %d compensações, esperado %d", len(got), k)
	}
	for i, stepID := range got {
		want := ids[k-1-i]
		if stepID != want {
			t.Fatalf("ordem[%d]=%s, esperado %s (LIFO)", i, stepID, want)
		}
	}
	// Cada efeito parcial revertido exactamente uma vez (estado consistente).
	for _, id := range ids {
		if n := log.countFor(id); n != 1 {
			t.Fatalf("compensação de %s correu %d vezes, esperado 1", id, n)
		}
	}
	// Política: compensating → ready (retry limpo).
	if m.Current() != state.Ready {
		t.Fatalf("estado final=%s, esperado ready", m.Current())
	}
	if obs.compensatedRan != k || obs.compensatedDup != 0 || obs.completed != 1 {
		t.Fatalf("obs: ran=%d dup=%d completed=%d", obs.compensatedRan, obs.compensatedDup, obs.completed)
	}
}

// ---------------------------------------------------------------------------
// Teste requerido 2: idempotência — reexecutar a saga NÃO duplica reversões.
// ---------------------------------------------------------------------------

func TestCompensationIdempotentNoDuplicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run-idem"
	const k = 4

	store := newStore(t)
	// fakeMachine pinada em compensating: a 1.ª Compensate tenta compensating→ready mas
	// o "commit" não avança o estado (crash simulado) — permite reexecutar a saga.
	fm := &fakeMachine{runID: runID, cur: state.Failed, pinCompensating: true}

	reg := NewCompensationRegistry()
	log := newEffectLog()
	ids := registerSteps(reg, k, log)

	obs := &countingObserver{}
	ledger := mustLedger(t, store)
	c, _ := NewSagaCoordinator(fm, ledger, reg, WithObserver(obs))

	// 1.ª execução: corre as K compensações.
	if err := c.Compensate(ctx); err != nil {
		t.Fatalf("1.ª Compensate: %v", err)
	}
	// 2.ª execução (reexecução da saga): tudo já aplicado ⇒ 0 novas reversões.
	if err := c.Compensate(ctx); err != nil {
		t.Fatalf("2.ª Compensate: %v", err)
	}

	// Cada reversão observável correu EXACTAMENTE uma vez (0 duplicados).
	for _, id := range ids {
		if n := log.countFor(id); n != 1 {
			t.Fatalf("reversão de %s correu %d vezes, esperado 1 (0 duplicados)", id, n)
		}
	}
	// A 2.ª passagem foi toda deduplicada.
	if obs.compensatedRan != k {
		t.Fatalf("reversões executadas=%d, esperado %d", obs.compensatedRan, k)
	}
	if obs.compensatedDup != k {
		t.Fatalf("reversões deduplicadas=%d, esperado %d (2.ª passagem)", obs.compensatedDup, k)
	}
}

// ---------------------------------------------------------------------------
// Teste requerido 3: crash DURANTE a compensação — retoma sem repetir nem saltar.
// ---------------------------------------------------------------------------

// keyFaultStore injecta UM crash no commit do ledger cujo step_id contém failSubstr —
// modela um crash-before-commit AUTÊNTICO: o efeito da compensação já correu, mas o
// registo durável não chegou a ser escrito. Os appends da máquina (transições) e os
// restantes appends do ledger passam intactos ao store real.
type keyFaultStore struct {
	inner      *eventstore.Store
	failSubstr string
	mu         sync.Mutex
	tripped    bool
}

var errCrashBeforeCommit = errors.New("crash injectado antes do commit da compensação")

func (f *keyFaultStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	f.mu.Lock()
	if !f.tripped && strings.Contains(in.StepID, f.failSubstr) {
		f.tripped = true
		f.mu.Unlock()
		return eventstore.AppendResult{}, errCrashBeforeCommit
	}
	f.mu.Unlock()
	return f.inner.Append(ctx, streamID, in, opts...)
}

func (f *keyFaultStore) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	return f.inner.Read(ctx, streamID, fromSeq)
}

// countCommittedComps lê o log e conta as compensações commitadas de forma durável,
// verificando que cada chave de compensação aparece NO MÁXIMO uma vez (a dedup do
// Event Store garante 0 registos de reversão duplicados).
func countCommittedComps(t *testing.T, store *eventstore.Store, runID string) map[string]int {
	t.Helper()
	events, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	seen := map[string]int{}
	for _, e := range events {
		if e.Type != durable.EventTypeLedgerApplied {
			continue
		}
		seen[e.IdempotencyKey]++
	}
	for key, n := range seen {
		if n != 1 {
			t.Fatalf("chave de compensação %q commitada %d vezes (esperado 1 — 0 duplicados)", key, n)
		}
	}
	return seen
}

func TestCrashDuringCompensationResumes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run-crash"
	const k = 6
	// LIFO: step-6, step-5, [step-4 CRASHA no commit], step-3, step-2, step-1.
	// step-6 e step-5 ficam dur-avelmente compensados; o crash dá-se a compensar step-4.

	store := newStore(t)
	m := mustMachine(t, store, runID)
	driveToFailed(t, ctx, m)
	log := newEffectLog()

	// --- Worker 1: crash-before-commit ao compensar step-4 (após step-6, step-5). ---
	reg1 := NewCompensationRegistry()
	ids := registerSteps(reg1, k, log)
	faulty := &keyFaultStore{inner: store, failSubstr: "comp-step-4"}
	ledger1, err := durable.NewStepLedger(faulty)
	if err != nil {
		t.Fatalf("NewStepLedger(faulty): %v", err)
	}
	// maxRetries=0: o crash não é re-tentado no mesmo worker — o processo "morre".
	c1, _ := NewSagaCoordinator(m, ledger1, reg1, WithMaxRetries(0))
	if err := c1.Compensate(ctx); !errors.Is(err, ErrCompensationExhausted) {
		t.Fatalf("worker 1 devia escalar por crash-before-commit, veio %v", err)
	}
	if m.Current() != state.Compensating {
		t.Fatalf("após crash o run devia estar em compensating, veio %s", m.Current())
	}
	// Só step-6 e step-5 ficaram dur-avelmente compensados (2 registos).
	if got := len(countCommittedComps(t, store, runID)); got != 2 {
		t.Fatalf("compensações commitadas após crash=%d, esperado 2", got)
	}

	// --- Worker 2 (processo novo): máquina e ledger reconstruídos do log. ---
	m2 := mustMachine(t, store, runID)
	if st, err := m2.Rebuild(ctx); err != nil || st != state.Compensating {
		t.Fatalf("Rebuild da máquina=%s err=%v, esperado compensating", st, err)
	}
	reg2 := NewCompensationRegistry()
	registerSteps(reg2, k, log) // mesma ordem determinística; ledger2 fresco (in-memory vazio)
	obs := &countingObserver{}
	c2, _ := NewSagaCoordinator(m2, mustLedger(t, store), reg2, WithObserver(obs))
	if err := c2.Compensate(ctx); err != nil {
		t.Fatalf("worker 2 (retoma): %v", err)
	}

	// Retoma: as 2 já commitadas são DEDUPLICADAS (não repetidas), as 4 pendentes
	// (incluindo step-4, cujo commit falhara) correm — sem saltar nenhuma.
	if obs.compensatedDup != 2 {
		t.Fatalf("worker 2 deduplicou %d, esperado 2 (step-6, step-5 já aplicadas)", obs.compensatedDup)
	}
	if obs.compensatedRan != k-2 {
		t.Fatalf("worker 2 executou %d, esperado %d", obs.compensatedRan, k-2)
	}
	// Estado durável final: as 6 compensações commitadas, cada chave UMA vez (0 duplicados).
	if got := len(countCommittedComps(t, store, runID)); got != k {
		t.Fatalf("compensações commitadas no total=%d, esperado %d", got, k)
	}
	if m2.Current() != state.Ready {
		t.Fatalf("estado final=%s, esperado ready", m2.Current())
	}
	_ = ids
}

// ---------------------------------------------------------------------------
// Teste requerido 4: transições failed→compensating→ready coerentes e reconstruíveis.
// ---------------------------------------------------------------------------

func TestTransitionsCoherentAndReplayable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run-replay"

	store := newStore(t)
	m := mustMachine(t, store, runID)
	driveToFailed(t, ctx, m)

	reg := NewCompensationRegistry()
	log := newEffectLog()
	registerSteps(reg, 3, log)
	c, _ := NewSagaCoordinator(m, mustLedger(t, store), reg)
	if err := c.Compensate(ctx); err != nil {
		t.Fatalf("Compensate: %v", err)
	}
	if m.Current() != state.Ready {
		t.Fatalf("estado=%s, esperado ready", m.Current())
	}

	// Reconstrução por replay num worker novo: o estado corrente é ready (a saga é
	// durável e reconstruível). A cadeia ready→running→failed→compensating→ready é
	// coerente com a tabela de AOS-017 (senão Rebuild falharia por ErrCorruptChain).
	m2 := mustMachine(t, store, runID)
	st, err := m2.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if st != state.Ready {
		t.Fatalf("Rebuild=%s, esperado ready", st)
	}

	// O run pode ser reclamado de novo (retry limpo): ready→running com fencing token.
	if err := m2.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(2)}); err != nil {
		t.Fatalf("retry ready→running: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Teste requerido 5: compensação que falha — retry idempotente / escalada honesta.
// ---------------------------------------------------------------------------

// TestCompensationRetryConverges: uma compensação falha nas primeiras tentativas e
// depois converge; a saga NÃO duplica a reversão e conclui em ready.
func TestCompensationRetryConverges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run-retry"

	store := newStore(t)
	m := mustMachine(t, store, runID)
	driveToFailed(t, ctx, m)

	reg := NewCompensationRegistry()
	var attempts int
	var okRuns int
	// Uma única compensação que falha 2x e sucede à 3.ª.
	_ = reg.Register(Compensation{StepID: "flaky", Action: func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("falha transitória")
		}
		okRuns++
		return nil
	}})

	obs := &countingObserver{}
	c, _ := NewSagaCoordinator(m, mustLedger(t, store), reg, WithMaxRetries(2), WithObserver(obs))
	if err := c.Compensate(ctx); err != nil {
		t.Fatalf("Compensate devia convergir, veio %v", err)
	}
	if attempts != 3 || okRuns != 1 {
		t.Fatalf("attempts=%d okRuns=%d, esperado 3 e 1 (retry idempotente, sem duplicar)", attempts, okRuns)
	}
	if obs.retries != 2 {
		t.Fatalf("obs.retries=%d, esperado 2", obs.retries)
	}
	if m.Current() != state.Ready {
		t.Fatalf("estado=%s, esperado ready", m.Current())
	}
}

// TestCompensationExhaustedEscalates: uma compensação irrecuperável esgota o retry; a
// saga ESCALA (ErrCompensationExhausted), NÃO transita para ready e NÃO finge sucesso.
func TestCompensationExhaustedEscalates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run-escalate"

	store := newStore(t)
	m := mustMachine(t, store, runID)
	driveToFailed(t, ctx, m)

	reg := NewCompensationRegistry()
	sentinel := errors.New("irrecuperável")
	var calls int
	_ = reg.Register(Compensation{StepID: "broken", Action: func(context.Context) error {
		calls++
		return sentinel
	}})

	obs := &countingObserver{}
	c, _ := NewSagaCoordinator(m, mustLedger(t, store), reg, WithMaxRetries(2), WithObserver(obs))
	err := c.Compensate(ctx)
	if !errors.Is(err, ErrCompensationExhausted) {
		t.Fatalf("esperado ErrCompensationExhausted, veio %v", err)
	}
	// O erro-raiz é preservado (não se finge sucesso nem se apaga a causa).
	if !errors.Is(err, sentinel) {
		t.Fatalf("erro devia envolver a causa-raiz, veio %v", err)
	}
	if calls != 3 { // 1 + 2 retries
		t.Fatalf("Action correu %d vezes, esperado 3", calls)
	}
	if obs.escalated != 1 || obs.completed != 0 {
		t.Fatalf("obs: escalated=%d completed=%d, esperado 1 e 0", obs.escalated, obs.completed)
	}
	// Semântica honesta: o run FICA em compensating (preso, exige intervenção).
	if m.Current() != state.Compensating {
		t.Fatalf("estado=%s, esperado compensating (não finge sucesso)", m.Current())
	}
}

// TestExhaustedThenRecoveredResumes: após uma escalada, corrigida a causa e retomada a
// saga, a reversão que faltava aplica-se e o run conclui — sem duplicar as já feitas.
func TestExhaustedThenRecoveredResumes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run-escalate-recover"

	store := newStore(t)
	m := mustMachine(t, store, runID)
	driveToFailed(t, ctx, m)

	log := newEffectLog()
	reg := NewCompensationRegistry()
	// step-1 compensa bem; step-2 (compensado PRIMEIRO, LIFO) falha até um switch.
	broken := true
	_ = reg.Register(Compensation{StepID: "step-1", Action: func(context.Context) error {
		log.record("step-1")
		return nil
	}})
	_ = reg.Register(Compensation{StepID: "step-2", Action: func(context.Context) error {
		if broken {
			return errors.New("ainda partido")
		}
		log.record("step-2")
		return nil
	}})

	c, _ := NewSagaCoordinator(m, mustLedger(t, store), reg, WithMaxRetries(0))
	if err := c.Compensate(ctx); !errors.Is(err, ErrCompensationExhausted) {
		t.Fatalf("1.ª passagem devia escalar, veio %v", err)
	}
	// step-2 (LIFO primeiro) falhou ⇒ step-1 nem chegou a correr.
	if log.countFor("step-1") != 0 {
		t.Fatalf("step-1 não devia correr antes de step-2 (LIFO)")
	}
	if m.Current() != state.Compensating {
		t.Fatalf("estado=%s, esperado compensating", m.Current())
	}

	// Corrige a causa e retoma (mesmo run, em compensating).
	broken = false
	if err := c.Compensate(ctx); err != nil {
		t.Fatalf("retoma após correcção: %v", err)
	}
	if log.countFor("step-2") != 1 || log.countFor("step-1") != 1 {
		t.Fatalf("reversões: step-2=%d step-1=%d, esperado 1 e 1", log.countFor("step-2"), log.countFor("step-1"))
	}
	if m.Current() != state.Ready {
		t.Fatalf("estado final=%s, esperado ready", m.Current())
	}
}

// TestCompensationAtLeastOnceOnCrashBeforeCommit prova a JANELA at-least-once do
// contrato (não exactly-once do efeito externo): um crash-before-commit do registo da
// compensação, seguido de retry na MESMA passagem (maxRetries>0), RE-CORRE comp.Action —
// o efeito inverso corre 2x observável enquanto a saga reporta sucesso e vai para ready.
// É o custo honesto de at-least-once: "0 duplicados observáveis" assenta na idempotência
// de comp.Action (aqui a Action sucede sempre e é contada, para expor a janela). O registo
// DURÁVEL fica, ainda assim, UMA vez (a dedup do ES colapsa-o).
func TestCompensationAtLeastOnceOnCrashBeforeCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run-at-least-once"

	store := newStore(t)
	m := mustMachine(t, store, runID)
	driveToFailed(t, ctx, m)

	// Uma única compensação cuja Action SEMPRE sucede; contamos as execuções do efeito.
	var effectRuns int
	reg := NewCompensationRegistry()
	_ = reg.Register(Compensation{StepID: "victim", Action: func(context.Context) error {
		effectRuns++
		return nil
	}})

	// O commit da compensação de "victim" falha UMA vez (crash-before-commit autêntico:
	// o efeito já correu, o registo durável não chegou a ser escrito).
	faulty := &keyFaultStore{inner: store, failSubstr: "comp-victim"}
	ledger, err := durable.NewStepLedger(faulty)
	if err != nil {
		t.Fatalf("NewStepLedger(faulty): %v", err)
	}

	obs := &countingObserver{}
	// maxRetries default (2): o retry na MESMA passagem re-corre o efeito.
	c, _ := NewSagaCoordinator(m, ledger, reg, WithObserver(obs))
	if err := c.Compensate(ctx); err != nil {
		t.Fatalf("Compensate devia convergir após o retry, veio %v", err)
	}

	// A JANELA: o efeito inverso correu 2x (crash-before-commit + retry) apesar de a saga
	// reportar sucesso — exactamente a refutação de "exactamente uma vez" sem idempotência.
	if effectRuns != 2 {
		t.Fatalf("comp.Action correu %d vezes, esperado 2 (crash-before-commit + retry: at-least-once)", effectRuns)
	}
	if obs.retries != 1 {
		t.Fatalf("obs.retries=%d, esperado 1 (o crash-before-commit conta como falha re-tentada)", obs.retries)
	}
	// A saga concluiu: run em ready (o efeito duplicado NÃO impede o sucesso reportado).
	if m.Current() != state.Ready {
		t.Fatalf("estado final=%s, esperado ready", m.Current())
	}
	// O registo DURÁVEL fica UMA vez (a dedup do ES garante 0 registos duplicados), mesmo
	// tendo o efeito observável corrido 2x.
	if got := len(countCommittedComps(t, store, runID)); got != 1 {
		t.Fatalf("compensações commitadas=%d, esperado 1 (registo durável único)", got)
	}
}

// TestOverCompensationIsCallerResponsibility CARACTERIZA a fronteira de responsabilidade
// do finding de sobre-compensação: o coordinator compensa TODA [Compensation] que o
// registry apresentar, SEM cruzar o ledger do passo ORIGINAL (run:<step_id>). Um passo
// cujo efeito directo NUNCA foi aplicado (não há registo run:<step_id> no ledger) é
// compensado na mesma. A invariante "passos não aplicados não são compensados" é, por
// contrato, responsabilidade EXCLUSIVA do chamador (registar-no-momento-de-aplicar); o
// coordinator não tem defesa-em-profundidade contra uma reconstrução que sobre-registe.
func TestOverCompensationIsCallerResponsibility(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run-over-compensate"

	store := newStore(t)
	m := mustMachine(t, store, runID)
	driveToFailed(t, ctx, m)

	// Registamos uma compensação para "phantom" — um passo cujo efeito ORIGINAL nunca foi
	// aplicado (não escrevemos nenhum registo run:phantom no ledger).
	var compensated bool
	reg := NewCompensationRegistry()
	_ = reg.Register(Compensation{StepID: "phantom", Action: func(context.Context) error {
		compensated = true
		return nil
	}})

	ledger := mustLedger(t, store)

	// Sanidade: o ledger do passo ORIGINAL está VAZIO (o efeito directo nunca aplicou).
	origKey, err := durable.IdempotencyKey(runID, "phantom")
	if err != nil {
		t.Fatalf("IdempotencyKey: %v", err)
	}
	if _, ok := ledger.Applied(origKey); ok {
		t.Fatalf("pré-condição do teste violada: o passo original não devia estar aplicado")
	}

	c, _ := NewSagaCoordinator(m, ledger, reg)
	if err := c.Compensate(ctx); err != nil {
		t.Fatalf("Compensate: %v", err)
	}

	// COMPORTAMENTO CARACTERIZADO: a compensação correu, apesar de o passo original nunca
	// ter aplicado. O coordinator NÃO consulta run:<step_id> — é responsabilidade do
	// chamador só registar compensações de passos efectivamente aplicados.
	if !compensated {
		t.Fatalf("o coordinator devia compensar toda compensação registada (sem cruzar o ledger original)")
	}
}

// ---------------------------------------------------------------------------
// Coerência do ledger de compensação com o Event Store (registo append-only).
// ---------------------------------------------------------------------------

// TestCompensationRecordedInEventStore verifica que cada compensação deixou um evento
// step.ledger.applied durável no stream do run, com a chave namespaced comp-.
func TestCompensationRecordedInEventStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run-es"
	const k = 3

	store := newStore(t)
	m := mustMachine(t, store, runID)
	driveToFailed(t, ctx, m)
	reg := NewCompensationRegistry()
	registerSteps(reg, k, newEffectLog())
	c, _ := NewSagaCoordinator(m, mustLedger(t, store), reg)
	if err := c.Compensate(ctx); err != nil {
		t.Fatalf("Compensate: %v", err)
	}

	events, err := store.Read(ctx, runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var compEvents int
	for _, e := range events {
		if e.Type != durable.EventTypeLedgerApplied {
			continue
		}
		compEvents++
		// A idempotency_key do evento de compensação é run:ledger-comp-step-N.
		if !strings.Contains(e.IdempotencyKey, ":ledger-comp-") {
			t.Fatalf("evento de ledger com chave inesperada: %q", e.IdempotencyKey)
		}
	}
	if compEvents != k {
		t.Fatalf("eventos de compensação=%d, esperado %d", compEvents, k)
	}
}
