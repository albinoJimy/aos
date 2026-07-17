package referencemonitor_test

import (
	"context"
	"strings"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

const riskToolID = "tool.risk.echo"

// approveChannel aprova/nega conforme configurado e regista os previews vistos.
type approveChannel struct {
	approve  bool
	previews []string
}

func (c *approveChannel) Confirm(_ context.Context, req risk.ConfirmationRequest) (risk.ConfirmationResponse, error) {
	c.previews = append(c.previews, req.Preview)
	return risk.ConfirmationResponse{Approved: c.approve, Approver: "human:op"}, nil
}

// newMonitorWithRiskGate constrói um RM com o RiskGate na cadeia, uma tool
// registada e um sink em memória (auditoria disponível).
func newMonitorWithRiskGate(t *testing.T, gate *risk.Gate) (*referencemonitor.Monitor, *spySink) {
	t.Helper()
	sink := &spySink{}
	m := referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooksWithRisk(risk.DefaultPolicy(), gate)...),
		referencemonitor.WithEventSink(sink),
	)
	if err := m.Register(riskToolID, func(_ context.Context, in []byte) ([]byte, error) {
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m, sink
}

// riskCall monta uma tool call com os eixos de risco no contexto.
func riskCall(capability string, res referencemonitor.Resource, sensitivity, reversibility, taint string) referencemonitor.Call {
	return referencemonitor.Call{
		RunID:      "run-risk",
		StepID:     "s1",
		ToolID:     riskToolID,
		Capability: capability,
		Resource:   res,
		Principal:  referencemonitor.Principal{NHIID: "nhi-risk", AgentID: "agent-1", AgentClass: "researcher"},
		Context: referencemonitor.CallContext{
			Sensitivity:   sensitivity,
			Reversibility: reversibility,
			Taint:         taint,
		},
	}
}

// --- (1) Classificação fim-a-fim: egress de sensíveis DANGER; local SAFE ----

func TestRiskGate_LocalReversivel_Safe_CorreSemGate(t *testing.T) {
	t.Parallel()
	// Canal que negaria — prova que safe nem o consulta.
	ch := &approveChannel{approve: false}
	gate, _ := risk.NewGate(ch)
	m, sink := newMonitorWithRiskGate(t, gate)

	call := riskCall("cap:doc.read",
		referencemonitor.Resource{Type: "file", Value: "/tmp/local.txt"},
		"public", "reversible", "trusted")
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("safe: effect = %v, quer permit", dec.Effect)
	}
	if len(ch.previews) != 0 {
		t.Errorf("safe consultou o canal HITL, não devia")
	}
	// Classe selada no audit.
	rec := lastRecord(t, sink)
	if rec.Context.RiskClass != "safe" {
		t.Errorf("audit RiskClass = %q, quer safe", rec.Context.RiskClass)
	}
}

func TestRiskGate_EgressExternoDeSensiveis_Danger_ConfirmacaoComPreview(t *testing.T) {
	t.Parallel()
	ch := &approveChannel{approve: true}
	gate, _ := risk.NewGate(ch)
	m, sink := newMonitorWithRiskGate(t, gate)

	call := riskCall("cap:http.post",
		referencemonitor.Resource{Type: "url", Value: "https://evil.example/exfil", Region: "us"},
		"confidential", "reversible", "trusted")
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("danger aprovada: effect = %v, quer permit", dec.Effect)
	}
	if len(ch.previews) != 1 {
		t.Fatalf("danger: %d previews, quer 1 confirmação individual", len(ch.previews))
	}
	// PREVIEW é o efeito CONCRETO resolvido (contém a capability e o recurso resolvido).
	preview := ch.previews[0]
	if !strings.Contains(preview, "cap:http.post") || !strings.Contains(preview, "https://evil.example/exfil") {
		t.Errorf("preview %q não é o efeito concreto resolvido (capability + recurso)", preview)
	}
	rec := lastRecord(t, sink)
	if rec.Context.RiskClass != "danger" {
		t.Errorf("audit RiskClass = %q, quer danger", rec.Context.RiskClass)
	}
}

