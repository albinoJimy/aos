package harness

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/saga"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// EPIC-11 · AOS-112 — suite de IDEMPOTÊNCIA POR PASSO sobre uma activity de
// DOMÍNIO com efeito externo observável. CONSOME os primitivos já Done — o
// step-ledger de AOS-014 ([durable.StepLedger]), o fencing de AOS-018
// ([durable.FencedAppender]/[durable.LeaseManager]) e a saga de AOS-020
// ([saga.SagaCoordinator]) — e os helpers EXPORTADOS do harness
// ([NewDomainEffect]/[DriveEffectSchedule]/[VerifyEffectIdempotency]). NÃO
// reimplementa nenhuma dessas mecânicas (precedente AOS-111).
//
// A suite usa eventstore.New() — o MESMO Event Store de referência que o gate 8
// corre — para provar as propriedades sobre o store durável real, não um duplo.
// O ambiente efémero de AOS-110 é o equivalente de produção-de-teste (satisfaz
// durable.EventStore/TokenSource); a suite fica no harness para ser apanhada pelo
// gate 8 com -race.
// ---------------------------------------------------------------------------

func domainStore(t *testing.T) *eventstore.Store {
	t.Helper()
	st, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ---------------------------------------------------------------------------
// AC1 + AC2 — exactamente-uma-vez sob retry com a MESMA f(run_id, step_id), com um
// CRASH injectado a meio (falha após o efeito, antes do commit) reexecutado sem
// duplicar o efeito.
// ---------------------------------------------------------------------------

// TestDomainEffectExactlyOnceUnderRetry prova o cerne de AOS-112: uma activity de
// domínio com um efeito externo OBSERVÁVEL (uma escrita idempotente) submetida ao
// calendário at-least-once com CRASH intercalado (worker A corre o efeito e regista;
// crash; worker B novo reconstrói do log e reexecuta; retry no mesmo worker) corre
// EXACTAMENTE UMA VEZ — Observed()==1 — porque a idempotency key f(run_id, step_id) é
// estável entre tentativas e o ledger de AOS-014 deduplica. Determinista.
func TestDomainEffectExactlyOnceUnderRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := domainStore(t)
	const runID = "run-domain-exactly-once"

	// (1) Via o helper de UMA CHAMADA que qualquer ticket reutiliza (AC5).
	eff := NewDomainEffect(runID, "activity-write")
	observed, err := VerifyEffectIdempotency(ctx, runID, store, eff)
	if err != nil {
		t.Fatalf("VerifyEffectIdempotency: %v", err)
	}
	if observed != 1 {
		t.Fatalf("efeito de domínio correu %d vezes sob retry+crash, esperado 1 (exactamente-uma-vez)", observed)
	}

	// (2) Um NOVO efeito com a MESMA key, sobre o MESMO store, já encontra o registo
	// durável (a 1.ª aplicação foi commitada): o crash-schedule deduplica-o em TODAS as
	// tentativas — o efeito observável NÃO corre de novo (0), prova da dedup durável.
	effAgain := NewDomainEffect(runID, "activity-write")
	if err := DriveEffectSchedule(ctx, runID, store, effAgain); err != nil {
		t.Fatalf("DriveEffectSchedule (reexecução durável): %v", err)
	}
	if got := effAgain.Observed(); got != 0 {
		t.Fatalf("reexecução com key já commitada correu %d vezes, esperado 0 (dedup durável)", got)
	}

	// (3) CONTROLO NEGATIVO: uma key NÃO-determinística (varia com a tentativa) falha a
	// deduplicar — o efeito corre nas 3 tentativas. Prova que a garantia vem da chave
	// canónica, não do acaso.
	brokenCount := 0
	broken := Effect{
		StepID: "activity-broken",
		KeyAt: func(attempt int) (string, error) {
			return durable.IdempotencyKey(runID, "activity-broken-try-"+string(rune('0'+attempt)))
		},
		Run: func(context.Context) (durable.Result, error) {
			brokenCount++
			return durable.Result{Status: "ok"}, nil
		},
		Observed: func() int { return brokenCount },
	}
	if err := DriveEffectSchedule(ctx, runID, store, broken); err != nil {
		t.Fatalf("DriveEffectSchedule (key não-determinística): %v", err)
	}
	if brokenCount != 3 {
		t.Fatalf("efeito com key não-determinística correu %d vezes, esperado 3 (dedup falha por design)", brokenCount)
	}
}

// ---------------------------------------------------------------------------
// AC3 — FENCING: uma escrita de worker OBSOLETO (token inferior) é REJEITADA sem
// corromper o estado, coerente com a liveness por lease/fencing token de AOS-018.
// COMPÕE durable.LeaseManager + durable.FencedAppender com o efeito de domínio; não
// reimplementa o fencing (o padrão provado em durable/fencing_test.go).
// ---------------------------------------------------------------------------

