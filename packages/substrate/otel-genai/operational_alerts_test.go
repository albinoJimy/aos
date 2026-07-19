package otelgenai

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// --- helpers: cenários sintéticos que rendem os sete SLIs ---------------------

// healthyOpInputs produz inputs onde os sete SLIs canónicos CUMPREM o SLO.
func healthyOpInputs() OperationalInputs {
	return OperationalInputs{
		Events: []WideEvent{
			{Operation: OpChat, TraceIDHex: "t1", RunID: "run-1", InputTokens: 1000,
				Attributes: map[string]any{AttrCacheHitRate: 0.95}},
			{Operation: OpExecuteTool, TraceIDHex: "t1", RunID: "run-1", LatencyNanos: int64(3 * time.Millisecond)},
			coldStartEvent("run-1", 40),
			headroomEvent("run-1", 5000),
			replayEvent("run-1", 1.0),
		},
		ControlPlaneAvailability: ptrF(0.9995),
		AuditWORMIntact:          ptrB(true),
	}
}

// --- AC1/AC2/AC5: catálogo default cobre os 7 SLIs e é versionado -------------

func TestDefaultOperationalAlertConfigCoversSevenSLIsWithRunbooks(t *testing.T) {
	c := DefaultOperationalAlertConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("config default deve ser válida: %v", err)
	}
	// AC1/AC2: cada SLI canónico tem regra; cada regra tem runbook.
	covered := make(map[string]bool)
	for _, r := range c.Rules {
		if r.Route.Runbook == "" {
			t.Errorf("regra %q sem runbook (AC2)", r.Name)
		}
		if !validSeverity(r.Severity) {
			t.Errorf("regra %q com severidade inválida %q", r.Name, r.Severity)
		}
		covered[r.SLI] = true
	}
	for _, sli := range canonicalSLIs {
		if !covered[sli] {
			t.Errorf("SLI canónico %q sem regra de alerta (AC1)", sli)
		}
	}
	// AC4: o headroom tem DUAS regras/rotas distintas (RB-01 vs RB-03).
	var rl, budget bool
	for _, r := range c.Rules {
		if r.SLI == SLIHeadroomTokens && r.Cause == CauseRateLimitCollapse && r.Route.Runbook == RunbookRateLimitCollapse {
			rl = true
		}
		if r.SLI == SLIHeadroomTokens && r.Cause == CauseBudgetExhaustion && r.Route.Runbook == RunbookBudgetExhaustion {
			budget = true
		}
	}
	if !rl || !budget {
		t.Errorf("AC4: headroom deve distinguir RB-01 (rate limit) de RB-03 (orçamento): rl=%v budget=%v", rl, budget)
	}
}

// --- AC2: teste de NÃO-ÓRFÃOS que FALHA o CI (o coração de AOS-105) -----------

func TestNoOrphanAlertsFailsCIWhenRunbookOrRuleMissing(t *testing.T) {
	// (a) Remover o runbook de UMA regra faz a validação (o gate do CI) FALHAR.
	c := DefaultOperationalAlertConfig()
	c.Rules[0].Route.Runbook = ""
	if err := c.Validate(); err == nil {
		t.Errorf("AC2: uma regra sem runbook devia FALHAR a validação de CI")
	}

	// (b) Remover a única regra de um SLI canónico faz a cobertura FALHAR.
	// Remove ambas as regras de headroom deixaria headroom_tokens órfão; basta remover
	// uma regra cujo SLI só tem essa (ex.: audit_worm_integrity — a última).
	c2 := DefaultOperationalAlertConfig()
	var kept []OperationalAlertRule
	for _, r := range c2.Rules {
		if r.SLI == SLIAuditWORMIntegrity {
			continue // remove a cobertura do audit WORM
		}
		kept = append(kept, r)
	}
	c2.Rules = kept
	if err := c2.Validate(); err == nil {
		t.Errorf("AC2: um SLI canónico sem regra devia FALHAR a validação de CI")
	}

	// A config default (todas as regras com runbook, todos os SLIs cobertos) passa.
	if err := DefaultOperationalAlertConfig().Validate(); err != nil {
		t.Errorf("a config default não devia ter órfãos: %v", err)
	}
}