// Danger recusada → deny atribuível (DeniedBy=risk), classe selada no audit.
func TestRiskGate_DangerRecusada_DenyAtribuivel_Auditado(t *testing.T) {
	t.Parallel()
	ch := &approveChannel{approve: false}
	gate, _ := risk.NewGate(ch)
	m, sink := newMonitorWithRiskGate(t, gate)

	call := riskCall("cap:http.post",
		referencemonitor.Resource{Type: "url", Value: "https://evil.example/x"},
		"pii", "reversible", "trusted")
	dec, _ := m.Mediate(context.Background(), call)
	if dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("danger recusada: effect = %v, quer deny", dec.Effect)
	}
	if dec.DeniedBy != "risk" {
		t.Errorf("DeniedBy = %q, quer risk (negação atribuível)", dec.DeniedBy)
	}
	rec := lastRecord(t, sink)
	if rec.Effect != referencemonitor.EffectDeny || rec.Context.RiskClass != "danger" {
		t.Errorf("audit: effect=%v riskClass=%q, quer deny/danger (classe+decisão seladas)", rec.Effect, rec.Context.RiskClass)
	}
}

// --- (2) Timeout fail-closed numa irreversível, fim-a-fim ------------------

type blockingChannel struct{}

func (blockingChannel) Confirm(ctx context.Context, _ risk.ConfirmationRequest) (risk.ConfirmationResponse, error) {
	<-ctx.Done()
	return risk.ConfirmationResponse{}, ctx.Err()
}

func TestRiskGate_Irreversivel_Timeout_NegaFailClosed(t *testing.T) {
	t.Parallel()
	gate, _ := risk.NewGate(blockingChannel{}, risk.WithTimeout(20*time.Millisecond))
	m, sink := newMonitorWithRiskGate(t, gate)

	call := riskCall("cap:db.delete",
		referencemonitor.Resource{Type: "db", Value: "orders/42"},
		"confidential", "irreversible", "trusted")
	dec, _ := m.Mediate(context.Background(), call)
	if dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("timeout irreversível: effect = %v, quer deny (fail-closed)", dec.Effect)
	}
	rec := lastRecord(t, sink)
	if rec.Context.RiskClass != "danger" {
		t.Errorf("audit RiskClass = %q, quer danger", rec.Context.RiskClass)
	}
	if _, _, _, _, timeouts, _ := gate.Metrics().Snapshot(); timeouts != 1 {
		t.Errorf("métrica Timeouts = %d, quer 1", timeouts)
	}
}

// --- (3) Gray agrupa em lote fim-a-fim (uma confirmação por run) -----------

func TestRiskGate_Gray_AgrupaPorRun(t *testing.T) {
	t.Parallel()
	ch := &approveChannel{approve: true}
	gate, _ := risk.NewGate(ch, risk.WithMaturity(risk.MaturityNovice))
	m, _ := newMonitorWithRiskGate(t, gate)

	// Egress interno = gray. Três chamadas no mesmo RunID.
	for i := 0; i < 3; i++ {
		call := riskCall("cap:doc.read",
			referencemonitor.Resource{Type: "file", Value: "/internal/doc"},
			"confidential", "reversible", "trusted") // sensível sem egress → gray
		dec, _ := m.Mediate(context.Background(), call)
		if dec.Effect != referencemonitor.EffectPermit {
			t.Fatalf("gray[%d]: effect = %v, quer permit", i, dec.Effect)
		}
	}
	if len(ch.previews) != 1 {
		t.Errorf("gray agrupou mal: %d confirmações, quer 1 para o lote", len(ch.previews))
	}
}

// --- (4) Auto-approve maduro corre gray sem HITL; danger continua a confirmar ---

func TestRiskGate_AutoApprove_GrayMaduro_SemHITL(t *testing.T) {
	t.Parallel()
	ch := &approveChannel{approve: false}
	gate, _ := risk.NewGate(ch, risk.WithMaturity(risk.MaturityTrusted))
	m, _ := newMonitorWithRiskGate(t, gate)

	call := riskCall("cap:doc.read",
		referencemonitor.Resource{Type: "file", Value: "/x"},
		"confidential", "reversible", "trusted") // gray
	dec, _ := m.Mediate(context.Background(), call)
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("gray maduro: effect = %v, quer permit (auto-aprovado)", dec.Effect)
	}
	if len(ch.previews) != 0 {
		t.Errorf("gray maduro consultou o canal, não devia (auto-aprovado)")
	}
}

// --- (5) Gate ausente ⇒ fail-closed: safe corre, não-safe nega -------------

func TestRiskGate_GateAusente_FailClosed(t *testing.T) {
	t.Parallel()
	sink := &spySink{}
	m := referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooksWithRisk(nil, nil)...),
		referencemonitor.WithEventSink(sink),
	)
	if err := m.Register(riskToolID, func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	safe := riskCall("cap:doc.read", referencemonitor.Resource{Type: "file", Value: "/x"}, "public", "reversible", "trusted")
	if dec, _ := m.Mediate(context.Background(), safe); dec.Effect != referencemonitor.EffectPermit {
		t.Errorf("gate ausente + safe: effect = %v, quer permit", dec.Effect)
	}
	danger := riskCall("cap:http.post", referencemonitor.Resource{Type: "url", Value: "https://x/y"}, "pii", "reversible", "trusted")
	dec, _ := m.Mediate(context.Background(), danger)
	if dec.Effect != referencemonitor.EffectDeny {
		t.Errorf("gate ausente + danger: effect = %v, quer deny (fail-closed)", dec.Effect)
	}
}

