package durable

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// downstream modela um alvo externo que HONRA a idempotency key (deduplica). É o
// modelo honesto do ADR-001: o effect pode bater no downstream mais de uma vez
// (at-least-once), mas o downstream materializa o efeito UMA vez por chave. O
// contador `materialized` conta EFEITOS OBSERVÁVEIS; `calls` conta invocações.
type downstream struct {
	mu           sync.Mutex
	effects      map[string][]byte // key → resultado (materializado uma vez)
	calls        map[string]int    // key → nº de vezes que o effect bateu no downstream
	materialized int               // total de efeitos OBSERVÁVEIS criados (deve = nº de keys)
}

func newDownstream() *downstream {
	return &downstream{effects: map[string][]byte{}, calls: map[string]int{}}
}

// apply é o efeito externo idempotente: a mesma key nunca cria dois efeitos
// observáveis. Devolve o resultado (sempre o mesmo para a mesma key).
func (d *downstream) apply(key string, payload []byte) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls[key]++
	if res, ok := d.effects[key]; ok {
		return res // dedup: já aplicado, 0 novos efeitos observáveis
	}
	res := append([]byte("done:"), payload...)
	d.effects[key] = res
	d.materialized++
	return res
}

func (d *downstream) observableFor(key string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.effects[key]; ok {
		return 1
	}
	return 0
}

func (d *downstream) callsFor(key string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[key]
}

// countingObserver conta apply/dedup para asserção de observabilidade.
type countingObserver struct {
	mu               sync.Mutex
	applied, deduped int
	lastHash         string
}

func (o *countingObserver) Applied(h string) {
	o.mu.Lock()
	o.applied++
	o.lastHash = h
	o.mu.Unlock()
}
func (o *countingObserver) Deduplicated(h string) {
	o.mu.Lock()
	o.deduped++
	o.lastHash = h
	o.mu.Unlock()
}

// faultyStore embrulha um Event Store real e injecta uma falha de Append (modela
// um crash entre o efeito e o commit do ledger). failAppends decrementa a cada
// tentativa: enquanto > 0, Append falha SEM persistir (o commit não acontece).
type faultyStore struct {
	inner       EventStore
	mu          sync.Mutex
	failAppends int
}

var errInjectedCrash = errors.New("crash injectado antes do commit do ledger")

func (f *faultyStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	f.mu.Lock()
	if f.failAppends > 0 {
		f.failAppends--
		f.mu.Unlock()
		return eventstore.AppendResult{}, errInjectedCrash
	}
	f.mu.Unlock()
	return f.inner.Append(ctx, streamID, in, opts...)
}

func (f *faultyStore) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	return f.inner.Read(ctx, streamID, fromSeq)
}

// stubStore devolve respostas FIXAS de Append/Read, para exercitar os ramos de
// erro de desserialização do registo canónico do ledger (evento corrompido) sem
// depender do Event Store real.
type stubStore struct {
	appendResult eventstore.AppendResult
	appendErr    error
	readEvents   []eventstore.Event
	readErr      error
}

