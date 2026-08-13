package planbudget_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/planbudget"
)

// planbudget_test.go — o adaptador é exercitado contra o ORÇAMENTO REAL de AOS-008
// (`budget.Budget`, hierarquia CAS), nunca contra um double: o que interessa provar
// é o ENCAIXE (a reserva sobe a cadeia de ancestrais e a confirmação fica), e um
// duplo de orçamento provaria apenas que o adaptador chama métodos.

// newTree monta uma árvore raiz→nó com o limite dado na raiz.
func newTree(t *testing.T, root budget.Amount, nodeLimit budget.Amount) *budget.Budget {
	t.Helper()
	b, err := budget.New("tree", root)
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	if err := b.AddNode("n", "tree", nodeLimit); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	return b
}

// TestDebitHitsTheWholeAncestry — o débito de uma decisão de ramo não fica no nó:
// sobe a cadeia até à raiz (é o invariante de ADR-008 que torna o orçamento da
// ÁRVORE uma coisa real). E fica CONFIRMADO, não meramente reservado.
func TestDebitHitsTheWholeAncestry(t *testing.T) {
	b := newTree(t, budget.Amount{Tokens: 100, CostMicroUSD: 100}, budget.Amount{Tokens: 100, CostMicroUSD: 100})
	m, err := planbudget.NewTreeBudgetMeter(b, budget.Amount{Tokens: 7, CostMicroUSD: 3})
	if err != nil {
		t.Fatalf("NewTreeBudgetMeter: %v", err)
	}
	ctx := context.Background()
	if err := m.ReserveConditionEval(ctx, "plan", "n"); err != nil {
		t.Fatalf("ReserveConditionEval: %v", err)
	}
	if err := m.CommitConditionEval(ctx, "plan", "n"); err != nil {
		t.Fatalf("CommitConditionEval: %v", err)
	}
	for _, id := range []string{"n", "tree"} {
		av, err := b.Available(id)
		if err != nil {
			t.Fatalf("Available(%q): %v", id, err)
		}
		if av.Tokens != 93 || av.CostMicroUSD != 97 {
			t.Fatalf("headroom de %q = %+v; queria menos 7 tokens e 3 micro-USD", id, av)
		}
	}
	snap := b.Snapshot()
	if snap["n"].Committed.Tokens != 7 {
		t.Fatalf("débito não CONFIRMADO no nó: %+v", snap["n"])
	}
}

// TestDebitFailsClosedWithoutHeadroom — sem headroom o débito FALHA (e o
// despachante, a jusante, mantém o nó em espera em vez de o podar). Nada fica
// reservado em nenhum nível: a reserva de AOS-008 faz rollback do prefixo.
func TestDebitFailsClosedWithoutHeadroom(t *testing.T) {
	b := newTree(t, budget.Amount{Tokens: 5, CostMicroUSD: 5}, budget.Amount{Tokens: 5, CostMicroUSD: 5})
	m, err := planbudget.NewTreeBudgetMeter(b, budget.Amount{Tokens: 9, CostMicroUSD: 1})
	if err != nil {
		t.Fatalf("NewTreeBudgetMeter: %v", err)
	}
	if err := m.ReserveConditionEval(context.Background(), "plan", "n"); err == nil {
		t.Fatal("débito passou sem headroom: o breaker de ADR-008 não foi consultado")
	}
	av, _ := b.Available("tree")
	if av.Tokens != 5 || av.CostMicroUSD != 5 {
		t.Fatalf("headroom da raiz mexeu numa reserva falhada: %+v", av)
	}
}

// TestMeterConstructionIsFailClosed — um custo NULO seria «avaliar de graça», que é
// exactamente o que ADR-022 §2.4(4) exclui; e um reserver nil é o débito a
// desaparecer em silêncio. Ambos falham na CONSTRUÇÃO.
func TestMeterConstructionIsFailClosed(t *testing.T) {
	b := newTree(t, budget.Amount{Tokens: 10, CostMicroUSD: 10}, budget.Amount{Tokens: 10, CostMicroUSD: 10})
	for name, args := range map[string]struct {
		r    budget.Reserver
		cost budget.Amount
	}{
		"reserver nil":   {nil, budget.Amount{Tokens: 1}},
		"custo nulo":     {b, budget.Amount{}},
		"custo negativo": {b, budget.Amount{Tokens: -1}},
	} {
		if _, err := planbudget.NewTreeBudgetMeter(args.r, args.cost); !errors.Is(err, planbudget.ErrMeterDeps) {
			t.Fatalf("%s: err = %v; queria ErrMeterDeps", name, err)
		}
	}
}

// TestReleaseUndoesReservation — o invariante «débito por DECISÃO, não por
// tentativa» sob falha do registo. O despachante RESERVA, tenta apensar o facto,
// falha, e LIBERTA. Repetido N vezes (a indisponibilidade do Event Store é
// persistente), o orçamento da árvore TEM de ficar exactamente onde estava — sem
// esta fase o mesmo nó era debitado N vezes e a árvore drenava à espera.
func TestReleaseUndoesReservation(t *testing.T) {
	b := newTree(t, budget.Amount{Tokens: 100, CostMicroUSD: 100}, budget.Amount{Tokens: 100, CostMicroUSD: 100})
	m, err := planbudget.NewTreeBudgetMeter(b, budget.Amount{Tokens: 7, CostMicroUSD: 3})
	if err != nil {
		t.Fatalf("NewTreeBudgetMeter: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := m.ReserveConditionEval(ctx, "plan", "n"); err != nil {
			t.Fatalf("passagem %d: ReserveConditionEval: %v", i, err)
		}
		// ... aqui o journal FALHA ...
		if err := m.ReleaseConditionEval(ctx, "plan", "n"); err != nil {
			t.Fatalf("passagem %d: ReleaseConditionEval: %v", i, err)
		}
	}
	for _, id := range []string{"n", "tree"} {
		av, err := b.Available(id)
		if err != nil {
			t.Fatalf("Available(%q): %v", id, err)
		}
		if av.Tokens != 100 || av.CostMicroUSD != 100 {
			t.Fatalf("5 passagens abortadas drenaram o orçamento de %q: %+v", id, av)
		}
	}
	if snap := b.Snapshot(); snap["n"].Committed.Tokens != 0 {
		t.Fatalf("uma decisão NUNCA registada ficou confirmada: %+v", snap["n"])
	}
}

// TestCommitOrReleaseWithoutReserveIsFailClosed — confirmar (ou libertar) um débito
// que ninguém reservou seria inventar contabilidade. Ambos recusam.
func TestCommitOrReleaseWithoutReserveIsFailClosed(t *testing.T) {
	b := newTree(t, budget.Amount{Tokens: 10, CostMicroUSD: 10}, budget.Amount{Tokens: 10, CostMicroUSD: 10})
	m, err := planbudget.NewTreeBudgetMeter(b, budget.Amount{Tokens: 1, CostMicroUSD: 1})
	if err != nil {
		t.Fatalf("NewTreeBudgetMeter: %v", err)
	}
	ctx := context.Background()
	if err := m.CommitConditionEval(ctx, "plan", "n"); !errors.Is(err, planbudget.ErrNoPendingReservation) {
		t.Fatalf("Commit sem Reserve: err = %v; queria ErrNoPendingReservation", err)
	}
	if err := m.ReleaseConditionEval(ctx, "plan", "n"); !errors.Is(err, planbudget.ErrNoPendingReservation) {
		t.Fatalf("Release sem Reserve: err = %v; queria ErrNoPendingReservation", err)
	}
}
