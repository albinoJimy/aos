package breaker

import (
	"context"

	"github.com/aos-ref/kernel/agent-runtime/state"
)

// Alert é o payload de um ALERTA OPERACIONAL de trip — rótulos e números, NUNCA
// segredos nem conteúdo de prompt. Transporta o suficiente para o operador triar a
// causa (o sinal, o alvo durável, o snapshot que disparou e os limiares em vigor).
type Alert struct {
	// RunID é o run que disparou (stream_id no Event Store).
	RunID string
	// Kind distingue a origem do alerta: um trip automático, uma escalada a humano ou
	// um abort gracioso.
	Kind AlertKind
	// Signal é o sinal primário do trip (vazio para escalada/abort manuais).
	Signal Signal
	// Target é o estado durável para onde o run transitou.
	Target state.State
	// Snapshot é a fotografia dos sinais no instante do trip.
	Snapshot SignalSnapshot
	// Thresholds são os limiares em vigor (a classe de agente).
	Thresholds Thresholds
	// Class é a classe de agente cujos limiares governaram a decisão.
	Class string
	// Note é uma nota operacional livre (rótulo, sem segredos) — usada nas acções
	// manuais (escalada/abort).
	Note string
}

// AlertKind classifica a origem de um [Alert].
type AlertKind string

const (
	// AlertTrip — trip automático do avaliador multi-sinal.
	AlertTrip AlertKind = "trip"
	// AlertEscalate — escalada manual a humano ([Breaker.EscalateToHuman]).
	AlertEscalate AlertKind = "escalate"
	// AlertAbort — abort gracioso ([Breaker.Abort]).
	AlertAbort AlertKind = "abort"
)

// AlertSink é a PORTA de alerta operacional. É chamada APÓS a transição durável ter
// sucesso (o alerta reflecte um facto consumado, não uma tentativa). O default é
// [NopAlertSink]. As implementações reais (paging, ticket, canal ops) NÃO devem
// bloquear indefinidamente nem receber segredos.
type AlertSink interface {
	// Alert entrega um alerta de trip/escalada/abort.
	Alert(ctx context.Context, a Alert)
}

// NopAlertSink descarta os alertas. É o default (o breaker funciona sem alerta ligado).
type NopAlertSink struct{}

// Alert implementa [AlertSink].
func (NopAlertSink) Alert(context.Context, Alert) {}

// AlertFunc adapta uma função a [AlertSink].
type AlertFunc func(ctx context.Context, a Alert)

// Alert implementa [AlertSink].
func (f AlertFunc) Alert(ctx context.Context, a Alert) { f(ctx, a) }
