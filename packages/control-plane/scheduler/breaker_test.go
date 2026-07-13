package scheduler_test

// Testes do circuit breaker de orçamento (AOS-029). Todos deterministas: relógio
// injectável (reutiliza fixedClock/mutClock de admission_test.go, mesmo package de
// teste), sem time.Now nem rand no caminho de decisão. Cobrem os Testes Requeridos:
//   - unit: trip por VELOCIDADE e por ESGOTAMENTO; transições closed/open/half-open;
//   - integração: trip pausa a árvore SEM efeitos duplicados (Machine do AOS-017);
//   - integração: retoma em half-open NÃO re-executa passos concluídos (ledger/replay);
//   - integração: aviso a ~80% PRECEDE o trip; limiares por classe/tenant respeitados;
//   - trip é fail-closed para o consumo (na dúvida, pára).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/scheduler"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Harness de teste.
// ---------------------------------------------------------------------------

// newTree constrói um orçamento hierárquico (AOS-026/008) com um nó raiz treeID e o
// limite dado. É a fonte REAL do remanescente/consumo que o breaker lê.
func newTree(t *testing.T, treeID string, limit budget.Amount) *budget.Budget {
	t.Helper()
	b, err := budget.New(treeID, limit)
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	return b
}

// consume reserva+consolida amt no nó (reduz o Available de forma durável), simulando
// o burn-down real da árvore.
func consume(t *testing.T, b *budget.Budget, node string, amt budget.Amount) {
	t.Helper()
	ctx := context.Background()
	r, err := b.Reserve(ctx, node, amt)
	if err != nil {
		t.Fatalf("budget.Reserve(%v): %v", amt, err)
	}
	if err := b.Commit(ctx, r); err != nil {
		t.Fatalf("budget.Commit: %v", err)
	}
}

// newBreaker constrói um breaker de teste sobre um Event Store real (AOS-002) e um
// orçamento real.
func newBreaker(t *testing.T, es *eventstore.Store, b *budget.Budget, treeID string, th scheduler.Thresholds, opts ...scheduler.BreakerOption) *scheduler.Breaker {
	t.Helper()
	br, err := scheduler.NewBreaker(es, b, treeID, th, opts...)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	return br
}

func newES(t *testing.T) *eventstore.Store {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	return es
}

// bigLimit é um limite folgado (nem esgotamento nem aviso interferem com o teste de
// velocidade).
var bigLimit = budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000}

// ---------------------------------------------------------------------------
// Unit: trip por VELOCIDADE.
// ---------------------------------------------------------------------------

func TestBreaker_TripByVelocity(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	b := newTree(t, "tree-vel", bigLimit)
	th := scheduler.Thresholds{
		VelocityTokens: 1000,
		Window:         time.Minute,
		// esgotamento desligado na prática (margem 0 e orçamento folgado).
	}
	br := newBreaker(t, es, b, "tree-vel", th, scheduler.WithBreakerClock(fixedClock(base)))
	ctx := context.Background()

	// Duas amostras de 600 tokens no MESMO instante (janela): soma 1200 > 1000 ⇒ trip.
	if st, err := br.Observe(ctx, budget.Amount{Tokens: 600}); err != nil || st != scheduler.BreakerClosed {
		t.Fatalf("Observe#1 = (%s,%v), quero (closed,nil)", st, err)
	}
	st, err := br.Observe(ctx, budget.Amount{Tokens: 600})
	if err != nil {
		t.Fatalf("Observe#2: %v", err)
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado = %s, quero open (trip por velocidade)", st)
	}
	// O evento de trip regista o motivo velocidade.
	recs, err := br.Replay(ctx)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	last := recs[len(recs)-1]
	if last.Type != scheduler.EventBudgetBreakerTripped || last.Reason != string(scheduler.ReasonVelocity) {
		t.Fatalf("último evento = (%s,%s), quero (tripped,velocity)", last.Type, last.Reason)
	}
}

// A velocidade RESPEITA a janela deslizante: amostras fora da janela não somam, logo
// não disparam (relógio injectável — prova de determinismo do sinal temporizado).
func TestBreaker_VelocityWindowSlides(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(2_000_000, 0))
	es := newES(t)
	b := newTree(t, "tree-slide", bigLimit)
	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute}
	br := newBreaker(t, es, b, "tree-slide", th, scheduler.WithBreakerClock(clk.now))
	ctx := context.Background()

	if _, err := br.Observe(ctx, budget.Amount{Tokens: 600}); err != nil {
		t.Fatalf("Observe#1: %v", err)
	}
	// Avança para além da janela: a 1ª amostra expira.
	clk.advance(2 * time.Minute)
	st, err := br.Observe(ctx, budget.Amount{Tokens: 600})
	if err != nil {
		t.Fatalf("Observe#2: %v", err)
	}
	if st != scheduler.BreakerClosed {
		t.Fatalf("estado = %s, quero closed (amostra antiga expirou, sem trip)", st)
	}
}

// ---------------------------------------------------------------------------
// Unit: trip por ESGOTAMENTO.
// ---------------------------------------------------------------------------

