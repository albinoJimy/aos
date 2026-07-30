package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"

	integration "github.com/aos-ref/integration"
)

// AOS-218 — provas FALSIFICÁVEIS de que o steer está LIGADO ao loop de PRODUÇÃO
// (ACHADO-2). Exercitam o nó REAL (Bootstrap): o [control.LoopSteer] composto no
// composition root + o [runStateGates] por-run. Sem a ligação, [WithSteerSource] não teria
// chamador de produção e nem a correcção nem a pausa teriam efeito — cada asserção
// distingue o comportamento LIGADO do inerte.

// recordingSteerModel corre 2 turnos: turno 1 pede uma tool (força a fronteira de
// fim-de-turno onde o steer é consumido), turno 2 conclui. Regista os PromptView vistos —
// o do turno 2 é onde a correcção TRUSTED tem de aparecer se o steer estiver ligado.
type recordingSteerModel struct {
	views *[]agentruntime.PromptView
	turn  int
}

func (m *recordingSteerModel) Call(_ context.Context, pv agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	*m.views = append(*m.views, pv)
	m.turn++
	if m.turn == 1 {
		return agentruntime.ModelResponse{
			Text:      "turno 1",
			ToolCalls: []agentruntime.ToolInvocation{{ToolID: "probe", Capability: "cap:probe", Input: []byte("x")}},
		}, nil
	}
	return agentruntime.ModelResponse{Text: "fim", Final: true}, nil
}

// steerNode compõe um nó com um operador ed25519 registado e o modelo dado. Devolve o nó,
// a privada do operador e o seu id.
func steerNode(t *testing.T, views *[]agentruntime.PromptView) (*Node, ed25519.PrivateKey, string) {
	t.Helper()
	const operatorID = "human:operator-steer"
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cfg := tnBaseConfig()
	cfg.Operators = map[string]ed25519.PublicKey{operatorID: pub}
	cfg.SteerClock = tnClock()
	cfg.Model = &recordingSteerModel{views: views}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node, priv, operatorID
}

func steerGoal(runID string) agentruntime.Goal {
	return agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: "nhi:agent-steer", AgentID: "agt", AgentClass: tnClass},
	}
}

func signedSignal(t *testing.T, priv ed25519.PrivateKey, id, runID string, kind control.SignalKind, payload []byte) control.Emitter {
	t.Helper()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand nonce: %v", err)
	}
	return integration.SignSignal(priv, id, runID, kind, payload, nonce, tnClock()())
}

// TestNodeSteerCorrectionReachesLoop (CA2) — um steer assinado PENDENTE quando o run
// corre faz o loop de PRODUÇÃO injectar a correcção TRUSTED no prompt do turno seguinte.
// Dois sentidos: SEM steer, o mesmo prompt NÃO a contém (o loop não muda).
func TestNodeSteerCorrectionReachesLoop(t *testing.T) {
	ctx := context.Background()
	correction := []byte("prioriza a superficie desktop")

	// (a) COM steer: correcção submetida antes de o run correr ⇒ pendente na fronteira do
	// turno 1 ⇒ injectada no prompt do turno 2.
	var views []agentruntime.PromptView
	node, priv, opID := steerNode(t, &views)
	const runID = "run-steer-applied"
	emit := signedSignal(t, priv, opID, runID, control.SignalSteer, correction)
	if err := node.Steer.Steer(ctx, runID, correction, emit); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if _, _, err := node.Runtime.Run(ctx, steerGoal(runID), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(views) < 2 {
		t.Fatalf("esperava >= 2 turnos, tive %d (a fronteira de fim-de-turno não foi cruzada)", len(views))
	}
	if !strings.Contains(string(views[1].Materialized), "correction=prioriza a superficie desktop") {
		t.Fatalf("correcção NÃO chegou ao prompt do turno 2 — steer não está ligado ao loop de produção.\nprompt: %s", views[1].Materialized)
	}
	if !strings.Contains(string(views[1].Materialized), "taint="+agentruntime.TaintTrusted) {
		t.Fatal("correcção no prompt não marcada taint=trusted (dado de controlo, não untrusted)")
	}

	// (b) SEM steer: o MESMO prompt do turno 2 NÃO contém a correcção — o loop é inalterado.
	var views2 []agentruntime.PromptView
	node2, _, _ := steerNode(t, &views2)
	if _, _, err := node2.Runtime.Run(ctx, steerGoal("run-no-steer-applied"), nil); err != nil {
		t.Fatalf("Run (sem steer): %v", err)
	}
	if len(views2) < 2 {
		t.Fatalf("esperava >= 2 turnos no controlo, tive %d", len(views2))
	}
	if strings.Contains(string(views2[1].Materialized), "correction=") {
		t.Fatal("prompt sem steer NÃO devia conter uma correcção — comportamento não é aditivo")
	}
}

// TestNodeSteerPauseIsDurable (CA2) — um pause assinado PENDENTE faz o loop de PRODUÇÃO
// PARAR graciosamente (Result.Paused) e materializar a transição DURÁVEL running→paused
// via a máquina de estados de AOS-017 (a FONTE do StateGate por-run). Verifica-se relendo
// o estado de uma máquina fresca sobre o mesmo Event Store (durabilidade, não projecção
// volátil). Sem pause, o run termina e a máquina NUNCA sai de ready.
func TestNodeSteerPauseIsDurable(t *testing.T) {
	ctx := context.Background()

	// (a) COM pause pendente + gate aberto (como o loop de serviço faz por-run).
	var views []agentruntime.PromptView
	node, priv, opID := steerNode(t, &views)
	const runID = "run-steer-paused"
	if err := node.stateGates.Open(ctx, runID, durable.FencingToken(1)); err != nil {
		t.Fatalf("stateGates.Open: %v", err)
	}
	emit := signedSignal(t, priv, opID, runID, control.SignalPause, nil)
	if err := node.Steer.Pause(ctx, runID, emit); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	res, _, err := node.Runtime.Run(ctx, steerGoal(runID), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Paused {
		t.Fatal("run com pause pendente devia parar graciosamente (Result.Paused)")
	}
	if res.Terminated {
		t.Fatal("run pausado não devia estar Terminated")
	}
	// DURABILIDADE: uma máquina fresca reconstruída do log tem de estar em PAUSED.
	assertMachineState(t, node.EventStore, runID, state.Paused)

	// (b) SEM pause: termina e a máquina fresca fica em READY (nunca reclamada/pausada).
	var views2 []agentruntime.PromptView
	node2, _, _ := steerNode(t, &views2)
	const runID2 = "run-no-pause"
	if err := node2.stateGates.Open(ctx, runID2, durable.FencingToken(1)); err != nil {
		t.Fatalf("stateGates.Open: %v", err)
	}
	res2, _, err := node2.Runtime.Run(ctx, steerGoal(runID2), nil)
	if err != nil {
		t.Fatalf("Run (sem pause): %v", err)
	}
	if res2.Paused {
		t.Fatal("run sem pause NÃO devia ficar Paused")
	}
	if !res2.Terminated {
		t.Fatal("run sem pause devia terminar normalmente")
	}
	assertMachineState(t, node2.EventStore, runID2, state.Ready)
}

// assertMachineState relê o estado durável do run de uma máquina FRESCA (prova de que a
// transição é durável, não uma projecção in-memory).
func assertMachineState(t *testing.T, store state.EventStore, runID string, want state.State) {
	t.Helper()
	m, err := state.NewMachine(store, runID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	got, err := m.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if got != want {
		t.Fatalf("estado durável do run %q = %q, quero %q", runID, got, want)
	}
}
