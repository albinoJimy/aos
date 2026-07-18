package otelgenai

import "testing"

// TestSemconvCanonicalNames fixa os nomes canónicos: são wire format estável, uma
// regressão aqui partiria qualquer backend OTel a jusante.
func TestSemconvCanonicalNames(t *testing.T) {
	cases := map[string]string{
		AttrOperationName:     "gen_ai.operation.name",
		AttrRequestModel:      "gen_ai.request.model",
		AttrInputTokens:       "gen_ai.usage.input_tokens",
		AttrOutputTokens:      "gen_ai.usage.output_tokens",
		AttrCostUSD:           "gen_ai.usage.cost_usd",
		AttrToolName:          "gen_ai.tool.name",
		AttrPrincipalNHI:      "aos.principal.nhi_id",
		AttrRunID:             "aos.run_id",
		AttrStepID:            "aos.step_id",
		AttrPromptHash:        "aos.prompt_hash",
		AttrPrefixHash:        "aos.prefix_hash",
		AttrToolCallHash:      "aos.tool_call.hash",
		AttrErrorType:         "error.type",
		AttrTaint:             "aos.taint",
		AttrResultTaint:       "aos.tool.result_taint",
		AttrDeniedBy:          "aos.decision.denied_by",
		AttrDecision:          "aos.decision",
		OpInvokeAgent:         "invoke_agent",
		OpChat:                "chat",
		OpExecuteTool:         "execute_tool",
		OpEvaluation:          "gen_ai.evaluation.result",
		AttrEvalVerdict:       "aos.eval.verdict",
		AttrEvalScore:         "aos.eval.score",
		AttrEvalDataset:       "aos.eval.dataset",
		AttrEvalSuite:         "aos.eval.suite",
		AttrEvalID:            "aos.eval.id",
		AttrEvalTargetTraceID: "aos.eval.target_trace_id",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("constante = %q, esperava %q", got, want)
		}
	}
}

func TestMicroUSDToUSD(t *testing.T) {
	if got := MicroUSDToUSD(1_500_000); got != 1.5 {
		t.Errorf("MicroUSDToUSD(1_500_000) = %v, esperava 1.5", got)
	}
	if got := MicroUSDToUSD(0); got != 0 {
		t.Errorf("MicroUSDToUSD(0) = %v, esperava 0", got)
	}
}
