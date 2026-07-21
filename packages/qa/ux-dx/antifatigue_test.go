package uxdx_test

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/governance/hitl"
)

// AC2 — ANTI-FADIGA / OVERRIDE-RATE. CONSOME a métrica de AOS-095 (NÃO a recria nem
// cria enforcement): compõe o [hitl.Channel] REAL, exercita-o e assere que o
// override-rate é EXPOSTO e que um limiar cronicamente alto é SINALIZADO como problema
// de superfície (rubber-stamping).

// Um override-rate cronicamente alto (> 0.40) é EXPOSTO em cada decisão e SINALIZADO
// como problema de superfície — o sinal anti-rubber-stamping (Art. 14 EU AI Act).
func TestAntiFatigue_OverrideRateExposedAndChronicallyHighSignaled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A métrica canónica de AOS-095 está exposta com o nome contratual.
	if hitl.MetricOverrideRate != "approval.override_rate" {
		t.Fatalf("MetricOverrideRate=%q, quero \"approval.override_rate\"", hitl.MetricOverrideRate)
	}

	// Canal REAL que aprova SEMPRE → override-rate → 1.0 (muito acima de 0.40): modela um
	// aprovador a fazer rubber-stamping crónico.
	ch, sink := realChannel(t, true)
	const n = 6
	for i := 0; i < n; i++ {
		if _, err := ch.Confirm(ctx, dangerReq("cap:fs.delete -> /data/synthetic")); err != nil {
			t.Fatalf("Confirm[%d]: %v", i, err)
		}
	}

	// O override-rate é EXPOSTO (via Metrics de AOS-095, consumido — não recriado).
	prompted, overrides, _, _, rate := ch.Metrics().Snapshot()
	if prompted != n || overrides != n {
		t.Fatalf("prompted=%d overrides=%d, quero %d/%d", prompted, overrides, n, n)
	}
	if rate != 1.0 {
		t.Fatalf("override-rate=%v, quero 1.0 (rubber-stamping)", rate)
	}
	if !ch.Metrics().Exceeds(hitl.DefaultOverrideRateThreshold) {
		t.Fatalf("override-rate %v devia exceder o limiar %v", rate, hitl.DefaultOverrideRateThreshold)
	}

	// A métrica foi EXPOSTA ao sink em CADA decisão, e o limiar excedido DISPAROU o
	// sinal anti-fadiga — o problema de superfície é sinalizado.
	lastRate, records, signals := sink.snapshot()
	if records != n {
		t.Fatalf("RecordOverrideRate chamado %d vezes, quero %d (exposto por decisão)", records, n)
	}
	if lastRate != 1.0 {
		t.Fatalf("último rate exposto=%v, quero 1.0", lastRate)
	}
	if signals == 0 {
		t.Fatal("override-rate cronicamente alto NÃO foi sinalizado (SignalHighOverrideRate não disparou)")
	}
}

// Contraprova NÃO-TAUTOLÓGICA: abaixo do limiar o sinal NÃO dispara (a bateria distingue
// um problema real de superfície de um funcionamento normal).
func TestAntiFatigue_BelowThresholdNotSignaled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Canal REAL que NEGA sempre (recusa assinada) → override-rate = 0.
	ch, sink := realChannel(t, false)
	for i := 0; i < 4; i++ {
		if _, err := ch.Confirm(ctx, dangerReq("cap:fs.delete -> /data/synthetic")); err != nil {
			t.Fatalf("Confirm[%d]: %v", i, err)
		}
	}
	if r := ch.Metrics().OverrideRate(); r != 0 {
		t.Fatalf("override-rate=%v, quero 0 (todas negadas)", r)
	}
	if _, _, signals := sink.snapshot(); signals != 0 {
		t.Fatalf("override-rate 0 NÃO devia disparar o sinal, disparou %d", signals)
	}

	// Metrics.Exceeds é não-tautológico: VERDADEIRO acima do limiar, FALSO abaixo, e nunca
	// sobre amostra vazia. (Consome o predicado de AOS-095 tal e qual.)
	var m hitl.Metrics
	if m.Exceeds(hitl.DefaultOverrideRateThreshold) {
		t.Fatal("amostra vazia não devia exceder nenhum limiar")
	}
	m.Prompted.Add(10)
	m.Overrides.Add(5) // rate 0.50
	if !m.Exceeds(0.40) {
		t.Fatal("rate 0.50 devia exceder 0.40 (acima)")
	}
	if m.Exceeds(0.60) {
		t.Fatal("rate 0.50 NÃO devia exceder 0.60 (abaixo)")
	}
}