func TestOperationalAlertConfigValidateFailClosed(t *testing.T) {
	base := DefaultOperationalAlertConfig()
	cases := map[string]func(*OperationalAlertConfig){
		"SemVer inválido":     func(c *OperationalAlertConfig) { c.Version = "v1" },
		"janela < 1":          func(c *OperationalAlertConfig) { c.SustainedWindows = 0 },
		"sem regras":          func(c *OperationalAlertConfig) { c.Rules = nil },
		"SLI não-canónico":    func(c *OperationalAlertConfig) { c.Rules[0].SLI = "inventado" },
		"severidade inválida": func(c *OperationalAlertConfig) { c.Rules[0].Severity = "meh" },
		"nome vazio":          func(c *OperationalAlertConfig) { c.Rules[0].Name = "" },
		"janela negativa":     func(c *OperationalAlertConfig) { c.Rules[0].SustainedWindows = -1 },
		"nome duplicado":      func(c *OperationalAlertConfig) { c.Rules[1].Name = c.Rules[0].Name },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			c := DefaultOperationalAlertConfig()
			c.Rules = append([]OperationalAlertRule(nil), base.Rules...)
			mut(&c)
			if err := c.Validate(); err == nil {
				t.Errorf("config inválida (%s) devia ser rejeitada fail-closed", name)
			}
		})
	}
}

// --- AC5: round-trip JSON reproduzível + artefacto embebido == código ---------

func TestOperationalAlertsRoundTripJSONReproducible(t *testing.T) {
	c := DefaultOperationalAlertConfig()
	raw, err := c.JSON()
	if err != nil {
		t.Fatalf("serialização falhou: %v", err)
	}
	got, err := LoadOperationalAlertConfig(raw)
	if err != nil {
		t.Fatalf("desserialização fail-closed rejeitou o próprio artefacto: %v", err)
	}
	if !reflect.DeepEqual(c, got) {
		t.Fatalf("round-trip não é idêntico:\n want %+v\n  got %+v", c, got)
	}
	raw2, err := got.JSON()
	if err != nil {
		t.Fatalf("re-serialização falhou: %v", err)
	}
	if !bytes.Equal(raw, raw2) {
		t.Errorf("serialização não determinista (bytes divergem no round-trip)")
	}
}

func TestEmbeddedOperationalAlertsMatchesCode(t *testing.T) {
	fromFile, err := EmbeddedOperationalAlertConfig()
	if err != nil {
		t.Fatalf("catálogo de alertas embebido inválido: %v", err)
	}
	if !reflect.DeepEqual(DefaultOperationalAlertConfig(), fromFile) {
		t.Fatalf("operational_alerts.json diverge de DefaultOperationalAlertConfig() — regenerar o artefacto")
	}
	gen, _ := DefaultOperationalAlertConfig().JSON()
	if !bytes.Equal(bytes.TrimRight(EmbeddedOperationalAlertsJSON(), "\n"), gen) {
		t.Errorf("bytes do artefacto embebido divergem da serialização do código")
	}
}

func TestOperationalAlertsArtifactHasNoSecrets(t *testing.T) {
	raw := EmbeddedOperationalAlertsJSON()
	forbidden := []string{"password", "secret", "token=", "apikey", "api_key", "bearer", "BEGIN "}
	lower := bytes.ToLower(raw)
	for _, f := range forbidden {
		if bytes.Contains(lower, bytes.ToLower([]byte(f))) {
			t.Errorf("artefacto de alertas contém possível segredo %q", f)
		}
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("artefacto não é JSON válido: %v", err)
	}
}

// --- AC3: avaliação QUERY-TIME a partir de Breaches() (não emit-time) ----------

func TestEvaluateOperationalAlertsHealthyNoFire(t *testing.T) {
	snap := DefaultDashboardCatalog().Render(healthyOpInputs())
	if len(snap.Breaches()) != 0 {
		t.Fatalf("dados saudáveis não deviam ter breaches, veio %d", len(snap.Breaches()))
	}
	alerts := EvaluateOperationalAlerts(snap, DefaultOperationalAlertConfig())
	if len(FiredAlerts(alerts)) != 0 {
		t.Errorf("sem breaches não deve disparar nenhum alerta, veio %v", FiredAlerts(alerts))
	}
}

