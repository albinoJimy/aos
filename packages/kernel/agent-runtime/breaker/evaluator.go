package breaker

import "github.com/aos-ref/kernel/agent-runtime/state"

// Decision é o veredicto do avaliador multi-sinal [Evaluate]. Trip indica se o breaker
// deve abrir; Target é o estado durável de destino ([state.Paused] ou [state.TimedOut]);
// Reason é o sinal PRIMÁRIO (o que fixa o alvo e a razão gravada); Crossed são TODOS os
// sinais que cruzaram o limiar (para o span/auditoria).
type Decision struct {
	// Trip indica se algum critério de disparo foi satisfeito.
	Trip bool
	// Target é o estado durável de destino (só significativo se Trip).
	Target state.State
	// Reason é o sinal primário que fixa o alvo (o de maior precedência entre os
	// cruzados). Vazio se !Trip.
	Reason Signal
	// Crossed lista todos os sinais que atingiram o respectivo limiar, por ordem de
	// precedência (wall-clock, cost, token, no-progress).
	Crossed []Signal
}

// Evaluate é o AVALIADOR MULTI-SINAL PURO: dado um [SignalSnapshot] e os [Thresholds],
// decide o trip SEM I/O, SEM relógio e SEM estado — a mesma entrada produz sempre a
// mesma [Decision] (determinista e testável em isolamento). É o núcleo desacoplado dos
// colectores (colectores → snapshot → Evaluate → decisão).
//
// # Regras
//
//   - Um sinal só participa se o seu limiar for > 0 (LIGADO). A comparação é `>=`
//     (fail-closed: atingir o limiar já cruza).
//   - [CompositionAny] (default): trip se ALGUM sinal ligado cruzar. [CompositionAll]:
//     trip só se TODOS os ligados cruzarem em simultâneo.
//   - PRECEDÊNCIA do alvo/razão: WALL-CLOCK primeiro (→ [state.TimedOut], a associação
//     fixada pela spec — o estado terminal vence), depois cost velocity, token velocity
//     e no-progress (→ [state.Paused]). Reason é o primeiro sinal cruzado nesta ordem.
func Evaluate(snap SignalSnapshot, th Thresholds) Decision {
	var crossed []Signal
	enabled := 0

	// Ordem de precedência ao construir `crossed`: wall-clock primeiro, para que o seu
	// alvo terminal (timed_out) vença quando também cruza outro sinal.
	if th.MaxWallClock > 0 {
		enabled++
		if snap.Wall >= th.MaxWallClock {
			crossed = append(crossed, SignalWallClock)
		}
	}
	if th.MaxCostMicroUSDPerSecond > 0 {
		enabled++
		if snap.CostMicroUSDPerSecond >= th.MaxCostMicroUSDPerSecond {
			crossed = append(crossed, SignalCostVelocity)
		}
	}
	if th.MaxTokensPerSecond > 0 {
		enabled++
		if snap.TokensPerSecond >= th.MaxTokensPerSecond {
			crossed = append(crossed, SignalTokenVelocity)
		}
	}
	if th.MaxStaleIterations > 0 {
		enabled++
		if snap.StaleIterations >= th.MaxStaleIterations {
			crossed = append(crossed, SignalNoProgress)
		}
	}

	if enabled == 0 || len(crossed) == 0 {
		return Decision{}
	}

	var trip bool
	switch th.resolvedComposition() {
	case CompositionAll:
		// Todos os sinais LIGADOS têm de estar cruzados em simultâneo.
		trip = len(crossed) == enabled
	default: // CompositionAny
		trip = true // len(crossed) >= 1 já garantido acima
	}
	if !trip {
		return Decision{Crossed: crossed}
	}

	reason := crossed[0] // precedência: wall-clock vem primeiro se cruzou
	return Decision{
		Trip:    true,
		Target:  targetFor(reason),
		Reason:  reason,
		Crossed: crossed,
	}
}

// targetFor mapeia o sinal primário no estado durável de destino: wall-clock →
// timed_out (fixado pela spec); velocity/no-progress → paused.
func targetFor(s Signal) state.State {
	if s == SignalWallClock {
		return state.TimedOut
	}
	return state.Paused
}

// reasonLabelFor mapeia o sinal na razão canónica gravada na transição durável (rótulo
// de auditoria, distinto do wall_clock_exceeded de Machine.CheckDeadlines para atribuir
// a causa AO BREAKER).
func reasonLabelFor(s Signal) string {
	switch s {
	case SignalCostVelocity:
		return "breaker_cost_velocity"
	case SignalTokenVelocity:
		return "breaker_token_velocity"
	case SignalWallClock:
		return "breaker_wall_clock_exceeded"
	case SignalNoProgress:
		return "breaker_no_progress"
	default:
		return "breaker_trip"
	}
}
