package otelgenai

import (
	"math"
	"testing"
	"time"
)

// --- helpers de construção de spans para os SLIs (reusam traceID/spanID/chatSpan
// de cost_aggregation_test.go) -----------------------------------------------

// chatCacheCost é um span `chat` que carrega o cache-hit-rate agregado (como o
// Model Gateway o anota, [AttrCacheHitRate]) mais tokens/custo — a fonte real do
// SLI de cache-hit e do de custo por trajectória.
func chatCacheCost(traceB, spanB byte, inTok int64, cacheRate float64, costMicro int64) SpanData {
	sd := chatSpan(traceB, spanB, 0, inTok, 0, costMicro)
	sd.Attributes = append(sd.Attributes, KeyValue{Key: AttrCacheHitRate, Value: cacheRate})
	return sd
}

// execToolLat constrói um span execute_tool com uma latência (o overhead de
// mediação = End-Start) e uma decisão de mediação anotada.
func execToolLat(traceB, spanB byte, latency time.Duration, decision string) SpanData {
	attrs := []KeyValue{{Key: AttrOperationName, Value: OpExecuteTool}}
	if decision != "" {
		attrs = append(attrs, KeyValue{Key: AttrDecision, Value: decision})
	}
	return SpanData{
		Name:          OpExecuteTool,
		SpanContext:   SpanContext{TraceID: traceID(traceB), SpanID: spanID(spanB)},
		StartUnixNano: 1_000_000,
		EndUnixNano:   1_000_000 + latency.Nanoseconds(),
		Attributes:    attrs,
	}
}

func traceHex(traceB byte) string { return SpanContext{TraceID: traceID(traceB)}.TraceIDHex() }

// --- SLOConfig fail-closed ---------------------------------------------------

func TestDefaultSLOConfigIsValidAndAlignedToDrivers(t *testing.T) {
	c := DefaultSLOConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("DefaultSLOConfig deve ser válida: %v", err)
	}
	if c.CacheHitRateTarget != 0.80 {
		t.Errorf("cache target = %v, quer 0.80 (ADR-009)", c.CacheHitRateTarget)
	}
	if c.MediationOverheadP95MaxNanos != int64(15*time.Millisecond) {
		t.Errorf("overhead p95 max = %d, quer 15ms", c.MediationOverheadP95MaxNanos)
	}
}

func TestLoadSLOConfigValidJSON(t *testing.T) {
	raw := []byte(`{
		"version":"2.1.0",
		"cache_hit_rate_target":0.9,
		"mediation_overhead_p95_max_nanos":10000000,
		"cost_per_trajectory_max_micro_usd":250000,
		"override_rate_max":0.05
	}`)
	c, err := LoadSLOConfig(raw)
	if err != nil {
		t.Fatalf("config válida rejeitada: %v", err)
	}
	if c.Version != "2.1.0" || c.CacheHitRateTarget != 0.9 {
		t.Errorf("config mal desserializada: %+v", c)
	}
}

func TestLoadSLOConfigFailClosed(t *testing.T) {
	cases := map[string]string{
		"SemVer inválido":       `{"version":"v1","cache_hit_rate_target":0.8,"mediation_overhead_p95_max_nanos":1,"cost_per_trajectory_max_micro_usd":1,"override_rate_max":0.1}`,
		"cache fora de gama":    `{"version":"1.0.0","cache_hit_rate_target":1.5,"mediation_overhead_p95_max_nanos":1,"cost_per_trajectory_max_micro_usd":1,"override_rate_max":0.1}`,
		"overhead não-positivo": `{"version":"1.0.0","cache_hit_rate_target":0.8,"mediation_overhead_p95_max_nanos":0,"cost_per_trajectory_max_micro_usd":1,"override_rate_max":0.1}`,
		"custo não-positivo":    `{"version":"1.0.0","cache_hit_rate_target":0.8,"mediation_overhead_p95_max_nanos":1,"cost_per_trajectory_max_micro_usd":-5,"override_rate_max":0.1}`,
		"override fora de gama": `{"version":"1.0.0","cache_hit_rate_target":0.8,"mediation_overhead_p95_max_nanos":1,"cost_per_trajectory_max_micro_usd":1,"override_rate_max":2}`,
		"JSON malformado":       `{not json`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadSLOConfig([]byte(raw)); err == nil {
				t.Fatalf("config inválida (%s) devia ser rejeitada fail-closed", name)
			}
		})
	}
}

func TestBuildDashboardRejectsInvalidConfigFailClosed(t *testing.T) {
	bad := SLOConfig{Version: "nope"}
	d := BuildDashboard(nil, nil, bad)
	if d.Config.Version != DefaultSLOConfig().Version {
		t.Fatalf("config inválida devia cair na default, veio %+v", d.Config)
	}
	// O fallback tem de ser OBSERVÁVEL (não silencioso): um consumidor/alerta detecta
	// que a config estrita foi rejeitada em vez de correr com os limiares errados.
	if !d.ConfigDefaulted {
		t.Fatal("ConfigDefaulted devia ser true quando a config passada é inválida")
	}
	// Uma config VÁLIDA não marca o fallback.
	ok := BuildDashboard(nil, nil, DefaultSLOConfig())
	if ok.ConfigDefaulted {
		t.Fatal("ConfigDefaulted devia ser false para uma config válida")
	}
}

