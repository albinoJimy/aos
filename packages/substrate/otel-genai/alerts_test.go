package otelgenai

import (
	"reflect"
	"testing"
	"time"
)

// --- helpers -----------------------------------------------------------------

// findAlert devolve o alerta com o nome dado (e se existe).
func findAlert(alerts []Alert, name string) (Alert, bool) {
	for _, a := range alerts {
		if a.Name == name {
			return a, true
		}
	}
	return Alert{}, false
}

// degradedDashboard produz um snapshot com os QUATRO SLIs em breach (cache thrash,
// custo acima do orçamento, overhead p95 alto, override-rate alto), a partir de
// spans sintéticos — a fonte real de AOS-085.
func degradedDashboard() DashboardSnapshot {
	const bad byte = 0x7
	spans := []SpanData{
		// cache thrash (0.10 < 0.80), custo 900k > 500k tecto.
		chatCacheCost(bad, 1, 1000, 0.10, 900_000),
		// overhead 50ms > 15ms, decisão escalate (override).
		execToolLat(bad, 2, 50*time.Millisecond, DecisionEscalate),
	}
	return BuildDashboardFromSpans(spans, DefaultSLOConfig())
}

// healthyDashboard produz um snapshot com todos os SLIs a cumprir.
func healthyDashboard() DashboardSnapshot {
	const good byte = 0x3
	spans := []SpanData{
		chatCacheCost(good, 1, 1000, 0.95, 10_000),
		execToolLat(good, 2, 2*time.Millisecond, DecisionPermit),
	}
	return BuildDashboardFromSpans(spans, DefaultSLOConfig())
}

// --- Config fail-closed ------------------------------------------------------

func TestDefaultAlertConfigIsValidAndCoversFourSLOs(t *testing.T) {
	c := DefaultAlertConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("DefaultAlertConfig deve ser válida: %v", err)
	}
	want := map[string]Severity{
		AlertCacheHitRateLow:       SevWarning,
		AlertCostPerTrajectoryHigh: SevCritical,
		AlertMediationOverheadHigh: SevCritical,
		AlertOverrideRateHigh:      SevWarning,
	}
	if len(c.Rules) != len(want) {
		t.Fatalf("esperava %d regras, veio %d", len(want), len(c.Rules))
	}
	for _, r := range c.Rules {
		sev, ok := want[r.Name]
		if !ok {
			t.Errorf("regra inesperada %q", r.Name)
			continue
		}
		if r.Severity != sev {
			t.Errorf("%s: severidade %q, quer %q", r.Name, r.Severity, sev)
		}
		if r.Route.Zero() {
			t.Errorf("%s: sem rota", r.Name)
		}
	}
}

func TestAlertConfigValidateFailClosed(t *testing.T) {
	base := DefaultAlertConfig()
	cases := map[string]func(*AlertConfig){
		"SemVer inválido":     func(c *AlertConfig) { c.Version = "v1" },
		"janela < 1":          func(c *AlertConfig) { c.SustainedWindows = 0 },
		"sem regras":          func(c *AlertConfig) { c.Rules = nil },
		"SLI desconhecido":    func(c *AlertConfig) { c.Rules[0].SLI = "inexistente" },
		"sem nome":            func(c *AlertConfig) { c.Rules[0].Name = "" },
		"severidade inválida": func(c *AlertConfig) { c.Rules[0].Severity = "urgente" },
		"sem rota":            func(c *AlertConfig) { c.Rules[0].Route = Route{} },
		"nome duplicado":      func(c *AlertConfig) { c.Rules[1].Name = c.Rules[0].Name },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			c := DefaultAlertConfig()
			// cópia profunda das regras para não partilhar o slice entre casos.
			c.Rules = append([]AlertRule(nil), base.Rules...)
			mut(&c)
			if err := c.Validate(); err == nil {
				t.Fatalf("config inválida (%s) devia ser rejeitada fail-closed", name)
			}
		})
	}
}

func TestEvaluateAlertsFallsBackToDefaultOnInvalidConfig(t *testing.T) {
	d := degradedDashboard()
	alerts := EvaluateAlerts(d, AlertConfig{Version: "nope"})
	// A default tem 4 regras; a config inválida foi substituída (fail-closed).
	if len(alerts) != 4 {
		t.Fatalf("config inválida devia cair na default (4 regras), veio %d", len(alerts))
	}
}

// --- (1) Violação sintética dispara o alerta com a severidade certa ----------

