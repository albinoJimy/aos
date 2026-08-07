package integration

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// AOS-021 §7 item 4 — TESTE DE COMPOSIÇÃO do ciclo completo
// ---------------------------------------------------------------------------
//
// Cada peça do bridge está testada isoladamente. Este teste prova que ELAS ENCAIXAM:
//
//	turno 1: doc_read EXECUTA · web_post ESCALA (nenhum efeito) → run suspende
//	         → cerimónia four-eyes REAL → grant durável
//	         → RETOMA: doc_read NÃO re-executa (step-ledger) · web_post executa 1x
//
// A propriedade que mais importa é a NEGATIVA: o efeito já aplicado do turno 1 não pode
// correr segunda vez na retoma. É o risco que o desenho identificou (R1) e a razão de a
// execução durável ser exigida em produção.

// riskEscalateHook modela o RiskGate: uma capability de risco exige aval humano, SALVO se
// a call já trouxer prova de aprovação VERIFICADA (colocada pelo [ApprovalGate] a montante).
type riskEscalateHook struct{ capability string }

func (riskEscalateHook) Name() string { return "risk" }

func (h riskEscalateHook) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	if call.Capability != h.capability {
		return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
	}
	if call.HumanApproval() != nil {
		// Um humano com autoridade assumiu ESTA acção — segue para os gates a jusante.
		return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
	}
	return referencemonitor.HookResult{Decision: referencemonitor.HookEscalate, Reason: "accao de risco exige aval humano"}, nil
}

// cycleSink capta a escalada (o que o nó faria: registar o pendente e suspender).
type cycleSink struct {
	pending []agentruntime.PendingApproval
}

func (s *cycleSink) Escalate(_ context.Context, p agentruntime.PendingApproval) error {
	s.pending = append(s.pending, p)
	return nil
}

// brokerEvidence adapta o broker à porta de evidência do loop.
type brokerEvidence struct{ b *ApprovalBroker }

func (e brokerEvidence) EvidenceFor(ctx context.Context, runID string, preview []byte) []byte {
	return e.b.EvidenceFor(ctx, runID, preview)
}

