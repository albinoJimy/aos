package cache_sli_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/cache/layout"
	"github.com/aos-ref/platform/model-gateway/metering/cache_sli"
	"github.com/aos-ref/platform/model-gateway/port"
)

// TestSampleFromUsage projecta port.Usage -> Sample (a ponte usada pelo GW) e
// confirma que, sem sinks configurados, o agregador ainda acumula (nop sinks).
func TestSampleFromUsage(t *testing.T) {
	t.Parallel()
	s := cache_sli.SampleFromUsage("run-1", "board-eu", "eu", port.Usage{
		PromptTokens: 100, CompletionTokens: 5, TotalTokens: 105, CacheReadTokens: 90, CacheWriteTokens: 10,
	})
	if s.RunID != "run-1" || s.Tenant != "board-eu" || s.Region != "eu" {
		t.Errorf("eixo de agregacao mal projectado: %+v", s)
	}
	if s.PromptTokens != 100 || s.CacheReadTokens != 90 || s.CacheWriteTokens != 10 {
		t.Errorf("tokens mal projectados: %+v", s)
	}
	rate, defined := cache_sli.CallRate(s)
	if !defined || rate != 0.9 {
		t.Errorf("rate = %v (defined=%v), quer 0.9", rate, defined)
	}
	// Sem sinks: o agregador default (nop metric/alert) acumula na mesma.
	rec := cache_sli.NewRecorder(cache_sli.WithClock(fixedClock()))
	rec.Observe(context.Background(), nil, s)
	if got, ok := rec.RateFor(cache_sli.Key{RunID: "run-1", Tenant: "board-eu"}); !ok || got != 0.9 {
		t.Errorf("RateFor = %v (ok=%v), quer 0.9", got, ok)
	}
}

// fixedClock devolve um relógio determinista (sem time.Now nos testes).
func fixedClock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

// --- 1. CÁLCULO por chamada (incl. PromptTokens==0 sem pânico) ---

func TestCallRate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		sample      cache_sli.Sample
		wantRate    float64
		wantDefined bool
	}{
		{
			name:        "hit total: read == prompt",
			sample:      cache_sli.Sample{PromptTokens: 100, CacheReadTokens: 100},
			wantRate:    1.0,
			wantDefined: true,
		},
		{
			name:        "hit parcial: 90/100",
			sample:      cache_sli.Sample{PromptTokens: 100, CacheReadTokens: 90},
			wantRate:    0.9,
			wantDefined: true,
		},
		{
			name:        "miss total: read 0 (cache write) -> 0%",
			sample:      cache_sli.Sample{PromptTokens: 100, CacheReadTokens: 0, CacheWriteTokens: 100},
			wantRate:    0.0,
			wantDefined: true,
		},
		{
			name:        "exactamente no limiar: 80/100",
			sample:      cache_sli.Sample{PromptTokens: 100, CacheReadTokens: 80},
			wantRate:    0.8,
			wantDefined: true,
		},
		{
			name:        "PromptTokens==0 -> INDEFINIDO, sem divisao por zero",
			sample:      cache_sli.Sample{PromptTokens: 0, CacheReadTokens: 0},
			wantRate:    0.0,
			wantDefined: false,
		},
		{
			name:        "PromptTokens negativo -> indefinido (defensivo)",
			sample:      cache_sli.Sample{PromptTokens: -5, CacheReadTokens: 3},
			wantRate:    0.0,
			wantDefined: false,
		},
		{
			name:        "read > prompt (inconsistente) -> fixado a 1.0",
			sample:      cache_sli.Sample{PromptTokens: 100, CacheReadTokens: 150},
			wantRate:    1.0,
			wantDefined: true,
		},
		{
			name:        "read negativo -> tratado como 0",
			sample:      cache_sli.Sample{PromptTokens: 100, CacheReadTokens: -10},
			wantRate:    0.0,
			wantDefined: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rate, defined := cache_sli.CallRate(tt.sample)
			if defined != tt.wantDefined {
				t.Fatalf("defined = %v, quer %v", defined, tt.wantDefined)
			}
			if defined && rate != tt.wantRate {
				t.Errorf("rate = %v, quer %v", rate, tt.wantRate)
			}
		})
	}
}

