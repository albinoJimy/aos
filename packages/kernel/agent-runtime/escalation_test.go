package agentruntime

import (
	"context"
	"errors"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// escalatingHook devolve SEMPRE escalate para a capability dada (o veredicto do RiskGate
// para uma acção gray/danger: requer gate humano, nenhum efeito ocorre).
type escalatingHook struct{ capability string }

func (escalatingHook) Name() string { return "risk" }

func (h escalatingHook) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	if call.Capability == h.capability {
		return referencemonitor.HookResult{Decision: referencemonitor.HookEscalate, Reason: "requer aval humano"}, nil
	}
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// spySink grava os pendentes recebidos e pode falhar a suspensão.
type spySink struct {
	pending []PendingApproval
	err     error
}

func (s *spySink) Escalate(_ context.Context, p PendingApproval) error {
	if s.err != nil {
		return s.err
	}
	s.pending = append(s.pending, p)
	return nil
}

// countingCheckpointer conta os checkpoints que CONFIRMAM uma activity (o cursor
// intra-iteração de AOS-015: fase "dispatched" com ConfirmedStepID preenchido).
type countingCheckpointer struct{ activities int }

func (c *countingCheckpointer) Checkpoint(_ context.Context, cp Checkpoint) error {
	if cp.Phase == PhaseDispatched && cp.ConfirmedStepID != "" {
		c.activities++
	}
	return nil
}

// newEscalationRuntime monta um RT cujo RM escala a capability privilegiada.
func newEscalationRuntime(t *testing.T, sink EscalationSink, extra ...Option) (*Runtime, *harness) {
	t.Helper()
	h := newHarness(t, nil)
	store := h.store
	rm := referencemonitor.New(
		referencemonitor.WithHooks(escalatingHook{capability: "cap:http.post"}),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)),
	)
	if err := rm.Register("web_post", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	opts := append([]Option{WithEscalationSink(sink)}, extra...)
	return New(escalatingModel(), rm, h.recorder, opts...), h
}

// escalatingModel pede uma tool privilegiada no 1.º turno e concluiria no 2.º.
func escalatingModel() ModelClient {
	turn := 0
	return ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		turn++
		if turn == 1 {
			return ModelResponse{ToolCalls: []ToolInvocation{{
				ToolID: "web_post", Capability: "cap:http.post",
				ResourceType: "http", ResourceValue: "https://api.example.com/x", ResourceRegion: "eu",
				Input: []byte(`{"body":"x"}`),
			}}}, nil
		}
		return ModelResponse{Text: "fim", Final: true}, nil
	})
}

// TestEscalation_ParaORunERegistaOPendente é o comportamento novo: um veredicto
// `escalate` suspende o run à espera de humano, em vez de o deixar seguir como se a call
// tivesse sido simplesmente negada.
func TestEscalation_ParaORunERegistaOPendente(t *testing.T) {
	sink := &spySink{}
	rt, _ := newEscalationRuntime(t, sink)
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("uma escalada não é erro do loop (é um desfecho): %v", err)
	}
	if !res.Escalated {
		t.Fatalf("o run devia ter parado à espera de humano; res=%+v", res)
	}
	if res.Terminated || res.Turns != 1 {
		t.Fatalf("devia parar no turno 1, sem terminar; turns=%d terminated=%t", res.Turns, res.Terminated)
	}
	if len(sink.pending) != 1 {
		t.Fatalf("devia registar exactamente 1 pendente, veio %d", len(sink.pending))
	}
	p := sink.pending[0]
	if p.ToolID != "web_post" || p.Capability != "cap:http.post" || p.ResourceValue != "https://api.example.com/x" {
		t.Fatalf("o pendente devia descrever O QUE vai executar: %+v", p)
	}
	if len(p.Preview) == 0 || string(res.EscalatedPreview) != string(p.Preview) {
		t.Fatalf("a preview do pendente e a do Result têm de coincidir (é o que as pernas assinam)")
	}
}

// TestEscalation_NaoConfirmaAActivity é a guarda do risco mais subtil: uma activity
// ESCALADA não produziu efeito nenhum. Se o loop a marcasse como confirmada no cursor
// intra-iteração, a retoma SALTÁ-LA-IA e a acção aprovada nunca executaria.
func TestEscalation_NaoConfirmaAActivity(t *testing.T) {
	cp := &countingCheckpointer{}
	sink := &spySink{}
	rt, _ := newEscalationRuntime(t, sink, WithCheckpointer(cp))
	if _, err := rt.Run(context.Background(), sampleGoal()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if cp.activities != 0 {
		t.Fatalf("uma activity ESCALADA (sem efeito) NÃO pode ser confirmada no cursor — a retoma saltá-la-ia; confirmadas=%d", cp.activities)
	}
}

// TestEscalation_SemSinkComportamentoInalterado: sem [WithEscalationSink] o escalate
// continua a ser tratado como uma negação (o run prossegue) — retro-compatível.
func TestEscalation_SemSinkComportamentoInalterado(t *testing.T) {
	h := newHarness(t, nil)
	rm := referencemonitor.New(
		referencemonitor.WithHooks(escalatingHook{capability: "cap:http.post"}),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(h.store)),
	)
	if err := rm.Register("web_post", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rt := New(escalatingModel(), rm, h.recorder)
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Escalated {
		t.Fatalf("sem sink não devia haver escalada registada")
	}
	if !res.Terminated {
		t.Fatalf("sem sink o run devia prosseguir e terminar no turno 2; res=%+v", res)
	}
}

// TestEscalation_FalhaDaSuspensaoEhFatal: se a transição durável falhar, o erro SOBE —
// prosseguir deixaria o agente a avançar como se nada tivesse ficado por decidir.
func TestEscalation_FalhaDaSuspensaoEhFatal(t *testing.T) {
	boom := errors.New("transição durável falhou")
	rt, _ := newEscalationRuntime(t, &spySink{err: boom})
	if _, err := rt.Run(context.Background(), sampleGoal()); !errors.Is(err, boom) {
		t.Fatalf("a falha da suspensão devia ser FATAL; err=%v", err)
	}
}

// TestEscalation_NenhumEfeitoOcorreu: o resultado de uma call escalada é untrusted-vazio
// (o RM não despachou nada) e o modelo vê o marcador com effect=escalate.
func TestEscalation_NenhumEfeitoOcorreu(t *testing.T) {
	rt, _ := newEscalationRuntime(t, &spySink{})
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.ToolResults) != 1 {
		t.Fatalf("esperava 1 resultado, veio %d", len(res.ToolResults))
	}
	if len(res.ToolResults[0].Value) != 0 || !res.ToolResults[0].IsUntrusted() {
		t.Fatalf("uma call escalada não produz efeito: valor tem de ser untrusted-vazio; %+v", res.ToolResults[0])
	}
}
