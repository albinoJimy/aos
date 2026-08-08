package main

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/worker"
	"github.com/aos-ref/substrate/eventstore"
)

// newSuspensaoDuravelHarness monta um run REALMENTE suspenso (a transição durável passa
// pelo mesmo runGate que o sink de escalada usa) e devolve um serviço com os baldes VAZIOS
// — a fotografia exacta de um nó que acabou de reiniciar.
func newSuspensaoDuravelHarness(t *testing.T, runID string, comPendente bool) *NodeService {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	gates := newRunStateGates(es, nil)
	ctx := context.Background()
	if err := gates.Open(ctx, runID, durable.FencingToken(1)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := gates.resolveGate(runID).EscalateToHuman(ctx, "teste"); err != nil {
		t.Fatalf("EscalateToHuman: %v", err)
	}
	gates.Close(runID) // o processo "morreu": o gate em memória desaparece

	pend, err := integration.NewPendingApprovals(es)
	if err != nil {
		t.Fatalf("NewPendingApprovals: %v", err)
	}
	if comPendente {
		if err := pend.Put(ctx, integration.PendingRecord{
			RunID: runID, StepID: "step-000004-tool-1", Turn: 4,
			ToolID: "doc_read", Capability: "cap:fs.read", Preview: []byte{0x01},
		}); err != nil {
			t.Fatalf("Put pendente: %v", err)
		}
	}
	leases, err := durable.NewLeaseManager(es, time.Minute)
	if err != nil {
		t.Fatalf("NewLeaseManager: %v", err)
	}
	assigner, err := worker.NewAssigner(leases)
	if err != nil {
		t.Fatalf("NewAssigner: %v", err)
	}
	// Registo de retoma REAL (vazio): o run está suspenso no log mas nunca chegou a
	// persistir o Goal — a retoma tem de o dizer com um erro seu, não com "não suspenso".
	resumes, err := integration.NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	return &NodeService{
		assigner:  assigner,
		leases:    leases,
		node:      &Node{stateGates: gates, PendingApprovals: pend, ResumeRecords: resumes},
		runs:      make(map[string]*runState),
		completed: make(map[string]*runState),
		suspended: make(map[string]*runState), // VAZIO — é o ponto do teste
		logw:      io.Discard,
	}
}

// TestSuspensaoDuravel_SobreviveAoRestart é a propriedade que faltava: o registo de
// retoma, o pendente e o grant são todos duráveis, mas o balde de suspensos vivia só em
// memória. Um restart tornava irretomável um run perfeitamente recuperável — o operador
// aprovava e nada consumia a aprovação.
func TestSuspensaoDuravel_SobreviveAoRestart(t *testing.T) {
	svc := newSuspensaoDuravelHarness(t, "run-suspenso", true)

	oc, susp := svc.Suspended(context.Background(), "run-suspenso")
	if !susp {
		t.Fatal("um run cujo log diz waiting_on_human tem de constar como suspenso apos um restart")
	}
	if !oc.Result.Escalated {
		t.Fatal("o desfecho parcial tem de dizer que houve escalada")
	}
	// O turno vem do pendente durável: reportar 0 seria uma segunda mentira operacional.
	if oc.Result.Turns != 4 {
		t.Fatalf("o turno em que o run parou vem do pendente duravel; obtive %d", oc.Result.Turns)
	}
}

// TestSuspensaoDuravel_RecusaResubmissaoAposRestart: sem isto, re-submeter o mesmo RunID
// depois de um restart RECOMEÇARIA do zero um run que está à espera de um humano —
// perdendo a trajectória e deixando o pendente e o grant órfãos.
func TestSuspensaoDuravel_RecusaResubmissaoAposRestart(t *testing.T) {
	svc := newSuspensaoDuravelHarness(t, "run-suspenso", false)
	err := svc.Submit(context.Background(), agentruntime.Goal{RunID: "run-suspenso"})
	if !errors.Is(err, ErrRunSuspended) {
		t.Fatalf("devia ser ErrRunSuspended (e dizer COMO retomar); veio: %v", err)
	}
	// A reserva tem de ter sido desfeita — senão o RunID ficava preso para sempre.
	svc.mu.Lock()
	_, reservado := svc.runs["run-suspenso"]
	svc.mu.Unlock()
	if reservado {
		t.Fatal("a recusa por suspensao tem de desfazer a reserva do RunID")
	}
}

// TestSuspensaoDuravel_RetomaEncontraORunAposRestart prova que a retoma já não devolve
// ErrRunNotSuspended por o balde estar vazio. Falha depois, por falta de registo de
// retoma — o que é a verdade neste harness e um erro DIFERENTE e informativo.
func TestSuspensaoDuravel_RetomaEncontraORunAposRestart(t *testing.T) {
	svc := newSuspensaoDuravelHarness(t, "run-suspenso", false)
	err := svc.Resume(context.Background(), "run-suspenso", "credencial-fresca")
	if errors.Is(err, ErrRunNotSuspended) {
		t.Fatal("a retoma tem de ENCONTRAR o run pelo log; ErrRunNotSuspended era o bug")
	}
	if !errors.Is(err, ErrNoResumeRecord) {
		t.Fatalf("sem registo de retoma o erro devia ser ErrNoResumeRecord; veio: %v", err)
	}
}

// TestSuspensaoDuravel_NaoSobrepoeAContabilidadeLocal: um run que ESTA réplica arquivou
// como terminado não pode voltar a parecer suspenso por o log ter uma transição antiga —
// é o caso do fail-closed de hostRun, que marca FALHADO um run cuja transição já ocorreu.
func TestSuspensaoDuravel_NaoSobrepoeAContabilidadeLocal(t *testing.T) {
	svc := newSuspensaoDuravelHarness(t, "run-suspenso", true)
	svc.completed["run-suspenso"] = &runState{runID: "run-suspenso", err: errors.New("falhou")}

	if _, susp := svc.Suspended(context.Background(), "run-suspenso"); susp {
		t.Fatal("um run com desfecho retido nesta replica NAO pode ser reportado como suspenso")
	}
	if err := svc.Resume(context.Background(), "run-suspenso", "x"); !errors.Is(err, ErrRunNotSuspended) {
		t.Fatalf("retomar um run terminado nesta replica devia ser ErrRunNotSuspended; veio: %v", err)
	}
}

// TestSuspensaoDuravel_RunNormalNaoEhAfectado: um run que nunca escalou não passa a
// suspenso, e um run sem transições (stream inexistente) também não. Sela que a consulta
// nova não muda o caminho comum.
func TestSuspensaoDuravel_RunNormalNaoEhAfectado(t *testing.T) {
	svc := newSuspensaoDuravelHarness(t, "run-suspenso", false)
	ctx := context.Background()

	if _, susp := svc.Suspended(ctx, "run-que-nunca-existiu"); susp {
		t.Fatal("um run sem stream reconstroi para ready, nao para suspenso")
	}
	// Um run a correr (ready→running) não é suspenso.
	if err := svc.node.stateGates.Open(ctx, "run-a-correr", durable.FencingToken(1)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g := svc.node.stateGates.resolveGate("run-a-correr")
	if err := g.Pause(ctx, "teste"); err != nil { // ready→running→paused (lazy-claim)
		t.Fatalf("Pause: %v", err)
	}
	if err := g.Resume(ctx, "teste"); err != nil { // paused→running
		t.Fatalf("Resume: %v", err)
	}
	if _, susp := svc.Suspended(ctx, "run-a-correr"); susp {
		t.Fatal("um run em running nao pode ser reportado como suspenso")
	}
}
