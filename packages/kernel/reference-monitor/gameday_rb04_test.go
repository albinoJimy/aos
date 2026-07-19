package referencemonitor

// gameday_rb04_test.go — GAME DAY do RB-04 (falha de PDP, ADR-011).
//
// Injecta o MODO DE FALHA REAL — o PDP indisponível (o hook de política devolve erro)
// e, depois, política corrompida (deny) — e prova a MITIGAÇÃO-CHAVE sobre o Reference
// Monitor REAL (AOS-003): o RM é FAIL-CLOSED — sem decisão de política válida, a tool
// call NÃO executa (Decision Deny), NUNCA fail-open. A indisponibilidade DEGRADA a
// DISPONIBILIDADE (calls legítimas negadas), NUNCA a SEGURANÇA. Depois RECUPERA:
// encaminhado para a réplica de PDP com política assinada, o RM volta a PERMITIR — a
// segurança nunca esteve comprometida.
//
// Sem stubs do RM: usa o [Monitor] real; só o PDP é um hook de teste cuja saúde se
// injecta (é exactamente o seam que produção liga ao PDP real de AOS-004).

import (
	"context"
	"errors"
	"testing"
)

// fakePDP é o hook de política (PDP) cuja disponibilidade/integridade se injecta — o
// seam real da cadeia de mediação. Indisponível ⇒ erro (fail-closed no RM); política
// corrompida ⇒ deny; saudável ⇒ allow com a versão assinada.
type fakePDP struct {
	available bool
	corrupted bool
	version   string
}

func (fakePDP) Name() string { return "policy" }

func (p *fakePDP) Evaluate(_ context.Context, _ *Call) (HookResult, error) {
	if !p.available {
		// PDP indisponível: latência de decisão estourou / sem resposta. O RM tem de
		// tratar isto como fail-closed (nunca deixar passar sem decisão).
		return HookResult{}, errors.New("pdp indisponível: sem decisão de política")
	}
	if p.corrupted {
		return HookResult{Decision: HookDeny, Reason: "política corrompida (assinatura inválida)"}, nil
	}
	return HookResult{Decision: HookAllow, PolicyVersion: p.version}, nil
}

// TestGameDay_RB04_PDPUnavailableFailsClosed prova sinal→mitigação→recuperação do RB-04.
func TestGameDay_RB04_PDPUnavailableFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	pdp := &fakePDP{available: false} // MODO DE FALHA: PDP em baixo.
	sink := &fakeSink{}
	var called bool
	m := New(WithHooks(pdp), WithEventSink(sink))
	if err := m.Register("tool.echo", toolSpy(&called, []byte("efeito"))); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// SINAL + MITIGAÇÃO: com o PDP indisponível, o RM NEGA fail-closed. A tool NÃO executa.
	d, err := m.Mediate(ctx, baseCall())
	if err != nil {
		t.Fatalf("Mediate (PDP em baixo): erro inesperado %v", err)
	}
	if d.Permitted() {
		t.Fatal("PDP indisponível NÃO pode resultar em permit (fail-open é proibido)")
	}
	if d.Effect != EffectDeny {
		t.Fatalf("Effect = %q, quero deny (fail-closed)", d.Effect)
	}
	if d.Code != CodeHookError {
		t.Fatalf("Code = %q, quero %q (erro do hook de política ⇒ fail-closed)", d.Code, CodeHookError)
	}
	if called {
		t.Fatal("a tool foi despachada sem decisão de política — VIOLAÇÃO de segurança (fail-open)")
	}

	// A SEGURANÇA nunca degrada: mesmo com política CORROMPIDA (não só indisponível), o
	// veredicto é deny — não se serve uma decisão inválida.
	pdp.available = true
	pdp.corrupted = true
	called = false
	d, err = m.Mediate(ctx, baseCall())
	if err != nil {
		t.Fatalf("Mediate (política corrompida): %v", err)
	}
	if d.Permitted() || d.Effect != EffectDeny {
		t.Fatalf("política corrompida devia negar; Effect=%q", d.Effect)
	}
	if d.Code != CodeDeniedByHook {
		t.Fatalf("Code = %q, quero %q", d.Code, CodeDeniedByHook)
	}
	if called {
		t.Fatal("tool despachada com política corrompida")
	}

	// RECUPERAÇÃO: encaminhado para a réplica de PDP com POLÍTICA ASSINADA (versão
	// revertida para a última assinada). A disponibilidade recupera; a tool legítima
	// volta a ser PERMITIDA e despachada — prova de que a segurança nunca esteve em risco.
	pdp.corrupted = false
	pdp.version = "1.4.2"
	called = false
	d, err = m.Mediate(ctx, baseCall())
	if err != nil {
		t.Fatalf("Mediate (PDP recuperado): %v", err)
	}
	if !d.Permitted() {
		t.Fatalf("após recuperar o PDP a call legítima devia ser permitida; Effect=%q Code=%q", d.Effect, d.Code)
	}
	if !called {
		t.Fatal("a tool devia ter sido despachada após a recuperação do PDP")
	}

	// Observabilidade: 2 negações (indisponível + corrompida) e 1 permit — a
	// indisponibilidade degradou disponibilidade (denials), nunca a segurança.
	permits, denials, _ := m.Metrics().Snapshot()
	if permits != 1 || denials != 2 {
		t.Fatalf("métricas = permits %d / denials %d, quero 1 / 2", permits, denials)
	}
}
