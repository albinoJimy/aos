package hipercare

import (
	"testing"
	"time"

	dr "github.com/aos-ref/platform/dr"
	runbooks "github.com/aos-ref/platform/runbooks"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ---------------------------------------------------------------------------
// Fixtures sintéticas — compõem os tipos REAIS das peças (catálogo de AOS-104,
// GameDayEvidence de AOS-102, CanonicalIDs de AOS-106) sem reimplementar nada.
// ---------------------------------------------------------------------------

// okValueFor devolve um SLIValue que CUMPRE o SLO do painel real e está AVALIADO
// (Samples>0). Value==SLO satisfaz DirMin (>=) e DirMax (<=); Met=true, sem breach.
func okValueFor(p otelgenai.SLIPanel) otelgenai.SLIValue {
	return otelgenai.SLIValue{
		Name:      p.SLI,
		Value:     p.SLO,
		SLO:       p.SLO,
		Direction: p.Direction,
		Met:       true,
		Samples:   3,
	}
}

// metSnapshot renderiza um OperationalSnapshot em que TODOS os painéis canónicos de
// AOS-104 estão avaliados e cumprem o SLO — o estado "tudo verde" da janela.
func metSnapshot() otelgenai.OperationalSnapshot {
	cat := otelgenai.DefaultDashboardCatalog()
	snap := otelgenai.OperationalSnapshot{CatalogVersion: cat.Version}
	for _, p := range cat.Panels {
		snap.Panels = append(snap.Panels, otelgenai.RenderedPanel{Panel: p, SLI: okValueFor(p)})
	}
	return snap
}

// setSLI substitui o SLIValue de um painel do snapshot (para as mutações negativas).
func setSLI(snap otelgenai.OperationalSnapshot, sli string, v otelgenai.SLIValue) otelgenai.OperationalSnapshot {
	out := otelgenai.OperationalSnapshot{CatalogVersion: snap.CatalogVersion}
	for _, rp := range snap.Panels {
		if rp.Panel.SLI == sli {
			v.Name = sli
			v.SLO = rp.SLI.SLO
			v.Direction = rp.SLI.Direction
			rp.SLI = v
		}
		out.Panels = append(out.Panels, rp)
	}
	return out
}

// okRunbooks devolve uma validação COM MTTR para cada runbook canónico de AOS-106.
func okRunbooks() []RunbookValidation {
	var out []RunbookValidation
	for i, id := range runbooks.CanonicalIDs() {
		out = append(out, RunbookValidation{
			RunbookID:   id,
			Title:       "runbook " + id,
			Validated:   true,
			Simulated:   true,
			MTTR:        time.Duration(5+i) * time.Minute,
			IncidentRef: "GD-2026-07-" + id,
		})
	}
	return out
}

// okDR devolve uma GameDayEvidence que passou com RPO/RTO dentro do alvo.
func okDR() DRRevalidation {
	return DRRevalidation{Evidence: dr.GameDayEvidence{
		At:        time.Date(2026, 7, 18, 3, 0, 0, 0, time.UTC),
		RPOWindow: 30 * time.Second,
		RPOTarget: time.Minute,
		RPOWithin: true,
		RTOTarget: 30 * time.Minute,
		RTOWithin: true,
		Passed:    true,
	}}
}

// okCalibration devolve uma calibração conduzida que reduziu o ruído.
func okCalibration() AlertCalibration {
	return AlertCalibration{
		AlertConfigVersion:  "1.0.0",
		FalsePositiveBefore: 0.22,
		FalsePositiveAfter:  0.06,
		OverrideRate:        0.03,
		GateEscapeRate:      0.0,
		Calibrated:          true,
		ThresholdChanges: []ThresholdChange{
			{Alert: otelgenai.AlertSandboxColdStartP95High, Field: "sustained_windows", Before: "3", After: "5",
				Rationale: "p95 de cold-start ruidoso na janela; alargar a janela sustentada reduz páginas"},
		},
	}
}

// fullReport monta um relatório COMPLETO (tudo-cumprido) — o caso (a).
func fullReport() HipercareReport {
	return HipercareReport{
		Version:          ReportVersion,
		Board:            "board-eu",
		WindowStart:      time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC),
		WindowEnd:        time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
		SLOConformance:   SLOConformanceFromSnapshot("14d", metSnapshot()),
		Runbooks:         okRunbooks(),
		AlertCalibration: okCalibration(),
		DR:               okDR(),
		DORA: DORAMetrics{
			DeployFrequencyPerWeek: 4,
			ChangeFailureRate:      0.08,
			MTTR:                   7 * time.Minute,
			LeadTime:               48 * time.Hour,
		},
		FollowUps: []FollowUpAction{
			{ID: "FU-01", Owner: "SRE", Summary: "Cablar audit.Verify ao SLI de integridade WORM", Priority: "P2"},
		},
	}
}