func TestSyntheticBreachFiresExpectedAlertsWithSeverity(t *testing.T) {
	d := degradedDashboard()
	alerts := EvaluateAlerts(d, DefaultAlertConfig())
	fired := FiredAlerts(alerts)
	if len(fired) != 4 {
		t.Fatalf("esperava os 4 alertas críticos disparados, veio %d: %+v", len(fired), fired)
	}
	wantSev := map[string]Severity{
		AlertCacheHitRateLow:       SevWarning,
		AlertCostPerTrajectoryHigh: SevCritical,
		AlertMediationOverheadHigh: SevCritical,
		AlertOverrideRateHigh:      SevWarning,
	}
	for name, sev := range wantSev {
		a, ok := findAlert(fired, name)
		if !ok {
			t.Errorf("alerta %q não disparou", name)
			continue
		}
		if a.Severity != sev {
			t.Errorf("%s: severidade %q, quer %q", name, a.Severity, sev)
		}
		// O alerta traz valor observado e limiar (accionável, sem segredos).
		if a.Threshold == 0 {
			t.Errorf("%s: limiar ausente no alerta", name)
		}
	}
}

// Cache thrash e explosão de custo alertam ANTES do impacto: bastam os SLIs a
// cruzar o limiar (observação única) — não é preciso esperar acumulação.
func TestCacheThrashAndCostExplosionAlertOnThresholdCross(t *testing.T) {
	d := degradedDashboard()
	alerts := EvaluateAlerts(d, DefaultAlertConfig())
	for _, name := range []string{AlertCacheHitRateLow, AlertCostPerTrajectoryHigh} {
		a, ok := findAlert(alerts, name)
		if !ok || !a.Fired {
			t.Errorf("%s devia disparar ao cruzar o limiar (antes do impacto)", name)
		}
	}
}

func TestHealthyDashboardFiresNothing(t *testing.T) {
	d := healthyDashboard()
	if fired := FiredAlerts(EvaluateAlerts(d, DefaultAlertConfig())); len(fired) != 0 {
		t.Errorf("dashboard saudável não devia disparar alertas, veio %+v", fired)
	}
}

// --- (2) Encaminhamento chega ao runbook/owner correcto ----------------------

func TestAlertRoutingReachesCorrectRunbook(t *testing.T) {
	d := degradedDashboard()
	alerts := EvaluateAlerts(d, DefaultAlertConfig())
	wantRoute := map[string]Route{
		AlertCacheHitRateLow:       {Runbook: "RB-03", Owner: "DevOps/SRE"},
		AlertCostPerTrajectoryHigh: {Runbook: "RB-03", Owner: "DevOps/SRE"},
		AlertMediationOverheadHigh: {Runbook: "RB-04", Owner: "DevOps/SRE"},
		AlertOverrideRateHigh:      {Runbook: "RB-05", Owner: "Governação/Segurança"},
	}
	for name, route := range wantRoute {
		a, ok := findAlert(alerts, name)
		if !ok {
			t.Errorf("alerta %q ausente", name)
			continue
		}
		if a.Route != route {
			t.Errorf("%s: rota %+v, quer %+v", name, a.Route, route)
		}
	}
	// O drill-down chega ao runbook COM o trace ofensor.
	badHex := traceHex(0x7)
	cost, _ := findAlert(alerts, AlertCostPerTrajectoryHigh)
	found := false
	for _, o := range cost.Offenders {
		if o == badHex {
			found = true
		}
	}
	if !found {
		t.Errorf("alerta de custo sem o trace ofensor no drill-down: %v", cost.Offenders)
	}
}

func TestEvaluatorExposesConfigAndRouting(t *testing.T) {
	cfg := DefaultAlertConfig()
	cfg.SustainedWindows = 5
	ev := NewAlertEvaluator(cfg)
	if ev.Config().SustainedWindows != 5 {
		t.Errorf("Config() não devolveu a config activa: %+v", ev.Config())
	}
	if r, ok := ev.RouteFor(AlertCostPerTrajectoryHigh); !ok || r.Runbook != "RB-03" {
		t.Errorf("RouteFor(custo) = %+v ok=%v, quer RB-03", r, ok)
	}
	if r, ok := ev.RouteFor("desconhecido"); ok || r != DefaultRoute {
		t.Errorf("RouteFor(desconhecido) devia cair na default fail-safe, veio %+v ok=%v", r, ok)
	}
}

func TestNewAlertEvaluatorFailClosedOnInvalidConfig(t *testing.T) {
	ev := NewAlertEvaluator(AlertConfig{Version: "nope"})
	// Fail-closed: cai na default (4 regras, janela 3).
	if got := ev.Config(); got.Version != DefaultAlertConfig().Version || len(got.Rules) != 4 {
		t.Fatalf("config inválida devia cair na default, veio %+v", got)
	}
	// O fallback tem de ser OBSERVÁVEL (simétrico ao ConfigDefaulted de AOS-085).
	if !ev.ConfigDefaulted() {
		t.Fatal("ConfigDefaulted() devia ser true quando a config passada é inválida")
	}
	// Uma config válida não marca o fallback.
	if NewAlertEvaluator(DefaultAlertConfig()).ConfigDefaulted() {
		t.Fatal("ConfigDefaulted() devia ser false para uma config válida")
	}
}

