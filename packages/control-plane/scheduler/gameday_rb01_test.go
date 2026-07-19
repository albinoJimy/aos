package scheduler_test

// gameday_rb01_test.go — GAME DAY do RB-01 (colapso de rate limit agregado, ADR-008).
//
// Injecta o MODO DE FALHA REAL — headroom global esgotado (o token-bucket distribuído
// no chão) — e prova a MITIGAÇÃO-CHAVE do runbook sobre a infra REAL (AOS-027/028),
// SEM stubs: o admission control RECUSA (adia) spawns sem headroom, o sub-agente NÃO é
// criado, e o max_spawn derivado colapsa para 0. Depois RECUPERA: libertado o headroom,
// o spawn adiado volta a ser admitido.
//
// Compõe [scheduler.Admission] + [scheduler.SpawnCoordinator] reais sobre um Event Store
// real e um sub-orçamento real (budgetSpawner), reutilizando os helpers de
// spawn_admission_test.go. A vertente "Pool.Resize max=0 sob headroom nulo" (AOS-103) é
// provada pela infra do pool em substrate/sandbox (TestAutoscaler_ZeroHeadroomFailsClosed)
// — aqui foca-se o ENFORCEMENT do escalonador, a fonte de verdade da recusa.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/scheduler"
)

// TestGameDay_RB01_AdmissionRefusesSpawnsWithoutHeadroom prova o ciclo sinal→mitigação
// →recuperação do RB-01 sobre a infra real.
func TestGameDay_RB01_AdmissionRefusesSpawnsWithoutHeadroom(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	ctx := context.Background()

	// Provider com TPM = custo de UM sub-agente: há headroom para exactamente um spawn.
	// (RPM largo para isolar a causa no TPM — o eixo de tokens de ADR-008.)
	const cost = int64(1000)
	adm, _ := newAdmForSpawn(t, cost, 1_000_000, base)

	// Sub-orçamento da árvore FOLGADO — assim a única causa de recusa possível é o
	// headroom global (o modo de falha do RB-01), não o sub-orçamento (que seria RB-03).
	b, _ := budget.New("run-rb01", budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
	sp := &budgetSpawner{b: b}
	coord, err := scheduler.NewSpawnCoordinator(adm, sp, scheduler.WithSpawnClock(fixed(base)))
	if err != nil {
		t.Fatalf("NewSpawnCoordinator: %v", err)
	}
	slice := budget.Amount{Tokens: 500, CostMicroUSD: 500}

	// DIAGNÓSTICO: com headroom cheio, max_spawn derivado é > 0 (não constante).
	maxFull, err := coord.MaxSpawn(ctx, spawnKey, "", cost)
	if err != nil {
		t.Fatalf("MaxSpawn (cheio): %v", err)
	}
	if maxFull < 1 {
		t.Fatalf("max_spawn com headroom cheio = %d, quero >= 1", maxFull)
	}

	// Primeiro spawn: admitido (consome TODO o headroom do provider).
	out1, err := coord.RequestSpawn(ctx, spawnReq("run-rb01", "child-1", cost, slice))
	if err != nil {
		t.Fatalf("RequestSpawn #1: %v", err)
	}
	if !out1.Admitted || out1.Ticket == nil {
		t.Fatalf("spawn #1 devia ser admitido; got %+v", out1)
	}

	// SINAL: headroom no chão. max_spawn colapsa para 0 (derivado, não constante — a
	// causa-raiz clássica do colapso agregado é um max_spawn fixo).
	maxEmpty, err := coord.MaxSpawn(ctx, spawnKey, "", cost)
	if err != nil {
		t.Fatalf("MaxSpawn (esgotado): %v", err)
	}
	if maxEmpty != 0 {
		t.Fatalf("max_spawn sob headroom nulo = %d, quero 0 (fail-closed)", maxEmpty)
	}

	// MITIGAÇÃO: o segundo spawn é RECUSADO (adiado) por falta de headroom — o admission
	// control não faz oversubscription. O sub-agente NÃO é criado (o Delegator nem é tocado).
	spawnsBefore := sp.spawnCount.Load()
	out2, err := coord.RequestSpawn(ctx, spawnReq("run-rb01", "child-2", cost, slice))
	if err != nil {
		if !errors.Is(err, scheduler.ErrSpawnDeferredNoHeadroom) {
			t.Fatalf("RequestSpawn #2: erro = %v, quero ErrSpawnDeferredNoHeadroom", err)
		}
	}
	if out2.Admitted {
		t.Fatal("spawn #2 NÃO devia ser admitido sob headroom nulo (oversubscription!)")
	}
	if !out2.Deferred {
		t.Fatalf("spawn #2 devia ser adiado (backpressure); got %+v", out2)
	}
	if out2.RetryAfter <= 0 {
		t.Fatalf("defer sem RetryAfter aconselhado; got %v", out2.RetryAfter)
	}
	if got := sp.spawnCount.Load(); got != spawnsBefore {
		t.Fatalf("o defer criou um sub-agente (spawnCount %d→%d): headroom nulo não pode gerar spawn", spawnsBefore, got)
	}

	// RECUPERAÇÃO: libertado o headroom do primeiro spawn (Finish), o token-bucket volta
	// a ter capacidade e o spawn antes adiado é ADMITIDO — recuperação verificável.
	if err := coord.Finish(ctx, out1.Ticket, true); err != nil {
		t.Fatalf("Finish #1: %v", err)
	}
	out3, err := coord.RequestSpawn(ctx, spawnReq("run-rb01", "child-3", cost, slice))
	if err != nil {
		t.Fatalf("RequestSpawn #3 (pós-recuperação): %v", err)
	}
	if !out3.Admitted || out3.Ticket == nil {
		t.Fatalf("após libertar headroom o spawn devia ser admitido; got %+v", out3)
	}
	if err := coord.Finish(ctx, out3.Ticket, true); err != nil {
		t.Fatalf("Finish #3: %v", err)
	}
}