func (s *stubStore) Append(context.Context, string, eventstore.EventInput, ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return s.appendResult, s.appendErr
}
func (s *stubStore) Read(context.Context, string, uint64) ([]eventstore.Event, error) {
	return s.readEvents, s.readErr
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

// ---------------------------------------------------------------------------
// Testes
// ---------------------------------------------------------------------------

// TestApplyIdempotentReexecution é o critério central: reexecutar o MESMO passo
// não corre o effect na 2.ª vez e devolve o resultado idêntico do ledger — 0
// efeitos duplicados.
func TestApplyIdempotentReexecution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	obs := &countingObserver{}
	ledger, err := NewStepLedger(newStore(t), WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := IdempotencyKey("run-1", "step-000001")

	var effectRuns int
	effect := func(context.Context) (Result, error) {
		effectRuns++
		return Result{Status: "ok", Payload: []byte("payload-1")}, nil
	}

	// 1.ª execução: corre o effect, aplica.
	res1, applied1, err := ledger.Apply(ctx, key, effect)
	if err != nil {
		t.Fatalf("Apply 1: %v", err)
	}
	if !applied1 {
		t.Fatalf("1.ª execução devia ter wasApplied=true")
	}

	// 2.ª execução (mesma key): NÃO corre o effect, devolve o memorizado.
	res2, applied2, err := ledger.Apply(ctx, key, effect)
	if err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if applied2 {
		t.Fatalf("reexecução devia ter wasApplied=false")
	}
	if effectRuns != 1 {
		t.Fatalf("effect correu %d vezes, quero 1 (0 efeitos duplicados)", effectRuns)
	}
	if string(res1.Payload) != string(res2.Payload) || res1.Status != res2.Status {
		t.Fatalf("resultado não idêntico entre execuções: %+v vs %+v", res1, res2)
	}
	if obs.applied != 1 || obs.deduped != 1 {
		t.Fatalf("observabilidade: applied=%d deduped=%d, quero 1/1", obs.applied, obs.deduped)
	}
}

// TestApplyErrorDoesNotRecord garante que um effect que falha não deixa registo —
// o passo não ficou aplicado e o retry volta a correr o effect.
func TestApplyErrorDoesNotRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger, _ := NewStepLedger(newStore(t))
	key, _ := IdempotencyKey("run-e", "step-000001")

	boom := errors.New("boom")
	if _, applied, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{}, boom
	}); !errors.Is(err, boom) || applied {
		t.Fatalf("Apply devia propagar erro sem aplicar, veio applied=%v err=%v", applied, err)
	}
	if _, ok := ledger.Applied(key); ok {
		t.Fatalf("erro não devia deixar registo no ledger")
	}
	// Retry converge.
	if _, applied, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok"}, nil
	}); err != nil || !applied {
		t.Fatalf("retry devia aplicar, veio applied=%v err=%v", applied, err)
	}
}

// TestFaultInjectionCrashBeforeCommit modela um crash APÓS o efeito mas ANTES do
// commit do ledger. O retry volta a correr o effect (o registo não existe), mas o
// downstream honra a key e regista o efeito UMA vez observável — 0 duplicados.
func TestFaultInjectionCrashBeforeCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	real := newStore(t)
	fs := &faultyStore{inner: real, failAppends: 1} // 1.º commit falha (crash)
	ledger, _ := NewStepLedger(fs)

	down := newDownstream()
	key, _ := IdempotencyKey("run-fi", "step-000007")
	// O effect propaga a MESMA key ao downstream (idempotência downstream).
	effect := func(context.Context) (Result, error) {
		out := down.apply(key, []byte("body"))
		return Result{Status: "ok", Payload: out}, nil
	}

	// 1.ª tentativa: effect corre (bate no downstream), mas o commit CRASHA.
	if _, _, err := ledger.Apply(ctx, key, effect); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("1.ª tentativa devia crashar no commit, veio %v", err)
	}

	// Retry (mesmo worker/ledger — o registo não ficou): effect corre OUTRA vez.
	res, applied, err := ledger.Apply(ctx, key, effect)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !applied {
		t.Fatalf("retry devia aplicar (registo não existia)")
	}

	// PROVA: o effect bateu no downstream 2x (at-least-once), mas o efeito
	// OBSERVÁVEL é exactamente 1 (downstream deduplicou pela key).
	if down.callsFor(key) != 2 {
		t.Fatalf("effect devia ter corrido 2x, veio %d", down.callsFor(key))
	}
	if down.observableFor(key) != 1 {
		t.Fatalf("efeitos OBSERVÁVEIS = %d, quero 1 (0 duplicados)", down.observableFor(key))
	}
	if down.materialized != 1 {
		t.Fatalf("materializações totais = %d, quero 1", down.materialized)
	}
	if string(res.Payload) != "done:body" {
		t.Fatalf("resultado inesperado: %q", res.Payload)
	}

	// 3.ª chamada: agora já commitado → already-applied, effect NÃO corre.
	if _, applied3, _ := ledger.Apply(ctx, key, effect); applied3 {
		t.Fatalf("3.ª chamada devia ser already-applied")
	}
	if down.callsFor(key) != 2 {
		t.Fatalf("effect não devia correr após commit, calls=%d", down.callsFor(key))
	}
}