// TestOperationalAlertTripPerSLI: cenário sintético por SLI — o alerta TRIPA quando o
// SLI cruza o limiar e RECUPERA quando volta (AC3/AC5, DoD).
func TestOperationalAlertTripPerSLI(t *testing.T) {
	c := DefaultDashboardCatalog()
	cfg := DefaultOperationalAlertConfig()

	cases := []struct {
		name      string
		alert     string
		breach    OperationalInputs
		recovered OperationalInputs
	}{
		{
			name:      "control_plane_availability",
			alert:     AlertControlPlaneAvailabilityLow,
			breach:    OperationalInputs{ControlPlaneAvailability: ptrF(0.90)},
			recovered: OperationalInputs{ControlPlaneAvailability: ptrF(0.9995)},
		},
		{
			name:  "mediation_overhead_p95",
			alert: AlertMediationOverheadP95High,
			breach: OperationalInputs{Events: []WideEvent{
				{Operation: OpExecuteTool, TraceIDHex: "t", LatencyNanos: int64(50 * time.Millisecond)},
			}},
			recovered: OperationalInputs{Events: []WideEvent{
				{Operation: OpExecuteTool, TraceIDHex: "t", LatencyNanos: int64(2 * time.Millisecond)},
			}},
		},
		{
			name:      "sandbox_cold_start_p95",
			alert:     AlertSandboxColdStartP95High,
			breach:    OperationalInputs{Events: []WideEvent{coldStartEvent("slow", 300)}},
			recovered: OperationalInputs{Events: []WideEvent{coldStartEvent("ok", 40)}},
		},
		{
			name:  "cache_hit_rate",
			alert: AlertOpCacheHitRateLow,
			breach: OperationalInputs{Events: []WideEvent{
				{Operation: OpChat, TraceIDHex: "t", InputTokens: 1000, Attributes: map[string]any{AttrCacheHitRate: 0.10}},
			}},
			recovered: OperationalInputs{Events: []WideEvent{
				{Operation: OpChat, TraceIDHex: "t", InputTokens: 1000, Attributes: map[string]any{AttrCacheHitRate: 0.95}},
			}},
		},
		{
			name:      "headroom_rate_limit_collapse",
			alert:     AlertHeadroomRateLimitCollapse,
			breach:    OperationalInputs{Events: []WideEvent{headroomEvent("wall", 0)}}, // zero ⇒ RB-01
			recovered: OperationalInputs{Events: []WideEvent{headroomEvent("ok", 5000)}},
		},
		{
			name:      "headroom_budget_exhaustion",
			alert:     AlertHeadroomBudgetExhaustion,
			breach:    OperationalInputs{Events: []WideEvent{headroomEvent("deficit", -100)}}, // negativo ⇒ RB-03
			recovered: OperationalInputs{Events: []WideEvent{headroomEvent("ok", 5000)}},
		},
		{
			name:      "replay_fidelity",
			alert:     AlertReplayFidelityLow,
			breach:    OperationalInputs{Events: []WideEvent{replayEvent("regressed", 0.5)}},
			recovered: OperationalInputs{Events: []WideEvent{replayEvent("clean", 1.0)}},
		},
		{
			name:      "audit_worm_integrity",
			alert:     AlertAuditWORMIntegrityBroken,
			breach:    OperationalInputs{AuditWORMIntact: ptrB(false)},
			recovered: OperationalInputs{AuditWORMIntact: ptrB(true)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// TRIP: observação única dispara o alerta esperado (SustainedWindows=1 aqui).
			breachSnap := c.Render(tc.breach)
			alerts := EvaluateOperationalAlerts(breachSnap, cfg)
			a, ok := findAlert(alerts, tc.alert)
			if !ok || !a.Fired {
				t.Errorf("%s: alerta %q devia TRIPAR no breach (ok=%v fired=%v)", tc.name, tc.alert, ok, ok && a.Fired)
			}
			if a.Route.Runbook == "" {
				t.Errorf("%s: alerta disparado sem runbook", tc.name)
			}

			// RECUPERA: quando o SLI volta ao alvo, o alerta não dispara.
			recSnap := c.Render(tc.recovered)
			rec := EvaluateOperationalAlerts(recSnap, cfg)
			if a2, ok := findAlert(rec, tc.alert); ok && a2.Fired {
				t.Errorf("%s: alerta %q não devia disparar após recuperação", tc.name, tc.alert)
			}
		})
	}
}

