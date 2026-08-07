package referencemonitor_test

import (
	"context"
	"errors"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

const (
	capPriv      = "cap:http.post" // privilegiada ⇒ o taint-gate barra sob untrusted
	approvalTool = "tool.approval.echo"
)

// fakeVerifier aceita a evidência "OK" desde que a preview coincida com a que lhe foi
// dada como esperada — modela o binding criptográfico do four-eyes real sem criptografia.
type fakeVerifier struct {
	accept       []byte // evidência aceite
	wantPreview  []byte // preview exigida (nil ⇒ qualquer)
	seenPreview  []byte
	calls        int
	forceFailure error
}

func (v *fakeVerifier) VerifyApproval(_ context.Context, evidence, preview []byte) (referencemonitor.ApprovalProof, error) {
	v.calls++
	v.seenPreview = append([]byte(nil), preview...)
	if v.forceFailure != nil {
		return referencemonitor.ApprovalProof{}, v.forceFailure
	}
	if string(evidence) != string(v.accept) {
		return referencemonitor.ApprovalProof{}, errors.New("evidencia invalida")
	}
	if v.wantPreview != nil && string(preview) != string(v.wantPreview) {
		return referencemonitor.ApprovalProof{}, errors.New("preview de outra accao")
	}
	return referencemonitor.ApprovalProof{Approvers: []string{"human:alice", "human:bob"}, DualControl: true}, nil
}

// newApprovalMonitor monta um RM com a cadeia approval → taint sobre uma capability
// privilegiada, e uma tool registada.
func newApprovalMonitor(t *testing.T, v referencemonitor.ApprovalVerifier) *referencemonitor.Monitor {
	t.Helper()
	m := referencemonitor.New(referencemonitor.WithHooks(
		referencemonitor.NewApprovalGate(v),
		referencemonitor.NewTaintGate(referencemonitor.NewStaticPrivilegedSet(capPriv)),
	))
	if err := m.Register(approvalTool, func(_ context.Context, in []byte) ([]byte, error) {
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m
}

// privCall é uma tool call PRIVILEGIADA com autorização untrusted (o caso do modelo).
func privCall(evidence []byte) referencemonitor.Call {
	return referencemonitor.Call{
		RunID:      "run-appr",
		StepID:     "s1",
		ToolID:     approvalTool,
		Capability: capPriv,
		Resource:   referencemonitor.Resource{Type: "http", Value: "https://api.example.com/x", Region: "eu"},
		Principal:  referencemonitor.Principal{NHIID: "agt-1"},
		Context:    referencemonitor.CallContext{Taint: "untrusted"},
		Input:      []byte(`{"body":"x"}`),

		ApprovalEvidence: evidence,
	}
}

// TestApproval_SemAprovacaoContinuaNegada é a linha de base: sem evidência, uma
// capability privilegiada autorizada por untrusted continua NEGADA — P4 intacto.
func TestApproval_SemAprovacaoContinuaNegada(t *testing.T) {
	m := newApprovalMonitor(t, &fakeVerifier{accept: []byte("OK")})
	dec, err := m.Mediate(context.Background(), privCall(nil))
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "taint" {
		t.Fatalf("sem aprovação devia ser deny|taint; effect=%s denied_by=%s", dec.Effect, dec.DeniedBy)
	}
}

// TestApproval_AprovacaoVerificadaDestrava é o caminho legítimo: com evidência VÁLIDA
// ligada a esta call, a mesma acção passa — sem que o taint mude.
func TestApproval_AprovacaoVerificadaDestrava(t *testing.T) {
	v := &fakeVerifier{accept: []byte("OK")}
	m := newApprovalMonitor(t, v)
	call := privCall([]byte("OK"))
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("com aprovação verificada devia PERMITIR; effect=%s denied_by=%s reason=%s", dec.Effect, dec.DeniedBy, dec.Reason)
	}
	// A preview que o verificador viu tem de ser a desta call (o binding).
	want := referencemonitor.ApprovalPreview(call)
	if string(v.seenPreview) != string(want) {
		t.Fatalf("o verificador devia receber a preview canónica DESTA call")
	}
}

// TestApproval_NaoMudaOTaint sela a propriedade central do desenho: destravar por
// aprovação NÃO reetiqueta a call como trusted. O registo continua a dizer a verdade —
// a acção FOI originada pelo modelo.
func TestApproval_NaoMudaOTaint(t *testing.T) {
	m := newApprovalMonitor(t, &fakeVerifier{accept: []byte("OK")})
	call := privCall([]byte("OK"))
	if _, err := m.Mediate(context.Background(), call); err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if call.Context.Taint != "untrusted" {
		t.Fatalf("o taint da call NÃO pode mudar por causa da aprovação; taint=%q", call.Context.Taint)
	}
}

// TestApproval_EvidenciaDeOutraAccaoNaoDestrava é o anti-relay: uma aprovação assinada
// para OUTRA acção não serve esta. O verificador exige a preview desta call.
func TestApproval_EvidenciaDeOutraAccaoNaoDestrava(t *testing.T) {
	outra := privCall(nil)
	outra.Resource.Value = "https://api.example.com/OUTRO-DESTINO"
	previewDeOutra := referencemonitor.ApprovalPreview(outra)

	// O verificador só aceita a preview da OUTRA acção.
	v := &fakeVerifier{accept: []byte("OK"), wantPreview: previewDeOutra}
	m := newApprovalMonitor(t, v)

	dec, err := m.Mediate(context.Background(), privCall([]byte("OK")))
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "taint" {
		t.Fatalf("uma aprovação de OUTRA acção não pode destravar esta; effect=%s denied_by=%s", dec.Effect, dec.DeniedBy)
	}
}

// TestApproval_InputAlteradoInvalida: mudar UM BYTE do input muda a preview — a
// aprovação deixa de valer (o humano aprovou aqueles argumentos, não outros).
func TestApproval_InputAlteradoInvalida(t *testing.T) {
	original := privCall([]byte("OK"))
	v := &fakeVerifier{accept: []byte("OK"), wantPreview: referencemonitor.ApprovalPreview(original)}
	m := newApprovalMonitor(t, v)

	adulterada := privCall([]byte("OK"))
	adulterada.Input = []byte(`{"body":"y"}`) // um byte diferente
	dec, err := m.Mediate(context.Background(), adulterada)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("input alterado devia invalidar a aprovação; effect=%s", dec.Effect)
	}
}

// TestApproval_VerificadorAusenteOuFalhaNaoAbre é o fail-closed: sem verificador ligado,
// ou com o verificador a falhar, nada é destravado. Uma avaria do subsistema de aprovação
// nunca ABRE — no máximo mantém fechado.
func TestApproval_VerificadorAusenteOuFalhaNaoAbre(t *testing.T) {
	t.Run("sem verificador", func(t *testing.T) {
		m := newApprovalMonitor(t, nil)
		dec, err := m.Mediate(context.Background(), privCall([]byte("OK")))
		if err != nil {
			t.Fatalf("Mediate: %v", err)
		}
		if dec.Effect != referencemonitor.EffectDeny {
			t.Fatalf("sem verificador nada destrava; effect=%s", dec.Effect)
		}
	})
	t.Run("verificador avariado", func(t *testing.T) {
		m := newApprovalMonitor(t, &fakeVerifier{accept: []byte("OK"), forceFailure: errors.New("subsistema em baixo")})
		dec, err := m.Mediate(context.Background(), privCall([]byte("OK")))
		if err != nil {
			t.Fatalf("Mediate: %v", err)
		}
		if dec.Effect != referencemonitor.EffectDeny {
			t.Fatalf("verificador avariado não pode abrir; effect=%s", dec.Effect)
		}
	})
}

// TestApproval_NaoPrivilegiadaNaoPrecisaDeAprovacao: o desenho não muda nada para
// capabilities não-privilegiadas — untrusted continua a ser dados legítimos.
func TestApproval_NaoPrivilegiadaNaoPrecisaDeAprovacao(t *testing.T) {
	m := newApprovalMonitor(t, &fakeVerifier{accept: []byte("OK")})
	call := privCall(nil)
	call.Capability = "cap:fs.read" // não está no conjunto privilegiado
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("capability não-privilegiada não devia exigir aprovação; effect=%s", dec.Effect)
	}
}