func TestBreaker_TripByExhaustion(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	b := newTree(t, "tree-exh", budget.Amount{Tokens: 1000, CostMicroUSD: 1000})
	th := scheduler.Thresholds{
		ExhaustionMargin: budget.Amount{Tokens: 100, CostMicroUSD: 100},
		// velocidade desligada (Window 0).
	}
	br := newBreaker(t, es, b, "tree-exh", th, scheduler.WithBreakerClock(fixedClock(base)))
	ctx := context.Background()

	// Consome até o remanescente cair para 50/50 (<= margem 100) ⇒ trip por esgotamento.
	consume(t, b, "tree-exh", budget.Amount{Tokens: 950, CostMicroUSD: 950})
	st, err := br.Observe(ctx, budget.Amount{Tokens: 950, CostMicroUSD: 950})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado = %s, quero open (trip por esgotamento)", st)
	}
	recs, _ := br.Replay(ctx)
	last := recs[len(recs)-1]
	if last.Reason != string(scheduler.ReasonExhaustion) {
		t.Fatalf("motivo = %s, quero exhaustion", last.Reason)
	}
	// O evento carrega o estado de orçamento no momento (remanescente 50/50).
	if last.AvailTokens != 50 || last.AvailCost != 50 {
		t.Fatalf("avail no evento = %d/%d, quero 50/50", last.AvailTokens, last.AvailCost)
	}
}

// ---------------------------------------------------------------------------
// Unit: transições closed/open/half-open completas.
// ---------------------------------------------------------------------------

func TestBreaker_FullStateCycle(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(3_000_000, 0))
	es := newES(t)
	b := newTree(t, "tree-cyc", bigLimit)
	th := scheduler.Thresholds{
		VelocityTokens: 1000,
		Window:         time.Minute,
		Cooldown:       30 * time.Second,
	}
	br := newBreaker(t, es, b, "tree-cyc", th, scheduler.WithBreakerClock(clk.now))
	ctx := context.Background()

	// closed → open (trip por velocidade).
	if _, err := br.Observe(ctx, budget.Amount{Tokens: 1500}); err != nil {
		t.Fatalf("trip: %v", err)
	}
	if br.State() != scheduler.BreakerOpen {
		t.Fatalf("estado = %s, quero open", br.State())
	}
	// open: consumo NEGADO (fail-closed) antes do cooldown.
	if ok, st, _ := br.Allow(ctx); ok || st != scheduler.BreakerOpen {
		t.Fatalf("Allow antes do cooldown = (%v,%s), quero (false,open)", ok, st)
	}
	// Cooldown decorre ⇒ open → half_open (retoma controlada; Allow concede o probe).
	clk.advance(30 * time.Second)
	ok, st, err := br.Allow(ctx)
	if err != nil || !ok || st != scheduler.BreakerHalfOpen {
		t.Fatalf("Allow após cooldown = (%v,%s,%v), quero (true,half_open,nil)", ok, st, err)
	}
	// half_open → closed: um Observe recuperado (velocidade normal, janela vazia).
	clk.advance(2 * time.Minute) // limpa a janela de velocidade
	st2, err := br.Observe(ctx, budget.Amount{Tokens: 1})
	if err != nil {
		t.Fatalf("Observe recuperado: %v", err)
	}
	if st2 != scheduler.BreakerClosed {
		t.Fatalf("estado = %s, quero closed (probe recuperado)", st2)
	}
	// Segundo ciclo: half_open → open (re-trip) — provar a aresta half_open→open.
	// Reabre por velocidade, volta a open, cooldown, e re-dispara no probe.
	if _, err := br.Observe(ctx, budget.Amount{Tokens: 1500}); err != nil { // closed→open
		t.Fatalf("re-trip setup: %v", err)
	}
	clk.advance(30 * time.Second)
	if ok, st, _ := br.Allow(ctx); !ok || st != scheduler.BreakerHalfOpen { // open→half_open
		t.Fatalf("reabertura = (%v,%s), quero (true,half_open)", ok, st)
	}
	// Probe ainda anómalo (janela mantém 1500) ⇒ half_open → open.
	st3, err := br.Observe(ctx, budget.Amount{Tokens: 1})
	if err != nil {
		t.Fatalf("probe re-trip: %v", err)
	}
	if st3 != scheduler.BreakerOpen {
		t.Fatalf("estado = %s, quero open (half_open→open)", st3)
	}
}

// ---------------------------------------------------------------------------
// Fail-closed: trip PÁRA o consumo (na dúvida, pára).
// ---------------------------------------------------------------------------

func TestBreaker_TripIsFailClosedForConsumption(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	b := newTree(t, "tree-fc", bigLimit)
	// Cooldown 0 ⇒ NUNCA reabre automaticamente: uma vez open, o consumo fica negado.
	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute, Cooldown: 0}
	br := newBreaker(t, es, b, "tree-fc", th, scheduler.WithBreakerClock(fixedClock(base)))
	ctx := context.Background()

	// Antes do trip: consumo permitido.
	if ok, _, _ := br.Allow(ctx); !ok {
		t.Fatalf("Allow closed = false, quero true")
	}
	// Trip.
	if _, err := br.Observe(ctx, budget.Amount{Tokens: 1500}); err != nil {
		t.Fatalf("trip: %v", err)
	}
	// Depois do trip, com cooldown 0: consumo SEMPRE negado (fail-closed).
	for i := 0; i < 3; i++ {
		if ok, st, _ := br.Allow(ctx); ok || st != scheduler.BreakerOpen {
			t.Fatalf("Allow#%d após trip = (%v,%s), quero (false,open)", i, ok, st)
		}
	}
}

// ---------------------------------------------------------------------------
// Integração: aviso a ~80% PRECEDE o trip.
// ---------------------------------------------------------------------------

