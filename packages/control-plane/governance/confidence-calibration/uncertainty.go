package confcalib

import (
	"fmt"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// DefaultUncertaintyThreshold é o limiar por omissão do Score abaixo do qual a
// avaliação se considera pouco segura (AC1). 0.7 é o default configurável do
// blueprint: um Score >= 0.7 num veredicto pass NÃO gera nota (confiança alta =
// silêncio, para evitar o disclaimer universal que gera ruído/fadiga).
const DefaultUncertaintyThreshold = 0.7

// UncertaintyReason classifica PORQUÊ a incerteza é sinalizada — para o span e para a
// superfície apresentarem a razão sem revelar conteúdo. Deriva SÓ do EvaluationResult
// consumido (Verdict/Score), nunca de um recálculo.
type UncertaintyReason string

const (
	// ReasonVerdictFail — a avaliação FALHOU (Verdict != EvalPass). É o sinal mais forte
	// de incerteza: o eval-gate fail-closed não admitiria este candidato.
	ReasonVerdictFail UncertaintyReason = "verdict_fail"
	// ReasonLowScore — a avaliação passou o veredicto mas o Score ficou ABAIXO do limiar:
	// passou por pouco, sinal de ambiguidade digno de nota.
	ReasonLowScore UncertaintyReason = "low_score"
)

// UncertaintyPolicy é o limiar SELECTIVO da linguagem de incerteza (AC1/AC3). É o único
// grau de liberdade da apresentação — NÃO altera nem reavalia o modelo. ShouldSurface
// consome o Score/Verdict do EvaluationResult directamente.
type UncertaintyPolicy struct {
	// Threshold é o Score mínimo para NÃO sinalizar incerteza (num veredicto pass).
	// Score < Threshold ⇒ sinaliza. Fora de (0,1] cai no DefaultUncertaintyThreshold.
	Threshold float64
}

// NewUncertaintyPolicy constrói a política com o limiar dado; um limiar inválido
// (<=0 ou >1) cai no DefaultUncertaintyThreshold (fail-safe: nunca um limiar que
// sinalize sempre ou nunca de forma degenerada).
func NewUncertaintyPolicy(threshold float64) UncertaintyPolicy {
	if threshold <= 0 || threshold > 1 {
		threshold = DefaultUncertaintyThreshold
	}
	return UncertaintyPolicy{Threshold: threshold}
}

// effectiveThreshold devolve o limiar em uso, tolerando um UncertaintyPolicy{} zero
// (limiar 0) construído directamente: cai no default.
func (p UncertaintyPolicy) effectiveThreshold() float64 {
	if p.Threshold <= 0 || p.Threshold > 1 {
		return DefaultUncertaintyThreshold
	}
	return p.Threshold
}

// ShouldSurface é a decisão SELECTIVA (AC1): devolve true SÓ quando há sinal de baixa
// confiança/ambiguidade — !result.Passed() OU result.Score < Threshold. Confiança alta
// (Passed E Score >= Threshold) ⇒ false (SEM nota, evitando o disclaimer universal).
//
// CONSOME o Verdict/Score do EvaluationResult dado — NÃO recalcula confiança, NÃO
// reavalia a trajectória, NÃO importa o replay/eval-engine. É função pura do resultado.
func (p UncertaintyPolicy) ShouldSurface(result otelgenai.EvaluationResult) bool {
	if !result.Passed() {
		return true
	}
	return result.Score < p.effectiveThreshold()
}

// UncertaintyNote é a linguagem de incerteza gerada — SÓ quando [ShouldSurface] é true.
// É um artefacto de APRESENTAÇÃO passivo: informa a decisão humana, não a substitui.
// Carrega SÓ o sinal consumido (Score/limiar/razão) e uma mensagem legível — nunca
// conteúdo do modelo nem PII.
type UncertaintyNote struct {
	// Reason é a razão da incerteza (verdict_fail | low_score).
	Reason UncertaintyReason
	// Score é o Score CONSUMIDO do EvaluationResult (não recalculado).
	Score float64
	// Threshold é o limiar efectivo que a política aplicou.
	Threshold float64
	// Message é a linguagem de incerteza legível a apresentar junto da acção.
	Message string
}

// NoteFor produz a [UncertaintyNote] SÓ quando a política decide sinalizar; devolve nil
// quando a confiança é alta (o nil É a selectividade — a ausência de nota). CONSOME o
// Score/Verdict do result; não recalcula nada.
func (p UncertaintyPolicy) NoteFor(result otelgenai.EvaluationResult) *UncertaintyNote {
	if !p.ShouldSurface(result) {
		return nil
	}
	th := p.effectiveThreshold()
	note := &UncertaintyNote{Score: result.Score, Threshold: th}
	if !result.Passed() {
		note.Reason = ReasonVerdictFail
		note.Message = fmt.Sprintf(
			"Confiança reduzida: a avaliação FALHOU (veredicto=%s, score=%.2f). Reveja antes de aprovar.",
			result.Verdict, result.Score)
	} else {
		note.Reason = ReasonLowScore
		note.Message = fmt.Sprintf(
			"Confiança reduzida: score %.2f abaixo do limiar %.2f (passou por pouco). Considere rever.",
			result.Score, th)
	}
	return note
}
