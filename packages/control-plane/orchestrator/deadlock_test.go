package orchestrator_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/contract"
	arstate "github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// TestWaitForGraphNoCycle: um wait-for graph acíclico não reporta ciclo.
func TestWaitForGraphNoCycle(t *testing.T) {
	t.Parallel()
	w := orchestrator.NewWaitForGraph()
	// t1 espera t2 espera t3 (cadeia, sem retorno).
	w.AddWait("t1", "t2")
	w.AddWait("t2", "t3")
	if cyc := w.FindCycle(); cyc != nil {
		t.Fatalf("wait-for acíclico reportou ciclo: %v", cyc)
	}
}

// TestWaitForGraphWithCycle: uma espera circular é detectada e o conjunto de
// tarefas devolvido é o do ciclo, ordenado (determinístico).
func TestWaitForGraphWithCycle(t *testing.T) {
	t.Parallel()
	w := orchestrator.NewWaitForGraph()
	// Ciclo t1→t2→t3→t1 e um ramo extra t0→t1 fora do ciclo.
	w.AddWait("t0", "t1")
	w.AddWait("t1", "t2")
	w.AddWait("t2", "t3")
	w.AddWait("t3", "t1")
	cyc := w.FindCycle()
	want := []string{"t1", "t2", "t3"}
	if !reflect.DeepEqual(cyc, want) {
		t.Fatalf("ciclo detectado=%v, quero %v", cyc, want)
	}
}

// TestWaitForGraphTwoNodeCycle: espera mútua directa (t1↔t2).
func TestWaitForGraphTwoNodeCycle(t *testing.T) {
	t.Parallel()
	w := orchestrator.NewWaitForGraph()
	w.AddWait("t1", "t2")
	w.AddWait("t2", "t1")
	if cyc := w.FindCycle(); !reflect.DeepEqual(cyc, []string{"t1", "t2"}) {
		t.Fatalf("ciclo t1↔t2 esperado, got %v", cyc)
	}
}

// setupContention monta um DAG + ledger com uma espera circular real sobre dois
// leases: t1 detém L1 e espera L2; t2 detém L2 e espera L1. Ambos os nós estão em
// running (contenção activa sob lease). Prioridades: t1=10, t2=5 (t2 é a vítima).
func setupContention(t *testing.T, es orchestrator.EventStore, runID string) (*orchestrator.DAG, *orchestrator.ResourceLedger) {
	t.Helper()
	d := orchestrator.NewDAG(runID)
	if err := d.AddNode(orchestrator.NodeSpec{TaskID: "t1", Priority: 10}); err != nil {
		t.Fatal(err)
	}
	if err := d.AddNode(orchestrator.NodeSpec{TaskID: "t2", Priority: 5}); err != nil {
		t.Fatal(err)
	}
	// Ambos os nós entram em running (o claim ready→running da máquina de AOS-017).
	mustRun(t, d, "t1")
	mustRun(t, d, "t2")

	l := orchestrator.NewResourceLedger()
	l.Acquire("t1", "lease:L1")
	l.Acquire("t2", "lease:L2")
	l.Wait("t1", "lease:L2")
	l.Wait("t2", "lease:L1")
	return d, l
}

// mustRun transita um nó ready→running via a API pública MarkRunning (o claim da
// máquina de estados de AOS-017), pondo o nó em contenção activa sob lease.
func mustRun(t *testing.T, d *orchestrator.DAG, taskID string) {
	t.Helper()
	if err := d.MarkRunning(taskID); err != nil {
		t.Fatalf("MarkRunning %s: %v", taskID, err)
	}
}

