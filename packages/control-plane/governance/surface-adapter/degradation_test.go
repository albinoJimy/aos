package surfaceadapter

import (
	"context"
	"testing"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// TestDegradacao_CanalSemDualControlRecusa (AC4): um card IRREVERSÍVEL numa superfície
// SEM suporte a dual-control (Telegram, no reference model) DEGRADA fail-closed —
// Degraded=true, a acção oferecida é encaminhar/recusar, NUNCA aprovar. Prova
// não-tautológica: os canais CAPAZES (desktop, slack) NÃO degradam o mesmo card.
func TestDegradacao_CanalSemDualControlRecusa(t *testing.T) {
	card := irreversibleCard(t)

	// Telegram: degrada.
	tg := renderOn(t, PlatformTelegram, card)
	if !tg.Degraded {
		t.Fatal("telegram (sem dual-control) devia DEGRADAR um card irreversivel")
	}
	if hasAction(tg, ActionApprove) {
		t.Fatal("render degradada NUNCA pode oferecer aprovar (aprovacao por omissao)")
	}
	if !hasAction(tg, ActionForward) {
		t.Fatal("render degradada devia oferecer encaminhar para um canal capaz")
	}
	if tg.DegradeReason == "" {
		t.Fatal("degradacao devia ter uma razao explicita")
	}
	// Nenhum botão da plataforma pode ser um "aprovar".
	if telegramHasApproveButton(tg) {
		t.Fatal("teclado telegram degradado nao pode ter botao de aprovar")
	}

	// Canais CAPAZES: NÃO degradam o mesmo card (a degradação depende da capacidade, não
	// é universal — não-tautológico).
	for _, p := range []Platform{PlatformDesktop, PlatformSlack} {
		rc := renderOn(t, p, card)
		if rc.Degraded {
			t.Fatalf("%s (capaz) NAO devia degradar: %s", p, rc.DegradeReason)
		}
		if !hasAction(rc, ActionApprove) {
			t.Fatalf("%s (capaz) devia oferecer aprovar", p)
		}
	}
}

// TestDegradacao_CaminhoDeDecisaoNaoAprova (AC4): o caminho de DECISÃO de uma render
// degradada NUNCA aprova por omissão — e NÃO chega sequer ao gate. Prova forte: usa um
// canal que APROVARIA tudo; mesmo assim a decisão do Telegram (degradado) é
// Authorized=false E o gate NÃO é chamado (spy.seen vazio).
func TestDegradacao_CaminhoDeDecisaoNaoAprova(t *testing.T) {
	// Canal que aprovaria qualquer coisa que lhe chegasse.
	spy := &spyChannel{resps: []risk.ConfirmationResponse{
		{Approved: true, Approver: "approver-a"},
		{Approved: true, Approver: "approver-b"},
	}}
	coll, err := approvalcard.NewDualControlCollector(spy)
	if err != nil {
		t.Fatalf("NewDualControlCollector: %v", err)
	}
	r, _ := RendererFor(PlatformTelegram)
	auth, err := NewSurfaceAuthorizer(r, coll)
	if err != nil {
		t.Fatalf("NewSurfaceAuthorizer: %v", err)
	}

	dec, rc, err := auth.Authorize(context.Background(), irreversibleCard(t))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !rc.Degraded {
		t.Fatal("render devia estar degradada")
	}
	if dec.Authorized {
		t.Fatalf("degradacao NUNCA autoriza por omissao: %s", dec.Reason)
	}
	if len(dec.Approvers) != 0 {
		t.Fatalf("degradacao nao devia ter aprovadores: %+v", dec.Approvers)
	}
	// A prova não-tautológica: o gate NEM SEQUER foi chamado (senão o spy teria aprovado).
	if len(spy.seen) != 0 {
		t.Fatalf("uma render degradada NAO devia chamar o gate: seen=%d", len(spy.seen))
	}
}

// telegramHasApproveButton indica se o teclado inline tem um botão cujo callback é uma
// acção de aprovar.
func telegramHasApproveButton(rc RenderedCard) bool {
	if rc.TelegramKeyboard == nil {
		return false
	}
	prefix := string(ActionApprove) + ":"
	for _, row := range rc.TelegramKeyboard.InlineButtons {
		for _, b := range row {
			if len(b.CallbackData) >= len(prefix) && b.CallbackData[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}