// TestCallRate_PromptZero_NaoPanica garante EXPLICITAMENTE a ausência de pânico
// (não só o resultado indefinido) para PromptTokens==0.
func TestCallRate_PromptZero_NaoPanica(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CallRate entrou em panico com PromptTokens==0: %v", r)
		}
	}()
	if _, defined := cache_sli.CallRate(cache_sli.Sample{PromptTokens: 0, CacheReadTokens: 10}); defined {
		t.Errorf("PromptTokens==0 devia ser indefinido")
	}
}

// --- 2. AGREGAÇÃO por run/tenant (isolamento) ---

func TestAggregation_ByRunTenant_Isolation(t *testing.T) {
	t.Parallel()
	rec := cache_sli.NewRecorder(cache_sli.WithClock(fixedClock()))
	ctx := context.Background()

	// runA/tenant1: dois hits altos -> ~0.95.
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "runA", Tenant: "tenant1", PromptTokens: 100, CacheReadTokens: 90})
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "runA", Tenant: "tenant1", PromptTokens: 100, CacheReadTokens: 100})

	// runB/tenant2: um miss total -> 0.0. NÃO deve contaminar runA/tenant1.
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "runB", Tenant: "tenant2", PromptTokens: 100, CacheReadTokens: 0})

	// Mesmo run, tenant DIFERENTE: chave distinta, isolada.
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "runA", Tenant: "tenant2", PromptTokens: 100, CacheReadTokens: 50})

	cases := []struct {
		key      cache_sli.Key
		wantRate float64
	}{
		{cache_sli.Key{RunID: "runA", Tenant: "tenant1"}, 0.95}, // (90+100)/(200)
		{cache_sli.Key{RunID: "runB", Tenant: "tenant2"}, 0.0},
		{cache_sli.Key{RunID: "runA", Tenant: "tenant2"}, 0.5},
	}
	for _, c := range cases {
		rate, defined := rec.RateFor(c.key)
		if !defined {
			t.Fatalf("chave %+v: indefinida", c.key)
		}
		if rate != c.wantRate {
			t.Errorf("chave %+v: rate = %v, quer %v (contaminacao entre chaves?)", c.key, rate, c.wantRate)
		}
	}

	// Uma chave nunca observada é indefinida (não 0).
	if _, defined := rec.RateFor(cache_sli.Key{RunID: "inexistente"}); defined {
		t.Errorf("chave inexistente devia ser indefinida")
	}
}

// TestUndefinedCall_NotAggregated: uma chamada com PromptTokens==0 é omitida da
// agregação (não conta como 0% que envenenaria o SLI).
func TestUndefinedCall_NotAggregated(t *testing.T) {
	t.Parallel()
	rec := cache_sli.NewRecorder(cache_sli.WithClock(fixedClock()))
	ctx := context.Background()
	key := cache_sli.Key{RunID: "r", Tenant: "t"}

	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 90})
	// Chamada indefinida: não deve alterar o agregado.
	rd := rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 0, CacheReadTokens: 0})
	if rd.CallDefined {
		t.Errorf("chamada com prompt 0 devia ser indefinida")
	}
	agg, ok := rec.Snapshot(key)
	if !ok || agg.Samples != 1 || agg.PromptTokens != 100 {
		t.Errorf("agregado contaminado por chamada indefinida: %+v", agg)
	}
}

// --- 3. ALERTA < limiar (dispara) vs >= limiar (nao dispara); limiar configuravel ---

func TestAlert_BelowThreshold_Fires(t *testing.T) {
	t.Parallel()
	alerts := &cache_sli.MemoryAlertSink{}
	rec := cache_sli.NewRecorder(
		cache_sli.WithAlertSink(alerts),
		cache_sli.WithClock(fixedClock()),
	)
	// Um único miss total: 0% < 80% -> alerta.
	rd := rec.Observe(context.Background(), nil,
		cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 0})
	if !rd.Alerted || alerts.Len() != 1 {
		t.Fatalf("esperava 1 alerta, rd.Alerted=%v len=%d", rd.Alerted, alerts.Len())
	}
	a := alerts.Alerts()[0]
	if a.Key != (cache_sli.Key{RunID: "r", Tenant: "t"}) || a.Threshold != cache_sli.DefaultThreshold {
		t.Errorf("alerta = %+v", a)
	}
	if a.Rate >= cache_sli.DefaultThreshold {
		t.Errorf("rate do alerta %v devia estar abaixo do limiar", a.Rate)
	}
}

