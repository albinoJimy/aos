package modelgateway_test

import (
	"context"
	"errors"
	"io"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/metering/attribution"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/pricing"
	"github.com/aos-ref/platform/model-gateway/routing/keypool"
)

// costTable é a tabela determinista do teste (região "eu", a default do newGateway).
func costTable(t *testing.T) *pricing.Table {
	t.Helper()
	tbl, err := pricing.NewTable("test", []pricing.Entry{
		{Model: "m", Region: "eu", Rate: pricing.Rate{
			InputPerMTokMicroUSD:      3_000_000,
			OutputPerMTokMicroUSD:     15_000_000,
			CacheReadPerMTokMicroUSD:  300_000,
			CacheWritePerMTokMicroUSD: 3_750_000,
		}},
	})
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	return tbl
}

// TestGateway_Cost_PerCall_Span_Aggregate_Burndown é o teste de integração de
// AOS-062 pela fachada: o GW deriva o custo por chamada (4 tipos de token × tabela
// versionada), emite-o no span OTel GenAI ligado a modelo/região/trajectória, agrega
// por run/árvore e alimenta o burn-down.
func TestGateway_Cost_PerCall_Span_Aggregate_Burndown(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500, CacheReadTokens: 200, CacheWriteTokens: 100},
	})

	metrics := &cost.MemoryMetricSink{}
	burn := cost.NewMemoryBurndownSink()
	rec := cost.NewRecorder(cost.NewCalculator(costTable(t)),
		cost.WithMetricSink(metrics), cost.WithBurndownSink(burn))
	tr := &agentruntime.RecordingTracer{}

	gw := newGateway(t, f, modelgateway.WithCost(rec), modelgateway.WithTracer(tr))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := gw.Chat(ctx, port.ChatRequest{
			Model: "m", Board: "board-eu", RunID: "run-1", TreeID: "tree-1",
			Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
		}); err != nil {
			t.Fatalf("chat[%d]: %v", i, err)
		}
	}

	// custo por chamada = billable(800)*3 + out(500)*15 + cr(200)*0.3 + cw(100)*3.75
	//                    = 2400 + 7500 + 60 + 375 = 10335 micro-USD
	const perCall = int64(10335)

	// Span leva o custo (USD + micro-USD exacto) + modelo/região (ligação à trajectória).
	sp := tr.SpansByOperation(agentruntime.OpChat)[0]
	if sp.Attributes[cost.AttrCostMicroUSD] != perCall {
		t.Fatalf("span custo micro-USD = %v, quer %d", sp.Attributes[cost.AttrCostMicroUSD], perCall)
	}
	if _, ok := sp.Attributes[cost.AttrCostUSD].(float64); !ok {
		t.Fatalf("span sem gen_ai.usage.cost_usd")
	}
	if sp.Attributes[agentruntime.AttrRequestModel] != "m" || sp.Attributes[cost.AttrRegion] != "eu" {
		t.Fatalf("custo nao ligado a modelo/regiao no span")
	}

	// Agregação por run E por árvore disponível para o burn-down.
	runAgg, ok := rec.CostForRun(cost.RunKey{RunID: "run-1", Tenant: "board-eu"})
	if !ok || runAgg.CostMicroUSD != 2*perCall {
		t.Fatalf("agregado por run = %+v (quer %d)", runAgg, 2*perCall)
	}
	treeAgg, ok := rec.CostForTree(cost.TreeKey{TreeID: "tree-1", Tenant: "board-eu"})
	if !ok || treeAgg.CostMicroUSD != 2*perCall {
		t.Fatalf("agregado por arvore = %+v (quer %d)", treeAgg, 2*perCall)
	}
	// Burn-down (porta) recebeu os incrementos.
	if got, _ := burn.Tree("tree-1", "board-eu"); got.CostMicroUSD != 2*perCall {
		t.Fatalf("burn-down arvore = %d (quer %d)", got.CostMicroUSD, 2*perCall)
	}
}