// TestFaultInjectionCrashAfterCommit modela um crash APÓS o commit do ledger mas
// antes de o worker devolver o resultado. Um novo worker reconstrói do Event
// Store (Rebuild) e a chave já está aplicada → effect NÃO corre. 0 duplicados.
func TestFaultInjectionCrashAfterCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	real := newStore(t)
	ledger1, _ := NewStepLedger(real)

	down := newDownstream()
	key, _ := IdempotencyKey("run-ac", "step-000003")
	effect := func(context.Context) (Result, error) {
		out := down.apply(key, []byte("x"))
		return Result{Status: "ok", Payload: out}, nil
	}

	// Worker 1: aplica e COMMITA. Depois "crasha" (perdemos ledger1).
	if _, applied, err := ledger1.Apply(ctx, key, effect); err != nil || !applied {
		t.Fatalf("worker1 Apply: applied=%v err=%v", applied, err)
	}

	// Worker 2 (novo processo, estado zero): reconstrói do Event Store.
	ledger2, _ := NewStepLedger(real)
	if err := ledger2.Rebuild(ctx, "run-ac"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	res, applied, err := ledger2.Apply(ctx, key, effect)
	if err != nil {
		t.Fatalf("worker2 Apply: %v", err)
	}
	if applied {
		t.Fatalf("worker2 devia ver already-applied (wasApplied=false)")
	}
	if down.callsFor(key) != 1 {
		t.Fatalf("effect correu %d vezes após crash-pós-commit, quero 1", down.callsFor(key))
	}
	if down.observableFor(key) != 1 {
		t.Fatalf("efeitos observáveis = %d, quero 1", down.observableFor(key))
	}
	if string(res.Payload) != "done:x" {
		t.Fatalf("resultado reconstruído inesperado: %q", res.Payload)
	}
}

// TestLedgerSurvivesWorkerRestart é o teste de integração: o ledger persiste no
// Event Store real e o estado sobrevive ao reinício do worker (Rebuild reconstrói
// múltiplas entradas key→resultado).
func TestLedgerSurvivesWorkerRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore(t)
	ledger1, _ := NewStepLedger(store)
	seq := NewStepSequencer()
	const runID = "run-restart"

	// Aplica 3 passos lógicos distintos.
	want := map[string]string{}
	for turn := 1; turn <= 3; turn++ {
		key, _ := seq.Key(runID, turn)
		payload := []byte("r-" + seq.StepID(runID, turn))
		if _, applied, err := ledger1.Apply(ctx, key, func(context.Context) (Result, error) {
			return Result{Status: "ok", Payload: payload}, nil
		}); err != nil || !applied {
			t.Fatalf("turno %d Apply: applied=%v err=%v", turn, applied, err)
		}
		want[key] = string(payload)
	}

	// "Reinício": novo ledger, estado zero, reconstrói do log.
	ledger2, _ := NewStepLedger(store)
	if err := ledger2.Rebuild(ctx, runID); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	for key, wantPayload := range want {
		res, ok := ledger2.Applied(key)
		if !ok {
			t.Fatalf("key %q perdida após reinício", key)
		}
		if string(res.Payload) != wantPayload {
			t.Fatalf("key %q: resultado reconstruído %q, quero %q", key, res.Payload, wantPayload)
		}
		// E um Apply pós-reinício não re-corre o effect.
		var ran bool
		if _, applied, _ := ledger2.Apply(ctx, key, func(context.Context) (Result, error) {
			ran = true
			return Result{}, nil
		}); applied || ran {
			t.Fatalf("key %q: Apply pós-reinício não devia correr effect (applied=%v ran=%v)", key, applied, ran)
		}
	}

	// Rebuild de um run inexistente não é erro.
	if err := ledger2.Rebuild(ctx, "run-inexistente"); err != nil {
		t.Fatalf("Rebuild de stream vazio devia ser nil, veio %v", err)
	}
}

// TestApplyConcurrentSameKey verifica a corrida: N goroutines aplicam a MESMA
// key concorrentemente; exactamente uma materializa (wasApplied=true) e todas
// devolvem o resultado idêntico. Alvo do -race.
func TestApplyConcurrentSameKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger, _ := NewStepLedger(newStore(t))
	key, _ := IdempotencyKey("run-conc", "step-000001")

	const n = 16
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		appliedN int
		effectN  int
		payloads = map[string]struct{}{}
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, applied, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
				mu.Lock()
				effectN++
				mu.Unlock()
				return Result{Status: "ok", Payload: []byte("canonical")}, nil
			})
			if err != nil {
				t.Errorf("Apply: %v", err)
				return
			}
			mu.Lock()
			if applied {
				appliedN++
			}
			payloads[string(res.Payload)] = struct{}{}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if appliedN != 1 {
		t.Fatalf("exactamente 1 goroutine devia materializar, veio %d", appliedN)
	}
	if len(payloads) != 1 {
		t.Fatalf("todas as goroutines deviam ver o MESMO resultado, veio %d distintos", len(payloads))
	}
	// O effect pode correr >1x (at-least-once), mas o ledger só regista 1.
	if _, ok := ledger.Applied(key); !ok {
		t.Fatalf("key devia estar aplicada")
	}
}

