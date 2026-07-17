package breaker

import (
	"context"
	"fmt"
	"sync"

	"github.com/aos-ref/kernel/agent-runtime/liveness"
	"github.com/aos-ref/kernel/agent-runtime/state"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// OpBreakerTrip é o nome de operação do SPAN dedicado de trip (a spec permite um novo
// nome; usa-se este). Fica sob a mesma porta [otelgenai.Tracer] partilhada pelo RT/RM.
const OpBreakerTrip = "aos.breaker.trip"

// Atributos do span de trip (namespace aos.breaker.*). Todos são rótulos/números —
// NENHUM segredo ou conteúdo de prompt entra no span.
const (
	attrBreakerSignal   = "aos.breaker.signal"
	attrBreakerTarget   = "aos.breaker.target_state"
	attrBreakerReason   = "aos.breaker.reason"
	attrBreakerCrossed  = "aos.breaker.crossed"
	attrBreakerClass    = "aos.breaker.class"
	attrBreakerCostVel  = "aos.breaker.cost_micro_usd_per_s"
	attrBreakerTokenVel = "aos.breaker.tokens_per_s"
	attrBreakerWallMs   = "aos.breaker.wall_ms"
	attrBreakerStale    = "aos.breaker.stale_iterations"
	attrBreakerTripped  = "aos.breaker.tripped"
	attrBreakerKind     = "aos.breaker.kind"
	attrBreakerErrType  = "aos.breaker.error_type"
)

// Breaker é o circuit breaker multi-sinal de UM run. Agrega os colectores (velocity/
// wall-clock/progress), corre o avaliador puro [Evaluate] e executa a acção de trip
// (transição durável + span + alerta). Seguro para uso concorrente (um mutex serializa
// observação/decisão/transição, à imagem da Machine de AOS-017 — dono único). Construir
// com [NewBreaker].
type Breaker struct {
	machine  *state.Machine
	class    string
	th       Thresholds
	velocity VelocitySource
	wall     WallClockSource
	progress ProgressSource
	tracer   otelgenai.Tracer
	alert    AlertSink

	mu    sync.Mutex
	stale int // iterações estéreis consecutivas (sinal no-progress); reset ao dar trip
}

// Option configura o [Breaker] na construção.
type Option func(*Breaker)

// WithVelocitySource liga a porta do sinal de cost/token velocity (AOS-078). Sem ela, os
// sinais de velocity nunca disparam (ficam a 0).
func WithVelocitySource(v VelocitySource) Option {
	return func(b *Breaker) {
		if v != nil {
			b.velocity = v
		}
	}
}

// WithWallClockSource substitui a fonte do sinal wall-clock. Por omissão o breaker deriva
// o wall-clock ABSOLUTO da própria [state.Machine] ([NewMachineWallClock]).
func WithWallClockSource(w WallClockSource) Option {
	return func(b *Breaker) {
		if w != nil {
			b.wall = w
		}
	}
}

// WithProgressSource liga a porta PLUGÁVEL do sinal de ausência de progresso. O detector
// concreto (action-dedup por hash) é AOS-081; sem esta porta o sinal nunca dispara.
func WithProgressSource(p ProgressSource) Option {
	return func(b *Breaker) {
		if p != nil {
			b.progress = p
		}
	}
}

// WithTracer injecta a porta OTel do span de trip (default [otelgenai.NoopTracer]).
// Partilha a árvore de spans do run.
func WithTracer(t otelgenai.Tracer) Option {
	return func(b *Breaker) {
		if t != nil {
			b.tracer = t
		}
	}
}

// WithAlertSink liga o alerta operacional de trip (default [NopAlertSink]).
func WithAlertSink(s AlertSink) Option {
	return func(b *Breaker) {
		if s != nil {
			b.alert = s
		}
	}
}

// NewBreaker constrói o breaker de um run. m (a máquina durável de AOS-017) é
// OBRIGATÓRIA — a sua ausência é fail-closed ([ErrNilMachine]). Os limiares são
// resolvidos do [ThresholdProvider] para a class dada; provider nil recai em [Thresholds]
// zero (todos os sinais desligados — o breaker é inerte até ser configurado). O sinal
// wall-clock deriva por omissão da própria máquina; ligue as fontes de velocity/progresso
// com as [Option] respectivas.
//
// FAIL-CLOSED de cablagem: se um limiar de velocity ou no-progress estiver LIGADO (>0)
// mas a fonte respectiva não tiver sido cablada, a construção recusa
// ([ErrVelocitySourceMissing] / [ErrProgressSourceMissing]) — sem fonte o escalar fica
// sempre a 0 e o sinal NUNCA cruzaria, produzindo um breaker que se julga configurado mas
// está cego ao sinal (catastrófico em [CompositionAll], onde um só sinal ligado-sem-fonte
// mata a composição inteira). Recusar na construção é coerente com o [ErrNilMachine] do
// resto do pacote. O wall-clock não precisa de cablagem — deriva por omissão da máquina.
func NewBreaker(m *state.Machine, provider ThresholdProvider, class string, opts ...Option) (*Breaker, error) {
	if m == nil {
		return nil, ErrNilMachine
	}
	var th Thresholds
	if provider != nil {
		th = provider.Thresholds(class)
	}
	b := &Breaker{
		machine: m,
		class:   class,
		th:      th,
		tracer:  otelgenai.NoopTracer{},
		alert:   NopAlertSink{},
	}
	b.wall = NewMachineWallClock(m) // default: wall-clock absoluto da máquina
	for _, o := range opts {
		o(b)
	}
	if b.tracer == nil {
		b.tracer = otelgenai.NoopTracer{}
	}
	if b.alert == nil {
		b.alert = NopAlertSink{}
	}
	// Fail-closed: um sinal ligado (limiar>0) tem de ter a sua fonte cablada, senão fica
	// silenciosamente inerte (ver [ErrVelocitySourceMissing]/[ErrProgressSourceMissing]).
	if (th.MaxCostMicroUSDPerSecond > 0 || th.MaxTokensPerSecond > 0) && b.velocity == nil {
		return nil, fmt.Errorf("%w (class %q)", ErrVelocitySourceMissing, class)
	}
	if th.MaxStaleIterations > 0 && b.progress == nil {
		return nil, fmt.Errorf("%w (class %q)", ErrProgressSourceMissing, class)
	}
	return b, nil
}

// Thresholds devolve os limiares em vigor (resolvidos para a classe do breaker).
func (b *Breaker) Thresholds() Thresholds { return b.th }

// Class devolve a classe de agente do breaker.
func (b *Breaker) Class() string { return b.class }

// Snapshot recolhe os sinais correntes num [SignalSnapshot] SEM avaliar nem agir. Inclui
// o contador de iterações estéreis corrente. Útil para observabilidade/depuração e é o
// que [Observe] passa ao avaliador. Seguro para uso concorrente.
func (b *Breaker) Snapshot() SignalSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked()
}

