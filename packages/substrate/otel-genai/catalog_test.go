package otelgenai

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// --- helpers de construção de wide events sintéticos para os SLIs canónicos ---

// coldStartEvent constrói um wide event com o cold-start de sandbox (ms) no bag,
// correlacionado por run_id — a fonte real do SLI de cold-start (produtor sandbox).
func coldStartEvent(runID string, coldMs int64) WideEvent {
	return WideEvent{
		RunID:      runID,
		TraceIDHex: runID,
		Attributes: map[string]any{MetricSandboxColdStartMs: coldMs},
	}
}

// replayEvent constrói um wide event com a fidelidade de replay no bag.
func replayEvent(runID string, fidelity float64) WideEvent {
	return WideEvent{
		RunID:      runID,
		TraceIDHex: runID,
		Attributes: map[string]any{MetricReplayFidelity: fidelity},
	}
}

// headroomEvent constrói um wide event com o headroom reservável no bag.
func headroomEvent(runID string, tokens int64) WideEvent {
	return WideEvent{
		RunID:      runID,
		TraceIDHex: runID,
		Attributes: map[string]any{MetricHeadroomFreeTokens: tokens},
	}
}

func ptrF(f float64) *float64 { return &f }
func ptrB(b bool) *bool       { return &b }

// --- AC6: dashboard-as-code — round-trip JSON reproduzível --------------------

func TestDefaultCatalogIsValidAndCoversSevenCanonicalSLIs(t *testing.T) {
	c := DefaultDashboardCatalog()
	if err := c.Validate(); err != nil {
		t.Fatalf("catálogo default deve ser válido: %v", err)
	}
	if len(c.Panels) != 7 {
		t.Fatalf("esperava 7 painéis canónicos, veio %d", len(c.Panels))
	}
	// AC2: cada SLI canónico tem painel, com SLO e janela (AC5).
	byName := make(map[string]SLIPanel)
	for _, p := range c.Panels {
		byName[p.SLI] = p
		if p.Window == "" {
			t.Errorf("painel %s sem janela de avaliação (AC5)", p.SLI)
		}
		if p.SLO == 0 && p.SLI != SLIHeadroomTokens {
			// headroom com SLO 1 é o único perto de 0; todos declaram um SLO explícito.
			t.Errorf("painel %s sem SLO indicado (AC5)", p.SLI)
		}
	}
	for _, want := range []string{
		SLIControlPlaneAvailability, SLIMediationOverheadP95, SLISandboxColdStartP95,
		SLICacheHitRate, SLIHeadroomTokens, SLIReplayFidelity, SLIAuditWORMIntegrity,
	} {
		if _, ok := byName[want]; !ok {
			t.Errorf("SLI canónico %q sem painel (AC2)", want)
		}
	}
}

func TestCatalogPanelsSplitByPlane(t *testing.T) {
	c := DefaultDashboardCatalog()
	ctrl := c.PanelsByPlane(PlaneControl)
	data := c.PanelsByPlane(PlaneData)
	if len(ctrl) == 0 || len(data) == 0 {
		t.Fatalf("AC1: esperava painéis em ambos os planos, controlo=%d dados=%d", len(ctrl), len(data))
	}
	if len(ctrl)+len(data) != len(c.Panels) {
		t.Errorf("todos os painéis têm de cair num plano: %d+%d != %d", len(ctrl), len(data), len(c.Panels))
	}
	// Mapeamento estático produtor→plano coerente com os painéis.
	for _, p := range c.Panels {
		if pl, ok := PlaneForProducer(p.Producer); ok && pl != p.Plane {
			t.Errorf("painel %s: plano %q diverge do produtor %s (%q)", p.SLI, p.Plane, p.Producer, pl)
		}
	}
}

func TestCatalogRoundTripJSONReproducible(t *testing.T) {
	c := DefaultDashboardCatalog()
	raw, err := c.JSON()
	if err != nil {
		t.Fatalf("serialização falhou: %v", err)
	}
	got, err := LoadDashboardCatalog(raw)
	if err != nil {
		t.Fatalf("desserialização fail-closed rejeitou o próprio artefacto: %v", err)
	}
	if !reflect.DeepEqual(c, got) {
		t.Fatalf("round-trip não é idêntico:\n want %+v\n  got %+v", c, got)
	}
	// Reproduzível: serializar o desserializado dá bytes idênticos.
	raw2, err := got.JSON()
	if err != nil {
		t.Fatalf("re-serialização falhou: %v", err)
	}
	if !bytes.Equal(raw, raw2) {
		t.Errorf("serialização não determinista (bytes divergem no round-trip)")
	}
}

