package scheduler

// Testes INTERNOS (package scheduler) da fórmula pura de derivação de max_spawn
// (AOS-028). Provam, sobre a própria função [deriveMaxSpawn], as três propriedades
// exigidas pelo ticket: derivação para vários níveis de headroom, MONOTONIA (mais
// headroom ⇒ ≥ spawns) e que o valor NÃO é constante (varia com o headroom, é 0
// sob headroom nulo). Determinístico: função pura, sem relógio nem estado.

import "testing"

func TestDeriveMaxSpawn_Levels(t *testing.T) {
	t.Parallel()
	const cost = int64(100)
	// Requests abundantes: a dimensão limitante é tokens (headroom/cost).
	const reqs = int64(1_000_000)
	cases := []struct {
		name           string
		headroomTokens int64
		want           int
	}{
		{"headroom nulo => 0 (nunca oversubscription)", 0, 0},
		{"headroom < custo => 0", 99, 0},
		{"headroom == custo => 1", 100, 1},
		{"headroom 5x custo => 5", 500, 5},
		{"headroom 10x custo => 10", 1000, 10},
		{"headroom nao-multiplo trunca (450/100=4)", 450, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveMaxSpawn(tc.headroomTokens, reqs, cost)
			if got != tc.want {
				t.Fatalf("deriveMaxSpawn(%d, _, %d) = %d, quero %d", tc.headroomTokens, cost, got, tc.want)
			}
		})
	}
}

// TestDeriveMaxSpawn_NotConstant prova que max_spawn NÃO é uma constante
// hard-coded: com o mesmo custo, headrooms distintos produzem valores distintos.
func TestDeriveMaxSpawn_NotConstant(t *testing.T) {
	t.Parallel()
	const cost = int64(50)
	const reqs = int64(1_000_000)
	seen := map[int]bool{}
	for _, h := range []int64{0, 50, 100, 250, 500, 1000, 5000} {
		seen[deriveMaxSpawn(h, reqs, cost)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("max_spawn parece constante (%d valores distintos); esperava variação com o headroom", len(seen))
	}
}

// TestDeriveMaxSpawn_Monotone prova a monotonia não-decrescente no headroom: mais
// headroom nunca reduz o número de spawns permitidos.
func TestDeriveMaxSpawn_Monotone(t *testing.T) {
	t.Parallel()
	const cost = int64(70)
	const reqs = int64(1_000_000)
	prev := -1
	for h := int64(0); h <= 10_000; h += 37 {
		got := deriveMaxSpawn(h, reqs, cost)
		if got < prev {
			t.Fatalf("monotonia violada em headroom=%d: %d < anterior %d", h, got, prev)
		}
		prev = got
	}
}

// TestDeriveMaxSpawn_RequestsBind prova que a dimensão de requests também limita:
// com tokens abundantes mas poucos requests, max_spawn é o nº de requests (1 por
// sub-agente).
func TestDeriveMaxSpawn_RequestsBind(t *testing.T) {
	t.Parallel()
	// Tokens dariam 1000 spawns, mas só há 3 requests de headroom.
	if got := deriveMaxSpawn(100_000, 3, 100); got != 3 {
		t.Fatalf("deriveMaxSpawn limitado por requests = %d, quero 3", got)
	}
}

// TestDeriveMaxSpawn_CostNormalized prova que custo <=0 é normalizado para 1
// (evita divisão por zero / spawn ilimitado).
func TestDeriveMaxSpawn_CostNormalized(t *testing.T) {
	t.Parallel()
	if got := deriveMaxSpawn(10, 1_000_000, 0); got != 10 {
		t.Fatalf("deriveMaxSpawn com custo 0 = %d, quero 10 (custo normalizado para 1)", got)
	}
	if got := deriveMaxSpawn(10, 1_000_000, -5); got != 10 {
		t.Fatalf("deriveMaxSpawn com custo negativo = %d, quero 10", got)
	}
}