// manualClock é um relógio determinístico avançável (satisfaz [durable.Clock]) — sem
// sleeps, para exercitar a expiração do lease de forma reprodutível.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock { return &manualClock{t: time.Unix(fixtureEpochUnix, 0).UTC()} }

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

// domainEffectWrite é a materialização durável do efeito externo de domínio no stream de
// negócio do run — o análogo, na suite de idempotência, do businessWrite de AOS-018. É o
// que se encaminha pelo [durable.FencedAppender] para que só o detentor do lease o escreva.
func domainEffectWrite(runID, stepID string) eventstore.EventInput {
	return eventstore.EventInput{
		Type:    "aos.domain.effect",
		Payload: []byte(`{"effect":"` + stepID + `"}`),
		RunID:   runID,
		StepID:  stepID,
	}
}

// committedDomainEffects conta os efeitos de domínio efectivamente materializados no
// stream de negócio do run — a base para provar zero duplicação sob fencing.
func committedDomainEffects(t *testing.T, store *eventstore.Store, runID string) int {
	t.Helper()
	evs, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return 0
		}
		t.Fatalf("Read stream de negócio: %v", err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == "aos.domain.effect" {
			n++
		}
	}
	return n
}

// TestDomainFencingRejectsStaleWrite prova o AC3 na suite de idempotência de domínio: o
// efeito externo de uma activity, encaminhado pelo [durable.FencedAppender] sob o lease
// de AOS-018, só se materializa se o worker detiver o token corrente. Worker A (token 1)
// escreve o efeito; A fica lento e o lease expira; B reclama (token 2); A "acorda" e
// tenta re-escrever o efeito com o token OBSOLETO → [durable.ErrStaleFencingToken], SEM
// tocar no Event Store; B escreve com o token corrente. Só os efeitos legítimos ficam —
// zero duplicação, estado não corrompido.
func TestDomainFencingRejectsStaleWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := domainStore(t)
	clk := newManualClock()
	const runID = "run-domain-fencing"
	const ttl = 30 * time.Second

	lm, err := durable.NewLeaseManager(store, ttl, durable.WithLeaseClock(clk))
	if err != nil {
		t.Fatalf("NewLeaseManager: %v", err)
	}
	fa, err := durable.NewFencedAppender(store, lm)
	if err != nil {
		t.Fatalf("NewFencedAppender: %v", err)
	}

	// A reclama (token 1) e escreve o efeito da activity — aceite (token == corrente).
	a, err := lm.Claim(ctx, runID)
	if err != nil {
		t.Fatalf("Claim A: %v", err)
	}
	if _, err := fa.Append(ctx, runID, a.Token, domainEffectWrite(runID, "activity-1")); err != nil {
		t.Fatalf("escrita de A (token corrente) = %v, quer nil", err)
	}

	// A fica lento; o lease expira; B reclama (token 2 > 1).
	clk.Advance(ttl + time.Nanosecond)
	b, err := lm.Claim(ctx, runID)
	if err != nil {
		t.Fatalf("Claim B: %v", err)
	}
	if b.Token.Value() <= a.Token.Value() {
		t.Fatalf("token de B (%d) não é > token de A (%d)", b.Token.Value(), a.Token.Value())
	}

	// A, obsoleto, tenta re-materializar o efeito com o token antigo — com um step_id
	// DISTINTO (não-idempotente com o de B, logo invisível à dedup do ES): só o fencing o
	// pode barrar → ErrStaleFencingToken, sem tocar no Event Store.
	if _, err := fa.Append(ctx, runID, a.Token, domainEffectWrite(runID, "activity-1-retryA")); !errors.Is(err, durable.ErrStaleFencingToken) {
		t.Fatalf("escrita obsoleta de A = %v, quer ErrStaleFencingToken", err)
	}

	// B escreve o efeito seguinte com o token corrente → aceite.
	if _, err := fa.Append(ctx, runID, b.Token, domainEffectWrite(runID, "activity-2")); err != nil {
		t.Fatalf("escrita de B (token corrente) = %v, quer nil", err)
	}

	// Só os efeitos legítimos de A e B se materializaram (2); a escrita obsoleta de A NÃO
	// corrompeu o estado nem duplicou o efeito.
	if got := committedDomainEffects(t, store, runID); got != 2 {
		t.Fatalf("efeitos materializados = %d, quer 2 (activity-1, activity-2; sem duplicado obsoleto)", got)
	}
}

// ---------------------------------------------------------------------------
// AC4 — SAGA DE COMPENSAÇÃO: um passo falhado recuperável compensa e permite retry
// idempotente (failed → compensating → ready), sem duplicar o efeito inverso. COMPÕE
// saga.SagaCoordinator + saga.CompensationRegistry + state.Machine (real) + o ledger de
// AOS-014; não reimplementa a saga.
// ---------------------------------------------------------------------------

