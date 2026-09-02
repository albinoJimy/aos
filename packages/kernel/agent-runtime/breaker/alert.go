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
// [NopAlertSink]. Nenhuma implementação deve receber segredos.
//
// # CONTRATO: Alert TEM DE SER NÃO-BLOQUEANTE (AOS-291)
//
// `Alert` tem de RETORNAR PROMPTAMENTE — em tempo comparável a um append em memória ou a
// um envio para um canal com espaço. Uma implementação que faça paging, abra um ticket ou
// escreva num canal ops NÃO faz esse I/O aqui: enfileira-o e devolve, e o envio real corre
// noutra goroutine com o seu próprio prazo e a sua própria política de falha.
//
// PORQUE O CONTRATO É ESTE E NÃO «o disjuntor impõe um prazo». As duas alternativas
// estavam em aberto na AC de AOS-291. Um prazo imposto aqui exigiria correr o sink numa
// goroutine e abandoná-la ao fim do prazo — e uma goroutine abandonada a meio de um POST
// continua a segurar o socket, continua a poder entregar o alerta tarde, e transforma um
// sink lento numa fuga silenciosa proporcional ao número de trips. Ficaria também um
// segundo problema por resolver: `Alert` não devolve erro, pelo que o disjuntor não teria
// como distinguir «entregue» de «abandonado» nem a quem o reportar. Exigir não-bloqueio
// põe a responsabilidade onde está a informação — quem escreve o sink sabe o que o seu
// transporte demora; o disjuntor não sabe, e não tem como aprender.
//
// O QUE ACONTECE SE UMA IMPLEMENTAÇÃO VIOLAR O CONTRATO. Desde AOS-291, menos do que
// acontecia: `Alert` corre FORA do mutex do disjuntor, pelo que um sink bloqueado já não
// prende `Snapshot`, `Abort` nem `EscalateToHuman` — antes prendia-os pelo tempo exacto do
// bloqueio (medido: 3,0008 s contra 1,669 µs em repouso). O que um sink bloqueado ainda
// prende é a goroutine do run, no fim do turno em que o disjuntor disparou. É bastante
// menos grave — o run já está a ser parado — mas não é nada, e é por isso que isto é um
// contrato e não uma sugestão.
type AlertSink interface {
	// Alert entrega um alerta de trip/escalada/abort. Não bloqueia — ver o contrato acima.
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
