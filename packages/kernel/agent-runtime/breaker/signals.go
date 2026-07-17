package breaker

import (
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Signal identifica um dos sinais INDEPENDENTES monitorizados no agente vivo. É um
// rótulo legível gravado no span de trip e na razão da transição durável (nunca um
// segredo).
type Signal string

const (
	// SignalCostVelocity — custo por segundo de wall-clock acima do limiar (o disjuntor
	// de custo partilhado com o orçamento por árvore, ADR-008). Leva a [state.Paused].
	SignalCostVelocity Signal = "cost_velocity"
	// SignalTokenVelocity — tokens por segundo de wall-clock acima do limiar. Leva a
	// [state.Paused].
	SignalTokenVelocity Signal = "token_velocity"
	// SignalWallClock — tempo ABSOLUTO desde a entrada em running acima do limiar. Leva
	// ao estado durável [state.TimedOut] (a spec fixa esta associação).
	SignalWallClock Signal = "wall_clock"
	// SignalNoProgress — iterações estéreis consecutivas (sem novo estado útil) acima do
	// limiar. O sinal concreto (action-dedup por hash) é AOS-081; aqui a porta é
	// [ProgressSource]. Leva a [state.Paused].
	SignalNoProgress Signal = "no_progress"
)

// SignalSnapshot é a FOTOGRAFIA dos sinais num instante — a ENTRADA do avaliador puro
// [Evaluate]. É produzida pelos colectores ([Breaker.Snapshot]) e é deliberadamente um
// conjunto de escalares (não portas): assim o avaliador é determinista e testável em
// isolamento, sem I/O nem relógio.
type SignalSnapshot struct {
	// CostMicroUSDPerSecond é a cost velocity (micro-USD por segundo de wall-clock),
	// derivada do [otelgenai.CostVelocity] de AOS-078. 0 se sem fonte/janela.
	CostMicroUSDPerSecond float64
	// TokensPerSecond é a token velocity (tokens de modelo por segundo de wall-clock).
	TokensPerSecond float64
	// Wall é o tempo ABSOLUTO decorrido desde a entrada em running (wall-clock, não o
	// [liveness.WorkClock.ActiveWork]). 0 se sem fonte.
	Wall time.Duration
	// StaleIterations é o nº de iterações consecutivas SEM progresso útil, contado pelo
	// breaker a partir da [ProgressSource]. 0 se a porta não está ligada.
	StaleIterations int
}

// VelocitySource é a PORTA do sinal de cost/token velocity: devolve o
// [otelgenai.CostVelocity] corrente do run (a projecção de AOS-078 sobre os spans
// chat da trajectória). É o MESMO sinal que o orçamento por árvore consome (ADR-008).
type VelocitySource interface {
	// Velocity devolve a velocity corrente do run.
	Velocity() otelgenai.CostVelocity
}

// VelocityFunc adapta uma função a [VelocitySource].
type VelocityFunc func() otelgenai.CostVelocity

// Velocity implementa [VelocitySource].
func (f VelocityFunc) Velocity() otelgenai.CostVelocity { return f() }

// WallClockSource é a PORTA do sinal wall-clock: devolve o tempo ABSOLUTO decorrido
// desde a entrada em running (não o relógio do lease nem o ActiveWork da liveness — é
// o backstop wall-clock absoluto que tecnica/08 §6 exige). O default de produção
// deriva-o da própria [state.Machine] ([NewMachineWallClock]).
type WallClockSource interface {
	// Elapsed devolve o tempo absoluto decorrido no estado running corrente.
	Elapsed() time.Duration
}

// WallClockFunc adapta uma função a [WallClockSource].
type WallClockFunc func() time.Duration

// Elapsed implementa [WallClockSource].
func (f WallClockFunc) Elapsed() time.Duration { return f() }

// ProgressSource é a PORTA PLUGÁVEL do sinal de ausência de progresso: reporta se o
// agente fez PROGRESSO ÚTIL na iteração corrente ("fez progresso nesta iteração?"). O
// breaker conta as iterações estéreis consecutivas a partir dela. O detector concreto
// — action-dedup por hash(tool+args) — é entregue por AOS-081 e liga-se AQUI; sem ele
// (porta nil) o sinal de no-progress nunca dispara.
type ProgressSource interface {
	// MadeProgress reporta se houve novo estado útil nesta iteração. false ⇒ iteração
	// estéril (incrementa o contador de no-progress).
	MadeProgress() bool
}

// ProgressFunc adapta uma função a [ProgressSource].
type ProgressFunc func() bool

// MadeProgress implementa [ProgressSource].
func (f ProgressFunc) MadeProgress() bool { return f() }

// EnabledSource é uma porta OPCIONAL que uma fonte de sinal pode implementar para declarar
// se está efectivamente ARMADA. A nil-check da cablagem fail-closed apanha uma fonte AUSENTE,
// mas não uma fonte PRESENTE que está inerte (ex.: um detector de action-dedup de AOS-081
// construído com Threshold<=0, cujo MadeProgress devolve sempre true — não-nil mas cego ao
// sinal). Quando a fonte implementa esta porta e reporta Enabled()==false enquanto o limiar
// respectivo está ligado, o breaker recusa a construção ([ErrProgressSourceInert]), fechando
// o buraco simetricamente com a nil-check. Fontes que NÃO a implementam são tratadas como
// armadas (compat retroactiva: uma [ProgressFunc] arbitrária continua válida).
type EnabledSource interface {
	// Enabled reporta se a fonte está armada (o sinal pode efectivamente cruzar).
	Enabled() bool
}
