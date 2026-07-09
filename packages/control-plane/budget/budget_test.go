package budget

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func amt(tokens, cost int64) Amount { return Amount{Tokens: tokens, CostMicroUSD: cost} }

// mustBudget constrói uma árvore raiz simples.
func mustBudget(t *testing.T, limit Amount) *Budget {
	t.Helper()
	b, err := New("tree-1", limit)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

// TestReserve_ConcurrentCAS_ZeroOvershoot é o teste central do ADR-008: N
// goroutines reservam a MESMA quantia contra um limite que só admite K reservas.
// A soma reservada NUNCA pode exceder o limite (0 overshoot). Corre sob -race.
func TestReserve_ConcurrentCAS_ZeroOvershoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		goroutines = 200
		perTokens  = 10
		perCost    = 3
		capacity   = goroutines / 2 // metade das reservas cabem
	)
	// Limite admite exactamente `capacity` reservas na dimensão MAIS restritiva.
	// tokens: capacity*perTokens ; custo: capacity*perCost — ambas alinhadas para
	// que o teto seja inequívoco.
	limit := amt(capacity*perTokens, capacity*perCost)
	b := mustBudget(t, limit)

	var ok, denied atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // largada simultânea maximiza a contenção
			_, err := b.Reserve(ctx, "tree-1", amt(perTokens, perCost))
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrNoHeadroom):
				denied.Add(1)
			default:
				t.Errorf("erro inesperado: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	// Invariante dura: o reservado nunca excede o limite em NENHUMA dimensão.
	snap := b.Snapshot()["tree-1"]
	if snap.Reserved.Tokens > limit.Tokens || snap.Reserved.CostMicroUSD > limit.CostMicroUSD {
		t.Fatalf("OVERSHOOT: reservado %v excede limite %v", snap.Reserved, limit)
	}
	// E deve ter admitido exactamente o que cabe (nem a mais nem a menos).
	if got := ok.Load(); got != capacity {
		t.Errorf("admitidas = %d, quero exactamente %d (sem overshoot nem sub-admissao)", got, capacity)
	}
	if got := denied.Load(); got != goroutines-capacity {
		t.Errorf("negadas = %d, quero %d", got, goroutines-capacity)
	}
	if snap.Reserved != amt(capacity*perTokens, capacity*perCost) {
		t.Errorf("reservado final = %v, quero %v", snap.Reserved, amt(capacity*perTokens, capacity*perCost))
	}
}

// TestReserve_ConcurrentSharedAncestor_ZeroOvershoot é o cenário real do ADR-008
// (map-reduce recursivo): várias sub-árvores IRMÃS a disputar concorrentemente o
// tecto de um ANCESTRAL comum. N goroutines reservam repartidas por duas sub-
// árvores cujo limite é generoso; o tecto REAL é a raiz partilhada. Prova
// atomicidade/0-overshoot NO NÍVEL INTERMÉDIO (o ancestral), não só numa folha=
// raiz plana. Corre sob -race.
func TestReserve_ConcurrentSharedAncestor_ZeroOvershoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		goroutines = 200
		perTokens  = 10
		perCost    = 3
		capacity   = goroutines / 2 // metade das reservas cabem no tecto da RAIZ
	)
	rootLimit := amt(capacity*perTokens, capacity*perCost)
	b := mustBudget(t, rootLimit)
	// Dois filhos irmãos com limites folgados (o dobro do tecto da raiz): sozinhos
	// admitiriam tudo, mas o ancestral partilhado é o tecto efectivo.
	childLimit := amt(capacity*perTokens*2, capacity*perCost*2)
	if err := b.AddNode("child-a", "tree-1", childLimit); err != nil {
		t.Fatalf("AddNode child-a: %v", err)
	}
	if err := b.AddNode("child-b", "tree-1", childLimit); err != nil {
		t.Fatalf("AddNode child-b: %v", err)
	}

	var ok, denied atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		nodeID := "child-a"
		if i%2 == 1 {
			nodeID = "child-b"
		}
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			<-start // largada simultânea maximiza a contenção no ancestral
			_, err := b.Reserve(ctx, nodeID, amt(perTokens, perCost))
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrNoHeadroom):
				denied.Add(1)
			default:
				t.Errorf("erro inesperado: %v", err)
			}
		}(nodeID)
	}
	close(start)
	wg.Wait()

	snap := b.Snapshot()
	root := snap["tree-1"]
	// Invariante dura NO ANCESTRAL: o reservado na raiz nunca excede o seu limite.
	if root.Reserved.Tokens > rootLimit.Tokens || root.Reserved.CostMicroUSD > rootLimit.CostMicroUSD {
		t.Fatalf("OVERSHOOT no ancestral: reservado %v excede limite %v", root.Reserved, rootLimit)
	}
	// Admite exactamente o que cabe no tecto do ancestral (nem a mais nem a menos).
	if got := ok.Load(); got != capacity {
		t.Errorf("admitidas = %d, quero exactamente %d", got, capacity)
	}
	if got := denied.Load(); got != goroutines-capacity {
		t.Errorf("negadas = %d, quero %d", got, goroutines-capacity)
	}
	if root.Reserved != amt(capacity*perTokens, capacity*perCost) {
		t.Errorf("reservado na raiz = %v, quero %v", root.Reserved, amt(capacity*perTokens, capacity*perCost))
	}
	// Contabilidade coerente: a soma reservada nos dois filhos iguala a da raiz
	// (cada reserva numa folha sobe a cadeia e debita o ancestral exactamente uma
	// vez).
	if sum := snap["child-a"].Reserved.Add(snap["child-b"].Reserved); sum != root.Reserved {
		t.Errorf("soma reservada nos filhos %v != reservado na raiz %v", sum, root.Reserved)
	}
}