func TestEmbeddedDashboardMatchesCode(t *testing.T) {
	// O artefacto versionado no repo tem de reproduzir EXACTAMENTE o catálogo do código
	// (dashboard-as-code, AC6: reproduzível a partir do código; ficheiro e código não
	// divergem).
	fromFile, err := EmbeddedDashboardCatalog()
	if err != nil {
		t.Fatalf("catálogo embebido inválido: %v", err)
	}
	if !reflect.DeepEqual(DefaultDashboardCatalog(), fromFile) {
		t.Fatalf("operational_dashboard.json diverge de DefaultDashboardCatalog() — regenerar o artefacto")
	}
	// O embed tem de ser byte-idêntico ao gerado (mais o newline final do ficheiro).
	gen, _ := DefaultDashboardCatalog().JSON()
	if !bytes.Equal(bytes.TrimRight(EmbeddedDashboardJSON(), "\n"), gen) {
		t.Errorf("bytes do artefacto embebido divergem da serialização do código")
	}
}

func TestLoadCatalogFailClosed(t *testing.T) {
	base := DefaultDashboardCatalog()
	// SemVer inválido.
	bad := base
	bad.Version = "v1"
	if raw, _ := bad.JSON(); mustReject(t, raw) {
	}
	// Painel com plano inválido.
	bad2 := base
	bad2.Panels = append([]SLIPanel(nil), base.Panels...)
	bad2.Panels[0].Plane = "edge"
	if raw, _ := bad2.JSON(); mustReject(t, raw) {
	}
	// Falta um SLI canónico (remove o primeiro painel).
	bad3 := base
	bad3.Panels = base.Panels[1:]
	if raw, _ := bad3.JSON(); mustReject(t, raw) {
	}
	// JSON malformado.
	mustReject(t, []byte(`{not json`))
}

func TestCatalogValidateBranches(t *testing.T) {
	good := DefaultDashboardCatalog().Panels
	full := func() []SLIPanel { return append([]SLIPanel(nil), good...) }
	cases := map[string]DashboardCatalog{
		"sem painéis":        {Version: "1.0.0", Panels: nil},
		"SLI vazio":          {Version: "1.0.0", Panels: append(full(), SLIPanel{SLI: "", Plane: PlaneData, Direction: DirMin, Window: "1m"})},
		"plano inválido":     {Version: "1.0.0", Panels: append(full(), SLIPanel{SLI: "x", Plane: "nope", Direction: DirMin, Window: "1m"})},
		"direcção inválida":  {Version: "1.0.0", Panels: append(full(), SLIPanel{SLI: "x", Plane: PlaneData, Direction: "sideways", Window: "1m"})},
		"sem janela":         {Version: "1.0.0", Panels: append(full(), SLIPanel{SLI: "x", Plane: PlaneData, Direction: DirMin, Window: ""})},
		"SLI duplicado":      {Version: "1.0.0", Panels: append(full(), good[0])},
		"falta SLI canónico": {Version: "1.0.0", Panels: good[1:]},
		"SemVer inválido":    {Version: "x", Panels: full()},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := c.Validate(); err == nil {
				t.Errorf("catálogo inválido (%s) devia ser rejeitado fail-closed", name)
			}
		})
	}
	// O catálogo default é válido (o caminho feliz de todas as guardas).
	if err := DefaultDashboardCatalog().Validate(); err != nil {
		t.Errorf("catálogo default devia ser válido: %v", err)
	}
}

func mustReject(t *testing.T, raw []byte) bool {
	t.Helper()
	if _, err := LoadDashboardCatalog(raw); err == nil {
		t.Errorf("catálogo inválido devia ser rejeitado fail-closed: %s", raw)
	}
	return true
}

// --- AC2/AC4: renderização query-time contra dados sintéticos -----------------