// ---------------------------------------------------------------------------
// (a) tudo-cumprido ⇒ CanExit=true, relatório completo.
// ---------------------------------------------------------------------------

func TestCanExit_AllMet(t *testing.T) {
	r := fullReport()
	ec := r.ExitCriteria()
	if !ec.CanExit() {
		t.Fatalf("esperava CanExit=true; missing=%v", ec.Missing)
	}
	if !r.CanExit() {
		t.Fatal("atalho HipercareReport.CanExit devia ser true")
	}
	if len(ec.Missing) != 0 {
		t.Fatalf("esperava Missing vazio; got %v", ec.Missing)
	}
	if !ec.SLOsSustained || !ec.RunbooksValidated || !ec.AlertsCalibrated || !ec.DRRevalidated {
		t.Fatalf("esperava todos os critérios true; got %+v", ec)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("relatório completo devia ser válido: %v", err)
	}
}

// ---------------------------------------------------------------------------
// (b) um-SLO-em-breach ⇒ CanExit=false (fail-closed, lista o SLO).
// ---------------------------------------------------------------------------

func TestCannotExit_SLOBreach(t *testing.T) {
	snap := setSLI(metSnapshot(), otelgenai.SLIMediationOverheadP95,
		otelgenai.SLIValue{Value: 1e12, Met: false, Samples: 5}) // avaliado e falha ⇒ breach
	r := fullReport()
	r.SLOConformance = SLOConformanceFromSnapshot("14d", snap)

	ec := r.ExitCriteria()
	if ec.CanExit() {
		t.Fatal("um SLO em breach TEM de impedir o encerramento (fail-closed)")
	}
	if ec.SLOsSustained {
		t.Fatal("SLOsSustained devia ser false com um SLO em breach")
	}
	if !containsSub(ec.Missing, otelgenai.SLIMediationOverheadP95) {
		t.Fatalf("Missing devia nomear o SLO em breach; got %v", ec.Missing)
	}
	// Os outros critérios continuam cumpridos — a falha é isolada ao SLO.
	if !ec.RunbooksValidated || !ec.AlertsCalibrated || !ec.DRRevalidated {
		t.Fatalf("só o critério de SLOs devia falhar; got %+v", ec)
	}
}

// ---------------------------------------------------------------------------
// (c) um-runbook-sem-MTTR ⇒ CanExit=false.
// ---------------------------------------------------------------------------

func TestCannotExit_RunbookWithoutMTTR(t *testing.T) {
	r := fullReport()
	// Zera o MTTR do primeiro runbook canónico (validado mas sem tempo medido).
	target := r.Runbooks[0].RunbookID
	r.Runbooks[0].MTTR = 0

	ec := r.ExitCriteria()
	if ec.CanExit() {
		t.Fatal("um runbook validado SEM MTTR não conta — não se pode encerrar")
	}
	if ec.RunbooksValidated {
		t.Fatal("RunbooksValidated devia ser false")
	}
	if !containsSub(ec.Missing, target) || !containsSub(ec.Missing, "MTTR") {
		t.Fatalf("Missing devia nomear o runbook e o MTTR em falta; got %v", ec.Missing)
	}
}

// (c') um-runbook-ausente ⇒ CanExit=false (registo em falta).
func TestCannotExit_RunbookMissing(t *testing.T) {
	r := fullReport()
	r.Runbooks = r.Runbooks[1:] // remove o primeiro canónico
	ec := r.ExitCriteria()
	if ec.CanExit() || ec.RunbooksValidated {
		t.Fatalf("um runbook canónico sem registo tem de impedir o fecho; %+v", ec)
	}
}

// ---------------------------------------------------------------------------
// (d) DR fora do RPO/RTO ⇒ CanExit=false.
// ---------------------------------------------------------------------------

