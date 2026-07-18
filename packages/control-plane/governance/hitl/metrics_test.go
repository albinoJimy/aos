package hitl

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
)

// AC5 — override-rate: a métrica é calculada e exposta como sinal; um override-rate
// acima do limiar dispara o sinal anti-rubber-stamping.
func TestOverrideRate_MeasuredExposedAndThresholdSignals(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	pub := vault.provision("approver-1", 0x11)

	// Guião: aprova sempre (override-rate → 1.0, muito acima do limiar).
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		return signApprovalFor(t, vault, "approver-1", true, p), nil
	}}
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", pub, RequiredAuthority(risk.ClassDanger))
	store := audit.NewMemStore()
	sink := &captureSink{}
	tracer := &captureTracer{}
	ch, err := NewChannel(reg, src, store,
		WithClock(fixedClock()),
		WithMetricSink(sink),
		WithTracer(tracer),
		WithOverrideRateThreshold(0.40),
	)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}

	const n = 5
	for i := 0; i < n; i++ {
		if _, err := ch.Confirm(context.Background(), dangerReq("run-7", "x")); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
	}

	prompted, overrides, _, _, rate := ch.Metrics().Snapshot()
	if prompted != n || overrides != n {
		t.Fatalf("esperava prompted=overrides=%d, obtive %d/%d", n, prompted, overrides)
	}
	if rate != 1.0 {
		t.Fatalf("override-rate esperado 1.0, obtive %v", rate)
	}
	// A métrica foi exposta ao sink em cada decisão.
	lastRate, records, signals := sink.snapshot()
	if records != n {
		t.Fatalf("esperava %d RecordOverrideRate, obtive %d", n, records)
	}
	if lastRate != 1.0 {
		t.Fatalf("sink: ultimo rate = %v, esperava 1.0", lastRate)
	}
	// O limiar 0.40 foi ultrapassado: o sinal disparou.
	if signals == 0 {
		t.Fatalf("override-rate > limiar devia disparar SignalHighOverrideRate")
	}
	// O span expõe o atributo do override-rate.
	sp := tracer.last()
	if sp == nil {
		t.Fatalf("nenhum span emitido")
	}
	if v, ok := sp.attr(AttrOverrideRate); !ok || v.(float64) != 1.0 {
		t.Fatalf("span sem atributo override_rate correcto: %v ok=%v", v, ok)
	}
	if v, ok := sp.attr("aos.hitl.override_rate_alarm"); !ok || v.(bool) != true {
		t.Fatalf("span devia marcar o alarme de override-rate")
	}
}

// Um override-rate ABAIXO do limiar não dispara o sinal.
func TestOverrideRate_BelowThresholdNoSignal(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	pub := vault.provision("approver-1", 0x11)
	// Guião: nega sempre (recusa assinada) → override-rate = 0.
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		return signApprovalFor(t, vault, "approver-1", false, p), nil
	}}
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", pub, RequiredAuthority(risk.ClassDanger))
	sink := &captureSink{}
	ch, _ := NewChannel(reg, src, audit.NewMemStore(), WithClock(fixedClock()), WithMetricSink(sink))
	for i := 0; i < 3; i++ {
		_, _ = ch.Confirm(context.Background(), dangerReq("run-7", "x"))
	}
	if _, _, signals := sink.snapshot(); signals != 0 {
		t.Fatalf("override-rate 0 nao devia disparar sinal, disparou %d", signals)
	}
	if r := ch.Metrics().OverrideRate(); r != 0 {
		t.Fatalf("override-rate esperado 0, obtive %v", r)
	}
}

// Metrics.Exceeds respeita a amostra vazia (não dispara sem prompts).
func TestMetrics_ExceedsEmptySample(t *testing.T) {
	t.Parallel()
	var m Metrics
	if m.Exceeds(0.0) {
		t.Fatalf("amostra vazia nao devia exceder nenhum limiar")
	}
	m.Prompted.Add(1)
	m.Overrides.Add(1)
	if !m.Exceeds(0.40) {
		t.Fatalf("rate 1.0 devia exceder 0.40")
	}
	if m.Exceeds(1.0) {
		t.Fatalf("rate 1.0 nao excede estritamente 1.0")
	}
}