// AC3: um SLI não avaliado (Samples==0) nunca dispara (anti-vacuidade).
func TestOperationalAlertsAntiVacuity(t *testing.T) {
	snap := DefaultDashboardCatalog().Render(OperationalInputs{}) // sem dados nem injecção
	alerts := EvaluateOperationalAlerts(snap, DefaultOperationalAlertConfig())
	if fired := FiredAlerts(alerts); len(fired) != 0 {
		t.Errorf("SLIs não avaliados não deviam disparar (anti-vacuidade), veio %v", fired)
	}
}

// AC4: o headroom NÃO colapsa RB-01 e RB-03 num alerta genérico — a causa roteia.
func TestHeadroomCauseRoutingDistinguishesRB01FromRB03(t *testing.T) {
	c := DefaultDashboardCatalog()
	cfg := DefaultOperationalAlertConfig()

	// headroom fixado no zero ⇒ colapso por rate limit ⇒ RB-01, NÃO budget.
	zero := EvaluateOperationalAlerts(c.Render(OperationalInputs{Events: []WideEvent{headroomEvent("wall", 0)}}), cfg)
	if a, ok := findAlert(zero, AlertHeadroomRateLimitCollapse); !ok || !a.Fired || a.Route.Runbook != RunbookRateLimitCollapse {
		t.Errorf("headroom 0 devia disparar RB-01 (rate limit): %+v", a)
	}
	if a, ok := findAlert(zero, AlertHeadroomBudgetExhaustion); ok && a.Fired {
		t.Errorf("headroom 0 NÃO devia disparar o alerta de orçamento (RB-03)")
	}

	// headroom NEGATIVO ⇒ esgotamento de orçamento ⇒ RB-03, NÃO rate limit.
	neg := EvaluateOperationalAlerts(c.Render(OperationalInputs{Events: []WideEvent{headroomEvent("deficit", -50)}}), cfg)
	if a, ok := findAlert(neg, AlertHeadroomBudgetExhaustion); !ok || !a.Fired || a.Route.Runbook != RunbookBudgetExhaustion {
		t.Errorf("headroom negativo devia disparar RB-03 (orçamento): %+v", a)
	}
	if a, ok := findAlert(neg, AlertHeadroomRateLimitCollapse); ok && a.Fired {
		t.Errorf("headroom negativo NÃO devia disparar o alerta de rate limit (RB-01)")
	}
}

// --- AC5: janela SUSTENTADA — pico transitório não alerta, breach persistente sim ---

func TestOperationalSustainedWindowSuppressesTransientAndFiresPersistent(t *testing.T) {
	c := DefaultDashboardCatalog()
	ev := NewOperationalAlertEvaluator(DefaultOperationalAlertConfig())
	if ev.ConfigDefaulted() {
		t.Fatalf("a config default não devia cair no fallback")
	}

	// cold-start tem janela sustentada 3. Um pico único NÃO dispara.
	breach := c.Render(OperationalInputs{Events: []WideEvent{coldStartEvent("slow", 300)}})
	healthy := c.Render(OperationalInputs{Events: []WideEvent{coldStartEvent("ok", 40)}})

	firstRound := ev.Observe(breach)
	if a, _ := findAlert(firstRound, AlertSandboxColdStartP95High); a.Fired {
		t.Errorf("um pico único não devia disparar (janela sustentada), streak=%d", a.Streak)
	}
	// Segunda observação em breach: ainda abaixo da janela (streak 2 < 3).
	if a, _ := findAlert(ev.Observe(breach), AlertSandboxColdStartP95High); a.Fired {
		t.Errorf("streak 2 < 3 não devia disparar")
	}
	// Terceira observação em breach: dispara (streak 3 >= 3).
	if a, _ := findAlert(ev.Observe(breach), AlertSandboxColdStartP95High); !a.Fired || a.Streak != 3 {
		t.Errorf("streak 3 devia disparar (fired=%v streak=%d)", a.Fired, a.Streak)
	}
	// Recupera: uma observação saudável ZERA o streak e o alerta pára.
	if a, _ := findAlert(ev.Observe(healthy), AlertSandboxColdStartP95High); a.Fired || a.Streak != 0 {
		t.Errorf("após recuperação o alerta devia parar e zerar o streak (fired=%v streak=%d)", a.Fired, a.Streak)
	}
}