func TestCannotExit_DROutsideRPO(t *testing.T) {
	r := fullReport()
	r.DR.Evidence.RPOWithin = false
	r.DR.Evidence.Passed = false // um game day fora do RPO não passa

	ec := r.ExitCriteria()
	if ec.CanExit() {
		t.Fatal("DR fora do RPO tem de impedir o encerramento")
	}
	if ec.DRRevalidated {
		t.Fatal("DRRevalidated devia ser false")
	}
	if !containsSub(ec.Missing, "DR") {
		t.Fatalf("Missing devia nomear o DR; got %v", ec.Missing)
	}
}

func TestCannotExit_DROutsideRTO(t *testing.T) {
	r := fullReport()
	r.DR.Evidence.RTOWithin = false
	r.DR.Evidence.Passed = false
	if r.CanExit() {
		t.Fatal("DR fora do RTO tem de impedir o encerramento")
	}
}

// ---------------------------------------------------------------------------
// (e) SLO não-avaliado (Samples==0) ⇒ CanExit=false (anti-vacuidade).
// ---------------------------------------------------------------------------

func TestCannotExit_SLONotEvaluated(t *testing.T) {
	// Met=true mas Samples==0 ⇒ NÃO avaliado. Não se encerra sobre ausência de dados.
	snap := setSLI(metSnapshot(), otelgenai.SLIReplayFidelity,
		otelgenai.SLIValue{Value: 1.0, Met: true, Samples: 0})
	r := fullReport()
	r.SLOConformance = SLOConformanceFromSnapshot("14d", snap)

	ec := r.ExitCriteria()
	if ec.CanExit() {
		t.Fatal("um SLO não avaliado (Samples==0) NÃO conta como cumprido — fail-closed")
	}
	if ec.SLOsSustained {
		t.Fatal("SLOsSustained devia ser false sobre um SLO não avaliado")
	}
	if !containsSub(ec.Missing, "não avaliado") {
		t.Fatalf("Missing devia explicar a ausência de dados; got %v", ec.Missing)
	}
}

// (e') SLO de saída AUSENTE do snapshot ⇒ CanExit=false.
func TestCannotExit_ExitSLOMissingFromSnapshot(t *testing.T) {
	// Snapshot só com um painel não-gate: falta a cobertura dos SLOs de saída.
	snap := otelgenai.OperationalSnapshot{CatalogVersion: "1.0.0"}
	r := fullReport()
	r.SLOConformance = SLOConformanceFromSnapshot("14d", snap)
	ec := r.ExitCriteria()
	if ec.CanExit() || ec.SLOsSustained {
		t.Fatalf("SLOs de saída ausentes têm de impedir o fecho; %+v", ec)
	}
	if !containsSub(ec.Missing, "ausente") {
		t.Fatalf("Missing devia sinalizar SLO ausente; got %v", ec.Missing)
	}
}

// ---------------------------------------------------------------------------
// AC4 — alertas não calibrados / ruído agravado ⇒ CanExit=false.
// ---------------------------------------------------------------------------

func TestCannotExit_AlertsNotCalibrated(t *testing.T) {
	r := fullReport()
	r.AlertCalibration.Calibrated = false
	if r.CanExit() {
		t.Fatal("alertas não calibrados têm de impedir o fecho")
	}
}

func TestCannotExit_AlertsNoiseWorsened(t *testing.T) {
	r := fullReport()
	r.AlertCalibration.FalsePositiveAfter = 0.9 // pior do que antes (0.22)
	ec := r.ExitCriteria()
	if ec.CanExit() || ec.AlertsCalibrated {
		t.Fatalf("calibração que AGRAVA o ruído não conta; %+v", ec)
	}
}

// ---------------------------------------------------------------------------
// Round-trip JSON reprodutível do relatório.
// ---------------------------------------------------------------------------