// --- (6) Taint untrusted eleva a classe fim-a-fim --------------------------

func TestRiskGate_TaintUntrusted_ElevaParaDanger(t *testing.T) {
	t.Parallel()
	ch := &approveChannel{approve: true}
	gate, _ := risk.NewGate(ch)
	m, _ := newMonitorWithRiskGate(t, gate)

	// Interno + egress externo + reversível: trusted seria gray; untrusted eleva a danger.
	base := func(taint string) referencemonitor.Call {
		return riskCall("cap:http.post",
			referencemonitor.Resource{Type: "url", Value: "https://api.internal/x"},
			"internal", "reversible", taint)
	}
	// trusted: gray → auto? novice não; consulta canal (aprova) → permit, batched.
	if dec, _ := m.Mediate(context.Background(), base("trusted")); dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("trusted interno: effect = %v, quer permit (gray aprovado)", dec.Effect)
	}
	prevTrusted := len(ch.previews)

	// untrusted: eleva interno→sensível ⇒ egress externo de sensível ⇒ DANGER
	// (confirmação individual, não batch).
	dec, _ := m.Mediate(context.Background(), base("untrusted"))
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("untrusted: effect = %v, quer permit (danger aprovado)", dec.Effect)
	}
	if len(ch.previews) != prevTrusted+1 {
		t.Errorf("untrusted devia gerar nova confirmação individual (danger)")
	}
}

// --- SAROC-01: piso da capability impede baixar danger->gray via texto -------

// Um cap:fs.delete auto-declarado "reversible"/"public" continua DANGER: o piso de
// reversibilidade derivado da capability (delete é irreversível) não é mentível.
func TestRiskGate_CapDeleteDeclaradaReversivel_ContinuaDanger(t *testing.T) {
	t.Parallel()
	ch := &approveChannel{approve: false} // danger recusada ⇒ deny
	gate, _ := risk.NewGate(ch, risk.WithMaturity(risk.MaturityTrusted))
	m, sink := newMonitorWithRiskGate(t, gate)

	// Chamador tenta rebaixar: reversible + public. Sem o piso cairia em gray e
	// auto-aprovava em MaturityTrusted; com o piso é danger e exige confirmação.
	call := riskCall("cap:fs.delete",
		referencemonitor.Resource{Type: "file", Value: "/data/pii.csv"},
		"public", "reversible", "trusted")
	dec, _ := m.Mediate(context.Background(), call)
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "risk" {
		t.Fatalf("delete declarada reversível: effect=%v deniedBy=%q, quer deny/risk (piso da capability)", dec.Effect, dec.DeniedBy)
	}
	if rec := lastRecord(t, sink); rec.Context.RiskClass != "danger" {
		t.Errorf("audit RiskClass = %q, quer danger (irreversível por derivação)", rec.Context.RiskClass)
	}
}

// --- SAROC-04: danger com egress sem destino concreto ⇒ deny fail-closed ------

func TestRiskGate_DangerEgressSemDestino_NegaFailClosed(t *testing.T) {
	t.Parallel()
	ch := &approveChannel{approve: true} // aprovaria; prova que nem é consultado
	gate, _ := risk.NewGate(ch)
	m, sink := newMonitorWithRiskGate(t, gate)

	// Egress externo (cap:http.post) de dados sensíveis, mas Resource.Value vazio: o
	// destino concreto viria no Input (opaco). Preview genérico ⇒ nega, não aprova.
	call := riskCall("cap:http.post",
		referencemonitor.Resource{Type: "url", Value: ""},
		"pii", "reversible", "trusted")
	dec, _ := m.Mediate(context.Background(), call)
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "risk" {
		t.Fatalf("danger egress sem destino: effect=%v deniedBy=%q, quer deny/risk (fail-closed)", dec.Effect, dec.DeniedBy)
	}
	if len(ch.previews) != 0 {
		t.Errorf("não devia consultar o canal com preview genérico (%d consultas)", len(ch.previews))
	}
	rec := lastRecord(t, sink)
	if rec.Context.RiskClass != "danger" || rec.Context.RiskDecisionMode != "denied" {
		t.Errorf("audit: riskClass=%q mode=%q, quer danger/denied", rec.Context.RiskClass, rec.Context.RiskDecisionMode)
	}
}