// snapshotLocked assume o lock detido.
func (b *Breaker) snapshotLocked() SignalSnapshot {
	var s SignalSnapshot
	if b.velocity != nil {
		v := b.velocity.Velocity()
		s.CostMicroUSDPerSecond = v.CostMicroUSDPerSecond()
		s.TokensPerSecond = v.TokensPerSecond()
	}
	if b.wall != nil {
		s.Wall = b.wall.Elapsed()
	}
	s.StaleIterations = b.stale
	return s
}

// Observe é a chamada de UMA iteração do agente vivo: actualiza o contador de progresso
// a partir da [ProgressSource], recolhe o snapshot, corre o avaliador puro e, se
// decidir trip, executa a acção durável (transição + span + alerta). Devolve a [Decision]
// e um erro só se a transição durável falhar (fail-closed — não engole).
//
// Idempotente: se o run já não estiver em running (trip anterior sem resume intermédio),
// a acção é no-op — sem duplicar transições nem alertas.
func (b *Breaker) Observe(ctx context.Context) (Decision, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// EXCLUSÃO DO TEMPO DE ESPERA (AOS-019 / tecnica/08 §6). Os sinais wall-clock e
	// no-progress medem TRABALHO ACTIVO, não tempo-de-parede bruto — senão uma espera
	// legítima (waiting_on_human/waiting_on_tool/paused) leria-se como "sem progresso" e
	// faria trip de um run saudável. Se o run NÃO conta como trabalho activo (não está em
	// running — via [liveness.CountsAsActiveWork]), não acumula, não avalia e não age: é
	// no-op. O contador de iterações estéreis PRESERVA-SE através da espera (uma retoma
	// parte de onde parou, sem falso reset). É também o guard de idempotência natural: um
	// run já parado por um trip anterior não conta como trabalho activo.
	if !liveness.CountsAsActiveWork(b.machine.Current()) {
		return Decision{}, nil
	}

	// Sinal de no-progress: sem progresso na iteração ⇒ incrementa o contador de
	// iterações estéreis; com progresso ⇒ reinicia. Sem porta ligada, o contador fica a 0.
	if b.progress != nil {
		if b.progress.MadeProgress() {
			b.stale = 0
		} else {
			b.stale++
		}
	}

	snap := b.snapshotLocked()
	dec := Evaluate(snap, b.th)
	if !dec.Trip {
		return dec, nil
	}
	if err := b.trip(ctx, dec, snap); err != nil {
		return dec, err
	}
	return dec, nil
}

