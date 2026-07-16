package sandbox

import (
	"context"
	"testing"
	"time"
)

func fixedClock(ts time.Time) func() time.Time { return func() time.Time { return ts } }

func TestColdStart_Percentile(t *testing.T) {
	tests := []struct {
		name    string
		samples []time.Duration
		p       float64
		want    time.Duration
	}{
		{"empty", nil, 0.95, 0},
		{"single", []time.Duration{10 * time.Millisecond}, 0.95, 10 * time.Millisecond},
		{"p95-of-100", ramp(100), 0.95, 95 * time.Millisecond},
		{"p50-of-10", ramp(10), 0.50, 5 * time.Millisecond},
		{"p100", ramp(10), 1.0, 10 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := percentileOf(tc.samples, tc.p); got != tc.want {
				t.Fatalf("percentileOf = %v, want %v", got, tc.want)
			}
		})
	}
}

// ramp devolve n durações 1ms..n*ms.
func ramp(n int) []time.Duration {
	out := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		out[i] = time.Duration(i+1) * time.Millisecond
	}
	return out
}

func sample(cs time.Duration) ColdStartSample {
	return ColdStartSample{ImageVersion: "img-v1", Driver: DriverFake, Outcome: OutcomeWarmHit, ColdStart: cs, Restore: 10 * time.Millisecond}
}

// TestColdStart_AlertOnBreachTransition prova que o alerta dispara na TRANSIÇÃO para
// incumprimento (p95 > alvo) e é anti-flapping (não repete enquanto continua em
// incumprimento), re-armando na recuperação.
func TestColdStart_AlertOnBreachTransition(t *testing.T) {
	metrics := &MemoryColdStartMetricSink{}
	alerts := &MemoryColdStartAlertSink{}
	rec := NewColdStartRecorder(
		WithColdStartTarget(100*time.Millisecond),
		WithColdStartMetricSink(metrics),
		WithColdStartAlertSink(alerts),
		WithColdStartClock(fixedClock(time.Unix(0, 0))),
	)
	ctx := context.Background()

	// Saudável: abaixo do alvo — sem alerta.
	rec.Observe(ctx, nil, sample(50*time.Millisecond))
	if alerts.Len() != 0 {
		t.Fatalf("nao devia alertar abaixo do alvo")
	}
	// Incumprimento: acima do alvo — dispara UMA vez.
	rd := rec.Observe(ctx, nil, sample(200*time.Millisecond))
	if !rd.Alerted || alerts.Len() != 1 {
		t.Fatalf("esperava 1 alerta na transicao, obtive alerted=%v len=%d", rd.Alerted, alerts.Len())
	}
	// Continua em incumprimento: NÃO repete (anti-flapping).
	rd = rec.Observe(ctx, nil, sample(210*time.Millisecond))
	if rd.Alerted || alerts.Len() != 1 {
		t.Fatalf("nao devia repetir alerta enquanto em incumprimento, len=%d", alerts.Len())
	}
	// Recupera bem abaixo do alvo com muitas amostras — re-arma (sem novo alerta).
	for i := 0; i < 200; i++ {
		rec.Observe(ctx, nil, sample(1*time.Millisecond))
	}
	if alerts.Len() != 1 {
		t.Fatalf("recuperacao nao devia disparar alerta, len=%d", alerts.Len())
	}
	agg, _ := rec.SnapshotAgg(PoolKey{"img-v1", DriverFake})
	if agg.Breached {
		t.Fatal("esperava re-armado (nao breached) apos recuperacao")
	}
	// Nova quebra dispara de novo (segundo alerta).
	for i := 0; i < 300; i++ {
		rec.Observe(ctx, nil, sample(500*time.Millisecond))
	}
	if alerts.Len() != 2 {
		t.Fatalf("esperava 2º alerta na nova quebra, len=%d", alerts.Len())
	}
	if metrics.Len() == 0 {
		t.Fatal("esperava métricas emitidas")
	}
}

// TestColdStart_MinSamplesSuppressesAlert prova que abaixo de minSamples o alerta
// não é avaliado (anti-flapping por amostra insuficiente).
func TestColdStart_MinSamplesSuppressesAlert(t *testing.T) {
	alerts := &MemoryColdStartAlertSink{}
	rec := NewColdStartRecorder(
		WithColdStartTarget(100*time.Millisecond),
		WithColdStartMinSamples(5),
		WithColdStartAlertSink(alerts),
	)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		rec.Observe(ctx, nil, sample(999*time.Millisecond))
	}
	if alerts.Len() != 0 {
		t.Fatalf("nao devia alertar com < minSamples, len=%d", alerts.Len())
	}
	rec.Observe(ctx, nil, sample(999*time.Millisecond)) // 5ª amostra
	if alerts.Len() != 1 {
		t.Fatalf("esperava alerta ao atingir minSamples, len=%d", alerts.Len())
	}
}

