package testkit_test

import (
	"sync"
	"testing"

	tk "github.com/aos-ref/testkit"
)

// TestFixtureStepID_Determinista: o step_id do turno é puro e zero-padded.
func TestFixtureStepID_Determinista(t *testing.T) {
	t.Parallel()
	if got := tk.FixtureStepID(1); got != "step-000001" {
		t.Fatalf("FixtureStepID(1)=%q, esperava step-000001", got)
	}
	if tk.FixtureStepID(2) != "step-000002" {
		t.Fatalf("step_id do turno 2 inesperado: %q", tk.FixtureStepID(2))
	}
	// Estabilidade entre chamadas (replay): a mesma posição ⇒ o mesmo step_id.
	first := tk.FixtureStepID(7)
	again := tk.FixtureStepID(7)
	if first != again {
		t.Fatalf("FixtureStepID nao e estavel para o mesmo turno: %q != %q", first, again)
	}
}

// TestFixtureKey_CompoemChaveCanonica: a idempotency key = run_id + ":" + step_id.
func TestFixtureKey_CompoemChaveCanonica(t *testing.T) {
	t.Parallel()
	key, err := tk.FixtureKey(1)
	if err != nil {
		t.Fatalf("FixtureKey: %v", err)
	}
	want := tk.FixtureRunID + ":" + "step-000001"
	if key != want {
		t.Fatalf("FixtureKey(1)=%q, esperava %q", key, want)
	}
	// Compõe a mesma função pura que a produção usa (durable.IdempotencyKey).
	direct, err := tk.IdempotencyKey(tk.FixtureRunID, "step-000001")
	if err != nil {
		t.Fatalf("IdempotencyKey: %v", err)
	}
	if direct != key {
		t.Fatalf("IdempotencyKey diverge de FixtureKey: %q != %q", direct, key)
	}
}

// TestIdempotencyKey_ValidacaoPropagada: um input com ':' é rejeitado (a chave
// nunca é silenciosamente ambígua).
func TestIdempotencyKey_ValidacaoPropagada(t *testing.T) {
	t.Parallel()
	if _, err := tk.IdempotencyKey("run:bad", "step-1"); err == nil {
		t.Fatal("esperava erro para run_id com ':'")
	}
	if _, err := tk.IdempotencyKey("", "step-1"); err == nil {
		t.Fatal("esperava erro para run_id vazio")
	}
}

// TestSeqIDGen_SequencialEUnico: ids deterministas, únicos, seguros -race.
func TestSeqIDGen_SequencialEUnico(t *testing.T) {
	t.Parallel()
	g := tk.NewSeqIDGen("h")
	if g.Next() != "h-1" || g.Next() != "h-2" {
		t.Fatal("SeqIDGen nao e sequencial 1,2,...")
	}
	g.Reset()
	if g.Next() != "h-1" {
		t.Fatal("Reset nao repos o contador")
	}

	// Concorrência: 200 Next em paralelo produzem 200 ids únicos.
	g2 := tk.NewSeqIDGen("id")
	const n = 200
	var wg sync.WaitGroup
	seen := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); seen[i] = g2.Next() }(i)
	}
	wg.Wait()
	uniq := map[string]bool{}
	for _, s := range seen {
		if uniq[s] {
			t.Fatalf("id duplicado sob concorrencia: %q", s)
		}
		uniq[s] = true
	}
	if len(uniq) != n {
		t.Fatalf("esperava %d ids unicos, obtive %d", n, len(uniq))
	}
}
