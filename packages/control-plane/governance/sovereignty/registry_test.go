package sovereignty_test

import (
	"testing"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
)

// TestRegionFor_Known resolve a região autorizada de um board registado.
func TestRegionFor_Known(t *testing.T) {
	t.Parallel()
	r := govsov.NewRegistry(map[string]string{"board-eu": "eu", "board-us": "us"})
	got, ok := r.RegionFor("board-eu")
	if !ok || got != "eu" {
		t.Fatalf("RegionFor(board-eu) = (%q,%v); quero (eu,true)", got, ok)
	}
}

// TestRegionFor_CaseInsensitive — a resolução é normalizada (caixa/espaços).
func TestRegionFor_CaseInsensitive(t *testing.T) {
	t.Parallel()
	r := govsov.NewRegistry(map[string]string{"Board-EU": "EU"})
	got, ok := r.RegionFor("  board-eu ")
	if !ok || got != "eu" {
		t.Fatalf("RegionFor normalizado = (%q,%v); quero (eu,true)", got, ok)
	}
}

// TestRegionFor_UnknownBoard_FailClosed — board desconhecido ⇒ ("",false), nunca
// uma região por omissão (ADR-011).
func TestRegionFor_UnknownBoard_FailClosed(t *testing.T) {
	t.Parallel()
	r := govsov.NewRegistry(map[string]string{"board-eu": "eu"})
	if got, ok := r.RegionFor("board-desconhecido"); ok || got != "" {
		t.Fatalf("board desconhecido = (%q,%v); quero (\"\",false) fail-closed", got, ok)
	}
}

// TestRegionFor_EmptyBoard_FailClosed — board vazio nunca resolve.
func TestRegionFor_EmptyBoard_FailClosed(t *testing.T) {
	t.Parallel()
	r := govsov.NewRegistry(map[string]string{"board-eu": "eu"})
	if _, ok := r.RegionFor(""); ok {
		t.Fatal("board vazio resolveu; quero fail-closed")
	}
}

// TestRegionFor_NilRegistry_FailClosed — um registo nil resolve tudo para não-autorizado.
func TestRegionFor_NilRegistry_FailClosed(t *testing.T) {
	t.Parallel()
	var r *govsov.Registry
	if _, ok := r.RegionFor("board-eu"); ok {
		t.Fatal("Registry nil resolveu; quero fail-closed")
	}
	if r.Len() != 0 {
		t.Fatalf("Len nil = %d; quero 0", r.Len())
	}
}

// TestNewRegistry_DropsEmptyEntries — entradas com board/região vazios são
// descartadas (nunca criam fronteira indefinida).
func TestNewRegistry_DropsEmptyEntries(t *testing.T) {
	t.Parallel()
	r := govsov.NewRegistry(map[string]string{
		"board-eu": "eu",
		"":         "us", // board vazio: descartado
		"board-x":  "",   // região vazia: descartada
		"  ":       "  ", // ambos vazios: descartado
	})
	if r.Len() != 1 {
		t.Fatalf("Len = %d; quero 1 (só board-eu válido)", r.Len())
	}
	if _, ok := r.RegionFor("board-x"); ok {
		t.Fatal("board-x com região vazia não devia estar registado")
	}
}

// TestNewRegistry_Immutable — mutar o mapa de origem após a construção NÃO afecta o
// registo (cópia defensiva).
func TestNewRegistry_Immutable(t *testing.T) {
	t.Parallel()
	src := map[string]string{"board-eu": "eu"}
	r := govsov.NewRegistry(src)
	src["board-eu"] = "us" // tentativa de mutação externa
	src["board-novo"] = "us"
	if got, _ := r.RegionFor("board-eu"); got != "eu" {
		t.Fatalf("região = %q; mutação externa vazou (quero eu)", got)
	}
	if _, ok := r.RegionFor("board-novo"); ok {
		t.Fatal("board-novo vazou da mutação externa")
	}
}

// TestAuthorized — a verificação de fronteira (allowlist regional / PEP).
func TestAuthorized(t *testing.T) {
	t.Parallel()
	r := govsov.NewRegistry(map[string]string{"board-eu": "eu"})
	cases := []struct {
		board, region string
		want          bool
	}{
		{"board-eu", "eu", true},
		{"board-eu", "EU", true},            // case-insensitive
		{"board-eu", "us", false},           // cross-border
		{"board-desconhecido", "eu", false}, // board desconhecido: fail-closed
		{"board-eu", "", false},             // região vazia: fail-closed
		{"", "eu", false},                   // board vazio: fail-closed
	}
	for _, c := range cases {
		if got := r.Authorized(c.board, c.region); got != c.want {
			t.Errorf("Authorized(%q,%q) = %v; quero %v", c.board, c.region, got, c.want)
		}
	}
}
