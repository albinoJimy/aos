package uxdx_test

import (
	"testing"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	surfaceadapter "github.com/aos-ref/control-plane/governance/surface-adapter"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// AC3 — PARIDADE DE SUPERFÍCIE (AOS-122). A MESMA [approvalcard.ApprovalCard] renderiza
// cards EQUIVALENTES nas três plataformas; um canal sem dual-control (Telegram) com um
// card irreversível DEGRADA fail-closed. COMPÕE os renderers de AOS-122 — não os
// reimplementa.

var allPlatforms = []surfaceadapter.Platform{
	surfaceadapter.PlatformSlack,
	surfaceadapter.PlatformTelegram,
	surfaceadapter.PlatformDesktop,
}

// A MESMA fonte canónica produz cards equivalentes (mesmo Preview + mesmo indicador de
// dual-control) nas 3 plataformas — a paridade funcional que evita divergência de
// semântica entre canais.
func TestParity_EquivalentCardsAcrossPlatforms(t *testing.T) {
	t.Parallel()

	// Card REVERSÍVEL (gray): representável em TODAS as plataformas (nenhuma degrada) —
	// o caso onde a paridade tem de ser total.
	card, err := approvalcard.BuildCard(
		risk.ConfirmationRequest{
			Class:      risk.ClassGray,
			Preview:    "cap:http.get -> https://api/synthetic",
			Principal:  requesterID,
			Capability: "cap:http.get",
			Resource:   "https://api/synthetic",
		},
		approvalcard.WithRequestID("parity-card-1"),
	)
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}

	rendered := make([]surfaceadapter.RenderedCard, 0, len(allPlatforms))
	for _, p := range allPlatforms {
		r, err := surfaceadapter.RendererFor(p)
		if err != nil {
			t.Fatalf("RendererFor(%s): %v", p, err)
		}
		rc, err := r.Render(card)
		if err != nil {
			t.Fatalf("Render(%s): %v", p, err)
		}
		rendered = append(rendered, rc)
	}

	// Os ESSENCIAIS canónicos são idênticos em todas as plataformas (derivam da MESMA
	// ApprovalCard — fonte única).
	base := rendered[0]
	for i, rc := range rendered {
		if rc.Preview != base.Preview {
			t.Fatalf("plataforma %s: Preview divergente (%q != %q)", rc.Platform, rc.Preview, base.Preview)
		}
		if rc.Preview == "" {
			t.Fatalf("plataforma %s: Preview vazio (efeito concreto ausente)", rc.Platform)
		}
		if rc.DualControlRequired != base.DualControlRequired {
			t.Fatalf("plataforma %s: dual-control divergente (%v != %v)", rc.Platform, rc.DualControlRequired, base.DualControlRequired)
		}
		if rc.Irreversible != base.Irreversible {
			t.Fatalf("plataforma %s: irreversibilidade divergente", rc.Platform)
		}
		if rc.RequestID != card.RequestID || rc.Requester != card.Requester {
			t.Fatalf("plataforma %s: identificadores divergentes do card", rc.Platform)
		}
		// Card reversível → NENHUMA plataforma degrada e todas oferecem aprovar.
		if rc.Degraded {
			t.Fatalf("plataforma %s: card reversível não devia degradar", rc.Platform)
		}
		if !hasAction(rc, surfaceadapter.ActionApprove) {
			t.Fatalf("plataforma %s: um card representável devia oferecer ActionApprove", rc.Platform)
		}
		// O bloco específico da plataforma correcta está populado (paridade estrutural).
		assertPlatformBlock(t, rc, i)
	}
}

// Um card IRREVERSÍVEL num canal SEM dual-control (Telegram) DEGRADA fail-closed:
// Degraded=true e NUNCA ActionApprove — encaminha para um canal capaz.
func TestParity_IrreversibleDegradesFailClosedOnTelegram(t *testing.T) {
	t.Parallel()

	card, err := approvalcard.BuildCard(
		dangerReq("cap:fs.delete -> /data/synthetic"),
		approvalcard.WithRequestID("parity-irrev-1"),
	)
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}
	if !card.DualControlRequired {
		t.Fatal("pré-condição: o card irreversível tem de exigir dual-control")
	}

	tr, err := surfaceadapter.RendererFor(surfaceadapter.PlatformTelegram)
	if err != nil {
		t.Fatalf("RendererFor(telegram): %v", err)
	}
	rc, err := tr.Render(card)
	if err != nil {
		t.Fatalf("Render(telegram): %v", err)
	}
	if !rc.Degraded {
		t.Fatal("FAIL-CLOSED violado: Telegram (sem dual-control) não degradou um card irreversível")
	}
	if hasAction(rc, surfaceadapter.ActionApprove) {
		t.Fatal("FAIL-CLOSED violado: uma render degradada NUNCA pode oferecer ActionApprove")
	}
	if !hasAction(rc, surfaceadapter.ActionForward) {
		t.Fatal("uma render degradada devia oferecer encaminhar para um canal capaz")
	}
	if rc.DegradeReason == "" {
		t.Fatal("a degradação devia explicar o motivo (transparência)")
	}

	// Contraprova de paridade: os canais COM dual-control (Slack/desktop) representam o
	// MESMO card irreversível sem degradar — a degradação é do CANAL, não do card.
	for _, p := range []surfaceadapter.Platform{surfaceadapter.PlatformSlack, surfaceadapter.PlatformDesktop} {
		r, _ := surfaceadapter.RendererFor(p)
		capable, err := r.Render(card)
		if err != nil {
			t.Fatalf("Render(%s): %v", p, err)
		}
		if capable.Degraded {
			t.Fatalf("plataforma %s suporta dual-control e NÃO devia degradar", p)
		}
		if !capable.DualControlRequired {
			t.Fatalf("plataforma %s: o indicador de dual-control tem de ser preservado", p)
		}
	}
}

// hasAction indica se a render oferece a acção lógica dada.
func hasAction(rc surfaceadapter.RenderedCard, kind surfaceadapter.ActionKind) bool {
	for _, a := range rc.Actions {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

// assertPlatformBlock verifica que o bloco específico da plataforma correcta está
// populado (paridade estrutural: cada plataforma tem a sua renderização derivada).
func assertPlatformBlock(t *testing.T, rc surfaceadapter.RenderedCard, i int) {
	t.Helper()
	switch rc.Platform {
	case surfaceadapter.PlatformSlack:
		if len(rc.SlackBlocks) == 0 {
			t.Fatalf("render Slack[%d] sem blocos", i)
		}
	case surfaceadapter.PlatformTelegram:
		if rc.TelegramKeyboard == nil {
			t.Fatalf("render Telegram[%d] sem teclado", i)
		}
	case surfaceadapter.PlatformDesktop:
		if rc.DesktopComponent == nil {
			t.Fatalf("render Desktop[%d] sem componente", i)
		}
	default:
		t.Fatalf("plataforma desconhecida na render[%d]: %s", i, rc.Platform)
	}
}