// TestReserve_TwoDimensions verifica que o headroom é avaliado nas DUAS
// dimensões: nega se não couber em tokens OU em custo, isoladamente.
func TestReserve_TwoDimensions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tests := []struct {
		name    string
		limit   Amount
		reserve Amount
		wantErr error
	}{
		{"cabe nas duas", amt(100, 100), amt(50, 50), nil},
		{"falha so em tokens", amt(10, 100), amt(50, 50), ErrNoHeadroom},
		{"falha so em custo", amt(100, 10), amt(50, 50), ErrNoHeadroom},
		{"exacto nas duas", amt(50, 50), amt(50, 50), nil},
		{"reserva zero invalida", amt(100, 100), amt(0, 0), ErrInvalidAmount},
		{"reserva negativa invalida", amt(100, 100), amt(-1, 10), ErrInvalidAmount},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := mustBudget(t, tc.limit)
			_, err := b.Reserve(ctx, "tree-1", tc.reserve)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, quero %v", err, tc.wantErr)
			}
		})
	}
}

// TestReserve_HierarchyConsumesAncestors: uma reserva numa sub-árvore consome
// headroom em TODOS os ancestrais até à raiz.
func TestReserve_HierarchyConsumesAncestors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// raiz(100,100) → filho(80,80) → neto(80,80). Limites dos filhos generosos;
	// o tecto REAL é a raiz.
	b := mustBudget(t, amt(100, 100))
	if err := b.AddNode("child", "tree-1", amt(80, 80)); err != nil {
		t.Fatalf("AddNode child: %v", err)
	}
	if err := b.AddNode("grand", "child", amt(80, 80)); err != nil {
		t.Fatalf("AddNode grand: %v", err)
	}

	if _, err := b.Reserve(ctx, "grand", amt(30, 30)); err != nil {
		t.Fatalf("Reserve grand: %v", err)
	}
	// A reserva de 30 no neto consome 30 em neto, filho E raiz.
	snap := b.Snapshot()
	for _, id := range []string{"grand", "child", "tree-1"} {
		if snap[id].Reserved != amt(30, 30) {
			t.Errorf("no %s reservado = %v, quero {30,30}", id, snap[id].Reserved)
		}
	}
	// A raiz tem agora 70 livres: uma segunda reserva de 80 no neto (que caberia
	// no limite 80 do neto e do filho) é NEGADA pelo tecto da raiz.
	if _, err := b.Reserve(ctx, "grand", amt(80, 80)); !errors.Is(err, ErrNoHeadroom) {
		t.Fatalf("esperava ErrNoHeadroom pelo tecto da raiz, obtive %v", err)
	}
}