// TestApproval_ProvaNaoEForjavelDeFora é a guarda ESTRUTURAL. Este teste está num pacote
// EXTERNO (referencemonitor_test) — o mesmo lugar de onde um atacante-integrador
// escreveria código. A prova de aprovação vive num campo NÃO-EXPORTADO: não há literal,
// setter nem conversão que a coloque a partir daqui. A única via é a evidência passar
// pelo verificador. Este teste documenta e fixa essa fronteira.
func TestApproval_ProvaNaoEForjavelDeFora(t *testing.T) {
	// Só é possível construir a call com EVIDÊNCIA (bytes opacos) — nunca com a prova.
	call := privCall([]byte("evidencia-inventada"))
	if call.HumanApproval() != nil {
		t.Fatalf("uma call recém-construída NUNCA pode trazer prova de aprovação")
	}
	// Com um verificador que rejeita esta evidência, a call é negada — provando que
	// "trazer bytes" não é "estar aprovado".
	m := newApprovalMonitor(t, &fakeVerifier{accept: []byte("OK")})
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("evidência inventada não pode destravar; effect=%s", dec.Effect)
	}
	// NOTA ESTRUTURAL: a linha abaixo NÃO COMPILA a partir deste pacote, e é isso que
	// torna a prova não-forjável (é o mesmo mecanismo de Decision.permit):
	//
	//     call.humanApproved = &referencemonitor.ApprovalProof{...}
	//                ^ unknown field / cannot refer to unexported field
}
