package autonomysurface

import (
	"context"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
)

// As PORTAS da superfície de autonomia (AOS-125). Fixam a fronteira entre esta
// camada de APRESENTAÇÃO e o registo/controlador de AOS-089/090: a superfície LÊ o
// nível/histórico (LevelReader) e o sinal de progresso (ReliabilityReader), e DELEGA
// o pedido de revisão à decisão da política (LevelReviewer). A superfície NUNCA
// decide o nível — não há porta de escrita aqui (nada de SetLevel).

// LevelReader é a PORTA de LEITURA do nível corrente e do histórico de transições de
// um par (agente, domínio). É satisfeita pelo *[autonomy.LevelRegistry] de AOS-089
// (LevelFor/Get/HistoryFor) — a superfície aceita-a por inversão e nunca conhece o
// armazenamento por trás. Note-se a AUSÊNCIA deliberada de qualquer método de
// MUTAÇÃO: a superfície só LÊ, jamais fixa o nível (isso é do Controller de AOS-090).
type LevelReader interface {
	// LevelFor devolve o nível corrente do par, FAIL-CLOSED em L0 se não houver
	// registo (contrato de [autonomy.Oracle]).
	LevelFor(agent, domain string) autonomy.Level
	// Get devolve o nível registado e se HAVIA registo (distingue "explicitamente L0"
	// de "sem nível").
	Get(agent, domain string) (autonomy.Level, bool)
	// HistoryFor devolve as transições do par, por ordem de aplicação, cada uma COM o
	// seu motivo/actor — a matéria-prima das [TransitionView] (AC3).
	HistoryFor(agent, domain string) []autonomy.LevelChange
}

// ReliabilityReader é a PORTA do sinal de progresso: a fiabilidade sustentada de um
// par sobre a janela deslizante da política. É satisfeita pela
// [autonomy.ReliabilitySource] de AOS-090 — a MESMA fonte que o Controller consulta
// para decidir a promoção, pelo que o progresso apresentado (AC2) reflecte fielmente
// a métrica em que a decisão assenta. Leitura pura: a superfície não a altera.
type ReliabilityReader interface {
	Reliability(agent, domain string, window time.Duration) autonomy.Reliability
}

// LevelReviewer é a PORTA de DECISÃO: a superfície DELEGA-lhe o pedido de revisão de
// nível (AC4) e o adaptador do wiring encaminha-o para o [autonomy.Controller].Evaluate
// de AOS-090 — que é quem DECIDE (promove só por fiabilidade sustentada, ou mantém).
// A superfície PEDE; a política DECIDE. RequestReview devolve a [autonomy.LevelChange]
// resultante e changed=true SÓ se a política promoveu — a superfície respeita a
// decisão e nunca fixa o nível por si.
type LevelReviewer interface {
	RequestReview(ctx context.Context, agent, domain string) (autonomy.LevelChange, bool, error)
}