// TestDomainSagaCompensatesAndRetriesIdempotent prova o AC4 com os primitivos reais: um
// run de domínio chega a failed (ready→running→failed); a saga compensa por ordem inversa
// idempotente e transita failed→compensating→ready (retry limpo). Depois:
//   - a compensação é IDEMPOTENTE: reaplicar a MESMA chave de compensação pelo ledger
//     devolve wasApplied=false e NÃO re-corre o efeito inverso (0 duplicados);
//   - o run recuperado permite RETRY LIMPO: um novo claim ready→running (token maior)
//     é aceite, e o estado é reconstruível por replay (Rebuild → ready).
func TestDomainSagaCompensatesAndRetriesIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := domainStore(t)
	const runID = "run-domain-saga"

	m, err := state.NewMachine(store, runID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	// ready → running (claim com fencing token) → failed (erro recuperável).
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("ready→running: %v", err)
	}
	if err := m.Transition(ctx, state.Failed, state.TransitionEvent{Reason: "efeito parcial recuperável"}); err != nil {
		t.Fatalf("running→failed: %v", err)
	}

	// A compensação da activity de domínio: um efeito inverso OBSERVÁVEL (contador).
	// A Action também captura o estado da máquina NO MOMENTO em que corre — torna a
	// passagem pelo estado transitório 'compensating' EXPLICITAMENTE visível nesta suite
	// (o coordinator real transita failed→compensating ANTES de correr as compensações e
	// só depois compensating→ready), em vez de a deixar implícita por construção.
	var reverted int
	var stateDuringComp state.State
	reg := saga.NewCompensationRegistry()
	if err := reg.Register(saga.Compensation{
		StepID: "activity-1",
		Action: func(context.Context) error { reverted++; stateDuringComp = m.Current(); return nil },
		Reason: "reverter escrita parcial",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ledger, err := durable.NewStepLedger(store)
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	coord, err := saga.NewSagaCoordinator(m, ledger, reg)
	if err != nil {
		t.Fatalf("NewSagaCoordinator: %v", err)
	}

	// Compensa: failed → compensating → ready.
	if err := coord.Compensate(ctx); err != nil {
		t.Fatalf("Compensate: %v", err)
	}
	// A passagem pelo estado transitório 'compensating' é EXPLÍCITA: a compensação correu
	// com a máquina em compensating (failed→compensating aconteceu antes do efeito inverso).
	if stateDuringComp != state.Compensating {
		t.Fatalf("estado durante a compensação = %s, esperado compensating (failed→compensating→ready)", stateDuringComp)
	}
	if m.Current() != state.Ready {
		t.Fatalf("estado após compensação = %s, esperado ready (retry limpo)", m.Current())
	}
	if reverted != 1 {
		t.Fatalf("efeito inverso correu %d vezes, esperado 1", reverted)
	}

	// IDEMPOTÊNCIA da compensação: reaplicar a MESMA chave de compensação (run:comp-…)
	// por um ledger reconstruído do log é DEDUPLICADO — a reversão NÃO corre de novo.
	compKey, err := saga.CompensationKey(runID, "activity-1")
	if err != nil {
		t.Fatalf("CompensationKey: %v", err)
	}
	l2, err := durable.NewStepLedger(store)
	if err != nil {
		t.Fatalf("NewStepLedger (l2): %v", err)
	}
	if err := l2.Rebuild(ctx, runID); err != nil {
		t.Fatalf("Rebuild l2: %v", err)
	}
	_, wasApplied, err := l2.Apply(ctx, compKey, func(context.Context) (durable.Result, error) {
		reverted++ // NÃO deve correr: a chave já foi commitada.
		return durable.Result{Status: saga.StatusCompensated}, nil
	})
	if err != nil {
		t.Fatalf("Apply (reexecução da compensação): %v", err)
	}
	if wasApplied {
		t.Fatalf("reaplicar a compensação devia ser deduplicado (wasApplied=false)")
	}
	if reverted != 1 {
		t.Fatalf("efeito inverso correu %d vezes após reexecução, esperado 1 (0 duplicados)", reverted)
	}

	// RETRY LIMPO: o run recuperado é reconstruível (ready) e reclamável de novo
	// (ready→running com um fencing token MAIOR).
	m2, err := state.NewMachine(store, runID)
	if err != nil {
		t.Fatalf("NewMachine (m2): %v", err)
	}
	st, err := m2.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild m2: %v", err)
	}
	if st != state.Ready {
		t.Fatalf("Rebuild = %s, esperado ready (a saga é durável e reconstruível)", st)
	}
	if err := m2.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(2)}); err != nil {
		t.Fatalf("retry ready→running (token novo): %v", err)
	}
}