// TestReserve_RollbackOnAncestorFailure: quando um ancestral não tem headroom, os
// níveis já debitados na tentativa são revertidos — SEM débito parcial residual.
func TestReserve_RollbackOnAncestorFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// filho generoso (1000) mas raiz apertada (10): reservar 50 no filho debita o
	// filho e depois FALHA na raiz → rollback do filho.
	b := mustBudget(t, amt(10, 10))
	if err := b.AddNode("child", "tree-1", amt(1000, 1000)); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	_, err := b.Reserve(ctx, "child", amt(50, 50))
	if !errors.Is(err, ErrNoHeadroom) {
		t.Fatalf("esperava ErrNoHeadroom, obtive %v", err)
	}
	// Nenhum débito residual em lado nenhum (rollback completo).
	snap := b.Snapshot()
	if snap["child"].Reserved != (Amount{}) {
		t.Errorf("filho ficou com débito residual %v apos rollback", snap["child"].Reserved)
	}
	if snap["tree-1"].Reserved != (Amount{}) {
		t.Errorf("raiz ficou com débito residual %v", snap["tree-1"].Reserved)
	}
	// E a árvore continua utilizável: uma reserva que cabe na raiz passa.
	if _, err := b.Reserve(ctx, "child", amt(10, 10)); err != nil {
		t.Fatalf("Reserve apos rollback devia passar: %v", err)
	}
}

// TestCommit_ConvertsReservedToCommitted em toda a cadeia.
func TestCommit_ConvertsReservedToCommitted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := mustBudget(t, amt(100, 100))
	_ = b.AddNode("child", "tree-1", amt(100, 100))

	r, err := b.Reserve(ctx, "child", amt(40, 40))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := b.Commit(ctx, r); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	snap := b.Snapshot()
	for _, id := range []string{"child", "tree-1"} {
		if snap[id].Reserved != (Amount{}) || snap[id].Committed != amt(40, 40) {
			t.Errorf("no %s = {res:%v com:%v}, quero {res:0 com:40}", id, snap[id].Reserved, snap[id].Committed)
		}
	}
	// Available reflecte o débito final.
	av, _ := b.Available("tree-1")
	if av != amt(60, 60) {
		t.Errorf("Available = %v, quero {60,60}", av)
	}
}

// TestRelease_NoLeakOnCancellation: reserva libertada devolve todo o headroom
// (rollback), sem leak — o cenário de falha/cancelamento.
func TestRelease_NoLeakOnCancellation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := mustBudget(t, amt(100, 100))
	_ = b.AddNode("child", "tree-1", amt(100, 100))

	r, err := b.Reserve(ctx, "child", amt(70, 70))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// Simula falha/cancelamento do spawn: liberta a reserva.
	if err := b.Release(ctx, r); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Headroom totalmente recuperado em toda a cadeia (sem leak).
	snap := b.Snapshot()
	for _, id := range []string{"child", "tree-1"} {
		if snap[id].Reserved != (Amount{}) || snap[id].Committed != (Amount{}) {
			t.Errorf("no %s com leak: {res:%v com:%v}", id, snap[id].Reserved, snap[id].Committed)
		}
	}
	// A capacidade total volta a estar disponível.
	if _, err := b.Reserve(ctx, "child", amt(100, 100)); err != nil {
		t.Fatalf("apos release a capacidade total devia estar livre: %v", err)
	}
}

// TestReservation_Idempotency cobre a máquina de estados: idempotência de commit
// e release, e os erros commit-após-release / release-após-commit.
func TestReservation_Idempotency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("commit idempotente", func(t *testing.T) {
		t.Parallel()
		b := mustBudget(t, amt(100, 100))
		r, _ := b.Reserve(ctx, "tree-1", amt(10, 10))
		if err := b.Commit(ctx, r); err != nil {
			t.Fatalf("1o commit: %v", err)
		}
		if err := b.Commit(ctx, r); err != nil {
			t.Fatalf("2o commit devia ser no-op: %v", err)
		}
		// O débito é aplicado UMA vez (não duplicado).
		if got := b.Snapshot()["tree-1"].Committed; got != amt(10, 10) {
			t.Errorf("committed = %v, quero {10,10} (sem duplo débito)", got)
		}
	})

	t.Run("release idempotente", func(t *testing.T) {
		t.Parallel()
		b := mustBudget(t, amt(100, 100))
		r, _ := b.Reserve(ctx, "tree-1", amt(10, 10))
		if err := b.Release(ctx, r); err != nil {
			t.Fatalf("1o release: %v", err)
		}
		if err := b.Release(ctx, r); err != nil {
			t.Fatalf("2o release devia ser no-op: %v", err)
		}
		if got := b.Snapshot()["tree-1"].Reserved; got != (Amount{}) {
			t.Errorf("reserved = %v, quero 0", got)
		}
	})

	t.Run("commit apos release erra", func(t *testing.T) {
		t.Parallel()
		b := mustBudget(t, amt(100, 100))
		r, _ := b.Reserve(ctx, "tree-1", amt(10, 10))
		if err := b.Release(ctx, r); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if err := b.Commit(ctx, r); !errors.Is(err, ErrCommitAfterRelease) {
			t.Fatalf("Commit apos release = %v, quero ErrCommitAfterRelease", err)
		}
	})

	t.Run("release apos commit erra", func(t *testing.T) {
		t.Parallel()
		b := mustBudget(t, amt(100, 100))
		r, _ := b.Reserve(ctx, "tree-1", amt(10, 10))
		if err := b.Commit(ctx, r); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if err := b.Release(ctx, r); !errors.Is(err, ErrReleaseAfterCommit) {
			t.Fatalf("Release apos commit = %v, quero ErrReleaseAfterCommit", err)
		}
	})
}