// trip executa a acção de disparo: (a) transição durável running → alvo (paused/
// timed_out), (b) span de trip, (c) alerta. É IDEMPOTENTE — se o run não estiver em
// running (já parado por um trip anterior, ou terminal) é no-op sem efeitos. FAIL-CLOSED:
// uma falha da transição durável propaga o erro (não é engolida) e o alerta NÃO dispara.
// Preserva a trajectória: a transição apenas APENDE ao event log; nada é destruído.
func (b *Breaker) trip(ctx context.Context, dec Decision, snap SignalSnapshot) error {
	// Pré-condição garantida por [Observe] sob o MESMO lock: o run conta como trabalho
	// activo (running). A idempotência do re-trip é assegurada a montante — um run já
	// parado por um trip anterior não conta como trabalho activo e nunca reentra aqui.
	ctx, span := b.tracer.StartSpan(ctx, OpBreakerTrip)
	b.decorateSpan(span, AlertTrip, dec.Reason, dec.Target, snap)
	span.SetAttribute(attrBreakerCrossed, joinSignals(dec.Crossed))

	if err := b.machine.Transition(ctx, dec.Target, state.TransitionEvent{Reason: reasonLabelFor(dec.Reason)}); err != nil {
		span.SetAttribute(attrBreakerTripped, false)
		span.SetAttribute(attrBreakerErrType, "durable_transition_failed")
		span.End()
		return fmt.Errorf("breaker: transição durável de trip (run %s → %s) falhou: %w",
			b.machine.RunID(), dec.Target, err)
	}
	span.SetAttribute(attrBreakerTripped, true)
	span.End()

	// A transição consumada reinicia o contador de no-progress: um resume posterior parte
	// de zero (evita re-trip imediato sem novas iterações estéreis).
	b.stale = 0

	b.alert.Alert(ctx, Alert{
		RunID:      b.machine.RunID(),
		Kind:       AlertTrip,
		Signal:     dec.Reason,
		Target:     dec.Target,
		Snapshot:   snap,
		Thresholds: b.th,
		Class:      b.class,
	})
	return nil
}

// EscalateToHuman transita o run vivo running → [state.WaitingOnHuman] (gate HITL), para
// que um operador decida — a alternativa graciosa ao trip automático. Emite span + alerta
// (kind escalate). Idempotente: se o run já estiver em waiting_on_human é no-op; fora de
// running devolve [ErrNotRunning] (não há trabalho vivo a escalar).
func (b *Breaker) EscalateToHuman(ctx context.Context, note string) error {
	return b.manualTransition(ctx, state.WaitingOnHuman, AlertEscalate, "breaker_escalate_human", note)
}

// Abort transita o run vivo running → [state.Failed] — o ABORT GRACIOSO: failed é a
// falha RECUPERÁVEL que entra na saga de compensação (failed → compensating), NÃO um
// kill cego. Emite span + alerta (kind abort). Idempotente: já em failed é no-op; fora de
// running devolve [ErrNotRunning].
func (b *Breaker) Abort(ctx context.Context, note string) error {
	return b.manualTransition(ctx, state.Failed, AlertAbort, "breaker_abort_graceful", note)
}

// manualTransition partilha a mecânica das acções manuais (escalar/abortar): valida a
// pré-condição de running, emite o span, transita e alerta. Fail-closed no erro da
// transição.
func (b *Breaker) manualTransition(ctx context.Context, target state.State, kind AlertKind, reason, note string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	cur := b.machine.Current()
	if cur == target {
		return nil // idempotente: já no alvo
	}
	if cur != state.Running {
		return ErrNotRunning
	}

	snap := b.snapshotLocked()
	ctx, span := b.tracer.StartSpan(ctx, OpBreakerTrip)
	b.decorateSpan(span, kind, "", target, snap)

	if err := b.machine.Transition(ctx, target, state.TransitionEvent{Reason: reason}); err != nil {
		span.SetAttribute(attrBreakerTripped, false)
		span.SetAttribute(attrBreakerErrType, "durable_transition_failed")
		span.End()
		return fmt.Errorf("breaker: transição durável %s (run %s → %s) falhou: %w",
			kind, b.machine.RunID(), target, err)
	}
	span.SetAttribute(attrBreakerTripped, true)
	span.End()
	b.stale = 0

	b.alert.Alert(ctx, Alert{
		RunID:      b.machine.RunID(),
		Kind:       kind,
		Target:     target,
		Snapshot:   snap,
		Thresholds: b.th,
		Class:      b.class,
		Note:       note,
	})
	return nil
}

// decorateSpan preenche os atributos comuns do span de trip/escalada/abort. Só
// rótulos/números — sem segredos.
func (b *Breaker) decorateSpan(span otelgenai.Span, kind AlertKind, signal Signal, target state.State, snap SignalSnapshot) {
	span.SetAttribute(otelgenai.AttrRunID, b.machine.RunID())
	span.SetAttribute(attrBreakerKind, string(kind))
	if signal != "" {
		span.SetAttribute(attrBreakerSignal, string(signal))
		span.SetAttribute(attrBreakerReason, reasonLabelFor(signal))
	}
	span.SetAttribute(attrBreakerTarget, string(target))
	span.SetAttribute(attrBreakerClass, b.class)
	span.SetAttribute(attrBreakerCostVel, snap.CostMicroUSDPerSecond)
	span.SetAttribute(attrBreakerTokenVel, snap.TokensPerSecond)
	span.SetAttribute(attrBreakerWallMs, snap.Wall.Milliseconds())
	span.SetAttribute(attrBreakerStale, snap.StaleIterations)
}

// joinSignals serializa a lista de sinais cruzados numa string estável (para o atributo
// de span), separada por vírgulas.
func joinSignals(sigs []Signal) string {
	out := ""
	for i, s := range sigs {
		if i > 0 {
			out += ","
		}
		out += string(s)
	}
	return out
}
