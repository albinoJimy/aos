package confcalib

import (
	"context"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Vocabulário de span da superfície de calibração (AC5). REUSA o AttrRunID partilhado de
// otel-genai (correlação run→trace) e ACRESCENTA a dimensão que só esta superfície
// conhece — se a incerteza foi mostrada, quantas correcções o histórico expôs, a razão
// da incerteza e a chave de contexto. São RÓTULOS de observabilidade, NUNCA segredos: o
// TEXTO da correcção JAMAIS entra num atributo (só a contagem/tipo agregados).
const (
	// OpCalibrationInteraction — aos.calibration.interaction: o span de interacção da
	// calibração, ligado ao trace do run.
	OpCalibrationInteraction = "aos.calibration.interaction"

	// AttrUncertaintyShown — aos.calibration.uncertainty_shown: se a linguagem de
	// incerteza foi apresentada (bool). É a selectividade de AC1 tornada observável.
	AttrUncertaintyShown = "aos.calibration.uncertainty_shown"
	// AttrUncertaintyReason — aos.calibration.uncertainty_reason: a razão da incerteza
	// (verdict_fail | low_score), presente só quando uncertainty_shown é true.
	AttrUncertaintyReason = "aos.calibration.uncertainty_reason"
	// AttrCorrectionCount — aos.calibration.correction_count: o nº de correcções em
	// contexto semelhante que o histórico expôs. É uma CONTAGEM agregada — nunca o texto.
	AttrCorrectionCount = "aos.calibration.correction_count"
	// AttrCalibrationContext — aos.calibration.context: a chave de contexto (agente/
	// capability/domínio). Identificador estrutural de agrupamento, nunca conteúdo/PII.
	AttrCalibrationContext = "aos.calibration.context"
)

// emitSpan abre e fecha o span de interacção da calibração, ligado ao run pelo AttrRunID,
// com a selectividade da incerteza, a razão (se mostrada), a contagem de correcções e o
// contexto. SEM segredos: o texto da correcção NUNCA entra num atributo.
func (c *Calibrator) emitSpan(ctx context.Context, cal Calibration) {
	_, span := c.tracer.StartSpan(ctx, OpCalibrationInteraction)
	span.SetAttribute(otelgenai.AttrOperationName, OpCalibrationInteraction)
	if c.runID != "" {
		span.SetAttribute(otelgenai.AttrRunID, c.runID)
	}
	span.SetAttribute(AttrUncertaintyShown, cal.UncertaintyShown())
	if cal.Uncertainty != nil {
		span.SetAttribute(AttrUncertaintyReason, string(cal.Uncertainty.Reason))
	}
	span.SetAttribute(AttrCorrectionCount, cal.Corrections.Count)
	if cal.Context != "" {
		span.SetAttribute(AttrCalibrationContext, cal.Context)
	}
	span.End()
}