func TestBreaker_Warning80PrecedesTrip(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	b := newTree(t, "tree-warn", budget.Amount{Tokens: 1000, CostMicroUSD: 1000})
	th := scheduler.Thresholds{
		WarnFraction:     0.8,
		ExhaustionMargin: budget.Amount{}, // trip só a 0
	}
	br := newBreaker(t, es, b, "tree-warn", th, scheduler.WithBreakerClock(fixedClock(base)))
	ctx := context.Background()

	// Consome 850/1000 ⇒ fracção 0.85 >= 0.8: AVISO, sem trip (remanescente 150 > 0).
	consume(t, b, "tree-warn", budget.Amount{Tokens: 850, CostMicroUSD: 850})
	st, err := br.Observe(ctx, budget.Amount{Tokens: 850, CostMicroUSD: 850})
	if err != nil {
		t.Fatalf("Observe aviso: %v", err)
	}
	if st != scheduler.BreakerClosed {
		t.Fatalf("estado após 85%% = %s, quero closed (só aviso)", st)
	}
	// Esgota o remanescente ⇒ trip por esgotamento.
	consume(t, b, "tree-warn", budget.Amount{Tokens: 150, CostMicroUSD: 150})
	st, err = br.Observe(ctx, budget.Amount{Tokens: 150, CostMicroUSD: 150})
	if err != nil {
		t.Fatalf("Observe trip: %v", err)
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado após esgotar = %s, quero open", st)
	}

	// O aviso PRECEDE o trip na sequência append-only (seq do warning < seq do trip).
	recs, _ := br.Replay(ctx)
	var warnSeq, tripSeq uint64
	for _, r := range recs {
		switch r.Type {
		case scheduler.EventBudgetWarning80Pct:
			if warnSeq == 0 {
				warnSeq = r.Seq
			}
		case scheduler.EventBudgetBreakerTripped:
			if tripSeq == 0 {
				tripSeq = r.Seq
			}
		}
	}
	if warnSeq == 0 || tripSeq == 0 {
		t.Fatalf("faltam eventos: warnSeq=%d tripSeq=%d", warnSeq, tripSeq)
	}
	if !(warnSeq < tripSeq) {
		t.Fatalf("warnSeq=%d NÃO precede tripSeq=%d", warnSeq, tripSeq)
	}
}

// O aviso é emitido UMA vez por ciclo closed (não spamma a cada Observe).
func TestBreaker_WarningOncePerCycle(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	b := newTree(t, "tree-warn1", budget.Amount{Tokens: 1000, CostMicroUSD: 1000})
	th := scheduler.Thresholds{WarnFraction: 0.8}
	br := newBreaker(t, es, b, "tree-warn1", th, scheduler.WithBreakerClock(fixedClock(base)))
	ctx := context.Background()

	consume(t, b, "tree-warn1", budget.Amount{Tokens: 850, CostMicroUSD: 850})
	for i := 0; i < 3; i++ {
		if _, err := br.Observe(ctx, budget.Amount{}); err != nil {
			t.Fatalf("Observe#%d: %v", i, err)
		}
	}
	recs, _ := br.Replay(ctx)
	warns := 0
	for _, r := range recs {
		if r.Type == scheduler.EventBudgetWarning80Pct {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("avisos = %d, quero exactamente 1 por ciclo", warns)
	}
}

// ---------------------------------------------------------------------------
// Integração: limiares por classe/tenant respeitados.
// ---------------------------------------------------------------------------

func TestBreaker_ThresholdsPerClassTenant(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	tp := scheduler.NewStaticThresholdProvider(scheduler.Thresholds{
		VelocityTokens: 1000, Window: time.Minute, // default (classe "basic")
	}).SetClass("premium", scheduler.Thresholds{
		VelocityTokens: 5000, Window: time.Minute, // premium tolera mais velocidade
	})

	newFor := func(class string) *scheduler.Breaker {
		es := newES(t)
		b := newTree(t, "tree-"+class, bigLimit)
		return newBreaker(t, es, b, "tree-"+class, tp.Thresholds(class, ""),
			scheduler.WithBreakerClassTenant(class, ""),
			scheduler.WithBreakerClock(fixedClock(base)))
	}
	ctx := context.Background()

	basic := newFor("basic")
	premium := newFor("premium")

	// 1500 tokens/janela: basic (limiar 1000) dispara; premium (limiar 5000) não.
	stB, _ := basic.Observe(ctx, budget.Amount{Tokens: 1500})
	stP, _ := premium.Observe(ctx, budget.Amount{Tokens: 1500})
	if stB != scheduler.BreakerOpen {
		t.Fatalf("basic = %s, quero open (limiar 1000 excedido)", stB)
	}
	if stP != scheduler.BreakerClosed {
		t.Fatalf("premium = %s, quero closed (limiar 5000 não excedido)", stP)
	}
}

// ---------------------------------------------------------------------------
// Integração: trip pausa a árvore SEM efeitos duplicados (Machine do AOS-017).
// ---------------------------------------------------------------------------

// claimRunning transita uma Machine ready → running (o claim exige fencing token).
func claimRunning(t *testing.T, m *state.Machine) {
	t.Helper()
	if err := m.Transition(context.Background(), state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("claim ready→running: %v", err)
	}
}

// countTransitions conta os eventos de transição de estado persistidos de um run.
func countTransitions(t *testing.T, es *eventstore.Store, runID string) int {
	t.Helper()
	evs, err := es.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read(%s): %v", runID, err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type == state.EventTypeTransition {
			n++
		}
	}
	return n
}