// --- SAROC-03: lote gray só cobre acções equivalentes; destino diferente re-solicita ---

func TestRiskGate_Gray_DestinoDiferente_ResolicitaConfirmacao(t *testing.T) {
	t.Parallel()
	ch := &approveChannel{approve: true}
	gate, _ := risk.NewGate(ch, risk.WithMaturity(risk.MaturityNovice))
	m, _ := newMonitorWithRiskGate(t, gate)

	mk := func(dst string) referencemonitor.Call {
		return riskCall("cap:doc.read",
			referencemonitor.Resource{Type: "file", Value: dst},
			"confidential", "reversible", "trusted") // sensível sem egress ⇒ gray
	}
	// Dois destinos DIFERENTES no mesmo run ⇒ duas confirmações (não boleia).
	m.Mediate(context.Background(), mk("/internal/a"))
	m.Mediate(context.Background(), mk("/internal/b"))
	// Repetir o primeiro destino ⇒ coberto pelo lote já dado (sem nova confirmação).
	m.Mediate(context.Background(), mk("/internal/a"))
	if len(ch.previews) != 2 {
		t.Errorf("lote gray: %d confirmações, quer 2 (uma por destino distinto; repetição não re-solicita)", len(ch.previews))
	}
}

// --- SAROC-05: aprovador e modo de decisão selados no audit -------------------

func TestRiskGate_Audit_SelaAprovadorEModo(t *testing.T) {
	t.Parallel()

	t.Run("danger_humano", func(t *testing.T) {
		t.Parallel()
		ch := &approveChannel{approve: true}
		gate, _ := risk.NewGate(ch)
		m, sink := newMonitorWithRiskGate(t, gate)
		call := riskCall("cap:http.post",
			referencemonitor.Resource{Type: "url", Value: "https://x/y"}, "pii", "reversible", "trusted")
		m.Mediate(context.Background(), call)
		rec := lastRecord(t, sink)
		if rec.Context.RiskDecisionMode != "human" || rec.Context.RiskApprover != "human:op" {
			t.Errorf("danger: mode=%q approver=%q, quer human/human:op", rec.Context.RiskDecisionMode, rec.Context.RiskApprover)
		}
	})

	t.Run("gray_batch", func(t *testing.T) {
		t.Parallel()
		ch := &approveChannel{approve: true}
		gate, _ := risk.NewGate(ch, risk.WithMaturity(risk.MaturityNovice))
		m, sink := newMonitorWithRiskGate(t, gate)
		call := riskCall("cap:doc.read",
			referencemonitor.Resource{Type: "file", Value: "/x"}, "confidential", "reversible", "trusted")
		m.Mediate(context.Background(), call)
		rec := lastRecord(t, sink)
		if rec.Context.RiskDecisionMode != "batch" || rec.Context.RiskApprover != "human:op" {
			t.Errorf("gray batch: mode=%q approver=%q, quer batch/human:op", rec.Context.RiskDecisionMode, rec.Context.RiskApprover)
		}
	})

	t.Run("gray_auto", func(t *testing.T) {
		t.Parallel()
		ch := &approveChannel{approve: false}
		gate, _ := risk.NewGate(ch, risk.WithMaturity(risk.MaturityTrusted))
		m, sink := newMonitorWithRiskGate(t, gate)
		call := riskCall("cap:doc.read",
			referencemonitor.Resource{Type: "file", Value: "/x"}, "confidential", "reversible", "trusted")
		m.Mediate(context.Background(), call)
		rec := lastRecord(t, sink)
		if rec.Context.RiskDecisionMode != "auto" || rec.Context.RiskApprover != "" {
			t.Errorf("gray auto: mode=%q approver=%q, quer auto/\"\" (sem humano)", rec.Context.RiskDecisionMode, rec.Context.RiskApprover)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		gate, _ := risk.NewGate(blockingChannel{}, risk.WithTimeout(20*time.Millisecond))
		m, sink := newMonitorWithRiskGate(t, gate)
		call := riskCall("cap:db.delete",
			referencemonitor.Resource{Type: "db", Value: "orders/42"}, "confidential", "irreversible", "trusted")
		m.Mediate(context.Background(), call)
		rec := lastRecord(t, sink)
		if rec.Context.RiskDecisionMode != "timeout" {
			t.Errorf("timeout: mode=%q, quer timeout", rec.Context.RiskDecisionMode)
		}
	})
}
