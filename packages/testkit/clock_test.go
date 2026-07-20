package testkit_test

import (
	"sync"
	"testing"
	"time"

	tk "github.com/aos-ref/testkit"
)

// TestFixedClock_Determinista: o relógio fixo devolve sempre o instante canónico.
func TestFixedClock_Determinista(t *testing.T) {
	t.Parallel()
	clk := tk.FixedClock()
	a := clk()
	b := clk()
	if !a.Equal(b) || !a.Equal(tk.CanonicalInstant) {
		t.Fatalf("FixedClock nao determinista: a=%v b=%v canonico=%v", a, b, tk.CanonicalInstant)
	}
}

// TestManualClock_Avanca: o relógio manual só avança quando pedido e nunca recua.
func TestManualClock_Avanca(t *testing.T) {
	t.Parallel()
	clk := tk.NewManualClock(time.Time{}) // cai no CanonicalInstant
	if !clk.Now().Equal(tk.CanonicalInstant) {
		t.Fatalf("start=%v, esperava %v", clk.Now(), tk.CanonicalInstant)
	}
	clk.Advance(90 * time.Second)
	if got := clk.Now().Sub(tk.CanonicalInstant); got != 90*time.Second {
		t.Fatalf("apos Advance(90s): delta=%v", got)
	}
	// Avanço negativo é ignorado (monotonicidade).
	before := clk.Now()
	clk.Advance(-time.Hour)
	if !clk.Now().Equal(before) {
		t.Fatalf("Advance negativo recuou o relogio: %v -> %v", before, clk.Now())
	}
	// Set no passado é ignorado; no futuro aplica.
	clk.Set(tk.CanonicalInstant) // passado -> ignorado
	if !clk.Now().Equal(before) {
		t.Fatalf("Set no passado alterou o relogio")
	}
	future := before.Add(time.Hour)
	if got := clk.Set(future); !got.Equal(future) {
		t.Fatalf("Set no futuro: %v, esperava %v", got, future)
	}
}

// TestManualClock_Concorrente prova segurança -race: Now e Advance em paralelo.
func TestManualClock_Concorrente(t *testing.T) {
	t.Parallel()
	clk := tk.NewManualClock(tk.CanonicalInstant)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); clk.Advance(time.Second) }()
		go func() { defer wg.Done(); _ = clk.Now() }()
	}
	wg.Wait()
	if got := clk.Now().Sub(tk.CanonicalInstant); got != 50*time.Second {
		t.Fatalf("apos 50 avancos de 1s: delta=%v", got)
	}
}
