package progresssurface

import (
	"context"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Vocabulário de span da superfície de progresso (DoD). REUSA o AttrRunID partilhado de
// otel-genai (correlação run→trace) e ACRESCENTA a dimensão que só esta superfície conhece
// — a fracção consumida, o limiar, a opção escolhida e a razão de degradação. São RÓTULOS
// de observabilidade, NUNCA segredos (sem prompt, sem args, sem PII).
const (
	// OpExhaustionPrompt — aos.control.exhaustion_prompt: o span do prompt apresentado a
	// ~limiar (a superfície ofereceu as 3 opções).
	OpExhaustionPrompt = "aos.control.exhaustion_prompt"
	// OpExhaustionDecision — aos.control.exhaustion_decision: o span da decisão (a opção
	// resolvida, ou a degradação por timeout).
	OpExhaustionDecision = "aos.control.exhaustion_decision"
	// OpBudgetWarning — aos.control.budget_warning: o AVISO de aproximação ao tecto
	// (AOS-262, primeira entrega). É DISTINTO de [OpExhaustionPrompt] de propósito: um
	// aviso não oferece opções e não espera resposta, e dar-lhe o nome do prompt faria
	// qualquer consumidor do canal de leitura inferir que houve uma escolha a apresentar.
	// Emitido UMA VEZ por run (latch em [ProgressSurface]).
	OpBudgetWarning = "aos.control.budget_warning"

	// AttrBudgetFraction — aos.control.budget_fraction: a fracção consumida [0,1+) no
	// instante do prompt/decisão.
	AttrBudgetFraction = "aos.control.budget_fraction"
	// AttrBudgetThreshold — aos.control.budget_threshold: o limiar configurado que disparou.
	AttrBudgetThreshold = "aos.control.budget_threshold"
	// AttrConsumedMicroUSD — aos.control.consumed_micro_usd: o custo consumido (micro-USD
	// int64), LIDO da agregação de EPIC-08 (não recontabilizado).
	AttrConsumedMicroUSD = "aos.control.consumed_micro_usd"
	// AttrLimitMicroUSD — aos.control.limit_micro_usd: o tecto do orçamento (micro-USD).
	AttrLimitMicroUSD = "aos.control.limit_micro_usd"
	// AttrProgressState — aos.control.progress_state: o rótulo do estado corrente do run.
	AttrProgressState = "aos.control.progress_state"
	// AttrProgressStep — aos.control.progress_step: o rótulo do passo corrente do run.
	AttrProgressStep = "aos.control.progress_step"
	// AttrExhaustionOption — aos.control.exhaustion_option: a opção resolvida
	// (extend/summarize_stop/abort/unset).
	AttrExhaustionOption = "aos.control.exhaustion_option"
	// AttrExtensionGranted — aos.control.extension_granted: se o controlo concedeu a
	// extensão delegada (só em OptionExtend).
	AttrExtensionGranted = "aos.control.extension_granted"
	// AttrDegradeReason — aos.control.degrade_reason: a razão da degradação por ausência de
	// resposta ao prompt.
	AttrDegradeReason = "aos.control.degrade_reason"
	// AttrConsumedTokens — aos.control.consumed_tokens: os tokens consumidos, LIDOS da
	// [BurndownSource] (o ledger de turnos). É a dimensão que HOJE decide a fracção — a de
	// micro-USD está a zero enquanto o canal de custo não estiver ligado (AOS-259).
	AttrConsumedTokens = "aos.control.consumed_tokens"
	// AttrLimitTokens — aos.control.limit_tokens: o tecto em tokens (porta BudgetReader).
	AttrLimitTokens = "aos.control.limit_tokens"
	// AttrWarningTurn — aos.control.warning_turn: o turno em que o limiar foi atingido (a
	// outra metade da correlação (run_id, turn) de AOS-261).
	AttrWarningTurn = "aos.control.warning_turn"
)

// emitWarningSpan abre e fecha o span do AVISO de burn-down (AOS-262). Chamado SÓ quando o
// latch arma (uma vez por run). Sem segredos: só rótulos e números.
func (s *ProgressSurface) emitWarningSpan(ctx context.Context, runID string, turn int, bd Burndown, prog ProgressSnapshot) {
	_, span := s.tracer.StartSpan(ctx, OpBudgetWarning)
	if runID != "" {
		span.SetAttribute(otelgenai.AttrRunID, runID)
	}
	span.SetAttribute(AttrWarningTurn, int64(turn))
	span.SetAttribute(AttrBudgetFraction, bd.Fraction)
	span.SetAttribute(AttrBudgetThreshold, s.threshold)
	span.SetAttribute(AttrConsumedTokens, bd.Consumed.Tokens)
	span.SetAttribute(AttrLimitTokens, bd.Limit.Tokens)
	span.SetAttribute(AttrConsumedMicroUSD, bd.Consumed.CostMicroUSD)
	span.SetAttribute(AttrLimitMicroUSD, bd.Limit.CostMicroUSD)
	if prog.State != "" {
		span.SetAttribute(AttrProgressState, prog.State)
	}
	if prog.Step != "" {
		span.SetAttribute(AttrProgressStep, prog.Step)
	}
	span.End()
}

// emitPromptSpan abre e fecha o span do prompt de exaustão, ligado ao run pelo AttrRunID,
// com a fracção/limiar, o burn-down e o progresso corrente. Sem segredos.
func (s *ProgressSurface) emitPromptSpan(ctx context.Context, bd Burndown, prog ProgressSnapshot) {
	_, span := s.tracer.StartSpan(ctx, OpExhaustionPrompt)
	if s.runID != "" {
		span.SetAttribute(otelgenai.AttrRunID, s.runID)
	}
	span.SetAttribute(AttrBudgetFraction, bd.Fraction)
	span.SetAttribute(AttrBudgetThreshold, s.threshold)
	span.SetAttribute(AttrConsumedMicroUSD, bd.Consumed.CostMicroUSD)
	span.SetAttribute(AttrLimitMicroUSD, bd.Limit.CostMicroUSD)
	if prog.State != "" {
		span.SetAttribute(AttrProgressState, prog.State)
	}
	if prog.Step != "" {
		span.SetAttribute(AttrProgressStep, prog.Step)
	}
	span.End()
}

// emitDecisionSpan abre e fecha o span da decisão do prompt, ligado ao run pelo AttrRunID,
// com a opção resolvida e — conforme o caminho — se a extensão foi concedida ou a razão da
// degradação por timeout. Sem segredos.
func (s *ProgressSurface) emitDecisionSpan(ctx context.Context, option ExhaustionOption, runID string, extensionGranted bool, degradeReason string) {
	_, span := s.tracer.StartSpan(ctx, OpExhaustionDecision)
	if runID != "" {
		span.SetAttribute(otelgenai.AttrRunID, runID)
	}
	span.SetAttribute(AttrExhaustionOption, option.String())
	if option == OptionExtend {
		span.SetAttribute(AttrExtensionGranted, extensionGranted)
	}
	if degradeReason != "" {
		span.SetAttribute(AttrDegradeReason, degradeReason)
	}
	span.End()
}
