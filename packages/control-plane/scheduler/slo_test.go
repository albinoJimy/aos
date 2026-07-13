package scheduler_test

// Testes dos SLIs/SLOs versionados + alertas deterministas + dashboard mínimo
// agregado (AOS-034). Deterministas: avaliação de alerta é função pura das
// observações (streak avançado só por Evaluate), sem time.Now/rand.
//
// Cobrem os Testes Requeridos do ticket:
//   - config de SLO versionada carrega/valida fail-closed;
//   - alerta DISPARA em headroom criticamente baixo e em saturação SUSTENTADA;
//   - avaliação determinística (mesma sequência ⇒ mesmos alertas);
//   - dashboard mínimo AGREGA o estado correctamente (o antídoto ao colapso agregado).

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/aos-ref/control-plane/scheduler"
)

// ---------------------------------------------------------------------------
// Config de SLO VERSIONADA — validação fail-closed.
// ---------------------------------------------------------------------------

func TestSLOConfig_LoadValidatesFailClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		json string
		ok   bool
	}{
		{"válida", `{"version":"1.2.0","headroom_critical_free_ratio":0.05,"headroom_utilization_target":0.85,"max_defer_rate":0.2,"sustained_saturation_windows":3}`, true},
		{"json malformado", `{`, false},
		{"semver inválido", `{"version":"v1","headroom_critical_free_ratio":0.05,"headroom_utilization_target":0.85,"max_defer_rate":0.2,"sustained_saturation_windows":3}`, false},
		{"ratio fora de gama", `{"version":"1.0.0","headroom_critical_free_ratio":1.5,"headroom_utilization_target":0.85,"max_defer_rate":0.2,"sustained_saturation_windows":3}`, false},
		{"janelas < 1", `{"version":"1.0.0","headroom_critical_free_ratio":0.05,"headroom_utilization_target":0.85,"max_defer_rate":0.2,"sustained_saturation_windows":0}`, false},
		{"defer-rate > 1", `{"version":"1.0.0","headroom_critical_free_ratio":0.05,"headroom_utilization_target":0.85,"max_defer_rate":1.5,"sustained_saturation_windows":3}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := scheduler.LoadSLOConfig([]byte(tc.json))
			if tc.ok && err != nil {
				t.Fatalf("esperava válida, erro: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("esperava fail-closed, aceitou: %+v", cfg)
			}
		})
	}
}

func TestSLOConfig_DefaultIsValid(t *testing.T) {
	t.Parallel()
	// O default tem de ser aceite por um evaluator sem cair no fallback silencioso.
	cfg := scheduler.DefaultSLOConfig()
	ev := scheduler.NewSLOEvaluator(cfg)
	if got := ev.Config().Version; got != cfg.Version {
		t.Errorf("evaluator caiu no fallback: version %q != %q", got, cfg.Version)
	}
}

func TestSLOEvaluator_InvalidConfigFallsBackToDefault(t *testing.T) {
	t.Parallel()
	// Config inválida (janelas 0) ⇒ evaluator usa o default (fail-closed, nunca corre
	// com thresholds sem sentido).
	ev := scheduler.NewSLOEvaluator(scheduler.SLOConfig{Version: "bad"})
	if ev.Config().Version != scheduler.DefaultSLOConfig().Version {
		t.Errorf("config inválida não caiu no default: %+v", ev.Config())
	}
}

// ---------------------------------------------------------------------------
// Alerta: headroom CRITICAMENTE baixo dispara.
// ---------------------------------------------------------------------------

func TestSLOEvaluator_HeadroomCriticalFires(t *testing.T) {
	t.Parallel()
	ev := scheduler.NewSLOEvaluator(scheduler.DefaultSLOConfig()) // crit 0.05
	// Uma chave com 3% livre (< 5%): crítico.
	d := scheduler.DashboardSnapshot{
		Headroom: []scheduler.HeadroomStat{
			{Key: "anthropic:claude:eu", FreeTokens: 30, ReservedTokens: 970, LimitTokens: 1000, FreeRatioTokens: 0.03, UtilizationTokens: 0.97},
		},
		MinHeadroomFreeRatio: 0.03,
		MaxUtilization:       0.97,
	}
	alerts := ev.Evaluate(d)
	if !alertFired(alerts, scheduler.AlertHeadroomCritical) {
		t.Errorf("headroom crítico não disparou (min free ratio 0.03 < 0.05)")
	}
	// Utilização 0.97 > alvo 0.85 ⇒ aviso.
	if !alertFired(alerts, scheduler.AlertHeadroomUtilizationHigh) {
		t.Errorf("utilização alta não disparou (0.97 > 0.85)")
	}
	// Severidade do crítico.
	for _, a := range alerts {
		if a.Name == scheduler.AlertHeadroomCritical && a.Severity != scheduler.SevCritical {
			t.Errorf("severidade do headroom crítico = %q, quer critical", a.Severity)
		}
	}
}

func TestSLOEvaluator_HeadroomHealthyDoesNotFire(t *testing.T) {
	t.Parallel()
	ev := scheduler.NewSLOEvaluator(scheduler.DefaultSLOConfig())
	d := scheduler.DashboardSnapshot{
		Headroom:             []scheduler.HeadroomStat{{Key: "k", FreeTokens: 800, ReservedTokens: 200, LimitTokens: 1000, FreeRatioTokens: 0.8, UtilizationTokens: 0.2}},
		MinHeadroomFreeRatio: 0.8,
		MaxUtilization:       0.2,
	}
	alerts := ev.Evaluate(d)
	if alertFired(alerts, scheduler.AlertHeadroomCritical) {
		t.Errorf("headroom saudável (80%% livre) não devia disparar crítico")
	}
	if alertFired(alerts, scheduler.AlertHeadroomUtilizationHigh) {
		t.Errorf("utilização baixa não devia disparar aviso")
	}
}

// ---------------------------------------------------------------------------
// Alerta: saturação SUSTENTADA dispara só após N observações consecutivas.
// ---------------------------------------------------------------------------

func TestSLOEvaluator_SustainedSaturationFiresAfterNWindows(t *testing.T) {
	t.Parallel()
	cfg := scheduler.DefaultSLOConfig()
	cfg.SustainedSaturationWindows = 3
	ev := scheduler.NewSLOEvaluator(cfg)

	saturated := scheduler.DashboardSnapshot{
		Partitions: []scheduler.PartitionStat{{Partition: "acme:P1", Tenant: "acme", Priority: "P1", Depth: 5, Capacity: 5, Saturated: true}},
	}

	// Observação 1 e 2: NÃO dispara (não é sustentada ainda).
	if alertFired(ev.Evaluate(saturated), scheduler.AlertSaturationSustained) {
		t.Fatalf("obs 1: saturação sustentada não devia disparar")
	}
	if alertFired(ev.Evaluate(saturated), scheduler.AlertSaturationSustained) {
		t.Fatalf("obs 2: saturação sustentada não devia disparar")
	}
	// Observação 3: DISPARA (3 consecutivas).
	if !alertFired(ev.Evaluate(saturated), scheduler.AlertSaturationSustained) {
		t.Fatalf("obs 3: saturação SUSTENTADA devia disparar")
	}

	// Uma observação NÃO saturada zera o streak — volta a não disparar.
	healthy := scheduler.DashboardSnapshot{
		Partitions: []scheduler.PartitionStat{{Partition: "acme:P1", Tenant: "acme", Priority: "P1", Depth: 0, Capacity: 5, Saturated: false}},
	}
	if alertFired(ev.Evaluate(healthy), scheduler.AlertSaturationSustained) {
		t.Fatalf("após alívio, saturação sustentada não devia disparar")
	}
	if alertFired(ev.Evaluate(saturated), scheduler.AlertSaturationSustained) {
		t.Fatalf("obs 1 pós-reset: não devia disparar (streak reiniciado)")
	}
}

// ---------------------------------------------------------------------------
// Determinismo: mesma sequência de observações ⇒ mesmos alertas.
// ---------------------------------------------------------------------------

func TestSLOEvaluator_Deterministic(t *testing.T) {
	t.Parallel()
	seq := []scheduler.DashboardSnapshot{
		{Partitions: []scheduler.PartitionStat{{Partition: "a:P1", Saturated: true}}, MinHeadroomFreeRatio: 0.5, Headroom: []scheduler.HeadroomStat{{Key: "k"}}},
		{Partitions: []scheduler.PartitionStat{{Partition: "a:P1", Saturated: true}}, MinHeadroomFreeRatio: 0.02, Headroom: []scheduler.HeadroomStat{{Key: "k", FreeRatioTokens: 0.02}}},
		{Partitions: []scheduler.PartitionStat{{Partition: "a:P1", Saturated: true}}, MinHeadroomFreeRatio: 0.5, Headroom: []scheduler.HeadroomStat{{Key: "k"}}},
	}
	run := func() [][]scheduler.Alert {
		ev := scheduler.NewSLOEvaluator(scheduler.DefaultSLOConfig())
		var out [][]scheduler.Alert
		for _, d := range seq {
			out = append(out, ev.Evaluate(d))
		}
		return out
	}
	a := run()
	b := run()
	if !reflect.DeepEqual(a, b) {
		t.Errorf("avaliação não determinística:\n%v\n!=\n%v", a, b)
	}
}

// ---------------------------------------------------------------------------
// Dashboard mínimo: AGREGA o estado correctamente (soma counters, último gauge por
// série) — evita o "individualmente ok, agregadamente colapsa".
// ---------------------------------------------------------------------------

func TestDashboard_AggregatesState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)

	// Duas partições, cada uma "individualmente" pequena; a SOMA é grande.
	sm.ObserveQueuePartition(ctx, scheduler.QueueSnapshot{Partition: "acme:P1", Tenant: "acme", Priority: "P1", Depth: 4, Capacity: 5, Saturated: true})
	sm.ObserveQueuePartition(ctx, scheduler.QueueSnapshot{Partition: "acme:P2", Tenant: "acme", Priority: "P2", Depth: 3, Capacity: 5, Saturated: false})
	// Actualização posterior da P1 (último gauge por série vence).
	sm.ObserveQueuePartition(ctx, scheduler.QueueSnapshot{Partition: "acme:P1", Tenant: "acme", Priority: "P1", Depth: 5, Capacity: 5, Saturated: true})

	// Duas chaves de headroom.
	sm.ObserveHeadroom(ctx, testKey, "acme", scheduler.HeadroomSnapshot{Tokens: 40, Requests: 900, LimitTokens: 1000, LimitRequests: 1000})
	sm.ObserveHeadroom(ctx, scheduler.ProviderKey{Provider: "openai", Model: "gpt", Region: "us"}, "beta",
		scheduler.HeadroomSnapshot{Tokens: 500, Requests: 900, LimitTokens: 1000, LimitRequests: 1000})

	// Counters: 3 admits, 2 defers, 1 degradação, 1 spawn adiado.
	sm.RecordAdmitted(ctx, testKey, "acme")
	sm.RecordAdmitted(ctx, testKey, "acme")
	sm.RecordAdmitted(ctx, testKey, "acme")
	sm.RecordDeferred(ctx, testKey, "acme", scheduler.DeferReasonNoHeadroom)
	sm.RecordDeferred(ctx, testKey, "acme", scheduler.DeferReasonBackpressure)
	sm.RecordDegradation(ctx, scheduler.ActionDowngrade, "acme", "P2")
	sm.RecordSpawnDeferred(ctx, testKey, "acme")

	d := scheduler.AggregateDashboard(rec.Measurements())

	if d.TotalQueueDepth != 8 { // 5 (P1 último) + 3 (P2)
		t.Errorf("TotalQueueDepth = %d, quer 8 (5+3)", d.TotalQueueDepth)
	}
	if d.MaxQueueDepth != 5 {
		t.Errorf("MaxQueueDepth = %d, quer 5", d.MaxQueueDepth)
	}
	if d.SaturatedPartitions != 1 {
		t.Errorf("SaturatedPartitions = %d, quer 1", d.SaturatedPartitions)
	}
	if d.Admitted != 3 || d.Deferred != 2 {
		t.Errorf("admitted/deferred = %d/%d, quer 3/2", d.Admitted, d.Deferred)
	}
	// Defer-rate = 2/(3+2) = 0.4.
	if d.DeferRate < 0.399 || d.DeferRate > 0.401 {
		t.Errorf("DeferRate = %.4f, quer 0.4", d.DeferRate)
	}
	if d.DegradationActions != 1 {
		t.Errorf("DegradationActions = %d, quer 1", d.DegradationActions)
	}
	if d.SpawnsDeferred != 1 {
		t.Errorf("SpawnsDeferred = %d, quer 1", d.SpawnsDeferred)
	}
	// Headroom: 2 chaves; mín free ratio = 40/1000 = 0.04.
	if len(d.Headroom) != 2 {
		t.Errorf("Headroom keys = %d, quer 2", len(d.Headroom))
	}
	if d.MinHeadroomFreeRatio < 0.039 || d.MinHeadroomFreeRatio > 0.041 {
		t.Errorf("MinHeadroomFreeRatio = %.4f, quer 0.04", d.MinHeadroomFreeRatio)
	}
	if d.TotalFreeTokens != 540 { // 40 + 500
		t.Errorf("TotalFreeTokens = %d, quer 540", d.TotalFreeTokens)
	}
}

// ---------------------------------------------------------------------------
// Dashboard determinístico: mesma entrada ⇒ mesmo descritor (serialização estável).
// ---------------------------------------------------------------------------

func TestDashboard_DeterministicAggregation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)
	for _, p := range []string{"acme:P1", "acme:P2", "beta:P0"} {
		sm.ObserveQueuePartition(ctx, scheduler.QueueSnapshot{Partition: p, Depth: 2, Capacity: 5})
	}
	ms := rec.Measurements()
	a := scheduler.AggregateDashboard(ms)
	b := scheduler.AggregateDashboard(ms)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("agregação não determinística")
	}
	// As partições vêm ORDENADAS (iteração estável).
	if len(a.Partitions) != 3 || a.Partitions[0].Partition != "acme:P1" || a.Partitions[2].Partition != "beta:P0" {
		t.Errorf("partições não ordenadas: %+v", a.Partitions)
	}
}

// ---------------------------------------------------------------------------
// Q1: headroom NÃO colapsa por provider_key — tenants distintos na MESMA chave
// mantêm-se em séries próprias, e o crítico de um NÃO é mascarado pelo outro.
// ---------------------------------------------------------------------------

func TestDashboard_HeadroomKeyedByTenantNoCollapse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)

	// MESMA provider_key (testKey), dois tenants: acme CRÍTICO (3% livre em tokens),
	// bob SAUDÁVEL (80% livre). Sem chavear por tenant, uma série sobrescreve a outra
	// e o crítico faz falso-negativo.
	sm.ObserveHeadroom(ctx, testKey, "acme", scheduler.HeadroomSnapshot{Tokens: 30, Requests: 900, LimitTokens: 1000, LimitRequests: 1000})
	sm.ObserveHeadroom(ctx, testKey, "bob", scheduler.HeadroomSnapshot{Tokens: 800, Requests: 900, LimitTokens: 1000, LimitRequests: 1000})

	d := scheduler.AggregateDashboard(rec.Measurements())

	// Duas séries distintas (não colapsaram numa só).
	if len(d.Headroom) != 2 {
		t.Fatalf("Headroom séries = %d, quer 2 (uma por tenant na mesma chave)", len(d.Headroom))
	}
	// O tenant viaja no descritor e a chave é a mesma.
	tenants := map[string]bool{}
	for _, hs := range d.Headroom {
		if hs.Key != testKey.String() {
			t.Errorf("HeadroomStat.Key = %q, quer %q", hs.Key, testKey.String())
		}
		tenants[hs.Tenant] = true
	}
	if !tenants["acme"] || !tenants["bob"] {
		t.Errorf("tenants nas séries = %v, quer acme+bob", tenants)
	}
	// A fracção livre mínima reflecte o tenant CRÍTICO (0.03), não é mascarada.
	if d.MinHeadroomFreeRatio < 0.029 || d.MinHeadroomFreeRatio > 0.031 {
		t.Errorf("MinHeadroomFreeRatio = %.4f, quer ~0.03 (tenant crítico)", d.MinHeadroomFreeRatio)
	}
	// O alerta crítico DISPARA (não há falso-negativo por colapso de tenant).
	ev := scheduler.NewSLOEvaluator(scheduler.DefaultSLOConfig())
	if !alertFired(ev.Evaluate(d), scheduler.AlertHeadroomCritical) {
		t.Errorf("headroom crítico devia disparar para o tenant acme (3%% livre)")
	}
}

// ---------------------------------------------------------------------------
// Q2: o crítico de headroom cobre a dimensão de REQUESTS (RPM). Uma chave
// esgotada em RPM mas folgada em TPM dispara o crítico (não é rebaixada a aviso).
// ---------------------------------------------------------------------------

func TestDashboard_HeadroomCriticalCoversRequests(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)

	// Tokens FOLGADOS (900/1000 → 90% livre) mas requests ESGOTADOS (10/1000 →
	// utilização 0.99 ⇒ 1% livre). Só olhando tokens, o crítico NÃO dispararia.
	sm.ObserveHeadroom(ctx, testKey, "acme", scheduler.HeadroomSnapshot{Tokens: 900, Requests: 10, LimitTokens: 1000, LimitRequests: 1000})

	d := scheduler.AggregateDashboard(rec.Measurements())

	if len(d.Headroom) != 1 {
		t.Fatalf("Headroom séries = %d, quer 1", len(d.Headroom))
	}
	hs := d.Headroom[0]
	// A fracção livre de requests é criticamente baixa (~0.01).
	if hs.FreeRatioRequests > 0.02 {
		t.Errorf("FreeRatioRequests = %.4f, quer ~0.01 (RPM esgotado)", hs.FreeRatioRequests)
	}
	// A fracção livre mínima passa a considerar o MÍNIMO das duas dimensões.
	if d.MinHeadroomFreeRatio > 0.02 {
		t.Errorf("MinHeadroomFreeRatio = %.4f, quer ~0.01 (dimensão requests)", d.MinHeadroomFreeRatio)
	}
	ev := scheduler.NewSLOEvaluator(scheduler.DefaultSLOConfig())
	if !alertFired(ev.Evaluate(d), scheduler.AlertHeadroomCritical) {
		t.Errorf("headroom crítico devia disparar por RPM esgotado (folga em tokens não deve mascarar)")
	}
}

// ---------------------------------------------------------------------------
// C4: o artefacto de config SLO versionado (slo.default.json) carrega via
// LoadSLOConfig e coincide com os defaults (fonte de verdade versionada real).
// ---------------------------------------------------------------------------

func TestSLOConfig_VersionedArtifactLoads(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("slo.default.json")
	if err != nil {
		t.Fatalf("ler slo.default.json: %v", err)
	}
	cfg, err := scheduler.LoadSLOConfig(raw)
	if err != nil {
		t.Fatalf("LoadSLOConfig(slo.default.json) fail-closed inesperado: %v", err)
	}
	if !reflect.DeepEqual(cfg, scheduler.DefaultSLOConfig()) {
		t.Errorf("artefacto versionado difere do default:\n%+v\n!=\n%+v", cfg, scheduler.DefaultSLOConfig())
	}
}

// ---------------------------------------------------------------------------
// BuildDashboard anexa os alertas avaliados (integração dashboard+SLO).
// ---------------------------------------------------------------------------

func TestBuildDashboard_AttachesAlerts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)
	// Headroom crítico (4% livre).
	sm.ObserveHeadroom(ctx, testKey, "acme", scheduler.HeadroomSnapshot{Tokens: 40, Requests: 40, LimitTokens: 1000, LimitRequests: 1000})

	ev := scheduler.NewSLOEvaluator(scheduler.DefaultSLOConfig())
	d := scheduler.BuildDashboard(rec.Measurements(), ev)
	if len(d.Alerts) == 0 {
		t.Fatalf("BuildDashboard não anexou alertas")
	}
	if !alertFired(d.Alerts, scheduler.AlertHeadroomCritical) {
		t.Errorf("BuildDashboard: headroom crítico devia constar dos alertas")
	}
	// FiredAlerts filtra só os disparados (query-time).
	fired := scheduler.FiredAlerts(d.Alerts)
	for _, a := range fired {
		if !a.Fired {
			t.Errorf("FiredAlerts devolveu um alerta não disparado: %+v", a)
		}
	}
}
