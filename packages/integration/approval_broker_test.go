package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// callParaAprovar é a tool call PRIVILEGIADA que precisa de aval humano.
func callParaAprovar() referencemonitor.Call {
	return referencemonitor.Call{
		RunID:      "run-aprov",
		StepID:     "s1",
		ToolID:     "web_post",
		Capability: "cap:http.post",
		Resource:   referencemonitor.Resource{Type: "http", Value: "https://api.example.com/results", Region: "eu"},
		Principal:  referencemonitor.Principal{NHIID: "agt-1"},
		Input:      []byte(`{"body":"resultado-42"}`),
	}
}

// brokerComAprovacaoDual monta um broker sobre o four-eyes REAL e conclui a cerimónia de
// dual-control para a preview dada, devolvendo o broker e o id do grant.
func brokerComAprovacaoDual(t *testing.T, preview []byte, opts ...ApprovalBrokerOption) (*ApprovalBroker, string) {
	t.Helper()
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	req := FourEyesRequest{
		RequestID:           "req-aprov-1",
		Preview:             preview,
		RiskClass:           risk.ClassDanger,
		DualControlRequired: true, // danger ⇒ irreversível ⇒ duas pernas distintas
	}
	legA := SignFourEyesLeg(privA, req, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	legB := SignFourEyesLeg(privB, req, "human:bob", "sess-B", "cred-B", challenge32(t), nil)

	broker, err := NewApprovalBroker(gate, NewMemApprovalStore(), opts...)
	if err != nil {
		t.Fatalf("NewApprovalBroker: %v", err)
	}
	g, err := broker.Approve(context.Background(), "grant-1", req, legA, legB)
	if err != nil {
		t.Fatalf("Approve (cerimónia dual): %v", err)
	}
	if !g.DualControl || len(g.Approvers) != 2 {
		t.Fatalf("grant devia registar dual-control com 2 aprovadores: %+v", g)
	}
	return broker, g.ID
}

// TestBroker_CerimoniaDestravaAAccaoAprovada é o caminho feliz: concluída a cerimónia
// four-eyes REAL, o grant verifica-se contra a preview daquela call.
func TestBroker_CerimoniaDestravaAAccaoAprovada(t *testing.T) {
	call := callParaAprovar()
	preview := referencemonitor.ApprovalPreview(call)
	broker, grantID := brokerComAprovacaoDual(t, preview)

	proof, err := broker.VerifyApproval(context.Background(), []byte(grantID), preview)
	if err != nil {
		t.Fatalf("a aprovação concluída devia verificar: %v", err)
	}
	if !proof.DualControl || len(proof.Approvers) != 2 {
		t.Fatalf("prova devia transportar a atribuição: %+v", proof)
	}
}

// TestBroker_UsoUnico: uma aprovação destrava UMA execução. A segunda tentativa com o
// mesmo grant falha — fecha o "aprovar uma vez, executar várias".
func TestBroker_UsoUnico(t *testing.T) {
	call := callParaAprovar()
	preview := referencemonitor.ApprovalPreview(call)
	broker, grantID := brokerComAprovacaoDual(t, preview)

	if _, err := broker.VerifyApproval(context.Background(), []byte(grantID), preview); err != nil {
		t.Fatalf("1.ª utilização devia passar: %v", err)
	}
	_, err := broker.VerifyApproval(context.Background(), []byte(grantID), preview)
	if !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("2.ª utilização devia falhar (uso-único); err=%v", err)
	}
}

// TestBroker_GrantDeOutraAccaoNaoServe é o anti-relay ao nível do broker: um grant
// legitimamente obtido para UMA acção não destrava outra.
func TestBroker_GrantDeOutraAccaoNaoServe(t *testing.T) {
	aprovada := callParaAprovar()
	previewAprovada := referencemonitor.ApprovalPreview(aprovada)
	broker, grantID := brokerComAprovacaoDual(t, previewAprovada)

	outra := callParaAprovar()
	outra.Resource.Value = "https://api.example.com/OUTRO"
	previewOutra := referencemonitor.ApprovalPreview(outra)

	_, err := broker.VerifyApproval(context.Background(), []byte(grantID), previewOutra)
	if !errors.Is(err, ErrGrantPreviewMismatch) {
		t.Fatalf("grant de outra acção não devia servir; err=%v", err)
	}
	// E ficou QUEIMADO: a tentativa de desvio não deixa o grant disponível para a acção
	// legítima (ordem fail-closed — consumir antes de verificar a amarra).
	if _, err := broker.VerifyApproval(context.Background(), []byte(grantID), previewAprovada); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("uma tentativa de desvio devia QUEIMAR o grant; err=%v", err)
	}
}

// TestBroker_ExpiraAos15Min: a decisão do dono é uma janela de 15 minutos. Passada ela, a
// aprovação não vale — o contexto em que o humano decidiu pode já não ser o mesmo.
func TestBroker_ExpiraAos15Min(t *testing.T) {
	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	agora := base
	call := callParaAprovar()
	preview := referencemonitor.ApprovalPreview(call)
	broker, grantID := brokerComAprovacaoDual(t, preview, WithApprovalClock(func() time.Time { return agora }))

	agora = base.Add(DefaultApprovalTTL + time.Second) // 15 min + 1s depois
	_, err := broker.VerifyApproval(context.Background(), []byte(grantID), preview)
	if !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("passados 15 min a aprovação devia expirar; err=%v", err)
	}
}