// TestColdStart_KeysIsolated prova que versões/drivers distintos não se contaminam.
func TestColdStart_KeysIsolated(t *testing.T) {
	rec := NewColdStartRecorder(WithColdStartTarget(100 * time.Millisecond))
	ctx := context.Background()
	rec.Observe(ctx, nil, ColdStartSample{ImageVersion: "a", Driver: DriverFake, ColdStart: 10 * time.Millisecond})
	rec.Observe(ctx, nil, ColdStartSample{ImageVersion: "b", Driver: DriverFake, ColdStart: 300 * time.Millisecond})
	if p := rec.P95For(PoolKey{"a", DriverFake}); p != 10*time.Millisecond {
		t.Fatalf("chave a contaminada: %v", p)
	}
	if p := rec.P95For(PoolKey{"b", DriverFake}); p != 300*time.Millisecond {
		t.Fatalf("chave b errada: %v", p)
	}
}

// TestColdStart_WindowBounded prova que a janela deslizante limita as amostras e o
// SLI reflecte a latência RECENTE (a história antiga sai da janela).
func TestColdStart_WindowBounded(t *testing.T) {
	rec := NewColdStartRecorder(WithColdStartWindow(10))
	ctx := context.Background()
	// 10 amostras altas, depois 10 baixas: a janela só retém as 10 recentes (baixas).
	for i := 0; i < 10; i++ {
		rec.Observe(ctx, nil, sample(100*time.Millisecond))
	}
	for i := 0; i < 10; i++ {
		rec.Observe(ctx, nil, sample(5*time.Millisecond))
	}
	agg, _ := rec.SnapshotAgg(PoolKey{"img-v1", DriverFake})
	if agg.Samples != 10 {
		t.Fatalf("esperava janela de 10, obtive %d", agg.Samples)
	}
	if agg.P95 != 5*time.Millisecond {
		t.Fatalf("janela nao descartou a historia antiga: p95=%v", agg.P95)
	}
}

// TestColdStart_ObserveExhaustion prova o registo observável de esgotamentos por
// política.
func TestColdStart_ObserveExhaustion(t *testing.T) {
	metrics := &MemoryColdStartMetricSink{}
	rec := NewColdStartRecorder(WithColdStartMetricSink(metrics))
	key := PoolKey{"img-v1", DriverFake}
	rec.ObserveExhaustion(context.Background(), key, PolicyReject)
	rec.ObserveExhaustion(context.Background(), key, PolicyExpand)
	agg, _ := rec.SnapshotAgg(key)
	if agg.Exhaustions != 2 {
		t.Fatalf("esperava 2 esgotamentos, obtive %d", agg.Exhaustions)
	}
	var found bool
	for _, m := range metrics.Metrics() {
		if m.Name == MetricPoolExhausted && m.Attributes[AttrExhaustionPolicy] == "reject" {
			found = true
		}
	}
	if !found {
		t.Fatal("esperava métrica de esgotamento com politica reject")
	}
}

// TestColdStart_NoSecretInMetrics varre as métricas emitidas e confirma que só
// transportam eixos não-secretos (versão/driver/resultado/scope) — nunca um segredo.
func TestColdStart_NoSecretInMetrics(t *testing.T) {
	metrics := &MemoryColdStartMetricSink{}
	rec := NewColdStartRecorder(WithColdStartMetricSink(metrics))
	rec.Observe(context.Background(), nil, sample(10*time.Millisecond))
	allowedAttrs := map[string]bool{
		AttrImageVersion: true, AttrDriver: true, AttrProvisionOutcome: true,
		AttrColdStartScope: true, AttrExhaustionPolicy: true,
	}
	for _, m := range metrics.Metrics() {
		for k := range m.Attributes {
			if !allowedAttrs[k] {
				t.Fatalf("atributo inesperado (possivel fuga) na métrica: %q", k)
			}
		}
	}
}

// TestColdStart_SpanAnnotation prova que a anotação de span leva o cold-start/p95 e
// é segura com span nil.
func TestColdStart_SpanAnnotation(t *testing.T) {
	rec := NewColdStartRecorder()
	span := &recordingSpan{name: "reserve", attrs: map[string]any{}}
	rec.Observe(context.Background(), span, sample(12*time.Millisecond))
	if _, ok := span.attrs[AttrColdStartMs]; !ok {
		t.Fatal("esperava anotacao de cold-start no span")
	}
	if _, ok := span.attrs[AttrColdStartP95Ms]; !ok {
		t.Fatal("esperava anotacao de p95 no span")
	}
	// span nil não deve entrar em panic.
	rec.Observe(context.Background(), nil, sample(12*time.Millisecond))
}

// TestColdStart_OptionGuards prova que as opções ignoram valores inválidos.
func TestColdStart_OptionGuards(t *testing.T) {
	rec := NewColdStartRecorder(
		WithColdStartTarget(-1),
		WithColdStartPercentile(2),
		WithColdStartMinSamples(0),
		WithColdStartWindow(0),
		WithColdStartMetricSink(nil),
		WithColdStartAlertSink(nil),
		WithColdStartClock(nil),
	)
	if rec.Target() != DefaultColdStartTarget {
		t.Fatalf("target invalido deveria manter default, obtive %v", rec.Target())
	}
	if rec.percentile != DefaultColdStartPercentile {
		t.Fatalf("percentil invalido deveria manter default")
	}
}
