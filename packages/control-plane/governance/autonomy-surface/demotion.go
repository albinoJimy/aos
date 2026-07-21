package autonomysurface

import (
	"context"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
)

// DemotionNotice é o aviso IMEDIATO e claro de um rebaixamento automático (AC5): NUNCA
// esconde a demoção — expõe de que nível para que nível se desceu e o MOTIVO/métrica que
// AOS-090 selou. É apresentação de uma decisão já tomada pela política; a superfície não
// a decide nem a suaviza.
type DemotionNotice struct {
	// Agent e Domain identificam o par rebaixado.
	Agent  string
	Domain string
	// From é o nível ANTERIOR (mais autónomo) e To o RESULTANTE (mais supervisionado).
	From autonomy.Level
	To   autonomy.Level
	// Reason é o motivo/anomalia que justificou a demoção (o que o Controller selou).
	Reason string
	// At é o instante (UTC) da demoção.
	At time.Time
}

// IsDemotion indica se uma [autonomy.LevelChange] é uma DEMOÇÃO — o novo nível é MAIS
// SUPERVISIONADO que o anterior (New < Old). Detecção pura, sem efeitos.
func IsDemotion(ch autonomy.LevelChange) bool { return ch.New < ch.Old }

// NotifyLevelChange reage a uma transição de nível selada por AOS-090 (evento
// autonomy.level_changed) e, se for uma DEMOÇÃO (To < From), produz uma [DemotionNotice]
// IMEDIATA com o seu motivo (AC5) — a superfície comunica o rebaixamento sem o esconder.
// Devolve (notice, true) numa demoção; (zero, false) numa promoção ou não-alteração (o
// caminho de promoção é apresentado pela [Surface.BuildLevelView], não aqui). Emite um
// span de interacção da demoção (DoD). A superfície apenas EXPLICA a decisão — nunca a
// toma nem chama SetLevel.
func (s *Surface) NotifyLevelChange(ctx context.Context, ch autonomy.LevelChange) (DemotionNotice, bool) {
	if !IsDemotion(ch) {
		return DemotionNotice{}, false
	}
	notice := DemotionNotice{
		Agent:  ch.Agent,
		Domain: ch.Domain,
		From:   ch.Old,
		To:     ch.New,
		Reason: ch.Reason,
		At:     ch.At,
	}
	s.emitInteraction(ctx, ch.Agent, ch.Domain, ch.New, SurfaceKindDemotion, ch.Reason)
	return notice, true
}
