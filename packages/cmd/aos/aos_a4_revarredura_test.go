package main

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// Achado A4 da auditoria de 2026-08-17. A varredura de crash-resume corria SÓ NO ARRANQUE. Num nó
// ÚNICO isso deixa um buraco observado em produção: mata-se o nó a meio de um run e, ao arrancar,
// a varredura ENCONTRA o órfão e SALTA-O — o lease da encarnação ANTERIOR DO MESMO PROCESSO ainda
// não expirou (TTL de 2 min) e ela recusa-se, correctamente, a roubar partição. Depois disso nada
// re-varre: o run fica em `running` para sempre, com trabalho real feito e durável, e a API
// responde `404` até alguém reiniciar OUTRA VEZ.
//
// A correcção NÃO reclama leases mais cedo — isso produziria dupla execução se o processo antigo
// ainda estivesse vivo. Limita-se a VOLTAR A TENTAR depois de o lease expirar.

// TestA4_ReVarreduraRetomaOrfaoSemSegundoArranque é a prova comportamental: o órfão é retomado
// pelo VARREDOR PERIÓDICO, sem ninguém chamar a varredura de arranque uma segunda vez.
func TestA4_ReVarreduraRetomaOrfaoSemSegundoArranque(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	ctx := context.Background()

	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	vault := audit.NewInMemoryKeyVault(nil)
	approvers := crashResumeApprovers(t)
	shared := func(cfg *Config) {
		cfg.EventStore = store
		cfg.DSARVault = vault
		cfg.DurableExecution = true
		cfg.Approvers = approvers
	}

	const runID = "run-a4-orfao"
	var counter int64

	// ---- incarnação 1: aplica o turno 1 e "crasha" deixando `running` sem desfecho ----
	node1, cred1 := obsPermitNodeWith(t, "", &crashResumeModel{finalFrom: 99}, shared)
	if err := node1.Runtime.Register("counter", func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt64(&counter, 1)
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	goal := agentruntime.Goal{
		RunID:      runID,
		Principal:  referencemonitor.Principal{NHIID: durAgent},
		Credential: cred1,
		Model:      agentruntime.ModelConfig{ModelID: "model:a4"},
		Objective:  "re-varredura de orfaos",
		MaxTurns:   4,
	}
	prod := goal
	prod.MaxTurns = 1
	if _, _, rerr := node1.Runtime.Run(ctx, prod, nil); rerr != nil && !errors.Is(rerr, agentruntime.ErrMaxTurnsExceeded) {
		t.Fatalf("turno 1: %v", rerr)
	}
	m, err := state.NewMachine(store, runID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := node1.ResumeRecords.Put(ctx, resumeRecordFromGoal(goal)); err != nil {
		t.Fatalf("registo de retoma: %v", err)
	}
	_ = node1.Close()

	// ---- incarnação 2: arranca e NÃO chama a varredura de arranque ----
	node2, _ := obsPermitNodeWith(t, "", &crashResumeModel{finalFrom: 2}, shared)
	t.Cleanup(func() { _ = node2.Close() })
	if err := node2.Runtime.Register("counter", func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt64(&counter, 1)
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	svc2, err := NewNodeService(node2, WithDeadlineSweepInterval(0), WithServiceLog(io.Discard))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	sweepCtx, stopSweep := context.WithCancel(ctx)
	t.Cleanup(func() {
		stopSweep()
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc2.Shutdown(sc)
	})

	// SÓ o varredor periódico. Se ele não funcionasse, o run ficaria em `running` para sempre —
	// que é exactamente o defeito observado em produção.
	svc2.StartOrphanSweeper(sweepCtx, 50*time.Millisecond)

	// Sonda-se em vez de esperar de uma vez: [NodeService.Wait] devolve (_, false) enquanto o run
	// NÃO estiver registado nesta réplica, e o ponto do teste é precisamente que quem o regista é
	// a re-varredura — não a varredura de arranque, que aqui nunca é chamada.
	deadline := time.Now().Add(20 * time.Second)
	var retomado bool
	for time.Now().Before(deadline) {
		wc, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, ok, werr := svc2.Wait(wc, runID)
		cancel()
		if werr != nil {
			t.Fatalf("Wait: %v", werr)
		}
		if ok {
			retomado = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !retomado {
		t.Fatal("o orfao TEM de ser retomado pela RE-VARREDURA, sem segundo arranque — ficou em `running` para sempre, que e o defeito observado em producao")
	}

	// E a retoma continua a não re-executar efeitos já aplicados: o dedup do step-ledger mantém-se.
	if got := atomic.LoadInt64(&counter); got != 1 {
		t.Fatalf("a re-varredura NAO pode re-executar o efeito ja aplicado; counter=%d (esperado 1)", got)
	}
}

// TestA4_IntervaloDesligadoNaoArrancaVarredor: o opt-out é explícito e não arranca goroutine.
func TestA4_IntervaloDesligadoNaoArrancaVarredor(t *testing.T) {
	// logw nil ⇒ [NodeService.log] e no-op: um servico vazio basta, e prova que o caminho
	// desligado nao toca em substrato nenhum.
	svc := &NodeService{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.StartOrphanSweeper(ctx, 0)  // desligado
	svc.StartOrphanSweeper(ctx, -1) // negativo é igualmente desligado
}

// TestA4_ParseIntervalo_FailClosed: uma duração ilegível ABORTA em vez de cair no default em
// silêncio — quem a escreveu julgaria tê-la afinado, e o sintoma só apareceria num crash.
func TestA4_ParseIntervalo_FailClosed(t *testing.T) {
	t.Run("vazia da o default", func(t *testing.T) {
		t.Setenv("AOS_CRASH_RESUME_INTERVAL", "")
		d, err := parseOrphanSweepInterval()
		if err != nil || d != DefaultOrphanSweepInterval {
			t.Fatalf("vazia devia dar o default (%s), veio %s err=%v", DefaultOrphanSweepInterval, d, err)
		}
	})
	t.Run("valida", func(t *testing.T) {
		t.Setenv("AOS_CRASH_RESUME_INTERVAL", "30s")
		d, err := parseOrphanSweepInterval()
		if err != nil || d != 30*time.Second {
			t.Fatalf("30s: veio %s err=%v", d, err)
		}
	})
	t.Run("zero desliga", func(t *testing.T) {
		t.Setenv("AOS_CRASH_RESUME_INTERVAL", "0")
		d, err := parseOrphanSweepInterval()
		if err != nil || d != 0 {
			t.Fatalf("0 tem de ser aceite e desligar, veio %s err=%v", d, err)
		}
	})
	for _, mau := range []string{"nao-e-duracao", "2", "-5s"} {
		t.Run("recusa "+mau, func(t *testing.T) {
			t.Setenv("AOS_CRASH_RESUME_INTERVAL", mau)
			if _, err := parseOrphanSweepInterval(); !errors.Is(err, ErrBadOrphanSweepInterval) {
				t.Fatalf("%q devia ser recusado com ErrBadOrphanSweepInterval, veio %v", mau, err)
			}
		})
	}
}