func TestRenderCoversEverySLIWithSLOAndWindow(t *testing.T) {
	c := DefaultDashboardCatalog()
	in := OperationalInputs{
		Events: []WideEvent{
			// cache-hit alto (cumpre 0.80).
			{Operation: OpChat, TraceIDHex: "t1", RunID: "run-1", InputTokens: 1000,
				Attributes: map[string]any{AttrCacheHitRate: 0.95}},
			// overhead baixo (cumpre 15ms).
			{Operation: OpExecuteTool, TraceIDHex: "t1", RunID: "run-1", LatencyNanos: int64(3 * time.Millisecond)},
			// cold-start baixo (cumpre 125ms).
			coldStartEvent("run-1", 40),
			// headroom saudável (cumpre >= 1).
			headroomEvent("run-1", 5000),
			// replay perfeito (cumpre 1.0).
			replayEvent("run-1", 1.0),
		},
		ControlPlaneAvailability: ptrF(0.9995), // injectado, cumpre 0.999.
		AuditWORMIntact:          ptrB(true),   // injectado, íntegro.
	}
	snap := c.Render(in)

	if snap.CatalogVersion != c.Version {
		t.Errorf("snapshot com versão %q, quer %q", snap.CatalogVersion, c.Version)
	}
	if len(snap.Panels) != len(c.Panels) {
		t.Fatalf("render devia produzir um painel por SLI: %d vs %d", len(snap.Panels), len(c.Panels))
	}
	// Cada painel renderizado traz SLO e janela (AC5) e está AVALIADO (tem dados).
	for _, rp := range snap.Panels {
		if rp.Panel.Window == "" {
			t.Errorf("%s renderizado sem janela", rp.Panel.SLI)
		}
		if rp.SLI.SLO != rp.Panel.SLO {
			t.Errorf("%s: SLO do valor (%v) diverge do painel (%v)", rp.Panel.SLI, rp.SLI.SLO, rp.Panel.SLO)
		}
		if !rp.SLI.Evaluated() {
			t.Errorf("%s devia estar avaliado com os dados sintéticos", rp.Panel.SLI)
		}
		if !rp.SLI.Met {
			t.Errorf("%s devia cumprir o SLO com dados saudáveis (val=%v slo=%v dir=%s)",
				rp.Panel.SLI, rp.SLI.Value, rp.SLI.SLO, rp.SLI.Direction)
		}
	}
	// AC1: dashboards por plano.
	if len(snap.ByPlane(PlaneControl)) == 0 || len(snap.ByPlane(PlaneData)) == 0 {
		t.Errorf("esperava painéis renderizados em ambos os planos")
	}
	if len(snap.Breaches()) != 0 {
		t.Errorf("dados saudáveis não deviam ter breaches, veio %d", len(snap.Breaches()))
	}
}

func TestRenderBreachesAndDrillDown(t *testing.T) {
	c := DefaultDashboardCatalog()
	in := OperationalInputs{
		Events: []WideEvent{
			// cold-start acima do tecto (200ms > 125ms) no run "slow".
			coldStartEvent("slow", 200),
			coldStartEvent("ok", 30),
			// replay degradado (0.5 < 1.0) no run "regressed".
			replayEvent("regressed", 0.5),
			replayEvent("clean", 1.0),
			// headroom esgotado (0 < 1) no run "starved".
			headroomEvent("starved", 0),
		},
		ControlPlaneAvailability: ptrF(0.90),  // abaixo de 0.999 ⇒ breach.
		AuditWORMIntact:          ptrB(false), // adulterado ⇒ breach.
	}
	snap := c.Render(in)

	// cold-start em breach com drill-down para o run ofensor.
	assertPanelBreached(t, snap, SLISandboxColdStartP95, "slow")
	assertPanelBreached(t, snap, SLIReplayFidelity, "regressed")
	assertPanelBreached(t, snap, SLIHeadroomTokens, "starved")

	// disponibilidade injectada em breach.
	cp, _ := snap.Panel(SLIControlPlaneAvailability)
	if !cp.SLI.Breached() || cp.SLI.Value != 0.90 {
		t.Errorf("disponibilidade injectada devia estar em breach (val=%v)", cp.SLI.Value)
	}
	// audit WORM adulterado ⇒ breach, valor 0.
	aw, _ := snap.Panel(SLIAuditWORMIntegrity)
	if !aw.SLI.Breached() || aw.SLI.Value != 0 {
		t.Errorf("audit WORM adulterado devia estar em breach com valor 0 (veio %v)", aw.SLI.Value)
	}
}