func TestAlert_AtOrAboveThreshold_DoesNotFire(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		read int64
	}{
		{"exactamente 80%", 80},
		{"acima: 85%", 85},
		{"hit total", 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			alerts := &cache_sli.MemoryAlertSink{}
			rec := cache_sli.NewRecorder(cache_sli.WithAlertSink(alerts), cache_sli.WithClock(fixedClock()))
			rd := rec.Observe(context.Background(), nil,
				cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: tt.read})
			if rd.Alerted || alerts.Len() != 0 {
				t.Errorf("nao devia alertar a >= 80%% (read=%d): alerted=%v len=%d", tt.read, rd.Alerted, alerts.Len())
			}
		})
	}
}

func TestAlert_ConfigurableThreshold(t *testing.T) {
	t.Parallel()
	// Com limiar 50%, um rate de 60% NÃO alerta; com o default 80%, alertaria.
	alerts := &cache_sli.MemoryAlertSink{}
	rec := cache_sli.NewRecorder(
		cache_sli.WithThreshold(0.5),
		cache_sli.WithAlertSink(alerts),
		cache_sli.WithClock(fixedClock()),
	)
	if rec.Threshold() != 0.5 {
		t.Fatalf("limiar = %v, quer 0.5", rec.Threshold())
	}
	rd := rec.Observe(context.Background(), nil,
		cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 60})
	if rd.Alerted || alerts.Len() != 0 {
		t.Errorf("60%% >= limiar 50%%: nao devia alertar")
	}
	// Mas 40% < 50% -> alerta.
	rd = rec.Observe(context.Background(), nil,
		cache_sli.Sample{RunID: "r2", Tenant: "t", PromptTokens: 100, CacheReadTokens: 40})
	if !rd.Alerted || alerts.Len() != 1 {
		t.Errorf("40%% < limiar 50%%: devia alertar (len=%d)", alerts.Len())
	}
}

// TestAlert_MinSamples_AntiFlapping: com um mínimo de amostras, uma única chamada
// ruidosa abaixo do limiar NÃO alerta; só ao atingir o mínimo.
func TestAlert_MinSamples_AntiFlapping(t *testing.T) {
	t.Parallel()
	alerts := &cache_sli.MemoryAlertSink{}
	rec := cache_sli.NewRecorder(
		cache_sli.WithMinSamples(3),
		cache_sli.WithAlertSink(alerts),
		cache_sli.WithClock(fixedClock()),
	)
	ctx := context.Background()
	// Duas amostras abaixo do limiar mas < minSamples: sem alerta ainda.
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 0})
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 0})
	if alerts.Len() != 0 {
		t.Fatalf("nao devia alertar antes de minSamples: len=%d", alerts.Len())
	}
	// Terceira amostra: atinge minSamples e o agregado está abaixo -> alerta.
	rd := rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 0})
	if !rd.Alerted || alerts.Len() != 1 {
		t.Errorf("devia alertar ao atingir minSamples: alerted=%v len=%d", rd.Alerted, alerts.Len())
	}
}