// TestLedgerEventShapeAndProducer verifica que o evento durável do ledger carrega
// o produtor injectado, o tipo canónico e a idempotency_key NAMESPACED
// (run_id:ledger-step_id) — distinta da chave lógica do passo, para não colidir
// com o turn.recorded homónimo no Event Store.
func TestLedgerEventShapeAndProducer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore(t)
	producer := eventstore.Producer{NHIID: "nhi:agent-1", Scope: []string{"scope:x"}}
	ledger, _ := NewStepLedger(store, WithProducer(producer))

	key, _ := IdempotencyKey("run-shape", "step-000001")
	if _, applied, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("p")}, nil
	}); err != nil || !applied {
		t.Fatalf("Apply: applied=%v err=%v", applied, err)
	}

	events, err := store.Read(ctx, "run-shape", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("esperava 1 evento, veio %d", len(events))
	}
	e := events[0]
	if e.Type != EventTypeLedgerApplied {
		t.Fatalf("tipo = %q, quero %q", e.Type, EventTypeLedgerApplied)
	}
	if e.Producer.NHIID != "nhi:agent-1" {
		t.Fatalf("produtor não propagado: %+v", e.Producer)
	}
	if e.IdempotencyKey != "run-shape:ledger-step-000001" {
		t.Fatalf("idempotency_key = %q, quero namespaced run-shape:ledger-step-000001", e.IdempotencyKey)
	}
	// A chave namespaced é DISTINTA da chave lógica do passo (sem colisão com o turno).
	if e.IdempotencyKey == key {
		t.Fatalf("chave do ledger colide com a chave lógica do passo %q", key)
	}
}

// TestApplyValidatesInput cobre os guardas de Apply.
func TestApplyValidatesInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger, _ := NewStepLedger(newStore(t))
	if _, _, err := ledger.Apply(ctx, "run:step", nil); !errors.Is(err, ErrNilEffect) {
		t.Fatalf("effect nil devia dar ErrNilEffect, veio %v", err)
	}
	if _, _, err := ledger.Apply(ctx, "malformada", func(context.Context) (Result, error) {
		return Result{}, nil
	}); !errors.Is(err, ErrMalformedKey) {
		t.Fatalf("key mal-formada devia dar ErrMalformedKey, veio %v", err)
	}
	if _, err := NewStepLedger(nil); !errors.Is(err, ErrNilStore) {
		t.Fatalf("store nil devia dar ErrNilStore, veio %v", err)
	}
}

// TestApplyCrossWorkerDuplicateReturnsCanonical cobre o caminho HONESTO
// restart-SEM-Rebuild / cross-worker (finding contract-clarity): um worker novo
// (ledger distinto, estado in-memory vazio, sem Rebuild) volta a correr o effect
// (at-least-once), mas o commit do ES devolve StatusDuplicate e Apply devolve o
// resultado CANÓNICO do vencedor — idêntico independentemente de quem ganhou.
func TestApplyCrossWorkerDuplicateReturnsCanonical(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore(t)
	key, _ := IdempotencyKey("run-xw", "step-000001")

	// Worker 1 aplica e commita o canónico.
	l1, _ := NewStepLedger(store)
	res1, applied1, err := l1.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("canonical")}, nil
	})
	if err != nil || !applied1 {
		t.Fatalf("worker1: applied=%v err=%v", applied1, err)
	}

	// Worker 2: novo ledger (records vazio), SEM Rebuild → corre o effect e bate no
	// ES, que deduplica (StatusDuplicate). O effect DIVERGENTE é ignorado: vence o
	// canónico do worker 1.
	l2, _ := NewStepLedger(store)
	var ran2 bool
	res2, applied2, err := l2.Apply(ctx, key, func(context.Context) (Result, error) {
		ran2 = true
		return Result{Status: "ok", Payload: []byte("payload-divergente")}, nil
	})
	if err != nil {
		t.Fatalf("worker2: %v", err)
	}
	if applied2 {
		t.Fatalf("worker2 devia ver StatusDuplicate (wasApplied=false)")
	}
	if !ran2 {
		t.Fatalf("worker2 SEM Rebuild corre o effect (at-least-once) antes da dedup no commit")
	}
	if string(res2.Payload) != "canonical" || string(res1.Payload) != string(res2.Payload) {
		t.Fatalf("resultado não canónico: w1=%q w2=%q", res1.Payload, res2.Payload)
	}
	// l2 memoriza o canónico: um novo Apply é already-applied, sem correr o effect.
	var ran3 bool
	if _, applied3, _ := l2.Apply(ctx, key, func(context.Context) (Result, error) {
		ran3 = true
		return Result{}, nil
	}); applied3 || ran3 {
		t.Fatalf("terceiro Apply devia ser already-applied (applied=%v ran=%v)", applied3, ran3)
	}
}