func assertPanelBreached(t *testing.T, snap OperationalSnapshot, sli, wantOffender string) {
	t.Helper()
	rp, ok := snap.Panel(sli)
	if !ok {
		t.Fatalf("painel %s ausente do snapshot", sli)
	}
	if !rp.SLI.Breached() {
		t.Errorf("%s devia estar em breach (val=%v slo=%v)", sli, rp.SLI.Value, rp.SLI.SLO)
		return
	}
	found := false
	for _, o := range rp.SLI.Offenders {
		if o == wantOffender {
			found = true
		}
	}
	if !found {
		t.Errorf("%s: drill-down não apontou o run ofensor %q (veio %v)", sli, wantOffender, rp.SLI.Offenders)
	}
}

// --- AC6/anti-vacuidade: os dois SLIs sem produtor injectam-se honestamente ----

func TestGapSLIsNotEvaluatedWithoutInjection(t *testing.T) {
	c := DefaultDashboardCatalog()
	// Sem injecção nem wide events: os SLIs sem produtor (disponibilidade, audit WORM) e
	// os derivados de wide events ficam NÃO AVALIADOS — nunca fabricam valor nem breach.
	snap := c.Render(OperationalInputs{})
	for _, sli := range []string{
		SLIControlPlaneAvailability, SLIAuditWORMIntegrity, SLISandboxColdStartP95,
		SLIHeadroomTokens, SLIReplayFidelity,
	} {
		rp, _ := snap.Panel(sli)
		if rp.SLI.Evaluated() {
			t.Errorf("%s não devia estar avaliado sem dados/injecção", sli)
		}
		if rp.SLI.Breached() {
			t.Errorf("%s não devia estar em breach sem dados (anti-vacuidade)", sli)
		}
	}
	if len(snap.Breaches()) != 0 {
		t.Errorf("sem dados nem injecção não deve haver breaches, veio %d", len(snap.Breaches()))
	}
}

func TestGapAndProducerFallbackInjection(t *testing.T) {
	c := DefaultDashboardCatalog()
	// Fallback injectado para os produtores ausentes dos wide events.
	in := OperationalInputs{
		ColdStartFallbackP95Ms:   ptrF(300), // > 125 ⇒ breach via fallback.
		HeadroomFallbackTokens:   ptrF(10),  // >= 1 ⇒ cumpre.
		ReplayFidelityFallback:   ptrF(1.0),
		ControlPlaneAvailability: ptrF(0.9995),
		AuditWORMIntact:          ptrB(true),
	}
	snap := c.Render(in)
	cs, _ := snap.Panel(SLISandboxColdStartP95)
	if !cs.SLI.Evaluated() || cs.SLI.Met {
		t.Errorf("cold-start via fallback 300ms devia violar o SLO 125ms (avaliado=%v met=%v)", cs.SLI.Evaluated(), cs.SLI.Met)
	}
	hr, _ := snap.Panel(SLIHeadroomTokens)
	if !hr.SLI.Evaluated() || !hr.SLI.Met {
		t.Errorf("headroom via fallback 10 devia cumprir >= 1")
	}
}

// --- AC3: custo por span agregável por run e por tenant, reconciliando chaves ---