func TestValidSemVer(t *testing.T) {
	ok := []string{"0.0.0", "1.0.0", "10.20.30", "1.2.3-alpha", "1.2.3+build", "1.2.3-rc.1+build.5"}
	for _, s := range ok {
		if !validSemVer(s) {
			t.Errorf("%q devia ser SemVer válido", s)
		}
	}
	bad := []string{"", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "v1.2.3", "a.b.c", "1.2.x"}
	for _, s := range bad {
		if validSemVer(s) {
			t.Errorf("%q NÃO devia ser SemVer válido", s)
		}
	}
}

// --- Percentil determinista --------------------------------------------------

func TestPercentileNanos(t *testing.T) {
	if got := percentileNanos(nil, 95); got != 0 {
		t.Errorf("p95 de vazio = %d, quer 0", got)
	}
	if got := percentileNanos([]int64{42}, 95); got != 42 {
		t.Errorf("p95 de um elemento = %d, quer 42", got)
	}
	xs := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	// type-7 p95: rank 0.95*9 = 8.55 → 9 + 0.55*(10-9) = 9.55 → round 10.
	if got := percentileNanos(xs, 95); got != 10 {
		t.Errorf("p95 de 1..10 = %d, quer 10", got)
	}
	// p50: rank 0.5*9=4.5 → 5 + 0.5*(6-5) = 5.5 → round 6.
	if got := percentileNanos(xs, 50); got != 6 {
		t.Errorf("p50 = %d, quer 6", got)
	}
	unordered := []int64{10, 1, 9, 2, 8, 3, 7, 4, 6, 5}
	if got := percentileNanos(unordered, 95); got != 10 {
		t.Errorf("p95 (desordenado) = %d, quer 10", got)
	}
	if unordered[0] != 10 {
		t.Errorf("percentileNanos mutou o input")
	}
}

// --- Integração: run conhecido → SLIs esperados (calculados à mão) ------------

func TestDashboardReflectsKnownRunSLIs(t *testing.T) {
	// Run conhecido (3 trajectórias A/B/C):
	//  A: chat cache 0.9 / 2000 prompt tok / custo 100_000; execute_tool 5ms permit.
	//  B: chat cache 0.7 / 1000 prompt tok / custo 300_000; execute_tool 20ms escalate.
	//  C: chat cache 0.95 / 1000 prompt tok / custo 50_000; execute_tool 10ms permit.
	spans := []SpanData{
		chatCacheCost(0xA, 1, 2000, 0.9, 100_000),
		execToolLat(0xA, 2, 5*time.Millisecond, DecisionPermit),
		chatCacheCost(0xB, 3, 1000, 0.7, 300_000),
		execToolLat(0xB, 4, 20*time.Millisecond, DecisionEscalate),
		chatCacheCost(0xC, 5, 1000, 0.95, 50_000),
		execToolLat(0xC, 6, 10*time.Millisecond, DecisionPermit),
	}
	d := BuildDashboardFromSpans(spans, DefaultSLOConfig())

	// (1) Cache-hit ponderado por prompt tokens:
	//     (0.9*2000 + 0.7*1000 + 0.95*1000)/4000 = 3450/4000 = 0.8625.
	if math.Abs(d.CacheHitRate.Value-0.8625) > 1e-9 {
		t.Errorf("cache-hit SLI = %v, quer 0.8625", d.CacheHitRate.Value)
	}
	if !d.CacheHitRate.Met {
		t.Errorf("cache-hit devia cumprir (0.8625 >= 0.80)")
	}
	if d.CacheHitRate.Samples != 3 {
		t.Errorf("cache samples = %d, quer 3", d.CacheHitRate.Samples)
	}

	// (2) Overhead p95 de {5,10,20}ms: rank 0.95*2=1.9 → 10 + 0.9*(20-10)=19ms.
	if math.Abs(d.MediationOverheadP95.Value-float64(19*time.Millisecond)) > 1 {
		t.Errorf("overhead p95 = %v, quer 19ms", d.MediationOverheadP95.Value)
	}
	if d.MediationOverheadP95.Met { // 19ms > 15ms
		t.Errorf("overhead p95 (19ms) devia violar o SLO de 15ms")
	}

	// (3) Custo por trajectória: A=100k, B=300k, C=50k ⇒ pior 300_000; tecto 500k ⇒ cumpre.
	if d.CostPerTrajectory.Value != 300_000 {
		t.Errorf("custo (pior trajectória) = %v, quer 300000", d.CostPerTrajectory.Value)
	}
	if !d.CostPerTrajectory.Met {
		t.Errorf("custo devia cumprir (300000 <= 500000)")
	}
	if d.CostPerTrajectory.Samples != 3 {
		t.Errorf("custo samples (traces) = %d, quer 3", d.CostPerTrajectory.Samples)
	}

	// (4) Override-rate: 3 decisões, 1 escalate ⇒ 1/3; tecto 0.10 ⇒ violado.
	if math.Abs(d.OverrideRate.Value-(1.0/3.0)) > 1e-9 {
		t.Errorf("override-rate = %v, quer 0.3333", d.OverrideRate.Value)
	}
	if d.OverrideRate.Met {
		t.Errorf("override-rate (0.333) devia violar o SLO de 0.10")
	}
}