// TestAlert_TransitionAndReArm: o alerta dispara UMA vez na transição para
// incumprimento (não a cada chamada) e re-arma quando o SLI recupera.
func TestAlert_TransitionAndReArm(t *testing.T) {
	t.Parallel()
	alerts := &cache_sli.MemoryAlertSink{}
	rec := cache_sli.NewRecorder(cache_sli.WithAlertSink(alerts), cache_sli.WithClock(fixedClock()))
	ctx := context.Background()
	k := cache_sli.Key{RunID: "r", Tenant: "t"}

	// Arranca saudável (>= 80%): sem alerta.
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 100})
	if alerts.Len() != 0 {
		t.Fatalf("saudavel nao devia alertar")
	}
	// Um grande miss arrasta o agregado abaixo: 100/300 < 0.8 -> 1 alerta (transição).
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 200, CacheReadTokens: 0})
	if got, _ := rec.RateFor(k); got >= cache_sli.DefaultThreshold {
		t.Fatalf("esperava agregado abaixo do limiar, got=%v", got)
	}
	if alerts.Len() != 1 {
		t.Fatalf("esperava 1 alerta na transicao, len=%d", alerts.Len())
	}
	// Continua abaixo: NÃO deve disparar de novo (anti-flapping).
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 0})
	if alerts.Len() != 1 {
		t.Errorf("nao devia re-disparar enquanto continua abaixo: len=%d", alerts.Len())
	}
	// Recupera acima do limiar (re-arma) e depois volta a cair -> novo alerta.
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 2000, CacheReadTokens: 2000})
	if got, _ := rec.RateFor(k); got < cache_sli.DefaultThreshold {
		t.Fatalf("esperava recuperacao acima do limiar, got=%v", got)
	}
	if alerts.Len() != 1 {
		t.Errorf("recuperacao nao devia gerar alerta: len=%d", alerts.Len())
	}
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100_000, CacheReadTokens: 0})
	if alerts.Len() != 2 {
		t.Errorf("nova queda depois de re-armar devia alertar: len=%d", alerts.Len())
	}
}

// --- 4. EMISSÃO OTel ligada à trajectória, sem segredo ---

func TestMetric_OTelAttributes_LinkedToTrajectory_NoSecret(t *testing.T) {
	t.Parallel()
	metrics := &cache_sli.MemoryMetricSink{}
	rec := cache_sli.NewRecorder(cache_sli.WithMetricSink(metrics), cache_sli.WithClock(fixedClock()))
	rec.Observe(context.Background(), nil, cache_sli.Sample{
		RunID: "run-42", Tenant: "board-eu", Region: "eu", PromptTokens: 100, CacheReadTokens: 90,
	})
	ms := metrics.Metrics()
	if len(ms) == 0 {
		t.Fatal("nenhuma metrica emitida")
	}
	var sawAggregate bool
	for _, m := range ms {
		if m.Name != cache_sli.MetricCacheHitRate {
			t.Errorf("nome de metrica inesperado: %q", m.Name)
		}
		// Ligação à trajectória: run/tenant/região presentes.
		if m.Attributes[cache_sli.AttrRunID] != "run-42" {
			t.Errorf("run_id ausente/errado: %v", m.Attributes[cache_sli.AttrRunID])
		}
		if m.Attributes[cache_sli.AttrTenant] != "board-eu" {
			t.Errorf("tenant ausente/errado: %v", m.Attributes[cache_sli.AttrTenant])
		}
		if m.Attributes[cache_sli.AttrRegion] != "eu" {
			t.Errorf("regiao ausente/errada: %v", m.Attributes[cache_sli.AttrRegion])
		}
		if m.Timestamp != time.Unix(1_700_000_000, 0).UTC() {
			t.Errorf("timestamp nao-determinista: %v", m.Timestamp)
		}
		// Sem segredo: os atributos são só um conjunto fechado não-secreto.
		for k := range m.Attributes {
			switch k {
			case cache_sli.AttrRunID, cache_sli.AttrTenant, cache_sli.AttrRegion, cache_sli.AttrScope:
			default:
				t.Errorf("atributo inesperado (potencial fuga): %q", k)
			}
		}
		if m.Attributes[cache_sli.AttrScope] == "aggregate" {
			sawAggregate = true
		}
	}
	if !sawAggregate {
		t.Errorf("esperava uma metrica de escopo agregado")
	}
}