func TestCostByRunAndTenantReconcilesTenantKeys(t *testing.T) {
	events := []WideEvent{
		// tenant via aos.tenant_id (otel-genai) — derivado no campo TenantID.
		{RunID: "run-A", TenantID: "acme", CostMicroUSD: 100_000,
			Attributes: map[string]any{AttrTenantID: "acme"}},
		{RunID: "run-A", TenantID: "acme", CostMicroUSD: 50_000,
			Attributes: map[string]any{AttrTenantID: "acme"}},
		// tenant via aos.tenant (Model Gateway) — DIVERGENTE; TenantOf reconcilia.
		{RunID: "run-B", CostMicroUSD: 30_000,
			Attributes: map[string]any{AttrTenantAlt: "acme"}},
		// tenant diferente.
		{RunID: "run-C", TenantID: "globex", CostMicroUSD: 70_000,
			Attributes: map[string]any{AttrTenantID: "globex"}},
	}

	// Por run.
	byRun := CostByRun(events)
	if byRun["run-A"].CostMicroUSD != 150_000 {
		t.Errorf("custo run-A = %d, quer 150000", byRun["run-A"].CostMicroUSD)
	}
	if got := byRun["run-A"].CostUSD(); got != 0.15 {
		t.Errorf("custo run-A em USD = %v, quer 0.15", got)
	}

	// Por tenant: acme reconcilia aos.tenant_id + aos.tenant (100k+50k+30k=180k).
	byTenant := CostByTenant(events)
	if byTenant["acme"].CostMicroUSD != 180_000 {
		t.Errorf("custo tenant acme = %d, quer 180000 (reconciliação de chaves falhou)", byTenant["acme"].CostMicroUSD)
	}
	if byTenant["globex"].CostMicroUSD != 70_000 {
		t.Errorf("custo tenant globex = %d, quer 70000", byTenant["globex"].CostMicroUSD)
	}

	// Composto run+tenant.
	byBoth := CostByRunAndTenant(events)
	var acmeA int64
	for k, u := range byBoth {
		run, tenant := SplitRunTenantKey(k)
		if run == "run-A" && tenant == "acme" {
			acmeA = u.CostMicroUSD
		}
	}
	if acmeA != 150_000 {
		t.Errorf("custo run-A/acme = %d, quer 150000", acmeA)
	}
}

// --- AC1/AC3: vista por run com drill-down span→tool call e custo por span -----

func TestBuildRunViewDrillDownToToolCall(t *testing.T) {
	// Um run (trace 0xA1): invoke_agent → chat (custo) + execute_tool (a tool call).
	root := agentSpan(0xA1, 0x01, 0x00, 0, 0, 0)
	chat := chatSpan(0xA1, 0x02, 0x01, 1000, 500, 120_000)
	tool := toolSpan(0xA1, 0x03, 0x01) // ToolName "search"

	traceHexA1 := SpanContext{TraceID: traceID(0xA1)}.TraceIDHex()
	events := []WideEvent{
		WideEventFromSpanData(chat, map[string]any{AttrRunID: "run-1", AttrCacheHitRate: 0.9}),
		WideEventFromSpanData(tool, map[string]any{AttrRunID: "run-1"}),
	}
	// Assegura a correlação run→trace nos wide events.
	for i := range events {
		events[i].RunID = "run-1"
		events[i].TraceIDHex = traceHexA1
	}

	rv := BuildRunView("run-1", events, []SpanData{root, chat, tool}, DefaultSLOConfig())

	if len(rv.TraceIDs) != 1 || rv.TraceIDs[0] != traceHexA1 {
		t.Fatalf("run devia resolver 1 trace (%s), veio %v", traceHexA1, rv.TraceIDs)
	}
	// Custo total do run = só o chat (sem dupla-contagem) = 120_000.
	if rv.TotalCostMicroUSD != 120_000 {
		t.Errorf("custo total do run = %d, quer 120000", rv.TotalCostMicroUSD)
	}
	if rv.TotalCostUSD != 0.12 {
		t.Errorf("custo total do run em USD = %v, quer 0.12", rv.TotalCostUSD)
	}
	// Drill-down: a tool call individual aparece com o seu nome, custo por span visível.
	var sawTool, sawChatCost bool
	for _, sc := range rv.Spans {
		if sc.Operation == OpExecuteTool && sc.ToolName == "search" {
			sawTool = true
		}
		if sc.Operation == OpChat && sc.CostMicroUSD == 120_000 && sc.CostUSD == 0.12 {
			sawChatCost = true
		}
	}
	if !sawTool {
		t.Errorf("drill-down não chegou à tool call individual 'search' (spans=%+v)", rv.Spans)
	}
	if !sawChatCost {
		t.Errorf("custo por span do chat não visível no drill-down")
	}
	// Rollup por sub-árvore: o invoke_agent tem OWN = o chat filho directo.
	if len(rv.Rollup) != 1 {
		t.Fatalf("esperava 1 rollup de trace, veio %d", len(rv.Rollup))
	}
	rootHex := SpanContext{SpanID: spanID(0x01)}.SpanIDHex()
	if own := rv.Rollup[0].OwnByAgent[rootHex]; own.CostMicroUSD != 120_000 {
		t.Errorf("OWN do invoke_agent = %d, quer 120000 (drill-down por sub-árvore)", own.CostMicroUSD)
	}
}