// --- Verificação: drill-down de um SLI DEGRADADO chega ao trace responsável ---

func TestDrillDownDegradedSLIReachesOffendingTrace(t *testing.T) {
	const good, bad byte = 0x1, 0x2
	spans := []SpanData{
		// trace bom: cache alto, overhead baixo, custo baixo, permit.
		chatCacheCost(good, 1, 1000, 0.95, 10_000),
		execToolLat(good, 2, 2*time.Millisecond, DecisionPermit),
		// trace mau: cache thrash (0.10), overhead alto (50ms), custo alto (900k), escalate.
		chatCacheCost(bad, 3, 1000, 0.10, 900_000),
		execToolLat(bad, 4, 50*time.Millisecond, DecisionEscalate),
	}
	d := BuildDashboardFromSpans(spans, DefaultSLOConfig())

	if len(d.Breaches()) == 0 {
		t.Fatal("esperava breaches (cache thrash + overhead + custo + override)")
	}
	badHex := traceHex(bad)

	assertOffender := func(name string, s SLIValue) {
		if s.Met {
			t.Errorf("%s devia estar degradado", name)
			return
		}
		if len(s.Offenders) == 0 {
			t.Errorf("%s degradado sem drill-down (offenders vazio)", name)
			return
		}
		found := false
		for _, o := range s.Offenders {
			if o == badHex {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: drill-down não apontou o trace ofensor %s (veio %v)", name, badHex, s.Offenders)
		}
	}

	assertOffender("cache-hit", d.CacheHitRate)
	assertOffender("overhead", d.MediationOverheadP95)
	assertOffender("custo", d.CostPerTrajectory)
	assertOffender("override", d.OverrideRate)

	// O trace bom NUNCA aparece como ofensor de custo.
	goodHex := traceHex(good)
	for _, o := range d.CostPerTrajectory.Offenders {
		if o == goodHex {
			t.Errorf("trace bom apareceu como ofensor de custo")
		}
	}
}

// --- Semântica "sem dados": nem breach nem cumprimento por vacuidade ---------

func TestSLINoDataIsNotBreach(t *testing.T) {
	d := BuildDashboard(nil, nil, DefaultSLOConfig())
	for _, s := range d.SLIs() {
		if s.Evaluated() {
			t.Errorf("%s não devia estar avaliado sem dados", s.Name)
		}
		if s.Breached() {
			t.Errorf("%s não devia estar em breach sem dados", s.Name)
		}
	}
	if len(d.Breaches()) != 0 {
		t.Errorf("sem dados não deve haver breaches, veio %d", len(d.Breaches()))
	}
}

func TestOverrideRateDenominatorGuardedAgainstZero(t *testing.T) {
	events := []WideEvent{{Operation: OpChat, TraceIDHex: "abc"}}
	sli := overrideRateSLI(events, 0.10)
	if sli.Evaluated() {
		t.Errorf("override-rate sem decisões não devia estar avaliado")
	}
	if sli.Value != 0 {
		t.Errorf("override-rate sem decisões = %v, quer 0", sli.Value)
	}
}

func TestCacheHitUnweightedFallbackWhenNoTokens(t *testing.T) {
	events := []WideEvent{
		{Operation: OpChat, TraceIDHex: "t1", Attributes: map[string]any{AttrCacheHitRate: 0.6}},
		{Operation: OpChat, TraceIDHex: "t2", Attributes: map[string]any{AttrCacheHitRate: 0.8}},
	}
	sli := cacheHitRateSLI(events, 0.80)
	if math.Abs(sli.Value-0.7) > 1e-9 {
		t.Errorf("cache-hit não-ponderado = %v, quer 0.7", sli.Value)
	}
	if sli.Met { // 0.7 < 0.80
		t.Errorf("cache-hit 0.7 devia violar o SLO 0.80")
	}
}

// --- Breaches é a superfície estável consumida por AOS-086 -------------------

func TestBreachesSurfaceForAlerts(t *testing.T) {
	const bad byte = 0x9
	spans := []SpanData{
		chatCacheCost(bad, 1, 1000, 0.5, 10_000),
		execToolLat(bad, 2, 1*time.Millisecond, DecisionPermit),
	}
	d := BuildDashboardFromSpans(spans, DefaultSLOConfig())
	// Só o cache-hit (0.5 < 0.80) está em breach; overhead/custo/override cumprem.
	breaches := d.Breaches()
	if len(breaches) != 1 || breaches[0].Name != SLICacheHitRate {
		t.Fatalf("esperava só cache-hit em breach, veio %+v", breaches)
	}
	if breaches[0].SLO != 0.80 || breaches[0].Direction != DirMin {
		t.Errorf("breach mal formado para AOS-086: %+v", breaches[0])
	}
}