// TestSpanAnnotation: o span da chamada é anotado com os rates (ligação à
// trajectória via o mesmo span gen_ai.*), sem segredo.
func TestSpanAnnotation(t *testing.T) {
	t.Parallel()
	tr := &agentruntime.RecordingTracer{}
	_, span := tr.StartSpan(context.Background(), agentruntime.OpChat)
	rec := cache_sli.NewRecorder(cache_sli.WithClock(fixedClock()))
	rec.Observe(context.Background(), span, cache_sli.Sample{
		RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 90,
	})
	span.End()
	spans := tr.Spans()
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span, got %d", len(spans))
	}
	attrs := spans[0].Attributes
	if attrs[cache_sli.AttrCallCacheHitRate] != 0.9 {
		t.Errorf("call hit rate no span = %v, quer 0.9", attrs[cache_sli.AttrCallCacheHitRate])
	}
	if attrs[cache_sli.AttrCacheHitRate] != 0.9 {
		t.Errorf("aggregate hit rate no span = %v, quer 0.9", attrs[cache_sli.AttrCacheHitRate])
	}
}

// TestObserve_NilSpan_Safe: Observe com span nil não entra em pânico.
func TestObserve_NilSpan_Safe(t *testing.T) {
	t.Parallel()
	rec := cache_sli.NewRecorder(cache_sli.WithClock(fixedClock()))
	rec.Observe(context.Background(), nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 10, CacheReadTokens: 5})
}

// --- 5. REGRESSÃO (liga a AOS-060): prefixo quebrado -> cache-read cai -> SLI
// desce -> alerta dispara. ---

// buildView constrói uma PromptView com o prefixo e o tail dados.
func buildView(turn int, prefix, tail []byte) agentruntime.PromptView {
	mat := append(append([]byte{}, prefix...), tail...)
	return agentruntime.PromptView{Turn: turn, Prefix: prefix, Materialized: mat}
}

func TestRegression_BrokenPrefix_DropsSLI_FiresAlert(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID, tenant = "run-thrash", "board-eu"

	guard := layout.NewGuard(layout.NewMemoryLedger())
	alerts := &cache_sli.MemoryAlertSink{}
	metrics := &cache_sli.MemoryMetricSink{}
	rec := cache_sli.NewRecorder(
		cache_sli.WithAlertSink(alerts),
		cache_sli.WithMetricSink(metrics),
		cache_sli.WithClock(fixedClock()),
	)

	prefix := []byte("=== SYSTEM ===\nassist\n=== TOOLSET (frozen) ===\ntool\tsearch\n=== CONTEXT ===\n")
	const toolSetHash = "sha256:frozen-abc"

	// Turno 1 (montagem estável): pina o prefixo. É um cache-WRITE (popular a cache):
	// read baixo, mas isolado; o warmup a seguir traz o SLI para cima.
	if err := guard.Admit(layout.Turn{RunID: runID, Index: 1, ToolSetHash: toolSetHash, View: buildView(1, prefix, []byte("t1"))}); err != nil {
		t.Fatalf("turno 1 (pin) devia ser admitido: %v", err)
	}
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: runID, Tenant: tenant, Region: "eu", PromptTokens: 100, CacheReadTokens: 95, CacheWriteTokens: 5})

	// Turnos 2 e 3 (prefixo BYTE-IDÊNTICO, tail estende): cache HIT alto -> SLI saudável.
	for i, tail := range [][]byte{[]byte("t1-t2"), []byte("t1-t2-t3")} {
		turn := i + 2
		if err := guard.Admit(layout.Turn{RunID: runID, Index: turn, ToolSetHash: toolSetHash, View: buildView(turn, prefix, tail)}); err != nil {
			t.Fatalf("turno estavel %d devia ser admitido (prefixo identico): %v", turn, err)
		}
		rec.Observe(ctx, nil, cache_sli.Sample{RunID: runID, Tenant: tenant, Region: "eu", PromptTokens: 100, CacheReadTokens: 98, CacheWriteTokens: 2})
	}

	// Antes da quebra: SLI saudável (>= 80%), sem alerta.
	if rate, _ := rec.RateFor(cache_sli.Key{RunID: runID, Tenant: tenant}); rate < cache_sli.DefaultThreshold {
		t.Fatalf("pre-quebra: SLI devia estar saudavel, got %v", rate)
	}
	if alerts.Len() != 0 {
		t.Fatalf("pre-quebra: nao devia haver alerta, len=%d", alerts.Len())
	}

	// QUEBRA DO PREFIXO (AOS-060): uma montagem reordena/altera o prefixo imutável.
	// A guarda de layout REJEITA com KindPrefixReordered — a prova de que o cache
	// thrash foi detectado a montante.
	brokenPrefix := []byte("=== SYSTEM ===\nassist\n=== TOOLSET (frozen) ===\ntool\tOUTRA\n=== CONTEXT ===\n")
	err := guard.Admit(layout.Turn{RunID: runID, Index: 4, ToolSetHash: toolSetHash, View: buildView(4, brokenPrefix, []byte("t1-t2-t3-t4"))})
	if err == nil {
		t.Fatal("montagem com prefixo quebrado devia ser rejeitada pela guarda (AOS-060)")
	}
	var v *layout.Violation
	if !errors.As(err, &v) || v.Kind != layout.KindPrefixReordered {
		t.Fatalf("esperava KindPrefixReordered, got %v", err)
	}

	// Consequência no provider: o prefixo mudou -> NENHUM cache-read (cache miss) ->
	// a chamada re-processa todo o prompt. O SLI agregado do run DESCE e o alerta dispara.
	rd := rec.Observe(ctx, nil, cache_sli.Sample{RunID: runID, Tenant: tenant, Region: "eu", PromptTokens: 300, CacheReadTokens: 0, CacheWriteTokens: 300})

	rate, defined := rec.RateFor(cache_sli.Key{RunID: runID, Tenant: tenant})
	if !defined || rate >= cache_sli.DefaultThreshold {
		t.Fatalf("pos-quebra: SLI devia descer abaixo de %v, got %v", cache_sli.DefaultThreshold, rate)
	}
	if !rd.Alerted || alerts.Len() != 1 {
		t.Fatalf("pos-quebra: o alerta devia disparar (alerted=%v len=%d)", rd.Alerted, alerts.Len())
	}
	a := alerts.Alerts()[0]
	if a.Key != (cache_sli.Key{RunID: runID, Tenant: tenant}) || a.Rate >= cache_sli.DefaultThreshold {
		t.Errorf("alerta mal-formado: %+v", a)
	}
}