// TestGateway_Cost_And_Attribution_SameSpan prova END-TO-END a ligação custo->principal
// exigida pelo Critério 2/DoD de AOS-062: numa única chamada, o MESMO span OTel GenAI
// leva EM PARALELO o principal (AttrPrincipalUser/AttrPrincipalAgent de AOS-057) E o
// custo (AttrCostMicroUSD/AttrCostUSD de AOS-062), ambos ligados ao mesmo modelo/região.
// Compõe WithAttribution + WithCost no MESMO GW (via o harness de AOS-057), fechando a
// lacuna de os dois estágios só coexistirem composicionalmente sem um teste conjunto.
func TestGateway_Cost_And_Attribution_SameSpan(t *testing.T) {
	t.Parallel()
	// Tabela de preços para (gpt-x, eu) — o modelo/região que o harness de AOS-057 usa.
	tbl, err := pricing.NewTable("aos062-attr", []pricing.Entry{
		{Model: "gpt-x", Region: "eu", Rate: pricing.Rate{
			InputPerMTokMicroUSD:      3_000_000,
			OutputPerMTokMicroUSD:     15_000_000,
			CacheReadPerMTokMicroUSD:  300_000,
			CacheWritePerMTokMicroUSD: 3_750_000,
		}},
	})
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	rec := cost.NewRecorder(cost.NewCalculator(tbl))

	pool := keypool.NewPool(keypool.Account{KeyID: "acct-eu-1", LimitRPM: 100})
	h := newAOS057Gateway(t, pool, modelgateway.WithCost(rec))
	// Usage com os 4 tipos de token para o modelo gpt-x.
	h.adpt.SetChatResponse("gpt-x", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500, CacheReadTokens: 200, CacheWriteTokens: 100},
	})

	h.chat(t, h.token(t, "alice", "agent-1"))

	// UM único span de chat leva EM PARALELO a atribuição (AOS-057) E o custo (AOS-062).
	sp := h.trace.SpansByOperation(agentruntime.OpChat)[0]

	// AOS-057: principal (utilizador + agente) no MESMO span.
	if sp.Attributes[attribution.AttrPrincipalUser] != "alice" || sp.Attributes[attribution.AttrPrincipalAgent] != "agent-1" {
		t.Fatalf("span sem principal (AOS-057): %+v", sp.Attributes)
	}
	// AOS-062: custo exacto (micro-USD) + USD no MESMO span.
	const perCall = int64(10335) // 800*3 + 500*15 + 200*0.3 + 100*3.75
	if sp.Attributes[cost.AttrCostMicroUSD] != perCall {
		t.Fatalf("span custo micro-USD = %v, quer %d", sp.Attributes[cost.AttrCostMicroUSD], perCall)
	}
	if _, ok := sp.Attributes[cost.AttrCostUSD].(float64); !ok {
		t.Fatalf("span sem gen_ai.usage.cost_usd (AOS-062)")
	}
	// Custo E atribuição partilham o mesmo modelo/região no MESMO span (ligação
	// custo->principal via modelo/região/trajectória, não em spans separados).
	if sp.Attributes[agentruntime.AttrRequestModel] != "gpt-x" {
		t.Fatalf("modelo divergente no span: %v", sp.Attributes[agentruntime.AttrRequestModel])
	}
	if sp.Attributes[cost.AttrRegion] != "eu" || sp.Attributes[attribution.AttrRegion] != "eu" {
		t.Fatalf("região do custo/atribuição divergente: cost=%v attr=%v",
			sp.Attributes[cost.AttrRegion], sp.Attributes[attribution.AttrRegion])
	}
}

// TestGateway_Cost_NoPrice_FailClosed prova que um (modelo, região) sem preço
// FALHA-FECHA a chamada síncrona (custo não-calculável = erro atribuível, não 0).
func TestGateway_Cost_NoPrice_FailClosed(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("sem-preco", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 100, CompletionTokens: 50},
	})
	rec := cost.NewRecorder(cost.NewCalculator(costTable(t)))
	tr := &agentruntime.RecordingTracer{}
	gw := newGateway(t, f, modelgateway.WithCost(rec), modelgateway.WithTracer(tr))

	_, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "sem-preco", Board: "b", RunID: "r",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if !errors.Is(err, pricing.ErrNoPrice) {
		t.Fatalf("esperava fail-closed ErrNoPrice, obtive %v", err)
	}
	// O span leva o error.type = cost_error.
	sp := tr.SpansByOperation(agentruntime.OpChat)[0]
	if sp.Attributes[agentruntime.AttrErrorType] != "cost_error" {
		t.Fatalf("error.type = %v, quer cost_error", sp.Attributes[agentruntime.AttrErrorType])
	}
	// Nada agregado (nunca 0 silencioso).
	if _, ok := rec.CostForRun(cost.RunKey{RunID: "r", Tenant: "b"}); ok {
		t.Fatalf("nao devia agregar um custo nao-calculavel")
	}
}

// TestGateway_Cost_Streaming prova que o custo corre no fim do streaming (com o usage
// final, incl. cache read/write) e agrega.
func TestGateway_Cost_Streaming(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500, CacheReadTokens: 200, CacheWriteTokens: 100},
	})
	rec := cost.NewRecorder(cost.NewCalculator(costTable(t)))
	gw := newGateway(t, f, modelgateway.WithCost(rec))

	stream, err := gw.ChatStream(context.Background(), port.ChatRequest{
		Model: "m", Board: "board-eu", RunID: "run-s", TreeID: "tree-s",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	for {
		if _, err := stream.Recv(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("Recv: %v", err)
		}
	}
	_ = stream.Close()

	agg, ok := rec.CostForRun(cost.RunKey{RunID: "run-s", Tenant: "board-eu"})
	if !ok || agg.CostMicroUSD != 10335 {
		t.Fatalf("custo do streaming nao agregado: %+v ok=%v", agg, ok)
	}
}

// TestGateway_Cost_Embeddings prova que o custo também é contabilizado em embeddings.
func TestGateway_Cost_Embeddings(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetEmbeddingsResponse("m", port.EmbeddingsResponse{
		Object: "list",
		Data:   []port.Embedding{{Index: 0, Object: "embedding", Embedding: []float64{0.1}}},
		Usage:  port.Usage{PromptTokens: 1000},
	})
	rec := cost.NewRecorder(cost.NewCalculator(costTable(t)))
	gw := newGateway(t, f, modelgateway.WithCost(rec))

	if _, err := gw.Embeddings(context.Background(), port.EmbeddingsRequest{
		Model: "m", Board: "board-eu", RunID: "run-e", TreeID: "tree-e", Input: []string{"x"},
	}); err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	// custo = 1000*3_000_000/1e6 = 3000 micro-USD (sem completion/cache).
	agg, ok := rec.CostForRun(cost.RunKey{RunID: "run-e", Tenant: "board-eu"})
	if !ok || agg.CostMicroUSD != 3000 {
		t.Fatalf("custo de embeddings errado: %+v ok=%v", agg, ok)
	}
}
