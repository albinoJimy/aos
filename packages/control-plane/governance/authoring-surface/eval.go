package authoringsurface

import (
	"context"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// EvalView é a projecção do resultado do eval-gate/canary a apresentar ao autor/
// ratificador ANTES da decisão (AC4). LÊ o [otelgenai.EvaluationResult] (veredicto/
// score, EPIC-09) e o CanaryPassed (EPIC-08) — a superfície mostra, nunca decide.
type EvalView struct {
	// Verdict é o veredicto do eval-gate ("pass"|"fail").
	Verdict string
	// Score é a métrica numérica associada (tipicamente 0..1).
	Score float64
	// CanaryPassed reporta se a candidata passou a fase de canary (EPIC-08).
	CanaryPassed bool
	// Detail é a identificação não-secreta da avaliação (suite/eval-id) para contexto.
	Detail string
}

// Admissible reporta se o eval-gate E o canary passaram — a pré-condição que o gate de
// ratificação de AOS-096 exige. É INFORMATIVO (a superfície não decide): apenas ecoa
// a condição para que o autor/ratificador a veja antes de agir. Fail-closed: qualquer
// veredicto que não seja "pass", ou um canary que não passou, é NÃO-admissível.
func (v EvalView) Admissible() bool {
	return v.Verdict == string(otelgenai.EvalPass) && v.CanaryPassed
}

// evalViewFrom projecta o [otelgenai.EvaluationResult] (a fonte da verdade do eval-
// gate) e o veredicto do canary numa [EvalView]. É a fonte ÚNICA do mapeamento
// resultado→vista — a composição sobre o tipo de EPIC-09, não uma cópia.
func evalViewFrom(r otelgenai.EvaluationResult, canaryPassed bool) EvalView {
	detail := r.Suite
	if r.EvalID != "" {
		if detail != "" {
			detail += "/"
		}
		detail += r.EvalID
	}
	return EvalView{
		Verdict:      string(r.Verdict),
		Score:        r.Score,
		CanaryPassed: canaryPassed,
		Detail:       detail,
	}
}

// EvalOutcome LÊ o resultado do eval-gate/canary via [EvalResultReader] e apresenta-o
// (AC4). Fail-closed: sem a porta configurada devolve [ErrNoEvalReader]; sem resultado
// conhecido devolve [ErrNoEvalResult]; uma candidata inválida devolve
// [ErrInvalidCandidate]. A superfície LÊ e mostra o veredicto/score/canary ANTES da
// decisão — nunca decide. Não emite span próprio (é uma leitura de apresentação sem
// efeito); os spans do loop cobrem dry_run/attribution_view/submit.
func (l *AuthoringLoop) EvalOutcome(ctx context.Context, candidate CandidateRef) (EvalView, error) {
	if !candidate.Valid() {
		return EvalView{}, ErrInvalidCandidate
	}
	if l.eval == nil {
		return EvalView{}, ErrNoEvalReader
	}

	res, canaryPassed, err := l.eval.EvalOutcome(ctx, candidate.Skill, candidate.Version)
	if err != nil {
		return EvalView{}, err
	}
	if res == (otelgenai.EvaluationResult{}) && !canaryPassed {
		return EvalView{}, ErrNoEvalResult
	}
	return evalViewFrom(res, canaryPassed), nil
}
