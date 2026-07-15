package modelgateway_test

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/metering/cache_sli"
	"github.com/aos-ref/platform/model-gateway/port"
)

// TestGateway_CacheSLI_PerCall_Aggregation_Alert é o teste de integração de
// AOS-061 pela fachada: o GW calcula o cache-hit-rate por chamada a partir do
// usage do provider, agrega por run/tenant e dispara alerta abaixo do limiar.
func TestGateway_CacheSLI_PerCall_Aggregation_Alert(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	// Resposta com cache HIT alto (95/100).
	f.SetChatResponse("hit", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 100, CompletionTokens: 5, TotalTokens: 105, CacheReadTokens: 95, CacheWriteTokens: 5},
	})
	// Resposta com cache MISS total (prefixo quebrado -> 0 read).
	f.SetChatResponse("miss", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 300, CompletionTokens: 5, TotalTokens: 305, CacheReadTokens: 0, CacheWriteTokens: 300},
	})

	alerts := &cache_sli.MemoryAlertSink{}
	metrics := &cache_sli.MemoryMetricSink{}
	rec := cache_sli.NewRecorder(
		cache_sli.WithAlertSink(alerts),
		cache_sli.WithMetricSink(metrics),
	)
	gw := newGateway(t, f, modelgateway.WithCacheSLI(rec))
	ctx := context.Background()

	// Duas chamadas HIT no mesmo run/tenant (board = tenant): SLI saudável.
	for i := 0; i < 2; i++ {
		if _, err := gw.Chat(ctx, port.ChatRequest{
			Model: "hit", Board: "board-eu", RunID: "run-1",
			Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
		}); err != nil {
			t.Fatalf("chat hit: %v", err)
		}
	}
	key := cache_sli.Key{RunID: "run-1", Tenant: "board-eu"}
	if rate, defined := rec.RateFor(key); !defined || rate < cache_sli.DefaultThreshold {
		t.Fatalf("pre-quebra: SLI devia estar saudavel, got %v (defined=%v)", rate, defined)
	}
	if alerts.Len() != 0 {
		t.Fatalf("pre-quebra: nao devia haver alerta, len=%d", alerts.Len())
	}

	// Uma chamada MISS no mesmo run: arrasta o SLI agregado abaixo de 80% -> alerta.
	if _, err := gw.Chat(ctx, port.ChatRequest{
		Model: "miss", Board: "board-eu", RunID: "run-1",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}); err != nil {
		t.Fatalf("chat miss: %v", err)
	}
	if rate, _ := rec.RateFor(key); rate >= cache_sli.DefaultThreshold {
		t.Fatalf("pos-quebra: SLI devia descer abaixo de 80%%, got %v", rate)
	}
	if alerts.Len() != 1 {
		t.Fatalf("pos-quebra: esperava 1 alerta, len=%d", alerts.Len())
	}

	// Isolamento: outro tenant no mesmo run não é contaminado.
	if _, defined := rec.RateFor(cache_sli.Key{RunID: "run-1", Tenant: "outro"}); defined {
		t.Errorf("tenant distinto nao devia existir (isolamento)")
	}
	if len(metrics.Metrics()) == 0 {
		t.Errorf("esperava metricas OTel emitidas")
	}
}

// TestGateway_CacheSLI_SpanAnnotation confirma que o span 'chat' leva o
// cache-hit-rate da chamada (ligação à trajectória, sem segredo).
func TestGateway_CacheSLI_SpanAnnotation(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 100, CompletionTokens: 5, TotalTokens: 105, CacheReadTokens: 90},
	})
	tr := &agentruntime.RecordingTracer{}
	rec := cache_sli.NewRecorder()
	gw := newGateway(t, f, modelgateway.WithCacheSLI(rec), modelgateway.WithTracer(tr))
	if _, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Board: "b", RunID: "r", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	spans := tr.SpansByOperation(agentruntime.OpChat)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span chat, got %d", len(spans))
	}
	if got := spans[0].Attributes[cache_sli.AttrCallCacheHitRate]; got != 0.9 {
		t.Errorf("span sem cache-hit-rate da chamada: got %v, quer 0.9", got)
	}
}

// TestGateway_CacheSLI_Streaming confirma que o SLI corre no fim do stream com o
// usage final (cache read/write do chunk final), não sobre zero tokens.
func TestGateway_CacheSLI_Streaming(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 100, CompletionTokens: 5, TotalTokens: 105, CacheReadTokens: 40},
	})
	alerts := &cache_sli.MemoryAlertSink{}
	rec := cache_sli.NewRecorder(cache_sli.WithAlertSink(alerts))
	gw := newGateway(t, f, modelgateway.WithCacheSLI(rec))

	stream, err := gw.ChatStream(context.Background(), port.ChatRequest{
		Model: "m", Board: "b", RunID: "r", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	// Drena o stream: o metering/SLI corre no EOF com o usage final.
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	_ = stream.Close()

	rate, defined := rec.RateFor(cache_sli.Key{RunID: "r", Tenant: "b"})
	if !defined || rate != 0.4 {
		t.Fatalf("SLI do stream = %v (defined=%v), quer 0.4", rate, defined)
	}
	// 40% < 80% -> alerta.
	if alerts.Len() != 1 {
		t.Errorf("stream abaixo do limiar devia alertar, len=%d", alerts.Len())
	}
}
