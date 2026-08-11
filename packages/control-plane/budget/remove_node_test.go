package budget

import (
	"context"
	"errors"
	"testing"
)

// TestRemoveNode cobre o outro lado do ciclo de vida de [Budget.AddNode] (AOS-256): remover,
// re-registar o MESMO id (a retoma de um run reutiliza o RunID), a idempotência exigida por
// quem chama em `defer`, e a recusa de remover a raiz.
func TestRemoveNode(t *testing.T) {
	ctx := context.Background()
	b, err := New("tree", Amount{Tokens: 100, CostMicroUSD: 100})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := b.AddNode("run-1", "tree", Amount{Tokens: 10, CostMicroUSD: 10}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	// Remover ⇒ o nó deixa de existir para reservas novas.
	if err := b.RemoveNode("run-1"); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}
	if _, err := b.Reserve(ctx, "run-1", Amount{Tokens: 1}); !errors.Is(err, ErrUnknownNode) {
		t.Errorf("depois de RemoveNode a reserva devia dar ErrUnknownNode; err=%v", err)
	}

	// IDEMPOTENTE: uma segunda remoção é no-op (a libertação corre em `defer`).
	if err := b.RemoveNode("run-1"); err != nil {
		t.Errorf("RemoveNode devia ser idempotente; err=%v", err)
	}

	// O MESMO id volta a poder ser registado — sem isto, a retoma de um run colidiria.
	if err := b.AddNode("run-1", "tree", Amount{Tokens: 10, CostMicroUSD: 10}); err != nil {
		t.Errorf("re-registo do mesmo id depois da remocao devia ser aceite; err=%v", err)
	}

	// A RAIZ não se remove.
	if err := b.RemoveNode("tree"); !errors.Is(err, ErrRootRemoval) {
		t.Errorf("remover a raiz devia dar ErrRootRemoval; err=%v", err)
	}
}

// TestRemoveNodeNaoCorrompeReservaEmCurso prova a semântica documentada: uma reserva já
// emitida guarda a sua própria cadeia de ancestrais, pelo que Commit/Release continuam a
// debitar/creditar correctamente mesmo depois de o nó ser removido da árvore.
func TestRemoveNodeNaoCorrompeReservaEmCurso(t *testing.T) {
	ctx := context.Background()
	b, err := New("tree", Amount{Tokens: 100, CostMicroUSD: 100})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := b.AddNode("run-1", "tree", Amount{Tokens: 10, CostMicroUSD: 10}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	r, err := b.Reserve(ctx, "run-1", Amount{Tokens: 4, CostMicroUSD: 4})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := b.RemoveNode("run-1"); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}
	if err := b.Release(ctx, r); err != nil {
		t.Fatalf("Release depois de RemoveNode: %v", err)
	}
	// A raiz recuperou o headroom (o crédito subiu a cadeia guardada na reserva).
	if got := b.Snapshot()["tree"]; got.Reserved.Tokens != 0 {
		t.Errorf("a raiz ficou com %d tokens reservados depois do Release; esperado 0", got.Reserved.Tokens)
	}
}
