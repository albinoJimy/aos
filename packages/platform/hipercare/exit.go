package hipercare

import (
	"fmt"
	"sort"
)

// ExitCriteria é o veredicto FAIL-CLOSED dos critérios de saída do hipercare (AC1). Cada
// booleano é um critério; Missing enumera, de forma legível e determinista, o que falta
// quando algum não está cumprido. [ExitCriteria.CanExit] só é true se TODOS os critérios
// estão cumpridos E Missing está vazio.
type ExitCriteria struct {
	// SLOsSustained — todos os SLOs de saída (AC2) avaliados+cumpridos e sem breach.
	SLOsSustained bool `json:"slos_sustained"`
	// RunbooksValidated — todos os canónicos RB-01..RB-05 validados com MTTR (AC3).
	RunbooksValidated bool `json:"runbooks_validated"`
	// AlertsCalibrated — alertas calibrados sem agravar o ruído (AC4).
	AlertsCalibrated bool `json:"alerts_calibrated"`
	// DRRevalidated — game day de DR passou com RPO/RTO dentro do alvo (AC5).
	DRRevalidated bool `json:"dr_revalidated"`
	// Missing é a lista determinista do que falta (vazia sse tudo cumprido).
	Missing []string `json:"missing,omitempty"`
}

// CanExit é o GATE: true sse TODOS os critérios estão cumpridos e nada falta. É
// fail-closed por construção — qualquer critério a false, ou qualquer entrada em Missing,
// impede o encerramento. Nunca encerra sobre ausência de dados (anti-vacuidade: um SLO
// não avaliado deixa SLOsSustained==false; um runbook sem MTTR deixa
// RunbooksValidated==false).
func (c ExitCriteria) CanExit() bool {
	return c.SLOsSustained &&
		c.RunbooksValidated &&
		c.AlertsCalibrated &&
		c.DRRevalidated &&
		len(c.Missing) == 0
}

// ExitCriteria avalia os critérios de saída a partir do relatório, COMPONDO os
// contratos das peças (nunca reavaliando o seu comportamento):
//
//   - AC2: cada SLO de saída ([CanonicalExitSLIs]) tem de estar presente no snapshot,
//     AVALIADO (Samples>0 — anti-vacuidade), cumprido (Met) e sem breach; e nenhum
//     painel do snapshot pode estar em breach;
//   - AC3: cada runbook canónico de [runbooks.CanonicalIDs] tem de ter um registo
//     validado com MTTR medido (> 0);
//   - AC4: a calibração de alertas tem de estar conduzida sem agravar o ruído;
//   - AC5: o game day de DR tem de ter passado com RPO/RTO dentro do alvo.
//
// Cada falha acrescenta uma linha legível a Missing. Determinista (ordem estável).
func (r HipercareReport) ExitCriteria() ExitCriteria {
	var missing []string

	// --- AC2: SLOs de saída sustentados sobre a janela. -----------------------
	slosOK := true
	conf := r.SLOConformance
	for _, sli := range canonicalExitSLIs {
		e, ok := conf.entryFor(sli)
		switch {
		case !ok:
			slosOK = false
			missing = append(missing, fmt.Sprintf("SLO %q: ausente do snapshot da janela", sli))
		case !e.Evaluated:
			// Anti-vacuidade: sem amostras não se afirma cumprimento — não se encerra.
			slosOK = false
			missing = append(missing, fmt.Sprintf("SLO %q: não avaliado (Samples==0) — não se encerra sobre ausência de dados", sli))
		case e.Breached:
			slosOK = false
			missing = append(missing, fmt.Sprintf("SLO %q: em breach (valor %.4g vs SLO %.4g %s)", sli, e.Value, e.SLO, e.Direction))
		case !e.Met:
			slosOK = false
			missing = append(missing, fmt.Sprintf("SLO %q: não cumprido (valor %.4g vs SLO %.4g %s)", sli, e.Value, e.SLO, e.Direction))
		}
	}
	// Um breach em QUALQUER painel avaliado (mesmo não sendo gate por si) impede o fecho.
	if conf.HasBreach() {
		for _, e := range conf.Entries {
			if e.Breached && !isExitSLI(e.SLI) {
				slosOK = false
				missing = append(missing, fmt.Sprintf("SLI %q em breach durante a janela (valor %.4g vs SLO %.4g %s)", e.SLI, e.Value, e.SLO, e.Direction))
			}
		}
	}

	// --- AC3: MTTR por runbook canónico. --------------------------------------
	runbooksOK := true
	byID := make(map[string]RunbookValidation, len(r.Runbooks))
	for _, rb := range r.Runbooks {
		byID[rb.RunbookID] = rb
	}
	for _, id := range canonicalRunbookIDs() {
		rb, ok := byID[id]
		switch {
		case !ok:
			runbooksOK = false
			missing = append(missing, fmt.Sprintf("runbook %q: sem registo de validação no hipercare", id))
		case !rb.Validated:
			runbooksOK = false
			missing = append(missing, fmt.Sprintf("runbook %q: não validado em incidente real/simulado", id))
		case rb.MTTR <= 0:
			runbooksOK = false
			missing = append(missing, fmt.Sprintf("runbook %q: sem MTTR medido (MTTR<=0)", id))
		}
	}

	// --- AC4: alertas calibrados. ---------------------------------------------
	alertsOK := r.AlertCalibration.OK()
	if !alertsOK {
		missing = append(missing, "alertas: não calibrados (calibração não conduzida ou ruído agravado)")
	}

	// --- AC5: DR revalidado (RPO/RTO). ----------------------------------------
	drOK := r.DR.OK()
	if !drOK {
		ev := r.DR.Evidence
		missing = append(missing, fmt.Sprintf("DR: game day não revalidado (passed=%t rpo_within=%t rto_within=%t)",
			ev.Passed, ev.RPOWithin, ev.RTOWithin))
	}

	sort.Strings(missing)
	return ExitCriteria{
		SLOsSustained:     slosOK,
		RunbooksValidated: runbooksOK,
		AlertsCalibrated:  alertsOK,
		DRRevalidated:     drOK,
		Missing:           missing,
	}
}

// CanExit é o atalho fail-closed: o hipercare só encerra (transita para regime) se todos
// os critérios de saída estão cumpridos. Equivale a r.ExitCriteria().CanExit().
func (r HipercareReport) CanExit() bool { return r.ExitCriteria().CanExit() }

// isExitSLI indica se um SLI é um dos SLOs de saída nomeados (para não duplicar a linha
// de breach de um SLO de saída, já reportada pelo laço da AC2).
func isExitSLI(sli string) bool {
	for _, s := range canonicalExitSLIs {
		if s == sli {
			return true
		}
	}
	return false
}
