package surfaceadapter

import (
	"strings"
	"testing"
)

// TestParidade_TresSuperficiesEquivalentes (AC1/AC2): a MESMA [approvalcard.ApprovalCard]
// renderiza em Slack/Telegram/Desktop cards EQUIVALENTES — mesmo Preview, mesma classe,
// mesmo indicador de dual-control, mesmo RequestID/Requester. Num canal CAPAZ nenhuma
// superfície degrada a semântica de aprovação. Usa um card REVERSÍVEL (dual-control não
// exigido) — capaz em todas as três superfícies.
func TestParidade_TresSuperficiesEquivalentes(t *testing.T) {
	card := reversibleCard(t)

	var first *RenderedCard
	for _, p := range allPlatforms() {
		rc := renderOn(t, p, card)

		// Nenhuma superfície degrada num canal capaz (reversível não exige dual-control).
		if rc.Degraded {
			t.Fatalf("%s: card reversivel NAO devia degradar num canal capaz: %s", p, rc.DegradeReason)
		}
		// Os essenciais canónicos são LIDOS do card (fonte única).
		if rc.Preview != card.Preview {
			t.Fatalf("%s: preview divergente: %q != %q", p, rc.Preview, card.Preview)
		}
		if rc.RequestID != card.RequestID || rc.Requester != card.Requester {
			t.Fatalf("%s: request-id/requester divergentes", p)
		}
		if rc.Class != card.Class || rc.Irreversible != card.Irreversible || rc.DualControlRequired != card.DualControlRequired {
			t.Fatalf("%s: classe/irreversivel/dual-control divergentes", p)
		}
		// A acção de aprovar existe (canal capaz); a de encaminhar não.
		if !hasAction(rc, ActionApprove) || hasAction(rc, ActionForward) {
			t.Fatalf("%s: canal capaz devia oferecer aprovar e NAO encaminhar: %+v", p, rc.Actions)
		}
		// O PREVIEW é preservado nos blocos ESPECÍFICOS da plataforma (AC2).
		if !platformBlockContains(t, rc, card.Preview) {
			t.Fatalf("%s: bloco especifico da plataforma nao preserva o preview", p)
		}

		// Equivalência entre superfícies: os essenciais coincidem par-a-par.
		if first == nil {
			c := rc
			first = &c
			continue
		}
		if rc.Preview != first.Preview || rc.Class != first.Class ||
			rc.Irreversible != first.Irreversible || rc.DualControlRequired != first.DualControlRequired ||
			rc.RequestID != first.RequestID || rc.Requester != first.Requester {
			t.Fatalf("%s: essenciais divergem de outra superficie (nao equivalentes)", p)
		}
	}
}

// TestParidade_DualControlPreservadoNosCanaisCapazes (AC2): um card IRREVERSÍVEL preserva
// o indicador de dual-control em TODAS as superfícies CAPAZES (desktop, slack) — nenhuma
// delas degrada a semântica. (Telegram, sem dual-control, é o caso de degradação — ver
// TestDegradacao*.)
func TestParidade_DualControlPreservadoNosCanaisCapazes(t *testing.T) {
	card := irreversibleCard(t)
	for _, p := range []Platform{PlatformDesktop, PlatformSlack} {
		rc := renderOn(t, p, card)
		if rc.Degraded {
			t.Fatalf("%s: canal capaz NAO devia degradar um card irreversivel: %s", p, rc.DegradeReason)
		}
		if !rc.DualControlRequired {
			t.Fatalf("%s: indicador de dual-control perdido", p)
		}
		// A nota de dual-control é visível ao aprovador no bloco da plataforma.
		if !platformBlockContains(t, rc, "dois aprovadores") {
			t.Fatalf("%s: bloco nao torna o dual-control visivel", p)
		}
		if !hasAction(rc, ActionApprove) {
			t.Fatalf("%s: canal capaz devia oferecer aprovar", p)
		}
	}
}

// platformBlockContains verifica se o texto renderizado ESPECÍFICO da plataforma contém
// a substring dada (o preview ou a nota de dual-control).
func platformBlockContains(t *testing.T, rc RenderedCard, sub string) bool {
	t.Helper()
	switch rc.Platform {
	case PlatformSlack:
		for _, b := range rc.SlackBlocks {
			if strings.Contains(b.Text, sub) {
				return true
			}
		}
	case PlatformTelegram:
		if rc.TelegramKeyboard != nil && strings.Contains(rc.TelegramKeyboard.Text, sub) {
			return true
		}
	case PlatformDesktop:
		if rc.DesktopComponent != nil && strings.Contains(rc.DesktopComponent.Body, sub) {
			return true
		}
	}
	return false
}