// TestBroker_DentroDoTTLAindaVale é o contraste do teste anterior (prova que a expiração
// não é vacuosa).
func TestBroker_DentroDoTTLAindaVale(t *testing.T) {
	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	agora := base
	call := callParaAprovar()
	preview := referencemonitor.ApprovalPreview(call)
	broker, grantID := brokerComAprovacaoDual(t, preview, WithApprovalClock(func() time.Time { return agora }))

	agora = base.Add(14 * time.Minute)
	if _, err := broker.VerifyApproval(context.Background(), []byte(grantID), preview); err != nil {
		t.Fatalf("dentro dos 15 min a aprovação devia valer; err=%v", err)
	}
}

// TestBroker_CerimoniaRecusadaNaoPersisteNada: se o four-eyes recusar (aqui: as duas
// pernas do MESMO principal, violando a distinção estrutural), nenhum grant é criado.
func TestBroker_CerimoniaRecusadaNaoPersisteNada(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")

	req := FourEyesRequest{
		RequestID:           "req-recusa",
		Preview:             referencemonitor.ApprovalPreview(callParaAprovar()),
		RiskClass:           risk.ClassDanger,
		DualControlRequired: true,
	}
	// Duas pernas do MESMO aprovador ⇒ não é four-eyes.
	legA := SignFourEyesLeg(privA, req, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	legB := SignFourEyesLeg(privA, req, "human:alice", "sess-B", "cred-B", challenge32(t), nil)

	store := NewMemApprovalStore()
	broker, err := NewApprovalBroker(gate, store)
	if err != nil {
		t.Fatalf("NewApprovalBroker: %v", err)
	}
	if _, err := broker.Approve(context.Background(), "grant-x", req, legA, legB); err == nil {
		t.Fatal("a cerimónia devia ser RECUSADA (mesmo principal nas duas pernas)")
	}
	// Nada persistido ⇒ nada destrava.
	if _, err := broker.VerifyApproval(context.Background(), []byte("grant-x"), req.Preview); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("uma cerimónia recusada não pode deixar grant; err=%v", err)
	}
}

// TestBroker_EvidenciaVaziaOuInventada: bytes arbitrários não são uma aprovação.
func TestBroker_EvidenciaVaziaOuInventada(t *testing.T) {
	call := callParaAprovar()
	preview := referencemonitor.ApprovalPreview(call)
	broker, _ := brokerComAprovacaoDual(t, preview)

	for _, ev := range [][]byte{nil, []byte(""), []byte("grant-inventado")} {
		if _, err := broker.VerifyApproval(context.Background(), ev, preview); !errors.Is(err, ErrGrantNotFound) {
			t.Fatalf("evidência %q não devia verificar; err=%v", ev, err)
		}
	}
}

// TestBroker_LigadoAoApprovalGateDestrava é a prova de INTEGRAÇÃO das duas etapas: o
// broker (política) ligado ao ApprovalGate do kernel (invariante) destrava de facto uma
// capability privilegiada que o taint-gate negaria — e SÓ ela.
func TestBroker_LigadoAoApprovalGateDestrava(t *testing.T) {
	call := callParaAprovar()
	call.Context = referencemonitor.CallContext{Taint: "untrusted"} // originada pelo modelo
	preview := referencemonitor.ApprovalPreview(call)
	broker, grantID := brokerComAprovacaoDual(t, preview)

	m := referencemonitor.New(referencemonitor.WithHooks(
		referencemonitor.NewApprovalGate(broker),
		referencemonitor.NewTaintGate(referencemonitor.NewStaticPrivilegedSet("cap:http.post")),
	))
	if err := m.Register("web_post", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// (1) SEM evidência: negada pelo taint-gate, como sempre.
	if dec, _ := m.Mediate(context.Background(), call); dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "taint" {
		t.Fatalf("sem aprovação devia ser deny|taint; effect=%s by=%s", dec.Effect, dec.DeniedBy)
	}

	// (2) COM a evidência do grant: destrava — e o taint continua untrusted.
	aprovada := call
	aprovada.ApprovalEvidence = []byte(grantID)
	dec, err := m.Mediate(context.Background(), aprovada)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("com aprovação four-eyes válida devia PERMITIR; effect=%s by=%s reason=%s", dec.Effect, dec.DeniedBy, dec.Reason)
	}
	if aprovada.Context.Taint != "untrusted" {
		t.Fatalf("o taint NÃO pode mudar por causa da aprovação; taint=%q", aprovada.Context.Taint)
	}

	// (3) A MESMA evidência não serve segunda vez (uso-único atravessa a cadeia toda).
	outraVez := call
	outraVez.ApprovalEvidence = []byte(grantID)
	if dec, _ := m.Mediate(context.Background(), outraVez); dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("reutilizar a aprovação devia falhar; effect=%s", dec.Effect)
	}
}
