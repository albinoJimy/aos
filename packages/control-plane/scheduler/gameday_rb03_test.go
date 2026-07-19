package scheduler_test

// gameday_rb03_test.go — GAME DAY do RB-03 (esgotamento de orçamento tokens/$, ADR-008).
//
// Injecta o MODO DE FALHA REAL — a árvore de execução a consumir o orçamento até ao fim
// — e prova a MITIGAÇÃO-CHAVE sobre o circuit breaker REAL (AOS-029) apoiado num
// orçamento hierárquico REAL (AOS-008), SEM stubs: a ~80% emite-se o aviso de exaustão
// graciosa; ao esgotar, o breaker DISPARA (fail-closed) e passa a NEGAR todo o consumo
// novo — sem efeitos parciais não compensados. O orçamento é medido em tokens/$, não em
// iterações. Em seguida prova a RECUPERAÇÃO/RETOMA verificável (paridade com os restantes
// game-days): após o cooldown e a libertação da reserva não-consolidada do passo parado
// (aumento efectivo de orçamento), o breaker reavalia e transita open → half_open → closed,
// voltando a permitir — a mitigação degrada a disponibilidade de forma REVERSÍVEL, nunca
// permanente. Reutiliza os helpers de breaker_test.go.

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/scheduler"
)

// TestGameDay_RB03_BudgetExhaustionTripsBreakerFailClosed prova o ciclo sinal→diagnóstico
// →mitigação→RECUPERAÇÃO do RB-03: aviso a ~80%, trip por esgotamento, Allow a NEGAR
// fail-closed, e (após cooldown + libertação da reserva do passo parado) a retoma
// controlada open → half_open → closed que volta a permitir o consumo.
func TestGameDay_RB03_BudgetExhaustionTripsBreakerFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const treeID = "run-rb03"
	const cooldown = 30 * time.Second
	// Relógio mutável (determinista): congela na fase de falha e avança para exercitar o
	// cooldown na fase de recuperação — sem time.Now no caminho de decisão.
	clk := &mutClock{}
	clk.set(time.Unix(1_000_000, 0))
	// Orçamento em tokens/$ (ADR-008): 1000/1000. Trip quando o remanescente <= 100.
	b := newTree(t, treeID, budget.Amount{Tokens: 1000, CostMicroUSD: 1000})
	es := newES(t)
	th := scheduler.Thresholds{
		ExhaustionMargin: budget.Amount{Tokens: 100, CostMicroUSD: 100},
		WarnFraction:     0.8,      // aviso de exaustão graciosa a ~80% ANTES do hard-trip.
		Cooldown:         cooldown, // após o cooldown, open → half_open permite a retoma controlada.
		// velocidade desligada (Window 0): isola o eixo de ESGOTAMENTO.
	}
	br := newBreaker(t, es, b, treeID, th, scheduler.WithBreakerClock(clk.now))

	// Antes de qualquer consumo, o breaker está fechado e PERMITE (caminho feliz).
	allowed, st, err := br.Allow(ctx)
	if err != nil {
		t.Fatalf("Allow (inicial): %v", err)
	}
	if !allowed || st != scheduler.BreakerClosed {
		t.Fatalf("estado inicial = (%v,%s), quero (permite, closed)", allowed, st)
	}

	// SINAL/DIAGNÓSTICO: burn-down a ~85% (850/1000). O breaker OBSERVA e emite o aviso
	// de exaustão graciosa (~80%), mas ainda NÃO dispara (remanescente 150 > margem 100).
	consume(t, b, treeID, budget.Amount{Tokens: 850, CostMicroUSD: 850})
	st, err = br.Observe(ctx, budget.Amount{Tokens: 850, CostMicroUSD: 850})
	if err != nil {
		t.Fatalf("Observe (~85%%): %v", err)
	}
	if st != scheduler.BreakerClosed {
		t.Fatalf("a ~85%% o breaker = %s, quero closed (só aviso, ainda não trip)", st)
	}
	if !hasBreakerWarning(t, br, ctx) {
		t.Fatal("esperava um evento de aviso de exaustão (~80%%) a preceder o trip")
	}
	// A ~85% o consumo ainda é permitido (graceful, não hard-stop).
	if allowed, _, _ := br.Allow(ctx); !allowed {
		t.Fatal("a ~85%% (só aviso) o consumo devia continuar permitido")
	}

	// MODO DE FALHA: um passo EM VOO RESERVA o remanescente e empurra a árvore para o
	// esgotamento (Available cai para <= margem). É uma RESERVA — headroom debitado mas
	// ainda NÃO consolidado (Committed) — e modela precisamente o passo em curso que o
	// breaker vai parar: a sua reserva não-consolidada será libertada (rollback, "sem
	// efeitos parciais não compensados", RB-03 §Mitigação), restaurando orçamento para a
	// recuperação. O esgotamento é REAL: Available conta reservado + consolidado.
	inflight, err := b.Reserve(ctx, treeID, budget.Amount{Tokens: 100, CostMicroUSD: 100}) // resta 50/50
	if err != nil {
		t.Fatalf("Reserve (passo em voo): %v", err)
	}
	st, err = br.Observe(ctx, budget.Amount{Tokens: 100, CostMicroUSD: 100})
	if err != nil {
		t.Fatalf("Observe (esgotado): %v", err)
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado após esgotamento = %s, quero open (trip)", st)
	}

	// MITIGAÇÃO fail-closed: com o breaker OPEN, Allow NEGA todo o consumo novo ("na
	// dúvida, pára") — a explosão de custo é contida, sem efeitos parciais não compensados.
	allowed, st, err = br.Allow(ctx)
	if err != nil {
		t.Fatalf("Allow (open): %v", err)
	}
	if allowed {
		t.Fatal("com o breaker OPEN o consumo devia ser NEGADO (fail-closed)")
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("Allow devolveu estado %s, quero open", st)
	}

	// O motivo do trip é ESGOTAMENTO (não velocidade) — o diagnóstico bate certo.
	recs, err := br.Replay(ctx)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if last := recs[len(recs)-1]; last.Reason != string(scheduler.ReasonExhaustion) {
		t.Fatalf("motivo do trip = %s, quero exhaustion", last.Reason)
	}

	// ----------------------------------------------------------------------------------
	// RECUPERAÇÃO/RETOMA VERIFICÁVEL (RB-03 §Mitigação 3–4; paridade com RB-01/02/04/05).
	// A mitigação fail-closed é REVERSÍVEL: degrada a disponibilidade temporariamente,
	// nunca de forma permanente. open → half_open (cooldown) → closed (probe recuperado).
	// ----------------------------------------------------------------------------------

	// Enquanto o cooldown não decorre, o consumo permanece NEGADO (fail-closed mantém-se).
	if ok, _, _ := br.Allow(ctx); ok {
		t.Fatal("antes do cooldown o consumo devia continuar NEGADO (fail-closed)")
	}

	// O cooldown decorre ⇒ open → half_open: RETOMA CONTROLADA — o Allow concede o probe
	// de continuação (o breaker volta a permitir, mas sob observação).
	clk.advance(cooldown)
	allowed, st, err = br.Allow(ctx)
	if err != nil {
		t.Fatalf("Allow (pós-cooldown): %v", err)
	}
	if !allowed || st != scheduler.BreakerHalfOpen {
		t.Fatalf("pós-cooldown = (%v,%s), quero (permite, half_open)", allowed, st)
	}

	// AUMENTO EFECTIVO DE ORÇAMENTO: a reserva do passo parado é LIBERTADA (rollback do
	// trabalho não-consolidado) — o remanescente sobe de 50 para 150 (> margem 100). É a
	// contrapartida real de "sem efeitos parciais não compensados": o que foi reservado
	// mas não gasto regressa ao envelope.
	if err := b.Release(ctx, inflight); err != nil {
		t.Fatalf("Release (reserva do passo parado): %v", err)
	}
	if avail, aerr := b.Available(treeID); aerr != nil {
		t.Fatalf("Available (pós-release): %v", aerr)
	} else if avail.Tokens <= th.ExhaustionMargin.Tokens || avail.CostMicroUSD <= th.ExhaustionMargin.CostMicroUSD {
		t.Fatalf("após libertar a reserva, remanescente = %d/%d, quero > margem %d/%d",
			avail.Tokens, avail.CostMicroUSD, th.ExhaustionMargin.Tokens, th.ExhaustionMargin.CostMicroUSD)
	}

	// PROBE RECUPERADO: com o orçamento acima da margem, o Observe em half_open já NÃO
	// re-dispara por esgotamento e FECHA o breaker (half_open → closed) — regime normal.
	st, err = br.Observe(ctx, budget.Amount{})
	if err != nil {
		t.Fatalf("Observe (probe recuperado): %v", err)
	}
	if st != scheduler.BreakerClosed {
		t.Fatalf("estado após recuperação = %s, quero closed (probe recuperado)", st)
	}

	// RECUPERAÇÃO VERIFICADA: com o breaker closed, o consumo volta a ser PERMITIDO.
	if allowed, st, _ := br.Allow(ctx); !allowed || st != scheduler.BreakerClosed {
		t.Fatalf("Allow (recuperado) = (%v,%s), quero (permite, closed)", allowed, st)
	}

	// A sequência append-only regista o ciclo COMPLETO — tripped → half_open → closed,
	// com os motivos exhaustion/cooldown/recovered — recuperação auditável (ADR-001).
	recs, err = br.Replay(ctx)
	if err != nil {
		t.Fatalf("Replay (pós-recuperação): %v", err)
	}
	assertBreakerRecoveryCycle(t, recs)
}

