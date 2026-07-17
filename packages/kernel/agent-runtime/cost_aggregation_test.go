package agentruntime

// Integração de AOS-078 no Agent Runtime: a agregação de custo/tokens da camada
// otel-genai sobre os spans REAIS emitidos pelo loop (Run) bate com os totais do
// próprio run (res.TotalUsage/res.TotalCostMicroUSD), contando SÓ os spans `chat` — o
// invoke_agent (agregado) e o execute_tool NÃO entram (sem dupla-contagem).

import (
	"context"
	"testing"

	otelgenai "github.com/aos-ref/substrate/otel-genai"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// TestCostAggregationOverRealRunSpans corre o loop com uma tool call (2 turnos) e prova
// que a agregação por trajectória sobre os spans emitidos == totais do run, e que somar
// TODOS os spans (incluindo o agregado do invoke_agent) daria o dobro.
func TestCostAggregationOverRealRunSpans(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, input []byte) ([]byte, error) {
			return append([]byte("echoed:"), input...), nil
		},
	})

	callN := 0
	model := ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
		callN++
		if callN == 1 {
			return ModelResponse{
				Text:         "vou chamar a tool echo",
				ToolCalls:    []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("ola")}},
				Usage:        Usage{InputTokens: 10, OutputTokens: 5},
				CostMicroUSD: 1200,
			}, nil
		}
		return ModelResponse{
			Text:         "concluído",
			Final:        true,
			Usage:        Usage{InputTokens: 8, OutputTokens: 3},
			CostMicroUSD: 900,
		}, nil
	})

	rt := New(model, h.rm, h.recorder, WithTracer(h.tracer))
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	spans := h.tracer.Spans()

	// A árvore tem invoke_agent + 2 chat + 1 execute_tool: a agregação conta SÓ os chats.
	agg := otelgenai.AggregateRecordedByTrace(spans)
	if len(agg) != 1 {
		t.Fatalf("esperava 1 trajectória, obtive %d", len(agg))
	}
	var got otelgenai.UsageTotals
	for _, v := range agg {
		got = v
	}

	// Consistência com os totais do próprio run.
	if got.CostMicroUSD != res.TotalCostMicroUSD {
		t.Fatalf("custo agregado (%d) != res.TotalCostMicroUSD (%d)", got.CostMicroUSD, res.TotalCostMicroUSD)
	}
	if got.InputTokens != res.TotalUsage.InputTokens || got.OutputTokens != res.TotalUsage.OutputTokens {
		t.Fatalf("tokens agregados %+v != res.TotalUsage %+v", got, res.TotalUsage)
	}
	if got.CostMicroUSD != 2100 || got.InputTokens != 18 || got.OutputTokens != 8 {
		t.Fatalf("agregação inesperada: %+v", got)
	}

	// Rollup: o invoke_agent (raiz) tem OWN == SUBTREE == total (todos os chats são
	// filhos directos dele, sem sub-agentes neste run).
	roll := otelgenai.RollupRecordedByTrace(spans)
	var tr otelgenai.TraceRollup
	for _, r := range roll {
		tr = r
	}
	if tr.Chats != 2 {
		t.Fatalf("chats no rollup = %d, esperava 2", tr.Chats)
	}
	// Localiza o span_id do invoke_agent.
	var agentHex string
	for _, s := range spans {
		if s.Operation == OpInvokeAgent {
			agentHex = s.SpanContext.SpanIDHex()
		}
	}
	if agentHex == "" {
		t.Fatal("invoke_agent não encontrado")
	}
	if tr.OwnByAgent[agentHex] != got || tr.SubtreeByAgent[agentHex] != got {
		t.Fatalf("OWN/SUBTREE do invoke_agent != total: own=%+v subtree=%+v total=%+v",
			tr.OwnByAgent[agentHex], tr.SubtreeByAgent[agentHex], got)
	}

	// NÃO-DUPLA-CONTAGEM sobre spans reais: somar o custo de TODOS os spans (o agregado
	// do invoke_agent JÁ vale 2100 + os dois chats 2100) daria 4200; a agregação fica em
	// 2100.
	var naive int64
	for _, s := range spans {
		if v, ok := s.Attributes[AttrCostMicroUSD]; ok {
			if n, ok := v.(int64); ok {
				naive += n
			}
		}
	}
	if naive != 4200 {
		t.Fatalf("soma naive de todos os spans = %d, esperava 4200 (agregado+chats)", naive)
	}
	if got.CostMicroUSD == naive {
		t.Fatalf("dupla-contagem: agregação (%d) == soma naive (%d)", got.CostMicroUSD, naive)
	}
}
