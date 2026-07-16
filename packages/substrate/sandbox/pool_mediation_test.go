package sandbox

import (
	"context"
	"testing"
	"time"
)

// TestPool_ExecutionStaysMediated prova que o pool COMPÕE (não substitui) a mediação
// do Reference Monitor de AOS-064: o pool disponibiliza a sandbox pronta (reserva
// rápida de uma VM limpa), mas o EFEITO só corre pelo caminho mediado
// (MediatedLauncher.Execute → rm.Mediate). O pool não expõe nenhum caminho de Exec.
func TestPool_ExecutionStaysMediated(t *testing.T) {
	store := newStore(t)
	rm := newPermitMonitor(store)

	launcher, err := NewLauncher(NewFakeDriver(), WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := NewMediatedLauncher(rm, launcher, "tool.pooled")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	rec := NewColdStartRecorder()
	pool := newPool(t, 2, WithPolicy(PolicyExpand), WithMaxSize(8), WithColdStartRecorder(rec), WithSynchronousReplenish())
	ctx := context.Background()

	// (1) Aprovisionamento: reserva uma sandbox pronta pelo pool (cold-start medido).
	lease, err := pool.Reserve(ctx)
	if err != nil {
		t.Fatalf("pool.Reserve: %v", err)
	}
	if lease.ColdStart() >= DefaultColdStartTarget {
		t.Fatalf("cold-start %v nao cumpre o alvo", lease.ColdStart())
	}

	// (2) Execução: SÓ pelo caminho mediado. O resultado é untrusted por construção.
	res, err := ml.Execute(ctx, defaultAuthz(), ExecRequest{
		RunID:  "run-pool-1",
		StepID: "step-1",
		Call:   ToolCall{ToolID: "echo", Command: "hello"},
	})
	if err != nil {
		t.Fatalf("Execute mediado: %v", err)
	}
	if !res.IsUntrusted() {
		t.Fatal("resultado deveria ser untrusted")
	}
	if string(res.Stdout) != "hello" {
		t.Fatalf("stdout inesperado: %q", res.Stdout)
	}

	// (3) Libertação: descarta o overlay sujo e repõe warm.
	lease.Release()
	pool.waitReplenish()
	if st := pool.Stats(); st.InUse != 0 {
		t.Fatalf("esperava 0 em uso apos release, obtive %d", st.InUse)
	}

	// A mediação selou o ciclo de vida no Event Store (audit-before-effect de AOS-064
	// intacto): created + exec + destroyed presentes.
	evs := readEvents(t, store, "run-pool-1")
	if len(eventsOfType(evs, EventInstanceCreated)) == 0 {
		t.Fatal("faltou o evento created (mediacao/audit de AOS-064 partida)")
	}
	if len(eventsOfType(evs, EventExecCompleted)) == 0 {
		t.Fatal("faltou o evento exec")
	}
	if len(eventsOfType(evs, EventInstanceDestroyed)) == 0 {
		t.Fatal("faltou o evento destroyed")
	}
}

// TestPool_DeniedByRMProducesNoEffect prova que, mesmo com o pool a disponibilizar
// VMs, uma negação do Reference Monitor impede qualquer efeito — o pool não abre um
// atalho ao RM.
func TestPool_DeniedByRMProducesNoEffect(t *testing.T) {
	store := newStore(t)
	rm := newDenyMonitor(store)
	launcher, err := NewLauncher(NewFakeDriver(), WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := NewMediatedLauncher(rm, launcher, "tool.pooled.deny")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}
	pool := newPool(t, 1, WithSynchronousReplenish())
	lease, err := pool.Reserve(context.Background())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	defer lease.Release()

	_, err = ml.Execute(context.Background(), defaultAuthz(), ExecRequest{RunID: "run-deny", StepID: "s1", Call: ToolCall{ToolID: "x", Command: "y"}})
	var denied *DeniedError
	if err == nil {
		t.Fatal("esperava negacao do RM")
	}
	if de, ok := err.(*DeniedError); ok {
		denied = de
	}
	if denied == nil {
		t.Fatalf("esperava *DeniedError, obtive %T: %v", err, err)
	}
}

// TestPool_RealGoOverheadIsNegligible mede o OVERHEAD Go REAL das máquinas de
// pool/restore (reserva + reposição), separado do timing MODELADO de microVM. Prova
// que a lógica de pool em si não introduz latência material (bem abaixo do orçamento
// de 125 ms), validando que o cold-start é dominado pelo restore modelado.
func TestPool_RealGoOverheadIsNegligible(t *testing.T) {
	p := newPool(t, 16, WithPolicy(PolicyExpand), WithMaxSize(64), WithSynchronousReplenish())
	ctx := context.Background()

	const iters = 2000
	start := time.Now()
	for i := 0; i < iters; i++ {
		l, err := p.Reserve(ctx)
		if err != nil {
			t.Fatalf("Reserve #%d: %v", i, err)
		}
		l.Release()
	}
	perOp := time.Since(start) / iters
	// O overhead Go real por operação reserva+release+reposição deve ser uma fracção
	// diminuta do orçamento de cold-start (folga generosa para CI lento).
	if perOp > 5*time.Millisecond {
		t.Fatalf("overhead Go por reserva %v demasiado alto (esperado << 125 ms)", perOp)
	}
	t.Logf("overhead Go real por reserva+release = %v (%d iteracoes)", perOp, iters)
}