func TestBreaker_TripParksTreeNoDuplicateEffects(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	b := newTree(t, "tree-park", bigLimit)

	// Duas tarefas em curso da árvore, cada uma com a sua Machine durável (AOS-017).
	mA, _ := state.NewMachine(es, "run-A")
	mB, _ := state.NewMachine(es, "run-B")
	claimRunning(t, mA)
	claimRunning(t, mB)

	parker := scheduler.NewMachineParker(func(treeID string) []*state.Machine {
		if treeID == "tree-park" {
			return []*state.Machine{mA, mB}
		}
		return nil
	})

	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute}
	br := newBreaker(t, es, b, "tree-park", th,
		scheduler.WithBreakerClock(fixedClock(base)),
		scheduler.WithBreakerParker(parker))
	ctx := context.Background()

	// Antes: 1 transição cada (ready→running).
	if got := countTransitions(t, es, "run-A"); got != 1 {
		t.Fatalf("run-A transições antes = %d, quero 1", got)
	}

	// Trip ⇒ as tarefas em curso transitam running → paused (estado durável seguro).
	if _, err := br.Observe(ctx, budget.Amount{Tokens: 1500}); err != nil {
		t.Fatalf("trip: %v", err)
	}
	if mA.Current() != state.Paused || mB.Current() != state.Paused {
		t.Fatalf("estados após trip = (%s,%s), quero (paused,paused)", mA.Current(), mB.Current())
	}
	// 2 transições cada: ready→running, running→paused.
	if got := countTransitions(t, es, "run-A"); got != 2 {
		t.Fatalf("run-A transições após trip = %d, quero 2", got)
	}

	// IDEMPOTÊNCIA / SEM EFEITOS DUPLICADOS: re-parar a árvore (retry do parker) é
	// no-op — as tarefas já não estão em running, logo nenhuma transição extra.
	if err := parker.ParkTree(ctx, "tree-park"); err != nil {
		t.Fatalf("ParkTree repetido: %v", err)
	}
	if got := countTransitions(t, es, "run-A"); got != 2 {
		t.Fatalf("run-A transições após re-park = %d, quero 2 (sem duplicar)", got)
	}
	if mA.Current() != state.Paused {
		t.Fatalf("run-A = %s, quero paused (inalterado)", mA.Current())
	}
}

// ---------------------------------------------------------------------------
// Integração: retoma em half-open NÃO re-executa passos concluídos (ledger/replay).
// ---------------------------------------------------------------------------

func TestBreaker_HalfOpenResumeDoesNotReexecuteCompletedSteps(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(5_000_000, 0))
	es := newES(t)
	b := newTree(t, "tree-resume", bigLimit)

	m, _ := state.NewMachine(es, "run-R")
	claimRunning(t, m)
	parker := scheduler.NewMachineParker(func(string) []*state.Machine {
		return []*state.Machine{m}
	})

	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute, Cooldown: 30 * time.Second}
	br := newBreaker(t, es, b, "tree-resume", th,
		scheduler.WithBreakerClock(clk.now),
		scheduler.WithBreakerParker(parker))
	ctx := context.Background()

	// Executor simples com LEDGER de passos concluídos (AOS-014). Só executa o que
	// Allow permitir; NUNCA re-executa um passo já no ledger — a não-reexecução é do
	// ledger/replay, o breaker só liberta a continuação.
	steps := []string{"s1", "s2", "s3", "s4"}
	runCount := map[string]int{}
	ledger := map[string]bool{}
	execUpTo := func(limit int) {
		for i, s := range steps {
			if i >= limit {
				return
			}
			ok, _, err := br.Allow(ctx)
			if err != nil {
				t.Fatalf("Allow em %s: %v", s, err)
			}
			if !ok {
				return // fail-closed: pára a continuação
			}
			if ledger[s] {
				continue // passo já concluído: NÃO re-executa
			}
			runCount[s]++
			ledger[s] = true
		}
	}

	// Executa s1,s2 (concluídos), depois o breaker dispara e pára a tarefa.
	execUpTo(2)
	if _, err := br.Observe(ctx, budget.Amount{Tokens: 1500}); err != nil {
		t.Fatalf("trip: %v", err)
	}
	if m.Current() != state.Paused {
		t.Fatalf("tarefa = %s, quero paused após trip", m.Current())
	}
	// Enquanto open, a continuação está barrada (fail-closed).
	execUpTo(len(steps))
	if runCount["s3"] != 0 {
		t.Fatalf("s3 executado com breaker open (%d), quero 0", runCount["s3"])
	}

	// Cooldown ⇒ half_open (retoma controlada); limpa a janela de velocidade.
	clk.advance(30 * time.Second)
	if ok, st, _ := br.Allow(ctx); !ok || st != scheduler.BreakerHalfOpen {
		t.Fatalf("reabertura = (%v,%s), quero (true,half_open)", ok, st)
	}
	// A tarefa retoma paused → running (sob o lease já detido — sem novo fencing token).
	if err := m.Resume(ctx, state.TransitionEvent{Reason: "breaker_half_open"}); err != nil {
		t.Fatalf("Resume paused→running: %v", err)
	}
	clk.advance(2 * time.Minute) // janela de velocidade limpa

	// Continua a execução: s1,s2 estão no ledger (não re-executam); s3,s4 correm.
	execUpTo(len(steps))
	for _, s := range []string{"s1", "s2", "s3", "s4"} {
		if runCount[s] != 1 {
			t.Fatalf("runCount[%s] = %d, quero 1 (sem re-execução de concluídos)", s, runCount[s])
		}
	}
}

