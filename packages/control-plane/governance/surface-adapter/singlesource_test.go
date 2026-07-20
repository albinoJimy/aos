package surfaceadapter

import (
	"testing"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// TestFonteUnica_MudancaCanonicaPropaga (AC1): alterar um campo do MODELO CANÓNICO
// propaga-se aos TRÊS renderers, provando que derivam da MESMA struct — não há um
// modelo duplicado por plataforma. Renderiza o mesmo card nas três superfícies e assere
// que TODAS reflectem o valor; depois muda o campo canónico (Preview) e re-renderiza,
// asserindo que as três reflectem o NOVO valor.
func TestFonteUnica_MudancaCanonicaPropaga(t *testing.T) {
	build := func(preview string) approvalcard.ApprovalCard {
		req := risk.ConfirmationRequest{
			Class:        risk.ClassDanger,
			Irreversible: false,
			Preview:      preview,
			Principal:    "agent-1",
			Capability:   "cap:http.post",
			Resource:     "https://api/x",
		}
		card, err := approvalcard.BuildCard(req, approvalcard.WithRequestID("card-src"))
		if err != nil {
			t.Fatalf("BuildCard: %v", err)
		}
		return card
	}

	const previewA = "cap:http.post -> https://api/A"
	const previewB = "cap:http.post -> https://api/B-ALTERADO"

	// Estado A: os três renderers reflectem o preview A.
	cardA := build(previewA)
	for _, p := range allPlatforms() {
		rc := renderOn(t, p, cardA)
		if rc.Preview != previewA || !platformBlockContains(t, rc, previewA) {
			t.Fatalf("%s: nao reflecte o preview canonico A", p)
		}
		if platformBlockContains(t, rc, previewB) {
			t.Fatalf("%s: reflecte um preview que o card ainda nao tem (modelo duplicado?)", p)
		}
	}

	// Estado B: MUDA o campo canónico. Os MESMOS três renderers reflectem o novo valor —
	// sem qualquer alteração nos renderers (a única fonte é o card).
	cardB := build(previewB)
	for _, p := range allPlatforms() {
		rc := renderOn(t, p, cardB)
		if rc.Preview != previewB || !platformBlockContains(t, rc, previewB) {
			t.Fatalf("%s: mudanca do campo canonico NAO se propagou", p)
		}
		if platformBlockContains(t, rc, previewA) {
			t.Fatalf("%s: ainda reflecte o preview antigo (fonte duplicada)", p)
		}
	}
}

// TestFonteUnica_IrreversibilidadePropaga (AC1): mudar a IRREVERSIBILIDADE canónica
// propaga o indicador de dual-control aos renderers dos canais capazes — a semântica de
// aprovação deriva do card, não de um modelo por plataforma.
func TestFonteUnica_IrreversibilidadePropaga(t *testing.T) {
	// Reversível: dual-control não exigido em nenhuma superfície.
	rev := reversibleCard(t)
	for _, p := range allPlatforms() {
		rc := renderOn(t, p, rev)
		if rc.DualControlRequired {
			t.Fatalf("%s: reversivel nao devia exigir dual-control", p)
		}
	}
	// Irreversível: dual-control exigido; os canais capazes reflectem-no.
	irr := irreversibleCard(t)
	for _, p := range []Platform{PlatformDesktop, PlatformSlack} {
		rc := renderOn(t, p, irr)
		if !rc.DualControlRequired {
			t.Fatalf("%s: irreversivel devia exigir dual-control (propagado do card)", p)
		}
	}
}
