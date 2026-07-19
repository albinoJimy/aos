package hipercare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	dr "github.com/aos-ref/platform/dr"
	runbooks "github.com/aos-ref/platform/runbooks"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ReportVersion é o SemVer do artefacto de relatório de hipercare (esquema dos tipos
// deste módulo). Sobe quando o formato serializado muda de forma incompatível.
const ReportVersion = "1.0.0"

// ---------------------------------------------------------------------------
// AC2 — conformidade de SLOs sobre a janela (projecção do OperationalSnapshot).
// ---------------------------------------------------------------------------

// canonicalExitSLIs são os SLIs cujos SLOs a AC2 nomeia como critério de saída do
// hipercare (disponibilidade 99,9%; mediação p95 < 15 ms; cold-start < 125 ms;
// cache-hit-rate > 80%; fidelidade de replay 100%). São um subconjunto dos sete SLIs
// canónicos do catálogo de AOS-104 — os dois restantes (headroom, integridade WORM)
// aparecem no relatório mas NÃO são gate de saída por si (o audit WORM só o é como
// breach, ver [SLOConformance.HasBreach]). Nomes ESTÁVEIS re-exportados de otel-genai.
var canonicalExitSLIs = []string{
	otelgenai.SLIControlPlaneAvailability,
	otelgenai.SLIMediationOverheadP95,
	otelgenai.SLISandboxColdStartP95,
	otelgenai.SLICacheHitRate,
	otelgenai.SLIReplayFidelity,
}

// CanonicalExitSLIs devolve uma cópia dos SLIs que são critério de saída (AC2).
func CanonicalExitSLIs() []string {
	out := make([]string, len(canonicalExitSLIs))
	copy(out, canonicalExitSLIs)
	return out
}

// SLOConformanceEntry é a conformidade de UM painel de SLI na janela de hipercare:
// o valor observado projectado pelo [otelgenai.OperationalSnapshot], o SLO+direcção do
// painel, e — crucial para a anti-vacuidade — Samples/Evaluated/Met/Breached lidos do
// [otelgenai.SLIValue] SEM reinterpretação. Required marca os que são gate de saída.
type SLOConformanceEntry struct {
	SLI       string  `json:"sli"`
	Title     string  `json:"title"`
	Plane     string  `json:"plane"`
	Value     float64 `json:"value"`
	SLO       float64 `json:"slo"`
	Direction string  `json:"direction"`
	Window    string  `json:"window"`
	// Samples é o nº de amostras que suportam o valor (0 ⇒ não avaliado, anti-vacuidade).
	Samples int `json:"samples"`
	// Evaluated == Samples > 0 (materializado para o relatório serializado ser auto-contido).
	Evaluated bool `json:"evaluated"`
	// Met/Breached vêm de [otelgenai.SLIValue] (Breached ⟺ Evaluated && !Met).
	Met      bool `json:"met"`
	Breached bool `json:"breached"`
	// Required marca um SLO que é critério de saída do hipercare (AC2).
	Required bool `json:"required"`
}

// Sustained indica que este SLO, sendo critério de saída, está cumprido de forma
// SUSTENTADA na janela: avaliado (Samples>0, anti-vacuidade), cumprido (Met) e sem
// breach. Um SLO Required não avaliado devolve false — não se encerra sobre ausência.
func (e SLOConformanceEntry) Sustained() bool {
	return e.Required && e.Evaluated && e.Met && !e.Breached
}

// SLOConformance é o relatório de conformidade de SLOs sobre a janela (AC2): a
// projecção completa do [otelgenai.OperationalSnapshot] com a marca de quais são gate.
type SLOConformance struct {
	CatalogVersion string                `json:"catalog_version"`
	Window         string                `json:"window"`
	Entries        []SLOConformanceEntry `json:"entries"`
}

// SLOConformanceFromSnapshot projecta um [otelgenai.OperationalSnapshot] (renderizado
// pelo catálogo de AOS-104 sobre os dados da janela) num [SLOConformance], marcando os
// SLOs de saída (AC2). É uma LEITURA pura — não reavalia nem altera os SLIValue.
func SLOConformanceFromSnapshot(window string, snap otelgenai.OperationalSnapshot) SLOConformance {
	required := make(map[string]bool, len(canonicalExitSLIs))
	for _, s := range canonicalExitSLIs {
		required[s] = true
	}
	out := SLOConformance{CatalogVersion: snap.CatalogVersion, Window: window}
	for _, rp := range snap.Panels {
		out.Entries = append(out.Entries, SLOConformanceEntry{
			SLI:       rp.Panel.SLI,
			Title:     rp.Panel.Title,
			Plane:     string(rp.Panel.Plane),
			Value:     rp.SLI.Value,
			SLO:       rp.SLI.SLO,
			Direction: rp.SLI.Direction,
			Window:    rp.Panel.Window,
			Samples:   rp.SLI.Samples,
			Evaluated: rp.SLI.Evaluated(),
			Met:       rp.SLI.Met,
			Breached:  rp.SLI.Breached(),
			Required:  required[rp.Panel.SLI],
		})
	}
	return out
}

// HasBreach indica se QUALQUER painel avaliado está em breach (não só os de saída) — um
// breach em qualquer SLI canónico impede o encerramento (fail-closed).
func (c SLOConformance) HasBreach() bool {
	for _, e := range c.Entries {
		if e.Breached {
			return true
		}
	}
	return false
}

// entryFor devolve a entrada de um SLI (ok=false se ausente do snapshot).
func (c SLOConformance) entryFor(sli string) (SLOConformanceEntry, bool) {
	for _, e := range c.Entries {
		if e.SLI == sli {
			return e, true
		}
	}
	return SLOConformanceEntry{}, false
}

// ---------------------------------------------------------------------------
// AC3 — MTTR por runbook (composição de runbooks.CanonicalIDs).
// ---------------------------------------------------------------------------

// RunbookValidation é o registo do exercício de UM runbook durante o hipercare: se foi
// validado em incidente real ou simulado e o MTTR MEDIDO. Anti-vacuidade: MTTR<=0 (não
// medido) não conta como validado (ver [RunbookValidation.OK]).
type RunbookValidation struct {
	RunbookID string `json:"runbook_id"`
	Title     string `json:"title,omitempty"`
	// Validated indica que o runbook foi exercido e recuperou o incidente.
	Validated bool `json:"validated"`
	// Simulated distingue incidente SIMULADO (game-day/injecção) de REAL (só rótulo).
	Simulated bool `json:"simulated"`
	// MTTR é o tempo medido de recuperação (serializa como int64 nanos, round-trip).
	MTTR time.Duration `json:"mttr"`
	// IncidentRef é a referência ao incidente/exercício (rótulo, sem PII).
	IncidentRef string `json:"incident_ref,omitempty"`
}

// OK indica que o runbook foi validado COM MTTR medido (> 0). MTTR<=0 ⇒ não validado
// (anti-vacuidade: não se dá por eficaz um runbook sem tempo de recuperação medido).
func (r RunbookValidation) OK() bool { return r.Validated && r.MTTR > 0 }

// ---------------------------------------------------------------------------
// AC4 — calibração de alertas (ruído observado, override/gate-escape).
// ---------------------------------------------------------------------------

// ThresholdChange documenta UM ajuste de limiar/janela feito na calibração (rótulo).
type ThresholdChange struct {
	Alert     string `json:"alert"`
	Field     string `json:"field"`
	Before    string `json:"before"`
	After     string `json:"after"`
	Rationale string `json:"rationale,omitempty"`
}

// AlertCalibration é a calibração do alerting-as-code de AOS-105 com base no ruído
// observado (AC4): a taxa de falsos-positivos ANTES e DEPOIS, o override-rate e o gate
// escape rate medidos (specs/01 §9), e os ajustes de limiar aplicados.
type AlertCalibration struct {
	AlertConfigVersion  string  `json:"alert_config_version"`
	FalsePositiveBefore float64 `json:"false_positive_rate_before"`
	FalsePositiveAfter  float64 `json:"false_positive_rate_after"`
	// OverrideRate — % de gates de risco auto/rapidamente aprovados (anti rubber-stamping).
	OverrideRate float64 `json:"override_rate"`
	// GateEscapeRate — defeitos detectados após passar todos os gates (→ 0).
	GateEscapeRate float64 `json:"gate_escape_rate"`
	// Calibrated marca que a calibração foi CONDUZIDA (revisão do ruído + ajustes).
	Calibrated       bool              `json:"calibrated"`
	ThresholdChanges []ThresholdChange `json:"threshold_changes,omitempty"`
}

// OK indica que a calibração foi conduzida e não PIOROU o ruído: Calibrated, com taxas
// não-negativas e falsos-positivos depois <= antes (a calibração reduz ou mantém o
// ruído, nunca o agrava). Não conta uma calibração vazia como cumprida.
func (a AlertCalibration) OK() bool {
	return a.Calibrated &&
		a.FalsePositiveBefore >= 0 && a.FalsePositiveAfter >= 0 &&
		a.OverrideRate >= 0 && a.GateEscapeRate >= 0 &&
		a.FalsePositiveAfter <= a.FalsePositiveBefore
}

// ---------------------------------------------------------------------------
// AC5 — revalidação de DR (composição de dr.GameDayEvidence).
// ---------------------------------------------------------------------------

// DRRevalidation compõe a evidência do game day de DR REPETIDO no hipercare (AOS-102).
// A evidência é embebida tal-e-qual — não se reavaliam RPO/RTO, lêem-se os veredictos.
type DRRevalidation struct {
	Evidence dr.GameDayEvidence `json:"evidence"`
}

// OK indica DR revalidado: o game day passou (recuperação íntegra) E o RPO e o RTO
// medidos estão dentro do alvo. Compõe os veredictos de [dr.GameDayEvidence] sem os
// recalcular (Passed já exige RPO/RTO within, mas exigimo-los explicitamente — a AC5
// nomeia RPO/RTO, e um Passed futuro que mudasse não nos deve encerrar por acidente).
func (d DRRevalidation) OK() bool {
	e := d.Evidence
	return e.Passed && e.RPOWithin && e.RTOWithin
}

// ---------------------------------------------------------------------------
// AC6 — métricas DORA + acções de acompanhamento.
// ---------------------------------------------------------------------------

// DORAMetrics são as métricas operacionais do relatório de transição (specs/01 §9):
// MTTR, change failure rate e deploy frequency. São REPORTADAS (evidência da transição),
// não são gate de saída — o gate são os critérios AC2..AC5.
type DORAMetrics struct {
	// DeployFrequencyPerWeek — deploys a produção por semana (↑ com estabilidade).
	DeployFrequencyPerWeek float64 `json:"deploy_frequency_per_week"`
	// ChangeFailureRate — fracção [0,1] de deploys que causam falha/rollback (< 0,15).
	ChangeFailureRate float64 `json:"change_failure_rate"`
	// MTTR — tempo médio de recuperação agregado (int64 nanos).
	MTTR time.Duration `json:"mttr"`
	// LeadTime — DoR→Done (opcional; rótulo de fluxo).
	LeadTime time.Duration `json:"lead_time,omitempty"`
}

// FollowUpAction é uma acção de acompanhamento da transição para operação em regime
// (AC6): o que fica em aberto depois de encerrar o hipercare, com dono.
type FollowUpAction struct {
	ID       string `json:"id"`
	Owner    string `json:"owner"`
	Summary  string `json:"summary"`
	DueBy    string `json:"due_by,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// ---------------------------------------------------------------------------
// HipercareReport — o relatório agregado versionado (round-trip JSON).
// ---------------------------------------------------------------------------

// HipercareReport é o relatório de prontidão/transição do hipercare (AC6): agrega a
// conformidade de SLOs (AC2), a validação de runbooks com MTTR (AC3), a calibração de
// alertas (AC4), a revalidação de DR (AC5) e as métricas DORA + acções (AC6). É
// VERSIONADO e JSON-serializável com round-trip reproduzível.
type HipercareReport struct {
	Version          string              `json:"version"`
	Board            string              `json:"board,omitempty"`
	WindowStart      time.Time           `json:"window_start"`
	WindowEnd        time.Time           `json:"window_end"`
	SLOConformance   SLOConformance      `json:"slo_conformance"`
	Runbooks         []RunbookValidation `json:"runbooks"`
	AlertCalibration AlertCalibration    `json:"alert_calibration"`
	DR               DRRevalidation      `json:"dr_revalidation"`
	DORA             DORAMetrics         `json:"dora"`
	FollowUps        []FollowUpAction    `json:"follow_ups,omitempty"`
}

// ErrInvalidReport sinaliza um relatório de hipercare malformado (fail-closed no load).
var ErrInvalidReport = fmt.Errorf("hipercare: relatório de hipercare inválido")

// validate rejeita um relatório sem versão ou com janela invertida (fail-closed). NÃO
// valida os critérios de saída (isso é [HipercareReport.CanExit]) — só a integridade do
// artefacto.
func (r HipercareReport) validate() error {
	if r.Version == "" {
		return fmt.Errorf("%w: version vazia", ErrInvalidReport)
	}
	if !r.WindowStart.IsZero() && !r.WindowEnd.IsZero() && r.WindowEnd.Before(r.WindowStart) {
		return fmt.Errorf("%w: window_end antes de window_start", ErrInvalidReport)
	}
	return nil
}

// Validate expõe a validação de integridade do artefacto (fail-closed).
func (r HipercareReport) Validate() error { return r.validate() }

// JSON serializa o relatório em JSON indentado e determinista (sem escape HTML) — a base
// do round-trip reproduzível e do artefacto versionado no repo.
func (r HipercareReport) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// LoadHipercareReport desserializa e valida a INTEGRIDADE de um relatório de JSON
// (fail-closed no artefacto malformado). É o outro lado do round-trip de
// [HipercareReport.JSON]. NÃO decide o encerramento — isso é [HipercareReport.CanExit].
func LoadHipercareReport(raw []byte) (HipercareReport, error) {
	var r HipercareReport
	if err := json.Unmarshal(raw, &r); err != nil {
		return HipercareReport{}, fmt.Errorf("%w: JSON: %v", ErrInvalidReport, err)
	}
	if err := r.validate(); err != nil {
		return HipercareReport{}, err
	}
	return r, nil
}

// canonicalRunbookIDs devolve os IDs canónicos de AOS-106 (RB-01..RB-05) que a AC3
// exige validados com MTTR. Composição directa de [runbooks.CanonicalIDs] — a fonte
// única da verdade; se AOS-106 acrescentar um runbook canónico, o gate passa a exigi-lo.
func canonicalRunbookIDs() []string { return runbooks.CanonicalIDs() }