// ---------------------------------------------------------------------------
// Replay/Rebuild: o estado do breaker reconstrói-se dos eventos (sobrevive a crash).
// ---------------------------------------------------------------------------

func TestBreaker_RebuildReconstructsState(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(7_000_000, 0))
	es := newES(t)
	b := newTree(t, "tree-rb", bigLimit)
	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute, Cooldown: 30 * time.Second}
	br := newBreaker(t, es, b, "tree-rb", th, scheduler.WithBreakerClock(clk.now))
	ctx := context.Background()

	// Leva o breaker a open.
	if _, err := br.Observe(ctx, budget.Amount{Tokens: 1500}); err != nil {
		t.Fatalf("trip: %v", err)
	}

	// Um dono NOVO (após "crash") reconstrói o breaker sobre o mesmo Event Store.
	br2 := newBreaker(t, es, b, "tree-rb", th, scheduler.WithBreakerClock(clk.now))
	st, err := br2.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado reconstruído = %s, quero open (fail-closed sobrevive a crash)", st)
	}
	// O trippedAt reconstruído mantém o cooldown coerente: antes de decorrer, nega;
	// após decorrer, reabre — prova de que o instante do trip veio do log.
	if ok, _, _ := br2.Allow(ctx); ok {
		t.Fatalf("Allow do breaker reconstruído (open, pré-cooldown) = true, quero false")
	}
	clk.advance(30 * time.Second)
	if ok, st, _ := br2.Allow(ctx); !ok || st != scheduler.BreakerHalfOpen {
		t.Fatalf("Allow reconstruído após cooldown = (%v,%s), quero (true,half_open)", ok, st)
	}
}

// Rebuild de um stream inexistente ⇒ closed (o estado inicial).
func TestBreaker_RebuildEmptyIsClosed(t *testing.T) {
	t.Parallel()
	es := newES(t)
	b := newTree(t, "tree-empty", bigLimit)
	br := newBreaker(t, es, b, "tree-empty", scheduler.Thresholds{})
	st, err := br.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if st != scheduler.BreakerClosed {
		t.Fatalf("estado = %s, quero closed", st)
	}
}

// ---------------------------------------------------------------------------
// Determinismo/concorrência: Observe concorrente é seguro (-race) e não excede o
// tecto de estados válidos.
// ---------------------------------------------------------------------------

func TestBreaker_ConcurrentObserveIsRaceFree(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	b := newTree(t, "tree-conc", bigLimit)
	th := scheduler.Thresholds{VelocityTokens: 100000, Window: time.Minute, WarnFraction: 0.8}
	br := newBreaker(t, es, b, "tree-conc", th, scheduler.WithBreakerClock(fixedClock(base)))
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = br.Observe(ctx, budget.Amount{Tokens: 10})
			_, _, _ = br.Allow(ctx)
		}()
	}
	wg.Wait()
	// Sem trip (limiar alto, orçamento folgado): permanece closed e consistente.
	if st := br.State(); st != scheduler.BreakerClosed {
		t.Fatalf("estado final = %s, quero closed", st)
	}
}

// Guards de construção fail-closed.
func TestNewBreaker_FailClosedDeps(t *testing.T) {
	t.Parallel()
	es := newES(t)
	b := newTree(t, "tree-x", bigLimit)
	if _, err := scheduler.NewBreaker(nil, b, "tree-x", scheduler.Thresholds{}); err == nil {
		t.Fatalf("NewBreaker sem log: quero erro")
	}
	if _, err := scheduler.NewBreaker(es, nil, "tree-x", scheduler.Thresholds{}); err == nil {
		t.Fatalf("NewBreaker sem budget: quero erro")
	}
	if _, err := scheduler.NewBreaker(es, b, "", scheduler.Thresholds{}); err == nil {
		t.Fatalf("NewBreaker sem tree_id: quero erro")
	}
}

// ---------------------------------------------------------------------------
// AOS-029-Q1: erro de leitura de orçamento é FAIL-CLOSED (na dúvida, abre) e a
// velocidade dispara mesmo com o reader indisponível.
// ---------------------------------------------------------------------------

// failingReader é um TreeBudgetReader que pode falhar a leitura do remanescente, para
// exercitar o caminho fail-closed do breaker (AOS-029-Q1).
type failingReader struct {
	mu    sync.Mutex
	fail  bool
	avail budget.Amount
	limit budget.Amount
	node  string
}

func (r *failingReader) Available(string) (budget.Amount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return budget.Amount{}, errors.New("budget reader indisponível")
	}
	return r.avail, nil
}

func (r *failingReader) Snapshot() map[string]budget.NodeState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return map[string]budget.NodeState{r.node: {Limit: r.limit}}
}

var _ scheduler.TreeBudgetReader = (*failingReader)(nil)

// Um erro de leitura do remanescente ABRE o breaker (fail-closed) e regista o motivo
// read_error — refuta o fail-OPEN em que o reader indisponível cegava o disparo.
func TestBreaker_ReadErrorTripsFailClosed(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	rd := &failingReader{fail: true, node: "tree-rerr", limit: bigLimit}
	br, err := scheduler.NewBreaker(es, rd, "tree-rerr", scheduler.Thresholds{},
		scheduler.WithBreakerClock(fixedClock(base)))
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	ctx := context.Background()

	st, oerr := br.Observe(ctx, budget.Amount{Tokens: 1})
	if oerr == nil {
		t.Fatalf("Observe com reader em erro: quero o erro de infra propagado")
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado = %s, quero open (fail-closed na leitura falhada)", st)
	}
	recs, _ := br.Replay(ctx)
	last := recs[len(recs)-1]
	if last.Type != scheduler.EventBudgetBreakerTripped || last.Reason != string(scheduler.ReasonReadError) {
		t.Fatalf("último evento = (%s,%s), quero (tripped,read_error)", last.Type, last.Reason)
	}
	// Fail-closed persiste: o consumo fica negado enquanto open (cooldown 0).
	if ok, _, _ := br.Allow(ctx); ok {
		t.Fatalf("Allow após read-error trip = true, quero false (fail-closed)")
	}
}