// TestDeadlockDetectAndResolve é o teste de INTEGRAÇÃO: uma espera circular real
// dispara deadlock.detected, a política determinística escolhe a vítima de menor
// prioridade, liberta os seus recursos e transita o nó (running→failed) via a
// máquina de estados de AOS-017.
func TestDeadlockDetectAndResolve(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()
	d, l := setupContention(t, es, "run-dl")

	dd, err := orchestrator.NewDeadlockDetector(d, l, es, eventstore.Producer{NHIID: "nhi:orq"})
	if err != nil {
		t.Fatalf("NewDeadlockDetector: %v", err)
	}
	res, err := dd.DetectAndResolve(ctx)
	if err != nil {
		t.Fatalf("DetectAndResolve: %v", err)
	}
	if res == nil {
		t.Fatal("esperava deadlock, got nil")
	}
	if !res.Applied {
		t.Fatal("efeitos deviam ter sido aplicados no primeiro commit")
	}
	if res.Victim != "t2" {
		t.Fatalf("vítima=%q, quero t2 (menor prioridade)", res.Victim)
	}
	if !reflect.DeepEqual(res.Tasks, []string{"t1", "t2"}) {
		t.Fatalf("tasks do ciclo=%v, quero [t1 t2]", res.Tasks)
	}
	if res.VictimFrom != arstate.Running || res.VictimTo != arstate.Failed {
		t.Fatalf("transição da vítima=%s→%s, quero running→failed", res.VictimFrom, res.VictimTo)
	}
	if !reflect.DeepEqual(res.ReleasedResources, []string{"lease:L2"}) {
		t.Fatalf("recursos libertados=%v, quero [lease:L2]", res.ReleasedResources)
	}
	// O nó vítima transitou de facto na máquina de estados.
	if st, _ := d.State("t2"); st != arstate.Failed {
		t.Fatalf("estado de t2=%s, quero failed", st)
	}
	// Eventos deadlock.detected e deadlock.resolved persistidos.
	assertEventCount(t, es, "run-dl", contract.EventDeadlockDetected, 1)
	assertEventCount(t, es, "run-dl", contract.EventDeadlockResolved, 1)

	// Após a resolução, a espera circular quebrou-se: nova detecção não encontra
	// deadlock (o recurso da vítima foi libertado).
	res2, err := dd.DetectAndResolve(ctx)
	if err != nil {
		t.Fatalf("segunda DetectAndResolve: %v", err)
	}
	if res2 != nil {
		t.Fatalf("após resolução não devia haver deadlock, got %+v", res2)
	}
}

// TestDeadlockResolutionIdempotent prova "sem efeitos duplicados" (ADR-001): um
// SEGUNDO resolvedor que reconstrói a MESMA espera circular sobre o MESMO run vê o
// deadlock.resolved já durável (duplicado), NÃO reaplica os efeitos (não liberta o
// recurso do seu ledger) e o Event Store guarda EXACTAMENTE UM deadlock.resolved.
func TestDeadlockResolutionIdempotent(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()

	// Resolvedor A: detecta e resolve (committed → efeitos aplicados).
	dA, lA := setupContention(t, es, "run-idem")
	ddA, err := orchestrator.NewDeadlockDetector(dA, lA, es, eventstore.Producer{NHIID: "nhi:orq"})
	if err != nil {
		t.Fatalf("detector A: %v", err)
	}
	resA, err := ddA.DetectAndResolve(ctx)
	if err != nil || resA == nil || !resA.Applied {
		t.Fatalf("resolução A falhou: res=%+v err=%v", resA, err)
	}

	// Resolvedor B: estado FRESCO (mesmos nós/recursos) — simula um worker de
	// replay que reconstrói a mesma espera circular. O deadlock.resolved já existe.
	dB, lB := setupContention(t, es, "run-idem")
	ddB, err := orchestrator.NewDeadlockDetector(dB, lB, es, eventstore.Producer{NHIID: "nhi:orq"})
	if err != nil {
		t.Fatalf("detector B: %v", err)
	}
	resB, err := ddB.DetectAndResolve(ctx)
	if err != nil {
		t.Fatalf("resolução B: %v", err)
	}
	if resB == nil {
		t.Fatal("B devia reportar o deadlock reconstruído")
	}
	if resB.Applied {
		t.Fatal("B NÃO devia reaplicar efeitos (deadlock.resolved é duplicado)")
	}
	// O ledger de B NÃO foi libertado (efeito não reaplicado): t2 ainda detém L2.
	if st, _ := dB.State("t2"); st != arstate.Running {
		t.Fatalf("B não devia ter transitado t2; estado=%s, quero running", st)
	}
	// O Event Store guarda exactamente UM deadlock.resolved e UM deadlock.detected.
	assertEventCount(t, es, "run-idem", contract.EventDeadlockResolved, 1)
	assertEventCount(t, es, "run-idem", contract.EventDeadlockDetected, 1)
}

