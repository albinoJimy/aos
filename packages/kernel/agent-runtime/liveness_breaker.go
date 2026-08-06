package agentruntime

import "context"

// ---------------------------------------------------------------------------
// AOS-080/081 — LivenessBreaker (circuit breaker multi-sinal do agente vivo)
// ---------------------------------------------------------------------------

// LivenessBreaker é a PORTA do circuit breaker multi-sinal do agente vivo, consultada na
// FRONTEIRA DE FIM DE TURNO. Existe para o loop poder terminar com um VEREDICTO ÚTIL
// («parou porque deixou de progredir», «excedeu o wall-clock», «queimou orçamento a esta
// velocidade») em vez de esgotar [DefaultMaxTurns] e devolver [ErrMaxTurnsExceeded] — uma
// paragem defensiva do esqueleto que não diz PORQUÊ.
//
// O adaptador real vive no pilar (packages/integration), sobre o
// [github.com/aos-ref/kernel/agent-runtime/breaker].Breaker já existente: é ele que detém
// os limiares por classe, as fontes de sinal (velocity/wall-clock/no-progress) e a
// transição DURÁVEL do run — o loop não replica nada disso. Idioma de portas do kernel:
// a porta aqui, o adaptador no composition root (ver ports.go).
//
// ADITIVA: sem [WithLivenessBreaker] o loop nunca a consulta e o comportamento de AOS-013
// é byte-idêntico.
type LivenessBreaker interface {
	// Observe reporta o fecho de UMA iteração do agente (fronteira de fim de turno) e
	// devolve se o breaker disparou e o rótulo do estado durável atingido
	// ("paused" | "timed_out").
	//
	// Contrato: quando devolve tripped=true, a transição durável JÁ foi materializada
	// pelo adaptador — o loop limita-se a parar e a reportar. Um erro é FATAL para o run
	// (fail-closed: uma falha da transição durável não pode ser engolida, senão o run
	// continuaria a queimar recursos com o disjuntor cego).
	//
	// É seguro chamar em qualquer turno: o breaker é internamente no-op quando o run não
	// conta como trabalho activo (esperas, ou um trip anterior), pelo que a chamada é
	// idempotente e não confunde tempo de espera com ausência de progresso.
	Observe(ctx context.Context, runID string, turn int) (tripped bool, target string, err error)
}

// WithLivenessBreaker injecta o circuit breaker do agente vivo (AOS-080/081). Um valor nil
// é ignorado (mantém o comportamento byte-idêntico de AOS-013, sem disjuntor).
func WithLivenessBreaker(b LivenessBreaker) Option {
	return func(rt *Runtime) {
		if b != nil {
			rt.breaker = b
		}
	}
}