// --- 5b. Saneamento do agregado (defesa-em-profundidade no eixo do alerta) ---

// TestAggregate_OverReport_DoesNotInflateNorSuppressAlert: um provider a reportar
// CacheReadTokens > PromptTokens (inconsistência) NÃO deve inflar o agregado acima
// de 1.0 nem suprimir o alerta (fail-silent). Espelha o clamp de CallRate no eixo
// do alerta (agregado).
func TestAggregate_OverReport_DoesNotInflateNorSuppressAlert(t *testing.T) {
	t.Parallel()
	alerts := &cache_sli.MemoryAlertSink{}
	rec := cache_sli.NewRecorder(cache_sli.WithAlertSink(alerts), cache_sli.WithClock(fixedClock()))
	ctx := context.Background()
	k := cache_sli.Key{RunID: "r", Tenant: "t"}

	// Sobre-report grosseiro: read (1000) >> prompt (100). callRate é fixado a 1.0;
	// o agregado NÃO pode passar de 1.0 (senão um miss posterior fica mascarado).
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 1000})
	if rate, ok := rec.RateFor(k); !ok || rate != 1.0 {
		t.Fatalf("agregado sobre-reportado devia ser fixado a 1.0, got %v (ok=%v)", rate, ok)
	}
	agg, _ := rec.Snapshot(k)
	if agg.CacheReadTokens != 100 {
		t.Errorf("numerador acumulado devia ser saneado a PromptTokens(100), got %d", agg.CacheReadTokens)
	}

	// Agora um miss total genuíno: read=0 em 100 prompt. O agregado real é 100/200 =
	// 0.5 < 0.80 -> o alerta DEVE disparar (não mascarado pelo numerador inflado).
	rd := rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 0})
	if got, _ := rec.RateFor(k); got != 0.5 {
		t.Fatalf("agregado pos-miss devia ser 0.5, got %v", got)
	}
	if !rd.Alerted || alerts.Len() != 1 {
		t.Errorf("miss apos sobre-report devia disparar alerta (nao mascarado): alerted=%v len=%d", rd.Alerted, alerts.Len())
	}
}

