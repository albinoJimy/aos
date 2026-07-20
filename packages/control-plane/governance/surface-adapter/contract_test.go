package surfaceadapter

import (
	"context"
	"errors"
	"testing"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// TestContrato_MajorIncompativelRejeitado (AC5): o adaptador CONSOME o contrato
// VERSIONADO de AOS-120. Um card que carimba um MAJOR incompatível com
// [approvalcard.CurrentVersion] é REJEITADO na render (fail-closed) em todas as
// superfícies — nunca renderizado silenciosamente.
func TestContrato_MajorIncompativelRejeitado(t *testing.T) {
	// Card com MAJOR incompatível (construído directamente para forçar a versão).
	incompat := approvalcard.ApprovalCard{
		SchemaVersion:       approvalcard.CardSchemaVersion{Major: approvalcard.CurrentVersion.Major + 1},
		RequestID:           "card-x",
		Requester:           "agent-1",
		Class:               risk.ClassDanger,
		Irreversible:        false,
		DualControlRequired: false,
		Preview:             "cap:x -> y",
	}
	for _, p := range allPlatforms() {
		r, _ := RendererFor(p)
		if _, err := r.Render(incompat); !errors.Is(err, ErrIncompatibleContract) {
			t.Fatalf("%s: esperava ErrIncompatibleContract, obtive %v", p, err)
		}
	}
}

// TestContrato_CompativelDentroDoMesmoMajor (AC5): uma diferença de MINOR/PATCH dentro
// do mesmo MAJOR é retrocompatível — a render prossegue.
func TestContrato_CompativelDentroDoMesmoMajor(t *testing.T) {
	req := risk.ConfirmationRequest{
		Class: risk.ClassGray, Irreversible: false, Preview: "cap:x -> y",
		Principal: "agent-1", Capability: "cap:x", Resource: "y",
	}
	card, err := approvalcard.BuildCard(req, approvalcard.WithRequestID("card-y"))
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}
	// Simula um card de MINOR superior dentro do mesmo MAJOR.
	card.SchemaVersion.Minor = approvalcard.CurrentVersion.Minor + 1
	rc := renderOn(t, PlatformSlack, card)
	if rc.Degraded {
		t.Fatal("card compativel (mesmo MAJOR) nao devia degradar por versao")
	}
}

// TestContrato_MapeamentoDeCanal (AC5): a plataforma mapeia para o
// [controlsurface.ChannelID] de AOS-119 — slack/telegram → chatbot, desktop → desktop.
func TestContrato_MapeamentoDeCanal(t *testing.T) {
	cases := map[Platform]controlsurface.ChannelID{
		PlatformSlack:    controlsurface.ChannelChatbot,
		PlatformTelegram: controlsurface.ChannelChatbot,
		PlatformDesktop:  controlsurface.ChannelDesktop,
	}
	for p, want := range cases {
		if got := p.ChannelID(); got != want {
			t.Fatalf("%s: ChannelID = %q, esperava %q", p, got, want)
		}
		// A render carrega o mesmo canal.
		rc := renderOn(t, p, reversibleCard(t))
		if rc.ChannelID != want {
			t.Fatalf("%s: RenderedCard.ChannelID = %q, esperava %q", p, rc.ChannelID, want)
		}
	}
}

// TestConstrutor_FailClosed: renderer/coletor nil ou plataforma desconhecida são
// fail-closed.
func TestConstrutor_FailClosed(t *testing.T) {
	if _, err := RendererFor(Platform("mastodon")); !errors.Is(err, ErrUnknownPlatform) {
		t.Fatalf("plataforma desconhecida devia ser ErrUnknownPlatform, obtive %v", err)
	}
	r, _ := RendererFor(PlatformSlack)
	if _, err := NewSurfaceAuthorizer(nil, nil); !errors.Is(err, ErrNilDependency) {
		t.Fatalf("renderer nil devia ser ErrNilDependency, obtive %v", err)
	}
	if _, err := NewSurfaceAuthorizer(r, nil); !errors.Is(err, ErrNilDependency) {
		t.Fatalf("coletor nil devia ser ErrNilDependency, obtive %v", err)
	}
}

// TestContrato_RenderErroPropagaNaAutorizacao (AC5): um card de contrato incompatível
// falha a autorização (sem tocar no gate).
func TestContrato_RenderErroPropagaNaAutorizacao(t *testing.T) {
	spy := &spyChannel{resps: []risk.ConfirmationResponse{{Approved: true, Approver: "x"}}}
	coll, _ := approvalcard.NewDualControlCollector(spy)
	r, _ := RendererFor(PlatformDesktop)
	auth, _ := NewSurfaceAuthorizer(r, coll)

	incompat := approvalcard.ApprovalCard{
		SchemaVersion: approvalcard.CardSchemaVersion{Major: approvalcard.CurrentVersion.Major + 2},
		RequestID:     "c", Requester: "agent-1", Class: risk.ClassDanger,
	}
	dec, _, err := auth.Authorize(context.Background(), incompat)
	if !errors.Is(err, ErrIncompatibleContract) {
		t.Fatalf("esperava ErrIncompatibleContract, obtive %v", err)
	}
	if dec.Authorized {
		t.Fatal("card incompativel nunca autoriza")
	}
	if len(spy.seen) != 0 {
		t.Fatal("card incompativel nao devia tocar no gate")
	}
}