// TestApplyRejectsReservedStepID cobre o fecho da colisão de namespace (finding
// key-collision): um step_id de negócio no prefixo reservado "ledger-" colidiria,
// na dedup GLOBAL do ES, com o registo do ledger homónimo — logo Apply recusa-o
// ANTES de qualquer efeito.
func TestApplyRejectsReservedStepID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger, _ := NewStepLedger(newStore(t))
	key, _ := IdempotencyKey("run-r", "ledger-step-000001")
	var ran bool
	_, applied, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		ran = true
		return Result{Status: "ok"}, nil
	})
	if !errors.Is(err, ErrReservedStepID) {
		t.Fatalf("step_id no namespace reservado devia dar ErrReservedStepID, veio %v", err)
	}
	if applied || ran {
		t.Fatalf("não devia correr o effect nem aplicar (applied=%v ran=%v)", applied, ran)
	}
	if _, ok := ledger.Applied(key); ok {
		t.Fatalf("recusa não devia deixar registo")
	}
}

// TestSensitiveResultsGuard cobre a guarda de segredos opt-in (WithSensitiveResults):
// em modo sensível um Payload em claro é recusado; uma referência marcada ou um
// Payload vazio são aceites.
func TestSensitiveResultsGuard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger, _ := NewStepLedger(newStore(t), WithSensitiveResults())

	// (a) Payload em claro (não-referência) é RECUSADO, sem deixar registo.
	key, _ := IdempotencyKey("run-sens", "step-000001")
	if _, applied, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("segredo-em-claro")}, nil
	}); !errors.Is(err, ErrClearResultInSensitiveMode) || applied {
		t.Fatalf("payload em claro devia ser recusado (applied=%v err=%v)", applied, err)
	}
	if _, ok := ledger.Applied(key); ok {
		t.Fatalf("recusa não devia deixar registo")
	}

	// (b) Uma REFERÊNCIA marcada é aceite.
	if _, applied, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("sha256:deadbeef"), Reference: true}, nil
	}); err != nil || !applied {
		t.Fatalf("referência marcada devia ser aceite (applied=%v err=%v)", applied, err)
	}

	// (c) Um Payload VAZIO é aceite (nada sensível a memorizar).
	key2, _ := IdempotencyKey("run-sens", "step-000002")
	if _, applied, err := ledger.Apply(ctx, key2, func(context.Context) (Result, error) {
		return Result{Status: "ok"}, nil
	}); err != nil || !applied {
		t.Fatalf("payload vazio devia ser aceite (applied=%v err=%v)", applied, err)
	}
}

// TestApplyDuplicateWithCorruptCanonicalPayload cobre o ramo StatusDuplicate com
// o registo canónico do vencedor corrompido: a reconstrução do resultado do
// vencedor (cross-worker) propaga o erro sem panic nem estado parcial.
func TestApplyDuplicateWithCorruptCanonicalPayload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := &stubStore{appendResult: eventstore.AppendResult{
		Status: eventstore.StatusDuplicate,
		Event:  eventstore.Event{Payload: []byte("{corrompido")},
	}}
	ledger, _ := NewStepLedger(st)
	key, _ := IdempotencyKey("run-dup", "step-000001")
	_, applied, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("p")}, nil
	})
	if err == nil {
		t.Fatalf("esperava erro de desserialização do registo canónico do vencedor")
	}
	if applied {
		t.Fatalf("não devia aplicar em erro de decode")
	}
	if _, ok := ledger.Applied(key); ok {
		t.Fatalf("erro de decode não devia deixar registo")
	}
}

