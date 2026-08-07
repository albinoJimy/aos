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

// evidenceSource devolve a evidência apenas para a preview registada — modela a store de
// grants do nó (infraestrutura TRUSTED, nunca o modelo).
type evidenceSource struct {
	forPreview []byte
	evidence   []byte
	asked      int
}

func (s *evidenceSource) EvidenceFor(_ context.Context, _ string, preview []byte) []byte {
	s.asked++
	if s.forPreview != nil && string(preview) == string(s.forPreview) {
		return s.evidence
	}
	return nil
}

// approvingHook modela a cadeia REAL na retoma: escala a capability privilegiada SALVO se
// a call trouxer evidência de aprovação verificada (é o papel combinado
// ApprovalGate+TaintGate, aqui condensado para isolar o comportamento do LOOP).
type approvingHook struct{ capability string }

func (approvingHook) Name() string { return "risk" }

func (h approvingHook) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	if call.Capability != h.capability {
		return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
	}
	if len(call.ApprovalEvidence) > 0 {
		return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
	}
	return referencemonitor.HookResult{Decision: referencemonitor.HookEscalate, Reason: "requer aval humano"}, nil
}

// TestEscalation_RetomaComEvidenciaExecuta fecha o CICLO do bridge: (1) sem aprovação a
// acção escala e o run suspende; (2) com a evidência da aprovação disponível para AQUELA
// preview, a MESMA acção volta a ser mediada e EXECUTA.
func TestEscalation_RetomaComEvidenciaExecuta(t *testing.T) {
	h := newHarness(t, nil)
	rm := referencemonitor.New(
		referencemonitor.WithHooks(approvingHook{capability: "cap:http.post"}),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(h.store)),
	)
	execCount := 0
	if err := rm.Register("web_post", func(_ context.Context, in []byte) ([]byte, error) {
		execCount++
		return []byte("publicado"), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// (1) 1.ª passagem: sem evidência ⇒ escalate ⇒ suspende, sem efeito.
	sink := &spySink{}
	rt1 := New(escalatingModel(), rm, h.recorder, WithEscalationSink(sink))
	res1, err := rt1.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run (1.ª): %v", err)
	}
	if !res1.Escalated || execCount != 0 {
		t.Fatalf("1.ª passagem devia escalar SEM efeito; escalated=%t execs=%d", res1.Escalated, execCount)
	}
	preview := res1.EscalatedPreview

	// (2) O humano aprovou: a store passa a ter evidência para AQUELA preview. A retoma
	// reproduz o turno (aqui, o mesmo modelo determinista) e a acção executa.
	src := &evidenceSource{forPreview: preview, evidence: []byte("grant-1")}
	rt2 := New(escalatingModel(), rm, h.recorder,
		WithEscalationSink(sink), WithApprovalEvidence(src))
	res2, err := rt2.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run (retoma): %v", err)
	}
	if res2.Escalated {
		t.Fatalf("com aprovação a retoma NÃO devia voltar a escalar; res=%+v", res2)
	}
	if execCount != 1 {
		t.Fatalf("a acção aprovada devia EXECUTAR exactamente 1x; execs=%d", execCount)
	}
	if !res2.Terminated {
		t.Fatalf("depois de executar, o run devia prosseguir e terminar; res=%+v", res2)
	}
}

// TestEscalation_EvidenciaSoParaAPreviewCerta: a fonte é consultada POR PREVIEW. Uma
// evidência registada para OUTRA acção não é entregue a esta — a amarra é exacta.
func TestEscalation_EvidenciaSoParaAPreviewCerta(t *testing.T) {
	h := newHarness(t, nil)
	rm := referencemonitor.New(
		referencemonitor.WithHooks(approvingHook{capability: "cap:http.post"}),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(h.store)),
	)
	execCount := 0
	if err := rm.Register("web_post", func(_ context.Context, _ []byte) ([]byte, error) {
		execCount++
		return nil, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Evidência registada para uma preview QUE NÃO É a desta call.
	src := &evidenceSource{forPreview: []byte("preview-de-outra-accao"), evidence: []byte("grant-x")}
	rt := New(escalatingModel(), rm, h.recorder,
		WithEscalationSink(&spySink{}), WithApprovalEvidence(src))

	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Escalated {
		t.Fatalf("evidência de OUTRA acção não pode destravar esta; res=%+v", res)
	}
	if execCount != 0 {
		t.Fatalf("nada devia executar; execs=%d", execCount)
	}
	if src.asked == 0 {
		t.Fatalf("a fonte devia ter sido consultada (por preview)")
	}
}