// A janela por-regra: o audit WORM (acuto, janela 1) dispara à PRIMEIRA observação.
func TestOperationalAcuteAlertFiresImmediately(t *testing.T) {
	c := DefaultDashboardCatalog()
	ev := NewOperationalAlertEvaluator(DefaultOperationalAlertConfig())
	snap := c.Render(OperationalInputs{AuditWORMIntact: ptrB(false)})
	if a, _ := findAlert(ev.Observe(snap), AlertAuditWORMIntegrityBroken); !a.Fired {
		t.Errorf("o audit WORM quebrado (janela 1) devia disparar à primeira observação")
	}
}

// Config inválida ⇒ evaluator cai na default (fail-closed) e torna-se observável.
func TestOperationalEvaluatorConfigDefaulted(t *testing.T) {
	ev := NewOperationalAlertEvaluator(OperationalAlertConfig{Version: "x"})
	if !ev.ConfigDefaulted() {
		t.Errorf("uma config inválida devia cair na default (observável)")
	}
	if err := ev.Config().Validate(); err != nil {
		t.Errorf("a config activa (default) devia ser válida: %v", err)
	}
	// RouteFor de um alerta conhecido resolve o runbook; de um desconhecido cai no default.
	if r, ok := ev.RouteFor(AlertAuditWORMIntegrityBroken); !ok || r.Runbook != ProcDisasterRecovery {
		t.Errorf("RouteFor devia resolver o runbook do alerta conhecido, veio %+v ok=%v", r, ok)
	}
	if r, ok := ev.RouteFor("inexistente"); ok || r != DefaultRoute {
		t.Errorf("RouteFor de alerta desconhecido devia devolver DefaultRoute/false, veio %+v ok=%v", r, ok)
	}
}

// --- AC6: agrupamento por plano + supressão que não esconde padrões sistémicos --

func TestGroupAndSuppressNeverSuppressesCriticalAndKeepsSummary(t *testing.T) {
	c := DefaultDashboardCatalog()
	// Plano de DADOS: cold-start alto (WARNING) + replay quebrado (CRITICAL) + audit
	// quebrado (CRITICAL). O WARNING é correlacionado a um CRITICAL do mesmo plano ⇒
	// suprimido; os CRITICAL ficam sempre activos.
	in := OperationalInputs{
		Events: []WideEvent{
			coldStartEvent("slow", 300),   // data, warning
			replayEvent("regressed", 0.5), // data, critical
		},
		AuditWORMIntact: ptrB(false), // data, critical
	}
	snap := c.Render(in)
	alerts := EvaluateOperationalAlerts(snap, DefaultOperationalAlertConfig())
	fired := FiredAlerts(alerts)
	groups := GroupFiredAlerts(alerts, snap)

	if len(groups) != 1 || groups[0].Plane != PlaneData {
		t.Fatalf("esperava 1 grupo no plano de dados, veio %+v", groups)
	}
	g := groups[0]
	// Nenhum CRITICAL é suprimido.
	for _, a := range g.Suppressed {
		if a.Severity == SevCritical {
			t.Errorf("um CRITICAL nunca deve ser suprimido: %q", a.Name)
		}
	}
	// O WARNING de cold-start é suprimido (correlacionado a um CRITICAL do plano).
	if _, ok := findAlert(g.Suppressed, AlertSandboxColdStartP95High); !ok {
		t.Errorf("o WARNING de cold-start devia ser suprimido sob os CRITICAL do plano")
	}
	// Os dois CRITICAL ficam activos (visíveis).
	if _, ok := findAlert(g.Active, AlertReplayFidelityLow); !ok {
		t.Errorf("replay CRITICAL devia ficar activo")
	}
	if _, ok := findAlert(g.Active, AlertAuditWORMIntegrityBroken); !ok {
		t.Errorf("audit CRITICAL devia ficar activo")
	}
	// O padrão sistémico não é escondido: o summary conta o TOTAL (incl. suprimidos).
	if g.Summary == "" {
		t.Errorf("o grupo devia ter um summary que mantém o padrão visível")
	}
	if len(g.Active)+len(g.Suppressed) != len(fired) {
		t.Errorf("todos os disparados têm de aparecer (activos+suprimidos)=%d vs fired=%d",
			len(g.Active)+len(g.Suppressed), len(fired))
	}
}

