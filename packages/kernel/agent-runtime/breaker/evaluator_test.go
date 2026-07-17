package breaker

import (
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/state"
)

// TestEvaluatePure_NoSignalsEnabled: sem limiares ligados, nunca dispara (breaker inerte).
func TestEvaluatePure_NoSignalsEnabled(t *testing.T) {
	dec := Evaluate(SignalSnapshot{CostMicroUSDPerSecond: 1e9, Wall: time.Hour, StaleIterations: 100}, Thresholds{})
	if dec.Trip {
		t.Fatalf("sem limiares ligados não devia disparar; dec=%+v", dec)
	}
}

// TestEvaluatePure_AnySingleSignal: com CompositionAny (default), qualquer sinal cruzado
// dispara — e o alvo/razão segue a precedência.
func TestEvaluatePure_AnySingleSignal(t *testing.T) {
	cases := []struct {
		name       string
		snap       SignalSnapshot
		th         Thresholds
		wantReason Signal
		wantTarget state.State
	}{
		{
			name:       "cost_velocity->paused",
			snap:       SignalSnapshot{CostMicroUSDPerSecond: 600_000},
			th:         Thresholds{MaxCostMicroUSDPerSecond: 500_000},
			wantReason: SignalCostVelocity,
			wantTarget: state.Paused,
		},
		{
			name:       "token_velocity->paused",
			snap:       SignalSnapshot{TokensPerSecond: 1200},
			th:         Thresholds{MaxTokensPerSecond: 1000},
			wantReason: SignalTokenVelocity,
			wantTarget: state.Paused,
		},
		{
			name:       "wall_clock->timed_out",
			snap:       SignalSnapshot{Wall: 6 * time.Minute},
			th:         Thresholds{MaxWallClock: 5 * time.Minute},
			wantReason: SignalWallClock,
			wantTarget: state.TimedOut,
		},
		{
			name:       "no_progress->paused",
			snap:       SignalSnapshot{StaleIterations: 3},
			th:         Thresholds{MaxStaleIterations: 3},
			wantReason: SignalNoProgress,
			wantTarget: state.Paused,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dec := Evaluate(c.snap, c.th)
			if !dec.Trip {
				t.Fatalf("esperava trip; dec=%+v", dec)
			}
			if dec.Reason != c.wantReason {
				t.Errorf("reason=%q; quero %q", dec.Reason, c.wantReason)
			}
			if dec.Target != c.wantTarget {
				t.Errorf("target=%q; quero %q", dec.Target, c.wantTarget)
			}
		})
	}
}

// TestEvaluatePure_FailClosedBoundary: a comparação é `>=` (atingir o limiar já cruza).
func TestEvaluatePure_FailClosedBoundary(t *testing.T) {
	th := Thresholds{MaxWallClock: 5 * time.Minute}
	if dec := Evaluate(SignalSnapshot{Wall: 5 * time.Minute}, th); !dec.Trip {
		t.Errorf("wall == limiar devia cruzar (fail-closed >=); dec=%+v", dec)
	}
	if dec := Evaluate(SignalSnapshot{Wall: 5*time.Minute - time.Nanosecond}, th); dec.Trip {
		t.Errorf("wall < limiar não devia cruzar; dec=%+v", dec)
	}
}

// TestEvaluatePure_WallClockPrecedence: quando wall-clock cruza junto com outro sinal, o
// alvo é timed_out (o terminal fixado pela spec vence).
func TestEvaluatePure_WallClockPrecedence(t *testing.T) {
	snap := SignalSnapshot{CostMicroUSDPerSecond: 1e6, Wall: 10 * time.Minute}
	th := Thresholds{MaxCostMicroUSDPerSecond: 500_000, MaxWallClock: 5 * time.Minute}
	dec := Evaluate(snap, th)
	if !dec.Trip || dec.Reason != SignalWallClock || dec.Target != state.TimedOut {
		t.Fatalf("wall-clock devia ter precedência (timed_out); dec=%+v", dec)
	}
	if len(dec.Crossed) != 2 {
		t.Errorf("crossed=%v; quero ambos os sinais", dec.Crossed)
	}
}

// TestEvaluatePure_CompositionAll: com CompositionAll, só dispara quando TODOS os sinais
// ligados cruzam em simultâneo.
func TestEvaluatePure_CompositionAll(t *testing.T) {
	th := Thresholds{
		MaxCostMicroUSDPerSecond: 500_000,
		MaxWallClock:             5 * time.Minute,
		Composition:              CompositionAll,
	}
	// Só o custo cruza → NÃO dispara (all exige ambos).
	if dec := Evaluate(SignalSnapshot{CostMicroUSDPerSecond: 600_000, Wall: time.Minute}, th); dec.Trip {
		t.Fatalf("all com só um sinal cruzado não devia disparar; dec=%+v", dec)
	}
	// Ambos cruzam → dispara, alvo timed_out (wall tem precedência).
	dec := Evaluate(SignalSnapshot{CostMicroUSDPerSecond: 600_000, Wall: 6 * time.Minute}, th)
	if !dec.Trip || dec.Target != state.TimedOut {
		t.Fatalf("all com ambos cruzados devia disparar (timed_out); dec=%+v", dec)
	}
}

// TestStaticThresholdProvider_Resolution: override de classe > default global.
func TestStaticThresholdProvider_Resolution(t *testing.T) {
	def := Thresholds{MaxWallClock: time.Hour}
	special := Thresholds{MaxWallClock: time.Minute}
	p := NewStaticThresholdProvider(def).SetClass("fast", special)

	if got := p.Thresholds("fast"); got.MaxWallClock != time.Minute {
		t.Errorf("classe fast=%v; quero override %v", got.MaxWallClock, time.Minute)
	}
	if got := p.Thresholds("outra"); got.MaxWallClock != time.Hour {
		t.Errorf("classe sem override=%v; quero default %v", got.MaxWallClock, time.Hour)
	}
}
