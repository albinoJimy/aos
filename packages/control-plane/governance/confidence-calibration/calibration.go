package confcalib

import (
	"context"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Calibration é o artefacto de CALIBRAÇÃO que se ANEXA à superfície de aprovação
// (AOS-120/122) para INFORMAR a decisão humana (AC4) — NÃO a substitui nem decide. É
// passivo: compõe a linguagem de incerteza SELECTIVA (nil quando a confiança é alta) e o
// histórico de correcções do contexto.
type Calibration struct {
	// Context é a chave de contexto (agente/capability/domínio) da acção calibrada.
	Context string
	// Uncertainty é a linguagem de incerteza a apresentar, ou nil quando a confiança é
	// alta (AC1: selectivo — o nil é a ausência de disclaimer).
	Uncertainty *UncertaintyNote
	// Corrections é o histórico de correcções relevante para o contexto (AC2), redigido.
	Corrections CorrectionSummary
}

// UncertaintyShown reporta se a calibração apresenta linguagem de incerteza (para o span
// e para a superfície). É exactamente a selectividade de AC1.
func (c Calibration) UncertaintyShown() bool { return c.Uncertainty != nil }

// Calibrator compõe a política de incerteza + o histórico de correcções + o Tracer numa
// superfície de calibração. CONSOME os sinais de EPIC-08/AOS-119 — não inventa métricas,
// não recalcula confiança, não substitui a decisão humana.
type Calibrator struct {
	policy  UncertaintyPolicy
	history *CorrectionHistory
	tracer  otelgenai.Tracer
	runID   string
}

// Option configura o [Calibrator] na construção.
type Option func(*Calibrator)

// WithRunID fixa o run_id para correlacionar o span de calibração ao run (AttrRunID).
func WithRunID(runID string) Option {
	return func(c *Calibrator) { c.runID = runID }
}

// New constrói o Calibrator com a política de incerteza e o histórico de correcções. Um
// tracer nil cai no NoopTracer (sem observabilidade, como o default do Runtime). Um
// history nil é tolerado: a calibração produz um sumário vazio (a incerteza mantém-se).
func New(policy UncertaintyPolicy, history *CorrectionHistory, tracer otelgenai.Tracer, opts ...Option) *Calibrator {
	if tracer == nil {
		tracer = otelgenai.NoopTracer{}
	}
	c := &Calibrator{policy: policy, history: history, tracer: tracer}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Calibrate compõe a calibração para um EvaluationResult e um contexto (AC1/AC2/AC3):
//
//   - A linguagem de incerteza é SELECTIVA — presente SÓ se result.Score < limiar OU
//     !result.Passed(); nil (ausente) quando a confiança é alta (AC1).
//   - O histórico de correcções é o do contexto semelhante, redigido (AC2/AC5).
//   - Os sinais são CONSUMIDOS: Calibrate é função PURA do EvaluationResult DADO — usa
//     result.Score/Verdict directamente, sem re-avaliar, sem importar o replay/eval-engine
//     (AC3). Não há recálculo local de confiança.
//
// Emite o span de interacção da calibração (AC5), sem segredos/PII. O erro só surge se a
// fonte de correcções falhar; a incerteza nunca falha (é pura sobre o result).
func (c *Calibrator) Calibrate(ctx context.Context, result otelgenai.EvaluationResult, contextKey string) (Calibration, error) {
	cal := Calibration{
		Context:     contextKey,
		Uncertainty: c.policy.NoteFor(result), // nil ⇒ confiança alta (selectivo)
	}
	if c.history != nil {
		sum, err := c.history.SummaryFor(ctx, contextKey)
		if err != nil {
			return Calibration{}, err
		}
		cal.Corrections = sum
	} else {
		cal.Corrections = CorrectionSummary{Context: contextKey, ByKind: map[string]int{}}
	}
	c.emitSpan(ctx, cal)
	return cal, nil
}