// assertBreakerRecoveryCycle confirma que o log do breaker contém o ciclo de recuperação
// completo por ordem: trip por esgotamento → reabertura por cooldown → fecho por
// recuperação. É a prova auditável de que a retoma aconteceu (não só o estado in-memory).
func assertBreakerRecoveryCycle(t *testing.T, recs []scheduler.BreakerRecord) {
	t.Helper()
	var tripSeq, halfSeq, closedSeq uint64
	var tripReason, halfReason, closedReason string
	for _, r := range recs {
		switch r.Type {
		case scheduler.EventBudgetBreakerTripped:
			if tripSeq == 0 {
				tripSeq, tripReason = r.Seq, r.Reason
			}
		case scheduler.EventBudgetBreakerHalfOpen:
			if halfSeq == 0 {
				halfSeq, halfReason = r.Seq, r.Reason
			}
		case scheduler.EventBudgetBreakerClosed:
			if closedSeq == 0 {
				closedSeq, closedReason = r.Seq, r.Reason
			}
		}
	}
	if tripSeq == 0 || halfSeq == 0 || closedSeq == 0 {
		t.Fatalf("faltam transições no log: tripped=%d half_open=%d closed=%d", tripSeq, halfSeq, closedSeq)
	}
	if !(tripSeq < halfSeq && halfSeq < closedSeq) {
		t.Fatalf("ordem das transições errada: tripped=%d half_open=%d closed=%d (quero crescente)", tripSeq, halfSeq, closedSeq)
	}
	if tripReason != string(scheduler.ReasonExhaustion) {
		t.Fatalf("motivo do trip = %s, quero exhaustion", tripReason)
	}
	if halfReason != string(scheduler.ReasonCooldown) {
		t.Fatalf("motivo da reabertura = %s, quero cooldown", halfReason)
	}
	if closedReason != string(scheduler.ReasonRecovered) {
		t.Fatalf("motivo do fecho = %s, quero recovered", closedReason)
	}
}

// hasBreakerWarning reporta se o breaker já emitiu um evento de aviso (~80%) no seu log
// de replay — a exaustão graciosa que PRECEDE o hard-trip.
func hasBreakerWarning(t *testing.T, br *scheduler.Breaker, ctx context.Context) bool {
	t.Helper()
	recs, err := br.Replay(ctx)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	for _, r := range recs {
		if r.Type == scheduler.EventBudgetWarning80Pct {
			return true
		}
	}
	return false
}