// A VELOCIDADE dispara mesmo com o reader indisponível: o sinal é independente da
// leitura de orçamento (AOS-029-Q1) e o motivo gravado é velocity.
func TestBreaker_VelocityTripsDespiteReaderError(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	rd := &failingReader{fail: true, node: "tree-vre", limit: bigLimit}
	br, err := scheduler.NewBreaker(es, rd, "tree-vre",
		scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute},
		scheduler.WithBreakerClock(fixedClock(base)))
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	ctx := context.Background()

	st, _ := br.Observe(ctx, budget.Amount{Tokens: 1500}) // burst >= 1000, reader em erro
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado = %s, quero open (velocidade dispara apesar do reader em erro)", st)
	}
	recs, _ := br.Replay(ctx)
	last := recs[len(recs)-1]
	if last.Reason != string(scheduler.ReasonVelocity) {
		t.Fatalf("motivo = %s, quero velocity (burst independente da leitura)", last.Reason)
	}
}

// ---------------------------------------------------------------------------
// AOS-029-Q2: o aviso ~80% PRECEDE o trip mesmo quando a margem de esgotamento é
// grande (esgotamento e aviso na MESMA Observe).
// ---------------------------------------------------------------------------

func TestBreaker_Warning80PrecedesTripWithLargeMargin(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	b := newTree(t, "tree-warnm", budget.Amount{Tokens: 1000, CostMicroUSD: 1000})
	// Margem 250 > (1-0.8)*1000 = 200: a 80% consumido, avail=200 <= 250 dispara — o
	// aviso e o trip coincidem na mesma Observe e o aviso TEM de os preceder no log.
	th := scheduler.Thresholds{
		WarnFraction:     0.8,
		ExhaustionMargin: budget.Amount{Tokens: 250, CostMicroUSD: 250},
	}
	br := newBreaker(t, es, b, "tree-warnm", th, scheduler.WithBreakerClock(fixedClock(base)))
	ctx := context.Background()

	consume(t, b, "tree-warnm", budget.Amount{Tokens: 800, CostMicroUSD: 800})
	st, err := br.Observe(ctx, budget.Amount{Tokens: 800, CostMicroUSD: 800})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado = %s, quero open (esgotamento com margem 250)", st)
	}
	recs, _ := br.Replay(ctx)
	var warnSeq, tripSeq uint64
	for _, r := range recs {
		switch r.Type {
		case scheduler.EventBudgetWarning80Pct:
			if warnSeq == 0 {
				warnSeq = r.Seq
			}
		case scheduler.EventBudgetBreakerTripped:
			if tripSeq == 0 {
				tripSeq = r.Seq
			}
		}
	}
	if warnSeq == 0 {
		t.Fatalf("aviso não emitido: o esgotamento (margem grande) suprimiu-o (AOS-029-Q2)")
	}
	if tripSeq == 0 || !(warnSeq < tripSeq) {
		t.Fatalf("warnSeq=%d NÃO precede tripSeq=%d", warnSeq, tripSeq)
	}
}

// ---------------------------------------------------------------------------
// AOS-029-Q4/C1: orçamento de UMA dimensão (só tokens; custo limite 0) NÃO dispara
// esgotamento espúrio na 1ª Observe — a dimensão não financiada é ignorada.
// ---------------------------------------------------------------------------

func TestBreaker_SingleDimensionBudgetNoSpuriousExhaustion(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	// Custo limite 0 = dimensão NÃO financiada (Available.CostMicroUSD=0 é "zero
	// permitido", não esgotamento).
	b := newTree(t, "tree-tok", budget.Amount{Tokens: 1_000_000, CostMicroUSD: 0})
	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute} // margem 0 (default)
	br := newBreaker(t, es, b, "tree-tok", th, scheduler.WithBreakerClock(fixedClock(base)))
	ctx := context.Background()

	// 1ª Observe modesta: NÃO dispara esgotamento pela dimensão custo não financiada.
	st, err := br.Observe(ctx, budget.Amount{Tokens: 10})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if st != scheduler.BreakerClosed {
		t.Fatalf("estado = %s, quero closed (sem esgotamento espúrio na dimensão custo 0)", st)
	}
	// A velocidade continua a governar: um burst dispara por VELOCIDADE, não esgotamento.
	st, err = br.Observe(ctx, budget.Amount{Tokens: 2000})
	if err != nil {
		t.Fatalf("Observe burst: %v", err)
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado = %s, quero open (velocidade)", st)
	}
	recs, _ := br.Replay(ctx)
	last := recs[len(recs)-1]
	if last.Reason != string(scheduler.ReasonVelocity) {
		t.Fatalf("motivo = %s, quero velocity (não esgotamento)", last.Reason)
	}
}

// ---------------------------------------------------------------------------
// AOS-029-Q5: consumo EXACTAMENTE igual ao tecto de velocidade dispara (>=).
// ---------------------------------------------------------------------------