// --- Custo por span: o custo emitido no span é lido em USD (AC3) ---------------

func TestSpanCostReadsUSDPerSpan(t *testing.T) {
	c := chatSpan(0xB2, 0x01, 0x00, 100, 100, 2_500_000) // 2.5 USD
	rv := BuildRunView(SpanContext{TraceID: traceID(0xB2)}.TraceIDHex(), nil, []SpanData{c}, DefaultSLOConfig())
	if len(rv.Spans) != 1 {
		t.Fatalf("esperava 1 span, veio %d", len(rv.Spans))
	}
	if rv.Spans[0].CostUSD != 2.5 {
		t.Errorf("custo por span = %v USD, quer 2.5", rv.Spans[0].CostUSD)
	}
}

// --- casos-limite: leitura float, chaves sem separador, run→trace fallback -----

func TestRenderReadsFloatProducerMetrics(t *testing.T) {
	// Os produtores (sandbox/scheduler) podem emitir os valores como float; attrNumeric
	// tem de os ler. Cold-start 130.0 (float) > 125 ⇒ breach.
	c := DefaultDashboardCatalog()
	in := OperationalInputs{
		Events: []WideEvent{
			{RunID: "r", TraceIDHex: "r", Attributes: map[string]any{MetricSandboxColdStartMs: 130.0}},
			// headroom float, e um valor não-numérico que deve ser ignorado.
			{RunID: "r", TraceIDHex: "r", Attributes: map[string]any{MetricHeadroomFreeTokens: 42.0}},
			{RunID: "r", TraceIDHex: "r", Attributes: map[string]any{MetricHeadroomFreeTokens: "n/a"}},
		},
	}
	snap := c.Render(in)
	cs, _ := snap.Panel(SLISandboxColdStartP95)
	if !cs.SLI.Evaluated() || cs.SLI.Met {
		t.Errorf("cold-start float 130ms devia violar 125ms (avaliado=%v met=%v)", cs.SLI.Evaluated(), cs.SLI.Met)
	}
	hr, _ := snap.Panel(SLIHeadroomTokens)
	if hr.SLI.Samples != 1 { // só o 42.0 conta; "n/a" ignorado.
		t.Errorf("headroom devia ter 1 amostra numérica, veio %d", hr.SLI.Samples)
	}
}

func TestEdgeHelpers(t *testing.T) {
	// SplitRunTenantKey sem separador ⇒ (chave, "").
	if run, tenant := SplitRunTenantKey("plain"); run != "plain" || tenant != "" {
		t.Errorf("split sem separador = (%q,%q), quer (plain,\"\")", run, tenant)
	}
	// runKeyOf sem RunID cai no trace_id.
	if k := runKeyOf(WideEvent{TraceIDHex: "tr-9"}); k != "tr-9" {
		t.Errorf("runKeyOf fallback trace = %q, quer tr-9", k)
	}
	// Panel ausente ⇒ ok=false.
	snap := DefaultDashboardCatalog().Render(OperationalInputs{})
	if _, ok := snap.Panel("inexistente"); ok {
		t.Errorf("Panel de SLI inexistente devia devolver ok=false")
	}
	// TenantOf sem qualquer chave ⇒ "".
	if got := TenantOf(WideEvent{}); got != "" {
		t.Errorf("TenantOf sem tenant = %q, quer \"\"", got)
	}
}

// --- verificação do JSON: sem segredos (só nomes/SLO/janelas) ------------------

func TestArtifactHasNoSecrets(t *testing.T) {
	raw := EmbeddedDashboardJSON()
	var forbidden = []string{"password", "secret", "token=", "apikey", "api_key", "bearer", "BEGIN "}
	lower := bytes.ToLower(raw)
	for _, f := range forbidden {
		if bytes.Contains(lower, bytes.ToLower([]byte(f))) {
			t.Errorf("artefacto de dashboard contém possível segredo %q", f)
		}
	}
	// Confirma que é JSON válido e sem valores sensíveis (só metadados de painel).
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("artefacto não é JSON válido: %v", err)
	}
}