// TestSettle_ConcurrentCommitRelease: commit e release concorrentes da MESMA
// reserva — exactamente um vence, o débito é aplicado uma só vez, sem corrida
// (sob -race). Prova a exclusão mútua por CAS na máquina de estados.
func TestSettle_ConcurrentCommitRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const trials = 100
	commits, releases := 0, 0
	for i := 0; i < trials; i++ {
		b := mustBudget(t, amt(100, 100))
		r, _ := b.Reserve(ctx, "tree-1", amt(10, 10))

		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); errs[0] = b.Commit(ctx, r) }()
		go func() { defer wg.Done(); errs[1] = b.Release(ctx, r) }()
		wg.Wait()

		// Exactamente um sucesso e um erro de transição oposta.
		nSuccess := 0
		for _, e := range errs {
			if e == nil {
				nSuccess++
			} else if !errors.Is(e, ErrCommitAfterRelease) && !errors.Is(e, ErrReleaseAfterCommit) {
				t.Fatalf("erro inesperado: %v", e)
			}
		}
		if nSuccess != 1 {
			t.Fatalf("esperava exactamente 1 sucesso, obtive %d", nSuccess)
		}
		snap := b.Snapshot()["tree-1"]
		// O estado é coerente: ou committed=10/reserved=0, ou tudo a zero.
		committed := snap.Committed == amt(10, 10) && snap.Reserved == (Amount{})
		released := snap.Committed == (Amount{}) && snap.Reserved == (Amount{})
		if !committed && !released {
			t.Fatalf("estado incoerente apos corrida: %+v", snap)
		}
		if committed {
			commits++
		} else {
			releases++
		}
	}
	t.Logf("resultado de %d corridas: %d commits, %d releases", trials, commits, releases)
}

// TestErrorsAndEdgeCases cobre nós/reservas inexistentes e limites inválidos.
func TestErrorsAndEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := mustBudget(t, amt(100, 100))

	if _, err := b.Reserve(ctx, "inexistente", amt(1, 1)); !errors.Is(err, ErrUnknownNode) {
		t.Errorf("Reserve no inexistente = %v, quero ErrUnknownNode", err)
	}
	if err := b.Commit(ctx, Reservation{ID: "nao-existe"}); !errors.Is(err, ErrReservationNotFound) {
		t.Errorf("Commit reserva inexistente = %v, quero ErrReservationNotFound", err)
	}
	if err := b.Release(ctx, Reservation{ID: "nao-existe"}); !errors.Is(err, ErrReservationNotFound) {
		t.Errorf("Release reserva inexistente = %v, quero ErrReservationNotFound", err)
	}
	if _, err := b.Available("inexistente"); !errors.Is(err, ErrUnknownNode) {
		t.Errorf("Available no inexistente = %v, quero ErrUnknownNode", err)
	}
	if err := b.AddNode("x", "inexistente", amt(1, 1)); !errors.Is(err, ErrUnknownParent) {
		t.Errorf("AddNode parent inexistente = %v, quero ErrUnknownParent", err)
	}
	if err := b.AddNode("tree-1", "tree-1", amt(1, 1)); !errors.Is(err, ErrNodeExists) {
		t.Errorf("AddNode duplicado = %v, quero ErrNodeExists", err)
	}
	if err := b.AddNode("bad", "tree-1", amt(-1, 0)); !errors.Is(err, ErrInvalidLimit) {
		t.Errorf("AddNode limite negativo = %v, quero ErrInvalidLimit", err)
	}
	if _, err := New("t", amt(-1, 0)); !errors.Is(err, ErrInvalidLimit) {
		t.Errorf("New limite negativo = %v, quero ErrInvalidLimit", err)
	}
	// Contexto cancelado é fail-closed em Reserve.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.Reserve(cctx, "tree-1", amt(1, 1)); err == nil {
		t.Error("Reserve com contexto cancelado devia falhar")
	}
}