func TestJSONRoundTrip(t *testing.T) {
	r := fullReport()
	raw, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	back, err := LoadHipercareReport(raw)
	if err != nil {
		t.Fatalf("LoadHipercareReport: %v", err)
	}
	raw2, err := back.JSON()
	if err != nil {
		t.Fatalf("JSON (2): %v", err)
	}
	if string(raw) != string(raw2) {
		t.Fatal("round-trip JSON não é reprodutível (bytes divergentes)")
	}
	// O veredicto sobrevive ao round-trip.
	if back.CanExit() != r.CanExit() {
		t.Fatal("CanExit divergiu após round-trip")
	}
	// Campos representativos preservados.
	if back.DR.Evidence.RPOTarget != r.DR.Evidence.RPOTarget {
		t.Fatalf("RPOTarget não sobreviveu: %v vs %v", back.DR.Evidence.RPOTarget, r.DR.Evidence.RPOTarget)
	}
	if len(back.Runbooks) != len(r.Runbooks) || back.Runbooks[0].MTTR != r.Runbooks[0].MTTR {
		t.Fatal("registos de runbook não sobreviveram ao round-trip")
	}
}

func TestLoadRejectsMalformed(t *testing.T) {
	if _, err := LoadHipercareReport([]byte("{not json")); err == nil {
		t.Fatal("JSON malformado devia ser rejeitado (fail-closed)")
	}
	// Versão vazia é rejeitada.
	if _, err := LoadHipercareReport([]byte(`{"window_start":"2026-07-05T00:00:00Z"}`)); err == nil {
		t.Fatal("relatório sem versão devia ser rejeitado")
	}
	// Janela invertida é rejeitada.
	bad := `{"version":"1.0.0","window_start":"2026-07-19T00:00:00Z","window_end":"2026-07-05T00:00:00Z"}`
	if _, err := LoadHipercareReport([]byte(bad)); err == nil {
		t.Fatal("janela invertida devia ser rejeitada")
	}
}

// ---------------------------------------------------------------------------
// Composição real: SLOConformanceFromSnapshot lê um snapshot RENDERIZADO pelo
// catálogo de AOS-104 (prova que a projecção compõe o contrato real, não um mock).
// ---------------------------------------------------------------------------

func TestSLOConformance_ComposesRealRender(t *testing.T) {
	avail := 0.9995
	worm := true
	cold := 100.0
	head := 10.0
	fid := 1.0
	in := otelgenai.OperationalInputs{
		ControlPlaneAvailability: &avail,
		AuditWORMIntact:          &worm,
		ColdStartFallbackP95Ms:   &cold,
		HeadroomFallbackTokens:   &head,
		ReplayFidelityFallback:   &fid,
	}
	snap := otelgenai.DefaultDashboardCatalog().Render(in)
	conf := SLOConformanceFromSnapshot("continuous", snap)

	if conf.CatalogVersion != snap.CatalogVersion {
		t.Fatalf("versão do catálogo não propagou: %q vs %q", conf.CatalogVersion, snap.CatalogVersion)
	}
	// Os SLIs injectados vieram avaliados e cumpridos do Render real.
	e, ok := conf.entryFor(otelgenai.SLIControlPlaneAvailability)
	if !ok || !e.Evaluated || !e.Met || !e.Required {
		t.Fatalf("disponibilidade do plano de controlo devia vir avaliada+cumprida+required: %+v", e)
	}
	// Os SLIs SEM produtor nem fallback (overhead, cache-hit) ficam NÃO avaliados no
	// Render real sem wide events — a projecção reflecte-o honestamente (anti-vacuidade).
	oh, _ := conf.entryFor(otelgenai.SLIMediationOverheadP95)
	if oh.Evaluated {
		t.Fatal("sem wide events, o overhead p95 devia ficar NÃO avaliado")
	}
	// E por isso este snapshot real (parcial) NÃO permite encerrar — anti-vacuidade.
	r := fullReport()
	r.SLOConformance = conf
	if r.CanExit() {
		t.Fatal("um snapshot com SLOs de saída não avaliados não pode encerrar o hipercare")
	}
}

func TestCanonicalExitSLIs_Stable(t *testing.T) {
	got := CanonicalExitSLIs()
	if len(got) != 5 {
		t.Fatalf("esperava 5 SLOs de saída (AC2); got %d: %v", len(got), got)
	}
	// Mutar a cópia não afecta o interno.
	got[0] = "x"
	if CanonicalExitSLIs()[0] == "x" {
		t.Fatal("CanonicalExitSLIs devia devolver uma cópia")
	}
}

// containsSub indica se algum elemento de xs contém a substring sub.
func containsSub(xs []string, sub string) bool {
	for _, x := range xs {
		if len(sub) == 0 {
			return true
		}
		if indexOf(x, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