// Sem CRITICAL num plano, os WARNING ficam TODOS activos (não se suprime por vazio).
func TestGroupAndSuppressKeepsWarningsWhenNoCritical(t *testing.T) {
	c := DefaultDashboardCatalog()
	in := OperationalInputs{Events: []WideEvent{coldStartEvent("slow", 300)}} // só WARNING
	snap := c.Render(in)
	alerts := EvaluateOperationalAlerts(snap, DefaultOperationalAlertConfig())
	groups := GroupFiredAlerts(alerts, snap)
	if len(groups) != 1 {
		t.Fatalf("esperava 1 grupo, veio %d", len(groups))
	}
	if len(groups[0].Suppressed) != 0 {
		t.Errorf("sem CRITICAL no plano, nenhum WARNING devia ser suprimido, veio %d", len(groups[0].Suppressed))
	}
	if _, ok := findAlert(groups[0].Active, AlertSandboxColdStartP95High); !ok {
		t.Errorf("o WARNING de cold-start devia ficar activo")
	}
}

// Reset zera o estado sustentado (recomeça a avaliação do zero).
func TestOperationalEvaluatorReset(t *testing.T) {
	c := DefaultDashboardCatalog()
	ev := NewOperationalAlertEvaluator(DefaultOperationalAlertConfig())
	breach := c.Render(OperationalInputs{Events: []WideEvent{coldStartEvent("slow", 300)}})
	ev.Observe(breach)
	ev.Observe(breach) // streak 2 acumulado
	ev.Reset()
	// Após o reset, a próxima observação recomeça em streak 1 (não dispara na janela 3).
	if a, _ := findAlert(ev.Observe(breach), AlertSandboxColdStartP95High); a.Streak != 1 || a.Fired {
		t.Errorf("após Reset o streak devia recomeçar em 1 sem disparar, veio streak=%d fired=%v", a.Streak, a.Fired)
	}
}

// RouteFor com runbook vazio numa regra existente cai na DefaultRoute (fail-safe).
func TestOperationalRouteForEmptyRunbookFallsBackToDefault(t *testing.T) {
	cfg := DefaultOperationalAlertConfig()
	cfg.Rules[0].Route.Runbook = "" // regra presente mas órfã
	r, ok := cfg.RouteFor(cfg.Rules[0].Name)
	if ok || r != DefaultRoute {
		t.Errorf("regra com runbook vazio devia cair na DefaultRoute/false, veio %+v ok=%v", r, ok)
	}
}

// Um alerta cujo SLI não tem plano conhecido cai no grupo residual "desconhecido".
func TestGroupAndSuppressResidualPlane(t *testing.T) {
	fired := []Alert{{Name: "x", SLI: "sli-sem-plano", Severity: SevWarning, Fired: true}}
	groups := GroupAndSuppress(fired, map[string]Plane{}) // sem mapeamento de plano
	if len(groups) != 1 || groups[0].Plane != "" {
		t.Fatalf("esperava 1 grupo residual (plano \"\"), veio %+v", groups)
	}
	if groups[0].Summary == "" || len(groups[0].Active) != 1 {
		t.Errorf("grupo residual devia surfacar o alerta com summary, veio %+v", groups[0])
	}
}

// Alertas de planos distintos caem em grupos distintos (correlação por plano).
func TestGroupAndSuppressSeparatesByPlane(t *testing.T) {
	c := DefaultDashboardCatalog()
	in := OperationalInputs{
		Events:                   []WideEvent{replayEvent("regressed", 0.5)}, // data, critical
		ControlPlaneAvailability: ptrF(0.90),                                 // control, critical
	}
	snap := c.Render(in)
	alerts := EvaluateOperationalAlerts(snap, DefaultOperationalAlertConfig())
	groups := GroupFiredAlerts(alerts, snap)
	if len(groups) != 2 {
		t.Fatalf("esperava 2 grupos (control + data), veio %d: %+v", len(groups), groups)
	}
	// Ordenados de forma determinista por plano ("control" < "data").
	if groups[0].Plane != PlaneControl || groups[1].Plane != PlaneData {
		t.Errorf("grupos deviam vir ordenados por plano, veio %q e %q", groups[0].Plane, groups[1].Plane)
	}
}
