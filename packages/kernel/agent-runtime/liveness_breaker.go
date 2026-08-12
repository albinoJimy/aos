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

// ActionObserver recebe, no FECHO da mediação de UMA tool call (o span execute_tool já
// terminou, qualquer que seja o veredicto — permit, deny ou escalate), o runID e o hash
// canónico da acção. É a fonte do sinal de NO-PROGRESS do disjuntor (AOS-251): o hash é o
// MESMO que o Reference Monitor anota no span execute_tool
// ([otelgenai.CanonicalToolCallHash] sobre a call JÁ na forma final, pós-reescrita) — não
// se inventa uma segunda noção de "acção". Uma call cuja reescrita falhou NÃO é observada
// (nunca chegou a ser mediada: não há execute_tool).
//
// O observador é infra-estrutura TRUSTED do nó (o detector de action-dedup); o loop
// limita-se a reportar. ADITIVO: sem [WithActionObserver] nada é reportado e o
// comportamento de AOS-013 é byte-idêntico.
type ActionObserver func(runID, toolCallHash string)

// WithActionObserver injecta o observador de acções (AOS-251). Um valor nil é ignorado.
func WithActionObserver(o ActionObserver) Option {
	return func(rt *Runtime) {
		if o != nil {
			rt.actionObserver = o
		}
	}
}