func TestBreaker_VelocityTripsAtExactThreshold(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	b := newTree(t, "tree-veleq", bigLimit)
	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute}
	br := newBreaker(t, es, b, "tree-veleq", th, scheduler.WithBreakerClock(fixedClock(base)))
	ctx := context.Background()

	st, err := br.Observe(ctx, budget.Amount{Tokens: 1000}) // == tecto ⇒ dispara
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado = %s, quero open (consumo == tecto dispara, fail-closed)", st)
	}
}

// ---------------------------------------------------------------------------
// AOS-029-Q3/C2: falha de ParkTree no trip é RE-TENTADA (reconciliação) enquanto
// open, sem transições duplicadas — as tarefas acabam paused.
// ---------------------------------------------------------------------------

// flakyParker falha as primeiras `failsLeft` chamadas (a tarefa fica em running) e
// depois delega no parker real (que a pára). Modela uma falha de infra (ES sem quórum).
type flakyParker struct {
	inner     scheduler.TaskParker
	failsLeft int
	calls     int
}

func (p *flakyParker) ParkTree(ctx context.Context, treeID string) error {
	p.calls++
	if p.failsLeft > 0 {
		p.failsLeft--
		return errors.New("ES sem quórum")
	}
	return p.inner.ParkTree(ctx, treeID)
}

func TestBreaker_ParkRetryAfterInfraFailure(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	b := newTree(t, "tree-prk", bigLimit)

	mA, _ := state.NewMachine(es, "run-PA")
	claimRunning(t, mA)
	inner := scheduler.NewMachineParker(func(treeID string) []*state.Machine {
		if treeID == "tree-prk" {
			return []*state.Machine{mA}
		}
		return nil
	})
	fp := &flakyParker{inner: inner, failsLeft: 1}

	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute}
	br := newBreaker(t, es, b, "tree-prk", th,
		scheduler.WithBreakerClock(fixedClock(base)),
		scheduler.WithBreakerParker(fp))
	ctx := context.Background()

	// Trip: ParkTree falha (1ª vez) — o breaker fica open mas a tarefa AINDA em running.
	st, err := br.Observe(ctx, budget.Amount{Tokens: 1500})
	if err == nil {
		t.Fatalf("Observe: quero o erro de paragem propagado no 1º trip")
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado = %s, quero open (fail-closed apesar da falha de paragem)", st)
	}
	if mA.Current() != state.Running {
		t.Fatalf("tarefa = %s, quero running (park falhou)", mA.Current())
	}

	// Reconciliação: um Allow seguinte re-tenta o park (idempotente) e PÁRA a tarefa.
	if ok, stt, rerr := br.Allow(ctx); rerr != nil || ok || stt != scheduler.BreakerOpen {
		t.Fatalf("Allow reconciliador = (%v,%s,%v), quero (false,open,nil)", ok, stt, rerr)
	}
	if mA.Current() != state.Paused {
		t.Fatalf("tarefa = %s, quero paused (park re-tentado com sucesso)", mA.Current())
	}
	// SEM duplicação: 2 transições (ready→running, running→paused).
	if got := countTransitions(t, es, "run-PA"); got != 2 {
		t.Fatalf("transições = %d, quero 2 (sem duplicar)", got)
	}
	// Um novo Allow NÃO re-para (parkPending já limpo) — sem transições extra.
	if _, _, err := br.Allow(ctx); err != nil {
		t.Fatalf("Allow pós-reconciliação: %v", err)
	}
	if got := countTransitions(t, es, "run-PA"); got != 2 {
		t.Fatalf("transições após novo Allow = %d, quero 2 (idempotente)", got)
	}
	if fp.calls != 2 {
		t.Fatalf("parker chamado %d vezes, quero 2 (falha + retry bem-sucedido)", fp.calls)
	}
}

// ---------------------------------------------------------------------------
// AOS-029-C3: os eventos half_open (cooldown) e closed (recovered) são persistidos e
// replayáveis; o Rebuild reconstrói half_open e closed.
// ---------------------------------------------------------------------------

func TestBreaker_ReplayAllTransitionsAndRebuildClosed(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(9_000_000, 0))
	es := newES(t)
	b := newTree(t, "tree-all", bigLimit)
	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute, Cooldown: 30 * time.Second}
	br := newBreaker(t, es, b, "tree-all", th, scheduler.WithBreakerClock(clk.now))
	ctx := context.Background()

	// closed → open (trip) → half_open (cooldown, via Allow) → closed (probe recuperado).
	if _, err := br.Observe(ctx, budget.Amount{Tokens: 1500}); err != nil {
		t.Fatalf("trip: %v", err)
	}
	clk.advance(30 * time.Second)
	if ok, stt, _ := br.Allow(ctx); !ok || stt != scheduler.BreakerHalfOpen {
		t.Fatalf("reabertura = (%v,%s), quero (true,half_open)", ok, stt)
	}
	clk.advance(2 * time.Minute) // limpa a janela de velocidade
	if stt, err := br.Observe(ctx, budget.Amount{Tokens: 1}); err != nil || stt != scheduler.BreakerClosed {
		t.Fatalf("recuperação = (%s,%v), quero (closed,nil)", stt, err)
	}

	recs, err := br.Replay(ctx)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	var seq []string
	var halfReason, closedReason string
	for _, r := range recs {
		switch r.Type {
		case scheduler.EventBudgetBreakerTripped, scheduler.EventBudgetBreakerHalfOpen, scheduler.EventBudgetBreakerClosed:
			seq = append(seq, r.Type)
		}
		switch r.Type {
		case scheduler.EventBudgetBreakerHalfOpen:
			halfReason = r.Reason
		case scheduler.EventBudgetBreakerClosed:
			closedReason = r.Reason
		}
	}
	want := []string{
		scheduler.EventBudgetBreakerTripped,
		scheduler.EventBudgetBreakerHalfOpen,
		scheduler.EventBudgetBreakerClosed,
	}
	if len(seq) != len(want) {
		t.Fatalf("sequência = %v, quero %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("sequência[%d] = %s, quero %s", i, seq[i], want[i])
		}
	}
	if halfReason != string(scheduler.ReasonCooldown) {
		t.Fatalf("half_open reason = %s, quero cooldown", halfReason)
	}
	if closedReason != string(scheduler.ReasonRecovered) {
		t.Fatalf("closed reason = %s, quero recovered", closedReason)
	}

	// Um dono NOVO reconstrói o estado closed a partir do log completo.
	br2 := newBreaker(t, es, b, "tree-all", th, scheduler.WithBreakerClock(clk.now))
	if st, err := br2.Rebuild(ctx); err != nil || st != scheduler.BreakerClosed {
		t.Fatalf("Rebuild = (%s,%v), quero (closed,nil)", st, err)
	}
}

