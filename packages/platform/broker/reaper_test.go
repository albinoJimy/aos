package broker

import (
	"context"
	"testing"
	"time"
)

// TestReapExpired_EvictaSoNaoInjectaveis prova que o reaper (AOS-264) liberta a
// memória das leases expiradas/revogadas e NÃO toca nas ainda usáveis.
func TestReapExpired_EvictaSoNaoInjectaveis(t *testing.T) {
	st := newStack(t, ttl)

	// duas trocas usáveis (mesma capability in-scope, runs distintos).
	h1, err := st.broker.Exchange(context.Background(), request("run-1", provInScopeCap))
	if err != nil {
		t.Fatalf("Exchange run-1: %v", err)
	}
	h2, err := st.broker.Exchange(context.Background(), request("run-2", provInScopeCap))
	if err != nil {
		t.Fatalf("Exchange run-2: %v", err)
	}

	// dentro do TTL e sem revogação: reap NÃO evicta nada.
	if n := st.broker.ReapExpired(st.clock.now()); n != 0 {
		t.Fatalf("reap dentro do TTL evictou %d (esperado 0)", n)
	}
	if _, ok := st.broker.store.get(h1); !ok {
		t.Fatal("lease usavel foi evictada")
	}

	// revoga a lease de run-1: passa a reapável (revogada), embora dentro do TTL.
	if !st.broker.Revoke(leaseIDFromHandle(st, h1)) {
		t.Fatal("Revoke devia encontrar a lease de run-1")
	}
	if n := st.broker.ReapExpired(st.clock.now()); n != 1 {
		t.Fatalf("reap devia evictar 1 revogada, evictou %d", n)
	}
	if _, ok := st.broker.store.get(h1); ok {
		t.Fatal("lease revogada nao foi evictada")
	}
	if _, ok := st.broker.store.get(h2); !ok {
		t.Fatal("reap evictou a lease usavel de run-2 por engano")
	}

	// avança para lá do TTL: a de run-2 fica expirada e é evictada.
	st.clock.advance(ttl + time.Second)
	if n := st.broker.ReapExpired(st.clock.now()); n != 1 {
		t.Fatalf("reap devia evictar 1 expirada, evictou %d", n)
	}
	if _, ok := st.broker.store.get(h2); ok {
		t.Fatal("lease expirada nao foi evictada")
	}

	// idempotente: um segundo reap sobre o store vazio evicta 0.
	if n := st.broker.ReapExpired(st.clock.now()); n != 0 {
		t.Fatalf("reap idempotente devia evictar 0, evictou %d", n)
	}
}
