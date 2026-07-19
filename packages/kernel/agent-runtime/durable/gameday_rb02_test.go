package durable

// gameday_rb02_test.go — GAME DAY do RB-02 (zumbi cross-host, ADR-001).
//
// Injecta o MODO DE FALHA REAL — um worker que aparenta estar `running` mas fica ZUMBI
// (pára de fazer heartbeat) — e prova a MITIGAÇÃO-CHAVE sobre a infra durável REAL
// (AOS-017/018): a posse decide-se por LEASE/HEARTBEAT + FENCING TOKEN, NUNCA por PID;
// deixa-se o lease EXPIRAR, outra réplica REATRIBUI com um novo fencing token
// estritamente maior, e as escritas do worker OBSOLETO são INVALIDADAS
// ([ErrStaleFencingToken]) — a tarefa não executa duas vezes. Também prova o
// FALSO-POSITIVO guard: enquanto o lease é válido (heartbeat), NÃO se reatribui um
// worker vivo (distingue-se de um zumbi / de `waiting_on_human`).
//
// Reutiliza os helpers de lease_test.go/fencing_test.go (newStore, newManager,
// newTestClock, businessWrite, committedWork, ttl) — infra real, sem stubs.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestGameDay_RB02_ZombieReassignedWithFencing prova sinal→diagnóstico→mitigação do RB-02.
func TestGameDay_RB02_ZombieReassignedWithFencing(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	store := newStore(t)
	m := newManager(t, store, clk)
	fa, err := NewFencedAppender(store, m)
	if err != nil {
		t.Fatalf("NewFencedAppender: %v", err)
	}
	ctx := context.Background()
	const run = "run-rb02"

	// Worker A reclama a partição (token 1) e faz progresso REAL (escrita fenced aceite).
	a, err := m.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim A: %v", err)
	}
	if _, err := fa.Append(ctx, run, a.Token, businessWrite(run, "a-1")); err != nil {
		t.Fatalf("escrita legítima de A: %v", err)
	}

	// FALSO-POSITIVO guard (diagnóstico): A ainda está VIVO. Um heartbeat renova o lease;
	// enquanto o lease é corrente e não expirou, uma tentativa de reatribuição é RECUSADA
	// (ErrLeaseHeld). NÃO se mata um worker vivo por PID — distingue-se de zumbi e de
	// `waiting_on_human` (estado durável legítimo).
	clk.Advance(ttl / 2)
	if _, err := m.Heartbeat(ctx, a); err != nil {
		t.Fatalf("Heartbeat de A (vivo): %v", err)
	}
	if _, err := m.Claim(ctx, run); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("reatribuição de um worker VIVO = %v, quero ErrLeaseHeld (falso-positivo evitado)", err)
	}

	// SINAL: A torna-se ZUMBI — pára de fazer heartbeat. O lease EXPIRA por TTL (não se
	// inspecciona PID; a verdade é o relógio do lease).
	clk.Advance(ttl + time.Nanosecond)

	// MITIGAÇÃO: outra réplica (B) reatribui a partição, mintando um fencing token
	// ESTRITAMENTE MAIOR.
	b, err := m.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim B (após expiração): %v", err)
	}
	if b.Token.Value() <= a.Token.Value() {
		t.Fatalf("token de B (%d) não é > token de A (%d): monotonicidade violada", b.Token.Value(), a.Token.Value())
	}

	// O zumbi A "acorda" e tenta escrever com o token OBSOLETO → INVALIDADO
	// (ErrStaleFencingToken), sem tocar no Event Store. A tarefa não executa duas vezes.
	if _, err := fa.Append(ctx, run, a.Token, businessWrite(run, "a-zumbi")); !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("escrita do zumbi A = %v, quero ErrStaleFencingToken", err)
	}

	// B (detentor corrente) escreve e faz progresso — aceite.
	if _, err := fa.Append(ctx, run, b.Token, businessWrite(run, "b-1")); err != nil {
		t.Fatalf("escrita legítima de B: %v", err)
	}

	// RECUPERAÇÃO verificável: só as escritas legítimas (A antes de zombificar, B após
	// reatribuição) materializaram efeitos — 2, SEM duplicação da do zumbi.
	if got := committedWork(t, store, run); got != 2 {
		t.Fatalf("efeitos materializados = %d, quero 2 (a-1, b-1) — sem dupla execução", got)
	}
}
