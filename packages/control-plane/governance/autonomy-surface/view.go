package autonomysurface

import (
	"context"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
)

// LevelView é a VISTA legível do estado de autonomia de um par (agente, domínio)
// (AC1/AC2/AC3): o nível corrente, o nível-alvo da próxima promoção, o progresso rumo
// a ele e as transições passadas com o seu motivo. É PURAMENTE DERIVADA de AOS-089/090
// — a superfície LÊ e APRESENTA, nunca decide.
type LevelView struct {
	// Agent e Domain identificam o par (agente, domínio) desta vista.
	Agent  string
	Domain string
	// Current é o nível CORRENTE, LIDO de [LevelReader.LevelFor] (== AOS-089, AC1).
	Current autonomy.Level
	// NextLevel é o nível-alvo da próxima promoção (Current+1). No tecto de promoção
	// (ou em L5) é igual a Current — não há próximo nível.
	NextLevel autonomy.Level
	// Progress é o progresso rumo a [NextLevel] derivado do sinal de fiabilidade de
	// AOS-090 (AC2). No tecto, descreve a ausência de próxima promoção.
	Progress ProgressToPromotion
	// Transitions são as transições passadas do par (promoção/demoção) COM o seu
	// motivo, LIDAS de [LevelReader.HistoryFor] por ordem de aplicação (AC3). A
	// superfície EXPLICA cada decisão que AOS-090 tomou — sem a tomar.
	Transitions []TransitionView
}

// TransitionView é uma transição passada apresentada COM o seu motivo/métrica (AC3):
// a superfície EXPLICA a decisão de AOS-090, nunca a decide. É LIDA de uma
// [autonomy.LevelChange] do histórico — só leitura.
type TransitionView struct {
	// From e To são o nível ANTERIOR e o RESULTANTE da transição.
	From autonomy.Level
	To   autonomy.Level
	// Reason é o motivo/métrica declarado da transição (o que AOS-090 selou). É a
	// EXPLICAÇÃO apresentada ao utilizador — nunca um segredo.
	Reason string
	// Actor é o principal atribuído à transição (o Controller nas automáticas).
	Actor string
	// At é o instante (UTC) da transição.
	At time.Time
}

// IsDemotion indica se esta transição apresentada foi uma DEMOÇÃO (To < From) — para a
// superfície a destacar como rebaixamento (AC5).
func (t TransitionView) IsDemotion() bool { return t.To < t.From }

// BuildLevelView constrói a [LevelView] de um par LENDO o nível corrente
// ([LevelReader.LevelFor], AC1), o progresso a partir do sinal de fiabilidade (AC2) e
// as transições do histórico com o seu motivo ([LevelReader.HistoryFor], AC3). Emite
// um span de interacção de vista (DoD). NUNCA chama SetLevel nem decide o nível — é uma
// projecção de leitura pura sobre AOS-089/090.
func (s *Surface) BuildLevelView(ctx context.Context, agent, domain string) LevelView {
	current := s.reader.LevelFor(agent, domain)
	ceil := s.cfg.PromotionCeil()

	next := current
	if current < ceil {
		next = current + 1
	}

	v := LevelView{
		Agent:       agent,
		Domain:      domain,
		Current:     current,
		NextLevel:   next,
		Progress:    s.computeProgress(agent, domain, current, ceil),
		Transitions: buildTransitions(s.reader.HistoryFor(agent, domain)),
	}
	s.emitInteraction(ctx, agent, domain, current, SurfaceKindView, "")
	return v
}

// buildTransitions projecta o histórico de [autonomy.LevelChange] em [TransitionView],
// preservando a ordem de aplicação. Leitura pura: copia From/To/Reason/Actor/At de cada
// elo — a superfície não altera nem reinterpreta o facto selado.
func buildTransitions(history []autonomy.LevelChange) []TransitionView {
	out := make([]TransitionView, 0, len(history))
	for _, ch := range history {
		out = append(out, TransitionView{
			From:   ch.Old,
			To:     ch.New,
			Reason: ch.Reason,
			Actor:  ch.Actor,
			At:     ch.At,
		})
	}
	return out
}
