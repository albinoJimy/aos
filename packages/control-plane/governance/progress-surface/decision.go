package progresssurface

import "context"

// ReasonExhaustionPromptTimeout é a razão de degradação aplicada quando o prompt de
// exaustão não recebe resposta (AC5): a superfície NUNCA morre em silêncio — a ausência de
// escolha degrada graciosamente via EPIC-03.
const ReasonExhaustionPromptTimeout = "exhaustion_prompt_timeout"

// Resolution é o resultado de resolver o prompt: a opção escolhida, o estado final e — se
// a opção foi OptionExtend — o resultado da extensão DELEGADA ao controlo.
type Resolution struct {
	// Option é a opção resolvida.
	Option ExhaustionOption
	// Extension é o resultado da extensão delegada (só significativo em OptionExtend).
	Extension ExtensionOutcome
	// State é o estado final do prompt (PromptResolved).
	State PromptState
}

// ResolvePrompt aplica a escolha do utilizador (AC3):
//
//   - OptionExtend        — DELEGA ao BudgetExtender.RequestExtension (a superfície PEDE, o
//     controlo IMPÕE). A superfície NÃO muta o orçamento; devolve o ExtensionOutcome do
//     controlo tal-qual.
//   - OptionSummarizeStop — devolve a decisão ao orquestrador (resumir-e-parar).
//   - OptionAbort         — devolve a decisão ao orquestrador (abortar).
//   - OptionUnset/inválida — ErrUnknownOption (fail-closed: uma escolha desconhecida não é
//     interpretada à força; o caminho legítimo sem resposta é OnPromptTimeout).
//
// Emite o span da decisão (opção + resultado) ligado ao run. Não contabiliza custo nem
// muta orçamento.
func (s *ProgressSurface) ResolvePrompt(ctx context.Context, option ExhaustionOption, req ExtensionRequest) (Resolution, error) {
	res := Resolution{Option: option, State: PromptResolved}
	switch option {
	case OptionExtend:
		if s.extender == nil {
			return Resolution{}, ErrNilBudgetExtender
		}
		out, err := s.extender.RequestExtension(ctx, req)
		if err != nil {
			return Resolution{}, err
		}
		res.Extension = out
		s.emitDecisionSpan(ctx, option, req.RunID, out.Granted, "")
		return res, nil
	case OptionSummarizeStop, OptionAbort:
		// A decisão devolve-se ao orquestrador — a superfície não age no orçamento.
		s.emitDecisionSpan(ctx, option, req.RunID, false, "")
		return res, nil
	default:
		return Resolution{}, ErrUnknownOption
	}
}

// OnPromptTimeout é o caminho SEM RESPOSTA (AC5): aplica a política de degradação de
// EPIC-03 via o Degrader (razão ReasonExhaustionPromptTimeout) — NUNCA um hard-stop cego.
// Emite o span da decisão com a razão da degradação. Fail-closed: sem Degrader configurado
// devolve ErrNilDegrader (a ausência de rede de degradação é um erro de composição, não um
// silêncio).
func (s *ProgressSurface) OnPromptTimeout(ctx context.Context) error {
	if s.degrader == nil {
		return ErrNilDegrader
	}
	s.emitDecisionSpan(ctx, OptionUnset, s.runID, false, ReasonExhaustionPromptTimeout)
	return s.degrader.Degrade(ctx, ReasonExhaustionPromptTimeout)
}