func TestRouteForFallsBackToDefaultFailSafe(t *testing.T) {
	c := DefaultAlertConfig()
	// Alerta conhecido → rota configurada.
	if r, ok := c.RouteFor(AlertMediationOverheadHigh); !ok || r.Runbook != "RB-04" {
		t.Errorf("RouteFor(overhead) = %+v ok=%v, quer RB-04", r, ok)
	}
	// Alerta desconhecido → DefaultRoute fail-safe (nunca órfão).
	r, ok := c.RouteFor("alerta_inexistente")
	if ok {
		t.Errorf("alerta desconhecido não devia ter rota configurada")
	}
	if r != DefaultRoute {
		t.Errorf("fail-safe = %+v, quer %+v", r, DefaultRoute)
	}
	// Uma regra com rota vazia também cai na default fail-safe.
	c2 := DefaultAlertConfig()
	c2.Rules[0].Route = Route{}
	if r, ok := c2.RouteFor(c2.Rules[0].Name); ok || r != DefaultRoute {
		t.Errorf("rota vazia devia cair na default fail-safe, veio %+v ok=%v", r, ok)
	}
}

// --- (3) Anti-ruído: transitória não dispara / sustentada dispara ------------

func TestTransientBreachDoesNotFireSustainedDoes(t *testing.T) {
	cfg := DefaultAlertConfig()
	cfg.SustainedWindows = 3
	ev := NewAlertEvaluator(cfg)
	bad := degradedDashboard()

	// Observação 1: breach mas streak=1 < 3 ⇒ NÃO dispara (pico transitório).
	a1 := ev.Observe(bad)
	if len(FiredAlerts(a1)) != 0 {
		t.Fatalf("1ª observação (transitória) não devia disparar, veio %+v", FiredAlerts(a1))
	}
	// Observação 2: streak=2 < 3 ⇒ ainda não.
	if len(FiredAlerts(ev.Observe(bad))) != 0 {
		t.Fatalf("2ª observação não devia disparar")
	}
	// Observação 3: streak=3 ⇒ SUSTENTADA, dispara.
	a3 := FiredAlerts(ev.Observe(bad))
	if len(a3) != 4 {
		t.Fatalf("3ª observação (sustentada) devia disparar os 4 alertas, veio %d", len(a3))
	}
	// O streak recupera: uma observação saudável ZERA e volta a exigir 3.
	if len(FiredAlerts(ev.Observe(healthyDashboard()))) != 0 {
		t.Fatalf("observação saudável devia zerar o streak (nada dispara)")
	}
	if len(FiredAlerts(ev.Observe(bad))) != 0 {
		t.Fatalf("após reset do streak, 1 breach não devia voltar a disparar")
	}
}

// Um breach INTERMITENTE (não consecutivo) nunca acumula até à janela ⇒ não dispara.
func TestIntermittentBreachNeverFires(t *testing.T) {
	cfg := DefaultAlertConfig()
	cfg.SustainedWindows = 3
	ev := NewAlertEvaluator(cfg)
	bad, good := degradedDashboard(), healthyDashboard()
	for i := 0; i < 10; i++ {
		var d DashboardSnapshot
		if i%2 == 0 {
			d = bad
		} else {
			d = good
		}
		if fired := FiredAlerts(ev.Observe(d)); len(fired) != 0 {
			t.Fatalf("breach intermitente disparou na iteração %d: %+v", i, fired)
		}
	}
}