// TestAggregate_NegativeRead_DoesNotPoisonNorSpuriousAlert: um read NEGATIVO
// (provider defeituoso) é saneado a 0 antes de acumular — não deprime o agregado
// abaixo de 0, não dispara alerta espúrio, e não envenena as outras amostras da
// mesma chave.
func TestAggregate_NegativeRead_DoesNotPoisonNorSpuriousAlert(t *testing.T) {
	t.Parallel()
	alerts := &cache_sli.MemoryAlertSink{}
	rec := cache_sli.NewRecorder(cache_sli.WithAlertSink(alerts), cache_sli.WithClock(fixedClock()))
	ctx := context.Background()
	k := cache_sli.Key{RunID: "r", Tenant: "t"}

	// Amostra saudável primeiro (100% hit).
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 100})
	// Amostra com read negativo: saneada a 0 -> contribui 0/100, não -90.
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: -90})

	agg, _ := rec.Snapshot(k)
	if agg.CacheReadTokens != 100 {
		t.Errorf("read negativo devia ser saneado a 0 (numerador=100), got %d", agg.CacheReadTokens)
	}
	// Agregado real = 100/200 = 0.5 (nao negativo, nao envenenado).
	if rate, _ := rec.RateFor(k); rate != 0.5 {
		t.Errorf("agregado com read negativo saneado devia ser 0.5, got %v", rate)
	}
	// O alerta em 0.5 < 0.80 é legítimo (miss real), mas causado pela AUSÊNCIA de
	// hit, não por um numerador negativo espúrio: exactamente 1 alerta na transição.
	if alerts.Len() != 1 {
		t.Errorf("esperava exactamente 1 alerta (transicao legitima), got %d", alerts.Len())
	}
}

// TestAggregateRate_Clamp: defesa-em-profundidade em Aggregate.Rate() — um
// acumulador construído com numerador negativo ou sobre-reportado (ex.: estado
// corrompido/importado) é fixado a [0,1], nunca produz rate negativo nem > 1.
func TestAggregateRate_Clamp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		agg  cache_sli.Aggregate
		want float64
	}{
		{"read negativo -> 0", cache_sli.Aggregate{CacheReadTokens: -50, PromptTokens: 100}, 0.0},
		{"read > prompt -> 1", cache_sli.Aggregate{CacheReadTokens: 500, PromptTokens: 100}, 1.0},
		{"normal", cache_sli.Aggregate{CacheReadTokens: 90, PromptTokens: 100}, 0.9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rate, defined := tt.agg.Rate()
			if !defined || rate != tt.want {
				t.Errorf("Rate() = %v (defined=%v), quer %v", rate, defined, tt.want)
			}
		})
	}
	// PromptTokens<=0 continua indefinido.
	if _, defined := (cache_sli.Aggregate{CacheReadTokens: 10, PromptTokens: 0}).Rate(); defined {
		t.Errorf("PromptTokens==0 devia ser indefinido")
	}
}

// --- 5c. Chave incompleta (RunID/Tenant vazios): não polui um balde global ---

