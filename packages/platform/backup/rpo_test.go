package backup

import (
	"context"
	"testing"
	"time"
)

// TestRPO_WithinOneMinute mede a JANELA EFECTIVA de RPO sob uma periodicidade de
// 30s com relógio injectado: em qualquer instante, o tempo desde que o backup
// ficou em dia com o head do Store mantém-se <= 1 min (AC4).
func TestRPO_WithinOneMinute(t *testing.T) {
	ctx := context.Background()
	const period = 30 * time.Second

	src := newSourceStore(t, "board-eu", "eu-west")
	now := t0
	clock := func() time.Time { return now }
	exp, _ := newExporter(t, src, "eu-west", WithClock(clock), WithPeriodicity(period))

	if !exp.WithinRPO(time.Minute) {
		t.Fatalf("periodicidade %v devia satisfazer RPO<=1min", exp.Periodicity())
	}

	var maxWindow time.Duration
	for k := 0; k < 6; k++ {
		// Eventos committed durante o intervalo.
		seed(t, src, "run-a", 2, "m")
		if _, err := exp.Export(ctx); err != nil {
			t.Fatalf("Export ciclo %d: %v", k, err)
		}
		// Instante imediatamente antes do próximo ciclo: pior caso de frescura.
		justBefore := now.Add(period - time.Nanosecond)
		if w := exp.RPOWindow(justBefore); w > maxWindow {
			maxWindow = w
		}
		now = now.Add(period)
	}

	if maxWindow > time.Minute {
		t.Fatalf("janela efectiva de RPO %v excede 1 min", maxWindow)
	}
	if maxWindow >= period+time.Second {
		t.Fatalf("janela de RPO %v não devia exceder a periodicidade %v", maxWindow, period)
	}
}

// TestRPO_PeriodicityGate confirma que uma periodicidade maior que o alvo NÃO
// satisfaz o RPO (a guarda é honesta).
func TestRPO_PeriodicityGate(t *testing.T) {
	src := newSourceStore(t, "board-eu", "eu-west")
	exp, _ := newExporter(t, src, "eu-west", WithPeriodicity(90*time.Second))
	if exp.WithinRPO(time.Minute) {
		t.Fatalf("periodicidade de 90s não devia satisfazer RPO<=1min")
	}
}
