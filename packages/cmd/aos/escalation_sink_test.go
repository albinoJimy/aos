package main

import (
	"context"
	"errors"
	"testing"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// newSinkHarness monta o adaptador de escalada sobre um Event Store real, com o gate de
// estado do run já aberto (o que o loop de serviço faz ao reclamar o run).
func newSinkHarness(t *testing.T, runID string, abrirGate bool) (*nodeEscalationSink, *runStateGates, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	gates := newRunStateGates(es, nil)
	if abrirGate {
		if err := gates.Open(context.Background(), runID, state.Uint64Token(1)); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	pend, err := integration.NewPendingApprovals(es)
	if err != nil {
		t.Fatalf("NewPendingApprovals: %v", err)
	}
	sink, err := newNodeEscalationSink(gates, pend)
	if err != nil {
		t.Fatalf("newNodeEscalationSink: %v", err)
	}
	return sink, gates, es
}

func pendingSample(runID string) agentruntime.PendingApproval {
	return agentruntime.PendingApproval{
		RunID: runID, StepID: "s1-tool-1", Turn: 1,
		ToolID: "web_post", Capability: "cap:http.post",
		ResourceType: "http", ResourceValue: "https://api.example.com/x", ResourceRegion: "eu",
		Preview: []byte{0x01, 0x02, 0x03, 0x04},
	}
}

// TestSink_SuspendeORunERegistaOPendente é o comportamento central: a escalada leva o run
// a waiting_on_human (transição DURÁVEL) e deixa o pendente visível ao operador.
func TestSink_SuspendeORunERegistaOPendente(t *testing.T) {
	const runID = "run-esc-1"
	sink, gates, es := newSinkHarness(t, runID, true)

	if err := sink.Escalate(context.Background(), pendingSample(runID)); err != nil {
		t.Fatalf("Escalate: %v", err)
	}

	// (1) o run está SUSPENSO à espera de humano.
	gate := gates.resolveGate(runID)
	if gate == nil {
		t.Fatal("gate do run devia existir")
	}
	if got := gate.m.Current(); got != state.WaitingOnHuman {
		t.Fatalf("o run devia ficar em waiting_on_human, está em %q", got)
	}

	// (2) o pendente está registado e DESCREVE o que vai executar.
	pend, err := integration.NewPendingApprovals(es)
	if err != nil {
		t.Fatalf("NewPendingApprovals: %v", err)
	}
	lista, err := pend.ListForRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	if len(lista) != 1 {
		t.Fatalf("esperava 1 pendente, veio %d", len(lista))
	}
	if lista[0].ToolID != "web_post" || lista[0].Capability != "cap:http.post" ||
		lista[0].ResourceValue != "https://api.example.com/x" {
		t.Fatalf("o pendente devia descrever a acção: %+v", lista[0])
	}
	if len(lista[0].Preview) == 0 {
		t.Fatal("o pendente tem de transportar a preview (é o que as pernas assinam)")
	}
}

// TestSink_SemGateFalhaFailClosed: sem máquina de estados aberta não há como suspender.
// Deixar o run seguir daria um agente a avançar como se nada tivesse ficado por decidir.
func TestSink_SemGateFalhaFailClosed(t *testing.T) {
	const runID = "run-sem-gate"
	sink, _, _ := newSinkHarness(t, runID, false) // gate NÃO aberto

	err := sink.Escalate(context.Background(), pendingSample(runID))
	if !errors.Is(err, ErrNoStateGateForRun) {
		t.Fatalf("sem gate a escalada devia falhar fail-closed; err=%v", err)
	}
}

// TestSink_PendenteSobreviveARestart: o registo é durável — um novo objecto sobre o MESMO
// Event Store continua a ver o que falta aprovar. Sem isto, um restart deixaria o operador
// sem saber o que decidir e o run suspenso até expirar.
func TestSink_PendenteSobreviveARestart(t *testing.T) {
	const runID = "run-esc-restart"
	sink, _, es := newSinkHarness(t, runID, true)
	if err := sink.Escalate(context.Background(), pendingSample(runID)); err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	renascido, err := integration.NewPendingApprovals(es)
	if err != nil {
		t.Fatalf("NewPendingApprovals (restart): %v", err)
	}
	lista, err := renascido.ListForRun(context.Background(), runID)
	if err != nil || len(lista) != 1 {
		t.Fatalf("o pendente devia sobreviver ao restart; n=%d err=%v", len(lista), err)
	}
}

// TestSink_RetomaDoHumanoVoltaARunning cobre a decisão do dono para o fim do TTL: o run
// volta a running (a call fica negada e o agente pode tentar outro caminho), em vez de ir
// para um estado terminal.
func TestSink_RetomaDoHumanoVoltaARunning(t *testing.T) {
	const runID = "run-esc-retoma"
	sink, gates, _ := newSinkHarness(t, runID, true)
	if err := sink.Escalate(context.Background(), pendingSample(runID)); err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	gate := gates.resolveGate(runID)
	if err := gate.ResumeFromHuman(context.Background(), "aprovacao expirada (TTL)"); err != nil {
		t.Fatalf("ResumeFromHuman: %v", err)
	}
	if got := gate.m.Current(); got != state.Running {
		t.Fatalf("depois da retoma o run devia voltar a running, está em %q", got)
	}
}
