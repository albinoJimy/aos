package integration

import (
	"testing"

	"github.com/aos-ref/platform/audit"
)

// cyclicWormDecorator é um decorador de [audit.Store] cujo Unwrap forma um ciclo — o caso
// patológico que o desembrulho de [wormBaseStore] tem de tolerar sem PENDURAR (SHOULD-2, AOS-381).
type cyclicWormDecorator struct {
	*audit.MemStore
	next audit.Store
}

func (c *cyclicWormDecorator) Unwrap() audit.Store { return c.next }

// TestWormBaseStore_TerminaEmCicloEnaoPendura fixa a invariante fail-safe: uma cadeia de Unwrap
// mal-composta (ciclo A→B→A) NÃO pode pendurar o arranque do nó num gate de segurança. O limite
// de iterações garante o retorno; e um ciclo patológico não iguala o base de um WORM real, pelo
// que o fail-closed do ápice RECUSA (a direcção segura).
func TestWormBaseStore_TerminaEmCicloEnaoPendura(t *testing.T) {
	t.Parallel()
	a := &cyclicWormDecorator{MemStore: audit.NewMemStore()}
	b := &cyclicWormDecorator{MemStore: audit.NewMemStore()}
	a.next = b
	b.next = a

	got := wormBaseStore(a) // se pendurasse, o teste esgotava o timeout
	if got == nil {
		t.Fatal("wormBaseStore devolveu nil")
	}

	real := audit.NewMemStore()
	if wormBaseStore(a) == wormBaseStore(real) {
		t.Fatal("um ciclo patologico nao devia igualar o base de um WORM real — o gate deixaria de recusar")
	}
}

// TestWormBaseStore_DesembrulhaDelegante prova o caso legítimo: um decorador que delega
// honestamente (Unwrap devolve o inner onde sela) colapsa para o mesmo base do inner — é o que
// permite ao fail-closed aceitar o revalidador selado ao WORM decorado com observabilidade.
func TestWormBaseStore_DesembrulhaDelegante(t *testing.T) {
	t.Parallel()
	base := audit.NewMemStore()
	deco := &cyclicWormDecorator{MemStore: base, next: base} // Unwrap → base (delega)
	if wormBaseStore(deco) != base {
		t.Fatal("um decorador que delega ao base devia colapsar para esse base")
	}
	if wormBaseStore(deco) != wormBaseStore(base) {
		t.Fatal("base(decorador) tem de igualar base(store cru) para o WORM unico valer sob observabilidade")
	}
}
