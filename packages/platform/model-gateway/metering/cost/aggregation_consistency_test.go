package cost_test

// Teste de CONSISTÊNCIA de AOS-078: prova que a agregação de custo/tokens da camada
// otel-genai (substrate/otel-genai) sobre os spans `chat` emitidos por um run BATE
// EXACTAMENTE com os totais que o Model Gateway acumula pelo [cost.Recorder]
// (RunKey/TreeKey) a partir do mesmo [cost.Calculator] e tabela de preços versionada.
// Satisfaz o DoD "Consistência da agregação validada contra os totais do Model
// Gateway".

import (
	"context"
	"testing"

	otelgenai "github.com/aos-ref/substrate/otel-genai"

	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/pricing"
)

// TestAggregationConsistentWithGatewayTotals corre N turnos: por cada turno calcula o
// custo pela tabela versionada (via Calculator), (a) alimenta o Recorder do GW e (b)
// emite um span `chat` com os tokens reais e o custo em micro-USD EXACTO. No fim, a
// agregação da otel-genai por trajectória tem de igualar CostForRun/CostForTree do GW —
// sem tolerância (ambos somam o mesmo inteiro micro-USD).
func TestAggregationConsistentWithGatewayTotals(t *testing.T) {
	const (
		model  = "claude-opus-4-8"
		region = "eu-west-1"
		tenant = "board:acme"
		runID  = "run_consistency_1"
		treeID = "tree_consistency_1"
	)

	table, err := pricing.NewTable("2026-07-test", []pricing.Entry{{
		Model:  model,
		Region: region,
		Rate: pricing.Rate{
			InputPerMTokMicroUSD:      3_000_000,
			OutputPerMTokMicroUSD:     15_000_000,
			CacheReadPerMTokMicroUSD:  300_000,
			CacheWritePerMTokMicroUSD: 3_750_000,
		},
	}})
	if err != nil {
		t.Fatalf("pricing.NewTable: %v", err)
	}
	calc := cost.NewCalculator(table)
	rec := cost.NewRecorder(calc)

	// Turnos SEM tokens de cache: assim o eixo de VOLUME do GW (prompt+completion+
	// cache_write) coincide com input+output dos spans chat, e a comparação de tokens é
	// directa (a consistência de custo vale para qualquer mistura; os tokens do span
	// chat são só input/output por semconv).
	turns := []cost.TokenCounts{
		{PromptTokens: 1200, CompletionTokens: 300},
		{PromptTokens: 800, CompletionTokens: 150},
		{PromptTokens: 2048, CompletionTokens: 512},
	}

	tr := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	ctx := context.Background()

	// invoke_agent envolve o run (carrega o agregado — que a agregação NÃO soma).
	actx, agent := tr.StartSpan(ctx, otelgenai.OpInvokeAgent)
	agent.SetAttribute(otelgenai.AttrOperationName, otelgenai.OpInvokeAgent)

	var wantCost, wantTokens int64
	for _, tc := range turns {
		amt, errCost := calc.Cost(tc, model, region)
		if errCost != nil {
			t.Fatalf("calc.Cost: %v", errCost)
		}
		wantCost += amt.CostMicroUSD
		wantTokens += tc.PromptTokens + tc.CompletionTokens

		// (a) totais do Model Gateway (RunKey/TreeKey).
		reading := rec.Observe(ctx, nil, cost.Sample{
			RunID:  runID,
			TreeID: treeID,
			Tenant: tenant,
			Region: region,
			Model:  model,
			Tokens: tc,
		})
		if reading.Err != nil {
			t.Fatalf("recorder.Observe: %v", reading.Err)
		}

		// (b) span chat com os tokens reais e o custo micro-USD EXACTO (a mesma
		// derivação que o loop.go do RT faz em callModel).
		_, chat := tr.StartSpan(actx, otelgenai.OpChat)
		chat.SetAttribute(otelgenai.AttrOperationName, otelgenai.OpChat)
		chat.SetAttribute(otelgenai.AttrRequestModel, model)
		chat.SetAttribute(otelgenai.AttrInputTokens, tc.PromptTokens)
		chat.SetAttribute(otelgenai.AttrOutputTokens, tc.CompletionTokens)
		chat.SetAttribute(otelgenai.AttrCostMicroUSD, amt.CostMicroUSD)
		chat.End()
	}
	// Agregado no invoke_agent (o que NÃO deve ser somado pela agregação).
	agent.SetAttribute(otelgenai.AttrCostMicroUSD, wantCost)
	agent.End()

	// --- Agregação da camada otel-genai sobre os spans chat emitidos ---
	agg := otelgenai.AggregateRecordedByTrace(tr.Spans())
	if len(agg) != 1 {
		t.Fatalf("esperava 1 trajectória, obtive %d", len(agg))
	}
	var otel otelgenai.UsageTotals
	for _, v := range agg {
		otel = v
	}

	// --- Totais do Model Gateway ---
	gwRun, ok := rec.CostForRun(cost.RunKey{RunID: runID, Tenant: tenant})
	if !ok {
		t.Fatal("CostForRun ausente")
	}
	gwTree, ok := rec.CostForTree(cost.TreeKey{TreeID: treeID, Tenant: tenant})
	if !ok {
		t.Fatal("CostForTree ausente")
	}

	// Consistência EXACTA de custo (sem tolerância).
	if otel.CostMicroUSD != wantCost {
		t.Fatalf("custo agregado otel-genai = %d, esperava %d", otel.CostMicroUSD, wantCost)
	}
	if otel.CostMicroUSD != gwRun.CostMicroUSD {
		t.Fatalf("custo otel-genai (%d) != total GW por run (%d)", otel.CostMicroUSD, gwRun.CostMicroUSD)
	}
	if otel.CostMicroUSD != gwTree.CostMicroUSD {
		t.Fatalf("custo otel-genai (%d) != total GW por árvore (%d)", otel.CostMicroUSD, gwTree.CostMicroUSD)
	}
	// Consistência de tokens (input+output vs volume facturável sem cache).
	if otel.TotalTokens() != wantTokens {
		t.Fatalf("tokens agregados otel-genai = %d, esperava %d", otel.TotalTokens(), wantTokens)
	}
	if otel.TotalTokens() != gwRun.Tokens {
		t.Fatalf("tokens otel-genai (%d) != total GW por run (%d)", otel.TotalTokens(), gwRun.Tokens)
	}

	// NÃO-DUPLA-CONTAGEM: a agregação (só-chats) NÃO inclui o agregado do invoke_agent.
	// Somar tudo (invoke_agent + chats) daria o dobro do custo — confirmamos que não.
	if otel.CostMicroUSD == 2*wantCost {
		t.Fatalf("dupla-contagem detectada: %d == 2*%d", otel.CostMicroUSD, wantCost)
	}
}
