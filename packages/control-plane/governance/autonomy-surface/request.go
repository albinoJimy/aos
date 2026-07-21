package autonomysurface

import (
	"context"

	"github.com/aos-ref/control-plane/governance/autonomy"
)

// RequestMoreAutonomy é o fluxo pelo qual o utilizador SOLICITA revisão de nível
// (AC4). A superfície NÃO decide: DELEGA o pedido ao [LevelReviewer] (o adaptador sobre
// o Controller.Evaluate de AOS-090), que é quem DECIDE — promove só por fiabilidade
// sustentada, ou mantém. Devolve exactamente a [autonomy.LevelChange] e o `changed` que
// a POLÍTICA produziu: se negado (changed=false) o nível fica INALTERADO, porque a
// superfície nunca chama SetLevel. Emite um span de interacção do pedido (DoD).
//
// Fail-closed: sem [LevelReviewer] configurado não há a quem delegar — devolve
// [ErrNoReviewer] e NUNCA se auto-promove.
func (s *Surface) RequestMoreAutonomy(ctx context.Context, agent, domain string) (autonomy.LevelChange, bool, error) {
	if s.reviewer == nil {
		return autonomy.LevelChange{}, false, ErrNoReviewer
	}
	current := s.reader.LevelFor(agent, domain)
	s.emitInteraction(ctx, agent, domain, current, SurfaceKindRequest, "")

	// DELEGA a decisão à política (AOS-090). A superfície apenas encaminha e devolve o
	// veredicto — não muta o registo em caso algum.
	return s.reviewer.RequestReview(ctx, agent, domain)
}

// Eligible expõe, de forma PROGRESSIVA por maturidade (AC4), se um par deve VER a opção
// de solicitar mais autonomia: só quando a janela tem cobertura ([ProgressToPromotion.WindowOK])
// E a fiabilidade medida cumpre o critério headline (Fraction >= 1.0) E ainda há próximo
// nível (Current < NextLevel). É apenas a decisão de APRESENTAR a opção — a promoção em
// si é sempre decidida pela política ao delegar via [Surface.RequestMoreAutonomy].
func Eligible(v LevelView) bool {
	return v.Current < v.NextLevel && v.Progress.WindowOK && v.Progress.Fraction >= 1.0
}