// Duas regras que PARTILHAM o mesmo SLI (fan-out para rotas/severidades distintas na
// mesma condição de breach) têm streaks INDEPENDENTES: cada Observe avança cada streak
// UMA vez. Regressão: com o streak antes indexado por SLI, o breach era contado duas
// vezes e a janela sustentada disparava a meio (2 obs em vez de 3).
func TestSharedSLIRulesDoNotDoubleCountStreak(t *testing.T) {
	cfg := AlertConfig{
		Version:          "1.0.0",
		SustainedWindows: 3,
		Rules: []AlertRule{
			{SLI: SLICacheHitRate, Name: "cache_a", Severity: SevWarning, Route: Route{Runbook: "RB-03", Owner: "DevOps/SRE"}},
			{SLI: SLICacheHitRate, Name: "cache_b", Severity: SevCritical, Route: Route{Runbook: "RB-03", Owner: "Governação/Segurança"}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config com duas regras sobre o mesmo SLI devia ser válida: %v", err)
	}
	ev := NewAlertEvaluator(cfg)
	bad := degradedDashboard()

	// Obs 1 e 2: streak 1 e 2 < 3 ⇒ NADA dispara (sem double-count).
	for i := 1; i <= 2; i++ {
		alerts := ev.Observe(bad)
		if fired := FiredAlerts(alerts); len(fired) != 0 {
			t.Fatalf("observação %d não devia disparar (janela=3), veio %+v", i, fired)
		}
		for _, a := range alerts {
			if a.Streak != i {
				t.Errorf("obs %d: regra %q com streak %d, quer %d", i, a.Name, a.Streak, i)
			}
		}
	}
	// Obs 3: streak=3 ⇒ SUSTENTADA, ambas as regras disparam.
	if fired := FiredAlerts(ev.Observe(bad)); len(fired) != 2 {
		t.Fatalf("3ª observação (sustentada) devia disparar as 2 regras, veio %d", len(fired))
	}
}

// Com SustainedWindows=1 o avaliador dispara à primeira observação em breach.
func TestSustainedWindowOneFiresImmediately(t *testing.T) {
	cfg := DefaultAlertConfig()
	cfg.SustainedWindows = 1
	ev := NewAlertEvaluator(cfg)
	if len(FiredAlerts(ev.Observe(degradedDashboard()))) != 4 {
		t.Fatalf("janela=1 devia disparar à primeira observação")
	}
}

func TestResetClearsStreak(t *testing.T) {
	cfg := DefaultAlertConfig()
	cfg.SustainedWindows = 2
	ev := NewAlertEvaluator(cfg)
	bad := degradedDashboard()
	ev.Observe(bad) // streak=1
	ev.Reset()
	if len(FiredAlerts(ev.Observe(bad))) != 0 {
		t.Fatalf("após Reset, streak devia recomeçar do zero (1 breach não dispara com janela=2)")
	}
}

// --- (4) Determinismo: mesma sequência ⇒ mesmos alertas ----------------------

func TestEvaluateAlertsDeterministic(t *testing.T) {
	d := degradedDashboard()
	cfg := DefaultAlertConfig()
	a := EvaluateAlerts(d, cfg)
	b := EvaluateAlerts(d, cfg)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("EvaluateAlerts não-determinista:\n%+v\n%+v", a, b)
	}
	// Ordem estável por nome.
	for i := 1; i < len(a); i++ {
		if a[i-1].Name > a[i].Name {
			t.Errorf("alertas não ordenados por nome: %q antes de %q", a[i-1].Name, a[i].Name)
		}
	}
}

func TestSustainedEvaluatorDeterministicOverSequence(t *testing.T) {
	seq := []DashboardSnapshot{
		degradedDashboard(), healthyDashboard(), degradedDashboard(),
		degradedDashboard(), degradedDashboard(), healthyDashboard(),
	}
	run := func() [][]Alert {
		ev := NewAlertEvaluator(DefaultAlertConfig())
		var out [][]Alert
		for _, d := range seq {
			out = append(out, ev.Observe(d))
		}
		return out
	}
	if !reflect.DeepEqual(run(), run()) {
		t.Fatal("avaliador sustentado não-determinista sobre a mesma sequência")
	}
}

// --- Semântica "sem dados": um SLI não avaliado não gera alerta --------------

func TestNoDataDoesNotFire(t *testing.T) {
	d := BuildDashboard(nil, nil, DefaultSLOConfig())
	if fired := FiredAlerts(EvaluateAlerts(d, DefaultAlertConfig())); len(fired) != 0 {
		t.Errorf("sem dados não deve disparar alertas, veio %+v", fired)
	}
	// Mesmo no avaliador sustentado, mil observações sem dados nunca disparam.
	ev := NewAlertEvaluator(DefaultAlertConfig())
	for i := 0; i < 5; i++ {
		if fired := FiredAlerts(ev.Observe(d)); len(fired) != 0 {
			t.Fatalf("sem dados (obs %d) não devia disparar", i)
		}
	}
}

// Message é rótulo accionável (contém o nome do SLI e a rota) e NÃO transporta
// payload — só nomes/números.
func TestAlertMessageIsLabelWithRoute(t *testing.T) {
	alerts := EvaluateAlerts(degradedDashboard(), DefaultAlertConfig())
	a, _ := findAlert(alerts, AlertOverrideRateHigh)
	if a.Message == "" {
		t.Fatal("mensagem de alerta vazia")
	}
	if !contains(a.Message, "RB-05") || !contains(a.Message, SLIOverrideRate) {
		t.Errorf("mensagem sem rota/SLI accionável: %q", a.Message)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