// O Rebuild reconstrói o estado half_open a partir dos eventos tripped + half_open.
func TestBreaker_RebuildReconstructsHalfOpen(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(11_000_000, 0))
	es := newES(t)
	b := newTree(t, "tree-rho", bigLimit)
	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute, Cooldown: 30 * time.Second}
	br := newBreaker(t, es, b, "tree-rho", th, scheduler.WithBreakerClock(clk.now))
	ctx := context.Background()

	if _, err := br.Observe(ctx, budget.Amount{Tokens: 1500}); err != nil {
		t.Fatalf("trip: %v", err)
	}
	clk.advance(30 * time.Second)
	if ok, st, _ := br.Allow(ctx); !ok || st != scheduler.BreakerHalfOpen {
		t.Fatalf("reabertura = (%v,%s), quero (true,half_open)", ok, st)
	}

	br2 := newBreaker(t, es, b, "tree-rho", th, scheduler.WithBreakerClock(clk.now))
	st, err := br2.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if st != scheduler.BreakerHalfOpen {
		t.Fatalf("estado reconstruído = %s, quero half_open", st)
	}
	// Em half_open o probe de continuação é concedido (retoma controlada).
	if ok, _, _ := br2.Allow(ctx); !ok {
		t.Fatalf("Allow do breaker reconstruído (half_open) = false, quero true")
	}
}

// ---------------------------------------------------------------------------
// AOS-029-C4: a reabertura open → half_open (via Allow) abre um span com os sinais.
// ---------------------------------------------------------------------------

func TestBreaker_EmitsSpanOnReopen(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(13_000_000, 0))
	es := newES(t)
	b := newTree(t, "tree-rspan", bigLimit)
	tr := &agentruntime.RecordingTracer{}
	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute, Cooldown: 30 * time.Second}
	br := newBreaker(t, es, b, "tree-rspan", th,
		scheduler.WithBreakerClock(clk.now),
		scheduler.WithBreakerTracer(tr))
	ctx := context.Background()

	if _, err := br.Observe(ctx, budget.Amount{Tokens: 1500}); err != nil {
		t.Fatalf("trip: %v", err)
	}
	spansBefore := len(tr.SpansByOperation("budget_breaker"))
	clk.advance(30 * time.Second)
	if ok, st, _ := br.Allow(ctx); !ok || st != scheduler.BreakerHalfOpen {
		t.Fatalf("reabertura = (%v,%s), quero (true,half_open)", ok, st)
	}
	spans := tr.SpansByOperation("budget_breaker")
	if len(spans) != spansBefore+1 {
		t.Fatalf("spans = %d, quero %d (Allow abriu um span na reabertura)", len(spans), spansBefore+1)
	}
	last := spans[len(spans)-1]
	if !last.Ended {
		t.Fatalf("span da reabertura não foi fechado")
	}
	if last.Attributes["aos.breaker.reason"] != string(scheduler.ReasonCooldown) {
		t.Fatalf("span reason = %v, quero cooldown", last.Attributes["aos.breaker.reason"])
	}
	if last.Attributes["aos.breaker.state"] != string(scheduler.BreakerHalfOpen) {
		t.Fatalf("span state = %v, quero half_open", last.Attributes["aos.breaker.state"])
	}
}

// O span do breaker é aberto e fechado (observabilidade — DoD): custo por span.
func TestBreaker_EmitsSpan(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es := newES(t)
	b := newTree(t, "tree-span", bigLimit)
	tr := &agentruntime.RecordingTracer{}
	th := scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute}
	br := newBreaker(t, es, b, "tree-span", th,
		scheduler.WithBreakerClock(fixedClock(base)),
		scheduler.WithBreakerTracer(tr))
	if _, err := br.Observe(context.Background(), budget.Amount{Tokens: 10, CostMicroUSD: 20}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	spans := tr.SpansByOperation("budget_breaker")
	if len(spans) != 1 || !spans[0].Ended {
		t.Fatalf("spans = %d (ended=%v), quero 1 fechado", len(spans), len(spans) == 1 && spans[0].Ended)
	}
	if _, ok := spans[0].Attributes["aos.breaker.cost_usd"]; !ok {
		t.Fatalf("span sem atributo de custo por span")
	}
}