// TestIncompleteKey_NotAggregated_NoAlert_MetricFlagged: chamadas sem RunID ou sem
// Tenant NÃO agregam para Key{"",""} (que misturaria runs/tenants distintos) nem
// avaliam alerta; emitem só a métrica DA CHAMADA, sinalizada como incompleta.
func TestIncompleteKey_NotAggregated_NoAlert_MetricFlagged(t *testing.T) {
	t.Parallel()
	alerts := &cache_sli.MemoryAlertSink{}
	metrics := &cache_sli.MemoryMetricSink{}
	rec := cache_sli.NewRecorder(
		cache_sli.WithAlertSink(alerts),
		cache_sli.WithMetricSink(metrics),
		cache_sli.WithClock(fixedClock()),
	)
	ctx := context.Background()

	// Dois "runs" distintos, ambos SEM RunID (só Tenant), com um miss total cada:
	// se colidissem em Key{"", tenant}? Não — Tenant vazio? Testamos os dois eixos.
	// Caso A: RunID vazio.
	rdA := rec.Observe(ctx, nil, cache_sli.Sample{RunID: "", Tenant: "t", PromptTokens: 100, CacheReadTokens: 0})
	// Caso B: Tenant vazio.
	rdB := rec.Observe(ctx, nil, cache_sli.Sample{RunID: "r", Tenant: "", PromptTokens: 100, CacheReadTokens: 0})

	for _, rd := range []cache_sli.Reading{rdA, rdB} {
		if !rd.IncompleteKey {
			t.Errorf("chave incompleta devia ser sinalizada: %+v", rd)
		}
		if rd.Alerted || rd.AggregateDefined {
			t.Errorf("chave incompleta nao devia alertar nem agregar: %+v", rd)
		}
		if !rd.CallDefined || rd.CallRate != 0 {
			t.Errorf("a metrica da chamada devia ser calculada (0%%): %+v", rd)
		}
	}

	// Nada foi agregado: os baldes incompletos não existem.
	if _, ok := rec.Snapshot(cache_sli.Key{RunID: "", Tenant: "t"}); ok {
		t.Errorf("Key{\"\",t} nao devia ter agregado")
	}
	if _, ok := rec.Snapshot(cache_sli.Key{RunID: "r", Tenant: ""}); ok {
		t.Errorf("Key{r,\"\"} nao devia ter agregado")
	}
	// Zero alertas de baldes incompletos.
	if alerts.Len() != 0 {
		t.Errorf("chaves incompletas nao deviam disparar alerta, got %d", alerts.Len())
	}
	// Cada chamada emitiu a métrica DA CHAMADA, marcada como incompleta, e NUNCA a
	// métrica de escopo agregado.
	ms := metrics.Metrics()
	if len(ms) != 2 {
		t.Fatalf("esperava 2 metricas (uma por chamada), got %d", len(ms))
	}
	for _, m := range ms {
		if m.Attributes[cache_sli.AttrScope] != "call" {
			t.Errorf("chave incompleta so devia emitir metrica de escopo 'call', got %v", m.Attributes[cache_sli.AttrScope])
		}
		if m.Attributes[cache_sli.AttrIncompleteKey] != true {
			t.Errorf("metrica de chave incompleta devia levar %s=true, got %+v", cache_sli.AttrIncompleteKey, m.Attributes)
		}
	}
}

// TestCompleteKey_NoIncompleteFlag: uma chave completa NÃO leva o atributo de
// incompleta (não deve poluir a métrica normal).
func TestCompleteKey_NoIncompleteFlag(t *testing.T) {
	t.Parallel()
	metrics := &cache_sli.MemoryMetricSink{}
	rec := cache_sli.NewRecorder(cache_sli.WithMetricSink(metrics), cache_sli.WithClock(fixedClock()))
	rec.Observe(context.Background(), nil, cache_sli.Sample{RunID: "r", Tenant: "t", PromptTokens: 100, CacheReadTokens: 90})
	for _, m := range metrics.Metrics() {
		if _, ok := m.Attributes[cache_sli.AttrIncompleteKey]; ok {
			t.Errorf("chave completa nao devia levar %s: %+v", cache_sli.AttrIncompleteKey, m.Attributes)
		}
	}
}

// --- 6. Concorrência (-race) ---

func TestConcurrent_Observe_Race(t *testing.T) {
	t.Parallel()
	rec := cache_sli.NewRecorder(
		cache_sli.WithAlertSink(&cache_sli.MemoryAlertSink{}),
		cache_sli.WithMetricSink(&cache_sli.MemoryMetricSink{}),
		cache_sli.WithClock(fixedClock()),
	)
	ctx := context.Background()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			run := "run" + string(rune('A'+g%3))
			for i := 0; i < 50; i++ {
				rec.Observe(ctx, nil, cache_sli.Sample{
					RunID: run, Tenant: "t", PromptTokens: 100, CacheReadTokens: int64(i % 101),
				})
			}
		}(g)
	}
	wg.Wait()
}
