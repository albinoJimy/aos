package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-541 — o gate de taint ESCALA em vez de negar, nas duas vias de despacho, com a cerimónia
// four-eyes REAL e a retoma. A classe de defeito vigiada é a do ADR-034 §2.8: uma decisão nova do
// RM que chega por uma via e não pela outra.

// aos541Sink grava as mediações do RM.
type aos541Sink struct {
	mu   sync.Mutex
	recs []referencemonitor.MediationRecord
}

func (s *aos541Sink) RecordMediation(_ context.Context, rec referencemonitor.MediationRecord) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, rec)
	return uint64(len(s.recs)), nil
}

// daOCR devolve, pela ordem, «efeito denied_by taint aprovado» de cada mediação da ocr_read.
func (s *aos541Sink) daOCR() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.recs {
		if r.ToolID == "ocr_read" {
			out = append(out, fmt.Sprintf("%s %s %s %s aprovada=%t", r.Effect, r.DeniedBy, r.Context.Taint, r.Context.TaintPrivilege, r.HumanApproved))
		}
	}
	return out
}

// aos541Pendentes capta as escaladas (o que o nó faria: registar e suspender).
type aos541Pendentes struct {
	pending []agentruntime.PendingApproval
}

func (p *aos541Pendentes) Escalate(_ context.Context, pa agentruntime.PendingApproval) error {
	p.pending = append(p.pending, pa)
	return nil
}