// TestNoDeadlockNoEvents: sem espera circular, o detector não emite nada.
func TestNoDeadlockNoEvents(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()
	d := orchestrator.NewDAG("run-clean")
	_ = d.AddNode(orchestrator.NodeSpec{TaskID: "t1", Priority: 1})
	_ = d.AddNode(orchestrator.NodeSpec{TaskID: "t2", Priority: 1})
	l := orchestrator.NewResourceLedger()
	l.Acquire("t1", "R1")
	l.Wait("t2", "R1") // t2 espera R1 mas t1 não espera nada: sem ciclo.

	dd, err := orchestrator.NewDeadlockDetector(d, l, es, eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewDeadlockDetector: %v", err)
	}
	res, err := dd.DetectAndResolve(ctx)
	if err != nil {
		t.Fatalf("DetectAndResolve: %v", err)
	}
	if res != nil {
		t.Fatalf("sem ciclo não devia haver resolução, got %+v", res)
	}
	if _, err := es.Read(ctx, "run-clean", 1); err == nil {
		t.Fatal("não deviam existir eventos no stream")
	}
}

// TestVictimTieBreakByRecency prova o desempate: em igual prioridade, a vítima é a
// tarefa MAIS RECENTE (maior seq de admissão).
func TestVictimTieBreakByRecency(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()
	d := orchestrator.NewDAG("run-tie")
	// t1 admitido primeiro, t2 depois; prioridade igual → vítima = t2 (mais recente).
	_ = d.AddNode(orchestrator.NodeSpec{TaskID: "t1", Priority: 7})
	_ = d.AddNode(orchestrator.NodeSpec{TaskID: "t2", Priority: 7})
	_ = d.MarkRunning("t1")
	_ = d.MarkRunning("t2")
	l := orchestrator.NewResourceLedger()
	l.Acquire("t1", "L1")
	l.Acquire("t2", "L2")
	l.Wait("t1", "L2")
	l.Wait("t2", "L1")

	dd, _ := orchestrator.NewDeadlockDetector(d, l, es, eventstore.Producer{})
	res, err := dd.DetectAndResolve(ctx)
	if err != nil {
		t.Fatalf("DetectAndResolve: %v", err)
	}
	if res.Victim != "t2" {
		t.Fatalf("desempate por recência: vítima=%q, quero t2", res.Victim)
	}
}

// assertEventCount verifica quantos eventos de um tipo existem no stream.
func assertEventCount(t *testing.T, es *eventstore.Store, runID, evType string, want int) {
	t.Helper()
	evs, err := es.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read %s: %v", runID, err)
	}
	got := 0
	for _, e := range evs {
		if e.Type == evType {
			got++
		}
	}
	if got != want {
		t.Fatalf("stream %s tem %d eventos %s, quero %d", runID, got, evType, want)
	}
}

// TestDeadlockDetectedPayloadCarriesSet prova que deadlock.detected transporta o
// conjunto de tarefas e os recursos disputados.
func TestDeadlockDetectedPayloadCarriesSet(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()
	d, l := setupContention(t, es, "run-payload")
	dd, _ := orchestrator.NewDeadlockDetector(d, l, es, eventstore.Producer{})
	if _, err := dd.DetectAndResolve(ctx); err != nil {
		t.Fatalf("DetectAndResolve: %v", err)
	}
	evs, err := es.Read(ctx, "run-payload", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var det contract.DeadlockDetectedPayload
	found := false
	for _, e := range evs {
		if e.Type == contract.EventDeadlockDetected {
			if err := json.Unmarshal(e.Payload, &det); err != nil {
				t.Fatalf("unmarshal detected: %v", err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("deadlock.detected ausente")
	}
	if !reflect.DeepEqual(det.Tasks, []string{"t1", "t2"}) {
		t.Fatalf("tasks=%v, quero [t1 t2]", det.Tasks)
	}
	if len(det.Resources) == 0 {
		t.Fatalf("recursos disputados vazios: %+v", det)
	}
}
