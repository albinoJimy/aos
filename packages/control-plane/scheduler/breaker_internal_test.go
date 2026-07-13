package scheduler

// Testes INTERNOS do breaker (AOS-029): a tabela declarativa de transições e os
// helpers puros. package scheduler (não _test) para aceder aos símbolos não
// exportados — determinísticos, sem I/O.

import "testing"

// A tabela declarativa é a ÚNICA fonte de verdade das transições do breaker: varre-se
// a matriz 3×3 e confirma-se que exactamente os quatro pares canónicos são válidos.
func TestBreaker_TransitionTableMatrix(t *testing.T) {
	all := []BreakerState{BreakerClosed, BreakerOpen, BreakerHalfOpen}
	valid := map[breakerTransition]bool{
		{BreakerClosed, BreakerOpen}:     true,
		{BreakerHalfOpen, BreakerOpen}:   true,
		{BreakerOpen, BreakerHalfOpen}:   true,
		{BreakerHalfOpen, BreakerClosed}: true,
	}
	for _, from := range all {
		for _, to := range all {
			want := valid[breakerTransition{from, to}]
			if got := isValidBreakerTransition(from, to); got != want {
				t.Errorf("isValidBreakerTransition(%s→%s) = %v, quero %v", from, to, got, want)
			}
		}
	}
}

// transitionEventType mapeia cada destino para o tipo de evento append-only correcto.
func TestBreaker_TransitionEventType(t *testing.T) {
	cases := []struct {
		to   BreakerState
		want string
	}{
		{BreakerOpen, EventBudgetBreakerTripped},
		{BreakerHalfOpen, EventBudgetBreakerHalfOpen},
		{BreakerClosed, EventBudgetBreakerClosed},
	}
	for _, c := range cases {
		if got := transitionEventType(c.to); got != c.want {
			t.Errorf("transitionEventType(%s) = %s, quero %s", c.to, got, c.want)
		}
	}
}

// dimFraction clampa consumed/limit a [0,1] e trata limit<=0 como 0 (sem base).
func TestBreaker_DimFraction(t *testing.T) {
	cases := []struct {
		consumed, limit int64
		want            float64
	}{
		{0, 1000, 0},
		{800, 1000, 0.8},
		{1000, 1000, 1},
		{1500, 1000, 1}, // clamp em 1
		{-5, 1000, 0},   // consumed negativo → 0
		{500, 0, 0},     // limit 0 → 0 (sem base)
	}
	for _, c := range cases {
		if got := dimFraction(c.consumed, c.limit); got != c.want {
			t.Errorf("dimFraction(%d,%d) = %v, quero %v", c.consumed, c.limit, got, c.want)
		}
	}
}

// parseBreakerStepID extrai o N de "brk-N" e rejeita o resto.
func TestBreaker_ParseStepID(t *testing.T) {
	cases := []struct {
		in string
		n  uint64
		ok bool
	}{
		{"brk-1", 1, true},
		{"brk-42", 42, true},
		{"brk-", 0, false},
		{"state-3", 0, false},
		{"brk-x", 0, false},
	}
	for _, c := range cases {
		n, ok := parseBreakerStepID(c.in)
		if n != c.n || ok != c.ok {
			t.Errorf("parseBreakerStepID(%q) = (%d,%v), quero (%d,%v)", c.in, n, ok, c.n, c.ok)
		}
	}
}

// isKnownBreakerState defende o Rebuild contra estados forjados.
func TestBreaker_IsKnownState(t *testing.T) {
	for _, s := range []BreakerState{BreakerClosed, BreakerOpen, BreakerHalfOpen} {
		if !isKnownBreakerState(s) {
			t.Errorf("isKnownBreakerState(%s) = false, quero true", s)
		}
	}
	for _, s := range []BreakerState{"", "bogus", "OPEN"} {
		if isKnownBreakerState(s) {
			t.Errorf("isKnownBreakerState(%q) = true, quero false", s)
		}
	}
}