// TestRebuildSkipsAndPropagatesDecodeError cobre, em Rebuild, o salto de eventos
// não-ledger (continue) e a propagação de erro num evento step.ledger.applied
// corrompido.
func TestRebuildSkipsAndPropagatesDecodeError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := &stubStore{readEvents: []eventstore.Event{
		{Type: "turn.recorded", Payload: []byte(`{"ignore":true}`)}, // salto (continue)
		{Type: EventTypeLedgerApplied, Payload: []byte("{corrompido")},
	}}
	ledger, _ := NewStepLedger(st)
	if err := ledger.Rebuild(ctx, "run-x"); err == nil {
		t.Fatalf("esperava erro de decode num evento step.ledger.applied corrompido")
	}
}

// TestApplySingleFlightCollapsesConcurrent prova o single-flight por-key (finding
// contract-clarity): com o líder bloqueado no effect, todos os seguidores da MESMA
// key encontram o registo in-flight e partilham o resultado canónico — o effect
// corre EXACTAMENTE uma vez dentro do processo.
func TestApplySingleFlightCollapsesConcurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger, _ := NewStepLedger(newStore(t))
	key, _ := IdempotencyKey("run-sf", "step-000001")

	var (
		mu      sync.Mutex
		effectN int
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	effect := func(context.Context) (Result, error) {
		mu.Lock()
		effectN++
		mu.Unlock()
		once.Do(func() { close(entered) })
		<-release
		return Result{Status: "ok", Payload: []byte("canonical")}, nil
	}

	const n = 8
	results := make([]Result, n)
	appliedFlags := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); results[0], appliedFlags[0], errs[0] = ledger.Apply(ctx, key, effect) }()
	<-entered // o líder está DENTRO do effect ⇒ inflight[key] registado e a persistir.
	for i := 1; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], appliedFlags[i], errs[i] = ledger.Apply(ctx, key, effect) }(i)
	}
	time.Sleep(50 * time.Millisecond) // deixa os seguidores parquearem em <-call.done
	close(release)
	wg.Wait()

	if effectN != 1 {
		t.Fatalf("single-flight: effect devia correr 1x, correu %d", effectN)
	}
	appliedCount := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Apply[%d] erro inesperado: %v", i, errs[i])
		}
		if appliedFlags[i] {
			appliedCount++
		}
		if string(results[i].Payload) != "canonical" {
			t.Fatalf("Apply[%d] resultado divergente: %q", i, results[i].Payload)
		}
	}
	if appliedCount != 1 {
		t.Fatalf("exactamente 1 devia materializar (wasApplied=true), veio %d", appliedCount)
	}
}

// TestApplySingleFlightPropagatesLeaderError cobre o ramo em que o líder do
// single-flight erra: os seguidores propagam o mesmo erro (nada fica registado) e
// o retry converge.
func TestApplySingleFlightPropagatesLeaderError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger, _ := NewStepLedger(newStore(t))
	key, _ := IdempotencyKey("run-sfe", "step-000001")

	boom := errors.New("boom-single-flight")
	var (
		mu      sync.Mutex
		effectN int
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	effect := func(context.Context) (Result, error) {
		mu.Lock()
		effectN++
		mu.Unlock()
		once.Do(func() { close(entered) })
		<-release
		return Result{}, boom
	}

	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _, errs[0] = ledger.Apply(ctx, key, effect) }()
	<-entered
	for i := 1; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, _, errs[i] = ledger.Apply(ctx, key, effect) }(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if effectN != 1 {
		t.Fatalf("single-flight: effect devia correr 1x mesmo em erro, correu %d", effectN)
	}
	for i := 0; i < n; i++ {
		if !errors.Is(errs[i], boom) {
			t.Fatalf("Apply[%d] devia propagar o erro do líder, veio %v", i, errs[i])
		}
	}
	// Erro não deixa registo ⇒ retry converge.
	if _, applied, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok"}, nil
	}); err != nil || !applied {
		t.Fatalf("retry pós-erro devia aplicar (applied=%v err=%v)", applied, err)
	}
}