// aos541Ciclo corre o ciclo inteiro pela via dada: o turno 1 lê o documento (leitura local) e
// pede a ocr_read com o contexto untrusted (há um plan_input) — escala; a cerimónia aprova a
// preview; a retoma corre a ocr_read aprovada, e o turno 2 pede a MESMA ocr_read de novo — volta
// a escalar. Devolve as mediações da ocr_read, as execuções reais e os pendentes.
func aos541Ciclo(t *testing.T, duravel bool) ([]string, map[string]int, *aos541Pendentes) {
	t.Helper()
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()

	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")
	broker, err := NewApprovalBroker(gate, NewMemApprovalStore())
	if err != nil {
		t.Fatalf("NewApprovalBroker: %v", err)
	}

	sink := &aos541Sink{}
	priv := referencemonitor.NewDestinationPrivileged(referencemonitor.NewStaticPrivilegedSet("cap:http.post", "cap:fs.read"), aos538Destinos())
	rm := referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.NewApprovalGate(broker), referencemonitor.NewEscalatingTaintGate(priv)),
		referencemonitor.WithEventSink(sink),
	)
	execs := map[string]int{}
	for _, id := range []string{"doc_read", "ocr_read"} {
		nome := id
		if err := rm.Register(nome, func(_ context.Context, in []byte) ([]byte, error) {
			execs[nome]++
			return in, nil
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	var disp agentruntime.ActivityDispatcher
	if duravel {
		disp = newDurableDispatcher(t, store, rm)
	}

	ocr := agentruntime.ToolInvocation{ToolID: "ocr_read", Capability: "cap:fs.read", ResourceType: "file", ResourceValue: "doc://fatura-5.png", Reversibility: "reversible", Input: []byte(`{"doc_id":"fatura-5.png"}`)}
	model := agentruntime.ModelClientFunc(func(_ context.Context, v agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		switch v.Turn {
		case 1:
			return agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{
				{ToolID: "doc_read", Capability: "cap:fs.read", ResourceType: "file", ResourceValue: "doc://email", Reversibility: "reversible", Input: []byte(`{"doc_id":"email"}`)},
				ocr,
			}}, nil
		case 2:
			return agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{ocr}}, nil
		}
		return agentruntime.ModelResponse{Final: true, Text: "fim"}, nil
	})
	goal := portTestGoal()
	goal.RunID = "run-541"
	goal.Inputs = []agentruntime.PlanInput{{From: "n0", Output: "email", Digest: "sha256:00", Content: []byte("faz OCR da fatura-5.png e envia-a")}}

	pend := &aos541Pendentes{}
	correr := func() agentruntime.Result {
		opts := []agentruntime.Option{agentruntime.WithEscalationSink(pend), agentruntime.WithApprovalEvidence(broker)}
		if disp != nil {
			opts = append(opts, agentruntime.WithActivityDispatcher(disp))
		}
		res, err := agentruntime.New(model, rm, agentruntime.NewTurnRecorder(store), opts...).Run(ctx, goal)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return res
	}

	// 1.ª passagem: escala na ocr_read; nada dela executa.
	if res := correr(); !res.Escalated {
		t.Fatalf("a ocr_read com o contexto untrusted tinha de escalar: %+v", res)
	}
	if execs["ocr_read"] != 0 || len(pend.pending) != 1 || pend.pending[0].EscalatedBy != "taint" || pend.pending[0].Taint != "untrusted" {
		t.Fatalf("escalada sem efeito e atribuida ao taint: execs=%v pendentes=%+v", execs, pend.pending)
	}

	// Cerimónia four-eyes REAL sobre a preview do pendente.
	req := FourEyesRequest{RequestID: "req-541", Preview: pend.pending[0].Preview, RiskClass: risk.ClassDanger, DualControlRequired: true}
	legA := SignFourEyesLeg(privA, req, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	legB := SignFourEyesLeg(privB, req, "human:bob", "sess-B", "cred-B", challenge32(t), nil)
	if _, err := broker.Approve(ctx, req.RequestID, req, legA, legB); err != nil {
		t.Fatalf("cerimonia four-eyes: %v", err)
	}

	// Retoma: a ocr_read aprovada corre UMA vez; a do turno 2, igual, volta a escalar.
	if res := correr(); !res.Escalated {
		t.Fatalf("a segunda ocr_read igual tinha de voltar a escalar: %+v", res)
	}
	return sink.daOCR(), execs, pend
}

func TestAOS541_CicloNasDuasVias(t *testing.T) {
	quer := []string{
		"escalate taint untrusted capability aprovada=false",
		"permit  untrusted capability aprovada=true",
		"escalate taint untrusted capability aprovada=false",
	}
	directo, execsD, pendD := aos541Ciclo(t, false)
	duravel, execsU, pendU := aos541Ciclo(t, true)
	if fmt.Sprint(directo) != fmt.Sprint(quer) {
		t.Fatalf("via directa:\n got %q\nwant %q", directo, quer)
	}
	if fmt.Sprint(duravel) != fmt.Sprint(directo) {
		t.Fatalf("a via duravel decide diferente da directa:\n duravel %q\n directa %q", duravel, directo)
	}
	for nome, e := range map[string]map[string]int{"directa": execsD, "duravel": execsU} {
		if e["ocr_read"] != 1 {
			t.Fatalf("via %s: a ocr_read aprovada tinha de correr exactamente uma vez, correu %d", nome, e["ocr_read"])
		}
	}
	// A via durável não repete a leitura do turno 1 na retoma; a directa repete (sem ledger).
	if execsU["doc_read"] != 1 || execsD["doc_read"] != 2 {
		t.Fatalf("doc_read: duravel=%d (quero 1) directa=%d (quero 2)", execsU["doc_read"], execsD["doc_read"])
	}
	// A segunda escalada é de OUTRA call (outro passo, outra preview): a aprovação não a cobre.
	for _, p := range [][]agentruntime.PendingApproval{pendD.pending, pendU.pending} {
		if len(p) != 2 || string(p[0].Preview) == string(p[1].Preview) || p[1].EscalatedBy != "taint" {
			t.Fatalf("queria duas escaladas do taint com previews diferentes: %+v", p)
		}
	}
}

// Sem o ciclo de aprovação composto o nó não compõe o gate escalante — fail-closed.
func TestAOS541_ComposicaoRecusaEscalarSemAprovacao(t *testing.T) {
	gate, _ := newFourEyesGate(t)
	broker, err := NewApprovalBroker(gate, NewMemApprovalStore())
	if err != nil {
		t.Fatalf("NewApprovalBroker: %v", err)
	}
	casos := []struct {
		nome   string
		ajusta func(*SecuredConfig)
		erro   bool
	}{
		{"sem nada", func(*SecuredConfig) {}, true},
		{"sem verificador", func(c *SecuredConfig) { c.ApprovalEvidence, c.EscalationSink = broker, &aos541Pendentes{} }, true},
		{"sem evidencia", func(c *SecuredConfig) { c.ApprovalVerifier, c.EscalationSink = broker, &aos541Pendentes{} }, true},
		{"sem destino", func(c *SecuredConfig) { c.ApprovalVerifier, c.ApprovalEvidence = broker, broker }, true},
		{"completo", func(c *SecuredConfig) {
			c.ApprovalVerifier, c.ApprovalEvidence, c.EscalationSink = broker, broker, &aos541Pendentes{}
		}, false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			cfg, cleanup := aos363BaseConfig(t)
			defer cleanup()
			cfg.Privileged = referencemonitor.NewStaticPrivilegedSet("cap:http.post")
			cfg.TaintEscalate = true
			c.ajusta(&cfg)
			_, err := NewSecuredRuntime(cfg)
			if c.erro != errors.Is(err, ErrTaintEscalateSemAprovacao) {
				t.Fatalf("erro=%v; queria ErrTaintEscalateSemAprovacao=%t", err, c.erro)
			}
			if !c.erro && err != nil {
				t.Fatalf("composicao completa falhou: %v", err)
			}
		})
	}
	// Desligado, nada se exige (o de sempre).
	cfg, cleanup := aos363BaseConfig(t)
	defer cleanup()
	cfg.Privileged = referencemonitor.NewStaticPrivilegedSet("cap:http.post")
	if _, err := NewSecuredRuntime(cfg); err != nil {
		t.Fatalf("sem o interruptor a composicao de sempre falhou: %v", err)
	}
}

