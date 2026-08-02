package planneraut

import (
	"math"
	"testing"
)

// ComputeSignals é puro e produz as rates esperadas, incluindo o SLI de fracção de
// planeamento. Falha-antes: se PlanningFraction usasse o denominador errado (só
// PlanningUnits em vez de planning+execution), a fracção viria 1.0, não 0.04.
func TestComputeSignals_RatesAndPlanningFraction(t *testing.T) {
	c := Counters{
		Plans:               100,
		ApprovedNoEdit:      95,
		Replans:             10,
		InvalidProposals:    3,
		CostSamples:         50,
		CostWithinTolerance: 48,
		PlanningUnits:       4,
		ExecutionUnits:      96, // total 100 ⇒ fracção 0.04
	}
	s := ComputeSignals(c, 0.02)

	approx := func(name string, got, want float64) {
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("%s = %v, esperado %v", name, got, want)
		}
	}
	approx("ApprovalNoEditRate", s.ApprovalNoEditRate, 0.95)
	approx("ReplanRate", s.ReplanRate, 0.10)
	approx("CostCalibration", s.CostCalibration, 0.96)
	approx("InvalidRate", s.InvalidRate, 0.03)
	approx("OverrideRate", s.OverrideRate, 0.02)
	approx("PlanningFraction (SLI)", s.PlanningFraction, 0.04)

	if !s.PlanningWithinSLI(DefaultMaxPlanningFraction) {
		t.Fatalf("fracção 0.04 devia respeitar o SLI de %v", DefaultMaxPlanningFraction)
	}
}

// Sem amostras de custo a calibração é sã (1.0), não penaliza. Falha-antes: um
// impl que dividisse 0/0 daria NaN e a comparação de envelope falharia aberta.
func TestComputeSignals_NoCostSamplesIsHealthy(t *testing.T) {
	s := ComputeSignals(Counters{Plans: 10, CostSamples: 0}, 0)
	if s.CostCalibration != 1.0 {
		t.Fatalf("CostCalibration sem amostras = %v, esperado 1.0", s.CostCalibration)
	}
}

// A taxa de override reportada fora de [0,1] é confinada fail-closed a 1.0 — e 1.0
// continua a disparar o tecto de override (não passa despercebida).
func TestComputeSignals_OverrideClampedFailClosed(t *testing.T) {
	s := ComputeSignals(Counters{Plans: 1}, 1.7)
	if s.OverrideRate != 1.0 {
		t.Fatalf("override 1.7 devia confinar a 1.0, veio %v", s.OverrideRate)
	}
	if b := DefaultEnvelope().Evaluate(s); len(b) == 0 {
		t.Fatalf("override confinado a 1.0 devia romper o tecto do envelope")
	}
}

// O SLI de fracção de planeamento é um sinal do envelope: acima de 5% é anomalia.
func TestEnvelope_PlanningFractionSLIBreach(t *testing.T) {
	env := DefaultEnvelope()
	// 10 unidades de planeamento em 100 ⇒ 10% > 5%.
	s := ComputeSignals(Counters{Plans: 100, ApprovedNoEdit: 100, PlanningUnits: 10, ExecutionUnits: 90}, 0)
	breaches := env.Evaluate(s)
	found := false
	for _, b := range breaches {
		if b.Signal == SignalPlanningFraction {
			found = true
			if b.Direction != DirectionAboveMax || b.Bound != DefaultMaxPlanningFraction {
				t.Fatalf("brecha de SLI malformada: %+v", b)
			}
		}
	}
	if !found {
		t.Fatalf("fracção de planeamento 10%% devia romper o SLI; brechas=%+v", breaches)
	}
}