// TestApprovalCycle_RetomaNaoRepeteEfeitos é o teste de composição do §7 item 4.
func TestApprovalCycle_RetomaNaoRepeteEfeitos(t *testing.T) {
	ctx := context.Background()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer es.Close()

	// --- Reference Monitor: ApprovalGate ANTES do gate de risco (a prova tem de estar
	// colocada quando o risco decide) ---
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")
	broker, err := NewApprovalBroker(gate, NewMemApprovalStore())
	if err != nil {
		t.Fatalf("NewApprovalBroker: %v", err)
	}
	rm := referencemonitor.New(
		referencemonitor.WithHooks(
			referencemonitor.NewApprovalGate(broker),
			riskEscalateHook{capability: "cap:http.post"},
		),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)

	docReads, webPosts := 0, 0
	if err := rm.Register("doc_read", func(context.Context, []byte) ([]byte, error) {
		docReads++
		return []byte("conteudo"), nil
	}); err != nil {
		t.Fatalf("Register doc_read: %v", err)
	}
	if err := rm.Register("web_post", func(context.Context, []byte) ([]byte, error) {
		webPosts++
		return []byte("publicado"), nil
	}); err != nil {
		t.Fatalf("Register web_post: %v", err)
	}

	// --- EXECUÇÃO DURÁVEL: é o step-ledger que impede a dupla execução na retoma ---
	ledger, err := durable.NewStepLedger(es)
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	actDisp, err := activity.NewDispatcher(rm, ledger)
	if err != nil {
		t.Fatalf("activity.NewDispatcher: %v", err)
	}
	durDisp, err := NewDurableDispatcher(actDisp)
	if err != nil {
		t.Fatalf("NewDurableDispatcher: %v", err)
	}

	// --- Modelo DETERMINISTA: o turno 1 emite as DUAS tool calls, sempre iguais. É o que
	// a reprodução da captura garante na retoma real (testada em cmd/aos). ---
	modelo := func() agentruntime.ModelClient {
		return agentruntime.ModelClientFunc(func(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
			if view.Turn == 1 {
				return agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{
					{ToolID: "doc_read", Capability: "cap:fs.read", Input: []byte(`{"doc_id":"notes"}`)},
					{ToolID: "web_post", Capability: "cap:http.post", Input: []byte(`{"body":"x"}`)},
				}}, nil
			}
			return agentruntime.ModelResponse{Text: "fim", Final: true}, nil
		})
	}

	goal := agentruntime.Goal{
		RunID:     "run-ciclo",
		Principal: referencemonitor.Principal{NHIID: "agt-1", AgentClass: "agent-worker"},
		System:    "sistema",
		Objective: "ler e publicar",
	}
	recorder := agentruntime.NewTurnRecorder(es)

	// ===================== 1.ª PASSAGEM: escala =====================
	sink := &cycleSink{}
	rt1 := agentruntime.New(modelo(), rm, recorder,
		agentruntime.WithActivityDispatcher(durDisp),
		agentruntime.WithEscalationSink(sink),
	)
	res1, err := rt1.Run(ctx, goal)
	if err != nil {
		t.Fatalf("1.ª passagem: %v", err)
	}
	if !res1.Escalated {
		t.Fatalf("o run devia SUSPENDER na web_post; res=%+v", res1)
	}
	if docReads != 1 {
		t.Fatalf("doc_read devia ter executado 1x no turno 1; execs=%d", docReads)
	}
	if webPosts != 0 {
		t.Fatalf("web_post ESCALADA não pode produzir efeito; execs=%d", webPosts)
	}
	if len(sink.pending) != 1 {
		t.Fatalf("devia haver 1 pendente registado, veio %d", len(sink.pending))
	}
	preview := sink.pending[0].Preview

	// ===================== APROVAÇÃO: cerimónia four-eyes REAL =====================
	req := FourEyesRequest{
		RequestID:           "req-ciclo",
		Preview:             preview,
		RiskClass:           risk.ClassDanger,
		DualControlRequired: true,
	}
	legA := SignFourEyesLeg(privA, req, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	legB := SignFourEyesLeg(privB, req, "human:bob", "sess-B", "cred-B", challenge32(t), nil)
	if _, err := broker.Approve(ctx, "req-ciclo", req, legA, legB); err != nil {
		t.Fatalf("cerimónia four-eyes: %v", err)
	}

	// ===================== 2.ª PASSAGEM: RETOMA =====================
	// O ledger é reconstruído do log (o que o nó faz em RebuildLedger antes de retomar):
	// é isto que faz o efeito já aplicado do turno 1 ser reconhecido e NÃO repetido.
	if err := ledger.Rebuild(ctx, goal.RunID); err != nil {
		t.Fatalf("Rebuild do ledger: %v", err)
	}
	rt2 := agentruntime.New(modelo(), rm, recorder,
		agentruntime.WithActivityDispatcher(durDisp),
		agentruntime.WithEscalationSink(sink),
		agentruntime.WithApprovalEvidence(brokerEvidence{b: broker}),
	)
	res2, err := rt2.Run(ctx, goal)
	if err != nil {
		t.Fatalf("retoma: %v", err)
	}

	// --- AS ASSERÇÕES QUE IMPORTAM ---
	if res2.Escalated {
		t.Fatalf("com aprovação a retoma NÃO devia voltar a escalar; res=%+v", res2)
	}
	if docReads != 1 {
		t.Fatalf("PROPRIEDADE CENTRAL: o efeito já aplicado do turno 1 NÃO pode correr segunda vez na retoma; doc_read execs=%d (esperado 1)", docReads)
	}
	if webPosts != 1 {
		t.Fatalf("a acção aprovada devia executar EXACTAMENTE 1x; web_post execs=%d", webPosts)
	}
	if !res2.Terminated {
		t.Fatalf("depois de executar, o run devia prosseguir e terminar; res=%+v", res2)
	}

	t.Logf("\n"+
		"  1.ª PASSAGEM : doc_read EXECUTOU (%d) · web_post ESCALOU sem efeito (%d)\n"+
		"  APROVAÇÃO    : cerimónia four-eyes REAL (dual-control, 2 pernas distintas)\n"+
		"  RETOMA       : doc_read NÃO repetiu (ledger already-applied) · web_post executou 1x\n"+
		"  TOTAIS       : doc_read=%d  web_post=%d  (run terminado=%t)",
		1, 0, docReads, webPosts, res2.Terminated)
}