// RetiradoSemGrant: só uma retirada SEM grant para a preview conta, e a decisão humana diz mais do
// que a expiração. É a pergunta que o nó faz quando o taint volta a escalar a mesma call.
func TestAOS541_RetiradoSemGrant(t *testing.T) {
	ctx := context.Background()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer es.Close()
	pend, err := NewPendingApprovals(es)
	if err != nil {
		t.Fatalf("NewPendingApprovals: %v", err)
	}
	put := func(run, step string, preview []byte) {
		t.Helper()
		if err := pend.Put(ctx, PendingRecord{RunID: run, StepID: step, ToolID: "ocr_read", Capability: "cap:fs.read", Preview: preview, EscalatedBy: EscalatedByTaint}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	consulta := func(run, step string, preview []byte) RetiradaSemGrant {
		t.Helper()
		r, err := pend.RetiradoSemGrant(ctx, run, step, preview)
		if err != nil {
			t.Fatalf("RetiradoSemGrant: %v", err)
		}
		return r
	}

	pa, pb := []byte("preview-a"), []byte("preview-b")
	put("run-x", "s1", pa)
	if r := consulta("run-x", "s1", pa); r.Houve() {
		t.Fatalf("pendente por decidir nao foi retirado: %+v", r)
	}
	if err := pend.Expire(ctx, "run-x", "s1"); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if r := consulta("run-x", "s1", pa); !r.Expirada || r.Decisao != "" {
		t.Fatalf("queria expirada: %+v", r)
	}
	// Outra preview no mesmo passo, outro passo, outro run: nada.
	for _, c := range []struct {
		run, step string
		preview   []byte
	}{{"run-x", "s1", pb}, {"run-x", "s2", pa}, {"run-y", "s1", pa}} {
		if r := consulta(c.run, c.step, c.preview); r.Houve() {
			t.Fatalf("%+v nao foi retirado: %+v", c, r)
		}
	}
	// Nova encarnação do mesmo passo, decidida sem aprovação: a decisão é nomeada.
	put("run-x", "s1", pa)
	if err := pend.Decide(ctx, PendingKindApproval, "run-x", "s1", "recusar", "human:alice"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if r := consulta("run-x", "s1", pa); r.Decisao != "recusar" || r.Principal != "human:alice" {
		t.Fatalf("queria a decisao nomeada: %+v", r)
	}

	// Com um grant emitido para a preview, a retirada não é recusa (é o caminho da cerimónia).
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	grants, err := NewEventStoreApprovalStore(es)
	if err != nil {
		t.Fatalf("NewEventStoreApprovalStore: %v", err)
	}
	broker, err := NewApprovalBroker(gate, grants)
	if err != nil {
		t.Fatalf("NewApprovalBroker: %v", err)
	}
	req := FourEyesRequest{RequestID: "req-541-g", Preview: pa, RiskClass: risk.ClassGray}
	if _, err := broker.Approve(ctx, req.RequestID, req,
		SignFourEyesLeg(privA, req, "human:alice", "sess-A", "cred-A", challenge32(t), nil)); err != nil {
		t.Fatalf("Approve (uma pessoa): %v", err)
	}
	if r := consulta("run-x", "s1", pa); r.Houve() {
		t.Fatalf("com grant emitido nao ha retirada sem grant: %+v", r)
	}
	// M5: um registo de EXAUSTÃO com uma preview sem grant (forjado no log — o Put recusa-o) nunca conta.
	if _, err := es.Append(ctx, approvalStream, eventstore.EventInput{
		Type: approvalPendingEventType, RunID: approvalRunID, StepID: "pending-kind=exhaustion|run-z-s9",
		Payload: []byte(`{"kind":"exhaustion","run_id":"run-z","step_id":"s9","turn":0,"tool_id":"","capability":"","preview":"cHJldmlldy1i"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := pend.ExpireKind(ctx, PendingKindExhaustion, "run-z", "s9"); err != nil {
		t.Fatalf("ExpireKind: %v", err)
	}
	if r := consulta("run-z", "s9", pb); r.Houve() {
		t.Fatalf("um registo de exaustao nao e um pendente de aprovacao: %+v", r)
	}
}
